# `facts.yaml` schema

`facts.yaml` is the positive-space truth source: things that **are
true** about the project's domain. Claims in artifacts are verified
against this file.

---

## Shape

The schema is intentionally flexible. Top-level keys are operator-
chosen categories; values are nested maps, lists, or scalars.

```yaml
# Product domain
product:
  name: ExampleSaaS
  features:
    - id: feature-search
      name: Universal search
      shipped: 2025-09-15
      tier: pro
  pricing:
    free_tier_users: 1
    pro_tier_users: 5

# Tech stack
tech_stack:
  primary_language: Go
  database: PostgreSQL

# Customers
customers:
  - id: acme-corp
    name: Acme Corp
    tier: enterprise
    last_renewal: 2026-01-15
    sensitivity: standard
```

---

## Per-entry metadata (recommended)

When facts have provenance, add these fields:

- `last_verified: YYYY-MM-DD` — when this fact was last checked
  against authoritative source. `leonard ground-truth stats`
  reports entries with stale or missing `last_verified`.
- `source: <url-or-ref>` — citation / where this came from
- `sensitivity: public | internal | private` — `list_facts`
  filters `private` entries unless `include_private: true`

---

## How verification works

The detector ([`detector.go`](../../internal/adapters/groundtruth/detector.go))
walks the parsed tree for any node whose scalar value matches a
detected claim. On match, the claim emits with:

- `Verdict: VerdictVerified`
- `EvidencePath: "<dotted.path>"` — the location in
  facts.yaml that satisfied the claim
- `EvidenceValue: <the matched value>`

The match is case-insensitive for strings, exact for
numbers/booleans. `time.Time` values (YAML dates) match against
`YYYY-MM-DD` formatted date claims.

---

## Sensitivity filtering

`list_facts` respects `sensitivity: private` markers:

```yaml
customers:
  - id: acme-corp
    name: Acme Corp
    contract_value_usd: 50000
    sensitivity: private   # entire entry filtered by default
  - id: beta-co
    name: Beta Co
    sensitivity: public    # always shown
```

A map with `sensitivity: private` is **dropped entirely** (whole
entry filtered, not just the sensitivity field). Lists of scalars
are untouched.

The MCP `list_facts(include_private: true)` short-circuits the
filter when a caller has opted in.

---

## Patterns

### Stable IDs

Add an `id:` field per entry. Tools that update facts.yaml (sync
plugins) key off `id` to update in place rather than appending.

### Date stamps

Use `last_verified: YYYY-MM-DD` per entry. Operators (and sync
plugins) stamp this on every refresh so stale data is visible.

### Source citations

Record where the fact came from:

```yaml
oss_contributions:
  - repo: anthropic/sdk-go
    number: 336
    status: closed
    source: https://github.com/anthropic/sdk-go/pull/336
    last_verified: 2026-05-22
```

`leonard sync github` ([`docs/sync-plugins.md`](../sync-plugins.md))
refreshes these against the GitHub API.

---

## What NOT to put in `facts.yaml`

- **Claims you're not certain of.** Use `unverified: true` or
  leave the entry out entirely.
- **Aspirational state.** Use a separate `roadmap.yaml` or similar.
- **Secrets.** This file is operator-readable; secrets belong in
  a secrets manager.
- **Anti-claims.** Use [`do-not-claim.md`](./do-not-claim.md).
