#!/usr/bin/env python3
"""Run bounded, credential-free C82 shipped controls."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess


EVIDENCE_ROOT = Path(__file__).resolve().parent
REPO_ROOT = next(parent for parent in EVIDENCE_ROOT.parents if (parent / "go.work").is_file())
ARTIFACT_ROOT = EVIDENCE_ROOT / "artifacts"


class RunFailure(RuntimeError):
    pass


def run(argv: list[str], cwd: Path = REPO_ROOT, timeout: float = 60, env: dict[str, str] | None = None) -> dict:
    process_env = os.environ.copy()
    if env:
        process_env.update(env)
    try:
        result = subprocess.run(argv, cwd=cwd, env=process_env, capture_output=True, text=True, timeout=timeout, check=False)
        output = (result.stdout + result.stderr).strip()
        return {"argv": argv, "returncode": result.returncode, "timed_out": False, "output": output[-65536:]}
    except subprocess.TimeoutExpired as error:
        return {"argv": argv, "returncode": None, "timed_out": True, "output": str((error.stdout or "") + (error.stderr or ""))[-65536:]}


def checked(argv: list[str], cwd: Path = REPO_ROOT, timeout: float = 60, env: dict[str, str] | None = None) -> dict:
    result = run(argv, cwd, timeout, env)
    if result["returncode"] != 0 or result["timed_out"]:
        raise RunFailure(f"command failed: {' '.join(argv)}\n{result['output']}")
    return result


def help_process_smoke() -> list[dict]:
    ARTIFACT_ROOT.mkdir(parents=True, exist_ok=True)
    binary = ARTIFACT_ROOT / "yui"
    build = checked(["go", "build", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"], timeout=120)
    help_run = checked([str(binary), "--help"], timeout=30)
    return [build, help_run]


def audio_tool_continuation() -> list[dict]:
    return [checked(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "TestRunAgentLoopSessionTerminalOutcomesAlwaysDrainAcceptedDelta|TestLiveRecordRuntimeScheduledAudioCompletesWithoutCapturedSessionClose|TestLiveRecordRuntimeScheduledAudioContinuesAfterEmptyDirectoryResult", "-count=1", "-timeout=240s"], timeout=270)]


def replay_bypass() -> list[dict]:
    return [checked(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "TestSessionCommandAudioInputReplaysCommittedFixture|TestRunSessionReplayBypassesPairedDeviceFeedbackController", "-count=1", "-timeout=180s"], timeout=210)]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=["audio-tool-continuation", "replay-bypass", "help-process-smoke"])
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=240)
    args = parser.parse_args()
    if args.case == "audio-tool-continuation":
        commands = audio_tool_continuation()
    elif args.case == "replay-bypass":
        commands = replay_bypass()
    else:
        commands = help_process_smoke()
    print(json.dumps({"case": args.case, "passed": True, "commands": commands}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RunFailure as error:
        print(f"run failure: {error}")
        raise SystemExit(1)
