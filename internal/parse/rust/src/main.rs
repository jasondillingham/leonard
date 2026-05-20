//! Symbol extractor invoked by internal/parse/rust.go via subprocess.
//!
//! Reads Rust source from stdin, optionally takes a qualified-name
//! prefix as argv[1], and prints a JSON array of symbol records to
//! stdout. On parse errors, writes a single line "line N: ParseError:
//! msg" to stderr and exits 2 — the Go side surfaces that as a per-file
//! ParseFailure exactly the same shape as the Python lane.
//!
//! Contract is narrow on purpose, matching the v0 brief for the other
//! languages: top-level fn, struct, enum, trait, type alias, const,
//! static, plus methods one level deep inside `impl Foo` blocks. We
//! do not descend into function bodies, modules, or nested impls —
//! Leonard's index is shallow.

use std::io::{self, Read, Write};
use std::process::ExitCode;

use serde::Serialize;
use syn::spanned::Spanned;
use syn::visit::{self, Visit};

#[derive(Serialize)]
struct Symbol {
    name: String,
    qualified_name: String,
    kind: &'static str,
    signature: String,
    start_line: usize,
    end_line: usize,
    exported: bool,
}

/// Walker over a parsed syn::File. Tracks only what's needed for the
/// shallow v0 contract: depth (so methods at impl-level are emitted
/// but functions in functions are skipped) and current_impl (so
/// methods get the right qualified_name prefix).
struct Extractor<'a> {
    prefix: &'a str,
    out: Vec<Symbol>,
    /// Name of the type currently inside an `impl Foo { ... }` block.
    /// Empty when at top level. Set during visit_item_impl; used by
    /// visit_impl_item_fn to qualify method names.
    current_impl: String,
}

impl<'a> Extractor<'a> {
    fn new(prefix: &'a str) -> Self {
        Self {
            prefix,
            out: Vec::new(),
            current_impl: String::new(),
        }
    }

    fn qname(&self, name: &str) -> String {
        if self.prefix.is_empty() {
            name.to_string()
        } else {
            format!("{}.{}", self.prefix, name)
        }
    }

    fn method_qname(&self, name: &str) -> String {
        // Methods are always inside an impl, so prefix.Type.method
        // when prefix is set, Type.method when not.
        let base = if self.prefix.is_empty() {
            self.current_impl.clone()
        } else {
            format!("{}.{}", self.prefix, self.current_impl)
        };
        format!("{}.{}", base, name)
    }

    fn span_lines(&self, span: proc_macro2::Span) -> (usize, usize) {
        let start = span.start();
        let end = span.end();
        (start.line.max(1), end.line.max(start.line.max(1)))
    }

    /// Bughunt-3 rust F2: Python/TS report start_line at the
    /// identifier line (skipping leading attributes and doc-comments).
    /// Match that — pass the ident's span as the start anchor and the
    /// item's full span as the end, so the returned range is "from
    /// the name to the end of the body" rather than "from the first
    /// attribute to the end".
    fn item_lines(
        &self,
        ident_span: proc_macro2::Span,
        body_span: proc_macro2::Span,
    ) -> (usize, usize) {
        let start = ident_span.start().line.max(1);
        let end = body_span.end().line.max(start);
        (start, end)
    }

    fn is_exported(vis: &syn::Visibility) -> bool {
        // syn::Visibility::Public matches `pub`. The other variants
        // (`pub(crate)`, `pub(super)`, ...) are visible inside the
        // crate but not from outside, so we treat them as unexported.
        // Matches the convention the Go extractor uses (only
        // `package`-exported names are Exported=true).
        matches!(vis, syn::Visibility::Public(_))
    }
}

impl<'ast> Visit<'ast> for Extractor<'_> {
    fn visit_item_fn(&mut self, node: &'ast syn::ItemFn) {
        let name = node.sig.ident.to_string();
        let (start, end) = self.item_lines(node.sig.ident.span(), node.span());
        let signature = render_fn_sig(&node.sig);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "function",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_struct(&mut self, node: &'ast syn::ItemStruct) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("struct {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "type",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_enum(&mut self, node: &'ast syn::ItemEnum) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("enum {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "type",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_trait(&mut self, node: &'ast syn::ItemTrait) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("trait {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "interface",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_type(&mut self, node: &'ast syn::ItemType) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("type {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "type",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_const(&mut self, node: &'ast syn::ItemConst) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("const {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "const",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_static(&mut self, node: &'ast syn::ItemStatic) {
        let name = node.ident.to_string();
        let (start, end) = self.item_lines(node.ident.span(), node.span());
        let signature = format!("static {}", name);
        self.out.push(Symbol {
            qualified_name: self.qname(&name),
            name,
            kind: "var",
            signature,
            start_line: start,
            end_line: end,
            exported: Self::is_exported(&node.vis),
        });
    }

    fn visit_item_impl(&mut self, node: &'ast syn::ItemImpl) {
        // Bughunt-3 rust F1: previously only handled syn::Type::Path
        // self_ty, silently dropping methods for `impl Display for
        // &Foo`, `impl X for (i32, i32)`, `impl X for [u8; N]`, etc.
        // Bughunt-3 rust F3: a multi-segment Path (impl Display for
        // std::collections::HashMap) collided with local-type
        // qnames because we only kept the last segment. Use the
        // full joined path for multi-segment self_ty so foreign-type
        // impl methods stay distinguishable.
        let prior = std::mem::take(&mut self.current_impl);
        if let Some(name) = impl_target_name(&node.self_ty) {
            self.current_impl = name;
        }
        for item in &node.items {
            if let syn::ImplItem::Fn(f) = item {
                self.visit_impl_item_fn(f);
            }
        }
        self.current_impl = prior;
    }

    fn visit_impl_item_fn(&mut self, node: &'ast syn::ImplItemFn) {
        if self.current_impl.is_empty() {
            return;
        }
        let name = node.sig.ident.to_string();
        let (start, end) = self.item_lines(node.sig.ident.span(), node.span());
        let signature = render_fn_sig(&node.sig);
        let exported = Self::is_exported(&node.vis);
        let qualified_name = self.method_qname(&name);
        self.out.push(Symbol {
            qualified_name,
            name,
            kind: "method",
            signature,
            start_line: start,
            end_line: end,
            exported,
        });
    }

    // We do NOT recurse into modules — top-level items in nested
    // `mod foo { ... }` blocks are out of v0 scope, matching the
    // shallow-index contract from Python and TS. Override the default
    // visit to no-op.
    fn visit_item_mod(&mut self, _node: &'ast syn::ItemMod) {}
}

/// impl_target_name renders the type an `impl` block is for, into
/// a name suitable for use as the impl_qname segment of method
/// qualified names.
///
/// Cases covered (bughunt-3 rust F1 + F3):
///   - Type::Path with one segment       → "Foo"
///   - Type::Path with multiple segments → "std::collections::HashMap"
///     (kept full so foreign-type methods don't collide with local
///     types that share a final-segment name)
///   - Type::Reference                  → "&Inner" (recursively)
///   - Type::Tuple / Array / Slice      → synthetic placeholder
///   - Anything else                    → "_anon_impl" sentinel
///
/// None is reserved for "couldn't render anything"; the caller
/// treats that as "skip this impl block's methods."
fn impl_target_name(ty: &syn::Type) -> Option<String> {
    match ty {
        syn::Type::Path(tp) => {
            if tp.path.segments.is_empty() {
                None
            } else if tp.path.segments.len() == 1 {
                tp.path.segments.last().map(|s| s.ident.to_string())
            } else {
                Some(
                    tp.path
                        .segments
                        .iter()
                        .map(|s| s.ident.to_string())
                        .collect::<Vec<_>>()
                        .join("::"),
                )
            }
        }
        syn::Type::Reference(tr) => impl_target_name(&tr.elem).map(|n| format!("&{}", n)),
        syn::Type::Tuple(_) => Some("_tuple".to_string()),
        syn::Type::Array(_) => Some("_array".to_string()),
        syn::Type::Slice(_) => Some("_slice".to_string()),
        _ => Some("_anon_impl".to_string()),
    }
}

/// Render a fn signature as a short string, mirroring the shape the
/// Python/TS extractors produce: `fn name(arg1, arg2)`. We
/// intentionally don't include types — keeps the signature compact
/// for terminal display and matches sibling extractors.
fn render_fn_sig(sig: &syn::Signature) -> String {
    let async_kw = if sig.asyncness.is_some() { "async " } else { "" };
    let name = sig.ident.to_string();
    let args: Vec<String> = sig
        .inputs
        .iter()
        .map(|arg| match arg {
            syn::FnArg::Receiver(_) => "self".to_string(),
            syn::FnArg::Typed(t) => match &*t.pat {
                syn::Pat::Ident(p) => p.ident.to_string(),
                _ => "_".to_string(),
            },
        })
        .collect();
    format!("{}fn {}({})", async_kw, name, args.join(", "))
}

fn run() -> ExitCode {
    let prefix: String = std::env::args().nth(1).unwrap_or_default();
    let mut src = String::new();
    if let Err(e) = io::stdin().read_to_string(&mut src) {
        let _ = writeln!(io::stderr(), "read stdin: {}", e);
        return ExitCode::from(1);
    }
    let file = match syn::parse_file(&src) {
        Ok(f) => f,
        Err(e) => {
            // Collapse syn's multi-line error to a single line so the
            // Go-side per-file ParseFailure summary stays terse.
            let line = e.span().start().line.max(1);
            let _ = writeln!(io::stderr(), "line {}: ParseError: {}", line, e);
            return ExitCode::from(2);
        }
    };
    let mut extractor = Extractor::new(&prefix);
    visit::visit_file(&mut extractor, &file);
    match serde_json::to_writer(io::stdout(), &extractor.out) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            let _ = writeln!(io::stderr(), "serialize: {}", e);
            ExitCode::from(1)
        }
    }
}

fn main() -> ExitCode {
    run()
}
