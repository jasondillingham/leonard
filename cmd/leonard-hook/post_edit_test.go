package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

type recordedIndex struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordedIndex) IndexFile(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, path)
	return nil
}

type recordedClaims struct {
	mu   sync.Mutex
	rows []hookClaim
}

type hookClaim struct {
	SessionID string
	Claim     string
	Evidence  string
	Verified  bool
}

func (r *recordedClaims) RecordClaim(sessionID, claim, evidence string, verified bool) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, hookClaim{sessionID, claim, evidence, verified})
	return int64(len(r.rows)), nil
}

type stubBackendWithSpies struct {
	idx    *recordedIndex
	claims *recordedClaims
}

func (s stubBackendWithSpies) Indexer(string) hooks.Indexer      { return s.idx }
func (s stubBackendWithSpies) Claims(string) hooks.ClaimRecorder { return s.claims }
func (s stubBackendWithSpies) Close() error                       { return nil }

func TestPostEditCmd_RoundTrip(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	payload, err := json.Marshal(hooks.PostToolUsePayload{
		SessionID:     "sess-abc",
		HookEventName: "PostToolUse",
		ToolName:      "Edit",
		ToolInput:     hooks.ToolInput{FilePath: target},
		CWD:           root,
	})
	if err != nil {
		t.Fatal(err)
	}

	spies := stubBackendWithSpies{idx: &recordedIndex{}, claims: &recordedClaims{}}
	cmd := newRootCmd(spies)
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"post-edit"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("post-edit: %v\nout=%s", err, out.String())
	}

	if got := spies.idx.calls; len(got) != 1 || got[0] != target {
		t.Fatalf("Indexer calls = %v, want [%s]", got, target)
	}
	if len(spies.claims.rows) != 1 {
		t.Fatalf("claims rows = %d", len(spies.claims.rows))
	}
	row := spies.claims.rows[0]
	if row.SessionID != "sess-abc" {
		t.Errorf("sessionID = %q", row.SessionID)
	}
	if !strings.Contains(row.Claim, "go vet=ok") {
		t.Errorf("claim summary = %q", row.Claim)
	}

	var resp hooks.HookResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%s", err, out.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
}

func TestPostEditCmd_StdinErrorBubblesUp(t *testing.T) {
	spies := stubBackendWithSpies{idx: &recordedIndex{}, claims: &recordedClaims{}}
	cmd := newRootCmd(spies)
	cmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"post-edit"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}

func TestResolveProjectRoot_WalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	got, err := resolveProjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("resolveProjectRoot = %q, want %q", got, root)
	}
}
