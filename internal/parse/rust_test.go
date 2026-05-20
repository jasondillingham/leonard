package parse

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireRustExtractor skips the test when the syn-based helper isn't
// built yet (CI sandbox without cargo, or a fresh checkout where
// nobody's run `cargo build --release` inside internal/parse/rust/).
// Returns the source-tree-rooted path so tests can probe it directly.
func requireRustExtractor(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("can't locate test file")
	}
	candidate := filepath.Join(filepath.Dir(here), "rust", "target", "release", defaultRustExtractorName)
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	if _, err := os.Stat(candidate); err != nil {
		t.Skipf("rust extractor not built; run `cargo build --release` in internal/parse/rust/: %v", err)
	}
	return candidate
}

const sampleRustSrc = `use std::collections::HashMap;

pub const VERSION: &str = "0.1";
const HIDDEN: u32 = 42;
pub static FLAG: bool = true;

pub fn hello(name: &str) -> String {
    format!("hello {}", name)
}

fn _helper() -> u32 { 1 }

pub async fn fetch_all(urls: Vec<String>) -> Vec<String> { urls }

pub struct Container<T> {
    pub items: Vec<T>,
}

impl<T: Clone> Container<T> {
    pub fn new(items: Vec<T>) -> Self {
        Self { items }
    }

    pub async fn load(_url: &str) -> Result<Self, std::io::Error> {
        unimplemented!()
    }

    fn _refresh(&self) -> usize { self.items.len() }
}

pub struct _Private;

pub enum Outcome<T> {
    Ok(T),
    Err(String),
}

pub trait Display {
    fn show(&self) -> String;
}

pub type Bag<T> = HashMap<String, T>;
`

func TestExtractRust_Symbols(t *testing.T) {
	requireRustExtractor(t)

	syms, err := ExtractRust("sample.rs", []byte(sampleRustSrc))
	if err != nil {
		t.Fatalf("ExtractRust: %v", err)
	}

	type want struct {
		kind     string
		qname    string
		exported bool
	}
	expect := map[string]want{
		"VERSION":     {"const", "sample.VERSION", true},
		"HIDDEN":      {"const", "sample.HIDDEN", false},
		"FLAG":        {"var", "sample.FLAG", true},
		"hello":       {"function", "sample.hello", true},
		"_helper":     {"function", "sample._helper", false},
		"fetch_all":   {"function", "sample.fetch_all", true},
		"Container":   {"type", "sample.Container", true},
		"_Private":    {"type", "sample._Private", true},
		"Outcome":     {"type", "sample.Outcome", true},
		"Display":     {"interface", "sample.Display", true},
		"Bag":         {"type", "sample.Bag", true},
	}
	wantMethods := map[string]want{
		"new":      {"method", "sample.Container.new", true},
		"load":     {"method", "sample.Container.load", true},
		"_refresh": {"method", "sample.Container._refresh", false},
	}

	got := map[string]want{}
	for _, s := range syms {
		if s.FilePath != "sample.rs" {
			t.Errorf("FilePath = %q, want sample.rs", s.FilePath)
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
			t.Errorf("missing symbol %q (kind %s); have %v", name, w.kind, keys(got))
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

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExtractRust_Signatures(t *testing.T) {
	requireRustExtractor(t)

	syms, err := ExtractRust("sample.rs", []byte(sampleRustSrc))
	if err != nil {
		t.Fatalf("ExtractRust: %v", err)
	}
	byKey := map[string]string{}
	for _, s := range syms {
		byKey[s.QualifiedName+":"+s.Kind] = s.Signature
	}
	cases := []struct {
		key, want string
	}{
		{"sample.hello:function", "fn hello(name)"},
		{"sample.fetch_all:function", "async fn fetch_all(urls)"},
		{"sample.Container:type", "struct Container"},
		{"sample.Container.new:method", "fn new(items)"},
		{"sample.Container.load:method", "async fn load("},
		{"sample.Outcome:type", "enum Outcome"},
		{"sample.Display:interface", "trait Display"},
		{"sample.Bag:type", "type Bag"},
		{"sample.VERSION:const", "const VERSION"},
		{"sample.FLAG:var", "static FLAG"},
	}
	for _, c := range cases {
		got, ok := byKey[c.key]
		if !ok {
			t.Errorf("no symbol %s; have %v", c.key, keys(byKey))
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s signature: got %q, want contains %q", c.key, got, c.want)
		}
	}
}

func TestExtractRust_ParseError(t *testing.T) {
	requireRustExtractor(t)
	_, err := ExtractRust("bad.rs", []byte("fn /\n"))
	if err == nil {
		t.Fatal("expected parse error")
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("parse error should be single-line for CLI surfacing: %q", msg)
	}
	if !strings.Contains(msg, "ParseError") {
		t.Errorf("expected ParseError in message, got %q", msg)
	}
}

func TestExtractRust_HonorsEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub-rust.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\necho '[]'\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("LEONARD_RUST_EXTRACTOR", stub)
	syms, err := ExtractRust("ignored.rs", []byte("fn whatever() {}\n"))
	if err != nil {
		t.Fatalf("ExtractRust with env override: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("stub returns [], got %v", syms)
	}
}

func TestExtractRust_TimeoutKillsHungHelper(t *testing.T) {
	tmp := t.TempDir()
	hang := filepath.Join(tmp, "hang.sh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("write hang: %v", err)
	}
	t.Setenv("LEONARD_RUST_EXTRACTOR", hang)
	old := rustTimeout
	rustTimeout = 200 * time.Millisecond
	t.Cleanup(func() { rustTimeout = old })

	start := time.Now()
	_, err := ExtractRust("x.rs", []byte("fn x() {}\n"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got %q", err.Error())
	}
	if elapsed > time.Second {
		t.Errorf("timeout took %v, want under 1s (limit %v)", elapsed, rustTimeout)
	}
}

func TestExtractRust_MissingExtractorReturnsUnavailable(t *testing.T) {
	t.Setenv("LEONARD_RUST_EXTRACTOR", "/definitely/not/a/real/path")
	_, err := ExtractRust("x.rs", []byte("fn x() {}\n"))
	if err != ErrRustExtractorUnavailable {
		// LookPath of an absolute non-existent path doesn't normally
		// fall through to source-tree fallback because we set the env
		// var explicitly. Anything other than the sentinel means our
		// precedence logic is wrong.
		t.Errorf("expected ErrRustExtractorUnavailable, got %v", err)
	}
}

// TestExtractRust_NonPathSelfType covers bughunt-3 rust F1. Methods
// on `impl X for &Foo`, `impl X for (i32, i32)`, etc. used to be
// silently dropped because visit_item_impl only handled Type::Path.
func TestExtractRust_NonPathSelfType(t *testing.T) {
	requireRustExtractor(t)
	src := `
pub trait Display { fn fmt(&self) -> String; }
pub struct Container;

impl Display for &Container {
    fn fmt(&self) -> String { String::new() }
}

impl Display for (i32, i32) {
    fn fmt(&self) -> String { String::new() }
}

impl Display for [u8; 4] {
    fn fmt(&self) -> String { String::new() }
}
`
	syms, err := ExtractRust("nonpath.rs", []byte(src))
	if err != nil {
		t.Fatalf("ExtractRust: %v", err)
	}
	byQName := map[string]bool{}
	for _, s := range syms {
		byQName[s.QualifiedName] = true
	}
	// Each impl block must contribute its method, distinguished
	// by the synthetic impl-target name.
	for _, want := range []string{
		"nonpath.&Container.fmt",
		"nonpath._tuple.fmt",
		"nonpath._array.fmt",
	} {
		if !byQName[want] {
			t.Errorf("missing method %q — non-Path self_ty regression", want)
		}
	}
}

// TestExtractRust_ForeignTypeImplPreservesPath covers bughunt-3 rust
// F3. Multi-segment self_ty paths used to keep only the last
// segment, so `impl Display for std::collections::HashMap` collided
// with a local-type `HashMap`. The full path is now joined into the
// impl target name so the qnames stay distinct.
func TestExtractRust_ForeignTypeImplPreservesPath(t *testing.T) {
	requireRustExtractor(t)
	src := `
pub trait Display { fn fmt(&self) -> String; }
pub struct HashMap;

impl Display for HashMap {
    fn fmt(&self) -> String { String::new() }
}

impl Display for std::collections::HashMap<String, u32> {
    fn fmt(&self) -> String { String::new() }
}
`
	syms, err := ExtractRust("collision.rs", []byte(src))
	if err != nil {
		t.Fatalf("ExtractRust: %v", err)
	}
	byQName := map[string]bool{}
	for _, s := range syms {
		byQName[s.QualifiedName] = true
	}
	if !byQName["collision.HashMap.fmt"] {
		t.Errorf("local HashMap.fmt missing")
	}
	if !byQName["collision.std::collections::HashMap.fmt"] {
		t.Errorf("foreign HashMap.fmt should preserve full path, found: %v", byQName)
	}
}

// TestExtractRust_StartLineSkipsAttributes covers bughunt-3 rust F2.
// Python/TS strip leading attributes and doc-comments from start_line;
// previously Rust included them, producing cross-language inconsistency.
func TestExtractRust_StartLineSkipsAttributes(t *testing.T) {
	requireRustExtractor(t)
	// Lines:
	//   1: /// doc comment
	//   2: /// continued
	//   3: #[derive(Debug)]
	//   4: pub struct Token {
	//   5:     name: String,
	//   6: }
	// The struct's start_line should be 4, NOT 1.
	src := "/// doc comment\n/// continued\n#[derive(Debug)]\npub struct Token {\n    name: String,\n}\n"
	syms, err := ExtractRust("attrs.rs", []byte(src))
	if err != nil {
		t.Fatalf("ExtractRust: %v", err)
	}
	var tok *struct{ start, end int }
	for _, s := range syms {
		if s.Name == "Token" {
			tok = &struct{ start, end int }{s.StartLine, s.EndLine}
		}
	}
	if tok == nil {
		t.Fatal("Token symbol missing")
	}
	if tok.start != 4 {
		t.Errorf("Token start_line = %d, want 4 (skip attributes + docs)", tok.start)
	}
}
