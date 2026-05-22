// Package dispatcher wires Leonard's adapter system into the
// cmd/leonard-hook and cmd/leonard-mcp binaries (#46). Its job is:
//
//  1. Decide which adapters to load (delegates to config.EnabledAdapters)
//  2. Construct + Init each adapter
//  3. Provide hook-time payload translation and aggregation helpers
//  4. Return a cleanup func that Closes all adapters in reverse order
//
// The side-effect imports below register the three v1.0 adapters in
// adapters' global registry on package init. Adding a new adapter
// type means adding one line here.
package dispatcher

import (
	// Adapter init() blocks call adapters.Register. Side-effect
	// imports trigger them.
	_ "github.com/jasondillingham/leonard/internal/adapters/code"
	_ "github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	_ "github.com/jasondillingham/leonard/internal/adapters/selflog"
)
