# Bug Hunt #4 — caps-and-limits

## Summary

Audited every external-input surface in Leonard v0.9.0 for size caps and limit handling. The v0.9 caps work where they're wired (`MaxHookPayloadBytes`, `MaxSnippetBytes`, decision/claim text caps, MCP limit fields), but coverage has clear gaps and one finding is critical: the `newOversizeTolerantScanner` "stub" in `cmd/leonard-mcp/stdin_filter.go` causes an **infinite log loop** the first time a real oversize line arrives, producing tens of MB of stderr per second and locking the server out of every subsequent request. Other gaps: `supersede_decision` bypasses every text cap, the CLI's `leonard decisions add` bypasses every text cap (security-1 F8 not fixed), indexed file size is uncapped (security-1 F4 still deferred), `list_files` has no result limit at all, and `find_symbol` documents `limit=0` as "unlimited" but is silently floored to 50 by the store. Off-by-one is uniformly inclusive (a payload of exactly the cap passes). The `capSnippets` zero-and-continue policy means a fabricated reference in a 1 MiB+ snippet, or beyond the 100th `MultiEdit` element, is silently allowed through the pre-edit guard.

## Bounds map

External input → cap layer → cap value → behavior on over-cap

| Input | Where capped | Cap | On exceed |
|---|---|---|---|
| Hook stdin (PostToolUse) | `hooks.readPayloadBytes` | 16 MiB (inclusive) | ErrDecode → exit 2 (block) |
| Hook stdin (PreToolUse) | `hooks.readPayloadBytes` | 16 MiB (inclusive) | ErrDecode → exit 2 (block) |
| Hook stdin (SessionStart) | `hooks.readPayloadBytes` | 16 MiB (inclusive) | ErrDecode → exit 1 (non-blocking) |
| Hook stdin (Stop) | `hooks.readPayloadBytes` | 16 MiB (inclusive) | ErrDecode → exit 1 (non-blocking) |
| `tool_input.new_string` / `content` / `new_source` per snippet | `hooks.capSnippets` | 1 MiB (`MaxSnippetBytes`) | Silently zeroed (treated as empty) |
| `MultiEdit.edits[]` element count | `snippetsForTool` | 100 (`MaxMultiEditElements`) | Tail silently truncated |
| MCP `record_decision.topic` | `mcp.recordDecision` | 256 bytes | Tool returns error |
| MCP `record_decision.choice` | `mcp.recordDecision` | 4 KiB | Tool returns error |
| MCP `record_decision.reasoning` | `mcp.recordDecision` | 32 KiB | Tool returns error |
| MCP `record_decision.related_files[]` | **unbounded** | — | Stored as-is |
| MCP `record_decision.related_symbols[]` | **unbounded** | — | Stored as-is |
| MCP `supersede_decision.new_choice` | **unbounded** | — | Stored as-is |
| MCP `supersede_decision.new_reasoning` | **unbounded** | — | Stored as-is |
| MCP `record_claim.claim` | `mcp.recordClaim` | 4 KiB | Tool returns error |
| MCP `record_claim.evidence` | `mcp.recordClaim` | 256 KiB | Tool returns error |
| post-edit hook claim text (summariseClaim output) | **unbounded** (always short in practice) | — | — |
| post-edit hook evidence text | `buildEvidence` | 16 KiB (`EvidenceCap`) | Truncated with `…(truncated)` trailer |
| MCP `verify_symbol.name` | **unbounded** | — | Passed to SQLite as-is |
| MCP `find_symbol.query` | **unbounded** | — | Passed to SQLite as-is |
| MCP `find_symbol.limit` | declared "0 = unlimited" but store floors to 50 | 50 then | Silently floored |
| MCP `find_symbol` result count | store default 50, MCP no cap | 50 | OK |
| MCP `list_files` result count | **unbounded** | — | Returns every file row |
| MCP `recent_changes.limit` | `recentChanges` | 500 (`recentChangesMaxLimit`); default 50; <=0 → default | Clamped |
| MCP `get_decisions.limit` | `getDecisions` | 200 (`getDecisionsMaxLimit`); default 20; <=0 → default | Clamped |
| MCP `get_stale_decisions.limit` | `getStaleDecisions` | 200 (`getStaleDecisionsMaxLimit`); default 50 | Clamped |
| MCP `get_unverified_claims` | **no limit field at all** | none | Returns every unverified row |
| CLI `leonard decisions add <topic> <choice> <reasoning...>` | **none** | — | Stored as-is (security-1 F8 not fixed) |
| CLI `leonard verify <name>` | **none** | — | Passed to SQLite as-is |
| Indexed source file (`os.ReadFile` in `indexAbs`) | **none** | — | Loaded into memory + parsed |
| `.gitignore` / `.leonardignore` | **none** | — | Loaded into memory |
| `go.mod` (pre-edit module-path resolver) | **none** | — | Loaded into memory |
| leonard-mcp stdin line (bufio.Scanner) | `newJSONLineFilter` | 16 MiB | Logged + scanner stays poisoned (see F1) |

---

## Findings

### F1 — `newOversizeTolerantScanner` causes infinite log loop on first oversize stdin line

- **Severity:** **high** (DoS — single oversize line locks the MCP server, dumps tens of MB of stderr per second)
- **Reproducer:**
  ```bash
  python3 -c "
  import sys, json
  sys.stdout.write(json.dumps({'jsonrpc':'2.0','id':0,'method':'initialize','params':{'protocolVersion':'2024-11-05','capabilities':{},'clientInfo':{'name':'x','version':'1'}}})+'\\n')
  sys.stdout.write(json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'})+'\\n')
  big = 'A' * (20 * 1024 * 1024)
  sys.stdout.write(json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'find_symbol','arguments':{'query':big}}})+'\\n')
  sys.stdout.write(json.dumps({'jsonrpc':'2.0','id':2,'method':'tools/list'})+'\\n')
  " > /tmp/probe.in
  (cat /tmp/probe.in; sleep 30) | leonard-mcp 2>/tmp/err.log >/dev/null &
  sleep 1
  kill %1
  wc -l /tmp/err.log    # observed: ~410k lines in 1 second
  ls -la /tmp/err.log   # observed: ~35 MB in 1 second
  ```
- **Observed:** When `r.src.Scan()` returns false with `bufio.ErrTooLong`, the handler logs the error, then reassigns `r.src = newOversizeTolerantScanner(r.src)` — which is a no-op stub that returns the same poisoned Scanner — and `continue`s. The next `r.src.Scan()` call on the same poisoned Scanner returns false with the same `bufio.ErrTooLong`, the log fires again, infinite loop. In a one-second test the loop wrote 410,399 stderr lines (~35 MB) and the follow-up `tools/list` request (id=2) was never serviced. The "stub" comment (`stdin_filter.go:93-103`) acknowledges the limitation but the actual behavior is worse than what the comment promises ("session still ends" — it doesn't end, it spins).
- **Expected:** Per the comment, an oversize line should be "log + skip" and the session should keep running. The session_id=2 tool call after the oversize line should be serviced. At minimum, the loop should not bury the host machine in stderr.
- **Suggested fix shape:** Replace `bufio.Scanner` with a custom line reader that can advance past an oversize line by discarding bytes until the next newline. Until then, the safest stopgap is to return the error rather than spin: drop the `if errors.Is(err, bufio.ErrTooLong)` branch and let ErrTooLong terminate the transport (the documented v0.9 comment claims this is what it does, but the code is one `continue` away from infinite). Confirmed via reading bufio docs: after `Scan` returns false with `Err() != nil`, subsequent `Scan` calls also return false with the same error — the Scanner is unrecoverable.
- **Out of scope for this investigation:** Whether the 16 MiB cap is the right number.

### F2 — `supersede_decision` bypasses every decision text cap

- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/sec-probe
  # (after recording any baseline decision as id=N)
  python3 -c "
  import sys, json
  huge = 'A' * (200 * 1024)
  msgs = [
    {'jsonrpc':'2.0','id':0,'method':'initialize','params':{'protocolVersion':'2024-11-05','capabilities':{},'clientInfo':{'name':'x','version':'1'}}},
    {'jsonrpc':'2.0','method':'notifications/initialized'},
    {'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'supersede_decision','arguments':{'decision_id':N,'new_choice':huge,'new_reasoning':huge}}},
  ]
  for m in msgs: sys.stdout.write(json.dumps(m)+'\\n')
  " | leonard-mcp
  sqlite3 .leonard/leonard.db "SELECT id, length(choice), length(reasoning) FROM decisions ORDER BY id DESC LIMIT 1;"
  # → length 204800 / 204800 — accepted without error
  ```
- **Observed:** `internal/mcp/decisions.go:183` (`supersedeDecision`) does not check `maxDecisionChoiceBytes` or `maxDecisionReasoningBytes` before delegating to `ds.SupersedeDecision`. `recordDecision` does check them. The asymmetry means a model that wants to evade the cap simply calls `supersede_decision` instead of `record_decision`.
- **Expected:** The same 4 KiB / 32 KiB checks `recordDecision` applies. Decision row size should be capped uniformly regardless of which tool produced the row.
- **Suggested fix shape:** Lift the size checks out of `recordDecision` into a helper (`validateDecisionText(choice, reasoning)`) and call it from both entry points.

### F3 — CLI `leonard decisions add` bypasses every decision text cap (security-1 F8 not fixed)

- **Severity:** low
- **Reproducer:**
  ```bash
  cd /tmp/sec-probe && leonard init .
  leonard decisions add 'topic' 'x' $(python3 -c "print('Z'*1000000)")
  sqlite3 .leonard/leonard.db "SELECT id, length(reasoning) FROM decisions;"
  # → 1|1000000 — accepted, no cap applied
  leonard decisions add $(python3 -c "print('T'*5000)") 'choice' 'reason'
  sqlite3 .leonard/leonard.db "SELECT length(topic) FROM decisions ORDER BY id DESC LIMIT 1;"
  # → 5000 — 5 KiB topic accepted (MCP cap: 256 bytes)
  ```
- **Observed:** `cmd/leonard/decisions.go:80` calls `rt.RecordDecision(... topic, choice, reasoning)` which routes to `realRuntime.RecordDecision` in `cmd/leonard/wire_real.go:93`, which calls `s.RecordDecision(store.Decision{...})` directly — no cap layer. Security-1 F8 already flagged this; v0.9 fixed the MCP side but left the CLI unfixed.
- **Expected:** The CLI should apply the same caps as MCP. Either share the cap helper (recommended by security-1 F8 suggested fix) or have the CLI go through the same validation layer the MCP tool does.
- **Suggested fix shape:** Move the cap constants out of `internal/mcp` into a `internal/limits` package (or `internal/hooks/limits.go` could grow them — naming aside, somewhere both layers import). Have both `mcp.recordDecision` and the CLI's `decisions add` call a shared `ValidateDecisionText` function.

### F4 — Indexed file size is uncapped (security-1 F4 still deferred)

- **Severity:** medium
- **Reproducer:** Create a 172 MB synthetic Go file containing valid syntax (`package foo\n` + millions of `// comment\n`). Run `leonard index`. RSS peaks at ~480 MB (measured via `/usr/bin/time -l`). A 2 GB synthetic file would OOM on most laptops.
  ```bash
  cd /tmp/caps-probe && leonard init .
  python3 -c "
  import sys
  sys.stdout.write('package foo\\n\\n')
  for i in range(200 * 1024 * 1024 // 50): sys.stdout.write('// some content line filler ABCDEFGHIJKL\\n')
  " > huge.go
  /usr/bin/time -l leonard index  # observed peak RSS 479 MB on a 172 MB file
  ```
- **Observed:** `internal/index/indexer.go:391` (`indexAbs`) calls `os.ReadFile(path)` with no size check. The full file bytes are then handed to `parse.ExtractGo` / `ExtractPython` / `ExtractTypeScript` / `ExtractRust`, each of which holds the bytes plus its own representation in memory. There's no per-file size cap anywhere in the chain.
- **Expected:** Either (a) skip files over some threshold (10 MiB is generous for hand-written source — generated files like minified JS or vendored libs are the realistic ones to skip), or (b) cap and record a parse failure. The store's `size_bytes` column already records the size, so a per-file `MaxIndexedFileBytes` constant would make the skip auditable.
- **Suggested fix shape:** Before `os.ReadFile`, stat the file. If `info.Size() > MaxIndexedFileBytes`, record a parse-failure-style entry ("file too large to index: 200 MB") and continue. Keep the file row so doctor/list_files still surfaces it; just skip the symbol extraction.
- **Out of scope for this investigation:** What the threshold should be. Bughunt-3 security F4 deferred this; nothing has changed since.

### F5 — Per-snippet 1 MiB cap silently zeroes the snippet (fabrication guard fails open)

- **Severity:** medium
- **Reproducer:**
  ```bash
  cd /tmp/probe && leonard init . && echo 'package foo' > foo.go && leonard index
  python3 -c "
  import json, sys
  big = 'package foo\\nfunc x() { other.SomeFakeFn() }\\n' + ('// pad\\n' * (1024 * 1024 // 8))
  payload = {
    'session_id':'s1','hook_event_name':'PreToolUse','tool_name':'Edit',
    'tool_input':{'file_path':'/tmp/probe/foo.go','new_string':big},
    'cwd':'/tmp/probe',
  }
  sys.stdout.write(json.dumps(payload))
  " | leonard-hook pre-edit
  # → {"continue":true}, exit 0 — fabricated reference SomeFakeFn never checked
  ```
- **Observed:** `capSnippets` zeroes any snippet over 1 MiB, and `decidePreEdit` then `strings.TrimSpace(snippet) == ""` skips the parse attempt entirely. A snippet at 1 MiB + 1 byte that contains a fabricated reference to a tracked-package symbol gets a clean `continue: true` instead of a block.
- **Expected:** This is a policy choice the brief calls out. Two reasonable alternatives:
  - Reject the whole hook (block the tool call) so the model knows the edit was rejected because the snippet was too large.
  - Truncate to the cap and parse the prefix (preserving as much of the parseable head as possible).
  - The current "silently allow" is the failure-open choice and contradicts the rest of the pre-edit hook's safety story.
- **Suggested fix shape:** Have `capSnippets` return a parallel `[]bool` of "dropped" markers, and surface a block decision when any are true with a reason like "snippet exceeds 1 MiB cap — split the edit". Or, less invasive: keep silent-allow but log to stderr so an operator can detect the bypass.

### F6 — MultiEdit element cap silently truncates (fabrication guard fails open on edits beyond the 100th)

- **Severity:** medium
- **Reproducer:**
  ```bash
  python3 -c "
  import json, sys
  edits = [{'old_string':'x','new_string':'y'} for _ in range(200)]
  edits[150] = {'old_string':'x','new_string':'package foo\\nfunc x() { other.SomeFakeFn() }'}
  payload = {
    'session_id':'s1','hook_event_name':'PreToolUse','tool_name':'MultiEdit',
    'tool_input':{'file_path':'/tmp/probe/foo.go','edits':edits},
    'cwd':'/tmp/probe',
  }
  sys.stdout.write(json.dumps(payload))
  " | leonard-hook pre-edit
  # → {"continue":true}, exit 0 — fabricated ref at edit #150 never seen
  ```
- **Observed:** `snippetsForTool` truncates `in.Edits` to the first 100. Edits 101..N are silently dropped without log or block. Same failure-mode as F5: the safety story breaks at the cap boundary instead of erring on the side of caution.
- **Expected:** Same as F5 — either block-and-report or log. The current silent truncation makes MultiEdit-with-101+-edits a trivial fabrication-guard bypass.
- **Suggested fix shape:** When `len(in.Edits) > MaxMultiEditElements`, return a block response naming the limit, or at least log to stderr. Aligns with the rest of the deny-on-uncertain policy.

### F7 — `list_files` has no limit (response could be millions of rows)

- **Severity:** medium
- **Reproducer:** Index a 100k+ file project (Linux kernel, e.g.) and call `list_files` from MCP with no pattern. The response includes every file row. Each row is ~150 bytes serialized → 100k files = ~15 MB response. The MCP JSON-RPC layer doesn't enforce a response cap either.
- **Observed:** `ListFilesInput` doesn't define a `Limit` field. `internal/store/store.go:469` (`ListFiles`) has no `LIMIT` clause in the SQL — it returns every matching row. `internal/mcp/server.go:111` (`listFiles`) doesn't slice the result.
- **Expected:** Either add a `limit` field (default ~500, hard cap maybe 5000) matching `recent_changes`'s shape, or document that `list_files` is unsuitable for large projects and direct callers to `recent_changes` + pattern filters.
- **Suggested fix shape:** Add `Limit int` to `ListFilesInput` with same default/cap shape as `recent_changes` (default 500, cap 5000 — files are smaller payloads than decisions so a higher cap is fine).

### F8 — `find_symbol` doc says `limit=0` is "unlimited" but store silently floors to 50

- **Severity:** low (cosmetic / misleading)
- **Reproducer:** Call `find_symbol` with `{"query": "X", "limit": 0}` on a project with >50 matches for X. Observe exactly 50 results.
- **Observed:** `FindSymbolInput.Limit` jsonschema says `"maximum number of matches to return (0 = unlimited)"` (`handlers.go:42`). But `store.FindSymbolsByQuery` (`store.go:450`) treats `limit <= 0` as 50. The MCP layer's `findSymbol` doesn't intercept, so `limit=0` resolves to 50 inside the store, not unlimited.
- **Expected:** Either honor the schema description (pass through a sentinel that disables LIMIT) or update the schema description. The "unlimited" promise is impossible-by-design once `list_files`-style accidentally-huge responses are on the table, so updating the doc to "max 50 with limit<=0" is the safer fix.
- **Suggested fix shape:** Change the jsonschema string to `"maximum number of matches to return (default 50, no hard cap)"`. Or make the MCP layer apply its own cap and clarify. Either is fine; the current text just shouldn't promise behavior the code doesn't deliver.

### F9 — `get_unverified_claims` has no limit field

- **Severity:** medium
- **Reproducer:** Run thousands of post-edit hooks that fail vet. Each writes a row. Call `get_unverified_claims` with no session_id: every unverified-and-not-superseded row is returned. With a busy session log accruing thousands of rows over weeks, the response grows unbounded.
- **Observed:** `GetUnverifiedClaimsInput` only has `SessionID` and `IncludeSuperseded`. No limit. `queryUnverifiedClaims` (`store.go:920`) has no LIMIT clause. The Stop hook applies its own `DefaultStopClaimLimit = 20` (`stop.go:66`) but that's a *consumer-side* clip, not a cap at the store/MCP boundary. A model calling `get_unverified_claims` directly gets the whole table.
- **Expected:** Match the shape of the other tools — add `Limit int` (default 50, cap 500). The current implicit "all rows" is consistent only with a fresh project.
- **Suggested fix shape:** Add `Limit` field to `GetUnverifiedClaimsInput`. Apply `getUnverifiedClaimsDefaultLimit` / `getUnverifiedClaimsMaxLimit` constants in `getUnverifiedClaims`. Pass through to `queryUnverifiedClaims`, which adds `LIMIT ?` to its SQL.

### F10 — `get_decisions` returns oversize rows verbatim (no per-row cap on read)

- **Severity:** low
- **Reproducer:** Write an oversize row via the CLI bypass (F3). Then call `get_decisions` with default limit. Each row carries the full reasoning blob — a 1 MB reasoning column produces a 1 MB JSON-RPC response line per row.
  ```bash
  leonard decisions add 'topic' 'x' $(python3 -c "print('Z'*1000000)")
  python3 -c "...record_decision 20 times via MCP..."
  # Then get_decisions returns 20 rows × varying sizes
  # Observed: 2 MB response for default limit after one CLI-bypassed row plus a few MCP rows
  ```
- **Observed:** `get_decisions` returns `Reasoning` verbatim (no per-row clamp on read). The cap is *only* enforced on write at the MCP layer. So an oversize row introduced via the CLI bypass (F3) or by a future tool change leaks across every subsequent read. Once a row is in the DB, it's permanent until manually deleted.
- **Expected:** Either reads should clamp oversize fields (with a `…(truncated)` marker), or the write-side caps must be defense-in-depth (CLI + MCP + store all reject). Either solves it; currently neither does.
- **Suggested fix shape:** If the cap is treated as a wire-level cap, clamp on read. If treated as a stored-value cap, fix F2 + F3 + add a store-level rejection. The bughunt-1 brief asks "where should caps live: MCP, store, or both?" — the v0.9 answer is "only MCP", which the CLI and supersede paths around.

### F11 — Decision `related_files` / `related_symbols` arrays are uncapped

- **Severity:** low
- **Reproducer:** Call `record_decision` with `related_files: ['A'*1MiB, 'B'*1MiB, ... × 1000]`. The arrays serialize to JSON and persist as TEXT in the `related_files` column.
- **Observed:** `recordDecision` only caps `topic`, `choice`, `reasoning`. The arrays bypass every cap and are stored as JSON strings in the DB. `get_stale_decisions` then iterates them — `s.missingFiles` does an N-query roundtrip per element. A decision with 100k bogus related_files would make `get_stale_decisions` issue 100k SELECTs.
- **Expected:** Cap the array length (~50 entries is reasonable for a real decision) and cap each element (file paths max ~512 bytes, symbol names max ~256 bytes).
- **Suggested fix shape:** Add `maxDecisionRelatedRefs` (e.g., 50) and check `len(in.RelatedFiles) + len(in.RelatedSymbols)`. Add per-element byte cap. Probably a single helper alongside the existing topic/choice/reasoning checks.

### F12 — `verify_symbol` / `find_symbol` query parameters are uncapped

- **Severity:** low (handled gracefully by SQLite but allocates memory)
- **Reproducer:** Call `verify_symbol` with `name="A" * (10 * 1024 * 1024)`. SQLite executes the parameterized query and returns no matches.
- **Observed:** No cap on `name` or `query`. The MCP wire frame is bounded by the bufio scanner's 16 MiB cap (which kills the session — see F1). The 10 MB query allocates the parameter + a `%A...%` LIKE pattern + escape-LIKE copy in memory.
- **Expected:** A 256-byte (`maxDecisionTopicBytes`-equivalent) cap on lookup names. Symbol names in Go/Python/TS/Rust are all well under 256 bytes in practice.
- **Suggested fix shape:** Add a `maxSymbolQueryBytes` constant (256) checked in `verifySymbol` and `findSymbol`. Reject early with an MCP error.

### F13 — `.gitignore` / `go.mod` reads are uncapped

- **Severity:** informational
- **Reproducer:** Place a 500 MB `.gitignore` at the project root. Run `leonard index`. The file is read fully into memory at `internal/index/indexer.go:472`.
- **Observed:** Both `loadIgnore` and `cmd/leonard-hook/pre_edit.go:101` (`defaultModulePath`, reading `go.mod`) use unbounded `os.ReadFile`. Realistically these files are tiny (gitignore: usually <10 KB; go.mod: usually <5 KB), but a hostile actor with file-system write access could plant a giant one.
- **Expected:** Same shape as F4. Stat-check, refuse if over a modest cap (1 MiB is plenty).
- **Suggested fix shape:** Same helper as F4.

### F14 — `MaxHookPayloadBytes` × `MaxSnippetBytes` × `MaxMultiEditElements` upper bound is generous

- **Severity:** informational
- **Math:** 16 MiB hook payload contains potentially 100 × 1 MiB = 100 MiB of snippet text when fully unpacked. `parseSnippet` makes up to 3 parse attempts per snippet (each allocates new bytes for the wrapper), so the worst-case allocator pressure per pre-edit invocation is roughly 300 MiB of source plus the AST. Measured: a real test of "16 MiB payload, 16 × 1 MiB snippets, no MultiEdit" allocates ~500 MB peak RSS (similar to F4's measurement on a 172 MB Go file). With MultiEdit elements added the worst case is materially higher.
- **Observed:** Each cap on its own is reasonable, but the cross-product isn't enforced anywhere. There's no "total snippet bytes per invocation" cap.
- **Expected:** Either document that the worst-case allocation is by-design acceptable (Claude Code's per-hook invocation is short-lived, so a 300-500 MB transient is recoverable), or add an aggregate cap (`MaxAggregateSnippetBytes = 16 * MaxSnippetBytes` — same as one full hook payload).
- **Suggested fix shape:** Add an aggregate sum check in `snippetsForTool`. Cheap; preserves the per-element semantics; bounds the pathological case without changing any normal flow.

### F15 — `record_decision` byte cap counts UTF-8 bytes, not runes/characters

- **Severity:** informational
- **Reproducer:** Pass `reasoning = '🚀' * 8192` (32 KiB exactly). Accepted. Pass `reasoning = '🚀' * 8193` (32 KiB + 4 bytes). Rejected with `record_decision: reasoning exceeds 32768 bytes`. So an emoji-heavy message gets ~8k characters but a plain-ASCII one gets ~32k.
- **Observed:** `len(s) > Max...Bytes` is a byte-length check (`internal/mcp/decisions.go:108-115`). UTF-8 multi-byte characters consume budget at 2-4× the rate of ASCII. The error message says "exceeds N bytes" which is technically correct but a user typing "exceeds N characters" might be confused.
- **Expected:** This is fine — byte caps are the right model for memory/DB defenses. Document in the schema description that the cap is bytes, not characters, so a non-English speaker doesn't get tripped up by a smaller-than-expected limit.
- **Suggested fix shape:** Update jsonschema strings on `reasoning` etc. to say "(max 32 KiB)" explicitly.

### F16 — Cap inclusivity is consistent (boundary = passes, boundary+1 = rejected)

- **Severity:** informational (verified working)
- **Reproducer:** A 16777216-byte (exactly 16 MiB) hook payload passes; 16777217-byte rejects. A 32 KiB reasoning passes; 32 KiB + 1 byte rejects.
- **Observed:** All caps use `len(s) > Max` — inclusive on the boundary. Documented here so future fixes don't change polarity by accident.

### F17 — Stop and SessionStart hooks don't apply `blockOnDecode` (advisory; exit 1 instead of 2 on ErrDecode)

- **Severity:** informational
- **Reproducer:**
  ```bash
  python3 -c "import sys; sys.stdout.write('A'*(17*1024*1024))" | leonard-hook stop
  # exit 1
  python3 -c "import sys; sys.stdout.write('A'*(17*1024*1024))" | leonard-hook session-start
  # exit 1
  ```
- **Observed:** `cmd/leonard-hook/stop.go:69` and `cmd/leonard-hook/session_start.go:70` wrap the handler error with `fmt.Errorf("...: %w", err)` but do NOT call `blockOnDecode`. So `ErrDecode` → exit 1 (non-blocking) for those two hooks. Pre-edit and post-edit do call `blockOnDecode` → exit 2 (block).
- **Expected:** This is correct for Stop/SessionStart — they're advisory, blocking would be wrong. Documented here so a future refactor doesn't accidentally make them blocking. The asymmetry is intentional but undocumented.

---

## Cap-coherence observations (not findings, observational)

- **DB column types:** `decisions.topic`, `decisions.choice`, `decisions.reasoning`, `claims.claim`, `claims.evidence` are all plain `TEXT` with no `CHECK` constraint. Caps are purely application-level. SQLite's default per-cell limit is ~1 GiB (`SQLITE_MAX_LENGTH`), well above anything Leonard would store. Document that the DB will accept anything the app sends.
- **Hook ErrDecode propagation:** `errors.Is(err, hooks.ErrDecode)` correctly traverses `fmt.Errorf("...: %w", err)` wraps. Pre-edit/post-edit's exit-2 mapping is solid.
- **Hook payload limit vs MCP stdio limit:** Both at 16 MiB (`MaxHookPayloadBytes` and `newJSONLineFilter` cap). Coherent. The comment at `limits.go:21-22` calls this out explicitly.
- **Decision reasoning cap (32 KiB) × `get_decisions` max (200 rows):** Worst-case response ≈ 6.4 MiB. Tolerable.
- **Recent_changes max (500) × per-row size (~150 bytes):** Worst-case response ≈ 75 KiB. Tolerable.

---

## Things that worked

- `readPayloadBytes` correctly maps oversize hook payloads to `ErrDecode` and via `blockOnDecode` → exit 2 for the blocking hooks. Verified end-to-end: a 16 MiB + 1 byte payload to `leonard-hook post-edit` exits 2 with a useful error message.
- `record_decision` caps fire cleanly: a 33 KiB reasoning is rejected with `record_decision: reasoning exceeds 32768 bytes` and the row never lands in the DB.
- `record_claim` caps are wired correctly (paralleling `record_decision`'s shape).
- `recent_changes` clamps `limit <= 0` to default 50 and `limit > 500` to 500. Verified by sending `limit: -5` and `limit: 99999` — both clamped silently to defaults.
- `get_decisions` clamps `limit <= 0` and `limit > 200` similarly.
- The cap boundary is uniformly inclusive (`>` not `>=`).
- Post-edit hook's `buildEvidence` correctly truncates with an explicit `…(truncated)` trailer.

## Open questions

- **Should caps be store-side too (defense-in-depth)?** F2, F3, F10 all stem from MCP-only caps. A store-level `RecordDecision` check would catch every caller. Adds duplication; what does the project policy prefer?
- **Should `capSnippets` block or allow?** F5 / F6 — the brief explicitly asks. My read: block is safer for v0 (deny-on-uncertain). But there's an argument that a 1 MiB+ snippet is probably auto-generated boilerplate where false-positive blocks would be more annoying than a missed fabrication check.
- **Is the bufio Scanner replacement work worth doing now?** F1 is a real bug (infinite loop), but the trigger requires a single oversize line — which a well-behaved Claude Code session won't produce. A simpler fix: just return the error instead of the broken `continue` loop. Long-term, a custom reader.
- **Should `list_files` keep its unbounded shape?** F7 — it works fine on Leonard's own ~50 file project but a Linux-kernel-sized index would melt. The brief calls it out as a question.
- **Where should caps "live" architecturally?** `internal/hooks/limits.go` holds hook caps. MCP caps are inline in `decisions.go` / `claims.go`. An `internal/limits` package could centralize them and become the import point for both layers (and the CLI). Worth discussing before fixing F3.

## Scratch artifacts to clean up

- `/tmp/caps-probe2/` (test project; deleted at end of probe)
- `/tmp/big.json`, `/tmp/exactly16.json`, `/tmp/over16.json`, `/tmp/pre.json`, `/tmp/multi.json`, `/tmp/mcp.in`, `/tmp/err.log` (probe inputs; deleted)
- `/tmp/leonard`, `/tmp/leonard-hook`, `/tmp/leonard-mcp` (binaries built for probing; safe to delete)
