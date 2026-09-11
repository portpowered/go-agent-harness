#!/usr/bin/env python3
"""Verify C55 public-boundary, causal-cleanup, scope, and process evidence."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["rtk", "proxy", "git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, check=True, capture_output=True, text=True).stdout.strip()).resolve()
REPORTS = EVIDENCE / "reports"
PROVENANCE = EVIDENCE / "provenance.json"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PRD_CURRENT_MAIN_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
SOURCE_REVISION = "904e1f4c3be6c1e629138632573bd2fb55d50938"
BRANCH = "codex/audio-runtime-c55-room-participant-mesh"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/"
OWNED_CODE = {
    "agent-cli/internal/room/mesh.go",
    "agent-cli/internal/room/mesh_test.go",
    "go-agent-runtime/services/rooms/mesh.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/events.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/browser_parity_test.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/runner.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/runner_test.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/state.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/mesh.go",
    "go-agent-runtime/services/rooms/internal/mesh/mesh.go",
    "go-agent-runtime/services/rooms/wire/mesh.go",
    "coverage-manifest/go-agent-runtime/services/rooms/internal/mesh/package.json",
}
FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
NON_EXECUTION_REPORTS = {"admission-board.json"}


class VerificationFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing evidence: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise VerificationFailure(f"invalid JSON evidence {path}: {error}") from error
    require(isinstance(value, dict), f"evidence is not an object: {path}")
    return value


def git(*args: str) -> str:
    result = subprocess.run(["rtk", "proxy", "git", *args], cwd=REPO_ROOT, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def execution_ok(execution: dict, label: str, expected: int = 0) -> None:
    require(execution.get("returncode") == expected, f"{label} returned {execution.get('returncode')}, want {expected}")
    require(execution.get("timed_out") is False and execution.get("output_bounded") is True and execution.get("disk_bounded") is True, f"{label} exceeded output or time bounds")
    cleanup = execution.get("cleanup", {})
    require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("reader_threads_stopped") is True and cleanup.get("group_survivor_detected") is not True, f"{label} left a live process group or reader")
    require(int(execution.get("elapsed_ms", 10**9)) <= 60000, f"{label} exceeded the 60 second child limit")


def verify_artifact(build: dict, label: str) -> None:
    artifact = build.get("artifact", {})
    require(isinstance(artifact, dict), f"{label} build artifact provenance is missing")
    path = EVIDENCE / str(artifact.get("path", ""))
    require(path.is_file() and not path.is_symlink(), f"{label} build artifact is missing")
    require(artifact.get("bytes") == path.stat().st_size and artifact.get("sha256") == sha256_file(path), f"{label} build artifact hash is stale")
    require(isinstance(artifact.get("build_input_manifest_sha256"), str) and artifact.get("build_input_manifest_sha256"), f"{label} build-input binding is missing")


def verify_external() -> None:
    payload = load(REPORTS / "external-positive.json")
    provenance = load(PROVENANCE)
    require(payload.get("schema") == "audio-runtime.c55.external.v1", "external report schema mismatch")
    require(payload.get("candidate_revision") == provenance.get("candidate_revision"), "external report is not candidate-bound")
    report = payload.get("report", {})
    require(report.get("participants") == ["alpha", "bravo", "charlie", "delta"] and report.get("pair_count") == 6, "external pair oracle changed")
    require(report.get("surviving_pair") is True and report.get("done_after_cleanup") is True, "external survivor/Done oracle changed")
    rollback = report.get("rollback", {})
    require(rollback.get("typed_connect_failure") and rollback.get("no_half_join") and rollback.get("nil_factory_rejected"), "external causal failure oracle is incomplete")
    verify_artifact(payload.get("build", {}), "external")
    executions = payload.get("executions", [])
    require(len(executions) == 3, "external execution matrix is incomplete")
    for execution in executions:
        execution_ok(execution, execution.get("label", "external execution"))


def verify_source_inventory() -> None:
    inventory = load(REPORTS / "source-inventory.json")
    require(inventory.get("schema") == "audio-runtime.c55.source-inventory.v1", "source inventory schema mismatch")
    path = inventory.get("path")
    require(path == "agent-cli/internal/room/mesh.go", "source inventory path mismatch")
    baseline = subprocess.run(["rtk", "proxy", "git", "show", f"{SOURCE_REVISION}:{path}"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout
    current = (REPO_ROOT / path).read_text(encoding="utf-8")
    require(inventory.get("baseline_revision") == SOURCE_REVISION and inventory.get("baseline_line_count") == len(baseline.splitlines()) and inventory.get("final_line_count") == len(current.splitlines()), "source line-count inventory is stale")
    retired = inventory.get("retired", {})
    retained = inventory.get("retained", {})
    require(retired and all(item.get("baseline_present") and not item.get("final_present") for item in retired.values()), "retired CLI declarations remain or were not inventoried")
    require(retained and all(item.get("final_present") for item in retained.values()), "retained CLI adapters are missing or were not inventoried")


def verify_negative() -> None:
    payload = load(REPORTS / "negative-controls.json")
    provenance = load(PROVENANCE)
    require(payload.get("schema") == "audio-runtime.c55.negative.v1" and payload.get("passed") is True, "negative-control report is not passed")
    controls = payload.get("controls", [])
    require({item.get("mutation") for item in controls} == {"mutate-count", "mutate-order", "mutate-cleanup", "mutate-half-join"}, "negative-control matrix is incomplete")
    for item in controls:
        execution = item.get("execution", {})
        execution_ok(execution, item.get("mutation", "negative control"), expected=execution.get("returncode"))
        require(execution.get("returncode") != 0 and item.get("failure_artifact"), f"negative oracle {item.get('mutation')} did not reject with an artifact")
    require(payload.get("candidate_revision") == provenance.get("candidate_revision"), "negative controls are not candidate-bound")


def verify_shipped_room() -> None:
    payload = load(REPORTS / "shipped-yui-room-mesh.json")
    provenance = load(PROVENANCE)
    require(payload.get("schema") == "audio-runtime.c55.shipped-room.v1", "shipped room report schema mismatch")
    require(payload.get("candidate_revision") == provenance.get("candidate_revision"), "shipped room report is not candidate-bound")
    manifest = payload.get("manifest", {})
    require(manifest.get("schema_version") == 1 and len(manifest.get("participants", [])) >= 2, "shipped room manifest is incomplete")
    verify_artifact(payload.get("build", {}), "shipped room")
    for execution in payload.get("executions", []):
        execution_ok(execution, execution.get("label", "shipped room"))
    public = next((item for item in payload.get("executions", []) if item.get("label") == "shipped-room-public-mesh"), None)
    require(public is not None, "shipped room mesh execution is missing")
    try:
        public_report = json.loads(public.get("stdout", "").splitlines()[-1])
    except (IndexError, json.JSONDecodeError) as error:
        raise VerificationFailure(f"shipped room mesh report is invalid: {error}") from error
    require(public_report.get("pair_count") == 6 and public_report.get("surviving_pair") is True and public_report.get("done_after_cleanup") is True, "shipped room mesh effect is missing")


def verify_shipped_replay() -> None:
    payload = load(REPORTS / "shipped-yui-audio-tool-replay.json")
    provenance = load(PROVENANCE)
    require(payload.get("schema") == "audio-runtime.c55.shipped-replay.v1" and payload.get("credentials") == "not_used", "shipped replay report is not credential-free")
    require(payload.get("candidate_revision") == provenance.get("candidate_revision"), "shipped replay report is not candidate-bound")
    replays = payload.get("replays", [])
    require(len(replays) == 1 and replays[0].get("fixture", "").endswith("c16-audio-tool.session.json"), "audio/tool replay fixture is missing")
    replay = replays[0]
    verify_artifact(payload.get("build", {}), "shipped replay")
    require(replay.get("fixture_sha256") == sha256_file(FIXTURE), "frozen audio/tool fixture hash changed")
    require(replay.get("pcm_bytes") == 4800 and replay.get("pcm_sha256") == "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502", "audio/tool replay PCM oracle changed")
    require(replay.get("terminal", {}).get("terminal_provenance") in ("provider", "replay"), "audio/tool terminal provenance is missing")
    require("PROBE_TOOL_MARKER_9182" in replay.get("execution", {}).get("stdout", ""), "audio/tool replay tool effect is missing")
    execution_ok(replay.get("execution", {}), "shipped audio/tool replay")


def verify_build_inputs(provenance: dict) -> None:
    manifest = load(REPORTS / "build-input-manifest.json")
    require(manifest.get("schema") == "audio-runtime.c55.build-inputs.v1", "build-input manifest schema mismatch")
    candidate = provenance.get("candidate_revision")
    require(manifest.get("candidate_revision") == candidate, "build-input manifest is not candidate-bound")
    entries = manifest.get("entries", [])
    require(isinstance(entries, list) and entries, "build-input manifest is empty")
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode()
    require(manifest.get("sha256") == hashlib.sha256(encoded).hexdigest(), "build-input manifest checksum is stale")
    for entry in entries:
        relative = entry.get("path", "")
        path = (REPO_ROOT / relative).resolve()
        require(relative and REPO_ROOT == path or REPO_ROOT in path.parents, f"build input escapes repository: {relative}")
        require(path.is_file() and not path.is_symlink(), f"build input is missing: {relative}")
        require(entry.get("bytes") == path.stat().st_size and entry.get("sha256") == sha256_file(path), f"build input hash is stale: {relative}")
    recorded = provenance.get("build_inputs", {})
    require(recorded.get("content_sha256") == manifest.get("sha256") and recorded.get("file_count") == len(entries), "provenance build-input binding is stale")


def verify_scope() -> None:
    mesh = (REPO_ROOT / "go-agent-runtime/services/rooms/internal/mesh/mesh.go").read_text(encoding="utf-8")
    public = (REPO_ROOT / "go-agent-runtime/services/rooms/mesh.go").read_text(encoding="utf-8")
    cli = (REPO_ROOT / "agent-cli/internal/room/mesh.go").read_text(encoding="utf-8")
    wire = (REPO_ROOT / "go-agent-runtime/services/rooms/wire/mesh.go").read_text(encoding="utf-8")
    require("participants map" not in cli and "pairs map" not in cli and "pendingPair" not in cli and "mutateMu" not in cli, "CLI retained mutable mesh state")
    require("participants map" in mesh and "pairs        map" in mesh and "pending" in mesh and "closing" in mesh and "mutateMu" in mesh, "private mesh does not own all mesh state")
    require("agent-cli" not in public and "rtc" not in public and "flag" not in public and "terminal" not in public.lower(), "public rooms contract leaked host concerns")
    require("internal/mesh" in wire and "func NewMesh" in wire and "PairFactory" in wire, "Wire mesh provider is not explicit")
    require(len(mesh.splitlines()) <= 400, "private mesh exceeds the new-file size budget")
    require(len(public.splitlines()) <= 400 and len(cli.splitlines()) <= 400, "mesh public/adapter file exceeds size budget")
    baseline_wire = subprocess.run(["rtk", "proxy", "git", "show", f"{SOURCE_REVISION}:go-agent-runtime/services/rooms/wire/wire_gen.go"], cwd=REPO_ROOT, check=True, capture_output=True).stdout
    require(hashlib.sha256(baseline_wire).hexdigest() == sha256_file(REPO_ROOT / "go-agent-runtime/services/rooms/wire/wire_gen.go"), "wire_gen.go changed")
    changed = set(git("diff", "--name-only", f"{SOURCE_REVISION}...HEAD").splitlines())
    require(all(path in OWNED_CODE or path.startswith(OWNED_PREFIX) for path in changed), f"candidate changed an unowned path: {sorted(changed - OWNED_CODE)}")


def verify_provenance(final: bool) -> None:
    provenance = load(PROVENANCE)
    require(provenance.get("schema") == "audio-runtime.c55.provenance.v1", "provenance schema mismatch")
    require(provenance.get("project") == "audio-runtime" and provenance.get("task") == "audio-runtime-c55-room-participant-mesh" and provenance.get("contract_revision") == "audio-runtime-v1", "provenance admission identity mismatch")
    require(provenance.get("branch") == BRANCH and git("branch", "--show-current") == BRANCH, "provenance branch mismatch")
    require(provenance.get("source_revision") == SOURCE_REVISION and provenance.get("fetched_main_revision") == SOURCE_REVISION, "fetched main provenance is stale")
    require(provenance.get("origin_main_at_run") == SOURCE_REVISION, "origin/main provenance is stale")
    require(subprocess.run(["rtk", "proxy", "git", "cat-file", "-e", f"{SOURCE_REVISION}^{{commit}}"], cwd=REPO_ROOT).returncode == 0, "captured fetched-main revision is unavailable")
    head = git("rev-parse", "HEAD")
    candidate = provenance.get("candidate_revision")
    require(isinstance(candidate, str) and candidate, "candidate revision is missing")
    for ancestor in (BASELINE_REVISION, INTEGRATION_REVISION, PRD_CURRENT_MAIN_REVISION, candidate):
        require(subprocess.run(["rtk", "proxy", "git", "merge-base", "--is-ancestor", ancestor, head], cwd=REPO_ROOT).returncode == 0, f"missing ancestry for {ancestor}")
    for path, digest in provenance.get("source_hashes", {}).items():
        require((REPO_ROOT / path).is_file() and sha256_file(REPO_ROOT / path) == digest, f"provenance source hash changed: {path}")
    verify_build_inputs(provenance)
    storage = provenance.get("storage", {})
    before = storage.get("before", {})
    after = storage.get("after", {})
    require(int(before.get("free_space_bytes", 0)) > 2 * 1024 * 1024 * 1024 and int(after.get("free_space_bytes", 0)) > 2 * 1024 * 1024 * 1024, "two GiB compilation reserve was not retained")
    require(int(after.get("binary_output_bytes", -1)) >= 0 and int(after.get("report_fixture_bytes", -1)) >= 0 and int(after.get("scratch_bytes", -1)) >= 0, "storage accounting is incomplete")
    candidate_reports = []
    for report_path in REPORTS.glob("*.json"):
        report = load(report_path)
        if "candidate_revision" in report:
            candidate_reports.append(report_path.name)
            require(report.get("candidate_revision") == candidate, f"report is not candidate-bound: {report_path.name}")
    require(len(candidate_reports) >= 5, "candidate evidence report set is incomplete")
    if final:
        require(not git("status", "--porcelain", "--untracked-files=all"), "final candidate tree is dirty")
        changed = set(git("diff", "--name-only", f"{SOURCE_REVISION}...HEAD").splitlines())
        require(all(path in OWNED_CODE or path.startswith(OWNED_PREFIX) for path in changed), "final candidate includes an unowned path")


def verify_resources() -> None:
    executions: list[dict] = []
    for path in REPORTS.glob("*.json"):
        payload = load(path)
        for key in ("executions", "controls"):
            if isinstance(payload.get(key), list):
                executions.extend(item.get("execution", item) for item in payload[key] if isinstance(item, dict))
        if isinstance(payload.get("build"), dict):
            executions.append(payload["build"])
    total_ms = 0
    for execution in executions:
        if "returncode" not in execution:
            continue
        total_ms += int(execution.get("elapsed_ms", 10**9))
        require(int(execution.get("elapsed_ms", 10**9)) <= 60000 and execution.get("output_bounded") is True and execution.get("disk_bounded") is True, f"retained execution exceeds a bound: {execution.get('label')}")
        require(execution.get("cleanup", {}).get("group_alive_after") is False, f"retained execution left a process group: {execution.get('label')}")
    require(total_ms <= 600000, f"retained executions exceed aggregate 600 second budget: {total_ms}ms")
    report_paths = [path for path in REPORTS.glob("*.json") if path.name not in NON_EXECUTION_REPORTS]
    text = "\n".join(path.read_text(encoding="utf-8", errors="replace") for path in [PROVENANCE, *report_paths]).lower()
    for marker in ("sk-", "authorization:", "realtime_api_key"):
        require(marker not in text, f"credential marker retained in evidence: {marker}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("provenance-scope-resources-failures", "final"))
    args = parser.parse_args()
    verify_provenance(args.mode == "final")
    verify_scope()
    verify_external()
    verify_source_inventory()
    verify_negative()
    verify_shipped_room()
    verify_shipped_replay()
    verify_resources()
    print(json.dumps({"schema": "audio-runtime.c55.verification.v1", "passed": True, "mode": args.mode}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationFailure as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
