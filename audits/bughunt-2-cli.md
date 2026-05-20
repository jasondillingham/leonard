# Bug Hunt #2 — cli

## Summary

Audited every command under `cmd/leonard` (`init`, `index`, `verify`, `decisions {list,add,stale}`, `claims unverified`, `doctor`, `mcp`) at build current. The new v0.2 surface (decisions, claims, doctor) is structurally sound — concurrent writes are clean, exit codes work where they're set, JSON-free output is consistent enough to grep — but several **correctness** and **documentation-drift** bugs are live. The two highest-impact findings: `leonard init` silently destroys a user-customized `config.toml` on re-run, and the indexer never deletes file rows for files removed on disk (so `verify` keeps returning matches for deleted code, even after `leonard index` is run as `doctor` instructs). Two medium findings: `LEONARD_PYTHON` is documented in error text but never actually read, and `leonard mcp --help` produces zero output (DisableFlagParsing eats the flag). A handful of UX gotchas round out the rest: CLI doesn't walk up to find `.leonard/`, `doctor` always exits 0 even with issues, `decisions stale` accepts neither `--topic` nor `--since` (inconsistent with `list`), and CLI `--limit 0` returns store defaults (50/200) rather than the MCP defaults (20/50) the help text advertises.

## Findings

### F1 — `leonard init` silently overwrites a customized `config.toml`
- **Severity:** high
- **Reproducer:**
  ```bash
  rm -rf /tmp/leonard-cfg && mkdir /tmp/leonard-cfg && cd /tmp/leonard-cfg
  leonard init .
  printf '\n[mysetting]\nmagic = 42\n' >> .leonard/config.toml
  leonard init .              # second invocation
  grep mysetting .leonard/config.toml   # gone
  ```
- **Observed:** The second `leonard init` rewrites `.leonard/config.toml` from `config.Default()` with no check. `[mysetting]` is gone. There is no warning, no prompt, no backup.
- **Expected:** Init's help text claims "Safe to re-run — the underlying schema migration is idempotent." Idempotent for the schema, sure; user data should not be silently destroyed. Either skip the write when the file exists, write only if absent, or compare-and-merge.
- **Suggested fix shape:** In `wire_real.go:Init`, stat `cfgPath` before `config.Save(...)`. If the file exists, skip the write (or load + write back only newly-introduced fields). Optionally log "leonard: existing config.toml left untouched" so the user knows.
- **Out of scope:** Whether config fields should even be honored by the CLI (separate finding — F5).

### F2 — `leonard index` never deletes file rows for files removed on disk
- **Severity:** high
- **Reproducer:**
  ```bash
  mkdir /tmp/leonard-del && cd /tmp/leonard-del
  cat > a.go <<'EOF'
  package main
  func Ghost() {}
  EOF
  leonard init . && leonard index
  rm a.go
  leonard index                          # claims "indexed 0 file(s)" or stale count
  leonard verify Ghost                   # exit 0, still returns the match
  leonard doctor                         # shows 1 stale file, says "run leonard index"
  leonard index                          # still doesn't fix it
  leonard verify Ghost                   # STILL returns the deleted symbol
  ```
- **Observed:** Deleted files leave their `files` + `symbols` rows in the store forever. `leonard verify` happily reports them as live. `leonard doctor` correctly detects the staleness and prints `(file row in store but missing on disk — run \`leonard index\`)` — but the suggested fix doesn't work because `IndexAll` only upserts files it sees on disk and never deletes orphans.
- **Expected:** A full re-index should converge the store to the current filesystem state. Either `IndexAll` enumerates the existing `files` rows and removes any not seen during the walk, or `doctor` offers a separate prune action, or both. As-is, the "ground truth" promise of Leonard fails the moment a user renames or deletes a file.
- **Suggested fix shape:** In `internal/index/indexer.go:IndexAll`, accumulate the relative paths visited during the walk, then `s.DeleteFile(rel)` (a new store method) for any `files` row whose path is not in that set. Tie this into a single transaction so a mid-walk crash doesn't leave half-pruned state.
- **Out of scope:** Whether the post-edit hook should also call something on file deletion (Claude Code's delete signal would be the trigger, but hooks lane already covers that).

### F3 — `LEONARD_PYTHON` env var documented in error text but never read
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/leonard-py && mkdir -p . && cd .
  cat > t.py <<'EOF'
  def hi(): pass
  EOF
  leonard init . && LEONARD_PYTHON=/totally/missing/python leonard index
  ```
- **Observed:** `internal/parse/python.go` declares `var pythonInterpreter = "python3"` and uses `exec.LookPath(pythonInterpreter)` — but never reads `os.Getenv("LEONARD_PYTHON")`. Setting the env var has zero effect; the standard `python3` is still used. The user-facing error string `parse: python3 not found on PATH (set LEONARD_PYTHON to override)` advertises an override that doesn't exist.
- **Expected:** Either the env var should actually be read (lazy `sync.Once` lookup that checks `os.Getenv("LEONARD_PYTHON")` first, falling back to `"python3"`), or the error message should not promise an override that isn't wired up. The leading code comment also says "Override via the LEONARD_PYTHON environment variable when the host has python3 under a non-standard name (uv, pyenv shims, etc.)" — which is currently a lie.
- **Suggested fix shape:** Add a once-guarded resolver in `python.go` that prefers the env var. Keep the existing default. Update the error string to match whichever direction wins.
- **Out of scope:** Same fix for `LEONARD_TSC`-style overrides for the TS parser (it's pure Go, no interpreter, so doesn't apply).

### F4 — `leonard mcp --help` silently produces zero output
- **Severity:** medium
- **Reproducer:**
  ```bash
  leonard mcp --help         # no output, exit 0
  leonard mcp -h             # no output, exit 0
  leonard help mcp           # actually shows help
  ```
- **Observed:** Because `newMCPCmd()` sets `DisableFlagParsing: true` (to pass everything through to leonard-mcp), `--help` is treated as just another argv element and forwarded. `leonard-mcp` swallows it silently. The user discovers nothing.
- **Expected:** `--help`/`-h` should behave like every other subcommand: print cobra's help block and exit. The whole point of disabling flag parsing is positional arg passthrough, but `--help` is a strong convention to honor.
- **Suggested fix shape:** Either (a) drop `DisableFlagParsing` and use `Args: cobra.ArbitraryArgs` (cobra will still pass unknown args through if no flags are declared on the cmd); or (b) keep `DisableFlagParsing` and intercept `args[0] == "--help" || "-h"` inside `RunE` to print the cmd's help block before exec'ing. Approach (a) is cleaner.
- **Out of scope:** Whether `leonard mcp` should accept flags at all — it doesn't, in practice.

### F5 — `.leonard/config.toml` is dead code from the CLI's perspective
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/leonard-cfg-honored && leonard init .
  echo "this is not valid TOML !!!" > .leonard/config.toml
  leonard index             # works fine
  leonard doctor            # works fine
  leonard verify Foo        # works fine
  echo '[index]
  languages = []
  ignore = ["src/"]' > .leonard/config.toml
  # creates src/foo.go ...
  leonard index             # still indexes src/foo.go — config.Ignore ignored
  ```
- **Observed:** No CLI command reads `config.toml`. `IndexConfig.Languages` is not consulted by the indexer (which dispatches by extension via a hardcoded map in `internal/index/indexer.go:langExtractors`). `IndexConfig.Ignore` is similarly ignored — only `.gitignore` and `.leonardignore` are honored. `VerifiersConfig` is read by post-edit hook only. `HooksConfig` is read by hooks only. From the user's mental model, the file Leonard writes during `init` is configuration; in reality it's a comment.
- **Expected:** Either honor the documented knobs (DESIGN.md §4.6 implies `[index]` controls indexing) or remove the config file from `init`'s output and document the knobs as hook-only. The current state where init writes a file the rest of the CLI ignores is misleading.
- **Suggested fix shape:** In `wire_real.go`, load `config.LoadOrDefault(cfgPath)` once at the top of `IndexAll`, push the slice into the Indexer ctor (`index.New(s, root, cfg.Index)`), and have the walker honor `Languages` (filter extensions) and `Ignore` (append to gitignore matcher). Or — if scope-creep — minimally fix DESIGN.md and init's output to be honest about which config fields are live.
- **Out of scope:** Whether config knobs should be CLI flags too (probably yes, but separate work).

### F6 — `leonard doctor` always exits 0 even when issues are present
- **Severity:** medium
- **Reproducer:**
  ```bash
  # set up project with stale files / unverified claims / parse failures
  leonard doctor; echo "exit: $?"     # exit: 0 regardless
  ```
- **Observed:** `cmd/leonard/doctor.go:newDoctorCmd` never returns a non-zero status. A CI script that wants to gate a build on "no parse failures, no stale files, no unverified claims" has no exit-code signal — it has to parse human-readable output.
- **Expected:** Either document the exit-code contract for doctor ("doctor exits 0 always; parse the output for issues") or make doctor return non-zero when `EmptyFiles + StaleFiles + UnverifiedClaims + StaleDecisionCount > 0`. The verify command explicitly documents `0/1/2`; doctor should too.
- **Suggested fix shape:** Add a `--strict` flag that flips doctor to exit 1 when issues are surfaced (keeps default behavior backward-compatible). Or split into exit 0 / exit 3 ("issues"). Either way, document the contract.
- **Out of scope:** Adding JSON output (F12).

### F7 — `decisions list --limit 0` returns 50, not the 20 the help text promises
- **Severity:** medium
- **Reproducer:**
  ```bash
  # populate >50 decisions
  for i in {1..60}; do leonard decisions add "t$i" c r; done
  leonard decisions list --limit 0   # "leonard: 50 decision(s)"
  leonard decisions list --limit 500 # "leonard: 60 decision(s)" — no cap
  ```
- **Observed:** CLI help says `"--limit int  max rows to return (0 = MCP default, currently 20)"`. The CLI bypasses the MCP layer entirely and calls `s.GetDecisions(topic, since, limit)` directly. Store default is 50 (`internal/store/store.go:550`). MCP's default-and-cap of 20/200 (`internal/mcp/decisions.go:89-90`) never fire. Similarly `decisions stale --limit 0` returns 200 (store default) instead of the 50 the help text claims, and `--limit 500` returns >200 (no cap at all).
- **Expected:** Either align the CLI defaults with the MCP defaults (the help text's stated contract) or update the help text to reflect the store defaults. The current state is documentation drift.
- **Suggested fix shape:** In `cmd/leonard/decisions.go`, replicate the MCP layer's clamps (`if limit <= 0 { limit = 20 }; if limit > 200 { limit = 200 }`) before passing through to the runtime. Or, cleaner, route CLI decision reads through `internal/mcp.getDecisions`-equivalent helpers shared with the MCP server.
- **Out of scope:** Whether the MCP defaults are the right defaults (separate UX call).

### F8 — `decisions stale` rejects `--topic` and `--since`, inconsistent with `list`
- **Severity:** low
- **Reproducer:**
  ```bash
  leonard decisions stale --topic auth     # exit 2, "unknown flag: --topic"
  leonard decisions stale --since 100      # exit 2, "unknown flag: --since"
  ```
- **Observed:** `decisions list` accepts `--topic` and `--since` but `decisions stale` only accepts `--limit`. A user inspecting stale-only decisions on a given topic has no filter knob.
- **Expected:** Either add the missing flags to `stale` (the store's `GetStaleDecisions` would need a minor signature widening), or document explicitly that stale-side filtering isn't supported. As-is the cobra surface looks inconsistent for no documented reason.
- **Suggested fix shape:** Add `--topic` and `--since` to `newDecisionsStaleCmd`. Optionally widen `store.GetStaleDecisions` to accept the same filter args. Low-effort, high-consistency win.

### F9 — CLI does not walk up the tree to find `.leonard/`
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/leonard-walkup && mkdir -p internal/store && leonard init . && leonard index
  cd internal/store
  leonard verify Open      # "no .leonard here — run `leonard init` first" (exit 2)
  ```
- **Observed:** Every command that needs `.leonard/` checks only `cwd/.leonard` (`init.go:dataDirName` / `decisions.go:dataDirForCwd`). git, npm, hg, cargo all walk up to find their repo marker. Leonard does not. Result: running any command from a project subdirectory fails with a "run init first" message that is technically wrong (init has been run, just not in cwd).
- **Expected:** Walk up from cwd toward the root looking for `.leonard/`. Stop at the filesystem root. If not found, then emit the "run init first" message. This is also a precondition for hook handlers to behave sensibly when Claude is operating inside a subdirectory.
- **Suggested fix shape:** Add `resolveDataDirWalkUp(cwd string) (string, error)` to a shared helper; replace the existing `os.Stat(cwd + "/.leonard")` calls. Should also update the error message to "no .leonard found between cwd and filesystem root — run `leonard init` somewhere up the tree."
- **Out of scope:** Whether `leonard init` should refuse to create a nested `.leonard/` if one already exists higher in the tree (probably yes, but separate).

### F10 — `leonard decisions <unknown>` and `claims <unknown>` exit 0 instead of 2
- **Severity:** low
- **Reproducer:**
  ```bash
  leonard decisions supersede               # exit 0, prints help
  leonard decisions totallymadeupthing      # exit 0, prints help
  leonard claims madeup                     # exit 0, prints help
  leonard nonexistent                       # exit 2 (root level — correct)
  ```
- **Observed:** Subcommand groups without an explicit `RunE` (decisions, claims) fall back to cobra's default `usage` behavior when given an unknown subcommand, exiting 0. The root cmd correctly returns 2. The inconsistency means a CI script that runs `leonard decisions ${subcmd}` can't detect a typo'd subcmd via exit code.
- **Expected:** Unknown subcommands should exit non-zero everywhere. Cobra has `SuggestionsMinimumDistance` and you can set `RunE: func(...) error { return errors.New(...) }` on the parent to force this.
- **Suggested fix shape:** Set `Args: cobra.NoArgs` on parent commands without a default action and / or add a `RunE` that returns a "no subcommand specified" exit-2 error when `len(args) > 0 && args[0]` isn't a known sub.

### F11 — `leonard index` prints "indexed N file(s)" where N is *total files in store*, not files reparsed
- **Severity:** low
- **Reproducer:**
  ```bash
  leonard index    # "leonard: indexed 88 file(s)"
  leonard index    # "leonard: indexed 88 file(s)"  — but ParseCount() was 0
  ```
- **Observed:** `realRuntime.IndexAll` calls `s.ListFiles("", "")` after `idx.IndexAll()` and reports `len(files)`. The Indexer already exposes `ParseCount()` returning the number of files actually re-parsed; nothing reads it. So "indexed N files" is the *total in store*, including files that weren't touched (or even visited if pruning ever happens).
- **Expected:** "indexed N file(s)" should reflect actual work done (`re-parsed N file(s)`, `total N file(s) in store`). On a no-op re-run a user sees "indexed 88" and reasonably assumes 88 files were processed — which they weren't.
- **Suggested fix shape:** Print both: `leonard: re-parsed N of M file(s)`. Or rename the message to `leonard: index up-to-date (N file(s) in store)` when ParseCount==0.

### F12 — No JSON output mode anywhere
- **Severity:** low (informational, but limits CI integration)
- **Reproducer:** Try to parse any output programmatically.
- **Observed:** Every command emits human-readable, prose-formatted output mixing summary lines and detail rows. There is no `--json` / `--format json` flag, no NDJSON streaming, nothing. A CI script that wants to know "are there parse failures? list them" has to grep `leonard index` stderr — which works today but breaks the instant the output prose changes.
- **Expected:** At minimum `verify`, `doctor`, `decisions list/stale`, `claims unverified` should support a structured-output flag. The DTOs already exist in `root.go` (`SymbolMatch`, `DoctorReport`, `DecisionRow`, etc.) — just `json.Marshal` them under a flag.
- **Suggested fix shape:** Add `--json` to each read command; the implementation is mostly `if jsonOut { json.NewEncoder(out).Encode(payload) }`. Lock the JSON schema so it can be a versioned contract.

### F13 — `leonard init <new-path>` silently creates project dirs
- **Severity:** low
- **Reproducer:**
  ```bash
  leonard init /tmp/typo-i-meant-to-type   # exit 0
  ls /tmp/typo-i-meant-to-type/.leonard    # exists
  ```
- **Observed:** `wire_real.go:Init` uses `os.MkdirAll(dataDir, 0o755)` which silently creates any missing parent dirs. A typo'd path silently produces a new project dir somewhere unexpected.
- **Expected:** Either require the path to already exist (more surprising for the create-a-new-project case) or print "creating project dir at <path>" so the user notices. Other tools (`git init <newdir>` for example) print a clear "Initialized empty Git repository in /path/" — which Leonard sort of does, but doesn't say "created the parent directory in the process."
- **Suggested fix shape:** Stat the parent before MkdirAll. If the *project root* (not just `.leonard`) didn't exist, print "leonard: created project root <path>" in addition to the existing "initialized" line.

### F14 — `decisions add` accepts empty `choice` and empty `reasoning`, only empty `topic` is rejected
- **Severity:** low
- **Reproducer:**
  ```bash
  leonard decisions add t "" reasoning              # ok
  leonard decisions add t choice ""                 # ok (with "" as the reasoning arg)
  leonard decisions list
  # #N  ...  t →            ← arrow with no choice
  # #N  ...  t → choice
  #      (no reasoning line)
  ```
- **Observed:** The store rejects empty `topic` ("empty topic" error) but accepts empty `choice` and empty `reasoning`. The decision row is recorded; CLI output renders `topic → ` with a trailing arrow and no choice, which is uglier than just refusing the row.
- **Expected:** Either reject empty `choice` at the store level (probably correct — a decision without a choice is meaningless) or render "topic → (no choice)" instead of a dangling arrow.
- **Suggested fix shape:** Add an empty-choice guard in `store.RecordDecision` symmetric to the empty-topic one. Optionally do the same for `reasoning`.

### F15 — `decisions add` joins variadic args with single space, lossily
- **Severity:** low (informational)
- **Reproducer:**
  ```bash
  leonard decisions add t c "word1" "word2"        # stored as "word1 word2"
  leonard decisions add t c "word1  word2"         # stored as "word1  word2" — original preserved
  ```
- **Observed:** `decisions.go:newDecisionsAddCmd` does `strings.Join(args[2:], " ")` — so if a user types reasoning words as separate args, the original whitespace structure is lost (double-spaces become singles, etc.). Newlines in a single quoted arg are preserved.
- **Expected:** Documented behavior — the help text does say "everything after the choice is joined with spaces" so this is technically as-designed. Worth flagging only because users used to MCP's single-string reasoning field may not expect this. Could be solved by recommending single-quoted multi-line reasoning in the help text.
- **Suggested fix shape:** Tighten help text: `<reasoning...>` (the reasoning, in one quoted string or as multiple words to be joined with spaces). Or `cobra.ExactArgs(3)` + explicit guidance to quote.

### F16 — Claims output shows "file:" twice when post-edit hook stored "file: ..." in evidence
- **Severity:** low (cosmetic)
- **Reproducer:**
  ```bash
  # trigger a vet-failure post-edit hook against a file, then:
  leonard claims unverified
  # #N  ...  tool=Edit file=<path>; index=ok; go vet=failed
  #       file: <path>
  #       session: <id>
  #       file: <path>     ← duplicate; first line of evidence
  ```
- **Observed:** `cmd/leonard/claims.go:43-53` prints `r.FilePath` on its own labeled line. Then `firstLine(r.Evidence)` is printed — and the post-edit hook (`internal/hooks/post_edit.go:237/448`) starts the evidence text with `file: <path>\n...`. The CLI ends up printing `file: <path>` twice for the same row.
- **Expected:** Either skip the FilePath line when Evidence's first line already says it, or skip the firstLine(Evidence) when it begins with `file: `.
- **Suggested fix shape:** In `cmd/leonard/claims.go`, strip a leading `file: ` (or filter for "interesting" first line) from Evidence before printing. Cleaner: change the hook to not encode the path into Evidence at all, since there's a dedicated `file_path` column.

### F17 — Doctor's 512-byte parse-failure threshold misses small-file parse failures
- **Severity:** low
- **Reproducer:**
  ```bash
  # write a small TS file with a syntax error (under 512 bytes)
  cat > foo.ts <<'EOF'
  export function foo( {
  EOF
  leonard index             # surfaces the parse failure on stderr
  leonard doctor            # does NOT list foo.ts as a parse-failure suspect
  ```
- **Observed:** `wire_real.go:185` hardcodes `docFileSizeCeiling = 512` to filter out Go `doc.go` files (package-comment-only) from the parse-failure suspect list. Any genuinely-broken file under 512 bytes is invisible to doctor. The threshold is Go-specific and was set to "comfortably above the largest doc.go in this repo (~290 bytes)" per the inline comment — but Python and TS have no equivalent convention.
- **Expected:** Either store a "parse_failed" flag on the file row at index time (so doctor can distinguish "intentionally symbol-less" from "broken") or use language-aware thresholds. The current heuristic is brittle.
- **Suggested fix shape:** Add a `parse_failed BOOLEAN DEFAULT 0` column on `files`, set by the indexer when a parser returns an error. Doctor reads it instead of guessing from size.

### F18 — `doctor` reports the same stale file as both "parse-failure suspect" and "stale file"
- **Severity:** low
- **Reproducer:** Insert a phantom file row pointing at a non-existent path (or rm a small file mid-index session). Then `leonard doctor`.
- **Observed:** A file that exists in the `files` table but not on disk gets flagged in BOTH the "parse-failure suspects" list (since it has zero symbols and SizeBytes > threshold) and the "stale files" list. The user sees the same path twice in different sections, with two different remedies.
- **Expected:** A stale file is not a parse-failure suspect — it's gone. De-dup by excluding `StaleFiles` from the `EmptyFiles` pool.
- **Suggested fix shape:** In `wire_real.go:Doctor`, build the StaleFiles set first, then build EmptyFiles excluding anything already in StaleFiles.

### F19 — DESIGN.md §4.5 lists commands that don't exist or are renamed
- **Severity:** low (documentation)
- **Reproducer:** Compare DESIGN.md §4.5 block to `leonard --help`.
- **Observed:** DESIGN.md §4.5 lists:
  - `leonard reindex <path>` — not implemented; `leonard reindex` exits 2 with "unknown command"
  - `leonard decision add` / `leonard decision list` (singular) — actual command is `leonard decisions ...` (plural)
  - `leonard decision add` is described as "interactive decision recording" — actual impl is positional args (`leonard decisions add <topic> <choice> <reasoning>`)
  - MCP has `record_decision` / `supersede_decision` / `get_decisions` / `get_stale_decisions`; CLI exposes `record_decision`-equivalent (`add`) and the two reads but not `supersede`.
- **Expected:** DESIGN.md should match the binary, or vice versa. Per the brief, the design-doc audit was explicitly in scope.
- **Suggested fix shape:** Update §4.5 to: list `index` not `index + reindex` (single-file is via the post-edit hook, not a CLI), use plural `decisions`, drop the "interactive" claim, add `decisions stale` and `claims unverified` which are missing from the doc. Optionally consider adding a `decisions supersede` CLI sub for parity with MCP.

### F20 — `^C` during `leonard index` does not propagate cancellation
- **Severity:** low
- **Reproducer:** On a very large project, start `leonard index`, hit Ctrl-C. The process won't exit until the current file's parse finishes — and might not exit at all if it's deep inside a parse.
- **Observed:** `main.go` sets up `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` and passes the context all the way down. Every `realRuntime` method accepts `ctx` but discards it (`_`). No code path in `internal/index`, `internal/store`, or `cmd/leonard/wire_real.go` ever checks `ctx.Done()` / `ctx.Err()`. So the signal is acknowledged at the top but never acted on by the inner loops.
- **Expected:** A long-running command should honor cancellation. At minimum, the file-walk loop in `IndexAll` should `select` on `ctx.Done()` between files.
- **Suggested fix shape:** Plumb the context through `Indexer.IndexAll(ctx)` and check it at the top of the per-file callback. Same for store batch operations if they ever get added.

### F21 — Future-dated `last indexed` produces negative duration in doctor output
- **Severity:** informational
- **Reproducer:** Manually set `files.indexed_at` to a future timestamp via sqlite3, then `leonard doctor`.
- **Observed:** Doctor prints `last indexed: 2027-...  (-31535999s ago)` — negative seconds because `now.Sub(t)` underflows. Cosmetic only; happens in practice if a developer has clock skew on a CI runner.
- **Expected:** Clamp negative durations to "in the future" or "0s".
- **Suggested fix shape:** In `humanDuration`, `if d < 0 { return "in the future" }`.

### F22 — `verify --kind` accepts any string, silently produces no match
- **Severity:** informational
- **Reproducer:**
  ```bash
  leonard verify --kind notARealKind Foo
  # no error, exit 1 ("no match")
  ```
- **Observed:** The kind flag is passed to `realRuntime.VerifySymbol` and used as a string equality filter. A typo like `--kind funktion` silently returns "no match" — looks like the symbol doesn't exist when really the filter is wrong.
- **Expected:** Validate the flag against the documented kinds (the help text already lists `function|method|type|const|var|interface`). Either pre-validate in cobra (`StringVar` + custom validator) or echo a warning when the kind isn't in the closed set.
- **Suggested fix shape:** Use `cobra.Command.RegisterFlagCompletionFunc` and validate inside `RunE` before calling the runtime — return a clear "kind must be one of: ..." error.

## Things that worked

These behaviors I specifically verified hold up:

- **Exit code 1 on `verify` not-found** — documented in help text, honored. Exit 2 on store errors.
- **Stdout/stderr discipline on `leonard index`** — summary line goes to stdout; per-file parse failures go to stderr with a sample cap. Pipeable.
- **Concurrent `decisions add`** — 30 parallel writers all land, IDs sequential, no `database is locked` failures. SQLite WAL holds up at this load.
- **`.gitignore` and `.leonardignore` honored** — both files at the project root are loaded; matching paths are skipped during walk.
- **`vendor/`, `node_modules/`, `dist/`, `build/`, `.git/` always skipped** — even without `.gitignore`. Confirmed by indexing 500 vendored Go files; none surfaced via `verify`.
- **Symlinked project dir works** — `cd` into a symlink-to-real-dir; init/index/verify all work.
- **Unicode paths** — `unicode/тест/file.py` indexes cleanly, `qualified_name` includes the unicode dir.
- **Empty-store reads (`decisions list`, `decisions stale`, `claims unverified`)** — all return exit 0 with the friendly "no X recorded/found" message.
- **`init` with no `.leonard/`** — creates dir, opens DB, runs migrations, writes config.toml.
- **`init` idempotent for the schema** — re-running against an existing `.leonard/` doesn't blow up the DB (only the config — see F1).
- **`leonard help <cmd>`** — works for every subcommand including `mcp`.
- **Permission errors handled cleanly** — `chmod 000 .leonard/leonard.db` then verify produces an exit-2 error with the underlying SQLite message.
- **Verify with SQL-meta characters** — `%`, `_`, `'`, `;` in the name arg all safely return "no match"; parameterized query is used.
- **MCP locator falls back from neighbor-dir to PATH** — explicit test in `root_test.go:TestMCPCmd_ReportsMissingBinary` covers the missing case.
- **`leonard <unknown>` exits 2** at the root level (subcommand groups are inconsistent — see F10).

## Open questions

- **F2 (deletion handling) — what's the design intent?** The brief makes a strong "ground truth" promise. Was the orphan-row case left out intentionally (e.g., to be implemented later via the post-edit hook's delete signal), or is it just unimplemented? Doctor's "run leonard index" message suggests the latter — index was the intended remedy.
- **F5 (config dead code)** — should the CLI honor `[index].languages` / `[index].ignore` from config.toml? The post-edit hook reads `[verifiers]`; the session-start hook reads `[hooks]`. If the answer is "yes, eventually," F1 (init overwriting config) becomes higher severity.
- **F9 (walk-up)** — preferred semantics for nested `.leonard/` (e.g., a monorepo with multiple sub-projects each running `leonard init`)? Walking up would cross those boundaries.
- **F6 (doctor exit code)** — should there be a `--strict` mode, or should doctor stay informational-only with a separate `leonard check` (or similar) for CI gating?
- **F12 (JSON output)** — is the human-readable output considered a stable contract for grep-based CI, or is it understood to be churning? The current help text contracts `verify`'s exit codes, but the prose format isn't documented anywhere.
- **Was `decisions supersede` deliberately omitted from the CLI?** MCP has it; CLI has only the read sides plus `add`. The brief's coordination notes hint that the broader UX (interactive `decisions add`) is still in flux.

## Out of scope for this investigation

- The MCP `decisions supersede` tool itself (only its absence from the CLI is noted).
- Interactive `decisions add` design (the brief explicitly excluded interactive prompts).
- Bash/zsh completion (cobra auto-generated; not deep-probed).
- The web UI (DESIGN explicit non-goal).
- Hook payload contracts (covered by bughunt-1-hooks lane).
- Per-file scan performance on huge trees (perf lane, not us).
