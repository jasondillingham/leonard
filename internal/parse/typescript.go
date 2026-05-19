package parse

import (
	"fmt"
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

// ExtractTypeScript extracts top-level TypeScript symbols (functions, classes,
// methods, interfaces, type aliases, const/let/var) from src. The returned
// symbols have FilePath set to path; ID and ParentID are left zero/nil — the
// store assigns them on insert.
//
// Phase 3 uses a hand-rolled scanner instead of a tree-sitter binding. See
// DESIGN.md §7 question #2 for the decision rationale. Out of v0 scope: enums,
// namespaces, decorators, default exports, JSX element extraction, generic
// type parameters (the simple name is recorded, generics are stripped).
func ExtractTypeScript(path string, src []byte) ([]store.Symbol, error) {
	stripped, err := stripTSLiterals(src)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	p := &tsParser{path: path, tokens: tokenizeTS(stripped)}
	p.parseFile()
	return p.syms, nil
}

// stripTSLiterals returns a byte slice the same length as src in which string
// literals (single, double, template), regex literals, comments (line and
// block), and the interior of template-literal expressions are replaced with
// spaces. Newlines are preserved so token line numbers continue to match src.
//
// Regex literals are disambiguated from division by tracking the class of the
// previous significant token: after a value-yielding token (ident, number,
// `)`, `]`, string/template/regex literal), a `/` is division; after an
// operator, punctuator, expression-starting keyword, or start-of-file, a `/`
// starts a regex.
func stripTSLiterals(src []byte) ([]byte, error) {
	out := make([]byte, len(src))
	i := 0
	regexPossible := true
	for i < len(src) {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				out[i] = ' '
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					out[i] = '\n'
				} else {
					out[i] = ' '
				}
				i++
			}
			if i+1 < len(src) {
				out[i] = ' '
				out[i+1] = ' '
				i += 2
			} else if i < len(src) {
				out[i] = ' '
				i++
			}
		case c == '/' && regexPossible:
			i = stripRegex(src, out, i)
			regexPossible = false
		case c == '"' || c == '\'':
			i = stripQuoted(src, out, i, c)
			regexPossible = false
		case c == '`':
			out[i] = ' '
			i = stripTSTemplate(src, out, i+1)
			regexPossible = false
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			out[i] = c
			i++
		case isTSIdentStart(c):
			start := i
			out[i] = c
			i++
			for i < len(src) && isTSIdentCont(src[i]) {
				out[i] = src[i]
				i++
			}
			regexPossible = isExprStartKeyword(string(src[start:i]))
		case c >= '0' && c <= '9':
			out[i] = c
			i++
			for i < len(src) && (isTSIdentCont(src[i]) || src[i] == '.') {
				out[i] = src[i]
				i++
			}
			regexPossible = false
		default:
			out[i] = c
			i++
			// `)` and `]` end a value (next `/` is division); all other
			// punctuators (`=`, `,`, `(`, `[`, `{`, `}`, `;`, `:`, `?`, `!`,
			// `&`, `|`, `^`, `~`, `+`, `-`, `*`, `%`, `<`, `>`, `.`, `@`,
			// `#`) leave us in an expression-start position.
			regexPossible = c != ')' && c != ']'
		}
	}
	return out, nil
}

// isExprStartKeyword reports whether the given identifier, when it appears
// as a keyword, is followed by an expression — so a `/` after it starts a
// regex literal rather than a division operator. Non-keyword identifiers
// and value-keywords (`this`, `true`, etc.) are values, so a `/` after them
// is division.
func isExprStartKeyword(ident string) bool {
	switch ident {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete",
		"void", "throw", "yield", "await", "case", "do", "else", "if",
		"while", "for", "switch", "default":
		return true
	}
	return false
}

// stripRegex blanks the body of a regex literal starting at i (the opening
// `/`). Character classes `[...]` are tracked so a `/` inside them doesn't
// terminate the regex. Trailing flag characters (e.g. `gimsuy`) are also
// blanked. Returns the index just past the last flag (or the end of the
// line on an unterminated regex).
func stripRegex(src, out []byte, i int) int {
	out[i] = ' '
	i++
	inClass := false
	for i < len(src) {
		c := src[i]
		if c == '\\' && i+1 < len(src) {
			out[i] = ' '
			if src[i+1] == '\n' {
				out[i+1] = '\n'
			} else {
				out[i+1] = ' '
			}
			i += 2
			continue
		}
		if c == '\n' {
			// Unterminated regex; bail so line counters stay aligned.
			out[i] = '\n'
			return i + 1
		}
		if c == '[' && !inClass {
			inClass = true
			out[i] = ' '
			i++
			continue
		}
		if c == ']' && inClass {
			inClass = false
			out[i] = ' '
			i++
			continue
		}
		if c == '/' && !inClass {
			out[i] = ' '
			i++
			for i < len(src) && isTSIdentCont(src[i]) {
				out[i] = ' '
				i++
			}
			return i
		}
		out[i] = ' '
		i++
	}
	return i
}

// stripQuoted blanks the body of a "..." or '...' string starting at i (the
// opening quote). Returns the index just after the closing quote (or the end
// of the line on unterminated strings).
func stripQuoted(src, out []byte, i int, quote byte) int {
	out[i] = ' '
	i++
	for i < len(src) {
		c := src[i]
		if c == '\\' && i+1 < len(src) {
			out[i] = ' '
			if src[i+1] == '\n' {
				out[i+1] = '\n'
			} else {
				out[i+1] = ' '
			}
			i += 2
			continue
		}
		if c == quote {
			out[i] = ' '
			return i + 1
		}
		if c == '\n' {
			// Unterminated string; bail so line counters stay aligned.
			out[i] = '\n'
			return i + 1
		}
		out[i] = ' '
		i++
	}
	return i
}

// stripTSTemplate blanks the body of a template literal. i points just after
// the opening backtick. `${...}` expression contents are also blanked (with
// nesting handled for inner templates, strings, and braces). Returns the
// index just after the closing backtick.
func stripTSTemplate(src, out []byte, i int) int {
	for i < len(src) {
		c := src[i]
		if c == '`' {
			out[i] = ' '
			return i + 1
		}
		if c == '\\' && i+1 < len(src) {
			out[i] = ' '
			if src[i+1] == '\n' {
				out[i+1] = '\n'
			} else {
				out[i+1] = ' '
			}
			i += 2
			continue
		}
		if c == '$' && i+1 < len(src) && src[i+1] == '{' {
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			depth := 1
			for i < len(src) && depth > 0 {
				c2 := src[i]
				switch {
				case c2 == '\n':
					out[i] = '\n'
					i++
				case c2 == '{':
					depth++
					out[i] = ' '
					i++
				case c2 == '}':
					depth--
					out[i] = ' '
					i++
				case c2 == '`':
					out[i] = ' '
					i = stripTSTemplate(src, out, i+1)
				case c2 == '"' || c2 == '\'':
					i = stripQuoted(src, out, i, c2)
				case c2 == '/' && i+1 < len(src) && src[i+1] == '/':
					for i < len(src) && src[i] != '\n' {
						out[i] = ' '
						i++
					}
				case c2 == '/' && i+1 < len(src) && src[i+1] == '*':
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
						if src[i] == '\n' {
							out[i] = '\n'
						} else {
							out[i] = ' '
						}
						i++
					}
					if i+1 < len(src) {
						out[i] = ' '
						out[i+1] = ' '
						i += 2
					} else if i < len(src) {
						out[i] = ' '
						i++
					}
				default:
					out[i] = ' '
					i++
				}
			}
			continue
		}
		if c == '\n' {
			out[i] = '\n'
		} else {
			out[i] = ' '
		}
		i++
	}
	return i
}

// tsToken is a lexer token from the stripped source.
type tsToken struct {
	kind string // "ident", "kw", "punct", "num"
	val  string
	line int
}

// tsKeywords lists the TS keywords this scanner recognizes. Anything else,
// including the names of standard types like "string" or "number", is
// returned as "ident". The list is intentionally narrow — only what the
// state machine needs to dispatch on or skip past.
var tsKeywords = map[string]bool{
	"export":   true,
	"default":  true,
	"declare":  true,
	"async":    true,
	"abstract": true,
	"override": true,

	"function":  true,
	"class":     true,
	"interface": true,
	"type":      true,

	"const": true,
	"let":   true,
	"var":   true,

	"public":    true,
	"private":   true,
	"protected": true,
	"static":    true,
	"readonly":  true,
	"get":       true,
	"set":       true,

	"extends":    true,
	"implements": true,
	"import":     true,
	"from":       true,

	"enum":      true,
	"namespace": true,
	"module":    true,
}

func isTSIdentStart(b byte) bool {
	return b == '_' || b == '$' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isTSIdentCont(b byte) bool {
	return isTSIdentStart(b) || (b >= '0' && b <= '9')
}

// tokenizeTS lexes the stripped source. The lexer is whitespace-insensitive
// except for newlines (which advance the line counter). It recognizes
// identifiers/keywords, numeric literals, the multi-char punctuators `...`
// and `=>`, and emits every other character as a single-char punct token.
func tokenizeTS(src []byte) []tsToken {
	var out []tsToken
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if isTSIdentStart(c) {
			start := i
			i++
			for i < len(src) && isTSIdentCont(src[i]) {
				i++
			}
			v := string(src[start:i])
			kind := "ident"
			if tsKeywords[v] {
				kind = "kw"
			}
			out = append(out, tsToken{kind: kind, val: v, line: line})
			continue
		}
		if c >= '0' && c <= '9' {
			start := i
			for i < len(src) {
				cc := src[i]
				if cc == '\n' || cc == ' ' || cc == '\t' || cc == '\r' {
					break
				}
				if isTSIdentCont(cc) || cc == '.' {
					i++
					continue
				}
				break
			}
			out = append(out, tsToken{kind: "num", val: string(src[start:i]), line: line})
			continue
		}
		if c == '.' && i+2 < len(src) && src[i+1] == '.' && src[i+2] == '.' {
			out = append(out, tsToken{kind: "punct", val: "...", line: line})
			i += 3
			continue
		}
		if c == '=' && i+1 < len(src) && src[i+1] == '>' {
			out = append(out, tsToken{kind: "punct", val: "=>", line: line})
			i += 2
			continue
		}
		out = append(out, tsToken{kind: "punct", val: string(c), line: line})
		i++
	}
	return out
}

// tsParser walks the token stream and emits symbols. Top-level recognition is
// done in tryTopLevel; class bodies recurse into parseClassMember. Anything
// not matched is skipped a token at a time so unsupported constructs don't
// stall the parser.
type tsParser struct {
	path   string
	tokens []tsToken
	pos    int
	syms   []store.Symbol
}

func (p *tsParser) peek() tsToken {
	if p.pos >= len(p.tokens) {
		return tsToken{}
	}
	return p.tokens[p.pos]
}

func (p *tsParser) parseFile() {
	for p.pos < len(p.tokens) {
		before := p.pos
		if !p.tryTopLevel() {
			if p.pos == before {
				p.pos++
			}
		}
	}
}

// tryTopLevel consumes the leading modifier sequence (`export`, `declare`,
// `async`, `abstract`) and dispatches on the next keyword. On any failure
// path it must restore p.pos to its starting value so parseFile can advance
// the cursor by one.
func (p *tsParser) tryTopLevel() bool {
	saved := p.pos
	startLine := p.peek().line
	exported := false
modifiers:
	for p.pos < len(p.tokens) {
		mt := p.peek()
		if mt.kind != "kw" {
			break
		}
		switch mt.val {
		case "export":
			exported = true
			p.pos++
		case "declare", "async", "abstract":
			p.pos++
		case "default":
			// `export default ...` is intentionally out of scope for v0.
			p.pos = saved
			return false
		default:
			break modifiers
		}
	}
	t := p.peek()
	if t.kind != "kw" {
		p.pos = saved
		return false
	}
	switch t.val {
	case "function":
		if p.parseFunction(exported, startLine) {
			return true
		}
	case "class":
		if p.parseClass(exported, startLine) {
			return true
		}
	case "interface":
		if p.parseInterface(exported, startLine) {
			return true
		}
	case "type":
		if p.parseTypeAlias(exported, startLine) {
			return true
		}
	case "const", "let", "var":
		if p.parseVarDecl(t.val, exported, startLine) {
			return true
		}
	case "enum", "namespace", "module":
		// Out of v0 scope; skip the whole declaration so subsequent code
		// remains parseable.
		p.skipDecl()
		return true
	case "import":
		p.skipDecl()
		return true
	}
	p.pos = saved
	return false
}

func (p *tsParser) parseFunction(exported bool, startLine int) bool {
	saved := p.pos
	if p.peek().val != "function" {
		p.pos = saved
		return false
	}
	p.pos++
	if p.peek().val == "*" {
		p.pos++ // generator
	}
	nameTok := p.peek()
	if nameTok.kind != "ident" {
		p.pos = saved
		return false
	}
	p.pos++
	p.skipGenericParams()
	if p.peek().val != "(" {
		p.pos = saved
		return false
	}
	params := p.captureBalanced("(", ")")
	if params == nil {
		p.pos = saved
		return false
	}
	p.skipReturnType()
	endLine := startLine
	if p.peek().val == "{" {
		body := p.captureBalanced("{", "}")
		if body == nil {
			p.pos = saved
			return false
		}
		if p.pos > 0 {
			endLine = p.tokens[p.pos-1].line
		} else if len(body) > 0 {
			endLine = body[len(body)-1].line
		}
	} else if p.peek().val == ";" {
		endLine = p.peek().line
		p.pos++
	}
	p.syms = append(p.syms, store.Symbol{
		FilePath:      p.path,
		Name:          nameTok.val,
		QualifiedName: nameTok.val,
		Kind:          "function",
		Signature:     "function " + nameTok.val + "(" + extractParamNames(params) + ")",
		StartLine:     startLine,
		EndLine:       endLine,
		Exported:      exported,
	})
	return true
}

func (p *tsParser) parseClass(exported bool, startLine int) bool {
	saved := p.pos
	if p.peek().val != "class" {
		p.pos = saved
		return false
	}
	p.pos++
	nameTok := p.peek()
	if nameTok.kind != "ident" {
		p.pos = saved
		return false
	}
	p.pos++
	p.skipGenericParams()
	// Skip extends/implements clauses until the body opens.
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if v == "{" {
			break
		}
		if v == ";" || v == "}" || v == "(" {
			p.pos = saved
			return false
		}
		p.pos++
	}
	if p.pos >= len(p.tokens) {
		p.pos = saved
		return false
	}
	p.pos++ // consume '{'
	endLine := startLine
	for p.pos < len(p.tokens) {
		t := p.peek()
		if t.val == "}" {
			endLine = t.line
			p.pos++
			break
		}
		before := p.pos
		if !p.parseClassMember(nameTok.val, exported) {
			if p.pos == before {
				p.pos++
			}
		}
	}
	p.syms = append(p.syms, store.Symbol{
		FilePath:      p.path,
		Name:          nameTok.val,
		QualifiedName: nameTok.val,
		Kind:          "type",
		Signature:     "class " + nameTok.val,
		StartLine:     startLine,
		EndLine:       endLine,
		Exported:      exported,
	})
	return true
}

// parseClassMember attempts to consume one class body member. Methods
// (constructor or regular) emit a symbol; fields are silently skipped.
// Methods inherit Exported from the parent class — the brief restricts
// `Exported = true` to the `export` modifier and that modifier cannot
// appear on a class member, so propagating the parent's flag is the most
// useful interpretation of reachability from outside the module.
func (p *tsParser) parseClassMember(className string, parentExported bool) bool {
	saved := p.pos
	startLine := p.peek().line
	// Modifiers.
	sawStatic := false
	for p.pos < len(p.tokens) {
		t := p.peek()
		if t.kind != "kw" {
			break
		}
		switch t.val {
		case "static":
			sawStatic = true
			p.pos++
			continue
		case "public", "private", "protected", "readonly",
			"async", "abstract", "override", "get", "set", "declare":
			p.pos++
			continue
		}
		break
	}
	// `static { ... }` initializer block (TS 4.4+). The body is balanced
	// internally; the class continues after it. No symbol is emitted.
	if sawStatic && p.peek().val == "{" {
		p.skipBalanced()
		return true
	}
	// Decorators on class members (`@log foo() { ... }`). Consume the
	// decorator's tokens — `@<expression>` optionally followed by an
	// argument list — then continue normal member parsing.
	if p.peek().val == "@" {
		p.skipDecorators()
		// Re-enter parseClassMember on the post-decorator tokens so any
		// modifiers (public/private/static/…) on the decorated member are
		// picked up correctly.
		return p.parseClassMember(className, parentExported)
	}
	if p.peek().val == "*" {
		p.pos++ // generator method
	}
	nameTok := p.peek()
	if nameTok.kind != "ident" {
		// Computed property names (`[Symbol.iterator]()`), semicolons, etc.
		// Leave them for the caller's skip-one fallback.
		p.pos = saved
		return false
	}
	p.pos++
	if p.peek().val == "?" {
		p.pos++ // optional member
	}
	p.skipGenericParams()
	if p.peek().val != "(" {
		// Field declaration; skip until ';' or '}'.
		p.skipUntilSemiOrEnd()
		return true
	}
	params := p.captureBalanced("(", ")")
	if params == nil {
		p.pos = saved
		return false
	}
	p.skipReturnType()
	endLine := startLine
	if p.peek().val == "{" {
		body := p.captureBalanced("{", "}")
		if body == nil {
			p.pos = saved
			return false
		}
		if p.pos > 0 {
			endLine = p.tokens[p.pos-1].line
		} else if len(body) > 0 {
			endLine = body[len(body)-1].line
		}
	} else if p.peek().val == ";" {
		endLine = p.peek().line
		p.pos++
	}
	p.syms = append(p.syms, store.Symbol{
		FilePath:      p.path,
		Name:          nameTok.val,
		QualifiedName: className + "." + nameTok.val,
		Kind:          "method",
		Signature:     nameTok.val + "(" + extractParamNames(params) + ")",
		StartLine:     startLine,
		EndLine:       endLine,
		Exported:      parentExported,
	})
	return true
}

func (p *tsParser) parseInterface(exported bool, startLine int) bool {
	saved := p.pos
	if p.peek().val != "interface" {
		p.pos = saved
		return false
	}
	p.pos++
	nameTok := p.peek()
	if nameTok.kind != "ident" {
		p.pos = saved
		return false
	}
	p.pos++
	p.skipGenericParams()
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if v == "{" {
			break
		}
		if v == ";" || v == "}" {
			p.pos = saved
			return false
		}
		p.pos++
	}
	if p.pos >= len(p.tokens) {
		p.pos = saved
		return false
	}
	body := p.captureBalanced("{", "}")
	if body == nil {
		p.pos = saved
		return false
	}
	endLine := startLine
	if len(body) > 0 {
		endLine = body[len(body)-1].line
	} else if p.pos > 0 {
		endLine = p.tokens[p.pos-1].line
	}
	p.syms = append(p.syms, store.Symbol{
		FilePath:      p.path,
		Name:          nameTok.val,
		QualifiedName: nameTok.val,
		Kind:          "interface",
		Signature:     "interface " + nameTok.val,
		StartLine:     startLine,
		EndLine:       endLine,
		Exported:      exported,
	})
	return true
}

func (p *tsParser) parseTypeAlias(exported bool, startLine int) bool {
	saved := p.pos
	if p.peek().val != "type" {
		p.pos = saved
		return false
	}
	p.pos++
	nameTok := p.peek()
	if nameTok.kind != "ident" {
		p.pos = saved
		return false
	}
	p.pos++
	p.skipGenericParams()
	if p.peek().val != "=" {
		p.pos = saved
		return false
	}
	p.pos++ // consume '='
	endLine := startLine
	depth := 0
	for p.pos < len(p.tokens) {
		t := p.peek()
		v := t.val
		if depth == 0 {
			if v == ";" {
				endLine = t.line
				p.pos++
				break
			}
			if t.kind == "kw" {
				switch v {
				case "export", "function", "class", "interface", "type",
					"const", "let", "var", "import", "declare", "enum",
					"namespace", "module":
					// Hit the next declaration without seeing `;`. Stop here
					// to keep that declaration parseable.
					return true
				}
			}
		}
		switch v {
		case "(", "{", "[", "<":
			depth++
		case ")", "}", "]", ">":
			if depth == 0 {
				return true
			}
			depth--
		}
		endLine = t.line
		p.pos++
	}
	p.syms = append(p.syms, store.Symbol{
		FilePath:      p.path,
		Name:          nameTok.val,
		QualifiedName: nameTok.val,
		Kind:          "type",
		Signature:     "type " + nameTok.val,
		StartLine:     startLine,
		EndLine:       endLine,
		Exported:      exported,
	})
	return true
}

func (p *tsParser) parseVarDecl(kw string, exported bool, startLine int) bool {
	saved := p.pos
	if p.peek().val != kw {
		p.pos = saved
		return false
	}
	p.pos++
	// `const enum` is enum, not a var decl.
	if p.peek().val == "enum" {
		p.skipDecl()
		return true
	}
	kind := "var"
	if kw == "const" {
		kind = "const"
	}
	emitted := false
	for p.pos < len(p.tokens) {
		nameTok := p.peek()
		if nameTok.kind != "ident" {
			if nameTok.val == "{" || nameTok.val == "[" {
				// Destructuring binding; not a single name we can record.
				p.skipBalanced()
				if p.peek().val == ":" {
					p.skipTypeAnnotation(true)
				}
				if p.peek().val == "=" {
					p.pos++
					p.skipInitializer()
				}
				if p.peek().val == "," {
					p.pos++
					continue
				}
				break
			}
			if !emitted {
				p.pos = saved
				return false
			}
			break
		}
		p.pos++
		if p.peek().val == ":" {
			p.skipTypeAnnotation(true)
		}
		if p.peek().val == "=" {
			p.pos++
			p.skipInitializer()
		}
		endLine := nameTok.line
		if p.pos > 0 {
			endLine = p.tokens[p.pos-1].line
		}
		p.syms = append(p.syms, store.Symbol{
			FilePath:      p.path,
			Name:          nameTok.val,
			QualifiedName: nameTok.val,
			Kind:          kind,
			Signature:     kw + " " + nameTok.val,
			StartLine:     startLine,
			EndLine:       endLine,
			Exported:      exported,
		})
		emitted = true
		if p.peek().val == "," {
			p.pos++
			continue
		}
		break
	}
	if p.peek().val == ";" {
		p.pos++
	}
	return true
}

// skipGenericParams: if the next token is `<`, consume through the matching
// `>`. Used to swallow `<T extends U>`-style parameter lists without
// extracting them.
func (p *tsParser) skipGenericParams() {
	if p.peek().val != "<" {
		return
	}
	p.pos++
	depth := 1
	for p.pos < len(p.tokens) && depth > 0 {
		v := p.peek().val
		if v == "<" {
			depth++
		} else if v == ">" {
			depth--
		}
		p.pos++
	}
}

// captureBalanced expects the current token to be `open`. It advances past
// `open`, returns the inner token slice, and leaves the cursor just past the
// matching `close`. Nested `open`/`close` pairs are counted; other delimiter
// types are not — TS source is assumed to be syntactically well-formed.
func (p *tsParser) captureBalanced(open, close string) []tsToken {
	if p.peek().val != open {
		return nil
	}
	p.pos++
	start := p.pos
	depth := 1
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if v == open {
			depth++
		} else if v == close {
			depth--
			if depth == 0 {
				inner := p.tokens[start:p.pos]
				p.pos++
				return inner
			}
		}
		p.pos++
	}
	return nil
}

// skipReturnType: from just after the `)` of a function parameter list,
// consume an optional `: T` return type annotation. Stops at the function
// body `{` at depth 0, at `;` (declaration-only), or at a top-level keyword.
// Tracks `()`, `[]`, and `<>` as delimiters but intentionally does NOT track
// `{}` — that lets us treat the first `{` at depth 0 as the function body.
// As a side effect, object-type-literal return types (`function f(): { a: T }
// { body }`) are mis-extracted: the first `{` is treated as the body. This is
// uncommon in real code and the parser still emits the function symbol; the
// only consequence is the wrong EndLine on those declarations. Documented
// trade-off for v0.
func (p *tsParser) skipReturnType() {
	if p.peek().val != ":" {
		return
	}
	p.pos++
	depth := 0
	for p.pos < len(p.tokens) {
		t := p.peek()
		if depth == 0 {
			switch t.val {
			case "{", ";":
				return
			}
			if t.kind == "kw" {
				switch t.val {
				case "export", "function", "class", "interface", "type",
					"const", "let", "var", "import", "declare", "default",
					"enum", "namespace", "module":
					return
				}
			}
		}
		switch t.val {
		case "(", "[", "<":
			depth++
		case ")", "]", ">":
			if depth > 0 {
				depth--
			} else {
				return
			}
		}
		p.pos++
	}
}

// skipTypeAnnotation: consume a type expression introduced by `:`. The colon
// must be the current token; it is consumed before tracking depth. The
// annotation ends on top-level `;`, `,`, `)`, `}`, `]`, `>`, or — if
// stopOnAssign is true — `=`. `<` and `>` count as delimiters here because
// type expressions use angle-bracket generics; `{}` also tracks because
// object type literals are valid in variable/parameter annotations.
func (p *tsParser) skipTypeAnnotation(stopOnAssign bool) {
	if p.peek().val != ":" {
		return
	}
	p.pos++
	depth := 0
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if depth == 0 {
			switch v {
			case ";", ",", ")", "}", "]", ">":
				return
			case "=":
				if stopOnAssign {
					return
				}
			}
		}
		switch v {
		case "(", "{", "[", "<":
			depth++
		case ")", "}", "]", ">":
			depth--
		}
		p.pos++
	}
}

// skipInitializer: consume the right-hand side of `name = ...`. Stops on a
// top-level `;` or `,`. Angle brackets are not tracked as delimiters because
// `<` and `>` in initializers are usually comparison operators, not generics.
func (p *tsParser) skipInitializer() {
	depth := 0
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if depth == 0 {
			if v == ";" || v == "," {
				return
			}
		}
		switch v {
		case "(", "{", "[":
			depth++
		case ")", "}", "]":
			if depth == 0 {
				return
			}
			depth--
		}
		p.pos++
	}
}

// skipUntilSemiOrEnd: from inside a class body, advance past a field decl.
// Consumes the trailing `;` if present; leaves `}` for the caller so the
// class body loop can detect the end.
func (p *tsParser) skipUntilSemiOrEnd() {
	depth := 0
	for p.pos < len(p.tokens) {
		v := p.peek().val
		if depth == 0 {
			if v == ";" {
				p.pos++
				return
			}
			if v == "}" {
				return
			}
		}
		switch v {
		case "(", "{", "[":
			depth++
		case ")", "}", "]":
			if depth == 0 {
				return
			}
			depth--
		}
		p.pos++
	}
}

// skipDecorators consumes a leading run of `@<expression>` decorators and
// their optional `(args)` lists. After it returns, the cursor is at the
// first token that is not part of a decorator (typically the next modifier
// or member name). The decorator expression is recognized as an identifier
// chain (`@a.b.c`); anything more exotic is left for the caller's
// skip-one-token fallback.
func (p *tsParser) skipDecorators() {
	for p.peek().val == "@" {
		p.pos++ // '@'
		t := p.peek()
		if t.kind != "ident" && t.kind != "kw" {
			// Not a recognizable decorator name; bail so we don't loop.
			return
		}
		p.pos++
		for p.peek().val == "." {
			p.pos++
			t := p.peek()
			if t.kind != "ident" && t.kind != "kw" {
				break
			}
			p.pos++
		}
		if p.peek().val == "(" {
			p.skipBalanced()
		}
	}
}

// skipBalanced consumes one balanced bracketed group starting at the cursor.
// No-op if the cursor isn't on `{`, `[`, or `(`.
func (p *tsParser) skipBalanced() {
	v := p.peek().val
	var close string
	switch v {
	case "{":
		close = "}"
	case "[":
		close = "]"
	case "(":
		close = ")"
	default:
		return
	}
	open := v
	p.pos++
	depth := 1
	for p.pos < len(p.tokens) && depth > 0 {
		vv := p.peek().val
		if vv == open {
			depth++
		} else if vv == close {
			depth--
		}
		p.pos++
	}
}

// skipDecl consumes a single declaration to its terminating `;` or matching
// `}`. Used for constructs out of v0 scope (enum, namespace, module, import).
func (p *tsParser) skipDecl() {
	depth := 0
	for p.pos < len(p.tokens) {
		v := p.peek().val
		switch v {
		case "{":
			depth++
			p.pos++
			continue
		case "}":
			depth--
			p.pos++
			if depth <= 0 {
				return
			}
			continue
		case ";":
			if depth == 0 {
				p.pos++
				return
			}
		}
		p.pos++
	}
}

// extractParamNames pulls bare parameter names out of a function or method
// parameter token slice. Type annotations, default values, and modifiers
// are dropped; rest params keep their `...` prefix; destructuring patterns
// collapse to `{...}` or `[...]` since there is no single name to record.
func extractParamNames(tokens []tsToken) string {
	var names []string
	i := 0
	for i < len(tokens) {
		// Skip parameter property modifiers (constructor short-hand).
		for i < len(tokens) {
			t := tokens[i]
			if t.kind == "kw" && (t.val == "public" || t.val == "private" ||
				t.val == "protected" || t.val == "readonly") {
				i++
				continue
			}
			break
		}
		if i >= len(tokens) {
			break
		}
		rest := ""
		if tokens[i].val == "..." {
			rest = "..."
			i++
		}
		if i >= len(tokens) {
			break
		}
		t := tokens[i]
		switch {
		case t.kind == "ident":
			names = append(names, rest+t.val)
			i++
		case t.val == "{" || t.val == "[":
			open := t.val
			close := "}"
			if open == "[" {
				close = "]"
			}
			depth := 1
			i++
			for i < len(tokens) && depth > 0 {
				v := tokens[i].val
				if v == open {
					depth++
				}
				if v == close {
					depth--
				}
				i++
			}
			names = append(names, rest+open+"..."+close)
		default:
			i++
		}
		// Advance to the next comma at depth 0.
		depth := 0
		for i < len(tokens) {
			v := tokens[i].val
			if depth == 0 && v == "," {
				i++
				break
			}
			switch v {
			case "(", "[", "{", "<":
				depth++
			case ")", "]", "}", ">":
				if depth > 0 {
					depth--
				}
			}
			i++
		}
	}
	return strings.Join(names, ", ")
}
