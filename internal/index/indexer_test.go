package index

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

// fixture lays out a temporary project rooted at t.TempDir() and returns
// the absolute root and a freshly-opened in-memory store.
type fixture struct {
	root  string
	store *store.Store
}

func newFixture(t *testing.T, files map[string]string) fixture {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	s, err := store.Open(filepath.Join(root, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return fixture{root: root, store: s}
}

func TestIndexAll_BasicWalk(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"a.go":           "package a\nfunc Alpha() {}\n",
		"sub/b.go":       "package b\ntype Beta struct{}\n",
		"README.md":      "skip me",
		"node_modules/x": "should be ignored",
		"vendor/v.go":    "package v\nfunc Skipped(){}\n",
	})

	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	files, err := fx.store.ListFiles("", "")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	got := pathSet(files)
	want := []string{"a.go", "sub/b.go"}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("expected %s indexed; have %v", w, got)
		}
	}
	if contains(got, "vendor/v.go") {
		t.Errorf("vendor/ should be skipped; have %v", got)
	}
	if contains(got, "README.md") {
		t.Errorf("non-go file should be skipped; have %v", got)
	}

	if syms, _ := fx.store.FindSymbolsByName("Alpha"); len(syms) != 1 {
		t.Errorf("expected Alpha symbol; got %d matches", len(syms))
	}
	if syms, _ := fx.store.FindSymbolsByName("Beta"); len(syms) != 1 {
		t.Errorf("expected Beta symbol; got %d matches", len(syms))
	}
}

func TestIndexAll_Incremental(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"a.go":     "package a\nfunc Alpha() {}\n",
		"b.go":     "package b\nfunc Beta() {}\n",
		"sub/c.go": "package c\nfunc Gamma() {}\n",
	})

	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("first IndexAll: %v", err)
	}
	firstCount := idx.ParseCount()
	if firstCount != 3 {
		t.Fatalf("first run parsed %d files, want 3", firstCount)
	}

	if err := idx.IndexAll(); err != nil {
		t.Fatalf("second IndexAll: %v", err)
	}
	if delta := idx.ParseCount() - firstCount; delta != 0 {
		t.Errorf("second IndexAll re-parsed %d files; want 0", delta)
	}

	// Mutate one file; only it should be re-parsed.
	if err := os.WriteFile(filepath.Join(fx.root, "b.go"), []byte("package b\nfunc BetaPrime() {}\n"), 0o644); err != nil {
		t.Fatalf("rewrite b.go: %v", err)
	}
	priorCount := idx.ParseCount()
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("third IndexAll: %v", err)
	}
	if delta := idx.ParseCount() - priorCount; delta != 1 {
		t.Errorf("after mutation, re-parsed %d files; want 1", delta)
	}

	// Old symbol from b.go should be gone; new one should be present.
	if syms, _ := fx.store.FindSymbolsByName("Beta"); len(syms) != 0 {
		t.Errorf("expected Beta gone after mutation; got %d", len(syms))
	}
	if syms, _ := fx.store.FindSymbolsByName("BetaPrime"); len(syms) != 1 {
		t.Errorf("expected BetaPrime present after mutation; got %d", len(syms))
	}
}

func TestIndexAll_Gitignore(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		".gitignore":     "build/\n*.gen.go\nsecret/\n",
		"a.go":           "package a\nfunc Keep() {}\n",
		"a.gen.go":       "package a\nfunc Generated() {}\n",
		"build/x.go":     "package x\nfunc BuildOnly() {}\n",
		"secret/s.go":    "package s\nfunc Secret() {}\n",
		"sub/keep.go":    "package sub\nfunc SubKeep() {}\n",
	})

	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	files, _ := fx.store.ListFiles("", "")
	got := pathSet(files)
	for _, blocked := range []string{"a.gen.go", "build/x.go", "secret/s.go"} {
		if contains(got, blocked) {
			t.Errorf("%s should be gitignored; got %v", blocked, got)
		}
	}
	for _, kept := range []string{"a.go", "sub/keep.go"} {
		if !contains(got, kept) {
			t.Errorf("%s should be indexed; got %v", kept, got)
		}
	}
}

func TestIndexAll_LeonardIgnore(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		".leonardignore": "*.skip.go\n",
		"a.go":           "package a\nfunc Keep() {}\n",
		"b.skip.go":      "package a\nfunc Skipped() {}\n",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}
	files, _ := fx.store.ListFiles("", "")
	got := pathSet(files)
	if contains(got, "b.skip.go") {
		t.Errorf(".leonardignore should exclude b.skip.go; got %v", got)
	}
	if !contains(got, "a.go") {
		t.Errorf("a.go should be indexed; got %v", got)
	}
}

func TestIndexFile_Single(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"a.go": "package a\nfunc Alpha() {}\n",
		"b.go": "package b\nfunc Beta() {}\n",
	})
	idx := New(fx.store, fx.root)

	// Relative path
	if err := idx.IndexFile("a.go"); err != nil {
		t.Fatalf("IndexFile rel: %v", err)
	}
	if syms, _ := fx.store.FindSymbolsByName("Alpha"); len(syms) != 1 {
		t.Errorf("expected Alpha after rel IndexFile; got %d", len(syms))
	}
	if syms, _ := fx.store.FindSymbolsByName("Beta"); len(syms) != 0 {
		t.Errorf("Beta should not be indexed yet; got %d", len(syms))
	}

	// Absolute path
	if err := idx.IndexFile(filepath.Join(fx.root, "b.go")); err != nil {
		t.Fatalf("IndexFile abs: %v", err)
	}
	if syms, _ := fx.store.FindSymbolsByName("Beta"); len(syms) != 1 {
		t.Errorf("expected Beta after abs IndexFile; got %d", len(syms))
	}

	// Re-indexing unchanged file is a no-op.
	prior := idx.ParseCount()
	if err := idx.IndexFile("a.go"); err != nil {
		t.Fatalf("IndexFile idempotent: %v", err)
	}
	if delta := idx.ParseCount() - prior; delta != 0 {
		t.Errorf("re-indexing unchanged file parsed %d files; want 0", delta)
	}
}

func TestIndexFile_Missing(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, nil)
	idx := New(fx.store, fx.root)
	if err := idx.IndexFile("nope.go"); err != nil {
		t.Errorf("missing file should be a silent no-op; got %v", err)
	}
}

func TestIndexFile_NonGoIgnored(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"a.txt": "not source",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexFile("a.txt"); err != nil {
		t.Fatalf("IndexFile txt: %v", err)
	}
	if files, _ := fx.store.ListFiles("", ""); len(files) != 0 {
		t.Errorf("expected no files indexed; got %d", len(files))
	}
}

func TestIndexFile_ParseError(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"bad.go": "package p\nfunc {\n",
	})
	idx := New(fx.store, fx.root)
	err := idx.IndexFile("bad.go")
	if err == nil {
		t.Fatal("expected parse error from bad.go")
	}
	// File row should still be persisted so we don't churn on it.
	if _, ok, _ := fx.store.GetFile("bad.go"); !ok {
		t.Errorf("expected bad.go file row to be persisted despite parse error")
	}
}

// TestIndexAll_SurfacesParseFailures locks in the contract that parse
// errors are exposed via ParseFailures() rather than silently swallowed.
// Before this surfacing existed, the indexer counted a file as "indexed"
// even when its symbols were dropped — exactly the false-done-claim
// pattern Leonard exists to prevent.
func TestIndexAll_SurfacesParseFailures(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"ok.go":  "package p\nfunc Good() {}\n",
		"bad.go": "package p\nfunc {\n",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}
	failures := idx.ParseFailures()
	if len(failures) != 1 {
		t.Fatalf("ParseFailures = %d, want 1; got %+v", len(failures), failures)
	}
	if failures[0].Path != "bad.go" {
		t.Errorf("ParseFailures[0].Path = %q, want bad.go", failures[0].Path)
	}
	if failures[0].Message == "" {
		t.Errorf("ParseFailures[0].Message empty — caller has nothing to surface")
	}
}

func pathSet(files []store.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestIndexAll_QualifiedNamesDisambiguateCrossFile covers selfhost F1: two
// files in different directories defining the same top-level name used to
// collide on qualified_name=="VERSION", making verify_symbol output
// ambiguous and reducing the index to "first one wins" semantics for the
// stale-decisions check. Python and TypeScript symbols now carry a
// filename-derived dotted prefix so their qualified_names are distinct.
func TestIndexAll_QualifiedNamesDisambiguateCrossFile(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"alpha/module.py":  "VERSION = '1.0'\n",
		"beta/module.py":   "VERSION = '2.0'\n",
		"src/module.ts":    "export const VERSION = '3.0';\n",
		"lib/module.ts":    "export const VERSION = '4.0';\n",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	syms, err := fx.store.FindSymbolsByName("VERSION")
	if err != nil {
		t.Fatalf("FindSymbolsByName: %v", err)
	}
	if len(syms) != 4 {
		t.Fatalf("expected 4 VERSION rows (2 python + 2 typescript), got %d", len(syms))
	}

	seen := map[string]string{}
	for _, s := range syms {
		if prior, dup := seen[s.QualifiedName]; dup {
			t.Errorf("qualified_name %q collides across files %q and %q — module prefix not applied", s.QualifiedName, prior, s.FilePath)
		}
		seen[s.QualifiedName] = s.FilePath
		// The prefix must include the directory component, not just the basename,
		// or alpha/module.py and beta/module.py would still collide.
		if s.QualifiedName == "VERSION" {
			t.Errorf("bare qualified_name=%q on %s — fix didn't apply", s.QualifiedName, s.FilePath)
		}
	}
}

// TestIndexAll_PrunesDeletedFiles covers bughunt-2 cli F2. The prior
// IndexAll walked the filesystem and updated rows but never deleted
// rows for files that had vanished — `verify_symbol` kept returning
// matches for code that no longer existed, the exact ground-truth-
// drift failure the project exists to prevent.
func TestIndexAll_PrunesDeletedFiles(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"keep.go":   "package keep\nfunc Stay() {}\n",
		"doomed.go": "package doomed\nfunc Vanish() {}\n",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("first IndexAll: %v", err)
	}
	// Sanity: both files indexed, both symbols visible.
	files, _ := fx.store.ListFiles("", "")
	if len(files) != 2 {
		t.Fatalf("expected 2 files after first index, got %d", len(files))
	}
	if syms, _ := fx.store.FindSymbolsByName("Vanish"); len(syms) != 1 {
		t.Fatalf("Vanish should index initially; got %d rows", len(syms))
	}

	// User deletes doomed.go from disk and re-indexes.
	if err := os.Remove(filepath.Join(fx.root, "doomed.go")); err != nil {
		t.Fatalf("remove doomed.go: %v", err)
	}
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("second IndexAll: %v", err)
	}

	// The file row must be gone.
	files, _ = fx.store.ListFiles("", "")
	if len(files) != 1 {
		t.Fatalf("expected 1 file after delete+reindex, got %d: %+v", len(files), files)
	}
	if files[0].Path != "keep.go" {
		t.Errorf("surviving file = %q, want keep.go", files[0].Path)
	}
	// And so must its symbols (FK CASCADE).
	if syms, _ := fx.store.FindSymbolsByName("Vanish"); len(syms) != 0 {
		t.Errorf("Vanish should be pruned after delete; got %d rows", len(syms))
	}
	// The surviving file's symbol is still there.
	if syms, _ := fx.store.FindSymbolsByName("Stay"); len(syms) != 1 {
		t.Errorf("Stay should survive prune; got %d rows", len(syms))
	}
}

// TestIndexAll_PruneIgnoresLeonardIgnoredFiles confirms the prune
// only fires on file-not-found. A path that's still on disk but
// excluded via .leonardignore must remain in the store (the user
// added an ignore rule, they didn't delete the file).
func TestIndexAll_PruneIgnoresLeonardIgnoredFiles(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, map[string]string{
		"keep.go":   "package keep\nfunc Stay() {}\n",
		"legacy.go": "package legacy\nfunc Old() {}\n",
	})
	idx := New(fx.store, fx.root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("first IndexAll: %v", err)
	}
	files, _ := fx.store.ListFiles("", "")
	if len(files) != 2 {
		t.Fatalf("expected 2 files after first index, got %d", len(files))
	}

	// User adds legacy.go to .leonardignore (file still on disk).
	if err := os.WriteFile(filepath.Join(fx.root, ".leonardignore"), []byte("legacy.go\n"), 0o644); err != nil {
		t.Fatalf("write .leonardignore: %v", err)
	}
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("second IndexAll: %v", err)
	}
	// The ignored file must NOT be pruned — it still exists on disk.
	files, _ = fx.store.ListFiles("", "")
	if len(files) != 2 {
		t.Errorf("ignored-but-on-disk file should not be pruned; got %d files", len(files))
	}
}
