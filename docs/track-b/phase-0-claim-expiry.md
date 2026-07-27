# Phase 0 — claim expiry (`verified` never decays)

**Status:** ready to build. Ungated — this is a defect in shipped code, not a design question.
**Schema:** v8 → v9.

## The defect

`claims` today (after migrations v1–v3):

```
id, session_id, claim, evidence, verified, recorded_at,
file_path, superseded_by_claim_id, tool, index_ok, vet_ok, vet_error_summary
```

Lifecycle operations: `RecordClaim`, `SupersedeClaimsForFile`, `SupersedeOutstandingFailures`,
`ResolveClaim`, `PurgeSupersededClaims`, `GetClaim`, `GetUnverifiedClaims`.

**There is no re-check path anywhere.** No `last_checked`, no TTL, no expiry. `verified` is a
one-time boolean written at record time. Supersession fires only from a *later edit to the same
file* (`SupersedeClaimsForFile`) or a later clean run (`SupersedeOutstandingFailures`) — never
from the underlying truth changing.

Consequence: a claim verified on 2026-05-20 still reads `verified = 1` today, even if what it
asserted stopped being true in June. Nothing in the system can distinguish *"checked, and it held"*
from *"checked two months ago, unknown since."*

That is the drift problem living inside the ledger built to prevent it. The schema answers
*"was this checked?"*; the question that matters is *"is this still true?"*

## Design

Move from two states to three:

| State | Meaning | Condition |
|---|---|---|
| `unverified` | never passed a check | `verified = 0` |
| `verified` | passed, and the check is still fresh | `verified = 1` AND not past TTL |
| **`stale`** | passed once, but the check has expired | `verified = 1` AND past TTL |

`stale` is deliberately **not** the same as `unverified`. Conflating them would flood the Stop
hook with everything ever verified, and would misreport a claim that *did* hold as one that never
did. Consumers must be able to say "5 claims haven't been re-checked in 30 days" separately from
"3 claims never passed."

## Schema — `migrateV9`

Append to the `migrations` slice in `internal/store/store.go` (index N applies to version N+1, so
`migrateV9` becomes `migrations[8]`) and bump `const schemaVersion = 9`.

```go
// migrateV9 adds re-check metadata so a verification can expire.
// Pre-v9 the `verified` flag was written once at record time and
// never revisited: a claim verified in May still reported as
// verified in July regardless of whether the underlying truth had
// moved. Supersession only ever fired on a later edit to the same
// file, never on the truth changing.
//
// Both columns are nullable and existing rows get NULL, which means
// "never expires" — so the migration is behavior-preserving for
// every claim already in the ledger.
func migrateV9(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE claims ADD COLUMN last_checked INTEGER`,
		`ALTER TABLE claims ADD COLUMN check_ttl_seconds INTEGER`,
		`CREATE INDEX idx_claims_last_checked ON claims(last_checked)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}
```

**NULL semantics are load-bearing.** `check_ttl_seconds IS NULL` means "never expires." Every
existing row gets NULL, so no previously-verified claim silently becomes stale on upgrade. Opting
a claim into expiry is explicit.

## Store API

### `Claim` struct

```go
LastChecked     *int64  // nil = never re-checked since RecordClaim
CheckTTLSeconds *int64  // nil = never expires
```

Pointers, not values — `0` is a meaningful TTL (immediately stale) and must be distinguishable
from "unset."

### Staleness predicate

One definition, used everywhere, so SQL and Go cannot disagree:

```
stale := verified = 1
     AND check_ttl_seconds IS NOT NULL
     AND COALESCE(last_checked, recorded_at) + check_ttl_seconds < :now
```

`COALESCE(last_checked, recorded_at)` means a claim never explicitly re-checked ages from when it
was recorded, which is the honest reading.

**Pass `now` in as a parameter; do not call `time.Now()` inside the query builder.** Tests must be
able to advance the clock without sleeping.

### New / changed methods

- `GetStaleClaims(sessionID string, now int64) ([]Claim, error)` — verified-but-expired, excluding
  superseded. Mirror `queryUnverifiedClaims`'s shape: same `selectCols`, same
  `superseded_by_claim_id IS NULL` filter, same `ORDER BY recorded_at DESC, id DESC`, same
  1000-row `queryRowCap`.
- `TouchClaim(claimID int64, now int64) error` — set `last_checked = now`. The re-check path that
  does not exist today.
- `queryUnverifiedClaims` — **unchanged behavior.** It filters `verified = 0`; stale claims have
  `verified = 1` and must not leak into it. Only `selectCols` and the row scan grow by two columns.

`selectCols` is a `const` string listing every column, and the scan is positional. Both must be
updated together in `queryUnverifiedClaims` and `GetClaim`. Missing one is a silent column-shift
bug — check for other `SELECT` sites over `claims` before finishing.

## Consumers

Keep this phase to the store plus a read path. Do not wire automatic re-checking; that is Phase 2+
and crosses a trust boundary (ROADMAP §2.7).

- **MCP** — add `get_stale_claims` alongside `get_unverified_claims`. Same limit-clamping
  (`safeLimit` + explicit cap) as its sibling; do not repeat the `get_truth_history` gap.
- **Stop hook** — surface a count line only when non-zero: `Leonard: 5 verified claims haven't
  been re-checked in 30+ days.` Follow the existing quiet-by-default posture; silence on zero.
- **CLI** — `leonard claims stale [--limit N]`, matching `claims` subcommand conventions.

## Backward compatibility

- Existing rows: `last_checked = NULL`, `check_ttl_seconds = NULL` → never stale. No behavior
  change for any claim recorded before v9.
- `RecordClaim` keeps its current signature. TTL is opt-in via a separate setter or an explicit
  field on the struct; do not change the default.
- A v9 store opened by a v8 binary trips the existing `current > schemaVersion` guard
  (`store.go:271`). That is correct and already handled.

## Tests

In `internal/store` — the package where the code lives, so this fix does not repeat the
`AllFiles`/`CountFiles` pattern of shipping untested.

1. **NULL TTL never goes stale** — claim verified with `check_ttl_seconds = NULL`, query far in
   the future, must not appear in `GetStaleClaims`.
2. **TTL boundary** — exactly at `last_checked + ttl` is *not* stale; one second past it is. Pins
   the `<` comparison.
3. **`TouchClaim` refreshes** — a stale claim, touched, leaves the stale set.
4. **Unverified never appears as stale** — `verified = 0` with an expired TTL stays in
   `GetUnverifiedClaims` and out of `GetStaleClaims`. The two sets must be disjoint.
5. **Superseded excluded** — matches `queryUnverifiedClaims` behavior.
6. **`COALESCE` fallback** — a claim with `last_checked = NULL` and a TTL ages from `recorded_at`.
7. **Migration is behavior-preserving** — open a v8 store with claims, migrate, assert every
   pre-existing claim reports exactly as before.

Test 4 is the one that would catch the most likely implementation error.

## Out of scope

- Automatic re-checking, resolvers, bindings — Phases 2 and 3.
- Deciding default TTLs per claim type. Ship the mechanism; set policy once Phase 1 shows which
  claims actually go stale in practice.
- Any change to how claims are *created*.
