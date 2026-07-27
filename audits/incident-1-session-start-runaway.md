# Incident-1 — session-start hook runaway (2 cores, hours, SIGTERM-immune)

**Date observed:** 2026-07-27
**Surface:** `leonard-hook session-start` → ground-truth adapter claim scan
**Severity:** HIGH (operator-facing: pegged 2 CPU cores for hours as an
orphan process; survived SIGTERM; recurring on every session start in the
affected project)
**Fixed in:** v0.55.0

## What happened

A Claude Code session started in `~/Documents/Homelab/job-hunt/golearn`
at 08:52 spawned `leonard-hook session-start` (v0.54.0). The process ran
at ~206% CPU indefinitely. Claude Code's hook timeout expired long
before, but timing out only stops Claude Code from *waiting* — the child
kept running as an orphan. A plain `kill` (SIGTERM) did not stop it;
only SIGKILL did. Reproduced deterministically: the same invocation was
still running with zero output when killed by a 30-second alarm, in a
project where the scan had historically completed in well under a
minute.

## Root cause — three compounding defects

**1. The fuzzy forbidden-claim scan did its expensive work before its
cheap work.** `findFuzzyOccurrences` ran a bounded-Levenshtein DP at
*every byte offset* of the haystack (× up to `2·threshold+1` window
sizes), and only *afterwards* applied the word-boundary rejection that
disqualifies the ~5-in-6 offsets that sit inside a word. Each DP call
also allocated two fresh row slices, so large scans were GC-bound on
top of being CPU-bound (the ~206% CPU = 1 spinning goroutine + GC).
Benchmark, 100 KB haystack, one rule, threshold 1:

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| v0.54.0 | 140,294,111 | 192,336,429 | 600,392 |
| v0.55.0 | 6,885,055 | 213,656 | 5 |

**2. The scan corpus included leonard's own truth tree.** The job-hunt
project sets `truth_dir = "source-of-truth/"` — a *non-hidden*
directory, so `walkMDFiles` happily fed the truth tree back into the
detector. That is circular by construction (`do-not-claim.md` matches
its own rules verbatim) and pathological in scale: `audit-log.md` is
machine-appended, had grown to **834 KB**, and grows forever. 95 rules
× fuzzy scan × 834 KB ≈ tens of billions of DP cells — hours of CPU.
Every project using the default hidden `.leonard/ground-truth/` was
shielded by the dotdir prune, which is why dogfooding never caught it.

**3. Nothing bounded the process's lifetime, and cancellation was
trapped but never observed.** `main.go` wires `signal.NotifyContext`
(so SIGTERM is *caught* and turned into a context cancellation), but
the session-start scan loop never checked `ctx` — the net effect was a
process that specifically *ignores* SIGTERM while spinning. No
wall-clock ceiling existed anywhere in the hook.

## Fixes (v0.55.0)

1. **`fuzzy.go`** — word-boundary and F016 edge rejections hoisted in
   front of the Levenshtein DP; left-edge check hoisted out of the
   window loop entirely (skips mid-word offsets in O(1)); DP row
   buffers allocated once per `findFuzzyOccurrences` call and reused
   (`levenshteinBuf`). Emitted-match semantics unchanged — all
   pre-existing fuzzy/word-boundary/F014/F016 tests pass unmodified.
2. **`session_start.go`** — the truth dir is excluded from the walk
   (new `walkMDFiles` variadic `excludeDirs` param); files over 1 MiB
   are skipped with a stderr note; the scan observes `ctx` and a
   5-second wall-clock budget between files, reporting a partial-scan
   note when truncated.
3. **`adapter.go`** — `truthDir` is now resolved against the
   *canonicalized* project root and symlink-resolved itself; without
   this the exclusion silently failed on macOS (`/var` vs
   `/private/var`).
4. **`cmd/leonard-hook/main.go`** — a 55-second wall-clock deadline on
   the root context (under Claude Code's 60 s default hook timeout)
   plus a watchdog goroutine: once the context is cancelled (signal or
   deadline), any handler still running after a 10 s grace is
   force-exited with code 1 (never the blocking exit 2). A hook
   invocation can no longer outlive its caller's patience no matter
   what a handler does.

## Verification

- Repro before fix: `leonard-hook session-start` in the affected
  project killed by 30 s alarm, no output produced.
- After fix: same invocation completes in 5.2 s wall — the 5 s scan
  budget triggers (95 fuzzy rules × 425 files is inherently heavy),
  30 files are scanned, and the response carries the partial-scan
  note: "(partial: 30 of 425 files scanned within the 5s
  session-start budget)".
- New regression tests: `TestSessionStart_ExcludesTruthDir`,
  `TestSessionStart_CancelledContextReportsPartialScan`,
  `TestSessionStart_SkipsOversizeFiles`,
  `BenchmarkFindFuzzyOccurrences_LargeHaystack`.

## Lessons

- A hook binary must own its worst case: any code Claude Code spawns
  needs a self-imposed deadline, because the caller's timeout does not
  kill the child.
- If you trap a signal into a context, every loop that can run long
  must check that context — otherwise trapping made behavior *worse*
  than the default (uncatchable-by-default became unkillable).
- Never feed a system's own append-only output back into its scanner;
  size-cap any corpus that grows without operator action.
- "Default config was safe" is a coverage gap, not reassurance: the
  dotdir prune hid this from every default-layout project. Dogfood the
  documented non-default configurations too.
