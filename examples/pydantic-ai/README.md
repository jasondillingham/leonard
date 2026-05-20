# Pydantic AI ↔ Leonard demo

A small Pydantic AI agent that asks Leonard's MCP server whether a list
of identifiers really exists in the current project. The point is to
show that any Pydantic AI agent can route "does X exist in this
codebase?" through Leonard with zero glue code — `MCPServerStdio`
launches `leonard-mcp` as a subprocess and its tools auto-register as
the agent's toolset.

## What it does

```
$ leonard init . && leonard index   # one-time setup for this project
$ ANTHROPIC_API_KEY=... uv run demo.py

symbol                    exists  kind       where
-------------------------------------------------------------------
Open                      True    function   internal/store/store.go:42
IndexAll                  True    function   internal/index/indexer.go:104
FabricatedDoesNotExist    False   unknown    (no match)

agent summary: Two real Leonard symbols verified; one fabrication caught.
```

The hardcoded `SYMBOLS_TO_CHECK` list in `demo.py` deliberately mixes
two real symbols with one fabrication. The agent must use Leonard's
`verify_symbol` tool to discriminate — and the typed `Report` output
forces it to commit to specific file/line/kind values from the tool
response rather than fabricating those too.

## Run it

From this directory:

```bash
# Optional: install pydantic-ai isolated from your global env
uv sync

# Make sure leonard-mcp is on PATH
go install ../../cmd/leonard-mcp

# Make sure the parent project has been initialized
cd ../..   # back to the leonard repo root
leonard init . && leonard index
cd examples/pydantic-ai

# Run
ANTHROPIC_API_KEY=sk-... uv run demo.py
```

The agent runs against whatever project is in `pwd` when you launch
it (Leonard's MCP server reads `.leonard/leonard.db` from its cwd).
Change directory into any Leonard-initialized project and point the
script there to verify against a different codebase.

## How it wires together

```
demo.py (Pydantic AI Agent)
   │
   │  toolsets=[MCPToolset(StdioTransport("leonard-mcp", ...))]
   ▼
leonard-mcp (stdio subprocess)
   │
   │  reads .leonard/leonard.db
   ▼
Leonard store (SQLite)
```

`StdioTransport` spawns `leonard-mcp` as a subprocess and pipes
JSON-RPC over stdin/stdout. `MCPToolset` performs the initialization
handshake and relays `tools/list` to the agent so `verify_symbol` /
`find_symbol` / `list_files` / `record_decision` / etc. all appear as
native tools the model can pick. No tool-schema duplication; if Leonard
adds a tool, it shows up here automatically the next run.

## Customizing

- **Different model:** the model string `anthropic:claude-sonnet-4-5`
  in `demo.py` is the only model-binding code. Swap for any Pydantic
  AI model identifier (`openai:gpt-4o`, `google-gla:gemini-...`).
- **Different task:** the `SYMBOLS_TO_CHECK` list + `SYSTEM_PROMPT`
  define what the agent does. For a code-review use case, pass the
  pre-edit snippets through and have the agent block fabricated
  references the way Leonard's hook does — but with LLM judgment
  over the verify-symbol responses.
- **Other MCP clients:** `StdioTransport` is part of FastMCP's
  client library. Anything that speaks stdio MCP (Claude Code,
  Cursor, Continue, Cline, …) can wire `leonard-mcp` the same way.
