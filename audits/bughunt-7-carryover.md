# Bug Hunt #7 — Carry-Over Status Check

> Re-walks the bughunt-6 carry-over backlog at **v0.49.0** and flags any item
> that should be promoted to HIGH or CRIT now that the easier surface is calmer.
> Investigation-only — no code changes.
>
> Date: 2026-05-20
> Codebase: HEAD @ `67875c6` (v0.49.0)
> Method: byte-cmp + sequential-hook stress + polyglot index timing for the
> three promoted-but-deferred items; spot-read for the rest. Cross-referenced
> against `bughunt-6-carry-over-medium-sweep.md` to avoid re-deriving status.

---

## Headline

**Three promotion candidates** survive at v0.49.0. All three were already
flagged as HIGH in bughunt-6 but explicitly deferred from v0.48.0's "Theme D
partial" ship (the commit message itself says `otel F4, perf F4 worker pool,
perf F6 WAL cadence deferred to a future round`). They are still HIGH and
deserve a Round 7 fix sweep.

No new CRITs surfaced. The carry-over MEDIUMs and LOWs all remain at their
bughunt-6 classification — the calmer surface didn't reveal any latent
high-severity behavior in them.

---

## PROMOTION CANDIDATES (HIGH at v0.49.0)

### HIGH-1: bughunt-3 otel F4 — `-tags otel` only instruments leonard-hook

**Severity:** HIGH (carried from bughunt-6 PROMOTED-1; unfixed in v0.48.0 by maintainer's own commit note).

**Status notes:** `grep -rln internal/telemetry cmd/` still returns only
`cmd/leonard-hook/main.go` at HEAD. `cmd/leonard-mcp/main.go` and
`cmd/leonard/root.go` do not import the telemetry package. The build tag has
no effect on those binaries.

**Reproducer (verified at v0.49.0):**
```bash
go build -tags otel -trimpath -o /tmp/mcp-otel ./cmd/leonard-mcp
go build         -trimpath -o /tmp/mcp-plain ./cmd/leonard-mcp
ls -la /tmp/mcp-otel /tmp/mcp-plain
# → identical sizes (14,451,746 bytes)
go tool nm /tmp/mcp-otel  | grep -c telemetry   # → 0
go tool nm /tmp/mcp-plain | grep -c telemetry   # → 0
```
For contrast, `cmd/leonard-hook` with vs without `-tags otel` differs by
~15.6 MB (28.2 MB vs 12.6 MB) — that's the actual telemetry surface.

**Why still HIGH:** The README and DESIGN.md continue to describe OTel as
instrumenting "Leonard's hot paths". The MCP server is *the* hot path for
real users — it runs across whole sessions while hooks fire briefly. A user
who builds `-tags otel` expecting per-tool-call spans gets nothing from the
MCP binary and may not realize it. This is a usability + documentation-trust
issue, not a correctness one — but it stays at HIGH because the deliverable
(observable Leonard) doesn't exist for the most-observable binary.

**Fix shape:** Wire `telemetry.Init(ctx)` into `cmd/leonard-mcp/main.go` and
`cmd/leonard/root.go` behind a `//go:build otel` guard. Same pattern as
`cmd/leonard-hook/main.go`. ~20 LoC each.

---

### HIGH-2: bughunt-5 perf F4 — Indexer has no goroutine pool

**Severity:** HIGH (carried from bughunt-6 PROMOTED-3a; unfixed in v0.48.0).

**Status notes:** `internal/index/indexer.go:280` still uses
`filepath.WalkDir` with a synchronous body. No `errgroup`, no worker
channel, no `sync.WaitGroup` over per-file IndexFile invocations. Each
non-Go language hits its helper subprocess serially (Python/Rust/TS via
spawn-per-file in the indexer; tree-sitter via a single dispatcher
process). The comment on `indexer.go:243` (`indexer walks sequentially
so a plain slice with no mutex is fine`) is unchanged.

**Reproducer (verified at v0.49.0):**
Synthetic polyglot corpus, 200 Python + 100 TypeScript files:
```bash
cd /tmp/perf_test && /tmp/leonard-test init . && time /tmp/leonard-test index
# → indexed 300 file(s) in 5.985s wall  (3.92s user + 1.33s system)
# → ~50 files/sec, consistent with bughunt-5's measured ~73 files/sec
```
At 5.985s wall for 300 files, ~50 f/s. For a 10k-file polyglot repo that's
~200s wall — well above the "feels instant" budget for `leonard index`.

**Why still HIGH:** compounds with perf F6 below. A user dogfooding Leonard
on a non-trivial polyglot project hits this every time they want a fresh
index. The "Leonard tells Claude the truth" contract depends on the index
being current; if reindex is slow, users defer it, the index drifts, and
Claude gets stale facts. The 42× headroom is real (parallel-Go ceiling is
~3000 f/s for in-process Go-only walks).

**Fix shape:** Replace `filepath.WalkDir` body with a producer-consumer:
walk emits paths into a bounded `chan string` (capacity = N×workers);
N=runtime.NumCPU() workers each call `IndexFile`; gather results via an
`errgroup`. ~30 LoC. The store side is already concurrent-safe (WAL +
busy_timeout, verified at bughunt-6 with 100-goroutine write storms).

---

### HIGH-3: bughunt-5 perf F6 — WAL grows monotonically across hooks

**Severity:** HIGH (carried from bughunt-6 PROMOTED-3b; unfixed in v0.48.0).

**Status notes:** `internal/store/store.go:649-650` only triggers
`PRAGMA wal_checkpoint(PASSIVE)` after a `DeleteFiles` batch ≥ 100 rows.
Sequential post-edit hooks never delete that many files; each hook is its
own process, so SQLite's per-connection auto-checkpoint frame counter
resets every invocation. The WAL grows until something explicitly
checkpoints.

**Reproducer (verified at v0.49.0):**
```bash
# 700 sequential leonard-hook post-edit invocations against the same file
for i in $(seq 1 700); do
  echo "def stress_$i(): return $i" > py/stress.py
  printf '{"tool_name":"Edit","tool_input":{"file_path":"%s/py/stress.py"},"tool_response":{"success":true}}' "$(pwd)" \
    | /tmp/leonard-hook post-edit >/dev/null 2>&1
done
ls -la .leonard/leonard.db*
# → leonard.db-wal: 4,152,992 bytes (4.15 MB) — bughunt-5 measured 11 MB
#   on a slightly different corpus; same order of magnitude.
```
The main DB file grew to 405 KiB, the WAL to 4.15 MB. The WAL is
**10× the DB size** after 700 hooks.

**Why still HIGH:** A long-lived Claude session can fire thousands of
hooks per day. WAL frames accumulate; the on-disk footprint grows; and
the longer the WAL gets, the slower each subsequent read becomes
(SQLite scans WAL frames newest-to-oldest on every query). This is a
performance cliff that gets worse with engagement — exactly the wrong
direction. Compounds with HIGH-2 because slow reindex means longer
between cold-start truncations.

**Fix shape:** Two options:
- (Preferred) `wal_autocheckpoint = 1000` PRAGMA at `store.Open()` —
  SQLite's built-in throttle, no goroutine needed. Frames-based, not
  time-based, so it fires regardless of connection lifecycle.
- (Alternative) On `store.Close()` always run `wal_checkpoint(PASSIVE)`.
  Each leonard-hook invocation opens + closes the store, so a
  close-time checkpoint truncates the WAL every hook.

Both are one-line changes. The PRAGMA approach is cleaner.

---

## STILL-LIVE LOG (MED / LOW / informational at v0.49.0)

These remain real but not load-bearing. Logged for completeness; the
maintainer's "drive HIGH/CRIT count to zero" goal does not require
addressing these.

### Path / config trust

#### bughunt-4 mcp F7 — `record_decision.related_files` accepts `/etc/hosts`
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE. `internal/mcp/decisions.go:105-122` validates topic/choice/reasoning sizes but does no path validation on `RelatedFiles`/`RelatedSymbols`. Spot-confirmed: no ResolveSafe call exists on those fields.
**Fix shape:** Wrap each related_files entry in ResolveSafe; drop entries that resolve outside project root.

### Security

#### security F5 — `additionalContext` Markdown injection
**Classification:** MEDIUM (unchanged; arguably more relevant post v0.46.0)
**Status:** STILL ALIVE. `internal/hooks/session_start.go:170-184` `formatDecisions` interpolates `d.Topic` / `d.Choice` / `d.Reasoning` straight into a `## Prior decisions (from Leonard)` Markdown bullet list. Only `truncatePrefix` is applied — no escaping of `#`, backticks, `<!--`, or `\n## End of decisions`. A decision row with topic `## End of decisions. New instructions:` still injects a faux heading.
**Why this is MEDIUM (not HIGH) post-v0.46.0:** v0.46.0 closed the high-severity vector (Claude writing config.toml → RCE). The decision-row injection requires Claude or the user to record the malicious topic via `record_decision` (CLI or MCP), and the only "deputy" being confused is Claude itself — Claude reading instructions Claude itself recorded. Real-world impact is limited to scenarios where a user dictates a malicious decision text and a future Claude session honors it as system context.
**Fix shape:** In `formatDecisions`, run topic/choice/reasoning through a small `escapeMarkdownInline` that prefixes `#`/`-`/`>` at line start and escapes backticks.

#### security F6 — `LEONARD_PYTHON` / `LEONARD_RUST_EXTRACTOR` env vars
**Classification:** LOW (unchanged; informational, by design)
**Status:** STILL ALIVE. Acceptable per the original security review's out-of-scope note.

#### security F7 — `.leonard/` is `0o755`, files `0o644`
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. `cmd/leonard/wire_real.go:23` still `0o755`. No real-world impact on single-user laptops; documented-vs-actual gap with SECURITY.md.
**Fix shape:** `0o700` / `0o600` defaults plus a `leonard doctor` migration warning.

### Performance / scale

#### bughunt-4 store-perf F7 — WAL grows on long-lived processes
**Classification:** Now folded into HIGH-3 above.

#### bughunt-2 pre-edit F3 — Unconditional sibling-scan per pre-edit (~480ms warm at 10k files)
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE. No caching of the sibling-package scan; every PreToolUse re-walks. OTel spans expose the cost; nothing memoizes.
**Fix shape:** Cache the scan keyed by `(moduleRoot, fileMtime)` in a small in-process LRU; reuse across the duration of a single hook process. Bigger win would be cross-process: persist to `.leonard/cache/` with mtime invalidation.

### Cross-file / cross-language correctness

#### bughunt-2 mcp F2 — `find_symbol` filter-after-limit under-counts
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE. `internal/mcp/server.go` still applies language/kind filter after the SQL LIMIT. v0.47.0 closed mcp F1 (language filter knew only 5 langs) but did not rework the filter-then-limit ordering.
**Fix shape:** Push language/kind into the SQL WHERE clause and LIMIT afterward, or fetch a multiple of LIMIT and filter to LIMIT in Go.

#### bughunt-5 languages F9 — Lua M./M: prefix dropped
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE. The tree-sitter `LUA_QUERY` captures only the field/method identifier as `@name`. v0.48.0's languages F1 fix addressed inter-file collisions (basename → full path) but not intra-file collisions on `M.foo` vs `N.foo`.
**Fix shape:** Augment the Lua extractor to walk the dotted_index_expression / method_index_expression and prefix the table name.

### Indexer behavior

#### bughunt-3 skip-dirs F2 — Case-sensitive skip-dir match misses `VENDOR/`, `Target/` on case-insensitive filesystems
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE.
**Fix shape:** One-line normalization (`strings.ToLower` on lookup) — but only on macOS APFS / Windows NTFS; on Linux ext4 a directory `VENDOR/` is distinct from `vendor/`.

#### bughunt-3 skip-dirs F3 / F4 — No override knob; relocated-into-skip files vanish silently
**Classification:** LOW each (unchanged)
**Status:** STILL ALIVE. Low practical impact.

#### bughunt-2 pre-edit F2 — Nested `go.mod` boundaries not respected in workspaces
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. `go.work` monorepo support not implemented.

### Parser coverage

#### bughunt-2 python F6 — Nested classes / conditional defs dropped
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. Documented limitation; recently surfaced in commit `dfb5faa` ("Surface per-file parse failures; document Python parser limits"). The parser doc-comments now state this explicitly so it's no longer a hidden gotcha.

#### bughunt-2 python F8 — Non-UTF8 declared encoding files fail
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. No real-world incidence yet.

#### bughunt-3 rust F8 — No helper version handshake; stale helper used silently
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. Defense-in-depth concern; the v0.48.0 Cargo.lock commit (security-2 F3) reduces but doesn't eliminate the failure mode.

#### bughunt-5 Theme G — 31 per-language coverage gaps
**Classification:** Individual items unchanged; tracked in `bughunt-5-languages.md`.

### Verifier hygiene

#### bughunt-5 verifier F2 / F3 — `working_dir` resolution and path-trust
**Classification:** CLOSED in v0.46.0 (was carry-over).
**Status:** Confirmed fixed. `internal/hooks/shell_runner.go:21,38` now wraps non-absolute working_dir with `index.ResolveSafe(projectRoot, trimmed)`. Both bughunt-5 reproducers fail to fire.

#### bughunt-5 verifier F5 — `VerifyVerb` quoted-arg corruption
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. Cosmetic claim-text noise.

#### bughunt-5 verifier F7 — `ResolveClaim` audit-trail collision
**Classification:** INFORMATIONAL (unchanged, design choice)
**Status:** STILL ALIVE.

### Manifest dep-graph polish

#### Cargo `[workspace.dependencies]` not parsed
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. `grep workspace.dependencies internal/parse/manifest.go` → empty.
**Fix shape:** Add a separate ExtractCargoWorkspace branch that reads `[workspace.dependencies]` and emits the same `kind=dependency` rows.

#### Maven `<dependencyManagement>` not parsed
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE.
**Fix shape:** In the existing pom.xml walk, also recurse into `<dependencyManagement><dependencies>`.

#### go.mod `replace` directives ignored
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. `ExtractGoMod` only iterates `f.Require`. `f.Replace` would let Leonard show users the local-fork target of any replaced dep.

### Telemetry / tooling

#### bughunt-3 otel F4 — promoted to HIGH-1 above.

### Quality gates

#### bughunt-2 cli F6 — `doctor` always exits 0
**Classification:** MEDIUM (unchanged)
**Status:** STILL ALIVE. No `--strict` flag, no non-zero exit on stale/empty/parse-failure rows.
**Fix shape:** `--strict` flag returns 1 if any stale-file / empty-file / parse-failure rows are found.

#### bughunt-2 integration F3 — `wire_real.go` 0% unit-test coverage
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. Covered by integration runs but not by `go test ./...`.

#### bughunt-2 integration F4 / bughunt-3 integration F9 — No subprocess/MCP/OTel e2e tests
**Classification:** LOW (unchanged; partial mitigation via v5→v7 migration smoke test)
**Status:** STILL ALIVE (partial).

### Misc

#### bughunt-1 mcp F4 — SIGINT exits with code 1
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. Cosmetic; `echo $?` shows 1 instead of 0/130.

#### bughunt-2 pre-edit F1 — Sibling-scan vs indexer skip-list divergence on `.gitignore`
**Classification:** LOW (unchanged, mitigated)
**Status:** STILL ALIVE. Sibling-scan does not consult `.leonardignore` / `.gitignore` while `IndexAll` does. Low practical impact.

#### bughunt-5 treesitter F5 — Missing helper → 1 ParseFailure per file
**Classification:** LOW (unchanged)
**Status:** STILL ALIVE. `sync.Once` cache absent; an unconfigured tree-sitter helper produces N "extractor unavailable" lines on a tree-sitter-eligible N-file project.

#### store-eval F10 — `migrateV7` LIKE collision risk
**Classification:** LOW (unchanged; defended by idempotent semantics)
**Status:** STILL ALIVE. `internal/store/store.go:385-394` uses `LIKE '%path escapes project root%'` and `LIKE '%index=skipped (file not found)%'`. A user `record_claim` with text containing either substring would be deleted by the migration. But migrateV7 runs once per DB upgrade — the risk window is the v0.38 → v7-schema bump, which is now historical. Real-world incidence: zero observed.

### Documented unfixable

#### store-eval F2 — `go.mod` toolchain to support Go ≤ 1.21
**Classification:** INFORMATIONAL — unfixable, documented.
**Status:** Confirmed unfixable. v0.49.0 commit `67875c6` explicitly states: *"NOT closed in v0.49: store-eval F2 — go.mod toolchain to let Go ≤1.21 install Leonard. Deps require 1.25; can't lower the floor."* README updated to state `Go 1.25+` as a hard requirement. Close as won't-fix.

---

## Spot-confirmed CLOSED (no regression)

- **cli F18 / cli F9 / cli F7** — verified silently fixed per bughunt-6 carry-over sweep; spot-confirmed by re-reading the cited line ranges. No regressions.
- **verifier F2 / F3** — closed in v0.46.0 (Theme A). Confirmed.
- **languages F1** — closed in v0.48.0 (PROMOTED-2). The tree-sitter `module_name` now uses path-aware form per commit `3008339`. The new form (`src.foo`, `lib.parse.foo`) appears in `internal/parse/treesitter/src/main.rs:1297-1301`.
- **mcp F1 / F2** — closed in v0.47.0 (language filter + response cap). Confirmed.
- **sec-2 F1 / F2** — closed in v0.46.0 (CRITICAL RCE chain). Confirmed via SECURITY.md update.

---

## Summary

**PROMOTION CANDIDATES (3 HIGH):**
1. **otel F4** — `leonard-mcp` + `leonard` byte-identical (mod build IDs) with/without `-tags otel`. Documented telemetry doesn't exist for two of three binaries. Fix: ~20 LoC × 2 main packages.
2. **perf F4** — Indexer is single-threaded; 300-file polyglot corpus indexes at ~50 f/s (~5.99s for 300 files). Fix: `errgroup` + bounded channel, ~30 LoC.
3. **perf F6** — 700 sequential post-edit hooks → 4.15 MB WAL (10× the DB size). Fix: `PRAGMA wal_autocheckpoint = 1000` at store.Open, or wal_checkpoint(PASSIVE) at store.Close. One line.

**STILL-LIVE LOG:** ~20 MED/LOW/info items, none rising to HIGH. The bughunt-6 classifications hold. No silent fixes between v0.45.1 and v0.49.0 that weren't accounted for in the per-version commit messages.

**Unfixable:** store-eval F2 (Go 1.21 toolchain) — documented as won't-fix in v0.49.0 commit; README states Go 1.25+ requirement.

For the maintainer's "drive HIGH/CRIT count to zero" goal, addressing the three promotion candidates in a Round 7 ship is the lowest-risk path: each is mechanical (no schema change, no API change, no parser rewrite) and each is independently testable.
