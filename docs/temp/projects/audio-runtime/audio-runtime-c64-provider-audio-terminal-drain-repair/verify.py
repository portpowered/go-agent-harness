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
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
EXPECTED_BRANCH = "codex/audio-runtime-c64-provider-audio-terminal-drain-repair"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c64-provider-audio-terminal-drain-repair/"
ALLOWED_PATHS = {
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go",
}
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


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing evidence: {path}")
    require(path.stat().st_size <= MAX_OUTPUT_BYTES, f"evidence exceeds {MAX_OUTPUT_BYTES} bytes: {path}")
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"evidence is not an object: {path}")
    return value


def verify_identity() -> None:
    prd = load_json(REPO_ROOT / "prd.json")
    require(prd.get("branchName") == EXPECTED_BRANCH, "prd.json branchName does not match the isolated branch")
    require(git_value("branch", "--show-current") == EXPECTED_BRANCH, "candidate is on the wrong branch")
    require(git_value("rev-parse", "origin/main") == BASE_REVISION, "origin/main is not the accepted freshly fetched main")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", STARTUP_INTEGRATION_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "startup integration ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", BASE_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "accepted main ancestry is missing")


def verify_negative() -> None:
    evidence = load_json(TASK_ROOT / "negative-evidence.json")
    require(evidence.get("schema") == "audio-runtime.c64.negative-evidence.v1", "negative evidence schema changed")
    require(evidence.get("source_revision") == BASE_REVISION, "negative evidence source revision changed")
    require(evidence.get("startup_integration_revision") == STARTUP_INTEGRATION_REVISION, "startup integration provenance changed")
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


def verify_causal_source() -> None:
    source = (REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go").read_text(encoding="utf-8")
    test = (REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go").read_text(encoding="utf-8")
    require("const rtcDevicePlaybackDrainTimeout = 5 * time.Second" in source, "finite drain bound is missing")
    drain_position = source.index("drainErr = s.DrainPlayback")
    close_position = source.index("sessionErr = s.Session.Close()")
    require(drain_position < close_position, "provider/session close still overtakes playback drain")
    require("return errors.Join(drainErr, sessionErr, s.binding.Close())" in source, "drain/provider/device errors are not joined")
    for marker in ("terminalObserved", "gracefulCloseRequested", "forwardReceive", "WaitForPump"):
        require(marker in source, f"causal lifecycle marker missing: {marker}")
    for marker in ("TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio", "drainStarted", "closeStarted", "provider-close-before-drain", "drain-before-provider-close", "reflect.DeepEqual", "RenderedSamples()", "PlaybackStats()", "QueuedSamples", "UnderflowSamples", "C64_RENDER_EVIDENCE", "provider/admission/consumption/queue reconciliation", "DroppedSamples", "OverflowEvents", "DiscardedSamples"):
        require(marker in test, f"deterministic barrier oracle missing: {marker}")
    require("time.Sleep(" not in test, "barrier control uses sleep-only scheduling")
    require(test.count("terminalDrainProviderSamples = 9600") == 1 and test.count("terminalDrainDeviceSamples   = 6400") == 1, "barrier sample counts are not exact")


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
    paths = set(filter(None, git_value("diff", "--name-only", BASE_REVISION).splitlines()))
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
    production = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go"
    test = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go"
    require(sum(1 for _ in production.open(encoding="utf-8")) <= 400, "production file exceeds its architecture line budget")
    require(sum(1 for _ in test.open(encoding="utf-8")) <= 600, "test file exceeds its architecture line budget")


def verify_delivery() -> None:
    verify_identity()
    require(not git_status(), "delivery provenance requires a clean candidate tree")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", git_value("rev-parse", "origin/main"), "HEAD"], cwd=REPO_ROOT).returncode == 0, "fetched main is not an ancestor of the candidate")
    paths = set(filter(None, git_value("diff", "--name-only", f"{BASE_REVISION}...HEAD").splitlines()))
    require(paths, "candidate head has no submitted diff")
    disallowed = sorted(path for path in paths if path not in ALLOWED_PATHS and not path.startswith(OWNED_PREFIX))
    require(not disallowed, f"submitted diff leaves admitted scope: {disallowed}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("provenance-and-negative", "transition-accounting-and-cleanup", "scope-regressions-and-resources", "delivery-provenance"))
    mode = parser.parse_args().mode
    verify_identity()
    verify_negative()
    verify_causal_source()
    result: dict[str, object] = {"schema": "audio-runtime.c64.verification.v1", "mode": mode}
    if mode == "transition-accounting-and-cleanup":
        result["focused_transition"] = run_focused_transition()
    elif mode == "scope-regressions-and-resources":
        verify_scope()
        result["changed_paths"] = sorted(changed_paths())
    elif mode == "delivery-provenance":
        verify_delivery()
        result["submitted_revision"] = git_value("rev-parse", "HEAD")
        result["submitted_paths"] = sorted(changed_paths())
    result["passed"] = True
    result["source_hashes"] = {
        "rtc_device_runtime.go": sha256_file(REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go"),
        "rtc_device_runtime_test.go": sha256_file(REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go"),
    }
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError, VerificationError) as error:
        print(f"C64 verification failure: {error}", file=sys.stderr)
        raise SystemExit(1) from error
