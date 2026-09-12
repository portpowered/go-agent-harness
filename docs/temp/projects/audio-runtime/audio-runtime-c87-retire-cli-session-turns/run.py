#!/usr/bin/env python3
"""Run the bounded, credential-free public session-turn consumer."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time


HERE = Path(__file__).resolve().parent
ROOT = next(parent for parent in HERE.parents if (parent / "go.work").is_file())
CONSUMER = HERE / "external-consumer"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 300.0
MAX_OUTPUT_BYTES = 64 * 1024


class RunError(RuntimeError):
    pass


def git_value(*args: str) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True, timeout=20)
    return result.stdout.strip()


def clean_environment() -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        if name.endswith("_API_KEY") or name in {"OPENAI_API_BASE", "OPENAI_ORG_ID", "REALTIME_API_KEY"}:
            del environment[name]
    environment.update({"GOWORK": "off", "C87_SOURCE_REVISION": git_value("rev-parse", "HEAD")})
    return environment


def run_child(command: list[str], timeout: float, deadline: float) -> dict[str, object]:
    remaining = min(timeout, deadline - time.monotonic())
    if remaining <= 0:
        raise RunError("aggregate timeout expired before child execution")
    started = time.monotonic()
    timed_out = False
    try:
        result = subprocess.run(
            command,
            cwd=CONSUMER,
            env=clean_environment(),
            stdin=subprocess.DEVNULL,
            capture_output=True,
            timeout=remaining,
            check=False,
        )
        returncode = result.returncode
        stdout = result.stdout[:MAX_OUTPUT_BYTES]
        stderr = result.stderr[:MAX_OUTPUT_BYTES]
    except subprocess.TimeoutExpired as error:
        timed_out = True
        returncode = None
        stdout = (error.stdout or b"")[:MAX_OUTPUT_BYTES]
        stderr = (error.stderr or b"")[:MAX_OUTPUT_BYTES]
    elapsed_ms = round((time.monotonic() - started) * 1000)
    return {
        "argv": command,
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": stdout.decode(errors="replace"),
        "stderr": stderr.decode(errors="replace"),
        "output_bounded": len(stdout) <= MAX_OUTPUT_BYTES and len(stderr) <= MAX_OUTPUT_BYTES,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=["credential-free-audio-tool-or-ask"], required=True)
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= MAX_CHILD_SECONDS:
        raise RunError("child timeout must be between 0 and 60 seconds")
    if not 0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS:
        raise RunError("aggregate timeout must be between 0 and 300 seconds")

    deadline = time.monotonic() + args.aggregate_timeout
    cases = {
        "consumer-tests": run_child(["go", "test", "./...", "-count=1"], args.child_timeout, deadline),
        "consumer-program": run_child(["go", "run", "."], args.child_timeout, deadline),
    }
    passed = all(case["returncode"] == 0 and not case["timed_out"] for case in cases.values())
    report = {
        "schema": "audio-runtime.c87.credential-free-run/v1",
        "case": args.case,
        "passed": passed,
        "candidate_revision": git_value("rev-parse", "HEAD"),
        "credential_environment_removed": True,
        "bounds": {
            "child_timeout_seconds": args.child_timeout,
            "aggregate_timeout_seconds": args.aggregate_timeout,
            "max_output_bytes": MAX_OUTPUT_BYTES,
        },
        "cases": cases,
    }
    print(json.dumps(report, sort_keys=True))
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RunError, OSError, subprocess.SubprocessError) as error:
        print(f"run failure: {error}", file=sys.stderr)
        raise SystemExit(1)
