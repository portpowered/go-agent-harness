#!/usr/bin/env python3
"""Verify the bounded-summary consumer through only its public recording API."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


WATCHDOG_SECONDS = 50
SUMMARY_MAX_BYTES = 2 << 20
SUMMARY_MAX_ITEMS = 4096
QUEUE_CAPACITY_BYTES = 16 << 20
MAX_OBSERVED_HEAP_BYTES = 8 << 20


class VerificationError(Exception):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def repo_root() -> Path:
    result = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(result.stdout.strip())


def run_consumer(case: str, consumer: Path, root: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c19-verify-") as temp_dir:
        report_path = Path(temp_dir) / f"{case}.json"
        environment = os.environ.copy()
        source = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        environment.setdefault("C19_SOURCE_REVISION", source)
        command = [str(consumer), "--case", case, "--output", str(report_path)]
        try:
            completed = subprocess.run(
                command,
                cwd=root,
                env=environment,
                capture_output=True,
                text=True,
                timeout=WATCHDOG_SECONDS,
            )
        except subprocess.TimeoutExpired as error:
            raise VerificationError(f"consumer exceeded {WATCHDOG_SECONDS}s watchdog: {error}") from error
        require(completed.returncode == 0, f"consumer exited {completed.returncode}: {completed.stderr.strip()}")
        require(report_path.is_file(), "consumer did not write its JSON report")
        try:
            report = json.loads(report_path.read_text())
        except json.JSONDecodeError as error:
            raise VerificationError(f"consumer report is not valid JSON: {error}") from error
        require(not report.get("error"), f"consumer reported an error: {report.get('error')}")
        return {
            "command": command,
            "returncode": completed.returncode,
            "stdout": completed.stdout,
            "stderr": completed.stderr,
            "report": report,
        }


def verify_common(report: dict) -> None:
    budget = report.get("summary_budget", {})
    require(budget.get("max_bytes") == SUMMARY_MAX_BYTES, "summary byte limit changed from documented 2 MiB")
    require(budget.get("max_items") == SUMMARY_MAX_ITEMS, "summary item limit changed from documented 4096")
    require(report.get("queue", {}).get("capacity_bytes") == QUEUE_CAPACITY_BYTES, "queue capacity fixture changed")
    require(report.get("clean_shutdown") is True, "consumer did not report a clean shutdown")
    require(report.get("lock_released") is True, "recording lock was not released")


def verify_normal(report: dict) -> None:
    verify_common(report)
    finalization = report.get("finalization", {})
    normal = report.get("normal", {})
    require(finalization.get("stable") is True, "normal finalization was not idempotent")
    require(not finalization.get("first_error") and not finalization.get("second_error"), "normal finalization unexpectedly failed")
    require(normal.get("complete_summary") is True, "normal summary was not complete")
    require(normal.get("transcript_snapshots") is True, "transcript snapshot replacement was not preserved")
    require(normal.get("tool_result_exact_once") is True, "tool result was not retained exactly once")
    require(normal.get("late_audio_attributed") is True, "late response audio was not attributed")
    require(normal.get("pcm_bytes") == 48, "normal output PCM byte count changed")


def verify_overflow(report: dict) -> None:
    verify_common(report)
    finalization = report.get("finalization", {})
    overflow = report.get("overflow", {})
    first_error = finalization.get("first_error", "")
    require(finalization.get("stable") is True, "overflow finalization was not idempotent")
    require("budget exceeded" in first_error and "short buffer" in first_error, "summary overflow was not observable")
    require(overflow.get("partial_status") is True, "overflow did not publish partial status")
    require(overflow.get("raw_tail_present") is True, "raw evidence after summary overflow was lost")
    require(overflow.get("terminal_preserved") is True, "terminal evidence after summary overflow was lost")
    require(overflow.get("raw_pcm_bytes") == 6, "raw PCM after summary overflow was lost")
    require(overflow.get("silent_success") is False, "overflow was silently reported as success")


def verify_characterize(report: dict) -> None:
    verify_common(report)
    require(report.get("finalization", {}).get("stable") is True, "characterization finalization was not stable")
    measurements = report.get("measurements", [])
    controls = report.get("no_recording_control", [])
    require([item.get("turns") for item in measurements] == [16, 64, 128, 256, 512], "characterization checkpoints changed")
    require([item.get("turns") for item in controls] == [16, 64, 128, 256, 512], "no-recording controls are incomplete")
    require(all(item.get("finalization_stable") and item.get("lock_released") for item in measurements), "a characterization run did not finalize cleanly")
    require(max(item.get("heap_live_bytes", 0) for item in measurements) <= MAX_OBSERVED_HEAP_BYTES, "observed retained heap exceeded the frozen evidence envelope")
    for measurement, control in zip(measurements, controls):
        require(measurement.get("events") == control.get("events"), "recording/control event counts diverged")
        require(measurement.get("payload_bytes") == control.get("payload_bytes"), "recording/control payload fixtures diverged")
        require(measurement.get("shutdown_elapsed_ms", 0) >= 0, "shutdown elapsed time is invalid")


def verify_matrix(report: dict) -> None:
    normal = dict(report)
    normal["finalization"] = report.get("normal_finalization", {})
    verify_normal(normal)
    overflow = dict(report)
    overflow["finalization"] = report.get("overflow_finalization", {})
    verify_overflow(overflow)


def verify(case: str, execution: dict) -> None:
    report = execution["report"]
    require(report.get("case") == case, f"consumer case mismatch: {report.get('case')!r}")
    if case == "normal":
        verify_normal(report)
    elif case == "overflow":
        verify_overflow(report)
    elif case == "characterize":
        verify_characterize(report)
    elif case == "matrix":
        verify_matrix(report)
    else:
        raise VerificationError(f"unsupported case {case!r}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", choices=("normal", "overflow", "characterize", "matrix"), required=True)
    parser.add_argument("--consumer", type=Path, required=True, help="built public consumer executable")
    parser.add_argument("--output", type=Path, help="write the verifier report here")
    args = parser.parse_args()

    result: dict = {"case": args.case, "consumer": str(args.consumer)}
    try:
        root = repo_root()
        consumer = args.consumer if args.consumer.is_absolute() else (Path.cwd() / args.consumer)
        require(consumer.is_file(), f"consumer executable not found: {consumer}")
        execution = run_consumer(args.case, consumer.resolve(), root)
        verify(args.case, execution)
        result.update({"passed": True, "execution": execution})
    except (OSError, subprocess.SubprocessError, VerificationError) as error:
        result.update({"passed": False, "error": str(error)})

    rendered = json.dumps(result, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered)
    print(rendered, end="")
    return 0 if result.get("passed") else 1


if __name__ == "__main__":
    sys.exit(main())
