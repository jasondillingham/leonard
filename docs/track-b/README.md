# Track B — build docs

Implementation specs for the drift-between-artifacts track. Rationale, evidence, and sequencing
live in [`../ROADMAP-v2-measurement-and-drift.md`](../ROADMAP-v2-measurement-and-drift.md) §2;
these documents are the build instructions.

| Phase | Doc | Status | Depth |
|---|---|---|---|
| 0 | [Claim expiry](./phase-0-claim-expiry.md) | **Shipped** (`aeb9a61`, schema v9) | Implementation |
| 1 | [Dogfood ground-truth on Leonard](./phase-1-dogfood-ground-truth.md) | **Run** → [results](./phase-1-results.md) | Procedure |
| 2 | [`doctor --drift`](./phase-2-doctor-drift.md) | Gated | Design |
| 3 | [Binding model](./phase-3-binding-model.md) | Gated | Sketch |

## Phase 1 outcome, in one line

The existing machinery **does not** solve this: 2,230 findings across 126 files, **zero** of the
real drift caught, and a factually-correct README producing 69 false positives. The unsolved
problem is **claim→fact association**, not truth resolution — which redirects Phase 2 toward
narrow structural checks and away from the heuristic claim detector. Full result and evidence in
[`phase-1-results.md`](./phase-1-results.md).

## The gate

Phases 2 and 3 are gated on Track A step 2 — a real fabrication number. That result determines
whether the symbol-index half or the claim half is the durable bet, and that changes what a drift
primitive should watch. **Phase 1 is a second gate:** it measures how much of the drift problem
the shipped machinery already solves once enabled, which may shrink Phase 2 substantially.

Phases 0 and 1 are ungated and can start immediately. Phase 0 is a defect in shipped code, not a
design question. Phase 1 costs about a day and de-risks everything after it.

**Do not build Phase 2 before Phase 1 has run.** The whole point of Phase 1 is to find out which
drift cases need new code at all.

## Why this track exists

Five drift failures found in a single afternoon (2026-07-27), all in the seam between two
artifacts rather than inside one file. Three are mechanically checkable; two are not, and are
explicitly out of scope. See ROADMAP §2.1.

The finding that reframed the track: **Leonard does not run its own ground-truth adapter**
(ROADMAP §2.0). The claim-checking half of the tool has never been pointed at this project. That
is why Phase 1 exists and why it comes before any new primitive.
