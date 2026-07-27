# Phase 2 — `leonard doctor --drift`

**Status:** design only. **Double-gated** — on Track A step 2 (a real fabrication number) and on
Phase 1 results. Do not build before both.

Phase 1 may delete most of this document. That is the intended outcome if it happens.

## Framing

`doctor` already reports **"stale paths (file row exists but missing on disk)"** — index↔disk
divergence. That is a drift check, shipped, in this command, today.

`--drift` generalizes a check `doctor` already performs. It is not a new concept, and arguing it
as one would be overselling.

Constraints inherited from `doctor`: read-only, terse output, no writes. Keep all three.

## Scope

Only the drift cases `facts.yaml` **structurally cannot express** — that is, whatever Phase 1
shows the existing machinery misses. Anything a fact can capture belongs in `facts.yaml`, not
here.

| Check | Catches | Resolver | Read-only? |
|---|---|---|---|
| Version constants agree with each other and with the README status line | Case 3 | Extract Go const; grep prose | yes |
| Installed binary traceable to a commit | Case 4 | Compare `--version` + build info against git | yes |
| Uncommitted changes to truth-tree files | new | `git status` over `.leonard/ground-truth/` | yes |

Check 1 currently lives in CI as a shell `grep` (`3f01e7b`). It belongs in the tool. Moving it
also means it runs locally before push rather than only in CI.

Check 2 is the one no other mechanism can perform, and the one that caught the sharpest failure of
2026-07-27: a `leonard-hook` binary running at 0.55.0 whose source existed in no commit.

## Design notes

**Built-in, not plugins.** Three resolvers do not justify a protocol, and built-ins sidestep the
trust boundary entirely (see Phase 3). Revisit only when a fourth resolver appears that Leonard
should not ship itself.

**Exit codes.** `doctor` is diagnostic. `--drift` should follow: report findings, exit 0 unless
the command itself failed. A `--strict` flag that exits non-zero on any drift is the CI-friendly
form, and is what would let this replace the CI `grep` rather than duplicate it.

**Check 2 mechanics.** Comparing an installed binary to a commit needs a version string plus
something git-anchored. `leonard-mcp` already supports `-ldflags "-X main.version=..."`. The
honest implementation stamps a commit SHA at build time and compares it against `git rev-parse`;
without a stamp, the check can only say "unknown provenance," which is still more than nothing and
is the correct answer for a `go install` build.

**False positives are the failure mode.** A drift check that cries wolf gets ignored, and this
tool already has one open dogfood item about exactly that. Each check must be precise enough to
run on every invocation without producing noise, or it should not ship.

## Open questions for after Phase 1

- Does `facts.yaml` plus `list-stale-claims` already cover check 1? If so, drop it.
- Is check 3 useful, or does `git status` already tell you? It may be redundant.
- Should `--drift` be default-on in `doctor` once it is quiet enough?

## Definition of done

`leonard doctor --drift` runs the checks Phase 1 proved necessary, reports nothing on a clean
repo, and catches the reintroduction of drift cases 3 and 4. Regression tests pin both.
