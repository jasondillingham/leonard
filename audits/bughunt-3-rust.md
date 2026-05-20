# Bug Hunt #3 — rust

## Summary

The Rust lane (Go-side `internal/parse/rust.go` + syn-based helper at `internal/parse/rust/src/main.rs`) is structurally sound and matches the v0 shallow-extraction contract used by the Python/TS lanes. It survived a 500-goroutine concurrent fan-out, handles Unicode identifiers, BOM, CRLF, and large inputs. Dogfooded against `clap-rs/clap` (330 files / 3,676 symbols).

That said, I found **one medium-severity correctness bug** (methods on non-`Type::Path` self_ty are silently dropped, e.g. `impl Display for (i32, i32)` and `impl Display for &Container`), **one cross-language inconsistency** in `start_line` semantics (Rust includes attribute/doc-comment lines while Python/TS exclude them), and several lower-severity hazards around install-time DX, stale-binary detection, foreign-type qname collisions, and resource use on very large files. None affect correctness on idiomatic small/medium Rust files — these are edge cases that real codebases do hit but ripgrep's idiomatic style happened not to.

## Findings

### F1 — Methods on non-`Type::Path` self_ty are silently dropped
- **Severity:** medium
- **Reproducer:**
  ```rust
  pub struct Container<T> { pub items: Vec<T> }

  impl<T> std::fmt::Display for Container<T> {
      fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { Ok(()) }
  }

  // Methods below this line are LOST.
  impl<'a, T> std::fmt::Debug for &'a Container<T> {        // Type::Reference
      fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { Ok(()) }
  }
  impl std::fmt::Display for (i32, i32) {                    // Type::Tuple
      fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { Ok(()) }
  }
  impl<const N: usize> std::fmt::Debug for [u8; N] {         // Type::Array
      fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { Ok(()) }
  }
  ```
  Pipe to the helper — only the first `fmt` (from the `Type::Path` impl) is emitted.
- **Observed:** Methods inside `impl Trait for X` where `X` is anything other than `syn::Type::Path` are completely missing from the output. `main.rs:207` only matches `syn::Type::Path`, leaves `current_impl` empty, and `visit_impl_item_fn` then defensively returns early because `current_impl.is_empty()`.
- **Expected:** All impl methods should be extracted. The qname's "Type" component for non-Path self types is a design call — could use the rendered token stream (`"&Container"`, `"(i32, i32)"`, `"[u8; N]"`), a synthetic tag, or just skip the qname prefix and emit at module scope. Anything that preserves the symbol.
- **Suggested fix shape:** Extend the `syn::Type::Path` arm to fall through to a stringified-token representation for the other variants. Or at minimum, log a per-file note so the user knows symbols were dropped (currently silent — there's no parse error to surface).
- **Out of scope for this investigation:** Whether the chosen qname format collides with anything else.

### F2 — `start_line` includes attributes and doc-comments; Python/TS exclude them
- **Severity:** medium
- **Reproducer:**
  ```rust
  /// Three
  /// lines
  /// of doc
  pub fn documented() {}

  #[cfg(test)]
  #[allow(dead_code)]
  pub fn attributed() {}
  ```
  Extractor returns `documented` with `start_line=1`, `attributed` with `start_line=6`. The `fn` keywords are actually on lines 4 and 8.
- **Observed:** `proc_macro2::Span::start()` reports the span of the whole item, including all leading attributes and doc-comments. The current code at `main.rs:74-78` uses `node.span().start()` directly with no attribute stripping.
- **Expected:** The Python extractor uses `ast.AST.lineno`, which for `FunctionDef`/`ClassDef` points at the `def`/`class` keyword, **not** the decorator list. The TS extractor (`internal/parse/typescript.go:541-590`) calls `parseDecorators` first and only then captures `startLine` from `p.peek().line`. Cross-language consistency suggests Rust should match.
- **Suggested fix shape:** For each item, iterate the attributes (`syn::ItemFn::attrs`, etc.) and check whether any attributes exist. If yes, use the span of the first non-attribute token (i.e. the `fn`/`pub`/`struct` keyword); if no, the current behavior is already correct. Alternatively, render the span of `sig.fn_token` / `node.ident` and use *that* line as the start.
- **Out of scope for this investigation:** Whether MCP/CLI consumers actually display the line number prominently enough for users to notice the off-by-N. The `Line:` field is returned by `find_symbol` (server.go:142) so it's user-visible.

### F3 — Foreign-type impls collide with local-type qnames
- **Severity:** medium
- **Reproducer:**
  ```rust
  pub struct Local;

  trait MyTrait { fn show(&self); }

  impl MyTrait for Vec<u8> {                                       // foreign type
      fn show(&self) { println!("vec"); }
  }
  impl std::fmt::Display for std::collections::HashMap<String, u32> {
      fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { Ok(()) }
  }
  ```
  Pipe with prefix `foreign`:
  ```json
  [
    {"name":"Local","qualified_name":"foreign.Local","kind":"type",...},
    {"name":"show","qualified_name":"foreign.Vec.show","kind":"method",...},
    {"name":"fmt","qualified_name":"foreign.HashMap.fmt","kind":"method",...}
  ]
  ```
- **Observed:** `main.rs:208` extracts `seg.ident` from the **last** path segment. `std::collections::HashMap` becomes `HashMap`; the path of the trait being implemented and the path of the foreign type are both discarded. The qname implies `HashMap` is defined in the same file as `Local`, which it isn't.
- **Expected:** Either record the full path of the self_ty (e.g. `foreign.std::collections::HashMap.fmt`) or tag impl-on-foreign-type methods differently so a `find_symbol HashMap` doesn't return a misleading hit.
- **Suggested fix shape:** Render the full path (joined with `::`) for impl method qnames. Or, since shallow extractors don't try to resolve, add a marker like `foreign.~HashMap.fmt` so callers can spot non-local impls. (Bikeshed: the cleanest answer is to not emit methods on non-local types at all, but you can't tell "local" vs "foreign" from a single file — the parser is module-scoped.)
- **Out of scope for this investigation:** Whether this also bites the Python lane (it doesn't — Python's `class` is unambiguous) or TS (mixed — `class extends Foo {}` only emits methods on the new class, not Foo).

### F4 — Methods on duplicate `#[cfg]`-gated items emit duplicate qnames
- **Severity:** low (matches Python/TS behavior; not Rust-specific)
- **Reproducer:**
  ```rust
  #[cfg(unix)]
  pub fn os_specific() -> &'static str { "unix" }

  #[cfg(not(unix))]
  pub fn os_specific() -> &'static str { "windows" }
  ```
  Output emits two rows with `qualified_name = "cfg_dup.os_specific"`. The store has no UNIQUE constraint on `qualified_name` (only an index — `internal/store/store.go:223-224`), so both rows persist.
- **Observed:** `find_symbol os_specific` returns two matches, both pointing at the same file but at different line ranges. Users won't immediately know which one matches their build target.
- **Expected:** Probably fine as-is — both definitions ARE in the source. But the cross-language symbol vocabulary doesn't have a "cfg-gated" hint, so the surface looks like a real duplicate. Worth at least documenting.
- **Suggested fix shape:** No fix needed for v0. Could later: include the cfg expression in the signature when one is present (`fn os_specific() [cfg(unix)]`) so the rows differ in a user-visible way.
- **Out of scope for this investigation:** Whether this breaks the post-edit hook's "did this edit add the symbol?" check.

### F5 — `pub use` re-exports invisible to the index
- **Severity:** informational
- **Reproducer:** Many `mod.rs` files in the wild (40 of 329 in clap) are nothing but re-exports:
  ```rust
  pub use action::ArgAction;
  pub use arg::Arg;
  pub use command::Command;
  ```
- **Observed:** Zero symbols emitted; the file is recorded as `language=rust` with no associated symbols. A user searching `find_symbol Arg` finds it via `arg.rs` but `mod.rs` looks empty.
- **Expected:** Matches Python/TS shallow contract — re-exports aren't first-class declarations. Worth confirming in DESIGN.md.
- **Suggested fix shape:** Document in the Rust lane brief that re-export-only files produce zero symbols by design. Or: emit a `var`/`use` symbol pointing at the original.
- **Out of scope:** Same shape question for `pub use foo::*` glob re-exports.

### F6 — Trait method declarations not extracted
- **Severity:** informational
- **Reproducer:**
  ```rust
  pub trait Service {
      fn name(&self) -> &str;
      fn description(&self) -> String { String::from("default") }
      fn build() -> Self where Self: Sized;
  }
  ```
  Output: only `Service` (kind=interface). The three `fn` declarations inside the trait are silently dropped.
- **Observed:** `visit_item_trait` is overridden (in spirit — it just emits the trait then falls through to default visit) but no `visit_trait_item_fn` handler exists. The default walker descends into trait items but the extractor has no emitter for them.
- **Expected:** Consistent with the v0 brief ("methods one level deep inside impl blocks"). Trait method declarations aren't mentioned. So the current behavior is intentional. But it means searching `find_symbol description` from the example above returns no hits.
- **Suggested fix shape:** No fix for v0. Future: add `visit_trait_item_fn` with qname `Trait.method` and a new `kind` (perhaps `trait_method` or reuse `method`).
- **Out of scope:** Whether default-method implementations should be emitted differently from required signatures.

### F7 — Associated consts and types inside impls are dropped
- **Severity:** informational
- **Reproducer:**
  ```rust
  pub struct Holder;
  impl Holder {
      pub const VERSION: &'static str = "1.0";
      pub const MAX: u32 = 100;
      pub fn new() -> Self { Holder }
  }
  ```
  Output emits `Holder` and `Holder.new` — but **not** `Holder.VERSION` or `Holder.MAX`.
- **Observed:** `visit_item_impl` only iterates `ImplItem::Fn` (`main.rs:213-217`); `ImplItem::Const` and `ImplItem::Type` are ignored.
- **Expected:** Matches v0 brief ("methods one level deep"). Constants aren't mentioned but a top-level `const FOO` would be emitted; an associated `const FOO` won't be. Asymmetric.
- **Suggested fix shape:** Symmetric extension: handle `ImplItem::Const` (emit as `Holder.VERSION`, kind=`const`) and possibly `ImplItem::Type` (emit as `Holder.Item`, kind=`type`).
- **Out of scope:** Same question for trait associated consts/types.

### F8 — No version handshake; stale helper binary used silently
- **Severity:** medium
- **Reproducer:**
  1. Edit `internal/parse/rust/src/main.rs` (e.g. add a new visit handler).
  2. Don't run `cargo build --release`.
  3. Run `leonard index` on a Rust project.
- **Observed:** Go-side execs whatever binary is in `target/release/leonard-extract-rust` (which is now stale). No warning. The user sees output consistent with the old code and assumes their edit is wrong.
- **Expected:** Either the Go side should detect drift (compare mtime of main.rs vs the binary, or have the helper emit `--version` and compare to a hard-coded constant), or it should automatically rebuild on drift.
- **Suggested fix shape:** Cheap option: at `rustExtractorPath()` time, if running from a source-tree fallback, stat the source files and the binary; if any source file is newer, log a "rust helper is older than source — rebuild?" warning to stderr. Or: have `main.rs` expose a constant version, the Go side runs the binary with `--version` once, compares to its own embedded expected-version, and falls back to `ErrRustExtractorUnavailable` (with a "stale binary" message) on mismatch.
- **Out of scope:** Whether to auto-invoke `cargo build` from Go (probably no — adds a hard cargo dependency at runtime).

### F9 — `LEONARD_RUST_EXTRACTOR` accepts non-executable files; error surface is noisy
- **Severity:** low
- **Reproducer:** Set `LEONARD_RUST_EXTRACTOR=/usr/bin/false` (real file, exit 1) and run `leonard index` against the clap dogfood corpus. Result: every Rust file (329 of them) produces `parse failed: rust extract failed: exit status 1:`. With 329 files, the "leonard: N file(s) failed to parse" summary shows 10 samples then "...and 319 more".
- **Observed:** The stat-only check in `rustExtractorPath()` (`rust.go:74`) passes whenever `os.Stat` succeeds, regardless of execute permission. A bogus env var that happens to point at an existing-but-broken binary produces an error per file, not a single top-level "rust extractor isn't working" message.
- **Expected:** A single top-level error like "rust extractor at /usr/bin/false: exit status 1" with a sample of files that would have been processed, rather than N repetitions.
- **Suggested fix shape:** Cache the first error from `rustExtractorPath()` or first exec-fail at indexer level, switch the lane into "unavailable" mode after the first failure, and surface it once. Same shape applies to Python lane.
- **Out of scope:** Whether to pre-check executability (`os.Stat` + mode bit 0o111) — partial fix, doesn't catch exit-1 binaries.

### F10 — Memory use scales ~150x source size; large files can OOM or timeout
- **Severity:** informational/low
- **Reproducer:**
  ```bash
  # Generate a 22MB synthetic Rust file with 500k functions
  python3 -c "for i in range(500000): print(f'pub fn fn_{i}(x: u32) -> u32 {{ x + {i} }}')" > /tmp/huge.rs
  /usr/bin/time -l leonard-extract-rust prefix < /tmp/huge.rs > /dev/null
  ```
- **Observed:** 22 MB input → ~3.0 GB peak RSS, 5.6s runtime. 2.1 MB input → ~325 MB peak RSS, 0.43s. Linear scaling ~150x.
- **Expected:** Real Rust files are rarely >100 KB, so 15 MB resident is fine. But generated Rust files (e.g. `prost` protobuf bindings, `tonic` codegen output, large `lalrpop` parsers) can hit 10+ MB. Those would chew 1.5 GB of RAM per file and risk hitting the 30s timeout on slow hosts.
- **Suggested fix shape:** Possibly add a size-cap that returns a parse failure (clearly labeled) for source >= some threshold (5 MB? 10 MB?). The walker already skips `target/` so generated files are usually filtered, but `build.rs`-emitted code under `src/` isn't.
- **Out of scope:** Whether syn 2 can be configured to use less memory (probably not — full feature set is needed for correctness).

### F11 — `read stdin` and `serialize` errors exit code 1, not 2; Go surface is verbose
- **Severity:** low
- **Reproducer:**
  ```bash
  printf '\xc3\x28' | leonard-extract-rust x  # invalid UTF-8
  # stderr: "read stdin: stream did not contain valid UTF-8"
  # exit 1
  ```
- **Observed:** Go side at `rust.go:128-135` special-cases exit code 2 ("clean parse error", emits just the stderr line). Exit 1 falls through to `"rust extract failed: exit status 1: read stdin: stream did not contain valid UTF-8"` — more verbose but the user-facing message is still clear.
- **Expected:** Not really broken. Just inconsistent: a parse error is concise ("line 5: ParseError: expected `;`") while a read error is `"rust extract failed: exit status 1: ..."`. Could collapse the latter onto the same single-line format.
- **Suggested fix shape:** Either also special-case exit 1 with `"%s"`-format-of-stderr, or change `main.rs` to return exit code 2 for any input-side issue. The latter is risky because exit-2 conflates "file unparseable" (user-fixable: rewrite Rust) with "input pipe broken" (probably an indexer bug). Better to extend Go side.
- **Out of scope:** Same pattern in Python lane.

### F12 — `Cargo.lock` is gitignored; non-reproducible builds across collaborators
- **Severity:** informational
- **Reproducer:** `cat internal/parse/rust/.gitignore` shows `Cargo.lock`. Each developer's `cargo build --release` resolves syn/proc-macro2/serde/serde_json to whatever-was-latest-on-crates.io that day.
- **Observed:** For an application/binary crate (which this is — `[[bin]]` in Cargo.toml, not `[lib]`), Rust community convention is to **commit** Cargo.lock so builds are reproducible. Library crates omit it; binaries include it. The current gitignore matches the library convention.
- **Expected:** Two developers running `cargo build --release` a month apart could end up with different syn versions, different bug surfaces, different output. For a parser, that's a real source of "works on my machine".
- **Suggested fix shape:** Remove `Cargo.lock` from `.gitignore`, commit a current lockfile.
- **Out of scope:** Whether to also pin the syn version more tightly than `^2`.

### F13 — No `rust-version` MSRV declared in Cargo.toml
- **Severity:** informational
- **Reproducer:** `grep rust-version internal/parse/rust/Cargo.toml` → nothing. A user on Rust 1.65 (random old version) running `cargo build --release` will get whatever syn 2 demands (currently 1.61+, may rise).
- **Observed:** No MSRV field. Implicit minimum is whatever the dependency tree needs.
- **Expected:** Either pin an MSRV with `rust-version = "1.74"` (or whatever syn 2.0.x requires) so `cargo build` errors out clearly on too-old Rust, or document the minimum elsewhere.
- **Suggested fix shape:** Add `rust-version = "<actual minimum>"` to `[package]` in Cargo.toml.
- **Out of scope:** Cross-platform compatibility (the Windows path in `rust.go:89-91` looks correct but I didn't run on Windows).

### F14 — Macro-generated items invisible to index (e.g. `make_struct!(Foo)`)
- **Severity:** informational
- **Reproducer:**
  ```rust
  macro_rules! make_struct {
      ($name:ident) => { pub struct $name { pub value: i32 } };
  }
  make_struct!(GeneratedFoo);
  ```
  Output: empty array. `GeneratedFoo` is invisible.
- **Observed:** syn parses macro invocations as `Item::Macro`, which has no `visit_item_*` handler in our extractor. The macro body isn't expanded (you'd need rustc / a procedural-macro host). `macro_rules!` definitions themselves are also dropped.
- **Expected:** This is the syn limitation; matches the Python lane's lack of `exec()`-handling. Documented in the brief ("currently SKIPPED").
- **Suggested fix shape:** None for v0. Worth mentioning in user-facing docs: "Symbols generated by declarative macros and proc macros are not indexed."
- **Out of scope:** Whether to detect-and-report common macro-generation patterns (e.g. `tonic::include_proto!`, `lalrpop_mod!`) so the user knows symbols are missing.

### F15 — `include!()` and `#[path = "..."]` are silently ignored
- **Severity:** informational
- **Reproducer:**
  ```rust
  include!("nonexistent.rs");
  #[path = "external.rs"]
  mod ext;
  pub fn after_include() {}
  ```
  Output: only `after_include`. The `include!` doesn't even attempt to resolve the path, and `#[path]` is just a `mod` declaration we skip.
- **Observed:** Matches v0 shallow-index design — we don't follow includes/paths.
- **Expected:** Fine for v0. Would silently miss symbols in projects that use `include!()` heavily (e.g. tonic-generated proto modules).
- **Suggested fix shape:** None for v0; document the limitation.
- **Out of scope:** Whether the indexer's file walk would pick up the included file anyway (probably yes, as a separate file).

### F16 — Trailing/leading whitespace in `LEONARD_RUST_EXTRACTOR` is trimmed, but newline-only is treated as empty
- **Severity:** informational
- **Reproducer:** `rust.go:69` uses `strings.TrimSpace(os.Getenv(...))`. If a user accidentally has `LEONARD_RUST_EXTRACTOR=$'\n'`, it'll be treated as empty and fall through to PATH lookup. Probably the right behavior; just notable.
- **Out of scope:** N/A.

### F17 — Pattern arguments collapse to `_` in signature rendering
- **Severity:** informational
- **Reproducer:**
  ```rust
  pub fn _odd_pat((a, b): (i32, i32)) -> i32 { a + b }
  pub fn _slice_pat(&[a, b, c]: &[i32; 3]) -> i32 { a + b + c }
  ```
  Signatures rendered as `fn _odd_pat(_)` and `fn _slice_pat(_)`. The destructured names are lost.
- **Observed:** `render_fn_sig` (`main.rs:262-265`) only handles `Pat::Ident`; everything else becomes `_`. Real Rust occasionally uses tuple/struct destructuring in args.
- **Expected:** The brief says signatures stay compact and intentionally skip types — but losing arg names entirely is worse than skipping types.
- **Suggested fix shape:** Render the pat with `quote::ToTokens::to_token_stream().to_string()` when not `Pat::Ident`. Adds a dependency or hand-rolled render but produces `fn _odd_pat((a, b))`.
- **Out of scope:** N/A.

## Things that worked

- **Concurrent fan-out (500 goroutines).** Each shells out independently, all 500 completed in 457ms with zero failures and no resource exhaustion. No subprocess race or shared-resource issues. The wrapper is reentrant.
- **`syn` feature coverage.** GATs (`type Item<'a>;`), const generics (`<const N: usize>`), HRTBs (`for<'a> Fn(...)`), lifetime parameters, where clauses, and `async fn` all parsed cleanly. `full` + `extra-traits` + `visit` is the right feature set.
- **Unicode identifiers.** `fn 你好() {}`, `struct Café`, `const π` all round-trip through JSON cleanly (UTF-8 preserved end-to-end).
- **BOM, CRLF, lone-CR line endings.** All handled by syn without surprise.
- **Tuple structs / unit structs / brace structs / empty enums.** All emit correctly with `kind=type`.
- **`pub(crate)`, `pub(super)` etc.** Correctly treated as non-exported (matches the doc).
- **Nested modules.** Skipped per design — `visit_item_mod` is no-op. Confirms the v0 shallow contract.
- **Empty input.** Returns `[]` (empty JSON array, exit 0).
- **Inner attributes (`#![...]`).** Handled by syn; lines after them resolve correctly.
- **Existing testdata fixture (`testdata/rust/module.rs`).** Stable — output unchanged from what the test suite expects.
- **Timeout enforcement (`TestExtractRust_TimeoutKillsHungHelper`).** Works as designed; `WaitDelay=500ms` matches the Python lane.
- **Source-tree fallback via `runtime.Caller(0)`.** Works correctly for both in-repo `go test` and `go install`-style installations from the leonard checkout. Only fails for `go install ...@latest` users (who'd see `ErrRustExtractorUnavailable` cleanly).
- **Real-world dogfood (clap).** 330 files indexed → 3,676 symbols, no panics, no parse failures from real Rust. 40 zero-symbol files are all either `pub use`-only re-exports or `macro_rules!`-only files — both consistent with intentional design.

## Open questions

1. **Should the helper handle non-`Type::Path` impl self_ty?** F1 is a real correctness gap but the fix is design work (what's the qname for `impl Display for [u8; 32]`?). Worth a brief design conversation before fixing.
2. **`start_line` semantics.** F2 is a cross-language inconsistency. Picking "first attribute" vs "first keyword" is a v0 spec decision Jason hasn't made — Python's `ast` happens to use the keyword, syn happens to use the first attribute. Aligning will require changing one of them.
3. **Should re-export files (F5) emit `use` symbols?** That's a tractable extension but it's a feature change, not a bug fix.
4. **What's the install path for non-developer users?** If `leonard` ever ships as a public binary, requiring `cargo build` post-install is hostile. Options: embed the helper as an `embed.FS` binary (per-platform), publish to crates.io and have a one-shot `cargo install`, or ship a portable PyOxidizer-style bundled binary. Out of scope here but worth a doc note.
5. **Should Cargo.lock be committed?** F12 — community convention says yes for binary crates. Easy change.
6. **Is the 30s rustTimeout sufficient on slow hardware for medium-size files?** A 22 MB synthetic file took 5.6s on an M-series Mac; on an emulated x86 host or low-end ARM box that could easily 6x. Real production files are smaller, so this is hypothetical.
