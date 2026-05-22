# Recipe: founding-story canonicalization

You have a frequently-cited project narrative ("how we got
started," "the 2024 launch," "the pivot story") and you want
every artifact to use the same phrasing — same dates, same
numbers, same framing.

## The pattern

**1. Write the canonical version in `stories.md`.**

```markdown
## STORY: 2024 product launch

### Short version
ExampleSaaS launched Pro tier in March 2024. Within 90 days, 200+
teams had upgraded. We grew from $0 to $50K MRR by end of Q2.

### Long version
In March 2024, after 18 months of free-tier-only operation, we
launched the Pro tier with team collaboration features. The launch
hit 200+ upgrades in 90 days and brought us to $50K MRR by end of
Q2 2024. The decision to gate team features (rather than per-seat
pricing) came from a customer interview series in late 2023 — see
decision log entry 2023-12-04.

### Do NOT drift
- Don't claim "Pro tier launched January 2024" — actual date is March 2024
- Don't claim "we have N enterprise customers" without checking facts.yaml
- Don't quote MRR figures from before March 2024 — we were on free tier

### Sensitivity
public-safe
```

**2. Agents drafting artifacts ask `get_story`.**

```
# MCP tool call from Claude Code
get_story(name: "2024 product launch")
```

Returns the short + long form. The model uses the canonical text
rather than reinventing.

**3. Anti-drift notes catch common reinventions.**

`pending-audit.log` flags claims that drift from the canonical
form. Pair this with the forbidden-claim layer:

```markdown
# do-not-claim.md
## Launch narrative

- ❌ "Pro tier launched in January 2024" — actual date is March 2024
- ❌ "we had 500 upgrades in the first month" — actual was 200+ in 90 days
```

## Why prose stories rather than just facts

A fact like `product.launch_date: 2024-03-15` lives in
`facts.yaml`. The CANONICAL PHRASING — "after 18 months of
free-tier-only operation, we launched the Pro tier..." — is voice
+ framing, not data. It belongs in `stories.md` where:

- The exact wording is the deliverable
- The "do NOT drift" list pins what reviewers care about
- Sensitivity flags scope where the story can be quoted

## Operator workflow

When the story evolves (e.g., the team adds a new milestone):

1. Edit `stories.md` (warn-tier; nothing blocks)
2. Update the corresponding `do-not-claim.md` anti-claims if any
   are now stale (require-tier; needs rationale)
3. Run `leonard truth-edit --trivial "phrasing tweak" stories.md`
   if it's a small edit and you don't want to write a full
   rationale entry
4. The new story is picked up on next reload (~2s) — no restart

## Naming stories for lookup

Story names are case-insensitive but should be **distinctive**:

- ✅ `2024 product launch`
- ✅ `Founding`
- ✅ `Acme Corp partnership`
- ❌ `launch` (too ambiguous — what launch?)
- ❌ `the time we did the thing` (operators won't remember it)

Lookup via MCP:

```
get_story(name: "2024 PRODUCT LAUNCH")   // matches "2024 product launch"
get_story(name: "founding")               // matches "Founding"
get_story(name: "non-existent")           // returns ErrStoryNotFound (clear error, not empty success)
```

## What this doesn't do

The story system doesn't enforce that artifacts USE the canonical
text — it just makes it available. If an agent ignores
`get_story` and drafts a different version, the
`do-not-claim.md` anti-claims are the enforcement layer (those
DO block at pre-edit time when trusted).

For artifact templates that should pull the canonical text
directly, build a small generator script that calls the MCP tool
and pre-fills the artifact. Out of scope for v1.0.
