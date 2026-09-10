#!/usr/bin/env python3
"""Run one focused command with the C29 bounded outer deadline."""

import argparse
import json
from pathlib import Path
import subprocess
import sys
import time


MAX_SECONDS = 120
MAX_OUTPUT_BYTES = 64 * 1024


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    command = list(args.command)
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        raise SystemExit("a command is required after --")
    started = time.monotonic()
    timed_out = False
    try:
        result = subprocess.run(
            command,
            cwd=Path(__file__).resolve().parents[5],
            capture_output=True,
            text=True,
            check=False,
            timeout=MAX_SECONDS,
        )
        stdout = result.stdout
        stderr = result.stderr
        returncode = result.returncode
    except subprocess.TimeoutExpired as error:
        timed_out = True
        stdout = error.stdout or ""
        stderr = error.stderr or ""
        returncode = 124
    elapsed = time.monotonic() - started
    if len(stdout.encode()) > MAX_OUTPUT_BYTES or len(stderr.encode()) > MAX_OUTPUT_BYTES:
        raise SystemExit("focused command output exceeded the bounded capture")
    report = {
        "command": command,
        "timeoutSeconds": MAX_SECONDS,
        "elapsedSeconds": elapsed,
        "timedOut": timed_out,
        "returncode": returncode,
        "stdout": stdout,
        "stderr": stderr,
    }
    args.report.resolve().write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps({"status": "passed" if returncode == 0 else "failed", "report": str(args.report.resolve())}))
    return returncode


if __name__ == "__main__":
    raise SystemExit(main())
