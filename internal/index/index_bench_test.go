package index

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

// findRepoRoot mirrors the helper in internal/parse — duplicated rather
// than exported because both packages might evolve independently.
func findRepoRoot(b *testing.B) string {
	b.Helper()
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			b.Fatalf("findRepoRoot: no go.mod found above %s", filepath.Dir(here))
		}
		dir = parent
	}
}

// BenchmarkIndexFile_Go covers the end-to-end post-edit hook hot path:
// stat + read + hash + parse + extract + store insert for a single Go
// file. This is the number DESIGN.md §7 q4 sets a 200ms p95 budget
// against. Uses the repo's own largest Go file as the realistic
// upper-bound case.
func BenchmarkIndexFile_Go(b *testing.B) {
	root := findRepoRoot(b)
	target := filepath.Join(root, "internal", "parse", "typescript.go")
	tmp := b.TempDir()
	s, err := store.Open(filepath.Join(tmp, "leonard.db"))
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })

	idx := New(s, root)
	// Pre-warm: first call writes a file row + symbol rows; subsequent
	// calls also hit the symbol-replace path (hash matches but we want
	// to measure the work, not the early-out).
	if err := idx.IndexFile(target); err != nil {
		b.Fatalf("warmup IndexFile: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Mutate the stored hash so the file looks dirty each iteration
		// — we want to measure re-parse + re-insert, not the skip path.
		if err := s.UpsertFile(store.File{
			Path: "internal/parse/typescript.go",
			Hash: "force-reparse",
		}); err != nil {
			b.Fatalf("UpsertFile: %v", err)
		}
		if err := idx.IndexFile(target); err != nil {
			b.Fatalf("IndexFile: %v", err)
		}
	}
}

// BenchmarkIndexFile_NoChange measures the early-out hot path: the
// hook fires on a save that didn't actually change the file's bytes,
// so hash matches and we return without re-parsing.
func BenchmarkIndexFile_NoChange(b *testing.B) {
	root := findRepoRoot(b)
	target := filepath.Join(root, "internal", "parse", "typescript.go")
	tmp := b.TempDir()
	s, err := store.Open(filepath.Join(tmp, "leonard.db"))
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })

	idx := New(s, root)
	if err := idx.IndexFile(target); err != nil {
		b.Fatalf("warmup IndexFile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := idx.IndexFile(target); err != nil {
			b.Fatalf("IndexFile: %v", err)
		}
	}
}
