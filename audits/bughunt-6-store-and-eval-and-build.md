# Bug Hunt #6 — store / eval / build

## Summary

Audited three surfaces that haven't gotten focused attention since v0.38 (store schemaVersion bump) and v0.10 (Inspect eval framework introduction): the SQLite store with all 7 migrations, the `evals/inspect/` Python harness, and the build system (Makefile, `go.mod` toolchain, Cargo crates, CI workflow). The store is in good shape — WAL works, FK enforcement is on, every index is exercised by the queries that need it, and a 100-goroutine write storm completes with zero `database is locked` errors. The eval framework runs (mockllm smoke + recent live logs in `evals/inspect/logs/` both pass). **The build system has clear rot**: the Makefile still describes `bosun` lanes and a `leonardreal` build tag that no longer exists, `go.mod` declares `go 1.25.0` with no `toolchain` directive (so Go ≤ 1.21 will refuse to build it without GOTOOLCHAIN intervention), and neither Cargo.lock is committed, so tree-sitter grammar versions drift on every fresh build. Two store-layer issues with material consequences: storeKey NFC normalization is applied only at the indexer layer, so claim rows hold un-normalized paths (supersession can silently miss across NFC/NFD); and `wal_checkpoint(PASSIVE)` after a bulk delete uses `s.db.Exec` after the transaction commits but with a hard-coded threshold (≥ 100) that doesn't capture the multi-megabyte-tx case.

## Findings

### F1 — Makefile is still bosun-era and references a build tag that no longer exists
- **Severity:** medium (cosmetic for runtime, but the bosun language is in the OSS-public face of the repo)
- **Reproducer:** `cat Makefile`
- **Observed:** The Makefile's top comment still says

  > `make check` is what bosun expects each lane to run before declaring done.
  > Until the store and parser lanes merge, we only test against this lane's
  > owned packages — the rest of ./... has no test files yet.

  …and ships a `build-real` target that invokes `go build -tags leonardreal ./cmd/leonard ./cmd/leonard-hook`. A grep over the tree confirms **no Go source file uses `//go:build leonardreal`** anywhere — that build tag has been dead since the phase-1 merge. `make check` also only tests four packages (`cmd/leonard`, `cmd/leonard-hook`, `internal/hooks`, `internal/config`) and skips everything the bug-hunt rounds added (store, mcp, index, parse, telemetry). Stale.
- **Expected:** `make check` runs the same test surface CI runs (`go vet ./... && go test ./...`); the `build-real` target and its associated comment are gone; the "bosun" branding is removed (or moved to a doc).
- **Suggested fix shape:** Replace `test` and `vet` targets with `./...`. Delete `build-real`. Replace lane-language with a one-liner. Optionally add a `cargo` target that builds the two Rust helpers (CI already does this; locally it's not discoverable from the Makefile).
- **Out of scope:** Whether `make check` should also run the Inspect mockllm smoke (currently no Make target exercises the Python eval).

### F2 — `go.mod` has no `toolchain` directive — Go ≤ 1.21 refuses to build
- **Severity:** medium
- **Reproducer:** `head -5 go.mod`
- **Observed:**
  ```
  module github.com/jasondillingham/leonard
  go 1.25.0
  ```
  No `toolchain` line. Go 1.22+ defaults to `GOTOOLCHAIN=auto` and will download `go1.25.0` on demand, so practically most users are fine. But Go ≤ 1.21 (still shipped by Ubuntu 22.04 / 24.04 LTS apt repos, RHEL 9 base packages, etc.) doesn't understand toolchain auto-download — `go build` errors with `module requires Go 1.25` and stops. The CI workflow pins `go-version: '1.25'` so CI is unaffected. The brief explicitly asked: "does the toolchain directive actually fetch on a system with only 1.21?" — answer is **no, because there is no toolchain directive** to do the fetching.
- **Expected:** Either an explicit `toolchain go1.25.0` line so even Go 1.22's GOTOOLCHAIN=auto behavior is unambiguous, or the README explicitly states "requires Go 1.22+ for toolchain auto-download; Go 1.25 to build directly."
- **Suggested fix shape:** Add `toolchain go1.25.0` to `go.mod` and document the minimum Go in README's "Requirements" or "Building" section.
- **Out of scope:** Whether the project should actually require Go 1.25 (vs e.g. 1.23). The min-version bump was presumably for a 1.25-only stdlib feature; not investigated.

### F3 — Cargo.lock files are gitignored, not committed
- **Severity:** medium
- **Reproducer:** `git ls-files | grep -E "Cargo\.(toml|lock)"`
- **Observed:** Only the two `Cargo.toml` files are tracked:
  ```
  internal/parse/rust/Cargo.toml
  internal/parse/treesitter/Cargo.toml
  ```
  Both crate-local `.gitignore` files explicitly list `Cargo.lock` (and `target/`). For binary crates — which both of these are (`[[bin]]` sections) — the Rust community convention is to **commit Cargo.lock** so the dispatcher binary's grammar versions and their transitive deps are reproducible.

  Tree-sitter is the case where this bites hardest: `Cargo.toml` lists 30+ grammar crates with caret-version constraints (`tree-sitter-java = "0.23"` matches `0.23.0..0.24.0`). A fresh build six weeks from now can pull a `0.23.x` point release that re-tokenizes some edge case (e.g. a new Kotlin grammar revision that captures property accessors differently) and the indexer's output silently changes for that language. Cold rebuild verified — see F4 — 16 s for treesitter, 7 s for the rust crate; reproducibility is otherwise solid except for this version drift.
- **Expected:** Both Cargo.lock files committed. The `.gitignore` lines that exclude them removed.
- **Suggested fix shape:** Drop `Cargo.lock` from the two crate-local `.gitignore` files; `git add internal/parse/rust/Cargo.lock internal/parse/treesitter/Cargo.lock`. The existing lock files (38 MB total uncompressed; 26 KB each as text) are reasonable to track.
- **Out of scope:** Pinning to exact versions in `Cargo.toml` itself (`= "0.23.5"`) is a less idiomatic alternative.

### F4 — CI workflow has no caching for either Go modules or Cargo targets
- **Severity:** medium (CI time tax — not correctness)
- **Reproducer:** `cat .github/workflows/ci.yml`
- **Observed:** No `actions/cache` steps, no `cache: true` on `setup-go@v5` (the action defaults to false; we don't pass it). The Rust job has no equivalent of Swatinem/rust-cache. Empirically the cold builds measured locally are:
  - tree-sitter helper, cold: 16.5 s wall (56 s CPU on 4 cores)
  - rust syn helper, cold: 7.2 s wall (29 s CPU on 4 cores)

  On GitHub-hosted runners (slower CPUs, no parallel cores effective in single-arch jobs), this is closer to ~60 s + ~25 s per push. CI runs both Rust builds on every push and PR. That's ~90 s of pure rebuild cost per CI run that a cargo cache would erase.
- **Expected:** `actions/cache` for `~/go/pkg/mod`, `~/.cache/go-build`, and the two `target/` directories — or the more idiomatic `Swatinem/rust-cache@v2` step in the Rust job. `actions/setup-go@v5` with `cache: true` for the Go job.
- **Suggested fix shape:** Add caching steps. Reference: `Swatinem/rust-cache@v2` keyed on `Cargo.lock` (which would also drive F3 — caching only works once `Cargo.lock` is committed).
- **Out of scope:** Per-grammar parallelism (e.g. splitting treesitter crate into smaller per-grammar crates so changes to one grammar don't re-compile all 30) — out of scope for CI tuning.

### F5 — CHANGELOG/audit history references "launch-readiness S4 / N6"; those files don't exist in the repo
- **Severity:** informational
- **Reproducer:** `grep -rn "launch-readiness" audits/ 2>/dev/null` returns nothing; `ls audits/ | grep -i launch` returns nothing.
- **Observed:** The bughunt-6 brief specifically references "launch-readiness S4" (Makefile WIP language) and "launch-readiness N6" (missing CI cache). Both findings are reproducible (see F1 and F4), but the audits themselves have been removed/never landed in `audits/`. Future contributors trying to back-reference will find nothing.
- **Expected:** The `audits/` directory either contains the launch-readiness reports, or some other surface (README, CONTRIBUTING) explains where the launch-readiness items went.
- **Suggested fix shape:** Either restore launch-readiness audits from history, or fold their action items into a single `audits/launch-readiness.md` index.
- **Out of scope:** Same for "task #35" — the brief says "Pending an actual live run since (task #35)" but no task-tracking surface in the repo references #35.

### F6 — `storeKey` NFC normalization applies only to the files table; claim rows carry un-normalized absolute paths
- **Severity:** medium (macOS-only edge case; silent supersession failure)
- **Reproducer:**

  Store-layer probe (run via `go run` from repo root):
  ```go
  nfc := norm.NFC.String("café.go") // single é codepoint
  nfd := norm.NFD.String("café.go") // e + combining acute
  s.UpsertFile(store.File{Path: nfc, ...})
  _, found, _ := s.GetFile(nfd) // found=false
  s.UpsertFile(store.File{Path: nfd, ...})
  files, _ := s.ListFiles("", "")  // len = 2, two rows
  ```
  Confirmed locally — two rows. `internal/index/indexer.go:656 storeKey()` applies `norm.NFC.String` before writing to the files table, but **the store itself does no normalization**. Any caller that bypasses `storeKey` and passes a path directly to `UpsertFile`/`GetFile`/`ReplaceSymbols` will create duplicates.
- **Observed:** The post-edit hook (`internal/hooks/post_edit.go:212`) sets `filePath = safePath` (output of `ResolveSafe`, an *absolute* platform-native path) and persists that into `claims.file_path`. `ResolveSafe` does no NFC normalization. So:
  1. Indexer writes `files.path = "café.go"` (NFC, slash-form, relative)
  2. Post-edit writes `claims.file_path = "/Users/.../café.go"` (NFD if that's what the hook payload contained — Claude Code on macOS can send either form depending on which tool emitted the path)
  3. `SupersedeClaimsForFile(filePath, ...)` matches by exact string equality on `claims.file_path`. A later post-edit firing with NFC on the same on-disk file won't match the prior NFD claim row. Failures hang in the ledger.

  Bughunt-4 path-trust F3 fixed half of this for the files table; the claims half is still wide open.
- **Expected:** Either store-layer normalization (every `UpsertFile`/`RecordClaim` normalizes to NFC inside the store) or extending `storeKey`-style normalization to every place that writes a path. The store layer is the safer fix.
- **Suggested fix shape:** Add NFC normalization inside `RecordClaim`, `SupersedeClaimsForFile`, and `SupersedeClaimsForFile`'s `file_path =` predicate. Or push the same normalization into `ResolveSafe`.
- **Out of scope:** Whether claims should store relative-slash paths (matching files table) instead of absolute paths. That's a bigger refactor and breaks the existing column semantics.

### F7 — `PRAGMA wal_checkpoint(PASSIVE)` housekeeping fires only on bulk deletes of ≥ 100 rows, never on bulk writes
- **Severity:** low
- **Reproducer:** `grep -A2 "wal_checkpoint" internal/store/store.go`
- **Observed:** `DeleteFiles` (line 634-636) runs `PRAGMA wal_checkpoint(PASSIVE)` after any commit that touched ≥ 100 rows. Comment explains: SQLite auto-checkpoints around 1000 WAL frames, but a single multi-megabyte transaction can blow past that. The fix is correct for the *delete* path — but the same WAL bloat happens on bulk *insert* paths:
  - `ReplaceSymbols` with a large file (5000-symbol generated parser, say) writes thousands of frames in one tx.
  - Indexer ingest of a fresh project: hundreds of files × ReplaceSymbols + UpsertFile in a single walk.

  No checkpoint trigger fires there. The WAL can grow large on cold-index ingest and not truncate until SQLite's auto-checkpoint fires (which it does, eventually — this is housekeeping, not correctness). Worth knowing for v1.
- **Expected:** Either consistent checkpoint behavior across bulk-write paths, or a periodic checkpoint (e.g. once per indexer run).
- **Suggested fix shape:** Add a `PRAGMA wal_checkpoint(PASSIVE)` at the end of `Indexer.IndexAll` (or wherever the cold-index run completes), not after each per-file tx — single call, non-fatal on error.
- **Out of scope:** Switching to `synchronous = NORMAL` (already the WAL default; `PRAGMA synchronous` reports 1 = NORMAL on the live DB).

### F8 — `examples/pydantic-ai/uv.lock` and `evals/inspect/uv.lock` are gitignored
- **Severity:** low (mirrors F3 for Python)
- **Reproducer:** `git check-ignore -v evals/inspect/uv.lock` → matched. `cat evals/inspect/.gitignore` shows `uv.lock` on line 2.
- **Observed:** Both Python projects have working uv.lock files locally (625 KB and similar) but they're not in the repo. A fresh checkout + `uv sync` will resolve against PyPI *as of that moment* — `inspect-ai>=0.3.220` will pick up whatever the latest release is, including potentially breaking changes to the `mcp_server_stdio` signature (which has changed at least once historically — bughunt-3 eval F1 was caused by a similar pickup).
- **Expected:** uv.lock committed for both reproducible installs. Convention varies — Python packaging guidance leans toward "commit lockfiles for applications, not libraries"; both of these are applications.
- **Suggested fix shape:** Remove `uv.lock` from both `.gitignore` files, commit existing lock files.

### F9 — `migrateV1` is non-idempotent if run after a partial v1 schema exists (no `CREATE TABLE IF NOT EXISTS`)
- **Severity:** low (only matters if migration was interrupted mid-tx; the wrapping tx should make this safe)
- **Reproducer:** Code inspection. `migrateV1` issues `CREATE TABLE files (...)` without `IF NOT EXISTS`. If a previous migration attempt committed (impossible per the wrapping tx in `migrate()`) or if someone hand-creates the files table, v1 re-application errors.
- **Observed:** Migration code wraps all `migrations[v](tx)` calls in a single `tx.Begin/Commit` block in `migrate()`, so if any migration step fails mid-run the whole tx rolls back. **In normal operation this is safe.** The risk is edge cases: a panic between `tx.Commit()` and the `INSERT INTO meta(schema_version)` write would leave the tables created but `schema_version` unrecorded — though those two statements are in the same tx, so this is impossible. Net: idempotence is via the surrounding tx, not the migrations themselves. Mention noted by the brief.
- **Expected:** Either explicit `IF NOT EXISTS` clauses everywhere, or a comment explaining the tx-level safety guarantee.
- **Suggested fix shape:** Add a top-of-`migrations`-list comment: "Idempotence is enforced by the surrounding transaction in `migrate()`. Individual migrations may use unconditional DDL."

### F10 — `migrateV7`'s LIKE patterns may delete user-recorded claims with matching substrings
- **Severity:** medium
- **Reproducer:**
  ```sql
  DELETE FROM claims WHERE claim LIKE '%path escapes project root%'
                       OR claim LIKE '%index=skipped (file not found)%';
  ```
- **Observed:** v7 is described as a "one-time cleanup of historical hook-emitted claim rows" with two patterns. But it deletes by `claim` text match — a user could legitimately have called `record_claim(claim="vet failed — index=skipped (file not found): foo.go")` via MCP and that row gets wiped on first open of the post-v7 DB. The patterns are unique enough that it's unlikely in practice, but it's not impossible.

  The cleaner approach would be to qualify by `tool` (set only by post-edit, not by MCP `record_claim`) AND/OR by the absence of the structured columns the hook always populates (`tool IS NULL` would exclude all hook-emitted rows — wrong direction; need `tool IS NOT NULL AND vet_ok IS NULL` or similar).
- **Expected:** A migration that can't accidentally delete a user's MCP-recorded claim with a similar string.
- **Suggested fix shape:** Replay v7 logic but include `AND vet_ok IS NULL` (the file-not-found / path-escape paths never set vet_ok). Or accept the risk and document it in the migrateV7 docstring (right now the docstring says "Both classes are identified by claim-text patterns the relevant handlers used. Idempotent — re-running on a clean DB deletes zero rows" — true for the originally-targeted rows, false for shared substrings).
- **Out of scope:** Whether v7 should re-run on every Open (it does — the migration runs once because `schema_version=7` is set after; re-running v7 with a hypothetical schemaVersion=8 bump won't include v7 again).

### F11 — `FindSymbolsByQuery` (substring LIKE) inherently cannot use an index
- **Severity:** informational
- **Reproducer:**
  ```
  EXPLAIN QUERY PLAN
  SELECT * FROM symbols WHERE name LIKE '%foo%' OR qualified_name LIKE '%foo%';
  -> SCAN symbols
  ```
- **Observed:** The leading `%` precludes B-tree index use. Every call scans the symbol table. On Leonard's own index (1318 symbols) this is sub-millisecond. On a large project (50k+ symbols — the bughunt-5 perf bounds), this becomes a hot path. There's a comment in `SupersedeOutstandingFailures` about a previous LIKE-grep that was switched to a structured column — the same shape applies here, but the goal of "substring search" is fundamentally LIKE-shaped.
- **Expected:** Either FTS5 (full-text search) for find_symbol's substring path, or a documented "stays O(n) — fine for project sizes ≤ 50k symbols" note.
- **Suggested fix shape:** Document the limit. Add an FTS5 virtual table only if a real-world project trips a perf budget.
- **Out of scope:** Lowercasing/`COLLATE NOCASE` for case-insensitive search — currently case-sensitive by design.

### F12 — Tree-sitter helper binary is 38 MB; strip doesn't help (mostly compiled grammar `.text`)
- **Severity:** informational
- **Reproducer:** `ls -lh internal/parse/treesitter/target/release/leonard-extract-treesitter` → 38 MB. After `strip`, still 38 MB.
- **Observed:** Cargo profile already sets `opt-level = 3`, `lto = "thin"`, `codegen-units = 1`. The crate links 30 grammars at compile time. Each grammar is a few hundred KB of generated C/Rust state tables; that's where the bulk of the binary lives. `strip = "symbols"` is set on the *rust* crate (1.7 MB binary) but **not** on the treesitter crate's `[profile.release]`. Adding `strip = "symbols"` there would shave maybe 5-15% but not change the order of magnitude.

  Real reduction would come from:
  - `lto = "fat"` (vs "thin") — trades 30-50% longer compile for ~10-20% smaller binary
  - Splitting grammars into Cargo features and not enabling them all by default
  - Lazy-loading grammars via the dispatcher pattern
- **Expected:** 38 MB is fine for a developer tool. Worth noting that the binary is much larger than the Go binaries (~12-14 MB each); shipping a release tarball is ~80 MB total.
- **Suggested fix shape:** Add `strip = "symbols"` to the treesitter Cargo.toml `[profile.release]` for parity with the rust crate. Document the 38 MB size in CONTRIBUTING or wherever the build artifacts are described.

### F13 — Stray `target 2/` directory next to `target/` in treesitter crate
- **Severity:** low (cosmetic — macOS Finder duplication, not Cargo)
- **Reproducer:** `ls -la internal/parse/treesitter/` → shows both `target/` and `target 2/`
- **Observed:** A user-mode-only (drwx------) empty `target 2/` directory exists next to the real Cargo `target/`. Looks like a Finder-side accidental duplication; ignored by `.gitignore` (`target/` excludes both via prefix match? actually no — `target/` is a fnmatch pattern that won't match `target 2`). Confirmed not tracked.
- **Expected:** Cleanup. No correctness impact.
- **Suggested fix shape:** `rm -rf "internal/parse/treesitter/target 2"`. Optionally extend `.gitignore` to `target*/`.

### F14 — Pre-built `./leonard`, `./leonard-hook`, `./leonard-mcp` binaries at repo root are stale
- **Severity:** low (gitignored, but misleading for repo-root invocations)
- **Reproducer:** `./leonard --help` shows only 5 subcommands (`completion`, `help`, `index`, `init`, `mcp`, `verify`); `go build && ./leonard --help` shows 10 (adds `claims`, `decisions`, `doctor`). The committed README assumes the new surface.
- **Observed:** The repo root has three executables timestamped May 19; they're built from a much older revision (no `doctor` command means pre-v0.30-ish). They're gitignored, so this is a local artifact, but the project's documented entry point is "from this directory, run `./leonard …`" — running that pre-built binary gives wrong behavior.
- **Expected:** Either rebuild on every push (Makefile `build` target → `go build -o ./leonard ./cmd/leonard …`) or document that the binaries are not auto-updated.
- **Suggested fix shape:** Update Makefile's `build` target to install the three binaries into the repo root. Or document that users should `go install ./cmd/...` instead.

### F15 — `leonard-extract-rust` ships with `dead_code` warning on `span_lines` method
- **Severity:** informational
- **Reproducer:** `cd internal/parse/rust && cargo build --release 2>&1 | grep -E "warning"` produces
  ```
  warning: method `span_lines` is never used
  ```
- **Observed:** Unused method. Either delete it or `#[allow(dead_code)]` it. Cosmetic — every cold build prints this.
- **Expected:** Clean build.
- **Suggested fix shape:** Delete the dead method.

### F16 — `examples/pydantic-ai/uv.lock` file present locally but never tested in CI
- **Severity:** informational
- **Reproducer:** `cat .github/workflows/ci.yml` → no Python steps; no exercise of the pydantic-ai demo.
- **Observed:** The README (line 215 of root README.md) advertises `examples/pydantic-ai` as a working demo. There's no CI check that even `python -m py_compile demo.py` still passes — let alone that the imports resolve against a pinned pydantic-ai version. I verified locally (`uv sync` + `python -c 'from pydantic_ai.mcp import MCPToolset; from fastmcp.client.transports import StdioTransport; print("imports ok")'`) succeeded, but **only against this checkout's resolved versions** because uv.lock isn't committed (F8).
- **Expected:** Optional CI lane that does at least `uv sync` and a syntax/import check.
- **Suggested fix shape:** Add a Python check job to `ci.yml` that does `cd examples/pydantic-ai && uv sync && uv run python -c "import demo"` — gated to PRs touching that path.

### F17 — CI workflow drives `go test ./...` but no `make check` equivalence is asserted
- **Severity:** informational
- **Reproducer:** Compare `cat .github/workflows/ci.yml` against `cat Makefile`.
- **Observed:** CI runs `go vet ./...`, `go test ./...`, `go test -race ./...`, `go test -tags otel ./...`, `CGO_ENABLED=0 go build ./cmd/...`, plus two `cargo build --release` runs. The Makefile's `make check` runs `go vet ./four-packages... && go test -race ./four-packages... -count=1`. They are **not equivalent** — local `make check` will pass while CI fails (or vice versa). This is the same issue as F1 from a different angle.
- **Expected:** `make check` exactly mirrors what CI runs.
- **Suggested fix shape:** Rewrite `make check` to invoke the same commands as CI in the same order. Possibly extract the CI commands into a shell script both reference.

### F18 — `synchronous` is implicitly NORMAL (1) — not configured by DSN; relies on SQLite default for WAL
- **Severity:** informational
- **Reproducer:** `sqlite3 .leonard/leonard.db "PRAGMA synchronous"` → `1`. `buildDSN()` in `internal/store/store.go:134-140` sets only `journal_mode(wal)`, `foreign_keys(on)`, `busy_timeout(5000)`. Not `synchronous`.
- **Observed:** SQLite defaults to `synchronous = NORMAL (1)` when `journal_mode = WAL` is set explicitly. That's the right choice (FULL gives marginal durability at significant write cost; OFF risks corruption on power loss). But it's not documented in `buildDSN`'s comment — a future contributor reading just that function couldn't know the choice was deliberate.
- **Expected:** Either explicit `synchronous(normal)` in the DSN (cosmetic but documents intent) or a comment explaining the reliance on SQLite's WAL default.
- **Suggested fix shape:** Add `q.Add("_pragma", "synchronous(normal)")` or extend the function-doc comment.

### F19 — `foreign_keys` is per-connection in SQLite; `sqlite3` CLI inspection shows it OFF
- **Severity:** informational
- **Reproducer:** Open Leonard's DB with `sqlite3 .leonard/leonard.db` → `PRAGMA foreign_keys` returns `0`.
- **Observed:** This is correct SQLite behavior — `PRAGMA foreign_keys` is per-connection, not persistent. The Leonard store's `buildDSN` sets it ON for every connection it opens. But an operator opening the DB with a different tool (sqlite3 CLI, DB Browser, custom script) won't have FK enforcement — they could insert dangling references. Manual surgery on the DB is rare but the foot-gun exists.
- **Expected:** Document. Or add a `CHECK` constraint or a trigger that mirrors FK behavior at row-write time (unnecessary armor, but defensive).
- **Suggested fix shape:** Add a one-line note in DESIGN.md §7 or store/doc.go: "All connections opened via `store.Open` enable foreign keys; external tools (sqlite3 CLI, etc.) won't have FK enforcement unless they also `PRAGMA foreign_keys=ON`."

### F20 — MCP-recorded claims leave `vet_ok` NULL, so `SupersedeOutstandingFailures` never resolves them
- **Severity:** low (documented as intentional in `claims.go` comments, but worth flagging)
- **Reproducer:** Read `internal/mcp/claims.go:93` — `cs.RecordClaim` is called without Tool/IndexOK/VetOK/VetErrorSummary. Read `store.go:1098` — `SupersedeOutstandingFailures` filters by `vet_ok = 0`, which is **integer-equals-zero**, not nullable.
- **Observed:** A model that calls `record_claim` with `verified=false` (the "flag this for follow-up" pattern documented in the tool description) creates a row with `vet_ok IS NULL`. `SupersedeOutstandingFailures` matches only `vet_ok = 0` rows — SQL NULL semantics means NULL ≠ 0, so model-recorded unverified claims are **never** automatically superseded by a later vet=ok post-edit hook. They hang forever until manually resolved via `leonard claims resolve <id>`.

  This is by design (see comment in `claims.go:55-56`: "Tool, IndexOK, VetOK, and VetErrorSummary are populated by the post-edit hook (schema v3+); claims recorded via the record_claim MCP tool from a model leave them empty"), but it means there's an asymmetry: hook-recorded claims auto-resolve; model-recorded claims do not.
- **Expected:** Either match the symmetry (model-recorded claims also get a project-wide auto-supersede when the next vet=ok lands), or surface the manual-resolution path in the `record_claim` tool description.
- **Suggested fix shape:** Document in the `record_claim` tool description that the claim will need manual resolution unless the model's intent is captured by a follow-up post-edit hook. Or extend `SupersedeOutstandingFailures` to also include `vet_ok IS NULL AND tool IS NULL` (the model-recorded shape).
- **Out of scope:** Whether the MCP `RecordClaimInput` should accept an optional `vet_ok` so the model can opt into supersession semantics.

## Things that worked

- **All 7 migrations apply cleanly** on a fresh DB. The migration runner wraps everything in a transaction so a partial failure rolls back. Schema version is read from `meta`, with a downgrade-detection check.
- **All 11 declared indexes exist on the live DB** and `EXPLAIN QUERY PLAN` confirms they're used by the queries the bug-hunt history added them for:
  - `idx_symbols_name`, `idx_symbols_file`, `idx_symbols_parent` (cascade), `idx_symbols_qname`
  - `idx_claims_session`, `idx_claims_file_path`, `idx_claims_vet_ok`, `idx_claims_verified`
  - `idx_files_indexed_at`, `idx_decisions_topic`, `idx_decisions_recorded_at`
- **FK enforcement is on per connection.** A direct probe inserting a symbol with a non-existent `file_path` correctly errors with `FOREIGN KEY constraint failed`. Same for `superseded_by_claim_id` pointing at a non-existent claim ID.
- **`storeKey` NFC normalization works for the files table** (verified with NFC vs NFD `café.go` inputs through the indexer — though see F6 for the gap on claims).
- **`ResolveSafe` rejects all the right cases**: dangling symlinks (bughunt-4 F2 fix is intact), symlinks inside the project pointing outside (security-1 F3), absolute paths outside the project, `../` relative escapes. Lexical containment + EvalSymlinks both run; both have to pass.
- **Concurrent access is solid**: 100-goroutine storm (each doing UpsertFile + ReplaceSymbols + RecordClaim) completes with zero errors. 20 concurrent post-edit hook invocations + 3 concurrent session-start hooks against a real `.leonard/leonard.db` complete with zero `database is locked` errors. WAL + 5 s `busy_timeout` is the right combo.
- **The Inspect eval framework runs**: mockllm smoke test (`uv run inspect eval tasks.py@fabrication_control --model mockllm/model --limit 1`) completes in <1 s. Live `evals/inspect/logs/` directory contains six recent runs from 2026-05-20 (control and treated arms), confirming this surface has been exercised end-to-end with a real API key.
- **`leonard-mcp` advertises all 10 tools** over stdio: `find_symbol`, `list_files`, `verify_symbol`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `recent_changes`. Schema looks correct; `tools/list` round-trip works.
- **`leonard doctor` against the Leonard repo itself produces sensible output**: 175 files, 1318 symbols across 4 languages, 0 decisions, 0 unverified claims. (62 stale-files entries are real but they're pre-existing leftover scratch indexings from bug-hunt rounds — `../../../../../tmp/bughunt*` paths — not a doctor bug.)
- **`examples/pydantic-ai/demo.py` imports resolve** under the locally-cached uv environment. Could not run the actual agent (no ANTHROPIC_API_KEY in this session), but the static surface is intact.
- **CGO_ENABLED=0 builds work** for all three Go binaries. modernc.org/sqlite is the pure-Go sqlite driver, so no CGo is ever required.
- **`go test ./...` passes**, `go vet ./...` is clean, `go mod verify` reports all modules verified.

## Open questions

- Does CI's GitHub-hosted ARM/Linux runner ever produce *different* tree-sitter outputs from local builds? Without committed `Cargo.lock` we can't reproduce CI's exact toolchain — relevant if someone files a bug "indexer extracts X locally, Y in CI."
- Should `leonard doctor` surface the dangling-`../`-path-escape leftovers (62 stale paths in the live DB) as a "would you like to prune these?" recommendation, vs requiring a manual `leonard index` re-walk to clean them up?
- Is there an intent to add CI for the Inspect eval (mockllm smoke as a pre-commit / per-PR check)? Bughunt-3 eval F13 hinted at it; nothing landed in `ci.yml`.
- The 38 MB treesitter helper binary is large enough that a `homebrew` formula or `cargo install` distribution path may want to be feature-flagged (one grammar at a time). Out of scope here but worth a decision before shipping a versioned release.
- Where did "task #35" live? The brief references it but no in-repo tracking artifact mentions it — Vikunja or similar external tracker?

## Out of scope for this investigation

- Performance benchmarking of `find_symbol` / `verify_symbol` against 50k-symbol projects (bughunt-5 perf-and-resource territory).
- The TypeScript / Python / Rust / tree-sitter parsers themselves (bughunt-3 and -5 rust/treesitter rounds).
- Hook payload schemas vs Claude Code's actual envelope (bughunt-1, -2, -4 hooks rounds).
- Inspect eval scoring methodology (binary vs fraction-of-fabricated) — bughunt-3 eval F8 already covered this.
- Telemetry / OTel internals — bughunt-3 otel round.
- DESIGN.md authorship / OSS readiness polish — separate effort.
