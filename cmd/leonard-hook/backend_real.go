package main

import (
	"path/filepath"

	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/index"
	"github.com/jasondillingham/leonard/internal/store"
)

// realBackend opens the project's SQLite store and returns an Indexer +
// ClaimRecorder adapter pair pointing at it.
type realBackend struct{}

func newDefaultBackend() Backend { return realBackend{} }

func (realBackend) Indexer(projectRoot string) hooks.Indexer {
	s := mustOpenStore(projectRoot)
	return index.New(s, projectRoot)
}

func (realBackend) Claims(projectRoot string) hooks.ClaimRecorder {
	s := mustOpenStore(projectRoot)
	return storeClaimsAdapter{s: s}
}

func (realBackend) Close() error { return nil }

// mustOpenStore is intentionally panicky — a hook running with a missing
// .leonard/leonard.db has nothing useful to do, and exit-1 with a clear
// reason is more debuggable than a silent malformed claim row. The Backend
// is built on demand for each hook invocation so the panic surfaces at the
// callsite.
func mustOpenStore(projectRoot string) *store.Store {
	s, err := store.Open(filepath.Join(projectRoot, ".leonard", "leonard.db"))
	if err != nil {
		panic(err)
	}
	return s
}

type storeClaimsAdapter struct{ s *store.Store }

// RecordClaim adapts hooks.ClaimRecorder onto store.Store.RecordClaim, which
// takes a struct. We pass time.Now via the store layer (it owns the clock
// per the brief), so the adapter just forwards the four fields.
func (a storeClaimsAdapter) RecordClaim(sessionID, claim, evidence string, verified bool) (int64, error) {
	return a.s.RecordClaim(store.Claim{
		SessionID: sessionID,
		Claim:     claim,
		Evidence:  evidence,
		Verified:  verified,
	})
}
