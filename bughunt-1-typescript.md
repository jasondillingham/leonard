# Bug Hunt #1 — typescript

## Summary

I audited the hand-rolled TypeScript scanner in `internal/parse/typescript.go`
against a corpus of ~50 "scary TypeScript" fixtures: template literals (nested
and multi-line), JSX with embedded TS, complex generics, decorators, default
exports, private class fields, async/await, type predicates, conditional &
mapped types, regex literals, unterminated strings, mixin classes, and more.
The corpus and harness script are under `_scratch/main.go` (not committed —
`_`-prefixed dirs are skipped by Go tooling) and the fixtures are reproduced
inline below.

Headline: the literal/comment stripper and the generic/JSX disambiguation are
solid — the truly tricky stuff in the docstring is handled. The bugs that
matter cluster around (a) **regex literals**, which the docstring explicitly
declines to strip and which the scanner doesn't treat as opaque tokens, and
(b) **constructs that contain `}` at depth 0 inside what should be balanced
bodies** — `static {}` blocks, mixin `extends Foo(Bar)`, unterminated
strings, and decorators on class members. The combined effect of (a) and (b)
is that **the parser will both fabricate symbols** (false positives, e.g. a
nested `function fakeNested() {}` getting hoisted to top-level when its
enclosing function body contains `/\}/`) **and silently drop real ones**
(e.g. every method after `@log foo()` in a decorated class).

Per the brief, the highest-severity item is in the "fabricated symbol
claims" category that the phase-1 brief explicitly tries to prevent.

---

## Findings

### H1 — Regex literal with `}` causes false-positive symbols and truncated function bodies

- **Severity:** high
- **Reproducer:**
  ```ts
  export function outer() {
      const re = /\}/;
      function fakeNested() { return 99; }
      return re;
  }
  ```
  Run through `ExtractTypeScript("regex.ts", src)`.
- **Observed:**
  ```
  symbols: 2
    {"name":"outer","kind":"function","start":1,"end":2}
    {"name":"fakeNested","kind":"function","start":3,"end":3}
  ```
  - `outer.EndLine` is **2** (the line of the regex `}`), not 5.
  - `fakeNested` is **extracted as a top-level function** — a fabricated symbol. It is not declared at module scope.

  Even worse inside a class:
  ```ts
  export class C {
      m() {
          const re = /\}/;
          return 1;
      }
      n() { return 2; }
  }
  ```
  Output: only `m` (with wrong end=3) and `C` (end=5). **`n` is silently lost.**

  And the simpler module-level case fabricates a function from inside a regex:
  ```ts
  const re = /\}function NotReal(){}/;
  export const real = 1;
  ```
  Output includes `{"name":"NotReal","kind":"function",…}` — a fabricated symbol.

- **Expected:** Regex literals should not contribute tokens to the parser. Both the documented behavior ("regex is intentionally not stripped") and the observed downstream effects need to be reconciled. The phase-1 brief calls out "fabricated symbol claims" as a safety hazard.
- **Suggested fix shape:** Either (a) recognize and blank regex literals during the strip pass — disambiguating from division by tracking what immediately precedes `/` (after a punct, keyword, or start-of-line, `/` starts a regex; after an ident/num/`)`/`]`, it's division); or (b) more conservative — blank the next `}` after every `/.../`-shaped token sequence in the tokenizer. The docstring's "false-positive risk for symbol extraction is acceptable for v0" was an underestimate; even one fabricated symbol breaks Leonard's safety story.
- **Out of scope for this investigation:** how often regex literals containing `}` appear in real codebases (anecdotally: any code that does `s.replace(/\}/, '')` or similar HTML/JSON munging).

---

### H2 — Class with `static {}` block: wrong endLine and silent loss of all methods after the block

- **Severity:** high (silent symbol loss in a construct shipping in TS 4.4+)
- **Reproducer:**
  ```ts
  export class C {
      static value = 0;
      static {
          console.log('init');
      }
      m() { return 1; }
  }
  ```
- **Observed:**
  ```
  symbols: 1
    {"name":"C","kind":"type","sig":"class C","start":1,"end":5}
  ```
  - `C.EndLine` is **5** (the closing `}` of the static block), not 7.
  - Method `m` is **not extracted** at all.

  Trace: `parseClassMember` sees `static` (kw modifier), advances; then sees `{` (punct, not ident) — restores `saved`, returns false. `parseClass` then advances 1 (past `static`) and re-enters `parseClassMember` at `{`. Same failure. Advances past `{`. Inside the block, `console` gets parsed as a field name, and the inner `}` is then mistaken for the class body terminator.
- **Expected:** Static initialization blocks should be skipped as a whole construct. The class symbol's `EndLine` should be the real closing `}`, and members after the block should still be extracted.
- **Suggested fix shape:** In `parseClassMember`, after consuming modifiers, if the next token is `{`, treat this as a static-init block and `skipBalanced()` past it (no symbol emitted). Equivalently, detect `static` followed by `{` as a special case before the name lookup.
- **Out of scope:** TC39 stage-3 decorator metadata blocks (`accessor` keyword, etc.) — `static {}` is the only one shipping today.

---

### H3 — Decorators on class members cause every subsequent member to be silently lost

- **Severity:** high (decorators ship in real TS code today; ecosystems like Angular/NestJS/TypeORM rely on them heavily)
- **Reproducer:**
  ```ts
  export class D {
      @log foo() { return 1; }
      @log bar() { return 2; }
      normal() { return 3; }
  }
  ```
- **Observed:**
  ```
  symbols: 1
    {"name":"D","kind":"type","sig":"class D","start":1,"end":5}
  ```
  None of `foo`, `bar`, or `normal` are extracted.

  Trace: `parseClassMember` sees `@` (punct, not a kw modifier) — modifier loop exits. `nameTok = @`, kind != ident → restore `saved`, return false. `parseClass` advances 1 past `@`. Next call: `nameTok = log` (ident), advances. Then `peek == foo` (not `(`, `?`, or `<`), so it's treated as a **field decl** named `log` and `skipUntilSemiOrEnd()` runs. That consumes `foo() { return 1; } @log bar() { return 2; } normal() { return 3; }` all the way to the class's terminating `}` (since no `;` at depth 0 appears inside any of those method bodies). Class body loop then sees the final `}` and breaks.

  Same outcome with parenthesized decorators (`@log() foo()…`) — see F-decorators below.

- **Expected:** Per the brief, decorators are "out of v0 scope but the parser should skip them gracefully." Skipping = consume the decorator and its argument list (if any), then continue normal member parsing. Methods after a decorator should still be extracted.
- **Suggested fix shape:** In `parseClassMember`, when the current token is `@`, consume it; if the next ident is followed by `(`, also `skipBalanced("(", ")")`. Then re-enter the modifier loop. Apply the same fix at top level for decorated classes — `@Component({...})` already lands the class because `parseFile` is permissive, but `StartLine` will reflect the line of `class`, not `@` (see L4).
- **Out of scope:** TC39 stage-3 decorator factories on parameter positions (`m(@inject foo: Foo)`).

---

### F1 — Parenthesized decorators on class methods produce a phantom method symbol

- **Severity:** medium (related to H3 but in the parens-present case, the parser also fabricates a method named after the decorator)
- **Reproducer:**
  ```ts
  @sealed
  @logger({ level: 'info' })
  export class Decorated {
      @readonly
      name = 'x';
      @log() greet(): string { return this.name; }
  }
  ```
- **Observed:** 4 symbols — including a `Decorated.log` method, kind=method, that does not exist in the source. (`greet` is also extracted, plus the class.)
- **Expected:** No symbol for the decorator. Only `greet` and the class.
- **Suggested fix shape:** Same as H3 — consume `@<ident>` and any trailing `(...)` before name lookup.
- **Out of scope:** Decorators on parameters (`m(@inject foo)`) — not probed here.

---

### M1 — `export default function|class …` emits the symbol with `Exported = false`

- **Severity:** medium
- **Reproducer:**
  ```ts
  export default function defaultFn() { return 1; }
  export default class DefaultCls { method() { return 1; } }
  export default async function defFn(): Promise<number> { return 1; }
  ```
- **Observed:** Each named declaration is extracted, but with `Exported=false`. Methods inside a default-exported class likewise get `Exported=false`. For example:
  ```
  {"name":"defaultFn","kind":"function","exported":false,...}
  {"name":"DefaultCls","kind":"type","exported":false,...}
  {"name":"DefaultCls.method","kind":"method","exported":false,...}
  ```
- **Expected:** The brief calls default exports "out of v0 scope" but specifies the parser should "not crash or mis-extract." Today's behavior is mis-extraction in the worst sense: the symbol is emitted with a wrong `Exported` flag. Two reasonable contracts: (a) skip the declaration entirely (matches `enum`/`namespace` treatment), or (b) emit with `Exported=true` (callers see "reachable from outside the module," which is what `Exported` represents elsewhere).
- **Suggested fix shape:** In `tryTopLevel`'s `case "default"` branch, instead of `p.pos = saved; return false`, call `p.skipDecl()` and return true. Anonymous `export default function () {}` is already silently dropped; this aligns named ones with that behavior. (Alternative: forward the export bit so `Exported=true`.)
- **Out of scope:** `export { foo as default }` (named re-export-as-default) — not probed.

---

### M2 — Mixin-style `extends Mixin(BaseClass)` causes the class to be silently dropped

- **Severity:** medium (common pattern in React HOC-style classes and some Angular code)
- **Reproducer:**
  ```ts
  export class C extends Mixin(BaseClass) {
      m() { return 1; }
  }
  export const after = 1;
  ```
- **Observed:**
  ```
  symbols: 1
    {"name":"after","kind":"const",...}
  ```
  `C` and `C.m` are **not extracted at all**.

  Trace: `parseClass` skips tokens up to the body `{`, but bails out hard if it sees `(`:
  ```go
  if v == ";" || v == "}" || v == "(" {
      p.pos = saved
      return false
  }
  ```
  `Mixin(BaseClass)` contains `(`, so the whole class parse aborts. The fallback then walks forward one token at a time without re-attempting class detection.
- **Expected:** The class should still extract. The `(` was meant to detect malformed `class C(` — but a `(` only matters before `{`, and even then only if it can't be inside a parenthesized expression. The narrower rule is: bail only if `(` appears *before* any `extends`/`implements` keyword.
- **Suggested fix shape:** In the `extends`/`implements` skip loop, track paren depth and only bail on stop conditions at depth 0. Or simpler: scan forward for `{` while ignoring `(...)` as a balanced unit.
- **Out of scope:** `class C extends (cond ? A : B)` — even more exotic but theoretically allowed.

---

### M3 — Unterminated string literal corrupts the rest of the file

- **Severity:** medium
- **Reproducer:**
  ```ts
  const broken = "unterminated
  export function realA() { return 1; }
  export const realB = 2;
  ```
- **Observed:**
  ```
  symbols: 1
    {"name":"broken","kind":"const","start":1,"end":3}
  ```
  `realA` and `realB` are **not extracted**. The stripper bails the string at the newline (good — preserves line counters), but the *tokens* after that point get pulled into `broken`'s initializer because `skipInitializer` only terminates on `;` or `,` at depth 0, and none appear at depth 0 in the rest of the file (the `;` inside `{ return 1; }` is at depth 1).
- **Expected:** A single syntax error should localize. Either: (a) report the unterminated-string error from `stripTSLiterals` (currently the function returns `(_, err)` but only ever returns nil); or (b) terminate `skipInitializer` on a top-level `}` or end of stream and continue with subsequent decls.
- **Suggested fix shape:** In `skipInitializer`, also stop on a top-level `}` (mirroring `skipUntilSemiOrEnd`). Hooks fire on incomplete editor state often; a stray unterminated string shouldn't void the whole file's index.
- **Out of scope:** Unterminated template literals and unterminated block comments — both would have similar properties; not probed individually.

---

### M4 — Object-type return annotation causes wrong `EndLine` and may leak nested symbols

- **Severity:** medium (documented trade-off, but the leakage isn't acknowledged)
- **Reproducer:**
  ```ts
  export function f(): {
      a: number;
      b: string;
  } {
      return { a: 1, b: '' };
  }
  export const after = 1;
  ```
- **Observed:**
  ```
  symbols: 2
    {"name":"f","kind":"function","start":1,"end":4}   // wrong: end should be 7
    {"name":"after","kind":"const",...}
  ```
  `f.EndLine = 4` instead of 7. Worse, the *real* body (`{ return { a: 1, b: '' }; }`) is now floating around at top level after `parseFunction` returns. In this case nothing harmful happens, but a body that contained any `function`/`class`/`const` decl at depth 0 inside that orphaned brace block would surface as a top-level symbol — same fabricated-symbol failure mode as H1.
- **Expected:** Either accurate `EndLine` (track `{}` depth properly in `skipReturnType`) or — per the docstring's "documented trade-off for v0" — at minimum, the leftover body shouldn't be re-parsed for top-level decls.
- **Suggested fix shape:** In `skipReturnType`, track `{}` depth like `()` and `[]` are tracked. The doc-comment claim that "this is uncommon in real code" is dubious — typed functional code returning anonymous record types is common in modern TS.
- **Out of scope:** Type-only return signatures (no body) and function-type aliases — those are parsed via `parseTypeAlias` which already tracks `{}`.

---

### L1 — Private class methods (`#name()`) lose the `#` prefix

- **Severity:** low
- **Reproducer:**
  ```ts
  export class C {
      #secret() { return 1; }
      method() { return 2; }
  }
  ```
- **Observed:**
  ```
  {"name":"secret","qname":"C.secret","kind":"method",...}
  {"name":"method","qname":"C.method","kind":"method",...}
  ```
  The private method `#secret` is recorded as plain `secret` — name and qname both lose the `#`. (`#` tokenizes as a single-char punct that's skipped by the class-body fallback.)
- **Expected:** Either `#secret` / `C.#secret`, or — if private class members are out of scope — drop them entirely (similar to fields). Today they're indistinguishable from public methods of the same name.
- **Suggested fix shape:** In `parseClassMember`, after the modifier loop, if the next token is `#` followed by an ident, fuse them into a single `name` token; or accept "#" as a name-start prefix.
- **Out of scope:** Private static methods (`static #foo()`); private fields (`#x = 1;`) already correctly ignored.

---

### L2 — Getter and setter with the same name produce duplicate `QualifiedName`

- **Severity:** low (depends on the store's uniqueness contract)
- **Reproducer:**
  ```ts
  export class Adv {
      get name(): string { return ''; }
      set name(v: string) { /* … */ }
  }
  ```
- **Observed:**
  ```
  {"qname":"Adv.name","kind":"method","sig":"name()",...}
  {"qname":"Adv.name","kind":"method","sig":"name(v)",...}
  ```
  Same `QualifiedName`. If the store has any uniqueness/upsert behavior keyed on `(file, qname)` (the schema in `internal/store/store.go` isn't shown here but worth checking), one of these silently replaces the other.
- **Expected:** Distinct identification — either suffix the qname (e.g., `Adv.name(get)` / `Adv.name(set)`) or pick one and skip the other.
- **Suggested fix shape:** When the `get`/`set` modifier was seen, append a marker to qname or the symbol kind.
- **Out of scope:** Whether downstream consumers actually use qname uniqueness — verifying that depends on the `store` lane.

---

### L3 — Function overloads emit N symbols with the same `QualifiedName`

- **Severity:** low (same store-dependent concern as L2)
- **Reproducer:**
  ```ts
  export function over(x: number): number;
  export function over(x: string): string;
  export function over(x: any): any { return x; }
  ```
- **Observed:** Three function symbols, all with `name="over"`, `qname="over"`. Same start/end-line situation as L2.
- **Expected:** Standard TS overload conventions: the *implementation* signature is the callable; the declaration-only signatures are forward declarations. The parser could collapse to a single symbol (the implementation) or emit all three with disambiguated qnames.
- **Suggested fix shape:** When emitting a function with no body (declaration-only, terminated by `;`), either skip it or mark it as a forward declaration. Today nothing distinguishes them downstream.
- **Out of scope:** Method overloads inside classes (similar shape, not probed exhaustively).

---

### L4 — Decorator-prefixed top-level class `StartLine` reflects the `class` line, not the `@` line

- **Severity:** low
- **Reproducer:**
  ```ts
  @Component({
      selector: 'x',
      template: 'y',
  })
  export class App {
      foo() {}
  }
  ```
- **Observed:** `App` is correctly extracted, but `StartLine=5` (the `export class` line). The class's source range from the developer's POV begins at line 1 (`@Component`).
- **Expected:** If `StartLine` is the "where this symbol's declaration starts in the file" line, the decorator should be included.
- **Suggested fix shape:** In `tryTopLevel`, detect a leading `@<ident>(...)?` block and capture its start line as the `startLine` for the following declaration.
- **Out of scope:** Whether anything downstream actually cares about the few-line difference.

---

### I1 — Interface body members are not emitted as separate symbols

- **Severity:** informational
- **Reproducer:**
  ```ts
  export interface Greeter {
      y(): void;
      z: number;
      w<T>(arg: T): T;
  }
  ```
- **Observed:** Only the interface itself is emitted; `Greeter.y`, `Greeter.z`, `Greeter.w` are not.
- **Expected:** Matches existing test contract (`TestExtractTypeScript_Symbols` expects interface methods to be absent). Worth flagging because the brief specifically mentioned "Interface body methods" in the corpus.
- **Suggested fix shape:** None for v0; document explicitly in the parser docstring (currently the docstring lists "JSX element extraction" and a few others as out-of-scope but says nothing about interface body methods).
- **Out of scope:** Whether self-host completeness (the selfhost lane) treats this as a false negative.

---

### I2 — `export default function () {}` (anonymous) is silently skipped — no symbol

- **Severity:** informational
- **Reproducer:**
  ```ts
  export default function () { return 1; }
  export const after = 1;
  ```
- **Observed:** Only `after` is emitted. The anonymous function isn't extracted because the parser requires an ident after `function`.
- **Expected:** Consistent with v0's "default is out of scope" — but inconsistent with M1, where named-default *is* extracted (wrongly).
- **Suggested fix shape:** Fix M1 by always skipping default exports; that aligns the two cases.

---

### I3 — Tagged template literals and generic tags are handled correctly

- **Severity:** informational (positive)
- **Reproducer:**
  ```ts
  const tagged = tag`function FakeFromTag(){}`;
  const taggedGen = tag<T>`stuff ${x}`;
  ```
- **Observed:** No fake symbols extracted; both vars correctly recorded. (The strip pass blanks the backtick body regardless of what comes before the backtick.)

---

### I4 — Interaction matrix worth knowing

- Regex `)`, `]` in initializers: parser terminates correctly because `skipInitializer` only triggers on `}` at depth 0; `re = /a)b/` stays inside the var decl (the `)` decrements depth, but depth is 0, so the function returns — wait, that's the bug too. Let me double-check: tokens for `const re = /a)b/;` are `const`, `re`, `=`, `/`, `a`, `)`, `b`, `/`, `;`. `skipInitializer` sees `)` at depth=0: `case ")", "}", "]": if depth == 0 { return }`. **Yes, regex `)` also terminates initializer prematurely.** The output happens to still extract `re` and `real` because there's nothing else to lose on that line — but if the regex were on the same line as multiple decls or followed by a statement that looks like a top-level decl, the same H1-style false positive would appear. Flagging this as part of H1's scope; same root cause.
- `const re = /a)b/; const NotReal = 5; …` — would `NotReal` be extracted? Probably yes, as a real const, so this isn't a *fabrication* in that case. But the const `re` would have `EndLine` of the line with `)`, not the line with `/`, and any later constructs on the same line would not be associated with the var decl correctly.

---

## Things that worked

These I specifically verified and they were correct:

1. **Nested template literals** including `` `${ `${ `${x}` }` }` `` — strip pass handles arbitrary nesting (see F-template-nested, F-template-deep-nested).
2. **Multi-line template literals** with `${...}` interpolation that spans newlines — line counting stays aligned (F-template-with-newlines).
3. **Strings containing fake declarations** — `"function StringHidden() {}"`, `'class Hidden {}'` — properly stripped.
4. **Comments in type annotations** — `function f(x: /* inline */ number): /* return */ string` works.
5. **JSX with embedded TS expressions** — `(x as number) + 1`, array maps with arrow `<span>{n}</span>` — top-level component correctly recorded.
6. **Complex generic constraints** — `function foo<T extends Record<K, V>, K extends string, V = T[K]>(x: T): T` works.
7. **Arrow generics in `.ts`** — `const id = <T>(x: T) => x` extracts the const; same in `.tsx` with the `<T,>` trailing-comma form.
8. **Function-type return signatures** like `(x: number) => string` as a generic constraint don't confuse `skipGenericParams` (the `=>` token is atomic).
9. **Class generic + extends + implements** — `class C<T> extends Base<A, B> implements I, J { … }` works as long as `extends` doesn't have parens (see M2).
10. **Type predicates** — `function isFoo(x: unknown): x is Foo` works.
11. **Conditional types and `infer`** — `type Unwrap<T> = T extends Promise<infer U> ? U : T` works.
12. **Mapped types** — `type Partial2<T> = { [K in keyof T]?: T[K] }` works.
13. **Abstract classes and abstract methods** — `abstract class A { abstract foo(): string; bar() {} }` — both methods extracted.
14. **`declare` declarations** — `declare function foo(x: number): string;` extracted (with `Exported=false`, which seems right).
15. **Async/await** — `async function`, `async arrow`, `await` in body all work.
16. **Trailing commas** in multi-line params, multi-line object literals, generic param lists with `<T,>` form.
17. **Rest params and destructured params** — `m(x = 1, { y, z = 2 }: T, ...rest: number[])` produces `m(x, {...}, ...rest)` signature.
18. **Class fields with arrow initializers including JSX** — `render = (): JSX.Element => <div>x</div>;` correctly skipped as a field; subsequent methods preserved.
19. **JSX text containing `>` and `<` characters** — `<div>a > b && c < d</div>` (where `<`/`>` aren't tracked as delimiters in `skipInitializer`) works.
20. **Computed property method names** — `[Symbol.iterator]()` silently skipped, not mis-extracted.
21. **Empty class body and other malformed snippets** — no panics on the existing `TestExtractTypeScript_EmptyAndMalformedSafe` corpus.
22. **Regex with `)` or `]`** — those don't terminate `skipInitializer` (only `}` does at depth 0); confirmed safe for the var-decl case. But see I4 — `)` does terminate the initializer when depth is already 0.

---

## Open questions

1. **What `Exported` is supposed to mean for default exports.** The brief calls default exports out of v0, but it doesn't say whether they should produce no symbol or a symbol with `Exported=true`. Today they produce a symbol with `Exported=false`, which is the worst option. Jason's call — but please pick.
2. **Are interface body members in scope for v1?** The selfhost lane should care: a Leonard-indexed `.d.ts` will look very sparse if interface fields/methods aren't extracted, which may surface as false negatives there.
3. **Does the store enforce `(file, qname)` uniqueness?** If yes, L2 (getter/setter) and L3 (overloads) actively drop rows; if no, they just produce duplicate hits. Worth confirming against `internal/store/store.go`.
4. **Should `.ts` vs `.tsx` change parser behavior?** Real TS treats `<T>` as a generic in `.ts` and ambiguously (often as JSX) in `.tsx`. Today the parser doesn't look at the file extension; the same string parses the same in either context. For top-level decls this is fine because every test passed; for arbitrary expressions inside bodies it could matter for future work that recurses into bodies.
5. **Regex stripping cost-benefit.** The docstring explicitly punts on regex because disambiguating regex from division is hard. Findings H1 and I4 say the cost is higher than estimated. A conservative heuristic (treat `/.../` after `=`, `,`, `(`, `[`, `{`, `return`, `:`, `;`, or a kw as regex) covers ~all real cases. Worth a small benchmark before/after to see if it's tractable.
6. **`extends Mixin(Base)` — is this in scope for v0 or v1?** The brief lists "Generic type parameters with complex constraints" as a corpus item, and mixins are a load-bearing feature in many TS codebases (Angular, NestJS). M2 is fixable in a few lines.

---

## Reproducer harness (not committed)

I drove these fixtures through `ExtractTypeScript` via a scratch program at
`_scratch/main.go` (Go-tooling-invisible because the dir name starts with
`_`). It imports `github.com/jasondillingham/leonard/internal/parse` and
calls `parse.ExtractTypeScript(path, src)` on each fixture, printing
symbols as JSON.

To reproduce a finding: paste the relevant fixture's source into a slice in
that file (or whatever fix-round scratch you set up), run `go run ./_scratch/`,
and compare against the bug's "Observed" section. The fix round should delete
the `_scratch/` dir entirely.
