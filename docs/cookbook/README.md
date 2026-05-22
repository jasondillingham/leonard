# Cookbook

Worked patterns for common ground-truth use cases. Each section is
a short recipe showing the relevant `facts.yaml` / `stories.md` /
`do-not-claim.md` / `filters.yaml` snippets plus the operator
workflow.

## Recipes

- [Preventing capability overclaims](./capability-overclaims.md) —
  product feature gaps that marketing copy might fudge
- [Customer legal-hold blocking](./customer-legal-hold.md) — block
  outreach + reference to customers under legal hold
- [Disclosure requirements](./disclosure-requirements.md) — force
  required disclaimers when sensitive content patterns appear
- [OSS contribution tracking](./oss-contribution-tracking.md) —
  keep `facts.oss_contributions` current via the github sync plugin
- [Founding-story canonicalization](./founding-story.md) — pin the
  canonical phrasing of a frequently-cited narrative

## Worked example projects

The end-to-end examples in [`examples/ground-truth/`](../../examples/ground-truth/)
ship complete fixture trees for three domains:

- `saas-product/` — SaaS marketing + customer messaging
- `compliance/` — SOC 2 control narrative scaffolding
- `personal-artifacts/` — résumés / cover letters / applications

See each example's README for the domain choices it encodes.
