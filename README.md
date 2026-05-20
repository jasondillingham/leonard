# Leonard

> *Leonard Hofstadter is the experimentalist who keeps Sheldon's overconfident theorizing tethered to reality. This tool plays the same role for Claude Code.*

A local-first, per-project ground-truth toolkit that helps Claude Code avoid hallucinating over the life of a project. Symbol index + decision log + claim ledger, exposed to Claude through MCP and enforced through hooks.

**Status:** v0.1, self-dogfooded on this repo as of 2026-05-19. See [`DESIGN.md`](./DESIGN.md) for the architecture and [`bughunt-1-triage.md`](./bughunt-1-triage.md) for the deferred MEDIUM-severity known gaps.

## What it does

Leonard targets four recurring Claude Code failure modes:

1. **Fabricated APIs/symbols** — pre-edit hook rejects references to symbols that don't exist in any tracked package.
2. **Drift from prior decisions** — durable decision log surfaced into every new session via the SessionStart hook.
3. **False "done" claims** — post-edit hook runs `go vet ./...` after each Edit/Write and writes the outcome into a claims ledger; the Stop hook surfaces any unverified claims at session end.
4. **Stale codebase facts** — incremental re-index on every edit keeps the symbol map fresh; the `recent_changes` MCP tool lets Claude ask "what's moved since I last looked?" instead of relying on memory.

## Components

- **`leonard`** — CLI: `init`, `index`, `verify`, `mcp` (passthrough).
- **`leonard-mcp`** — stdio MCP server. Tools: `verify_symbol`, `find_symbol`, `list_files`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `recent_changes`.
- **`leonard-hook`** — hook dispatcher with `pre-edit`, `post-edit`, `session-start`, `stop` subcommands.

All three share a single SQLite store at `.leonard/leonard.db` in the project root.

## Language support

| Language | Parser | Status |
|---|---|---|
| Go | stdlib `go/parser` | Production. Self-dogfooded on this repo; vet+test invariant enforced via the post-edit hook. |
| TypeScript / TSX | hand-rolled (`internal/parse/typescript.go`) | Real-world tested on 401-file / 7k-symbol Zod corpus plus a 22-file Next.js App-Router project. Functions (including `export const f = () => …` arrow forms — async, generic, typed-return, single-param sugar), classes (with generic defaults, abstract, methods named after keywords like `default`/`type`, inline `{ a: T }`-shaped return types), interfaces, type aliases, const/let/var all extract with accurate file/line. |
| Python | host `python3` via subprocess | Production. v0.2 swapped gpython for an exec of the host's `python3` running an embedded `ast` walker. Every Python version the user has installed is supported — f-strings, PEP 526/585/604/695, walrus, `match`, async, etc. Cost: ~40ms per parse vs gpython's ~35µs, still well under the 200ms hook latency budget. Requires `python3` on PATH (override via `LEONARD_PYTHON`); a missing interpreter surfaces as a per-file parse failure rather than tanking the whole index. |
| Rust | syn-based subprocess (`leonard-extract-rust`) | Production. v0.5 added a small Rust binary at `internal/parse/rust/` that uses the canonical `syn` crate to walk the AST and emit JSON. Same dependency model as Python — a helper binary the host has to build once via `cargo build --release` (or install on PATH). Dogfooded against ripgrep: 100 of 100 files indexed, 2,678 symbols extracted, every modern syntax handled (async fn, generics, impl Trait, GATs, const generics). Override via `LEONARD_RUST_EXTRACTOR`; v0 scope is top-level fn/struct/enum/trait/type alias/const/static + methods one level deep inside `impl` blocks. |

## Install

```bash
git clone <this repo>
cd leonard
go install ./cmd/...   # puts leonard, leonard-mcp, leonard-hook in $GOPATH/bin
```

Requires Go 1.25+ (auto-fetched via toolchain directive if you have 1.21+).

## Dogfood wiring (this repo)

Leonard is wired into its own development through `.claude/settings.local.json` (gitignored — personal config, not shared). The shape:

```jsonc
{
  "mcpServers": {
    "leonard": { "command": "/path/to/go/bin/leonard-mcp" }
  },
  "hooks": {
    "PreToolUse":  [{ "matcher": "Edit|Write|MultiEdit|NotebookEdit", "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook pre-edit"  }]}],
    "PostToolUse": [{ "matcher": "Edit|Write|MultiEdit",              "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook post-edit" }]}],
    "SessionStart":[{ "matcher": "",            "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook session-start" }]}],
    "Stop":        [{ "matcher": "",            "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook stop" }]}]
  }
}
```

Enable Leonard in a new project: `leonard init .`, then drop the same JSON into that project's `.claude/settings.local.json` and restart Claude Code in the directory.

## Project layout

```
cmd/{leonard,leonard-mcp,leonard-hook}   # the three binaries
internal/store                            # SQLite-backed data layer
internal/index                            # file walker + incremental dispatch
internal/parse                            # Go (stdlib), Python (gpython), TypeScript (hand-rolled) extractors
internal/mcp                              # MCP tool handlers + StoreAdapter
internal/hooks                            # hook handler implementations
internal/config                           # .leonard/config.toml loader
```

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).
