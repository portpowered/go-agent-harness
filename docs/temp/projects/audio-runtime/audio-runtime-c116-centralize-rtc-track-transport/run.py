#!/usr/bin/env python3
"""Run the bounded credential-free C116 regression cases without CI polling."""

from __future__ import annotations

import argparse
import os
import pathlib
import signal
import subprocess
import sys
import time


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]

CASES: dict[str, tuple[pathlib.Path, list[str]]] = {
    "rtc-track-roundtrip": (
        ROOT,
        ["go", "test", "./go-agent-runtime/services/rtctransport/...", "-count=1"],
    ),
    "external-media": (
        ROOT / "agent-cli",
        ["go", "test", "./internal/wire", "-count=1"],
    ),
    "device-probe-software": (
        ROOT / "agent-cli",
        [
            "go", "test", "./internal/transport/cli", "-run",
            "TestS2SV9WebRTCDevice", "-count=1", "-timeout=120s",
        ],
    ),
    "c21-consumption-replay": (
        ROOT,
        [
            "go", "test", "./go-device-gateway/pkg/runtime", "-run",
            "^TestC21Consumption", "-count=1", "-timeout=120s",
        ],
    ),
    "credential-free-audio-tool": (
        ROOT / "agent-cli",
        [
            "go", "test", "./internal/services/internal/agentruntime", "-run",
            "TestSessionCommandAudioRoundtripRecordsNonSilentReply|TestSessionCommandAudioRoundtripSilentInputFailsAssertions|TestSessionCommandAudioRoundtripTruncatedInputFailsReplay",
            "-count=1", "-timeout=120s",
        ],
    ),
}


def safe_environment() -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    return {
        key: value for key, value in os.environ.items()
        if not any(part in key.lower() for part in blocked)
    }


def stop_process_group(process: subprocess.Popen[str]) -> None:
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            process.wait(timeout=0.5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
    else:
        process.terminate()
        try:
            process.wait(timeout=0.5)
        except subprocess.TimeoutExpired:
            process.kill()


def run_bounded(
    child: list[str], cwd: pathlib.Path, environment: dict[str, str], timeout: float,
) -> tuple[int, str, str, bool]:
    options: dict[str, object] = {
        "cwd": cwd,
        "env": environment,
        "text": True,
        "stdout": subprocess.PIPE,
        "stderr": subprocess.PIPE,
    }
    if os.name == "posix":
        options["start_new_session"] = True
    process = subprocess.Popen(["rtk", *child], **options)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        stop_process_group(process)
        stdout, stderr = process.communicate()
        return process.returncode or 124, stdout, stderr, True
    return process.returncode, stdout, stderr, False


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=sorted(CASES), required=True)
    parser.add_argument("--child-timeout", type=int, default=120)
    parser.add_argument("--aggregate-timeout", type=int, default=360)
    args = parser.parse_args()

    if args.child_timeout <= 0 or args.aggregate_timeout <= 0:
        parser.error("timeouts must be positive")
    if args.aggregate_timeout < args.child_timeout:
        parser.error("aggregate timeout must cover the child timeout")
    environment = safe_environment()
    deadline = time.monotonic() + args.aggregate_timeout
    for name in args.case:
        cwd, child = CASES[name]
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            print(f"C116 aggregate deadline exceeded before case: {name}", file=sys.stderr)
            return 124
        returncode, stdout, stderr, timed_out = run_bounded(
            child, cwd, environment, min(float(args.child_timeout), remaining),
        )
        sys.stdout.write(stdout)
        sys.stderr.write(stderr)
        if timed_out:
            print(f"C116 case timed out: {name}", file=sys.stderr)
            return 124
        if returncode != 0:
            print(f"C116 case failed: {name}", file=sys.stderr)
            return returncode or 1
        print(f"C116 case passed: {name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
