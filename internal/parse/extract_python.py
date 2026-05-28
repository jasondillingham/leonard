"""Symbol extractor invoked by internal/parse/python.go via subprocess.

Reads Python source from stdin, optionally takes a qualified-name prefix
as argv[1], and prints a JSON array of symbol records to stdout. On
SyntaxError, writes a "line N: SyntaxError: msg" line to stderr and
exits 2 — the Go side surfaces that as a per-file ParseFailure exactly
like the gpython predecessor did.

The contract is narrow on purpose: top-level def / async def / class
(plus methods one level deep) / Assign / AnnAssign. Function and class
bodies are NOT walked for nested decls — Leonard's v0 brief keeps the
index shallow.

Requires Python 3.9+ for ast.unparse (used for class-bases signatures).
Older host Pythons degrade to "class Name" without bases.
"""

import ast
import json
import sys


def is_exported(name: str) -> bool:
    """Mirrors the gpython-backed predecessor: bare-underscore and
    leading-underscore names are private; dunders (``__init__`` etc.)
    are conventionally not part of a module's public surface either."""
    if not name or name == "_":
        return False
    if name.startswith("__") and name.endswith("__") and len(name) > 4:
        return False
    return not name.startswith("_")


def render_bases(node: ast.ClassDef) -> str:
    if not node.bases:
        return ""
    if not hasattr(ast, "unparse"):
        return ""
    try:
        return ", ".join(ast.unparse(b) for b in node.bases)
    except Exception:
        return ""


def func_signature(node, prefix: str = "def") -> str:
    args = []
    a = node.args
    posonly = list(getattr(a, "posonlyargs", []) or [])
    if posonly:
        args.extend(arg.arg for arg in posonly)
        args.append("/")
    args.extend(arg.arg for arg in a.args)
    if a.vararg is not None:
        args.append("*" + a.vararg.arg)
    elif a.kwonlyargs:
        args.append("*")
    args.extend(arg.arg for arg in a.kwonlyargs)
    if a.kwarg is not None:
        args.append("**" + a.kwarg.arg)
    return f"{prefix} {node.name}({', '.join(args)})"


def class_signature(node: ast.ClassDef) -> str:
    bases = render_bases(node)
    if bases:
        return f"class {node.name}({bases})"
    return f"class {node.name}"


def deepest_lineno(node: ast.AST, start: int) -> int:
    """End-line approximation. ast.AST exposes end_lineno on 3.8+ for
    most statement nodes — we prefer it; otherwise walk every descendant
    and take the maximum reported line."""
    end = getattr(node, "end_lineno", None)
    if isinstance(end, int) and end >= start:
        return end
    end = start
    for child in ast.walk(node):
        ln = getattr(child, "lineno", None)
        if isinstance(ln, int) and ln > end:
            end = ln
    return end


def qname(prefix: str, name: str) -> str:
    return f"{prefix}.{name}" if prefix else name


def function_record(node, prefix: str, class_qname: str = "") -> dict:
    name = node.name
    if class_qname:
        kind = "method"
        full = f"{class_qname}.{name}"
    else:
        kind = "function"
        full = qname(prefix, name)
    return {
        "name": name,
        "qualified_name": full,
        "kind": kind,
        "signature": func_signature(node),
        "start_line": node.lineno,
        "end_line": deepest_lineno(node, node.lineno),
        "exported": is_exported(name),
    }


def class_records(node: ast.ClassDef, prefix: str) -> list:
    cls_qname = qname(prefix, node.name)
    out = [{
        "name": node.name,
        "qualified_name": cls_qname,
        "kind": "type",
        "signature": class_signature(node),
        "start_line": node.lineno,
        "end_line": deepest_lineno(node, node.lineno),
        "exported": is_exported(node.name),
    }]
    for sub in node.body:
        if isinstance(sub, (ast.FunctionDef, ast.AsyncFunctionDef)):
            out.append(function_record(sub, prefix, class_qname=cls_qname))
    return out


def assign_records(node: ast.Assign, prefix: str) -> list:
    out = []
    end = deepest_lineno(node, node.lineno)
    for target in node.targets:
        if isinstance(target, ast.Name) and target.id != "_":
            out.append({
                "name": target.id,
                "qualified_name": qname(prefix, target.id),
                "kind": "var",
                "signature": f"var {target.id}",
                "start_line": node.lineno,
                "end_line": end,
                "exported": is_exported(target.id),
            })
    return out


def ann_assign_record(node: ast.AnnAssign, prefix: str):
    """PEP 526 annotated assignment: ``x: int = 1`` or ``x: int``.
    Only Name targets are emitted — attribute/subscript writes are
    skipped, matching the Assign behavior."""
    if not isinstance(node.target, ast.Name) or node.target.id == "_":
        return None
    return {
        "name": node.target.id,
        "qualified_name": qname(prefix, node.target.id),
        "kind": "var",
        "signature": f"var {node.target.id}",
        "start_line": node.lineno,
        "end_line": deepest_lineno(node, node.lineno),
        "exported": is_exported(node.target.id),
    }


def extract(src: str, prefix: str) -> list:
    tree = ast.parse(src)
    out = []
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            out.append(function_record(node, prefix))
        elif isinstance(node, ast.ClassDef):
            out.extend(class_records(node, prefix))
        elif isinstance(node, ast.Assign):
            out.extend(assign_records(node, prefix))
        elif isinstance(node, ast.AnnAssign):
            rec = ann_assign_record(node, prefix)
            if rec is not None:
                out.append(rec)
    return out


def main() -> int:
    prefix = sys.argv[1] if len(sys.argv) > 1 else ""
    # Read raw bytes and decode with utf-8-sig so a UTF-8 BOM at the
    # start of the file is stripped before ast.parse() sees it.
    # sys.stdin.read() leaves the BOM as U+FEFF, which ast.parse()
    # rejects even though CPython executes the file cleanly.
    src = sys.stdin.buffer.read().decode("utf-8-sig")
    try:
        syms = extract(src, prefix)
    except SyntaxError as e:
        line = e.lineno or 0
        msg = e.msg or "syntax error"
        sys.stderr.write(f"line {line}: SyntaxError: {msg}\n")
        return 2
    json.dump(syms, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
