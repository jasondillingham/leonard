# `filters.yaml` schema

`filters.yaml` is the **strategic rules** layer: where
`do-not-claim.md` is "don't say this," `filters.yaml` is "don't
do this work in the first place." Two filter types ship in v0.8:

- **`path_filters`** — block `Write` operations into forbidden paths
- **`content_filters`** — require disclosures when content patterns
  appear

---

## Shape

```yaml
path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
      - beta-co
    reason: "Contract terminated 2026-03-15; no further outreach"

  - path_pattern: "secrets/"
    reason: "Blanket ban on writing to secrets/"

content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "Disclose existing competitive arrangement (see disclosures.md)"
```

---

## `path_filters`

Each entry blocks a `Write` whose path matches `path_pattern` and
whose first capture group is in `forbidden_values`.

| Field | Type | Notes |
|---|---|---|
| `path_pattern` | regex string | Compiled at load time. Patterns with parse errors fail `leonard ground-truth lint`. |
| `forbidden_values` | list of strings | Compared case-insensitively against the first capture group. Empty list = "any match is forbidden" (blanket ban). |
| `reason` | string | Surfaced in the deny message. Optional but recommended. |

### Behavior

- File path is resolved project-relative before matching.
- Requires `leonard config trust ground-truth` to actually
  block. Without trust → warning to stderr, edit proceeds.
- Override on a one-off basis with
  `leonard override --once --reason "..." <path>`. The token is
  single-use and expires in 5 minutes.

### Example deny

```
$ # Attempting to Write applications/acme-corp/cover-letter.md
leonard: ground-truth pre-edit: Path "applications/acme-corp/cover-letter.md"
is forbidden by path_filters[0]: Contract terminated 2026-03-15;
no further outreach. Use `leonard override --once --reason "..."`
to bypass for a single edit.
```

---

## `content_filters`

Each entry requires that when `content_pattern` matches the
edit's content, the `required` disclosure text must also appear.

| Field | Type | Notes |
|---|---|---|
| `content_pattern` | regex string | Compiled at load time. Match runs against the edit's full content. |
| `required` | string | Disclosure text. Must appear verbatim (case-insensitive) in the content for the edit to pass. |

### Behavior

- Runs after `path_filters` and before the forbidden-claim guard.
- Trust + override semantics identical to `path_filters`.
- Disclosure check is case-insensitive substring on the content.

### Example deny

```
$ # Attempting to Write proposal.md containing "bid on contract"
leonard: ground-truth pre-edit: Content matches content_filters[0]
but the required disclosure is missing: "see disclosures.md". Add
the disclosure verbatim to the content, or use `leonard override
--once --reason "..."` to bypass for a single edit.
```

---

## Common patterns

### Per-customer outreach blocks

```yaml
path_filters:
  - path_pattern: "customers/([^/]+)/"
    forbidden_values:
      - legal-hold-co
      - terminated-co
    reason: "Legal hold or terminated relationship"
```

### Blanket directory bans

```yaml
path_filters:
  - path_pattern: "secrets/"
    # forbidden_values omitted → blanket ban
    reason: "Secrets directory is operator-only"
```

### Disclosure requirements

```yaml
content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "see disclosures.md"

  - content_pattern: "(?i)guaranteed (uptime|sla)"
    required: "past-90-days uptime cited from facts.yaml"
```

---

## Hot reload (#27)

Saving `filters.yaml` triggers an automatic reload within ~2
seconds. Parse errors keep the prior state intact; a malformed
edit doesn't break in-flight sessions. See
[`internal/adapters/groundtruth/reload.go`](../../internal/adapters/groundtruth/reload.go).

---

## Implementation pointers

- Parser: [`internal/adapters/groundtruth/filters.go`](../../internal/adapters/groundtruth/filters.go)
- Guard: [`internal/adapters/groundtruth/filter_guard.go`](../../internal/adapters/groundtruth/filter_guard.go)
- Override CLI: [`cmd/leonard/override.go`](../../cmd/leonard/override.go)
