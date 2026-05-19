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
