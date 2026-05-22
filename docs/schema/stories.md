# `stories.md` schema

`stories.md` holds canonical phrasings of common claims/narratives.
When an agent drafts something that references one of these
stories, it uses the canonical phrasing rather than reinventing.

The MCP `get_story(name)` tool returns the canonical text. Useful
for "founding story," "launch story," "the customer-XYZ
relationship," etc.

---

## Shape

```markdown
## STORY: 2024 product launch

### Short version
ExampleSaaS launched Pro tier in March 2024. Within 90 days, 200+
teams had upgraded.

### Long version
In March 2024, after 18 months of free-tier-only operation, we
launched the Pro tier with team collaboration features. The launch
hit 200+ upgrades in 90 days and brought us to $50K MRR by end of
Q2 2024.

### Do NOT drift
- Don't claim "Pro tier launched January 2024" — actual date is March 2024
- Don't quote MRR figures from before March 2024

### Sensitivity
public-safe
```

---

## Section headers

- **`## STORY: <name>`** — begins a story. `<name>` is the lookup
  key (case-insensitive). One per story.
- **`### Short version`** — concise canonical phrasing (~40 words)
- **`### Long version`** — paragraph-length form
- **`### Do NOT drift`** — bullet list of anti-drift notes. Each
  bullet becomes an entry in `story.DoNotDrift`.
- **`### Sensitivity`** — single line: `public-safe` | `internal`
  | `private`

All subsections are optional except `## STORY:`.

---

## Sensitivity values

- **`public-safe`** — fine to quote in public artifacts
- **`internal`** — quote only in internal docs / customer-facing
  but pre-cleared
- **`private`** — never quote externally; investigative reference
  only

The value is informational in v0.6 — agents and operators are
trusted to honor it. Future versions may gate `get_story` results
by sensitivity, matching the `facts.yaml` filter pattern.

---

## How `get_story` works

```
$ # MCP tool call
get_story(name: "2024 product launch")

{
  "name": "2024 product launch",        // canonical casing preserved
  "short": "ExampleSaaS launched...",
  "long": "In March 2024...",
  "do_not_drift": [
    "Don't claim 'Pro tier launched January 2024' — actual date is March 2024",
    "Don't quote MRR figures from before March 2024"
  ],
  "sensitivity": "public-safe",
  "line": 1                              // source line in stories.md
}
```

Lookup is case-insensitive. Missing story returns an error
(`ErrStoryNotFound`), not an empty success.

---

## Patterns

### Stories vs Facts

- **`facts.yaml`** is structured data (queryable, machine-
  consumable, often sync'd from sources).
- **`stories.md`** is curated prose (operator-authored, voice +
  framing matter).

A fact like `product.launch_date: 2024-03-15` lives in facts.yaml.
The narrative "we launched in March 2024 after 18 months of
free-tier" lives in stories.md as the canonical phrasing of how to
talk about that date.

### Story naming

Use short, lookup-friendly names:

- ✅ `2024 product launch`
- ✅ `Founding`
- ✅ `Acme Corp partnership`
- ❌ `How we got Pro tier off the ground in Q1 2024` (too long;
  case-insensitive but still verbose)

### Anti-drift specificity

Anti-drift notes work best when they cite **the exact wrong
phrasing** the agent might use:

- ✅ "Don't claim 'Pro tier launched January 2024' — actual date
  is March 2024"
- ❌ "Don't get the launch date wrong"

The specific form catches the drift; the vague form is forgettable.

---

## Implementation pointers

- Parser: [`internal/adapters/groundtruth/stories.go`](../../internal/adapters/groundtruth/stories.go)
- MCP tool: [`internal/adapters/groundtruth/mcp.go`](../../internal/adapters/groundtruth/mcp.go) (`getStory`)
