# Example: Compliance documentation

A compliance team using Claude Code to draft SOC 2 control
narratives, audit responses, and regulator-facing summaries. The
ground-truth tree prevents:

- Claiming controls the company doesn't actually have
- Claiming compliance with frameworks the company isn't certified
  for
- Quoting auditor findings before the audit is final
- Drafting responses without required legal-counsel routing

## Tree

```
truth-tree/                     # copy to your-project/.leonard/ground-truth/
├── facts.yaml                  implemented controls + technologies + data flows
├── stories.md                  canonical control-narrative phrasings
├── do-not-claim.md             framework gaps + auditor finding restrictions
├── filters.yaml                counsel-routing requirements
└── audit-log.md                (populated as artifacts are written)
```

## Domain choices encoded

**facts.yaml structure:**
- `framework` — which frameworks the org IS implementing
- `controls` — implemented controls (CC1-CC9 for SOC 2)
- `data_flows` — actual data paths (basis for narratives)
- `auditor` — engaged auditor + audit window

**do-not-claim.md categories:**
- Framework gaps (HIPAA, PCI, FedRAMP not in scope)
- Audit status (not complete until final report)
- Implementation claims (controls not yet in production)

**filters.yaml:**
- `content_filters` requires "see counsel routing in
  legal-routing.md" for any draft that references binding
  representations

## Try it

```
cd examples/ground-truth/compliance
leonard init --adapter=ground-truth
leonard config trust ground-truth

# Try drafting a control narrative:
echo "Our SOC 2 audit is complete." > control-cc6.md
leonard check control-cc6.md
```

Expected: one forbidden hit citing `Audit status#1`.
