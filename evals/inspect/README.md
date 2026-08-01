# Inspect eval — does Leonard reduce fabrication?

The premise of this project is that grounding Claude in a real symbol
index reduces fabricated API references. This eval measures that
premise directly using the UK AI Security Institute's
[Inspect](https://inspect.aisi.org.uk/) framework. It's the receipt.

## What it measures

A small dataset of "tempted to fabricate" coding prompts targeting
Leonard's own internals (see `samples.py`). For each prompt, the model
is asked to write Go code that references real Leonard helpers — the
ones it would have to *know about* the codebase to name correctly.

The scorer (see `scoring.py`) pipes every produced snippet through
`leonard-hook pre-edit`, Leonard's actual production fabrication
detector. A score of 1.0 means the hook found zero fabricated
references; 0.0 means it blocked the snippet for inventing names.

Four tasks share that dataset + scorer (in `tasks.py`):

| Task | Tools | System prompt | What it measures |
|---|---|---|---|
| `fabrication_control` | — | — | Claude alone. Baseline rate from model priors. |
| `fabrication_prompt_only` | — | ✅ | The prompt's effect with no tools to back it. Separates "told to verify" from "able to verify." |
| `fabrication_with_leonard` | ✅ | — | `leonard-mcp` wired via `mcp_server_stdio` — tools advertised, model decides whether to use. |
| `fabrication_with_leonard_system_prompt` | ✅ | ✅ | Tools plus an explicit nudge to `verify_symbol` first. The gap between "tools available" and "tools available + told." |

`fabrication_prompt_only` was added in `caac19e`, which also fixed a bug where arm 4 never
applied its system prompt at all (`Task()` silently discarded the `system_message=` kwarg, making
it a duplicate of arm 3). **Every `.eval` log in `logs/` predates that fix.**

## Running

```bash
cd evals/inspect

# Install Inspect into an isolated env
uv sync

# Build the Leonard binaries the scorer + MCP server need
go install ../../cmd/...

# The scorer locates leonard-hook itself: $LEONARD_HOOK_BIN, else PATH, else
# $(go env GOPATH)/bin — the `go install` target, which is NOT on PATH by
# default. Set the env var only to pin a specific build:
#   export LEONARD_HOOK_BIN=/path/to/leonard-hook

# Make sure the repo is indexed (the scorer needs .leonard/leonard.db)
(cd ../.. && leonard init . && leonard index)

# Run a single task
export ANTHROPIC_API_KEY=sk-...
uv run inspect eval tasks.py@fabrication_control \
    --model anthropic/claude-sonnet-4-5

# Or run all four back-to-back
uv run inspect eval \
    tasks.py@fabrication_control \
    tasks.py@fabrication_prompt_only \
    tasks.py@fabrication_with_leonard \
    tasks.py@fabrication_with_leonard_system_prompt \
    --model anthropic/claude-sonnet-4-5

# Inspect the results
uv run inspect view
```

**Write the run down.** `logs/` is gitignored, so a run leaves no trace in the repo. Record it in
[`RESULTS.md`](./RESULTS.md), which carries the reporting rules — most importantly: exclude
`scorer_error`, report `no_code_block` separately, and never quote the mean as a fabrication rate.

A full pass over the 7 samples is ~$0.50–$2 depending on how chatty
the treatment runs get (the treated runs use more tokens because each
tool call is an extra round-trip).

## Interpreting the numbers

For each task Inspect reports an aggregate `accuracy` (fraction of
samples scoring 1.0) plus a `mean` (same thing — every Score is
either 0.0 or 1.0, the two metrics happen to coincide). The
**delta between tasks** is the receipt — e.g., control 0.30
vs. treated 0.85 means Leonard cuts the fabrication rate from 70% to
15% on this benchmark.

Each sample's metadata records *which* references the hook flagged
as fabricated, so a low score on the control run produces a concrete
list of made-up names the model would have shipped. Show those in any
external write-up to make the numbers tangible.

## Caveats

- **Eval-on-itself.** Both the dataset and the scoring oracle target
  Leonard's own codebase. The numbers measure fabrication on *this*
  project, not a generic one. Useful as a controlled experiment;
  shouldn't be extrapolated as a universal fabrication-reduction
  multiplier.
- **The scorer is binary.** A snippet with one fabricated reference
  scores the same as one with five. A future version could score on
  fraction-of-references-fabricated for a smoother signal.
- **Sample count is small.** Seven samples is enough to see whether
  the directionality is right; not enough for tight confidence
  intervals. Adding samples is cheap — `samples.py` is the only file
  to edit.

## Why use leonard-hook for scoring?

We deliberately re-use the production fabrication detector rather
than reimplementing the check in Python.

1. **Honest measurement.** The hook is the value proposition.
   Measuring something else would measure the wrong thing.
2. **Forward compat.** Any improvement to the hook's heuristics
   (better generic-type checking, future method-signature
   verification) automatically improves the eval. The score number
   reflects the current state of the actual product.
