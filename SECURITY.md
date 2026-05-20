# Security Policy

## Threat model

Leonard runs locally, per-project, against source code the user has chosen to expose to Claude Code. The threat model focuses on:

- **Confused-deputy bugs**: a malicious or confused Claude Code session emitting hook payloads or MCP tool calls that get Leonard to act outside its intended scope (e.g. indexing files outside the project root, executing commands in unintended directories).
- **Resource exhaustion**: a payload that causes Leonard's helpers to OOM or pegs CPU.
- **Information disclosure**: anything that exposes data from one project's `.leonard/leonard.db` to another, or leaks symbols across project boundaries.

Out of scope:

- Threats requiring Claude Code itself to be compromised — Leonard treats Claude Code as a trusted-enough caller (its commands are user-initiated).
- Multi-user / multi-tenant attacks — Leonard is single-user by design.
- Threats requiring root or shell access to the local machine — attackers with that level of access can already do anything.

## Existing guards

The bug-hunt + security-review discipline has produced several concrete guards:

| Guard | Versions | What it protects against |
|---|---|---|
| Path-trust sweep (`ResolveSafe`) | v0.8, v0.14 | confused-deputy `file_path: /etc/hosts` payloads; symlink-out escapes; NFC/NFD normalization collisions |
| Resource caps (per-payload, per-snippet, per-element-count, per-response) | v0.9, v0.13 | OOM via crafted PreToolUse payloads; runaway MCP response sizes |
| Custom line reader replacing `bufio.Scanner` | v0.13 | busy-spin DoS on oversize stdin lines |
| MCP stdin filter | v0.6 | wrong-version / malformed JSON-RPC frames killing the transport |
| `[post_edit.verify]` config is operator-authored only | v0.37, v0.46 | command injection. v0.46 closed the security-2 F1 chain: pre-edit hook now rejects Edit/Write/MultiEdit against any path under `.leonard/`, so Claude cannot author the verifier's `command` field. The directory is reserved for operator wiring + the SQLite store. |
| Ledger hygiene (vet_ok-based supersede, no missing-file claims) | v0.38, v0.39 | ledger pollution / supersede mis-classification |
| `working_dir` path-trust validation | v0.46 | confused-deputy. `[post_edit.verify].working_dir` values are validated via `ResolveSafe` against the project root; escapes fall back to the project root with a captured-output note. |
| `idx_symbols_parent` and other store indexes | v0.7.1, v0.15 | DoS via expensive queries |

See [`audits/security-1-review.md`](./audits/security-1-review.md) for the full security review and the [`audits/`](./audits/) directory for finding-by-finding history across the five bug-hunt rounds.

## Reporting a vulnerability

If you find a security-sensitive issue:

1. **Don't open a public issue.** Instead, email the project maintainer or use GitHub's [Private Vulnerability Reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) feature on this repo.
2. Include a reproducer if possible — a minimal config or hook payload that triggers the bad behavior.
3. Describe the threat model angle: confused-deputy? Resource exhaustion? Cross-project leak? Other?

We aim to acknowledge reports within 7 days. Critical issues get a v0.X.0 release as soon as the fix passes the bug-hunt discipline (regression test + commit message linking the finding).

## Disclosure timeline

- We don't have a formal disclosure window — Leonard is a small-audience local-first tool, not a service with users to coordinate with. We'll fix and release; the CHANGELOG.md entry will describe the issue.
- If the finding is in a transitive dependency (e.g. a tree-sitter grammar, modernc.org/sqlite, the MCP SDK), we'll coordinate with that upstream and document the workaround until it's fixed.

## Bug-hunt-style audits welcome

If you'd like to run a security-focused bug hunt against Leonard, the format is documented in [`audits/security-1-review.md`](./audits/security-1-review.md) + the round-N triage files under `audits/`. Findings filed in that shape are easy to action.
