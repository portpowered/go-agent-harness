#!/usr/bin/env python3
"""Bounded credential-free software replay for the C79 lifecycle candidate."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
EVIDENCE = HERE / "evidence" / "runs"

CASES = {
    "scheduled-tool-continuation": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "^TestToolContinuationRemainsOneScheduledLifecycle$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Test(SessionProgressObserver_ChainedToolContinuation|RunAgentLoopSessionRetriesScheduledToolContinuationOnce)$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "^TestSessionCommand_CreditsConsecutiveScheduledToolContinuations$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "terminal-and-malformed": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "Test(Response|Malformed|Duplicate|Wrong|Terminal|Close|Order|Reset)",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Session(Diagnostics|Terminal|Cancellation)|SessionDiagnostics",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "credential-free-audio-tool-replay": [
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "Test(InteractionReplay_PrintsNormalizedEventsAsNDJSON|InteractionCommand_HelpDocumentsReplayOutputAndCredentialFreeBehavior|SessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI)$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
}


def clean_environment() -> dict[str, str]:
    env = os.environ.copy()
    for name in list(env):
        if name.endswith(("_API_KEY", "_TOKEN", "_SECRET")) or name in {"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"}:
            env.pop(name, None)
    env["GOCACHE"] = "/tmp/go-build-audio-runtime-c79-replay"
    return env


def run_child(argv: list[str], *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    remaining = max(0.1, deadline - time.monotonic())
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=ROOT, env=env, capture_output=True, text=True, timeout=min(timeout, remaining))
        return {
            "argv": argv,
            "returncode": result.returncode,
            "stdout": result.stdout[-8000:],
            "stderr": result.stderr[-8000:],
            "elapsed_seconds": round(time.monotonic() - started, 3),
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "argv": argv,
            "returncode": 124,
            "stdout": str(exc.stdout or "")[-8000:],
            "stderr": str(exc.stderr or "")[-8000:],
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "timeout": True,
        }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=sorted(CASES))
    parser.add_argument("--child-timeout", type=int, default=30)
    parser.add_argument("--aggregate-timeout", type=int, default=180)
    args = parser.parse_args()

    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    env = clean_environment()
    checks = []
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c79-yui-") as directory:
        binary = Path(directory) / "yui"
        checks.append(run_child(["go", "build", "-o", str(binary), "./agent-cli/cmd/yui"], timeout=args.child_timeout, deadline=deadline, env=env))
        if checks[-1]["returncode"] == 0:
            checks.append(run_child([str(binary), "interaction", "--help"], timeout=args.child_timeout, deadline=deadline, env=env))
        for command in CASES[args.case]:
            if time.monotonic() >= deadline:
                checks.append({"argv": command, "returncode": 124, "timeout": True, "error": "aggregate timeout exhausted"})
                break
            checks.append(run_child(command, timeout=args.child_timeout, deadline=deadline, env=env))

    result = {
        "schema": "audio-runtime-c79-replay/v1",
        "case": args.case,
        "credential_free": True,
        "software_replay_only": True,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "checks": checks,
    }
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    (EVIDENCE / f"{args.case}.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if all(check.get("returncode") == 0 for check in checks) else 1


if __name__ == "__main__":
    raise SystemExit(main())
