# Bug Hunt #4 — rust-round-2

## Summary

Audit of `internal/parse/rust/src/main.rs` (v0.12.0) and the `leonard-extract-rust` helper binary, focused on (a) verifying the round-3 F1/F2/F3 fixes held, (b) re-auditing the deferred F4–F14 items, and (c) probing edge cases the v0.12 fixes themselves may have introduced. **All three v0.12 fixes verified working on the original reproducers and on additional edge cases.** Two existing findings (F4 cfg-gated dupes, F10 ~140x memory blow-up) are still live and bordering on medium. Several previously-noted F-items remain accurate. Two new informational findings surfaced: synthetic-placeholder collisions (`_tuple`/`_array`/`_slice`/`_anon_impl`) and silent loss of generic args / leading `::` / `mut` in `impl_target_name`. Dogfooded against ripgrep (100 files / 2,712 symbols) and serde (208 files / 1,690 symbols); both produced 9 "parse-failure suspects" each, every one a legitimate macro-only or `pub use`-only file with no extractable symbols — not regressions, just a misleading `doctor` label.

Scratch fixtures lived at `/tmp/bughunt4-rust/`. Dogfood checkouts at `/tmp/rust-dogfood` (ripgrep, pre-existing) and `/tmp/serde-dogfood` (fresh shallow clone).

## Findings

### F1 — `impl_target_name` drops generic args, lifetimes, `mut`, and leading `::`
- **Severity:** low (cosmetic/dedup; arguably correct given the qname-as-bucket design)
- **Reproducer:**
  ```rust
  trait Bar { fn bar(&self); }
  pub struct Foo;

  impl Bar for &mut Foo            { fn bar(&self) {} }   // → m.&Foo.bar       (loses mut)
  impl Bar for Box<Foo>            { fn bar(&self) {} }   // → m.Box.bar        (loses <Foo>)
  impl Bar for Box<dyn Bar>        { fn bar(&self) {} }   // → m.Box.bar        (collides with line 4)
  impl Bar for ::std::vec::Vec<u8> { fn bar(&self) {} }   // → m.std::vec::Vec.bar (loses leading ::)
  ```
- **Observed:** `impl_target_name` walks `tp.path.segments` and joins identifiers with `::`, but ignores `PathArguments::AngleBracketed` (the `<Foo>` part of `Box<Foo>`), ignores reference mutability (`tr.mutability` in `syn::TypeReference` is never read), and ignores `path.leading_colon`.
- **Expected:** Either preserve enough disambiguation that distinct types stay distinct (so `Box<Foo>` and `Box<dyn Bar>` get different qnames), or document the lossy bucket-by-base-type behavior as intentional.
- **Suggested fix shape:** `impl_target_name` could `quote!(#ty).to_string()` the entire `syn::Type` rather than hand-walking it. That preserves generic args, lifetimes, mut, leading `::`, etc. — costs a bit of binary size for the `quote` crate. Alternatively, for the cases that matter most: (a) prepend `"mut "` when `tr.mutability.is_some()`, (b) handle `AngleBracketed` args, (c) preserve `leading_colon`. The lifetime case (`&'a Foo`) already lands on `&Foo` — that's fine, lifetimes don't affect monomorphic identity from the user's perspective.
- **Out of scope for this investigation:** Whether the qname is ever interpreted as a typed lookup key (it isn't — it's a display/dedup string).

### F2 — Synthetic placeholders `_tuple`, `_array`, `_slice`, `_anon_impl` collide
- **Severity:** low (matches F1 in spirit but more dramatic — every fn pointer impl, every trait object impl, etc., share one bucket)
- **Reproducer:**
  ```rust
  trait Bar { fn bar(&self); }
  impl Bar for (i32, i32)            { fn bar(&self) {} }   // → m._tuple.bar
  impl Bar for (String, String, u8)  { fn bar(&self) {} }   // → m._tuple.bar (collision)
  impl Bar for ()                    { fn bar(&self) {} }   // → m._tuple.bar (collision)
  impl Bar for fn(i32) -> i32        { fn bar(&self) {} }   // → m._anon_impl.bar
  impl Bar for fn(String)            { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  impl Bar for dyn std::fmt::Display { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  impl Bar for dyn std::fmt::Debug   { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  impl Bar for !                     { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  impl Bar for *const u8             { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  impl Bar for *mut i32              { fn bar(&self) {} }   // → m._anon_impl.bar (collision)
  ```
- **Observed:** A file with three impls on three different trait objects yields three rows all named `_anon_impl.bar` with identical name+qualified_name. `find_symbol bar` returns three indistinguishable hits.
- **Expected:** The qname should encode enough about the self_ty to keep distinct types distinct. If that's not feasible, at minimum the synthetic placeholder name should incorporate something about the type — e.g. `_tuple_3` for arity, `_fn_ptr`, `_trait_obj_Display`, `_never`, `_ptr_const_u8`.
- **Suggested fix shape:** Same as F1 above — use `quote!(#ty).to_string()` for the full self_ty representation, then sanitize to a qname-safe form (replace whitespace and dots, but keep the structural information). Fixes F1 and F2 together.
- **Out of scope for this investigation:** Whether anyone hits this in real code. dyn-trait impls and fn-pointer impls are uncommon outside of stdlib internals and macro-heavy crates.

### F3 — cfg-gated method dupes still produce conflicting qnames (round-3 F4 confirmed live)
- **Severity:** medium
- **Reproducer:**
  ```rust
  #[cfg(unix)]    pub fn platform_fn() -> &'static str { "unix" }
  #[cfg(windows)] pub fn platform_fn() -> &'static str { "windows" }

  pub struct Foo;
  #[cfg(unix)]    impl Foo { pub fn open(&self) -> i32 { 0 } }
  #[cfg(windows)] impl Foo { pub fn open(&self) -> i32 { 1 } }
  ```
- **Observed:** Both `m.platform_fn` and both `m.Foo.open` get persisted to the symbols table. No UNIQUE constraint exists on `(file_path, qualified_name)` (see `internal/store/store.go:212-225` — only PRIMARY KEY id, no compound unique), so duplicates coexist. `verify` / `find_symbol` returns both rows. The DB doesn't distinguish which `cfg` each came from.
- **Expected:** Recoverable correctness issue — the rows exist, just ambiguous on lookup. Worth either (a) deduping by `(qualified_name, signature)` at insert time and surfacing as a single "cfg-gated" symbol, or (b) extending the symbols schema with an optional `cfg_predicate` column so the surface is honest about which platform a method belongs to.
- **Suggested fix shape:** Cheapest fix: at the indexer's insert path, detect duplicate `(file_path, qualified_name)` rows and either merge them (combine start/end line ranges) or skip the second insert with a `parse_warning` log line. Adding a real `cfg_predicate` is bigger and would slot into the post-v1 schema.
- **Out of scope for this investigation:** Whether real codebases actually use enough cfg-gated dupes to make this a usability problem — ripgrep does have a few but they all live in test files. Likely-affected: tokio (heavy on `cfg(loom)`), nix, libc.

### F4 — `pub use` re-exports invisible to the index (round-3 F5 confirmed live)
- **Severity:** low
- **Reproducer:**
  ```rust
  pub use crate::inner::Thing;
  pub use std::collections::HashMap as Map;
  mod inner { pub struct Thing; }
  pub struct Local;
  ```
- **Observed:** Only `Local` is extracted. `Thing` and `Map` produce no rows. An external caller using `outer::Thing` or `outer::Map` would get no index hit, even though those are the canonical surface names.
- **Expected:** Either index `pub use` as a `kind=alias` or `kind=reexport` symbol pointing at the re-exported target, or document this as a v0 limitation.
- **Suggested fix shape:** Add a `visit_item_use` that walks `UseTree::{Name,Rename,Path,Group,Glob}` and emits a row for each terminal name. For `pub use foo::Bar` it would emit `outer.Bar` with kind=alias and signature `pub use foo::Bar`. Glob imports (`pub use foo::*`) would have to be skipped — we can't know what they bring in without resolving.
- **Out of scope for this investigation:** Glob re-exports are unsolvable without a real type-checker; acknowledge and skip.

### F5 — Trait method declarations not extracted (round-3 F6 confirmed live)
- **Severity:** low (design question)
- **Reproducer:**
  ```rust
  pub trait Tr {
      const REQ: u32;
      fn req(&self);
      type AssocType;
  }
  ```
- **Observed:** Only the `Tr` trait itself is indexed (kind=interface). `REQ`, `req`, `AssocType` are dropped.
- **Expected:** Design question — Python/TS extractors index interface body members. The `parse-failure suspects` count in `doctor` already conflates "no extractable symbols" with "parse error"; trait-only files contribute to that confusion.
- **Suggested fix shape:** Add `visit_item_trait` body iteration: for each `TraitItem::{Fn,Const,Type}`, emit a row with `kind=method`/`const`/`type` and qname `prefix.TraitName.member`. Mirrors what `visit_item_impl` already does. Caveat: trait method declarations have no body, so `end_line` would equal `start_line` for `;`-terminated decls. That's fine.
- **Out of scope for this investigation:** Default method *implementations* inside a trait body — they have bodies and would set `end_line` past the `;` mark.

### F6 — Associated consts and types inside impls are dropped (round-3 F7 confirmed live)
- **Severity:** low
- **Reproducer:**
  ```rust
  pub struct Foo;
  impl Foo {
      pub const MAX: u32 = 100;
      pub const MIN: u32 = 0;
      pub type Alias = i32;
      pub fn new() -> Self { Foo }
  }
  ```
- **Observed:** Only `m.Foo.new` is extracted. `MAX`, `MIN`, `Alias` are silently dropped.
- **Expected:** `pub const MAX: u32 = 100` on a public type is a real callable surface (`Foo::MAX`). Indexing it would let `verify_symbol Foo::MAX` succeed.
- **Suggested fix shape:** In `visit_item_impl`'s `for item in &node.items` loop, add arms for `ImplItem::Const` and `ImplItem::Type`. Each emits one row, qname = `method_qname(member_name)`, kind = `const` / `type`. Cost is ~20 lines of code; no new dependencies. Test fixture sits next to the existing `Container<T>` fixture.
- **Out of scope for this investigation:** None — this is just an extension of what visit_item_impl already does for Fn.

### F7 — `union` types are not extracted at all
- **Severity:** low
- **Reproducer:**
  ```rust
  pub union MyUnion {
      pub a: i32,
      pub b: f32,
  }
  ```
- **Observed:** `[]` — no `visit_item_union` override in the `Extractor`, so the default visitor walks into the union body but never emits a top-level row. Indexing skips this symbol entirely.
- **Expected:** `union` is the third nominal type next to `struct` and `enum`. It should index identically — `kind=type`, signature `union MyUnion`. Unions are rare in app code but common in FFI/sys crates.
- **Suggested fix shape:** Add `fn visit_item_union(&mut self, node: &'ast syn::ItemUnion)`, parallel to `visit_item_struct` (the syn AST node has the same shape: `ident`, `vis`, `fields`).
- **Out of scope for this investigation:** Whether to surface union safety (`union` access is `unsafe`) in the signature.

### F8 — Memory use ~144x source size; 5 MB file → 670 MB RSS (round-3 F10 confirmed live)
- **Severity:** medium (was medium in round 3; not addressed in v0.12)
- **Reproducer:**
  ```bash
  # Generate a 5 MB Rust file with a giant enum + 30k functions
  python3 /tmp/bughunt4-rust/gen_5mb.py
  /usr/bin/time -l ./target/release/leonard-extract-rust m < /tmp/bughunt4-rust/big5.rs > /dev/null
  ```
- **Observed:** On an Apple M-series, a 4.68 MB Rust file with 30k symbols took 0.72s real, 0.60s user, and peaked at **673 MB RSS** (= ~144x source size). 30,001 symbols emitted; JSON output 4.7 MB. No timeout, no OOM, but the helper is heavy.
- **Expected:** Indexer-side this is fine for normal Rust files (<100 KB typical, <500 KB even for large hand-written files). The risk window is build-script-generated code or proc-macro-emitted code (prost, tonic, lalrpop, sqlx-macros), which can hit 10+ MB. On a tighter host (CI runner, container w/ 1 GB cgroup), one such file would OOM the helper subprocess, surfacing as a per-file `ParseFailure` to the Go side via the exit-code path. The indexer continues, so this is recoverable — but the user sees a confusing failure.
- **Suggested fix shape:** Two options. (a) Add a source-size cap at the Go side (`internal/parse/rust.go`): if `len(src) > 2 * 1024 * 1024`, return a clean `parse: skipped (too large: 5MB)` failure without launching the helper. Cheap, deterministic. (b) Replace the `Vec<Symbol>` accumulator + `serde_json::to_writer` post-hoc serialize with a streaming JSON writer (emit each Symbol as it's visited). Bigger refactor; saves ~50% memory.
- **Out of scope for this investigation:** Whether the 30s `rustTimeout` is enough on slow hardware — wasn't hit in this round (0.72s for 5 MB).

### F9 — `Cargo.lock` is gitignored; cross-collaborator builds non-reproducible (round-3 F12 confirmed live)
- **Severity:** low (decision question)
- **Reproducer:** `cat internal/parse/rust/.gitignore` → contains `Cargo.lock`. `git ls-files | grep -i "rust/Cargo.lock"` → no match.
- **Observed:** Each collaborator's `cargo build --release` resolves syn/proc-macro2/serde versions independently. A future syn 2.x point release with a behavior change (e.g. how it handles new Rust syntax) would yield silent inter-collaborator drift in the index. The convention for binary crates (which this is) is to **commit** Cargo.lock; the convention for library crates is to gitignore it. `leonard-extract-rust` is a binary, not a library.
- **Expected:** Commit `Cargo.lock` so the helper binary's parse behavior is bit-reproducible across machines.
- **Suggested fix shape:** Remove `Cargo.lock` from `internal/parse/rust/.gitignore`, `git add internal/parse/rust/Cargo.lock`, commit. One-line gitignore change + one new tracked file.

### F10 — No `rust-version` MSRV in Cargo.toml (round-3 F13 confirmed live)
- **Severity:** low
- **Reproducer:** `grep rust-version internal/parse/rust/Cargo.toml` → no match.
- **Observed:** Collaborators with older Rust toolchains may silently get a different syn version or fail to compile in ways unrelated to leonard. There's no signal of what `rustc` version the maintainer tested with.
- **Expected:** Pin `rust-version = "1.74"` (or whatever the current minimum supported is) so `cargo build` fails fast on older toolchains.
- **Suggested fix shape:** Add `rust-version = "1.74"` under `[package]`. syn 2 requires Rust 1.61+; 1.74 is conservative for modern features without being aggressive.

### F11 — `read stdin` and `serialize` errors exit code 1, not 2 (round-3 F11 confirmed unchanged)
- **Severity:** low
- **Reproducer:** Not directly reproducible from outside (stdin-read failure requires kernel-level interference). Code path: lines 322-348 of main.rs.
- **Observed:** Lines 326-328 (`read stdin: {}`, exit 1) and 343-346 (`serialize: {}`, exit 1) write to stderr but use a different exit code from line 335-337 (`line N: ParseError: ...`, exit 2). The Go side (`internal/parse/rust.go` lines 127-136) only treats exit-2 as a clean `ParseFailure`; any other non-zero exit code surfaces as `rust extract failed: %v: %s` with the raw stderr.
- **Expected:** Verbose error surface is acceptable for genuinely-unexpected failures (stdin closed, OOM during serialize). Working as intended.
- **Suggested fix shape:** No change. Documented as informational.

### F12 — Macro-generated items invisible to the index (round-3 F14 reconfirmed)
- **Severity:** informational (out of v0 scope)
- **Reproducer:**
  ```rust
  #[macro_export]
  macro_rules! my_macro { ($x:expr) => { $x + 1 }; }
  pub fn after_macro() -> i32 { 0 }
  ```
- **Observed:** `my_macro` (a declarative macro) is silently dropped. Function calls that *expand* macros to produce items (`define_struct!(Foo)`) similarly produce nothing.
- **Expected:** Indexing macros and macro expansions is out of scope for v0. Acknowledged.
- **Suggested fix shape:** Add `visit_item_macro` to emit `macro_rules!` definitions as `kind=macro`. Bigger work — macro *expansions* would need full proc-macro evaluation, which we won't do.

### F13 — `#[path = "..."]` and `include!()` silently ignored (round-3 F15 reconfirmed)
- **Severity:** informational
- **Reproducer:**
  ```rust
  #[path = "other_module.rs"] mod other;
  include!("generated.rs");
  pub fn local() -> i32 { 0 }
  ```
- **Observed:** Only `local` is extracted. `other` (an external file referenced via `#[path]`) and the contents of `generated.rs` (via `include!()`) are never followed.
- **Expected:** This is correct behavior per the v0 shallow contract — the walker doesn't recurse into modules. Following `#[path]` would require filesystem awareness in the helper, which it intentionally doesn't have. Acknowledged.

### F14 — `unsafe`, `const`, `extern` keywords lost from rendered signature
- **Severity:** low (cosmetic)
- **Reproducer:**
  ```rust
  pub async fn async_fn() -> i32 { 0 }      // → "async fn async_fn()"  ✓
  pub unsafe fn unsafe_fn() {}              // → "fn unsafe_fn()"       ✗ (lost unsafe)
  pub const fn const_fn() -> i32 { 0 }      // → "fn const_fn()"        ✗ (lost const)
  pub extern "C" fn extern_fn() {}          // → "fn extern_fn()"       ✗ (lost extern "C")
  pub async unsafe fn both() -> i32 { 0 }   // → "async fn both()"      ✗ (lost unsafe)
  ```
- **Observed:** `render_fn_sig` (lines 305-320) only checks `sig.asyncness`. `sig.unsafety`, `sig.constness`, and `sig.abi` are ignored.
- **Expected:** For `verify`-level display the missing keywords aren't load-bearing, but `unsafe fn` is a real safety contract — losing it in the signature display obscures that the function is unsafe to call.
- **Suggested fix shape:** Extend `render_fn_sig` to prepend `"unsafe "`, `"const "`, and `"extern \"ABI\" "` qualifiers in canonical Rust order: `[const] [async] [unsafe] [extern "abi"] fn`. ~6 extra lines.

## Things that worked

Verified explicitly during this round:

1. **v0.12 F1 fix holds.** All non-`Type::Path` self_ty cases extract methods correctly: `&Container`, `&mut Foo` (qname collapses to `&Foo` but the method emits), `&[u8]` (`&_slice`), `&&Foo`, `(i32, i32)` (`_tuple`), `[u8; 4]` (`_array`), `fn(i32) -> i32` (`_anon_impl`). None silently dropped.
2. **v0.12 F2 fix holds.** Every visit_* site (`fn`, `struct`, `enum`, `trait`, `type`, `const`, `static`, plus `impl` methods) correctly anchors `start_line` on the identifier line, skipping leading `///` doc and `#[attr]` lines. Verified on a fixture with multi-line `#[deprecated(since=..., note="long\nattribute\nstring")]` and 5-line doc blocks — start_line lands on `pub fn`/`pub struct`/etc.
3. **v0.12 F3 fix holds.** Multi-segment paths preserve their full join: `std::collections::HashMap`, `crate::store::HashMap`, `super::Container`, `self::HashMap` all emit distinct qnames. Local single-segment `HashMap` still produces `m.HashMap.fmt` and does not collide.
4. **Visibility classification correct.** `pub` → exported=true; `pub(crate)`, `pub(super)`, `pub(in path)`, and private all → exported=false. Matches Go's package-scope convention.
5. **Async fn signature renders.** `render_fn_sig` correctly prepends `"async "` for `async fn`. (Other keywords are not — see F14.)
6. **Dogfood — ripgrep.** `/tmp/rust-dogfood` re-indexed cleanly: 100 files, 2,712 symbols, 9 parse-failure suspects (same as round 3). Spot-checked the 9: every one is a legit `pub use` aggregator (`crates/grep/src/lib.rs`, `crates/searcher/src/lib.rs`), a `macro_rules!`-only file (`tests/macros.rs`, `crates/searcher/src/macros.rs`, `crates/printer/src/macros.rs`), or an integration-test file using `rgtest!()` macro invocations (`tests/feature.rs`, `tests/multiline.rs`, `tests/regression.rs`, `tests/tests.rs`). No false positives from the F3 multi-segment-path change.
7. **Dogfood — serde (new project).** `/tmp/serde-dogfood` cloned shallow, indexed cleanly: 208 files, 1,690 symbols, 9 parse-failure suspects (all `pub use` lib.rs roots or `macro_rules!` files). Spot-checked 5 well-known symbols: `Serialize` (trait, serde_core/src/ser/mod.rs:234), `Deserialize` (line 554), `Serializer` (line 355), `Deserializer` (line 945), `SerializeStruct` (line 1907) — all found exactly once, all `start_line` values pointed at the `pub trait X` line (skipping the multi-line `#[diagnostic::on_unimplemented(...)]` attribute that precedes them). F2 fix verified on real-world code.
8. **`leonard-extract-rust` binary size.** 1,745,856 bytes (1.745 MB) for the macOS arm64 release build. Up only slightly from round 3 (round-3 noted "<2 MB"). The added `impl_target_name` function plus the `extra-traits` feature on syn cost very little because syn was already pulling in most of what's needed.
9. **`item_lines` for impl methods with attributes.** Verified end-to-end: a method with `#[test]` + `#[allow(dead_code)]` + 2 doc lines correctly produces `start_line` = the `pub fn` line, not the doc line.

## Open questions

1. **Should `impl_target_name` preserve generic args?** The current behavior collapses `Box<Foo>` and `Box<dyn Bar>` to `Box`. If yes, the cleanest fix is `quote!(#ty).to_string()`; cost is bringing in the `quote` crate (~30 KB binary). If no, document the bucket-by-base-type semantics so the F1/F2 collisions aren't surprising.
2. **Should `pub const` / `pub type` inside impl blocks be indexed?** Mechanically trivial (F6 fix is ~20 LOC). Design question: does the v0 shallow contract include associated members beyond methods?
3. **What size cap (if any) should the Go side enforce on Rust source files?** F8 / round-3 F10 has been latent since round 3. Without a cap, a 10 MB build.rs-generated file in a user's `src/` would OOM on a 1 GB container. Suggested 2 MB based on observed ~144x memory scaling.
4. **Should `Cargo.lock` be tracked?** Decision needed — the convention for binary crates is to commit it, but the project hasn't yet.
5. **Cross-platform helper binary distribution.** The source-tree fallback in `rustExtractorPath` does append `.exe` on Windows. But a Linux-built leonard with a macOS-built `leonard-extract-rust` would fail at `cmd.Run()` with `exec format error`. The current contract is "user builds the helper locally via cargo"; if leonard ever ships a pre-built helper, it'll need per-platform tarballs. Not actionable yet.
6. **Cfg-gated dupe handling.** F3 above is a real correctness gap on platform-conditional codebases (tokio, nix, libc). Worth a brief design call before fix-round: dedup, schema column, or accept as-is?

## Out of scope for this investigation

- Performance benchmarks vs DESIGN.md §7 Q4 (hook latency budget).
- Whether `find_symbol` / `verify_symbol` UX should disambiguate ambiguous qname hits (multiple rows with same qualified_name).
- The wider question of how `doctor`'s "parse-failure suspects" label conflates "0 symbols extracted" with "syn::parse_file errored" — affects this lane but is a `doctor` concern, not a Rust extractor concern.
