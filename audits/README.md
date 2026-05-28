# Audits

Bug-hunt + security-review history for Leonard. Every CRITICAL and
HIGH-severity finding referenced here has been closed in a versioned
fix round (see the project's top-level [`CHANGELOG.md`](../CHANGELOG.md)
for which version closed which finding).

## Index

| Round | Surface | Triage doc | Per-lane findings |
|---|---|---|---|
| Bughunt #1 | Initial v0.1 dogfood (hooks, MCP, selfhost, TypeScript) | [`bughunt-1-triage.md`](./bughunt-1-triage.md) | [`hooks`](./bughunt-1-hooks.md), [`mcp`](./bughunt-1-mcp.md), [`selfhost`](./bughunt-1-selfhost.md), [`typescript`](./bughunt-1-typescript.md), [`brief`](./bughunt-1-brief.md) |
| Bughunt #2 | Hooks, MCP, store/CLI, Python, pre-edit, integration | [`bughunt-2-triage.md`](./bughunt-2-triage.md) | [`cli`](./bughunt-2-cli.md), [`integration`](./bughunt-2-integration.md), [`mcp`](./bughunt-2-mcp.md), [`pre-edit`](./bughunt-2-pre-edit.md), [`python`](./bughunt-2-python.md) |
| Bughunt #3 | Rust parser, skip-dirs, OTel, eval framework | [`bughunt-3-triage.md`](./bughunt-3-triage.md) | [`rust`](./bughunt-3-rust.md), [`skip-dirs-prune`](./bughunt-3-skip-dirs-prune.md), [`otel`](./bughunt-3-otel.md), [`eval-framework`](./bughunt-3-eval-framework.md), [`integration`](./bughunt-3-integration.md) |
| Security #1 | Cross-cutting security review | [`security-1-review.md`](./security-1-review.md) | (single document) |
| Bughunt #4 | v0.7.1–v0.12.0 surface | [`bughunt-4-triage.md`](./bughunt-4-triage.md) | [`caps-and-limits`](./bughunt-4-caps-and-limits.md), [`mcp-and-hooks`](./bughunt-4-mcp-and-hooks.md), [`path-trust-deep`](./bughunt-4-path-trust-deep.md), [`rust-round-2`](./bughunt-4-rust-round-2.md), [`store-perf`](./bughunt-4-store-perf.md), [`integration`](./bughunt-4-integration.md) |
| Bughunt #5 | v0.19–v0.38 (tree-sitter + 24 languages + ledger hygiene) | [`bughunt-5-triage.md`](./bughunt-5-triage.md) | [`treesitter-dispatcher`](./bughunt-5-treesitter-dispatcher.md), [`languages`](./bughunt-5-languages.md), [`preprocessors`](./bughunt-5-preprocessors.md), [`verifier-and-ledger`](./bughunt-5-verifier-and-ledger.md), [`integration`](./bughunt-5-integration.md), [`perf-and-resource`](./bughunt-5-perf-and-resource.md) |
| Bughunt #6 + Security #2 | v0.39–v0.45.1 (post-edit verifier, audits/ move) | [`bughunt-6-triage.md`](./bughunt-6-triage.md) | [`fix-round-validation`](./bughunt-6-fix-round-validation.md), [`mcp-and-hooks-deep`](./bughunt-6-mcp-and-hooks-deep.md), [`launch-surface-detail`](./bughunt-6-launch-surface-detail.md), [`carry-over-medium-sweep`](./bughunt-6-carry-over-medium-sweep.md), [`store-and-eval-and-build`](./bughunt-6-store-and-eval-and-build.md), [`security-2-review`](./security-2-review.md) |
| Bughunt #12 | v0.52 + in-flight v0.53; full L1–L8 sweep (cap edges, index correctness, concurrency, ground-truth adapter, MCP protocol fuzz, real dogfood UX, path-trust macOS/APFS) — 46 findings, 3 HIGH | [`bughunt-12-findings.md`](./bughunt-12-findings.md) | [`brief`](./bughunt-12-brief.md), [`protocol-fuzz`](./bughunt-12-protocol-fuzz.md), [`dogfood`](./bughunt-12-dogfood.md), [`path-trust`](./bughunt-12-path-trust.md) |

> _Note: bughunt rounds 7–11 ran but were never added to this index. The files exist at `bughunt-{7..11}-*.md` / `security-{3..5}-review.md` — backfill the rows here when convenient._

## How rounds are structured

Each round follows the same shape:

1. **Brief** — what surfaces to audit, what's in scope, how lanes
   are partitioned. Done in conversation, not committed (the one
   exception is `bughunt-1-brief.md` which became the canonical
   findings-shape template).
2. **Per-lane findings** — multiple subagents independently audit
   different surfaces. Each writes `bughunt-N-<lane>.md` with
   findings tagged by severity (critical / high / medium / low /
   informational), each with a reproducer, observed/expected
   behavior, and a suggested fix shape.
3. **Triage** — `bughunt-N-triage.md` synthesizes themes across the
   lanes, picks the fix-round priority list, and explicitly notes
   what's deferred.
4. **Fix round** — one minor version bump per theme, with a commit
   message referencing the original finding IDs. Regression tests
   pin each fix.

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md) for how to contribute
findings or fixes in this shape.
