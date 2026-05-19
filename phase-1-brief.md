# Leonard — Phase 1 Brief

> **Audience:** parallel Claude Code sessions launched by bosun. Each lane below becomes its own session/worktree/branch.
> **Source of truth:** `DESIGN.md` in this repo. Read it before starting.

## Phase 1 goal

Ship a working slice of Leonard that kills two of the four hallucination modes:

- **Fabricated APIs/symbols** — via a queryable symbol index + `verify_symbol` MCP tool.
- **False "done" claims** — via a post-edit hook that runs Go's existing checks and writes the outcome to a claims ledger.

Phase 1 is **Go-only** for parsing. Python and TypeScript come in phase 2/3.

## Phase 1 acceptance criteria (whole project)

A phase-1 build is done when all of the following are true:

1. `leonard init` creates `.leonard/leonard.db` and runs a full index of the current Go project without error.
2. `leonard index` re-runs the index; second run is incremental (only files whose hash changed get re-parsed).
3. `leonard verify <name>` returns symbol matches from the CLI (functions, methods, types, exported consts/vars).
4. `leonard-mcp` exposes `verify_symbol`, `find_symbol`, and `list_files` over stdio and they return correct results against a fixture Go project.
5. `leonard-hook post-edit` consumes a Claude Code `PostToolUse` hook payload from stdin, re-indexes the touched files, runs `go vet ./...`, and writes a claim row.
6. `go test -race ./... -count=1` is clean across all packages.
7. Leonard can index itself without crashing or returning fabricated symbol claims.

## Coordination rules (apply to every lane)

- **Stay in your lane.** Lane boundaries are package boundaries — declared in each lane's "Files I own" list. Use `bosun claim` if you need to touch anything outside it.
- **No global state.** No package-level mutable state. No `init()` side effects beyond Cobra registration.
- **Race-clean is non-negotiable.** Run `go test -race ./... -count=1` before `bosun done`.
- **Update DESIGN.md open questions** when you resolve one (tree-sitter binding choice, hook perf budget, etc.). Decide deliberately and record the decision.

---

## store

You own the SQLite-backed Leonard data layer. Everyone else (parser, mcp, cli) waits on your `Store` interface to land.

**Files you own (exclusively):**
- `internal/store/` — everything in this package

**Add dependencies:**
- `modernc.org/sqlite` (pure-Go SQLite driver, no CGo)

**Implement:**

```go
package store

type File struct {
    Path       string
    Hash       string
    Language   string
    SizeBytes  int64
    IndexedAt  int64 // unix seconds
}

type Symbol struct {
    ID            int64
    FilePath      string
    Name          string
    QualifiedName string
    Kind          string // function|method|type|const|var|interface
    Signature     string
    StartLine     int
    EndLine       int
    Exported      bool
    ParentID      *int64
}

type Decision struct {
    ID           int64
    Topic        string
    Choice       string
    Reasoning    string
    RecordedAt   int64
    SupersededBy *int64
}

type Claim struct {
    ID         int64
    SessionID  string
    Claim      string
    Evidence   string
    Verified   bool
    RecordedAt int64
}

type Store struct{ /* unexported */ }

func Open(path string) (*Store, error)
func (s *Store) Close() error

// Files + symbols
func (s *Store) UpsertFile(f File) error
func (s *Store) GetFile(path string) (File, bool, error)
func (s *Store) ReplaceSymbols(filePath string, syms []Symbol) error
func (s *Store) FindSymbolsByName(name string) ([]Symbol, error)
func (s *Store) FindSymbolsByQuery(q string, limit int) ([]Symbol, error)
func (s *Store) ListFiles(pattern, lang string) ([]File, error)

// Decisions
func (s *Store) RecordDecision(d Decision) (int64, error)
func (s *Store) GetDecisions(topic string, since int64, limit int) ([]Decision, error)
func (s *Store) SupersedeDecision(id int64, choice, reasoning string) (int64, error)

// Claims
func (s *Store) RecordClaim(c Claim) (int64, error)
func (s *Store) GetUnverifiedClaims(sessionID string) ([]Claim, error)
```

Schema is in `DESIGN.md` §4.2 — implement it in a `migrate()` called from `Open`. Use a `meta` table for schema versioning so phase 2 can add columns safely.

**Acceptance for this lane:**
- All exported methods covered by table-driven unit tests against a `t.TempDir()` DB
- WAL mode enabled on Open (so concurrent hooks don't block each other)
- `go test -race ./internal/store/... -count=1` clean
- Schema migration is idempotent — running `Open` twice on the same DB is a no-op

## parser (depends: store)

You own Go symbol extraction and the file walker that drives indexing.

**Files you own (exclusively):**
- `internal/parse/` — all symbol extractors (just `golang.go` in phase 1)
- `internal/index/` — walker + dispatcher

**Add dependencies:**
- Tree-sitter Go binding. **Decide between `github.com/smacker/go-tree-sitter` (CGo, mature) and a pure-Go alternative.** Document the choice in DESIGN.md §7 open question #2 and explain why.

**Implement:**

```go
// internal/parse
func ExtractGo(path string, src []byte) ([]store.Symbol, error)
// Extracts: top-level funcs, methods, types (struct/interface/alias),
// top-level const/var, with exported flag, signature string, line range.

// internal/index
type Indexer struct {
    Store *store.Store
    Root  string
    // ...
}
func New(s *store.Store, root string) *Indexer
func (i *Indexer) IndexAll() error      // full walk
func (i *Indexer) IndexFile(path string) error // single-file
```

Walker behavior:
- Respect `.gitignore` and `.leonardignore`
- Skip `vendor/`, `node_modules/`, `dist/`, `build/`, `.git/` by default
- Use sha256 per file; skip re-parse if hash unchanged from last index

**Acceptance for this lane:**
- Indexing the Leonard repo itself yields all expected Go symbols (verifiable by `leonard verify` from the CLI lane matching known names like `Open`, `ExtractGo`, `Indexer`)
- Incremental: running `IndexAll` twice in a row, the second run does zero re-parses (assert via instrumented test counter)
- `go test -race ./internal/{parse,index}/... -count=1` clean

## mcp (depends: store)

You own the stdio MCP server and the v1 tool handlers.

**Files you own (exclusively):**
- `cmd/leonard-mcp/` — the main entrypoint
- `internal/mcp/` — tool handlers

**Add dependencies:**
- `github.com/modelcontextprotocol/go-sdk` (the official MCP go-sdk). **Requires Go 1.25** — add a `toolchain go1.25.0` directive to `go.mod` and update the `go` directive accordingly.

**Implement these v1 tools:**

| Tool | Input | Output |
|---|---|---|
| `verify_symbol` | `name: string, kind?: string, language?: string` | `{exists: bool, matches: [{file, line, signature, kind, qualified_name}]}` |
| `find_symbol` | `query: string, kind?: string, limit?: int` | `[{file, line, signature, kind, qualified_name}]` |
| `list_files` | `pattern?: string, language?: string` | `[{path, language, size_bytes}]` |

Each handler is a thin shim over `store` — no business logic in `internal/mcp/`.

**Acceptance for this lane:**
- `leonard-mcp` starts on stdio, responds to `tools/list` listing the three v1 tools with correct JSON schema
- Integration test in `internal/mcp/` exercises each tool end-to-end against a fixture Go project in `testdata/`
- `go test -race ./cmd/leonard-mcp/... ./internal/mcp/... -count=1` clean

## cli (depends: store)

You own the human-facing CLI and the post-edit hook dispatcher.

**Files you own (exclusively):**
- `cmd/leonard/` — Cobra CLI entrypoint
- `cmd/leonard-hook/` — hook dispatcher entrypoint
- `internal/hooks/` — hook handler implementations
- `internal/config/` — config loader (just enough for phase 1)

**Add dependencies:**
- `github.com/spf13/cobra` (matches bosun — keeps the homelab Go projects consistent)
- BurntSushi/toml or pelletier/go-toml/v2 for `.leonard/config.toml` (pick one, document why)

**Implement CLI commands:**

```
leonard init [path]      # create .leonard/, open DB (runs schema migration)
leonard index            # full re-index of cwd
leonard verify <name>    # CLI mirror of MCP verify_symbol
leonard mcp              # exec leonard-mcp (convenience pass-through)
```

**Implement hook subcommand:**

```
leonard-hook post-edit   # consume PostToolUse JSON from stdin
```

Post-edit handler logic:
1. Parse Claude Code's `PostToolUse` JSON payload (the tool-use envelope with `tool_input.file_path`)
2. Call `Indexer.IndexFile(file_path)` to refresh the symbol index for that file
3. Shell out to `go vet ./...` (when in a Go project — detect via presence of `go.mod`)
4. Write a `claims` row: `verified` = true if vet exit 0, `evidence` = vet stdout/stderr trimmed
5. Print a hook response JSON to stdout (Claude Code format), exit 0

**Acceptance for this lane:**
- `leonard init` + `leonard index` + `leonard verify <name>` all work end-to-end against a sample Go project (use `testdata/`)
- Piping a synthetic `PostToolUse` JSON into `leonard-hook post-edit` produces:
  - An updated row in `files` and replaced rows in `symbols` for the touched file
  - A new `claims` row with the vet outcome
- `go test -race ./cmd/{leonard,leonard-hook}/... ./internal/{hooks,config}/... -count=1` clean

---

## What is NOT in phase 1

Anything from DESIGN.md §6 phase 2 or 3:

- `record_decision` / `get_decisions` MCP tools (the schema must exist; the handlers don't)
- `SessionStart`, `pre-edit`, `Stop` hooks
- Python or TypeScript parsers
- `record_claim` / `get_unverified_claims` as MCP tools (phase-1 hook writes claims directly to the store)
- Override paths for blocked pre-edits

If you're tempted to extend scope, write a TODO in `DESIGN.md` §6 phase 2/3 and keep moving.
