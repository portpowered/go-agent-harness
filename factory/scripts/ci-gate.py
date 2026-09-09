#!/usr/bin/env python3
"""Adapt the bounded CI waiter to the factory decision-envelope contract.

Usage: ``python3 factory/scripts/ci-gate.py <work-name>``

The factory invokes this script from ``FACTORY_ROOT``.  The work name resolves
to one managed ``.claude/worktrees/<work-name>`` checkout.  The checkout's
local commit is captured before CI polling and checked again afterwards; a
CI result for another commit is never accepted.  ``ci-wait.py`` owns all
polling and this adapter only classifies its terminal result for routing.

Every evaluated gate outcome is emitted as one native decision envelope and
exits zero so ``SCRIPT_RUN`` can route semantic rejection and infrastructure
failure through its authored transitions.  Values copied from GitHub are
bounded and URLs have query credentials removed; waiter diagnostics and raw
command output are never included in feedback.
"""

import contextlib
import importlib.util
import io
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit


SCRIPT_DIR = Path(__file__).resolve().parent
CI_WAIT_SCRIPT = SCRIPT_DIR / "ci-wait.py"
SHA_PATTERN = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
MAX_FEEDBACK_LENGTH = 2400
MAX_CHECKS_IN_FEEDBACK = 12
MAX_CHECK_NAME_LENGTH = 120
MAX_REASON_LENGTH = 320
GIT_CALL_TIMEOUT_SECONDS = 30
GH_CALL_TIMEOUT_SECONDS = 30
ANSI_ESCAPE_PATTERN = re.compile(
    r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[@-_])"
)
CONTROL_CHARACTER_PATTERN = re.compile(r"[\x00-\x1f\x7f-\x9f]")
SENSITIVE_ASSIGNMENT_PATTERN = re.compile(
    r"(?ix)"
    r"(?P<key>\b(?:authorization|proxy-authorization|"
    r"token|access[_-]?token|refresh[_-]?token|password|passwd|secret|"
    r"api[_-]?key|client[_-]?secret|private[_-]?key|credential(?:s)?)\b)"
    r"(?P<quote>['\"]?)"
    r"(?:\s*(?:[:=]\s*|\s+))"
    r"(?:(?:bearer|basic)\s+)?"
    r"(?:['\"][^'\"]*['\"]|[^\s,;)\]}]+)"
)
STANDALONE_CREDENTIAL_PATTERN = re.compile(
    r"(?i)\b(?P<scheme>bearer|basic)(?:\s*[:=]\s*|\s+)"
    r"[^\s,;)\]}]+"
)


class GateError(RuntimeError):
    """A bounded gate precondition or evidence failure."""


@dataclass(frozen=True)
class CIWaitRun:
    """Captured result of one in-process ci-wait invocation."""

    exit_code: int
    stdout: str
    stderr: str
    failure: object = None


def _factory_root():
    configured = os.environ.get("FACTORY_ROOT")
    return Path(configured).resolve() if configured else Path.cwd().resolve()


def _safe_text(value, limit=MAX_REASON_LENGTH):
    """Return bounded single-line text with credential-like values redacted."""
    text = "" if value is None else str(value)
    # Replace terminal escape sequences and all C0/C1 bytes with whitespace
    # before collapsing it.  Keeping a separator prevents a control byte from
    # joining a credential scheme and its value into an unredactable token.
    text = ANSI_ESCAPE_PATTERN.sub(" ", text)
    text = CONTROL_CHARACTER_PATTERN.sub(" ", text)
    text = " ".join(text.split())

    def redact_assignment(match):
        return f"{match.group('key')}{match.group('quote')}=[redacted]"

    text = SENSITIVE_ASSIGNMENT_PATTERN.sub(redact_assignment, text)
    text = STANDALONE_CREDENTIAL_PATTERN.sub(
        lambda match: f"{match.group('scheme')} [redacted]", text
    )
    return text[:limit]


def _safe_url(value):
    """Keep a job URL's useful path while dropping query/fragment secrets."""
    if not isinstance(value, str):
        return None
    value = value.strip()
    try:
        parsed = urlsplit(value)
    except ValueError:
        return None
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.netloc:
        return None
    # A userinfo component can carry a credential even when the query is
    # empty.  Refuse it instead of trying to reconstruct the authority.
    if parsed.username is not None or parsed.password is not None:
        return None
    host = parsed.hostname
    if not host:
        return None
    try:
        port = parsed.port
    except ValueError:
        return None
    authority = host
    if ":" in host and not host.startswith("["):
        authority = f"[{host}]"
    if port is not None:
        authority += f":{port}"
    url = urlunsplit((parsed.scheme.lower(), authority, parsed.path, "", ""))
    return _safe_text(url, 280)


def _valid_sha(value):
    return isinstance(value, str) and bool(SHA_PATTERN.fullmatch(value.lower()))


def _work_name(value):
    """Validate the public work-name shape without trusting path input."""
    if not isinstance(value, str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,119}", value):
        raise GateError("work name is not a safe lowercase identifier")
    return value


def resolve_worktree(root, work_name):
    """Resolve one managed worktree and reject path escapes or missing paths."""
    work_name = _work_name(work_name)
    managed_root = (Path(root) / ".claude" / "worktrees").resolve()
    worktree = (managed_root / work_name).resolve()
    if worktree.parent != managed_root:
        raise GateError("worktree path escapes the managed worktrees directory")
    if not worktree.is_dir():
        raise GateError(f"managed worktree does not exist for {work_name!r}")
    git_marker = worktree / ".git"
    if not git_marker.is_file():
        raise GateError(f"managed worktree for {work_name!r} is not a linked Git worktree")
    return worktree


def _git_output(worktree, *args):
    try:
        result = subprocess.run(
            ["git", "-C", str(worktree), *args],
            capture_output=True,
            text=True,
            timeout=GIT_CALL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise GateError(f"Git {args[0] if args else 'command'} was unavailable") from error
    if result.returncode != 0:
        raise GateError(f"Git {args[0] if args else 'command'} failed")
    value = (result.stdout or "").strip()
    if not value:
        raise GateError(f"Git {args[0] if args else 'command'} returned no value")
    return value


def pin_local_head(worktree, work_name):
    """Capture a valid local SHA and require the managed branch identity."""
    head = _git_output(worktree, "rev-parse", "HEAD").lower()
    if not _valid_sha(head):
        raise GateError("managed worktree HEAD is not a complete commit SHA")
    branch = _git_output(worktree, "branch", "--show-current")
    expected_branch = f"codex/{work_name}"
    if branch != expected_branch:
        raise GateError(
            f"managed worktree branch is {_safe_text(branch, 160)!r}; "
            f"expected {expected_branch!r}"
        )
    return head


def _load_ci_wait_module():
    spec = importlib.util.spec_from_file_location("factory_ci_wait", CI_WAIT_SCRIPT)
    if spec is None or spec.loader is None:
        raise GateError("bounded CI waiter could not be loaded")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_ci_wait(worktree, branch):
    """Run ci-wait in the target worktree while retaining its typed failure."""
    module = _load_ci_wait_module()
    stdout = io.StringIO()
    stderr = io.StringIO()
    previous_cwd = Path.cwd()
    previous_argv = sys.argv
    exit_code = 0
    try:
        os.chdir(worktree)
        sys.argv = [str(CI_WAIT_SCRIPT), branch]
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            try:
                module.main()
            except SystemExit as error:
                exit_code = error.code if isinstance(error.code, int) else 1
    finally:
        sys.argv = previous_argv
        os.chdir(previous_cwd)
    return CIWaitRun(
        exit_code=exit_code,
        stdout=stdout.getvalue(),
        stderr=stderr.getvalue(),
        failure=getattr(module, "last_failure", None),
    )


def _failure_kind(failure):
    value = getattr(failure, "kind", None)
    if hasattr(value, "value"):
        value = value.value
    return value if isinstance(value, str) else "infrastructure"


def _format_check(check):
    if not isinstance(check, dict):
        return "malformed check row"
    name = _safe_text(check.get("name") or check.get("context"), MAX_CHECK_NAME_LENGTH)
    state = _safe_text(check.get("state") or "unknown", 40)
    link = _safe_url(check.get("link") or check.get("detailsUrl"))
    detail = f"{name}={state}"
    if link:
        detail += f" ({link})"
    return detail


def _conflict_feedback(failure, pinned_head):
    """Route only a validated current-head conflict back to its executor."""
    pr = getattr(failure, "pr", None)
    head = getattr(failure, "head_ref_oid", "")
    if not isinstance(pr, int) or isinstance(pr, bool) or pr <= 0:
        raise GateError("conflict evidence has no valid PR number")
    if not _valid_sha(head):
        raise GateError("conflict evidence has no complete PR head SHA")
    head = head.lower()
    if head != pinned_head:
        return _safe_text(
            f"Conflict evidence for PR #{pr} is stale: remote head {head} does not "
            f"match the managed candidate {pinned_head}. This is not treated as a "
            "current-head conflict; return this task to the executor to update the "
            "candidate and restart CI.",
            MAX_FEEDBACK_LENGTH,
        )
    return _safe_text(
        f"PR #{pr} current candidate head {head} is explicitly "
        "mergeable=CONFLICTING. Return this task to the same executor: fetch "
        "origin main, merge main into the task branch, resolve the actual conflicts "
        "preserving both sides, run focused regressions, and push a changed head on "
        "the same PR.",
        MAX_FEEDBACK_LENGTH,
    )


def _failure_feedback(failure):
    """Build bounded routing feedback without forwarding waiter diagnostics."""
    kind = _failure_kind(failure)
    reason = _safe_text(getattr(failure, "reason", "CI waiter exited without classified evidence"))
    head = _safe_text(getattr(failure, "head_ref_oid", ""), 80)
    pr = getattr(failure, "pr", None)
    prefix = "CI checks failed" if kind == "checks-failed" else "CI gate infrastructure failed"
    if isinstance(pr, int) and not isinstance(pr, bool) and pr > 0:
        prefix += f" for PR #{pr}"
    if _valid_sha(head):
        prefix += f" at head {head}"
    checks = getattr(failure, "checks", ()) or ()
    formatted = [_format_check(check) for check in checks[:MAX_CHECKS_IN_FEEDBACK]]
    if formatted:
        reason += "; checks: " + "; ".join(formatted)
    if kind == "checks-failed":
        return _safe_text(
            f"{prefix}: {reason}. Return this task to the executor with the listed failures.",
            MAX_FEEDBACK_LENGTH,
        )
    return _safe_text(
        f"{prefix}: {reason}. Meta-planner intervention is required before retrying.",
        MAX_FEEDBACK_LENGTH,
    )


def _read_merged_pr_head(worktree, pr_number):
    """Read merged PR head evidence for ci-wait's explicit merged path."""
    try:
        result = subprocess.run(
            [
                "gh",
                "pr",
                "view",
                str(pr_number),
                "--json",
                "number,state,headRefOid",
            ],
            cwd=worktree,
            capture_output=True,
            text=True,
            timeout=GH_CALL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise GateError("merged PR head evidence was unavailable") from error
    if result.returncode != 0:
        raise GateError("merged PR head evidence was unavailable")
    try:
        evidence = json.loads((result.stdout or "").strip())
    except (TypeError, json.JSONDecodeError) as error:
        raise GateError("merged PR head evidence was malformed") from error
    if not isinstance(evidence, dict):
        raise GateError("merged PR head evidence was malformed")
    if evidence.get("number") != pr_number or evidence.get("state") != "MERGED":
        raise GateError("merged PR head evidence no longer identifies the same merged PR")
    head = evidence.get("headRefOid")
    if not _valid_sha(head):
        raise GateError("merged PR head evidence has no complete head SHA")
    return head.lower()


def evaluate(worktree, work_name, pinned_head):
    """Run the waiter and return either an envelope decision or a GateError."""
    if not _valid_sha(pinned_head):
        raise GateError("managed worktree HEAD pin is not a complete commit SHA")
    pinned_head = pinned_head.lower()
    branch = f"codex/{work_name}"
    run = run_ci_wait(worktree, branch)
    if run.exit_code != 0:
        failure = run.failure
        if failure is None:
            failure = type(
                "UnclassifiedCIWaitFailure",
                (),
                {
                    "kind": "infrastructure",
                    "reason": "CI waiter exited without classified evidence",
                },
            )()
        kind = _failure_kind(failure)
        if kind == "conflicting":
            current_head = _git_output(worktree, "rev-parse", "HEAD").lower()
            if current_head != pinned_head:
                return "REJECTED", _safe_text(
                    f"The managed worktree changed during conflict observation from "
                    f"{pinned_head} to {current_head}. The conflict evidence is not "
                    "treated as current; return this task to the executor to restart "
                    "CI for the new candidate.",
                    MAX_FEEDBACK_LENGTH,
                )
            return "REJECTED", _conflict_feedback(failure, pinned_head)
        decision = "REJECTED" if kind == "checks-failed" else "FAILED"
        return decision, _failure_feedback(failure)

    try:
        payload = json.loads(run.stdout)
    except (TypeError, json.JSONDecodeError) as error:
        raise GateError(
            f"CI waiter emitted invalid success JSON at position {getattr(error, 'pos', '?')}"
        ) from error
    if not isinstance(payload, dict) or payload.get("status") != "ready":
        raise GateError("CI waiter success output was not a ready result")

    reason = payload.get("reason")
    pr_number = payload.get("pr")
    if not isinstance(pr_number, int) or isinstance(pr_number, bool) or pr_number <= 0:
        raise GateError("CI waiter success output has no valid PR number")
    pr_state = payload.get("prState")
    if reason == "checks-terminal" and pr_state != "OPEN":
        raise GateError("CI waiter checks result did not identify an open PR")
    if reason == "pr-merged" and pr_state != "MERGED":
        raise GateError("CI waiter merged result did not identify a merged PR")
    if reason == "pr-merged":
        observed_head = _read_merged_pr_head(worktree, pr_number)
    elif reason == "checks-terminal":
        observed_head = payload.get("headRefOid")
        if not _valid_sha(observed_head):
            raise GateError("CI waiter success output has no complete current-head SHA")
        observed_head = observed_head.lower()
    else:
        raise GateError("CI waiter success output has an unknown success reason")

    if observed_head != pinned_head:
        return (
            "REJECTED",
            _safe_text(
                f"CI evidence is for stale head {observed_head}, while the managed "
                f"worktree candidate is {pinned_head}. Return this task to the "
                "executor to update the candidate and restart CI.",
                MAX_FEEDBACK_LENGTH,
            ),
        )
    current_head = _git_output(worktree, "rev-parse", "HEAD").lower()
    if current_head != pinned_head:
        return (
            "REJECTED",
            _safe_text(
                f"The managed worktree changed during CI polling from {pinned_head} "
                f"to {current_head}. Return this task to the executor to restart CI "
                "for the new candidate.",
                MAX_FEEDBACK_LENGTH,
            ),
        )

    check_count = payload.get("checks")
    if isinstance(check_count, int) and not isinstance(check_count, bool):
        evidence = f"{check_count} required checks"
    else:
        evidence = "required checks"
    if reason == "pr-merged":
        feedback = (
            f"PR #{pr_number} is already merged at head {pinned_head}; "
            "route to the reviewer for idempotent merged-state reconciliation."
        )
    else:
        feedback = f"All {evidence} passed for PR #{pr_number} at head {pinned_head}; ready for independent review."
    return "ACCEPTED", _safe_text(feedback, MAX_FEEDBACK_LENGTH)


def emit_envelope(decision, feedback):
    """Emit exactly one bounded native decision envelope."""
    if decision not in {"ACCEPTED", "REJECTED", "FAILED"}:
        decision = "FAILED"
    print(
        json.dumps(
            {"decision": decision, "feedback": _safe_text(feedback, MAX_FEEDBACK_LENGTH)},
            sort_keys=True,
        )
    )


def main(argv=None):
    args = list(sys.argv[1:] if argv is None else argv)
    if len(args) != 1:
        emit_envelope("FAILED", "Usage: ci-gate.py <work-name>")
        return 0

    work_name = args[0]
    try:
        root = _factory_root()
        worktree = resolve_worktree(root, work_name)
        pinned_head = pin_local_head(worktree, work_name)
        decision, feedback = evaluate(worktree, work_name, pinned_head)
    except GateError as error:
        decision = "FAILED"
        feedback = _safe_text(f"CI gate infrastructure failed: {error}")
    except Exception as error:
        # Keep unexpected local/runtime failures routable and bounded.  Do
        # not print a traceback or dependency command output into the envelope.
        decision = "FAILED"
        feedback = _safe_text(f"CI gate infrastructure failed: {error}")
    emit_envelope(decision, feedback)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
