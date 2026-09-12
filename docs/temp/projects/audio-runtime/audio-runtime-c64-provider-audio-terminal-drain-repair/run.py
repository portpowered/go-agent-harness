#!/usr/bin/env python3
"""Run the deterministic C64 terminal-drain candidate and source controls."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
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
CURRENT_MAIN_REVISION = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
EXPECTED_BRANCH = "codex/audio-runtime-c64-provider-audio-terminal-drain-repair"
TEST_PACKAGE = "./agent-cli/internal/services/internal/agentruntime"
TEST_NAME = "TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio"
TEST_RELATIVE = Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go")
TEST_FIXTURE_RELATIVES = (TEST_RELATIVE,)
PRODUCTION_RELATIVE = Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go")
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 180.0
MAX_OUTPUT_BYTES = 64 * 1024
COMPACT_OUTPUT_BYTES = 12 * 1024


class RunnerError(RuntimeError):
    pass


def git_value(*args: str, cwd: Path = REPO_ROOT) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, check=True, capture_output=True, text=True
    ).stdout.strip()


def git_status(cwd: Path = REPO_ROOT) -> str:
    return subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=all"],
        cwd=cwd,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.rstrip("\n")


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_revision_file(revision: str, relative: Path) -> str:
    result = subprocess.run(
        ["git", "show", f"{revision}:{relative.as_posix()}"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    )
    return sha256_bytes(result.stdout)


def sha256_candidate_diff(revision: str) -> str:
    result = subprocess.run(
        ["git", "diff", "--binary", f"{CURRENT_MAIN_REVISION}...{revision}"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    )
    return sha256_bytes(result.stdout)


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


def run_bounded(
    command: list[str],
    timeout: float,
    *,
    cwd: Path = REPO_ROOT,
    env_overrides: dict[str, str] | None = None,
) -> dict[str, object]:
    started = time.monotonic()
    environment = {**os.environ, "CGO_ENABLED": "0"}
    if env_overrides:
        environment.update(env_overrides)
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=environment,
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
        "cwd": str(cwd),
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


def compact_result(result: dict[str, object]) -> dict[str, object]:
    compact = dict(result)
    output = str(compact.pop("output", ""))
    compact["output_tail"] = output[-COMPACT_OUTPUT_BYTES:]
    return compact


def parse_marker(output: str, marker: str) -> dict[str, object]:
    for line in output.splitlines():
        if marker not in line:
            continue
        fields: dict[str, object] = {}
        for token in line.split(marker, 1)[1].split():
            key, separator, value = token.partition("=")
            if not separator:
                continue
            fields[key] = int(value) if value.isdigit() else value
        return fields
    return {}


def require_process(
    result: dict[str, object], *, label: str, returncode: int
) -> None:
    if result.get("returncode") != returncode:
        raise RunnerError(json.dumps({"label": label, "result": compact_result(result)}, sort_keys=True))
    if result.get("timed_out") or not result.get("output_bounded"):
        raise RunnerError(json.dumps({"label": label, "result": compact_result(result)}, sort_keys=True))
    cleanup = result.get("cleanup")
    if (
        not isinstance(cleanup, dict)
        or not cleanup.get("parent_reaped")
        or not cleanup.get("reader_thread_joined")
        or cleanup.get("group_alive_after")
    ):
        raise RunnerError(json.dumps({"label": label, "result": compact_result(result)}, sort_keys=True))


def ensure_aggregate(deadline: float, label: str) -> None:
    if time.monotonic() > deadline:
        raise RunnerError(f"aggregate timeout exceeded during {label}")


def build_test(
    *,
    cwd: Path,
    artifact: Path,
    timeout: float,
    deadline: float,
    label: str,
) -> dict[str, object]:
    ensure_aggregate(deadline, f"{label} build")
    command = [
        "go",
        "test",
        "-tags=nomicrophone",
        "-c",
        "-o",
        str(artifact),
        TEST_PACKAGE,
    ]
    result = run_bounded(command, timeout, cwd=cwd)
    require_process(result, label=f"{label} build", returncode=0)
    if not artifact.is_file():
        raise RunnerError(f"{label} build did not produce {artifact}")
    ensure_aggregate(deadline, f"{label} build completion")
    return result


def run_test_binary(
    *,
    artifact: Path,
    cwd: Path,
    timeout: float,
    deadline: float,
    label: str,
    env_overrides: dict[str, str] | None = None,
) -> dict[str, object]:
    ensure_aggregate(deadline, f"{label} execution")
    command = [
        str(artifact),
        "-test.run",
        f"^{TEST_NAME}$",
        "-test.v",
        "-test.count=1",
        "-test.timeout=60s",
    ]
    result = run_bounded(command, timeout, cwd=cwd, env_overrides=env_overrides)
    ensure_aggregate(deadline, f"{label} execution completion")
    return result


def create_baseline_worktree(temp_root: Path) -> Path:
    baseline_root = temp_root / "baseline"
    subprocess.run(
        ["git", "worktree", "add", "--quiet", "--detach", str(baseline_root), BASE_REVISION],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    for relative in TEST_FIXTURE_RELATIVES:
        baseline_test = baseline_root / relative
        candidate_test = REPO_ROOT / relative
        baseline_test.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(candidate_test, baseline_test)
    return baseline_root


def remove_baseline_worktree(baseline_root: Path) -> bool:
    removed = subprocess.run(
        ["git", "worktree", "remove", "--force", str(baseline_root)],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    ).returncode == 0
    return removed


def candidate_evidence(
    *,
    candidate_hashes: dict[str, object],
    artifact: Path,
    build: dict[str, object],
    run: dict[str, object],
) -> dict[str, object]:
    require_process(run, label="repaired candidate", returncode=0)
    sequence = parse_marker(str(run["output"]), "C64_SEQUENCE_EVIDENCE ")
    render = parse_marker(str(run["output"]), "C64_RENDER_EVIDENCE ")
    expected_sequence = {
        "provider_events": 17,
        "tool_calls": 2,
        "continuation": "two-tool",
        "final_response": "terminal-drain-final-response",
        "order": "tool-turn-before-final-audio",
    }
    expected_render = {
        "provider_samples": 9600,
        "admitted_samples": 6400,
        "consumed_samples": 6400,
        "rendered_samples": 6720,
        "queued_samples": 0,
        "underflow_samples": 320,
        "callback_count": 14,
        "shutdown": "complete",
    }
    if any(sequence.get(key) != value for key, value in expected_sequence.items()):
        raise RunnerError(json.dumps({"label": "sequence evidence", "evidence": sequence}, sort_keys=True))
    if any(render.get(key) != value for key, value in expected_render.items()):
        raise RunnerError(json.dumps({"label": "render evidence", "evidence": render}, sort_keys=True))
    return {
        "expected_outcome": "repaired-pass",
        "source_hashes": candidate_hashes,
        "artifact_sha256": sha256_file(artifact),
        "sequence_evidence": sequence,
        "render_evidence": render,
        "build": compact_result(build),
        "result": compact_result(run),
    }


def source_failure_evidence(
    *,
    source_hashes: dict[str, object],
    artifact: Path,
    build: dict[str, object],
    run: dict[str, object],
) -> dict[str, object]:
    require_process(run, label="accepted source control", returncode=1)
    failure = parse_marker(str(run["output"]), "C64_ACCEPTED_SOURCE_FAILURE ")
    expected = {
        "provider_samples": 9600,
        "admitted_samples": 0,
        "consumed_samples": 0,
        "rendered_samples": 0,
        "queued_samples": 0,
        "underflow_samples": 0,
        "callback_count": 0,
        "shutdown": "provider-close",
    }
    if any(failure.get(key) != value for key, value in expected.items()):
        raise RunnerError(json.dumps({"label": "accepted source failure", "evidence": failure}, sort_keys=True))
    return {
        "expected_outcome": "accepted-source-failure",
        "source_hashes": source_hashes,
        "artifact_sha256": sha256_file(artifact),
        "failure_evidence": failure,
        "build": compact_result(build),
        "result": compact_result(run),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("terminal-drain-interleaving",))
    parser.add_argument("--expect", required=True, choices=("accepted-source-failure", "repaired-pass"))
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= MAX_CHILD_SECONDS:
        raise SystemExit("child timeout must be in (0, 60] seconds")
    if not 0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS:
        raise SystemExit("aggregate timeout must be in (0, 180] seconds")

    branch = git_value("branch", "--show-current")
    origin_main = git_value("rev-parse", "origin/main")
    candidate_revision = git_value("rev-parse", "HEAD")
    if branch != EXPECTED_BRANCH:
        raise SystemExit(f"wrong isolated branch: {branch}")
    if origin_main != CURRENT_MAIN_REVISION:
        raise SystemExit(f"origin/main is {origin_main}, expected {CURRENT_MAIN_REVISION}")
    if git_status():
        raise SystemExit("runner requires a clean committed candidate tree")
    if subprocess.run(
        ["git", "merge-base", "--is-ancestor", CURRENT_MAIN_REVISION, candidate_revision],
        cwd=REPO_ROOT,
    ).returncode != 0:
        raise SystemExit("current fetched main is not an ancestor of the candidate")

    candidate_production = REPO_ROOT / PRODUCTION_RELATIVE
    candidate_hashes: dict[str, object] = {
        "rtc_device_runtime.go": sha256_file(candidate_production),
        "test_fixtures": {
            relative.as_posix(): sha256_file(REPO_ROOT / relative)
            for relative in TEST_FIXTURE_RELATIVES
        },
    }
    aggregate_deadline = time.monotonic() + args.aggregate_timeout
    temp_root = Path(tempfile.mkdtemp(prefix="audio-runtime-c64-"))
    baseline_root: Path | None = None
    try:
        if args.expect == "repaired-pass":
            artifact = temp_root / "candidate.test"
            build = build_test(
                cwd=REPO_ROOT,
                artifact=artifact,
                timeout=args.child_timeout,
                deadline=aggregate_deadline,
                label="repaired candidate",
            )
            run = run_test_binary(
                artifact=artifact,
                cwd=REPO_ROOT,
                timeout=args.child_timeout,
                deadline=aggregate_deadline,
                label="repaired candidate",
            )
            candidate = candidate_evidence(
                candidate_hashes=candidate_hashes,
                artifact=artifact,
                build=build,
                run=run,
            )
            payload = {
                "schema": "audio-runtime.c64.interleaving-run.v2",
                "passed": True,
                "case": args.case,
                "expectation": args.expect,
                "branch": branch,
                "candidate_revision": candidate_revision,
                "source_revision": BASE_REVISION,
                "current_main_revision": origin_main,
                "startup_integration_revision": STARTUP_INTEGRATION_REVISION,
                "candidate_diff_sha256": sha256_candidate_diff(candidate_revision),
                "fixture_revision": candidate_revision,
                "fixture_test_hashes": candidate_hashes["test_fixtures"],
                "candidate": candidate,
            }
        else:
            baseline_root = create_baseline_worktree(temp_root)
            baseline_hashes: dict[str, object] = {
                "rtc_device_runtime.go": sha256_revision_file(BASE_REVISION, PRODUCTION_RELATIVE),
                "test_fixtures": {
                    relative.as_posix(): sha256_file(baseline_root / relative)
                    for relative in TEST_FIXTURE_RELATIVES
                },
            }
            artifact = temp_root / "accepted-source.test"
            build = build_test(
                cwd=baseline_root,
                artifact=artifact,
                timeout=args.child_timeout,
                deadline=aggregate_deadline,
                label="accepted source",
            )
            run = run_test_binary(
                artifact=artifact,
                cwd=baseline_root,
                timeout=args.child_timeout,
                deadline=aggregate_deadline,
                label="accepted source",
                env_overrides={"C64_CANCEL_ON_TERMINAL": "1"},
            )
            source = source_failure_evidence(
                source_hashes=baseline_hashes,
                artifact=artifact,
                build=build,
                run=run,
            )
            payload = {
                "schema": "audio-runtime.c64.interleaving-run.v2",
                "passed": True,
                "case": args.case,
                "expectation": args.expect,
                "branch": branch,
                "candidate_revision": candidate_revision,
                "source_revision": BASE_REVISION,
                "current_main_revision": origin_main,
                "startup_integration_revision": STARTUP_INTEGRATION_REVISION,
                "candidate_diff_sha256": sha256_candidate_diff(candidate_revision),
                "fixture_revision": candidate_revision,
                "fixture_test_hashes": candidate_hashes["test_fixtures"],
                "accepted_source_control": source,
            }
        ensure_aggregate(aggregate_deadline, "evidence assembly")
        print(json.dumps(payload, sort_keys=True))
        return 0
    finally:
        if baseline_root is not None:
            remove_baseline_worktree(baseline_root)
        shutil.rmtree(temp_root, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, RunnerError) as error:
        raise SystemExit(f"C64 runner failed: {error}") from error
