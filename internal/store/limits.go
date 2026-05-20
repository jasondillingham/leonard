package store

// Resource caps for decision and claim text. Defined here in the
// store package so both the MCP-tool layer (internal/mcp) and the
// CLI layer (cmd/leonard) can validate against the same values.
//
// Sized generously above realistic real-world entries while
// preventing the obvious abuse cases. Sizes are in bytes — see
// caller-side validators for the rationale.
const (
	MaxDecisionTopicBytes     = 256
	MaxDecisionChoiceBytes    = 4 << 10  // 4 KiB
	MaxDecisionReasoningBytes = 32 << 10 // 32 KiB

	MaxClaimSummaryBytes  = 4 << 10
	MaxClaimEvidenceBytes = 256 << 10
)
