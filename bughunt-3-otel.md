# Bug Hunt #3 — otel

## Summary

I audited `internal/telemetry/` (4 files, ~190 LOC) plus its three call sites in
`internal/hooks/{pre_edit,post_edit}.go` and `cmd/leonard-hook/main.go`. The
default-build no-op promise holds up: `Span()` costs ~1 ns/op with zero
allocations and the OTel SDK is verifiably absent from the default binary.
The `-tags otel` build wires up real spans whose parent/child structure is
correct (sibling-scan nests under pre-edit; index + vet nest under post-edit).

However, the v0.6.0 instrumentation has two real defects that defeat the
"give operators visibility on hot paths" goal in the most common scenarios:

1. **`os.Exit()` in `main.go` skips the deferred `shutdown(ctx)`** — every
   non-zero exit code from a hook (including the load-bearing exit-2 block
   path that turns ErrDecode into a blocking deny) **drops all queued spans**.
   Operators see telemetry for clean runs only — exactly the wrong direction.
2. **SIGINT-cancelled ctx propagates into the deferred Shutdown**, which then
   immediately gives up. A hook killed mid-flight also drops its spans.

Plus several smaller issues: leonard-mcp and the indexer aren't instrumented
despite the v0.6.0 version bump implying they were; `OTEL_SERVICE_NAME` is
silently ignored; the sibling-scan span discards the child ctx so any future
nested instrumentation can't link in; MultiEdit fires one span tree regardless
of N edits (no per-snippet visibility); and the OTLP unreachable-endpoint case
silently hangs the process for 30s while the batch processor times out.

Binary size delta with `-tags otel` (stripped): 7.9 MB → 18.8 MB, **+10.9 MB
(+137%)**. The default install story stays intact (OTel deps are completely
absent from the no-tag binary), but the cost of opting in is meaningful.

## Findings

### F1 — `os.Exit()` in main.go drops queued spans on every non-zero hook exit
- **Severity:** medium
- **Reproducer:**
  ```bash
  go build -tags otel -o /tmp/leonard-hook-otel ./cmd/leonard-hook
  # Success path: 2 spans on stderr (pre-edit + sibling-scan)
  cat /tmp/good-payload.json | OTEL_TRACES_EXPORTER=stdout /tmp/leonard-hook-otel pre-edit 2>/tmp/ok.log
  wc -l /tmp/ok.log   # => 2
  # Failure path: malformed JSON triggers exit 2
  echo '{not-valid' | OTEL_TRACES_EXPORTER=stdout /tmp/leonard-hook-otel pre-edit 2>/tmp/fail.log
  wc -l /tmp/fail.log # => 0 (only the human "leonard-hook: ..." line)
  ```
- **Observed:** A hook that returns an error reaches `os.Exit(exitCodeFor(err))`
  at `cmd/leonard-hook/main.go:37`. Per Go semantics, `os.Exit` skips deferred
  functions, so `defer shutdown(ctx)` on line 30 never runs. The
  `BatchSpanProcessor` has spans queued but unflushed; the OS reaps the
  process. The pre-edit + sibling-scan spans for the failed call are gone.
- **Expected:** Telemetry is *most* useful on failure paths — the exit-2
  blocking path is load-bearing for Leonard's safety story, and operators
  need to see how long the sibling-scan took before it failed.
- **Suggested fix shape:** Replace `os.Exit(code)` with a sentinel return from
  `main()` — e.g., wrap the body in `code := run(ctx)`, then `_ = shutdown(ctx)`
  *before* `os.Exit(code)`. The existing `TestOSExitConfinedToMain` guard
  ensures this is the only site that needs the change.

### F2 — SIGINT cancels the ctx that Shutdown needs to flush
- **Severity:** medium
- **Reproducer:**
  ```go
  // /tmp/bench-vend/shutdown_ctx_test.go (against the otel SDK with a test HTTP server)
  ctx, cancel := context.WithCancel(context.Background())
  shutdown, _ := telemetry.Init(ctx)
  _, end := telemetry.Span(ctx, "needs-flush"); end()
  cancel()                  // mimic SIGINT arriving before defer runs
  shutdown(ctx)             // returns "context canceled"
  // → server saw 0 POSTs (vs 1 with fresh ctx)
  ```
- **Observed:** `cmd/leonard-hook/main.go:19` derives ctx from
  `signal.NotifyContext`. When the user/system sends SIGINT or SIGTERM, that
  ctx is cancelled — *and that same cancelled ctx is what gets passed to
  `shutdown(ctx)` on line 30*. OTel's `tp.Shutdown` honors ctx cancellation
  and aborts its flush immediately, returning `context.Canceled`. All
  in-flight spans are dropped silently (the discarded `_` swallows the error).
- **Expected:** Even on signal-driven exit, the operator should see spans for
  the hook activity up to the cancellation point. SIGINT is rare on a hook
  process (they live milliseconds) but Claude Code's hook timeout enforcement
  *does* signal the child on overrun — exactly the case where telemetry is
  most valuable.
- **Suggested fix shape:** Pass a fresh `context.Background()` (or a short
  bounded timeout, e.g., `context.WithTimeout(context.Background(), 5*time.Second)`)
  to `shutdown()` instead of the cancelled signal-ctx. Goes hand-in-hand with
  F1's fix.

### F3 — Unreachable OTLP endpoint blocks the hook for 30 seconds
- **Severity:** medium
- **Reproducer:**
  ```bash
  # 10.255.255.1 is a TCP black-hole (no SYN-ACK). 4318 is the OTLP default.
  time (OTEL_EXPORTER_OTLP_ENDPOINT=http://10.255.255.1:4318 \
        /tmp/leonard-hook-otel pre-edit < /tmp/pre-edit-payload.json)
  # → 30.02s total, then "processor export timeout" on stderr
  # → hook response was on stdout almost immediately (~30ms), but the
  #   process kept running while BatchSpanProcessor.Shutdown waited for
  #   the export to finish or hit the SDK's 30s default timeout.
  ```
- **Observed:** The pre-edit response lands on stdout fast (Claude Code is
  unblocked) but the process doesn't exit for 30 seconds because the OTel
  SDK's batch span processor uses a 30-second default export timeout in
  Shutdown. A hook invocation that should be 20ms turns into 30 seconds
  whenever the collector is offline.
- **Expected:** If the collector is unreachable the hook should give up
  quickly — a few hundred ms is plenty. A misconfigured `OTEL_EXPORTER_OTLP_ENDPOINT`
  pointing at a dead collector should not 30x the hook latency.
- **Suggested fix shape:** Two layers — (a) configure
  `otlptracehttp.WithTimeout(2*time.Second)` (or similar) so individual
  export attempts don't hang; (b) wrap the Shutdown call in
  `context.WithTimeout(ctx, 2*time.Second)` so the overall flush is bounded
  regardless of the SDK default.

### F4 — leonard-mcp and the `leonard` CLI are not instrumented despite the version bump
- **Severity:** low (commit message did call out "indexer-side spans
  deferred," but the v0.6.0 bump to leonard-mcp's version + the README's
  claim that v0.6 adds OTel implies broader coverage)
- **Reproducer:**
  ```bash
  go build -o /tmp/lm-d ./cmd/leonard-mcp
  go build -tags otel -o /tmp/lm-o ./cmd/leonard-mcp
  ls -la /tmp/lm-d /tmp/lm-o   # both are 14,331,954 bytes — IDENTICAL
  grep -rn "telemetry" cmd/leonard-mcp/ cmd/leonard/ internal/mcp/ internal/index/
  # → no matches
  ```
- **Observed:** Neither `leonard-mcp` nor the `leonard` CLI imports the
  telemetry package. The `-tags otel` build of leonard-mcp is byte-identical
  to the default build because no telemetry surface is referenced. The MCP
  server's tool calls (verify_symbol, find_symbol, list_files, …) and the
  indexer's full-tree walks have no traces — the two longest-running
  operations in the system are invisible.
- **Expected:** Either both binaries call `telemetry.Init`, or the
  commit/README scope is clearer ("hook spans only, v0.7 adds the rest").
  The current shape — version-bump-but-no-instrumentation — is misleading.
- **Suggested fix shape:** Add `telemetry.Init`/`shutdown` to
  `cmd/leonard-mcp/main.go` and `cmd/leonard/main.go` even before any spans
  are added; the call site is two lines and ensures future spans elsewhere
  in the call tree actually flush. Tracking spans inside the MCP handlers
  and the indexer walk is the natural v0.7 follow-up.

### F5 — `OTEL_SERVICE_NAME` is silently overridden by the hardcoded "leonard"
- **Severity:** low
- **Reproducer:**
  ```bash
  OTEL_TRACES_EXPORTER=stdout OTEL_SERVICE_NAME=custom-name \
    /tmp/leonard-hook-otel pre-edit < /tmp/pre-edit-payload.json 2>&1 |
    grep -oE '"service.name"[^}]*' | head -1
  # → "service.name","Value":{"Type":"STRING","Value":"leonard"
  # OTEL_SERVICE_NAME was ignored. (OTEL_RESOURCE_ATTRIBUTES=env=prod IS honored.)
  ```
- **Observed:** `otel.go` builds the resource with
  `resource.New(ctx, resource.WithAttributes(semconv.ServiceName("leonard")))`.
  Because the manual `WithAttributes` is the last writer, it overrides the
  env-derived `service.name` from `OTEL_SERVICE_NAME`. Per the OTel spec,
  the env var should win.
- **Expected:** An operator running Leonard alongside other services in a
  shared collector should be able to label this instance's spans (e.g.,
  `OTEL_SERVICE_NAME=leonard-prod`) without rebuilding.
- **Suggested fix shape:** Only set `semconv.ServiceName("leonard")` as a
  *default* when `OTEL_SERVICE_NAME` is not in the env. Or use
  `resource.WithProcessFromEnv()` /
  `resource.WithFromEnv()` before the WithAttributes call so the env wins.

### F6 — `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` is silently unsupported
- **Severity:** informational
- **Reproducer:** N/A (read the imports: `otlptracehttp`, no `otlptracegrpc`).
- **Observed:** The HTTP exporter is unconditionally chosen. Setting
  `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` (which the OTel spec says should select
  the gRPC exporter) has no effect — every span goes over HTTP/JSON regardless.
- **Expected:** Either honor the env var (requires importing the grpc
  exporter — meaningful binary-size cost) or document the limitation in
  the package doc / README.
- **Suggested fix shape:** Add a one-liner in `otel.go`'s Init doc and the
  README's OTel section: "HTTP transport only; gRPC unsupported."

### F7 — Sibling-scan span discards the child ctx, preventing future nested instrumentation
- **Severity:** low
- **Reproducer:**
  ```go
  // internal/hooks/pre_edit.go:185
  _, endSiblings := telemetry.Span(ctx, "leonard.pre-edit.sibling-scan")
  siblings := readSiblingPackages(opts.ModuleRoot, opts.ModulePath)
  endSiblings()
  ```
- **Observed:** The new child ctx returned by `Span()` is discarded.
  `readSiblingPackages` doesn't take a ctx today, so nothing breaks. But if
  somebody later wants per-directory or per-package nested spans (e.g., to
  see which dir-walk subtree dominates the 480ms cost), they'll either have
  to refactor the call site or live with spans that orphan to the
  `pre-edit` parent instead of nesting under `sibling-scan`. The same
  pattern repeats in post-edit at lines 183 and 187 (`_, endIndex`,
  `_, endVet`).
- **Expected:** Either pass the new ctx into the callee (even if unused) so
  future nesting is one keyword away, or document explicitly that these
  spans are leaf-only timers.
- **Suggested fix shape:** Change the three call sites to bind the child
  ctx: `siblingCtx, endSiblings := telemetry.Span(ctx, ...)`. Today
  `readSiblingPackages` is plain — no ctx surface — so add `_ = siblingCtx`
  to silence the linter, then plumb the ctx through whenever the
  function gets a ctx argument.

### F8 — MultiEdit with N edits produces one span tree, hiding per-snippet outliers
- **Severity:** informational (feature gap, not a bug)
- **Reproducer:**
  ```bash
  # Payload with 3 edits → still 2 spans (pre-edit + sibling-scan), not 3+1
  OTEL_TRACES_EXPORTER=stdout /tmp/leonard-hook-otel pre-edit < /tmp/multi-payload.json 2>&1 |
    grep -oE '"Name":"leonard[^"]*"' | sort -u
  # → leonard.pre-edit, leonard.pre-edit.sibling-scan (no per-snippet spans)
  ```
- **Observed:** The snippet loop body in `decidePreEdit` (pre_edit.go:190-227)
  has no telemetry. A MultiEdit with N snippets executes N parse-snippet +
  N findFabricatedReferences passes invisibly to the operator.
- **Expected:** For diagnosing "this single edit ran slow," per-snippet
  timing helps. Today the operator just sees a wide pre-edit span and has to
  guess.
- **Suggested fix shape:** Wrap the loop body in a
  `leonard.pre-edit.snippet` span with the snippet index as an attribute
  (snippet=0..N-1). One span call per snippet — measurable cost is two
  allocations per edit, trivial. Out of scope for "fix the bugs we found"
  but worth a follow-up issue.

### F9 — `readSiblingPackages` ignores ctx cancellation
- **Severity:** informational
- **Reproducer:** Read pre_edit.go:361-404. The function takes no ctx, walks
  `moduleRoot` synchronously. A 10k-file walk that's mid-stream when the
  caller's ctx is cancelled (e.g., hook timeout) keeps walking until the OS
  signals process death.
- **Observed:** ctx cancellation has no influence on the sibling scan; an
  operator who configures a tight hook timeout will see the hook killed
  *during* the scan rather than gracefully cut short.
- **Expected:** A long-running walk should honor cancellation so the hook
  can produce a partial result + telemetry instead of being SIGKILL'd.
- **Suggested fix shape:** Plumb ctx through `readSiblingPackages` and
  return early when `ctx.Err() != nil` inside the walk callback.

### F10 — Malformed `OTEL_EXPORTER_OTLP_ENDPOINT` doesn't fail Init
- **Severity:** informational
- **Reproducer:**
  ```bash
  OTEL_EXPORTER_OTLP_ENDPOINT=not://a/real/url /tmp/leonard-hook-otel pre-edit < /tmp/pre-edit-payload.json
  # → exit 0, hook works, stderr shows: 
  #   "traces export: Post "https://a/real/url/v1/traces": remote error: tls: unrecognized name"
  ```
- **Observed:** The otlptracehttp exporter accepts the env var blindly,
  Init succeeds, and the malformed scheme only surfaces at export time as
  a noisy stderr log line per flush. The hook itself completes fine.
- **Expected:** Either reject obviously-bad endpoint values at Init (parse
  via `net/url`, require http/https scheme) or treat the exporter error log
  as acceptable. The current behavior is defensible — Init failure is
  documented as non-fatal — but the stderr noise might confuse operators
  who set a typo'd endpoint.
- **Suggested fix shape:** None required; document in README that the
  endpoint is passed through unvalidated.

## Things that worked

I verified all of these explicitly:

- **No-op zero-overhead claim.** `BenchmarkSpan` against a vendored copy of
  `internal/telemetry` (default build): **0.97 ns/op, 0 B/op, 0 allocs/op**.
  Nested (outer + inner): **1.57 ns/op, 0 B/op, 0 allocs/op**. The Go
  compiler inlines the no-op `Span` + `noopEnd` calls down to a one-cycle
  branch. Tagged build: **153 ns/op, 264 B/op, 4 allocs/op** for a single
  Span; **313 ns/op, 528 B/op, 8 allocs/op** nested. Real cost, real spans.
- **OTel deps absent from default binary.** `otool -L /tmp/leonard-default
  | grep -ci otel` → 0. `go tool nm /tmp/leonard-default | grep -c otel`
  → 0 (vs 1,801 with the tag). Build tag containment is clean.
- **Binary size discipline.** `cmd/leonard-mcp` is byte-identical with and
  without the tag because it doesn't import telemetry. `cmd/leonard-hook`
  stripped: 7.92 MB → 18.79 MB with the tag — meaningful but well under
  the LinuxServer-image-bundling typical for OTel toolchains.
- **All package tests pass under `-tags otel`** (`go test -tags otel ./...`).
- **`go test -tags otel -count=10 -race ./internal/telemetry/`** — clean.
  `goleak.VerifyNone` confirms no leaked goroutines after `Shutdown` in
  both stdout and OTLP modes.
- **Span parent/child structure is correct.** Verified by dumping the
  stdout exporter JSON and inspecting `Parent.SpanID` → `SpanContext.SpanID`
  edges:
  - pre-edit: `leonard.pre-edit` (root) → `leonard.pre-edit.sibling-scan`
    (parent=pre-edit, ChildSpanCount=1 on parent)
  - post-edit: `leonard.post-edit` (root) → `leonard.post-edit.index`,
    `leonard.post-edit.vet` (both with parent=post-edit, ChildSpanCount=2)
- **Empty env vars route to no-op.** `unset OTEL_*` → no exporter wired up,
  no stderr noise, no measurable overhead.
- **`OTEL_TRACES_EXPORTER=none`** returns the noop shutdown directly without
  standing up the SDK — matches the documented behavior.
- **Double-`End()` is safe** (verified by test): OTel's `span.End()` is
  idempotent. The `EndSpan` closure can be called twice without panic;
  the second call is a no-op and only one span is exported.
- **`Span()` called before `Init()`** doesn't panic — falls back to OTel's
  global default no-op TracerProvider. Spans are silently dropped, which
  is fine for a `defer end()` site in a non-Init'd binary.
- **`telemetry.Enabled()`** correctly returns `false` in default builds
  and `true` under `-tags otel`. The build-tag-confined `Enabled()`
  contract is honored.
- **Reachable OTLP collector flush.** With a real `httptest.Server` as
  endpoint, a fresh-ctx `Shutdown` actually POSTs spans (counted via
  request handler) — the SDK plumbing is functional when the collector
  is up.

## Open questions

- **Does the BatchSpanProcessor's queue have a sensible size for hooks?**
  Hooks emit 2-3 spans per invocation. The OTel default queue is 2048;
  that's fine for one-shot hook processes but worth confirming nothing
  bad happens at high-frequency post-edit firing (multi-file refactor).
- **Should `OTEL_TRACES_SAMPLER` be honored?** The current code always
  installs an "always-on" provider via `NewTracerProvider(WithBatcher(...))`.
  An operator who wants to sample 1% of pre-edit calls can't today.
  Low-priority — defer to first user request.
- **Does `OTEL_EXPORTER_OTLP_HEADERS` work?** Should — it's handled by the
  otlptracehttp exporter automatically. Untested in this audit.
- **What's the right action for the `internal/index` walker?** Spans there
  would solve the "is my full-tree reindex slow because of file count or
  parser cost" question, but require threading ctx through `Indexer.IndexFile`
  (the commit message explicitly deferred this). Worth its own design
  round before instrumenting.
- **Scratch artifacts.** Benchmark/test files used during this audit live
  in `/tmp/bench-vend/` (a vendored copy of `internal/telemetry` plus
  goroutine-leak + benchmark tests) and `/tmp/leonard-hook-otel`,
  `/tmp/lh-d`, `/tmp/lh-o`, `/tmp/lm-d`, `/tmp/lm-o` (built binaries used
  for size comparison). Not in version control.
