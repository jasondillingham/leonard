# Leonard — Bug Hunt #5 Triage

> Five parallel lanes audited v0.19–v0.38 surfaces: the tree-sitter
> dispatcher, the 25+ added languages, the Vue/Svelte/Astro/Jupyter/
> OpenAPI/manifest preprocessors, the v0.37 post-edit verifier +
> v0.38 ledger hygiene, and perf/resource characteristics at scale.
> ~110 findings. This doc picks the fix-round-5 priorities.

## Decision

**Fix all 3 HIGH-severity findings, plus the v0.38 internal
inconsistency (integration F3), plus the parent-folding completeness
theme. Defer the language-by-language coverage gaps unless a
real-world Purser-style PR surfaces a specific one.**

Reasoning: the HIGHs found this round are all narrow + concrete +
reproducible. **The biggest cluster of risk is in the v0.38 ledger
work we just shipped** — `SupersedeOutstandingFailures` is too
broad, `migrateV7` deletes a pattern that `handleMissingFile` still
generates, and `ResolveClaim` bypasses the v0.13 evidence cap. We
shipped that lane two hours ago; it deserves a follow-up before it
accumulates use.

The language coverage gaps (C++ in-class methods, Lua qname
shape, GLSL primitive uniforms, etc.) are real but per-language
and unbounded. Better to address them when a Purser-shaped project
actually surfaces the friction; chasing them all would be a
multi-week sweep.

## Cross-cutting themes

### Theme A — v0.38 ledger correctness (URGENT, all critical)

| ID | What | Severity |
|---|---|---|
| verifier F1 | `SupersedeOutstandingFailures` `claim LIKE '%=failed%'` matches ANY claim text containing the substring. A user-recorded claim `"user_input=failed to load gracefully"` gets silently superseded by the next vet=ok. Reproduced. | **high** |
| integration F3 | `migrateV7` deletes claims matching `index=skipped (file not found)`, but `handleMissingFile` STILL writes that exact pattern on every missing-file post-edit. New rows accumulate forever — the cleanup is one-shot but the symptom regenerates. Also, the pattern `=skipped` doesn't match `SupersedeOutstandingFailures`' `=failed` filter, so they don't get superseded either. Both halves of the fix are broken on this code path. | medium |
| verifier F4 | `timeout = "-30s"` parses cleanly via `time.ParseDuration` (negative durations are valid), then silently breaks every verifier run with `context deadline exceeded`. No stderr hint. | medium |
| verifier F6 | `ResolveClaim` bypasses `MaxClaimEvidenceBytes` (256 KiB v0.13 cap). 100 KiB `--note` accepted with no truncation. | medium |
| verifier F11 | `command = " "` (whitespace-only) passes the `!= ""` gate and runs `sh -c " "` which always succeeds — silent permissive verifier. | low |

**Sweep:** the supersede needs a structural fix — use the existing
`vet_ok` integer column (added in migrateV3) or a dedicated
"verifier_failed" column instead of LIKE matching on claim text.
`handleMissingFile` should also stop recording claims (same logic
as v0.38's `handleEscapedPath` change — file-not-found is a
tool-layer decision, not an unverified work claim). Negative
timeouts should reject + stderr-hint. `ResolveClaim` should
truncate the note. Whitespace-only commands should reject.

### Theme B — Parent-folding completeness (per language)

Round 3 + 4 noted this as a documented limitation; round 5 shows
it's more pervasive than acknowledged:

| ID | What | Severity |
|---|---|---|
| treesitter F3 | parent-folding only triggers for kind `method|function` — `const|type|interface` captures collide silently | medium |
| languages F1 | basename module_name causes same-name files in different dirs to collide (28 tree-sitter languages share this) | medium |
| languages F2 | GraphQL field_definitions + Proto RPCs silently fail parent-fold (grammars use named-child instead of `name:` field) | medium |
| languages F9 | Lua silently drops M./M: table prefix from qnames | medium |
| integration F6 | tree-sitter-sequel SQL: two CREATE TABLEs with `id` columns both emit `qualified_name="test.id"` — no UNIQUE constraint on symbols catches this | medium |

**Sweep:** the unifying fix is in `find_parent_name` in main.rs.
Either:
1. Walk by node kind (use `tree-sitter::Query`'s ability to bind
   captures to ancestors), OR
2. Add a `parent_name_paths: &[(node_kind, child_path)]` to each
   `Language` so per-grammar conventions can be honored, OR
3. Add a UNIQUE constraint on `(file_path, qualified_name, kind)`
   in symbols at the store layer so collisions surface as errors
   rather than silent overwrites.

For v0.39 scope, #2 is the most-bang-for-the-buck — fixes 4 of the
5 issues in this theme.

### Theme C — Preprocessor regex robustness

| ID | What | Severity |
|---|---|---|
| preproc F1 | Vue/Svelte regex truncates on `</script>` inside string literals (`const html = "</script>";` ends the block early) | medium |
| preproc F2 | Vue/Svelte regex matches `<script>` inside HTML comments — phantom symbol risk | medium |
| preproc F3 | package.json parse fails entirely on any non-string dep value (object-form `{ "version": "..." }` nukes the file's symbols) | medium |
| preproc F30 | Astro frontmatter regex truncates on `\n---\n` inside a template literal — same root-cause as F1 | medium |

**Sweep:** the regex extractors need bounded-scope state machines
instead of `.*?` non-greedy matching. Replace `scriptBlockRe` +
`astroFrontmatterRe` with small hand-written parsers that track
quote/comment context. Larger change than the other themes but the
right fix. F3 needs `cargoVersion`-style polymorphic value handling
in package.json too.

### Theme D — Tree-sitter dispatcher polish

| ID | What | Severity |
|---|---|---|
| treesitter F1 | Invalid UTF-8 → exit 1 (infra failure) instead of exit 2 (parse error) | medium |
| treesitter F2 | HCL multi-label blocks emit duplicate symbols (contradicts source comment) | medium |
| treesitter F4 | `is_exported` substring matching: C# `protected internal` classifies as not-exported | low |
| treesitter F5 | Missing helper binary produces 1 ParseFailure PER FILE (1000 identical errors on a 1k-file project) | medium |
| languages F3 | `is_exported` wrong for every default-public language — Ruby methods, Swift methods, Zig pub, C extern, Solidity external all report exported:false | medium |

**Sweep:** read source as `Vec<u8>` instead of `String` to handle
invalid UTF-8; fix the HCL query to anchor first label; centralize
exported-detection into a per-language closure rather than the
shared substring search; cache the "helper not built" error
globally so it fires once per index, not once per file.

### Theme E — Documentation drift (carry-over from rounds 3/4)

| ID | What | Severity |
|---|---|---|
| integration F1 | README/DESIGN.md list 4 languages; actual is 39+4 manifest formats; DESIGN.md §6 still calls Rust/Swift/Ruby/Java/C/C++ "Future / explicit non-MVP" | medium |
| integration F2 | CHANGELOG.md stops at v0.18; README says "Status: v0.17.0"; binary stamps v0.38.0. Twenty versions unrecorded. | medium |
| integration F8 | No `.github/workflows/`, no git tags, no CONTRIBUTING.md (carry-over from bughunt-3) | informational |

**Sweep:** docs sweep PR. The biggest gap is CHANGELOG — 20 commits
to append. One coherent change; flag separately from
correctness/security fixes.

### Theme F — Perf at scale

| ID | What | Severity |
|---|---|---|
| perf F1 | 8 MB input → 839 MB RSS in helper subprocess (107× amplification). 8 MiB cap prevents worst case but the remaining ceiling is ~1 GB momentary per invocation | medium |
| perf F2 | `get_unverified_claims` on 500k-claim ledger = 1.15s — SQL has no LIMIT clause, full materialization in Go | medium |
| perf F4 | Indexer has no goroutine pool — 42× slowdown vs Go-only on a 10k-file polyglot project (~73 files/sec sequential vs ~3000 files/sec parallel-Go) | medium |
| perf F6 | WAL grows monotonically across hook invocations (11 MB after 700 sequential hooks) — wal_checkpoint only fires after big DELETEs | medium |
| perf F9 | Manifest symbol pollution unbounded — 200 workspaces × 35 deps = 7000 const symbols indistinguishable from real consts | medium |

**Sweep:** four independent fixes. F1 needs source-size pre-check
in the helper itself (the indexer's 8 MiB cap doesn't help if a
single Rust file happens to be 7.9 MB). F2 needs LIMIT in SQL +
parameter binding. F4 needs a worker pool — typical Go pattern,
~30 LoC. F6 needs periodic wal_checkpoint on long-running
processes. F9 needs a `kind="dependency"` so manifest symbols
don't pollute the `const` namespace.

### Theme G — Language-by-language coverage gaps (DEFERRED)

Round 5 found 32 language-specific issues across 28 languages. The
big one is **languages F4 (HIGH): C++ in-class inline methods are
entirely missed** — `nlohmann::basic_json::dump()` is invisible in
551 indexed files because the C++ query doesn't capture
`field_declaration` with inline body. This is the dominant idiom
in header-only C++.

Defer the bulk of these to a future "C++ refinement" PR. The fix
shape for each is small but the surface is huge; better to address
when a project hits the specific gap. Track in
`bughunt-5-languages.md` as the punch list.

The one to consider promoting: **languages F4**. It's HIGH and
header-only C++ is common enough that this surfaces immediately
for any C++ user. ~5 lines in the C++ query to capture
`field_declaration > function_declarator > body: compound_statement`.

## Lane summaries

### Lane A — `treesitter-dispatcher` (18 findings, file `bughunt-5-treesitter-dispatcher.md`)

**Solid:** per-process concurrency safety, context timeout, exit-2
ParseError handshake, BOM/CRLF/Unicode/NUL identifier handling, env
override precedence, no tree-sitter version split in Cargo.lock,
`_`-prefixed predicate capture filtering. Helper binary 38 MiB
arm64, ~7 ms cold subprocess.

### Lane B — `languages` (32 findings, file `bughunt-5-languages.md`)

| Severity | Count |
|---|---|
| high | 1 (C++ in-class inline methods missed) |
| medium | 13 (cross-cutting + per-language) |
| low | 11 |
| informational | 7 |

**Solid:** Java nested types, Ruby class/method parent-fold, PHP
modifiers, Elixir #any-of? predicate, Solidity grammar, WIT
func_items, CMake multi-line decls.

### Lane C — `preprocessors` (30+ findings, file `bughunt-5-preprocessors.md`)

Headline: Vue/Svelte regex truncates on `</script>` in strings;
Astro frontmatter regex same shape; package.json fails on
object-form deps.

**Solid:** Swagger petstore real-world test, UTF-8 multibyte line
offsets, Jupyter concurrent stateless calls, 8 MiB file cap,
defaultSkipDirs blocks node_modules.

### Lane D — `verifier-and-ledger` (20 findings, file `bughunt-5-verifier-and-ledger.md`)

| Severity | Count |
|---|---|
| high | 1 (SupersedeOutstandingFailures LIKE too broad) |
| medium | 5 (working_dir cwd-relative, no path-trust, negative-timeout, command-cap-bypass, ResolveClaim cap-bypass) |
| low | 5 |
| positive/informational | 9 |

**Solid:** concurrent supersede clean, migrateV7 idempotent, race
detector clean, escape-path no-claim change works as documented.

### Lane E — `integration` (12 findings, file `bughunt-5-integration.md`)

Key items: README/DESIGN drift (4 vs 39 languages), CHANGELOG 20
versions stale, **integration F3 v0.38 inconsistency**, missing
unit tests for jupyter/openapi/manifest, SupersedeOutstandingFailures
+ ResolveClaim at 0% direct coverage, public-release infra still
missing.

**Solid:** all Go tests green, `go mod tidy` clean, CGO_ENABLED=0
build still works, 50× concurrent post-edit hooks with 0 errors,
v5→v7 schema migration smoke-tested cleanly, manifest dep graph
end-to-end working.

### Lane F — `perf-and-resource` (21 findings, file `bughunt-5-perf-and-resource.md`)

| Headline | Number |
|---|---|
| Polyglot cold index throughput | ~73 files/sec (vs Go-only ~3000) |
| Tree-sitter helper cold latency | 2.7-37 ms |
| Helper RSS on 7.8 MB Ruby file | 839 MB (107× amplification) |
| get_unverified_claims on 500k ledger | 1.15s |
| SupersedeOutstandingFailures LIKE on 100k claims | 90 ms |
| WAL after 700 sequential hooks | 11 MB |
| Concurrent post-edit hooks | 100 → 303 ms, 0 errors |
| OTel build size delta | +124% |
| Helper binary | 40 MB |

**Solid:** all hooks ≤200 ms in steady state, race detector clean,
SQLite handles concurrency under WAL, manifest indexing works at
scale.

## What ships in fix-round-5

In priority order:

1. **Theme A — v0.38 ledger correctness** (HIGH + 3 MED). Highest
   urgency because we just shipped this code and it's actively
   misbehaving. Single PR: switch supersede to use the `vet_ok`
   column instead of LIKE, stop recording missing-file claims,
   reject negative timeouts, cap ResolveClaim note size.
2. **languages F4 (C++ in-class methods)** — HIGH; ~5 lines.
3. **Theme B — parent-folding completeness** (5 MEDs). Single PR:
   per-grammar `parent_name_paths` so each language's convention
   is honored. Also adds UNIQUE constraint to symbols table.
4. **Theme C — preprocessor robustness** (4 MEDs). Bigger PR;
   replace regex extractors with bounded-scope state machines for
   Vue/Svelte/Astro. package.json polymorphic value handling.
5. **Theme D — tree-sitter dispatcher polish** (5 items). UTF-8
   binary read, HCL anchor, per-language is_exported.
6. **Theme F partial — perf F1 + F2 + F9**. Per-file size cap in
   the helper, LIMIT in SQL, manifest symbol kind.
7. **Theme E — docs sweep**. CHANGELOG + README + DESIGN catch-up.

## What's deferred

- **Most language-by-language coverage gaps** — track in
  `bughunt-5-languages.md`; address when a real project hits the
  specific issue.
- **Theme F perf F4 (indexer worker pool)** — significant change
  to the indexer; defer until perf is a real complaint.
- **Theme F perf F6 (WAL checkpoint policy)** — workaround exists
  (vacuum on demand).
- **Public-release infra** — still tracked separately.
- **The 7 deferred MEDIUMs from bughunt-2/3** — still alive but
  not blocking.

## What stays out of scope

- LOW + informational findings (catalogued for completeness).
- Removing tree-sitter-smithy (still incompatible).
- Refactoring the symbol qname scheme (would require a re-index of
  every project).

If a fix lane finds a new HIGH while implementing, surface and
decide on the spot.
