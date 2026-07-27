# Phase 1 — dogfood the ground-truth adapter on Leonard

**Status:** ready to run. Ungated. Mostly procedure, not code.
**Cost:** about a day.
**Purpose:** measure how much of the drift problem the *shipped* machinery already solves, before
designing anything new.

## Why this comes first

`EnabledAdapters` (`internal/config/config.go:103`) auto-enables `ground-truth` only when
`.leonard/ground-truth/` exists. This repo's `.leonard/` holds `config.toml` and the database and
nothing else — no truth tree, no `facts.yaml`, no `[sync.*]`. **Only the `code` adapter loads.**

So the claim-checking half of Leonard has never been pointed at Leonard. The drift failures that
motivated this track happened in a project where the tool that would catch them was switched off.

Designing a new primitive before turning on the existing one would be guessing.

## Procedure

### 1. Create the truth tree

`.leonard/ground-truth/` with `facts.yaml`, `stories.md`, `do-not-claim.md`, `filters.yaml`
(the adapter loads all four; empty files are valid).

Seed `facts.yaml` with the facts this project has *actually* gotten wrong, drawn from the drift
cases and the doc-reconciliation commit (`3f01e7b`):

```yaml
leonard:
  version: "0.54.0"           # cmd/leonard/root.go Version
  schema_version: 8           # internal/store/schemaVersion
  bughunt_rounds: 12          # distinct bughunt-N in audits/
  security_reviews: 5         # security-N-review.md in audits/
  treesitter_grammars: 29     # tree-sitter-* deps in the crate's Cargo.toml
  released_versions: 60       # "## vX.Y.Z" headings in CHANGELOG.md
```

Every value above was wrong in the docs at some point today. That is the point of the fixture.

### 2. Enable and trust

The ground-truth adapter is a blocking adapter and requires an explicit trust marker:

```
leonard config trust ground-truth
```

Confirm it loads: the dispatcher should now report two adapters, not one.

### 3. Run the scan

```
leonard list-stale-claims --scope '**/*.md'
```

### 4. Record results

Write `docs/track-b/phase-1-results.md`. For each drift case, what happened:

| Case | Drift | Caught? | Notes |
|---|---|---|---|
| 1 | README "six bug-hunt rounds" vs 12 in `audits/` | ? | word-vs-numeral is the open question |
| 3 | README v0.52.0 vs `Version = "0.54.0"` | ? | |
| — | false positives | ? | count them; noise is a real cost |

## What the answers mean

**If cases 1 and 3 are caught:** Track B is mostly *"turn it on and write the facts down."*
Phase 2 shrinks to what `facts.yaml` structurally cannot express — binary provenance and git
state — and the binding model may not be needed at all.

**If they are missed:** the gap is real and now precisely characterized. Record *why* each was
missed, because that is the actual specification for Phase 2. Likely candidates:

- Word-vs-numeral (`"six"` vs `6`) — a detector-matching gap, cheap to fix.
- No mechanism ties a prose string to a Go constant — a genuine resolver gap.
- The facts have to be maintained by hand, so `facts.yaml` itself drifts — the strongest argument
  for resolvers, and worth calling out explicitly if observed.

**If it produces heavy false positives:** that is its own finding. DOGFOOD open item 1
(audit-log noise from numeric-token over-matching) predicts this, and Phase 1 either confirms it
on a new corpus or shows it was specific to prose-heavy job-hunt documents.

## The trap to avoid

The facts above are hand-maintained. Nothing keeps `bughunt_rounds: 12` correct when a thirteenth
round lands — this file can drift exactly like the README did.

**Do not fix that by hand during Phase 1.** Whether it drifts, and how fast, is a finding. It is
the cleanest possible argument for resolvers: if the fact table needs a resolver to stay honest,
Phase 2 is justified by direct evidence rather than by reasoning.

## Definition of done

`docs/track-b/phase-1-results.md` exists with the table filled in, false-positive count recorded,
and one paragraph on what it implies for Phase 2 scope. That paragraph is the deliverable.
