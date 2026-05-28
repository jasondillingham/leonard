# Bughunt-12 — Lane L6 (MCP protocol fuzz) findings

**Lane:** L6 — MCP JSON-RPC stdio protocol layer fuzz
**Baseline:** Leonard d716b02, binaries `leonard-mcp 0.53.0`
**Discovered:** 2026-05-27
**Runlog:** `runlog/run-2026-05-27-L6-protocol-fuzz.md`
**Driver script:** `harness/lanes/L6-protocol-fuzz.sh`

Severity scale (matches `findings/FINDINGS.md`):
- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — path traversal escaping project root, DoS crashing hook process, secret leakage, trust bypass under known attack classes
- **MEDIUM** — resource exhaustion within bounds, error-swallowing masking problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F021 | LOW | L6 protocol | Unknown method returns `code:0` instead of JSON-RPC standard `-32601 method-not-found` | confirmed |
| F022 | LOW | L6 protocol | `initialize` accepted twice in same session; no reset, no error | confirmed |
| F023 | LOW | L6 protocol | `tools/call` works between `initialize` and `notifications/initialized`; ordering not enforced | confirmed |
| F024 | LOW | L6 protocol | Unknown client `protocolVersion`: server falls back without explicit error; clients that don't compare versions silently mismatch | confirmed |
| F025 | LOW | L6 protocol | Batch / array-shaped frames silently dropped instead of returning `-32600 invalid-request` (MCP 2025-06-18 disallows batches; silent-drop is the remaining defect) | confirmed |
| F026 | LOW | L6 protocol | Duplicate in-flight ids: server emits two responses both with `id=500` | confirmed |
| F027 | LOW | L6 protocol | Schema validation errors return as tool errors (`result.isError`) instead of JSON-RPC `-32602 invalid-params` — two parallel error channels | confirmed |
| F028 | LOW | L6 protocol | `null` for required field leaks Go `reflect` internals in user-visible error | confirmed |
| ~~F029~~ | n/a | L6 protocol | ~~Embedded NUL in `verify_symbol.name`~~ — withdrawn after advisor review: behavior is correct (NUL passes through to SQLite parameter binding, returns mismatch). Moved to negative-space. | withdrawn |
| F030 | LOW | L6 protocol | Two frames concatenated with no newline between are dropped silently; no `-32700` parse error returned | confirmed |

**Severity mix:** 9 LOW (after advisor review downgraded F025 LOW based on MCP 2025-06-18 transport spec, and withdrew F029). All extend the F008 family ("MCP error-shape is sloppy") with concrete new failure classes.

**Probes executed:** 33 sub-tests across L6a–L6j (catalog, init state machine, batch, additionalProperties, notifications, dup-id, frame-boundary, null/missing, unicode).

**Error-code surface tally (across all probes):**

| code | count | example |
|---|---:|---|
| `0` (non-standard) | 3 | pre-init `tools/call`; unknown method; bad `protocolVersion` type |
| `-32600` | 2 | params missing; `notifications/cancelled` with id |
| `-32602` | 1 | unknown tool name |
| `result.isError` (no JSON-RPC code) | 3 | missing required arg; arguments-as-array; additionalProperties:false violation |
| Silently dropped (stderr only, no client response) | 7 | bad JSON, wrong jsonrpc version, missing jsonrpc/method, batch, empty batch, concatenated frames |

This is what F008 generalizes to: the server has **three different error channels** (JSON-RPC `error`, MCP tool `isError`, stderr-only drop) selected by reasons not consistent with the JSON-RPC spec.

---

## F021 — Unknown method returns `code:0` instead of `-32601` (LOW)

**Files:** wherever `internal/mcp/` dispatches by `method` and constructs the not-found error response.

**Observed.** Three distinct error-construction sites all emit `code:0` (extends F008):

| Site | Message | Should be |
|---|---|---|
| pre-init `tools/call` | `method "tools/call" is invalid during session initialization` | `-32002` server-state |
| unknown method | `JSON RPC not handled: "nope/does_not_exist" unsupported` | **`-32601` Method not found** (spec-mandated) |
| `initialize` with `protocolVersion:12345` (int) | `handling 'initialize': unmarshaling ...into Go struct field mcp.initializeParamsV2.protocolVersion of type string` | `-32602` invalid-params (and strip Go-runtime text) |

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py raw '{"jsonrpc":"2.0","id":2,"method":"nope/does_not_exist","params":{}}' \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print([r for r in d['responses'] if r.get('id')==2][0]['error'])"
# => {'code': 0, 'message': 'JSON RPC not handled: "nope/does_not_exist" unsupported'}
```

**Why this is LOW.** Cosmetic + interop. No exploit. But: JSON-RPC 2.0 §5.1 explicitly enumerates `-32601` as method-not-found; clients implementing the spec by-the-letter will branch on `-32601` for retry/discovery logic and never see this class. Bigger fish — bughunt-11's F008 documented the same shape — but L6 now shows it's systemic, not one-off.

**Fix shape.** Single error-helper that picks the spec code by class:
- `-32700` for JSON parse errors (the dropped-line path should plumb this back as a response)
- `-32600` for malformed envelope (already used for params missing — good)
- `-32601` for unknown method (the gap)
- `-32602` for invalid params (already used for unknown tool — extend to all schema-validation failures, see F027)
- `-32002`/`-32003` (server-defined) for handshake-state errors (replace `code:0`)
- `-32603` for genuine internal errors

Add a regression test that asserts each error class hits its spec code.

**Discovered.** 2026-05-27 — L6a catalog probe.

---

## F022 — `initialize` accepted twice in same session; no reset, no error (LOW)

**Files:** `internal/mcp/server.go` — handler for `initialize`. The current implementation appears to be idempotent-write to a session struct without checking "already initialized."

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
# After the harness's default initialize+notifications/initialized handshake,
# send a SECOND initialize:
python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":100,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"redteam-2nd-init","version":"0.1"}}}'
# => id=1 (handshake) result-ok, id=100 (re-init) result-ok — both succeed
```

**Observed.** Second `initialize` returns a full `result` payload (`capabilities`, `protocolVersion`, `serverInfo`). No error. No log line on stderr. The server effectively just re-handshakes.

**Why this is LOW.** MCP spec treats `initialize` as a one-shot per-session lifecycle event. There's no protocol-level harm here — tools/list and tools/call still work. But:
- A client that wants to renegotiate capabilities (e.g. after a tool-list change) silently looks like it re-initialized when really nothing was cleaned up.
- A future MCP feature that ties session state (e.g. logging subscriptions, request cancellation tokens) to the init lifecycle would be vulnerable to surprise.

**Fix shape.** On second `initialize`, return `-32600` invalid-request with message "session already initialized." Or document explicitly that leonard-mcp supports re-handshake (and what state, if any, gets reset).

**Discovered.** 2026-05-27 — L6b init state-machine probe.

---

## F023 — `tools/call` works between `initialize` and `notifications/initialized` (LOW)

**Files:** `internal/mcp/server.go` — the gate that rejects pre-init `tools/call` likely checks "received initialize request" rather than "received initialized notification."

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py rawnohandshake \
  '{"jsonrpc":"2.0","id":110,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"r","version":"0.1"}}}' \
  '{"jsonrpc":"2.0","id":111,"method":"tools/call","params":{"name":"list_files","arguments":{}}}'
# => id=111 returns result.content (success!) — even though the client never sent
#    notifications/initialized between the two frames.
```

**Observed.** The pre-init `tools/call` reject (F008's site) IS active before `initialize`, but goes away as soon as the server has *responded* to `initialize` — the additional `notifications/initialized` handshake step is not gated. Compare with the F008 "method invalid during session initialization" error, which fires correctly when `tools/call` precedes `initialize`.

**Why this is LOW.** MCP lifecycle spec mandates client sends `notifications/initialized` before any further requests. A well-behaved client always does. A buggy/red-team client that skips it gets full tool surface — but it gets full tool surface either way (no auth boundary involved). Risk is purely "loose protocol enforcement," not exploitable.

**Fix shape.** Move the "session ready" gate to "received `notifications/initialized`" rather than "responded to `initialize`." Add a regression test: send init only, then tools/list — should get the `-32002` "wrong session state" error (matching F021's recommendation).

**Discovered.** 2026-05-27 — L6b probe.

---

## F024 — Unknown client `protocolVersion`: silent fallback; spec-compliant clients catch it, others don't (LOW)

**Files:** `internal/mcp/server.go` `initialize` handler — appears to accept any string for `protocolVersion`.

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py rawnohandshake \
  '{"jsonrpc":"2.0","id":101,"method":"initialize","params":{"protocolVersion":"1999-01-01","capabilities":{},"clientInfo":{"name":"r","version":"0.1"}}}'
# => 200 OK, server replies with protocolVersion "2025-11-25" (its own)
```

**Observed.**
- Client sends `"protocolVersion": "2025-06-18"` (the harness default) → server echoes `"2025-06-18"` back.
- Client sends `"protocolVersion": "1999-01-01"` → server replies with **its own** `"2025-11-25"` and proceeds normally.
- Client sends `"protocolVersion": 12345` (integer) → server errors with `code:0` and a leaky Go unmarshal message (F021c).

The string-valued bad version is silently negotiated by the server picking its own version. Per MCP spec, the client should disconnect if its requested version is not supported; here the only signal is that the response version differs from the request. A client that doesn't compare is happily working with a mismatched protocol.

**Why this is LOW.** Server-side defensive: the server falls back to a known-good version it supports. A spec-compliant client will detect the mismatch and abort. Risk is real only for non-compliant clients (which is most red-team tooling). Documenting this falls under "protocol robustness" rather than a defect.

**Fix shape.** On unsupported `protocolVersion` (string but unknown), return `-32602` invalid-params with a message listing supported versions: `"unsupported protocolVersion '1999-01-01'; server supports: [2025-06-18, 2025-11-25]"`. This gives the operator (and Claude Code's MCP layer) an actionable diagnostic.

**Discovered.** 2026-05-27 — L6b probe.

---

## F025 — Batch / array-shaped frames silently dropped; should return `-32600` (LOW)

**Files:**
- `internal/mcp/server.go` (or wherever stdin lines are decoded) — the line scanner likely calls `json.Unmarshal` into a single-frame struct, which fails for `[...]` arrays
- The error handler logs "dropped non-JSON-RPC line" to stderr but emits no client-visible response

**Important spec note (advisor-corrected):** The MCP 2025-06-18 transport spec (https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) explicitly disallows batches: *"Messages are individual JSON-RPC requests, notifications, or responses"* (stdio) and for Streamable HTTP *"The body of the POST request MUST be a single JSON-RPC request, notification, or response."* So leonard-mcp is **correct** not to process batches at the transport level. The defect that remains is **silent-drop vs. clean `-32600 invalid-request` response** — same as F030.

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py raw \
  '[{"jsonrpc":"2.0","id":200,"method":"tools/list"},{"jsonrpc":"2.0","id":201,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0001"}}}]'
# => (0 batch responses; stderr: 'leonard-mcp: dropped non-JSON-RPC line on stdin: "[..."')

# Empty batch also dropped silently, never -32600:
python3 harness/mcp.py raw '[]'
# => stderr only; no client-visible response
```

**Observed.** Three batch shapes all dropped silently:
1. Two-request batch — silently dropped, stderr line logged.
2. Cold (no handshake) batch including `initialize` — dropped.
3. Empty batch `[]` — dropped.

The dropped-line stderr log is **not** visible to the JSON-RPC client. A client that sent a batch (mistaken belief that JSON-RPC 2.0 generic semantics apply) hangs waiting for N responses that never arrive.

**Why LOW (downgraded from MEDIUM after advisor review).**
- MCP 2025-06-18 transport spec disallows batches — `leonard-mcp` correctly does not process them.
- The remaining issue is purely the silent-drop response shape: an MCP-naïve but JSON-RPC-2.0-aware client expects `-32600` rejection per JSON-RPC §5.1.
- No exploit, no hang on the operator's hot path (Claude Code doesn't batch).
- The fix is small: one extra branch in the decode path.

**Fix shape.**
- In the decode path, peek the first non-whitespace byte. If `[`, return a JSON-RPC error `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request: batch requests not supported (MCP 2025-06-18 transport requires single messages)"}}`.
- Same fix path closes F030 (concatenated frames).
- Add a regression test: send `[]`, assert clean `-32600` response.

**Discovered.** 2026-05-27 — L6c probe. Severity revised to LOW per advisor + MCP 2025-06-18 spec verification.

---

## F026 — Duplicate in-flight ids: server emits two responses with same id (LOW)

**Files:** `internal/mcp/server.go` request-dispatch loop — appears to not track in-flight ids.

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":500,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0001"}}}' \
  '{"jsonrpc":"2.0","id":500,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc1499"}}}' \
  > /tmp/dup.json
python3 -c "
import json
d = json.load(open('/tmp/dup.json'))
for r in d['responses']:
    if r.get('id') == 500:
        ms = r.get('result',{}).get('structuredContent',{}).get('matches',[])
        print('id=500, first sym=', ms[0].get('qualified_name') if ms else 'none')
"
# => id=500, first sym= bulk.BulkFunc0001
# => id=500, first sym= bulk.BulkFunc1499
```

**Observed.** Two distinct responses, both with `id:500`. A client that maintains a `{id → pending_request}` map and routes responses by id will deliver the wrong response to the wrong call site (whichever it processes second overwrites the slot, but the first has already been consumed — outcome depends entirely on client implementation).

**Why this is LOW.** JSON-RPC 2.0 §4 does not explicitly mandate in-flight id uniqueness; it only requires the server echo the id back. The actual problem is **client-side routing**: any JSON-RPC client maintaining `{id → pending_callback}` collides when two responses carry the same id. The server doing its honest job (compute & reply) is technically correct. But:
- A red-team or buggy client can trigger this trivially.
- A server-side defense (reject the second request-with-already-in-flight-id with `-32600` invalid-request) would protect downstream clients from their own bugs.
- F008-class shape — protocol-quality leak with no exploit path.

**Fix shape.** Track `in-flight ids` in the dispatcher. If a new request arrives with an id already in the set, reject with `-32600 invalid request: duplicate id 500`. Release the id when the response goes out. Add regression test.

**Discovered.** 2026-05-27 — L6f probe.

---

## F027 — Schema validation errors return as tool errors, not JSON-RPC -32602 (LOW)

**Files:** `internal/mcp/server.go` `tools/call` handler — the path that runs JSON-Schema validation on `params.arguments` against the per-tool input schema.

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
# Same failure class, two different error channels:

# (a) unknown tool → JSON-RPC error.code -32602:
python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}'
# => "error": {"code": -32602, "message": "unknown tool \"no_such_tool\""}

# (b) missing required arg → result.isError text (NO error.code):
python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"verify_symbol","arguments":{}}}'
# => "result": {"content": [{"text": "validating \"arguments\": validating root: required: missing properties: [\"name\"]"}], "isError": true}

# (c) additionalProperties:false violation → also result.isError:
python3 harness/mcp.py call verify_symbol '{"name":"BulkFunc0001","extra_field":"ignored?"}'
# => "result": {"content": [{"text": "validating \"arguments\": validating root: unexpected additional properties [\"extra_field\"]"}], "isError": true}

# (d) wrong type for known field → also result.isError:
python3 harness/mcp.py call list_files '{"limit":"fifty"}'
# => "result": {"content": [{"text": "...type: fifty has type \"string\", want \"integer\""}], "isError": true}
```

**Observed.** Schema-validation failures use `result.isError:true`. Tool-name-lookup failures use JSON-RPC `error.code:-32602`. Both are pre-execution failures of the same class ("params don't conform"). A client distinguishing protocol errors (treat as retryable / fatal differently) sees the same class through two channels.

**Why this is LOW.**
- F008 already documents the broader error-shape problem; F027 specifies one bifurcation in detail.
- Both error channels are still actionable text — the model can read either.
- The mismatch is a footgun for any embedder that branches on response shape (`if "error" in resp: ...` vs `if resp["result"].get("isError"): ...`).

**Fix shape.** Either:
- Move ALL schema-validation failures to JSON-RPC `error.code:-32602` (consistent with unknown-tool case). Recommended — this is closer to the JSON-RPC spec.
- OR move unknown-tool to `result.isError:true` (consistent with current schema-validation path). Less spec-aligned.

Pick one and apply uniformly. Document in DESIGN.md which error channel maps to which failure class.

**Discovered.** 2026-05-27 — L6a probe.

---

## F028 — `null` for required field leaks Go reflect internals (LOW)

**Files:** the JSON Schema validation library leonard-mcp uses (likely `github.com/santhosh-tekuri/jsonschema` or similar — wherever the `type: <invalid reflect.Value>` string comes from).

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py call find_symbol '{"query":null}'
# => "text": "validating \"arguments\": validating root: validating /properties/query: type: <invalid reflect.Value> has type \"null\", want \"string\""
```

**Observed.** The `<invalid reflect.Value>` phrase is a Go reflect.Value zero-value String() formatter artifact. Same family as F006 (Go unmarshal error leak), F012 (Go struct field name leak) — Leonard's MCP layer leaks Go-runtime internals in user-visible errors.

**Why this is LOW.** Cosmetic, but accumulating: F006, F012, F021 (initialize int variant), and now F028 all leak Go-runtime detail. The model reading these sees noise instead of an actionable diagnostic. A single error-formatter that strips Go-isms would close all four.

**Fix shape.** Wrap the validator's error in a friendly formatter:
- Strip `<invalid reflect.Value>` → `null`
- Strip `Go struct field X.foo of type Y` → just `field 'foo'`
- Strip `mcp.initializeParamsV2` → `initialize params`

Add a unit test that runs every error-emitting path and asserts no Go-runtime tokens remain in the user-visible text.

**Discovered.** 2026-05-27 — L6h probe.

---

## ~~F029~~ — withdrawn after advisor review

**Original claim.** Embedded NUL (U+0000) in `verify_symbol.name` silently treated as a mismatch query.

**Why withdrawn.** Reproducer ran `verify_symbol` with `name="BulkFunc<NUL>extra"` and got `exists:false`. That is the **correct** behavior — the symbol stored as `BulkFunc0001` genuinely doesn't contain a NUL byte, so the mismatch is expected. No truncation, no SQL injection (Leonard's `store.go` uses parameter binding), no surprise behavior observed. Per advisor review this padded the count.

The NUL pass-through behavior is recorded in the **Tested-clean (negative space)** section below.

**Re-file criterion.** If a future audit demonstrates **actual** NUL-truncation in SQLite parameter binding (e.g. `SELECT length(?)` returning < bound length with a NUL-containing param), this should be re-filed with the truncation evidence as the reproducer.

---

## F030 — Two frames concatenated with no newline silently dropped; no -32700 (LOW)

**Files:** the stdin line scanner (`bufio.Scanner` or equivalent) and the post-scan JSON decoder.

**Reproducer:**
```bash
cd /Users/jasondillingham/Documents/Homelab/projectdogwalker
python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":600,"method":"tools/list"}{"jsonrpc":"2.0","id":601,"method":"tools/list"}'
# => (0 responses for ids 600/601; stderr: 'leonard-mcp: dropped non-JSON-RPC line on stdin: "{...}{...}"')
```

**Observed.** The scanner reads the concatenated line as a single string, the JSON decoder rejects it (extra data after first object), and the server silently drops it with a stderr log. A client that mis-frames its output (forgot the newline) hangs forever waiting for two responses.

Same shape as F025 (batch silently dropped). Server defends against malformed input by dropping; it should respond with `-32700 parse error`.

**Why this is LOW.** Well-behaved clients newline-terminate every frame; this is an attacker / red-team / mis-implemented-client case. But:
- `-32700 parse error` (with `id: null`) is the JSON-RPC spec'd response for unparseable input.
- The dropped-line stderr log is invisible to the JSON-RPC client.

**Fix shape.** In the JSON decode path, when `json.Unmarshal` returns "invalid character '{' after top-level value" or similar, respond with `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error: trailing data after JSON value"}}` to stdout, in addition to the stderr log. Same treatment for the malformed-JSON / wrong-jsonrpc-version / missing-jsonrpc / missing-method cases — all currently drop silently (counted: 7 separate drop sites in this lane's runlog).

**Discovered.** 2026-05-27 — L6g probe.

---

## Tested-clean (negative space — worth recording so the next audit doesn't redo)

- **`additionalProperties:false` enforcement**: works for both `verify_symbol` and `find_symbol`. Schema-validator correctly rejects unknown fields. (Channel is `result.isError`, not `-32602` — see F027 for that nuance.)
- **`tools/list` is stable across the handshake variants tested** — returns 14 tools in every successful session.
- **Notifications with non-existent `requestId`** (cancelled for an id that doesn't exist) — silently no-op (`tools/list` afterward still works). Probably correct: cancellation of unknown request is a no-op per spec.
- **Unknown notification methods** (`notifications/imaginary`) — silently no-op. Probably correct: notifications are fire-and-forget.
- **Notification-shaped frame with id** (`notifications/cancelled` with `id:310`): server CORRECTLY returns `-32600 invalid request: unexpected id for "notifications/cancelled"`. Good — the dispatcher distinguishes notification methods from request methods.
- **Empty `notifications/initialized` x2** — no error, no double-init confusion.
- **Unicode RTLO + BOM in query string** — passed through to SQLite cleanly, returns 0 matches (likely correct).
- **`name:null` for `verify_symbol`** — rejected with same `<invalid reflect.Value>` shape as F028.
- **Multiple sites tested for handshake-state gate**: pre-init `tools/call` correctly rejected; the rejection error code (`code:0`) is the F008/F021 issue, not the gate itself.
- **Embedded NUL (U+0000) in `verify_symbol.name`** — passes through to SQLite parameter binding cleanly, returns `exists:false` (the mismatch is correct since stored symbols don't contain NUL). No truncation, no SQL injection observed (originally filed as F029, withdrawn per advisor review).

---

## Round status

Lane L6 ran 33 sub-tests, surfaced **9 findings** (all LOW; F029 withdrawn after advisor review confirmed correct behavior). All findings extend the F008 "MCP error-shape is sloppy" family — none are new severity classes. Highest-leverage fix:

1. **F021 + F025 + F027 + F030 (all LOW, single fix)** — adopt the JSON-RPC spec error-code mapping consistently across the server: `-32700` parse, `-32600` invalid request (including batch + empty-batch + concatenated frames), `-32601` method-not-found, `-32602` invalid params (including schema-validation failures, not `result.isError`), `-32002`/`-32003` for handshake-state, `-32603` for genuine internals. Closes F008 (the original `code:0` finding) too. Single error-helper.
2. **F028 (LOW)** — Go-runtime token stripper in the error formatter; closes F006/F012 as well.
3. **F022 / F023 / F024 / F026 (all LOW)** — small protocol-correctness defenses (reject second initialize, gate on `notifications/initialized`, surface protocolVersion mismatch, reject duplicate in-flight ids). Each is a few lines and a regression test.

**Combined with bughunt-12 totals through L5/L3:** **29 findings** overall, 0 CRITICAL, 2 HIGH (F013, F020), 10 MEDIUM, 17 LOW.
