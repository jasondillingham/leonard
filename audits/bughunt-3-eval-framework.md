# Bug Hunt #3 — eval-framework

## Summary

Audited the Inspect eval at `evals/inspect/` end-to-end without spending API
credit. The framework registers cleanly (`inspect list tasks tasks.py` returns
all three) and the scorer wires through to the production `leonard-hook
pre-edit` as designed. But several wiring and methodology bugs would distort
results before the model gets a chance to perform — most notably the scorer
silently degrades to "everything passes" when invoked from a cwd outside the
Leonard tree, the `mcp` Python dependency is missing from `pyproject.toml` so
the treated arms fail to start, the `extract_go_code` regex misses three
common fence variants Claude actually emits, and the methodology conflates
"model honestly refused" with "model fabricated" in the headline number. The
$0.50–$2 cost estimate in the README is pessimistic by 2–5×.

## Findings

### F1 — Scorer silently degrades to "approve everything" when subprocess cwd is outside the Leonard repo

- **Severity:** high
- **Reproducer:**
  ```bash
  export PATH=$HOME/go/bin:$PATH  # or wherever leonard-hook is installed
  cd /tmp
  python3 -c "
  import sys; sys.path.insert(0, '<repo>/evals/inspect')
  from scoring import detect_fabrications
  code = '''package main
  import \"github.com/jasondillingham/leonard/internal/store\"
  func main(){ _ = store.NoSuchSymbolFabFab }
  '''
  fabs, raw = detect_fabrications(code, '<repo>')
  print('fabricated:', fabs); print('raw:', raw[:200])
  "
  ```
- **Observed:** `fabricated: []` and `raw: {"continue":true}`. The hook
  returned "allow" on a snippet referencing a clearly fabricated symbol.
- **Root cause:** `scoring.py:79` calls `subprocess.run([hook, "pre-edit"],
  …)` without `cwd=`. The hook's `resolveProjectRoot()` in
  `cmd/leonard-hook/post_edit.go:64` calls `os.Getwd()` and walks up looking
  for a `.leonard/` directory. From `/tmp` (or any path not under the Leonard
  tree) the walk-up fails, the hook falls back to the empty cwd as project
  root, finds no DB, and the `defaultPreEditOpener` (`cmd/leonard-hook/pre_edit.go:63`)
  silently substitutes a `permissiveStore` that reports every symbol as
  present. **Every snippet then scores 1.0 regardless of what the model
  wrote.** The JSON payload's `cwd` field is set to `PROJECT_ROOT`
  (`scoring.py:71`) but the hook never reads payload.CWD on the pre-edit path
  — only post_edit consults it.
- **Expected:** Scorer should produce the same result regardless of the
  Python process's cwd. The README documents `cd evals/inspect` which
  happens to walk-up into the repo so the bug doesn't bite the documented
  path — but any future caller (CI runner, helper script, Inspect's
  log-relocation behavior) that lands the subprocess in a non-repo cwd
  silently inverts the test.
- **Suggested fix shape:** Pass `cwd=project_root` to `subprocess.run`. Two
  lines. Optionally also have the hook honor the payload's `cwd` field as
  the second source of truth (so it matches the post_edit handler's
  behavior).
- **Out of scope:** A loud failure mode is preferable to permissive-fallback
  for this use case — the hook's design assumption (fresh repo before
  `leonard init`) doesn't apply when the scorer is the caller. Separate
  conversation.

### F2 — Treated arms fail to start: `mcp` Python package missing from pyproject.toml

- **Severity:** high
- **Reproducer:**
  ```bash
  cd <repo>/evals/inspect
  uv sync
  uv run inspect eval tasks.py@fabrication_with_leonard --model mockllm/model
  ```
- **Observed:**
  ```
  ERROR: MCP tools requires optional dependencies. Install with:
  pip install mcp
  ```
  Same for `fabrication_with_leonard_system_prompt`. The control arm runs
  fine. Two of three tasks are unreachable from the documented setup.
- **Expected:** `uv sync` + the README's command runs all three tasks end
  to end.
- **Root cause:** `mcp_server_stdio` from `inspect_ai.tool` requires the
  `mcp` extra at runtime (`inspect_ai/tool/_mcp/server.py:141` calls
  `verfify_mcp_package()`). `pyproject.toml` lists only `inspect-ai>=0.3.220`
  with no extras and no explicit `mcp` dep.
- **Suggested fix shape:** Add `"mcp"` (or `"inspect-ai[mcp]>=0.3.220"` if
  Inspect ships an extras marker — check current packaging) to the
  `dependencies` list, then re-`uv lock`. Update the README's "Install
  Inspect" step to mention it if relevant.
- **Out of scope:** Whether Inspect should bundle MCP support by default
  (upstream decision).

### F3 — `extract_go_code` regex misses common fence variants Claude emits

- **Severity:** medium
- **Reproducer:** the regex `r"```go\s*\n(.+?)\n```"` with `DOTALL`
  produces these results (run from `/tmp/test_regex.py`):
  | Input form                                  | Matches? |
  |---|---|
  | `` ```go\nfoo\n``` ``                       | yes (correct) |
  | `` ```golang\nfoo\n``` ``                   | **no**   |
  | `` ``` go\nfoo\n``` `` (space before lang)  | **no**   |
  | `` ```GO\nfoo\n``` `` / `` ```Go\n... ``    | **no**   |
  | `` ```go\nfoo``` `` (no \n before closing)  | **no**   |
  | `` ```go\r\nfoo\r\n``` `` (CRLF)            | yes but captures stray `\r` |
- **Observed:** A model that lang-tags its block as `golang` (Pygments
  convention, common in tutorials) or uses uppercase `GO`, or emits a tight
  one-liner with no final newline before the closing fence, scores 0.0 via
  `failure_mode: no_code_block`. That conflates "model didn't produce code"
  with "model produced code but in a slightly different fence form."
- **Expected:** Per the scorer's docstring (`scoring.py:42-49`), the design
  is "accept the ```go form only because that's what the sample prompts
  explicitly request." But Claude's sampling temperature plus its training
  on mixed `go`/`golang` fence styles means non-`go` lang tags are not rare
  — and they're a *meaningless* failure mode for what the eval is trying to
  measure.
- **Suggested fix shape:** Either (a) broaden the regex to
  `r"```(?:go|golang)\s*\n(.+?)\n```"` with `re.IGNORECASE`, plus a
  fallback that allows the closing fence at end-of-string with no preceding
  newline; or (b) leave the strict regex in place but tell the user in the
  README that a meaningful subset of "0.0 scores" might be lang-hint
  variance, not fabrication.
- **Out of scope:** Unfenced code recovery (the model emitting raw Go with
  no fence at all). That's a real instruction-following failure and
  scoring it as 0.0 is the right call.

### F4 — Brittle reason-string parser silently zeroes out fabrications if hook wording changes

- **Severity:** medium
- **Reproducer:**
  ```python
  FABRICATION_PREFIX = "blocked references to symbols not in the index:"
  # If pre_edit.go:482 changes "blocked references to symbols not in the index"
  # to e.g. "blocked references" or "rejected symbols",
  # then `if FABRICATION_PREFIX not in reason: return [], proc.stdout`
  # fires and the scorer returns an empty list.
  ```
- **Observed:** When the hook denies but the prefix substring isn't present,
  `detect_fabrications` returns `([], proc.stdout)`. The caller in
  `score()` treats empty list as "no fabrications" → score 1.0. A future
  refactor of the hook's `blockResponse` message wording (`pre_edit.go:481`)
  would silently flip every denied snippet to a perfect score. The contract
  between hook wording and scorer parsing is undocumented and unenforced.
- **Expected:** The scorer should treat "denied without parseable reason"
  as a scorer error, not as "no fabrications." Or use a structured field
  on the hook response (a dedicated `fabricatedReferences: []string`
  array in `PreToolUseSpecificOutput`) instead of marshalling a list into
  prose.
- **Suggested fix shape:** Short-term: change the `if FABRICATION_PREFIX
  not in reason: return [], proc.stdout` branch to raise / return a sentinel
  the scorer can convert to `failure_mode: scorer_error`. Long-term: add a
  structured field on the hook response so the scorer (and any other
  consumer) doesn't parse prose.
- **Out of scope:** Whether the hook's English reason should also be
  preserved for Claude Code to read — yes, it should; both can coexist.

### F5 — Hook approves package-qualified method references that are syntactically invalid Go

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/p.json <<'EOF'
  {"session_id":"x","hook_event_name":"PreToolUse","tool_name":"Write",
   "tool_input":{"file_path":"<repo>/_eval_scratch.go",
   "content":"package main\nimport \"github.com/jasondillingham/leonard/internal/store\"\nfunc main(){ _ = store.GetDecisions }"}}
  EOF
  leonard-hook pre-edit < /tmp/p.json
  # → {"continue":true}
  ```
- **Observed:** `store.GetDecisions` is a method on `*store.Store`, not a
  package-level function. `store.GetDecisions(...)` is not valid Go (you
  can only write `s.GetDecisions(...)`). The hook approves it because
  `HasSymbol("GetDecisions")` returns true regardless of symbol kind
  (`internal/store/store.go:420` `FindSymbolsByName` doesn't filter by
  kind, and `preEditSymbolAdapter.HasSymbol` just asks "does any symbol
  of this name exist?"). The eval can't distinguish a model that wrote
  callable Go from one that wrote `store.MethodName` because it sounds
  plausible.
- **Why it matters for the eval:** The `recent-decisions` sample explicitly
  asks for "package-qualified calls only (`store.X`, not method calls on
  the receiver)." Models that follow that instruction end up writing
  `store.GetDecisions(...)` (uncallable Go), and the hook approves it.
  The sample is asking the model to produce subtly wrong code, then
  scoring it correct. Same pattern in `indexer-construct` ("trigger an
  IndexAll pass" — IndexAll is a method).
- **Expected:** Either (a) the hook should validate the symbol is a
  package-level declaration (not a method) before approving; or (b) the
  sample prompts should be reworded to ask for honest receiver-method
  shape; or (c) the eval should document that it measures "did the model
  reference a name that exists somewhere in the index", which is weaker
  than "did the model write valid, callable Go."
- **Suggested fix shape:** Shortest path: reword `recent-decisions` and
  `indexer-construct` to ask for the natural receiver-method shape, and
  flag in the README that the eval doesn't catch receiver-method fabs
  (already partly documented in `samples.py:7-9` but not in README).
  Bigger fix: extend the hook's index check to filter by symbol kind so
  `pkg.MethodName` package-qualified references are *not* approved by
  the presence of a method of that name. That's a real semantic
  improvement to the production hook, not just an eval fix.

### F6 — `hook-fabrication-scan` trap sample doesn't actually trap

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/p.json <<'EOF'
  {"session_id":"x","hook_event_name":"PreToolUse","tool_name":"Write",
   "tool_input":{"file_path":"<repo>/_eval_scratch.go",
   "content":"package main\nimport \"github.com/jasondillingham/leonard/internal/hooks\"\nfunc main(){ _ = hooks.readSiblingPackages }"}}
  EOF
  leonard-hook pre-edit < /tmp/p.json
  # → {"continue":true}
  ```
- **Observed:** `hooks.readSiblingPackages` is unexported and *cannot* be
  called from outside `internal/hooks`, but the symbol *is* in the index
  (Leonard's parser indexes unexported symbols too). So a model that
  produces a snippet referencing the real-but-uncallable name scores 1.0,
  a model that fabricates a plausible exported name scores 0.0, and a
  model that honestly refuses ("you can't, it's unexported") scores 0.0
  because no ```go block was produced. The intended trap — "honest answer
  = best score" — is inverted: the *only* path to a 1.0 is to write
  uncallable code citing the real internal name.
- **Expected:** Per the comment at `samples.py:60-62`, the trap is
  designed to reward "you can't call it from outside the package" — but
  the scoring rubric assigns 0.0 to no-code-block answers regardless of
  honesty.
- **Suggested fix shape:** Either (a) replace the sample with one whose
  honest answer is callable Go (so the trap is "name the real exported
  helper" — e.g., probe a real exported helper that's surrounded by
  plausible-but-fabricated siblings); or (b) introduce a third score
  outcome — `1.0` for refused-correctly-with-justification, gated by a
  separate scorer pass that looks for keywords like "unexported",
  "internal", "can't call from outside" in non-code-block outputs. Hard
  to do without an LLM-judge, so (a) is the cheaper fix.

### F7 — Headline metric conflates "model honestly refused" with "model fabricated"

- **Severity:** medium (methodology)
- **Reproducer:** N/A — by inspection of `scoring.py:115-125`. Both
  failure modes return `value=0.0`. Inspect averages the values to
  produce the reported `accuracy` / `mean`.
- **Observed:** A model that says "you can't call this from outside the
  package" (the *correct* behavior we want) is scored identically to a
  model that confidently writes `hooks.ScanModule(...)` (the
  *fabrication* we're trying to measure). The metadata distinguishes
  them (`failure_mode: no_code_block` vs `fabrication`), so a careful
  analyst can pull the receipts apart in `inspect view`, but the headline
  comparison number ("control 0.30 vs treated 0.85") muddles the
  two failure modes.
- **Expected:** The headline number reflects the property being
  measured. Refusal is not a fabrication — it's the safety behavior the
  whole project is for.
- **Suggested fix shape:** Either (a) the README explicitly carves out
  "refusal" as a separate failure mode and instructs the reader to look
  at the per-sample table, not just the headline; or (b) `no_code_block`
  becomes a *neutral* score (Inspect supports `NoAnswer` outcomes, or
  the scorer can return value=None for "unscoreable"); or (c) two
  separate metrics are emitted, one for "did the model write valid
  code", one for "did the code it wrote contain fabrications," and the
  report shows both.
- **Out of scope:** Whether refusal is actually the right behavior on the
  *non-trap* samples (where there is a callable answer). That's a
  different judgement call.

### F8 — Binary scoring means severity of fabrication is invisible

- **Severity:** low (already noted in README + bughunt-2)
- **Reproducer:** by inspection of `scoring.py:138-143`. Any non-empty
  `fabricated` list → 0.0. A snippet with one made-up reference scores
  identically to one with five.
- **Observed:** Two models can both score 0.0 while one is wildly worse.
  Inspect's `mean` collapses to the same number as `accuracy`.
- **Expected:** Same — this is documented and the receipts in metadata
  do preserve the count and names. A future version could swap to
  `value = max(0.0, 1.0 - len(fabricated) / max(len(refs), 1))` for
  fraction-of-references signal, but that's a roadmap item.
- **Suggested fix shape:** No action needed for v0.4. Note for future
  iteration: add an `n_refs_total` and `n_fabricated` to metadata so the
  comparison report can show "control fabricated 14 of 28 references;
  treated 2 of 31" rather than just "control 0.30 vs treated 0.85."

### F9 — Temperature unspecified → comparisons aren't reproducible

- **Severity:** low
- **Reproducer:** `tasks.py` does not pass a `GenerateConfig(temperature=...)`
  to `Task(...)`. Inspect inherits the provider default (Anthropic = 1.0).
  Re-running the same task pulls a different sample from the
  distribution.
- **Observed:** "control 0.30 vs treated 0.85" today might be "0.40 vs
  0.85" tomorrow. The receipt is point-estimate-noisy.
- **Expected:** If the eval is presented as "the receipt," reproducibility
  matters. At minimum, pin `temperature=0` (or document the running
  conditions per pass).
- **Suggested fix shape:** Add `config=GenerateConfig(temperature=0.0)` to
  each `Task(...)` call. `from inspect_ai.solver import GenerateConfig`
  or similar.

### F10 — Cost estimate in README is high by 2-5×

- **Severity:** low (doc)
- **Reproducer:** Back-of-envelope. 7 samples, Sonnet 4.5 ($3/M input,
  $15/M output).
  - Control: ~200 input tokens/sample × 7 + ~300 output × 7 = 1.4k input
    + 2.1k output = $0.036.
  - Treated (each, two such): ~1500 token tool definitions + ~200 input
    × 5 turns + ~50 input × 5 tool returns + ~400 final output ≈ ~9k
    input + ~400 output per sample × 7 = ~$0.23 each.
  - All three: ~$0.50.
- **Observed:** README says $0.50–$2 per pass over the 7 samples
  (`README.md:58`). The lower bound matches my estimate; the upper bound
  assumes ~15+ tool calls per sample, which is implausible for the
  current sample shape.
- **Expected:** README cost should match observed cost so users can
  budget reliably.
- **Suggested fix shape:** Rephrase as "≈ $0.50 per full pass (control +
  both treated arms over 7 samples); allow up to ~$1 if the treated
  model takes more than 5 tool calls per sample." Add a footnote that
  exact cost depends on whether the model spawns extra `find_symbol` /
  `list_files` calls in addition to `verify_symbol`.

### F11 — Sample `target` field is set but never read

- **Severity:** informational
- **Reproducer:** `grep -n "target" evals/inspect/scoring.py` → one hit, in
  the function signature `score(state, target)` — never referenced in the
  body.
- **Observed:** Each sample has a `target` (e.g., `store.Decision`) and
  prose comments listing plausible fabrications. None of that information
  flows into the score. The `target` is decorative; the scorer's oracle
  is entirely the production hook.
- **Expected:** Either use the target (e.g., as an additional check —
  "did the model's snippet include the expected real symbol?") or remove
  it to reduce confusion for future contributors.
- **Suggested fix shape:** Add a secondary scorer that checks the
  declared target appears in the snippet — this catches "the model
  wrote code that uses no Leonard symbols at all" (which currently
  scores 1.0 trivially). Or move target/fabrication-candidates into
  metadata and drop the top-level field.

### F12 — README missing PATH precondition for `~/go/bin`

- **Severity:** informational
- **Reproducer:**
  ```bash
  # On a fresh machine where ~/go/bin isn't in PATH:
  go env GOBIN  # empty → installs go to ~/go/bin
  go install ./cmd/...
  uv run inspect eval tasks.py@fabrication_control --model mockllm/model
  # subprocess spawned with leonard-hook not on PATH → scorer error per sample
  ```
- **Observed:** The scorer raises `leonard-hook not found on PATH`
  (`scoring.py:64-66`), every sample becomes a `failure_mode:
  scorer_error` row. Loud, but easy to misread as "the eval is broken."
- **Expected:** README either tells the user to ensure `~/go/bin` is on
  PATH, or `tasks.py` resolves `leonard-hook` against an explicit
  install location.
- **Suggested fix shape:** Add a one-liner to the README under "Build
  the Leonard binaries" — `export PATH=$(go env GOPATH)/bin:$PATH`. Or
  have `mcp_server_stdio` and the scorer use `shutil.which` against a
  configured fallback list.

### F13 — Sibling-scan walks `evals/` unfiltered, no cost issue today

- **Severity:** informational
- **Reproducer:** Hook latency measured 5× sequentially: ~15 ms each
  (cold or warm, doesn't matter much on this repo size). 30 s
  subprocess timeout is two thousand times that.
- **Observed:** No problem. `readSiblingPackages`
  (`internal/hooks/pre_edit.go:361`) skips `vendor`, `testdata`,
  `node_modules`, and dot-dirs but does *not* skip `evals/`. The
  `evals/inspect/` Python files don't have `.go` extensions so they're
  ignored anyway; the walker is incidentally safe here. For a future
  language extension where the eval samples might include `.go` shims,
  the walk would pick them up — a hypothetical alias collision risk but
  not real today.
- **Expected:** Same.
- **Suggested fix shape:** None for now. If the eval ever stages real
  `.go` snippets to disk inside `evals/`, add `evals` to the
  skip-this-dir list in `readSiblingPackages`.

### F14 — `_eval_scratch.go` filename has no collision risk today

- **Severity:** informational
- **Reproducer:** N/A by design. The scratch file path passed to the hook
  doesn't exist on disk; the sibling-package walker only sees files that
  *do* exist; the model snippet's references are SelectorExpr against
  declared alias identifiers — `_eval_scratch` is an unusual alias and
  would have to be declared via `import _eval_scratch "...somepath..."`
  in the snippet to be picked up.
- **Observed:** No way for a snippet to spuriously "match" the scratch
  filename today.
- **Expected:** Same. Document for future maintainers in case the scratch
  filename is ever moved into a directory.
- **Suggested fix shape:** None.

## Things that worked

- `inspect list tasks tasks.py` returns all three tasks cleanly under
  current `inspect-ai==0.3.223`. The `@task` decorator, the `Task` /
  `MemoryDataset` / `Sample` API, and the `@scorer(metrics=[...])`
  decorator are still the right API surface — no upstream drift versus
  the v0.4 commit.
- `mockllm/model` works for dry-runs without API spend; output without a
  ```go block correctly scores 0.0 with `failure_mode: no_code_block` on
  all 7 samples.
- `_project_root()` correctly resolves PROJECT_ROOT via
  `os.path.dirname(__file__)` regardless of the caller's cwd. The
  scorer's payload includes the right project path; only the subprocess
  cwd is wrong (F1).
- `mcp_server_stdio(cwd=PROJECT_ROOT, command="leonard-mcp", ...)` is the
  right wiring; the MCP binary's own `os.Getwd()` then picks up the
  intended root. The Python side does pass `cwd` here — it's only the
  subprocess in `scoring.py` that doesn't.
- All sample targets currently resolve to real symbols in the v0.7 codebase:
  `store.Decision`, `store.Claim`, `parse.ExtractPython`,
  `parse.ExtractTypeScript`, `index.New`, `config.LoadOrDefault`,
  `hooks.readSiblingPackages`. The `config-shape-after-bughunt2` post-trim
  shape (`InjectDecisionsAtSessionStart`, `LoadOrDefault`) still matches
  the current `internal/config/config.go`.
- The block-reason wording in `pre_edit.go:481-484` exactly matches the
  prefix the scorer searches for today. Brittle (F4) but correct as of
  this commit.
- 30 s subprocess timeout is generously sufficient — measured ~15 ms per
  call against the live `.leonard/leonard.db`, two thousand times under
  budget. Bughunt-2's 480 ms warm-walk projection for a 10k-file project
  is still well under the 30 s ceiling.
- Sibling-pkg walker skips the documented dot-dirs / `vendor` /
  `testdata` / `node_modules` — no extra noise from the `evals/inspect/`
  Python tree.

## Open questions

- Should the eval pin a specific `leonard-hook` binary version so results
  are reproducible across hook changes (F-series fixes might land between
  pass-1 and pass-2)? Or document — as the README does — that "the score
  reflects the hook's CURRENT detection scope"? Trade-off: pinning
  protects the headline number from regressing for legitimate detection
  improvements (which would *correctly* make the model look worse);
  not-pinning lets the eval ride the production hook for free. I'd
  default to not-pinning but record the hook's `git rev-parse HEAD` in
  the eval log header as provenance.
- Is haiku-4-5 worth supporting as a cheaper alternative? My back-of-envelope
  says full-pass cost is already ~$0.50 on sonnet — the only reason to
  add haiku would be for "how cheap can we get fabrication-prevention
  signal." Decide based on whether the eval is presented as "Leonard
  beats Sonnet" (premium model) or "Leonard helps every Anthropic model"
  (sweep).
- Dry-run path for CI: feasible (mockllm + ModelOutput.from_content), but
  worth the lift? The scorer can be unit-tested in plain Python without
  Inspect at all — just import `detect_fabrications` and feed it
  hand-crafted snippets. That's the smallest CI smoke test.
- The eval-on-itself caveat in the README is correct but underweighted:
  the dataset is 7 hand-picked traps targeting *this* codebase, and the
  oracle is the hook on *this* codebase. The number measures "does
  Leonard reduce fabrication on Leonard's own internals." It does not
  generalize to a universal multiplier. Worth being more loudly explicit
  about that in any external write-up that cites the receipt.

## Out of scope for this investigation

- Whether to extend the hook to validate symbol *kind* against the
  selector form (i.e., reject `pkg.MethodName` when MethodName is a
  method). Real product improvement, mentioned in F5, but it's a hook
  change not an eval change.
- Whether the leonard-hook binary checked into the repo root is the right
  artifact to commit. The one in the worktree is schema-v1 against a
  schema-v4 DB. Caught it by accident — `go install ./cmd/leonard-hook`
  produces a working binary that the repo-checked-in `./leonard-hook`
  doesn't match. Separate hygiene concern.
- README polish (badge for "uses Inspect," etc.) — out of bughunt scope.
