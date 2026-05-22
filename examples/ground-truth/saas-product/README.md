# Example: SaaS product

A solo-founder SaaS company using Claude Code for marketing copy,
customer emails, blog drafts, and pitch decks. The ground-truth
tree prevents:

- Claiming features the product doesn't have
- Quoting pricing tiers from old marketing
- Naming customers who haven't approved case-study citation
- Bidding language without the required disclosure

## Tree

```
truth-tree/                     # copy to your-project/.leonard/ground-truth/
├── facts.yaml                  what IS true about the product
├── stories.md                  canonical launch + founding narratives
├── do-not-claim.md             capability gaps + customer NDAs
├── filters.yaml                blocked outreach paths + disclosure rules
└── audit-log.md                (populated as edits happen)
```

## Domain choices encoded

**facts.yaml structure:**
- `product` — name, features, current pricing
- `tech_stack` — implementation reality
- `customers` — approved-for-citation list (each marked with
  `sensitivity` flag)

**do-not-claim.md categories:**
- Product capability gaps (HIPAA, mobile, real-time)
- Compliance status (SOC 2 in progress)
- Customer relationship rules (Acme Corp under NDA)

**filters.yaml:**
- `path_filters` blocks edits to `customers/<name>/` for
  forbidden customer IDs
- `content_filters` requires disclosure when "bid on contract"
  appears

## Try it

```
cd examples/ground-truth/saas-product
# Init a project here (or copy the tree elsewhere first):
leonard init --adapter=ground-truth
leonard config trust ground-truth

# Try writing a doc that overclaims:
echo "ExampleSaaS supports HIPAA-compliant workflows." > marketing-draft.md
leonard check marketing-draft.md
```

Expected output: one forbidden hit, citing
`Product capability gaps#1`.
