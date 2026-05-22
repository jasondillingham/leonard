// Package groundtruth is the ground-truth Adapter implementation.
//
// v0.6 (issue #8) ships the parser layer: at Init the adapter reads
// the five-file tree from .leonard/ground-truth/ and holds the parsed
// structures in memory for later issues' hook + MCP logic to consume.
//
// The five files (see docs/ROADMAP-v1-ground-truth.md#the-ground-truth-schema):
//
//   - facts.yaml          what IS true (positive-space truth source)
//   - stories.md          canonical phrasings of common claims
//   - do-not-claim.md     forbidden claims / hard rejection list
//   - filters.yaml        strategic rules for what work gets done
//   - audit-log.md        append-only ledger of claim verifications
//
// All five are optional. A missing file is treated as "no entries for
// that category" — operators can ship a partial tree (e.g., only
// facts.yaml + do-not-claim.md) and the adapter degrades cleanly.
// Malformed files produce errors with line numbers where possible.
//
// In v0.6 the hook methods are no-ops:
//
//   - PreEdit returns Pass    (hard-deny lands in #23)
//   - PostEdit returns empty  (advisory pending-audit.log lands in #18;
//                              auto-append to audit-log.md lands in #29)
//   - SessionStart returns empty
//   - Stop returns empty
//   - RegisterTools is a no-op (verify_claim/list_facts/get_story
//                              MCP tools land in #10–#12)
//
// Coexistence: the adapter registers itself as "ground-truth" in the
// global registry; the dispatcher (deferred to issue #46) will load it
// alongside the code adapter when both are enabled in
// .leonard/config.toml.
package groundtruth
