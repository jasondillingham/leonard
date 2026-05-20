# Bug Hunt #6 — mcp-and-hooks-deep-audit

## Summary

Investigation-only deep re-audit of the MCP server (`leonard-mcp` /
`internal/mcp/`) and the four hook handlers (`leonard-hook` /
`internal/hooks/`) at v0.45.1. The accumulated changes since round 4
land roughly as advertised — `--version` works on all three binaries,
`migrateV7` is properly idempotent, the v0.38 / v0.39 claim-hygiene
fix-up reaches the Stop hook cleanly, and the `PostEditOptions` shape
change from v0.37 is internally consistent.

Three real bugs surfaced:

1. **HIGH — MCP `language` filter is stuck at 5 languages.** The
   `languageFromPath` helper that gates verify_symbol/find_symbol's
   language filter only knows `go/python/typescript/javascript`. v0.45
   advertises "27+ languages" and the indexer happily writes
   `rust`/`java`/`ruby`/etc. into `files.language`, but a model that
   calls `find_symbol(query="Foo", language="rust")` gets zero matches
   because every Rust file's extension returns `""` from the helper
   and `"" != "rust"`. The filter is silently exclusive — a real
   working tool surface from the model's perspective vanishes for the
   22+ non-Go-family languages.

2. **HIGH — `verify_symbol` and `find_symbol` have no aggregate
   response-size cap and no MCP-layer limit ceiling.** The store's
   `FindSymbolsByName` has no SQL `LIMIT` at all; `FindSymbolsByQuery`
   accepts whatever limit the model passes through (no MCP cap). A
   verify on a common name (`new`, `get`, `set`) on a 200k-symbol
   index materializes every match, and `find_symbol(limit=999999)`
   pulls a million rows into Go memory before encoding the response.
   The claims/decisions tools got 1 MiB aggregate caps in bughunt-4
   (`maxClaimsResponseBytes`, `maxDecisionsResponseBytes`); the
   symbol tools got missed.

3. **MEDIUM — `get_unverified_claims` silently truncates 1000→limit
   without flagging.** v0.44's SQL-side `LIMIT 1000` is correct as a
   safety cap, but with >1000 unverified rows the MCP layer slices
   to the user's limit (max 200) and returns a normal-looking
   response with no `truncated` field. A model paging through the
   ledger has no way to detect that 800 older rows are silently
   omitted.

Smaller findings: `record_claim` accepts arbitrary `file_path`
strings with no path-trust validation (low severity — the value
sticks in the DB but isn't used for any filesystem operation);
`PostToolUsePayload` doesn't decode `notebook_path`, so if a user
adds NotebookEdit to the PostToolUse matcher the hook exits 2 and
blocks the tool; `find_symbol`/`verify_symbol` expose the
`exported` flag from the store layer but never put it on the wire
(harmless but worth knowing v0.43's `is_exported` rework doesn't
reach Claude).

Things that flat-out work as designed: `--version` on all three
binaries (including the argv-check path in `leonard-mcp`); the
v0.16 `file_path` → `SupersedeClaimsForFile` round-trip;
`SessionStart` empty-decisions emits a clean minimal response;
`migrateV7` is idempotent against a fresh DB; SQLite WAL +
`busy_timeout(5000)` serializes concurrent Stop + post-edit
correctly; the v0.39 `vet_ok = 0` filter on
`SupersedeOutstandingFailures` correctly leaves MCP-recorded
claims alone (they have `vet_ok = NULL`).

## Findings

### F1 — MCP `language` filter only knows 5 languages; rejects 22+ supported languages

- **Severity:** high
- **Reproducer:** call `find_symbol(query="impl", language="rust")` on
  a project with rust files indexed. The store layer hands back every
  matching symbol; the MCP layer's `filterAndConvert`
  (`internal/mcp/server.go:155`) drops them all because
  `languageFromPath("foo.rs")` returns `""` and
  `"" != "rust"`. The same problem applies to `verify_symbol`.
- **Observed:** `Matches: []` for any language outside
  `{go, python, typescript, javascript}`.
- **Expected:** Either the helper covers every language the indexer
  writes into `files.language`, or the filter delegates to the
  per-row `files.language` value the store already persists. The
  README claims "27+ languages" so the filter regressing to 5 makes
  the MCP surface partially useless for users on Rust/Ruby/Java/etc.
- **Suggested fix shape:** replace `languageFromPath` with a lookup
  into the persisted `files.language` (the store record already
  carries it via `FileRecord.Language`) and pass that through into
  `SymbolRecord` so `filterAndConvert` can compare on the real
  indexed label rather than re-deriving from extension.
- **Out of scope for this investigation:** whether
  `languageFromPath` is used anywhere else where the same bug
  applies.

### F2 — `verify_symbol` / `find_symbol` have no aggregate response cap and no MCP limit ceiling

- **Severity:** high
- **Reproducer:**
  - `verify_symbol(name="get")` on the leonard self-index (or any
    realistically-sized project) returns every match with no cap.
    `FindSymbolsByName` has no SQL `LIMIT`
    (`internal/store/store.go:489`); the MCP shim
    (`internal/mcp/server.go:94`) passes the result straight to
    `filterAndConvert` with `limit=0` (unlimited).
  - `find_symbol(query="t", limit=10000000)` is accepted verbatim;
    `FindSymbolsByQuery` only defaults limit when ≤0
    (`internal/store/store.go:503`), so the model's value flows
    through to SQL.
- **Observed:** unbounded materialization on both code paths.
- **Expected:** parity with the claims/decisions tools. Bughunt-4
  added `maxClaimsResponseBytes`/`maxDecisionsResponseBytes` (both 1
  MiB) and per-row size accounting in
  `getUnverifiedClaims`/`getDecisions`. The symbol tools predate
  those caps and never got the equivalent treatment.
- **Suggested fix shape:** add a `verifySymbolMaxMatches` /
  `findSymbolMaxLimit` (e.g. 1000) clamp in
  `internal/mcp/server.go`'s shims, and a `maxSymbolsResponseBytes`
  aggregate cap inside `filterAndConvert`. For the `name=` path,
  push a `LIMIT` into `FindSymbolsByName` even at the store layer
  (mirroring v0.44's `queryRowCap` on claims).
- **Out of scope:** whether the SDK transport itself caps writes
  beyond what the wire format permits.

### F3 — `get_unverified_claims` silently truncates without a flag in the response

- **Severity:** medium
- **Reproducer:** record 1500 unverified claims, call
  `get_unverified_claims(limit=200)`. The SQL `LIMIT 1000` in
  `queryUnverifiedClaims`
  (`internal/store/store.go:1193`) returns 1000 rows; the
  MCP layer (`internal/mcp/claims.go:117`) slices to 200; the
  response carries 200 claims and no truncation flag.
- **Observed:** Caller has no way to know 1300 rows were dropped (300
  at SQL layer + 800 at MCP slice).
- **Expected:** The response should expose either a boolean
  `truncated`, a `total_available` count, or a `next_cursor` so a
  paging session can detect omission. The 1 MiB-byte cap on the
  same tool's response already returns early without signaling —
  the same pattern.
- **Suggested fix shape:** add `Truncated bool` to
  `GetUnverifiedClaimsOutput` (also to `GetDecisionsOutput`) and
  set it true when (a) the SQL cap fires, (b) the byte cap fires,
  or (c) the user's `limit` truncates the result. This is one of
  three structurally identical silent-truncation paths
  (claims/decisions/byte-cap-in-`getDecisions`) — fix them
  together.

### F4 — `record_claim.file_path` is unvalidated; arbitrary strings persist forever

- **Severity:** low
- **Reproducer:** MCP call
  `record_claim(claim="...", evidence="...", verified=false, file_path="/etc/shadow")`.
  The `recordClaim` shim (`internal/mcp/claims.go:83`) only checks
  size caps on `claim` and `evidence`; `file_path` is passed
  through to `RecordClaim` and stored verbatim. The post-edit
  hook (`internal/hooks/post_edit.go:208`) routes `file_path`
  through `index.ResolveSafe` before any disk read; the MCP
  surface does not.
- **Observed:** Foreign paths persist in `claims.file_path`. They
  never match a real `SupersedeClaimsForFile` query (`UPDATE …
  WHERE file_path = ?` only ever sees project-relative paths), so
  the row stays unverified indefinitely.
- **Expected:** Either reject paths outside the project root with a
  clear error, or normalize to "" so the row is unscoped (no future
  supersession target). The current behavior leaves persistent
  junk in the ledger.
- **Suggested fix shape:** wrap `recordClaim` with a `ResolveSafe`
  guard mirroring the post-edit hook. The MCP server doesn't carry
  a project root directly, but `leonard-mcp` does (cwd-derived
  during `main.go`) — thread it through the adapter or store it
  alongside `StoreAdapter.dbPath`. Document trade-off: dropping
  the row entirely vs. nulling the field vs. erroring.
- **Out of scope:** whether `record_decision.related_files` has
  the same exposure (bughunt-4 mcp F8 flagged this; status?).

### F5 — `PostToolUsePayload` doesn't decode `notebook_path`; NotebookEdit on PostToolUse blocks

- **Severity:** medium
- **Reproducer:** add `NotebookEdit` to the user's PostToolUse hook
  matcher in `.claude/settings.json`. The post-edit handler
  unmarshals into `ToolInput { FilePath string
  json:"file_path" }` (`internal/hooks/post_edit.go:75`).
  Claude Code's NotebookEdit payload uses `notebook_path`, not
  `file_path`. The post-edit handler hits
  `tool_input.file_path missing` (`post_edit.go:189`), returns
  `ErrDecode`, and exits 2 — which Claude Code interprets as
  "block this tool call." The pre-edit handler explicitly handles
  both fields (`PreEditToolInput.NotebookPath`,
  `pre_edit.go:179`), so the two hooks are asymmetric on the
  same surface.
- **Observed:** Any user who adds NotebookEdit to PostToolUse (the
  README example deliberately omits it, but the discrepancy is
  silent) bricks NotebookEdit. Pre-edit handles it; post-edit
  blocks.
- **Expected:** Either (a) post-edit decodes both fields and
  short-circuits on non-source files the way pre-edit does, or
  (b) the README explicitly calls out "NotebookEdit on PostToolUse
  will block; do not include it." The current "exit-2 if you
  include it" is a footgun that the binary itself doesn't
  diagnose.
- **Suggested fix shape:** add `NotebookPath string
  json:"notebook_path"` to `ToolInput`; when `FilePath` is empty
  fall back to `NotebookPath`; when the file isn't a tracked
  source (.go for the vet path, or any file the indexer
  recognizes for the index path) emit a no-op `{"continue":
  true}` instead of erroring.

### F6 — `exported` flag is loaded from the store but never appears on the wire

- **Severity:** informational
- **Reproducer:** Inspect `SymbolMatch` (`internal/mcp/handlers.go:9`).
  Fields: `File`, `Line`, `Signature`, `Kind`, `QualifiedName`. No
  `Exported`. The `SymbolRecord` returned by the adapter
  (`internal/mcp/adapter.go:266`) carries `Exported: s.Exported`,
  but `filterAndConvert` (`internal/mcp/server.go:158`) drops it.
- **Observed:** v0.43's `is_exported` rework on the parser side
  ("modifier text tokenized on word boundaries", etc.) cannot be
  surfaced through any MCP tool. A model has no way to ask
  "is this symbol exported".
- **Expected:** Either the field reaches the wire (and v0.43's
  changelog entry is meaningful at the MCP layer), or the field
  should not be derived in the adapter at all.
- **Suggested fix shape:** add `Exported bool json:"exported"` to
  `SymbolMatch` and populate from `s.Exported` in
  `filterAndConvert`. One-line change, but a behavior change for
  any caller already round-tripping the structured output —
  document in the changelog.
- **Out of scope:** whether any consumer (Claude Code itself, the
  evals harness) actually wants this field.

### F7 — `list_files` materializes all matching rows from SQL before truncation

- **Severity:** low
- **Reproducer:** in a 100k-file project, call `list_files()`. The
  store's `ListFiles` (`internal/store/store.go:523`) has no SQL
  `LIMIT`; it reads every row matching the GLOB/lang filters into a
  Go slice, and the MCP layer (`internal/mcp/server.go:131`) then
  truncates to ≤1000.
- **Observed:** Per-call memory cost scales with project size, not
  with the requested limit. Each file row is small (path + lang +
  size + indexed_at, ~hundreds of bytes), so the absolute cost is
  modest, but the pattern is the same one bughunt-5 perf F2 fixed
  for `queryUnverifiedClaims` and the symbol queries also miss
  (see F2 above).
- **Expected:** push the limit into SQL.
- **Suggested fix shape:** `ListFiles(pattern, lang string, limit
  int)`, or a separate `ListFilesPaged` that takes a hard cap
  matching v0.44's `queryRowCap = 1000` pattern. Caller in
  `internal/mcp/adapter.go` and `internal/mcp/server.go` already
  knows the effective limit before dispatching.

### F8 — Concurrent post-edit Stop window: Stop can see a transiently un-superseded failure claim

- **Severity:** low
- **Reproducer:** post-edit hook on file X with vet=ok runs three
  separate SQL statements: `RecordClaim` (INSERT),
  `SupersedeClaimsForFile` (UPDATE), `SupersedeOutstandingFailures`
  (UPDATE) — see `internal/hooks/post_edit.go:248-269`. Not in a
  shared transaction. If a Stop hook process fires
  `GetUnverifiedClaims` between RecordClaim and the supersedes,
  it sees the just-recorded vet=ok claim AND any outstanding
  failures that are about to be marked superseded by it.
- **Observed:** Stop output may include claims that are about to
  be invalidated by the next SQL statement in the post-edit
  process.
- **Expected:** Per the v0.38/v0.39 narrative, a vet=ok run "resolves"
  outstanding failures. The window between writes lets Stop see
  the pre-resolution state. SQLite WAL serializes correctly so
  there's no corruption — just a small consistency hiccup.
- **Suggested fix shape:** wrap the three writes in a single
  `RecordClaimAndSupersede` store method that runs them in one
  `tx.Begin()`/`Commit()`. Optional given how narrow the window
  is and how rarely it surfaces (Stop fires on session end, not
  per edit).

### F9 — `find_symbol(limit=0)` defaults to 50 at store layer; MCP doesn't surface a default

- **Severity:** informational
- **Reproducer:** call `find_symbol(query="x")` with no `limit`.
  `FindSymbolsByQuery` (`internal/store/store.go:503`) caps at 50
  when `limit <= 0`. The MCP layer (`internal/mcp/server.go:108`)
  passes `in.Limit` through unchanged.
- **Observed:** The model only knows the default is 50 by reading
  the source. The `FindSymbolInput.Limit`'s jsonschema description
  says "0 = unlimited" (`internal/mcp/handlers.go:42`).
- **Expected:** Description should say "0 = use default (50)" to
  match actual behavior — and ideally the MCP shim should apply
  the default itself (the way `getUnverifiedClaims` does) so the
  store layer's default isn't a hidden contract.
- **Suggested fix shape:** match the
  `findSymbolDefaultLimit`/`findSymbolMaxLimit` pattern used by
  the other tools. Documents the contract and decouples MCP-layer
  policy from the store-layer fallback.

### F10 — `recent_changes` with negative `since` returns all rows; no upper bound

- **Severity:** informational
- **Reproducer:** call `recent_changes(since=-1)`. The store
  comparison `indexed_at >= ?` (`internal/store/recent.go:14`)
  matches every row. The MCP shim and store both treat negative
  `since` as "no filter."
- **Observed:** Caller can request "all changes" by passing a
  negative number, even though the schema describes since as
  "unix-seconds lower bound" (implies ≥0).
- **Expected:** This is benign — the resulting query is still
  capped by `limit` (max 500). But the schema documentation is
  out of sync with behavior.
- **Suggested fix shape:** either clamp negative `since` to 0 in
  the shim (`internal/mcp/changes.go:38`) or update the schema
  description to acknowledge the "no filter" semantics.

### F11 — Stop/SessionStart decode errors return exit 1 with stdout empty

- **Severity:** informational
- **Reproducer:** `echo 'not json' | leonard-hook stop` (or
  `session-start`) returns exit 1; stdout is empty.
  `blockOnDecode` is only applied to pre-edit and post-edit
  (correctly — those are blocking hooks). Stop/SessionStart are
  advisory.
- **Observed:** Claude Code receives no JSON response and an exit
  1. Per the documented contract this is "non-blocking error" —
  the session proceeds without the advisory injection/surface.
- **Expected:** Either always write a minimal `{"continue":true}`
  to stdout on decode failure (so Claude Code's hook output handler
  has something to parse) or accept the current behavior with
  documentation.
- **Suggested fix shape:** wrap decode errors in the
  Stop/SessionStart RunE bodies so they still emit a
  `{"continue":true}` JSON line. The hook surfaces are advisory
  anyway; failing silent is arguably worse than degrading to
  no-op.

### F12 — `record_claim` accepts post-edit-only fields nowhere; tri-state hygiene

- **Severity:** informational
- **Reproducer:** `RecordClaimInput`
  (`internal/mcp/claims.go:21`) accepts `claim`, `evidence`,
  `verified`, `session_id`, `file_path` — but NOT `tool`,
  `index_ok`, `vet_ok`, `vet_error_summary`. Those are
  post-edit-hook-only fields (`internal/hooks/post_edit.go:33`).
  An MCP-recorded claim from a model therefore lands in the DB
  with `tool/index_ok/vet_ok/vet_error_summary` all NULL.
- **Observed:** The brief asked whether `record_claim` accepts
  these fields. Answer: it does not, and `SupersedeOutstandingFailures`
  (which filters on `vet_ok = 0`) consequently leaves
  MCP-recorded claims untouched on project-wide vet=ok runs. A
  vet=ok run on file X with `SupersedeClaimsForFile(X, …)` will
  catch them by file_path match, but a vet=ok run on a sibling
  file Y will not.
- **Expected:** This is correct by design — MCP claims aren't tied
  to a verifier exit code, so they should not be batch-resolved
  by an unrelated vet pass. The brief's prompt implied a possible
  shape mismatch; the actual story is "field absence is the
  intended boundary."
- **Suggested fix shape:** none. Worth documenting in
  `RecordClaimInput`'s jsonschema descriptions that the structured
  verifier fields are post-edit-only and `record_claim`-recorded
  rows are only superseded by a same-file vet=ok hook.

## Things that worked

- **`--version` works on all three binaries.**
  - `leonard-mcp --version` → `leonard-mcp 0.45.1` (argv-check
    before MCP run loop; `cmd/leonard-mcp/main.go:35-46`).
  - `leonard-mcp version` (no dash), `leonard-mcp -v`,
    `leonard-mcp --help`, `leonard-mcp -h`, `leonard-mcp help` all
    hit the same switch and return cleanly.
  - `leonard-mcp -- --version` (POSIX end-of-options): `--` is
    `os.Args[1]`, doesn't match any case, falls through to MCP
    run loop and dies on missing store. Acceptable — POSIX `--`
    isn't claimed.
  - `leonard-mcp foo` (unknown arg): falls through to MCP run loop
    (exits 1 on missing `.leonard/leonard.db`). Reasonable since
    the binary's job is stdio MCP, not CLI parsing.
  - `leonard --version` and `leonard-hook --version` both return
    cleanly via cobra's built-in `Version` field; both stamped at
    `0.45.1`.

- **`migrateV7` is idempotent against a fresh DB.** The migration
  runner records `schema_version` and only applies pending
  migrations (`internal/store/store.go:188-193`); a fresh DB runs
  v1…v7 in one transaction. The DELETE in v7
  (`internal/store/store.go:370`) hits no rows on a fresh
  install; running v7 a second time is prevented by the
  schema_version gate, not by the DELETE itself, but the DELETE
  is structurally idempotent anyway (it's a SET-shape WHERE
  filter).

- **v0.39 claim-hygiene reaches Stop cleanly.** The Stop hook
  reads `GetUnverifiedClaims(sessionID)` (not `…All`), so
  superseded rows are hidden. The `handleEscapedPath` and
  `handleMissingFile` no-record paths
  (`internal/hooks/post_edit.go:299, 335`) mean those two
  classes never end up in the ledger anymore. The v0.39 fix is
  internally consistent at the v0.45.1 surface.

- **MCP claim supersession (v0.16) round-trips correctly.**
  `RecordClaimInput.FilePath` flows through
  `cs.RecordClaim(…, in.FilePath, in.Verified)`
  (`internal/mcp/claims.go:93`) → `store.Claim.FilePath` →
  `SupersedeClaimsForFile(file_path, …)` matches on equality
  and supersedes. Tested at the unit level
  (`internal/mcp/claims_test.go`). Behavior holds.

- **`SessionStart` with zero decisions emits a clean response.**
  The handler (`internal/hooks/session_start.go:104`) only sets
  `HookSpecificOutput` when `len(decisions) > 0`; the response
  is `{"continue":true}` with no injection. Matches the brief's
  expectation.

- **`PostEditOptions` v0.37 shape is internally consistent.**
  `AlwaysVet`, `VetVerb`, `VetTimeout`, `Vet` all flow from
  `cmd/leonard-hook/post_edit.go:48-71` into the same fields
  read in `internal/hooks/post_edit.go:171-181`. The
  `parseVerifyTimeout` helper guards `≤0` durations correctly
  (v0.39 verifier F4).

- **PreToolUse handles all four write tools symmetrically.** Edit,
  Write, MultiEdit, NotebookEdit each have a dedicated case in
  `snippetsForTool` and `validateToolInputSizes`
  (`internal/hooks/pre_edit.go:270-320`); MultiEdit cap of 100
  elements rejects the WHOLE payload with `ErrDecode` (exit 2)
  rather than partially-applying — so no "element 50 fails but
  1-49 leak through" risk.

- **MultiEdit cap is pre-check, not post-application.**
  `validateToolInputSizes` runs before any snippet processing
  (`internal/hooks/pre_edit.go:167`), so an over-cap MultiEdit
  is rejected before the fabrication guard runs at all. No
  partial-claim risk.

- **stdin JSON-RPC filter resyncs past oversize lines (v0.13).**
  `lineReader` (`cmd/leonard-mcp/stdin_filter.go:104`) enters
  `skipping` mode on >16 MiB lines and drains until the next
  newline. The bughunt-2 mcp F1 / Security-1 F12 spin loop is
  closed.

- **SQLite concurrency holds.** WAL + `busy_timeout(5000)`
  (`internal/store/store.go:136-138`) serializes concurrent Stop
  + post-edit + MCP reads. Round 4's harness measured 5,184 MCP
  queries + 154 post-edit hooks in 8s with 0 errors. No new
  evidence to contradict that.

- **MultiEdit + Notebook decode envelope.** PreToolUse's
  `PreEditToolInput` decodes both `file_path` and `notebook_path`
  (`internal/hooks/pre_edit.go:60-66`), so the pre-edit side
  doesn't break on either tool. Asymmetry is only on the
  PostToolUse side (F5).

- **Decode-error exit code map is correct.** pre-edit + post-edit
  use `blockOnDecode` (exit 2 = block); stop + session-start let
  generic errors propagate to cobra (exit 1 = non-blocking).
  Matches Claude Code's documented hook contract.

- **Response-size caps are wired for claims + decisions.**
  `maxClaimsResponseBytes` and `maxDecisionsResponseBytes` (both
  1 MiB) clamp aggregate output in `getUnverifiedClaims` and
  `getDecisions` — the symbol tools are the gap (F2). The claim
  / decision side is intact post-bughunt-4.

- **`supersede_decision` rejects negative / zero IDs.** Guard at
  `internal/mcp/decisions.go:216`. Large invalid IDs fail at SQL
  with a clean `decision %d not found` error. New_choice /
  new_reasoning are cap-validated (bughunt-4 caps F2).

## Open questions

- **Should the MCP layer expose a `database_replaced` error
  signal to clients?** `ErrDatabaseReplaced` is a JSON-encoded
  error string (`internal/mcp/adapter.go:18`), but every MCP tool
  call short-circuits through `preflight` and returns that as a
  generic error. A client that wants to gracefully retry can't
  distinguish from any other store failure without parsing the
  error string. Out of scope here; possible bughunt-7 item.

- **Is the v0.41 parent-folding qname change reachable through
  any MCP tool?** `verify_symbol` is name-only (matches
  `symbols.name`); `find_symbol` matches both name and
  qualified_name via LIKE substring. So `module.users.id` is
  findable as `users.id`, `module.users`, or `id`. A model that
  was caching qnames from a pre-v0.41 session and tries to
  re-verify via `verify_symbol("id")` still hits — the basename
  didn't change. No actual regression at the MCP layer; this
  was effectively a no-op for the MCP surface.

- **Should `list_files`'s pagination story be re-thought?** 1000
  is a hard ceiling with no cursor. A 50k-file project can't
  enumerate fully through this tool. Not a bug — it's the
  documented contract — but it's a feature gap that might
  matter for evals.

- **Does any real Claude Code session pipe non-JSON-RPC noise
  through stdin frequently enough that the filter's
  log-to-stderr surfacing matters?** The filter handles the case
  cleanly but the noise level is unknown. Out of scope; would
  need real-session logs to answer.
