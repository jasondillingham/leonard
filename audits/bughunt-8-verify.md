# Bug Hunt #8 — fix verification + new-surface probe (post v0.50.x)

> Lane: **verify-iteration**. Investigation-only — no code changes.
>
> Date: 2026-05-20
> Codebase: HEAD @ `ef36e9a` (v0.50.2)
> Method: per-fix reproducers (each verified to FAIL pre-fix and PASS post-fix
> via comparison against the bughunt-7 audit), then adversarial probing of
> v0.50's new code surfaces. Scratch in `/tmp/bh8-test`, `/tmp/perf-300`,
> `/tmp/sql-limit`, `/tmp/sizecap`.

---

## Headline

v0.50.0 closed **3 of 4** documented `.leonard/` bypasses; the case-insensitive,
symlink-target, and Unix-backslash classes are dead.

But the Bash command scanner (the v0.50.0 fix for bughunt-7 F2) has **multiple
unhandled shell-quote-removal bypasses**. The scanner does a literal-bytes
substring search for `.leonard` with token-boundary detection — it does not
account for bash quote removal, ANSI-C `$'...'` escapes, single-character
backslash escapes, parameter expansion, or command substitution. **Four
confirmed bypasses re-open the v0.46 RCE chain end-to-end** (demonstrated:
file written to `/tmp/bh8-pwned.txt` after triggering a post-edit hook with a
maliciously-installed `[post_edit.verify].command`).

v0.50.1 (`MaxSymbolQueryRows=1000` + `maxIndexedFileBytes=2 MiB`) is **fully
correct** for both the store-layer LIMIT and the file-size cap. Verified
empirically on 5000 same-name symbols and a 2.05 MiB Python file.

v0.50.2's `otel F4` is closed — both `leonard` and `leonard-mcp` now carry
1434 telemetry symbols with `-tags otel` (1 without). The indexer worker pool
(`perf F4`) is closed — 300-file polyglot corpus indexes in 1.14s vs the
v0.49.0 baseline of 5.99s (5.25× speedup). **But `perf F6` (WAL growth) is
only partially closed**: `wal_autocheckpoint=1000` works for long-lived
processes (leonard-mcp, leonard index) but not for the *sequential per-process
hook* case that bughunt-7 specifically called out, because each hook process
gets a fresh frame counter. 700 hooks still grow the WAL to 4.14 MiB — same
number bughunt-7 measured at v0.49.0.

---

## Findings

### F1 — Bash scanner: backslash-escape shell-quote-removal bypass (RCE chain reopened)
- **Severity:** critical
- **Reproducer:**
  ```bash
  # Write a malicious config via the bash bypass (NOT blocked)
  bash -c 'cat > .L\E\O\N\A\R\D/config.toml <<EOF
  [post_edit.verify]
  command = "echo PWNED > /tmp/bh8-pwned.txt"
  EOF'

  # Trigger a post-edit hook — Leonard reads the new config and shells it
  printf '%s' '{"tool_name":"Edit","tool_input":{"file_path":"/tmp/bh8-test/test.go"},"tool_response":{"success":true}}' \
    | /tmp/leonard-hook post-edit
  # /tmp/bh8-pwned.txt now exists with "PWNED" content.
  ```
  Verified end-to-end: the hook printed
  `{"continue":true,"systemMessage":"leonard: re-indexed /tmp/bh8-test/test.go, echo PWNED_AT_1779322742 ok"}`
  and `/tmp/bh8-pwned.txt` was created with the timestamp contents.
- **Observed:** `bashTouchesLeonardDir` does a literal-bytes substring search
  for `.leonard/` / `.leonard\` / token-bounded `.leonard`. The command
  `.L\E\O\N\A\R\D/config.toml` does **not** contain the byte sequence
  `.leonard` — every letter after the leading `.L` is preceded by a backslash.
  Bash strips the `\X` to `X` at parse time, so the executed path is
  `.LEONARD/config.toml`, which then resolves to `.leonard/config.toml` on
  the case-insensitive APFS / NTFS / Samba filesystems the v0.50.0 fix was
  specifically designed for.
- **Expected:** The whole point of v0.50.0 was that **case-insensitive
  filesystem + bash-redirect = no bypass**. The scanner needs to know that
  bash will perform quote-removal before the path lands at the kernel.
- **Suggested fix shape:** The scanner needs a minimal shell quote-removal
  pass before the segment check: strip `\\X` → `X` outside single quotes,
  drop empty `''` / `""`, and only then look for `.leonard`. That covers F1
  + F2 + F3 + F4 below. The same pass also catches the ANSI-C bypass if
  `$'...'` decodes the C escape sequences. Alternatively (and more
  defensible): hand the command string to a real shell parser library
  (`github.com/mvdan/sh/syntax`) and walk the AST for redirect / pipe /
  `tee` / `cp` / `mv` / etc. targeting `.leonard`. The current heuristic
  is too lossy.
- **Out of scope:** Bash readonly tools (`grep`, `cat`, `ls`) targeting
  `.leonard` are still over-blocked (F8 below) — distinct issue.

### F2 — Bash scanner: empty-quote shell-quote-removal bypass (RCE chain reopened)
- **Severity:** critical
- **Reproducer:**
  ```bash
  bash -c "echo X > .l''eonard/config.toml"
  # After bash strips '', the executed path is .leonard/config.toml
  # The pre-edit Bash payload with the SAME string is ALLOWED.
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"echo X > .l''eonard/config.toml"}}' \
    | /tmp/leonard-hook pre-edit
  # → {"continue":true}
  ```
- **Observed:** Empty single-quoted strings `''` between two characters of a
  word are stripped by bash's quote-removal step. The scanner sees the literal
  `.l''eonard/` bytes — no match for `.leonard`. Verified: the resulting file
  lands at `.leonard/config.toml` while the hook returns `continue:true`.
- **Expected:** Same as F1 — quote removal is a documented bash step that the
  scanner is naïve about.
- **Suggested fix shape:** Same as F1 — quote-removal pass or AST-level
  parser.

### F3 — Bash scanner: command-substitution split bypass (RCE chain reopened)
- **Severity:** critical
- **Reproducer:**
  ```bash
  bash -c 'echo X > .l$(echo eonard/config.toml)'
  # After command substitution, executed path is .leonard/config.toml
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"echo X > .l$(echo eonard/config.toml)"}}' \
    | /tmp/leonard-hook pre-edit
  # → {"continue":true}
  ```
  Verified: writes to `.leonard/config.toml` on disk.
- **Observed:** Command substitution `$(...)` returns text that bash splices
  into the surrounding word. The scanner sees `.l$(echo eonard/config.toml)`
  literally — no match.
- **Expected:** Same as F1/F2.
- **Suggested fix shape:** Same as F1 — proper shell parser. As a stopgap,
  blocking the literal substring `$(.*\.leonard` regex would close the
  trivial form but not nested substitutions.

### F4 — Bash scanner: parameter-expansion default-value bypass (RCE chain reopened)
- **Severity:** critical
- **Reproducer:**
  ```bash
  bash -c 'echo X > .leon${unset:-ard}/config.toml'
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"echo X > .leon${unset:-ard}/config.toml"}}' \
    | /tmp/leonard-hook pre-edit
  # → {"continue":true}
  ```
  Verified: writes to `.leonard/config.toml` on disk.
- **Observed:** `${var:-default}` returns `default` when `var` is unset. The
  scanner sees `.leon${unset:-ard}/` — no `.leonard` token.
- **Expected:** Same as F1.
- **Suggested fix shape:** Same as F1. The fact that there are now *four*
  distinct quote-removal/expansion classes that bypass argues hard for a real
  shell parser; the lexical-scan approach can't cover the surface.

### F5 — Bash scanner: ANSI-C `$'...'` escape bypass
- **Severity:** critical (same chain as F1-F4)
- **Reproducer:**
  ```bash
  bash -c "echo X > \$'.l\\145onard'/config.toml"
  # $'\\145' decodes to 'e' (octal 145 = 101 decimal)
  ```
  The pre-edit hook with that JSON-encoded command is allowed.
- **Observed:** ANSI-C quoting `$'...'` is a separate quote-removal class
  where `\NNN` is octal-decoded. The scanner does no octal/hex decoding.
- **Suggested fix shape:** Same as F1. The list of bypass classes is now
  long enough that a properly-implemented shell parser is the only correct
  answer.

### F6 — `wal_autocheckpoint(1000)` doesn't fire for sequential hook processes
- **Severity:** high (regression-of-fix-shape)
- **Reproducer:**
  ```bash
  # 700 sequential post-edit hooks against /tmp/perf-300
  for i in $(seq 1 700); do
    echo "def stress_${i}(): return ${i}" > py_1.py
    printf '%s' "{\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$(pwd)/py_1.py\"},\"tool_response\":{\"success\":true}}" \
      | /tmp/leonard-hook post-edit >/dev/null 2>&1
  done
  ls -la .leonard/leonard.db*
  # leonard.db:      495 KB
  # leonard.db-wal: 4148 KB  (≈ 10× the DB; same as bughunt-7 v0.49.0 baseline)
  ```
- **Observed:** SQLite's `wal_autocheckpoint` is a *per-connection frame
  counter*. Each leonard-hook invocation is its own process with its own
  connection — typically writing ≪1000 frames before close. The pragma
  never triggers a checkpoint, so the WAL grows monotonically across hook
  invocations just like at v0.49.0.
- **Expected:** Per the v0.50.2 commit message: *"Bounds WAL growth on
  long-running processes"* — strictly true; long-lived processes
  (`leonard-mcp` and `leonard index`) get the autocheckpoint. But
  bughunt-7's HIGH-3 *was specifically about sequential hooks*, with a
  reproducer running 700 hooks in a loop. The reproducer at v0.50.2 still
  shows 4.14 MiB of WAL — the fix shape addresses long-lived sessions but
  not the exact scenario flagged.
- **Suggested fix shape:** Add an explicit `PRAGMA wal_checkpoint(PASSIVE)`
  in `Store.Close()`. Each hook then truncates the WAL on the way out.
  bughunt-7's HIGH-3 listed this as the *alternative* fix shape; v0.50.2
  picked the autocheckpoint path which doesn't actually solve the hook
  case. Could also do both (cheap, robust): autocheckpoint guards the
  long-lived path, close-time checkpoint guards the per-process hook path.

### F7 — Bash scanner over-blocks innocent read-only commands
- **Severity:** medium (UX regression introduced by v0.50.0)
- **Reproducer:**
  ```bash
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"cd .leonard && pwd"}}' \
    | /tmp/leonard-hook pre-edit
  # → DENY
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"echo .leonard"}}' \
    | /tmp/leonard-hook pre-edit
  # → DENY
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"cat $HOME/.leonard/config.toml"}}' \
    | /tmp/leonard-hook pre-edit
  # → DENY (read-only access of legitimate config)
  ```
- **Observed:** The scanner blocks any Bash command containing a `.leonard/`
  or `.leonard\\` substring or `.leonard` token-boundary match, regardless
  of whether the command writes. `cd .leonard && pwd`, `cat
  $HOME/.leonard/config.toml`, `echo .leonard` all get denied. The code
  comment acknowledges this trade-off (*"blocking a harmless Bash that just
  mentions the path is better than letting through an actual write"*) — but
  combined with the F1-F5 bypass classes (which prove the lexical approach
  is insufficient anyway), the scanner is in a bad spot: too aggressive
  AND too leaky.
- **Expected:** A read of `$HOME/.leonard/config.toml` by a debugging
  developer is normal and shouldn't be denied; an actual write/redirect to
  `.leonard/` should be denied. The current heuristic can't distinguish.
- **Suggested fix shape:** Replace lexical scan with a real shell parser
  (resolves F1-F5 too); walk the AST for redirections (`>`, `>>`, `<>`,
  `&>`, `>|`), `tee`, `cp`, `mv`, `dd of=`, `rm`, `chmod` etc., AND for
  command names + flags that mutate. Only those targeting `.leonard` get
  blocked; reads pass through. ~150 LoC vs the current ~40, but the
  current approach is unsound.

### F8 — `bash` scanner JSON-escape failure: certain commands error rather than denying
- **Severity:** low (defense-in-depth issue, not a bypass)
- **Reproducer:**
  ```bash
  printf '%s' '{"tool_name":"Bash","tool_input":{"command":"echo x > \\.leonard/config.toml"}}' \
    | /tmp/leonard-hook pre-edit
  # → leonard-hook: pre-edit: hooks: payload decode error: ...
  ```
  Several quote-shaped command strings exit with `payload decode error`
  rather than reaching the guard at all. From the hook's stdout-stderr
  contract, Claude Code probably interprets exit code 2 as "block the
  tool call" — so this is failsafe-deny by accident, not by design.
- **Observed:** A malformed JSON payload yields exit 2 from the hook. The
  Bash tool launcher in Claude Code likely treats non-zero exit as a
  block — so the user does NOT execute the command, but they also see no
  helpful permission-decision-reason.
- **Expected:** Either decode loosely (accept the command verbatim and
  check it), or surface a clear `permissionDecisionReason` saying "I
  couldn't decode your command, refusing as a safety measure".
- **Suggested fix shape:** Catch the JSON unmarshal error in the Bash
  case specifically and emit a deny response with a useful reason.
  Possibly switch to a streaming/relaxed decoder for the `command` field.

### F9 — The `isUnderLeonardDirResolved` ancestor walk may attribute a project root containing `.leonard` to a path outside the project
- **Severity:** informational (no observed exploit)
- **Reproducer:** Conceptual — when a write target's path doesn't exist and
  the walk-up reaches the deepest existing ancestor that happens to live
  inside a `.leonard` directory tree (e.g., a project that legitimately
  *is* inside `/home/user/.leonard/myproject/`), the resolved path will
  match and the write will be denied even though the operator never
  intended `.leonard` as Leonard's storage. This is purely theoretical —
  using `.leonard` as a parent directory name would be a strange choice —
  but worth flagging because the walk-up logic doesn't distinguish "the
  *file's* parent is .leonard" from "*some ancestor* is .leonard".
- **Observed:** The walk-up resolves the deepest existing ancestor then
  re-attaches trailing segments. The segment check then runs over the
  *full resolved path*, including ancestors that have nothing to do with
  the operator's `.leonard` dir.
- **Expected:** The check should probably anchor at the project root and
  only match `.leonard` segments at or below it. Today it matches any
  `.leonard` anywhere in the absolute path, which has the same false-
  positive shape as F7 for filesystem paths.
- **Suggested fix shape:** Pass the project root into the resolver and
  only match segments at or below it. (Same fix shape as F7 in spirit:
  the lexical/scan approach is too coarse.)

---

## Probes that PASSED (verified closed)

### sec-3 F1 — symlink bypass (CRITICAL — closed)
`safe.txt → .leonard/config.toml` symlink, then `Write safe.txt` → **DENY**.
Verified on macOS. Reason returned with the full deny payload. Also tested:
- `bait/.leonard/foo.txt` through a symlinked parent → DENY
- `linktolconf/foo.txt` where `linktolconf` symlinks `.leonard/` → DENY
- `outer/normal_dir/config.toml` where `outer/normal_dir → .leonard` → DENY
- `does_not_exist/.leonard/config.toml` (file doesn't exist yet) → DENY (via lexical check)

### sec-3 F2 — case-insensitive bypass (CRITICAL — closed)
`.LEONARD/config.toml`, `.Leonard/config.toml`, `.LeOnArD/config.toml`,
`.lEonarD/config.toml` all → DENY. `strings.EqualFold` covers the segment
match completely.

### bughunt-7 F3 — backslash bypass on Unix (HIGH — closed)
`proj\.leonard\config.toml` → DENY. `filepath.ToSlash` normalization makes
the guard separator-agnostic.

### sec-3 F3 — store SQL LIMIT (HIGH — closed)
Synthesized 5000 `.go` files each declaring `func foo()`. Store-direct
query returns 1000 rows (capped at `MaxSymbolQueryRows = 1000`). MCP
`verify_symbol(name="foo")` returns 500 (capped at `MaxSymbolResults`).
`find_symbol(query="foo", limit=10000000)` returns 500 (clamped at SQL,
not just at the MCP wire).

### sec-3 F4 — 2 MiB helper RSS cap (HIGH — closed)
2.05 MiB Python file is **rejected as oversize**, surfaced as a
ParseFailure: `big.py: file size 2147780 bytes exceeds 2097152 byte
indexing cap`. `maxIndexedFileBytes = 2 << 20` confirmed at
`internal/index/indexer.go:607`.

### otel F4 — `-tags otel` instruments `leonard` + `leonard-mcp` (HIGH — closed)
```
/tmp/leonard-noop : 12,715,442 bytes,  1 telemetry symbol
/tmp/leonard-otel : 25,516,130 bytes, 1434 telemetry symbols
/tmp/mcp-noop     : 14,385,730 bytes,  1 telemetry symbol
/tmp/mcp-otel     : 25,319,554 bytes, 1434 telemetry symbols
```
Both binaries have real OTel instrumentation when built with the tag, with
the documented ~2× size difference.

### perf F4 — indexer worker pool (HIGH — closed)
300-file polyglot (200 .py + 100 .ts) corpus from scratch:
- v0.49.0 baseline (per bughunt-7): **5.99s wall**
- v0.50.2: **1.14s wall** (587% CPU usage = ~5.87 cores effective)

**5.25× speedup** matches the "5×" claim. Re-index on unchanged tree is
0.025s wall (hash cache hit). The `parseFailures` mutex correctly
serializes concurrent appends; `go test ./... -race` clean.

### Stress: 100 concurrent post-edit hooks → zero `database is locked` errors
Verified: all 100 finished in 0.77s wall, no errors. SQLite's WAL + 5-second
busy_timeout handles the contention cleanly. Confirms perf F4's concurrency
safety claim.

### NFC normalization at claim writes
- Empty string: `normalizeClaimPath("")` returns `""` (early return at line 26).
- NFC + NFD `café.go`: both normalize to identical NFC bytes via
  `norm.NFC.String`.
- Invalid UTF-8: `norm.NFC.String` returns the bytes unchanged. No panic.
- Mixed NFC/NFD parts in same path: normalized cleanly to NFC.

---

## Tally

### HIGH/CRIT CLOSED since v0.49.0
- **sec-3 F1** (symlink bypass) — closed in v0.50.0 via `isUnderLeonardDirResolved`.
- **sec-3 F2** (case-insensitive) — closed in v0.50.0 via `strings.EqualFold`.
- **bughunt-7 F3** (backslash on Unix) — closed in v0.50.0 via `filepath.ToSlash`.
- **sec-3 F3** (no SQL LIMIT) — closed in v0.50.1 (`MaxSymbolQueryRows=1000`).
- **sec-3 F4** (helper RSS) — closed in v0.50.1 (`maxIndexedFileBytes = 2 << 20`).
- **otel F4** (telemetry binaries) — closed in v0.50.2.
- **perf F4** (indexer worker pool) — closed in v0.50.2.

### HIGH/CRIT INTRODUCED OR STILL OPEN in this round (for v0.51 to fix)
- **F1 (CRITICAL)** — Bash backslash-escape bypass. Re-opens v0.46 RCE chain. Demonstrated end-to-end.
- **F2 (CRITICAL)** — Bash empty-quote bypass. Same RCE chain.
- **F3 (CRITICAL)** — Bash command-substitution bypass. Same RCE chain.
- **F4 (CRITICAL)** — Bash parameter-expansion bypass. Same RCE chain.
- **F5 (CRITICAL)** — Bash ANSI-C `$'...'` escape bypass. Same RCE chain.
- **F6 (HIGH)** — `wal_autocheckpoint` doesn't fire for per-process hook flow. bughunt-7 perf F6 only partially closed; the exact reproducer (700 sequential hooks → 4 MiB WAL) still passes.

### MED/LOW introduced or still open
- **F7 (MED)** — Bash scanner over-blocks innocent read-only commands.
- **F8 (LOW)** — JSON decode error path on certain Bash payloads.
- **F9 (INFO)** — `isUnderLeonardDirResolved` walk-up matches `.leonard` anywhere in the absolute path.

### Termination recommendation
**Do NOT stop the loop.** F1-F5 are five distinct CRITICAL bypasses of the
same fix (v0.50.0's bash scanner) — they re-open the precise RCE chain the
v0.46 work was designed to close. The fact that the bypass count grew under
adversarial probing (one initial concern → five working exploits in under an
hour) strongly suggests the lexical-scan approach is fundamentally
insufficient. Recommend v0.51 either:

  1. **Replace `bashTouchesLeonardDir` with an AST-level shell parser**
     (`github.com/mvdan/sh/syntax`) that walks redirects, pipes, command
     substitutions, and recursively descends. Block any write to a path
     whose resolved-string form matches `.leonard` (case-insensitive
     segment). Probably ~150 LoC; closes F1-F5 + F7 simultaneously.

  2. **Remove the bash scanner entirely** and document loudly that Leonard's
     `.leonard/` guard covers only `Edit/Write/MultiEdit/NotebookEdit`, and
     that operators must disallow Bash writes to `.leonard/` via Claude
     Code's own permission ACL. This is the simpler path and matches
     bughunt-7 F2's `(b)` recommendation; it gives up on defense-in-depth
     for Bash but stops claiming a guarantee Leonard can't actually deliver.

Independent of F1-F5: F6 needs an explicit `PRAGMA wal_checkpoint(PASSIVE)`
in `Store.Close()` — one-line change. The autocheckpoint pragma added in
v0.50.2 is correct for long-lived processes but not for the hook flow the
bughunt-7 reproducer measured.
