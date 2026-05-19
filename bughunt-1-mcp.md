# Bug Hunt #1 — mcp

## Summary

I built `cmd/leonard-mcp` and drove a real `mcp.CommandTransport` client (same
SDK as the server, `github.com/modelcontextprotocol/go-sdk v1.6.0`) against the
already-indexed `~/Documents/Homelab/leonard/.leonard/leonard.db`, exercising all
nine v1/v2/v3 tools plus a handful of failure modes. The wire surface is mostly
sound — `tools/list` advertises every tool with `additionalProperties:false`,
required-field declarations work, and JSON-Schema validation rejects bad input
before the handler runs. But three things bite hard once a real client is
involved: **(1)** `record_claim` declares `session_id` optional in the schema
while the store rejects an empty value, so any well-behaved client that omits
`session_id` (the schema's natural reading) hits a misleading "empty session id"
error; **(2)** if the DB file is removed or replaced under a running server,
the server silently keeps reading/writing the deleted inode — Claude Code sees
"normal" responses while writes are headed to a ghost file that vanishes on
shutdown; **(3)** the `list_files.pattern` schema description claims `path.Match`
syntax, but the store uses SQLite `GLOB`, which has materially different
semantics (`*` crosses `/`, malformed patterns silently match nothing). A few
smaller issues round out the report — exit code on SIGINT, asymmetric language
filter on `find_symbol`, error-message wording on `supersede_decision`.

Scratch harness lives at `/tmp/leonard-mcp-harness/` (separate `go.mod` with a
`replace` directive pointing at this worktree). The temp probe project lives at
`/tmp/leonard-mcp-probe/`. None of that is in version control.

## Findings

### F1 — `record_claim` schema declares `session_id` optional but the store rejects empty session_id

- **Severity:** high
- **Reproducer:**

  ```jsonc
  // tools/call payload
  { "name": "record_claim",
    "arguments": { "claim": "go vet clean", "evidence": "no output", "verified": true } }
  ```

  ```text
  Result:
    isError=true
    text: record_claim: store: RecordClaim: empty session id
  ```

  The schema says `session_id` is optional (`"required": ["claim","evidence","verified"]`,
  no `session_id`); `internal/mcp/claims.go:20-26` defines it with
  `json:"session_id,omitempty"`. The handler at `internal/mcp/claims.go:54-63`
  passes whatever the caller sent through to the store, and
  `internal/store/store.go` (its `RecordClaim` path) rejects an empty session id.
- **Observed:** Every well-behaved client that constructs a payload from the
  advertised schema and omits `session_id` gets a confusing error from a layer
  several tiers below the API contract.
- **Expected:** Either (a) `session_id` is required in the schema (and the
  description is updated to say so), or (b) the handler/store accepts empty and
  records the claim under a sentinel like `""`/`"unattributed"`. The header
  comment in `claims.go:14-18` says "MCP layer plumbs it through faithfully…
  the real store rejects empty values, which surface as an MCP error result"
  — meaning today's behavior is intentional at the store layer, but the wire
  schema doesn't reflect that.
- **Suggested fix shape:** Move the required-ness up to the schema (most
  honest), or accept empty in the store. Either way, get the two layers to
  agree. The `record_claim` description's "the stop hook fills it in" wording
  is misleading too — `record_claim` is called by the *live* tool-using
  session, not by the stop hook (see also F6).
- **Out of scope for this investigation:** Whether Claude Code actually has a
  `session_id` it can pass on every call — this is the larger product question
  that motivated the wording.

### F2 — DB removed/replaced mid-session causes silent data loss

- **Severity:** high
- **Reproducer:** `/tmp/leonard-mcp-harness/probe2/main.go` — copy the indexed
  `.leonard/leonard.db` to a scratch dir, start `leonard-mcp` with the scratch
  dir as CWD, perform a normal `verify_symbol`/`list_files`/`record_decision`,
  then `os.Remove(.leonard/leonard.db + -wal + -shm)`, then perform the same
  calls again. Output:

  ```text
  Before deletion:
    verify_symbol(NewServer)  → exists=true, signature... (correct)
    list_files(go)            → file_count=66
    record_decision           → {"decision_id":2}

  [removed leonard.db + wal/shm]

  After full deletion:
    verify_symbol(NewServer)  → exists=true, same signature (READS still work via held FD)
    list_files(go)            → file_count=66
    record_decision           → {"decision_id":3}   ← WRITE acknowledged, but to ghost inode

  [wrote empty file at leonard.db]    (e.g., user re-ran `leonard init`)

  After empty file recreated:
    verify_symbol(NewServer)  → exists=true, same signature (still serving from ghost)
    list_files(go)            → file_count=66
    record_decision           → {"decision_id":4}   ← ACK, but the on-disk file is empty
  ```

- **Observed:** No client-visible error at any point. Reads keep returning the
  pre-deletion data; writes get fresh row IDs and report success. When the
  server eventually shuts down, the open FD's deleted inode is reclaimed and
  every write made after the deletion is gone. Anyone who re-init'd the DB
  in the meantime sees an empty file that the running server happily ignores.
- **Expected:** A removed/replaced DB should be detectable. At minimum, writes
  should fail and surface an MCP error so the calling session knows its claim
  /decision didn't persist. Ideally the server logs a clear message ("DB file
  vanished — restart leonard-mcp") and exits.
- **Suggested fix shape:** Probably the cheapest is a periodic `os.Stat(dbPath)`
  +/- inode comparison on a timer, or run that check inline on each write call;
  if the inode no longer matches, return a structured MCP error and tear down.
  A heavier-handed approach: open the DB in `mode=ro` plus a separate writer
  connection that re-opens before each write. Either way, the principle is
  *no silent ghost-file writes*.
- **Out of scope for this investigation:** Coordinating with the hook
  processes that also write through `store.Open` — same shape of bug there if
  the hook keeps the connection long-lived, but I didn't probe it.

### F3 — `list_files.pattern` schema says `path.Match` but implementation is SQLite `GLOB`

- **Severity:** medium
- **Reproducer:** `/tmp/leonard-mcp-harness/probe3/main.go` against the same
  indexed DB.

  ```text
  pattern="internal/*.go"
    matched=44     (SQLite GLOB: * crosses /)
    e.g. internal/config/config.go  ← path.Match would return FALSE
  pattern="*.go"
    matched=66     (all files; path.Match would return FALSE for cmd/leonard-hook/main.go)
  pattern="**/*.go"
    matched=66     (** is treated as just one *)
  pattern="["
    matched=0      (SQLite GLOB silently doesn't match;
                    path.Match would return ErrBadPattern)
  ```

  Source: `internal/mcp/handlers.go:53` advertises
  `"optional glob matched against file path (path.Match syntax)"`. The store's
  implementation (`internal/store/store.go:362-398`) builds `path GLOB ?` which
  is SQLite GLOB syntax — wildcards span path separators, malformed patterns
  silently match nothing, no `**` support beyond what `*` already does.
- **Observed:** Schema documentation misrepresents the wildcard semantics that
  Claude Code (or any other MCP client) would use. A client that constructs
  globs assuming Go `path.Match` semantics gets very different — and silently
  broader — match sets than they'd expect.
- **Expected:** Pick one and tell the truth. SQLite GLOB is plenty useful;
  the description just needs to say so.
- **Suggested fix shape:** Update the `jsonschema` description on
  `ListFilesInput.Pattern` to match the implementation, e.g.
  `"optional glob matched against file path (SQLite GLOB syntax: * matches any
  sequence including / ; ? matches one character; [abc] character classes)"`.
  Optionally validate the pattern at the handler before hitting SQLite and
  return a structured error on bad glob.
- **Out of scope for this investigation:** Whether the *right* call is to
  switch the store to `path.Match`-style filtering done in Go after the query
  — that's a design decision, not a schema-doc bug.

### F4 — SIGINT clean shutdown exits with code 1

- **Severity:** medium
- **Reproducer:** Start the binary, send SIGINT, observe exit code:

  ```text
  --- SIGINT clean shutdown ---
    started pid=5463, sending SIGINT
  leonard-mcp: context canceled        ← stderr from main.go:25
    exited within 3s, err=exit status 1
  ```

  In `cmd/leonard-mcp/main.go:30-54`, `signal.NotifyContext` cancels `ctx` on
  SIGINT/SIGTERM. `srv.Run` returns whatever the SDK gives it on cancellation
  — which is the standard `context.Canceled` error. `run()` then returns that
  error, and `main()` prints it and exits 1.
- **Observed:** A clean signal-initiated shutdown looks like a crash to any
  process supervisor / Claude Code wrapper that judges by exit code.
- **Expected:** SIGINT / SIGTERM are intentional shutdown signals; exit 0 is
  the conventional response. (`defer st.Close()` does fire — that part of the
  brief's question is "yes" — but the user-visible exit story is wrong.)
- **Suggested fix shape:** In `run()`, special-case `errors.Is(err,
  context.Canceled)` and return nil when the ctx that was cancelled was the
  signal one. Or in main(), check the error and translate canceled→exit 0.
- **Out of scope for this investigation:** Whether `srv.Run` actually returns
  `context.Canceled` reliably across SDK versions — the test relies on the
  current v1.6.0 behavior.

### F5 — `find_symbol` lacks a `language` filter despite `verify_symbol` having one

- **Severity:** medium
- **Reproducer:** Compare tool schemas:

  ```text
  verify_symbol.inputSchema.properties: name, kind, language
  find_symbol.inputSchema.properties:   query, kind, limit       ← no language
  ```

  In `internal/mcp/handlers.go:38-42`, `FindSymbolInput` has no `Language`
  field, and `server.go:108` hardcodes `""` when calling `filterAndConvert` for
  the find path. Whereas `verifySymbol` (`server.go:99`) does pass
  `in.Language` through.
- **Observed:** A caller who wants to "find all `Server` symbols, Go only"
  cannot — they must call `find_symbol{query:"Server"}` and post-filter
  client-side, or fall back to `list_files{language:"go"}` and walk files
  themselves.
- **Expected:** Symmetric filter surface across the two read tools. The brief
  and DESIGN.md both treat them as siblings.
- **Suggested fix shape:** Add `Language string` to `FindSymbolInput` and
  forward it into the existing `filterAndConvert` call. One-line change.
- **Out of scope for this investigation:** Whether `find_symbol` should also
  expose a `language` filter on the *store* side (i.e., push down into SQL)
  for performance — the existing `verify_symbol` does it client-side via
  `languageFromPath`, which is fine at v1 scale.

### F6 — Misleading description on `record_claim.session_id`

- **Severity:** low
- **Reproducer:** `tools/list` for `record_claim`:

  ```json
  "session_id": {
    "description": "opaque Claude Code session identifier; the stop hook fills it in",
    "type": "string"
  }
  ```

  The actual flow: `record_claim` is invoked by the *live* Claude Code session
  via MCP. The stop hook (a separate process — `cmd/leonard-hook/stop.go`)
  doesn't call `record_claim`; it reads claims via the ledger. So "the stop
  hook fills it in" is at best confusing, at worst wrong.
- **Observed:** Misleading documentation visible to every MCP client that
  introspects the schema.
- **Expected:** Describe the field as "the calling session's id, supplied by
  Claude Code at tool-call time" or similar. If the field is *meant* to be
  back-filled later, F1 already shows that's not how the store treats it.
- **Suggested fix shape:** Reword the jsonschema description; possibly
  cross-reference the stop hook's role as a *consumer* (`get_unverified_claims`)
  rather than a producer.
- **Out of scope for this investigation:** The conceptual question of who owns
  `session_id` — Claude Code, the hook layer, or Leonard.

### F7 — `record_claim` schema requires `evidence` and `verified` even though both can be sensibly empty/defaulted

- **Severity:** low
- **Reproducer:** `tools/list` shows `record_claim.inputSchema.required =
  ["claim","evidence","verified"]`. The handler (`internal/mcp/claims.go:54-63`)
  only rejects `in.Claim == ""`. So `evidence=""` is accepted by the handler
  but the schema forces the client to send the field anyway; and `verified` is
  a Go `bool` whose JSON-omit default would naturally be `false` (the
  "flag for follow-up" path described in the field's own description).
- **Observed:** Strict required list forces extra ceremony in every client
  call site. Not broken — just noisier than necessary.
- **Expected:** `evidence` could plausibly be optional (the brief frames it as
  "supporting evidence — can be longer than the claim itself" which reads as
  optional). `verified` is a judgment call — making it required forces the
  caller to be explicit, which has a defensible case ("don't accidentally
  drop unverified claims"). Less clear-cut than `evidence`.
- **Suggested fix shape:** Drop `omitempty` from `Evidence` if you want to
  keep it required as documented, OR add `omitempty` and remove it from the
  schema-required list. Either way, get the JSON tag and the required list
  aligned with the handler's actual validation.
- **Out of scope for this investigation:** None — clear surface fix.

### F8 — `supersede_decision` reports `"decision_id is required"` for negative IDs, not just zero/missing

- **Severity:** low
- **Reproducer:**

  ```text
  payload: {"decision_id": -1, "new_choice": "x", "new_reasoning": "y"}
  →  isError=true
     text: supersede_decision: decision_id is required
  ```

  Handler at `internal/mcp/decisions.go:105`: `if in.DecisionID <= 0 { return …
  errors.New("supersede_decision: decision_id is required") }`. The schema
  doesn't constrain `decision_id` to be `>= 1`, so the JSON validator lets a
  negative through to the handler.
- **Observed:** The error message says the id is "required", which is false —
  the caller did supply it; it's just invalid.
- **Expected:** Distinct messages: `"decision_id is required"` for absent,
  `"decision_id must be positive"` for `<= 0`. Or constrain the schema with
  `"minimum": 1` and let validation catch it before the handler.
- **Suggested fix shape:** Either schema-side `minimum: 1` (via a
  `jsonschema:"minimum=1"` tag — check what `jsonschema-go` accepts) or split
  the handler's branch with a clearer message for the `< 0` case.
- **Out of scope for this investigation:** None.

### F9 — Bad glob (`[`) silently returns empty list, no error

- **Severity:** low
- **Reproducer:** `list_files { "pattern": "[" }` returns `{"files":[]}`.
  Same shape as a well-formed pattern that simply has no matches. SQLite
  GLOB's malformed-pattern semantics: never match. The handler doesn't
  validate the pattern before passing it down. A user who typo'd a pattern
  has no way to distinguish "no files matched" from "your pattern is broken".
- **Observed:** Silent. No `isError`, no diagnostic.
- **Expected:** Either validate the pattern at the handler (return a structured
  error) or document that malformed patterns simply yield zero results.
- **Suggested fix shape:** Pre-validate with a quick `path.Match("dummy",
  pattern)` and surface the `ErrBadPattern` if it returns — caveat that this
  only catches `path.Match`-syntax breakage; SQLite-GLOB-specific malformations
  (like `[` is malformed in both, so this works for the common case) would
  pass. Or do an empty-set GLOB check via a tiny `SELECT 1 WHERE 'x' GLOB ?`
  guard query before the main query.
- **Out of scope for this investigation:** This finding is partly subsumed by
  F3 (sort out the glob semantics first).

### F10 — Negative `limit` / `since` silently accepted

- **Severity:** low
- **Reproducer:**

  ```text
  find_symbol { "query": "S", "limit": -1 }            → returns all matches
  recent_changes { "limit": -5 }                       → returns 50 (default)
  recent_changes { "since": -1000 }                    → returns full list
  ```

  `find_symbol`'s `filterAndConvert` cuts off at `limit > 0 && len(out) >=
  limit` (so a negative limit acts like unlimited). `recent_changes`'s handler
  does `if limit <= 0 { limit = defaultLimit }` (so it gets the default, fine)
  but the schema is `"type": "integer"` with no `minimum`, so the validator
  accepts arbitrary negatives.
- **Observed:** No client-side error, but the behavior diverges from the
  docstring's "0 = unlimited" / "default 50, capped at 500" wording.
- **Expected:** Either reject negatives at the schema layer (`minimum: 0`) or
  normalize them in the handler with a comment that this is intentional. Same
  for `since` — a unix-seconds lower bound being negative is nonsensical.
- **Suggested fix shape:** Schema-side `minimum: 0` on all numeric inputs
  where 0 is the documented "no constraint" value.
- **Out of scope for this investigation:** None.

### F11 — `find_symbol { "query": "" }` matches everything

- **Severity:** informational
- **Reproducer:** Empty-string query returns 200+ matches (capped only by
  default store limit). The schema says
  `"substring matched against symbol name and qualified name
  (case-insensitive)"`, which is technically true: every string contains the
  empty string as a substring. Could be intentional ("zero arg = browse all")
  or accidental.
- **Observed:** Effectively a "list all symbols" shortcut.
- **Expected:** Probably document it explicitly ("empty query lists all
  symbols, subject to limit") or reject the empty string and force callers
  to use `list_files` for browsing.
- **Suggested fix shape:** One line of doc, or a one-line guard in
  `findSymbol`.
- **Out of scope for this investigation:** Whether browsing is a use case the
  product wants to support via find_symbol vs. a separate tool.

### F12 — Corrupted DB at startup causes client-visible `EOF` rather than a structured MCP error

- **Severity:** informational
- **Reproducer:** Overwrite `.leonard/leonard.db` with random bytes, start the
  server, connect a client. The server prints
  `leonard-mcp: open store: store: ping sqlite: file is not a database (26)`
  to stderr and exits with code 1 — before the MCP handshake completes. The
  client's `Connect()` returns `calling "initialize": EOF`.
- **Observed:** The diagnostic is in stderr, but the client only sees EOF.
- **Expected:** Acceptable for v0 — stderr is the canonical place for startup
  failure messages and MCP doesn't really specify a "server is broken at
  startup" response. Just worth knowing for client-side error UX.
- **Suggested fix shape:** None required; perhaps a CLAUDE.md / README note
  about troubleshooting EOF-on-connect (= check stderr).
- **Out of scope for this investigation:** None.

### F13 — `**` in pattern silently behaves the same as `*` (no recursive-glob support)

- **Severity:** informational
- **Reproducer:** `list_files { "pattern": "**/*.go" }` returns the same 66
  files as `list_files { "pattern": "*.go" }`. SQLite GLOB doesn't have `**`
  semantics; the duplicate stars are collapsed.
- **Observed:** A user with bash/gitignore muscle memory who writes `**/...`
  gets a result that *looks* right (because `*` already crosses `/` in SQLite
  GLOB) but for the wrong reason — they're not getting recursive behavior,
  they're getting "match anything anywhere".
- **Expected:** Tied to F3 — once the glob syntax is documented honestly,
  this collapses into the same fix.
- **Suggested fix shape:** Doc-only.
- **Out of scope for this investigation:** None.

### F14 — No flag/env override for the DB path

- **Severity:** informational
- **Reproducer:** `cmd/leonard-mcp/main.go:34-41` hardcodes
  `filepath.Join(cwd, ".leonard", "leonard.db")`. There's no `--db` flag, no
  `LEONARD_DB` env, no override. Running `leonard-mcp` from any directory
  other than a Leonard project root fails immediately. Claude Code MCP
  configs typically set CWD when spawning the server, so this is generally
  fine, but it's a brittle coupling worth noting.
- **Observed:** Hard error if CWD isn't right; clear message ("run `leonard
  init` first").
- **Expected:** Probably fine for v0. A `--db` flag would help debugging /
  testing in CI without spinning up a real project.
- **Suggested fix shape:** Add a cobra-style flag if/when stdio testing in
  CI gets implemented.
- **Out of scope for this investigation:** None.

### F15 — Stdio transport never tested in CI (the brief's own framing)

- **Severity:** informational
- **Reproducer:** `grep -r "StdioTransport" internal/` returns nothing in
  tests; `grep -r "NewInMemoryTransports" internal/` is everywhere. The
  binary's only end-to-end coverage was the manual smoke test that produced
  the very `.leonard/leonard.db` I used for this audit.
- **Observed:** Every issue in F1–F4 specifically would have been caught by a
  single integration test that spawns the binary and drives it over the real
  stdio transport.
- **Expected:** At least one test in `internal/mcp/` (or a new
  `internal/mcp/stdio_test.go`) that uses `mcp.CommandTransport` against a
  built `leonard-mcp` binary against a fixture DB. The harness I wrote is
  essentially that test, modulo packaging.
- **Suggested fix shape:** `go build` the binary in `TestMain` (or use
  `testdata/leonard-mcp` checked-in), spawn via `mcp.CommandTransport`,
  exercise tools/list + at least one call per tool, plus the four invalid-input
  shapes (missing-required, unknown-property, wrong-type, null).
- **Out of scope for this investigation:** This is its own future PR, not a
  fix for the existing code.

## Things that worked

- **All nine tools** were advertised correctly in `tools/list` with
  well-formed JSON-Schema input/output specs (including `additionalProperties:
  false` everywhere). The opt-in registration pattern for the v2/v3 tool sets
  (`registerDecisionTools`, `registerClaimTools`, `registerChangesTool` only
  wire when the store implements the matching interface) works as designed —
  the real `StoreAdapter` satisfies all three, so the live server exposes all
  nine tools.
- **Schema validation via `additionalProperties:false`** correctly rejects
  unknown fields at the JSON-Schema layer before the handler runs:
  `verify_symbol {"name":"NewServer","bogus":42}` →
  `"unexpected additional properties [\"bogus\"]"`. Same for wrong types
  (`name: 123` → `"type: 123 has type \"integer\", want \"string\""`),
  for `null` on a required field, and for missing required fields. The
  handlers don't have to re-do this.
- **Tool-name validation:** Calling `tools/call` with an unknown name or `""`
  returns a proper JSON-RPC error (`unknown tool "no_such_tool_xyz"`) — the
  SDK handles this without forwarding to a handler.
- **Happy-path output structure matches the advertised schema** for every
  tool. `StructuredContent` is present and well-formed; the human-readable
  text payload is also valid JSON of the same shape (good for clients that
  introspect either path).
- **`verify_symbol` filters** (`kind`, `language`) both apply correctly when
  set; empty filters are no-ops.
- **`list_files` filters** (`language=go`, `pattern=...`) both narrow as
  expected (caveats in F3 / F9 / F13 about pattern semantics aside).
- **`record_decision` → `get_decisions` → `supersede_decision`** round-trip
  works: decision is recorded, retrievable by topic, supersedable by id, and
  the new id comes back from supersede.
- **`record_claim` (with non-empty session_id) → `get_unverified_claims`**
  round-trip works: unverified claims are filterable by `session_id` and the
  filter correctly returns empty for unknown sessions.
- **`recent_changes`** returns the expected file list with `indexed_at`
  populated, since-filter works (`since=99999999999` → empty), and the limit
  cap (`limit=99999` → still bounded at 500 per the docstring) holds.
- **Startup error paths:** DB-missing prints a clear "run `leonard init`
  first" message; DB-unreadable (`chmod 000`) prints
  `"unable to open database file (14)"` and exits 1. Both fail closed, not
  open.
- **Clean shutdown on EOF:** Closing stdin causes the server to exit within
  3s. The deferred `st.Close()` in `main.go:47` fires.
- **Concurrent writers from two `leonard-mcp` processes** (25 alternating
  `record_decision` calls each, both rooted on the same DB) all succeeded.
  SQLite WAL + `busy_timeout(5000)` in `internal/store/store.go:108` is doing
  its job for moderate contention. (Real concurrency under hook storm is the
  selfhost lane's territory, not mine.)

## Open questions

- **DB-deletion detection strategy (F2):** Is per-call inode/mtime checking
  cheap enough to do every call, or should it be a goroutine? Tied to the
  question of whether the hook subprocesses (which also call `store.Open`)
  have the same exposure.
- **Should `session_id` be required or back-filled?** F1 and F6 both point
  at the same underlying question that needs a product decision: where does
  `session_id` come from and at what layer is it required? The current code
  is "ambiguous in the middle" — the schema is permissive, the store is
  strict, and the description blames a hook that isn't involved.
- **Glob syntax:** Is the intent that `list_files.pattern` should match
  `path.Match` (Go) or `GLOB` (SQLite)? Today the store does GLOB, the doc
  says path.Match, and the brief doesn't take a side. Whichever way the fix
  round chooses, the *other* layer needs to move.
- **SIGINT exit code policy:** Is exit 0 on signal the right convention given
  the broader Leonard CLI's exit-code expectations elsewhere? I assumed so
  but didn't verify against `cmd/leonard` or `cmd/leonard-hook`.
- **Stdio CI coverage:** F15 is informational, but the real follow-up is
  "what's the smallest stable integration test we can carry?" — the harness
  I wrote (~150 LOC plus a /tmp/ probe DB) is a starting point but probably
  not the right artifact to check in; a `testdata/leonard-mcp.sh`-driven
  table test or similar would be lighter.
