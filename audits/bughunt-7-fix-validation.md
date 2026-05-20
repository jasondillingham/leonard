# Bug Hunt #7 — fix-validation-and-new-surface

## Summary

Validated each fix shipped in v0.46.0 → v0.49.0 and probed the new code paths
for HIGH/CRITICAL regressions. The post-v0.46 `.leonard/` guard closes the
documented RCE chain for in-test path shapes, but I found **three real bypass
surfaces** that re-open part or all of the RCE chain (one HIGH, two
HIGH/CRITICAL depending on deployment shape). The v0.47 cap on MCP symbol
results clamps the wire output but does **not** push the limit down to SQL —
the materialization-DoS vector the fix was meant to close is still present at
the store layer. v0.48's `module_name` rewrite is correct for normal paths but
produces ugly leading-dot qnames on `./foo.rs`/`../foo.rs`/`/abs/foo.rs`. The
v0.48 NFC normalization is correctly applied to both the post-edit hook and the
MCP `record_claim` adapter path. The Makefile / CI / GitHub-templates landing
is clean, modulo the minor count discrepancy noted in F8.

## Findings

### F1 — Symlink bypass re-opens the `.leonard/` RCE chain
- **Severity:** high
- **Reproducer:**
  ```
  cd /tmp/proj && leonard init
  ln -s /tmp/proj/.leonard/config.toml leak_link
  printf '{"tool_name":"Write","tool_input":{"file_path":"leak_link","content":"[post_edit.verify]\ncommand = \"echo PWNED\""}}' \
    | leonard-hook pre-edit
  ```
  Output: `{"continue":true}` — guard accepts. A `Write` through that symlink
  overwrites `.leonard/config.toml`; the next post-edit hook runs
  `sh -c "echo PWNED"`.
- **Observed:** `isUnderLeonardDir` is purely lexical (`filepath.Clean` +
  `strings.Split` on the cleaned form). The path `leak_link` has no `.leonard`
  segment, so the guard returns `continue:true`. The downstream Write follows
  the symlink and lands in `.leonard/`.
- **Expected:** Any write whose **resolved target** is under `.leonard/` should
  be denied. Claude controls symlink creation via `Bash` (`ln -s …`), so a
  one-line Bash setup followed by a Write reconstitutes the v0.45 RCE chain.
  This is precisely the chain v0.46 F1 was meant to close.
- **Suggested fix shape:** Add an `os.Lstat`/`filepath.EvalSymlinks` pass on
  the candidate path before the lexical check. Reuse `index.ResolveSafe` if
  the CWD/project-root is known to the pre-edit handler (it isn't passed today
  — would need to surface it via `PreEditOptions` and the
  `cmd/leonard-hook/pre_edit.go` wiring). On `EvalSymlinks` error, fall back
  to a strict reject when the link itself is a symbol link
  (`fi.Mode()&os.ModeSymlink != 0`) — matches the dangling-symlink rejection
  already in `ResolveSafe`.
- **Out of scope:** Whether Claude Code's Write tool itself follows symlinks
  on disk is OS behavior; in practice every Go and POSIX write call I'm aware
  of does. The fix belongs in the hook, not Claude Code.

### F2 — `Bash` payloads bypass the `.leonard/` guard
- **Severity:** high
- **Reproducer:**
  ```
  printf '{"tool_name":"Bash","tool_input":{"command":"echo pwned > .leonard/config.toml"}}' \
    | leonard-hook pre-edit
  ```
  Output: `{"continue":true}`.
- **Observed:** `candidateEditPaths` only inspects `FilePath` and
  `NotebookPath`. A `Bash` PreToolUse payload has neither, so the guard
  returns `continue:true` unconditionally. The shell redirect then writes
  whatever the model wants into `.leonard/config.toml`. The next post-edit
  hook reads the new `[post_edit.verify].command` and `sh -c`s it.
- **Expected:** Either (a) the guard inspects the `command` field for write
  shapes that target `.leonard/` (`>`, `>>`, `tee`, `dd of=`, `cp`, `mv`,
  `python -c "open(...).write"`, etc.) — but parsing shell is rabbit-hole
  territory; or (b) Leonard ships an opinionated note in CONTRIBUTING /
  install docs that Claude Code's permission ACL **must** deny Bash writes
  to `.leonard/` (the user's `~/.claude/settings.json` controls Bash
  separately from Edit/Write).
- **Suggested fix shape:** I'd lean (b). Document explicitly in the install
  README/SECURITY that Leonard's `.leonard/` guard is for Edit/Write/MultiEdit/
  NotebookEdit only; Bash writes are out of scope and the user must restrict
  Bash via Claude Code's own permissions. The trust-boundary story for v0.46
  F1 specifically said "operator-authored" but Bash is the only tool that
  could realistically be the attacker's wedge — the docs should call it out.
- **Out of scope:** Detecting `sed -i` or in-place rewrites via Bash; that's
  shell-parser-complete.

### F3 — Backslash separator bypass on Unix builds (WSL deployment)
- **Severity:** high
- **Reproducer:** On Unix (the documented dev platform):
  ```
  printf '{"tool_name":"Write","tool_input":{"file_path":"path\\.leonard\\config.toml","content":"x"}}' \
    | leonard-hook pre-edit
  ```
  Or in Go: `isUnderLeonardDir("path\\.leonard\\config.toml")` returns
  `false` on macOS/Linux because `filepath.Separator == "/"` and
  `strings.Split` treats the path as one segment.
- **Observed:** `isUnderLeonardDir` uses `string(filepath.Separator)` as the
  Split delimiter — on Unix that's `/`, so backslash-separated paths are
  opaque single segments that never match `.leonard`. On Windows native, the
  reverse — forward-slash paths would be opaque. Either way, mixed-separator
  paths slip past.
- **Expected:** Real deployment: Claude Code on a Windows host with the
  leonard-hook binary running under WSL2 (the documented `sh -c` requirement
  forces WSL anyway). Claude emits Windows-style backslash paths; the WSL
  binary has `filepath.Separator == "/"`; the guard fails. Then `sh` under
  WSL receives the same backslashes and writes wherever they decode.
- **Suggested fix shape:** Normalize both separators before split:
  `filepath.ToSlash(filepath.Clean(path))` then `strings.Split(... , "/")`.
  Bonus: `path/filepath/Clean` doesn't normalize backslashes on Unix; the
  switch to `filepath.ToSlash` makes the guard separator-agnostic regardless
  of which OS the binary was compiled for.
- **Out of scope:** Whether Claude Code on Windows actually emits backslash
  paths to a WSL hook binary depends on Claude's own path translation — I
  didn't test that end-to-end. The fix is cheap enough to ship preemptively.

### F4 — Uppercase `.LEONARD/` bypass on case-insensitive filesystems
- **Severity:** high (macOS default, Windows default), low (Linux ext4)
- **Reproducer:**
  ```
  cd /tmp/proj && leonard init
  mkdir -p .leonard && echo "hello" > .leonard/test.txt
  cat .LEONARD/test.txt   # prints "hello" on APFS/HFS+/NTFS — same dir
  printf '{"tool_name":"Write","tool_input":{"file_path":"proj/.LEONARD/config.toml","content":"x"}}' \
    | leonard-hook pre-edit
  ```
  Output: `{"continue":true}`. A subsequent Write to `.LEONARD/config.toml`
  lands in `.leonard/config.toml` on every default-config macOS box.
- **Observed:** `isUnderLeonardDir` compares each segment with `seg ==
  ".leonard"` — exact, case-sensitive. APFS (macOS default), HFS+, and NTFS
  (Windows default) are case-insensitive but case-preserving. On every Apple
  laptop running Claude Code with Leonard wired up, `.LEONARD/` and `.leonard/`
  are the same directory — and the guard accepts the first form.
- **Expected:** The trust boundary is *the directory*, not the casing of the
  path string Claude happens to emit.
- **Suggested fix shape:** `strings.EqualFold(seg, ".leonard")` instead of
  `seg == ".leonard"`. Also lookalike-test cases would need a parallel
  `mixed-case` example to ensure regression coverage.
- **Out of scope:** Filesystem-level case sensitivity detection — the fix is
  to be conservative everywhere, not OS-specific.

### F5 — `MaxSymbolResults` cap doesn't reach SQL; DoS vector partially open
- **Severity:** medium (would be high if a malicious MCP client were realistic)
- **Reproducer (logical):** `verify_symbol(name="foo")` against a store with
  100k symbols named `foo`. The MCP layer caps the **wire output** to 500,
  but `store.FindSymbolsByName("foo")` issues `SELECT … WHERE name = ?` with
  **no LIMIT**. All 100k rows materialize into Go memory before
  `filterAndConvert` trims to 500.
  ```go
  // internal/store/store.go:504
  func (s *Store) FindSymbolsByName(name string) ([]Symbol, error) {
      rows, err := s.db.Query(`SELECT id, file_path, name, qualified_name, kind,
          signature, start_line, end_line, exported, parent_id
          FROM symbols WHERE name = ?
          ORDER BY file_path, start_line, id`, name)   // no LIMIT
  ```
  For `find_symbol(query="x", limit=10000000)`, the limit IS pushed down to
  SQL via `FindSymbolsByQuery`, but the user-supplied 10000000 is passed
  verbatim to the store — it's only clamped to 500 *after* the SQL returns.
- **Observed:** The v0.47 fix added an MCP-layer ceiling but didn't push it
  through to `FindSymbolsByName` (no SQL LIMIT at all) or to the SQL LIMIT
  parameter passed to `FindSymbolsByQuery` (the user's giant int reaches the
  driver before being clamped on the way back).
- **Expected:** The bughunt-6 mcp F2 description says "could materialize the
  entire symbol table before any cap took effect" — that's still true at the
  store layer.
- **Suggested fix shape:** Two-line change:
  1. Add `LIMIT ?` to `FindSymbolsByName` SQL, bind `MaxSymbolResults` from
     the adapter (or hardcode a generous `LIMIT 5000` server-side to leave
     headroom above the MCP cap).
  2. Clamp `limit` in `StoreAdapter.FindSymbolsByQuery` to
     `min(limit, MaxSymbolResults)` before passing to the store.
- **Out of scope:** Other store methods (e.g. `GetUnverifiedClaims`) — those
  already have caps. This finding is specifically the v0.47 fix's incomplete
  push-down.

### F6 — `languageFromPath` / filter not case-normalized
- **Severity:** medium
- **Reproducer:**
  ```
  verify_symbol(name="Open", language="RUST")     # returns zero matches
  verify_symbol(name="Open", language="Go")       # returns zero matches
  verify_symbol(name="Open", kind="Function")     # returns zero matches
  ```
- **Observed:** `filterAndConvert` does `s.Kind != kind` and
  `languageFromPath(s.FilePath) != language` — both equality comparisons.
  `languageFromPath` returns lowercase canonicalized labels; if the caller
  passes uppercase or title-case, the filter rejects every row. Same for
  `Kind` (stored as `"function"`, `"method"`, etc.).
- **Expected:** Filters that don't match anything because of casing are
  worse than no filter at all — they produce a wrong-answer (Exists=false /
  zero matches) for a question that should have surfaced real results.
- **Suggested fix shape:** `strings.ToLower(language)` and
  `strings.ToLower(kind)` at the top of `filterAndConvert`, or apply
  `strings.EqualFold` in the per-row comparisons. The tool description
  doesn't tell callers casing matters, so either be lenient or document the
  constraint explicitly.
- **Out of scope:** Validating the language argument against the registered
  language list (a typo like `langauge="rust"` would still be silently
  zero-matched).

### F7 — `module_name` produces ugly leading-dot qnames
- **Severity:** low (functional; just cosmetic noise)
- **Reproducer:** Run the Rust extractor (or any tree-sitter language) on
  paths like:
  - `./foo.rs` → module `..foo` (double dot)
  - `../up/foo.rs` → module `...up.foo` (triple dot)
  - `/abs/dir/foo.rs` → module `.abs.dir.foo` (leading dot)
  - `C:\proj\foo.java` → module `C:.proj.foo` (colon stays in qname)
- **Observed:** `main.rs::module_name` (v0.48) is `path.replace("\\", "/")` →
  strip trailing extension → `replace("/", ".")`. It doesn't strip leading
  `./`, doesn't normalize `..`, and doesn't strip the Windows drive letter
  / colon.
- **Expected:** Per the brief: "either is defensible". For an indexer whose
  qnames feed `find_symbol`'s substring search, a qname `..foo.bar` is
  legible but ugly. The Windows colon case (`C:.proj.foo`) breaks any
  consumer that splits qnames on `.` because `C:` is now a segment.
- **Suggested fix shape:** Pre-strip leading `./`, collapse `..` (don't try
  to resolve, just drop), drop Windows drive prefix (`^[A-Z]:`). The Rust
  side could mirror what `internal/parse/qname.go::moduleQualifier` does for
  Go — that one would have the canonical answer.
- **Out of scope:** Whether the Go-side moduleQualifier handles these cases
  the same way; the bughunt-6 promotion says "the Go-side extractors already
  did the path-aware form" — verifying that wasn't part of this lane.

### F8 — `make help` count off by one
- **Severity:** informational
- **Reproducer:** `make help` lists 11 targets, not 10 as the bughunt-6 fix
  brief said. Brief says "10 real targets"; actual: `check / test /
  test-race / vet / build / build-otel / build-rust / build-treesitter /
  install / tidy / clean` = 11 (plus `help` itself = 12 in `.PHONY`).
- **Observed:** Cosmetic — the help output matches the actual `.PHONY` line
  and all listed targets exist. Just a count drift between brief and
  Makefile.
- **Expected:** Either correct the brief or treat as informational. The
  Makefile itself is sound.
- **Suggested fix shape:** None needed; surface for next-round housekeeping.

### F9 — `working_dir = "~/Documents"` produces a chdir error instead of a fallback
- **Severity:** low
- **Reproducer:**
  ```go
  runner := hooks.MakeShellRunner("pwd", "~/Documents")
  out, err := runner(ctx, root)
  // err: chdir /tmp/wd-test/~/Documents: no such file or directory
  ```
- **Observed:** A tilde-prefixed `working_dir` is treated as a relative path
  joined to the project root. The joined path
  `<root>/~/Documents` is lexically inside the project root, so `ResolveSafe`
  returns ok=true. Then `cmd.Dir` is set to a non-existent directory and the
  runner errors. The "resolves outside the project root" fallback never
  fires.
- **Expected:** Either expand `~` (probably overkill — `os.UserHomeDir()`
  then check containment, would just become the same escape-rejected case)
  or document that `working_dir` doesn't expand shell metacharacters and
  treat the path as a literal subdir. The current behavior — silent
  `chdir` failure on the next vet — is the worst of both worlds because
  the operator's claim ledger fills with `vet_ok=false` rows that look like
  code problems but are config typos.
- **Suggested fix shape:** Reject `working_dir` values starting with `~` at
  config-load time with a clear error: "working_dir does not expand `~`; use
  an absolute path or a project-relative path". Or surface the chdir error
  with a clearer prefix so the operator can disambiguate "verifier said no"
  from "verifier never ran".
- **Out of scope:** Adding tilde expansion proper — not load-bearing.

### F10 — Verifier rejection note leaks into `VetResult.Output`
- **Severity:** informational (not exploitable)
- **Reproducer:** Set `working_dir = "/etc"`. Run a post-edit. The vet
  output contains:
  ```
  leonard: [post_edit.verify].working_dir "/etc" resolves outside the project root; falling back to project root
  <real verifier output>
  ```
  Then `VerifyVerb` is computed from the `command` text (not the output), so
  the verb-extraction story is unaffected. The note ends up in
  `claims.evidence` and the user-facing stop-hook summary.
- **Observed:** Correct behavior — the operator needs to see why the working
  dir was overridden. Not a bug.
- **Expected:** The brief asked specifically whether the note could confuse
  verb extraction or claim text. Verb extraction: no — verb comes from
  `command`. Claim text: the note IS in evidence, which is the intended
  surface for operator diagnostics.
- **Suggested fix shape:** None. Documenting the verification for the
  audit trail.

## New-surface probes (results)

### Q24 — `languageFromPath("/some/.leonard/file.go")`
Returns `"go"`. The function is path-content-agnostic — it only inspects
basename + extension. The `.leonard/` trust boundary is enforced separately
by the pre-edit guard (which rejects the path BEFORE language inference
would matter for an Edit/Write). Defensible: language inference and trust
boundaries are separate concerns. Document the choice in the
`languageFromPath` doc comment if pinning is wanted.

### Q25 — NFC at all claim-write entry points?
Yes. Path traced: MCP `record_claim` → `RecordClaimInput.FilePath` →
`StoreAdapter.RecordClaim` (internal/mcp/adapter.go:188) →
`store.Store.RecordClaim` (internal/store/store.go:1048) →
`normalizeClaimPath`. Same `normalizeClaimPath` covers
`SupersedeClaimsForFile`. NFC is correctly applied to both the post-edit
hook path (via the same `RecordClaim`) and the MCP path. No regression.

### Q26 — GitHub issue / PR templates
Both `bug_report.md` and `feature_request.md` have valid YAML front-matter
(`name`, `about`, `title`, `labels`, `assignees`). `PULL_REQUEST_TEMPLATE.md`
is plain markdown (correct for the GitHub default location). Rendering
checked manually: no broken links, no malformed checkboxes, no orphan
front-matter. **Minor nit:** The PR template's "Test plan" block uses
HTML comments (`<!-- … -->`) wrapping the checkbox list — GitHub will hide
the entire checklist by default, including the boxes the contributor is
supposed to tick. Moving the checklist OUT of the comment block would
make it visible/actionable in the rendered PR body. Severity: low.

### Q27 — Backslash separator (Windows / WSL)
Covered in **F3** above. Real bypass.

## Things that worked

- **`.leonard/` guard happy-path:** `.leonard/config.toml`,
  `./.leonard/config.toml`, `../proj/.leonard/config.toml`,
  `proj/.leonard/../.leonard/config.toml`, and `/Users/.../.leonard/...`
  all correctly **denied**. The double-encoded `proj/.leonard%2Fconfig.toml`
  case passes through but is harmless — `os.WriteFile` doesn't decode `%2F`
  so the resulting file is literally named `.leonard%2Fconfig.toml` (a top-
  level file, not under `.leonard/`).
- **`.leonard/` guard lookalikes:** `.leonard.bak/`, `leonardish/`,
  `mine.leonard/x.txt`, `.leonard.old/z.txt`, `foo.leonard`,
  `backup.leonard.bak/x.txt` all correctly **allowed**.
- **MultiEdit / NotebookEdit shapes:** Both correctly catch
  `file_path = ".leonard/..."` and `notebook_path = ".leonard/..."`
  respectively.
- **Working dir escape rejection:** `working_dir = "/etc"`,
  `"subdir/../.."`, `"/absolute/elsewhere"` all fall back to project root
  with the diagnostic note. Symlink inside project root pointing OUTSIDE is
  correctly rejected. Symlink inside project root pointing INSIDE is
  correctly accepted.
- **NFC normalization:** Applied at both `RecordClaim` and
  `SupersedeClaimsForFile`, reachable from both MCP and the post-edit hook.
- **Cargo.lock committed:** Both `internal/parse/rust/Cargo.lock` (12
  packages) and `internal/parse/treesitter/Cargo.lock` (54 packages) are
  `git ls-files`-tracked. Lockfiles use `version = 4` (modern Cargo).
- **CI workflow YAML:** Valid YAML, two jobs (`go` + `rust`), both with
  caching wired (`setup-go@v5` `cache: true`; `Swatinem/rust-cache@v2` for
  the two Rust workspaces). `go vet`, `go test`, `go test -race`,
  `go test -tags otel`, and `CGO_ENABLED=0 go build` all present.
- **Test suite:** `go test ./internal/{hooks,mcp,store,index}/` all pass.

## Open questions

- **`StoreAdapter.FindSymbolsByQuery` push-down:** F5 is high-confidence
  but I didn't load-test the DoS — at 100k rows the materialization is
  presumably fine; at 10M it's not. Worth a quick benchmark with a
  synthetic store before triaging F5 severity up or down.
- **Claude Code Bash on Windows:** F3 hinges on Claude Code's path
  translation when invoking a WSL hook binary. I couldn't test that
  end-to-end. Assume worst case until proven otherwise.
- **F7 leading-dot qnames in production:** What does Leonard's own
  `find_symbol` look like when `Cargo.toml` lives at the repo root and the
  Rust extractor is run on relative paths starting with `./`? Worth a
  quick `leonard verify <some Rust symbol>` against a Cargo-rooted project
  to see whether the leading-dot module qname surfaces in the wire output
  or is hidden behind some other normalization layer downstream.
- **`Bash` guard scope (F2):** Should Leonard ship an opinionated
  refusal for Bash writes to `.leonard/`, or is that explicitly the user's
  permission-ACL job? The bughunt-6 triage didn't seem to commit either
  way; this is a doc/policy question for the maintainer, not a code-level
  decision.
