package code

import "github.com/jasondillingham/leonard/internal/adapters"

// init registers the "code" adapter in the global adapters registry.
// Importing this package (via a side-effect import in cmd/leonard-hook
// or cmd/leonard-mcp once those binaries are rewired through the
// dispatcher) is enough to make the adapter discoverable to
// adapters.New.
//
// Implicit-enable: the dispatcher's enabled-adapters resolution rule
// (deferred to the cmd-rewiring follow-up issue) will register the
// code adapter when EITHER:
//
//   - [[adapters]] with type = "code" appears in .leonard/config.toml,
//     OR
//   - the project has a go.mod at the root (the v0.52 default — every
//     Go project gets the code adapter without ceremony), OR
//   - [post_edit.verify] is set (an operator who configured a
//     verifier has implicitly opted into post-edit verification, which
//     is a code-adapter concern)
//
// This init() only handles the registry side; the implicit-enable
// rule is the dispatcher's job and lives outside this package.
func init() {
	adapters.Register(Name, New)
}
