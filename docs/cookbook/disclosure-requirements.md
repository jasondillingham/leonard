# Recipe: disclosure requirements

When your content mentions certain topics (uptime guarantees,
competitive bids, performance numbers, security claims), legal /
compliance / brand wants a specific disclosure included.

## The pattern

**1. Configure `content_filters` in `filters.yaml`.**

```yaml
content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "see disclosures.md"

  - content_pattern: "(?i)guaranteed (uptime|sla)"
    required: "past-90-days uptime cited from facts.yaml"

  - content_pattern: "(?i)faster than"
    required: "benchmark conditions in benchmarks-2026.md"
```

**2. Edits that match the pattern but lack the disclosure deny.**

```
$ # Write a proposal.md containing "We are pleased to bid on contract A-100."
leonard: ground-truth pre-edit: Content matches content_filters[0]
but the required disclosure is missing: "see disclosures.md".
```

**3. Add the disclosure and re-edit.**

```markdown
We are pleased to bid on contract A-100. Per our policy, see
disclosures.md for any pre-existing arrangements with the issuer.
```

## How the matcher works

- `content_pattern` is a Go regex. `(?i)` makes it
  case-insensitive.
- `required` is matched as a case-insensitive substring (NOT a
  regex). Include the literal text you want to see.
- Both patterns are compiled at load time. Syntax errors fail
  `leonard ground-truth lint`.

## Common patterns

### Performance claims

```yaml
content_filters:
  - content_pattern: "(?i)faster than|outperforms"
    required: "benchmark conditions in benchmarks-2026.md"

  - content_pattern: "(?i)latency"
    required: "p50/p99 measured against canonical traffic"
```

### Compliance assertions

```yaml
content_filters:
  - content_pattern: "(?i)SOC ?2"
    required: "audit status: see compliance/soc2.md"

  - content_pattern: "(?i)HIPAA"
    required: "HIPAA scope limited per Section 5 of MSA"
```

### Pricing claims

```yaml
content_filters:
  - content_pattern: "(?i)cheapest|lowest price|free forever"
    required: "subject to fair-use limits in pricing.md §3.2"
```

## Why this isn't just a forbidden-claim rule

`do-not-claim.md` says "never say X." `content_filters` says "if
you say X, you MUST also say Y." They cover different shapes:

- The marketer should be ABLE to mention performance ("our pipeline
  is faster than the previous version") — but they should also
  cite the benchmark conditions.
- The lawyer should be ABLE to discuss SOC 2 — but the discussion
  must include scope context.

`do-not-claim.md` would be too blunt for these cases (everyone
needs to talk about performance). `content_filters` is the right
shape: a conditional disclosure requirement.

## When the disclosure is wrong

The match is exact-text. "see disclosures.md" requires that
literal phrase. If your team prefers a different wording, change
`required` to match — the disclosure text is operator-authored.

The reverse risk (operators including the disclosure but in a
form the matcher doesn't accept) is real. Document the canonical
form in `disclosures.md` or similar so operators know what to
write.

## What this doesn't catch

Operators can satisfy `required` with the literal phrase even when
they're not actually disclosing anything ("We promise this is
faster than the competition. See disclosures.md."). The matcher
can't audit *content* of the disclosure — just presence.

For real compliance, pair this with operator review of
`pending-audit.log` entries that contain content_filter triggers.
