package sync_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync"
)

// writeTestPlugin drops a bash script at <dir>/plugin.sh that echoes
// the supplied stdout body verbatim, regardless of stdin. Returns
// the absolute path. Skips on non-unix.
func writeTestPlugin(t *testing.T, dir, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test plugin requires /bin/sh")
	}
	path := filepath.Join(dir, "plugin.sh")
	script := "#!/bin/sh\ncat > /dev/null\ncat <<'PLUGIN_BODY'\n" + body + "\nPLUGIN_BODY\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	return path
}

func TestRun_HappyPath(t *testing.T) {
	dir := t.TempDir()
	cmdPath := writeTestPlugin(t, dir, `{
  "updated_facts": {"tech_stack": {"primary_language": "Rust"}},
  "changes": [
    {"path": "tech_stack.primary_language", "old": "Go", "new": "Rust", "reason": "language swap"}
  ]
}`)

	res, err := sync.Run(context.Background(), sync.Plugin{
		Name:    "test",
		Command: cmdPath,
	}, map[string]any{"tech_stack": map[string]any{"primary_language": "Go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.UpdatedFacts["tech_stack"].(map[string]any)["primary_language"]; got != "Rust" {
		t.Errorf("updated facts: want Rust, got %v", got)
	}
	if len(res.Changes) != 1 || res.Changes[0].Reason != "language swap" {
		t.Errorf("changes: %+v", res.Changes)
	}
	if res.Elapsed == 0 {
		t.Error("Elapsed should be populated")
	}
}

func TestRun_PassesFactsAndConfigOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test plugin requires /bin/sh")
	}
	dir := t.TempDir()
	// This plugin reads stdin and echoes it as the value of
	// updated_facts.echoed so the test can assert.
	scriptPath := filepath.Join(dir, "plugin.sh")
	script := `#!/bin/sh
set -e
PAYLOAD=$(cat)
printf '{"updated_facts": {"echoed": %s}, "changes": []}\n' "$PAYLOAD"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	res, err := sync.Run(context.Background(), sync.Plugin{
		Name:    "echo",
		Command: scriptPath,
		Config:  map[string]any{"plugin_setting": "abc"},
	}, map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	echoed, ok := res.UpdatedFacts["echoed"].(map[string]any)
	if !ok {
		t.Fatalf("echoed not present: %v", res.UpdatedFacts)
	}
	facts, _ := echoed["facts"].(map[string]any)
	cfg, _ := echoed["config"].(map[string]any)
	if facts == nil || facts["x"] == nil {
		t.Errorf("facts subset not passed on stdin: %+v", facts)
	}
	if cfg == nil || cfg["plugin_setting"] != "abc" {
		t.Errorf("config not passed on stdin: %+v", cfg)
	}
}

func TestRun_NonZeroExitSurfacesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fail.sh")
	script := `#!/bin/sh
echo "rate-limited, try again later" >&2
exit 2
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := sync.Run(context.Background(), sync.Plugin{
		Name:    "failer",
		Command: scriptPath,
	}, nil)
	if err == nil {
		t.Fatal("expected non-zero error")
	}
	if !strings.Contains(res.Stderr, "rate-limited") {
		t.Errorf("stderr not surfaced: %q", res.Stderr)
	}
}

func TestRun_RejectsInvalidJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	scriptPath := writeTestPlugin(t, dir, "not json at all")
	_, err := sync.Run(context.Background(), sync.Plugin{
		Name:    "broken",
		Command: scriptPath,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("want JSON parse error, got %v", err)
	}
}

func TestRun_RequiresCommand(t *testing.T) {
	_, err := sync.Run(context.Background(), sync.Plugin{Name: "nocmd"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Command is required") {
		t.Errorf("want Command-required error, got %v", err)
	}
}

// TestRun_BoundsStdoutSize covers bughunt-11 F4: a plugin that
// emits more than maxPluginOutputBytes (16 MiB) to stdout must
// produce an error rather than allocate unbounded memory.
func TestRun_BoundsStdoutSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "huge.sh")
	// Write 17 MiB of 'x' to stdout — just over the cap. dd's
	// stderr goes to /dev/null so it doesn't pollute the test's
	// stderr buffer.
	script := `#!/bin/sh
cat > /dev/null
dd if=/dev/zero bs=1048576 count=17 2>/dev/null | tr '\0' 'x'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := sync.Run(context.Background(), sync.Plugin{
		Name: "huge", Command: scriptPath,
	}, nil)
	if err == nil {
		t.Fatal("expected error for over-cap stdout")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("want cap-exceeded error, got %v", err)
	}
}

// Note: context-deadline cancellation behavior is governed by
// exec.CommandContext, which on some platforms doesn't propagate
// SIGKILL through `/bin/sh` to grandchildren (the `sleep` we'd want
// to interrupt). Testing this properly requires Setpgid + group
// kill plumbing that isn't worth adding at v0.9 plugin-runner
// scope. The defaultTimeout constant still acts as a safety ceiling
// in production.
