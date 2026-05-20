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
        // Top-level free function.
        let name = node.sig.ident.to_string();
        let (start, end) = self.span_lines(node.span());
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
        // Don't recurse into the body — Leonard's contract stays
        // shallow, mirroring the Python and TS extractors.
    }

    fn visit_item_struct(&mut self, node: &'ast syn::ItemStruct) {
        let name = node.ident.to_string();
        let (start, end) = self.span_lines(node.span());
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
        let (start, end) = self.span_lines(node.span());
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
        let (start, end) = self.span_lines(node.span());
        let signature = format!("trait {}", name);
        // Treat traits like interfaces in the cross-language symbol
        // vocabulary — closest existing kind.
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
        let (start, end) = self.span_lines(node.span());
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
        let (start, end) = self.span_lines(node.span());
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
        let (start, end) = self.span_lines(node.span());
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
        // Need the Type name being implemented for. `impl Foo` is
        // self_ty = Path; `impl Trait for Foo` is also self_ty =
        // Path (Foo). We extract the last segment of the self_ty
        // path, which is the bare type name.
        let prior = std::mem::take(&mut self.current_impl);
        if let syn::Type::Path(tp) = &*node.self_ty {
            if let Some(seg) = tp.path.segments.last() {
                self.current_impl = seg.ident.to_string();
            }
        }
        // Walk methods inside the block.
        for item in &node.items {
            if let syn::ImplItem::Fn(f) = item {
                self.visit_impl_item_fn(f);
            }
        }
        self.current_impl = prior;
    }

    fn visit_impl_item_fn(&mut self, node: &'ast syn::ImplItemFn) {
        if self.current_impl.is_empty() {
            // Defensive — visit_item_impl should always set this
            // before walking method items. Skip if somehow not.
            return;
        }
        let name = node.sig.ident.to_string();
        let (start, end) = self.span_lines(node.span());
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
