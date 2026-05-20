# Contributing to Leonard

Thanks for your interest. Leonard is a small, opinionated project — most of the value comes from its **bug-hunt → triage → fix → repeat** discipline. Contributions that fit that pattern are easiest to accept.

## Quick start

```bash
git clone https://github.com/jasondillingham/leonard.git
cd leonard
go install ./cmd/...
(cd internal/parse/rust && cargo build --release)
(cd internal/parse/treesitter && cargo build --release)
go test ./...
```

Then wire Leonard into a project per the README's "Dogfood wiring" section and use it on real work. The fastest way to find a meaningful improvement is to **use Leonard until it surprises you**, then file an issue describing what you expected versus what happened.

## What kinds of contributions land well

### 🟢 New languages (tree-sitter strategy)

The infrastructure to add a language is ~50 LoC. The recipe:

1. Add `tree-sitter-<lang>` to `internal/parse/treesitter/Cargo.toml`.
2. Register the grammar in `Language::lookup` in `internal/parse/treesitter/src/main.rs` (one `match` arm + a `LANG_QUERY` const).
3. Write the tree-sitter query — see existing examples for the shape. Capture as `@function`, `@method`, `@type`, `@interface`, or `@const`; bind names via `@name`.
4. Add `parent_container_kinds` + `default_exported_methods` for the Language struct.
5. Add a one-liner `ExtractXxx` in `internal/parse/treesitter.go`.
6. Register the file extension in `internal/index/indexer.go`'s `langExtractors`.
7. Dogfood against a real project from that ecosystem; describe the dogfood result in the PR body.

If the language doesn't have a tree-sitter grammar on crates.io, or has a one that's tree-sitter-version-incompatible (see Smithy in v0.31), open an issue first to discuss.

### 🟢 Bug-hunt findings + fixes

The standard discipline:

1. **Bug hunt** — run a structured audit and write findings to `bughunt-N-<lane>.md`. See `bughunt-5-*.md` for the format. Each finding gets a severity (high/medium/low/informational), reproducer, observed/expected behavior, suggested fix shape.
2. **Triage** — `bughunt-N-triage.md` synthesizes themes and picks the fix-round priority list.
3. **Fix round** — one minor version bump per theme (`v0.X.0: <theme title>`). Tests cover the regression. Commit messages reference the original finding ID (`F1`, `F4`, etc.).

PRs that follow this shape get reviewed fast.

### 🟢 Language refinement

The `bughunt-5-languages.md` punch list has 32 specific per-language gaps that are deferred. Each one is a small, well-scoped PR. Pick a language you actually use and fix the specific finding.

### 🟢 Performance + correctness fixes

Bughunts surface these regularly. Recent examples: `idx_symbols_parent` (v0.7.1, ~137× speedup), the bufio.Scanner busy-spin DoS fix (v0.13), the v0.38 ledger semantics + v0.39 follow-up. Look for the deferred entries in the triage docs.

### 🟡 Documentation / examples

Smaller scope, easy to land. The `examples/` directory has space for more wiring examples (currently just `pydantic-ai/`).

### 🟡 New MCP tools / hooks

These are bigger design changes; open an issue first.

### 🔴 Things unlikely to land

- New language-extractor strategies competing with tree-sitter (the strategy is intentional consolidation).
- Smithy support — blocked on grammar maintainership upstream, not on our side.
- Per-user / multi-user features (Leonard is per-project local-first by design; DESIGN.md §1 lists this as a non-goal).
- Renaming the project, the symbol vocabulary, or the on-wire MCP schemas (would force every dogfooded project to re-index).

## Code style

- Go: `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...` all green. `go test -tags otel ./...` also green.
- Rust: `cargo build --release` in both `internal/parse/rust/` and `internal/parse/treesitter/`.
- Tests required for new behavior. Aim for the small-reproducer style — assert one specific contract per test.
- Comments explain WHY when the code's WHAT is non-obvious. Cite finding IDs (`bughunt-5 verifier F1`, etc.) when fixing a documented bug — the audit trail in commit messages compounds.

## Commit + PR conventions

- Branches: `feature/<short-name>` or `fix/<short-name>`.
- Commit messages: see the existing log for shape. Multi-paragraph is fine when there's context worth preserving.
- PR titles: short (under 70 chars). Body has summary + test plan + compatibility notes if any.
- Generated co-authorship attribution (`Co-Authored-By: Claude ...`) is welcome for PRs developed with Claude Code — that's how most of Leonard was built.

## Reporting bugs

If you find a bug that needs a deep investigation, the most useful filing shape is an `issue + PR` pair. The issue describes the failure mode (constraint to honor, root cause if known, motivation); the PR ships a fix that respects the constraint. See [issue #2 / PR #3](https://github.com/jasondillingham/leonard/pull/3) for an example — that pattern means the maintainer can review one coherent unit rather than chase a thread.

## Security

For security-sensitive findings, see [`SECURITY.md`](./SECURITY.md) before opening a public issue.

## Asking before writing code

For non-trivial changes, open an issue describing the approach first. Cheaper to align before than to rewrite after.
