# Recipe: customer legal-hold blocking

Some customers are under legal hold — no further outreach, no
new artifacts naming them, no contract amendments until counsel
clears it. You want the toolkit to block edits that would touch
those customer files at all.

## The pattern

**1. Configure path-based blocking in `filters.yaml`.**

```yaml
path_filters:
  - path_pattern: "customers/([^/]+)/"
    forbidden_values:
      - acme-corp
      - beta-co
    reason: "Legal hold; outreach must route through counsel"

  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - terminated-co
    reason: "Contract terminated 2026-03-15; no further outreach"
```

**2. Trust the adapter.**

```
leonard config trust ground-truth
```

**3. Edits to `customers/acme-corp/...` will now Deny.**

```
$ # Claude Code trying to Write customers/acme-corp/2026-summary.md
leonard: ground-truth pre-edit: Path "customers/acme-corp/2026-summary.md"
is forbidden by path_filters[0]: Legal hold; outreach must route
through counsel. Use `leonard override --once --reason "..."` to
bypass for a single edit.
```

## Bypassing for one-off legal-cleared writes

When counsel clears a specific edit:

```
$ leonard override --once --reason "Acme cleared by legal 2026-05-22; this single email" customers/acme-corp/email-followup.md
leonard: override token granted for customers/acme-corp/email-followup.md
         reason: Acme cleared by legal 2026-05-22; this single email
         token expires in 5m0s; consumed on next matching edit.
```

The next matching PreEdit consumes the token and proceeds.
Second edit re-denies. The `reason` is logged in
`pending-decisions.log` for the audit trail.

## Removing the block when the hold ends

Edit `filters.yaml` to remove `acme-corp` from `forbidden_values`.
Hot reload picks up the change within ~2 seconds — no restart
needed.

The selflog adapter logs the `filters.yaml` edit as a
**require-tier** draft entry. The rationale you write in the
decision-log entry (when you promote the draft) is searchable via
`leonard truth-history filters.yaml`.

## Why per-customer blocking rather than global

Path filters are scoped to the directory shape, so the rest of
the project keeps working. You can:

- Continue editing `customers/safe-customer/*` freely
- Continue editing `customers/acme-corp/historical-record.md`
  ONLY if you bother to use `leonard override --once`
- Trust that any *new* artifact under `customers/acme-corp/` will
  fire the guard

## What this doesn't catch

References to the customer in OTHER files (`customers/safe-co/email.md`
mentioning Acme in a comparison) are NOT blocked by path_filters
alone. For mention-blocking, combine with `do-not-claim.md`:

```markdown
## Customer relationship rules

- ❌ "Acme Corp" — Legal hold; do not reference by name anywhere.
```

This will trigger a forbidden hit on the name itself wherever it
appears. Combine the two layers for full coverage.
