#!/usr/bin/env python3
"""Run the bounded credential-free C116 regression cases without CI polling."""

from __future__ import annotations

import argparse
import os
import pathlib
import subprocess
import sys


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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=sorted(CASES), required=True)
    parser.add_argument("--child-timeout", type=int, default=120)
    parser.add_argument("--aggregate-timeout", type=int, default=360)
    args = parser.parse_args()

    if args.aggregate_timeout < args.child_timeout:
        parser.error("aggregate timeout must cover the child timeout")
    environment = safe_environment()
    for name in args.case:
        cwd, child = CASES[name]
        result = subprocess.run(
            ["rtk", *child], cwd=cwd, env=environment,
            text=True, capture_output=True, timeout=args.child_timeout,
        )
        sys.stdout.write(result.stdout)
        sys.stderr.write(result.stderr)
        if result.returncode != 0:
            print(f"C116 case failed: {name}", file=sys.stderr)
            return result.returncode or 1
        print(f"C116 case passed: {name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
