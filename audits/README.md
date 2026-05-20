# Audits

Bug-hunt + security-review history for Leonard. Every HIGH-severity
finding referenced here has been closed in a versioned fix round
(see the project's top-level [`CHANGELOG.md`](../CHANGELOG.md) for
which version closed which finding).

## Index

| Round | Date | Triage doc | Per-lane findings |
|---|---|---|---|
| Bughunt #1 | v0.1–v0.4 surface | [`bughunt-1-triage.md`](./bughunt-1-triage.md) | `bughunt-1-hooks.md`, `bughunt-1-mcp.md`, `bughunt-1-selfhost.md`, `bughunt-1-typescript.md` |
| Bughunt #2 | v0.4–v0.5 surface | [`bughunt-2-triage.md`](./bughunt-2-triage.md) | `bughunt-2-cli.md`, `bughunt-2-integration.md`, `bughunt-2-mcp.md`, `bughunt-2-pre-edit.md`, `bughunt-2-python.md`, `bughunt-2-store.md`, `bughunt-2-typescript.md` |
| Bughunt #3 | v0.5–v0.7 surface | [`bughunt-3-triage.md`](./bughunt-3-triage.md) | `bughunt-3-rust.md`, `bughunt-3-skip-dirs.md`, `bughunt-3-otel.md`, `bughunt-3-eval.md` |
| Security #1 | Cross-cutting security review | [`security-1-review.md`](./security-1-review.md) | (single document) |
| Bughunt #4 | v0.7.1–v0.12.0 surface | [`bughunt-4-triage.md`](./bughunt-4-triage.md) | `bughunt-4-caps.md`, `bughunt-4-mcp.md`, `bughunt-4-path-trust.md`, `bughunt-4-store-perf.md`, `bughunt-4-cli.md`, `bughunt-4-pre-edit.md` |
| Bughunt #5 | v0.19–v0.38 surface | [`bughunt-5-triage.md`](./bughunt-5-triage.md) | `bughunt-5-treesitter-dispatcher.md`, `bughunt-5-languages.md`, `bughunt-5-preprocessors.md`, `bughunt-5-verifier-and-ledger.md`, `bughunt-5-integration.md`, `bughunt-5-perf-and-resource.md` |

## How rounds are structured

Each round follows the same shape:

1. **Brief** — what surfaces to audit, what's in scope, how lanes
   are partitioned. Done in conversation, not committed.
2. **Per-lane findings** — multiple subagents independently audit
   different surfaces. Each writes `bughunt-N-<lane>.md` with
   findings tagged by severity (high / medium / low / informational),
   each with a reproducer, observed/expected behavior, and a
   suggested fix shape.
3. **Triage** — `bughunt-N-triage.md` synthesizes themes across the
   lanes, picks the fix-round priority list, and explicitly notes
   what's deferred. Committed.
4. **Fix round** — one minor version bump per theme, with a commit
   message referencing the original finding IDs. Regression tests
   pin each fix.

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md) for how to contribute
findings or fixes in this shape.
