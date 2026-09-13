#!/usr/bin/env python3
"""Run bounded credential-free RTC service and shipped-CLI replay probes."""

from __future__ import annotations

import argparse
import os
import signal
import subprocess
import sys
import time
from pathlib import Path


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[4]
YUI = HERE / "artifacts/yui"
REPLAY = REPO / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_text_reply.session.json"
MAX_OUTPUT = 64 * 1024


CASES = {
    "rtc-local-fixture": [
        (
            "rtc-positive-public-service",
            [
                "go", "test", "./agent-cli/internal/services/internal/agentruntime",
                "-run", "^TestRunSession_WebRTCCompletesHermeticTurnThroughExportedService$",
                "-count=1", "-timeout=30s", "-v",
            ],
            "PASS: TestRunSession_WebRTCCompletesHermeticTurnThroughExportedService",
        ),
        (
            "rtc-failure-identity",
            [
                "go", "test", "./agent-cli/internal/services/internal/agentruntime",
                "-run", "^TestSessionRTCRuntime_PreservesTypedFailuresAndRedactsMediaCredentials$",
                "-count=1", "-timeout=30s", "-v",
            ],
            "PASS: TestSessionRTCRuntime_PreservesTypedFailuresAndRedactsMediaCredentials",
        ),
    ],
    "non-rtc-replay": [
        (
            "shipped-yui-replay",
            [str(YUI), "session", "--replay", str(REPLAY)],
            "[session closed: fixture_complete]",
        ),
    ],
}


def run_child(label: str, command: list[str], timeout: int) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=REPO,
        env={**os.environ, "GOWORK": os.environ.get("GOWORK", "")},
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate(timeout=2)
    output = stdout + stderr
    return {
        "label": label,
        "command": command,
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "output_bytes": len(output.encode()),
        "output": output,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=tuple(CASES), action="append", required=True)
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=240)
    args = parser.parse_args()
    started = time.monotonic()
    results: list[dict[str, object]] = []
    try:
        for case in args.case:
            for label, command, marker in CASES[case]:
                if time.monotonic() - started > args.aggregate_timeout:
                    raise RuntimeError("aggregate timeout exceeded")
                if case == "non-rtc-replay" and not YUI.is_file():
                    raise RuntimeError(f"shipped yui is missing: {YUI}")
                result = run_child(label, command, args.child_timeout)
                output = str(result["output"])
                if result["returncode"] != 0 or result["timed_out"]:
                    raise RuntimeError(f"{label} failed: {result}")
                if int(result["output_bytes"]) > MAX_OUTPUT:
                    raise RuntimeError(f"{label} exceeded {MAX_OUTPUT}-byte output bound")
                if marker not in output:
                    raise RuntimeError(f"{label} missing causal marker {marker!r}: {output}")
                results.append({key: value for key, value in result.items() if key != "output"})
    except (OSError, subprocess.SubprocessError, RuntimeError) as error:
        print(f"verification failed: {error}", file=sys.stderr)
        return 1
    print({"status": "ACCEPTED", "cases": results})
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
