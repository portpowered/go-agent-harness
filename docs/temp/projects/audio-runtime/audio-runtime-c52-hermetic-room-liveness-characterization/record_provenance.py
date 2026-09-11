#!/usr/bin/env python3
"""Record immutable C52 inputs and index the bounded characterization evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from run import apply_declared_environment, hermetic_base_environment


EVIDENCE = Path(__file__).resolve().parent
TASK = "audio-runtime-c52-hermetic-room-liveness-characterization"
PROJECT = "audio-runtime"
CONTRACT_REVISION = "audio-runtime-v1"
BRANCH = "codex/audio-runtime-c52-hermetic-room-liveness-characterization"
PR438 = "823bd350fe5d11782c38bda87d7b7bfd7d89d7cd"
PLANNING_MAIN = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
CI_RUN = "34562579355"
CI_JOB = "103148160889"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/"


def load(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def write(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def rel(path: Path) -> str:
    return path.resolve().relative_to(EVIDENCE.resolve()).as_posix()


def command_result(command: list[str], cwd: Path, env: dict[str, str] | None = None, timeout: float = 60.0) -> dict[str, object]:
    try:
        result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, check=False)
        return {"command": command, "cwd": str(cwd), "exit_code": result.returncode, "stdout": result.stdout, "stderr": result.stderr}
    except subprocess.TimeoutExpired as exc:
        return {
            "command": command,
            "cwd": str(cwd),
            "exit_code": None,
            "stdout": (exc.stdout or "") if isinstance(exc.stdout, str) else "",
            "stderr": (exc.stderr or "") if isinstance(exc.stderr, str) else "",
            "timed_out": True,
        }


def git(repo: Path, *args: str) -> str:
    result = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def ancestor(repo: Path, revision: str, candidate: str) -> bool:
    return subprocess.run(["git", "-C", str(repo), "merge-base", "--is-ancestor", revision, candidate], check=False).returncode == 0


def workspace_inputs(repo: Path) -> list[dict[str, object]]:
    paths = [
        "go.work",
        "agent-cli/go.mod", "agent-cli/go.sum",
        "go-agent-runtime/go.mod", "go-agent-runtime/go.sum",
        "go-agent-loop/go.mod", "go-agent-loop/go.sum",
        "go-audio/go.mod", "go-audio/go.sum",
        "go-device-gateway/go.mod", "go-device-gateway/go.sum",
        "go-llm-gateway/go.mod", "go-llm-gateway/go.sum",
    ]
    result: list[dict[str, object]] = []
    for item in paths:
        path = repo / item
        if not path.is_file():
            raise RuntimeError(f"missing selected workspace input: {item}")
        result.append({"path": item, "bytes": path.stat().st_size, "sha256": sha256(path)})
    return result


def dirty_paths(repo: Path) -> list[str]:
    result = subprocess.run(["git", "-C", str(repo), "status", "--porcelain", "--untracked-files=all"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"git status failed: {result.stderr.strip()}")
    return [line[3:] for line in result.stdout.splitlines() if len(line) >= 4]


def parse_go_env(result: dict[str, object], keys: list[str]) -> dict[str, str]:
    lines = str(result.get("stdout", "")).splitlines()
    return {key: lines[index] if index < len(lines) else "" for index, key in enumerate(keys)}


def tree_bytes(path: Path) -> int:
    if path.is_file():
        return path.stat().st_size
    return sum(item.stat().st_size for item in path.rglob("*") if item.is_file()) if path.is_dir() else 0


def storage_record(run: dict[str, object], archives: dict[str, object]) -> dict[str, object]:
    run_ids = ["20260911T065500Z-dev1", "20260911T070000Z-go1267", "20260911T071000Z-go-auto", "20260911T072000Z-module-root", "20260911T073000Z-overlay-type", "20260911T074500Z-checkpoint-event", str(run.get("run_id"))]
    run_records = []
    for run_id in run_ids:
        path = EVIDENCE / "runs" / run_id
        scratch = path / "scratch"
        cache = path / "cache"
        scratch_content_present = scratch.is_dir() and any(scratch.iterdir())
        record: dict[str, object] = {
            "run_id": run_id,
            "retained_bytes": tree_bytes(path),
            "scratch_content_present": scratch_content_present,
            "cache_present": cache.exists(),
            "cleanup_verified": not scratch_content_present and not cache.exists(),
        }
        run_json = path / "run.json"
        if run_json.is_file():
            run_value = load(run_json)
            if isinstance(run_value, dict) and isinstance(run_value.get("storage"), dict):
                run_storage = run_value["storage"]
                record["storage"] = run_storage
                record["free_space_before_cleanup_bytes"] = run_storage.get("free_space_before_cleanup_bytes")
                record["free_space_after_cleanup_bytes"] = run_storage.get("free_space_after_cleanup_bytes")
                record["archive_reuse"] = run_storage.get("archive_reuse")
        run_records.append(record)
    latest_storage = run.get("storage")
    if not isinstance(latest_storage, dict):
        raise RuntimeError("canonical matrix run has no storage accounting")
    archive_bytes = sum(int(item.get("bytes", 0)) for item in archives.values() if isinstance(item, dict))
    archive_reuse = latest_storage.get("archive_reuse")
    if not isinstance(archive_reuse, dict):
        raise RuntimeError("canonical matrix run has no archive reuse identity")
    return {
        "schema": "audio-runtime-c52-storage-v1",
        "observed_at": datetime.now(timezone.utc).isoformat(),
        "source_staging_bytes": latest_storage.get("source_staging_bytes", archive_bytes),
        "source_archives": archives,
        "binary_output_bytes": latest_storage.get("binary_output_bytes", 0),
        "fixture_report_bytes": sum(int(item["retained_bytes"]) for item in run_records),
        "current_run_fixture_report_bytes": latest_storage.get("fixture_report_bytes"),
        "reproducible_scratch_bytes_before_cleanup": latest_storage.get("reproducible_scratch_bytes_before_cleanup"),
        "cache_bytes_before_cleanup": latest_storage.get("cache_bytes_before_cleanup"),
        "reproducible_scratch_bytes_retained_after_cleanup": latest_storage.get("reproducible_scratch_bytes_retained_after_cleanup"),
        "free_space_before_run_bytes": latest_storage.get("free_space_before_run_bytes"),
        "free_space_before_cleanup_bytes": latest_storage.get("free_space_before_cleanup_bytes"),
        "free_space_after_cleanup_bytes": latest_storage.get("free_space_after_cleanup_bytes"),
        "free_space_limitation": "free space was measured immediately before and after cleanup; background filesystem activity is not controlled",
        "archive_reuse": archive_reuse,
        "reuse_identity": {key: {"revision": value.get("revision"), "content_sha256": value.get("content_sha256"), "retained_content_sha256": value.get("retained_content_sha256"), "fixed_revision_verified": value.get("fixed_revision_verified")} for key, value in archive_reuse.items() if isinstance(value, dict)},
        "runs": run_records,
        "cleanup": {
            "known_generated_cache_and_scratch_removed": all(item["cleanup_verified"] for item in run_records),
            "current_run": latest_storage.get("cleanup"),
            "retained": ["run.json", "cell logs", "traces", "overlay manifests", "negative controls", "source archives", "CI log/metadata"],
        },
    }


def ci_record() -> dict[str, object]:
    log_path = EVIDENCE / "ci/run-34562579355-job-103148160889.log"
    metadata_path = EVIDENCE / "ci/run-34562579355-job-103148160889.json"
    metadata = load(metadata_path)
    if not isinstance(metadata, dict):
        raise RuntimeError("CI metadata must be an object")
    capture = metadata.get("capture", {}) if isinstance(metadata.get("capture"), dict) else {}
    return {
        "run_id": CI_RUN,
        "job_id": CI_JOB,
        "attempt": metadata.get("attempt"),
        "head_sha": metadata.get("headSha"),
        "status": metadata.get("status"),
        "conclusion": metadata.get("conclusion"),
        "created_at": metadata.get("createdAt"),
        "started_at": metadata.get("startedAt"),
        "updated_at": metadata.get("updatedAt"),
        "url": metadata.get("url"),
        "workflow": metadata.get("workflowName"),
        "log_path": rel(log_path),
        "metadata_path": rel(metadata_path),
        "log_bytes": log_path.stat().st_size,
        "log_lines": capture.get("logLines"),
        "log_sha256": sha256(log_path),
        "metadata_sha256": sha256(metadata_path),
        "captured_at": capture.get("capturedAt"),
        "log_command": capture.get("logCommand"),
        "exact_observation": {
            "selected_test": "TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout",
            "source_file": "go-agent-runtime/services/rooms/internal/lifecycle/browser_parity_test.go",
            "line": 220,
            "typed_error": "silent_provider_timeout: provider response produced no observable output",
            "observed": "room run error was non-nil at the test assertion",
            "interpretation": "downstream CI observation only; not a causal attribution",
        },
    }


def evidence_index(provenance: dict[str, object], run: dict[str, object]) -> dict[str, object]:
    files = [
        "README.md", "provenance.json", "storage.json", "matrix.json", "matrix-results.json", "expected-checkpoints.json",
        "first-divergence.json", "classification.json", "causal-finding-map.json",
        "integrity-controls.json",
        "primary-rerun.json", "regressions/summary.json", "regressions/normal.json", "regressions/race.json",
        "ci/run-34562579355-job-103148160889.log",
        "ci/run-34562579355-job-103148160889.json",
    ]
    indexed = []
    for item in files:
        path = EVIDENCE / item
        if path.is_file():
            indexed.append({"path": item, "bytes": path.stat().st_size, "sha256": sha256(path)})
    return {
        "schema": "audio-runtime-c52-evidence-index-v1",
        "project": PROJECT,
        "task": TASK,
        "comparison": {"pr438": PR438, "planning_main": PLANNING_MAIN},
        "prior_ci_observation": provenance["ci_observation"],
        "primary_observation": provenance["primary_observation"],
        "fresh_matrix": {
            "run_id": run.get("run_id"),
            "run_path": f"runs/{run.get('run_id')}/run.json",
            "matrix_results_path": "matrix-results.json",
            "cell_count": run.get("cell_count"),
            "all_cells_passed": all(item.get("status") == "PASS" for item in run.get("cells", []) if isinstance(item, dict)),
            "negative_controls_rejected": run.get("negative_controls", {}).get("all_rejected") if isinstance(run.get("negative_controls"), dict) else False,
        },
        "classification_path": "classification.json",
        "first_divergence_path": "first-divergence.json",
        "causal_map_path": "causal-finding-map.json",
        "indexed_files": indexed,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo-root", type=Path, default=None)
    args = parser.parse_args()
    repo = args.repo_root.resolve() if args.repo_root else Path(git(EVIDENCE, "rev-parse", "--show-toplevel"))
    factory_root = Path(os.environ.get("FACTORY_ROOT", "")).resolve()
    if not factory_root.is_dir():
        raise SystemExit("FACTORY_ROOT is unavailable")
    server = os.environ.get("FACTORY_SERVER_URL", "")
    control = factory_root / "factory/scripts/project-control.py"
    status = command_result([sys.executable, str(control), "status", "--root", str(factory_root)], factory_root)
    verify = command_result([sys.executable, str(control), "verify-work", "--type", "task", "--name", TASK], factory_root)
    try:
        verify_json = json.loads(str(verify.get("stdout", "")))
    except json.JSONDecodeError:
        verify_json = {"raw_stdout": verify.get("stdout", "")}
    if verify.get("exit_code") != 0 or not isinstance(verify_json, dict) or verify_json.get("status") != "admitted":
        raise SystemExit(f"project admission verification failed: {verify_json}")

    matrix_path = EVIDENCE / "matrix.json"
    matrix = load(matrix_path)
    results_path = EVIDENCE / "matrix-results.json"
    run = load(results_path)
    if not isinstance(matrix, dict) or not isinstance(run, dict):
        raise SystemExit("matrix and matrix-results must be objects")
    run_id = str(run.get("run_id", ""))
    run_path = EVIDENCE / "runs" / run_id / "run.json"
    local_regressions_path = EVIDENCE / "regressions/summary.json"
    local_regressions = load(local_regressions_path) if local_regressions_path.is_file() else None
    existing_provenance = load(EVIDENCE / "provenance.json") if (EVIDENCE / "provenance.json").is_file() else {}
    candidate = git(repo, "rev-parse", "HEAD")
    previous_candidate = str(existing_provenance.get("candidate_evidence_parent_revision", "")) if isinstance(existing_provenance, dict) else ""
    previous_source = str(existing_provenance.get("candidate_source_revision", "")) if isinstance(existing_provenance, dict) else ""
    tested_source = previous_source if previous_candidate and previous_candidate == candidate else candidate
    refreshed_main = git(repo, "rev-parse", "refs/remotes/origin/main")
    status_paths = dirty_paths(repo)
    outside_owned = [path for path in status_paths if not path.startswith(OWNED_PREFIX)]
    base = matrix.get("base_environment", {}) if isinstance(matrix.get("base_environment"), dict) else {}
    with tempfile.TemporaryDirectory(prefix="c52-provenance-") as temp_dir:
        go_env = hermetic_base_environment(os.environ, Path(temp_dir))
        apply_declared_environment(go_env, base, "matrix.base_environment", allow_base_tags=True)
        go_env.update({"GOCACHE": str(Path(temp_dir) / "gocache"), "GOMODCACHE": str(Path(temp_dir) / "gomodcache"), "GOTMPDIR": str(Path(temp_dir) / "gotmp"), "GOWORK": ""})
        for key in ("GOCACHE", "GOMODCACHE", "GOTMPDIR"):
            Path(go_env[key]).mkdir(parents=True, exist_ok=True)
        go_version = command_result(["go", "version"], repo, go_env)
        go_keys = ["GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOWORK", "GOMODCACHE", "GOCACHE", "GOTMPDIR", "GOENV", "GOPROXY", "GOSUMDB"]
        go_env_result = command_result(["go", "env", *go_keys], repo, go_env)
    ci = ci_record()
    primary_path = EVIDENCE / "primary-rerun.json"
    primary = load(primary_path)
    if not isinstance(primary, dict):
        raise SystemExit("primary observation must be an object")
    archives = run.get("archives", {}) if isinstance(run.get("archives"), dict) else {}
    source_archives = {"pr438": archives.get("pr438"), "planning_main": archives.get("planning-main")}
    storage = storage_record(run, source_archives)
    write(EVIDENCE / "storage.json", storage)
    runner_files = {
        "matrix_sha256": sha256(matrix_path),
        "run_sha256": sha256(run_path),
        "overlay_sha256": str(run.get("overlay_sha256", "")),
        "verify_sha256": sha256(EVIDENCE / "verify.py"),
        "runner_sha256": sha256(EVIDENCE / "run.py"),
        "provenance_script_sha256": sha256(Path(__file__)),
        "expected_checkpoints_sha256": sha256(EVIDENCE / "expected-checkpoints.json"),
        "capture_ci_sha256": sha256(EVIDENCE / "capture_ci.py"),
        "regressions_script_sha256": sha256(EVIDENCE / "run_regressions.py"),
    }
    integrity_controls_path = EVIDENCE / "integrity-controls.json"
    if integrity_controls_path.is_file():
        runner_files["integrity_controls_sha256"] = sha256(integrity_controls_path)
    ancestry = {
        "baseline": ancestor(repo, BASELINE, candidate),
        "startup_integration": ancestor(repo, STARTUP_INTEGRATION, candidate),
        "planning_main": ancestor(repo, PLANNING_MAIN, candidate),
        "refreshed_origin_main": ancestor(repo, refreshed_main, candidate),
    }
    provenance = {
        "schema": "audio-runtime-c52-provenance-v1",
        "project": PROJECT,
        "task": TASK,
        "contract_revision": CONTRACT_REVISION,
        "factory": {"root": str(factory_root), "server": server, "session": "~default", "status_command": status, "verify_work_command": verify, "verify_work_result": verify_json},
        "branch": {"expected": BRANCH, "actual": git(repo, "branch", "--show-current"), "matches": git(repo, "branch", "--show-current") == BRANCH},
        "worktree": {"root": str(repo), "isolated": ".claude/worktrees/audio-runtime-c52-hermetic-room-liveness-characterization" in str(repo), "prd_branch_name": BRANCH},
        "candidate_source_revision": tested_source,
        "candidate_evidence_parent_revision": candidate,
        "evidence_candidate_revision": candidate,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "planning_main_revision": PLANNING_MAIN,
        "pr438_head_revision": PR438,
        "refreshed_origin_main_revision": refreshed_main,
        "ancestry": ancestry,
        "toolchain": {"go_version_command": go_version, "go_env_command": go_env_result, "go_env": parse_go_env(go_env_result, go_keys), "cpu_count": os.cpu_count()},
        "environment_allowlist": {
            "base": base,
            "cell_overrides": {str(cell.get("id")): cell.get("env", {}) for cell in matrix.get("cells", []) if isinstance(cell, dict)},
            "fixed": {key: go_env[key] for key in ("PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "GOENV", "GOPROXY", "GOPRIVATE", "GONOPROXY", "GONOSUMDB")},
            "runner_injected": ["C52_REVISION", "C52_MATRIX_CELL", "C52_TRACE_PATH", "C52_OVERLAY_HASH", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "GOWORK"],
            "removed_secret_keys": ["OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY"],
            "ambient_environment_forwarded": False,
            "cpu_count": os.cpu_count(),
        },
        "workspace_inputs": workspace_inputs(repo),
        "source_archives": source_archives,
        "ci_observation": ci,
        "primary_observation": primary,
        "runner_inputs": runner_files,
        "no_source_mutation": not outside_owned,
        "source_mutation_audit": {"dirty_paths": status_paths, "outside_owned_paths": outside_owned, "owned_prefix": OWNED_PREFIX, "production_source_unchanged": not outside_owned},
        "matrix_run": {"run_id": run_id, "path": rel(run_path), "matrix_results_sha256": sha256(results_path), "aggregate_deadline_met": run.get("aggregate_deadline_met"), "cell_count": run.get("cell_count"), "negative_controls": run.get("negative_controls")},
        "storage": {"path": rel(EVIDENCE / "storage.json"), "sha256": sha256(EVIDENCE / "storage.json"), "summary": storage},
        "local_regressions": {"path": rel(local_regressions_path), "sha256": sha256(local_regressions_path), "summary": local_regressions} if isinstance(local_regressions, dict) else {"path": rel(local_regressions_path), "available": False},
        "preflight_repairs": [
            {"run_id": "20260911T065500Z-dev1", "reason": "GOTOOLCHAIN=local selected unavailable host Go 1.24.5 for go.work requiring 1.26.7", "behavioral_conclusion": "invalid prerequisite; preserved only"},
            {"run_id": "20260911T070000Z-go1267", "reason": "isolated GOTMPDIR directory was not created by runner", "behavioral_conclusion": "invalid runner setup; preserved only"},
            {"run_id": "20260911T071000Z-go-auto", "reason": "scratch command ran from workspace root instead of go-agent-runtime module root", "behavioral_conclusion": "invalid runner setup; preserved only"},
            {"run_id": "20260911T072000Z-module-root", "reason": "scratch overlay used a named RoomTerminationReason as string", "behavioral_conclusion": "invalid overlay compile; preserved only"},
        ],
    }
    write(EVIDENCE / "provenance.json", provenance)
    write(EVIDENCE / "evidence-index.json", evidence_index(provenance, run))
    print(json.dumps({"status": "PASS", "provenance": "provenance.json", "evidence_index": "evidence-index.json", "candidate_source_revision": tested_source, "evidence_candidate_revision": candidate, "refreshed_origin_main_revision": refreshed_main}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
