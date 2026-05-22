# Worked example projects

Three self-contained ground-truth trees demonstrating Leonard's
verification model across distinct domains. Each example is a
complete `.leonard/ground-truth/` fixture you can copy into your
own project as a starting point.

## Examples

| Directory | Domain | What it demonstrates |
|---|---|---|
| `saas-product/` | SaaS marketing + customer messaging | features, pricing tiers, capability gaps, customer NDA blocking |
| `compliance/` | SOC 2 control narratives | implemented controls, regulatory-claim gating, audit-trail requirements |
| `personal-artifacts/` | Résumés / cover letters / applications | employment history, skills, certifications-not-held, role-fit filters |

Each example has its own `README.md` explaining the domain choices
the fixture encodes.

## Using an example

```
# Copy the tree you want to start from:
cp -r examples/ground-truth/saas-product/.leonard/ground-truth /path/to/your-project/.leonard/

# Then customize for your real data:
cd /path/to/your-project
$EDITOR .leonard/ground-truth/facts.yaml
$EDITOR .leonard/ground-truth/do-not-claim.md
# ... etc

# Init Leonard if you haven't:
leonard init --adapter=ground-truth

# Trust the adapter so it can block forbidden claims:
leonard config trust ground-truth

# Verify a draft artifact:
leonard check path/to/your/draft.md
```

## What examples are NOT

- **Real customer data.** All examples use fictional company /
  customer / employee names. Replace before using in production.
- **Complete.** Examples are deliberately compact (~10-20 facts,
  ~5 rules) to be readable. Your real tree will be larger.
- **Schema-rigid.** The schemas are flexible. Each example shows
  ONE shape that works; your domain may want different
  conventions.
