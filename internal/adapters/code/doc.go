// Package code is the code-symbol Adapter implementation. It wraps the
// existing internal/hooks and internal/mcp logic so the abstract
// adapters.Adapter contract (defined in internal/adapters) can stand
// in for what cmd/leonard-hook and cmd/leonard-mcp do today by direct
// call.
//
// This is the v0.7 deliverable from docs/ROADMAP-v1-ground-truth.md
// (issue #7). The acceptance contract is "structural-only refactor —
// every existing test passes unchanged": CodeAdapter.PreEdit produces
// the same JSON-on-stdout that hooks.HandlePreEdit does for any given
// envelope, and likewise for PostEdit / SessionStart / Stop. The
// RegisterTools method attaches the same MCP tool set that
// internal/mcp.NewServer attaches today.
//
// Why wrapping instead of moving the logic outright: internal/hooks
// has ~2,000 lines of fabrication-guard / verifier / claim-ledger
// logic that's been hardened across six bug-hunt rounds and two
// security reviews. Re-homing it would multiply the surface this PR
// touches without changing what the adapter contract proves. The
// wrapper is a thin shim; the load-bearing code stays where it is.
//
// The cmd/leonard-hook and cmd/leonard-mcp binaries are NOT rewired
// to dispatch through this adapter in this PR. That rewiring is
// tracked separately (will become issue #46) and lands once the
// ground-truth adapter (#8) gives the dispatch a second adapter to
// aggregate. Until then CodeAdapter is the reference implementation
// of the contract — used by tests that prove backwards-compat and
// by the future dispatcher.
package code
