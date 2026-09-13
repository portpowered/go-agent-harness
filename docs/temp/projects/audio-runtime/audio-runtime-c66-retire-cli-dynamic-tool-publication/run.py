#!/usr/bin/env python3
"""Run bounded focused C66 process evidence without credentials or CI polling."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time


ROOT = Path(__file__).resolve().parent
REPO_ROOT = ROOT.parents[4]


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def clean_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = "off"
    environment["LANG"] = "C"
    environment["LC_ALL"] = "C"
    return environment


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def stop_group(process: subprocess.Popen[bytes]) -> None:
    if group_alive(process.pid):
        os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        if group_alive(process.pid):
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=2.0)


def run_child(label: str, argv: list[str], cwd: Path, timeout: float) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=clean_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        stop_group(process)
        stdout, stderr = process.communicate(timeout=2.0)
    require(not timed_out, f"{label} exceeded child timeout {timeout}s")
    require(process.returncode == 0, f"{label} failed with {process.returncode}: {stderr.decode(errors='replace')}")
    require(not group_alive(process.pid), f"{label} left a live process group")
    return {
        "label": label,
        "argv": argv,
        "cwd": str(cwd.relative_to(REPO_ROOT)),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "stdout": stdout.decode(errors="replace"),
        "stderr": stderr.decode(errors="replace"),
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "credential_free_environment": True,
        "process_group_reaped": True,
    }


def case_commands(case: str) -> list[tuple[str, list[str], Path]]:
    if case == "managed-browser-page-tool-switch":
        return [
            (
                "hermetic-cli-page-surface-switch",
                [
                    "go",
                    "test",
                    "./internal/services/internal/agentruntime",
                    "-run",
                    "^TestSessionDynamicToolPublisher_HermeticCatalogSwitchExecutesCurrentSurface$",
                    "-count=1",
                    "-timeout=120s",
                ],
                REPO_ROOT / "agent-cli",
            )
        ]
    if case == "refresh-send-watch-failures":
        return [
            (
                "runtime-publication-failures",
                [
                    "go",
                    "test",
                    "./services/toolpublication/internal/publisher",
                    "-run",
                    "TestPublisher(DigestNoOpAndSchemaFailureIdentity|SinkFailureRetainsSurfaceAndNilPortsFailClosed|WatchCloseCancellationAndStopAreDeterministic)$",
                    "-count=1",
                    "-timeout=120s",
                ],
                REPO_ROOT / "go-agent-runtime",
            )
        ]
    if case == "credential-free-audio-tool-replay":
        return [
            (
                "credential-free-audio-replay-controls",
                [
                    "go",
                    "test",
                    "./pkg/recording",
                    "-run",
                    "Test(ReplayLifecycleAcceptsOneCleanSession|TraceReplaySplitsBlocksAndAdvancesDeterministicClock)$",
                    "-count=1",
                    "-timeout=120s",
                ],
                REPO_ROOT / "go-audio",
            ),
            (
                "credential-free-public-tool-consumer",
                ["go", "test", "./...", "-count=1", "-timeout=120s"],
                ROOT / "consumer",
            ),
        ]
    raise EvidenceFailure(f"unknown case: {case}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--case",
        required=True,
        choices=(
            "managed-browser-page-tool-switch",
            "refresh-send-watch-failures",
            "credential-free-audio-tool-replay",
        ),
    )
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    require(0 < args.child_timeout <= 60.0, "child timeout must be in (0, 60]")
    require(0 < args.aggregate_timeout <= 300.0, "aggregate timeout must be in (0, 300]")
    started = time.monotonic()
    results: list[dict[str, object]] = []
    try:
        for label, argv, cwd in case_commands(args.case):
            remaining = args.aggregate_timeout - (time.monotonic() - started)
            require(remaining > 0, "aggregate timeout expired before next child")
            results.append(run_child(label, argv, cwd, min(args.child_timeout, remaining)))
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "case": args.case, "error": str(error)}, sort_keys=True))
        return 1
    classification = {
        "managed-browser-page-tool-switch": "hermetic WebMCP adapter regression; not a live browser/customer acceptance claim",
        "refresh-send-watch-failures": "focused runtime service failure and shutdown controls",
        "credential-free-audio-tool-replay": "offline audio replay controls plus independent public consumer; no Realtime or credentials",
    }[args.case]
    print(json.dumps({"status": "passed", "case": args.case, "classification": classification, "elapsed_seconds": round(time.monotonic() - started, 6), "results": results}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
