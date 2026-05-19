package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loadFixture returns a MemStore preloaded with the testdata/sample
// project's symbols. The fixture is referenced (not parsed) — until the
// parser lane lands we hand-write the records the indexer will produce.
func loadFixture(t *testing.T) *leonardmcp.MemStore {
	t.Helper()
	store := leonardmcp.NewMemStore()

	store.AddFile(leonardmcp.FileRecord{Path: "testdata/sample/main.go", Language: "go", SizeBytes: 420})
	store.AddFile(leonardmcp.FileRecord{Path: "testdata/sample/helper.py", Language: "python", SizeBytes: 96})

	store.AddSymbol(leonardmcp.SymbolRecord{
		FilePath: "testdata/sample/main.go", Name: "Greeter", QualifiedName: "main.Greeter",
		Kind: "type", Signature: "type Greeter struct { Prefix string }",
		StartLine: 8, EndLine: 10, Exported: true,
	})
	store.AddSymbol(leonardmcp.SymbolRecord{
		FilePath: "testdata/sample/main.go", Name: "Greet", QualifiedName: "main.Greeter.Greet",
		Kind: "method", Signature: "func (g *Greeter) Greet(name string)",
		StartLine: 13, EndLine: 15, Exported: true,
	})
	store.AddSymbol(leonardmcp.SymbolRecord{
		FilePath: "testdata/sample/main.go", Name: "DefaultGreeting", QualifiedName: "main.DefaultGreeting",
		Kind: "const", Signature: `const DefaultGreeting = "Hello,"`,
		StartLine: 18, EndLine: 18, Exported: true,
	})
	store.AddSymbol(leonardmcp.SymbolRecord{
		FilePath: "testdata/sample/main.go", Name: "main", QualifiedName: "main.main",
		Kind: "function", Signature: "func main()",
		StartLine: 20, EndLine: 23, Exported: false,
	})
	store.AddSymbol(leonardmcp.SymbolRecord{
		FilePath: "testdata/sample/helper.py", Name: "shout", QualifiedName: "helper.shout",
		Kind: "function", Signature: "def shout(name: str) -> str",
		StartLine: 2, EndLine: 3, Exported: true,
	})
	return store
}

// newSession spins up an MCP server with the provided store over an
// in-memory transport pair and returns a connected client session.
func newSession(t *testing.T, store leonardmcp.SymbolStore) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := leonardmcp.NewServer(store, leonardmcp.DefaultImplementation())

	serverTr, clientTr := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTr, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "leonard-mcp-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session
}

// decodeResult walks a CallToolResult's structured content into a typed
// destination. The SDK serializes typed-handler outputs into the result's
// StructuredContent field; we round-trip through JSON to stay loose about
// the SDK's exact representation.
func decodeResult[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var dest T
	if res == nil {
		t.Fatalf("nil CallToolResult")
	}
	if res.IsError {
		t.Fatalf("tool reported error: %+v", res.Content)
	}
	// Prefer StructuredContent (typed output) when present.
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal StructuredContent: %v", err)
		}
		if err := json.Unmarshal(raw, &dest); err != nil {
			t.Fatalf("unmarshal StructuredContent into %T: %v\nraw: %s", dest, err, raw)
		}
		return dest
	}
	// Fall back to the first text content block as JSON.
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(tc.Text), &dest); err != nil {
				t.Fatalf("unmarshal text content into %T: %v\ntext: %s", dest, err, tc.Text)
			}
			return dest
		}
	}
	t.Fatalf("no structured or text content in result")
	return dest
}

func TestToolsList(t *testing.T) {
	session := newSession(t, loadFixture(t))

	got := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}

	for _, want := range []string{"verify_symbol", "find_symbol", "list_files"} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, keys(got))
		}
	}
}

func TestVerifySymbolExists(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "verify_symbol",
		Arguments: map[string]any{"name": "Greeter"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.VerifySymbolOutput](t, res)
	if !out.Exists {
		t.Fatalf("expected Exists=true for Greeter, got %+v", out)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("expected 1 match for Greeter, got %d: %+v", len(out.Matches), out.Matches)
	}
	m := out.Matches[0]
	if m.File != "testdata/sample/main.go" || m.Line != 8 || m.Kind != "type" || m.QualifiedName != "main.Greeter" {
		t.Errorf("unexpected match: %+v", m)
	}
}

func TestVerifySymbolMissing(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "verify_symbol",
		Arguments: map[string]any{"name": "NotARealSymbol"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.VerifySymbolOutput](t, res)
	if out.Exists {
		t.Errorf("expected Exists=false, got %+v", out)
	}
	if len(out.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(out.Matches))
	}
}

func TestVerifySymbolKindFilter(t *testing.T) {
	session := newSession(t, loadFixture(t))

	// "Greet" the method exists; with a kind=type filter it should not.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "verify_symbol",
		Arguments: map[string]any{"name": "Greet", "kind": "type"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.VerifySymbolOutput](t, res)
	if out.Exists {
		t.Errorf("kind=type filter should exclude method Greet, got %+v", out)
	}
}

func TestVerifySymbolLanguageFilter(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "verify_symbol",
		Arguments: map[string]any{"name": "shout", "language": "go"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.VerifySymbolOutput](t, res)
	if out.Exists {
		t.Errorf("language=go should exclude python shout, got %+v", out)
	}
}

func TestFindSymbolSubstring(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_symbol",
		Arguments: map[string]any{"query": "greet"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.FindSymbolOutput](t, res).Matches

	// Expect Greeter, Greet, DefaultGreeting — case-insensitive substring.
	names := map[string]bool{}
	for _, m := range out {
		names[m.QualifiedName] = true
	}
	for _, want := range []string{"main.Greeter", "main.Greeter.Greet", "main.DefaultGreeting"} {
		if !names[want] {
			t.Errorf("expected query=greet to surface %q, got %v", want, keys(names))
		}
	}
}

func TestFindSymbolLimit(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_symbol",
		Arguments: map[string]any{"query": "greet", "limit": 2},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.FindSymbolOutput](t, res).Matches
	if len(out) != 2 {
		t.Errorf("expected limit=2 to cap output, got %d: %+v", len(out), out)
	}
}

func TestListFilesAll(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_files",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.ListFilesOutput](t, res).Files
	if len(out) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(out), out)
	}
}

func TestListFilesLanguageFilter(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_files",
		Arguments: map[string]any{"language": "go"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.ListFilesOutput](t, res).Files
	if len(out) != 1 || out[0].Path != "testdata/sample/main.go" {
		t.Fatalf("expected only main.go for language=go, got %+v", out)
	}
}

func TestListFilesPattern(t *testing.T) {
	session := newSession(t, loadFixture(t))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_files",
		Arguments: map[string]any{"pattern": "testdata/sample/*.py"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	out := decodeResult[leonardmcp.ListFilesOutput](t, res).Files
	if len(out) != 1 || out[0].Path != "testdata/sample/helper.py" {
		t.Fatalf("expected only helper.py, got %+v", out)
	}
}

// ---- store error propagation ----

type errStore struct{}

var errBoom = errors.New("boom")

func (errStore) FindSymbolsByName(context.Context, string) ([]leonardmcp.SymbolRecord, error) {
	return nil, errBoom
}
func (errStore) FindSymbolsByQuery(context.Context, string, int) ([]leonardmcp.SymbolRecord, error) {
	return nil, errBoom
}
func (errStore) ListFiles(context.Context, string, string) ([]leonardmcp.FileRecord, error) {
	return nil, errBoom
}

func TestStoreErrorSurfaces(t *testing.T) {
	session := newSession(t, errStore{})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "verify_symbol",
		Arguments: map[string]any{"name": "anything"},
	})
	// MCP semantics: handler errors surface as a result with IsError=true
	// (the RPC itself succeeds), not as a Go error from CallTool.
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError=true, got %+v", res)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
