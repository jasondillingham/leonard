package parse

import (
	"bytes"
	"testing"
)

// Fuzz seeds chosen to exercise the three HIGH-class bugs from bughunt-1 plus
// a generic baseline. Each is a real-world TS snippet that has historically
// (or would have plausibly) tripped the hand-rolled scanner. Run with:
//
//	go test ./internal/parse/ -run=^$ -fuzz=FuzzStripTSLiterals -fuzztime=30s
//	go test ./internal/parse/ -run=^$ -fuzz=FuzzExtractTypeScript -fuzztime=30s
var tsFuzzSeeds = [][]byte{
	[]byte(sampleTypeScriptSrc),
	// H1 territory: regex literal containing `}`. A misclassified `/` would
	// treat the regex body as code and the brace as a body-closer, hoisting
	// nested symbols and truncating outer scope.
	[]byte("const r = /\\}/g;\nfunction leak() { return 1; }\n"),
	// H2 territory: TS 4.4+ static initializer block — must be tracked as a
	// class-internal block, not the end of the class.
	[]byte("class C {\n  static { console.log('init') }\n  method() {}\n}\n"),
	// H3 territory: decorator on a class member.
	[]byte("class C {\n  @log foo() {}\n  bar() {}\n}\n"),
	// Template literals nested with expressions containing braces.
	[]byte("const s = `a${ {x:1}.x }b`;\nexport function f() {}\n"),
	// Empty input, ASCII edge cases, lone delimiters.
	{},
	[]byte("/"),
	[]byte("//"),
	[]byte("/*"),
	[]byte("`${"),
}

// FuzzStripTSLiterals asserts that the literal-stripping pass:
//  1. never panics,
//  2. preserves byte length (the contract of stripTSLiterals),
//  3. preserves every newline at the same offset (so line numbers remain
//     valid after stripping).
//
// These are the load-bearing invariants the rest of the parser depends on.
func FuzzStripTSLiterals(f *testing.F) {
	for _, seed := range tsFuzzSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		out, err := stripTSLiterals(src)
		if err != nil {
			// Legitimate unterminated-literal errors are fine; the parser
			// reports them up the chain rather than crashing.
			return
		}
		if len(out) != len(src) {
			t.Fatalf("length changed: %d → %d", len(src), len(out))
		}
		for i, c := range src {
			if c == '\n' && out[i] != '\n' {
				t.Fatalf("newline at offset %d not preserved (got %q)", i, out[i])
			}
		}
	})
}

// FuzzExtractTypeScript asserts that the public entry point:
//  1. never panics on arbitrary input,
//  2. never returns a symbol whose line range falls outside the source —
//     a symbol claiming a phantom line is the exact failure shape of H1
//     (fabricated symbols hoisted from inside a regex literal).
func FuzzExtractTypeScript(f *testing.F) {
	for _, seed := range tsFuzzSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		syms, err := ExtractTypeScript("fuzz.ts", src)
		if err != nil {
			return
		}
		// One past the last byte counts as a line in our convention; an
		// extractor reporting a line beyond that is fabricating location.
		maxLine := bytes.Count(src, []byte{'\n'}) + 1
		for _, s := range syms {
			if s.StartLine < 1 || s.StartLine > maxLine {
				t.Fatalf("StartLine %d out of [1, %d] for %q in %q",
					s.StartLine, maxLine, s.QualifiedName, src)
			}
			if s.EndLine < s.StartLine || s.EndLine > maxLine {
				t.Fatalf("EndLine %d out of [%d, %d] for %q in %q",
					s.EndLine, s.StartLine, maxLine, s.QualifiedName, src)
			}
			if s.Name == "" {
				t.Fatalf("empty symbol name (qname=%q kind=%q) in %q",
					s.QualifiedName, s.Kind, src)
			}
		}
	})
}
