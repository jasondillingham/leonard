"""Pydantic AI agent that consults Leonard before approving symbol references.

Run from inside a Leonard-initialized project (one where `leonard init`
has been executed and `.leonard/leonard.db` exists). The agent is asked
whether a list of symbol names is real or fabricated; it must use
Leonard's MCP tools to answer rather than guessing from its priors.

The point of this demo is to show that any Pydantic AI agent can route
"does X exist in this codebase?" through Leonard's MCP surface with
zero glue code: pydantic_ai.mcp.MCPServerStdio launches `leonard-mcp`
as a subprocess, its tools/list response is auto-registered as the
agent's toolset, and the typed result is whatever the LLM produces.

Requirements:
- `leonard-mcp` on PATH (run `go install ./cmd/leonard-mcp` from the
  repo root).
- A Leonard-initialized project in the current working directory
  (run `leonard init . && leonard index`).
- ANTHROPIC_API_KEY in the environment.
- pydantic-ai installed (see pyproject.toml or run via `uv run`).
"""

from __future__ import annotations

import asyncio
import os
import sys
from typing import Literal

from fastmcp.client.transports import StdioTransport
from pydantic import BaseModel, Field
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset


class SymbolVerdict(BaseModel):
    """One row of the agent's final report."""

    name: str = Field(description="the symbol name the agent was asked about")
    exists: bool = Field(description="true when verify_symbol returned a non-empty match set")
    where: str | None = Field(
        default=None,
        description="file:line of the first match, or null when no match",
    )
    kind: Literal["function", "method", "type", "const", "var", "interface", "unknown"] = Field(
        default="unknown",
        description="kind of the first match — proves the agent actually inspected the response, not just the bool",
    )


class Report(BaseModel):
    """The final structured output: one verdict per asked-about symbol."""

    verdicts: list[SymbolVerdict]
    summary: str = Field(description="one short sentence describing the verdict pattern")


SYMBOLS_TO_CHECK = ["Open", "IndexAll", "FabricatedDoesNotExist"]
"""Two real Leonard symbols and one obvious fabrication.

`Open` is `store.Open` (the SQLite store constructor). `IndexAll` is
the indexer's walk entry point. `FabricatedDoesNotExist` is the
control — the agent must not invent a match for it.
"""

SYSTEM_PROMPT = """You are a code-review assistant.

The user will give you a list of identifiers. For each one, call the
`verify_symbol` tool against the project index to determine whether
the symbol actually exists. Report what verify_symbol says — do not
guess. If verify_symbol returns matches, report the first match's
file/line and kind. If verify_symbol returns no matches, mark the
symbol as fabricated.

Do not call verify_symbol more than once per symbol."""


async def main() -> int:
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        print(
            "demo.py: set ANTHROPIC_API_KEY to run the agent against a real model",
            file=sys.stderr,
        )
        return 2

    cwd = os.getcwd()
    if not os.path.isdir(os.path.join(cwd, ".leonard")):
        print(
            f"demo.py: no .leonard/ in {cwd} — run `leonard init . && leonard index` first",
            file=sys.stderr,
        )
        return 2

    # StdioTransport spawns leonard-mcp as a subprocess and pipes
    # JSON-RPC over stdin/stdout. cwd is what leonard-mcp uses to
    # locate .leonard/leonard.db, so we forward this process's cwd
    # verbatim — the agent gets the same project view the user would
    # get in `leonard verify`. MCPToolset auto-discovers leonard's
    # tools/list response and registers each tool with the agent.
    transport = StdioTransport(command="leonard-mcp", args=[], cwd=cwd)
    leonard = MCPToolset(transport)

    agent = Agent(
        "anthropic:claude-sonnet-4-5",
        toolsets=[leonard],
        system_prompt=SYSTEM_PROMPT,
        output_type=Report,
    )

    prompt = "Check these symbols: " + ", ".join(SYMBOLS_TO_CHECK)
    async with leonard:
        result = await agent.run(prompt)
    report = result.output

    width = max(len(v.name) for v in report.verdicts) + 2
    print()
    print(f"{'symbol':<{width}}{'exists':<8}{'kind':<11}where")
    print("-" * (width + 8 + 11 + 30))
    for v in report.verdicts:
        where = v.where or "(no match)"
        print(f"{v.name:<{width}}{str(v.exists):<8}{v.kind:<11}{where}")
    print()
    print(f"agent summary: {report.summary}")
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
