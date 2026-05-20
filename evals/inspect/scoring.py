"""Scorer: pipe the model's produced Go through Leonard's pre-edit hook.

The scorer extracts the fenced ```go block from the model's output,
constructs a synthetic PreToolUse payload pointing at a scratch file
inside the Leonard project root, and pipes it to `leonard-hook
pre-edit`. The hook's existing fabrication-detection logic (the same
code path that protects Claude Code in production) tells us which
symbol references in the snippet don't resolve against the project's
real symbol index.

A perfect score (1.0) means every symbol reference the model produced
exists in the index. Anything less indicates fabrication — and the
exact list of fabricated references is preserved in the score's
metadata so the report can show *what* the model invented, not just
how often it did.

Design note: we intentionally re-use Leonard's pre-edit hook rather
than reimplementing the check in Python. (1) The hook is the
production code path Leonard's value proposition rests on — measuring
something else would measure the wrong thing. (2) Any improvement to
the hook's heuristics (better sibling-package resolution, future
generic-type checking) automatically improves the eval.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess

from inspect_ai.scorer import Score, Target, accuracy, mean, scorer, stderr
from inspect_ai.solver import TaskState


GO_BLOCK_RE = re.compile(r"```go\s*\n(.+?)\n```", re.DOTALL)
FABRICATION_PREFIX = "blocked references to symbols not in the index:"


def extract_go_code(text: str) -> str | None:
    """Return the first ```go fenced block in text, or None if absent.

    Models occasionally emit unfenced code or fence it as ```golang or
    just ```; we accept the ```go form only because that's what the
    sample prompts explicitly request. A missing block is a different
    failure mode (the model didn't follow instructions) and the scorer
    surfaces it separately.
    """
    m = GO_BLOCK_RE.search(text)
    return m.group(1) if m else None


def detect_fabrications(code: str, project_root: str) -> tuple[list[str], str]:
    """Run leonard-hook pre-edit against code; return (fabricated, raw_response).

    The hook is the production fabrication detector. We synthesize a
    Write payload pointing at a scratch path inside project_root so the
    hook's sibling-package scan picks up the real module's packages.
    Empty list means the hook said "allow" — no fabrications found.
    """
    hook = shutil.which("leonard-hook")
    if hook is None:
        raise RuntimeError(
            "leonard-hook not found on PATH — run `go install ./cmd/leonard-hook`"
        )
    payload = json.dumps(
        {
            "session_id": "eval-fab-scorer",
            "hook_event_name": "PreToolUse",
            "cwd": project_root,
            "tool_name": "Write",
            "tool_input": {
                "file_path": os.path.join(project_root, "_eval_scratch.go"),
                "content": code,
            },
        }
    )
    proc = subprocess.run(
        [hook, "pre-edit"],
        input=payload,
        capture_output=True,
        text=True,
        timeout=30,
    )
    # exit 2 from blockOnDecode is a decode failure of *our* payload —
    # treat as scorer error, not a "no fabrications".
    if proc.returncode != 0:
        raise RuntimeError(
            f"leonard-hook pre-edit exit={proc.returncode}: {proc.stderr.strip()}"
        )
    response = json.loads(proc.stdout)
    spec = response.get("hookSpecificOutput", {})
    if spec.get("permissionDecision") != "deny":
        return [], proc.stdout
    reason = spec.get("permissionDecisionReason", "")
    if FABRICATION_PREFIX not in reason:
        return [], proc.stdout
    refs = reason.split(FABRICATION_PREFIX, 1)[1].strip()
    return [r.strip() for r in refs.split(",") if r.strip()], proc.stdout


@scorer(metrics=[accuracy(), mean(), stderr()])
def fabrication_scorer(project_root: str):
    """Score 1.0 for fabrication-free output, 0.0 when the hook flags any.

    Returns Score with rich metadata so the comparison report can
    distinguish "no block produced" (different bug — model didn't
    follow output spec) from "block produced but contained fabrications".
    """

    async def score(state: TaskState, target: Target) -> Score:
        output = state.output.completion
        code = extract_go_code(output)
        if code is None:
            return Score(
                value=0.0,
                answer=output[:200],
                explanation="no ```go fenced block in output",
                metadata={"failure_mode": "no_code_block", "fabricated": []},
            )
        try:
            fabricated, raw = detect_fabrications(code, project_root)
        except (RuntimeError, subprocess.TimeoutExpired) as exc:
            return Score(
                value=0.0,
                answer=code[:200],
                explanation=f"scorer error: {exc}",
                metadata={"failure_mode": "scorer_error", "fabricated": []},
            )
        if not fabricated:
            return Score(
                value=1.0,
                answer=code[:200],
                explanation="no fabricated references detected",
                metadata={"failure_mode": None, "fabricated": []},
            )
        return Score(
            value=0.0,
            answer=code[:200],
            explanation=f"hook blocked {len(fabricated)} reference(s): {', '.join(fabricated)}",
            metadata={"failure_mode": "fabrication", "fabricated": fabricated},
        )

    return score
