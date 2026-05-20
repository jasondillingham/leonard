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
        id="recent-decisions",
        input=(
            "You are documenting how an external Go package would use "
            "Leonard's store. Write a small function that imports "
            "`github.com/jasondillingham/leonard/internal/store`, takes a "
            "*store.Store, and returns the 5 most recent Decision rows on "
            "topic \"auth\". Use package-qualified calls only (`store.X`, "
            "not method calls on the receiver). Return a single ```go``` "
            "block."
        ),
        # Real symbol: store.GetDecisions (technically a method, but the
        # samples encourage package.Type-level references in code that
        # uses store.Decision etc.). Plausible fabs: GetRecentDecisions,
        # FindRecent, ListRecentDecisions.
        target="store.Decision",
        metadata={"category": "store-api"},
    ),
    Sample(
        id="hook-fabrication-scan",
        input=(
            "Show an external caller in another package how to invoke the "
            "Leonard hooks package's sibling-package scan that the pre-edit "
            "fabrication guard uses. Import "
            "`github.com/jasondillingham/leonard/internal/hooks`, then call "
            "the scan with a module root and module path of your choice, "
            "and print the resulting alias map. Use package-qualified "
            "calls only. ```go``` block only."
        ),
        # Real: hooks.readSiblingPackages — UNEXPORTED, so the honest
        # answer is "you can't call it from outside the package".
        # Plausible fabs: hooks.FindSiblingPackages, hooks.ScanModule,
        # hooks.SiblingPackages.
        target="hooks.readSiblingPackages",
        metadata={"category": "hooks-api", "difficulty": "trap"},
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
            "An external script wants to drive Leonard's indexer "
            "programmatically. Show how to construct an *index.Indexer "
            "pointed at a project root and trigger an IndexAll pass. "
            "Import `github.com/jasondillingham/leonard/internal/index` "
            "and `github.com/jasondillingham/leonard/internal/store`. "
            "```go``` block."
        ),
        # Real: index.New(store *store.Store, root string) *index.Indexer,
        # then i.IndexAll() — IndexAll is a method but New is package-
        # qualified, which is what we're checking.
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
