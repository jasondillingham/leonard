# Bug Hunt #2 — mcp

## Summary

I rebuilt `cmd/leonard-mcp` (binary at `~/go/bin/leonard-mcp`), drove the
binary over `mcp.CommandTransport` from a real `github.com/modelcontextprotocol/go-sdk`
client, and re-ran the Round 1 surface plus the new bits introduced by mcp F3,
F4, F5 and the DB-watcher work that followed. The Round 1 fixes hold up well:
SIGINT/SIGTERM now exit 0 cleanly (F4), `list_files.pattern` advertises SQLite
GLOB semantics (F3), `find_symbol` carries a `language` filter (F5), DB
deletion / inode-swap is detected lazily on every call **and** by a 2s ticker
that tears the server down (R1 F2), and `record_claim` happily accepts an empty
`session_id` (R1 F1). Concurrency, FD count, long-running sessions and the
indexed-DB read paths all look solid.

But the surface still has teeth. The two findings that bite hardest in this
round are:

1. **A single malformed JSON-RPC line on stdio kills the server with exit 1 and
   no JSON-RPC error response.** Anything that emits to the wrong file
   descriptor in the parent (a stray `console.log`, a forked subprocess writing
   to inherited stdout, a corrupted pipe) crashes leonard-mcp instantly.
2. **`find_symbol` + language/kind filter undercounts.** The store applies
   `LIMIT N` *before* the MCP layer applies kind/language filters, so asking
   for `limit=5, language=python` can return 0 rows even when matching Python
   rows exist further down the result set. A caller has no way to know more
   results exist beyond the cap.

The rest are doc-versus-implementation mismatches that survived Round 1 or
appeared in the F5 change, plus a few small validation oddities (`<invalid
reflect.Value>` leaking into error messages, empty `choice`/`reasoning`/
`evidence` slipping past required-field schemas, the "0 = unlimited" claim on
`find_symbol.limit` that the implementation maps to 50).

Scratch harness lives at `/tmp/leonard-mcp-r2/harness/` (`go.mod` with a
`replace` directive pointing at this worktree). The probe project (copy of the
real `.leonard/leonard.db`) lives at `/tmp/leonard-mcp-r2/probe/`. Neither is
in version control.

## Findings

### F1 — Malformed JSON-RPC on stdio crashes the server with no error response

- **Severity:** high
- **Reproducer:** spawn `leonard-mcp` over stdio, complete the handshake, then
  write any line that doesn't unmarshal as a JSON-RPC request:

  ```text
  >> {"jsonrpc":"2.0","id":1,"method":"initialize",...}
  << {... handshake ok ...}
  >> not valid json
  !! leonard-mcp: invalid character 'o' in literal null (expecting 'u')
  [exit] code=1
  ```

  Every flavor I tried produces the same outcome (`/tmp/leonard-mcp-r2/harness/raw3.go`):

  | input | server response | exit |
  |-------|------------------|------|
  | `not valid json` | nothing on stdout, error on stderr | 1 |
  | `42` (valid JSON, wrong shape) | nothing on stdout, error on stderr | 1 |
  | `{}` (no `jsonrpc` field) | nothing on stdout, error on stderr | 1 |
  | `{"id":99,"method":"tools/list"}` (no `jsonrpc` field) | nothing on stdout, error on stderr | 1 |
  | `{"jsonrpc":"2.0","id":99,"method":"no/such/method"}` | JSON-RPC error frame, server stays alive | 0 (clean EOF later) |
  | `{"jsonrpc":"2.0","id":99,"method":"tools/list","params":{},"extra_field":"x"}` | success | 0 |
  | notification with unknown method | absorbed silently | 0 |

  So the SDK's transport reader gives up on the first line it can't decode as
  a JSON-RPC envelope, and `cmd/leonard-mcp/main.go` prints the err to stderr
  and exits 1. No clean recovery, no error frame to the client.
- **Observed:** A leading newline, a stray subprocess `print()` that landed on
  stdout, a corrupted byte from a misbehaving terminal multiplexer — any of
  these terminates the server. From the client side, the in-flight call (if
  any) sees a transport error rather than a structured tool error; subsequent
  calls fail with EOF / "broken pipe".
- **Expected:** The JSON-RPC 2.0 spec says a server receiving a non-conforming
  message must respond with a `Parse error` (-32700) or `Invalid Request`
  (-32600) frame and continue serving. The Go SDK's `jsonrpc2` package treats
  malformed input as fatal to the run loop — Leonard inherits that.
- **Suggested fix shape:** The transport read loop needs to swallow per-line
  parse errors instead of returning them. If the SDK doesn't expose a knob,
  wrap `mcp.StdioTransport{}` in a custom transport that filters non-JSON
  lines (or any line that fails the `jsonrpc2.wireCombined` decode) and emits
  a Parse-error frame instead. Alternatively, accept the SDK's behavior and
  document that leonard-mcp expects strict stdio cleanliness (no shared FDs,
  no inherited writers) — but that's a much weaker guarantee.
- **Out of scope for this investigation:** Whether the SDK upstream considers
  this a bug; opened-issue territory rather than Leonard-side fix.

### F2 — `find_symbol` undercounts when combined with `language` or `kind` filter

- **Severity:** medium
- **Reproducer:** the symbol corpus has many Go matches for `query="a"` and
  exactly one Python match. Asking the store for limit=N returns the first N
  *unfiltered* matches; the MCP layer then applies language/kind filters,
  shrinking the response below N — even when the underlying corpus has more
  matching rows beyond the cap.

  ```text
  Q=a limit=1   lang=python →  0 matches
  Q=a limit=5   lang=python →  0 matches
  Q=a limit=10  lang=python →  0 matches
  Q=a limit=100 lang=python →  1 matches   ← the row exists, it was outside the LIMIT

  Q=a limit=1   kind=function →  0 matches
  Q=a limit=5   kind=function →  2 matches
  Q=a limit=10  kind=function →  5 matches
  Q=a limit=100 kind=function → 18 matches
  ```

  Source: `internal/store/store.go:432-450` runs `LIMIT ?` on the
  `name LIKE ?` query and returns at most that many rows. The MCP layer
  (`internal/mcp/server.go:103-109` → `filterAndConvert` at lines 131-152)
  then drops rows whose language or kind doesn't match. Since `languageFromPath`
  is purely a filename-extension lookup, the store can't push the filter down
  the way it does for `list_files`.
- **Observed:** A caller who wants "the first 5 Python symbols containing
  Server" can get 0 results back even though Python symbols exist in the DB.
  No structured signal that more matches were skipped — the response shape is
  indistinguishable from "no Python matches at all".
- **Expected:** The advertised `limit` is the size of the returned result set
  after all filters. Either:
  - Push language/kind into the store SQL (would need a language column on
    `symbols`, which currently has only `file_path` to derive from — adding it
    is a schema migration), or
  - Iterate the store cursor in the MCP layer and stop only when the
    *post-filter* count reaches `limit` (open-ended fetch with chunked
    pagination on the store side), or
  - Document the cap as "max raw symbols inspected, not max matches returned"
    and give a clear way to page through.
- **Suggested fix shape:** Add a language column to `store.Symbol`. The phase-1
  schema already has `file_path`, so the indexer can populate language at
  index time from the path extension. Then `FindSymbolsByQuery` can take a
  language filter and apply it inside the SQL `WHERE`. `kind` is already a
  column on `symbols`, so the same fix covers both.
- **Out of scope for this investigation:** Whether the indexer's existing
  `Symbol` struct already has the data ready to populate — a quick read of
  `internal/store/store.go:265+` suggests it doesn't, but the schema column
  add is straightforward.

### F3 — `find_symbol.limit` schema says "0 = unlimited" but the store caps at 50

- **Severity:** low
- **Reproducer:**

  ```text
  find_symbol { "query": "a", "limit": -1 } →  50 matches
  find_symbol { "query": "a", "limit":  0 } →  50 matches
  find_symbol { "query": "a", "limit":  1 } →   1 matches
  ```

  `internal/mcp/handlers.go:42` documents `limit` as `"maximum number of
  matches to return (0 = unlimited)"`. `internal/store/store.go:434-437`
  treats `limit <= 0` as `50` (the store's hard default), and the MCP
  `filterAndConvert` post-filter respects whatever the store returned. So
  there is no path that produces "unlimited" — it caps at 50 silently.
- **Observed:** A model that reads the schema and sends `limit:0` to mean
  "give me everything" gets exactly 50 rows. Indistinguishable from "there
  are only 50 matches".
- **Expected:** Either honor the documented contract (treat `limit <= 0` as
  unbounded, with an MCP-layer cap of, say, 1000 to protect the wire) or
  update the description to say `"maximum number of matches to return (default
  50, no override for unlimited)"`.
- **Suggested fix shape:** Update the doc to match the implementation; that's
  the cheap one. If the intent is to expose unlimited, change the store to
  treat `limit == 0` as "no LIMIT" and add a separate MCP-layer cap.
- **Out of scope for this investigation:** Whether unlimited is a sensible
  surface to expose at all given context-budget concerns.

### F4 — `record_decision` accepts empty `choice` and `reasoning` strings

- **Severity:** low
- **Reproducer:**

  ```text
  record_decision { "topic": "x", "choice": "",  "reasoning": ""  } → {"decision_id":13}
  record_decision { "topic": "x", "choice": "ok", "reasoning": "" } → {"decision_id":14}
  ```

  The schema (`internal/mcp/decisions.go:14-21`) marks `topic`, `choice`,
  `reasoning` as required and describes `choice` as "what was chosen (one
  short line)" and `reasoning` as "why it was chosen (a sentence or two)" —
  both clearly imply non-empty. JSON-Schema "required" only enforces presence,
  not non-emptiness; the handler at lines 93-102 validates only `topic`. The
  store (`internal/store/store.go:521-524`) likewise enforces only `topic`.
  Empty strings round-trip through `get_decisions` as is.
- **Observed:** A bug or a misbehaving model can plant decisions with no
  rationale that future sessions surface as if they were considered choices.
- **Expected:** If the schema requires the field, the handler should reject
  empty values. Alternatively, mark the fields optional and document them as
  "free-text — empty allowed but discouraged".
- **Suggested fix shape:** In `recordDecision`, after the topic check, add
  symmetric checks for `choice` and `reasoning`.
- **Out of scope for this investigation:** Whether to enforce a minimum
  length or just non-empty — a strict no-whitespace check would probably go
  too far.

### F5 — `supersede_decision` accepts empty `new_choice` and `new_reasoning`, breaking the chain

- **Severity:** low
- **Reproducer:**

  ```text
  supersede_decision { "decision_id": 1, "new_choice": "", "new_reasoning": "" }
    → {"new_decision_id": 16}

  $ sqlite3 .leonard/leonard.db "SELECT id, topic, choice, reasoning, superseded_by FROM decisions WHERE id IN (1,16);"
  1|hunt-mcp-test|A|because|16
  16|hunt-mcp-test|||
  ```

  Schema (`decisions.go:77-81`) lists `new_choice` and `new_reasoning` as
  required, but neither the handler at lines 163-172 nor the store (see
  schema migration v3 / `SupersedeDecision` body) enforces non-emptiness.
- **Observed:** Decision 1's chain now points at decision 16, which has no
  choice and no reasoning. A future session asking "what's the current take
  on hunt-mcp-test?" sees an empty decision. The supersession history is
  intact (you could walk back to row 1), but the most-recent-row read tool
  gives you nothing.
- **Expected:** Reject empty `new_choice` / `new_reasoning` with the same
  shape as the empty-topic check. Same flavor of fix as F4.
- **Suggested fix shape:** Mirror F4 in `supersedeDecision`.
- **Out of scope for this investigation:** Whether a "withdraw decision"
  tool is wanted as a sibling of supersede — that's a product question.

### F6 — `record_claim` accepts empty `evidence`

- **Severity:** low
- **Reproducer:**

  ```text
  record_claim { "claim": "x", "evidence": "", "verified": true } → {"claim_id": 268}
  ```

  Same shape as R1 F7. Schema requires `evidence` (`claims.go:19-25`); handler
  (lines 63-72) only checks `claim`; store (`store.go:771-790`) doesn't check
  either. The schema description says "supporting evidence — command output,
  exit codes, file paths; can be longer than the claim itself" — the
  "can be longer than the claim" phrasing implies the writer expected a
  non-empty value.
- **Observed:** Claims with empty evidence persist and surface in
  `get_unverified_claims` as a row with no audit trail.
- **Expected:** Either non-empty enforcement or schema downgrade to optional.
- **Suggested fix shape:** Same as F4/F5.
- **Out of scope for this investigation:** None.

### F7 — Unknown / mis-cased `language` filter silently returns zero matches

- **Severity:** low
- **Reproducer:**

  ```text
  find_symbol { "query": "Server", "language": "xx"     } → {"matches": []}
  find_symbol { "query": "Server", "language": "Go"     } → {"matches": []}
  find_symbol { "query": "Server", "language": "golang" } → {"matches": []}
  find_symbol { "query": "Server", "language": "go"     } → {"matches": [...one match...]}

  verify_symbol { "name": "NewServer", "language": "Go" } → {"exists": false, "matches": []}
  ```

  `internal/mcp/language.go:12-25` maps file extensions to canonical labels
  `go|python|typescript|javascript` (lowercase). The MCP filter (`server.go:137`)
  is strict-equal against the canonical label. Anything that isn't one of
  those four labels silently filters to empty.
- **Observed:** A model that reasonably guesses `"Go"` (TitleCase) or
  `"rust"` (a language Leonard hasn't shipped support for) gets a clean
  `[]` with no diagnostic. Hard to distinguish "no symbols here" from "your
  filter is a typo".
- **Expected:** Either:
  - Document the canonical labels in the schema (e.g. enum-style: `"one of:
    go, python, typescript, javascript"`),
  - Accept and normalize variants (`go`/`Go`/`golang` → `go`), or
  - Return a structured error when `language` doesn't normalize to a known
    label.
- **Suggested fix shape:** Add the four labels as a JSON-Schema `enum` on the
  language fields in `VerifySymbolInput`, `FindSymbolInput`, and
  `ListFilesInput`. The validator will reject typos at the wire layer; no
  handler change needed.
- **Out of scope for this investigation:** Adding a `language` enum constant
  set elsewhere in the codebase (it would have to live somewhere the schema
  generator can see); minor refactor.

### F8 — Error messages leak `<invalid reflect.Value>` for null-valued required fields

- **Severity:** low
- **Reproducer:**

  ```text
  verify_symbol { "name": null }
   → "validating \"arguments\": validating root: validating /properties/name:
      type: <invalid reflect.Value> has type \"null\", want \"string\""

  recent_changes { "since": null }
   → "validating \"arguments\": validating root: validating /properties/since:
      type: <invalid reflect.Value> has type \"null\", want \"integer\""
  ```

  The validator is `github.com/google/jsonschema-go` (transitive via
  `go-sdk`); it formats `nil` reflect values as `<invalid reflect.Value>`
  in its error string. Leonard surfaces this verbatim to the caller as
  the MCP tool error.
- **Observed:** The text is ugly and exposes Go-internal reflect machinery
  to a model. Not actually wrong — it does explain what happened — but
  it's not the kind of message you want a model regurgitating to the user.
- **Expected:** Either patch upstream's formatter (out of scope) or
  intercept the error in the MCP layer and rewrite the
  `<invalid reflect.Value>` substring to `null`. A small string-replace
  shim would do it.
- **Suggested fix shape:** Wrap the SDK's error or pre-validate explicit
  null-for-required cases in a custom hook if the SDK exposes one. Cheapest:
  a `strings.ReplaceAll(err.Error(), "<invalid reflect.Value>", "null")` at
  the boundary before returning to the client.
- **Out of scope for this investigation:** None.

### F9 — `verify_symbol { name: "" }` is accepted; returns `exists: false`

- **Severity:** low
- **Reproducer:**

  ```text
  verify_symbol { "name": "" } → {"exists": false, "matches": []}
  ```

  Schema requires `name` (so absent is rejected) but doesn't constrain it to
  non-empty. The handler (`server.go:94-101`) passes through to
  `FindSymbolsByName` which returns nothing for the empty string. So the
  caller gets a "no, doesn't exist" answer to a question they didn't really
  ask.
- **Observed:** Indistinguishable from a real "this name isn't in the index"
  response.
- **Expected:** `name=""` should be a validation error, not a silent
  no-results. Schema description says "the symbol name to look up (exact
  match)" — empty has no business there.
- **Suggested fix shape:** Add a `minLength: 1` constraint on `name` in
  `VerifySymbolInput`, or check at the handler for `in.Name == ""`. Same
  shape problem for `find_symbol.query` (R1 F11) — an empty substring
  matches everything; tied to the same fix.
- **Out of scope for this investigation:** None.

### F10 — `supersede_decision` error message for negative `decision_id` still misleading

- **Severity:** low
- **Reproducer:**

  ```text
  supersede_decision { "decision_id": -1, "new_choice": "x", "new_reasoning": "y" }
    → "supersede_decision: decision_id is required"

  supersede_decision { "decision_id":  0, "new_choice": "x", "new_reasoning": "y" }
    → "supersede_decision: decision_id is required"
  ```

  Carry-over from R1 F8 — the handler at `decisions.go:163-165` says `if
  in.DecisionID <= 0`, but the error string says "required". For `-1` the
  caller did supply an id; it's just invalid.
- **Observed:** Confusing error message.
- **Expected:** Schema-side `minimum: 1` would push this into validator
  territory and the message would be "/properties/decision_id: ... want a
  number >= 1" — clearer. Or split the handler branch:
  - `decision_id == 0` (missing) → `"decision_id is required"`
  - `decision_id < 0` (invalid)  → `"decision_id must be positive"`
- **Suggested fix shape:** Either approach.
- **Out of scope for this investigation:** None.

### F11 — Error-message prefix inconsistency: symbol tools don't prefix, decision/claim/changes tools do

- **Severity:** informational
- **Reproducer:** After the DB is removed mid-session, every adapter method
  returns `ErrDatabaseReplaced` (a JSON sentinel). The decision/claim/changes
  handlers wrap it with the tool name; the symbol handlers don't.

  ```text
  find_symbol     → {"code":"database-replaced","message":"..."}
  list_files      → {"code":"database-replaced","message":"..."}
  verify_symbol   → {"code":"database-replaced","message":"..."}

  record_decision → record_decision: {"code":"database-replaced","message":"..."}
  record_claim    → record_claim: {"code":"database-replaced","message":"..."}
  recent_changes  → recent_changes: {"code":"database-replaced","message":"..."}
  ```

  Source: `verifySymbol`/`findSymbol`/`listFiles` in `internal/mcp/server.go`
  return the error from the store unchanged. The decision/claim/changes
  handlers in `decisions.go`/`claims.go`/`changes.go` use
  `fmt.Errorf("<tool>: %w", err)`.
- **Observed:** Two error shapes for the same underlying cause. A client
  parsing the JSON sentinel will work for both, but log scrapers and
  human-readable surfaces will look inconsistent.
- **Expected:** Pick one style and apply it everywhere. Tool-name prefixing
  is more useful for debugging.
- **Suggested fix shape:** Add the `tool: ...` prefix in the three symbol
  handlers, or strip it from the others.
- **Out of scope for this investigation:** None.

### F12 — `verify_symbol.name` "exact match" doesn't include qualified_name lookups

- **Severity:** informational
- **Reproducer:**

  ```text
  verify_symbol { "name": "NewServer"     } → {"exists": true, ...}
  verify_symbol { "name": "mcp.NewServer" } → {"exists": false, "matches": []}
  ```

  `Store.FindSymbolsByName` (referenced from `adapter.go:85-94`) matches
  against `symbols.name` only — not `qualified_name`. The schema description
  is `"the symbol name to look up (exact match)"`, which doesn't clarify
  whether that's the bare name or the qualified name. `find_symbol` documents
  itself as searching both; `verify_symbol` is silent.
- **Observed:** A model that pastes a qualified name (which it gets back
  from `find_symbol`) into `verify_symbol` gets `exists:false` and may
  conclude the symbol disappeared.
- **Expected:** Either document that `verify_symbol.name` is the bare name
  (consistent with `Store.FindSymbolsByName`'s behavior), or accept both
  bare and qualified names. The latter is friendlier for round-tripping
  between the two tools.
- **Suggested fix shape:** Doc change at minimum:
  `"the bare symbol name (not the qualified name) to look up — use find_symbol
  for qualified-name substring searches"`. Optional: have `verify_symbol`
  fall back to qualified-name lookup if the bare-name search returns empty.
- **Out of scope for this investigation:** None.

### F13 — `list_files.pattern` description is honest about `*` but omits range classes and is wrong about `[!c]` negation

- **Severity:** informational
- **Reproducer:** From `/tmp/leonard-mcp-r2/harness/main.go::probeGlobs`:

  ```text
  pattern="internal/mcp/serv[a-z]r.go"       → matches internal/mcp/server.go (range works)
  pattern="internal/[!s]tore/store.go"        → matches internal/store/store.go (!! negation does NOT work)
  pattern="internal/[^s]tore/store.go"        → no match (^ is the SQLite negator)
  pattern="internal/store\.go"                → no match (backslash is NOT an escape in SQLite GLOB)
  ```

  Source: `internal/mcp/handlers.go:53` says
  `"SQLite GLOB: * matches any sequence incl. /, ? matches one char, [abc]
  character classes; no ** recursion, malformed patterns silently match zero
  rows"`. Two gaps:
  1. **Range classes work** — `[a-z]` matches a single character in the
     range — but the description only mentions enumerated sets.
  2. **Negation only works with `[^c]`, NOT `[!c]`** — in SQLite GLOB,
     `[!s]` is a character class containing the literal `!` and `s`. The
     description doesn't mention negation, so the lie of omission gets a
     pass; but a user who carries Unix-shell muscle memory (where `[!c]`
     does negate) gets the wrong result.
  3. **Backslash is not an escape character** — SQLite GLOB has no escape
     mechanism, period. To match a literal `[` you'd need to use a character
     class. The description omits this.
- **Observed:** A query like `list_files {"pattern": "internal/[!s]tore/store.go"}`
  returns `internal/store/store.go` — the opposite of what shell-glob
  intuition says.
- **Expected:** Add ranges (`[a-z]`) to the docs; either explicitly call out
  `[^c]` for negation (not `[!c]`), or call out that negation isn't reliably
  supported. Add a note that there's no escape syntax.
- **Suggested fix shape:** Expand the description to:
  `"SQLite GLOB: * matches any sequence (incl. /), ? matches one char,
  [abc]/[a-z] character classes, [^abc] negation (note: ! does not negate
  in SQLite GLOB), no escape character for literals; malformed patterns
  silently match zero rows."`
- **Out of scope for this investigation:** Whether to ship a custom matcher
  that's more shell-like — answered by R1 F3 (decision was to be honest about
  GLOB, not switch implementations).

### F14 — Store doc comment claims `FindSymbolsByQuery` is case-sensitive; SQLite LIKE makes it case-insensitive

- **Severity:** informational
- **Reproducer:** `internal/store/store.go:432` says
  `"does a case-sensitive substring search"` but the query uses `LIKE`,
  which is **case-insensitive ASCII** in SQLite by default:

  ```text
  find_symbol { "query": "newserver" } → matches mcp.NewServer
  find_symbol { "query": "NEWSERVER" } → matches mcp.NewServer
  ```

  The MCP-layer schema (`handlers.go:39`) correctly says "case-insensitive";
  the store-layer doc lies. Internal mismatch, not user-visible.
- **Observed:** Slightly confusing for anyone reading the store code in
  isolation.
- **Expected:** Either change the store doc to "case-insensitive (SQLite
  LIKE)" or change the query to use `GLOB`/binary collation if case-sensitivity
  is actually desired. The MCP layer says case-insensitive — keep it that
  way; just fix the store comment.
- **Suggested fix shape:** One-line doc update on `FindSymbolsByQuery`.
- **Out of scope for this investigation:** Whether case-insensitive is the
  right default at all; users searching `New` vs `new` will currently get
  the same hits.

### F15 — `recent_changes.since` accepts negative values silently

- **Severity:** informational
- **Reproducer:**

  ```text
  recent_changes { "since": -1000 } → returns the entire 50-row default page
  ```

  `internal/store/recent.go:8-16` uses `WHERE indexed_at >= ?` with the
  raw value. Since `indexed_at` is always positive, any negative `since`
  effectively means "no lower bound". Same shape as R1 F10.
- **Observed:** Indistinguishable from `since=0` or `since` omitted.
- **Expected:** Schema-side `minimum: 0` would push back at the validator;
  the existing handler could also normalize. Schema doc says "optional
  unix-seconds lower bound" — negative unix seconds are conceptually
  nonsensical.
- **Suggested fix shape:** Add `minimum: 0` to both `RecentChangesInput.Since`
  and `GetDecisionsInput.Since`.
- **Out of scope for this investigation:** None.

## Things that worked

The R1 fixes hold up cleanly and several behaviors are notably solid:

- **R1 F1 fixed:** `record_claim` with omitted `session_id` succeeds and the
  resulting row has `session_id=""` in the store. `get_unverified_claims` (no
  filter) returns the unscoped row alongside session-tagged ones.
  `/tmp/leonard-mcp-r2/harness/main.go::probeClaim` confirms.

- **R1 F2 fixed:** Deleting `.leonard/leonard.db` mid-session results in:
  1. Immediate `database-replaced` error on the next tool call (lazy
     detection in `StoreAdapter.preflight`).
  2. A background 2-second ticker in `watchDatabase` (cmd/leonard-mcp/main.go:92)
     that calls `stop()` and tears the server down with exit 0.
  3. Empty-file replacement at the same path also triggers the inode-mismatch
     branch and returns the same structured error.

  `/tmp/leonard-mcp-r2/harness/dbdel.go` reproduces all three. Server exits
  with code 0 after the watcher fires.

- **R1 F3 fixed:** `list_files.pattern` schema description now honestly says
  SQLite GLOB. (F13 above is a follow-on tightening, not a regression.)

- **R1 F4 fixed:** SIGINT and SIGTERM both exit 0 cleanly. EOF on stdin also
  exits 0. `cmd/leonard-mcp/main.go::translateExitErr` converts the
  `context.Canceled` from `srv.Run` to `nil` before main exits.

- **R1 F5 fixed:** `find_symbol` now exposes a `language` field. Schema
  parity with `verify_symbol` is restored.

- **Concurrency:** 50 concurrent `record_claim` calls from a single client
  session against a fresh stdio binary all completed in 1.16s with 0 errors.
  WAL + busy_timeout(5000) handles the contention.
  (`/tmp/leonard-mcp-r2/harness/main.go::probeConcurrent`.)

- **Long sessions:** 1000 `find_symbol` calls in 378ms with no slowdown over
  time. Linear behavior, no memory growth signal.

- **FD hygiene:** A single connection issuing 2000 tool calls keeps FD count
  flat at 15 (`lsof -p <pid>` snapshot every 200 iterations). 100 sequential
  connect/disconnect cycles complete cleanly.

- **DB locked by external process:** Opening a `sqlite3` session with
  `BEGIN EXCLUSIVE` causes write calls to block for the 5s busy_timeout,
  then return a clean `record_decision: store: RecordDecision: database is
  locked (5) (SQLITE_BUSY)` as an MCP tool error (`isError=true`). The
  server doesn't crash; the next call after the external session releases
  the lock succeeds. Reads (via WAL) continue working throughout.
  (`/tmp/leonard-mcp-r2/harness/locking.go`.)

- **Schema validation surface:** the validator (`jsonschema-go` via the SDK)
  cleanly rejects:
  - Wrong types (`name: 123` → `has type "integer", want "string"`)
  - Missing required fields (`{"jsonrpc":"2.0","id":99,"method":"tools/call",
    "params":{"name":"verify_symbol","arguments":{}}}` → `required: missing
    properties: ["name"]`)
  - Unknown additional properties (`additionalProperties:false` everywhere)
  - Non-integer `since` / `limit` (rejected, including floats and date strings)
  All return `isError=true` with a structured text content; the server stays
  alive.

- **Unknown method:** A request for `no/such/method` returns
  `{"error":{"code":0,"message":"JSON RPC not handled..."}}` and the server
  keeps serving. Notifications for unknown methods are silently absorbed.
  (Modulo F1, which is about unparseable input — not unknown methods.)

- **`tools/list` pagination:** All 10 tools fit in one response; the SDK
  handles cursor passing transparently. Bogus cursor returns "invalid params"
  cleanly.

- **`recent_changes` cap:** `limit=99999` is correctly capped at 500;
  `limit=0` falls back to the documented default of 50. (Schema docs are
  honest here.)

- **`get_stale_decisions`:** Recording a decision with non-existent
  `related_files` / `related_symbols` correctly causes those entries to
  appear in the stale list; a decision with real refs does not.

- **Edge inputs round-trip:** unicode topics (`決定-éàü-😀`), null bytes
  (`with\x00null`), control chars, 64-KB reasoning strings — all stored
  and retrieved with no corruption; null bytes come back as ` ` in
  JSON.

## Open questions

- **F1 — stdio robustness:** Is "swallow per-line parse errors and continue"
  achievable without forking `mcp.StdioTransport` from the SDK? If not, is
  the right answer to document the strict-stdio expectation in the README and
  add a watchdog to restart leonard-mcp if the parent process detects it
  died? Process-supervisor territory.

- **F2 — limit-with-filter undercount:** Best-fixed by pushing language into
  the store schema (a column on `symbols`), which is a small migration but
  not free. Alternative: take it on the chin and document
  `"limit applies before language/kind filter; you may need to retry with a
  larger limit"`. Product call.

- **F7 — language enum:** Is the canonical-label set frozen at
  `go/python/typescript/javascript` or do we expect to add `rust`/`swift`/etc.
  in v0.2? If frozen, the enum constraint is easy and high-value; if growing,
  the schema needs to be regenerated when the indexer learns a new language.

- **`session_id` provenance:** Still unclear whether Claude Code is meant to
  supply `session_id` on every `record_claim` call, or whether the field
  exists primarily for the hook layer to fill in post-hoc. The post-Round-1
  rewording in `claims.go:14-18` softened this to "opaque tag — empty means
  unscoped", which makes the schema honest but leaves the operational story
  ambiguous. R1 F6 raised the same question; still open.

- **Asymmetric error prefixing (F11):** Decision/claim/changes handlers wrap
  errors with the tool name; symbol handlers don't. Is the right answer to
  unify on the wrapped style, or to drop the wrapping (let the SDK's
  CallToolResult.IsError + the underlying message speak for themselves)?
  Both are reasonable; whatever the call, the codebase should pick one.

- **Stdio CI coverage (R1 F15 — still open):** The harness I wrote is a
  reasonable starting point but its 14 separate sub-probes are too
  ad-hoc to land as-is. A `internal/mcp/stdio_test.go` that spawns the
  binary against a fixture DB and runs a table-driven test through
  `mcp.CommandTransport` would prevent regressions on F1 and similar
  transport-level issues that in-memory tests miss.
