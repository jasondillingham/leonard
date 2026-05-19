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
		"const:VERSION":           {"const", "VERSION", true},
		"var:counter":             {"var", "counter", false},
		"var:legacyFlag":          {"var", "legacyFlag", false},
		"const:a":                 {"const", "a", false},
		"const:b":                 {"const", "b", false},
		"function:greet":          {"function", "greet", true},
		"function:_privateHelper": {"function", "_privateHelper", false},
		"function:fetchAll":       {"function", "fetchAll", true},
		"interface:Greeter":       {"interface", "Greeter", true},
		"type:ID":                 {"type", "ID", true},
		"type:Pair":               {"type", "Pair", false},
		"type:Container":          {"type", "Container", true},
		"type:_Private":           {"type", "_Private", false},
		"method:Container.constructor": {"method", "Container.constructor", true},
		"method:Container.add":         {"method", "Container.add", true},
		"method:Container._refresh":    {"method", "Container._refresh", true},
		"method:Container.load":        {"method", "Container.load", true},
		"method:Container.size":        {"method", "Container.size", true},
		"method:_Private.constructor":  {"method", "_Private.constructor", false},
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
	for _, unwanted := range []string{"method:Container.created", "method:Container.items", "var:created", "var:items"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unexpected symbol %s leaked from class field", unwanted)
		}
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
		{"function:greet", "function greet(name)"},
		{"function:fetchAll", "function fetchAll(urls, opts)"},
		{"interface:Greeter", "interface Greeter"},
		{"type:ID", "type ID"},
		{"type:Container", "class Container"},
		{"method:Container.constructor", "constructor(initial)"},
		{"method:Container.add", "add(item)"},
		{"method:Container.load", "load(url)"},
		{"method:Container.size", "size()"},
		{"const:VERSION", "const VERSION"},
		{"var:counter", "let counter"},
		{"var:legacyFlag", "var legacyFlag"},
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
		"Counter":  "const",
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

