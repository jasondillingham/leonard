package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeIndexer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeIndexer) IndexFile(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	return f.err
}

func (f *fakeIndexer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

type recordedClaim struct {
	SessionID string
	Claim     string
	Evidence  string
	Verified  bool
}

type fakeClaims struct {
	mu       sync.Mutex
	rows     []recordedClaim
	nextID   int64
	recordErr error
}

func (f *fakeClaims) RecordClaim(sessionID, claim, evidence string, verified bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	f.nextID++
	f.rows = append(f.rows, recordedClaim{SessionID: sessionID, Claim: claim, Evidence: evidence, Verified: verified})
	return f.nextID, nil
}

func (f *fakeClaims) Rows() []recordedClaim {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedClaim, len(f.rows))
	copy(out, f.rows)
	return out
}

func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
}

func encodePayload(t *testing.T, p PostToolUsePayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestHandlePostEdit_VetPasses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	target := filepath.Join(root, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID:     "sess-1",
		HookEventName: "PostToolUse",
		ToolName:      "Edit",
		ToolInput:     ToolInput{FilePath: target},
		CWD:           root,
	}))
	var stdout bytes.Buffer

	stubVet := func(ctx context.Context, dir string) (string, error) {
		if dir != root {
			t.Errorf("vet run in %q want %q", dir, root)
		}
		return "", nil
	}

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: idx,
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}

	if got := idx.Calls(); len(got) != 1 || got[0] != target {
		t.Fatalf("Indexer calls = %v, want [%s]", got, target)
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !row.Verified {
		t.Errorf("claim should be verified")
	}
	if row.SessionID != "sess-1" {
		t.Errorf("sessionID = %q", row.SessionID)
	}
	if !strings.Contains(row.Claim, "go vet=ok") {
		t.Errorf("claim summary missing vet status: %q", row.Claim)
	}
	if !strings.Contains(row.Evidence, "go vet: ok") {
		t.Errorf("evidence missing vet status: %q", row.Evidence)
	}

	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
	if !strings.Contains(resp.SystemMessage, "go vet ok") {
		t.Errorf("system message = %q", resp.SystemMessage)
	}
}

func TestHandlePostEdit_VetFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-2",
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: filepath.Join(root, "broken.go")},
		CWD:       root,
	}))
	var stdout bytes.Buffer

	stubVet := func(ctx context.Context, dir string) (string, error) {
		return "broken.go:1: nope", errors.New("exit status 1")
	}

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: idx,
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}

	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	row := rows[0]
	if row.Verified {
		t.Error("vet failure should produce verified=false")
	}
	if !strings.Contains(row.Evidence, "broken.go:1: nope") {
		t.Errorf("evidence missing vet output: %q", row.Evidence)
	}
	if !strings.Contains(row.Evidence, "exit status 1") {
		t.Errorf("evidence missing exit error: %q", row.Evidence)
	}
}

func TestHandlePostEdit_SkipsVetWithoutGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // no go.mod
	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-3",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: filepath.Join(root, "doc.md")},
		CWD:       root,
	}))
	var stdout bytes.Buffer

	vetCalled := false
	stubVet := func(ctx context.Context, dir string) (string, error) {
		vetCalled = true
		return "should not run", nil
	}

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: idx,
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	if vetCalled {
		t.Error("vet should not run without go.mod")
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	if !rows[0].Verified {
		t.Error("non-Go project should produce verified=true (nothing to check)")
	}
	if !strings.Contains(rows[0].Claim, "go vet=skipped") {
		t.Errorf("claim summary = %q", rows[0].Claim)
	}
}

func TestHandlePostEdit_IndexFailureRecorded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	idx := &fakeIndexer{err: errors.New("parse blew up")}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-4",
		ToolInput: ToolInput{FilePath: filepath.Join(root, "x.go")},
		CWD:       root,
	}))
	var stdout bytes.Buffer

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: idx,
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	if rows[0].Verified {
		t.Error("index failure should leave verified=false")
	}
	if !strings.Contains(rows[0].Evidence, "parse blew up") {
		t.Errorf("evidence missing index error: %q", rows[0].Evidence)
	}
}

func TestHandlePostEdit_MissingFilePath(t *testing.T) {
	t.Parallel()
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-5",
		ToolName:  "Edit",
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  &fakeClaims{},
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "file_path") {
		t.Fatalf("expected file_path error, got %v", err)
	}
}

func TestHandlePostEdit_EmptyStdin(t *testing.T) {
	t.Parallel()
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  &fakeClaims{},
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}

func TestHandlePostEdit_RecordClaimErrorIsFatal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	claims := &fakeClaims{recordErr: errors.New("disk full")}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-6",
		ToolInput: ToolInput{FilePath: filepath.Join(root, "x.go")},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected record claim error, got %v", err)
	}
}

func TestHandlePostEdit_EvidenceTruncated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	long := strings.Repeat("A", 100_000)
	stubVet := func(ctx context.Context, dir string) (string, error) { return long, errors.New("exit 1") }
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-7",
		ToolInput: ToolInput{FilePath: filepath.Join(root, "x.go")},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer:     &fakeIndexer{},
		Claims:      claims,
		Vet:         stubVet,
		EvidenceCap: 512,
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	if len(rows[0].Evidence) > 512 {
		t.Errorf("evidence length %d exceeds cap 512", len(rows[0].Evidence))
	}
	if !strings.HasSuffix(rows[0].Evidence, "(truncated)") {
		t.Errorf("evidence missing truncation marker: %q", rows[0].Evidence[len(rows[0].Evidence)-40:])
	}
}

func TestRunGoVet_RealCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := RunGoVet(ctx, root)
	if err != nil {
		t.Fatalf("RunGoVet: %v (output=%q)", err, out)
	}
}
