package groundtruth

import "fmt"

// Action is the dispatcher's response to a hook event involving a
// matched rule. v0.6 carries the enum but only "reject" / "warn" /
// "log-only" / "ignore" are recognized; the hook logic that branches
// on these lives in later issues.
type Action string

const (
	// ActionReject blocks the edit and surfaces the rule citation to
	// Claude. The default for forbidden-claim hits.
	ActionReject Action = "reject"

	// ActionWarn logs the finding but proceeds. The default for
	// unverified-claim hits.
	ActionWarn Action = "warn"

	// ActionLogOnly writes the finding to the audit log silently.
	ActionLogOnly Action = "log-only"

	// ActionIgnore drops the finding entirely. Used to disable a
	// category temporarily without removing the rule files.
	ActionIgnore Action = "ignore"
)

// Config is the adapter-specific subset of .leonard/config.toml's
// [[adapters]] entry. Decoded from adapters.Config.Raw at Init.
type Config struct {
	// TruthDir is the path to the ground-truth tree, relative to the
	// project root. Defaults to "ground-truth/" when unset.
	TruthDir string

	// VerifyTargets is the list of glob patterns that name files
	// whose content should be claim-checked. v0.6 stores it but
	// doesn't act on it. Default: ["*.md", "*.txt", "*.yaml"].
	VerifyTargets []string

	// ForbiddenAction is what the dispatcher should do when a
	// proposed edit matches a do-not-claim.md rule. Default
	// ActionReject. Honored by #23.
	ForbiddenAction Action

	// UnverifiedAction is what the dispatcher should do when a
	// proposed edit makes a claim with no matching facts.yaml entry.
	// Default ActionWarn. Honored by #18.
	UnverifiedAction Action

	// OpinionHandling controls whether preference/value statements
	// ("I believe...", "I prefer...") get logged. Default
	// ActionIgnore. Honored by #18.
	OpinionHandling Action

	// ExemptPaths is the operator-configurable allowlist of glob
	// patterns relative to projectRoot whose contents are exempt
	// from the do-not-claim matcher. Files INSIDE the truth tree
	// (truth_dir) are exempt automatically — they contain the
	// rules themselves and would otherwise trip the matcher on
	// their own quoted phrases (#84). ExemptPaths covers other
	// meta-files (project planning, dogfood logs, audit notes)
	// that legitimately reference forbidden phrases as commentary
	// rather than as claims (#8).
	//
	// Globs match against projectRoot-relative paths via
	// filepath.Match. Examples:
	//   - "leonard-dogfood.md"     (a specific file)
	//   - "docs/audits/*.md"       (a directory of files)
	//   - "**/notes.md"            (any file named notes.md)
	ExemptPaths []string
}

// defaultConfig returns the zero-config defaults applied when an
// [[adapters]] block is absent or only partially specified.
func defaultConfig() Config {
	return Config{
		// Default lives under .leonard/ so a project that hasn't
		// configured truth_dir gets the v0.6 layout. Operators who
		// want their tree at the project root (e.g. "source-of-
		// truth/") set truth_dir explicitly.
		TruthDir:         ".leonard/ground-truth/",
		VerifyTargets:    []string{"*.md", "*.txt", "*.yaml"},
		ForbiddenAction:  ActionReject,
		UnverifiedAction: ActionWarn,
		OpinionHandling:  ActionIgnore,
	}
}

// decodeConfig pulls adapter fields out of the raw map (as decoded
// from TOML into adapters.Config.Raw). Missing keys take their
// defaults. Unknown keys are ignored — the toml decoder already
// dropped them. Returns an error only when a known key has a value of
// the wrong type, so operator typos in TOML produce clear messages
// instead of silent defaults.
func decodeConfig(raw map[string]any) (Config, error) {
	cfg := defaultConfig()

	if v, ok := raw["truth_dir"]; ok {
		s, ok := v.(string)
		if !ok {
			return cfg, fmt.Errorf("groundtruth: truth_dir must be a string, got %T", v)
		}
		cfg.TruthDir = s
	}

	if v, ok := raw["verify_targets"]; ok {
		list, ok := v.([]any)
		if !ok {
			return cfg, fmt.Errorf("groundtruth: verify_targets must be a list of strings, got %T", v)
		}
		out := make([]string, 0, len(list))
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				return cfg, fmt.Errorf("groundtruth: verify_targets[%d] must be a string, got %T", i, e)
			}
			out = append(out, s)
		}
		cfg.VerifyTargets = out
	}

	if v, ok := raw["exempt_paths"]; ok {
		list, ok := v.([]any)
		if !ok {
			return cfg, fmt.Errorf("groundtruth: exempt_paths must be a list of strings, got %T", v)
		}
		out := make([]string, 0, len(list))
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				return cfg, fmt.Errorf("groundtruth: exempt_paths[%d] must be a string, got %T", i, e)
			}
			out = append(out, s)
		}
		cfg.ExemptPaths = out
	}

	for _, m := range []struct {
		key string
		dst *Action
	}{
		{"forbidden_action", &cfg.ForbiddenAction},
		{"unverified_action", &cfg.UnverifiedAction},
		{"opinion_handling", &cfg.OpinionHandling},
	} {
		v, ok := raw[m.key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return cfg, fmt.Errorf("groundtruth: %s must be a string, got %T", m.key, v)
		}
		a, err := parseAction(s)
		if err != nil {
			return cfg, fmt.Errorf("groundtruth: %s: %w", m.key, err)
		}
		*m.dst = a
	}

	return cfg, nil
}

func parseAction(s string) (Action, error) {
	switch Action(s) {
	case ActionReject, ActionWarn, ActionLogOnly, ActionIgnore:
		return Action(s), nil
	default:
		return "", fmt.Errorf("unknown action %q (expected one of: reject, warn, log-only, ignore)", s)
	}
}
