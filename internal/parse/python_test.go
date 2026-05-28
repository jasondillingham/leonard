package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const samplePythonSrc = `"""Top-level fixture module."""

VERSION = "0.1"
_internal = 42
PUBLIC_NAME = "leonard"


def hello(name):
    return "hello " + name


def _helper():
    return None


def add(a, b, *rest, **opts):
    total = a + b
    for r in rest:
        total = total + r
    return total


class Widget:
    pass


class _PrivateWidget(Widget):
    pass


class Container:
    def __init__(self, name):
        self.name = name

    def add(self, item):
        pass

    def _internal_helper(self):
        return None
`

func TestExtractPython_Symbols(t *testing.T) {
	t.Parallel()

	syms, err := ExtractPython("sample.py", []byte(samplePythonSrc))
	if err != nil {
		t.Fatalf("ExtractPython: %v", err)
	}

	type want struct {
		kind     string
		qname    string
		exported bool
	}
	expect := map[string]want{
		"VERSION":         {"var", "sample.VERSION", true},
		"_internal":       {"var", "sample._internal", false},
		"PUBLIC_NAME":     {"var", "sample.PUBLIC_NAME", true},
		"hello":           {"function", "sample.hello", true},
		"_helper":         {"function", "sample._helper", false},
		"add":             {"function", "sample.add", true},
		"Widget":          {"type", "sample.Widget", true},
		"_PrivateWidget":  {"type", "sample._PrivateWidget", false},
		"Container":       {"type", "sample.Container", true},
	}
	wantMethods := map[string]want{
		"__init__":         {"method", "sample.Container.__init__", false},
		"add":              {"method", "sample.Container.add", true},
		"_internal_helper": {"method", "sample.Container._internal_helper", false},
	}

	got := map[string]want{}
	for _, s := range syms {
		if s.FilePath != "sample.py" {
			t.Errorf("FilePath = %q, want sample.py", s.FilePath)
		}
		if s.StartLine <= 0 || s.EndLine < s.StartLine {
			t.Errorf("%s: bad line range %d-%d", s.Name, s.StartLine, s.EndLine)
		}
		key := s.Name
		if s.Kind == "method" {
			key = "M:" + s.Name
		}
		// `add` exists both as a top-level function and a Container method; the
		// method key already prefixes M: above, but the top-level function would
		// collide with the method's bare Name in this map if we weren't careful.
		// Track it explicitly so both versions survive.
		if s.Kind == "function" {
			key = "F:" + s.Name
		}
		got[key] = want{s.Kind, s.QualifiedName, s.Exported}
	}

	for name, w := range expect {
		var key string
		switch w.kind {
		case "function":
			key = "F:" + name
		case "method":
			key = "M:" + name
		default:
			key = name
		}
		g, ok := got[key]
		if !ok {
			t.Errorf("missing symbol %q (kind %s)", name, w.kind)
			continue
		}
		if g != w {
			t.Errorf("%s: got %+v, want %+v", name, g, w)
		}
	}
	for name, w := range wantMethods {
		g, ok := got["M:"+name]
		if !ok {
			t.Errorf("missing method %q", name)
			continue
		}
		if g != w {
			t.Errorf("method %s: got %+v, want %+v", name, g, w)
		}
	}
}

func TestExtractPython_Signatures(t *testing.T) {
	t.Parallel()

	syms, err := ExtractPython("sample.py", []byte(samplePythonSrc))
	if err != nil {
		t.Fatalf("ExtractPython: %v", err)
	}

	byKey := map[string]string{}
	for _, s := range syms {
		byKey[s.QualifiedName+":"+s.Kind] = s.Signature
	}

	cases := []struct {
		key  string
		want string
	}{
		{"sample.hello:function", "def hello(name)"},
		{"sample._helper:function", "def _helper()"},
		{"sample.add:function", "def add(a, b, *rest, **opts)"},
		{"sample.Widget:type", "class Widget"},
		{"sample._PrivateWidget:type", "class _PrivateWidget(Widget)"},
		{"sample.Container.__init__:method", "def __init__(self, name)"},
		{"sample.Container.add:method", "def add(self, item)"},
		{"sample.VERSION:var", "var VERSION"},
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

func TestExtractPython_SkipsBlankAndAttrTargets(t *testing.T) {
	t.Parallel()
	src := `_ = 1
Real = 2

class C:
    pass

c = C()
c.x = 3
`
	syms, err := ExtractPython("p.py", []byte(src))
	if err != nil {
		t.Fatalf("ExtractPython: %v", err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if names["_"] {
		t.Fatalf("did not expect blank identifier symbol")
	}
	if names["x"] {
		t.Fatalf("did not expect c.x attribute-write to surface a symbol")
	}
	for _, want := range []string{"Real", "C", "c"} {
		if !names[want] {
			t.Errorf("expected %q to be extracted; got %v", want, names)
		}
	}
}

func TestExtractPython_EndLineSpansBody(t *testing.T) {
	t.Parallel()
	src := `def long_func():
    a = 1
    b = 2
    return a + b
`
	syms, err := ExtractPython("p.py", []byte(src))
	if err != nil {
		t.Fatalf("ExtractPython: %v", err)
	}
	var fn *struct{ start, end int }
	for _, s := range syms {
		if s.Name == "long_func" {
			fn = &struct{ start, end int }{s.StartLine, s.EndLine}
		}
	}
	if fn == nil {
		t.Fatal("long_func not extracted")
	}
	if fn.start != 1 {
		t.Errorf("start line = %d, want 1", fn.start)
	}
	if fn.end < 4 {
		t.Errorf("end line = %d, want >= 4 (return statement)", fn.end)
	}
}

func TestExtractPython_DecoratedFunctionExtracted(t *testing.T) {
	t.Parallel()
	src := `def deco(f):
    return f

@deco
def wrapped():
    return 1
`
	syms, err := ExtractPython("p.py", []byte(src))
	if err != nil {
		t.Fatalf("ExtractPython: %v", err)
	}
	found := false
	for _, s := range syms {
		if s.Name == "wrapped" && s.Kind == "function" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected `wrapped` to be extracted despite @deco")
	}
}

func TestExtractPython_ParseError(t *testing.T) {
	t.Parallel()
	_, err := ExtractPython("bad.py", []byte("def :\n"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// TestExtractPython_ParseErrorIsSingleLine verifies the parser's error
// string is the single-line summary expected by indexer ParseFailures
// surfacing. Python's raw SyntaxError formats as multiple lines by
// default; the subprocess extractor collapses to "line N: SyntaxError: …".
func TestExtractPython_ParseErrorIsSingleLine(t *testing.T) {
	t.Parallel()
	// Genuinely-invalid Python: missing function name and parens.
	_, err := ExtractPython("x.py", []byte("def\n"))
	if err == nil {
		t.Fatal("expected parse error on malformed def")
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("error message contains a newline (should be single-line for CLI): %q", msg)
	}
	if !strings.Contains(msg, "SyntaxError") {
		t.Errorf("expected SyntaxError in message, got %q", msg)
	}
	if !strings.Contains(msg, "line ") {
		t.Errorf("expected a line number in message, got %q", msg)
	}
}

// TestExtractPython_ModernSyntax exercises the v0.2 parser swap:
// the old gpython-backed extractor rejected every one of these forms
// with a SyntaxError. The subprocess-to-python3 backend parses them
// natively and emits the same symbol shape as any other file.
func TestExtractPython_ModernSyntax(t *testing.T) {
	t.Parallel()
	src := `# modern-Python smoke test — all forms gpython rejected pre-3.5

VERSION: str = "0.1"           # PEP 526 annotated assignment
items: list[int] = []           # PEP 585 generic alias

def greet(name: str) -> str:
    return f"hello {name}"      # f-string

async def fetch_all(urls: list[str]) -> list[str]:
    return [u for u in urls if (n := len(u)) > 0]  # walrus

class Box[T]:                   # PEP 695 type parameter
    def put(self, item: T | None) -> None:  # PEP 604 union
        self.last = item

def kind_of(v: int | str) -> str:
    match v:                    # structural pattern matching
        case int():
            return "int"
        case str():
            return "str"
        case _:
            return "other"
`
	syms, err := ExtractPython("modern.py", []byte(src))
	if err != nil {
		t.Fatalf("ExtractPython on modern syntax: %v", err)
	}
	byQName := map[string]string{}
	for _, s := range syms {
		byQName[s.QualifiedName] = s.Kind
	}
	for _, want := range []struct {
		qname, kind string
	}{
		{"modern.VERSION", "var"},
		{"modern.items", "var"},
		{"modern.greet", "function"},
		{"modern.fetch_all", "function"},
		{"modern.Box", "type"},
		{"modern.Box.put", "method"},
		{"modern.kind_of", "function"},
	} {
		if got := byQName[want.qname]; got != want.kind {
			t.Errorf("%s: kind=%q, want %q (have %v)", want.qname, got, want.kind, byQName)
		}
	}
}

// TestExtractPython_HonorsLeonardPythonEnv covers fix-2 / python F1.
// The earlier code documented LEONARD_PYTHON in the error string and
// comment but never read os.Getenv. Setting the var to a stub script
// must route the exec through it.
func TestExtractPython_HonorsLeonardPythonEnv(t *testing.T) {
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub-python.sh")
	// The stub ignores args + stdin and emits an empty JSON array, the
	// minimum response the extractor accepts as "no symbols found".
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\necho '[]'\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("LEONARD_PYTHON", stub)

	syms, err := ExtractPython("ignored.py", []byte("# whatever\n"))
	if err != nil {
		t.Fatalf("ExtractPython with env override: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("stub returns [], got %v", syms)
	}
}

// TestExtractPython_TimeoutKillsHungInterpreter covers fix-2 / python F2.
// A pyenv/uv shim or NFS stall that hangs python3 used to freeze the
// indexer indefinitely (no exec.CommandContext, no Cancel). The
// extractor now caps each call at pythonTimeout — verify by pointing
// LEONARD_PYTHON at a script that never exits.
func TestExtractPython_TimeoutKillsHungInterpreter(t *testing.T) {
	tmp := t.TempDir()
	hang := filepath.Join(tmp, "hang.sh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("write hang script: %v", err)
	}
	t.Setenv("LEONARD_PYTHON", hang)
	oldTimeout := pythonTimeout
	pythonTimeout = 200 * time.Millisecond
	t.Cleanup(func() { pythonTimeout = oldTimeout })

	start := time.Now()
	_, err := ExtractPython("x.py", []byte("x = 1\n"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got %q", err.Error())
	}
	// Generous upper bound: even on a loaded CI box, 1s should be plenty.
	if elapsed > time.Second {
		t.Errorf("timeout took %v, want under 1s (timeout was %v)", elapsed, pythonTimeout)
	}
}

// TestExtractPython_MissingInterpreterReturnsUnavailable covers the
// no-python3-on-PATH case: the indexer should get ErrPythonUnavailable
// (which it surfaces as a per-file ParseFailure), not a hard crash.
func TestExtractPython_MissingInterpreterReturnsUnavailable(t *testing.T) {
	t.Setenv("LEONARD_PYTHON", "/definitely/not/a/real/binary")
	_, err := ExtractPython("x.py", []byte("x = 1\n"))
	if err != ErrPythonUnavailable {
		t.Errorf("expected ErrPythonUnavailable, got %v", err)
	}
}

// TestExtractPython_UTF8BOM verifies that a file whose first three bytes are
// the UTF-8 BOM (EF BB BF / U+FEFF) is parsed without error. CPython executes
// BOM-prefixed files cleanly, but ast.parse() rejects them with a SyntaxError
// unless the BOM is stripped first.
func TestExtractPython_UTF8BOM(t *testing.T) {
	t.Parallel()
	const bom = "\xef\xbb\xbf"
	src := bom + "BOM_VAR = 1\n"
	syms, err := ExtractPython("bom.py", []byte(src))
	if err != nil {
		t.Fatalf("ExtractPython rejected BOM-prefixed file: %v", err)
	}
	found := false
	for _, s := range syms {
		if s.Name == "BOM_VAR" {
			found = true
		}
	}
	if !found {
		t.Errorf("BOM_VAR not extracted from BOM-prefixed file; got %v", syms)
	}
}
