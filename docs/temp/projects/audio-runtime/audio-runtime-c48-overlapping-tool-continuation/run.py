#!/usr/bin/env python3
"""Bounded C48 public-runtime overlap probe."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=OWNED_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
FIXTURE = OWNED_ROOT / "fixtures.json"
CONSUMER_SOURCE = OWNED_ROOT / "consumer" / "main.go"
BIN = OWNED_ROOT / "bin" / "c48-consumer"
ARTIFACT_ROOT = OWNED_ROOT / "artifacts" / "runs"
PROVENANCE = OWNED_ROOT / "provenance.json"
MAX_OUTPUT_BYTES = 64 * 1024
MAX_REPORT_BYTES = 262144
MIN_FREE_BYTES = 2 * 1024**3
TIMEOUT_SECONDS = 10


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_capture(command: list[str], timeout: float = 30.0) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(command, cwd=REPO_ROOT, capture_output=True, text=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired as error:
        raise VerificationError(f"command exceeded {timeout:g}s: {' '.join(command)}") from error


def bounded_child(command: list[str], timeout: float) -> tuple[int, str]:
    process = subprocess.Popen(
        command,
        cwd=REPO_ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    try:
        output, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            output, _ = process.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            output, _ = process.communicate(timeout=2)
        return 124, output[-MAX_OUTPUT_BYTES:]
    return process.returncode, output[-MAX_OUTPUT_BYTES:]


def load_json(path: Path) -> dict:
    require(path.is_file(), f"missing JSON artifact: {path}")
    require(path.stat().st_size <= MAX_REPORT_BYTES, f"report exceeds {MAX_REPORT_BYTES} bytes: {path}")
    value = json.loads(path.read_text())
    require(isinstance(value, dict), f"JSON artifact is not an object: {path}")
    return value


def expected_calls() -> list[dict[str, str]]:
    return [
        {"id": "call-000-alpha", "name": "lookup_alpha", "arguments": '{"key":"alpha","turn":0}', "result": "result:lookup_alpha:000"},
        {"id": "call-000-beta", "name": "lookup_beta", "arguments": '{"key":"beta","turn":0}', "result": "result:lookup_beta:000"},
        {"id": "call-001-alpha", "name": "lookup_alpha", "arguments": '{"key":"alpha","turn":1}', "result": "result:lookup_alpha:001"},
        {"id": "call-001-beta", "name": "lookup_beta", "arguments": '{"key":"beta","turn":1}', "result": "result:lookup_beta:001"},
    ]


def validate_positive(report: dict) -> None:
    require(report.get("scenario") == "overlapping_tool_continuation", "positive scenario mismatch")
    require(report.get("clean_shutdown") is True, f"positive run was not clean: {report.get('error')}")
    require(report.get("trace_complete") is True, "positive public trace incomplete")
    overlap = report.get("overlap", {})
    require(overlap.get("both_responses_before_first_result") is True, "response overlap barrier was not proven")
    require(overlap.get("first_result_after_all_calls") is True, "first result preceded the four-call barrier")
    require(overlap.get("active_response_ids") == ["tool-resp-000", "tool-resp-001"], "active response IDs mismatch")
    require(overlap.get("cross_routing_verified") is True, "cross-routing was not verified")
    require(overlap.get("exactly_once_verified") is True, "exactly-once results were not verified")
    expected = expected_calls()
    require(report.get("tool_calls") == expected, f"tool calls mismatch: {report.get('tool_calls')}")
    expected_results = [{"id": item["id"], "name": item["name"], "arguments": "", "result": item["result"]} for item in expected]
    results = [dict(item) for item in report.get("tool_results", [])]
    require(results == expected_results, f"tool results mismatch: {results}")
    require(report.get("outbound_tool_result_ids") == [item["id"] for item in expected], "forwarded result order mismatch")
    require(report.get("responses") == ["tool-resp-000", "tool-resp-001", "final-resp-000", "final-resp-001"], "response identity mismatch")
    require(report.get("reverse_completion") == ["call-000-beta", "call-000-alpha", "call-001-beta", "call-001-alpha"], "reverse completion was not observed")
    require(report.get("terminal", {}).get("reason") == "provider_authored_completion", "terminal reason mismatch")
    require(report.get("terminal", {}).get("provenance") == "provider", "terminal provenance mismatch")
    require(report.get("events", {}).get("overflow_drops", 1) == 0, "public event overflow occurred")
    require(report.get("events", {}).get("count", 0) <= 4096, "public event trace exceeded bound")


def main() -> int:
    try:
        require(FIXTURE.is_file(), f"missing fixture: {FIXTURE}")
        require(CONSUMER_SOURCE.is_file(), f"missing consumer: {CONSUMER_SOURCE}")
        free_bytes = shutil.disk_usage(OWNED_ROOT).free
        require(free_bytes >= MIN_FREE_BYTES, f"free storage {free_bytes} is below required 2 GiB")
        source_revision = run_capture(["git", "rev-parse", "HEAD"], timeout=5).stdout.strip()
        branch = run_capture(["git", "branch", "--show-current"], timeout=5).stdout.strip()
        status = run_capture(["git", "status", "--porcelain", "--untracked-files=all"], timeout=5).stdout.strip()
        require(not status, f"source tree must be clean before evidence binding: {status.splitlines()[0] if status else ''}")
        fixture_sha = sha256_file(FIXTURE)
        build = run_capture(["go", "build", "-trimpath", "-o", str(BIN), str(CONSUMER_SOURCE)], timeout=60)
        require(build.returncode == 0, f"consumer build failed: {(build.stderr or build.stdout)[-4000:]}")
        require(BIN.is_file(), "consumer build did not produce a binary")
        BIN.chmod(0o755)
        binary_sha = sha256_file(BIN)
        run_stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + f"-{time.time_ns() % 1_000_000_000:09d}"
        run_root = ARTIFACT_ROOT / run_stamp
        run_root.mkdir(parents=True, exist_ok=True)
        provenance = {
            "schema": "audio-runtime-c48.provenance.v1",
            "source_revision": source_revision,
            "branch": branch,
            "fixture_sha256": fixture_sha,
            "consumer_source": str(CONSUMER_SOURCE.relative_to(REPO_ROOT)),
            "consumer_binary_sha256": binary_sha,
            "free_storage_bytes": free_bytes,
            "limits": {"min_free_storage_bytes": MIN_FREE_BYTES, "max_child_output_bytes": MAX_OUTPUT_BYTES, "timeout_seconds": TIMEOUT_SECONDS},
        }
        positive_root = run_root / "positive"
        positive_root.mkdir()
        positive_report = positive_root / "report.json"
        positive_command = [str(BIN), "-fixture", str(FIXTURE), "-output", str(positive_report), "-artifact-root", str(positive_root), "-source-revision", source_revision]
        positive_rc, positive_output = bounded_child(positive_command, TIMEOUT_SECONDS)
        require(positive_rc == 0, f"positive probe failed ({positive_rc}): {positive_output}")
        validate_positive(load_json(positive_report))
        controls: dict[str, dict] = {}
        for control in ("missing", "duplicate", "swapped"):
            control_root = run_root / control
            control_root.mkdir()
            control_report = control_root / "report.json"
            command = [str(BIN), "-fixture", str(FIXTURE), "-output", str(control_report), "-artifact-root", str(control_root), "-control", control, "-source-revision", source_revision]
            rc, output = bounded_child(command, TIMEOUT_SECONDS)
            require(rc != 0, f"negative control {control} unexpectedly passed")
            value = load_json(control_report)
            require(value.get("error"), f"negative control {control} did not record a causal error")
            controls[control] = {"exit_code": rc, "error": value["error"], "report": str(control_report.relative_to(REPO_ROOT)), "output_tail": output[-2000:]}
        provenance["positive"] = {"exit_code": positive_rc, "report": str(positive_report.relative_to(REPO_ROOT))}
        provenance["controls"] = controls
        PROVENANCE.write_text(json.dumps(provenance, indent=2, sort_keys=True) + "\n")
        print(json.dumps({"decision": "PASS", "source_revision": source_revision, "positive_report": str(positive_report.relative_to(REPO_ROOT)), "controls": controls}, sort_keys=True))
        return 0
    except (OSError, subprocess.SubprocessError, VerificationError, json.JSONDecodeError) as error:
        print(json.dumps({"decision": "FAIL", "error": str(error)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
