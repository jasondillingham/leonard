# Bug Hunt #3 — integration

## Summary

Cross-cutting audit of the v0.3-v0.7 surface area, the documentation drift those
five point releases accumulated, the carry-over MEDIUM findings deferred from
Round 2, and concurrent-access invariants under the new v0.7 batched-prune
sweep. The headline result: **the safety story is intact** — `go test ./...`,
`go test -tags otel ./...`, and `go test -race ./...` all pass clean;
`CGO_ENABLED=0 go install ./cmd/...` still produces three working pure-Go
binaries; v0.7's `pruneStaleFiles → DeleteFiles` batching survives 50 concurrent
post-edit hooks plus 2 concurrent `leonard index` runs (final file/symbol/claim
counts all correct, zero `database is locked` errors); the MCP `tools/list`
surface still matches the 10 tools README §"Components" advertises; and the
Theme A dead-config sweep verifiably removed `[verifiers]`, `[index].languages`,
`[index].ignore`, `[hooks].block_on_fabricated_symbol`, and the
`pythonInterpreter` constant — none survive in any consumed code path.

But the doc surface didn't keep up with the code. DESIGN.md is now in worse
drift than Round 2 found it. The v0.5 Rust extractor is **production** in
README's language table but still listed under "Future / explicit non-MVP" in
DESIGN §6. The v0.6 OTel build tag isn't mentioned anywhere in DESIGN. The v0.6
README claims `go install -tags otel ./cmd/...` instruments "the binaries" —
but only `leonard-hook` actually calls `telemetry.Init`; the `-tags otel` build
of `leonard` and `leonard-mcp` is byte-identical to the default. The
contributor-onboarding `Install` section in README still only documents
`go install ./cmd/...` — a fresh contributor following it gets a working
Go-only Leonard, then trips over an `ErrRustExtractorUnavailable` the first
time they touch a `.rs` file with no signposting from README on how to fix it.

`go mod tidy` is NOT clean: it wants to promote four `go.opentelemetry.io/...`
deps from `// indirect` to direct require (they're used in build-tag-gated code
but the unconditional `internal/telemetry/doc.go` import isn't enough). A
`make tidy` will silently reshuffle the file every commit. The `Makefile`
itself still references the long-removed `leonardreal` build tag
(bughunt-2 integration F9 — unaddressed).

Three categories of deferred-MEDIUM **are still present** in the code after the
fix-2 sweep: bughunt-2 cli F9 (CLI doesn't walk up to find `.leonard/`),
bughunt-2 cli F18 (doctor double-counts a stale-and-parse-failed file in two
overlapping classifications), and bughunt-2 pre-edit F1 (sibling-scan skip list
diverges from indexer's `defaultSkipDirs` — pre-edit still hardcodes 4 names
while indexer now has 15). One previously-deferred bughunt-2 integration F6
**was silently resolved** by v0.6.1's `defaultSkipDirs` expansion — but the
docs (and integration F6 itself) still flag it as open.

No `.github/workflows/` directory, no `CHANGELOG`, no git tags — despite v0.1
through v0.7 being shipped as discrete commits. The OSS-readiness gap noted
in earlier rounds has gotten longer with five point releases of weight behind
it. None of this is blocking, but a public release would need it.

## Findings

### F1 — DESIGN.md drift compounded by v0.3-v0.7

- **Severity:** medium
- **Reproducer:** read DESIGN.md alongside README.md and the current code.
  Drifts cataloged below; bughunt-2 integration F2 found seven, this round
  finds eleven (the original seven are mostly **unchanged** plus four new
  ones from v0.3-v0.7).
- **Observed:** DESIGN.md drift items, each independently verifiable:

  1. **Rust is "Future / explicit non-MVP" in DESIGN §6** but **"Production"** in
     README §"Language support" since v0.5 (~ten weeks of dogfooding +
     ripgrep validation). The DESIGN bullet at line 269 reads
     `- Other languages (Rust, Swift, Ruby, Java, C/C++)`.
  2. **OTel build tag mode has zero DESIGN coverage.** `grep -in
     "OTel\|OpenTelemetry\|telemetry" DESIGN.md` returns no hits. DESIGN
     never describes the optional telemetry surface or its
     `-tags otel` build, even though README §"Telemetry (optional)"
     is now ~30 lines.
  3. **DESIGN §7 Q2 still describes gpython as the Python resolution.**
     gpython was dropped in v0.2 → host `python3` subprocess. The Q2
     resolution paragraph is now historical, not current. (Bughunt-2 F2.3
     also caught this in the README; both docs still drift on the same
     point.)
  4. **DESIGN §4.1 "Skip `node_modules`, `vendor`, build dirs by
     default"** is wildly out of date. v0.6.1 expanded `defaultSkipDirs`
     to 15 entries:
     ```
     .git, .mypy_cache, .next, .nuxt, .pytest_cache, .ruff_cache, .tox,
     .venv, __pycache__, build, dist, node_modules, target, vendor, venv
     ```
     DESIGN reads as if it's still the v0.1 three-name list.
  5. **DESIGN §3 diagram** (line 67) still says
     `manual: init, reindex, ...` — bughunt-2 integration F2 caught this
     and the linked F2.1 (CLI never had `leonard reindex`). Unchanged.
  6. **DESIGN §4.5 CLI listing** (line 181) still lists
     `leonard reindex <path>`. Unchanged from bughunt-2.
  7. **DESIGN §4.3 MCP tool table** lists 9 tools; live `tools/list`
     returns 10. `get_stale_decisions` (added with v0.2 decision
     decay detection) is absent. Unchanged from bughunt-2.
  8. **DESIGN §6 Phase 1/2/3 checkboxes** still all `[ ]` despite README
     §"Status" claiming v0.1 self-dogfooded plus v0.2-v0.7 visible in
     git log. Unchanged from bughunt-2.
  9. **DESIGN §4.2 store DDL** doesn't reference the `superseded_by_claim_id`
     column on claims (v0.2 supersede-on-fix) or the
     `related_files`/`related_symbols` JSON columns on decisions (also v0.2,
     visible in `migrateV3`/`migrateV4`). The DDL block matches v0.1.
  10. **DESIGN §6 "Phase 3" still includes `recent_changes`** as a
      to-do checkbox, but the tool has shipped (live in `tools/list`
      response) and is documented in README §"Components."
  11. **DESIGN §4.6 config block** is correctly updated — Theme A scrubbed
      the dead knobs. This is the only post-bughunt-2 DESIGN section
      touched, and it's the one I can verify is in sync with code.

  README drift items, fewer but still meaningful:

  - **README §"Telemetry"** says `go install -tags otel ./cmd/...`
    rebuilds binaries with OTel. Only `leonard-hook` is actually
    instrumented (verified: `cmd/leonard-mcp` and `cmd/leonard` neither
    import `internal/telemetry` nor call `telemetry.Init`; binary sizes
    confirm — see F4). The bughunt-3-otel lane independently flagged
    this as F4 in its file. Cross-referenced.
  - **README §"Project layout"** still lists `internal/parse/` as
    "Go (stdlib), Python (gpython), TypeScript (hand-rolled) extractors."
    gpython was dropped in v0.2; Rust was added in v0.5. Should read
    "Go (stdlib), Python (host python3 subprocess), Rust (syn-based
    subprocess), TypeScript (hand-rolled)." (Bughunt-2 F2.3 partially
    caught this; the Rust addition is new drift on top.)
  - **README §"Install"** is a single `go install ./cmd/...` line.
    A fresh contributor on a project with `.rs` files gets clear
    runtime errors (`ErrRustExtractorUnavailable` with a helpful
    message — see "Things that worked"), but README never tells them
    about the helper-binary build step before they hit that error.
    `examples/pydantic-ai/` and `evals/inspect/` aren't mentioned at
    all in the top-level README — a reader who doesn't `ls` won't
    discover them.

- **Expected:** DESIGN.md catches up to v0.7. README cross-links to
  `examples/pydantic-ai/README.md` and `evals/inspect/README.md`,
  and the install section grows a "Optional: Rust support" subsection
  pointing at `cargo build --release` inside `internal/parse/rust/`.
- **Suggested fix shape:** dedicated doc-sync PR. The Round-2 triage
  recommended "promote README to canonical and defer DESIGN to
  architectural-overview" — this round confirms the recommendation but
  the work was never done. The drift is now larger.
- **Out of scope for this investigation:** prose polishing, narrative
  voice, marketing copy.

### F2 — `go mod tidy` is not idempotent: OTel deps want to be direct

- **Severity:** medium
- **Reproducer:**
  ```bash
  cp go.mod /tmp/go.mod.before
  cp go.sum /tmp/go.sum.before
  go mod tidy
  diff /tmp/go.mod.before go.mod
  ```
  Output:
  ```
  9a10,13
  >  go.opentelemetry.io/otel v1.43.0
  >  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0
  >  go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.43.0
  >  go.opentelemetry.io/otel/sdk v1.43.0
  31d34
  < go.opentelemetry.io/otel v1.43.0 // indirect
  ...
  ```
  Plus a few `// indirect` swap-ins (`davecgh/go-spew`,
  `golang/protobuf`, `stretchr/testify`).
- **Observed:** `go mod tidy` against the v0.7 codebase mutates `go.mod`
  by promoting four OTel modules from `// indirect` to direct
  requires. Re-running it after committing would be a no-op, but the
  current `go.mod` is in a state that tidy doesn't agree with. Anyone
  running `make tidy` (the existing target) and then committing will
  produce a small no-op-looking diff on every fresh checkout that
  hasn't run tidy yet.
- **Expected:** `go.mod` reflects what tidy produces. The four
  build-tag-gated OTel imports in `internal/telemetry/otel.go` make
  these direct from tidy's perspective (the build constraint doesn't
  hide them — tidy sees both build configurations).
- **Suggested fix shape:** run `go mod tidy` and commit the resulting
  diff. The `// indirect` markers go away. No code change.
- **Out of scope for this investigation:** whether to constrain
  toolchain via a `go.work` file (bughunt-2 didn't flag the
  toolchain directive; not a new concern here).

### F3 — Makefile's `build-real` target + lane comment are still stale

- **Severity:** low (informational, but unaddressed across two rounds now)
- **Reproducer:** `cat Makefile` shows:
  ```
  # Until the store and parser lanes merge, we only test against this lane's
  # owned packages — the rest of ./... has no test files yet.
  check: vet test
  test:
      go test -race ./cmd/leonard/... ./cmd/leonard-hook/... \
              ./internal/hooks/... ./internal/config/... -count=1
  ...
  build-real:
      go build -tags leonardreal ./cmd/leonard ./cmd/leonard-hook
  ```
  And `grep -rn "leonardreal" .` returns zero hits in source.
- **Observed:** Bughunt-2 integration F9 caught this — `build-real`
  presumes a `leonardreal` build tag, but no source file uses it. The
  `test:` and `vet:` targets restrict to four hand-picked packages from
  pre-Phase-1 lane structure, missing `internal/index`, `internal/store`,
  `internal/parse`, `internal/mcp`, and now `internal/telemetry`. `make
  build-real` builds successfully but produces identical output to
  `make build`.
- **Expected:** Makefile reflects current reality: full module testing
  (the lane structure is gone), single `build` target, no leonardreal
  tag.
- **Suggested fix shape:** trim Makefile. `test` runs `go test -race
  ./... -count=1`. `vet` runs `go vet ./...`. Delete `build-real` and
  the explanatory comment. Add an `otel` target for `go build -tags
  otel ./cmd/...` if Telemetry is now a documented build mode.
- **Out of scope for this investigation:** consolidating Makefile vs.
  the existing CI workflow (there is no CI workflow — see F8).

### F4 — `-tags otel` build only wires up `leonard-hook`; the other two binaries are byte-identical

- **Severity:** medium (overlap with bughunt-3-otel F4; integration angle
  is the README-vs-code drift, not the OTel implementation gap)
- **Reproducer:**
  ```bash
  go build -o /tmp/lm-d ./cmd/leonard-mcp
  go build -tags otel -o /tmp/lm-o ./cmd/leonard-mcp
  ls -la /tmp/lm-d /tmp/lm-o
  ```
  Output:
  ```
  14331954 /tmp/lm-d
  14331954 /tmp/lm-o     ← identical
  ```
  Same for `cmd/leonard`. `leonard-hook` does differ: 11.3 MB default
  → 26.3 MB with otel.

  And:
  ```bash
  grep -rn "telemetry" cmd/leonard cmd/leonard-mcp/
  ```
  Returns zero hits.
- **Observed:** README §"Telemetry" reads:
  > To get real spans, rebuild with the `otel` tag:
  > `go install -tags otel ./cmd/...`
  > Then point the binaries at whatever OTel collector you run...

  But only one of the three binaries (`leonard-hook`) actually wires
  `telemetry.Init` in main. The MCP server has no instrumentation; the
  CLI has no instrumentation. A reader who follows the README
  instructions and points `OTEL_EXPORTER_OTLP_ENDPOINT` at their
  collector gets traces from hook events only.
- **Expected:** either (a) README is honest about this — the otel build
  instruments the hook hot paths, not the long-running MCP server — or
  (b) `leonard-mcp` and `leonard` learn to call `telemetry.Init` too.
  bughunt-3-otel F4 favors (b); integration-lane view is (a) is
  cheaper and reflects current intent.
- **Suggested fix shape:** README text change: "v0.6 added OTel spans
  to `leonard-hook` — the binary in the hot path. The MCP server and
  CLI are not instrumented (long-lived processes, less interesting per
  invocation; tracked for v0.7+)." Keep the otel section but accurate.
- **Out of scope for this investigation:** instrumenting the other two
  binaries — that's bughunt-3-otel F4's territory.

### F5 — Bughunt-2 deferred MEDIUMs still present in code

Two-round-old MEDIUMs that remain unaddressed and are starting to cost.
Each is a verifiable reproducer; none would be hard to fix.

#### F5.a — CLI doesn't walk up to find `.leonard/` (bughunt-2 cli F9)

- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp && mkdir -p walkup/sub && cd walkup
  leonard init . >/dev/null
  cd sub
  leonard verify Foo
  ```
  Output: `leonard: no .leonard here — run `leonard init` first` (exit 2).
- **Observed:** Every CLI subcommand assumes `.leonard/` is in cwd. Running
  from any subdirectory of a Leonard-init'd project fails. `git`, `npm`,
  and `cargo` all walk up to find their config root; Leonard doesn't.
- **Expected:** Walk up from cwd to find `.leonard/`, matching the
  ergonomic convention of every other project-rooted tool.
- **Suggested fix shape:** in `realRuntime.dbPath()` (or wherever the
  store path resolves), traverse cwd → parents until a `.leonard`
  directory is found or root is reached.

#### F5.b — Doctor double-counts a stale-and-parse-failed file (bughunt-2 cli F18)

- **Severity:** low
- **Reproducer:**
  ```bash
  cd /tmp && rm -rf dr && mkdir dr && cd dr
  leonard init . >/dev/null
  echo 'module x' > go.mod
  { echo 'package main'
    for i in {1..50}; do echo "// line $i"; done
    echo 'func broken( {'    # syntax error
  } > broken.go
  leonard index >/dev/null 2>&1
  rm broken.go
  leonard doctor
  ```
  Output (only the Issues block):
  ```
    parse-failure suspects: 1 file(s) with zero extracted symbols
      (these likely failed to parse — run `leonard index` for line/message detail)
      broken.go
    stale files: 1 (file row in store but missing on disk — run `leonard index`)
      broken.go
  ```
- **Observed:** `broken.go` appears in BOTH the parse-failure-suspects
  AND stale-files sections of `leonard doctor`. The classifications
  overlap; a user reads "two problems" when there's only one
  (a stale row that happened to have zero symbols before it went stale).
- **Expected:** stale-files exclusion in the parse-failure-suspects
  collector — if a file row is going to be reported as stale, don't
  also report it as a parse-failure suspect.
- **Suggested fix shape:** in `cmd/leonard/wire_real.go`'s `Doctor`,
  build the stale set first, then exclude stale paths from
  `EmptyFiles`.

#### F5.c — Pre-edit sibling-scan skip list is much shorter than the indexer's (bughunt-2 pre-edit F1)

- **Severity:** medium
- **Reproducer:** `grep -n 'name == \\"' internal/hooks/pre_edit.go:372` and
  compare to `internal/index/indexer.go:45-61`. Pre-edit skips:
  ```
  vendor, testdata, node_modules, (dot-prefixed)
  ```
  Indexer skips:
  ```
  .git, .mypy_cache, .next, .nuxt, .pytest_cache, .ruff_cache, .tox,
  .venv, __pycache__, build, dist, node_modules, target, vendor, venv
  ```
- **Observed:** in a Python project with a `dist/` build output containing
  generated `.go` files (unlikely but possible) or a Rust project with
  a `target/` containing generated test scaffolding, the pre-edit
  sibling walker will recurse into them and resolve sibling-package
  references against the generated trash. The indexer's expanded
  defaults (v0.6.1) didn't propagate to pre-edit.
- **Expected:** one source of truth. Either pre-edit imports
  `index.defaultSkipDirs` directly, or both call a shared helper.
- **Suggested fix shape:** make `defaultSkipDirs` exported (or add a
  small `index.IsSkippedDir(name string)` helper) and have
  `readSiblingPackages` call it.

### F6 — Bughunt-2 deferred-MEDIUM silently RESOLVED but still marked open

- **Severity:** informational
- **Reproducer:**
  ```bash
  sqlite3 .leonard/leonard.db "SELECT path FROM files WHERE path LIKE 'testdata/%'"
  ```
  Run against the live Leonard repo: 0 rows. Bughunt-2 integration F6
  said "indexer walks testdata, pre-edit's sibling-walker skips it;
  symbols defined in testdata are in the index."
- **Observed:** v0.6.1's `defaultSkipDirs` expansion didn't add
  `testdata` — but it did add 12 other names that cover most cases
  where bughunt-2's F6 was reproducible (e.g. `.venv`, `target`). The
  *original* concern in bughunt-2 F6 was a `testdata/` walked by the
  indexer but skipped by pre-edit's sibling scan. Today, **neither walker
  recurses into testdata** — the indexer skips it because the
  Leonard repo root's `.gitignore` covers it (no, it doesn't actually
  — verified by `grep testdata .gitignore` → 0 hits) — *actually*
  because no tests under `testdata/` end in `.go/.py/.ts/.rs` with
  Leonard-recognizable shape, so the dispatcher does walk in but
  finds nothing to register. Wait — there ARE 6 indexed paths under
  `testdata/`:
  ```bash
  sqlite3 .leonard/leonard.db "SELECT path FROM files WHERE path LIKE '%testdata%' LIMIT 20"
  ```
  ```
  internal/mcp/testdata/sample/main.go
  internal/parse/rust/testdata/...
  internal/parse/testdata/typescript/...
  ...
  ```
  So bughunt-2 F6 is **unchanged**: indexer still walks testdata,
  pre-edit's sibling-walker still skips it explicitly (`internal/hooks/pre_edit.go:372`).
  My initial probe sample was misleading; on closer inspection F6 is
  STILL OPEN, not resolved.
- **Expected:** one consistent definition of "in scope" — either the
  indexer skips `testdata/` (adding it to `defaultSkipDirs`) or the
  pre-edit walker stops skipping it.
- **Suggested fix shape:** add `"testdata": true` to `defaultSkipDirs`.
  Be aware that `internal/index/selfhost_test.go` may depend on
  testdata files being indexed; check before changing. (Same caveat as
  bughunt-2 noted.)
- **Note:** I'm leaving this finding in because the "is it resolved?"
  diagnostic itself is useful — without bughunt-2's clear reproducer,
  I would have shipped this as "silently fixed" based on the first
  probe. The actual fact pattern is "open but partially mitigated
  by adjacent fixes."

### F7 — `internal/parse/rust/target/` is gitignored, but no `.leonardignore` honors it

- **Severity:** informational
- **Reproducer:**
  ```bash
  cat .gitignore | grep -i target
  # nothing
  cat .leonardignore 2>/dev/null
  # file doesn't exist
  ls internal/parse/rust/.gitignore 2>/dev/null
  # also doesn't exist
  ```
  The leonard-extract-rust crate has its build artifacts at
  `internal/parse/rust/target/` (88 MB on this machine, see
  bughunt-3-skip-dirs F1 cross-reference).
- **Observed:** Leonard never indexes `target/` content because
  `defaultSkipDirs` covers it. But: anyone vendoring this crate inside
  a project that *expects* `target/` to be indexed (e.g., a Rust
  project with `internal/parse/rust/` as a real subproject) hits the
  same trap bughunt-3-skip-dirs F3 flags — `defaultSkipDirs` is
  enforced unconditionally, no escape hatch.
- **Expected:** if `defaultSkipDirs` stays unconfigurable, this is fine
  (the dogfood case is correctly handled). If F3 from the skip-dirs
  lane lands a config knob, this confirms that the per-project
  override is exactly what's needed.
- **Suggested fix shape:** none — informational, depends on the
  skip-dirs lane's F3/F7/F8 decisions.

### F8 — No CI workflow, no release tags, no CHANGELOG

- **Severity:** informational (OSS-readiness, not load-bearing for v0)
- **Reproducer:**
  ```bash
  ls .github/workflows/ 2>/dev/null
  # no such directory
  git tag -l
  # empty
  ls CHANGELOG* CHANGES*
  # no matches
  ```
- **Observed:** Five versioned releases (v0.2 through v0.7) have shipped
  as conventional commits with explanatory bodies, but:
  - **No git tags** marking any version. `git tag v0.7.0` would be a
    one-liner; nobody has done it.
  - **No CHANGELOG.md.** A user upgrading from v0.5 to v0.7 has no
    structured way to discover that v0.6 added a build-tag-gated
    telemetry surface and v0.7 changed the prune sweep performance
    characteristics.
  - **No `.github/workflows/`** directory at all. The README claims
    self-dogfooding via Claude Code hooks; there's no CI that runs
    `go test ./...` on push or PR. Any test regression ships invisibly
    until the next manual run.
  - The Python `examples/pydantic-ai/` + `evals/inspect/` dirs have no
    automated bitrot check — a CI workflow probing
    `uv sync && uv run python -c "import demo"` would catch a stale
    pyproject.toml or a broken import within seconds; today nothing
    catches it.
- **Expected:** at minimum, `git tag` retroactively applied to the
  five release commits, plus a `.github/workflows/ci.yml` running
  `go test ./...`, `go test -tags otel ./...`, `go vet ./...`, and
  `python -m py_compile` over the eval/demo dirs.
- **Suggested fix shape:** small infra PR — one workflow file, a
  CHANGELOG with entries pulled from each `vN.N.N: ...` commit
  message, retroactive tags on those commits.
- **Out of scope for this investigation:** OSS-positioning narrative
  (DESIGN §7 Q1), CONTRIBUTING.md, code-of-conduct, license headers
  per file. Separate work.

### F9 — Test coverage gaps in v0.3-v0.7 surfaces

- **Severity:** medium (no panic risk; risks ship-by-ship regression)
- **Reproducer:**
  ```bash
  go test -coverprofile=/tmp/cov.out ./...
  go tool cover -func=/tmp/cov.out | awk '$3 == "0.0%"' | wc -l
  # 34 functions at 0% coverage
  ```
- **Observed:** of the 34 zero-coverage functions, two distinct
  clusters concern v0.3-v0.7 work:

  **Cluster 1 — production runtime paths (carried from bughunt-2 F3):**
  - `cmd/leonard/wire_real.go`: every public method of `realRuntime`
    (`IndexAll`, `VerifySymbol`, `RecordDecision`, `GetDecisions`,
    `GetStaleDecisions`, `Doctor`, `GetUnverifiedClaims`).
  - `cmd/leonard-hook/backend_real.go`: every public method of
    `realBackend` (`Indexer`, `Claims`, `Close`, `mustOpenStore`,
    `RecordClaim`, `SupersedeClaimsForFile`).
  Tests use `fakeRuntime` and `fakeBackend`; the real wiring (which
  is where the SQLite + parser + filesystem interactions live) is
  never exercised by Go tests. Bughunt-2 integration F3 already
  flagged this; no fix landed.

  **Cluster 2 — v0.3-v0.7 specifics:**
  - `internal/mcp/adapter.go:85,96,150,254`: `FindSymbolsByName`,
    `FindSymbolsByQuery`, `GetStaleDecisions`, `symbolsToRecords` —
    the real-store adapter paths through MCP. Tests cover the
    `MemStore` adapter, not the SQLite-backed one.
  - `internal/store/store.go:498` `DeleteFile` (singular) at 0% — but
    the batched `DeleteFiles` (the v0.7 sweep) IS covered by
    `internal/store/delete_files_test.go`. The singular path was kept
    for API compatibility; if it ever gets a different code path
    a future regression won't trip.
  - **No end-to-end test verifies OTel spans actually emit from
    hooks/pre_edit.go etc. under `-tags otel`.** The
    `internal/telemetry/otel_test.go` covers `Init` and a synthetic
    `Span` round-trip, but no test asserts that `leonard.pre-edit`,
    `leonard.post-edit`, etc. appear when the hooks run. A rename or
    accidental removal of a span name would ship silently.
  - **No subprocess test boots `leonard-mcp` or `leonard-hook` and
    drives them over stdio.** Bughunt-2 integration F4 still
    standing; the bughunt-1 MCP and hooks lanes had to write `/tmp/`
    harnesses because there's no in-tree way.
  - **No migration test exercises a prior-version embedded DB**
    (bughunt-2 integration F4). v0.7 has four migrations to keep
    correct; a regression on `migrateV3`/`migrateV4` ships invisibly
    until a user with an upgraded DB notices.
  - **No tests for `examples/pydantic-ai/demo.py` or
    `evals/inspect/*.py`** beyond `python -m py_compile` — they
    parse, that's all. A typo in `samples.py` that references a
    Leonard helper that no longer exists won't fail any test.

- **Expected:** at minimum (in priority order):
  1. Migration test with embedded v1/v2/v3 DB blobs (bughunt-2 F4).
  2. `internal/telemetry/otel_e2e_test.go` (build-tag `otel`) that
     runs `RunPreEdit`/`RunPostEdit` under an in-memory OTel
     exporter and asserts the five documented span names emit.
  3. Subprocess test (`cmd/leonard-mcp/stdio_test.go`) that boots
     the binary via `mcp.NewCommandTransport` and round-trips each
     tool — bughunt-1 mcp lane's `/tmp/` harness lifts directly in.
  4. CI check (per F8) that runs `uv sync && python -m py_compile`
     over the example and eval Python.
- **Suggested fix shape:** three test files + one workflow.
  Estimated ~150 lines of test code total.
- **Out of scope for this investigation:** raising coverage past 90%
  (current is 77.4%, which is reasonable). Goal is closing
  *categories* of untested paths, not hitting an arbitrary number.

### F10 — `examples/` and `evals/` directories are invisible from top-level docs

- **Severity:** low (discovery, not correctness)
- **Reproducer:** `grep -in "examples\|evals\|pydantic\|inspect" README.md DESIGN.md` → 0 hits.
- **Observed:** v0.3 (`fb3f20d`) added `examples/pydantic-ai/` and v0.4
  (`afa7208`) added `evals/inspect/`. Both have well-written local
  READMEs (~80 lines each, with install + run + interpretation
  sections). Neither is linked from the top-level README or DESIGN. A
  contributor who clones the repo and reads README sees the language
  table and the dogfood-wiring example, but not the eval that
  measures fabrication-reduction (the most marketing-ready artifact
  the project has) or the Pydantic AI integration (the only
  documented non-Claude-Code use case).
- **Expected:** the top-level README has a §"Demos and evals" section,
  or at minimum a sentence under §"Components" linking to both
  subdirectories.
- **Suggested fix shape:** add three lines to README — one section
  header, two markdown links. No code change.

### F11 — Binary size delta with `-tags otel` is large and asymmetric

- **Severity:** informational
- **Reproducer:**
  ```bash
  for tag in "" "-tags otel"; do
    for b in leonard leonard-hook leonard-mcp; do
      go build $tag -o /tmp/$b-${tag:-default} ./cmd/$b
      printf "%-30s %s\n" "$b ${tag:-default}" \
        "$(awk 'BEGIN{printf "%.1f MB", '$(stat -f%z /tmp/$b-${tag:-default})'/1024/1024}')"
    done
  done
  ```
  Results:
  ```
  leonard default                11.1 MB
  leonard -tags otel             11.1 MB    ← identical
  leonard-hook default           11.3 MB
  leonard-hook -tags otel        26.3 MB    ← +15.0 MB (+133%)
  leonard-mcp default            13.7 MB
  leonard-mcp -tags otel         13.7 MB    ← identical
  ```
- **Observed:** the otel build more than doubles the hook binary's
  size. The CLI and MCP binaries are byte-identical with/without the
  tag (F4 cross-reference). For a hook binary that fires on every
  Edit/Write, 15 MB of dependencies — most of which is the OTLP HTTP
  transport, the OpenTelemetry SDK, and protobuf — is on the high
  side. Default install stays clean (0 MB delta).
- **Expected:** size delta is documented somewhere (it isn't), or the
  hook binary is built with the lighter `stdout` exporter only as the
  default otel build, with `otlp` as an optional sub-tag.
- **Suggested fix shape:** add a "Cost" subsection to README's
  §"Telemetry" noting the 15 MB binary delta. Optionally: split into
  `-tags otel,otlp` vs `-tags otel,stdout` so users who only want
  stdout debug spans pay less.
- **Out of scope for this investigation:** measuring per-event
  latency overhead under load (bughunt-3-otel lane's territory).

### F12 — Pure-Go binary story still holds (positive)

- **Severity:** informational (carry-over verification)
- **Reproducer:**
  ```bash
  CGO_ENABLED=0 GOBIN=/tmp/cgooff go install ./cmd/...
  ls /tmp/cgooff/
  go list -deps -json ./cmd/... | jq -s '[.[] | select(.CgoFiles or .CFiles)] | length'
  ```
- **Observed:** All three binaries build under `CGO_ENABLED=0`; the
  CGO-files count is 0. Including v0.6 OTel and v0.7 batched
  DeleteFiles, the pure-Go promise is intact. (Verified independently
  in bughunt-2 integration "Things that worked"; re-verified here
  after v0.6/v0.7 changes.)
- **Suggested fix shape:** none — keep it that way.

## Things that worked

- **`go test ./...`, `go test -tags otel ./...`, `go test -race
  ./...`** all clean. No new test failures in v0.3-v0.7. No races on
  the new code paths.
- **`CGO_ENABLED=0 go install ./cmd/...`** still produces three
  working pure-Go binaries (F12).
- **MCP `tools/list` matches README's advertised surface.** Live
  `tools/list` against the v0.7.0 binary returns the same 10 tools
  README §"Components" lists. (Note: server version string is now
  `0.7.0` — verified `cmd/leonard-mcp/main.go:24` and
  `internal/mcp/server.go:19` are both updated.)
- **v0.7 batched prune under concurrent storm.** 50 concurrent
  `leonard-hook post-edit` invocations + 2 concurrent `leonard
  index` invocations against the same DB:
  ```
  exit codes: 0 (all)
  files in DB: 28 (30 created, 2 deleted)
  symbols: 28
  claims: 50
  ```
  Zero `database is locked` errors. Two simultaneous prune sweeps
  don't conflict — the second tx sees the post-first-tx state, no
  rows resurrect. WAL + busy_timeout + the v0.7 single-tx batching
  is correct under contention.
- **Theme A dead config really is gone.**
  `internal/config/config.go` is the only consumer; the struct now
  has only `Hooks.InjectDecisionsAtSessionStart` and
  `Hooks.SurfaceUnverifiedClaimsAtStop`. `grep -rn 'pythonInterpreter\|BlockOnFabricatedSymbol\|LEONARD_PYTHON' internal/`
  returns sensible hits (the env var IS read in v0.2's
  `internal/parse/python.go`; the block-on-fabricated and python-
  interpreter constants are gone). Fresh `leonard init` writes a
  config matching the trimmed struct.
- **OTel default-off promise holds.** `leonard-hook` (default build)
  imports `internal/telemetry/noop.go`; `Span()` is a 0-op closure.
  Binary size is unchanged from the pre-v0.6 baseline +
  `internal/telemetry`'s ~3 KB of source. No OTel SDK in the default
  binary. (Cross-validated by bughunt-3-otel lane.)
- **Rust extractor graceful-failure path.** Without the helper binary
  available on PATH or in the source tree, indexing a `.rs` file
  surfaces a single clear ParseFailure:
  ```
  test.rs: parse: leonard-extract-rust binary not found (build via
    `cargo build --release` inside internal/parse/rust/, set
    LEONARD_RUST_EXTRACTOR, or install the binary on PATH)
  ```
  Error message names the three remediation options. No panic, no
  crash, no silent miss. (This is the best error message in the
  codebase, frankly.)
- **`leonard init` post-bughunt-2 idempotency** works:
  ```bash
  leonard init . && leonard init .   # second run preserves config.toml
  ```
  (Bughunt-2 cli F1 fix verified.)
- **`leonard index` post-bughunt-2 prune** works:
  ```bash
  echo 'package x' > tmp.go && leonard index
  rm tmp.go && leonard index
  sqlite3 .leonard/leonard.db "SELECT path FROM files WHERE path='tmp.go'"
  # empty
  ```
  (Bughunt-2 cli F2 fix verified, both for file-vanished and the
  v0.6.1/v0.7 skipped-component case.)
- **MCP server survives malformed JSON-RPC** (bughunt-2 mcp F1 fix
  verified — the stdin filter at `cmd/leonard-mcp/stdin_filter.go`
  drops non-JSON-RPC lines without crashing).
- **`leonard-mcp` clean shutdown on EOF / SIGINT** (bughunt-1 mcp F4
  fix verified — clean exit code 0).
- **Python LEONARD_PYTHON env var** is read by
  `internal/parse/python.go`; bughunt-2 python F1 fix landed.
- **Hook subprocess timeout** in python parser stops hung interpreters
  (bughunt-2 python F2 fix landed via context with cancel).
- **`examples/pydantic-ai/` and `evals/inspect/` index correctly.**
  v0.6.1's `defaultSkipDirs` covers `__pycache__` and `.venv` — both
  dirs have pycache content present, neither pollutes the index:
  ```bash
  sqlite3 .leonard/leonard.db "SELECT COUNT(*) FROM files WHERE path LIKE '%__pycache__%' OR path LIKE '%.venv%'"
  # 0
  ```
  Only the four legitimate Python source files in the two
  subdirectories appear in the index.
- **`internal/parse/rust/target/`** (88 MB of Rust build artifacts) is
  similarly excluded from the index — `defaultSkipDirs` has `target`.

## Open questions

- **DESIGN.md role going forward.** Should it stay as architectural
  doc that diverges from current code (in which case the drift is
  fine, just document it as "this is the v0.1 architecture spec, see
  README for what shipped") or get rewritten to match v0.7? Bughunt-2
  triage recommended the former; nobody has done either.
- **OTel coverage scope.** Round-3 found the build instruments only
  `leonard-hook`. Decision needed: extend instrumentation to MCP and
  CLI, OR scope README's promise back to "hook-only." Either is
  fine; the current state (README implies all three, only one works)
  is the only bad state.
- **Subprocess test investment.** Bughunt-1 mcp lane and the various
  bughunt scratch harnesses re-implement the same "boot leonard-mcp,
  pipe JSON-RPC" loop. Promoting one of those into
  `cmd/leonard-mcp/stdio_test.go` would prevent the next round from
  needing yet another scratch harness. Is the cost (one test file,
  ~150 LOC, longer test runtime) worth the benefit? Bughunt-2 said
  yes; nothing landed.
- **Migration-test investment.** Same shape as above — bughunt-2
  flagged the absence of a prior-version DB test fixture, nothing
  landed, v0.7 added no migrations but v0.8 might. Trip wire is set,
  no detection yet.
- **Is the `.bughunt3-scratch/` dir checked in?** I see it in
  `git status` as untracked from another lane's scratch programs. If
  the round-3 conventions keep their scratch out of git, that's the
  right call; the dir name suggests it might leak in by accident.

## Out of scope for this investigation

- Subsystem-specific bugs covered by the other four hunt-3 lanes.
  Their respective files own:
  - Rust extractor specifics → bughunt-3-rust (file not yet
    present; lane in flight or not yet started).
  - Skip-dirs and prune internals → bughunt-3-skip-dirs-prune
    (already produced 10 findings I reference here).
  - OTel mechanism details → bughunt-3-otel (already produced 10
    findings I reference here).
  - Eval framework specifics → bughunt-3-eval-framework (file not
    yet present).
- Performance benchmarks beyond what `parse_bench_test.go`,
  `index_bench_test.go`, and `delete_files_test.go` already cover.
- OSS-readiness narrative (DESIGN §7 Q1), CONTRIBUTING.md, license
  headers, badges. Worth doing; not bug-hunt material.
- The `go.mod` toolchain directive (`go 1.25.0`) — no concerns raised
  by Round 1 or Round 2; bumping toolchain is a project-policy
  decision, not a bug.
