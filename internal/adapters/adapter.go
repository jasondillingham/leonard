package adapters

import (
	"context"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/config"
)

// Adapter is the per-project, per-domain plug-in contract. Each enabled
// adapter contributes its own pre-edit guard, post-edit verification,
// session-start surface, stop summary, and MCP tool surface. The
// dispatcher aggregates across all enabled adapters (see the Aggregate*
// helpers in this package).
//
// Implementations must be safe for concurrent invocation: each hook
// binary may run while a separate MCP request is in-flight inside
// leonard-mcp.
type Adapter interface {
	// Name returns the adapter's registry key (e.g., "code",
	// "ground-truth"). Two adapters enabled in the same project MUST
	// NOT share a name — the dispatcher uses it as a stable identifier
	// in telemetry and error messages.
	Name() string

	// Init is called once per process before any hook or MCP request.
	// Implementations should load on-disk state (symbol index, truth
	// files, etc.) and return a non-nil error if the state is malformed
	// in a way the adapter cannot recover from. The dispatcher treats
	// Init errors as fatal for that adapter only — other adapters in
	// the same project continue to run.
	Init(ctx context.Context, cfg Config) error

	// Close releases any resources Init opened. It MUST be idempotent —
	// the hook binaries invoke Close defensively on error paths and
	// again at normal shutdown.
	Close() error

	// PreEdit inspects a proposed edit before Claude Code applies it.
	// Returning a result whose Decision is Deny rejects the edit; the
	// Reason field is surfaced to Claude so it can choose a different
	// approach. The default zero-value PreEditResult is Pass.
	//
	// PreEdit MUST NOT mutate any on-disk state — the edit has not
	// happened yet.
	PreEdit(ctx context.Context, p PreEditPayload) (PreEditResult, error)

	// PostEdit runs after an Edit/Write/MultiEdit has been applied.
	// Adapters return AdditionalContext markdown (prepended to the next
	// assistant turn), SystemMessage text (shown to the operator, not
	// the model), and any structured Claims the dispatcher should
	// persist via the claim ledger.
	//
	// PostEdit is advisory: it cannot block, and any non-nil error
	// is logged by the dispatcher but does not propagate to Claude.
	PostEdit(ctx context.Context, p PostEditPayload) (PostEditResult, error)

	// SessionStart returns markdown to inject into the model's context
	// at session start. The dispatcher honors the Source field on the
	// payload (skipping injection for "compact" and "clear" — see
	// internal/hooks.shouldInjectForSource for the existing semantics).
	// Empty AdditionalContext means "no contribution from this adapter."
	SessionStart(ctx context.Context, p SessionStartPayload) (SessionStartResult, error)

	// Stop returns text surfaced to the operator at session end. Stop
	// is advisory only — adapters cannot block a session from ending
	// (Claude Code's Stop hook schema doesn't accept a deny verdict
	// without also blocking the session, which would be a worse UX
	// than letting it close).
	Stop(ctx context.Context, p StopPayload) (StopResult, error)

	// RegisterTools attaches the adapter's MCP tools to srv. Called
	// once per leonard-mcp process, after Init. Adapters that expose
	// no MCP tools implement this as a no-op returning nil.
	RegisterTools(srv *mcp.Server) error
}

// Config carries everything an Adapter.Init implementation needs to
// bring itself up: project root, shared utilities, the adapter's own
// TOML subtree, and the global Leonard config for adapters that need
// to read pre-existing top-level keys ([hooks], [post_edit]).
type Config struct {
	// ProjectRoot is the absolute path to the directory containing
	// .leonard/. Adapters MUST treat any file path outside ProjectRoot
	// as out-of-scope — the existing path-trust guard in
	// internal/hooks (rejecting file_path values that resolve outside
	// the project root) should be preserved by every adapter.
	ProjectRoot string

	// Stderr is where the adapter writes operational logs. The hook
	// binaries pass os.Stderr; tests can pipe a buffer to assert on
	// what was logged.
	Stderr io.Writer

	// Raw is the un-typed TOML subtree for this adapter, lifted from
	// .leonard/config.toml's [[adapters]] array by the dispatcher.
	// Adapters decode their own schema from this map; the dispatcher
	// does not parse adapter-specific fields.
	Raw map[string]any

	// Global is the project-wide config slice that pre-dates the
	// adapter system ([hooks], [post_edit]). The code adapter reads
	// [post_edit.verify] from here; other adapters can ignore it.
	Global config.Config
}

// Decision is the verdict a pre-edit hook returns. The dispatcher
// aggregates per-adapter Decisions with deny-beats-pass semantics.
type Decision int

const (
	// Pass allows the edit to proceed. Zero value so the default
	// PreEditResult passes when an adapter forgets to set Decision.
	Pass Decision = iota

	// Deny rejects the edit. The dispatcher surfaces the adapter's
	// Reason field to Claude via the PreToolUse hookSpecificOutput
	// permissionDecisionReason channel.
	Deny

	// Ask reserves a slot for future "human in the loop" verdicts.
	// v0.6 dispatchers treat Ask the same as Deny — adapters MAY
	// emit it, but the aggregator collapses it for now. v0.8 may
	// promote it to its own flow.
	Ask
)

// String returns the lower-case spelling of a Decision so log output
// and telemetry attribute values match the documented enum.
func (d Decision) String() string {
	switch d {
	case Pass:
		return "pass"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	default:
		return "unknown"
	}
}
