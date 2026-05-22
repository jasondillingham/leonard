// Package selflog is the self-logging adapter — the toolkit-side
// half of the v0.6 self-logging amendment (see docs/ROADMAP-v1-
// ground-truth.md). It watches for edits to truth-source files
// (both domain truth under .leonard/ground-truth/ and toolkit truth
// inside Leonard's own source) and appends a draft TruthChange
// entry to .leonard/pending-decisions.log.
//
// v0.6 is advisory: the draft is logged as JSON-per-line so an
// operator can review and promote, but PostEdit never blocks the
// edit. The hard require-tier enforcement (block-without-rationale)
// lands in #25; the auto-confirmation flow that promotes a draft
// into the decisions DB lands in #28 alongside the truth-history
// MCP tool.
//
// Why a separate adapter (not folded into groundtruth):
//   - Self-logging applies to TOOLKIT truth too, not just domain
//     truth. Editing internal/adapters/*.go is a toolkit-truth
//     change; folding the logic into groundtruth would mix
//     concerns.
//   - Operators can disable self-logging independently by removing
//     the [[adapters]] entry, without giving up the ground-truth
//     adapter's claim detection.
//   - The tier policy ("require" vs "warn" vs "skip") is
//     selflog-specific configuration; keeping it in its own
//     package keeps the groundtruth adapter focused.
//
// Hook methods other than PostEdit are no-ops in v0.6 — see the
// adapter.go file for the full list and the issue each gains
// behavior in.
package selflog
