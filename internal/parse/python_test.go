package parse

import (
	"strings"
	"testing"
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
		"VERSION":         {"var", "VERSION", true},
		"_internal":       {"var", "_internal", false},
		"PUBLIC_NAME":     {"var", "PUBLIC_NAME", true},
		"hello":           {"function", "hello", true},
		"_helper":         {"function", "_helper", false},
		"add":             {"function", "add", true},
		"Widget":          {"type", "Widget", true},
		"_PrivateWidget":  {"type", "_PrivateWidget", false},
		"Container":       {"type", "Container", true},
	}
	wantMethods := map[string]want{
		"__init__":         {"method", "Container.__init__", false},
		"add":              {"method", "Container.add", true},
		"_internal_helper": {"method", "Container._internal_helper", false},
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
		{"hello:function", "def hello(name)"},
		{"_helper:function", "def _helper()"},
		{"add:function", "def add(a, b, *rest, **opts)"},
		{"Widget:type", "class Widget"},
		{"_PrivateWidget:type", "class _PrivateWidget(Widget)"},
		{"Container.__init__:method", "def __init__(self, name)"},
		{"Container.add:method", "def add(self, item)"},
		{"VERSION:var", "var VERSION"},
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
// surfacing. gpython's raw error is a multi-line Python-style traceback;
// we collapse it so the CLI can render one line per failed file.
func TestExtractPython_ParseErrorIsSingleLine(t *testing.T) {
	t.Parallel()
	// `dict[str, int]` is PEP 585 generic alias syntax (Python 3.9+) —
	// gpython rejects it with a SyntaxError. Using this gives the test a
	// realistic modern-Python failure mode to capture.
	_, err := ExtractPython("x.py", []byte("x: dict[str, int] = {}\n"))
	if err == nil {
		t.Fatal("expected parse error on modern-Python annotation")
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
