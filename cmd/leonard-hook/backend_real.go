package main

import (
	"path/filepath"
	"sync"

	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/index"
	"github.com/jasondillingham/leonard/internal/store"
)

// realBackend opens the project's SQLite store and returns an Indexer +
// ClaimRecorder adapter pair pointing at it. A single store is shared
// across the Indexer() and Claims() calls within a hook invocation so
// the Close() at end-of-process can be deterministic — bughunt-9 F3
// (HIGH): the v0.51 Store.Close-time WAL checkpoint was dead code
// because each hook subcommand opened the store via mustOpenStore but
// nothing closed it. Without Close the wal_checkpoint(PASSIVE) in
// Close() never fires and WAL grows unbounded across many sequential
// hook processes.
type realBackend struct {
	mu sync.Mutex
	s  *store.Store
}

func newDefaultBackend() Backend { return &realBackend{} }

func (b *realBackend) Indexer(projectRoot string) hooks.Indexer {
	return index.New(b.store(projectRoot), projectRoot)
}

func (b *realBackend) Claims(projectRoot string) hooks.ClaimRecorder {
	return storeClaimsAdapter{s: b.store(projectRoot)}
}

// Close releases the shared store, which triggers
// wal_checkpoint(PASSIVE) in store.Close(). The cobra root command
// defers this after every subcommand so each hook process leaves the
// WAL bounded. bughunt-9 F3.
func (b *realBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.s == nil {
		return nil
	}
	err := b.s.Close()
	b.s = nil
	return err
}

// store lazily opens the project's store once per realBackend
// lifetime. Indexer() and Claims() share the same connection so
// Close() actually closes a single resource.
func (b *realBackend) store(projectRoot string) *store.Store {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.s == nil {
		s, err := store.Open(filepath.Join(projectRoot, ".leonard", "leonard.db"))
		if err != nil {
			// mustOpenStore-equivalent: reached only after the cobra
			// layer verified the DB exists. Other failures are
			// unrecoverable for the hook's purpose; panic loudly so
			// the operator notices.
			panic(err)
		}
		b.s = s
	}
	return b.s
}

type storeClaimsAdapter struct{ s *store.Store }

// RecordClaim adapts hooks.ClaimRecorder onto store.Store.RecordClaim. The
// store owns the clock (per the brief), so we leave RecordedAt zero and let
// the store stamp it.
func (a storeClaimsAdapter) RecordClaim(rec hooks.ClaimRecord) (int64, error) {
	return a.s.RecordClaim(store.Claim{
		SessionID:       rec.SessionID,
		Claim:           rec.Claim,
		Evidence:        rec.Evidence,
		Verified:        rec.Verified,
		FilePath:        rec.FilePath,
		Tool:            rec.Tool,
		IndexOK:         rec.IndexOK,
		VetOK:           rec.VetOK,
		VetErrorSummary: rec.VetErrorSummary,
	})
}

func (a storeClaimsAdapter) SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error) {
	return a.s.SupersedeClaimsForFile(filePath, supersedingClaimID)
}

func (a storeClaimsAdapter) SupersedeOutstandingFailures(supersedingClaimID int64) (int, error) {
	return a.s.SupersedeOutstandingFailures(supersedingClaimID)
}
