# Leonard v1.0 Roadmap — Generalized Ground-Truth Toolkit

**Status:** Design proposal (not yet implementation)
**Created:** 2026-05-21
**Target shipping:** v1.0 (estimated 4-6 weeks of development across v0.6–v1.0)
**Author:** Jason Dillingham
**Audience:** Any contributor or Claude Code session picking up this work

---

## TL;DR

Leonard today (v0.52) is a ground-truth toolkit for **code symbols** — its `symbols.db`, pre-edit hook, post-edit verifier, and claim ledger all exist to prevent agents from hallucinating function names, paths, and types in code.

But hallucination is broader than code. Agents drift from truth in any domain where there's a knowable ground-truth: customer details, product features, regulatory claims, marketing copy, internal documentation, personal facts. The existing Leonard primitives are domain-agnostic — they're just plumbed only to code today.

This roadmap generalizes Leonard from "code ground-truth" to **"pluggable ground-truth"**: per-project domain adapters that share Leonard's existing pre-edit/post-edit/SessionStart hook architecture, claim ledger, decision log, MCP tool surface, and trust system. Code adapter keeps working unchanged. A new "ground-truth" adapter ships in v0.6 and matures through v1.0, enabling Leonard to guard prose, claims, facts, and strategic rules across any domain a user defines.

Net result: Leonard's market expands from "developers using Claude Code" to "anyone using AI for prose where claims matter" — technical writers, marketing, sales, compliance, legal, founders, analysts. Same primitives, broader domain.

---

## Why

### The drift problem isn't unique to code

Leonard exists because agents claim function names that don't exist, claim edits they didn't make, and write plausible-looking code that doesn't compile. The shape of the problem:

1. There's a ground-truth somewhere (the codebase, the symbol table).
2. The agent doesn't actually consult it before claiming.
3. The output looks plausible enough that a human reviewer might not catch it.
4. Compounded over many edits, drift becomes invisible.

Now consider non-code domains:

- **Marketing claims about a product**: agent writes a landing-page paragraph claiming a feature the product doesn't have. Plausible. Hard to catch in review. Compounds across the site.
- **Sales messaging about a customer**: agent drafts an outreach email referencing the customer's stated needs from a Slack thread the agent half-remembered. Plausible. Hard to catch. Compounds across the pipeline.
- **Compliance documentation**: agent writes an SOC 2 control narrative claiming controls that aren't actually implemented. Plausible. Hard to catch. Compounds across audit cycles.
- **Personal claims in artifacts** (résumés, blogs, public profiles): agent writes a confident sentence about an experience the human doesn't actually have. Plausible. Hard to catch. Compounds across opportunities.

All of these share the same structure as the code hallucination problem. **The fix is structurally identical: a ground-truth source, a mechanical verifier, a hard rejection path for forbidden claims, and an append-only audit ledger.**

### Why Leonard is the right place to solve this

- Leonard already ships the primitives: pre-edit hook, post-edit verifier, SessionStart hook, claim ledger, decision log, MCP tool surface, trust system, per-project config.
- Leonard already enforces an operator-trust model: verifiers require explicit `leonard config trust` authorization by SHA-256 fingerprint. The same model extends naturally to domain-truth adapters.
- Leonard is local-first and self-hosted, so the ground-truth never leaves the user's machine — critical for sensitive domains (legal, compliance, personal).
- Leonard already integrates with Claude Code via MCP and hooks — the existing integration channel works for any domain.

The only change required is generalizing the "what is truth" component from "code symbols" to "configurable domain source."

### What this is NOT

- This is not an LLM-classifier-for-prose project. Claim detection will start with regex + heuristic (fast, deterministic) and can layer in LLM-based extraction later if needed.
- This is not a knowledge-graph project. Ground-truth is a small set of YAML/Markdown files, not a graph database. Simple, hand-editable, version-controllable.
- This is not a replacement for code-symbol verification. Both adapters coexist; some projects use one, some use both.
- This is not cloud-dependent. Everything runs locally. No external service required (auto-sync plugins for things like GitHub API are optional adapters).

---

## Current state (Leonard v0.52)

Reference baseline so any contributor knows what's already there.

### Binaries

| Binary | Purpose |
|---|---|
| `leonard` | CLI: `init`, `index`, `config`, `--version` |
| `leonard-mcp` | MCP server exposed to Claude Code via `.claude/settings.local.json` |
| `leonard-hook` | Hook bridge — invoked from Claude Code hooks (PreToolUse, PostToolUse, SessionStart, Stop) with subcommands `pre-edit`, `post-edit`, `session-start`, `stop` |

### Per-project artifacts

```
.leonard/
├── config.toml         # project config
├── symbols.db          # symbol index built from source tree
└── (decisions/, ledger entries — verify current internal structure in code)
```

### Existing config schema (excerpt)

```toml
# .leonard/config.toml — example after `leonard init`

[post_edit.verify]
command = "go vet ./..."     # default for Go; auto-detected from go.mod
timeout = "60s"
```

Verifier must be authorized via `leonard config trust` (v0.51+). Trust file at `$XDG_CONFIG_HOME/leonard/trust/<sha256-of-project-root>.sha256` (v0.52 moved this out of `.leonard/` to prevent `.leonard/`-write attacks from poisoning trust).

### Existing MCP tools (verify exact list in `leonard-mcp` source)

- `verify_symbol(name)` — does this symbol exist?
- `find_symbol(query=...)` — search the index
- `get_decisions()` — recall prior decisions

### Existing hooks (registered via Claude Code's `.claude/settings.local.json`)

- **PreToolUse** (matcher: `Edit|Write|MultiEdit|NotebookEdit|Bash`) → `leonard-hook pre-edit`
- **PostToolUse** (matcher: `Edit|Write|MultiEdit`) → `leonard-hook post-edit`
- **SessionStart** → `leonard-hook session-start`
- **Stop** → `leonard-hook stop`

### Existing security surface

- Path-trust guard: rejects `file_path` values that resolve outside the project root (confused-deputy fix)
- Resource caps: payload 16 MiB, snippet 1 MiB, MultiEdit element-count 100
- Trust system: verifier commands require explicit operator authorization
- Two focused security reviews + six bug-hunt rounds; every HIGH/CRITICAL finding closed

### Language adapter status

- 29 languages via tree-sitter
- 4 production-dogfooded parsers: Go, TypeScript, Python (via host `ast`), Rust (via `syn`)

---

## Architectural shift

### Today

Leonard has ONE concept of "ground-truth": the symbol index built by `leonard index`. All hooks and tools assume code as the domain.

### Future

Leonard has a **pluggable adapter system**. Each adapter is a self-contained domain implementation that registers:

1. What it considers ground-truth (where it reads from)
2. What it indexes / validates against
3. What it exposes as MCP tools
4. What its pre-edit / post-edit hooks check
5. What it surfaces at SessionStart

Adapters run independently and can coexist. A project can have:

- Just the **code adapter** (Leonard today's behavior)
- Just the **ground-truth adapter** (prose / claims project — no code symbols needed)
- **Both** active simultaneously (codebase WITH a product-context truth source)
- Future adapters: customer-data, compliance-rules, regulatory, etc.

### Schema-level commitment

This roadmap commits to a **stable five-file schema** for the first non-code adapter (the ground-truth adapter). This schema is intentionally generic — designed to work for any domain a user wants to express truth in.

The same five-file schema becomes the **template** for any future adapter that maps onto the "facts / quotables / anti-claims / strategic rules / audit log" pattern. Future adapters (e.g., customer-data with structured API sync) may add their own fields, but the five-file pattern is the convention.

---

## The ground-truth schema

```
.leonard/
├── config.toml                  # adapter selection + per-adapter config
├── symbols.db                   # if code adapter active
└── ground-truth/                # if ground-truth adapter active
    ├── facts.yaml               # what IS true
    ├── stories.md               # canonical phrasings of common claims/narratives
    ├── do-not-claim.md          # what is NOT true / NOT allowed
    ├── filters.yaml             # strategic rules that affect what gets done
    └── audit-log.md             # append-only ledger
```

### File 1: `facts.yaml`

**Purpose:** Things that ARE TRUE about the project's domain. The positive-space truth source.

**Format:** YAML, hierarchical, hand-editable. Schema is intentionally flexible — the verifier reads it as structured data and looks up claims by path. Convention is to organize by category at the top level.

**Generic example fields** (each domain defines its own categories):

```yaml
# Product domain
product:
  name: ExampleSaaS
  features:
    - id: feature-search
      name: Universal search
      shipped: 2025-09-15
      tier: pro
    - id: feature-export
      name: CSV export
      shipped: 2025-11-02
      tier: free
  pricing:
    free_tier_users: 1
    pro_tier_users: 5
    enterprise_tier_users: unlimited

# Customer domain
customers:
  - id: acme-corp
    name: Acme Corp
    tier: enterprise
    last_renewal: 2026-01-15
    contract_value_usd: 50000
    primary_contact: jane@acme.example
    sensitivity: standard
```

**Per-entry metadata (recommended convention):**

- `last_verified: YYYY-MM-DD` — when this fact was last checked against authoritative source
- `source: <url-or-ref>` — citation / where this came from
- `sensitivity: public | internal | private` — who can see this surface in artifacts

### File 2: `stories.md`

**Purpose:** Canonical phrasings of common claims, narratives, talking points. When an agent drafts something that references one of these, the agent uses the canonical phrasing rather than reinventing.

**Format:** Markdown with sections per story. Each story can have a short version (1-3 sentences) and a long version (paragraph), plus anti-drift notes.

**Generic example:**

```markdown
## STORY: 2024 product launch

### Short version (~40 words)
ExampleSaaS launched Pro tier in March 2024. Within 90 days, 200+ teams
had upgraded. We grew from $0 to $50K MRR by the end of Q2.

### Long version (paragraph)
In March 2024, after 18 months of free-tier-only operation, we launched
the Pro tier with team collaboration features. The launch hit 200+
upgrades in 90 days and brought us to $50K MRR by end of Q2 2024.
The decision to gate team features (rather than per-seat pricing) came
from a customer interview series in late 2023 — see decision log entry
2023-12-04.

### Do NOT drift
- Don't claim "Pro tier launched January 2024" — actual date is March 2024
- Don't claim "we have N enterprise customers" without checking facts.yaml
- Don't quote MRR figures from before March 2024 — we were on free tier

### Sensitivity
Public-safe.
```

### File 3: `do-not-claim.md`

**Purpose:** Things that are NOT TRUE or NOT ALLOWED to be claimed. Hard rejection list for the pre-edit guard.

**Format:** Markdown with bulleted anti-claims and reasons. The verifier scans proposed text for patterns matching these; matches get rejected (or warned about) per project policy.

**Generic example:**

```markdown
# Do Not Claim

## Product capability gaps

- ❌ "ExampleSaaS supports HIPAA-compliant workflows" — We are NOT HIPAA-
  certified. Customers may need to add their own BAA layer. Removing this
  claim from anywhere we say it.
- ❌ "We have a mobile app" — No native mobile app. We have a responsive
  web UI. Don't conflate.
- ❌ "Real-time collaboration" — Our sync is eventually-consistent with
  5-30s lag depending on tier. Don't claim "real-time" or "instant."

## Customer relationship rules

- ❌ "Acme Corp uses ExampleSaaS for [unstated workflow]" — Don't claim
  specific customer workflows in public artifacts unless Acme has
  explicitly approved a case study. Check customer-approvals.yaml.
- ❌ Public pricing or contract values for any customer — Always internal.

## Compliance / legal

- ❌ "We comply with [framework] regulations" without specific framework
  name and verification — Always require explicit citation.
```

### File 4: `filters.yaml`

**Purpose:** Strategic rules that affect what gets done. Where `do-not-claim` is "don't say this," `filters.yaml` is "don't do this work in the first place."

**Format:** YAML with named filter categories. Used at scaffolding-time or action-time, before content gets drafted.

**Generic example:**

```yaml
# Customer engagement filters
customer_engagement:
  forbidden_customers:
    - id: acme-corp
      reason: "Contract terminated 2026-03-15; no further outreach"
      forbidden_until: 2027-01-01
    - id: beta-co
      reason: "Legal hold; outreach must route through counsel"
      forbidden_until: indefinite

# Product messaging filters
product_messaging:
  forbidden_channels:
    - twitter  # Brand voice not yet defined; await marketing lead
  required_disclosures:
    - context: "Public claims about uptime"
      disclosure: "Past-90-days uptime % must be cited from facts.yaml"
```

### File 5: `audit-log.md`

**Purpose:** Append-only ledger. Per artifact, what claims were made and what their verification status was. This is the prose-domain analog of Leonard's existing code-claim ledger.

**Format:** Markdown with dated sections. Each section logs a single artifact (cover letter, customer email, marketing draft, compliance narrative, etc.).

**Generic schema:**

```markdown
## YYYY-MM-DD — <artifact identifier> — <status>

**Claims made:**
- [claim text] — VERIFIED (matched facts.yaml > <path>)
- [claim text] — UNVERIFIED (no matching fact; warning issued)
- [claim text] — FORBIDDEN (matched do-not-claim.md > <rule>) ⚠️ MUST FIX
- [claim text] — OPINION (preference/value statement; not verifiable)

**Stories quoted:**
- <story-name from stories.md>

**Anti-claims acknowledged:**
- [verbatim acknowledgment of a gap]

**Notes / drift detected:**
- [any drift; lessons; items to update facts.yaml with]
```

---

## Adapter system

### `.leonard/config.toml` — extended schema

```toml
# v1.0 schema (backwards-compatible with v0.52 — code adapter is implicit if [post_edit.verify] is set)

[[adapters]]
type = "code"
languages = []  # auto-detect from project root
# Implicit fallback: if [post_edit.verify] is set, code adapter is enabled.

[[adapters]]
type = "ground-truth"
truth_dir = "ground-truth/"        # relative to project root
verify_targets = ["*.md", "*.txt", "*.yaml"]
# Behavior knobs:
forbidden_action = "reject"        # reject | warn | log-only
unverified_action = "warn"         # reject | warn | log-only
opinion_handling = "ignore"        # ignore | log-only

[adapters.ground-truth.claim_detection]
# v0.6 default: heuristic regex
mode = "heuristic"                 # heuristic | llm | hybrid
# v0.9 optional: hybrid mode falls back to local LLM for ambiguous sentences
llm_endpoint = ""                  # only used if mode = "llm" or "hybrid"

[post_edit.verify]
# Existing code-adapter verifier (unchanged from v0.52)
command = "go vet ./..."
timeout = "60s"
```

### Trust authorization extends naturally

`leonard config trust` continues to require operator authorization for any executable verifier. The ground-truth adapter introduces no new executables by default — claim verification runs in-process. If a project configures an external claim-extractor binary, that binary requires trust authorization the same way `go vet` does today.

### Hook plumbing per adapter

When `leonard-hook pre-edit` fires:

1. Read all enabled adapters from config
2. For each adapter, call its `pre-edit` handler with the proposed edit payload
3. Aggregate results: reject if ANY adapter rejects; warn if ANY adapter warns; pass otherwise
4. Return aggregated verdict to Claude Code

Same shape for `post-edit`, `session-start`, `stop`.

### MCP tool registration per adapter

The `leonard-mcp` binary registers tools from all enabled adapters. Existing code-adapter tools (`verify_symbol`, `find_symbol`, `get_decisions`) keep their names. Ground-truth adapter adds:

- `verify_claim(text)` → `VERIFIED | UNVERIFIED | FORBIDDEN | OPINION` with reasoning
- `list_facts(category)` → returns relevant fact-yaml entries
- `get_story(name)` → returns canonical text from stories.md
- `check_forbidden(text)` → returns matching do-not-claim entries
- `check_filter(category, target)` → returns filter verdict
- `log_audit(artifact_id, claims, stories_used, anti_claims_acknowledged)` → appends to audit-log.md

---

## Phasing: v0.6 → v1.0

| Version | Capability | Effort estimate |
|---|---|---|
| **v0.6** | Ground-truth adapter alpha: reads facts.yaml/stories.md, exposes verify_claim MCP tool, basic claim extraction via regex heuristic | 1-2 weeks |
| **v0.7** | Forbidden-claim guard: do-not-claim.md hard rejection in pre-edit hook (same shape as path-trust guard) | 3-5 days |
| **v0.8** | Filter enforcement at scaffolding: pre-edit hook reads filters.yaml; rejects Write into forbidden-target paths | 2-3 days |
| **v0.9** | Audit log + auto-sync plugin interface: post-edit logs claims to audit-log.md; pluggable "sync" adapters (GitHub API, generic webhook, local script) for keeping facts.yaml fresh | 1-2 weeks |
| **v1.0** | Full MCP tool surface, hybrid claim-detection (heuristic + LLM fallback), polish, docs, examples | 3-5 days |

Total: 4-6 weeks of focused development across the five phases.

---

## Per-phase detailed specs

### v0.6 — Ground-truth adapter alpha

**Goal:** A working ground-truth adapter that reads facts.yaml, exposes verify_claim, and provides advisory (non-blocking) warnings via post-edit hook.

**Deliverables:**

1. New adapter type `ground-truth` registerable in `.leonard/config.toml`
2. Reads YAML/Markdown files from `truth_dir` (default `ground-truth/`)
3. Heuristic claim detector (regex-based — see implementation sketch below)
4. New MCP tool: `verify_claim(text) → VerificationResult`
5. New MCP tool: `list_facts(category)`
6. New MCP tool: `get_story(name)`
7. Post-edit hook logs unverified claims to a `pending-audit.log` file (advisory; not yet blocking)
8. `leonard init --adapter=ground-truth` CLI flag to scaffold the directory structure
9. Tests: golden-file tests for claim detection, fact lookup, story retrieval

**Heuristic claim detector — implementation sketch (v0.6):**

Claims are detected in proposed text via regex patterns:

```go
// Sample patterns (illustrative; will expand based on dogfooding)
var claimPatterns = []ClaimPattern{
    // "I built X" / "I shipped X" / "I created X"
    {Regex: `\bI (built|shipped|created|launched|wrote|designed|architected) (.+?)[.;,]`,
     Category: "personal_action",
     LookupHint: "projects.*.name OR projects.*.description"},

    // "{number} {unit}" — quantitative claims
    {Regex: `\b(\d+(?:,\d{3})*(?:\.\d+)?)\s*(messages|users|customers|requests|MB|GB|TB|%|years|months|days)\b`,
     Category: "quantitative",
     LookupHint: "metrics.*"},

    // "{tech} production" / "production {tech}"
    {Regex: `\b(production|shipped|live) (Go|Python|TypeScript|Java|Ruby|Rust|...)\b`,
     Category: "tech_in_production",
     LookupHint: "tech_stack.*"},

    // Dates
    {Regex: `\b(\d{4}-\d{2}-\d{2}|\d{4})\b`,
     Category: "date",
     LookupHint: "*"},

    // Forbidden-pattern matches (from do-not-claim.md after parsing)
    // Generated dynamically at startup.
}
```

For each detected claim:

1. Look up against `facts.yaml` using the `LookupHint` path
2. Compare against `do-not-claim.md` entries (parsed once at startup)
3. Return verdict: `VERIFIED | UNVERIFIED | FORBIDDEN | OPINION`

**Validation criteria (v0.6 done = all of these):**

- [ ] `leonard init --adapter=ground-truth` creates the directory structure
- [ ] `verify_claim("I shipped Go in production")` returns `VERIFIED` if facts.yaml has Go in `tech_stack.primary_language`, else `UNVERIFIED`
- [ ] `list_facts("projects")` returns the list of projects from facts.yaml
- [ ] `get_story("2024 product launch")` returns the canonical text
- [ ] Post-edit hook detects 5+ claims in a 500-word draft and writes them to `pending-audit.log`
- [ ] Code adapter (existing v0.52 behavior) is unaffected; existing projects keep working

### v0.7 — Forbidden-claim hard guard

**Goal:** Pre-edit hook hard-rejects edits that introduce forbidden claims.

**Deliverables:**

1. Pre-edit hook integrates ground-truth adapter's check
2. When `forbidden_action = "reject"` (default) and a forbidden claim is detected, the hook returns a reject verdict with the offending text + rule citation
3. Reject message includes the do-not-claim.md line that triggered the match, so the operator knows why
4. New `leonard config trust ground-truth` command authorizes the adapter to reject edits (operator opt-in, like existing verifier trust)
5. Backwards-compatible: existing projects without `ground-truth/` directory see no change

**Implementation:**

- Parse do-not-claim.md at session start; build pattern set of forbidden claims
- Match proposed edit against patterns
- On match: reject with hookResponse `{"continue": false, "reason": "Forbidden claim: '<text>' matches rule '<rule>'"}`
- Pattern matching can use fuzzy matching (Levenshtein distance ≤ 3) to catch near-matches; document the threshold so operators can tune

**Validation criteria (v0.7 done):**

- [ ] Pre-edit hook rejects a Write/Edit that introduces text matching do-not-claim.md
- [ ] Reject message includes the offending claim + the rule that matched
- [ ] `leonard config trust ground-truth` is required before reject behavior is active
- [ ] False-positive rate measured on a 1000-claim corpus; threshold tunable
- [ ] Tests: positive (real forbidden claim rejected), negative (similar-but-OK claim passes), edge (operator overrides via inline annotation)

### v0.8 — Filter enforcement at scaffolding

**Goal:** When a Write tool tries to create a file in a path that matches a forbidden filter, reject before the file exists.

**Deliverables:**

1. Pre-edit hook reads `filters.yaml`
2. For Write operations creating new files, check the target path / file content against filter rules
3. Common pattern: `applications/{company}/` directories trigger a check against forbidden-companies list
4. Generic pattern: `filters.yaml` maps path patterns or content patterns to forbidden / required-disclosure rules
5. Operator gets a clear reject message with filter citation

**Implementation:**

```yaml
# filters.yaml — example
path_filters:
  - path_pattern: "applications/(.+)/"
    capture_as: "company"
    check: "company NOT IN forbidden_customers"
    forbidden_customers:
      - acme-corp
      - beta-co

content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "Disclose existing competitive arrangement (see disclosures.md)"
```

**Validation criteria (v0.8 done):**

- [ ] `Write("applications/acme-corp/cover-letter.md", ...)` is rejected with filter citation
- [ ] Operator can override per-invocation with `bash leonard override --once --reason "..."` 
- [ ] Filter rules are hot-reloaded when filters.yaml changes
- [ ] Tests: filter triggers, override path, hot-reload

### v0.9 — Audit log + auto-sync plugin interface

**Goal:** Audit logging works automatically; pluggable sync adapters keep facts.yaml fresh.

**Deliverables:**

1. Post-edit hook auto-appends claim audit to `audit-log.md`
2. Stop hook surfaces an end-of-session summary (claims made, anti-claims acknowledged, drift detected)
3. New `leonard sync` CLI subcommand runs configured sync plugins
4. Sync plugin interface: any executable that takes a JSON input (current facts.yaml subset) and outputs an updated JSON subset (with `last_verified` bumped and any changes)
5. Built-in sync plugin: `leonard sync github` — verifies OSS PR statuses in facts.yaml against GitHub API and updates `status` / `last_verified` fields
6. Documentation for writing custom sync plugins

**Sync plugin protocol:**

```bash
# Sync plugin is an executable in PATH or referenced in config:
[adapters.ground-truth.sync.github]
command = "/usr/local/bin/leonard-sync-github"
schedule = "daily"
config_file = ".leonard/sync-github.toml"

# Plugin receives on stdin:
# { "facts": { "oss_contributions": [ ... ] } }
# Plugin returns on stdout:
# { "updated_facts": { "oss_contributions": [ ... with status fields updated ... ] },
#   "changes": [ { "path": "...", "old": ..., "new": ..., "reason": "..." } ] }
```

**Validation criteria (v0.9 done):**

- [ ] Post-edit hook appends an audit entry for every Write/Edit
- [ ] Stop hook prints session summary
- [ ] `leonard sync github` updates OSS PR statuses in facts.yaml
- [ ] Custom sync plugin works (example: a shell script that updates a single fact)
- [ ] Tests: sync plugin protocol, audit log append, summary generation

### v1.0 — Polish, hybrid detection, documentation

**Goal:** Production-ready release with comprehensive docs and example projects.

**Deliverables:**

1. Hybrid claim detection (regex heuristic + optional LLM fallback for ambiguous sentences)
2. `leonard verify <file>` CLI subcommand — run all enabled adapters' checks against a file without writing
3. Full MCP tool surface (all six tools documented and tested)
4. README updates: ground-truth adapter section, example workflows
5. New `docs/` directory with:
   - `docs/adapters/code.md` — code adapter reference
   - `docs/adapters/ground-truth.md` — ground-truth adapter reference
   - `docs/schema/facts.md` — facts.yaml schema reference
   - `docs/schema/stories.md` — stories.md schema reference
   - `docs/schema/do-not-claim.md` — do-not-claim.md schema reference
   - `docs/schema/filters.md` — filters.yaml schema reference
   - `docs/cookbook/` — generic example projects (see below)
6. `examples/ground-truth/` directory with template + 2-3 generic worked examples
7. Bug-hunt round 7 + focused security review #3 (per Leonard release discipline)
8. CHANGELOG.md entry

**Validation criteria (v1.0 done):**

- [ ] All MCP tools work via Claude Code integration test
- [ ] CLI subcommand `leonard verify <file>` works
- [ ] Documentation covers: install, init both adapter types, write facts.yaml, dogfooding workflow
- [ ] Examples directory has 2-3 worked projects
- [ ] Bug-hunt round 7 + security review #3 complete; all HIGH/CRITICAL closed
- [ ] CHANGELOG entry written
- [ ] Released to public

---

## Backwards compatibility

**Non-negotiable:** No existing Leonard project breaks.

- `.leonard/` directories without a `ground-truth/` subdirectory continue to use only the code adapter (current behavior)
- `[post_edit.verify]` config remains the canonical way to configure the code adapter's verifier; new `[[adapters]]` syntax is additive
- Existing MCP tools (`verify_symbol`, `find_symbol`, `get_decisions`) keep their names and signatures
- Existing hooks keep working identically when no ground-truth adapter is configured
- `leonard config trust` continues to work for code-adapter verifiers; ground-truth adapter introduces a separate trust scope

**Migration path for existing users:**

```bash
cd my-existing-leonard-project/
leonard init --adapter=ground-truth  # adds ground-truth/ subdir alongside symbols.db
# Edit ground-truth/facts.yaml, stories.md, do-not-claim.md, filters.yaml
leonard config trust ground-truth    # authorize the new adapter
# Done — both adapters now active.
```

No data migration required; the two adapters coexist with no cross-interference.

---

## Reference implementation: generic example projects

These are illustrative — they show how the schema applies to non-code domains. Concrete worked examples ship in `examples/ground-truth/` as part of v1.0.

### Example 1: SaaS product context

A solo founder building a SaaS product wants Claude to write blog posts, marketing copy, and customer emails without making feature claims that aren't true.

```
my-saas/
├── src/                          # actual product code
├── content/                      # blog posts, marketing copy
└── .leonard/
    ├── config.toml               # both adapters active
    ├── symbols.db                # code adapter indexed src/
    └── ground-truth/
        ├── facts.yaml            # product features, pricing tiers
        ├── stories.md            # the "launch story," "founding story"
        ├── do-not-claim.md       # features-we-don't-have, certifications-we-lack
        ├── filters.yaml          # forbidden marketing channels, required disclosures
        └── audit-log.md          # per blog post / email
```

When Claude drafts a blog post in `content/`, the ground-truth adapter verifies feature claims against `facts.yaml`, rejects any claim that matches `do-not-claim.md` (e.g., "we're HIPAA compliant" when we're not), and logs the audit.

When Claude edits source code in `src/`, the code adapter verifies symbols as today.

### Example 2: Compliance documentation

A compliance team uses Claude to draft SOC 2 control narratives and audit responses.

```
compliance-docs/
└── .leonard/
    ├── config.toml               # ground-truth adapter only
    └── ground-truth/
        ├── facts.yaml            # implemented controls, technologies, data flows
        ├── stories.md            # canonical phrasings for each control area
        ├── do-not-claim.md       # controls-we-don't-have, regulations-we-don't-cover
        ├── filters.yaml          # forbidden auditors, required-CA-counsel-review rules
        └── audit-log.md          # per control narrative draft
```

Pre-edit hook rejects any draft that claims a control the company doesn't actually have. Audit log creates a defensible paper trail.

### Example 3: Customer-facing sales operation

A sales team uses Claude to draft outreach and follow-up emails. They need to never misrepresent product capabilities, never message customers under legal hold, and always cite verified customer history.

```
sales/
└── .leonard/
    ├── config.toml
    └── ground-truth/
        ├── facts.yaml            # customer data: contracts, contacts, history
        ├── stories.md            # canonical product pitches per segment
        ├── do-not-claim.md       # capability claims that aren't ready
        ├── filters.yaml          # forbidden customers (legal hold, terminated)
        └── audit-log.md          # per email draft
```

Pre-edit hook rejects a draft to a forbidden customer; logs every claim made to each customer; checks that any quoted product capability matches `facts.yaml`.

### Example 4: Personal claim-making (résumés, applications, public profiles)

An individual using Claude to draft cover letters, application form answers, LinkedIn posts, etc. Wants to never overclaim or make up experiences.

```
personal-artifacts/
└── .leonard/
    ├── config.toml
    └── ground-truth/
        ├── facts.yaml            # employment, projects, OSS contributions, skills
        ├── stories.md            # canonical narratives for major experiences
        ├── do-not-claim.md       # skills-I-don't-have, certifications-I-lack, comp-rules
        ├── filters.yaml          # company-fit filters, role-shape filters
        └── audit-log.md          # per cover letter / application
```

This is the original motivating use case for this roadmap, but the schema makes it generic.

---

## MCP tool design

Full surface exposed to Claude Code by `leonard-mcp` when ground-truth adapter is active.

### `verify_claim(text: string) → VerificationResult`

Detect claims in the text and return verification verdict per claim.

```json
{
  "claims": [
    {
      "text": "I shipped Go in production",
      "verdict": "VERIFIED",
      "evidence_path": "facts.yaml#tech_stack.primary_language",
      "evidence_value": "Go"
    },
    {
      "text": "I have a Master's degree in CS",
      "verdict": "FORBIDDEN",
      "rule_path": "do-not-claim.md#education",
      "rule_text": "No formal degree"
    },
    {
      "text": "I value safety-first engineering",
      "verdict": "OPINION",
      "note": "Preference statement; not verifiable"
    }
  ],
  "summary": {
    "verified": 1,
    "unverified": 0,
    "forbidden": 1,
    "opinion": 1
  }
}
```

### `list_facts(category: string | nil) → FactsResult`

Return facts from facts.yaml. Optional category filter.

### `get_story(name: string) → StoryResult`

Return canonical text from stories.md. Includes short version, long version, sensitivity flag.

### `check_forbidden(text: string) → ForbiddenCheckResult`

Cheaper than verify_claim — only checks for forbidden patterns. Useful as a fast pre-check.

### `check_filter(category: string, target: string) → FilterCheckResult`

Check if a target (company name, customer id, channel, etc.) is in a filter category's forbidden list.

### `log_audit(payload: AuditPayload) → void`

Append an entry to audit-log.md. Used by Claude after completing an artifact, to log claims made and verification statuses.

---

## CLI surface

New `leonard` subcommands and flags shipping across v0.6–v1.0:

```bash
# v0.6
leonard init --adapter=ground-truth         # scaffold .leonard/ground-truth/
leonard init --adapter=code,ground-truth    # both adapters

# v0.7
leonard config trust ground-truth           # authorize ground-truth adapter to reject edits

# v0.8
leonard override --once --reason "<text>"   # one-time bypass of filter rules (logged)

# v0.9
leonard sync                                # run all configured sync plugins
leonard sync <plugin-name>                  # run a specific sync plugin
leonard sync list                           # list configured sync plugins
leonard sync github --dry-run               # preview GitHub-API-based updates

# v1.0
leonard verify <file>                       # run all active adapters against a file (no write)
leonard verify <file> --adapter=ground-truth  # restrict to one adapter
leonard ground-truth lint                   # validate facts.yaml / stories.md / do-not-claim.md syntax
leonard ground-truth stats                  # show fact count, story count, last-verified ages
```

---

## Open design questions

These are deliberately unresolved in this roadmap; decide during implementation based on dogfooding:

1. **Claim detection sensitivity (v0.6).** Heuristic regex will have false positives. What's the right balance between "catch every claim" (high recall, more false positives) and "only catch obvious claims" (lower recall, fewer false positives)? Default thresholds to be set per dogfooding with real prose artifacts.

2. **Forbidden-claim fuzzy matching (v0.7).** Levenshtein distance ≤ 3 catches near-paraphrases. But it can also flag legitimate similar-but-different claims. Make this tunable per-rule in do-not-claim.md?

3. **Audit log granularity (v0.9).** Log every claim per edit? Every claim per artifact (defining "artifact" how)? Daily summary? Per-session summary? Probably configurable, but default needs to balance signal vs noise.

4. **Sync plugin sandboxing (v0.9).** Sync plugins execute arbitrary code. They should run under Leonard's existing trust system, but should they also run in a more restricted sandbox (e.g., no network access for non-network plugins)? Resolve when first non-network sync plugin is written.

5. **LLM fallback for hybrid claim detection (v1.0).** When heuristic regex doesn't match a clear pattern but the sentence is making a claim, fall back to a local LLM (Ollama-style). What model? How expensive per claim? Cache results? Operator-controlled?

6. **Multi-project facts.yaml sharing (post-v1.0).** Users with multiple Leonard projects may want a shared base facts.yaml (e.g., "my personal facts") plus project-specific overlays. Out of v1.0 scope but worth considering for v1.1.

7. **Versioning of facts.yaml (post-v1.0).** When facts.yaml changes, prior audit-log entries reference an old version of truth. Should the audit log preserve the facts.yaml-at-time-of-claim, or just reference the current state? Probably the former for true forensic value.

---

## Success criteria

The roadmap is "done" when:

1. **Both adapters coexist cleanly.** A project with both code-symbol verification and ground-truth verification works without cross-interference.
2. **Generic schema validates against multiple domains.** The five-file schema works for at least three distinct domains (e.g., product context, compliance, customer-facing) without modification.
3. **Existing users see zero regression.** v0.52 projects upgraded to v1.0 with no config changes continue to work identically.
4. **Documentation is sufficient for new contributor pickup.** A new contributor with no Leonard context can read `docs/` and contribute to either adapter.
5. **Bug-hunt round 7 + security review #3 close all HIGH/CRITICAL findings.** Leonard's release discipline holds through this expansion.
6. **The ground-truth adapter is dogfooded for at least 30 days on a real personal project.** No theoretical-only release.

---

## Implementation notes for new contributor

If you (Claude or human) are picking up this work for the first time:

### Read first

1. This roadmap (you're here)
2. `~/Documents/Homelab/leonard/README.md`
3. `~/Documents/Homelab/leonard/DESIGN.md`
4. `~/Documents/Homelab/leonard/CHANGELOG.md` — see v0.51, v0.52 release notes for recent direction
5. `~/Documents/Homelab/leonard/SECURITY.md` — understand the trust model before touching adapter authorization

### Understand the existing architecture before modifying

- `cmd/leonard/` — CLI entry point
- `cmd/leonard-mcp/` — MCP server entry point
- `cmd/leonard-hook/` — hook bridge entry point
- `internal/` — core logic (verify the internal package structure in code)
- `audits/` — past security review artifacts (worth reading to understand prior decisions)
- `evals/` — test fixtures and evaluation harnesses

### Recommended starting point

Implement v0.6 in this order:

1. Define the adapter interface (Go interface in `internal/adapters/`)
2. Refactor existing code-symbol logic to implement the adapter interface (no behavior change)
3. Add `ground-truth` adapter implementation
4. Wire ground-truth adapter through hooks
5. Add MCP tools
6. Write tests
7. Update README + CHANGELOG

Refactoring the existing logic to the new adapter interface FIRST (step 2, no behavior change) ensures backwards compatibility is provable before ground-truth adapter ships. This is the lowest-risk path.

### Dogfooding

Leonard is self-dogfooded. While developing the ground-truth adapter, write a minimal `ground-truth/` for the Leonard project itself (facts about Leonard's features, do-not-claim entries for "don't claim Leonard does X that it doesn't"). This catches integration bugs early and validates the schema against a real domain.

### Release discipline

Per Leonard's existing convention:

- Every minor release (v0.6, v0.7, etc.) gets a bug-hunt round before merging the version bump
- v1.0 specifically gets a focused security review (#3) in addition to the bug-hunt
- CHANGELOG.md entries written before tagging
- No HIGH/CRITICAL findings open at release

---

## End of roadmap

Questions, gaps, or proposed amendments: open an issue against this file or add a "## Amendment YYYY-MM-DD" section at the bottom.

---

## Amendment 2026-05-21 — Self-logging truth changes

### The principle

The roadmap above defines how artifacts get verified against ground-truth. This amendment adds the symmetric piece: **when ground-truth itself changes, the change pairs with a recorded rationale.** Over time the rationale log tells the story of the build — not just *what* the project's truth was at any point, but *why* it became that way.

Leonard already has the "why" channel: the decision log (`record_decision`, `supersede_decision`, `get_decisions`, `get_stale_decisions`). What's missing is the discipline that ties decision-log entries to truth-changing edits. This amendment closes that gap.

### What counts as a "truth change"

Two scopes, treated with the same mechanism but tunable separately:

1. **Domain truth** — edits to files inside `.leonard/ground-truth/` (facts.yaml, stories.md, do-not-claim.md, filters.yaml). The truth the project verifies artifacts against.
2. **Toolkit truth** — edits to Leonard's own source, config, or scope that change what Leonard considers verifiable: adapter implementations, claim-detection patterns, trust-system code, hook handlers, schema definitions, this roadmap. The truth Leonard itself codifies.

Both deserve a recorded "why." Domain-truth changes affect what the project will accept in artifacts going forward. Toolkit-truth changes affect what Leonard means by "verified" going forward. Both are load-bearing across future sessions.

Audit-log (existing v0.9 deliverable) covers a third scope — *artifact* claims (what was said and whether it was verified). Decision log covers *truth* changes (why truth moved). They're complementary; neither replaces the other.

### Mechanism: post-edit hook pairs edits with rationale

The post-edit hook gains a new responsibility for matching paths:

1. Detect that the edit touched a truth-scope file (matched against `[truth_change_log]` config patterns).
2. Auto-draft a rationale from the current conversation context: the user prompt that motivated the change, the diff summary, and any decision-log entries this supersedes.
3. Present the draft to the operator for confirmation (via Claude Code's existing approval surface, same shape as `leonard config trust`).
4. On confirm: append a decision-log entry linking to the diff (git SHA or pre/post hash if not in git) and the truth files affected.
5. On reject of the draft: the operator either edits the rationale or marks the change as `trivial:` with a short reason (typo, whitespace, formatting). Trivial entries still get logged but don't require full rationale.

This is **not** an LLM-classifies-importance step. The hook always logs *something*; the operator decides whether it's `rationale:` or `trivial:`.

### Tiered enforcement policy

Different truth files have different stakes. Hard-requiring rationale for every facts.yaml typo is friction; soft-warning on do-not-claim.md edits is malpractice. The policy is configured per-file in `config.toml`:

```toml
[truth_change_log]
# Where rationale entries are stored (defaults to existing decision log).
# Set to a dedicated path if you want truth-changes separated from
# general project decisions.
log_path = ".leonard/decisions/"   # default: existing decision log
auto_draft = true                  # post-edit hook drafts rationale from context

# Per-file enforcement tiers
[truth_change_log.policy]
"ground-truth/do-not-claim.md" = "require"   # block edit until rationale recorded
"ground-truth/filters.yaml"    = "require"   # high-stakes; strategic rules
"ground-truth/facts.yaml"      = "warn"      # frequent churn; soft prompt
"ground-truth/stories.md"      = "warn"      # phrasing edits are common
"ground-truth/audit-log.md"    = "skip"      # append-only artifact log, not truth

# Toolkit-truth scope (Leonard's own source)
"internal/adapters/**"         = "require"   # adapter contracts are load-bearing
"internal/trust/**"            = "require"   # trust system changes need rationale
"cmd/**"                       = "warn"      # CLI surface changes
"docs/ROADMAP*.md"             = "warn"      # roadmap changes (this file)
```

Tier semantics:

- `require` — post-edit hook rejects the edit if no rationale is recorded. Same shape as existing forbidden-claim reject (v0.7). Operator can pass `--trivial "<short reason>"` to bypass with a logged trivial entry.
- `warn` — post-edit hook logs the rationale (auto-drafted, operator can edit), but does not block.
- `skip` — no rationale logging for this path. Used for files that are themselves logs / generated.

### Decision-log entry schema (extended)

The existing decision-log entry format gains optional fields for truth-change provenance:

```yaml
# Existing fields (unchanged)
id: 2026-05-21-001
title: "Tighten HIPAA do-not-claim rule"
status: active   # active | superseded
recorded_at: 2026-05-21T14:23:00-04:00

# New fields for truth-change entries
truth_change:
  scope: domain                              # domain | toolkit
  files:                                     # truth files this entry covers
    - ground-truth/do-not-claim.md
  diff_ref: "git:a1b2c3d"                    # git SHA, or "hash:<pre>-><post>" if not in git
  motivated_by: |
    User flagged on 2026-05-21 that the existing "no HIPAA"
    line was ambiguous — could be read as "we're working toward it."
    Tightening to make absence explicit.
  supersedes: 2026-04-12-003                 # optional; if this replaces a prior decision

# Existing rationale field (unchanged; this is the "why")
rationale: |
  Customer-facing artifacts have started hedging on HIPAA. The
  prior wording invited that. New wording leaves no ambiguity.
```

Existing decision-log entries without a `truth_change` block continue to work unchanged. The block is purely additive metadata.

### Auto-draft mechanism

The hook drafts the `motivated_by` and `rationale` fields from:

1. The most recent user message in the current session (the prompt that prompted the edit).
2. The diff itself (what changed, summarized).
3. Any decision-log entries this edit appears to supersede (matched by overlapping file paths + content similarity).

Draft is presented to the operator for inline edit before commit. Operator can also reject the draft entirely and write the rationale by hand. Auto-draft is opt-out via `auto_draft = false` for operators who prefer to always write rationale themselves.

This keeps the friction-vs-completeness tradeoff acceptable: the operator confirms a pre-written rationale (cheap) rather than composing from scratch every time (expensive enough to skip).

### New MCP tools

Two additions to the surface introduced in v0.6:

- `record_truth_change(scope, files, diff_ref, motivated_by, rationale, supersedes?)` → appends a decision-log entry with the `truth_change` block. Called by the post-edit hook after operator confirmation.
- `get_truth_history(file_path)` → returns all decision-log entries whose `truth_change.files` includes `file_path`, oldest first. Lets future sessions ask "why is this rule the way it is?"

Existing `get_decisions()` and `supersede_decision()` continue to work; the new tools are convenience wrappers over the same underlying log.

### CLI surface

```bash
# Inspect why a specific truth file is what it is
leonard truth-history ground-truth/do-not-claim.md

# Inspect why a specific rule exists (line-level)
leonard truth-history ground-truth/do-not-claim.md:42

# Bypass require-tier with a logged trivial entry
leonard truth-edit --trivial "fix typo" ground-truth/facts.yaml

# Render the build story (chronological narrative of truth changes)
leonard truth-story                          # all scopes
leonard truth-story --scope=domain           # domain truth only
leonard truth-story --since=2026-01-01       # date-bounded
leonard truth-story --format=markdown        # default; also json, plain
```

`leonard truth-story` is the payoff feature. It renders the decision log as a chronological narrative — for each truth change: when, what files moved, what motivated it, and how it relates to prior decisions. This is the "log tells the story of the build" deliverable.

### Phasing

Slot this work into the existing v0.6–v1.0 phasing without extending the timeline:

| Version | Self-logging deliverable |
|---|---|
| **v0.6** | `truth_change` block in decision-log schema. Auto-draft logic in post-edit hook (advisory only — no enforcement yet). |
| **v0.7** | Enforcement tiers (`require` / `warn` / `skip`) wired through post-edit hook. `--trivial` bypass. |
| **v0.8** | `get_truth_history` MCP tool. `leonard truth-history` CLI. |
| **v0.9** | `leonard truth-story` CLI (narrative rendering). |
| **v1.0** | Docs: `docs/self-logging.md` covering policy tuning, draft-confirmation workflow, and reading `truth-story` output. |

No new phase added. Each version's self-logging slice fits inside the existing scope.

### Validation criteria additions (per phase)

Add to the existing per-phase done-criteria:

- **v0.6:** Editing `ground-truth/facts.yaml` triggers auto-draft of a rationale; operator can confirm or edit; entry lands in decision log with `truth_change.scope: domain`.
- **v0.7:** Editing `ground-truth/do-not-claim.md` without a recorded rationale is rejected by the post-edit hook. Passing `--trivial "<reason>"` allows the edit and logs a trivial entry.
- **v0.8:** `leonard truth-history ground-truth/do-not-claim.md` returns the chronological list of entries that changed that file, oldest first.
- **v0.9:** `leonard truth-story --since=2026-01-01` renders a coherent markdown narrative — anyone reading it can reconstruct the rough arc of truth changes without reading individual diffs.
- **v1.0:** A new contributor can run `leonard truth-story` against the Leonard repo itself and understand the toolkit's own evolution from v0.52 to v1.0.

### Self-application

Leonard's own development is the first test. From v0.6 onward, every edit to Leonard's own source under `require` paths logs a rationale. By v1.0, `leonard truth-story` against the Leonard repo should read as a coherent history of how the toolkit grew up — the dogfooding deliverable for this amendment.

### Open questions (added to the v0 list)

8. **Diff-ref format for non-git projects.** Most Leonard users are in git repos, but the schema supports `hash:<pre>-><post>` for non-git cases. Is the pre/post content hash enough, or does Leonard need to snapshot the actual pre/post content somewhere? Probably hash-only for v0.6, snapshots if a real user case arrives.

9. **Cross-scope supersession.** If a domain-truth entry (e.g., a do-not-claim rule) was originally motivated by a toolkit-truth decision (e.g., "we added fuzzy matching, so this rule needs tightening"), should the supersession chain cross scopes? Probably yes — the chain is by entry id, not by scope.

10. **Rationale staleness.** Existing `get_stale_decisions` already exists for the general decision log. Should truth-change entries get a separate staleness signal (e.g., a rule that was last touched 18 months ago and may no longer reflect the project's current view)? Probably reuses the existing staleness mechanism rather than forking it.

### Rollback / disablement

If an operator finds the self-logging discipline more friction than value, every part of it is opt-out:

- `auto_draft = false` disables draft generation (operator writes rationale manually).
- Set every policy tier to `warn` to disable rejection.
- Set every policy tier to `skip` to disable logging entirely.
- Removing `[truth_change_log]` from config.toml falls back to v0.52 behavior (decision log exists but isn't tied to truth-file edits).

No data is ever destroyed by disabling — existing entries remain in the decision log.
