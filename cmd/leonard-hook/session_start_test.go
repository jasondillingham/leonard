package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// seedStore opens a fresh SQLite store at <root>/.leonard/leonard.db and
// inserts the supplied decisions. Returns the *store.Store (which satisfies
// hooks.DecisionReader directly) so the caller can also use it for assertions.
func seedStore(t *testing.T, root string, decisions []store.Decision) *store.Store {
	t.Helper()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir .leonard: %v", err)
	}
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, d := range decisions {
		if _, err := s.RecordDecision(d); err != nil {
			t.Fatalf("seed decision %q: %v", d.Topic, err)
		}
	}
	return s
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func runSessionStartCmd(t *testing.T, payload []byte) (hooks.SessionStartResponse, string, error) {
	t.Helper()
	cmd := newSessionStartCmd()
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())

	if err := cmd.Execute(); err != nil {
		return hooks.SessionStartResponse{}, out.String(), err
	}
	var resp hooks.SessionStartResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return hooks.SessionStartResponse{}, out.String(), err
	}
	return resp, out.String(), nil
}

func encodePayload(t *testing.T, p hooks.SessionStartPayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestSessionStartCmd_InjectsSeededDecisions(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []store.Decision{
		{Topic: "naming", Choice: "snake_case", Reasoning: "matches existing dataset"},
		{Topic: "toml lib", Choice: "pelletier/go-toml/v2", Reasoning: "actively maintained"},
	})
	chdir(t, root)

	payload := encodePayload(t, hooks.SessionStartPayload{
		SessionID:     "sess-1",
		HookEventName: "SessionStart",
		Source:        "startup",
		CWD:           root,
	})
	resp, raw, err := runSessionStartCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected injection, got nil hookSpecificOutput; stdout=%s", raw)
	}
	if resp.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"toml lib", "pelletier/go-toml/v2", "naming", "snake_case"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("injected context missing %q\n%s", want, ctx)
		}
	}
}

func TestSessionStartCmd_MissingStoreEmitsNoInjection(t *testing.T) {
	root := t.TempDir()
	// Create .leonard/ so resolveProjectRoot picks this directory, but skip
	// the DB file — the brief mandates this case is a graceful no-op.
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdir(t, root)

	payload := encodePayload(t, hooks.SessionStartPayload{HookEventName: "SessionStart"})
	resp, raw, err := runSessionStartCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection for missing store, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Error("Continue should be true even when store is missing")
	}
}

func TestSessionStartCmd_EmptyStoreEmitsNoInjection(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, nil) // creates the DB, records zero decisions
	chdir(t, root)

	payload := encodePayload(t, hooks.SessionStartPayload{HookEventName: "SessionStart"})
	resp, raw, err := runSessionStartCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection for empty decisions table, got %+v", resp.HookSpecificOutput)
	}
}

func TestSessionStartCmd_ConfigLimitHonored(t *testing.T) {
	root := t.TempDir()
	// Seed five decisions, then cap at 2 via config.
	seedStore(t, root, []store.Decision{
		{Topic: "a", Choice: "1", Reasoning: "x", RecordedAt: 100},
		{Topic: "b", Choice: "2", Reasoning: "x", RecordedAt: 200},
		{Topic: "c", Choice: "3", Reasoning: "x", RecordedAt: 300},
		{Topic: "d", Choice: "4", Reasoning: "x", RecordedAt: 400},
		{Topic: "e", Choice: "5", Reasoning: "x", RecordedAt: 500},
	})
	cfg := config.Default()
	cfg.Hooks.InjectDecisionsAtSessionStart = 2
	if err := config.Save(cfg, filepath.Join(root, dataDirName, config.Filename)); err != nil {
		t.Fatalf("save config: %v", err)
	}
	chdir(t, root)

	payload := encodePayload(t, hooks.SessionStartPayload{HookEventName: "SessionStart"})
	resp, raw, err := runSessionStartCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected injection; stdout=%s", raw)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	// GetDecisions orders newest-first, so the top two are e and d.
	if !strings.Contains(ctx, "**e**") || !strings.Contains(ctx, "**d**") {
		t.Errorf("expected top-two decisions e and d, got: %s", ctx)
	}
	for _, unwanted := range []string{"**a**", "**b**", "**c**"} {
		if strings.Contains(ctx, unwanted) {
			t.Errorf("limit not respected — found %q in: %s", unwanted, ctx)
		}
	}
}

func TestSessionStartCmd_DefaultLimitTen(t *testing.T) {
	root := t.TempDir()
	// Seed eleven decisions; default limit of 10 should drop the oldest.
	rows := make([]store.Decision, 0, 11)
	for i := 1; i <= 11; i++ {
		rows = append(rows, store.Decision{
			Topic:      fmt.Sprintf("t%02d", i),
			Choice:     "c",
			Reasoning:  "r",
			RecordedAt: int64(i),
		})
	}
	seedStore(t, root, rows)
	chdir(t, root)

	payload := encodePayload(t, hooks.SessionStartPayload{HookEventName: "SessionStart"})
	resp, raw, err := runSessionStartCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected injection; stdout=%s", raw)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if strings.Contains(ctx, "**t01**") {
		t.Errorf("default limit of 10 should have excluded t01:\n%s", ctx)
	}
	if !strings.Contains(ctx, "**t11**") {
		t.Errorf("newest decision t11 missing:\n%s", ctx)
	}
	// Bullet lines start with "- **" — count occurrences.
	if got := strings.Count(ctx, "- **"); got != 10 {
		t.Errorf("expected exactly 10 bullets, got %d:\n%s", got, ctx)
	}
}

func TestSessionStartCmd_StubOpenerErrorBubblesUp(t *testing.T) {
	cmd := newSessionStartCmdWithOpener(func(string) (hooks.DecisionReader, error) {
		return nil, errors.New("disk on fire")
	})
	cmd.SetIn(bytes.NewReader(encodePayload(t, hooks.SessionStartPayload{HookEventName: "SessionStart"})))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("expected open-store error to bubble up, got %v", err)
	}
}

func TestSessionStartCmd_EmptyStdinErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, root)

	cmd := newSessionStartCmd()
	cmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}
