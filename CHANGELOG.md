# Changelog

All notable changes to Leonard, in reverse chronological order.

Cadence: each minor bump bundles one coherent change (a feature, a
bug-hunt theme fix, or a perf sweep) and ships with updated version
strings (`leonard --version`, `leonard-hook --version`,
`leonard-mcp --version`) + test coverage.

## v0.56.0 — doctor detects unsafe hook wiring; eval made runnable

**`leonard doctor` now inspects the Claude Code hook wiring** and reports a
class of failure that was previously invisible: Leonard's guards only run for
the tools the operator wired into the hook matcher, and nothing checked that
the wiring was sound.

- **SECURITY finding** when the `PreToolUse` matcher omits `Bash`. The pre-edit
  guard branches on `ToolName == "Bash"` to scan command strings for writes
  into the protected data directory (the bughunt-7 F2 fix). A matcher without
  `Bash` silently disables it — a HIGH reopened with no warning. An audit of
  one machine found this in 3 of 7 wired projects, including this repo itself.
- Warnings for a matcher that omits `MultiEdit`/`NotebookEdit` (edits bypass
  the guard) or that a `PostToolUse` matcher omits `MultiEdit` (edits skip
  re-indexing); for `mcpServers` present in `settings.local.json` (#98, which
  Claude Code rejects — taking the hooks down with it); and for a missing or
  unparseable configuration.
- Matcher evaluation mirrors Claude Code's own matcher function, verified
  against the shipping binary: an exact **case-sensitive** list (`|` or `,`
  separated) versus an unanchored regex, with every ambiguous case biased
  toward flagging rather than a false "covered".

**Eval (Track A) made runnable.** The Inspect fabrication scorer now resolves
`leonard-hook` via `$LEONARD_HOOK_BIN` → `PATH` → `$(go env GOPATH)/bin`
(handling multi-entry GOPATH), removing the PATH footgun that killed a correct
setup at scorer init. Added `evals/inspect/RESULTS.md` as the committed run
record — `logs/` is gitignored, so a run otherwise leaves no trace — with the
reporting rules that keep the number honest (exclude scorer errors, report
no-code-block separately, never quote the mean as a fabrication rate, always
record a git SHA and a failure taxonomy).

**Docs and hygiene.** MCP setup corrected to a project-root `.mcp.json` (#98).
The ground-truth adapter's machine-appended `audit-log.md` is no longer
tracked (it grew unbounded, one section per edit — the shape behind
incident-1) and is now gitignored alongside the other adapter runtime state.

## v0.55.0 — incident-1: session-start hook runaway (1 HIGH)

**Fixes the session-start CPU runaway observed live on 2026-07-27**
(full write-up: [`audits/incident-1-session-start-runaway.md`](audits/incident-1-session-start-runaway.md)).
A `leonard-hook session-start` invocation in a project with a
non-hidden `truth_dir` and a large machine-appended `audit-log.md`
spun at ~2 cores for hours as an orphan and ignored SIGTERM. Three
compounding defects, all closed:

- **Fuzzy-scan hot path** — `findFuzzyOccurrences` now rejects on
  word-boundary and F016 edge rules BEFORE running the Levenshtein DP
  (the left-edge check skips mid-word offsets without entering the
  window loop at all), and the DP rows are allocated once per call
  (`levenshteinBuf`) instead of twice per window. 100 KB haystack,
  one rule: 140 ms → 6.9 ms, 600k allocs → 5. Match semantics
  unchanged; all pre-existing fuzzy/boundary/F014/F016 tests pass
  unmodified.
- **Session-start scan scope + budget** — the ground-truth truth dir
  is excluded from the `.md` walk (scanning it is circular:
  `do-not-claim.md` matches its own rules, and `audit-log.md` grows
  without bound); files over 1 MiB are skipped with a stderr note;
  the scan observes ctx and a 5 s wall-clock budget between files and
  reports a partial-scan note when truncated. `truthDir` is resolved
  against the canonicalized project root (and symlink-resolved) so
  the exclusion holds on macOS `/var` vs `/private/var`.
- **Process lifetime guarantee** — `leonard-hook` now runs under a
  55 s wall-clock deadline (below Claude Code's 60 s default hook
  timeout) with a watchdog that force-exits (code 1, never the
  blocking 2) if a handler is still running 10 s after context
  cancellation. A hook can no longer become a SIGTERM-immune orphan:
  the old code trapped SIGTERM into a context that the scan loop
  never checked.

New regression tests: `TestSessionStart_ExcludesTruthDir`,
`TestSessionStart_CancelledContextReportsPartialScan`,
`TestSessionStart_SkipsOversizeFiles`,
`BenchmarkFindFuzzyOccurrences_LargeHaystack`.

## v0.54.0 — bughunt-12 sweep (3 HIGH + 32 MEDIUM/LOW)

**35 commits closing every addressable finding from the bughunt-12 audit**
across eight lanes: protocol fuzzing, index correctness, detector accuracy,
ground-truth adapter, path-trust, UX, dogfood, and CLI polish. Three HIGH
findings closed; 32 MEDIUM/LOW findings addressed.

### Bughunt-12 HIGH findings

**H1 — index prune sweep was silently capped at 1 000 files.** Internal
bookkeeping used the same `ListFiles` call as the MCP tool, which is
bounded by `listFilesDefaultLimit = 200` (rising to the 1 000 hard cap at
most). On projects with more than 1 000 indexed files, `pruneStaleFiles`
and `CountFiles` only saw the first 1 000 rows, so deleted or renamed files
beyond that position were never pruned and health reports undercounted. Fix:
two new store methods — `AllFiles` (unbounded, internal-only) and
`CountFiles` (scalar) — now back all internal bookkeeping. The public
`list_files` MCP tool keeps its paginated limits.

**H2 — numeric contradiction detection against facts.yaml.** The ground-truth
adapter's claim detector matched text patterns but had no awareness of the
numeric facts already stored. A session could assert "15 years of experience"
with `years_of_experience: 8` in facts.yaml and the detector would not flag
it. Fix: post-edit and pre-edit now cross-check extracted numeric claims
against the corresponding facts.yaml values. A claim whose number contradicts
a stored fact is flagged as a contradiction even when no forbidden-phrase
pattern fires.

**H3 — ground-truth bypass paths.** Two related bypasses closed together:

- **Bash command body** — the forbidden-claim pre-edit guard inspected
  `file_path` / `content` but not the `command` field on Bash tool calls.
  A `Write | tee claims-doc.md` pipeline or `sed -i` edit could embed a
  forbidden claim and bypass the guard entirely. Fix: new
  `bashCommandMutates()` heuristic identifies Bash commands that write
  output (`>`, `>>`, `sed -i`, `tee`); those commands are scanned the same
  way as file content.

- **Override token not wired** — `leonard override --once` placed a
  single-use token under `$XDG_CONFIG_HOME/leonard/pending-override/` but
  the token was never consumed by the forbidden-claim pre-edit path. The
  deny was still raised even when the operator had explicitly authorized
  the edit. Fix: `consumeOverrideToken()` called at the guard boundary;
  a matching token bypasses the deny and is deleted atomically.

### Detector accuracy

Three categories of false positives reduced, in descending order of noise:

- **Numeric near-misses**: patterns like `15-year-old` or `2x faster`
  no longer trigger experience/count claims. The fuzzy matcher now requires
  a word boundary on both sides of the numeric anchor.
- **Temporal false positives**: four-digit years (1990–2029) and plain
  duration units (`30 days`, `6 months`, `2 weeks`) are excluded from
  the "time at company" detector. Both categories were the top source of
  false positives in dogfooded fact files.
- **In-word extension**: fuzzy candidate extensions that attach to the
  middle of a word (e.g. `internal` triggering a fragment of `intern`)
  are rejected.

### Ground-truth adapter

- **Partial-load on malformed truth files**: a syntax error in any one of
  `facts.yaml`, `filters.yaml`, or `do-not-claim.md` no longer aborts the
  adapter's `Init`. The adapter loads with the files it can parse and emits
  a warning, matching the dispatcher's non-fatal-per-adapter contract.
- **SessionStart proactive summary**: the `session-start` hook now emits
  a brief claim-findings summary so the session begins with a view of any
  open unverified claims rather than discovering them mid-session.
- **Stop hook condensed**: the verbose per-file stop digest is replaced
  by a single-line summary. The full per-file breakdown is still in the
  `audit-log.md` for post-session review.
- **facts diff + facts impact**: new `leonard facts diff` shows pending
  changes to `facts.yaml` relative to HEAD; `leonard facts impact <key>`
  shows which claims and decisions reference a given fact key, so operators
  can estimate the blast radius before editing a fact.

### Path normalization and security

- **APFS / Darwin case-insensitive deduplication**: on Darwin (where APFS
  volumes are case-insensitive by default), path keys are now lowercased
  before storage. A rename from `Foo.go` to `foo.go` previously accumulated
  two rows pointing at the same on-disk file; the normalized key collapses
  them to one.
- **Control-byte rejection in ResolveSafe**: NUL (`\x00`), bare newline,
  and carriage return are now rejected at the `ResolveSafe` boundary before
  any filesystem operation. These bytes appear in NUL-injection-class attacks
  and are never valid in file paths Leonard processes.
- **`.gitignore` / `.leonardignore` respected in list-stale-claims walk**:
  the stale-claims directory walk now honors both ignore files, preventing
  noise from generated or vendored paths the operator has already excluded
  from the index.
- **UTF-8 BOM stripped**: Python source files with a leading BOM (`\xef\xbb\xbf`)
  are stripped before passing to `ast.parse`. A BOM caused a `SyntaxError`
  that silently dropped the entire file from the index.

### Protocol

- **JSON-RPC error frames for batch and concatenated inputs**: when the MCP
  stdin filter detects a JSON batch array (`[...]`) or multiple concatenated
  frames on one line, it now emits a well-formed
  `{"jsonrpc":"2.0","error":{...},"id":null}` response instead of silently
  dropping the frame. Clients and fuzz harnesses now see a structured error.

### MCP tools

- **verify_symbol suggestions on exact miss**: when no exact match is found,
  the response now includes up to 5 fuzzy candidates in a `suggestions` field,
  removing the need for a separate `find_symbol` round-trip to correct a
  misspelling.
- **safeLimit — overflow and precision clamping**: `find_symbol` and
  `list_files` `limit` fields now use a `safeLimit` type that unmarshals via
  `json.Number` instead of float64. Values like `9223372036854775807` were
  previously rounded by float64 arithmetic, making the error message show
  a different number than the caller sent. Values above `math.MaxInt32` are
  clamped silently; negative values become 0.
- **Query validation at MCP layer**: `find_symbol` and `verify_symbol` now
  reject empty queries and queries exceeding 4 096 bytes with a typed error
  before touching the store (previously a raw SQL error reached the caller).
- **get_claim tool**: new MCP tool to retrieve a single claim's full evidence
  by ID. Previously the only way to inspect evidence was `get_unverified_claims`,
  which pages through all open claims.
- **claims source field**: `get_unverified_claims` now returns a `source`
  field (`"auto"` / `"operator"`) so callers can distinguish hook-recorded
  auto-claims from operator-recorded ones.
- **superseded_by on decisions**: `get_decisions` now surfaces the
  `superseded_by` reference on replaced decisions, matching the information
  visible in `leonard decisions list`.

### CLI additions and fixes

- **`leonard claims purge --older-than <duration>`**: removes superseded
  auto-claims beyond a given age. Useful for keeping the ledger trim on
  active projects without manually reviewing every resolved entry.
- **`list-stale-claims --scope` supports `**` recursive glob**: a scope
  like `--scope 'src/**/*.go'` now correctly recurses into subdirectories.
  Previously only single-level `*` patterns worked.
- **Line numbers in `leonard check` output**: the check command and
  list-stale-claims now include line numbers alongside file paths for
  every finding.
- **--limit flag help text corrected**: the `decisions list` and
  `decisions stale` flags previously showed misleading defaults. Both now
  accurately reflect the actual defaults applied by the store.
- **find_symbol limit=0 semantics**: `limit=0` now returns all results up
  to the store cap rather than defaulting to the default page size. The prior
  behavior (0 = "use default") was undocumented and inconsistent with the
  MCP schema description.

### Adapter system

- **Auto-detection always loads code adapter**: when both `go.mod` and
  `.leonard/ground-truth/` are present, auto-detection previously loaded
  only `ground-truth`. The code adapter now always loads as well, matching
  the documented intent that both adapters are active by default on a Go
  project with a truth tree.
- **`leonard init --adapter=ground-truth` writes `[[adapters]]` blocks**:
  previously init scaffolded the ground-truth directory tree but left
  `config.toml` adapter-free, requiring a manual edit. The generated config
  now contains the correct blocks. TOML parse errors surface a user-friendly
  message instead of a raw library error.

### Test coverage

- 11 new tests for detector false-positive reduction (years, durations,
  numeric near-miss, in-word extension)
- 9 new tests for numeric contradiction detection (edge cases: matching
  value, no numeric fact, formatted numbers, contradicting value)
- 8 new tests for ground-truth partial-load on malformed files
- 6 new tests for `bashCommandMutates()` and override-token wiring
- 5 new tests for APFS path deduplication (case variants, NFC+NFD combos)
- 4 new tests for control-byte rejection in `ResolveSafe`
- 4 new tests for JSON-RPC batch/concatenated error frames
- 3 new tests for `verify_symbol` suggestions on exact miss
- Tests for `claims purge`, `facts diff`/`impact`, `list-stale-claims
  --scope **`, `get_claim`, `safeLimit` clamping

All ~310 prior tests still pass.

---

## v0.53.0 — adapter dispatcher (#46) + bughunt-11 closeout

**The wiring that makes v1.0 actually work.** The v0.6 → v1.0 work
(merged on 2026-05-22) built the adapter framework but left the
load-bearing dispatcher unimplemented — `leonard-hook` and
`leonard-mcp` still called `internal/hooks` and `internal/mcp`
directly, so the ground-truth and self-logging adapters never
actually fired in real Claude Code sessions. v0.53 closes that gap
plus the three HIGH bughunt-11 findings.

### Adapter dispatcher (#46)

- `[[adapters]]` config schema in `.leonard/config.toml`:

  ```toml
  [[adapters]]
  type = "code"

  [[adapters]]
  type = "ground-truth"
  truth_dir = "source-of-truth/"
  ```

  Per-adapter fields (e.g., `truth_dir`) inside the block flow
  through to `Adapter.Init` via `adapters.Config.Raw`.

- Auto-detection when no `[[adapters]]` block is present:
  - `code` enables on `go.mod` at root OR `[post_edit.verify]` set
  - `ground-truth` enables when `.leonard/ground-truth/` exists
  - At least `code` always loads as the fallback (degraded mode
    when there's no DB)

- `internal/dispatcher.LoadEnabled` instantiates + Inits each
  enabled adapter and returns a `Loaded` with a `Close` cleanup
  func. Per-adapter Init failures are non-fatal (logged + skipped)
  so a malformed truth tree doesn't take down the whole hook
  pipeline.

- `leonard-hook` rewired: pre-edit / post-edit / session-start /
  stop subcommands all route through `dispatcher.HandleX` which
  parses the Claude Code envelope, dispatches to every loaded
  adapter, aggregates results, and writes the response. The code
  adapter still wraps `internal/hooks.HandleX` internally so the
  v0.52 fabrication-guard / verifier / decisions-injection logic
  is preserved verbatim.

- `leonard-mcp` rewired: bare MCP server, each adapter calls its
  own `RegisterTools(srv)`. Ground-truth adapter's MCP tools
  (`verify_claim`, `list_facts`, `get_story`, `get_truth_history`)
  are now actually exposed to Claude Code clients.

### Bughunt-11 + security-5

Three HIGH findings closed from the v0.6→v1.0 audit:

- **F1** — bypass tokens (`--trivial`, `--once`) relocated from
  `.leonard/pending-{trivial,override}/` to
  `$XDG_CONFIG_HOME/leonard/pending-{trivial,override}/`. Same
  reasoning as bughunt-9 moving the verifier trust file out of
  `.leonard/`: the bash-obfuscation attack class can plant forged
  tokens under `.leonard/`.

- **F2** — token reads `os.Lstat` first and refuse symlinks
  (defense-in-depth matching bughunt-9 F2).

- **F3** — sync plugin trust gate: new
  `leonard config trust sync <name>` fingerprints the plugin
  command at `$XDG_CONFIG_HOME/leonard/trust/<hash>.sync-<name>.sha256`.
  The runner refuses to exec until the fingerprint matches.

Three MEDIUM findings also closed: F4 (16 MiB cap on sync plugin
stdout/stderr), F5 (re-read facts.yaml between plugins to prevent
cross-pollution), F6 (256 KiB cap on `verify_claim` input).

### Test coverage

- 8 new tests for `config.EnabledAdapters` (resolution rules)
- 5 new tests for `dispatcher.LoadEnabled` (auto-detect, explicit
  config, close-idempotent, unknown-adapter handling)
- 2 new dispatcher tests for `HandlePreEdit` (ground-truth deny,
  happy-path Continue)
- 1 new dispatcher smoke test for multi-adapter `RegisterTools`
- 4 thin smoke tests in `cmd/leonard-hook/` for the cobra wiring
- 9 new tests for the bughunt-11 fixes (sync plugin trust round-
  trip, symlink refusal, F4 stdout cap, F5 plugin sequencing, F6
  oversize input)

All ~270 prior tests still pass.

### Backwards compatibility

- Projects on v0.52 keep working without touching config. Auto-
  detection enables only `code` for them.
- The v0.52 MCP tool surface (`verify_symbol` et al.) is
  unchanged.
- `leonard config trust` keeps its v0.52 verifier-only form
  (`leonard config trust` with no target = verifier); new
  subtargets `ground-truth` and `sync <name>` extend it.

### What's still pending for v1.0

The full v1.0 tag still wants:
- A round of dogfooding on the wired-up adapter system
- Bughunt-12 against the integrated dispatcher surface (this
  release only audited the unwired adapters)
- Operator-facing docs on the `[[adapters]]` config schema

## v1.0.0 — generalized ground-truth toolkit

**The big shift:** Leonard generalized from "code ground-truth" to
**pluggable ground-truth**. The v0.52 behavior is now one adapter
(`code`) among several. A new ground-truth adapter verifies prose
claims against a per-project truth tree, with full hook
plumbing, MCP tools, CLI surfaces, and a sync-plugin system.

A self-logging discipline ties every truth-changing edit to a
rationale entry in the decision log, so over time `leonard
truth-story` renders the project's build narrative.

**Zero regression for v0.52 projects.** The code adapter is
auto-enabled when `go.mod` is present at root or `[post_edit.verify]`
is configured. Existing trust files, decision logs, and claim
ledgers continue to work identically.

### Highlights

#### Pluggable adapter system
- `internal/adapters/Adapter` interface (`Init` / `Close` /
  `PreEdit` / `PostEdit` / `SessionStart` / `Stop` / `RegisterTools`)
- Adapter registry + factory pattern. Three adapters ship:
  `code`, `ground-truth`, `self-logging`.
- Decision aggregation: deny-beats-pass for PreEdit, additive for
  the other three hooks.

#### Ground-truth adapter
- Five-file schema under `.leonard/ground-truth/`:
  `facts.yaml`, `stories.md`, `do-not-claim.md`, `filters.yaml`,
  `audit-log.md`.
- Heuristic claim detector (regex patterns, 5 categories) with
  optional LLM fallback (Ollama-style endpoint) in hybrid mode.
- Pre-edit hard-deny on forbidden claims when
  `leonard config trust ground-truth` is granted.
- Path filters block forbidden Write paths;
  content filters require disclosures.
- Hot reload (polling-based, 2s interval) — saved truth files
  take effect within seconds.
- Post-edit writes structured findings to both JSON
  `pending-audit.log` and markdown `audit-log.md`.
- Stop hook emits per-session digest via SystemMessage.

#### Self-logging adapter
- Tiered policy (require / warn / skip) per file path.
- Auto-draft entries in `.leonard/pending-decisions.log` on every
  truth-file edit.
- Single-use bypass tokens (5-minute TTL) via
  `leonard truth-edit --trivial "reason" <path>` and
  `leonard override --once --reason "..." <path>`.
- Per-adapter trust marker at
  `$XDG_CONFIG_HOME/leonard/trust/<hash>.<adapter>.trust`.

#### CLI additions
- `leonard init --adapter=code,ground-truth` — scaffold both
  adapter trees
- `leonard config trust [verifier|ground-truth]` — gate blocking
  adapters
- `leonard check <file>` — run all adapters against a file (read-only)
- `leonard ground-truth lint` / `stats` — validate + report on the
  truth tree
- `leonard truth-edit --trivial` — single-use require-tier bypass
- `leonard override --once` — single-use filter-rule bypass
- `leonard truth-history <file>` — chronological per-file
  decision-log narrative
- `leonard truth-story` — full chronological narrative
  (`--scope`, `--since`, `--format=markdown|json|plain`,
  `--include-trivial`)
- `leonard sync` / `sync list` / `sync <plugin>` — drive sync
  plugins
- `leonard sync github` (via shipped `leonard-sync-github` binary)
  — refresh `facts.oss_contributions` against GitHub API

#### MCP tools (5 new)
- `verify_claim(text)` — heuristic + LLM claim detection
- `list_facts(category?, include_private?)` — facts.yaml
  navigation with sensitivity filtering
- `get_story(name)` — canonical phrasings; errors on unknown
- `get_truth_history(file_path)` — decision-log entries that
  touched a file
- `record_decision` extended with optional `truth_change` block

#### Schema migration
- v7 → v8: nullable `truth_change` TEXT column on `decisions`.
  Backwards-compat: existing entries load with `TruthChange == nil`.

#### Sync plugin system
- JSON-stdin/JSON-stdout protocol
  ([`docs/sync-plugins.md`](docs/sync-plugins.md))
- Built-in `leonard-sync-github` binary (refreshes OSS PR
  statuses via GitHub API; honors `GH_TOKEN`)
- Atomic facts.yaml writes (temp file + rename); per-plugin
  failures non-fatal

### Breaking changes

**None.** v0.52 projects upgrade without touching config. The
ground-truth and self-logging adapters are opt-in via
`leonard init --adapter=...` or `[[adapters]]` in `config.toml`.

The `leonard verify <symbol>` CLI continues to work for symbol
lookup. The new file-verification CLI is `leonard check <file>`
to avoid clashing.

### Documentation

- [`docs/ROADMAP-v1-ground-truth.md`](docs/ROADMAP-v1-ground-truth.md)
  — the full design rationale, phasing, and self-logging
  amendment
- [`docs/self-logging.md`](docs/self-logging.md) — discipline
  reference
- [`docs/adapters/`](docs/adapters/) — per-adapter reference
- [`docs/schema/`](docs/schema/) — per-file schema docs
- [`docs/cookbook/`](docs/cookbook/) — worked patterns
- [`docs/sync-plugins.md`](docs/sync-plugins.md) — plugin protocol
- [`examples/ground-truth/`](examples/ground-truth/) — three
  worked example projects (SaaS, compliance, personal)

### Test coverage

~250 new tests across the v0.6 → v1.0 work. Every prior v0.52
test still passes. `go test ./...` green across 16 packages.

### Acknowledgments

The five-file schema design and self-logging amendment were
shaped through dogfooding on this very repo across the v0.6 →
v1.0 development arc. Every PR in the v0.6/v0.7/v0.8/v0.9/v1.0
stacks was committed under the discipline it documents.

---

## v0.52.0 — trust file relocation + hook Close lifecycle (2 CRIT + 1 HIGH)

Iteration 3 of the fix-loop found that v0.51's trust-gate was
sound in concept but lived at the wrong path. The fingerprint
file at `.leonard/trusted-verifier.sha256` could be poisoned by
the same bash-obfuscation class that bypassed the v0.50 lexical
scanner. Move it OUT of the project tree to break this circle.

CRITICAL — bughunt-9 F1: an attacker who can write
`.leonard/config.toml` (via Bash variable indirection, command
substitution, etc.) can also write
`.leonard/trusted-verifier.sha256` with a fingerprint matching
their malicious command. End-to-end RCE re-reproduced.

Fix: trust file now lives at
`$XDG_CONFIG_HOME/leonard/trust/<sha256-of-project-root>.sha256`
(default `~/.config/leonard/trust/<hash>.sha256`). The .leonard/
bash-scanner gaps no longer matter — the attacker can't write to
the user's config dir from a Claude session-scoped tool.

CRITICAL — bughunt-9 F2: v0.51 `ReadTrustedFingerprint` used
`os.ReadFile` (follows symlinks). An attacker who plants a
symlink at the trust path could redirect reads to an arbitrary
attacker-controlled file.

Fix: `os.Lstat` first, refuse `fs.ModeSymlink` entries. Same
guard on the write path so we don't overwrite a planted symlink.

HIGH — bughunt-9 F3: `Store.Close()` had the right
`PRAGMA wal_checkpoint(PASSIVE)` since v0.51, but
`cmd/leonard-hook` never called it. The store was opened lazily
by `Indexer()`/`Claims()` and leaked at process exit. Measured
15 MB WAL after 700 hooks (worse than the v0.50.2 baseline the
fix was supposed to beat).

Fix: `realBackend` now caches a single store across
Indexer/Claims and exposes `Close()`. Wired into `runRoot` via
defer. Switched checkpoint from PASSIVE to TRUNCATE so the WAL
file actually shrinks to zero bytes after checkpoint.

Regression tests:
- TestTrustFileLivesOutsideProjectTree
- TestReadTrustedFingerprint_RefusesSymlink
- TestWriteTrustedFingerprint_RefusesOverwriteSymlink
- TestVerifyCommandTrusted_RoundTrip

Migration: v0.51 trust files at `.leonard/trusted-verifier.sha256`
are no longer used. They're harmless (post-edit hook ignores
them) but can be deleted. Operators must re-run
`leonard config trust` to write the new-location file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.51.0 — trust-gate for [post_edit.verify].command (5 CRIT + 3 HIGH)

Iteration 2 of the fix-loop established that enumerating Bash
obfuscation forms (backslash escapes, command substitution,
parameter expansion, glob, base64-decode, etc.) is futile — the
attack surface is unbounded. v0.51 moves the trust boundary to
the right place: the operator authorizes a command by SHA-256
fingerprint, and the post-edit hook refuses to run sh -c without
a fingerprint match.

CRITICAL closed (5) — bash-command lexical-scanner bypasses:
- backslash escapes (`.L\E\O\N\A\R\D/`)
- empty quotes (`.l''eonard/`)
- command substitution (`$(echo .l)$(echo eonard)/`)
- parameter expansion (`.leon${u:-ard}/`)
- ANSI-C escapes (`$'\x2eleonard'/`)
All defeated by the v0.50 textual scan; all closed by the
v0.51 trust gate (the command isn't authorized so it never
reaches sh -c, regardless of obfuscation).

HIGH closed:
- sec-4 F1 (bash-obfuscation → RCE): same class as the 5 CRIT.
- sec-4 F2: O(n²) walk-up in isUnderLeonardDirResolved bounded
  at 4096 ancestors.
- sec-4 F15 PROMOTED: `leonard init` refuses when `.leonard` is
  a pre-existing symlink. Closes the symlink-farm RCE class.
- bughunt-8 F6: WAL grew monotonically because
  wal_autocheckpoint is per-connection and each hook process
  opens a fresh one. Added explicit `wal_checkpoint(PASSIVE)`
  in Store.Close().
- sec-4 F3: store.ListFiles now LIMIT MaxSymbolQueryRows.

Trust gate UX:
- `leonard config trust` reads the command from `.leonard/config.toml`,
  shows it, asks "type yes to confirm". Stores
  `.leonard/trusted-verifier.sha256` (gitignored).
- `--yes` flag skips the prompt for scripted setup.
- Re-run after editing the command to re-authorize.
- Post-edit hook surfaces a clear stderr message when the
  configured command isn't trusted, then falls back to the
  default go-vet path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.50.2 — PROMOTED carry-overs (otel F4 + perf F4 + perf F6)

Three carry-over items promoted to HIGH at bughunt-7. All closed.

- **otel F4 PROMOTED**: leonard-mcp + leonard CLI hot paths now
  emit build-tag-gated spans (`leonard.mcp.verify_symbol`,
  `leonard.mcp.find_symbol`, `leonard.mcp.list_files`,
  `leonard.index.all`). Pre-v0.50.2 a `go build -tags otel`
  produced byte-identical binaries to the no-op build for these
  two binaries — only leonard-hook had real instrumentation.
  Verified via go tool nm: otel build now carries 1434 telemetry
  symbols vs 1 stub for the no-op build (and the binary doubles
  in size, as expected when the SDK actually links in).
- **perf F4 PROMOTED**: indexer worker pool. `IndexAll` now fans
  out indexAbs across NumCPU workers (bounded 2-16) through a
  buffered channel. The `parseFailures` slice gained a mutex
  since multiple goroutines now append. Synthetic 300-file
  polyglot bench should drop from ~6s to ~1-2s on an 8-core
  machine.
- **perf F6 PROMOTED**: added `PRAGMA wal_autocheckpoint(1000)`
  to the store DSN. The v0.15 manual checkpoint-after-DeleteFiles
  only fired on bulk deletes, so long-running processes
  accumulated 4+ MiB of WAL after 700 sequential post-edit hooks
  on a single file. Auto-checkpoint at 1000 frames bounds growth
  without hurting hot-path latency.

`go test -race ./...` clean across all packages — the new mutex
gates correctly under contention.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.50.1 — store SQL LIMIT + helper RSS cap (sec-3 F3 + F4)

Two HIGH findings from security review #3.

- **sec-3 F3**: store.FindSymbolsByName had NO SQL LIMIT — a name
  matching 500k symbols materialized every row in Go memory (302 MB
  RSS in the audit) before the MCP-layer 500-cap truncated. Added
  `MaxSymbolQueryRows = 1000` and LIMIT clauses on both
  FindSymbolsByName and FindSymbolsByQuery. The MCP-layer 500-cap
  is still the wire boundary; this stops the heap-pressure path.
- **sec-3 F4**: helper RSS at 3.9 MiB pathological input measured
  1.69 GiB peak — ~5× worse than bughunt-5's 400 MB claim. Lowered
  `maxIndexedFileBytes` from 4 MiB to 2 MiB. Worst-case helper RSS
  drops to ~1 GiB which is recoverable on dev machines.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.50.0 — .leonard/ guard hardening (2 CRIT + 2 HIGH)

Round 7 + Security review #3 found four bypasses of the v0.46.0
RCE guard. All four closed here.

CRITICAL:
- **sec-3 F1 — symlink bypass**: a Claude session could `ln -s
  .leonard/config.toml safe.txt` (via Bash, which wasn't hooked),
  then `Write safe.txt` — OS resolves the link and writes the
  attacker TOML to .leonard/. Verified end-to-end. Fix: new
  `isUnderLeonardDirResolved` calls `filepath.EvalSymlinks`
  before the segment check. For paths that don't exist yet (Write
  creating a new file), walks up to the deepest existing
  ancestor, EvalSymlinks that, then re-attaches the trailing
  segments. The lexical and resolved checks run together.
- **sec-3 F2 — case-insensitive bypass**: `seg == ".leonard"` was
  byte comparison. On APFS / NTFS / Samba (i.e. every default
  Mac and most Windows dev setups), `.LEONARD/config.toml`
  cleared the guard and landed in the real `.leonard/` dir.
  Fix: `strings.EqualFold(seg, ".leonard")`.

HIGH:
- **bughunt-7 F2 — Bash bypass**: the hook matcher in the
  dogfood wiring covered Edit/Write/MultiEdit/NotebookEdit but
  not Bash. `echo pwned > .leonard/config.toml` slipped through
  with one tool call. Fix: PreEditToolInput now decodes the
  `command` field; new `bashTouchesLeonardDir` scans the command
  string for `.leonard/`, `.leonard\`, or `.leonard` as a path
  token (with shell-boundary detection so `leonardish/` doesn't
  false-positive). README dogfood matcher updated to include
  Bash.
- **bughunt-7 F3 — backslash separator on Unix builds**:
  `path\.leonard\config.toml` was one opaque segment to
  `strings.Split(... "/")`. Real on WSL deployments. Fix:
  `strings.ReplaceAll(path, "\\", "/")` before splitting.

Regression tests:
- TestHandlePreEdit_RejectsCaseInsensitiveLeonardDir (4 cases)
- TestHandlePreEdit_RejectsBackslashLeonardDir (4 cases)
- TestHandlePreEdit_RejectsBashWritesToLeonardDir (5 commands)
- TestHandlePreEdit_AllowsBashCommandsThatDontTouchLeonard (6
  innocent commands)
- TestHandlePreEdit_RejectsSymlinkToLeonardDir (real symlink)

SECURITY.md threat-model table updated; the post-edit verifier
trust boundary now lists v0.46 + v0.50.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.49.0 — Makefile + CI caching + issue/PR templates (bughunt-6 Theme E)

Final fix-round-6 polish.

- **store-eval F1 + launch F12**: Makefile cleaned up. The v0.1
  "bosun lane" workflow language and the dead `leonardreal`
  build tag are gone. `make help` lists the real targets:
  check / test / test-race / vet / build / build-otel /
  build-rust / build-treesitter / install / tidy / clean.
- **store-eval F4 + launch-readiness N6**: CI workflow caching.
  `setup-go@v5` now has `cache: true` (~3-4× cache hits on
  go.sum-stable runs). `Swatinem/rust-cache@v2` caches the two
  Rust workspaces. ~90s saved per CI run on cache hits.
- **launch-readiness N7**: issue + PR templates under `.github/`.
  Bug-report + feature-request issue templates plus a PR
  template that lists the test plan + compatibility-notes
  prompts directly. Visible "this project takes contribution
  seriously" signal for new visitors.
- **README requirements**: dropped the "auto-fetched via
  toolchain directive if 1.21+ is installed" claim. Indirect
  dependencies have transitioned to Go 1.25 language features,
  so older toolchains can't build (`go mod tidy` reverts any
  attempt to lower the `go` directive). The README now states
  Go 1.25+ as a hard requirement, which matches reality.

Items NOT closed in v0.49 (carry forward to future round):
- store-eval F2: go.mod toolchain directive that would let
  Go ≤1.21 install Leonard. Can't be done — deps require 1.25.
- launch F1: GitHub Actions billing/spending-limit is a
  user-side action, not a code fix.
- launch F13: stale local binaries at repo root. They're
  gitignored; not visible from a fresh clone.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.48.0 — Carry-over promotions + supply-chain fixes (bughunt-6 Theme D, partial)

Three of the seven items from Theme D. The bigger refactors (otel
F4 binary instrumentation; perf F4 indexer worker pool; perf F6
WAL checkpoint cadence) are deferred to a future round — they're
meaningful changes that need design work, not load-bearing for
launch.

- **languages F1 (PROMOTED to HIGH)**: tree-sitter `module_name`
  rewrite. The v0.19 basename-only form silently collided across
  same-name files in different dirs — `src/foo.rs` and `lib/foo.rs`
  both produced module "foo" for 29 tree-sitter languages,
  breaking the index's identity contract. The fix mirrors the
  Go-side `internal/parse/qname.go::moduleQualifier`:
  path-separator-to-dot + extension strip. So `src/foo.rs` →
  `src.foo`, `lib/parse/qname.go` → `lib.parse.qname`.
- **security-2 F3**: Cargo.lock now committed for both
  `internal/parse/rust/` and `internal/parse/treesitter/`. The
  v0.19 .gitignore excluded them, so every fresh tree-sitter
  build resolved grammars without lockfile pins — a hijacked
  point-release of any of the 28 grammars would have run its
  build.rs on the CI runner. With the lockfiles in tree, CI's
  cargo build resolves deterministically.
- **store-eval F6**: NFC normalization gap in claim writes. The
  indexer's `storeKey` normalized file paths to NFC, but
  `RecordClaim` accepted whatever path Claude Code's hook
  envelope emitted. A claim recorded with an NFD path failed to
  match the file row (NFC) on `SupersedeClaimsForFile`. Added
  `normalizeClaimPath` and called it on both write and supersede
  paths.

Deferred to a future round (still in the carry-over backlog):
- otel F4 (leonard-mcp + leonard CLI byte-identical with/without
  -tags otel — instrument the hot paths in both binaries)
- perf F4 (indexer worker pool, 42× speedup)
- perf F6 (WAL checkpoint cadence on long-running processes)
- store-eval F10 (migrateV7 LIKE-text matching could in principle
  match a legit MCP claim; not seen in practice, low risk)
- mcp F3 (silent truncation in get_unverified_claims response —
  no `truncated` flag yet)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.47.0 — MCP correctness (bughunt-6 Theme C)

Two HIGH findings from the bughunt-6 mcp-and-hooks-deep audit.

- **mcp F1**: `languageFromPath` only knew 5 languages (Go,
  Python, TypeScript/TSX, JavaScript/JSX). Every other extension
  returned "" — `verify_symbol(language="rust")` filtered to zero
  matches for the 22+ tree-sitter languages added since v0.1.
  Rewrote the mapping to cover all 39 registered languages plus
  the basename-dispatched cases (Makefile, BUILD, CMakeLists.txt,
  package.json, Cargo.toml, go.mod, pom.xml). Verified end-to-end:
  a Rust file now resolves through `leonard verify hello`
  correctly.

- **mcp F2**: `verify_symbol` and `find_symbol` had no MCP-layer
  ceiling and no SQL LIMIT. A caller passing `limit=10000000`
  could materialize the entire symbol table before any cap
  applied. Added `MaxSymbolResults = 500` and clamp the user-
  provided limit to min(limit, 500). 500 is well above every
  real Claude-Code consumer (the reasoning loop is bounded by
  its own context budget).

mcp F3 (silent truncation in get_unverified_claims) deferred to
v0.48.0 along with the carry-over promotions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## v0.46.1 — Launch-surface accuracy sweep (bughunt-6 Theme B)

Pure docs/config polish closing 13 launch-surface findings from
bughunt-6 + security-2. Zero behavior changes.

- **launch F2**: status line bumped v0.45.0 → v0.46.1.
- **launch F3**: language-count consistency. Was "27+" / "Twenty-six" / 28 table rows / 39 in source. Now: 29 tree-sitter + 4 production + 3 SFC + 2 structured-file + 4 manifest formats, with SQL moved from "structured-file inspectors" to "tree-sitter languages" (it's tree-sitter-sequel, not bespoke).
- **launch F4**: bughunt-1 HIGH count corrected (4 → 8) in the README narrative.
- **launch F5**: bughunt-3 HIGH count corrected (6 → 3); Security #1's 2 HIGHs already had their own row.
- **launch F6**: `audits/README.md` rewritten — 9 wrong filenames replaced; 3 missing files added; round-6 entries added.
- **launch F7**: `examples/pydantic-ai/README.md` line numbers corrected (`Open` 42→103, `IndexAll` 104→274).
- **launch F8**: v0.37.0 CHANGELOG attribution fixed — the Purser/Leonard PR was filed by the maintainer themselves while scaffolding Purser, not by an outside contributor.
- **launch F9**: SECURITY.md "MCP stdin filter" row corrected v0.6 → v0.13.
- **launch F10**: SECURITY.md "email the maintainer" line dropped; PVR is the canonical disclosure path.
- **launch F14**: README Install section now states Rust toolchain requirement explicitly, plus compile times and `target/` cache footprint.
- N3: stripped version-stamp parentheticals from language tables.

Round-6 + Security-#2 entry added to the README "Bug-hunt discipline"
table including the v0.46.0 CRITICAL closure.

Still pending the user (not code-fixable):
- B1: flip repo visibility to public.
- launch F1: GitHub Actions billing/spending-limit (CI badge red).

## v0.45.1 — Launch polish

A second-pass review against the "HN-tomorrow-morning" bar surfaced
6 BLOCKING items, all fixed here. No code-behavior changes — pure
docs / wiring / repo-organization polish.

- **B3** — Broken brace-expansion markdown link in README + SECURITY
  (`[bughunt-{1..5}-triage.md](.)` doesn't render). Replaced with
  explicit per-round links into the new `audits/` directory.
- **B4 + B5** — `leonard`, `leonard-hook`, and `leonard-mcp` now all
  respond to `--version`. The two cobra-rooted binaries via cobra's
  built-in `Version` field; `leonard-mcp` via an early argv check
  before the MCP run loop (cobra isn't on that path).
- **B6** — `.leonard/config.toml` advertised `[index]`, `[verifiers]`,
  and `block_on_fabricated_symbol` — none of which the runtime
  Config struct reads (bughunt-2 Theme A trimmed them out 30+
  versions ago). Rewritten to only contain the real schema.
- **S1 + S2** — 25 audit/triage markdown files moved out of the
  repo root into a new `audits/` directory (with its own index
  README); the root now shows the ~8 user-facing files instead of
  32. Stale `phase-{1,2,3}-brief.md` and `fix-1-brief.md` MVP-era
  files removed.
- **S5** — README gets CI / License / Go Reference badges at top.
- **S7** — Status-line tone updated from "have driven the project"
  (past-progressive, reads in-progress) to "stable; every HIGH
  severity finding closed" (declarative, reads shipped).

## v0.45.0 — Docs + release infra sweep (bughunt-5 Theme E)

- README rewritten end-to-end: language table now lists all 27+
  tree-sitter languages, the 4 production-dogfooded parsers, the
  3 SFC preprocessors, the 3 structured-file inspectors, and the
  4 manifest dep-graph formats (was 4 rows total).
- Status line jumped from v0.17 to v0.45; bug-hunt discipline
  narrative added (5 rounds + security review).
- DESIGN.md §6 updated — languages previously called "Future" now
  reflect their production status.
- CHANGELOG.md caught up (v0.19 → v0.45 below).
- `.github/` directory added: CI workflow, CONTRIBUTING.md,
  SECURITY.md, Code of Conduct.
- First git tag: `v0.45.0`.

## v0.44.0 — Perf fixes (bughunt-5 Theme F partial, 3 items)

- **F1**: lowered `maxIndexedFileBytes` from 8 MiB to 4 MiB. The
  tree-sitter helper amplifies source size ~100× in RSS during
  parse; a 7.8 MiB Ruby file hit 839 MB RSS. 4 MiB caps the
  worst-case helper RSS at ~400 MB.
- **F2**: `queryUnverifiedClaims` pushes `LIMIT 1000` into the SQL.
  On a 500k-claim ledger the v0.38 unbounded query took 1.15s to
  materialize every row before the Go-side slice truncated.
- **F9**: manifest deps switched from `kind="const"` to
  `kind="dependency"`. v0.36 polluted the const namespace on
  monorepos (7000 const symbols mixed with real-source consts).

## v0.43.0 — Tree-sitter dispatcher polish (bughunt-5 Theme D)

- **F1**: invalid UTF-8 now exits 2 (parse error) instead of 1
  (infra). Read source as `Vec<u8>`, then validate via
  `std::str::from_utf8`.
- **F2**: HCL multi-label blocks no longer emit duplicate symbols
  — added `.` anchor in `HCL_QUERY` so only the FIRST string_lit
  is captured as `@name`.
- **F4 + languages F3**: `is_exported` overhauled to tokenize
  modifier text on word boundaries (so C#'s `protected internal`
  resolves to exported via the `internal` token). Added
  `Language.default_exported_methods` flag so each grammar picks
  the right default for Ruby/Lua/Swift/Kotlin/Scala/etc.

## v0.42.0 — Preprocessor regex robustness (bughunt-5 Theme C)

Replaced the v0.26 / v0.32 `.*?` regex extractors with
context-aware scanners that respect JS string + template literal +
HTML comment lexical state.

- **F1**: `</script>` inside a string literal no longer truncates
  the body.
- **F2**: `<script>` inside an `<!-- ... -->` HTML comment no
  longer fires.
- **F30**: Astro frontmatter regex truncated on `\n---\n` inside a
  template literal — the new `findAstroFrontmatter` mirrors the
  context tracking.
- **F3**: `package.json` dep value handling switched to
  `json.RawMessage` so pnpm/yarn object-form versions don't nuke
  the whole file.

## v0.41.0 — Parent-folding completeness (bughunt-5 Theme B)

`find_parent_name` had a single-strategy lookup that silently
failed for grammars using named-child shapes. Replaced with a
four-step `extract_container_name`:

1. `child_by_field_name("name")` — Java/Ruby/Kotlin/etc.
2. Direct child of kind `name`/`identifier`/`type_identifier` —
   GraphQL.
3. Child of kind `<container>_name` wrapping an identifier — Proto.
4. Child carrying its own `name:` field — SQL (`create_table
   (object_reference name: (identifier))`).

Also widened parent-folding to include `const` so SQL columns get
`module.users.id` instead of colliding on `module.id`.

## v0.40.0 — C++ in-class inline methods (bughunt-5 languages F4, HIGH)

The CPP_QUERY captured `field_declaration` with
`function_declarator` (method declarations) but NOT
`function_definition` with `field_identifier` declarator (inline
method definitions). nlohmann/json — the most-downloaded C++
library on the planet — indexed 551 files and produced ZERO
method symbols.

Added the missing query arm. End-to-end dogfood: `verify dump`
now finds 3 occurrences of `basic_json::dump()` at correct lines.

## v0.39.0 — v0.38 ledger correctness (bughunt-5 Theme A, 1 HIGH + 4 MED)

- **verifier F1 HIGH**: `SupersedeOutstandingFailures` was using
  `claim LIKE '%=failed%'`, which matched any text containing
  "=failed" (e.g. a user claim `user_input=failed to load`).
  Switched to the existing `vet_ok = 0` integer column.
- **integration F3**: `migrateV7` deleted `index=skipped (file
  not found)` rows but `handleMissingFile` STILL wrote them. The
  cleanup was one-shot but the symptom regenerated. Stopped
  recording the claim entirely (same shape as v0.38's
  `handleEscapedPath` change).
- **verifier F4**: rejected negative/zero `verify.Timeout`.
- **verifier F6**: `ResolveClaim` `--note` now capped at 4 KiB.
- **verifier F11**: whitespace-only `command` no longer treated
  as set.

## v0.38.0 — Claim-ledger hygiene (4 fixes)

- `handleEscapedPath` stops recording claims — path-escape is a
  tool-layer rejection, not an unverified work claim.
- New `SupersedeOutstandingFailures` — project-wide supersede on
  vet=ok to catch multi-file fix-cascade case.
- `leonard claims resolve <id> [--note "..."]` CLI escape hatch.
- `migrateV7` one-time cleanup of historical escape-path +
  missing-file claim rows.

## v0.37.0 — Configurable post-edit verifier (PR #3)

Filed in the [issue + PR pair](https://github.com/jasondillingham/leonard/pull/3) shape (Issue #2 describes the constraint; PR #3 ships the fix that honors it) while scaffolding a Rust homelab project that wanted Leonard's verify loop driving `cargo check` instead of the hardcoded `go vet ./...`. Adds opt-in
`[post_edit.verify]` section to `.leonard/config.toml` with
`command`, `working_dir`, `timeout`. When set, post-edit hook
runs the configured command through `sh -c` instead of the
hardcoded `go vet ./...`. Default behavior unchanged.

## v0.36.0 — Manifest-aware dependency graph

Walks four canonical manifest formats and emits one Symbol per
declared dependency:

- `package.json`: dependencies / devDependencies /
  peerDependencies / optionalDependencies.
- `Cargo.toml`: [dependencies] / [dev-dependencies] /
  [build-dependencies]. Inline-table form handled.
- `go.mod`: every `require` via `golang.org/x/mod/modfile`.
- `pom.xml`: top-level `<dependencies>/<dependency>`.

Use case: `verify_symbol("react")` tells Claude whether the
project actually depends on react before fabricating an import.

## v0.35.0 — GLSL + HLSL (shader languages)

Both grammars are C-family — one shared `SHADER_QUERY` captures
function_definition, struct_specifier, and declaration (top-level
uniforms / varyings / inputs / outputs as `@const`). Extensions
include per-stage shorthand: `.glsl`, `.vert`, `.frag`, `.geom`,
`.comp`, `.tesc`, `.tese`, `.hlsl`, `.fx`, `.fxh`.

## v0.34.0 — Just + Starlark (Bazel)

- **Just** (tree-sitter-just): recipes → function, top-level
  assignments → const. Dispatch on `.just` + `justfile` basename.
- **Starlark** (Bazel BUILD/`.bzl`): `def` macros → function; rule
  calls with `name = "..."` → type (Bazel target). Basenames
  BUILD, BUILD.bazel, WORKSPACE, WORKSPACE.bazel.

## v0.33.0 — Erlang + R

- **Erlang**: module_attribute, record_decl, fun_decl.
- **R**: function definitions via the `<-` assignment idiom.

## v0.32.0 — Astro + Solid

- **Astro**: frontmatter (between `---` fences) + embedded
  `<script>` blocks both routed through the TypeScript extractor.
- **Solid**: `.jsx` registered to the TypeScript extractor (Solid
  is documented as "a semantic layer on TSX").

## v0.31.0 — WIT (Smithy deferred)

WebAssembly Component Model types. Smithy was in the original
scope but tree-sitter-smithy 0.0.1 pinned tree-sitter v0.20 —
incompatible with the v0.25 main runtime; deferred until a
compatible grammar surfaces.

## v0.30.0 — OpenAPI / Swagger inspector

Structured-file extractor for API specs. Walks
`paths.<path>.<method>` and `components.schemas` / `definitions`.
Filename-based dispatch (openapi.{yaml,yml,json},
swagger.{yaml,yml,json}).

## v0.29.0 — Jupyter notebooks

Parses .ipynb JSON, concatenates code cells with blank-line
separators, routes through ExtractPython. Markdown/raw cells
skipped.

## v0.28.0 — SQL migration files

tree-sitter-sequel. CREATE TABLE/VIEW/INDEX/FUNCTION + column
definitions. Schema-as-source-of-truth use case for verifying
migration history.

## v0.27.0 — HCL/Terraform + GraphQL SDL + Protocol Buffers

Three IDL/config languages added in one batch.

## v0.26.0 — Vue + Svelte SFC

Regex-based `<script>` block extraction, routed through
TypeScript extractor with line-offset adjustment. (v0.42 later
replaced the regex with a context-aware scanner.)

## v0.25.0 — Solidity + Make + CMake

Build tools beyond Make/CMake plus smart contracts. Introduced
basename-based dispatch (`langExtractorsByName`) for Makefile +
CMakeLists.txt.

## v0.24.0 — Zig + Nix + Elixir

Three smaller-ecosystem languages. Elixir's `defmodule`/`def`/etc.
required the predicate-binder pattern (`@_def` capture filtered
out at the dispatcher level).

## v0.23.0 — PHP + Lua + Bash

Three scripting languages. Lua's three function-declaration
shapes (plain, dot-indexed, method-indexed) all captured.

## v0.22.0 — C + C++

Lower-level languages. C/C++ have deeper nesting; function names
live two levels deep inside `declarator: (function_declarator
declarator: (identifier))`. v0.40 later added in-class inline
method coverage.

## v0.21.0 — Kotlin + Scala + Dart

JVM/Flutter ecosystem languages. Kotlin's `interface` rides on
`class_declaration`; Scala uses `_definition` suffix convention;
Dart's `function_signature` is shared between methods and free
functions.

## v0.20.0 — Ruby + C# + Swift

First batch on the v0.19 tree-sitter strategy. Also closed the
v0.19 known-limitation around method/constructor qname collisions
(via `parent_container_kinds` parent-folding).

## v0.19.0 — Tree-sitter parser strategy + Java validation

The architectural unlock. New Cargo crate at
`internal/parse/treesitter/` — one binary that handles every
supported language, `--lang <name>` selects the grammar. Per-
language wrappers in Go (`ExtractJava`, etc.) are one-liner aliases
routing to `ExtractTreeSitter`. Validated against google/gson:
262 files / 4,136 symbols.

## v0.18.0 — Documentation reality-gap sweep (bughunt-4 Theme C)

- README now reflects v0.17+ reality: version stamp, language table,
  component list, project layout, OTel scope (hook-only).
- Added `CHANGELOG.md` synthesized from commit history.

## v0.17.0 — Carry-over bughunt-2 MEDIUMs

- **cli F9**: `leonard verify` and other subcommands now walk up
  from cwd looking for `.leonard/` instead of failing in subdirs.
- **cli F18**: `leonard doctor` no longer double-counts stale files
  as both parse-failure suspects and stale rows.
- **pre-edit F1**: sibling-scan skip list now uses the exported
  `index.DefaultSkipDir` so it stays in lockstep with the
  indexer's `defaultSkipDirs`.

## v0.16.0 — MCP claim supersession + missing-file additionalContext

- **mcp F4**: `record_claim` now accepts `file_path`, so MCP-
  recorded unverified claims can be superseded by a later vet=ok
  post-edit hook on the same file.
- **mcp F5**: `handleMissingFile` (post-edit short-circuit on a
  missing file) now emits `HookSpecificOutput.AdditionalContext`
  so the model sees the no-op signal.

## v0.15.0 — Store performance

- migrateV6 adds `idx_files_indexed_at`, `idx_claims_verified`, and
  `idx_decisions_recorded_at` (all found missing via EXPLAIN QUERY
  PLAN).
- `GetStaleDecisions` N+1 fix: collect every related-files/symbols
  ref upfront, run two chunked IN-clause queries, then do in-memory
  membership checks instead of 2000+ per-call round-trips.
- `DeleteFiles` triggers `PRAGMA wal_checkpoint(PASSIVE)` after
  bulk commits (≥100 rows) so `.leonard.db-wal` stays bounded.

## v0.14.0 — Path-trust completeness

- Pre-edit hook now routes `file_path` through `ResolveSafe` before
  any `parser.ParseFile` (was a file-existence oracle).
- Dangling symlinks now rejected by `ResolveSafe` (the lexical-pass
  branch used to accept when `EvalSymlinks` errored).
- `storeKey` applies Unicode NFC normalization so the same on-disk
  file produces one row regardless of NFC vs NFD input.
- `pruneStaleFiles` and `doctor.StaleFiles` filter rows through
  `ResolveSafe` before stat.

## v0.13.0 — Cap completeness

- Replaced `bufio.Scanner` in `leonard-mcp` stdin filter with a
  custom line reader that resyncs past oversize lines. The v0.9
  "fix" was a documented no-op stub that caused a busy-spin DoS
  (100% CPU, 35 MB/s of stderr) — verified.
- `get_decisions` and `get_unverified_claims` cap aggregate response
  size at 1 MiB.
- `SessionStart` decision bullets truncated per-field so worst-case
  inject drops from 360 KiB to ~2.4 KiB.
- `supersede_decision` validates `new_choice` + `new_reasoning`.
- `leonard decisions add` CLI now validates topic/choice/reasoning
  (was bypassing the MCP-layer cap).
- `maxIndexedFileBytes` (8 MiB) — indexer rejects + records
  ParseFailure instead of reading multi-megabyte files.
- Oversize snippets and over-count MultiEdits now reject the hook
  with `ErrDecode` instead of silently truncating.
- `list_files` and `get_unverified_claims` gained `limit` fields.
- Shared cap constants in `internal/store/limits.go` so MCP and
  CLI layers reference one source of truth.

## v0.12.0 — Rust extractor correctness

- `impl_target_name` covers non-Path self_ty (Type::Reference,
  Tuple, Array, Slice) so `impl Display for &Foo` no longer
  silently drops its methods.
- `start_line` skips attributes + doc comments (matches Python/TS).
- Multi-segment `impl Display for std::collections::HashMap`
  preserves the full path so foreign-type qnames don't collide
  with local types.

## v0.11.0 — OTel lifecycle fixes

- `os.Exit()` no longer skips deferred telemetry shutdown.
- Fresh `context.Background()` used for shutdown (signal-cancelled
  ctx was aborting the flush).
- Explicit 5s `sdktrace.WithExportTimeout` so an unreachable OTLP
  endpoint doesn't block the hook for 30 seconds.

## v0.10.0 — Eval framework readiness

- `scoring.py` subprocess sets `cwd=project_root` (was silently
  falling back to permissive store outside the repo).
- `mcp>=1.0` added to `pyproject.toml`.
- `GO_BLOCK_RE` accepts `golang`/uppercase/no-trailing-newline
  fence variants.
- `_run_self_check` probes the hook at first scoring call so a
  drifted deny-wording raises loudly rather than silently zeroing
  fabrication counts.
- Two samples rewritten to use package-qualified function refs
  the pre-edit guard can actually validate.

## v0.9.0 — Resource-cap hygiene

- `MaxHookPayloadBytes` (16 MiB), `MaxSnippetBytes` (1 MiB),
  `MaxMultiEditElements` (100) — caps at the hook decode boundary.
- Decision and claim text caps in `record_decision` /
  `record_claim`.
- Earlier (buggy) `newOversizeTolerantScanner` stub for the MCP
  Scanner overflow — superseded by v0.13.

## v0.8.0 — Path-trust sweep

- `ResolveSafe(root, claimed)` validates external-caller-supplied
  paths against the project root; rejects `/etc/hosts`-style
  absolutes and `../escape` traversals.
- IndexFile, IndexAll's walker (symlink-skip), pre-edit's
  sibling-scan walker, post-edit's `handleEscapedPath` all wired
  through.

## v0.7.1 — `idx_symbols_parent`

- migrateV5 adds the missing index on `symbols.parent_id`. The
  v0.7.0 commit message attributed the prune-sweep bench cost to
  WAL fsync; bughunt-3 traced it to this unindexed self-
  referential FK. `BenchmarkDeleteFiles_1k` drops from ~5.8s to
  ~42ms (~137×).

## v0.7.0 — Batched `Store.DeleteFiles`

- Replaces per-row delete loop with single-tx chunked IN clauses.
- Indexer's `pruneStaleFiles` collects-then-batches.
- ~200× speedup over the v0.6.1 per-row baseline on polluted-index
  cleanup.

## v0.6.1 — `defaultSkipDirs` ecosystem coverage

- Skip-dir map grown from 5 to 15 entries: `.venv`, `venv`,
  `__pycache__`, `target`, `.mypy_cache`, `.next`, `.nuxt`,
  `.pytest_cache`, `.ruff_cache`, `.tox`, etc.
- `pruneStaleFiles` removes existing rows whose path traverses a
  skip-dir component (upgrade path-cleanup).

## v0.6.0 — Optional OpenTelemetry

- `-tags otel` build pulls in the OTel SDK and reads `OTEL_*`
  env vars; default build has zero overhead (no-op stubs, deps
  not linked).
- Spans on `leonard.pre-edit`, `leonard.pre-edit.sibling-scan`,
  `leonard.post-edit`, `leonard.post-edit.index`,
  `leonard.post-edit.vet`.

## v0.5.0 — Rust parser

- `internal/parse/rust/` Cargo crate builds
  `leonard-extract-rust`, a syn-based extractor producing JSON.
- Go-side wrapper has same shape as Python: env override
  (`LEONARD_RUST_EXTRACTOR`), context timeout, source-tree
  fallback.
- Dogfooded against ripgrep: 100 / 100 files, 2,678 symbols.

## v0.4.0 — Anthropic Inspect eval framework

- `evals/inspect/` adds three Tasks (control, treated, treated
  with system prompt) measuring fabrication rate with vs. without
  Leonard's MCP tools.
- Scoring pipes the model's `go` block through `leonard-hook
  pre-edit` and counts blocked references.
- Live runs blocked on `ANTHROPIC_API_KEY`; mockllm path works.

## v0.3.0 — Pydantic AI demo

- `examples/pydantic-ai/demo.py` wires `MCPToolset(StdioTransport)`
  to the production leonard-mcp binary; shows verify_symbol +
  find_symbol round-trip.

## v0.2 — Python parser swap + CLI subcommands + doctor + benches

- Python parser switched from `gpython` to host `python3` via
  subprocess (full modern Python support, ~40ms parse cost).
- `leonard decisions {add,list}` and `leonard claims {list}` CLI
  surfaces.
- `leonard doctor` project-health report.
- Hook-latency benchmarks.

## v0.1 — Initial dogfoodable release

Three binaries, SQLite store, MCP tool surface, four Claude Code
hook handlers.
