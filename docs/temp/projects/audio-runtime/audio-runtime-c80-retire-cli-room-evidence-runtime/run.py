#!/usr/bin/env python3
"""Bounded executable probes for the C80 candidate.

The shipped yui route is checked without opening hardware or a provider. The
hermetic Go cases then exercise the actual room and non-room runtime paths.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import signal
import subprocess
import sys


PROJECT = Path(__file__).resolve().parent


def repo_root() -> Path:
    for candidate in (PROJECT, *PROJECT.parents):
        if (candidate / "go.work").is_file():
            return candidate
    raise RuntimeError("could not locate repository go.work")


ROOT = repo_root()
YUI = PROJECT / "artifacts/yui"


def run(command: list[str], cwd: Path, timeout: int, expect_success: bool = True) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env.setdefault("NO_COLOR", "1")
    if cwd == PROJECT / "external-consumer":
        env["GOWORK"] = "off"
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=(os.name == "posix"),
    )
    try:
        stdout, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGTERM)
        else:
            process.terminate()
        try:
            stdout, _ = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
            stdout, _ = process.communicate()
        raise SystemExit(f"probe timed out after {timeout}s: {' '.join(command)}\n{stdout}") from exc
    completed = subprocess.CompletedProcess(command, process.returncode, stdout, "")
    sys.stdout.write(stdout)
    if expect_success and completed.returncode:
        raise SystemExit(f"probe failed ({completed.returncode}): {' '.join(command)}")
    if not expect_success and completed.returncode == 0:
        raise SystemExit(f"negative probe unexpectedly succeeded: {' '.join(command)}")
    return completed


def require_yui() -> None:
    if not YUI.is_file():
        raise SystemExit(f"missing immutable yui artifact: {YUI}; build it with the PRD command first")


def room_record_evidence(timeout: int) -> None:
    require_yui()
    example = run([str(YUI), "room", "run", "--example"], ROOT, timeout)
    if '"participants"' not in example.stdout or '"schema_version": 1' not in example.stdout:
        raise SystemExit("yui room example did not expose the admitted manifest route")
    run(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "^TestRoomRunRecordThenReplay_FullEndToEndReplaySucceeds$", "-count=1", "-timeout=300s"], ROOT, timeout)
    consumer = PROJECT / "external-consumer"
    run(["go", "test", "./...", "-run", "^TestExternalConsumerUsesOnlyPublicRoomEvidencePorts$", "-count=1", "-timeout=90s"], consumer, timeout)


def corrupted_bundle(timeout: int) -> None:
    run(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "^TestLoadRoomReplayPlanRejectsSameLengthMutationAsMismatch$", "-count=1", "-timeout=300s"], ROOT, timeout)


def non_room_audio_tool_replay(timeout: int) -> None:
    run(["go", "test", "./agent-cli/test/integration", "-run", "^TestSessionToolCallConversationSpokenReplyReflectsRealToolResult$", "-count=1", "-timeout=240s"], ROOT, timeout)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=["room-record-evidence", "corrupted-bundle", "non-room-audio-tool-replay"])
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    args = parser.parse_args()
    timeout = min(args.child_timeout, args.aggregate_timeout)
    if args.case == "room-record-evidence":
        room_record_evidence(timeout)
    elif args.case == "corrupted-bundle":
        corrupted_bundle(timeout)
    else:
        non_room_audio_tool_replay(timeout)
    print(f"vertical probe case passed: {args.case}")


if __name__ == "__main__":
    main()
