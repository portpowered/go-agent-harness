#!/usr/bin/env python3
"""Run bounded credential-free C136 public audio/tool regressions."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
MAX_OUTPUT_BYTES = 1 << 20

CASES: dict[str, tuple[Path, list[str]]] = {
    "credential-free-audio-tool": (
        ROOT / "agent-cli",
        [
            "go",
            "test",
            "./internal/services/internal/agentruntime",
            "-run",
            "TestSessionCommandAudioRoundtripRecordsNonSilentReply|TestSessionCommandAudioRoundtripSilentInputFailsAssertions|TestSessionCommandAudioRoundtripTruncatedInputFailsReplay",
            "-count=1",
            "-timeout=120s",
        ],
    ),
    "existing-interruption-tool-regression": (
        ROOT / "agent-cli",
        [
            "go",
            "test",
            "./test/integration",
            "-run",
            "Test(SessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI|ReadImageSpokenFailedContinuationIsActionable|SessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle)$",
            "-count=1",
            "-timeout=120s",
        ],
    ),
}


class RunError(RuntimeError):
    pass


def clean_environment() -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    environment = {
        key: value
        for key, value in os.environ.items()
        if not any(part in key.lower() for part in blocked)
    }
    environment.update(
        {
            "GOWORK": "off",
            "GOCACHE": f"/tmp/audio-runtime-c136-run-{os.getpid()}",
        }
    )
    return environment


def stop_process_group(process: subprocess.Popen[str]) -> dict[str, bool]:
    term_sent = False
    kill_sent = False
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGTERM)
            term_sent = True
        except ProcessLookupError:
            return {"term_sent": False, "kill_sent": False}
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
            process.wait(timeout=2.0)
    return {"term_sent": term_sent, "kill_sent": kill_sent}


def process_group_exists(process_group_id: int) -> bool:
    try:
        os.killpg(process_group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    except OSError:
        return True
    return True


def run_bounded(
    child: list[str], cwd: Path, environment: dict[str, str], timeout: float
) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        ["rtk", *child],
        cwd=cwd,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=os.name == "posix",
    )
    timed_out = False
    cleanup = {"term_sent": False, "kill_sent": False}
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        stdout = (error.stdout or "")[-MAX_OUTPUT_BYTES:]
        stderr = (error.stderr or "")[-MAX_OUTPUT_BYTES:]
        cleanup = stop_process_group(process)
        tail_stdout, tail_stderr = process.communicate()
        stdout += tail_stdout[-MAX_OUTPUT_BYTES:]
        stderr += tail_stderr[-MAX_OUTPUT_BYTES:]
    reaped = process.poll() is not None
    if not reaped:
        cleanup.update(stop_process_group(process))
        process.wait(timeout=2.0)
        reaped = True
    survivors = process_group_exists(process.pid) if os.name == "posix" else False
    stdout = stdout[-MAX_OUTPUT_BYTES:]
    stderr = stderr[-MAX_OUTPUT_BYTES:]
    return {
        "argv": child,
        "cwd": str(cwd.relative_to(ROOT)),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": round((time.monotonic() - started) * 1000),
        "output_bounded": len(stdout) <= MAX_OUTPUT_BYTES and len(stderr) <= MAX_OUTPUT_BYTES,
        "stdout": stdout,
        "stderr": stderr,
        "cleanup": {
            **cleanup,
            "reaped": reaped,
            "survivors": survivors,
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=sorted(CASES), required=True)
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= 60:
        raise RunError("child timeout must be between 0 and 60 seconds")
    if not 0 < args.aggregate_timeout <= 300:
        raise RunError("aggregate timeout must be between 0 and 300 seconds")
    deadline = time.monotonic() + args.aggregate_timeout
    environment = clean_environment()
    cases: dict[str, object] = {}
    for name in args.case:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RunError(f"aggregate timeout expired before {name}")
        cwd, child = CASES[name]
        result = run_bounded(child, cwd, environment, min(args.child_timeout, remaining))
        cases[name] = result
        if (
            result["returncode"] != 0
            or result["timed_out"]
            or not result["output_bounded"]
            or result["cleanup"]["survivors"]
        ):
            raise RunError(f"case failed: {name}: {json.dumps(result, sort_keys=True)}")
    report = {
        "schema": "audio-runtime.c136.public-regressions/v1",
        "passed": True,
        "cases": cases,
        "bounds": {
            "child_timeout_seconds": args.child_timeout,
            "aggregate_timeout_seconds": args.aggregate_timeout,
            "max_output_bytes": MAX_OUTPUT_BYTES,
        },
        "credential_environment_scrubbed": True,
        "candidate_revision": subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
            timeout=20,
        ).stdout.strip(),
    }
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RunError, OSError, subprocess.SubprocessError) as error:
        print(f"run failure: {error}", file=sys.stderr)
        raise SystemExit(1)
