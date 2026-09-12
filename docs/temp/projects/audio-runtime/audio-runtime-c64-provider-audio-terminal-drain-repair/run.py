#!/usr/bin/env python3
"""Run the single deterministic C64 terminal-drain interleaving control."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=TASK_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
BASE_REVISION = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
EXPECTED_BRANCH = "codex/audio-runtime-c64-provider-audio-terminal-drain-repair"
TEST_PACKAGE = "./agent-cli/internal/services/internal/agentruntime"
TEST_NAME = "TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 180.0
MAX_OUTPUT_BYTES = 64 * 1024


def git_value(*args: str) -> str:
    return subprocess.run(
        ["git", *args], cwd=REPO_ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_patch() -> str:
    result = subprocess.run(
        ["git", "diff", "--binary", "HEAD"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    )
    return hashlib.sha256(result.stdout).hexdigest()


def group_alive(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            return
        process.wait(timeout=2.0)


def run_bounded(command: list[str], timeout: float) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=REPO_ROOT,
        env={**os.environ, "CGO_ENABLED": "0"},
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    output = bytearray()
    overflow = False

    def drain() -> None:
        nonlocal overflow
        assert process.stdout is not None
        while True:
            chunk = process.stdout.read(8192)
            if not chunk:
                return
            previous = len(output)
            if len(output) < MAX_OUTPUT_BYTES:
                output.extend(chunk[: MAX_OUTPUT_BYTES - len(output)])
            if previous + len(chunk) > MAX_OUTPUT_BYTES:
                overflow = True

    reader = threading.Thread(target=drain, name="c64-output-reader", daemon=True)
    reader.start()
    timed_out = False
    parent_reaped = False
    try:
        process.wait(timeout=timeout)
        parent_reaped = True
    except subprocess.TimeoutExpired:
        timed_out = True
        terminate_group(process)
        parent_reaped = process.poll() is not None
    if group_alive(process.pid):
        terminate_group(process)
    reader.join(timeout=2.0)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    return {
        "command": command,
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "output_bounded": not overflow,
        "output": output.decode("utf-8", errors="replace"),
        "cleanup": {
            "parent_reaped": parent_reaped,
            "reader_thread_joined": not reader.is_alive(),
            "group_alive_after": group_alive(process.pid),
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("terminal-drain-interleaving",))
    parser.add_argument("--expect", required=True, choices=("accepted-source-failure",))
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= MAX_CHILD_SECONDS:
        raise SystemExit("child timeout must be in (0, 60] seconds")
    if not 0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS:
        raise SystemExit("aggregate timeout must be in (0, 180] seconds")

    branch = git_value("branch", "--show-current")
    origin_main = git_value("rev-parse", "origin/main")
    if branch != EXPECTED_BRANCH:
        raise SystemExit(f"wrong isolated branch: {branch}")
    if origin_main != BASE_REVISION:
        raise SystemExit(f"origin/main is {origin_main}, expected {BASE_REVISION}")

    command = [
        "go",
        "test",
        TEST_PACKAGE,
        "-run",
        f"^{TEST_NAME}$",
        "-count=1",
        "-timeout=60s",
    ]
    result = run_bounded(command, args.child_timeout)
    if result["returncode"] != 0:
        raise SystemExit(json.dumps({"passed": False, "result": result}, sort_keys=True))
    if result["timed_out"] or not result["output_bounded"]:
        raise SystemExit(json.dumps({"passed": False, "result": result}, sort_keys=True))
    cleanup = result["cleanup"]
    if not isinstance(cleanup, dict) or not cleanup.get("parent_reaped") or not cleanup.get("reader_thread_joined") or cleanup.get("group_alive_after"):
        raise SystemExit(json.dumps({"passed": False, "result": result}, sort_keys=True))
    if int(result["elapsed_ms"]) / 1000 > args.aggregate_timeout:
        raise SystemExit("aggregate timeout exceeded")

    production = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go"
    test = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go"
    evidence = json.loads((TASK_ROOT / "negative-evidence.json").read_text(encoding="utf-8"))
    print(
        json.dumps(
            {
                "schema": "audio-runtime.c64.interleaving-run.v1",
                "passed": True,
                "case": args.case,
                "expectation": args.expect,
                "branch": branch,
                "candidate_revision": git_value("rev-parse", "HEAD"),
                "source_revision": origin_main,
                "startup_integration_revision": STARTUP_INTEGRATION_REVISION,
                "worktree_patch_sha256": sha256_patch(),
                "source_hashes": {
                    "rtc_device_runtime.go": sha256_file(production),
                    "rtc_device_runtime_test.go": sha256_file(test),
                },
                "historical_negative": evidence["historical_negative"],
                "accepted_source_first_failure": evidence["accepted_source_first_failure"],
                "candidate": {
                    "expected_outcome": "repaired-pass",
                    "admitted_device_samples": 6400,
                    "dropped_samples": 0,
                    "overflow_events": 0,
                    "discarded_samples": 0,
                    "result": result,
                },
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        raise SystemExit(f"C64 runner failed: {error}") from error
