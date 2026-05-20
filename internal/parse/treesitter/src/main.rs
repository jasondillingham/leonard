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
/// language: the tree-sitter grammar, the query .scm text, and the
/// module-name derivation rule. Look it up by name from the CLI arg.
struct Language {
    grammar: tree_sitter::Language,
    /// .scm query that captures the symbol-bearing nodes. Each
    /// capture name in the query maps 1:1 to a Leonard symbol kind
    /// via `capture_kind`.
    query_src: &'static str,
}

impl Language {
    fn lookup(name: &str) -> Option<Self> {
        match name {
            "java" => Some(Self {
                grammar: tree_sitter_java::LANGUAGE.into(),
                query_src: JAVA_QUERY,
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

/// capture_kind maps a tree-sitter query capture name to Leonard's
/// cross-language symbol kind vocabulary. Unknown capture names
/// fall through to "function" — keep queries in sync with this
/// table.
fn capture_kind(capture: &str) -> &'static str {
    match capture {
        "type" => "type",
        "interface" => "interface",
        "method" => "method",
        "function" => "function",
        "const" => "const",
        _ => "function",
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
        let (name_node, kind_node) = match (name_node, kind_node) {
            (Some(n), Some(k)) => (n, k),
            _ => continue,
        };
        let name = name_node
            .utf8_text(src.as_bytes())
            .map_err(|e| format!("utf8 text: {}", e))?;
        let kind = capture_kind(kind_capture);
        let qualified_name = format!("{}.{}", module, name);
        let start_line = name_node.start_position().row + 1;
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

/// is_exported applies the per-language visibility heuristic. Java's
/// `public` modifier sits in the node's `modifiers` child; absence
/// means package-private (treated here as not exported, matching how
/// Leonard's other extractors classify private things).
fn is_exported(_kind: &str, node: &tree_sitter::Node, src: &str) -> bool {
    let mut cursor = node.walk();
    for child in node.children(&mut cursor) {
        if child.kind() == "modifiers" {
            if let Ok(text) = child.utf8_text(src.as_bytes()) {
                if text.contains("public") {
                    return true;
                }
            }
        }
    }
    false
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
