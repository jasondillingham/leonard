# Leonard

> *Leonard Hofstadter is the experimentalist who keeps Sheldon's overconfident theorizing tethered to reality. This tool plays the same role for Claude Code.*

A local-first, per-project ground-truth toolkit that helps Claude Code avoid hallucinating over the life of a project. Symbol index + decision log + claim ledger, exposed to Claude through MCP and enforced through hooks.

**Status:** v0.17.0, self-dogfooded on this repo. Four bug-hunt rounds + one focused security review have driven the project through 17 minor releases; the deferred-MEDIUM lists in [`bughunt-{1,2,3,4}-triage.md`](.) track what's left. See [`DESIGN.md`](./DESIGN.md) for the architecture.

Security and correctness fixes since v0.1:
- v0.7.1 — `idx_symbols_parent` (~137× prune speedup)
- v0.8.0 — path-trust sweep: file_path values from hook payloads are now rejected when they resolve outside the project root (was a confused-deputy)
- v0.9.0 — resource caps on hook payloads, decisions, claims, MultiEdit
- v0.13.0 — cap completeness: real line reader replaces a busted `bufio.Scanner` that busy-spun on oversize lines; per-response and per-bullet truncation everywhere
- v0.14.0 — path-trust completeness: pre-edit, prune, doctor all wired through; dangling-symlink rejection; NFC normalization at storeKey
- v0.15.0 — store-perf: three missing indexes + N+1 fix in get_stale_decisions + WAL checkpointing
- v0.16.0 — MCP-recorded claims can now be superseded by post-edit vet=ok runs
- v0.17.0 — three multi-round carry-over MEDIUMs closed

## What it does

Leonard targets four recurring Claude Code failure modes:

1. **Fabricated APIs/symbols** — pre-edit hook rejects references to symbols that don't exist in any tracked package.
2. **Drift from prior decisions** — durable decision log surfaced into every new session via the SessionStart hook.
3. **False "done" claims** — post-edit hook runs `go vet ./...` after each Edit/Write and writes the outcome into a claims ledger; the Stop hook surfaces any unverified claims at session end.
4. **Stale codebase facts** — incremental re-index on every edit keeps the symbol map fresh; the `recent_changes` MCP tool lets Claude ask "what's moved since I last looked?" instead of relying on memory.

## Components

- **`leonard`** — CLI: `init`, `index`, `verify`, `doctor`, `decisions`, `claims`, `mcp`. CLI now walks up looking for `.leonard/` so invocations from subdirs work.
- **`leonard-mcp`** — stdio MCP server. Tools: `verify_symbol`, `find_symbol`, `list_files`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `recent_changes`. All read tools cap response size (≤1 MiB). All write tools cap input text and reject oversize payloads cleanly.
- **`leonard-hook`** — hook dispatcher with `pre-edit`, `post-edit`, `session-start`, `stop` subcommands. Path-trust guard rejects file_path values outside the project root. Resource caps on payload (16 MiB), snippet (1 MiB), MultiEdit element count (100).

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
internal/parse                            # Go (stdlib), Python (subprocess), Rust (syn subprocess), TypeScript (hand-rolled) extractors
internal/parse/rust                       # Cargo crate for the syn-based extractor
internal/telemetry                        # OTel instrumentation (build-tag-gated)
examples/pydantic-ai                      # Python demo wiring leonard-mcp into a pydantic-ai agent
evals/inspect                             # Anthropic Inspect eval framework for fabrication rate
internal/mcp                              # MCP tool handlers + StoreAdapter
internal/hooks                            # hook handler implementations
internal/config                           # .leonard/config.toml loader
```

## Telemetry (optional)

v0.6 added build-tag-gated OpenTelemetry spans on the hot paths the
bughunt-2 perf round flagged. **`leonard-hook` is the only binary
currently instrumented** — `leonard-mcp` and `leonard` CLI compile
identically under the tag (no spans emitted). Default builds have
zero overhead — the no-op stubs compile in and the OTel SDK doesn't
load. To get real spans, rebuild with the `otel` tag:

```bash
go install -tags otel ./cmd/...
```

Then point the binaries at whatever OTel collector you run:

```bash
# OTLP (preferred — sends to a collector at the endpoint URL)
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318

# Or local debugging — writes one JSON span per line to stderr
export OTEL_TRACES_EXPORTER=stdout
```

Spans currently produced:

| Span | What it times |
|---|---|
| `leonard.pre-edit` | whole PreToolUse handler |
| `leonard.pre-edit.sibling-scan` | the F8 module-wide walk (bughunt-2's perf concern) |
| `leonard.post-edit` | whole PostToolUse handler |
| `leonard.post-edit.index` | the single-file re-index call |
| `leonard.post-edit.vet` | `go vet ./...` plus result parsing |

With no env vars set the tagged build still runs but exports nothing —
useful in CI when you want the option available but no traffic going
out by default.

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).
