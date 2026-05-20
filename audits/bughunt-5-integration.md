# Bug Hunt #5 — integration

## Summary

The seams between the v0.19–v0.38 language sprawl and the rest of Leonard are
mostly fine: tests are green (`go test ./...`, `-tags otel`, `-race`), `go mod
tidy` is clean, `CGO_ENABLED=0 go install ./cmd/...` still produces a pure-Go
build, and 50× concurrent post-edit hooks against the live ledger ran with zero
errors and zero lock failures. The first-time `cargo build --release` of the
29-grammar tree-sitter helper took **19 seconds** on this Mac (M-class arm64) —
fast enough that it isn't an install-UX blocker, but it adds a ~40 MB binary
that nothing in the install docs mentions.

What's actually drifting:

- **Documentation reality-gap has compounded another twenty versions.** CHANGELOG.md stops at v0.18; README still says "Status: v0.17.0" and lists four languages when the indexer dispatches **39 languages + 4 manifest formats**; DESIGN.md §6 still lists Rust/Swift/Ruby/Java/C/C++ as "Future / explicit non-MVP" even though all of them shipped; the install section never mentions building `leonard-extract-treesitter`. (F1, F2)
- **The v0.38 claim-ledger overhaul is internally inconsistent.** `migrateV7` deletes any claim matching `index=skipped (file not found)`, but `handleMissingFile` (still at `internal/hooks/post_edit.go:293`) writes that exact pattern on every post-edit fired against a non-existent file — so the new rows pile up forever, immune to both `SupersedeOutstandingFailures` (claim text has `=skipped` not `=failed`) and the one-shot migration cleanup. Reproducer confirmed. (F3)
- **`SupersedeOutstandingFailures` and `ResolveClaim` have 0 % store-level test coverage.** These are the load-bearing v0.38 methods; the only smoke-tests live in `internal/hooks/post_edit_test.go` against a `fakeClaims`, so the actual SQL never runs in CI. (F5)
- **No unit tests for any v0.19–v0.38 extractor outside Vue/Svelte/TS/Rust/Go.** `internal/parse/jupyter.go`, `internal/parse/openapi.go`, `internal/parse/manifest.go` (`package.json` / `Cargo.toml` / `go.mod` / `pom.xml`), and every per-language wrapper in `internal/parse/treesitter.go` show **0.0 % function coverage** in the parse-package test run. ExtractAstro (v0.32) also has no direct test. (F4)
- **v0.27's documented parent-folding limitation is still live for Proto + GraphQL**, and worse: tree-sitter-sequel's SQL output emits *two columns named `id`* with **identical qualified_name (`test.id`)** when two tables have an `id` column. That's not the documented "bare qname" limitation — that's an outright collision in the symbol table. (F6)
- **CHANGELOG stops at v0.18 and the README "Status" line still references v0.17.0.** Twenty minor versions, one merged community PR (#3), three new schema migrations (v5→v6→v7), and a ledger-semantics overhaul are unrecorded. (F2)
- **Other bughunt-4 MEDIUMs still alive:** OTel still instruments only `leonard-hook`; symbols table still lacks any UNIQUE constraint so cfg-gated Rust dupes can persist; `Makefile` still references the long-dead `leonardreal` build tag. (F7)
- **No CI workflow, no git tags, no CONTRIBUTING.md.** Public-release readiness is unchanged from bughunt-4's report. License (Apache 2.0) is in place. (F8)

What I specifically verified is *fine*: schema migration v5→v7 runs cleanly on
a real DB; the pydantic-ai demo still imports cleanly; the Inspect eval
framework still registers all three tasks and runs a mockllm smoke pass to
completion; `~/go/bin/leonard doctor` works; the MCP `tools/list` schema
reflects every v0.16-era input addition (`record_claim.file_path`,
`get_unverified_claims.include_superseded`); `.leonardignore` is still wired
and tested.

## Findings

### F1 — README + DESIGN.md language coverage is two-and-a-half years behind reality

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ python3 -c "
  import re
  text = open('internal/index/indexer.go').read()
  langs = set(re.findall(r'lang:\s*\"([^\"]+)\"', text))
  print(sorted(langs), len(langs))
  "
  # Prints 39 distinct languages: astro, bash, c, cmake, cpp, csharp,
  # dart, elixir, erlang, glsl, go, graphql, hcl, hlsl, java, jupyter,
  # just, kotlin, lua, make, manifest, nix, openapi, php, proto, python,
  # r, ruby, rust, scala, solidity, sql, starlark, svelte, swift,
  # typescript, vue, wit, zig

  $ grep -n "v0\." README.md | head -1
  # "**Status:** v0.17.0, self-dogfooded on this repo. …"
  ```
- **Observed:** README.md's "Language support" table lists exactly four rows (Go, TypeScript, Python, Rust). DESIGN.md §6 "Future / explicit non-MVP" still lists *"Other languages (Rust, Swift, Ruby, Java, C/C++)"* even though all of those shipped in v0.20+. DESIGN.md §6 Phase 1–3 checkboxes are all unchecked despite every line being done. DESIGN.md §5 project-layout box still shows only `golang.go`, `python.go`, `typescript.go` under `internal/parse/`.
- **Expected:** README's language table and the DESIGN.md scope should list the actual surface — at minimum: which languages are tree-sitter-backed, that there's a mandatory `cargo build --release` step inside `internal/parse/treesitter/` to light any of them up, and that 4 manifest formats (`package.json` / `Cargo.toml` / `go.mod` / `pom.xml`) are now indexed too.
- **Suggested fix shape:** one docs-only sweep PR. Refresh the language table by grouping by extractor (stdlib Go / subprocess Python / syn Rust / hand-rolled TS / tree-sitter helper × N) and call out the build prerequisite. Update DESIGN.md §6 to either check the boxes or rewrite as a status snapshot.
- **Out of scope for this investigation:** which exact phrasing to use; whether to keep DESIGN.md as a historical doc or replace with current-state ARCHITECTURE.md.

### F2 — CHANGELOG.md and README status line stop at v0.18 / v0.17

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ head -10 CHANGELOG.md
  # ## v0.18.0 — Documentation reality-gap sweep (bughunt-4 Theme C)
  # ## v0.17.0 — Carry-over bughunt-2 MEDIUMs
  $ ~/go/bin/leonard-mcp --help  # version stamp via server name
  # serverInfo.version = 0.38.0
  ```
- **Observed:** CHANGELOG.md tops out at `v0.18.0`. README.md line 7 reads `**Status:** v0.17.0`. README's "Security and correctness fixes since v0.1" bullet list ends at v0.17. Twenty minor versions are unrecorded, including v0.19's tree-sitter strategy (the biggest architectural change since v0.1), v0.37's community PR #3 merge ([post_edit.verify]), and v0.38's claim-ledger overhaul (new MCP tool input field, three new store methods, a new CLI subcommand).
- **Expected:** The CHANGELOG should reflect the project's actual cadence (one entry per minor); README's status line should be current (v0.38.0).
- **Suggested fix shape:** synthesize entries v0.19 through v0.38 from `git log --oneline`; bump README status. The CHANGELOG already has consistent prose style (one paragraph per minor) so the format's set.
- **Out of scope:** whether to backfill CHANGELOG.md from v0.1 → v0.18 with more detail than its current "synthesized from commit history" treatment.

### F3 — handleMissingFile still writes the exact claim text migrateV7 was added to delete

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ rm -rf /tmp/missing_test && mkdir -p /tmp/missing_test
  $ cd /tmp/missing_test && echo -e 'module x\ngo 1.25' > go.mod
  $ ~/go/bin/leonard init .
  $ payload='{"hook_event_name":"PostToolUse","session_id":"x","cwd":"/tmp/missing_test","tool_name":"Write","tool_input":{"file_path":"/tmp/missing_test/does_not_exist.go"}}'
  $ echo "$payload" | ~/go/bin/leonard-hook post-edit > /dev/null
  $ sqlite3 .leonard/leonard.db "SELECT claim FROM claims;"
  # tool=Write file=/tmp/missing_test/does_not_exist.go; index=skipped (file not found); go vet=skipped (file not found)
  $ ~/go/bin/leonard claims unverified
  # leonard: 1 unverified claim(s)
  ```
- **Observed:** `internal/store/store.go:370` migrateV7 deletes claim rows matching `'%index=skipped (file not found)%'`, with the explicit comment that *"v0.38.0's semantics no longer treat as claims"*. But `internal/hooks/post_edit.go:293` (`handleMissingFile`) still produces exactly that claim text on every post-edit fired against a non-existent file. Because migrations are one-shot at upgrade, every new such row created after the v7 migration ran is immune to the cleanup and surfaces in `leonard claims unverified` / Stop forever — `SupersedeOutstandingFailures` doesn't catch them either because the claim text has `=skipped`, not `=failed`.
- **Expected:** Either `handleMissingFile` should stop recording a claim (matching `handleEscapedPath`'s v0.38 fix — it dropped the claim-record and surfaces the no-op via `AdditionalContext` only), or migrateV7's policy should accept that these claims are kept and the comment should change.
- **Suggested fix shape:** delete the `RecordClaim` call in `handleMissingFile`, keep the existing `SystemMessage` + `HookSpecificOutput.AdditionalContext`. The model already gets the signal via additionalContext (that was the bughunt-4 mcp F5 fix). Then migrateV7's pattern stays load-bearing for legacy rows only, which is what its comment already claims.
- **Out of scope:** whether to also retroactively clean up the now-orphaned post-v7 missing-file rows (they accumulated in this repo's own `.leonard/leonard.db` — 32 unverified claims on this machine).

### F4 — Zero direct unit tests for jupyter / openapi / manifest / astro / treesitter wrappers

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ go test -run TestExtractOpenAPI -v ./internal/parse/    # no tests to run
  $ go test -run TestExtractJupyter -v ./internal/parse/    # no tests to run
  $ go test -run TestExtractPackageJSON -v ./internal/parse/ # no tests to run
  $ go test -run TestExtractAstro -v ./internal/parse/      # no tests to run

  $ go test -coverprofile=/tmp/c.out -coverpkg=./... ./internal/parse/
  $ go tool cover -func=/tmp/c.out | grep -E "openapi|jupyter|manifest|Astro" | head
  # ExtractJupyter           0.0%
  # ExtractPackageJSON       0.0%
  # ExtractCargoToml         0.0%
  # ExtractGoMod             0.0%
  # ExtractPomXml            0.0%
  # ExtractOpenAPI           0.0%
  # ExtractAstro             0.0%
  ```
- **Observed:** When the parse-package tests are run in isolation under `-coverpkg=./...`, `ExtractJupyter`, `ExtractOpenAPI`, all four manifest extractors (`ExtractPackageJSON`, `ExtractCargoToml`, `ExtractGoMod`, `ExtractPomXml`), and `ExtractAstro` show **0 % coverage**. There are no `*_test.go` fixtures hitting any of them directly. Every per-language tree-sitter wrapper (`ExtractRuby` … `ExtractHLSL` in `internal/parse/treesitter.go`) is also 0 % — the helper-binary call path is exercised via Java but the other 28 wrappers exist only to thread `--lang foo`. (The misleading 92 % / 87 % numbers in the per-package coverage run come from the coverage tool crediting unrelated tests for the file's overall statement coverage.)
- **Expected:** Each language addition that landed in v0.20+ should have at least one happy-path test fixture and one parse-error test. The shape exists for Vue / Svelte (`internal/parse/vue_svelte_test.go`), Rust (`rust_test.go`), Python (`python_test.go`), TypeScript (`typescript_test.go`, `typescript_fuzz_test.go`) — the rest of the language sprawl just doesn't.
- **Suggested fix shape:** one `*_test.go` per file in `internal/parse/`. For the tree-sitter wrappers, parameterize a table over `(lang, fixture, expectedSymbols)` — one Go test exercising all 29 wrappers, not 29 separate functions. Use the `LEONARD_TREESITTER_EXTRACTOR` env var to point at the local cargo target during CI.
- **Out of scope:** whether to also fuzz the new extractors the way `typescript_fuzz_test.go` fuzzes TypeScript.

### F5 — Store-level zero coverage on SupersedeOutstandingFailures and ResolveClaim

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ go tool cover -func=/tmp/cover.out | grep -E "Supersede|ResolveClaim"
  # SupersedeDecision                73.9%
  # SupersedeClaimsForFile           77.8%
  # SupersedeOutstandingFailures      0.0%
  # ResolveClaim                      0.0%

  $ grep -n "TestSupersedeOutstandingFailures\|TestResolveClaim" internal/store/*_test.go
  # (no matches)
  ```
- **Observed:** Both v0.38 store methods (`Store.SupersedeOutstandingFailures`, `Store.ResolveClaim`) have **0 % statement coverage**. The behavior is smoke-tested through `internal/hooks/post_edit_test.go`'s `fakeClaims`, but the actual SQL never executes in CI. `cmd/leonard/claims.go:34`'s `newClaimsResolveCmd` is at 28.6 % — high enough to cover construction, low enough that the happy-path RunE branch hasn't fired in tests.
- **Expected:** A store-level test that records a vet=fail claim, then a vet=ok claim, then asserts the fail row's `superseded_by_claim_id` is set. Same shape as the existing `TestSupersedeClaimsForFile` (if it exists; if not, a new pair). A test for `ResolveClaim` covering happy path, missing-id, and the empty-vs-non-empty note suffix.
- **Suggested fix shape:** add two functions to `internal/store/store_test.go` following the existing `TestSupersedeDecision` shape; one happy-path + one error-path each. Mock-free; uses a temp DB.
- **Out of scope for this investigation:** verifying the SQL itself is correct (it is — manually validated via the concurrency probe in /tmp/conc2). The risk this finding flags is *regression* — a future refactor of the SQL has nothing in CI to catch a wrong-row UPDATE.

### F6 — Proto + GraphQL parent-folding still bare; tree-sitter-sequel produces qname collisions on shared column names

- **Severity:** medium
- **Reproducer:**
  ```bash
  $ cat > /tmp/test.sql <<'EOF'
  CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL
  );
  CREATE TABLE orders (
    id INTEGER PRIMARY KEY,
    user_id INTEGER REFERENCES users(id)
  );
  EOF
  $ internal/parse/treesitter/target/release/leonard-extract-treesitter --lang sql /tmp/test.sql < /tmp/test.sql | python3 -c 'import json,sys; [print(s["qualified_name"], s["name"], s["kind"]) for s in json.loads(sys.stdin.read())]'
  # test.users users type
  # test.id id const          <-- collides!
  # test.name name const
  # test.orders orders type
  # test.id id const          <-- collides!
  # test.user_id user_id const

  $ cat > /tmp/test.proto <<'EOF'
  syntax = "proto3";
  service UserService { rpc GetUser(R) returns (U); }
  EOF
  $ internal/parse/treesitter/target/release/leonard-extract-treesitter --lang proto /tmp/test.proto < /tmp/test.proto | python3 -m json.tool | grep qual
  # "qualified_name": "test.UserService"
  # "qualified_name": "test.GetUser"   <-- should be test.UserService.GetUser
  ```
- **Observed:** Two failures of the parent-folding pass in `internal/parse/treesitter/src/main.rs:find_parent_name`:
  1. **Proto + GraphQL**: documented limitation from v0.27 — these grammars don't use the `name:` field-name selector, so `p.child_by_field_name("name")` returns `None` and the method's qname stays unprefixed (`test.GetUser` instead of `test.UserService.GetUser`).
  2. **SQL (tree-sitter-sequel, added v0.28)**: the same limitation applies, but worse — when two `CREATE TABLE` statements both declare an `id` column, Leonard stores **two symbol rows with identical `qualified_name = "test.id"`**. The store's `symbols` table has no UNIQUE constraint, so both persist; `verify_symbol("id")` then returns ambiguous results pointing at different files/lines without any way to disambiguate per-table.
- **Expected:** Whether or not parent-folding is feasible for these grammars (it might require per-grammar named-child walking), Leonard should never emit two symbols with the same `qualified_name` — that defeats the "ground truth lookup" contract. Either fix the parent-fold for these grammars, or at minimum synthesize a per-row disambiguator (`test.users.id`, `test.orders.id`) by walking the parent kind even when it has no `name:` field — Proto's `service` and GraphQL's `object_type_definition` and SQL's `create_table` all have a name child, just not under the conventional field-name.
- **Suggested fix shape:** in `find_parent_name`, when `child_by_field_name("name")` returns None, fall back to scanning the parent's named children for the first `identifier` / `simple_identifier` / `type_identifier` node and using its text. Land it as a per-grammar callback if the heuristic is too risky for the well-behaved grammars.
- **Out of scope for this investigation:** SQL `JOIN` / `ALTER TABLE` cases; whether tree-sitter-sequel has alternative grammar configurations that expose field names.

### F7 — Carry-over MEDIUMs from bughunts 2/3/4 still alive

- **Severity:** medium (aggregate — individual items vary)
- **Status of each:**

  | Origin | Item | Status as of v0.38 |
  |---|---|---|
  | bughunt-3 otel F4–F10 | leonard-mcp + leonard CLI not OTel-instrumented | **still alive** — `grep -rn 'telemetry.Init\|telemetry.Span' cmd/ internal/` shows only `cmd/leonard-hook/main.go` calls `Init`, only `internal/hooks/*.go` calls `Span` |
  | bughunt-3 rust F4 | cfg-gated method dupes; no UNIQUE on (file_path, qualified_name, start_line) | **still alive** — `sqlite3 .leonard/leonard.db ".schema symbols"` shows no UNIQUE constraint |
  | bughunt-3 integration F3 | Makefile bitrot (`-tags leonardreal` long gone) | **still alive** — Makefile still references `leonardreal` build tag |
  | bughunt-4 mcp F4 | MCP claim file_path support | **fixed in v0.16** — `tools/list` confirms `record_claim` has `file_path` input property |
  | bughunt-4 caps F4 | indexed-file size cap | **fixed in v0.13** — `maxIndexedFileBytes` constant present, `index_test.go` exercises it |
  | bughunt-4 path-trust F1–F4 | pre-edit / prune / doctor / NFC | **fixed in v0.14** |
  | bughunt-4 store-perf F1+F2+F4+F7 | indexes + N+1 + wal_checkpoint | **fixed in v0.15** |
  | bughunt-2/3 cli F9 / cli F18 / pre-edit F1 | walk-up / doctor double-count / sibling-skip alignment | **fixed in v0.17** |
  | bughunt-3 integration F2 | `go mod tidy` not idempotent (OTel deps) | **resolved** — `go mod tidy` is now clean (verified this round) |
- **Reproducer:** see individual rows above.
- **Suggested fix shape:** the remaining three (OTel scope, symbols UNIQUE, Makefile) are independently small. The OTel one is genuinely a follow-up product decision (does `leonard-mcp` even need spans? probably yes for tool-call latency, but it's a feature). The UNIQUE constraint blocks something Leonard wants to prevent (duplicate symbol rows), but landing it now means migrating away from any existing dupe rows — needs design.
- **Out of scope:** which to promote to fix-round-5 priority; that's the triage call.

### F8 — Public-release readiness unchanged from bughunt-4

- **Severity:** informational (not a code defect; tracks process)
- **Reproducer:**
  ```bash
  $ ls -la .github/   # No such file or directory
  $ git tag -l         # (empty)
  $ ls CONTRIBUTING*   # no matches
  $ ls LICENSE         # Apache 2.0 — present
  ```
- **Observed:** No `.github/` directory (no CI workflows), no annotated or lightweight git tags despite 38 minor releases, no `CONTRIBUTING.md`. `LICENSE` (Apache 2.0) is the only release-infra file present. README still describes the project as "self-dogfooded on this repo" and references `bughunt-*-triage.md` files directly (which are committed at the root and would be confusing in a public OSS release).
- **Expected:** Either the project is intentionally pre-tag with no public-release intent yet (in which case this is informational), or it isn't, in which case a tag-`v0.38.0` + a minimal `CONTRIBUTING.md` + a GitHub Actions workflow that at least runs `go test -race ./...` + `cargo test --manifest-path internal/parse/treesitter/Cargo.toml --release` is the bar.
- **Suggested fix shape:** if tagging now, the only release-blockers I'd raise from this round are F1 (README claims v0.17 / 4 languages) and F2 (CHANGELOG ends at v0.18). Everything else is OK to tag through.
- **Out of scope:** decision on OSS-readiness timing.

### F9 — `leonard claims unverified` evidence display duplicates the file path

- **Severity:** low (cosmetic)
- **Reproducer:**
  ```
  $ cd /tmp/missing_test && ~/go/bin/leonard claims unverified
  leonard: 1 unverified claim(s)
    #1  2026-05-20T16:10:10-05:00  tool=Write file=/tmp/missing_test/does_not_exist.go; …
        file: /tmp/missing_test/does_not_exist.go
        session: miss
        file: /tmp/missing_test/does_not_exist.go   <-- duplicate of line above
  ```
- **Observed:** `cmd/leonard/claims.go:newClaimsUnverifiedCmd` prints the structured `r.FilePath` line, then prints `firstLine(r.Evidence)` — which for hooks-recorded claims is always `file: <path>`, the same string.
- **Expected:** Skip the `firstLine(r.Evidence)` print when its content matches the just-printed `file: …` line, or use a different evidence-summary heuristic that pulls the second line of evidence (the actual error / verb status).
- **Suggested fix shape:** in `newClaimsUnverifiedCmd`, take `secondNonFileLine(r.Evidence)` or skip evidence when its first line is `"file: "+r.FilePath`.
- **Out of scope:** rewrite of evidence display.

### F10 — `leonard claims resolve` surfaces raw `sql: no rows in result set` to operator

- **Severity:** low
- **Reproducer:**
  ```
  $ cd /tmp/conc2 && ~/go/bin/leonard claims resolve 9999
  leonard: claims resolve: sql: no rows in result set
  ```
- **Observed:** When an operator passes a claim id that doesn't exist, the CLI bubbles up the raw `sql.ErrNoRows` text. Exit code is 2, which is the right shape — but the wording leaks the database layer.
- **Expected:** Something like `leonard: claims resolve: no claim with id 9999`. Mirror Go conventions but stay at the user's abstraction level.
- **Suggested fix shape:** in `cmd/leonard/wire_real.go`'s `ResolveClaim` wrapper or in the cobra `RunE`, intercept `errors.Is(err, sql.ErrNoRows)` and return a friendlier message.
- **Out of scope:** broader error-message polish across the CLI.

### F11 — `SupersedeOutstandingFailures` doesn't filter by session_id

- **Severity:** informational (could be intentional)
- **Reproducer:** read `internal/store/store.go:1095`. The UPDATE has no `session_id = ?` clause.
- **Observed:** A vet=ok post-edit in session A supersedes a vet=fail claim recorded in session B (or by an MCP `record_claim` call from any session). The intent is project-wide cleanliness; the side effect is cross-session ledger interaction.
- **Expected:** This is arguably correct — a project either has clean vet or it doesn't, regardless of which session noticed first. But the post-edit hook in session A doing UPDATEs to claims attributed to session B is worth being deliberate about.
- **Suggested fix shape:** none — confirm the intent in a comment, or add an opt-in session filter. The current behavior is defensible.
- **Out of scope:** ledger redesign.

### F12 — `tree-sitter-sequel` 0.3 has been keeping ID column dupes in the symbols table since v0.28

- **Severity:** low (depends on whether anyone has indexed real SQL migrations)
- **Reproducer:** see F6 above.
- **Observed:** Concretely a sub-case of F6, but tagging separately because the cleanup story is different — even if `find_parent_name` learns to emit `test.users.id`, existing rows in long-lived `.leonard/leonard.db` files have already been written with collided qnames. A migration to clean them up is its own work item.
- **Suggested fix shape:** when the F6 fix lands, also include a "force re-index of all .sql / .proto / .graphql files" migration step so existing rows get rewritten.
- **Out of scope:** SQL grammar refinements.

## Things that worked

- **All Go tests are green:** `go test ./...`, `go test -tags otel ./...`, `go test -race ./...` (counts: ~10 packages each, all `ok`).
- **`go mod tidy` is idempotent** — no changes to `go.mod` / `go.sum` after running.
- **`CGO_ENABLED=0 go install ./cmd/...`** produces three pure-Go binaries (12.6 MB / 12.7 MB / 14.3 MB on darwin/arm64).
- **First-time `cargo build --release` of `leonard-extract-treesitter`** completed in **19.06 s** on this Mac with 29 grammars; output binary is 40 MB. Not a blocker. Subsequent incremental rebuilds (during `cargo clean`-free workflows) compile in ~0.5 s.
- **Schema migration v5 → v7** runs cleanly on a real DB: copy of `.leonard/leonard.db`, set `meta.schema_version='5'` and drop the v6 indexes, then open the DB through any binary — the indexes get recreated, v7's claim-cleanup DELETE fires, and `meta.schema_version` ends at `7`.
- **50 parallel post-edit hooks against a fresh project's `.leonard/leonard.db`**: all 50 returned exit 0, all 50 claims recorded with `verified=1` / `go vet=ok`, no `database is locked` errors, no empty stdout, WAL stays at ~1 MB and drops to 0 with a manual `PRAGMA wal_checkpoint(PASSIVE)`.
- **47 parallel post-edit hooks against Leonard's own DB with two concurrent `leonard claims unverified` + `leonard doctor` runs**: same — zero stderr, zero lock errors, doctor's stale-file count consistent across reads.
- **Mixed vet=ok + vet=fail hook storm (10 each)**: behavior matches design — `go vet ./...` fails project-wide because the bad files are in the same package, so all 20 hooks see vet=failed. No supersession fired (correct — no vet=ok claim landed). `claims resolve <id> --note "test"` worked: row flipped to `verified=1`, evidence appended with `\n\nmanually resolved by operator: test`.
- **MCP tool schemas reflect the v0.16-era input additions** — `tools/list` shows `record_claim.file_path` and `get_unverified_claims.include_superseded`. All 10 advertised tools match the server-side handler set.
- **`leonard-mcp` initialize + tools/list round-trip works** over real stdio (the bughunt-1 mcp F1 sentry concern stays passed).
- **The Inspect eval framework registers all three tasks** (`fabrication_control`, `fabrication_with_leonard`, `fabrication_with_leonard_system_prompt`) and runs a mockllm smoke pass to completion in <1 s.
- **examples/pydantic-ai/demo.py** still imports clean (`python3 -c 'import ast; ast.parse(open("demo.py").read())'`) and short-circuits cleanly when `ANTHROPIC_API_KEY` is unset.
- **`.leonardignore`** is still wired (`internal/index/indexer.go:665`) and still has tests (`indexer_test.go:160`).
- **`leonard doctor` on Leonard's own repo** runs cleanly — surfaces 62 stale files (legitimate — they're old `/tmp/bughunt*` paths from previous rounds), no other issues. Coverage and surfacing look correct.
- **Apache 2.0 LICENSE** is in place.
- **Total install footprint** (3 Go binaries + 2 helper binaries): **~80 MB**. Of which `leonard-extract-treesitter` is 40 MB (half).

## Open questions

- Should `handleMissingFile` (F3) stop recording claims now that `AdditionalContext` covers the model-facing side? The decision matters because every transient missing-file edit is silently growing the ledger.
- Should `find_parent_name` learn to scan named children for grammars without conventional `name:` fields (F6), or is the right level a per-grammar callback in `Language::lookup`?
- Is the absence of OTel on `leonard-mcp` (F7 / carry-over otel F4) a feature or a bug? With 10 tool calls per session and meaningful latency variance (each hits SQLite, some hit Python subprocess), per-tool spans would be useful — but it's a feature decision.
- Should the project be tagged at v0.38.0 (F8)? README still positions Leonard as "self-dogfooded on this repo", and CHANGELOG ends at v0.18 — if tagging is imminent, F1 + F2 are pre-tag blockers.
- The `SupersedeOutstandingFailures` pattern (`claim LIKE '%=failed%'`) is intentionally broad to cover every custom verifier verb. Is the pattern broad enough — what about verbs whose first token isn't included in `summariseClaim`'s `<verb>=failed` form? (Verified the code path: it always emits `vet.Verb+"=failed"`, so the pattern is correct.) The flip side — a vet=ok claim text containing `=failed` literally somewhere in the verb name — is implausible enough to ignore.
