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


GO_BLOCK_RE = re.compile(r"```\s*(?:go|golang)\s*\n(.+?)\n?```", re.DOTALL | re.IGNORECASE)
FABRICATION_PREFIX = "blocked references to symbols not in the index:"

# Module-level: run a one-time self-check at first scoring call so a
# silently-broken wiring surfaces as an error rather than a stream of
# misleading 1.0 scores. Pinned strings the hook is expected to emit
# match bughunt-3 eval F4's robustness concern.
_self_check_done = False
_self_check_lock_msg = (
    "scoring.py self-check failed: leonard-hook's deny response does not "
    "match the expected shape. The hook's wording may have drifted from "
    "the scorer's parser. Verify pre_edit.go's blockResponse format "
    "matches FABRICATION_PREFIX above."
)


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


def _run_self_check(project_root: str) -> None:
    """Verify the hook's deny-response wording matches our parser.

    Sends a snippet referencing an obvious fabricated symbol; expects
    the hook to deny with the FABRICATION_PREFIX prefix in its reason
    string. If the hook approves or the reason doesn't contain the
    prefix, the scorer would silently zero out every detection — so
    raise loudly at scorer construction time instead of letting an
    entire eval run finish with bogus 1.0 scores.
    """
    global _self_check_done
    if _self_check_done:
        return
    probe = (
        "package other\n"
        "import \"github.com/jasondillingham/leonard/internal/store\"\n"
        "var _ = store.ObviouslyFakeFnEvalSelfCheck\n"
    )
    fabs, raw = _detect_fabrications_raw(probe, project_root)
    if not any("ObviouslyFakeFnEvalSelfCheck" in f for f in fabs):
        raise RuntimeError(f"{_self_check_lock_msg}\nraw response: {raw[:500]}")
    _self_check_done = True


def _resolve_hook_bin() -> str:
    """Locate the leonard-hook binary the scorer pipes candidate code through.

    ROADMAP-v2 §1.2 names the PATH entry as one of the three things standing
    between this harness and a real number. `go install` writes to
    $(go env GOPATH)/bin, which is *not* on PATH by default — /usr/local/go/bin
    is the toolchain, not the install target — so `shutil.which` alone turns a
    correct setup into a hard failure at scorer init.

    Resolution order:
      1. $LEONARD_HOOK_BIN, if set. Explicit operator intent wins.
      2. PATH.
      3. $(go env GOPATH)/bin/leonard-hook, the default `go install` target.

    A bad explicit override raises rather than silently falling through to a
    different binary — scoring against the wrong hook would produce numbers
    that look fine and mean nothing, which is the failure mode the self-check
    and the forced cwd both already exist to prevent.
    """
    override = os.environ.get("LEONARD_HOOK_BIN")
    if override:
        if not (os.path.isfile(override) and os.access(override, os.X_OK)):
            raise RuntimeError(
                f"LEONARD_HOOK_BIN={override!r} is not an executable file. "
                "Unset it to fall back to PATH, or point it at a real "
                "leonard-hook binary."
            )
        return override

    found = shutil.which("leonard-hook")
    if found:
        return found

    try:
        gopath = subprocess.run(
            ["go", "env", "GOPATH"],
            capture_output=True,
            text=True,
            timeout=10,
            check=True,
        ).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        gopath = ""
    # GOPATH may hold several colon-separated entries; `go install` writes to
    # the bin/ of the first. Check every entry rather than treating the whole
    # string as one path (which would build "/a:/b/bin/leonard-hook").
    for entry in gopath.split(os.pathsep):
        if not entry:
            continue
        candidate = os.path.join(entry, "bin", "leonard-hook")
        if os.path.isfile(candidate) and os.access(candidate, os.X_OK):
            return candidate

    raise RuntimeError(
        "leonard-hook not found. Build it with `go install ./cmd/...` from the "
        "repo root, then either add $(go env GOPATH)/bin to PATH or set "
        "LEONARD_HOOK_BIN to the binary's full path."
    )


def detect_fabrications(code: str, project_root: str) -> tuple[list[str], str]:
    """Run leonard-hook pre-edit against code; return (fabricated, raw_response).

    The hook is the production fabrication detector. We synthesize a
    Write payload pointing at a scratch path inside project_root so the
    hook's sibling-package scan picks up the real module's packages.
    Empty list means the hook said "allow" — no fabrications found.

    First call also runs a self-check that the hook's deny wording
    still matches the parser; subsequent calls skip the check.
    """
    _run_self_check(project_root)
    return _detect_fabrications_raw(code, project_root)


def _detect_fabrications_raw(code: str, project_root: str) -> tuple[list[str], str]:
    """detect_fabrications minus the self-check, so the self-check can
    use it without recursing."""
    hook = _resolve_hook_bin()
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
    # bughunt-3 eval F1: the hook resolves the project root by
    # walking up from its own CWD looking for a .leonard/ directory.
    # When the scorer is launched from outside the repo (e.g. via
    # `inspect eval` from a Python venv dir), the hook's lookup
    # falls back to a permissive store and every snippet scores
    # 1.0 — silent. Force cwd=project_root so the hook always
    # resolves to the real .leonard/leonard.db Leonard's index lives
    # in.
    proc = subprocess.run(
        [hook, "pre-edit"],
        input=payload,
        capture_output=True,
        text=True,
        timeout=30,
        cwd=project_root,
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
