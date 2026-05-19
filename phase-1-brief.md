# Leonard — Phase 1 Brief

> **Audience:** parallel Claude Code sessions launched by bosun.
> **Source of truth:** `DESIGN.md` in this repo. Read it before starting.

## Phase 1 goal

Ship a working slice of Leonard that kills two of the four hallucination modes:

- **Fabricated APIs/symbols** — via a queryable symbol index + `verify_symbol` MCP tool.
- **False "done" claims** — via a post-edit hook that runs Go's existing checks and writes the outcome to a claims ledger.

Phase 1 is **Go-only** for parsing. Python and TypeScript come in phase 2/3.

## Phase 1 acceptance criteria

A phase-1 build is done when all of the following are true:

1. `leonard init` creates `.leonard/leonard.db` and runs a full index of the current Go project without error.
2. `leonard index` re-runs the index; second run is incremental (only files whose hash changed get re-parsed).
3. `leonard verify <name>` returns symbol matches from the CLI (functions, methods, types, exported consts/vars).
4. `leonard-mcp` exposes `verify_symbol`, `find_symbol`, and `list_files` over stdio and they return correct results against a fixture Go project.
5. `leonard-hook post-edit` consumes a Claude Code `PostToolUse` hook payload from stdin, re-indexes the touched files, runs `go vet ./...`, and writes a claim row.
6. `go test -race ./... -count=1` is clean across all packages.
7. Leonard can index itself without crashing or returning fabricated symbol claims.

## Lane proposal — designed to be independent

The packages map cleanly to lanes. Lane 1 (store + schema) is the bottleneck — others can stub against the schema interface in parallel once the SQL is settled.

### Lane A — Store (`internal/store/`)
**Owns:** SQLite connection, schema migration, CRUD over `files`, `symbols`, `decisions`, `claims`.
**Dependencies:** `modernc.org/sqlite` (pure-Go). Add to `go.mod`.
**Exports (sketch):**
```go
type Store struct{ ... }
func Open(path string) (*Store, error)
func (s *Store) UpsertFile(f File) error
func (s *Store) ReplaceSymbols(filePath string, syms []Symbol) error
func (s *Store) FindSymbolsByName(name string) ([]Symbol, error)
func (s *Store) FindSymbolsByQuery(q string, limit int) ([]Symbol, error)
func (s *Store) ListFiles(pattern, lang string) ([]File, error)
func (s *Store) RecordClaim(c Claim) (int64, error)
```
**Done when:** all schema tables created via migration, every exported method has a table-driven unit test against a `t.TempDir()` DB, race-clean.

### Lane B — Parser + indexer (`internal/parse/`, `internal/index/`)
**Owns:** tree-sitter Go grammar wrapper, AST → `[]Symbol` extraction; file walker, hash-based incremental dispatch.
**Dependencies:** tree-sitter binding. **Pick one and document the choice:** `github.com/smacker/go-tree-sitter` (CGo, mature) vs. a pure-Go alternative. Note in DESIGN.md open question #2.
**Exports (sketch):**
```go
// internal/parse
func ExtractGo(path string, src []byte) ([]store.Symbol, error)

// internal/index
type Indexer struct{ Store *store.Store; ... }
func (i *Indexer) IndexAll(root string) error
func (i *Indexer) IndexFile(path string) error
```
**Done when:** indexing the leonard repo itself yields all expected symbols (verifiable by `leonard verify` matching known function names); incremental re-index only re-parses changed files (verified by sha256 comparison + integration test).

### Lane C — MCP server (`cmd/leonard-mcp/`, `internal/mcp/`)
**Owns:** stdio MCP server, tool handlers for `verify_symbol`, `find_symbol`, `list_files`.
**Dependencies:** `github.com/modelcontextprotocol/go-sdk` (requires Go 1.25 — set toolchain directive in `go.mod`).
**Exports (sketch):** tool handler functions wrapping store queries; no business logic outside store.
**Done when:** server starts, responds to `tools/list` and the three v1 tools; integration test in `testdata/` exercises end-to-end against a sample Go project.

### Lane D — CLI + post-edit hook (`cmd/leonard/`, `cmd/leonard-hook/`, `internal/hooks/`)
**Owns:** Cobra-based CLI (`init`, `index`, `verify`, `mcp`), hook dispatcher with `post-edit` subcommand.
**Dependencies:** `github.com/spf13/cobra` (matches bosun's choice — keeps the homelab Go projects consistent).
**Exports (sketch):** standard `cmd/.../main.go` entry + Cobra command tree. `internal/hooks/postedit.go` reads JSON from stdin, decodes Claude's `PostToolUse` payload, calls `Indexer.IndexFile()`, shells out to `go vet`, writes claim row.
**Done when:** `leonard init` + `leonard index` + `leonard verify <name>` all work against a Go project; piping a synthetic `PostToolUse` JSON into `leonard-hook post-edit` produces the expected DB writes.

## Coordination rules for sessions

- **Lane A goes first or alone.** Once the schema and `Store` interface are merged, lanes B/C/D can proceed in parallel.
- **Don't edit files outside your lane** without `bosun claim` first. Lane boundaries are package boundaries.
- **No global state.** No package-level mutable state. No `init()` side effects beyond Cobra registration. (Same rule bosun applies to itself — keep it parallel-friendly.)
- **Race-clean is non-negotiable.** Run `go test -race ./... -count=1` before `bosun done`.
- **Update DESIGN.md open questions** when you resolve one (tree-sitter binding choice, hook perf budget, etc.). Don't silently decide — record the decision in the doc.

## What is NOT in phase 1

Anything from DESIGN.md §6 phase 2 or 3:

- `record_decision` / `get_decisions` MCP tools
- `SessionStart`, `pre-edit`, `Stop` hooks
- Python or TypeScript parsers
- `record_claim` / `get_unverified_claims` MCP tools (but the **schema** for claims must exist — phase-1 hook writes to it)
- Override paths for blocked pre-edits

If you're tempted to extend scope, write a TODO in `DESIGN.md` §6 phase 2/3 and keep moving. (Same rule bosun uses for itself.)

## When phase 1 is done

All seven acceptance criteria green. Tag `v0.1`, write a brief `RELEASES.md` entry, and merge to `main`.

Phase 2 starts from there.
