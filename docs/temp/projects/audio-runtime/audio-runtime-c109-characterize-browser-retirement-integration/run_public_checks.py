#!/usr/bin/env python3
"""Run bounded software-only C109 checks in a disposable required merge tree."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any

from analyze import C61, C83, FIXED_DATE, run as run_command, revision, status_lines, tree


ROOT = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
AGENT_LOOP_MODULE = "github.com/portpowered/go-agent-harness/go-agent-loop"
AGENT_RUNTIME_MODULE = "github.com/portpowered/go-agent-harness/go-agent-runtime"
AGENT_CLI_MODULE = "github.com/portpowered/go-agent-harness/agent-cli"
C61_DIR = "docs/temp/projects/audio-runtime/audio-runtime-c61-retire-cli-browser-scenario-contract"
C83_DIR = "docs/temp/projects/audio-runtime/audio-runtime-c83-retire-cli-browser-scenario-runner"


def safe_environment(extra: dict[str, str] | None = None) -> tuple[dict[str, str], list[str]]:
    markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    env = dict(os.environ)
    removed: list[str] = []
    for name in list(env):
        upper = name.upper()
        if any(marker in upper for marker in markers):
            removed.append(name)
            del env[name]
    if extra:
        env.update(extra)
    return env, sorted(removed)


def bounded(argv: list[str], cwd: Path, timeout: int, *, env_extra: dict[str, str] | None = None, label: str) -> dict[str, Any]:
    env, removed = safe_environment(env_extra)
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    timed_out = False
    try:
        output, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        output = exc.stdout or ""
        if isinstance(output, bytes):
            output = output.decode(errors="replace")
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            tail, _ = process.communicate(timeout=5)
            output += tail or ""
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            tail, _ = process.communicate()
            output += tail or ""
    elapsed = round(time.monotonic() - started, 3)
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        process_group_gone = True
    except PermissionError:
        process_group_gone = False
    else:
        process_group_gone = False
    output = output[-16000:]
    forbidden_output_markers = ("OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "C109_SECRET_MARKER", "PROBE_CREDENTIAL_MARKER")
    return {
        "label": label,
        "command": " ".join(argv),
        "working_tree": "synthetic-required-merge-tree",
        "status": "passed" if process.returncode == 0 and not timed_out else ("timeout" if timed_out else "failed"),
        "exit_code": process.returncode,
        "timeout_seconds": timeout,
        "elapsed_seconds": elapsed,
        "timed_out": timed_out,
        "process_group_gone": process_group_gone,
        "credential_environment_scrubbed": True,
        "removed_credential_environment_names": removed,
        "credential_output_markers_absent": not any(marker in output for marker in forbidden_output_markers),
        "output": output,
    }


def create_required_tree() -> tuple[Path, Path]:
    temporary_root = Path(tempfile.mkdtemp(prefix="c109-public-")).resolve()
    worktree = temporary_root / "tree"
    fixed_env = {
        "GIT_AUTHOR_NAME": "C109 Rehearsal",
        "GIT_AUTHOR_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_COMMITTER_NAME": "C109 Rehearsal",
        "GIT_COMMITTER_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_AUTHOR_DATE": FIXED_DATE,
        "GIT_COMMITTER_DATE": FIXED_DATE,
        "GIT_MERGE_AUTOEDIT": "no",
        "LC_ALL": "C",
    }
    run_command(["git", "worktree", "add", "--detach", str(worktree), MAIN], cwd=ROOT)
    try:
        for ref, label in ((C61, "c61"), (C83, "c83")):
            run_command(["git", "merge", "--no-ff", "--no-commit", ref], cwd=worktree, env=fixed_env)
            run_command(["git", "commit", "--no-verify", "-m", f"C109 public required merge {label}"], cwd=worktree, env=fixed_env)
    except Exception:
        run_command(["git", "merge", "--abort"], cwd=worktree, check=False)
        run_command(["git", "worktree", "remove", "--force", str(worktree)], cwd=ROOT, check=False)
        temporary_root.rmdir()
        raise
    return temporary_root, worktree


def command_set(case: str, tree_root: Path, child_timeout: int) -> list[tuple[list[str], Path, dict[str, str], str, int]]:
    runtime = tree_root / "go-agent-runtime"
    cli = tree_root / "agent-cli"
    loop = tree_root / "go-agent-loop"
    c61 = tree_root / C61_DIR
    c83 = tree_root / C83_DIR / "external-consumer"
    package_pattern = "Browser|Scenario|Runner|Order|Interrupt|Cancel|Terminal"
    coverpkg = ",".join(
        (
            f"{AGENT_CLI_MODULE}/internal/services/internal/agentruntime",
            f"{AGENT_RUNTIME_MODULE}/services/browserscenario/...",
            f"{AGENT_RUNTIME_MODULE}/services/browserrunner/...",
        )
    )
    commands: list[tuple[list[str], Path, dict[str, str], str, int]] = []
    if case == "browser-audio-tool":
        commands.extend(
            [
                (["go", "test", "./services/browserscenario/...", "./services/browserrunner/...", "-run", package_pattern, "-count=1", "-timeout=300s"], runtime, {}, "browserrunner+browserscenario normal", 300),
                (["go", "test", "-race", "./services/browserscenario/...", "./services/browserrunner/...", "-run", package_pattern, "-count=1", "-timeout=420s"], runtime, {}, "browserrunner+browserscenario race", 420),
                (["go", "test", "./...", "-count=1", "-timeout=180s"], c61 / "consumer", {"GOWORK": "off"}, "C61 external consumer normal GOWORK=off", 180),
                (["go", "test", "-race", "./...", "-count=1", "-timeout=240s"], c61 / "consumer", {"GOWORK": "off"}, "C61 external consumer race GOWORK=off", 240),
                (["go", "test", "./...", "-count=1", "-timeout=180s"], c83, {"GOWORK": "off"}, "C83 external consumer normal GOWORK=off", 180),
                (["go", "test", "-race", "./...", "-count=1", "-timeout=240s"], c83, {"GOWORK": "off"}, "C83 external consumer race GOWORK=off", 240),
                (["go", "test", "./pkg/agentloop", "-run", "^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$", "-count=3", "-timeout=120s"], loop, {}, "accepted-main delta barrier regression", 120),
                (["go", "test", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=180s"], cli, {}, "CLI cancellation normal", 180),
                (["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=240s"], cli, {}, "CLI cancellation race", 240),
                (["go", "test", "-tags=nomicrophone", "-count=10", "-timeout=300s", f"-coverpkg={coverpkg}", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation"], cli, {"CGO_ENABLED": "0", "GOWORK": "off"}, "nomicrophone cross-module coverpkg cancellation", 300),
                (["bash", "scripts/test-session-ci-regressions.sh", "all"], tree_root, {"COUNT": "1"}, "accumulated session CI regressions all", 300),
                (["python3", f"{C61_DIR}/run.py", "--mode", "shipped-yui-browser-audio-tool-replay"], tree_root, {}, "shipped credential-free browser/audio/tool workflow", 180),
            ]
        )
    elif case == "malformed-or-canceled":
        commands.extend(
            [
                (["go", "run", f"{C61_DIR}/verify.go", "--mode", "malformed-credential-overflow-timeout-cleanup"], tree_root, {}, "malformed credential overflow timeout cleanup", 180),
                (["go", "test", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=180s"], cli, {}, "canceled browser scenario normal", 180),
                (["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=240s"], cli, {}, "canceled browser scenario race", 240),
            ]
        )
    else:
        raise ValueError(f"unsupported case: {case}")
    return commands


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=("browser-audio-tool", "malformed-or-canceled"), required=True)
    parser.add_argument("--tree", type=Path)
    parser.add_argument("--child-timeout", type=int, default=90)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    temporary_root: Path | None = None
    worktree: Path
    if args.tree:
        worktree = args.tree.resolve()
    else:
        temporary_root, worktree = create_required_tree()
    checks: list[dict[str, Any]] = []
    started = time.monotonic()
    try:
        for argv, cwd, extra_env, label, command_timeout in command_set(args.case, worktree, args.child_timeout):
            remaining = args.aggregate_timeout - int(time.monotonic() - started)
            if remaining <= 0:
                checks.append({"label": label, "status": "skipped-aggregate-timeout", "command": " ".join(argv)})
                continue
            # The regression script is itself the aggregate child: its internal
            # normal/coverage/race lanes need the full bounded aggregate
            # window, while each focused command is capped by child-timeout.
            child_cap = command_timeout if label.startswith("accumulated ") else args.child_timeout
            timeout = min(command_timeout, child_cap if child_cap > 0 else command_timeout, remaining)
            checks.append(bounded(argv, cwd, max(1, timeout), env_extra=extra_env, label=label))
        passed = all(item.get("status") == "passed" for item in checks)
        process_groups_clean = all(item.get("process_group_gone", True) for item in checks)
        credential_free = all(item.get("credential_environment_scrubbed", True) and item.get("credential_output_markers_absent", True) for item in checks)
        result = {
            "schema": "audio-runtime-c109-public-checks-v1",
            "case": args.case,
            "status": "passed" if passed and process_groups_clean and credential_free else "failed",
            "synthetic": {
                "base": MAIN,
                "order": ["c61", "c83"],
                "tree": tree(ROOT, "HEAD", cwd=worktree),
                "status": status_lines(ROOT, cwd=worktree),
                "software_only": True,
                "hardware_or_acoustic_claim": False,
            },
            "bounded": {
                "child_timeout_seconds": args.child_timeout,
                "aggregate_timeout_seconds": args.aggregate_timeout,
                "elapsed_seconds": round(time.monotonic() - started, 3),
                "process_groups_clean": process_groups_clean,
            },
            "credential_free": credential_free,
            "effects": {
                "workflow": "local synthetic fixture/process only",
                "external_browser_or_microphone": False,
                "cleanup_claim": process_groups_clean and not status_lines(ROOT, cwd=worktree),
            },
            "checks": checks,
        }
    finally:
        if temporary_root and worktree.exists():
            run_command(["git", "worktree", "remove", "--force", str(worktree)], cwd=ROOT, check=False)
        if temporary_root:
            temporary_root.rmdir() if temporary_root.exists() and not any(temporary_root.iterdir()) else None
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"case": args.case, "status": result["status"], "output": str(args.output)}, sort_keys=True))
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
