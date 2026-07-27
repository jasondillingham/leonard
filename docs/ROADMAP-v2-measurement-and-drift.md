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

- **Arm 3 never applied its system prompt. It was an exact duplicate of arm 2.** *(Found and
  fixed after this document was first written; the original text here claimed arm 3 merely
  confounded two variables, which understated the problem.)*

  `Task()` has no `system_message` parameter. It accepts `**kwargs` typed as
  `TaskDeprecatedArgs` and recognizes exactly four names — `plan`, `tool_environment`,
  `epochs_reducer`, `max_messages` — silently discarding anything else **with no warning**
  (`inspect_ai/_eval/task/task.py:152-174`). So `system_message=` passed to `Task()` did nothing,
  and `SYSTEM_PROMPT` — referenced nowhere else in the file — never reached a model. Arm 3 was
  arm 2 plus a message limit.

  In Inspect, `system_message` is a **solver**. The fix is
  `solver=[system_message(TEXT), use_tools(...), generate()]`. Verified by solver-chain length:
  control 1 step, arm 3 now 3 steps; previously 2, identical to arm 2.

  A fourth arm, `fabrication_prompt_only` (grounding prompt, no tools), was added at the same
  time. Without it, any gain in arm 3 could be explained entirely by `SYSTEM_PROMPT`'s "do not
  invent function or method names" instruction with the tools contributing nothing. That
  distinction — *"Leonard helps"* versus *"telling a model not to make things up helps"* — is the
  most important thing this eval has to separate, and until now it could not.

  **Every recorded run predates this fix**, which is a second reason the existing logs establish
  nothing.
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

### 1.5 Phase 3 — the ablation harness

**Gated on §1.4 step 2.** Build this only if the first real run shows a moderate effect. If the
effect is enormous, a table with honest caveats is enough and this is over-engineering. Written
now so it's ready when the number lands, not as a commitment to build it.

The proper name for what this does is an **ablation study**: vary one component at a time and
measure what each contributes. Inspect 0.3.223 (already pinned) supplies the runner — epochs,
multiple arms per run, log persistence, comparison viewer. The engineering is small. The
statistics are the entire game.

#### The matrix

Replace the hardcoded task functions with a parameterized cross product:

| Dimension | Values |
|---|---|
| Tools | none, Leonard MCP |
| Prompt | bare, grounding instruction, grounding + tool nudge |
| Model's index state | full, stale, **empty** |

Index state is meaningless when tools are off, so the space is 3 + (3 × 3) = **12 cells**, not 18.

The prompt dimension **must** be applied as a solver — `solver=[system_message(TEXT), …]`. Passing
`system_message=` to `Task()` is silently discarded (see §1.3), which is exactly how the original
arm 3 sat as a duplicate of arm 2 from 2026-05-20 until it was caught today.

#### The ablation that justifies the harness

**Empty index.** The model can still call `verify_symbol`; it just gets "not found" for
everything.

If the empty-index arm scores as well as the full-index arm, Leonard's benefit is not grounding —
it is that handing a model verification tools makes it more cautious. That is a placebo effect and
nothing currently rules it out. If full clearly beats empty, the index's contribution is isolated
and the claim becomes defensible.

No other cell in the matrix separates *"grounding works"* from *"tools make models careful."* This
experiment alone is worth the harness.

#### A design detail that is easy to get wrong

**The scorer's index is the oracle; the model's index is the treatment. They must be decoupled.**

Today both derive from the same `PROJECT_ROOT` — `mcp_server_stdio(cwd=PROJECT_ROOT)` for the
model's tools, `fabrication_scorer(PROJECT_ROOT)` for the oracle. They are already separate
parameters that happen to be assigned the same value, so splitting them is a small change:

- `model_index_root` — varies per cell (full / stale / empty fixture)
- `oracle_root` — **always** the real, fully-indexed repo

Wire these together and a stale-index arm would be scored against a stale oracle, which would make
fabrications look correct. That would invert the result silently, which is the worst failure mode
an eval can have.

Fixtures: *empty* is an `init`-ed but never-`index`-ed store; *stale* is the tree indexed at an
older commit while samples are written against current.

#### Pre-registration

The matrix invites 12-cell fishing. Multiple comparisons inflate false positives, so declare the
hypotheses before running:

- **Primary:** `tools=leonard, index=full, prompt=bare` vs `tools=none, prompt=bare`.
  Isolates tools + index with prompt held constant. This is the headline number.
- **Secondary:** `index=empty` vs `index=full` (both tools on, prompt bare). The placebo test.
- **Tertiary:** `prompt=grounding` vs `prompt=bare` (both tools off). Isolates the instruction —
  the confound described in §1.3.

Everything else is exploratory and should be reported as such.

#### Power targets

Two-proportion test, α = 0.05 two-sided, 80% power, independent samples:

| Control → treated | Samples per arm |
|---|---|
| 40% → 60% | 97 |
| 50% → 70% | 93 |
| 50% → 80% | 39 |
| 40% → 80% | 23 |
| 30% → 90% | 10 |
| 20% → 95% | 6 |

At n=7 only a very large effect is detectable. That is the gate in §1.4: if the first run lands
near the bottom rows, we are done and this harness is unnecessary. If it lands near the top, the
dataset needs to grow to roughly 40–100 samples before any comparison means anything.

#### Epochs are not a substitute for samples

Inspect's `--epochs` re-runs each sample N times. This reduces variance from **model
stochasticity** — it does not buy **generalization**. Twenty epochs over 7 prompts measures those
7 prompts precisely; it says little about coding tasks in general, because observations within a
sample are correlated and effective n is far below raw observation count.

- More epochs → "this result is reproducible"
- More samples → "this result generalizes"

Both are worth having; treating them as interchangeable is how eval results get quietly
overstated.

#### Held-out split

Once the harness can hill-climb (change a hook heuristic, re-run, keep what helps), overfitting
becomes the dominant risk. With 7 prompts, tuning against the full set fits noise, and the
published number becomes the number that was overfit to.

Grow the dataset first, then split: tune on dev, report on held-out data never used for tuning.
The current 7 samples are a reasonable dev set; the held-out set should be written fresh.

#### Cost

12 cells × n samples × epochs. At n=40 with 3 epochs that is ~1,440 model calls per full sweep.
Run the three pre-registered comparisons at full power and the exploratory cells at reduced n.

#### Regression use

Tie every run to a git SHA. The sweep then doubles as a guard against Leonard regressing its own
value proposition — a stronger version of the `docs` CI check added in `3f01e7b`, and the only
mechanism proposed here that would catch a heuristic change that quietly makes fabrication worse.

---

## 2. Track B — drift between artifacts

Phase 0 is a shipped defect and is specced to implementation depth. Phase 1 is a cheap experiment
that sizes everything after it. Phases 2+ stay sketches on purpose — Track A's result determines
whether the code side or the claim side is the durable bet, and that changes what this should
watch. **Do not build Phase 2+ before the measurement gate resolves.**

### 2.0 The finding that reframes this track

**Leonard does not run its own ground-truth adapter.**

`EnabledAdapters` (`internal/config/config.go:103`) auto-enables `ground-truth` only when
`.leonard/ground-truth/` exists. This repo's `.leonard/` contains `config.toml` and the database
and nothing else. There is no truth tree, no `facts.yaml`, and no `[sync.*]` config. Only the
`code` adapter loads.

So the four drift failures below were not missed because the primitive is too weak. They were
missed because **the half of Leonard that checks claims was switched off on Leonard.** The
symbol-index half is dogfooded thoroughly; the claim half — the half §3 argues is the more durable
bet — has never been pointed at this project.

That inverts the first question. It is not "what new primitive is needed?" It is "how much of this
does the shipped machinery already catch once it is turned on?" Nobody knows, and it is cheap to
find out. That is Phase 1.

### 2.1 The evidence, all from one afternoon

Leonard tracks claims inside files you designate and symbols inside your code. Every truth failure
found in this session lived in the **seam between two artifacts**, and Leonard caught none of them:

| # | Drift | Between |
|---|---|---|
| 1 | README claimed "six bug-hunt rounds and two security reviews"; `audits/` held twelve and five | prose ↔ repo contents |
| 2 | `DESIGN.md` header read "design phase, no code yet" across ~44k LOC | prose ↔ code |
| 3 | README status line said v0.52.0 while `cmd/leonard/root.go` said v0.54.0 | prose ↔ constant |
| 4 | `~/go/bin/leonard-hook` running at 0.55.0, its source in **no commit** | installed artifact ↔ source |

A fifth, found later the same day and arguably the sharpest: `evals/inspect/tasks.py`'s third arm
carried a docstring describing a system-prompt experiment that **was not running**, because
`Task()` silently discarded the `system_message=` kwarg. A claim in a docstring diverged from what
the code did, for two months, with no mechanism anywhere that could have noticed.

Number 4 is the thesis in miniature. A ground-truth toolkit that cannot say *"the binary executing
on your machine is not in your repository"* is missing the highest-consequence drift class there
is. The fix shipped for #3 was a `grep` in CI — an unglamorous patch to apply *to a ground-truth
tool*.

**Case 2 is explicitly out of scope.** "Design phase, no code yet" is a semantic claim about a
whole document. No resolver catches it, and pretending otherwise would oversell what this
primitive does. Three of the five cases are mechanically checkable; #2 needs a claim detector, and
#5 needs something that can compare a docstring's assertion to a library's actual behavior, which
is beyond anything proposed here.

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

### 2.4 Phase 0 — `verified` never decays *(a shipped defect; build this regardless)*

Independent of every resolver question, small, and wrong today.

The `claims` table is `(id, session_id, claim, evidence, verified, recorded_at, file_path,
superseded_by_claim_id, tool, index_ok, vet_ok, vet_error_summary)`. The lifecycle operations are
`RecordClaim`, `SupersedeClaimsForFile`, `SupersedeOutstandingFailures`, `ResolveClaim`,
`PurgeSupersededClaims`, `GetClaim`, `GetUnverifiedClaims`.

**There is no re-check anywhere.** No `last_checked` column, no TTL, no expiry. `verified` is a
one-time boolean, and supersession fires on *a later edit to the same file* — never on the
underlying truth changing. A claim verified on 2026-05-20 still reads as verified today even if
what it asserted stopped being true in June.

That is the drift problem living inside the ledger built to prevent it. The ledger models *"was
this checked?"* when the question that matters is *"is this still true?"*

Minimum fix:

- Add `last_checked INTEGER` and `check_ttl_seconds INTEGER` (nullable — NULL means "never
  expires", preserving current behavior for existing rows).
- A claim past its TTL reports as `stale`, distinct from both `verified` and `unverified`.
  Three states, not two.
- `GetUnverifiedClaims` gains a sibling that returns stale-but-previously-verified claims, so the
  Stop hook can surface "5 claims haven't been re-checked in 30 days" without conflating them with
  claims that never passed.

This needs a schema migration (v9) and touches `queryUnverifiedClaims`. It does not need
resolvers, bindings, or any new concept — just an honest answer to how old a verification is.

### 2.5 Phase 1 — dogfood the ground-truth adapter on Leonard *(the experiment that sizes the rest)*

Per §2.0, it has never been enabled here. Before designing a new primitive, find out what the
existing one already catches.

1. Create `.leonard/ground-truth/` with a `facts.yaml` holding the facts this project keeps
   getting wrong: current version, bug-hunt round count, security-review count, tree-sitter
   grammar count, schema version.
2. Run `leonard list-stale-claims --scope '**/*.md'` across the repo.
3. Record which of drift cases #1 and #3 it catches, and which it misses.

**This is the decision point for the whole track.** If declaring five facts catches the README
drift with zero new code, Track B is mostly "turn it on and write the facts down," and Phases 2+
shrink to the cases facts.yaml genuinely cannot express (binary provenance, git state). If it
catches nothing, the gap is real and precisely characterized, which is a far better position from
which to design than the current one.

Either outcome is worth a day. Do not skip to Phase 2 without this result.

### 2.6 Phase 2 — `leonard doctor --drift` *(design depth only; gated on the measurement gate)*

`doctor` already reports **"stale paths (file row exists but missing on disk)"** — index↔disk
divergence. This is not a new concept; it is the same check generalized, which is the right way to
argue for it.

Built-in resolvers for the cases `facts.yaml` cannot express:

| Check | Catches | Resolver |
|---|---|---|
| Version constants agree with each other and the README | #3 | extract Go const, grep prose |
| Installed binary traceable to a commit | #4 | compare `--version` + build info to git |
| Uncommitted changes to files ground-truth claims depend on | new | `git status` over the truth tree |

Check 1 currently lives in CI as a `grep` (`3f01e7b`). It belongs in the tool.

Whether these become plugins or stay built in is a Phase 3 question. Built-in first: three
resolvers do not justify a protocol.

### 2.7 Phase 3+ — the binding model *(sketch; do not spec further yet)*

The general form from §2.2, with the seam named:

- **Declared** in the truth tree (operator-authored, reviewable, in git) — mirroring how
  `facts.yaml` already works.
- **Cached** in the store with `last_checked` and last verdict — mirroring how the DB already
  holds derived state.

That split is the existing facts.yaml↔store philosophy, not a new architecture.

**Two constraints found while grounding this, both real:**

1. **Sync plugins give less than hoped.** The protocol (`internal/adapters/groundtruth/sync/doc.go`)
   already returns `changes: [{path, old, new, reason}]`, which *is* a drift report, so
   `leonard sync --dry-run` would surface drift for free. But it only covers facts a plugin owns,
   and this repo currently declares **zero** facts and configures **zero** sync plugins. It is an
   opportunistic win once Phase 1 exists, not a phase of its own.
2. **Automatic checking crosses a trust boundary.** `sync/doc.go` states that explicit
   `leonard sync` invocation *is* the trust boundary, and that fingerprinting plugin paths the way
   `[post_edit.verify].command` is fingerprinted remains unbuilt. Any SessionStart-triggered drift
   check that runs external resolvers needs that trust work first. Built-in resolvers (Phase 2)
   sidestep this; plugin-based ones do not.

---

## 3. Sequencing

1. **Track A steps 1–2** (§1.4) — a real number. Days, not weeks; most of the work is done.
2. **Decide the bet.** A large effect argues for investing in the code side. A small one argues
   the claim ledger is the durable half, which is my prior for a separate reason: symbol indexing
   is in a race against model scale and context growth, while *"what has this project publicly
   committed to, and does this draft contradict it?"* is not a problem bigger models solve.
3. **Track A steps 3–4** (§1.4) — defensible number, committed results.
4. **Track A phase 3** (§1.5) — the ablation harness, **only if step 2 showed a moderate effect**.
   The empty-index arm is the reason to build it; if it can't be run, don't build the rest.
5. **Track B phases 2+** (§2.6–2.7) — gated on step 2.

**Two items are not gated and can start now**, because neither depends on the measurement result:

- **Track B Phase 0** (§2.4) — `verified` never decaying is a defect in shipped code, not a
  design question.
- **Track B Phase 1** (§2.5) — dogfooding the ground-truth adapter on Leonard costs a day and
  determines how much of Phases 2+ is even needed. Running it early is strictly better than
  designing around a guess.

The branch point for everything else is step 2. Until a number exists, the plan should not pretend
to know which half of the project is the durable bet.

## 4. Operational note

The `docs` CI job added in `3f01e7b` asserts the README status line matches
`cmd/leonard/root.go`'s `Version`. **Any commit that bumps the version constants must bump the
README in the same commit.** The in-flight v0.55.0 changeset will hit this first — it currently
has constants at 0.55.0 against a README saying 0.54.0. That check is doing its job, but it is a
surprise if nobody is expecting it.
