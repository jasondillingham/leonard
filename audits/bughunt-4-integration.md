# Bug Hunt #4 — integration

## Summary

Cross-cutting audit of the v0.7.1-v0.12.0 fix round (path-trust, resource caps,
eval-readiness, OTel lifecycle, Rust correctness, prune-perf migration) plus
the still-present deferred MEDIUMs from rounds 1-3.

**The safety surface holds.** `go test ./...`, `go test -tags otel ./...`, and
`go test -race ./...` all pass clean. A v0.12 binary opens and migrates a
v0.6-shaped DB cleanly (`idx_symbols_parent` appears post-migration). v0.8.0
path-trust does not reject legitimate paths on a normal Go project. End-to-end
OTel works (`OTEL_TRACES_EXPORTER=stdout` produces properly-nested
`leonard.pre-edit` and `leonard.pre-edit.sibling-scan` spans on a real
PreToolUse payload). 50 concurrent post-edit hooks under v0.7.0 batched-prune +
v0.8.0 path-trust produce the expected 50 claim rows with no lock errors. The
default binary still has zero OTel symbols (`go tool nm | grep -c
opentelemetry == 0`); the OTel surface remains opt-in and absent from the
default build.

But **the docs got worse**. README still calls Leonard "v0.1" while the
binary stamps itself "0.12.0". The "Components" bullet for the CLI lists only
`init / index / verify / mcp`, missing the `decisions`, `claims`, and `doctor`
subcommands that have been live since v0.2. The "Project layout" line still
reads "Python (gpython)" — gpython was dropped four months ago in v0.2. The
Telemetry section says `-tags otel` instruments "the binaries" but only
`leonard-hook` calls `telemetry.Init`; `leonard` and `leonard-mcp` are
byte-identical between default and `-tags otel` builds at the symbol level
(MD5 differs from build-ID stamping only; `nm` shows zero `opentelemetry`
symbols in either). v0.8 path-trust, v0.9 resource caps, and v0.7.1's
`idx_symbols_parent` migration have **zero coverage** in README or DESIGN —
all three are user-visible behaviour changes that landed silently.

`go mod tidy` is still NOT idempotent — wants to promote four
`go.opentelemetry.io/...` deps from indirect to direct require (bughunt-3
integration F2, unaddressed). Makefile still references `leonardreal` build
tag (bughunt-2 integration F9, dead through rounds 2 and 3).

Three deferred MEDIUMs from earlier rounds are **confirmed still present** in
v0.12.0 code:
- **bughunt-2 cli F9** — CLI doesn't walk up to find `.leonard/`; reproduced.
- **bughunt-2 cli F18** — doctor double-counts a stale-and-empty file; reproduced.
- **bughunt-2 pre-edit F1** — sibling-scan skip list still hardcodes 4 names
  while indexer's `defaultSkipDirs` now has 15.

Public-release readiness has not moved since bughunt-3 flagged it: still **no
git tags**, **no `.github/workflows/`**, **no `CHANGELOG.md`**, **no
`CONTRIBUTING.md`**. The first eval live run is now genuinely runnable
end-to-end on `mockllm` (verified — wiring works, mockllm completes a sample
without crashing), so the only remaining blocker for an
`ANTHROPIC_API_KEY`-driven run is having `leonard-mcp` on `PATH` (the eval
hard-codes the binary name rather than a path). The eval README documents the
`go install` step that puts it there, so this is documented-not-broken — but
worth a CI-friendly fallback path.

Eval framework round-3 MEDIUMs F3 (regex-fence detection), F4 (brittle
reason-string parser), F7-F14 (binary scoring, temperature unpinned, README
cost figure) are all still present — v0.10.0 closed only F1+F2+F5+F6 from the
five flagged in the bughunt-3 triage.

## Findings

### F1 — README is two months behind the code (compounded drift)

- **Severity:** medium
- **Reproducer:** `grep "v0\.1\|gpython\|init.*index.*verify.*mcp" README.md`
  and compare against `~/go/bin/leonard --help` and `mcp.DefaultImplementation()`.
- **Observed:** README §"Status" says "**v0.1**, self-dogfooded on this repo as
  of 2026-05-19." `internal/mcp/server.go:19` returns version `"0.12.0"`.
  README §"Components" lists CLI subcommands as `init`, `index`, `verify`, `mcp`
  — the live binary has `claims`, `decisions`, `doctor` in addition (live
  since v0.2). README §"Project layout" still reads
  `internal/parse # Go (stdlib), Python (gpython), TypeScript (hand-rolled)
  extractors` — gpython was removed in v0.2 and Rust was added in v0.5. README
  §"Telemetry" claims `-tags otel` rebuilds "the binaries" with OTel — only
  `leonard-hook` is instrumented (see F2). Bughunt-3 integration F1 + the
  README portion of that finding catalogued this; v0.8 through v0.12 added
  four more user-visible changes without touching the README at all.

  Specifically uncovered in README:
  1. v0.7.1 `idx_symbols_parent` migration — first index run against a pre-v5
     DB does a one-time CREATE INDEX. Operators upgrading from v0.6.x see a
     migration step they don't expect.
  2. v0.8.0 path-trust — silently rejects PostToolUse `file_path` values that
     escape the project root. A user might wonder why a `/tmp/foo.go` Edit
     doesn't show up in their index.
  3. v0.9.0 resource caps — `MaxSnippetBytes = 1 MiB`,
     `MaxHookPayloadBytes = 16 MiB`, `MaxMultiEditElements = 100`,
     `maxDecisionTopicBytes = 256`, `maxDecisionChoiceBytes = 4 KiB`,
     `maxDecisionReasoningBytes = 32 KiB`, `maxClaimSummaryBytes = 4 KiB`,
     `maxClaimEvidenceBytes = 256 KiB`. A user hitting any of these gets a
     generic error and no doc to consult.
  4. `.leonardignore` is implemented and tested (see
     `internal/index/indexer.go:467`, `internal/index/indexer_test.go:160`)
     but never mentioned in README. Combined with v0.6.1's expanded
     `defaultSkipDirs`, the use case for `.leonardignore` is now narrow
     (project-local additions to the default ignore list — e.g.,
     `generated/` or a custom `out/`) but it's still real.
- **Expected:** README content matches code as of v0.12.0 — version, CLI
  surface, language list, OTel scope, security/limit story.
- **Suggested fix shape:** single README-update PR that:
  - Replaces "v0.1" → "v0.12" with brief changelog or links to commit log.
  - Rewrites "Components" to mention `claims`, `decisions`, `doctor`.
  - Replaces "Python (gpython)" with "Python (host `python3` subprocess)" and
    adds "Rust (`syn`-based subprocess at `internal/parse/rust/`)".
  - Tightens "Telemetry" — say which binaries are instrumented (only
    `leonard-hook`) so users don't expect spans from CLI/MCP.
  - Adds a "Security defaults" subsection covering the path-trust check and
    the resource caps with their numeric values.
  - One sentence on `.leonardignore` syntax + use case.
- **Out of scope for this investigation:** DESIGN.md drift (covered by
  bughunt-3 integration F1 — still entirely valid).

### F2 — OTel `-tags otel` is documented as instrumenting all three binaries; only one binary uses it

- **Severity:** medium
- **Reproducer:**
  ```bash
  go build -tags otel -o /tmp/leonard-mcp-otel ./cmd/leonard-mcp
  go build -o /tmp/leonard-mcp-default ./cmd/leonard-mcp
  ls -l /tmp/leonard-mcp-*               # same size (14,332,050)
  go tool nm /tmp/leonard-mcp-otel    | grep -c opentelemetry  # 0
  go tool nm /tmp/leonard-mcp-default | grep -c opentelemetry  # 0
  ```
  Same for `./cmd/leonard`. Only `cmd/leonard-hook/main.go` calls
  `telemetry.Init`. README §"Telemetry" reads "rebuild with the `otel` tag"
  and "the binaries" plural.
- **Observed:** Binaries are byte-identical-in-symbols between default and
  `-tags otel` builds for `leonard` and `leonard-mcp` (md5 differs only from
  build-ID/timestamp stamping). README copy promises something that only one
  of the three binaries delivers. bughunt-3 otel F4 originally flagged this;
  v0.11 (otel lifecycle fixes) explicitly did not touch the scope.
- **Expected:** Either (a) all three binaries call `telemetry.Init` so
  `leonard mcp` invocations and `leonard index` runs produce spans too, or (b)
  README clearly says "only the hook is instrumented in v0.12; CLI and MCP
  server are planned for a later release."
- **Suggested fix shape:** (a) is the better doc story long-term — a real OTel
  user wants `leonard.mcp.tool.verify_symbol` spans and `leonard.index.walk`
  spans, not just hook timings. (b) is a one-line README edit. Pick based on
  whether OTel rollout to the other binaries is a near-term plan; if not,
  document the limitation rather than overpromising.
- **Out of scope:** the actual cost/benefit of CLI/MCP instrumentation (not
  this lane).

### F3 — `go mod tidy` is still non-idempotent (bughunt-3 integration F2 unaddressed)

- **Severity:** medium
- **Reproducer:** `go mod tidy -diff`
- **Observed:** Wants to promote four deps from indirect to direct require:
  ```
  +	go.opentelemetry.io/otel v1.43.0
  +	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0
  +	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.43.0
  +	go.opentelemetry.io/otel/sdk v1.43.0
  ```
  These are used in `internal/telemetry/otel.go` under `//go:build otel` so
  Go's dependency resolver sees them as indirect from the default build's
  POV. A contributor who runs `make tidy` (or any IDE that does it on save)
  silently reshuffles `go.mod` every commit.
- **Expected:** `go mod tidy` is a no-op on a fresh checkout. CI should fail
  if it isn't.
- **Suggested fix shape:** options:
  1. Add a no-tag-required import marker in `internal/telemetry/doc.go` that
     pulls in the four OTel paths as untagged blank imports (`_ ".../otel"`)
     — keeps them resolvable but blank.
  2. Manually maintain the four entries as direct require even when no untagged
     code uses them (Go 1.17+ allows this; tidy will respect a comment-marked
     entry).
  3. Move OTel deps to a separate module under `internal/telemetry/` so the
     main module's tidy is clean. Heaviest of the three.
- **Out of scope:** OTel coverage gaps (F2 above).

### F4 — bughunt-2 cli F9: CLI still doesn't walk up to find `.leonard/`

- **Severity:** medium (carry-over)
- **Reproducer:**
  ```bash
  mkdir -p /tmp/f9/sub && cd /tmp/f9 && leonard init .
  cd sub && leonard verify Foo
  ```
- **Observed:** `leonard: no .leonard here — run 'leonard init' first`
  (exit 2). Working from a subdirectory of a Leonard-enabled project fails
  every CLI subcommand that needs the store. `cmd/leonard/decisions.go:132`
  (`dataDirForCwd`) checks only `filepath.Join(cwd, dataDirName)` with no
  upward walk; `cmd/leonard/doctor.go:31` does the same inline.
- **Expected:** every git/npm/cargo CLI walks up from `cwd` looking for the
  project marker. Leonard should match.
- **Suggested fix shape:** introduce `dataDirForCwd` that walks up via
  `filepath.Dir` until it finds `.leonard/` or hits the filesystem root,
  matching git's `.git/` lookup. Used by all CLI subcommands and by
  `init` to detect "already inside an existing Leonard project — refuse to
  re-init without a flag."

### F5 — bughunt-2 cli F18: doctor still double-counts stale+empty files

- **Severity:** medium (carry-over)
- **Reproducer:**
  ```bash
  mkdir /tmp/f18 && cd /tmp/f18 && leonard init .
  # Create a >512B Go file with no top-level decls (only comments).
  printf 'package main\n%s\n' "$(yes '// pad' | head -n 50)" > big.go
  leonard index   # indexes 1 file, 0 symbols
  rm big.go
  leonard doctor
  ```
- **Observed:**
  ```
  Issues
    parse-failure suspects: 1 file(s) with zero extracted symbols
      big.go
    stale files: 1 (file row in store but missing on disk — run leonard index)
      big.go
  ```
  The same file is reported as both "parse-failure suspect" and "stale" —
  classifications that should be mutually exclusive (a file that's missing on
  disk can't also be a parse failure; the parser never ran on it this index
  pass).
- **Expected:** the stale-file check should run first; files in `StaleFiles`
  should be excluded from the parse-failure suspect list.
- **Suggested fix shape:** in `cmd/leonard/wire_real.go` Doctor, build the
  stale set first, then exclude stale paths from `EmptyFiles` accumulation
  (lines 173-200). One-line change of the loop order plus a set-membership
  check.

### F6 — bughunt-2 pre-edit F1: sibling-scan skip list still diverges from `defaultSkipDirs`

- **Severity:** medium (carry-over; worsened by v0.6.1's expansion of
  `defaultSkipDirs`)
- **Reproducer:** compare `internal/index/indexer.go:45-61` (15 entries) to
  `internal/hooks/pre_edit.go:389` (4 entries).
- **Observed:** `defaultSkipDirs` in the indexer is 15 names:
  `.git, .mypy_cache, .next, .nuxt, .pytest_cache, .ruff_cache, .tox, .venv,
  __pycache__, build, dist, node_modules, target, vendor, venv`. The
  sibling-scan walker in `readSiblingPackages` hardcodes only `vendor`,
  `testdata`, `node_modules`, plus dot-prefixed dirs. So a pre-edit run on a
  project with `dist/`, `build/`, `target/`, `.venv/`, etc. walks into those
  directories — costly on large projects (bughunt-2 pre-edit F3 measured
  480ms warm on a 10k-file project; v0.6.1 silently made this worse for
  projects with `dist/build` because the indexer skips them but the pre-edit
  walker keeps descending).
- **Expected:** one source of truth for "skip-by-default dirs" used by both
  the indexer walk and the pre-edit sibling scan.
- **Suggested fix shape:** export `defaultSkipDirs` (or wrap as
  `func IsSkipDir(name string) bool`) from `internal/index`, import from
  `internal/hooks/pre_edit.go`. Single-line diff each side.

### F7 — Resource cap on indexed file size is missing (security F4 partial close)

- **Severity:** medium
- **Reproducer:** `grep -rn 'maxFile\|MaxIndexed\|fileSizeCeiling' internal/index/`
  returns nothing.
- **Observed:** v0.9.0 added caps for hook snippet (1 MiB), MultiEdit element
  count (100), decision/claim text (32 KiB / 256 KiB). But the indexer's
  `indexAbs` (`internal/index/indexer.go:390`) reads the whole file via
  `os.ReadFile` with no size check. A multi-GB file in the project root —
  whether by accident (a committed log dump, an SQLite extract, a binary
  blob) or by adversarial input (a contributor adding a giant `.go` file that
  appears legitimate) — gets fully loaded into memory before the extension
  filter even runs. The existing extension filter (`.go|.py|.rs|.ts|.tsx`)
  reduces the attack surface, but a `.py` file is unbounded; and the
  indexer's `IndexFile` codepath skips even the extension gate.
- **Expected:** Theme B from bughunt-3 triage says "introduce `maxSnippetBytes`,
  `maxDecisionTextBytes`, `maxClaimTextBytes`." v0.9 covered the first three.
  Security F4 also flagged "indexed-file size" as part of the same theme —
  not covered.
- **Suggested fix shape:** add `const MaxIndexedFileBytes = 16 << 20` (or 4
  MiB — Leonard's purpose is symbol extraction, no real source file approaches
  that) in `internal/index/indexer.go`. Stat the file before `ReadFile`; on
  oversize, log to `ParseFailures` and continue.

### F8 — Eval F4 still present: brittle reason-string parser in scoring.py

- **Severity:** medium (deferred from bughunt-3 eval F4)
- **Reproducer:** `grep -n FABRICATION_PREFIX evals/inspect/scoring.py`
- **Observed:** `evals/inspect/scoring.py:38` hardcodes
  `FABRICATION_PREFIX = "blocked references to symbols not in the index:"`,
  then parses the hook's `permissionDecisionReason` string at line 152-155 by
  splitting on that prefix. `internal/hooks/pre_edit.go:505` writes that
  exact string. Any future tweak to the hook's English wording (caps,
  punctuation, the word "blocked" → "rejected") silently breaks the scorer —
  every block produces an empty fabrications list and scores 1.0. The hook
  doesn't have a test that locks the prefix; nothing fails CI when the strings
  drift.
- **Expected:** the scorer should not depend on parsing the user-facing
  string. It should either (a) call `find_symbol` directly via the MCP
  client to discover which refs are missing, or (b) the hook should emit a
  machine-readable field (e.g., a JSON sidecar in `hookSpecificOutput`) with
  the list of fabricated references.
- **Suggested fix shape:** preferred — (b), add a non-standard sub-field to
  `PreToolUseSpecificOutput` (e.g.
  `metadata.fabricatedReferences []string`), populated whenever the hook
  blocks. Claude Code's hook spec accepts unknown fields; the eval scorer
  reads the field; the English reason stays for the model. (a) is uglier
  because it makes the scorer carry an MCP client.

### F9 — Eval depends on `leonard-mcp` being on `PATH`; not always true in CI

- **Severity:** low
- **Reproducer:**
  ```bash
  cd evals/inspect && uv sync
  unset GOPATH PATH=/usr/bin:/bin uv run inspect eval \
      tasks.py@fabrication_with_leonard --model mockllm/model --limit 1
  ```
- **Observed:**
  ```
  FileNotFoundError: [Errno 2] No such file or directory: 'leonard-mcp'
  ```
  `tasks.py:79` configures `mcp_server_stdio(command="leonard-mcp", args=[])`
  with no path. Works fine in dev (where `$HOME/go/bin` is on PATH) but is a
  trip-hazard for CI / containerized eval runs.
- **Expected:** the eval should resolve the binary deterministically — either
  via an explicit path (`shutil.which("leonard-mcp") or build-it-here`) or by
  letting the contributor set `LEONARD_MCP_BIN` in their env.
- **Suggested fix shape:** at the top of `tasks.py`, do
  `LEONARD_MCP = os.environ.get("LEONARD_MCP_BIN", shutil.which("leonard-mcp"))`
  with a clear assertion message if neither resolves. Pass `LEONARD_MCP` as
  `command=` instead of the bare name. One-shot fix; documents the
  environmental dependency.

### F10 — No live migration smoke test in tree

- **Severity:** medium (carry-over from bughunt-2 integration F4 and bughunt-3
  integration F9)
- **Reproducer:** `grep -rn 'TestMigrate\|migrateV.*_test' internal/store/`
- **Observed:** Zero tests for stepwise migration. The current store_test.go
  checks only that `schema_version` ends at `5` after a fresh `Store.Open`.
  Nothing tests v1→v5, v3→v5, or v4→v5 against a real prior-schema DB. A bad
  migration step would silently corrupt every user's data on upgrade — and
  the v0.7.1 `migrateV5` migration was added without a test that exercises
  the actual ALTER path against a pre-existing v4 DB.

  **Manual smoke test (this round):** I built leonard@v0.6.0 (commit f7bfbc4),
  ran `leonard init . && leonard index` against a copy of the repo to create
  a v4-schema DB, then pointed v0.12 at the same `.leonard/leonard.db`. v0.12
  migrated cleanly and `idx_symbols_parent` appeared in the index list. So
  the migration **works** today — but there's nothing in CI that would catch
  a future regression.
- **Expected:** a test that, for each historical schema version (v1, v2, v3,
  v4), seeds a small fixture DB at that version and confirms a fresh
  `Store.Open` migrates it cleanly to current.
- **Suggested fix shape:** introduce `internal/store/migrate_test.go` with a
  series of `TestMigrate_FromV{N}` subtests. Each prepares a `:memory:` SQLite
  with the table DDL from schema vN (cached as a string literal so the test
  doesn't depend on running the actual migration), inserts a small
  representative row in each affected table, runs `Store.Open`, and asserts:
  (a) `schema_version == current`, (b) the seeded rows are still present and
  readable, (c) every expected post-migration index/column exists.

### F11 — Public-release scaffolding still absent (carry-over from bughunt-3)

- **Severity:** low (informational, but holds back any "publish this" move)
- **Reproducer:** `ls .github CHANGELOG.md CONTRIBUTING.md; git tag`
- **Observed:**
  - No `.github/workflows/` — no CI runs at all. `make check` covers a subset
    of packages (Makefile L9: only `cmd/leonard{,-hook}/...` +
    `internal/hooks/...` + `internal/config/...`) and uses a dead
    `leonardreal` build tag for `make build-real` (L21). The Makefile's
    own assumptions are years out of date.
  - No git tags. v0.1 through v0.12 ship as commit messages only;
    `go install github.com/jasondillingham/leonard/cmd/...@v0.12.0` won't
    resolve.
  - No CHANGELOG.md.
  - No CONTRIBUTING.md.
  - LICENSE present (Apache 2.0).
  - README still labels project as "v0.1" — see F1.
- **Expected:** for a project that's ready to put in front of strangers,
  these are table stakes. Bughunt-3 integration F8 catalogued the same gap.
- **Suggested fix shape:** a separate "public-release prep" workstream rather
  than a finding-by-finding fix. The pieces are mechanical:
  - `.github/workflows/test.yml` running `go test ./...`,
    `go test -tags otel ./...`, `go test -race ./...`,
    `go mod tidy -diff` (gated; see F3) on push.
  - `git tag v0.12.0` retroactively on the matching commit, and tags going
    forward.
  - CHANGELOG generated from commit messages (each `vN.N.N: ...` commit is
    already changelog-shaped).
  - CONTRIBUTING.md covering: how to build leonard-extract-rust, how to
    install hooks via `.claude/settings.local.json`, how to run the bug
    hunts, how to add a new language extractor.
  - Naming decision — "Leonard" is still in use everywhere; no obvious
    conflicts surfaced in this round.

### F12 — Makefile bitrot persists (Lane integration F9 / bughunt-2, F3 / bughunt-3)

- **Severity:** low
- **Reproducer:** `cat Makefile`
- **Observed:** `make build-real` invokes `go build -tags leonardreal …`. The
  `leonardreal` tag was removed when the store/parser branches merged
  pre-v0.1. The comment above the target ("After the store and parser
  branches merge to main, swap `make build` for `make build-real`") is
  historical. `make check` only tests four packages — missing `cmd/leonard-mcp`,
  `internal/index`, `internal/mcp`, `internal/parse`, `internal/store`,
  `internal/telemetry`.
- **Expected:** `make check` should match the real test surface (`./...`).
  `make build-real` should be removed or rewritten.
- **Suggested fix shape:** replace `Makefile` contents with `go test ./...`
  + `go vet ./...` + `go build ./cmd/...` targets and drop the
  `leonardreal` tag entirely. Optionally add a `make check-otel` target.

### F13 — Rust F4-F7 (cfg-gated duplicates, `pub use`, trait method bodies, associated consts) still deferred

- **Severity:** informational (consistent with the shallow-index contract)
- **Reproducer:** see bughunt-3 rust F4-F7.
- **Observed:** `internal/parse/rust/src/main.rs` still walks only
  `visit_item_fn`, `visit_item_struct`, `visit_item_enum`, `visit_item_trait`,
  `visit_item_type`, `visit_item_const`, `visit_item_static`, and
  `visit_item_impl` → method-decls inside impl blocks. No `visit_item_use`
  (no `pub use` re-exports). No `cfg(...)` attribute inspection (sibling cfg
  branches both extracted with same qname). No `visit_trait_item_fn` (trait
  method declarations dropped — only impl-side methods caught). No
  `visit_impl_item_const` (associated constants dropped).
- **Expected:** documented in DESIGN.md or in `internal/parse/rust/`'s
  README — none of these are extracted by design.
- **Suggested fix shape:** add a "v0 scope" comment block to
  `internal/parse/rust/src/main.rs` (or a separate `SCOPE.md`) calling out
  the four exclusions so a contributor doesn't get surprised. No code change
  required.

### F14 — Pre-edit F3 perf concern still real for large monorepos

- **Severity:** low
- **Reproducer:** see bughunt-2 pre-edit F3 — 480ms warm at 10k Go files.
- **Observed:** `readSiblingPackages` re-walks the entire module on every
  pre-edit invocation (no cache). With OTel disabled the span overhead is
  zero but the actual filesystem walk cost is unchanged. With OTel enabled
  the operation is now observable as the `leonard.pre-edit.sibling-scan`
  span — operators can see this — but no caching has been added between
  bughunt-2 (when the perf concern was flagged) and v0.12. F6 above
  (skip-dir divergence) makes this worse on a project that has `dist/` or
  `build/` (the walker descends into them while the indexer skips).
- **Expected:** for a module of N packages the walk is O(N), happens on
  every Edit/Write, blocks the hook. Should be either (a) cached with
  invalidation on package-add/remove, or (b) replaced by reading from the
  symbol store (every indexed `.go` file already has its package name in
  symbols).
- **Suggested fix shape:** (b) is cleaner — sibling-package alias map can be
  served by the store with a single query (`SELECT DISTINCT … FROM symbols
  WHERE language='go'`). Avoids a redundant filesystem walk on the hook hot
  path. Lift to a function on `internal/store` and call from `pre_edit.go`
  instead of `readSiblingPackages`.

### F15 — Eval framework live-run readiness: mockllm path works; PATH-resolution is the only remaining gap for ANTHROPIC_API_KEY run

- **Severity:** informational
- **Reproducer:**
  ```bash
  cd evals/inspect && uv sync
  PATH="$HOME/go/bin:$PATH" uv run inspect eval \
      tasks.py@fabrication_with_leonard --model mockllm/model --limit 1
  ```
  Completes successfully with score 0.0 (mockllm produces no Go code, so the
  scorer correctly reports `failure_mode=no_code_block`).
- **Observed (positive):** With v0.10.0's eval-readiness fixes applied,
  the framework is wired correctly:
  - `mcp` is in `pyproject.toml` (v0.10 fix to F2).
  - `scoring.py` passes `cwd=project_root` to the hook subprocess (v0.10
    fix to F1).
  - `inspect list tasks` returns all three tasks.
  - `inspect eval` against mockllm completes both the control arm and the
    treated arm without crashing.
- **Observed (still broken):**
  - F9 (above) — `leonard-mcp` must be on PATH at task-start. Not blocking,
    but easy to miss in CI.
  - F8 (above) — `scoring.py`'s reason-string parser will silently report
    zero fabrications if pre-edit's English wording changes.
  - F3 from bughunt-3 (regex code-fence detection) — `extract_go_code`
    still uses one regex (`extract_go_code` in scoring.py); doesn't catch
    `golang`, uppercase, fences with a space, or no-trailing-newline forms.
    A live model often emits ` ```Go ` or ` ```golang `; those samples
    score 0.0 silently.
  - F7-F14 from bughunt-3 — binary scoring, temperature unpinned, README
    cost figure (5× off), inverted hook-fabrication-scan trap. All
    correctness/measurement issues that don't block running, but skew the
    interpretation of any live numbers produced.
- **Expected:** an `ANTHROPIC_API_KEY`-driven run is now possible. The
  resulting numbers will be directionally meaningful but should be reported
  with caveats about the unaddressed scoring issues.
- **Suggested fix shape:** none for this finding — it's a status report.
  Address F8 + F9 + the F3 fence-regex before publishing eval numbers
  externally.

### F16 — DESIGN.md drift compounds bughunt-3 integration F1

- **Severity:** medium
- **Reproducer:** `grep -in 'OTel\|OpenTelemetry\|telemetry\|path-trust\|maxSnippet\|idx_symbols_parent' DESIGN.md` returns zero hits.
- **Observed:** DESIGN.md has been touched once since bughunt-3 (the §4.6
  config block was scrubbed for Theme A in v0.2). It has **no coverage** of:
  - v0.5 Rust extractor (still listed as "Future / explicit non-MVP" at
    line 269).
  - v0.6 OTel build-tag mode.
  - v0.7.1 schema migration (the v5 index).
  - v0.8 path-trust hygiene.
  - v0.9 resource caps.
  - v0.10 eval-framework readiness fixes.
  - v0.11 OTel lifecycle fixes.
  - v0.12 Rust correctness improvements.

  Items 1-7 of bughunt-3 integration F1 are all still present unchanged.
  Items 8-11 are also unchanged.
- **Expected:** DESIGN.md is the architecture-of-record. It should be in
  sync with the code or the divergence should be explicitly marked.
- **Suggested fix shape:** dedicated "DESIGN sync" PR — same shape as the
  recommendation in bughunt-3 integration F1. Tick the §6 phase boxes that
  shipped, move Rust out of "Future" into the language matrix, write a §4.7
  on OTel scope and §4.8 on path-trust + resource caps, update the §4.3 MCP
  tool table to 10 entries.

## Things that worked

Verified solid in this round:

- **`go test ./...` clean** on default build.
- **`go test -tags otel ./...` clean** — OTel build tag adds no test
  regressions.
- **`go test -race ./...` clean** — no new race conditions introduced by
  v0.7.0+v0.7.1+v0.8.0 (batched prune + path-trust).
- **v0.6 → v0.12 migration works** — built leonard@f7bfbc4 (v0.6),
  created a v4-schema DB, pointed v0.12 at it. Migration completed;
  `idx_symbols_parent` appears in the index list post-migration.
- **v0.8 path-trust doesn't reject legitimate paths** — tested on a normal
  Go project with sub-packages. `leonard index` indexed every file;
  `verify` found symbols across the package layout. Confirms bughunt-3
  observation on a real fixture.
- **OTel end-to-end works** — built `leonard-hook` with `-tags otel`, set
  `OTEL_TRACES_EXPORTER=stdout`, fired a real PreToolUse payload. Got
  properly-nested `leonard.pre-edit` (parent) + `leonard.pre-edit.sibling-scan`
  (child) spans on stderr. v0.11's lifecycle fixes hold.
- **Concurrent post-edit hooks** — 50 parallel post-edit hooks under v0.7.0
  batched-prune + v0.8.0 path-trust against the same DB. Final claim count =
  50, zero `database is locked` errors, all file rows correct.
- **`tools/list` exposes 10 tools** — matches README §"Components" exactly:
  `verify_symbol, find_symbol, list_files, record_decision, get_decisions,
  supersede_decision, get_stale_decisions, record_claim,
  get_unverified_claims, recent_changes`. (DESIGN §4.3 still lists 9, but
  README is right.)
- **Default binary has zero OTel symbols** —
  `go tool nm /tmp/leonard-mcp-default | grep -c opentelemetry == 0`.
  The opt-in story is real.
- **Binary size growth v0.6 → v0.12 is minimal** — `leonard` +608 B,
  `leonard-mcp` +96 B, `leonard-hook` +36 KB (default build). The
  `-tags otel` build of `leonard-hook` adds ~15.7 MB over the default, as
  expected.
- **Eval framework runs end-to-end on mockllm** — both control and treated
  arms complete (with `leonard-mcp` on PATH). v0.10 closed the structural
  blockers to live runs.
- **`leonard doctor` produces a reasonable health report** on this repo —
  163 files, 1139 symbols, 4 languages, no parse failures or stale files.

## Open questions

- **DESIGN.md sync — should it happen now or with v1.0?** Doc drift is the
  consistently-flagged-and-consistently-deferred theme across all four
  bughunt rounds. Question is whether to slot a single docs-sync PR ahead of
  the v0.13 round or wait until a "1.0 readiness" pass.
- **OTel scope decision: instrument the other two binaries, or document the
  limitation?** v0.11 closed the lifecycle bugs but didn't touch scope. A
  real OTel user wants spans on `leonard.mcp.tool.*` calls, not just the
  hook hot path.
- **Pre-edit F3 sibling-scan caching** — when does a 480ms warm walk on a
  10k-file project start hurting? Speechify FastAPI was 0 parse failures and
  smaller; the gold dataset for this perf concern would be a real
  monorepo. Without one we're optimizing in the abstract.
- **`.leonardignore` future** — v0.6.1's `defaultSkipDirs` expansion covers
  the common cases the file was added for (`.venv`, `dist`, `build`,
  `target`). Worth documenting + keeping for project-local additions, or is
  it now overhead for a use case the defaults handle?
- **`leonard-mcp` discovery for eval** — F9 is small but it's one of those
  details that breaks a CI integration. Easy fix; just needs to be decided.
- **Indexed-file size cap** (F7) — what's the actual budget? 4 MiB? 16 MiB?
  The current `indexer.go` has no number to argue with; pick one before the
  next round and document it.
