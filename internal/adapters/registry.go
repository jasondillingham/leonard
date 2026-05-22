package adapters

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a fresh, uninitialized Adapter. Adapter packages
// call Register from their init() function so the global registry is
// populated by the time the dispatcher reads config.toml.
type Factory func() Adapter

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// ErrUnknownAdapter wraps the lookup error returned by New when no
// factory is registered under the given name. Callers can use
// errors.Is(err, ErrUnknownAdapter) to surface a clean "did you mean..."
// message instead of leaking the raw lookup failure.
var ErrUnknownAdapter = errors.New("unknown adapter")

// Register associates a name with a Factory. Adapter packages MUST
// call this from their init() function. Duplicate registration panics
// — a duplicate name is always a wiring bug (two packages both
// claiming "code") that we want to catch at startup, not at first use.
//
// Register is safe to call concurrently though in practice all
// registration happens during program initialization on a single
// goroutine.
func Register(name string, f Factory) {
	if name == "" {
		panic("adapters: Register called with empty name")
	}
	if f == nil {
		panic("adapters: Register called with nil factory for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("adapters: duplicate registration: " + name)
	}
	registry[name] = f
}

// New looks up a registered adapter by name and constructs an
// instance. The returned Adapter is uninitialized — callers must
// invoke Init before any hook or MCP call. Returns an error wrapping
// ErrUnknownAdapter when no factory is registered.
func New(name string) (Adapter, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAdapter, name)
	}
	return f(), nil
}

// Names returns the sorted list of registered adapter names. Used by
// the CLI's "leonard init" to print available adapter types and by
// telemetry to attribute hook events to a known adapter.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// reset clears the registry. Exported only via the _test.go shim
// (resetRegistry) so package tests can isolate from each other; it
// has no production callers.
func reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Factory{}
}
