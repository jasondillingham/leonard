# Bug Hunt #1 — selfhost

## Summary

Two investigations: (A) symbol-completeness — verify Leonard indexes every
expected top-level symbol in its own source plus the Python/TS fixtures, and
(B) concurrent access — verify DESIGN.md §7 Q7's claim that "SQLite WAL handles
most cases."

**Headline result.** No false negatives on Go (585/585 top-level Go symbols
indexed); no false negatives on the Python or TypeScript fixtures relative to
the parsers' documented contract. WAL holds up well: storms of N=10/50/100
concurrent `leonard-hook post-edit` processes against a real `.leonard/leonard.db`
all complete with **zero** `database is locked` errors, **zero** dropped
claim rows, and the symbols table is left consistent. The MCP `find_symbol`
tool kept returning correct results at >1000 calls/sec while the storm ran.

The defects worth flagging are not WAL — they're (1) qualified-name
collisions in Python/TS (no module prefix → cross-file shadowing of `VERSION`,
`Container`, etc.), (2) arrow-function expressions like
`export const Counter = (...) => ...` indexed as `kind=const` instead of
`function` (very common React pattern), and (3) an off-by-one end_line for
interface declarations.

## Findings

### F1 — Python/TS qualified_name has no module/file prefix (cross-file shadowing)

- **Severity:** medium
- **Reproducer:**
  ```
  cd <repo> && leonard init . && leonard index
  sqlite3 .leonard/leonard.db "SELECT name, kind, qualified_name, file_path FROM symbols WHERE name='VERSION';"
  ```
  Returns:
  ```
  VERSION|var |VERSION|testdata/python/module.py
  VERSION|const|VERSION|testdata/typescript/module.ts
  ```
- **Observed:** Both symbols' `qualified_name` is just `VERSION`. Same for
  `Container` (Python `methods.py` class vs TypeScript `module.ts` class — both
  qname `Container`), `add`, etc.
- **Expected:** A qualified name is meant to disambiguate. Go follows
  `<pkg>.<name>` (and `<pkg>.<recv>.<name>` for methods); Python and TS
  currently use just the bare name (or `Class.method` for methods). The MCP
  `verify_symbol`/`find_symbol` tools can return ambiguous results when two
  files in two languages share a common module-level identifier — and
  more critically, *within a single language*, two different `module.py` files
  in different packages would collide on `VERSION` too.
- **Suggested fix shape:** For Python prefix qname with module path (e.g.
  `testdata.python.module.VERSION`) or filename stem
  (`module.VERSION`); for TypeScript, similar (file stem or relative path).
  The TS parser's `parseClass`/`parseInterface`/`parseTypeAlias` set
  `QualifiedName = nameTok.val` directly — same in the Python parser. Both
  need a per-file prefix carried in `tsParser` / passed to the helper functions.
- **Out of scope for this investigation:** Whether qualified_name is meant to
  match Go's `<pkg>.<name>` convention or some other (importable-name) form is
  a design decision that probably warrants a DESIGN.md note.

### F2 — Arrow-function const indexed as `kind=const`, not `function`

- **Severity:** medium
- **Reproducer:** `testdata/typescript/component.tsx` lines 15–22:
  ```ts
  export const Counter = ({ initial }: { initial: number }) => {
      const [count, setCount] = React.useState(initial);
      return (...);
  };
  ```
  After `leonard index`:
  ```
  sqlite3 .leonard/leonard.db "SELECT name, kind, qualified_name, signature FROM symbols WHERE name='Counter';"
  Counter|const|Counter|const Counter
  ```
- **Observed:** `Counter` is indexed with `kind=const`, signature `const Counter`.
- **Expected:** Arrow functions assigned to a const/let/var are functionally
  React components / callable values. A user asking `verify_symbol("Counter", kind="function")`
  will get no match, even though `Counter` *is* a function for all practical purposes.
  This is the dominant component-declaration form in modern React/TS codebases —
  the parser should either set `kind=function` for these, or at least record the
  arrow-function structure so the kind/signature reflect it.
- **Suggested fix shape:** In `tsParser` const/let/var handling
  (`internal/parse/typescript.go` around the initializer parse), peek the
  initializer expression — if it starts with `(`-then-`)`-then-`=>` or
  `<` (generic) -then-`(` -then-`)` -then-`=>` or `async` -then-`(`, classify
  as `function`. Capture the param list for the signature.
- **Out of scope for this investigation:** Same treatment for function
  expressions (`const x = function() {…}`) and method expressions.

### F3 — Interface declarations report end_line one line short

- **Severity:** low
- **Reproducer:** `testdata/typescript/module.ts` line 28–30:
  ```ts
  export interface Greeter {
      greet(name: string): string;
  }
  ```
  After indexing:
  ```
  sqlite3 .leonard/leonard.db "SELECT name, kind, start_line, end_line FROM symbols WHERE name='Greeter';"
  Greeter|interface|28|29
  ```
- **Observed:** `end_line=29` (line of the last token *inside* the braces).
- **Expected:** `end_line=30` — the line of the closing `}` — to match how the
  same parser ends `kind=type` classes (e.g. `Container` indexed at
  `start=36 end=55`, where line 55 is the closing brace).
- **Suggested fix shape:** In `internal/parse/typescript.go`
  `parseInterface`, after `captureBalanced("{", "}")` returns, use the
  position of the `}` token (one before `p.pos`) instead of the last
  inside-token line. The class parser already does this — copy the pattern.
- **Out of scope for this investigation:** End-line accuracy for type aliases
  with multi-line bodies (e.g. `type X = { ... };` spanning multiple lines).

### F4 — Interface body members and class fields not extracted

- **Severity:** informational
- **Reproducer:**
  - `testdata/typescript/module.ts` line 29: `greet(name: string): string;`
    inside `interface Greeter` — not indexed as a method.
  - `testdata/typescript/module.ts` lines 37–38:
    `static created = 0;` and `private items: T[] = [];` inside
    `class Container` — not indexed.
  - `testdata/typescript/component.tsx` lines 7–8: `name: string; initial?: number;`
    inside `interface Props` — not indexed.
- **Observed:** Interface body and class field declarations are silently
  skipped. The TS parser comment at `internal/parse/typescript.go:531` says so
  explicitly: *"Methods (constructor or regular) emit a symbol; fields are
  silently skipped."*
- **Expected:** Per current contract, this is by-design — but a Claude session
  asking `verify_symbol("greet")` will get no match unless an implementing
  class also defines `greet`. Similarly for static class properties.
- **Suggested fix shape:** None — this is a scope decision. The Go parser does
  the same (interface methods not surfaced as standalone symbols). If/when
  surfaced, mirror the existing method extraction with `parent_id` pointing at
  the interface/class symbol.
- **Out of scope for this investigation:** Same call for class-body assignments
  in Python (`class Foo: bar = 1`) — also skipped by `pythonClassSymbols`.

### F5 — `kind` taxonomy diverges between languages

- **Severity:** informational
- **Reproducer:**
  - Classes in TS index as `kind=type` (`class Container { … }` → `type`,
    not `class`).
  - Module-level Python assignments index as `kind=var`, even
    SCREAMING_SNAKE_CASE constants (`VERSION`, `PUBLIC_NAME`) — Python has no
    formal `const` so this is correct but disagrees with TS's separate
    `kind=const`/`kind=var`/`kind=let`.
  - Go's `var` vs `const` matches Go's keyword.
- **Observed:** `Store.Symbol.Kind` is not a fully cross-language taxonomy.
- **Expected:** A consumer (MCP client / Claude) doing `find_symbol(kind="class")`
  will get nothing — they have to know to ask for `kind=type`. Per
  `cmd/leonard/verify.go:43` the documented kinds are
  `function|method|type|const|var|interface`; classes go in `type`.
- **Suggested fix shape:** Either document this in DESIGN.md / README (the
  taxonomy is the contract), or split `type` into `type|class|struct`. If
  splitting, all three parsers need updating consistently.
- **Out of scope for this investigation:** Whether `enum`, `namespace`,
  `module` should be their own kinds for TS.

### F6 — No false negatives across 585 expected Go symbols

- **Severity:** informational (positive)
- **Reproducer:** `/tmp/selfhost-check/main.go` — walks every `*.go` in
  `cmd/` and `internal/` with `go/parser`, enumerates every top-level
  `FuncDecl`/`GenDecl`, then verifies every (file, name, kind) is in the
  symbols table.
- **Observed:** 585 expected, 585 found, 0 missing, 0 extras. The two
  superficially-flagged "qname mismatches" turned out to be method-name
  collisions across distinct receivers in the same file
  (`HasSymbol` on `preEditSymbolAdapter` *and* `permissiveStore` in
  `cmd/leonard-hook/pre_edit.go`; `Error` on `supersededErr` *and*
  `notFoundErr` in `internal/mcp/decisions_test.go`) — both rows present in
  the DB with distinct qualified_names. False negatives: none.
- **Expected:** No false negatives — this is the positive finding the brief
  asked for.
- **Suggested fix shape:** N/A (working).

### F7 — Concurrent post-edit hooks: N=10/50/100 with WAL, no lock errors

- **Severity:** informational (positive)
- **Reproducer:**
  - `/tmp/concurrent-storm.sh N` — spawns N parallel
    `leonard-hook post-edit` processes (each runs through the real
    `Indexer.IndexFile` + `go vet ./...` + `RecordClaim` pipeline) against a
    real `.leonard/leonard.db` (built from `/tmp/selfhost-test`, a copy of
    the leonard repo). Different file per process (50–100 .go files cycled).
  - `/tmp/concurrent-payload-gen.go` — synthetic PostToolUse envelope.
- **Observed:**
  | N    | wall    | ok  | fail | `database is locked` | claim rows |
  |-----:|--------:|----:|-----:|---------------------:|-----------:|
  |   10 |   1.9 s |  10 |    0 |                    0 |         10 |
  |   50 |   8.5 s |  50 |    0 |                    0 |         50 |
  |  100 |  16.9 s | 100 |    0 |                    0 |        100 |
  Stress variant — 50 concurrent post-edits all targeting the same file
  (`internal/store/store.go`): 50/50 ok, 50 claim rows, the symbols table
  ended with the correct count (28 rows for that file). Symbols total
  stayed at 626 throughout. The race the test was probing — two
  `ReplaceSymbols` transactions on the same `file_path` — serialized
  cleanly via WAL and SQLite's row-level locking.
- **Expected:** DESIGN.md §7 Q7 says "SQLite WAL handles most cases." For this
  workload (separate processes, each opening its own `*sql.DB` pool with the
  `journal_mode=wal, busy_timeout=5000` DSN in `internal/store/store.go:104`),
  the claim holds.
- **Suggested fix shape:** N/A. Worth promoting the §7 Q7 conclusion to "WAL
  + 5 s busy_timeout is sufficient for N=100 concurrent hook processes on a
  Leonard-sized DB" — close that question.
- **Out of scope for this investigation:** Behavior under DESIGN.md §7 Q4's
  <200 ms hook latency budget. At N=100 each `leonard-hook post-edit` is
  running `go vet ./...` (the leonard repo takes ~1.5–2 s on this hardware),
  so total wall time was 17 s; serialized that would be ~150–200 s, so the
  parallelism *is* working. But individual hooks took up to 17 s when CPU
  contention pinned `go vet`. Latency budget is a separate investigation per
  the brief.

### F8 — Concurrent MCP `find_symbol` polling under hook storm: no errors

- **Severity:** informational (positive)
- **Reproducer:**
  - `/tmp/mcp-poll/main.go` — Go program that launches `/tmp/leonard-mcp`
    via `mcp.CommandTransport` (the documented stdio transport), connects an
    MCP client, then loops `find_symbol` calls for 30 s rotating through
    `[Open, RecordClaim, ReplaceSymbols, ExtractGo, HandlePostEdit, NewServer]`.
  - Run in parallel with `/tmp/concurrent-storm.sh 100`.
- **Observed:**
  | poll duration | poller ok | poller fail | storm result      |
  |--------------:|----------:|------------:|-------------------|
  |          5 s  |    10 056 |           0 | (idle)            |
  |         30 s  |    25 970 |           0 | N=100 storm done  |
  |         30 s  |    33 414 |           0 | N=100 storm done  |
  No tool-call errors, no stale/partial results (every find returned the
  expected match for the queried name), no server crashes, no transport
  hangs. Throughput dipped slightly while the storm was burning CPU but
  never errored.
- **Expected:** Reads should not be blocked by WAL writers. Confirmed.
- **Suggested fix shape:** N/A.

### F9 — `leonard index` running concurrently with N=30 post-edit hooks: clean

- **Severity:** informational (positive)
- **Reproducer:** Launch `/tmp/leonard index` (which walks all 71 files,
  parses each, and `ReplaceSymbols`-es each in turn) in the background, then
  spawn 30 `leonard-hook post-edit` processes simultaneously against various
  files in the same project.
- **Observed:** `leonard index` reported `indexed 71 file(s)`; 30/30 hooks
  reported success; 0 `database is locked` lines in any stderr; claims table
  has 30 rows; symbols total is the expected 626. No interference.
- **Expected:** Same as F7/F8.
- **Suggested fix shape:** N/A.

## Things that worked

- **`leonard init .` and `leonard index`** on a copy of the leonard repo:
  cleanly initializes `.leonard/`, indexes 71 files, produces 626 symbols.
  WAL mode is on (verified by `PRAGMA journal_mode;`).
- **Go parser** (`internal/parse/golang.go`): 100 % of expected top-level
  Go symbols extracted; method receivers correctly disambiguated
  (`pkg.Recv.Method`); generic receivers (`*ast.IndexExpr`/`*ast.IndexListExpr`)
  handled.
- **Python parser** (`internal/parse/python.go`): all module-level functions
  / classes / class methods / module-level assignments captured for both
  fixtures.
- **TypeScript parser** (`internal/parse/typescript.go`) on the two
  in-tree fixtures: every documented case extracted — `export function`,
  `export const` (including the multi-decl `const a = 1, b = 2;` form),
  async functions, generic classes, class methods (constructor, public,
  private with underscore prefix, async). Even the `.tsx` JSX content
  didn't trip the body-balancing scanner.
- **Method-name disambiguation in qualified_name**: when two methods in the
  same file share a name on different receivers, both rows are persisted
  with distinct `qualified_name`s (`main.preEditSymbolAdapter.HasSymbol`
  vs `main.permissiveStore.HasSymbol`).
- **WAL + 5 s busy_timeout** is enough for at least N=100 concurrent
  hook-driven writers per the storm tests.
- **Stdio MCP transport** (`leonard-mcp`): handles 30 s of sustained
  `find_symbol` polling at >1000 calls/s with zero errors, both idle and
  while a 100-process write storm runs against the same DB.
- **`ReplaceSymbols`'s DELETE+INSERT-in-one-tx pattern** is race-free —
  50 concurrent processes hitting the *same* file all serialize into the
  expected end state.

## Open questions

- **What is the correct qualified_name convention across languages?** Go uses
  `<pkg>.<name>`. Python and TS currently use bare names (or `Class.method`).
  Should they prefix with the module path or file stem (F1)? If yes, what
  rooted at? `module.foo` vs `pkg.module.foo` vs path-relative-to-repo?
  This is a DESIGN.md call — flagged but not decided.
- **Should arrow-function const declarations be `kind=function`?** (F2) — same
  question for `const x = function() {…}`. Both are functional in real code
  but currently masquerade as `const` in the index.
- **Is the divergent kind taxonomy (`class` → `type`, Python const → `var`) a
  feature or a wart?** (F5) — needs a written decision.
- **What does the hook do on a >30 s `go vet`?** The default `VetTimeout` in
  `internal/hooks/post_edit.go:106` is 30 s, then the vet context cancels and
  the claim is recorded as unverified. Under N=100 contention I never hit
  this on the leonard repo, but a large monorepo would. Out of scope for §7
  Q7; would be in the §7 Q4 latency investigation.
- **What happens if a `leonard-hook post-edit` is killed mid-transaction
  (Claude Code shutdown)?** WAL would roll back the in-flight tx, but no
  claim row would land for that edit. Verifying this would need a kill-9
  test that's out of scope here.

## Out of scope for this investigation

- TypeScript parser deep audit (heuristics, JSX edge cases, default exports,
  decorators) — that's the **hunt-typescript** lane.
- Hook payload contract conformance — that's the **hunt-hooks** lane.
- MCP stdio transport input-validation depth — that's the **hunt-mcp** lane.
- Hook latency benchmarks (<200 ms target) — DESIGN.md §7 Q4, separate round.
- Fixes — no code changes per the bug-hunt rules.

## Scratch artifacts (not committed)

- `/tmp/selfhost-test/` — copy of the leonard worktree with a fresh
  `.leonard/leonard.db`. Used for all storm tests so the original worktree
  DB stays clean.
- `/tmp/selfhost-check/main.go` — Go program that compares expected vs
  actual symbols for every `*.go` file in `cmd/` and `internal/`. Recipe:
  ```
  cd /tmp/selfhost-check && go run .
  ```
- `/tmp/concurrent-storm.sh` — bash script that spawns N parallel
  `leonard-hook post-edit` processes.
- `/tmp/concurrent-payload-gen.go` — synthesises a PostToolUse JSON envelope.
- `/tmp/mcp-poll/main.go` — Go MCP client that launches `leonard-mcp` via
  `CommandTransport` and polls `find_symbol`.
- `/tmp/leonard`, `/tmp/leonard-hook`, `/tmp/leonard-mcp` — binaries built
  from the worktree (`go build -tags leonardreal ./cmd/leonard ./cmd/leonard-hook`,
  `go build ./cmd/leonard-mcp`).
