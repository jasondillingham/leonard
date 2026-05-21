# Bug Hunt #3 — skip-dirs and prune

## Summary

Stress-tested v0.6.1's `defaultSkipDirs` + `pathHasSkippedComponent` matcher and v0.7.0's batched `Store.DeleteFiles`. The matcher itself is correct as a substring-vs-component check — every adversarial path I threw at `pathHasSkippedComponent` produced the expected answer (substring matches like `target-lang`, `venvironment`, `my-vendor` do NOT false-positive). The interesting findings are elsewhere:

- **High:** Bench-comment misattributes the 5.8s DeleteFiles_1k cost to "WAL fsync" — it's actually the **missing index on `symbols.parent_id`**. The FK cascade walks an unindexed self-reference, turning a 1k-file delete from ~40 ms into ~6 s. Adding `CREATE INDEX idx_symbols_parent ON symbols(parent_id)` produces a ~180× speedup on the existing benchmark, much larger than v0.7.0's already-shipped batching win.
- **Medium:** Case-sensitive skip-dir matcher misses uppercase variants (`VENDOR/`, `Target/`), and on macOS' default case-insensitive-but-preserving APFS this lets vendored junk slip past the walker.
- **Medium:** A source dir the user explicitly named `build/` (or `dist/`, `target/`) is silently dropped — no warning, no config knob, no per-project override. The `.leonardignore` lane only adds ignores; it can't subtract from `defaultSkipDirs`.
- **Medium:** A file moved into a skip-dir on disk gets removed from the index entirely (old row pruned because file vanished from `src/`; new path under `archive/build/` was never indexed). The "still on disk, just relocated" case is invisible to the user.
- **Low:** `DeleteFiles` chunk size of 500 is 65× more conservative than necessary — modernc.org/sqlite v1.50.1's empirical max is 32766 variables. Bench shows zero throughput difference at any chunk size from 500 to 32000.
- **Low:** `DeleteFiles` returns a non-zero `total` on a fully-rolled-back batch when a later chunk's Exec fails. Caller logs "deleted N rows" but nothing was committed.
- **Informational:** Walker correctly does not follow symlinks (Go's `filepath.WalkDir` doesn't), so a `vendor` symlink pointing OUT of the project is just skipped by name. A *non-skip-named* symlink pointing out is also not followed.
- **Informational:** Two concurrent `leonard index` invocations against the same DB both complete cleanly with the 5s busy_timeout. No `database is locked` errors at N=2 on this workload.

## Findings

### F1 — DeleteFiles bottleneck is unindexed `symbols.parent_id`, not WAL fsync

- **Severity:** high
- **Reproducer:** Run the existing `BenchmarkDeleteFiles_1k`, then re-run after adding an index on `parent_id`. Scratch program at `<repo>/.bughunt3-scratch/parent_probe.go` reproduces both with and without the index, with and without populated parent links. Results on Apple M1 Pro:
  ```
  no idx_symbols_parent, no parent links     7.13 s
  WITH idx_symbols_parent, no parent links   37.87 ms
  no idx_symbols_parent, WITH parent links   6.03 s
  WITH idx_symbols_parent, WITH parent links 41.93 ms
  ```
  Additional WAL-bypass test (`<repo>/.bughunt3-scratch/chunkbench.go`) shows journal_mode=delete, memory, off all run within 1-2% of WAL on the same workload — WAL is NOT the bottleneck.
- **Observed:** The `BenchmarkDeleteFiles_1k` comment in `internal/store/delete_files_test.go:108` reads "pure SQLite FK-cascade + WAL fsync cost." The benchmark consistently lands at ~5.8 s/op. With `journal_mode=off` it lands at ~5.9 s/op. Variance is noise. The cost is the FK CASCADE on `symbols.parent_id` — without `idx_symbols_parent`, every parent-row deletion triggers a full scan of `symbols` looking for children to cascade into. For 10k symbol rows × 1k file deletes that's quadratic.
- **Expected:** The v0.7.0 commit message frames batching as a `~225×` win over per-row delete. Adding the missing index on top is a further ~150× win on this same workload. A polluted-index sweep that takes 5.8 s today should take well under 100 ms.
- **Suggested fix shape:**
  1. Add migration v5: `CREATE INDEX idx_symbols_parent ON symbols(parent_id)`.
  2. Update the benchmark comment — "pure cascade + WAL fsync" is misleading; the dominant term is FK-cascade-without-index.
  3. Consider whether `parent_id` warrants any other lookups in the codebase. If not, the index is essentially write-amplification mitigation for cascade only, which is fine.
- **Out of scope for this investigation:** Whether other FK cascades in the schema have the same problem. `decisions.superseded_by` and `claims.superseded_by_claim_id` both reference their own tables and would have analogous unindexed-cascade cost if there's ever a `DELETE FROM decisions` or `DELETE FROM claims`. Today nothing does those, so not urgent.

### F2 — Case-sensitive skip-dir match misses ALL-CAPS / Title-Case on macOS

- **Severity:** medium
- **Reproducer:** `<repo>/.bughunt3-scratch/main.go` scenario C. Creates `VENDOR/x.go`, `Target/x.rs`, `vendor/y.go` in a temp project. Output:
  ```
  indexed: Target/x.rs
  indexed: VENDOR/x.go
  indexed: VENDOR/y.go
  ```
  (`vendor/y.go` and `VENDOR/x.go` collide on APFS — `VENDOR` is the on-disk dirname; both `vendor/` and `VENDOR/` writes landed there. `Target/x.rs` would normally be skipped but is happily indexed.)
- **Observed:** The map check `defaultSkipDirs[d.Name()]` is exact-case. On macOS APFS (default, case-insensitive-but-preserving) a user who mistypes `Vendor/` once at clone time gets the casing baked into the filesystem entry and the matcher will not fire.
- **Expected:** Per the v0.6.1 intent (catch ecosystem build dirs regardless of who set them up), case-insensitive match would catch the common `Build/`, `Target/`, `Dist/` typos seen in IDE-generated projects. There's no real-world case where a directory legitimately named `VENDOR/` should be indexed.
- **Suggested fix shape:** Normalize the directory name with `strings.ToLower(d.Name())` before the map lookup, both in the walker and in `pathHasSkippedComponent`. If you want platform-aware behavior, only do this on `runtime.GOOS == "darwin" || runtime.GOOS == "windows"` — but the simpler fix is to always lowercase.
- **Out of scope for this investigation:** Whether case-insensitive matching could *over*-match on Linux ext4 (where `VENDOR/` and `vendor/` are two distinct dirs). It can, but the userbase that names a source directory `VENDOR/` on a case-sensitive FS is the empty set.

### F3 — Walker silently drops user-named `build/` (or `dist/`, `target/`, etc.) source dirs

- **Severity:** medium
- **Reproducer:** `<repo>/.bughunt3-scratch/main.go` scenario B. Project layout:
  ```
  cmd/build/main.go   // user-chosen name for a `build` CLI command
  cmd/run/main.go
  ```
  Output: `indexed: cmd/run/main.go` only. The entire `cmd/build/` subtree is invisible to Leonard. No warning, no log, no parse-failure entry.
- **Observed:** `defaultSkipDirs` treats any directory whose basename matches a skipped term as junk, regardless of context. Leonard itself ships with no `cmd/build/`, but real Go projects do — `goreleaser/cmd/build`, `kubernetes/cmd/build`, etc. Same problem for `dist/` (some teams put production-served HTML under it), `target/` (less common in Go but exists), `vendor/` (Go's standard vendoring dir IS source code in some workflows — though admittedly less so since Go modules).
- **Expected:** Either (a) match more selectively (e.g., only skip `build/` if a sibling indicator like `Makefile` / `CMakeLists.txt` exists, or only skip `target/` if a sibling `Cargo.toml` exists), or (b) provide a per-project escape hatch — e.g., honor a `!build/` line in `.leonardignore` to mean "include this even though the default is to skip." Today, `.leonardignore` is *additive* — there's no way to *un*-skip a default.
- **Suggested fix shape:** Add a `[index].include_dirs` (or similar) knob to config.toml that adds names to a denylist-of-the-skip-list. Simpler: support a `!`-prefixed pattern in `.leonardignore` that overrides the default skip. The bug hunt rounds previously removed `[index].ignore` from config; with 15 hardcoded names now this is a reasonable time to reintroduce a per-project knob — see F8.
- **Out of scope for this investigation:** Whether the file extension check makes this safer (e.g., `cmd/build/main.go` is `.go` and we'd only skip it because the *dir* is skipped, not the *file*; a `build/output.txt` file would be skipped both ways).

### F4 — File relocated into a skip-dir disappears from the index, no warning

- **Severity:** medium
- **Reproducer:** `<repo>/.bughunt3-scratch/main.go` scenario E. Index `src/foo.go`, then `mv src/foo.go archive/build/foo.go`, then re-index. Output:
  ```
  initial files: [src/foo.go]
  after move:    [] (file is still on disk at archive/build/foo.go)
  ```
- **Observed:** Prune sweep correctly removes `src/foo.go` (file vanished). The walker doesn't index `archive/build/foo.go` because the path traverses `build`. Net effect: the symbol is gone from Leonard's index even though the user can still see the file on disk. `find_symbol` / `verify_symbol` will silently lie ("no such symbol"). The CLI doesn't print "pruned N rows" so the user has no breadcrumb.
- **Expected:** At minimum, surface what got pruned. Ideally, a heuristic that says "this file was last seen at src/foo.go with hash X, and a file with hash X now exists at archive/build/foo.go — the move into a skip-dir is suspicious, do you want to add a `!archive/` rule to `.leonardignore`?" That's gold-plating, but at least pruning N rows should be logged.
- **Suggested fix shape:**
  1. `pruneStaleFiles` should return the count + categorize (skipdir vs missing).
  2. The CLI's `leonard index` summary should print `Pruned N stale rows (M moved into skip-dirs).` so the user sees the cleanup happen.
  3. (Optional) When a prune-by-skipdir hits the same hash that a prune-by-missing also removed, log "looks like a file move into a skipped dir."
- **Out of scope for this investigation:** Whether anyone actually relies on this behavior. Plausibly some teams move dead-code archives into `archive/`, and skip-dir-pruning that path is what they want. The fix should be opt-in clarity, not changed semantics.

### F5 — `DeleteFiles` chunk size 500 is 65× too conservative

- **Severity:** low
- **Reproducer:** `<repo>/.bughunt3-scratch/maxvars.go` empirically probes the SQLite variable cap on modernc.org/sqlite v1.50.1:
  ```
  n=500    OK
  n=999    OK
  n=1000   OK
  n=32766  OK
  n=32767  ERROR SQL logic error: too many SQL variables (1)
  ```
  `<repo>/.bughunt3-scratch/chunkbench.go` confirms no perf difference between chunk size 500 and 32000 on the same 1k-row workload (5.9 s vs 6.2 s, within noise).
- **Observed:** `internal/store/store.go:518` comment says "well under SQLite's default 999-parameter cap." That comment was true for SQLite ≤3.32; since 3.32 (Aug 2020) the default rose to 32766. Modernc.org/sqlite v1.50.1 ships SQLite 3.45.x. So the actual cap is 32766. The "well under 999" framing leaves ~65× of headroom on the table for no reason.
- **Expected:** Since the bench shows no benefit at higher chunk sizes for this workload, this is mostly a comment-rot issue rather than a perf issue. But for the polluted-index case (12k+ rows), chunk size 32000 would do it in one chunk instead of 24, which slightly reduces query-plan overhead.
- **Suggested fix shape:** Bump `chunkSize` constant to 1000 or 2000 (still safe headroom over old-SQLite-default of 999 for the rare case someone builds against ancient SQLite); update the comment to reflect the actual 32766 cap. Don't go all the way to 32000 because (a) no measurable benefit and (b) larger placeholder strings cost CPU to build.
- **Out of scope for this investigation:** Whether anyone builds Leonard against a SQLite older than 3.32. The modernc.org/sqlite Go driver bundles its own SQLite source, so unless someone vendors a stale version, this is academic.

### F6 — `DeleteFiles` returns misleading total on partial-chunk failure

- **Severity:** low
- **Reproducer:** Static analysis of `internal/store/store.go:548-553`:
  ```go
  res, err := tx.Exec("DELETE FROM files WHERE path IN ("+placeholders+")", args...)
  if err != nil {
      return total, fmt.Errorf("store: DeleteFiles: exec chunk %d-%d: %w", start, end, err)
  }
  n, _ := res.RowsAffected()
  total += int(n)
  ```
  If a later chunk Execs an error (disk full, locking timeout, etc.) after earlier chunks have already incremented `total`, the function returns `(total, err)`. The `defer tx.Rollback()` then undoes all the earlier deletes. Caller sees a non-zero count and a non-nil error.
- **Observed:** A caller that does `n, err := s.DeleteFiles(paths); if err != nil { log.Printf("deleted %d before error: %v", n, err) }` will print a misleading number. The current sole caller (`pruneStaleFiles`) discards the count on error (`if _, err := i.Store.DeleteFiles(toDelete); err != nil { return err }`), so the live bug is dormant. But the contract is wrong on its face — the function says it "returns the count of file rows actually deleted" and on the error path the count is rows DELETE-tagged but not committed.
- **Expected:** Return `(0, err)` on chunk-Exec failure, since the rollback means zero rows persisted.
- **Suggested fix shape:** On chunk-Exec error, `return 0, fmt.Errorf(...)`. Or: keep the running total but document it as "rows the DELETEs reported affected before rollback, for diagnostic purposes only."
- **Out of scope for this investigation:** Whether the `defer tx.Rollback()` actually executes if `tx.Commit()` already ran. Go's `sql` package treats post-Commit Rollback as a no-op (`sql.ErrTxDone`), and the deferred call ignores the error, so this is fine — just worth knowing.

### F7 — `.leonardignore` only at project root; no override for `defaultSkipDirs`

- **Severity:** informational
- **Reproducer:** Read `internal/index/indexer.go:371-389`. `loadIgnore` only reads `.gitignore` and `.leonardignore` at `i.Root` — sub-directory ignore files are not consulted. Combined with the additive-only semantics, a user has no way to say "for this project, please DO index `cmd/build/`."
- **Observed:** A user whose project legitimately has source under a default-skipped name has exactly one workaround: rename the directory. There's no escape hatch.
- **Expected:** Either:
  - Subdirectory `.gitignore` / `.leonardignore` files are honored (matches git's actual semantics), OR
  - `.leonardignore` supports `!path` syntax to un-skip a default, OR
  - `config.toml` grows `[index].skip_dirs = [...]` to fully override.
- **Suggested fix shape:** Pick one — simplest is `.leonardignore` `!`-prefix syntax since `go-gitignore` already supports negation patterns. The matcher would need a small change to consult negation patterns BEFORE the `defaultSkipDirs` check.
- **Out of scope for this investigation:** Whether sub-dir `.gitignore` support is worth the complexity. Git itself honors them; doing the same for Leonard would mean walking up the tree at every level. Probably not worth it for the current scope.

### F8 — Time to reintroduce a config knob for skip dirs

- **Severity:** informational
- **Reproducer:** `grep -rn skip_dir internal/config/` — no knob exists. `internal/config/config.go:23-25` notes "[index].ignore was removed because ignore paths are handled by .gitignore + .leonardignore." That decision pre-dated v0.6.1's expansion from `{vendor, node_modules, .git, build, dist}` to 15 names.
- **Observed:** The skip list is hardcoded at Go-compile time. Every new ecosystem added (Rust→target, Python→.venv, Next.js→.next, etc.) requires a Leonard release. The list is also unconditionally global — a project that wants `target/` but skips `node_modules/` has no recourse.
- **Expected:** A `[index].skip_dirs` array in config.toml (or `~/.leonard/config.toml` for user-wide defaults) that the indexer merges with its built-in list. Either union (add to default) or replace (override default).
- **Suggested fix shape:** Add `[index].skip_dirs []string` and `[index].extra_skip_dirs []string` — the former replaces the default, the latter unions with it. Document in config.go's package comment.
- **Out of scope for this investigation:** Whether `.leonardignore` `!`-syntax (F7) is a sufficient escape hatch on its own — possibly yes, depending on UX priorities.

### F9 — Prune sweep is silent on the CLI

- **Severity:** informational
- **Reproducer:** Run `leonard index` on a project with stale + skip-dir rows. Compare CLI output to actual DB state. The "we just pruned X rows" event is invisible.
- **Observed:** `pruneStaleFiles` returns `error` only, not count. The CLI's `leonard index` summary (per `cmd/leonard/`) reports files-parsed and parse-failures but nothing about pruned rows. A user who's wondering why `verify_symbol` no longer finds their symbol has no breadcrumb back to "Leonard pruned the row because the file moved into a skip-dir."
- **Expected:** Either a summary line or a `--verbose` flag that prints the prune list.
- **Suggested fix shape:** `pruneStaleFiles` returns `(prunedSkipdir, prunedMissing int, err error)`; the CLI prints `Pruned N stale rows (M missing on disk, K under skip-dirs).` when both are >0.
- **Out of scope for this investigation:** Whether the `leonard doctor` command (referenced in code) already surfaces this via a different path.

### F10 — `pruneStaleFiles` loads every file row into memory

- **Severity:** informational
- **Reproducer:** Read `internal/index/indexer.go:209-237`. `i.Store.ListFiles("", "")` returns `[]store.File`. For a 100k-row index, that's 100k structs (each with a path + hash + lang strings) held simultaneously, then iterated with an `os.Stat` per row.
- **Observed:** Each row is ~150-300 bytes of allocated memory. At 100k rows that's ~25 MB transient, plus the syscall storm of 100k stats. Not a problem at today's scale (Leonard self-index is ~150 rows), but the v0.6.1 commit message specifically calls out "polluted reindex picked up ~12k site-packages rows" — that's already enough to make the stat loop noticeable.
- **Expected:** Either stream rows through `Query()` directly, or do the prune logic in SQL (`DELETE FROM files WHERE NOT EXISTS (SELECT 1 ... )` is hard to express because the existence check is on the OS, but the skip-dir half of the prune COULD be done in SQL as `DELETE FROM files WHERE path GLOB '*/vendor/*' OR ...`).
- **Suggested fix shape:** Two-phase prune. Phase 1: `DELETE FROM files WHERE path GLOB ANY (skipdir-globs)` in SQL — handles the upgrade-cleanup case entirely without loading rows. Phase 2: `SELECT path FROM files` streaming + os.Stat for the missing-on-disk case. Even better with `Cursor`-style iteration so we don't allocate the whole slice. Not urgent at current scale.
- **Out of scope for this investigation:** Profiling actual memory at 100k+ rows to confirm the concern.

## Things that worked

- **`pathHasSkippedComponent` is correct against substring false-positives.** Verified against 30+ adversarial paths (`<repo>/.bughunt3-scratch/skip_dirs_probe.go`): `target-lang`, `venvironment`, `my-vendor`, `node_modules_old`, `.git-extras` etc. all correctly return false. The split-on-`/` approach is the right shape.

- **Forward-slash invariant holds end-to-end.** `storeKey` (`internal/index/indexer.go:356-362`) always converts via `filepath.ToSlash` before persisting. `pruneStaleFiles` (`indexer.go:220`) always converts back via `filepath.FromSlash` before `os.Stat`. The matcher's `strings.Split(rel, "/")` is therefore safe — no Windows backslash leak via the indexer.

- **Walker does not follow symlinks.** Verified scenarios F and G in `<repo>/.bughunt3-scratch/main.go`. A `vendor` symlink pointing OUT of the project is just skipped by name. A non-skip-named symlink (`src`) pointing OUT is also not followed — `filepath.WalkDir` returns the entry as a non-directory, and the walker's directory branch never fires. So symlink loops are not a concern.

- **`DeleteFiles` chunk boundary math is correct at 501.** Scenario H in the integration probe: 501 paths produces two chunks (500 + 1), the placeholder-trim in the second chunk works (single `?` remaining trims correctly to a single `?`), all 501 rows are deleted.

- **`DeleteFiles` is safe against weird-named paths.** Single quote (`o'connor.go`), double quote (`he said "hi".go`), semicolon, backslash, Unicode (`unicode-π-θ-ω.go`, emoji), and a 250+ char path all delete correctly. Parameterized IN clause does its job — no SQL injection vector.

- **Two concurrent `leonard index` invocations work.** N=2 with 250 rows + prune work completes in 34 ms with zero errors. WAL + the 5s `busy_timeout` is adequate for this case.

- **`DeleteFiles([])` and `DeleteFiles(nil)` are cheap.** Returns `(0, nil)` without opening a transaction. Existing test `TestDeleteFiles_EmptyIsCheap` covers this.

- **Prune-on-skip cleanup works end-to-end.** Scenario D: pre-pollute a fresh DB with 5 rows under skip-dirs + 1 missing-on-disk row + 1 real row. After `IndexAll`, only the real row survives. The v0.6.1 upgrade-cleanup story holds.

## Open questions

- **Should `defaultSkipDirs` lowercase its inputs?** F2 says yes for macOS / Windows users; on Linux it'd cause occasional over-skip. Bias toward yes — the cost of indexing a `Vendor/` typo is much worse than the cost of NOT-indexing some hypothetical `VENDOR/` source dir (essentially zero such dirs exist in the wild).

- **Is `cmd/build/` in real OSS projects common enough to be worth fixing F3?** Quick sample of large Go repos: kubernetes/cmd has no `build` subdir; goreleaser has `cmd/build/` (it's THE main entry point for the project!); pulumi has `cmd/pulumi-language-go` but no `cmd/build`. The set is non-empty. Affects subjective importance of F3.

- **Should `pruneStaleFiles` be opt-in?** Some users might want stale rows to persist as a kind of "code archaeology" — knowing what was here three weeks ago. Today there's no opt-out. The bughunt-2 cli F2 fix made it the default; no UX feedback yet on whether anyone wants the old behavior back.

- **Is the migration v5 add-index-on-parent_id safe to run on existing DBs?** The migration is idempotent at the SQL level (`CREATE INDEX IF NOT EXISTS`) and SQLite indexes are built atomically. For a Leonard user with a populated DB, the migration is one quick scan of `symbols` — at typical scales (~5k rows), instant. At polluted-index scales (100k+ rows) it might be a one-time second of delay on first `leonard index` after upgrade. Worth flagging in the migration's comment but not a blocker.

- **F5 chunk-size bump: is 1000 the right new default, or 2000?** No measurable difference in either direction. Choosing 1000 keeps the placeholder-string build cheap and stays within every historical SQLite default I can find.

- **Should `loadIgnore` also read `.dockerignore`?** Some projects use `.dockerignore` as their canonical "stuff that's not source" list — Leonard could opt to read it as a third source alongside `.gitignore` and `.leonardignore`. Out of scope here but worth a separate decision.

- **Scratch artifacts** under `<repo>/.bughunt3-scratch/` (cleaned up after the fix round; no longer present in the repo). The lane produced:
  - `main.go` — integration probe (scenarios A-I)
  - `maxvars.go` — SQLite max-variable cap probe
  - `chunkbench.go` — journal_mode × chunk_size matrix
  - `cascade_probe.go` — `EXPLAIN QUERY PLAN` on the cascade path
  - `parent_probe.go` — the headline F1 finding (180× speedup demo)
  - `concurrent.go` — N=2 race probe
  - `partialfail.go` — F6 static-analysis note
