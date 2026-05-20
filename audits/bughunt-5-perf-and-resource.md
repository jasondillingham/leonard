# Bug Hunt #5 — perf-and-resource

## Summary

Audited Leonard v0.38.0's resource/perf surface after the v0.19–v0.38 expansion
(tree-sitter helper covering 29 grammars, claim-ledger semantics, manifest
dep-graph). Measured subprocess overhead, scale behavior of the claims table,
WAL growth, hook latency, and memory characteristics with synthetic 10k-file
polyglot fixtures and 100k–500k claim ledgers.

Headline results: the v0.13 `maxIndexedFileBytes=8 MiB` cap saves the indexer
from the worst pathological cases, but **a single ~8 MiB file in some grammars
spikes helper-process RSS to ~840 MB** (Ruby) or ~790 MB (Rust syn). Cold
polyglot indexing throughput is **~73 files/sec — a ~42x regression from the
v0.6 Go-only path (~3000 files/sec)** — driven entirely by per-file subprocess
fan-out. `get_unverified_claims` reads the entire claims table even when
returning the default 50 rows, costing **~1.15 s on a 500k-claim ledger**. The
WAL file grows monotonically across hook invocations (11 MB after 700 hooks
on a near-empty repo) and is never checkpointed except inside `DeleteFiles`.
Race detector clean; no SQLite "database is locked" errors observed under 100
concurrent post-edit hooks. Pre/post-edit hooks land in **30–140 ms on
unchanged-vet-verb-Go projects** — under the 200 ms perceptual budget — but a
`vet=ok` post-edit that triggers `SupersedeOutstandingFailures` on a 50k-claim
ledger is **~140 ms**, and configuring `cargo check` raises the worst-case
ceiling to the 30 s `VetTimeout`.

Severity rubric used: per `bughunt-1-brief.md` (high / medium / low / informational).

## Findings

### F1 — Big-file extractor RSS amplification (~100x for some tree-sitter grammars, ~130x for Rust syn)
- **Severity:** medium
- **Reproducer:**
  ```bash
  # 7.7 MB Ruby file (under the 8 MiB maxIndexedFileBytes cap)
  python3 -c "
  lines = [f'  def m{i}; {i}; end' for i in range(310000)]
  open('/tmp/big.rb','w').write('class Big\n' + '\n'.join(lines) + '\nend\n')
  "
  /usr/bin/time -l \
    internal/parse/treesitter/target/release/leonard-extract-treesitter \
    --lang ruby /tmp/big.rb < /tmp/big.rb > /dev/null
  ```
- **Observed:** the helper process peaks at **839 MB RSS** while processing a
  ~7.8 MB Ruby file (`peak memory footprint = 837 MB`). Other measurements:
  | Helper | File size | Peak RSS | Amplification |
  |---|---|---|---|
  | leonard-extract-treesitter (java) | 7.7 MB | 326 MB | 42x |
  | leonard-extract-treesitter (cpp) | 6.4 MB | 255 MB | 40x |
  | leonard-extract-treesitter (ruby) | 4.6 MB | 491 MB | 106x |
  | leonard-extract-treesitter (ruby) | 7.8 MB | 839 MB | 107x |
  | leonard-extract-rust (syn) | 6.1 MB | 787 MB | 129x |
- **Expected:** the v0.13 `maxIndexedFileBytes=8 MiB` cap was sized to bound
  allocator pressure (bughunt-4 caps F4 measured 172 MB → 480 MB on the prior
  no-cap path). The cap protects the indexer but tree-sitter / syn parse-tree
  representations are not flat: each declaration produces multiple AST nodes
  whose footprint grows >>linearly with source size, and the indexer
  blocking-waits on the helper so the parent's RSS doesn't reflect it. On a
  Mac with 16 GB RAM this is recoverable; on a 4 GB CI box or a Linux machine
  configured with low cgroup memory limits, a single ~5 MB Ruby file would
  OOM-kill the helper subprocess.
- **Suggested fix shape:** either (a) tighten `maxIndexedFileBytes` to ~2 MiB
  (the realistic "human-authored source file" upper bound — generated files
  larger than this are usually skip-list candidates), or (b) make the cap
  per-language (Rust syn and Ruby/Cpp tree-sitter clearly need a lower
  ceiling than the small-grammar languages), or (c) add cgroup-style RSS
  capping by setting `cmd.SysProcAttr` rlimits on Linux. Documenting the
  amplification factor in `DESIGN.md §7` would also help operators tune
  CI memory.
- **Out of scope:** whether tree-sitter has a streaming/cursor API that
  bounds memory; whether syn's AST footprint can be tuned via crate
  features.

### F2 — `get_unverified_claims` reads the entire claims table on every call
- **Severity:** medium
- **Reproducer:** seed a 500k-claim DB via sqlite3 (see /tmp/claimsbench-2),
  then call MCP `get_unverified_claims` with default arguments.
  ```
  500k claims, default limit (returns 50 rows):  med=1171ms  resp=14kB
  500k claims, limit=200 (returns 200 rows):     med=1149ms  resp=56kB
  ```
- **Observed:** the call site in `internal/mcp/claims.go:113` invokes
  `cs.GetUnverifiedClaims(ctx, sessionID, includeSuperseded)` which delegates
  to `store.queryUnverifiedClaims`. That SQL has no `LIMIT` clause —
  every unverified row is materialized into `[]Claim` before the caller
  slices to the configured limit (default 50, max 200). Two scans on a 500k
  ledger take ~1.15 s wall-clock and a corresponding heap allocation.
- **Expected:** when the API caps the response at 200 rows and 1 MiB, the
  SQL should also be bounded — push `LIMIT 200` down into the query so
  SQLite uses the `idx_claims_recorded_at` index (added in migrateV6 only
  on `recorded_at`, not on `(verified, recorded_at)` — see also F3) and
  stops after 200 rows.
- **Suggested fix shape:** thread `limit` from `getUnverifiedClaims` →
  `ClaimStore.GetUnverifiedClaims` → `queryUnverifiedClaims` and append
  `LIMIT ?` to the SQL. Bonus: a composite `idx_claims_verified_recorded`
  on `(verified, superseded_by_claim_id, recorded_at DESC)` would turn the
  ORDER BY into an index walk.
- **Out of scope:** the CLI `leonard claims unverified` has the same shape
  but I didn't probe whether the CLI uses a different query path.

### F3 — `idx_claims_verified` is useless when every row has `verified=0`
- **Severity:** low
- **Reproducer:** seed any ledger where 100% of claims are unverified
  (common in v0.38's reality — a "real claim" is unverified-by-design so
  the Stop hook can surface it):
  ```
  sqlite> EXPLAIN QUERY PLAN
          SELECT ... FROM claims WHERE verified=0 AND superseded_by_claim_id IS NULL
          ORDER BY recorded_at DESC, id DESC;
  SEARCH claims USING INDEX idx_claims_verified (verified=?)
  USE TEMP B-TREE FOR ORDER BY
  ```
- **Observed:** SQLite picks `idx_claims_verified` but since the filter
  cardinality matches every row, the index walks the full table anyway
  and then sorts via a temp B-tree. This is what makes F2 expensive.
- **Expected:** the planner should ideally pick a recorded_at-ordered
  index so the ORDER BY + LIMIT is a prefix scan. Currently `migrateV6`
  added `idx_decisions_recorded_at` on decisions but the analogous index
  on claims (`idx_claims_recorded_at`) was not added — the only
  recorded_at-ordered index access is via the bigger temp sort.
- **Suggested fix shape:** add `CREATE INDEX idx_claims_recorded_at ON
  claims(recorded_at DESC, id DESC)` in a new migration, or replace
  `idx_claims_verified` with a composite covering the WHERE + ORDER BY.
- **Out of scope:** whether the index is useful at all when the table
  is small (it isn't — SQLite full-scans tables under ~10k rows anyway).

### F4 — Per-file tree-sitter throughput is ~73 files/sec (vs ~3000 files/sec for in-process Go parsing)
- **Severity:** medium
- **Reproducer:** synthetic 10k-file polyglot project (~1250 files each in
  Go/Python/TS/Java/Ruby/C++/Rust/C#), cold index.
  ```
  Go-only 10k files (in-process go/parser): 3.29s  (~3000 files/sec)
  Polyglot 10k files (subprocess per file): 134.11s  (~73 files/sec)
  ```
- **Observed:** the indexer is single-threaded (`internal/index/indexer.go`,
  no goroutine pool, no errgroup) and dispatches one `exec.Command` per
  file for every language other than Go. Tree-sitter helper cold-call
  latency varies widely by grammar:
  ```
  lang        median (ms)   lang        median (ms)
  graphql       2.78         dart          6.05
  proto         2.96         r             7.71
  hcl           2.94         zig          10.00
  lua           3.01         sql          11.02
  nix           3.50         csharp       12.65
  make          3.51         ruby         13.12
  cmake         3.81         glsl         13.93
  solidity      4.02         scala        14.57
  just          4.11         elixir       25.44
  erlang        4.34         hlsl         25.33
  java          5.14         cpp          32.23
  bash          6.02         swift        37.03
  ```
  (syn extractor for Rust: ~2.2 ms median. Python helper: ~25 ms median.)
- **Expected:** sequential subprocess fan-out is the brief's documented
  trade-off and within range for typical projects (under ~5k files indexes
  in <1 min). But a worker pool sized to `runtime.NumCPU()` would close
  most of the 42x gap — each subprocess holds enough memory to make this
  worthwhile (8 cores × 10 ms parallel ≈ 1.25 ms wall per file vs 10 ms
  serial), and the existing per-file timeout already protects against
  one-bad-file stalling everyone.
- **Suggested fix shape:** swap `filepath.WalkDir` for a walker that
  enqueues into a bounded `worker pool` (or `errgroup.SetLimit`). The
  Store writes are already serialized through SQLite — only the parser
  fan-out needs concurrency. `parseCount.Add` is already atomic; only
  `parseFailures` needs a `sync.Mutex`.
- **Out of scope:** whether subprocess startup overhead can be amortized
  by reusing a long-running helper (F12 below).

### F5 — `SupersedeOutstandingFailures`' `claim LIKE '%=failed%'` does a full scan
- **Severity:** informational (acknowledged in the brief as a known limitation)
- **Reproducer:**
  ```
  500k claims, 5k matching '%=failed%':  UPDATE took 0.17s
  100k claims, 1k matching:              UPDATE took 0.09s
  ```
- **Observed:** SQLite can't use any index for a leading-wildcard LIKE.
  Planner uses `idx_claims_verified` to filter, then evaluates the LIKE
  pattern per row. Cost is linear in `count(verified=0 AND not superseded)`.
  At realistic ledger sizes (≤10k claims for a typical project) this is
  imperceptible; at the 500k synthetic scale it's still under a second.
- **Expected:** the cost scales with ledger size and SQLite has no
  better plan available without a structured "is_failure_claim" column.
- **Suggested fix shape:** if this ever becomes hot, the existing v0.13
  `vet_ok` column already encodes the same predicate as an indexable
  integer — `claim LIKE '%=failed%'` could be replaced with
  `vet_ok = 0` (note `idx_claims_vet_ok` already exists). Old rows
  predating the column have `vet_ok=NULL`, so the migration would need
  a backfill pass or the predicate would become
  `(vet_ok = 0 OR (vet_ok IS NULL AND claim LIKE '%=failed%'))`.
- **Out of scope:** semantic question — does `SupersedeOutstandingFailures`
  want to match _every_ verb-style failure ("foo=failed") or only the
  known set (vet, cargo check, pnpm tsc)?

### F6 — WAL grows monotonically across hook invocations; no checkpoint between hooks
- **Severity:** medium
- **Reproducer:** 700 sequential post-edit hooks against a small project
  with a 50-file Go workspace.
  ```
  iter 100: db=212992B    wal=2257792B
  iter 200: db=282624B    wal=4478472B
  iter 300: db=307200B    wal=6662072B
  iter 400: db=335872B    wal=8956912B
  iter 500: db=364544B    wal=11152872B
  iter 700: db=~400KB     wal=~11MB
  ```
  After running `leonard doctor` (which keeps the connection open longer):
  ```
  After doctor:  leonard.db-wal: No such file or directory
  ```
- **Observed:** SQLite's default `wal_autocheckpoint=1000` (~4 MB) is in
  effect. But each `leonard-hook` invocation opens, writes a few rows,
  and closes — the auto-checkpoint runs at commit time, but the writer
  process exits before reading the WAL header to confirm a successful
  truncate. The WAL grows to 11 MB on a project where the main DB is
  400 KB. A long-running connection (`leonard doctor`, `leonard index`)
  triggers the checkpoint and the WAL vanishes.
- **Expected:** if `leonard-mcp` is running continuously alongside, its
  long-lived connection should checkpoint regularly. But in a
  Claude-Code-only session where the user never invokes a `leonard`
  CLI command, the WAL grows forever until session end. On a `~/` git
  repo with months of churn between manual leonard runs, this is a
  real disk footprint.
- **Suggested fix shape:** either (a) call `PRAGMA wal_checkpoint(TRUNCATE)`
  at the end of each post-edit hook (a few ms; matches the
  v0.15 pattern of explicit checkpoints after big DELETEs), (b) lower
  `wal_autocheckpoint` to ~100 pages, or (c) have `leonard-mcp`'s
  long-running connection issue a periodic `PRAGMA wal_checkpoint(PASSIVE)`
  on a ticker.
- **Out of scope:** whether the modernc.org/sqlite driver respects
  `PRAGMA journal_size_limit` (it would auto-truncate at the limit).

### F7 — `leonard-extract-treesitter` re-compiles the .scm query on every invocation
- **Severity:** informational
- **Reproducer:** trace `internal/parse/treesitter/src/main.rs:991`.
- **Observed:** `Query::new(&lang.grammar, lang.query_src)` is called inside
  `extract()`, which runs once per process invocation. Since the process
  exits after one extraction, the query is parsed exactly once per file
  indexed — wasted work that scales with the polyglot file count, but the
  cost is sub-millisecond per call.
- **Expected:** caching would help only if F4's worker-pool fix or F12's
  daemon mode lands; for the current single-shot model it's fine.
- **Suggested fix shape:** `OnceLock<HashMap<&'static str, Query>>` keyed
  by language name. Trivial to add later — flagging now so the choice is
  conscious.
- **Out of scope:** how tree-sitter's `LanguageRef::query()` would change
  the equation; not currently exposed by the bindings.

### F8 — `leonard-extract-treesitter` binary size is 40 MB (29 grammars statically linked)
- **Severity:** informational
- **Reproducer:** `ls -l internal/parse/treesitter/target/release/leonard-extract-treesitter`
- **Observed:**
  ```
  leonard-extract-treesitter: 39,991,792 bytes (40 MB)
  leonard-extract-rust (syn):  1,745,856 bytes (1.7 MB)
  ```
  Each tree-sitter grammar adds ~1 MB of generated C parser code. The
  29-grammar release binary is ~40 MB — larger than any of the Go binaries.
- **Expected:** ship size matters for OSS adoption (every grammar adds
  ~1 MB of dead weight for the average user, who only uses 2-3 languages)
  but isn't a runtime concern. Cargo doesn't strip dead code from C
  dependencies the way Go does for unused Go packages.
- **Suggested fix shape:** Cargo feature flags per grammar (`--features
  java,rust,cpp`), or a build script that emits only the linked grammars
  used by `langExtractors`. Both are non-trivial — flag, don't fix.

### F9 — Manifest extractor emits one Symbol per dep, unbounded
- **Severity:** medium
- **Reproducer:** synthetic monorepo with 200 workspaces × 35 deps each =
  7000 manifest symbols from 200 `package.json` files.
  ```
  /tmp/leonard doctor on the monorepo:
    files: 401 total
      manifest    201
      typescript  200
    symbols: 7200 total
      manifest    7000
      typescript    200
  ```
  `verify dep-1` returns **200 matches** (one per workspace, indistinguishable
  from each other in CLI output):
  ```
  packages/pkg000/package.json:1  const  packages.pkg000.package.dep-1  dependency dep-1@^1.0.0
  packages/pkg001/package.json:1  const  packages.pkg001.package.dep-1  dependency dep-1@^1.0.0
  ...
  ```
- **Observed:** every dep in every manifest becomes a row in the `symbols`
  table with `kind=const`. There is no upper bound. A real monorepo
  (Babel, Next.js, Vue's main repo) routinely has 100+ workspaces with
  50+ deps — easily 10k symbols just from manifests, drowning out actual
  source symbols in `find_symbol` results.
- **Expected:** manifest symbols are useful for `verify_symbol("react")`
  but shouldn't dominate `find_symbol` substring searches. Two issues:
  (a) the kind is `const`, which is the same kind real source `const`s
  get — `find_symbol(query="utils", kind="const")` returns dep noise
  alongside real consts; (b) no de-duplication when the same dep appears
  in N workspaces.
- **Suggested fix shape:** distinct kind value (`kind="dependency"` or
  `kind="manifest_dep"`) so the existing kind filter does the work; or
  collapse multi-workspace duplicates into a single symbol with all
  declaring files listed in evidence; or cap manifest-emitted symbols at
  a per-file budget (e.g. first 500 deps, with a parse-failure note for
  the rest).
- **Out of scope:** whether `leonard-mcp`'s tool descriptions warn the
  model about manifest symbols (didn't audit the tool prose).

### F10 — Cold start of `leonard-mcp` is ~12 ms
- **Severity:** informational (works well)
- **Reproducer:** 5 trials launching `leonard-mcp`, sending initialize +
  initialized + a single tools/call:
  ```
  trial 0: init=12.5ms  first_tool=0.7ms  total=13.2ms
  trial 1: init=11.2ms  first_tool=0.6ms  total=11.8ms
  trial 2: init=10.9ms  first_tool=0.7ms  total=11.6ms
  trial 3: init=11.0ms  first_tool=0.6ms  total=11.6ms
  trial 4: init=13.1ms  first_tool=0.2ms  total=13.3ms
  ```
- **Observed:** DB open + migration check + adapter wire-up + JSON-RPC
  handshake completes in ~12 ms cold. First `find_symbol` after that
  is sub-millisecond.

### F11 — Hook latency budget: ~30-140 ms in steady state, 30 s ceiling with `cargo check`
- **Severity:** informational
- **Reproducer:** various hook flavors against a small Go project,
  median across 5 trials per case.
  ```
  pre-edit (no edit verification path):           ~33 ms
  post-edit on cache-hit Go file (no re-parse):   ~32 ms
  post-edit on Go file with go vet=ok:            ~38 ms
  post-edit on tree-sitter Java file (no vet):    ~38 ms
  post-edit on tree-sitter C++ file (no vet):     ~62 ms
  post-edit on Python file (gpython helper):      ~30-65 ms (varies)
  post-edit cold first-call on go.mod project:    ~650 ms (go vet warmup)
  post-edit warm with 50k-claim supersede:        ~140 ms
  session-start:                                  ~31 ms
  stop:                                           ~32 ms
  ```
- **Observed:** steady-state hooks all under 200 ms — the perceptual
  budget. Outliers:
  * Cold-cache go vet on first invocation on a new project: 650 ms
  * `cargo check` (v0.37 verify config) is configurable but bounded by
    `VetTimeout = 30 seconds`. A real `cargo check` on a non-tiny crate
    is 5-60 seconds — well above the 200 ms threshold. The current
    design records this in the claim ledger asynchronously from Claude's
    perspective, but Claude's tool-output stream blocks until the hook
    returns.
- **Expected:** matches the brief. The 30 s `VetTimeout` is a known
  upper bound for users who configure `cargo check`.
- **Suggested fix shape:** consider whether to fire-and-forget the vet
  step (record claim asynchronously after returning to Claude) — this
  changes semantics meaningfully so it's a design call. Documented as
  informational, not a fix recommendation.

### F12 — No worker pool / no daemon mode: subprocess fan-out is the dominant cost
- **Severity:** medium (overlaps F4; tracking separately so the fix shape
  is clearly orthogonal)
- **Reproducer:** see F4. Also confirmed by grepping for goroutine /
  worker / pool primitives in the indexer:
  ```
  $ grep -rn "go func\|errgroup\|sync.WaitGroup" internal/index/
  (no matches)
  ```
- **Observed:** the indexer iterates files via `filepath.WalkDir` and
  inline-calls `i.indexAbs(path)`. Each call either short-circuits on
  hash-match or invokes one subprocess. For a polyglot index, ~25 of
  the 29 grammars use tree-sitter via `leonard-extract-treesitter`, so
  every non-Go file is a full process fork + grammar load + parse +
  serialize cycle.
- **Expected:** acceptable for the v0.6 Go-only design. Now that
  v0.19-v0.38 added 25+ subprocess-mediated languages, the design
  doesn't scale to the polyglot case it now supports.
- **Suggested fix shape:** two orthogonal options:
  * **Worker pool:** parallelize the WalkDir → indexAbs path with
    `runtime.NumCPU()` workers. SQLite writes already serialize via
    busy-timeout; only the parse step needs fan-out. Lowest-risk;
    delivers 4-8x on the polyglot path.
  * **Long-running helper:** spawn one `leonard-extract-treesitter
    --daemon` per language and pipe file content + path over a
    framed protocol. Saves the per-call 2 ms fork overhead and the
    grammar load. Higher complexity (helper lifecycle, error
    recovery) but enables sub-ms per-file extraction.

### F13 — Tree-sitter helper binary is 40 MB and ships even when only a few languages are used
- **Severity:** informational
- See F8 — folded as a duplicate, leaving the cross-reference.

### F14 — `migrateV7` is O(rows-matching-pattern), fast at realistic scale
- **Severity:** informational
- **Reproducer:** 10k claims, 1819 matching the v7 cleanup patterns,
  schema downgraded to v6:
  ```
  DELETE FROM claims WHERE claim LIKE '%path escapes project root%'
                       OR claim LIKE '%index=skipped (file not found)%';
  ```
- **Observed:** ~90 ms for the SQL alone; ~900 ms wall-clock for
  `leonard doctor` to detect-and-upgrade including process startup.
  Both fine.

### F15 — No FD exhaustion risk on macOS (`ulimit -n = 1048576`)
- **Severity:** informational
- **Reproducer:** 10k-file polyglot index with `lsof -p <pid>` sampling.
- **Observed:** FD count stays at 16-21 throughout the index pass
  (DB connection + WAL files + stdin/out/err of the current
  extractor subprocess). Sequential indexer = bounded FD usage.
- **Expected:** if F4/F12's worker pool lands, each worker holds 3-4
  pipe FDs to its current subprocess. 8 workers = ~50 FDs. Still well
  under the 256-default Linux limit.

### F16 — OTel build adds 124% to `leonard-hook` size (improvement from bughunt-4's 137%)
- **Severity:** informational
- **Reproducer:** `go build -o /tmp/leonard-hook ./cmd/leonard-hook` vs
  `go build -tags otel -o /tmp/leonard-hook-otel ./cmd/leonard-hook`.
  ```
  no-otel: 12.06 MB
  otel:    27.03 MB
  delta:   +15.7 MB (+124.2%)
  ```
- **Observed:** the OTel SDK pulls in gRPC, protobuf, metrics exporters,
  trace exporters. Modest reduction from bughunt-4's 137% — likely
  v0.x dep updates trimmed some duplication.
- **Expected:** acceptable for a tagged build. Default build stays under
  13 MB.

### F17 — Race detector clean across the full test suite
- **Severity:** informational
- **Reproducer:** `go clean -testcache && go test -race ./...`
- **Observed:** all 10 packages pass under `-race` (timings 1-5 s
  per package). No goroutine leaks, no data-race warnings.

### F18 — 100 concurrent post-edit hooks: no DB locking, no errors
- **Severity:** informational (works well)
- **Reproducer:** spawn N goroutines, each `subprocess.Popen([leonard-hook,
  post-edit])` writing to the same `.leonard/leonard.db`. Synthetic 50-file
  Go workspace.
  ```
  N=10:  150 ms total,  0 fails
  N=50:  179 ms total,  0 fails
  N=100: 303 ms total,  0 fails
  ```
  After 100 concurrent hooks, all 100 files appear in the symbols table
  exactly once, all 100 claim rows recorded.
- **Observed:** `busy_timeout(5000)` plus WAL mode handles the contention.
  Hooks serialize on the writer lock but no time-outs.
- **Expected:** matches DESIGN.md §7 Q7 ("SQLite WAL handles most cases").
- **Note:** with a large pre-existing claims table (50k rows) + concurrent
  vet=ok hooks each triggering `SupersedeOutstandingFailures`, average
  per-hook latency rises to ~75 ms (30 hooks → 2.2 s total). Linear with
  ledger size.

### F19 — Concurrent `leonard index` + post-edit hooks: no conflicts
- **Severity:** informational
- **Reproducer:** start a `leonard index` and fire 50 parallel
  post-edit hooks against the same DB.
- **Observed:** both complete cleanly, final symbol/file counts match
  the union of both writers.

### F20 — Per-file extractor timeout is 30 s; sequential indexer can stall
- **Severity:** low
- **Reproducer:** a pathologically bad file (synthetic infinite-loop
  tree-sitter input) would block the indexer for up to 30 s. Wasn't
  hit in practice — included for completeness.
- **Observed:** `internal/parse/treesitter.go:30 — treesitterTimeout = 30 *
  time.Second`. Same for rust.go (rustTimeout) and python.go
  (pythonTimeout). A stuck extractor freezes the sequential indexer
  walk.
- **Expected:** with F4/F12's worker pool, a 30 s stall on one file
  only loses 1/N of throughput while other workers proceed.
- **Suggested fix shape:** the timeout itself is fine; the architectural
  fix is the worker pool from F4/F12.

### F21 — DB disk footprint: ~6 MB per 10k polyglot files
- **Severity:** informational
- **Reproducer:** 10k-file synthetic polyglot → 25k symbols → 5.6 MB DB.
- **Observed:**
  ```
  10000 files, 25003 symbols → 5.6 MB (database) + ~0-11 MB (WAL)
  ```
  Roughly 220 bytes per symbol row including indexes. Predictable
  growth. Manifest-heavy projects (F9) would skew this — 7k manifest
  symbols on a 401-file project is ~1.5 MB just from deps.

## Things that worked

- **`maxIndexedFileBytes=8 MiB` enforces a parse failure record on
  oversized files** without attempting extraction — verified by
  inspecting `indexer.go:571-578`. The 480 MB worst case from
  bughunt-4 caps F4 is prevented; F1's findings are within the
  remaining cap.
- **The `idx_symbols_parent` (v0.7.1) and recent_changes indexes
  (v0.15) hold up at scale** — verify_symbol and find_symbol queries
  on the 10k-file polyglot DB return in sub-millisecond.
- **Race detector clean** across the full test suite (F17).
- **No SQLite "database is locked" errors** under 100 concurrent
  post-edit hooks (F18).
- **`busy_timeout(5000)`** prevents contention errors even when a
  long-running supersede UPDATE serializes with concurrent writers (F18).
- **WAL-mode reads** are non-blocking — confirmed concurrent
  `leonard index` + post-edit hooks coexist without errors (F19).
- **Hook latency** stays under 200 ms in steady state (F11), within
  the perceptual budget for the user.
- **`get_unverified_claims` 1 MiB response cap** (bughunt-4 mcp F2)
  prevents runaway responses; the underlying query cost (F2) is
  the remaining concern.
- **`leonard-mcp` cold start is ~12 ms** (F10), well within the
  Claude Code subprocess-launch budget.

## Open questions

- **Is a worker pool (F4/F12) a v0.39 priority?** A 4-8x indexing
  speedup on polyglot projects is the single biggest perf win the
  audit surfaced. Implementation risk is moderate (concurrent
  `parseFailures` append needs a mutex, telemetry spans need
  per-worker scoping). Worth a separate brief.
- **Should `maxIndexedFileBytes` drop from 8 MiB to ~2 MiB (F1)?**
  Current cap doesn't prevent ~840 MB helper RSS on a single file.
  A 2 MiB cap would keep amplification under ~250 MB, which is
  safe on most CI boxes. Trade-off: a few real source files
  (large generated parsers, vendored bundles that snuck past
  skip-dirs) would now skip extraction.
- **Manifest symbol kind (F9)** — is the right answer a new kind
  string (`"dependency"`), a separate `manifest_deps` table joined
  on read, or a per-workspace dedup? The current design pollutes
  `find_symbol` results on monorepos. Picking the right shape
  depends on the model's expected usage pattern, which I didn't
  probe.
- **WAL checkpoint policy (F6):** the lowest-risk fix is
  `wal_checkpoint(TRUNCATE)` in post-edit hook's cleanup, but it
  adds ~5 ms to every hook. Alternative: lower `wal_autocheckpoint`
  to ~100 pages. Either is fine; needs a call.
- **`cargo check` hook latency (F11)** — if real users routinely
  configure cargo check, Claude is going to see multi-second hook
  responses. Is this acceptable? DESIGN.md doesn't take a position.

## Scratch files left in /tmp

- `/tmp/polyglot/` — 10k-file polyglot fixture (8 languages × 1250 files
  each). Used for F4, F21. Re-create via the Python snippet in F4.
- `/tmp/claimsbench-2/` — 100k-then-509k synthetic claims ledger. Used
  for F2, F3, F5. Re-create via the sqlite3 snippet in F2.
- `/tmp/hookrace/` — 50-file Go workspace + go.mod, exercised by F6,
  F11, F18, F19.
- `/tmp/monorepo/` — 200-workspace monorepo with 7000 manifest deps.
  Used for F9.
- `/tmp/migrate-test/` — schema-v6 DB used to time migrateV7 (F14).
- `/tmp/big-near-cap.{rb,cpp,java,rs}` — near-8-MiB files for F1.
- `/tmp/tiny.*` — single-decl fixtures for the per-language helper
  timing table in F4.
- `/tmp/leonard`, `/tmp/leonard-hook`, `/tmp/leonard-mcp`,
  `/tmp/leonard-hook-otel` — v0.38.0 binaries used throughout.
