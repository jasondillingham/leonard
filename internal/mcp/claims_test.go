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

// memClaimStore embeds *MemStore so it satisfies SymbolStore unchanged,
// and adds a thread-safe slice of claims so it also satisfies ClaimStore.
// Mirrors the memDecisionStore pattern in decisions_test.go.
type memClaimStore struct {
	*leonardmcp.MemStore

	mu     sync.Mutex
	nextID int64
	claims []claimRow
}

type claimRow struct {
	id         int64
	sessionID  string
	claim      string
	evidence   string
	verified   bool
	recordedAt int64
}

func newMemClaimStore() *memClaimStore {
	return &memClaimStore{MemStore: leonardmcp.NewMemStore()}
}

func (m *memClaimStore) RecordClaim(_ context.Context, sessionID, claim, evidence string, verified bool) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.claims = append(m.claims, claimRow{
		id:         m.nextID,
		sessionID:  sessionID,
		claim:      claim,
		evidence:   evidence,
		verified:   verified,
		recordedAt: time.Now().Unix(),
	})
	return m.nextID, nil
}

// recordAt seeds a claim with a deterministic timestamp so ordering tests
// don't depend on wall-clock granularity.
func (m *memClaimStore) recordAt(sessionID, claim, evidence string, verified bool, recordedAt int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.claims = append(m.claims, claimRow{
		id:         m.nextID,
		sessionID:  sessionID,
		claim:      claim,
		evidence:   evidence,
		verified:   verified,
		recordedAt: recordedAt,
	})
	return m.nextID
}

func (m *memClaimStore) GetUnverifiedClaims(_ context.Context, sessionID string) ([]leonardmcp.ClaimRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := make([]claimRow, 0, len(m.claims))
	for _, c := range m.claims {
		if c.verified {
			continue
		}
		if sessionID != "" && c.sessionID != sessionID {
			continue
		}
		filtered = append(filtered, c)
	}
	// Newest first, ties broken by id desc (matches store.GetUnverifiedClaims).
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].recordedAt != filtered[j].recordedAt {
			return filtered[i].recordedAt > filtered[j].recordedAt
		}
		return filtered[i].id > filtered[j].id
	})
	out := make([]leonardmcp.ClaimRecord, len(filtered))
	for i, c := range filtered {
		out[i] = leonardmcp.ClaimRecord{
			ID:         c.id,
			SessionID:  c.sessionID,
			Claim:      c.claim,
			Evidence:   c.evidence,
			RecordedAt: c.recordedAt,
		}
	}
	return out, nil
}

// ---- tools/list ----

func TestToolsListIncludesClaims(t *testing.T) {
	sess := newSession(t, newMemClaimStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	for _, want := range []string{"record_claim", "get_unverified_claims"} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, keys(got))
		}
	}
	// Phase-1 surface must still be present.
	for _, want := range []string{"verify_symbol", "find_symbol", "list_files"} {
		if !got[want] {
			t.Errorf("phase-1 tool missing after claim wiring: %q", want)
		}
	}
}

// When the store doesn't satisfy ClaimStore (phase-1 fixture), the claim
// tools must NOT be registered. Guards the type-assertion branch in
// register().
func TestClaimToolsAbsentWithoutClaimStore(t *testing.T) {
	sess := newSession(t, leonardmcp.NewMemStore())
	got := map[string]bool{}
	for tool, err := range sess.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iterator: %v", err)
		}
		got[tool.Name] = true
	}
	for _, claimTool := range []string{"record_claim", "get_unverified_claims"} {
		if got[claimTool] {
			t.Errorf("tool %q registered against a symbol-only store", claimTool)
		}
	}
}

// ---- record_claim ----

func TestRecordClaim(t *testing.T) {
	st := newMemClaimStore()
	sess := newSession(t, st)

	res := callTool(t, sess, "record_claim", map[string]any{
		"claim":      "go vet clean",
		"evidence":   "exit 0",
		"verified":   false,
		"session_id": "sess-1",
	})
	out := decodeResult[leonardmcp.RecordClaimOutput](t, res)
	if out.ClaimID <= 0 {
		t.Fatalf("expected positive claim_id, got %d", out.ClaimID)
	}

	// Round-trip via get_unverified_claims.
	res = callTool(t, sess, "get_unverified_claims", map[string]any{})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, res).Claims
	if len(got) != 1 {
		t.Fatalf("expected 1 unverified claim after one record_claim, got %d", len(got))
	}
	if got[0].Claim != "go vet clean" || got[0].SessionID != "sess-1" || got[0].ID != out.ClaimID {
		t.Errorf("round-trip mismatch: %+v", got[0])
	}
}

func TestRecordClaimRequiresClaim(t *testing.T) {
	sess := newSession(t, newMemClaimStore())
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "record_claim",
		Arguments: map[string]any{"claim": "", "evidence": "x", "verified": false, "session_id": "s"},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError for empty claim, got %+v", res)
	}
}

func TestRecordClaimVerifiedHidden(t *testing.T) {
	st := newMemClaimStore()
	sess := newSession(t, st)

	callTool(t, sess, "record_claim", map[string]any{
		"claim":      "tests pass",
		"evidence":   "PASS",
		"verified":   true,
		"session_id": "sess-1",
	})
	callTool(t, sess, "record_claim", map[string]any{
		"claim":      "lint clean",
		"evidence":   "exit 0",
		"verified":   false,
		"session_id": "sess-1",
	})

	res := callTool(t, sess, "get_unverified_claims", map[string]any{})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, res).Claims
	if len(got) != 1 {
		t.Fatalf("expected 1 unverified claim (verified=true should be hidden), got %d: %+v", len(got), got)
	}
	if got[0].Claim != "lint clean" {
		t.Errorf("wrong claim surfaced: %+v", got[0])
	}
}

// ---- get_unverified_claims ----

func TestGetUnverifiedClaimsAcrossSessions(t *testing.T) {
	st := newMemClaimStore()
	now := time.Now().Unix()
	st.recordAt("sess-1", "vet", "ok", false, now-30)
	st.recordAt("sess-2", "build", "ok", false, now-20)
	st.recordAt("sess-1", "test", "ok", false, now-10)

	sess := newSession(t, st)
	res := callTool(t, sess, "get_unverified_claims", map[string]any{})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, res).Claims
	if len(got) != 3 {
		t.Fatalf("expected 3 claims across sessions, got %d: %+v", len(got), got)
	}
	if got[0].Claim != "test" {
		t.Errorf("expected newest-first ordering, got %+v", got)
	}
}

func TestGetUnverifiedClaimsSessionFilter(t *testing.T) {
	st := newMemClaimStore()
	now := time.Now().Unix()
	st.recordAt("sess-1", "vet", "ok", false, now-30)
	st.recordAt("sess-2", "build", "ok", false, now-20)
	st.recordAt("sess-1", "test", "ok", false, now-10)

	sess := newSession(t, st)
	res := callTool(t, sess, "get_unverified_claims", map[string]any{"session_id": "sess-1"})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, res).Claims
	if len(got) != 2 {
		t.Fatalf("expected 2 sess-1 claims, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.SessionID != "sess-1" {
			t.Errorf("session filter leaked claim from %q", c.SessionID)
		}
	}
}

func TestGetUnverifiedClaimsEmpty(t *testing.T) {
	sess := newSession(t, newMemClaimStore())
	res := callTool(t, sess, "get_unverified_claims", map[string]any{})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, res)
	if len(got.Claims) != 0 {
		t.Errorf("expected empty slice, got %+v", got.Claims)
	}
}

// ---- real store.Store round-trip ----

// Acceptance criterion: both tools must round-trip data via the real
// store.Store through StoreAdapter, not just the in-memory fixture.
func TestClaimToolsAgainstRealStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "leonard.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	adapter := leonardmcp.NewStoreAdapter(st)
	sess := newSession(t, adapter)

	// verified=false → should appear in get_unverified_claims.
	rec := callTool(t, sess, "record_claim", map[string]any{
		"claim":      "go vet clean",
		"evidence":   "exit 0",
		"verified":   false,
		"session_id": "sess-real-1",
	})
	id := decodeResult[leonardmcp.RecordClaimOutput](t, rec).ClaimID
	if id <= 0 {
		t.Fatalf("expected positive claim id, got %d", id)
	}

	// verified=true → should NOT appear in get_unverified_claims.
	callTool(t, sess, "record_claim", map[string]any{
		"claim":      "already-confirmed assertion",
		"evidence":   "manually checked",
		"verified":   true,
		"session_id": "sess-real-1",
	})

	// Second session, unverified.
	callTool(t, sess, "record_claim", map[string]any{
		"claim":      "other-session vet",
		"evidence":   "exit 0",
		"verified":   false,
		"session_id": "sess-real-2",
	})

	// No filter → unverified claims across all sessions.
	list := callTool(t, sess, "get_unverified_claims", map[string]any{})
	got := decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, list).Claims
	if len(got) != 2 {
		t.Fatalf("expected 2 unverified claims across sessions, got %d: %+v", len(got), got)
	}

	// Session filter → only sess-real-1's unverified claim.
	list = callTool(t, sess, "get_unverified_claims", map[string]any{"session_id": "sess-real-1"})
	got = decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, list).Claims
	if len(got) != 1 {
		t.Fatalf("expected 1 sess-real-1 unverified claim, got %d: %+v", len(got), got)
	}
	if got[0].ID != id || got[0].Claim != "go vet clean" || got[0].SessionID != "sess-real-1" {
		t.Errorf("real-store round-trip mismatch: %+v", got[0])
	}
	if got[0].RecordedAt <= 0 {
		t.Errorf("expected recorded_at to be populated, got %d", got[0].RecordedAt)
	}

	// Empty session id is rejected by the real store; the failure should
	// surface as an MCP error result, not a transport error.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "record_claim",
		Arguments: map[string]any{
			"claim":    "no session",
			"evidence": "",
			"verified": false,
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError when real store rejects empty session_id, got %+v", res)
	}
}
