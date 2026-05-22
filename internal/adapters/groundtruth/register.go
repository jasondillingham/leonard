package groundtruth

import "github.com/jasondillingham/leonard/internal/adapters"

// init registers the "ground-truth" adapter in the global registry.
// Importing this package (typically via a side-effect import in the
// dispatcher, deferred to #46) makes the adapter discoverable to
// adapters.New.
//
// Unlike the code adapter, the ground-truth adapter has NO implicit-
// enable rule: it activates only when an [[adapters]] block in
// .leonard/config.toml explicitly opts in with type = "ground-truth".
// Operators have to author the truth tree, and silently turning the
// adapter on without one would be a footgun.
func init() {
	adapters.Register(Name, New)
}
