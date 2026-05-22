# Bughunt-11 + security-5 — findings

**Status:** investigation complete; all HIGH + MEDIUM fixes landed
**Audit branch:** `audit/bughunt-11-security-5`
**Started:** 2026-05-22

## Severity scale

- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — path traversal escaping project root, DoS crashing the
  hook process, secret leakage to logs, trust bypass under known
  attack classes
- **MEDIUM** — resource exhaustion within bounds, error-swallowing
  that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths,
  structural error-message leakage

## Rollup

| # | Severity | Theme | Summary |
|---|---|---|---|
| F1 | HIGH | Self-logging / filter override | Bypass tokens live under `.leonard/` — vulnerable to the bash-obfuscation class that bughunt-9 closed for verifier trust → **closed in `b862b0a`** |
| F2 | HIGH | Self-logging / filter override | Bypass token read follows symlinks — same shape as bughunt-9 F2 → **closed in `b862b0a`** |
| F3 | HIGH | Sync plugins | Plugin command has no trust gate — same shape as the pre-bughunt-9 verifier-trust gap → **closed in `51bc166`** |
| F4 | MEDIUM | Sync plugins | Plugin stdout/stderr buffers unbounded — DoS → **closed in `8b091c0`** |
| F5 | MEDIUM | Sync plugins | Cross-plugin facts cross-pollution → **closed in pending commit** |
| F6 | MEDIUM | MCP — verify_claim | Input text unbounded → **closed in `8b091c0`** |
| F7 | LOW | github sync | No iteration cap on `oss_contributions` count |
| F8 | LOW | github sync | HTTP response body decoded without size limit |
| F9 | LOW | Sync CLI | Plugin command path printed without escape filtering |
| F10 | LOW | MCP — get_truth_history | `Limit` field uncapped |
| F11 | LOW | Self-logging / filter override | `os.Remove` errors silently ignored on token consume |
| F12 | LOW | MCP — list_facts | Returned map is a direct reference to adapter state |

## HIGH findings

### F1 — Bypass tokens live under `.leonard/` (HIGH)

**Files:**
- `internal/adapters/selflog/trivial.go:42-49`
- `internal/adapters/groundtruth/override.go:44`
- `cmd/leonard/truth_edit.go` (token writer)
- `cmd/leonard/override.go` (token writer)

**Attack:** An attacker who can trigger the bash-obfuscation class on
the operator's session can plant a token at:

```
.leonard/pending-trivial/<sha256(rel)>[:32].json
```

containing valid JSON with the target `file_path`, an attacker-
chosen `trivial_reason`, and a recent timestamp. The next PreEdit
to that file consumes the token, bypasses require-tier
self-logging enforcement, and logs the bypass with the attacker's
reason as "operator-blessed."

Same shape for `pending-override/` (filter rule bypass).

**Why this is HIGH:** The bughunt-2 `.leonard/` guard is the only
defense, and bughunt-9 explicitly relocated the verifier trust
file OUT of `.leonard/` because the guard's residual gaps make it
an unsafe trust container. The bypass-token mechanism added in
v0.7 (#26) and v0.8 (#17) didn't follow the bughunt-9 precedent.

**Fix:** Relocate tokens to
`$XDG_CONFIG_HOME/leonard/pending-{trivial,override}/<project-hash>/<rel-hash>.json`
matching the trust-file pattern. The XDG dir is per-user, not
under `.leonard/`, so the bash-obfuscation attack class can't
reach it from a session-scoped Claude tool.

### F2 — Bypass token read follows symlinks (HIGH)

**Files:**
- `internal/adapters/selflog/trivial.go:51`
- `internal/adapters/groundtruth/override.go:46`

**Attack:** `os.ReadFile(tokenPath)` follows symlinks. An attacker
who can plant a symlink at the token path can redirect token
reads to arbitrary attacker-controlled files (e.g., a stale token
they previously wrote to a tmp dir).

Same shape as bughunt-9 F2 for the verifier trust file. Fixed
there by `os.Lstat` first + refuse symlinks.

**Why this is HIGH:** Compound with F1; even with F1 fixed (tokens
moved to XDG), if an attacker can write to XDG (multi-user
machine), they can plant a symlink there too. Defense-in-depth
needs the symlink refusal.

**Fix:** Add `os.Lstat` check before read; refuse if symlink.

### F3 — Sync plugin command has no trust gate (HIGH)

**Files:**
- `cmd/leonard/sync.go:200` (executes from config)
- `internal/adapters/groundtruth/sync/plugin.go:89` (`exec.CommandContext`)

**Attack:** Same shape as the pre-bughunt-9 verifier-trust gap.
The plugin command path comes from `.leonard/config.toml`. An
attacker who plants a malicious command path there (via the bash-
obfuscation class) gets RCE when the operator next runs
`leonard sync`.

The operator types `leonard sync` voluntarily but doesn't see
which binary is about to run unless they re-read config.toml each
time. Operator habituation makes this a real risk.

**Why this is HIGH:** This is the exact attack pre-bughunt-9
closed for the verifier command. Sync plugins reintroduce it for
a new code path.

**Fix:** Apply the same trust-gate model as
`[post_edit.verify].command`. Add
`leonard config trust sync <name>` that fingerprints the plugin
command. The plugin runner refuses to exec unless the fingerprint
matches.

## MEDIUM findings

### F4 — Sync plugin stdout/stderr buffers unbounded (MEDIUM)

**File:** `internal/adapters/groundtruth/sync/plugin.go:91-93`

A buggy or hostile plugin can write gigabytes to stdout/stderr and
OOM the `leonard sync` process.

**Fix:** Wrap `bytes.Buffer` with `io.LimitReader` at 16 MiB. Beyond
that, treat as failure ("plugin output exceeded 16 MiB cap").

### F5 — Cross-plugin facts pollution (MEDIUM)

**File:** `cmd/leonard/sync.go:81-93`

The CLI loads `facts.yaml` once before iterating plugins. After
plugin A writes its updates, plugin B still receives the pre-A
facts as input. Plugin B's output overwrites plugin A's changes
when its `writeFactsAtomically` runs.

**Fix:** Re-read `facts.yaml` between plugins. Or run plugins in
parallel and reject if they touch overlapping paths.

### F6 — `verify_claim` MCP tool unbounded text (MEDIUM)

**File:** `internal/adapters/groundtruth/mcp.go` (verifyClaim handler)

Claude can send a 100 MB string to `verify_claim` and trigger
expensive fuzzy matching against the rule set. The hook-layer cap
doesn't apply to direct MCP tool invocations.

**Fix:** Cap `in.Text` at 256 KiB at the MCP layer; return a clear
"text too large" error beyond that.

## LOW findings

Brief — fix at maintainer's discretion:

- **F7** `github.go:72` — cap loop at 500 entries per run
- **F8** `github.go:170` — wrap response body with `io.LimitReader(_, 1<<20)`
- **F9** `cmd/leonard/sync.go:200` — `strconv.Quote(pc.Command)` before
  printing
- **F10** `truth_history.go:50` — clamp `Limit` to 1000 at MCP layer
- **F11** `trivial.go`, `override.go` — `os.Chtimes` token mtime to
  past-TTL before Remove so a failed Remove still produces an
  expired token
- **F12** `mcp.go:listFacts` — deep-clone the returned map

## Out-of-scope items confirmed clean

- **Schema v8 migration** — additive column, parameterized SQL,
  JSON serialization round-trip tested. No injection surface.
- **Trust system extensions for adapters** —
  `WriteAdapterTrust`/`AdapterTrusted` mirror the v0.52 verifier-
  trust posture (XDG location, 0o700 dir, 0o600 file, symlink
  refusal, validAdapterName whitelist). Solid.
- **Hot-reload goroutine lifecycle** — channel-as-parameter
  pattern survives `Close` field-nilling correctly. RWMutex
  read/write boundaries clean.
- **Ground-truth file parsing** — yaml.v3 has no known
  deserialization gadgets; markdown parsing is purely textual.
  Pattern matchers use Go RE2 (no ReDoS).
- **Code adapter / hooks-layer shims** — unchanged from v0.52
  surface, covered by bughunts 1-10 + security-1-4.

## Closing criteria

All HIGH findings (F1, F2, F3) close before v1.0 tag. MEDIUM
findings (F4, F5, F6) close as separate commits — operator
quality bar. LOW findings flagged in this doc; close opportunistically.
