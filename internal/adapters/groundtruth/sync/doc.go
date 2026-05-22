// Package sync defines the external sync-plugin protocol introduced
// in v0.9 (#31).
//
// A sync plugin is any executable that follows the JSON-stdin/
// JSON-stdout contract described below. Plugins refresh facts.yaml
// against an authoritative source (GitHub PR statuses, customer
// CRM, monitoring dashboards, etc.) so Leonard's claim verifier
// stays current without operator effort.
//
// Plugin protocol
//
// On stdin, the plugin receives a JSON object:
//
//	{
//	  "facts":  { ... subset of facts.yaml the plugin operates on ... },
//	  "config": { ... per-plugin config from .leonard/config.toml ... }
//	}
//
// On stdout, the plugin must emit a JSON object:
//
//	{
//	  "updated_facts": { ... refreshed subset ... },
//	  "changes": [
//	    { "path": "...", "old": ..., "new": ..., "reason": "..." }
//	  ]
//	}
//
// Exit 0 indicates success. Non-zero exits are surfaced to the
// operator with whatever the plugin wrote to stderr.
//
// Trust
//
// v0.9 ships the plugin runner without a persistent trust file
// (the CLI's `leonard sync` subcommand is the trust boundary —
// the operator explicitly invokes a plugin, no automatic
// invocation). A follow-up may extend `leonard config trust` to
// fingerprint plugin paths for the same SHA-256-fingerprint
// protection the verifier command has.
//
// Failure semantics
//
// Plugin failures must not corrupt facts.yaml. The runner writes
// the refreshed facts to a temp file and renames atomically only
// after the plugin returns success. On any error, facts.yaml is
// untouched.
package sync
