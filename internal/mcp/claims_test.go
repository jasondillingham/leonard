package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	id           int64
	sessionID    string
	claim        string
	evidence     string
	filePath     string
	verified     bool
	recordedAt   int64
	supersededBy *int64
}

func newMemClaimStore() *memClaimStore {
	return &memClaimStore{MemStore: leonardmcp.NewMemStore()}
}

func (m *memClaimStore) RecordClaim(_ context.Context, sessionID, claim, evidence, filePath string, verified bool) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.claims = append(m.claims, claimRow{
		id:         m.nextID,
		sessionID:  sessionID,
		claim:      claim,
		evidence:   evidence,
		filePath:   filePath,
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

func (m *memClaimStore) GetUnverifiedClaims(_ context.Context, sessionID string, includeSuperseded bool) ([]leonardmcp.ClaimRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := make([]claimRow, 0, len(m.claims))
	for _, c := range m.claims {
		if c.verified {
			continue
		}
		if !includeSuperseded && c.supersededBy != nil {
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

	// Omitting session_id (the schema declares it optional) records an
	// unscoped claim and round-trips through get_unverified_claims without
	// any session filter. session_id is opaque to Leonard.
	res := callTool(t, sess, "record_claim", map[string]any{
		"claim":    "no session",
		"evidence": "",
		"verified": false,
	})
	unscopedID := decodeResult[leonardmcp.RecordClaimOutput](t, res).ClaimID
	if unscopedID <= 0 {
		t.Fatalf("expected positive id for unscoped claim, got %d", unscopedID)
	}

	list = callTool(t, sess, "get_unverified_claims", map[string]any{})
	got = decodeResult[leonardmcp.GetUnverifiedClaimsOutput](t, list).Claims
	var unscoped *leonardmcp.ClaimEntry
	for i := range got {
		if got[i].ID == unscopedID {
			unscoped = &got[i]
			break
		}
	}
	if unscoped == nil {
		t.Fatalf("unscoped claim %d not surfaced by get_unverified_claims: %+v", unscopedID, got)
	}
	if unscoped.SessionID != "" || unscoped.Claim != "no session" {
		t.Errorf("unscoped claim round-trip mismatch: %+v", *unscoped)
	}
}

// TestDatabaseReplacedDetection exercises F2's lazy-detection path: once
// the on-disk DB file is removed under a running server, the very next
// tool call must surface a structured database-replaced error rather than
// silently writing to the unlinked inode. Restarting against the freshly
// re-init'd file must come up cleanly.
func TestDatabaseReplacedDetection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "leonard.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = st.Close()
		}
	})

	adapter := leonardmcp.NewStoreAdapter(st)
	if err := adapter.WatchDatabase(dbPath); err != nil {
		t.Fatalf("WatchDatabase: %v", err)
	}
	sess := newSession(t, adapter)

	// Baseline: a tool call works while the DB is intact.
	ok := callTool(t, sess, "record_claim", map[string]any{
		"claim": "before", "evidence": "x", "verified": false, "session_id": "s",
	})
	if id := decodeResult[leonardmcp.RecordClaimOutput](t, ok).ClaimID; id <= 0 {
		t.Fatalf("baseline record_claim returned non-positive id: %d", id)
	}

	// Simulate `rm -f .leonard/leonard.db*` from another shell. The store's
	// FD stays valid but the path no longer resolves to the same inode.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		_ = os.Remove(p)
	}

	// Lazy detection: the next tool call must surface database-replaced.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "record_claim",
		Arguments: map[string]any{
			"claim": "after", "evidence": "x", "verified": false, "session_id": "s",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError after DB removed, got %+v", res)
	}
	text := errorText(t, res)
	if !strings.Contains(text, `"code":"database-replaced"`) {
		t.Fatalf("error result missing database-replaced code; text=%q", text)
	}

	// A non-claim tool path must also be guarded — the inode check lives in
	// the adapter, so every method routes through it.
	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_files",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool transport: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError on list_files after DB removed, got %+v", res)
	}
	if !strings.Contains(errorText(t, res), `"code":"database-replaced"`) {
		t.Fatalf("list_files did not surface database-replaced")
	}

	// "Restart": close the stale store, re-init at the same path, spin up a
	// fresh adapter+session. Should come up cleanly with no errors.
	if err := st.Close(); err != nil {
		t.Fatalf("Close stale store: %v", err)
	}
	closed = true

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("re-init store: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	adapter2 := leonardmcp.NewStoreAdapter(st2)
	if err := adapter2.WatchDatabase(dbPath); err != nil {
		t.Fatalf("WatchDatabase after restart: %v", err)
	}
	sess2 := newSession(t, adapter2)

	rec := callTool(t, sess2, "record_claim", map[string]any{
		"claim": "post-restart", "evidence": "x", "verified": false, "session_id": "s",
	})
	if id := decodeResult[leonardmcp.RecordClaimOutput](t, rec).ClaimID; id <= 0 {
		t.Fatalf("post-restart record_claim returned non-positive id: %d", id)
	}
}

// errorText returns the textual payload of an IsError result so callers
// can match against substrings. Falls back through TextContent and the
// raw Content slice.
func errorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
