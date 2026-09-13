#!/usr/bin/env python3
"""Run C127's bounded ordered current-main and accepted-C64 controls."""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import tempfile
import time


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(
    ["git", "rev-parse", "--show-toplevel"],
    cwd=TASK_ROOT,
    check=True,
    capture_output=True,
    text=True,
).stdout.strip())
PINNED_CURRENT_MAIN = "09c70f51243caeaf1184c4806b99bbf7749e3044"
ACCEPTED_C64 = "59af6325614d80173447fe2018a0471e27b4e7b1"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BRANCH = "codex/audio-runtime-c127-repair-current-main-terminal-drain-regression"
LIVE_TEST = "TestTerminalDrainOrderedBoundaryTrace"
C64_TEST = "TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio"
LIVE_TEST_FILE = Path("go-agent-runtime/services/session/internal/live/service_test.go")
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
MAX_OUTPUT_BYTES = 64 * 1024
SENSITIVE_PARTS = ("api", "token", "secret", "password", "credential", "authorization")


class RunnerError(RuntimeError):
    pass


def git_value(*args: str, cwd: Path = REPO_ROOT) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, check=True, capture_output=True, text=True
    ).stdout.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_environment(**overrides: str) -> dict[str, str]:
    environment = {
        key: value for key, value in os.environ.items()
        if not any(part in key.lower() for part in SENSITIVE_PARTS)
    }
    environment.update(overrides)
    return environment


def process_group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
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


def run_bounded(command: list[str], cwd: Path, timeout: float, env: dict[str, str]) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    timed_out = False
    output = b""
    try:
        output, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        output = (error.output or b"") if isinstance(error.output, bytes) else (error.output or b"").encode()
        terminate_group(process)
        remainder, _ = process.communicate()
        output += remainder
    output_bounded = len(output) <= MAX_OUTPUT_BYTES
    if not output_bounded:
        output = output[-MAX_OUTPUT_BYTES:]
    return {
        "command": command,
        "cwd": str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "output_bounded": output_bounded,
        "output_tail": output.decode("utf-8", errors="replace"),
        "cleanup": {
            "parent_reaped": process.poll() is not None,
            "group_alive_after": process_group_alive(process.pid),
        },
    }


@contextmanager
def detached_worktree(revision: str, prefix: str):
    parent = Path(tempfile.mkdtemp(prefix=prefix))
    worktree = parent / "source"
    subprocess.run(
        ["git", "worktree", "add", "--quiet", "--detach", str(worktree), revision],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    try:
        yield worktree
    finally:
        subprocess.run(
            ["git", "worktree", "remove", "--force", str(worktree)],
            cwd=REPO_ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
        shutil.rmtree(parent, ignore_errors=True)


def require_process(result: dict[str, object], *, label: str, returncode: int) -> None:
    if result.get("returncode") != returncode:
        raise RunnerError(f"{label} returned {result.get('returncode')}: {result.get('output_tail', '')[-2000:]}")
    if result.get("timed_out") or not result.get("output_bounded"):
        raise RunnerError(f"{label} exceeded its bounded execution contract")
    cleanup = result.get("cleanup")
    if not isinstance(cleanup, dict) or cleanup.get("parent_reaped") is not True or cleanup.get("group_alive_after"):
        raise RunnerError(f"{label} left a process-group survivor: {cleanup}")


def parse_fields(output: str, marker: str) -> dict[str, object]:
    for line in output.splitlines():
        if marker not in line:
            continue
        fields: dict[str, object] = {}
        for token in line.split(marker, 1)[1].split():
            key, separator, value = token.partition("=")
            if separator:
                fields[key] = int(value) if re.fullmatch(r"-?\d+", value) else value
        return fields
    return {}


def parse_trace(output: str) -> list[dict[str, object]]:
    match = re.search(r"C127_ORDERED_BOUNDARY_TRACE events=(\[.*\])", output)
    if not match:
        return []
    value = json.loads(match.group(1))
    if not isinstance(value, list):
        return []
    return value


def candidate_control(deadline: float) -> dict[str, object]:
    if time.monotonic() > deadline:
        raise RunnerError("aggregate deadline exceeded before candidate control")
    result = run_bounded(
        [
            "go", "test", "-count=1", "-v",
            "./go-agent-runtime/services/session/internal/live",
            "-run", f"^{LIVE_TEST}$", "-timeout=30s",
        ],
        REPO_ROOT,
        MAX_CHILD_SECONDS,
        safe_environment(CGO_ENABLED="0", GOWORK=str(REPO_ROOT / "go.work")),
    )
    require_process(result, label="candidate ordered boundary control", returncode=0)
    trace = parse_trace(str(result["output_tail"]))
    if len(trace) != 6:
        raise RunnerError(f"candidate ordered trace has {len(trace)} events")
    result["trace"] = trace
    return result


def baseline_control(deadline: float, candidate_test: Path) -> dict[str, object]:
    if time.monotonic() > deadline:
        raise RunnerError("aggregate deadline exceeded before current-main control")
    with detached_worktree(PINNED_CURRENT_MAIN, "audio-runtime-c127-current-main-") as baseline:
        overlay = baseline / LIVE_TEST_FILE
        shutil.copy2(candidate_test, overlay)
        result = run_bounded(
            [
                "go", "test", "-count=1", "-v", "./services/session/internal/live",
                "-run", f"^{LIVE_TEST}$", "-timeout=30s",
            ],
            baseline / "go-agent-runtime",
            MAX_CHILD_SECONDS,
            safe_environment(CGO_ENABLED="0", GOWORK="off"),
        )
        require_process(result, label="unmodified current-main negative control", returncode=1)
        output = str(result["output_tail"])
        if "provider media admitted during Receive: context deadline exceeded" not in output:
            raise RunnerError("current-main negative control did not prove the preclaim failure")
        result["source_revision"] = PINNED_CURRENT_MAIN
        result["test_overlay_sha256"] = sha256_file(candidate_test)
        result["failure_marker"] = "provider media admitted during Receive: context deadline exceeded"
        return result


def c64_control(deadline: float) -> dict[str, object]:
    if time.monotonic() > deadline:
        raise RunnerError("aggregate deadline exceeded before accepted C64 control")
    with detached_worktree(ACCEPTED_C64, "audio-runtime-c127-c64-control-") as control:
        result = run_bounded(
            [
                "go", "test", "-count=1", "-v",
                "./internal/services/internal/agentruntime",
                "-run", f"^{C64_TEST}$", "-timeout=60s",
            ],
            control / "agent-cli",
            MAX_CHILD_SECONDS,
            safe_environment(CGO_ENABLED="0", GOWORK="off"),
        )
        require_process(result, label="accepted C64 control", returncode=0)
        sequence = parse_fields(str(result["output_tail"]), "C64_SEQUENCE_EVIDENCE ")
        render = parse_fields(str(result["output_tail"]), "C64_RENDER_EVIDENCE ")
        expected_sequence = {"provider_events": 17, "tool_calls": 2, "continuation": "two-tool", "final_response": "terminal-drain-final-response", "order": "tool-turn-before-final-audio"}
        expected_render = {"provider_samples": 9600, "admitted_samples": 6400, "consumed_samples": 6400, "rendered_samples": 6720, "queued_samples": 0, "underflow_samples": 320, "callback_count": 14, "shutdown": "complete"}
        if sequence != expected_sequence or render != expected_render:
            raise RunnerError(f"accepted C64 control markers changed: {sequence} / {render}")
        result["source_revision"] = ACCEPTED_C64
        result["sequence"] = sequence
        result["render"] = render
        return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=TASK_ROOT / "causal-run.json")
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= MAX_CHILD_SECONDS:
        raise SystemExit("child timeout must be in (0, 60]")
    if not 0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS:
        raise SystemExit("aggregate timeout must be in (0, 600]")
    if git_value("branch", "--show-current") != BRANCH:
        raise SystemExit("wrong isolated branch")
    if git_value("status", "--porcelain", "--untracked-files=all"):
        raise SystemExit("causal runner requires a clean committed candidate before writing its report")
    origin_main = git_value("rev-parse", "origin/main")
    candidate_revision = git_value("rev-parse", "HEAD")
    for ancestor in (PINNED_CURRENT_MAIN, ACCEPTED_C64, STARTUP_INTEGRATION):
        subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, candidate_revision], cwd=REPO_ROOT, check=True)
    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    candidate = candidate_control(deadline)
    baseline = baseline_control(deadline, REPO_ROOT / LIVE_TEST_FILE)
    c64 = c64_control(deadline)
    report = {
        "schema": "audio-runtime.c127.ordered-causal-run.v1",
        "task": "audio-runtime-c127-repair-current-main-terminal-drain-regression",
        "branch": BRANCH,
        "candidate_revision": candidate_revision,
        "pinned_current_main": PINNED_CURRENT_MAIN,
        "origin_main_at_run": origin_main,
        "accepted_c64": ACCEPTED_C64,
        "startup_integration": STARTUP_INTEGRATION,
        "bounds": {
            "per_process_seconds": args.child_timeout,
            "aggregate_seconds": args.aggregate_timeout,
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
        },
        "input": {
            "provider_sample_range": [0, 6400],
            "schedule": "same six-boundary fixture; no sleeps or retry-until-reproduction",
            "timing_domain": "monotonic-process",
        },
        "controls": {
            "candidate_repaired": candidate,
            "current_main_unmodified": baseline,
            "accepted_c64_healthy": c64,
        },
        "first_divergence": {
            "boundary": "rtc_forwarding",
            "candidate": "sequence 1 claims provider-owned RTC media before sequence 2 Receive",
            "current_main": "sequence 1 Receive occurs without a claim and the provider media is released",
            "accepted_c64": "provider/tool sequence and provider-to-render accounting remain exact with queue zero",
            "later_layer_rejected": [
                "response terminal: candidate forwards the terminal after the media boundary",
                "sink admission: accepted C64 control admits 6400 samples with zero drops/overflow/discards",
                "device rendering: accepted C64 control reconciles 6400 consumed and 6720 rendered with only the recorded underflow tail",
                "graceful drain: accepted C64 control reports shutdown=complete and queued_samples=0",
            ],
        },
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "candidate_revision": candidate_revision, "origin_main_at_run": origin_main, "aggregate_elapsed_ms": report["bounds"]["aggregate_elapsed_ms"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, RunnerError, json.JSONDecodeError) as error:
        raise SystemExit(f"C127 causal runner failed: {error}") from error
