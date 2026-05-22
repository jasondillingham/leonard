# Writing custom sync plugins

A **sync plugin** is any executable that refreshes `facts.yaml`
against an authoritative source: GitHub PR statuses, CRM customer
records, monitoring dashboards, the master spreadsheet your team
already trusts. Plugins keep Leonard's claim verifier current
without operator effort.

This doc covers the protocol contract, how to configure a plugin,
trust authorization, error semantics, and two worked examples
(shell + Go).

---

## Protocol

A plugin is a process Leonard invokes with a JSON envelope on
stdin and expects a JSON envelope on stdout.

### Stdin

```json
{
  "facts":  { ... subset of facts.yaml the plugin operates on ... },
  "config": { ... per-plugin config from .leonard/config.toml ... }
}
```

- `facts` is the **entire current facts.yaml tree** as parsed
  from disk. The plugin reads what it needs (typically a single
  top-level key) and ignores the rest.
- `config` is the contents of the matching
  `[sync.<name>.config]` block in `.leonard/config.toml`. Empty
  map when no block is configured.

### Stdout

```json
{
  "updated_facts": { ... refreshed tree ... },
  "changes": [
    {
      "path":   "oss_contributions.0.status",
      "old":    "open",
      "new":    "merged",
      "reason": "PR merged 2026-04-20"
    }
  ]
}
```

- `updated_facts` becomes the new `facts.yaml` content. Plugins
  should preserve unrelated keys (return what they read, not just
  what they touched) so a partial sync doesn't truncate the tree.
- `changes` is a flat slice of per-field updates. `path` is the
  dotted key (`a.b.0.c`). `old` and `new` are the JSON values
  before and after. `reason` is the plugin's explanation,
  surfaced to operators verbatim.

### Exit codes

- **0** = success. Leonard parses stdout and applies
  `updated_facts` to `facts.yaml` (atomically via temp file +
  rename) unless `--dry-run` is set.
- **non-zero** = failure. Leonard logs the plugin's stderr and
  continues to the next plugin. `facts.yaml` is not touched for
  the failing plugin.

If stdout isn't valid JSON, Leonard treats it as a failure.

### Timeout

Leonard caps each plugin invocation at **5 minutes**. Long-running
network sync should chunk work and emit progress to stderr;
multi-hour batch jobs belong outside Leonard.

---

## Configuring a plugin

In `.leonard/config.toml`:

```toml
[sync.github]
command = "/usr/local/bin/leonard-sync-github"

[sync.github.config]
poll_interval = "daily"
```

The TOML key name (`github`) becomes the plugin's display name in
`leonard sync list` and the named-plugin argument to
`leonard sync github`.

`command` is the executable path. Absolute paths are safest;
relative paths resolve against the operator's `$PATH` at invocation
time.

`[sync.<name>.config]` is optional and passes through to the
plugin's stdin payload verbatim.

---

## Trust authorization

v0.9 ships the plugin runner **without** a persistent trust file.
The trust boundary is the explicit `leonard sync` CLI invocation —
operators run plugins by hand, no automatic invocation from hooks.

A follow-up will extend `leonard config trust` to fingerprint
plugin paths the same way `[post_edit.verify].command` is
fingerprinted today. Until then, operators should:

1. Inspect the plugin source before adding it to `config.toml`.
2. Use absolute paths so a `$PATH`-hijack can't swap the binary.
3. Set restrictive file permissions on the plugin (`0o755`,
   owned by the operator).

---

## Example: shell plugin

```bash
#!/bin/sh
# leonard-sync-myteam.sh
#
# Reads facts.team_members from stdin, queries our HR API, stamps
# last_verified on each entry. Emits the updated tree on stdout.

set -e
PAYLOAD=$(cat)

# Extract the slice we care about. (jq is convenient; any JSON
# tool works.)
FACTS=$(echo "$PAYLOAD" | jq '.facts')
TODAY=$(date -u +%Y-%m-%d)

# This stub just stamps last_verified on every team_members entry
# without touching the rest of the tree. A real plugin would call
# the HR API and update name / role / etc.
UPDATED=$(echo "$FACTS" | jq --arg today "$TODAY" '
  .team_members |= map(. + {last_verified: $today})
')

# Emit the envelope.
jq -n --argjson facts "$UPDATED" '{
  updated_facts: $facts,
  changes: []
}'
```

Configure:

```toml
[sync.myteam]
command = "/usr/local/bin/leonard-sync-myteam.sh"
```

Run:

```
$ leonard sync myteam
leonard sync: running myteam (/usr/local/bin/leonard-sync-myteam.sh)
  no changes (took 12ms)
```

---

## Example: Go plugin

```go
// cmd/leonard-sync-prometheus/main.go
//
// Reads facts.alerts_threshold from stdin, queries Prometheus for
// current alert counts, updates last_breach + count. Demonstrates
// the plugin protocol from Go.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type Input struct {
	Facts  map[string]any `json:"facts"`
	Config map[string]any `json:"config"`
}

type Output struct {
	UpdatedFacts map[string]any `json:"updated_facts"`
	Changes      []Change       `json:"changes"`
}

type Change struct {
	Path   string `json:"path"`
	Old    any    `json:"old,omitempty"`
	New    any    `json:"new,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func main() {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read stdin:", err)
		os.Exit(1)
	}
	var in Input
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(os.Stderr, "parse input:", err)
		os.Exit(1)
	}

	// Pull plugin config (e.g., the Prometheus URL).
	promURL, _ := in.Config["url"].(string)
	if promURL == "" {
		promURL = "http://localhost:9090"
	}

	// ... query promURL, build updates ...
	_ = promURL

	out := Output{
		UpdatedFacts: in.Facts, // pass-through for this stub
		Changes:      []Change{},
	}
	out.UpdatedFacts["last_synced_at"] = time.Now().UTC().Format(time.RFC3339)
	out.Changes = append(out.Changes, Change{
		Path: "last_synced_at",
		New:  out.UpdatedFacts["last_synced_at"],
	})

	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode output:", err)
		os.Exit(1)
	}
}
```

Build and configure:

```
$ go build -o /usr/local/bin/leonard-sync-prometheus ./cmd/leonard-sync-prometheus
```

```toml
[sync.prometheus]
command = "/usr/local/bin/leonard-sync-prometheus"

[sync.prometheus.config]
url = "https://prometheus.internal.example.com"
```

---

## Built-in: `leonard-sync-github`

Leonard ships one built-in plugin: [`cmd/leonard-sync-github`](../cmd/leonard-sync-github/).

It refreshes `facts.oss_contributions` entries against the GitHub
REST API. See the package docs at
[`internal/adapters/groundtruth/sync/github`](../internal/adapters/groundtruth/sync/github)
for the full schema and behavior.

---

## Testing your plugin

The simplest end-to-end test:

```bash
$ echo '{"facts": {"x": 1}, "config": {}}' | /path/to/your-plugin
{"updated_facts": {"x": 1}, "changes": []}
```

For Go plugins, mirror the
[`github_test.go`](../internal/adapters/groundtruth/sync/github/github_test.go)
pattern: use `httptest.NewServer` to mock the upstream API, then
call your sync function directly with the test server's URL.

---

## Operational tips

- **Idempotency:** plugins should produce the same output on
  repeat invocations against an unchanged source. Leonard's
  atomic write means a re-run won't corrupt `facts.yaml` if the
  upstream is stable.
- **Rate limits:** for network-bound plugins, surface 429 / 403
  rate-limit errors as clear stderr messages. Operators can rerun
  later; the plugin shouldn't block.
- **Stamp `last_verified`:** even when nothing changed, stamping
  `last_verified` on touched entries lets operators see the most
  recent reach-out time. Emit a corresponding `Change` so the
  audit trail is complete.
- **Plain JSON only:** no extra preamble on stdout. Stderr is
  where logs and progress belong.
