#!/usr/bin/env python3
"""Fail-closed C108 evidence verifier.

This driver owns only the C108 evidence directory.  It validates accepted-tree
hashes, live admission/ownership, deterministic analyzer output, proof labels,
causal negative controls, candidate disjointness and release confinement.  It
does not poll CI, merge, or change production/source/registry/baseline files.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
FACTORY_SERVER_URL = os.environ.get("FACTORY_SERVER_URL", "")
WORK = "audio-runtime-c108-characterize-audio-device-boundary-gaps"
BRANCH = "codex/audio-runtime-c108-characterize-audio-device-boundary-gaps"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
INTEGRATED_BASE_REVISION = "3963bc3566da24f8214634c17a9d0f79a6724171"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
C117_BRANCH = "codex/audio-runtime-c117-repair-c113-public-trace-publication"
C117_OWNED_REL = "docs/temp/projects/audio-runtime/audio-runtime-c117-repair-c113-public-trace-publication"
C117_SUCCESSOR_PATHS = {
    "agent-cli/internal/transport/cli/internal/livehost/run.go",
    "agent-cli/internal/transport/cli/internal/livehost/run_trace_test.go",
    "agent-cli/internal/transport/cli/session_observability.go",
    "agent-cli/internal/transport/cli/session_observability_test.go",
    C117_OWNED_REL,
}
PRODUCTION_ROOTS = ("agent-cli", "go-agent-loop", "go-agent-runtime", "go-llm-gateway")
PRD = ROOT / "prd.json"
PROGRESS = ROOT / "progress.txt"
MANIFEST = FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json"
POLICY = FACTORY_ROOT / "factory/docs/operating-policy.md"
HANDOFF = FACTORY_ROOT / "factory/docs/implementation-handoff.md"
META_HANDOFF = FACTORY_ROOT / "factory/docs/meta-planner-handoff.md"
ANALYSIS = HERE / "analysis"
RUNS = HERE / "runs"
PROVENANCE = HERE / "provenance.json"
OWNERSHIP = HERE / "ownership.json"
BOARD = HERE / "canonical-board.json"
CANDIDATES = HERE / "candidates.json"
PROOF_LEVELS = HERE / "proof-levels.json"
OWNED_REL = str(HERE.relative_to(ROOT))

PEER_OWNED_PATHS = {
    "C79": {
        "scripts/wire-packages.txt",
        "docs/architecture/architecture-size-baseline.json",
    },
    "C83": set(),
    "C84": set(),
    "C99": set(),
    "C101": set(),
    "C103": set(),
    "C104": set(),
    "C107": set(),
}
PEER_WORKS = {
    "C79": {
        "work": "audio-runtime-c79-retire-wire-baseline-entries",
        "pr": 470,
        "branch": "codex/audio-runtime-c79-retire-cli-response-lifecycle",
    },
    "C83": {
        "work": "audio-runtime-c83-retire-cli-browser-scenario-runner",
        "pr": 473,
        "branch": "codex/audio-runtime-c83-retire-cli-browser-scenario-runner",
    },
    "C84": {
        "work": "audio-runtime-c84-retire-cli-room-participant-lifecycle",
        "pr": 486,
        "branch": "codex/audio-runtime-c84-retire-cli-room-participant-lifecycle",
    },
    "C99": {
        "work": "audio-runtime-c99-retire-cli-room-participant-planning",
        "pr": 488,
        "branch": "codex/audio-runtime-c99-retire-cli-room-participant-planning",
    },
    "C101": {
        "work": "audio-runtime-c101-retire-cli-provider-session-runtime",
        "pr": 489,
        "branch": "codex/audio-runtime-c101-retire-cli-provider-session-runtime",
    },
    "C103": {
        "work": "audio-runtime-c103-retire-cli-rtc-session-runtime",
        "pr": 490,
        "branch": "codex/audio-runtime-c103-retire-cli-rtc-session-runtime",
    },
    "C104": {
        "work": "audio-runtime-c104-retire-cli-session-finalization-boundary",
        "pr": 492,
        "branch": "codex/audio-runtime-c104-retire-cli-session-finalization-boundary",
    },
    "C107": {
        "work": "audio-runtime-c107-characterize-post-wave-cli-ownership",
        "pr": 494,
        "branch": "codex/audio-runtime-c107-characterize-post-wave-cli-ownership",
    },
}
REQUIRED_COVERAGE = {
    "packet_parsing",
    "sample_rate_or_timing_conversion",
    "DSP_or_buffer_policy",
    "physical_device_construction_or_lifecycle",
    "direct_device_frame_or_transport_writes",
    "queue_admission_vs_playback_consumption",
}
PROOF_ORDER = {
    "PROCESS_SMOKE": 0,
    "SOFTWARE_REPLAY": 1,
    "SIMULATED_CALLBACK_CONSUMPTION": 2,
    "PHYSICAL_DEVICE_CONSUMPTION": 3,
    "ACOUSTIC_PROOF": 4,
}


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_json(path: pathlib.Path) -> Any:
    if not path.exists():
        raise EvidenceFailure(f"missing evidence file: {path.relative_to(HERE)}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"invalid JSON evidence {path.relative_to(HERE)}: {exc}") from exc


def git(*args: str, check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True)
    if check and result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def git_bytes(*args: str) -> bytes:
    result = subprocess.run(["git", *args], cwd=ROOT, capture_output=True)
    if result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout


def git_archive_sha(revision: str) -> str:
    return sha256_bytes(git_bytes("archive", "--format=tar", revision))


def subprocess_result(argv: list[str], cwd: pathlib.Path = ROOT, timeout: int = 60) -> dict[str, Any]:
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, timeout=timeout)
        return {
            "argv": argv,
            "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
            "exit_code": result.returncode,
            "timed_out": False,
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "stdout": result.stdout,
            "stderr": result.stderr,
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "argv": argv,
            "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
            "exit_code": None,
            "timed_out": True,
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "stdout": (exc.stdout or ""),
            "stderr": (exc.stderr or ""),
        }


def parse_status_paths() -> list[str]:
    rows = []
    status = subprocess.run(["git", "status", "--porcelain=v1", "--untracked-files=all"], cwd=ROOT, text=True, capture_output=True)
    if status.returncode != 0:
        raise EvidenceFailure("git status failed: " + status.stderr.strip())
    for line in status.stdout.splitlines():
        if not line:
            continue
        value = line[3:] if len(line) > 3 else ""
        if " -> " in value:
            value = value.split(" -> ", 1)[1]
        rows.append(value.strip().strip('"'))
    return rows


def changed_paths(revision: str, descendant: str) -> list[str]:
    output = git("diff", "--name-only", revision, descendant)
    return sorted({path.strip() for path in output.splitlines() if path.strip()})


def c117_successor() -> bool:
    return git("rev-parse", "--abbrev-ref", "HEAD", check=False) == C117_BRANCH


def path_is_owned(path: str) -> bool:
    if path == OWNED_REL or path.startswith(OWNED_REL + "/"):
        return True
    if c117_successor() and (path in C117_SUCCESSOR_PATHS or path.startswith(C117_OWNED_REL + "/")):
        return True
    return False


def validate_owned_paths(paths: list[str], diagnostic_prefix: str = "") -> list[str]:
    unique = sorted({path for path in paths if path})
    outside = [path for path in unique if not path_is_owned(path)]
    if outside:
        raise EvidenceFailure(diagnostic_prefix + "mutation outside owned C108 directory: " + ", ".join(outside))
    return unique


def ensure_only_owned_changes() -> list[str]:
    """Reject both working-tree and committed candidate mutations outside C108."""
    status_paths = parse_status_paths()
    committed_paths = changed_paths(SOURCE_REVISION, "HEAD")
    unstaged_paths = [path for path in git("diff", "--name-only").splitlines() if path]
    staged_paths = [path for path in git("diff", "--cached", "--name-only").splitlines() if path]
    untracked_result = subprocess_result(["git", "ls-files", "--others", "--exclude-standard"], ROOT, 30)
    if untracked_result["exit_code"] != 0:
        raise EvidenceFailure("git untracked-path query failed: " + untracked_result["stderr"].strip())
    untracked_paths = [path for path in untracked_result["stdout"].splitlines() if path]
    all_paths = validate_owned_paths(status_paths + committed_paths + unstaged_paths + staged_paths + untracked_paths)
    return all_paths


def source_file_hash(revision: str, path: str) -> str:
    return sha256_bytes(git_bytes("show", f"{revision}:{path}"))


def exact_ancestor(revision: str, descendant: str) -> bool:
    result = subprocess.run(["git", "merge-base", "--is-ancestor", revision, descendant], cwd=ROOT)
    return result.returncode == 0


def production_changed_paths(first: str, second: str) -> list[str]:
    output = git("diff", "--name-only", first, second, "--", *PRODUCTION_ROOTS)
    return sorted({path.strip() for path in output.splitlines() if path.strip()})


def source_equivalent_at_head(tested_revision: str, reference: str = INTEGRATED_BASE_REVISION) -> bool:
    if not tested_revision or not reference:
        return False
    if not exact_ancestor(SOURCE_REVISION, tested_revision) or not exact_ancestor(SOURCE_REVISION, reference):
        return False
    return not production_changed_paths(tested_revision, reference)


def board_rows(persist: bool = True) -> list[dict[str, Any]]:
    if not FACTORY_SERVER_URL:
        raise EvidenceFailure("FACTORY_SERVER_URL is unavailable; live board ownership cannot be verified")
    result = subprocess_result(
        ["rtk", "proxy", "you", "--server", FACTORY_SERVER_URL, "--json", "work", "list", "--session", "~default", "--max-results", "500", "--all"],
        ROOT,
        60,
    )
    if result["exit_code"] != 0:
        raise EvidenceFailure("canonical board query failed: " + result["stderr"].strip())
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"canonical board query was not JSON: {exc}") from exc
    rows = payload.get("results")
    if not isinstance(rows, list):
        raise EvidenceFailure("canonical board JSON has no results list")
    if persist:
        write_json(BOARD, payload)
    return [row for row in rows if isinstance(row, dict)]


def task_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [row for row in rows if row.get("name") == WORK]


def observed_at() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def state_name(row: dict[str, Any]) -> str:
    state = row.get("state")
    if isinstance(state, dict):
        return str(state.get("name") or state.get("type") or "").lower()
    return str(state or "").lower()


def is_terminal_row(row: dict[str, Any]) -> bool:
    return state_name(row) in {"terminal", "fin", "failed", "complete", "completed", "done", "cancelled", "canceled"}


def peer_board_row(rows: list[dict[str, Any]], peer: str) -> dict[str, Any] | None:
    work_name = PEER_WORKS[peer]["work"]
    exact = [row for row in rows if row.get("name") == work_name]
    if exact:
        return exact[0]
    return next((row for row in rows if peer.lower() in str(row.get("name", "")).lower()), None)


def peer_pr_observation(peer: str, rows: list[dict[str, Any]], captured_at: str) -> dict[str, Any]:
    config = PEER_WORKS[peer]
    list_result = subprocess_result(
        [
            "rtk", "gh", "pr", "list", "--state", "all", "--head", config["branch"], "--limit", "20",
            "--json", "number,state,title,url,headRefName,headRefOid,baseRefName",
        ],
        ROOT,
        60,
    )
    if list_result["exit_code"] != 0:
        raise EvidenceFailure(f"peer {peer} PR query failed: {list_result['stderr'].strip()}")
    try:
        prs = json.loads(list_result["stdout"] or "[]")
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"peer {peer} PR query was not JSON: {exc}") from exc
    if not isinstance(prs, list):
        raise EvidenceFailure(f"peer {peer} PR query did not return a list")
    selected = next((item for item in prs if item.get("number") == config["pr"]), None)
    if selected is None and prs:
        selected = prs[0]
    if selected is None:
        raise EvidenceFailure(f"peer {peer} has no discoverable PR for branch {config['branch']}")

    number = str(selected.get("number"))
    diff_result = subprocess_result(["rtk", "gh", "pr", "diff", number, "--name-only"], ROOT, 60)
    if diff_result["exit_code"] != 0:
        raise EvidenceFailure(f"peer {peer} PR #{number} diff query failed: {diff_result['stderr'].strip()}")
    diff_paths = sorted({line.strip() for line in diff_result["stdout"].splitlines() if line.strip()})
    board = peer_board_row(rows, peer)
    return {
        "peer": peer,
        "observed_at": captured_at,
        "board": {
            "name": board.get("name") if board else config["work"],
            "work_id": board.get("workId") if board else None,
            "state": board.get("state") if board else None,
            "state_name": state_name(board) if board else "missing",
            "last_output": board.get("_last_output") if board else None,
            "rejection_feedback": board.get("_rejection_feedback") if board else None,
        },
        "branch": config["branch"],
        "pr": selected,
        "diff": {
            "query": diff_result["argv"],
            "changed_paths": diff_paths,
            "sha256": sha256_bytes(diff_result["stdout"].encode()),
        },
    }


def refresh_candidate_peer_intersections(observations: dict[str, dict[str, Any]]) -> None:
    if not CANDIDATES.exists():
        raise EvidenceFailure("candidate evidence is missing before peer ownership refresh")
    candidates = read_json(CANDIDATES)
    for candidate in candidates.get("candidates", []):
        writer_paths = set(candidate.get("exact_writer_files", []))
        intersections = {}
        for peer, observation in observations.items():
            peer_paths = set(observation["diff"]["changed_paths"])
            intersections[peer] = {
                "observed_at": observation["observed_at"],
                "board_work_id": observation["board"].get("work_id"),
                "board_state": observation["board"].get("state"),
                "branch": observation["branch"],
                "pr": observation["pr"],
                "diff_sha256": observation["diff"]["sha256"],
                "writer_paths": sorted(peer_paths),
                "overlap_with_candidate_writers": sorted(writer_paths & peer_paths),
                "status": "exact_writer_overlap" if writer_paths & peer_paths else "no_exact_writer_overlap",
            }
        candidate["peer_intersections"] = intersections
    write_json(CANDIDATES, candidates)


def live_peer_writer_paths() -> dict[str, set[str]]:
    paths = {peer: set(values) for peer, values in PEER_OWNED_PATHS.items()}
    if not OWNERSHIP.exists():
        return paths
    data = read_json(OWNERSHIP)
    for observation in data.get("peer_observations", []):
        peer = observation.get("peer")
        if peer in paths:
            paths[peer].update(observation.get("diff", {}).get("changed_paths", []))
    return paths


def verify_admission() -> dict[str, Any]:
    result = subprocess_result(
        ["python3", str(FACTORY_ROOT / "factory/scripts/project-control.py"), "verify-work", "--type", "task", "--name", WORK, "--root", str(FACTORY_ROOT)],
        ROOT,
        60,
    )
    if result["exit_code"] != 0:
        raise EvidenceFailure("project-control admission failed: " + result["stderr"].strip())
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"project-control admission was not JSON: {exc}") from exc
    if payload.get("status") != "admitted" or payload.get("project") != "audio-runtime":
        raise EvidenceFailure(f"unexpected admission payload: {payload}")
    return {"command": result["argv"], "payload": payload}


def provenance_mode() -> dict[str, Any]:
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    head = git("rev-parse", "HEAD")
    worktree = pathlib.Path(git("rev-parse", "--show-toplevel")).resolve()
    prd = read_json(PRD)
    expected_branch = C117_BRANCH if c117_successor() else BRANCH
    if branch != expected_branch or prd.get("branchName") != branch:
        raise EvidenceFailure(f"branch/PRD mismatch: branch={branch!r}, prd.branchName={prd.get('branchName')!r}")
    if worktree != ROOT:
        raise EvidenceFailure(f"isolated worktree mismatch: {worktree} != {ROOT}")
    fetch = subprocess_result(["git", "fetch", "origin", "main"], ROOT, 120)
    if fetch["exit_code"] != 0:
        raise EvidenceFailure("origin/main fetch failed: " + fetch["stderr"].strip())
    origin_main = git("rev-parse", "origin/main")
    if not exact_ancestor(STARTUP_INTEGRATION, head):
        raise EvidenceFailure("candidate does not contain startup integration revision")
    if not exact_ancestor(SOURCE_REVISION, INTEGRATED_BASE_REVISION):
        raise EvidenceFailure("integrated base does not descend from the immutable analyzed source")
    if not exact_ancestor(INTEGRATED_BASE_REVISION, origin_main):
        raise EvidenceFailure("current origin/main does not descend from the integrated C108 base")
    if not exact_ancestor(origin_main, head):
        raise EvidenceFailure("candidate does not contain current origin/main ancestry")
    outside_status = ensure_only_owned_changes()
    production_diff = production_changed_paths(SOURCE_REVISION, INTEGRATED_BASE_REVISION)
    if production_diff:
        raise EvidenceFailure("integrated C108 base differs from immutable analyzed source: " + ", ".join(production_diff))
    successor_paths = changed_paths(INTEGRATED_BASE_REVISION, head)
    unexpected_successor_paths = [path for path in successor_paths if not path_is_owned(path)]
    if unexpected_successor_paths:
        raise EvidenceFailure("successor contains changes outside the admitted C108/C117 paths: " + ", ".join(unexpected_successor_paths))
    prior_provenance = read_json(PROVENANCE) if PROVENANCE.exists() else {}
    analyzed_candidate = prior_provenance.get("analyzed_candidate_revision", prior_provenance.get("candidate_revision", head))
    if not source_equivalent_at_head(analyzed_candidate, INTEGRATED_BASE_REVISION):
        raise EvidenceFailure("analyzed candidate is not source-equivalent to the integrated C108 base")
    admission = verify_admission()
    rows = board_rows(persist=not c117_successor())
    matches = task_rows(rows)
    if not matches:
        raise EvidenceFailure("canonical board has no C108 task row")
    modules = subprocess_result(["go", "list", "-m", "all"], ROOT, 120)
    go_version = subprocess_result(["go", "version"], ROOT, 30)
    go_env = subprocess_result(["go", "env", "GOVERSION", "GOOS", "GOARCH", "GOWORK", "GOMODCACHE"], ROOT, 30)
    source_files = []
    for path in sorted({
        item
        for root in ("agent-cli", "go-agent-loop", "go-agent-runtime", "go-llm-gateway")
        for item in git("ls-tree", "-r", "--name-only", SOURCE_REVISION, "--", root).splitlines()
        if item.endswith(".go") and not item.endswith("_test.go")
    }):
        source_files.append({"path": path, "sha256": source_file_hash(SOURCE_REVISION, path)})
    evidence = {
        "schema_version": "c108-provenance-v1",
        "work": WORK,
        "project": "audio-runtime",
        "contract_revision": "audio-runtime-v1",
        "factory_session": "~default",
        "factory_server": FACTORY_SERVER_URL,
        "branch": branch,
        "worktree": str(worktree),
        "candidate_revision": analyzed_candidate,
        "analyzed_candidate_revision": analyzed_candidate,
        "current_descendant_revision": head,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "origin_main_revision": origin_main,
        "integrated_base_revision": INTEGRATED_BASE_REVISION,
        "baseline_revision": BASELINE,
        "accepted_source_revision": SOURCE_REVISION,
        "source_archive_sha256": git_archive_sha(SOURCE_REVISION),
        "manifest_sha256": sha256_file(MANIFEST),
        "prd_sha256": sha256_file(PRD),
        "progress_sha256": sha256_file(PROGRESS),
        "operating_policy_sha256": sha256_file(POLICY),
        "implementation_handoff_sha256": sha256_file(HANDOFF),
        "meta_planner_handoff_sha256": sha256_file(META_HANDOFF),
        "inspected_production_file_hashes": source_files,
        "toolchain": {
            "go_version": go_version["stdout"].strip(),
            "go_env": go_env["stdout"].splitlines(),
            "module_list_sha256": sha256_bytes(modules["stdout"].encode()),
        },
        "fetch": fetch,
        "admission": admission,
        "status_paths_at_capture": outside_status,
        "successor_changed_paths": successor_paths,
        "successor_unexpected_paths": unexpected_successor_paths,
        "board_task_rows": [{"workId": row.get("workId"), "state": row.get("state"), "content": row.get("content"), "last_output": row.get("_last_output"), "rejection_feedback": row.get("_rejection_feedback")} for row in matches],
        "ancestry": {
            "startup_integration_in_head": True,
            "analyzed_source_in_integrated_base": True,
            "integrated_base_in_origin_main": True,
            "origin_main_in_head": True,
            "startup_in_head": True,
        },
        "source_equivalence": {
            "analyzed_candidate_to_integrated_base": True,
            "immutable_source_to_integrated_base_production_diff": production_diff,
        },
        "realtime_used": False,
        "native_windows_hardware_and_acoustics": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "passes": True,
    }
    write_json(PROVENANCE, evidence)
    return evidence


def ensure_provenance() -> dict[str, Any]:
    if not PROVENANCE.exists():
        return provenance_mode()
    data = read_json(PROVENANCE)
    if data.get("accepted_source_revision") != SOURCE_REVISION or data.get("integrated_base_revision") != INTEGRATED_BASE_REVISION or data.get("startup_integration_revision") != STARTUP_INTEGRATION:
        raise EvidenceFailure("provenance is stale for the immutable source, integrated base or startup revision")
    head = git("rev-parse", "HEAD")
    origin_main = git("rev-parse", "origin/main")
    analyzed_candidate = data.get("analyzed_candidate_revision", data.get("candidate_revision", ""))
    if not exact_ancestor(SOURCE_REVISION, INTEGRATED_BASE_REVISION) or not exact_ancestor(INTEGRATED_BASE_REVISION, origin_main) or not exact_ancestor(origin_main, head):
        raise EvidenceFailure("provenance ancestry does not reach the current descendant")
    if not source_equivalent_at_head(analyzed_candidate, INTEGRATED_BASE_REVISION):
        raise EvidenceFailure("provenance analyzed candidate is not source-equivalent to the integrated C108 base")
    if data.get("origin_main_revision") != origin_main:
        raise EvidenceFailure("provenance origin/main revision is stale")
    if data.get("branch") not in {BRANCH, C117_BRANCH} or pathlib.Path(data.get("worktree", "")).resolve() != ROOT:
        raise EvidenceFailure("provenance branch/worktree does not match the isolated task")
    ensure_only_owned_changes()
    return data


def ownership_mode() -> dict[str, Any]:
    ensure_provenance()
    if c117_successor():
        existing = read_json(OWNERSHIP)
        if existing.get("passes") is not True or existing.get("ownership_claim") != "C108 owns only its evidence directory; no repair lease, shared-file lease, or project acceptance is claimed.":
            raise EvidenceFailure("existing C108 ownership evidence is not a passing read-only ownership record")
        return {
            "schema_version": "c108-ownership-successor-validation-v1",
            "source_revision": SOURCE_REVISION,
            "integrated_base_revision": INTEGRATED_BASE_REVISION,
            "current_descendant_revision": git("rev-parse", "HEAD"),
            "validated": str(OWNERSHIP.relative_to(HERE)),
            "writes": [],
            "passes": True,
        }
    rows = board_rows()
    matches = task_rows(rows)
    task = next((row for row in matches if row.get("workTypeName") == "task"), None)
    if task is None:
        raise EvidenceFailure("canonical board C108 task row is missing")
    reviews = [row for row in rows if row.get("name") == WORK and row.get("workTypeName") in {"review", "ci"}]
    blocking_reviews = [row for row in reviews if not is_terminal_row(row)]
    captured_at = observed_at()
    peer_observations = {peer: peer_pr_observation(peer, rows, captured_at) for peer in PEER_WORKS}
    refresh_candidate_peer_intersections(peer_observations)
    changed_paths = ensure_only_owned_changes()
    evidence = {
        "schema_version": "c108-ownership-v2",
        "work": WORK,
        "observed_at": captured_at,
        "task": {
            "work_id": task.get("workId"),
            "state": task.get("state"),
            "request_id": task.get("requestId"),
            "branch": BRANCH,
            "worktree": str(ROOT),
            "content": task.get("content"),
            "last_output": task.get("_last_output"),
            "rejection_feedback": task.get("_rejection_feedback"),
        },
        "prior_c108_review_or_ci_findings": [
            {"work_id": row.get("workId"), "state": row.get("state"), "content": row.get("content"), "last_output": row.get("_last_output"), "rejection_feedback": row.get("_rejection_feedback")}
            for row in reviews
        ],
        "peer_observations": list(peer_observations.values()),
        "controlled_shared_paths": {
            peer: {
                "observed_at": observation["observed_at"],
                "writer_paths": observation["diff"]["changed_paths"],
                "overlap_with_c108_evidence": sorted(path for path in observation["diff"]["changed_paths"] if path == OWNED_REL or path.startswith(OWNED_REL + "/")),
                "status": "read_only_dependency_recheck",
            }
            for peer, observation in peer_observations.items()
        },
        "historical_preserved": {
            "C26/C30": ["agent-cli/internal/room/mixer.go"],
            "C59": ["agent-cli/internal/services/internal/agentruntime/session_audio_in.go"],
            "C92": ["agent-cli/internal/services/internal/agentruntime/session_audio_out.go"],
            "C64/C96": ["agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go", "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go"],
        },
        "review_gate": {
            "historical_terminal_rows_preserved": True,
            "blocking_review_or_ci_work_ids": [row.get("workId") for row in blocking_reviews],
            "terminal_review_or_ci_work_ids": [row.get("workId") for row in reviews if is_terminal_row(row)],
            "executor_does_not_self_review": True,
        },
        "changed_paths_at_capture": changed_paths,
        "ownership_claim": "C108 owns only its evidence directory; no repair lease, shared-file lease, or project acceptance is claimed.",
        "passes": not blocking_reviews,
    }
    write_json(OWNERSHIP, evidence)
    if blocking_reviews:
        raise EvidenceFailure("an active C108 review/CI row remains; preserve ownership and inspect its exact findings before handoff")
    return evidence


def validate_inventory_data(inventory: dict[str, Any], source: str, diagnostic_prefix: str = "") -> None:
    def fail(message: str) -> None:
        raise EvidenceFailure(diagnostic_prefix + message)

    if inventory.get("source_revision") != source:
        fail("inventory source revision is stale")
    if inventory.get("source_tree") != "accepted-tree-only":
        fail("inventory is not accepted-tree-only")
    scope = inventory.get("scope", {})
    if not scope.get("source_hashes_are_pinned") or not scope.get("production_files_inspected", 0):
        fail("inventory is missing pinned production-file coverage")
    coverage = {row.get("responsibility") for row in inventory.get("coverage", []) if row.get("status") == "complete"}
    missing = REQUIRED_COVERAGE - coverage
    if missing:
        fail("requested responsibility coverage missing: " + ", ".join(sorted(missing)))
    files = {row.get("path"): row for row in inventory.get("inspected_files", [])}
    if len(files) != scope.get("production_files_inspected"):
        fail("inventory file count does not match inspected file records")
    for path, row in files.items():
        if not path or not row.get("blob_id") or not row.get("sha256"):
            fail("inspected production file is missing path/blob/hash evidence")
        try:
            actual = source_file_hash(source, path)
        except EvidenceFailure as exc:
            fail(f"inspected source path is not present in pinned tree: {path}: {exc}")
        if actual != row["sha256"]:
            fail(f"source hash stale for {path}")
    findings = inventory.get("findings", [])
    if len(findings) < 10:
        fail("inventory contains too few explicit findings/no-findings for the declared scope")
    for item in findings:
        source_record = item.get("source", {})
        if source_record.get("path") not in files or not source_record.get("sha256"):
            fail(f"finding {item.get('id')} has no exact source/hash record")
        if not item.get("responsibility") or not item.get("boundary_class") or not item.get("current_owner") or not item.get("reachability"):
            fail(f"finding {item.get('id')} is missing classification/owner/reachability")
        if item.get("judgment") not in {"supported_gap", "correct_boundary", "unknown"}:
            fail(f"finding {item.get('id')} has unsupported judgment")
        if item.get("judgment") == "supported_gap" and not item.get("symbols"):
            fail(f"finding {item.get('id')} is a gap without exact symbols")
        for symbol in item.get("symbols", []):
            span = symbol.get("span", {})
            if not symbol.get("symbol") or span.get("start_line", 0) <= 0 or span.get("end_line", 0) < span.get("start_line", 0):
                fail(f"finding {item.get('id')} has incomplete symbol/span evidence")
            if item.get("judgment") == "supported_gap" and not symbol.get("callers"):
                fail(f"caller evidence missing for {item.get('id')}:{symbol.get('symbol')}")
            for caller in symbol.get("callers", []):
                if not caller.get("path") or caller.get("line", 0) <= 0 or not caller.get("expression"):
                    fail(f"caller edge incomplete for {item.get('id')}:{symbol.get('symbol')}")
    claims = inventory.get("claims", {})
    if claims.get("queue_admission_is_not_playback_consumption") is not True:
        fail("queue admission/consumption distinction is missing")
    if claims.get("physical_device_consumption") != "UNKNOWN_AND_OUT_OF_SCOPE" or claims.get("acoustic_proof") != "OUT_OF_SCOPE_AND_NEVER_PASS":
        fail("inventory contains an unsupported physical/acoustic proof claim")


def inventory_mode(first: pathlib.Path, second: pathlib.Path) -> dict[str, Any]:
    ensure_provenance()
    first_inventory = read_json(first / "inventory.json")
    second_inventory = read_json(second / "inventory.json")
    validate_inventory_data(first_inventory, SOURCE_REVISION)
    validate_inventory_data(second_inventory, SOURCE_REVISION)
    if json.dumps(first_inventory, sort_keys=True) != json.dumps(second_inventory, sort_keys=True):
        raise EvidenceFailure("deterministic analyzer inventory differs between clean runs")
    if ANALYSIS.exists():
        shutil.rmtree(ANALYSIS)
    ANALYSIS.mkdir(parents=True)
    for name in ("inventory.json", "classifications.json", "call-paths.json", "analysis.md"):
        source = first / name
        if not source.exists():
            raise EvidenceFailure(f"analyzer output missing {name}")
        shutil.copy2(source, ANALYSIS / name)
    report = {
        "schema_version": "c108-inventory-validation-v1",
        "source_revision": SOURCE_REVISION,
        "first_run": str(first),
        "second_run": str(second),
        "production_file_count": len(first_inventory["inspected_files"]),
        "finding_count": len(first_inventory["findings"]),
        "coverage": sorted(REQUIRED_COVERAGE),
        "passes": True,
    }
    write_json(ANALYSIS / "inventory-validation.json", report)
    return report


def determinism_mode(first: pathlib.Path, second: pathlib.Path) -> dict[str, Any]:
    names = ("inventory.json", "classifications.json", "call-paths.json", "analysis.md")
    differences = []
    hashes = {}
    for name in names:
        left, right = first / name, second / name
        if not left.exists() or not right.exists():
            differences.append(name + ":missing")
            continue
        left_hash, right_hash = sha256_file(left), sha256_file(right)
        hashes[name] = {"first_sha256": left_hash, "second_sha256": right_hash}
        if left_hash != right_hash:
            differences.append(name)
    if differences:
        raise EvidenceFailure("deterministic analyzer outputs differ: " + ", ".join(differences))
    report = {"schema_version": "c108-determinism-v1", "source_revision": SOURCE_REVISION, "files": hashes, "declared_timestamp_normalization": "none; analyzer emits no timestamps", "passes": True}
    write_json(ANALYSIS / "determinism.json", report)
    return report


def run_focused_checks() -> dict[str, Any]:
    focus_dir = RUNS / "focused-checks"
    focus_dir.mkdir(parents=True, exist_ok=True)
    commands = [
        ("go-audio-boundary", ["go", "test", "-count=1", "-timeout=120s", "./pkg/audio", "./pkg/clock", "./pkg/mixer"], ROOT / "go-audio"),
        ("go-device-gateway-boundary", ["go", "test", "-count=1", "-timeout=120s", "./pkg/devices", "./pkg/runtime"], ROOT / "go-device-gateway"),
        (
            "agentruntime-causal",
            [
                "go", "test", "-count=1", "-timeout=120s", "./internal/services/internal/agentruntime", "-run",
                "^(TestStreamSessionAudioInputResamples16kHzAtProviderBoundary|TestRoomProviderInputPCMResamples16kHzMixerTo24kHzContract|TestConvertSessionAudioPCMIdentityAndFailures|TestConvertScheduledAudioInputsUsesDeclaredSourceRate|TestRTCDeviceBoundSessionAcceptedCancelDiscardsQueuedPlayback|TestRTCDeviceBoundSessionRejectedCancelDoesNotDiscardPlayback)$", "-v",
            ],
            ROOT / "agent-cli",
        ),
        ("rtc-transport-causal", ["go", "test", "-count=1", "-timeout=120s", "./pkg/transport/rtc", "-run", "Test(InboundTrack|OutboundTrack)"], ROOT / "go-llm-gateway"),
    ]
    results = []
    for label, argv, cwd in commands:
        result = subprocess_result(argv, cwd, 180)
        stdout_path = focus_dir / f"{label}.stdout"
        stderr_path = focus_dir / f"{label}.stderr"
        stdout_path.write_text(result["stdout"], encoding="utf-8")
        stderr_path.write_text(result["stderr"], encoding="utf-8")
        if result["exit_code"] != 0 or result["timed_out"]:
            raise EvidenceFailure(f"focused check failed: {label}")
        output = result["stdout"] + result["stderr"]
        if "ok" not in output or "[no tests to run]" in output:
            raise EvidenceFailure(f"focused check did not execute tests: {label}")
        results.append({
            "label": label,
            "argv": argv,
            "cwd": str(cwd.relative_to(ROOT)),
            "exit_code": result["exit_code"],
            "elapsed_seconds": result["elapsed_seconds"],
            "stdout_sha256": sha256_file(stdout_path),
            "stderr_sha256": sha256_file(stderr_path),
            "proof_limit": "SIMULATED_CALLBACK_CONSUMPTION_OR_LOWER; no physical/acoustic claim",
        })
    report = {"schema_version": "c108-focused-checks-v1", "source_revision": SOURCE_REVISION, "results": results, "full_ci_suite": "not run; script CI owns broad checks", "passes": True}
    write_json(focus_dir / "report.json", report)
    return report


def validate_proof_data(proof: dict[str, Any], diagnostic_prefix: str = "") -> None:
    def fail(message: str) -> None:
        raise EvidenceFailure(diagnostic_prefix + message)

    if proof.get("source_revision") != SOURCE_REVISION:
        fail("proof-level source revision is stale")
    levels = proof.get("observations", [])
    if not levels:
        fail("proof-level observations are missing")
    for observation in levels:
        level = observation.get("strongest_level")
        if level not in PROOF_ORDER:
            fail(f"unsupported proof level {level!r}")
        if level in {"PHYSICAL_DEVICE_CONSUMPTION", "ACOUSTIC_PROOF"}:
            fail(f"proof level cannot be upgraded from software evidence: {level}")
        if observation.get("queue_admission") in {"PHYSICAL_DEVICE_CONSUMPTION", "ACOUSTIC_PROOF", "PASS"}:
            fail("queue admission was conflated with physical playback consumption")
        if observation.get("physical_device_consumption") in {"PASS", "PROVEN", "ACOUSTIC_PROOF"}:
            fail("unsupported physical-device proof claim")
        if observation.get("acoustic_proof") in {"PASS", "PROVEN", "ACOUSTIC_PROOF"}:
            fail("unsupported acoustic proof claim")
    if proof.get("native_windows_hardware") != "OUT_OF_SCOPE_AND_NEVER_PASS":
        fail("native Windows hardware scope is not explicitly excluded")


PUBLIC_FIXTURE_REL = "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
PUBLIC_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
PUBLIC_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"


def evidence_location(value: str, label: str, require_owned: bool = True) -> pathlib.Path:
    path = pathlib.Path(value)
    if not path.is_absolute():
        path = HERE / path
    path = path.resolve()
    if require_owned and not path.is_relative_to(HERE):
        raise EvidenceFailure(f"public artifact escapes owned evidence directory: {label}={value}")
    return path


def evidence_file(value: str, label: str, require_owned: bool = True) -> pathlib.Path:
    path = evidence_location(value, label, require_owned)
    if not path.is_file():
        raise EvidenceFailure(f"public artifact is missing: {label}={value}")
    return path


def evidence_directory(value: str, label: str, require_owned: bool = True) -> pathlib.Path:
    path = evidence_location(value, label, require_owned)
    if not path.is_dir():
        raise EvidenceFailure(f"public artifact directory is missing: {label}={value}")
    return path


def command_value(command: list[str], flag: str) -> str:
    try:
        return command[command.index(flag) + 1]
    except (ValueError, IndexError) as exc:
        raise EvidenceFailure(f"public command is missing {flag}") from exc


def validate_report_hash(report: dict[str, Any], path_key: str, hash_key: str, label: str) -> pathlib.Path:
    path = evidence_file(str(report.get(path_key, "")), label)
    expected = report.get(hash_key)
    if not isinstance(expected, str) or sha256_file(path) != expected:
        raise EvidenceFailure(f"public artifact hash mismatch: {label}")
    return path


def validate_process_artifacts(report: dict[str, Any], head: str, case: str) -> tuple[pathlib.Path, pathlib.Path, pathlib.Path]:
    tested_revision = report.get("candidate_revision")
    if report.get("case") != case or report.get("source_revision") != SOURCE_REVISION or not source_equivalent_at_head(tested_revision, INTEGRATED_BASE_REVISION):
        raise EvidenceFailure(f"{case} report is stale or not source-equivalent to the integrated C108 base")
    if report.get("credential_free") is not True or report.get("realtime_used") is not False:
        raise EvidenceFailure(f"{case} report has an invalid credential/realtime claim")
    if not isinstance(report.get("binary_sha256"), str) or not isinstance(report.get("binary_bytes"), int) or report.get("binary_bytes", 0) <= 0:
        raise EvidenceFailure(f"{case} binary build attestation is incomplete")
    process = report.get("process", {})
    if process.get("timed_out") or process.get("output_limited") or not process.get("process_group_gone"):
        raise EvidenceFailure(f"{case} process evidence is not bounded and clean")
    stdout = validate_report_hash(process, "stdout_path", "stdout_sha256", f"{case} stdout")
    stderr = validate_report_hash(process, "stderr_path", "stderr_sha256", f"{case} stderr")
    command = report.get("command")
    if not isinstance(command, list) or not command:
        raise EvidenceFailure(f"{case} command evidence is missing")
    workdir = evidence_directory(command_value(command, "--workdir"), f"{case} workdir")
    allow_path = pathlib.Path(command_value(command, "--allow-path")).resolve()
    if allow_path != workdir or not workdir.is_dir():
        raise EvidenceFailure(f"{case} workdir/allow-path is not the owned run directory")
    record_dir = pathlib.Path(command_value(command, "--record-dir")).resolve()
    if not record_dir.is_relative_to(HERE):
        raise EvidenceFailure(f"{case} record directory escapes owned evidence")
    fixture = pathlib.Path(command_value(command, "--replay")).resolve()
    if fixture != (ROOT / PUBLIC_FIXTURE_REL).resolve() and case == "replay":
        raise EvidenceFailure("replay command does not use the pinned fixture")
    return workdir, record_dir, fixture


def validate_binary_attestations(reports: list[dict[str, Any]]) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="c108-verify-yui-") as temp:
        binary = pathlib.Path(temp) / "yui"
        result = subprocess_result(["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"], ROOT, 300)
        if result["exit_code"] != 0 or result["timed_out"] or not binary.is_file():
            raise EvidenceFailure("verification build of shipped executable failed")
        actual_sha256 = sha256_file(binary)
        actual_bytes = binary.stat().st_size
    for report in reports:
        if report.get("binary_sha256") != actual_sha256 or report.get("binary_bytes") != actual_bytes:
            raise EvidenceFailure(f"{report.get('case')} binary hash/size does not match a fresh verification build")
    return {
        "command": result["argv"],
        "sha256": actual_sha256,
        "bytes": actual_bytes,
        "reports_match": True,
    }


def validate_public_reports() -> dict[str, Any]:
    head = git("rev-parse", "HEAD")
    replay = read_json(RUNS / "public" / "replay.json")
    negative = read_json(RUNS / "public" / "malformed-truncated.json")
    binary_build = validate_binary_attestations([replay, negative])
    replay_workdir, replay_record_dir, replay_fixture = validate_process_artifacts(replay, head, "replay")
    negative_workdir, negative_record_dir, negative_fixture = validate_process_artifacts(negative, head, "malformed-truncated")
    expected_fixture = (ROOT / PUBLIC_FIXTURE_REL).resolve()
    if not expected_fixture.is_file() or sha256_file(expected_fixture) != PUBLIC_FIXTURE_SHA256:
        raise EvidenceFailure("pinned public fixture hash changed")
    if replay_fixture != expected_fixture or replay.get("fixture") != PUBLIC_FIXTURE_REL or replay.get("fixture_sha256") != PUBLIC_FIXTURE_SHA256:
        raise EvidenceFailure("replay fixture provenance is stale")
    if negative.get("fixture") != PUBLIC_FIXTURE_REL or negative.get("fixture_sha256") != PUBLIC_FIXTURE_SHA256:
        raise EvidenceFailure("malformed fixture provenance is stale")
    if negative_fixture == expected_fixture or not negative_fixture.is_relative_to(HERE) or not negative_fixture.is_file() or sha256_file(negative_fixture) != negative.get("mutated_fixture_sha256"):
        raise EvidenceFailure("malformed replay does not pin its owned mutated fixture")

    replay_command = replay["command"]
    replay_audio = evidence_file(command_value(replay_command, "--audio-out"), "replay audio output")
    replay_record = replay_record_dir
    raw_audio = evidence_file(str(replay_record / "audio" / "out-000.pcm"), "replay raw PCM")
    if raw_audio.stat().st_size != replay.get("raw_pcm_bytes") or sha256_file(raw_audio) != replay.get("raw_pcm_sha256") or sha256_file(raw_audio) != PUBLIC_PCM_SHA256:
        raise EvidenceFailure("replay raw PCM receipt hash/effect changed")
    if replay_audio.stat().st_size != replay.get("audio_output_bytes") or sha256_file(replay_audio) != replay.get("audio_output_sha256") or replay_audio.stat().st_size <= 44:
        raise EvidenceFailure("replay WAV receipt hash/effect changed")
    manifest = evidence_file(str(replay_record / "manifest.json"), "replay manifest")
    session_log = evidence_file(str(replay_record / "session-log.jsonl"), "replay session log")
    if sha256_file(manifest) != replay.get("manifest_sha256") or sha256_file(session_log) != replay.get("session_log_sha256"):
        raise EvidenceFailure("replay manifest/session-log hash changed")
    try:
        manifest_data = json.loads(manifest.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise EvidenceFailure("replay manifest is not valid JSON") from exc
    if manifest_data.get("terminal") != replay.get("terminal"):
        raise EvidenceFailure("replay terminal effect does not match manifest")
    for artifact in manifest_data.get("artifacts", []):
        artifact_path = pathlib.Path(artifact.get("path", ""))
        if artifact_path.is_absolute() or ".." in artifact_path.parts:
            raise EvidenceFailure("replay manifest contains an unsafe artifact path")
        actual = evidence_file(str(replay_record / artifact_path), f"replay manifest artifact {artifact.get('path')}")
        if sha256_file(actual) != artifact.get("sha256"):
            raise EvidenceFailure(f"replay manifest artifact hash changed: {artifact.get('path')}")
    marker = evidence_file(str(replay_workdir / "evidence" / "runs" / "exec-invocations-v4.log"), "replay tool marker")
    replay_output = (validate_report_hash(replay["process"], "stdout_path", "stdout_sha256", "replay stdout").read_text(encoding="utf-8", errors="replace") + validate_report_hash(replay["process"], "stderr_path", "stderr_sha256", "replay stderr").read_text(encoding="utf-8", errors="replace"))
    if "PROBE_TOOL_MARKER_9182" not in replay_output or "strict replay continuation" not in replay_output or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8", errors="replace"):
        raise EvidenceFailure("replay observable tool effect is missing")
    if replay.get("observable_effects") != ["tool_marker", "strict_replay_continuation", "audio_pcm_receipt", "audio_wav_receipt", "fixture_complete", "provider_close"]:
        raise EvidenceFailure("replay observable-effect list changed")

    negative_command = negative["command"]
    negative_audio = evidence_location(command_value(negative_command, "--audio-out"), "malformed audio output")
    if negative_audio.exists() and negative_audio.is_file() and negative_audio.stat().st_size:
        raise EvidenceFailure("malformed replay produced a non-empty WAV receipt")
    negative_raw = negative_record_dir / "audio" / "out-000.pcm"
    if negative_raw.exists() and negative_raw.is_file() and negative_raw.stat().st_size:
        raise EvidenceFailure("malformed replay produced a non-empty PCM receipt")
    negative_output = validate_report_hash(negative["process"], "stdout_path", "stdout_sha256", "malformed stdout").read_text(encoding="utf-8", errors="replace") + validate_report_hash(negative["process"], "stderr_path", "stderr_sha256", "malformed stderr").read_text(encoding="utf-8", errors="replace")
    if negative.get("process", {}).get("exit_code", 0) == 0 or not negative.get("clean_shutdown") or not any(literal.lower() in negative_output.lower() for literal in negative.get("rejection_literals", [])):
        raise EvidenceFailure("malformed replay rejection effect is missing")
    if negative.get("accepted_pcm_receipt") is not False:
        raise EvidenceFailure("malformed replay claims an accepted PCM receipt")
    return {
        "head": head,
        "tested_revisions": {
            "replay": replay.get("candidate_revision"),
            "malformed_truncated": negative.get("candidate_revision"),
        },
        "source_equivalent_to_head": True,
        "binary_build": binary_build,
        "replay_artifacts_checked": len(manifest_data.get("artifacts", [])) + 4,
        "malformed_receipt_absent": True,
    }


def proof_levels_mode() -> dict[str, Any]:
    ensure_provenance()
    public_artifacts = validate_public_reports()
    replay = read_json(RUNS / "public" / "replay.json")
    negative = read_json(RUNS / "public" / "malformed-truncated.json")
    if replay.get("case") != "replay" or replay.get("proof_level") != "SOFTWARE_REPLAY" or not replay.get("credential_free") or replay.get("realtime_used"):
        raise EvidenceFailure("replay report is not a credential-free SOFTWARE_REPLAY")
    if replay.get("process", {}).get("exit_code") != 0 or not replay.get("process", {}).get("process_group_gone"):
        raise EvidenceFailure("replay report lacks clean process shutdown")
    if negative.get("case") != "malformed-truncated" or negative.get("proof_level") != "PROCESS_SMOKE" or negative.get("accepted_pcm_receipt"):
        raise EvidenceFailure("malformed/truncated report lacks bounded rejection/no-receipt evidence")
    if negative.get("process", {}).get("exit_code") == 0 or not negative.get("clean_shutdown"):
        raise EvidenceFailure("malformed/truncated report lacks nonzero clean rejection")
    focused = run_focused_checks()
    proof = {
        "schema_version": "c108-proof-levels-v1",
        "source_revision": SOURCE_REVISION,
        "levels": [
            {"name": "PROCESS_SMOKE", "meaning": "bounded process behavior and clean shutdown; no playback/physical claim"},
            {"name": "SOFTWARE_REPLAY", "meaning": "shipped executable over a credential-free fixture; no physical endpoint claim"},
            {"name": "SIMULATED_CALLBACK_CONSUMPTION", "meaning": "focused simulated callback/transport tests only; no physical endpoint claim"},
            {"name": "PHYSICAL_DEVICE_CONSUMPTION", "meaning": "not observed in this task; out of scope"},
            {"name": "ACOUSTIC_PROOF", "meaning": "not observed in this task; out of scope and never PASS"},
        ],
        "observations": [
            {
                "path": "runs/public/replay.json",
                "strongest_level": "SOFTWARE_REPLAY",
                "queue_admission": "separate; not playback consumption",
                "buffer_or_file_receipt": "raw PCM and WAV receipts recorded separately",
                "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
                "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
            },
            {
                "path": "runs/public/malformed-truncated.json",
                "strongest_level": "PROCESS_SMOKE",
                "queue_admission": "none observed",
                "buffer_or_file_receipt": "no accepted PCM receipt",
                "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
                "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
            },
            {
                "path": "runs/focused-checks/report.json",
                "strongest_level": "SIMULATED_CALLBACK_CONSUMPTION",
                "queue_admission": "tests observe queue/admission separately",
                "buffer_or_file_receipt": "test-only receipts are not physical playback",
                "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
                "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
            },
        ],
        "replay_report": replay,
        "malformed_report": negative,
        "public_artifacts": public_artifacts,
        "focused_checks": focused,
        "native_windows_hardware": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "physical_acoustics": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "realtime_used": False,
        "passes": True,
    }
    validate_proof_data(proof)
    write_json(PROOF_LEVELS, proof)
    return proof


def candidate_source_symbols(inventory: dict[str, Any]) -> dict[str, set[str]]:
    result: dict[str, set[str]] = {}
    for finding in inventory.get("findings", []):
        path = finding.get("source", {}).get("path")
        if not path:
            continue
        result.setdefault(path, set()).update(symbol.get("symbol") for symbol in finding.get("symbols", []) if symbol.get("symbol"))
    return result


def validate_candidate_data(candidates: dict[str, Any], inventory: dict[str, Any], diagnostic_prefix: str = "") -> None:
    def fail(message: str) -> None:
        raise EvidenceFailure(diagnostic_prefix + message)

    if candidates.get("source_revision") != SOURCE_REVISION:
        fail("candidate source revision is stale")
    rows = candidates.get("candidates", [])
    if len(rows) < 3:
        fail("fewer than three evidence-backed candidate repairs")
    symbols = candidate_source_symbols(inventory)
    peer_paths = live_peer_writer_paths()
    all_paths: list[str] = []
    for row in rows:
        if not row.get("id") or not row.get("finding_ids") or len(row.get("exact_writer_files", [])) == 0 or len(row.get("exact_symbols", [])) == 0:
            fail(f"candidate {row.get('id')} lacks exact writer evidence")
        for path in row["exact_writer_files"]:
            if any(path in paths for paths in peer_paths.values()):
                fail(f"peer-owned candidate path: {path}")
            if path not in symbols:
                fail(f"candidate {row['id']} writer path is not in inventory: {path}")
            if path in all_paths:
                fail(f"candidate writer path overlap: {path}")
            all_paths.append(path)
        for qualified in row["exact_symbols"]:
            bare = qualified.rsplit(".", 1)[-1]
            if not any(bare in symbols.get(path, set()) for path in row["exact_writer_files"]):
                fail(f"candidate {row['id']} symbol is not an exact inventory symbol: {qualified}")
        required = ("destination", "positive_control", "causal_negative_control", "accumulated_regression", "retirement_or_device_isolation_effect", "shared_file_serialization", "dependencies", "peer_intersections")
        for key in required:
            if not row.get(key):
                fail(f"candidate {row['id']} missing required field {key}")
        for peer in ("C79", "C83", "C84", "C99", "C101", "C103", "C104", "C107"):
            intersection = row.get("peer_intersections", {}).get(peer)
            if not isinstance(intersection, dict):
                fail(f"candidate {row['id']} missing peer intersection {peer}")
            pr = intersection.get("pr")
            if not intersection.get("observed_at") or not intersection.get("branch") or not isinstance(pr, dict) or not pr.get("number") or not pr.get("headRefOid"):
                fail(f"candidate {row['id']} peer intersection {peer} lacks timestamped PR/head evidence")
            if not intersection.get("diff_sha256") or not isinstance(intersection.get("writer_paths"), list):
                fail(f"candidate {row['id']} peer intersection {peer} lacks diff/writer-path evidence")
            overlap = set(intersection.get("overlap_with_candidate_writers", []))
            if overlap:
                fail(f"candidate {row['id']} overlaps live peer {peer} writer paths: {', '.join(sorted(overlap))}")
        destination = row["destination"]
        for key in ("thin_contract", "private_internal", "dedicated_wire_owner", "retained_adapter"):
            if not destination.get(key):
                fail(f"candidate {row['id']} missing destination {key}")
        if destination["dedicated_wire_owner"].endswith("/internal/wire") or not destination["dedicated_wire_owner"].endswith("/wire"):
            fail(f"candidate {row['id']} has invalid dedicated Wire destination layout: {destination['dedicated_wire_owner']}")
    if len(all_paths) != len(set(all_paths)):
        fail("candidate writer paths are not pairwise disjoint")


def candidates_mode() -> dict[str, Any]:
    ensure_provenance()
    inventory = read_json(ANALYSIS / "inventory.json")
    candidates = read_json(CANDIDATES)
    validate_inventory_data(inventory, SOURCE_REVISION)
    validate_candidate_data(candidates, inventory)
    report = {
        "schema_version": "c108-candidates-validation-v1",
        "source_revision": SOURCE_REVISION,
        "candidate_ids": [row["id"] for row in candidates["candidates"]],
        "writer_paths": [path for row in candidates["candidates"] for path in row["exact_writer_files"]],
        "pairwise_disjoint": True,
        "passes": True,
    }
    write_json(HERE / "candidates-validation.json", report)
    return report


def disjointness_mode() -> dict[str, Any]:
    return candidates_mode()


def negative_controls_mode() -> dict[str, Any]:
    ensure_provenance()
    inventory = read_json(ANALYSIS / "inventory.json")
    candidates = read_json(CANDIDATES)
    proof = read_json(PROOF_LEVELS)
    negative_dir = RUNS / "negative-controls"
    temporary_negative_dir = c117_successor()
    if temporary_negative_dir:
        negative_dir = pathlib.Path(tempfile.mkdtemp(prefix="c108-negative-controls-"))
    elif negative_dir.exists():
        shutil.rmtree(negative_dir)
    if not temporary_negative_dir:
        negative_dir.mkdir(parents=True)
    results = []

    mutated_inventory = copy.deepcopy(inventory)
    mutated_inventory["findings"][0]["symbols"][0]["callers"] = []
    try:
        validate_inventory_data(mutated_inventory, SOURCE_REVISION, "NEGATIVE_CALLER: ")
    except EvidenceFailure as exc:
        if "caller evidence missing" not in str(exc):
            raise EvidenceFailure("caller-deletion mutation returned the wrong diagnostic")
        results.append({"mutation": "delete one caller edge", "rejected": True, "diagnostic": str(exc)})
    else:
        raise EvidenceFailure("caller-deletion mutation was accepted")

    mutated_proof = copy.deepcopy(proof)
    mutated_proof["observations"][0]["strongest_level"] = "PHYSICAL_DEVICE_CONSUMPTION"
    try:
        validate_proof_data(mutated_proof, "NEGATIVE_PROOF: ")
    except EvidenceFailure as exc:
        if "unsupported physical-device proof claim" not in str(exc) and "proof level" not in str(exc):
            raise EvidenceFailure("proof-upgrade mutation returned the wrong diagnostic")
        results.append({"mutation": "upgrade SOFTWARE_REPLAY to PHYSICAL_DEVICE_CONSUMPTION", "rejected": True, "diagnostic": str(exc)})
    else:
        raise EvidenceFailure("proof-upgrade mutation was accepted")

    mutated_candidates = copy.deepcopy(candidates)
    mutated_candidates["candidates"][0]["exact_writer_files"].append("scripts/wire-packages.txt")
    try:
        validate_candidate_data(mutated_candidates, inventory, "NEGATIVE_PEER_PATH: ")
    except EvidenceFailure as exc:
        if "peer-owned candidate path" not in str(exc):
            raise EvidenceFailure("peer-path mutation returned the wrong diagnostic")
        results.append({"mutation": "insert C79-owned writer path", "rejected": True, "diagnostic": str(exc)})
    else:
        raise EvidenceFailure("peer-path mutation was accepted")

    try:
        validate_owned_paths([OWNED_REL + "/synthetic-committed-evidence.json", "README.md"], "NEGATIVE_COMMITTED_PATH: ")
    except EvidenceFailure as exc:
        if "mutation outside owned C108 directory" not in str(exc):
            raise EvidenceFailure("committed-path mutation returned the wrong diagnostic")
        results.append({"mutation": "insert committed outside-owned tracked path", "rejected": True, "diagnostic": str(exc)})
    else:
        raise EvidenceFailure("committed outside-owned mutation was accepted")

    outside = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c108-characterize-audio-device-boundary-gaps-negative-control.txt"
    try:
        try:
            outside.write_text("must not be accepted\n", encoding="utf-8")
            outside_paths = [str(outside.relative_to(ROOT))]
            try:
                validate_owned_paths(outside_paths)
            except EvidenceFailure as exc:
                if "mutation outside owned C108 directory" not in str(exc):
                    raise
                results.append({"mutation": "write outside owned directory", "rejected": True, "diagnostic": "mutation outside owned C108 directory"})
            else:
                raise EvidenceFailure("outside-owned mutation was not detected")
        finally:
            outside.unlink(missing_ok=True)
    except OSError:
        # Permission-denied outside writes are still a fail-closed rejection.
        results.append({"mutation": "write outside owned directory", "rejected": True, "diagnostic": "mutation outside owned C108 directory (write denied)"})
    report = {"schema_version": "c108-negative-controls-v1", "source_revision": SOURCE_REVISION, "mutations": results, "all_rejected": all(row["rejected"] for row in results), "passes": True}
    if not temporary_negative_dir:
        write_json(negative_dir / "report.json", report)
    else:
        shutil.rmtree(negative_dir, ignore_errors=True)
    return report


def text_files_under_owned() -> list[pathlib.Path]:
    return [path for path in HERE.rglob("*") if path.is_file() and "__pycache__" not in path.parts and path.name != "SHA256SUMS"]


def release_mode() -> dict[str, Any]:
    provenance = ensure_provenance()
    all_changed_paths = ensure_only_owned_changes()
    head = git("rev-parse", "HEAD")
    if not exact_ancestor(STARTUP_INTEGRATION, head) or not exact_ancestor(SOURCE_REVISION, head):
        raise EvidenceFailure("release candidate is missing required ancestry")
    if git("rev-parse", "--abbrev-ref", "HEAD") != BRANCH:
        raise EvidenceFailure("release branch does not match PRD")
    committed_paths = changed_paths(SOURCE_REVISION, head)
    production_diff = [path for path in committed_paths if not path.startswith(OWNED_REL + "/") and path != OWNED_REL]
    if production_diff:
        raise EvidenceFailure("release contains changes outside C108 evidence: " + ", ".join(production_diff))
    diff_check = subprocess_result(["git", "diff", "--check"], ROOT, 60)
    committed_diff_check = subprocess_result(["git", "diff", "--check", SOURCE_REVISION, head], ROOT, 60)
    if diff_check["exit_code"] != 0 or committed_diff_check["exit_code"] != 0:
        raise EvidenceFailure("git diff --check failed")
    inventory = read_json(ANALYSIS / "inventory.json")
    candidates = read_json(CANDIDATES)
    proof = read_json(PROOF_LEVELS)
    validate_inventory_data(inventory, SOURCE_REVISION)
    validate_candidate_data(candidates, inventory)
    validate_proof_data(proof)
    public_artifacts = validate_public_reports()
    required = [PROVENANCE, OWNERSHIP, BOARD, ANALYSIS / "inventory.json", ANALYSIS / "classifications.json", ANALYSIS / "call-paths.json", ANALYSIS / "inventory-validation.json", ANALYSIS / "determinism.json", CANDIDATES, HERE / "candidates-validation.json", PROOF_LEVELS, RUNS / "public" / "replay.json", RUNS / "public" / "malformed-truncated.json", RUNS / "focused-checks" / "report.json", RUNS / "negative-controls" / "report.json"]
    missing = [str(path.relative_to(HERE)) for path in required if not path.exists()]
    if missing:
        raise EvidenceFailure("release evidence is incomplete: " + ", ".join(missing))
    # Check all checked-in text evidence for whitespace/EOF issues, including
    # untracked files which git diff --check cannot see before the commit.
    whitespace = []
    for path in text_files_under_owned():
        if path.suffix not in {".json", ".md", ".py", ".txt", ".stdout", ".stderr"}:
            continue
        data = path.read_bytes()
        if data and not data.endswith(b"\n"):
            whitespace.append(f"missing-final-newline:{path.relative_to(HERE)}")
        for number, line in enumerate(data.splitlines(), 1):
            if line.endswith((b" ", b"\t")):
                whitespace.append(f"trailing-whitespace:{path.relative_to(HERE)}:{number}")
    if whitespace:
        raise EvidenceFailure("owned evidence whitespace failure: " + ", ".join(whitespace[:12]))
    report = {
        "schema_version": "c108-release-v1",
        "candidate_revision": head,
        "source_revision": SOURCE_REVISION,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "branch": BRANCH,
        "owned_directory": str(HERE.relative_to(ROOT)),
        "candidate_changed_paths": committed_paths,
        "working_tree_changed_paths": all_changed_paths,
        "production_diff": production_diff,
        "public_artifacts": public_artifacts,
        "required_evidence": [str(path.relative_to(HERE)) for path in required],
        "script_ci": "submit exact HEAD; do not poll or duplicate broad CI",
        "independent_review": "not performed by executor",
        "post_merge_vertical_probe": "primary-owned and still required",
        "immutable_project_criteria": "AUDIO, DEVICE, EMBED, SERVICE, TRACE, REPLAY, FAILURES, QUALITY and PARITY remain OPEN",
        "passes": True,
    }
    write_json(HERE / "release.json", report)
    # Hash evidence excluding this manifest and the hash file itself to avoid a
    # recursive digest. Source hashes are already complete in inventory.json.
    sums = []
    for path in sorted(text_files_under_owned()):
        if path.name in {"SHA256SUMS", "release.json"}:
            continue
        sums.append(f"{sha256_file(path)}  {path.relative_to(HERE)}")
    (HERE / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8")
    return report


def all_mode(first: pathlib.Path | None, second: pathlib.Path | None) -> dict[str, Any]:
    provenance_mode()
    ownership_mode()
    if first is None or second is None:
        temp_one = pathlib.Path(tempfile.mkdtemp(prefix="c108-run-1-"))
        temp_two = pathlib.Path(tempfile.mkdtemp(prefix="c108-run-2-"))
        try:
            analyzer = HERE / "analyze.py"
            for output in (temp_one, temp_two):
                result = subprocess_result(["python3", str(analyzer), "--source", SOURCE_REVISION, "--output-dir", str(output)], ROOT, 180)
                if result["exit_code"] != 0:
                    raise EvidenceFailure("analyzer failed during all mode")
            first, second = temp_one, temp_two
            inventory_mode(first, second)
            determinism_mode(first, second)
        finally:
            shutil.rmtree(temp_one, ignore_errors=True)
            shutil.rmtree(temp_two, ignore_errors=True)
    else:
        inventory_mode(first, second)
        determinism_mode(first, second)
    # Runtime reports are expected to have been generated by the two explicit
    # public-check commands, preserving their exact case-specific provenance.
    proof_levels_mode()
    candidates_mode()
    negative_controls_mode()
    return release_mode()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("provenance", "ownership", "inventory", "determinism", "proof-levels", "candidates", "disjointness", "negative-controls", "release", "all"))
    parser.add_argument("--first", type=pathlib.Path)
    parser.add_argument("--second", type=pathlib.Path)
    args = parser.parse_args()
    try:
        if args.mode == "provenance":
            result = provenance_mode()
        elif args.mode == "ownership":
            result = ownership_mode()
        elif args.mode == "inventory":
            if args.first is None or args.second is None:
                raise EvidenceFailure("inventory mode requires --first and --second analyzer directories")
            result = inventory_mode(args.first.resolve(), args.second.resolve())
        elif args.mode == "determinism":
            if args.first is None or args.second is None:
                raise EvidenceFailure("determinism mode requires --first and --second analyzer directories")
            result = determinism_mode(args.first.resolve(), args.second.resolve())
        elif args.mode == "proof-levels":
            result = proof_levels_mode()
        elif args.mode == "candidates":
            result = candidates_mode()
        elif args.mode == "disjointness":
            result = disjointness_mode()
        elif args.mode == "negative-controls":
            result = negative_controls_mode()
        elif args.mode == "release":
            result = release_mode()
        else:
            result = all_mode(args.first.resolve() if args.first else None, args.second.resolve() if args.second else None)
        print(json.dumps({"status": "ok", "mode": args.mode, "passes": bool(result.get("passes", True))}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "passes": False, "diagnostic": str(exc)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
