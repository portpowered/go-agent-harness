#!/usr/bin/env python3
"""Verify C64 provenance, causal ordering, scope, and delivery evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


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
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c64-provider-audio-terminal-drain-repair/"
ALLOWED_PATHS = {
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go",
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_terminal_drain_test.go",
}
TEST_FIXTURE_RELATIVES = (
    Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go"),
    Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_terminal_drain_test.go"),
)
PRODUCTION_RELATIVE = Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go")
MAX_OUTPUT_BYTES = 64 * 1024


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git_value(*args: str) -> str:
    return subprocess.run(
        ["git", *args], cwd=REPO_ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()


def git_status() -> str:
    return subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=all"],
        cwd=REPO_ROOT,
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


def load_json(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing evidence: {path}")
    require(path.stat().st_size <= MAX_OUTPUT_BYTES, f"evidence exceeds {MAX_OUTPUT_BYTES} bytes: {path}")
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"evidence is not an object: {path}")
    return value


def require_hex(value: object, label: str) -> str:
    require(isinstance(value, str) and len(value) == 64, f"{label} is not a SHA256 digest")
    try:
        int(value, 16)
    except ValueError as error:
        raise VerificationError(f"{label} is not hexadecimal") from error
    return value


def verify_process(record: object, *, label: str, returncode: int, marker: str) -> dict:
    require(isinstance(record, dict), f"missing {label} execution record")
    require(record.get("returncode") == returncode, f"{label} return code changed")
    require(record.get("timed_out") is False, f"{label} timed out")
    require(record.get("output_bounded") is True, f"{label} output was not bounded")
    cleanup = record.get("cleanup")
    require(isinstance(cleanup, dict), f"{label} cleanup record missing")
    require(cleanup.get("parent_reaped") is True, f"{label} parent was not reaped")
    require(cleanup.get("reader_thread_joined") is True, f"{label} output reader did not join")
    require(cleanup.get("group_alive_after") is False, f"{label} process group survived")
    output_tail = record.get("output_tail")
    require(isinstance(output_tail, str) and marker in output_tail, f"{label} marker is missing from bounded output")
    return record


def verify_build(record: object, *, label: str) -> dict:
    build = verify_process(record, label=f"{label} build", returncode=0, marker="")
    command = build.get("command")
    require(isinstance(command, list), f"{label} build command missing")
    require("go" in command and "test" in command and "-c" in command, f"{label} was not compiled as a test artifact")
    require("-tags=nomicrophone" in command, f"{label} build was not hermetic")
    return build


def verify_fixture_hashes(record: dict, expected: dict[str, str], *, label: str) -> None:
    source_hashes = record.get("source_hashes")
    require(isinstance(source_hashes, dict), f"{label} source hashes missing")
    require(source_hashes.get("rtc_device_runtime.go") == expected["rtc_device_runtime.go"], f"{label} production hash changed")
    fixture_hashes = source_hashes.get("test_fixtures")
    require(fixture_hashes == expected["test_fixtures"], f"{label} fixture hashes changed")
    require_hex(record.get("artifact_sha256"), f"{label} artifact hash")


def verify_identity(evidence: dict) -> None:
    prd = load_json(REPO_ROOT / "prd.json")
    require(prd.get("branchName") == EXPECTED_BRANCH, "prd.json branchName does not match the isolated branch")
    require(git_value("branch", "--show-current") == EXPECTED_BRANCH, "candidate is on the wrong branch")
    require(git_value("rev-parse", "origin/main") == CURRENT_MAIN_REVISION, "origin/main is not the freshly fetched current main")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", STARTUP_INTEGRATION_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "startup integration ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", BASE_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "accepted main ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", CURRENT_MAIN_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "current fetched main is not an ancestor of the candidate")
    candidate_revision = evidence.get("candidate_revision")
    require(isinstance(candidate_revision, str), "candidate revision is missing from evidence")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", candidate_revision, "HEAD"], cwd=REPO_ROOT).returncode == 0, "evidence fixture revision is not an ancestor of the candidate")
    require(evidence.get("source_revision") == BASE_REVISION, "evidence source revision changed")
    require(evidence.get("current_main_revision") == CURRENT_MAIN_REVISION, "evidence current main changed")
    require(evidence.get("startup_integration_revision") == STARTUP_INTEGRATION_REVISION, "evidence startup ancestry changed")
    require(evidence.get("fixture_revision") == candidate_revision, "fixture revision is not tied to candidate evidence")
    require_hex(evidence.get("candidate_diff_sha256"), "candidate diff hash")


def verify_negative() -> None:
    evidence = load_json(TASK_ROOT / "negative-evidence.json")
    require(evidence.get("schema") == "audio-runtime.c64.negative-evidence.v2", "negative evidence schema changed")
    verify_identity(evidence)
    historical = evidence.get("historical_negative", {})
    require(historical.get("ci_run") == "34638493032", "historical CI run was relabeled")
    trials = historical.get("trials", [])
    require(len(trials) == 2 and {item.get("trial") for item in trials} == {"06", "12"}, "historical trial set changed")
    for trial in trials:
        require(trial.get("compared_samples") == 174391, "historical compared sample count changed")
        require(trial.get("actual_samples") == 167991, "historical actual sample count changed")
        require(trial.get("missing_samples") == 6400, "historical missing sample count changed")
        require(all(trial.get(key) == 0 for key in ("dropped_samples", "overflow_events", "discarded_samples", "discard_events")), "historical loss accounting changed")
    inherited = evidence.get("inherited_occurrences", [])
    require({item.get("ci_run") for item in inherited} == {"34624302582", "34631305649"}, "inherited negative occurrences were not retained")
    require(all(item.get("compared_samples") == 174391 and item.get("actual_samples") == 167991 and item.get("missing_samples") == 6400 and item.get("drop_overflow_discard_accounting") == "all zero" for item in inherited), "inherited negative accounting changed")
    first = evidence.get("accepted_source_first_failure", {})
    require(first.get("returncode") == 1 and first.get("interleaving") == "provider-close-before-drain", "first accepted-source negative was not retained")
    require(first.get("provider_samples") == 9600 and first.get("admitted_device_samples") == 0 and first.get("expected_device_samples") == 6400 and first.get("missing_samples") == 6400, "first negative sample accounting changed")
    require(first.get("candidate_derived_expectation") is False, "negative expectation is candidate-derived")

    fixture_revision = evidence["fixture_revision"]
    fixture_hashes = {
        relative.as_posix(): sha256_revision_file(fixture_revision, relative)
        for relative in TEST_FIXTURE_RELATIVES
    }
    candidate_record = evidence.get("repaired_candidate")
    require(isinstance(candidate_record, dict), "repaired candidate evidence missing")
    require(candidate_record.get("expected_outcome") == "repaired-pass", "repaired candidate expectation changed")
    candidate_hashes = {
        "rtc_device_runtime.go": sha256_file(REPO_ROOT / PRODUCTION_RELATIVE),
        "test_fixtures": fixture_hashes,
    }
    verify_fixture_hashes(candidate_record, candidate_hashes, label="repaired candidate")
    verify_build(candidate_record.get("build"), label="repaired candidate")
    verify_process(candidate_record.get("result"), label="repaired candidate", returncode=0, marker="C64_RENDER_EVIDENCE")
    sequence = candidate_record.get("sequence_evidence")
    require(sequence == {"provider_events": 17, "tool_calls": 2, "continuation": "two-tool", "final_response": "terminal-drain-final-response", "order": "tool-turn-before-final-audio"}, "sequence evidence changed")
    render = candidate_record.get("render_evidence")
    require(render == {"provider_samples": 9600, "admitted_samples": 6400, "consumed_samples": 6400, "rendered_samples": 6720, "queued_samples": 0, "underflow_samples": 320, "callback_count": 14, "shutdown": "complete"}, "render evidence changed")
    candidate_test_hashes = candidate_record.get("source_hashes", {}).get("test_fixtures")
    require(candidate_test_hashes == fixture_hashes, "candidate fixture was not hash-bound to its revision")
    require_hex(evidence.get("candidate_diff_sha256"), "candidate diff hash")

    source_record = evidence.get("accepted_source_control")
    require(isinstance(source_record, dict), "accepted source control evidence missing")
    require(source_record.get("expected_outcome") == "accepted-source-failure", "accepted source expectation changed")
    source_hashes = {
        "rtc_device_runtime.go": sha256_revision_file(BASE_REVISION, PRODUCTION_RELATIVE),
        "test_fixtures": fixture_hashes,
    }
    verify_fixture_hashes(source_record, source_hashes, label="accepted source")
    verify_build(source_record.get("build"), label="accepted source")
    verify_process(source_record.get("result"), label="accepted source", returncode=1, marker="C64_ACCEPTED_SOURCE_FAILURE")
    failure = source_record.get("failure_evidence")
    require(failure == {"provider_samples": 9600, "admitted_samples": 0, "consumed_samples": 0, "rendered_samples": 0, "queued_samples": 0, "underflow_samples": 0, "callback_count": 0, "shutdown": "provider-close"}, "accepted source failure evidence changed")
    require(source_record.get("source_hashes", {}).get("test_fixtures") == fixture_hashes, "accepted source fixture was not copied from the candidate fixture")


def verify_causal_source() -> None:
    source = (REPO_ROOT / PRODUCTION_RELATIVE).read_text(encoding="utf-8")
    tests = "\n".join((REPO_ROOT / relative).read_text(encoding="utf-8") for relative in TEST_FIXTURE_RELATIVES)
    require("const rtcDevicePlaybackDrainTimeout = 5 * time.Second" in source, "finite drain bound is missing")
    drain_position = source.index("drainErr = s.DrainPlayback")
    close_position = source.index("sessionErr = s.Session.Close()")
    require(drain_position < close_position, "provider/session close still overtakes playback drain")
    require("return errors.Join(drainErr, sessionErr, s.binding.Close())" in source, "drain/provider/device errors are not joined")
    for marker in ("terminalObserved", "gracefulCloseRequested", "forwardReceive", "DrainPlayback", "WaitForPump", "PlaybackSamplesObserver"):
        require(marker in source, f"causal lifecycle marker missing: {marker}")
    for marker in ("TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio", "drainStarted", "closeStarted", "continuationRequested", "provider-close-before-drain", "drain-before-provider-close", "reflect.DeepEqual", "RenderedSamples()", "PlaybackStats()", "QueuedSamples", "UnderflowSamples", "C64_SEQUENCE_EVIDENCE", "C64_RENDER_EVIDENCE", "C64_ACCEPTED_SOURCE_FAILURE", "provider/admission/consumption/queue reconciliation", "DroppedSamples", "OverflowEvents", "DiscardedSamples", "ToolExecutor", "ToolDefinitions", "StreamObserver"):
        require(marker in tests, f"deterministic barrier oracle missing: {marker}")
    require("time.Sleep(" not in tests, "barrier control uses sleep-only scheduling")
    require(tests.count("terminalDrainProviderSamples = 9600") == 1 and tests.count("terminalDrainDeviceSamples   = 6400") == 1, "barrier sample counts are not exact")


def run_focused_transition() -> dict:
    command = [
        "go",
        "test",
        "-tags=nomicrophone",
        "-count=1",
        "-timeout=180s",
        "./agent-cli/internal/services/internal/agentruntime",
        "-run",
        "^(TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio|TestRTCDeviceBoundSessionAcceptedCancelDiscardsQueuedPlayback|TestRTCDeviceBoundSessionRejectedCancelDoesNotDiscardPlayback|TestRTCDeviceBoundSessionDropsInFlightPlaybackAcrossCancelAndResume|TestRunSessionRTCDeviceBindingStartsRuntimePumps|TestRunSessionRTCDeviceBindingPropagatesPumpError)$",
    ]
    result = subprocess.run(command, cwd=REPO_ROOT, env={**os.environ, "CGO_ENABLED": "0"}, capture_output=True, text=True, timeout=185)
    require(result.returncode == 0, f"focused transition tests failed: {(result.stdout + result.stderr)[-4000:]}")
    return {"command": command, "returncode": result.returncode, "output": (result.stdout + result.stderr)[-MAX_OUTPUT_BYTES:]}


def changed_paths() -> set[str]:
    paths = set(filter(None, git_value("diff", "--name-only", f"{CURRENT_MAIN_REVISION}...HEAD").splitlines()))
    paths.update(filter(None, git_value("diff", "--cached", "--name-only").splitlines()))
    for line in git_status().splitlines():
        if len(line) > 3:
            paths.add(line[3:])
    return paths


def verify_scope() -> None:
    for diff_args in (("diff", "--check"), ("diff", "--cached", "--check")):
        diff_check = subprocess.run(["git", *diff_args], cwd=REPO_ROOT, capture_output=True, text=True)
        require(diff_check.returncode == 0, f"git {' '.join(diff_args)} failed: {diff_check.stdout}{diff_check.stderr}")
    paths = changed_paths()
    require(paths, "candidate has no recorded implementation or evidence change")
    disallowed = sorted(path for path in paths if path not in ALLOWED_PATHS and not path.startswith(OWNED_PREFIX))
    require(not disallowed, f"out-of-scope changed paths: {disallowed}")
    for relative, budget in ((PRODUCTION_RELATIVE, 400), *[(path, 600) for path in TEST_FIXTURE_RELATIVES]):
        path = REPO_ROOT / relative
        require(sum(1 for _ in path.open(encoding="utf-8")) <= budget, f"{relative} exceeds its architecture line budget")


def verify_delivery() -> None:
    require(not git_status(), "delivery provenance requires a clean candidate tree")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", CURRENT_MAIN_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "fetched main is not an ancestor of the candidate")
    paths = set(filter(None, git_value("diff", "--name-only", f"{CURRENT_MAIN_REVISION}...HEAD").splitlines()))
    require(paths, "candidate head has no submitted diff")
    disallowed = sorted(path for path in paths if path not in ALLOWED_PATHS and not path.startswith(OWNED_PREFIX))
    require(not disallowed, f"submitted diff leaves admitted scope: {disallowed}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("provenance-and-negative", "transition-accounting-and-cleanup", "scope-regressions-and-resources", "delivery-provenance"))
    mode = parser.parse_args().mode
    evidence = load_json(TASK_ROOT / "negative-evidence.json")
    verify_identity(evidence)
    verify_negative()
    verify_causal_source()
    result: dict[str, object] = {"schema": "audio-runtime.c64.verification.v2", "mode": mode}
    if mode == "transition-accounting-and-cleanup":
        result["focused_transition"] = run_focused_transition()
    elif mode == "scope-regressions-and-resources":
        verify_scope()
        result["changed_paths"] = sorted(changed_paths())
    elif mode == "delivery-provenance":
        verify_scope()
        verify_delivery()
        result["submitted_revision"] = git_value("rev-parse", "HEAD")
        result["submitted_paths"] = sorted(changed_paths())
    result["passed"] = True
    result["source_hashes"] = {
        "rtc_device_runtime.go": sha256_file(REPO_ROOT / PRODUCTION_RELATIVE),
        "test_fixtures": {relative.as_posix(): sha256_file(REPO_ROOT / relative) for relative in TEST_FIXTURE_RELATIVES},
    }
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError, VerificationError) as error:
        print(f"C64 verification failure: {error}", file=sys.stderr)
        raise SystemExit(1) from error
