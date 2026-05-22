# Self-logging truth changes

Leonard's claim verifier checks artifacts against the truth tree.
Self-logging is the symmetric piece: **when the truth tree itself
changes, the change pairs with a recorded rationale.** Over time
the rationale log becomes the project's narrative — not just what
the truth was at any point, but why it became that way.

The mechanism is the existing decision log extended with a
`truth_change` block. The operator-facing surfaces are:

- `leonard truth-history <file>` — narrative for one file
- `leonard truth-story` — full chronological narrative
- `leonard truth-edit --trivial "reason"` — bypass require-tier
  for typo fixes

This doc covers the **principle**, the **policy tiers**, the
**auto-draft mechanism**, how to **read truth-story output**, and
**rollback / disablement**.

---

## The principle

Two scopes count as a "truth change":

1. **Domain truth** — edits to files under `.leonard/ground-truth/`
   (facts.yaml, stories.md, do-not-claim.md, filters.yaml). The
   truth the project verifies artifacts against.
2. **Toolkit truth** — edits to Leonard's own source, config, or
   roadmap that change what Leonard considers verifiable: adapter
   implementations, the trust system, hook handlers, the schema
   doc itself.

Both deserve a rationale. Domain-truth changes affect what the
project will accept in artifacts going forward. Toolkit-truth
changes affect what Leonard means by "verified" going forward.

The audit log (`audit-log.md`) handles a third scope — *artifact*
claims — answering "what was said and did it verify?" Self-logging
handles "why did truth move?" The two are complementary; neither
replaces the other.

---

## Policy tiers

Different truth files have different stakes. The selflog adapter's
default policy ([`internal/adapters/selflog/policy.go`](../internal/adapters/selflog/policy.go)):

| Path | Scope | Tier |
|---|---|---|
| `.leonard/ground-truth/audit-log.md` | domain | **skip** |
| `.leonard/ground-truth/do-not-claim.md` | domain | **require** |
| `.leonard/ground-truth/filters.yaml` | domain | **require** |
| `.leonard/ground-truth/facts.yaml` | domain | **warn** |
| `.leonard/ground-truth/stories.md` | domain | **warn** |
| `internal/adapters/` | toolkit | **require** |
| `internal/trust/` | toolkit | **require** |
| `cmd/` | toolkit | **warn** |
| `docs/ROADMAP*` | toolkit | **warn** |

### Tier semantics

- **skip** — no rationale logging. Used for files that are
  themselves logs / generated (e.g., `audit-log.md`).
- **warn** — drafts a rationale entry in
  `.leonard/pending-decisions.log` but doesn't block the edit. The
  operator sees a stderr line; the edit proceeds.
- **require** — blocks the edit (denies) when:
  1. The adapter is trusted (`leonard config trust self-logging`),
     AND
  2. No bypass is active (no `--trivial` token, no `LEONARD_TRUTH_CONFIRMED_FILES` env var)
- **require** without trust → falls back to warn behavior. The
  operator sees a hint to run `leonard config trust self-logging`
  to enable blocking; the edit proceeds.

---

## Auto-draft mechanism

When PostEdit touches a truth file, the selflog adapter writes a
**draft entry** to `.leonard/pending-decisions.log`:

```json
{
  "ts": "2026-05-22T14:23:00Z",
  "session_id": "claude-code-session-abc",
  "tool": "Edit",
  "file_path": "ground-truth/do-not-claim.md",
  "scope": "domain",
  "tier": "require",
  "draft": {
    "motivated_by": "Auto-drafted on edit to ground-truth/do-not-claim.md. Add rationale before promoting."
  }
}
```

The `motivated_by` is a v0.6 placeholder. v1.0 doesn't yet draft
rationale from session context — that's a future enhancement
([roadmap amendment §"Auto-draft mechanism"](./ROADMAP-v1-ground-truth.md#auto-draft-mechanism)).
For now operators review pending drafts and promote them into the
decisions DB via `record_decision` (MCP) or by re-issuing through
session-aware tooling.

### Bypass mechanisms

For require-tier edits operators have three ways to proceed:

1. **`leonard truth-edit --trivial "reason" <path>`** — writes a
   single-use bypass token (5-minute TTL) at
   `.leonard/pending-trivial/<sha256>.json`. The next matching
   PreEdit consumes the token, allows the edit, and logs a
   trivial draft entry. Use this for typos, whitespace,
   formatting.
2. **`LEONARD_TRUTH_CONFIRMED_FILES=<rel-path>`** — env var listing
   files for which a rationale has been recorded out-of-band. The
   adapter checks this on each PreEdit. Used by scripted workflows
   that record rationale explicitly before editing.
3. **`LEONARD_TRUTH_TRIVIAL=<rel-path>`** — env var equivalent of
   `--trivial` for one-off shell invocations.

---

## Reading `truth-story` output

```
$ leonard truth-story --scope=domain --since=2026-01-01
# Truth-source change narrative

3 entries shown.

[#7] 2026-04-15 12:00 UTC  scope=domain
  topic:        initial-rule
  motivated_by: Initial HIPAA rule
  diff:         git:a1b2c3d

[#12] 2026-05-02 09:15 UTC  scope=domain  supersedes=#7
  topic:        tighten-rule
  motivated_by: User flagged ambiguity; tightening wording.
  diff:         git:e8eced8

[#18] 2026-05-22 14:23 UTC  scope=domain
  topic:        add-soc2-rule
  motivated_by: Audit kicked off, add the rule before legal review.
  diff:         git:5e9c450
```

Each entry:

- **`[#N]`** is the decision-log row ID (use `leonard
  truth-history --include-trivial` to drill in)
- **`scope=`** is `domain` or `toolkit`
- **`supersedes=#N`** marks the entry as a chain link — the
  earlier `#N` was the previous version of this rule/file
- **`topic:`** is the operator-supplied subject line
- **`motivated_by:`** is the rationale
- **`diff:`** is the change reference (git SHA or content hash)

Format options:

- `--format=markdown` (default) — the shape above
- `--format=json` — full schema with all TruthChange fields
- `--format=plain` — tab-separated one entry per line; pipe to
  awk/grep

Filters:

- `--scope=domain|toolkit` — restrict
- `--since=YYYY-MM-DD` — date lower bound
- `--include-trivial` — show `--trivial` bypass entries (collapsed
  by default with a footer count)
- `--limit=N` — cap entries (default 200)

---

## Rollback / disablement

Every part of self-logging is opt-out.

### Stop the adapter

Remove the `[[adapters]]` entry of type `self-logging` from
`.leonard/config.toml`. The hook still runs (the truth files are
read by the ground-truth adapter for its own purposes) but no
draft entries are logged.

### Stop blocking

`leonard config trust self-logging` is the trust grant. Remove the
marker file at
`$XDG_CONFIG_HOME/leonard/trust/<project-hash>.self-logging.trust`
to revert require-tier edits to warn-style (advisory, non-blocking)
behavior.

### Clear pending drafts

`.leonard/pending-decisions.log` is append-only. To start fresh,
delete the file. The next PostEdit creates a new one.

### Disable a tier

Override the policy in `.leonard/config.toml`:

```toml
[[truth_change_log.policy]]
path = ".leonard/ground-truth/do-not-claim.md"
tier = "warn"   # downgrade from require → warn
```

(Config-driven policy override is a v0.8 follow-up; v0.6/v0.7 ship
with the policy hardcoded in `internal/adapters/selflog/policy.go`.)

---

## Why this matters

Leonard's existing decision log records project decisions. The
self-logging amendment extends that to **changes to the project's
verification rules themselves**. A team six months from now —
including the future you — can ask:

- Why is this `do-not-claim.md` rule worded this way?
- When did `tech_stack.primary_language` change from Python to Go?
- What was the rationale for tightening the path_filter rule?

Without self-logging, those answers live in git history (if you
remember to write good commit messages) or in your head (if you
remember at all). With self-logging, they live alongside the truth
they explain.

The cost: operators have to record rationale for require-tier
edits. The mitigation: `--trivial` for non-load-bearing tweaks;
warn tier for files that churn frequently.

---

## Implementation pointers

- Policy table: [`internal/adapters/selflog/policy.go`](../internal/adapters/selflog/policy.go)
- Adapter: [`internal/adapters/selflog/adapter.go`](../internal/adapters/selflog/adapter.go)
- Decision-log schema extension: [`internal/store/store.go` (`TruthChange`)](../internal/store/store.go)
- CLI: [`cmd/leonard/truth_edit.go`](../cmd/leonard/truth_edit.go),
  [`cmd/leonard/truth_history.go`](../cmd/leonard/truth_history.go),
  [`cmd/leonard/truth_story.go`](../cmd/leonard/truth_story.go)
- MCP tool: [`internal/mcp/truth_history.go`](../internal/mcp/truth_history.go)
- Roadmap section: [`ROADMAP-v1-ground-truth.md` — "Self-logging truth changes"](./ROADMAP-v1-ground-truth.md#amendment-2026-05-21--self-logging-truth-changes)
