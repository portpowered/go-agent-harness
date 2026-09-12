#!/usr/bin/env python3
"""Run the bounded C74 external-consumer cases with causal negative controls."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
CONSUMER = HERE / "external-consumer"

CASES: dict[str, tuple[str, int, str]] = {
    "default-explicit-transport": ("valid", 0, '"default_transport":"ws"'),
    "default-and-explicit-transport": ("valid", 0, '"explicit_transport":"webrtc"'),
    "credential-free-replay": ("replay", 0, '"status":"replay-ok"'),
    "credential-free-audio-tool-replay": ("replay", 0, '"status":"replay-ok"'),
    "invalid-model": ("invalid-model", 1, "not realtime-capable"),
    "invalid-transport": ("invalid-transport", 1, "invalid session runtime selection"),
    "runner-negative-controls": ("invalid-transport", 1, "invalid session runtime selection"),
}


def execute(name: str, child_timeout: int) -> dict[str, Any]:
    mode, expected_exit, expected_text = CASES[name]
    started = time.monotonic()
    completed = subprocess.run(
        ["go", "run", ".", mode],
        cwd=CONSUMER,
        env={**os.environ, "GOWORK": "off"},
        capture_output=True,
        text=True,
        timeout=child_timeout,
    )
    output = completed.stdout + completed.stderr
    if completed.returncode != expected_exit:
        raise RuntimeError(f"{name}: exit {completed.returncode}, expected {expected_exit}: {output}")
    if expected_text not in output:
        raise RuntimeError(f"{name}: missing causal marker {expected_text!r}: {output}")
    return {
        "case": name,
        "argv": ["go", "run", ".", mode],
        "cwd": str(CONSUMER),
        "expected_exit": expected_exit,
        "observed_exit": completed.returncode,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=tuple(CASES) + ("all",), default="all")
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=600)
    args = parser.parse_args()
    names = list(CASES) if args.case == "all" else [args.case]
    try:
        started = time.monotonic()
        results = []
        for name in names:
            if time.monotonic() - started > args.aggregate_timeout:
                raise RuntimeError(f"aggregate timeout after {args.aggregate_timeout}s")
            results.append(execute(name, args.child_timeout))
    except (OSError, subprocess.SubprocessError, RuntimeError) as exc:
        print(json.dumps({"status": "FAILED", "error": str(exc)}), file=sys.stderr)
        return 1
    print(json.dumps({"status": "ACCEPTED", "cases": results}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
