# Bughunt-12 — L8 (path-trust, macOS / Darwin / APFS edges) findings

**Lane:** L8 — path-trust guard, Darwin-specific gotchas
**Started:** 2026-05-27
**Baseline:** Leonard d716b02 (+ post-d716b02 working tree, `git rev-parse --short HEAD` reports `4dc9023` at probe time); binaries `leonard 0.52.0`, `leonard-mcp 0.53.0`, `leonard-hook 0.52.0`; macOS / Darwin 25.2.0 arm64 / APFS (case-insensitive-but-preserving).

**Lane runlog:** `runlog/run-2026-05-27-L8-path-trust.md`
**Lane script:** `harness/lanes/L8-path-trust.sh`

Severity scale (matches `~/Documents/Homelab/leonard/audits/`):
- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — path traversal escaping project root, DoS crashing hook process, secret leakage, trust bypass under known attack classes
- **MEDIUM** — resource exhaustion within bounds, error-swallowing masking problems, weak input validation, store-key duplication that breaks ground-truth correctness
- **LOW** — quality, races without practical exploit paths, structural leakage / log noise

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F031 | MEDIUM | L8 path-trust | APFS case-only path variants insert N store rows for one on-disk file — `verify_symbol` reports N matches for a symbol that exists in 1 file (Darwin/APFS-only; bughunt-4 F3 "OOS" extension) | confirmed with discriminating test |
| F032 | MEDIUM | L8 path-trust | Project-dir prefix case mismatch (e.g. `FIXTURES/...` vs `fixtures/...`) creates duplicate store rows + dual `verify_symbol` matches on case-insensitive volumes (sibling of F031, different attack surface — the `cwd`-relative join produces the case-mismatch path) | confirmed |
| F033 | LOW | L8 path-trust | Embedded NUL / newline / CR bytes in `file_path` still pass `ResolveSafe` (bughunt-4 F6 still open). No exploit primitive in v0.52 — `filepath.Clean` doesn't peel `..` past root in observed cases — but the noisy bytes propagate verbatim into systemMessage / additionalContext / would land in claim rows | confirmed |
| F003-recheck | LOW | L8 path-trust | `leonard index` reports `indexed 1000 file(s)` even with 1536 files in DB (re-confirmation of F003 on a slightly higher fixture corpus; **not a new finding**, noted to avoid filing it twice) | already filed as F003 |

**Things-that-worked (no findings, closure verifications):**
- Bughunt-4 F1: pre-edit's silent-allow on path-escape policy is intact. For non-`.go` paths (`pre-edit Write /etc/passwd` → `{"continue":true}`), the `.go`-suffix gate at `pre_edit.go:216` returns `allowResponse()` *before* the ResolveSafe check at `:225` even runs. For `.go`-suffixed but path-escaping inputs (e.g. `/etc/passwd.go`, `fixtures/symlinks/escapes_root.go` whose symlink target leaves root), the response is also `{"continue":true}` — verified by code-path inspection (`if !ok { return allowResponse(), nil }` at pre_edit.go:225-228 returns before `readFileImports` is called), not by direct strace observation. The post-edit symmetry probe (which DOES reject the symlink with "path escapes project root") corroborates that ResolveSafe answers `ok=false` for the same payload.
- Bughunt-4 F2: dangling-symlink rejection at indexer.go:560-565 still rejects (`post-edit Write fixtures/symlinks/escapes_root.go` → "path escapes project root").
- Bughunt-4 F3 NFC/NFD: `storeKey` at indexer.go:700-702 normalizes to NFC. Two `post-edit` calls with the same path in NFC bytes vs NFD bytes → **1 store row, 1 symbol row**. F3 NFC half is closed.
- `/tmp` vs `/private/tmp` realpath drift on Darwin: both forms rejected as outside-root by ResolveSafe (lexical + secondary EvalSymlinks check correctly handle both).
- `..` traversal (`../foo.go`, `../../../etc/passwd.go`, absolute with embedded `..`): all rejected post-Clean.
- Bughunt-11 F1 (trust token at `$XDG_CONFIG_HOME`, not `.leonard/`): planted forged token at the old `.leonard/pending-trivial/<hash>.json` location — `consumeTrivialToken` does NOT consult it; remains on disk after hook fires.
- Bughunt-11 F2 (symlink-token refusal): planted symlink at the canonical `$XDG_CONFIG_HOME/leonard/pending-trivial/<hash>.json` → refused, not consumed, attack file untouched, symlink untouched (per the "operator visibility over silent cleanup" docstring at trivial.go:60-64).
- MCP-side `list_files pattern=../../../etc/*` / `/etc/*` / `fixtures/../../../etc/*`: all return `{"files":[]}` — store's SQLite GLOB matches against NFC-normalized in-store path strings only, and no such strings start with `/etc` or contain `../`. No information leak.
- Empty/whitespace `file_path`: pre-edit returns silent allow; post-edit rejects at decode boundary (`leonard dispatcher: code post-edit error: hooks: payload decode error: tool_input.file_path missing from PostToolUse payload`) — slightly leaky error message but no security impact.
- Very long paths (500/1000/2000 char dirname): pass ResolveSafe (lexical containment OK), fail later at `os.Stat` with clean `file name too long` and a `re-index ... failed` system message. No crash, no escape.

---

## F031 — APFS case-only path variants → N store rows for one file (MEDIUM, Darwin/APFS-only)

**Files (root cause):**
- `internal/index/indexer.go:526-589` — `ResolveSafe` returns the caller-supplied lexical form; no case-normalization.
- `internal/index/indexer.go:691-702` — `storeKey` `norm.NFC.String(filepath.ToSlash(rel))` normalizes unicode (NFC) and slashes, but does **not** lowercase. On a case-insensitive volume, `Camel.go` and `CAMEL.GO` produce different `storeKey` values for the same APFS inode.
- `internal/store/store.go` — `UpsertFile` keys on `path` exactly; each distinct storeKey gets its own row.

**Bughunt-4 F3 OOS note (the predecessor that flagged this):**
> "Out of scope for this investigation: Case-only collisions (`README.md` vs `readme.md` on case-insensitive APFS volumes). Same family of bug; treat in the same fix."

The same-fix-as-NFC closure didn't happen — NFC landed (F3 NFC half closed) but the case half didn't.

**Discriminating reproducer (this lane, 2026-05-27 22:04 ET):**

```bash
# One on-disk file
mkdir -p fixtures/L8_scratch/case
cat > fixtures/L8_scratch/case/Camel.go <<'EOF'
package casecheck
func L8CaseCamel() {}
EOF

# Fire post-edit three times with three case variants
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/Camel.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/camel.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/CAMEL.GO

# Inode + content check — all three resolve to the same APFS inode + same content hash
stat -f '%i  %N' fixtures/L8_scratch/case/{Camel.go,camel.go,CAMEL.GO}
# 230172698  fixtures/L8_scratch/case/Camel.go
# 230172698  fixtures/L8_scratch/case/camel.go
# 230172698  fixtures/L8_scratch/case/CAMEL.GO

# DB has 3 file rows + 3 symbol rows
sqlite3 .leonard/leonard.db \
  "select path, hash from files where path like 'fixtures/L8_scratch/case/%';"
# fixtures/L8_scratch/case/Camel.go|f57eab37...
# fixtures/L8_scratch/case/camel.go|f57eab37...
# fixtures/L8_scratch/case/CAMEL.GO|f57eab37...

sqlite3 .leonard/leonard.db \
  "select name, file_path from symbols where name = 'L8CaseCamel';"
# L8CaseCamel|fixtures/L8_scratch/case/Camel.go
# L8CaseCamel|fixtures/L8_scratch/case/camel.go
# L8CaseCamel|fixtures/L8_scratch/case/CAMEL.GO
```

**Observed via the MCP surface (the consumer-visible failure mode):**

```bash
python3 harness/mcp.py call verify_symbol '{"name":"L8PrefixCase"}'
# exists=true, matches=[
#   {file:"FIXTURES/L8_scratch/case/PrefixCase.go", line:2, ...},
#   {file:"fixtures/L8_scratch/case/PrefixCase.go", line:2, ...}
# ]
```

The model is told the symbol exists in **two** files (with different paths!) when it exists in one file on disk. Same file, same line, same signature — the row count is the only delta.

**Why this is MEDIUM:**
- Leonard's whole value proposition is "ground truth — verify_symbol won't tell the model something exists when it doesn't." This bug doesn't fabricate a symbol, but it inflates its location count, which is the exact failure mode the README enumerates: *"hallucinated file paths — verify_symbol pinpoints where a symbol lives."* Pinpoint now returns N points for one location.
- Repeatable trivially. Any project on macOS where two tools (or two model turns, or the model itself running in different sessions) reference the same file with slightly different case will accumulate duplicate rows. Examples that produce divergence today: `pkg/Foo.go` vs `pkg/foo.go`, `README.md` vs `readme.md`, `Tests/` vs `tests/`. None of these are exotic.
- Compounds with F003/F020: each duplicate row is also a potential orphan post-rename. If on-disk `Camel.go` is removed but the only reference the next session uses is `camel.go`, the row keyed by `camel.go` would be checked-and-pruned correctly — but **so would `Camel.go` and `CAMEL.GO`**, because `os.Stat` follows APFS case-insensitive resolution and all three forms `Stat()` successfully **even after the rm** until the canonical-case file is the one removed. The asymmetry between "indexer stores case-preserving" and "OS resolves case-insensitively" is the bug root.
- Test confirmation: after `rm fixtures/L8_scratch/case/Camel.go`, all three rows DID get pruned by `leonard index` (because the canonical form was removed, all variants now `Stat → ENOENT`). So this specific failure path closes itself. But the inflated-match-count failure during normal operation persists.

**Severity rationale (MEDIUM not HIGH):**
- No path escape: all 3 rows resolve to the same in-root file.
- No fabrication-into-thin-air: the symbol does exist.
- The harm is duplicate-result inflation in `verify_symbol` / `find_symbol`, which violates Leonard's contract softer than F020 but still meaningfully (the model sees 2-3 "files" containing the same single thing).
- Darwin/APFS-only — Linux ext4 keeps the bytes verbatim so the three case variants would name three different files there (no overlap, no bug). Fix is platform-conditional: only `runtime.GOOS == "darwin"` (and Windows NTFS) need case normalization.

**Fix shape:**
- Add an `IsCaseInsensitiveFS(root string) bool` helper that probes the root once at indexer construction (touch a file, stat its uppercase, compare inode). Cache the result on the Indexer.
- In `storeKey`, when case-insensitive: `strings.ToLower(rel)` after the NFC normalization. The store rows become canonical lower-case; reads from `verify_symbol` / `find_symbol` lower-case the input similarly before lookup.
- Alternative (less invasive but more expensive): keep storeKey case-preserving but add a `UNIQUE(LOWER(path))` constraint via SQLite `COLLATE NOCASE` on the `files.path` column; `UpsertFile` then deduplicates at insertion. Trade-off: the persisted "canonical" path string becomes whichever case happened to land first, which may surprise operators inspecting the DB.
- Document: Leonard chooses lower-case canonical on case-insensitive filesystems. (The same docstring at indexer.go:691-702 that explained NFC choice should extend to case.)
- Add a regression test: write file `Foo.go`, post-edit three case variants, assert 1 file row + 1 symbol row.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 7 (intentional extension of bughunt-4 F3 OOS).

---

## F032 — Project-dir prefix case mismatch creates duplicate rows (MEDIUM, Darwin/APFS-only)

**Files (root cause):**
- `internal/index/indexer.go:526-589` `ResolveSafe` — the path-trust check passes for any case combination of the prefix (because APFS resolves case-insensitively at the OS layer, so the secondary `EvalSymlinks` cross-check succeeds for both `FIXTURES/...` and `fixtures/...`).
- `internal/index/indexer.go:691-702` `storeKey` — preserves whatever case the caller passed.

**Reproducer (this lane):**

```bash
# Real project root has lowercase "fixtures/"
mkdir -p fixtures/L8_scratch/case
cat > fixtures/L8_scratch/case/PrefixCase.go <<'EOF'
package casecheck
func L8PrefixCase() {}
EOF

# Hook fires twice — once with caller-supplied uppercase FIXTURES, once with the
# correct lowercase fixtures. On APFS both resolve to the same dir.
python3 harness/hook.py post-edit Write FIXTURES/L8_scratch/case/PrefixCase.go
python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/PrefixCase.go

# Two rows. Two verify_symbol matches.
sqlite3 .leonard/leonard.db "select path from files where path like '%PrefixCase%';"
# FIXTURES/L8_scratch/case/PrefixCase.go
# fixtures/L8_scratch/case/PrefixCase.go
```

The threat surface is subtly different from F031: F031 is the **basename** case varying (`Camel.go` vs `camel.go`); F032 is the **directory prefix** case varying (`FIXTURES/...` vs `fixtures/...`). The two paths concatenate with the project root via `filepath.Join`, and the contained-check at `ResolveSafe` indexer.go:543-549 uses `filepath.Rel` which is case-sensitive in its lexical-containment branch — but `EvalSymlinks` (called at the secondary check, line 553-561) succeeds for both forms because APFS handles case insensitively at the OS layer. So both pass.

**Why this is MEDIUM:**
- Same shape as F031 (duplicate rows), different attack surface. A Claude Code session that the model runs from a path which differs in case (e.g. `cd $WORK/Fixtures` after the operator set up the project under `fixtures/`) emits hook payloads with the case-shifted prefix; every subsequent edit writes a duplicate row.
- The model has direct control over the form via `cwd` and the `file_path` payload field. So a confused step (model invents an uppercase-fixtures path in a follow-up) inflates the DB silently.
- Could be filed as part of F031's fix — the same "always lowercase storeKey on case-insensitive FS" closure covers both. Filing it separately because the failure mode is also testable independently (the basename-case test from F031 wouldn't surface the prefix-case bug if storeKey lowercased only the basename).

**Fix shape:** Same as F031. The single-fix closure: `runtime.GOOS == "darwin"` (and `windows`) → lowercase the **entire** rel-from-root storeKey, not just the basename. Same regression test should cover both cases — assert (a) basename-case-variant writes produce 1 row, (b) prefix-case-variant writes produce 1 row.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 8.

---

## F033 — Embedded NUL / newline / CR in `file_path` still pass ResolveSafe (LOW)

**Files:**
- `internal/index/indexer.go:526-589` `ResolveSafe` — no `strings.ContainsAny(claimed, "\x00\n\r")` guard.
- `internal/hooks/post_edit.go:188` `strings.TrimSpace(payload.ToolInput.FilePath)` — trims outer space only; internal whitespace and control bytes survive.

**Bughunt-4 F6 referenced:**
> "Reject any claimed path containing a NUL or newline byte. These can't appear in any legitimate Unix or Windows pathname."
> "Suggested fix shape: At the top of ResolveSafe... `if strings.ContainsAny(claimed, "\x00\n") { return "", false }`."

**Closure verification:** the suggested fix was not landed. Current `ResolveSafe` accepts these bytes.

**Reproducer (this lane, three control-byte variants):**

```python
# All three pass ResolveSafe; OS rejects at stat
fp_cases = [
    "fixtures/L8_scratch/nul\x00../../../etc/passwd",
    "fixtures/\n../../etc/passwd",
    "fixtures/\r../../etc/passwd",
]
# For each: post-edit returns:
#   "leonard: <abs path with embedded byte> file not found, skipping re-index"
# meaning ResolveSafe returned ok=true. The byte propagates into stdout/stderr
# verbatim.
```

For the **NUL case** specifically: input `fixtures/L8_scratch/nul\x00../../../etc/passwd` cleans to `<project>/fixtures/etc/passwd` (the `nul\x00..` is one path segment with NUL in the middle, then three `..` peel back to project + `fixtures` + `etc/passwd`). The resolved abs is **inside** the project root, so no actual escape. ResolveSafe returns ok=true. The system message:

```
leonard: /Users/jasondillingham/Documents/Homelab/projectdogwalker/fixtures/etc/passwd file not found, skipping re-index
```

The NUL never reaches the OS stat because Clean has consumed the segment containing it — but only because the path happens to lexically clean to an in-root location. A different NUL placement that didn't end up cleaned away would propagate to `os.Stat`, which returns "invalid argument" rather than "no such file" (since POSIX paths can't have NULs).

For the **newline/CR cases**: the byte survives Clean (Clean treats newline/CR as ordinary characters inside a single path segment). Output systemMessage and additionalContext carry the raw byte (` ` / `\n` / `\r` in JSON, depending on the encoder).

**Why this is LOW (not MEDIUM):**
- No exploit primitive on v0.52 — the resolved path always lands either (a) inside root (so no escape) or (b) outside root (so caught by the lexical containment check before bytes matter). The bytes themselves can't be used to smuggle past the guard in either direction.
- Operator-visible noise only: the bytes propagate into Claude-Code-rendered systemMessage strings, into JSON-RPC `additionalContext` fields, and (if a real file with `\r` in its name happened to exist on disk and get matched) would land in DB `path` columns where downstream tools handle them awkwardly.
- A future change to `filepath.Clean` semantics, or to how `os.Stat` resolves on a future Go version / future Darwin filesystem, could turn this latent class active. A 4-character defense (`ContainsAny`) closes the class for free.

**Fix shape:**
- At the top of `ResolveSafe` (indexer.go:528, after the empty guard):
  ```go
  if strings.ContainsAny(claimed, "\x00\n\r") {
      return "", false
  }
  ```
- Same treatment on `root` for symmetry.
- Extend the existing `TestResolveSafe_*` table-test in `internal/index/indexer_test.go` with the three control-byte cases — explicit `ok=false` expected.

**Discovered.** 2026-05-27 — bughunt-12 L8 probe 10. Re-verifies bughunt-4 F6 still applies post-v0.52.

---

## Note on the 2 runlog FAILs

The lane runlog reports 71 PASS / 2 FAIL. **Both FAILs are harness bugs, not Leonard defects** — verified independently:

- `DB symbol L8CaseCamel locations` — the helper `db_symbols_for_name` queried `select file_path, line from symbols`, but the symbols table doesn't have a `line` column under that name (the actual column is something else). Helper bug only. The follow-up direct sqlite query (`select name, file_path from symbols where name = 'L8CaseCamel'`) succeeded and returned the 3 expected rows — that's what's in the F031 reproducer.
- `post-edit with vertical tab` — Python's `json` module raised `bad json: Invalid control character at: line 1 column 124 (char 123)` before any bytes reached Leonard. Test never executed the hook. The newline / CR / NUL equivalents (which Python's JSON encoder *does* accept) are tested in the same section and all returned the same b4-F6-class behavior; vertical tab would follow the same path.

## Open observations (not filed)

- **`leonard index` reports `indexed 1000 file(s)` when DB has 1536** — this is **F003 re-confirmed**, not new. Logging here so the next path-trust audit doesn't waste cycles relitigating.
- **Post-edit emits `leonard dispatcher: code post-edit error: hooks: payload decode error: tool_input.file_path missing from PostToolUse payload` on empty file_path** — leaks Go-error formatting to stderr (similar shape to F006/F012). Operator-visible only; not security. File under "error-message hygiene" if a sweep ever covers that.
- **Very-long paths (500/1000/2000 char dirname components)** return cleanly with `file name too long` system message. Worth noting Leonard doesn't itself impose a path-length cap before passing to `os.Stat` — depends entirely on OS rejection. A future Linux config with much higher PATH_MAX could change blast-radius, but no defect today.
- **The lane script wrote out-of-root probes** (e.g. `post-edit /tmp/foo.go`, `post-edit /etc/passwd`) and **none** caused side effects on disk — Leonard never reaches the file write/read because ResolveSafe rejects before any I/O. Positive confirmation the integration is sealed.

---

## Round-status delta after L8

L8 contributes **3 new findings**: F031 + F032 (MEDIUM each, both Darwin/APFS-only, both extensions of the bughunt-4 F3 closure that landed NFC but not case-insensitive); F033 (LOW, bughunt-4 F6 closure verification — still open, no exploit primitive). The pre-edit "silent-allow on path-escape" policy is correct and intentional (per bughunt-4 F1's documented rationale). All other bughunt-4 path-trust closures hold (F1 silent-allow, F2 dangling-symlink reject, F3-NFC, F4 prune-with-ResolveSafe). Bughunt-11 F1/F2 trust-token closures hold.

Total L8 work: 73 sub-tests across 14 sections + 1 hand-verification of the symlink-token cleanup behavior + 1 hand-verification of the NUL-vs-Clean interaction. 3 findings (2 MEDIUM, 1 LOW). All Darwin-specific; fix is platform-conditional.
