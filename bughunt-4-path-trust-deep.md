# Bug Hunt #4 — path-trust-deep

## Summary

Probed `index.ResolveSafe` and every caller that flows attacker-controlled
paths into the filesystem. The v0.8 guard works correctly for the
straightforward cases (lexical `..` escape, absolute path outside root,
symlink-inside-pointing-outside, `/private/var` → `/var` macOS root
symlink) and the prefix-confusion case (root `/tmp/foo` vs sibling
`/tmp/foobar`). The serious gap is **the pre-edit hook never calls
ResolveSafe** — it opens any `.go`-suffixed `file_path` from a PreToolUse
payload directly via `go/parser`. Three other gaps are
medium-severity: the lexical-fallback branch for dangling symlinks
pointing outside the root, the lack of unicode normalization on
storeKeys (NFC vs NFD compose to different store rows for the same
APFS file), and pruneStaleFiles/doctor doing direct
`filepath.Join(root, f.Path)` on stored paths without re-validating
them with ResolveSafe — a pre-v0.8 polluted DB row would survive prune.
Several smaller informational gaps: TOCTOU between check and use,
embedded NUL/newline bytes accepted by ResolveSafe (rejected by OS
later but the rejection isn't classified as "path escapes root").

Probe driver lives at `/tmp/path-trust-probe/` (shadow copy of
ResolveSafe + ~40 adversarial inputs). Source is unchanged on disk;
findings below cite line numbers in `internal/index/indexer.go` and
`internal/hooks/pre_edit.go` from the working tree.

## Findings

### F1 — pre-edit hook bypasses ResolveSafe entirely

- **Severity:** medium
- **Reproducer:**
  Send a PreToolUse payload with a crafted `tool_input.file_path`:
  ```json
  {"hook_event_name":"PreToolUse","tool_name":"Edit",
   "tool_input":{"file_path":"/etc/hosts.go",
                  "new_string":"package main\nimport _ \"x\"\n"}}
  ```
  `internal/hooks/pre_edit.go:171` only gates by `strings.HasSuffix(filePath, ".go")`.
  At `pre_edit.go:177` `readFileImports(filePath)` calls `parser.ParseFile`
  on the unvalidated path. Nothing in the pre-edit code path calls
  `index.ResolveSafe`.
- **Observed:** Hook reads any `.go`-suffixed path the payload asks for.
  Works as a file-existence oracle (different code paths for missing
  vs present vs unparsable files) and would parse the contents of any
  user-readable `.go` file on the box. Probes like
  `/Users/victim/.aws/credentials.go`, `/etc/passwd.go`,
  `/private/var/some-other-project/secrets.go` all execute the
  read attempt — the timing and the resulting allow/deny decision
  expose whether the path exists.
- **Expected:** Per the security-1 F1 threat model that motivated
  ResolveSafe, every attacker-controlled `file_path` should pass
  through ResolveSafe before any filesystem read or write. The
  post-edit hook does this (`hooks/post_edit.go:182`); the pre-edit
  hook does not.
- **Suggested fix shape:** In `decidePreEdit`, after extracting
  `filePath` (`pre_edit.go:164–170`) and before either the
  `.go`-suffix check or the `readFileImports` call, run
  `safe, ok := index.ResolveSafe(opts.ModuleRoot, filePath)` and
  emit an allow response with a system message ("path outside root,
  skipping fabrication check") when `!ok`. The hook can't usefully
  *deny* the edit on path-escape (the user might be legitimately
  editing a sibling project; pre-edit is best-effort) but it must
  not open the file.
- **Out of scope for this investigation:** Whether
  `permissionDecision: "deny"` is the right user-facing response
  when path escapes root, vs the silent-allow this fix proposes.

### F2 — Dangling symlinks whose target points outside root pass ResolveSafe

- **Severity:** medium
- **Reproducer:** Inside a project root, create
  `ln -s /tmp/nonexistent-target-9999 dangle-out`, then ask
  `ResolveSafe(root, "dangle-out")`.
  ```
  dangling symlink target outside root  | ok=true
  ```
  (See `/tmp/path-trust-probe/main.go` "Symlinks" section.)
- **Observed:** `ResolveSafe` falls back to the lexical check
  (`indexer.go:357`). The symlink itself is lexically under root
  ("dangle-out" sits under root), `filepath.EvalSymlinks(abs)` returns
  an error because the target doesn't resolve, and the `if errA ==
  nil` branch at `indexer.go:360` is skipped — so the resolved-symlinks
  cross-check never runs. ResolveSafe returns `(abs, true)`.
- **Expected:** A symlink whose declared target is outside the root
  should be rejected even when the target doesn't currently exist.
  Otherwise a post-edit hook firing on `dangle-out` after the
  attacker `touch /tmp/nonexistent-target-9999` is now a TOCTOU
  write-into-outside primitive.
- **Suggested fix shape:** When `filepath.EvalSymlinks(abs)`
  returns a non-nil error, do a per-component `os.Lstat`/`os.Readlink`
  walk from rootClean down to `abs`'s final component; if any
  intermediate link's target evaluates to a path containing `..` or
  starting with `/`-not-under-rootClean, reject. Cheaper alternative:
  on `EvalSymlinks` error, walk `abs` upward looking for the deepest
  existing prefix, run `EvalSymlinks` on that prefix, and check that
  `abs` is still contained relative to the resolved prefix.
- **Out of scope for this investigation:** Whether to also detect
  symlink-chain-too-long (`ELOOP`) and reject vs accept — currently
  loops pass the lexical check (`loopA -> loopB -> loopA` returns
  ok=true with the unresolved abs path, which downstream `os.ReadFile`
  then rejects with ELOOP). Not a security issue — just a noisier
  failure than necessary.

### F3 — Unicode NFC/NFD store-key duplication on case-normalizing filesystems

- **Severity:** medium
- **Reproducer:**
  ```
  root/café.go  (single file on APFS)
  ResolveSafe(root, "café.go" /* NFC: c-a-f-é(U+00E9) */)  → ok=true, abs ends in NFC bytes
  ResolveSafe(root, "café.go" /* NFD: c-a-f-e + U+0301 */) → ok=true, abs ends in NFD bytes
  os.Stat(absNFD) → SUCCEEDS (APFS normalizes internally)
  filepath.Rel(root, absNFC) → "63 61 66 c3 a9 2e 67 6f"  (NFC)
  filepath.Rel(root, absNFD) → "63 61 66 65 cc 81 2e 67 6f" (NFD)
  ```
  Two different `storeKey` strings for the **same on-disk file**.
- **Observed:** `Indexer.storeKey` (`indexer.go:454`) is
  `filepath.ToSlash(filepath.Rel(Root, abs))`. ResolveSafe and
  `storeKey` preserve whatever normalization form the caller
  supplied. APFS happily reads/writes through either form. So
  `UpsertFile` (`store.go:336`, conflict key is `path`) inserts **two
  rows** for the same file when one post-edit fires with NFC and
  another with NFD. `pruneStaleFiles` will then stat both abs paths;
  both succeed on APFS, so neither row gets pruned. The symbol table
  ends up double-populated: every symbol in the file gets one row
  keyed by NFC-path and another keyed by NFD-path.
- **Expected:** A single on-disk file produces a single store row.
  Either ResolveSafe normalizes (preferred) or the store key
  normalizes before the `path` column is written.
- **Suggested fix shape:** Add a `normalizePath(abs)` helper in
  `internal/index` that runs `unicode/norm`.`NFC.String()` over the
  path bytes; call it at the bottom of ResolveSafe before returning,
  and also in `storeKey`. Cheap (one alloc per call). Independently,
  consider documenting that Leonard chooses NFC as canonical — the
  fix only matters on case-normalizing filesystems (APFS, HFS+,
  NTFS); ext4 keeps the bytes verbatim so the two paths name two
  different files there.
- **Out of scope for this investigation:** Case-only collisions
  (`README.md` vs `readme.md` on case-insensitive APFS volumes).
  Same family of bug; treat in the same fix.

### F4 — pruneStaleFiles, doctor, and recent_changes don't re-validate stored paths against ResolveSafe

- **Severity:** medium
- **Reproducer:** A pre-v0.8 `.leonard/leonard.db` could contain a
  file row with `path = "../escape.go"` (the very confused-deputy
  ResolveSafe was built to plug). Running `leonard index` against that
  DB on v0.12 triggers `pruneStaleFiles` (`indexer.go:218`):
  ```go
  abs := filepath.Join(i.Root, filepath.FromSlash(f.Path))  // line 229
  _, statErr := os.Stat(abs)                                  // line 230
  ```
  `filepath.Join("/proj", "../escape.go")` cleans to `/escape.go`
  (one level above root). If the file exists, prune keeps the row;
  if not, prune deletes the row. Either way, no `ErrPathEscapesRoot`
  is raised and the stale row's *abspath* is read from outside the
  project. Same untrusted-stored-path stat happens in
  `cmd/leonard/wire_real.go:172` (the doctor `StaleFiles` check).
- **Observed:** A polluted DB silently retains out-of-root rows
  through the prune sweep and stat's the corresponding absolute
  paths each `leonard doctor` run.
- **Expected:** `pruneStaleFiles` and the doctor's StaleFiles check
  should treat any row whose path doesn't resolve under root via
  `ResolveSafe` as stale-and-deletable.
- **Suggested fix shape:** Replace the two
  `filepath.Join(root, f.Path)` + `os.Stat` blocks with
  ```go
  abs, ok := ResolveSafe(i.Root, filepath.FromSlash(f.Path))
  if !ok {
      toDelete = append(toDelete, f.Path)
      continue
  }
  _, statErr := os.Stat(abs)
  ...
  ```
  Side benefit: a future migration that detects an escape can log
  the bad row's path before deletion so operators see what got
  cleaned up.
- **Out of scope for this investigation:** Whether to write a
  one-shot migration that scans every file row at `store.Open` time
  and removes the unsafe ones, rather than waiting for the next
  `leonard index` run.

### F5 — TOCTOU: ResolveSafe is a check, not a commitment; symlink swap between check and use is unmitigated

- **Severity:** low (documented in design comments, but worth a
  pinned test + a note in the security-review doc)
- **Reproducer:** See `/tmp/path-trust-probe/main.go` "TOCTOU
  informational" section.
  ```
  pre-swap:  ResolveSafe(root, "swap/ok.go") → ok=true, abs=.../root/swap/ok.go
  (between calls: rm root/swap; ln -s /tmp/outside root/swap)
  post-swap: ResolveSafe(root, "swap/ok.go") → ok=true, abs=.../root/swap/ok.go
              (same lexical path; subsequent os.ReadFile follows the new link)
  ```
- **Observed:** Both calls return `ok=true` with the same abs path.
  The post-edit hook stat's at `post_edit.go:193` then calls
  `IndexFile(filePath)` at `:198` which calls `os.ReadFile`
  (`indexer.go:391`). An attacker who can swap `swap/` from the
  legitimate target to a symlink-out between line 182 (ResolveSafe)
  and line 391 (os.ReadFile) reads the foreign file's contents into
  the symbol index.
- **Expected:** Either explicit acknowledgment that ResolveSafe is
  a check-not-commitment and the threat model excludes filesystem
  race adversaries, or a hardened path that opens the file via a
  resolved fd and re-checks `Fstat` to confirm the dev/inode matches
  the resolved-symlinks value at check time.
- **Suggested fix shape:** Add a comment to ResolveSafe documenting
  the TOCTOU window explicitly. For high-assurance behavior:
  resolve via `os.Open` + `os.File.Fd` + `golang.org/x/sys/unix.Fstatat`
  with `AT_SYMLINK_NOFOLLOW`; compare the file's `st_dev` against
  root's `st_dev` to catch cross-filesystem escapes. v0.12-grade
  fix is the comment + a `BUGS.md`-style entry; v1.0 fix is the
  hardened path. The Round-3 brief explicitly closed "obvious" cases
  — flagging this as the next-tier residual risk fits the lane.
- **Out of scope for this investigation:** Whether Claude Code's
  trust model assumes the local filesystem is mutually trusted with
  the user (yes for v0 — a malicious local process is out of
  Leonard's threat model — but worth saying so explicitly).

### F6 — Embedded NUL/newline bytes in claimed paths pass ResolveSafe; OS rejects on read

- **Severity:** low (no privilege escalation; just diagnostic
  noise)
- **Reproducer:**
  ```
  ResolveSafe(root, "ok.go\x00../etc/hosts")  → ok=true, abs contains NUL
  ResolveSafe(root, "ok.go\n../etc/hosts")    → ok=true, abs contains \n
  os.Stat(abs) → "invalid argument" (NUL) or "no such file" (newline)
  ```
- **Observed:** `filepath.Clean` doesn't strip these bytes;
  `filepath.Join` happily includes them; lexical containment via
  `filepath.Rel` returns a `..`-free relative path. The path
  succeeds ResolveSafe and fails on the next syscall. The post-edit
  hook then records a claim row whose `file_path` column contains
  the raw NUL — which sqlite stores fine, but every downstream
  text-based tool (logs, grep, CLI output) handles awkwardly. The
  resulting claim text "file not found on disk — index and vet
  skipped" (`post_edit.go:260`) gives the attacker no signal that
  the request was malformed.
- **Expected:** Reject any claimed path containing a NUL or newline
  byte. These can't appear in any legitimate Unix or Windows
  pathname.
- **Suggested fix shape:** At the top of ResolveSafe, after the
  empty-string guard, add
  ```go
  if strings.ContainsAny(claimed, "\x00\n") {
      return "", false
  }
  ```
  Same for `root` for symmetry.
- **Out of scope for this investigation:** Whether other ASCII
  control chars (CR `\r`, vertical tab `\v`) should also be
  rejected. Probably yes — none of them appear in legitimate paths
  — but the security upside is marginal compared to the NUL/newline
  pair.

### F7 — Windows-style paths (backslash separators, drive letters) pass ResolveSafe on Unix and produce nonsense storeKeys

- **Severity:** informational
- **Reproducer:**
  ```
  ResolveSafe(root, "..\\outside\\secret.txt")  → ok=true, abs="<root>/..\\outside\\secret.txt"
  ResolveSafe(root, "C:\\foo\\bar")             → ok=true, abs="<root>/C:\\foo\\bar"
  ```
- **Observed:** On Unix, `\` is a literal pathname character.
  `filepath.Clean` doesn't touch backslashes, `Rel` reports the
  path as lexically inside root, and the resulting `os.Stat` returns
  ENOENT. No security impact today — the attacker can't traverse
  out because `\` ≠ `/` on Unix — but the claim row will carry the
  garbage path string verbatim.
- **Expected:** Either normalize backslashes to forward slashes on
  input (so a confused cross-platform tool can't smuggle them in),
  or reject any claimed path containing `\` on Unix. Note: this
  collides with legitimate-but-rare use of `\` in filenames; the
  v0 brief doesn't promise filename-with-backslash support.
- **Suggested fix shape:** Optional follow-up — at most a deny on
  `\` (since Leonard targets a Go ecosystem that doesn't use them)
  with a clear error message. Lower priority than F1–F4.
- **Out of scope for this investigation:** Windows host behavior —
  ResolveSafe on Windows would have `filepath.Separator == '\'` and
  the lexical check would treat `..\\foo` correctly. The probe ran
  on darwin only.

### F8 — Whitespace-prefixed paths pass ResolveSafe and produce stat-not-found at consume time, with a misleading "file not found" claim

- **Severity:** informational
- **Reproducer:** `ResolveSafe(root, " ok.go") → ok=true`,
  subsequent `os.Stat("<root>/ ok.go")` returns ENOENT (no file
  with a leading space).
- **Observed:** The post-edit hook's
  `filePath := strings.TrimSpace(payload.ToolInput.FilePath)`
  (`post_edit.go:162`) trims outer whitespace, but only from the
  raw payload field — the trim doesn't happen for the value the
  pre-edit hook reads (`pre_edit.go:164`, also `TrimSpace`, but
  internal whitespace and the post-trim value can still contain
  legitimate-looking-but-not-extant filenames). Net effect: noisy
  ENOENT, no security gap.
- **Expected:** Same as current behavior; ENOENT-handling already
  covers this gracefully via `handleMissingFile`. Noting it because
  the brief asked about "trailing slashes / repeated separators /
  unusual whitespace" — all of those are handled correctly (slashes
  collapse via `filepath.Clean`, whitespace surfaces as
  file-not-found).
- **Suggested fix shape:** None needed.

### F9 — record_decision / record_claim accept untrusted file_path strings in related_files; get_stale_decisions compares them by literal string

- **Severity:** low (no filesystem read happens — the comparison is
  pure SQL `WHERE path = ?` against the store) — but worth noting
  for the trust-boundary doc
- **Reproducer:**
  ```python
  # via MCP record_decision
  {"topic":"t","choice":"c","reasoning":"r",
   "related_files":["/etc/passwd","../escape.go","C:\\foo"]}
  ```
  `internal/store/store.go:605–628` serializes the array verbatim
  into the `related_files` JSON column. `GetStaleDecisions`
  (`store.go:684`) queries `SELECT 1 FROM files WHERE path = ?`
  with each value. Mismatch → "missing" → flagged as stale.
- **Observed:** No filesystem read occurs; the path strings live
  inside the decisions table as JSON. `MissingFiles` ends up
  listing arbitrary attacker text because none of those values
  matches a real `files.path` row. Not exploitable per se — but a
  malicious payload could clutter `decisions stale` output with
  attacker-controlled strings the user then sees.
- **Expected:** Either validate that each related_files entry
  passes `index.ResolveSafe(projectRoot, p)` at record_decision
  time (rejecting absolute / out-of-root paths), or accept them
  as-is but document that related_files is freeform-text not a
  filesystem reference. The current docstring at `decisions.go:19`
  says "file paths this decision reasons about" which implies they
  should resolve.
- **Suggested fix shape:** Plumb projectRoot through to
  `recordDecision` (already available in the CLI; for the MCP
  surface, the server already knows it via `os.Getwd()` at startup).
  Reject any entry whose ResolveSafe-against-root returns ok=false.
  Backward-compat-safe because the existing schema doesn't change.
- **Out of scope for this investigation:** Whether the same
  validation should apply to related_symbols (no — symbol names
  are opaque identifiers, not filesystem references).

### F10 — readSiblingPackages walk follows directory symlinks that point outside the module root

- **Severity:** informational
- **Reproducer:** Inside a project, create
  `ln -s /usr/local/src sibling-lib` (a directory symlink pointing
  outside the module). Trigger a pre-edit hook.
- **Observed:** `internal/hooks/pre_edit.go:383`
  `filepath.WalkDir(moduleRoot, ...)` — the docstring of
  `filepath.WalkDir` says "WalkDir does not follow symbolic links",
  meaning it WILL emit the symlink's *fileInfo* but won't descend
  through it. The hook's per-file branch
  (`pre_edit.go:397`) skips file symlinks. But the directory
  symlink itself is presented to the callback with `d.IsDir()`
  returning the *link's* IsDir (false), so the dir-skip branch at
  `:387` isn't entered — the entry is then evaluated by the
  file-handling branch which sees the link is a symlink and
  returns nil. Net behavior: walk doesn't descend into the link's
  target. **No actual gap** — verified by reading `path/filepath`
  docs and the existing comment at `:394`. Noting because the
  brief explicitly asked.
- **Expected:** Walk should not follow symlink dirs outside the
  module root. Current behavior already meets this.
- **Suggested fix shape:** None. Worth a Things-That-Work entry.

## Things that worked

The following were probed and confirmed correct in ResolveSafe:

- **Lexical `..` escape** (`../outside/secret.txt`) — rejected.
- **Absolute path outside root** (`/etc/hosts`) — rejected.
- **Symlink inside root pointing to absolute outside file**
  (`<root>/passwd -> /etc/passwd`) — rejected via the
  resolved-symlinks secondary check at `indexer.go:360–367`.
- **Symlink inside root pointing to outside dir**
  (`<root>/link-out -> /tmp/outside`) — rejected, including
  paths under the symlink (`link-out/secret.txt`).
- **Symlink whose target is `..`** (`<root>/escape -> ..`) —
  rejected even though the symlink itself is lexically inside
  root, because EvalSymlinks resolves to the parent and the
  containment check fails.
- **`/var` → `/private/var` macOS root symlink case** — the
  fallback "resolved-vs-resolved" branch at `indexer.go:377–384`
  correctly accepts `<symlinked-root>/ok.go` when the symlink and
  the canonical path both resolve into the same real directory.
- **Prefix-confusion** (root `/tmp/foo`, candidate
  `/tmp/foobar/x.go`) — rejected because `filepath.Rel` returns
  `..`-prefixed.
- **Empty root, empty claimed, both empty** — all rejected at the
  guard at `indexer.go:332`.
- **Repeated separators, mid-path `//`, trailing slashes** —
  `filepath.Clean` collapses correctly; lexical containment
  decides as expected.
- **The IndexAll walker skips file-level symlinks**
  (`indexer.go:170`) — verified by reading the walk code; matches
  the security-1 F3 fix.
- **readSiblingPackages skips symlinks at the file level**
  (`pre_edit.go:397`) — same.
- **Post-edit hook rejects out-of-root file_path payloads** —
  `post_edit.go:182–185`'s ResolveSafe + handleEscapedPath path
  works exactly as the security-1 F1 fix intended (verified by
  observing the hook itself reject this finding's source file when
  I edited /tmp/path-trust-probe during the investigation — the
  rejection message reached stderr correctly).

## Open questions

- **Is the pre-edit handler intended to perform a path-trust check
  at all?** F1 reads the surface as "yes" because security-1 F1's
  rationale ("a crafted payload should not flow attacker paths
  into the filesystem") applies equally to read-side opens, not
  just writes. But it's arguable that pre-edit's job is purely
  about the *snippet content*, and the only reason it reads the
  target file is to harvest aliases — in which case rejecting
  out-of-root targets purely silently (no deny, just an empty
  alias map) might be the right behavior. Need a product call.

- **TOCTOU (F5) — is the threat model "local filesystem is
  trusted with the user"?** If yes, F5 is informational-only and
  should land as a comment in ResolveSafe. If no, the
  open-via-fd hardening is real work and probably belongs in a
  v1.x security pass.

- **NFC/NFD (F3) — does Leonard support non-macOS filesystems
  with unicode-normalizing semantics?** Windows NTFS also
  normalizes; Linux ext4 / btrfs preserve bytes verbatim. The fix
  is platform-independent (always normalize storeKeys to NFC) but
  the bug is only observable on APFS/HFS+/NTFS — worth confirming
  the test fixture covers macOS before signing off.

- **F9 / decision-side validation** — does the project want the
  MCP record_decision tool to actively reject out-of-root
  related_files, or simply not promise that they're filesystem
  references? The latter is a docs change and zero-risk.

- **Out of scope for this investigation:** I did not probe the
  Windows pathname behavior of ResolveSafe (different separator,
  drive-letter semantics, `\\?\` extended-length prefix), so F7
  remains theoretical. The probe driver ran only on darwin.
  Suggest a CI matrix entry for windows-latest to cover the
  separator semantics.
