package parse

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireTreesitterExtractor skips when the cargo crate hasn't been
// built yet. Mirrors requireRustExtractor — CI without cargo or a
// fresh checkout shouldn't fail; just skip.
func requireTreesitterExtractor(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("can't locate test file")
	}
	candidate := filepath.Join(filepath.Dir(here), "treesitter", "target", "release", defaultTreesitterExtractorName)
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	if _, err := os.Stat(candidate); err != nil {
		t.Skipf("treesitter extractor not built; run `cargo build --release` in internal/parse/treesitter/: %v", err)
	}
	return candidate
}

const sampleJavaSrc = `package com.example;

/** Renders a greeting. */
public class Greeter {
    private final String prefix;

    public Greeter(String prefix) {
        this.prefix = prefix;
    }

    public String hello(String name) {
        return prefix + " " + name;
    }

    private void log(String msg) {
        System.err.println(msg);
    }
}

interface Logger {
    void log(String msg);
}

enum Mode {
    QUIET, LOUD;
}

record Point(int x, int y) {}
`

// TestExtractJava_Symbols pins the v0.19 cross-cutting contract: the
// tree-sitter dispatcher correctly identifies every Java declaration
// kind Leonard's symbol model cares about, and the JSON shape matches
// the syn extractor's shape exactly.
func TestExtractJava_Symbols(t *testing.T) {
	requireTreesitterExtractor(t)
	syms, err := ExtractJava("Greeter.java", []byte(sampleJavaSrc))
	if err != nil {
		t.Fatalf("ExtractJava: %v", err)
	}
	// Index by (qname, kind) — Java's class+constructor share a bare
	// name, so multiple symbols can legitimately produce the same
	// qname. v0.20+ will refine method qnames to include the
	// enclosing class.
	type symKey struct{ qname, kind string }
	have := map[symKey]bool{}
	for _, s := range syms {
		if s.FilePath != "Greeter.java" {
			t.Errorf("FilePath = %q, want Greeter.java", s.FilePath)
		}
		if s.StartLine <= 0 || s.EndLine < s.StartLine {
			t.Errorf("%s: bad line range %d-%d", s.Name, s.StartLine, s.EndLine)
		}
		have[symKey{s.QualifiedName, s.Kind}] = true
	}

	wantTypes := []symKey{
		{"Greeter.Greeter", "type"},      // class
		{"Greeter.Logger", "interface"},  // package-private interface
		{"Greeter.Mode", "type"},         // enum
		{"Greeter.Point", "type"},        // record
	}
	for _, w := range wantTypes {
		if !have[w] {
			t.Errorf("missing %s/%s", w.qname, w.kind)
		}
	}

	// Methods (constructor counts as a method) are extracted under
	// the file-module qname. Java's class.method nesting layer is
	// flattened in v0.19 — matches the v0.5 Rust extractor's shape
	// of method qnames not including the file-module twice.
	wantMethodNames := []string{"hello", "log"}
	for _, name := range wantMethodNames {
		var found bool
		for _, s := range syms {
			if s.Kind == "method" && s.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing method %s", name)
		}
	}
}

func TestExtractJava_ParseError(t *testing.T) {
	requireTreesitterExtractor(t)
	// Tree-sitter is error-tolerant — it produces a partial tree for
	// malformed input rather than failing outright. So for v0.19 we
	// just confirm that even on garbage input the extractor doesn't
	// crash; it returns whatever it can parse.
	_, err := ExtractJava("bad.java", []byte("class { ! @ #\n"))
	if err != nil {
		// Acceptable: tree-sitter rejected this entirely.
		if !strings.Contains(err.Error(), "ParseError") {
			t.Errorf("unexpected error shape: %v", err)
		}
	}
}

func TestExtractJava_UnknownLanguage(t *testing.T) {
	requireTreesitterExtractor(t)
	_, err := ExtractTreeSitter("not-a-real-language", "x.foo", []byte("nope\n"))
	if err == nil {
		t.Fatal("expected unknown-language error")
	}
	if !strings.Contains(err.Error(), "unknown language") {
		t.Errorf("error should mention unknown language: %v", err)
	}
}

func TestExtractJava_HonorsEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	stub := filepath.Join(tmp, "stub-ts.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null\necho '[]'\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("LEONARD_TREESITTER_EXTRACTOR", stub)
	syms, err := ExtractJava("ignored.java", []byte("class Foo {}\n"))
	if err != nil {
		t.Fatalf("ExtractJava with env override: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("stub returns [], got %v", syms)
	}
}

func TestExtractJava_TimeoutKillsHungHelper(t *testing.T) {
	tmp := t.TempDir()
	hang := filepath.Join(tmp, "hang.sh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("write hang: %v", err)
	}
	t.Setenv("LEONARD_TREESITTER_EXTRACTOR", hang)
	old := treesitterTimeout
	treesitterTimeout = 200 * time.Millisecond
	t.Cleanup(func() { treesitterTimeout = old })

	start := time.Now()
	_, err := ExtractJava("x.java", []byte("class X {}\n"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got %q", err.Error())
	}
	if elapsed > time.Second {
		t.Errorf("timeout took %v, want under 1s (limit %v)", elapsed, treesitterTimeout)
	}
}
