# Bug Hunt #1 — Triage (v0.1 ship)

> **Decision:** fix all HIGH findings, defer MEDIUM and below to a later round.
> **Date:** 2026-05-19
> **Source:** `bughunt-1-{hooks,mcp,selfhost,typescript}.md`

## Why HIGH-only

The HIGH findings include three that **directly break Leonard's reason to exist**:

- `hooks F1` — pre-edit's block response is wrong-shaped → fabricated-symbol guard ships dead-on-arrival
- `hooks F2` — exit-1-on-decode-error means Claude Code treats hook errors as non-blocking → malformed payloads silently bypass the guard
- `typescript H1` — regex literals with `}` cause the parser to fabricate symbols, violating phase-1 brief's "no fabricated symbol claims" contract

Plus five more HIGH findings that are real correctness issues (panic on missing DB, silent data loss on DB swap, schema/store mismatch, two more TS parser holes). All of them undermine the trust story Leonard sells.

MEDIUMs are real bugs too, but every one of them is recoverable behavior — the user gets a wrong answer, not a broken safety contract. They can wait.

## The eight HIGH findings, grouped by fix lane

### Lane A — `fix-hooks` (3 findings)

All in `cmd/leonard-hook/*.go` + `internal/hooks/*.go`. Same package, related fixes — must land together or risk regressing each other.

| ID | One-liner | Fix shape |
|---|---|---|
| `hooks F1` | `pre-edit` block response uses PostToolUse-shaped fields (`decision: "block"` + `continue: false`) instead of PreToolUse's `hookSpecificOutput.permissionDecision: "deny"` | Rewrite the block-response builder to emit the PreToolUse-specific shape. Test with the documented envelope, not Leonard's invented one. |
| `hooks F2` | Every handler returns exit 1 on decode errors; Claude Code treats exit 1 as **non-blocking** | Change decode-error exits to **exit 2** (the documented "block the action" code). Audit every `os.Exit(1)` in the hook tree; reserve exit 1 for "Leonard problem, allow the action," exit 2 for "Leonard blocks the action." |
| `hooks F3` | `post-edit` panics with Go stack trace when `.leonard/leonard.db` missing | Replace the `mustOpenStore` panic path with a graceful no-op response + `Continue=true`. Match the existing pattern in `session-start` and `stop`. |

### Lane B — `fix-typescript` (3 findings)

All in `internal/parse/typescript.go`. Sequential within the lane (each fix changes the scanner state machine) but the lane itself is parallel-safe.

| ID | One-liner | Fix shape |
|---|---|---|
| `typescript H1` | Regex literals like `/\}/` aren't stripped, so the `}` inside them is counted as a body-closer → nested functions hoisted to top-level, outer body truncated | Treat regex literals as opaque tokens during stripping, same as strings and template literals. Add detection: `/` after operator/punctuator/keyword starts a regex; `/` after identifier/number/closing-bracket starts a division. |
| `typescript H2` | TS 4.4+ `static {}` blocks → silent symbol loss (parser bails out of the class) | The brace-counter needs to recognize `static {` as a class-internal block, not the end of the class. Track depth correctly through it. |
| `typescript H3` | Decorators on class members (`@log foo()`) cause every subsequent method to be dropped | Decorator skipping is currently mis-bounded. Either skip the whole decorator-call expression (including parens) as a token sequence, or accept decorators as legitimate member-prefix syntax. |

### Lane C — `fix-mcp` (2 findings)

Both in `internal/mcp/` + the `record_claim` validation in `internal/store/`. Two findings, related (both about contract-vs-implementation drift).

| ID | One-liner | Fix shape |
|---|---|---|
| `mcp F1` | `record_claim` schema declares `session_id` optional but the store rejects empty value | Either (a) update the schema to make `session_id` required, or (b) update the store to accept empty `session_id`. **Pick (b)** — the brief's lineage says `session_id` is opaque to Leonard; treat it like a tag, not a foreign key. |
| `mcp F2` | DB removed/replaced mid-session causes silent data loss (server keeps writing to ghost inode) | On every MCP tool call, verify the DB file at the configured path still exists and has the inode the server opened. If it's been swapped, return a structured `database-replaced` error and exit cleanly. Defense in depth: also wire a periodic stat-and-check goroutine. |

## What's deferred to a later round (MEDIUM)

Twelve MEDIUM findings, kept here verbatim so the next reviewer can see what we knowingly punted:

- `hooks F4` — `post-edit` writes vet result to `systemMessage` (Claude never sees the vet outcome). Visibility, not correctness.
- `hooks F5` — `post-edit` reports "re-indexed" for files that don't exist on disk.
- `hooks F6` — `pre-edit` silently allows `MultiEdit`, `NotebookEdit`, future write-shaped tools.
- `hooks F7` — `session-start` re-injects all decisions on every event including `compact`.
- `hooks F8` — `pre-edit` bypassable via Write payloads referencing unknown packages without imports.
- `hooks F9` — `pre-edit` fails open on empty stdin under agent stress (related to F2; F2's fix should cover this).
- `mcp F3` — `list_files.pattern` schema says `path.Match` but implementation is SQLite `GLOB` (different semantics).
- `mcp F4` — SIGINT clean shutdown exits with code 1.
- `mcp F5` — `find_symbol` lacks a `language` filter despite `verify_symbol` having one.
- `typescript` various MEDIUMs — mixin classes, multi-decorator-parens, getters/setters loss.
- `selfhost F1` — Python/TS qualified_name has no module prefix → cross-file shadowing.
- `selfhost F2` — Arrow-function `export const X = () => ...` indexed as `kind=const` not `function` (every React component file).

## What stays explicitly out of scope

- All LOW + INFORMATIONAL findings (no functional impact).
- Anything labeled "Out of scope for this investigation" in the findings docs.
- New features. Performance benchmarks. OSS-readiness polish.

If a fix lane discovers a new HIGH-severity issue while implementing, surface it in the PR description and decide on the spot whether to fold it in or punt to the next round. **Do not extend scope without surfacing the decision.**
