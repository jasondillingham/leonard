// Package adapters defines the Adapter contract that Leonard's hook
// dispatchers and MCP server invoke for each enabled adapter.
//
// v0.6 introduces this abstraction so the existing code-symbol logic
// (refactored into CodeAdapter under v0.7, issue #7) and the new
// ground-truth adapter (v0.6+, issue #8) share a single dispatch path.
// The contract is intentionally minimal: lifecycle (Init/Close), four
// hook entry points (PreEdit/PostEdit/SessionStart/Stop), and one MCP
// surface (RegisterTools).
//
// The aggregator helpers in this package collapse per-adapter results
// into the single response shape each Claude Code hook expects:
//
//   - PreEdit:      deny-beats-pass — if ANY adapter denies, the final
//                   verdict is Deny and the first deny reason is
//                   reported.
//   - PostEdit:     additive — AdditionalContext blocks, SystemMessage
//                   lines, and Claims slices are concatenated across
//                   adapters in registration order.
//   - SessionStart: additive — markdown blocks joined with a "---"
//                   separator; empty blocks are dropped.
//   - Stop:         additive — SystemMessage lines joined with
//                   newlines; empties are dropped.
//
// Issue #6 (this package) defines the contract and ships contract tests
// against a stub adapter. Existing behavior is unchanged — no caller
// imports this package yet. Issue #7 will refactor internal/hooks +
// internal/mcp to dispatch through Adapter.
package adapters
