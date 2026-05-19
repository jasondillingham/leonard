# Bug Hunt #1 — hooks

## Summary

I audited the four `leonard-hook` subcommands (`post-edit`, `pre-edit`,
`session-start`, `stop`) against the current Claude Code hook contract
documented at https://code.claude.com/docs/en/hooks. The envelope-decode
side of the contract is mostly fine (Go's default JSON tolerance covers
the docs-vs-code drift), but the **response side and the exit-code
discipline are wrong in ways that break the hooks' enforcement story.**

The biggest single problem: `pre-edit`'s block response uses
PostToolUse-shaped fields (`decision: "block"` + `continue: false`)
where the documented PreToolUse contract requires
`hookSpecificOutput.permissionDecision: "deny"`. The current shape
either halts the entire Claude Code agent (because of `continue: false`)
or — once Claude Code drops legacy compatibility for the top-level
`decision` field — silently allows the edit, which would mean the
fabricated-symbol guard ships dead-on-arrival. Either outcome breaks
DESIGN.md §1's whole reason for `pre-edit` to exist.

A close second: every handler returns **exit 1** (Go's idiomatic
"something went wrong") on decode errors, but Claude Code treats exit 1
as **non-blocking**. Per the docs: *"only exit code 2 blocks the
action. Claude Code treats exit code 1 as a non-blocking error and
proceeds with the action."* So a malformed PreToolUse payload, an
empty stdin, or any of the well-formedness checks all currently
**fail-open**: Claude proceeds with the Edit/Write while the user sees
a hook-error notice. The fabrication guard has a free bypass via "send
a JSON payload Leonard can't parse."

A third, narrower one: `post-edit` panics with a Go stack trace
(exiting 2) when `.leonard/leonard.db` doesn't exist — i.e., before
the user has run `leonard init`. The intent ("intentionally panicky"
per `backend_real.go:30`) was a clean exit-1; the implementation
crashes loudly. Anyone enabling the hook before running `leonard init`
gets a panic on every Edit until they re-read the README.

## Findings

### F1 — pre-edit block response uses the wrong field for PreToolUse, and `continue: false` halts the whole agent
- **Status:** fixed on `bosun/fix-hooks` (2026-05-19). `PreEditResponse` no longer carries `decision`/`reason`/`stopReason`/`continue:false`; the deny path emits `hookSpecificOutput.permissionDecision: "deny"` + `permissionDecisionReason`, allow paths just emit `{"continue":true}`. Reproducer covered by `TestPreEditCmd_DenyWireShape` and `TestHandlePreEdit_BlocksFabricatedTrackedSymbol` (both assert the JSON does NOT contain `continue:false` or `decision`).
- **Severity:** high
- **Reproducer:**
  ```bash
  # Project with go.mod and an empty leonard.db
  mkdir -p /tmp/hookprobe/lib && cd /tmp/hookprobe && /tmp/leonard init .
  printf 'module example.com/hookprobe\n\ngo 1.22\n' > go.mod
  printf 'package lib\n\nfunc RealOne() {}\n' > lib/lib.go

  cat <<'JSON' | /tmp/leonard-hook pre-edit
  {
    "session_id": "s",
    "transcript_path": "/tmp/t.jsonl",
    "cwd": "/tmp/hookprobe",
    "permission_mode": "default",
    "hook_event_name": "PreToolUse",
    "tool_name": "Edit",
    "tool_use_id": "tu",
    "tool_input": {
      "file_path": "/tmp/hookprobe/payload.go",
      "old_string": "x",
      "new_string": "package main\nimport \"example.com/hookprobe/lib\"\nfunc main(){ lib.NonExistent() }"
    }
  }
  JSON
  ```
- **Observed:** exit 0 with
  ```json
  {"continue":false,"decision":"block","reason":"leonard pre-edit: blocked references to symbols not in the index: lib.NonExistent","stopReason":"…"}
  ```
  Two things are wrong:
  1. The PreToolUse output schema (per the current docs) uses
     `hookSpecificOutput.permissionDecision: "deny"` +
     `permissionDecisionReason`. The top-level `decision: "block"` is
     the **PostToolUse** field — at best it's silently ignored on
     PreToolUse, at worst Claude Code logs a warning.
  2. `continue: false` is the top-level "halt Claude entirely" knob
     (`"If false, Claude stops processing entirely"`). It's never the
     right escalation for "deny this one tool call" — even if `decision`
     was respected, `continue: false` would still abort the session.
- **Expected:** the documented PreToolUse-deny envelope, with
  `continue` omitted (defaulting to true) so the session keeps running
  after the deny:
  ```json
  {
    "hookSpecificOutput": {
      "hookEventName": "PreToolUse",
      "permissionDecision": "deny",
      "permissionDecisionReason": "leonard pre-edit: blocked references …"
    }
  }
  ```
- **Suggested fix shape:** rework `internal/hooks/pre_edit.go`'s
  `PreEditResponse` to mirror the SessionStart/Stop pattern (already
  used in the same package): a top-level shell + a
  `hookSpecificOutput` pointer carrying `hookEventName` /
  `permissionDecision` / `permissionDecisionReason`. The allow path
  should just emit `{"continue": true}` (or omit the field entirely —
  default is true). Drop `Continue: false`, `Decision`, `Reason`, and
  `StopReason` from the block path.
- **Out of scope for this investigation:** whether Claude Code is
  *currently* maintaining legacy `decision: "block"` compatibility on
  PreToolUse — even if it is today, the docs treat it as undocumented
  behavior and the `continue: false` halt is independently broken.

### F2 — every decode-error path returns exit 1, which Claude Code treats as **non-blocking**: fabrication guard fails open on malformed input
- **Status:** fixed on `bosun/fix-hooks` (2026-05-19). Decode errors in the four handlers now wrap a new `hooks.ErrDecode` sentinel; the cobra layer's `blockOnDecode` maps those to a typed `*exitErr{code: 2}` for pre-edit and post-edit, and `main.go` calls `os.Exit(exitCodeFor(err))`. Session-start and stop deliberately don't wrap (advisory hooks, exit 1 is correct). Reproducers: `TestPreEditCmd_DecodeFailureExitsBlocking`, `TestPostEditCmd_StdinErrorBubblesUp`, plus unit-level `TestBlockOnDecode_*`.
- **Severity:** high
- **Reproducer:**
  ```bash
  echo 'not json{' | /tmp/leonard-hook pre-edit
  # → "leonard-hook: pre-edit: hooks: decode PreToolUse payload: …" on stderr
  # → exit 1
  ```
  Same shape from `post-edit`, `session-start`, and `stop`. Also fires
  on empty stdin, missing `tool_input.file_path`, wrong-type fields
  (e.g., `session_id` as an int), and JSON whose top-level isn't an
  object.
- **Observed:** exit code 1 with stderr text and **no stdout**. Per
  the docs: *"Any other exit code is a non-blocking error for most
  hook events… Execution continues."* and *"only exit code 2 blocks
  the action… Claude Code treats exit code 1 as a non-blocking error
  and proceeds with the action, even though 1 is the conventional Unix
  failure code. If your hook is meant to enforce a policy, use `exit
  2`."* So Claude Code shows a hook-error notice and **proceeds with
  the Edit/Write**.
- **Expected:** decode errors from `pre-edit` should be blocking
  (exit 2) — a payload Leonard can't parse is a fabrication-guard
  bypass otherwise. For `post-edit`/`session-start`/`stop`, exit 1
  is *defensible* (the hook is advisory, so a failed audit shouldn't
  halt the tool call), but it should still be a deliberate choice
  rather than the side-effect of `cobra.Command.RunE` returning
  whatever the handler bubbled up.
- **Suggested fix shape:** map handler errors to exit codes
  per-hook at the cobra layer: pre-edit decode failures → exit 2
  (block), post-edit/session-start/stop decode failures → exit 1
  (non-blocking but logged). A small helper in `cmd/leonard-hook/`
  that detects "this is a payload-shape error" (vs a real
  infrastructure error) is enough; the handler itself can keep
  returning typed errors.
- **Out of scope for this investigation:** what to do when the
  *store* errors mid-handler (e.g., DB locked under concurrent
  access) — that's a separate decision tree and overlaps the
  selfhost lane's concurrent-access probe.

### F3 — `post-edit` panics with a Go stack trace when `.leonard/leonard.db` is missing instead of exiting cleanly
- **Status:** fixed on `bosun/fix-hooks` (2026-05-19). The post-edit cobra RunE now stat-checks `.leonard/leonard.db` before touching the Backend; missing DB short-circuits with `{"continue": true}` on stdout, `"leonard: post-edit skipped — run \`leonard init\` first"` on stderr, and exit 0. `mustOpenStore`'s remaining call sites (DB exists but `store.Open` fails) keep the panic — the brief explicitly out-of-scopes that case. Reproducer: `TestPostEditCmd_MissingDBNoOps` exercises the production `realBackend` against a fresh tmpdir to prove the panic path is unreachable.
- **Severity:** high
- **Reproducer:**
  ```bash
  mkdir /tmp/no-leonard && cd /tmp/no-leonard
  echo '{"session_id":"s","hook_event_name":"PostToolUse","cwd":"/tmp/no-leonard",
         "tool_name":"Edit","tool_input":{"file_path":"x.go"}}' \
    | /tmp/leonard-hook post-edit
  # → panic: store: ping sqlite: unable to open database file (14)
  # → goroutine 1 [running]: main.mustOpenStore(...)
  # →   /…/cmd/leonard-hook/backend_real.go:37
  # → exit 2
  ```
- **Observed:** A Go panic with full stack trace, exiting 2. Exit
  code 2 is the **blocking** code — so Claude Code surfaces the
  stderr text (the panic) to Claude as a tool-call error message,
  blocks the Edit, and tells Claude to fix it. Claude then sees a Go
  runtime panic and is likely to try to "fix" it by editing
  unrelated source, or by ignoring it.
- **Expected:** When `.leonard/leonard.db` is missing, the
  post-edit hook should exit 0 with a no-op response (matching the
  pattern `pre-edit`/`session-start`/`stop` already use:
  `permissiveStore`/`(nil, nil)` openers). Failing that, the
  fallback should be a clean exit-1 with a *user-facing* message
  ("leonard: not initialized — run `leonard init` first"), not a
  Go panic. The intent is already documented at
  `cmd/leonard-hook/backend_real.go:29`:
  *"mustOpenStore is intentionally panicky — exit-1 with a clear
  reason is more debuggable than a silent malformed claim row."*
  The implementation diverged from the comment: it currently exits
  2 with a stack trace.
- **Suggested fix shape:** mirror the `defaultStoreOpener` pattern
  from `session_start.go:26`. When the DB file is absent, return a
  no-op `Indexer`/`ClaimRecorder` pair and let `HandlePostEdit` emit
  its normal response — or short-circuit at the cobra layer and emit
  `{"continue": true, "systemMessage": "leonard: not initialized …"}`
  with exit 0.
- **Out of scope for this investigation:** what to do when the DB
  exists but `store.Open` fails for an unrelated reason (corruption,
  permissions). The current panic path covers that case too but a
  cleaner exit-1 still applies.

### F4 — `post-edit` writes its index/vet result to `systemMessage` only — Claude never sees the vet outcome
- **Severity:** medium
- **Reproducer:** any successful `post-edit` invocation:
  ```bash
  cd /tmp/hookprobe && cat /tmp/payload-clean.json | /tmp/leonard-hook post-edit
  # → {"continue":true,"systemMessage":"leonard: re-indexed …/clean.go, go vet ok"}
  ```
- **Observed:** the index/vet outcome lands only in the top-level
  `systemMessage` field. Per the docs `systemMessage` is "Warning
  message shown to user" — it's a UX channel for the operator, not a
  context channel for Claude. The PostToolUse output also supports
  `hookSpecificOutput.additionalContext` (the same field
  `session-start` and `stop` already use), which **is** added to
  Claude's context window for the next turn.
- **Expected:** when vet fails (i.e., the verifier flagged the
  edit), Claude should see that *and* the user should see it. The
  current implementation tells the user but not the model — defeats
  DESIGN.md §1's "kills false 'done' claims" goal because Claude can
  still confidently say "tests pass" after a vet failure it was
  never told about. The `Stop` hook will eventually surface
  unverified claims, but a post-edit vet failure should reach Claude
  inside the same turn.
- **Suggested fix shape:** keep `systemMessage` for the user-facing
  summary, **also** populate
  `hookSpecificOutput.additionalContext` with a Claude-readable
  version (e.g., the vet output + claim-id) so the model knows it
  has an unverified claim to address. The `hookSpecificOutput` shape
  for PostToolUse is documented and the same pattern is already
  implemented in `internal/hooks/session_start.go` and
  `internal/hooks/stop.go`.
- **Out of scope for this investigation:** whether the post-edit
  hook should also use `decision: "block"` + `reason` on vet
  failures (that's the documented way to *force* Claude to address a
  failure rather than just observe it). Phase 1's brief is explicit
  that post-edit is non-blocking; revisiting that is a phase-N
  question.

### F5 — `post-edit` reports "re-indexed" for files that don't exist on disk
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/hookprobe && cat <<'JSON' | /tmp/leonard-hook post-edit
  {"session_id":"s","hook_event_name":"PostToolUse","cwd":"/tmp/hookprobe",
   "tool_name":"Edit","tool_input":{"file_path":"/tmp/this/does/not/exist.go"}}
  JSON
  # → {"continue":true,"systemMessage":"leonard: re-indexed /tmp/this/does/not/exist.go, go vet reported issues — claim recorded as unverified"}
  ```
- **Observed:** `indexErr == nil` (the indexer silently no-ops on a
  missing path), so `summaryMessage` (`internal/hooks/post_edit.go:260`)
  falls into the "vet reported issues" branch and asserts the file was
  re-indexed. The claim row records "index=ok" too, which is wrong —
  there was nothing to index.
- **Expected:** the indexer should signal "file not found" as a real
  error so `summaryMessage` can emit something like *"leonard:
  file not found, skipping re-index"*. Either side of the contract
  works (indexer returns an error, or the hook stat-checks the path
  before calling), but the current claim that the file was re-indexed
  is misleading evidence.
- **Suggested fix shape:** add an `os.Stat` check at the top of
  `HandlePostEdit` after the file_path extraction. On ENOENT,
  short-circuit with a "file not found, skipping" claim row and
  matching system message. Don't change the indexer contract — keep
  it tolerant of missing files for other callers.
- **Out of scope for this investigation:** what to do when the
  file exists but isn't tracked by Leonard (e.g., it's in
  `node_modules/`). The same code path probably wants similar
  handling.

### F6 — `pre-edit` silently allows `MultiEdit`, `NotebookEdit`, and unknown future write-shaped tools
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/hookprobe && cat <<'JSON' | /tmp/leonard-hook pre-edit
  {"session_id":"s","hook_event_name":"PreToolUse","cwd":"/tmp/hookprobe",
   "tool_name":"MultiEdit","tool_input":{
     "file_path":"/tmp/hookprobe/lib/lib.go",
     "edits":[{"old_string":"func RealOne()","new_string":"func RealOne(){ lib.NonExistent() }"}]
   }}
  JSON
  # → {"continue":true}
  ```
  Same pass-through for any `tool_name` not in `{"Edit","Write"}` —
  see `internal/hooks/pre_edit.go:147` `snippetForTool`.
- **Observed:** `snippetForTool` only knows about `Edit` and `Write`.
  Anything else returns `targeted=false` and the hook allows the call
  unconditionally. The current Claude Code docs only list `Edit` and
  `Write` for file-modifying tools, but `MultiEdit` and
  `NotebookEdit` have shipped in older versions and may again — and
  any v0 IDE-integration MCP tool with its own "write code" surface
  also bypasses the guard.
- **Expected:** `MultiEdit` should be expanded into its list of
  edits (each with `file_path` + `old_string` + `new_string`) and
  every snippet should be checked. `NotebookEdit` should at minimum
  surface a warning that the guard didn't run.
- **Suggested fix shape:** broaden `snippetForTool` to handle
  `MultiEdit` (iterate `edits[]`, concat or check each separately)
  and `NotebookEdit` (treat `new_source` like `new_string`). For
  unknown tool names that match a "writes a path" shape, fall back
  to a configurable list rather than a hard-coded one — the leonard
  config already has a `[hooks]` section that could carry an
  `extra_write_tools` knob.
- **Out of scope for this investigation:** whether MCP tool calls
  (e.g., `mcp__leonard__record_decision`) should ever fall under
  pre-edit. Almost certainly not, but worth a one-line note in the
  brief.

### F7 — `session-start` doesn't react to `source` — re-injects all decisions on every event including `compact`
- **Severity:** medium
- **Reproducer:** any `SessionStart` payload with `"source":
  "compact"`:
  ```bash
  cd /tmp/hookprobe && cat <<'JSON' | /tmp/leonard-hook session-start
  {"session_id":"s","transcript_path":"/tmp/t","cwd":"/tmp/hookprobe",
   "hook_event_name":"SessionStart","source":"compact","model":"claude-sonnet-4-6"}
  JSON
  ```
- **Observed:** the handler decodes `Source` (`session_start.go:23`)
  but never reads it. Every SessionStart firing — startup, resume,
  clear, **and compact** — injects the same N most-recent decisions
  block. After a `compact` event Claude already has the prior
  conversation summarized; re-injecting "## Prior decisions (from
  Leonard)" on top of that is duplicate context and wastes tokens.
  `clear` is similarly suspect: the user explicitly asked to start
  fresh.
- **Expected:** the handler should at least skip injection when
  `source == "compact"` (Claude already retained the decisions via
  the summary) and document its behavior for `clear`. `startup` and
  `resume` should both inject.
- **Suggested fix shape:** branch on `payload.Source` early in
  `HandleSessionStart`. The `source` values are documented:
  `"startup"`, `"resume"`, `"clear"`, `"compact"`. A small map of
  "should we inject for this source?" suffices.
- **Out of scope for this investigation:** whether the decisions
  block should de-dupe against what's already in the
  conversation transcript — that requires the transcript-reading
  capability the brief doesn't yet have.

### F8 — `pre-edit`'s blocking guard is bypassable by sending a Write payload whose `content` references unknown packages without imports
- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/hookprobe && cat <<'JSON' | /tmp/leonard-hook pre-edit
  {"session_id":"s","hook_event_name":"PreToolUse","cwd":"/tmp/hookprobe",
   "tool_name":"Edit","tool_input":{
     "file_path":"/tmp/hookprobe/lib/clean.go",
     "old_string":"x","new_string":"lib.NonExistent()"
   }}
  JSON
  # → {"continue":true}
  ```
- **Observed:** A naked fragment with no import block — e.g., an
  Edit replacing a function body — gets wrapped by `parseSnippet`'s
  third attempt (`"package leonardshim\n\nfunc _leonardShim() {\n…\n}\n"`).
  `collectImportsFromFile` then finds zero imports in the wrapped
  snippet. `readFileImports` parses the *real* target file's import
  block — but in `clean.go` the `lib` alias isn't imported (because
  it's a peer package). The handler concludes `lib` isn't an alias
  it knows about and lets the reference through.
- **Expected:** when a snippet calls `pkg.Name` and `pkg` isn't
  in any known import list, the hook can't decide whether it's
  fabricated or external. Today it falls open. A safer default is
  to *attempt to resolve `pkg` as a sibling-package alias* by
  scanning the target file's package's directory, and only fall
  open when nothing matches. The DESIGN.md "kills fabricated APIs"
  goal calls for "no false negatives" on tracked packages — and
  sibling-package references inside the same module are tracked
  packages.
- **Suggested fix shape:** extend `readFileImports` (or add a peer
  function) to also enumerate `package <name>` headers from sibling
  `.go` files under the same module path. The result is a small map
  of "known package names in this module" that augments the import
  alias map. Out of v0 scope per the phase-3 brief's deferred
  pre-edit follow-ups — flagging here so the fix round has a
  pointer.
- **Out of scope for this investigation:** cross-module resolution
  (already deferred per DESIGN.md phase-3 §pre-edit follow-ups).

### F9 — exit-code-1-for-decode-errors interaction with empty stdin: `pre-edit` fails open when Claude Code sends a malformed PreToolUse payload during agent stress
- **Severity:** medium
- **Reproducer:** see F2. Also fires under a Cobra-level error
  (e.g., `leonard-hook pre-edit --unknownflag`) which surfaces as
  exit 1 with no JSON.
- **Observed:** same fail-open behavior as F2, but worth a separate
  callout because the *Cobra* error path (unknown flag, args
  validation) goes through the same code path. Any wrapper script
  that accidentally forwards a flag to `leonard-hook` silently
  bypasses the guard.
- **Expected:** the cobra wrapper should map "this is a cobra
  parse/usage error" to exit 1 (non-blocking) and "this is a
  hook-protocol-violation error" to exit 2 (blocking), at least for
  `pre-edit`.
- **Suggested fix shape:** in `cmd/leonard-hook/main.go`, after
  `root.Execute()` returns an error, check if the error wraps a
  payload-decoding error from `internal/hooks` and exit 2 in that
  case. Cobra usage errors remain exit 1.
- **Out of scope for this investigation:** what to do about
  `post-edit`'s mustOpenStore panic interacting with exit 2 (F3
  covers the panic itself).

### F10 — `stop` handler decodes `stop_hook_active`, a field that's no longer in the documented Stop envelope
- **Severity:** low
- **Reproducer:** field-level audit of `internal/hooks/stop.go:25`
  vs the current Stop input docs.
- **Observed:** `StopPayload.StopHookActive` has JSON tag
  `"stop_hook_active"`. The current Stop envelope documents only
  `session_id`, `transcript_path`, `cwd`, `permission_mode`,
  `effort`, `hook_event_name`. `stop_hook_active` appeared in
  earlier hook docs (it was the "already inside a stop hook" guard).
  Either it was renamed or removed.
- **Expected:** the field name probably shouldn't be tracking
  legacy docs.
- **Suggested fix shape:** drop `StopHookActive` from the
  `StopPayload` struct — it's unused. Or alias it as an `any` so a
  future re-emergence doesn't break decode. The current code is
  harmless (Go ignores absent fields), so this is informational.
- **Out of scope for this investigation:** whether any of the
  *other* removed-from-docs fields are still relevant (e.g.,
  whether older Claude Code versions still send
  `stop_hook_active`).

### F11 — allow responses don't carry a `hookEventName` to self-identify
- **Severity:** low
- **Reproducer:** `pre-edit`'s allow path emits `{"continue":true}`
  with no `hookSpecificOutput.hookEventName`. Same shape from
  `session-start` when there are no decisions to inject, and from
  `stop` when there are no unverified claims.
- **Observed:** Claude Code accepts the minimal response, so this
  isn't broken — but every other hook response in the docs carries
  `"hookEventName": "<HookName>"` inside `hookSpecificOutput`, which
  helps when debugging. The non-empty branches in
  `session-start` and `stop` already set it; only the
  no-injection branches and the `pre-edit` allow path skip it.
- **Expected:** consistent shape across allow / no-op / block
  responses.
- **Suggested fix shape:** when emitting an allow or no-op
  response, still populate `hookSpecificOutput.hookEventName` (no
  `additionalContext`). Trivial — three lines per handler.
- **Out of scope for this investigation:** nothing.

### F12 — type-mismatch errors leak Go-internal struct field paths to the operator log
- **Severity:** low
- **Reproducer:**
  ```bash
  echo '{"session_id": 12345, "hook_event_name":"Stop"}' | /tmp/leonard-hook stop
  # → leonard-hook: stop: hooks: decode Stop payload: json: cannot unmarshal number into Go struct field StopPayload.session_id of type string
  ```
- **Observed:** error message exposes `StopPayload.session_id` and
  type info. Fine for a Go dev; noise for an operator who's writing
  a hook payload by hand or watching the debug log.
- **Expected:** a friendlier message like "session_id must be a
  string" — also makes the message resilient to renaming the
  internal struct.
- **Suggested fix shape:** wrap `json.Unmarshal` errors of
  `*json.UnmarshalTypeError` and produce a human message keyed off
  the JSON field name, not the Go struct field name. Cheap and
  improves debuggability.
- **Out of scope for this investigation:** nothing.

### F13 — full stdin is buffered into memory before any validation
- **Severity:** informational
- **Reproducer:**
  ```bash
  # 2 MB Edit.new_string parses fine and fast (~30ms), but
  # io.ReadAll has no upper bound:
  yes | head -c 50000000 | /tmp/leonard-hook pre-edit  # hypothetical
  ```
- **Observed:** all four decoders use `io.ReadAll(stdin)`
  unconditionally before any size check. A misbehaving Claude Code
  build or a malicious wrapper could feed an arbitrarily large
  payload; the hook would happily buffer 1 GB before erroring on
  JSON parse.
- **Expected:** a reasonable upper bound (say 10 MB — well above
  any plausible Edit/Write payload) returned as a clean
  "payload too large" error.
- **Suggested fix shape:** wrap stdin in `io.LimitReader` at a
  configurable cap. Default to 10 MB. On EOF before parse
  completion, error with a specific message.
- **Out of scope for this investigation:** whether `pre-edit`'s
  `go/parser` call has its own memory concerns at >1 MB inputs
  (tested at 2 MB — fine).

### F14 — relative `file_path` in `tool_input` is passed through without canonicalization
- **Severity:** informational
- **Reproducer:**
  ```bash
  cd /tmp/hookprobe && cat <<'JSON' | /tmp/leonard-hook post-edit
  {"session_id":"s","hook_event_name":"PostToolUse","cwd":"/tmp/hookprobe",
   "tool_name":"Edit","tool_input":{"file_path":"lib/lib.go"}}
  JSON
  # → re-indexes "lib/lib.go" — happens to work because cwd is correct,
  #   but the claim row records the relative path literally.
  ```
- **Observed:** the handler trims the path but doesn't canonicalize
  it against `cwd`. Claims and index rows then carry mixed
  absolute/relative paths depending on what Claude Code happens to
  send.
- **Expected:** canonicalize relative paths against `payload.CWD`
  (which the schema guarantees) before passing to the indexer and
  recording in claims. Self-host completeness depends on this — the
  hunt-selfhost lane will see false negatives from a path-shape
  mismatch otherwise.
- **Suggested fix shape:** one `filepath.Abs` + `filepath.Clean`
  call near the top of `HandlePostEdit` after extracting the
  file_path. Same in `decidePreEdit`.
- **Out of scope for this investigation:** path-traversal hardening
  (the hook runs in-session, no trust boundary worth defending).

## Things that worked

- **Forward-compat with extra unknown fields.** All four decoders
  tolerate fields the schema doesn't define (e.g., `agent_id`,
  `permission_mode`, `effort`, future additions). Verified with a
  SessionStart payload containing
  `"future_field_that_does_not_exist": {…}` — clean exit 0 and the
  expected `{"continue":true}` response. This is just Go's default
  `encoding/json` behavior, but it's the right default.
- **Unicode in file paths and Edit content.** Tested with a
  fabricated path `/tmp/hookprobe/lib/世界.go` and a `new_string`
  containing `café — résumé — 🎉\nfunc Ünicodé() {}`. Decode and
  parse handle UTF-8 cleanly; go/parser accepts non-ASCII
  identifiers per the Go spec.
- **Large `Edit.new_string` (2 MB).** Pre-edit parses a payload
  with a 2 MiB string literal in ~30 ms. `go/parser` is more than
  fine at this size.
- **Pre-edit pass-through for non-Edit/Write tools and non-Go
  files.** Bash, Read, README.md, etc. all pass through cleanly
  with `{"continue":true}` and zero work done. (F6 is a *coverage*
  gap — MultiEdit also passes through — but the pass-through itself
  is correct for the documented tool list.)
- **Missing `.leonard/leonard.db` no-op behavior on three of four
  hooks.** `pre-edit` (permissive store), `session-start` (nil
  decision reader → no injection), `stop` (nil claims reader → no
  surfacing) all degrade gracefully on a fresh checkout. Only
  `post-edit` panics — see F3.
- **Empty `decisions`/`claims` slice → minimal allow response with
  no `hookSpecificOutput`.** `session-start` and `stop` both emit
  `{"continue":true}` when there's nothing to surface, which matches
  the docs' "any field omitted is acceptable" guidance.
- **Per-handler unit-test seams** (`storeOpener`, `claimsStoreOpener`,
  `preEditOpener`, `modulePathReader`, `VetRunner`). Each
  collaborator is replaceable; the test suites in
  `cmd/leonard-hook/*_test.go` and `internal/hooks/*_test.go` exercise
  the seams cleanly. This is the part of the code I'd expect to be
  cheapest to fix the findings above against.

## Open questions

1. **Does Claude Code currently maintain legacy `decision: "block"`
   support on PreToolUse, or has the field been removed?** F1 is
   high-severity either way (the `continue: false` halt is
   independent), but the *exact failure mode* depends on this. A
   real Claude Code session probing the current behavior would
   resolve it.
2. **Is `MultiEdit` still a Claude Code tool in production?** The
   docs only list `Edit`/`Write`. If MultiEdit is fully retired,
   F6's "future write-shaped tools" framing matters more than the
   immediate pass-through. If it's still alive in some Claude Code
   build, F6 escalates to high.
3. **What does `post-edit`'s vet-failure case actually look like end
   to end when wired into Claude Code?** F4 argues that
   `systemMessage` isn't visible to the model. A live session would
   confirm — the docs are clear but worth eyeballing.
4. **Should `pre-edit` ever block on a Write whose content imports
   a package the snippet itself doesn't declare?** This is the F8
   sibling-package case dressed as a policy question. The phase-3
   brief explicitly defers cross-module resolution to "phase
   3+follow-ups", but sibling-package within the same module is
   arguably in v0 scope.
5. **The post-edit handler runs `go vet ./...` synchronously
   inside the hook.** DESIGN.md §7 Q4 (<200ms p95 budget) is
   explicitly out of scope for this round, but `go vet ./...` on a
   medium repo can take seconds — and the hook blocks Claude Code's
   turn until it returns. The current implementation has a 30s
   timeout; the question is whether the budget should be much
   tighter than that.
