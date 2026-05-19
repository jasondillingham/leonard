package parse

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/go-python/gpython/ast"
	"github.com/go-python/gpython/parser"
	"github.com/go-python/gpython/py"

	"github.com/jasondillingham/leonard/internal/store"
)

// ExtractPython extracts module-level Python symbols (functions, classes,
// methods, vars) from src. The returned symbols have FilePath set to path; ID
// and ParentID are left zero/nil — the store assigns them on insert.
//
// Phase 2 uses go-python/gpython's parser (pure Go) instead of a tree-sitter
// binding. See DESIGN.md §7 question #2 for the decision rationale.
func ExtractPython(path string, src []byte) ([]store.Symbol, error) {
	mod, err := parser.Parse(bytes.NewReader(src), path, py.ExecMode)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	module, ok := mod.(*ast.Module)
	if !ok {
		return nil, fmt.Errorf("parse %s: top-level node is %T, want *ast.Module", path, mod)
	}

	var syms []store.Symbol
	for _, stmt := range module.Body {
		syms = append(syms, pythonTopLevelStmt(path, stmt)...)
	}
	return syms, nil
}

// pythonTopLevelStmt extracts symbols from a module-level statement. The brief
// limits v0 to def/class/assign; anything else (imports, ifs, exprs) is ignored.
func pythonTopLevelStmt(path string, stmt ast.Stmt) []store.Symbol {
	switch s := stmt.(type) {
	case *ast.FunctionDef:
		return []store.Symbol{pythonFunctionSymbol(path, "", s)}
	case *ast.ClassDef:
		return pythonClassSymbols(path, s)
	case *ast.Assign:
		return pythonAssignSymbols(path, s)
	}
	return nil
}

// pythonFunctionSymbol builds a function (or method, when classQname is set)
// symbol from a FunctionDef. Decorators are intentionally ignored for v0 — the
// brief says to extract the wrapped name as if the decorator were absent.
func pythonFunctionSymbol(path, classQname string, fn *ast.FunctionDef) store.Symbol {
	name := string(fn.Name)
	kind := "function"
	qname := name
	if classQname != "" {
		kind = "method"
		qname = classQname + "." + name
	}
	return store.Symbol{
		FilePath:      path,
		Name:          name,
		QualifiedName: qname,
		Kind:          kind,
		Signature:     pythonFuncSignature(name, fn),
		StartLine:     fn.GetLineno(),
		EndLine:       deepestLineno(fn, fn.GetLineno()),
		Exported:      isPythonExported(name),
	}
}

// pythonClassSymbols returns the class itself plus one entry per method defined
// directly inside its body. Nested classes and class-body assignments are
// skipped — v0 only walks one level deep.
func pythonClassSymbols(path string, cls *ast.ClassDef) []store.Symbol {
	name := string(cls.Name)
	out := []store.Symbol{{
		FilePath:      path,
		Name:          name,
		QualifiedName: name,
		Kind:          "type",
		Signature:     pythonClassSignature(cls),
		StartLine:     cls.GetLineno(),
		EndLine:       deepestLineno(cls, cls.GetLineno()),
		Exported:      isPythonExported(name),
	}}
	for _, sub := range cls.Body {
		if fn, ok := sub.(*ast.FunctionDef); ok {
			out = append(out, pythonFunctionSymbol(path, name, fn))
		}
	}
	return out
}

// pythonAssignSymbols extracts one var symbol per Name target. Non-Name targets
// (attribute writes like `x.y = …`, subscripts, tuple unpacking) are skipped.
func pythonAssignSymbols(path string, a *ast.Assign) []store.Symbol {
	end := deepestLineno(a, a.GetLineno())
	var out []store.Symbol
	for _, target := range a.Targets {
		n, ok := target.(*ast.Name)
		if !ok {
			continue
		}
		name := string(n.Id)
		if name == "_" {
			continue
		}
		out = append(out, store.Symbol{
			FilePath:      path,
			Name:          name,
			QualifiedName: name,
			Kind:          "var",
			Signature:     "var " + name,
			StartLine:     a.GetLineno(),
			EndLine:       end,
			Exported:      isPythonExported(name),
		})
	}
	return out
}

func pythonFuncSignature(name string, fn *ast.FunctionDef) string {
	return "def " + name + pythonArgsSignature(fn.Args)
}

// pythonArgsSignature renders an Arguments node as a Python-like parameter
// list. Defaults and annotations are omitted to keep signatures short; v0
// signatures are intended for surface-level disambiguation, not exact recall.
func pythonArgsSignature(args *ast.Arguments) string {
	if args == nil {
		return "()"
	}
	var parts []string
	for _, a := range args.Args {
		parts = append(parts, string(a.Arg))
	}
	if args.Vararg != nil {
		parts = append(parts, "*"+string(args.Vararg.Arg))
	}
	for _, a := range args.Kwonlyargs {
		parts = append(parts, string(a.Arg))
	}
	if args.Kwarg != nil {
		parts = append(parts, "**"+string(args.Kwarg.Arg))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func pythonClassSignature(cls *ast.ClassDef) string {
	if len(cls.Bases) == 0 {
		return "class " + string(cls.Name)
	}
	bases := make([]string, 0, len(cls.Bases))
	for _, b := range cls.Bases {
		bases = append(bases, renderPythonExpr(b))
	}
	return "class " + string(cls.Name) + "(" + strings.Join(bases, ", ") + ")"
}

// renderPythonExpr collapses a Python expression to a short identifier form
// for signatures. Anything more complex than a Name or dotted Attribute
// degrades to "...", which is fine for v0 — signatures are not a contract.
func renderPythonExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Name:
		return string(x.Id)
	case *ast.Attribute:
		return renderPythonExpr(x.Value) + "." + string(x.Attr)
	}
	return "..."
}

// deepestLineno returns the maximum Lineno reachable from node, falling back
// to start when the walk finds nothing larger. Used to approximate end lines:
// gpython's AST has no end positions, so the line of the deepest descendant is
// the best cheap proxy.
func deepestLineno(node ast.Ast, start int) int {
	max := start
	ast.Walk(node, func(n ast.Ast) bool {
		if p, ok := n.(interface{ GetLineno() int }); ok {
			if ln := p.GetLineno(); ln > max {
				max = ln
			}
		}
		return true
	})
	return max
}

// isPythonExported follows Python's leading-underscore convention: any name
// not starting with `_` is considered exported. Matches the brief's rule.
func isPythonExported(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return r != '_'
}
