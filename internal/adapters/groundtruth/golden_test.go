package groundtruth_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// updateGolden, when set via `go test -update`, rewrites the
// expected/*.json files with whatever the detector currently
// produces. Run this after an intentional pattern change to
// regenerate the regression net. The flag is local to this file so
// it doesn't interfere with other package flags.
var updateGolden = flag.Bool("update", false, "rewrite expected/*.json golden files")

// goldenSetup initializes a GroundTruthAdapter against the shared
// fixtures tree under testdata/golden/fixtures/. The fixtures are
// copied into a tempdir under .leonard/ground-truth/ so the adapter
// finds them by the standard path.
func goldenSetup(t *testing.T) *groundtruth.GroundTruthAdapter {
	t.Helper()
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join("testdata", "golden", "fixtures")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter)
}

// TestDetect_Golden walks testdata/golden/inputs/*.txt, runs the
// detector against each input using the shared fixtures, and
// compares the JSON output to the matching expected/*.json file.
//
// The test catches pattern drift across iterations: any change in
// pattern set, lookup logic, or output shape that affects detection
// fires the corresponding golden case. Run with `go test -update`
// to refresh the golden files after an intentional change.
//
// JSON is marshaled with indent for human-readable diffs and ends
// in a trailing newline so the file roundtrips through `cat` /
// editors cleanly.
func TestDetect_Golden(t *testing.T) {
	a := goldenSetup(t)

	inputsDir := filepath.Join("testdata", "golden", "inputs")
	expectedDir := filepath.Join("testdata", "golden", "expected")

	entries, err := os.ReadDir(inputsDir)
	if err != nil {
		t.Fatalf("read inputs: %v", err)
	}

	if *updateGolden {
		if err := os.MkdirAll(expectedDir, 0o755); err != nil {
			t.Fatalf("mkdir expected: %v", err)
		}
	}

	cases := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(inputsDir, e.Name()))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}

			out := a.VerifyClaimForTest(groundtruth.VerifyClaimInput{Text: string(input)})

			gotJSON, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			gotJSON = append(gotJSON, '\n')

			expectedPath := filepath.Join(expectedDir, name+".json")
			if *updateGolden {
				if err := os.WriteFile(expectedPath, gotJSON, 0o644); err != nil {
					t.Fatalf("write expected: %v", err)
				}
				t.Logf("updated %s", expectedPath)
				return
			}

			wantJSON, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected (run with -update to regenerate): %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
					name, string(wantJSON), string(gotJSON))
			}
		})
		cases++
	}

	if cases == 0 {
		t.Fatal("no input cases found under testdata/golden/inputs/")
	}
}
