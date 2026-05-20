# Bug Hunt #5 — treesitter-dispatcher

## Summary

Investigated the v0.19 tree-sitter dispatcher: the Rust crate at `internal/parse/treesitter/`, the Go wrapper at `internal/parse/treesitter.go`, and the dispatcher's behavior across all 28 currently-supported grammars. The architecture is fundamentally sound — per-call process isolation makes concurrent extraction safe by construction, the 8 MiB pre-subprocess file cap bounds memory, and the exit-code-2 ParseError handshake works as designed. However, several real bugs were found in the dispatcher core: substring-based visibility detection, missing UTF-8 fallback (any non-UTF-8 byte fatals a file), HCL multi-label blocks emit duplicate symbols (in direct contradiction of the source comment), parent-folding never fires for `@const`/`@type`/`@interface` kinds (only for `method`/`function`), and the missing-binary failure mode produces one ParseFailure per file (potentially thousands) with the same message. Several grammars have real false-negative query coverage gaps (C/C++ pointer-returning functions, Dart constructors/getters, WIT world exports, Erlang `-export` visibility).

## Findings

### F1 — Invalid UTF-8 in source kills extraction with confusing exit code

- **Severity:** medium
- **Reproducer:**
  ```sh
  BIN=internal/parse/treesitter/target/release/leonard-extract-treesitter
  printf 'class Foo { String \xe4\xb8 hello() { return ""; } }' | \
    "$BIN" --lang java Foo.java
  # → stderr: "leonard-extract-treesitter: read stdin: stream did not contain valid UTF-8"
  # → exit code: 1
  ```
- **Observed:** main.rs uses `io::stdin().read_to_string(&mut src)` which requires the entire input be valid UTF-8. Any single bad byte (a truncated multi-byte character, a Latin-1 byte snuck into a comment, a binary blob in a string literal) causes the whole file to be rejected with **exit code 1**. The Go-side handler at `internal/parse/treesitter.go:114` reserves exit code 2 for "parse error" — exit 1 is treated as the catch-all `"tree-sitter extract failed: %v: %s"` shape. Per-file ParseFailure is still recorded (log-and-continue works), but the error message that surfaces is `tree-sitter extract failed: exit status 1: leonard-extract-treesitter: read stdin: stream did not contain valid UTF-8` — which looks like an infra failure, not a per-file problem.
- **Expected:** non-UTF-8 input either (a) be lossily decoded so tree-sitter still parses what it can, or (b) be surfaced as exit-code 2 (ParseError) with a clean detail so the CLI summary reads sensibly. Tree-sitter itself accepts `&[u8]` and tolerates non-UTF-8 — the `read_to_string` choice is a deliberate restriction that doesn't match the parser's actual capability.
- **Suggested fix shape:** read stdin into a `Vec<u8>` via `read_to_end`, then either (1) pass `&src` (bytes) into `parser.parse(...)` since tree-sitter `parse_with_options` accepts byte slices, or (2) on UTF-8 failure emit a ParseError JSON to stdout and exit 2. The first option preserves more correctness; the second at least classifies the failure correctly.
- **Out of scope:** whether any v0.19-shipped projects actually contain non-UTF-8 files (some C/C++ codebases historically do).

### F2 — HCL multi-label blocks emit duplicate symbols (comment claims first-label only)

- **Severity:** medium
- **Reproducer:**
  ```hcl
  resource "aws_instance" "web" {
    ami = "ami-12345"
  }
  ```
  ```sh
  "$BIN" --lang hcl sample.tf < sample.tf
  ```
- **Observed:** Two separate symbols emitted for the single `resource` block — one with `name=aws_instance`, one with `name=web` — both with the same span (lines 1-3), both `kind=type`, both `exported=true`. With many resources in a real Terraform file this doubles the symbol count.
  ```json
  [{"qualified_name":"sample.aws_instance","name":"aws_instance","kind":"type",...,"start_line":1,"end_line":3},
   {"qualified_name":"sample.web","name":"web","kind":"type",...,"start_line":1,"end_line":3}]
  ```
- **Expected:** The HCL_QUERY's source-code comment at main.rs:670-672 explicitly states: "v0.27 captures only the first label. Multi-label blocks (`resource "aws_instance" "web"`) keep just the type name in the Symbol". Actual behavior contradicts this. Either the comment is wrong, or the query is.
- **Suggested fix shape:** anchor the `(string_lit (template_literal) @name)` pattern with a `.` (first-child) anchor so only the first label is captured, e.g. `(block (identifier) @_kind . (string_lit (template_literal) @name) ...)`. Or capture both labels into a combined name like `aws_instance.web`.
- **Out of scope:** whether the `web` (resource instance name) is more useful for verify_symbol than `aws_instance` (resource type) — that's a query-refinement debate for a different lane.

### F3 — Parent-folding only applies to method/function kinds; const, type, interface never get folded

- **Severity:** medium
- **Reproducer:**
  ```sql
  CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
  CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT);
  ```
  ```sh
  "$BIN" --lang sql sample.sql
  ```
- **Observed:** Both tables' `id` and `name` columns get identical qnames `sample.id` and `sample.name`. Each pair is two distinct rows that find_symbol cannot disambiguate. Same root cause hits Ruby (`module M; class Greeter; end; end` → `Greeter` has qname `sample.Greeter`, not `sample.M.Greeter`), GraphQL (`type User { id }` and `interface Node { id }` both produce `sample.id`), Java nested classes, etc.
- **Root cause:** main.rs:1050 gates parent-folding on `matches!(kind, "method" | "function")`. SQL columns are `kind=const`; classes/interfaces inside modules are `kind=type`/`kind=interface`. These never fold even when `parent_container_kinds` is non-empty for the language.
- **Expected:** any capture whose enclosing node is in `parent_container_kinds` should have the parent name folded in. Without folding, qname collisions across containers are inevitable for any nested-symbol grammar.
- **Suggested fix shape:** remove the `matches!(kind, "method" | "function")` guard. Apply parent-folding to any kind when a parent container is found. The risk is double-folding (e.g. a constant inside a class inside a module would walk all the way up) — bound the walk to one level, which is the current behavior.
- **Out of scope:** whether find_parent_name should walk multiple levels (currently only finds the first ancestor matching).

### F4 — `is_exported` uses substring matching on modifier text — false matches possible

- **Severity:** low
- **Reproducer:** Conceptual — any tree-sitter grammar where a `modifiers` node's text concatenation contains the substring `"public"` or `"private"` for reasons other than the visibility keyword would incorrectly classify. Practical example: PHP `public_static` doesn't exist, but a grammar's modifier node might one day include text like `protected_internal` (C# does have `protected internal` as a real modifier — the modifier text concatenation would contain both `"protected"` and `"internal"`, and `is_exported` checks `private`/`internal`/`protected` to bail at false).
- **Observed:** main.rs:1110-1121 does:
  ```rust
  if text.contains("public") || text.contains("open") { return true; }
  if text.contains("private") || text.contains("internal") || ... { return false; }
  ```
  C# `protected internal` is exported in C# semantics (member visible to derived classes outside the assembly), but matches `"protected"` first → returns false. Same for `private protected` in C# — semantically restricted but the order check returns false.
- **Expected:** modifier matching should be token-level, not substring. The grammar's `modifiers` node has child modifier nodes; iterate those and check exact equality.
- **Suggested fix shape:** walk `modifiers` children with `node.walk()` and compare `child.kind()` (or its full text) against an exact set rather than `contains`. Languages that pile modifiers into a flat text blob can still use a whitespace-tokenized split.
- **Out of scope:** the broader question of which modifier vocabularies count as "exported" for each language — Swift's `internal` (module-default) being marked not-exported is debatable.

### F5 — Missing helper binary produces a ParseFailure per file, not a single fatal error

- **Severity:** medium
- **Reproducer:** delete or rename the helper binary, run `leonard index` on a project with 1000 Java/Ruby/etc. files.
- **Observed:** `treesitterExtractorPath()` returns `ErrTreesitterExtractorUnavailable` for every file. `indexAbs` at indexer.go:600 treats this as just another extract error, appending a ParseFailure per file with the message `"parse: leonard-extract-treesitter binary not found (build via cargo build --release inside internal/parse/treesitter/, set LEONARD_TREESITTER_EXTRACTOR, or install the binary on PATH)"`. A 1000-file project produces 1000 identical ParseFailures. CLI summary becomes unreadable. Same issue affects ErrRustExtractorUnavailable per `internal/parse/rust.go`.
- **Expected:** The first time the extractor is found-missing, log it once, mark the language unavailable for the rest of the walk, and either (a) silently skip subsequent files of that language or (b) emit a single "Java extractor not built; skipping 1000 files" line.
- **Suggested fix shape:** Cache the lookup result inside `Indexer` (or process-globally with `sync.Once`). On first ErrTreesitterExtractorUnavailable, record one parseFailure with a counted summary and short-circuit subsequent calls for the same language.
- **Out of scope:** whether build instructions in the error message should be terser.

### F6 — C/C++ queries miss pointer-returning functions and out-of-line definitions

- **Severity:** medium
- **Reproducer:**
  ```c
  int* make_array(int n);
  static const char *get_name(void);
  typedef int (*callback_t)(int, int);
  ```
  ```cpp
  namespace MyNs { class Foo { public: Foo(); ~Foo(); }; }
  void MyNs::Foo::method() {}
  ```
- **Observed:** From the C query, `make_array` and `get_name` are missed — the function_declarator's declarator is `pointer_declarator(identifier ...)`, not directly `identifier`. The typedef `callback_t` is missed — its declarator is `function_declarator`, not `type_identifier`. From the C++ query, constructors (`Foo()`), destructors (`~Foo()`), copy constructors, operator overloads, and out-of-line member definitions (`MyNs::Foo::method`) are all missed.
- **Expected:** Pointer-returning functions and qualified out-of-line definitions are essential symbols in any real C/C++ codebase — these aren't edge cases.
- **Suggested fix shape:** add alternative query patterns that match through `pointer_declarator` recursively for C/C++ function definitions, and add patterns for `destructor_name`, `operator_name`, and `qualified_identifier` declarators in C++.
- **Out of scope:** per-language query refinement is explicitly a separate lane — flagging this here only because it's the dispatcher's job to load the right query.

### F7 — Dart constructors, getters, setters, named constructors not captured

- **Severity:** medium
- **Reproducer:**
  ```dart
  class Foo {
    Foo();
    Foo.named();
    int x;
    void doStuff() {}
    int get bar => x;
    set bar(int v) => x = v;
  }
  ```
- **Observed:** Only `Foo` (the class) and `doStuff` (a function_signature) are extracted. `Foo()`, `Foo.named()`, getters, setters all missing.
- **Expected:** these are first-class member kinds.
- **Suggested fix shape:** add captures for `constructor_signature`, `named_constructor_signature`, `getter_signature`, `setter_signature` in DART_QUERY.
- **Out of scope:** per-language refinement detail; the dispatcher's role here is just that the missed kinds aren't dispatcher bugs.

### F8 — WIT `world` exports/imports not captured

- **Severity:** medium
- **Reproducer:**
  ```wit
  world test {
      export hello: func();
      export goodbye: func();
  }
  ```
- **Observed:** Only the `test` world itself is captured. `hello` and `goodbye` are missed because the query targets `func_item`, but `export hello: func()` likely parses as a different node type (export_item or similar). The world's actual exported functions vanish.
- **Suggested fix shape:** add capture for export_item / import_item nodes inside world.
- **Out of scope:** WIT grammar details.

### F9 — Erlang `-export([...])` visibility not honored

- **Severity:** low
- **Reproducer:**
  ```erlang
  -module(my_mod).
  -export([hello/1]).

  hello(Name) -> io:format("Hi ~p~n", [Name]).
  internal_helper(X) -> X + 1.
  ```
- **Observed:** Both `hello` and `internal_helper` have `exported=false`. The `-export` declaration is captured as a separate `module_attribute` symbol (kind=type, name=`my_mod` — actually the name field of the module attribute is the module name, not the function being exported). is_exported can't see into `-export` lists because that information lives in a sibling top-level node, not on the function node.
- **Expected:** `hello` should be `exported=true` because it appears in `-export([hello/1])`.
- **Suggested fix shape:** Erlang needs a two-pass approach — collect `-export([...])` lists first, then mark matching `fun_decl` nodes exported. The current architecture (one-pass capture-name-driven extract) doesn't support this directly. Could be punted to a per-language post-pass.
- **Out of scope:** broader pattern of grammars that express exportedness at the top level rather than per-symbol (Python `__all__`, JS `export {...}` list at end of file).

### F10 — Swift overloaded `init` methods produce identical qnames

- **Severity:** low
- **Reproducer:**
  ```swift
  class Greeter {
      init(prefix: String) { ... }
      init?(_ optional: Int) { return nil }
  }
  ```
- **Observed:** Two symbols both with qname `sample.Greeter.init`, identical kind, identical signature `fn init()`. The store probably treats them as distinct rows on (id, file_path) but on (qname, kind) they collide. Same applies to Java overloaded constructors.
- **Expected:** overload-aware disambiguation, or at least a per-line component in qname.
- **Suggested fix shape:** either (a) embed parameter types in the synthesized signature so `init(String)` and `init(Int?)` differ, or (b) append a line-number suffix on collision. Or accept this as a known limitation and document.
- **Out of scope:** the broader signature-as-source-of-truth design.

### F11 — `module_name` path-stem extraction has surprising behavior with dots in filenames

- **Severity:** low
- **Reproducer:**
  ```sh
  echo 'class Foo {}' | "$BIN" --lang java 'Test.helper.java'
  # → module "Test.helper", qname "Test.helper.Foo"
  echo 'class Foo {}' | "$BIN" --lang java '.'
  # → module "", qname ".Foo"
  ```
- **Observed:** main.rs:1164 uses `rsplit_once('.')` which strips only the last extension. A file `Test.helper.java` keeps `Test.helper` in the module. A path of just `.` produces a degenerate `.Foo` qname.
- **Expected:** unclear what's right — `Test.helper.java` could reasonably yield `Test.helper` (the current behavior) or `Test_helper` (after sanitization). The `.`-path case should probably never happen in practice (the Go side passes project-relative paths), but is degenerate.
- **Suggested fix shape:** define module-name semantics in the design doc. Probably: replace dots-in-stem with underscores or strip everything before the last dot when the stem itself contains dots. Or accept the current behavior and document.
- **Out of scope:** Windows path-separator handling (`module_name` only splits on `/`).

### F12 — Helper binary stdout size unbounded — pathological input amplification 8x

- **Severity:** low (mitigated by indexer's 8 MiB pre-cap)
- **Reproducer:**
  ```sh
  # 11.6 MB Java file of tightly-packed empty methods → 95 MB JSON output
  python3 -c "
  print('class C{')
  for i in range(800000): print(f'void m{i:08d}(){{}}', end='')
  print('}')" > /tmp/pack.java
  "$BIN" --lang java pack.java < /tmp/pack.java | wc -c
  ```
- **Observed:** ~8x output amplification (each `void m12345678(){}` is 19 bytes, produces ~140-byte JSON record). The Go side reads everything into `bytes.Buffer` via `cmd.Stdout = &stdout` with no size cap. At the 8 MiB input cap, worst-case JSON output is ~64-70 MB; doubled in Go memory during decode → ~140 MB transient allocation per file. RSS measurement of the helper itself: 8 MB input → 418 MB resident set; 11 MB input → 647 MB resident.
- **Expected:** the 8 MiB indexer cap bounds this in practice. But the helper's own memory bloat (50x amplification: 11 MB input → 647 MB RSS) is significant when many extractors run concurrently. A future change that raises the file cap would amplify this.
- **Suggested fix shape:** cap stdout in the Go reader (`io.LimitReader` wrapping stdout). 100 MB feels safe; would catch helper bugs that emit unbounded output. Also worth documenting the Rust-side memory cost.
- **Out of scope:** whether 8 MiB is the right file cap (covered by bughunt-4 caps).

### F13 — Query compiled at every invocation (per-file overhead, not cached)

- **Severity:** informational
- **Observed:** main.rs:991 calls `Query::new(&lang.grammar, lang.query_src)` inside `extract()` — once per binary invocation. Since the binary is fork+exec'd per file, every file pays the query-compile cost. On a 5000-file Java project, that's 5000 compiles. Measured per-invocation startup ~7ms total for a trivial file, so query compile is a small fraction; not a real perf problem at current scale.
- **Expected:** Either accept the cost (it's bounded by ~10ms per file regardless) or use a long-running helper with a query cache (would require redesigning the wire protocol). Subprocess startup dwarfs query compile, so caching alone wouldn't help much.
- **Suggested fix shape:** Defer. If/when the architecture moves to a long-lived helper with batched requests, the query cache becomes worth it.
- **Out of scope:** the long-lived-helper rearchitecture is a separate, larger conversation.

### F14 — `language()` vs `LANGUAGE` convention drift across 28 grammars

- **Severity:** informational
- **Observed:** main.rs's `Language::lookup` uses three different grammar-binding conventions across crates:
  - `tree_sitter_java::LANGUAGE.into()` — most modern crates
  - `tree_sitter_dart::language()`, `tree_sitter_wit::language()` — older convention
  - `tree_sitter_glsl::LANGUAGE_GLSL.into()`, `tree_sitter_hlsl::LANGUAGE_HLSL.into()`, `tree_sitter_php::LANGUAGE_PHP.into()` — multi-grammar-per-crate convention
  
  Tree-sitter is migrating to the `tree-sitter-language` crate as the ABI-decoupled binding. Older crates (Dart 0.0.4 — last published years ago) depend directly on `tree-sitter` not `tree-sitter-language`. Cargo.lock currently has only one `tree-sitter` version (0.25.10) — they unified — but a future grammar that pins an older `tree-sitter` (like the deferred Smithy 0.0.1 on tree-sitter 0.20) would split the dep tree and create runtime ABI mismatches.
- **Expected:** the bare convention drift isn't a bug, but it's a maintenance burden. When a grammar's next release bumps the convention (older crates often migrate to `LANGUAGE` const at some point), the dispatcher has to be updated by hand.
- **Suggested fix shape:** add a Cargo.lock check in CI (`cargo tree --duplicates --depth=0`) to catch the moment a second `tree-sitter` runtime version appears. Document the convention-per-grammar in the source as a table so the next bump goes smoothly.
- **Out of scope:** the broader question of pinning vs floating grammar versions.

### F15 — `read_to_string` requires whole-input materialization; no streaming or partial read

- **Severity:** informational
- **Observed:** main.rs reads stdin to completion before parsing. For an 8 MiB Java file producing 95 MB of JSON, the entire input lives in memory before tree-sitter parses, then the entire tree lives in memory, then the entire JSON output lives in memory. Helper RSS hits 600+ MB for pathological inputs.
- **Expected:** acceptable trade-off for now — tree-sitter parses bytes-at-once, not streaming. But if multiple `ExtractTreeSitter` calls run concurrently from the same Go process (e.g. via `errgroup` in the indexer walk), the host can quickly accumulate gigabytes of resident memory.
- **Suggested fix shape:** document the memory profile. Consider bounding the indexer's concurrent tree-sitter extractions via a semaphore (separate from the per-file cap). Could also explore tree-sitter's incremental parsing or chunked input, but those are larger architectural changes.
- **Out of scope:** general indexer concurrency tuning.

### F16 — Helper accepts `--lang foo --lang bar` (last-wins) silently; no error on duplicate flag

- **Severity:** informational (cosmetic)
- **Reproducer:** `echo 'class Foo {}' | "$BIN" --lang java --lang ruby Foo.java` → returns `[]` (ruby grammar, no Java matches).
- **Observed:** parse_args at main.rs:945 silently lets `--lang` be specified twice; the second one wins. No error.
- **Expected:** duplicate flag should error so a buggy caller surfaces immediately.
- **Suggested fix shape:** `if lang.is_some() { return Err("--lang specified twice"); }` in the match arm.
- **Out of scope:** general CLI hygiene.

### F17 — Stdin invalid-UTF8 stderr message ends up in Go error chain unhelpfully

- **Severity:** low (related to F1)
- **Observed:** When the helper exits 1 due to UTF-8 error, the Go side surfaces: `tree-sitter extract failed: exit status 1: leonard-extract-treesitter: read stdin: stream did not contain valid UTF-8`. This is recorded as a ParseFailure and shown in the CLI summary. It looks like a Leonard infrastructure failure ("can't even read stdin"), not a per-file data issue ("this file isn't UTF-8").
- **Expected:** classify as ParseError (exit 2) so the user sees "ParseError: file not UTF-8" or similar.
- **Suggested fix shape:** see F1.

### F18 — Per-file ParseFailure on a missing extractor floods CLI summary

- **Severity:** medium (duplicate of F5 — flagging here so it's clear the impact spans both the missing-binary case and the per-file UTF-8 case)
- **See F5 for details.**

## Things that worked

Specific behaviors I verified are working correctly:

- **Concurrent extraction safety**: 50 concurrent shell invocations of the helper binary all succeed; tree-sitter trees are per-process so no shared mutable state. Per-call isolation by construction.
- **WaitDelay + context timeout**: A helper that writes partial JSON then hangs gets killed at ctx timeout; the partial stdout is not JSON-decoded (Go returns the timeout error first, exit code != 2). Tested with a `printf '[{...part' && sleep 60` stub.
- **8 MiB pre-subprocess file cap**: `indexer.go:571` rejects files >8 MiB *before* spawning the helper. Pathological input bounded.
- **Exit code 2 ParseError contract**: helper emits `{"error":"ParseError","detail":"..."}` JSON on stdout and exits 2; Go side at `treesitter.go:114-129` decodes it and surfaces as `ParseError: <detail>`.
- **Unknown language detection**: `ExtractTreeSitter("not-a-real-language", ...)` returns the right error.
- **`_`-prefixed capture filtering**: Elixir's `@_def`, Starlark's `@_arg`, CMake's `@_cmd`, HCL's `@_kind` all correctly skipped in kind-capture selection.
- **Elixir #eq?/#any-of? predicate filtering**: tested with non-def calls inside a defmodule body; only def/defp/defmacro/defmodule/defprotocol calls are captured.
- **BOM, CRLF, empty input**: helper handles all three correctly. Empty input → `[]`.
- **Unicode identifiers**: multi-byte UTF-8 identifiers (`class Åé`, `void m中()`) survive into qnames correctly.
- **NUL bytes in identifiers**: serde_json correctly escapes embedded NULs as ` ` in JSON output (some grammars allow them in word/identifier tokens — Bash with quoted function names did).
- **Sequential subprocess startup**: ~7ms per invocation including grammar load, query compile, parse, JSON serialize for a trivial file. Acceptable per-file cost.
- **WIT, Solidity, CMake, Make, Starlark**: end-to-end tested with realistic samples — parent-folding works for the cases that have `field_name="name"` children (WIT, Solidity).
- **Per-language wrapper consistency**: every `ExtractXxx` wrapper in `treesitter.go` is a one-line shim — uniform contract, no per-language drift in the Go-side handshake.
- **Env override precedence**: `LEONARD_TREESITTER_EXTRACTOR` is honored (verified by existing test `TestExtractJava_HonorsEnvOverride`); same precedence as the syn extractor.
- **No tree-sitter version split in Cargo.lock**: only one `tree-sitter` (0.25.10) and one `tree-sitter-language` (0.1.7) resolved — no ABI drift right now.
- **Stdout no trailing newline**: helper writes `]` as the last byte; Go's `json.Unmarshal` is fine with this; consistent across all language outputs.

## Open questions

- **What's the right `kind` for SQL columns?** Currently `const` — but column-as-symbol semantics are weird (find_symbol("id") returns N hits across N tables). Worth a design discussion: is the SQL surface "schema graph" or "named flat list of declarations"?
- **Should Ruby methods default to `exported=true` since Ruby's default visibility is public?** The current `is_exported` heuristic returns false for methods without explicit modifiers. This conflicts with the actual Ruby visibility model. Whether to fix depends on what `exported=true` means semantically.
- **Should the helper become long-lived?** Subprocess startup is ~7ms, query compile is ~1ms. Over a 10000-file project that's 70-80 seconds of pure overhead. A long-lived helper (one process per language, fed via stdio length-prefixed messages) could eliminate this. Requires designing the wire protocol and is a bigger change than the v0.19 dispatcher.
- **What's the canonical answer for grammar conventions (LANGUAGE vs language())?** Future bumps may require touching `Language::lookup` for many crates simultaneously. Documenting the per-grammar convention in a table inside main.rs would reduce upgrade pain.
- **Should the per-call timeout vary by language?** Currently 30s for all languages. Tree-sitter-cpp with deep templates or tree-sitter-typescript with massive JSX can be slow; tree-sitter-bash is fast. No way to know without benchmarking the slow path on real repos.

## Out of scope for this investigation

- Per-language query refinement (other lanes, separate finding scopes).
- The broader language coverage debate (which grammars to add/remove).
- Cross-compilation pipelines (Linux ARM, macOS x86_64) — flagged but not exercised; would need a build matrix probe.
- Long-lived helper rearchitecture — significant design change, would need its own brief.
- The Rust-side test gap (zero `cargo test` cases) — could be filled in a follow-up but isn't a dispatcher bug.
- Bughunt-4 caps F4 RSS bounds — that lane covered the 8 MiB cap; F12 here just notes the helper-side amplification.
- Scratch files used in this investigation live at `/tmp/sample.*`, `/tmp/big.java`, `/tmp/pack.java`, etc. None were committed.
