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
            if cap.index as usize == name_idx {
                name_node = Some(cap.node);
            } else {
                kind_node = Some(cap.node);
                kind_capture = capture_names[cap.index as usize];
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
