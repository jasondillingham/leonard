# Example: Personal artifacts

An individual using Claude Code to draft cover letters, application
forms, LinkedIn posts, and résumé updates. The ground-truth tree
prevents:

- Overclaiming skills not actually held
- Naming employers from outside the actual employment history
- Claiming certifications not earned
- Targeting companies the operator has explicitly ruled out

This is the original motivating use case for the v0.6 ground-truth
amendment — see [`docs/ROADMAP-v1-ground-truth.md`](../../../docs/ROADMAP-v1-ground-truth.md).

## Tree

```
truth-tree/                     # copy to your-project/.leonard/ground-truth/
├── facts.yaml                  employment / projects / OSS / skills / certifications
├── stories.md                  canonical career narratives
├── do-not-claim.md             skills not held + certifications not earned + comp rules
├── filters.yaml                company-fit filters + role-shape filters
└── audit-log.md                (populated per application)
```

## Domain choices encoded

**facts.yaml structure:**
- `employment` — real positions held with dates
- `oss_contributions` — public OSS work (sync'd via
  `leonard sync github`)
- `skills` — claimed-and-defensible skills only
- `certifications` — actual certs held
- `compensation_floor` — minimum acceptable comp (private)

**do-not-claim.md categories:**
- Skills not at production level
- Certifications not held
- Compensation negotiation rules

**filters.yaml:**
- `path_filters` blocks writing to `applications/<company>/` for
  ruled-out companies (compensation gap, values mismatch, etc.)
- `content_filters` requires the comp floor be cited when "salary
  expectations" appears

## Try it

```
cd examples/ground-truth/personal-artifacts
leonard init --adapter=ground-truth
leonard config trust ground-truth

# Try drafting a cover letter with an inflated skill claim:
echo "I have deep expertise in Rust." > cover-letter-draft.md
leonard check cover-letter-draft.md
```

Expected: one forbidden hit (or unverified) — depends on whether
"Rust" is in your facts.yaml skills list.
