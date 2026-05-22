package groundtruth_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// getProjectRoot pulls the project root out of an adapter by
// reflecting on the Detect-accessible state — we don't expose
// projectRoot publicly, but the tests need it to write fixtures.
//
// Implementation: use the Facts().Path which carries the absolute
// path to the truth dir; back out two levels.
func getProjectRoot(t *testing.T, a *groundtruth.GroundTruthAdapter) string {
	t.Helper()
	facts := a.Facts()
	if facts == nil || facts.Path == "" {
		t.Fatal("adapter has no Facts.Path; can't derive project root")
	}
	// Facts.Path is <root>/.leonard/ground-truth/facts.yaml. Walk
	// up three levels (file → ground-truth → .leonard → root).
	return filepath.Dir(filepath.Dir(filepath.Dir(facts.Path)))
}

func writeFileBytes(t *testing.T, root, rel, content string) {
	t.Helper()
	dest := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func pathJoin(parts ...string) string {
	return filepath.Join(parts...)
}

// Defensive: ensure the type-assertion helper test relies on isn't
// shadowed by another package. Pulling reflect imports via a
// trivially-used reference keeps it from being unused if the helpers
// shift around.
var _ = reflect.TypeOf
