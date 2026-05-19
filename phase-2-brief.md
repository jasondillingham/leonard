# Leonard — Phase 2 Brief

> **Audience:** parallel Claude Code sessions launched by bosun. Each lane below becomes its own session/worktree/branch.
> **Source of truth:** `DESIGN.md` in this repo, plus `phase-1-brief.md` (already shipped) for context on the v1 surface.

## Phase 2 goal

Land the next two hallucination defenses from DESIGN.md §1:

- **Drift from prior decisions** — durable decision recording exposed via MCP, plus a `SessionStart` hook that surfaces recent decisions into every new Claude Code session.
- **Fabricated APIs in proposed edits** — a `PreToolUse` hook that rejects edits whose code references symbols that don't exist in any tracked package.

Plus the first cross-language step: a Python parser so the same defenses work outside Go.

## Phase 2 acceptance criteria (whole project)

A phase-2 build is done when **all of the following** are true:

1. `leonard-mcp` exposes `record_decision`, `get_decisions`, and `supersede_decision` over stdio. They round-trip data through the real `store.Store` (no in-memory adapter).
2. `leonard-hook session-start` reads recent decisions from the store and emits a Claude Code `SessionStart` hook response that injects them as additional system content. Configurable cap on N (default 10).
3. `leonard-hook pre-edit` consumes a Claude Code `PreToolUse` payload for an `Edit` or `Write` tool call. When the proposed code adds a reference (function call, type use, exported var) to a symbol that lives in a tracked package and does not exist in the index, the hook returns a block response with a clear reason. Otherwise it returns `allow`. **Tracked packages = packages whose files are indexed by Leonard.** Stdlib + external module references are out of scope (too noisy for v0).
4. The indexer recognizes `.py` files. The Python parser extracts module-level functions, classes, methods (functions defined inside class bodies), and top-level assignments. Verified by an integration test that indexes a Python fixture in `testdata/` and looks up known names with `verify_symbol`.
5. **All of phase 1 still works** — `leonard init`, `leonard index`, `leonard verify`, `leonard-hook post-edit`, and `leonard-mcp` v1 tools (`verify_symbol`, `find_symbol`, `list_files`) behave the same as before the phase-2 work landed.
6. `go test -race ./... -count=1` is clean across all packages.
7. DESIGN.md §6 "Phase 2" checkboxes are ticked. Phase 3 items remain unchecked.

## Coordination rules (apply to every lane)

- **Stay in your lane.** Files-owned lists below define the boundary. Use `bosun claim` for anything outside.
- **No regressions to phase 1.** Adding to interfaces is fine; changing existing method signatures, MCP tool schemas, or hook subcommand contracts is not.
- **No global state.** Same rule bosun has for itself.
- **Race-clean is non-negotiable.** `go test -race ./... -count=1` before `bosun done`.
- **Record decisions you make in DESIGN.md §7.** If you pick a tree-sitter binding, settle a hook block strategy, or decide a default cap value — write it down. Bonus: use `leonard record_decision` once your lane is done to dogfood the new MCP tool.

---

## decisions

Add the MCP tools for the decisions surface. The `store.Store` already has `RecordDecision`, `GetDecisions`, and `SupersedeDecision` — your job is the MCP layer + tests.

**Files you own (exclusively):**
- `internal/mcp/decisions.go` — new file with handler functions and input/output types
- `internal/mcp/decisions_test.go` — new file with end-to-end tests (via `NewInMemoryTransports`)
- `internal/mcp/server.go` — extend `register()` to wire the three new tools (small additive change; no concurrent edits expected from other lanes)
- `internal/mcp/adapter.go` — extend `StoreAdapter` with `RecordDecision` / `GetDecisions` / `SupersedeDecision` methods + matching `DecisionStore` interface extension
- `internal/mcp/store.go` — add `DecisionRecord` and `DecisionStore` interface (one of: extend `SymbolStore`, or add a sibling interface — your call, document the choice)

**Tools to implement:**

| Tool | Input | Output |
|---|---|---|
| `record_decision` | `topic: string, choice: string, reasoning: string` | `{decision_id: int64}` |
| `get_decisions` | `topic?: string, since?: int64, limit?: int (default 20, max 200)` | `[{id, topic, choice, reasoning, recorded_at}]` (wrapped in single-field envelope like v1's `find_symbol`) |
| `supersede_decision` | `decision_id: int64, new_choice: string, new_reasoning: string` | `{new_decision_id: int64}` |

The `topic` filter on `get_decisions` is exact-match for v0. Add a comment marking substring search as phase-3 if you think it's worth it.

**Acceptance for this lane:**
- All three tools land on the running `leonard-mcp` server (verifiable via the SDK's in-memory transport tests).
- Round-trips work against the real `store.Store` via `StoreAdapter` — not just the in-memory test store.
- `go test -race ./internal/mcp/... -count=1` clean.
- Phase 1's three v1 tools still work, with their schemas unchanged.

## sessionstart

Add the `SessionStart` hook handler that reads recent decisions and injects them into a new Claude Code session as additional system content.

**Files you own (exclusively):**
- `cmd/leonard-hook/session_start.go` — Cobra subcommand wiring
- `cmd/leonard-hook/session_start_test.go` — unit tests
- `internal/hooks/session_start.go` — handler implementation
- `internal/hooks/session_start_test.go` — handler unit tests
- `cmd/leonard-hook/root.go` — register the new subcommand (small additive edit; coordinate with `preedit` lane if it touches `root.go` at the same time → use `bosun claim` to serialize that touch)

**Behavior:**

1. Read a `SessionStart` hook JSON payload from stdin (Claude Code's documented shape).
2. Open the store at `<projectRoot>/.leonard/leonard.db`. If missing, emit an empty hook response and exit 0 — don't crash a fresh session that hasn't run `leonard init`.
3. Call `store.GetDecisions("", 0, N)` where `N` defaults to `10` (configurable via the `inject_decisions_at_session_start` key in `.leonard/config.toml` — extend `config.Config` to carry it).
4. Format the decisions as a short Markdown block: a heading like `## Prior decisions (from Leonard)` then one bullet per decision with topic, choice, and a one-line reason.
5. Emit a Claude Code `SessionStart` hook response that injects the Markdown as additional system content. Exit 0.

If there are zero recorded decisions, emit a no-op response (exit 0, no injection).

**Acceptance for this lane:**
- Piping a synthetic `SessionStart` JSON into `leonard-hook session-start` against a store with seeded decisions produces a response whose injected content lists those decisions.
- Empty-store and missing-store cases both succeed with no injection.
- The config knob is honored when set; default is 10 when unset.
- `go test -race ./cmd/leonard-hook/... ./internal/hooks/... -count=1` clean.

## preedit

Add the `PreToolUse` hook handler that rejects edits referencing symbols that don't exist in any tracked package. The most ambitious of the four lanes; the others can ship phase 2 even if you can't.

**Files you own (exclusively):**
- `cmd/leonard-hook/pre_edit.go` — Cobra subcommand wiring
- `cmd/leonard-hook/pre_edit_test.go` — unit tests
- `internal/hooks/pre_edit.go` — handler implementation
- `internal/hooks/pre_edit_test.go` — handler unit tests
- `cmd/leonard-hook/root.go` — register the new subcommand (coordinate with `sessionstart` via `bosun claim`)

**Behavior:**

1. Read a `PreToolUse` JSON payload from stdin for tool `Edit` or `Write` (others: pass through).
2. Pull the proposed new content from the payload — for `Edit` use `tool_input.new_string`, for `Write` use `tool_input.content`.
3. Parse the snippet with `go/parser.ParseFile` in `parser.AllErrors` mode (only for `.go` files in v0; non-Go files pass through). If the snippet doesn't stand alone (e.g., `Edit` to a function body), wrap it in a minimal `package _; func _() { ... }` shim so the parser can chew it.
4. Walk the AST. For every `*ast.SelectorExpr` (`pkg.Name`) or `*ast.CallExpr` where the receiver is an identifier, collect the (package-or-receiver, symbol) pair.
5. For each collected reference, decide if it's **tracked**:
   - Resolve the package from the file's existing imports (read the target file's imports via the store's file row + a quick header parse).
   - Tracked = the package's path is under `github.com/jasondillingham/leonard/` (the current module path — fetch from `go.mod` at startup).
6. For each tracked reference, query `store.FindSymbolsByName(name)` (already exists). If zero matches, mark as fabricated.
7. If any fabricated references, return a block response with a single concise reason that lists them. Otherwise return allow.

**Out of scope (phase 3 candidates, write a TODO in DESIGN.md §6 instead of expanding):**
- Cross-module symbol resolution
- Generic type parameter checking
- Verifying method signatures (only existence is checked in v0)
- Python pre-edit (lane operates on Go only; Python preedit lands when Python coverage matures)
- An override path / `--force` flag — Claude Code's existing hook-deny override is enough for v0

**Acceptance for this lane:**
- Piping a synthetic `PreToolUse` Edit payload that adds `store.NoSuchFunction()` returns a block response naming `store.NoSuchFunction`.
- Piping a payload that adds a real call (e.g. `store.Open(...)`) returns allow.
- Snippets that reference stdlib (`fmt.Println`) or external modules (`github.com/spf13/cobra.Command`) pass through.
- `go test -race ./cmd/leonard-hook/... ./internal/hooks/... -count=1` clean.

## python

Add a Python parser, register it with the indexer, and add a Python fixture for the self-host test.

**Files you own (exclusively):**
- `internal/parse/python.go` — symbol extractor
- `internal/parse/python_test.go` — unit tests
- `internal/index/indexer.go` — **one line only**: register `.py` in the `langExtractors` table. If you need a wider change here, write a TODO in DESIGN.md and surface it in the PR.
- `testdata/python/` — fixture Python source files for the integration test
- `internal/index/python_test.go` — integration test that indexes the Python fixture and asserts symbol presence (this is a new file, not editing the existing `indexer_test.go`)

**Add dependencies:**

Pick one and document the choice in DESIGN.md §7 Q2 (which is still open for non-Go):

- `github.com/smacker/go-tree-sitter` + `tree_sitter_python` — most mature, CGo (carries build complexity on Windows / pure-static-binary distribution).
- A pure-Go alternative — e.g. parsing via the WASM-compiled tree-sitter grammar through `wazero` (no CGo, larger binary). Note: less battle-tested for Python.
- Hand-rolled `compile.Parse`-style approach using only the Go ecosystem (no, don't — that's Python-3.x grammar reinvention).

The decision is yours; document the why in DESIGN.md §7 along the same shape as the lane-1 Go decision.

**Symbols to extract:**

- Module-level `def name(...)` → `kind = "function"`
- Module-level `class Name(...)` → `kind = "type"`
- Method `def name(self, ...)` inside a class body → `kind = "method"`, with the class as `qualified_name` prefix (`ClassName.method_name`)
- Top-level assignments `NAME = expr` → `kind = "var"`, with `Exported` true when the name is not underscore-prefixed
- Decorated functions/classes: ignore the decorators for v0; just extract the name they wrap

Skip: comprehensions, lambdas, type aliases (Python 3.12 syntax), `__init__` is just another method (don't special-case it).

**Acceptance for this lane:**
- `internal/parse/python.go` exposes `ExtractPython(path string, src []byte) ([]store.Symbol, error)` mirroring the Go extractor's signature.
- `testdata/python/` contains at least two files: one module with top-level defs/classes, one with methods inside classes. The integration test indexes them and finds every expected symbol via `store.FindSymbolsByName`.
- The hash-gated incremental behavior still holds: running `IndexAll` twice in a row re-parses zero Python files the second time.
- `go test -race ./internal/parse/... ./internal/index/... -count=1` clean.

---

## What is NOT in phase 2

From DESIGN.md §6 phase 3:

- `record_claim` / `get_unverified_claims` as MCP tools (the post-edit hook already writes to the claims table; the MCP tool wraps it in phase 3)
- `Stop` hook surfacing unverified claims
- TypeScript parser
- `recent_changes` MCP tool
- Cross-file/cross-package symbol resolution
- Override paths for blocked pre-edits

If you're tempted to add anything from that list, write it into DESIGN.md §6 phase 3 with a one-line rationale and keep moving.
