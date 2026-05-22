package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

func TestEnabledAdapters_ExplicitListWins(t *testing.T) {
	cfg := &config.Config{
		Adapters: []config.AdapterConfig{
			{Type: "ground-truth"},
			{Type: "code"},
		},
	}
	got := cfg.EnabledAdapters(t.TempDir())
	want := []string{"ground-truth", "code"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("explicit order: want %v, got %v", want, got)
	}
}

func TestEnabledAdapters_DedupesExplicitList(t *testing.T) {
	cfg := &config.Config{
		Adapters: []config.AdapterConfig{
			{Type: "code"},
			{Type: "code"},
			{Type: "ground-truth"},
		},
	}
	got := cfg.EnabledAdapters(t.TempDir())
	want := []string{"code", "ground-truth"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedup: want %v, got %v", want, got)
	}
}

func TestEnabledAdapters_AutoDetectsCodeFromGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	got := cfg.EnabledAdapters(root)
	if len(got) != 1 || got[0] != "code" {
		t.Errorf("auto-detect from go.mod: want [code], got %v", got)
	}
}

func TestEnabledAdapters_AutoDetectsGroundTruthFromDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".leonard", "ground-truth"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	got := cfg.EnabledAdapters(root)
	// No go.mod → only ground-truth detected; code is the fallback
	// only when nothing else is enabled.
	if len(got) != 1 || got[0] != "ground-truth" {
		t.Errorf("auto-detect from gt dir: want [ground-truth], got %v", got)
	}
}

func TestEnabledAdapters_BothSignalsBothEnabled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".leonard", "ground-truth"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	got := cfg.EnabledAdapters(root)
	want := []string{"code", "ground-truth"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("both signals: want %v, got %v", want, got)
	}
}

func TestEnabledAdapters_NeitherSignalFallsBackToCode(t *testing.T) {
	// Empty project, no config — still return [code] so the v0.52
	// fabrication-guard path stays loaded.
	cfg := &config.Config{}
	got := cfg.EnabledAdapters(t.TempDir())
	if len(got) != 1 || got[0] != "code" {
		t.Errorf("fallback: want [code], got %v", got)
	}
}

func TestEnabledAdapters_VerifierConfigEnablesCode(t *testing.T) {
	// No go.mod, but post_edit.verify.command set → code enables.
	root := t.TempDir()
	cfg := &config.Config{
		PostEdit: config.PostEditConfig{
			Verify: config.VerifyConfig{Command: "cargo check"},
		},
	}
	got := cfg.EnabledAdapters(root)
	if len(got) != 1 || got[0] != "code" {
		t.Errorf("verifier signal: want [code], got %v", got)
	}
}
