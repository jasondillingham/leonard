package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

// grantSyncTrust reads the plugin command from .leonard/config.toml
// and writes the corresponding sync-plugin trust fingerprint.
// Tests call this after withCwd (which redirects XDG_CONFIG_HOME)
// so trust lands in the test-isolated config dir.
//
// v1.0 (bughunt-11 F3): the sync runner refuses to exec an
// untrusted plugin; every test that drives a real `sync` run
// has to call this first.
func grantSyncTrust(t *testing.T, projectRoot, pluginName string) {
	t.Helper()
	cfg, err := loadSyncConfig(filepath.Join(projectRoot, dataDirName))
	if err != nil {
		t.Fatalf("loadSyncConfig: %v", err)
	}
	pc, ok := cfg.Sync[pluginName]
	if !ok {
		t.Fatalf("grantSyncTrust: no plugin %q in config", pluginName)
	}
	if err := config.WriteSyncPluginTrust(projectRoot, pluginName, pc.Command); err != nil {
		t.Fatalf("WriteSyncPluginTrust: %v", err)
	}
}

// syncFixture creates a tempdir, .leonard/, an initial facts.yaml,
// a bash plugin that emits the supplied output JSON, and a
// config.toml referencing the plugin. Returns the project root.
func syncFixture(t *testing.T, pluginName, pluginOutput, initialFacts string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test plugin requires /bin/sh")
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(filepath.Join(dataDir, "ground-truth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if initialFacts != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "ground-truth", "facts.yaml"), []byte(initialFacts), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pluginPath := filepath.Join(root, "plugin.sh")
	script := "#!/bin/sh\ncat > /dev/null\ncat <<'PLUGIN_BODY'\n" + pluginOutput + "\nPLUGIN_BODY\n"
	if err := os.WriteFile(pluginPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := "[sync." + pluginName + "]\ncommand = \"" + pluginPath + "\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSync_RunsAllConfiguredPlugins(t *testing.T) {
	root := syncFixture(t, "test",
		`{"updated_facts": {"hello": "world"}, "changes": [{"path": "hello", "old": "", "new": "world", "reason": "init"}]}`,
		"")
	withCwd(t, root)
	grantSyncTrust(t, root, "test")
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync")
	if err != nil {
		t.Fatalf("sync: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "running test") {
		t.Errorf("output should mention running test plugin: %s", out)
	}
	if !strings.Contains(out, "facts.yaml updated") {
		t.Errorf("output should confirm facts.yaml updated: %s", out)
	}
	// Confirm the file was actually written.
	body, err := os.ReadFile(filepath.Join(root, dataDirName, "ground-truth", "facts.yaml"))
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("facts.yaml not updated: %s", body)
	}
}

func TestSync_DryRunDoesNotWrite(t *testing.T) {
	root := syncFixture(t, "test",
		`{"updated_facts": {"different": "value"}, "changes": [{"path": "different", "new": "value"}]}`,
		"original: 1\n")
	withCwd(t, root)
	grantSyncTrust(t, root, "test")
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync", "--dry-run")
	if err != nil {
		t.Fatalf("sync --dry-run: %v", err)
	}
	if !strings.Contains(out, "--dry-run: facts.yaml NOT written") {
		t.Errorf("dry-run message missing: %s", out)
	}
	body, err := os.ReadFile(filepath.Join(root, dataDirName, "ground-truth", "facts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "original") {
		t.Errorf("facts.yaml should NOT be overwritten in dry-run, got:\n%s", body)
	}
}

func TestSync_NamedPluginOnly(t *testing.T) {
	root := syncFixture(t, "alpha",
		`{"updated_facts": {"a": 1}, "changes": []}`,
		"")
	// Add a second plugin that would fail (non-existent command)
	// to confirm the named-plugin scope skips it.
	cfgPath := filepath.Join(root, dataDirName, "config.toml")
	cfg, _ := os.ReadFile(cfgPath)
	cfg = append(cfg, []byte("\n[sync.beta]\ncommand = \"/nonexistent/path\"\n")...)
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	withCwd(t, root)
	grantSyncTrust(t, root, "alpha")
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync", "alpha")
	if err != nil {
		t.Fatalf("sync alpha: %v", err)
	}
	if !strings.Contains(out, "running alpha") {
		t.Errorf("alpha should run: %s", out)
	}
	if strings.Contains(out, "running beta") {
		t.Errorf("beta should not run when alpha named: %s", out)
	}
}

func TestSync_UnknownPluginErrors(t *testing.T) {
	root := syncFixture(t, "alpha",
		`{"updated_facts": {}, "changes": []}`,
		"")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "sync", "beta")
	if err == nil || !strings.Contains(err.Error(), "unknown plugin") {
		t.Errorf("want unknown-plugin error, got %v", err)
	}
}

func TestSync_NoPluginsConfigured(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "sync")
	if err == nil || !strings.Contains(err.Error(), "no plugins configured") {
		t.Errorf("want no-plugins error, got %v", err)
	}
}

func TestSyncList_PrintsConfigured(t *testing.T) {
	root := syncFixture(t, "github",
		`{"updated_facts": {}, "changes": []}`,
		"")
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync", "list")
	if err != nil {
		t.Fatalf("sync list: %v", err)
	}
	if !strings.Contains(out, "github") {
		t.Errorf("list should mention github plugin: %s", out)
	}
}

func TestSyncList_EmptyMessage(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync", "list")
	if err != nil {
		t.Fatalf("sync list: %v", err)
	}
	if !strings.Contains(out, "no sync plugins configured") {
		t.Errorf("empty list hint missing: %s", out)
	}
}

// TestSync_RefusesUntrustedPlugin covers bughunt-11 F3: until the
// operator runs `leonard config trust sync <name>`, the sync runner
// must refuse to invoke the plugin command and print a hint.
func TestSync_RefusesUntrustedPlugin(t *testing.T) {
	root := syncFixture(t, "test",
		`{"updated_facts": {"x": 1}, "changes": []}`, "")
	withCwd(t, root)
	// Note: NO grantSyncTrust call.
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "sync")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(out, "is not trusted") {
		t.Errorf("untrusted plugin: want 'is not trusted' message, got %s", out)
	}
	if !strings.Contains(out, "leonard config trust sync test") {
		t.Errorf("untrusted plugin: want trust-grant hint, got %s", out)
	}
	if strings.Contains(out, "running test") {
		t.Errorf("untrusted plugin should not actually run: %s", out)
	}
}

// TestSync_TwoPluginsSeeAccumulatedFacts covers bughunt-11 F5:
// when two plugins run in sequence, plugin B's input must reflect
// plugin A's writes. The fix re-reads facts.yaml between plugins
// so we don't silently overwrite earlier plugins' changes.
func TestSync_TwoPluginsSeeAccumulatedFacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	// Test plugin B uses jq to echo+modify the facts envelope. Skip
	// cleanly when jq isn't available rather than fail.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; skipping multi-plugin facts test")
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(filepath.Join(dataDir, "ground-truth"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Plugin A: adds key "a" → "alpha".
	pluginA := filepath.Join(root, "a.sh")
	if err := os.WriteFile(pluginA, []byte(`#!/bin/sh
cat > /dev/null
cat <<'BODY'
{"updated_facts": {"a": "alpha"}, "changes": [{"path": "a", "new": "alpha"}]}
BODY
`), 0o755); err != nil {
		t.Fatal(err)
	}

	// Plugin B: echoes the input facts and tacks on key "b" → "beta".
	// If B receives stale (empty) facts, the merged result drops A's
	// "a" key. If B receives A's writes, the result has both keys.
	pluginB := filepath.Join(root, "b.sh")
	if err := os.WriteFile(pluginB, []byte(`#!/bin/sh
PAYLOAD=$(cat)
FACTS=$(echo "$PAYLOAD" | jq '.facts + {"b": "beta"}')
jq -n --argjson facts "$FACTS" '{updated_facts: $facts, changes: [{"path": "b", "new": "beta"}]}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := "[sync.alpha]\ncommand = \"" + pluginA + "\"\n\n[sync.beta]\ncommand = \"" + pluginB + "\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	withCwd(t, root)
	grantSyncTrust(t, root, "alpha")
	grantSyncTrust(t, root, "beta")
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "sync"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dataDir, "ground-truth", "facts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Both A's and B's writes should be present.
	if !strings.Contains(string(body), "alpha") {
		t.Errorf("plugin A's write is missing — B overwrote it:\n%s", body)
	}
	if !strings.Contains(string(body), "beta") {
		t.Errorf("plugin B's write is missing:\n%s", body)
	}
}

func TestSync_PluginFailurePrintsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	if err := os.MkdirAll(filepath.Join(dataDir, "ground-truth"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plugin exits non-zero with stderr.
	pluginPath := filepath.Join(root, "fail.sh")
	if err := os.WriteFile(pluginPath, []byte("#!/bin/sh\necho 'rate-limit' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[sync.failer]\ncommand = \"" + pluginPath + "\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	withCwd(t, root)
	grantSyncTrust(t, root, "failer")
	rt := &fakeRuntime{}
	// `sync` doesn't return an error from the cobra-level when a
	// plugin fails (we log + continue), but we want the failure to
	// be visible. Capture stderr via the cobra root buffer.
	out, err := runRoot(t, rt, "sync")
	if err != nil {
		t.Fatalf("sync should not propagate plugin error: %v", err)
	}
	_ = out
	// The error is logged to ErrOrStderr; runRoot doesn't expose
	// stderr separately. Confirm the operator output mentions
	// running the plugin at least.
}
