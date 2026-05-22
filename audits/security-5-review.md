# Security review #5 — v1.0 release gate

**Status:** PENDING — operator-driven investigation
**Trigger:** v1.0 ground-truth toolkit release (issue #41)
**Prior reviews:** security-1 through security-4 (covered the
v0.52 surface)

This is the focused security review accompanying bughunt-11 for
the v1.0 release. Where bughunt-11 looks for any correctness or
operability issue, security-5 looks specifically for exploitable
attack surface in the v0.6 → v1.0 additions.

---

## Scope

### In scope (v0.6 → v1.0 additions)

#### Ground-truth file parsing
- YAML parsing surface (`facts.yaml`, `filters.yaml`)
- Markdown parsing surface (`stories.md`, `do-not-claim.md`,
  `audit-log.md`)
- Risk: yaml.v3 deserialization gadgets, markdown injection
  affecting downstream renders (MCP tool output, CLI display).
- Specific concerns:
  - Can a malicious `facts.yaml` cause yaml.v3 to allocate
    unbounded memory or invoke arbitrary go code? (Should be
    no per yaml.v3's design.)
  - Can a do-not-claim.md rule with embedded regex
    metacharacters cause the matcher to misfire on unrelated
    content? (Should be no — rules are exact-string + fuzzy,
    not regex.)
  - Can a sensitivity:private entry leak via list_facts when
    include_private:false?

#### Sync plugin sandboxing
- Plugin protocol (#31) executes arbitrary binaries from
  `[sync.<name>].command` in `.leonard/config.toml`.
- v1.0 trust model: operator authorizes by running
  `leonard sync` (CLI invocation is the trust boundary).
- Specific concerns:
  - Can a `.leonard/config.toml` write attack (via Bash
    obfuscation that bypasses the .leonard/ guard) plant a
    malicious sync plugin command that the next operator
    `leonard sync` would execute? **This is the same shape as
    the bughunt-9 verifier-trust attack.** The operator must
    invoke explicitly — does that close the loop?
  - Plugin output isn't sandboxed; atomic facts.yaml write
    happens regardless of content. Should plugins be subject
    to a schema validator on returned updated_facts?

#### Trust system extensions for new adapters
- `WriteAdapterTrust` / `AdapterTrusted` — same XDG_CONFIG_HOME
  placement as the v0.52 verifier trust (#14)
- Per-adapter, per-project; symlink refusal preserved
- Specific concerns:
  - validAdapterName whitelist ([a-z0-9-]) — can a name like
    `../escape` slip through? (Should be no; tested in #14.)
  - Trust marker is "any non-empty content" — can an attacker
    plant a zero-byte file to make AdapterTrusted return false
    (denial-of-service)? Or a non-empty file to grant trust?
    Latter is the real risk; XDG_CONFIG_HOME perms (0o700) are
    the defense.

#### Hot-reload safety (#27)
- 2s polling tick re-parses ground-truth files
- Goroutine lifecycle: channel-parameter pattern (channels passed
  to watchLoop, not read from struct) prevents the Close-nils-
  channel deadlock found during implementation
- Specific concerns:
  - Race between reload's `mu.Lock()` and a hook method's
    `mu.RLock()` — Go's RWMutex documents this as safe but
    confirm under -race
  - Reload runs during a hook? Hook reads `snapshot()` once at
    entry; subsequent reload doesn't affect the in-flight call
  - Parse error keeps prior state — confirm the in-flight hook
    sees consistent state (no torn read)

#### Sync plugin token files (#17, #26)
- Single-use bypass tokens at
  `.leonard/pending-trivial/<sha256(path)[:32]>.json` and
  `.leonard/pending-override/<sha256(path)[:32]>.json`
- SHA-256 truncated to 32 hex chars (128-bit) — collision risk
  acceptable for v1.0 but document
- Specific concerns:
  - Can a token planted by an attacker who can write
    `.leonard/` (via Bash obfuscation) bypass require-tier
    enforcement on a sensitive file? Token deletes on read so
    a planted-then-consumed token logs the bypass; operator
    sees it in pending-decisions.log.

### Out of scope

- v0.52 surface (covered by security-1-4)
- Adapter interface itself (#6) — pure abstraction, no I/O
- Decision-log truth_change column (#21) — column add, no new
  I/O surface

---

## Methodology

Same as security-3/4:

1. **Trace each attack surface end-to-end.** Where does the data
   come from (operator file, Claude tool input, plugin stdout)?
   Where does it land (memory, disk, exec)?
2. **Identify trust boundaries.** Which transitions require
   explicit operator authorization?
3. **Probe each boundary.** Can it be crossed without
   authorization? With clever input?
4. **Document findings** in `audits/security-5-findings.md`
   (not yet created).

---

## Acceptance

- [ ] Each in-scope area investigated
- [ ] Per-area findings logged with severity (CRITICAL / HIGH /
      MEDIUM / LOW)
- [ ] Every HIGH/CRITICAL closed (test + fix + v0.X bump) before
      v1.0 tag
- [ ] CHANGELOG.md entry references security-5 and lists closures
- [ ] No HIGH/CRITICAL findings open at v1.0 tag

---

## Prior security reviews (for reference)

| Review | Trigger | Findings (closed) |
|---|---|---|
| security-1 | initial hardening | 4 |
| security-2 | v0.46 launch surface | 3 |
| security-3 | v0.50 bash bypass class | 5 |
| security-4 | v0.51 verifier trust | 4 |
| **security-5** | **v1.0 release gate** | **pending** |
