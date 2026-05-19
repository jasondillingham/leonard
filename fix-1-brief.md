# Leonard — Fix Round #1 Brief

> **Audience:** parallel Claude Code sessions launched by bosun.
> **Triage source:** `bughunt-1-triage.md` — read it first.
> **Scope:** the eight HIGH findings only. Mediums are explicitly deferred.

## What landed before this round

`bughunt-1-{hooks,mcp,selfhost,typescript}.md` — the four investigation reports — are on `main`. So is `bughunt-1-triage.md` capturing the decision to fix HIGH-only.

## Acceptance criteria (whole project)

A fix-round-1 build is done when:

1. All eight HIGH findings have a fix on `main` with a test that demonstrates the fix (specifically: a test that fails on `main` before the fix and passes after).
2. The reproducer in each finding's `bughunt-1-*.md` entry, when run against the post-fix code, no longer reproduces the issue.
3. **No regressions to phase 1/2/3.** Every existing test still passes. Every existing MCP tool, hook subcommand, and CLI command keeps its current contract.
4. `go test -race ./... -count=1` clean.
5. No MEDIUM findings touched this round. If a fix accidentally lands a MEDIUM as a side effect, note it in the commit body — don't expand scope deliberately.

## Coordination rules

- **Stay in your lane.** Files-owned lists below define the boundary. The three lanes touch different files; this round should produce **zero conflicts**.
- **Each finding gets its own test.** The bughunt finding gives you the reproducer — translate it into a Go test that fails-then-passes.
- **Update the finding's status in the source bughunt doc** as a final commit in your lane: add `- **Status (2026-05-19):** fixed in <commit-sha-or-lane>.` under the finding. That keeps the audit trail tight.
- **Race-clean is non-negotiable.** `go test -race ./... -count=1` before `bosun done`.

---

## fix-hooks

Three findings, all in the hook layer. Fix together — F1 and F2 are interrelated (both about how Claude Code interprets a hook's exit-and-output pair).

**Files you own (exclusively):**
- `cmd/leonard-hook/post_edit.go` + `cmd/leonard-hook/pre_edit.go` + `cmd/leonard-hook/session_start.go` + `cmd/leonard-hook/stop.go`
- `cmd/leonard-hook/post_edit_test.go` + `cmd/leonard-hook/pre_edit_test.go` + `cmd/leonard-hook/session_start_test.go` + `cmd/leonard-hook/stop_test.go`
- `cmd/leonard-hook/backend_real.go` + `cmd/leonard-hook/root.go` (small touches if needed)
- `internal/hooks/*.go` (everything in the package)

**Findings to fix:**

### F1 — pre-edit block response shape

The current handler emits a PostToolUse-shaped block response. The PreToolUse contract requires `hookSpecificOutput.permissionDecision: "deny"` with `permissionDecisionReason: <message>`, and **must NOT** include `continue: false` (which halts the entire agent).

Fix shape: separate the PreToolUse response builder from the PostToolUse one. Don't share a struct between them. The PreToolUse response is its own thing; treat it that way in the code.

**Test:** craft a synthetic `PreToolUse` payload that contains a fabricated symbol reference; pipe it into `leonard-hook pre-edit`; assert the response JSON contains `hookSpecificOutput.permissionDecision="deny"` and **does not** contain `continue: false`. The current test passes because it asserts the wrong shape — fix the test alongside the handler.

### F2 — exit codes for decode errors

Today: every handler returns `os.Exit(1)` on JSON decode failure, empty stdin, etc. Claude Code's exit-code contract: **exit 0 = allow, exit 1 = non-blocking error (allow), exit 2 = block the action.** Decode errors that block fabrication should return exit 2.

Audit every `os.Exit(N)` call across the hook tree. For each, decide which class the failure is in:
- "Leonard problem, but the action is fine" → exit 1
- "Leonard can't make a decision, the action should be blocked" → exit 2 (use this for decode failures on the pre-edit / post-edit guard hooks)
- "All good" → exit 0

**Test:** pipe garbage to `leonard-hook pre-edit`; assert exit code 2. Pipe garbage to `leonard-hook post-edit`; assert exit code 2. Don't pipe garbage to `session-start` / `stop` — those are advisory, exit 1 is correct.

### F3 — post-edit panic on missing DB

`backend_real.go`'s `mustOpenStore` is a sledgehammer. The post-edit hook should degrade gracefully when `.leonard/leonard.db` is missing (e.g., fresh checkout that hasn't run `leonard init`). The session-start and stop hooks already do this — match their pattern.

Fix shape: replace the panic with a no-op response (Continue=true, exit 0) plus an operator-facing diagnostic on stderr ("leonard: post-edit skipped — run `leonard init` first").

**Test:** invoke `leonard-hook post-edit` against a tmp dir with no `.leonard/`; assert exit 0 + Continue=true in the response.

**Acceptance for this lane:**
- All three reproducers from `bughunt-1-hooks.md` F1/F2/F3 fail before the fix and pass after.
- Every existing hook test still passes.
- `go test -race ./cmd/leonard-hook/... ./internal/hooks/... -count=1` clean.
- `bughunt-1-hooks.md` updated with status lines for F1, F2, F3.

## fix-typescript

Three findings, all in `internal/parse/typescript.go`. Single file; sequential fixes within the lane.

**Files you own (exclusively):**
- `internal/parse/typescript.go`
- `internal/parse/typescript_test.go`

**Findings to fix:**

### H1 — regex literals not stripped

The scanner's string/comment/template-literal stripping doesn't cover regex literals. So `const re = /\}/` leaves a `}` in the stripped stream, which the brace counter interprets as a body-closer.

Fix shape: extend the stripper to recognize regex literals. The classic disambiguation: `/` after an operator, keyword, punctuator, or start-of-expression context starts a regex; `/` after an identifier, numeric literal, or closing bracket is division. Implement this as a small "previous-token-class" tracker during scanning.

Once detected, replace the regex body (including escapes for `\/` and `\]`) with spaces, same treatment as strings.

**Test:** the reproducer in H1's entry — `export function outer() { const re = /\}/; function fakeNested() { ... } return re; }`. After fix: only `outer` should be extracted as a top-level function; `fakeNested` is properly nested and excluded.

### H2 — `static {}` blocks lose subsequent class members

The brace counter doesn't recognize `static { ... }` initializer blocks (TS 4.4+) as class-internal. When it hits the `{` of a static block, it treats the matching `}` as closing the class.

Fix shape: in the class-body scanning state, recognize `static {` as opening a class-internal block. The matching `}` is balanced internally; the class continues after it. Same treatment for the `{...}` of static initializer expressions.

**Test:** fixture with `class Foo { static count = 0; static { Foo.count = 1; } static incr() { ... } method() { ... } }` — assert all four members (`count`, the static block, `incr`, `method`) are correctly handled and `incr` + `method` appear as methods.

### H3 — decorators eat subsequent class methods

`@log foo() { ... }` causes the decorator-handling code path to swallow more than the decorator. Specifically, every method **after** the decorated one disappears from the index.

Fix shape: bound the decorator skipping correctly. The decorator is: `@<expression>` optionally followed by `(args)`. After the decorator's tokens are consumed, scanning continues normally at the next class member. The current implementation either runs off the end or eats the next member's tokens.

**Test:** fixture with three decorated methods in a class — `class Foo { @log a() {} @log b() {} @log c() {} }`. After fix: all three methods extracted with `kind=method` and correct names.

**Acceptance for this lane:**
- All three reproducers from `bughunt-1-typescript.md` H1/H2/H3 fail before the fix and pass after.
- Every existing TS test still passes.
- `go test -race ./internal/parse/... ./internal/index/... -count=1` clean.
- `bughunt-1-typescript.md` updated with status lines for H1, H2, H3.

## fix-mcp

Two findings, both in `internal/mcp/` + a small touch to `internal/store/` for F1's resolution.

**Files you own (exclusively):**
- `internal/mcp/claims.go` + `internal/mcp/claims_test.go`
- `internal/mcp/adapter.go` (for the `RecordClaim` adapter signature/validation, if applicable)
- `internal/store/store.go` — **only** the `RecordClaim` validation; nothing else. Coordinate via `bosun claim` if you need to touch more.
- `cmd/leonard-mcp/main.go` (for the inode-check goroutine wiring in F2)
- `internal/mcp/server.go` — **only** if F2's fix needs a startup-time hook for the file-watcher; otherwise don't touch

**Findings to fix:**

### F1 — record_claim session_id optional-vs-required mismatch

The MCP schema declares `session_id` optional (no `required` entry). The store's `RecordClaim` rejects empty `session_id` with `"empty session id"`. Either the schema is wrong or the store is wrong.

The triage decision: **the store is wrong**. `session_id` is opaque to Leonard; it should be treated as a tag, not a foreign key. Empty is a valid value (meaning "not associated with any session yet").

Fix shape: remove the empty-session-id rejection in `store.Store.RecordClaim`. Update the relevant store test to confirm empty is accepted. Update the schema description on `record_claim.session_id` to note that empty means "unscoped."

**Test:** call `record_claim` over MCP with `session_id` omitted entirely; expect a successful `{claim_id: <int>}` response. Then call `get_unverified_claims` with no `session_id` filter; expect the new row to appear in the results.

### F2 — DB removed/replaced mid-session causes silent data loss

When `.leonard/leonard.db` is `rm`'d or replaced (e.g., the user runs `leonard init` from another shell), the server keeps reading and writing to the now-unlinked inode. The user sees normal responses, but writes vanish when the server exits.

Fix shape: two complementary mechanisms:
1. **Lazy detection on every tool call:** before each handler runs, `os.Stat` the configured DB path; if it doesn't exist OR its inode differs from the open file's inode (`fi.Sys().(*syscall.Stat_t).Ino`), return a structured error: `{"code":"database-replaced","message":"the project store has been replaced; restart leonard-mcp"}`. Don't try to auto-recover — restarting is the correct response.
2. **Active watcher:** a goroutine launched from `main.run()` that polls (or uses `fsnotify` if it's already a transitive dep — don't add it as a new direct dep just for this) and triggers a clean server shutdown if the swap is detected. Logs to stderr.

**Test:** spin up the server against a temp DB. Make a tool call; succeed. `os.Remove` the DB. Make another tool call; expect the structured `database-replaced` error. Restart the server; expect it to come up cleanly.

**Acceptance for this lane:**
- Both reproducers from `bughunt-1-mcp.md` F1/F2 fail before the fix and pass after.
- Every existing MCP test still passes.
- `go test -race ./internal/mcp/... ./internal/store/... -count=1` clean.
- `bughunt-1-mcp.md` updated with status lines for F1, F2.

---

## What is NOT in this round

- **Any MEDIUM finding.** They're all listed in `bughunt-1-triage.md` for the next round.
- **Refactors.** Fix the bug; resist tidying the surrounding code.
- **DESIGN.md changes** unless the fix invalidates a documented behavior (rare).
- **Performance work.** Out of scope, period.

If a fix lane discovers a new HIGH-severity issue while implementing, surface it in the PR description. Decide on the spot whether to fold it in or punt to round 2. **Do not extend scope without surfacing the decision.**
