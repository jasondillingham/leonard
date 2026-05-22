package dispatcher_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/dispatcher"
)

func TestLoadEnabled_AutoDetectsCodeFromGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	out, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &stderr)
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer out.Close()

	if len(out.Adapters) == 0 {
		t.Fatalf("expected at least one adapter to load, got 0 (stderr=%s)", stderr.String())
	}
	var sawCode bool
	for _, a := range out.Adapters {
		if a.Name() == "code" {
			sawCode = true
		}
	}
	if !sawCode {
		names := names(out.Adapters)
		t.Errorf("expected code adapter, got %v (stderr=%s)", names, stderr.String())
	}
}

func TestLoadEnabled_GroundTruthFromExistingTree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dataDir := filepath.Join(root, ".leonard")
	gtDir := filepath.Join(dataDir, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimum-viable truth tree so the adapter Init succeeds.
	for name, body := range map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# Rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stderr bytes.Buffer
	out, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &stderr)
	if err != nil {
		t.Fatalf("LoadEnabled: %v (stderr=%s)", err, stderr.String())
	}
	defer out.Close()

	var sawGroundTruth bool
	for _, a := range out.Adapters {
		if a.Name() == "ground-truth" {
			sawGroundTruth = true
		}
	}
	if !sawGroundTruth {
		t.Errorf("ground-truth not loaded; got %v (stderr=%s)", names(out.Adapters), stderr.String())
	}
}

func TestLoadEnabled_ExplicitConfigBlocks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dataDir := filepath.Join(root, ".leonard")
	gtDir := filepath.Join(dataDir, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# Rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Write config.toml with only ground-truth enabled.
	cfg := `[[adapters]]
type = "ground-truth"
`
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	out, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &stderr)
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer out.Close()
	if len(out.Adapters) != 1 || out.Adapters[0].Name() != "ground-truth" {
		t.Errorf("explicit config: want [ground-truth], got %v", names(out.Adapters))
	}
}

func TestLoadEnabled_CloseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestLoadEnabled_UnknownAdapterSkippedWithLog(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `[[adapters]]
type = "no-such-adapter"
`
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	out, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &stderr)
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer out.Close()
	if len(out.Adapters) != 0 {
		t.Errorf("unknown adapter should be skipped, got %v", names(out.Adapters))
	}
	if !strings.Contains(stderr.String(), "no-such-adapter") {
		t.Errorf("stderr should mention the skipped adapter, got %q", stderr.String())
	}
}

func names(as []adapters.Adapter) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Name()
	}
	return out
}
