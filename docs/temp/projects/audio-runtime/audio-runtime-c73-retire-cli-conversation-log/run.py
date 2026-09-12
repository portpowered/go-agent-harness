#!/usr/bin/env python3
"""Run bounded C73 accumulated regressions without polling external CI."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from typing import Any


TASK_DIR = Path(__file__).resolve().parent
REPO_ROOT = TASK_DIR.parents[4]
CLI_DIR = REPO_ROOT / "agent-cli"
MAX_OUTPUT_BYTES = 64 * 1024


CASES = {
    "recorded-audio-tool-session-log": (
        "TestSessionDirectoryRecordingWritesConversationSessionLog|TestSessionDirectoryRecordingCapturesCorrelatedToolLifecycle",
    ),
    "non-recorded-audio-tool-regression": (
        "TestRunSessionWithRecordingDirectoryUsesRunnerAndPreservesPairedOutput|TestRunSessionWithRecordingDirectoryAudioFilesKeepsOnePersistentConversation",
    ),
    "runner-negative-controls": (
        "TestSessionRecordingFlagsRemainIndependentAndComposable|TestRunSessionWithRecordingDirectoryPreservesProviderAndRecordingErrorsOverEmptyRecording",
    ),
}


def bounded(text: str) -> str:
    encoded = text.encode("utf-8", errors="replace")
    if len(encoded) <= MAX_OUTPUT_BYTES:
        return text
    return encoded[:MAX_OUTPUT_BYTES].decode("utf-8", errors="replace") + "\n...[output capped]"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=sorted(CASES))
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=600.0)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= 60:
        parser.error("--child-timeout must be between 1 and 60 seconds")
    if not 0 < args.aggregate_timeout <= 600:
        parser.error("--aggregate-timeout must be between 1 and 600 seconds")

    started = time.monotonic()
    env = os.environ.copy()
    env["GOWORK"] = "off"
    command = [
        "go",
        "test",
        "./internal/services/internal/agentruntime",
        "-run",
        CASES[args.case][0],
        "-count=1",
        "-timeout=150s",
    ]
    try:
        completed = subprocess.run(
            command,
            cwd=CLI_DIR,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=min(args.child_timeout, args.aggregate_timeout),
            check=False,
        )
        status = "passed" if completed.returncode == 0 else "failed"
        output = bounded(completed.stdout)
        exit_code = completed.returncode
    except subprocess.TimeoutExpired as exc:
        status = "timeout"
        output = bounded((exc.stdout or "") if isinstance(exc.stdout, str) else "")
        exit_code = None

    result: dict[str, Any] = {
        "case": args.case,
        "status": status,
        "command": command,
        "child_timeout": args.child_timeout,
        "aggregate_timeout": args.aggregate_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "exit_code": exit_code,
        "output": output,
    }
    print(json.dumps(result, sort_keys=True))
    return 0 if status == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
