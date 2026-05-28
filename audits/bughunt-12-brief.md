# projectdogwalker — Leonard red-team / dogfood harness

**Purpose.** Drive Leonard to (and past) its limits. Record every bug, limit,
rough edge, or game-changer-class improvement. Reusable on other projects later
— nothing in `harness/` is projectdogwalker-specific.

Conceptually this is **Bughunt round #12** (last in `~/Documents/Homelab/leonard/audits/`
was bughunt-11 + security-5). Findings follow the audit's severity scale and
section shape so they can be promoted to `audits/bughunt-12-<lane>.md` when
validated.

## Baseline (locked)

- **Leonard HEAD:** `d716b02c76f649237f89b5d62d312142cdbd8fc6` (`feat: leonard facts diff + facts impact <key>`, 2026-05-27 20:26 -0500)
- **Working-tree drift:** `internal/adapters/groundtruth/stop.go` + `stop_test.go` modified — a Stop-summary simplification in flight. My binaries include it.
- **Installed binaries:** `leonard 0.52.0` / `leonard-mcp 0.53.0` / `leonard-hook 0.52.0` (see Finding F001 — this skew is source-level, not a stale build)
- **Test suite:** `go test ./...` all green at HEAD
- **MCP wire identity:** `serverInfo.version = 0.53.0`, `name = leonard-mcp`
- **MCP tools registered (default code adapter):** `find_symbol`, `verify_symbol`, `list_files`, `recent_changes`, `record_decision`, `get_decisions`, `supersede_decision`, `get_stale_decisions`, `record_claim`, `get_unverified_claims`, `get_truth_history` (11 total) — `verify_claim`, `list_facts`, `record_fact`, etc. require enabling the `groundtruth` adapter.

## How this harness works

```
projectdogwalker/
├── RED-TEAM-PLAN.md             ← you are here
├── .mcp.json                    ← wires leonard-mcp into Claude Code (next session)
├── .claude/settings.local.json  ← (hooks block to be pasted manually — see Wiring)
├── .leonard/                    ← Leonard store (sqlite + config)
├── harness/
│   ├── mcp.py    — JSON-RPC stdio client (list / call / raw / fuzz)
│   ├── hook.py   — synthesize Claude Code hook payloads → leonard-hook
│   ├── rt.sh     — logging helpers (rt_init, rt_run, rt_finding, rt_summary)
│   └── lanes/    — one shell script per red-team lane (created as we go)
├── fixtures/    — adversarial code + payloads (multi-language, edge-case)
├── runlog/      — chronological transcript per (date, lane)
└── findings/
    └── FINDINGS.md   — severity rollup table; per-finding details inline
```

Every lane script sources `harness/rt.sh`, calls `rt_init <lane>`, runs the
sub-tests via `rt_run "desc" <cmd>...`, records anomalies with
`rt_finding <id> <severity> "<title>" "<details>"`, and ends with `rt_summary`.

## Wiring status

- **MCP server:** wired via `.mcp.json` at project root. Takes effect on next Claude Code session (the `/mcp` reconnect).
- **Hooks:** the auto-mode classifier blocks me from editing `.claude/settings.local.json` because that file already holds a `permissions.allow` array — any edit reads as me widening my own permissions. Operator should paste this once and approve via `/hooks`:

  ```jsonc
  "hooks": {
    "PreToolUse":  [{ "matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{ "type": "command", "command": "/Users/jasondillingham/go/bin/leonard-hook pre-edit"  }]}],
    "PostToolUse": [{ "matcher": "Edit|Write|MultiEdit",                    "hooks": [{ "type": "command", "command": "/Users/jasondillingham/go/bin/leonard-hook post-edit" }]}],
    "SessionStart":[{ "matcher": "", "hooks": [{ "type": "command", "command": "/Users/jasondillingham/go/bin/leonard-hook session-start" }]}],
    "Stop":        [{ "matcher": "", "hooks": [{ "type": "command", "command": "/Users/jasondillingham/go/bin/leonard-hook stop" }]}]
  },
  ```

  Note: the Homelab CLAUDE.md template shows `mcpServers` *inside* `settings.local.json` — Claude Code's current schema rejects that field there. `.mcp.json` at project root is the right place. Worth updating the template.

## Coverage map — what's already deep vs. where this round adds value

**Already deep (rounds 1–11):** per-language parsers (Go/Python/Rust/TS + 24
tree-sitter grammars + preprocessors), MCP input validation + caps, hook
payload caps, path-trust guard (round 4 was a dedicated lane), verifier-trust
SHA, self-log/override bypass tokens, sync plugins, ground-truth `verify_claim`/
`get_truth_history`/`list_facts` MCP surfaces (round 11).

**Thin / unexplored — this round's targets:**

| Lane | Why thin |
|---|---|
| L1 — Version & build invariants | Trivially hit-able now (F001) but no prior lane targets cross-binary version-string drift |
| L2 — Cap-edge & overflow re-sweep | Round 4 set the caps; verify each is exact at-cap vs over-cap, including unicode-byte vs string-len |
| L3 — Index correctness across many edits | Round 4 looked at store perf, not the *correctness* drift after long Add/Edit/Move/Delete sequences |
| L4 — SQLite concurrency & crash-consistency | Parallel index + MCP + hook, kill -9 mid-write, WAL truncation, lock contention timeouts |
| L5 — v0.53 ground-truth adapter | Newest code (`internal/adapters/groundtruth/`). Fuzz threshold dropped 3→1 in #85; `stop.go` is being simplified right now; stories loose-heading just shipped |
| L6 — MCP protocol resilience | Malformed/oversized/concurrent frames, missing notifications/initialized, unknown methods, invalid IDs |
| L7 — Real-dogfood UX | Use Leonard for actual edits in this project and capture every friction the operator would hit (extends DOGFOOD.md). Closest to "make it a game-changer" |
| L8 — Path-trust regression on macOS | macOS case-insensitive FS + NFC/NFD unicode + `/private/tmp` realpath + symlink chains were a known foot-gun on Darwin |

Plan to drive L1 + L2 + L6 first (high signal-to-effort), then L5 + L8, then L3 + L4 (heaviest), with L7 layered throughout.

## Findings ledger

Live rollup: [`findings/FINDINGS.md`](./findings/FINDINGS.md). Severity scale
matches Leonard's `audits/` convention.

## Reusing this harness on another project

```bash
# in any project root that you want Leonard-instrumented
mkdir -p harness runlog findings fixtures
cp /path/to/projectdogwalker/harness/{mcp.py,hook.py,rt.sh} harness/
~/go/bin/leonard init .
# write a .mcp.json for that project (copy from here, adjust binary path if needed)
```

The harness reads `RT_ROOT` from `$PWD` when sourced; no project-name baked in.

## Logging conventions (so future sessions can pick this up)

- **Runlog**: `runlog/run-<date>-<lane>.md` — full chronological transcript with
  every command's stdout/stderr (truncated at 8K/4K). Findings marked with 🚩.
- **Findings index**: `findings/FINDINGS.md` — severity-sorted rollup table.
  Per-finding detail goes either in the runlog (for terse) or a dedicated
  `findings/F<NNN>-<slug>.md` (for HIGH/CRITICAL).
- **Promotion**: a finding validated by a reproducer + a proposed fix shape
  gets promoted to `~/Documents/Homelab/leonard/audits/bughunt-12-<lane>.md`
  matching the round-11 file shape.
