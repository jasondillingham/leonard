# Bug Hunt #10 — fix verification + security review (iteration 4, post v0.52.0)

> Lane: **verify-iteration + security pass combined**. Investigation-only — no code changes.
>
> Date: 2026-05-20
> Codebase: HEAD @ `3d0d1e1` (v0.52.0)
> Method: per-iteration-3-closure reproducers, then aggressive probe for new HIGH/CRIT findings.
> Scratch in `/tmp/bh10-*` (cleaned at end).

---

## Headline

**TERMINATE — zero HIGH/CRITICAL findings.**

All three iteration-3 findings are closed. The v0.52.0 redesign relocated the trust fingerprint file out of the project tree and gave `realBackend` a real `Close()` lifecycle. Both reproducers from iteration 3 (F1 trust-file poisoning via bash obfuscation, F2 trust-file symlink read) now fail cleanly with `falling back to defaults` / `refusing to follow`. The WAL file is zero-bytes after 700 sequential post-edit hooks (down from 15 MB at v0.51).

Aggressive probing surfaced no NEW HIGH or CRITICAL. The carry-over MED/LOW set (sec-3 F5 db perms, sec-3 F12 NotebookEdit decode, sec-3 F13 LEONARD_* relative paths, sec-3 F18 / sec-1 F5 additionalContext fencing) remains unchanged; none are blocking.

The fix-loop should stop here.

---

## Iteration-3 closure verification

| iteration-3 finding | Status at v0.52.0 | Evidence |
|---|---|---|
| **bughunt-9 F1** (CRIT) — trust-gate bypassable via `.leonard/`-write bash obfuscation | **CLOSED** | Reproducer plants `.leonard/trusted-verifier.sha256` at the OLD path + a malicious `[post_edit.verify].command`. Post-edit hook ignores the old path, emits `UNTRUSTED — falling back to defaults`. `/tmp/f1-pwned.txt` is never created. |
| **bughunt-9 F2** (CRIT) — trust file followed through symlinks | **CLOSED** | Reproducer plants symlink at `~/Library/Application Support/leonard/trust/<hash>.sha256 → /tmp/f2-attacker-hash`. Hook emits `trust check failed, falling back to defaults: trust: ... is a symlink; refusing to follow`. `/tmp/f2-pwned.txt` is never created. |
| **bughunt-9 F3** (HIGH) — WAL checkpoint at Close is dead code | **CLOSED** | 700 sequential post-edit hooks against a real DB leave `.leonard/leonard.db-wal` at **0 bytes** (file is removed entirely; SQLite's TRUNCATE checkpoint + closing the last connection cleans both wal and shm). DB grew 77K → 262K (normal indexing). |

Hook lifecycle confirmed by reading `cmd/leonard-hook/main.go:70-84`:

```
runRoot(ctx) →
  backend := newDefaultBackend()
  defer backend.Close()  // (line 72-76)
  root := newRootCmd(backend)
  root.SetContext(ctx)
  err := root.Execute()
  return ...
```

The defer fires on:
- successful subcommand (verified: F3 reproducer above)
- error return from `RunE` (verified: invalid JSON payload — WAL still cleared)
- Go panic in a `RunE` (verified by reading: defers unwind even on panic; the corrupt-DB panic in `realBackend.store()` does still run the `Close()` defer — though `b.s` is nil at that point so Close returns nil)

The defer does NOT fire on:
- SIGKILL — documented behavior (next-run autocheckpoint catches up)
- The `os.Exit(code)` in `main()` — not relevant because `runRoot` already returned before `main` reaches `os.Exit`, so the defer ran inside `runRoot`'s scope.

`realBackend.Close()` is safe to call when the store was never opened (nil guard at backend_real.go:43). `Indexer()` and `Claims()` share a single `*store.Store` via the `b.s == nil` lazy init at backend_real.go:55-66 — confirms the iteration-3 audit's "design smell where post-edit opens TWO sqlite connections per hook" is also fixed.

---

## Probes that came up empty (documented for the next iteration if one is needed)

### Trust-gate residuals

- **`os.UserConfigDir` cross-platform behavior** — on macOS returns `~/Library/Application Support` and ignores `$XDG_CONFIG_HOME` (verified empirically). On Linux returns `$XDG_CONFIG_HOME` or `$HOME/.config`. Per-user dir in all cases. No cross-user leak.
- **`XDG_CONFIG_HOME` redirection** — would require an attacker who can set env vars in the parent shell before `claude` launches, which is well outside the threat model ("attackers with root or shell access can already do anything", SECURITY.md). On macOS this var isn't honored anyway.
- **TOCTOU between `Lstat` and `ReadFile` in `ReadTrustedFingerprint`** — theoretically possible (regular file at Lstat → swap to symlink → ReadFile follows). Requires same-user concurrent code execution, out of threat model.
- **Trust file canonicalization** — `TrustFilePath` uses `filepath.Abs(projectRoot)` which does NOT resolve symlinks. Result: trust at `/path/proj` does NOT cover `/symlinked/proj` even when they point to the same directory. This is **intentional and correct** — an attacker who plants a symlink can't ride existing trust through it. Documented in `internal/config/trust.go:28-31`. Empirically verified: granting trust via `/tmp/bh10-canon` does NOT authorize the same project accessed via `/tmp/bh10-canon-link` (symlink) — hook emits UNTRUSTED.
- **Trust file write perms** — first `leonard config trust` from a clean state creates `~/Library/Application Support/leonard/trust/` with 0o700 and the fingerprint file with 0o600. Verified.
- **Empty `[post_edit.verify].command`** — `VerifyCommandTrusted` short-circuits to `(false, nil)` for empty/whitespace command. Hook falls back to default `go vet`. Verified.
- **Whitespace handling on the fingerprint side** — `ReadTrustedFingerprint` trims, `FingerprintCommand` trims input. Confirmed: trust file with leading/trailing whitespace + extra newlines still matches.
- **Case sensitivity** — fingerprint compare is exact-string (case-sensitive). Uppercase hex doesn't match lowercase hex. Verified — hook rejects the mismatch.
- **Trust file unreadable (perms 0)** — `ReadTrustedFingerprint` propagates the read error; hook treats as `trust check failed, falling back to defaults`. Fail-closed. Verified.
- **`config trust --help` text references the OLD path** (`.leonard/trusted-verifier.sha256`) in two doc strings. Cosmetic only; the code uses `TrustFilePath()`. See LOW finding L4 below.

### bashTouchesLeonardDir bypasses (carried from iteration 3)

The v0.50 lexical scanner still misses all the bughunt-8 obfuscation classes (`D=.leonard; ...`, `.l?onard/`, `.l''eonard/`, `tee`, command substitution). Empirically verified — pre-edit emits `{"continue":true}` for each.

**This is no longer exploitable for RCE** because:

1. To get sh -c execution the attacker needs BOTH a malicious `.leonard/config.toml` AND a matching trust fingerprint at `~/Library/Application Support/leonard/trust/<hash>.sha256` (or `~/.config/leonard/trust/<hash>.sha256` on Linux).
2. The v0.52 trust file lives OUTSIDE `.leonard/`. The bashTouchesLeonardDir scanner is not consulted at all when the destination path doesn't textually contain `.leonard`, so an attacker would need to write to `~/Library/...` via a Bash/Edit/Write tool call — and Claude Code's own permission model requires operator approval for paths outside the project root.
3. Per the SECURITY.md threat model: "Threats requiring Claude Code itself to be compromised — Leonard treats Claude Code as a trusted-enough caller (its commands are user-initiated)." A successful attack here requires the operator to approve two suspicious tool calls (one targeting `~/Library/...`, one targeting `.leonard/`), which is out of Leonard's scope.

I verified the chain works *if* both Bash calls are approved (chain reproduced in `/tmp/bh10-bashtrust` — wrote both files manually after the pre-edit hook said `{"continue":true}`, then post-edit executed the malicious command). But this is the documented "operator approved the unsafe action" path, not a defense gap.

### v0.52.0 new surfaces

- **`realBackend.store()` panics on bad DB** — corrupt `leonard.db` (header zeroed) → `panic: store: ping sqlite: file is not a database (26)` with full Go stack trace to stderr, exit code 2. The `defer backend.Close()` in `runRoot` runs (defers unwind on panic) but `b.s` is nil at that point so Close is a no-op. The panic propagates out of `runRoot` → `main()` → Go runtime exits 2 with stack trace. Telemetry shutdown defer DOES NOT run because the panic skips past `main()`'s `shutdown()` call. **MED-LOW** (operator hint is ugly but not an exploit) — see L1 below.
- **Race between pre-edit + post-edit running in parallel** — each spawns a fresh process with its own `realBackend`. SQLite WAL serializes writes. No shared state. Safe.
- **`Close()` error printed to stderr** — non-fatal, doesn't change exit code (main.go:74). Acceptable.
- **`b.mu` mutex on the lazy store init** — Indexer/Claims are called sequentially in a single hook RunE goroutine. The mutex is belt-and-suspenders. No deadlock surface.

### Carry-overs from sec-4 / sec-3 / sec-1

| Finding | Severity | Status at v0.52 | Notes |
|---|---|---|---|
| sec-3 F5 / sec-2 F5 / sec-4 F13 — `.leonard/leonard.db` mode 0o644 | MED | Still 0o644 | Multi-user box risk; out of threat model per SECURITY.md (single-user) |
| sec-4 F12 / sec-3 F12 — NotebookEdit post-edit decode | LOW | Carry | Not retested |
| sec-4 F19 / sec-3 F13 — `LEONARD_*` env vars accept relative paths | LOW | Carry | Operator-controlled env var; relative path is operator's choice |
| sec-4 F17 / sec-3 F5 / sec-1 F5 — additionalContext `vetErrorSummary` unfenced | MED | Carry | `internal/hooks/post_edit.go:374` still emits raw summary. Markdown-injection from verifier output. Not exploitable directly (just a prompt-injection vector into Claude's context), but cosmetic-promotion candidate. |
| sec-3 F6 — clone-to-RCE | **CLOSED** | Trust file outside project tree → a malicious clone can ship `.leonard/config.toml` but trust file at `~/...` is absent on the victim's machine. Verified: post-edit emits UNTRUSTED, command not executed. |

### MED/LOW probes worth logging (not blocking, not new HIGH/CRIT)

#### L1 (LOW) — corrupt `.leonard/leonard.db` causes raw panic with stack trace

- **Where:** `cmd/leonard-hook/backend_real.go:64` — `panic(err)` in the `realBackend.store()` lazy init when `store.Open` returns a non-nil error.
- **Reproducer:** `dd if=/dev/zero of=.leonard/leonard.db bs=1 count=64 conv=notrunc`. Next post-edit hook prints a Go panic + stack trace to stderr and exits 2.
- **Why LOW:** Operator sees a noisy crash but it's their corrupted DB. No RCE, no information disclosure beyond the panic message. The same panic shape was discussed in sec-4 F21.
- **Suggested fix shape:** in `realBackend.store()`, return an error instead of panicking; thread the error back through `Indexer`/`Claims` to a single op (`b.openOrErr`) that the cobra `RunE` can surface as a normal "post-edit: open store" return — same shape as `defaultPreEditOpener`.

#### L2 (LOW) — stale doc strings in `cmd/leonard/config.go`

- **Where:** `cmd/leonard/config.go:30` and `:44` — both reference `.leonard/trusted-verifier.sha256` as the trust file path. The actual file is at `$XDG_CONFIG_HOME/leonard/trust/<hash>.sha256` (resolved via `TrustFilePath`).
- **Why LOW:** Cosmetic. `leonard config trust --help` shows the stale path, but the long-description of the `config` parent command (the one displayed without `--help`) was updated; only the embedded comments and `Long:` for the `trust` subcommand are stale. Trust still writes to the right place.
- **Suggested fix shape:** update both doc strings to match the v0.52 path. Possibly emit the actual path from `TrustFilePath` at help time so the doc can't drift again.

#### L3 (LOW) — `internal/config/trust.go:13` package-doc still narrates the v0.51 `.leonard/trusted-verifier.sha256` location

- Same shape as L2 but in the trust package's file-level comment. Reads OK because the comment also explains the v0.52 relocation; the v0.51 path is described as the broken predecessor. Not misleading, but could trim.

#### L4 (LOW) — SECURITY.md still describes trust file as `.leonard/trusted-verifier.sha256`

- **Where:** `SECURITY.md` — the "Existing guards" table row for "`[post_edit.verify].command` requires operator trust" says `stores the SHA-256 fingerprint in .leonard/trusted-verifier.sha256 (gitignored, per-machine)`.
- **Why LOW:** Doc-only. Operators who go grep for the trust file will be confused. Functionality unaffected.

#### L5 (INFORMATIONAL) — bashTouchesLeonardDir's bypass surface is documented but not closed

- The pre-edit bash lexical scanner still has the bughunt-8 F1-F5 obfuscation gaps (`D=.leonard`, glob, empty-quote, command-substitution). At v0.52 these no longer chain to RCE because the trust file is out of `.leonard/`. **But**: the `.leonard/` write guard is now ONLY defense-in-depth (the trust gate is the real defense). If a future change relocates the trust file BACK into `.leonard/`, or introduces a new operator-authored file under `.leonard/` (e.g. a `.leonard/secrets.toml`), the bashTouchesLeonardDir bypasses become exploitable again.
- **Suggested fix shape:** either (a) replace bashTouchesLeonardDir with a real bash parser walk (mvdan/sh — sec-4 F1 suggestion), or (b) document explicitly in `bashTouchesLeonardDir` that the scanner is best-effort and any new operator-trust-bearing file MUST live outside `.leonard/`.

---

## Termination criterion

**TERMINATE — zero HIGH/CRITICAL findings remain.**

- F1 (CRIT, iter-3) — trust gate bypassable: **CLOSED** by trust file relocation.
- F2 (CRIT, iter-3) — trust file symlink: **CLOSED** by `os.Lstat` refusal.
- F3 (HIGH, iter-3) — WAL checkpoint dead code: **CLOSED** by `realBackend.Close()` + defer in `runRoot` + PASSIVE→TRUNCATE.

Five LOW findings carried (4 doc-stale variants + 1 corrupt-DB panic shape), one INFORMATIONAL on the residual bashTouchesLeonardDir bypasses (no longer exploitable but documented design fragility). None are blocking.

The user's termination condition (zero HIGH or CRITICAL) is met. The fix-loop can stop.

If a future iteration is needed for unrelated reasons, the cheapest cleanup pass is:
1. Update L2/L3/L4 doc strings (5-line patch) — eliminates operator confusion about where the trust file lives.
2. Convert `realBackend.store()` panic to an error return (L1, ~20 lines) — kills the only remaining crash-shape on a corrupt DB.
3. Either replace `bashTouchesLeonardDir` with `mvdan/sh` AST walk OR add a defensive comment that operator-trust files must never land under `.leonard/` (L5, design-doc-only).

None of these are necessary to close the bug-hunt fix-loop; they're hygiene for the next minor release.
