package parse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/store"
)

// defaultTreesitterExtractorName is the basename of the multi-language
// tree-sitter helper compiled from internal/parse/treesitter/. One
// binary handles every tree-sitter language Leonard supports — the
// `--lang <name>` arg picks the grammar. The same pattern as the syn
// extractor: build once via `cargo build --release`, override the
// path via LEONARD_TREESITTER_EXTRACTOR.
const defaultTreesitterExtractorName = "leonard-extract-treesitter"

// treesitterTimeout caps the subprocess. Matches the syn extractor
// budget — tree-sitter parsing itself is faster than syn, but
// subprocess startup + grammar load is the bottleneck.
var treesitterTimeout = 30 * time.Second

// treesitterExtractorOverride is set in tests; production reads the
// LEONARD_TREESITTER_EXTRACTOR env var. Mirrors rustExtractorOverride.
var treesitterExtractorOverride string

// ErrTreesitterExtractorUnavailable surfaces when the helper binary
// isn't reachable. Indexer surfaces this as a per-file ParseFailure
// so one bad language file doesn't tank the whole walk.
var ErrTreesitterExtractorUnavailable = errors.New(
	"parse: leonard-extract-treesitter binary not found " +
		"(build via `cargo build --release` inside internal/parse/treesitter/, " +
		"set LEONARD_TREESITTER_EXTRACTOR, or install the binary on PATH)",
)

// treesitterSymbolRecord mirrors the JSON shape emitted by
// internal/parse/treesitter/src/main.rs. Identical shape to the syn
// extractor's record — both produce the same Symbol wire format.
type treesitterSymbolRecord struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Exported      bool   `json:"exported"`
}

// treesitterExtractorPath returns the path to the helper binary using
// the same precedence rules as the syn extractor: explicit test
// override, then $LEONARD_TREESITTER_EXTRACTOR, then PATH, then the
// cargo target dir inside the source tree.
func treesitterExtractorPath() (string, error) {
	if treesitterExtractorOverride != "" {
		return treesitterExtractorOverride, nil
	}
	if env := strings.TrimSpace(os.Getenv("LEONARD_TREESITTER_EXTRACTOR")); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", ErrTreesitterExtractorUnavailable
		}
		return env, nil
	}
	if bin, err := exec.LookPath(defaultTreesitterExtractorName); err == nil {
		return bin, nil
	}
	if _, here, _, ok := runtime.Caller(0); ok {
		candidate := filepath.Join(filepath.Dir(here), "treesitter", "target", "release", defaultTreesitterExtractorName)
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", ErrTreesitterExtractorUnavailable
}

// ExtractTreeSitter extracts symbols for any language the helper
// binary supports. The `lang` argument selects the tree-sitter
// grammar (passed to the helper's --lang flag). `path` is the
// indexer-relative path used to derive the module qualifier;
// `src` is the file content.
//
// New languages light up here for free once added to the Rust crate
// and registered in langExtractors below (see ExtractJava etc.).
func ExtractTreeSitter(lang, path string, src []byte) ([]store.Symbol, error) {
	bin, err := treesitterExtractorPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), treesitterTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--lang", lang, path)
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Stdin = bytes.NewReader(src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("tree-sitter %s extract timed out after %s", lang, treesitterTimeout)
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			// Exit-code 2 means parse error or unknown language —
			// helper writes a JSON {error,detail} blob to stdout.
			// Surface the detail through ParseFailure rather than
			// failing the whole index pass.
			var pe struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &pe); err == nil && pe.Detail != "" {
				return nil, fmt.Errorf("ParseError: %s", pe.Detail)
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "ParseError"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("tree-sitter extract failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	var records []treesitterSymbolRecord
	if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
		return nil, fmt.Errorf("decode extract-treesitter output: %w", err)
	}
	out := make([]store.Symbol, 0, len(records))
	for _, r := range records {
		out = append(out, store.Symbol{
			FilePath:      path,
			Name:          r.Name,
			QualifiedName: r.QualifiedName,
			Kind:          r.Kind,
			Signature:     r.Signature,
			StartLine:     r.StartLine,
			EndLine:       r.EndLine,
			Exported:      r.Exported,
		})
	}
	return out, nil
}

// Per-language wrappers below let the indexer register
// .ext -> ExtractXxx in langExtractors without exposing the language
// name string at the registration site. Each one is a thin alias for
// ExtractTreeSitter — adding a new tree-sitter language is one
// alias here + one Cargo dep + one Language::lookup arm + one .scm
// query in the Rust crate + one extension in langExtractors.

func ExtractJava(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("java", path, src)
}

func ExtractRuby(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("ruby", path, src)
}

func ExtractCSharp(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("csharp", path, src)
}

func ExtractSwift(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("swift", path, src)
}

func ExtractKotlin(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("kotlin", path, src)
}

func ExtractScala(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("scala", path, src)
}

func ExtractDart(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("dart", path, src)
}

func ExtractC(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("c", path, src)
}

func ExtractCpp(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("cpp", path, src)
}

func ExtractPHP(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("php", path, src)
}

func ExtractLua(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("lua", path, src)
}

func ExtractBash(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("bash", path, src)
}

func ExtractZig(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("zig", path, src)
}

func ExtractNix(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("nix", path, src)
}

func ExtractElixir(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("elixir", path, src)
}

func ExtractSolidity(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("solidity", path, src)
}

func ExtractMake(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("make", path, src)
}

func ExtractCMake(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("cmake", path, src)
}

func ExtractHCL(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("hcl", path, src)
}

func ExtractGraphQL(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("graphql", path, src)
}

func ExtractProto(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("proto", path, src)
}

func ExtractSQL(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("sql", path, src)
}

func ExtractWIT(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("wit", path, src)
}

func ExtractErlang(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("erlang", path, src)
}

func ExtractR(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("r", path, src)
}

func ExtractJust(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("just", path, src)
}

func ExtractStarlark(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("starlark", path, src)
}

func ExtractGLSL(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("glsl", path, src)
}

func ExtractHLSL(path string, src []byte) ([]store.Symbol, error) {
	return ExtractTreeSitter("hlsl", path, src)
}
