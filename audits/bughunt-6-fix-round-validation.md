# Bug Hunt #6 — fix-round-validation

## Summary

Validated each of the 25 fixes shipped across v0.39 → v0.45.1 by writing
reproducers that would fail pre-fix and pass post-fix. All 25 verifications
PASS. The fix round closed every original bughunt-5 finding it claimed to
close. The two prior "no-op fix" patterns (v0.9 Scanner, v0.16 supersede)
do **not** repeat here — the v0.39 supersede-by-vet_ok fix is functionally
correct, the v0.40 C++ in-class arm catches the exact `basic_json::dump()`
case from the LANG-F4 reproducer, etc.

Three minor follow-ups surfaced during the regression sweep — none rise to
HIGH; the highest is a LOW: stale doc-comments inside `internal/mcp/*.go`
still cite deleted `phase-{1,2,3}-brief.md` filenames. The new
"`internal` token = exported" heuristic mislabels Kotlin module-private
declarations as exported (semantic, not data-loss). The 4 MiB cap will
silently reject some legit-but-rare large single-file libraries (SQLite
amalgamation, big tree-sitter `parser.c`) — surfaced as a `ParseFailure`,
not a crash.

Test harness lives at `/tmp/bughunt6/` (per-fix per-subdir Leonard
projects, plus a cloned nlohmann/json for the dogfood case).

---

## Verification matrix

| # | Fix | Result | Notes |
|---|-----|--------|-------|
| 1 | v0.39 verifier F1 HIGH (`vet_ok=0` supersede) | PASS | bogus claim id=1 (vet_ok NULL) preserved; real failure id=2 superseded by winner id=3 |
| 2 | v0.39 integration F3 (handleMissingFile) | PASS | No claim row written; additionalContext present |
| 3 | v0.39 verifier F4 (negative timeout) | PASS | `-30s` rejected with stderr hint, falls back to 60s |
| 4 | v0.39 verifier F6 (`--note` 4 KiB cap) | PASS | 5000-byte note truncated; final evidence 4142 bytes with `…(truncated)` marker |
| 5 | v0.39 verifier F11 (whitespace command) | PASS | `command = " "` falls through to default `go vet` path |
| 6 | v0.40 C++ inline methods | PASS | nlohmann/json: `verify dump` returns 3 matches incl. `basic_json::dump()` |
| 7 | v0.41 SQL column collision | PASS | `schema.users.id` and `schema.posts.id` are distinct |
| 8 | v0.41 GraphQL field parent-fold | PASS | `Query.user`, `Query.post`, `User.id`, `User.name` |
| 9 | v0.41 Proto RPC parent-fold | PASS | `Users.Get`, `Users.Set` |
| 10 | v0.41 Java regression | PASS | `Foo.Foo.bar` / `Foo.Foo.baz` still produced |
| 11 | v0.42 Vue F1 (`</script>` in string) | PASS | Scanner respects string state; body not truncated |
| 12 | v0.42 Vue F2 (`<script>` in HTML comment) | PASS | Commented `<script>` not extracted |
| 13 | v0.42 Astro F30 (`\n---\n` in template literal) | PASS | Frontmatter not truncated; `hello` extracted from body |
| 14 | v0.42 package.json F3 (object-form dep) | PASS | `react`, `foo` (object), `bar` all kept as `kind=dependency` |
| 15 | v0.43 UTF-8 F1 (invalid \xff) | PASS | Helper exits 2 with `{"error":"ParseError","detail":"source is not valid UTF-8 at byte N"}` |
| 16 | v0.43 HCL F2 (multi-label resource) | PASS | `resource "aws_instance" "web" {}` produces one symbol named `aws_instance` |
| 17 | v0.43 C# `protected internal` | PASS | exported=1 |
| 18 | v0.43 lang F3 (Ruby/Java defaults) | PASS | Ruby unmodified `bar`: exported=1; Java unmodified `bar`: exported=0 |
| 19 | v0.44 F1 (4 MiB cap) | PASS | 5.0 MB Java file rejected as ParseFailure |
| 20 | v0.44 F2 (claim query cap) | PASS | 5000 claims → 1000 returned |
| 21 | v0.44 F9 (manifest dep kind) | PASS | All package.json deps have `kind=dependency` |
| 22 | v0.45.1 B4/B5 (`--version`) | PASS | All three binaries report `0.45.1` |
| 23 | v0.45.1 B3 (README links) | PASS | All 6 audit links in README resolve under `audits/` |
| 24 | v0.45.1 B6 (`.leonard/config.toml`) | PASS | Template + `init`-generated files both contain only `[hooks]` + `[post_edit.verify]`; no `[index]`/`[verifiers]`/`block_on_fabricated_symbol` |
| 25 | v0.45.1 CHANGELOG accuracy | PASS | CHANGELOG entry matches commit body — B3, B4+B5, B6, S1+S2, S5, S7 (exactly 6 items, all present in `git show v0.45.1`) |

All 25 PASS.

---

## Findings (NEW regressions / smells surfaced during validation)

### F1 — Kotlin `internal` mislabeled as exported

- **Severity:** low (semantic, not data-loss)
- **Reproducer:** `/tmp/bughunt6/ktproj/internal.kt`
  ```kotlin
  internal class Hidden {
      fun foo(): Int = 1
  }
  class Public {
      internal fun bar(): Int = 1
  }
  ```
  Index then `SELECT name, exported FROM symbols`:
  ```
  Hidden       exported=1
  Hidden.foo   exported=1
  Public.bar   exported=1
  ```
- **Observed:** `internal class` and `internal fun` are flagged exported=1.
- **Expected:** Kotlin's `internal` means module-private — not public API.
  The post-v0.43 word-tokenized `is_exported` treats `internal` as public
  to handle C#'s `protected internal` (which IS more permissive than
  `protected`). The same token in Kotlin/Scala/Swift means the opposite.
- **Suggested fix shape:** Either add a per-language opt-out flag
  (`treat_internal_as_public`) on the `Language` struct, defaulting true
  for C# and false elsewhere; or move the modifier-to-exported mapping
  into a per-language table instead of the cross-language match arm at
  `internal/parse/treesitter/src/main.rs:1178-1185`.
- **Out of scope:** Scala `private[package]` was tested separately and
  parses correctly as private (exported=0), because the `[package]`
  qualifier doesn't reach the modifier text. Only the bare `internal`
  keyword is affected.

### F2 — `internal/mcp/*.go` still cites deleted `phase-{1,2,3}-brief.md`

- **Severity:** low (stale doc-comments)
- **Reproducer:**
  ```bash
  grep -rn "phase-[123]-brief" internal/mcp/
  ```
  Five hits:
  - `internal/mcp/changes.go:11`
  - `internal/mcp/handlers.go:4`
  - `internal/mcp/store.go:7`
  - `internal/mcp/decisions.go:14`
  - `internal/mcp/claims.go:14`
- **Observed:** Doc comments reference filenames the v0.45.1 commit
  deleted (`phase-1-brief.md`, `phase-2-brief.md`, `phase-3-brief.md`).
- **Expected:** Either move the briefs to `audits/phase-*` and re-link,
  or update the comments to point at `DESIGN.md §4.x` (the comments
  already cite DESIGN.md for the same content).
- **Suggested fix shape:** s/phase-N-brief.md/DESIGN.md §4.x/ across the
  five files. The DESIGN.md section pins are already there alongside; the
  brief reference is redundant once the briefs are gone.
- **Out of scope:** No reference in cmd/, internal/store/, internal/hooks/,
  internal/index/, or internal/config/.

### F3 — 4 MiB cap rejects some legit large single-file libraries

- **Severity:** informational
- **Reproducer:** Any source file >4 MiB. Real-world examples:
  - SQLite amalgamation `sqlite3.c` (~8.6 MB)
  - Generated `parser.c` from large tree-sitter grammars (e.g. tree-sitter-typescript: ~5 MB)
  - Some single-file C++ amalgamations (DuckDB ~10 MB)
  - Minified JS bundles checked into `_dist/` (legitimate cases exist for some libraries that ship a pre-built bundle alongside source)
- **Observed:** These files are surfaced as a `ParseFailure` with message
  `file size <N> bytes exceeds 4194304 byte indexing cap` rather than
  indexed.
- **Expected:** The bughunt-5 perf F1 motivation (4 MiB × ~100× helper
  RSS amplification = ~400 MB worst case) is real and justified — this
  isn't a bug, just a known trade-off. Worth documenting in DESIGN.md /
  README so users running Leonard on a project that vendors SQLite or
  a large generated parser know why their `verify_symbol("sqlite3_open")`
  returns no_match.
- **Suggested fix shape:** Two options:
  1. **Doc only:** add a "Known limitations" section to README listing
     the 4 MiB cap and naming the typical victims.
  2. **Surface in `leonard doctor`:** dedicate a check that walks the
     project, finds files >4 MiB, and reports them with their language
     so the operator sees "vendored sqlite3.c is too big to index" at
     diagnosis time instead of after a failed verify.
- **Out of scope:** Not reproduced against a specific real project that
  Leonard is currently dogfooded against — leonard's own tree has no
  source file >1 MB. The risk is theoretical-but-non-zero.

---

## Things that worked (regression-free)

Cross-confirmed during validation:

- **post-edit happy path** still records a claim with `vet_ok=1`,
  re-indexes the touched file, and updates the symbols table.
- **Existing parent-folding** for Java, Kotlin (nested classes), Python
  (`class_definition`), C++ (`class_specifier`), Solidity
  (`contract_declaration`), Scala — all still produce the expected
  `module.Container.method` qnames after the v0.41 rewrite.
- **C++ out-of-class method definitions** (`int Foo::baz() {…}`) still
  produce a `function` symbol at the namespace level alongside the new
  `method` symbol — the v0.40 addition is additive, not replacement.
- **Proto, GraphQL, SQL** all gained correct parent-folding without
  breaking the file-level types they emitted in v0.40 and earlier.
- **Vue/Astro/Svelte** preprocessor symbols still emit with correct line
  offsets after the v0.42 scanner rewrite. Regex literals containing
  `</script>` (an additional edge case beyond the brief's string + comment
  cases) are also handled correctly — script body not truncated.
- **TypeScript/Python** extractors (Go-side, not Rust-side) untouched and
  still pass their existing test suites.
- **Manifest dep extractor** (v0.36) — `package.json` object-form deps
  no longer nuke the file, AND the dep `kind` flipped from `const` to
  `dependency` cleanly. No `const`-shaped dep rows left in the index
  after a re-index.
- **`leonard --version` / `leonard-hook --version`** via cobra's
  `Version` field; **`leonard-mcp --version`** via the early-argv check
  before the MCP run loop (cobra isn't on that path). All three report
  `0.45.1`. The early-argv check is at `cmd/leonard-mcp/main.go` — also
  honors `-v` shorthand alongside `--version`.
- **`go test ./...`** all 10 packages green at HEAD (v0.45.1).
- **`go vet ./...`** clean at HEAD.

---

## Open questions

- **Kotlin `internal` semantic mismatch (F1):** is the
  visibility-modifier-as-public-token heuristic intentional for "best
  effort" purposes (i.e. Leonard's job is "does the symbol exist", not
  "is it public API"), or is the export bit meant to drive a meaningful
  caller-facing filter? Affects whether F1 is fixed or wontfix.
- **Tree-sitter helper RSS amplification (F3 / v0.44 F1 context):** the
  v0.44 commit message cites a 7.8 MiB Ruby file → 839 MB RSS measurement.
  Was that on the just-fixed helper, or pre-fix? If the amplification is
  still ~100×, even the new 4 MiB cap allows ~400 MB worst case — fine
  on dev laptops, not great inside CI containers with `--memory 512m`.
  No reproducer attempted in this lane; flag for the next perf round.
- **Stale phase-brief comments (F2):** unclear whether the original
  comments were authored as "the canonical schema source" or as
  "for additional context". If the latter, deletion is fine; if the
  former, the schema should be promoted into DESIGN.md before deleting
  the cross-reference.

---

## Scratch fixtures (kept under `/tmp/bughunt6/`)

Each directory is a self-contained `.leonard`-initialized Leonard
project containing the minimal fixture for one fix:

- `proj/` — F1 + F3 + F4 + F11 (Go project for the verifier surface)
- `cppmini/`, `cppnested/` — F1 minimal + namespace fold check
- `json/` — full nlohmann/json clone (LANG-F4 dogfood)
- `sqlproj/`, `gqlproj/`, `protoproj/` — Theme B parent-folding cases
- `javaproj/`, `ktproj/`, `csproj/`, `rubyproj/`, `scalaproj/`, `solproj/` — exported defaults + regressions
- `vueproj/`, `astroproj/` — Theme C preprocessor cases (F1, F2, F30,
  plus an extra regex-literal probe)
- `pkgproj/` — package.json F3 + F9
- `utf8proj/`, `bad.rs` — Theme D UTF-8 invalid byte
- `bigfile/` — Theme F 5 MB Java cap rejection
- `hclproj/` — Theme D HCL multi-label
- `fresh/` — clean `leonard init` for the config.toml template check

None of these write into the leonard repo. Safe to `rm -rf /tmp/bughunt6/`
after the fix-round consumer is done with them.
