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
)

type fakeSymStore struct {
	mu      sync.Mutex
	has     map[string]bool
	queries []string
	err     error
}

func newFakeSymStore(known ...string) *fakeSymStore {
	has := make(map[string]bool, len(known))
	for _, n := range known {
		has[n] = true
	}
	return &fakeSymStore{has: has}
}

func (f *fakeSymStore) HasSymbol(name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, name)
	if f.err != nil {
		return false, f.err
	}
	return f.has[name], nil
}

func (f *fakeSymStore) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.queries))
	copy(out, f.queries)
	return out
}

const testModulePath = "github.com/jasondillingham/leonard"

func writeTarget(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func encodePreToolUsePayload(t *testing.T, p PreToolUsePayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func runPreEdit(t *testing.T, s SymbolStore, payload []byte) PreEditResponse {
	t.Helper()
	resp, _ := runPreEditWithRaw(t, s, payload)
	return resp
}

// runPreEditWithRaw returns the decoded response plus the exact JSON bytes
// emitted. Tests that care about wire-shape ("does the JSON contain
// continue: false?") need the raw bytes — Go's bool zero value is
// ambiguous with an omitted field after a round-trip.
func runPreEditWithRaw(t *testing.T, s SymbolStore, payload []byte) (PreEditResponse, []byte) {
	t.Helper()
	var out bytes.Buffer
	err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      s,
		ModulePath: testModulePath,
	}, bytes.NewReader(payload), &out)
	if err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}
	raw := append([]byte(nil), out.Bytes()...)
	var resp PreEditResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, out.String())
	}
	return resp, raw
}

// preEditDenied reports whether resp carries a "deny" permission decision.
// Centralising the check keeps callsites readable when the test only cares
// about block-vs-allow and not the exact reason string.
func preEditDenied(resp PreEditResponse) bool {
	return resp.HookSpecificOutput != nil && resp.HookSpecificOutput.PermissionDecision == "deny"
}

func TestHandlePreEdit_AllowsExistingTrackedSymbol(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore("Open")
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: `s, _ := store.Open("x"); _ = s`},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("expected allow, got %+v", resp)
	}
}

func TestHandlePreEdit_BlocksFabricatedTrackedSymbol(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore("Open") // NoSuchFunction not in index
	resp, raw := runPreEditWithRaw(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "store.NoSuchFunction()"},
	}))
	if !preEditDenied(resp) {
		t.Fatalf("expected PreToolUse deny, got %+v", resp)
	}
	if resp.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", resp.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "store.NoSuchFunction") {
		t.Errorf("permissionDecisionReason missing fabricated symbol: %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
	// F1 regression: emitting `continue: false` on a deny halts the whole
	// agent. Verified at the JSON level because Go's bool default is false
	// and the omitted-vs-explicit distinction only exists in the bytes.
	if bytes.Contains(raw, []byte(`"continue":false`)) {
		t.Errorf("deny response must not carry `continue: false`: %s", raw)
	}
	// And it must not lean on the legacy PostToolUse-style decision field.
	if bytes.Contains(raw, []byte(`"decision"`)) {
		t.Errorf("deny response must not carry the PostToolUse `decision` field: %s", raw)
	}
}

func TestHandlePreEdit_PassesThroughStdlibReferences(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "fmt"

func Foo() {}
`)
	s := newFakeSymStore() // empty
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: `fmt.Println("hi")`},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("expected allow for stdlib ref, got %+v", resp)
	}
	if got := s.Queries(); len(got) > 0 {
		t.Errorf("stdlib ref should not query store, queries=%v", got)
	}
}

func TestHandlePreEdit_PassesThroughExternalModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/spf13/cobra"

func Foo() {}
`)
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "_ = cobra.Command{}"},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("expected allow for external ref, got %+v", resp)
	}
}

func TestHandlePreEdit_LocalReceiverIgnored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

func Foo() {}
`)
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "c.NoSuchMethod()"},
	}))
	if !resp.Continue {
		t.Fatalf("expected allow for local receiver, got %+v", resp)
	}
}

func TestHandlePreEdit_PassesThroughForOtherTools(t *testing.T) {
	t.Parallel()
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Read",
		ToolInput: PreEditToolInput{FilePath: "anything.go"},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("expected allow for non-edit tool, got %+v", resp)
	}
}

func TestHandlePreEdit_PassesThroughForNonGoFiles(t *testing.T) {
	t.Parallel()
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: "/tmp/x.py", NewString: "store.NoSuchFunction()"},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("expected allow for non-go file, got %+v", resp)
	}
}

// TestHandlePreEdit_MultiEditBlocksFabricated closes the F6 bypass: the
// pre-edit guard used to scan only Edit + Write payloads, so a MultiEdit
// (which is what Claude Code reaches for whenever it has two changes in
// one file) could fabricate symbols freely. Each edit's new_string is
// concatenated into a single snippet; a fabricated reference in any one
// edit must trigger a deny.
func TestHandlePreEdit_MultiEditBlocksFabricated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore() // empty: any store.X reference is fabricated
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName: "MultiEdit",
		ToolInput: PreEditToolInput{
			FilePath: target,
			Edits: []PreEditMultiEdit{
				{OldString: "func Foo() {}", NewString: "func Foo() { _ = 1 }"},
				{OldString: "// placeholder", NewString: "s, _ := store.GhostFunction()\n_ = s\n"},
			},
		},
	})
	resp := runPreEdit(t, s, payload)
	if !preEditDenied(resp) {
		t.Fatalf("MultiEdit with fabricated ref should be denied, got %+v", resp)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "store.GhostFunction") {
		t.Errorf("reason should mention the fabricated symbol: %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestHandlePreEdit_MultiEditAllowsCleanBatch keeps the symmetric case
// honest: a MultiEdit whose edits all reference known symbols passes
// through. Without this assertion a regression that made MultiEdit
// blanket-deny would look correct against the failure test above.
func TestHandlePreEdit_MultiEditAllowsCleanBatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore("Open") // store.Open exists
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName: "MultiEdit",
		ToolInput: PreEditToolInput{
			FilePath: target,
			Edits: []PreEditMultiEdit{
				{OldString: "func Foo() {}", NewString: "func Foo() { _ = 1 }"},
				{OldString: "// placeholder", NewString: "s, _ := store.Open(\"x\")\n_ = s\n"},
			},
		},
	})
	resp := runPreEdit(t, s, payload)
	if preEditDenied(resp) {
		t.Fatalf("clean MultiEdit should be allowed, got deny: %+v", resp.HookSpecificOutput)
	}
}

// TestHandlePreEdit_NotebookEditPassesThroughNonGo verifies the
// NotebookEdit payload decodes cleanly and short-circuits at the .go
// suffix gate (notebooks are .ipynb). The guard is a no-op for notebooks
// but mustn't crash or fail open on a malformed envelope.
func TestHandlePreEdit_NotebookEditPassesThroughNonGo(t *testing.T) {
	t.Parallel()
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName: "NotebookEdit",
		ToolInput: PreEditToolInput{
			NotebookPath: "/tmp/notebook.ipynb",
			NewSource:    "import store\nstore.Ghost()\n",
		},
	}))
	if !resp.Continue || preEditDenied(resp) {
		t.Fatalf("NotebookEdit on .ipynb should pass through, got %+v", resp)
	}
}

func TestHandlePreEdit_WrapsFunctionBodySnippet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore() // empty so Open is fabricated
	snippet := "s, _ := store.Open(\"path\")\n_ = s\n"
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: snippet},
	}))
	if !preEditDenied(resp) {
		t.Fatalf("expected deny for fabricated ref in body snippet, got %+v", resp)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "store.Open") {
		t.Errorf("permissionDecisionReason should mention store.Open: %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestHandlePreEdit_HandlesWriteContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "new.go") // file doesn't exist yet
	content := `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Bar() { store.Ghost() }
`
	s := newFakeSymStore("Open")
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Write",
		ToolInput: PreEditToolInput{FilePath: target, Content: content},
	}))
	if !preEditDenied(resp) {
		t.Fatalf("expected deny for fabricated ref in write content, got %+v", resp)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "store.Ghost") {
		t.Errorf("permissionDecisionReason should mention store.Ghost: %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestHandlePreEdit_BlockListsMultipleFabricated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore() // both A and B fabricated
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "store.A(); store.B(); store.A()"},
	}))
	if !preEditDenied(resp) {
		t.Fatalf("expected deny, got %+v", resp)
	}
	reason := resp.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "store.A") || !strings.Contains(reason, "store.B") {
		t.Errorf("expected both store.A and store.B in reason: %q", reason)
	}
	a, b := 0, 0
	for _, q := range s.Queries() {
		switch q {
		case "A":
			a++
		case "B":
			b++
		}
	}
	if a != 1 || b != 1 {
		t.Errorf("expected each unique ref queried once, got A=%d B=%d (all=%v)", a, b, s.Queries())
	}
}

func TestHandlePreEdit_AllowsOnParseFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", "package foo\n")
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "{{{ this is not Go at all ))]"},
	}))
	if !resp.Continue {
		t.Fatalf("expected allow on parse failure, got %+v", resp)
	}
}

func TestHandlePreEdit_EmptyStdin(t *testing.T) {
	t.Parallel()
	err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      newFakeSymStore(),
		ModulePath: testModulePath,
	}, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}

func TestHandlePreEdit_StoreErrorBubblesUp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore()
	s.err = errors.New("disk on fire")
	err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      s,
		ModulePath: testModulePath,
	}, bytes.NewReader(encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "store.Anything()"},
	})), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("expected store error to bubble up, got %v", err)
	}
}

func TestHandlePreEdit_AllowsWhenNoTrackedRefs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

func Foo() {}
`)
	s := newFakeSymStore()
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "x := 1\n_ = x"},
	}))
	if !resp.Continue {
		t.Fatalf("expected allow, got %+v", resp)
	}
}

func TestHandlePreEdit_NilStoreIsError(t *testing.T) {
	t.Parallel()
	err := HandlePreEdit(context.Background(), PreEditOptions{
		ModulePath: testModulePath,
	}, bytes.NewReader(encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName: "Edit",
	})), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "SymbolStore") {
		t.Fatalf("expected nil-store error, got %v", err)
	}
}

func TestHandlePreEdit_AliasedImportResolves(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import s "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	store := newFakeSymStore() // empty
	resp := runPreEdit(t, store, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "s.PhantomCall()"},
	}))
	if !preEditDenied(resp) {
		t.Fatalf("expected deny via aliased import, got %+v", resp)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "s.PhantomCall") {
		t.Errorf("permissionDecisionReason should mention aliased ref: %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestHandlePreEdit_EmptyModulePathSkipsBlocking(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := writeTarget(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"

func Foo() {}
`)
	s := newFakeSymStore()
	var out bytes.Buffer
	err := HandlePreEdit(context.Background(), PreEditOptions{
		Store: s, // ModulePath intentionally empty
	}, bytes.NewReader(encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "store.Vapor()"},
	})), &out)
	if err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}
	var resp PreEditResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Continue {
		t.Fatalf("expected allow when ModulePath is empty, got %+v", resp)
	}
}

// TestPreEdit_F8_SiblingPackageReferenceCaught covers hooks F8: an Edit whose
// new_string references a sibling package without a matching import in the
// target file used to bypass the fabrication guard, because the snippet's
// wrapped AST had no imports and the target file didn't import the sibling
// either. The handler now scans the module for sibling package names and
// uses them as a fallback alias map. Fabricated references trip the guard
// even when no import alias is visible.
func TestPreEdit_F8_SiblingPackageReferenceCaught(t *testing.T) {
	t.Parallel()
	moduleRoot := t.TempDir()
	libDir := filepath.Join(moduleRoot, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	// A sibling package with one real symbol. The bug case is the snippet
	// referencing a name *not* in the index.
	writeTarget(t, libDir, "lib.go", "package lib\n\nfunc RealOne() {}\n")
	// The file under edit lives at module root, doesn't import lib.
	target := writeTarget(t, moduleRoot, "main.go", "package main\n\nfunc main() {}\n")

	store := newFakeSymStore("RealOne") // RealOne known; NonExistent is not.

	// Negative case: fabricated reference must block.
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		SessionID:     "s",
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput: PreEditToolInput{
			FilePath:  target,
			NewString: "lib.NonExistent()",
		},
	})
	var out bytes.Buffer
	if err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      store,
		ModulePath: testModulePath,
		ModuleRoot: moduleRoot,
	}, bytes.NewReader(payload), &out); err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}
	var resp PreEditResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nstdout=%q", err, out.String())
	}
	if !preEditDenied(resp) {
		t.Fatalf("expected deny for lib.NonExistent (sibling fabrication), got %s", out.String())
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "lib.NonExistent") {
		t.Errorf("deny reason should mention lib.NonExistent, got %q", resp.HookSpecificOutput.PermissionDecisionReason)
	}

	// Positive case: real reference must still pass.
	out.Reset()
	payload = encodePreToolUsePayload(t, PreToolUsePayload{
		SessionID:     "s",
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput: PreEditToolInput{
			FilePath:  target,
			NewString: "lib.RealOne()",
		},
	})
	if err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      store,
		ModulePath: testModulePath,
		ModuleRoot: moduleRoot,
	}, bytes.NewReader(payload), &out); err != nil {
		t.Fatalf("HandlePreEdit (positive): %v", err)
	}
	resp = PreEditResponse{}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode positive: %v", err)
	}
	if preEditDenied(resp) {
		t.Fatalf("expected allow for lib.RealOne, got deny: %s", out.String())
	}
}

// TestPreEdit_F8_NoModuleRootKeepsCurrentBehavior pins the backwards-compat
// contract: when ModuleRoot is empty (e.g. the cmd-layer couldn't locate
// the project root), the sibling scan is skipped and the existing
// allow-on-unresolved behavior is preserved. Otherwise existing tests
// would inherit the new strictness silently.
func TestPreEdit_F8_NoModuleRootKeepsCurrentBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := writeTarget(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		SessionID:     "s",
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput: PreEditToolInput{
			FilePath:  target,
			NewString: "anything.Goes()",
		},
	})
	store := newFakeSymStore()
	resp := runPreEdit(t, store, payload) // ModuleRoot unset via runPreEdit's options
	if preEditDenied(resp) {
		t.Fatalf("expected allow when ModuleRoot is empty (no sibling scan), got deny")
	}
}

// TestHandlePreEdit_OversizePayloadRejected covers security-1 F2.
// A 17 MiB payload (one byte over MaxHookPayloadBytes) must be
// rejected at the decode boundary, not allocate through to parseSnippet.
func TestHandlePreEdit_OversizePayloadRejected(t *testing.T) {
	t.Parallel()
	// Build a payload that's just barely over the cap. We construct
	// raw bytes rather than json-marshaling so the test isn't sensitive
	// to encoding overhead.
	big := make([]byte, MaxHookPayloadBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	err := HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
		bytes.NewReader(big), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected oversize-payload error")
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("error should wrap ErrDecode (so exit-code maps to 2): %v", err)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size cap: %v", err)
	}
}

// TestHandlePreEdit_OversizeSnippetRejected covers bughunt-4 caps F5.
// v0.9 silently zeroed oversize snippets, which let fabricated
// references at the truncated positions sneak past the guard. The
// v0.13 fix rejects the whole hook with ErrDecode so Claude sees a
// clear "edit rejected" signal via exit-code 2.
func TestHandlePreEdit_OversizeSnippetRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := writeTarget(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	huge := strings.Repeat("a", MaxSnippetBytes+1)
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		SessionID:     "s",
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput: PreEditToolInput{
			FilePath:  target,
			NewString: huge,
		},
	})
	err := HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
		bytes.NewReader(payload), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected oversize-snippet rejection")
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("error should wrap ErrDecode (exit-code 2): %v", err)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention the cap: %v", err)
	}
}

// TestHandlePreEdit_MultiEditOverCountRejected covers bughunt-4 caps
// F6. v0.9 truncated MultiEdit.Edits to MaxMultiEditElements, which
// let a fabricated reference at position 150 pass through the
// fabrication guard. v0.13 rejects the whole hook with ErrDecode
// instead.
func TestHandlePreEdit_MultiEditOverCountRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := writeTarget(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	edits := make([]PreEditMultiEdit, MaxMultiEditElements+5)
	for i := range edits {
		edits[i] = PreEditMultiEdit{NewString: "x"}
	}
	payload := encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName: "MultiEdit",
		ToolInput: PreEditToolInput{
			FilePath: target,
			Edits:    edits,
		},
	})
	err := HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
		bytes.NewReader(payload), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected over-count rejection")
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("error should wrap ErrDecode: %v", err)
	}
}

// TestHandlePreEdit_RejectsWritesUnderLeonardDir pins security review
// #2 F1 (CRITICAL): writes under `.leonard/` are operator-authored
// only — Claude must not author the post-edit verifier's command.
func TestHandlePreEdit_RejectsWritesUnderLeonardDir(t *testing.T) {
	t.Parallel()
	cases := []string{
		".leonard/config.toml",
		"./.leonard/config.toml",
		"./.leonard/leonard.db",
		"/Users/somebody/proj/.leonard/config.toml",
		"a/b/c/.leonard/scratch.md",
	}
	for _, fp := range cases {
		fp := fp
		t.Run(fp, func(t *testing.T) {
			payload := encodePreToolUsePayload(t, PreToolUsePayload{
				ToolName: "Write",
				ToolInput: PreEditToolInput{
					FilePath: fp,
					Content:  "[post_edit.verify]\ncommand = \"echo pwned\"",
				},
			})
			var out bytes.Buffer
			if err := HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
				bytes.NewReader(payload), &out); err != nil {
				t.Fatalf("HandlePreEdit: %v", err)
			}
			var resp PreEditResponse
			if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v\nstdout=%q", err, out.String())
			}
			if resp.HookSpecificOutput == nil {
				t.Fatal("expected hookSpecificOutput with deny decision")
			}
			if resp.HookSpecificOutput.PermissionDecision != "deny" {
				t.Errorf("permissionDecision = %q, want deny",
					resp.HookSpecificOutput.PermissionDecision)
			}
			if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "operator-authored") {
				t.Errorf("reason should explain operator-authored; got %q",
					resp.HookSpecificOutput.PermissionDecisionReason)
			}
		})
	}
}

// TestHandlePreEdit_AllowsLeonardLookalikePaths confirms that the
// `.leonard/` guard matches the EXACT path segment, not a substring.
// `.leonard.bak`, `mybackup.leonard`, `leonardish/` all pass through.
func TestHandlePreEdit_AllowsLeonardLookalikePaths(t *testing.T) {
	t.Parallel()
	cases := []string{
		"foo.leonard",
		"backup.leonard.bak/x.txt",
		"leonardish/y.toml",
		".leonard.old/z.txt",
	}
	for _, fp := range cases {
		fp := fp
		t.Run(fp, func(t *testing.T) {
			payload := encodePreToolUsePayload(t, PreToolUsePayload{
				ToolName:  "Write",
				ToolInput: PreEditToolInput{FilePath: fp, Content: "x"},
			})
			var out bytes.Buffer
			if err := HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
				bytes.NewReader(payload), &out); err != nil {
				t.Fatalf("HandlePreEdit: %v", err)
			}
			var resp PreEditResponse
			if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.HookSpecificOutput != nil && resp.HookSpecificOutput.PermissionDecision == "deny" {
				t.Errorf("lookalike path %q wrongly blocked: %s", fp, resp.HookSpecificOutput.PermissionDecisionReason)
			}
		})
	}
}
