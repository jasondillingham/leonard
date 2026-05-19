# Leonard — Phase 3 Brief

> **Audience:** parallel Claude Code sessions launched by bosun. Each lane below becomes its own session/worktree/branch.
> **Source of truth:** `DESIGN.md` in this repo, plus `phase-1-brief.md` and `phase-2-brief.md` for the v1/v2 surface already shipped.

## Phase 3 goal

Close out DESIGN.md §6 phase 3:

- **Claim discipline** — MCP tools that let Claude record and review what it's claimed to have done, surfaced at session end via a `Stop` hook.
- **Stale-fact awareness** — a `recent_changes` MCP tool so Claude can ask "what's moved since I last looked?" instead of relying on memory.
- **TypeScript coverage** — second non-stdlib parser, parity with the Python lane from phase 2.

After phase 3, all four hallucination modes from DESIGN.md §1 have at least a v0 defense and Leonard is feature-complete for the planned MVP.

## Phase 3 acceptance criteria (whole project)

A phase-3 build is done when **all of the following** are true:

1. `leonard-mcp` exposes `record_claim` (write) and `get_unverified_claims` (read). They round-trip through the real `store.Store` via `StoreAdapter`.
2. `leonard-mcp` exposes `recent_changes` returning `[{path, indexed_at}]` ordered by `indexed_at DESC`, with optional `since` (unix seconds) and `limit` (default 50, max 500) inputs.
3. `leonard-hook stop` consumes a Claude Code `Stop` hook payload. When the project's store has unverified claims for the current session, the hook emits a response that surfaces them as additional context (same shape as `session-start`'s injection). When zero unverified claims, no-op response with `Continue=true`.
4. The indexer recognizes `.ts` and `.tsx` files (and optionally `.js`/`.jsx` — your call, document the scope). The TypeScript parser extracts: module-level `function`/`const`/`let`/`var` declarations, `class` declarations with methods, top-level `type` and `interface` declarations, and exported names. Verified by an integration test indexing a TS fixture in `testdata/`.
5. **All of phase 1 and phase 2 still works** — every existing MCP tool, hook subcommand, and CLI command keeps its current contract.
6. `go test -race ./... -count=1` is clean across all packages.
7. DESIGN.md §6 "Phase 3" checkboxes are ticked. The §6 "Future / explicit non-MVP" list stays untouched.

## Coordination rules (apply to every lane)

- **Stay in your lane.** Files-owned lists below define the boundary. Use `bosun claim` for anything outside.
- **No regressions to phase 1 or phase 2.** Adding to interfaces is fine; changing existing method signatures, MCP tool schemas, or hook subcommand contracts is not.
- **No global state.** Same rule bosun has for itself.
- **Race-clean is non-negotiable.** `go test -race ./... -count=1` before `bosun done`.
- **Coordination touch in `cmd/leonard-hook/root.go`:** the `stop` lane registers a new subcommand there. No other phase-3 lane touches that file, so no `bosun claim` is needed — but mention it in the commit body for traceability.

---

## claims

Add the MCP tools for the claims surface. The `store.Store` already has `RecordClaim` and `GetUnverifiedClaims` — your job is the MCP layer, the `ClaimStore` interface, and tests. Pattern matches the phase-2 `decisions` lane closely; treat that as your template.

**Files you own (exclusively):**
- `internal/mcp/claims.go` — new file with handler functions and input/output types
- `internal/mcp/claims_test.go` — new file with end-to-end tests via `NewInMemoryTransports`
- `internal/mcp/server.go` — extend `register()` to wire the two new tools (small additive change; coordinate via `bosun claim` if another lane also touches this — none should in phase 3)
- `internal/mcp/adapter.go` — extend `StoreAdapter` with `RecordClaim` / `GetUnverifiedClaims` methods
- `internal/mcp/store.go` — add `ClaimRecord` and `ClaimStore` interface (sibling to `SymbolStore`/`DecisionStore`, same pattern decisions used)

**Tools to implement:**

| Tool | Input | Output |
|---|---|---|
| `record_claim` | `claim: string, evidence: string, verified: bool, session_id?: string` | `{claim_id: int64}` |
| `get_unverified_claims` | `session_id?: string` | `{claims: [{id, session_id, claim, recorded_at}]}` (wrap in a single-field envelope; `evidence` is intentionally omitted from the read shape because it can be large — fetch via a future per-id read if needed) |

Note: `session_id` is opaque to Leonard. The hook lane (`stop`) will pass whatever Claude Code surfaces as the session identifier in its hook payload, so plumb it through faithfully. If the input omits `session_id` on `record_claim`, store `""` and let the caller decide later.

**Acceptance for this lane:**
- Both tools round-trip data via the real `store.Store` (not just the in-memory test fixture).
- `record_claim` with `verified=true` does not show up in `get_unverified_claims`.
- `record_claim` with `verified=false` does.
- `get_unverified_claims` with no `session_id` returns claims across all sessions; with one set, filters to that session.
- `go test -race ./internal/mcp/... -count=1` clean.
- Phase 1/2 tools still pass their tests with schemas unchanged.

## changes

Add the `recent_changes` MCP tool. The store doesn't yet expose a "files indexed since X" query — your job is to add it (in a new store file, not by editing `store.go`) and wire it through.

**Files you own (exclusively):**
- `internal/store/recent.go` — **new file** in package `store` adding `ListFilesIndexedSince(since int64, limit int) ([]File, error)`. Implementation is a single `SELECT path, language, size_bytes, indexed_at FROM files WHERE indexed_at >= ? ORDER BY indexed_at DESC LIMIT ?`.
- `internal/store/recent_test.go` — table-driven unit tests against a `t.TempDir()` DB
- `internal/mcp/changes.go` — new file with the `recent_changes` handler + input/output types
- `internal/mcp/changes_test.go` — end-to-end tests via `NewInMemoryTransports`
- `internal/mcp/server.go` — extend `register()` to wire `recent_changes` (additive)
- `internal/mcp/adapter.go` — extend `StoreAdapter` with `ListFilesIndexedSince(ctx, since, limit) ([]FileRecord, error)`. Reuse the existing `FileRecord` type — no new record shape needed; just add an `IndexedAt` field if it's missing.
- `internal/mcp/store.go` — add `ChangesStore` interface (sibling to the others) declaring `ListFilesIndexedSince(ctx, since, limit) ([]FileRecord, error)`. If `FileRecord` doesn't currently carry `IndexedAt`, add it (this is a back-compat additive change — the field is only set on this path).

**Tool to implement:**

| Tool | Input | Output |
|---|---|---|
| `recent_changes` | `since?: int64 (unix seconds, default 0), limit?: int (default 50, max 500)` | `{changes: [{path, language, size_bytes, indexed_at}]}` |

**Acceptance for this lane:**
- Indexing a project, then mutating two files, then `recent_changes` returns those two files first, in descending `indexed_at` order.
- `since` filter excludes files indexed before the cutoff.
- `limit` is enforced (default 50, hard cap 500).
- `go test -race ./internal/store/... ./internal/mcp/... -count=1` clean.

## stop

Add the `Stop` hook handler that surfaces unverified claims for the current session at session end.

**Files you own (exclusively):**
- `cmd/leonard-hook/stop.go` — Cobra subcommand wiring
- `cmd/leonard-hook/stop_test.go` — unit tests
- `internal/hooks/stop.go` — handler implementation
- `internal/hooks/stop_test.go` — handler unit tests
- `cmd/leonard-hook/root.go` — register the new subcommand (additive; no other phase-3 lane touches this)

**Behavior:**

1. Read a `Stop` hook JSON payload from stdin (Claude Code's documented shape). Extract `session_id` from the payload (whichever field Claude Code uses for this; document in a code comment).
2. Open the store at `<projectRoot>/.leonard/leonard.db`. If missing, emit a no-op response (`Continue=true`) and exit 0 — don't crash a session on a fresh checkout.
3. Call `store.GetUnverifiedClaims(sessionID)`.
4. If zero results, emit a no-op response with `Continue=true` and exit 0.
5. If one or more results, format them as a Markdown block (`## Unverified claims (from Leonard)` + one bullet per claim with a short prefix of the claim text). Emit a hook response whose `hookSpecificOutput.additionalContext` carries the Markdown.
6. **Do not block the stop.** Phase 3 stays advisory — surfacing claims is enough; future iterations can decide whether to block.

Configurable cap: default 20 claims surfaced, configurable via `.leonard/config.toml` key `surface_unverified_claims_at_stop` (extend `config.Config`).

**Acceptance for this lane:**
- Piping a synthetic `Stop` JSON into `leonard-hook stop` against a store with seeded unverified claims emits a response surfacing them.
- Verified-only and empty-store cases produce no-op responses.
- Missing-store case is a clean no-op (exit 0, `Continue=true`).
- Config cap is honored when set; default is 20 when unset.
- `go test -race ./cmd/leonard-hook/... ./internal/hooks/... -count=1` clean.

## typescript

Add a TypeScript (and optionally JavaScript) parser. Second non-stdlib parser; treat the phase-2 `python` lane as your reference for structure.

**Files you own (exclusively):**
- `internal/parse/typescript.go` — symbol extractor
- `internal/parse/typescript_test.go` — unit tests
- `internal/index/indexer.go` — **one line only**: register `.ts`/`.tsx` (and `.js`/`.jsx` if you choose to cover them) in the `langExtractors` table. If you need a wider change here, write a TODO in DESIGN.md and surface it in the PR body.
- `testdata/typescript/` — fixture TS source files for the integration test
- `internal/index/typescript_test.go` — integration test that indexes the fixture and asserts symbol presence (new file, not editing existing tests)

**Add dependencies:**

There is no Go-stdlib TypeScript parser. Realistic options (pick one and document in DESIGN.md §7 Q2 — extend the entry rather than overwrite):

- `github.com/smacker/go-tree-sitter` + `tree_sitter_typescript` — most mature, CGo (carries Windows build pain + static-binary distribution friction)
- WASM tree-sitter via `wazero` — pure-Go, larger binary, less ergonomic but no CGo
- A pure-Go partial parser hand-rolled for symbol extraction only — only consider if both above are blocked; document why

The python lane chose `go-python/gpython` for the pure-Go-no-CGo philosophy; weigh that precedent against TS-grammar maturity in each candidate before you decide.

**Symbols to extract (`.ts` / `.tsx` at minimum):**

- `function name(...)` → `kind = "function"`
- `class Name { ... }` → `kind = "type"`
- Methods inside a class body → `kind = "method"`, qualified as `ClassName.methodName`
- `interface Name { ... }` → `kind = "interface"`
- `type Name = ...` → `kind = "type"`
- Top-level `const`/`let`/`var name = ...` → `kind = "const"` or `"var"`
- `export` modifier sets `Exported = true`; default exports are out of scope for v0

Skip: enums, namespaces, decorators, JSX element type extraction, generic type parameters (just record the simple name).

**Acceptance for this lane:**
- `internal/parse/typescript.go` exposes `ExtractTypeScript(path string, src []byte) ([]store.Symbol, error)`.
- `testdata/typescript/` contains at least two fixtures: one module with top-level declarations + a class + an interface, one with TSX syntax (React-style component if convenient).
- Integration test indexes the fixture and finds every expected symbol via `store.FindSymbolsByName`.
- Hash-gated incremental behavior still holds.
- `go test -race ./internal/parse/... ./internal/index/... -count=1` clean.

---

## What is NOT in phase 3

After phase 3, Leonard has hit DESIGN.md §6's MVP scope. The "Future / explicit non-MVP" list explicitly defers:

- Other languages (Rust, Swift, Ruby, Java, C/C++)
- Cross-file/cross-package symbol resolution
- Decision/claim export/import (OSS positioning concern)
- Web UI for browsing decisions/claims
- Multi-project / global mode

Don't extend into any of those even when the codebase invites it. Same scope-discipline rule bosun applies to itself.

If you finish your lane early and want to do more: improve test coverage in a package you understand, or add benchmarks against the targets in DESIGN.md §7 Q3 (hook performance budget — <200ms p95 for re-index of one file). Both are valuable and stay inside scope.
