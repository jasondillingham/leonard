package store

import "testing"

func i64(v int64) *int64 { return &v }

// recordClaimAt inserts a claim and returns its ID.
func recordClaimAt(t *testing.T, s *Store, c Claim) int64 {
	t.Helper()
	id, err := s.RecordClaim(c)
	if err != nil {
		t.Fatalf("RecordClaim: %v", err)
	}
	return id
}

func staleIDs(t *testing.T, s *Store, now int64) map[int64]bool {
	t.Helper()
	claims, err := s.GetStaleClaims("", now)
	if err != nil {
		t.Fatalf("GetStaleClaims: %v", err)
	}
	out := make(map[int64]bool, len(claims))
	for _, c := range claims {
		out[c.ID] = true
	}
	return out
}

// TestClaimExpiry_NilTTLNeverGoesStale pins the backward-compatibility
// guarantee of migration v9: every claim recorded before the migration
// has check_ttl_seconds NULL, and NULL means "never expires". If this
// breaks, upgrading silently marks the entire existing ledger stale.
func TestClaimExpiry_NilTTLNeverGoesStale(t *testing.T) {
	s, _ := newTestStore(t)
	id := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "no ttl", Evidence: "e",
		Verified: true, RecordedAt: 1000,
	})

	const farFuture = 1 << 40
	if staleIDs(t, s, farFuture)[id] {
		t.Error("claim with NULL check_ttl_seconds must never go stale")
	}

	c, found, err := s.GetClaim(id)
	if err != nil || !found {
		t.Fatalf("GetClaim: err=%v found=%v", err, found)
	}
	if c.IsStale(farFuture) {
		t.Error("Claim.IsStale must agree with the SQL: NULL TTL never expires")
	}
}

// TestClaimExpiry_TTLBoundary pins the comparison as strictly-less-than.
// Exactly at the expiry instant the check is still fresh; one second past
// it is stale. An off-by-one here silently shifts every TTL by a second.
func TestClaimExpiry_TTLBoundary(t *testing.T) {
	s, _ := newTestStore(t)
	id := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "ttl 100", Evidence: "e",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(100),
	})

	cases := []struct {
		name      string
		now       int64
		wantStale bool
	}{
		{"before expiry", 1099, false},
		{"exactly at expiry", 1100, false},
		{"one past expiry", 1101, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := staleIDs(t, s, tc.now)[id]; got != tc.wantStale {
				t.Errorf("SQL: stale=%v, want %v (now=%d)", got, tc.wantStale, tc.now)
			}
			c, _, err := s.GetClaim(id)
			if err != nil {
				t.Fatalf("GetClaim: %v", err)
			}
			if got := c.IsStale(tc.now); got != tc.wantStale {
				t.Errorf("Claim.IsStale: %v, want %v (now=%d)", got, tc.wantStale, tc.now)
			}
		})
	}
}

// TestClaimExpiry_TouchRefreshes covers the re-check path that did not
// exist before v9: confirming a claim still holds must reset its window.
func TestClaimExpiry_TouchRefreshes(t *testing.T) {
	s, _ := newTestStore(t)
	id := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "refreshable", Evidence: "e",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(100),
	})

	const now = 1200
	if !staleIDs(t, s, now)[id] {
		t.Fatal("precondition: claim should be stale before touching")
	}
	if err := s.TouchClaim(id, now); err != nil {
		t.Fatalf("TouchClaim: %v", err)
	}
	if staleIDs(t, s, now)[id] {
		t.Error("touched claim must leave the stale set")
	}
	// The window restarts from the touch, not from recorded_at.
	if !staleIDs(t, s, now+101)[id] {
		t.Error("touched claim must go stale again once the new window elapses")
	}
}

// TestClaimExpiry_TouchMissingClaimErrors — a caller working from a
// stale ID should hear about it rather than get a silent no-op.
func TestClaimExpiry_TouchMissingClaimErrors(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.TouchClaim(424242, 1000); err == nil {
		t.Error("expected an error touching a nonexistent claim")
	}
}

// TestClaimExpiry_UnverifiedIsNeverStale is the disjointness guarantee
// and the most likely place for an implementation error. An unverified
// claim with a long-expired TTL is still *unverified*, not stale — the
// two sets must never overlap, or the Stop hook double-reports and a
// claim that never passed gets described as one whose check lapsed.
func TestClaimExpiry_UnverifiedIsNeverStale(t *testing.T) {
	s, _ := newTestStore(t)
	id := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "never passed", Evidence: "e",
		Verified: false, RecordedAt: 1000, CheckTTLSeconds: i64(10),
	})

	const now = 5000
	if staleIDs(t, s, now)[id] {
		t.Error("unverified claim must not appear in GetStaleClaims")
	}

	unverified, err := s.GetUnverifiedClaims("sess")
	if err != nil {
		t.Fatalf("GetUnverifiedClaims: %v", err)
	}
	var found bool
	for _, c := range unverified {
		if c.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("unverified claim must remain in GetUnverifiedClaims regardless of TTL")
	}
}

// TestClaimExpiry_SupersededExcluded pins GetStaleClaims' supersession
// filter, matching GetUnverifiedClaims so the two read paths agree.
//
// The supersession column is set directly rather than through
// SupersedeClaimsForFile, because that method only supersedes rows with
// verified = 0 — it exists to retire unverified failure records — so no
// current code path can supersede a verified claim. The filter is still
// worth pinning: it is in the query, and a future re-check path that
// supersedes a verified claim would otherwise silently double-report it.
func TestClaimExpiry_SupersededExcluded(t *testing.T) {
	s, _ := newTestStore(t)
	old := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "old", Evidence: "e", FilePath: "a.go",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(10),
	})
	newer := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "new", Evidence: "e", FilePath: "a.go",
		Verified: true, RecordedAt: 1100, CheckTTLSeconds: i64(10),
	})
	if _, err := s.db.Exec(
		`UPDATE claims SET superseded_by_claim_id = ? WHERE id = ?`, newer, old,
	); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}

	stale := staleIDs(t, s, 9000)
	if stale[old] {
		t.Error("superseded claim must be excluded from GetStaleClaims")
	}
	if !stale[newer] {
		t.Error("superseding claim should still be reported stale")
	}
}

// TestClaimExpiry_AgesFromRecordedAtWhenNeverChecked pins the COALESCE
// fallback: a claim never explicitly re-checked ages from when it was
// recorded, not from zero (which would make everything instantly stale)
// and not never (which would make TTL meaningless until first touch).
func TestClaimExpiry_AgesFromRecordedAtWhenNeverChecked(t *testing.T) {
	s, _ := newTestStore(t)
	id := recordClaimAt(t, s, Claim{
		SessionID: "sess", Claim: "never touched", Evidence: "e",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(500),
	})

	if staleIDs(t, s, 1400)[id] {
		t.Error("should not be stale 400s into a 500s window")
	}
	if !staleIDs(t, s, 1600)[id] {
		t.Error("should be stale 600s into a 500s window")
	}

	c, _, err := s.GetClaim(id)
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if c.LastChecked != nil {
		t.Errorf("LastChecked should round-trip as nil, got %v", *c.LastChecked)
	}
	if c.CheckTTLSeconds == nil || *c.CheckTTLSeconds != 500 {
		t.Errorf("CheckTTLSeconds should round-trip as 500, got %v", c.CheckTTLSeconds)
	}
}

// TestClaimExpiry_SessionFilter — an empty sessionID spans sessions,
// matching GetUnverifiedClaims.
func TestClaimExpiry_SessionFilter(t *testing.T) {
	s, _ := newTestStore(t)
	a := recordClaimAt(t, s, Claim{
		SessionID: "A", Claim: "a", Evidence: "e",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(10),
	})
	b := recordClaimAt(t, s, Claim{
		SessionID: "B", Claim: "b", Evidence: "e",
		Verified: true, RecordedAt: 1000, CheckTTLSeconds: i64(10),
	})

	all := staleIDs(t, s, 9000)
	if !all[a] || !all[b] {
		t.Error("empty sessionID should return stale claims across sessions")
	}

	onlyA, err := s.GetStaleClaims("A", 9000)
	if err != nil {
		t.Fatalf("GetStaleClaims(A): %v", err)
	}
	if len(onlyA) != 1 || onlyA[0].ID != a {
		t.Errorf("sessionID filter should return only session A's claim, got %d rows", len(onlyA))
	}
}
