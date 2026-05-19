# Leonard

> *Leonard Hofstadter is the experimentalist who keeps Sheldon's overconfident theorizing tethered to reality. This tool plays the same role for Claude Code.*

A local-first, per-project ground-truth toolkit that helps Claude Code avoid hallucinating over the life of a project. Symbol index + decision log + claim ledger, exposed to Claude through MCP and enforced through hooks.

**Status:** Pre-alpha, design phase. See [`DESIGN.md`](./DESIGN.md) for the architecture.

## What it does (planned)

Leonard targets four recurring Claude Code failure modes:

1. **Fabricated APIs/symbols** — pre-edit hook rejects references to symbols that don't exist in the project.
2. **Drift from prior decisions** — durable decision log surfaced into every new session.
3. **False "done" claims** — post-edit hook runs project's lint/test and writes the outcome into a claim ledger.
4. **Stale codebase facts** — incremental re-index on every edit keeps the symbol map fresh.

## Components

- **`leonard`** — CLI for setup, manual indexing, decision recording, doctor.
- **`leonard-mcp`** — stdio MCP server registered with Claude Code.
- **`leonard-hook`** — hook dispatcher invoked from `.claude/settings.json`.

All three share a single SQLite store at `.leonard/leonard.db` in the project root.

## Install

Not yet — see DESIGN.md for the build plan.

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).
