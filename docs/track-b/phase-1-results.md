# Phase 1 results — dogfooding the ground-truth adapter on Leonard

**Run:** 2026-07-27, at commit `aeb9a61`.
**Setup:** `.leonard/ground-truth/` seeded with six facts, all verified against the repo before
the run. Empty `do-not-claim.md`, `stories.md`, `filters.yaml` (read-only measurement; nothing
should block edits).
**Command:** `leonard list-stale-claims --scope '**/*.md'`
**Detector mode:** heuristic — the default. The LLM/hybrid path was not exercised.

## Headline

**The existing machinery does not solve this problem.** It produced **2,230 findings across 126
files** and caught **none** of the drift that motivated the track. Every one of the eight findings
it labeled `VERIFIED` was also wrong.

Signal, on this corpus, is zero.

## The numbers

| Category | Count |
|---|---|
| CONTRADICTION | 1,659 |
| UNVERIFIED | 571 (322 date, 241 quantitative, 6 personal, 2 tech) |
| VERIFIED | 8 |
| OPINION | 1 |
| **Files with findings** | **126** |

Contradictions by blamed fact: `bughunt_rounds` 884, `schema_version` 344, `security_reviews` 260,
`released_versions` 86, `treesitter_grammars` 85.

**`README.md` — which was corrected earlier the same day and is factually accurate — produced 69
findings, 67 of them contradictions.** That single number is the clearest statement of the
problem: a correct document is indistinguishable from a wrong one.

## Did it catch the real drift? No.

Controlled retrospective: the pre-fix `README.md` from commit `ceb3070` was extracted and scanned
in isolation. Line 15 read:

> **Status: v0.52.0 — stable.** Self-dogfooded across 46 minor releases with **six bug-hunt rounds
> and two focused security reviews**…

Four wrong values in one sentence — version, release count, round count, review count — against a
`facts.yaml` that declared all four correctly.

| Probe | Result |
|---|---|
| Any finding on line 15 | **none** |
| `"46"` flagged against `released_versions: 60` | **never flagged** |
| Word forms `"six"` / `"two"` flagged | **never flagged** |
| `v0.52.0` flagged against `version: 0.54.0` | **never flagged** |

The file produced 41 findings. Not one was the drift.

## Why it fails

The detector matches **bare numerals** against fact *values*, with no association between the
numeral and what it denotes. Representative false positives:

- `"2.0"` from the **Apache-2.0** license badge → contradicts `schema_version: 9`
- `"1"`, `"2"`, `"3"` — list markers, version fragments, arbitrary digits → contradict
  `bughunt_rounds: 12`
- `"54.0"` from a version string → contradicts `bughunt_rounds: 12`

And the eight `VERIFIED` findings are the same failure with the sign flipped:

```
[VERIFIED] quantitative — "5 MB"  evidence=leonard.security_reviews
```

**A file size in megabytes was accepted as evidence confirming the number of security reviews**,
because both contain a 5.

Meanwhile "46 minor releases" was *not* matched to `released_versions`, even though that pairing is
semantically obvious and the numeral is right there. So the matcher is simultaneously too loose
(any digit matches any fact) and too strict (the actual pairing was missed).

This is DOGFOOD open item 1 — numeric-token over-matching — confirmed on a new corpus and
substantially worse than that item describes. It is not prose-specific or job-hunt-specific.

## Fairness checks

- **Not a wiring problem.** `leonard ground-truth stats` confirmed the adapter loaded and read all
  six scalars, and contradictions were emitted with `facts=leonard.<key> (expected N)` — the
  mechanism engaged and produced answers. The answers were wrong.
- **Not an empty-`filters.yaml` problem.** `filters.yaml` governs path and content *forbidden*
  rules; it has no bearing on numeric contradiction matching. Nothing was left unconfigured that
  would have changed this.
- **Untested variable:** the heuristic detector was measured, not the hybrid/LLM path
  (`ModeHeuristic` is the default). An LLM pass might associate tokens with facts far better.
  That is worth testing before concluding the *approach* fails — this result establishes that the
  *default* fails.

## What this means for Phase 2

The finding is more useful than "we need resolvers," and it partly redirects the plan.

**The unsolved problem is claim→fact association, not truth resolution.** A resolver tells you
what the current value *is*. It does nothing about knowing which token in a document asserts that
value. Even with a perfect resolver for `bughunt_rounds`, this detector would still flag
`Apache-2.0` and still miss `46 minor releases`.

That has three consequences:

1. **It strengthens Phase 2's narrow, built-in checks.** The CI `grep` added in `3f01e7b` works
   precisely because it is explicit: *this* constant against *that* line. No association guessing.
   `doctor --drift` should be built the same way — structural comparisons between named things —
   and should not inherit the fuzzy matcher.
2. **It weakens the "just declare facts" hypothesis.** Phase 1's optimistic branch — declaring
   facts catches the drift with no new code — is disproven for this class of fact. Facts alone did
   not help.
3. **It raises the bar for Phase 3.** A general binding model is only worth building if bindings
   are *explicit* (this document line asserts this fact key). Inferring them is what just failed
   at scale.

**Recommendation:** proceed to Phase 2 with built-in structural checks only, and drop any
dependence on the heuristic claim detector. Treat Phase 3 as gated on an explicit-binding design,
not an inference one.

## Secondary finding worth its own item

Numeric over-matching makes the ground-truth adapter unusable on a technical repository as
configured. Any project with version numbers, licence identifiers, byte sizes, or ordered lists
will drown. Either the detector needs type/unit awareness (`5 MB` is not `5 reviews`) and explicit
binding, or numeric contradiction detection should be opt-in per fact rather than on by default.

This is worth filing on the public repo independently of Track B.

## Artifacts

The truth tree at `.leonard/ground-truth/` is retained — it is a valid fixture and the facts in it
are correct. It is now the input to whatever Phase 2 becomes.

Note the trap flagged in the build doc: those six facts are hand-maintained, and `schema_version`
already had to be written as 9 rather than 8 because Phase 0 bumped it hours earlier. The fact
table is drifting already.
