package parse

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"unicode"

	"github.com/jasondillingham/leonard/internal/store"
)

// ExtractGo extracts top-level Go symbols (funcs, methods, types, const, var)
// from src. The returned symbols have FilePath set to path; ID and ParentID
// are left zero/nil — the store assigns them on insert.
//
// Phase 1 uses the standard library go/parser instead of a tree-sitter
// binding. See DESIGN.md §7 question #2 for the decision rationale.
func ExtractGo(path string, src []byte) ([]store.Symbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	pkg := file.Name.Name
	var syms []store.Symbol

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms = append(syms, funcSymbol(fset, path, pkg, d))
		case *ast.GenDecl:
			syms = append(syms, genDeclSymbols(fset, path, pkg, d)...)
		}
	}

	return syms, nil
}

func funcSymbol(fset *token.FileSet, path, pkg string, fn *ast.FuncDecl) store.Symbol {
	name := fn.Name.Name
	kind := "function"
	qname := pkg + "." + name

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = "method"
		recv := receiverTypeName(fn.Recv.List[0].Type)
		if recv != "" {
			qname = pkg + "." + recv + "." + name
		}
	}

	start := fset.Position(fn.Pos())
	end := fset.Position(fn.End())

	return store.Symbol{
		FilePath:      path,
		Name:          name,
		QualifiedName: qname,
		Kind:          kind,
		Signature:     funcSignature(fn),
		StartLine:     start.Line,
		EndLine:       end.Line,
		Exported:      isExported(name),
	}
}

func genDeclSymbols(fset *token.FileSet, path, pkg string, gd *ast.GenDecl) []store.Symbol {
	var out []store.Symbol

	switch gd.Tok {
	case token.TYPE:
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			out = append(out, typeSymbol(fset, path, pkg, ts))
		}
	case token.CONST, token.VAR:
		kind := "const"
		if gd.Tok == token.VAR {
			kind = "var"
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == "_" {
					continue
				}
				start := fset.Position(name.Pos())
				end := fset.Position(name.End())
				out = append(out, store.Symbol{
					FilePath:      path,
					Name:          name.Name,
					QualifiedName: pkg + "." + name.Name,
					Kind:          kind,
					Signature:     valueSignature(kind, name.Name, vs),
					StartLine:     start.Line,
					EndLine:       end.Line,
					Exported:      isExported(name.Name),
				})
			}
		}
	}

	return out
}

func typeSymbol(fset *token.FileSet, path, pkg string, ts *ast.TypeSpec) store.Symbol {
	name := ts.Name.Name
	kind := classifyType(ts)

	start := fset.Position(ts.Pos())
	end := fset.Position(ts.End())

	return store.Symbol{
		FilePath:      path,
		Name:          name,
		QualifiedName: pkg + "." + name,
		Kind:          kind,
		Signature:     typeSignature(ts),
		StartLine:     start.Line,
		EndLine:       end.Line,
		Exported:      isExported(name),
	}
}

func classifyType(ts *ast.TypeSpec) string {
	if _, ok := ts.Type.(*ast.InterfaceType); ok {
		return "interface"
	}
	return "type"
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	}
	return ""
}

func funcSignature(fn *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("func ")
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		b.WriteString("(")
		b.WriteString(renderExpr(fn.Recv.List[0].Type))
		b.WriteString(") ")
	}
	b.WriteString(fn.Name.Name)
	if fn.Type.TypeParams != nil {
		b.WriteString(renderFieldList(fn.Type.TypeParams, "[", "]"))
	}
	b.WriteString(renderFieldList(fn.Type.Params, "(", ")"))
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		b.WriteString(" ")
		needParens := len(fn.Type.Results.List) > 1 || (len(fn.Type.Results.List) == 1 && len(fn.Type.Results.List[0].Names) > 0)
		if needParens {
			b.WriteString(renderFieldList(fn.Type.Results, "(", ")"))
		} else {
			b.WriteString(renderExpr(fn.Type.Results.List[0].Type))
		}
	}
	return b.String()
}

func typeSignature(ts *ast.TypeSpec) string {
	var b strings.Builder
	b.WriteString("type ")
	b.WriteString(ts.Name.Name)
	if ts.TypeParams != nil {
		b.WriteString(renderFieldList(ts.TypeParams, "[", "]"))
	}
	if ts.Assign.IsValid() {
		b.WriteString(" = ")
	} else {
		b.WriteString(" ")
	}
	b.WriteString(renderExpr(ts.Type))
	return b.String()
}

func valueSignature(kind, name string, vs *ast.ValueSpec) string {
	var b strings.Builder
	b.WriteString(kind)
	b.WriteString(" ")
	b.WriteString(name)
	if vs.Type != nil {
		b.WriteString(" ")
		b.WriteString(renderExpr(vs.Type))
	}
	return b.String()
}

func renderFieldList(fl *ast.FieldList, open, close string) string {
	var parts []string
	for _, f := range fl.List {
		typ := renderExpr(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
			continue
		}
		names := make([]string, 0, len(f.Names))
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return open + strings.Join(parts, ", ") + close
}

func renderExpr(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch expr.(type) {
	case *ast.StructType:
		return "struct{...}"
	case *ast.InterfaceType:
		return "interface{...}"
	}
	var b strings.Builder
	if err := printer.Fprint(&b, token.NewFileSet(), expr); err != nil {
		return ""
	}
	return collapseWhitespace(b.String())
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	r := []rune(name)[0]
	return unicode.IsUpper(r)
}
