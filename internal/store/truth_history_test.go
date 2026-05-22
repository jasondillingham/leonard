package store_test

import (
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

func openTruthHistoryStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "leonard.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGetTruthHistory_RejectsEmptyPath(t *testing.T) {
	s := openTruthHistoryStore(t)
	if _, err := s.GetTruthHistory("", 10); err == nil {
		t.Error("empty path should error")
	}
}

func TestGetTruthHistory_ReturnsOnlyEntriesForPath(t *testing.T) {
	s := openTruthHistoryStore(t)

	// Two decisions for file A, one for file B, one without
	// TruthChange. GetTruthHistory("A") should return only the two
	// A entries.
	mustRecord := func(topic string, tc *store.TruthChange) {
		if _, err := s.RecordDecision(store.Decision{
			Topic:       topic,
			TruthChange: tc,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	mustRecord("first-A", &store.TruthChange{Scope: "domain", Files: []string{"ground-truth/A.md"}, MotivatedBy: "initial A rule"})
	mustRecord("only-B", &store.TruthChange{Scope: "domain", Files: []string{"ground-truth/B.md"}, MotivatedBy: "B"})
	mustRecord("second-A", &store.TruthChange{Scope: "domain", Files: []string{"ground-truth/A.md"}, MotivatedBy: "tighten A"})
	mustRecord("non-truth", nil)

	got, err := s.GetTruthHistory("ground-truth/A.md", 10)
	if err != nil {
		t.Fatalf("GetTruthHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("count: want 2, got %d (%v)", len(got), got)
	}
	// Oldest-first ordering: first-A before second-A.
	if got[0].Topic != "first-A" {
		t.Errorf("ordering: want first-A first, got %q", got[0].Topic)
	}
	if got[1].Topic != "second-A" {
		t.Errorf("ordering: want second-A second, got %q", got[1].Topic)
	}
}

func TestGetTruthHistory_MultiFileEntry(t *testing.T) {
	s := openTruthHistoryStore(t)

	// A single decision whose Files slice covers both A and B
	// shows up when querying for either.
	if _, err := s.RecordDecision(store.Decision{
		Topic: "joint",
		TruthChange: &store.TruthChange{
			Scope: "domain",
			Files: []string{"ground-truth/A.md", "ground-truth/B.md"},
		},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	for _, path := range []string{"ground-truth/A.md", "ground-truth/B.md"} {
		got, err := s.GetTruthHistory(path, 10)
		if err != nil {
			t.Fatalf("GetTruthHistory(%s): %v", path, err)
		}
		if len(got) != 1 {
			t.Errorf("count for %s: want 1, got %d", path, len(got))
		}
	}
}

func TestGetTruthHistory_RespectsLimit(t *testing.T) {
	s := openTruthHistoryStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.RecordDecision(store.Decision{
			Topic: "loop",
			TruthChange: &store.TruthChange{
				Scope: "domain",
				Files: []string{"ground-truth/A.md"},
			},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, err := s.GetTruthHistory("ground-truth/A.md", 3)
	if err != nil {
		t.Fatalf("GetTruthHistory: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit: want 3, got %d", len(got))
	}
}

func TestGetTruthHistory_EmptyForUnknownPath(t *testing.T) {
	s := openTruthHistoryStore(t)
	if _, err := s.RecordDecision(store.Decision{
		Topic: "A",
		TruthChange: &store.TruthChange{Scope: "domain", Files: []string{"ground-truth/A.md"}},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := s.GetTruthHistory("ground-truth/nowhere.md", 10)
	if err != nil {
		t.Fatalf("GetTruthHistory: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unknown path: want 0, got %d", len(got))
	}
}
