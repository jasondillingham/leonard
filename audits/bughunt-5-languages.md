# Bug Hunt #5 — languages

## Summary

I sanity-checked every tree-sitter language added since v0.20 (24 grammars: Ruby, C#, Swift, Kotlin, Scala, Dart, C, C++, PHP, Lua, Bash, Zig, Nix, Elixir, Solidity, Make, CMake, HCL, GraphQL, Proto, SQL, WIT, Erlang, R, Just, Starlark, GLSL, HLSL) plus Astro and Solid (.jsx). For each I drove a focused fixture through `leonard-extract-treesitter` and inspected the JSON output; for Ruby, C++, Elixir, and Lua I additionally indexed real OSS corpora (sinatra, nlohmann/json, phoenix_live_reload, plenary.nvim) and spot-checked named symbols.

Headline results:

- **Cross-cutting issues** (affecting *all* tree-sitter languages):
  - The module-name prefix is just the file basename (no directory path), so two files with the same basename in different directories produce identical qnames. Python and TypeScript don't have this problem because their extractors use full path. **`bughunt-5 LANG-F1`** below.
  - Field-bound `(name)` captures without a `name:` field binding (GraphQL, Proto) break parent-folding silently — `find_parent_name` returns None because it queries `child_by_field_name("name")` on the parent. **F2**.
  - The `is_exported` heuristic doesn't know about each language's visibility default. Ruby methods (public by default), Swift internal-by-default, Zig `pub`, C `extern`/`static`, Solidity `external/public` are all reported `exported:false` for methods. **F3**.
- **Per-language correctness issues**: C++ misses in-class inline methods entirely (a huge surface in real header-only libraries — `nlohmann::basic_json::dump()` is invisible); Lua silently drops the `M.` / `M:` prefix from method names; HCL still emits two-symbols-per-resource; GraphQL/Proto field & RPC qnames collide; SQL columns collide across tables; Astro frontmatter regex closes early on an embedded `---`.
- **Things that worked well**: Ruby/C#/Swift/Kotlin/Java/PHP all do parent-fold class.method correctly. Java nested types work. CMake multi-line function decls work. Just, R, Bash, Nix, Solidity, WIT all behaved as documented. `defaultSkipDirs` correctly excludes new languages' extensions under `node_modules/target/__pycache__`. Tree-sitter does not capture comment content as identifiers.

All fixtures and reproducer commands below assume `/tmp/bughunt5/run.sh` is the wrapper script in `/tmp/bughunt5/run.sh`:
```bash
#!/bin/bash
BIN=/Users/jasondillingham/Documents/Homelab/leonard/internal/parse/treesitter/target/release/leonard-extract-treesitter
"$BIN" --lang "$1" "$2"
```

## Findings

### LANG-F1 — Tree-sitter module_name uses file basename only — cross-directory qname collisions

- **Severity:** medium
- **Reproducer:**
  ```bash
  mkdir -p /tmp/poly/{a,b} && cat > /tmp/poly/a/foo.lua <<<'function bar() return 1 end' \
    && cat > /tmp/poly/b/foo.lua <<<'function bar() return 2 end' \
    && cd /tmp/poly && leonard init && leonard index && leonard verify bar
  ```
- **Observed:** Both Lua functions report qname `foo.bar` despite living in different directories. This is true for **every tree-sitter language** because `module_name` in `internal/parse/treesitter/src/main.rs:1163` only takes the file's basename (without extension). Python (`internal/parse/python.go`) and TypeScript (`internal/parse/typescript.go`) use the project-relative file path, producing distinct qnames like `py.foo.bar` vs `lua/a/foo.bar`.
- **Expected:** Either use the indexer-relative path (matching Python/TS), or at minimum include the immediate parent directory so common patterns like `cmd/foo/main.go` and `internal/foo/main.go` don't collide on `main`. Sinatra's `lib/sinatra/base.rb` and `rack-protection/lib/rack/protection/base.rb` both produce `base.Base` for their respective `Base` classes — observed in `/tmp/bughunt5-sinatra`.
- **Suggested fix shape:** In `module_name(path)`, drop only the extension and replace path separators with `.` — `lib/sinatra/base.rb` → `lib.sinatra.base`. Python's qname uses dotted paths already; aligning here would make cross-language qnames more uniform.
- **Out of scope:** Should `module_name` strip a leading project prefix? That depends on a policy decision (project-relative vs absolute) that's not language-specific.

### LANG-F2 — GraphQL / Proto: parent-fold silently fails on captures without a `name:` field binding

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/s.graphql <<'EOF'
  type User { id: ID! name: String! email: String }
  type Post { id: ID! title: String! }
  EOF
  /tmp/bughunt5/run.sh graphql s.graphql < /tmp/s.graphql
  ```
- **Observed:** Both `User.id` and `Post.id` get qname `s.id` (collision). Same for proto: `service UserService { rpc GetUser(...); }` produces `sample.GetUser`, not `sample.UserService.GetUser`. find_parent_name (main.rs:1137) walks the parent chain looking for `object_type_definition`/`service` and then calls `p.child_by_field_name("name")` — but the GRAPHQL_QUERY / PROTO_QUERY define their object types without an explicit `name:` field binding (e.g. `(object_type_definition (name) @name)` not `(object_type_definition name: (name) @name)`), so the grammar's child-by-field lookup returns None.
- **Expected:** Field definitions inside `type User { ... }` should produce qname `module.User.id`. RPCs should produce qname `module.UserService.GetUser`.
- **Suggested fix shape:** Two options: (a) update find_parent_name to fall back to walking children for a `(name)` / `(identifier)` node when child_by_field_name returns None, or (b) update the grammar queries to use the explicit `name:` field selector (verify against grammar's node-types.json). Verify the same fix for WIT's `record_item`/`enum_item`/`variant_item`/`flags_item` (currently not folded under interface_item; only func_item is, because tree-sitter-wit's grammar happens to use `name:` for that one).
- **Out of scope:** Real GraphQL schemas often nest with input/union/scalar at the top level — those are already type symbols and the fix doesn't affect them.

### LANG-F3 — `is_exported` is wrong for every language whose visibility default is "public"

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/r.rb <<<'class Foo; def bar; 1; end; end'
  /tmp/bughunt5/run.sh ruby r.rb < /tmp/r.rb  # bar -> exported:false
  ```
- **Observed:** `is_exported` (main.rs:1105) checks for a `modifiers`/`modifier`/`visibility_modifier` child. None of these grammars expose them for the languages where they'd matter most:
  - **Ruby**: methods are public by default. All Ruby methods get `exported:false`.
  - **Swift**: protocol-method requirements are implicitly accessible; all Swift methods get `exported:false`. Same for interface methods.
  - **Zig**: `pub fn add(...)` keyword is a separate token in the grammar (`visibility_modifier` may exist but the heuristic isn't matching it). `pub fn` is reported `exported:false`.
  - **C**: `static` vs external linkage is the convention; not detected. Every C function is `exported:false`.
  - **Solidity**: `external/public/internal/private` visibility — not detected; all methods `exported:false`.
  - **Erlang**: `-export([foo/1])` is module-level; per-function export info not surfaced.
- **Expected:** Each language's `exported` should reflect the language's actual visibility model. At minimum, Ruby and Swift methods should default to true (matching the type/interface fallback at main.rs:1130). The brief explicitly asked about this for Ruby.
- **Suggested fix shape:** Add a per-language exported default (e.g. on the `Language` struct: `default_method_exported: bool`). Ruby/Swift/Lua/Bash/Erlang would default true; Java/C#/Kotlin/Scala/Zig/C/C++ default false. Then `is_exported` returns the per-language default when no modifier child is found. As a bonus, detect Zig `pub` (look for a preceding `pub` keyword in the function node's children).
- **Out of scope:** Compile-time visibility (Ruby's `private` keyword in a class body affects all subsequent methods) — the grammar's tree shape doesn't make this trivially queryable from a single node.

### LANG-F4 — C++ misses in-class inline method definitions (e.g. nlohmann::json::dump)

- **Severity:** high
- **Reproducer:**
  ```bash
  cat > /tmp/c.cpp <<'EOF'
  class Foo {
  public:
      int bar() { return 1; }     // inline body — NOT captured
      int baz();                  // forward decl — captured
  };
  int Foo::baz() { return 2; }    // out-of-class — captured as @function
  EOF
  /tmp/bughunt5/run.sh cpp c.cpp < /tmp/c.cpp
  ```
- **Observed:** Only `Foo` (the class) and `Foo.baz` (the forward declaration) get captured. `bar()` defined inline inside the class body is completely missing. Confirmed against nlohmann/json's `basic_json::dump()` (declared at `include/nlohmann/json.hpp:1319` with an inline body): `leonard verify dump` returns "no match" against the indexed repository (551 files).
- **Expected:** A method with an inline body is still a method on the class and should be captured with qname `module.Foo.bar`. This pattern is the dominant idiom for header-only C++ libraries — STL, nlohmann/json, fmt, boost-style headers. Missing it makes Leonard nearly useless for verifying C++ method existence in modern codebases.
- **Suggested fix shape:** Add a CPP_QUERY pattern for `(function_definition declarator: (function_declarator declarator: (field_identifier) @name)) @method` — note that when a function_definition appears as a direct child of a `field_declaration_list` (inside a class_specifier), the declarator's inner identifier is a `field_identifier` rather than `identifier`. Verify the same in C's grammar for inline struct-member functions if C ever needs that.
- **Out of scope:** Constructors defined inside class body parse as `declaration` rather than `field_declaration` (round-4 finding) — see F5 for that.

### LANG-F5 — C++ class constructor/destructor declarations are still uncaptured (round-4 known, still present in v0.38)

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/c.cpp <<'EOF'
  class Greeter {
  public:
      Greeter(const std::string& name);
      ~Greeter();
      static Greeter* create(const std::string& n);
  };
  EOF
  /tmp/bughunt5/run.sh cpp c.cpp < /tmp/c.cpp
  ```
- **Observed:** Only `Greeter` (the class) is captured. The in-class declarations of constructor `Greeter(...)`, destructor `~Greeter()`, and the static factory `create(...)` are missing. This was flagged in round 4 and still holds.
- **Expected:** All three should produce method-kind symbols qname-folded under `Greeter`.
- **Suggested fix shape:** Constructors in tree-sitter-cpp parse as `declaration` nodes whose declarator is a `function_declarator` whose inner declarator is an `identifier` matching the class name. Add a pattern; verify via tree-sitter-cli playground. Static methods declared (not defined) inside a class body parse similarly. See main.rs:457-489 comments — design note acknowledges this is a v0.22 limitation.

### LANG-F6 — C++ out-of-class method definitions fold to namespace instead of class

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/c.cpp <<'EOF'
  namespace acme {
  class Greeter { public: std::string greet() const; };
  std::string Greeter::greet() const { return "hi"; }
  }
  EOF
  /tmp/bughunt5/run.sh cpp c.cpp < /tmp/c.cpp
  ```
- **Observed:** `Greeter::greet()` defined out-of-class produces qname `c.acme.greet` (folded under the namespace, not under Greeter), with kind=`function`. The qualified_identifier left-hand-side (`Greeter`) is dropped.
- **Expected:** qname should be `c.acme.Greeter.greet`, kind=method.
- **Suggested fix shape:** When the captured function_definition's declarator is a `qualified_identifier`, derive the qname from the qualified_identifier's `scope:` chain (which gives namespace + class), not from ancestor walking. The current ancestor walk picks up `namespace_definition` which is reasonable for free functions but wrong for member functions.

### LANG-F7 — Nested types not folded into outer container's qname (cross-language)

- **Severity:** medium
- **Reproducer:**
  ```bash
  # Java
  cat > /tmp/o.java <<'EOF'
  public class Outer { public static class Inner {} }
  EOF
  /tmp/bughunt5/run.sh java o.java < /tmp/o.java
  # -> Inner gets qname "o.Inner", not "o.Outer.Inner"
  ```
  Reproduces in: Java (inner class), C# (inner class), Kotlin (`sealed class Result { class Ok }`), Ruby (`module M; class C; end`), Elixir (`defmodule A do; defmodule B`), C++ (nested struct in namespace), Scala (companion).
- **Observed:** Methods of nested types are parent-folded correctly (`Outer.Inner.doIt`), but the nested type itself is not (still `Outer.Inner` instead of `Outer.Outer.Inner`). Inconsistent.
- **Expected:** Either consistently fold (`Outer.Outer.Inner`) or document that nested-type qnames intentionally skip outer scope.
- **Suggested fix shape:** Apply the parent-container fold to type-kind captures as well as method-kind, OR explicitly call out in the brief that nested-type names use only the immediate parent.
- **Out of scope:** Ruby's `module Greetings ... class Greeter` is a special case because the `class` node has its own grammar shape (constant name vs identifier).

### LANG-F8 — Ruby `attr_accessor`/`attr_reader`/`attr_writer` invisible

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/r.rb <<'EOF'
  class Foo
    attr_accessor :name
    attr_reader :id
  end
  EOF
  /tmp/bughunt5/run.sh ruby r.rb < /tmp/r.rb
  ```
- **Observed:** Only `Foo` is captured. `attr_accessor :name` defines `name` and `name=` at runtime; `attr_reader :id` defines `id`. None are visible in the index.
- **Expected:** Real Sinatra/Rails code uses these constantly. Ideally treat `attr_accessor :foo` as defining method `foo` (and `foo=` for accessor/writer).
- **Suggested fix shape:** Add a Ruby-specific call-target pattern matching `(call method: (identifier) @_attr (#any-of? @_attr "attr_accessor" "attr_reader" "attr_writer") arguments: (argument_list (simple_symbol) @name))` — synthesize a `method` symbol per symbol arg. Document `define_method` and `class_eval` block-defined methods as known blind spots.
- **Out of scope:** `define_method(:foo) { ... }` is harder — the symbol literal is more nested in the AST. Leave as documented limitation.

### LANG-F9 — Lua silently drops `M.foo` / `M:foo` table prefix in qname

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/l.lua <<'EOF'
  local M = {}
  function M.add(a, b) return a + b end
  function M:greet() return "hi" .. self.name end
  function topLevel() return 1 end
  EOF
  /tmp/bughunt5/run.sh lua l.lua < /tmp/l.lua
  ```
- **Observed:** `M.add` produces qname `l.add` (M prefix lost) — identical to a free function `add`. `M:greet` same. Confirmed in plenary.nvim where multiple files have local classes with `function Class:new(...)` and all collapse to `<file>.new` qnames.
- **Expected:** Qname should reflect the table-as-namespace prefix, e.g. `l.M.add`. The Rust comment at main.rs:160-165 says "the .scm captures the table portion of the name only as a disambiguator" but the table portion is NOT used in the qname — it's discarded.
- **Suggested fix shape:** In the LUA_QUERY, expose the table identifier as e.g. `@scope`, then handle `@scope` in the extractor by folding into qname like `module.scope.name`. Lua has no class concept so this is the only signal available.

### LANG-F10 — HCL still emits two-symbols-per-resource and collides on resource type

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/m.tf <<'EOF'
  resource "aws_instance" "web" { ami = "x" }
  resource "aws_instance" "db" { ami = "y" }
  EOF
  /tmp/bughunt5/run.sh hcl m.tf < /tmp/m.tf
  ```
- **Observed:** Four symbols emitted: `m.aws_instance` (twice — one for each resource), `m.web`, `m.db`. The intent (per main.rs:660-672 comment) is to capture both labels, but the result is qname collisions on the resource type and orphan instance-name symbols with no resource-type context.
- **Expected:** A Terraform resource `resource "aws_instance" "web"` should produce ONE symbol with qname `m.aws_instance.web` (the canonical Terraform reference). Either combine the two labels into a single qname or skip the type-label entirely.
- **Suggested fix shape:** Update HCL_QUERY to capture both labels under a single match, then build qname `module.<type>.<name>` in the extractor for two-label blocks. One-label blocks (`variable "x"`, `output "y"`, `locals`) keep the current `module.<name>` shape.
- **Out of scope:** `terraform {}` and `locals {}` blocks have no labels and currently never match — accept as informational.

### LANG-F11 — SQL columns collide across CREATE TABLE statements (round-4 known)

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/s.sql <<'EOF'
  CREATE TABLE users (id SERIAL, name VARCHAR(255));
  CREATE TABLE posts (id SERIAL, title VARCHAR(500));
  EOF
  /tmp/bughunt5/run.sh sql s.sql < /tmp/s.sql
  ```
- **Observed:** Both `id` columns produce qname `s.id`. The SQL_QUERY's `column_definition` is captured as `@const` but parent_container_kinds includes `create_table` — parent-fold isn't applied to const-kind, only method/function. So columns end up unfolded.
- **Expected:** qname `s.users.id` and `s.posts.id` so a `verify_symbol("users.email")` MCP call finds the right column.
- **Suggested fix shape:** Extend the `matches!(kind, "method" | "function")` check at main.rs:1050 to include `"const"`. Verify it doesn't regress other languages' const captures (Make variables, Just assignments, Starlark name kwargs, shader uniforms — all of which currently produce flat qnames and should remain flat). Better: per-language opt-in via a Language flag `fold_consts_into_parent: bool`.

### LANG-F12 — SQL `CREATE FUNCTION ... RETURNS users` spuriously emits a `users` function symbol

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/s.sql <<'EOF'
  CREATE TABLE users (id SERIAL);
  CREATE FUNCTION get_user(p_id INTEGER) RETURNS users AS $$ SELECT * FROM users $$ LANGUAGE sql;
  EOF
  /tmp/bughunt5/run.sh sql s.sql < /tmp/s.sql
  ```
- **Observed:** Output includes two symbols for `users`: one type (the table) and one function (line 2). The create_function pattern's `(object_reference name: (identifier) @name)` matches both the function name AND the RETURNS type clause because both parse as object_references inside create_function.
- **Expected:** Only `get_user` and the table `users` should appear.
- **Suggested fix shape:** Add an anchor `.` to the create_function pattern, or constrain it to match the function's own name node specifically (likely via a field name like `function_name:` if the grammar exposes one).

### LANG-F13 — Astro frontmatter regex closes early on embedded `---`

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/early.astro <<'EOF'
  ---
  const s = `line1
  ---
  line3`;
  export function afterEarly() { return 1; }
  ---
  <p>{s}</p>
  EOF
  cd /tmp/test && leonard init && leonard index && leonard verify afterEarly
  # -> "no match"
  ```
- **Observed:** The frontmatter regex `(?s)\A---\s*\n(.*?)\n---` (vue_svelte.go:53) is non-greedy and closes at the first `---` it finds. A backtick template literal containing `---` on its own line truncates the frontmatter, dropping `afterEarly` from the captured script.
- **Expected:** Astro frontmatter is closed only by a `---` on its own line at column 0 — backtick contents shouldn't terminate it. Real Astro components rarely have `---` in strings, but markdown-rendering Astro setups can.
- **Suggested fix shape:** Use a stricter terminator that requires the closing `---` to be at line start AND followed by a newline or EOF, AND ideally be the LAST `---` before the template begins. A simple improvement: scan for `\n---\s*$` with the `m` flag and skip occurrences inside string literals — or just document the edge case as a known limitation since it's rare in practice.

### LANG-F14 — Kotlin: top-level `fun` and `interface` mis-classified

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/k.kt <<'EOF'
  fun topLevelFunc(x: Int) = x + 1
  interface Shape { fun area(): Double }
  EOF
  /tmp/bughunt5/run.sh kotlin k.kt < /tmp/k.kt
  ```
- **Observed:**
  - `topLevelFunc` has kind=`method` (the KOTLIN_QUERY captures `function_declaration` as `@method`). Should be `function` since it's not inside a class.
  - `Shape` (interface) has kind=`type`, not `interface`. Documented at main.rs:376-380 — Kotlin's `interface` keyword rides on `class_declaration` so the query can't distinguish.
- **Expected:** Top-level Kotlin functions are kind=function. Interface keyword should map to kind=interface.
- **Suggested fix shape:** Have the extractor inspect a `function_declaration`'s parent — if no parent_container ancestor is found, emit kind=function. For interface vs class, check the class_declaration node's first child token: if it's the `interface` keyword, override kind to `interface`. Same trick for `enum class`.

### LANG-F15 — Dart constructors and abstract class methods uncaptured

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/d.dart <<'EOF'
  class Person {
    final String name;
    Person(this.name);            // constructor — NOT captured
    String greet() => "hi";       // captured
  }
  EOF
  /tmp/bughunt5/run.sh dart d.dart < /tmp/d.dart
  ```
- **Observed:** Only `Person` (type) and `greet` (function, folded under Person) emitted. The constructor `Person(this.name)` is invisible.
- **Expected:** Constructor should appear as a method-kind symbol with qname `module.Person.Person` (Java-style) or synthesized `init` (Swift-style).
- **Suggested fix shape:** Add a pattern for `(constructor_signature ...)` or whatever the tree-sitter-dart grammar uses for constructors.

### LANG-F16 — Swift methods report kind=function (should be method when parent-folded)

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/s.swift <<'EOF'
  class Person {
    func greet() -> String { return "hi" }
  }
  EOF
  /tmp/bughunt5/run.sh swift s.swift < /tmp/s.swift
  ```
- **Observed:** `greet` has kind=`function`, qname `s.Person.greet` (correctly folded). The SWIFT_QUERY uses `function_declaration` → `@function` for both top-level and in-class functions; the parent-fold sets a correct qname but doesn't reclassify the kind.
- **Expected:** When a function_declaration appears inside a class/protocol/struct/enum, kind should be `method`. Tree-sitter-swift uses `class_declaration` for class/struct/enum (per the code comment), so all four cases benefit from this fix.
- **Suggested fix shape:** After `find_parent_name` succeeds for an `@function` capture, override `kind` to `method` (currently the code only updates qname). Same applies to Dart (LANG-F15 above mentions kind=function for what should be method) and Kotlin (LANG-F14).

### LANG-F17 — Lua `function bar() end` at top level is kind=method, not function

- **Severity:** low
- **Reproducer:** Already shown in LANG-F9 fixture. The Ruby `def free_method` outside any class also produces kind=method.
- **Observed:** Languages where the grammar's "function declaration" node is the same shape regardless of class membership all produce kind=method even at module top-level. Affects: Ruby (`def foo` at top level), Kotlin top-level `fun` (also captured in F14), Scala top-level `def`, Elixir top-level `def` (rare but possible).
- **Expected:** Top-level free functions should be kind=function.
- **Suggested fix shape:** Same as LANG-F16 — make kind depend on whether parent-fold succeeded.

### LANG-F18 — C# partial classes and constructor overloads emit duplicate qnames

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/s.cs <<'EOF'
  namespace App {
      public partial class Order { public Order() {} public Order(int id) {} public void Submit() {} }
      public partial class Order { public void Cancel() {} }
  }
  EOF
  /tmp/bughunt5/run.sh csharp s.cs < /tmp/s.cs
  ```
- **Observed:** Two `Order` type symbols both qname `Sample.Order` (no namespace folding). Two constructors both qname `Sample.Order.Order` (overload ambiguity). All from the same file — start_line distinguishes but qname-based lookup returns ambiguous matches.
- **Expected:** Partial classes should arguably collapse to a single type symbol per project (canonical C# semantics) — or at minimum carry distinct spans. Constructor overloads should be distinguishable via signature in qname.
- **Suggested fix shape:** Partial classes: keep both symbols but ensure downstream consumers know they're the same class (perhaps a `partial: true` marker or merge during indexing). Overload disambiguation: include arity or first-arg type in qname for methods — but this changes the cross-language qname contract significantly.
- **Out of scope:** Namespace folding (the broader cross-language F7).

### LANG-F19 — C# namespaces NOT captured as symbols and not folded into nested type qnames

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/s.cs <<'EOF'
  namespace Acme.App {
      public class Person { }
  }
  EOF
  /tmp/bughunt5/run.sh csharp s.cs < /tmp/s.cs
  ```
- **Observed:** `Person` has qname `s.Person` — namespace `Acme.App` is invisible. The CSHARP_QUERY (main.rs:323-348) has no `namespace_declaration` capture at all.
- **Expected:** Either capture `namespace Acme.App` as a symbol, or fold its name into nested types' qnames (`s.Acme.App.Person`).
- **Suggested fix shape:** Add `(namespace_declaration name: (qualified_name) @name) @type` to CSHARP_QUERY. Same applies to PHP namespaces (currently captured but with backslash-preserving name and not folded into class qnames).

### LANG-F20 — C# properties (`{ get; set; }`) invisible

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/s.cs <<'EOF'
  public class User { public int Id { get; set; } public string Name { get; set; } }
  EOF
  /tmp/bughunt5/run.sh csharp s.cs < /tmp/s.cs
  ```
- **Observed:** Only `User` captured. Properties `Id` and `Name` invisible.
- **Expected:** Properties should appear as method or const symbols.
- **Suggested fix shape:** Add `(property_declaration name: (identifier) @name) @method` (or @const) to CSHARP_QUERY.

### LANG-F21 — Elixir `defimpl` not captured as type; def in defimpl collides with defprotocol

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/e.ex <<'EOF'
  defprotocol Sayable do
    def say(thing)
  end
  defimpl Sayable, for: Atom do
    def say(a), do: Atom.to_string(a)
  end
  EOF
  /tmp/bughunt5/run.sh elixir e.ex < /tmp/e.ex
  ```
- **Observed:** `defimpl Sayable, for: Atom do ... end` produces no symbol. Both `say` functions get qname `e.say` — collision.
- **Expected:** defimpl block should produce a type-kind symbol like `Sayable.Atom` or `Sayable_Atom`. The def inside should fold under it.
- **Suggested fix shape:** Add a `defimpl` clause similar to `defmodule` to ELIXIR_QUERY. Resolve the name as the joined `<protocol>.<for-target>` (more complex query because there are two args). Document as known limitation if too complex.

### LANG-F22 — Elixir nested defmodule + def in module not parent-folded (v0.24 known)

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/e.ex <<'EOF'
  defmodule Outer do
    defmodule Inner do
      def shout(s), do: String.upcase(s)
    end
    def greet(n), do: "hi"
  end
  EOF
  /tmp/bughunt5/run.sh elixir e.ex < /tmp/e.ex
  ```
- **Observed:** `Inner` qname `e.Inner` (not `e.Outer.Inner`). `shout` qname `e.shout` (not `e.Outer.Inner.shout`). `greet` qname `e.greet`. The parent_container_kinds is empty for Elixir (main.rs:185-188 documents this) so nothing folds.
- **Expected:** Module hierarchy should be reflected. Elixir's whole namespace model is module-dotted names.
- **Suggested fix shape:** Hard — the grammar wraps everything in `call` nodes, so the ancestor walk doesn't have a clean "I'm inside defmodule X" signal. May require dedicated AST analysis. Document as accepted limitation.

### LANG-F23 — Lua `local function helper` indistinguishable from global function

- **Severity:** low
- **Reproducer:** See LANG-F9 fixture.
- **Observed:** `local function helper(x)` and `function topLevel(x)` both produce the same symbol shape with `exported:false`. Lua callers can't tell which functions are file-internal vs module-exported.
- **Expected:** local functions should be `exported:false`; non-local should be `exported:true` (or whatever per-language Lua convention dictates).
- **Suggested fix shape:** Detect the `local` keyword preceding `function_declaration` in the parent's children; toggle exported accordingly. Or use a `@local` capture name to flag it.

### LANG-F24 — Zig method on struct value not parent-folded; `pub` not detected

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/z.zig <<'EOF'
  const Point = struct {
      x: i32,
      pub fn dist(self: Point) f64 { return 0.0; }
  };
  EOF
  /tmp/bughunt5/run.sh zig z.zig < /tmp/z.zig
  ```
- **Observed:** `dist` qname `z.dist` (Point not folded). `pub fn` exported:false.
- **Expected:** `dist` qname `z.Point.dist`. `pub fn dist` exported:true.
- **Suggested fix shape:** Add `struct_declaration`/`enum_declaration`/`union_declaration` to Zig's parent_container_kinds, but Zig binds these to a const so the parent walk needs to find the enclosing `variable_declaration` — non-trivial. Document. For `pub`, detect the keyword in node siblings.

### LANG-F25 — Java module-name (cosmetic): file basename vs package path

- **Severity:** informational
- **Reproducer:**
  ```bash
  cat > /tmp/Outer.java <<'EOF'
  package com.acme;
  public class Outer { public void hello() {} }
  EOF
  /tmp/bughunt5/run.sh java Outer.java < /tmp/Outer.java
  ```
- **Observed:** qname `Outer.Outer.hello` (file basename × class × method). Canonical Java reference would be `com.acme.Outer.hello`.
- **Expected:** Optional — if a Java `package` declaration is at file top, use it as the module prefix. Same for Kotlin `package`, Scala `package`, C#/PHP namespaces. Tied to F1.
- **Suggested fix shape:** Per-language module_name derivation that respects package declarations. Out of scope for the basename-collision fix alone but adjacent.

### LANG-F26 — Solidity event captured as method (semantic mismatch)

- **Severity:** low
- **Reproducer:** See Solidity fixture above.
- **Observed:** `event Transfer(address indexed from, address indexed to, uint256 v);` captured with kind=method.
- **Expected:** Events aren't methods. Closest in Leonard's vocabulary would be `const` or a new `event` kind. Acceptable as informational since downstream tooling can usually treat events specially.

### LANG-F27 — PHP enum methods not folded under enum

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/e.php <<'EOF'
  <?php
  enum Color: string {
      case Red = "red";
      public function hex(): string { return "#000"; }
  }
  EOF
  /tmp/bughunt5/run.sh php e.php < /tmp/e.php
  ```
- **Observed:** `hex` qname `e.hex` (Color not folded). PHP parent_container_kinds (main.rs:151-156) lists `class_declaration`/`interface_declaration`/`trait_declaration` but not `enum_declaration`.
- **Expected:** qname `e.Color.hex`.
- **Suggested fix shape:** Add `enum_declaration` to PHP's parent_container_kinds.

### LANG-F28 — GLSL/HLSL: `int`/`float` declarations missed

- **Severity:** medium
- **Reproducer:**
  ```bash
  cat > /tmp/s.glsl <<'EOF'
  uniform float intensity;
  uniform int sampleCount;
  vec3 color;
  EOF
  /tmp/bughunt5/run.sh glsl s.glsl < /tmp/s.glsl
  ```
- **Observed:** Only `color` captured. `intensity` (`float`) and `sampleCount` (`int`) silently dropped.
- **Expected:** All three should appear. The SHADER_QUERY pattern requires `type: (type_identifier)`, but primitive types (`float`, `int`, `bool`, `void`) parse as `primitive_type` in tree-sitter-c-family grammars.
- **Suggested fix shape:** Either add a second pattern `(declaration type: (primitive_type) declarator: (identifier) @name) @const`, or relax the type field selector. Worth verifying tree-sitter-glsl uses `primitive_type` (not just `type_identifier`).

### LANG-F29 — Starlark captures rule-calls inside macro body, treating template targets as real

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/x.bzl <<'EOF'
  def my_macro(srcs):
      go_library(name = "inner_target", srcs = srcs)
  EOF
  /tmp/bughunt5/run.sh starlark x.bzl < /tmp/x.bzl
  ```
- **Observed:** `inner_target` captured as a type symbol — but it only exists when `my_macro` is invoked. The query is structural, not flow-sensitive.
- **Expected:** Either skip rule-calls nested inside `function_definition` bodies, or document this as an accepted false positive (macro bodies do define the schema of what targets get materialized, so arguably it's a feature).

### LANG-F30 — `make` rule name `.PHONY` captured (cosmetic)

- **Severity:** informational
- **Reproducer:**
  ```bash
  cat > /tmp/Makefile <<'EOF'
  .PHONY: all
  all:
      echo hi
  EOF
  /tmp/bughunt5/run.sh make Makefile < /tmp/Makefile
  ```
- **Observed:** `.PHONY` produces a function symbol with qname `Makefile..PHONY`. The double-dot in qname is ugly.
- **Expected:** Either skip `.PHONY`/`.SUFFIXES`/dot-prefixed special targets, or accept it (informational only).

### LANG-F31 — R `setClass`/`setGeneric` / `setMethod` invisible

- **Severity:** low
- **Reproducer:**
  ```bash
  cat > /tmp/r.R <<'EOF'
  setClass("Person", representation(name = "character"))
  setGeneric("greet", function(x) standardGeneric("greet"))
  setMethod("greet", "Person", function(x) paste("hi", x@name))
  myFunc <- function(x) x + 1
  EOF
  /tmp/bughunt5/run.sh r r.R < /tmp/r.R
  ```
- **Observed:** Only `myFunc` captured. R's S4 class/generic/method system is invisible.
- **Expected:** S4 is a common pattern in scientific R code. Could add patterns analogous to Elixir's defmodule.
- **Suggested fix shape:** Add patterns for call-expressions with target identifiers `setClass`/`setGeneric`/`setMethod` and pull the string first-arg as name.

### LANG-F32 — `recyclarr.yml` style cross-file indexing (positive test): polyglot skip dirs work

- **Severity:** informational
- **Reproducer:** see "skip dirs" test above — node_modules/target/__pycache__ exclusions hold for all new languages.
- **Observed:** Files inside `node_modules/`, `target/`, `__pycache__/`, `dist/`, `build/`, `vendor/`, `.git/`, `.venv/`, `.next/`, `.nuxt/`, `.tox/`, `.pytest_cache/`, `.ruff_cache/`, `.mypy_cache/`, `venv/` are skipped regardless of language extension. Confirmed for `.lua`, `.kt`, `.rb`, `.cpp`, `.scala`, `.swift`.
- **Expected:** This is the right behavior.

## Things that worked

- **Java nested types**: `Outer.Inner.doIt` qname is correctly folded; constructor `Outer.Outer.Outer` doesn't collide with type `Outer.Outer` (round 4 fix held).
- **Ruby class + method parent-fold**: `Greeter.greet` is correct.
- **Kotlin companion object methods**: fold under enclosing class.
- **PHP modifier detection**: `public`/`private`/`protected` correctly drives `exported`. `public function greet()` returns true; `private function secret()` returns false.
- **Elixir `#any-of?` predicate**: still works (`def`/`defp` both captured under the same pattern).
- **Solidity grammar**: `interface`/`library`/`contract` distinguished correctly; constructor synthesized to `init`.
- **WIT `func_item` in interface bodies**: parent-folded under interface name (works because `interface_item` has `name:` field).
- **CMake multi-line function decls**: `function(name arg1 arg2)` across lines works.
- **CMake `function_def` followed by call**: function name is captured, not the call args.
- **Just `@silent` recipes**: captured as `silent` (`@` stripped).
- **Starlark `name` kwarg in non-first position**: still matches (no anchor constraint).
- **Astro frontmatter normal case**: line numbers correctly offset for embedded script.
- **defaultSkipDirs**: applies to all new language extensions.
- **Tree-sitter doesn't capture comment content**: no false positives from `// class FakeClass {}` style comments in Java/C++.
- **Bash function syntax variants**: both `foo() {...}` and `function foo {...}` captured.

## Open questions

- **Cross-language qname convention**: Should `module_name` change to use the full file path (matching Python/TS) across all tree-sitter languages? This would break any existing project that has hooks consuming qnames. Worth a design decision before fixing F1.
- **Nested type folding (F7)**: design choice — fold or don't? Currently inconsistent (methods fold, nested types don't). Pick one and document.
- **C++ overload handling (F18 generalized)**: every language with overloaded methods (C#, C++, Java, Kotlin, Swift) currently emits identical qnames per overload, with start_line as the only distinguisher. Is that acceptable, or do we need arity/type-in-qname to disambiguate verify_symbol matches?
- **Astro frontmatter (F13)**: how robust does it need to be? A user writing `---` inside a backtick is rare but possible. Probably document as edge case.
- **Elixir nested defmodule (F22)**: the grammar's all-`call`-nodes shape makes parent walk infeasible. Accept as limitation?
- **Per-language `is_exported` policy (F3)**: Ruby/Swift/Lua/Bash default-public — should we add per-language defaults to the Language struct, or document that `exported` is best-effort and consumers shouldn't rely on it for non-modifier languages?

## Out of scope (noted but not probed deeply)

- TypeScript / Python parsers are not tree-sitter; their high-priority dogfood was already covered in earlier bug-hunt rounds.
- Vue and Svelte (.vue / .svelte): same `<script>` extraction shape as Astro; not re-audited.
- Performance — none of the per-language extractors were benchmarked. The 30 s subprocess timeout (treesitter.go:30) has plenty of headroom for the sample sizes tested.
- The `module_name` strip-extension logic on multi-dot files (`foo.test.ts`) — the implementation uses `rsplit_once('.')` which keeps `foo.test` as the module name. Likely fine, not probed deeply.
