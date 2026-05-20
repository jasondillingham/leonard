# Changelog

All notable changes to Leonard, in reverse chronological order.

Cadence: each minor bump bundles one coherent change (a feature, a
bug-hunt theme fix, or a perf sweep) and ships with an updated
`leonard-mcp --version` string + test coverage.

## v0.18.0 — Documentation reality-gap sweep (bughunt-4 Theme C)

- README now reflects v0.17+ reality: version stamp, language table,
  component list, project layout, OTel scope (hook-only).
- Added `CHANGELOG.md` synthesized from commit history.

## v0.17.0 — Carry-over bughunt-2 MEDIUMs

- **cli F9**: `leonard verify` and other subcommands now walk up
  from cwd looking for `.leonard/` instead of failing in subdirs.
- **cli F18**: `leonard doctor` no longer double-counts stale files
  as both parse-failure suspects and stale rows.
- **pre-edit F1**: sibling-scan skip list now uses the exported
  `index.DefaultSkipDir` so it stays in lockstep with the
  indexer's `defaultSkipDirs`.

## v0.16.0 — MCP claim supersession + missing-file additionalContext

- **mcp F4**: `record_claim` now accepts `file_path`, so MCP-
  recorded unverified claims can be superseded by a later vet=ok
  post-edit hook on the same file.
- **mcp F5**: `handleMissingFile` (post-edit short-circuit on a
  missing file) now emits `HookSpecificOutput.AdditionalContext`
  so the model sees the no-op signal.

## v0.15.0 — Store performance

- migrateV6 adds `idx_files_indexed_at`, `idx_claims_verified`, and
  `idx_decisions_recorded_at` (all found missing via EXPLAIN QUERY
  PLAN).
- `GetStaleDecisions` N+1 fix: collect every related-files/symbols
  ref upfront, run two chunked IN-clause queries, then do in-memory
  membership checks instead of 2000+ per-call round-trips.
- `DeleteFiles` triggers `PRAGMA wal_checkpoint(PASSIVE)` after
  bulk commits (≥100 rows) so `.leonard.db-wal` stays bounded.

## v0.14.0 — Path-trust completeness

- Pre-edit hook now routes `file_path` through `ResolveSafe` before
  any `parser.ParseFile` (was a file-existence oracle).
- Dangling symlinks now rejected by `ResolveSafe` (the lexical-pass
  branch used to accept when `EvalSymlinks` errored).
- `storeKey` applies Unicode NFC normalization so the same on-disk
  file produces one row regardless of NFC vs NFD input.
- `pruneStaleFiles` and `doctor.StaleFiles` filter rows through
  `ResolveSafe` before stat.

## v0.13.0 — Cap completeness

- Replaced `bufio.Scanner` in `leonard-mcp` stdin filter with a
  custom line reader that resyncs past oversize lines. The v0.9
  "fix" was a documented no-op stub that caused a busy-spin DoS
  (100% CPU, 35 MB/s of stderr) — verified.
- `get_decisions` and `get_unverified_claims` cap aggregate response
  size at 1 MiB.
- `SessionStart` decision bullets truncated per-field so worst-case
  inject drops from 360 KiB to ~2.4 KiB.
- `supersede_decision` validates `new_choice` + `new_reasoning`.
- `leonard decisions add` CLI now validates topic/choice/reasoning
  (was bypassing the MCP-layer cap).
- `maxIndexedFileBytes` (8 MiB) — indexer rejects + records
  ParseFailure instead of reading multi-megabyte files.
- Oversize snippets and over-count MultiEdits now reject the hook
  with `ErrDecode` instead of silently truncating.
- `list_files` and `get_unverified_claims` gained `limit` fields.
- Shared cap constants in `internal/store/limits.go` so MCP and
  CLI layers reference one source of truth.

## v0.12.0 — Rust extractor correctness

- `impl_target_name` covers non-Path self_ty (Type::Reference,
  Tuple, Array, Slice) so `impl Display for &Foo` no longer
  silently drops its methods.
- `start_line` skips attributes + doc comments (matches Python/TS).
- Multi-segment `impl Display for std::collections::HashMap`
  preserves the full path so foreign-type qnames don't collide
  with local types.

## v0.11.0 — OTel lifecycle fixes

- `os.Exit()` no longer skips deferred telemetry shutdown.
- Fresh `context.Background()` used for shutdown (signal-cancelled
  ctx was aborting the flush).
- Explicit 5s `sdktrace.WithExportTimeout` so an unreachable OTLP
  endpoint doesn't block the hook for 30 seconds.

## v0.10.0 — Eval framework readiness

- `scoring.py` subprocess sets `cwd=project_root` (was silently
  falling back to permissive store outside the repo).
- `mcp>=1.0` added to `pyproject.toml`.
- `GO_BLOCK_RE` accepts `golang`/uppercase/no-trailing-newline
  fence variants.
- `_run_self_check` probes the hook at first scoring call so a
  drifted deny-wording raises loudly rather than silently zeroing
  fabrication counts.
- Two samples rewritten to use package-qualified function refs
  the pre-edit guard can actually validate.

## v0.9.0 — Resource-cap hygiene

- `MaxHookPayloadBytes` (16 MiB), `MaxSnippetBytes` (1 MiB),
  `MaxMultiEditElements` (100) — caps at the hook decode boundary.
- Decision and claim text caps in `record_decision` /
  `record_claim`.
- Earlier (buggy) `newOversizeTolerantScanner` stub for the MCP
  Scanner overflow — superseded by v0.13.

## v0.8.0 — Path-trust sweep

- `ResolveSafe(root, claimed)` validates external-caller-supplied
  paths against the project root; rejects `/etc/hosts`-style
  absolutes and `../escape` traversals.
- IndexFile, IndexAll's walker (symlink-skip), pre-edit's
  sibling-scan walker, post-edit's `handleEscapedPath` all wired
  through.

## v0.7.1 — `idx_symbols_parent`

- migrateV5 adds the missing index on `symbols.parent_id`. The
  v0.7.0 commit message attributed the prune-sweep bench cost to
  WAL fsync; bughunt-3 traced it to this unindexed self-
  referential FK. `BenchmarkDeleteFiles_1k` drops from ~5.8s to
  ~42ms (~137×).

## v0.7.0 — Batched `Store.DeleteFiles`

- Replaces per-row delete loop with single-tx chunked IN clauses.
- Indexer's `pruneStaleFiles` collects-then-batches.
- ~200× speedup over the v0.6.1 per-row baseline on polluted-index
  cleanup.

## v0.6.1 — `defaultSkipDirs` ecosystem coverage

- Skip-dir map grown from 5 to 15 entries: `.venv`, `venv`,
  `__pycache__`, `target`, `.mypy_cache`, `.next`, `.nuxt`,
  `.pytest_cache`, `.ruff_cache`, `.tox`, etc.
- `pruneStaleFiles` removes existing rows whose path traverses a
  skip-dir component (upgrade path-cleanup).

## v0.6.0 — Optional OpenTelemetry

- `-tags otel` build pulls in the OTel SDK and reads `OTEL_*`
  env vars; default build has zero overhead (no-op stubs, deps
  not linked).
- Spans on `leonard.pre-edit`, `leonard.pre-edit.sibling-scan`,
  `leonard.post-edit`, `leonard.post-edit.index`,
  `leonard.post-edit.vet`.

## v0.5.0 — Rust parser

- `internal/parse/rust/` Cargo crate builds
  `leonard-extract-rust`, a syn-based extractor producing JSON.
- Go-side wrapper has same shape as Python: env override
  (`LEONARD_RUST_EXTRACTOR`), context timeout, source-tree
  fallback.
- Dogfooded against ripgrep: 100 / 100 files, 2,678 symbols.

## v0.4.0 — Anthropic Inspect eval framework

- `evals/inspect/` adds three Tasks (control, treated, treated
  with system prompt) measuring fabrication rate with vs. without
  Leonard's MCP tools.
- Scoring pipes the model's `go` block through `leonard-hook
  pre-edit` and counts blocked references.
- Live runs blocked on `ANTHROPIC_API_KEY`; mockllm path works.

## v0.3.0 — Pydantic AI demo

- `examples/pydantic-ai/demo.py` wires `MCPToolset(StdioTransport)`
  to the production leonard-mcp binary; shows verify_symbol +
  find_symbol round-trip.

## v0.2 — Python parser swap + CLI subcommands + doctor + benches

- Python parser switched from `gpython` to host `python3` via
  subprocess (full modern Python support, ~40ms parse cost).
- `leonard decisions {add,list}` and `leonard claims {list}` CLI
  surfaces.
- `leonard doctor` project-health report.
- Hook-latency benchmarks.

## v0.1 — Initial dogfoodable release

Three binaries, SQLite store, MCP tool surface, four Claude Code
hook handlers.
