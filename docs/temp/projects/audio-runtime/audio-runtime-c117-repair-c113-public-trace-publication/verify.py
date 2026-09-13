#!/usr/bin/env python3
"""Fail-closed C117 admission, scope and runtime evidence verifier."""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import subprocess
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
FACTORY_SERVER_URL = os.environ.get("FACTORY_SERVER_URL", "")
WORK = "audio-runtime-c117-repair-c113-public-trace-publication"
BRANCH = "codex/audio-runtime-c117-repair-c113-public-trace-publication"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PLANNING_MAIN = "3963bc3566da24f8214634c17a9d0f79a6724171"
C108_SOURCE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C112_WORK = "audio-runtime-c112-retire-cli-session-trace-evidence"
C112_BRANCH = "codex/audio-runtime-c112-retire-cli-session-trace-evidence"
C112_PR = 498
C79_WORK = "audio-runtime-c79-retire-cli-response-lifecycle"
C79_BRANCH = "codex/audio-runtime-c79-retire-cli-response-lifecycle"
C79_PR = 470
# The fail-closed host tests were initially added in a separate file. Keep the
# deletion in the allowed diff while recording their consolidation into the
# existing trace test file to satisfy the maintained-package budget.
C117_TEST = "agent-cli/internal/transport/cli/internal/livehost/trace_test.go"
C117_LEGACY_TEST = "agent-cli/internal/transport/cli/internal/livehost/run_trace_test.go"
C117_REL = str(HERE.relative_to(ROOT))
C108_REL = "docs/temp/projects/audio-runtime/audio-runtime-c108-characterize-audio-device-boundary-gaps"
C108_PROVENANCE = f"{C108_REL}/provenance.json"
C108_VERIFIER = f"{C108_REL}/verify.py"
C108_SUMS = f"{C108_REL}/SHA256SUMS"
REPRODUCTION = HERE / "runs" / "c113-reproduction.json"
PRE_TEST_REPORT = HERE / "runs" / "pre-c112-test.json"
ADMISSION_REPORT = HERE / "admission.json"
CENSUS_REPORT = HERE / "census.json"
SCOPE_REPORT = HERE / "pre-c112-scope.json"
C108_SCOPE_REPORT = HERE / "c108-source-base-and-checksums.json"
MANIFEST = FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json"


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise EvidenceFailure(f"missing evidence: {path.relative_to(HERE)}") from exc
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"invalid JSON evidence {path.relative_to(HERE)}: {exc}") from exc


def command_result(argv: list[str], cwd: pathlib.Path = ROOT, timeout: int = 60) -> dict[str, Any]:
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, timeout=timeout)
        return {
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": result.returncode,
            "timed_out": False,
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "stdout": result.stdout,
            "stderr": result.stderr,
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": None,
            "timed_out": True,
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "stdout": exc.stdout or "",
            "stderr": exc.stderr or "",
        }


def git(*args: str) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True)
    if result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def git_file_bytes(revision: str, path: str) -> bytes | None:
    result = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=ROOT, capture_output=True)
    if result.returncode != 0:
        return None
    return result.stdout


def exact_ancestor(revision: str, descendant: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", revision, descendant], cwd=ROOT).returncode == 0


def changed_paths(revision: str, descendant: str) -> list[str]:
    output = git("diff", "--name-only", revision, descendant)
    return sorted({line.strip() for line in output.splitlines() if line.strip()})


def status_paths() -> list[str]:
    result = subprocess.run(["git", "status", "--porcelain=v1", "--untracked-files=all"], cwd=ROOT, text=True, capture_output=True)
    if result.returncode != 0:
        raise EvidenceFailure("git status failed: " + result.stderr.strip())
    paths = []
    for line in result.stdout.splitlines():
        if len(line) < 4:
            continue
        value = line[3:]
        if " -> " in value:
            value = value.split(" -> ", 1)[1]
        paths.append(value.strip().strip('"'))
    return sorted(set(paths))


def validate_scope(paths: list[str], allowed: set[str]) -> None:
    outside = [path for path in sorted(set(paths)) if path not in allowed and not path.startswith(C117_REL + "/")]
    if outside:
        raise EvidenceFailure("pre-C112 mutation outside owned C117 paths: " + ", ".join(outside))


def read_factory_manifest() -> dict[str, Any]:
    try:
        return json.loads(MANIFEST.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"factory manifest unavailable or invalid: {MANIFEST}") from exc


def verify_admission() -> dict[str, Any]:
    result = command_result(
        [
            "python3",
            str(FACTORY_ROOT / "factory/scripts/project-control.py"),
            "verify-work",
            "--type",
            "task",
            "--name",
            WORK,
            "--root",
            str(FACTORY_ROOT),
        ],
        ROOT,
        60,
    )
    if result["exit_code"] != 0:
        raise EvidenceFailure("project-control admission failed: " + result["stderr"].strip())
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"project-control admission was not JSON: {exc}") from exc
    if payload.get("status") != "admitted" or payload.get("project") != PROJECT:
        raise EvidenceFailure(f"unexpected admission payload: {payload}")
    manifest = read_factory_manifest()
    if manifest.get("project") != PROJECT or manifest.get("contractRevision") != CONTRACT or manifest.get("baselineRevision") != BASELINE:
        raise EvidenceFailure("admitted manifest identity or baseline is not exact")
    return {
        "command": result["argv"],
        "payload": payload,
        "manifest": {
            "path": str(MANIFEST),
            "sha256": sha256_file(MANIFEST),
            "project": manifest.get("project"),
            "contractRevision": manifest.get("contractRevision"),
            "baselineRevision": manifest.get("baselineRevision"),
            "sole_project": True,
            "acceptance_waiver": False,
        },
    }


def board_census() -> dict[str, Any]:
    if not FACTORY_SERVER_URL:
        raise EvidenceFailure("FACTORY_SERVER_URL is unavailable")
    result = command_result(
        ["rtk", "proxy", "you", "--server", FACTORY_SERVER_URL, "--json", "work", "list", "--session", "~default", "--max-results", "500", "--all"],
        ROOT,
        60,
    )
    if result["exit_code"] != 0:
        raise EvidenceFailure("canonical board query failed: " + result["stderr"].strip())
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"canonical board was not JSON: {exc}") from exc
    rows = payload.get("results")
    if not isinstance(rows, list):
        raise EvidenceFailure("canonical board has no results list")
    selected = []
    for row in rows:
        name = str(row.get("name", ""))
        if any(token in name.lower() for token in ("c117", "c112", "c79")):
            selected.append({
                "workId": row.get("workId"),
                "name": row.get("name"),
                "workTypeName": row.get("workTypeName"),
                "state": row.get("state"),
                "worktree": row.get("worktree"),
                "branch": row.get("branch"),
                "baseRevision": row.get("baseRevision"),
                "last_output": row.get("_last_output"),
                "rejection_feedback": row.get("_rejection_feedback"),
                "content": row.get("content"),
            })
    required_names = {WORK, C112_WORK, C79_WORK}
    discovered_names = {str(row.get("name")) for row in selected}
    missing = sorted(required_names - discovered_names)
    if missing:
        raise EvidenceFailure("canonical board is missing required ownership rows: " + ", ".join(missing))
    return {
        "command": result["argv"],
        "payload_sha256": sha256_bytes(result["stdout"].encode()),
        "rows": selected,
    }


def pull_pr(pr: int, branch: str) -> dict[str, Any]:
    view = command_result(
        ["rtk", "gh", "pr", "view", str(pr), "--json", "number,state,title,url,headRefName,headRefOid,baseRefName,statusCheckRollup"],
        ROOT,
        60,
    )
    if view["exit_code"] != 0:
        raise EvidenceFailure(f"PR #{pr} query failed: {view['stderr'].strip()}")
    try:
        metadata = json.loads(view["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"PR #{pr} query was not JSON: {exc}") from exc
    if metadata.get("headRefName") != branch:
        raise EvidenceFailure(f"PR #{pr} branch mismatch: {metadata.get('headRefName')!r} != {branch!r}")
    diff = command_result(["rtk", "gh", "pr", "diff", str(pr), "--name-only"], ROOT, 60)
    if diff["exit_code"] != 0:
        raise EvidenceFailure(f"PR #{pr} diff query failed: {diff['stderr'].strip()}")
    paths = sorted({line.strip() for line in diff["stdout"].splitlines() if line.strip()})
    return {
        "metadata": metadata,
        "diff": {"paths": paths, "sha256": sha256_bytes(diff["stdout"].encode()), "command": diff["argv"]},
    }


def source_census() -> dict[str, Any]:
    paths = [
        "agent-cli/internal/transport/cli/internal/livehost/run.go",
        C117_TEST,
        "agent-cli/internal/transport/cli/session_observability.go",
        "agent-cli/internal/transport/cli/session_observability_test.go",
    ]
    source_rows = []
    for path in paths:
        planning = git_file_bytes(PLANNING_MAIN, path)
        current = (ROOT / path).read_bytes() if (ROOT / path).is_file() else None
        source_rows.append({
            "path": path,
            "planning_main_sha256": sha256_bytes(planning) if planning is not None else None,
            "current_sha256": sha256_bytes(current) if current is not None else None,
            "present_at_planning_main": planning is not None,
            "present_current": current is not None,
        })
    c108_paths = [f"{C108_REL}/{name}" for name in ("verify.py", "provenance.json", "SHA256SUMS")]
    c108_rows = [{"path": path, "planning_main_sha256": sha256_bytes(git_file_bytes(PLANNING_MAIN, path) or b"")} for path in c108_paths]
    callers = []
    for needle in ("livehost.Run", "NewSessionCommandWithLive", "TraceAudio", "sessiontrace"):
        result = command_result(["rg", "-n", needle, "agent-cli/internal/transport/cli", "agent-cli/internal/wire", "go-agent-runtime/services", "--glob", "*.go"], ROOT, 30)
        callers.append({"needle": needle, "exit_code": result["exit_code"], "stdout": result["stdout"], "stdout_sha256": sha256_bytes(result["stdout"].encode())})
    return {"host_paths": source_rows, "c108_artifacts_at_planning_main": c108_rows, "caller_searches": callers}


def validate_reproduction() -> dict[str, Any]:
    report = read_json(REPRODUCTION)
    if report.get("source_revision") != PLANNING_MAIN or report.get("artifact_sha256") != "27896a6907f5c8e02e185acf9338f07d38e56ebe21896d0b1a740ecde1f2a20d":
        raise EvidenceFailure("C113 reproduction is not pinned to the immutable artifact/source")
    if report.get("fixture_sha256") != "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169":
        raise EvidenceFailure("C113 reproduction fixture identity is stale")
    if report.get("passes") is not True or report.get("trace", {}).get("present") is not False:
        raise EvidenceFailure("C113 reproduction does not preserve the positive-but-missing-trace defect")
    process = report.get("process", {})
    if process.get("exit_code") != 0 or process.get("timed_out") or process.get("output_limited") or not process.get("process_group_gone"):
        raise EvidenceFailure("C113 reproduction is not a bounded clean success")
    required = ("PROBE_TOOL_MARKER_9182", "strict replay continuation", "nonempty provider PCM", "nonempty output WAV", "five-artifact recording manifest", "fixture_complete", "provider_close")
    if report.get("positive_effects") != list(required):
        raise EvidenceFailure("C113 positive effect list is incomplete")
    recording = report.get("recording", {})
    if len(recording.get("manifest", {}).get("artifacts", [])) != 5:
        raise EvidenceFailure("C113 manifest does not prove five matching artifacts")
    if report.get("audio_output", {}).get("bytes", 0) <= 44:
        raise EvidenceFailure("C113 output WAV is empty")
    return report


def run_expected_red_test() -> dict[str, Any]:
    result = command_result(
        ["go", "test", "./agent-cli/internal/transport/cli/internal/livehost", "-run", "Trace.*(Required|Propagation|FailClosed)", "-count=1", "-timeout=180s"],
        ROOT,
        180,
    )
    output = result["stdout"] + result["stderr"]
    if result["exit_code"] == 0:
        raise EvidenceFailure("pre-C112 fail-closed host test unexpectedly passed")
    if "TestTraceRequiredFailsClosedWithoutPublicService" not in output or "fail-closed public trace-service diagnostic" not in output:
        raise EvidenceFailure("pre-C112 test did not fail at the intended missing-public-service oracle")
    if "undefined:" in output or "cannot find package" in output or "build failed" in output:
        raise EvidenceFailure("pre-C112 test failed to compile instead of exercising the host oracle")
    bounded = {key: value for key, value in result.items() if key not in {"stdout", "stderr"}}
    bounded["stdout"] = result["stdout"][:12000]
    bounded["stderr"] = result["stderr"][:12000]
    bounded["expected_red"] = True
    return bounded


def validate_c117_allowed_paths(paths: list[str]) -> None:
    allowed = {C117_TEST, C117_LEGACY_TEST, C108_PROVENANCE, C108_VERIFIER, C108_SUMS}
    outside = [path for path in sorted(set(paths)) if path not in allowed and not path.startswith(C117_REL + "/")]
    if outside:
        raise EvidenceFailure("C117 candidate changed an unowned path: " + ", ".join(outside))


def c108_source_base_and_checksums() -> dict[str, Any]:
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    head = git("rev-parse", "HEAD")
    worktree = pathlib.Path(git("rev-parse", "--show-toplevel")).resolve()
    if branch != BRANCH or read_json(ROOT / "prd.json").get("branchName") != BRANCH:
        raise EvidenceFailure("C117 branch/PRD identity is not exact")
    if worktree != ROOT:
        raise EvidenceFailure(f"isolated worktree mismatch: {worktree} != {ROOT}")
    origin_main = git("rev-parse", "origin/main")
    for revision in (STARTUP_INTEGRATION, PLANNING_MAIN, origin_main):
        if not exact_ancestor(revision, head):
            raise EvidenceFailure(f"C117 candidate is missing required ancestry {revision}")
    validate_c117_allowed_paths(changed_paths(PLANNING_MAIN, head) + status_paths())
    provenance_path = ROOT / C108_PROVENANCE
    provenance = read_json(provenance_path)
    if provenance.get("accepted_source_revision") != C108_SOURCE or provenance.get("integrated_base_revision") != PLANNING_MAIN:
        raise EvidenceFailure("C108 provenance does not separate immutable source and integrated base")
    if provenance.get("origin_main_revision") != origin_main or provenance.get("startup_integration_revision") != STARTUP_INTEGRATION:
        raise EvidenceFailure("C108 provenance does not pin the fetched current main and startup integration")
    current_descendant = provenance.get("current_descendant_revision")
    if not current_descendant or not exact_ancestor(current_descendant, head):
        raise EvidenceFailure("C108 provenance current descendant is not an ancestor of the candidate")
    if provenance.get("successor_unexpected_paths") != [] or provenance.get("source_equivalence", {}).get("immutable_source_to_integrated_base_production_diff") != []:
        raise EvidenceFailure("C108 provenance records an unexpected source or successor drift")
    if provenance.get("source_equivalence", {}).get("analyzed_candidate_to_integrated_base") is not True:
        raise EvidenceFailure("C108 analyzed candidate lacks explicit integrated-base equivalence")
    checksum = command_result(["shasum", "-a", "256", "-c", "SHA256SUMS"], provenance_path.parent, 120)
    if checksum["exit_code"] != 0 or checksum["timed_out"]:
        raise EvidenceFailure("C108 SHA256SUMS does not exactly validate the retained evidence")
    report = {
        "schema_version": "audio-runtime-c117-c108-source-base-checksums-v1",
        "work": WORK,
        "branch": branch,
        "worktree": str(worktree),
        "head": head,
        "origin_main": origin_main,
        "baseline": BASELINE,
        "startup_integration": STARTUP_INTEGRATION,
        "analyzed_source": C108_SOURCE,
        "integrated_base": PLANNING_MAIN,
        "current_descendant": current_descendant,
        "changed_paths": sorted(set(changed_paths(PLANNING_MAIN, head) + status_paths())),
        "c108_owned_changes": [C108_VERIFIER, C108_PROVENANCE, C108_SUMS],
        "checksum": {key: value for key, value in checksum.items() if key not in {"stdout", "stderr"}},
        "negative_controls": "preserved; validated by the C108 verifier without rewriting C108 evidence on the C117 successor branch",
        "native_windows_hardware_and_acoustics": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "passes": True,
    }
    write_json(C108_SCOPE_REPORT, report)
    return report


def pre_c112_scope() -> dict[str, Any]:
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    head = git("rev-parse", "HEAD")
    worktree = pathlib.Path(git("rev-parse", "--show-toplevel")).resolve()
    prd = read_json(ROOT / "prd.json")
    if branch != BRANCH or prd.get("branchName") != BRANCH:
        raise EvidenceFailure(f"branch/PRD mismatch: {branch!r}/{prd.get('branchName')!r}")
    if worktree != ROOT:
        raise EvidenceFailure(f"isolated worktree mismatch: {worktree} != {ROOT}")
    origin_main = git("rev-parse", "origin/main")
    if origin_main != PLANNING_MAIN:
        raise EvidenceFailure(f"pre-C112 scope requires planning origin/main {PLANNING_MAIN}, found {origin_main}")
    for revision in (STARTUP_INTEGRATION, PLANNING_MAIN):
        if not exact_ancestor(revision, head):
            raise EvidenceFailure(f"pre-C112 candidate is missing required ancestry {revision}")
    allowed = {C117_TEST, C117_LEGACY_TEST, C108_PROVENANCE, C108_VERIFIER, C108_SUMS}
    validate_scope(changed_paths(PLANNING_MAIN, head) + status_paths(), allowed)
    forbidden = [path for path in changed_paths(PLANNING_MAIN, head) + status_paths() if path.startswith("go-agent-runtime/services/sessiontrace/") or path in {"scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"}]
    if forbidden:
        raise EvidenceFailure("pre-C112 scope mutates C112/C79-owned paths: " + ", ".join(sorted(set(forbidden))))
    admission = verify_admission()
    board = board_census()
    c112_pr = pull_pr(C112_PR, C112_BRANCH)
    c79_pr = pull_pr(C79_PR, C79_BRANCH)
    census = {
        "schema_version": "audio-runtime-c117-census-v1",
        "source_revision": PLANNING_MAIN,
        "current_head": head,
        "host_source_and_test_paths": source_census(),
        "c112": {"work": C112_WORK, "branch": C112_BRANCH, "pr": c112_pr},
        "c79": {"work": C79_WORK, "branch": C79_BRANCH, "pr": c79_pr},
        "peer_exclusions": {
            "c112_owned_paths": c112_pr["diff"]["paths"],
            "c79_owned_paths": c79_pr["diff"]["paths"],
            "c112_consumption": "withheld until exact head has green SCRIPT CI, independent approval and guarded merge",
        },
    }
    reproduction = validate_reproduction()
    test = run_expected_red_test()
    write_json(ADMISSION_REPORT, admission)
    write_json(CENSUS_REPORT, {"board": board, **census})
    write_json(PRE_TEST_REPORT, test)
    report = {
        "schema_version": "audio-runtime-c117-pre-c112-scope-v1",
        "work": WORK,
        "project": PROJECT,
        "contract_revision": CONTRACT,
        "branch": branch,
        "worktree": str(worktree),
        "head": head,
        "origin_main": origin_main,
        "baseline": BASELINE,
        "startup_integration": STARTUP_INTEGRATION,
        "planning_main": PLANNING_MAIN,
        "required_ancestry": {revision: True for revision in (STARTUP_INTEGRATION, PLANNING_MAIN)},
        "changed_paths": sorted(set(changed_paths(PLANNING_MAIN, head) + status_paths())),
        "admission": "admitted",
        "c113_reproduction": {"report": str(REPRODUCTION.relative_to(HERE)), "passes": reproduction["passes"], "timeline_present": reproduction["trace"]["present"]},
        "pre_c112_test": {"report": str(PRE_TEST_REPORT.relative_to(HERE)), "expected_red": test["expected_red"]},
        "c112_consumption": "withheld",
        "c79_shared_paths": "unchanged",
        "physical_device_and_acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "passes": True,
    }
    write_json(SCOPE_REPORT, report)
    return report


def main() -> int:
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("pre-c112-scope", "c108-source-base-and-checksums", "final-scope-provenance-and-runtime"))
    args = parser.parse_args()
    try:
        if args.mode == "pre-c112-scope":
            result = pre_c112_scope()
        elif args.mode == "c108-source-base-and-checksums":
            result = c108_source_base_and_checksums()
        else:
            raise EvidenceFailure(f"{args.mode} remains gated on C112 merge and final runtime evidence")
        print(json.dumps({"status": "ok", "mode": args.mode, "passes": bool(result.get("passes"))}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "passes": False, "diagnostic": str(exc)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
