package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// seedClaimsStore opens a fresh SQLite store at <root>/.leonard/leonard.db
// and inserts the supplied claims. Mirrors seedStore in session_start_test.go
// but for the claims table. The returned *store.Store satisfies
// hooks.ClaimsReader directly so the caller may also use it for assertions.
func seedClaimsStore(t *testing.T, root string, claims []store.Claim) *store.Store {
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
	for _, c := range claims {
		if _, err := s.RecordClaim(c); err != nil {
			t.Fatalf("seed claim %q: %v", c.Claim, err)
		}
	}
	return s
}

func runStopCmd(t *testing.T, payload []byte) (hooks.StopResponse, string, error) {
	t.Helper()
	cmd := newStopCmd()
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())

	if err := cmd.Execute(); err != nil {
		return hooks.StopResponse{}, out.String(), err
	}
	var resp hooks.StopResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return hooks.StopResponse{}, out.String(), err
	}
	return resp, out.String(), nil
}

func encodeStopPayload(t *testing.T, p hooks.StopPayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestStopCmd_SurfacesSeededUnverifiedClaims(t *testing.T) {
	root := t.TempDir()
	seedClaimsStore(t, root, []store.Claim{
		{SessionID: "sess-1", Claim: "Renamed Foo to Bar in pkg/api.", Evidence: "git diff", Verified: false, RecordedAt: 100},
		{SessionID: "sess-1", Claim: "Added retry logic to fetch().", Evidence: "see fetch.go", Verified: false, RecordedAt: 200},
		{SessionID: "sess-other", Claim: "Wrong session — must be filtered out.", Verified: false, RecordedAt: 300},
	})
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{
		SessionID:     "sess-1",
		HookEventName: "Stop",
		CWD:           root,
	})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if !resp.Continue {
		t.Error("Continue must be true — Stop hook is advisory")
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected surfacing, got nil hookSpecificOutput; stdout=%s", raw)
	}
	if resp.HookSpecificOutput.HookEventName != "Stop" {
		t.Errorf("hookEventName = %q", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"Renamed Foo to Bar", "Added retry logic"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("surfaced context missing %q\n%s", want, ctx)
		}
	}
	if strings.Contains(ctx, "Wrong session") {
		t.Errorf("session filter leaked unrelated claim:\n%s", ctx)
	}
}

func TestStopCmd_VerifiedOnlyEmitsNoSurfacing(t *testing.T) {
	root := t.TempDir()
	seedClaimsStore(t, root, []store.Claim{
		{SessionID: "sess-1", Claim: "Already verified.", Verified: true, RecordedAt: 100},
	})
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{SessionID: "sess-1", HookEventName: "Stop"})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no surfacing when all claims verified, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
}

func TestStopCmd_EmptyStoreEmitsNoSurfacing(t *testing.T) {
	root := t.TempDir()
	seedClaimsStore(t, root, nil) // creates the DB, records zero claims
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{HookEventName: "Stop"})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no surfacing for empty claims table, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
}

func TestStopCmd_MissingStoreEmitsNoSurfacing(t *testing.T) {
	root := t.TempDir()
	// Create .leonard/ so resolveProjectRoot picks this directory, but skip
	// the DB file — the brief mandates this case is a graceful no-op so a
	// fresh checkout doesn't fail the session stop.
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{HookEventName: "Stop"})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no surfacing for missing store, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Error("Continue should be true even when store is missing")
	}
}

func TestStopCmd_ConfigCapHonored(t *testing.T) {
	root := t.TempDir()
	rows := make([]store.Claim, 0, 5)
	for i := 1; i <= 5; i++ {
		rows = append(rows, store.Claim{
			SessionID:  "sess-1",
			Claim:      "claim-" + string(rune('a'+i-1)),
			Verified:   false,
			RecordedAt: int64(i),
		})
	}
	seedClaimsStore(t, root, rows)

	cfg := config.Default()
	cfg.Hooks.SurfaceUnverifiedClaimsAtStop = 2
	if err := config.Save(cfg, filepath.Join(root, dataDirName, config.Filename)); err != nil {
		t.Fatalf("save config: %v", err)
	}
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{SessionID: "sess-1", HookEventName: "Stop"})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected surfacing; stdout=%s", raw)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if got := strings.Count(ctx, "\n- "); got != 2 {
		t.Errorf("expected exactly 2 bullets at cap=2, got %d:\n%s", got, ctx)
	}
	// GetUnverifiedClaims orders newest-first, so claim-e and claim-d are kept.
	if !strings.Contains(ctx, "claim-e") || !strings.Contains(ctx, "claim-d") {
		t.Errorf("expected top-two claims claim-e and claim-d:\n%s", ctx)
	}
	for _, unwanted := range []string{"claim-a", "claim-b", "claim-c"} {
		if strings.Contains(ctx, unwanted) {
			t.Errorf("cap not respected — found %q in: %s", unwanted, ctx)
		}
	}
}

func TestStopCmd_DefaultCapTwentyWhenConfigAbsent(t *testing.T) {
	root := t.TempDir()
	// Seed 25 unverified claims with no config.toml — default cap of 20
	// must apply, dropping the five oldest.
	rows := make([]store.Claim, 0, 25)
	for i := 1; i <= 25; i++ {
		rows = append(rows, store.Claim{
			SessionID:  "sess-1",
			Claim:      "row",
			Verified:   false,
			RecordedAt: int64(i),
		})
	}
	seedClaimsStore(t, root, rows)
	chdir(t, root)

	payload := encodeStopPayload(t, hooks.StopPayload{SessionID: "sess-1", HookEventName: "Stop"})
	resp, raw, err := runStopCmd(t, payload)
	if err != nil {
		t.Fatalf("execute: %v\nstdout=%s", err, raw)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected surfacing; stdout=%s", raw)
	}
	if got := strings.Count(resp.HookSpecificOutput.AdditionalContext, "\n- "); got != hooks.DefaultStopClaimLimit {
		t.Errorf("expected default cap %d, got %d", hooks.DefaultStopClaimLimit, got)
	}
}

func TestStopCmd_StubOpenerErrorBubblesUp(t *testing.T) {
	cmd := newStopCmdWithOpener(func(string) (hooks.ClaimsReader, error) {
		return nil, errors.New("disk on fire")
	})
	cmd.SetIn(bytes.NewReader(encodeStopPayload(t, hooks.StopPayload{HookEventName: "Stop"})))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("expected open-store error to bubble up, got %v", err)
	}
}

func TestStopCmd_EmptyStdinErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, root)

	cmd := newStopCmd()
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
