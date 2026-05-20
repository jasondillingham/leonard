package parse

import (
	"strings"
	"testing"
)

const sampleTypeScriptSrc = `// top-level fixture
import { something } from "./other";

export const VERSION = "0.1";
let counter = 0;
var legacyFlag: boolean = true;

const a = 1, b = 2;

export function greet(name: string): string {
    return "hi " + name;
}

function _privateHelper() {
    return 42;
}

export async function fetchAll(urls: string[], opts?: { signal?: AbortSignal }) {
    return urls.length;
}

export interface Greeter {
    greet(name: string): string;
}

export type ID = string | number;

type Pair<K, V> = { key: K; value: V };

export class Container<T> extends Base implements Greeter {
    static created = 0;
    private items: T[] = [];

    constructor(initial: T[]) {
        this.items = initial;
    }

    public add(item: T): void {
        this.items.push(item);
    }

    private _refresh() {
        return this.items.length;
    }

    async load(url: string): Promise<T[]> {
        return [];
    }

    get size(): number {
        return this.items.length;
    }
}

class _Private {
    constructor() {}
}
`

func TestExtractTypeScript_Symbols(t *testing.T) {
	t.Parallel()

	syms, err := ExtractTypeScript("sample.ts", []byte(sampleTypeScriptSrc))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}

	type want struct {
		kind     string
		qname    string
		exported bool
	}

	got := map[string]want{}
	for _, s := range syms {
		if s.FilePath != "sample.ts" {
			t.Errorf("FilePath = %q, want sample.ts", s.FilePath)
		}
		if s.StartLine <= 0 || s.EndLine < s.StartLine {
			t.Errorf("%s: bad line range %d-%d", s.Name, s.StartLine, s.EndLine)
		}
		key := s.Kind + ":" + s.QualifiedName
		got[key] = want{s.Kind, s.QualifiedName, s.Exported}
	}

	expect := map[string]want{
		"const:sample.VERSION":           {"const", "sample.VERSION", true},
		"var:sample.counter":             {"var", "sample.counter", false},
		"var:sample.legacyFlag":          {"var", "sample.legacyFlag", false},
		"const:sample.a":                 {"const", "sample.a", false},
		"const:sample.b":                 {"const", "sample.b", false},
		"function:sample.greet":          {"function", "sample.greet", true},
		"function:sample._privateHelper": {"function", "sample._privateHelper", false},
		"function:sample.fetchAll":       {"function", "sample.fetchAll", true},
		"interface:sample.Greeter":       {"interface", "sample.Greeter", true},
		"type:sample.ID":                 {"type", "sample.ID", true},
		"type:sample.Pair":               {"type", "sample.Pair", false},
		"type:sample.Container":          {"type", "sample.Container", true},
		"type:sample._Private":           {"type", "sample._Private", false},
		"method:sample.Container.constructor": {"method", "sample.Container.constructor", true},
		"method:sample.Container.add":         {"method", "sample.Container.add", true},
		"method:sample.Container._refresh":    {"method", "sample.Container._refresh", true},
		"method:sample.Container.load":        {"method", "sample.Container.load", true},
		"method:sample.Container.size":        {"method", "sample.Container.size", true},
		"method:sample._Private.constructor":  {"method", "sample._Private.constructor", false},
	}

	for key, w := range expect {
		g, ok := got[key]
		if !ok {
			keys := make([]string, 0, len(got))
			for k := range got {
				keys = append(keys, k)
			}
			t.Errorf("missing symbol %s; have %v", key, keys)
			continue
		}
		if g != w {
			t.Errorf("%s: got %+v, want %+v", key, g, w)
		}
	}

	// `created` and `items` are class fields, not methods — they must not show
	// up as methods or as top-level vars.
	for _, unwanted := range []string{"method:sample.Container.created", "method:sample.Container.items", "var:sample.created", "var:sample.items"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unexpected symbol %s leaked from class field", unwanted)
		}
	}
}

// TestExtractTypeScript_ObjectTypeLiteralReturn locks in the fix for the
// real-world bug found dogfooding the parser against Zod: a class method
// whose return type is an inline `{ ... }` object type literal used to
// confuse the brace counter. The type literal was treated as the body,
// the real body's closing brace was mistaken for the class end, and every
// class member declared after that method was silently dropped from the
// index.
//
// This fixture mirrors the shape in Zod's v3/types.ts ZodType class.
func TestExtractTypeScript_ObjectTypeLiteralReturn(t *testing.T) {
	t.Parallel()
	src := `export class C {
  early(): string {
    return "";
  }
  inline(input: number): {
    status: string;
    ctx: number;
  } {
    return { status: "", ctx: input };
  }
  late(): number {
    return 1;
  }
  unionShape(): { a: number } | string {
    return "";
  }
  arrowShape(): (x: number) => string {
    return (_x) => "";
  }
}
`
	syms, err := ExtractTypeScript("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	want := []string{
		"method:c.C.early",
		"method:c.C.inline",
		"method:c.C.late",
		"method:c.C.unionShape",
		"method:c.C.arrowShape",
	}
	got := map[string]bool{}
	var classEndLine int
	for _, s := range syms {
		got[s.Kind+":"+s.QualifiedName] = true
		if s.QualifiedName == "c.C" && s.Kind == "type" {
			classEndLine = s.EndLine
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing symbol %s; the object-type-literal return type bug truncated the class", w)
		}
	}
	// The class spans from line 1 to the final `}` on line 20.
	if classEndLine < 19 {
		t.Errorf("class C end_line = %d, want >= 19 (truncation indicates skipReturnType regression)", classEndLine)
	}
}

// TestExtractTypeScript_KeywordAsMethodName locks in the fix for the
// Zod-dogfood bug: methods whose name is a TS keyword token (`default`,
// `type`, `import`, `declare`, ...) were not recognized by parseClassMember,
// the class-body loop walked through their tokens one at a time, and the
// real body's closing `}` was eventually mistaken for the class end —
// silently dropping every method declared after the keyword-named one.
// TestExtractTypeScript_ArrowFunctionExports covers selfhost F2. Each of
// these shapes should produce kind=function (not kind=const) so that a
// `find_symbol(kind=function)` query against a real-world TS codebase
// matches them. Dogfooded against Zod where 166 of these were previously
// hidden in the const namespace.
func TestExtractTypeScript_ArrowFunctionExports(t *testing.T) {
	t.Parallel()
	src := `export const plain = () => 1;
export const oneArg = (x: number) => x * 2;
export const typed: (x: number) => number = (x) => x + 1;
export const asynced = async () => Promise.resolve(1);
export const generic = <T,>(x: T): T => x;
export const sugar = x => x + 1;
export const asyncSugar = async x => x + 1;
export const block = (n: number) => {
  const doubled = n * 2;
  return doubled;
};
export const NOT_A_FUNCTION = 42;
export const obj = { a: 1, b: 2 };
`
	syms, err := ExtractTypeScript("a.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	want := map[string]string{
		"plain":          "function",
		"oneArg":         "function",
		"typed":          "function",
		"asynced":        "function",
		"generic":        "function",
		"sugar":          "function",
		"asyncSugar":     "function",
		"block":          "function",
		"NOT_A_FUNCTION": "const",
		"obj":            "const",
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: kind=%q, want %q", name, got[name], kind)
		}
	}
	// Signature for arrow-function symbols should look like the function
	// form so verify_symbol output is consistent across declaration styles.
	for _, s := range syms {
		if s.Kind == "function" && !strings.HasPrefix(s.Signature, "function "+s.Name+"(") {
			t.Errorf("%s signature = %q, want prefix `function %s(`", s.Name, s.Signature, s.Name)
		}
	}
}

func TestExtractTypeScript_KeywordAsMethodName(t *testing.T) {
	t.Parallel()
	src := `export class C {
  before(): void {}
  default(x: number): C {
    return this;
  }
  type(): string { return ""; }
  declare(): void {}
  after(): void {}
}
`
	syms, err := ExtractTypeScript("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	wantMethods := []string{"before", "default", "type", "declare", "after"}
	got := map[string]bool{}
	for _, s := range syms {
		if s.Kind == "method" {
			got[s.Name] = true
		}
	}
	for _, w := range wantMethods {
		if !got[w] {
			t.Errorf("method %s missing (keyword-as-method-name regression?)", w)
		}
	}
}

// TestExtractTypeScript_StringLiteralMethodName covers `"foo"(...) { ... }`.
// The literal strips to whitespace before tokenizing, so the parser sees a
// bare `(` with no identifier. We can't emit a symbol (no textual handle)
// but we still need to consume the member's structure so the class brace
// counter stays aligned — otherwise the body's closing brace gets
// interpreted as the class end and subsequent members are lost.
func TestExtractTypeScript_StringLiteralMethodName(t *testing.T) {
	t.Parallel()
	src := `export class C {
  before(): void {}
  "~validate"(data: unknown): string {
    return "";
  }
  after(): void {}
}
`
	syms, err := ExtractTypeScript("c.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	var hasBefore, hasAfter bool
	for _, s := range syms {
		if s.Kind == "method" && s.Name == "before" {
			hasBefore = true
		}
		if s.Kind == "method" && s.Name == "after" {
			hasAfter = true
		}
	}
	if !hasBefore {
		t.Error("method 'before' missing")
	}
	if !hasAfter {
		t.Error("method 'after' missing — string-literal method name caused class-body truncation")
	}
}

func TestExtractTypeScript_Signatures(t *testing.T) {
	t.Parallel()

	syms, err := ExtractTypeScript("sample.ts", []byte(sampleTypeScriptSrc))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}

	byKey := map[string]string{}
	for _, s := range syms {
		byKey[s.Kind+":"+s.QualifiedName] = s.Signature
	}

	cases := []struct {
		key  string
		want string
	}{
		{"function:sample.greet", "function greet(name)"},
		{"function:sample.fetchAll", "function fetchAll(urls, opts)"},
		{"interface:sample.Greeter", "interface Greeter"},
		{"type:sample.ID", "type ID"},
		{"type:sample.Container", "class Container"},
		{"method:sample.Container.constructor", "constructor(initial)"},
		{"method:sample.Container.add", "add(item)"},
		{"method:sample.Container.load", "load(url)"},
		{"method:sample.Container.size", "size()"},
		{"const:sample.VERSION", "const VERSION"},
		{"var:sample.counter", "let counter"},
		{"var:sample.legacyFlag", "var legacyFlag"},
	}
	for _, c := range cases {
		got, ok := byKey[c.key]
		if !ok {
			t.Errorf("no symbol %s", c.key)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s signature: got %q, want contains %q", c.key, got, c.want)
		}
	}
}

func TestExtractTypeScript_TSXComponent(t *testing.T) {
	t.Parallel()
	src := `import * as React from "react";

interface Props {
    name: string;
}

export function Greeting({ name }: Props) {
    return <div className="hello">Hello, {name}!</div>;
}

export const Counter = ({ initial }: { initial: number }) => {
    const [count, setCount] = React.useState(initial);
    return (
        <button onClick={() => setCount(count + 1)}>
            {count}
        </button>
    );
};

export default Greeting;
`
	syms, err := ExtractTypeScript("App.tsx", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}

	want := map[string]string{
		"Props":    "interface",
		"Greeting": "function",
		// selfhost F2: arrow-function exports are now classified as
		// `function`, not `const`. Counter is `const Counter = (...) => …` —
		// same shape every React component file in the wild uses.
		"Counter": "function",
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: kind=%q, want %q; full: %v", name, got[name], kind, got)
		}
	}

	for _, s := range syms {
		switch s.Name {
		case "Greeting", "Counter":
			if !s.Exported {
				t.Errorf("%s should be Exported=true", s.Name)
			}
		case "Props":
			if s.Exported {
				t.Errorf("Props should be Exported=false (not declared with export)")
			}
		}
	}
}

func TestExtractTypeScript_StringsAndCommentsHideKeywords(t *testing.T) {
	t.Parallel()
	// All of these would look like top-level decls if comment/string stripping
	// were broken. None should be extracted as symbols.
	src := "// function HiddenInComment() {}\n" +
		"/* class HiddenInBlock {} */\n" +
		"const note = \"function StringHidden() {}\";\n" +
		"const tpl = `class TplHidden {} ${\"function NestedHidden(){}\"}`;\n" +
		"function Real() { return 1; }\n"
	syms, err := ExtractTypeScript("hide.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	for _, banned := range []string{"HiddenInComment", "HiddenInBlock", "StringHidden", "TplHidden", "NestedHidden"} {
		if names[banned] {
			t.Errorf("did not expect to extract %q — should have been stripped", banned)
		}
	}
	for _, kept := range []string{"note", "tpl", "Real"} {
		if !names[kept] {
			t.Errorf("expected to extract %q; got names=%v", kept, names)
		}
	}
}

func TestExtractTypeScript_NestedFunctionsIgnored(t *testing.T) {
	t.Parallel()
	src := `function outer() {
    function inner() { return 1; }
    class Inside { method() {} }
    return inner();
}
`
	syms, err := ExtractTypeScript("nested.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	names := map[string][]string{}
	for _, s := range syms {
		names[s.Name] = append(names[s.Name], s.Kind)
	}
	if _, ok := names["inner"]; ok {
		t.Errorf("nested function `inner` should not surface; got %v", names)
	}
	if _, ok := names["Inside"]; ok {
		t.Errorf("nested class `Inside` should not surface; got %v", names)
	}
	if _, ok := names["method"]; ok {
		t.Errorf("nested-class method `method` should not surface; got %v", names)
	}
	if _, ok := names["outer"]; !ok {
		t.Errorf("expected top-level `outer` function; got %v", names)
	}
}

func TestExtractTypeScript_LineRanges(t *testing.T) {
	t.Parallel()
	src := `// line 1
function spanning(
    a: number,
    b: number,
): number {
    const sum = a + b;
    return sum;
}

class Box {
    constructor(public name: string) {}

    label(prefix: string): string {
        return prefix + this.name;
    }
}
`
	syms, err := ExtractTypeScript("range.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	get := func(name string) (start, end int, found bool) {
		for _, s := range syms {
			if s.Name == name {
				return s.StartLine, s.EndLine, true
			}
		}
		return 0, 0, false
	}
	start, end, ok := get("spanning")
	if !ok {
		t.Fatal("spanning not extracted")
	}
	if start != 2 {
		t.Errorf("spanning start = %d, want 2", start)
	}
	if end < 8 {
		t.Errorf("spanning end = %d, want >= 8 (closing brace)", end)
	}

	start, end, ok = get("Box")
	if !ok {
		t.Fatal("Box not extracted")
	}
	if start != 10 {
		t.Errorf("Box start = %d, want 10", start)
	}
	if end < 16 {
		t.Errorf("Box end = %d, want >= 16 (closing brace)", end)
	}
}

func TestExtractTypeScript_EnumAndNamespaceIgnored(t *testing.T) {
	t.Parallel()
	src := `enum Color { Red, Green, Blue }
const enum Mode { On, Off }
namespace Geometry { export const PI = 3.14; }

export const after = 1;
`
	syms, err := ExtractTypeScript("enum.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Kind
	}
	for _, banned := range []string{"Color", "Mode", "Geometry"} {
		if _, ok := names[banned]; ok {
			t.Errorf("did not expect %q to surface (out-of-scope enum/namespace); got %v", banned, names)
		}
	}
	if names["after"] != "const" {
		t.Errorf("expected `after` after the enum/namespace block; got %v", names)
	}
}

func TestExtractTypeScript_MultiBindingVarDecl(t *testing.T) {
	t.Parallel()
	src := `const x = 1, y = "two", z = { nested: 3 };
let p: number = 5, q = 6;
`
	syms, err := ExtractTypeScript("multi.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	want := map[string]string{
		"x": "const", "y": "const", "z": "const",
		"p": "var", "q": "var",
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: kind=%q, want %q; full=%v", name, got[name], kind, got)
		}
	}
}

func TestExtractTypeScript_DestructuringSkipped(t *testing.T) {
	t.Parallel()
	src := `const { a, b } = obj;
const [c, d] = arr;
const real = 1;
`
	syms, err := ExtractTypeScript("destr.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	for _, s := range syms {
		switch s.Name {
		case "a", "b", "c", "d":
			t.Errorf("destructured binding %q should not be extracted in v0", s.Name)
		}
	}
	found := false
	for _, s := range syms {
		if s.Name == "real" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected `real` to be extracted alongside the destructured bindings")
	}
}

func TestExtractTypeScript_EmptyAndMalformedSafe(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"",
		"// just a comment\n",
		"const ;",
		"function (",
		"class { }",
	} {
		_, err := ExtractTypeScript("x.ts", []byte(src))
		if err != nil {
			t.Errorf("ExtractTypeScript(%q): unexpected error %v", src, err)
		}
	}
}

// H1: regex literals must not contribute tokens to the parser, even when
// they contain `}` or `)`. Otherwise the brace counter mis-terminates the
// enclosing body and nested decls get hoisted to top-level.
func TestExtractTypeScript_RegexLiteralsStripped(t *testing.T) {
	t.Parallel()

	t.Run("function body containing regex with }", func(t *testing.T) {
		src := `export function outer() {
    const re = /\}/;
    function fakeNested() { return 99; }
    return re;
}
`
		syms, err := ExtractTypeScript("regex.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		names := map[string]string{}
		for _, s := range syms {
			names[s.Name] = s.Kind
		}
		if names["fakeNested"] != "" {
			t.Errorf("fakeNested was hoisted as a top-level symbol: %v", names)
		}
		if names["outer"] != "function" {
			t.Errorf("outer not extracted as function: %v", names)
		}
		for _, s := range syms {
			if s.Name == "outer" && s.EndLine < 5 {
				t.Errorf("outer.EndLine = %d, want >= 5 (real closing brace)", s.EndLine)
			}
		}
	})

	t.Run("class method containing regex with }", func(t *testing.T) {
		src := `export class C {
    m() {
        const re = /\}/;
        return 1;
    }
    n() { return 2; }
}
`
		syms, err := ExtractTypeScript("regex.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		names := map[string]string{}
		for _, s := range syms {
			names[s.QualifiedName] = s.Kind
		}
		if names["regex.C.m"] != "method" {
			t.Errorf("C.m missing: %v", names)
		}
		if names["regex.C.n"] != "method" {
			t.Errorf("C.n silently lost: %v", names)
		}
	})

	t.Run("module-level regex with fake function inside", func(t *testing.T) {
		src := `const re = /\}function NotReal(){}/;
export const real = 1;
`
		syms, err := ExtractTypeScript("regex.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		for _, s := range syms {
			if s.Name == "NotReal" {
				t.Errorf("NotReal fabricated from inside regex literal: %+v", s)
			}
		}
		names := map[string]bool{}
		for _, s := range syms {
			names[s.Name] = true
		}
		if !names["re"] || !names["real"] {
			t.Errorf("missing expected symbols: %v", names)
		}
	})

	t.Run("division is not confused for regex", func(t *testing.T) {
		src := `export const ratio = a / b / c;
export function divide(x: number, y: number): number { return x / y; }
`
		syms, err := ExtractTypeScript("div.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		names := map[string]string{}
		for _, s := range syms {
			names[s.Name] = s.Kind
		}
		if names["ratio"] != "const" {
			t.Errorf("ratio not extracted as const: %v", names)
		}
		if names["divide"] != "function" {
			t.Errorf("divide not extracted as function: %v", names)
		}
	})
}

// H2: `static { ... }` initializer blocks (TS 4.4+) must be skipped as a
// whole construct so the matching `}` isn't mistaken for the class body
// terminator.
func TestExtractTypeScript_StaticInitBlock(t *testing.T) {
	t.Parallel()

	src := `export class Foo {
    static count = 0;
    static {
        Foo.count = 1;
    }
    static incr() { Foo.count++; }
    method() { return Foo.count; }
}
`
	syms, err := ExtractTypeScript("static.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	byQName := map[string]string{}
	for _, s := range syms {
		byQName[s.QualifiedName] = s.Kind
	}
	if byQName["static.Foo"] != "type" {
		t.Errorf("Foo class missing: %v", byQName)
	}
	if byQName["static.Foo.incr"] != "method" {
		t.Errorf("Foo.incr lost after static block: %v", byQName)
	}
	if byQName["static.Foo.method"] != "method" {
		t.Errorf("Foo.method lost after static block: %v", byQName)
	}
	for _, s := range syms {
		if s.Name == "Foo" && s.EndLine < 7 {
			t.Errorf("Foo.EndLine = %d, want >= 7 (real closing brace)", s.EndLine)
		}
	}
}

// H3: decorators on class members must be bounded — every method after the
// first decorated one was being eaten by skipUntilSemiOrEnd.
func TestExtractTypeScript_DecoratedClassMembers(t *testing.T) {
	t.Parallel()

	t.Run("bare decorators", func(t *testing.T) {
		src := `export class Foo {
    @log a() { return 1; }
    @log b() { return 2; }
    @log c() { return 3; }
}
`
		syms, err := ExtractTypeScript("dec.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		byQName := map[string]string{}
		for _, s := range syms {
			byQName[s.QualifiedName] = s.Kind
		}
		for _, name := range []string{"dec.Foo.a", "dec.Foo.b", "dec.Foo.c"} {
			if byQName[name] != "method" {
				t.Errorf("%s missing or wrong kind: %v", name, byQName)
			}
		}
	})

	t.Run("parenthesized decorators", func(t *testing.T) {
		src := `export class D {
    @readonly name = 'x';
    @log() greet(): string { return this.name; }
    @log({ level: 'info' }) shout(): string { return this.name; }
}
`
		syms, err := ExtractTypeScript("dec.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		byQName := map[string]string{}
		for _, s := range syms {
			byQName[s.QualifiedName] = s.Kind
		}
		if byQName["dec.D.greet"] != "method" {
			t.Errorf("D.greet missing: %v", byQName)
		}
		if byQName["dec.D.shout"] != "method" {
			t.Errorf("D.shout missing: %v", byQName)
		}
		// The parenthesized form was fabricating a `log` method (F1).
		if _, leaked := byQName["dec.D.log"]; leaked {
			t.Errorf("D.log fabricated from decorator name: %v", byQName)
		}
	})

	t.Run("decorator stacks with modifiers", func(t *testing.T) {
		src := `export class E {
    @log @cache static compute(): number { return 1; }
    @log async fetchOne(): Promise<number> { return 1; }
}
`
		syms, err := ExtractTypeScript("dec.ts", []byte(src))
		if err != nil {
			t.Fatalf("ExtractTypeScript: %v", err)
		}
		byQName := map[string]string{}
		for _, s := range syms {
			byQName[s.QualifiedName] = s.Kind
		}
		if byQName["dec.E.compute"] != "method" {
			t.Errorf("E.compute missing: %v", byQName)
		}
		if byQName["dec.E.fetchOne"] != "method" {
			t.Errorf("E.fetchOne missing: %v", byQName)
		}
	})
}


// TestExtractTypeScript_MixinExtends covers TS M2: a class extending the
// result of a mixin call (e.g. `extends Mixin(Base)`) used to be silently
// dropped because parseClass's pre-body scan bailed on '('. After the fix,
// the class and its members extract normally and subsequent top-level
// declarations are no longer eaten by the abandoned cursor.
func TestExtractTypeScript_MixinExtends(t *testing.T) {
	t.Parallel()
	src := `export class C extends Mixin(BaseClass) {
    m() { return 1; }
}
export const after = 1;
`
	syms, err := ExtractTypeScript("mixin.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	byQName := map[string]string{}
	for _, s := range syms {
		byQName[s.QualifiedName] = s.Kind
	}
	if byQName["mixin.C"] != "type" {
		t.Errorf("class C with mixin extends not extracted: %v", byQName)
	}
	if byQName["mixin.C.m"] != "method" {
		t.Errorf("method C.m not extracted: %v", byQName)
	}
	if byQName["mixin.after"] != "const" {
		t.Errorf("post-class const lost after mixin class: %v", byQName)
	}
}

// TestExtractTypeScript_UnterminatedStringLocalizes covers TS M3: an
// unterminated string used to eat the rest of the file because
// skipInitializer kept consuming through balanced {}/[]/() groups in the
// downstream real code. A stray syntax error should localize — symbols
// declared after the broken line must still extract.
func TestExtractTypeScript_UnterminatedStringLocalizes(t *testing.T) {
	t.Parallel()
	src := "const broken = \"unterminated\nexport function realA() { return 1; }\nexport const realB = 2;\n"
	syms, err := ExtractTypeScript("broken.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	byQName := map[string]string{}
	for _, s := range syms {
		byQName[s.QualifiedName] = s.Kind
	}
	if byQName["broken.realA"] != "function" {
		t.Errorf("realA should extract after unterminated string: %v", byQName)
	}
	if byQName["broken.realB"] != "const" {
		t.Errorf("realB should extract after unterminated string: %v", byQName)
	}
}

// TestExtractTypeScript_ObjectTypeLiteralReturnTopLevel covers TS M4: a
// top-level function with an object-type-literal return annotation used
// to truncate at the wrong line and leak the body as orphaned tokens,
// hiding subsequent decls from extraction. (The class-method variant is
// already covered by TestExtractTypeScript_ObjectTypeLiteralReturn.)
func TestExtractTypeScript_ObjectTypeLiteralReturnTopLevel(t *testing.T) {
	t.Parallel()
	src := `export function f(): {
    a: number;
    b: string;
} {
    return { a: 1, b: '' };
}
export const after = 1;
`
	syms, err := ExtractTypeScript("m4.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	byQName := map[string]int{}
	for _, s := range syms {
		byQName[s.QualifiedName] = s.EndLine
	}
	if _, ok := byQName["m4.f"]; !ok {
		t.Fatalf("function f missing; have %+v", byQName)
	}
	if got := byQName["m4.f"]; got < 6 {
		t.Errorf("f end_line = %d, want >= 6 (real closing brace on line 6)", got)
	}
	if _, ok := byQName["m4.after"]; !ok {
		t.Errorf("post-function const after missing — body leaked as top-level tokens: %+v", byQName)
	}
}

// TestExtractTypeScript_ExportDefaultNamed covers TS M1: a named
// `export default function|class ...` used to emit the symbol with
// Exported=false because the default branch bailed and the outer
// parser re-attempted without the export bit. Forward the export
// bit so default exports show as reachable.
func TestExtractTypeScript_ExportDefaultNamed(t *testing.T) {
	t.Parallel()
	src := `export default function defaultFn() { return 1; }
export default class DefaultCls { method() { return 1; } }
export default async function asyncDef(): Promise<number> { return 1; }
`
	syms, err := ExtractTypeScript("def.ts", []byte(src))
	if err != nil {
		t.Fatalf("ExtractTypeScript: %v", err)
	}
	byQName := map[string]struct {
		kind     string
		exported bool
	}{}
	for _, s := range syms {
		byQName[s.QualifiedName] = struct {
			kind     string
			exported bool
		}{s.Kind, s.Exported}
	}
	for _, want := range []struct {
		qname, kind string
	}{
		{"def.defaultFn", "function"},
		{"def.DefaultCls", "type"},
		{"def.asyncDef", "function"},
	} {
		got, ok := byQName[want.qname]
		if !ok {
			t.Errorf("%s missing: %v", want.qname, byQName)
			continue
		}
		if got.kind != want.kind {
			t.Errorf("%s: kind=%q, want %q", want.qname, got.kind, want.kind)
		}
		if !got.exported {
			t.Errorf("%s: Exported=false, want true (default exports are reachable from outside)", want.qname)
		}
	}
	// Methods inside an export-default class should also be Exported=true.
	if m, ok := byQName["def.DefaultCls.method"]; ok && !m.exported {
		t.Errorf("DefaultCls.method: Exported=false, want true (inherits from enclosing class)")
	}
}
