# Leonard — Bug Hunt #2 Triage

> **Five parallel investigations.** ~70 findings total across `python`, `cli`,
> `pre-edit`, `mcp`, and `integration` lanes. This doc decides what's worth
> fixing now and what gets parked for v0.3.

## Decision

**Fix all HIGH-severity findings + the systemic dead-config theme + the
ground-truth drift theme.** Everything else is logged and deferred.

Reasoning: the Round 2 corpus reveals a pattern the per-lane reports didn't
emphasize individually — Leonard is shipping documentation it doesn't honor.
Five separate findings (across four lanes) all reduce to "we promised a config
knob / env var / behavior and the code never reads it." That's the same
category of failure the project exists to prevent in Claude's output, so it's
priority #1 to close.

The HIGH findings are independently load-bearing:

- **Subprocess Python with no timeout** (a hung interpreter freezes the
  indexer until it's killed)
- **Malformed JSON-RPC crashes the MCP server** (any noisy parent process
  takes down Claude's tool surface)
- **`leonard init` silently overwrites a customized config.toml** (data loss
  on re-init; the user's tunables vanish)
- **`leonard index` never removes file rows for deleted files** (`verify`
  keeps returning matches for code that doesn't exist — the exact "ground
  truth lying to Claude" failure mode the project is built to prevent)

MEDIUMs are real bugs but recoverable. Several are perf gaps or correctness
edges that didn't trigger in any audited corpus. They wait.

## Cross-cutting themes (each gets a single fix sweep)

### Theme A — Dead configuration surface

A user editing `.leonard/config.toml` to tune Leonard sees no effect for most
documented knobs. Specific dead items (one fix per row, or a single
config-plumbing sweep):

| Knob / env var | Where documented | Where consumed |
|---|---|---|
| `[verifiers].go = [...]` | DESIGN.md §4.6, `init.go` writes default | nowhere — post-edit hardcodes `go vet ./...` |
| `[verifiers].python = [...]` | same | nowhere |
| `[verifiers].typescript = [...]` | same | nowhere |
| `[index].languages = [...]` | DESIGN.md §4.6, init writes default | nowhere — indexer.go uses a hardcoded `langExtractors` map |
| `[index].ignore = [...]` | same | nowhere — only `.gitignore` / `.leonardignore` are honored |
| `[hooks].block_on_fabricated_symbol` | DESIGN.md §4.6, init writes default `true` | nowhere — pre-edit always blocks |
| `[hooks].vet_timeout` | nowhere documented but on `PostEditOptions` | only the test harness sets it |
| `[hooks].evidence_cap` | same | same |
| `$LEONARD_PYTHON` | `python.go` line 28 comment, the error string at parse-failure time, the v0.2 commit message | nowhere — `pythonInterpreter` is a hardcoded `"python3"` constant |

**Recommended sweep:** one PR that either (a) plumbs each value through, or
(b) removes it from defaults/docs. Pick per-knob: `[verifiers]` is real
product surface and should be plumbed; `[hooks]` knobs are testing artifacts
and should be removed from the doc; `$LEONARD_PYTHON` should be read by
`exec.LookPath` (3 lines).

### Theme B — Ground-truth drift

The index can fall out of sync with disk in ways the user can't detect from
inside Leonard:

- `cli F2`: `leonard index` walks the filesystem and updates rows for files
  it finds — but never deletes rows for files that vanished. After
  `rm internal/foo.go && leonard index`, `verify foo` keeps returning the
  stale match. This is the single most load-bearing bug in Round 2: it
  breaks the "ask Leonard, get truth" contract for any project where files
  get deleted.
- `cli F18`: doctor reports the same stale file as both "parse-failure
  suspect" and "stale file" — overlapping classifications.
- `integration F6`: indexer walks `testdata/`, pre-edit sibling-scan skips
  it. Two views of "in scope" disagree.
- `integration F4`: zero tests run migrations against a real prior-schema DB.
  A bad migration would silently corrupt the store on upgrade.

**Recommended sweep:** add a stale-row pruner to the IndexAll walk (after
collecting visited paths, `DELETE FROM files WHERE path NOT IN (...)` —
symbols cascade), align the testdata policy between indexer and pre-edit,
and add a migration smoke test.

## Lane summaries

### Lane A — `python` (11 findings, file `bughunt-2-python.md`)

| ID | What | Severity |
|---|---|---|
| F1 | `LEONARD_PYTHON` env var documented but never read | **high** |
| F2 | No subprocess timeout — a hung python3 freezes the indexer | **high** |
| F3 | Stderr truncated at first newline — real error class lost on non-2 exits | medium |
| F6 | Nested classes / conditional defs silently dropped | medium |
| F8 | Files with non-UTF8 declared encoding can't be parsed | medium |
| F4 | LookPath runs per-call (comment claims once) | low |
| F5 | Async functions render as `def …` not `async def …` | low |
| F7 | `name = lambda: ...` indexed as `kind=var` | low |
| F9 | `__all__` ignored | informational |
| F10 | PEP 695 `type Alias = …` not extracted | informational |
| F11 | Property getter/setter pair = duplicate qualified_name rows | informational |

**Solid:** modern syntax acceptance (the headline win), Unicode identifiers,
NUL/empty inputs, 3 MB / 50k-function file in 2s, SyntaxError surfacing.
Zero parse failures across 733 files of pip+setuptools+wheel.

### Lane B — `cli` (22 findings, file `bughunt-2-cli.md`)

| ID | What | Severity |
|---|---|---|
| F1 | `leonard init` overwrites custom config.toml (data loss) | **high** |
| F2 | `leonard index` doesn't remove rows for deleted files | **high** |
| F3 | `$LEONARD_PYTHON` documented in error text but unimplemented | medium |
| F5 | `config.toml` is dead code from the CLI's perspective | medium |
| F6 | `doctor` always exits 0 — no CI gating signal | medium |
| F7 | `decisions list --limit 0` returns 50 not the 20 promised | medium |
| F9 | CLI doesn't walk up to find `.leonard/` — fails from subdirs | medium |
| F4, F8, F10, F11, F19 | doc drift, help-text gaps, exit-code inconsistency | low |
| F12 | No JSON output mode | informational |
| F14, F15 | `decisions add` accepts empty fields, lossy arg join | low |
| F16, F17, F18, F20–F22 | smaller issues — see file | low / informational |

**Solid:** 30 concurrent decision writers work, `.gitignore` integration,
Unicode paths, SQL-meta-character safety, symlinked project dirs.

### Lane C — `pre-edit` (13 findings, file `bughunt-2-pre-edit.md`)

No HIGH. The F8 sibling-scan from Round 1 is functionally correct but
introduces several behavioral gaps:

| ID | What | Severity |
|---|---|---|
| F1 | Sibling-scan skip list diverges from indexer's (`dist`, `build` leaks; no `.gitignore`) | medium |
| F2 | Nested `go.mod` boundaries not respected in workspace monorepos | medium |
| F3 | Unconditional walk per pre-edit — 480ms warm at 10k files (over 200ms budget) | medium |
| F4 | `block_on_fabricated_symbol` config dead | medium |
| F10 | Post-edit missing-file path doesn't reach the model via additionalContext | medium |
| F5–F9, F11–F13 | smaller correctness/perf gaps — see file | low |

**Solid:** symlink safety, generics (IndexExpr/IndexListExpr), MultiEdit
coverage, source-gating (F7), F4 vet-fail additionalContext.

### Lane D — `mcp` (15 findings, file `bughunt-2-mcp.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Malformed JSON-RPC line crashes server with exit 1, no error frame | **high** |
| F2 | `find_symbol` undercount when language/kind + limit combined (store LIMITs before the MCP filter) | medium |
| F3–F6 | empty `choice`/`reasoning`/`evidence` slip past required schemas; `limit=0` claim mismatch | low |
| F7 | Unknown/mis-cased `language` filter silently returns `[]` | low |
| F8 | `<invalid reflect.Value>` substring leaks into client error messages | low |
| F9–F15 | minor schema-honesty gaps — see file | low / informational |

**Solid:** Round 1's F1–F5 fixes all hold up under real-client driving.
Concurrency, FD hygiene, long-running sessions, DB-replacement detection
(both lazy and the 2s watcher), JSON-RPC schema validation.

### Lane E — `integration` (9 findings, file `bughunt-2-integration.md`)

| ID | What | Severity |
|---|---|---|
| F1 | Multiple documented config knobs + env vars dead (consolidated in Theme A above) | **high** |
| F2 | DESIGN.md, README.md, code disagree in 7 specific places | medium |
| F3 | `wire_real.go` has 0% test coverage (the production runtime path) | medium |
| F4 | No tests for binary subprocess, real stdio MCP, or schema migrations | medium |
| F5 | Two simultaneous `leonard init` invocations race | low |
| F6 | indexer walks testdata, pre-edit's sibling-walker skips it | low |
| F7 | Stop hook can't reach the model via additionalContext (Round 1 deferred) | low |
| F8 | `transcript_path` field never read | informational |
| F9 | Makefile references long-removed build tag / lane structure | informational |

**Solid:** concurrency (100 parallel post-edit hooks, 0 lock errors),
`go test -race` clean, pure-Go build with `CGO_ENABLED=0` works, supersede-
on-fix end-to-end, decision-list ordering deterministic.

## What's deferred to a later round (MEDIUM and below)

Documented per-finding in each lane file. The notable cluster worth
mentioning here as a "next round's bughunt-3 candidates" group:

- Pre-edit performance is the biggest single deferred risk — `pre-edit F3`'s
  480ms warm at 10k files. Worth measuring against the user's actual largest
  project before deciding whether to optimize.
- Python coverage gaps (`python F6` nested classes, `python F8` non-UTF8,
  `python F10` PEP 695 type aliases, `python F11` getter/setter dupes) are
  all v0.3 candidates if real-world Python use surfaces them.
- MCP schema honesty (`mcp F2` undercount with filter+limit) needs a
  decision: filter-before-limit semantics rework, or document the limit as
  "after raw query, before filter".
- Doc drift (`integration F2`) is a separate work item — one docs-update
  PR can close most of it.

## What stays explicitly out of scope

- LOW + INFORMATIONAL findings (catalogued for completeness, not for fixing).
- New tools / new languages / new hook types (DESIGN.md §6 future work).
- Performance benchmarks beyond what's already in `parse_bench_test.go`
  and `index_bench_test.go`.
- OSS-readiness audit (LICENSE headers, package docs, contributor guide).

If a fix-round lane discovers a new HIGH-severity issue while implementing,
surface it and decide on the spot whether to fold it in or punt — same rule
as Round 1. **Don't extend scope without surfacing the decision.**
