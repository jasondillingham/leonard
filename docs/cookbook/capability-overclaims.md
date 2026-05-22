# Recipe: preventing capability overclaims

You have a product, and marketing copy / blog posts / customer
emails are at risk of claiming features the product doesn't have.

## The pattern

**1. Record what IS true in `facts.yaml`.**

```yaml
product:
  name: ExampleSaaS
  features:
    - id: feature-search
      name: Universal search
      shipped: 2025-09-15
    - id: feature-export
      name: CSV export
      shipped: 2025-11-02
```

**2. Record what is NOT true in `do-not-claim.md`.**

```markdown
## Product capability gaps

- ❌ "ExampleSaaS supports HIPAA-compliant workflows" — We are NOT
  HIPAA-certified. Customers may add their own BAA layer.
- ❌ "We have a mobile app" — No native mobile app. We have a
  responsive web UI.
- ❌ "Real-time collaboration" — Our sync is eventually-consistent
  with 5-30s lag. Don't claim "real-time" or "instant."
```

**3. Trust the adapter so claims are blocked.**

```
leonard config trust ground-truth
```

**4. Use the CLI to verify before publishing.**

```
$ leonard check marketing/landing.md
marketing/landing.md: 1 finding(s) — 1 forbidden, ...
  [FORBIDDEN] forbidden — "ExampleSaaS supports HIPAA…"  rule=Product capability gaps#1
```

## Operator workflow

- Updating `facts.yaml` is a **warn-tier** edit per the
  self-logging policy. Save it; a draft entry lands in
  `pending-decisions.log` but nothing blocks.
- Updating `do-not-claim.md` is a **require-tier** edit. The
  selflog adapter wants a rationale. Either:
  - Run `leonard truth-edit --trivial "fix typo" .leonard/ground-truth/do-not-claim.md`
    for non-load-bearing edits, OR
  - Record a rationale out-of-band and set
    `LEONARD_TRUTH_CONFIRMED_FILES=...` for the edit.
- Re-publish artifacts AFTER `leonard check` returns clean.

## Why the two-file split

`facts.yaml` is the positive-space ground truth. The detector
checks claims against it to **verify** (mark as `verified` when
matched).

`do-not-claim.md` is the negative-space list. The detector checks
claims against it to **block** (mark as `forbidden`, and deny the
edit when trusted).

Both are needed because:

- Pattern matching alone (facts.yaml) can't say "this claim is
  forbidden" — only "this claim isn't verified."
- A forbidden list alone (`do-not-claim.md`) doesn't let claims
  *positively* succeed when they match a fact.

## What this doesn't catch

Negated forms ("we do NOT have a mobile app") still contain the
forbidden phrase verbatim and will trigger a forbidden hit. The
v0.7 fuzzy matcher has a measured false-positive rate of ~9% on
the corpus — see [`docs/adapters/ground-truth.md`](../adapters/ground-truth.md)
for the limit + v1.0 hybrid detection (#36) that aims to close
the gap.

For now: review `pending-audit.log` for negated forms and either
rewrite the artifact or use `leonard override --once` to bypass
the false positive.
