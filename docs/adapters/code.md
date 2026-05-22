# Code adapter

The code adapter is Leonard's original behavior, refactored into
the adapter contract in v0.7 (#7). It indexes the project's source
tree, exposes symbol-lookup MCP tools, blocks edits that reference
fabricated symbols, and runs a project verifier on every
Edit/Write.

If your project has a `go.mod` at the root and you've never
touched `.leonard/config.toml`, this is what you're running.

---

## What it does

Four failure modes targeted:

1. **Fabricated APIs/symbols** — pre-edit hook rejects references
   to symbols that don't exist in any tracked package.
2. **Drift from prior decisions** — durable decision log surfaced
   into every new session via the SessionStart hook.
3. **False "done" claims** — post-edit hook runs `go vet ./...`
   (or `[post_edit.verify].command`) and records the outcome.
4. **Stale codebase facts** — incremental re-index on every edit;
   the `recent_changes` MCP tool lets Claude ask "what's moved
   since I last looked?"

---

## MCP tools

| Tool | Description |
|---|---|
| `verify_symbol` | Does this symbol exist? Returns matches with file/line. |
| `find_symbol` | Substring search the symbol index. |
| `list_files` | List indexed files (optional glob + language filter). |
| `record_decision` | Persist a project decision. |
| `get_decisions` | Recall decisions (newest-first). |
| `supersede_decision` | Replace an existing decision. |
| `get_stale_decisions` | Decisions whose refs no longer resolve. |
| `record_claim` | Append to the claim ledger. |
| `get_unverified_claims` | List unresolved claims for a session. |
| `recent_changes` | Files indexed since timestamp. |

All read tools cap response size (≤1 MiB). All write tools cap
input text and reject oversize payloads.

---

## Configuration

### Implicit-enable

The code adapter is auto-enabled when:

- The project has a `go.mod` at root, OR
- `[post_edit.verify]` is configured in `.leonard/config.toml`

No `[[adapters]]` block needed.

### Custom verifier

```toml
[post_edit.verify]
command = "cargo check --workspace"
timeout = "60s"
```

The verifier requires explicit trust:

```
leonard config trust
```

Trust fingerprints the command via SHA-256 and stores the marker
at `$XDG_CONFIG_HOME/leonard/trust/<project-hash>.sha256`. The
post-edit hook refuses to run an untrusted command.

---

## Language support

Production-dogfooded parsers (4):

| Language | Parser | Validation |
|---|---|---|
| Go | stdlib `go/parser` | Self-dogfooded |
| TypeScript / TSX / JSX | hand-rolled | Zod (401 files) + Next.js App-Router |
| Python | host `python3` via subprocess | full PEP coverage |
| Rust | `syn` via subprocess | ripgrep + clap-rs |

Tree-sitter parsers (29 additional languages) share a single Rust
helper. See [`README.md`](../../README.md) language tables for the
full list.

---

## Implementation pointers

- Adapter shim: [`internal/adapters/code/`](../../internal/adapters/code/)
- Hook logic: [`internal/hooks/`](../../internal/hooks/)
- MCP server: [`internal/mcp/`](../../internal/mcp/)
- Parsers: [`internal/parse/`](../../internal/parse/)

---

## Status

Production-stable since v0.52 (the pre-adapter shape). The v0.7
refactor (#7) keeps every existing test passing — zero regression
for v0.52 projects.
