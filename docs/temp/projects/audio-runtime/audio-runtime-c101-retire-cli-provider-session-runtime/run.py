#!/usr/bin/env python3
"""Run bounded, credential-free C101 CLI replay probes."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time


TASK_DIR = Path(__file__).resolve().parent
ROOT = TASK_DIR.parents[4]
DEFAULT_BINARY = TASK_DIR / "artifacts/yui"
CASES = ("openai-replay", "grok-replay", "divergent-replay", "non-provider-smoke")


def scrub(value: str) -> str:
    value = re.sub(r"(?i)(api[_-]?key|authorization|token|secret|password)=?[^\s,;]+", r"\1=[REDACTED]", value)
    return value[-1600:]


def command_for(case: str, binary: Path) -> tuple[list[str], bool, tuple[str, ...]]:
    if case == "openai-replay":
        return [str(binary), "session", "--replay", str(ROOT / "agent-cli/test/integration/testdata/openai_realtime_text.session.json")], True, ("replay_complete",)
    if case == "grok-replay":
        return [str(binary), "session", "--replay", str(ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json")], True, ("Hello there",)
    if case == "divergent-replay":
        return [str(binary), "session", "--replay", str(ROOT / "agent-cli/test/integration/testdata/openai_realtime_text.session.json"), "--prompt", "c101-divergent-prompt"], False, ("replay mismatch",)
    return [str(binary), "--help"], True, ("Usage:",)


def run_case(case: str, binary: Path, timeout_seconds: int) -> dict:
    command, expected_success, markers = command_for(case, binary)
    env = os.environ.copy()
    for key in list(env):
        if re.search(r"(?i)(api[_-]?key|authorization|token|secret|password)", key):
            env.pop(key, None)
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=ROOT,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=(os.name != "nt"),
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        timed_out = True
        if os.name != "nt":
            os.killpg(process.pid, signal.SIGKILL)
        else:
            process.kill()
        stdout, stderr = process.communicate()
    combined = stdout + "\n" + stderr
    success = (
        not timed_out
        and ((process.returncode == 0) == expected_success)
        and all(marker.lower() in combined.lower() for marker in markers)
    )
    return {
        "case": case,
        "command": command,
        "exit_code": process.returncode,
        "expected_success": expected_success,
        "timed_out": timed_out,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "markers": markers,
        "passed": success,
        "stdout_tail": scrub(stdout),
        "stderr_tail": scrub(stderr),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, default=DEFAULT_BINARY)
    parser.add_argument("--case", action="append", choices=CASES)
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"binary is not executable: {binary}")
    cases = args.case or list(CASES)
    started = time.monotonic()
    results = []
    for case in cases:
        if time.monotonic() - started >= args.aggregate_timeout:
            results.append({"case": case, "passed": False, "error": "aggregate timeout exhausted"})
            continue
        results.append(run_case(case, binary, args.child_timeout))
    report = {"cases": results, "passed": all(result.get("passed", False) for result in results)}
    (TASK_DIR / "artifacts/run-report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
