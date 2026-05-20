package parse

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// findRepoRoot walks up from this test file until it finds go.mod, then
// returns that directory. Lets benchmarks reference real source files
// without hard-coding paths relative to the test cwd.
func findRepoRoot(b *testing.B) string {
	b.Helper()
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			b.Fatalf("findRepoRoot: no go.mod found above %s", filepath.Dir(here))
		}
		dir = parent
	}
}

func readFile(b *testing.B, path string) []byte {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read %s: %v", path, err)
	}
	return data
}

// BenchmarkExtractGo measures parse + symbol extraction on the largest
// Go file in the repo (internal/parse/typescript.go ~1500 LOC). This is
// the realistic upper end of "one file" the post-edit hook will see on
// this codebase — most are smaller.
func BenchmarkExtractGo(b *testing.B) {
	root := findRepoRoot(b)
	path := filepath.Join(root, "internal", "parse", "typescript.go")
	src := readFile(b, path)
	b.ResetTimer()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := ExtractGo(path, src); err != nil {
			b.Fatalf("ExtractGo: %v", err)
		}
	}
}

// BenchmarkExtractTypeScript on a fixture-sized TS file. The dogfood corpus
// (Zod's v3/types.ts) is the realistic large case (~3k lines) but it's not
// vendored — this is the smallest meaningful number, scale roughly linearly.
func BenchmarkExtractTypeScript(b *testing.B) {
	root := findRepoRoot(b)
	path := filepath.Join(root, "testdata", "typescript", "module.ts")
	src := readFile(b, path)
	b.ResetTimer()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := ExtractTypeScript(path, src); err != nil {
			b.Fatalf("ExtractTypeScript: %v", err)
		}
	}
}

// BenchmarkExtractPython on the fixture. gpython is the parser under
// test until v0.2's tree-sitter swap; we want a baseline number to
// compare against post-swap.
func BenchmarkExtractPython(b *testing.B) {
	root := findRepoRoot(b)
	path := filepath.Join(root, "testdata", "python", "module.py")
	src := readFile(b, path)
	b.ResetTimer()
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		if _, err := ExtractPython(path, src); err != nil {
			b.Fatalf("ExtractPython: %v", err)
		}
	}
}
