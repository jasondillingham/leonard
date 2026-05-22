package dispatcher_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"

	"github.com/jasondillingham/leonard/internal/dispatcher"
)

// TestRegisterToolsAcrossAdapters confirms #46's MCP-side
// integration: when both code + ground-truth adapters load, their
// RegisterTools calls coexist on the same MCP server. This is the
// dispatcher path leonard-mcp/main.go takes in production.
func TestRegisterToolsAcrossAdapters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	// go.mod → triggers code adapter; .leonard/ground-truth/ →
	// triggers ground-truth adapter. Both should register tools.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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

	loaded, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer loaded.Close()

	// At least both adapters should be present in degraded mode
	// (no DB at .leonard/leonard.db, but the code adapter still
	// registers with permissive-store semantics).
	gotNames := map[string]bool{}
	for _, a := range loaded.Adapters {
		gotNames[a.Name()] = true
	}
	for _, want := range []string{"code", "ground-truth"} {
		if !gotNames[want] {
			t.Errorf("expected %s adapter to load; got %v", want, gotNames)
		}
	}

	// Build a bare MCP server and call RegisterTools on each.
	// Confirm no errors and (importantly) no panic — collisions
	// between adapter tool registrations would surface here.
	srv := leonardmcp.NewBareServer(leonardmcp.Implementation{
		Name:    "test-leonard-mcp",
		Version: "test",
	})
	for _, a := range loaded.Adapters {
		if err := a.RegisterTools(srv); err != nil {
			t.Errorf("%s RegisterTools: %v", a.Name(), err)
		}
	}
}
