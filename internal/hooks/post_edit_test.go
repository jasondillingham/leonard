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

type supersedeCall struct {
	FilePath           string
	SupersedingClaimID int64
}

type fakeClaims struct {
	mu                   sync.Mutex
	rows                 []ClaimRecord
	nextID               int64
	recordErr            error
	supersedeCalls       []supersedeCall
	supersedeErr         error
	broadSupersedeCount  int
}

func (f *fakeClaims) RecordClaim(rec ClaimRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	f.nextID++
	f.rows = append(f.rows, rec)
	return f.nextID, nil
}

func (f *fakeClaims) SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.supersedeErr != nil {
		return 0, f.supersedeErr
	}
	f.supersedeCalls = append(f.supersedeCalls, supersedeCall{FilePath: filePath, SupersedingClaimID: supersedingClaimID})
	return 0, nil
}

func (f *fakeClaims) SupersedeOutstandingFailures(supersedingClaimID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.supersedeErr != nil {
		return 0, f.supersedeErr
	}
	// Record under file_path="" to distinguish from file-scoped calls
	// in tests that inspect the SupersedeCalls log.
	f.supersedeCalls = append(f.supersedeCalls, supersedeCall{FilePath: "", SupersedingClaimID: supersedingClaimID})
	return f.broadSupersedeCount, nil
}

func (f *fakeClaims) Rows() []ClaimRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ClaimRecord, len(f.rows))
	copy(out, f.rows)
	return out
}

func (f *fakeClaims) SupersedeCalls() []supersedeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]supersedeCall, len(f.supersedeCalls))
	copy(out, f.supersedeCalls)
	return out
}

// seed creates an empty file at path so HandlePostEdit's stat-check (the
// hooks F5 missing-file guard) doesn't short-circuit. Tests that exercise
// index/vet logic only care that PostToolUse fired against a touched file —
// the contents are irrelevant because the indexer and vet are stubbed.
func seed(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed file %s: %v", path, err)
	}
	return path
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
	// Green run must NOT emit a systemMessage (bughunt-12 F042: per-edit
	// "vet ok" noise clutters the session surface on every clean edit).
	if resp.SystemMessage != "" {
		t.Errorf("clean run should not emit systemMessage, got %q", resp.SystemMessage)
	}
	// Green run must NOT inject context — a noisy "everything fine" on every
	// edit would drown out the failure-path signal that actually matters.
	if resp.HookSpecificOutput != nil {
		t.Errorf("green run should not emit hookSpecificOutput, got %+v", resp.HookSpecificOutput)
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
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "broken.go"))},
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

	// hooks F4: vet failure must reach the model via additionalContext, not
	// just systemMessage (which Claude never sees). Without this, the whole
	// "false done claim" guard becomes invisible to the agent.
	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("vet failure must populate hookSpecificOutput.additionalContext")
	}
	if resp.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", resp.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "go vet FAILED") {
		t.Errorf("additionalContext missing vet failure callout: %q", resp.HookSpecificOutput.AdditionalContext)
	}
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "broken.go:1: nope") {
		t.Errorf("additionalContext should include first vet error: %q", resp.HookSpecificOutput.AdditionalContext)
	}
}

func TestHandlePostEdit_IndexFailureReachesModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	idx := &fakeIndexer{err: errors.New("parse blew up")}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-idx-ctx",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "x.go"))},
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
	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("index failure must populate hookSpecificOutput.additionalContext")
	}
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "parse blew up") {
		t.Errorf("additionalContext missing index error: %q", resp.HookSpecificOutput.AdditionalContext)
	}
}

func TestHandlePostEdit_NoModelContextWhenVetSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // no go.mod → vet skipped, nothing to surface
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-noctx",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "README.md"))},
		CWD:       root,
	}))
	var stdout bytes.Buffer
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("vet-skipped run should not surface to the model, got %+v", resp.HookSpecificOutput)
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
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "doc.md"))},
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
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "x.go"))},
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
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "x.go"))},
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
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "x.go"))},
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

func TestVetErrorSummary(t *testing.T) {
	t.Parallel()
	tru := true
	_ = tru
	cases := []struct {
		name string
		vet  VetResult
		want string
	}{
		{name: "vet skipped", vet: VetResult{Ran: false}, want: ""},
		{name: "vet passed", vet: VetResult{Ran: true, Passed: true, Output: ""}, want: ""},
		{
			name: "fail with project error",
			vet: VetResult{Ran: true, Passed: false,
				Output:  "# example.com/project\n# [example.com/project]\nvet: project.go:10: declared and not used: foo",
				ExitErr: "exit status 1"},
			want: "vet: project.go:10: declared and not used: foo",
		},
		{
			name: "fail with only headers falls back to exit error",
			vet: VetResult{Ran: true, Passed: false,
				Output:  "# example.com/project\n# [example.com/project]",
				ExitErr: "exit status 1"},
			want: "exit status 1",
		},
		{
			name: "long error truncated",
			vet: VetResult{Ran: true, Passed: false,
				Output:  "vet: " + strings.Repeat("x", 500),
				ExitErr: "exit 1"},
			want: ("vet: " + strings.Repeat("x", 500))[:vetErrorSummaryCap],
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := vetErrorSummary(tc.vet)
			if got != tc.want {
				t.Errorf("vetErrorSummary:\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

func TestHandlePostEdit_PopulatesStructuredFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	target := seed(t, filepath.Join(root, "broken.go"))
	stubVet := func(ctx context.Context, dir string) (string, error) {
		return "# example.com/project\nvet: broken.go:1: declared and not used: foo", errors.New("exit 1")
	}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-struct",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: target},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Tool != "Edit" {
		t.Errorf("Tool = %q, want Edit", r.Tool)
	}
	if r.IndexOK == nil || !*r.IndexOK {
		t.Errorf("IndexOK = %v, want pointer to true", r.IndexOK)
	}
	if r.VetOK == nil || *r.VetOK {
		t.Errorf("VetOK = %v, want pointer to false", r.VetOK)
	}
	if r.VetErrorSummary != "vet: broken.go:1: declared and not used: foo" {
		t.Errorf("VetErrorSummary = %q", r.VetErrorSummary)
	}
}

func TestHandlePostEdit_VetSkippedLeavesVetOKNil(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // no go.mod
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-nogo",
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "README.md"))},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	rows := claims.Rows()
	if rows[0].VetOK != nil {
		t.Errorf("VetOK = %v, want nil (vet skipped)", rows[0].VetOK)
	}
	if rows[0].IndexOK == nil || !*rows[0].IndexOK {
		t.Errorf("IndexOK = %v, want pointer to true", rows[0].IndexOK)
	}
}

func TestHandlePostEdit_SupersedesOnVetPass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	target := seed(t, filepath.Join(root, "x.go"))
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-sup",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: target},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	// v0.38: vet=ok triggers two supersede calls — file-scoped
	// (SupersedeClaimsForFile, target) and project-wide
	// (SupersedeOutstandingFailures, FilePath=""). Both should
	// reference the same superseding claim ID.
	calls := claims.SupersedeCalls()
	if len(calls) != 2 {
		t.Fatalf("SupersedeCalls = %d, want 2 (file-scoped + project-wide)", len(calls))
	}
	if calls[0].FilePath != target {
		t.Errorf("file-scoped supersede FilePath = %q, want %q", calls[0].FilePath, target)
	}
	if calls[1].FilePath != "" {
		t.Errorf("project-wide supersede FilePath = %q, want empty", calls[1].FilePath)
	}
	for i, c := range calls {
		if c.SupersedingClaimID != 1 {
			t.Errorf("calls[%d].SupersedingClaimID = %d, want 1", i, c.SupersedingClaimID)
		}
	}
	rows := claims.Rows()
	if len(rows) != 1 || rows[0].FilePath != target {
		t.Errorf("recorded FilePath = %q, want %q", rows[0].FilePath, target)
	}
}

func TestHandlePostEdit_DoesNotSupersedeOnVetFail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	stubVet := func(ctx context.Context, dir string) (string, error) {
		return "broken.go:1: nope", errors.New("exit 1")
	}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-fail",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "broken.go"))},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	if calls := claims.SupersedeCalls(); len(calls) != 0 {
		t.Errorf("vet=fail should not trigger supersession, got %d calls", len(calls))
	}
}

func TestHandlePostEdit_DoesNotSupersedeWhenVetSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // no go.mod
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-skip",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "doc.md"))},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     func(ctx context.Context, dir string) (string, error) { return "", nil },
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	if calls := claims.SupersedeCalls(); len(calls) != 0 {
		t.Errorf("vet=skipped should not trigger supersession (file might still be broken in other ways), got %d calls", len(calls))
	}
}

func TestFilterVetNoise(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "no noise passes through",
			in:   "# example.com/x\nx.go:1: real problem",
			want: "# example.com/x\nx.go:1: real problem",
		},
		{
			name: "drops go-m1cpu block",
			in: "# github.com/shoenig/go-m1cpu\n" +
				"/go/pkg/mod/github.com/shoenig/go-m1cpu@v0.1.6/cpu.go:75:17: warning: variable length array folded to constant array as an extension [-Wgnu-folding-constant]\n" +
				"/go/pkg/mod/github.com/shoenig/go-m1cpu@v0.1.6/cpu.go:77:16: warning: variable length array folded to constant array as an extension [-Wgnu-folding-constant]\n" +
				"# example.com/project\n" +
				"# [example.com/project]\n" +
				"vet: project.go:10: declared and not used: foo",
			want: "# example.com/project\n" +
				"# [example.com/project]\n" +
				"vet: project.go:10: declared and not used: foo",
		},
		{
			name: "noise block at EOF",
			in: "# example.com/project\n" +
				"vet: project.go:10: real failure\n" +
				"# github.com/shoenig/go-m1cpu\n" +
				"/path/to/cpu.go:1: warning: noise",
			want: "# example.com/project\nvet: project.go:10: real failure",
		},
		{
			name: "only noise yields empty",
			in: "# github.com/shoenig/go-m1cpu\n" +
				"/path/cpu.go:1: warning: foo\n" +
				"/path/cpu.go:2: warning: bar",
			want: "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := filterVetNoise(tc.in)
			if got != tc.want {
				t.Errorf("filterVetNoise:\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

func TestHandlePostEdit_StripsVetNoise(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	noisy := "# github.com/shoenig/go-m1cpu\n" +
		"/go/pkg/mod/github.com/shoenig/go-m1cpu@v0.1.6/cpu.go:75:17: warning: variable length array folded to constant array as an extension [-Wgnu-folding-constant]\n" +
		"# example.com/x/cmd/app\n" +
		"vet: cmd/app/main.go:1: real error"
	stubVet := func(ctx context.Context, dir string) (string, error) {
		return noisy, errors.New("exit status 1")
	}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-noise",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: seed(t, filepath.Join(root, "main.go"))},
		CWD:       root,
	}))
	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: &fakeIndexer{},
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	if strings.Contains(rows[0].Evidence, "go-m1cpu") {
		t.Errorf("evidence still contains go-m1cpu noise: %q", rows[0].Evidence)
	}
	if !strings.Contains(rows[0].Evidence, "vet: cmd/app/main.go:1: real error") {
		t.Errorf("evidence missing real vet error: %q", rows[0].Evidence)
	}
}

// TestHandlePostEdit_MissingFileSkipsIndex asserts that when Edit/Write
// claims to have modified a path that doesn't actually exist on disk, the
// hook records a "file not found, skipping" claim instead of falsely
// asserting it re-indexed the file. The indexer silently no-ops on missing
// paths, so an unchecked path through HandlePostEdit produces the misleading
// "re-indexed X, go vet reported issues" message documented in hooks F5.
func TestHandlePostEdit_MissingFileSkipsIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)
	missing := filepath.Join(root, "ghost.go") // never created

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	vetCalled := false
	stubVet := func(ctx context.Context, dir string) (string, error) {
		vetCalled = true
		return "", nil
	}

	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID:     "sess-missing",
		HookEventName: "PostToolUse",
		ToolName:      "Edit",
		ToolInput:     ToolInput{FilePath: missing},
		CWD:           root,
	}))
	var stdout bytes.Buffer

	if err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer: idx,
		Claims:  claims,
		Vet:     stubVet,
	}, stdin, &stdout); err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}

	if calls := idx.Calls(); len(calls) != 0 {
		t.Errorf("indexer should not be called on missing file, got %v", calls)
	}
	if vetCalled {
		t.Error("vet should not run when file is missing")
	}

	// v0.39 (bughunt-5 integration F3): missing-file path no
	// longer records a claim — it's a tool-layer signal, not an
	// unverified work claim. Model still gets additionalContext.
	if rows := claims.Rows(); len(rows) != 0 {
		t.Errorf("v0.39: missing-file PostToolUse must NOT record claim rows; got %d", len(rows))
	}

	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true even on missing file")
	}
	msg := strings.ToLower(resp.SystemMessage)
	if !strings.Contains(msg, "file not found") {
		t.Errorf("SystemMessage should say file-not-found, got %q", resp.SystemMessage)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("v0.39: missing-file must populate additionalContext so the model sees the no-op")
	}
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "not on disk") {
		t.Errorf("additionalContext should mention file is not on disk, got %q", resp.HookSpecificOutput.AdditionalContext)
	}
	if strings.Contains(msg, "re-indexed") {
		t.Errorf("SystemMessage must not claim re-indexed for missing file: %q", resp.SystemMessage)
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

// TestHandlePostEdit_RejectsEscapedFilePath covers security-1 F1.
// A crafted PostToolUse payload with file_path resolving outside
// the project root used to flow straight through to IndexFile and
// store foreign symbols as project content. The handler now
// rejects upfront and emits an additionalContext message so the
// model sees the block.
//
// v0.38 removed the claim-record on this path — the rejection is
// a tool-layer decision, not an unverified work claim, and
// recording it polluted Stop's "unverified claims" summary
// forever. The test now asserts (a) the model still gets
// additionalContext, (b) no claim row is created, (c) the indexer
// is never called.
func TestHandlePostEdit_RejectsEscapedFilePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGoMod(t, root)

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stubVet := func(ctx context.Context, dir string) (string, error) {
		t.Error("vet should not run when path is rejected")
		return "", nil
	}

	var lastStdout bytes.Buffer
	for _, attack := range []string{
		"/etc/hosts",        // absolute outside root
		"../../sneaky.go",   // dot-dot escape
		"/tmp/elsewhere.go", // absolute under tmp but not under root
	} {
		attack := attack
		t.Run(attack, func(t *testing.T) {
			stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
				SessionID:     "sess-attack",
				HookEventName: "PostToolUse",
				ToolName:      "Edit",
				ToolInput:     ToolInput{FilePath: attack},
				CWD:           root,
			}))
			lastStdout.Reset()
			if err := HandlePostEdit(context.Background(), PostEditOptions{
				Indexer: idx,
				Claims:  claims,
				Vet:     stubVet,
			}, stdin, &lastStdout); err != nil {
				t.Fatalf("HandlePostEdit: %v", err)
			}
			var resp HookResponse
			if err := json.Unmarshal(lastStdout.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v\nstdout=%q", err, lastStdout.String())
			}
			if resp.HookSpecificOutput == nil {
				t.Fatal("escaped-path rejection must populate additionalContext")
			}
			if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "escapes the project root") &&
				!strings.Contains(resp.HookSpecificOutput.AdditionalContext, "resolves outside the project root") {
				t.Errorf("additionalContext should explain the escape: %q", resp.HookSpecificOutput.AdditionalContext)
			}
		})
	}

	if calls := idx.Calls(); len(calls) != 0 {
		t.Errorf("indexer should not be called on escaped paths; got %v", calls)
	}
	if rows := claims.Rows(); len(rows) != 0 {
		t.Errorf("v0.38: escaped-path rejections must NOT record claim rows; got %d rows", len(rows))
	}
}

// TestHandlePostEdit_CustomVerifierRunsWithoutGoMod proves that
// AlwaysVet bypasses the hasGoModule short-circuit and that VetVerb
// reaches both the claim summary and the evidence block. A non-Go
// project (no go.mod) used to silently no-op the verifier — this
// guards the new [post_edit.verify] code path so a future refactor
// of runVet can't quietly re-introduce the Go-centric gate.
func TestHandlePostEdit_CustomVerifierRunsWithoutGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // intentionally no go.mod
	target := filepath.Join(root, "lib.rs")
	if err := os.WriteFile(target, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var ran bool
	stubVet := func(ctx context.Context, dir string) (string, error) {
		ran = true
		if dir != root {
			t.Errorf("verifier ran in %q, want %q", dir, root)
		}
		return "Checking purser v0.1.0\n   Finished dev [unoptimized] in 0.42s", nil
	}

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-rust",
		ToolName:  "Edit",
		ToolInput: ToolInput{FilePath: target},
		CWD:       root,
	}))
	var stdout bytes.Buffer

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer:   idx,
		Claims:    claims,
		Vet:       stubVet,
		VetVerb:   "cargo check",
		AlwaysVet: true,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}
	if !ran {
		t.Fatal("verifier was never invoked — AlwaysVet did not bypass hasGoModule")
	}
	rows := claims.Rows()
	if len(rows) != 1 {
		t.Fatalf("claim rows = %d", len(rows))
	}
	if !strings.Contains(rows[0].Claim, "cargo check=ok") {
		t.Errorf("claim summary missing custom verb: %q", rows[0].Claim)
	}
	if !strings.Contains(rows[0].Evidence, "cargo check: ok") {
		t.Errorf("evidence missing custom verb: %q", rows[0].Evidence)
	}
}

// TestHandlePostEdit_CustomVerifierFailureUsesVerb confirms that a
// custom-verb failure routes the right label to the model — without
// this, a `cargo check` failure would tell Claude "go vet FAILED" and
// the model would chase a non-existent Go problem.
func TestHandlePostEdit_CustomVerifierFailureUsesVerb(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := seed(t, filepath.Join(root, "lib.rs"))

	stubVet := func(ctx context.Context, dir string) (string, error) {
		return "error[E0425]: cannot find value `x` in this scope", errors.New("exit status 101")
	}

	idx := &fakeIndexer{}
	claims := &fakeClaims{}
	stdin := bytes.NewReader(encodePayload(t, PostToolUsePayload{
		SessionID: "sess-rust-fail",
		ToolName:  "Write",
		ToolInput: ToolInput{FilePath: target},
		CWD:       root,
	}))
	var stdout bytes.Buffer

	err := HandlePostEdit(context.Background(), PostEditOptions{
		Indexer:   idx,
		Claims:    claims,
		Vet:       stubVet,
		VetVerb:   "cargo check",
		AlwaysVet: true,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandlePostEdit: %v", err)
	}

	var resp HookResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("verifier failure must populate hookSpecificOutput.additionalContext")
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "cargo check FAILED") {
		t.Errorf("model context should use the configured verb, got: %q", ctx)
	}
	if strings.Contains(ctx, "go vet") {
		t.Errorf("model context leaked the default verb into a custom-verifier run: %q", ctx)
	}

	rows := claims.Rows()
	if len(rows) != 1 || rows[0].Verified {
		t.Fatalf("expected one unverified claim, got %+v", rows)
	}
	if !strings.Contains(rows[0].Claim, "cargo check=failed") {
		t.Errorf("claim summary missing custom verb on failure: %q", rows[0].Claim)
	}
}

func TestVerifyVerb(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", "verify"},
		{"cargo", "cargo"},
		{"cargo check", "cargo check"},
		{"cargo check --workspace", "cargo check"},
		{"pnpm tsc --noEmit", "pnpm tsc"},
		{"go vet ./...", "go vet"},
		{"ruff", "ruff"},
		{"sh -c 'echo hi'", "sh"}, // first arg is a flag → label is just the program
	}
	for _, tc := range cases {
		got := VerifyVerb(tc.in)
		if got != tc.want {
			t.Errorf("VerifyVerb(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMakeShellRunner_Success(t *testing.T) {
	t.Parallel()
	runner := MakeShellRunner("echo hello && echo world", "")
	out, err := runner(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("runner err: %v", err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("expected both lines in output, got %q", out)
	}
}

func TestMakeShellRunner_Failure(t *testing.T) {
	t.Parallel()
	runner := MakeShellRunner("echo nope >&2; exit 7", "")
	out, err := runner(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected non-nil error for non-zero exit")
	}
	if !strings.Contains(out, "nope") {
		t.Errorf("expected stderr captured into combined output, got %q", out)
	}
}

// TestMakeShellRunner_WorkingDirOverride exercises the v0.46
// path-trust-aware working_dir handling. A working_dir inside the
// project root resolves; a working_dir outside falls back with a
// captured-output rejection note (security review #2 F2).
func TestMakeShellRunner_WorkingDirOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Inside the project root: pwd should print the resolved subdir.
	runner := MakeShellRunner("pwd", "subdir")
	out, err := runner(context.Background(), root)
	if err != nil {
		t.Fatalf("runner err: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), filepath.Base(sub)) {
		t.Errorf("pwd output %q did not end with %q", out, filepath.Base(sub))
	}
}

// TestMakeShellRunner_WorkingDirEscapeRejected pins the security
// review #2 F2 fix: a working_dir that resolves outside the project
// root must NOT execute the command in that directory.
func TestMakeShellRunner_WorkingDirEscapeRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := MakeShellRunner("pwd", "/etc")
	out, err := runner(context.Background(), root)
	if err != nil {
		t.Fatalf("runner err: %v", err)
	}
	if !strings.Contains(out, "resolves outside the project root") {
		t.Errorf("expected fallback note in output, got %q", out)
	}
	// pwd should print the project root, not /etc.
	if strings.Contains(out, "/etc\n") {
		t.Errorf("verifier ran in /etc despite path-trust; output: %q", out)
	}
}
