#!/usr/bin/env python3
"""Fail-closed verification for the C52 evidence bundle."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from collections import Counter
from pathlib import Path


EVIDENCE = Path(__file__).resolve().parent
MATRIX = EVIDENCE / "matrix.json"
EXPECTED = EVIDENCE / "expected-checkpoints.json"
PR438 = "823bd350fe5d11782c38bda87d7b7bfd7d89d7cd"
PLANNING_MAIN = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
TASK = "audio-runtime-c52-hermetic-room-liveness-characterization"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/"
EXPECTED_PACKAGE = "./services/rooms/internal/lifecycle"

# A valid overlay trace emits the same lifecycle shape for every individual
# subtest invocation. Presence-only checks let a duplicated terminal or a
# transformed checkpoint pass while retaining all of the expected names.
PER_CASE_TRACE_COUNTS = {
    "case_started": 1,
    "live_open": 2,
    "event_drain_created": 2,
    "handle_start": 2,
    "live_event_received": 3,
    "terminal_observed": 1,
    "terminal_metadata_latched": 1,
    "peer_cancel_snapshot": 1,
    "room_context_cancel_requested": 1,
    "participant_wait_returned": 2,
    "participant_handle_closed": 2,
    "participant_result_recorded": 2,
    "room_result_snapshot": 1,
    "room_run_return": 1,
    "handle_wait_returned": 2,
    "handle_close_returned": 2,
    "participant_event_drain_waited": 2,
    "participant_wait_completed": 2,
}
PER_CASE_TRACE_MIN_COUNTS = {
    "liveness_cancel_requested": 1,
    "handle_cancel": 3,
    "sink_publish_before": 3,
    "sink_publish": 3,
    "sink_publish_returned": 3,
    "diagnostic_published": 5,
}


class VerificationError(Exception):
    pass


def load(path: Path) -> object:
    if not path.is_file():
        raise VerificationError(f"missing evidence file: {path.relative_to(EVIDENCE)}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise VerificationError(f"invalid JSON: {path}: {exc}") from exc


def write(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def run_git(*args: str) -> tuple[int, str, str]:
    result = subprocess.run(["git", *args], cwd=EVIDENCE, capture_output=True, text=True, check=False)
    return result.returncode, result.stdout.rstrip("\n"), result.stderr.strip()


def run_root() -> Path:
    code, output, error = run_git("rev-parse", "--show-toplevel")
    if code != 0:
        raise VerificationError(f"cannot find repository root: {error}")
    return Path(output)


def latest_run() -> tuple[dict[str, object], Path]:
    value = load(EVIDENCE / "matrix-results.json")
    if not isinstance(value, dict):
        raise VerificationError("matrix-results.json must be an object")
    run_id = value.get("run_id")
    if not isinstance(run_id, str) or not run_id:
        raise VerificationError("matrix result has no run_id")
    run_file = EVIDENCE / "runs" / run_id / "run.json"
    run = load(run_file)
    if not isinstance(run, dict):
        raise VerificationError("run.json must be an object")
    return run, EVIDENCE / "runs" / run_id


def records(run: dict[str, object]) -> list[dict[str, object]]:
    values = run.get("cells")
    if not isinstance(values, list):
        raise VerificationError("matrix run has no cells")
    return [value for value in values if isinstance(value, dict)]


def process_failure(record: dict[str, object]) -> bool:
    process = record.get("process")
    if not isinstance(process, dict):
        return True
    cleanup = process.get("cleanup")
    return bool(
        process.get("timed_out")
        or process.get("output_overflow")
        or process.get("reader_survivor")
        or (
            isinstance(cleanup, dict)
            and (cleanup.get("group_survivor") or cleanup.get("term_error") or cleanup.get("kill_error"))
        )
    )


def selection(record: dict[str, object]) -> dict[str, object]:
    value = record.get("selection")
    if not isinstance(value, dict):
        raise VerificationError(f"cell {record.get('cell_id')} has no selection record")
    return value


def required_run_counts(record: dict[str, object]) -> dict[str, int]:
    value = selection(record).get("required_test_run_counts")
    if not isinstance(value, dict):
        raise VerificationError("missing required test run counts")
    return {str(key): int(value) for key, value in value.items()}


def required_tests_for_cell(cell: dict[str, object]) -> list[str]:
    args = [str(value) for value in cell.get("args", [])]
    if "-run" not in args:
        raise VerificationError(f"matrix cell {cell.get('id')} has no -run selector")
    run = args[args.index("-run") + 1]
    if run.endswith("/provider_timeout$"):
        return ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"]
    if run.endswith("/empty_response$"):
        return ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/empty_response"]
    return [
        "TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout",
        "TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/empty_response",
    ]


def requested_count_for_cell(cell: dict[str, object]) -> int | None:
    args = [str(value) for value in cell.get("args", [])]
    values = [value.split("=", 1)[1] for value in args if value.startswith("-count=")]
    if len(values) != 1 or not values[0].isdigit() or int(values[0]) <= 0:
        return None
    return int(values[0])


def parse_raw_go_json(
    stdout_path: Path,
    required_tests: list[str],
    package: str,
    expected_count: int | None = None,
) -> dict[str, object]:
    if not stdout_path.is_file():
        raise VerificationError(f"missing raw go test output: {stdout_path}")
    events: list[dict[str, object]] = []
    invalid_lines: list[str] = []
    for raw in stdout_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            value = json.loads(raw)
        except json.JSONDecodeError:
            invalid_lines.append(raw)
            continue
        if isinstance(value, dict):
            events.append(value)
    test_events: dict[str, dict[str, int]] = {}
    for event in events:
        test = event.get("Test")
        if not isinstance(test, str) or not test:
            continue
        action = str(event.get("Action", ""))
        counts = test_events.setdefault(test, {"run": 0, "pass": 0, "fail": 0, "skip": 0})
        if action in counts:
            counts[action] += 1
    output = "\n".join(str(event.get("Output", "")) for event in events if event.get("Action") == "output")
    cached_marker = "(cached)" in output
    no_tests_marker = "[no tests to run]" in output or any(
        event.get("Action") == "output" and "no tests to run" in str(event.get("Output", ""))
        for event in events
    )
    required_test_pass_counts = {test: test_events.get(test, {}).get("pass", 0) for test in required_tests}
    required_test_fail_counts = {test: test_events.get(test, {}).get("fail", 0) for test in required_tests}
    required_test_skip_counts = {test: test_events.get(test, {}).get("skip", 0) for test in required_tests}
    selection_valid = (
        expected_count is not None
        and expected_count > 0
        and not invalid_lines
        and not cached_marker
        and all(test_events.get(test, {}).get("run", 0) == expected_count for test in required_tests)
        and all(required_test_pass_counts[test] == expected_count for test in required_tests)
        and all(required_test_fail_counts[test] == 0 for test in required_tests)
        and all(required_test_skip_counts[test] == 0 for test in required_tests)
    )
    return {
        "json_line_count": len(events),
        "invalid_json_lines": len(invalid_lines),
        "invalid_json_samples": invalid_lines[:3],
        "test_events": test_events,
        "required_test_run_counts": {test: test_events.get(test, {}).get("run", 0) for test in required_tests},
        "required_test_pass_counts": required_test_pass_counts,
        "required_test_fail_counts": required_test_fail_counts,
        "required_test_skip_counts": required_test_skip_counts,
        "requested_count": expected_count,
        "selection_valid": selection_valid,
        "relevant_test_names": sorted(name for name in test_events if name.startswith("TestRunnerRoutesTypedLivenessFaultAndPreservesPeer")),
        "package_events": [event for event in events if event.get("Package") == "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle" and not event.get("Test")],
        "cached_marker": cached_marker,
        "no_tests_marker": no_tests_marker,
        "first_assertion": next((line for line in output.splitlines() if "browser_parity_test.go:" in line), ""),
        "package": package,
    }


def raw_selection_for_process(
    stdout_path: Path,
    process: dict[str, object],
    required_tests: list[str],
    package: str,
    expected_count: int | None = None,
) -> dict[str, object]:
    if not isinstance(process, dict):
        raise VerificationError(f"missing process record for {stdout_path}")
    if not stdout_path.is_file():
        raise VerificationError(f"missing raw go test output: {stdout_path}")
    if process.get("stdout_bytes") != stdout_path.stat().st_size or process.get("stdout_sha256") != digest(stdout_path):
        raise VerificationError(f"raw stdout does not match process provenance: {stdout_path}")
    return parse_raw_go_json(stdout_path, required_tests, package, expected_count)


def raw_selection_for_record(run_root: Path, record: dict[str, object], cell: dict[str, object]) -> dict[str, object]:
    label = str(record.get("revision_label"))
    cell_id = str(cell.get("id"))
    stdout_path = run_root / "cells" / label / cell_id / "stdout.log"
    try:
        stdout_path.resolve().relative_to(run_root.resolve())
    except ValueError as exc:
        raise VerificationError(f"matrix stdout path escapes run root: {stdout_path}") from exc
    process = record.get("process")
    if not isinstance(process, dict):
        raise VerificationError(f"missing process record for {label}/{cell_id}")
    return raw_selection_for_process(
        stdout_path,
        process,
        required_tests_for_cell(cell),
        EXPECTED_PACKAGE,
        requested_count_for_cell(cell),
    )


def validate_raw_selection(
    parsed: dict[str, object],
    required_tests: list[str],
    expected_count: int | None,
    context: str,
    *,
    require_selected: bool,
) -> None:
    if expected_count is None or expected_count <= 0:
        raise VerificationError(f"{context} has no valid frozen -count value")
    test_events = parsed.get("test_events")
    if not isinstance(test_events, dict):
        raise VerificationError(f"{context} has no raw go test action counts")
    expected = expected_count if require_selected else 0
    for test in required_tests:
        counts = test_events.get(test)
        if not isinstance(counts, dict):
            raise VerificationError(f"{context} did not emit raw actions for {test}")
        actual = {
            "run": counts.get("run", 0),
            "pass": counts.get("pass", 0),
            "fail": counts.get("fail", 0),
            "skip": counts.get("skip", 0),
        }
        expected_actions = {"run": expected, "pass": expected, "fail": 0, "skip": 0}
        if actual != expected_actions:
            raise VerificationError(f"{context} raw selection for {test} is {actual}, expected {expected_actions}")
    if require_selected and parsed.get("selection_valid") is not True:
        raise VerificationError(f"{context} raw selection is not valid")
    if not require_selected and parsed.get("selection_valid") is True:
        raise VerificationError(f"{context} unexpectedly validated a zero-test selection")


def verify_provenance() -> dict[str, object]:
    value = load(EVIDENCE / "provenance.json")
    if not isinstance(value, dict):
        raise VerificationError("provenance.json must be an object")
    required = [
        "schema", "project", "task", "contract_revision", "factory", "branch", "worktree",
        "candidate_source_revision", "evidence_candidate_revision", "startup_integration_revision", "planning_main_revision",
        "pr438_head_revision", "refreshed_origin_main_revision", "ancestry", "toolchain",
        "environment_allowlist", "workspace_inputs", "source_archives", "ci_observation",
        "primary_observation", "runner_inputs", "no_source_mutation",
    ]
    missing = [key for key in required if key not in value]
    if missing:
        raise VerificationError(f"provenance missing fields: {missing}")
    if value.get("project") != "audio-runtime" or value.get("task") != TASK:
        raise VerificationError("provenance project/task identity mismatch")
    branch = value.get("branch")
    worktree = value.get("worktree")
    if not isinstance(branch, dict) or branch.get("matches") is not True or branch.get("actual") != branch.get("expected"):
        raise VerificationError("provenance branch does not match the admitted task")
    if not isinstance(worktree, dict) or worktree.get("isolated") is not True or worktree.get("prd_branch_name") != branch.get("expected"):
        raise VerificationError("provenance worktree/PRD branch confirmation is incomplete")
    if value.get("startup_integration_revision") != STARTUP_INTEGRATION or value.get("planning_main_revision") != PLANNING_MAIN or value.get("pr438_head_revision") != PR438:
        raise VerificationError("declared comparison ancestry/revision identity changed")
    for key in ("candidate_source_revision", "evidence_candidate_revision"):
        if not re.fullmatch(r"[0-9a-f]{40}", str(value.get(key, ""))):
            raise VerificationError(f"provenance candidate revision is incomplete: {key}")
    ancestry = value.get("ancestry")
    if not isinstance(ancestry, dict):
        raise VerificationError("ancestry must be an object")
    for key in ("baseline", "startup_integration", "planning_main", "refreshed_origin_main"):
        if ancestry.get(key) is not True:
            raise VerificationError(f"required ancestry is not proven: {key}")
    environment_allowlist = value.get("environment_allowlist")
    if not isinstance(environment_allowlist, dict):
        raise VerificationError("environment allowlist is incomplete")
    environment_base = environment_allowlist.get("base", environment_allowlist)
    required_base = {"CGO_ENABLED", "GOFLAGS", "GOTRACEBACK", "GOTOOLCHAIN", "GOSUMDB"}
    if not isinstance(environment_base, dict) or not required_base.issubset(environment_base):
        raise VerificationError("environment allowlist is incomplete")
    fixed_environment = environment_allowlist.get("fixed")
    if not isinstance(fixed_environment, dict) or not {"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "GOENV", "GOPROXY"}.issubset(fixed_environment):
        raise VerificationError("fixed hermetic environment is incomplete")
    runner_injected = environment_allowlist.get("runner_injected")
    if not isinstance(runner_injected, list) or not {"C52_REVISION", "C52_MATRIX_CELL", "C52_TRACE_PATH", "C52_OVERLAY_HASH", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "GOWORK"}.issubset(runner_injected):
        raise VerificationError("runner environment allowlist is incomplete")
    workspace = value.get("workspace_inputs")
    if not isinstance(workspace, list) or not workspace:
        raise VerificationError("workspace/module hashes are missing")
    for item in workspace:
        if not isinstance(item, dict) or not item.get("path") or not re.fullmatch(r"[0-9a-f]{64}", str(item.get("sha256", ""))):
            raise VerificationError("workspace input hash is incomplete")
    archives = value.get("source_archives")
    if not isinstance(archives, dict) or set(archives) != {"pr438", "planning_main"}:
        raise VerificationError("both source archives are required")
    for item in archives.values():
        if not isinstance(item, dict) or not re.fullmatch(r"[0-9a-f]{64}", str(item.get("sha256", ""))):
            raise VerificationError("source archive hash is incomplete")
        path = EVIDENCE / str(item.get("path", ""))
        if not path.is_file() or digest(path) != item["sha256"]:
            raise VerificationError(f"source archive hash mismatch: {path}")
    ci = value.get("ci_observation")
    if not isinstance(ci, dict) or ci.get("run_id") != "34562579355" or ci.get("job_id") != "103148160889" or ci.get("head_sha") != PR438:
        raise VerificationError("prior CI observation identity is incomplete")
    log_path = EVIDENCE / str(ci.get("log_path", ""))
    metadata_path = EVIDENCE / str(ci.get("metadata_path", ""))
    if (
        not log_path.is_file()
        or not metadata_path.is_file()
        or not re.fullmatch(r"[0-9a-f]{64}", str(ci.get("log_sha256", "")))
        or not re.fullmatch(r"[0-9a-f]{64}", str(ci.get("metadata_sha256", "")))
        or digest(log_path) != ci.get("log_sha256")
        or digest(metadata_path) != ci.get("metadata_sha256")
    ):
        raise VerificationError("complete prior CI log/metadata is missing or changed")
    metadata = load(metadata_path)
    if not isinstance(metadata, dict):
        raise VerificationError("prior CI metadata must be an object")
    metadata_identity = {
        "databaseId": int(ci["run_id"]),
        "headSha": PR438,
        "status": ci.get("status"),
        "conclusion": ci.get("conclusion"),
        "attempt": ci.get("attempt"),
    }
    if any(metadata.get(key) != expected for key, expected in metadata_identity.items()):
        raise VerificationError("prior CI metadata identity does not match the declared observation")
    capture = metadata.get("capture")
    if not isinstance(capture, dict):
        raise VerificationError("prior CI metadata has no capture provenance")
    if (
        capture.get("logSha256") != ci.get("log_sha256")
        or capture.get("logBytes") != ci.get("log_bytes")
        or capture.get("logLines") != ci.get("log_lines")
        or capture.get("logExitCode") != 0
    ):
        raise VerificationError("prior CI metadata capture does not match the declared log")
    primary = value.get("primary_observation")
    if not isinstance(primary, dict) or primary.get("status") != "REPORTED_UNDER_PAYLOAD" or primary.get("independently_verified") is not False:
        raise VerificationError("primary rerun must remain explicitly reported, not fabricated")
    runner = value.get("runner_inputs")
    if not isinstance(runner, dict):
        raise VerificationError("runner/overlay provenance is missing")
    for key in ("matrix_sha256", "run_sha256", "overlay_sha256", "verify_sha256"):
        if not re.fullmatch(r"[0-9a-f]{64}", str(runner.get(key, ""))):
            raise VerificationError(f"runner hash missing: {key}")
    if value.get("no_source_mutation") is not True:
        raise VerificationError("provenance does not prove source mutation isolation")
    return {"mode": "provenance", "passes": True, "candidate_source_revision": value.get("candidate_source_revision"), "ci_log_sha256": ci.get("log_sha256")}


def trace_events(path: Path) -> list[dict[str, object]]:
    if not path.is_file():
        raise VerificationError(f"missing trace: {path}")
    result: list[dict[str, object]] = []
    sequences: set[int] = set()
    for line_number, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), start=1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise VerificationError(f"invalid trace JSON at {path}:{line_number}: {exc}") from exc
        if not isinstance(value, dict) or value.get("schema") != "audio-runtime-c52-trace-v1":
            raise VerificationError(f"invalid trace record at {path}:{line_number}")
        sequence = value.get("seq")
        if isinstance(sequence, bool) or not isinstance(sequence, int) or sequence in sequences:
            raise VerificationError(f"invalid or duplicate trace sequence at {path}:{line_number}")
        sequences.add(sequence)
        result.append(value)
    if not result:
        raise VerificationError(f"empty trace: {path}")
    # Trace writes can be appended by concurrent callbacks in a different
    # order from the atomic sequence assignment.  Sequence is the canonical
    # event order; validate timestamp shape without treating timestamp order
    # as causal because sampling happens in the callback after sequence
    # allocation.
    ordered = sorted(result, key=lambda event: int(event["seq"]))
    for event in ordered:
        monotonic = event.get("monotonic_ns")
        if isinstance(monotonic, bool) or not isinstance(monotonic, int) or monotonic < 0:
            raise VerificationError(f"trace monotonic timestamp is invalid at {path}")
    return ordered


def record_trace(record: dict[str, object], run_root: Path) -> list[dict[str, object]]:
    relative = record.get("trace_path")
    if not isinstance(relative, str) or not relative:
        raise VerificationError(f"cell {record.get('cell_id')} has no trace path")
    path = EVIDENCE / relative
    try:
        path.resolve().relative_to(EVIDENCE.resolve())
    except ValueError as exc:
        raise VerificationError("trace path escapes owned evidence") from exc
    if record.get("trace_sha256") and digest(path) != record.get("trace_sha256"):
        raise VerificationError(f"trace hash mismatch: {relative}")
    events = trace_events(path)
    for event in events:
        if event.get("revision") != record.get("revision") or event.get("matrix_cell") != record.get("cell", {}).get("id"):
            raise VerificationError(f"trace provenance mismatch in {relative}")
    return events


def event_fields(event: dict[str, object]) -> dict[str, str]:
    value = event.get("fields")
    return {str(key): str(item) for key, item in value.items()} if isinstance(value, dict) else {}


def verify_matrix() -> dict[str, object]:
    matrix = load(MATRIX)
    if not isinstance(matrix, dict) or matrix.get("schema") != "audio-runtime-c52-matrix-v1" or matrix.get("frozen") is not True:
        raise VerificationError("matrix is not the frozen C52 matrix")
    run, run_root = latest_run()
    if run.get("matrix_sha256") != digest(MATRIX):
        raise VerificationError("matrix changed after execution")
    values = records(run)
    matrix_cells = matrix.get("cells")
    expected_cells = {str(cell.get("id")): cell for cell in matrix_cells if isinstance(cell, dict)} if isinstance(matrix_cells, list) else {}
    expected_count = len(expected_cells) * 2
    if len(values) != expected_count or expected_count != 22:
        raise VerificationError(f"matrix cell count {len(values)} != frozen 22")
    if float(run.get("aggregate_timeout_seconds", 0)) > 900 or run.get("aggregate_deadline_met") is not True:
        raise VerificationError("aggregate deadline was not met or exceeds 900 seconds")
    seen: set[tuple[str, str]] = set()
    behavioral_failures: list[dict[str, object]] = []
    for record in values:
        label = str(record.get("revision_label"))
        revision = str(record.get("revision"))
        cell = record.get("cell")
        if label not in {"pr438", "planning-main"} or revision not in {PR438, PLANNING_MAIN} or not isinstance(cell, dict):
            raise VerificationError("matrix record revision/cell identity is invalid")
        key = (label, str(cell.get("id")))
        if key in seen:
            raise VerificationError(f"duplicate matrix result: {key}")
        seen.add(key)
        if key[1] not in expected_cells or cell != expected_cells[key[1]]:
            raise VerificationError(f"matrix cell was changed after execution for {key}")
        if record.get("status") == "NOT_RUN_AGGREGATE_DEADLINE" or process_failure(record):
            raise VerificationError(f"runner control failed for {key}")
        cleanup = record.get("process", {}).get("cleanup") if isinstance(record.get("process"), dict) else None
        if not isinstance(cleanup, dict) or cleanup.get("reason") != "parent_exit":
            raise VerificationError(f"normal parent-exit group cleanup was not recorded for {key}")
        frozen_cell = expected_cells[key[1]]
        required_tests = required_tests_for_cell(frozen_cell)
        expected_test_count = requested_count_for_cell(frozen_cell)
        raw = raw_selection_for_record(run_root, record, frozen_cell)
        validate_raw_selection(
            raw,
            required_tests,
            expected_test_count,
            f"{label}/{key[1]}",
            require_selected=True,
        )
        embedded = selection(record)
        if embedded != raw:
            raise VerificationError(f"embedded selection differs from raw go test JSON for {key}")
        counts = {str(name): int(value) for name, value in raw["required_test_run_counts"].items()}
        if not counts or any(value != expected_test_count for value in counts.values()):
            raise VerificationError(f"zero required test selection for {key}: {counts}")
        parsed = raw
        if int(parsed.get("invalid_json_lines", 0)) != 0 or parsed.get("cached_marker") is True:
            raise VerificationError(f"invalid/cached go test JSON for {key}")
        if record.get("process", {}).get("exit_code") != 0:
            behavioral_failures.append({"revision_label": label, "cell_id": cell.get("id"), "exit_code": record.get("process", {}).get("exit_code"), "first_failure": record.get("process", {}).get("first_failure")})
    negative = run.get("negative_controls")
    if not isinstance(negative, dict) or negative.get("all_rejected") is not True:
        raise VerificationError("selection/process negative controls did not all reject")
    return {"mode": "matrix", "passes": True, "cell_count": len(values), "behavioral_failures": behavioral_failures, "run_id": run.get("run_id")}


def case_groups(events: list[dict[str, object]]) -> list[list[dict[str, object]]]:
    starts = [index for index, event in enumerate(events) if event.get("kind") == "case_started"]
    groups: list[list[dict[str, object]]] = []
    for index, start in enumerate(starts):
        end = starts[index + 1] if index + 1 < len(starts) else len(events)
        groups.append(events[start:end])
    return groups


def verify_overlay_integrity() -> dict[str, object]:
    allowed = {
        "go-agent-runtime/services/rooms/internal/lifecycle/c52_overlay.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/events.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/participant.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/state.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/runner.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/browser_parity_test.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/runner_test.go",
    }
    run, run_root = latest_run()
    manifests = []
    for label in ("pr438", "planning-main"):
        manifest = load(run_root / f"overlay-{label}/overlay-manifest.json")
        if not isinstance(manifest, dict):
            raise VerificationError("overlay manifest must be an object")
        files = manifest.get("files")
        if not isinstance(files, list) or not files:
            raise VerificationError(f"overlay files missing for {label}")
        for item in files:
            if not isinstance(item, dict) or str(item.get("path")) not in allowed:
                raise VerificationError(f"overlay touched an undeclared scratch path: {item}")
        manifests.append({"label": label, "source_sha256": manifest.get("source_sha256"), "file_count": len(files)})
    code, output, _ = run_git("status", "--porcelain", "--untracked-files=all")
    if code != 0:
        raise VerificationError("cannot inspect source mutation")
    outside = [line[3:] for line in output.splitlines() if len(line) >= 4 and not line[3:].startswith(OWNED_PREFIX)]
    if outside:
        raise VerificationError(f"tracked/source files changed outside C52 ownership: {outside}")
    return {"mode": "overlay-integrity", "passes": True, "manifests": manifests, "outside_owned_changes": []}


REQUIRED_TRACE_KINDS = [
    "case_started", "live_open", "event_drain_created", "handle_start", "live_event_received",
    "terminal_observed", "terminal_metadata_latched", "liveness_cancel_requested", "handle_cancel",
    "sink_publish_before", "sink_publish", "sink_publish_returned", "diagnostic_published",
    "peer_cancel_snapshot", "room_context_cancel_requested", "participant_wait_returned",
    "participant_handle_closed", "participant_result_recorded", "room_result_snapshot", "room_run_return",
]


def validate_case_trace(group: list[dict[str, object]], record: dict[str, object], observed_failure: bool = False) -> None:
    if not group or group[0].get("kind") != "case_started":
        raise VerificationError(f"case trace does not start with case_started for {record.get('revision_label')}/{record.get('cell', {}).get('id')}")
    counts = Counter(str(event.get("kind")) for event in group)
    for kind, expected in PER_CASE_TRACE_COUNTS.items():
        if counts.get(kind, 0) != expected:
            raise VerificationError(
                f"checkpoint cardinality for {kind} is {counts.get(kind, 0)}, expected {expected} "
                f"for {record.get('revision_label')}/{record.get('cell', {}).get('id')}"
            )
    for kind, minimum in PER_CASE_TRACE_MIN_COUNTS.items():
        if counts.get(kind, 0) < minimum:
            raise VerificationError(
                f"checkpoint count for {kind} is {counts.get(kind, 0)}, expected at least {minimum} "
                f"for {record.get('revision_label')}/{record.get('cell', {}).get('id')}"
            )
    classification = event_fields(group[0]).get("classification")
    if classification not in {"silent_provider_timeout", "silent_provider_empty_response"}:
        raise VerificationError("case_started lacks a valid typed liveness classification")
    if group[0].get("participant") != "silent":
        raise VerificationError("case_started has an unexpected participant")
    liveness = [event_fields(event) for event in group if event.get("kind") == "live_event_received" and event_fields(event).get("event_kind") == "liveness_fault"]
    if len(liveness) != 1 or liveness[0].get("liveness_classification") != classification:
        raise VerificationError("typed liveness event is missing or transformed")
    terminal = [event_fields(event) for event in group if event.get("kind") == "terminal_observed"]
    latched = [event_fields(event) for event in group if event.get("kind") == "terminal_metadata_latched"]
    if terminal[0].get("liveness_classification") != classification or latched[0].get("classification") != classification:
        raise VerificationError("terminal metadata does not preserve typed liveness classification")
    outcomes = [event for event in group if event.get("kind") == "test_outcome"]
    deferred_outcomes = [event for event in group if event.get("kind") == "test_deferred_outcome"]
    if observed_failure:
        if outcomes or len(deferred_outcomes) != 1:
            raise VerificationError("failed case does not have exactly one bounded deferred outcome")
    elif len(outcomes) != 1 or deferred_outcomes:
        raise VerificationError("passing case does not have exactly one direct outcome")
    peer_snapshots = [event_fields(event) for event in group if event.get("kind") == "peer_cancel_snapshot"]
    if observed_failure:
        if len(peer_snapshots) != 1 or peer_snapshots[0].get("before") != "external_room_cancel" or peer_snapshots[0].get("count") == "0":
            raise VerificationError("failed case lacks the observed non-zero peer cancellation boundary")
    elif any(fields.get("count") != "0" or fields.get("before") != "external_room_cancel" for fields in peer_snapshots):
        raise VerificationError("peer cancellation was not zero before external room cancellation")
    allowed_cancel_reasons = {"test_external_cancel"}
    if observed_failure:
        allowed_cancel_reasons.add("test_deferred_cleanup_after_assertion")
    if any(event_fields(event).get("reason") not in allowed_cancel_reasons for event in group if event.get("kind") == "room_context_cancel_requested"):
        raise VerificationError("room cancellation checkpoint has an unexpected reason")
    for kind in ("participant_wait_returned", "participant_handle_closed", "participant_result_recorded"):
        participants = sorted(str(event.get("participant")) for event in group if event.get("kind") == kind)
        if participants != ["peer", "silent"]:
            raise VerificationError(f"{kind} does not close exactly the peer and silent participants")
    sink_keys = {
        kind: sorted((str(event.get("participant")), event_fields(event).get("event_kind", "")) for event in group if event.get("kind") == kind)
        for kind in ("sink_publish_before", "sink_publish", "sink_publish_returned")
    }
    if not (sink_keys["sink_publish_before"] == sink_keys["sink_publish"] == sink_keys["sink_publish_returned"]):
        raise VerificationError("diagnostic/sink checkpoint routing was transformed")


def verify_checkpoints() -> dict[str, object]:
    run, run_root = latest_run()
    outcomes: list[dict[str, object]] = []
    for record in records(run):
        events = record_trace(record, run_root)
        groups = case_groups(events)
        args = record.get("cell", {}).get("args", []) if isinstance(record.get("cell"), dict) else []
        count_values = [str(value) for value in args if str(value).startswith("-count=")]
        expected_cases = int(count_values[0].split("=", 1)[1]) * len(required_tests_for_cell(record["cell"])) if len(count_values) == 1 and isinstance(record.get("cell"), dict) else -1
        if len(count_values) != 1 or len(groups) != expected_cases:
            raise VerificationError(f"trace case count does not match frozen test count for {record.get('revision_label')}/{record.get('cell', {}).get('id')}")
        process = record.get("process")
        process_failed = isinstance(process, dict) and process.get("exit_code") != 0
        failed_groups = [group for group in groups if not any(event.get("kind") == "test_outcome" for event in group)]
        if failed_groups and not process_failed:
            raise VerificationError(f"trace has an unattributed failed case despite a passing process for {record.get('revision_label')}/{record.get('cell', {}).get('id')}")
        if process_failed and len(failed_groups) != 1:
            raise VerificationError(f"failed process does not have exactly one attributable failed case for {record.get('revision_label')}/{record.get('cell', {}).get('id')}")
        for group in groups:
            validate_case_trace(group, record, observed_failure=group in failed_groups)
        kinds = [str(event.get("kind")) for event in events]
        missing = [kind for kind in REQUIRED_TRACE_KINDS if kind not in kinds]
        peer_counts = [event_fields(event).get("count") for event in events if event.get("kind") == "peer_cancel_snapshot"]
        if missing:
            raise VerificationError(f"missing checkpoints for {record.get('revision_label')}/{record.get('cell', {}).get('id')}: {missing}")
        if not peer_counts or any(count != "0" for count in peer_counts):
            raise VerificationError("peer cancellation was not zero before external cancellation")
        for event in events:
            if event.get("kind") == "live_event_received" and event_fields(event).get("event_kind") == "liveness_fault" and event_fields(event).get("liveness_classification") not in {"silent_provider_timeout", "silent_provider_empty_response"}:
                raise VerificationError("liveness event lacks typed classification")
        outcomes.append({"revision_label": record.get("revision_label"), "cell_id": record.get("cell", {}).get("id"), "event_count": len(events), "kinds": sorted(set(kinds)), "peer_cancel_counts": peer_counts})
    return {"mode": "checkpoints", "passes": True, "cells": outcomes}


def first_divergence() -> dict[str, object]:
    expected = load(EXPECTED)
    if not isinstance(expected, dict):
        raise VerificationError("expected checkpoints must be an object")
    order = expected.get("per_case_order")
    if not isinstance(order, list) or not order:
        raise VerificationError("expected checkpoint order is missing")
    run, run_root = latest_run()
    cases: list[dict[str, object]] = []
    for record in records(run):
        events = record_trace(record, run_root)
        for group_index, group in enumerate(case_groups(events), start=1):
            observed_failure = not any(event.get("kind") == "test_outcome" for event in group)
            validate_case_trace(group, record, observed_failure=observed_failure)
            by_kind: dict[str, list[dict[str, object]]] = {}
            for event in group:
                by_kind.setdefault(str(event.get("kind")), []).append(event)
            missing = [str(kind) for kind in order if str(kind) not in by_kind]
            positions = {kind: int(by_kind[kind][0].get("seq", 0)) for kind in by_kind}
            order_violations: list[dict[str, object]] = []
            last = -1
            for kind in order:
                kind = str(kind)
                if kind not in positions:
                    continue
                if positions[kind] < last:
                    order_violations.append({"checkpoint": kind, "seq": positions[kind], "previous_seq": last})
                last = max(last, positions[kind])
            signature = [str(event.get("kind")) for event in group]
            finding = "none"
            peer_snapshots = [event_fields(event) for event in group if event.get("kind") == "peer_cancel_snapshot"]
            if any(fields.get("before") == "external_room_cancel" and fields.get("count") != "0" for fields in peer_snapshots):
                finding = "transformed:peer_cancel_snapshot"
            elif missing:
                finding = f"missing:{missing[0]}"
            elif order_violations:
                finding = f"reordered:{order_violations[0]['checkpoint']}"
            fields = event_fields(next((event for event in reversed(group) if event.get("kind") in {"test_outcome", "test_deferred_outcome"}), {}))
            cases.append({
                "revision_label": record.get("revision_label"),
                "revision": record.get("revision"),
                "cell_id": record.get("cell", {}).get("id"),
                "case_index": group_index,
                "classification": next((event_fields(event).get("classification") for event in group if event.get("kind") == "case_started"), ""),
                "first_divergence": finding,
                "missing_checkpoints": missing,
                "order_violations": order_violations,
                "observed_signature": signature,
                "later_consequences": {
                    "room_run_error": fields.get("run_error", ""),
                    "room_termination_reason": fields.get("termination_reason", ""),
                    "participant_result_count": sum(1 for event in group if event.get("kind") == "participant_result_recorded"),
                    "cleanup_events": [kind for kind in ("participant_wait_returned", "participant_handle_closed", "participant_event_drain_waited", "participant_wait_completed") if kind in signature],
                },
            })
    if not cases:
        raise VerificationError("no case traces available for first-divergence analysis")
    result = {
        "schema": "audio-runtime-c52-first-divergence-v1",
        "comparison": {"pr438": PR438, "planning_main": PLANNING_MAIN},
        "expected_state_machine": order,
        "cases": cases,
        "overall": "NO_DIVERGENCE_OBSERVED" if all(item["first_divergence"] == "none" for item in cases) else "OBSERVED_CHECKPOINT_VARIANCE",
        "interpretation": "The line-220 CI assertion remains a downstream observation; this file names only the first missing/reordered checkpoint in the owned trace and preserves later result/cleanup consequences separately.",
    }
    write(EVIDENCE / "first-divergence.json", result)
    return {"mode": "first-divergence", "passes": True, "overall": result["overall"], "case_count": len(cases)}


def verify_cleanup() -> dict[str, object]:
    run, run_root = latest_run()
    survivors: list[object] = []
    cells = []
    for record in records(run):
        process = record.get("process", {})
        cleanup = process.get("cleanup", {}) if isinstance(process, dict) else {}
        if not isinstance(cleanup, dict) or cleanup.get("reason") != "parent_exit":
            raise VerificationError(f"missing normal parent-exit cleanup for {record.get('revision_label')}/{record.get('cell', {}).get('id')}")
        if cleanup.get("group_survivor"):
            survivors.append({"revision_label": record.get("revision_label"), "cell_id": record.get("cell", {}).get("id")})
        events = record_trace(record, run_root)
        for participant in ("silent", "peer"):
            if not any(event.get("kind") == "participant_result_recorded" and event.get("participant") == participant for event in events):
                raise VerificationError(f"missing participant result cleanup for {participant}")
        cells.append({"revision_label": record.get("revision_label"), "cell_id": record.get("cell", {}).get("id"), "process_group_survivor": bool(cleanup.get("group_survivor")) if isinstance(cleanup, dict) else True})
    negative = run.get("negative_controls", {}).get("controls", {}) if isinstance(run.get("negative_controls"), dict) else {}
    for control_name in ("survivor_process", "normal_exit_survivor"):
        control = negative.get(control_name, {}) if isinstance(negative, dict) else {}
        if not isinstance(control, dict) or control.get("rejected") is not True:
            raise VerificationError(f"{control_name} negative control did not prove group cleanup")
    if survivors:
        raise VerificationError(f"owned process survivors remain: {survivors}")
    return {"mode": "cleanup", "passes": True, "cells": cells, "negative_survivor_controls": ["survivor_process", "normal_exit_survivor"]}


def verify_no_retry_and_selection() -> dict[str, object]:
    matrix = load(MATRIX)
    run, run_root = latest_run()
    ids = [str(record.get("cell", {}).get("id")) for record in records(run)]
    expected_ids = {str(cell.get("id")) for cell in matrix.get("cells", []) if isinstance(cell, dict)} if isinstance(matrix, dict) else set()
    counts = {cell_id: ids.count(cell_id) for cell_id in set(ids)}
    if set(counts) != expected_ids or any(count != 2 for count in counts.values()):
        raise VerificationError("matrix cell identity is not exactly one attempt per revision")
    if any(record.get("attempt") != 1 for record in records(run)):
        raise VerificationError("matrix contains a retry or missing attempt marker")
    if not isinstance(matrix, dict) or len(matrix.get("cells", [])) != 11:
        raise VerificationError("frozen matrix does not contain 11 cells")
    negative = run.get("negative_controls", {}).get("controls", {}) if isinstance(run.get("negative_controls"), dict) else {}
    for name in ("zero_test_selection", "wrong_test_selection"):
        value = negative.get(name, {}) if isinstance(negative, dict) else {}
        if not isinstance(value, dict) or value.get("rejected") is not True:
            raise VerificationError(f"{name} was not fail-closed")
        process = value.get("process")
        if not isinstance(process, dict):
            raise VerificationError(f"{name} has no process record")
        package = EXPECTED_PACKAGE if name == "zero_test_selection" else "./services/session/internal/live"
        control_directory = "zero-test-selection" if name == "zero_test_selection" else "wrong-package-selection"
        raw = raw_selection_for_process(
            run_root / "negative-controls" / control_directory / "stdout.log",
            process,
            ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"],
            package,
            1,
        )
        validate_raw_selection(
            raw,
            ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"],
            1,
            name,
            require_selected=False,
        )
        if value.get("selection") != raw:
            raise VerificationError(f"{name} embedded selection differs from raw go test JSON")
        selected = raw.get("required_test_run_counts", {})
        if any(int(count) != 0 for count in selected.values()):
            raise VerificationError(f"{name} unexpectedly selected a required test")
    return {"mode": "no-retry-and-selection", "passes": True, "attempts": "one per frozen cell", "negative_controls": ["zero_test_selection", "wrong_test_selection"]}


def classify_outcomes(outcomes: list[dict[str, object]], runner_valid: bool) -> str:
    behavior_failures = [item for item in outcomes if item["behavior_passed"] is False]
    if not runner_valid:
        return "INCONCLUSIVE"
    if not behavior_failures:
        return "NON_REPRODUCED"
    signatures = [tuple(item["first_divergence_signatures"]) for item in behavior_failures]
    if len(behavior_failures) >= 2 and len(set(signatures)) == 1 and len({item["revision_label"] for item in behavior_failures}) == 1:
        return "DETERMINISTIC"

    order_failures = [item for item in behavior_failures if item.get("kind") == "explicit-order"]
    order_controls = [item for item in outcomes if item.get("kind") == "explicit-order" and item["behavior_passed"] is True]
    failed_order_ids = {str(item.get("cell_id")) for item in order_failures}
    passed_order_ids = {str(item.get("cell_id")) for item in order_controls}
    if (
        order_failures
        and len(order_failures) == len(behavior_failures)
        and len(failed_order_ids) == 1
        and len(order_controls) >= 2
        and failed_order_ids.isdisjoint(passed_order_ids)
    ):
        return "ORDER_SENSITIVE"

    package_failures = [item for item in behavior_failures if item.get("kind") == "package-concurrency"]
    package_controls = [item for item in outcomes if item.get("kind") == "package-concurrency" and item["behavior_passed"] is True]
    if (
        package_failures
        and len(package_failures) == len(behavior_failures)
        and any(item.get("cell_id") == "package-concurrent" for item in package_failures)
        and any(item.get("cell_id") == "package-serialized" for item in package_controls)
    ):
        return "PACKAGE_CONCURRENCY_SENSITIVE"

    if {item["revision_label"] for item in behavior_failures} != {"pr438", "planning-main"}:
        return "REVISION_SPECIFIC"
    return "INCONCLUSIVE"


def classification_controls() -> dict[str, object]:
    def outcome(revision: str, cell_id: str, passed: bool, signature: str = "none") -> dict[str, object]:
        return {
            "revision_label": revision,
            "cell_id": cell_id,
            "kind": "explicit-order",
            "behavior_passed": passed,
            "first_divergence_signatures": [signature],
        }

    cases = {
        "both_orders_fail": [
            outcome("pr438", "order-timeout-first", False, "missing:a"),
            outcome("pr438", "order-empty-first", False, "missing:b"),
            outcome("planning-main", "order-timeout-first", False, "missing:c"),
            outcome("planning-main", "order-empty-first", False, "missing:d"),
        ],
        "one_order_fails_with_repeated_opposite_controls": [
            outcome("pr438", "order-timeout-first", False, "missing:a"),
            outcome("pr438", "order-empty-first", True),
            outcome("planning-main", "order-timeout-first", False, "missing:a"),
            outcome("planning-main", "order-empty-first", True),
        ],
    }
    expected = {
        "both_orders_fail": "INCONCLUSIVE",
        "one_order_fails_with_repeated_opposite_controls": "ORDER_SENSITIVE",
    }
    actual = {name: classify_outcomes(values, True) for name, values in cases.items()}
    for name, label in expected.items():
        if actual[name] != label:
            raise VerificationError(f"classification control {name} returned {actual[name]}, expected {label}")
    return {"passes": True, "expected": expected, "actual": actual}


def classification() -> dict[str, object]:
    matrix_report = verify_matrix()
    run, run_root = latest_run()
    first = load(EVIDENCE / "first-divergence.json") if (EVIDENCE / "first-divergence.json").is_file() else first_divergence_fileless(run, run_root)
    outcomes: list[dict[str, object]] = []
    for record in records(run):
        process = record.get("process", {})
        events = record_trace(record, run_root)
        failures = [item for item in first.get("cases", []) if item.get("revision_label") == record.get("revision_label") and item.get("cell_id") == record.get("cell", {}).get("id") and item.get("first_divergence") != "none"]
        first_assertion = record.get("selection", {}).get("first_assertion", "") if isinstance(record.get("selection"), dict) else ""
        outcomes.append({
            "revision_label": record.get("revision_label"),
            "revision": record.get("revision"),
            "cell_id": record.get("cell", {}).get("id"),
            "kind": record.get("cell", {}).get("kind"),
            "behavior_exit_code": process.get("exit_code") if isinstance(process, dict) else None,
            "behavior_passed": isinstance(process, dict) and process.get("exit_code") == 0,
            "first_assertion": first_assertion,
            "first_divergence_signatures": [item.get("first_divergence") for item in failures] or ["none"],
            "trace_sha256": record.get("trace_sha256"),
            "selected": record.get("selection", {}).get("required_test_run_counts", {}) if isinstance(record.get("selection"), dict) else {},
            "runner_controls_valid": not process_failure(record),
        })
    runner_valid = all(item["runner_controls_valid"] for item in outcomes)
    label = classify_outcomes(outcomes, runner_valid)
    controls = classification_controls()
    behavior_failures = [item for item in outcomes if item["behavior_passed"] is False]
    result = {
        "schema": "audio-runtime-c52-classification-v1",
        "classification": label,
        "passes": False,
        "within_declared_matrix": True,
        "behavioral_failure_count": len(behavior_failures),
        "trial_numerator_denominator": {
            "all_cells": {"numerator": sum(1 for item in outcomes if item["behavior_passed"]), "denominator": len(outcomes)},
            "pr438": {"numerator": sum(1 for item in outcomes if item["revision_label"] == "pr438" and item["behavior_passed"]), "denominator": sum(1 for item in outcomes if item["revision_label"] == "pr438")},
            "planning_main": {"numerator": sum(1 for item in outcomes if item["revision_label"] == "planning-main" and item["behavior_passed"]), "denominator": sum(1 for item in outcomes if item["revision_label"] == "planning-main")},
        },
        "outcomes": outcomes,
        "alternative_labels_rejected": {
            "DETERMINISTIC": "requires repeated same-revision failures with one first divergence",
            "ORDER_SENSITIVE": "requires order-confined failures and repeated opposite-order negative controls",
            "PACKAGE_CONCURRENCY_SENSITIVE": "requires concurrent-only failures with serialized controls",
            "REVISION_SPECIFIC": "requires an outcome difference under identical controls",
            "INCONCLUSIVE": "reserved for invalid controls or ambiguous mixed outcomes",
        },
        "confidence_limits": [
            "The matrix is bounded to the two clean archives, fixed counts, fixed scheduling variants, and the available Darwin toolchain.",
            "A clean matrix supports NON_REPRODUCED within bounds, not proof that the hosted CI observation was flaky or absent.",
            "The primary 10/10 report remains separately reported because its raw command and environment are unavailable.",
        ],
        "source_observation": "PR438 run 34562579355/job 103148160889 selected provider_timeout and reported a non-nil silent_provider_timeout room error at browser_parity_test.go:220; this is not inferred as causal.",
        "matrix_report": matrix_report,
        "classification_controls": controls,
    }
    write(EVIDENCE / "classification.json", result)
    return {"mode": "classification", "passes": True, "classification": label, "behavioral_failure_count": len(behavior_failures)}


def first_divergence_fileless(run: dict[str, object], run_root: Path) -> dict[str, object]:
    # This path is only used when a caller asks for classification directly.
    first_divergence()
    value = load(EVIDENCE / "first-divergence.json")
    if not isinstance(value, dict):
        raise VerificationError("generated first-divergence report is invalid")
    return value


def causal_map() -> dict[str, object]:
    classification_value = load(EVIDENCE / "classification.json")
    first = load(EVIDENCE / "first-divergence.json")
    if not isinstance(classification_value, dict) or not isinstance(first, dict):
        raise VerificationError("classification/first-divergence evidence is missing")
    label = classification_value.get("classification")
    if label == "NON_REPRODUCED":
        result = {
            "schema": "audio-runtime-c52-causal-finding-map-v1",
            "status": "NON_REPRODUCED",
            "repair": None,
            "first_divergence": first.get("overall"),
            "active_owner_intersections": [],
            "next_observable_trigger": {
                "condition": "The next exact hermetic lifecycle-package failure for TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout under captured runner image/environment/package scheduling.",
                "action": "Invoke the retained C52 overlay and checkpoint schema before any source repair; preserve the complete child log, selected-test JSON actions, typed liveness event, peer-cancel boundary, result/error, and group-cleanup evidence.",
                "owner": "Primary/meta-planner routes the newly observed exact source/API divergence after checkpoint capture; C52 makes no production ownership claim.",
            },
            "limitations": [
                "No defect absence is claimed outside the declared bounded matrix.",
                "No CI flakiness or hosted scheduling cause is claimed from the local result.",
                "No production or tracked test file was changed by this slice.",
            ],
        }
    else:
        reproduced = [item for item in first.get("cases", []) if item.get("first_divergence") != "none"]
        result = {
            "schema": "audio-runtime-c52-causal-finding-map-v1",
            "status": "REPRODUCED_BUT_REPAIR_UNIMPLEMENTED",
            "first_divergence": reproduced,
            "source_anchors": {
                "pr438": ["go-agent-runtime/services/rooms/internal/lifecycle/events.go:eventObserver", "go-agent-runtime/services/rooms/internal/lifecycle/state.go:noteTerminal/failParticipant/waitParticipant", "go-agent-runtime/services/rooms/internal/lifecycle/runner.go:finishRun/finalizeRun"],
                "planning_main": ["go-agent-runtime/services/rooms/internal/lifecycle/events.go:eventObserver", "go-agent-runtime/services/rooms/internal/lifecycle/state.go:noteTerminal/failParticipant/waitParticipant", "go-agent-runtime/services/rooms/internal/lifecycle/runner.go:finishRun/finalizeRun"],
            },
            "api_owner": "Not assigned by C52; primary must reconcile the exact first divergence against active leases before authorizing a repair.",
            "proposed_exclusive_owned_paths": [],
            "active_owner_intersections": ["Deferred until the primary confirms the exact source/API boundary; no directory-wide lease is proposed."],
            "positive_reproduction": reproduced,
            "causal_negative_control": "The opposite explicit order, serialized package setting, and identical comparison revision controls remain required before assigning sensitivity.",
            "expected_repaired_checkpoint": "The first divergent checkpoint identified above must change while later result and cleanup controls remain asserted.",
            "repair_implemented": False,
        }
    write(EVIDENCE / "causal-finding-map.json", result)
    return {"mode": "causal-finding-map", "passes": True, "status": result["status"]}


def all_modes() -> list[dict[str, object]]:
    reports = [verify_provenance(), verify_matrix(), verify_overlay_integrity(), verify_checkpoints(), first_divergence(), verify_cleanup(), verify_no_retry_and_selection(), classification(), causal_map()]
    return reports


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["provenance", "matrix", "overlay-integrity", "checkpoints", "first-divergence", "cleanup", "no-retry-and-selection", "classification", "causal-finding-map", "all"], required=True)
    args = parser.parse_args()
    try:
        if args.mode == "provenance":
            report = verify_provenance()
        elif args.mode == "matrix":
            report = verify_matrix()
        elif args.mode == "overlay-integrity":
            report = verify_overlay_integrity()
        elif args.mode == "checkpoints":
            report = verify_checkpoints()
        elif args.mode == "first-divergence":
            report = first_divergence()
        elif args.mode == "cleanup":
            report = verify_cleanup()
        elif args.mode == "no-retry-and-selection":
            report = verify_no_retry_and_selection()
        elif args.mode == "classification":
            report = classification()
        elif args.mode == "causal-finding-map":
            report = causal_map()
        else:
            report = all_modes()
        print(json.dumps({"status": "PASS", "report": report}, sort_keys=True))
        return 0
    except (VerificationError, KeyError, TypeError, ValueError) as exc:
        print(json.dumps({"status": "FAIL", "error": str(exc)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
