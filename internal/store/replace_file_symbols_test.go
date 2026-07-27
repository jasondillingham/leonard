package store

import "testing"

// seedFile writes one file row plus a single symbol so a later
// ReplaceFileSymbols has prior state to either replace or roll back to.
func seedFile(t *testing.T, s *Store, path, hash string) {
	t.Helper()
	if err := s.UpsertFile(File{Path: path, Hash: hash, Language: "go", SizeBytes: 10, IndexedAt: 100}); err != nil {
		t.Fatalf("seed UpsertFile: %v", err)
	}
	if err := s.ReplaceSymbols(path, []Symbol{{
		Name: "Original", QualifiedName: "pkg.Original", Kind: "function",
		StartLine: 1, EndLine: 2, Exported: true,
	}}); err != nil {
		t.Fatalf("seed ReplaceSymbols: %v", err)
	}
}

// TestReplaceFileSymbols_RollsBackHashOnSymbolFailure is the regression
// test for the split-transaction index corruption.
//
// indexAbs used to commit the file row (carrying the NEW content hash)
// and then write symbols in a SEPARATE transaction. When the symbol
// write failed, the store was left holding the new hash beside the old
// parse's symbols — and because indexAbs skips any file whose stored
// hash matches on disk, that pairing was never revisited. The index
// served stale symbols as ground truth, permanently.
//
// The assertion that matters is on the HASH: it must still be the old
// value, because that is what forces the next run to re-parse instead
// of hash-skipping. A test that only checked the symbols would pass
// against the buggy two-transaction code as well.
func TestReplaceFileSymbols_RollsBackHashOnSymbolFailure(t *testing.T) {
	s, _ := newTestStore(t)
	seedFile(t, s, "a.go", "hash-old")

	// A ParentID that matches no tag in this batch is passed through as
	// a literal symbol ID; 999999 does not exist, so the insert trips
	// the symbols.parent_id foreign key and the batch fails partway.
	bogusParent := int64(999999)
	err := s.ReplaceFileSymbols(
		File{Path: "a.go", Hash: "hash-new", Language: "go", SizeBytes: 20, IndexedAt: 200},
		[]Symbol{{
			Name: "Broken", QualifiedName: "pkg.Broken", Kind: "function",
			StartLine: 1, EndLine: 2, ParentID: &bogusParent,
		}},
	)
	if err == nil {
		t.Fatal("expected the symbol write to fail on the parent_id foreign key")
	}

	got, found, getErr := s.GetFile("a.go")
	if getErr != nil {
		t.Fatalf("GetFile: %v", getErr)
	}
	if !found {
		t.Fatal("file row vanished; the rollback should preserve the prior row")
	}
	if got.Hash != "hash-old" {
		t.Errorf("hash must roll back to %q, got %q — the new hash paired with stale symbols is the corruption this fix prevents", "hash-old", got.Hash)
	}
	if got.SizeBytes != 10 || got.IndexedAt != 100 {
		t.Errorf("whole row should roll back, got size=%d indexed_at=%d", got.SizeBytes, got.IndexedAt)
	}

	syms, symErr := s.FindSymbolsByName("Original")
	if symErr != nil {
		t.Fatalf("FindSymbolsByName: %v", symErr)
	}
	if len(syms) != 1 {
		t.Errorf("prior symbols should survive the rollback, got %d", len(syms))
	}
}

// TestReplaceFileSymbols_CommitsBothOnSuccess pins the happy path: the
// file row and its symbols land together, so a caller that sees the new
// hash can rely on the symbols beside it being from the same parse.
func TestReplaceFileSymbols_CommitsBothOnSuccess(t *testing.T) {
	s, _ := newTestStore(t)
	seedFile(t, s, "a.go", "hash-old")

	if err := s.ReplaceFileSymbols(
		File{Path: "a.go", Hash: "hash-new", Language: "go", SizeBytes: 20, IndexedAt: 200},
		[]Symbol{{
			Name: "Fresh", QualifiedName: "pkg.Fresh", Kind: "function",
			StartLine: 1, EndLine: 2, Exported: true,
		}},
	); err != nil {
		t.Fatalf("ReplaceFileSymbols: %v", err)
	}

	got, found, err := s.GetFile("a.go")
	if err != nil || !found {
		t.Fatalf("GetFile: err=%v found=%v", err, found)
	}
	if got.Hash != "hash-new" {
		t.Errorf("hash: want %q, got %q", "hash-new", got.Hash)
	}

	fresh, err := s.FindSymbolsByName("Fresh")
	if err != nil {
		t.Fatalf("FindSymbolsByName(Fresh): %v", err)
	}
	if len(fresh) != 1 {
		t.Errorf("new symbol should be present, got %d", len(fresh))
	}
	stale, err := s.FindSymbolsByName("Original")
	if err != nil {
		t.Fatalf("FindSymbolsByName(Original): %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("prior symbols should be cleared, got %d", len(stale))
	}
}

// TestReplaceFileSymbols_CreatesRowForNewFile covers the insert path —
// the file row must be written before the symbols, since symbols.file_path
// is a foreign key into files(path).
func TestReplaceFileSymbols_CreatesRowForNewFile(t *testing.T) {
	s, _ := newTestStore(t)

	if err := s.ReplaceFileSymbols(
		File{Path: "new.go", Hash: "h", Language: "go", SizeBytes: 5, IndexedAt: 300},
		[]Symbol{{Name: "N", QualifiedName: "pkg.N", Kind: "function", StartLine: 1, EndLine: 1}},
	); err != nil {
		t.Fatalf("ReplaceFileSymbols on a fresh path: %v", err)
	}

	if _, found, err := s.GetFile("new.go"); err != nil || !found {
		t.Fatalf("file row missing: err=%v found=%v", err, found)
	}
	syms, err := s.FindSymbolsByName("N")
	if err != nil {
		t.Fatalf("FindSymbolsByName: %v", err)
	}
	if len(syms) != 1 {
		t.Errorf("want 1 symbol, got %d", len(syms))
	}
}

// TestReplaceFileSymbols_RejectsEmptyPath guards the argument check.
func TestReplaceFileSymbols_RejectsEmptyPath(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.ReplaceFileSymbols(File{Hash: "h"}, nil); err == nil {
		t.Error("expected an error for an empty path")
	}
}
