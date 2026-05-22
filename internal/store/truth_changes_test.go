package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jasondillingham/leonard/internal/store"
)

func openTruthChangesStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "leonard.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGetTruthChanges_OldestFirst(t *testing.T) {
	s := openTruthChangesStore(t)
	now := time.Now().Unix()

	// Insert out of order.
	for _, r := range []struct {
		topic string
		t     int64
	}{
		{"third", now + 200},
		{"first", now},
		{"second", now + 100},
	} {
		if _, err := s.RecordDecision(store.Decision{
			Topic:       r.topic,
			RecordedAt:  r.t,
			TruthChange: &store.TruthChange{Scope: "domain"},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	got, err := s.GetTruthChanges("", 0, 10)
	if err != nil {
		t.Fatalf("GetTruthChanges: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("count: want %d, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].Topic != w {
			t.Errorf("[%d]: want %q, got %q", i, w, got[i].Topic)
		}
	}
}

func TestGetTruthChanges_ScopeFilter(t *testing.T) {
	s := openTruthChangesStore(t)
	for _, sc := range []string{"domain", "toolkit", "domain"} {
		if _, err := s.RecordDecision(store.Decision{
			Topic:       sc + "-entry",
			TruthChange: &store.TruthChange{Scope: sc},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, err := s.GetTruthChanges("domain", 0, 10)
	if err != nil {
		t.Fatalf("GetTruthChanges: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("domain-only: want 2, got %d", len(got))
	}
	for _, d := range got {
		if d.TruthChange.Scope != "domain" {
			t.Errorf("non-domain leaked: %v", d.TruthChange)
		}
	}
}

func TestGetTruthChanges_SinceFilter(t *testing.T) {
	s := openTruthChangesStore(t)
	now := time.Now().Unix()
	for i, ts := range []int64{now - 1000, now - 500, now} {
		if _, err := s.RecordDecision(store.Decision{
			Topic:       string(rune('a' + i)),
			RecordedAt:  ts,
			TruthChange: &store.TruthChange{Scope: "domain"},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, err := s.GetTruthChanges("", now-600, 10)
	if err != nil {
		t.Fatalf("GetTruthChanges: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("since filter: want 2, got %d", len(got))
	}
}

func TestGetTruthChanges_IgnoresEntriesWithoutTruthChange(t *testing.T) {
	s := openTruthChangesStore(t)
	if _, err := s.RecordDecision(store.Decision{Topic: "plain"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := s.RecordDecision(store.Decision{
		Topic:       "truth-tracked",
		TruthChange: &store.TruthChange{Scope: "domain"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := s.GetTruthChanges("", 0, 10)
	if err != nil {
		t.Fatalf("GetTruthChanges: %v", err)
	}
	if len(got) != 1 || got[0].Topic != "truth-tracked" {
		t.Errorf("plain entry should be filtered out: %+v", got)
	}
}

func TestGetTruthChanges_Limit(t *testing.T) {
	s := openTruthChangesStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.RecordDecision(store.Decision{
			Topic:       string(rune('a' + i)),
			TruthChange: &store.TruthChange{Scope: "domain"},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, err := s.GetTruthChanges("", 0, 3)
	if err != nil {
		t.Fatalf("GetTruthChanges: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit: want 3, got %d", len(got))
	}
}
