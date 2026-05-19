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
	mu         sync.Mutex
	rows       []hooks.ClaimRecord
	supersedes []hookSupersede
}

type hookSupersede struct {
	FilePath           string
	SupersedingClaimID int64
}

func (r *recordedClaims) RecordClaim(rec hooks.ClaimRecord) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rec)
	return int64(len(r.rows)), nil
}

func (r *recordedClaims) SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supersedes = append(r.supersedes, hookSupersede{filePath, supersedingClaimID})
	return 0, nil
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
	// Touch the SQLite file so the cobra layer doesn't take its
	// missing-DB short-circuit and skip the handler entirely. The stub
	// Backend in this test never opens the file, so empty is fine.
	if err := os.WriteFile(filepath.Join(dataDir, "leonard.db"), nil, 0o644); err != nil {
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
	// Seed an empty .leonard/leonard.db so the cobra layer doesn't take its
	// missing-DB short-circuit before reaching the handler — we need to
	// reach the decode-error path.
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "leonard.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, root)

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
	// F2: decode errors on post-edit must map to exit code 2 so Claude
	// Code treats the failure as blocking. Exit 1 would be non-blocking
	// and let the unverified write through.
	if got := exitCodeFor(err); got != 2 {
		t.Errorf("decode error exit code = %d, want 2 (block)", got)
	}
}

// F3 reproducer: a fresh checkout that hasn't run `leonard init` has no
// .leonard/leonard.db. The pre-fix behavior was to panic with a Go stack
// trace via mustOpenStore; the fix is to short-circuit at the cobra layer
// and emit a no-op {"continue":true} on stdout with an operator hint on
// stderr.
func TestPostEditCmd_MissingDBNoOps(t *testing.T) {
	root := t.TempDir()
	chdir(t, root) // No .leonard/ — resolveProjectRoot returns cwd unchanged.

	payload, err := json.Marshal(hooks.PostToolUsePayload{
		SessionID:     "sess-fresh",
		HookEventName: "PostToolUse",
		ToolName:      "Edit",
		ToolInput:     hooks.ToolInput{FilePath: filepath.Join(root, "x.go")},
		CWD:           root,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Use the production realBackend so the test actually exercises the
	// codepath that previously hit mustOpenStore's panic.
	cmd := newRootCmd(newDefaultBackend())
	cmd.SetIn(bytes.NewReader(payload))
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"post-edit"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no-op success, got error: %v\nstderr=%s", err, stderr.String())
	}
	if got := exitCodeFor(nil); got != 0 {
		t.Errorf("expected exit code 0 path, got %d", got)
	}
	var resp hooks.HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Errorf("Continue should be true on missing-DB no-op, got %+v", resp)
	}
	if !strings.Contains(stderr.String(), "leonard init") {
		t.Errorf("stderr should hint at `leonard init`: %q", stderr.String())
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
