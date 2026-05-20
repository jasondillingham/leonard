# Leonard — Bug Hunt #6 + Security Review #2 Triage

> Six parallel lanes against the v0.39–v0.45.1 surface: fix-round validation, security review #2, launch-surface detail (deeper second-pass), MCP+hooks deep audit, carry-over MEDIUM sweep across all 5 prior rounds, and store/eval/build. **~110 findings. One CRITICAL.**
>
> This doc picks the fix-round-6 priorities.

## Decision

**Block the public release until the CRITICAL is closed.** Everything else fits in a follow-up sweep, but the security-2 F1 finding is end-to-end RCE through a project-authored config that Claude itself can write — the whole "config.toml is trust-boundary" assumption in SECURITY.md breaks the moment Claude can author the config. This is the kind of finding HN readers screenshot.

Order of attack:

1. **v0.46.0 — CRITICAL security fix** (security-2 F1 + F2)
2. **v0.46.1 — README accuracy + status-line + CI fix** (launch-surface F1–F6)
3. **v0.47.0 — MCP correctness** (mcp F1 + F2: language filter, response caps on symbol tools)
4. **v0.48.0 — Carry-over PROMOTED items + Cargo.lock commit** (security-2 F3, perf F1 re-measure, carry-over promotions)
5. **v0.49.0 — Docs + makefile + go.mod toolchain** (S4, S8, S10, store-eval F1/F2)

## Headline finding (CRITICAL)

### security-2 F1 — End-to-end RCE chain via Claude-authored config

**Severity:** CRITICAL — blocking public release.

**Reproducer (verified end-to-end):**

1. Pre-edit fabrication guard only inspects `.go` files (`internal/hooks/pre_edit.go:181`). All other extensions pass through.
2. Claude writes `.leonard/config.toml` (or edits an existing one) with:
   ```toml
   [post_edit.verify]
   command = "id > /tmp/sec2-pwned.txt"
   ```
3. The next Edit/Write of ANY file (e.g. `README.md`, `foo.txt`) fires the post-edit hook.
4. The hook reads the new config, executes the attacker-supplied command via `sh -c`. `/tmp/sec2-pwned.txt` is written.

**Why this matters:**

- SECURITY.md's published guarantee — "`[post_edit.verify]` config is project-authored only" — relies on `.leonard/config.toml` being trustworthy. Once Claude can write to it, the guarantee evaporates.
- Real-world scenario: a malicious user-prompt convinces Claude to "create a project config", Claude writes the TOML, the very next edit fires the payload. No malware install. No git push. The chain is purely local.
- The attack surface scales with every Claude session that has Edit/Write tool access to the project directory.

**Fix shape:**

- **Primary:** pre-edit guard must reject Edit/Write where `file_path` is `.leonard/config.toml` (or any path under `.leonard/`). The Leonard-internal config is operator-authored, not Claude-authored. Treat it as a tool-layer boundary.
- **Secondary (defense in depth):** post-edit hook should reload config from a snapshot taken at the START of the session, not re-read it from disk per hook invocation. That way a mid-session config change can't take effect without an explicit restart.
- **Tertiary:** SECURITY.md threat-model update — explicitly call out that Claude CAN edit `.leonard/config.toml` and that the guard prevents this.

### security-2 F2 — `working_dir` no path-trust validation (CARRY)

**Severity:** HIGH (carries from bughunt-5 verifier F3 — still unfixed at v0.45.1).

Reproducer: `[post_edit.verify].working_dir = "/etc"` runs the verifier in /etc. With F1 closed, this is the attacker's remaining lateral move — but it's still a real escalation.

Fix shape: add `ResolveSafe(projectRoot, working_dir)` in `internal/hooks/shell_runner.go`. Default to projectRoot when empty; reject paths outside.

## Cross-cutting themes

### Theme A — CRITICAL + HIGH security (v0.46.0)

| ID | What | Severity |
|---|---|---|
| **sec-2 F1** | End-to-end RCE via `.leonard/config.toml` written by Claude | **CRITICAL** |
| sec-2 F2 | `working_dir` no path-trust (carry from bughunt-5 verifier F3) | HIGH |

Ship as v0.46.0. Single coherent security fix. Update SECURITY.md threat model.

### Theme B — Launch-surface accuracy (v0.46.1)

| ID | What | Severity |
|---|---|---|
| launch F1 | **CI is failing on both v0.45.0 and v0.45.1** due to GitHub Actions billing/spending-limit. README badge renders RED on the homepage. | HIGH |
| launch F2 | Status line says "v0.45.0 — stable" but released version is v0.45.1 | HIGH |
| launch F3 | Tree-sitter language count inconsistent across 3 README sites (27+ summary, 26 intro, 28 table rows, 29 in source) | HIGH |
| launch F4 | Bughunt #1 HIGH count wrong in README narrative table (4 vs 8 actual in triage) | HIGH |
| launch F6 | `audits/README.md` index lists 9 filenames that don't exist; misses 3 that do | HIGH |
| launch F5 | Bughunt #3 likely overcounted | MED |
| launch F7 | `examples/pydantic-ai/README.md` cites wrong line numbers for `Open` + `IndexAll` | MED |
| launch F8 | PR #3 attributed to "external contribution from Purser" but actually authored by maintainer | MED |
| launch F10 | SECURITY.md "email maintainer" has no email (S10 unfixed) | MED |
| launch F11 | NotebookEdit asymmetry between PreToolUse + PostToolUse + post_edit doesn't read `notebook_path` (S11 unfixed) | MED |
| launch F12 | Makefile WIP language: "Until the store and parser lanes merge"; `leonardreal` build tag references nothing (S4 unfixed) | MED |
| launch F14 | README cargo build doesn't state Rust toolchain dependency (S8 unfixed) | MED |
| launch F15 | Repo private — clone steps in README will fail for outside users (B1 still pending the user's manual flip) | MED |

Single sweep v0.46.1 — all docs+config corrections, no behavior changes. After this lands, the README reads consistently and accurately.

### Theme C — MCP correctness (v0.47.0)

| ID | What | Severity |
|---|---|---|
| mcp F1 | **`language` filter only knows 5 languages**: `verify_symbol(language="rust")` (or any of 22+ non-Go-family languages) returns ZERO matches. `languageFromPath` hasn't been updated since v0.1. | HIGH |
| mcp F2 | **`verify_symbol` and `find_symbol` have NO response cap**: `FindSymbolsByName` has no SQL `LIMIT`; `find_symbol(limit=10000000)` flows verbatim to SQL. | HIGH |
| mcp F3 | `get_unverified_claims` silently truncates 1000→200→user-limit with no `truncated` flag | MED |
| mcp other | NotebookEdit PostToolUse asymmetry footgun, Exported flag never reaches wire, list_files materializes before truncation | LOW |

mcp F1 + F2 are visible breakage for users on non-Go languages — must ship.

### Theme D — Promotions from carry-over sweep (v0.48.0)

Three deferred MEDIUMs that the carry-over lane promoted to HIGH:

| ID | What | Severity |
|---|---|---|
| languages F1 (PROMOTED) | Basename-only `module_name` collides across 28 tree-sitter languages. Same-named files in different directories produce identical qnames — breaks the index's identity contract. | HIGH (was MED) |
| otel F4 (PROMOTED) | `-tags otel` is documented as instrumenting "the binaries" but `leonard-mcp` and `leonard` are byte-identical with vs without the tag. Confirmed via `go build` byte-cmp. | HIGH (was MED) |
| perf F4 + F6 (PROMOTED) | No indexer worker pool (42× slower than parallel-Go ceiling) AND monotonic WAL growth. Compound effect: real-scale projects pressure users to re-index less which makes WAL grow more. | HIGH (was MED) |

Also in this sweep:
- security-2 F3 (Cargo.lock missing): commit Cargo.lock for both Rust crates
- security-2 F4 (RSS amplification ~5× worse than bughunt-5 measured: 1.95 GiB on 4 MiB input). Lower cap further? Or just document?
- store-eval F6 (NFC normalization gap between files table and claim writes)
- store-eval F10 (migrateV7 LIKE-text matching could delete user-recorded MCP claims with coincidental substring)

### Theme E — Docs + makefile + go.mod toolchain (v0.49.0)

| ID | What | Severity |
|---|---|---|
| store-eval F1 | Makefile still bosun-era stale (S4) | MED |
| store-eval F2 | `go.mod` has no toolchain directive — systems with Go ≤ 1.21 fail | MED |
| store-eval F4 | CI workflow has zero caching (~90s wasted per run) | LOW |
| launch F9 | SECURITY.md cites v0.6 for stdin filter (wrong version) | LOW |
| launch F13/F19 | Stale local binaries at root; column header mismatch in audits/README.md | LOW |
| launch F20 | S6 unfixed: no 30-second pitch block | LOW |

### Theme F — Carry-over STILL-ALIVE items (not yet planned)

24 items from the carry-over sweep are still alive at v0.45.1 (the three above were promoted; the remaining 21 are real but not load-bearing for launch). Tracked in `bughunt-6-carry-over-medium-sweep.md`. Address in a future round.

Notable mentions:
- `verifier F2/F3` (working_dir cwd-relative) — being closed in v0.46.0
- `mcp F7` (`related_files` accepts /etc/hosts)
- `security F5` (additionalContext Markdown injection — `#`, backticks, `<!--` not escaped)
- Manifest dep-graph polish: workspace/dependencyManagement/replace directives still ignored

## What ships in fix-round-6

In priority order, per the triage:

1. **v0.46.0 — CRITICAL security fix** (Theme A: sec-2 F1 + F2). Pre-edit guard rejects writes to `.leonard/config.toml` and `.leonard/`-subpaths. `working_dir` gets ResolveSafe. SECURITY.md threat model updated.

2. **v0.46.1 — Launch-surface accuracy** (Theme B: 5 HIGH + 8 MED docs/config fixes). Status-line version, language count, bughunt counts, audits/README index, etc.

3. **v0.47.0 — MCP correctness** (Theme C: mcp F1 + F2 HIGHs).

4. **v0.48.0 — Carry-over promotions + Cargo.lock + store-eval items** (Theme D).

5. **v0.49.0 — Makefile + go.mod toolchain + CI caching + remaining docs** (Theme E).

## What's deferred

- 21 STILL-ALIVE items from the carry-over sweep that aren't load-bearing for launch.
- Inspect eval LIVE run (task #35 — still blocked on `ANTHROPIC_API_KEY`).
- The 32-item bughunt-5 language coverage punch list (still deferred per round 5 triage).

## What stays out of scope

- LOW + informational findings (catalogued for completeness).
- Refactoring the symbol qname scheme to be path-aware (would force re-index of every project; languages F1 promotion is fixed via per-language config not by changing the scheme universally — see audit for detail).
- Repo visibility flip — your manual action.
- GitHub Actions billing fix — your action.

## Confirmed-solid summary (across all 6 lanes)

The good news side:

- All 25 fixes from v0.39 → v0.45.1 PASS validation (no recurrence of the "fix was secretly a no-op" pattern from earlier rounds).
- v0.40 C++ in-class methods verified end-to-end on real nlohmann/json.
- v0.41 parent-folding works for SQL, GraphQL, Proto, Java, Kotlin, Scala, Solidity.
- v0.42 quote-aware scanner handles all 4 bughunt-5 reproducers.
- v0.43 UTF-8 + HCL anchor + word-tokenized is_exported all behave as advertised (one Kotlin `internal` edge case noted, low severity).
- v0.44 size cap + LIMIT 1000 + kind="dependency" all in effect.
- v0.45.1 all three `--version` flags work; argv parsing for leonard-mcp handles every documented case.
- Schema v7 migrations all idempotent; FK enforcement on; WAL + busy_timeout serializes concurrent operations.
- 100-goroutine write storm + 20 concurrent post-edit hooks + 3 session-starts: zero database-locked errors.
- All 11 store indexes present and used (verified via EXPLAIN QUERY PLAN). `idx_claims_vet_ok` is now LIVE-USED (was dead weight at bughunt-4) thanks to v0.39's SQL switch from LIKE to integer column.
- MCP `tools/list` returns all 10 tools with correct schemas.
- `examples/pydantic-ai/demo.py` imports resolve (modulo F7 stale line numbers).
- `go test ./...`, `go vet ./...`, `go test -race ./...`, `CGO_ENABLED=0 go build`: all green locally.
- No personal data or secrets committed beyond Co-Authored-By trailers (only `noreply@anthropic.com`).
- No source TODOs/FIXMEs.

If a fix lane finds a new HIGH while implementing, surface and decide on the spot.
