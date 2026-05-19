package store

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// newTestStore opens a Store in t.TempDir() and registers Close as cleanup.
// The returned path is the on-disk DB so individual tests can reopen it.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leonard.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, path
}

func TestOpenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leonard.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Seed a row so we can prove the second Open didn't wipe state.
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h1", Language: "go", SizeBytes: 10, IndexedAt: 100}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, ok, err := s2.GetFile("a.go")
	if err != nil || !ok {
		t.Fatalf("GetFile after reopen: ok=%v err=%v", ok, err)
	}
	if got.Hash != "h1" || got.IndexedAt != 100 {
		t.Fatalf("unexpected file after reopen: %+v", got)
	}

	// Verify schema_version still equals current.
	var v string
	if err := s2.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version=%q want %d", v, schemaVersion)
	}
}

func TestOpenWALEnabled(t *testing.T) {
	s, _ := newTestStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q want wal", mode)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") want error, got nil")
	}
}

func TestUpsertAndGetFile(t *testing.T) {
	s, _ := newTestStore(t)

	cases := []struct {
		name string
		in   File
	}{
		{"insert", File{Path: "pkg/a.go", Hash: "abc", Language: "go", SizeBytes: 42, IndexedAt: 1000}},
		{"overwrite", File{Path: "pkg/a.go", Hash: "def", Language: "go", SizeBytes: 99, IndexedAt: 2000}},
		{"different path", File{Path: "pkg/b.go", Hash: "xyz", Language: "go", SizeBytes: 5, IndexedAt: 3000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.UpsertFile(tc.in); err != nil {
				t.Fatalf("UpsertFile: %v", err)
			}
			got, ok, err := s.GetFile(tc.in.Path)
			if err != nil {
				t.Fatalf("GetFile: %v", err)
			}
			if !ok {
				t.Fatalf("GetFile %q missing", tc.in.Path)
			}
			if got != tc.in {
				t.Fatalf("GetFile got %+v want %+v", got, tc.in)
			}
		})
	}
}

func TestUpsertFileFillsIndexedAt(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "x.go", Hash: "h", Language: "go", SizeBytes: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	got, _, err := s.GetFile("x.go")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.IndexedAt == 0 {
		t.Fatal("IndexedAt should have been populated when zero")
	}
}

func TestUpsertFileRejectsEmptyPath(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{}); err == nil {
		t.Fatal("UpsertFile{} want error")
	}
}

func TestGetFileMissing(t *testing.T) {
	s, _ := newTestStore(t)
	_, ok, err := s.GetFile("nope.go")
	if err != nil {
		t.Fatalf("GetFile err: %v", err)
	}
	if ok {
		t.Fatal("GetFile missing should report ok=false")
	}
}

func TestReplaceSymbolsBasic(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h", Language: "go", IndexedAt: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	first := []Symbol{
		{Name: "Open", QualifiedName: "store.Open", Kind: "function", Signature: "func Open(path string) (*Store, error)", StartLine: 5, EndLine: 12, Exported: true},
		{Name: "helper", QualifiedName: "store.helper", Kind: "function", Signature: "func helper()", StartLine: 14, EndLine: 16},
	}
	if err := s.ReplaceSymbols("a.go", first); err != nil {
		t.Fatalf("ReplaceSymbols first: %v", err)
	}

	got, err := s.FindSymbolsByName("Open")
	if err != nil {
		t.Fatalf("FindSymbolsByName: %v", err)
	}
	if len(got) != 1 || got[0].QualifiedName != "store.Open" || !got[0].Exported {
		t.Fatalf("unexpected matches: %+v", got)
	}

	// Replacing should remove the old rows entirely.
	replacement := []Symbol{
		{Name: "OpenV2", QualifiedName: "store.OpenV2", Kind: "function", StartLine: 1, EndLine: 2, Exported: true},
	}
	if err := s.ReplaceSymbols("a.go", replacement); err != nil {
		t.Fatalf("ReplaceSymbols replace: %v", err)
	}
	if got, err := s.FindSymbolsByName("Open"); err != nil || len(got) != 0 {
		t.Fatalf("Open should be gone, got %+v err=%v", got, err)
	}
	if got, err := s.FindSymbolsByName("helper"); err != nil || len(got) != 0 {
		t.Fatalf("helper should be gone, got %+v err=%v", got, err)
	}
	if got, err := s.FindSymbolsByName("OpenV2"); err != nil || len(got) != 1 {
		t.Fatalf("OpenV2 should exist, got %+v err=%v", got, err)
	}
}

func TestReplaceSymbolsBatchParentRefs(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h", Language: "go", IndexedAt: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	parentTag := int64(1)
	childParent := parentTag
	syms := []Symbol{
		{ID: parentTag, Name: "Store", QualifiedName: "store.Store", Kind: "type", StartLine: 1, EndLine: 3, Exported: true},
		{Name: "Open", QualifiedName: "store.Open", Kind: "method", StartLine: 5, EndLine: 9, Exported: true, ParentID: &childParent},
	}
	if err := s.ReplaceSymbols("a.go", syms); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}

	parents, err := s.FindSymbolsByName("Store")
	if err != nil || len(parents) != 1 {
		t.Fatalf("find parent: %+v err=%v", parents, err)
	}
	children, err := s.FindSymbolsByName("Open")
	if err != nil || len(children) != 1 {
		t.Fatalf("find child: %+v err=%v", children, err)
	}
	child := children[0]
	if child.ParentID == nil {
		t.Fatalf("child ParentID nil, want %d", parents[0].ID)
	}
	if *child.ParentID != parents[0].ID {
		t.Fatalf("child ParentID=%d want %d", *child.ParentID, parents[0].ID)
	}
}

func TestReplaceSymbolsCascadesOnFileDelete(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h", Language: "go", IndexedAt: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	syms := []Symbol{
		{Name: "Foo", QualifiedName: "pkg.Foo", Kind: "function", StartLine: 1, EndLine: 2, Exported: true},
	}
	if err := s.ReplaceSymbols("a.go", syms); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM files WHERE path = ?`, "a.go"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	got, err := s.FindSymbolsByName("Foo")
	if err != nil {
		t.Fatalf("FindSymbolsByName: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("symbols should have cascaded, got %+v", got)
	}
}

func TestReplaceSymbolsRejectsUnknownFile(t *testing.T) {
	s, _ := newTestStore(t)
	// Foreign key enforcement should refuse inserts for an unknown file.
	err := s.ReplaceSymbols("ghost.go", []Symbol{{Name: "Foo", QualifiedName: "pkg.Foo", Kind: "function", StartLine: 1, EndLine: 1, Exported: true}})
	if err == nil {
		t.Fatal("ReplaceSymbols against unknown file should error")
	}
}

func TestFindSymbolsByQuery(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h", Language: "go", IndexedAt: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	syms := []Symbol{
		{Name: "Open", QualifiedName: "store.Open", Kind: "function", StartLine: 1, EndLine: 2, Exported: true},
		{Name: "OpenFile", QualifiedName: "store.OpenFile", Kind: "function", StartLine: 3, EndLine: 4, Exported: true},
		{Name: "close", QualifiedName: "store.close", Kind: "function", StartLine: 5, EndLine: 6},
	}
	if err := s.ReplaceSymbols("a.go", syms); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}

	cases := []struct {
		name      string
		query     string
		limit     int
		wantNames []string
	}{
		{"substring on name", "pen", 0, []string{"Open", "OpenFile"}},
		{"matches qualified prefix", "store.O", 0, []string{"Open", "OpenFile"}},
		{"no match", "xyz", 0, nil},
		{"limit clamps results", "", 1, []string{"Open"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.FindSymbolsByQuery(tc.query, tc.limit)
			if err != nil {
				t.Fatalf("FindSymbolsByQuery: %v", err)
			}
			var names []string
			for _, g := range got {
				names = append(names, g.Name)
			}
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("names=%v want %v", names, tc.wantNames)
			}
		})
	}
}

func TestFindSymbolsByQueryEscapesWildcards(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertFile(File{Path: "a.go", Hash: "h", Language: "go", IndexedAt: 1}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	syms := []Symbol{
		{Name: "literal_underscore", QualifiedName: "pkg.literal_underscore", Kind: "function", StartLine: 1, EndLine: 1, Exported: false},
		{Name: "noUnderscore", QualifiedName: "pkg.noUnderscore", Kind: "function", StartLine: 2, EndLine: 2, Exported: true},
	}
	if err := s.ReplaceSymbols("a.go", syms); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	// If `_` were treated as a wildcard, "noUnderscore" would also match.
	got, err := s.FindSymbolsByQuery("_underscore", 0)
	if err != nil {
		t.Fatalf("FindSymbolsByQuery: %v", err)
	}
	if len(got) != 1 || got[0].Name != "literal_underscore" {
		t.Fatalf("wildcard escape failed: %+v", got)
	}
}

func TestListFiles(t *testing.T) {
	s, _ := newTestStore(t)
	files := []File{
		{Path: "internal/store/store.go", Hash: "a", Language: "go", SizeBytes: 1, IndexedAt: 1},
		{Path: "internal/store/store_test.go", Hash: "b", Language: "go", SizeBytes: 2, IndexedAt: 2},
		{Path: "internal/parse/golang.go", Hash: "c", Language: "go", SizeBytes: 3, IndexedAt: 3},
		{Path: "README.md", Hash: "d", Language: "markdown", SizeBytes: 4, IndexedAt: 4},
	}
	for _, f := range files {
		if err := s.UpsertFile(f); err != nil {
			t.Fatalf("UpsertFile %q: %v", f.Path, err)
		}
	}

	cases := []struct {
		name      string
		pattern   string
		lang      string
		wantPaths []string
	}{
		{"all", "", "", []string{"README.md", "internal/parse/golang.go", "internal/store/store.go", "internal/store/store_test.go"}},
		{"by language", "", "go", []string{"internal/parse/golang.go", "internal/store/store.go", "internal/store/store_test.go"}},
		{"by glob", "internal/store/*.go", "", []string{"internal/store/store.go", "internal/store/store_test.go"}},
		{"glob + language", "internal/*/*.go", "go", []string{"internal/parse/golang.go", "internal/store/store.go", "internal/store/store_test.go"}},
		{"no match", "no-such/*", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListFiles(tc.pattern, tc.lang)
			if err != nil {
				t.Fatalf("ListFiles: %v", err)
			}
			var paths []string
			for _, f := range got {
				paths = append(paths, f.Path)
			}
			if !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Fatalf("paths=%v want %v", paths, tc.wantPaths)
			}
		})
	}
}

func TestRecordAndGetDecisions(t *testing.T) {
	s, _ := newTestStore(t)
	mustRecord := func(d Decision) int64 {
		t.Helper()
		id, err := s.RecordDecision(d)
		if err != nil {
			t.Fatalf("RecordDecision: %v", err)
		}
		return id
	}
	id1 := mustRecord(Decision{Topic: "db", Choice: "sqlite", Reasoning: "embedded", RecordedAt: 1000})
	id2 := mustRecord(Decision{Topic: "db", Choice: "modernc", Reasoning: "no cgo", RecordedAt: 2000})
	id3 := mustRecord(Decision{Topic: "parser", Choice: "tree-sitter", Reasoning: "incremental", RecordedAt: 1500})

	if id1 == id2 || id2 == id3 {
		t.Fatalf("ids should be distinct: %d %d %d", id1, id2, id3)
	}

	cases := []struct {
		name    string
		topic   string
		since   int64
		limit   int
		wantIDs []int64
	}{
		{"all", "", 0, 0, []int64{id2, id3, id1}},
		{"by topic", "db", 0, 0, []int64{id2, id1}},
		{"since cutoff", "", 1500, 0, []int64{id2, id3}},
		{"limit", "", 0, 1, []int64{id2}},
		{"no match", "missing", 0, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.GetDecisions(tc.topic, tc.since, tc.limit)
			if err != nil {
				t.Fatalf("GetDecisions: %v", err)
			}
			var ids []int64
			for _, d := range got {
				ids = append(ids, d.ID)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("ids=%v want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestRecordDecisionRejectsEmptyTopic(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.RecordDecision(Decision{Choice: "x", Reasoning: "y"}); err == nil {
		t.Fatal("RecordDecision empty topic should error")
	}
}

func TestRecordDecisionFillsRecordedAt(t *testing.T) {
	s, _ := newTestStore(t)
	id, err := s.RecordDecision(Decision{Topic: "x", Choice: "y", Reasoning: "z"})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.GetDecisions("x", 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("GetDecisions: %+v err=%v", got, err)
	}
	if got[0].ID != id || got[0].RecordedAt == 0 {
		t.Fatalf("RecordedAt not auto-filled: %+v", got[0])
	}
}

func TestSupersedeDecision(t *testing.T) {
	s, _ := newTestStore(t)
	orig, err := s.RecordDecision(Decision{Topic: "lib", Choice: "A", Reasoning: "first", RecordedAt: 1000})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	next, err := s.SupersedeDecision(orig, "B", "switched")
	if err != nil {
		t.Fatalf("SupersedeDecision: %v", err)
	}
	if next == orig {
		t.Fatalf("supersede should mint new id, got %d", next)
	}

	all, err := s.GetDecisions("lib", 0, 0)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expect 2 decisions, got %+v", all)
	}
	var origRow, newRow *Decision
	for i, d := range all {
		switch d.ID {
		case orig:
			origRow = &all[i]
		case next:
			newRow = &all[i]
		}
	}
	if origRow == nil || newRow == nil {
		t.Fatalf("missing rows: %+v", all)
	}
	if origRow.SupersededBy == nil || *origRow.SupersededBy != next {
		t.Fatalf("orig.SupersededBy=%v want %d", origRow.SupersededBy, next)
	}
	if newRow.SupersededBy != nil {
		t.Fatalf("new row should not be superseded, got %v", *newRow.SupersededBy)
	}
	if newRow.Topic != "lib" {
		t.Fatalf("new row topic=%q want lib", newRow.Topic)
	}
}

func TestSupersedeDecisionErrors(t *testing.T) {
	s, _ := newTestStore(t)

	t.Run("missing id", func(t *testing.T) {
		if _, err := s.SupersedeDecision(999, "x", "y"); err == nil {
			t.Fatal("expected error for missing decision")
		}
	})

	t.Run("already superseded", func(t *testing.T) {
		orig, err := s.RecordDecision(Decision{Topic: "t", Choice: "a", Reasoning: "r"})
		if err != nil {
			t.Fatalf("RecordDecision: %v", err)
		}
		if _, err := s.SupersedeDecision(orig, "b", "r2"); err != nil {
			t.Fatalf("first SupersedeDecision: %v", err)
		}
		if _, err := s.SupersedeDecision(orig, "c", "r3"); err == nil {
			t.Fatal("expected error superseding twice")
		}
	})
}

func TestClaimsLifecycle(t *testing.T) {
	s, _ := newTestStore(t)

	c1, err := s.RecordClaim(Claim{SessionID: "sess-1", Claim: "go vet clean", Evidence: "exit 0", Verified: true, RecordedAt: 1000})
	if err != nil {
		t.Fatalf("RecordClaim: %v", err)
	}
	c2, err := s.RecordClaim(Claim{SessionID: "sess-1", Claim: "tests pass", Evidence: "FAIL", Verified: false, RecordedAt: 2000})
	if err != nil {
		t.Fatalf("RecordClaim: %v", err)
	}
	c3, err := s.RecordClaim(Claim{SessionID: "sess-2", Claim: "build clean", Evidence: "exit 1", Verified: false, RecordedAt: 3000})
	if err != nil {
		t.Fatalf("RecordClaim: %v", err)
	}
	if c1 == c2 || c2 == c3 {
		t.Fatalf("ids should be distinct: %d %d %d", c1, c2, c3)
	}

	all, err := s.GetUnverifiedClaims("")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims all: %v", err)
	}
	if len(all) != 2 || all[0].ID != c3 || all[1].ID != c2 {
		t.Fatalf("unverified all wrong: %+v", all)
	}

	scoped, err := s.GetUnverifiedClaims("sess-1")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims sess-1: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != c2 {
		t.Fatalf("scoped wrong: %+v", scoped)
	}

	empty, err := s.GetUnverifiedClaims("nobody")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims missing: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no rows, got %+v", empty)
	}
}

func TestClaimsSupersession(t *testing.T) {
	s, _ := newTestStore(t)
	const path = "/repo/cmd/init.go"

	fail1, err := s.RecordClaim(Claim{SessionID: "sess", Claim: "vet fail", Evidence: "x", FilePath: path, RecordedAt: 100})
	if err != nil {
		t.Fatalf("RecordClaim fail1: %v", err)
	}
	fail2, err := s.RecordClaim(Claim{SessionID: "sess", Claim: "vet fail again", Evidence: "y", FilePath: path, RecordedAt: 200})
	if err != nil {
		t.Fatalf("RecordClaim fail2: %v", err)
	}
	otherFile, err := s.RecordClaim(Claim{SessionID: "sess", Claim: "unrelated", Evidence: "z", FilePath: "/repo/other.go", RecordedAt: 300})
	if err != nil {
		t.Fatalf("RecordClaim otherFile: %v", err)
	}
	pass, err := s.RecordClaim(Claim{SessionID: "sess", Claim: "vet ok", Evidence: "ok", FilePath: path, Verified: true, RecordedAt: 400})
	if err != nil {
		t.Fatalf("RecordClaim pass: %v", err)
	}

	n, err := s.SupersedeClaimsForFile(path, pass)
	if err != nil {
		t.Fatalf("SupersedeClaimsForFile: %v", err)
	}
	if n != 2 {
		t.Errorf("supersede count = %d, want 2", n)
	}

	// Default list hides superseded rows; otherFile remains visible.
	visible, err := s.GetUnverifiedClaims("")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != otherFile {
		t.Errorf("visible = %+v, want only id=%d", visible, otherFile)
	}

	// Include-history opt-in returns all three unverified rows.
	all, err := s.GetUnverifiedClaimsAll("")
	if err != nil {
		t.Fatalf("GetUnverifiedClaimsAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all rows = %d, want 3", len(all))
	}
	supersededIDs := map[int64]bool{fail1: true, fail2: true}
	for _, c := range all {
		if supersededIDs[c.ID] {
			if c.SupersededByClaimID == nil || *c.SupersededByClaimID != pass {
				t.Errorf("claim %d superseded_by = %v, want %d", c.ID, c.SupersededByClaimID, pass)
			}
		} else if c.SupersededByClaimID != nil {
			t.Errorf("claim %d unexpectedly superseded by %d", c.ID, *c.SupersededByClaimID)
		}
	}

	// Re-running supersede is a no-op (claims are already linked).
	if n, err := s.SupersedeClaimsForFile(path, pass); err != nil || n != 0 {
		t.Errorf("second SupersedeClaimsForFile = (%d, %v), want (0, nil)", n, err)
	}

	// Empty filePath is a defensive no-op (callers that don't know the path
	// shouldn't accidentally supersede everything).
	if n, err := s.SupersedeClaimsForFile("", 999); err != nil || n != 0 {
		t.Errorf("empty filePath SupersedeClaimsForFile = (%d, %v), want (0, nil)", n, err)
	}
}

// session_id is opaque to Leonard; empty means "unscoped" and must be accepted.
// The unscoped row should round-trip through GetUnverifiedClaims when no
// session filter is supplied.
func TestRecordClaimAcceptsEmptySession(t *testing.T) {
	s, _ := newTestStore(t)
	id, err := s.RecordClaim(Claim{Claim: "x", Evidence: "y"})
	if err != nil {
		t.Fatalf("RecordClaim empty session: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}
	got, err := s.GetUnverifiedClaims("")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims: %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].SessionID != "" {
		t.Fatalf("expected one unscoped claim, got %+v", got)
	}
}

func TestRecordClaimFillsRecordedAt(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.RecordClaim(Claim{SessionID: "s", Claim: "c", Evidence: "e"}); err != nil {
		t.Fatalf("RecordClaim: %v", err)
	}
	got, err := s.GetUnverifiedClaims("s")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetUnverifiedClaims: %+v err=%v", got, err)
	}
	if got[0].RecordedAt == 0 {
		t.Fatal("RecordedAt not auto-filled")
	}
}

// TestConcurrentReadsAndWrites exercises the store from multiple goroutines so
// `go test -race` flags any data races and so WAL mode's concurrent-reader
// promise is exercised against a real workload.
func TestConcurrentReadsAndWrites(t *testing.T) {
	s, _ := newTestStore(t)

	const (
		writers     = 4
		readers     = 8
		perWorker   = 25
		filePrefix  = "f"
	)

	// Seed a file each writer can attach symbols to (FK requires the file row).
	for i := 0; i < writers; i++ {
		if err := s.UpsertFile(File{
			Path:      filePrefix + string(rune('a'+i)) + ".go",
			Hash:      "seed",
			Language:  "go",
			SizeBytes: 1,
			IndexedAt: int64(1000 + i),
		}); err != nil {
			t.Fatalf("seed file %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers+readers)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := filePrefix + string(rune('a'+idx)) + ".go"
			for j := 0; j < perWorker; j++ {
				syms := []Symbol{
					{Name: "Sym", QualifiedName: "pkg.Sym", Kind: "function", StartLine: j, EndLine: j, Exported: true},
				}
				if err := s.ReplaceSymbols(path, syms); err != nil {
					errCh <- err
					return
				}
				if _, err := s.RecordClaim(Claim{SessionID: "race", Claim: "vet", Evidence: "ok"}); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if _, err := s.FindSymbolsByName("Sym"); err != nil {
					errCh <- err
					return
				}
				if _, err := s.ListFiles("", "go"); err != nil {
					errCh <- err
					return
				}
				if _, err := s.GetUnverifiedClaims("race"); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("worker error: %v", err)
	}
}
