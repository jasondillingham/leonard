# Bug-hunt round 11 — v1.0 release gate

**Status:** PENDING — operator-driven investigation
**Trigger:** v1.0 ground-truth toolkit release (issue #41)
**Scope:** all v0.6 → v1.0 additions; the existing v0.52 surface
not in scope (covered by bughunts 1-10 + security-1-4)

This is the structured pre-release pass per Leonard's existing
release discipline. Every minor bump gets one; v1.0's bug-hunt is
larger because the diff is larger.

---

## Methodology

Same six-theme approach as bughunt-6, applied to the v1.0 surface:

1. **Theme A — Hooks**
   Pre-edit / post-edit / session-start / stop handlers across
   the three adapters (code, ground-truth, self-logging). Look
   for misordered checks, swallowed errors, missing
   path-canonicalization, blocking on disk I/O during a hook.

2. **Theme B — MCP**
   Five new tools (verify_claim, list_facts, get_story,
   get_truth_history, plus the extended get_decisions schema).
   Bounds checking, payload caps, sensitivity-filter coverage,
   error-shape consistency with v0.52 tools.

3. **Theme C — Store**
   Schema v8 migration (truth_change column). Round-trip
   coverage for the new TruthChange fields, including
   Supersedes pointer chains, Trivial flag, multi-file entries.
   Check the indexes still serve get_decisions / get_stale_
   decisions efficiently.

4. **Theme D — Sync plugins**
   Plugin protocol (#31), built-in github plugin (#32), CLI
   driver (#33). Failure-mode coverage: plugin timeout, plugin
   non-zero exit, malformed stdout, atomic facts.yaml write
   under crash, rate-limit handling.

5. **Theme E — Self-logging**
   Tier policy enforcement (#25), --trivial bypass token TTL
   and single-use semantics (#26), override --once token TTL
   (#17). Token-file collision risks (SHA-256 truncation),
   replay attacks, trust marker symlink defense.

6. **Theme F — Hot reload**
   Goroutine lifecycle on Close (the channel-parameter fix from
   #27). Race conditions when reload coincides with a hook
   invocation. Parse-error recovery (prior state retained).

---

## Investigation targets

Per-theme, look for:

- **HIGH/CRITICAL** — exploitable today, or trivially exploitable
  with operator-controlled input
- **MEDIUM** — possible exploitation requires unusual input or
  multi-step setup
- **LOW** — quality concern, not a security issue

Findings get one entry per row in `bughunt-11-triage.md` (not yet
created — operator populates as findings accumulate):

```markdown
| ID | Severity | Theme | File:line | Summary | Fix shape |
|---|---|---|---|---|---|
| F1 | HIGH | A — Hooks | ... | ... | ... |
```

---

## Acceptance

- [ ] All themes investigated; investigator notes captured per
      theme in `bughunt-11-<theme>.md`
- [ ] Triage rollup at `bughunt-11-triage.md`
- [ ] Every HIGH/CRITICAL closed (separate commit per fix, with
      test coverage and a v0.X bump)
- [ ] CHANGELOG.md entry references the bughunt by number and
      links findings
- [ ] No HIGH/CRITICAL findings open at v1.0 tag

---

## Prior bug-hunt rounds (for reference)

| Round | Trigger | Findings (closed) |
|---|---|---|
| bughunt-1 | initial dogfooding | 12 |
| bughunt-2 | v0.45 caps | 9 |
| bughunt-3 | v0.47 otel | 7 |
| bughunt-4 | v0.48 store-perf | 6 |
| bughunt-5 | v0.49 launch readiness | 4 |
| bughunt-6 | v0.50 themes | 11 |
| bughunt-7 | v0.50 carryover | 5 |
| bughunt-8 | v0.51 verifier | 3 |
| bughunt-9 | v0.52 iteration 3 | 3 |
| bughunt-10 | v0.52 iteration 4 (TERMINATES) | 0 |
| **bughunt-11** | **v1.0 release gate** | **pending** |

---

## Time estimate

Per-theme investigation: ~2-4 hours each. Total: ~15-25 hours of
focused review. The actual triage + closure work can be parallel
to bughunt-12 if findings concentrate in one theme.
