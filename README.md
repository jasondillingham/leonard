<p align="center">
  <img src="docs/assets/leonard-logo.png" alt="Leonard logo — auditor at a desk, holding glasses by one earhook, checking a ledger" width="320">
</p>

# Leonard

[![CI](https://github.com/jasondillingham/leonard/actions/workflows/ci.yml/badge.svg)](https://github.com/jasondillingham/leonard/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Reference](https://pkg.go.dev/badge/github.com/jasondillingham/leonard.svg)](https://pkg.go.dev/github.com/jasondillingham/leonard)

> *Leonard Hofstadter is the experimentalist who keeps Sheldon's overconfident theorizing tethered to reality. This tool plays the same role for Claude Code.*

A local-first, per-project ground-truth toolkit that helps Claude Code avoid hallucinating over the life of a project. Symbol index + decision log + claim ledger, exposed to Claude through MCP and enforced through hooks.

**Status: v0.55.0 — stable.** Self-dogfooded across 60 releases with **twelve bug-hunt rounds and five security reviews**; every HIGH and CRITICAL finding closed. Audit trail lives under [`audits/`](./audits/). Architecture in [`DESIGN.md`](./DESIGN.md); release history in [`CHANGELOG.md`](./CHANGELOG.md).

**v1.0 in flight** — generalizing Leonard from "code ground-truth" to pluggable ground-truth (code adapter + new ground-truth adapter for prose/claims/facts), plus self-logging of truth changes. Design in [`docs/ROADMAP-v1-ground-truth.md`](./docs/ROADMAP-v1-ground-truth.md); phased work tracked under milestones [v0.6](https://github.com/jasondillingham/leonard/milestone/1)–[v1.0](https://github.com/jasondillingham/leonard/milestone/5).

```
29 tree-sitter languages   +   4 production-dogfooded parsers   +   3 SFC preprocessors
+ 2 structured-file inspectors (Jupyter, OpenAPI)
+ 4 manifest dependency-graph formats (package.json, Cargo.toml, go.mod, pom.xml)
```

## What it does

Leonard targets four recurring Claude Code failure modes:

1. **Fabricated APIs/symbols** — pre-edit hook rejects references to symbols that don't exist in any tracked package.
2. **Drift from prior decisions** — durable decision log surfaced into every new session via the SessionStart hook.
3. **False "done" claims** — post-edit hook runs a project verifier (default `go vet ./...`, configurable via `[post_edit.verify]` — see below) after each Edit/Write and writes the outcome into a claims ledger; the Stop hook surfaces any unverified claims at session end.
4. **Stale codebase facts** — incremental re-index on every edit keeps the symbol map fresh; the `recent_changes` MCP tool lets Claude ask "what's moved since I last looked?" instead of relying on memory.

## Components

- **`leonard`** — CLI: `init`, `index`, `verify`, `doctor`, `decisions`, `claims`, `mcp`. Walks up looking for `.leonard/` so invocations from subdirs work.
- **`leonard-mcp`** — stdio MCP server. Tools: `verify_symbol`, `find_symbol`, `list_files`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `recent_changes`. All read tools cap response size (≤1 MiB). All write tools cap input text and reject oversize payloads cleanly.
- **`leonard-hook`** — hook dispatcher with `pre-edit`, `post-edit`, `session-start`, `stop` subcommands. Path-trust guard rejects file_path values outside the project root. Resource caps on payload (16 MiB), snippet (1 MiB), MultiEdit element count (100).

All three share a single SQLite store at `.leonard/leonard.db` in the project root.

## Language support

### Production-dogfooded parsers

These four ship with bespoke (non-tree-sitter) parsers and have been validated against substantial real-world corpora.

| Language | Parser | Validation |
|---|---|---|
| **Go** | stdlib `go/parser` | Self-dogfooded on this repo; vet+test invariant enforced via the post-edit hook. |
| **TypeScript / TSX / JSX** | hand-rolled (`internal/parse/typescript.go`) | 401-file / 7k-symbol Zod corpus + 22-file Next.js App-Router project. Functions (including arrow forms), classes (with generic defaults, abstract), interfaces, type aliases, const/let/var. JSX/Solid use the same extractor. |
| **Python** | host `python3` via subprocess | Embedded `ast` walker; every modern Python construct (f-strings, PEP 526/585/604/695, walrus, `match`, async). Override via `LEONARD_PYTHON`. |
| **Rust** | syn-based subprocess (`leonard-extract-rust`) | Dogfooded against ripgrep (100/100 files, 2,678 symbols) and clap-rs. Full async/generics/impl Trait/GATs/const generics coverage. Override via `LEONARD_RUST_EXTRACTOR`. |

### Tree-sitter parsers

Twenty-nine additional languages share a single Rust helper (`internal/parse/treesitter/`) that statically links the tree-sitter runtime + per-language grammars. Build once with `cargo build --release` inside the crate; override via `LEONARD_TREESITTER_EXTRACTOR`.

| Language | Extensions | Notable |
|---|---|---|
| Java | `.java` | Dogfooded against Gson (262 files / 4,136 symbols). |
| Ruby | `.rb` | Dogfooded against Sinatra (147 files / 1,132 symbols). |
| C# | `.cs` | Class/struct/record/enum/delegate/method/constructor. |
| Swift | `.swift` | Including `init_declaration` synthesis. |
| Kotlin | `.kt`, `.kts` | Includes `object_declaration`. |
| Scala | `.scala` | `_definition` convention; trait → interface. |
| Dart | `.dart` | Flutter-friendly; function_signature parent-folding. |
| C | `.c`, `.h` | function_definition + struct/union/enum/typedef. |
| C++ | `.cc`, `.cpp`, `.cxx`, `.hpp`, `.hh` | In-class inline methods extracted; header-only libraries like nlohmann/json fully indexed. |
| PHP | `.php` | namespace + class/interface/trait/enum + methods. |
| Lua | `.lua` | Three function-declaration shapes (plain, dot-indexed, method-indexed). |
| Bash | `.sh`, `.bash` | Function definitions. |
| Zig | `.zig` | const-bound struct/enum/union, function_declaration. |
| Nix | `.nix` | Function bindings inside attrsets. |
| Elixir | `.ex`, `.exs` | defmodule/def/defp/defmacro/defprotocol (predicate-filtered). |
| Solidity | `.sol` | Contracts/interfaces/libraries + function/modifier/event/constructor. |
| Erlang | `.erl`, `.hrl` | module_attribute, record_decl, fun_decl. |
| R | `.r` (case-insensitive) | `x <- function(...)` idiom captured. |
| Just | basename `justfile`/`Justfile` | Recipes as functions. |
| Starlark (Bazel) | `.bzl`, `.bazel`, `.star` + basename `BUILD`/`BUILD.bazel`/`WORKSPACE`/`WORKSPACE.bazel` | `function_def` + rule calls with `name = "..."` (filter-anchored). |
| Make | `.mk` + basename `Makefile`/`GNUmakefile` | Rules and variable_assignment. |
| CMake | `.cmake` + basename `CMakeLists.txt` | function_def + `add_library`/`add_executable`/`option`. |
| HCL / Terraform | `.tf`, `.tfvars`, `.hcl` | `resource`/`variable`/`module`/`output`/`data`/`provider`/`locals`/`terraform` blocks. |
| GraphQL SDL | `.graphql`, `.gql` | object/interface/enum/scalar/union/input + field_definitions (parent-folded). |
| Protocol Buffers | `.proto` | message/enum/service/rpc (parent-folded). |
| WIT | `.wit` | WebAssembly Component Model: interface/world/func/record/enum/variant/flags. |
| SQL | `.sql` | CREATE TABLE/VIEW/INDEX/FUNCTION + column definitions (parent-folded under their table). |
| GLSL | `.glsl`, `.vert`, `.frag`, `.geom`, `.comp`, `.tesc`, `.tese` | Functions, struct types, uniform/varying/in/out declarations. |
| HLSL | `.hlsl`, `.fx`, `.fxh` | Same query shape as GLSL (C-family). |

### SFC preprocessors

Three frontend formats share a context-aware scanner (quote- and HTML-comment-aware) that extracts the script blocks and routes them through the TypeScript extractor with line-offset adjustment.

| Format | Extension | What gets extracted |
|---|---|---|
| Vue | `.vue` | `<script>` and `<script setup>` blocks (script symbols only — template/style ignored). |
| Svelte | `.svelte` | Same shape as Vue. |
| Astro | `.astro` | Frontmatter (between `---` fences) PLUS embedded `<script>` blocks. |

### Structured-file inspectors

Specialized walkers for formats where "symbols" means something different from functions/types.

| Format | What gets extracted |
|---|---|
| Jupyter notebooks (`.ipynb`) | Code cells concatenated and routed through the Python extractor (markdown/raw skipped). |
| OpenAPI / Swagger | Filename-detected (`openapi.{yaml,yml,json}` or `swagger.*`). Operations (`GET /users/{id}`) + schema names. Both Swagger 2.0 (`definitions`) and OpenAPI 3 (`components.schemas`) handled. |

### Manifest dependency graph

Four manifest formats emit one Symbol per declared dependency with `kind="dependency"` so Claude can run `verify_symbol("react")` to confirm a package is actually in the project's dependency set before fabricating an import.

| Manifest | Coverage |
|---|---|
| `package.json` | `dependencies` + `devDependencies` + `peerDependencies` + `optionalDependencies`. Handles both bare-string and pnpm/yarn object-form versions. |
| `Cargo.toml` | `[dependencies]` + `[dev-dependencies]` + `[build-dependencies]`. Inline-table versions handled; path/git deps tagged. |
| `go.mod` | Every `require` directive via `golang.org/x/mod/modfile`; indirect requires marked. |
| `pom.xml` | Top-level `<dependencies>/<dependency>`; signature folds `groupId:artifactId@version`. |

## Bug-hunt discipline

Leonard's quality comes from a recurring loop: **bug-hunt → triage → fix → repeat**. Twelve rounds + five security reviews have shaped the codebase:

| Round | Surface audited | Findings | Documented as |
|---|---|---|---|
| Bughunt #1 | Initial v0.1 dogfood surface | 8 HIGH | [`audits/bughunt-1-triage.md`](./audits/bughunt-1-triage.md) |
| Bughunt #2 | Hooks, MCP, store, languages | 6 HIGH | [`audits/bughunt-2-triage.md`](./audits/bughunt-2-triage.md) |
| Bughunt #3 | Rust parser, skip-dirs, OTel, eval framework | 3 HIGH | [`audits/bughunt-3-triage.md`](./audits/bughunt-3-triage.md) |
| Security #1 | Cross-cutting security review | 2 HIGH (confused-deputy + memory amp) | [`audits/security-1-review.md`](./audits/security-1-review.md) |
| Bughunt #4 | v0.7–v0.12 surfaces | 4 HIGH | [`audits/bughunt-4-triage.md`](./audits/bughunt-4-triage.md) |
| Bughunt #5 | v0.19–v0.38 surfaces (tree-sitter + ledger hygiene) | 3 HIGH | [`audits/bughunt-5-triage.md`](./audits/bughunt-5-triage.md) |
| Bughunt #6 + Security #2 | v0.39–v0.45.1 surfaces (post-edit verifier, audits/ move) | **1 CRITICAL** + 9 HIGH | [`audits/bughunt-6-triage.md`](./audits/bughunt-6-triage.md) |
| Bughunt #7 | v0.49.0 — re-walk of the #6 carry-over backlog, promotion check | investigation-only | [`audits/bughunt-7-carryover.md`](./audits/bughunt-7-carryover.md) |
| Security #3 | v0.49.0 cross-cutting security review | — | [`audits/security-3-review.md`](./audits/security-3-review.md) |
| Bughunt #8 | v0.50.2 — fix verification + new-surface probe | investigation-only | [`audits/bughunt-8-verify.md`](./audits/bughunt-8-verify.md) |
| Security #4 | v0.50.2 cross-cutting security review | — | [`audits/security-4-review.md`](./audits/security-4-review.md) |
| Bughunt #9 | v0.51.0 — fix verification + security pass (iteration 3) | investigation-only | [`audits/bughunt-9-iteration-3.md`](./audits/bughunt-9-iteration-3.md) |
| Bughunt #10 | v0.52.0 — fix verification + security pass (iteration 4) | investigation-only | [`audits/bughunt-10-iteration-4.md`](./audits/bughunt-10-iteration-4.md) |
| Bughunt #11 + Security #5 | v1.0 release gate | all HIGH + MEDIUM landed | [`audits/bughunt-11-findings.md`](./audits/bughunt-11-findings.md) |
| Bughunt #12 | v0.52 + in-flight v0.53 — full L1–L8 sweep (cap edges, index correctness, concurrency, ground-truth adapter, MCP protocol fuzz, dogfood UX, path-trust on macOS/APFS) | 46 findings, 3 HIGH | [`audits/bughunt-12-findings.md`](./audits/bughunt-12-findings.md) |

Every CRITICAL and HIGH severity finding has been closed (CRITICAL was the v0.46.0 RCE-chain fix — see [`audits/security-2-review.md`](./audits/security-2-review.md)). Most MEDIUMs too — the remainder live in the deferred lists per round. Rounds #7–#10 were verification passes over prior fix rounds rather than new-finding sweeps; the full index, including per-lane documents, lives in [`audits/README.md`](./audits/README.md).

## Install

Requirements: **Go 1.25+** and a **Rust toolchain** (`cargo`) for the syn-based Rust extractor and the 29-grammar tree-sitter dispatcher. The `python3` extractor uses whatever Python is already on `PATH`.

```bash
git clone https://github.com/jasondillingham/leonard.git
cd leonard
go install ./cmd/...   # leonard, leonard-mcp, leonard-hook → $GOPATH/bin
```

For Rust source extraction (~9 s, 55 MB target):

```bash
(cd internal/parse/rust && cargo build --release)
```

For the 29 tree-sitter languages (~15–20 s, ~400 MB target cache):

```bash
(cd internal/parse/treesitter && cargo build --release)
```

Both Rust helpers compile once per machine. Leonard discovers them automatically; override the paths via `LEONARD_RUST_EXTRACTOR` and `LEONARD_TREESITTER_EXTRACTOR` if you install them elsewhere.

## Dogfood wiring (this repo)

Leonard is wired into its own development through `.claude/settings.local.json` (gitignored — personal config, not shared). The shape:

```jsonc
{
  "mcpServers": {
    "leonard": { "command": "/path/to/go/bin/leonard-mcp" }
  },
  "hooks": {
    "PreToolUse":  [{ "matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook pre-edit"  }]}],
    "PostToolUse": [{ "matcher": "Edit|Write|MultiEdit",              "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook post-edit" }]}],
    "SessionStart":[{ "matcher": "",            "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook session-start" }]}],
    "Stop":        [{ "matcher": "",            "hooks": [{ "type": "command", "command": "/path/to/go/bin/leonard-hook stop" }]}]
  }
}
```

Enable Leonard in a new project: `leonard init .`, then drop the same JSON into that project's `.claude/settings.local.json` and restart Claude Code in the directory.

## Per-project verifier (`[post_edit.verify]`)

By default the post-edit hook runs `go vet ./...` when a `go.mod` is at the project root, and records `verify=skipped (no go.mod)` otherwise. Projects in any other language can opt in to their own verifier by adding a section to `.leonard/config.toml`:

```toml
[post_edit.verify]
command = "cargo check --workspace"     # required: runs through `sh -c`
working_dir = ""                          # optional: defaults to project root
timeout = "60s"                           # optional: defaults to 60s; parsed via time.ParseDuration
```

When `command` is set, every Edit/Write triggers it. A non-zero exit becomes an unverified claim and surfaces in the model-facing additional context as `<verb> FAILED` (where `<verb>` is the first program + sub-command, e.g. `cargo check`). A zero exit records a verified claim — same shape as the Go path.

```toml
# Rust workspace
[post_edit.verify]
command = "cargo check --workspace"

# TypeScript (pnpm)
[post_edit.verify]
command = "pnpm tsc --noEmit"

# Python (combine ruff + mypy)
[post_edit.verify]
command = "ruff check . && mypy ."
timeout = "120s"
```

A missing or malformed `[post_edit.verify]` falls back silently to the Go default — projects that haven't opted in keep their current behavior byte-for-byte.

### Authorizing the command (`leonard config trust`)

Since v0.51 the verifier command **does not auto-execute**. After
configuring `[post_edit.verify].command`, run:

```bash
leonard config trust    # interactive — shows the command, asks for "yes"
```

This stores the SHA-256 fingerprint at
`$XDG_CONFIG_HOME/leonard/trust/<sha256-of-project-root>.sha256`
(per-user, outside the project tree — so a malicious `.leonard/`
write can't poison the fingerprint). The post-edit hook
recomputes the fingerprint on every edit and refuses to run
`sh -c` until it matches. If you edit the command later, re-run
`leonard config trust` to authorize the new shape.

Why: a cloned-from-elsewhere project shouldn't be able to execute
shell commands the moment you make your first edit. The trust step
is the user's "I've read this command, I accept what it does"
signal. Closes the entire Bash-obfuscation bypass class (the
audit trail in [`audits/security-4-review.md`](./audits/security-4-review.md)
enumerates 5 obfuscation forms that defeated the v0.50
command-string scanner).

## Project layout

```
cmd/{leonard,leonard-mcp,leonard-hook}    # the three primary binaries
cmd/leonard-sync-github                   # optional sync plugin: resolves GitHub-backed facts
internal/store                            # SQLite-backed data layer (schema v8)
internal/index                            # file walker + incremental dispatch
internal/parse                            # extractors — Go, Python, Rust, TypeScript natively; everything else via subprocess
internal/parse/rust                       # Cargo crate: syn-based Rust extractor
internal/parse/treesitter                 # Cargo crate: tree-sitter dispatcher (29 grammars)
internal/adapters                         # Adapter contract + registry (code, ground-truth, selflog)
internal/dispatcher                       # loads enabled adapters, fans hook events out, aggregates verdicts
internal/mcp                              # MCP tool handlers + StoreAdapter
internal/hooks                            # PreToolUse / PostToolUse / SessionStart / Stop handlers
internal/config                           # .leonard/config.toml loader + verifier trust store
internal/telemetry                        # OTel instrumentation (build-tag-gated)
examples/pydantic-ai                      # Python demo wiring leonard-mcp into a pydantic-ai agent
evals/inspect                             # Inspect (UK AI Security Institute) eval for fabrication rate
```

## Telemetry (optional)

v0.6 added build-tag-gated OpenTelemetry spans on the hot paths the bughunt-2 perf round flagged. **`leonard-hook` is the only binary currently instrumented** — `leonard-mcp` and `leonard` CLI compile identically under the tag (no spans emitted). Default builds have zero overhead — the no-op stubs compile in and the OTel SDK doesn't load. Rebuild with the `otel` tag to opt in:

```bash
go install -tags otel ./cmd/...
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
# Or for local debugging:
export OTEL_TRACES_EXPORTER=stdout
```

Spans produced: `leonard.pre-edit` / `.sibling-scan`, `leonard.post-edit` / `.index` / `.vet`. With no env vars set the tagged build still runs but exports nothing.

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the bug-hunt → triage → fix loop, language-addition pattern, and PR review expectations.

## Security

See [`SECURITY.md`](./SECURITY.md) for disclosure process. The path-trust + resource-cap + verifier-isolation guards are documented in [`audits/security-1-review.md`](./audits/security-1-review.md).

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).
