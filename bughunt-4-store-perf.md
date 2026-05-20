# Bug Hunt #4 — store-perf

## Summary

Audited every hot-path query in `internal/store/store.go` (+ `recent.go`) by running `EXPLAIN QUERY PLAN` against a production-shape DB seeded with 1k files / 10k symbols / 100 decisions / 100 claims, then re-benched at 5k / 10k / 50k / 100k scales. Six queries currently full-SCAN their tables and would benefit from indexes — three meaningfully, three at the margin. One existing index (`idx_claims_vet_ok`) is **unused** by any query and is pure write overhead. One subtle N+1 read pattern in `GetStaleDecisions` issues one round trip per related ref. The prune sweep curve after v0.7.1 is now cleanly linear (1k=41ms → 10k=501ms → 100k=5.8s), so the v0.7.1 `idx_symbols_parent` fix landed on the right bottleneck. WAL housekeeping is the new concern: no code path calls `wal_checkpoint`, and a 100k-row wipe produced a 183 MB `.leonard.db-wal` file that auto-checkpoint won't reclaim while any reader holds a snapshot.

Scratch programs are at `/tmp/bughunt4/{explain2,bench,readbench,concurrent,replacesyms,cascadenoparent,migrate,claimsidx,recentidx,decisidx}/`.

## Findings

### F1 — `ListFilesIndexedSince` (powers `recent_changes`) full-scans files
- **Severity:** medium
- **Reproducer:** seed 50k files with a range of `indexed_at`, then run `EXPLAIN QUERY PLAN SELECT path, language, size_bytes, indexed_at FROM files WHERE indexed_at >= ? ORDER BY indexed_at DESC, path LIMIT 50`.
- **Observed:** plan is `SCAN files / USE TEMP B-TREE FOR ORDER BY`. At 50k files the query takes ~6.0 ms with `since=0` and ~2.7 ms with a recent `since` cutoff — SQLite scans every row whether or not `since` filters most of them out.
- **Expected:** `SEARCH files USING INDEX idx_files_indexed_at (indexed_at>?)` with the b-tree producing rows in the right order so the LIMIT can short-circuit.
- **Measured impact:** adding `CREATE INDEX idx_files_indexed_at ON files(indexed_at)` drops the same 50k-row query from ~6.0 ms to ~76 µs (≈80×). The recent-cutoff variant goes 2.7 ms → 62 µs (≈40×).
- **Why it matters:** `recent_changes` is called by the MCP server on every session-start ping per phase-3, and Jason's CLAUDE.md describes Leonard being invoked from real Claude Code sessions. Cost grows linearly with file count — at 100k files it's headed toward 12 ms per call.
- **Suggested fix shape:** add `idx_files_indexed_at` in a v6 migration. CREATE INDEX on a 100k-row table took 50 ms in benchmark, so the upgrade cost is invisible to users.
- **Out of scope:** whether the MCP layer should cache recent-changes results between session-start pings.

### F2 — `GetUnverifiedClaims` full-scans claims when no session is supplied
- **Severity:** medium
- **Reproducer:** seed 10k claims (10% unverified-and-open), then `EXPLAIN QUERY PLAN SELECT … FROM claims WHERE verified = 0 AND superseded_by_claim_id IS NULL ORDER BY recorded_at DESC, id DESC`.
- **Observed:** `SCAN claims / USE TEMP B-TREE FOR ORDER BY` — full table scan, then sort. The session-scoped variant correctly uses `idx_claims_session`, but the no-session variant has no useful index.
- **Expected:** an index that lets SQLite walk only the unverified-open rows in `recorded_at DESC` order.
- **Measured impact:** at 10k rows the no-session query takes ~770 µs. Adding `CREATE INDEX idx_claims_unverified_open ON claims(recorded_at DESC) WHERE verified = 0 AND superseded_by_claim_id IS NULL` (a *partial* index) drops it to ~450 µs (~1.7×). The win grows with table size because the partial index keeps O(unverified-open) rows, not O(total claims).
- **Why it matters:** the `stop` hook calls `GetUnverifiedClaims(sessionID="")` to summarize cross-session failures. Claims is the table most likely to grow over a project's lifetime — verified rows accumulate, the unverified-open subset stays small.
- **Suggested fix shape:** add a partial index keyed on `recorded_at` with the same WHERE clause. Index stays tiny (just the open-failure rows) and is write-amplification-friendly.
- **Out of scope:** whether to add a periodic `DELETE FROM claims WHERE verified = 1 AND recorded_at < ?` retention policy.

### F3 — `GetDecisions` ORDER BY full-scans decisions when topic filter is absent
- **Severity:** low (decisions table is expected to stay small in practice)
- **Reproducer:** seed 5k decisions, then `EXPLAIN QUERY PLAN SELECT … FROM decisions ORDER BY recorded_at DESC, id DESC LIMIT 50`.
- **Observed:** `SCAN decisions / USE TEMP B-TREE FOR ORDER BY`. Same plan when only `since` filters; topic filter does use `idx_decisions_topic`.
- **Measured impact:** at 5k decisions the no-filter query is ~735 µs. With `CREATE INDEX idx_decisions_recorded_at ON decisions(recorded_at)` it drops to ~41 µs (~18×). The topic+since variant gets to ~70 µs.
- **Why it matters:** `GetDecisions` is the MCP `get_decisions` tool's backing query. Leonard's design encourages keeping decision counts "modest" (per the GetStaleDecisions docstring's 200 cap) so absolute cost is unlikely to be felt, but the plan is suboptimal as-is.
- **Suggested fix shape:** add `idx_decisions_recorded_at` in the same v6 migration as F1.
- **Out of scope:** whether GetStaleDecisions should join against files/symbols once instead of N+1 probing (see F4).

### F4 — `GetStaleDecisions` issues one round trip per related-files/symbols ref (N+1)
- **Severity:** medium
- **Reproducer:** read `missingFiles` / `missingSymbols` at `internal/store/store.go:726-762`. Each function loops over its slice and runs a separate `s.db.QueryRow(...)` per element.
- **Observed:** for a decision with k related files and m related symbols, `GetStaleDecisions` performs `k+m` separate SQL round trips per decision, and processes up to 200 decisions. A decision touching 5 files and 5 symbols × 200 decisions = 2000 round trips per call. Plans for the individual probes are good (`SEARCH … USING COVERING INDEX sqlite_autoindex_files_1` and the multi-index OR on symbols name/qname) so per-probe cost is microseconds, but the per-call constant is awkward.
- **Expected:** a single SQL query per decision that returns which refs are missing, or even a single query at the top of the loop that pre-loads all referenced paths and names into a set.
- **Suggested fix shape:** dedupe the `RelatedFiles` / `RelatedSymbols` slices across all 200 decisions up front, do two `WHERE path IN (?,?,…)` and `WHERE name IN (?,?,…) OR qualified_name IN (?,?,…)` queries, then compute per-decision missingness from the resulting sets. Cuts round trips from O(200×(k+m)) to O(2).
- **Out of scope:** whether `OR name = ? OR qualified_name = ?` should be split into two probes for clarity vs the current multi-index-OR plan.

### F5 — `idx_claims_vet_ok` is dead weight — no query filters on it
- **Severity:** low
- **Reproducer:** `grep -rn 'vet_ok' internal/ cmd/` finds only inserts, the SELECT projection list in `queryUnverifiedClaims`, and the migration itself. No `WHERE vet_ok = ?` lives anywhere in the codebase.
- **Observed:** the index gets updated on every `RecordClaim` and `SupersedeClaimsForFile` but is never consulted by the planner.
- **Why it matters:** every claim INSERT pays the index-maintenance cost for nothing. With the v0.7.x post-edit hook writing a claim per edit, this is meaningful pure-loss write amplification.
- **Suggested fix shape:** drop the index in a v6 migration (`DROP INDEX idx_claims_vet_ok`). The column stays; only the unused index goes. If future queries want vet_ok filtering, add it back then with the partial-index pattern from F2.
- **Out of scope:** whether `index_ok` warrants its own index (also currently never filtered on, also dead-weight-by-default).

### F6 — `ListFiles` with only the `language` filter scans the files table
- **Severity:** low
- **Reproducer:** `EXPLAIN QUERY PLAN SELECT path, hash, language, size_bytes, indexed_at FROM files WHERE language = ? ORDER BY path` against a 50k-file DB.
- **Observed:** `SCAN files USING INDEX sqlite_autoindex_files_1` — uses the path autoindex purely as a scan ordering aid, not for the language filter. ~1.6 ms at 50k files for `language = 'go'`.
- **Expected:** would benefit from `CREATE INDEX idx_files_language ON files(language)`.
- **Why it matters:** the `list_files` MCP tool is plausibly called with `lang="go"` (the model wants a quick survey of Go files). At 50k files the absolute cost is fine, but the plan is inelegant and the cost grows linearly. Adding the index also helps the doctor query that segments by language (currently `SymbolCountsByFile` does the work, but a counts-by-language query would benefit).
- **Suggested fix shape:** add `idx_files_language`. Bonus: a composite `(language, path)` would cover both filter and ORDER BY in one b-tree.
- **Out of scope:** whether `path GLOB` patterns could be made anchored-prefix-aware (e.g., `internal/*` could in principle be a range scan on path).

### F7 — `.leonard.db-wal` grows unboundedly during bulk wipes; no `wal_checkpoint` ever called
- **Severity:** medium
- **Reproducer:** seed 100k files / 1M symbols, then batch-delete every file as `DeleteFiles` does. Inspect `.leonard.db-wal` afterward.
- **Observed:** WAL grew to **183 MB** after a 100k-file delete; the main DB shrank but the WAL stayed huge. `grep -rn 'wal_checkpoint\|PRAGMA wal' internal/` finds zero hits. Modernc.org/sqlite uses SQLite default `wal_autocheckpoint=1000` (pages, ~4 MB), but auto-checkpoint cannot reclaim WAL pages while any reader holds a snapshot — and the test had a long-lived `db` handle still open.
- **Expected:** either explicit `PRAGMA wal_checkpoint(TRUNCATE)` after large transactions (the prune sweep is the obvious candidate), or accept the bound and document it.
- **Why it matters:** on a real Claude Code session, the user's `.leonard/leonard.db-wal` can balloon to hundreds of megabytes during reindex of a polluted store (the v0.6.1 site-packages cleanup the comment in `DeleteFiles` cites). For laptop users with constrained disk, this is surprising. The WAL also keeps growing after `leonard index` until the process exits.
- **Suggested fix shape:** call `PRAGMA wal_checkpoint(TRUNCATE)` at end of `DeleteFiles` when `total > some_threshold` (say, 1000 rows), and possibly at end of a full `leonard index` run. Cheap and bounds disk usage. Alternative: bump `wal_autocheckpoint` lower, but that adds latency to every commit.
- **Out of scope:** WAL2 (a SQLite extension that swaps between two WAL files) — not available in modernc.org/sqlite as far as I can tell.

### F8 — `FindSymbolsByQuery` LIKE `%foo%` cannot use any index — by design, but worth documenting
- **Severity:** informational
- **Reproducer:** `EXPLAIN QUERY PLAN SELECT … FROM symbols WHERE name LIKE ? OR qualified_name LIKE ?` with leading-wildcard patterns.
- **Observed:** `SCAN symbols / USE TEMP B-TREE FOR ORDER BY`. At 50k symbols, the LIKE query takes ~11 ms. At 500k symbols (extrapolating linearly) ~110 ms.
- **Expected:** unavoidable with leading wildcards; SQLite's `LIKE` is also case-insensitive by default for ASCII which can suppress index use.
- **Why it matters:** acceptable today. If the corpus grows past Leonard's typical use case, this becomes the slowest read query. FTS5 would cover this in O(log) but is a much bigger change.
- **Suggested fix shape:** either (a) document the substring-search cost ceiling in DESIGN.md so users know not to lean on `find_symbol{kind:substring}` as a primary navigation tool at 100k+ symbols, or (b) add an FTS5 virtual table populated alongside symbols on insert. (b) is a non-trivial design change.
- **Out of scope:** anchored prefix optimization (LIKE 'foo%') — would be a separate query path.

### F9 — `ReplaceSymbols` prepares a fresh INSERT statement per call (not per-Store)
- **Severity:** low
- **Reproducer:** read `internal/store/store.go:393-400`. Every `ReplaceSymbols` call does `tx.Prepare(...)` then `defer stmt.Close()` once per file.
- **Observed:** the indexer calls `ReplaceSymbols` once per file. On Leonard's own repo (163 files) that's 163 prepare/close cycles, on a 10k-file repo it's 10k cycles. modernc.org/sqlite caches compiled statements internally so the cost is bounded, but it's an avoidable constant.
- **Expected:** prepared once per `*Store` lifetime, or via a `database/sql` Stmt cache (`db.Prepare`). The store could hold a `*sql.Stmt` and reuse it across calls.
- **Measured impact:** 100 ReplaceSymbols cycles × 20 symbols each = 42.8 ms total in my benchmark. Hard to attribute how much is prepare overhead vs the actual insert work. Likely sub-millisecond aggregate even at scale.
- **Suggested fix shape:** cache a long-lived `*sql.Stmt` on `*Store` and use `tx.Stmt(s.symbolInsertStmt)` to associate it with the per-call transaction. This is the canonical Go pattern. Wins are small but free.
- **Out of scope:** whether `ReplaceSymbols` should accept a slice of `(filePath, []Symbol)` so the entire indexer pass is one transaction (currently each file is its own tx — see F10).

### F10 — Indexer commits a separate transaction per file
- **Severity:** informational
- **Reproducer:** read `internal/index/indexer.go:445` — `i.Store.ReplaceSymbols(rel, syms)` opens-commits-closes once per file.
- **Observed:** for the leonard self-host (163 files), that's 163 transaction commits, each waiting for an `fsync` (default WAL behavior). For a 100k-file index, 100k commits. The walking time dominates the cost so this isn't user-facing yet, but it's the natural next bottleneck once parsing gets faster.
- **Expected:** indexer batches some N files into one transaction, trading recovery granularity for commit amortization.
- **Suggested fix shape:** out-of-scope for this lane (it's an indexer change, not a store change) — note it as the next-tier perf lever once the cheap index adds land. If we add it, expose a `WithTx(func(*Tx) error)` helper on `*Store` so the indexer can decide batch boundaries without `*Store` knowing about the indexer's chunking.
- **Out of scope:** anything below `internal/store/`.

### F11 — `idx_symbols_qname` may be redundant given LIKE coverage
- **Severity:** informational
- **Reproducer:** check what currently uses `idx_symbols_qname`. The only equality-on-qualified_name query is `missingSymbols` (`WHERE name = ? OR qualified_name = ? LIMIT 1`) — which plans as a `MULTI-INDEX OR` using both `idx_symbols_name` and `idx_symbols_qname`. No other query filters on qname alone.
- **Observed:** the index pays its INSERT-time maintenance cost on every symbol write to help one rare codepath (stale-decision auditing).
- **Why it matters:** small. Mostly worth noting because if F4's batched query gets implemented, `missingSymbols` becomes `WHERE name IN (...) OR qualified_name IN (...) LIMIT N`, which the planner may handle differently.
- **Suggested fix shape:** leave alone unless write throughput becomes a measured problem. Mentioning for completeness — every index has a cost, and the qname index's benefit is concentrated in one quiet caller.
- **Out of scope:** any change here.

## Things that worked

- **`FindSymbolsByName`** uses `idx_symbols_name` cleanly: ~70 µs at 50k symbols.
- **`GetFile`** uses the primary-key autoindex: trivial.
- **`SymbolCountsByFile`** uses `idx_symbols_file` as a covering index: no row reads needed.
- **`SupersedeClaimsForFile`** uses `idx_claims_file_path`: efficient.
- **`SupersedeDecision`'s lookup** is on integer primary key: trivial.
- **`DeleteFiles`** plans cleanly after v0.7.1's `idx_symbols_parent`: `SEARCH files USING INDEX … / SEARCH symbols USING COVERING INDEX idx_symbols_file (file_path=?)` — no leftover SCAN. Removing the parent index in a control test showed the cascade phase reverts to `SCAN symbols`, confirming v0.7.1's diagnosis was correct.
- **Prune-sweep curve** at 1k/10k/100k files is cleanly linear (41 ms / 501 ms / 5.8 s for the full wipe with the v0.7.1 index in place). No superlinear bottleneck lurking.
- **Concurrent WAL** works as advertised: 596,886 concurrent reads and 1,157 writes in 3 seconds with no `database is locked` errors. Readers see a consistent snapshot of pre-writer-commit state without blocking.
- **`ReplaceSymbols`** tag-to-real-ID remap is O(N) — single pass, O(1) map insert/lookup per symbol. Not the O(N²) failure mode the brief asked me to rule out.
- **Migration cost is tiny**: building `idx_symbols_parent` on a 100k-symbol table takes 50 ms. Adding F1/F2/F3's proposed indexes will be similarly invisible.

## Open questions

- **F7 disposition.** Auto-checkpoint defaults *do* reclaim WAL space in normal operation; the 183 MB number I measured was during a tight benchmark loop that held an open reader connection the whole time. Real Leonard usage (process exits between operations) probably never sees this. Worth a separate confirmation under realistic usage before adding explicit checkpoint calls.
- **F2 partial-index trade-off.** SQLite partial indexes work but the planner sometimes ignores them in favor of simpler alternatives. The 1.7× number was on a 10% selectivity workload; at higher selectivity (more unverified claims accumulating) the gap could narrow. Worth EXPLAIN-checking on a real corpus before committing to the index shape.
- **F4 cost in practice.** 200 decisions × 5 refs each is hypothetical; Jason's CLAUDE.md shows zero decisions on his own self-host today. The N+1 is real but the absolute time at typical decision counts is sub-millisecond. Prioritize after F1/F2.
- **Whether `idx_files_indexed_at` (F1) should be DESC.** SQLite indexes are bidirectional by default for equality but the planner can use either direction for range. The "ORDER BY indexed_at DESC" might pick up an extra optimization with `CREATE INDEX … ON files(indexed_at DESC)`. Worth benching the variant.
- **leonard-hook v0.12.0 vs v5 schema.** While running my scratch programs, every Write/Edit to `/tmp/` (outside the leonard repo) triggered the project's post-edit hook, which panicked with `store: db schema v5 newer than supported v4`. That suggests the `~/go/bin/leonard-hook` binary in PATH was built against an older schemaVersion constant than the repo's current source. Not a store-perf concern, but worth flagging — the brief said "current `~/go/bin/leonard*` binaries are v0.12.0" but they don't recognize schema v5 from v0.7.1.
