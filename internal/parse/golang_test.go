package parse

import (
	"strings"
	"testing"
)

const sampleSrc = `package sample

import "context"

// Greeter says hi.
type Greeter struct {
	Name string
}

type stringer interface {
	String() string
}

type Aliased = Greeter

type Vec[T any] struct{ data []T }

const (
	Pi      = 3.14
	Version = "1.0"
	unexp   = 1
)

var DefaultName = "world"

var x, Y int

func TopLevel(ctx context.Context, s string) (int, error) {
	_ = ctx
	_ = s
	return 0, nil
}

func unexportedFunc() {}

func (g Greeter) Hello() string  { return "hi " + g.Name }
func (g *Greeter) SetName(n string) { g.Name = n }

func (v *Vec[T]) Push(x T) { v.data = append(v.data, x) }
`

func TestExtractGo_Symbols(t *testing.T) {
	t.Parallel()

	syms, err := ExtractGo("sample.go", []byte(sampleSrc))
	if err != nil {
		t.Fatalf("ExtractGo: %v", err)
	}

	type want struct {
		kind     string
		qname    string
		exported bool
	}
	expect := map[string]want{
		"Greeter":         {"type", "sample.Greeter", true},
		"stringer":        {"interface", "sample.stringer", false},
		"Aliased":         {"type", "sample.Aliased", true},
		"Vec":             {"type", "sample.Vec", true},
		"Pi":              {"const", "sample.Pi", true},
		"Version":         {"const", "sample.Version", true},
		"unexp":           {"const", "sample.unexp", false},
		"DefaultName":     {"var", "sample.DefaultName", true},
		"x":               {"var", "sample.x", false},
		"Y":               {"var", "sample.Y", true},
		"TopLevel":        {"function", "sample.TopLevel", true},
		"unexportedFunc":  {"function", "sample.unexportedFunc", false},
	}
	wantMethods := map[string]want{
		"Hello":   {"method", "sample.Greeter.Hello", true},
		"SetName": {"method", "sample.Greeter.SetName", true},
		"Push":    {"method", "sample.Vec.Push", true},
	}

	got := map[string]want{}
	for _, s := range syms {
		if s.FilePath != "sample.go" {
			t.Errorf("FilePath = %q, want sample.go", s.FilePath)
		}
		if s.StartLine <= 0 || s.EndLine < s.StartLine {
			t.Errorf("%s: bad line range %d-%d", s.Name, s.StartLine, s.EndLine)
		}
		key := s.Name
		if s.Kind == "method" {
			key = "M:" + s.Name
		}
		got[key] = want{s.Kind, s.QualifiedName, s.Exported}
	}

	for name, w := range expect {
		g, ok := got[name]
		if !ok {
			t.Errorf("missing symbol %q", name)
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

func TestExtractGo_Signatures(t *testing.T) {
	t.Parallel()

	syms, err := ExtractGo("sample.go", []byte(sampleSrc))
	if err != nil {
		t.Fatalf("ExtractGo: %v", err)
	}

	byName := map[string]string{}
	for _, s := range syms {
		byName[s.Name+":"+s.Kind] = s.Signature
	}

	cases := []struct {
		key  string
		want string
	}{
		{"TopLevel:function", "func TopLevel(ctx context.Context, s string) (int, error)"},
		{"unexportedFunc:function", "func unexportedFunc()"},
		{"Hello:method", "func (Greeter) Hello() string"},
		{"SetName:method", "func (*Greeter) SetName(n string)"},
		{"Push:method", "func (*Vec[T]) Push(x T)"},
		{"Greeter:type", "type Greeter struct{...}"},
		{"stringer:interface", "type stringer interface{...}"},
		{"Aliased:type", "type Aliased = Greeter"},
		{"Pi:const", "const Pi"},
		{"DefaultName:var", "var DefaultName"},
	}
	for _, c := range cases {
		got, ok := byName[c.key]
		if !ok {
			t.Errorf("no symbol %s", c.key)
			continue
		}
		if !strings.Contains(got, strings.TrimSuffix(c.want, "...}")) {
			t.Errorf("%s signature: got %q, want contains %q", c.key, got, c.want)
		}
	}
}

func TestExtractGo_SkipsBlankIdent(t *testing.T) {
	t.Parallel()
	src := `package p
var _ = 1
var Real = 2
`
	syms, err := ExtractGo("p.go", []byte(src))
	if err != nil {
		t.Fatalf("ExtractGo: %v", err)
	}
	for _, s := range syms {
		if s.Name == "_" {
			t.Fatalf("did not expect blank identifier symbol")
		}
	}
	found := false
	for _, s := range syms {
		if s.Name == "Real" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Real var to be extracted")
	}
}

func TestExtractGo_ParseError(t *testing.T) {
	t.Parallel()
	_, err := ExtractGo("bad.go", []byte("package p\nfunc {"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}
