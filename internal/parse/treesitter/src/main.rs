//! Leonard's tree-sitter multi-language symbol extractor.
//!
//! Reads source code from stdin and emits a JSON array of symbols on
//! stdout in the same shape as the syn-based Rust extractor and the
//! Python helper. Tree-sitter handles the parsing; per-language
//! `.scm` queries pick out the node kinds Leonard's shallow symbol
//! model needs (functions, methods, types, etc.).
//!
//! CLI shape (matches the syn extractor's contract so the Go-side
//! wrapper stays simple):
//!
//!     leonard-extract-treesitter --lang java path/to/File.java
//!
//! Exit codes:
//!   0  — success, JSON array on stdout
//!   1  — read stdin / serialize / IO failure (single-line msg on stderr)
//!   2  — parse error / unknown language (single-line msg on stderr)
//!
//! Adding a new language: add the grammar to Cargo.toml, register it
//! in `Language::lookup`, write the .scm query, and the Go side gets
//! it for free via ExtractTreeSitter("name", ...).

use std::io::{self, Read, Write};
use std::process::ExitCode;

use serde::Serialize;
use tree_sitter::{Parser, Query, QueryCursor, StreamingIterator};

/// Symbol matches the JSON shape every Leonard extractor produces.
/// Keep the field names + kind vocabulary stable across languages —
/// Go-side `parse.Symbol` and downstream `store.Symbol` consume this
/// shape directly.
#[derive(Serialize)]
struct Symbol<'a> {
    qualified_name: String,
    name: &'a str,
    kind: &'a str,
    signature: String,
    start_line: usize,
    end_line: usize,
    exported: bool,
}

#[derive(Serialize)]
struct ParseError<'a> {
    error: &'a str,
    detail: String,
}

/// Language carries everything needed to extract symbols for a given
/// language: the tree-sitter grammar, the query .scm text, the
/// per-language list of node kinds that count as "parent containers"
/// for method-name folding, and the module-name derivation rule.
/// Look it up by name from the CLI arg.
struct Language {
    grammar: tree_sitter::Language,
    /// .scm query that captures the symbol-bearing nodes. Each
    /// capture name in the query maps 1:1 to a Leonard symbol kind
    /// via `capture_kind`.
    query_src: &'static str,
    /// Node kinds that are valid parents for methods (used for qname
    /// folding). When a method-shaped capture is emitted, the
    /// extractor walks the node's ancestors looking for any of these
    /// kinds and prepends the parent's name in the qname:
    ///   module.Parent.method  instead of  module.method
    /// Disambiguates the Java/C# class-constructor-shares-name case.
    parent_container_kinds: &'static [&'static str],
}

impl Language {
    fn lookup(name: &str) -> Option<Self> {
        match name {
            "java" => Some(Self {
                grammar: tree_sitter_java::LANGUAGE.into(),
                query_src: JAVA_QUERY,
                parent_container_kinds: &[
                    "class_declaration",
                    "interface_declaration",
                    "record_declaration",
                    "enum_declaration",
                    "annotation_type_declaration",
                ],
            }),
            "ruby" => Some(Self {
                grammar: tree_sitter_ruby::LANGUAGE.into(),
                query_src: RUBY_QUERY,
                parent_container_kinds: &["class", "module", "singleton_class"],
            }),
            "csharp" => Some(Self {
                grammar: tree_sitter_c_sharp::LANGUAGE.into(),
                query_src: CSHARP_QUERY,
                parent_container_kinds: &[
                    "class_declaration",
                    "interface_declaration",
                    "struct_declaration",
                    "record_declaration",
                    "enum_declaration",
                ],
            }),
            "swift" => Some(Self {
                grammar: tree_sitter_swift::LANGUAGE.into(),
                query_src: SWIFT_QUERY,
                parent_container_kinds: &[
                    "class_declaration",
                    "protocol_declaration",
                ],
            }),
            "kotlin" => Some(Self {
                grammar: tree_sitter_kotlin_ng::LANGUAGE.into(),
                query_src: KOTLIN_QUERY,
                parent_container_kinds: &[
                    "class_declaration",
                    "object_declaration",
                ],
            }),
            "scala" => Some(Self {
                grammar: tree_sitter_scala::LANGUAGE.into(),
                query_src: SCALA_QUERY,
                parent_container_kinds: &[
                    "class_definition",
                    "object_definition",
                    "trait_definition",
                ],
            }),
            "dart" => Some(Self {
                grammar: tree_sitter_dart::language(),
                query_src: DART_QUERY,
                parent_container_kinds: &["class_definition"],
            }),
            "c" => Some(Self {
                grammar: tree_sitter_c::LANGUAGE.into(),
                query_src: C_QUERY,
                parent_container_kinds: &[
                    "struct_specifier",
                    "union_specifier",
                ],
            }),
            "cpp" => Some(Self {
                grammar: tree_sitter_cpp::LANGUAGE.into(),
                query_src: CPP_QUERY,
                parent_container_kinds: &[
                    "class_specifier",
                    "struct_specifier",
                    "union_specifier",
                    "namespace_definition",
                ],
            }),
            "php" => Some(Self {
                grammar: tree_sitter_php::LANGUAGE_PHP.into(),
                query_src: PHP_QUERY,
                parent_container_kinds: &[
                    "class_declaration",
                    "interface_declaration",
                    "trait_declaration",
                ],
            }),
            "lua" => Some(Self {
                grammar: tree_sitter_lua::LANGUAGE.into(),
                query_src: LUA_QUERY,
                // Lua has no class concept — table-as-namespace is
                // the convention but the grammar doesn't tag it.
                // Methods declared via M.foo() / M:foo() get folded
                // via the table identifier captured in the query
                // itself, not via ancestor walk.
                parent_container_kinds: &[],
            }),
            "bash" => Some(Self {
                grammar: tree_sitter_bash::LANGUAGE.into(),
                query_src: BASH_QUERY,
                parent_container_kinds: &[],
            }),
            "zig" => Some(Self {
                grammar: tree_sitter_zig::LANGUAGE.into(),
                query_src: ZIG_QUERY,
                parent_container_kinds: &[],
            }),
            "nix" => Some(Self {
                grammar: tree_sitter_nix::LANGUAGE.into(),
                query_src: NIX_QUERY,
                parent_container_kinds: &[],
            }),
            "elixir" => Some(Self {
                grammar: tree_sitter_elixir::LANGUAGE.into(),
                query_src: ELIXIR_QUERY,
                // Elixir's defmodule wraps a do_block; the parent
                // walk-up goes through call nodes which are not
                // useful for qname-folding here. Keep empty for v0.24.
                parent_container_kinds: &[],
            }),
            "solidity" => Some(Self {
                grammar: tree_sitter_solidity::LANGUAGE.into(),
                query_src: SOLIDITY_QUERY,
                parent_container_kinds: &[
                    "contract_declaration",
                    "interface_declaration",
                    "library_declaration",
                ],
            }),
            "make" => Some(Self {
                grammar: tree_sitter_make::LANGUAGE.into(),
                query_src: MAKE_QUERY,
                parent_container_kinds: &[],
            }),
            "cmake" => Some(Self {
                grammar: tree_sitter_cmake::LANGUAGE.into(),
                query_src: CMAKE_QUERY,
                parent_container_kinds: &[],
            }),
            "hcl" => Some(Self {
                grammar: tree_sitter_hcl::LANGUAGE.into(),
                query_src: HCL_QUERY,
                parent_container_kinds: &[],
            }),
            "graphql" => Some(Self {
                grammar: tree_sitter_graphql::LANGUAGE.into(),
                query_src: GRAPHQL_QUERY,
                parent_container_kinds: &[
                    "object_type_definition",
                    "interface_type_definition",
                    "input_object_type_definition",
                ],
            }),
            "proto" => Some(Self {
                grammar: tree_sitter_proto::LANGUAGE.into(),
                query_src: PROTO_QUERY,
                parent_container_kinds: &["message", "service"],
            }),
            "sql" => Some(Self {
                grammar: tree_sitter_sequel::LANGUAGE.into(),
                query_src: SQL_QUERY,
                parent_container_kinds: &["create_table"],
            }),
            "wit" => Some(Self {
                grammar: tree_sitter_wit::language(),
                query_src: WIT_QUERY,
                parent_container_kinds: &["interface_item", "world_item"],
            }),
            "erlang" => Some(Self {
                grammar: tree_sitter_erlang::LANGUAGE.into(),
                query_src: ERLANG_QUERY,
                parent_container_kinds: &[],
            }),
            "r" => Some(Self {
                grammar: tree_sitter_r::LANGUAGE.into(),
                query_src: R_QUERY,
                parent_container_kinds: &[],
            }),
            "just" => Some(Self {
                grammar: tree_sitter_just::LANGUAGE.into(),
                query_src: JUST_QUERY,
                parent_container_kinds: &[],
            }),
            "starlark" => Some(Self {
                grammar: tree_sitter_starlark::LANGUAGE.into(),
                query_src: STARLARK_QUERY,
                parent_container_kinds: &[],
            }),
            _ => None,
        }
    }
}

/// JAVA_QUERY captures top-level Java declarations Leonard tracks.
/// Each `@capture` name encodes the Leonard symbol kind for the
/// matched node — `@function`, `@type`, `@interface`, `@method`,
/// `@const`. The `name:` field-name selector inside each pattern
/// pins the identifier child so the same query gives us both the
/// containing node (for span/exported derivation) and the bare name.
const JAVA_QUERY: &str = r#"
(class_declaration
  name: (identifier) @name) @type

(interface_declaration
  name: (identifier) @name) @interface

(annotation_type_declaration
  name: (identifier) @name) @interface

(record_declaration
  name: (identifier) @name) @type

(enum_declaration
  name: (identifier) @name) @type

(method_declaration
  name: (identifier) @name) @method

(constructor_declaration
  name: (identifier) @name) @method
"#;

/// RUBY_QUERY captures Ruby's top-level decls. Ruby has `class`,
/// `module`, `method` (instance) and `singleton_method` (class
/// methods like `def self.foo`).
const RUBY_QUERY: &str = r#"
(class
  name: (constant) @name) @type

(module
  name: (constant) @name) @type

(method
  name: (identifier) @name) @method

(singleton_method
  name: (identifier) @name) @method
"#;

/// CSHARP_QUERY captures C# decls. Differs from Java mostly in
/// having structs as a distinct kind and delegate_declaration for
/// function-pointer types. Properties and fields exposed under
/// const + interface for cross-language consistency.
const CSHARP_QUERY: &str = r#"
(class_declaration
  name: (identifier) @name) @type

(interface_declaration
  name: (identifier) @name) @interface

(struct_declaration
  name: (identifier) @name) @type

(record_declaration
  name: (identifier) @name) @type

(enum_declaration
  name: (identifier) @name) @type

(delegate_declaration
  name: (identifier) @name) @type

(method_declaration
  name: (identifier) @name) @method

(constructor_declaration
  name: (identifier) @name) @method
"#;

/// SWIFT_QUERY captures Swift's top-level decls. Tree-sitter-swift
/// quirks worth knowing:
///   - `class_declaration` is the node for class, struct, AND enum
///     (the body type — class_body vs enum_class_body — is the
///     discriminator). v0.20 maps all three to kind=type; future
///     refinement could split.
///   - `init_declaration` has no `name:` field; we synthesize "init"
///     in the extractor when an `@method` capture has no `@name`.
///   - Protocols use `protocol_function_declaration` (no body) for
///     their method declarations, separate from `function_declaration`.
const SWIFT_QUERY: &str = r#"
(class_declaration
  name: (type_identifier) @name) @type

(protocol_declaration
  name: (type_identifier) @name) @interface

(function_declaration
  name: (simple_identifier) @name) @function

(protocol_function_declaration
  name: (simple_identifier) @name) @method

(init_declaration) @method.init
"#;

/// KOTLIN_QUERY (tree-sitter-kotlin-ng). Kotlin's `interface` and
/// `enum class` ride on `class_declaration` — the keyword is the
/// discriminator, not a separate node kind. v0.21 maps all to type.
/// Objects (Kotlin's singleton companion-or-standalone) get
/// object_declaration.
const KOTLIN_QUERY: &str = r#"
(class_declaration
  name: (identifier) @name) @type

(object_declaration
  name: (identifier) @name) @type

(function_declaration
  name: (identifier) @name) @method
"#;

/// SCALA_QUERY uses Scala's `_definition` suffix (vs Java/C#'s
/// `_declaration`). Traits map to interface; class + object both
/// to type. function_definition is concrete; function_declaration
/// is the abstract trait-method form.
const SCALA_QUERY: &str = r#"
(class_definition
  name: (identifier) @name) @type

(object_definition
  name: (identifier) @name) @type

(trait_definition
  name: (identifier) @name) @interface

(function_definition
  name: (identifier) @name) @method

(function_declaration
  name: (identifier) @name) @method
"#;

/// DART_QUERY captures class_definition (used for both regular and
/// abstract classes) and function_signature (used by both methods
/// inside classes and top-level free functions — parent-folding
/// disambiguates).
const DART_QUERY: &str = r#"
(class_definition
  name: (identifier) @name) @type

(function_signature
  name: (identifier) @name) @function
"#;

/// C_QUERY captures top-level C declarations. Functions don't use a
/// `name:` field — the identifier lives inside
/// `declarator: (function_declarator declarator: (identifier))`.
/// Tree-sitter queries handle the nesting fine.
///
/// typedef in C produces type_definition whose `declarator` IS the
/// typedef name (a type_identifier at that position, since the typedef
/// is defining a new type alias).
const C_QUERY: &str = r#"
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name)) @function

(struct_specifier
  name: (type_identifier) @name) @type

(union_specifier
  name: (type_identifier) @name) @type

(enum_specifier
  name: (type_identifier) @name) @type

(type_definition
  declarator: (type_identifier) @name) @type
"#;

/// CPP_QUERY captures C++ on top of C plus: namespace_definition
/// (treated as type — Leonard's vocabulary doesn't have a namespace
/// kind), class_specifier, methods inside class bodies (which are
/// field_declaration nodes containing function_declarators).
///
/// Templates and operator overloads are NOT captured as separate
/// symbols in v0.22 — the templated class/function is what we want;
/// template_declaration wraps it transparently.
const CPP_QUERY: &str = r#"
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name)) @function

(function_definition
  declarator: (function_declarator
    declarator: (qualified_identifier
      name: (identifier) @name))) @function

(class_specifier
  name: (type_identifier) @name) @type

(struct_specifier
  name: (type_identifier) @name) @type

(union_specifier
  name: (type_identifier) @name) @type

(enum_specifier
  name: (type_identifier) @name) @type

(namespace_definition
  name: (namespace_identifier) @name) @type

(type_definition
  declarator: (type_identifier) @name) @type

(field_declaration
  declarator: (function_declarator
    declarator: (field_identifier) @name)) @method
"#;

/// PHP_QUERY (tree-sitter-php). PHP uses `(name)` as the canonical
/// identifier node kind. Coverage: namespace, class, interface,
/// trait, method (inside class/trait/interface), free function.
const PHP_QUERY: &str = r#"
(namespace_definition
  name: (namespace_name) @name) @type

(class_declaration
  name: (name) @name) @type

(interface_declaration
  name: (name) @name) @interface

(trait_declaration
  name: (name) @name) @type

(enum_declaration
  name: (name) @name) @type

(method_declaration
  name: (name) @name) @method

(function_definition
  name: (name) @name) @function
"#;

/// LUA_QUERY captures three function-declaration shapes Lua uses:
/// plain (`function foo()`), dot-indexed (`function M.foo()`), and
/// method-indexed (`function M:foo()`). The dot/method forms are
/// the Lua idiom for class-like patterns — capture them as methods
/// since they're conceptually instance/static methods on the table.
///
/// Lua has no real class node kind, so parent-folding is a no-op
/// here; the .scm captures the table portion of the name only as a
/// disambiguator.
const LUA_QUERY: &str = r#"
(function_declaration
  name: (identifier) @name) @function

(function_declaration
  name: (dot_index_expression
    field: (identifier) @name)) @method

(function_declaration
  name: (method_index_expression
    method: (identifier) @name)) @method
"#;

/// BASH_QUERY captures shell function definitions. Bash's symbol
/// surface is minimal — functions are about all there is. Top-level
/// `readonly` / `declare` variables could be added in a refinement.
const BASH_QUERY: &str = r#"
(function_definition
  name: (word) @name) @function
"#;

/// ZIG_QUERY. Zig structs are values bound to a const, not a
/// top-level kind: `const Foo = struct { ... };`. To capture them
/// as types, we match variable_declaration nodes whose RHS is a
/// struct_declaration.
const ZIG_QUERY: &str = r#"
(function_declaration
  name: (identifier) @name) @function

(variable_declaration
  (identifier) @name
  (struct_declaration)) @type

(variable_declaration
  (identifier) @name
  (enum_declaration)) @type

(variable_declaration
  (identifier) @name
  (union_declaration)) @type
"#;

/// NIX_QUERY captures top-level function bindings — `foo = arg: ...`
/// is the Nix idiom for declaring a function. Non-function bindings
/// (data, strings, derivation attrs) aren't captured in v0.24 — the
/// generic @const variant produced too many false positives on
/// realistic flake.nix / default.nix files.
const NIX_QUERY: &str = r#"
(binding
  attrpath: (attrpath attr: (identifier) @name)
  expression: (function_expression)) @function
"#;

/// ELIXIR_QUERY uses #eq? predicates to filter call nodes by their
/// target identifier. defmodule/def/defp/defmacro/defprotocol all
/// parse as identical-shape `call` nodes — only the target text
/// differs. The double-paren around (identifier) is needed because
/// the predicate operates on the capture, not the pattern.
const ELIXIR_QUERY: &str = r#"
(call
  target: ((identifier) @_def
            (#eq? @_def "defmodule"))
  (arguments (alias) @name)) @type

(call
  target: ((identifier) @_def
            (#eq? @_def "defprotocol"))
  (arguments (alias) @name)) @interface

(call
  target: ((identifier) @_def
            (#any-of? @_def "def" "defp"))
  (arguments (call target: (identifier) @name))) @method

(call
  target: ((identifier) @_def
            (#eq? @_def "defmacro"))
  (arguments (call target: (identifier) @name))) @method
"#;

/// SOLIDITY_QUERY captures the smart-contract structural elements
/// Leonard's symbol model maps onto: contract/interface/library as
/// the type-or-interface, function/modifier/event/constructor as
/// methods. Solidity's grammar names are explicit (no overloading
/// of one node kind for multiple purposes), so the query is direct.
const SOLIDITY_QUERY: &str = r#"
(contract_declaration
  name: (identifier) @name) @type

(interface_declaration
  name: (identifier) @name) @interface

(library_declaration
  name: (identifier) @name) @type

(function_definition
  name: (identifier) @name) @method

(modifier_definition
  name: (identifier) @name) @method

(event_definition
  name: (identifier) @name) @method

(constructor_definition) @method.init
"#;

/// MAKE_QUERY captures Makefile build targets and top-level variable
/// assignments. Make's "symbols" are sparse — targets and vars are
/// the unit. Targets become @function (the build action); variables
/// become @const.
const MAKE_QUERY: &str = r#"
(rule
  (targets (word) @name)) @function

(variable_assignment
  name: (word) @name) @const
"#;

/// CMAKE_QUERY captures CMake function definitions plus the
/// common target-introducing commands (add_library, add_executable,
/// option). Functions defined via `function(name)...endfunction()`
/// look distinct from regular commands; add_library / add_executable
/// look like normal_command nodes filtered by their identifier text.
const CMAKE_QUERY: &str = r#"
(function_def
  (function_command
    (argument_list
      . (argument (unquoted_argument) @name)))) @function

(normal_command
  (identifier) @_cmd
  (argument_list
    . (argument (unquoted_argument) @name))
  (#any-of? @_cmd "add_library" "add_executable" "option")) @type
"#;

/// HCL_QUERY captures Terraform / HCL declaration blocks. Every
/// resource/variable/module/output/data/provider/locals block has
/// shape `(block (identifier) (string_lit (template_literal)) ...)`.
/// The identifier is the keyword (resource/variable/etc.); the
/// first string_lit's template_literal child is the block's name.
///
/// v0.27 captures only the first label. Multi-label blocks
/// (`resource "aws_instance" "web"`) keep just the type name in
/// the Symbol; the resource instance name is lost — refinement
/// for a later version.
const HCL_QUERY: &str = r#"
(block
  (identifier) @_kind
  (string_lit (template_literal) @name)
  (#any-of? @_kind "resource" "variable" "module" "output"
                    "data" "provider" "locals" "terraform")) @type
"#;

/// GRAPHQL_QUERY covers SDL declarations. object/interface/enum/
/// scalar/union/input — all map to type or interface in Leonard's
/// vocabulary. Field definitions inside an object/interface are
/// captured as @method so parent-folding gives them parent-scoped
/// qnames.
const GRAPHQL_QUERY: &str = r#"
(object_type_definition
  (name) @name) @type

(interface_type_definition
  (name) @name) @interface

(enum_type_definition
  (name) @name) @type

(scalar_type_definition
  (name) @name) @type

(union_type_definition
  (name) @name) @type

(input_object_type_definition
  (name) @name) @type

(field_definition
  (name) @name) @method
"#;

/// PROTO_QUERY captures Protocol Buffers schema elements. Messages
/// and enums are types; services are interfaces; RPCs are methods
/// (parent-folded under their service).
const PROTO_QUERY: &str = r#"
(message
  (message_name (identifier) @name)) @type

(enum
  (enum_name (identifier) @name)) @type

(service
  (service_name (identifier) @name)) @interface

(rpc
  (rpc_name (identifier) @name)) @method
"#;

/// SQL_QUERY (tree-sitter-sequel) captures schema-defining
/// statements: CREATE TABLE/VIEW/INDEX/FUNCTION, and column
/// definitions inside CREATE TABLE bodies. ALTER TABLE isn't
/// captured as a "symbol" itself — it modifies an existing one
/// — but its add-column children could be (deferred to a
/// refinement).
///
/// Schema-as-source-of-truth is the use case Leonard targets here:
/// migrations live as .sql files in most modern projects, and
/// Claude verifying "does column users.email exist" against the
/// indexed migration tree is a real prevention vector.
const SQL_QUERY: &str = r#"
(create_table
  (object_reference name: (identifier) @name)) @type

(create_view
  (object_reference name: (identifier) @name)) @type

(create_index
  column: (identifier) @name) @type

(create_function
  (object_reference name: (identifier) @name)) @function

(column_definition
  name: (identifier) @name) @const
"#;

/// WIT_QUERY (tree-sitter-wit) covers the WebAssembly Interface
/// Types declarations: interface, world, plus typedef wrappers
/// (record, enum, variant, flags, type-alias) and free func_items
/// inside interface bodies.
const WIT_QUERY: &str = r#"
(interface_item
  name: (identifier) @name) @interface

(world_item
  name: (identifier) @name) @type

(func_item
  name: (identifier) @name) @method

(record_item
  name: (identifier) @name) @type

(enum_item
  name: (identifier) @name) @type

(variant_item
  name: (identifier) @name) @type

(flags_item
  name: (identifier) @name) @type
"#;

/// ERLANG_QUERY (tree-sitter-erlang). Erlang's `fun_decl` wraps
/// a function_clause whose `name:` is the function ident — two
/// levels of nesting from the @function capture. record_decl and
/// module_attribute have flatter shapes.
const ERLANG_QUERY: &str = r#"
(module_attribute
  name: (atom) @name) @type

(record_decl
  name: (atom) @name) @type

(fun_decl
  clause: (function_clause name: (atom) @name)) @function
"#;

/// R_QUERY uses R's `<-` assignment idiom for function declarations.
/// `x <- function(...) { ... }` parses as binary_operator whose
/// rhs is function_definition. v0.33 captures only function-shaped
/// assignments; constant assignments would overlap noisily.
const R_QUERY: &str = r#"
(binary_operator
  lhs: (identifier) @name
  rhs: (function_definition)) @function
"#;

/// JUST_QUERY (tree-sitter-just) captures justfile recipes
/// (build/test commands) as functions and top-level variable
/// assignments as consts.
const JUST_QUERY: &str = r#"
(recipe
  (recipe_header name: (identifier) @name)) @function

(assignment
  left: (identifier) @name) @const
"#;

/// STARLARK_QUERY (Bazel BUILD/*.bzl). Two complementary captures:
///
///   1. function_definition — Python-style `def` macros, the
///      reusable abstractions users write in .bzl files.
///   2. call invocations whose first arg is a `name = "..."`
///      keyword — these are Bazel rule calls (go_library,
///      cc_binary, etc.) that declare a build target. The string
///      value of the `name` arg becomes the symbol name; the
///      rule function name (go_library, etc.) is the "kind"
///      context surfaced via the #any-of? predicate.
const STARLARK_QUERY: &str = r#"
(function_definition
  name: (identifier) @name) @function

(call
  arguments: (argument_list
    (keyword_argument
      name: (identifier) @_arg
      value: (string (string_content) @name)))
  (#eq? @_arg "name")) @type
"#;

/// capture_kind maps a tree-sitter query capture name to Leonard's
/// cross-language symbol kind vocabulary. Unknown capture names
/// fall through to "function" — keep queries in sync with this
/// table.
fn capture_kind(capture: &str) -> &'static str {
    match capture {
        "type" => "type",
        "interface" => "interface",
        "method" | "method.init" => "method",
        "function" => "function",
        "const" => "const",
        _ => "function",
    }
}

/// synthesized_name returns the implicit name for capture types
/// that don't carry an @name child (Swift's init_declaration is the
/// only one in v0.20). Returns None when the capture should be
/// skipped due to a missing name.
fn synthesized_name(capture: &str) -> Option<&'static str> {
    match capture {
        "method.init" => Some("init"),
        _ => None,
    }
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().collect();
    let (lang_name, path) = match parse_args(&args) {
        Ok(v) => v,
        Err(msg) => {
            eprintln!("leonard-extract-treesitter: {}", msg);
            return ExitCode::from(1);
        }
    };

    let lang = match Language::lookup(&lang_name) {
        Some(l) => l,
        None => {
            emit_parse_error(&format!("unknown language: {}", lang_name));
            return ExitCode::from(2);
        }
    };

    let mut src = String::new();
    if let Err(err) = io::stdin().read_to_string(&mut src) {
        eprintln!("leonard-extract-treesitter: read stdin: {}", err);
        return ExitCode::from(1);
    }

    let mut parser = Parser::new();
    if parser.set_language(&lang.grammar).is_err() {
        eprintln!("leonard-extract-treesitter: failed to set language");
        return ExitCode::from(1);
    }

    let tree = match parser.parse(&src, None) {
        Some(t) => t,
        None => {
            emit_parse_error("parse failed");
            return ExitCode::from(2);
        }
    };

    let module = module_name(&path);
    let symbols = match extract(&tree, &lang, &src, &module) {
        Ok(s) => s,
        Err(msg) => {
            emit_parse_error(&msg);
            return ExitCode::from(2);
        }
    };

    let out = serde_json::to_string(&symbols).unwrap_or_else(|_| "[]".to_string());
    if let Err(err) = io::stdout().write_all(out.as_bytes()) {
        eprintln!("leonard-extract-treesitter: write stdout: {}", err);
        return ExitCode::from(1);
    }
    ExitCode::SUCCESS
}

fn parse_args(args: &[String]) -> Result<(String, String), &'static str> {
    // Expected: program --lang <name> <path>
    let mut lang = None;
    let mut path = None;
    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--lang" => {
                i += 1;
                if i >= args.len() {
                    return Err("--lang requires a value");
                }
                lang = Some(args[i].clone());
            }
            other => {
                if path.is_some() {
                    return Err("unexpected extra positional arg");
                }
                path = Some(other.to_string());
            }
        }
        i += 1;
    }
    let lang = lang.ok_or("missing --lang <name>")?;
    let path = path.ok_or("missing path argument")?;
    Ok((lang, path))
}

fn emit_parse_error(detail: &str) {
    let err = ParseError {
        error: "ParseError",
        detail: detail.to_string(),
    };
    let json = serde_json::to_string(&err).unwrap_or_else(|_| "{}".to_string());
    let _ = io::stdout().write_all(json.as_bytes());
}

/// extract walks every match of the language's query against the
/// parse tree and produces a Leonard Symbol per top-level capture.
/// "Top-level capture" means the @capture name that names the symbol
/// kind (@type/@method/etc.) — @name is the identifier child and
/// gets used for the Symbol.name field but isn't a symbol on its own.
fn extract<'src>(
    tree: &tree_sitter::Tree,
    lang: &Language,
    src: &'src str,
    module: &str,
) -> Result<Vec<Symbol<'src>>, String> {
    let query = Query::new(&lang.grammar, lang.query_src)
        .map_err(|e| format!("query compile failed: {}", e))?;
    let capture_names = query.capture_names();
    // Identify which capture indexes are "kind captures" vs the @name
    // child. Anything not literally "name" is treated as a symbol-
    // kind capture; the corresponding node becomes one Symbol.
    let name_idx = capture_names
        .iter()
        .position(|n| *n == "name")
        .ok_or_else(|| "query missing @name capture".to_string())?;

    let mut cursor = QueryCursor::new();
    let mut out = Vec::new();
    let mut matches = cursor.matches(&query, tree.root_node(), src.as_bytes());
    while let Some(m) = matches.next() {
        // For each match the query produces one symbol; the @name
        // capture identifies the bare ident, the other capture
        // identifies the enclosing node (used for span + signature).
        let mut name_node = None;
        let mut kind_node = None;
        let mut kind_capture: &str = "function";
        for cap in m.captures {
            let cn = capture_names[cap.index as usize];
            if cap.index as usize == name_idx {
                name_node = Some(cap.node);
            } else if cn.starts_with('_') {
                // Capture names prefixed with `_` are predicate-
                // only (e.g. Elixir's @_def for #eq? filtering on
                // call targets). They don't carry symbol-kind
                // information; skip them when picking the
                // kind_node so the actual @type/@method/etc.
                // capture wins.
                continue;
            } else {
                kind_node = Some(cap.node);
                kind_capture = cn;
            }
        }
        let kind_node = match kind_node {
            Some(k) => k,
            None => continue,
        };
        // Allow captures that have no @name child by synthesizing a
        // name from the capture's role — Swift's init_declaration is
        // the v0.20 example. synthesized_name returns &'static str
        // which coerces to the src-borrowed lifetime fine.
        let name: &str = if let Some(n) = name_node {
            n.utf8_text(src.as_bytes())
                .map_err(|e| format!("utf8 text: {}", e))?
        } else if let Some(syn) = synthesized_name(kind_capture) {
            syn
        } else {
            continue;
        };
        let kind = capture_kind(kind_capture);
        // For method-shaped captures, fold the enclosing container's
        // name into the qname so `Greeter` (class) and `Greeter()`
        // (constructor) don't share `Greeter.Greeter`. v0.19 left
        // this collision in; v0.20 fixes it.
        let qualified_name = if matches!(kind, "method" | "function")
            && !lang.parent_container_kinds.is_empty()
        {
            if let Some(parent) =
                find_parent_name(&kind_node, lang.parent_container_kinds, src)
            {
                format!("{}.{}.{}", module, parent, name)
            } else {
                format!("{}.{}", module, name)
            }
        } else {
            format!("{}.{}", module, name)
        };
        // start_line anchors on the @name child when present (matches
        // Python/TS — skips leading attrs/doc comments); falls back
        // to the enclosing node's start when synthesized (Swift init).
        let start_line = name_node
            .map(|n| n.start_position().row + 1)
            .unwrap_or_else(|| kind_node.start_position().row + 1);
        let end_line = kind_node.end_position().row + 1;
        let signature = render_signature(kind, name, &kind_node, src);
        let exported = is_exported(kind, &kind_node, src);
        out.push(Symbol {
            qualified_name,
            name,
            kind,
            signature,
            start_line,
            end_line,
            exported,
        });
    }
    Ok(out)
}

/// render_signature builds the short cross-language signature string
/// callers see via `find_symbol`. v0.19 keeps it simple — name +
/// kind marker — and individual language additions can refine later
/// (e.g. Java method params) by branching on `kind`.
fn render_signature(kind: &str, name: &str, _node: &tree_sitter::Node, _src: &str) -> String {
    match kind {
        "type" | "interface" => format!("{} {}", kind, name),
        "method" | "function" => format!("fn {}()", name),
        "const" => format!("const {}", name),
        _ => name.to_string(),
    }
}

/// is_exported applies a cross-language visibility heuristic. Many
/// tree-sitter grammars expose a `modifiers` child carrying the
/// access modifier text — Java `public`, C# `public`/`internal`,
/// Swift modifiers under `modifiers`. The cross-language convention
/// Leonard uses: visible-by-default languages (Ruby) mark
/// everything exported; access-modifier languages export only
/// when "public" or "open" appears.
fn is_exported(_kind: &str, node: &tree_sitter::Node, src: &str) -> bool {
    let mut cursor = node.walk();
    for child in node.children(&mut cursor) {
        let ck = child.kind();
        if ck == "modifiers" || ck == "modifier" || ck == "visibility_modifier" {
            if let Ok(text) = child.utf8_text(src.as_bytes()) {
                if text.contains("public") || text.contains("open") {
                    return true;
                }
                if text.contains("private")
                    || text.contains("internal")
                    || text.contains("protected")
                    || text.contains("fileprivate")
                {
                    return false;
                }
            }
        }
    }
    // No modifier seen — language-default visibility. For Ruby,
    // methods are public by default; for Java, package-private (not
    // exported). The grammars don't expose a "this is Ruby" flag,
    // so we approximate: classes/interfaces seen without modifiers
    // default to exported (matches Ruby and Swift) but methods
    // default to NOT exported (matches Java/C# package-private).
    matches!(_kind, "type" | "interface")
}

/// find_parent_name walks node's ancestors looking for the first
/// node whose kind is in container_kinds, then returns the text of
/// that node's `name:` child. Used to fold a method's enclosing
/// class name into the method's qualified_name.
fn find_parent_name(
    node: &tree_sitter::Node,
    container_kinds: &[&str],
    src: &str,
) -> Option<String> {
    let mut cur = node.parent();
    while let Some(p) = cur {
        if container_kinds.iter().any(|k| *k == p.kind()) {
            // Try the conventional `name:` field first; some
            // grammars use different field names (Ruby's `name`
            // points at a constant node).
            if let Some(name_node) = p.child_by_field_name("name") {
                if let Ok(text) = name_node.utf8_text(src.as_bytes()) {
                    return Some(text.to_string());
                }
            }
        }
        cur = p.parent();
    }
    None
}

/// module_name derives the Leonard "module" prefix for qualified
/// names. v0.19 uses the file's basename minus extension — same as
/// the syn extractor's default. Per-language refinements (e.g.
/// reading Java package decls) can land in later versions.
fn module_name(path: &str) -> String {
    let last = path.rsplit('/').next().unwrap_or(path);
    let stem = last.rsplit_once('.').map(|(s, _)| s).unwrap_or(last);
    stem.to_string()
}
