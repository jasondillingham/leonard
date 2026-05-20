# Bug Hunt #4 — mcp-and-hooks

## Summary

Re-audited the MCP server and the four hook handlers as a single system,
with binaries rebuilt fresh (`go install ./cmd/leonard-mcp ./cmd/leonard-hook`)
and driven from a real MCP `CommandTransport` client harness plus
synthetic-stdin hook drivers. The v0.9.0 resource caps land cleanly at
the field level and concurrency under WAL holds up (5,184 MCP queries
+ 154 post-edit hook runs in 8s with 0 errors). The most consequential
new finding is **response-size amplification**: a malicious or
buggy session can pollute `decisions` with 200 max-sized rows
(~36 KiB each after the caps) and `get_decisions(limit=200)` returns
**7.4 MB** of structured content; that same data lands in the
SessionStart hook on every subsequent `startup`/`resume` because
`formatDecisions` has no per-row truncation (unlike the Stop hook,
which already truncates to 120 runes per bullet). A model can
inadvertently DoS its own next session.

Three behaviours that the previous rounds left open are still
materially broken in production:

1. **Security-1 F12 oversize-line is technically alive.**
   `newOversizeTolerantScanner` is documented as a no-op stub that
   "at least logs a clear message." In practice, after a 16+ MiB
   stdin line the `bufio.Scanner` is poisoned: the read loop hits
   `ErrTooLong` on every subsequent `Scan()` and spins, spamming
   stderr with "dropped oversize line" forever and **delivering no
   further responses to the client**. The session is effectively dead
   from the client's perspective even though the process is alive.
2. **MCP-recorded claims cannot ever be superseded.** `record_claim`
   accepts no `file_path` field; the adapter passes an empty
   `FilePath` to the store; `SupersedeClaimsForFile` short-circuits
   on empty `file_path`. A model that records an unverified claim
   via MCP, then has Claude Code's post-edit hook subsequently land
   a vet=ok run, ends the session with the orphaned unverified row
   still surfacing on the Stop hook.
3. **`handleMissingFile` and `handleEscapedPath` are
   indistinguishable on the ledger** except via human-readable
   substring search of the `claim` text, and `handleMissingFile`
   carries no `hookSpecificOutput.additionalContext` while
   `handleEscapedPath` does — so Claude sees the escape rejection
   but not the missing-file skip. The two failure modes were
   reasoned about together in the post-edit handler but their
   reach back to the model differs.

Several smaller findings: caps aren't mentioned in tool schemas (a
caller has no way to know `topic` is 256 B before sending 10 KiB);
`record_decision.related_files` is unvalidated, so foreign paths
like `/etc/hosts` happily live in the DB; SessionStart payloads
silently bloat the model's context if the store has been polluted;
unknown-method calls return a JSON-RPC error with `code:0` instead
of a documented MCP error code; and post-edit's `exit 2` semantics
on payload decode failure are documented as "block" but the edit
has already happened by the time PostToolUse fires — exit 2 is the
right code (Claude Code surfaces stderr to the model) but the
comment in `cmd/leonard-hook/exit.go` overstates it.

Scratch harness lives at `/tmp/bughunt-4/harness/` (Go module with a
`replace` pointing at this worktree) and the probe project (real
`.leonard/leonard.db` copy plus a few synthetic Go files) at
`/tmp/bughunt-4/probe/`. Both are outside version control. Several
`/tmp/bughunt-4/harness/*.go.bak` files in the harness dir are
single-file scratch drivers preserved as `.bak` so they don't
conflict with the multi-probe `main.go`.

## Findings

### F1 — Oversize stdin line still kills the MCP session (Security-1 F12 alive)

- **Severity:** high
- **Reproducer:**

  ```text
  /tmp/bughunt-4/harness/stdio_probe.go.bak — opens raw pipes to
  leonard-mcp, completes the handshake, writes one line of 16 MiB+10
  bytes of 'x', closes the line with '\n', then writes a valid
  tools/list call.

  Result:
    STDOUT: {"jsonrpc":"2.0","id":1,"result":{ … handshake ok … }}
    --- TIMEOUT — server hung after oversize line ---
  ```

  Source: `cmd/leonard-mcp/stdin_filter.go:60-71`. After
  `bufio.ErrTooLong` the loop calls `newOversizeTolerantScanner`,
  which returns the same poisoned scanner (lines 93-106 self-document
  this: *"the function just returns the same Scanner"*). The next
  iteration's `Scan()` returns `ErrTooLong` again, the loop logs +
  continues, and the process spins on the same byte position forever.
  Stderr fills with "dropped oversize line on stdin (exceeds Scanner
  buffer cap); continuing"; the client receives no further responses
  and eventually times out the call. CPU pegs.
- **Observed:** Session is dead from the client's perspective. From the
  process's perspective the binary is still running and burning CPU.
  A `tools/call` issued in flight at the moment of the oversize line
  either hangs forever or eventually surfaces as a transport read
  error after the client's deadline.
- **Expected:** Per the F12 comment intent: drop the oversize line,
  emit a single log, resume reading from the next newline. The
  documented design was "log and skip"; the actual behavior is "log
  and die louder."
- **Suggested fix shape:** Replace `bufio.Scanner` with a streaming
  line reader that can advance past an oversize line by discarding
  bytes until the next `\n`. `bufio.Reader.ReadSlice('\n')` returns
  `bufio.ErrBufferFull` for an oversize line but leaves the buffer
  positioned where you can keep draining until you find the
  terminator. A handful of lines.
- **Out of scope for this investigation:** Whether the 16 MiB cap is
  the right number; the cap itself looks reasonable.

### F2 — `get_decisions(limit=200)` can return ~7.4 MB of structured content; no response-size cap

- **Severity:** high
- **Reproducer:** `/tmp/bughunt-4/harness/main.go::probeResponseSize`
  (or `./harness response-size`):

  ```text
  recording 200 decisions with maxed reasoning (32 KiB)...
    inserted in 230ms
    fetched in 397ms, isError=false
    structuredContent bytes=7,390,834
    text bytes=7,390,834
  ```

  Each `record_decision` cap (topic 256 B + choice 4 KiB + reasoning
  32 KiB ≈ 36.6 KiB per row); 200 rows × ~37 KiB ≈ 7.4 MB. Both the
  text and structured-content forms carry the full payload.
- **Observed:** A model running `record_decision` in a loop (or a
  bug that records too many decisions per session) can grow the
  store to multiple MB of decisions; `get_decisions(limit=200)`
  returns the lot in one MCP response. Claude reads the full result
  into its context.
- **Expected:** The MCP layer should bound per-call response size.
  The field-level caps (32 KiB reasoning, 4 KiB choice) prevent any
  single row from being ridiculous, but the aggregate isn't
  bounded. `get_unverified_claims` already omits `evidence` for
  this exact reason (comment at `claims.go:38-42`); the same
  thinking should apply to `get_decisions`'s `reasoning`.
- **Suggested fix shape:** Either (a) truncate `reasoning` to a
  preview (e.g. first 500 chars + ellipsis) in the
  `decisionRecordToEntry` translation, with a sibling `get_decision`
  tool for full text by id; or (b) lower the per-row cap (e.g.
  reasoning 4 KiB) so a 200-row response stays under ~1 MB. The
  current 32 KiB ceiling is much larger than realistic prose.
- **Out of scope for this investigation:** Whether the 200-row cap
  itself is sensible — for normal use it is, the issue is the
  per-row size.

### F3 — Same data bloats SessionStart injection on every startup/resume

- **Severity:** high
- **Reproducer:** After F2's pollution, run any non-compact/clear
  SessionStart payload:

  ```text
  $ echo '{"session_id":"x","hook_event_name":"SessionStart","source":"startup","cwd":"/tmp/bughunt-4/probe"}' \
      | leonard-hook session-start > /tmp/resp.json
  $ wc -c /tmp/resp.json
  577,138  /tmp/resp.json
  ```

  Polluted store with only 200 32-KiB-reasoning rows, default
  `DefaultDecisionsLimit = 10` — but the first 10 rows happen to be
  the response-size-* set, so the injection alone is 577 KB of
  Markdown. (The harness output is larger only because the harness
  printed a preview line; the underlying additionalContext is
  ~580 KB.)

  Source: `internal/hooks/session_start.go:159-173`. `formatDecisions`
  joins:

  ```go
  fmt.Fprintf(&b, "- **%s** → %s", topic, choice)
  if reason != "" {
      fmt.Fprintf(&b, " — %s", reason)
  }
  ```

  `reason` comes from `firstNonEmptyLine(d.Reasoning)`. A 32-KiB
  reasoning with no embedded newlines counts as one line and is
  passed through unchanged. Compare with `internal/hooks/stop.go:69-70`,
  which already has `stopClaimPrefixMax = 120` and truncates each
  bullet via `truncatePrefix`.
- **Observed:** Every `source=startup`/`source=resume` invocation
  injects up to ~360 KiB of Markdown into Claude's context (10 rows
  × 36 KiB worst case). On a polluted project that's a real
  context-budget hit on every session start.
- **Expected:** Per-bullet truncation, matching `stop.go`'s
  pattern. A decision's `choice` is meant to be one short line and
  `reasoning` a sentence or two — neither needs >200 chars in the
  injection.
- **Suggested fix shape:** Mirror `stop.go`'s `stopClaimPrefixMax`
  and `truncatePrefix` for both `choice` and `reason`. A
  `sessionStartFieldMax` of 200-300 runes per field gives plenty of
  room for the actual decision while preventing the bloat.
- **Out of scope for this investigation:** Whether the per-field
  cap should kick in at the `record_decision` validation layer
  instead (F2 covers that angle).

### F4 — MCP-recorded claims cannot be superseded by post-edit hooks

- **Severity:** medium
- **Reproducer:**

  1. From an MCP session: `record_claim {claim:"vet might fail on
     x.go", evidence:"none yet", verified:false}`. Store row has
     `file_path = NULL`.
  2. Edit x.go via Claude Code; post-edit hook fires, runs vet=ok,
     records a new claim with `file_path = x.go, verified=true`,
     then calls `SupersedeClaimsForFile(x.go, …)`.
  3. The MCP claim is **not** superseded because
     `SupersedeClaimsForFile`'s `WHERE file_path = ?` clause never
     matches a NULL/empty file_path (the store also short-circuits
     empty path → `(0, nil)` at line 884).
  4. `get_unverified_claims` still returns the orphaned row; Stop
     hook surfaces it to the user.

  Verified against the probe DB:

  ```text
  $ sqlite3 .leonard/leonard.db "SELECT id, file_path, verified, superseded_by_claim_id FROM claims ORDER BY id DESC LIMIT 5"
  231|/tmp/.../broken.go|0|
  229|/tmp/.../huge_broken.go|0|
  228|/tmp/.../broken.go|0|
  224||0|              ← MCP-recorded; no file_path; permanent unverified
  ```
- **Observed:** A session that records an unverified claim via MCP
  has no path to clear it; the Stop hook re-surfaces it forever.
- **Expected:** Either (a) `record_claim` accepts an optional
  `file_path` so models can scope claims to the file they're
  reasoning about, and the post-edit supersession then picks up
  those rows the next time vet passes on that file; or (b) the
  schema documents that MCP-recorded claims are unscoped and
  require the model to explicitly resolve them (no tool exists for
  that today). The current behavior — accept claims, surface them
  forever, no path to mark them resolved — is the worst of both.
- **Suggested fix shape:** Add `file_path` (optional) to
  `RecordClaimInput`; thread it through the adapter to the store;
  document in the schema that supplying it enables the post-edit
  supersession lifecycle. Cheaper alternative: add a `resolve_claim`
  MCP tool (`PUT claims/{id} verified=true`).
- **Out of scope for this investigation:** Whether the lifecycle
  needs a more elaborate "verify_claim" tool that re-runs vet/test
  to confirm.

### F5 — `handleMissingFile` doesn't carry hookSpecificOutput; the model can't tell its edit was a no-op

- **Severity:** medium
- **Reproducer:**

  ```text
  $ echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"/tmp/bughunt-4/probe/does-not-exist.go"},"cwd":"/tmp/bughunt-4/probe","hook_event_name":"PostToolUse"}' \
      | leonard-hook post-edit
  {"continue":true,"systemMessage":"leonard: /tmp/bughunt-4/probe/does-not-exist.go file not found, skipping re-index"}
  ```

  Compare with `handleEscapedPath`:

  ```text
  $ echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"/etc/hosts"},...}' \
      | leonard-hook post-edit
  {"continue":true,"systemMessage":"leonard: /etc/hosts rejected — path escapes project root","hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Leonard rejected file_path \"/etc/hosts\": ..."}}
  ```

  Source: `internal/hooks/post_edit.go:257-280` (missing) sets only
  `SystemMessage`. Lines 289-319 (escape) set both `SystemMessage`
  and `hookSpecificOutput.additionalContext`. The asymmetry is
  intentional only if missing-file is considered "not the model's
  problem" — but that's the wrong default: if Claude edits a path
  that doesn't exist, that's almost always Claude misjudging
  whether its previous `Write` actually landed.
- **Observed:** The model receives no feedback when a PostToolUse
  fires on a missing file. Same situation, escaped path, the model
  sees the rejection.
- **Expected:** Either both paths set additionalContext, or
  neither. A model that just edited a file Claude Code rejected
  should know the edit didn't land — otherwise it'll proceed
  reasoning about a state of the world that isn't real.
- **Suggested fix shape:** Add an `AdditionalContext` to
  `handleMissingFile` along the lines of "Leonard observed a
  PostToolUse for X but the file doesn't exist on disk. Your
  previous edit may have been rejected (e.g. by a permission hook).
  Verify the file's contents before continuing."
- **Out of scope for this investigation:** Whether the two paths
  should share a structured discriminant in the ledger (see F6).

### F6 — Missing-file and escaped-path claim rows are indistinguishable in the ledger

- **Severity:** low
- **Reproducer:**

  ```text
  $ sqlite3 .leonard/leonard.db \
      "SELECT id, tool, file_path, index_ok, vet_ok FROM claims WHERE id IN (226,227)"
  226|Edit|/etc/hosts|         | ← escaped
  227|Edit|/tmp/.../doesnt|    | ← missing
  ```

  Both rows have `tool=Edit`, both have `index_ok=NULL`, both have
  `vet_ok=NULL`, both have a file_path. The only differentiator is
  the human-readable string inside the `claim` text column
  ("rejected (path escapes project root)" vs "skipped (file not
  found)").
- **Observed:** A future tool that wants to filter claims by
  failure type can only LIKE-grep the claim text. No structured
  column captures the failure mode.
- **Expected:** Either a discriminator column (e.g. `failure_mode`
  enum: ok / missing / escaped / index_err / vet_fail), or the
  claim text becomes a parser-friendly canonical string. Today's
  prefixes are stable but neither documented nor tested as such.
- **Suggested fix shape:** Add a `failure_mode` TEXT column to the
  claims table; populate it in both handle\* paths; expose it via
  `ClaimEntry`. Or: define and export the prefix strings from
  `internal/hooks` so downstream callers can match on them
  consistently.
- **Out of scope for this investigation:** Whether v0 needs
  consumers of this discriminator yet.

### F7 — `record_decision.related_files` is unvalidated; foreign paths land in the DB

- **Severity:** medium
- **Reproducer:**

  ```text
  $ ./harness foreign-related-files
  isError=false
  result: {"decision_id":229}
  decision row: {... "related_files":["/etc/hosts","../../../etc/passwd","/dev/null"] ...}
  stale: {"decisions":[{"decision":{...},
       "missing_files":["/etc/hosts","../../../etc/passwd","/dev/null"]}]}
  ```

  Source: `internal/mcp/decisions.go:104-122`. No validation of
  `RelatedFiles` / `RelatedSymbols` — they're handed straight to
  the store, which JSON-encodes them into the row. The stale-check
  later runs `SELECT 1 FROM files WHERE path = ?` against each
  entry, which always fails for foreign paths, so they
  perma-appear in the stale list (cosmetic noise plus a
  security-adjacent surface).
- **Observed:** A model can plant arbitrary strings — file paths
  outside the project root, paths with embedded NUL, very long
  paths — into the DB via `related_files`. The strings are inert
  (nothing executes or stat()s them in a privileged way) but they
  pollute the `get_stale_decisions` output forever.
- **Expected:** At a minimum, validate that each entry is a
  reasonable project-relative path: doesn't contain `..` segments,
  doesn't start with `/` (the in-repo convention seems to be
  relative paths — confirmed by inspecting the real `.leonard/leonard.db`),
  and a length cap to prevent storing megabyte-paths. Reject (or
  silently drop) anything that looks foreign.
- **Suggested fix shape:** In `recordDecision`, after the topic
  check, iterate `in.RelatedFiles` and apply a `filepath.IsLocal`-style
  guard (or call `index.ResolveSafe` lexically, without touching
  the filesystem). Same for `related_symbols` — bound the length
  per entry and array count.
- **Out of scope for this investigation:** Whether the
  stale-decisions tool itself should filter out foreign paths
  retroactively (F11 below).

### F8 — Caps aren't documented in any tool schema; a caller has no way to budget

- **Severity:** low
- **Reproducer:** `tools/list` →

  ```text
  record_decision/topic:    "the decision topic (e.g. ...)"  ← no 256 B cap mentioned
  record_decision/choice:   "what was chosen (one short line)"  ← no 4 KiB cap
  record_decision/reasoning: "why it was chosen (a sentence or two)"  ← no 32 KiB cap
  record_claim/claim:        "the assertion being recorded ..."  ← no 4 KiB cap
  record_claim/evidence:     "supporting evidence ..."  ← no 256 KiB cap
  ```

  None of `internal/mcp/{decisions,claims}.go`'s jsonschema tags
  reference the limits. The error message correctly says "topic
  exceeds 256 bytes" but only after the call has already paid the
  round-trip cost (and the model has already committed to writing
  a too-long payload).
- **Observed:** Models budget against the description; a 30-KiB
  reasoning section is plausibly "a sentence or two" only if you
  squint. A `maxLength` in the schema is the standard way to
  surface a hard ceiling.
- **Expected:** Either JSON-Schema `maxLength` attributes or a
  prose sentence in the description (e.g. `"...; capped at 32 KiB"`).
  `maxLength` would push the rejection into the validator layer
  for cheaper feedback.
- **Suggested fix shape:** Add `jsonschema:"...; maxLength: 256"`
  (or whatever the SDK's tag syntax accepts) to each capped field.
  The schema is the contract; the description should match the
  handler.
- **Out of scope for this investigation:** Whether the SDK's
  jsonschema-go transitive dep supports `maxLength` enforcement
  end-to-end.

### F9 — Unknown method returns `code: 0` not the JSON-RPC 2.0 Method-Not-Found code

- **Severity:** low
- **Reproducer:**

  ```text
  >> {"jsonrpc":"2.0","id":2,"method":"no/such/method","params":{}}
  << {"jsonrpc":"2.0","id":2,"error":{"code":0,"message":"JSON RPC not handled: \"no/such/method\" unsupported"}}
  ```

  JSON-RPC 2.0 defines `-32601` for Method-Not-Found. The SDK
  returns `code: 0` here.
- **Observed:** Strict JSON-RPC clients that switch on `error.code`
  treat `0` as an unspecified error. Doesn't matter for the MCP
  go-sdk client, but a third-party MCP-compatible client (e.g.
  Inspect's Python `mcp` library) might log "unknown error code:
  0" rather than "method not found."
- **Expected:** `error.code: -32601` for unknown methods.
- **Suggested fix shape:** Upstream — the MCP go-sdk
  (`jsonrpc2.Handler` plumbing) emits this code; not a Leonard fix.
  Worth filing upstream and pinning the version when the upstream
  fix lands.
- **Out of scope for this investigation:** Any other JSON-RPC
  error-code drift from spec.

### F10 — Tool-not-found surfaces as a transport-level error, not `IsError=true`

- **Severity:** low
- **Reproducer:**

  ```text
  $ ./harness missing-tool
  err=calling "tools/call": unknown tool "verify_symbol_typo"
  ```

  No `CallToolResult.IsError` because the SDK never returns a
  result — the error reaches the client at the transport layer
  (`*mcp.ClientSession.CallTool` returns `(*Result, error)`; this
  case populates the second return).
- **Observed:** From Claude's perspective: when Claude calls a
  mistyped tool (e.g. `record_decisin`), the tool call fails with
  a transport error rather than a structured IsError text response.
  Most MCP clients map this to the model differently than they
  would `IsError=true`.
- **Expected:** Per the MCP spec, tool errors *and* missing-tool
  errors should both surface in the same structured shape so the
  model gets a uniform "this didn't work and here's why" signal.
- **Suggested fix shape:** Likely an upstream-go-sdk issue; the
  spec allows either form. Worth documenting that mistyped tool
  names show up differently from in-handler errors.
- **Out of scope for this investigation:** Whether to ship a
  custom `tools/call` dispatcher that catches missing tools and
  emits an IsError response.

### F11 — Stale-decisions surfaces foreign / synthetic file paths forever; no cleanup path

- **Severity:** low
- **Reproducer:** After F7, every subsequent `get_stale_decisions`
  call returns the foreign-path decision. There's no
  `cleanup_stale_decisions` or `delete_decision` tool. The
  decision must be `supersede_decision`'d, but that just creates a
  new row; the old row's `related_files` are still listed as
  stale.
- **Observed:** A model with sloppy `related_files` hygiene
  accumulates stale-list noise that can't be pruned without
  direct sqlite3 access.
- **Expected:** Either (a) `supersede_decision` should also clear
  the stale-cache for the prior row, or (b) a dedicated delete /
  archive tool. Today the table just grows.
- **Suggested fix shape:** Filter superseded decisions out of
  `GetStaleDecisions` at the SQL layer (`WHERE superseded_by IS
  NULL` already partially handled in the v3 schema; verify).
  Alternative: add a TTL on related_files entries that have been
  missing for N days.
- **Out of scope for this investigation:** Whether decisions
  themselves should ever be deletable (intent of the ledger is
  append-only — that's defensible).

### F12 — Post-edit `exit 2` on payload decode failure is correct but the comment is misleading

- **Severity:** informational
- **Reproducer:** `cmd/leonard-hook/exit.go:23-26`:

  > "The pre-edit and post-edit guards must use exit 2 for decode
  > failures — otherwise a malformed payload would let the would-be-
  > fabricated edit through."

  For PreToolUse: correct (exit 2 blocks the would-be tool call,
  pre-execution). For PostToolUse: misleading — the edit has
  *already* happened by the time PostToolUse fires. Exit 2 here is
  still the right choice (Claude Code's docs say exit 2 from
  PostToolUse pipes stderr to the model, useful to nudge the
  model when the hook can't process), but the framing as
  "otherwise a fabricated edit gets through" doesn't apply.
- **Observed:** Comment overstates the safety story. Behavior is
  correct.
- **Expected:** Comment should distinguish the two: "pre-edit
  blocks the call (exit 2 = block); post-edit surfaces the decode
  failure to the model so it doesn't proceed under the assumption
  the edit was indexed."
- **Suggested fix shape:** One-paragraph comment update.
- **Out of scope for this investigation:** None.

### F13 — Oversize-line spam pegs CPU even when no client is listening

- **Severity:** medium (resource exhaustion)
- **Reproducer:** Send one 16 MiB+ line to leonard-mcp's stdin and
  do nothing else:

  ```text
  ( python3 -c 'import sys; sys.stdout.write("x"*(16*1024*1024+10)+"\n")'; sleep 60 ) | leonard-mcp 2>/dev/null
  ```

  `top` shows leonard-mcp using ~100% CPU for the entire 60s.
- **Observed:** A single misbehaving parent process that streams
  oversize lines (or even one), then leaves the server alive,
  spins the process forever. Filed under "F1's downstream effect"
  — the F1 fix addresses both.
- **Expected:** No background work when there's nothing to do.
- **Suggested fix shape:** Same as F1.
- **Out of scope for this investigation:** None.

### F14 — Stop hook surfaces unverified claims to user only; the model can't self-audit

- **Severity:** low
- **Reproducer:** `internal/hooks/stop.go:36-52` and its comment.
  After a session where the model recorded several
  `record_claim {verified:false}` via MCP, the Stop hook emits:

  ```text
  {"continue":true,"systemMessage":"## Unverified claims (from Leonard)\n\n- vet might fail on x.go\n..."}
  ```

  `systemMessage` is user-visible; per the comment, **not visible
  to the model**. The model that recorded the claim never receives
  any acknowledgement at session end.
- **Observed:** The Stop hook's role as a self-correction surface
  is one-way. The user sees the unverified-claim list; Claude
  doesn't get it.
- **Expected:** The recent commit (`491e9c0`) intentionally moved
  this to systemMessage because `hookSpecificOutput` isn't valid
  for Stop hooks. The trade-off (user-visible vs. model-visible)
  is real, but the brief asked whether it's still right. Since
  per-edit failures already reach the model via PostToolUse's
  additionalContext, **MCP record_claim {verified:false} calls
  bypass that channel** — they were the load-bearing case for the
  Stop surfacing, and they're now invisible to the model.
- **Suggested fix shape:** One option: re-emit a SessionStart hint
  on the *next* session that picks up unresolved claims from the
  prior session, via the SessionStart `additionalContext` (which
  IS valid). Another: add `record_claim` to PostToolUse's
  additionalContext immediately on call — the model just made the
  call, so it already knows, but having it reflected in the next
  turn's context reinforces the unverified state. Neither is
  obvious; this is more of a product call than a bug.
- **Out of scope for this investigation:** Whether the Stop hook
  should `decision: "block"` on unresolved claims (would interrupt
  the session — likely too aggressive).

### F15 — `files.indexed_at` is unindexed; `recent_changes` does a full table scan

- **Severity:** informational (not a regression — pre-existing)
- **Reproducer:** `internal/store/store.go:205-211` (files schema)
  and `internal/store/recent.go:8-34` (ListFilesIndexedSince).
  No `CREATE INDEX ... ON files(indexed_at)` in any migration. The
  `recent_changes` MCP tool's `WHERE indexed_at >= ? ORDER BY
  indexed_at DESC` query scans the entire files table.
- **Observed:** For the current probe DB (62 files) the query is
  fast. For a project with 100k indexed files the cost scales
  linearly.
- **Expected:** v0.7.1 added the parent_id index on symbols; the
  same care for files.indexed_at would be cheap to add.
- **Suggested fix shape:** A `migrateV6` that adds
  `idx_files_indexed_at`. One line + tests.
- **Out of scope for this investigation:** Benchmark numbers to
  justify; the index cost is trivial.

### F16 — MCP `record_claim` accepts `verified=false` with no follow-up obligation

- **Severity:** informational
- **Reproducer:** A session that calls `record_claim
  {verified:false}` and never follows up has no enforcement
  mechanism. The Stop hook surfaces the row (F14 — to the user,
  not the model). The next session's SessionStart hook doesn't
  re-mention it.
- **Observed:** The unverified-claims ledger grows monotonically
  across sessions; only post-edit's vet=ok supersession trims it
  (F4 — but MCP-recorded claims can't be trimmed).
- **Expected:** Either a TTL or an explicit `resolve_claim` tool.
  Today the ledger is append-only with no model-side affordance
  to acknowledge.
- **Suggested fix shape:** Add a `resolve_claim {claim_id,
  resolution}` MCP tool that marks a claim verified. Bonus:
  surface unresolved claims from prior sessions in SessionStart's
  additionalContext so the model is reminded.
- **Out of scope for this investigation:** Whether the ledger
  needs to be append-only by design for forensic reasons.

## Things that worked

The platform's hardened surfaces look solid:

- **Cap rejection shape (security-1 F4/F8):** every cap is a clean
  `IsError=true` text content with a structured message that
  identifies the field and the byte limit ("`record_decision:
  topic exceeds 256 bytes`", same shape for choice, reasoning,
  claim, evidence). The server stays alive. Probed via
  `./harness caps`.

- **Concurrent cap rejection:** 50 concurrent `record_decision`
  calls (half over the choice cap, half within) all returned
  within ~1.5s — 25 ok, 25 cap-rejected, 0 transport errors.
  `./harness concurrent-caps`.

- **MCP + hook concurrency under WAL:** 5,184 MCP `find_symbol`
  calls + 154 post-edit hook subprocess runs across 8 seconds
  (4 goroutines each) — zero errors on either surface.
  `./harness mcp-hook-race`.

- **`stdin_filter` core behavior:** non-JSON-RPC lines (raw text,
  scalars, objects without `jsonrpc`, wrong-version envelopes,
  empty lines, arrays) are dropped + logged; valid envelopes pass
  through unchanged; the SDK never sees malformed input. F1's
  fix-2 promise holds outside the oversize-line case. Verified
  via the unit tests in `internal/mcp/cmd/leonard-mcp/stdin_filter_test.go`
  and the raw stdio harness.

- **SessionStart `compact` / `clear` skip:** verified that both
  emit `{"continue":true}` with no additionalContext — the v0.4-era
  skip logic survived the v0.10 eval-framework rework intact.
  (See F3 for the inverse — `startup`/`resume` still inject.)

- **Stale-decision lifecycle end-to-end:** record a decision with
  `related_files: ["subdir/staletest.go"]`, write the file, run
  `leonard index`, delete the file, run `leonard index` (which
  prunes the row in v0.6.1+v0.7.0). `get_stale_decisions`
  correctly returns the decision with `missing_files:
  ["subdir/staletest.go"]`. The v0.7.0 prune-sweep + the existing
  stale-check path play together cleanly.

- **`handleEscapedPath` model-visibility:** Continue=true, both
  systemMessage and hookSpecificOutput.additionalContext
  populated, exit 0. The Security-1 F1 fix reaches the model.

- **Pre-edit hook oversize payload rejection:** a 16 MiB+ stdin
  payload to `leonard-hook pre-edit` exits 2 with a clean
  ErrDecode wrapping: `payload decode error: hook payload
  exceeds 16777216 bytes`. The pre-edit fabrication guard's
  load-bearing safety story (security-1 F2) holds.

- **Post-edit additionalContext bound:** even with 500 fabricated
  function declarations triggering vet errors, the post-edit
  `additionalContext` stays at ~250 bytes — `vetErrorSummary`'s
  200-char cap + the standard prefix. The 32-KiB `vet.Output`
  goes into the `evidence` column (capped at
  `defaultEvidenceCap = 16 KiB`), never the model's context.
  The brief's worry about a context-budget blow-out on this
  surface is unfounded.

- **`handleMissingFile` claim persistence:** a PostToolUse on a
  missing file does record a claim row with IndexOK/VetOK as NULL
  (the tri-state was preserved through the F4 column work). The
  forensic trail exists; the model-visibility is what's broken
  (F5).

- **`MaxMultiEditElements = 100` cap:** confirmed in source
  (`internal/hooks/limits.go:31-33`); the F6 MultiEdit/NotebookEdit
  fix in v0.10 era is honored by `snippetsForTool`.

- **Decision/claim text round-trip with unicode and control bytes:**
  the bughunt-2 mcp probe's confirmation still holds — unicode
  topics, NUL-byte content, control chars all round-trip cleanly
  through both surfaces.

- **Self-check in `evals/inspect/scoring.py`:** the
  `_run_self_check` guard against silently-broken hook wiring is
  not exploitable by a malicious sample — the probe text is fixed
  Python code; Claude has no input that reaches the self-check
  call. The `_self_check_done` module-level flag is non-thread-safe
  but Inspect runs scorers sequentially per task, so the race is
  cosmetic.

## Open questions

- **F1/F13 fix strategy:** swap `bufio.Scanner` for a hand-rolled
  line reader, or pin a buf size that's so large it's effectively
  unbounded? Real MCP messages are kilobytes; 16 MiB is already
  generous. The fix is "advance past the oversize line by
  draining to the next \n" — well-known idiom but it does mean
  forking the SDK's `IOTransport` further.

- **F2 / F3:** what's the right per-row cap on `reasoning` for
  `get_decisions` output? 4 KiB? 1 KiB? Both bound the response
  but lose detail; an alternative is a `get_decision_full` tool
  that fetches one row at a time. Product call.

- **F4:** does adding `file_path` to `record_claim` complicate
  the model's prompt? The model has to know which file to scope
  the claim to — the current "unscoped claim" semantics may be
  intentional. If so, an alternative is a `resolve_claim` tool
  with no file_path required. Either way, the lifecycle needs an
  out for MCP-recorded claims.

- **F7 / F11:** should `related_files` be validated with
  `index.ResolveSafe` (matching the v0.8 path-trust sweep
  elsewhere), or accepted as opaque strings and trusted to be
  project-relative? The latter is more permissive (e.g. a
  cross-repo decision can reference paths in a sister repo) but
  invites the foreign-path noise. Path-trust-deep lane raised
  similar questions for prune.

- **F14:** the recent Stop-hook commit (`491e9c0`) was correct
  per the Claude Code schema validator, but the chosen channel
  (systemMessage) breaks the model-feedback loop for MCP-recorded
  claims. Is there a SessionStart-based workaround (re-surface
  prior unresolved claims at the start of the *next* session)? It
  preserves the "Stop is not interruptive" property while
  keeping the model in the loop.

- **`session_id` provenance (carry-over from rounds 1-3):**
  models calling `record_claim` via MCP don't have access to the
  Claude Code session_id. The schema accepts empty (correct). But
  this means MCP-recorded claims with empty session_id pile up in
  `get_unverified_claims("")` cross-session. Whether that's a
  feature (one ledger across all sessions) or a bug (each session
  should see only its own claims) hasn't been settled.
