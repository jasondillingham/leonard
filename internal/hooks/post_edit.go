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
)

// Indexer is the minimum surface internal/index.Indexer must satisfy for the
// post-edit hook. Keeping the contract local lets us unit-test without
// pulling the parser lane in as a dependency.
type Indexer interface {
	IndexFile(path string) error
}

// ClaimRecorder is the minimum surface internal/store.Store must satisfy for
// the post-edit hook. RecordClaim persists the row; SupersedeClaimsForFile
// links prior unverified rows for filePath to a fresh vet=ok claim so
// stop-time output focuses on failures the next edit didn't already fix.
type ClaimRecorder interface {
	RecordClaim(sessionID, claim, evidence, filePath string, verified bool) (int64, error)
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
// blocks: it always sets Continue=true and exits 0. SystemMessage carries a
// short summary that Claude Code surfaces to the user.
type HookResponse struct {
	Continue       bool   `json:"continue"`
	SuppressOutput bool   `json:"suppressOutput,omitempty"`
	SystemMessage  string `json:"systemMessage,omitempty"`
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

	indexErr := opts.Indexer.IndexFile(filePath)

	vet := runVet(ctx, opts, root)

	claim := summariseClaim(payload, filePath, vet, indexErr)
	evidence := buildEvidence(filePath, indexErr, vet, opts.EvidenceCap)
	verified := indexErr == nil && (!vet.Ran || vet.Passed)

	claimID, err := opts.Claims.RecordClaim(payload.SessionID, claim, evidence, filePath, verified)
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
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode response: %w", err)
	}
	return nil
}

func decodePayload(r io.Reader) (PostToolUsePayload, error) {
	var p PostToolUsePayload
	body, err := io.ReadAll(r)
	if err != nil {
		return p, fmt.Errorf("hooks: read stdin: %w", err)
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
