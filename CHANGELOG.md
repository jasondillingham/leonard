# Changelog

All notable changes to Leonard, in reverse chronological order.

Cadence: each minor bump bundles one coherent change (a feature, a
bug-hunt theme fix, or a perf sweep) and ships with updated version
strings (`leonard --version`, `leonard-hook --version`,
`leonard-mcp --version`) + test coverage.

## v0.48.0 — Carry-over promotions + supply-chain fixes (bughunt-6 Theme D, partial)

Three of the seven items from Theme D. The bigger refactors (otel
F4 binary instrumentation; perf F4 indexer worker pool; perf F6
WAL checkpoint cadence) are deferred to a future round — they're
meaningful changes that need design work, not load-bearing for
launch.

- **languages F1 (PROMOTED to HIGH)**: tree-sitter `module_name`
  rewrite. The v0.19 basename-only form silently collided across
  same-name files in different dirs — `src/foo.rs` and `lib/foo.rs`
  both produced module "foo" for 29 tree-sitter languages,
  breaking the index's identity contract. The fix mirrors the
  Go-side `internal/parse/qname.go::moduleQualifier`:
  path-separator-to-dot + extension strip. So `src/foo.rs` →
  `src.foo`, `lib/parse/qname.go` → `lib.parse.qname`.
- **security-2 F3**: Cargo.lock now committed for both
  `internal/parse/rust/` and `internal/parse/treesitter/`. The
  v0.19 .gitignore excluded them, so every fresh tree-sitter
  build resolved grammars without lockfile pins — a hijacked
  point-release of any of the 28 grammars would have run its
  build.rs on the CI runner. With the lockfiles in tree, CI's
  cargo build resolves deterministically.
- **store-eval F6**: NFC normalization gap in claim writes. The
  indexer's `storeKey` normalized file paths to NFC, but
  `RecordClaim` accepted whatever path Claude Code's hook
  envelope emitted. A claim recorded with an NFD path failed to
  match the file row (NFC) on `SupersedeClaimsForFile`. Added
  `normalizeClaimPath` and called it on both write and supersede
  paths.

Deferred to a future round (still in the carry-over backlog):
- otel F4 (leonard-mcp + leonard CLI byte-identical with/without
  -tags otel — instrument the hot paths in both binaries)
- perf F4 (indexer worker pool, 42× speedup)
- perf F6 (WAL checkpoint cadence on long-running processes)
- store-eval F10 (migrateV7 LIKE-text matching could in principle
  match a legit MCP claim; not seen in practice, low risk)
- mcp F3 (silent truncation in get_unverified_claims response —
  no `truncated` flag yet)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.47.0 — MCP correctness (bughunt-6 Theme C)

Two HIGH findings from the bughunt-6 mcp-and-hooks-deep audit.

- **mcp F1**: `languageFromPath` only knew 5 languages (Go,
  Python, TypeScript/TSX, JavaScript/JSX). Every other extension
  returned "" — `verify_symbol(language="rust")` filtered to zero
  matches for the 22+ tree-sitter languages added since v0.1.
  Rewrote the mapping to cover all 39 registered languages plus
  the basename-dispatched cases (Makefile, BUILD, CMakeLists.txt,
  package.json, Cargo.toml, go.mod, pom.xml). Verified end-to-end:
  a Rust file now resolves through `leonard verify hello`
  correctly.

- **mcp F2**: `verify_symbol` and `find_symbol` had no MCP-layer
  ceiling and no SQL LIMIT. A caller passing `limit=10000000`
  could materialize the entire symbol table before any cap
  applied. Added `MaxSymbolResults = 500` and clamp the user-
  provided limit to min(limit, 500). 500 is well above every
  real Claude-Code consumer (the reasoning loop is bounded by
  its own context budget).

mcp F3 (silent truncation in get_unverified_claims) deferred to
v0.48.0 along with the carry-over promotions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.46.1 — Launch-surface accuracy sweep (bughunt-6 Theme B)

Pure docs/config polish closing 13 launch-surface findings from
bughunt-6 + security-2. Zero behavior changes.

- **launch F2**: status line bumped v0.45.0 → v0.46.1.
- **launch F3**: language-count consistency. Was "27+" / "Twenty-six" / 28 table rows / 39 in source. Now: 29 tree-sitter + 4 production + 3 SFC + 2 structured-file + 4 manifest formats, with SQL moved from "structured-file inspectors" to "tree-sitter languages" (it's tree-sitter-sequel, not bespoke).
- **launch F4**: bughunt-1 HIGH count corrected (4 → 8) in the README narrative.
- **launch F5**: bughunt-3 HIGH count corrected (6 → 3); Security #1's 2 HIGHs already had their own row.
- **launch F6**: `audits/README.md` rewritten — 9 wrong filenames replaced; 3 missing files added; round-6 entries added.
- **launch F7**: `examples/pydantic-ai/README.md` line numbers corrected (`Open` 42→103, `IndexAll` 104→274).
- **launch F8**: v0.37.0 CHANGELOG attribution fixed — the Purser/Leonard PR was filed by the maintainer themselves while scaffolding Purser, not by an outside contributor.
- **launch F9**: SECURITY.md "MCP stdin filter" row corrected v0.6 → v0.13.
- **launch F10**: SECURITY.md "email the maintainer" line dropped; PVR is the canonical disclosure path.
- **launch F14**: README Install section now states Rust toolchain requirement explicitly, plus compile times and `target/` cache footprint.
- N3: stripped version-stamp parentheticals from language tables.

Round-6 + Security-#2 entry added to the README "Bug-hunt discipline"
table including the v0.46.0 CRITICAL closure.

Still pending the user (not code-fixable):
- B1: flip repo visibility to public.
- launch F1: GitHub Actions billing/spending-limit (CI badge red).

## v0.45.1 — Launch polish

A second-pass review against the "HN-tomorrow-morning" bar surfaced
6 BLOCKING items, all fixed here. No code-behavior changes — pure
docs / wiring / repo-organization polish.

- **B3** — Broken brace-expansion markdown link in README + SECURITY
  (`[bughunt-{1..5}-triage.md](.)` doesn't render). Replaced with
  explicit per-round links into the new `audits/` directory.
- **B4 + B5** — `leonard`, `leonard-hook`, and `leonard-mcp` now all
  respond to `--version`. The two cobra-rooted binaries via cobra's
  built-in `Version` field; `leonard-mcp` via an early argv check
  before the MCP run loop (cobra isn't on that path).
- **B6** — `.leonard/config.toml` advertised `[index]`, `[verifiers]`,
  and `block_on_fabricated_symbol` — none of which the runtime
  Config struct reads (bughunt-2 Theme A trimmed them out 30+
  versions ago). Rewritten to only contain the real schema.
- **S1 + S2** — 25 audit/triage markdown files moved out of the
  repo root into a new `audits/` directory (with its own index
  README); the root now shows the ~8 user-facing files instead of
  32. Stale `phase-{1,2,3}-brief.md` and `fix-1-brief.md` MVP-era
  files removed.
- **S5** — README gets CI / License / Go Reference badges at top.
- **S7** — Status-line tone updated from "have driven the project"
  (past-progressive, reads in-progress) to "stable; every HIGH
  severity finding closed" (declarative, reads shipped).

## v0.45.0 — Docs + release infra sweep (bughunt-5 Theme E)

- README rewritten end-to-end: language table now lists all 27+
  tree-sitter languages, the 4 production-dogfooded parsers, the
  3 SFC preprocessors, the 3 structured-file inspectors, and the
  4 manifest dep-graph formats (was 4 rows total).
- Status line jumped from v0.17 to v0.45; bug-hunt discipline
  narrative added (5 rounds + security review).
- DESIGN.md §6 updated — languages previously called "Future" now
  reflect their production status.
- CHANGELOG.md caught up (v0.19 → v0.45 below).
- `.github/` directory added: CI workflow, CONTRIBUTING.md,
  SECURITY.md, Code of Conduct.
- First git tag: `v0.45.0`.

## v0.44.0 — Perf fixes (bughunt-5 Theme F partial, 3 items)

- **F1**: lowered `maxIndexedFileBytes` from 8 MiB to 4 MiB. The
  tree-sitter helper amplifies source size ~100× in RSS during
  parse; a 7.8 MiB Ruby file hit 839 MB RSS. 4 MiB caps the
  worst-case helper RSS at ~400 MB.
- **F2**: `queryUnverifiedClaims` pushes `LIMIT 1000` into the SQL.
  On a 500k-claim ledger the v0.38 unbounded query took 1.15s to
  materialize every row before the Go-side slice truncated.
- **F9**: manifest deps switched from `kind="const"` to
  `kind="dependency"`. v0.36 polluted the const namespace on
  monorepos (7000 const symbols mixed with real-source consts).

## v0.43.0 — Tree-sitter dispatcher polish (bughunt-5 Theme D)

- **F1**: invalid UTF-8 now exits 2 (parse error) instead of 1
  (infra). Read source as `Vec<u8>`, then validate via
  `std::str::from_utf8`.
- **F2**: HCL multi-label blocks no longer emit duplicate symbols
  — added `.` anchor in `HCL_QUERY` so only the FIRST string_lit
  is captured as `@name`.
- **F4 + languages F3**: `is_exported` overhauled to tokenize
  modifier text on word boundaries (so C#'s `protected internal`
  resolves to exported via the `internal` token). Added
  `Language.default_exported_methods` flag so each grammar picks
  the right default for Ruby/Lua/Swift/Kotlin/Scala/etc.

## v0.42.0 — Preprocessor regex robustness (bughunt-5 Theme C)

Replaced the v0.26 / v0.32 `.*?` regex extractors with
context-aware scanners that respect JS string + template literal +
HTML comment lexical state.

- **F1**: `</script>` inside a string literal no longer truncates
  the body.
- **F2**: `<script>` inside an `<!-- ... -->` HTML comment no
  longer fires.
- **F30**: Astro frontmatter regex truncated on `\n---\n` inside a
  template literal — the new `findAstroFrontmatter` mirrors the
  context tracking.
- **F3**: `package.json` dep value handling switched to
  `json.RawMessage` so pnpm/yarn object-form versions don't nuke
  the whole file.

## v0.41.0 — Parent-folding completeness (bughunt-5 Theme B)

`find_parent_name` had a single-strategy lookup that silently
failed for grammars using named-child shapes. Replaced with a
four-step `extract_container_name`:

1. `child_by_field_name("name")` — Java/Ruby/Kotlin/etc.
2. Direct child of kind `name`/`identifier`/`type_identifier` —
   GraphQL.
3. Child of kind `<container>_name` wrapping an identifier — Proto.
4. Child carrying its own `name:` field — SQL (`create_table
   (object_reference name: (identifier))`).

Also widened parent-folding to include `const` so SQL columns get
`module.users.id` instead of colliding on `module.id`.

## v0.40.0 — C++ in-class inline methods (bughunt-5 languages F4, HIGH)

The CPP_QUERY captured `field_declaration` with
`function_declarator` (method declarations) but NOT
`function_definition` with `field_identifier` declarator (inline
method definitions). nlohmann/json — the most-downloaded C++
library on the planet — indexed 551 files and produced ZERO
method symbols.

Added the missing query arm. End-to-end dogfood: `verify dump`
now finds 3 occurrences of `basic_json::dump()` at correct lines.

## v0.39.0 — v0.38 ledger correctness (bughunt-5 Theme A, 1 HIGH + 4 MED)

- **verifier F1 HIGH**: `SupersedeOutstandingFailures` was using
  `claim LIKE '%=failed%'`, which matched any text containing
  "=failed" (e.g. a user claim `user_input=failed to load`).
  Switched to the existing `vet_ok = 0` integer column.
- **integration F3**: `migrateV7` deleted `index=skipped (file
  not found)` rows but `handleMissingFile` STILL wrote them. The
  cleanup was one-shot but the symptom regenerated. Stopped
  recording the claim entirely (same shape as v0.38's
  `handleEscapedPath` change).
- **verifier F4**: rejected negative/zero `verify.Timeout`.
- **verifier F6**: `ResolveClaim` `--note` now capped at 4 KiB.
- **verifier F11**: whitespace-only `command` no longer treated
  as set.

## v0.38.0 — Claim-ledger hygiene (4 fixes)

- `handleEscapedPath` stops recording claims — path-escape is a
  tool-layer rejection, not an unverified work claim.
- New `SupersedeOutstandingFailures` — project-wide supersede on
  vet=ok to catch multi-file fix-cascade case.
- `leonard claims resolve <id> [--note "..."]` CLI escape hatch.
- `migrateV7` one-time cleanup of historical escape-path +
  missing-file claim rows.

## v0.37.0 — Configurable post-edit verifier (PR #3)

Filed in the [issue + PR pair](https://github.com/jasondillingham/leonard/pull/3) shape (Issue #2 describes the constraint; PR #3 ships the fix that honors it) while scaffolding a Rust homelab project that wanted Leonard's verify loop driving `cargo check` instead of the hardcoded `go vet ./...`. Adds opt-in
`[post_edit.verify]` section to `.leonard/config.toml` with
`command`, `working_dir`, `timeout`. When set, post-edit hook
runs the configured command through `sh -c` instead of the
hardcoded `go vet ./...`. Default behavior unchanged.

## v0.36.0 — Manifest-aware dependency graph

Walks four canonical manifest formats and emits one Symbol per
declared dependency:

- `package.json`: dependencies / devDependencies /
  peerDependencies / optionalDependencies.
- `Cargo.toml`: [dependencies] / [dev-dependencies] /
  [build-dependencies]. Inline-table form handled.
- `go.mod`: every `require` via `golang.org/x/mod/modfile`.
- `pom.xml`: top-level `<dependencies>/<dependency>`.

Use case: `verify_symbol("react")` tells Claude whether the
project actually depends on react before fabricating an import.

## v0.35.0 — GLSL + HLSL (shader languages)

Both grammars are C-family — one shared `SHADER_QUERY` captures
function_definition, struct_specifier, and declaration (top-level
uniforms / varyings / inputs / outputs as `@const`). Extensions
include per-stage shorthand: `.glsl`, `.vert`, `.frag`, `.geom`,
`.comp`, `.tesc`, `.tese`, `.hlsl`, `.fx`, `.fxh`.

## v0.34.0 — Just + Starlark (Bazel)

- **Just** (tree-sitter-just): recipes → function, top-level
  assignments → const. Dispatch on `.just` + `justfile` basename.
- **Starlark** (Bazel BUILD/`.bzl`): `def` macros → function; rule
  calls with `name = "..."` → type (Bazel target). Basenames
  BUILD, BUILD.bazel, WORKSPACE, WORKSPACE.bazel.

## v0.33.0 — Erlang + R

- **Erlang**: module_attribute, record_decl, fun_decl.
- **R**: function definitions via the `<-` assignment idiom.

## v0.32.0 — Astro + Solid

- **Astro**: frontmatter (between `---` fences) + embedded
  `<script>` blocks both routed through the TypeScript extractor.
- **Solid**: `.jsx` registered to the TypeScript extractor (Solid
  is documented as "a semantic layer on TSX").

## v0.31.0 — WIT (Smithy deferred)

WebAssembly Component Model types. Smithy was in the original
scope but tree-sitter-smithy 0.0.1 pinned tree-sitter v0.20 —
incompatible with the v0.25 main runtime; deferred until a
compatible grammar surfaces.

## v0.30.0 — OpenAPI / Swagger inspector

Structured-file extractor for API specs. Walks
`paths.<path>.<method>` and `components.schemas` / `definitions`.
Filename-based dispatch (openapi.{yaml,yml,json},
swagger.{yaml,yml,json}).

## v0.29.0 — Jupyter notebooks

Parses .ipynb JSON, concatenates code cells with blank-line
separators, routes through ExtractPython. Markdown/raw cells
skipped.

## v0.28.0 — SQL migration files

tree-sitter-sequel. CREATE TABLE/VIEW/INDEX/FUNCTION + column
definitions. Schema-as-source-of-truth use case for verifying
migration history.

## v0.27.0 — HCL/Terraform + GraphQL SDL + Protocol Buffers

Three IDL/config languages added in one batch.

## v0.26.0 — Vue + Svelte SFC

Regex-based `<script>` block extraction, routed through
TypeScript extractor with line-offset adjustment. (v0.42 later
replaced the regex with a context-aware scanner.)

## v0.25.0 — Solidity + Make + CMake

Build tools beyond Make/CMake plus smart contracts. Introduced
basename-based dispatch (`langExtractorsByName`) for Makefile +
CMakeLists.txt.

## v0.24.0 — Zig + Nix + Elixir

Three smaller-ecosystem languages. Elixir's `defmodule`/`def`/etc.
required the predicate-binder pattern (`@_def` capture filtered
out at the dispatcher level).

## v0.23.0 — PHP + Lua + Bash

Three scripting languages. Lua's three function-declaration
shapes (plain, dot-indexed, method-indexed) all captured.

## v0.22.0 — C + C++

Lower-level languages. C/C++ have deeper nesting; function names
live two levels deep inside `declarator: (function_declarator
declarator: (identifier))`. v0.40 later added in-class inline
method coverage.

## v0.21.0 — Kotlin + Scala + Dart

JVM/Flutter ecosystem languages. Kotlin's `interface` rides on
`class_declaration`; Scala uses `_definition` suffix convention;
Dart's `function_signature` is shared between methods and free
functions.

## v0.20.0 — Ruby + C# + Swift

First batch on the v0.19 tree-sitter strategy. Also closed the
v0.19 known-limitation around method/constructor qname collisions
(via `parent_container_kinds` parent-folding).

## v0.19.0 — Tree-sitter parser strategy + Java validation

The architectural unlock. New Cargo crate at
`internal/parse/treesitter/` — one binary that handles every
supported language, `--lang <name>` selects the grammar. Per-
language wrappers in Go (`ExtractJava`, etc.) are one-liner aliases
routing to `ExtractTreeSitter`. Validated against google/gson:
262 files / 4,136 symbols.

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
