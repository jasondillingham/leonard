# Bug Hunt #5 — verifier-and-ledger

## Summary

Audit of the v0.37 `[post_edit.verify]` config surface and the v0.38 claim-ledger
hygiene changes. The two known follow-up nits (no working_dir path-trust
validation, no command size cap) confirm as **medium/low** severity, but the
investigation surfaces three new real bugs:

1. **High — `SupersedeOutstandingFailures` over-matches.** The `claim LIKE
   '%=failed%'` pattern matches any claim text containing the substring `=failed`,
   not just verifier-failure claims. A claim recorded via the `record_claim` MCP
   tool whose text happens to include `=failed` (e.g. user-supplied
   `args=failed_count=0`) is wrongly resolved on the next vet=ok run.
2. **Medium — `WorkingDir` resolves against the leonard-hook process cwd, not
   `projectRoot`.** When the user sets `working_dir = "subdir"` expecting "subdir
   under the project root", the verifier actually runs in `${leonard-hook's
   cwd}/subdir`. The two differ in real Claude Code use (Claude can invoke hooks
   from any cwd that happens to be inside the project tree).
3. **Medium — `timeout = "-30s"` parses fine and silently turns every verifier
   run into an instant "context deadline exceeded" failure**, with no stderr
   hint, no claim text differentiation, and no observable cause. The same
   parser-fallback mechanism that catches `"30"`/`"forever"` is bypassed because
   `time.ParseDuration("-30s")` returns `(-30s, nil)`.

The four PR-3 review categories Jason called out (working_dir path-trust,
command size cap, sh injection, working_dir defaulting) all check out as
intentional design choices except where noted. The ledger-hygiene work is
mostly solid — the supersede pattern is the one substantive miss.

`go test -race ./internal/hooks/ ./internal/store/` passes clean (5.4s
combined).

## Findings

### F1 — `SupersedeOutstandingFailures` matches any claim containing `=failed`, not just verifier failures

- **Severity:** high
- **Reproducer:**

  ```bash
  rm -rf /tmp/bh5 && mkdir /tmp/bh5
  cd /tmp/bh5 && /tmp/leonard init . >/dev/null
  cat > /tmp/bh5/.leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "true"
  timeout = "10s"
  EOF

  # Inject a free-form unverified claim whose text contains "=failed"
  # but is NOT a verifier-failure record. Mirrors a record_claim MCP
  # call from a model.
  sqlite3 .leonard/leonard.db "INSERT INTO claims
      (session_id, claim, evidence, verified, recorded_at)
      VALUES ('s', 'user_input=failed to load gracefully', '', 0,
              strftime('%s','now'))"

  # One vet=ok post-edit fires SupersedeOutstandingFailures and the
  # unrelated claim is now marked superseded.
  echo "package x" > foo.go
  echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"foo.go"},"cwd":"/tmp/bh5"}' \
      | /tmp/leonard-hook post-edit >/dev/null
  sqlite3 .leonard/leonard.db \
      "SELECT id, claim, superseded_by_claim_id
       FROM claims WHERE claim LIKE 'user_input%'"
  ```

- **Observed:** The row `user_input=failed to load gracefully` gets
  `superseded_by_claim_id` set to the new vet=ok claim's ID. The Stop hook
  will silently drop it from its summary.
- **Expected:** Supersede should only catch rows the post-edit hook itself wrote
  with the `<verb>=failed` claim-format established in `summariseClaim` (e.g.
  `; go vet=failed`, `; cargo check=failed`). Free-form `record_claim` text
  that happens to contain the substring `=failed` is operator-authored
  content; the verifier shouldn't decide it's been "resolved" by something
  unrelated.
- **Why it matters:** v0.16 (mcp F4) made the MCP `record_claim` path actively
  more useful by adding `file_path` so SupersedeClaimsForFile can match.
  Models are now encouraged to record claims through this path. A claim with
  text like `args=failed_count=0` or `error message: connection=failed` is
  realistic content, especially from prose-y assistants. Once it lands in the
  ledger, the next verifier-clean run silently destroys it.
- **Suggested fix shape:**
  - Tighten the LIKE to the substring `=failed;` or to a regex anchored at end
    or `;`-boundary. Or introduce a column (`source = 'verifier'`) on the
    claims table and filter by that.
  - Alternative: only supersede claims where `vet_ok = 0` (v3+ rows), which
    captures only verifier-derived failure rows.
- **Out of scope for this investigation:** Whether the supersede broadcast
  pattern itself is the right shape; F8 below covers cost/scale concerns.

### F2 — `working_dir` relative paths resolve against the leonard-hook process cwd, not the project root

- **Severity:** medium
- **Reproducer:**

  ```bash
  rm -rf /tmp/bh5b && mkdir -p /tmp/bh5b/subdir
  cd /tmp/bh5b && /tmp/leonard init . >/dev/null
  cat > /tmp/bh5b/.leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "pwd"
  working_dir = "subdir"
  timeout = "10s"
  EOF
  # Run leonard-hook from a non-root cwd (Claude Code commonly does
  # this — the hook walks up to find .leonard).
  cd /tmp/bh5b/subdir && echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"/tmp/bh5b/foo.go"},"cwd":"/tmp/bh5b"}' \
      | /tmp/leonard-hook post-edit
  sqlite3 /tmp/bh5b/.leonard/leonard.db "SELECT evidence FROM claims ORDER BY id DESC LIMIT 1"
  ```

- **Observed:** `chdir subdir: no such file or directory` — exec resolves
  `cmd.Dir = "subdir"` relative to the hook process's own cwd
  (`/tmp/bh5b/subdir`), not against `projectRoot` (`/tmp/bh5b`).
- **Expected:** `working_dir = "subdir"` should mean "the `subdir/` directory
  under the project root". The docstring on `VerifyConfig.WorkingDir` says
  "Useful when the verifier lives in a subdirectory of a polyrepo" — that
  implies project-root-relative.
- **Suggested fix shape:** In `MakeShellRunner`, when `workingDir` is set and
  not absolute, do `dir = filepath.Join(projectRoot, dir)`. Add a test in
  `post_edit_test.go` that exercises a relative working_dir from a non-root cwd.
- **Out of scope for this investigation:** Whether to validate that the joined
  path lies inside `projectRoot` (see F3 — the same fix shape could trivially
  add that check).

### F3 — No path-trust validation on `working_dir`; absolute paths to system directories are accepted

- **Severity:** medium
- **Reproducer:**

  ```bash
  rm -rf /tmp/bh5c && mkdir /tmp/bh5c
  cd /tmp/bh5c && /tmp/leonard init . >/dev/null
  cat > /tmp/bh5c/.leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "pwd && ls | head -3"
  working_dir = "/etc"
  timeout = "10s"
  EOF
  echo "package x" > /tmp/bh5c/main.go
  cd /tmp/bh5c && echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"main.go"},"cwd":"/tmp/bh5c"}' \
      | /tmp/leonard-hook post-edit
  sqlite3 .leonard/leonard.db "SELECT evidence FROM claims ORDER BY id DESC LIMIT 1"
  ```

- **Observed:** Verifier runs from `/etc`, outputs include the directory
  contents of `/etc`. Persisted in the evidence column.
- **Expected:** Reject `working_dir` values that escape the project root,
  symmetric to the `file_path` containment check the same hook does via
  `index.ResolveSafe` two lines earlier in HandlePostEdit.
- **Threat model nuance:** The PR-3 review noted this is "deferred" because
  `.leonard/config.toml` is project-authored. That's true on a steady-state
  basis. The wrinkle is that Claude Code itself can edit `.leonard/config.toml`
  through normal Edit/Write flows — there's no special-case protection on
  config.toml as a `pre-edit` target. A confused-deputy scenario: a
  prompt-injected session can rewrite `.leonard/config.toml` to point
  `working_dir` somewhere sensitive, and the very next post-edit hook executes
  the verifier from there. Combined with F4 (no command size cap, plus
  arbitrary `sh -c` invocation), this is more than a config-file note.
- **Suggested fix shape:**
  - Validate `working_dir` against `index.ResolveSafe(projectRoot, workingDir)`
    in `cmd/leonard-hook/post_edit.go` after the config load. On failure, log
    a stderr warning, fall back to projectRoot.
  - Combined with F2 (relative paths → projectRoot-relative), the natural shape
    is: `if abs && !under projectRoot → reject`; `if relative → join with
    projectRoot`. That gives a single containment story.
- **Out of scope for this investigation:** Whether to extend the same check to
  `command` itself (e.g. block `command = "rm -rf /"`). That's a much weaker
  defense — sh-injection-via-command is the explicit point of the feature.

### F4 — Negative timeouts (`timeout = "-30s"`) parse silently and break the verifier

- **Severity:** medium
- **Reproducer:**

  ```bash
  rm -rf /tmp/bh5d && mkdir /tmp/bh5d
  cd /tmp/bh5d && /tmp/leonard init . >/dev/null
  cat > /tmp/bh5d/.leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "echo hi"
  timeout = "-30s"
  EOF
  echo "package x" > /tmp/bh5d/main.go
  cd /tmp/bh5d && echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"main.go"},"cwd":"/tmp/bh5d"}' \
      | /tmp/leonard-hook post-edit 2>&1
  sqlite3 .leonard/leonard.db "SELECT claim, vet_error_summary FROM claims ORDER BY id DESC LIMIT 1"
  ```

- **Observed:** No stderr hint. The verifier reports `=failed` with
  `vet_error_summary = "context deadline exceeded"`. Every subsequent
  post-edit will fail the same way. The user has no signal that the timeout
  config is the cause.
- **Expected:** Either:
  - `parseVerifyTimeout` rejects non-positive durations and falls back to
    `defaultVerifyTimeout` with the same stderr hint it emits for parse
    failures, OR
  - the stderr hint mentions the actual cause (negative duration).
- **Related sub-edge cases probed:**
  - `timeout = "30"` — `time.ParseDuration` errors → fallback path fires
    correctly with `leonard: invalid [post_edit.verify].timeout "30", using
    1m0s: time: missing unit in duration "30"`. Works.
  - `timeout = "0s"` — parses to `d=0, err=nil` in `parseVerifyTimeout`, then
    `HandlePostEdit` sees `opts.VetTimeout == 0` and coerces to 30s. So `0s`
    silently means "30s" instead of "no timeout" — surprising but not broken.
    Worth a docstring note.
  - `timeout = "forever"` / `"30 seconds"` — both fail to parse; fallback path
    fires with a clear hint.
- **Suggested fix shape:** In `parseVerifyTimeout`, after the `ParseDuration`
  call, check `if d <= 0` and treat it as a parse failure with a tailored
  message.

### F5 — `VerifyVerb` produces malformed claim text on quoted/composed commands

- **Severity:** low
- **Reproducer:**

  ```bash
  # In a project with the hook wired up:
  cat > .leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "echo 'some error msg' && exit 7"
  EOF
  # Trigger any Edit.
  sqlite3 .leonard/leonard.db "SELECT claim FROM claims ORDER BY id DESC LIMIT 1"
  ```

- **Observed:** `claim = "tool=Edit file=...; index=ok; echo 'some=failed"`.
  The verb is `echo 'some` because `strings.Fields` doesn't honor shell
  quoting. The stray `'` survives into the claim text.
- **Expected:** Either keep the verb a single program token (drop everything
  after the first whitespace, including the quoted argument), or detect
  quoting and degrade to "verify"/"shell" for non-trivial inputs.
- **Other awkward verbs probed:**
  - `command = "CI=true cargo check"` → verb is `CI=true cargo`. Claim text
    reads `CI=true cargo=failed` — the `=` collision with the `<verb>=ok/failed`
    convention is also confusing for downstream parsers.
  - `command = "ruff check && mypy ."` → verb is `ruff check`. A failure could
    be from mypy; the verb is half the picture.
  - `command = "sudo cargo check"` → verb is `sudo cargo`. Bearable.
  - `command = " "` (whitespace only) → verb falls back to `"verify"` (good)
    but `sh -c " "` is invoked and exits 0; the claim records `verify=ok` for
    a no-op verifier. Document.
- **Suggested fix shape:**
  - Trim to the first program token when the second token looks like a value
    (contains `=`, starts with `'` or `"`).
  - Drop the verb-in-claim-text format entirely for non-default commands;
    use a fixed string like `verify` in the claim summary and let the
    `evidence` field carry the command. That also kills F1 (the `=failed`
    pattern can be safely tightened to `; verify=failed`).
- **Out of scope for this investigation:** The "verify" naming convention vs
  surfacing the full command string somewhere model-readable.

### F6 — `ResolveClaim` bypasses the v0.13 evidence size cap

- **Severity:** low
- **Reproducer:**

  ```bash
  NOTE=$(head -c 102400 /dev/urandom | base64 | tr -d '\n' | head -c 102400)
  /tmp/leonard claims resolve <id> --note "$NOTE"
  sqlite3 .leonard/leonard.db "SELECT length(evidence) FROM claims WHERE id=<id>"
  # → 102515 (or however large)
  ```

- **Observed:** The 100 KB note is appended to the claim's `evidence` column
  with no cap. `store.MaxClaimEvidenceBytes` (256 KiB) is enforced by the MCP
  `record_claim` handler (`internal/mcp/claims.go:91`) but not by
  `ResolveClaim`.
- **Expected:** The same cap (or a tighter one — notes don't need the full
  256 KiB) should apply. The cap is "shared" via `internal/store/limits.go`
  expressly because the v0.13 bughunt-4 caps F3 wanted one source of truth;
  ResolveClaim arrived after that work and dodged the validator.
- **Suggested fix shape:** In `newClaimsResolveCmd` (or in `Store.ResolveClaim`
  itself, alongside the trim), reject notes whose length would push
  `len(existing_evidence) + len(suffix)` past
  `store.MaxClaimEvidenceBytes`. A friendlier alternative is to truncate the
  note with a "…(truncated)" tail.

### F7 — `ResolveClaim` flips `verified` to true while claim text still says `=failed`

- **Severity:** informational (design choice with audit-trail implications)
- **Reproducer:**

  ```bash
  /tmp/leonard claims resolve <id-of-existing-failure-row> --note "intentional"
  sqlite3 .leonard/leonard.db "SELECT verified, claim FROM claims WHERE id=<id>"
  # → verified=1, claim still contains "...=failed"
  ```

- **Observed:** The row presents an audit-trail contradiction: `verified=1`
  with text `tool=Edit file=...; index=ok; go vet=failed`. An auditor or
  future tool that does `SELECT * FROM claims WHERE claim LIKE '%=failed%'`
  for a forensic report will surface this row but Stop will not.
- **Expected (design question):** If the goal is "this row should stop
  surfacing in Stop without losing the historical truth that the verifier
  failed once", consider:
  - A separate `resolved_at` / `resolved_note` column distinct from
    `verified`. Keep `verified` semantically tied to "the verifier said OK at
    record time".
  - Or, write a new claim row that supersedes the failure (the same shape
    SupersedeClaimsForFile uses) instead of mutating verified in place.
- **Cross-cut with F1:** A `claim LIKE '%=failed%' AND verified = 0` filter
  (which is exactly what `SupersedeOutstandingFailures` uses) skips ResolveClaim'd
  rows, so the audit-trail collision is only visible to direct DB readers, not
  to the supersede engine. But the human-facing `claims unverified` CLI and the
  MCP `get_unverified_claims` tool similarly skip them. The collision is real
  but contained — the inconsistency is "this row says it failed but it's
  resolved", not "this row will misbehave".

### F8 — `SupersedeOutstandingFailures` does an unbounded LIKE-with-leading-wildcard

- **Severity:** informational (acceptable today, plan for the future)
- **Query plan:** `EXPLAIN QUERY PLAN UPDATE claims SET ... WHERE verified = 0
  AND superseded_by_claim_id IS NULL AND claim LIKE '%=failed%' AND id != ?`
  reports `SEARCH claims USING INDEX idx_claims_verified (verified=?)`. So
  SQLite uses the `idx_claims_verified` index (added in v6) to restrict to
  verified=0 rows, then linear-scans the LIKE.
- **Observed:** On a ledger with 10k+ verified=0 unsuperseded rows, the LIKE
  matches every row in linear time per supersede call. Each successful
  vet=ok post-edit triggers this. The supersession cascade rapidly drains
  verified=0 down to small counts, so the steady-state cost is bounded — but
  a session with no successful runs and then a big "fix everything" landing
  could see one expensive supersede.
- **Expected:** Acceptable today (the partition stays small in practice). If
  ledger size becomes a concern, the right shape is the F1 fix (replace the
  LIKE with a typed column).
- **Out of scope for this investigation:** Benchmarking on a real 10k+
  unsuperseded-row ledger; the store-perf lane in bughunt-4 would re-open if
  this becomes a hotspot.

### F9 — `command` size is unbounded

- **Severity:** low (PR-3 known nit)
- **Observed:** TOML happily decodes a 100 MB command string. `sh -c <100 MB>`
  on macOS/Linux can take it (argv via execve is the bottleneck — typical
  ARG_MAX on macOS is 1 MB, on Linux 128 KB+ depending on distro). At
  configured-by-the-user-on-their-own-machine trust boundaries this is mostly
  benign, but TOML decode allocates the full string, persists it in memory
  across re-reads, and the eventual `sh -c` exec would silently truncate or
  ENOMEM on absurdly large inputs.
- **Expected:** Cap at, say, 4 KiB (much larger than any realistic verifier
  invocation; `cargo check --workspace --features foo,bar,baz` fits in
  ~80 bytes). Reject with a clear hint and fall back to defaults — matching
  parseVerifyTimeout's "log + fall back" pattern.
- **Out of scope for this investigation:** Cap on `working_dir` (less
  interesting; filesystem path limits already kick in).

### F10 — `cmd/leonard claims resolve` surfaces `sql.ErrNoRows` directly

- **Severity:** low
- **Reproducer:**

  ```bash
  /tmp/leonard claims resolve 999999
  # → "leonard: claims resolve: sql: no rows in result set"
  ```

- **Observed:** The CLI passes `sql.ErrNoRows` straight through, leaking the
  database/sql package internal string into the user-facing error.
- **Expected:** Friendlier message: `leonard: claims resolve: no claim with id
  999999`. The store-layer `ResolveClaim` already returns `sql.ErrNoRows`
  sentinel which the CLI can detect with `errors.Is(err, sql.ErrNoRows)`.
- **Suggested fix shape:** In `cmd/leonard/claims.go newClaimsResolveCmd`,
  wrap with a sentinel check before printing.

### F11 — `[post_edit.verify].command` whitespace-only is accepted as a working verifier

- **Severity:** informational
- **Observed:** `command = " "` and `command = "\t"` pass the `verify.Command
  != ""` check in `cmd/leonard-hook/post_edit.go:60`, get passed to `sh -c
  "<whitespace>"` (which exits 0 with no output), and produce a verified=1
  claim with verb "verify". Effectively an opt-in to "always succeed, never
  actually verify anything". Probably not what a user intended if they typed
  a space by accident.
- **Expected:** `strings.TrimSpace(verify.Command) == ""` should also trigger
  the fallback to the default `go vet` behavior (or log a hint).
- **Suggested fix shape:** Change the gate to
  `strings.TrimSpace(verify.Command) != ""`.

### F12 — `/bin/sh` semantics differ across host platforms

- **Severity:** informational
- **Observed:** macOS `/bin/sh` is `bash --posix`; Ubuntu/Debian `/bin/sh` is
  `dash`; Alpine is `ash`. Real verifier commands that lean on bashisms
  (`[[ ... ]]`, `=~`, `<( ... )` process substitution, `${var,,}`) will work
  on a developer's Mac and break on a CI runner of the same Leonard config.
- **Expected:** Document this on `VerifyConfig.Command` so users know the
  contract is POSIX-shell, not bash.
- **Suggested fix shape:** README-only. No code change.

### F13 — Concurrent `SupersedeOutstandingFailures` calls coexist without deadlock or corruption

- **Severity:** informational (this one is a *positive* finding)
- **Reproducer:** 30 failure-recording post-edits seeded, then 10 vet=ok
  post-edits fired concurrently (`for ... &; wait`). Each invocation called
  SupersedeClaimsForFile + SupersedeOutstandingFailures. All ran to completion
  with no stderr output and no surviving `verified=0 AND
  superseded_by_claim_id IS NULL AND claim LIKE '%=failed%'` rows.
- **Mechanism:** SQLite's default UPDATE statements take a RESERVED then
  EXCLUSIVE lock; the busy_timeout(5000) pragma in `buildDSN` absorbs the
  contention. Each post-edit's pair of UPDATEs is independent rather than
  transactional (no `BEGIN`/`COMMIT` wrapping the two supersede calls), so
  two concurrent vet=ok claims racing to supersede the same row don't deadlock —
  the second UPDATE just finds the row already pointed at the first claim
  and the `superseded_by_claim_id IS NULL` filter excludes it. Self-supersede
  is also prevented by the `AND id != ?` clause.

### F14 — `migrateV7` is idempotent against re-runs

- **Severity:** informational (positive finding)
- **Reproducer:**

  ```bash
  sqlite3 .leonard/leonard.db "UPDATE meta SET value='6' WHERE key='schema_version'"
  /tmp/leonard claims unverified  # triggers re-open → re-migrate
  sqlite3 .leonard/leonard.db "SELECT value FROM meta WHERE key='schema_version'"
  # → 7
  ```

- **Observed:** Re-running v7 against a DB whose v7-target rows are already
  gone deletes 0 rows. A fresh init runs v1..v7 in sequence; v7 against an
  empty claims table is also a no-op.
- **Forward-compat note:** A future Leonard version that adds a new
  "tool-layer rejection" claim shape won't be retroactively cleaned by v7.
  That's the right answer — migrations are point-in-time. Worth a comment
  on `migrateV7` noting the principle.

### F15 — `command = ""` (the documented no-op sentinel) works as expected

- **Severity:** informational (positive finding)
- **Observed:** With `command = ""`, the cmd/leonard-hook wiring skips the
  shell-runner branch entirely. The hook falls back to RunGoVet auto-detection.
  In a non-Go project (no `go.mod`), the runVet path reports
  `vet.Ran = false` and the claim text is `go vet=skipped (no go.mod)`.
- **Verified by review of `cmd/leonard-hook/post_edit.go:60` —
  `if verify := cfg.PostEdit.Verify; verify.Command != ""`** — exact empty-string
  comparison. F11 covers the whitespace-but-not-empty edge.

### F16 — `AlwaysVet` skips the `hasGoModule` gate in non-Go projects

- **Severity:** informational (positive finding)
- **Reproducer:**

  ```bash
  mkdir /tmp/bh5-py && cd /tmp/bh5-py && /tmp/leonard init . >/dev/null
  # no go.mod
  cat > .leonard/config.toml <<'EOF'
  [post_edit]
  [post_edit.verify]
  command = "echo verifier-ran"
  EOF
  echo 'x = 1' > foo.py
  echo '{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"foo.py"},"cwd":"/tmp/bh5-py"}' \
      | /tmp/leonard-hook post-edit
  # → claim "tool=Edit file=...; index=ok; echo verifier-ran=ok"
  ```

- **Observed:** Verifier ran. AlwaysVet correctly suppresses the Go-only
  short-circuit.

### F17 — Verifier exit-7 carries exit code and output cleanly into evidence

- **Severity:** informational (positive finding)
- **Observed:** `command = "echo 'errmsg' && exit 7"` produces a claim with
  `evidence` containing `exit: exit status 7` and the stderr/stdout
  combined. `vet_error_summary` carries the first non-header line. The
  `additionalContext` model-facing block correctly tags it as FAILED.
- **Caveat:** The verb-extraction issue from F5 leaks the malformed verb
  string into both summary and additionalContext, hurting readability of an
  otherwise-good signal.

### F18 — Verifier timeout via context.WithTimeout wraps cleanly

- **Severity:** informational (positive finding)
- **Reproducer:** `command = "sleep 10"`, `timeout = "1s"`. Result: claim
  records `=failed`, `vet_error_summary = "context deadline exceeded"`,
  partial output (if any) preserved in evidence. The context cancellation
  reaches the subprocess and kills it. No process leak observed.

### F19 — `verified=0` filter excludes the fresh vet=ok claim from supersede

- **Severity:** informational (positive finding)
- **Observed:** When HandlePostEdit records the new vet=ok claim, it's
  inserted with `verified=1`. Then `SupersedeOutstandingFailures` runs with
  `WHERE verified = 0 AND ...`, so the new row can't self-match. The
  belt-and-suspenders `AND id != ?` further excludes it. Self-supersede
  protection is solid.

### F20 — Schema-change watchers note: `SupersedeOutstandingFailures` writes through `WHERE superseded_by_claim_id IS NULL`, so it interlocks correctly with `SupersedeClaimsForFile`

- **Severity:** informational (positive finding)
- **Mechanism:** When HandlePostEdit calls SupersedeClaimsForFile first and
  then SupersedeOutstandingFailures, any row the first call already pointed
  at the new claim has `superseded_by_claim_id = newID` (not NULL), so the
  second call's `WHERE superseded_by_claim_id IS NULL` excludes it. No
  double-write; no overcounting in the RowsAffected return.

## Things that worked

- v0.38 `handleEscapedPath` no longer recording a claim — confirmed via direct
  reproducer.
- Project-wide supersede when triggered by a real vet=ok run (the file-scoped
  multi-file-fix-cascade case is the v0.38 motivation; it works).
- `time.ParseDuration` fallback for parseable-but-unitless ("30") and
  syntactically-bad ("forever", "30 seconds") values — clean stderr hint, no
  crash.
- `[post_edit.verify].command = ""` (TOML default written by `leonard init`)
  preserves v0.1 RunGoVet auto-detection behavior.
- `command = "echo a | tee b"` shell-pipeline works; verb extracted as `echo`,
  not split on `|`.
- Concurrent post-edits via the supersede pair (30 failure rows + 10 parallel
  vet=ok writers) ran clean with no `database is locked` errors, no
  deadlocks, no surviving stale failures.
- `go test -race ./internal/hooks/ ./internal/store/` is clean (5.4s).
- `migrateV7` idempotence on re-application (forced via meta.value='6' downgrade
  + re-open) is correct.
- `leonard claims resolve <id>` happy path: writes evidence suffix, flips
  verified=1, prints `leonard: claim #N resolved`.
- `verified=0` partition uses `idx_claims_verified` index for the supersede
  query, so the LIKE scan is bounded to the unverified subset.
- MCP `record_claim` v0.16 `file_path` field continues to flow into
  `RecordClaim` → `claims.file_path`, where `SupersedeClaimsForFile` can match
  it; verified by code review of `internal/mcp/claims.go:93` →
  `internal/mcp/adapter.go:196`.

## Open questions

- **F1 — `=failed` pattern over-match:** how likely is real-world collision?
  Need to scan the kinds of free-form `record_claim` text Claude Code sessions
  actually produce in the wild. The reproducer is contrived; if no real claim
  text contains `=failed` outside of verifier output, this is informational, not
  high-severity. Recommending high because the failure mode is silent
  data-loss and the fix shape (tighten the LIKE) is cheap.
- **F3 — config.toml as a confused-deputy target:** is the pre-edit hook's
  fabricated-symbol guard the only safety net here? Should `.leonard/config.toml`
  itself be denied as an Edit target, similar to how some pre-commit hooks
  treat `.git/config`?
- **F5 — `VerifyVerb` quoting:** is the right shape "make the verb dumber"
  (just the first program token, drop everything else) or "tag the claim with
  a structured verifier_id"? The latter unlocks F1's tightening and a cleaner
  Stop summary; the former is one-line.
- **Remaining 32 unverified-claims rows in Leonard's own DB:** the brief
  notes these include legit `index=failed; go vet=ok` claims from bughunt-3
  rust-probe scratch files. Should the v7 cleanup pattern be extended to
  catch `claim LIKE '%/tmp/rust-probe/%'` or any `file_path` outside the
  repo? Probably no — those rows are *real* failures Leonard recorded
  faithfully; the right cleanup is `leonard claims resolve` per-row. The CLI
  exists for exactly this purpose. (Did not query the production DB to
  confirm the 32 count — auto-mode classifier denied that read; relied on the
  brief's reported numbers.)
- **F8 — supersede broadcast cost:** at what verified=0 partition size does
  the LIKE scan start to hurt? Steady-state in practice should be low (Stop
  + supersede keep it small). A pathological "1000 failure claims piled up
  during a long no-verify-clean session" could be measured but wasn't here.
