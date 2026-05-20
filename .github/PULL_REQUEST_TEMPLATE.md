<!--
Thanks for the PR. CONTRIBUTING.md has the bug-hunt → triage → fix
discipline; PRs that fit that shape land fast.
-->

## Summary

<!-- 1-3 bullets. What changes, and why. -->

## Test plan

<!--
- [ ] go vet ./...
- [ ] go test ./...
- [ ] go test -race ./...
- [ ] go test -tags otel ./...
- [ ] (if Rust touched) cargo build --release in internal/parse/rust and/or internal/parse/treesitter
- [ ] If this fixes a bug-hunt finding, reference the original ID in the commit message
-->

## Compatibility / migration notes

<!--
Does this require a re-index? Does it change the on-wire MCP
schema? Does it affect the symbol qname shape? If yes, call it out
explicitly — those are the changes most likely to bite existing
dogfooded projects.
-->
