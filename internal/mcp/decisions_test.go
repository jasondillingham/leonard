package mcp_test

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// memDecisionStore embeds *MemStore so it satisfies SymbolStore unchanged,
// and adds a thread-safe slice of decisions so it also satisfies
// DecisionStore. We don't extend MemStore directly — mem_store.go is
// shared with the phase-1 tests and isn't owned by this lane.
type memDecisionStore struct {
	*leonardmcp.MemStore

	mu        sync.Mutex
	nextID    int64
	decisions []decisionRow
}

type decisionRow struct {
	id             int64
	topic          string
	choice         string
	reasoning      string
	recordedAt     int64
	supersededBy   *int64
	relatedFiles   []string
	relatedSymbols []string
}

func newMemDecisionStore() *memDecisionStore {
	return &memDecisionStore{MemStore: leonardmcp.NewMemStore()}
}

func (m *memDecisionStore) RecordDecision(_ context.Context, topic, choice, reasoning string, relatedFiles, relatedSymbols []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.decisions = append(m.decisions, decisionRow{
		id:             m.nextID,
		topic:          topic,
		choice:         choice,
		reasoning:      reasoning,
		recordedAt:     time.Now().Unix(),
		relatedFiles:   relatedFiles,
		relatedSymbols: relatedSymbols,
	})
	return m.nextID, nil
}

// recordAt is a test helper for seeding decisions with deterministic
// timestamps (since/limit tests rely on ordering).
func (m *memDecisionStore) recordAt(topic, choice, reasoning string, recordedAt int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.decisions = append(m.decisions, decisionRow{
		id:         m.nextID,
		topic:      topic,
		choice:     choice,
		reasoning:  reasoning,
		recordedAt: recordedAt,
	})
	return m.nextID
}

func (m *memDecisionStore) GetDecisions(_ context.Context, topic string, since int64, limit int) ([]leonardmcp.DecisionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Filter.
	filtered := make([]decisionRow, 0, len(m.decisions))
	for _, d := range m.decisions {
		if topic != "" && d.topic != topic {
			continue
		}
		if since > 0 && d.recordedAt < since {
			continue
		}
		filtered = append(filtered, d)
	}
	// Newest first, ties broken by id desc (matches store.GetDecisions).
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].recordedAt != filtered[j].recordedAt {
			return filtered[i].recordedAt > filtered[j].recordedAt
		}
		return filtered[i].id > filtered[j].id
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	out := make([]leonardmcp.DecisionRecord, len(filtered))
	for i, d := range filtered {
		out[i] = leonardmcp.DecisionRecord{
			ID:             d.id,
			Topic:          d.topic,
			Choice:         d.choice,
			Reasoning:      d.reasoning,
			RecordedAt:     d.recordedAt,
			RelatedFiles:   d.relatedFiles,
			RelatedSymbols: d.relatedSymbols,
		}
	}
	return out, nil
}

func (m *memDecisionStore) GetStaleDecisions(_ context.Context, limit int) ([]leonardmcp.StaleDecisionRecord, error) {
	// Test fixture: nothing is ever stale. Real staleness logic lives in
	// the store layer and is exercised by store-level tests.
	return nil, nil
}

func (m *memDecisionStore) SupersedeDecision(_ context.Context, id int64, newChoice, newReasoning string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		topic string
		found bool
	)
	for i, d := range m.decisions {
		if d.id != id {
			continue
		}
		found = true
		if d.supersededBy != nil {
			return 0, errBoomSuperseded(id, *d.supersededBy)
		}
		topic = d.topic
		m.nextID++
		newID := m.nextID
		m.decisions[i].supersededBy = &newID
		m.decisions = append(m.decisions, decisionRow{
			id:         newID,
			topic:      topic,
			choice:     newChoice,
			reasoning:  newReasoning,
			recordedAt: time.Now().Unix(),
		})
		return newID, nil
	}
	if !found {
		return 0, errBoomNotFound(id)
	}
	return 0, nil
}

type supersededErr struct {
	id, by int64
}

func (e supersededErr) Error() string { return "decision already superseded" }

type notFoundErr struct{ id int64 }

func (e notFoundErr) Error() string { return "decision not found" }

func errBoomSuperseded(id, by int64) error { return supersededErr{id: id, by: by} }
func errBoomNotFound(id int64) error       { return notFoundErr{id: id} }

// ---- helpers ----

// callTool is a tiny wrapper around session.CallTool that fatals on
// transport errors and unexpected tool errors.
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// ---- tools/list ----

func TestToolsListIncludesDecisions(t *testing.T) {
	sess := newSession(t, newMemDecisionStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	for _, want := range []string{"record_decision", "get_decisions", "supersede_decision"} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, keys(got))
		}
	}
	// Phase-1 surface must still be present (schemas unchanged is asserted
	// by the original mcp_test.go suite; here we just ensure coexistence).
	for _, want := range []string{"verify_symbol", "find_symbol", "list_files"} {
		if !got[want] {
			t.Errorf("phase-1 tool missing after decision wiring: %q", want)
		}
	}
}

// When the store doesn't satisfy DecisionStore (phase-1 fixture), the
// decision tools must NOT be registered. Guards the type-assertion branch
// in register().
func TestDecisionToolsAbsentWithoutDecisionStore(t *testing.T) {
	sess := newSession(t, leonardmcp.NewMemStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	for _, decisionTool := range []string{"record_decision", "get_decisions", "supersede_decision"} {
		if got[decisionTool] {
			t.Errorf("tool %q registered against a symbol-only store", decisionTool)
		}
	}
}

// ---- record_decision ----

func TestRecordDecision(t *testing.T) {
	store := newMemDecisionStore()
	sess := newSession(t, store)

	res := callTool(t, sess, "record_decision", map[string]any{
		"topic":     "auth library",
		"choice":    "use authentik",
		"reasoning": "self-hostable and we already run it",
	})
	out := decodeResult[leonardmcp.RecordDecisionOutput](t, res)
	if out.DecisionID <= 0 {
		t.Fatalf("expected positive decision_id, got %d", out.DecisionID)
	}

	// Round-trip via get_decisions.
	res = callTool(t, sess, "get_decisions", map[string]any{})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions
	if len(got) != 1 {
		t.Fatalf("expected 1 decision after one record_decision, got %d", len(got))
	}
	if got[0].Topic != "auth library" || got[0].Choice != "use authentik" {
		t.Errorf("round-trip mismatch: %+v", got[0])
	}
}

func TestRecordDecisionRequiresTopic(t *testing.T) {
	sess := newSession(t, newMemDecisionStore())
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "record_decision",
		Arguments: map[string]any{"topic": "", "choice": "x", "reasoning": "y"},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError for empty topic, got %+v", res)
	}
}

// ---- get_decisions ----

func TestGetDecisionsTopicFilter(t *testing.T) {
	store := newMemDecisionStore()
	now := time.Now().Unix()
	store.recordAt("caching", "redis", "speed", now-30)
	store.recordAt("auth", "authentik", "self-host", now-20)
	store.recordAt("caching", "valkey", "redis fork drama", now-10)

	sess := newSession(t, store)
	res := callTool(t, sess, "get_decisions", map[string]any{"topic": "caching"})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions

	if len(got) != 2 {
		t.Fatalf("expected 2 caching decisions, got %d: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Topic != "caching" {
			t.Errorf("topic filter leaked %q", d.Topic)
		}
	}
	// Newest first.
	if got[0].Choice != "valkey" {
		t.Errorf("expected newest-first ordering, got %+v", got)
	}
}

func TestGetDecisionsSinceFilter(t *testing.T) {
	store := newMemDecisionStore()
	now := time.Now().Unix()
	store.recordAt("a", "old1", "", now-100)
	store.recordAt("a", "old2", "", now-50)
	store.recordAt("a", "new", "", now-5)

	sess := newSession(t, store)
	res := callTool(t, sess, "get_decisions", map[string]any{"since": now - 10})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions
	if len(got) != 1 || got[0].Choice != "new" {
		t.Fatalf("since filter wrong: %+v", got)
	}
}

func TestGetDecisionsLimitDefault(t *testing.T) {
	store := newMemDecisionStore()
	now := time.Now().Unix()
	for i := 0; i < 25; i++ {
		store.recordAt("bulk", "c", "r", now-int64(i))
	}
	sess := newSession(t, store)

	res := callTool(t, sess, "get_decisions", map[string]any{})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions
	if len(got) != 20 {
		t.Errorf("expected default limit of 20, got %d", len(got))
	}
}

func TestGetDecisionsLimitCap(t *testing.T) {
	store := newMemDecisionStore()
	now := time.Now().Unix()
	for i := 0; i < 250; i++ {
		store.recordAt("bulk", "c", "r", now-int64(i))
	}
	sess := newSession(t, store)

	// Request more than the cap; expect cap to apply.
	res := callTool(t, sess, "get_decisions", map[string]any{"limit": 1000})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions
	if len(got) != 200 {
		t.Errorf("expected limit cap of 200, got %d", len(got))
	}
}

func TestGetDecisionsExplicitLimit(t *testing.T) {
	store := newMemDecisionStore()
	now := time.Now().Unix()
	for i := 0; i < 25; i++ {
		store.recordAt("bulk", "c", "r", now-int64(i))
	}
	sess := newSession(t, store)

	res := callTool(t, sess, "get_decisions", map[string]any{"limit": 5})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res).Decisions
	if len(got) != 5 {
		t.Errorf("expected limit=5 honored, got %d", len(got))
	}
}

func TestGetDecisionsEmpty(t *testing.T) {
	sess := newSession(t, newMemDecisionStore())
	res := callTool(t, sess, "get_decisions", map[string]any{})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, res)
	if len(got.Decisions) != 0 {
		t.Errorf("expected empty slice, got %+v", got.Decisions)
	}
}

// ---- supersede_decision ----

func TestSupersedeDecision(t *testing.T) {
	store := newMemDecisionStore()
	sess := newSession(t, store)

	rec := callTool(t, sess, "record_decision", map[string]any{
		"topic":     "cache",
		"choice":    "redis",
		"reasoning": "default",
	})
	origID := decodeResult[leonardmcp.RecordDecisionOutput](t, rec).DecisionID

	sup := callTool(t, sess, "supersede_decision", map[string]any{
		"decision_id":   origID,
		"new_choice":    "valkey",
		"new_reasoning": "redis license change",
	})
	newID := decodeResult[leonardmcp.SupersedeDecisionOutput](t, sup).NewDecisionID
	if newID <= 0 || newID == origID {
		t.Fatalf("unexpected new id: orig=%d new=%d", origID, newID)
	}

	// Both decisions should be present under the same topic; the new one
	// first (newest).
	list := callTool(t, sess, "get_decisions", map[string]any{"topic": "cache"})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, list).Decisions
	if len(got) != 2 {
		t.Fatalf("expected 2 cache decisions after supersede, got %d", len(got))
	}
	if got[0].ID != newID || got[0].Choice != "valkey" {
		t.Errorf("expected new decision first, got %+v", got[0])
	}
}

func TestSupersedeDecisionNotFound(t *testing.T) {
	sess := newSession(t, newMemDecisionStore())
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "supersede_decision",
		Arguments: map[string]any{"decision_id": 999, "new_choice": "x", "new_reasoning": "y"},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError for unknown decision, got %+v", res)
	}
}

func TestSupersedeDecisionRequiresID(t *testing.T) {
	sess := newSession(t, newMemDecisionStore())
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "supersede_decision",
		Arguments: map[string]any{"decision_id": 0, "new_choice": "x", "new_reasoning": "y"},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError for missing decision_id, got %+v", res)
	}
}

// ---- real store.Store round-trip ----

// Acceptance criterion: round-trips work against the real store.Store via
// StoreAdapter — not just the in-memory test fixture.
func TestDecisionToolsAgainstRealStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	adapter := leonardmcp.NewStoreAdapter(st)
	sess := newSession(t, adapter)

	// record_decision
	rec := callTool(t, sess, "record_decision", map[string]any{
		"topic":     "indexing strategy",
		"choice":    "hash-gated",
		"reasoning": "avoid reparsing unchanged files",
	})
	id := decodeResult[leonardmcp.RecordDecisionOutput](t, rec).DecisionID
	if id <= 0 {
		t.Fatalf("expected positive decision id, got %d", id)
	}

	// get_decisions
	list := callTool(t, sess, "get_decisions", map[string]any{"topic": "indexing strategy"})
	got := decodeResult[leonardmcp.GetDecisionsOutput](t, list).Decisions
	if len(got) != 1 || got[0].ID != id || got[0].Choice != "hash-gated" {
		t.Fatalf("real-store round-trip mismatch: %+v", got)
	}
	if got[0].RecordedAt <= 0 {
		t.Errorf("expected recorded_at to be populated, got %d", got[0].RecordedAt)
	}

	// supersede_decision
	sup := callTool(t, sess, "supersede_decision", map[string]any{
		"decision_id":   id,
		"new_choice":    "hash + tree-sitter",
		"new_reasoning": "smaller diffs survive better",
	})
	newID := decodeResult[leonardmcp.SupersedeDecisionOutput](t, sup).NewDecisionID
	if newID == id || newID <= 0 {
		t.Fatalf("supersede returned bad id: orig=%d new=%d", id, newID)
	}

	// Both decisions should appear newest-first.
	list = callTool(t, sess, "get_decisions", map[string]any{"topic": "indexing strategy"})
	got = decodeResult[leonardmcp.GetDecisionsOutput](t, list).Decisions
	if len(got) != 2 {
		t.Fatalf("expected 2 decisions after supersede, got %d", len(got))
	}
	if got[0].ID != newID {
		t.Errorf("expected new decision first, got %+v", got)
	}

	// Superseding the already-superseded original must surface an error.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "supersede_decision",
		Arguments: map[string]any{"decision_id": id, "new_choice": "x", "new_reasoning": "y"},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError when re-superseding, got %+v", res)
	}
}
