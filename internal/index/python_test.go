package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

// TestIndexAll_Python indexes testdata/python/ and asserts that every expected
// top-level symbol — functions, classes, methods, vars — is reachable via
// store.FindSymbolsByName. Also confirms the hash-gated incremental path:
// a second IndexAll on an untouched tree re-parses zero Python files.
func TestIndexAll_Python(t *testing.T) {
	t.Parallel()

	root, err := repoRoot()
	if err != nil {
		t.Skipf("could not locate repo root: %v", err)
	}
	fixtureRoot := filepath.Join(root, "testdata", "python")
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

	// Files counted: the fixture has two .py files. The indexer's parseCount
	// reflects how many extractor calls fired this run.
	if got := idx.ParseCount(); got != 2 {
		t.Errorf("first IndexAll parsed %d files, want 2", got)
	}

	// Files indexed are tagged with language=python.
	pyFiles, err := s.ListFiles("", "python")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(pyFiles) != 2 {
		t.Errorf("python file count = %d, want 2 (paths: %v)", len(pyFiles), fileNames(pyFiles))
	}

	wantSymbols := []struct {
		name  string
		kind  string
		qname string
	}{
		// From module.py
		{"VERSION", "var", "module.VERSION"},
		{"PUBLIC_NAME", "var", "module.PUBLIC_NAME"},
		{"_internal", "var", "module._internal"},
		{"hello", "function", "module.hello"},
		{"add", "function", "module.add"},
		{"_helper", "function", "module._helper"},
		{"Widget", "type", "module.Widget"},
		{"_PrivateWidget", "type", "module._PrivateWidget"},
		// From methods.py
		{"Container", "type", "methods.Container"},
		{"Renderer", "type", "methods.Renderer"},
		{"__init__", "method", "methods.Container.__init__"},
		{"remove", "method", "methods.Container.remove"},
		{"_refresh", "method", "methods.Container._refresh"},
		{"render", "method", "methods.Renderer.render"},
		{"flush", "method", "methods.Renderer.flush"},
	}
	for _, w := range wantSymbols {
		syms, err := s.FindSymbolsByName(w.name)
		if err != nil {
			t.Fatalf("FindSymbolsByName(%q): %v", w.name, err)
		}
		if !hasSymbol(syms, w.kind, w.qname) {
			t.Errorf("missing symbol name=%q kind=%q qname=%q; got %v",
				w.name, w.kind, w.qname, summarizeSymbols(syms))
		}
	}

	// `add` is overloaded: a top-level function in module.py AND a method on
	// Container in methods.py. FindSymbolsByName must surface both rows.
	addSyms, err := s.FindSymbolsByName("add")
	if err != nil {
		t.Fatalf("FindSymbolsByName(add): %v", err)
	}
	if len(addSyms) < 2 {
		t.Errorf("expected `add` to match both function and method; got %d rows", len(addSyms))
	}

	// Hash-gated incremental: re-running IndexAll on an untouched tree must
	// not increment the parse counter.
	prior := idx.ParseCount()
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("second IndexAll: %v", err)
	}
	if delta := idx.ParseCount() - prior; delta != 0 {
		t.Errorf("second IndexAll re-parsed %d Python files; want 0", delta)
	}
}

func hasSymbol(syms []store.Symbol, kind, qname string) bool {
	for _, s := range syms {
		if s.Kind == kind && s.QualifiedName == qname {
			return true
		}
	}
	return false
}

func summarizeSymbols(syms []store.Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Kind+":"+s.QualifiedName)
	}
	return out
}

func fileNames(files []store.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
