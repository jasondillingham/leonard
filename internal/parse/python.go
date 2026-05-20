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
		// Bare summary — the indexer wraps with the path, so don't double it.
		return nil, fmt.Errorf("%s", summarizePythonParseError(err))
	}
	module, ok := mod.(*ast.Module)
	if !ok {
		return nil, fmt.Errorf("parse %s: top-level node is %T, want *ast.Module", path, mod)
	}

	prefix := moduleQualifier(path)
	var syms []store.Symbol
	for _, stmt := range module.Body {
		syms = append(syms, pythonTopLevelStmt(path, prefix, stmt)...)
	}
	return syms, nil
}

// summarizePythonParseError flattens gpython's multi-line *py.Exception
// rendering into a single useful line. The raw form embeds the file path
// (redundant — the caller already has it), a code excerpt, several blank
// lines, and the actual error type/message. Callers usually want
// "<line>: <message>" so CLI summaries fit on one screen line.
func summarizePythonParseError(err error) string {
	raw := strings.TrimSpace(err.Error())
	var locLine, errLine string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "File ") && locLine == "" {
			locLine = line
		}
		// Python error types end in "Error" and look like
		// "SyntaxError: 'invalid syntax'" or "IndentationError: ...".
		if errLine == "" && strings.Contains(line, "Error:") {
			errLine = line
		}
	}
	switch {
	case locLine != "" && errLine != "":
		// "File ..., line 121, offset 36" → "line 121"
		loc := locLine
		if i := strings.Index(loc, "line "); i >= 0 {
			loc = loc[i:]
			if j := strings.Index(loc, ","); j >= 0 {
				loc = loc[:j]
			}
		}
		return loc + ": " + errLine
	case errLine != "":
		return errLine
	case locLine != "":
		return locLine
	default:
		return raw
	}
}

// pythonTopLevelStmt extracts symbols from a module-level statement. The brief
// limits v0 to def/class/assign; anything else (imports, ifs, exprs) is ignored.
// prefix is the dotted module qualifier from moduleQualifier(path) — empty for
// callers that didn't compute one, in which case qualified_names stay bare.
func pythonTopLevelStmt(path, prefix string, stmt ast.Stmt) []store.Symbol {
	switch s := stmt.(type) {
	case *ast.FunctionDef:
		return []store.Symbol{pythonFunctionSymbol(path, prefix, "", s)}
	case *ast.ClassDef:
		return pythonClassSymbols(path, prefix, s)
	case *ast.Assign:
		return pythonAssignSymbols(path, prefix, s)
	}
	return nil
}

// pythonFunctionSymbol builds a function (or method, when classQname is set)
// symbol from a FunctionDef. Decorators are intentionally ignored for v0 — the
// brief says to extract the wrapped name as if the decorator were absent.
//
// classQname, when non-empty, already includes the module prefix (it was
// built by pythonClassSymbols), so we don't add prefix again on methods.
func pythonFunctionSymbol(path, prefix, classQname string, fn *ast.FunctionDef) store.Symbol {
	name := string(fn.Name)
	kind := "function"
	qname := joinQName(prefix, name)
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
func pythonClassSymbols(path, prefix string, cls *ast.ClassDef) []store.Symbol {
	name := string(cls.Name)
	classQname := joinQName(prefix, name)
	out := []store.Symbol{{
		FilePath:      path,
		Name:          name,
		QualifiedName: classQname,
		Kind:          "type",
		Signature:     pythonClassSignature(cls),
		StartLine:     cls.GetLineno(),
		EndLine:       deepestLineno(cls, cls.GetLineno()),
		Exported:      isPythonExported(name),
	}}
	for _, sub := range cls.Body {
		if fn, ok := sub.(*ast.FunctionDef); ok {
			out = append(out, pythonFunctionSymbol(path, prefix, classQname, fn))
		}
	}
	return out
}

// pythonAssignSymbols extracts one var symbol per Name target. Non-Name targets
// (attribute writes like `x.y = …`, subscripts, tuple unpacking) are skipped.
func pythonAssignSymbols(path, prefix string, a *ast.Assign) []store.Symbol {
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
			QualifiedName: joinQName(prefix, name),
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
