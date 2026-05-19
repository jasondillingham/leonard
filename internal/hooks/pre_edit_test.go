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
	var out bytes.Buffer
	err := HandlePreEdit(context.Background(), PreEditOptions{
		Store:      s,
		ModulePath: testModulePath,
	}, bytes.NewReader(payload), &out)
	if err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}
	var resp PreEditResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, out.String())
	}
	return resp
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
	if !resp.Continue || resp.Decision == "block" {
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
	resp := runPreEdit(t, s, encodePreToolUsePayload(t, PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: PreEditToolInput{FilePath: target, NewString: "store.NoSuchFunction()"},
	}))
	if resp.Continue || resp.Decision != "block" {
		t.Fatalf("expected block, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "store.NoSuchFunction") {
		t.Errorf("reason missing fabricated symbol: %q", resp.Reason)
	}
	if resp.StopReason == "" {
		t.Errorf("StopReason should mirror Reason for cli surfacing")
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
	if !resp.Continue || resp.Decision == "block" {
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
	if !resp.Continue || resp.Decision == "block" {
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
	if !resp.Continue || resp.Decision == "block" {
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
	if !resp.Continue || resp.Decision == "block" {
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
	if resp.Continue || resp.Decision != "block" {
		t.Fatalf("expected block for fabricated ref in body snippet, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "store.Open") {
		t.Errorf("reason should mention store.Open: %q", resp.Reason)
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
	if resp.Continue || resp.Decision != "block" {
		t.Fatalf("expected block for fabricated ref in write content, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "store.Ghost") {
		t.Errorf("reason should mention store.Ghost: %q", resp.Reason)
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
	if !strings.Contains(resp.Reason, "store.A") || !strings.Contains(resp.Reason, "store.B") {
		t.Errorf("expected both store.A and store.B in reason: %q", resp.Reason)
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
	if resp.Continue || resp.Decision != "block" {
		t.Fatalf("expected block via aliased import, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "s.PhantomCall") {
		t.Errorf("reason should mention aliased ref: %q", resp.Reason)
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
