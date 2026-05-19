package mcp_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
)

// memChangesStore embeds *MemStore so it satisfies SymbolStore unchanged
// and adds a thread-safe slice of changes so it also satisfies
// ChangesStore. Mirrors memDecisionStore in decisions_test.go.
type memChangesStore struct {
	*leonardmcp.MemStore

	mu      sync.Mutex
	changes []leonardmcp.FileRecord
}

func newMemChangesStore() *memChangesStore {
	return &memChangesStore{MemStore: leonardmcp.NewMemStore()}
}

// recordChange seeds a change with a deterministic indexed_at, replacing
// any prior entry for the same path (mirroring UpsertFile).
func (m *memChangesStore) recordChange(path, lang string, size, indexedAt int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := leonardmcp.FileRecord{Path: path, Language: lang, SizeBytes: size, IndexedAt: indexedAt}
	for i, existing := range m.changes {
		if existing.Path == path {
			m.changes[i] = rec
			return
		}
	}
	m.changes = append(m.changes, rec)
}

func (m *memChangesStore) ListFilesIndexedSince(_ context.Context, since int64, limit int) ([]leonardmcp.FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := make([]leonardmcp.FileRecord, 0, len(m.changes))
	for _, c := range m.changes {
		if c.IndexedAt < since {
			continue
		}
		filtered = append(filtered, c)
	}
	// Newest first (matches store.ListFilesIndexedSince ordering).
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].IndexedAt > filtered[j].IndexedAt
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// ---- tools/list ----

func TestToolsListIncludesRecentChanges(t *testing.T) {
	sess := newSession(t, newMemChangesStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	if !got["recent_changes"] {
		t.Errorf("tools/list missing recent_changes (got %v)", keys(got))
	}
	for _, want := range []string{"verify_symbol", "find_symbol", "list_files"} {
		if !got[want] {
			t.Errorf("phase-1 tool missing after changes wiring: %q", want)
		}
	}
}

// When the store doesn't satisfy ChangesStore (phase-1 fixture), the
// recent_changes tool must NOT be registered. Guards the type-assertion
// branch in register().
func TestChangesToolAbsentWithoutChangesStore(t *testing.T) {
	sess := newSession(t, leonardmcp.NewMemStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	if got["recent_changes"] {
		t.Errorf("recent_changes registered against a symbol-only store")
	}
}

// ---- recent_changes ----

func TestRecentChangesNewestFirst(t *testing.T) {
	store := newMemChangesStore()
	store.recordChange("a.go", "go", 10, 1000)
	store.recordChange("b.go", "go", 20, 2000)
	store.recordChange("c.py", "python", 30, 3000)

	sess := newSession(t, store)
	res := callTool(t, sess, "recent_changes", map[string]any{})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes

	if len(got) != 3 {
		t.Fatalf("expected 3 changes, got %d: %+v", len(got), got)
	}
	if got[0].Path != "c.py" || got[1].Path != "b.go" || got[2].Path != "a.go" {
		t.Errorf("expected newest-first ordering, got %+v", got)
	}
	if got[0].IndexedAt != 3000 || got[0].Language != "python" || got[0].SizeBytes != 30 {
		t.Errorf("change entry round-trip mismatch: %+v", got[0])
	}
}

func TestRecentChangesSinceFilter(t *testing.T) {
	store := newMemChangesStore()
	store.recordChange("old1.go", "go", 1, 100)
	store.recordChange("old2.go", "go", 1, 500)
	store.recordChange("new.go", "go", 1, 1000)

	sess := newSession(t, store)
	res := callTool(t, sess, "recent_changes", map[string]any{"since": 600})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 1 || got[0].Path != "new.go" {
		t.Fatalf("since filter wrong: %+v", got)
	}
}

func TestRecentChangesLimitDefault(t *testing.T) {
	store := newMemChangesStore()
	for i := 0; i < 75; i++ {
		store.recordChange(fileN(i), "go", 1, int64(i+1))
	}
	sess := newSession(t, store)

	res := callTool(t, sess, "recent_changes", map[string]any{})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 50 {
		t.Errorf("expected default limit of 50, got %d", len(got))
	}
}

func TestRecentChangesLimitCap(t *testing.T) {
	store := newMemChangesStore()
	for i := 0; i < 600; i++ {
		store.recordChange(fileN(i), "go", 1, int64(i+1))
	}
	sess := newSession(t, store)

	res := callTool(t, sess, "recent_changes", map[string]any{"limit": 5000})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 500 {
		t.Errorf("expected limit cap of 500, got %d", len(got))
	}
}

func TestRecentChangesExplicitLimit(t *testing.T) {
	store := newMemChangesStore()
	for i := 0; i < 25; i++ {
		store.recordChange(fileN(i), "go", 1, int64(i+1))
	}
	sess := newSession(t, store)

	res := callTool(t, sess, "recent_changes", map[string]any{"limit": 5})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 5 {
		t.Errorf("expected limit=5 honored, got %d", len(got))
	}
}

func TestRecentChangesEmpty(t *testing.T) {
	sess := newSession(t, newMemChangesStore())
	res := callTool(t, sess, "recent_changes", map[string]any{})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res)
	if len(got.Changes) != 0 {
		t.Errorf("expected empty slice, got %+v", got.Changes)
	}
}

// ---- real store.Store round-trip ----

// Acceptance criterion: indexing a project, then mutating two files,
// then recent_changes returns those two files first in descending
// indexed_at order — against the real store.Store via StoreAdapter.
func TestRecentChangesAgainstRealStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Initial index of three files.
	for i, p := range []string{"a.go", "b.go", "c.py"} {
		lang := "go"
		if filepath.Ext(p) == ".py" {
			lang = "python"
		}
		if err := st.UpsertFile(store.File{
			Path:      p,
			Hash:      "h1",
			Language:  lang,
			SizeBytes: int64(10 * (i + 1)),
			IndexedAt: 1000,
		}); err != nil {
			t.Fatalf("UpsertFile %q: %v", p, err)
		}
	}

	// Mutate two files at later timestamps — these should bubble up first.
	if err := st.UpsertFile(store.File{Path: "b.go", Hash: "h2", Language: "go", SizeBytes: 99, IndexedAt: 2000}); err != nil {
		t.Fatalf("mutate b: %v", err)
	}
	if err := st.UpsertFile(store.File{Path: "c.py", Hash: "h2", Language: "python", SizeBytes: 88, IndexedAt: 3000}); err != nil {
		t.Fatalf("mutate c: %v", err)
	}

	adapter := leonardmcp.NewStoreAdapter(st)
	sess := newSession(t, adapter)

	// since cutoff above the initial index keeps only the two mutations.
	res := callTool(t, sess, "recent_changes", map[string]any{"since": 1500})
	got := decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 2 {
		t.Fatalf("expected 2 mutated files, got %d: %+v", len(got), got)
	}
	if got[0].Path != "c.py" || got[1].Path != "b.go" {
		t.Errorf("expected c.py then b.go, got %+v", got)
	}
	if got[0].IndexedAt != 3000 || got[1].IndexedAt != 2000 {
		t.Errorf("indexed_at not round-tripped: %+v", got)
	}
	if got[0].SizeBytes != 88 || got[1].SizeBytes != 99 {
		t.Errorf("size_bytes not round-tripped: %+v", got)
	}

	// No since filter — all three rows in descending indexed_at order.
	res = callTool(t, sess, "recent_changes", map[string]any{})
	got = decodeResult[leonardmcp.RecentChangesOutput](t, res).Changes
	if len(got) != 3 {
		t.Fatalf("expected 3 rows total, got %d: %+v", len(got), got)
	}
	if got[0].Path != "c.py" || got[1].Path != "b.go" || got[2].Path != "a.go" {
		t.Errorf("ordering wrong: %+v", got)
	}
}

func fileN(i int) string {
	return fmt.Sprintf("f%d.go", i)
}
