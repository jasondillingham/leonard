# Bug Hunt #2 — pre-edit

## Summary

Audited `internal/hooks/pre_edit.go` end-to-end with focus on hooks F8 (the new sibling-package scan in `readSiblingPackages`), plus a regression sweep across `post_edit.go`, `session_start.go`, and `stop.go`. F8 itself is solid for the fix case it was added for — sibling-package fabrication that wasn't imported in the target file now blocks correctly. But the brand-new walk has several behavioral gaps that diverge from the rest of the codebase (different skip list from the indexer, ignores `.gitignore` and `config.toml [index] ignore`, conflates nested modules) and bites perf: a 10k-file Go module takes ~480ms per pre-edit invocation, well over the 200ms p95 budget, and the walk runs on *every* `Edit/Write/MultiEdit` regardless of whether the snippet has any unresolved sibling references. Several config knobs documented in DESIGN.md §4.6 (`block_on_fabricated_symbol`, sibling-scan ignores, `vet_timeout`, `evidence_cap`) are not plumbed from `config.toml` into the handlers, so users editing those keys see no effect.

Auxiliary findings on post-edit (F5 missing-file path doesn't reach the model + doesn't supersede prior claims) and session-start (no per-line cap on injected reasoning) follow.

## Findings

### F1 — Sibling-scan skip list diverges from the indexer's, including `dist/`, `build/` and missing `.gitignore`

- **Severity:** medium
- **Reproducer:**
  ```bash
  # Synthesize a project with build artifacts containing .go files
  P=/tmp/scan-skip-divergence
  mkdir -p "$P/dist/foo" "$P/build/bar" "$P/coverage/baz"
  cat > "$P/go.mod" <<'EOF'
  module example.com/scan-skip
  go 1.22
  EOF
  cat > "$P/main.go" <<'EOF'
  package main
  func main() {}
  EOF
  cat > "$P/dist/foo/x.go" <<'EOF'
  package distfoo
  func DistFn() {}
  EOF
  cat > "$P/build/bar/x.go" <<'EOF'
  package buildbar
  func BuildFn() {}
  EOF
  (cd "$P" && /Users/jasondillingham/go/bin/leonard init && /Users/jasondillingham/go/bin/leonard index)
  # Sibling-scan picks up distfoo and buildbar — but the indexer didn't index them
  payload='{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"'"$P/main.go"'","new_string":"distfoo.NonExistent()"},"cwd":"'"$P"'"}'
  echo "$payload" | (cd "$P" && /Users/jasondillingham/go/bin/leonard-hook pre-edit)
  # → deny on distfoo.NonExistent (sibling-scan resolved the alias)
  ```
- **Observed:** `internal/hooks/pre_edit.go:362` skips only `vendor`, `testdata`, `node_modules`, and dotted directories. The indexer at `internal/index/indexer.go:25-33` skips `{vendor, node_modules, dist, build, .git}` — a different set. Result:
  - `dist/` and `build/`: pre-edit registers a sibling alias for any package found there, but the symbol DB has no symbols from those dirs because the indexer skipped them. Every reference to a `dist/`-borne package name is reported as fabricated, even references to symbols that exist in the source tree of the dist artifact.
  - `testdata/`: pre-edit skips it, but the indexer *does* index it. Symbols defined in `testdata/` are in the store; sibling-scan won't register the alias, so a test fixture referencing a `testdata/`-borne package by name falls through to allow without checking the store. (Smaller risk — testdata usually isn't referenced from production code.)
  - Neither `.gitignore` nor `.leonardignore` is consulted. The indexer reads both (`internal/index/indexer.go:274-293`).
  - `config.toml [index] ignore` (default `["vendor/", "node_modules/", "dist/", "build/"]`) is not read by the sibling scan either; a user customising it to add `legacy/` to the ignore list still has `legacy/`'s packages contribute aliases to pre-edit.
- **Expected:** Sibling-scan and the indexer should agree on the universe of "in-module packages." Either share the indexer's `defaultSkipDirs` + `loadIgnore` logic, or call into the indexer directly (consult the `files` table for "what packages does the store know about?" — that's what HasSymbol queries anyway).
- **Suggested fix shape:** Plumb `internal/index.defaultSkipDirs` and `loadIgnore` into the sibling walker. Or, simpler, derive the alias map from the store's `files` table — every file the indexer learned about has a known directory, the package name can be the directory's `package` declaration. That eliminates the walk entirely and keeps the two paths consistent by construction.
- **Out of scope:** Verifying that the indexer's `defaultSkipDirs` is itself complete (already audited in Round 1).

### F2 — Sibling-scan crosses nested `go.mod` module boundaries

- **Severity:** medium
- **Reproducer:**
  ```bash
  # Outer workspace go.mod + inner per-service go.mod (a common monorepo layout)
  P=/tmp/nested-gomod
  rm -rf "$P"
  mkdir -p "$P/svc-a" "$P/svc-b/lib"
  cat > "$P/go.mod" <<'EOF'
  module example.com/workspace
  go 1.22
  EOF
  cat > "$P/svc-a/go.mod" <<'EOF'
  module example.com/svc-a
  go 1.22
  EOF
  cat > "$P/svc-a/main.go" <<'EOF'
  package main
  func main() {}
  EOF
  cat > "$P/svc-b/go.mod" <<'EOF'
  module example.com/svc-b
  go 1.22
  EOF
  cat > "$P/svc-b/lib/b.go" <<'EOF'
  package blib
  func BFn() {}
  EOF
  mkdir -p "$P/.leonard"
  (cd "$P" && /Users/jasondillingham/go/bin/leonard init && /Users/jasondillingham/go/bin/leonard index)
  payload='{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"'"$P/svc-a/main.go"'","new_string":"blib.NoSuch()"},"cwd":"'"$P/svc-a"'"}'
  echo "$payload" | (cd "$P/svc-a" && /Users/jasondillingham/go/bin/leonard-hook pre-edit)
  # → deny on blib.NoSuch — even though svc-a can't import svc-b/lib (separate module)
  ```
- **Observed:** `readSiblingPackages` (pre_edit.go:351) walks the entire `moduleRoot` tree without checking for nested `go.mod` files. With a workspace root that has its own `go.mod`, the sibling scan happily attributes every `.go` file under it to `<outer-module>/<rel-path>`, including files that belong to inner submodules. In the reproducer, `svc-b/lib/b.go` (real path `example.com/svc-b/lib`) is registered as `example.com/workspace/svc-b/lib`. The `isTrackedImport` check passes (prefix matches outer module), so `blib.NoSuch()` triggers a fabrication block even though `svc-a` literally cannot import `svc-b`'s code in Go's module system.
- **Expected:** The walk should stop at any subdirectory containing its own `go.mod` (treat each module's tree as a closed unit). For workspace layouts with no outer `go.mod`, `defaultModulePath` returns `""` and the scan is already skipped — that case is correct.
- **Suggested fix shape:** Inside the `filepath.Walk` callback, when `info.IsDir() && path != moduleRoot`, check `os.Stat(filepath.Join(path, "go.mod"))`. If a `go.mod` exists, `return filepath.SkipDir`. Cheap (one stat per directory), preserves module isolation.
- **Out of scope:** Go workspace files (`go.work`) — they're a different mechanism (multiple modules visible in one workspace, but each still has its own `go.mod`). The above fix handles them correctly because each workspace member still has its own `go.mod` boundary.

### F3 — Sibling-scan walks even when the snippet imports nothing in-module, with measurable cost on large repos

- **Severity:** medium (perf)
- **Reproducer:**
  ```bash
  # 10k Go files — synthetic but representative of a real microservices monorepo
  P=/tmp/walk-perf
  mkdir -p "$P"
  cat > "$P/go.mod" <<'EOF'
  module example.com/walkperf
  go 1.22
  EOF
  cat > "$P/main.go" <<'EOF'
  package main
  func main() {}
  EOF
  for i in $(seq 1 100); do for j in $(seq 1 100); do
    mkdir -p "$P/sub_$i/pkg_$j"
    cat > "$P/sub_$i/pkg_$j/x.go" <<EOF
  package pkg_${i}_${j}
  func F() {}
  EOF
  done; done
  (cd "$P" && /Users/jasondillingham/go/bin/leonard init)
  # snippet that touches stdlib only — should not need the sibling scan at all
  payload='{"session_id":"s","tool_name":"Edit","tool_input":{"file_path":"'"$P/main.go"'","new_string":"_ = 1"},"cwd":"'"$P"'"}'
  for i in 1 2 3; do
    start=$(date +%s%N)
    echo "$payload" | (cd "$P" && /Users/jasondillingham/go/bin/leonard-hook pre-edit >/dev/null)
    end=$(date +%s%N)
    echo "$(( (end - start) / 1000000 ))ms"
  done
  ```
- **Observed:**
  - 10k-file project: cold ~625ms, warm ~480ms per pre-edit invocation.
  - 6.8k-file Go stdlib (`/usr/local/go/src`): cold ~450ms, warm ~100ms.
  - 1.1k-file project (`bosun`): 10-30ms.
  - 80-file project (`leonard` itself): 10-15ms.
  - The walk runs unconditionally regardless of whether the snippet has any unresolved selector references. The reproducer's snippet (`_ = 1`) has zero selector expressions, so the entire walk's work is thrown away.
- **Expected:**
  - Sibling-scan should only run when the snippet has at least one selector `pkg.X` whose `pkg` ident isn't resolved by the snippet's own imports or the target file's imports. (For `_ = 1`, no walk is needed.)
  - On large repos, a single pre-edit at p95 should be well under 200ms. The phase-3 brief calls out p95 ≤ 200ms for hook turnaround.
- **Suggested fix shape:**
  1. Lazy scan: defer `readSiblingPackages` until after parsing the snippet and collecting unresolved selector idents. If none, skip the walk entirely.
  2. Bound the walk: cap on number of `.go` files visited (say 5k); if exceeded, log and fall back to the pre-F8 behavior (allow on unresolved). The handler must never deadline a real edit.
  3. Cache: keyed by `(moduleRoot, mtime of root dir)`, persisted to `.leonard/sibling_cache.json`. Cross-invocation caching turns warm cost from 480ms to a single stat. Invalidate when any directory mtime under the root changed since the cached snapshot. This is the only way to actually hit the budget on 10k-file repos.
- **Out of scope:** Whether the walker can be parallelised — Go's `filepath.Walk` is single-threaded. A `godirwalk`-style concurrent walker would close the gap further, but the cache fix is more impactful and avoids a new dependency.

### F4 — Config knob `block_on_fabricated_symbol` is dead

- **Severity:** medium
- **Reproducer:** Search the codebase:
  ```bash
  grep -rn 'BlockOnFabricatedSymbol' /Users/jasondillingham/Documents/Homelab/leonard/
  # Only matches: the struct field definition and the Default() initializer
  ```
- **Observed:** `internal/config/config.go:45` defines `BlockOnFabricatedSymbol bool` and `Default()` sets it to `true`. `leonard init` writes the field to `.leonard/config.toml`. But nothing reads it — neither `cmd/leonard-hook/pre_edit.go` nor `internal/hooks/pre_edit.go` consults `config.LoadOrDefault` at all. A user who sets `block_on_fabricated_symbol = false` in config.toml expecting to disable the guard sees no change in behavior.
- **Expected:** The config key should toggle the guard. If `false`, `HandlePreEdit` should emit `allowResponse()` regardless of snippet content.
- **Suggested fix shape:** Mirror the session-start / stop pattern. In `cmd/leonard-hook/pre_edit.go`, call `config.LoadOrDefault` and pass `cfg.Hooks.BlockOnFabricatedSymbol` through `PreEditOptions.Enabled` (new field). In `HandlePreEdit`, short-circuit to allow when disabled.
- **Out of scope:** Whether `false` should still log/record the would-be-blocked attempt — that's a UX decision for Jason.

### F5 — `VetTimeout` and `EvidenceCap` aren't tunable from `config.toml`

- **Severity:** low
- **Reproducer:** Search the codebase:
  ```bash
  grep -rn 'VetTimeout\|EvidenceCap\|vet_timeout\|evidence_cap' /Users/jasondillingham/Documents/Homelab/leonard/internal/config/ /Users/jasondillingham/Documents/Homelab/leonard/cmd/
  # Only matches: hooks.PostEditOptions fields and the default constants
  ```
- **Observed:** `PostEditOptions.VetTimeout` (default 30s, post_edit.go:143) and `PostEditOptions.EvidenceCap` (default 16 KiB, post_edit.go:146) are wire-only — never read from `config.toml`. The pelletier round-trip in `Default()` doesn't have keys for them either, so a user with a slow `go vet` (CGo, lots of packages, network-mounted GOPATH) has no way to extend the deadline short of editing source and rebuilding the binary.
- **Expected:** Either both are tunable via `[hooks]` config keys, or the design explicitly says "compiled-in only" and the field is removed from `PostEditOptions`.
- **Suggested fix shape:** Add `VetTimeoutSeconds int` and `EvidenceCapBytes int` to `HooksConfig`; default 30/16384; plumb through `cmd/leonard-hook/post_edit.go`. Bonus: `vet_timeout_seconds = 0` could mean "skip vet entirely" for projects that maintain their own pre-commit hook.
- **Out of scope:** A "skip vet for non-Go edits" config knob — that already happens automatically via `hasGoModule`.

### F6 — Sibling-scan first-wins semantics are silent on collisions

- **Severity:** low
- **Reproducer:**
  ```
  # Two sibling dirs with the same package name
  $WORK/E/aaa/util/u.go   → package util; func AaaUtil() {}
  $WORK/E/zzz/util/u.go   → package util; func ZzzUtil() {}
  ```
  Snippet `util.AaaUtil()` resolves; snippet `util.ZzzUtil()` also resolves; snippet `util.NoSuch()` is correctly blocked. Each works because the store's `HasSymbol` is name-only — the *path* the sibling-scan picked doesn't matter for the deny.
- **Observed:** `readSiblingPackages` (pre_edit.go:388) uses first-wins (`if _, exists := out[pkgName]; !exists`). Walk order is alphabetical, but that's a `filepath.Walk` implementation detail, not a documented contract. So the resolved import path is essentially nondeterministic from the user's perspective.
- **Expected:** Same — first-wins is fine because the store check is name-only. But the false-negative documented in `defaultPackageAlias`'s comment (snippet refs `bar.SharedName()`, only `foo.SharedName` exists; allowed because name matches) is exacerbated when two packages share a name: the snippet can be referring to either, and only one of them is the "first" in walk order. There's nothing the user can do to influence which.
- **Suggested fix shape:** When `pkgName` is already in the map, drop the entry entirely (record `nil` or remove): "ambiguous, sibling scan can't help, fall back to allow on unresolved." This trades a little extra fabrication potential for predictability. Alternatively, record both paths and check both — but the store doesn't carry package context, so this is moot until/unless the schema gains a package-path column.
- **Out of scope:** A broader symbol-DB schema change to track (package, name) pairs — that's beyond v0.

### F7 — Pre-edit guard doesn't see fabricated bare names from dot-imports

- **Severity:** informational
- **Reproducer:**
  ```go
  // target.go
  package x
  import . "github.com/jasondillingham/leonard/internal/store"
  // snippet replaces some body:
  PhantomFunc()
  ```
- **Observed:** The guard is selector-only (`*ast.SelectorExpr`). A snippet that uses `PhantomFunc()` directly (bare Ident, dot-import in effect) walks through the AST as a plain `*ast.Ident` and falls through. So a fabricated symbol introduced via dot-import bypasses the guard.
- **Expected:** Dot imports are rare in idiomatic Go (gomega/gomock/etc.) — accepting this gap is reasonable. But it should be documented in the handler comments.
- **Suggested fix shape:** When the target file (or snippet) has a dot-import on a tracked package, also check bare Ident calls inside the snippet. Risk: lots of false positives on identifiers that look like names but are actually local. Probably not worth it for v0.
- **Out of scope:** Implementing dot-import handling.

### F8 — `defaultPackageAlias` false-negative when package name != directory name AND the snippet uses the *dir* name

- **Severity:** informational
- **Reproducer:**
  ```
  $WORK/F/dirname/x.go   → package mismatched; func Real() {}
  ```
  Snippet `dirname.NoSuch()` — neither the sibling scan (which sees `mismatched`, not `dirname`) nor the target file's imports register `dirname` as an alias. So the snippet falls through to allow.
- **Observed:** Confirmed: when pkg-name != dir-name, only the pkg-name maps via sibling scan. Snippet using the directory-name receiver is silently allowed even if the symbol is fabricated.
- **Expected:** v0-acceptable. Go convention is that pkg-name matches the last segment of the dir path; exceptions (testfixture, godoc, etc.) are rare.
- **Suggested fix shape:** Optionally also register `filepath.Base(filepath.Dir(path))` as a fallback alias. Risk: collisions with the pkg-name map. Probably not worth it.
- **Out of scope:** —

### F9 — Sibling-scan registers packages from files with `//go:build never` constraints

- **Severity:** low
- **Reproducer:** A package whose only file is build-constrained:
  ```go
  // lib/lib_ignore.go
  //go:build never
  package lib
  func IgnoreFn() {}
  ```
- **Observed:** `parser.ParseFile` with `PackageClauseOnly` doesn't evaluate build tags. The walker registers `lib` as a sibling. The indexer also walks the file and emits its symbols (the indexer doesn't evaluate build tags either). So `lib.IgnoreFn()` is allowed by pre-edit — but `go build` would reject it because no file satisfies the constraint in the active build mode.
- **Expected:** The pre-edit guard is best-effort, and `go vet` in the post-edit hook would catch the build error anyway. Accepting this is fine.
- **Suggested fix shape:** None — let post-edit catch it.
- **Out of scope:** Cross-validating with build-tag evaluation; that requires `go/build.Context`.

### F10 — Post-edit missing-file path doesn't reach the model (regression risk for F4)

- **Severity:** medium
- **Reproducer:** Construct a PostToolUse payload pointing at a file that doesn't exist on disk (because user vetoed the write, or because the path was bogus). The handler in `internal/hooks/post_edit.go:174-176` short-circuits to `handleMissingFile`.
- **Observed:** `handleMissingFile` (post_edit.go:234-257):
  - Records a claim with `Verified: false`, `IndexOK: nil`, `VetOK: nil`. Good.
  - Emits a `HookResponse` with `SystemMessage: "leonard: ... file not found, skipping re-index"`. Good — operator sees something.
  - **Does NOT set `HookSpecificOutput.AdditionalContext`.** The model is left believing its write took effect. Hooks F4 fixed exactly this gap for vet-fail; the missing-file path needs the same treatment or Claude continues onto its next instruction assuming the write landed.
  - **Does NOT call `SupersedeClaimsForFile`.** A prior unverified claim for the same file (say, a vet-fail from an earlier edit) stays in the ledger alongside the new "file not found" claim. Both surface at Stop time.
- **Expected:** When PostToolUse fires for a file that doesn't exist, the model should see something equivalent to "Leonard post-edit: <file> wasn't found on disk after your tool call — the write likely didn't land. Re-check or try again." That's the same shape as the F4 vet-failure callout.
- **Suggested fix shape:** Inside `handleMissingFile`, build a `PostToolUseSpecificOutput` with `AdditionalContext` describing the missing file. Skip supersession — a missing-file event isn't a fix for anything, so keep the prior failure claim alive.
- **Out of scope:** Whether the missing-file detection should also short-circuit if the file just hasn't been created yet (Write to a brand-new path that succeeded). The current stat check happens BEFORE the indexer runs; in production the stat fires after Claude Code has actually written the file, so a brand-new path normally exists by then. But this assumption isn't tested.

### F11 — Session-start injected context has no per-line cap, can balloon

- **Severity:** low
- **Reproducer:** Record a decision with a 10 KB single-line reasoning:
  ```bash
  /Users/jasondillingham/go/bin/leonard decisions record --topic "x" --choice "y" --reasoning "$(yes A | head -c 10000)"
  ```
  Then trigger SessionStart. The injected `additionalContext` includes the full 10 KB string inline.
- **Observed:** `formatDecisions` (session_start.go:159-173) inlines `firstNonEmptyLine(d.Reasoning)` with no length cap. With `DefaultDecisionsLimit = 10`, ten 10 KB reasons = 100 KB of injected system context.
- **Expected:** The Stop hook applies `stopClaimPrefixMax = 120` rune cap (stop.go:70, 145-154). Session-start should apply a comparable cap on each decision line — say, 200 runes per bullet — so a chatty reasoning field can't dominate Claude's session-start context window.
- **Suggested fix shape:** Apply `truncatePrefix` (already in stop.go) to the reason line; consider exporting it to a shared helper or defining a package-private equivalent in session_start.go. Pick a longer cap (200-300 runes) so meaningful reasoning still surfaces, but no single decision can be a wall of text.
- **Out of scope:** Total-payload cap — separate concern, but a related one. Claude Code's documented `additionalContext` payload limit isn't published anywhere I could find. Worth checking in the docs before sinking effort into a total cap.

### F12 — `vet` exit error string in `vet_error_summary` leaks process detail when output is header-only

- **Severity:** low
- **Reproducer:** Construct a vet run that fails with output that's entirely `# pkg` headers (e.g., `go vet` of a package with stale build artifacts).
- **Observed:** `vetErrorSummary` (post_edit.go:346-373) falls back to `vet.ExitErr` when no actionable line is found. `ExitErr` is the stringification of `exec.Command.Run`'s error — typically `"exit status 1"` but on a context-deadline can become `"signal: killed"` or similar process-detail strings. Those land in the claim row's `vet_error_summary` column as opaque process artifacts rather than something a human can act on.
- **Expected:** A human-readable fallback like "go vet returned non-zero with no parseable output" rather than `exit status 1`. The `signal: killed` case would be especially confusing because it implies an OOM kill when it's actually a timeout.
- **Suggested fix shape:** When falling through to ExitErr, rewrite known patterns:
  - `errors.Is(err, context.DeadlineExceeded)` → `"go vet timed out after Ns"`.
  - `strings.Contains(err.Error(), "signal: killed")` → `"go vet exited via signal (timeout or OS kill)"`.
  - `strings.HasPrefix(err.Error(), "exit status")` → `"go vet exited non-zero with no parseable diagnostics"`.
- **Out of scope:** Whether the indexer should retry vet after a timeout. Per the brief, vet shouldn't grow into a coordination layer.

### F13 — Hook-config plumbing pattern is ad-hoc; pre-edit isn't called even when it should be

- **Severity:** informational
- **Reproducer:** Compare `cmd/leonard-hook/session_start.go:62` and `cmd/leonard-hook/stop.go:61` (both call `config.LoadOrDefault`) vs `cmd/leonard-hook/pre_edit.go` (no config call at all) vs `cmd/leonard-hook/post_edit.go` (no config call at all).
- **Observed:** Two of four hooks read the config file; the other two don't. There's no shared "load config + project root" helper. As new tunables get added (per F4, F5), each hook will need its own ad-hoc plumbing.
- **Expected:** A single `loadHookConfig(root) (config.Config, error)` helper used by all four hooks. Reduces drift when the schema changes.
- **Suggested fix shape:** Add `loadHookConfig` to a shared `cmd/leonard-hook/config.go`. Have all four hook cobra commands call it; pass the relevant slice of fields into each handler.
- **Out of scope:** Per-project vs. user-level config inheritance — out of v0 scope per the brief.

## Things that worked

Behaviors I verified are correct:

- **F8 fix itself:** sibling-scan correctly blocks `lib.NonExistent()` when `lib` is an in-module sibling but not imported in the target file. Real `lib.RealOne()` correctly passes. Confirmed via repro at the cobra layer (`/Users/jasondillingham/go/bin/leonard-hook pre-edit`) on a synthesized project.
- **F8 + explicit-import precedence:** when the target file or snippet imports `lib` aliased to an external package, the sibling-scan's `lib` mapping is NOT clobbered — the explicit import wins. Verified in scenario K (`/tmp/leo-bh2/scenarios2.sh`).
- **F8 + main packages:** `package main` is correctly skipped in `readSiblingPackages` so a snippet like `main.NoSuch()` allows through. With `cmd/foo` + `cmd/bar` both being `package main`, neither registers (correct).
- **F8 + symlinks:** `filepath.Walk` doesn't follow symlinks by default, so circular dir-level symlinks, in-tree dir->dir loops, and out-of-tree symlinks all behave (no walk loop, no walk escape). Empirically confirmed at /tmp/leo-bh2/scenarios3.sh.
- **F8 + malformed Go files:** a junk .go file in the tree (not parseable) is silently skipped by `PackageClauseOnly` returning `perr != nil`. Walker continues with other files. Verified in scenario H.
- **F8 + comment-clause `package`:** files with `// package secret` in doc comments do NOT register `secret` as a sibling — `PackageClauseOnly` reads only the real clause. Verified in `/tmp/leo-bh2/scenarios3.sh` scenario D2.
- **F8 + permission-denied dir:** when a sub-directory is `chmod 000`, the walk silently swallows the err and continues. No panic. Verified in scenarios4.
- **F8 + generics (IndexExpr / IndexListExpr):** `lib.NoSuch[int]()` and `lib.Fab[int, string]()` both go through the SelectorExpr inside the IndexExpr, so the fabrication guard correctly catches them. Verified scenarios2 scenario J.
- **F8 + type assertions / type literals:** `var v T = lib.NoSuch(x)`, `x.(lib.NoSuch)`, `var x lib.NoSuch` all trip the guard via SelectorExpr in the AST. Verified scenarios6.
- **F6 (MultiEdit / NotebookEdit):** MultiEdit fabrication blocks on any element of `edits[]`. NotebookEdit on `.ipynb` correctly short-circuits at the .go suffix gate. Tests already cover these (pre_edit_test.go:246-323).
- **F5 (post-edit missing file):** the short-circuit does what its test claims — no index call, no vet call, claim recorded with IndexOK/VetOK = nil. (The model-visibility gap is filed separately as F10 above.)
- **F7 (session-start source gating):** `source: "compact"` and `source: "clear"` correctly skip injection without calling the decisions reader. Tests cover both. Empty/unknown sources fall through to inject.
- **F4 (post-edit vet-fail AdditionalContext):** the model-visible "go vet FAILED — do not claim done" callout still reaches Claude via `PostToolUseSpecificOutput.AdditionalContext`. The Stop hook's `systemMessage`-only surfacing doesn't interfere (different channel; Stop fires later anyway).
- **F8 + module-boundary safety net (intentional skip):** when `ModuleRoot == ""` (cmd-layer couldn't locate go.mod), sibling scan is short-circuited to empty map at pre_edit.go:353. Verified by removing go.mod and confirming pre-F8 behavior.
- **Cross-session claim supersession:** `SupersedeClaimsForFile` matches by `file_path` only, no `session_id` filter. A vet-fail recorded in session A is correctly superseded by a vet-pass in session B. Verified by reading `internal/store/store.go:799-818`.
- **Pre-edit binary execution path:** `cmd/leonard-hook/pre_edit.go:35` resolves project root via `resolveProjectRoot` (walks up looking for `.leonard/`). When the dir is missing, falls back to cwd. When the SQLite DB is missing, falls back to `permissiveStore` that approves everything — the right behavior for a fresh checkout.

## Open questions

1. **Sibling-scan budget on real-world large repos.** I tested synthetic 10k-file modules (~480ms) and the Go stdlib (6.8k files, ~100ms warm). I don't have access to Kubernetes/etcd-scale repos to confirm whether p95 stays bounded. The fix for F3 should target a documented budget — maybe 100ms warm, 300ms cold — and bench against k8s.io/kubernetes' 30k+ file tree.

2. **Decision-injection size limit in Claude Code.** F11 notes there's no documented Claude Code limit on `additionalContext` size. Empirically a few KB seems fine; a few MB clearly isn't. Without official guidance, the right cap for session-start injection is a guess.

3. **MultiEdit + NotebookEdit perf when `edits[]` is large.** Each entry triggers a fresh `parseSnippet` + AST walk. The sibling-scan runs once (correctly), but the per-edit AST work is O(edits) and could be slow for a 50-element MultiEdit. I didn't bench this.

4. **Whether `BlockOnFabricatedSymbol = false` should still record the would-be-block as a claim or decision for audit purposes.** F4 mentions this as a UX decision.

5. **Generated code with `package` clauses inside comments — but actually the comment goes BEFORE the package clause.** `parser.PackageClauseOnly` reads up through the package clause. I confirmed in scenario D2 that a `// package foo` comment doesn't get parsed as a clause. But: what if the file is `//go:generate` output that mis-emits the clause? Probably rare, didn't probe.

6. **Race condition between sibling-scan and indexer running on the same .go file.** Both might mid-walk during a parallel `leonard index` + Claude edit. The pre-edit walker calls `parser.ParseFile(..., nil, parser.PackageClauseOnly)` which opens the file for read. If the indexer is mid-write (it writes to the same DB, not source files), there's no conflict. But if the user has a code generator simultaneously writing .go files, the parser could read mid-write. Tolerable — `perr != nil` would skip that file silently. Not investigated.

## Scratch files (not committed)

All probe scripts and fixtures live under `/tmp/leo-bh2/`. The Go walk-benchmark stub is `/tmp/leo-bh2/walk_bench/main.go` (rebuilt as `/tmp/walk_bench`). Re-runnable but tied to those absolute paths — adjust before using in fix-round PRs.
