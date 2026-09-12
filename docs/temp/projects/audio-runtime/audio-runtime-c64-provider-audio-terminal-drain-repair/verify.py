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
	"agent-cli/test/integration/session_tool_audio_remote_e2e_test.go",
}
TEST_FIXTURE_RELATIVES = (Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go"),)
PRODUCTION_RELATIVE = Path("agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go")
MAX_OUTPUT_BYTES = 64 * 1024
BUILD_INPUT_PREFIXES = (
    "agent-cli",
    "go-agent-loop",
    "go-agent-runtime",
    "go-audio",
    "go-device-gateway",
    "go-llm-gateway",
    "go.work",
    "go.work.sum",
)
BUILD_ENV_KEYS = ("GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOTOOLCHAIN", "GOVERSION", "GOWORK")


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


def sha256_candidate_diff(revision: str) -> str:
    result = subprocess.run(
        ["git", "diff", "--binary", f"{CURRENT_MAIN_REVISION}...{revision}"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    )
    return sha256_bytes(result.stdout)


def build_input_paths() -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-z", "--", *BUILD_INPUT_PREFIXES],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    )
    paths = [Path(raw) for raw in result.stdout.decode().split("\0") if raw]
    require(paths, "candidate build input manifest is empty")
    return paths


def build_input_manifest() -> dict[str, object]:
    digest = hashlib.sha256()
    paths = build_input_paths()
    for relative in paths:
        digest.update(relative.as_posix().encode("utf-8"))
        digest.update(b"\0")
        digest.update((REPO_ROOT / relative).read_bytes())
        digest.update(b"\0")
    return {
        "algorithm": "path-nul-content-nul-sha256-v1",
        "path_prefixes": list(BUILD_INPUT_PREFIXES),
        "file_count": len(paths),
        "sha256": digest.hexdigest(),
    }


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


def require_revision(value: object, label: str) -> str:
    require(isinstance(value, str) and len(value) == 40, f"{label} is not a full revision")
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
    if marker:
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


def verify_build_provenance(
    record: object,
    *,
    label: str,
    tested_source_revision: str,
    fixture_revision: str,
    expected_inputs: dict[str, object] | None = None,
) -> dict:
    build = verify_build(record, label=label)
    provenance = build.get("provenance")
    require(isinstance(provenance, dict), f"{label} build provenance is missing")
    require(provenance.get("tested_source_revision") == tested_source_revision, f"{label} tested source revision changed")
    require(provenance.get("fixture_revision") == fixture_revision, f"{label} fixture revision changed")

    inputs = provenance.get("build_inputs")
    require(isinstance(inputs, dict), f"{label} build input manifest is missing")
    require(inputs.get("algorithm") == "path-nul-content-nul-sha256-v1", f"{label} build input algorithm changed")
    require(inputs.get("path_prefixes") == list(BUILD_INPUT_PREFIXES), f"{label} build input scope changed")
    require(isinstance(inputs.get("file_count"), int) and inputs.get("file_count", 0) > 0, f"{label} build input count is invalid")
    require_hex(inputs.get("sha256"), f"{label} build input manifest hash")
    if expected_inputs is not None:
        require(inputs == expected_inputs, f"{label} build inputs are not bound to the candidate tree")

    toolchain = provenance.get("toolchain")
    require(isinstance(toolchain, dict), f"{label} toolchain provenance is missing")
    go_version = toolchain.get("go_version")
    require(isinstance(go_version, str) and go_version.startswith("go version go"), f"{label} Go version provenance is invalid")
    go_env = toolchain.get("go_env")
    require(isinstance(go_env, dict) and set(go_env) == set(BUILD_ENV_KEYS), f"{label} Go environment provenance is incomplete")
    require(go_env.get("CGO_ENABLED") == "0", f"{label} build was not recorded with CGO_ENABLED=0")
    require(all(isinstance(go_env.get(key), str) for key in BUILD_ENV_KEYS), f"{label} Go environment values are invalid")

    flags = provenance.get("build_flags")
    require(isinstance(flags, dict), f"{label} build flags are missing")
    require(flags.get("tags") == ["nomicrophone"], f"{label} build tags changed")
    require(flags.get("cgo_enabled") == "0", f"{label} build CGO flag changed")
    require(flags.get("test_package") == TEST_PACKAGE, f"{label} test package changed")
    require(flags.get("command") == build.get("command"), f"{label} executable build command was not captured")

    artifact = provenance.get("artifact")
    require(isinstance(artifact, dict), f"{label} artifact provenance is missing")
    require(isinstance(artifact.get("bytes"), int) and artifact.get("bytes", 0) > 0, f"{label} artifact size is invalid")
    artifact_hash = require_hex(artifact.get("sha256"), f"{label} artifact provenance hash")
    require(record.get("artifact_sha256") == artifact_hash, f"{label} artifact hash is not cross-bound")
    return build


def verify_execution(
    record: object,
    *,
    label: str,
    returncode: int,
    marker: str,
    expected_environment: dict[str, str],
) -> dict:
    run = verify_process(record, label=label, returncode=returncode, marker=marker)
    command = run.get("command")
    require(isinstance(command, list) and len(command) == 6, f"{label} test command is incomplete")
    require(command[1:] == [
        "-test.run",
        f"^{TEST_NAME}$",
        "-test.v",
        "-test.count=1",
        "-test.timeout=60s",
    ], f"{label} test flags changed")
    flags = run.get("test_flags")
    require(flags == {"run": f"^{TEST_NAME}$", "verbose": True, "count": 1, "timeout": "60s"}, f"{label} test flag provenance is missing")
    require(run.get("environment_overrides") == expected_environment, f"{label} test environment provenance changed")
    return run


def verify_fixture_hashes(record: dict, expected: dict[str, str], *, label: str) -> None:
    source_hashes = record.get("source_hashes")
    require(isinstance(source_hashes, dict), f"{label} source hashes missing")
    require(source_hashes.get("rtc_device_runtime.go") == expected["rtc_device_runtime.go"], f"{label} production hash changed")
    fixture_hashes = source_hashes.get("test_fixtures")
    require(fixture_hashes == expected["test_fixtures"], f"{label} fixture hashes changed")
    require_hex(record.get("artifact_sha256"), f"{label} artifact hash")


def verify_identity(evidence: dict) -> None:
    prd = load_json(REPO_ROOT / "prd.json")
    head = git_value("rev-parse", "HEAD")
    require(prd.get("branchName") == EXPECTED_BRANCH, "prd.json branchName does not match the isolated branch")
    require(git_value("branch", "--show-current") == EXPECTED_BRANCH, "candidate is on the wrong branch")
    require(git_value("rev-parse", "origin/main") == CURRENT_MAIN_REVISION, "origin/main is not the freshly fetched current main")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", STARTUP_INTEGRATION_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "startup integration ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", BASE_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "accepted main ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", CURRENT_MAIN_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "current fetched main is not an ancestor of the candidate")
    candidate_revision = require_revision(evidence.get("candidate_revision"), "candidate revision")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", candidate_revision, "HEAD"], cwd=REPO_ROOT).returncode == 0, "evidence fixture revision is not an ancestor of the candidate")
    require(evidence.get("source_revision") == BASE_REVISION, "evidence source revision changed")
    require(evidence.get("current_main_revision") == CURRENT_MAIN_REVISION, "evidence current main changed")
    require(evidence.get("startup_integration_revision") == STARTUP_INTEGRATION_REVISION, "evidence startup ancestry changed")
    require(evidence.get("fixture_revision") == candidate_revision, "fixture revision is not tied to candidate evidence")
    require(evidence.get("candidate_diff_sha256") == sha256_candidate_diff(candidate_revision), "candidate diff hash is stale or unbound")

    verification_revision = require_revision(evidence.get("verification_revision"), "verification revision")
    require(verification_revision == candidate_revision, "evidence verification revision is not the executable candidate revision")
    require(evidence.get("verification_diff_sha256") == evidence.get("candidate_diff_sha256"), "verification diff hash is not bound to the executable candidate")
    evidence_only = evidence.get("evidence_only_descendant")
    evidence_paths = evidence.get("evidence_only_paths")
    require(isinstance(evidence_only, bool), "evidence-only descendant binding is missing")
    require(isinstance(evidence_paths, list) and all(isinstance(path, str) for path in evidence_paths), "evidence-only path binding is invalid")
    if candidate_revision == head:
        require(evidence_only is False and evidence_paths == [], "same-head evidence has an invalid descendant binding")
    else:
        require(evidence_only is True, "evidence executable revision is stale without an explicit descendant binding")
        expected_evidence_path = OWNED_PREFIX + "negative-evidence.json"
        require(evidence_paths == [expected_evidence_path], "evidence-only descendant includes unexpected paths")
        descendant_paths = set(filter(None, git_value("diff", "--name-only", f"{candidate_revision}..{head}").splitlines()))
        require(descendant_paths == {expected_evidence_path}, "post-execution changes are not evidence-only")


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
    require(evidence.get("fixture_test_hashes") == fixture_hashes, "top-level fixture hashes are not revision-bound")
    candidate_record = evidence.get("repaired_candidate")
    require(isinstance(candidate_record, dict), "repaired candidate evidence missing")
    require(candidate_record.get("expected_outcome") == "repaired-pass", "repaired candidate expectation changed")
    candidate_hashes = {
        "rtc_device_runtime.go": sha256_file(REPO_ROOT / PRODUCTION_RELATIVE),
        "test_fixtures": fixture_hashes,
    }
    verify_fixture_hashes(candidate_record, candidate_hashes, label="repaired candidate")
    verify_build_provenance(
        candidate_record.get("build"),
        label="repaired candidate",
        tested_source_revision=candidate_revision,
        fixture_revision=candidate_revision,
        expected_inputs=build_input_manifest(),
    )
    verify_execution(
        candidate_record.get("result"),
        label="repaired candidate",
        returncode=0,
        marker="C64_RENDER_EVIDENCE",
        expected_environment={},
    )
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
    verify_build_provenance(
        source_record.get("build"),
        label="accepted source",
        tested_source_revision=BASE_REVISION,
        fixture_revision=candidate_revision,
    )
    verify_execution(
        source_record.get("result"),
        label="accepted source",
        returncode=1,
        marker="C64_ACCEPTED_SOURCE_FAILURE",
        expected_environment={"C64_CANCEL_ON_TERMINAL": "1"},
    )
    failure = source_record.get("failure_evidence")
    require(failure == {"provider_samples": 9600, "admitted_samples": 0, "consumed_samples": 0, "rendered_samples": 0, "queued_samples": 0, "underflow_samples": 0, "callback_count": 0, "shutdown": "provider-close"}, "accepted source failure evidence changed")
    require(source_record.get("source_hashes", {}).get("test_fixtures") == fixture_hashes, "accepted source fixture was not copied from the candidate fixture")
    require(evidence.get("source_control_mode") == "detached accepted-source production with candidate fixture", "accepted source control mode is not explicit")


def verify_causal_source() -> None:
    source = (REPO_ROOT / PRODUCTION_RELATIVE).read_text(encoding="utf-8")
    tests = "\n".join((REPO_ROOT / relative).read_text(encoding="utf-8") for relative in TEST_FIXTURE_RELATIVES)
    require("const rtcDevicePlaybackDrainTimeout = 5 * time.Second" in source, "finite drain bound is missing")
    drain_position = source.index("drainErr = s.DrainPlayback")
    close_position = source.index("sessionErr = s.Session.Close()")
    require(drain_position < close_position, "provider/session close still overtakes playback drain")
    require("return errors.Join(drainErr, sessionErr, s.binding.Close())" in source, "drain/provider/device errors are not joined")
    for marker in ("terminalObserved", "gracefulCloseRequested", "forwardReceive", "DrainPlayback", "WaitForPump"):
        require(marker in source, f"causal lifecycle marker missing: {marker}")
    for marker in ("TestRTCDeviceBoundSessionTerminalDrainPreservesAcceptedProviderAudio", "PlaybackSamplesObserver", "drainStarted", "closeStarted", "continuationRequested", "provider-close-before-drain", "drain-before-provider-close", "reflect.DeepEqual", "RenderedSamples()", "PlaybackStats()", "QueuedSamples", "UnderflowSamples", "C64_SEQUENCE_EVIDENCE", "C64_RENDER_EVIDENCE", "C64_ACCEPTED_SOURCE_FAILURE", "provider/admission/consumption/queue reconciliation", "DroppedSamples", "OverflowEvents", "DiscardedSamples", "ToolExecutor", "ToolDefinitions", "StreamObserver"):
        require(marker in tests, f"deterministic barrier oracle missing: {marker}")
    require("time.Sleep(" not in tests, "barrier control uses sleep-only scheduling")
    require(tests.count("terminalDrainProviderSamples  = 9600") == 1 and tests.count("terminalDrainDeviceSamples    = 6400") == 1, "barrier sample counts are not exact")


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
