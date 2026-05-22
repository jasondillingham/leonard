package adapters

// PreEditPayload carries the bits of Claude Code's PreToolUse envelope
// that adapters consume. The dispatcher constructs this from the raw
// JSON envelope (internal/hooks.PreToolUsePayload) so adapters never
// see Claude-Code-specific wire shapes — that lets us evolve the
// transport without breaking adapters.
type PreEditPayload struct {
	// SessionID is the Claude Code session identifier. Adapters use it
	// to scope cross-edit state (e.g., per-session claim ledgers).
	SessionID string

	// Tool is the Claude Code tool name: "Edit", "Write", "MultiEdit",
	// "NotebookEdit", or "Bash". Adapters that only care about
	// file-shaped tools should skip Bash; the code adapter inspects
	// Bash to catch shell redirections that would write under
	// .leonard/.
	Tool string

	// FilePath is the absolute or project-relative path the edit
	// targets. For NotebookEdit this is the notebook_path field; the
	// dispatcher normalizes both into FilePath. Empty for Bash.
	FilePath string

	// Content is the unified snippet the edit would write: NewString
	// for Edit, Content for Write, NewSource for NotebookEdit, and a
	// concatenation of edits[].new_string for MultiEdit. The
	// dispatcher joins MultiEdit pieces so an adapter sees the full
	// proposed delta.
	Content string

	// Command is populated only when Tool == "Bash". Adapters that
	// inspect shell commands (the .leonard/ guard, future filter
	// rules) read this; others can ignore it.
	Command string

	// CWD is Claude Code's reported working directory. Adapters that
	// resolve relative paths or locate go.mod use it. Empty if the
	// hook envelope omitted it.
	CWD string
}

// PreEditResult is what each adapter returns from PreEdit. The
// dispatcher aggregates a slice of these with deny-beats-pass rules
// (see AggregatePreEdit).
type PreEditResult struct {
	// Decision is Pass (default), Deny, or Ask. See Decision docs.
	Decision Decision

	// Reason is surfaced to Claude when Decision == Deny. Leave empty
	// when passing. Should be a single short sentence — Claude reads
	// it on the next assistant turn and decides what to try next.
	Reason string

	// AdapterName is filled in by the dispatcher if empty, so error
	// paths can attribute a deny to a specific adapter without each
	// implementation having to remember to set it.
	AdapterName string
}

// PostEditPayload carries the post-edit envelope Adapters consume.
// Mirrors internal/hooks.PostToolUsePayload but flattened to the
// fields adapters actually read.
type PostEditPayload struct {
	SessionID string
	Tool      string
	FilePath  string
	CWD       string
}

// PostEditResult is the adapter's contribution to the post-edit
// hookSpecificOutput. The dispatcher concatenates AdditionalContext
// blocks, SystemMessage lines, and Claims slices across adapters in
// registration order (see AggregatePostEdit).
type PostEditResult struct {
	// AdditionalContext is markdown the dispatcher prepends to the
	// next assistant turn via PostToolUse hookSpecificOutput
	// additionalContext. Empty string means "no contribution."
	AdditionalContext string

	// SystemMessage is shown to the operator (not the model) via the
	// Claude Code systemMessage channel. The post-edit hook's existing
	// "vet failed" surfacing uses this.
	SystemMessage string

	// Claims are structured claim records the dispatcher persists via
	// the claim ledger. Existing code-adapter claims (vet outcome,
	// index outcome) and future ground-truth-adapter claims
	// (forbidden-pattern hits, verified facts) share this shape.
	Claims []ClaimRecord
}

// ClaimRecord is the adapter-facing shape of a claim ledger entry.
// Mirrors internal/hooks.ClaimRecord and internal/mcp.ClaimRecord but
// declared here so adapter implementations don't have to import
// either of those packages.
type ClaimRecord struct {
	// Claim is the short description: "vet passed", "forbidden:
	// HIPAA-compliant", "verified fact: tech_stack.primary_language".
	Claim string

	// Evidence is supporting detail: vet stderr summary, the
	// do-not-claim.md rule that matched, the facts.yaml path that
	// satisfied the claim. Bounded length — the ledger caps payload
	// size, so adapters should summarize rather than dump.
	Evidence string

	// FilePath ties the claim to the file the edit touched (when
	// known). Empty for claims that aren't file-scoped.
	FilePath string

	// Verified marks the claim as a positive verification ("vet ok",
	// "fact matched") rather than a negative finding ("vet failed",
	// "forbidden pattern hit"). Negative findings are the ones the
	// Stop hook surfaces to the operator at session end.
	Verified bool

	// AdapterName is filled by the dispatcher (see PreEditResult).
	AdapterName string
}

// SessionStartPayload mirrors Claude Code's SessionStart envelope.
// Source is the only field adapters typically branch on — the
// dispatcher uses it for the "compact"/"clear" no-op short-circuit
// before calling adapters.
type SessionStartPayload struct {
	SessionID string

	// Source is one of "startup", "resume", "compact", "clear". The
	// dispatcher already skips the adapter loop entirely for "compact"
	// and "clear" (matching the existing internal/hooks behavior); a
	// payload an adapter sees will always be "startup" or "resume".
	Source string

	CWD string
}

// SessionStartResult is the adapter's contribution to the session-start
// injection. The dispatcher concatenates AdditionalContext blocks with
// a "---" separator (see AggregateSessionStart) and emits a single
// hookSpecificOutput additionalContext payload.
type SessionStartResult struct {
	// AdditionalContext is markdown to inject at session start. Empty
	// string means "no contribution."
	AdditionalContext string
}

// StopPayload mirrors Claude Code's Stop envelope. SessionID is the
// only field the existing Stop handler actually reads — adapters that
// surface per-session claim summaries use it to scope the query.
type StopPayload struct {
	SessionID string
	CWD       string
}

// StopResult is the adapter's contribution to the stop surface. The
// dispatcher concatenates SystemMessage lines across adapters with
// newline separators (see AggregateStop).
type StopResult struct {
	// SystemMessage is shown to the operator at session end via the
	// Claude Code systemMessage channel. Empty means "no contribution."
	SystemMessage string
}
