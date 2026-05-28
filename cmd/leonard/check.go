package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// newCheckCmd implements the v1.0 `leonard check <file>` CLI (#37).
// Roadmap nomenclature called it `leonard verify <file>` but the
// existing v0.52 surface uses `leonard verify <symbol>` for the
// symbol-index lookup, so the file-verification command lives under
// `check` to keep the existing CLI non-breaking.
func newCheckCmd() *cobra.Command {
	var (
		adapterFilter string
		format        string
	)
	cmd := &cobra.Command{
		Use:   "check <file>",
		Short: "Run all enabled adapters' checks against a file (read-only).",
		Long: `Reads the file at <file>, runs every enabled adapter's check
against it, and reports per-claim verdicts. Never modifies the
target file or any ground-truth file.

Exit code:
  0  no findings
  1  unverified findings (warn)
  2  forbidden findings (reject)
  >2 error

Flags:
  --adapter=ground-truth    restrict to one adapter
  --format=plain|json       output format (default plain)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			absTarget := target
			if !filepath.IsAbs(absTarget) {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				absTarget = filepath.Join(cwd, target)
			}

			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			content, err := os.ReadFile(absTarget)
			if err != nil {
				return fmt.Errorf("check: read %s: %w", absTarget, err)
			}

			switch adapterFilter {
			case "", "ground-truth":
				// allowed
			default:
				return fmt.Errorf("check: --adapter must be empty or 'ground-truth' (got %q)", adapterFilter)
			}

			switch format {
			case "", "plain", "json":
				// allowed
			default:
				return fmt.Errorf("check: --format must be 'plain' or 'json' (got %q)", format)
			}

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				return fmt.Errorf("check: %w", err)
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				return fmt.Errorf("check: init ground-truth adapter: %w", err)
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			res := gta.Detect(string(content))
			renderCheckResult(cmd.OutOrStdout(), absTarget, content, res, format)

			switch {
			case res.Summary.Forbidden > 0:
				return &exitCode{code: 2}
			case res.Summary.Unverified > 0:
				return &exitCode{code: 1}
			default:
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&adapterFilter, "adapter", "", "restrict to one adapter (currently only ground-truth is supported)")
	cmd.Flags().StringVar(&format, "format", "plain", "output format: plain | json")
	return cmd
}

func renderCheckResult(out io.Writer, path string, content []byte, res groundtruth.DetectionResult, format string) {
	if format == "json" {
		// Reuse the verify_claim wire shape so jq pipelines are
		// portable across MCP and CLI surfaces.
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"file":    path,
			"claims":  claimsToWire(res.Claims),
			"summary": res.Summary,
		})
		return
	}

	if len(res.Claims) == 0 {
		fmt.Fprintf(out, "%s: clean (no findings)\n", path)
		return
	}
	fmt.Fprintf(out, "%s: %d finding(s) — %d forbidden, %d unverified, %d verified, %d opinion\n",
		path, res.Summary.Total, res.Summary.Forbidden, res.Summary.Unverified, res.Summary.Verified, res.Summary.Opinion)

	// Sort claims by start byte for stable output.
	claims := make([]groundtruth.Claim, len(res.Claims))
	copy(claims, res.Claims)
	sort.Slice(claims, func(i, j int) bool {
		return claims[i].StartByte < claims[j].StartByte
	})

	for _, c := range claims {
		var detail string
		switch c.Verdict {
		case groundtruth.VerdictForbidden:
			detail = fmt.Sprintf("rule=%s", c.RulePath)
		case groundtruth.VerdictVerified:
			detail = fmt.Sprintf("evidence=%s", c.EvidencePath)
		case groundtruth.VerdictOpinion:
			detail = "preference/value statement"
		}
		line := byteToLine(content, c.StartByte)
		fmt.Fprintf(out, "  [%s] %s — %q  (line %d)  %s\n",
			strings.ToUpper(c.Verdict.String()),
			c.Category,
			truncate(c.Text, 80),
			line,
			detail,
		)
	}
}

// byteToLine returns the 1-based line number for the given byte offset in src.
func byteToLine(src []byte, offset int) int {
	if offset <= 0 || len(src) == 0 {
		return 1
	}
	if offset > len(src) {
		offset = len(src)
	}
	return 1 + bytes.Count(src[:offset], []byte{'\n'})
}

// claimsToWire mirrors the verify_claim MCP tool's wire schema so
// `leonard check --format=json` and the MCP tool emit the same
// per-claim shape.
func claimsToWire(claims []groundtruth.Claim) []map[string]any {
	out := make([]map[string]any, 0, len(claims))
	for _, c := range claims {
		entry := map[string]any{
			"text":       c.Text,
			"category":   c.Category,
			"verdict":    c.Verdict.String(),
			"start_byte": c.StartByte,
			"end_byte":   c.EndByte,
		}
		if c.EvidencePath != "" {
			entry["evidence_path"] = c.EvidencePath
		}
		if c.RulePath != "" {
			entry["rule_path"] = c.RulePath
			entry["rule_text"] = c.RuleText
		}
		if c.Note != "" {
			entry["note"] = c.Note
		}
		out = append(out, entry)
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// notExistsHint annotates fs.ErrNotExist so a missing-file invocation
// gets a clear operator message.
func init() {
	_ = errors.New // keep errors package referenced even if future
	//               edits drop the only call site
}
