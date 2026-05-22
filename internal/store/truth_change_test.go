package store_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

func openTruthChangeStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "leonard.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTruthChange_RoundTrip(t *testing.T) {
	s := openTruthChangeStore(t)

	tc := &store.TruthChange{
		Scope:       "domain",
		Files:       []string{"ground-truth/do-not-claim.md"},
		DiffRef:     "git:a1b2c3d",
		MotivatedBy: "User flagged ambiguity on 2026-05-22.",
	}
	id, err := s.RecordDecision(store.Decision{
		Topic:       "tighten-hipaa-rule",
		Choice:      "make absence explicit",
		Reasoning:   "Prior wording invited hedging.",
		TruthChange: tc,
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	got, err := s.GetDecisions("tighten-hipaa-rule", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].ID != id {
		t.Errorf("ID: want %d, got %d", id, got[0].ID)
	}
	if got[0].TruthChange == nil {
		t.Fatal("TruthChange round-tripped as nil")
	}
	if !reflect.DeepEqual(got[0].TruthChange, tc) {
		t.Errorf("TruthChange mismatch\nwant: %+v\ngot:  %+v", tc, got[0].TruthChange)
	}
}

func TestTruthChange_LegacyEntryHasNil(t *testing.T) {
	// Decisions recorded without the new block continue to load
	// with TruthChange == nil. Contract guarantee from #21.
	s := openTruthChangeStore(t)
	if _, err := s.RecordDecision(store.Decision{
		Topic:     "legacy",
		Choice:    "old style",
		Reasoning: "no truth_change",
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.GetDecisions("legacy", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if got[0].TruthChange != nil {
		t.Errorf("legacy entry should have nil TruthChange, got %+v", got[0].TruthChange)
	}
}

func TestTruthChange_ToolkitScope(t *testing.T) {
	// Toolkit-scope entries reference Leonard's own source; mostly
	// a smoke test that the Scope field round-trips arbitrary
	// values.
	s := openTruthChangeStore(t)
	if _, err := s.RecordDecision(store.Decision{
		Topic: "internal-adapters-refactor",
		TruthChange: &store.TruthChange{
			Scope:       "toolkit",
			Files:       []string{"internal/adapters/code/post_edit.go"},
			DiffRef:     "git:e8eced8",
			MotivatedBy: "Move existing logic behind Adapter interface.",
		},
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.GetDecisions("internal-adapters-refactor", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if got[0].TruthChange.Scope != "toolkit" {
		t.Errorf("Scope: want %q, got %q", "toolkit", got[0].TruthChange.Scope)
	}
}

func TestTruthChange_SupersedesPointer(t *testing.T) {
	s := openTruthChangeStore(t)

	firstID, err := s.RecordDecision(store.Decision{
		Topic: "rule-iteration",
		TruthChange: &store.TruthChange{
			Scope:       "domain",
			Files:       []string{"ground-truth/do-not-claim.md"},
			MotivatedBy: "First version of the rule.",
		},
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	if _, err := s.RecordDecision(store.Decision{
		Topic: "rule-iteration",
		TruthChange: &store.TruthChange{
			Scope:       "domain",
			Files:       []string{"ground-truth/do-not-claim.md"},
			MotivatedBy: "Tighten wording.",
			Supersedes:  &firstID,
		},
	}); err != nil {
		t.Fatalf("second: %v", err)
	}

	got, err := s.GetDecisions("rule-iteration", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	// Newest-first; second entry's Supersedes should point at first.
	if got[0].TruthChange.Supersedes == nil {
		t.Fatal("Supersedes nil on second entry")
	}
	if *got[0].TruthChange.Supersedes != firstID {
		t.Errorf("Supersedes: want %d, got %d", firstID, *got[0].TruthChange.Supersedes)
	}
}

func TestTruthChange_TrivialEntry(t *testing.T) {
	s := openTruthChangeStore(t)
	if _, err := s.RecordDecision(store.Decision{
		Topic: "trivial",
		TruthChange: &store.TruthChange{
			Scope:         "domain",
			Files:         []string{"ground-truth/facts.yaml"},
			Trivial:       true,
			TrivialReason: "fix typo in product name",
		},
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.GetDecisions("trivial", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if !got[0].TruthChange.Trivial {
		t.Error("Trivial flag lost in round-trip")
	}
	if got[0].TruthChange.TrivialReason != "fix typo in product name" {
		t.Errorf("TrivialReason: want %q, got %q", "fix typo in product name", got[0].TruthChange.TrivialReason)
	}
}

func TestTruthChange_RelatedRefsCoexist(t *testing.T) {
	// A truth-change entry can also carry RelatedFiles /
	// RelatedSymbols. The two systems are independent — confirm
	// both round-trip together.
	s := openTruthChangeStore(t)
	if _, err := s.RecordDecision(store.Decision{
		Topic:          "combined",
		RelatedFiles:   []string{"internal/adapters/code/post_edit.go"},
		RelatedSymbols: []string{"CodeAdapter.PostEdit"},
		TruthChange: &store.TruthChange{
			Scope:       "toolkit",
			MotivatedBy: "Adapter contract change.",
		},
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.GetDecisions("combined", 0, 10)
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	if len(got[0].RelatedFiles) != 1 {
		t.Errorf("RelatedFiles lost: %+v", got[0].RelatedFiles)
	}
	if got[0].TruthChange == nil {
		t.Error("TruthChange lost")
	}
}
