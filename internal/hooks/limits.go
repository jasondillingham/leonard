package hooks

import (
	"fmt"
	"io"
)

// Resource caps for hook payloads. Security-1 F2/F4/F9: a 143 MB
// PreToolUse payload allocated ~7 GB RSS because parseSnippet ran
// three full parse attempts on an unbounded new_string. Adding caps
// at the decode boundary stops the worst case before any allocation
// downstream.
//
// Sizing approach: pick a value comfortably above the largest
// real-world payload Claude Code is expected to emit, but small
// enough that an accidental or malicious oversize input is rejected
// in milliseconds rather than blowing up the indexer.
const (
	// MaxHookPayloadBytes is the upper bound on a single hook event's
	// JSON envelope read from stdin. 16 MiB matches the MCP stdin
	// filter's Scanner cap so the two surfaces have consistent limits.
	MaxHookPayloadBytes = 16 << 20

	// MaxSnippetBytes is the per-Edit/Write/MultiEdit/NotebookEdit
	// snippet cap. Real-world `new_string` values are kilobytes;
	// 1 MiB leaves orders of magnitude of headroom while preventing
	// the parseSnippet-amplification DoS.
	MaxSnippetBytes = 1 << 20

	// MaxMultiEditElements bounds the `edits[]` array on a MultiEdit
	// payload. Claude Code's documented MultiEdit shape doesn't have
	// a hard cap; in practice 20+ edits in one call is rare.
	MaxMultiEditElements = 100
)

// readPayloadBytes reads up to MaxHookPayloadBytes+1 bytes from r so
// that an oversize stream surfaces as a clean ErrDecode rather than
// an OOM. Returns the buffered bytes on success.
func readPayloadBytes(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxHookPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("hooks: read stdin: %w", err)
	}
	if len(body) > MaxHookPayloadBytes {
		return nil, fmt.Errorf("%w: hook payload exceeds %d bytes", ErrDecode, MaxHookPayloadBytes)
	}
	return body, nil
}
