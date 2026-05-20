# Leonard — Bug Hunt #4 Triage

> Five parallel lanes audited the v0.7.1–v0.12.0 fix-round work plus
> carry-over MEDIUMs from rounds 1–3. ~70 findings total. This doc
> picks the fix-round-4 priorities.

## Decision

**Fix every HIGH-severity finding (4 total) plus the cap-completeness
sweep (Theme A) and the path-trust-completeness sweep (Theme B). Defer
everything else.**

Reasoning: the HIGHs found this round are all real "Leonard
misbehaves under one bad input" failures with concrete reproducers —
none of them require an attacker, just a confused or unlucky Claude
session. The v0.9.0 cap work was incomplete in specific places
(supersede, CLI decisions add, indexed-file size, fail-open at cap
boundaries) — finishing it is small mechanical effort with real
payoff. v0.8.0's path-trust likewise covered the post-edit + indexer
surfaces but missed pre-edit and prune; the same `ResolveSafe`
helper just needs to be plumbed to two more call sites.

The most surprising HIGH: bughunt-3 security F12 (Scanner oversize-
line drops the connection) had been "fixed" in v0.9.0 with a
deliberately-no-op stub function. Round 4 demonstrates the stub is
**worse than the original bug**: an oversize line causes a busy-spin
loop pegging CPU at 100% and writing 35 MB/s of stderr forever.

## Cross-cutting themes

### Theme A — Resource-cap completeness (security)

The v0.9 cap work plugged the most-obvious sites but left holes:

| ID | What | Severity |
|---|---|---|
| caps F1 / mcp F1 | bufio.Scanner oversize-line busy-spins CPU + spams stderr | **high** |
| mcp F2 | `get_decisions(limit=200)` returns up to 7.4 MB of structured content; no per-response cap | **high** |
| mcp F3 | SessionStart `additionalContext` un-truncated; can inject 360 KiB per session | **high** |
| caps F2 | `supersede_decision` bypasses every decision text cap | medium |
| caps F3 | `leonard decisions add` CLI bypasses caps (bughunt-2 security F8 still alive) | medium |
| caps F4 | Indexed file size uncapped — 172 MB file → 480 MB RSS | medium |
| caps F5 | Oversize snippet → `capSnippets` silently zeros it; pre-edit guard fails open | medium |
| caps F6 | MultiEdit > 100 elements → fabricated symbol at position 150 passes through | medium |
| caps F7 | `list_files` has no limit field at all | medium |
| caps F9 | `get_unverified_claims` has no limit field either | medium |

**Sweep:** the cleanest design is a single utility (`internal/store/limits.go`
or extend `internal/hooks/limits.go`) that defines all caps in one place +
applies them at every write point. Specific fixes:

- Replace `newOversizeTolerantScanner` no-op stub with a real
  line-reader that can resync after a too-long line — write our own
  reader instead of bufio.Scanner.
- Add `MaxDecisionsResponseBytes` / `MaxClaimsResponseBytes` and
  truncate when assembling responses.
- Add per-bullet `TruncateForInjection` in `formatDecisions`
  (mirroring the 120-rune cap that `stop.go` already uses).
- Add cap checks to `supersede_decision` and to `realRuntime.RecordDecision` / `realRuntime.RecordClaim` CLI paths.
- Cap indexed file size with `maxIndexedFileBytes`; reject before
  reading.
- Change `capSnippets` from "silently zero" to "reject the whole
  hook with ErrDecode" so the fabrication guard's fail-open path
  is closed.

### Theme B — Path-trust completeness (security)

v0.8.0's `ResolveSafe` covered IndexFile + post-edit. Round 4 found
the unguarded siblings:

| ID | What | Severity |
|---|---|---|
| path-trust F1 | Pre-edit hook reads `payload.ToolInput.FilePath` and passes to `readFileImports` → `parser.ParseFile` without ResolveSafe. File-existence oracle at minimum. | medium |
| path-trust F2 | Dangling symlinks pass ResolveSafe — when EvalSymlinks errors, the resolved-vs-resolved check is skipped and the lexical-pass branch returns ok | medium |
| path-trust F3 | Unicode NFC vs NFD → two separate file rows for the same on-disk file | medium |
| path-trust F4 | `pruneStaleFiles` and `doctor.StaleFiles` use direct `filepath.Join` — pre-v0.8 polluted rows with `..` survive prune | medium |

**Sweep:** plumb ResolveSafe into pre-edit's `decidePreEdit`. Fix
F2 by treating EvalSymlinks-errors-with-dangling-symlinks as an
explicit reject. Add NFC normalization at the storeKey boundary
(F3). Add ResolveSafe to pruneStaleFiles + doctor (F4).

### Theme C — Documentation reality-gap

| ID | What | Severity |
|---|---|---|
| integration F1 | README at "v0.1", gpython, missing v0.7–v0.12; binary stamps v0.12.0 | medium |
| integration F4 | OTel scope mismatch — README plural ("the binaries") but only `leonard-hook` instrumented | medium |
| integration F8 | No CI workflow, no git tags, no CHANGELOG, no CONTRIBUTING | informational |

**Sweep:** single docs PR. Doesn't block any user-facing behavior;
flag separately from the security/correctness fixes so it doesn't
delay them. Round-3 already noted this; round 4 confirms no
progress in the meantime.

## Lane summaries

### Lane A — `path-trust-deep` (9 findings, file `bughunt-4-path-trust-deep.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Pre-edit hook bypasses ResolveSafe entirely | medium |
| F2 | Dangling symlinks pass ResolveSafe | medium |
| F3 | Unicode NFC/NFD → duplicate file rows | medium |
| F4 | pruneStaleFiles / doctor use raw filepath.Join | medium |
| F5–F9 | TOCTOU (informational), NUL bytes, backslash paths, related_files unvalidated | low / informational |

**Solid:** lexical `..` escape rejection, absolute-outside rejection,
direct symlink-out rejection, `/var → /private/var` resolved-
fallback, prefix-confusion correctness, empty inputs, IndexAll +
readSiblingPackages symlink skips, post-edit `handleEscapedPath`
end-to-end (Leonard rejected the agent's own probe files mid-
investigation — confirmation by self-test).

### Lane B — `caps-and-limits` (17 findings, file `bughunt-4-caps-and-limits.md`)

Includes the headline F1 HIGH (Scanner infinite-loop) plus 6
MEDIUMs covering specific cap-bypass sites + the response-size
gap.

**Solid:** the four documented hook payload caps fire correctly
with clear errors + correct exit-2 mapping. MCP `record_decision`,
`record_claim`, `recent_changes`, `get_decisions`, `get_stale_decisions`
all enforce their documented caps + clamp `limit`.

### Lane C — `store-perf` (11 findings, file `bughunt-4-store-perf.md`)

| ID | What | Severity |
|---|---|---|
| F1 | `ListFilesIndexedSince` (powers `recent_changes`) full-scans files | medium |
| F2 | `GetUnverifiedClaims` full-scans claims when no session_id supplied | medium |
| F4 | `GetStaleDecisions` is N+1 — one round-trip per related-files/symbols ref × up to 200 decisions | medium |
| F7 | `.leonard.db-wal` grows unbounded — no `wal_checkpoint` ever called | medium |
| F3, F5, F6, F8–F11 | smaller issues — missing indexes, dead `idx_claims_vet_ok`, etc. | low / informational |

**Solid:** v0.7.1's `idx_symbols_parent` win is real and stable
(bench still at ~42 ms/op for 1k files). Concurrent reads behave
correctly under WAL.

### Lane D — `rust-round-2` (14 findings, file `bughunt-4-rust-round-2.md`)

| ID | What | Severity |
|---|---|---|
| F3 | cfg-gated method dupes; no UNIQUE constraint in symbols table | medium |
| F8 | Memory ~144× source size — 4.68 MB → 673 MB RSS | medium |
| F1, F2, F4–F7, F9–F14 | impl_target_name drops generic args / mutability / `::`; synthetic placeholders collide; pub use / trait decls / impl consts still dropped; union types unhandled; etc. | low / informational |

**Solid:** all three v0.12.0 fixes verified working on the original
reproducers + additional edge cases. No regressions. Dogfooded on
ripgrep + serde (5 known traits spot-checked, all found at correct
start_line).

### Lane E — `integration` (16 findings, file `bughunt-4-integration.md`)

| ID | What | Severity |
|---|---|---|
| F1 | DESIGN.md + README drift compounded by v0.7–v0.12 (11 specific drifts) | medium |
| F2 | `go mod tidy` not idempotent (bughunt-3 F2 still open) | medium |
| F3 | Bughunt-2 deferred MEDIUMs still present (cli F9, cli F18, pre-edit F1) | medium |
| F4 | `-tags otel` only instruments `leonard-hook` | medium |
| F5–F16 | smaller drifts, no CI/tags/CHANGELOG, Makefile bitrot, etc. | low / informational |

**Solid:** `go test ./...`, `go test -tags otel ./...`, `go test
-race ./...` all pass. v0.6 → v0.12 DB migration works in a manual
smoke test. v0.8 path-trust doesn't reject legitimate paths.
50 concurrent post-edit hooks: 50 claims, zero lock errors.
Default binary has zero OTel symbols.

### Lane F — `mcp-and-hooks` (17 findings, file `bughunt-4-mcp-and-hooks.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Scanner oversize-line still kills MCP session (rediscovery of caps F1 from MCP angle) | **high** |
| F2 | `get_decisions(limit=200)` × 32 KiB reasoning = 7.4 MB response | **high** |
| F3 | SessionStart `additionalContext` un-truncated — 360 KiB inject worst case | **high** |
| F4 | MCP-recorded claims cannot be superseded (no `file_path` input field) | medium |
| F5 | `handleMissingFile` doesn't emit `additionalContext` so the model can't see the no-op | medium |
| F7 | `record_decision.related_files` accepts `/etc/hosts` | medium |
| F13 | Oversize-line CPU spin downstream of F1 | medium |
| F6, F8–F12, F14–F17 | Smaller items — schema doc gaps, transport-error inconsistencies, etc. | low / informational |

**Solid:** Cap rejection shape, cap enforcement under concurrency
(5184 + 154 ops, 0 errors), stdin filter for normal cases,
SessionStart compact/clear skip, stale-decision lifecycle, post-edit
additionalContext naturally bounded, eval-framework self-check not
exploitable.

## What ships in fix-round-4

In priority order:

1. **Theme A — caps completeness** (HIGH × 3 + 7 MED). Bundle into
   one PR. Biggest single security payoff.
2. **Theme B — path-trust completeness** (4 MED). Adds 4 more
   ResolveSafe call sites + the dangling-symlink + NFC fixes.
3. **store-perf F1+F2+F4+F7** — missing indexes + N+1 fix + WAL
   checkpointing. Real workload payoff.
4. **mcp F4+F5** — MCP claim supersession + handleMissingFile
   additionalContext.
5. **Carry-over from bughunt-2/3**: cli F9 (walk-up to find
   `.leonard/`), cli F18 (doctor double-count), pre-edit F1
   (sibling-scan skip list).
6. **Theme C — documentation drift sweep**.

## What's deferred

- **Rust extractor coverage gaps** (F4–F7, F12–F13, F8 memory) —
  consistent with the shallow v0 contract. Promote when a real
  Rust user surfaces the pain.
- **Telemetry expansion** (otel F4 — leonard-mcp + leonard CLI
  not instrumented) — not a correctness bug.
- **CI / tags / CHANGELOG** — public-release infra, tracked
  separately.
- **Eval framework polish** (eval F3 regex, F4 brittle parser) —
  works on the mockllm path; live run unblocked.

## What stays out of scope

- LOW + INFORMATIONAL items (catalogued in lane files for completeness).
- New tools / new hooks / new languages.
- The Inspect eval live run — same blocker as before
  (ANTHROPIC_API_KEY).

If a fix lane finds a new HIGH while implementing, surface and
decide on the spot. Don't extend scope silently.
