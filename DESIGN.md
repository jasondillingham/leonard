# Leonard — Design Doc

> *Leonard Hofstadter is the experimentalist who keeps Sheldon's overconfident theorizing tethered to reality. This tool plays the same role for Claude Code.*

**Status:** Draft v0.1 — design phase, no code yet.
**Last updated:** 2026-05-18

---

## 1. Problem

Claude Code hallucinates in four recurring ways. Each compounds over the life of a project:

| # | Failure mode | What it looks like |
|---|---|---|
| 1 | **Fabricated APIs/symbols** | Invents function names, struct fields, library methods, CLI flags that don't exist |
| 2 | **Drift from prior decisions** | Forgets or contradicts earlier choices (lib pick, naming, architecture) |
| 3 | **False "done" claims** | Says "tests pass" / "feature works" without actually verifying |
| 4 | **Stale codebase facts** | References paths, line numbers, behavior that *was* true but changed |

These aren't fixable by a better prompt. They need an external system that (a) knows the project's ground truth, (b) can verify Claude's claims against it, and (c) can enforce checks Claude cannot skip.

## 2. Goals & non-goals

**Goals**
- Per-project, local-first store of verifiable project facts (symbols, files, deps, decisions, claims).
- Expose those facts to Claude through MCP tools it queries on demand.
- Enforce verification through hooks Claude cannot opt out of.
- Cheap incremental updates — no full re-index on every edit.
- Self-contained: single Go binary, SQLite, no daemons or external services.

**Non-goals (v1)**
- Multi-project / org-wide knowledge graphs.
- General RAG over docs/Slack/PRs.
- Cloud sync, multi-user collaboration.
- Replacing existing test runners, linters, type checkers — Leonard *invokes* them.
- Cross-language unified semantic model — each language gets its own parser, no global symbol resolution.

## 3. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                       Claude Code session                       │
│                                                                 │
│  ┌──────────────┐  MCP   ┌──────────────────────────────────┐   │
│  │   Claude     │◄──────►│  leonard-mcp  (stdio MCP server) │   │
│  │              │        └──────────────┬───────────────────┘   │
│  │              │                       │                       │
│  │              │  hook calls           ▼                       │
│  │              │◄────────────── ┌────────────┐                 │
│  │              │                │  store     │ SQLite          │
│  └──────┬───────┘                │            │                 │
│         │                        └─────▲──────┘                 │
│         │ Edit/Write triggers          │                        │
│         ▼                              │                        │
│  ┌──────────────┐  exec   ┌────────────┴───────┐                │
│  │  hook        │────────►│  leonard-hook      │                │
│  │  (settings)  │         │  (PreToolUse,      │                │
│  └──────────────┘         │   PostToolUse,     │                │
│                           │   SessionStart,    │                │
│                           │   Stop)            │                │
│                           └────────────────────┘                │
└─────────────────────────────────────────────────────────────────┘
                                  ▲
                                  │
                          ┌───────┴────────┐
                          │ leonard (CLI)  │  manual: init, reindex,
                          │                │  decision add/list, doctor
                          └────────────────┘
```

Three binaries, one shared store:

- **`leonard`** — CLI for setup, manual operations, inspection.
- **`leonard-mcp`** — stdio MCP server registered in Claude Code's MCP config.
- **`leonard-hook`** — single binary dispatched from `settings.json` hooks, subcommands per hook type.

All three operate on the same `.leonard/leonard.db` SQLite file in the project root.

## 4. Components

### 4.1 Symbol indexer

**Goal:** answer "does symbol X exist? where? what's its signature?" in <10ms.

- **Parser:** per-language. Phase 1 uses Go's standard `go/parser` + `go/ast` for Go (see §7 q2). Phase 2 introduces tree-sitter for Python and TypeScript; binding choice deferred until then.
- **Languages (MVP):** Go, Python, TypeScript/JavaScript.
- **Granularity:** functions, methods, types/classes, top-level constants/vars, exported symbols. Skip locals.
- **Incremental:** per-file SHA256; only re-parse files whose hash changed.
- **Walks:** respect `.gitignore` + `.leonardignore`. Skip `node_modules`, `vendor`, build dirs by default.

### 4.2 Store (SQLite, modernc.org/sqlite — pure Go, no CGo)

```sql
CREATE TABLE files (
  path        TEXT PRIMARY KEY,
  hash        TEXT NOT NULL,
  language    TEXT NOT NULL,
  size_bytes  INTEGER NOT NULL,
  indexed_at  INTEGER NOT NULL  -- unix seconds
);

CREATE TABLE symbols (
  id               INTEGER PRIMARY KEY,
  file_path        TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  qualified_name   TEXT NOT NULL,        -- e.g. "pkg.Type.Method"
  kind             TEXT NOT NULL,        -- function|method|type|const|var|interface
  signature        TEXT,
  start_line       INTEGER NOT NULL,
  end_line         INTEGER NOT NULL,
  exported         INTEGER NOT NULL,     -- 0/1
  parent_id        INTEGER REFERENCES symbols(id) ON DELETE CASCADE
);
CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_qname ON symbols(qualified_name);
CREATE INDEX idx_symbols_file ON symbols(file_path);

CREATE TABLE decisions (
  id            INTEGER PRIMARY KEY,
  topic         TEXT NOT NULL,
  choice        TEXT NOT NULL,
  reasoning     TEXT NOT NULL,
  recorded_at   INTEGER NOT NULL,
  superseded_by INTEGER REFERENCES decisions(id)
);
CREATE INDEX idx_decisions_topic ON decisions(topic);

CREATE TABLE claims (
  id           INTEGER PRIMARY KEY,
  session_id   TEXT NOT NULL,
  claim        TEXT NOT NULL,
  evidence     TEXT NOT NULL,            -- command output, test result, etc.
  verified     INTEGER NOT NULL,         -- 0/1
  recorded_at  INTEGER NOT NULL
);
CREATE INDEX idx_claims_session ON claims(session_id);

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

### 4.3 MCP server (`leonard-mcp`)

Tool contracts (v1):

| Tool | Inputs | Returns |
|---|---|---|
| `verify_symbol` | `name`, optional `kind`, optional `language` | `{exists: bool, matches: [{file, line, signature, kind, qualified_name}]}` |
| `find_symbol` | `query` (substring/regex), optional `kind`, `limit` | `[{file, line, signature, kind, qualified_name}]` |
| `list_files` | optional `pattern` (glob), optional `language` | `[{path, language, size_bytes}]` |
| `recent_changes` | optional `since` (unix seconds or ISO), `limit` | `[{path, indexed_at}]` |
| `record_decision` | `topic`, `choice`, `reasoning` | `{decision_id}` |
| `get_decisions` | optional `topic`, optional `since`, `limit` | `[{id, topic, choice, reasoning, recorded_at}]` |
| `supersede_decision` | `decision_id`, `new_choice`, `new_reasoning` | `{new_decision_id}` |
| `record_claim` | `claim`, `evidence`, `verified` | `{claim_id}` |
| `get_unverified_claims` | optional `session_id` | `[{id, claim, recorded_at}]` |

Built on `github.com/modelcontextprotocol/go-sdk` — Jason recently contributed Issue #916 → PR #918, so the SDK is familiar territory.

### 4.4 Hooks (`leonard-hook <subcommand>`)

Configured in project `.claude/settings.json`. Each subcommand reads the hook's JSON payload from stdin, writes a JSON response to stdout, exits 0 (allow) or 1 (block).

| Hook | Subcommand | Phase | Behavior |
|---|---|---|---|
| `PostToolUse` (Edit, Write) | `post-edit` | v1 | Recompute file hash, re-parse, update symbol index. Run configured verifiers (lint/test) async, write outcome to `claims` table. |
| `SessionStart` | `session-start` | v1 | Inject last N decisions + open unverified claims into context as additional system content. |
| `PreToolUse` (Edit, Write) | `pre-edit` | v2 | Parse proposed change. Extract referenced symbols/imports. Reject if any reference a non-existent symbol (with override path). |
| `Stop` | `stop` | v2 | If session has unverified claims, surface them; optionally block stop until acknowledged. |

Exit-code semantics match Claude Code hook conventions: `0` = allow, `1` = block with reason in stdout JSON.

### 4.5 CLI (`leonard`)

```
leonard init [path]              # create .leonard/ + DB, scaffold config
leonard index                    # full re-index
leonard reindex <path>           # single-file re-index
leonard doctor                   # check index health, parser versions
leonard verify <symbol>          # CLI lookup (mirror of verify_symbol)
leonard decision add             # interactive decision recording
leonard decision list [--topic T]
leonard claims unverified
leonard mcp                      # exec leonard-mcp (convenience)
```

### 4.6 Configuration (`.leonard/config.toml`)

```toml
[index]
languages = ["go", "python", "typescript"]
ignore = ["vendor/", "node_modules/", "dist/", "build/"]

[verifiers]
# Commands run by post-edit hook. Stdout/exit code stored in `claims`.
go = ["go build ./...", "go vet ./..."]
python = ["ruff check ."]
typescript = ["tsc --noEmit"]

[hooks]
inject_decisions_at_session_start = 10  # last N
block_on_fabricated_symbol = true       # v2
```

## 5. Project layout

```
leonard/
├── DESIGN.md                # this file
├── README.md                # user-facing intro (later)
├── go.mod
├── cmd/
│   ├── leonard/             # CLI
│   ├── leonard-mcp/         # MCP stdio server
│   └── leonard-hook/        # hook dispatcher
├── internal/
│   ├── store/               # SQLite layer (sqlc or hand-rolled)
│   ├── index/               # walker, dispatcher
│   ├── parse/               # tree-sitter wrappers
│   │   ├── golang.go
│   │   ├── python.go
│   │   └── typescript.go
│   ├── mcp/                 # tool implementations
│   ├── hooks/               # per-hook handlers
│   └── config/              # config loader
├── testdata/                # sample projects per language
└── .leonard/                # example config + gitignored DB
```

## 6. MVP phasing

### Phase 1 — kills fabricated APIs + false "done" (target: 1-2 weekends)

- [ ] `leonard init`, `leonard index` (Go-only first)
- [ ] SQLite store + schema
- [ ] Tree-sitter Go parser, symbol extraction
- [ ] `leonard-mcp` with `verify_symbol`, `find_symbol`, `list_files`
- [ ] `leonard-hook post-edit` — re-index touched files, run `go vet`, write to claims
- [ ] Self-host: dogfood Leonard's own development

### Phase 2 — kills drift + pre-edit fabrication (target: +1 weekend)

- [ ] `record_decision` / `get_decisions` MCP tools
- [ ] `SessionStart` hook injecting recent decisions
- [ ] `pre-edit` hook with symbol-reference verification
- [ ] Python parser

### Phase 3 — kills stale facts + claim discipline

- [ ] `record_claim` / `get_unverified_claims`
- [ ] `Stop` hook surfacing unverified claims
- [ ] TypeScript parser
- [ ] `recent_changes` MCP tool wired in
- [ ] **pre-edit follow-ups** (deferred from phase 2): cross-module symbol resolution; generic type-parameter checking; verifying *method signatures* in addition to name existence; Python pre-edit (Python coverage matures first); an override path / `--force` flag (Claude Code's existing hook-deny override is enough for v0).

### Future / explicit non-MVP

- Other languages (Rust, Swift, Ruby, Java, C/C++)
- Cross-file/cross-package symbol resolution
- Decision export/import (for OSS positioning if it goes public)
- Web UI for browsing decisions/claims
- Multi-project / global mode

## 7. Open questions

1. **OSS positioning** — does Leonard get released? If so, the BBT-named brand needs a strong README narrative. If not, hardcode opinions and skip the abstraction layer.
2. **Tree-sitter binding choice** — ~~`smacker/go-tree-sitter` is most popular but has CGo. Pure-Go alternatives exist but are less complete per-language. Decide before phase 1 starts.~~ **Resolved (phase 1):** the Go extractor uses the standard library's `go/parser` + `go/ast` instead of a tree-sitter binding. Phase 1 is Go-only and the stdlib is pure-Go (no CGo build pain), faster than tree-sitter for Go specifically, and handles generics, type aliases, and method receivers without extra grammar work. Tree-sitter remains the right call for phase 2 (Python, TypeScript) where the stdlib does not help — binding choice (CGo `smacker/go-tree-sitter` vs a pure-Go WASM-based alternative) is deferred until that work starts.
3. **TOML library** — *Resolved 2026-05-18 (cli lane):* `pelletier/go-toml/v2`. It is the actively-maintained successor to BurntSushi/toml, has zero-allocation decoding for the small configs Leonard reads, and exposes both struct-tag and AST APIs in case Phase 2 needs to edit a config in place.
4. **Hook performance budget** — `post-edit` runs on every Write/Edit. What's the latency ceiling before it feels bad? (Target: <200ms p95 for re-index of one file.)
5. **Claim model fidelity** — does Claude actually have to *call* `record_claim`, or do we infer claims from its prose? The former is enforceable but requires Claude's cooperation; the latter needs an LLM-based extraction step (defeats the purpose).
6. **Override path** — when `pre-edit` blocks a fabricated reference, how does Claude force through? CLI flag? Special MCP tool (`override_block(reason)`)? No override (strict)?
7. **Concurrent access** — multiple Claude Code sessions on the same project would hit the same DB. SQLite WAL mode handles most cases; need to verify hook concurrency.
8. **Dogfooding constraint** — building Leonard *with* Claude Code while Leonard isn't done yet means we run blind on fabrication checks for the indexer code itself. Acceptable risk for v0.

## 8. Out of scope explicitly

- Replacing or competing with: gopls, pyright, language servers in general. Leonard is *adjacent* to LSPs — it serves Claude, not the editor.
- Long-form documentation extraction (docstrings, README parsing) — symbols only in v1.
- Anything requiring an LLM call from inside Leonard itself. Leonard is deterministic infrastructure; the LLM lives in Claude Code.
