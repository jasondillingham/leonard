# Leonard — Bug Hunt #3 + Security Review #1 Triage

> Six parallel investigations covered v0.5–v0.7 surfaces (Rust parser,
> skip-dirs+prune, OTel, eval framework, integration) plus the first
> focused security review. ~80 findings total. This doc decides what
> ships in fix-round-3.

## Decision

**Fix every HIGH-severity finding (3 bughunt + 2 security) plus the
"unsafe by default" cluster from the security review.** Defer all
MEDIUMs that are correctness-or-perf concerns without an exploitable
edge.

Reasoning: round 3 surfaced fewer total findings than round 2 (~80 vs
~70) but a much higher fraction of them are real safety or
correctness issues. The HIGHs are not optional:

- A polluted index from a confused-deputy path traversal silently
  invalidates Leonard's whole "ground truth" contract (security F1)
- A pre-edit DoS via memory amplification breaks the daily-use story
  (security F2; 50× RSS on a 143 MB payload → ~7 GB)
- The Inspect eval framework can't produce signal as shipped (eval
  F1+F2 — scoring.py runs in the wrong cwd, MCP dep missing from
  pyproject) — these block the project's claimed-but-unrun receipt
- The headline `DeleteFiles` perf number is wrong: the bottleneck
  isn't WAL fsync (the commit message claim), it's an unindexed
  `symbols.parent_id` column (skip-dirs F1, ~180× speedup available
  with a single `CREATE INDEX`)

## Cross-cutting themes

### Theme A — Path-trust hygiene (security)

Three closely-related findings reduce to "the indexer + pre-edit
hook follow file paths supplied by an external caller without
checking they live inside the project root":

| ID | What | Severity |
|---|---|---|
| security F1 | Post-edit `file_path` accepts absolute paths and `../` traversal; indexes files outside the project root with no warning | **high** |
| security F3 | `IndexAll` follows symlinks pointing out of the project root | medium |
| security F11 | `go vet` inherits the parent process's full environment in whatever dir CWD says | informational |

**Sweep:** add a single `resolveSafe(root, claimed)` helper that
joins, cleans, then verifies the result still has `root` as a
prefix (`filepath.Rel` + `..` check). Call it from `IndexFile`,
`pruneStaleFiles`'s stat, `handleMissingFile`, the sibling-scan
walker, and the post-edit hook before vet. Add `filepath.WalkDir`
options or a wrapper that doesn't follow symlinks (use
`fs.WalkDirFunc` + `d.Type()` check instead of `os.Stat`). One
PR.

### Theme B — Resource-cap hygiene (security)

Multiple inputs have no upper bound:

| ID | What | Severity |
|---|---|---|
| security F2 | `new_string` size; 143 MB → 7 GB RSS via repeated `parseSnippet` attempts | **high** |
| security F4 | Decision / claim text + indexed-file size | medium |
| security F8 | Same at the CLI surface | low |
| security F9 | MultiEdit element count unbounded | low |
| security F12 | leonard-mcp's 16 MiB Scanner ceiling truncates rather than returns an error | low |

**Sweep:** introduce package-level constants `maxSnippetBytes` (try
1 MiB; vast majority of real-world `new_string` payloads are well
under 100 KB), `maxDecisionTextBytes`, `maxClaimTextBytes`. Reject
oversize inputs at the decode boundary with a clear error message.
For the Scanner ceiling: emit a Parse-error JSON-RPC frame and
keep the connection alive instead of dropping.

### Theme C — Eval framework "first live run" blockers

The eval cannot produce meaningful numbers until:

| ID | What | Severity |
|---|---|---|
| eval F1 | scoring.py needs explicit `cwd=` for the hook subprocess; today the scorer's pre-edit call falls back to `permissiveStore` (every snippet scores 1.0 silently) when run from outside the repo | **high** |
| eval F2 | `mcp` Python package is missing from `pyproject.toml`; treated arms fail at startup with "MCP tools requires optional dependencies" | **high** |
| eval F4 | Brittle reason-string parser in scoring.py; if pre-edit's English message ever changes, scorer silently reports zero fabrications | medium |
| eval F5 | Pre-edit approves `pkg.MethodName` package-qualified method references that are syntactically invalid Go; two samples ask the model to produce exactly this form | medium |
| eval F6 | The `hook-fabrication-scan` trap is inverted; honest refusal scores 0.0 | medium |

**Sweep:** small targeted PR — F1 + F2 mechanical, F4 by making
scoring.py call `find_symbol` on each reference instead of parsing
the deny message, F5+F6 by rewriting the affected samples to
elicit only the form Leonard's guard actually checks. Then the
eval can be run.

## Lane summaries

### Lane A — `rust` (17 findings, file `bughunt-3-rust.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Methods on non-`Type::Path` self_ty (refs, tuples, arrays) silently dropped | medium |
| F2 | `start_line` includes attributes/doc-comments; Python/TS strip them | medium |
| F3 | `impl Display for std::fmt::Foo` collides with local-type qnames | medium |
| F8 | No version handshake; stale helper binary used silently after a Rust source edit | medium |
| F4–F7, F9–F17 | cfg-gated duplicates, `pub use` invisible, trait method decls dropped, associated consts dropped, etc. | low / informational |

**Solid:** survives 500-goroutine concurrent fan-out, 22 MB inputs,
Unicode identifiers, BOM/CRLF round-trip; clap-rs (330 files, 3,676
symbols) indexes cleanly.

### Lane B — `skip-dirs-prune` (10 findings, file `bughunt-3-skip-dirs-prune.md`)

| ID | What | Severity |
|---|---|---|
| F1 | `BenchmarkDeleteFiles_1k` cost is unindexed `symbols.parent_id`, NOT WAL fsync; adding `idx_symbols_parent` drops bench from ~6s → ~38ms (~180×) | **high** |
| F2 | Case-sensitive skip-dir match misses `VENDOR/`, `Target/` on macOS APFS | medium |
| F3 | User-named `cmd/build/main.go` silently dropped, no override knob | medium |
| F4 | Files relocated INTO a skip-dir disappear, no warning | medium |
| F5–F10 | Chunk size 65× too conservative, misleading total on partial-rollback, root-only `.leonardignore`, etc. | low / informational |

**Solid:** matcher correct against substring false-positives
(`target-lang`, `venvironment`), forward-slash invariant holds,
walker doesn't follow symlinks, chunk-boundary math correct, weird
filenames safe.

### Lane C — `otel` (10 findings, file `bughunt-3-otel.md`)

| ID | What | Severity |
|---|---|---|
| F1 | `os.Exit()` in main.go skips deferred shutdown; every non-zero hook exit drops queued spans | medium |
| F2 | SIGINT cancels the same ctx then passed to `shutdown(ctx)`; cancelled ctx aborts flush | medium |
| F3 | Unreachable OTLP endpoint blocks hook for 30 s (batch processor's default export timeout) | medium |
| F4 | leonard-mcp and `leonard` CLI are byte-identical with vs. without `-tags otel`; only leonard-hook is instrumented | low |
| F5–F10 | `OTEL_SERVICE_NAME` overridden, gRPC unsupported, ctx-discard at child sites, no per-snippet visibility, etc. | low / informational |

**Solid:** no-op truly zero-cost (0.97 ns/op, 0 allocs); OTel deps
fully absent from default binary (`go tool nm` confirms); span tree
nests correctly under tag; no goroutine leaks after Shutdown.

### Lane D — `eval-framework` (14 findings, file `bughunt-3-eval-framework.md`)

| ID | What | Severity |
|---|---|---|
| F1 | scoring.py subprocess missing `cwd=`; outside-repo invocations fall through to permissive store, every snippet scores 1.0 silently | **high** |
| F2 | `mcp` Python dep missing from pyproject.toml; treated arms fail at startup | **high** |
| F3 | `extract_go_code` regex misses `golang`, uppercase, ` ``` go ` (with space), no-trailing-newline forms | medium |
| F4 | Brittle reason-string parser breaks silently if hook wording changes | medium |
| F5 | Pre-edit approves package-qualified method references that are invalid Go | medium |
| F6 | `hook-fabrication-scan` trap inverted (honest refusal scores 0.0) | medium |
| F7–F14 | Binary scoring, temperature unpinned, README cost wrong by 5×, dry-run path discoverable via mockllm, etc. | low / informational |

**Solid:** `inspect list tasks` registers all 3, sample targets
resolve in current code, hook latency well under 30 s timeout,
MCP server cwd correctly threaded.

### Lane E — `integration` (12 findings, file `bughunt-3-integration.md`)

| ID | What | Severity |
|---|---|---|
| F1 | DESIGN.md drift: 11 specific places where v0.3–v0.7 contradicts the design doc | medium |
| F2 | `go mod tidy` wants to promote 4 OTel deps from indirect to direct | medium |
| F4 | `-tags otel` only wires `leonard-hook`; the other two binaries byte-identical | medium |
| F5 | Bughunt-2 deferred MEDIUMs still present (cli F9, cli F18, pre-edit F1) | medium |
| F9 | No subprocess tests, no migration tests, no OTel end-to-end span name test | medium |
| F3, F6–F8, F10–F11 | Makefile bitrot, `examples/`/`evals/` invisible from README, no CI/tags/CHANGELOG, OTel binary +15 MB undocumented | low / informational |
| F12 | Pure-Go binary story holds (positive) | informational |

**Solid:** `go test ./...`, `go test -tags otel ./...`, `go test
-race ./...` all clean; 50 concurrent post-edit hooks + 2 concurrent
`leonard index` runs against the same DB — zero locked errors;
`CGO_ENABLED=0 go install ./cmd/...` works.

### Lane F — `security` (18 findings, file `security-1-review.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Post-edit accepts `/etc/hosts`-style `file_path`; indexer follows and stores foreign symbols as project content | **high** |
| F2 | Pre-edit DoS: 143 MB payload → ~7 GB RSS (50× amp) | **high** |
| F3 | `IndexAll` follows symlinks pointing out of project root | medium |
| F4 | No size caps on decision/claim text or indexed-file size | medium |
| F5 | `additionalContext` passes user-controlled text verbatim to Claude (prompt-injection vector) | medium |
| F6 | `LEONARD_PYTHON`/`LEONARD_RUST_EXTRACTOR` enable PATH shimming | low |
| F7 | `.leonard/` is `0o755`, files `0o644`; should be `0o700`/`0o600` for a single-user tool | low |
| F8 | CLI `decisions add` doesn't cap arg sizes either | low |
| F9 | MultiEdit per-element caps mitigate F2 but edit-count is unbounded | low |
| F10–F12 | stderr leaks paths, `go vet` inherits full env, Scanner cap closes connection instead of erroring | informational |
| F13 | `recent_changes` returns paths verbatim including any escaped paths from F1 | informational |
| F14–F18 | Confirmed-safe items: no HTTP listen, all SQL parameterized, MCP SDK validates inputs, subprocesses use argv form (no shell), Python/Rust parsers are parse-only (no execution), OTel spans carry no user data | informational |

## What ships in fix-round-3

In priority order:

1. **security F1 + F3 (Theme A) — path-trust sweep.** Single PR adding
   `resolveSafe` + symlink-no-follow + call sites. Closes the worst
   confused-deputy. **Highest-priority.**
2. **security F2 + Theme B — resource caps.** Single PR adding
   `maxSnippetBytes`/`maxDecisionTextBytes`/etc. Stops the memory
   amplification. Documents the limits.
3. **skip-dirs F1 — `idx_symbols_parent` index.** One-line schema
   migration. ~180× perf on prune. Trivial fix, huge payoff.
4. **eval F1 + F2 + F4 + F5 + F6 (Theme C) — eval framework readiness.**
   Bundle into one PR so a live eval run becomes possible.
5. **otel F1 + F2 + F3 — telemetry-lifecycle fixes.** main.go's
   `os.Exit()` + SIGINT-ctx mistake + OTLP timeout block.
6. **rust F1 + F2 + F3 — Rust correctness.** Non-Path self_ty,
   start_line attribute inclusion, foreign-type collision.

## What's deferred

Documented per-finding in each lane file. Notable deferred clusters:

- **Carry-over from bughunt-2**: cli F9 (CLI doesn't walk up to
  find `.leonard/`), cli F18 (doctor double-counts stale+parse-fail),
  pre-edit F1 (sibling-scan skip list diverges from indexer's
  defaultSkipDirs). Round 3 confirms all three are still present;
  none break a contract today.
- **Skip-dirs UX**: case-sensitivity (F2), no escape hatch (F3, F7,
  F8). Worth a dedicated lane in a future round.
- **Rust coverage gaps**: F4–F7 (cfg-gated duplicates, `pub use`,
  trait method bodies, associated consts) — all consistent with the
  documented shallow contract; document rather than fix.
- **OTel polish**: F4–F10 (leonard-mcp not instrumented, env-var
  mismatches, no per-snippet spans) — incremental; ship after the
  lifecycle fixes.
- **Documentation drift** (integration F1, F10, F11) — needs a docs
  sweep PR but doesn't block any user-facing behavior.

## What stays explicitly out of scope

- LOW + INFORMATIONAL findings (catalogued for completeness).
- New language extractors (DESIGN.md §6 future work).
- CI / release infrastructure — covered by the "public-release
  readiness" plan as a separate workstream.
- Eval framework refinements beyond the live-run blockers (binary
  scoring, temperature, README cost numbers — fix later).

If a fix-round lane discovers a new HIGH while implementing, surface
it and decide on the spot — same rule as round 1/2.
