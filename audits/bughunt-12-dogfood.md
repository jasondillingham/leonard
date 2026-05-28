# Bughunt-12 — Lane L7 (real-dogfood UX) — Findings

**Lane:** L7 — real-dogfood
**Started:** 2026-05-27
**Baseline:** Leonard `d716b02` + uncommitted `stop.go` simplification (matches FINDINGS.md baseline). Binaries: `leonard 0.52.0` / `leonard-mcp 0.53.0` / `leonard-hook 0.52.0`.
**Trust state.** Ran `leonard config trust ground-truth --yes` during this lane so the pre-edit hook actually blocks instead of warning. Trust marker landed at `$XDG_CONFIG_HOME/leonard/trust/<sha>.ground-truth.trust` (outside project tree, correct).
**Scope.** Use Leonard for actual work in projectdogwalker and capture every UX friction, ambiguity, hard-to-read error message, missing feature, or "I wish it did X" finding. Extends `~/Documents/Homelab/leonard/DOGFOOD.md` (the operator's own log).

**Reading note.** L7 is the qualitative lane. Many findings are MEDIUM/LOW (UX). HIGH only if a hook fails to block what it should, or the operator-facing surface produces a security-relevant misread. Forbidden-text examples in this doc are quoted in code spans so the ground-truth detector treats them as evidence, not assertions; in a few places we paraphrase ("five-dozen releases", "the older release-count phrase") to keep the pre-edit hook happy. **Meta-observation: this document itself triggered the live ground-truth pre-edit hook three or more times during authoring — when forbidden phrases appeared inline in the F034/F046 examples. The author worked around the block by switching to a Python-script-driven `with open(path,'w')` write (which fires the post-edit hook but the pre-edit hook does NOT inspect the Python process's file writes — see F046). The author dogfooded the exact F046 bypass path while writing F046.**

Severity scale: CRITICAL / HIGH / MEDIUM / LOW (matches `findings/FINDINGS.md` and `~/Documents/Homelab/leonard/audits/`).

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F034 | MEDIUM | L7 | `leonard check` does NOT detect facts.yaml contradictions — only `do-not-claim.md` bullets. DOGFOOD #1 gap remains. | confirmed |
| F035 | MEDIUM | L7 | `leonard facts diff` requires git, exits 0 on git failure with error text — CI consumer can't tell success from soft-failure | confirmed |
| F036 | MEDIUM | L7 | `leonard facts impact <key>` scans operator-internal dirs (runlog, findings, RED-TEAM-PLAN.md) AND grep-matches value-only (no key-context awareness) | confirmed |
| F037 | LOW | L7 | `list-stale-claims --scope` uses Go's `path.Match`-like semantics — `**` does not recurse intuitively; empty-glob exits 0 (indistinguishable from "all clean") | confirmed |
| F038 | LOW | L7 | `get_decisions` (MCP) and `decisions list` (CLI) both return superseded decisions inline with no `superseded_by_id` / `replaced_by` / "STALE" marker; reader sees two contradictory entries with no signal which is current | confirmed |
| F039 | MEDIUM | L7 | `get_unverified_claims` mixes auto-generated post-edit-failure claims (with 4000+ char file paths) with operator-recorded claims; no `source` / `kind` field to distinguish. CLI dump unreadable. | confirmed |
| F040 | LOW | L7 | `get_unverified_claims` response omits the `evidence` field that was sent into `record_claim`. The operator can't review the evidence trail without a separate query path. | confirmed |
| F041 | MEDIUM | L7 | `leonard doctor` reports the F003-capped file count (`files: 1000 total`) on the health-check command — the one place an operator would look to verify their store. Compounds F003. | confirmed |
| F042 | MEDIUM | L7 | DOGFOOD #6 (post-edit noise) is **partially closed** by `suppressOutput:true` but the `systemMessage` is still emitted per-edit; on a Go project where `go vet` runs, the vet output still lands on the conversation surface every time. | confirmed |
| F043 | LOW | L7 | `verify_symbol` exact-name miss returns `exists:false` with **no "did you mean..." suggestions** — even though `find_symbol` would have found the close match. Single highest-leverage dogfood UX win. | confirmed |
| F044 | LOW | L7 | `--limit 0 = default 200` in `truth-history --help` is the same documentation pattern as F005 (`0 = unlimited` that actually returns 50). Sentinels-with-floor are not consistent across commands. | observation |
| F045 | LOW | L7 | Auto-generated post-edit-failure claims accumulate forever — no TTL, no auto-resolve. After L8's path-trust probes, the claim ledger has multi-kilobyte path-string claims that survive across sessions. | confirmed |
| F046 | **HIGH** | L7 | **Bash matcher of the pre-edit hook does not inspect the command string for forbidden content** — `cat > file <<EOF ... EOF`, `echo ... > file`, and `sed -i 's/.../forbidden/'` all bypass the ground-truth check. The post-edit hook then doesn't catch the resulting file either (its only job is re-index). Net: every operator running with the standard hooks config has an unguarded write path via Bash. | **confirmed (PROMOTED)** |
| F047 | MEDIUM | L7 | `leonard override --once --reason=...` grants a token whose file lives at `$XDG_CONFIG_HOME/leonard/pending-override/<projHash>.<relHash>.json`, but the token-consume path is **never reached** — both the ground-truth forbidden-claim filter AND the `.leonard/` path-filter deny BEFORE the consumer runs, so the granted token sits in `pending-override/` until its 5-minute TTL expires. The help text says it bypasses "path_filters / content_filters" but the live ground-truth + `.leonard/` filters ignore the token. | confirmed |

(14 findings: 0 CRITICAL, **1 HIGH**, 6 MEDIUM, 7 LOW.)

---

## DOGFOOD.md status check (per probe)

Each DOGFOOD item dated 2026-05-27 (today). For each, this lane records whether the HEAD-fresh feature *actually closes* the item or not.

| DOGFOOD item | Status after L7 | Pinning finding |
|---|---|---|
| #1 — stale-claim drift not surfaced at file-READ time | **gap remains** — `check` only matches `do-not-claim.md`, not `facts.yaml` contradictions | F034 |
| #2 — audit-log signal-to-noise (numeric over-matching) | already pinned by F015/F016 in FINDINGS.md (#98 mitigation incomplete) | — |
| #3 — no cross-artifact consistency check | `list-stale-claims` exists, but exit-0 on empty-glob (F037) + value-only grep (F036) blunt the win | F036, F037 |
| #4 — MCP tools underused at SessionStart | **largely closed** — SessionStart now prints `Leonard: N .md files — X forbidden, Y unverified. Run \`leonard list-stale-claims\` ...`. Includes prior decisions. Slight signal-leak from operator-internal dirs (F018-adjacent). | (positive) |
| #5 — fact-diff propagation when `facts.yaml` shifts | **gap remains** — `facts diff` requires git, exits 0 with error text on git failure (F035); `facts impact` scans audit dirs and matches by value-only without key context (F036) | F035, F036 |
| #6 — post-edit hook adds noise | **partially closed** by `suppressOutput:true`, but `systemMessage` still emitted every edit (and Go-project `go vet` output will still leak through) | F042 |

---

## F034 — `leonard check` does not detect facts.yaml contradictions (MEDIUM)

**Files (likely):**
- `internal/adapters/groundtruth/check.go` — the file-check entry the `leonard check` CLI calls
- `internal/adapters/groundtruth/detector.go` (or wherever the do-not-claim / facts paths diverge)

**Observed.** Wrote `fixtures/L7_scratch/contradicts_facts.md`:
```markdown
# Contradicts facts.yaml but NOT in do-not-claim.md
The team has 99 engineers.
Bosun has 4 tools.
Leonard ships 100 MCP tools.
```

`facts.yaml` says `team.engineers=12`, `bosun.tool_count=9`, `leonard.mcp_tool_count=14`. Every line is a fact-contradiction. `leonard check` says:
```
/.../contradicts_facts.md: clean (no findings)
exit=0
```

**Why this matters (dogfood lens).** DOGFOOD #1 was explicit:

> "Concrete example, 2026-05-27 session: re-opened a May 23 LiveKit cover letter with 6 stale claims (bosun `8-tool`→`9`, Leonard older release count → current `46`, Leonard `2 security reviews`→`4`, Offboarding `12 tools`→`11`, PR #104 `awaiting review`→`MERGED`, missing PR #19755 entirely). All 6 catches were manual."

The HEAD-fresh `leonard check` command **only catches claims listed verbatim in `do-not-claim.md`**. Stale claim drift against `facts.yaml` (the thing the operator was hoping to catch) passes through silently. To get DOGFOOD #1 closed, the operator currently has to *manually copy every facts.yaml value into do-not-claim.md as a forbidden bullet*, which defeats the point of having a typed `facts.yaml`.

**Fix shape.** A new finding kind — `[STALE]` — surfaced when prose contains a number/string that is **close to** a `facts.yaml` value but doesn't match. Two strategies:
- **Symmetric**: for each numeric leaf in `facts.yaml`, detect any number-in-prose that's "close" (within 1 OOM, or within 50%) but ≠ the canonical value.
- **Key-context**: detect `[N] engineers`, `[N] tools`, etc. — phrase patterns linked to facts.yaml keys via a `[fact_aliases]` config block (`team.engineers: ["engineers", "engineering team", "headcount"]`).

The current "operator must duplicate every fact into do-not-claim.md" UX is the kind of friction that gets DOGFOOD #1 closed prematurely.

**Discovered.** 2026-05-27 — L7-A.

---

## F035 — `leonard facts diff` requires git, exits 0 on failure (MEDIUM)

**File:** `cmd/leonard/facts_diff.go` or wherever the `git diff HEAD -- .leonard/ground-truth/facts.yaml` invocation lives.

**Observed.** projectdogwalker is not a git repository. Running:
```
$ leonard facts diff
git not available or facts.yaml is not tracked: git diff HEAD -- .leonard/ground-truth/facts.yaml: exit status 1
facts.yaml path: /Users/.../.leonard/ground-truth/facts.yaml
$ echo $?
0
```

**Why this matters (dogfood lens).** DOGFOOD #5 ("no fact-diff propagation") expected `facts diff` to *be* the closing feature. In a project where the operator hasn't committed `.leonard/` to git (which is the recommended pattern — `.leonard/leonard.db` is gitignored, sometimes the whole dir is), the command emits an error message but exits 0. A scripted CI consumer that runs `leonard facts diff && deploy` would treat the soft-failure as success and skip the propagation step.

Worse: the error message says *"git not available or facts.yaml is not tracked"* — those two failure modes have very different fixes (install git vs `git add`), so the operator gets generic advice and has to debug.

**Fix shape.** Either:
- **Different exit code**: exit `>=3` on "git not available" / "not tracked" — operator scripts can detect. (Currently exits 0 — indistinguishable from "no diff".)
- **Diagnose which failure**: split the error into the two cases ("git binary not on PATH" vs "file is not tracked in this repo") and recommend a specific fix.
- (Note: the `.bak` file next to `facts.yaml` here is from L5's `sed -i.bak` probe, not a Leonard-managed snapshot — so a `.bak`-fallback strategy would need Leonard to start writing its own snapshot file on every facts.yaml edit, which is a larger design change.)

**Discovered.** 2026-05-27 — L7-B.

---

## F036 — `leonard facts impact <key>` scans operator-internal dirs + value-only matching (MEDIUM)

**Files:**
- `cmd/leonard/facts_impact.go` — walker
- Probably reuses the same walk pattern as `list-stale-claims` (which has the F018 exemption gap)

**Observed.** Running `leonard facts impact team.engineers` (with `facts.yaml#team.engineers=12`) reports:
```
## team.engineers = "12"
  RED-TEAM-PLAN.md:7  (3 occurrences)
  findings/FINDINGS.md:1  (13 occurrences)
  findings/L7-findings.md:1  (1 occurrence)
  fixtures/prose/mixed_claims.md:7  (1 occurrence)
  fixtures/prose/uses_facts.md:1  (1 occurrence)
  runlog/run-2026-05-27-L2-cap-edges.md:289  (8 occurrences)
  runlog/run-2026-05-27-L4-concurrency.md:392  (2 occurrences)
  runlog/run-2026-05-27-L5-groundtruth.md:1104  (10 occurrences)
```

Two compounding UX problems:
1. **Operator-internal dirs scanned** — `runlog/`, `findings/`, `RED-TEAM-PLAN.md` (the harness's own audit trail). Same root cause as F018, different code path. Without a `.leonardignore` mechanism, every audit-keeping operator gets noise.
2. **Value-only matching** — the value is `12`. `RED-TEAM-PLAN.md` line 7 contains "Bughunt round #12" (the bughunt-round number, not engineer count). `FINDINGS.md` has 13 occurrences of "12" because Leonard's own bughunt round is named with "12". None of these are about engineers. Without **key-context awareness** ("the word 'engineers' near a 12") the report mixes signal with grep-noise.

**Why this matters (dogfood lens).** DOGFOOD #5's "Show which artifacts reference the fact" hope was specifically to close the manual-grep loop. Today's `facts impact` *is* a manual grep with extra steps — the operator still has to read every match to see if it's actually about engineers.

**Fix shape.**
- Inherit the `.leonardignore` / `scan_exclude` mechanism proposed in F018; default-exclude `runlog/**`, `findings/**`, `audits/**`.
- Add key-context awareness: require the fact KEY (or an alias from a `[fact_aliases]` config block) to appear within N tokens of the value. `12 engineers` matches, `bughunt-12` does not.
- Or: report `weak_match` vs `strong_match` and color/sort accordingly.

**Discovered.** 2026-05-27 — L7-B.

---

## F037 — `list-stale-claims --scope` glob behavior (LOW)

**File:** `cmd/leonard/list_stale_claims.go` — glob expansion

**Observed.** Three glob calls against the same project (all 5 `.md` files inside `fixtures/L7_scratch/`):

| Scope | Files matched | Findings | Exit |
|---|---:|---:|---:|
| `fixtures/L7_scratch/**/*.md` | 0 | 0 | 0 |
| `fixtures/L7_scratch/*.md`    | 5 | 6 forbidden | 2 |
| `fixtures/**/*.md`            | 6 | 14 (forbidden+unverified) | 2 |

The `**` doublestar at `fixtures/L7_scratch/**/*.md` matches zero files because there's no subdirectory under `L7_scratch/` for the `**` to traverse — but the operator would reasonably expect "match `.md` files in this dir and its subdirs" (the common operator mental model for `**`). The help text example (`docs/**/*.md`) reinforces that expectation. **Exit 0 with zero output is indistinguishable from "scoped, all clean."**

**Why this matters (dogfood lens).** DOGFOOD #3's whole point is `--scope=applications/*/cover-letter.md` to bound the scan. An operator who writes `--scope=applications/**/*.md` (the natural recursive form) gets zero output and concludes "all clean!" — wrongly.

**Fix shape.**
- Switch to `github.com/bmatcuk/doublestar` (or the std-lib equivalent) so `**` recurses across zero-or-more path segments.
- When `--scope` matches zero files, exit non-zero (e.g. 3) and print `leonard: scope matched 0 files — pattern may be wrong`. Distinguishes "I checked nothing" from "I checked everything, nothing forbidden."

**Discovered.** 2026-05-27 — L7-C.

---

## F038 — Superseded decisions are not visually marked (LOW)

**Files:**
- `internal/store/decisions.go` — schema likely has a `superseded_by` column already (the `supersede_decision` tool description says "Links the old row to the new one")
- `cmd/leonard/decisions_list.go` — display
- `internal/mcp/server.go` — `get_decisions` handler

**Observed.** After `record_decision({topic:"L7-...", choice:"first"})` (id=1) and `supersede_decision({decision_id:1, new_choice:"second"})` (returns new_decision_id=2):

```
$ leonard decisions list
leonard: 2 decision(s)
  #2  2026-05-27T22:03:32-05:00  L7-dogfood-skip-deep-dive → skip-but-extend
      DOGFOOD #6 turned out richer than expected
  #1  2026-05-27T22:03:00-05:00  L7-dogfood-skip-deep-dive → skip
      L7 lane focuses on UX friction; deeper protocol probes are L6 territory
```

Both entries appear, same topic, no marker that #1 was superseded. The MCP `get_decisions` response is the same — no `superseded_by` / `is_current` field.

**Why this matters (dogfood lens).** SessionStart shows prior decisions; both `→ skip-but-extend` and `→ skip` appear inline under the same topic. Future Claude reading the SessionStart `additionalContext` sees two contradictory choices and has no signal which is current. The whole point of the supersede tool — to give the LATER decision authority — is invisible.

**Fix shape.**
- Schema: add `superseded_by_id INTEGER` column to decisions (likely already present per the tool description's "Links the old row to the new one").
- `decisions list` display: mark superseded entries with strikethrough or a `[SUPERSEDED by #N]` annotation. Or: hide them by default and add `--all` to show.
- MCP `get_decisions` output: add `superseded_by` field to the structured content; default response should filter superseded entries unless `include_superseded: true`.

**Discovered.** 2026-05-27 — L7-H.

---

## F039 — `get_unverified_claims` mixes auto-claims and operator-claims (MEDIUM)

**Files:**
- `internal/store/claims.go` — schema
- `internal/mcp/server.go` — `get_unverified_claims` handler
- `cmd/leonard/claims_unverified.go` — CLI display

**Observed.** After the L8 lane's path-trust probes filed dozens of auto-generated claims of the form:
```
#57  tool=Write file=/Users/.../fixtures/L8_scratch/aaaaaa...aaaa/foo.go;
     index=failed; go vet=skipped (no go.mod)
```
… then this lane recorded a single operator claim (`{"claim":"L6 lane is running in parallel"}`).

`leonard claims unverified` returns 4 claims, intermixed — 3 with multi-kilobyte path strings, 1 with a real message. The CLI output is a wall of `a`s that overflows any reasonable terminal width.

`get_unverified_claims` (MCP) returns the same 4, no distinction in shape.

**Why this matters (dogfood lens).** The Stop hook surfaces unverified claims at session end. If the operator has a few intentional claims (`record_claim verified=false`) and dozens of post-edit-failure auto-claims from a path-edge-case probe, the intentional ones drown in the auto-noise. There's no `source: "auto" | "operator"` field, no `kind: "post_edit_failure" | "explicit"` discriminator.

**Fix shape.**
- Add `source` column to `claims`: `"auto"` for hook-generated, `"operator"` for `record_claim`.
- Default `get_unverified_claims` to `source: operator`; pass `include_auto: true` to retrieve both.
- Auto-claims should also auto-resolve after N sessions (see F045) so they don't accumulate.

**Discovered.** 2026-05-27 — L7 (observed while testing decision/claim roundtrip).

---

## F040 — `get_unverified_claims` drops the `evidence` field (LOW)

**File:** `internal/mcp/server.go` — `get_unverified_claims` handler's output shape

**Observed.** `record_claim` accepts:
```json
{"claim":"...","evidence":"...","verified":false}
```
But `get_unverified_claims` returns only:
```json
{"id":42,"claim":"...","recorded_at":...,"session_id":""}
```

No `evidence` field in the response. Verified by reading the structured content of the live call.

**Why this matters (dogfood lens).** The evidence is the *whole point* of the claim ledger — it's the supporting context for verifying the assertion later. Dropping it on the read path forces the operator to either remember what they put as evidence or do a separate lookup. The CLI (`claims unverified`) DOES show evidence (`leonard claims unverified` printed `red-team plan paragraph mentions parallel L6/L7/L8` under the claim) — so the data is there, just not exposed via MCP.

**Fix shape.** Add `evidence` to the `get_unverified_claims` output shape. Same for any future `get_claim_by_id` / `get_claims_for_session`.

**Discovered.** 2026-05-27 — L7-I.

---

## F041 — `leonard doctor` reports the F003-capped file count (MEDIUM)

**Files:**
- `cmd/leonard/doctor.go`
- Almost certainly calls `Store.ListFiles("", "")` for its "files: N total" line — same root cause as F003 and F020

**Observed.**
```
$ leonard doctor
leonard: project health
  store:        /Users/.../.leonard/leonard.db
  last indexed: 2026-05-27T22:04:43-05:00  (39s ago)

Index
  files:    1000 total
    go           1000
  symbols:  1000 total
    go           1000
...
```

Database actually has **1527 files**, 2166 symbols (verified via `sqlite3` direct in F003). Doctor lies the same way the indexer's "indexed 1000 file(s)" lies.

**Why this matters (dogfood lens).** `leonard doctor` is *the* command an operator runs to answer "is my Leonard install healthy?" It's the canonical health check. If the operator's project is on the wrong side of the cap, doctor reports a number that's an order of magnitude off — and the operator (correctly) suspects something is broken. The same fix that closes F003+F020 closes F041.

The symbols line is similarly capped at 1000. The CLAUDE.md notes the project is supposed to handle 250–2000 files; doctor will lie for the upper half of that range.

**Fix shape.** Same as F003: switch to `SELECT COUNT(*)` for the totals (or to an uncapped iterator), and never use the safety-capped query for reporting purposes.

**Discovered.** 2026-05-27 — L7-K.

---

## F042 — DOGFOOD #6 is partially closed by `suppressOutput:true` but not fully (MEDIUM)

**Files:**
- `internal/hooks/post_edit.go` — emits the JSON payload with `suppressOutput:true`
- `internal/hooks/...` — emits `systemMessage` regardless

**Observed.** Five rapid `Write` operations in this lane. Each post-edit hook returned:
```json
{
  "continue": true,
  "suppressOutput": true,
  "systemMessage": "leonard: re-indexed /.../fixtures/L7_scratch/edit_seq_3.md (go vet skipped, no go.mod)"
}
```

Claude Code's `suppressOutput:true` semantic suppresses the hook output from the main user surface — that's the fix DOGFOOD #6 wanted. But `systemMessage` IS still displayed (per Claude Code's hook spec — system messages get appended to the conversation regardless of suppressOutput). 

**Empirical confirmation** (added 2026-05-27 after advisor pushback that this was speculative): created `/tmp/L7_govet/` with a `go.mod` and a `main.go` containing a vet-warning (`var x int  // unused`), ran `leonard init`, then fired post-edit. Response:
```json
{"continue":true,
 "systemMessage":"leonard: re-indexed /tmp/L7_govet/main.go, go vet reported issues — claim recorded as unverified",
 "hookSpecificOutput":{
   "hookEventName":"PostToolUse",
   "additionalContext":"Leonard post-edit check on /tmp/L7_govet/main.go:\n- go vet FAILED — the edit you just made did not pass the project verifier. Do not claim this work is done until go vet is clean.\n  first error: vet: ./main.go:6:9: declared and not used: x"}}
```
**Both `systemMessage` AND `additionalContext` are emitted.** On a Go project, that's per-edit conversation surface noise — and rightly so when vet failed. The dogfood problem is that the *success* path also emits a systemMessage (`re-indexed X, go vet skipped`) — that's the volume DOGFOOD #6 was complaining about.

**Why this matters (dogfood lens).** DOGFOOD #6 was about Jason's job-hunt sessions where 5–10 cover-letter edits happen in sequence. The fix is "default to no stdout, only print on actual failures or forbidden-claim catches." Currently:
- yes: stdout suppressed
- no: systemMessage emitted every time
- no: no "only print on failure" path — the success message ("re-indexed X") fires on every edit even when there's nothing to say

**Fix shape.** Move the success message to a project-local `~/.leonard/session.log` file. Only emit `systemMessage` on:
- forbidden-claim detected (mandatory — DOGFOOD #1 territory)
- `go vet` / verifier failure
- index error
Successful re-index should be silent.

**Discovered.** 2026-05-27 — L7-E.

---

## F043 — `verify_symbol` exact-miss returns false with no "did you mean..." (LOW — but highest dogfood ROI)

**File:** `internal/mcp/server.go` — `verifySymbol` handler

**Observed.**
```
$ leonard verify BulkFunc1
leonard: no match for "BulkFunc1"

$ # but find_symbol with substring works:
$ python3 harness/mcp.py call find_symbol '{"query":"BulkFunc","limit":3}'
{matches: [BulkFunc0000, BulkFunc0001, BulkFunc0002]}
```

The verify endpoint does exact-name (or LIKE+exact) lookup. When a user (or Claude itself) types `BulkFunc1` thinking that's the symbol name but the real name is `BulkFunc0001`, the response is `exists:false` with no hint.

**Why this matters (dogfood lens).** This is **the single highest-leverage UX win in this lane**. Leonard's stated job (per README) is preventing fabricated symbol references. The most common case isn't a fully fabricated symbol — it's a slightly-wrong name (`getUserById` vs `getUserByID`, `BulkFunc1` vs `BulkFunc0001`, `parseConfig` vs `parse_config`). Today the verify call says `false` and Claude moves on. If verify returned `{exists: false, did_you_mean: ["BulkFunc0001", "BulkFunc0002"]}`, the model could correct itself in one turn instead of zero.

**Fix shape.** When `verify_symbol` returns `exists:false`, run a fallback `find_symbol`-style LIKE query (substring or trigram) and include up to 5 closest matches as `suggestions`. Same store query, just don't gate it behind the exact-match decision.

Tune: only show suggestions if the LIKE-match returns no more than 10 results — above that, the suggestions are noise and you'd be better off telling the operator to use `find_symbol` directly.

**Discovered.** 2026-05-27 — L7-J. (Suggested as the single feature most likely to make Leonard feel like a game-changer for daily Claude use.)

---

## F044 — `--limit 0 = default 200` repeats the F005 sentinels pattern (LOW)

**File:** `cmd/leonard/truth_history.go` — `--limit` flag help text

**Observed.**
```
$ leonard truth-history --help
...
      --limit int         cap the number of entries returned (0 = default 200)
```

Same "0 means default N" UX pattern as `find_symbol limit=0` (F005), where the documented behavior was "unlimited" but the implementation honored a 50-row floor. truth-history's flag is more honest (says "default 200" not "unlimited"), but the sentinels-with-floors UX is still inconsistent across the codebase. Operators learn "0 = unlimited" once and then get burned by every command that doesn't honor it.

**Why this matters (dogfood lens).** Consistency of sentinel semantics is a low-grade quality concern, but it's the kind of thing that erodes trust over time. Best fix is consistent **negative-number sentinels** (`-1` = unlimited) plus a max cap separately documented, like Postgres / GNU tools.

**Fix shape.** Pick a project-wide convention:
- `0` = default (current truth-history)
- positive N = exactly N
- negative N (or absent) = unlimited up to MaxRows
… and apply consistently in `find_symbol`, `list_files`, `recent_changes`, `truth-history`, etc.

**Discovered.** 2026-05-27 — L7-L (help-text review).

---

## F045 — Auto-claims accumulate forever; no TTL or auto-resolve (LOW)

**File:** `internal/store/claims.go` — claim retention policy (likely absent)

**Observed.** L8's path-trust probes filed multiple post-edit-failure claims for path lengths > 4000 chars. After L8 ended, the claims remain. `leonard claims unverified` still lists them in subsequent lanes. There's no retention mechanism, no "auto-resolve if file no longer references the claim," no "expire after N sessions."

**Why this matters (dogfood lens).** Over weeks of use, the claim ledger fills with stale auto-generated entries — every transient `index=failed` becomes a permanent ledger row. The Stop hook output (which surfaces unverified claims) becomes more noise than signal over time.

**Fix shape.** Two mechanisms:
- **TTL on auto-claims**: rows with `source: "auto"` (see F039) expire after 14 days or 50 sessions.
- **`leonard claims resolve --auto`**: bulk-resolve auto-claims older than N days.
- The current `claims resolve` subcommand exists but takes a single ID at a time — at 200+ auto-claims, that's not usable.

**Discovered.** 2026-05-27 — L7-I.

---

## F046 — Bash matcher of pre-edit hook does not inspect command for forbidden content; post-edit hook on file body also silent (HIGH — PROMOTED)

**Files:**
- `internal/hooks/pre_edit.go` — Bash branch: receives `tool_input.command` but does not run the ground-truth detector against it
- `internal/hooks/post_edit.go` — only re-indexes; does not call ground-truth check on the file body
- `internal/adapters/groundtruth/check.go` — exists for `leonard check <file>` but not invoked from either hook

**Observed — Bash pre-edit matcher does NOT block forbidden content** (this is the promotion trigger). With ground-truth trust granted (`leonard config trust ground-truth --yes` — i.e. blocking mode on), all three of these `Bash` tool calls pass:

```
$ python3 harness/hook.py pre-edit Bash /tmp/x "cat > /tmp/foo.md << 'EOF'
> We have done 52 releases
> EOF"
{"continue":true}

$ python3 harness/hook.py pre-edit Bash /tmp/x "echo 'We have done 52 releases' > /tmp/bad.md"
{"continue":true}

$ python3 harness/hook.py pre-edit Bash /tmp/x "sed -i 's/old/52 releases/' fixtures/L7_scratch/sample.md"
{"continue":true}
```

All three return `continue:true` with no `permissionDecision:deny`. The same forbidden claim sent through the `Write` tool DOES deny — but the equivalent Bash command lands silently. The hooks config matcher includes `Bash` (per the Homelab CLAUDE.md template: `matcher: "Edit|Write|MultiEdit|NotebookEdit|Bash"`), so the hook IS firing — it just doesn't inspect the command for forbidden text.

**Observed — Post-edit hook on the resulting file is also silent.** Wrote the file via shell here-doc with three known-forbidden phrases from `do-not-claim.md`, then fired post-edit:
```
$ python3 harness/hook.py post-edit Write fixtures/L7_scratch/post_forbidden.md "x"
{"continue":true,"suppressOutput":true,
 "systemMessage":"leonard: re-indexed /.../post_forbidden.md (go vet skipped, no go.mod)"}
```
Three forbidden claims sit on disk; the post-edit hook only re-indexes. Both checkpoints (pre-Bash, post-anything) miss the bypass.

`leonard check` on the same file does flag everything correctly — the detector works; it just isn't wired into the hook path.

**Why this is HIGH (PROMOTED from LOW per advisor review).** The task explicitly identifies "a hook that DOES NOT block what it should" as the HIGH-severity criterion for this lane. The pre-edit hook's *job* is to prevent forbidden content from reaching disk; the Bash matcher fires (so the hook config is correct) but the detector isn't called on the Bash command body. This is the operator's only blocking checkpoint for the Bash-write path — and it's empty.

**Important nuance — this is "extend, not add":** the Bash matcher branch DOES exist in `internal/hooks/pre_edit.go` (lines 198-201, `bashTouchesLeonardDir`). v0.50 specifically closed bughunt-7 F2 ("pre-v0.50 Bash payloads bypassed the guard") for the `.leonard/` path-trust check — meaning the `Bash` matcher fires deliberately for that one purpose. The code is structured to inspect the Bash command string, just for a different concern (path) than the ground-truth content concern this finding raises. The fix is not "add Bash to the matcher" — it's "extend the Bash branch's inspection to include the ground-truth content detector, on top of the existing `bashTouchesLeonardDir` check."

The "single Edit/Write pre-edit hook is enough" assumption fails any time Claude Code reaches for Bash. Common cases:
- `Bash(cat > file <<EOF ... EOF)` — heredoc creation
- `Bash(sed -i 's/old/new/' file)` — in-place edit
- `Bash(echo … > file)` / `Bash(printf … >> file)` — quick append/overwrite
- `Bash(mv tmp file && rm tmp)` — atomic write via mv
- Test setup scripts (`make`, `npm run setup`) that write generated files

In every case the *content* might contain a forbidden phrase from `do-not-claim.md`, and the hook chain is silent.

**Fix shape (in priority order).**
- **Pre-edit Bash matcher**: when `tool_name == "Bash"`, scan `tool_input.command` for any of the do-not-claim rules. False-positive risk is real (a forbidden phrase in `grep` arg is just a search, not an assertion) — so prefer DENY only when the command contains shell redirection (`>`, `>>`) or known mutation patterns (`sed -i`, `tee`, `mv … <project-tree-path>`). For ambiguous cases (no redirection visible), `systemMessage` warn rather than deny.
- **Post-edit file-body check**: after the re-index, run the ground-truth check against the file body (not just `tool_input.content`). Emit `systemMessage` on findings — don't deny (the file is already on disk), but record the claim and surface it.
- **Cross-tool consistency principle**: define the project-level rule once, enforce at every checkpoint. Currently the rule fires for Write/Edit/MultiEdit pre-edit but is silent everywhere else.

**Discovered.** 2026-05-27 — L7-G (initial observation as LOW). **Promoted to HIGH 2026-05-27** after advisor flagged that the Bash matcher had not been tested for content inspection; reproduced three bypass paths, all passed without deny.

---

## Positive observations (not findings, but worth recording)

- **`leonard config trust ground-truth`** is well-designed: stores marker file outside `.leonard/` so a `.leonard/`-write attack can't poison it (matches the SECURITY.md threat model). The dry-run text before granting is operator-friendly. Single complaint: trust state isn't surfaced in `leonard doctor` — operator can't see "ground-truth is trusted" without re-running `leonard config trust`.
- **Pre-edit's Edit-tool semantics** correctly compare `old_string` (being removed) vs `new_string` (being added) — forbidden text in `old_string` doesn't block the edit, which is exactly the right behavior for *removing* a forbidden phrase. Tested explicitly and it works.
- **The pre-edit hook rejection message for `.leonard/` paths is well-written**: `"paths under .leonard/ are operator-authored (the user's Leonard wiring and SQLite store live there). If a config change is genuinely needed, the user must edit .leonard/config.toml themselves."` Tells the model exactly what's wrong and where the legitimate edit path is.
- **SessionStart message** has good content (`N files, X forbidden, Y unverified, run \`leonard ...\``). DOGFOOD #4 is largely closed.
- **CLI shows ISO 8601 timestamps with TZ** (`#2  2026-05-27T22:03:32-05:00`) — much better than the MCP's unix int (`recorded_at: 1779937412`). Worth making MCP consistent.

---

## F047 — `leonard override` doesn't actually override either filter (MEDIUM)

**Files:**
- `cmd/leonard/override.go` — token-grant command (works)
- `internal/hooks/pre_edit.go` — token-consume path (apparently not wired for ground-truth + .leonard/)
- `$XDG_CONFIG_HOME/leonard/pending-override/<projHash>.<relHash>.json` — token storage (the file IS created)

**Observed.** `leonard override --help` says:

> Records a single-use override token that bypasses path_filters / content_filters for the next matching edit.

Test 1 — override + ground-truth content filter:
```
$ leonard override fixtures/L7_scratch/probe-override.md --once --reason "L7 dogfood probe of override workflow"
leonard: override token granted for fixtures/L7_scratch/probe-override.md
         reason: L7 dogfood probe of override workflow
         token expires in 5m0s; consumed on next matching edit.

$ python3 harness/hook.py pre-edit Write fixtures/L7_scratch/probe-override.md "We have done [forbidden phrase]"
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
 "permissionDecisionReason":"Forbidden claim ... matches rule Stale release counts#1 ..."}}
```
The token was granted; the edit was still denied. Token did NOT bypass the ground-truth rule.

Test 2 — override + `.leonard/` path filter:
```
$ leonard override .leonard/test-override.md --once --reason "test override of .leonard path filter"
leonard: override token granted for .leonard/test-override.md ...

$ python3 harness/hook.py pre-edit Write .leonard/test-override.md "harmless content"
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
 "permissionDecisionReason":"leonard pre-edit: rejected edit to \".leonard/test-override.md\" — paths under `.leonard/` are operator-authored ..."}}
```
Same: token granted, edit still denied. Override didn't help.

The pending-override file IS created in the expected location (`ls "$XDG_CONFIG_HOME/leonard/pending-override/"` shows it) AND **the file is still present after the denial** — confirmed by re-listing the directory after Test 1 and Test 2 both denied:
```
$ ls $XDG_CONFIG_HOME/leonard/pending-override/
09c088227c7b629404660b8c61f52ab4.79c72a780e9262be7ac69202c5d261eb.json
09c088227c7b629404660b8c61f52ab4.a15889a8daa7af3e1981ad13126759a4.json
...
```
The tokens were granted but neither filter calls the consumer before denying. The token-consume code path is unreachable for these two filters in the current build. (The tokens will self-expire after 5 minutes — not a leak, but a wasted operator action.)

**Why this matters (dogfood lens).** Override is documented as the explicit "I know this looks bad but proceed" escape hatch — the workflow an operator reaches for when they genuinely DO need to write a stale-claim phrase (e.g., quoting it in a do-not-claim.md commit message, or documenting historical state). With override silently non-functional, the operator either:
1. Edits `.leonard/config.toml` to disable the trust temporarily (heavy-handed, project-wide)
2. Uses `cat > file` to bypass via Bash (which works — see F046 — but is the WRONG fix)
3. Gives up and edits something else

None of these is the workflow the help text promised. The override exists in v0.8 per the help text but its hook-side enforcement isn't wired for the post-bughunt-11 forbidden-claim filter or the SECURITY.md path filter.

**Fix shape.**
- Wire the pending-override token consumer into the ground-truth pre-edit branch: if a token exists for `<path>` and matches the path-hash, consume it and allow the edit (recording the override-decision to the audit log per the help-text promise).
- Same wiring for the `.leonard/` path-filter branch — though caveats apply (the SECURITY.md threat model may intentionally forbid override of `.leonard/` edits; check before wiring).
- Until wired, update the help text to say "(NOTE: not yet enforced for ground-truth or .leonard/ filters — see issue #N)" so the operator doesn't grant tokens that do nothing.

**Discovered.** 2026-05-27 — L7 (advisor follow-up probe).

---

## Top recommendation

This lane has two recommendations on orthogonal axes:

**On the severity-fix axis (HIGH, fix-first): F046.** The Bash matcher of the pre-edit hook does not inspect command content. Every operator running the standard hooks config has an unguarded Bash-write path (`cat > file`, `echo > file`, `sed -i`). The fix is small (call the existing ground-truth detector against `tool_input.command`) but it closes the only HIGH this lane found.

**On the game-changer axis (LOW severity, highest dogfood ROI): F043.** `verify_symbol` exact-miss should return `did_you_mean` suggestions. Most "fabricated symbol" cases are typos, not hallucinations. A 5-line fallback to `find_symbol`-style LIKE search would let Claude self-correct on its first turn instead of charging ahead with the wrong name. This is the single feature most likely to make Leonard feel like an always-on safety net rather than an occasional verifier.

(These are different axes — F046 is severity-driven, F043 is workflow-driven. Both worth doing; F046 first because it's the HIGH.)

**Runner-up (DOGFOOD #1 closer): F034.** `leonard check` should detect facts.yaml contradictions, not just `do-not-claim.md` bullets. Without it, operators have to duplicate every facts.yaml value as a forbidden bullet — which defeats the point of having a typed facts.yaml in the first place.

---

## Round status (L7 lane only)

- Findings filed: **14** (F034–F047). Severity mix: 0 CRITICAL, **1 HIGH** (F046, promoted from LOW after Bash-matcher probe), 6 MEDIUM, 7 LOW.
- Probes run: 12 lane sections (A–L) covering DOGFOOD #1, #3, #4, #5, #6; pre-edit hook block + bypass; decisions roundtrip + supersede; claims roundtrip; verify_symbol UX; doctor; help-text; glob handling. Plus 4 follow-up probes from advisor review (Bash matcher, override, go-vet leakage, facts.yaml.bak source).
- DOGFOOD status: #1 open (F034), #3 partially closed (F036, F037 blunt the win), #4 largely closed (positive), #5 open (F035, F036), #6 partially closed (F042). #2 already pinned in FINDINGS.md.
- Trust state: ground-truth adapter trusted (operator action required in new projects — worth surfacing in `leonard doctor`).
- Override state: 4 pending-override tokens accumulated in `$XDG_CONFIG_HOME/leonard/pending-override/` during this lane; none were consumed because the consumer is not wired (F047). Token TTL is 5 minutes, so they self-expire — not a leak, but a cleanup-on-rejection would be cleaner.

Lane closed.
