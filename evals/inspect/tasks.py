"""Inspect tasks: fabrication eval with and without Leonard's MCP tools.

Two tasks share one dataset and one scorer; the only difference is
whether the solver has Leonard's MCP tools wired into its turn loop.
The hypothesis is that the treated solver fabricates fewer symbol
references because it can verify_symbol before committing to a name.

Run:

    inspect eval evals/inspect/tasks.py@fabrication_control \\
                 evals/inspect/tasks.py@fabrication_with_leonard \\
        --model anthropic/claude-sonnet-4-5

Then compare the two log files with `inspect view`.
"""

from __future__ import annotations

import os

from inspect_ai import Task, task
from inspect_ai.dataset import MemoryDataset
from inspect_ai.solver import generate, system_message, use_tools
from inspect_ai.tool import mcp_server_stdio, mcp_tools

from samples import SAMPLES
from scoring import fabrication_scorer


def _project_root() -> str:
    """Resolve the Leonard repo root from this file's location.

    The eval references the real .leonard/leonard.db so the scorer's
    pre-edit hook can resolve sibling packages and verify the model's
    proposed symbols against the live index. Treating the eval as
    portable doesn't make sense — its samples are written against
    *this* codebase.
    """
    return os.path.abspath(os.path.join(os.path.dirname(__file__), os.pardir, os.pardir))


PROJECT_ROOT = _project_root()


SYSTEM_PROMPT = """You are a senior Go developer working in the Leonard codebase.

When asked to write code that references existing symbols, you must
only use names that actually exist in the project. Do not invent
function or method names that sound plausible — verify them first if
you're not sure. When you produce code, wrap it in a ```go fenced
code block.
"""


@task
def fabrication_control() -> Task:
    """Control arm: Claude writes Go code with no project-grounding tools.

    This measures the baseline fabrication rate from the model's
    priors plus whatever it can infer from the task description alone.
    """
    return Task(
        dataset=MemoryDataset(samples=SAMPLES),
        solver=[generate()],
        scorer=fabrication_scorer(PROJECT_ROOT),
    )


@task
def fabrication_with_leonard() -> Task:
    """Treatment arm: same prompts, but with Leonard's MCP tools wired in.

    The model can call verify_symbol / find_symbol / list_files
    against the real .leonard/leonard.db at PROJECT_ROOT before
    committing to a name. The hypothesis is fewer fabrications.
    """
    leonard = mcp_server_stdio(
        name="leonard",
        command="leonard-mcp",
        args=[],
        cwd=PROJECT_ROOT,
    )
    return Task(
        dataset=MemoryDataset(samples=SAMPLES),
        solver=[use_tools(mcp_tools(leonard)), generate()],
        scorer=fabrication_scorer(PROJECT_ROOT),
    )


TOOL_NUDGE = (
    "\nThe leonard MCP tools (verify_symbol, find_symbol, "
    "list_files) are available. Use verify_symbol to check any "
    "name you're unsure about before writing it into the code."
)


@task
def fabrication_prompt_only() -> Task:
    """Control + grounding system prompt, no tools.

    Isolates the effect of *telling* a model not to fabricate from the
    effect of giving it the means to check. Without this arm, any
    improvement in fabrication_with_leonard_system_prompt could be
    explained entirely by SYSTEM_PROMPT's "do not invent function or
    method names" instruction, with the tools contributing nothing.
    """
    return Task(
        dataset=MemoryDataset(samples=SAMPLES),
        solver=[system_message(SYSTEM_PROMPT), generate()],
        scorer=fabrication_scorer(PROJECT_ROOT),
    )


@task
def fabrication_with_leonard_system_prompt() -> Task:
    """Treatment + explicit system prompt nudging tool use.

    A spec issue: the control prompts say "use existing methods only"
    but don't tell the model HOW to verify. The treated arm exposes
    the tools; this arm also tells the model it has them and should
    use verify_symbol first. Measures the gap between "tools available"
    and "tools available + told to use them".

    The system prompt is applied as a SOLVER. Task() takes no
    `system_message` parameter — it accepts **kwargs and recognizes
    exactly four deprecated names (plan, tool_environment,
    epochs_reducer, max_messages), silently discarding anything else
    with no warning. Passing system_message= to Task() therefore did
    nothing, which made this arm an exact duplicate of
    fabrication_with_leonard. Every recorded run predates this fix.
    """
    leonard = mcp_server_stdio(
        name="leonard",
        command="leonard-mcp",
        args=[],
        cwd=PROJECT_ROOT,
    )
    return Task(
        dataset=MemoryDataset(samples=SAMPLES),
        solver=[
            system_message(SYSTEM_PROMPT + TOOL_NUDGE),
            use_tools(mcp_tools(leonard)),
            generate(),
        ],
        scorer=fabrication_scorer(PROJECT_ROOT),
        message_limit=20,
    )
