package code

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
)

// RegisterTools satisfies adapters.Adapter. Attaches the v1 code-symbol
// tool set (verify_symbol, find_symbol, list_files, plus the gated
// decisions/claims/recent_changes tools when the cached store satisfies
// those interfaces) to srv.
//
// In degraded mode (no .leonard/leonard.db) RegisterTools is a no-op:
// the tools would have nothing to query and would return surprising
// "store unavailable" errors on every call. Skipping registration
// instead means MCP clients see a server that simply doesn't advertise
// the code tools until init has happened.
func (a *CodeAdapter) RegisterTools(srv *mcp.Server) error {
	snap := a.snapshot()
	if snap.storeAdapter == nil {
		return nil
	}
	leonardmcp.RegisterTools(srv, snap.storeAdapter)
	return nil
}
