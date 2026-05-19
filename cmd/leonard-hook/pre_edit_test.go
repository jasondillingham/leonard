package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/hooks"
)

type fakePreEditStore struct {
	has map[string]bool
}

func (f *fakePreEditStore) HasSymbol(name string) (bool, error) {
	return f.has[name], nil
}

func writeProjectFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func setupProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/jasondillingham/leonard\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return root
}

func runPreEditCmd(t *testing.T, open preEditOpener, readModule modulePathReader, payload []byte) (hooks.PreEditResponse, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "leonard-hook"}
	cmd.AddCommand(newPreEditCmdWithDeps(open, readModule))
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"pre-edit"})
	if err := cmd.Execute(); err != nil {
		return hooks.PreEditResponse{}, fmt.Errorf("execute: %w\nstdout=%s", err, out.String())
	}
	var resp hooks.PreEditResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, out.String())
	}
	return resp, nil
}

func TestPreEditCmd_RoundTripAllow(t *testing.T) {
	root := setupProjectRoot(t)
	target := writeProjectFile(t, root, "foo.go", `package foo

import "fmt"
`)

	closedCount := 0
	open := func(string) (hooks.SymbolStore, func() error, error) {
		return &fakePreEditStore{has: map[string]bool{"Println": true}},
			func() error { closedCount++; return nil }, nil
	}
	readModule := func(string) string { return "github.com/jasondillingham/leonard" }

	payload, err := json.Marshal(hooks.PreToolUsePayload{
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput:     hooks.PreEditToolInput{FilePath: target, NewString: `fmt.Println("hi")`},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runPreEditCmd(t, open, readModule, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Continue || resp.Decision == "block" {
		t.Fatalf("expected allow, got %+v", resp)
	}
	if closedCount != 1 {
		t.Errorf("closer called %d times, want 1", closedCount)
	}
}

func TestPreEditCmd_RoundTripBlock(t *testing.T) {
	root := setupProjectRoot(t)
	target := writeProjectFile(t, root, "foo.go", `package foo

import "github.com/jasondillingham/leonard/internal/store"
`)

	open := func(string) (hooks.SymbolStore, func() error, error) {
		return &fakePreEditStore{has: map[string]bool{}}, func() error { return nil }, nil
	}
	readModule := func(string) string { return "github.com/jasondillingham/leonard" }

	payload, err := json.Marshal(hooks.PreToolUsePayload{
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput:     hooks.PreEditToolInput{FilePath: target, NewString: "store.PhantomFunction()"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runPreEditCmd(t, open, readModule, payload)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Continue || resp.Decision != "block" {
		t.Fatalf("expected block, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "store.PhantomFunction") {
		t.Errorf("reason missing phantom: %q", resp.Reason)
	}
}

func TestPreEditCmd_OpenerErrorBubblesUp(t *testing.T) {
	setupProjectRoot(t)
	open := func(string) (hooks.SymbolStore, func() error, error) {
		return nil, func() error { return nil }, errors.New("locked db")
	}
	readModule := func(string) string { return "github.com/jasondillingham/leonard" }
	cmd := &cobra.Command{Use: "leonard-hook"}
	cmd.AddCommand(newPreEditCmdWithDeps(open, readModule))
	payload, _ := json.Marshal(hooks.PreToolUsePayload{
		ToolName:  "Edit",
		ToolInput: hooks.PreEditToolInput{FilePath: "foo.go", NewString: "x := 1"},
	})
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"pre-edit"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "locked db") {
		t.Fatalf("expected opener error to bubble up, got %v", err)
	}
}

func TestDefaultModulePath_ReadsGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/example/leonard\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := defaultModulePath(root), "github.com/example/leonard"; got != want {
		t.Errorf("defaultModulePath = %q, want %q", got, want)
	}
}

func TestDefaultModulePath_QuotedDirective(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module \"github.com/example/leonard\"\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := defaultModulePath(root), "github.com/example/leonard"; got != want {
		t.Errorf("defaultModulePath = %q, want %q", got, want)
	}
}

func TestDefaultModulePath_ReturnsEmptyOnMissingFile(t *testing.T) {
	t.Parallel()
	if got := defaultModulePath(t.TempDir()); got != "" {
		t.Errorf("defaultModulePath without go.mod = %q, want empty", got)
	}
}

func TestPermissiveStoreAllowsEverything(t *testing.T) {
	t.Parallel()
	has, err := permissiveStore{}.HasSymbol("anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has {
		t.Error("permissiveStore should report every name as present")
	}
}
