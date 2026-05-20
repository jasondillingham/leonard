# Bug Hunt #2 — python

## Summary

I audited the v0.2 Python parser swap (commit `099a936`, `internal/parse/python.go`
+ embedded `extract_python.py`) by running the embedded extractor over the
existing fixtures, the entire CPython 3.14 stdlib (1844 files, 68k symbols),
and every Python file in `/opt/homebrew/lib/python3.14/site-packages` (pip,
setuptools, wheel, certifi — 733 files, 11k symbols). I also drove the
freshly-built `leonard` binary through `leonard init` / `leonard index` on a
synthetic project to exercise the indexer → ParseFailure surfacing end-to-end.
The scratch driver and fixtures are under `/tmp/leonard-bh2/` — not committed.

Headline: the AST-walking script itself is **solid on real-world code** —
zero failures across pip + setuptools + most of stdlib, including every
"modern syntax" form the gpython predecessor choked on (f-strings, walrus,
match, PEP 526/585/604/695). But the **Go-side subprocess wrapper has three
real bugs**:

1. The documented `LEONARD_PYTHON` env override is **never read** —
   `os.Getenv` is not called anywhere in the package.
2. There is **no timeout / context / cancellation** on the subprocess. A
   slow/hung/recursive python3 shim hangs the indexer forever and survives
   parent-process termination as an orphan on Unix.
3. When the subprocess exits with anything other than 0/2 (the SyntaxError
   path), the user-facing failure message is `"python3 extract failed:
   exit status 1: Traceback (most recent call last):"` — the actual error
   (`UnicodeDecodeError`, `RuntimeError`, etc.) is hidden because the
   indexer truncates stderr at the first newline, which is the traceback
   header rather than the error.

There are also two material symbol-fidelity smells inherited from the v0
"shallow indexing" stance that gpython exhibited too — nested classes and
conditional defs are silently dropped — plus one signature-rendering miss
(async functions show `def …` not `async def …`).

---

## Findings

### F1 — `LEONARD_PYTHON` env override is documented but unimplemented
- **Severity:** high
- **Reproducer:**
  ```
  # Build current code
  go build -o /tmp/leonard-fresh ./cmd/leonard
  mkdir /tmp/proj && echo 'def hi(): pass' > /tmp/proj/hello.py
  cd /tmp/proj
  /tmp/leonard-fresh init .
  LEONARD_PYTHON=/nonexistent /tmp/leonard-fresh index
  # → "indexed 1 file(s)" with 1 symbol persisted
  sqlite3 .leonard/leonard.db 'SELECT COUNT(*) FROM symbols;'  # → 1
  ```
- **Observed:** `LEONARD_PYTHON` has **zero effect**. The indexer happily
  finds `python3` on PATH and parses the file regardless of what
  `LEONARD_PYTHON` is set to. Conversely, if `python3` is genuinely missing
  from PATH but `LEONARD_PYTHON=/opt/homebrew/bin/python3` points at a real
  interpreter, the indexer **still fails** with `parse: python3 not found
  on PATH` because nothing consults the env var.
- **Expected:** Per the docstring on `pythonInterpreter` (line 28 of
  `internal/parse/python.go`) and the user-facing error string at line 35
  (`"set LEONARD_PYTHON to override"`), setting the env var should:
  - Direct the subprocess to use that path/name instead of `python3`
  - Surface the error from THAT binary if it's broken
  Same code-grep proof:
  ```
  $ grep -rn "LEONARD_PYTHON\|os.Getenv" internal/parse/
  internal/parse/python.go:28:// Override via the LEONARD_PYTHON environment variable when the host
  internal/parse/python.go:35:var ErrPythonUnavailable = errors.New("parse: python3 not found on PATH (set LEONARD_PYTHON to override)")
  # — no os.Getenv call anywhere
  ```
  The commit message for `099a936` also explicitly claims: *"LEONARD_PYTHON
  env var overrides the binary name for uv/pyenv shims."*
- **Suggested fix shape:** Add `os.Getenv("LEONARD_PYTHON")` lookup at the
  top of `ExtractPython` (or once per process, lazily, with `sync.Once`
  matching the "Resolved once via PATH lookup" comment — which is itself
  not currently true; see F4). Fall back to `"python3"` when unset. A
  test that runs `t.Setenv("LEONARD_PYTHON", "/usr/bin/false")` and
  asserts the resulting ParseFailure points at `/usr/bin/false` would
  pin the contract.
- **Out of scope for this investigation:** Whether pyenv/uv shims that
  resolve `python3` to a wrapper script have additional quirks (those
  wrappers do reach the script, but the docstring's claim about non-standard
  names — `pyenv shims` — currently can't be exercised at all).

---

### F2 — Subprocess has no timeout; a hung interpreter hangs the indexer forever
- **Severity:** high
- **Reproducer:**
  ```
  # 1. Drop a shim that masquerades as python3 but recurses forever:
  cat > /tmp/python3 <<'EOF'
  #!/usr/bin/env bash
  exec "$0" "$@"
  EOF
  chmod +x /tmp/python3

  # 2. Force PATH so leonard finds the shim:
  PATH=/tmp /tmp/leonard-fresh index
  # → hangs forever; ps shows hundreds of forked bash processes
  ```
  Less contrived path: a pyenv shim that takes 0.5s to start (not unusual
  on some setups) gets dispatched per-file by a sequential indexer. 100
  files = 50 seconds. With no timeout, a misconfigured pyenv that hangs
  silently bricks the indexer.
- **Observed:**
  - `internal/parse/python.go:71` calls `cmd.Run()` with no
    `exec.CommandContext`, no `WithTimeout`, no `Process.Kill` path.
  - `grep -rn "Context\|Timeout\|context.WithCancel" internal/parse/
    internal/index/` returns zero hits.
  - Killing the leonard parent process (`SIGINT`) does NOT propagate to
    the python3 child on Unix — `cmd.Run()` doesn't install a
    `Cancel`/`WaitDelay` handler, so the child is reparented to init
    and runs to completion (or in the recursive-shim case, forever).
- **Expected:**
  - A bounded per-file budget — even 30 seconds would be a sane upper
    bound for any pathologically large file (the 50MB / 800k-function
    test in §F-perf below took 65s on a fast machine, which is
    arguably an acceptable timeout floor).
  - The brief explicitly mentioned "the per-file 30s default" — that
    default does not exist in the code.
  - SIGINT on the leonard process should kill in-flight python3
    children (set `cmd.SysProcAttr.Setpgid = true` + send `Signal(SIGKILL)`
    on context cancel).
- **Suggested fix shape:** Wire `exec.CommandContext(ctx, bin, ...)` with
  `context.WithTimeout(ctx, 30*time.Second)` (or a configurable knob in
  `.leonard/config.toml`). Set `cmd.WaitDelay` so a child that ignores
  its terminate signal still gets killed. The indexer should also accept
  a `context.Context` and propagate signal handling.
- **Out of scope for this investigation:** End-to-end signal handling on
  Windows (where process groups work differently); we tested macOS only.

---

### F3 — Non-zero / non-SyntaxError exit hides the real error in CLI output
- **Severity:** medium
- **Reproducer:**
  ```
  mkdir /tmp/proj-binary && cd /tmp/proj-binary
  /tmp/leonard-fresh init .
  cp /bin/ls binary.py            # binary content masquerading as .py
  /tmp/leonard-fresh index
  # → "leonard: 1 file(s) failed to parse — symbols dropped:
  #     binary.py: python3 extract failed: exit status 1: Traceback (most recent call last):"
  ```
  Real-world hit: any `.py` file in CPython's stdlib `test/encoded_modules/`
  (latin-1, koi8-r, etc.) declares `# -*- coding: iso-8859-1 -*-` but
  Python's `sys.stdin.read()` defaults to UTF-8 and ignores the file's
  declared encoding — those files raise `UnicodeDecodeError` and surface
  with the same useless `Traceback (most recent call last):` line. I hit
  this on `tokenizedata/badsyntax_pep3120.py` and `encoded_modules/module_iso_8859_1.py`
  when running the extractor over stdlib (5 such files, ~0.3% of 1849
  total).
- **Observed:**
  - `internal/parse/python.go:82` wraps the subprocess error with
    `fmt.Errorf("python3 extract failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))`
    — note `strings.TrimSpace` only trims edges, leaving embedded newlines.
  - `internal/index/indexer.go:236` then truncates at the first `\n`:
    `if nl := strings.Index(msg, "\n"); nl >= 0 { msg = msg[:nl] }`.
  - The result: Python's standard `Traceback (most recent call last):`
    header survives, the actual `ErrorClass: message` (which is the
    LAST line of a traceback in Python) gets sliced off.
- **Expected:** The CLI summary should show the actionable error type
  (e.g. `UnicodeDecodeError: 'utf-8' codec can't decode byte 0xca…`),
  not the boilerplate traceback header.
- **Suggested fix shape:** In `extract_python.py`, broaden the existing
  `try/except SyntaxError` to also catch `UnicodeDecodeError` (during
  `sys.stdin.read()`) and any `Exception` during `ast.parse()`. Emit
  the same single-line `"line N: ErrorClass: msg"` format for all
  errors and exit 2 (or a dedicated code per class). Alternatively
  on the Go side, parse a Python traceback and extract the last
  non-empty line before serializing — but doing it script-side is
  cleaner.
- **Out of scope for this investigation:** Encoding detection — reading
  the file with `# -*- coding: … -*-` honored would require reading via
  `tokenize.detect_encoding` on a `BufferedReader`, which is more
  invasive than the immediate UX fix.

---

### F4 — `exec.LookPath` runs per-call, not once; comment claims otherwise
- **Severity:** low
- **Reproducer:** Read `internal/parse/python.go:21-30` then `:60-64`:
  the docstring says *"Resolved once via PATH lookup at first use so each
  parse call doesn't re-stat /usr/bin/python3"* but `LookPath` is the
  very first line of `ExtractPython` and runs every invocation.
- **Observed:** Each `ExtractPython` call pays the cost of a fresh
  `LookPath` (a PATH walk + stat-per-directory). On a typical macOS
  PATH of ~20 entries this is ~50µs — small but unbounded with
  filesystem latency (e.g. a stale NFS mount).
- **Expected:** Match the docstring — resolve once via `sync.Once`.
- **Suggested fix shape:** `var resolveOnce sync.Once; var resolved
  string; var resolveErr error`. Or, if F1's fix lands first, fold the
  env-var lookup into the same `sync.Once` block.
- **Out of scope for this investigation:** Whether `LookPath` is hot
  enough on real workloads to matter — probably not (subprocess startup
  dwarfs it), but the comment lies regardless.

---

### F5 — Async functions/methods render as `def …`, not `async def …`
- **Severity:** low
- **Reproducer:**
  ```python
  class Async:
      async def fetch(self): return 1
  ```
  Extracted signature: `def fetch(self)`. Expected: `async def fetch(self)`.
- **Observed:** `extract_python.py:60` always uses `prefix="def"` because
  `func_signature(node, prefix: str = "def")` is called by `function_record`
  with no override. The function handles `ast.AsyncFunctionDef` for
  *recognition* (top-level dispatch at line 163, class-body dispatch at
  line 120) but the rendered signature drops the `async` marker.
- **Expected:** Signature should distinguish async functions —
  `find_symbol` users seeing `def fetch(self)` will assume sync, leading
  to subtle "this is not awaitable" bugs in agent-generated code.
- **Suggested fix shape:** In `function_record`, pass `prefix="async def"`
  when `isinstance(node, ast.AsyncFunctionDef)`.
- **Out of scope for this investigation:** Return type annotations and
  parameter type annotations are also dropped — that's a broader signature
  fidelity question deferred to v0 design ("Defaults and annotations are
  omitted to keep signatures short", per old code commentary).

---

### F6 — Nested classes and conditional defs are silently dropped
- **Severity:** medium
- **Reproducer:**
  ```python
  class Outer:
      class Inner:                       # NOT extracted
          def inner_method(self):        # NOT extracted
              ...
      def outer_method(self):            # extracted
          ...

  if sys.platform == "darwin":
      def mac_only(): ...                # NOT extracted

  if __name__ == "__main__":
      def cli_main(): ...                # NOT extracted (very common idiom)
  ```
- **Observed:** Confirmed via my fidelity fixture
  (`/tmp/leonard-bh2/fidelity.py`): `Outer.Inner` and `Outer.Inner.inner_method`
  produce zero rows; `mac_only` and `cli_main` produce zero rows. The
  brief asked me to "compare the new behavior to the old gpython behavior" —
  the old gpython parser had the same limitation, so this is *not* a
  regression, but it IS a real-world miss that the v0.2 commit message
  doesn't acknowledge.
- **Expected:** Per `extract_python.py:11-12` ("function and class bodies
  are NOT walked for nested decls — Leonard's v0 brief keeps the index
  shallow"), this is documented behavior. But the brief flagged it as a
  thing to investigate, and there's a real cost: `cli_main()` is one of
  Python's most idiomatic patterns, and Leonard's agent users will be
  surprised when `verify_symbol cli_main` returns nothing for a file
  that visibly defines it.
- **Suggested fix shape:** Two options:
  1. Cheap: at minimum, walk one level of `if __name__ == "__main__"`
     blocks (string-match on the condition's AST). Doesn't help nested
     classes.
  2. Comprehensive: walk all `ast.If`/`ast.Try`/`ast.With` bodies for
     top-level-equivalent defs, and one level of nested classes
     (`Outer.Inner` becomes a `type` symbol with qualified_name
     `mod.Outer.Inner`).
  Either way, document the new behavior in `extract_python.py`'s
  module docstring.
- **Out of scope for this investigation:** Whether `find_symbol` should
  also search by partial qname (e.g. `Inner` finding `Outer.Inner`) —
  that's a store-layer query, not parser business.

---

### F7 — Lambda assigned to a name gets `kind=var`, not `kind=function`
- **Severity:** low
- **Reproducer:**
  ```python
  square = lambda x: x * x
  # → {"name":"square","kind":"var","signature":"var square","exported":true}
  ```
- **Observed:** Module-level `name = lambda …` is treated as a plain
  `Assign` with no inspection of the RHS. Compare to `def square(x):
  return x * x` which gets `kind=function`. Semantically equivalent code
  is indexed under two different kinds.
- **Expected:** Either:
  - Detect `ast.Lambda` RHS and emit `kind=function` (with a synthetic
    `def square(x)` signature), OR
  - Document that lambdas-as-functions are intentionally classified as
    vars (they ARE assignments, after all).
- **Suggested fix shape:** Check `isinstance(node.value, ast.Lambda)` in
  `assign_records` and route to `function_record` with a constructed
  signature.
- **Out of scope for this investigation:** `functools.partial`,
  `operator.add`-as-callable, etc. — only the trivial lambda case is
  cheap to detect.

---

### F8 — Files with non-UTF8 declared encoding can't be parsed at all
- **Severity:** medium
- **Reproducer:** Pick any `.py` file that declares `# -*- coding: latin-1 -*-`
  (or any non-utf8) and contains non-ASCII bytes — CPython's test corpus
  has several. The Python script's `sys.stdin.read()` uses Python's default
  stdin encoding (`utf-8` on macOS/Linux unless `PYTHONIOENCODING` is
  set) and ignores the file's declared encoding because the file's bytes
  arrive via a stdin pipe, not via filesystem-open semantics where
  `tokenize.detect_encoding` would fire.
- **Observed:** `UnicodeDecodeError` exits 1, surfaced as the F3 traceback
  noise. The Go side has NO retry / re-encoding logic.
- **Expected:** Either:
  - Decode the bytes script-side using `tokenize.detect_encoding`
    against the byte stream BEFORE handing them to `ast.parse`, OR
  - Surface a clear error like "file declares non-utf-8 encoding;
    Leonard requires UTF-8 source".
- **Suggested fix shape:** In `extract_python.py main()`, replace
  `src = sys.stdin.read()` with:
  ```python
  raw = sys.stdin.buffer.read()
  enc = tokenize.detect_encoding(io.BytesIO(raw).readline)[0]
  src = raw.decode(enc)
  ```
  This is what `ast.parse(<file>)` does internally; replicating it via
  the bytes-in-stdin path keeps a single source of truth.
- **Out of scope for this investigation:** BOM handling (Python's tokenizer
  already strips UTF-8 BOM but other BOMs are SyntaxError-equivalent).

---

### F9 — `__all__` is ignored; export classification is leading-underscore only
- **Severity:** informational
- **Reproducer:**
  ```python
  __all__ = ["public_one"]
  def public_one(): ...      # exported=True ✓
  def _private_in_all(): ... # could be in __all__ but exported=False (heuristic says private)
  def not_in_all(): ...      # NOT in __all__ but exported=True (heuristic says public)
  ```
- **Observed:** The `is_exported` function in `extract_python.py:23` only
  considers the leading-underscore convention; `__all__` is never read.
- **Expected:** Python's actual export contract IS `__all__` when it's
  defined; leading-underscore is just convention. For "is this name
  intended to be public?" the right answer is "if `__all__` exists, only
  things in it; else leading-underscore heuristic".
- **Suggested fix shape:** Before walking the body, scan for a top-level
  `__all__ = [...]` (literal list of strings) and use it as an override
  for `is_exported`. Falls back to current behavior when absent or
  non-trivial.
- **Out of scope for this investigation:** Star-import resolution, `from
  pkg import *` semantics — that's pkg-discovery work, not parser work.

---

### F10 — PEP 695 `type Alias = …` statements are not extracted
- **Severity:** informational
- **Reproducer:**
  ```python
  type Vector = list[float]   # Python 3.12+ — ast.TypeAlias node
  ```
  Produces zero symbols.
- **Observed:** `extract_python.py:159-173` handles `FunctionDef`,
  `AsyncFunctionDef`, `ClassDef`, `Assign`, `AnnAssign` — no `ast.TypeAlias`
  branch. PEP 613 `UserId: TypeAlias = int` IS captured (as an `AnnAssign`)
  but the cleaner PEP 695 form is silently dropped.
- **Expected:** PEP 695 type aliases ARE the modern way to declare type
  aliases; missing them means Leonard understates the surface of any
  3.12+ codebase that adopted the syntax.
- **Suggested fix shape:** Add `elif isinstance(node, ast.TypeAlias)` →
  emit as `kind=var` or a new `kind=typealias` (the latter requires
  store schema agreement, the former is shallow but consistent with how
  `TypeAlias`-annotated PEP 613 forms are stored today).
- **Out of scope for this investigation:** PEP 695 generic syntax in
  class/function signatures (e.g. `class Box[T]:`) — those DO parse
  successfully (the existing `TestExtractPython_ModernSyntax` exercises
  this), only the bare-statement form is missed.

---

### F11 — Property getter/setter pairs duplicate `qualified_name` rows
- **Severity:** informational
- **Reproducer:**
  ```python
  class Widget:
      @property
      def name(self): return self._name
      @name.setter
      def name(self, value): self._name = value
  ```
  Yields two rows with qualified_name `mod.Widget.name`, both kind=method,
  with different signatures.
- **Observed:** Inspect `internal/store/store.go:215-225`: qualified_name
  is **not** UNIQUE-constrained, so both rows persist. `FindSymbolsByName("name")`
  returns both, ranked arbitrarily.
- **Expected:** Ambiguous — either:
  - Coalesce property getter/setter into one symbol with a combined
    signature (`property name(self) / setter name(self, value)`), OR
  - Document that property pairs produce two rows and let consumers
    handle.
- **Suggested fix shape:** This is a design decision, not a bug. Worth
  noting because the v0.2 commit didn't address it and downstream
  `verify_symbol` consumers may not expect duplicates.
- **Out of scope for this investigation:** Whether overloaded `@typing.overload`
  functions (which produce N+1 rows: N type stubs + 1 implementation)
  exhibit similar duplication. They likely do.

---

### Perf note (not a bug)

The brief asked about subprocess startup cost. Measured ~30ms per file on
Apple Silicon (Python 3.14) — the commit message's "~40ms" claim holds. For
a 1000-file Python project, that's 30s of subprocess startup on top of
parse work. The indexer is sequential
(`internal/index/indexer.go:104-156`), so this is purely serial. Possible
future optimization: a long-lived python3 subprocess that reads
`{path, src_len}\n<src>` chunks over stdin — but it's a v0 perf concern,
not a v0 bug.

A 50MB Python file (800k functions) took 65s to parse and produced 162MB of
JSON; the Go side holds both the stdout buffer and the parsed slice in
memory simultaneously (`bytes.Buffer` at line 68-69, plus the
`pythonSymbolRecord` slice). For a typical project this is fine; for a
pathologically large vendored file it could OOM. Not exercised in any
real-world corpus we tested.

---

## Things that worked

I verified these behaviors against real-world Python:

- **Modern syntax acceptance.** Walrus, match, f-strings, PEP 585 generics,
  PEP 604 unions, PEP 526 annotated assignments, PEP 695 generic class
  parameters (`class Box[T]:`) — all parse successfully and produce the
  expected symbol rows. This is the headline improvement over gpython
  and it delivers.
- **Real-world corpora.** Zero parse failures across 733 files of pip /
  setuptools / wheel / certifi (11k symbols), and 1844 of 1849 stdlib
  files (the 5 failures are F8 encoding issues, not parser bugs).
- **Concurrent invocations.** 50 simultaneous extractions of `methods.py`
  via shell fan-out completed in 670ms (process-startup-bound, not
  memory-bound). The Go indexer is single-threaded so this isn't
  exercised by the production code path, but the script is reentrant-safe.
- **Unicode identifiers.** `def π(x):`, `class Δ:`, `日本語 = "x"` all
  round-trip through JSON correctly (UTF-8 escapes in JSON, decoded back
  to runes by Go's `encoding/json`).
- **Binary content / NUL bytes.** A `.py` file containing a binary blob
  exits with `SyntaxError: source code string cannot contain null bytes`
  (exit 2, clean error). Non-decodable UTF-8 binary exits 1, see F3/F8.
- **Empty input.** `echo "" | ...` produces `[]` (empty array), exit 0.
- **Stdin sequence.** The script always reads stdin BEFORE parsing, so
  there's no EPIPE-on-stdin-writer risk.
- **Large file.** A 3MB Python file with 50k functions parsed in 2s; a
  50MB file with 800k functions in 65s — slow but completes.
- **Existing test fixtures.** `testdata/python/module.py` and
  `methods.py` extract identical symbols from both the gpython predecessor
  and the new subprocess extractor (qualified_name format aside — that
  changed in commit `1218a45`, unrelated to the v0.2 swap).
- **SyntaxError surfacing.** All malformed-syntax cases route through
  exit-code-2 with a single-line "line N: SyntaxError: msg" stderr — the
  Go side surfaces it correctly. The pre-existing
  `TestExtractPython_ParseErrorIsSingleLine` test enforces this contract.
- **Render-bases fallback for Python <3.9.** `render_bases` checks
  `hasattr(ast, "unparse")` — on a 3.8 host, class signatures degrade to
  `"class Name"` without bases (documented in the module docstring).
  The rest of `deepest_lineno` already handles `end_lineno`'s absence
  (it was added in 3.8 too).

## Open questions

- **Should the parser walk nested decls (F6)?** v0 brief explicitly says
  "shallow" but the most common Python idioms (`if __name__ == "__main__"`,
  inner classes used as namespaces) lose visibility. Either accept this
  and document it loudly in `leonard doctor`, or carve out the
  `if __name__` case as a tiny exception.
- **What's the long-term plan for non-UTF8 source (F8)?** Decoding via
  `tokenize.detect_encoding` is the right answer if Leonard wants to
  support arbitrary legacy codebases. If "UTF-8 only, declared
  loudly" is the v0 stance, F8 becomes a docs/error-message issue.
- **Should `LEONARD_PYTHON` (F1) take a full path, a binary name on PATH,
  or both?** Current docstring is ambiguous. uv shims often live in
  `~/.local/bin`, pyenv shims in `~/.pyenv/shims`. A path is more
  predictable; a name leverages PATH for portability. Pick one in the
  fix-up commit.
- **Is a 30s timeout (F2) the right default, or should there be a
  per-file size-based budget?** I suggested 30s because the brief
  mentioned it, but the 50MB-file test took 65s — a strict 30s would
  drop those even when the parse would have succeeded. A size-based
  budget (`max(30s, src_size_MB * 2s)`?) might be saner. Decision is
  Jason's.
