package hooks

import "errors"

// ErrDecode marks a stdin payload that the hook handler couldn't parse as a
// well-formed Claude Code hook envelope (empty stdin, invalid JSON,
// type-mismatched fields, etc.). The cobra layer matches on this sentinel via
// errors.Is so the pre-edit / post-edit guards can map decode failures to
// exit-code 2 (block the tool call) instead of exit-code 1 (which Claude Code
// treats as a non-blocking error and would let the fabricated edit through).
var ErrDecode = errors.New("hooks: payload decode error")
