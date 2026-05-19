package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

// TestIndexAll_TypeScript indexes testdata/typescript/ and asserts that every
// expected symbol — functions, classes, methods, interfaces, types,
// const/let/var — is reachable via store.FindSymbolsByName. Also confirms
// the hash-gated incremental path: a second IndexAll on an untouched tree
// re-parses zero TypeScript files.
func TestIndexAll_TypeScript(t *testing.T) {
	t.Parallel()

	root, err := repoRoot()
	if err != nil {
		t.Skipf("could not locate repo root: %v", err)
	}
	fixtureRoot := filepath.Join(root, "testdata", "typescript")
	if _, err := os.Stat(fixtureRoot); err != nil {
		t.Fatalf("fixture dir missing: %v", err)
	}

	tmp := t.TempDir()
	s, err := store.Open(filepath.Join(tmp, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	idx := New(s, fixtureRoot)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	// Two files in the fixture; both should trigger a parse.
	if got := idx.ParseCount(); got != 2 {
		t.Errorf("first IndexAll parsed %d files, want 2", got)
	}

	tsFiles, err := s.ListFiles("", "typescript")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(tsFiles) != 2 {
		t.Errorf("typescript file count = %d, want 2 (paths: %v)", len(tsFiles), tsFileNames(tsFiles))
	}

	wantSymbols := []struct {
		name  string
		kind  string
		qname string
	}{
		// From module.ts
		{"VERSION", "const", "VERSION"},
		{"PUBLIC_NAME", "const", "PUBLIC_NAME"},
		{"counter", "var", "counter"},
		{"legacyFlag", "var", "legacyFlag"},
		{"a", "const", "a"},
		{"b", "const", "b"},
		{"hello", "function", "hello"},
		{"_helper", "function", "_helper"},
		{"fetchAll", "function", "fetchAll"},
		{"Greeter", "interface", "Greeter"},
		{"ID", "type", "ID"},
		{"Pair", "type", "Pair"},
		{"Container", "type", "Container"},
		{"_PrivateBox", "type", "_PrivateBox"},
		{"add", "method", "Container.add"},
		{"_refresh", "method", "Container._refresh"},
		{"load", "method", "Container.load"},
		// `constructor` exists on both Container and _PrivateBox.
		// From component.tsx
		{"Props", "interface", "Props"},
		{"Greeting", "function", "Greeting"},
		{"Counter", "const", "Counter"},
		{"Panel", "type", "Panel"},
		{"render", "method", "Panel.render"},
	}
	for _, w := range wantSymbols {
		syms, err := s.FindSymbolsByName(w.name)
		if err != nil {
			t.Fatalf("FindSymbolsByName(%q): %v", w.name, err)
		}
		if !tsHasSymbol(syms, w.kind, w.qname) {
			t.Errorf("missing symbol name=%q kind=%q qname=%q; got %v",
				w.name, w.kind, w.qname, tsSummarizeSymbols(syms))
		}
	}

	// `constructor` exists on both Container and _PrivateBox; FindSymbolsByName
	// must surface both rows.
	ctors, err := s.FindSymbolsByName("constructor")
	if err != nil {
		t.Fatalf("FindSymbolsByName(constructor): %v", err)
	}
	if len(ctors) < 2 {
		t.Errorf("expected `constructor` to match both classes; got %d rows: %v",
			len(ctors), tsSummarizeSymbols(ctors))
	}

	// Exported flag spot-checks.
	expectExported := map[string]bool{
		"VERSION":    true,
		"counter":    false,
		"hello":      true,
		"_helper":    false,
		"Greeter":    true,
		"Container":  true,
		"_PrivateBox": false,
		"Greeting":   true,
		"Counter":    true,
		"Panel":      true,
	}
	for name, want := range expectExported {
		syms, err := s.FindSymbolsByName(name)
		if err != nil {
			t.Fatalf("FindSymbolsByName(%q): %v", name, err)
		}
		if len(syms) == 0 {
			continue // already reported above
		}
		if syms[0].Exported != want {
			t.Errorf("%s: Exported=%v, want %v", name, syms[0].Exported, want)
		}
	}

	// Hash-gated incremental: re-running IndexAll on an untouched tree must
	// not increment the parse counter.
	prior := idx.ParseCount()
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("second IndexAll: %v", err)
	}
	if delta := idx.ParseCount() - prior; delta != 0 {
		t.Errorf("second IndexAll re-parsed %d TypeScript files; want 0", delta)
	}
}

func tsHasSymbol(syms []store.Symbol, kind, qname string) bool {
	for _, s := range syms {
		if s.Kind == kind && s.QualifiedName == qname {
			return true
		}
	}
	return false
}

func tsSummarizeSymbols(syms []store.Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Kind+":"+s.QualifiedName)
	}
	return out
}

func tsFileNames(files []store.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
