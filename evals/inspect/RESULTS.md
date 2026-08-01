# Eval results — fabrication rate

Recorded runs of the Inspect harness. **`logs/` is gitignored** (the `.eval` files are large and
machine-specific), so a run leaves no trace in the repo unless it is written down here. That is
the entire reason this file exists — see
[`ROADMAP-v2`](../../docs/ROADMAP-v2-measurement-and-drift.md) §1.4 step 4.

> **Status: no real-model run has happened yet.** Every `.eval` log on disk used
> `model=mockllm/model`, two of six errored, and the rest scored 0.0. They establish nothing.
> The table below is empty on purpose. Do not cite a number from `logs/`.

---

## Before you run

Three prerequisites.

The scorer finds `leonard-hook` on its own: `$LEONARD_HOOK_BIN` if set, else PATH, else
`$(go env GOPATH)/bin/leonard-hook` (the default `go install` target, which is *not* on PATH by
default). So `go install ./cmd/...` is normally enough. Set `LEONARD_HOOK_BIN` explicitly if you
need to score against a specific build — a bad value raises rather than silently falling back to a
different binary.

```bash
cd evals/inspect

# 1. Inspect + deps
uv sync

# 2. Build the binaries the scorer and the MCP arms need
go install ../../cmd/...

# 3. Fresh index — the scorer's oracle is .leonard/leonard.db
(cd ../.. && leonard index)

export ANTHROPIC_API_KEY=sk-...
```

Run all four arms against the same model:

```bash
uv run inspect eval \
    tasks.py@fabrication_control \
    tasks.py@fabrication_prompt_only \
    tasks.py@fabrication_with_leonard \
    tasks.py@fabrication_with_leonard_system_prompt \
    --model anthropic/claude-sonnet-4-5

uv run inspect view
```

Cost is roughly $0.50–$2 per full pass; the treated arms are chattier because each tool call is
another round-trip.

---

## Recording a run

Copy the block below, fill it in, commit it in the same change as any code it depended on.

### Run YYYY-MM-DD — `<model>` @ `<git-sha>`

| Arm | n | Clean-sample rate | Fabricated refs / sample | `no_code_block` | `scorer_error` |
|---|---|---|---|---|---|
| `fabrication_control` | | | | | |
| `fabrication_prompt_only` | | | | | |
| `fabrication_with_leonard` | | | | | |
| `fabrication_with_leonard_system_prompt` | | | | | |

**Failure taxonomy** — group the non-clean samples by *shape*, not count:

| Cluster | Arms affected | n | Example fabricated reference |
|---|---|---|---|
| | | | |

**Notes / anomalies:**

---

## Rules for reporting a number

These are not optional polish. Each one exists because the harness will otherwise produce a
number that reads as meaningful and isn't.

1. **Exclude `scorer_error` from the reported rate outright.** It means the scorer failed, not
   that the model fabricated. Report the count separately. (`scoring.py` returns 0.0 for
   `no_code_block`, `scorer_error`, and genuine fabrication alike — the three are
   indistinguishable in the aggregate metric.)
2. **Report `no_code_block` separately too.** A model that answered in prose without a ```` ```go ````
   fence did not fabricate; it declined the format.
3. **The mean is not a fabrication rate.** Per-sample scoring is binary, so Inspect's `accuracy`
   and `mean` are both "fraction of completely clean samples." A snippet with one bad reference
   scores the same as one with five. Report **clean-sample rate** *and*
   **fabricated-references-per-sample**; the second is in each sample's
   `metadata["fabricated"]`.
4. **Always record the git SHA and the model string.** The scorer *is* the production hook, so a
   number is only meaningful against the commit that produced it.
5. **Always fill in the failure taxonomy.** A single aggregate score hides clustered failures —
   a 94% pass rate reads as healthy right up until the remaining 6% turn out to be one bug
   repeated. Grouping costs a few minutes at run time and is painful to reconstruct later.
   Leonard has already been bitten by exactly this: Track B Phase 1
   ([`docs/track-b/phase-1-results.md`](../../docs/track-b/phase-1-results.md)) produced 2,230
   findings that were all one failure — numeric over-matching — wearing different clothes.
   (Framing borrowed from Kyle Bartlett, Bartlett Labs; credit him if this reaches a write-up.)

## Known validity limits

Carry these into any external write-up. Fuller treatment in ROADMAP-v2 §1.3.

- **n = 7.** Enough to see directionality, not enough for tight confidence intervals. §1.5's power
  table wants 40–100 per arm for realistic effects.
- **A deny from a non-code adapter reads as clean.** The scorer only counts a denial as a
  fabrication if the reason carries `FABRICATION_PREFIX`. A ground-truth or self-logging deny
  scores **1.0** — a false clean.
- **Eval-on-itself.** Dataset and scoring oracle both target Leonard's own codebase. These are
  numbers about *this* project, not a universal fabrication-reduction multiplier.
- **The guard can't see receiver methods.** It walks `SelectorExpr` with a known package alias,
  so `s.Foo()` is invisible to it. Samples deliberately elicit package-qualified calls
  (`store.Foo`); a sample that doesn't is measuring nothing.
- **Every recorded `.eval` log predates the arm-3 fix** (`caac19e`), when `Task()` silently
  discarded the `system_message=` kwarg and arm 3 was a duplicate of arm 2.
