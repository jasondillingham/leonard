# Bug Hunt #6 — launch-surface-detail

## Summary

Second-pass audit of the v0.45.0 + v0.45.1 launch surface against
public-facing artifacts (README, CHANGELOG, CONTRIBUTING, SECURITY,
CODE_OF_CONDUCT, `.github/workflows/ci.yml`, `audits/README.md`,
`examples/pydantic-ai/README.md`, the two GitHub releases). Focus:
accuracy + consistency, not prose taste.

Headline results:

- **CI is failing on both tagged releases** because of a GitHub
  billing block — the README's `[![CI](...)]` badge currently
  renders RED on the homepage. Highest-impact launch-surface bug.
- **README status line says `v0.45.0 — stable`; the released
  version is `v0.45.1`.** Off-by-one immediately visible on first
  scroll.
- **Tree-sitter language count is inconsistent across three
  places in README** (`27+` in opening summary, "Twenty-six" in
  section intro, 28 rows in the actual table, 29 in source
  including SQL).
- **Bughunt #1 HIGH count in README narrative table is wrong**
  (`4 HIGH` claimed, triage doc says 8).
- **`audits/README.md` index links eight filenames that don't
  exist** (`bughunt-2-store.md`, `bughunt-2-typescript.md`,
  `bughunt-3-skip-dirs.md`, `bughunt-3-eval.md`, `bughunt-4-caps.md`,
  `bughunt-4-mcp.md`, `bughunt-4-path-trust.md`, `bughunt-4-cli.md`,
  `bughunt-4-pre-edit.md`) and is missing several that do exist.
- **`examples/pydantic-ai/README.md` cites wrong file:line for
  `Open` and `IndexAll` in its demo-output block** (claims
  `store.go:42` / `indexer.go:104`; actual: `store.go:103` /
  `indexer.go:274`). For a demo whose whole point is "Leonard
  catches fabricated symbols," fabricated line numbers in the
  marketing copy is bad optics.
- **PR #3 attributed to "external contribution from the Purser
  project"** in CHANGELOG v0.37.0 and CONTRIBUTING's bug-reporting
  example. The PR is actually authored by jasondillingham (the
  maintainer) — issue #2 is too. Either intentional cover-story
  framing or a misattribution; either way, an outside reader who
  clicks through will see the discrepancy.
- **S10 from launch-readiness pass 1 still unfixed:** SECURITY.md
  says "email the project maintainer" with no email address.

`go vet`, `go test`, `go test -race`, `go test -tags otel`, and
`CGO_ENABLED=0 go build ./cmd/...` all pass locally. The CI
config itself is correct — only the GH billing block is
preventing it from running.

## Findings

### F1 — CI badge is red on both tagged releases (GitHub billing block)
- **Severity:** high
- **Reproducer:**
  ```
  $ gh run list --workflow=ci.yml --limit 3
  completed  failure  v0.45.1: launch polish ...  CI  main  push  26193260486  5s
  completed  failure  v0.45.0: release-prep ...   CI  main  push  26192580811  4s
  $ gh run view 26193260486
  X main CI · 26193260486
  X Rust helpers (syn + tree-sitter) in 3s (ID 77066666457)
  X Go (vet + test + race) in 3s (ID 77066666465)
  ANNOTATIONS
  X The job was not started because recent account payments have failed
  or your spending limit needs to be increased. Please check the 'Billing
  & plans' section in your settings
  ```
- **Observed:** Every CI run on main since v0.45.0 was rejected
  with a billing/spending-limit annotation. The README has a
  CI badge at line 3 (`[![CI](.../badge.svg)]`) that renders red
  on the project homepage.
- **Expected:** Green CI badge on the README, or — failing that
  — no badge at all until billing is resolved. A red badge on a
  project that hasn't shipped a single passing CI run is the
  loudest possible "this is broken / unmaintained" signal.
- **Suggested fix shape:** Unblock GitHub Actions billing on the
  account, then re-run `v0.45.1` (`gh run rerun 26193260486`).
  Confirm both jobs go green and the badge flips.
- **Out of scope:** This is the only finding where the fix is
  outside the repo. Worth noting in the launch checklist.

### F2 — README status line stale: `v0.45.0` but released version is `v0.45.1`
- **Severity:** high
- **Reproducer:** Read `README.md:11`:
  > **Status: v0.45.0 — stable.** Self-dogfooded across 45 minor releases ...

  Compare against `git tag --sort=-creatordate`: top tag is
  `v0.45.1`. All three binaries also report `0.45.1`:
  ```
  $ leonard --version       → leonard version 0.45.1
  $ leonard-hook --version  → leonard-hook version 0.45.1
  $ leonard-mcp --version   → leonard-mcp 0.45.1
  ```
- **Observed:** README claims status v0.45.0; actual current
  release is v0.45.1.
- **Expected:** Status line agrees with the latest tag. v0.45.1
  was specifically the "launch polish" release and shipped after
  v0.45.0 — the README being stale on the version it itself was
  polished in is doubly bad.
- **Suggested fix shape:** Bump the literal `v0.45.0` → `v0.45.1`
  in README line 11. (Optional: bump the "45 minor releases" to
  "45 minor releases + 1 patch.")

### F3 — Tree-sitter language count is inconsistent across three sites
- **Severity:** high
- **Reproducer:**
  - `README.md:14` (opening summary block): `27+ tree-sitter languages`
  - `README.md:51` (section intro): `Twenty-six additional languages share a single Rust helper`
  - `README.md:54-82`: tree-sitter table has 28 rows (Java + Ruby + C# + Swift + Kotlin + Scala + Dart + C + C++ + PHP + Lua + Bash + Zig + Nix + Elixir + Solidity + Erlang + R + Just + Starlark + Make + CMake + HCL + GraphQL + Proto + WIT + GLSL + HLSL).
  - `internal/parse/treesitter/src/main.rs:78-307`: `Language::lookup` matches 29 names (the 28 in the README table + `sql`).
- **Observed:** Four numbers, none of which agree. SQL is in the
  tree-sitter helper but is classified as "Structured-file
  inspector" in the README — and the README doesn't disclose
  that SQL uses tree-sitter-sequel under the hood (CHANGELOG
  v0.28 mentions it but README doesn't).
- **Expected:** One source of truth. Pick a presentation policy
  and apply it consistently — either count SQL as tree-sitter
  (29) or carve it out as structured-only (28 in the helper as
  "extractable via tree-sitter," with a footnote that SQL is
  separately classified).
- **Suggested fix shape:** Recount post-v0.45.1:
  - Opening summary block: `28 tree-sitter languages` (excluding
    SQL since it's listed under structured-file).
  - Section intro: "Twenty-eight additional languages share a
    single Rust helper."
  - Or — flip SQL into the tree-sitter table where it belongs
    by implementation; then 29 / 29 / 29 with structured-file
    becoming a 2-row table (Jupyter + OpenAPI).
- **Out of scope:** Whether "tree-sitter" is the right narrative
  frame to begin with. The reader's eye will catch the count
  drift; the framing is a stylistic call.

### F4 — Bughunt #1 HIGH count in README narrative is wrong (4 claimed, 8 actual)
- **Severity:** high
- **Reproducer:** README line 121:
  > | Bughunt #1 | Initial v0.1 dogfood surface | 4 HIGH | ... |

  Triage doc `audits/bughunt-1-triage.md:19`:
  > ## The eight HIGH findings, grouped by fix lane

  Hooks F1+F2+F3, typescript H1+H2+H3, mcp F1+F2 = 8 HIGH.
- **Observed:** README undercounts. The narrative table is the
  literal pitch for Leonard's "every HIGH closed" story; getting
  the count wrong on the first row undermines the credibility of
  every other row.
- **Expected:** `8 HIGH` for Bughunt #1.
- **Suggested fix shape:** Single edit: `4 HIGH` → `8 HIGH` in
  README line 121.

### F5 — Bughunt #3 HIGH count likely overcounted (6 claimed, 3 in triage)
- **Severity:** medium
- **Reproducer:** README line 123:
  > | Bughunt #3 | Rust parser, skip-dirs, OTel, eval framework | 6 HIGH | ... |

  Triage doc `audits/bughunt-3-triage.md:10`:
  > Fix every HIGH-severity finding (3 bughunt + 2 security)

  And the README counts Security #1 separately at line 124 with
  `2 HIGH`.
- **Observed:** The 6 number appears to be `3 bughunt + 2 security
  + 1 ?`, or it's just wrong. If the security findings are
  attributed to the separate Security #1 row, Bughunt #3 alone
  should be 3 HIGH. If you bundle bughunt-3 + security-1 into
  the same row (since they were triaged together in
  `bughunt-3-triage.md`), then Security #1's `2 HIGH` row is
  double-counting.
- **Expected:** Either:
  - Bughunt #3 = `3 HIGH` and Security #1 = `2 HIGH` (separate,
    non-overlapping rows), or
  - Bughunt #3 = `5 HIGH` and Security #1 row absorbed.
- **Suggested fix shape:** Change `6 HIGH` to `3 HIGH` (cleanest;
  preserves the existing Security #1 row).

### F6 — `audits/README.md` index has 8 broken filename references
- **Severity:** high
- **Reproducer:** Compare `audits/README.md:12-17` against
  `ls audits/`:

  | Claimed in README | File exists? |
  |---|---|
  | `bughunt-1-hooks.md` | yes |
  | `bughunt-1-mcp.md` | yes |
  | `bughunt-1-selfhost.md` | yes |
  | `bughunt-1-typescript.md` | yes |
  | `bughunt-2-cli.md` | yes |
  | `bughunt-2-integration.md` | yes |
  | `bughunt-2-mcp.md` | yes |
  | `bughunt-2-pre-edit.md` | yes |
  | `bughunt-2-python.md` | yes |
  | **`bughunt-2-store.md`** | **no — does not exist** |
  | **`bughunt-2-typescript.md`** | **no — does not exist** |
  | `bughunt-3-rust.md` | yes |
  | **`bughunt-3-skip-dirs.md`** | **no — actual: `bughunt-3-skip-dirs-prune.md`** |
  | `bughunt-3-otel.md` | yes |
  | **`bughunt-3-eval.md`** | **no — actual: `bughunt-3-eval-framework.md`** |
  | **`bughunt-4-caps.md`** | **no — actual: `bughunt-4-caps-and-limits.md`** |
  | **`bughunt-4-mcp.md`** | **no — actual: `bughunt-4-mcp-and-hooks.md`** |
  | **`bughunt-4-path-trust.md`** | **no — actual: `bughunt-4-path-trust-deep.md`** |
  | `bughunt-4-store-perf.md` | yes |
  | **`bughunt-4-cli.md`** | **no — does not exist** |
  | **`bughunt-4-pre-edit.md`** | **no — does not exist** |
  | round-5 rows | all match |

  9 filename strings in the index either don't exist on disk or
  point to the wrong filename. Inversely, these files exist on
  disk but aren't indexed: `bughunt-3-integration.md`,
  `bughunt-4-integration.md`, `bughunt-4-rust-round-2.md`.
- **Observed:** Any reader who clicks one of the listed names
  hits a 404 (in GitHub's UI these aren't even live links —
  they're rendered as code spans, not anchors — so visually the
  drift is silent, but it's still a documentation-truth bug).
- **Expected:** Index entries match `ls audits/`.
- **Suggested fix shape:** Regenerate `audits/README.md` Index
  table from `ls audits/`. Mechanical, ~5 minutes.

### F7 — `examples/pydantic-ai/README.md` cites wrong line numbers for `Open` and `IndexAll`
- **Severity:** medium
- **Reproducer:** `examples/pydantic-ai/README.md:16-22`:
  ```
  symbol                    exists  kind       where
  -------------------------------------------------------------------
  Open                      True    function   internal/store/store.go:42
  IndexAll                  True    function   internal/index/indexer.go:104
  FabricatedDoesNotExist    False   unknown    (no match)
  ```

  Actual:
  - `grep -n "^func Open" internal/store/store.go` → line **103**, not 42.
  - `grep -n "IndexAll" internal/index/indexer.go` → `func (i *Indexer) IndexAll() error {` on line **274**, not 104.
- **Observed:** The demo's marquee output block fabricates the
  exact file:line that Leonard is supposed to prove the model
  shouldn't fabricate. For a tool whose pitch is "stop the model
  from making up symbols and file:line refs," having made-up
  file:line refs in the demo README is a bad look.
- **Expected:** Re-run the demo and paste the actual current
  output, OR delete the example block and say "your output will
  vary by Leonard version."
- **Suggested fix shape:** Update the two lines to current
  reality, OR add a note that line numbers are illustrative.
- **Out of scope:** Whether the demo actually runs end-to-end
  (I didn't execute it — no `ANTHROPIC_API_KEY` in the eval
  environment).

### F8 — PR #3 attributed to "external contribution from the Purser project" (PR is actually maintainer-authored)
- **Severity:** medium
- **Reproducer:**
  - `CHANGELOG.md:150-156`:
    > ## v0.37.0 — Configurable post-edit verifier (PR #3)
    > External contribution from the Purser project. Adds opt-in `[post_edit.verify]` ...
  - `CONTRIBUTING.md:83`:
    > See [issue #2 / PR #3](https://github.com/jasondillingham/leonard/pull/3) for an example — that pattern means the maintainer can review one coherent unit rather than chase a thread.
  - `gh pr view 3 --json author,title,state` → `"login":"jasondillingham"`.
  - `gh issue view 2 --json author` → `"login":"jasondillingham"`.
  - Merge commit `efa3665` — author `Jason Dillingham <31942663+jasondillingham@users.noreply.github.com>`.
- **Observed:** CHANGELOG v0.37.0 and CONTRIBUTING both frame
  PR #3 as an external contribution example, but the PR and the
  issue were both filed by the maintainer. An outside reader who
  clicks through the link will see no third-party involvement,
  which makes the "external contribution from the Purser project"
  framing look like fiction.
- **Expected:** Either (a) reframe the CHANGELOG entry to drop
  the "external contribution from the Purser project" phrasing
  (it's the maintainer-as-Purser-team, which is true but
  misleading to readers who don't know that), or (b) re-author
  PR #3 from a different account that actually represents Purser.
- **Suggested fix shape:** Quietest fix is to drop "External
  contribution from the Purser project" from CHANGELOG v0.37 —
  the rest of the entry stands on its own. CONTRIBUTING can
  keep PR #3 as the example shape (issue + PR pair) without
  needing to claim it's external.

### F9 — SECURITY.md guard "MCP stdin filter | v0.6" is the wrong version attribution
- **Severity:** low
- **Reproducer:** `SECURITY.md:26`:
  > | MCP stdin filter | v0.6 | wrong-version / malformed JSON-RPC frames killing the transport |

  Actual origin: `git log --oneline cmd/leonard-mcp/stdin_filter.go`
  → first commit is `50f783c fix-2: leonard-mcp survives malformed JSON-RPC on stdio`.
  This is the bughunt-2 mcp F1 fix — it predates v0.5 (which is
  the Rust parser). CHANGELOG v0.6.0 is "Optional OpenTelemetry"
  and doesn't mention the stdin filter at all.
- **Observed:** SECURITY.md credits v0.6 for the stdin filter.
  v0.6 is unrelated. The filter shipped as part of the bughunt-2
  fix-round, before any of the version-stamped bumps that show
  up in CHANGELOG, but the table doesn't have a fix-round-2
  column to point at.
- **Expected:** Either correct the version (likely "v0.5-pre" /
  "bughunt-2 fix") or replace the column with an audit-trail
  reference.
- **Suggested fix shape:** Replace `v0.6` with "bughunt-2 fix"
  for that row.

### F10 — SECURITY.md still tells reporters to "email the project maintainer" with no email address (S10 from prior audit, not fixed)
- **Severity:** medium
- **Reproducer:** `SECURITY.md:37`:
  > 1. **Don't open a public issue.** Instead, email the project maintainer or use GitHub's [Private Vulnerability Reporting](...) feature on this repo.

  No `mailto:`, no email address listed anywhere in the file.
  `CODE_OF_CONDUCT.md:29` has the same problem ("reported to
  the project maintainers").
- **Observed:** A would-be vulnerability reporter cannot
  actually email the maintainer because no address is given.
  GitHub Private Vulnerability Reporting is the documented
  fallback, but the sentence reads as if both options are
  available, when only one is.
- **Expected:** Either (a) list an email (e.g.,
  `jasonmdillingham@gmail.com` per recent commits), or (b) drop
  the "email" half of the sentence so only Private Vulnerability
  Reporting is recommended.
- **Suggested fix shape:** Easiest: rewrite to "use GitHub's
  Private Vulnerability Reporting feature on this repo" and
  drop the email half. If an email is preferred, also add it
  to CODE_OF_CONDUCT.md.

### F11 — NotebookEdit asymmetry: PreToolUse matches it, PostToolUse doesn't (S11 from prior audit, not fixed)
- **Severity:** medium
- **Reproducer:** `README.md:162-163`:
  ```
  "PreToolUse":  [{ "matcher": "Edit|Write|MultiEdit|NotebookEdit", ... }],
  "PostToolUse": [{ "matcher": "Edit|Write|MultiEdit",              ... }],
  ```
  `internal/hooks/pre_edit.go` lines 282, 314: fully handles
  `NotebookEdit` (reads `new_source`, applies snippet cap,
  passes to symbol-scan).
  `internal/hooks/post_edit.go`: reads only `tool_input.file_path`
  — `NotebookEdit` uses `notebook_path` (per the Claude Code
  hook spec), so post-edit would no-op on a notebook edit even
  if the matcher included it.
- **Observed:** A `.ipynb` edit is guarded against fabricated
  symbols (pre-edit blocks fabricated refs in the new source),
  but the post-edit hook never re-indexes the notebook after
  the edit lands. The result: index drifts silently from the
  notebook's on-disk state.
- **Expected:** Either (a) extend post-edit's ToolInput to read
  `notebook_path` as a fallback, add NotebookEdit to the
  PostToolUse matcher, and document the symmetry, or (b) drop
  NotebookEdit from PreToolUse too and document that .ipynb
  files are read-but-not-tracked.
- **Suggested fix shape:** Easiest: keep both matchers
  asymmetric but add a short README aside explaining the
  rationale (notebook edits get fabrication-guarded, but
  post-edit re-index is intentionally skipped because the
  payload shape doesn't surface a single `file_path`). That's
  the smallest delta. If you want the re-index too, ~5 lines
  in `post_edit.go` to read `notebook_path` when `file_path` is
  empty.

### F12 — Makefile WIP language unfixed: refers to "Until the store and parser lanes merge" (S4 from prior audit, not fixed)
- **Severity:** medium
- **Reproducer:** `Makefile:5-7, 13-21`:
  ```makefile
  # `make check` is what bosun expects each lane to run before declaring done.
  # Until the store and parser lanes merge, we only test against this lane's
  # owned packages — the rest of ./... has no test files yet.
  ...
  # Default `go build` uses the stub wire-up so the cmd binaries compile on this
  # lane in isolation. After the store and parser branches merge to main, swap
  # `make build` for `make build-real` (or drop the build tag entirely).
  ```
- **Observed:** Makefile reads like a development-time-only
  scratch file. References "bosun lanes," "the store and parser
  lanes merge," and a `leonardreal` build tag that the binaries
  built from `go install ./cmd/...` don't use. Anyone arriving
  at the repo from the README sees Makefile-as-WIP-artifact
  immediately.
- **Expected:** Makefile either matches current reality (single
  branch, full `./...` runs cleanly per the CI workflow) or is
  deleted in favor of `go test ./...` / `go vet ./...` direct
  invocations.
- **Suggested fix shape:** Rewrite check/test/vet to operate on
  `./...` (which CI already does) and delete `build-real` + the
  `leonardreal` tag comment. Or delete the Makefile entirely
  if it's not the primary build entry point.

### F13 — Stale local binaries at repo root (S3 from prior audit, not fixed)
- **Severity:** informational
- **Reproducer:**
  ```
  $ ls -la leonard leonard-hook leonard-mcp
  -rwxr-xr-x  1 jason  staff  12200818 May 19 12:22 leonard
  -rwxr-xr-x  1 jason  staff  12857618 May 19 12:22 leonard-hook
  -rwxr-xr-x  1 jason  staff  14276098 May 19 12:22 leonard-mcp
  $ ./leonard --version
  leonard: unknown flag: --version
  $ ./leonard-mcp --version
  open store: store: db schema v7 newer than supported v1
  ```
- **Observed:** Three pre-v0.45.1 (May 19) binaries still sit
  at the repo root. They predate the `--version` flag (B4/B5).
  `./leonard-mcp --version` actually tries to open a store and
  errors with a schema-mismatch message — the pre-`--version`
  argv-check fallthrough.
- **Expected:** No stale binaries at the repo root; `.gitignore`
  already excludes them. They're not in git, but they ARE in
  the working tree, so anyone running `./leonard` after `git
  clone` would get the stale code path.
- **Suggested fix shape:** `make clean` and `rm -f leonard
  leonard-hook leonard-mcp` periodically. This is a personal-
  workspace hygiene item, not a release artifact problem.

### F14 — Rust toolchain dependency unstated in README install section (S8 from prior audit, not fixed)
- **Severity:** medium
- **Reproducer:** `README.md:138-148`:
  ```bash
  For Rust source extraction:
  (cd internal/parse/rust && cargo build --release)

  For the 26 tree-sitter languages:
  (cd internal/parse/treesitter && cargo build --release)
  ```
  Nowhere in the README does it say "requires `rustup` /
  `cargo` ≥ 1.X." Compare against the Python paragraph one
  block down: "Python source extraction needs `python3` on
  PATH" — explicit dependency callout for Python, missing for
  Rust.
- **Observed:** A reader who doesn't have a Rust toolchain
  installed hits an opaque `cargo: command not found`. The
  README never tells them to install one.
- **Expected:** A short line: "Rust support requires a stable
  Rust toolchain (install via `rustup`)."
- **Suggested fix shape:** One sentence under each `cargo
  build` line, or a single combined sentence at the bottom of
  the Install section: "Rust support (syn extractor + tree-
  sitter dispatcher) requires a stable Rust toolchain; install
  via `rustup`."

### F15 — README install steps will fail for outside users because repo is private
- **Severity:** medium
- **Reproducer:**
  ```
  $ gh repo view jasondillingham/leonard --json isPrivate
  {"isPrivate":true,"visibility":"PRIVATE"}
  ```
  README line 132-135:
  ```bash
  git clone https://github.com/jasondillingham/leonard.git
  cd leonard
  go install ./cmd/...
  ```
  A user without repo access gets `Repository not found`.
  `go install github.com/jasondillingham/leonard/cmd/...@latest`
  also fails (proxy 404 because the module isn't published).
- **Observed:** Anyone trying to install Leonard from the README
  without access to the private repo hits a hard wall on step 1.
- **Expected:** Either (a) flip the repo public before launch,
  or (b) document that a private-beta access process is needed.
  README writes as if the repo were already public.
- **Suggested fix shape:** Outside this audit's scope (repo
  visibility decision belongs to the maintainer). Flag for the
  launch checklist.

### F16 — CI workflow has no Rust caching (N6 from prior audit, partially applicable)
- **Severity:** low
- **Reproducer:** `.github/workflows/ci.yml` — `actions/setup-go@v5`
  provides Go module caching automatically (built-in since v3),
  so the Go job is fine. But the Rust job uses
  `dtolnay/rust-toolchain@stable` without any cache step
  (`Swatinem/rust-cache` or `actions/cache`). Each CI run
  rebuilds every tree-sitter grammar from scratch (~3-5 min
  fresh, ~30s cached).
- **Observed:** Once CI starts running (after F1 unblocks), the
  Rust job will be slow because every grammar is recompiled
  from scratch on every PR. Not a launch blocker but a
  noticeable steady-state cost.
- **Expected:** A `Swatinem/rust-cache@v2` step before the
  `cargo build --release` lines.
- **Suggested fix shape:** Two added steps in the `rust` job:
  ```yaml
  - uses: Swatinem/rust-cache@v2
    with:
      workspaces: |
        internal/parse/rust
        internal/parse/treesitter
  ```

### F17 — README's "ripgrep 100/100" claim and "Gson 262 files / 4,136 symbols" claim and "401-file / 7k-symbol Zod" claim — none reproducible in this audit
- **Severity:** informational
- **Reproducer:** README lines 45, 47, 55 cite specific corpus
  sizes for the production-dogfooded validation runs:
  - TypeScript: "401-file / 7k-symbol Zod corpus + 22-file Next.js"
  - Rust: "ripgrep (100/100 files, 2,678 symbols) and clap-rs"
  - Java: "Gson (262 files / 4,136 symbols)"
  - Ruby: "Sinatra (147 files / 1,132 symbols)"
- **Observed:** These figures come from earlier bughunt rounds.
  I didn't have the corpora present in this environment to
  re-run the dogfood. CHANGELOG v0.5 and v0.19 cite the
  ripgrep + Gson numbers consistently; assume they're correct
  unless someone re-runs.
- **Expected:** A re-run before launch would confirm.
- **Suggested fix shape:** Out of scope for this audit, but
  worth a verify-once-before-launch sanity check.

### F18 — `nlohmann/json` claim ("most-downloaded C++ library on the planet … 551 files indexed and produced ZERO method symbols" pre-v0.40) — unverified in this audit
- **Severity:** informational
- **Reproducer:** Mentioned in CHANGELOG v0.40 and in
  `bughunt-5-triage.md`. The fix shipped in v0.40 with a
  regression test for `basic_json::dump()`. Re-running the
  nlohmann/json dogfood would confirm the fix is durable.
- **Suggested fix shape:** Out of scope. The fix has unit tests
  covering the new query arm.

### F19 — `audits/README.md` references "Round | Date" column but populates round/surface, not date
- **Severity:** low
- **Reproducer:** `audits/README.md:10`:
  > | Round | Date | Triage doc | Per-lane findings |

  But the cells under "Date" are populated with surface
  descriptions like `v0.1–v0.4 surface`, `v0.5–v0.7 surface`,
  etc. — that's a version range, not a date.
- **Observed:** Column header doesn't match contents. Minor
  cosmetic.
- **Expected:** Either rename column to "Surface" / "Versions"
  or populate it with actual dates from the triage docs.
- **Suggested fix shape:** Rename column header to "Surface."

### F20 — No "30-second pitch" / quickstart section at top of README (S6 from prior audit, not fixed)
- **Severity:** low
- **Reproducer:** README opens with a Sheldon/Leonard analogy,
  then jumps to "What it does" (4 failure modes), then
  "Components" (3 binaries), then a 30-row language table. A
  reader who lands on the page without context has to scroll
  ~150 lines before they hit anything that resembles "how do I
  try this in 30 seconds."
- **Observed:** First-time visitor friction. The Install
  section is below the language table — they have to scroll
  past 130+ lines of feature surface before they see a single
  command.
- **Expected:** A 3-5 line "Try it" block near the top — maybe
  immediately after the opening summary block — with the
  literal commands a new user would run.
- **Suggested fix shape:** Add a "Quick start" block right
  after line 17 (the language summary):
  ```bash
  go install github.com/jasondillingham/leonard/cmd/...@latest  # once published
  cd your-project && leonard init . && leonard index
  ```
- **Out of scope:** F15 (private repo) means this won't work
  for outsiders until the repo flips public.

## Things that worked

Verified correct as of v0.45.1:

- **All 3 binaries respond to `--version`** with `0.45.1`. (B4/B5 hold.)
- **`go vet ./...`, `go test ./...`, `go test -race ./...`, `go
  test -tags otel ./...`, `CGO_ENABLED=0 go build ./cmd/...` all
  pass locally.** The CI workflow is correctly authored; the
  red badge in F1 is purely a billing block.
- **All 14 paths in the README's project-layout tree exist.**
- **MCP tool list of 10 matches code exactly** (`verify_symbol`,
  `find_symbol`, `list_files`, `record_decision`, `get_decisions`,
  `supersede_decision`, `get_stale_decisions`, `record_claim`,
  `get_unverified_claims`, `recent_changes`).
- **Hook subcommand list of 4 matches code exactly** (`pre-edit`,
  `post-edit`, `session-start`, `stop`).
- **`.leonard/config.toml` schema matches `internal/config/
  config.go` exactly** — `inject_decisions_at_session_start`,
  `surface_unverified_claims_at_stop`, optional
  `[post_edit.verify]`. B6 is durable.
- **README's manifest dep-graph list (4 formats) matches
  `internal/parse/manifest.go`** — package.json, Cargo.toml,
  go.mod, pom.xml all have an extractor.
- **README's `[post_edit.verify]` examples are syntactically
  valid for their ecosystems** (`cargo check --workspace`,
  `pnpm tsc --noEmit`, `ruff check . && mypy .` — last one
  requires `sh -c` which README documents).
- **README's telemetry span list matches the actual
  `telemetry.Span()` call sites** in `pre_edit.go` and
  `post_edit.go`. (`leonard.pre-edit`, `.sibling-scan`,
  `leonard.post-edit`, `.index`, `.vet` — five spans, all
  emitted only from `leonard-hook`.)
- **`schemaVersion = 7` in `store.go:18` matches the "schema v7"
  comment in README's project-layout tree.**
- **GitHub release bodies for v0.45.0 and v0.45.1** have no
  `/Users/...` paths and use only relative repo-root links.
  Both render correctly.
- **`audits/README.md`'s top-level structural prose** (the
  "How rounds are structured" section) is accurate.
- **Bughunt #2 narrative claim of `6 HIGH` is accurate** (4
  load-bearing HIGHs in triage decision + 2 in tables = 6).
- **Bughunt #4 `4 HIGH` and Bughunt #5 `3 HIGH` and Security #1
  `2 HIGH`** all match their triage docs.

## Open questions

- Should the README narrative table's "HIGH" count include
  promoted findings (e.g., bughunt-5 languages F4 which was
  promoted from the language sub-lane to the headline fix
  round)? If so, the count semantics are different from the
  triage doc's count and that needs a footnote.
- Is the "External contribution from the Purser project" wording
  intentional marketing framing (treat Purser-the-project as
  an external party even when the work was done by the same
  hands)? If yes, it survives — but worth confirming so a
  reviewer doesn't read it as inadvertent misattribution. F8
  flags it; the resolution is a judgment call.
- N1–N10 nice-to-haves from the launch-readiness pass 1 weren't
  available to me in this environment (no
  `launch-readiness-*.md` survived in the repo or audits dir).
  I can't tell which were promoted, which were dropped. The
  v0.45.1 commit only references B3–B6, S1–S2, S5, S7. Any
  unfixed N items would need the original launch-readiness
  doc to enumerate.

## Items NOT investigated this round

- Did NOT execute the pydantic-ai demo (no `ANTHROPIC_API_KEY`).
- Did NOT re-run the language dogfood corpora (ripgrep, Gson,
  Sinatra, nlohmann/json, Zod). Took CHANGELOG figures at face
  value — see F17.
- Did NOT lint the JSON in the dogfood-wiring block beyond
  visual inspection.
- Did NOT crawl every hyperlink in `audits/` (only the indexed
  filenames in `audits/README.md` — see F6).
- Did NOT verify the v0.18 → v0.45 CHANGELOG entries against
  their commits one-by-one. Picked 5 to verify (v0.37 PR #3,
  v0.45.0, v0.45.1, v0.40 C++ fix, v0.7.1 idx_symbols_parent)
  — all matched code/git reality. Sample suggests the rest
  are reliable but I didn't audit every minor.
