# `do-not-claim.md` schema

`do-not-claim.md` is the forbidden-claim list. Each rule names a
claim that should NOT appear in artifacts. When an edit's content
matches a rule, the pre-edit hook denies (if trusted) or warns (if
not).

---

## Shape

```markdown
# Do Not Claim

## Product capability gaps

- ❌ "ExampleSaaS supports HIPAA-compliant workflows" — We are NOT
  HIPAA-certified.
- ❌ "We have a mobile app" — No native mobile app. We have a
  responsive web UI.
- ❌ "Real-time collaboration" — Our sync is eventually-consistent
  with 5-30s lag. {fuzz: exact}

## Compliance

- ❌ "SOC 2 audit complete" — In progress, not complete.
- ❌ "GDPR compliant by default" {fuzz: exact}
```

---

## Section structure

- **`## <Category>`** — operator-chosen grouping (e.g.,
  "Product capability gaps", "Compliance", "Customer NDAs").
  Categories are surfaced in deny messages and in
  `leonard ground-truth stats`.
- **`- ❌ "<text>" — <reason>`** — one rule per bullet. The
  ❌ marker is canonical; the parser also accepts 🚫, X, x.
- **`{fuzz: N}` / `{fuzz: exact}`** — optional inline annotation
  (see "Fuzzy matching" below)

The reason after ` — ` (em-dash) is surfaced verbatim in the deny
message. Operators can also use ` -- ` or ` - ` as separators.

---

## Fuzzy matching

The matcher catches near-paraphrases by default with Levenshtein
distance ≤ 3 (`DefaultFuzzThreshold`). Override per rule:

| Annotation | Behavior |
|---|---|
| (none) | Default threshold (3) |
| `{fuzz: 5}` | Tolerate up to 5 character edits |
| `{fuzz: exact}` | No fuzz — must be a verbatim substring |

The two-pass matcher always prefers exact matches over fuzzy ones,
so an exact substring of the rule emits at the rule's exact
length even when surrounding text would also match fuzzily.

---

## Operator tips

### Verbatim claim text

Quote the **exact claim** as it might appear. The matcher
substring-matches; it isn't semantic.

- ✅ `"ExampleSaaS supports HIPAA-compliant workflows"`
- ❌ `"any HIPAA claim"` (too vague to match)

### Use `{fuzz: exact}` for ambiguous wording

If your rule could plausibly catch innocent prose ("we comply
with..." as part of a longer sentence), mark it `{fuzz: exact}`
to avoid false positives.

### Reason is operator-facing

The deny message shows the rule's reason. Write reasons that help
the operator fix the artifact:

- ✅ `"Not HIPAA-certified. Customers may add their own BAA layer."`
- ❌ `"don't say this"`

---

## What the matcher CAN'T do

Pure pattern matching can't distinguish "X is true" from "X is NOT
true." The current matcher emits a forbidden hit on either phrase
if "X" appears verbatim. See [`docs/adapters/ground-truth.md`](../adapters/ground-truth.md)
for the FP rate measured on the v0.7 corpus and the v1.0 hybrid
detection (#36) that aims to close this gap.

---

## Implementation pointers

- Parser: [`internal/adapters/groundtruth/donotclaim.go`](../../internal/adapters/groundtruth/donotclaim.go)
- Matcher: [`internal/adapters/groundtruth/fuzzy.go`](../../internal/adapters/groundtruth/fuzzy.go)
- FP corpus: [`evals/ground-truth/corpus/`](../../evals/ground-truth/corpus/)
- Test: `internal/adapters/groundtruth/fp_corpus_test.go`
