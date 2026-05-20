# Bug Hunt #2 — integration

## Summary

The cross-cutting investigation surfaced one high-impact bug (`integration F1` — the `[verifiers]` / `[hooks].block_on_fabricated_symbol` / `[index].languages` / `[index].ignore` / `LEONARD_PYTHON` knobs are *all* documented but *none* are actually consulted by the running code) and a handful of mediums centered on documentation drift between DESIGN.md, README.md, and the implementation. The good news: concurrency is solid (100 parallel post-edit hooks, MCP-during-storm, and indexer-during-storm all clean — zero `database is locked` errors, every claim persisted, store invariants intact), `go test -race ./...` passes clean, `CGO_ENABLED=0 go install ./cmd/...` produces three working binaries on a fresh GOBIN, and `go mod tidy` is a no-op. The end-to-end SessionStart → PreToolUse → PostToolUse → Stop flow works on synthetic Claude-Code-shaped payloads, supersede-on-fix works end-to-end (claims 1+2 → linked to claim 3 via `superseded_by_claim_id`), and `get_decisions` returns newest-first deterministically (`ORDER BY recorded_at DESC, id DESC`).

Headline gap: roughly **half of the documented configuration surface (and the documented `LEONARD_PYTHON` override) is dead code** — readers parse it, nothing in the indexer or hook path branches on the values. The DESIGN doc still describes a v0.1 indexer that doesn't match the v0.2 Python lane, and the README contradicts itself on Python parsing within four sections. None of these break the safety story the way the Round-1 HIGHs did, but they actively mislead — a fresh contributor following README+DESIGN will reach for knobs that look real and don't do anything.

## Findings

### F1 — `[verifiers]`, `[hooks].block_on_fabricated_symbol`, `[index].languages`, `[index].ignore`, and `LEONARD_PYTHON` are all dead configuration
- **Severity:** high
- **Reproducer:** see five tiny scenarios in `/tmp/seam-test`:
  1. `[verifiers]` override
     ```bash
     cat > /tmp/seam-test/.leonard/config.toml <<'EOF'
     [verifiers]
     go = ['/usr/bin/true']  # claim "always passes"
     EOF
     cat > /tmp/seam-test/bad.go <<'EOF'
     package seam
     import "fmt"
     func Bad() { fmt.Sprintf("%d", "not-int") }
     EOF
     printf '{"session_id":"sess-2","tool_name":"Edit","cwd":"/tmp/seam-test","hook_event_name":"PostToolUse","tool_input":{"file_path":"/tmp/seam-test/bad.go"}}' | leonard-hook post-edit
     ```
     Result: vet=failed in the claim row anyway — the configured `/usr/bin/true` is never run. `internal/hooks/post_edit.go:140` hard-codes `opts.Vet = RunGoVet` and `RunGoVet` itself shells out to `go vet ./...` unconditionally (`post_edit.go:415-423`). The verifiers config has no consumer in `internal/hooks/` or `cmd/leonard-hook/`.

  2. `[hooks].block_on_fabricated_symbol = false`
     ```bash
     # Set the knob to false then send a snippet referencing a fabricated symbol
     # — guard should disengage per the documented contract.
     ```
     Result: the pre-edit guard still emits `permissionDecision: "deny"`. `grep -rn BlockOnFabricatedSymbol cmd/ internal/` returns only the struct definition and `Default()` value — no read site.

  3. `[index].languages = ['go']`
     ```bash
     # config.toml says only Go is indexed; drop a .py file in the project
     cat > /tmp/seam-test/keepme.py <<'EOF'
     def ShouldBeSkipped(): pass
     EOF
     leonard index
     sqlite3 /tmp/seam-test/.leonard/leonard.db "SELECT path, language FROM files"
     ```
     Result: Python file is indexed anyway (`keepme.py|python` appears in the output). The dispatch in `internal/index/indexer.go:42-50` keys off file extension, never on `cfg.Index.Languages`.

  4. `[index].ignore = ['srcs/']`
     ```bash
     mkdir /tmp/seam-test/srcs && echo 'package srcs; func Ignored() int { return 1 }' > /tmp/seam-test/srcs/x.go
     leonard index
     ```
     Result: `srcs/x.go` is indexed despite the ignore pattern. `loadIgnore` (`indexer.go:276-294`) reads `.gitignore` and `.leonardignore` only, not the TOML.

  5. `LEONARD_PYTHON=<override>`:
     ```bash
     grep -rn 'LEONARD_PYTHON\|os.Getenv' /Users/jasondillingham/Documents/Homelab/leonard/internal/parse/
     # Only hits: the error-message string in python.go and the doc comment.
     # Zero os.Getenv calls.
     ```
     The `pythonInterpreter` package var (`python.go:30`) is initialized once to the string literal `"python3"` and never reassigned. README line 32 says "override via `LEONARD_PYTHON`" — that override is a lie.

- **Observed:** five documented configuration knobs / env vars have no read site. Setting them has zero behavioral effect.
- **Expected:** either the code honors them (README/DESIGN imply this) or the docs stop advertising them. DESIGN.md §4.6 (`[verifiers]`, `[hooks].block_on_fabricated_symbol`) presents these as the configuration surface; README's Python row promises `LEONARD_PYTHON` as the override. A user setting `block_on_fabricated_symbol = false` to unblock a stubborn pre-edit guard will assume Leonard is broken when the guard keeps firing.
- **Suggested fix shape:** wire one knob in, gut the rest. The `[verifiers]` block is the highest-value one to actually implement (it would close a real soft spot — Python and TypeScript projects today get *no* verifier run; only Go projects with a `go.mod` get `go vet`). `LEONARD_PYTHON` is a one-line `os.Getenv` add. `[hooks].block_on_fabricated_symbol` and `[index].languages`/`[index].ignore` should either grow consumers or be deleted from `internal/config` + `Default()` so the file `leonard init` writes doesn't mislead.
- **Out of scope for this investigation:** designing the wiring; lane was integration-only. Note that `inject_decisions_at_session_start` and `surface_unverified_claims_at_stop` ARE consulted (`cmd/leonard-hook/session_start.go:62` and `cmd/leonard-hook/stop.go:61`) — those two are real.

### F2 — Documentation drift between DESIGN.md, README.md, and code
- **Severity:** medium
- **Reproducer:** seven drifts found, all reproducible by reading the three sources side-by-side:

  1. **`leonard reindex <path>` advertised, not implemented.** DESIGN.md §4.5 lists `leonard reindex <path>` as the per-file re-index command. `leonard reindex` errors "unknown command" — only `leonard index` exists, and it has no `<path>` arg. (Cobra's "Did you mean this? index" suggestion fires.) `leonard index` always does a full walk; there's no single-file public surface in the CLI.

  2. **`get_stale_decisions` missing from DESIGN.** README line 22 lists 10 MCP tools; the live `tools/list` confirms 10. DESIGN.md §4.3 lists 9 — `get_stale_decisions` is absent from the contract table.

  3. **README contradicts itself on the Python parser.** Line 32: "host `python3` via subprocess … swapped gpython for an exec of the host's `python3`." Line 70 (project layout): "internal/parse # Go (stdlib), Python (gpython), TypeScript (hand-rolled)". gpython is no longer a dependency (`grep -i gpython go.mod` → nothing). DESIGN.md §7 Q2 still describes gpython as the resolution, never updated.

  4. **`.claude/settings.local.json` matcher mismatch.** README §"Dogfood wiring" shows PreToolUse matcher `Edit|Write|MultiEdit|NotebookEdit` and PostToolUse matcher `Edit|Write|MultiEdit`. The repo's actual `.claude/settings.local.json` uses `Edit|Write` for *both* (and for both hook directions). The pre-edit and post-edit handlers in the binary do support MultiEdit/NotebookEdit (`pre_edit.go:240-257` and the post-edit indexer takes any file path), but the matcher in the dogfood config never fires for those tools — meaning Leonard *as deployed against itself* doesn't enforce the fabrication guard when Claude uses MultiEdit. This was flagged for the bughunt-1 docs (F6 was "extend pre-edit guard to MultiEdit/NotebookEdit" → the handler was updated) but the deployed config never followed.

  5. **`leonard claims unverified` description doesn't mention the file-scope ambiguity.** Running it from any subdir of a leonard-init'd project happily resolves to that project's DB — but if you cd outside and run it, the help text just says "List unverified claims" without explaining that it walks up looking for `.leonard/`. Not a bug, just a sharp edge.

  6. **DESIGN.md §6 "Phase 1 / 2 / 3" checkboxes still all `[ ]`.** Per README line 7 ("v0.1, self-dogfooded as of 2026-05-19"), every phase 1 and phase 2 bullet is done and most of phase 3. The checkboxes in DESIGN.md were never ticked. Not a bug but it makes DESIGN feel pre-implementation when it's not.

  7. **DESIGN.md §3 architecture diagram says "leonard reindex"; README never mentions reindex.** Consistent with finding 1.

- **Observed:** new contributors face three sources of truth (DESIGN > README > code) that disagree on tool count, parser implementation, CLI surface, and config knobs.
- **Expected:** DESIGN refreshed after the v0.1 ship; README's project-layout sub-block updated alongside its language table.
- **Suggested fix shape:** dedicate a single doc-sync pass. README is closest to truth — promote it to canonical, defer DESIGN to the architectural-overview role (and update the bits that are out of date).
- **Out of scope for this investigation:** prose polishing.

### F3 — `wire_real.go` (the production runtime for the `leonard` CLI) has 0% test coverage
- **Severity:** medium
- **Reproducer:** `go test -coverprofile=/tmp/cover.out ./... && go tool cover -func=/tmp/cover.out | awk '$3 == "0.0%"'` lists 31 zero-coverage functions; **every public method of `realRuntime`** is among them (`Init`, `IndexAll`, `VerifySymbol`, `RecordDecision`, `GetDecisions`, `GetStaleDecisions`, `Doctor`, `GetUnverifiedClaims` plus the helper `newDefaultRuntime`). Tests use `fakeRuntime` (`cmd/leonard/root_test.go:14`) — the real path that exercises SQLite end-to-end is never compiled-and-driven by Go tests. The `cmd/leonard-hook/backend_real.go` shows the same shape (Indexer/Claims/Close/mustOpenStore/RecordClaim/SupersedeClaimsForFile all 0%) — the hook binary's real backend is uncovered too.
- **Observed:** `go test ./...` is green even if every method on `realRuntime` is broken (so long as the interface satisfies and the file builds). The smoke catches accidentally-wrong shapes but not behavior.
- **Expected:** at least one black-box test that builds the `leonard` and `leonard-hook` binaries, invokes them as subprocesses against a real SQLite DB, and asserts the output. No test in the repo uses `exec.Command` against a Leonard binary (verified by `grep -rln 'exec.Command\|os/exec\|CommandTransport' cmd/ internal/ | grep _test.go` → no hits).
- **Suggested fix shape:** a single `cmd/leonard/integration_test.go` that does `go build`, then drives the binary across `init → index → verify → decisions add → decisions list → doctor` with `testing/iotest` + `os/exec`. Same idea for `leonard-hook` (covered by F4 below). The MCP lane bug hunt #1 noted the stdio transport was also untested — the in-process MCP fixtures (`NewInMemoryTransports`) are the only path tests exercise.
- **Out of scope for this investigation:** rewriting the test harness.

### F4 — No tests exercise the binaries as subprocesses; no tests exercise stdio MCP; no tests cover migration from prior schema versions
- **Severity:** medium
- **Reproducer:**
  - `grep -rln 'exec.Command' cmd/ internal/ | grep _test.go` → 0 hits.
  - `grep -rln 'CommandTransport\|StdioTransport' cmd/ internal/ | grep _test.go` → 0 hits.
  - `grep -n 'migrateV[0-9]' internal/store/store_test.go` returns only references inside fresh-DB tests — no test creates a v1 DB, opens it, asserts the migration runs to v4 with prior rows preserved.
- **Observed:** the four test categories that would catch the most painful release-time regressions are absent. The fact that bughunt-1 had to write `/tmp/` harnesses to find F4/F5/F6 in the hooks lane is the symptom: there's no in-tree way to drive the binary surfaces.
- **Expected:** at least token coverage for each category. A test that ships an embedded v1 DB blob and opens it through `store.Open` would catch a migration regression in seconds; today such a regression ships and is only caught when a user with an older `.leonard/leonard.db` upgrades.
- **Suggested fix shape:** three small additions:
  1. `internal/store/migration_test.go` — checks in a tiny v1 DB blob under `testdata/`, asserts `Open()` migrates it cleanly through v4 and prior data survives.
  2. `cmd/leonard-mcp/stdio_test.go` — boots the binary with `mcp.NewCommandTransport`, calls every tool once. The bughunt-1-mcp lane's `/tmp/` harness can be lifted in directly.
  3. `cmd/leonard/integration_test.go` — see F3.
- **Out of scope for this investigation:** implementing them.

### F5 — Two simultaneous `leonard init` invocations: one wins, the other prints a scary SQLITE_BUSY trace
- **Severity:** low
- **Reproducer:**
  ```bash
  rm -rf /tmp/init-race && mkdir -p /tmp/init-race
  (leonard init /tmp/init-race 2>&1; echo "exit_a=$?") > /tmp/init-a.log &
  (leonard init /tmp/init-race 2>&1; echo "exit_b=$?") > /tmp/init-b.log &
  wait
  ```
  Output:
  ```
  --- A ---
  leonard: initialized /tmp/init-race/.leonard
  exit_a=0
  --- B ---
  leonard: init /tmp/init-race: store: ping sqlite: database is locked (5) (SQLITE_BUSY)
  exit_b=2
  ```
- **Observed:** the loser of the init race exits 2 with a SQLite-shaped error. The DB ends up correctly initialized (A's work persisted, B's was a no-op anyway since init is idempotent).
- **Expected:** init is documented as idempotent ("Safe to re-run — the underlying schema migration is idempotent"). A concurrent caller should see the same idempotent semantics: B should see "already initialized" or just exit 0.
- **Suggested fix shape:** in `realRuntime.Init`, if `store.Open` fails with `SQLITE_BUSY` during the *ping*, retry once after a short backoff. Or, simpler: check whether `leonard.db` exists *before* opening and short-circuit to "already initialized." Either approach matches the documented "safe to re-run" contract better than the current "two concurrent users get one scary log line."
- **Out of scope for this investigation:** all other concurrency probes (10, 50, 100 parallel post-edit hooks; MCP-during-storm; index-during-storm) were clean. See the "Things that worked" section.

### F6 — Indexer's testdata-walking divergence from pre-edit's sibling-package walker
- **Severity:** low
- **Reproducer:** `sqlite3 ~/Documents/Homelab/leonard/.leonard/leonard.db "SELECT path FROM files WHERE path LIKE '%testdata%'"` returns six entries including `testdata/python/methods.py` and `internal/mcp/testdata/sample/main.go`. But the pre-edit sibling-package walker (`internal/hooks/pre_edit.go:362`) explicitly skips `testdata` directories: `if path != moduleRoot && (... || name == "testdata" || ...)`. The indexer (`internal/index/indexer.go:27-33`) does NOT skip testdata.
- **Observed:** symbols defined in `testdata/` are present in the symbol index, but pre-edit's fabrication guard's sibling-import resolution will never resolve a `testdata`-only package. Worse: an Edit that *imports* testdata code (which the indexer says exists) and references its symbols would pass the fabrication check (HasSymbol returns true), while a sibling resolution attempt would silently miss. The two sets disagree on what counts as "in the project."
- **Expected:** one consistent definition of "in scope." Either both walk testdata or neither.
- **Suggested fix shape:** add "testdata" to `defaultSkipDirs` in the indexer. The `internal/mcp/testdata/sample/main.go` and the `testdata/python/`+`testdata/typescript/` fixtures shouldn't appear in `verify_symbol` lookups — they're fixtures, not project code. Be careful: the self-host test (`internal/index/selfhost_test.go`) may depend on these being indexed; check before changing.
- **Out of scope for this investigation:** the broader question of whether the indexer should honor `[index].ignore` from config.toml — covered in F1.

### F7 — Stop hook can't actually surface unverified claims to the model
- **Severity:** low (architecturally the right call; surfaced because it's mentioned in the brief)
- **Reproducer:** end-to-end seam test with a vet-failing edit during session "sim-2":
  ```bash
  # bad.go fails vet; post-edit records an unverified claim
  printf '{"session_id":"sim-2",...PostToolUse..."tool_input":{"file_path":".../bad.go"}}' | leonard-hook post-edit
  # Stop hook fires; reads unverified claims for sim-2
  printf '{"session_id":"sim-2","hook_event_name":"Stop","cwd":"...","stop_hook_active":false}' | leonard-hook stop
  ```
  Stop emits:
  ```json
  {"continue":true,"systemMessage":"## Unverified claims (from Leonard)\n\n- tool=Edit file=/tmp/seam-test/bad.go; index=ok; go vet=failed"}
  ```
  Per the official Claude Code hook doc (https://code.claude.com/docs/en/hooks): `systemMessage` is **user-visible only**, not visible to Claude. So Claude finishes the turn without ever seeing the unverified-claim block.
- **Observed:** the Stop hook surfaces the claim to the human via `systemMessage` but the model is unaware. Per DESIGN.md §4.4: "If session has unverified claims, surface them; optionally block stop until acknowledged."
- **Expected:** either (a) Stop emits `decision: "block"` + `reason: "..."` (the docs say this is what Stop accepts), forcing Claude to keep working / acknowledge, or (b) the surfacing mechanism stays advisory and the docs update to reflect that.
- **Suggested fix shape:** Stop should optionally promote to `{"decision":"block","reason":"<unverified claims>"}` when `surface_unverified_claims_at_stop > 0`. Today the config knob exists but only changes how many claims appear in the user-visible message, not whether the model sees them.
- **Note:** this was triaged in bughunt-1 (`hooks F4`) and deferred. Mentioning it here because the integration lane's brief asked specifically about "does each hook handle the others' outputs — e.g. the additionalContext from SessionStart actually reaches Claude in a real session — or do we just trust the contract?" SessionStart's additionalContext does reach the model; PostToolUse's does too; Stop's surface to the model does not exist by design (until the F4 fix lands).
- **Out of scope for this investigation:** the deferred F4 fix itself.

### F8 — `transcript_path` field present in every hook envelope is never read
- **Severity:** informational
- **Reproducer:** `grep -rn 'transcript_path\|TranscriptPath' cmd/ internal/` → 0 hits. The Claude Code docs (fetched at https://code.claude.com/docs/en/hooks during this investigation) call out `transcript_path` as one of the "always present" common input fields across all hook events.
- **Observed:** Leonard's payload structs (`PostToolUsePayload`, `PreToolUsePayload`, `SessionStartPayload`, `StopPayload`) define `SessionID`, `HookEventName`, `CWD`, and a couple event-specific fields — never `TranscriptPath`.
- **Expected:** harmless — `encoding/json` silently drops unknown fields by default. But it's a missed handle for richer claims/decisions (e.g., a claim could cite a specific transcript-relative line range).
- **Suggested fix shape:** none for v1; note for v2 if Leonard ever wants to point at the conversation context that produced a claim.

### F9 — `Makefile` references a build tag and lane structure that no longer exist
- **Severity:** informational
- **Reproducer:**
  ```bash
  grep -n 'leonardreal\|store and parser lanes\|build-real' Makefile
  grep -rn 'leonardreal\|//go:build leonardreal' cmd/ internal/
  ```
  The Makefile (`build-real: go build -tags leonardreal ./cmd/leonard ./cmd/leonard-hook`) presumes the existence of a `leonardreal` build tag. No source file in the tree has that tag. The Makefile comment refers to "the store and parser branches merge to main" — they did, months of phases ago.
- **Observed:** `make build-real` builds successfully but silently does nothing different from `make build`. The tag is inert.
- **Expected:** delete `build-real`, drop the comment, simplify the file.
- **Suggested fix shape:** trim Makefile to `check / test / vet / build / tidy / clean`. Update `test` and `vet` to cover the full module (the in-file comment about lane-local testing is also stale).

## Things that worked

- **Concurrent post-edit hook storm.** 10 / 50 / 100 parallel `leonard-hook post-edit` invocations against a real `.leonard/leonard.db` with three rotating `session_id` values: 0 lock errors, 100/100 claim rows persisted, 100/100 files indexed, all expected `Symbol<i>` rows present. Multiple verifying queries (`SELECT count(*) FROM claims; FROM files; FROM symbols`) all return the expected counts. (Scratch script: `/tmp/concurrent-probe/probe.sh`.) SQLite WAL + `busy_timeout=5000` per `internal/store/store.go:138` handles the storm cleanly.
- **MCP during storm.** 50 concurrent `verify_symbol` calls via the SDK's `CommandTransport` (real stdio): 50/0 ok/fail. Run simultaneously with the post-edit hook storm: still 50/0 ok/fail, post-edit still 50/50 clean.
- **Indexer during post-edit storm.** `leonard index` reindexing 100 generated files while 30 concurrent post-edit hooks fire on the same files: 0 lock errors, all 30 claim rows persisted, file/symbol counts correct.
- **`go test -race ./...`** passes on every package, including `internal/store` which has the actual SQLite contention story.
- **`CGO_ENABLED=0 go install ./cmd/...`** produces three working binaries (`leonard`, `leonard-hook`, `leonard-mcp`) on a fresh `GOBIN`. All three bind to PATH and run. Binary sizes: 11–14 MB (mostly modernc.org/sqlite). `otool -L` shows only libSystem + libresolv + (on leonard-mcp) CoreFoundation/Security — the latter from `golang.org/x/oauth2/internal`, not from CGo. `go list -deps -json ./cmd/... | jq` finds zero packages with `CgoFiles`/`CFiles`/`CXXFiles`. Cross-platform ready.
- **`go mod tidy`** is clean — no changes to `go.mod` or `go.sum`. No unused deps. (`go list -m all` returns 49 modules — appropriate for the dep graph.)
- **Supersede-on-fix works end-to-end.** Edit a Go file → vet fails (claim 1 written, verified=0). Edit again, still failing (claim 2, verified=0). Fix the bug, edit a third time → claim 3 written (verified=1), AND claims 1 and 2's `superseded_by_claim_id` column is updated to 3. `SupersedeClaimsForFile` (`store.go:799`) runs in the post-edit happy path.
- **Decision lifecycle: newest-first ordering is deterministic.** `store.GetDecisions` uses `ORDER BY recorded_at DESC, id DESC LIMIT ?` (`store.go:570`) — ties broken by id descending, so three decisions added in the same second still come back newest-first. Verified by adding three decisions to one topic and observing the CLI output reverses them correctly.
- **MCP `tools/list` matches README's advertised surface.** Live `tools/list` against `leonard-mcp` returns exactly the 10 tools README §"Components" lists: `verify_symbol`, `find_symbol`, `list_files`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `recent_changes`. README is the source of truth here; DESIGN.md drift is documented in F2.
- **Synthetic Claude Code session flow.** Manually piping JSON payloads in SessionStart → PreToolUse → PostToolUse → Stop order into `leonard-hook` against a real `.leonard/leonard.db` works end-to-end. The Round-1-fixed pre-edit response shape (`hookSpecificOutput.permissionDecision: "deny"`) is correct per the live Claude Code hook docs (verified by fetching https://code.claude.com/docs/en/hooks). The Round-1-fixed `exit 2` semantics on decode errors are correct (verified by `printf '' | leonard-hook pre-edit; echo $?` → 2).
- **Large `new_string` payload (1.1 MB).** pre-edit handles it without panic, runs the `go/parser` cleanly, returns `{"continue":true}` in milliseconds.
- **Unknown fields tolerated.** A payload containing `permission_mode`, `transcript_path`, and a junk `unknown_field` decodes cleanly — encoding/json drops them, the handler proceeds.
- **Empty stdin: blocks with exit 2.** Verified: `printf '' | leonard-hook pre-edit` exits 2 (correct — per Round-1 `hooks F2` fix). Same for the other three subcommands.
- **`leonard mcp` not-found path.** With `leonard-mcp` absent from the directory beside `leonard` and from PATH, `leonard mcp` exits 2 with the message "leonard-mcp not found beside leonard or on PATH" — correctly surfaces the failure rather than silently doing nothing.

## Open questions

- **Should the dogfood `.claude/settings.local.json` adopt the README's `Edit|Write|MultiEdit|NotebookEdit` matcher?** The handlers support these; the deployed config doesn't fire the guard for them. Easy one-line fix to `.claude/settings.local.json` once Jason confirms (settings.local.json is gitignored — personal config, so the answer is "Jason changes his file and possibly updates the README example").
- **Does Leonard care about the `transcript_path` field at all?** It's "always present" per the docs but is unused. Could enable richer claim provenance ("this claim was made at line N of transcript X"). Probably v2.
- **What's the deletion vs implementation policy for the dead config in F1?** Five knobs, three (`[verifiers]`, `[index].languages`, `[index].ignore`) would take real work to implement (each one has cascading questions — e.g. does `languages = ['go']` mean don't dispatch the Python extractor, or also remove existing Python symbols from the index on next walk?). Two (`block_on_fabricated_symbol`, `LEONARD_PYTHON`) are one-line wires. Bosun decides scope of the cleanup.
- **Should `init` race-handling fall under F1 cleanup (it's a docs-vs-code thing) or its own change?** Likely the latter — init idempotency is a real contract worth shoring up regardless of the other config drift.
- **Schema-migration robustness.** No test exists that opens a real prior-version DB. If a future migration drops data, the regression ships silently until a user with an upgraded `.leonard/leonard.db` notices. Worth investing one test fixture before the schema reaches v5.
