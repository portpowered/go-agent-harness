#!/usr/bin/env python3
"""Run bounded credential-free C135 shipped regression cases."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
CASES = {
    "help": (ROOT, ["proxy"]),
    "credential-free-audio-tool": (
        ROOT,
        [
            "go", "test", "./agent-cli/internal/services/internal/agentruntime",
            "-run", "TestSessionCommandAudioRoundtripRecordsNonSilentReply|TestSessionCommandAudioRoundtripSilentInputFailsAssertions|TestSessionCommandAudioRoundtripTruncatedInputFailsReplay",
            "-count=1", "-timeout=120s",
        ],
    ),
    "interruption": (
        ROOT,
        [
            "go", "test", "./agent-cli/internal/services/internal/agentruntime",
            "-run", "TestStartSessionAudioInterruptionsReleasesOnlyFirstMatchingDispatch|TestRTCDeviceSinkInterruptionUsesActuallyConsumedDeviceSamples|TestRTCDeviceSinkInterruptionTimerSurvives24kTo16kConversion",
            "-count=1", "-timeout=120s",
        ],
    ),
    "software-device": (
        ROOT,
        [
            "go", "test", "./agent-cli/internal/services/internal/devices",
            "-run", "TestService(EnumerateAndSelectExposeOnlyMetadata|ProbeAvailabilityAndCancellation|RunVirtualProbeUsesInputAndOutputContracts|RunVirtualProbeRejectsUnavailableAndCancelledRuns)",
            "-count=1", "-timeout=120s",
        ],
    ),
    "external-media": (
        ROOT,
        ["go", "test", "./agent-cli/internal/wire", "-count=1", "-timeout=120s"],
    ),
    "c21-consumption": (
        ROOT,
        [
            "go", "test", "./go-device-gateway/pkg/runtime",
            "-run", "^TestC21Consumption", "-count=1", "-timeout=120s",
        ],
    ),
}

class RunnerFailure(RuntimeError):
    pass


def safe_environment() -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    env = {
        key: value for key, value in os.environ.items()
        if not any(part in key.lower() for part in blocked)
    }
    env["GOCACHE"] = "/tmp/go-build-audio-runtime-c135"
    return env


def process_group_exists(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    except OSError:
        return True
    return True


def stop_group(process: subprocess.Popen[str]) -> tuple[bool, bool]:
    term_sent = kill_sent = False
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGTERM)
            term_sent = True
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=1.0)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
                kill_sent = True
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2.0)
            except subprocess.TimeoutExpired:
                pass
    else:
        process.terminate()
        term_sent = True
        try:
            process.wait(timeout=1.0)
        except subprocess.TimeoutExpired:
            process.kill()
            kill_sent = True
    return term_sent, kill_sent


def run_case(name: str, cwd: Path, argv: list[str], timeout: float, deadline: float, env: dict[str, str]) -> dict:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise RunnerFailure(f"aggregate timeout before {name}")
    process = subprocess.Popen(
        ["rtk", *argv],
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=(os.name == "posix"),
    )
    started = time.monotonic()
    timed_out = False
    term_sent = kill_sent = False
    stdout = stderr = ""
    try:
        stdout, stderr = process.communicate(timeout=min(timeout, remaining))
    except subprocess.TimeoutExpired as expired:
        timed_out = True
        stdout = (expired.stdout or "")[-8000:]
        stderr = (expired.stderr or "")[-8000:]
        term_sent, kill_sent = stop_group(process)
        final_stdout, final_stderr = process.communicate(timeout=3.0)
        stdout += final_stdout or ""
        stderr += final_stderr or ""
    reaped = process.poll() is not None
    if not reaped:
        try:
            process.kill()
        except ProcessLookupError:
            pass
        process.wait(timeout=2.0)
        reaped = True
    survivors = process_group_exists(process.pid) if os.name == "posix" else False
    return {
        "name": name,
        "argv": argv,
        "returncode": 124 if timed_out else process.returncode,
        "stdout": stdout[-8000:],
        "stderr": stderr[-8000:],
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "timed_out": timed_out,
        "cleanup": {
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "reaped": reaped,
            "survivors": survivors,
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--case", action="append", choices=sorted(CASES), required=True)
    parser.add_argument("--wait-for-close", action="store_true")
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=420)
    args = parser.parse_args()
    artifact = args.artifact if args.artifact.is_absolute() else ROOT / args.artifact
    if not artifact.is_file():
        parser.error(f"artifact does not exist: {artifact}")
    if args.child_timeout <= 0 or args.aggregate_timeout <= 0 or args.aggregate_timeout < args.child_timeout:
        parser.error("timeouts must be positive and aggregate timeout must cover child timeout")
    cases = {name: CASES[name] for name in dict.fromkeys(args.case)}
    cases["help"] = (ROOT, ["proxy", str(artifact), "--help"])
    env = safe_environment()
    deadline = time.monotonic() + args.aggregate_timeout
    results = {}
    for name in cases:
        cwd, argv = cases[name]
        result = run_case(name, cwd, argv, float(args.child_timeout), deadline, env)
        results[name] = result
        if result["timed_out"] or result["returncode"] != 0:
            break
    missing = sorted(set(cases) - set(results))
    report = {
        "schema": "audio-runtime-c135-shipped/v1",
        "artifact": {
            "path": str(artifact),
            "sha256": hashlib.sha256(artifact.read_bytes()).hexdigest(),
            "bytes": artifact.stat().st_size,
        },
        "source_revision": subprocess.run(
            ["rtk", "git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True
        ).stdout.strip(),
        "cases": results,
        "missing_cases": missing,
        "wait_for_close": args.wait_for_close,
        "hardware_acoustics": "out_of_scope",
    }
    report_path = HERE / "evidence" / "run-report.json"
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if not missing and all(result["returncode"] == 0 and not result["timed_out"] for result in results.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
