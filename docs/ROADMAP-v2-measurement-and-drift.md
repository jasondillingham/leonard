# Roadmap v2 — measurement first, then drift-between-artifacts

**Status:** proposal, 2026-07-27. Nothing here is built.
**Companion to:** [`ROADMAP-v1-ground-truth.md`](./ROADMAP-v1-ground-truth.md), which generalized
Leonard from code ground-truth to pluggable ground-truth. This document covers what comes after.

---

## 0. A correction, stated up front

An earlier pass over this repo concluded that `evals/inspect/logs/` was empty and that the
fabrication eval had never been built out. **That was wrong**, and the real finding is sharper.

The harness is complete and carefully built. Six `.eval` logs exist from 2026-05-20. What they
actually contain:

| Log | Task | Status | Samples | Score |
|---|---|---|---|---|
| 15-55-29 | `fabrication_control` | success | 7 | 0.0 |
| 15-55-39 | `fabrication_control` | **error** | — | — |
| 17-13-34 | `fabrication_control` | success | 1 | 0.0 |
| 17-13-43 | `fabrication_with_leonard` | **error** | — | — |
| 17-13-59 | `fabrication_with_leonard` | success | 1 | 0.0 |
| 21-05-04 | `fabrication_control` | success | 1 | 0.0 |

**Every run used `model=mockllm/model`** — Inspect's mock model, which returns canned output. Two
of six errored. The rest ran 1 or 7 samples and scored 0.0 throughout.

So the eval was wired up, smoke-tested against a mock, and stopped there. It has never touched a
real model. That is a materially different situation from "unmeasured because nobody built it":
the work is essentially done and was abandoned a few inches from a number.

This correction is itself the argument for §2. A claim about this project's own state survived in
prose until someone re-derived it from the artifacts. That is precisely the failure Leonard exists
to catch, and Leonard did not catch it.

---

## 1. Track A — get a real number

### 1.1 What already exists, and it's good

`evals/inspect/tasks.py` defines three arms over one dataset and one scorer:

- `fabrication_control` — no tools, no system prompt. Baseline from model priors.
- `fabrication_with_leonard` — same prompts, Leonard's MCP server wired in via
  `mcp_server_stdio` so the model *can* call `verify_symbol` / `find_symbol` / `list_files`.
- `fabrication_with_leonard_system_prompt` — tools plus a system prompt telling the model it has
  them and should use `verify_symbol` first.

The third arm is the interesting one. "Tools available" and "tools available and the model was
told to use them" are different hypotheses, and separating them was a good instinct.

The scorer (`scoring.py`) is the strongest part. Rather than reimplementing fabrication detection
in Python, it synthesizes a `PreToolUse` payload and pipes the model's Go through
**`leonard-hook pre-edit` itself** — the same production code path that protects Claude Code. Two
consequences the file states explicitly: the eval measures the real thing, and any improvement to
the hook's heuristics improves the eval for free.

It also carries two defenses that suggest someone had already been burned:

- A **self-check** (`scoring.py:66-87`) sends a probe referencing an obviously fake symbol and
  raises loudly if the hook's deny wording no longer matches the parser — so drift surfaces as an
  error instead of a silent stream of perfect scores.
- **Forced `cwd=project_root`** (`scoring.py:133-140`), because the hook walks up from its own CWD
  looking for `.leonard/`; launched from a venv directory it would fall back to a permissive store
  and score everything 1.0, silently.

I verified the scorer's contract still holds: the hook emits
`"leonard pre-edit: blocked references to symbols not in the index: %s"` at
`internal/hooks/pre_edit.go:585`, matching `FABRICATION_PREFIX`. The parser is not stale.

### 1.2 Prerequisites — verified, small

1. **`leonard-hook` must be on `PATH`.** `scoring.py:108` uses `shutil.which("leonard-hook")` and
   raises if it's absent. No shell config on this machine puts `~/go/bin` on `PATH` (the Claude
   Code hook wiring uses absolute paths, which is why this has never mattered). Either add
   `~/go/bin` to `PATH` for the run, or teach the scorer a `LEONARD_HOOK_BIN` override — the
   latter is a three-line change and makes the eval reproducible for anyone else.
2. **`ANTHROPIC_API_KEY` is not set** in the shell environment. Inspect needs it to reach a real
   model.
3. **`--model`.** The whole gap. `inspect eval` defaults to whatever is configured; every recorded
   run got `mockllm`.

The `inspect` CLI is already installed in `evals/inspect/.venv/`.

### 1.3 Validity problems, split by how much work they imply

These matter because a number that can't survive scrutiny is worse than no number.

**Group 1 — reporting. Fixable without touching the harness; the data is already captured.**

- **The metric conflates three failure modes.** `no_code_block`, `scorer_error`, and `fabrication`
  all return `value=0.0` (`scoring.py:171-198`). A model that forgets to fence its code scores
  identically to one that invents a method. The `metadata.failure_mode` field distinguishes them,
  but the headline metric does not. **Any published number must exclude `scorer_error` outright
  and report `no_code_block` separately** — it measures instruction-following, not fabrication.
- **Per-sample scoring is binary, so the mean is not a fabrication rate.** A sample with one
  invented reference and a sample with eight both score 0.0. `mean` is therefore
  *"fraction of completely clean samples."* The per-reference lists live in
  `metadata.fabricated` and are never aggregated. Report both: clean-sample rate *and*
  fabricated-references-per-sample. The second is the more honest effect measure.

**Group 2 — experimental design. Real changes.**

- **Arm 3 confounds two variables.** `SYSTEM_PROMPT` (`tasks.py:45-52`) already says *"only use
  names that actually exist… Do not invent function or method names that sound plausible."* That
  is a fabrication-suppressing instruction on its own. Arm 3 adds that prompt **and** the
  tool-use nudge simultaneously, so any delta against arm 2 cannot be attributed to either.
  **Fix: add a fourth arm** with `SYSTEM_PROMPT` and no tool nudge (or no tools at all), isolating
  prompt effect from tool effect. This is the single most valuable change in this document — it is
  the difference between "Leonard helps" and "telling a model not to make things up helps," and
  those are very different claims.
- **n=7 is too small for a defensible effect size.** Seven samples (`store-open`,
  `hooks-pre-edit-handler`, `parse-python-shape`, `indexer-construct`, `parse-typescript-shape`,
  `claim-store-helpers`, `config-shape-after-bughunt2`) with binary scoring gives eight possible
  means per arm. Raise sample count, add epochs, or both.
- **A non-code adapter's deny reads as "clean."** `scoring.py:152-153` returns "no fabrications"
  whenever a deny reason lacks `FABRICATION_PREFIX`. Since the v1.0 dispatcher runs the
  ground-truth and self-log adapters alongside the code adapter, a deny from either scores **1.0** —
  a false clean. The eval should disable non-code adapters for the run, or match on adapter
  attribution rather than reason text.

### 1.4 Sequencing — resist fixing everything first

The tempting move is to fix all five issues and then run. The better order is:

1. **Run it once, as-is, against a real model**, with the Group 1 caveats stated in the writeup.
   Cost: an API key, a `PATH` entry, one flag.
2. **Look at the number.** If the effect is large at n=7, there is something publishable
   immediately and the Group 2 work becomes "make it defensible." If it's noise, Group 2 *is* the
   job. Right now nobody knows which, and planning as though we do would be guessing.
3. **Then** add arm 4, grow the dataset, and fix the adapter-attribution false-clean.
4. **Commit the result.** `logs/` is gitignored (correctly — they're large and machine-specific),
   so a run leaves no trace in the repo. Add a small committed `evals/inspect/RESULTS.md`:
   date, model, arm, n, clean-sample rate, references-per-sample, and the caveats. Without that,
   the next person re-derives this same correction in six months.

**Deliverable:** one table, four arms, stated caveats, reproducible command.

---

## 2. Track B — drift between artifacts

Deliberately a sketch, not a spec. Track A should resolve first, because it tells us whether the
code side or the claim side is the durable bet, and that changes what this should watch.

### 2.1 The evidence, all from one afternoon

Leonard tracks claims inside files you designate and symbols inside your code. Every truth failure
found in this session lived in the **seam between two artifacts**, and Leonard caught none of them:

| # | Drift | Between |
|---|---|---|
| 1 | README claimed "six bug-hunt rounds and two security reviews"; `audits/` held twelve and five | prose ↔ repo contents |
| 2 | `DESIGN.md` header read "design phase, no code yet" across ~44k LOC | prose ↔ code |
| 3 | README status line said v0.52.0 while `cmd/leonard/root.go` said v0.54.0 | prose ↔ constant |
| 4 | `~/go/bin/leonard-hook` running at 0.55.0, its source in **no commit** | installed artifact ↔ source |

Number 4 is the thesis in miniature. A ground-truth toolkit that cannot say *"the binary executing
on your machine is not in your repository"* is missing the highest-consequence drift class there
is. The fix shipped for #3 was a `grep` in CI — an unglamorous patch to apply *to a ground-truth
tool*, and a decent signal that the primitive is missing rather than the rule.

### 2.2 The generalization

All four are the same shape: **two representations of one truth, able to diverge silently.**
Today Leonard models one instance of this (a claim in prose vs. a value in `facts.yaml`). The
general form is a *binding* between a claim and a resolver:

```
binding := (subject, expected, resolver, last_checked, verdict)
```

Where `resolver` is anything that can produce the current truth: a file's contents, a Go constant,
a `git` query, a checksum of an installed binary, an HTTP endpoint. The four rows above become
four resolvers over one mechanism.

### 2.3 Primitives that already exist

This is why the sketch is plausible rather than a rewrite:

- **The claim ledger** already models exactly the lifecycle needed — claims with evidence, a
  verified/unverified verdict, supersession when truth moves, and TTL-based purging. It is
  currently fed by prose scanning; nothing about it is prose-specific.
- **The `Adapter` contract** (`internal/adapters/adapter.go`) is a clean eight-method interface
  (`Name`, `Init`, `Close`, `PreEdit`, `PostEdit`, `SessionStart`, `Stop`, `RegisterTools`) with a
  registry, and the dispatcher already fans hook events across adapters and aggregates verdicts.
  A drift adapter is a new registry entry, not a new architecture.
- **The sync-plugin protocol** (`internal/adapters/groundtruth/sync/`) is the strongest fit and
  the most underrated asset here. It is already a JSON-stdin/JSON-stdout contract for
  *"go ask an external system what's true and refresh facts"* — with output caps, timeouts, and a
  trust gate. `cmd/leonard-sync-github` proves the shape. **A resolver is a sync plugin.**
  `leonard-resolve-gitstate`, `leonard-resolve-binary`, `leonard-resolve-const` are all the same
  protocol pointed at different sources.

The honest version: the mechanism is largely built. What's missing is the *binding* — declaring
that a particular claim is answerable by a particular resolver, and checking it on a schedule or
at session start.

### 2.4 Smallest thing worth building

Not the general framework. One resolver, chosen because it caught a real problem today:

**`leonard doctor --drift`** answering three questions:

1. Is the installed binary's version traceable to a commit? (catches #4)
2. Do the version constants agree with each other and with the README status line? (catches #3 —
   currently a CI `grep` that belongs in the tool)
3. Are there uncommitted changes to files that ground-truth claims depend on?

If that proves useful in daily dogfooding, generalize to the binding model. If it doesn't, we've
spent a day instead of a quarter.

---

## 3. Sequencing

1. **Track A steps 1–2** — a real number. Days, not weeks; most of the work is done.
2. **Decide the bet.** A large effect argues for investing in the code side. A small one argues
   the claim ledger is the durable half, which is my prior for a separate reason: symbol indexing
   is in a race against model scale and context growth, while *"what has this project publicly
   committed to, and does this draft contradict it?"* is not a problem bigger models solve.
3. **Track A step 3–4** — defensible number, committed results.
4. **Track B minimal** — `leonard doctor --drift`.

## 4. Operational note

The `docs` CI job added in `3f01e7b` asserts the README status line matches
`cmd/leonard/root.go`'s `Version`. **Any commit that bumps the version constants must bump the
README in the same commit.** The in-flight v0.55.0 changeset will hit this first — it currently
has constants at 0.55.0 against a README saying 0.54.0. That check is doing its job, but it is a
surprise if nobody is expecting it.
