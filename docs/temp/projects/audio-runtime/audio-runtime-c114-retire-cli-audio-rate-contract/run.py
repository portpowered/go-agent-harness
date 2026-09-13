#!/usr/bin/env python3
"""Run bounded C114 software replay and caller regression cases."""

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
ROOT = HERE.parents[4]
RUNS = HERE / "runs"
CASES = {
    "c21-rate-consumption": [
        "go", "test", "./go-agent-runtime/services/audiorate/...",
        "-run", "Test(Convert|Resolve|Configure|Service|NewService)", "-count=1", "-timeout=180s",
    ],
    "c50-audio-tool-replay": [
        "go", "test", "./agent-cli/internal/services/internal/agentruntime",
        "-run", "Test(ReplayServiceConsumesActualWireObservationEnvelope|ReplayServiceRunsRealCoreLoopAgainstRecordedWire|SessionCommandAudioInputReplaysCommittedFixture)",
        "-count=1", "-timeout=240s",
    ],
    "room-scheduled-audio": [
        "go", "test", "./agent-cli/internal/services/internal/agentruntime",
        "-run", "Test(LiveRecordRuntimeScheduledAudioCompletesWithoutCapturedSessionClose|LiveRecordRuntimeScheduledAudioContinuesAfterEmptyDirectoryResult|PlanSessionRuntime_ScheduledAudioUsesPersistentLiveLifecycle)",
        "-count=1", "-timeout=240s",
    ],
}


class RunFailure(RuntimeError):
    pass


def run_case(name: str, command: list[str], timeout: float) -> dict[str, Any]:
    started = time.monotonic()
    environment = os.environ.copy()
    environment.pop("GOWORK", None)
    process = subprocess.Popen(
        ["rtk", "proxy", *command],
        cwd=ROOT,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout, stderr = exc.stdout or b"", exc.stderr or b""
        os.killpg(process.pid, signal.SIGTERM)
        try:
            tail_out, tail_err = process.communicate(timeout=3)
            stdout += tail_out
            stderr += tail_err
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            tail_out, tail_err = process.communicate()
            stdout += tail_out
            stderr += tail_err
    result = {
        "case": name,
        "command": command,
        "timeoutSeconds": timeout,
        "elapsedSeconds": round(time.monotonic() - started, 3),
        "exitCode": process.returncode,
        "timedOut": timed_out,
        "proofLevel": "SOFTWARE_REPLAY",
        "physicalConsumptionClaim": False,
        "stdout": stdout.decode(errors="replace")[-1 << 20 :],
        "stderr": stderr.decode(errors="replace")[-1 << 20 :],
    }
    if timed_out or process.returncode != 0:
        raise RunFailure(f"{name} failed: {result['stderr'][-1200:]}")
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=sorted(CASES), required=True)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=300)
    args = parser.parse_args()
    started = time.monotonic()
    report: dict[str, Any] = {"status": "ACCEPTED", "proofLevel": "SOFTWARE_REPLAY", "cases": []}
    try:
        for name in args.case:
            if time.monotonic() - started >= args.aggregate_timeout:
                raise RunFailure("aggregate timeout exceeded")
            report["cases"].append(run_case(name, CASES[name], args.child_timeout))
    except (OSError, RunFailure) as exc:
        report["status"] = "FAILED"
        report["error"] = str(exc)
    report["elapsedSeconds"] = round(time.monotonic() - started, 3)
    RUNS.mkdir(parents=True, exist_ok=True)
    path = RUNS / "run-cases.json"
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": report["status"], "report": str(path.relative_to(HERE))}, sort_keys=True))
    return 0 if report["status"] == "ACCEPTED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
