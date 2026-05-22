package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

// TestLoad_PopulatesAdapterRaw confirms that [[adapters]] blocks
// with extra fields beyond `type` populate AdapterConfig.Raw so
// the dispatcher can hand per-adapter config to Init.
func TestLoad_PopulatesAdapterRaw(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	body := `[[adapters]]
type = "ground-truth"
truth_dir = "source-of-truth/"
verify_targets = ["*.md", "*.yaml"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Adapters) != 1 {
		t.Fatalf("want 1 adapter, got %d", len(cfg.Adapters))
	}
	a := cfg.Adapters[0]
	if a.Type != "ground-truth" {
		t.Errorf("Type: want ground-truth, got %q", a.Type)
	}
	if a.Raw == nil {
		t.Fatal("Raw should be populated, got nil")
	}
	if got, want := a.Raw["truth_dir"], "source-of-truth/"; got != want {
		t.Errorf("Raw[truth_dir]: want %q, got %v", want, got)
	}
	wantTargets := []any{"*.md", "*.yaml"}
	if got, ok := a.Raw["verify_targets"].([]any); !ok || !reflect.DeepEqual(got, wantTargets) {
		t.Errorf("Raw[verify_targets]: want %v, got %v", wantTargets, a.Raw["verify_targets"])
	}
	if _, present := a.Raw["type"]; present {
		t.Errorf("Raw should NOT include 'type' (Type field captures it), got %v", a.Raw)
	}
}
