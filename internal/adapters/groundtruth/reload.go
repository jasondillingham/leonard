package groundtruth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// reloadInterval is how often the watch goroutine stats the
// ground-truth files looking for changes. 2 seconds keeps the
// syscall cost negligible (5 stat() calls every 2s) while making
// interactive edits feel responsive.
const reloadInterval = 2 * time.Second

// watchedFiles is the canonical list of files whose mtime changes
// trigger a reparse. Kept aligned with what Init loads — the
// audit-log.md presence check doesn't trigger reload because it's
// append-only and writes through the post-edit hook, not the
// operator's editor.
var watchedFiles = []string{
	"facts.yaml",
	"filters.yaml",
	"stories.md",
	"do-not-claim.md",
}

// startWatch begins a background goroutine that polls the
// ground-truth files for changes and re-parses on each change.
// Returns immediately. Close() cancels and waits for the goroutine.
//
// The watch is deliberately conservative: any mtime change on any
// watched file triggers a full reload of everything. Acceptable
// because the files are small (operators ship ~kilobytes) and the
// reparse runs once per change, not per tick.
//
// Parse errors during reload are logged to stderr and the prior
// state is kept — a malformed save shouldn't break in-flight
// sessions.
//
// The stop / done channels are captured at goroutine start so
// Close's later nil'ing of the struct fields doesn't strand the
// goroutine reading from a nil channel.
func (a *GroundTruthAdapter) startWatch(ctx context.Context) {
	stop := make(chan struct{})
	done := make(chan struct{})

	a.mu.Lock()
	a.watchStop = stop
	a.watchDone = done
	a.mu.Unlock()

	// Seed the mtime cache so the first tick doesn't see "change."
	initial := a.snapshotMtimes()

	go a.watchLoop(ctx, initial, stop, done)
}

// watchLoop runs until Close is called or ctx is cancelled. Polls
// at reloadInterval; on any mtime change runs ReloadOnce and
// updates the cache. stop / done are passed in (not read from the
// struct) so Close can nil the struct fields without breaking the
// goroutine's shutdown signal.
func (a *GroundTruthAdapter) watchLoop(ctx context.Context, last map[string]time.Time, stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := a.snapshotMtimes()
			if !mtimesEqual(last, current) {
				if err := a.ReloadOnce(); err != nil {
					a.mu.RLock()
					stderr := a.stderr
					a.mu.RUnlock()
					fmt.Fprintf(stderr, "leonard: ground-truth hot-reload: %v\n", err)
					// Don't update the mtime cache on error so we
					// keep retrying — operator's next save (which
					// fixes the typo) will trigger another reload.
					continue
				}
			}
			last = current
		}
	}
}

// snapshotMtimes stats the watched files and returns mtimes. A
// file that doesn't exist (or stat fails) is recorded as the zero
// time so it differs from a future "now exists" state.
func (a *GroundTruthAdapter) snapshotMtimes() map[string]time.Time {
	a.mu.RLock()
	dir := a.truthDir
	a.mu.RUnlock()
	out := make(map[string]time.Time, len(watchedFiles))
	for _, name := range watchedFiles {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			out[name] = info.ModTime()
		} else {
			out[name] = time.Time{}
		}
	}
	return out
}

func mtimesEqual(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !v.Equal(b[k]) {
			return false
		}
	}
	return true
}

// ReloadOnce re-parses all four watched files from the configured
// truthDir and atomically swaps the adapter's state. Exposed for
// tests (skip the polling delay) and for future CLI subcommands
// that want explicit reload.
//
// Parse errors leave the adapter's state unchanged — the prior
// truth tree keeps serving hook requests until the operator
// re-saves with valid syntax.
func (a *GroundTruthAdapter) ReloadOnce() error {
	a.mu.RLock()
	dir := a.truthDir
	a.mu.RUnlock()
	if dir == "" {
		return nil
	}

	facts, err := loadFacts(filepath.Join(dir, "facts.yaml"))
	if err != nil {
		return err
	}
	filters, err := loadFilters(filepath.Join(dir, "filters.yaml"))
	if err != nil {
		return err
	}
	stories, err := loadStories(filepath.Join(dir, "stories.md"))
	if err != nil {
		return err
	}
	rules, err := loadRules(filepath.Join(dir, "do-not-claim.md"))
	if err != nil {
		return err
	}
	hasAudit, err := auditLogPresent(filepath.Join(dir, "audit-log.md"))
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.facts = facts
	a.filters = filters
	a.stories = stories
	a.rules = rules
	a.auditLogExists = hasAudit
	a.mu.Unlock()
	return nil
}
