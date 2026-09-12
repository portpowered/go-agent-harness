#!/usr/bin/env python3
"""Run the credential-free C88 consumer and the existing non-room regression."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
CONSUMER = HERE / "external-consumer"
MAX_CHILD_SECONDS = 60.0
MAX_OUTPUT_BYTES = 64 * 1024


class RunError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RunError(message)


def run_child(args: list[str], cwd: Path, timeout: float) -> dict[str, object]:
    environment = os.environ.copy()
    environment.update({
        "GOWORK": "off",
        "C88_SOURCE_REVISION": "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f",
        "C88_CANDIDATE_REVISION": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip(),
        "HOME": str(HERE / ".run-home"),
    })
    started = time.monotonic()
    process = subprocess.Popen(args, cwd=cwd, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True, text=True)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        timed_out = False
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
            stdout, stderr = process.communicate(timeout=5)
    elapsed = time.monotonic() - started
    require(len(stdout.encode()) <= MAX_OUTPUT_BYTES and len(stderr.encode()) <= MAX_OUTPUT_BYTES, "child output exceeded the 64 KiB cap")
    return {"returncode": process.returncode, "timed_out": timed_out, "elapsed_ms": round(elapsed * 1000), "stdout": stdout, "stderr": stderr}


def run_consumer(timeout: float) -> dict[str, object]:
    result = run_child(["go", "run", "."], CONSUMER, timeout)
    require(result["returncode"] == 0 and result["timed_out"] is False, f"external consumer failed: {result['stderr']}")
    lines = str(result["stdout"]).splitlines()
    require(len(lines) == 1, f"external consumer emitted unexpected output: {result['stdout']}")
    report = json.loads(lines[0])
    require(report.get("schema") == "audio-runtime.c88.roomcapabilities-consumer/v1", "external consumer schema mismatch")
    require(report.get("constructed_via") == "roomcapabilities/wire.NewService", "external consumer bypassed the dedicated wire constructor")
    require(report.get("mismatch_error_identity") is True and report.get("static_preserved") is True and report.get("stale_tool_rejected") is True, "external consumer negative/refresh oracle failed")
    require(report.get("first_initialized_once") is True and report.get("first_closed_once") is True and report.get("second_initialized_once") is True and report.get("second_closed_once") is True, "external consumer lifecycle oracle failed")
    return {"label": "credential-free-room-tool-composition", "execution": {key: value for key, value in result.items() if key not in ("stdout", "stderr")}, "report": report}


def run_non_room(timeout: float) -> dict[str, object]:
    result = run_child(["go", "test", "./test/integration", "-tags=nomicrophone", "-run", "^TestSessionToolSingleCallRoundTripThroughCLI$", "-count=1", "-timeout=45s", "-v"], ROOT / "agent-cli", timeout)
    require(result["returncode"] == 0 and result["timed_out"] is False, f"non-room audio/tool regression failed: {result['stderr']} {result['stdout'][-2000:]}")
    require("PASS" in str(result["stdout"]), "non-room regression did not report PASS")
    return {"label": "non-room-audio-tool-regression", "execution": {key: value for key, value in result.items() if key not in ("stdout", "stderr")}, "sentinel": "TestSessionToolSingleCallRoundTripThroughCLI"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=["credential-free-room-tool-composition", "non-room-audio-tool-regression", "all"], default="all")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    require(0 < args.child_timeout <= MAX_CHILD_SECONDS, "child timeout must be between 0 and 60 seconds")
    require(0 < args.aggregate_timeout <= 300.0, "aggregate timeout must be between 0 and 300 seconds")
    started = time.monotonic()
    results = []
    if args.case in ("credential-free-room-tool-composition", "all"):
        results.append(run_consumer(args.child_timeout))
    if args.case in ("non-room-audio-tool-regression", "all"):
        results.append(run_non_room(args.child_timeout))
    require(time.monotonic() - started <= args.aggregate_timeout, "aggregate evidence timeout exceeded")
    print(json.dumps({"task": "audio-runtime-c88-retire-cli-room-tool-capabilities", "passed": True, "cases": results}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RunError, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        print(f"evidence run failed: {error}")
        raise SystemExit(1)
