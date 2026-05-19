package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

// TestIndexAll_SelfHost indexes the worktree this test runs from and asserts
// that known symbols owned by the parser/index lane show up. This is the
// "Indexing the Leonard repo itself yields all expected Go symbols" check
// from the brief — but limited to symbols this lane actually owns, so the
// test stays green even before the other lanes' code lands.
func TestIndexAll_SelfHost(t *testing.T) {
	t.Parallel()

	root, err := repoRoot()
	if err != nil {
		t.Skipf("could not locate repo root: %v", err)
	}

	tmp := t.TempDir()
	s, err := store.Open(filepath.Join(tmp, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	idx := New(s, root)
	if err := idx.IndexAll(); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	for _, name := range []string{"ExtractGo", "Indexer", "New", "IndexAll", "IndexFile"} {
		syms, err := s.FindSymbolsByName(name)
		if err != nil {
			t.Fatalf("FindSymbolsByName(%s): %v", name, err)
		}
		if len(syms) == 0 {
			t.Errorf("expected to find symbol %q in self-host index", name)
		}
	}
}

// repoRoot walks up from the working directory until it finds a go.mod.
func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
