# Recipe: OSS contribution tracking

You have a portfolio of open-source contributions referenced in
artifacts (résumés, cover letters, blog posts). Each contribution
has a status (open / closed / merged) that changes over time, and
you want the status to stay current without manual checking.

## The pattern

**1. Record contributions in `facts.yaml`.**

```yaml
oss_contributions:
  - repo: anthropic/sdk-go
    number: 336
    status: closed
    last_verified: 2026-04-15
    note: "Closed after Stainless duplicate"

  - repo: 1Password/connect-sdk-go
    number: 104
    status: open
    last_verified: 2026-04-15

  - repo: tailscale/tailscale
    number: 19755
    status: open
    last_verified: 2026-04-15
```

**2. Configure the github sync plugin in `.leonard/config.toml`.**

```toml
[sync.github]
command = "/path/to/leonard-sync-github"
```

**3. Set `GH_TOKEN` so the plugin can reach the GitHub API.**

```
export GH_TOKEN="$(gh auth token)"
```

**4. Run sync to refresh statuses.**

```
$ leonard sync github
leonard sync: running github (/path/to/leonard-sync-github)
  3 change(s) in 412ms:
  - oss_contributions.0.last_verified: 2026-04-15 -> 2026-05-22  (stamped on github sync)
  - oss_contributions.1.last_verified: 2026-04-15 -> 2026-05-22  (stamped on github sync)
  - oss_contributions.2.status: open -> merged  (PR #19755 merged)
  facts.yaml updated
```

## What gets updated

For each entry with `repo` + `number`:

- `status` — `open` / `closed` / `merged` (the plugin distinguishes
  merged from closed-not-merged)
- `merged_at` — populated when the PR was merged
- `last_verified` — stamped to today's UTC date on every successful sync

Entries without `repo` or `number` pass through unchanged.

## Failure modes (non-fatal per entry)

- **HTTP 404** — entry passes through; a `Change` records the
  error in `Reason` so you see it in the operator output
- **Rate limited (no GH_TOKEN)** — clear error message; rerun
  with `GH_TOKEN` set
- **Non-list `oss_contributions`** — whole-tree error (fix the
  YAML and rerun)

## Scheduling

The sync isn't automatic — operators run it on demand. Common
patterns:

- **Manual before publishing** — run `leonard sync github` before
  re-publishing artifacts that reference contributions
- **Cron / launchd** — wire `leonard sync github` to a weekly
  scheduler; the `--dry-run` flag previews changes without
  writing
- **CI** — a scheduled GitHub Actions job that runs the sync and
  commits any `facts.yaml` updates back to the repo

## Why a separate sync step (not automatic at hook time)

The post-edit hook fires on every edit and is bounded by Claude
Code's timeout (~10s). A sync that hits the GitHub API for N
contributions could exceed that. Keeping sync explicit:

- Operators control when network I/O happens
- Rate limits don't cascade into hook failures
- Multi-second jobs don't block the model's next turn

## What this doesn't do

The sync only updates fields the plugin understands (`status`,
`merged_at`, `last_verified`). It doesn't:

- Add new entries (operators add new contributions by hand)
- Remove entries for deleted PRs (status remains as last seen)
- Update fields outside `oss_contributions`

For other source-of-truth sync needs (CRM, monitoring dashboards,
spreadsheets) write your own plugin — see
[`docs/sync-plugins.md`](../sync-plugins.md).
