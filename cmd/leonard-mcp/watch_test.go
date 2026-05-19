package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
)

// seedAdapter creates a fresh store at dir/.leonard/leonard.db and returns the
// path plus an adapter with WatchDatabase already wired against it. The store
// is closed via t.Cleanup so callers don't have to thread it through.
func seedAdapter(t *testing.T, dir string) (string, *leonardmcp.StoreAdapter) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".leonard"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(dir, ".leonard", "leonard.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := leonardmcp.NewStoreAdapter(st)
	if err := a.WatchDatabase(dbPath); err != nil {
		t.Fatalf("WatchDatabase: %v", err)
	}
	return dbPath, a
}

// waitFor polls cond at 5ms granularity for up to timeout, failing the test if
// it never becomes true. Used in place of time.Sleep so the test isn't a
// hardcoded delay race.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchDatabase_TriggersStopOnRemoval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath, a := seedAdapter(t, dir)

	var stopCalled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var errOut bytes.Buffer
	go func() {
		watchDatabase(ctx, func() { stopCalled.Store(true) }, a, dbPath, &errOut, 5*time.Millisecond)
		close(done)
	}()

	// Idle for one tick to confirm the watcher doesn't false-positive on a
	// healthy DB.
	time.Sleep(20 * time.Millisecond)
	if stopCalled.Load() {
		t.Fatal("watcher fired before the DB was removed")
	}

	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db: %v", err)
	}
	waitFor(t, time.Second, "stop() to be called after removal", stopCalled.Load)
	<-done
	if !bytes.Contains(errOut.Bytes(), []byte("removed or replaced")) {
		t.Errorf("expected diagnostic on stderr, got %q", errOut.String())
	}
}

func TestWatchDatabase_TriggersStopOnInodeSwap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath, a := seedAdapter(t, dir)

	var stopCalled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watchDatabase(ctx, func() { stopCalled.Store(true) }, a, dbPath, &bytes.Buffer{}, 5*time.Millisecond)
		close(done)
	}()

	// Swap the file under the running watcher — same path, new inode.
	// rename+create exercises the "fresh DB on disk, ghost handle in memory"
	// scenario the inode check exists to catch.
	if err := os.Rename(dbPath, dbPath+".old"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("not really sqlite, but the watcher only stats it"), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	waitFor(t, time.Second, "stop() to be called after inode swap", stopCalled.Load)
	<-done
}

func TestWatchDatabase_ExitsCleanlyOnCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath, a := seedAdapter(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var stopCalled atomic.Bool
	go func() {
		watchDatabase(ctx, func() { stopCalled.Store(true) }, a, dbPath, &bytes.Buffer{}, 5*time.Millisecond)
		close(done)
	}()

	// Cancellation should make the goroutine return without ever firing stop.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not exit on ctx cancel")
	}
	if stopCalled.Load() {
		t.Error("ctx cancel should not trip the swap-detected stop()")
	}
}
