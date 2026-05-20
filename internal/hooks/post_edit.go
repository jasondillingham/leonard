package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/index"
	"github.com/jasondillingham/leonard/internal/telemetry"
)

// Indexer is the minimum surface internal/index.Indexer must satisfy for the
// post-edit hook. Keeping the contract local lets us unit-test without
// pulling the parser lane in as a dependency.
type Indexer interface {
	IndexFile(path string) error
}

// ClaimRecord carries the structured fields the post-edit hook derives, so
// adapters can persist them as real columns instead of leaving callers to
// re-parse the claim/evidence blobs later. IndexOK and VetOK are pointers
// because they're tri-state ({nil, true, false}) — VetOK is nil when no
// go.mod was found and vet was skipped; IndexOK is nil only when the hook
// short-circuited before the index step (currently never, but reserved).
type ClaimRecord struct {
	SessionID       string
	Claim           string
	Evidence        string
	FilePath        string
	Verified        bool
	Tool            string
	IndexOK         *bool
	VetOK           *bool
	VetErrorSummary string
}

// ClaimRecorder is the minimum surface internal/store.Store must satisfy for
// the post-edit hook. RecordClaim persists the row; SupersedeClaimsForFile
// links prior unverified rows for filePath to a fresh vet=ok claim so
// stop-time output focuses on failures the next edit didn't already fix.
type ClaimRecorder interface {
	RecordClaim(rec ClaimRecord) (int64, error)
	SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error)
}

// PostToolUsePayload mirrors the Claude Code PostToolUse hook envelope. The
// fields we actually consume are session_id, tool_input.file_path, and cwd
// (used to locate go.mod). Everything else is decoded loosely.
type PostToolUsePayload struct {
	SessionID     string    `json:"session_id"`
	HookEventName string    `json:"hook_event_name"`
	ToolName      string    `json:"tool_name"`
	ToolInput     ToolInput `json:"tool_input"`
	CWD           string    `json:"cwd"`
}

// ToolInput captures the subset of Edit/Write tool input we read. notRead
// fields (old_string, new_string, etc.) deserialize without complaint and we
// drop them.
type ToolInput struct {
	FilePath string `json:"file_path"`
}

// HookResponse is the JSON document we emit on stdout. The phase-1 hook never
// blocks: it always sets Continue=true and exits 0.
//
// SystemMessage is the short status line Claude Code surfaces to the human
// user in the terminal — not visible to the model.
//
// HookSpecificOutput.AdditionalContext is the channel that *does* reach the
// model: Claude Code prepends it to the next assistant turn. The post-edit
// hook only populates this on failures (index error or vet=fail) so a green
// run doesn't pollute the model's context with status pings.
type HookResponse struct {
	Continue           bool                       `json:"continue"`
	SuppressOutput     bool                       `json:"suppressOutput,omitempty"`
	SystemMessage      string                     `json:"systemMessage,omitempty"`
	HookSpecificOutput *PostToolUseSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// PostToolUseSpecificOutput is the hookSpecificOutput envelope Claude Code
// expects for PostToolUse events. AdditionalContext is the only field that
// reaches the model — this is what makes `false done claims` (Leonard's
// failure-mode #3) actually self-correcting instead of silent.
type PostToolUseSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// PostEditOptions wires the post-edit handler to its collaborators. ProjectRoot
// is the working directory `go vet ./...` should run in (typically the cwd of
// the Claude Code session). Vet is injected so tests can substitute a stub —
// the production wire-up passes RunGoVet.
type PostEditOptions struct {
	Indexer      Indexer
	Claims       ClaimRecorder
	ProjectRoot  string
	Vet          VetRunner
	VetTimeout   time.Duration
	EvidenceCap  int
	Now          func() time.Time
}

// VetRunner shells out to `go vet ./...` (or an equivalent verifier).
// Returns combined stdout/stderr, a non-nil error if the command exited
// non-zero. The error must not be returned for a clean vet run.
type VetRunner func(ctx context.Context, projectRoot string) (output string, err error)

// VetResult is what HandlePostEdit synthesises from a VetRunner call so that
// the claim row carries useful evidence even when vet succeeds.
type VetResult struct {
	Ran      bool   // false when no go.mod was found
	Passed   bool
	Output   string
	ExitErr  string // empty on success
}

// defaultEvidenceCap bounds how much vet output we persist. 16 KiB is enough
// for several pages of vet diagnostics without blowing up the SQLite row.
const defaultEvidenceCap = 16 * 1024

// HandlePostEdit reads a PostToolUse JSON envelope from stdin, refreshes the
// symbol index for the touched file, runs `go vet ./...` when a go.mod is
// present, persists a claim row, and writes a Claude Code hook response to
// stdout. It returns nil on success regardless of vet outcome — vet failures
// are recorded as unverified claims, not handler errors.
func HandlePostEdit(ctx context.Context, opts PostEditOptions, stdin io.Reader, stdout io.Writer) error {
	ctx, end := telemetry.Span(ctx, "leonard.post-edit")
	defer end()

	if opts.Indexer == nil {
		return errors.New("hooks: Indexer is required")
	}
	if opts.Claims == nil {
		return errors.New("hooks: ClaimRecorder is required")
	}
	if opts.Vet == nil {
		opts.Vet = RunGoVet
	}
	if opts.VetTimeout == 0 {
		opts.VetTimeout = 30 * time.Second
	}
	if opts.EvidenceCap == 0 {
		opts.EvidenceCap = defaultEvidenceCap
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	payload, err := decodePayload(stdin)
	if err != nil {
		return err
	}
	filePath := strings.TrimSpace(payload.ToolInput.FilePath)
	if filePath == "" {
		return fmt.Errorf("%w: tool_input.file_path missing from PostToolUse payload", ErrDecode)
	}

	root := opts.ProjectRoot
	if root == "" {
		root = strings.TrimSpace(payload.CWD)
	}
	if root == "" {
		root = "."
	}

	// Security-1 F1: reject file_path values that resolve outside
	// the project root BEFORE any filesystem read/write. A crafted
	// PostToolUse payload with file_path=/etc/hosts or "../../other.go"
	// used to flow straight through to IndexFile, which then stored
	// foreign symbols as if they were project content. Validate
	// upfront so the stat-check below and the IndexFile call below
	// both run only on contained paths.
	safePath, ok := index.ResolveSafe(root, filePath)
	if !ok {
		return handleEscapedPath(opts, payload, filePath, stdout)
	}
	filePath = safePath

	// hooks F5: PostToolUse fires whether or not the edit actually landed —
	// e.g. a write Claude believes succeeded but the user rejected via a hook
	// veto. The indexer silently no-ops on missing paths, so without this
	// stat-check we'd happily report "re-indexed X, go vet ok" for files that
	// don't exist. Short-circuit with an honest claim instead.
	if _, statErr := os.Stat(filePath); errors.Is(statErr, os.ErrNotExist) {
		return handleMissingFile(opts, payload, filePath, stdout)
	}

	_, endIndex := telemetry.Span(ctx, "leonard.post-edit.index")
	indexErr := opts.Indexer.IndexFile(filePath)
	endIndex()

	_, endVet := telemetry.Span(ctx, "leonard.post-edit.vet")
	vet := runVet(ctx, opts, root)
	endVet()

	claim := summariseClaim(payload, filePath, vet, indexErr)
	evidence := buildEvidence(filePath, indexErr, vet, opts.EvidenceCap)
	verified := indexErr == nil && (!vet.Ran || vet.Passed)

	indexOK := indexErr == nil
	rec := ClaimRecord{
		SessionID:       payload.SessionID,
		Claim:           claim,
		Evidence:        evidence,
		FilePath:        filePath,
		Verified:        verified,
		Tool:            payload.ToolName,
		IndexOK:         &indexOK,
		VetOK:           vetOKPtr(vet),
		VetErrorSummary: vetErrorSummary(vet),
	}
	claimID, err := opts.Claims.RecordClaim(rec)
	if err != nil {
		return fmt.Errorf("hooks: record claim: %w", err)
	}
	// A fresh vet=ok + index=ok run on a file resolves any earlier vet=fail
	// claims still on its ledger — fix-and-forget noise stops re-surfacing
	// at stop-time once the file is clean. Supersession failures are
	// non-fatal; the claim row was recorded fine, and a missed supersession
	// just means the older row stays visible until the next clean edit.
	if verified && vet.Ran {
		if _, supErr := opts.Claims.SupersedeClaimsForFile(filePath, claimID); supErr != nil {
			fmt.Fprintf(os.Stderr, "leonard: supersede prior claims for %s: %v\n", filePath, supErr)
		}
	}

	resp := HookResponse{
		Continue:      true,
		SystemMessage: summaryMessage(filePath, vet, indexErr),
	}
	if ctx := modelContext(filePath, vet, indexErr); ctx != "" {
		resp.HookSpecificOutput = &PostToolUseSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: ctx,
		}
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode response: %w", err)
	}
	return nil
}

// handleMissingFile records a claim and emits a response describing the
// PostToolUse event for a file that doesn't exist on disk. IndexOK and VetOK
// are left nil to distinguish "skipped" from a recorded success or failure —
// the tri-state was already reserved for this short-circuit path on
// ClaimRecord.IndexOK. Returns nil unless persistence fails.
func handleMissingFile(opts PostEditOptions, payload PostToolUsePayload, filePath string, stdout io.Writer) error {
	claim := fmt.Sprintf("tool=%s file=%s; index=skipped (file not found); go vet=skipped (file not found)",
		coalesce(payload.ToolName, "edit"), filePath)
	evidence := fmt.Sprintf("file: %s\nfile not found on disk — index and vet skipped\n", filePath)
	rec := ClaimRecord{
		SessionID: payload.SessionID,
		Claim:     claim,
		Evidence:  evidence,
		FilePath:  filePath,
		Verified:  false,
		Tool:      payload.ToolName,
	}
	if _, err := opts.Claims.RecordClaim(rec); err != nil {
		return fmt.Errorf("hooks: record claim: %w", err)
	}
	resp := HookResponse{
		Continue:      true,
		SystemMessage: fmt.Sprintf("leonard: %s file not found, skipping re-index", filePath),
		// Bughunt-4 mcp F5: previously this path set only
		// SystemMessage (user-visible, model-invisible). The model
		// then saw a successful PostToolUse with no signal that its
		// edit produced no on-disk result, and would happily claim
		// the work done. Mirror handleEscapedPath's
		// additionalContext shape so the model sees the no-op.
		HookSpecificOutput: &PostToolUseSpecificOutput{
			HookEventName: "PostToolUse",
			AdditionalContext: fmt.Sprintf(
				"Leonard saw the PostToolUse event for %q but the file is not on disk — your edit may have been rejected by the user, vetoed by another hook, or never landed. Re-check the file's existence before claiming the work is done.",
				filePath,
			),
		},
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode response: %w", err)
	}
	return nil
}

// handleEscapedPath records a claim and emits a response describing
// the PostToolUse event for a file_path that resolves outside the
// project root. Mirrors handleMissingFile's shape — Continue=true,
// verified=false, IndexOK/VetOK nil, plus a system message that
// reaches the user. Security-1 F1: a crafted payload with
// `file_path: /etc/hosts` or `../../other.go` used to skip this
// guard and get indexed as project content.
func handleEscapedPath(opts PostEditOptions, payload PostToolUsePayload, filePath string, stdout io.Writer) error {
	claim := fmt.Sprintf("tool=%s file=%s; index=rejected (path escapes project root); go vet=skipped",
		coalesce(payload.ToolName, "edit"), filePath)
	evidence := fmt.Sprintf("file: %s\nrejected: path resolves outside the project root; index + vet skipped\n", filePath)
	rec := ClaimRecord{
		SessionID: payload.SessionID,
		Claim:     claim,
		Evidence:  evidence,
		FilePath:  filePath,
		Verified:  false,
		Tool:      payload.ToolName,
	}
	if _, err := opts.Claims.RecordClaim(rec); err != nil {
		return fmt.Errorf("hooks: record claim: %w", err)
	}
	resp := HookResponse{
		Continue:      true,
		SystemMessage: fmt.Sprintf("leonard: %s rejected — path escapes project root", filePath),
		HookSpecificOutput: &PostToolUseSpecificOutput{
			HookEventName: "PostToolUse",
			AdditionalContext: fmt.Sprintf(
				"Leonard rejected file_path %q: the path resolves outside the project root. The edit was not indexed. If this was intentional, the change still lives on disk but Leonard's symbol index ignored it.",
				filePath,
			),
		},
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode response: %w", err)
	}
	return nil
}

// modelContext returns the text Claude should see on its next turn, or "" when
// nothing's worth surfacing. A clean run returns "" so a green edit doesn't
// pollute the model's context. An index or vet failure returns a short prefixed
// block — Leonard-tagged so Claude can recognize it as ground-truth feedback
// rather than user text.
func modelContext(filePath string, vet VetResult, indexErr error) string {
	if indexErr == nil && (!vet.Ran || vet.Passed) {
		return ""
	}
	var b strings.Builder
	b.WriteString("Leonard post-edit check on ")
	b.WriteString(filePath)
	b.WriteString(":\n")
	if indexErr != nil {
		fmt.Fprintf(&b, "- symbol index refresh failed: %s\n", indexErr.Error())
	}
	if vet.Ran && !vet.Passed {
		b.WriteString("- go vet ./... FAILED — the edit you just made did not pass vet. Do not claim this work is done until vet is clean.\n")
		summary := vetErrorSummary(vet)
		if summary != "" {
			fmt.Fprintf(&b, "  first error: %s\n", summary)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func decodePayload(r io.Reader) (PostToolUsePayload, error) {
	var p PostToolUsePayload
	body, err := readPayloadBytes(r)
	if err != nil {
		return p, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, fmt.Errorf("%w: empty PostToolUse payload on stdin", ErrDecode)
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("%w: decode PostToolUse payload: %v", ErrDecode, err)
	}
	return p, nil
}

func runVet(ctx context.Context, opts PostEditOptions, root string) VetResult {
	if !hasGoModule(root) {
		return VetResult{Ran: false}
	}
	vctx, cancel := context.WithTimeout(ctx, opts.VetTimeout)
	defer cancel()
	out, err := opts.Vet(vctx, root)
	res := VetResult{Ran: true, Output: filterVetNoise(out)}
	if err == nil {
		res.Passed = true
	} else {
		res.ExitErr = err.Error()
	}
	return res
}

// vetNoisePackages lists header lines (`# <pkg>`) whose entire diagnostic
// block is unconditional environment noise rather than a defect in the
// project's code. shoenig/go-m1cpu emits CGO compiler warnings every run on
// Apple-silicon macs regardless of what changed in the project, burying the
// real vet failure under boilerplate that's identical across runs.
var vetNoisePackages = []string{
	"github.com/shoenig/go-m1cpu",
}

// vetErrorSummaryCap bounds how much of the first vet error line we keep
// in claims.vet_error_summary. Vet messages are usually a single short
// line ("declared and not used: foo") but some include a long type path —
// 200 chars is enough for the meaningful prefix without bloating the row.
const vetErrorSummaryCap = 200

// vetOKPtr returns a *bool for the v3-schema vet_ok column. Nil when vet
// didn't run; otherwise a pointer to vet.Passed. Forwards the same
// tri-state the Claim row uses.
func vetOKPtr(vet VetResult) *bool {
	if !vet.Ran {
		return nil
	}
	b := vet.Passed
	return &b
}

// vetErrorSummary picks the first actionable line out of (already-filtered)
// vet output: skips `# <pkg>` headers, the duplicate `# [<pkg>]` marker Go
// 1.20+ emits, and blank lines. Returns "" when vet passed or didn't run —
// the column is meant to be human-scannable, not a full failure record.
func vetErrorSummary(vet VetResult) string {
	if !vet.Ran || vet.Passed {
		return ""
	}
	for _, raw := range strings.Split(vet.Output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			continue
		}
		if len(line) > vetErrorSummaryCap {
			line = line[:vetErrorSummaryCap]
		}
		return line
	}
	// Output was nothing but headers — fall back to the exit error string
	// so the column isn't empty for an obviously-failed vet run.
	if vet.ExitErr != "" {
		s := vet.ExitErr
		if len(s) > vetErrorSummaryCap {
			s = s[:vetErrorSummaryCap]
		}
		return s
	}
	return ""
}

// filterVetNoise removes diagnostic blocks for packages in vetNoisePackages
// from raw `go vet ./...` combined-output. A block starts at a `# <pkg>`
// header line and extends through every subsequent line up to (but not
// including) the next `# ` header or EOF. Non-noise blocks pass through
// untouched.
func filterVetNoise(out string) string {
	if out == "" {
		return out
	}
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	skip := false
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			pkg := strings.TrimPrefix(line, "# ")
			skip = false
			for _, noise := range vetNoisePackages {
				if pkg == noise || strings.HasPrefix(pkg, noise+" ") {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		} else if skip {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func hasGoModule(root string) bool {
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}

// RunGoVet is the production VetRunner. Replace it in tests by setting
// PostEditOptions.Vet to a stub.
func RunGoVet(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func summariseClaim(p PostToolUsePayload, filePath string, vet VetResult, indexErr error) string {
	parts := []string{
		fmt.Sprintf("tool=%s file=%s", coalesce(p.ToolName, "edit"), filePath),
	}
	if indexErr != nil {
		parts = append(parts, "index=failed")
	} else {
		parts = append(parts, "index=ok")
	}
	if vet.Ran {
		if vet.Passed {
			parts = append(parts, "go vet=ok")
		} else {
			parts = append(parts, "go vet=failed")
		}
	} else {
		parts = append(parts, "go vet=skipped (no go.mod)")
	}
	return strings.Join(parts, "; ")
}

func buildEvidence(filePath string, indexErr error, vet VetResult, cap int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", filePath)
	if indexErr != nil {
		fmt.Fprintf(&b, "index error: %s\n", indexErr.Error())
	}
	switch {
	case !vet.Ran:
		b.WriteString("go vet: skipped (no go.mod at project root)\n")
	case vet.Passed:
		b.WriteString("go vet: ok\n")
		if vet.Output != "" {
			b.WriteString(vet.Output)
			b.WriteString("\n")
		}
	default:
		b.WriteString("go vet: failed\n")
		if vet.ExitErr != "" {
			fmt.Fprintf(&b, "exit: %s\n", vet.ExitErr)
		}
		if vet.Output != "" {
			b.WriteString(vet.Output)
			b.WriteString("\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if cap > 0 && len(out) > cap {
		const trailer = "\n…(truncated)"
		head := cap - len(trailer)
		if head < 0 {
			head = 0
		}
		out = out[:head] + trailer
	}
	return out
}

func summaryMessage(filePath string, vet VetResult, indexErr error) string {
	switch {
	case indexErr != nil:
		return fmt.Sprintf("leonard: re-index of %s failed: %v", filePath, indexErr)
	case !vet.Ran:
		return fmt.Sprintf("leonard: re-indexed %s (go vet skipped, no go.mod)", filePath)
	case vet.Passed:
		return fmt.Sprintf("leonard: re-indexed %s, go vet ok", filePath)
	default:
		return fmt.Sprintf("leonard: re-indexed %s, go vet reported issues — claim recorded as unverified", filePath)
	}
}

func coalesce(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
