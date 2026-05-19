# Leonard — Bug Hunt #1 Brief

> **Audience:** parallel Claude Code sessions launched by bosun.
> **Format:** investigation-only. Each lane writes a findings markdown. **No code changes this round.** A separate fix round comes after we know what's actually broken.

## Why this round exists

Leonard shipped 12 parallel sessions' worth of code across three phases in two days. Lane-local tests are green but several surfaces never got real integration testing:

- The TypeScript parser is 1119 LOC of hand-rolled scanner — the highest implementation risk in the codebase.
- Hook handlers were unit-tested against synthetic JSON, not real Claude Code payloads.
- The MCP server's stdio transport was never exercised by a real client; all tests use `NewInMemoryTransports`.
- Concurrent access is an open question in DESIGN.md §7 Q7.
- Self-host completeness is unverified — false negatives are silent.

Each lane probes one surface and reports findings. No fixes yet — we want to see the whole landscape before deciding what to fix.

## Output format (applies to every lane)

Write your findings to `bughunt-1-<lane>.md` at the repo root. Use this structure:

```markdown
# Bug Hunt #1 — <lane>

## Summary
One paragraph: what you investigated and the headline result.

## Findings
For each bug, edge case, or smell:

### F1 — short title
- **Severity:** high / medium / low / informational
- **Reproducer:** exact command, code snippet, or fixture that demonstrates the issue
- **Observed:** what happens
- **Expected:** what should happen (per DESIGN.md, the briefs, or common sense)
- **Suggested fix shape:** sketch — don't prescribe an implementation
- **Out of scope for this investigation:** anything you noticed but didn't probe

### F2 — ...

## Things that worked
Behaviors you specifically verified are correct. Important — knowing what is solid is as valuable as knowing what isn't.

## Open questions
Things you couldn't determine without running the system through a real Claude Code session, or that depend on decisions Jason hasn't made yet.
```

Severity rubric:
- **high** — data loss, panic, wrong-answer that breaks the safety story (e.g., pre-edit hook lets a fabricated symbol through, claims ledger silently drops rows)
- **medium** — wrong behavior that's recoverable (e.g., parser misses a symbol, hook ignores a config knob)
- **low** — cosmetic / minor (misleading log message, suboptimal default)
- **informational** — works fine but worth knowing (e.g., "TypeScript parser handles JSX but not JSX-with-generics")

## Coordination rules

- **No code changes.** Don't edit anything under `cmd/`, `internal/`, or `testdata/`. If you need a fixture to demonstrate a bug, write it inline in your findings markdown using a fenced code block, or describe how to produce it.
- **Scratch files are fine** if you keep them out of version control: write them under `/tmp/`, not the repo. Mention the scratch in your findings if it's useful for the fix round.
- **Don't add Go dependencies.** This is an audit, not implementation.
- **Each lane writes one file.** Filename: `bughunt-1-<lane>.md`. Add it via `git add bughunt-1-<lane>.md && git commit` so bosun sees you ahead and can squash-merge it.
- **No `claim`/`done`/`merge` coordination needed between lanes** — every lane writes a different filename. Conflict-free by construction.

---

## hunt-typescript

You are auditing the TypeScript parser (`internal/parse/typescript.go`, 1119 LOC, hand-rolled scanner) against the kind of TypeScript that real codebases contain.

**Investigation steps:**

1. Read `internal/parse/typescript.go` end-to-end. Note every place a heuristic is used (string stripping, brace counting, regex-based detection). Each heuristic is a candidate failure point.
2. Read `internal/parse/typescript_test.go` and `testdata/typescript/` to see what's already covered.
3. Build a corpus of "scary TypeScript" fixtures *as code snippets in your findings file*, covering at minimum:
   - Template literals with `${...}` interpolation, including nested template literals
   - JSX (`.tsx`) with TS expressions embedded in JSX braces
   - Generic type parameters with complex constraints — e.g. `function foo<T extends Record<K, V>, U = T>(...)`
   - Arrow functions vs function declarations — `const foo = <T>(x: T) => T` (the angle bracket starts a generic, not JSX)
   - Default exports — out of v0 scope per the brief, but the parser should at least *not crash or mis-extract* on `export default function ...`
   - Decorators — `@decorator class Foo` — out of v0 scope but the parser should skip them gracefully
   - Interface body methods — `interface X { y(): void; z: number; }`
   - Classes with static methods, getters/setters, private fields (`#name`)
   - Async/await — `async function`, `async arrow`, `await` inside the body
   - Type predicates — `function isFoo(x: unknown): x is Foo`
   - Conditional types — `type X<T> = T extends string ? A : B`
   - Multi-line strings, comments inside type annotations, trailing commas
4. For each scary fixture, predict what `ExtractTypeScript` *should* return per the brief, then run the parser (you can write a small `main.go` in `/tmp/` that imports `github.com/jasondillingham/leonard/internal/parse` and runs `ExtractTypeScript` — that's a scratch tool, not a code change).
5. Note divergences.

**Output:** `bughunt-1-typescript.md` per the format above.

## hunt-hooks

You are auditing the four hook handlers (`post-edit`, `pre-edit`, `session-start`, `stop`) against Claude Code's actual hook payload contracts.

**Investigation steps:**

1. Read Claude Code's hook documentation for the four hook types. The payload shapes are documented at https://docs.anthropic.com/en/docs/claude-code/hooks (or wherever the current canonical reference is). **Find the actual envelope schema for each.** Note field names, types, and which fields are guaranteed vs optional.
2. For each hook subcommand, read the handler:
   - `cmd/leonard-hook/post_edit.go` + `internal/hooks/post_edit.go`
   - `cmd/leonard-hook/pre_edit.go` + `internal/hooks/pre_edit.go`
   - `cmd/leonard-hook/session_start.go` + `internal/hooks/session_start.go`
   - `cmd/leonard-hook/stop.go` + `internal/hooks/stop.go`
   Audit: does the JSON unmarshalling target match the documented envelope? Watch for snake_case vs camelCase, missing-field tolerance, type mismatches (int vs string), and the existence and source of `session_id` in particular (the `stop` lane explicitly noted this was guessed).
3. For the response shape, audit what each handler writes to stdout. Does it match Claude Code's expected hook response format (`continue`, `stopReason`, `hookSpecificOutput.additionalContext`, `decision` for block/allow, etc.)?
4. Test with synthetic payloads:
   - Craft a *minimal* payload for each hook type that matches the real documented envelope
   - Pipe it into the built binary (`./leonard-hook <subcommand>`) and observe the response
   - Compare to what Claude Code would expect on the other end
5. Failure modes to specifically probe:
   - Missing optional fields
   - Extra unknown fields (forward-compat — does the handler choke?)
   - Unicode in file paths / content
   - Very large `Edit.new_string` (>1 MB) — does pre-edit's `go/parser` cope?
   - `Write` payload with binary content — does anything panic?

**Output:** `bughunt-1-hooks.md` per the format above.

## hunt-mcp

You are auditing the `leonard-mcp` stdio server against a real MCP client over the documented stdio transport.

**Investigation steps:**

1. Read `cmd/leonard-mcp/main.go` and `internal/mcp/server.go` end-to-end. Note that all existing tests use `NewInMemoryTransports` — the stdio transport (`mcp.StdioTransport{}`) is wired but never exercised in CI.
2. Build the binary (`go build -o /tmp/leonard-mcp ./cmd/leonard-mcp` — that's not a code change, just a temp build).
3. Spin up an MCP client (from the same SDK, `github.com/modelcontextprotocol/go-sdk/mcp`) configured to launch the binary via `mcp.NewCommandTransport(exec.Command("/tmp/leonard-mcp"))` or equivalent. Write the harness as a `/tmp/` scratch script.
4. Drive the client through every v1/v2/v3 tool against a real `.leonard/leonard.db` (use the one already at `~/Documents/Homelab/leonard/.leonard/leonard.db` from the smoke test, or `leonard init` a temp project):
   - `verify_symbol`, `find_symbol`, `list_files` (phase 1)
   - `record_decision`, `get_decisions`, `supersede_decision` (phase 2)
   - `record_claim`, `get_unverified_claims`, `recent_changes` (phase 3)
5. For each tool: confirm the wire schema matches what the server advertises (`tools/list`), the input validation rejects malformed input cleanly, and the output structure matches what a Claude Code session would see.
6. Probe failure modes:
   - Tool call with missing required field
   - Tool call with extra unknown field
   - Tool call with wrong type for a field
   - `tools/list` followed by a tool that doesn't exist
   - Server behavior when the DB file is locked / missing / corrupted (simulate by chmod or by removing it mid-session)
   - Stdio close / reopen — does the server cleanly shut down on EOF?
   - SIGINT — does the deferred `st.Close()` in main.go actually fire?

**Output:** `bughunt-1-mcp.md` per the format above.

## hunt-selfhost

Two related investigations bundled — both fast, both high-signal:

### Sub-investigation A: self-host symbol completeness

The phase-1 brief said "Leonard can index itself without crashing or returning fabricated symbol claims." That's negative — *no false positives*. The other direction matters too: *no false negatives*. Verify that every symbol we'd expect to find in Leonard's own source actually shows up in the index.

**Steps:**

1. Run `leonard init .` and `leonard index` against the leonard repo root.
2. For every Go file in `cmd/` and `internal/`, enumerate the symbols Leonard *should* have extracted via `go doc -all <pkg>` or `go list -json -f '{{.Imports}}'` or a tree-walk of `go/ast` (whichever you find cleanest). Compare to what `store.FindSymbolsByName` returns for each expected name.
3. For every Python file in `testdata/python/`, do the same comparison against what the Python fixtures declare.
4. For every TypeScript file in `testdata/typescript/`, same.
5. Note any false negatives. For each: was it skipped by the walker? Mis-extracted by the parser? Persisted but with a wrong qualified_name?

### Sub-investigation B: concurrent access (DESIGN.md §7 Q7)

Verify (or refute) the design-doc claim that "SQLite WAL handles most cases."

**Steps:**

1. From a `/tmp/` scratch script, simulate concurrent post-edit hooks: spawn N goroutines (try N=10, 50, 100), each running `leonard-hook post-edit` with a synthetic payload pointing at a different file in the leonard repo. Use a real `.leonard/leonard.db`.
2. Observe: do any hooks fail with `database is locked` or similar? Are claim rows written for all N? Is the final symbols table correct?
3. Simultaneously, have a second `/tmp/` script poll the MCP server's `find_symbol` while the goroutine storm runs. Do queries return stale or partial results? Does the MCP server ever error?
4. Test with the indexer too: run `leonard index` while a post-edit hook is firing. Any locking issues?

**Output:** `bughunt-1-selfhost.md` per the format above. Bundle both sub-investigations in one doc with clear section headers.

---

## What is NOT in this round

- **Fixes.** No code changes. The follow-up round will plan fixes against the union of findings.
- **Performance benchmarks.** DESIGN.md §7 Q4 (hook latency <200ms) is open but is its own investigation; out of scope here.
- **OSS-readiness audit.** README polish, license headers, package docs — separate work.
- **New languages.** Rust / Swift / Ruby / Java / C/C++ are listed in DESIGN.md §6 as "Future / explicit non-MVP" — not part of any bug hunt round.

If your investigation surfaces something that fits one of those categories, note it in your findings's "Out of scope for this investigation" line and keep moving.
