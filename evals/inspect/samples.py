"""Dataset of fabrication-prone coding prompts targeting Leonard's own repo.

Each sample asks the model to write Go code that references real Leonard
internals using *package-qualified* calls (e.g. `store.Foo`, `parse.Bar`,
`mcp.AddTool`). That's the form Leonard's pre-edit fabrication guard
currently checks: it walks `SelectorExpr` where the receiver is a known
package alias and validates the selected name against the symbol index.
Receiver-method calls (`s.Foo()` where `s` is a parameter) are out of
scope for the guard today, so the samples deliberately avoid that shape.

The eval runs every sample twice: once with Leonard's MCP tools
available (treatment), once without (control). The scorer pipes the
model's produced code through `leonard-hook pre-edit`, counts the
fabricated symbol references it detects, and surfaces the delta.

Adding samples: keep the task description deliberately *underspecified*
on the API names — say "the existing store helpers" rather than
"store.Foo". The whole point is to measure whether the model verifies
before referencing. Force package-qualified shape by phrasing the
prompt as "show how external callers would call ...".
"""

from inspect_ai.dataset import Sample


SAMPLES: list[Sample] = [
    Sample(
        id="store-open",
        input=(
            "Write a small Go program that opens a Leonard store at the "
            "path `./.leonard/leonard.db`. Import "
            "`github.com/jasondillingham/leonard/internal/store` and use "
            "the package-level constructor — do not invent a name. "
            "Return a single ```go``` block."
        ),
        # Real: store.Open(path string) (*store.Store, error). Plausible
        # fabs: store.New, store.OpenStore, store.NewStore, store.Connect.
        # Package-level (not method-on-receiver), so pre-edit's
        # fabrication guard can actually detect the wrong name.
        target="store.Open",
        metadata={"category": "store-api"},
    ),
    Sample(
        id="hooks-pre-edit-handler",
        input=(
            "Write a Go program that imports "
            "`github.com/jasondillingham/leonard/internal/hooks` and calls "
            "the package-level PreToolUse handler with a context, options, "
            "stdin reader, and stdout writer. Use the real function name "
            "from the package's public API. ```go``` block only."
        ),
        # Real: hooks.HandlePreEdit(ctx, opts, stdin, stdout). Exported,
        # package-qualified, fabrication-detectable. Plausible fabs:
        # hooks.PreEdit, hooks.HandlePreToolUse, hooks.DispatchPreEdit.
        target="hooks.HandlePreEdit",
        metadata={"category": "hooks-api"},
    ),
    Sample(
        id="parse-python-shape",
        input=(
            "Write a Go program that imports "
            "`github.com/jasondillingham/leonard/internal/parse`, runs the "
            "Python extractor on a file, and prints how many symbols came "
            "back. Use the real function name and signature. ```go``` "
            "block."
        ),
        # Real: parse.ExtractPython(path string, src []byte) ([]store.
        # Symbol, error). Plausible fabs: parse.ParsePython, parse.
        # ExtractPy, parse.ExtractFromPython.
        target="parse.ExtractPython",
        metadata={"category": "parse-api"},
    ),
    Sample(
        id="indexer-construct",
        input=(
            "Write a Go program that constructs an *index.Indexer rooted "
            "at \"/tmp/project\" against a given *store.Store. Import "
            "`github.com/jasondillingham/leonard/internal/index` and "
            "`github.com/jasondillingham/leonard/internal/store`. Use "
            "the real package-level constructor — don't invent one. "
            "```go``` block."
        ),
        # Real: index.New(store *store.Store, root string) *index.Indexer.
        # Pure package-qualified, no methods. Plausible fabs: index.NewIndexer,
        # index.CreateIndexer, index.Open.
        target="index.New",
        metadata={"category": "indexer-api"},
    ),
    Sample(
        id="parse-typescript-shape",
        input=(
            "Same shape as the Python example, but for TypeScript: "
            "import `github.com/jasondillingham/leonard/internal/parse` "
            "and call its TypeScript extractor on a file. Use the real "
            "function name. ```go``` block."
        ),
        # Real: parse.ExtractTypeScript. Plausible fabs: parse.ParseTS,
        # parse.ExtractTS, parse.ExtractTypescript (wrong case).
        target="parse.ExtractTypeScript",
        metadata={"category": "parse-api"},
    ),
    Sample(
        id="claim-store-helpers",
        input=(
            "Show external code that writes a claim row to a Leonard "
            "store. Import "
            "`github.com/jasondillingham/leonard/internal/store`, construct "
            "a store.Claim, and call the right store helper to persist "
            "it. Use package-qualified types and the real helper name. "
            "```go``` block."
        ),
        # Real: store.Claim{...}, then s.RecordClaim(c). The fields of
        # store.Claim matter here too — exercising store.Claim, store.
        # Decision (in other samples), etc.
        target="store.Claim",
        metadata={"category": "store-api"},
    ),
    Sample(
        id="config-shape-after-bughunt2",
        input=(
            "Show how external code reads Leonard's project-local config. "
            "Import `github.com/jasondillingham/leonard/internal/config`, "
            "call the helper that loads-or-defaults from a path, and "
            "print the value of the InjectDecisionsAtSessionStart field. "
            "```go``` block."
        ),
        # Real: config.LoadOrDefault(path string) (config.Config, error)
        # plus config.HooksConfig.InjectDecisionsAtSessionStart. Bughunt-2
        # Theme A trimmed several other fields off the struct, so this
        # sample also tests "is the model looking at CURRENT code or
        # stale priors". Plausible fabs: config.Load (exists but
        # different signature), config.Read, config.LoadConfig.
        target="config.LoadOrDefault",
        metadata={"category": "config-api"},
    ),
]
