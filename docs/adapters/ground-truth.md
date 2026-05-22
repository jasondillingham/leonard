# Ground-truth adapter

The ground-truth adapter is the v0.6+ addition. Where the code
adapter (`docs/adapters/code.md`) verifies code symbols, the
ground-truth adapter verifies **prose claims** against a
project-local truth tree.

Use it when you have a body of text (docs, marketing copy,
customer emails, compliance narratives, applications) and you
need to:

- Prevent claims about features the product doesn't have
- Prevent claims about customers under legal hold
- Prevent claims about certifications the org hasn't achieved
- Force disclosures when certain content patterns appear
- Block edits in forbidden directory shapes
  (e.g., `applications/<forbidden-company>/`)

---

## Setup

```
leonard init --adapter=ground-truth
```

Scaffolds `.leonard/ground-truth/` with five empty-but-valid
template files:

| File | Purpose |
|---|---|
| `facts.yaml` | What IS true (positive-space truth source) |
| `stories.md` | Canonical phrasings of common claims |
| `do-not-claim.md` | Forbidden claims (hard rejection list) |
| `filters.yaml` | Strategic rules — path + content filters |
| `audit-log.md` | Append-only ledger of claim verifications |

Edit the files with project-specific content. See the
`docs/schema/` pages for each file's shape.

---

## How it gates

Four hooks fire per Claude Code interaction:

### PreEdit (#23)
- Reads the proposed Content
- Runs `Detect` to find claims (regex + optional LLM fallback)
- For `Forbidden` verdicts: returns `Deny` with rule citation —
  IF `leonard config trust ground-truth` has been granted.
  Otherwise logs a warning and proceeds.
- Path filters (#15) and content filters (#16) run before claim
  detection so a blocked path or missing disclosure makes the
  rest moot.

### PostEdit (#18, #29)
- Reads the just-written file
- Re-runs `Detect`
- Appends UNVERIFIED + FORBIDDEN findings to
  `.leonard/pending-audit.log` (JSON) AND
  `<truth_dir>/audit-log.md` (operator-facing markdown)

### SessionStart
- v0.9: no-op (forward placeholder for "loaded N facts" surfacing)

### Stop (#30)
- Scans `pending-audit.log` for this session
- Emits a markdown digest via SystemMessage:
  - finding counts per verdict
  - per-file rollup
  - pointer to `leonard truth-story` / `leonard truth-history`

---

## MCP tools

| Tool | Description |
|---|---|
| `verify_claim(text)` | Run the claim detector against text; return per-claim verdicts. |
| `list_facts(category?, include_private?)` | Navigate `facts.yaml` by dotted path; private filter respects sensitivity. |
| `get_story(name)` | Return canonical text for a named story. |
| `get_truth_history(file_path)` | Return decision-log entries that touched `file_path`. |

Tools auto-register when the adapter is enabled.

---

## Claim verdicts

| Verdict | When |
|---|---|
| `verified` | Claim matched an entry in `facts.yaml` (with evidence path) |
| `unverified` | Pattern fired but no facts entry matched |
| `forbidden` | Claim matches a `do-not-claim.md` rule (with rule citation) |
| `opinion` | Preference/value statement; not verifiable |

Pattern set in [`internal/adapters/groundtruth/detector.go`](../../internal/adapters/groundtruth/detector.go).

---

## Hybrid mode (#36)

For sentences the regex heuristic misses, hybrid mode falls back
to a local LLM (Ollama-style):

```toml
[[adapters]]
type = "ground-truth"
truth_dir = "ground-truth/"

[adapters.ground-truth.claim_detection]
mode = "hybrid"
llm_endpoint = "http://localhost:11434"
llm_model = "qwen2.5:0.5b"
```

See [`docs/self-logging.md`](../self-logging.md) for the related
self-logging discipline.

---

## Implementation pointers

- Adapter: [`internal/adapters/groundtruth/`](../../internal/adapters/groundtruth/)
- Sync plugins: [`docs/sync-plugins.md`](../sync-plugins.md)
- Self-logging: [`docs/self-logging.md`](../self-logging.md)
- Schema docs: [`docs/schema/`](../schema/)
- Examples: [`examples/ground-truth/`](../../examples/ground-truth/)
