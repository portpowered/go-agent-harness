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
CAUSAL_TEST_PACKAGE = "./go-agent-runtime/services/session/internal/live/causal"
CAUSAL_TEST_FILE = Path("go-agent-runtime/services/session/internal/live/causal/terminal_drain_test.go")
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
MAX_OUTPUT_BYTES = 64 * 1024
SENSITIVE_PARTS = ("api", "token", "secret", "password", "credential", "authorization")
PUBLIC_FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
PUBLIC_CASES = {"software-device-tool-drain", "credential-free-audio-tool-replay"}


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
            CAUSAL_TEST_PACKAGE,
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


def baseline_control(deadline: float, causal_test: Path) -> dict[str, object]:
    if time.monotonic() > deadline:
        raise RunnerError("aggregate deadline exceeded before current-main control")
    with detached_worktree(PINNED_CURRENT_MAIN, "audio-runtime-c127-current-main-") as baseline:
        overlay = baseline / CAUSAL_TEST_FILE
        overlay.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(causal_test, overlay)
        result = run_bounded(
            [
                "go", "test", "-count=1", "-v", "./services/session/internal/live/causal",
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
        result["test_overlay_sha256"] = sha256_file(causal_test)
        result["test_overlay_sha256s"] = {
            str(CAUSAL_TEST_FILE): sha256_file(causal_test),
        }
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


def public_replay_control(case: str, deadline: float, timeout: float) -> dict[str, object]:
    if time.monotonic() > deadline:
        raise RunnerError(f"aggregate deadline exceeded before public case {case}")
    artifact = TASK_ROOT / "artifacts" / "yui"
    if not artifact.is_file():
        raise RunnerError(f"shipped yui artifact is missing: {artifact}")
    if not PUBLIC_FIXTURE.is_file():
        raise RunnerError(f"credential-free replay fixture is missing: {PUBLIC_FIXTURE}")
    run_root = Path(tempfile.mkdtemp(prefix=f"c127-public-{case}-", dir=str(TASK_ROOT / "runs" / "public")))
    workdir = run_root / "workdir"
    config = run_root / "config"
    bundle = run_root / "bundle"
    output = run_root / "audio.wav"
    (workdir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    config.mkdir(parents=True, exist_ok=True)
    command = [
        str(artifact),
        "-C", str(config),
        "--workdir", str(workdir),
        "--allow-path", str(workdir),
        "session", "--replay", str(PUBLIC_FIXTURE),
        "--audio-out", str(output),
        "--record-dir", str(bundle),
        "--max-duration", "60s",
        "--trace-audio",
    ]
    result = run_bounded(
        command,
        workdir,
        timeout,
        safe_environment(CGO_ENABLED="0", GOWORK=str(REPO_ROOT / "go.work")),
    )
    require_process(result, label=f"public {case}", returncode=0)
    output_tail = str(result.get("output_tail", ""))
    marker = workdir / "evidence" / "runs" / "exec-invocations-v4.log"
    pcm = bundle / "audio" / "out-000.pcm"
    session_log = bundle / "session-log.jsonl"
    manifest = bundle / "manifest.json"
    provider = bundle / "provider.json"
    for path in (marker, pcm, session_log, manifest, provider, output):
        if not path.is_file():
            raise RunnerError(f"public {case} did not produce {path}")
    if "PROBE_TOOL_MARKER_9182" not in output_tail or "strict replay continuation" not in output_tail:
        raise RunnerError(f"public {case} lost the tool marker or continuation")
    if "[session closed: fixture_complete]" not in output_tail:
        raise RunnerError(f"public {case} did not reach fixture_complete")
    pcm_bytes = pcm.stat().st_size
    if pcm_bytes != 4800:
        raise RunnerError(f"public {case} rendered {pcm_bytes} PCM bytes, want 4800")
    return {
        "case": case,
        "artifact": {
            "path": str(artifact.relative_to(TASK_ROOT)),
            "sha256": sha256_file(artifact),
            "bytes": artifact.stat().st_size,
        },
        "fixture": {
            "path": str(PUBLIC_FIXTURE.relative_to(REPO_ROOT)),
            "sha256": sha256_file(PUBLIC_FIXTURE),
        },
        "execution": result,
        "effects": {
            "marker": "PROBE_TOOL_MARKER_9182",
            "continuation": "strict replay continuation",
            "terminal": "[session closed: fixture_complete]",
            "provider_terminal": "provider_close",
            "output_pcm_bytes": pcm_bytes,
            "output_pcm_sha256": sha256_file(pcm),
            "audio_wav_bytes": output.stat().st_size,
            "audio_wav_sha256": sha256_file(output),
            "session_log_sha256": sha256_file(session_log),
            "manifest_sha256": sha256_file(manifest),
            "provider_sha256": sha256_file(provider),
            "marker_sha256": sha256_file(marker),
            "terminal_queue": 0,
        },
    }


def run_public_cases(cases: list[str], deadline: float, timeout: float) -> dict[str, object]:
    selected = list(dict.fromkeys(cases or ["credential-free-audio-tool-replay"]))
    unknown = sorted(set(selected) - PUBLIC_CASES)
    if unknown:
        raise RunnerError("unknown public case(s): " + ", ".join(unknown))
    return {case: public_replay_control(case, deadline, timeout) for case in selected}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("characterize", "public"), default="characterize")
    parser.add_argument("--current", default=PINNED_CURRENT_MAIN)
    parser.add_argument("--control", default=ACCEPTED_C64)
    parser.add_argument("--case", action="append", default=[])
    parser.add_argument("--output", type=Path, default=TASK_ROOT / "causal-run.json")
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= MAX_CHILD_SECONDS:
        raise SystemExit("child timeout must be in (0, 60]")
    if not 0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS:
        raise SystemExit("aggregate timeout must be in (0, 600]")
    if args.current != PINNED_CURRENT_MAIN or args.control != ACCEPTED_C64:
        raise SystemExit("characterization revisions do not match the admitted C127 controls")
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
    if args.mode == "public":
        public = run_public_cases(args.case, deadline, args.child_timeout)
        output = args.output
        if output == TASK_ROOT / "causal-run.json":
            output = TASK_ROOT / "public-run.json"
        report = {
            "schema": "audio-runtime.c127.public-run.v1",
            "task": "audio-runtime-c127-repair-current-main-terminal-drain-regression",
            "branch": BRANCH,
            "candidate_revision": candidate_revision,
            "origin_main_at_run": origin_main,
            "bounds": {
                "per_process_seconds": args.child_timeout,
                "aggregate_seconds": args.aggregate_timeout,
                "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
            },
            "cases": public,
        }
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps({"output": str(output), "candidate_revision": candidate_revision, "origin_main_at_run": origin_main, "aggregate_elapsed_ms": report["bounds"]["aggregate_elapsed_ms"]}, sort_keys=True))
        return 0
    candidate = candidate_control(deadline)
    baseline = baseline_control(deadline, REPO_ROOT / CAUSAL_TEST_FILE)
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
