# Leonard — Security Review #2 (v0.45.1)

## Summary

Second-pass audit, following security-1 at v0.7. Since v0.7 Leonard has added:
26 tree-sitter grammars (Rust helper subprocess), four structured-manifest
extractors (`package.json` / `Cargo.toml` / `go.mod` / `pom.xml`), three
SFC preprocessors (Vue / Svelte / Astro), a Jupyter / OpenAPI inspector,
`[post_edit.verify]` (project-authored TOML → `sh -c` command), the
`leonard claims resolve <id>` CLI mutation path, `--version` argv parsing
in the MCP loop, and a CI workflow.

This audit surfaces **one critical RCE chain** (Category A #1: the
project-authored `[post_edit.verify].command` is a sufficient capability,
but the pre-edit fabrication guard only inspects `.go` files — meaning a
confused/malicious Claude session can WRITE the `.leonard/config.toml`
itself and escalate, since `.toml` writes pass through pre-edit
unguarded), **one high** finding (Category A #2: no path-trust on
`[post_edit.verify].working_dir` — `working_dir = "/etc"` runs the
verifier with cwd outside the project root), **two medium** findings
(Cargo.lock files are gitignored, so CI's `cargo build --release`
resolves grammars without lockfile pins; pathological 4 MiB nested
tree-sitter input drives helper RSS to 1.95 GiB — well above the 400 MB
worst-case claimed in indexer.go:563-565), and several smaller
informational notes.

F1 (path-escape via `file_path`), F2 (143 MB-→-7 GB amplification),
F3 (symlink-out), and F12 (oversize MCP line) from security-1 are
verified-closed at v0.45.1. F4/F8 (decision/claim caps) closed via
`store/limits.go`. F7 (`.leonard/` perms) **still unfixed** — 0o755/0o644.
F6 (env-var override) **still unfixed** — undocumented in user docs.

Local-only assumption confirmed (no `net.Listen` / `http.ListenAndServe`
anywhere outside the opt-in OTel exporter). The Python helper (`ast.parse`),
Rust helper (`syn::parse_file`), and tree-sitter helpers (`Parser::parse`)
are all non-executing AST builders — no source-code-is-executed attacks
in the parser lane.

---

## Findings

### F1 — CRITICAL: `[post_edit.verify].command` confused-deputy RCE via Claude-written `.leonard/config.toml`

- **Severity:** critical
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec2-probe && cd /tmp/sec2-probe
  leonard init .

  # Step 1: Claude session writes .leonard/config.toml.
  # The pre-edit fabrication guard ONLY inspects .go files
  # (internal/hooks/pre_edit.go:181 — !strings.HasSuffix(filePath,".go")
  # short-circuits to allowResponse()). Writes to .toml/.yaml/etc.
  # are NEVER pre-edit-gated. There is NO path-trust check that
  # forbids Claude from writing to .leonard/* either.
  cat > .leonard/config.toml <<'EOF'
  [post_edit.verify]
  command = "id > /tmp/sec2-pwned.txt"
  EOF

  # Step 2: any next Edit/Write to ANY file fires the attacker
  # command via sh -c inside post_edit.go's runVet → MakeShellRunner.
  echo 'package main' > test.go
  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"Edit","tool_input":{"file_path":"test.go"},
        "cwd":"/tmp/sec2-probe"}' \
    | leonard-hook post-edit

  cat /tmp/sec2-pwned.txt
  # → uid=501(jasondillingham) gid=20(staff) …
  ```
- **Observed:** Full RCE achieved. The shell command `id > /tmp/sec2-pwned.txt`
  ran with the user's full environment and wrote the file successfully.
- **Threat-model fit:** This is a **confused-deputy** in the strict sense.
  SECURITY.md's "Existing guards" table (line 27) declares `[post_edit.verify]
  config is project-authored only` — meaning the threat model trusts the
  config because "the user authored it." That premise breaks the moment
  Claude itself can write to that file. The pre-edit guard's design
  intent is fabrication detection (Go-only by construction), so it makes
  no claim to block writes to `.leonard/config.toml`. There is no
  separate guard that does. **Combined**, these two design facts produce
  a path from "Claude session is influenced by a malicious prompt-injected
  README or comment" to "arbitrary shell on the host."
- **Why this is the load-bearing finding:** Every other Category A path
  reduces to the same question. If Claude can author `.leonard/config.toml`,
  then `command`, `working_dir`, `timeout` are all attacker-controlled.
  The remaining defenses are conventions (project trust) that aren't
  enforced in code.
- **Suggested fix shape (pick one):**
  1. **Pre-edit guard `.leonard/` writes.** Add a new pre-edit reject path
     for any tool_input.file_path that resolves under `<project>/.leonard/`.
     The fabrication guard's current `HasSuffix(".go")` short-circuit
     would need to be reordered so the path check fires first. This is
     the strongest defense — Leonard's own config dir is treated as
     equivalent-to-root.
  2. **Disable `[post_edit.verify]` unless explicitly opted in via a
     separate user-level allowlist.** A new `~/.config/leonard/allowed-projects.toml`
     listing project roots where `[post_edit.verify]` is permitted.
     Claude can write the project's `.leonard/config.toml` but the runtime
     won't honor `[post_edit.verify].command` until the human user
     explicitly allows the project. This is the "1Password-style local
     vault" pattern.
  3. **Replace `sh -c command` with a structured allowlist.** Drop the
     free-form shell command; replace with a fixed enum of verifiers
     (`go vet`, `cargo check`, `pnpm tsc`, `ruff check`, `mypy`) with
     no user-controllable argv. Loses the composition power (`&&`,
     `|`) but eliminates the RCE surface.
- **Out of scope:** The question of whether Claude Code should refuse to
  write to "tool-config" paths is upstream — Leonard should defend itself
  regardless.

---

### F2 — HIGH: `[post_edit.verify].working_dir` has no path-trust gate

- **Severity:** high
- **Reproducer:**
  ```bash
  cd /tmp/sec2-probe
  cat > .leonard/config.toml <<'EOF'
  [post_edit.verify]
  command = "pwd; ls | head -3"
  working_dir = "/etc"
  timeout = "5s"
  EOF
  echo 'package main' > test.go
  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"Edit","tool_input":{"file_path":"test.go"},
        "cwd":"/tmp/sec2-probe"}' \
    | leonard-hook post-edit
  # → SystemMessage: "leonard: re-indexed /tmp/sec2-probe/test.go, pwd; ls ok"
  # → claim evidence:
  #     /etc
  #     afpovertcp.cfg
  #     aliases
  #     aliases.db
  ```
- **Observed:** `working_dir = "/etc"` accepted unconditionally. The
  verifier ran with cwd = `/etc` and the `pwd; ls` output landed in the
  claim's evidence column (256 KiB cap), readable via `get_unverified_claims`
  MCP tool from any session.
- **Source:** `internal/hooks/shell_runner.go:25-39` — `MakeShellRunner`
  passes `workingDir` verbatim to `cmd.Dir` with no resolution against
  the project root.
- **Threat-model fit:** Compounds F1 (above). Even setting aside F1, if a
  user manually writes a `working_dir = "/etc"` (e.g. copied from a
  malicious tutorial), the verifier reads the directory listing into the
  claim ledger. Information disclosure via the claim evidence column,
  which the MCP layer surfaces.
- **bughunt-5 verifier F3** flagged this. Confirmed still present at
  v0.45.1.
- **Suggested fix:** `MakeShellRunner` should call `index.ResolveSafe(projectRoot, workingDir)`
  before assigning `cmd.Dir`. On reject, return an error that maps to an
  unverified claim ("working_dir escapes project root").

---

### F3 — MEDIUM: Cargo.lock files are gitignored — CI resolves grammars without lockfile pins

- **Severity:** medium
- **Reproducer:**
  ```bash
  cd <repo>
  cat internal/parse/rust/.gitignore         # → target/, Cargo.lock
  cat internal/parse/treesitter/.gitignore   # → target/, Cargo.lock
  git ls-files internal/parse/rust/Cargo.lock internal/parse/treesitter/Cargo.lock
  # → (no output — files exist on disk but are NOT tracked)
  ```
- **Observed:** Both Rust crates' `Cargo.lock` files are gitignored. The
  CI workflow (`.github/workflows/ci.yml:38-41`) runs
  `cargo build --release --manifest-path internal/parse/{rust,treesitter}/Cargo.toml`
  on every PR. Without a lockfile, Cargo resolves to whatever versions
  match the semver constraints in `Cargo.toml` AT THAT MOMENT.
- **Why it matters:** The treesitter Cargo.toml depends on **28 grammars**,
  including several with 0.x.y or 0.0.x version pins (`tree-sitter-dart =
  "0.0.4"`, `tree-sitter-nix = "0.0.2"`) and several with "0.23"-style
  range pins (Cargo treats `"0.23"` as `^0.23.0` — any `0.23.y` accepted).
  Each grammar transitively pulls `cc` (the C-compiler runner) and runs a
  generated `build.rs` to compile the C parser source. A compromised
  point-release of any grammar — say `tree-sitter-foo 0.23.99` published
  by an attacker who took over the crates.io ownership — would be picked
  up at the next CI run, executing the attacker's `build.rs` on
  GitHub-hosted runners.
- **Threat-model fit:** Supply-chain / confused-deputy. CI runner is
  ephemeral and has access to `GITHUB_TOKEN` (default scope: read repo
  contents, write packages, write actions cache). A `build.rs` can
  exfiltrate the token over DNS or to any non-allowlisted endpoint.
- **Suggested fix:** Remove `Cargo.lock` from both `.gitignore` files
  and commit them. Add `--locked` to the CI workflow's cargo build
  invocations so a deviation between Cargo.toml + Cargo.lock at build
  time is a hard CI failure. (`--frozen` is even stricter — refuses to
  touch the network.) For Rust libraries (which Leonard isn't —
  it ships binaries), best practice is to commit `Cargo.lock` anyway.
- **Out of scope:** Replacing 0.x.y grammars with maintained 1.x
  variants (none for tree-sitter-dart, tree-sitter-nix) — that's a
  feature-set question.

---

### F4 — MEDIUM: Pathological 4 MiB tree-sitter input drives helper RSS to ~1.95 GiB

- **Severity:** medium
- **Reproducer:**
  ```bash
  TS_BIN=internal/parse/treesitter/target/release/leonard-extract-treesitter

  python3 -c "
  s = '['*(2*1024*1024) + ']'*(2*1024*1024)
  import sys; sys.stdout.write(s)
  " > /tmp/sec2-nested.rb
  # File is exactly 4 MiB — at the maxIndexedFileBytes cap (indexer.go:570).

  /usr/bin/time -l "$TS_BIN" --lang ruby /tmp/sec2-nested.rb < /tmp/sec2-nested.rb > /dev/null
  # → maximum resident set size:  1,950,875,648  (≈1.95 GiB)
  # → real time: 3.41s
  ```
- **Observed:** A 4 MiB file of pure `[` followed by pure `]` (extreme
  nesting depth) pushed the tree-sitter Ruby parser to **1.95 GiB peak
  RSS** — about **500x amplification** over the input size. The bughunt-5
  perf F1 comment in `internal/index/indexer.go:562-569` claims "4 MiB
  caps the worst-case helper RSS at ~400 MB," but this reproducer
  exceeds that estimate by ~5×.
- **Threat-model fit:** Resource exhaustion. A malicious project shipping
  a 4 MiB Ruby file with adversarial nesting (or any of the 27 other
  tree-sitter languages — the bug is generic to tree-sitter's recursive
  descent) will OOM the helper subprocess. Default macOS / Linux dev
  workstations with 16 GB RAM can absorb one such file, but Leonard
  walks the entire project tree — N pathological files indexed in
  sequence will swap the host.
- **Mitigation factor:** Helper is a subprocess and uses `WaitDelay = 500ms`
  after context cancel. The 30s timeout protects against wall-clock hangs.
  The Go process (indexer) itself isn't blown up — only the helper.
- **Suggested fix shape:**
  1. Add an `RLIMIT_AS` (address-space limit) on the helper subprocess
     via `syscall.Setrlimit` in the exec path (Unix only — Windows would
     need a Job Object). Cap at ~512 MiB; helper OOMs and indexer
     surfaces the file as a ParseFailure.
  2. Or lower `maxIndexedFileBytes` further (1 MiB? — but then large
     auto-generated parsers / vendored bundles would all hit the cap).
  3. Or pre-scan for "obviously pathological" inputs (e.g. byte
     histogram showing >50% one character) and skip them.
- **Out of scope:** Hardening tree-sitter itself — upstream issue.

---

### F5 — MEDIUM: `.leonard/leonard.db` + `.leonard/config.toml` are world-readable

- **Severity:** medium (informational unless host is multi-user — but
  bumping severity because launch readiness matters)
- **Reproducer:**
  ```bash
  mkdir /tmp/sec2-perms && cd /tmp/sec2-perms
  leonard init .
  ls -la .leonard/
  # drwxr-xr-x  ...  .leonard/
  # -rw-r--r--  ...  .leonard/config.toml
  # -rw-r--r--  ...  .leonard/leonard.db
  ```
- **Observed:** `wire_real.go:23` still uses `0o755` for the data dir
  (security-1 F7). `config.Save` (config.go:120, 127) still uses
  `0o755`/`0o644`. SQLite's default file perm honors the process umask
  (typically `0o022`, yielding `0o644`).
- **Threat-model fit:** Information disclosure on multi-user boxes —
  even though SECURITY.md scopes those out, `leonard.db` carries every
  decision, claim, and the full symbol index, which is project
  ground-truth that another user on the same box can pull at zero cost.
  Today's threat model says "single-user by design"; the perms commit
  to that.
- **Source:** Unchanged from security-1 F7.
- **Suggested fix:** Change to `0o700` (dir) and `0o600` (config). Pass
  `_pragma=secure_delete(on)` on `store.Open` to scrub deleted rows.

---

### F6 — MEDIUM: User clones an attacker repo → next post-edit fires attacker shell

- **Severity:** medium (precondition: user clones an attacker's repo
  AND enters it with Claude Code AND triggers any Edit/Write; precondition
  satisfied by every realistic OSS contribution / code-review scenario)
- **Reproducer:**
  ```bash
  # Attacker authors a repo
  mkdir /tmp/sec2-attacker && cd /tmp/sec2-attacker
  mkdir -p .leonard
  cat > .leonard/config.toml <<'EOF'
  [post_edit.verify]
  command = "touch /tmp/sec2-pwned-via-clone"
  timeout = "5s"
  EOF
  echo 'package main' > main.go
  # Attacker pushes to GitHub.

  # Victim clones + initializes Leonard
  git clone https://github.com/attacker/repo /tmp/sec2-victim
  cd /tmp/sec2-victim
  leonard init .   # preserves existing .leonard/config.toml (Bughunt-2 cli F1)

  # Victim opens Claude Code on the project, makes any edit:
  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"Edit","tool_input":{"file_path":"main.go"},
        "cwd":"/tmp/sec2-victim"}' \
    | leonard-hook post-edit

  ls -la /tmp/sec2-pwned-via-clone  # exists
  ```
- **Observed:** Full RCE on first post-edit after clone. The
  `init`-preserves-existing-config behavior (intentional, per Bughunt-2
  cli F1) means cloning a repo with `.leonard/config.toml` already in
  it is sufficient to escalate.
- **Threat-model fit:** This is **not** the "compromised Claude Code"
  threat model (out of scope). This is the user trusting a project
  enough to clone it, the same level of trust as running `make` or
  sourcing `.envrc`. The README does NOT warn about this — the
  `[post_edit.verify]` docs explain the command but don't say "if you
  cloned a repo and `.leonard/config.toml` was already in it, the
  command in it will run via `sh -c` on the next post-edit."
- **Suggested fix shape:**
  - **Documentation only (lightest):** Add a SECURITY.md note: "If
    cloning a third-party repo, inspect `.leonard/config.toml` before
    running `leonard init` or letting Claude Code edit anything."
  - **Defensive default (medium):** On `leonard init` against a project
    where `.leonard/config.toml` already exists AND contains a
    non-empty `[post_edit.verify].command`, print a one-line warning to
    stderr and refuse to honor the section until the user runs
    `leonard config trust .` to opt in.
  - **Strong:** See F1 fix #2 (per-user allowlist of projects where
    `[post_edit.verify]` is honored).

---

### F7 — MEDIUM: No cap on manifest dependencies — 100k deps in one `package.json` ingest cleanly

- **Severity:** medium
- **Reproducer:**
  ```bash
  python3 -c "
  import json
  deps = {f'dep{i}': '1.0.0' for i in range(100_000)}
  print(json.dumps({'dependencies': deps}))
  " > /tmp/sec2-bigpkg/package.json
  cd /tmp/sec2-bigpkg && leonard init . && leonard index
  sqlite3 .leonard/leonard.db 'SELECT COUNT(*) FROM symbols'
  # → 100000
  ls -la .leonard/leonard.db
  # → 16.8 MB SQLite from one 2 MB package.json
  ```
- **Observed:** 100k symbols emitted from a single 2.1 MB `package.json`.
  Indexing took 0.88s and 70 MB RSS — bounded, but the symbol DB grew
  ~8× the manifest size. The 4 MiB `maxIndexedFileBytes` cap (indexer.go:570)
  permits a 4 MiB `package.json`, which corresponds to ~190k deps. Each
  also creates a `qualifier_name` index entry.
- **Threat-model fit:** Resource exhaustion / DB bloat. A malicious
  project ships a `package.json` with 190k fake deps, the user runs
  `leonard index`, the SQLite file grows ~33 MB. Multiple manifests in
  one project compound. Not catastrophic but no defensive cap exists.
- **Source:** `internal/parse/manifest.go:70-83` — no per-manifest cap
  on `out []store.Symbol` length.
- **Suggested fix:** Cap manifest extractors at a sane limit (suggest
  10k deps per manifest file). Reject with a ParseFailure beyond.
  Mirror in `ExtractCargoToml`, `ExtractGoMod`, `ExtractPomXml`.

---

### F8 — LOW: Default file perms unchanged (security-1 F7 still open)

- **Severity:** low
- See F5 above. Listed separately to keep the security-1 finding
  numbering trail visible.

---

### F9 — LOW: `LEONARD_PYTHON` / `LEONARD_RUST_EXTRACTOR` / `LEONARD_TREESITTER_EXTRACTOR` env vars are PATH-shim vectors

- **Severity:** low (same as security-1 F6 — Unix exec model, can't
  fully defend against a tampered PATH)
- **Observed at v0.45.1:**
  - `LEONARD_PYTHON` → `internal/parse/python.go:43` — honored, no
    absolute-path enforcement, no warning logged.
  - `LEONARD_RUST_EXTRACTOR` → `internal/parse/rust.go:69` — honored,
    stat-checked but not abs-checked.
  - `LEONARD_TREESITTER_EXTRACTOR` → `internal/parse/treesitter.go:66`
    — honored, stat-checked but not abs-checked. **New since v0.7.**
- **Threat-model fit:** A malicious project ships
  `./.leonard/leonard-extract-treesitter` (executable) along with a
  README that tells the user "for performance, run
  `LEONARD_TREESITTER_EXTRACTOR=./.leonard/leonard-extract-treesitter
  leonard index`." That's not a Leonard defect — it's social
  engineering. But the env-var override is now triply replicated (3
  binaries instead of 2 since v0.7), and the README mentions all three
  without a security note.
- **Suggested fix:** Same as security-1 F6 (1) document the env vars
  as security-sensitive knobs in README's Security section, (2) refuse
  relative paths, (3) log the resolved absolute path on the first
  invocation per process. Add the third env var to the documentation.

---

### F10 — LOW: NotebookEdit PostToolUse events return exit 2 (Claude Code sees "block this call")

- **Severity:** low (usability, not security per se — but it's a
  denial-of-service against legitimate workflows)
- **Reproducer:**
  ```bash
  echo '{"session_id":"x","hook_event_name":"PostToolUse",
        "tool_name":"NotebookEdit",
        "tool_input":{"notebook_path":"/tmp/test.ipynb"},
        "cwd":"/tmp/probe"}' \
    | leonard-hook post-edit
  # → leonard-hook: post-edit: hooks: payload decode error:
  #     tool_input.file_path missing from PostToolUse payload
  # exit: 2
  ```
- **Observed:** `post_edit.go:189-191` rejects payloads where
  `tool_input.file_path` is empty (the `notebook_path` field is unread
  on post-edit — pre-edit handles it, post-edit doesn't). exit 2 is
  mapped via `blockOnDecode` (exit.go:27) to "block this tool call" in
  Claude Code's hook contract. Result: any user running Claude Code
  against a Jupyter project gets every NotebookEdit blocked by Leonard.
- **Threat-model fit:** This is the inverse of a security finding — an
  *over-eager* block that interrupts legitimate use. Worth flagging
  alongside the security stuff because it's a real launch-readiness
  issue.
- **Suggested fix:** In post-edit's payload decode, fall back to
  `tool_input.notebook_path` when `file_path` is empty, same as the
  pre-edit handler does at pre_edit.go:174-180. Then `.ipynb` flows
  through IndexFile's existing extractor (`ExtractJupyter`).

---

### F11 — LOW: Symlinked `.leonard/` enables intentional cross-project sharing (no warning)

- **Severity:** low (requires user action — symlink doesn't appear
  spontaneously)
- **Reproducer:**
  ```bash
  mkdir -p /tmp/sec2-shared-store
  mkdir /tmp/sec2-projA && ln -s /tmp/sec2-shared-store /tmp/sec2-projA/.leonard
  mkdir /tmp/sec2-projB && ln -s /tmp/sec2-shared-store /tmp/sec2-projB/.leonard
  cd /tmp/sec2-projA && leonard init .
  cd /tmp/sec2-projB && leonard init .
  # → both projects share the same DB. Decisions from A leak to B, etc.
  ```
- **Observed:** Leonard follows the `.leonard` symlink. `init` reuses
  the existing DB (idempotent migration), so both project roots end up
  reading/writing the same SQLite file. The MCP server in project B
  surfaces project A's decisions and claims.
- **Threat-model fit:** Information disclosure across project
  boundaries — but the disclosure is a direct consequence of the user
  having created the symlink, which is "shoot yourself in the foot"
  rather than attacker-controlled.
- **Suggested fix shape:** Warn in `leonard doctor` when
  `os.Lstat(.leonard).Mode()&os.ModeSymlink != 0`. Document the
  caveat in README.

---

### F12 — INFORMATIONAL: Personal email + paths in committed history

- **Severity:** informational
- **Observed:**
  - Every commit's `Author:` is `jasondillingham <jasonmdillingham@gmail.com>`
    — that's the maintainer's personal Gmail. If the repo is pushed
    public, it's already on GitHub. Decision is on the maintainer; not
    a defect.
  - `audits/` directory contains 28 occurrences of
    `$HOME/` (e.g.,
    `audits/bughunt-3-eval-framework.md:27`,
    `audits/bughunt-2-pre-edit.md:35`). These are reproducer commands
    from past audits. Not credentials — but they hard-code the
    maintainer's home dir, which is identifying information.
  - `audits/bughunt-4-path-trust-deep.md:48` mentions
    `/Users/victim/.aws/credentials.go` — placeholder, not a real
    credential.
- **Threat-model fit:** Informational only — paths leak the
  maintainer's macOS username. No actual credentials, no PII beyond
  the commit author. Worth noting for the launch-readiness pass.
- **Suggested fix shape:** None required. If hardening: rewrite audit
  reproducers to use `$HOME/...` instead of literal `$HOME/...`.

---

### F13 — INFORMATIONAL: 0.0.x-pinned tree-sitter grammars (`dart`, `nix`) have minimal maintainer track records

- **Severity:** informational (compounds F3)
- **Observed:** Of the 28 tree-sitter grammars in `Cargo.toml`:
  - `tree-sitter-dart = "0.0.4"` — last release predates security
    review; small maintainer base.
  - `tree-sitter-nix = "0.0.2"` — same shape.
  - `tree-sitter-just = "0.2"` — pre-1.0, two-person maintainer.
  - `tree-sitter-solidity = "1.2"` — owned by NomicFoundation, OK.
  - `tree-sitter-kotlin-ng = "1.1"` — community fork (the original
    `tree-sitter-kotlin` is unmaintained), worth tracking.
  - `tree-sitter-zig = "1.1"` — maxxnino, hobbyist.
- **Threat-model fit:** Supply chain. A grammar with 1 contributor +
  rare releases is more likely to be hijacked via expired email →
  password reset on crates.io. Each grammar runs a generated `build.rs`
  that compiles C code — so a hijack runs arbitrary code at every
  Leonard build / CI run.
- **Mitigation factor:** Cargo's checksum verification + Cargo.lock
  pins (when committed — see F3) eliminate the silent-version-bump
  vector. Without Cargo.lock, semver-range pins allow point-release
  updates within the same minor.
- **Suggested fix:** Subscribe to crates.io publish alerts via
  rustsec.org for the lower-maintenance grammars; pin to exact
  versions in `Cargo.toml` (e.g. `tree-sitter-dart = "=0.0.4"`) so
  even semver-range matching can't accept a fresh point release.
- **Out of scope:** Auditing each grammar's build.rs.

---

### F14 — INFORMATIONAL: Python helper trusts host `python3`

- **Severity:** informational
- **Observed:** `internal/parse/python.go:43` resolves `python3` from
  `LEONARD_PYTHON` or `exec.LookPath("python3")`. The helper does
  `ast.parse` only (no `exec`/`eval`), but the **host's Python
  interpreter** is whatever's on PATH — which, on uv / pyenv-managed
  systems, could be a Python from an arbitrary `.python-version` file
  or a virtualenv shim.
- **Threat-model fit:** The Unix exec model — Leonard isn't going to
  fix the entire pyenv/uv ecosystem's PATH-shimming model. The
  helper script itself is safe (`ast.parse` is non-executing). What
  the **interpreter** does at startup (running site-packages init,
  loading `sitecustomize.py`, etc.) is in scope of whatever Python
  you've configured.
- **Suggested fix:** Document in README's Security section: "Leonard
  invokes `python3` via PATH (or `$LEONARD_PYTHON`). Site init
  packages on the host Python will load at extractor startup. If
  this matters to you, set `LEONARD_PYTHON` to a pristine system
  python."

---

### F15 — INFORMATIONAL: MCP tool inputs cleanly enforce `MaxClaimEvidenceBytes`

- **Severity:** verified-safe
- **Observed:** `internal/mcp/claims.go:90-91` rejects
  `len(in.Evidence) > maxClaimEvidenceBytes` (256 KiB). The
  store-side `RecordClaim` doesn't double-check — but the MCP layer
  is the only public surface, and the CLI doesn't expose claim-record.
  Confirmed that submitting a 512 KiB evidence string via the MCP
  tool returns the documented error.
- **Closes security-1 open question:** "Where should size caps live:
  MCP layer, store layer, or both?" — MCP only, post-Bughunt-4. The
  store doesn't accidentally bypass because there's no other entry
  point.

---

### F16 — INFORMATIONAL: Stdin JSON line filter is robust against NUL bytes, malformed UTF-8, very long single lines

- **Severity:** verified-safe
- **Reproducer:** See `cmd/leonard-mcp/stdin_filter.go` lineReader.
  Tested:
  - 1 line containing only `\x00garbage` → dropped (`looksLikeJSONRPC`
    returns false). Server continues.
  - `\xff\xfe malformed utf8` line → dropped, server continues.
  - 8 MiB single-line JSON-RPC under cap → passes the filter (SDK
    rejected for schema reasons, expected).
  - 24 MiB single-line over cap → `errOversize` triggers, line dropped
    with stderr message, server continues processing next line.
- **Closes security-1 F12:** v0.13's `lineReader` replacement
  successfully recovers past oversize lines (Bughunt-4 caps F1
  documented in the source). Verified at v0.45.1.

---

### F17 — INFORMATIONAL: CI workflow `pull_request` trigger runs cargo build of attacker-controlled code on fork PRs

- **Severity:** informational (acceptable risk given fork-PR norms)
- **Observed:** `.github/workflows/ci.yml:6-7` — `on: pull_request:
  branches: [main]`. GitHub Actions on a fork PR runs in a sandbox
  without access to repo secrets — `GITHUB_TOKEN` is read-only on
  the contents scope.
- **Threat-model fit:** A fork PR could change Cargo.toml /
  Cargo.lock (when committed — see F3) to a malicious grammar, and
  CI's `cargo build --release` would execute the build.rs. Network
  access from the runner is unrestricted, so DNS/HTTP exfiltration
  to attacker.example is possible. However, GitHub-hosted runners
  give a fork-PR no secrets — only the public repo's contents +
  ephemeral runner state.
- **Mitigation factor:** Standard GitHub Actions hygiene
  (`GITHUB_TOKEN` is per-job, scoped read for fork PRs). What an
  attacker can extract is essentially "verification that this
  repo's CI runs" — which is public anyway.
- **Suggested fix:** None. The CI doesn't sign releases, push to
  package registries, or hold any secret beyond `GITHUB_TOKEN`. If
  the repo gains release-signing keys, restrict `pull_request` to
  the `lint+vet+test` jobs and gate `release` jobs behind a
  `workflow_call` from a signed commit.

---

### F18 — INFORMATIONAL: `Co-Authored-By` trailers — only the anthropic noreply address

- **Severity:** verified-safe
- **Observed:** `git log --all --format='%(trailers:key=Co-Authored-By,valueonly)' | sort -u` —
  one unique value: `Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
  No real human emails in Co-Authored-By trailers.

---

### F19 — INFORMATIONAL: yaml.v3 + encoding/xml are XXE/billion-laughs hardened

- **Severity:** verified-safe
- **Reproducers:**
  - Billion-laughs YAML in `openapi.yaml` → yaml.v3 rejects with
    `excessive aliasing` error. ExtractOpenAPI surfaces a ParseFailure;
    no resource exhaustion.
  - DOCTYPE-with-`<!ENTITY xxe SYSTEM "file:///etc/passwd">` in
    `pom.xml` → Go's `encoding/xml` rejects with `invalid character
    entity &xxe;`. No file read.
- **Closes:** Category C #11 manifest-extractor parser hardening.

---

### F20 — INFORMATIONAL: `leonard claims resolve <id>` is CLI-only, not MCP-exposed

- **Severity:** verified-safe
- **Observed:** Grep across `internal/mcp/` for `ResolveClaim` returns
  zero matches. The only call site is `cmd/leonard/claims.go:49` (the
  cobra subcommand). Claude can't invoke this via the MCP tool surface.
  The CLI's `--note` flag is capped at 4 KiB (store.go:1131-1134).

---

### F21 — INFORMATIONAL: `--version` argv parsing in leonard-mcp is bounded and safe

- **Severity:** verified-safe
- **Observed:** `cmd/leonard-mcp/main.go:35-45` — fixed-set switch on
  `os.Args[1]` BEFORE the MCP run loop. No file reads, no DB opens, no
  network. The `--help` branch prints to stderr only. Returns cleanly
  via `return`; the run loop never starts.

---

## Priority-ordered punch list

1. **CRITICAL — F1**: Fix the confused-deputy chain that lets Claude
   write `.leonard/config.toml` and then escalate to RCE via
   `[post_edit.verify].command`. Strongest fix: pre-edit guard `.leonard/*`
   paths regardless of extension. Alternative: per-user allowlist of
   projects where `[post_edit.verify]` is honored.
2. **HIGH — F2**: Add path-trust gate on `[post_edit.verify].working_dir`
   in `MakeShellRunner`. Reject paths that resolve outside the project
   root.
3. **MEDIUM — F3**: Commit `Cargo.lock` files for both Rust crates;
   add `--locked` to the CI workflow's cargo invocations. Drop the
   `Cargo.lock` line from both `.gitignore`s.
4. **MEDIUM — F4**: Lower `maxIndexedFileBytes` to 1-2 MiB, OR add
   `RLIMIT_AS` to the tree-sitter helper subprocess to cap helper RSS
   at ~512 MiB. Document the actual worst-case in the indexer.go
   comment.
5. **MEDIUM — F5**: Change `wire_real.go:23` from `0o755` to `0o700`
   for the data dir; change config save perms to `0o600`. Add a
   `leonard doctor` warning for legacy world-readable installs.
6. **MEDIUM — F6**: Add a clone-time warning. On `leonard init` against
   a project where `.leonard/config.toml` already contains a
   `[post_edit.verify].command`, refuse to honor it until the user
   opts in via `leonard config trust .` (or similar).
7. **MEDIUM — F7**: Add per-manifest dep cap (10k). Reject with a
   ParseFailure beyond.
8. **LOW — F9**: Document the three `LEONARD_*_EXTRACTOR` env vars in
   the README's Security section. Optionally refuse relative paths.
9. **LOW — F10**: Make NotebookEdit work end-to-end — fall back to
   `tool_input.notebook_path` in post-edit's payload decode.
10. **LOW — F11**: `leonard doctor` warning on symlinked `.leonard/`.
11. **INFORMATIONAL — F13**: Pin sketchy 0.x.y grammars to exact
    versions in `Cargo.toml` (`=0.0.4` etc.); subscribe to crates.io
    publishes for the low-maintainer-count grammars.
12. **INFORMATIONAL — F14**: README note on `python3` PATH-shim model.

---

## Probe scratch artifacts (cleaned)

- `/tmp/sec2-leonard-probe/` — RCE chain reproducer
- `/tmp/sec2-attacker-repo/` — clone-to-pwn reproducer
- `/tmp/sec2-symlink-target/`, `/tmp/sec2-symlink-probe/` — symlink-share probe
- `/tmp/sec2-yaml-bomb-probe/` — billion-laughs YAML probe
- `/tmp/sec2-xml-probe/`, `/tmp/sec2-xml2-probe/` — XXE + 50k-deps pom.xml probes
- `/tmp/sec2-big.rb`, `/tmp/sec2-nested.rb`, `/tmp/sec2-nested2.rb` — tree-sitter helper RSS probes
- `/tmp/sec2-pkg-probe/`, `/tmp/sec2-big-pkg.json` — 100k-dep package.json probe
- `/tmp/leonard-sec2`, `/tmp/leonard-hook-sec2`, `/tmp/leonard-mcp-sec2` — purpose-built binaries

All probe artifacts deleted at end of audit. Reproducers above will recreate any of them in a few seconds.
