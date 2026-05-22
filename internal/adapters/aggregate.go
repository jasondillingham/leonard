package adapters

import "strings"

// sessionStartSeparator delimits per-adapter blocks in the aggregated
// SessionStart payload. Matches the convention used elsewhere in
// Leonard's markdown surfaces.
const sessionStartSeparator = "\n\n---\n\n"

// AggregatePreEdit collapses per-adapter PreEdit results into the
// single verdict the dispatcher returns to Claude Code. The rule is
// deny-beats-pass: if ANY adapter denied, the final decision is Deny
// and the first such adapter's Reason is reported. Ask is collapsed
// to Deny in v0.6 (see Decision docs).
//
// The function preserves AdapterName from the deciding adapter so
// telemetry and error messages can attribute the deny.
func AggregatePreEdit(results []PreEditResult) PreEditResult {
	for _, r := range results {
		if r.Decision == Deny || r.Decision == Ask {
			return PreEditResult{
				Decision:    Deny,
				Reason:      r.Reason,
				AdapterName: r.AdapterName,
			}
		}
	}
	return PreEditResult{Decision: Pass}
}

// AggregatePostEdit concatenates per-adapter PostEdit contributions in
// registration order. AdditionalContext blocks are joined with blank
// lines; SystemMessage lines are joined with newlines; Claims slices
// are appended. Empty contributions are dropped (no leading/trailing
// blank-line noise in the merged context).
func AggregatePostEdit(results []PostEditResult) PostEditResult {
	var contexts []string
	var messages []string
	var claims []ClaimRecord
	for _, r := range results {
		if r.AdditionalContext != "" {
			contexts = append(contexts, r.AdditionalContext)
		}
		if r.SystemMessage != "" {
			messages = append(messages, r.SystemMessage)
		}
		claims = append(claims, r.Claims...)
	}
	return PostEditResult{
		AdditionalContext: strings.Join(contexts, "\n\n"),
		SystemMessage:     strings.Join(messages, "\n"),
		Claims:            claims,
	}
}

// AggregateSessionStart joins per-adapter SessionStart markdown blocks
// with a "---" separator so the model sees a clear boundary between
// adapter contributions. Empty blocks are dropped so a no-op adapter
// doesn't insert a bare "---" with nothing above or below it.
func AggregateSessionStart(results []SessionStartResult) SessionStartResult {
	var blocks []string
	for _, r := range results {
		if r.AdditionalContext != "" {
			blocks = append(blocks, r.AdditionalContext)
		}
	}
	return SessionStartResult{
		AdditionalContext: strings.Join(blocks, sessionStartSeparator),
	}
}

// AggregateStop joins per-adapter Stop SystemMessage strings with
// newlines so the operator sees one combined surface at session end.
// Empty messages are dropped.
func AggregateStop(results []StopResult) StopResult {
	var lines []string
	for _, r := range results {
		if r.SystemMessage != "" {
			lines = append(lines, r.SystemMessage)
		}
	}
	return StopResult{
		SystemMessage: strings.Join(lines, "\n"),
	}
}
