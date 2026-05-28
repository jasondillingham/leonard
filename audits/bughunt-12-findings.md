# Bughunt-12 — Findings rollup

**Round:** Bughunt #12 (+ tracking notes for any new security review)
**Started:** 2026-05-27
**Baseline:** Leonard d716b02 + uncommitted `stop.go` simplification (see RED-TEAM-PLAN.md)

> **Reading note.** In `runlog/*.md`, `PASS` means *"the session didn't time out and the call returned exit 0"* — it does NOT mean "the server gave the correct answer." Many findings here (F004, F005, F009) were `PASS exit=0` calls whose response payload exposes the bug. Always cross-check the per-call response in the runlog for any test where correctness matters more than liveness.

Severity scale (matches `~/Documents/Homelab/leonard/audits/`):
- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — path traversal escaping project root, DoS crashing hook process, secret leakage, trust bypass under known attack classes
- **MEDIUM** — resource exhaustion within bounds, error-swallowing masking problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F001 | LOW | L1 build invariants | Version-constant drift across the three binaries (mcp 0.53.0, leonard + hook 0.52.0) — three separate sources of truth | confirmed |
| F002 | LOW | L1 docs | Homelab CLAUDE.md "setup pattern" template puts `mcpServers` in `settings.local.json`, but the current Claude Code schema rejects it there — `.mcp.json` is the correct location | confirmed |
| F003 | MEDIUM | L3 index | `leonard index` reports a false `FilesIndexed` count when project has >1000 files — the count is computed via a SQL-capped `ListFiles` query (sec-4 F3 cap), so on any non-toy project the operator sees `indexed 1000 file(s)` regardless of true count | confirmed (1515 fixtures → reported 1000) |
| F004 | MEDIUM | L5 parser robustness | Python parser rejects UTF-8-BOM-prefixed files that CPython's `python3 file.py` executes cleanly — symbols silently absent from index, `verify_symbol` returns false for symbols that genuinely exist | confirmed |
| F005 | MEDIUM | L2 caps | `find_symbol limit=0` returns exactly 50 matches; schema says `"0 = unlimited"`. Bug is *specific to the limit==0 path* (every value 1..500 returns exactly that many) — root cause is in `store.FindSymbolsByQuery` zero-default, not the MCP `filterAndConvert` clamp | confirmed + root cause narrowed |
| F006 | LOW | L2 caps | INT64-max `limit` triggers a leaky JSON-unmarshal error with float64 precision loss + a stray `}` in the user-visible message | confirmed |
| F007 | LOW | L2 caps | `find_symbol` with a 64 KiB query leaks a raw SQLite `"LIKE or GLOB pattern too complex"` instead of a clean MCP-layer "query too long" cap | confirmed |
| F008 | LOW | L6 protocol | MCP error responses use `code: 0` — JSON-RPC spec reserves 0 (standard server-defined range is -32000..-32099) — clients switching on code break | confirmed |
| F009 | LOW | L2 caps | `find_symbol query=""` returns up to 500 matches (empty substring matches everything — correct LIKE semantics, not a defect). Schema declares `required:["query"]` but no `minLength:1`. Reframed as a design choice; recommend `minLength:1` to surface caller mistakes | confirmed |
| F010 | MEDIUM | L5 onboarding | `adapters = []` (the default `leonard init` writes) silently *swaps* the tool surface based on whether `.leonard/ground-truth/` exists. Operator scaffolding ground-truth without changing config loses `find_symbol`/`verify_symbol`/etc. — 11 code-adapter tools replaced by 3 ground-truth tools, no warning | confirmed |
| F011 | MEDIUM | L5 onboarding | Ground-truth setup is broken end-to-end: `leonard init --adapter=code,ground-truth` scaffolds files but **does not update config.toml**; the natural inferred TOML shape `adapters = ["code","ground-truth"]` (matching the CLI flag syntax) crashes leonard-mcp at startup with a leaky Go-type error. Correct shape `[[adapters]] / type = "..."` is undocumented in CLI help and CLAUDE.md | confirmed |
| F012 | LOW | L5 onboarding | leonard-mcp startup error on bad config (`toml: cannot decode TOML string into struct field config.Config.Adapters of type []config.AdapterConfig`) leaks Go struct names. Operator can't fix what they can't translate | confirmed (companion to F011) |
| F013 | HIGH | L5 detector | **One malformed entry anywhere in `.leonard/ground-truth/*` causes the entire ground-truth adapter to fail init silently** — all 3 MCP tools (`verify_claim`, `list_facts`, `get_story`) drop from `tools/list` with the error only on stderr. A single bad story heading on line 23 took down the whole adapter. Operator sees "unknown tool" calls without context | confirmed |
| F014 | MEDIUM | L5 fuzzy | Fuzz threshold 1 (#85 default) over-matches on **numeric near-misses** — `"53 releases"` matches the forbidden rule `"52 releases"` (Levenshtein 1, semantically a different fact). `"57 releases"` also matches. Any 1-digit difference in a rule's number gets flagged | confirmed |
| F015 | MEDIUM | L5 detector | **4-digit years and "N years" tenure phrases still produce unverified-claim FPs after #98 fix.** "I started here in 2010 and stayed until 2015" → both years flagged. "2026 is the year. 13 years of dyslexia" → both flagged. Echoes DOGFOOD #2 directly; #98 mitigation incomplete | confirmed |
| F016 | MEDIUM | L5 fuzzy/word-boundary | **Word-boundary anchoring (#85) doesn't apply to the fuzzy-window's grown edge** — rule `"Scrum master"` matches inside `"Scrum mastery"` because the fuzzy window grew to length 13 (distance 1 insertion) and the boundary check only protects the rule's exact-length edge. Also double-counts: same span emits both the exact-match span and the fuzzy-extended span | confirmed |
| F017 | LOW | L5 facts impact | `leonard facts impact ""` (empty key) silently falls through to all-keys mode. An operator with a variable-substitution bug gets a huge report instead of a clear error | confirmed |
| F018 | MEDIUM | L5 detector scope | **`leonard check` and `list-stale-claims` scan `runlog/`, `findings/`, `RED-TEAM-PLAN.md` — every ISO date in the operator's OWN audit trail becomes a "finding."** No mechanism to exclude operator-internal harness/log dirs. The #74272b0 ground-truth-file exemption helps, but not for arbitrary working dirs. Single L5 run produced **192 findings** mostly from re-detecting fixture text echoed in this lane's own runlog | confirmed |
| F019 | MEDIUM | L5 caps | **`verify_claim` at exactly 262144-byte cap returns NO response in 30s** while cap+1 (262145) is rejected cleanly with the F6 "text too large" message. Looks like a `>` vs `>=` off-by-one — at-cap text falls through to the expensive fuzzy scan against every rule, hanging the server | confirmed |
| **F020** | **HIGH** | L3 index | **`pruneStaleFiles` uses the SQL-capped `Store.ListFiles("", "")` — same root cause as F003.** On any project with >1000 files, files at sort position > 1000 are NEVER checked for staleness. Deleted/moved file rows accumulate forever, and `verify_symbol` returns `exists=true` for symbols whose source file has been deleted — exactly the "fabricated APIs/symbols" failure Leonard exists to prevent | **confirmed with discriminating test** |
| F021 | LOW | L6 protocol | Unknown method returns `code:0` instead of JSON-RPC standard `-32601 method-not-found` | confirmed |
| F022 | LOW | L6 protocol | `initialize` accepted twice in same session; no reset, no error | confirmed |
| F023 | LOW | L6 protocol | `tools/call` works between `initialize` and `notifications/initialized`; ordering not enforced | confirmed |
| F024 | LOW | L6 protocol | Unknown client `protocolVersion`: server falls back without explicit error; clients that don't compare versions silently mismatch | confirmed |
| F025 | LOW | L6 protocol | Batch / array-shaped frames silently dropped instead of returning `-32600 invalid-request` (MCP 2025-06-18 disallows batches; silent-drop is the remaining defect) | confirmed |
| F026 | LOW | L6 protocol | Duplicate in-flight ids: server emits two responses both with `id=500` | confirmed |
| F027 | LOW | L6 protocol | Schema validation errors return as tool errors (`result.isError`) instead of JSON-RPC `-32602 invalid-params` — two parallel error channels | confirmed |
| F028 | LOW | L6 protocol | `null` for required field leaks Go `reflect` internals in user-visible error | confirmed |
| F030 | LOW | L6 protocol | Two frames concatenated with no newline between are dropped silently; no `-32700` parse error returned | confirmed |
| F031 | MEDIUM | L8 path-trust | APFS case-only path variants insert N store rows for one on-disk file — `verify_symbol` reports N matches for a symbol that exists in 1 file (Darwin/APFS-only; bughunt-4 F3 "OOS" extension) | confirmed with discriminating test |
| F032 | MEDIUM | L8 path-trust | Project-dir prefix case mismatch (e.g. `FIXTURES/...` vs `fixtures/...`) creates duplicate store rows + dual `verify_symbol` matches on case-insensitive volumes (sibling of F031, different attack surface — the `cwd`-relative join produces the case-mismatch path) | confirmed |
| F033 | LOW | L8 path-trust | Embedded NUL / newline / CR bytes in `file_path` still pass `ResolveSafe` (bughunt-4 F6 still open). No exploit primitive in v0.52 — `filepath.Clean` doesn't peel `..` past root in observed cases — but the noisy bytes propagate verbatim into systemMessage / additionalContext / would land in claim rows | confirmed |
| F034 | MEDIUM | L7 | `leonard check` does NOT detect facts.yaml contradictions — only `do-not-claim.md` bullets. DOGFOOD #1 gap remains. | confirmed |
| F035 | MEDIUM | L7 | `leonard facts diff` requires git, exits 0 on git failure with error text — CI consumer can't tell success from soft-failure | confirmed |
| F036 | MEDIUM | L7 | `leonard facts impact <key>` scans operator-internal dirs (runlog, findings, RED-TEAM-PLAN.md) AND grep-matches value-only (no key-context awareness) | confirmed |
| F037 | LOW | L7 | `list-stale-claims --scope` uses Go's `path.Match`-like semantics — `**` does not recurse intuitively; empty-glob exits 0 (indistinguishable from "all clean") | confirmed |
| F038 | LOW | L7 | `get_decisions` (MCP) and `decisions list` (CLI) both return superseded decisions inline with no `superseded_by_id` / `replaced_by` / "STALE" marker; reader sees two contradictory entries with no signal which is current | confirmed |
| F039 | MEDIUM | L7 | `get_unverified_claims` mixes auto-generated post-edit-failure claims (with 4000+ char file paths) with operator-recorded claims; no `source` / `kind` field to distinguish. CLI dump unreadable. | confirmed |
| F040 | LOW | L7 | `get_unverified_claims` response omits the `evidence` field that was sent into `record_claim`. The operator can't review the evidence trail without a separate query path. | confirmed |
| F041 | MEDIUM | L7 | `leonard doctor` reports the F003-capped file count (`files: 1000 total`) on the health-check command — the one place an operator would look to verify their store. Compounds F003. | confirmed |
| F042 | MEDIUM | L7 | DOGFOOD #6 (post-edit noise) is **partially closed** by `suppressOutput:true` but the `systemMessage` is still emitted per-edit; on a Go project where `go vet` runs, the vet output still lands on the conversation surface every time. | confirmed |
| F043 | LOW | L7 | `verify_symbol` exact-name miss returns `exists:false` with **no "did you mean..." suggestions** — even though `find_symbol` would have found the close match. Single highest-leverage dogfood UX win. | confirmed |
| F044 | LOW | L7 | `--limit 0 = default 200` in `truth-history --help` is the same documentation pattern as F005 (`0 = unlimited` that actually returns 50). Sentinels-with-floor are not consistent across commands. | observation |
| F045 | LOW | L7 | Auto-generated post-edit-failure claims accumulate forever — no TTL, no auto-resolve. After L8's path-trust probes, the claim ledger has multi-kilobyte path-string claims that survive across sessions. | confirmed |
| F046 | **HIGH** | L7 | **Bash matcher of the pre-edit hook does not inspect the command string for forbidden content** — `cat > file <<EOF ... EOF`, `echo ... > file`, and `sed -i 's/.../forbidden/'` all bypass the ground-truth check. The post-edit hook then doesn't catch the resulting file either (its only job is re-index). Net: every operator running with the standard hooks config has an unguarded write path via Bash. | **confirmed (PROMOTED)** |
| F047 | MEDIUM | L7 | `leonard override --once --reason=...` grants a token whose file lives at `$XDG_CONFIG_HOME/leonard/pending-override/<projHash>.<relHash>.json`, but the token-consume path is **never reached** — both the ground-truth forbidden-claim filter AND the `.leonard/` path-filter deny BEFORE the consumer runs, so the granted token sits in `pending-override/` until its 5-minute TTL expires. The help text says it bypasses "path_filters / content_filters" but the live ground-truth + `.leonard/` filters ignore the token. | confirmed |

(Lane runs append rows as findings surface — see `runlog/` for full traces.)

---

## F001 — Version-constant drift across the three binaries (LOW)

**Files:**
- `cmd/leonard/root.go:13`           — `const Version = "0.52.0"`
- `cmd/leonard-hook/root.go:21`      — `const Version = "0.52.0"`
- `cmd/leonard-mcp/main.go:27`       — `var version = "0.53.0"`
- `internal/mcp/server.go:21`        — `DefaultImplementation` hardcodes `"0.52.0"`

**Observed.** A clean `go install ./cmd/...` from HEAD `d716b02` produces:

```
leonard version 0.52.0
leonard-mcp 0.53.0
leonard-hook version 0.52.0
```

…and the MCP server's wire `serverInfo.version` reports `0.53.0` while the
default Implementation in `internal/mcp/server.go` (used by any embedder that
calls `mcp.NewServer(...)` without overriding) still says `0.52.0`. The repo's
latest tag is `v0.52.0` — there is no `v0.53.0` tag yet.

**Reproducer (from this red-team lane):**

```bash
cd ~/Documents/Homelab/leonard && git log -1 --format=%H && go install ./cmd/...
~/go/bin/leonard --version       # 0.52.0
~/go/bin/leonard-mcp --version   # 0.53.0
~/go/bin/leonard-hook --version  # 0.52.0
```

Also confirmed over the wire — `tools/list` after a fresh `initialize`:

```json
"serverInfo": {"name": "leonard-mcp", "version": "0.53.0"}
```

**Why this is LOW.** No security impact. But:
- The CLAUDE.md project-setup section explicitly tells operators that "all
  three should match" — there's no clean way for an operator to verify a
  consistent install today.
- A test fixture (or third-party embedder) that imports
  `mcp.DefaultImplementation()` quietly disagrees with what `leonard-mcp`
  reports over the wire.
- The version string is now a maintainer-error surface: every release will
  require remembering to update three files (or four, counting the README badge).

**Fix shape.** Single source of truth. Either:
- `internal/version/version.go` exporting `const Version` consumed by all three
  `cmd/.../root.go` and by `internal/mcp/server.go`; OR
- `-ldflags "-X main.version=..."` set in `Makefile install` from a `VERSION`
  file at the repo root.

Add a regression test that asserts all three `--version` strings match.

**Discovered.** 2026-05-27 — first bughunt-12 orientation step (visible before
any test ran).

---

## F002 — Stale Homelab CLAUDE.md template for Leonard setup (LOW, docs)

**File:** `~/Documents/Homelab/CLAUDE.md` (Leonard section, "Setup pattern" subsection)

**Observed.** The setup template instructs operators to add `mcpServers` to
`.claude/settings.local.json`:

```jsonc
{
  "mcpServers": { "leonard": { "command": "..." } },
  "hooks": { ... }
}
```

Current Claude Code settings.json schema (this CLI version) **rejects** the
`mcpServers` field there:

```
Settings validation failed:
- : Unrecognized field: mcpServers. Check for typos or refer to the documentation for valid fields
```

The correct location is `.mcp.json` at project root (the standard `.mcp.json`
mechanism is enumerated in the schema via `enabledMcpjsonServers` /
`enableAllProjectMcpServers`).

**Why this is LOW.** Anyone copying the template into a fresh project gets a
broken `settings.local.json` that **silently disables every other setting in
that file** (Claude Code rejects the whole file on schema failure, per the
update-config skill's troubleshooting notes). It's also externally visible:
this is the entrypoint instruction for getting Leonard working in a new project.

**Fix shape.** Update the Homelab CLAUDE.md `Setup pattern` block: move the
`mcpServers` block out into a sibling `.mcp.json` file and keep only `hooks`
in `settings.local.json`. Concrete `.mcp.json` template:

```json
{
  "mcpServers": {
    "leonard": { "command": "/Users/jasondillingham/go/bin/leonard-mcp", "args": [], "env": {} }
  }
}
```

**Discovered.** 2026-05-27 — observed while wiring `projectdogwalker` (the
classifier surfaced the schema rejection during the settings edit attempt).

---

## F003 — `leonard index` reports a misleading `FilesIndexed` count (MEDIUM)

**Files:**
- `cmd/leonard/wire_real.go:55-66` — `IndexAll` computes `FilesIndexed` via `s.ListFiles("", "")`
- `internal/store/store.go:644-688` — `Store.ListFiles` hard-caps rows at `MaxSymbolQueryRows` (1000) at the SQL boundary (sec-4 F3 hardening — heap-pressure fix)
- `cmd/leonard/index.go:30` — prints `leonard: indexed %d file(s)` to stdout

**Observed (from this lane).** Generated 1500 `.go` files + 1 `.py` under `fixtures/` of projectdogwalker, ran `leonard index`:

```
$ ~/go/bin/leonard index
leonard: indexed 1000 file(s)
leonard: 6 file(s) failed to parse — symbols dropped:
  ...
```

Direct DB inspection contradicts the report:

```
$ sqlite3 .leonard/leonard.db "select count(*) from files;"
1515

$ sqlite3 .leonard/leonard.db "select count(*) from symbols where name like 'BulkFunc%';"
1500    # all 1500 generated symbols are present
```

All 1500 fixture files are actually in the store with their symbols extracted.
The CLI just **counted them via a query that caps at 1000**.

**Why this is MEDIUM (not LOW).**
- It is operator-visible misinformation on the primary "did it work?" command.
  On any real medium-sized monorepo (>1k files — typical) the operator sees
  `indexed 1000` and reasonably concludes the indexer truncated their corpus.
  Many will then file false bug reports, or worse, paper over the "missing
  files" by skip-listing dirs or running the indexer in pieces.
- The CLAUDE.md project-setup section says the indexer handles "250–2000 files
  typical" — straddling exactly the cap.
- A scripted CI consumer that parses the `indexed N` line and asserts
  `N == git ls-files | wc -l` will silently fail for every project past 1000.

**Cause.** Sec-4 F3 (rightly) capped `Store.ListFiles` at the SQL boundary to
prevent heap pressure on monorepo queries. The fix was correct in scope for
its caller (MCP `list_files`), but `wire_real.go:65` reuses the same query to
compute a *total count*, where the cap silently lies.

**Fix shape.** Don't use a row-capped query to count. Options:
- Add `Store.CountFiles() (int, error)` doing `SELECT COUNT(*) FROM files` and use it for the report.
- Or have `index.Indexer.IndexAll()` return the count of files it actually walked + indexed (more accurate semantically — `FilesIndexed` should reflect *what the indexer just did*, not the DB total). The indexer already iterates the file list internally; returning that count is free.
- Add a regression test: generate >1000 dummy files, assert reported count == DB count.

**Discovered.** 2026-05-27 — surfaced on the first deliberate large-corpus test of the bughunt-12 campaign.

---

## F004 — Python parser rejects UTF-8 BOM that CPython accepts (MEDIUM)

**File (likely cause):** `internal/parse/python.go` and/or `internal/parse/extract_python.py` — wherever the Python source is loaded for parsing.

**Observed.**

```python
$ xxd fixtures/edge/bom_utf8.py | head -1
00000000: efbb bf64 6566 2062 6f6d 5f66 756e 6374  ...def bom_funct

$ python3 fixtures/edge/bom_utf8.py    # CPython runs it cleanly
$ echo $?
0
```

But Leonard's indexer drops the file with a parse error:

```
fixtures/edge/bom_utf8.py: line 1: SyntaxError: invalid non-printable character U+FEFF
```

The `bom_function` symbol is therefore **silently absent from the symbol
index**. A subsequent `verify_symbol` call returns `exists=false` for a
function that genuinely exists and runs.

**Why this is MEDIUM.**
- BOMs in source files are real — Windows-created Python sources commonly
  carry them, and `git` happily preserves them. The pre-edit fabrication
  guard could then **reject a valid reference** to `bom_function` in a
  Claude Code session, blocking legitimate work.
- The "indexed N" report flags it as a failure, so it's not technically
  silent — but operators who see `1 file failed to parse` for a BOMed file
  reasonably assume their file is broken (it isn't). The error message is
  also misleading: U+FEFF *is* "non-printable" only by Python's
  `ast.parse()` default — CPython itself strips the BOM during source loading.
- A 2-line fix at the top of the parser path (`strings.TrimPrefix(src, "﻿")`
  or read with `utf-8-sig` encoding) eliminates the class.

**Fix shape.** Strip a leading BOM (U+FEFF, byte sequence `EF BB BF`) before
handing source to the parser. Apply the same to *all* language parsers —
tree-sitter, the Go parser, the TS parser, and the Rust extractor are all
candidates for the same gap; round 12 hasn't yet probed each.

**Out-of-scope / open.** Multi-byte BOMs (UTF-16/UTF-32) — Leonard probably
doesn't need to support those for source code, but should at least skip them
cleanly rather than mis-parse.

**Discovered.** 2026-05-27 — bughunt-12 fixture run.

---

## F005 — `find_symbol limit=0` honors a 50-row floor, contradicts "0 = unlimited" (MEDIUM)

**Files (likely):**
- `internal/store/store.go` — `FindSymbolsByQuery(ctx, query, limit)` (the receiver of the user's `limit=0`)
- `internal/mcp/server.go:131-148` — `findSymbol` forwards `in.Limit` directly to the store
- `internal/mcp/server.go:206-230` — `filterAndConvert` correctly handles `limit<=0 → MaxSymbolResults`, so the cap-to-50 happens *before* this fix kicks in

**Tool description (over the wire):**
> `"maximum number of matches to return (0 = unlimited)"`

**Observed.** With 600 `ManySym*` symbols in the indexed DB:

| caller `limit` | matches returned |
|---:|---:|
|     1 |   1 |
|    25 |  25 |
|    49 |  49 |
|    50 |  50 |
|    51 |  51 |
|   100 | 100 |
|   200 | 200 |
|   499 | 499 |
|   500 | 500 |
| **0** | **50** |

Every value 1..500 returns exactly that many. Only `limit=0` — the documented
"unlimited" sentinel — returns 50.

**Reproducer:**
```bash
python3 harness/mcp.py call find_symbol '{"query":"ManySym","limit":0}' \
  | jq '.result.structuredContent.matches | length'
# => 50
```

**Why this is MEDIUM.**
- The tool description is explicit (`"0 = unlimited"`) — Claude is reading it.
  A reasoning step like "list all the ManySym candidates" with `limit=0` quietly
  hides 90% of the matches.
- Symptom is hard to notice: 50 results "look plausible" — there's no error,
  no warning, just a silently truncated view of the codebase. The exact failure
  mode the rest of Leonard's design protects against (model trusting an
  incomplete view as ground truth).
- The fix is one of two trivial lines: either match the description, or
  update the description to match behavior — both are 1-line changes plus a
  regression test.

**Fix shape.**
- Recommended: fix the behavior. In `store.FindSymbolsByQuery`, when `limit==0`
  treat it as "use MaxSymbolResults" (or pass no SQL LIMIT and let the MCP
  layer's existing `filterAndConvert` 500-cap handle it).
- Add a table-driven regression test: `for L in [0,1,49,50,51,499,500,501,1000]`
  → assert returned count == expected.
- If preserving the 50 default is intentional, change the tool description
  to `"(0 = use default, currently 50)"` and document the rationale.

**Discovered + verified.** 2026-05-27 — bughunt-12 lane L2; root cause narrowed
after an advisor review caught the original hypothesis ("store applies a 50-row
default") was ambiguous between "across all limit values" vs "only when
limit==0". The discriminator table above pins it to the latter.

---

## F006 — INT64-max `limit` leaks float64 precision + a stray `}` (LOW)

**File:** wherever `ListFilesInput.Limit` unmarshals JSON into a Go `int`.

**Reproducer:**
```bash
python3 harness/mcp.py call list_files '{"limit":9223372036854775807}'
```

**Response (tool error):**
```
json: cannot unmarshal number 9223372036854776000} overflows into Go struct field mcp.ListFilesInput.limit of type int
```

Two issues in one error:
1. **Stray `}` mid-message** — the JSON-RPC frame's closing brace is bleeding into the unmarshal-error text.
2. **Float64 precision loss** — `9223372036854775807` (input) becomes `9223372036854776000` (rounded to nearest representable double).

**Why LOW.** No exploit, no DoS. But: a 64-bit-max value (a reasonable input
from any non-Go client thinking in `i64`) triggers an error that leaks Go
struct-field internals to the model, and the precision-mangled value in the
message could confuse downstream debugging.

**Fix shape.** Clamp limit to `MaxSymbolResults` *before* JSON-unmarshal — use
`json.Number` or a custom unmarshaler. Strip frame characters from the user-
visible error. (The 2-frame bleed suggests the JSON decoder is reading past
its frame; verify the JSON-RPC framing is whole-frame, not greedy.)

**Discovered.** 2026-05-27 — bughunt-12 lane L2.

---

## F007 — `find_symbol` leaks raw SQLite "pattern too complex" on oversized query (LOW)

**File:** `internal/store/store.go` `FindSymbolsByQuery` SQL execution path.

**Reproducer:**
```bash
python3 -c 'import json;print(json.dumps({"query":"A"*65536}))' | python3 harness/mcp.py call find_symbol -
```

**Response:**
```
store: FindSymbolsByQuery: SQL logic error: LIKE or GLOB pattern too complex (1)
```

SQLite's LIKE/GLOB has an internal complexity cap (`SQLITE_MAX_LIKE_PATTERN_LENGTH`,
default 50000 bytes). A 64 KiB query trips it and the error reaches the model
as a tool error.

**Why LOW.** Recoverable, no exploit. But the model now sees a SQLite internal
error instead of a clean "query exceeds N bytes" message at the MCP layer.

**Fix shape.** Add a frontend cap on `find_symbol.query` length (suggest 4 KiB
— well above any real consumer, far below SQLite's pattern-length limit). Same
treatment for `verify_symbol.name` (also a LIKE pattern) and `list_files.pattern`.

**Discovered.** 2026-05-27 — bughunt-12 lane L2.

---

## F008 — MCP error responses use `code: 0` (LOW)

**File:** wherever the MCP layer constructs JSON-RPC error responses (likely
in `internal/mcp/` error-path helpers).

**Reproducer (tools/call before initialize):**
```bash
python3 harness/mcp.py rawnohandshake \
  '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_files","arguments":{}}}'
```

**Response:**
```json
{"jsonrpc":"2.0","id":7,"error":{"code":0,"message":"method \"tools/call\" is invalid during session initialization"}}
```

JSON-RPC 2.0 spec requires `code` to be an integer (any), but defines:
- `-32600`–`-32603` — protocol-level errors
- `-32000`–`-32099` — server-defined "Server error" range
- `0` is not standard. Clients dispatching by code (e.g. `if err.code == -32601: retry`) won't see this as any known class.

**Why LOW.** Server *does* correctly reject the pre-handshake call (defensive
posture is right). But the code value reduces interop with strict JSON-RPC
clients.

**Fix shape.** Pick a stable server-defined code in `-32000..-32099` — e.g.
`-32002` ("server is in wrong state for the request"). Document the code map
in `DESIGN.md` so clients can switch on it.

**Discovered.** 2026-05-27 — bughunt-12 lane L2.

---

## F009 — `find_symbol query=""` returns everything (LOW — design recommendation)

Reframed after advisor review: empty-string-matches-everything is the
**correct** LIKE-semantic behavior. The input schema declares
`required: ["query"]` but no `minLength: 1`, so an empty string is valid input
that the SQL `'%%'` pattern legitimately matches against every row.

**Reproducer:** `python3 harness/mcp.py call find_symbol '{"query":""}'`
→ returns 500 matches (the MaxSymbolResults cap, also working correctly).

**Why LOW (not a defect).** The cap fired; nothing leaked. The recommendation
is purely defensive: a model that accidentally sends `query=""` (variable
substitution forgot to fill in) currently gets back a plausible-looking
"top 500 symbols" instead of an error pointing at the bug.

**Fix shape.** Add `"minLength": 1` to the `query` property in the input schema
for `find_symbol`. Tool-call validation rejects empty-string before SQL runs.
Same treatment worth considering for `verify_symbol.name` and `record_decision.topic`.

**Discovered.** 2026-05-27.

---

## Open observations (not yet filed as findings)

These surfaced during orientation but lack the reproducer + root-cause analysis
that a real finding needs. They are entry points for the next lanes:

- **L8 entry:** `pre-edit` hook returned `continue:true` for `tool_input.file_path =
  /etc/passwd` (an absolute path far outside the project root). Whether this is
  correct ("not my project, leave it to Claude Code's permissions") or a path-
  trust gap (the guard should explicitly reject and say so) needs investigation
  — start by reading `internal/hooks/pre_edit.go` and tracing the path-trust
  branch for absolute-outside-root paths. Not classified pending root cause.
- **L4 entry:** `leonard-mcp` is SIGPIPE-sensitive — piping its stdout to
  `head -c N` where N < total response size causes the server to lose its
  buffered output and the consumer reads zero bytes. Not a defect (every
  Unix tool dies on SIGPIPE), but worth verifying the equivalent failure
  doesn't manifest when Claude Code's MCP client closes a stream mid-response
  during a model context overflow.
- **L7 entry:** confirmed via `mcp.py` smoke-tests that **just spawning
  `leonard-mcp` triggers a project-tree scan** (file appears in `list_files`
  with no prior `leonard index`). Useful for testing but means every MCP query
  starts a goroutine that walks the tree — worth measuring cost on a 10k-file
  repo (L4 perf lane).

## F013 — Malformed stories.md silently kills the entire ground-truth adapter (HIGH)

**Files:**
- `internal/adapters/groundtruth/stories.go` — parser that hits `empty story name` and returns error
- `internal/adapters/groundtruth/adapter.go` — adapter `New()` that bubbles the parse error up
- `internal/dispatcher/*` — dispatcher that catches the error and drops the adapter

**Observed.** Wrote a stories.md fixture containing a deliberately malformed entry (`## STORY:` with empty name after the prefix, simulating an operator typo):

```markdown
## STORY:
Empty name after STORY: prefix — pathological case.
```

Subsequent `tools/list` returned 11 tools (code-adapter only). The 3 ground-truth tools (`verify_claim`, `list_facts`, `get_story`) silently vanished. Any subsequent `tools/call` to them returned:

```json
{"error": {"code": -32602, "message": "unknown tool \"verify_claim\""}}
```

The only diagnostic — visible **only on stderr** of leonard-mcp, which Claude Code may not surface — was:

```
leonard dispatcher: "ground-truth" init failed: .leonard/ground-truth/stories.md:23: empty story name
```

**Why this is HIGH.**
- This is the user-facing surface that ground-truth promises (`verify_claim`, `list_facts`, `get_story`). When the operator's tools mysteriously disappear, debugging starts from "did I configure adapters right?" not "is there a single bad heading in one of my truth files?"
- The operator scaffold itself encourages experimentation with stories — every line they edit is a potential adapter-killer.
- Adjacent fixture probe (`## architecture` loose heading) — UNTESTED for this round because the empty-STORY entry preceded it and killed adapter init before the loose-heading parser could be evaluated. This is itself a known-unknown: did the #83 loose-heading fix actually work? We can't tell from this lane because the parser bailed.
- A single typo in `do-not-claim.md`, `facts.yaml`, or `stories.md` likely has the same blast radius (not yet tested for facts.yaml; likely true given the adapter init flow).

**Fix shape.** Partial-load on parse errors:
- Stories: skip the malformed entry, load the rest, warn loudly via `systemMessage` or via an MCP `notification/log` message at HIGH level.
- Facts: same — invalid leaf shouldn't prevent the valid leaves from being queryable.
- The adapter init returning `error` is too coarse — split into "fatal config error" (refuse to start) vs "partial-load with warnings."
- Surface partial-load warnings via a new `get_health` MCP tool or via the `systemMessage` channel so Claude Code can render them.

**Discovered.** 2026-05-27 — bughunt-12 lane L5 setup phase. Surfaced before any L5 probe ran.

---

## F018 — `leonard check` / `list-stale-claims` scan operator-internal dirs by default (MEDIUM)

**Files:**
- `cmd/leonard/check.go` / `cmd/leonard/list_stale_claims.go` — walkers that recurse from cwd
- `internal/adapters/groundtruth/mdwalk.go` (likely) — markdown discovery
- `internal/adapters/groundtruth/exemption.go` — existing `74272b0` ground-truth-file exemption

**Observed.** Running `leonard list-stale-claims` from a project root yielded **192 findings**, of which the vast majority were ISO timestamps inside the harness's own audit trail:

```
runlog/run-2026-05-27-L5-groundtruth.md: 192 finding(s) — 64 forbidden, 124 unverified, 4 verified, 0 opinion
  [UNVERIFIED] date — "2026-05-27"  (line 1109)
  [UNVERIFIED] date — "2026-05-27"  (line 1130)
  ... 60+ more identical-looking ISO timestamps ...
  [FORBIDDEN] forbidden — "52 releases"  (line 1530)  rule=Stale release counts#1
  ... fixture text being detected verbatim out of the lane's own runlog ...
```

The runlog files contain (a) verbatim copies of fixture inputs from the lane (which then get classified as forbidden), and (b) every timestamp the harness wrote. Both are operator-internal artifacts that should not count as claim work-product.

`findings/FINDINGS.md` itself also got scanned — every "Discovered. 2026-05-27" line became a stale-date claim.

**Why this is MEDIUM.**
- The signal-to-noise problem is real and operator-discouraging. After Leonard ships and operators start running `list-stale-claims`, every project with a `CHANGELOG.md`, `audits/`, or any decision-log file will flood with timestamps. The default UX punishes the operators most likely to benefit (those who keep audit trails).
- The fix is conceptually simple but currently absent: a `.leonardignore` (or `[scan.exclude]` in config.toml) to scope what `check` walks.
- The existing #74272b0 ground-truth-file exemption shows the team understands this class — F018 is the next instance of the same shape, just for arbitrary operator dirs.

**Fix shape.**
- Add `scan_exclude` (default: `["runlog/**", "**/.*"]`) to `config.toml` or a `.leonardignore` file.
- Default-exclude common audit dirs (`audits/**`, `runlog/**`, `CHANGELOG.md`) and let operators opt in via `--include` or by clearing the default.
- Document the exclusion mechanism inline in the `leonard check` help text.

**Discovered.** 2026-05-27 — L5g/L5j.

---

## F019 — `verify_claim` at-cap input (exactly 262144 bytes) hangs the server (MEDIUM)

**Files:**
- `internal/adapters/groundtruth/mcp.go` — `verify_claim` MCP handler (bughunt-11 F6 cap site)
- Likely the cap check is `if len(in.Text) > MaxClaimText` (strict `>`) — exactly-at-cap is allowed in but the body is then handed to the fuzzy detector

**Observed.**

| Input size | Result |
|---:|---|
| 262144 bytes (== cap) | **No response in 30 seconds** (`NO RESPONSE for id=2`) |
| 262145 bytes (cap + 1) | Tool error: `"verify_claim: text exceeds 262144 bytes"` (clean, fast) |
| 1 MiB | Tool error (clean, fast) |
| empty string | Empty findings (works) |

**Reproducer:**
```bash
python3 -c 'import json; print(json.dumps({"text":"x"*262144}))' \
  | MCP_TIMEOUT=30 python3 harness/mcp.py call verify_claim -
# => NO RESPONSE for id=2
```

**Why MEDIUM (not HIGH).** Not a remote-DoS — only callable via MCP stdio from a same-host MCP client (Claude Code). But the model can absolutely send a 262144-byte text inadvertently — a `verify_claim` call on the body of a large markdown file (DOGFOOD #1 envisions this) sized at exactly the limit hangs the server, and Claude Code's tool-call timeout (typically 30-60s) fires before any response, presenting as a generic "tool timed out."

**Fix shape.**
- Change the cap check to `>=`: any text at cap or above gets the clean "text too large" rejection, no expensive scan attempted.
- Or, after passing the size guard, run the detector with a wall-clock timeout (e.g. 10s) so any pathological input — even within size limit — returns rather than hangs.
- Add a regression test: at-cap input should respond (with claims or with cap rejection) within 1 second.

**Discovered.** 2026-05-27 — L5e cap-edge sweep.

---

## F020 — Prune sweep misses files past sort-position 1000; `verify_symbol` lies (HIGH)

**Files:**
- `internal/index/indexer.go:393-430` — `pruneStaleFiles()`, the v0.7 fix for bughunt-2 cli F2 ("ground-truth-drift")
- `internal/index/indexer.go:394` — the offending line: `files, err := i.Store.ListFiles("", "")`
- `internal/store/store.go:644-688` — `Store.ListFiles` SQL-caps at `MaxSymbolQueryRows` (1000) per the sec-4 F3 hardening (same site that causes F003)

**The cycle Leonard exists to break.** From `README.md`:
> "Leonard targets four recurring Claude Code failure modes:
> 1. **Fabricated APIs/symbols** — pre-edit hook rejects references to symbols
>    that don't exist in any tracked package."

F020 silently re-introduces failure mode #1 on every real project.

**The bug.** `pruneStaleFiles` collects "every file row that should be
removed" by listing files from the store via `Store.ListFiles("", "")`.
That listing is bounded at the SQL boundary by `LIMIT MaxSymbolQueryRows`
(1000). On any DB with >1000 file rows, only the first 1000 (ORDER BY
path) are checked for existence. Any deleted/moved/renamed file whose row
sits at sort position > 1000 is **invisible to the pruner forever**.

**Discriminating reproducer (this lane, 2026-05-27 21:51).**

Same operation, same DB, two files with different sort positions:

| Fixture | Sort position | After `rm` + `leonard index` |
|---|---:|---|
| `fixtures/a_prune_test.go` | 0 (front of alphabet) | **pruned** ✓ — sym & file row gone |
| `fixtures/l3_scratch/will_be_deleted.go` | ~1510 (past cap) | **NOT pruned** — `verify_symbol` still returns `exists=true` with `match: fixtures/l3_scratch/will_be_deleted.go line 2`, for a file that does not exist on disk |

```bash
# After running the L3 lane against the 1500-file fixture set:
sqlite3 .leonard/leonard.db "select count(*) from files;"
# => 1527

python3 <<'PY'
import os, sqlite3
db = sqlite3.connect('.leonard/leonard.db')
rows = db.execute("SELECT path FROM files").fetchall()
orphans = [p for (p,) in rows if not os.path.exists(p)]
print(f"orphan rows: {len(orphans)}")
PY
# => orphan rows: 8

# verify_symbol confirms the model would see the stale entry:
python3 harness/mcp.py call verify_symbol '{"name":"WillBeDeleted_L3e"}'
# => exists=True, match: fixtures/l3_scratch/will_be_deleted.go line 2
```

**Why this is HIGH.**
- This is the **exact failure mode Leonard's pre-edit fabrication guard
  is designed to prevent.** A symbol whose declaration was removed weeks
  ago will continue to verify as `exists=true` with a precise (wrong)
  file/line, indefinitely.
- The threshold for hitting it is trivially low. The CLAUDE.md project-
  setup section explicitly describes Leonard handling "250–2000 files
  typical." Every project past 1000 files is vulnerable; the breaking
  point isn't size-of-deletion but sort-position-of-deletion.
- Operator-invisible. There is no log line, no stderr warning, no count
  reported by `leonard index` (and F003 itself ensures the operator gets
  a misleading "1000 file(s)" instead of a real count from which they
  could infer the discrepancy).
- Compounds with F003: same root cause site, both bugs would be closed
  by the same fix.
- The fix is small and the test infrastructure already exists. The repo
  has `TestIndexAll_PrunesDeletedFiles` (passing!) — it just uses a
  fixture corpus too small to trigger the cap. Extending that test to
  generate >1000 dummy files would have caught this.

**Fix shape.**
- Add `Store.IterFiles(fn func(File) error)` or `Store.AllFilePaths() ([]string, error)` — a cursor or fully-paginated iterator that doesn't apply the safety cap. Document that this is for INTERNAL bookkeeping only.
- Replace `i.Store.ListFiles("", "")` at `indexer.go:394` with the new uncapped iterator.
- Same call site exists at `wire_real.go:65` (F003) — same fix closes both.
- Regression test: generate 1500 dummy files, delete the alphabetically-last one, assert it's pruned.

**Discovered.** 2026-05-27 — bughunt-12 lane L3 discriminating test. Initial L3e/L3f failures showed the symptom (8 orphan rows, both delete and rename failed to clean up); the discriminating test with `a_prune_test.go` vs `fixtures/l3_scratch/*` pinned the root cause.

---

## L4 — concurrency & crash-consistency — NO NEW FINDINGS (positive result worth recording)

Negative-space evidence so subsequent audits don't redo the work. L4 covered
the SQLite + WAL foundation under stress; everything held.

**Tested and clean:**

| Probe | Result |
|---|---|
| 8 concurrent `leonard index` against same DB | All workers rc=0; zero `locked`/`busy` errors; `integrity_check=ok` after each N=2/4/8 |
| MCP query during running indexer | +60ms latency over baseline cold-spawn (~940ms) → noise. WAL readers not blocked by writer |
| `kill -9` mid-index (20ms into a ~500ms write window) | rc=137 ✓, `integrity_check=ok` immediately, re-run `leonard index` recovers cleanly with full counts |
| 20 rapid back-to-back `leonard index` calls | WAL returns to 0B between runs — checkpoints firing aggressively, no unbounded growth |
| Concurrent in-session MCP tool calls (14 tools/call + 3 tools/list across 3 sessions) | All 17 IDs answered; no dropped responses; intermixed tools/list during in-flight tools/call works |
| Two MCP server processes against same DB simultaneously | Both rc=0, both return correct matches |
| pre-edit hook + `leonard index` running concurrently | Both rc=0, no lock contention |

**Out-of-scope confirmed clean.** The SQLite busy_timeout (5s default per
bughunt-3 notes) is sufficient even at N=8 on this workload size; WAL recovery
after SIGKILL is robust; in-process MCP request handling is concurrent-safe.

**Per-CALL stdio MCP cold-spawn cost** of ~940ms is observable (every `mcp.py
call` spawns a fresh `leonard-mcp` process) but not a defect — it's the price
of the stdio transport model. A long-lived MCP client (which is how Claude
Code actually uses it) doesn't pay this per-call.

---

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

---

## F031 — APFS case-only path variants → N store rows for one file (MEDIUM, Darwin/APFS-only)

**Files (root cause):**
- `internal/index/indexer.go:526-589` — `ResolveSafe` returns the caller-supplied lexical form; no case-normalization.
- `internal/index/indexer.go:691-702` — `storeKey` `norm.NFC.String(filepath.ToSlash(rel))` normalizes unicode (NFC) and slashes, but does **not** lowercase. On a case-insensitive volume, `Camel.go` and `CAMEL.GO` produce different `storeKey` values for the same APFS inode.
- `internal/store/store.go` — `UpsertFile` keys on `path` exactly; each distinct storeKey gets its own row.

**Bughunt-4 F3 OOS note (the predecessor that flagged this):**
> "Out of scope for this investigation: Case-only collisions (`README.md` vs `readme.md` on case-insensitive APFS volumes). Same family of bug; treat in the same fix."

The same-fix-as-NFC closure didn't happen — NFC landed (F3 NFC half closed) but the case half didn't.

**Discriminating reproducer (this lane, 2026-05-27 22:04 ET):**

```bash
# One on-disk file
mkdir -p fixtures/L8_scratch/case
cat > fixtures/L8_scratch/case/Camel.go <<'EOF'
package casecheck
func L8CaseCamel() {}
EOF

# Fire post-edit three times with three case variants
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/Camel.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/camel.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/CAMEL.GO

# Inode + content check — all three resolve to the same APFS inode + same content hash
stat -f '%i  %N' fixtures/L8_scratch/case/{Camel.go,camel.go,CAMEL.GO}
# 230172698  fixtures/L8_scratch/case/Camel.go
# 230172698  fixtures/L8_scratch/case/camel.go
# 230172698  fixtures/L8_scratch/case/CAMEL.GO

# DB has 3 file rows + 3 symbol rows
sqlite3 .leonard/leonard.db \
  "select path, hash from files where path like 'fixtures/L8_scratch/case/%';"
# fixtures/L8_scratch/case/Camel.go|f57eab37...
# fixtures/L8_scratch/case/camel.go|f57eab37...
# fixtures/L8_scratch/case/CAMEL.GO|f57eab37...

sqlite3 .leonard/leonard.db \
  "select name, file_path from symbols where name = 'L8CaseCamel';"
# L8CaseCamel|fixtures/L8_scratch/case/Camel.go
# L8CaseCamel|fixtures/L8_scratch/case/camel.go
# L8CaseCamel|fixtures/L8_scratch/case/CAMEL.GO
```

**Observed via the MCP surface (the consumer-visible failure mode):**

```bash
python3 harness/mcp.py call verify_symbol '{"name":"L8PrefixCase"}'
# exists=true, matches=[
#   {file:"FIXTURES/L8_scratch/case/PrefixCase.go", line:2, ...},
#   {file:"fixtures/L8_scratch/case/PrefixCase.go", line:2, ...}
# ]
```

The model is told the symbol exists in **two** files (with different paths!) when it exists in one file on disk. Same file, same line, same signature — the row count is the only delta.

**Why this is MEDIUM:**
- Leonard's whole value proposition is "ground truth — verify_symbol won't tell the model something exists when it doesn't." This bug doesn't fabricate a symbol, but it inflates its location count, which is the exact failure mode the README enumerates: *"hallucinated file paths — verify_symbol pinpoints where a symbol lives."* Pinpoint now returns N points for one location.
- Repeatable trivially. Any project on macOS where two tools (or two model turns, or the model itself running in different sessions) reference the same file with slightly different case will accumulate duplicate rows. Examples that produce divergence today: `pkg/Foo.go` vs `pkg/foo.go`, `README.md` vs `readme.md`, `Tests/` vs `tests/`. None of these are exotic.
- Compounds with F003/F020: each duplicate row is also a potential orphan post-rename. If on-disk `Camel.go` is removed but the only reference the next session uses is `camel.go`, the row keyed by `camel.go` would be checked-and-pruned correctly — but **so would `Camel.go` and `CAMEL.GO`**, because `os.Stat` follows APFS case-insensitive resolution and all three forms `Stat()` successfully **even after the rm** until the canonical-case file is the one removed. The asymmetry between "indexer stores case-preserving" and "OS resolves case-insensitively" is the bug root.
- Test confirmation: after `rm fixtures/L8_scratch/case/Camel.go`, all three rows DID get pruned by `leonard index` (because the canonical form was removed, all variants now `Stat → ENOENT`). So this specific failure path closes itself. But the inflated-match-count failure during normal operation persists.

**Severity rationale (MEDIUM not HIGH):**
- No path escape: all 3 rows resolve to the same in-root file.
- No fabrication-into-thin-air: the symbol does exist.
- The harm is duplicate-result inflation in `verify_symbol` / `find_symbol`, which violates Leonard's contract softer than F020 but still meaningfully (the model sees 2-3 "files" containing the same single thing).
- Darwin/APFS-only — Linux ext4 keeps the bytes verbatim so the three case variants would name three different files there (no overlap, no bug). Fix is platform-conditional: only `runtime.GOOS == "darwin"` (and Windows NTFS) need case normalization.

**Fix shape:**
- Add an `IsCaseInsensitiveFS(root string) bool` helper that probes the root once at indexer construction (touch a file, stat its uppercase, compare inode). Cache the result on the Indexer.
- In `storeKey`, when case-insensitive: `strings.ToLower(rel)` after the NFC normalization. The store rows become canonical lower-case; reads from `verify_symbol` / `find_symbol` lower-case the input similarly before lookup.
- Alternative (less invasive but more expensive): keep storeKey case-preserving but add a `UNIQUE(LOWER(path))` constraint via SQLite `COLLATE NOCASE` on the `files.path` column; `UpsertFile` then deduplicates at insertion. Trade-off: the persisted "canonical" path string becomes whichever case happened to land first, which may surprise operators inspecting the DB.
- Document: Leonard chooses lower-case canonical on case-insensitive filesystems. (The same docstring at indexer.go:691-702 that explained NFC choice should extend to case.)
- Add a regression test: write file `Foo.go`, post-edit three case variants, assert 1 file row + 1 symbol row.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 7 (intentional extension of bughunt-4 F3 OOS).

---

---

## F032 — Project-dir prefix case mismatch creates duplicate rows (MEDIUM, Darwin/APFS-only)

**Files (root cause):**
- `internal/index/indexer.go:526-589` `ResolveSafe` — the path-trust check passes for any case combination of the prefix (because APFS resolves case-insensitively at the OS layer, so the secondary `EvalSymlinks` cross-check succeeds for both `FIXTURES/...` and `fixtures/...`).
- `internal/index/indexer.go:691-702` `storeKey` — preserves whatever case the caller passed.

**Reproducer (this lane):**

```bash
# Real project root has lowercase "fixtures/"
mkdir -p fixtures/L8_scratch/case
cat > fixtures/L8_scratch/case/PrefixCase.go <<'EOF'
package casecheck
func L8PrefixCase() {}
EOF

# Hook fires twice — once with caller-supplied uppercase FIXTURES, once with the
# correct lowercase fixtures. On APFS both resolve to the same dir.
python3 harness/hook.py post-edit Write FIXTURES/L8_scratch/case/PrefixCase.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/PrefixCase.go

# Two rows. Two verify_symbol matches.
sqlite3 .leonard/leonard.db "select path from files where path like '%PrefixCase%';"
# FIXTURES/L8_scratch/case/PrefixCase.go
# fixtures/L8_scratch/case/PrefixCase.go
```

The threat surface is subtly different from F031: F031 is the **basename** case varying (`Camel.go` vs `camel.go`); F032 is the **directory prefix** case varying (`FIXTURES/...` vs `fixtures/...`). The two paths concatenate with the project root via `filepath.Join`, and the contained-check at `ResolveSafe` indexer.go:543-549 uses `filepath.Rel` which is case-sensitive in its lexical-containment branch — but `EvalSymlinks` (called at the secondary check, line 553-561) succeeds for both forms because APFS handles case insensitively at the OS layer. So both pass.

**Why this is MEDIUM:**
- Same shape as F031 (duplicate rows), different attack surface. A Claude Code session that the model runs from a path which differs in case (e.g. `cd $WORK/Fixtures` after the operator set up the project under `fixtures/`) emits hook payloads with the case-shifted prefix; every subsequent edit writes a duplicate row.
- The model has direct control over the form via `cwd` and the `file_path` payload field. So a confused step (model invents an uppercase-fixtures path in a follow-up) inflates the DB silently.
- Could be filed as part of F031's fix — the same "always lowercase storeKey on case-insensitive FS" closure covers both. Filing it separately because the failure mode is also testable independently (the basename-case test from F031 wouldn't surface the prefix-case bug if storeKey lowercased only the basename).

**Fix shape:** Same as F031. The single-fix closure: `runtime.GOOS == "darwin"` (and `windows`) → lowercase the **entire** rel-from-root storeKey, not just the basename. Same regression test should cover both cases — assert (a) basename-case-variant writes produce 1 row, (b) prefix-case-variant writes produce 1 row.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 8.

---

---

## F033 — Embedded NUL / newline / CR in `file_path` still pass ResolveSafe (LOW)

**Files:**
- `internal/index/indexer.go:526-589` `ResolveSafe` — no `strings.ContainsAny(claimed, "\x00\n\r")` guard.
- `internal/hooks/post_edit.go:188` `strings.TrimSpace(payload.ToolInput.FilePath)` — trims outer space only; internal whitespace and control bytes survive.

**Bughunt-4 F6 referenced:**
> "Reject any claimed path containing a NUL or newline byte. These can't appear in any legitimate Unix or Windows pathname."
> "Suggested fix shape: At the top of ResolveSafe... `if strings.ContainsAny(claimed, "\x00\n") { return "", false }`."

**Closure verification:** the suggested fix was not landed. Current `ResolveSafe` accepts these bytes.

**Reproducer (this lane, three control-byte variants):**

```python
# All three pass ResolveSafe; OS rejects at stat
fp_cases = [
    "fixtures/L8_scratch/nul\x00../../../etc/passwd",
    "fixtures/\n../../etc/passwd",
    "fixtures/\r../../etc/passwd",
]
# For each: post-edit returns:
#   "leonard: <abs path with embedded byte> file not found, skipping re-index"
# meaning ResolveSafe returned ok=true. The byte propagates into stdout/stderr
# verbatim.
```

For the **NUL case** specifically: input `fixtures/L8_scratch/nul\x00../../../etc/passwd` cleans to `<project>/fixtures/etc/passwd` (the `nul\x00..` is one path segment with NUL in the middle, then three `..` peel back to project + `fixtures` + `etc/passwd`). The resolved abs is **inside** the project root, so no actual escape. ResolveSafe returns ok=true. The system message:

```
leonard: /Users/jasondillingham/Documents/Homelab/projectdogwalker/fixtures/etc/passwd file not found, skipping re-index
```

The NUL never reaches the OS stat because Clean has consumed the segment containing it — but only because the path happens to lexically clean to an in-root location. A different NUL placement that didn't end up cleaned away would propagate to `os.Stat`, which returns "invalid argument" rather than "no such file" (since POSIX paths can't have NULs).

For the **newline/CR cases**: the byte survives Clean (Clean treats newline/CR as ordinary characters inside a single path segment). Output systemMessage and additionalContext carry the raw byte (` ` / `\n` / `\r` in JSON, depending on the encoder).

**Why this is LOW (not MEDIUM):**
- No exploit primitive on v0.52 — the resolved path always lands either (a) inside root (so no escape) or (b) outside root (so caught by the lexical containment check before bytes matter). The bytes themselves can't be used to smuggle past the guard in either direction.
- Operator-visible noise only: the bytes propagate into Claude-Code-rendered systemMessage strings, into JSON-RPC `additionalContext` fields, and (if a real file with `\r` in its name happened to exist on disk and get matched) would land in DB `path` columns where downstream tools handle them awkwardly.
- A future change to `filepath.Clean` semantics, or to how `os.Stat` resolves on a future Go version / future Darwin filesystem, could turn this latent class active. A 4-character defense (`ContainsAny`) closes the class for free.

**Fix shape:**
- At the top of `ResolveSafe` (indexer.go:528, after the empty guard):
  ```go
  if strings.ContainsAny(claimed, "\x00\n\r") {
      return "", false
  }
  ```
- Same treatment on `root` for symmetry.
- Extend the existing `TestResolveSafe_*` table-test in `internal/index/indexer_test.go` with the three control-byte cases — explicit `ok=false` expected.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 10. Re-verifies bughunt-4 F6 still applies post-v0.52.

---

---

## F034 — `leonard check` does not detect facts.yaml contradictions (MEDIUM)

**Files (likely):**
- `internal/adapters/groundtruth/check.go` — the file-check entry the `leonard check` CLI calls
- `internal/adapters/groundtruth/detector.go` (or wherever the do-not-claim / facts paths diverge)

**Observed.** Wrote `fixtures/L7_scratch/contradicts_facts.md`:
```markdown
# Contradicts facts.yaml but NOT in do-not-claim.md
The team has 99 engineers.
Bosun has 4 tools.
Leonard ships 100 MCP tools.
```

`facts.yaml` says `team.engineers=12`, `bosun.tool_count=9`, `leonard.mcp_tool_count=14`. Every line is a fact-contradiction. `leonard check` says:
```
/.../contradicts_facts.md: clean (no findings)
exit=0
```

**Why this matters (dogfood lens).** DOGFOOD #1 was explicit:

> "Concrete example, 2026-05-27 session: re-opened a May 23 LiveKit cover letter with 6 stale claims (bosun `8-tool`→`9`, Leonard older release count → current `46`, Leonard `2 security reviews`→`4`, Offboarding `12 tools`→`11`, PR #104 `awaiting review`→`MERGED`, missing PR #19755 entirely). All 6 catches were manual."

The HEAD-fresh `leonard check` command **only catches claims listed verbatim in `do-not-claim.md`**. Stale claim drift against `facts.yaml` (the thing the operator was hoping to catch) passes through silently. To get DOGFOOD #1 closed, the operator currently has to *manually copy every facts.yaml value into do-not-claim.md as a forbidden bullet*, which defeats the point of having a typed `facts.yaml`.

**Fix shape.** A new finding kind — `[STALE]` — surfaced when prose contains a number/string that is **close to** a `facts.yaml` value but doesn't match. Two strategies:
- **Symmetric**: for each numeric leaf in `facts.yaml`, detect any number-in-prose that's "close" (within 1 OOM, or within 50%) but ≠ the canonical value.
- **Key-context**: detect `[N] engineers`, `[N] tools`, etc. — phrase patterns linked to facts.yaml keys via a `[fact_aliases]` config block (`team.engineers: ["engineers", "engineering team", "headcount"]`).

The current "operator must duplicate every fact into do-not-claim.md" UX is the kind of friction that gets DOGFOOD #1 closed prematurely.

**Discovered.** 2026-05-27 — L7-A.

---

---

## F035 — `leonard facts diff` requires git, exits 0 on failure (MEDIUM)

**File:** `cmd/leonard/facts_diff.go` or wherever the `git diff HEAD -- .leonard/ground-truth/facts.yaml` invocation lives.

**Observed.** projectdogwalker is not a git repository. Running:
```
$ leonard facts diff
git not available or facts.yaml is not tracked: git diff HEAD -- .leonard/ground-truth/facts.yaml: exit status 1
facts.yaml path: /Users/.../.leonard/ground-truth/facts.yaml
$ echo $?
0
```

**Why this matters (dogfood lens).** DOGFOOD #5 ("no fact-diff propagation") expected `facts diff` to *be* the closing feature. In a project where the operator hasn't committed `.leonard/` to git (which is the recommended pattern — `.leonard/leonard.db` is gitignored, sometimes the whole dir is), the command emits an error message but exits 0. A scripted CI consumer that runs `leonard facts diff && deploy` would treat the soft-failure as success and skip the propagation step.

Worse: the error message says *"git not available or facts.yaml is not tracked"* — those two failure modes have very different fixes (install git vs `git add`), so the operator gets generic advice and has to debug.

**Fix shape.** Either:
- **Different exit code**: exit `>=3` on "git not available" / "not tracked" — operator scripts can detect. (Currently exits 0 — indistinguishable from "no diff".)
- **Diagnose which failure**: split the error into the two cases ("git binary not on PATH" vs "file is not tracked in this repo") and recommend a specific fix.
- (Note: the `.bak` file next to `facts.yaml` here is from L5's `sed -i.bak` probe, not a Leonard-managed snapshot — so a `.bak`-fallback strategy would need Leonard to start writing its own snapshot file on every facts.yaml edit, which is a larger design change.)

**Discovered.** 2026-05-27 — L7-B.

---

---

## F036 — `leonard facts impact <key>` scans operator-internal dirs + value-only matching (MEDIUM)

**Files:**
- `cmd/leonard/facts_impact.go` — walker
- Probably reuses the same walk pattern as `list-stale-claims` (which has the F018 exemption gap)

**Observed.** Running `leonard facts impact team.engineers` (with `facts.yaml#team.engineers=12`) reports:
```
## team.engineers = "12"
  RED-TEAM-PLAN.md:7  (3 occurrences)
  findings/FINDINGS.md:1  (13 occurrences)
  findings/L7-findings.md:1  (1 occurrence)
  fixtures/prose/mixed_claims.md:7  (1 occurrence)
  fixtures/prose/uses_facts.md:1  (1 occurrence)
  runlog/run-2026-05-27-L2-cap-edges.md:289  (8 occurrences)
  runlog/run-2026-05-27-L4-concurrency.md:392  (2 occurrences)
  runlog/run-2026-05-27-L5-groundtruth.md:1104  (10 occurrences)
```

Two compounding UX problems:
1. **Operator-internal dirs scanned** — `runlog/`, `findings/`, `RED-TEAM-PLAN.md` (the harness's own audit trail). Same root cause as F018, different code path. Without a `.leonardignore` mechanism, every audit-keeping operator gets noise.
2. **Value-only matching** — the value is `12`. `RED-TEAM-PLAN.md` line 7 contains "Bughunt round #12" (the bughunt-round number, not engineer count). `FINDINGS.md` has 13 occurrences of "12" because Leonard's own bughunt round is named with "12". None of these are about engineers. Without **key-context awareness** ("the word 'engineers' near a 12") the report mixes signal with grep-noise.

**Why this matters (dogfood lens).** DOGFOOD #5's "Show which artifacts reference the fact" hope was specifically to close the manual-grep loop. Today's `facts impact` *is* a manual grep with extra steps — the operator still has to read every match to see if it's actually about engineers.

**Fix shape.**
- Inherit the `.leonardignore` / `scan_exclude` mechanism proposed in F018; default-exclude `runlog/**`, `findings/**`, `audits/**`.
- Add key-context awareness: require the fact KEY (or an alias from a `[fact_aliases]` config block) to appear within N tokens of the value. `12 engineers` matches, `bughunt-12` does not.
- Or: report `weak_match` vs `strong_match` and color/sort accordingly.

**Discovered.** 2026-05-27 — L7-B.

---

---

## F037 — `list-stale-claims --scope` glob behavior (LOW)

**File:** `cmd/leonard/list_stale_claims.go` — glob expansion

**Observed.** Three glob calls against the same project (all 5 `.md` files inside `fixtures/L7_scratch/`):

| Scope | Files matched | Findings | Exit |
|---|---:|---:|---:|
| `fixtures/L7_scratch/**/*.md` | 0 | 0 | 0 |
| `fixtures/L7_scratch/*.md`    | 5 | 6 forbidden | 2 |
| `fixtures/**/*.md`            | 6 | 14 (forbidden+unverified) | 2 |

The `**` doublestar at `fixtures/L7_scratch/**/*.md` matches zero files because there's no subdirectory under `L7_scratch/` for the `**` to traverse — but the operator would reasonably expect "match `.md` files in this dir and its subdirs" (the common operator mental model for `**`). The help text example (`docs/**/*.md`) reinforces that expectation. **Exit 0 with zero output is indistinguishable from "scoped, all clean."**

**Why this matters (dogfood lens).** DOGFOOD #3's whole point is `--scope=applications/*/cover-letter.md` to bound the scan. An operator who writes `--scope=applications/**/*.md` (the natural recursive form) gets zero output and concludes "all clean!" — wrongly.

**Fix shape.**
- Switch to `github.com/bmatcuk/doublestar` (or the std-lib equivalent) so `**` recurses across zero-or-more path segments.
- When `--scope` matches zero files, exit non-zero (e.g. 3) and print `leonard: scope matched 0 files — pattern may be wrong`. Distinguishes "I checked nothing" from "I checked everything, nothing forbidden."

**Discovered.** 2026-05-27 — L7-C.

---

---

## F038 — Superseded decisions are not visually marked (LOW)

**Files:**
- `internal/store/decisions.go` — schema likely has a `superseded_by` column already (the `supersede_decision` tool description says "Links the old row to the new one")
- `cmd/leonard/decisions_list.go` — display
- `internal/mcp/server.go` — `get_decisions` handler

**Observed.** After `record_decision({topic:"L7-...", choice:"first"})` (id=1) and `supersede_decision({decision_id:1, new_choice:"second"})` (returns new_decision_id=2):

```
$ leonard decisions list
leonard: 2 decision(s)
  #2  2026-05-27T22:03:32-05:00  L7-dogfood-skip-deep-dive → skip-but-extend
      DOGFOOD #6 turned out richer than expected
  #1  2026-05-27T22:03:00-05:00  L7-dogfood-skip-deep-dive → skip
      L7 lane focuses on UX friction; deeper protocol probes are L6 territory
```

Both entries appear, same topic, no marker that #1 was superseded. The MCP `get_decisions` response is the same — no `superseded_by` / `is_current` field.

**Why this matters (dogfood lens).** SessionStart shows prior decisions; both `→ skip-but-extend` and `→ skip` appear inline under the same topic. Future Claude reading the SessionStart `additionalContext` sees two contradictory choices and has no signal which is current. The whole point of the supersede tool — to give the LATER decision authority — is invisible.

**Fix shape.**
- Schema: add `superseded_by_id INTEGER` column to decisions (likely already present per the tool description's "Links the old row to the new one").
- `decisions list` display: mark superseded entries with strikethrough or a `[SUPERSEDED by #N]` annotation. Or: hide them by default and add `--all` to show.
- MCP `get_decisions` output: add `superseded_by` field to the structured content; default response should filter superseded entries unless `include_superseded: true`.

**Discovered.** 2026-05-27 — L7-H.

---

---

## F039 — `get_unverified_claims` mixes auto-claims and operator-claims (MEDIUM)

**Files:**
- `internal/store/claims.go` — schema
- `internal/mcp/server.go` — `get_unverified_claims` handler
- `cmd/leonard/claims_unverified.go` — CLI display

**Observed.** After the L8 lane's path-trust probes filed dozens of auto-generated claims of the form:
```
#57  tool=Write file=/Users/.../fixtures/L8_scratch/aaaaaa...aaaa/foo.go;
     index=failed; go vet=skipped (no go.mod)
```
… then this lane recorded a single operator claim (`{"claim":"L6 lane is running in parallel"}`).

`leonard claims unverified` returns 4 claims, intermixed — 3 with multi-kilobyte path strings, 1 with a real message. The CLI output is a wall of `a`s that overflows any reasonable terminal width.

`get_unverified_claims` (MCP) returns the same 4, no distinction in shape.

**Why this matters (dogfood lens).** The Stop hook surfaces unverified claims at session end. If the operator has a few intentional claims (`record_claim verified=false`) and dozens of post-edit-failure auto-claims from a path-edge-case probe, the intentional ones drown in the auto-noise. There's no `source: "auto" | "operator"` field, no `kind: "post_edit_failure" | "explicit"` discriminator.

**Fix shape.**
- Add `source` column to `claims`: `"auto"` for hook-generated, `"operator"` for `record_claim`.
- Default `get_unverified_claims` to `source: operator`; pass `include_auto: true` to retrieve both.
- Auto-claims should also auto-resolve after N sessions (see F045) so they don't accumulate.

**Discovered.** 2026-05-27 — L7 (observed while testing decision/claim roundtrip).

---

---

## F040 — `get_unverified_claims` drops the `evidence` field (LOW)

**File:** `internal/mcp/server.go` — `get_unverified_claims` handler's output shape

**Observed.** `record_claim` accepts:
```json
{"claim":"...","evidence":"...","verified":false}
```
But `get_unverified_claims` returns only:
```json
{"id":42,"claim":"...","recorded_at":...,"session_id":""}
```

No `evidence` field in the response. Verified by reading the structured content of the live call.

**Why this matters (dogfood lens).** The evidence is the *whole point* of the claim ledger — it's the supporting context for verifying the assertion later. Dropping it on the read path forces the operator to either remember what they put as evidence or do a separate lookup. The CLI (`claims unverified`) DOES show evidence (`leonard claims unverified` printed `red-team plan paragraph mentions parallel L6/L7/L8` under the claim) — so the data is there, just not exposed via MCP.

**Fix shape.** Add `evidence` to the `get_unverified_claims` output shape. Same for any future `get_claim_by_id` / `get_claims_for_session`.

**Discovered.** 2026-05-27 — L7-I.

---

---

## F041 — `leonard doctor` reports the F003-capped file count (MEDIUM)

**Files:**
- `cmd/leonard/doctor.go`
- Almost certainly calls `Store.ListFiles("", "")` for its "files: N total" line — same root cause as F003 and F020

**Observed.**
```
$ leonard doctor
leonard: project health
  store:        /Users/.../.leonard/leonard.db
  last indexed: 2026-05-27T22:04:43-05:00  (39s ago)

Index
  files:    1000 total
    go           1000
  symbols:  1000 total
    go           1000
...
```

Database actually has **1527 files**, 2166 symbols (verified via `sqlite3` direct in F003). Doctor lies the same way the indexer's "indexed 1000 file(s)" lies.

**Why this matters (dogfood lens).** `leonard doctor` is *the* command an operator runs to answer "is my Leonard install healthy?" It's the canonical health check. If the operator's project is on the wrong side of the cap, doctor reports a number that's an order of magnitude off — and the operator (correctly) suspects something is broken. The same fix that closes F003+F020 closes F041.

The symbols line is similarly capped at 1000. The CLAUDE.md notes the project is supposed to handle 250–2000 files; doctor will lie for the upper half of that range.

**Fix shape.** Same as F003: switch to `SELECT COUNT(*)` for the totals (or to an uncapped iterator), and never use the safety-capped query for reporting purposes.

**Discovered.** 2026-05-27 — L7-K.

---

---

## F042 — DOGFOOD #6 is partially closed by `suppressOutput:true` but not fully (MEDIUM)

**Files:**
- `internal/hooks/post_edit.go` — emits the JSON payload with `suppressOutput:true`
- `internal/hooks/...` — emits `systemMessage` regardless

**Observed.** Five rapid `Write` operations in this lane. Each post-edit hook returned:
```json
{
  "continue": true,
  "suppressOutput": true,
  "systemMessage": "leonard: re-indexed /.../fixtures/L7_scratch/edit_seq_3.md (go vet skipped, no go.mod)"
}
```

Claude Code's `suppressOutput:true` semantic suppresses the hook output from the main user surface — that's the fix DOGFOOD #6 wanted. But `systemMessage` IS still displayed (per Claude Code's hook spec — system messages get appended to the conversation regardless of suppressOutput). 

**Empirical confirmation** (added 2026-05-27 after advisor pushback that this was speculative): created `/tmp/L7_govet/` with a `go.mod` and a `main.go` containing a vet-warning (`var x int  // unused`), ran `leonard init`, then fired post-edit. Response:
```json
{"continue":true,
 "systemMessage":"leonard: re-indexed /tmp/L7_govet/main.go, go vet reported issues — claim recorded as unverified",
 "hookSpecificOutput":{
   "hookEventName":"PostToolUse",
   "additionalContext":"Leonard post-edit check on /tmp/L7_govet/main.go:\n- go vet FAILED — the edit you just made did not pass the project verifier. Do not claim this work is done until go vet is clean.\n  first error: vet: ./main.go:6:9: declared and not used: x"}}
```
**Both `systemMessage` AND `additionalContext` are emitted.** On a Go project, that's per-edit conversation surface noise — and rightly so when vet failed. The dogfood problem is that the *success* path also emits a systemMessage (`re-indexed X, go vet skipped`) — that's the volume DOGFOOD #6 was complaining about.

**Why this matters (dogfood lens).** DOGFOOD #6 was about Jason's job-hunt sessions where 5–10 cover-letter edits happen in sequence. The fix is "default to no stdout, only print on actual failures or forbidden-claim catches." Currently:
- yes: stdout suppressed
- no: systemMessage emitted every time
- no: no "only print on failure" path — the success message ("re-indexed X") fires on every edit even when there's nothing to say

**Fix shape.** Move the success message to a project-local `~/.leonard/session.log` file. Only emit `systemMessage` on:
- forbidden-claim detected (mandatory — DOGFOOD #1 territory)
- `go vet` / verifier failure
- index error
Successful re-index should be silent.

**Discovered.** 2026-05-27 — L7-E.

---

---

## F043 — `verify_symbol` exact-miss returns false with no "did you mean..." (LOW — but highest dogfood ROI)

**File:** `internal/mcp/server.go` — `verifySymbol` handler

**Observed.**
```
$ leonard verify BulkFunc1
leonard: no match for "BulkFunc1"

$ # but find_symbol with substring works:
$ python3 harness/mcp.py call find_symbol '{"query":"BulkFunc","limit":3}'
{matches: [BulkFunc0000, BulkFunc0001, BulkFunc0002]}
```

The verify endpoint does exact-name (or LIKE+exact) lookup. When a user (or Claude itself) types `BulkFunc1` thinking that's the symbol name but the real name is `BulkFunc0001`, the response is `exists:false` with no hint.

**Why this matters (dogfood lens).** This is **the single highest-leverage UX win in this lane**. Leonard's stated job (per README) is preventing fabricated symbol references. The most common case isn't a fully fabricated symbol — it's a slightly-wrong name (`getUserById` vs `getUserByID`, `BulkFunc1` vs `BulkFunc0001`, `parseConfig` vs `parse_config`). Today the verify call says `false` and Claude moves on. If verify returned `{exists: false, did_you_mean: ["BulkFunc0001", "BulkFunc0002"]}`, the model could correct itself in one turn instead of zero.

**Fix shape.** When `verify_symbol` returns `exists:false`, run a fallback `find_symbol`-style LIKE query (substring or trigram) and include up to 5 closest matches as `suggestions`. Same store query, just don't gate it behind the exact-match decision.

Tune: only show suggestions if the LIKE-match returns no more than 10 results — above that, the suggestions are noise and you'd be better off telling the operator to use `find_symbol` directly.

**Discovered.** 2026-05-27 — L7-J. (Suggested as the single feature most likely to make Leonard feel like a game-changer for daily Claude use.)

---

---

## F044 — `--limit 0 = default 200` repeats the F005 sentinels pattern (LOW)

**File:** `cmd/leonard/truth_history.go` — `--limit` flag help text

**Observed.**
```
$ leonard truth-history --help
...
      --limit int         cap the number of entries returned (0 = default 200)
```

Same "0 means default N" UX pattern as `find_symbol limit=0` (F005), where the documented behavior was "unlimited" but the implementation honored a 50-row floor. truth-history's flag is more honest (says "default 200" not "unlimited"), but the sentinels-with-floors UX is still inconsistent across the codebase. Operators learn "0 = unlimited" once and then get burned by every command that doesn't honor it.

**Why this matters (dogfood lens).** Consistency of sentinel semantics is a low-grade quality concern, but it's the kind of thing that erodes trust over time. Best fix is consistent **negative-number sentinels** (`-1` = unlimited) plus a max cap separately documented, like Postgres / GNU tools.

**Fix shape.** Pick a project-wide convention:
- `0` = default (current truth-history)
- positive N = exactly N
- negative N (or absent) = unlimited up to MaxRows
… and apply consistently in `find_symbol`, `list_files`, `recent_changes`, `truth-history`, etc.

**Discovered.** 2026-05-27 — L7-L (help-text review).

---

---

## F045 — Auto-claims accumulate forever; no TTL or auto-resolve (LOW)

**File:** `internal/store/claims.go` — claim retention policy (likely absent)

**Observed.** L8's path-trust probes filed multiple post-edit-failure claims for path lengths > 4000 chars. After L8 ended, the claims remain. `leonard claims unverified` still lists them in subsequent lanes. There's no retention mechanism, no "auto-resolve if file no longer references the claim," no "expire after N sessions."

**Why this matters (dogfood lens).** Over weeks of use, the claim ledger fills with stale auto-generated entries — every transient `index=failed` becomes a permanent ledger row. The Stop hook output (which surfaces unverified claims) becomes more noise than signal over time.

**Fix shape.** Two mechanisms:
- **TTL on auto-claims**: rows with `source: "auto"` (see F039) expire after 14 days or 50 sessions.
- **`leonard claims resolve --auto`**: bulk-resolve auto-claims older than N days.
- The current `claims resolve` subcommand exists but takes a single ID at a time — at 200+ auto-claims, that's not usable.

**Discovered.** 2026-05-27 — L7-I.

---

---

## F046 — Bash matcher of pre-edit hook does not inspect command for forbidden content; post-edit hook on file body also silent (HIGH — PROMOTED)

**Files:**
- `internal/hooks/pre_edit.go` — Bash branch: receives `tool_input.command` but does not run the ground-truth detector against it
- `internal/hooks/post_edit.go` — only re-indexes; does not call ground-truth check on the file body
- `internal/adapters/groundtruth/check.go` — exists for `leonard check <file>` but not invoked from either hook

**Observed — Bash pre-edit matcher does NOT block forbidden content** (this is the promotion trigger). With ground-truth trust granted (`leonard config trust ground-truth --yes` — i.e. blocking mode on), all three of these `Bash` tool calls pass:

```
$ python3 harness/hook.py pre-edit Bash /tmp/x "cat > /tmp/foo.md << 'EOF'
> We have done 52 releases
> EOF"
{"continue":true}

$ python3 harness/hook.py pre-edit Bash /tmp/x "echo 'We have done 52 releases' > /tmp/bad.md"
{"continue":true}

$ python3 harness/hook.py pre-edit Bash /tmp/x "sed -i 's/old/52 releases/' fixtures/L7_scratch/sample.md"
{"continue":true}
```

All three return `continue:true` with no `permissionDecision:deny`. The same forbidden claim sent through the `Write` tool DOES deny — but the equivalent Bash command lands silently. The hooks config matcher includes `Bash` (per the Homelab CLAUDE.md template: `matcher: "Edit|Write|MultiEdit|NotebookEdit|Bash"`), so the hook IS firing — it just doesn't inspect the command for forbidden text.

**Observed — Post-edit hook on the resulting file is also silent.** Wrote the file via shell here-doc with three known-forbidden phrases from `do-not-claim.md`, then fired post-edit:
```
$ python3 harness/hook.py post-edit Write fixtures/L7_scratch/post_forbidden.md "x"
{"continue":true,"suppressOutput":true,
 "systemMessage":"leonard: re-indexed /.../post_forbidden.md (go vet skipped, no go.mod)"}
```
Three forbidden claims sit on disk; the post-edit hook only re-indexes. Both checkpoints (pre-Bash, post-anything) miss the bypass.

`leonard check` on the same file does flag everything correctly — the detector works; it just isn't wired into the hook path.

**Why this is HIGH (PROMOTED from LOW per advisor review).** The task explicitly identifies "a hook that DOES NOT block what it should" as the HIGH-severity criterion for this lane. The pre-edit hook's *job* is to prevent forbidden content from reaching disk; the Bash matcher fires (so the hook config is correct) but the detector isn't called on the Bash command body. This is the operator's only blocking checkpoint for the Bash-write path — and it's empty.

**Important nuance — this is "extend, not add":** the Bash matcher branch DOES exist in `internal/hooks/pre_edit.go` (lines 198-201, `bashTouchesLeonardDir`). v0.50 specifically closed bughunt-7 F2 ("pre-v0.50 Bash payloads bypassed the guard") for the `.leonard/` path-trust check — meaning the `Bash` matcher fires deliberately for that one purpose. The code is structured to inspect the Bash command string, just for a different concern (path) than the ground-truth content concern this finding raises. The fix is not "add Bash to the matcher" — it's "extend the Bash branch's inspection to include the ground-truth content detector, on top of the existing `bashTouchesLeonardDir` check."

The "single Edit/Write pre-edit hook is enough" assumption fails any time Claude Code reaches for Bash. Common cases:
- `Bash(cat > file <<EOF ... EOF)` — heredoc creation
- `Bash(sed -i 's/old/new/' file)` — in-place edit
- `Bash(echo … > file)` / `Bash(printf … >> file)` — quick append/overwrite
- `Bash(mv tmp file && rm tmp)` — atomic write via mv
- Test setup scripts (`make`, `npm run setup`) that write generated files

In every case the *content* might contain a forbidden phrase from `do-not-claim.md`, and the hook chain is silent.

**Fix shape (in priority order).**
- **Pre-edit Bash matcher**: when `tool_name == "Bash"`, scan `tool_input.command` for any of the do-not-claim rules. False-positive risk is real (a forbidden phrase in `grep` arg is just a search, not an assertion) — so prefer DENY only when the command contains shell redirection (`>`, `>>`) or known mutation patterns (`sed -i`, `tee`, `mv … <project-tree-path>`). For ambiguous cases (no redirection visible), `systemMessage` warn rather than deny.
- **Post-edit file-body check**: after the re-index, run the ground-truth check against the file body (not just `tool_input.content`). Emit `systemMessage` on findings — don't deny (the file is already on disk), but record the claim and surface it.
- **Cross-tool consistency principle**: define the project-level rule once, enforce at every checkpoint. Currently the rule fires for Write/Edit/MultiEdit pre-edit but is silent everywhere else.

**Discovered.** 2026-05-27 — L7-G (initial observation as LOW). **Promoted to HIGH 2026-05-27** after advisor flagged that the Bash matcher had not been tested for content inspection; reproduced three bypass paths, all passed without deny.

---

## Positive observations (not findings, but worth recording)

- **`leonard config trust ground-truth`** is well-designed: stores marker file outside `.leonard/` so a `.leonard/`-write attack can't poison it (matches the SECURITY.md threat model). The dry-run text before granting is operator-friendly. Single complaint: trust state isn't surfaced in `leonard doctor` — operator can't see "ground-truth is trusted" without re-running `leonard config trust`.
- **Pre-edit's Edit-tool semantics** correctly compare `old_string` (being removed) vs `new_string` (being added) — forbidden text in `old_string` doesn't block the edit, which is exactly the right behavior for *removing* a forbidden phrase. Tested explicitly and it works.
- **The pre-edit hook rejection message for `.leonard/` paths is well-written**: `"paths under .leonard/ are operator-authored (the user's Leonard wiring and SQLite store live there). If a config change is genuinely needed, the user must edit .leonard/config.toml themselves."` Tells the model exactly what's wrong and where the legitimate edit path is.
- **SessionStart message** has good content (`N files, X forbidden, Y unverified, run \`leonard ...\``). DOGFOOD #4 is largely closed.
- **CLI shows ISO 8601 timestamps with TZ** (`#2  2026-05-27T22:03:32-05:00`) — much better than the MCP's unix int (`recorded_at: 1779937412`). Worth making MCP consistent.

---

---

## F047 — `leonard override` doesn't actually override either filter (MEDIUM)

**Files:**
- `cmd/leonard/override.go` — token-grant command (works)
- `internal/hooks/pre_edit.go` — token-consume path (apparently not wired for ground-truth + .leonard/)
- `$XDG_CONFIG_HOME/leonard/pending-override/<projHash>.<relHash>.json` — token storage (the file IS created)

**Observed.** `leonard override --help` says:

> Records a single-use override token that bypasses path_filters / content_filters for the next matching edit.

Test 1 — override + ground-truth content filter:
```
$ leonard override fixtures/L7_scratch/probe-override.md --once --reason "L7 dogfood probe of override workflow"
leonard: override token granted for fixtures/L7_scratch/probe-override.md
         reason: L7 dogfood probe of override workflow
         token expires in 5m0s; consumed on next matching edit.

$ python3 harness/hook.py pre-edit Write fixtures/L7_scratch/probe-override.md "We have done [forbidden phrase]"
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
 "permissionDecisionReason":"Forbidden claim ... matches rule Stale release counts#1 ..."}}
```
The token was granted; the edit was still denied. Token did NOT bypass the ground-truth rule.

Test 2 — override + `.leonard/` path filter:
```
$ leonard override .leonard/test-override.md --once --reason "test override of .leonard path filter"
leonard: override token granted for .leonard/test-override.md ...

$ python3 harness/hook.py pre-edit Write .leonard/test-override.md "harmless content"
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
 "permissionDecisionReason":"leonard pre-edit: rejected edit to \".leonard/test-override.md\" — paths under `.leonard/` are operator-authored ..."}}
```
Same: token granted, edit still denied. Override didn't help.

The pending-override file IS created in the expected location (`ls "$XDG_CONFIG_HOME/leonard/pending-override/"` shows it) AND **the file is still present after the denial** — confirmed by re-listing the directory after Test 1 and Test 2 both denied:
```
$ ls $XDG_CONFIG_HOME/leonard/pending-override/
09c088227c7b629404660b8c61f52ab4.79c72a780e9262be7ac69202c5d261eb.json
09c088227c7b629404660b8c61f52ab4.a15889a8daa7af3e1981ad13126759a4.json
...
```
The tokens were granted but neither filter calls the consumer before denying. The token-consume code path is unreachable for these two filters in the current build. (The tokens will self-expire after 5 minutes — not a leak, but a wasted operator action.)

**Why this matters (dogfood lens).** Override is documented as the explicit "I know this looks bad but proceed" escape hatch — the workflow an operator reaches for when they genuinely DO need to write a stale-claim phrase (e.g., quoting it in a do-not-claim.md commit message, or documenting historical state). With override silently non-functional, the operator either:
1. Edits `.leonard/config.toml` to disable the trust temporarily (heavy-handed, project-wide)
2. Uses `cat > file` to bypass via Bash (which works — see F046 — but is the WRONG fix)
3. Gives up and edits something else

None of these is the workflow the help text promised. The override exists in v0.8 per the help text but its hook-side enforcement isn't wired for the post-bughunt-11 forbidden-claim filter or the SECURITY.md path filter.

**Fix shape.**
- Wire the pending-override token consumer into the ground-truth pre-edit branch: if a token exists for `<path>` and matches the path-hash, consume it and allow the edit (recording the override-decision to the audit log per the help-text promise).
- Same wiring for the `.leonard/` path-filter branch — though caveats apply (the SECURITY.md threat model may intentionally forbid override of `.leonard/` edits; check before wiring).
- Until wired, update the help text to say "(NOTE: not yet enforced for ground-truth or .leonard/ filters — see issue #N)" so the operator doesn't grant tokens that do nothing.

**Discovered.** 2026-05-27 — L7 (advisor follow-up probe).

---

---

## Round status

**Bughunt-12 is substantively COMPLETE across all 8 designed lanes** (L1–L8 of RED-TEAM-PLAN.md).

| Lane | Sub-tests | New findings | Highest severity |
|---|---:|---:|---|
| L1 (orientation) | — | 2 | LOW |
| L2 (cap edges) | 47 | 7 | MEDIUM |
| L3 (index correctness) | 31 + discriminating test | 1 | **HIGH** (F020) |
| L4 (concurrency + crash) | 22 | **0 — GREEN LANE** | — |
| L5 (v0.53 ground-truth) | ~40 + setup | 10 | **HIGH** (F013) |
| L6 (MCP protocol fuzz) | 33 | 9 | LOW (cluster — all F008 family) |
| L7 (real dogfood) | 12 sections | 14 | **HIGH** (F046 — PROMOTED) |
| L8 (path-trust macOS) | 73 | 3 | MEDIUM |

**Findings total: 46.** Severity mix: 0 CRITICAL, **3 HIGH**, 19 MEDIUM, 24 LOW.

**Highest-ROI fix order** (best leverage per developer-hour):

1. **F020 + F003** — *single fix at the same site* (`Store.ListFiles("", "")` → `Store.IterFiles`/`Store.AllFilePaths` at `indexer.go:394` and `wire_real.go:65`). Closes the HIGH that re-introduces Leonard's #1 failure mode (fabricated-symbol references via stale prune rows) plus the misleading-count MEDIUM together.
2. **L7 F046 HIGH (Bash matcher bypass)** — extend the ground-truth content detector into the Bash matcher's command-inspect path. `cat > f`, `sed -i`, `echo > f` currently bypass *every* forbidden-claim and content-filter rule. Distinct shape from F020.
3. **F013** — partial-load with loud warnings on malformed ground-truth files. One bad story heading shouldn't take down all 3 MCP tools.
4. **F018** — `.leonardignore` / `scan_exclude` for `runlog/`, `findings/`, audit dirs. Without this, every project Leonard touches drowns in self-detection.
5. **L6 error-helper** — one helper that maps every protocol failure to its JSON-RPC spec code closes **F008 + F021 + F025 + F027 + F030** in one change.
6. **F005** — `find_symbol limit=0 → MaxSymbolResults` (or update tool description to match the 50 default).
7. **F014/F015/F016** — detector quality round (fuzz numeric over-match, year/tenure FPs, fuzzy-window word-boundary).
8. **L8 F031/F032** — lowercase `storeKey` on Darwin/Windows. Same change closes both APFS case-only finds.
9. **F019** — `>` → `>=` in `verify_claim` cap check.

**Validated negative-space:** L4 confirmed the SQLite + WAL foundation handles concurrency (N=8 parallel indexes, kill -9 mid-write, in-session concurrent tool calls, two MCP servers same DB) without bugs. Future audits can skip the storage-layer scrutiny.

**Per-lane source files** (audit-format, ready to promote to `~/Documents/Homelab/leonard/audits/bughunt-12-<lane>.md`):
`findings/L6-findings.md`, `findings/L7-findings.md`, `findings/L8-findings.md`. Per-finding details for F021–F047 below are extracted from these files.
