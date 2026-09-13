#!/usr/bin/env python3
"""Run bounded C76 evidence children without polling CI or external services."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
CONSUMER = HERE / "consumer"
RUNS = HERE / "runs"
MAX_OUTPUT_BYTES = 64 * 1024


CASES: dict[str, list[tuple[str, list[str], Path, dict[str, str]]]] = {
    "consumer": [
        (
            "external-consumer",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=180s"],
            CONSUMER,
            {"GOWORK": "off"},
        )
    ],
    "focused": [
        (
            "devices-observability",
            [
                "rtk",
                "proxy",
                "go",
                "test",
                "./go-agent-runtime/services/devices/internal/observability",
                "-run",
                "Observer|Diagnostic|Metric|Projection|Fanout|Immutable",
                "-count=1",
                "-timeout=120s",
            ],
            ROOT,
            {},
        ),
        (
            "cli-playback-observability",
            [
                "rtk",
                "proxy",
                "go",
                "test",
                "./agent-cli/internal/services/internal/agentruntime",
                "-run",
                "Test(SessionPlayback|SessionCapture|EmitRoomParticipant|PlanSessionRuntimePlayback|RunRoom_HumanParticipantPlayback)",
                "-count=1",
                "-timeout=180s",
            ],
            ROOT,
            {},
        ),
    ],
    "room-overflow": [
        (
            "room-participant-overflow",
            [
                "rtk",
                "proxy",
                "go",
                "test",
                "./agent-cli/internal/services/internal/agentruntime",
                "-run",
                "^TestRunRoom_HumanParticipantPlaybackOverflowNamesParticipant$",
                "-count=1",
                "-timeout=180s",
            ],
            ROOT,
            {},
        )
    ],
    "shipped-loopback-rendered-loss": [
        (
            "shipped-agent-audio-device-server",
            [
                "rtk",
                "proxy",
                "go",
                "test",
                "./agent-cli/test/integration",
                "-run",
                "^TestAgentBinaryNaturalCloseDrainsRemoteDevicePCM$",
                "-count=1",
                "-timeout=180s",
            ],
            ROOT,
            {},
        )
    ],
    "existing-audio-tool-replay": [
        (
            "existing-c16-audio-tool-replay",
            [
                "python3",
                "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/verify.py",
                "--mode",
                "parity",
            ],
            ROOT,
            {},
        )
    ],
}


def kill_group(process: subprocess.Popen[bytes]) -> None:
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)


def run_child(label: str, command: list[str], cwd: Path, overrides: dict[str, str], timeout: float) -> dict[str, Any]:
    environment = os.environ.copy()
    environment.update(overrides)
    started = time.monotonic()
    process = subprocess.Popen(command, cwd=cwd, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        stdout = error.output or b""
        stderr = error.stderr or b""
        kill_group(process)
        trailing_stdout, trailing_stderr = process.communicate()
        stdout += trailing_stdout
        stderr += trailing_stderr
    elapsed_ms = round((time.monotonic() - started) * 1000)
    output_bounded = len(stdout) <= MAX_OUTPUT_BYTES and len(stderr) <= MAX_OUTPUT_BYTES
    record: dict[str, Any] = {
        "label": label,
        "command": command,
        "cwd": str(cwd),
        "environment_overrides": overrides,
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
        "output_bounded": output_bounded,
    }
    RUNS.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    stem = f"{stamp}-{label}"
    (RUNS / f"{stem}.stdout").write_bytes(stdout[:MAX_OUTPUT_BYTES])
    (RUNS / f"{stem}.stderr").write_bytes(stderr[:MAX_OUTPUT_BYTES])
    record["stdout"] = f"runs/{stem}.stdout"
    record["stderr"] = f"runs/{stem}.stderr"
    (RUNS / f"{stem}.json").write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return record


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=["all", *CASES], default="all")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=600.0)
    args = parser.parse_args()
    selected = [name for name in CASES if args.case == "all" or args.case == name]
    started = time.monotonic()
    results: list[dict[str, Any]] = []
    for name in selected:
        for label, command, cwd, overrides in CASES[name]:
            remaining = args.aggregate_timeout - (time.monotonic() - started)
            if remaining <= 0:
                results.append({"label": label, "timed_out": True, "returncode": None, "error": "aggregate timeout exhausted"})
                continue
            results.append(run_child(label, command, cwd, overrides, min(args.child_timeout, remaining)))
    passed = all(result.get("returncode") == 0 and not result.get("timed_out") and result.get("output_bounded") for result in results)
    summary = {
        "schema": "audio-runtime.c76.evidence-run.v1",
        "case": args.case,
        "passed": passed,
        "aggregate_elapsed_ms": round((time.monotonic() - started) * 1000),
        "children": results,
    }
    print(json.dumps(summary, sort_keys=True))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
