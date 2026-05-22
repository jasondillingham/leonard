package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRuntime struct {
	initCalls   []initCall
	indexCalls  []indexCall
	verifyCalls []verifyCall

	initErr       error
	indexErr      error
	indexCount    int
	indexFailures []ParseFailure
	verifyOut     []SymbolMatch
	verifyErr     error

	// decisions/claims tape
	recordDecisionCalls []recordDecisionCall
	recordDecisionID    int64
	recordDecisionErr   error

	getDecisionsCalls []getDecisionsCall
	getDecisionsOut   []DecisionRow
	getDecisionsErr   error

	getStaleCalls []getStaleCall
	getStaleOut   []StaleDecisionRow
	getStaleErr   error

	getTruthHistoryCalls []getTruthHistoryCall
	getTruthHistoryOut   []TruthHistoryRow
	getTruthHistoryErr   error

	getTruthChangesCalls []getTruthChangesCall
	getTruthChangesOut   []TruthHistoryRow
	getTruthChangesErr   error

	getUnverifiedCalls []getUnverifiedCall
	getUnverifiedOut   []ClaimRow
	getUnverifiedErr   error

	resolveClaimCalls []resolveClaimCall
	resolveClaimErr   error

	doctorCalls []doctorCall
	doctorOut   DoctorReport
	doctorErr   error
}

type doctorCall struct {
	Root string
	Data string
}

type recordDecisionCall struct {
	Topic, Choice, Reasoning string
}
type getDecisionsCall struct {
	Topic string
	Since int64
	Limit int
}
type getStaleCall struct{ Limit int }
type getTruthHistoryCall struct {
	FilePath string
	Limit    int
}
type getTruthChangesCall struct {
	Scope string
	Since int64
	Limit int
}
type getUnverifiedCall struct{ SessionID string }

type resolveClaimCall struct {
	ClaimID int64
	Note    string
}

type initCall struct {
	Root string
	Data string
}

type indexCall struct {
	Root string
	Data string
}

type verifyCall struct {
	Data string
	Name string
	Kind string
}

func (f *fakeRuntime) Init(_ context.Context, root, data string) error {
	f.initCalls = append(f.initCalls, initCall{root, data})
	return f.initErr
}

func (f *fakeRuntime) IndexAll(_ context.Context, root, data string) (IndexResult, error) {
	f.indexCalls = append(f.indexCalls, indexCall{root, data})
	return IndexResult{FilesIndexed: f.indexCount, ParseFailures: f.indexFailures}, f.indexErr
}

func (f *fakeRuntime) VerifySymbol(_ context.Context, data, name, kind string) ([]SymbolMatch, error) {
	f.verifyCalls = append(f.verifyCalls, verifyCall{data, name, kind})
	return f.verifyOut, f.verifyErr
}

func (f *fakeRuntime) RecordDecision(_ context.Context, _, topic, choice, reasoning string) (int64, error) {
	f.recordDecisionCalls = append(f.recordDecisionCalls, recordDecisionCall{topic, choice, reasoning})
	return f.recordDecisionID, f.recordDecisionErr
}

func (f *fakeRuntime) GetDecisions(_ context.Context, _, topic string, since int64, limit int) ([]DecisionRow, error) {
	f.getDecisionsCalls = append(f.getDecisionsCalls, getDecisionsCall{topic, since, limit})
	return f.getDecisionsOut, f.getDecisionsErr
}

func (f *fakeRuntime) GetStaleDecisions(_ context.Context, _ string, limit int) ([]StaleDecisionRow, error) {
	f.getStaleCalls = append(f.getStaleCalls, getStaleCall{limit})
	return f.getStaleOut, f.getStaleErr
}

func (f *fakeRuntime) GetTruthChanges(_ context.Context, _, scope string, since int64, limit int) ([]TruthHistoryRow, error) {
	f.getTruthChangesCalls = append(f.getTruthChangesCalls, getTruthChangesCall{scope, since, limit})
	return f.getTruthChangesOut, f.getTruthChangesErr
}

func (f *fakeRuntime) GetTruthHistory(_ context.Context, _, filePath string, limit int) ([]TruthHistoryRow, error) {
	f.getTruthHistoryCalls = append(f.getTruthHistoryCalls, getTruthHistoryCall{filePath, limit})
	return f.getTruthHistoryOut, f.getTruthHistoryErr
}

func (f *fakeRuntime) GetUnverifiedClaims(_ context.Context, _, sessionID string) ([]ClaimRow, error) {
	f.getUnverifiedCalls = append(f.getUnverifiedCalls, getUnverifiedCall{sessionID})
	return f.getUnverifiedOut, f.getUnverifiedErr
}

func (f *fakeRuntime) ResolveClaim(_ context.Context, _ string, claimID int64, note string) error {
	f.resolveClaimCalls = append(f.resolveClaimCalls, resolveClaimCall{claimID, note})
	return f.resolveClaimErr
}

func (f *fakeRuntime) Doctor(_ context.Context, root, data string) (DoctorReport, error) {
	f.doctorCalls = append(f.doctorCalls, doctorCall{root, data})
	return f.doctorOut, f.doctorErr
}

func runRoot(t *testing.T, rt Runtime, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd(rt)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

// withCwd temporarily chdirs to dir. The Runtime fakes use cwd, so each test
// hops into a tempdir.
//
// Also redirects XDG_CONFIG_HOME / HOME to a per-test tempdir so any CLI
// that touches $XDG_CONFIG_HOME (trust files, v1.0 bypass tokens) doesn't
// scribble in the operator's real config. v1.0 (bughunt-11 F1) made this
// load-bearing — truth-edit / override now write tokens under XDG.
func withCwd(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)
}

func TestInitCmd_UsesCwdWhenNoArg(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "init")
	if err != nil {
		t.Fatalf("init: %v\nout=%s", err, out)
	}
	if len(rt.initCalls) != 1 {
		t.Fatalf("Init called %d times", len(rt.initCalls))
	}
	call := rt.initCalls[0]
	expectedRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(call.Root)
	if gotRoot != expectedRoot {
		t.Errorf("Init root = %q, want %q", call.Root, root)
	}
	if filepath.Base(call.Data) != dataDirName {
		t.Errorf("Init dataDir basename = %q", filepath.Base(call.Data))
	}
	if !strings.Contains(out, "initialized") {
		t.Errorf("stdout = %q", out)
	}
}

func TestInitCmd_AcceptsPathArg(t *testing.T) {
	rt := &fakeRuntime{}
	target := t.TempDir()
	_, err := runRoot(t, rt, "init", target)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if len(rt.initCalls) != 1 || rt.initCalls[0].Root != target {
		t.Fatalf("Init root = %q, want %q", rt.initCalls[0].Root, target)
	}
}

func TestInitCmd_PropagatesError(t *testing.T) {
	rt := &fakeRuntime{initErr: errors.New("disk full")}
	target := t.TempDir()
	_, err := runRoot(t, rt, "init", target)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected disk full error, got %v", err)
	}
}

func TestIndexCmd_RequiresInit(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "index")
	if err == nil || !strings.Contains(err.Error(), "leonard init") {
		t.Fatalf("expected init-required error, got %v", err)
	}
	if len(rt.indexCalls) != 0 {
		t.Errorf("IndexAll should not be called when uninitialised")
	}
}

func TestIndexCmd_PrintsCount(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{indexCount: 42}
	out, err := runRoot(t, rt, "index")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(rt.indexCalls) != 1 {
		t.Fatalf("IndexAll calls = %d", len(rt.indexCalls))
	}
	if !strings.Contains(out, "indexed 42") {
		t.Errorf("stdout = %q", out)
	}
}

func TestIndexCmd_SurfacesParseFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		indexCount: 10,
		indexFailures: []ParseFailure{
			{Path: "src/api.py", Message: "line 121: SyntaxError: 'invalid syntax'"},
			{Path: "src/auth.py", Message: "line 30: SyntaxError: 'invalid syntax'"},
		},
	}
	out, err := runRoot(t, rt, "index")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	// Both the summary line and the per-file sample must reach the output.
	// Without this surfacing, a user sees "indexed 10 files" and has no
	// hint that 2 files silently dropped their symbols.
	if !strings.Contains(out, "indexed 10") {
		t.Errorf("missing indexed count: %q", out)
	}
	if !strings.Contains(out, "2 file(s) failed to parse") {
		t.Errorf("missing failure summary line: %q", out)
	}
	if !strings.Contains(out, "src/api.py") || !strings.Contains(out, "line 121") {
		t.Errorf("missing per-file detail: %q", out)
	}
}

func TestVerifyCmd_FoundPrintsRows(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		verifyOut: []SymbolMatch{
			{File: "internal/store/store.go", Line: 12, Signature: "func Open(path string) (*Store, error)", Kind: "function", QualifiedName: "store.Open"},
		},
	}
	out, err := runRoot(t, rt, "verify", "Open")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rt.verifyCalls[0].Name != "Open" {
		t.Errorf("name = %q", rt.verifyCalls[0].Name)
	}
	if !strings.Contains(out, "store.go:12") || !strings.Contains(out, "function") || !strings.Contains(out, "store.Open") {
		t.Errorf("stdout missing details: %q", out)
	}
}

func TestVerifyCmd_NotFoundReturnsExitCode1(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "verify", "DoesNotExist")
	var ec *exitCode
	if !errors.As(err, &ec) {
		t.Fatalf("expected exitCode error, got %T %v", err, err)
	}
	if ec.code != 1 {
		t.Errorf("exit code = %d, want 1", ec.code)
	}
}

func TestVerifyCmd_KindFilterPassedThrough(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		verifyOut: []SymbolMatch{{File: "x.go", Line: 1, Kind: "method", QualifiedName: "T.M"}},
	}
	_, err := runRoot(t, rt, "verify", "--kind", "method", "M")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rt.verifyCalls[0].Kind != "method" {
		t.Errorf("kind passed through = %q", rt.verifyCalls[0].Kind)
	}
}

func TestMCPCmd_ReportsMissingBinary(t *testing.T) {
	rt := &fakeRuntime{}
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	_, err := runRoot(t, rt, "mcp")
	if err == nil || !strings.Contains(err.Error(), "leonard-mcp not found") {
		t.Fatalf("expected leonard-mcp not found, got %v", err)
	}
}

// Used to silence unused-import warnings if io is not referenced elsewhere.
var _ = io.Discard
