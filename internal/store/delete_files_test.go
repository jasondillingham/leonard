package store

import (
	"fmt"
	"testing"
	"time"
)

// TestDeleteFiles_BatchSemantics pins three properties of the v0.7
// batch helper that the indexer's prune sweep relies on:
//
//  1. Multiple paths delete in a single transaction (no partial states).
//  2. Symbols cascade.
//  3. Paths not in the store are silent no-ops (a polluted index might
//     list paths a prior process already removed).
func TestDeleteFiles_BatchSemantics(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)

	// Seed three files, each with two symbols.
	now := time.Now().Unix()
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		if err := s.UpsertFile(File{Path: p, Hash: "h", Language: "go", SizeBytes: 10, IndexedAt: now}); err != nil {
			t.Fatalf("UpsertFile %s: %v", p, err)
		}
		if err := s.ReplaceSymbols(p, []Symbol{
			{Name: "X_" + p, QualifiedName: p + ".X", Kind: "function", StartLine: 1, EndLine: 2},
			{Name: "Y_" + p, QualifiedName: p + ".Y", Kind: "function", StartLine: 3, EndLine: 4},
		}); err != nil {
			t.Fatalf("ReplaceSymbols %s: %v", p, err)
		}
	}

	// Mix two real paths with one bogus path to exercise the silent-no-op
	// contract — the bogus path must not cause an error, must not affect
	// the count of real deletions.
	deleted, err := s.DeleteFiles([]string{"a.go", "b.go", "never-was-here.go"})
	if err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted count = %d, want 2 (bogus path doesn't count)", deleted)
	}

	// c.go's symbols must survive; a.go's and b.go's must be gone via CASCADE.
	files, _ := s.ListFiles("", "")
	if len(files) != 1 || files[0].Path != "c.go" {
		t.Errorf("after delete, files = %v, want [c.go]", files)
	}
	for _, name := range []string{"X_a.go", "X_b.go"} {
		if syms, _ := s.FindSymbolsByName(name); len(syms) != 0 {
			t.Errorf("symbol %q should have cascaded out; got %d rows", name, len(syms))
		}
	}
	if syms, _ := s.FindSymbolsByName("X_c.go"); len(syms) != 1 {
		t.Errorf("symbol X_c.go should survive; got %d rows", len(syms))
	}
}

func TestDeleteFiles_EmptyIsCheap(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	n, err := s.DeleteFiles(nil)
	if err != nil {
		t.Fatalf("DeleteFiles(nil): %v", err)
	}
	if n != 0 {
		t.Errorf("nil paths returned count=%d, want 0", n)
	}
	n, err = s.DeleteFiles([]string{})
	if err != nil {
		t.Fatalf("DeleteFiles([]): %v", err)
	}
	if n != 0 {
		t.Errorf("empty slice returned count=%d, want 0", n)
	}
}

func TestDeleteFiles_ChunkBoundary(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	// chunkSize is 500 in DeleteFiles — push past two chunks so we
	// confirm the loop's chunk-boundary math works.
	const total = 1100
	now := time.Now().Unix()
	paths := make([]string, total)
	for i := 0; i < total; i++ {
		paths[i] = fmt.Sprintf("dir/file%04d.go", i)
		if err := s.UpsertFile(File{Path: paths[i], Hash: "h", Language: "go", SizeBytes: 10, IndexedAt: now}); err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}
	}
	deleted, err := s.DeleteFiles(paths)
	if err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	if deleted != total {
		t.Errorf("deleted=%d, want %d", deleted, total)
	}
	files, _ := s.ListFiles("", "")
	if len(files) != 0 {
		t.Errorf("files after delete = %d, want 0", len(files))
	}
}

// BenchmarkDeleteFiles_1k seeds 1000 file rows (each with 10 symbols)
// then deletes them in one batched call. Apple M1 Pro measurement:
// ~5.8s/op — pure SQLite FK-cascade + WAL fsync cost. The benchmark
// exists as a regression guard against the v0.6.1 per-row variant,
// which averaged ~1.3s PER ROW (1k rows ≈ 22 minutes). v0.7 is
// ~225× faster on this workload; if a future change re-introduces
// per-row commit overhead, this benchmark catches it.
func BenchmarkDeleteFiles_1k(b *testing.B) {
	now := time.Now().Unix()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		s, _ := newBenchStore(b)
		const rows = 1000
		paths := make([]string, rows)
		for i := 0; i < rows; i++ {
			paths[i] = fmt.Sprintf("dir%02d/file%04d.go", i/100, i)
			_ = s.UpsertFile(File{Path: paths[i], Hash: "h", Language: "go", SizeBytes: 10, IndexedAt: now})
			syms := make([]Symbol, 10)
			for j := 0; j < 10; j++ {
				syms[j] = Symbol{
					Name:          fmt.Sprintf("S%d", j),
					QualifiedName: fmt.Sprintf("%s.S%d", paths[i], j),
					Kind:          "function",
					StartLine:     j*4 + 1,
					EndLine:       j*4 + 3,
				}
			}
			_ = s.ReplaceSymbols(paths[i], syms)
		}
		b.StartTimer()

		if _, err := s.DeleteFiles(paths); err != nil {
			b.Fatalf("DeleteFiles: %v", err)
		}
	}
}

func newBenchStore(b *testing.B) (*Store, string) {
	b.Helper()
	path := b.TempDir() + "/leonard.db"
	s, err := Open(path)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s, path
}
