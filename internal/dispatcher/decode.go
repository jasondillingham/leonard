package dispatcher

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// decodeEnvelope decodes a Claude Code hook envelope from r into v,
// bounding the read at hooks.MaxHookPayloadBytes.
//
// The bound is the point of this helper. The dispatcher entry points
// used json.NewDecoder(r).Decode(&env) directly, which reads until EOF
// with no cap — so hooks.MaxHookPayloadBytes, the security-1 F2/F4/F9
// mitigation, was never enforced on the production path. It lives in
// hooks.readPayloadBytes, which the CodeAdapter only reaches *after*
// the dispatcher has already materialized the whole envelope and the
// adapter has re-encoded it into a second buffer. Measured cost of the
// gap: a 200 MB payload drove ~2.45 GB peak RSS before anything
// rejected it.
//
// Errors wrap hooks.ErrDecode so cmd/leonard-hook's blockOnDecode maps
// them to exit 2 (block the tool call) rather than exit 1. That matches
// what internal/hooks already does for the same failures: a payload the
// guard could not inspect must never read as approval.
func decodeEnvelope(r io.Reader, v any) error {
	body, err := io.ReadAll(io.LimitReader(r, hooks.MaxHookPayloadBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read stdin: %v", hooks.ErrDecode, err)
	}
	if len(body) > hooks.MaxHookPayloadBytes {
		return fmt.Errorf("%w: hook payload exceeds %d bytes", hooks.ErrDecode, hooks.MaxHookPayloadBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: decode envelope: %v", hooks.ErrDecode, err)
	}
	return nil
}
