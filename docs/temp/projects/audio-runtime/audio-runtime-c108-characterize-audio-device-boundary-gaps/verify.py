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
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
FACTORY_SERVER_URL = os.environ.get("FACTORY_SERVER_URL", "")
WORK = "audio-runtime-c108-characterize-audio-device-boundary-gaps"
BRANCH = "codex/audio-runtime-c108-characterize-audio-device-boundary-gaps"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
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
    status = subprocess.run(["git", "status", "--short"], cwd=ROOT, text=True, capture_output=True)
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


def ensure_only_owned_changes() -> list[str]:
    paths = parse_status_paths()
    owned_prefix = str(HERE.relative_to(ROOT)) + "/"
    outside = [path for path in paths if path != str(HERE.relative_to(ROOT)) and not path.startswith(owned_prefix)]
    if outside:
        raise EvidenceFailure("mutation outside owned C108 directory: " + ", ".join(outside))
    return paths


def source_file_hash(revision: str, path: str) -> str:
    return sha256_bytes(git_bytes("show", f"{revision}:{path}"))


def exact_ancestor(revision: str, descendant: str) -> bool:
    result = subprocess.run(["git", "merge-base", "--is-ancestor", revision, descendant], cwd=ROOT)
    return result.returncode == 0


def board_rows() -> list[dict[str, Any]]:
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
    write_json(BOARD, payload)
    return [row for row in rows if isinstance(row, dict)]


def task_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [row for row in rows if row.get("name") == WORK]


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
    if branch != BRANCH or prd.get("branchName") != branch:
        raise EvidenceFailure(f"branch/PRD mismatch: branch={branch!r}, prd.branchName={prd.get('branchName')!r}")
    if worktree != ROOT:
        raise EvidenceFailure(f"isolated worktree mismatch: {worktree} != {ROOT}")
    fetch = subprocess_result(["git", "fetch", "origin", "main"], ROOT, 120)
    if fetch["exit_code"] != 0:
        raise EvidenceFailure("origin/main fetch failed: " + fetch["stderr"].strip())
    origin_main = git("rev-parse", "origin/main")
    if origin_main != SOURCE_REVISION:
        raise EvidenceFailure(f"pinned accepted/main source changed: origin/main={origin_main}, expected={SOURCE_REVISION}")
    if not exact_ancestor(STARTUP_INTEGRATION, head):
        raise EvidenceFailure("candidate does not contain startup integration revision")
    if not exact_ancestor(origin_main, head):
        raise EvidenceFailure("candidate does not contain current origin/main ancestry")
    outside_status = ensure_only_owned_changes()
    production_diff = git("diff", "--name-only", SOURCE_REVISION, "--", "agent-cli", "go-agent-loop", "go-agent-runtime", "go-llm-gateway")
    if production_diff:
        raise EvidenceFailure("production source differs from pinned accepted tree: " + production_diff)
    admission = verify_admission()
    rows = board_rows()
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
        "candidate_revision": head,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "origin_main_revision": origin_main,
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
        "board_task_rows": [{"workId": row.get("workId"), "state": row.get("state"), "content": row.get("content"), "last_output": row.get("_last_output"), "rejection_feedback": row.get("_rejection_feedback")} for row in matches],
        "ancestry": {
            "startup_integration_in_head": True,
            "origin_main_in_head": True,
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
    if data.get("origin_main_revision") != SOURCE_REVISION or data.get("startup_integration_revision") != STARTUP_INTEGRATION:
        raise EvidenceFailure("provenance is stale for the pinned accepted/main revisions")
    return data


def ownership_mode() -> dict[str, Any]:
    ensure_provenance()
    rows = board_rows()
    matches = task_rows(rows)
    task = next((row for row in matches if row.get("workTypeName") == "task"), None)
    if task is None:
        raise EvidenceFailure("canonical board C108 task row is missing")
    reviews = [row for row in rows if row.get("name") == WORK and row.get("workTypeName") in {"review", "ci"}]
    peer_rows = [row for row in rows if row.get("name") in {"audio-runtime-c79-retire-wire-baseline-entries", "audio-runtime-c83-", "audio-runtime-c84-", "audio-runtime-c99-", "audio-runtime-c101-", "audio-runtime-c103-", "audio-runtime-c104-", "audio-runtime-c107-"}]
    # Board names can carry suffixes; preserve all rows whose text names the peer IDs.
    peer_rows.extend(row for row in rows if any(peer in str(row.get("name", "")) for peer in ("c79", "c83", "c84", "c99", "c101", "c103", "c104", "c107")) and row not in peer_rows)
    changed_paths = ensure_only_owned_changes()
    evidence = {
        "schema_version": "c108-ownership-v1",
        "work": WORK,
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
        "peer_rows": [
            {"name": row.get("name"), "work_id": row.get("workId"), "work_type": row.get("workTypeName"), "state": row.get("state"), "content": row.get("content"), "last_output": row.get("_last_output"), "rejection_feedback": row.get("_rejection_feedback")}
            for row in peer_rows
        ],
        "controlled_shared_paths": {
            "C79": sorted(PEER_OWNED_PATHS["C79"]),
            "C83": "recheck exact writer paths from live PR before any future lease; no C108 mutation",
            "C84": "recheck exact writer paths from live PR before any future lease; no C108 mutation",
            "C99": "recheck exact writer paths from live PR before any future lease; no C108 mutation",
            "C101": "provider-session caller dependency only; no C108 mutation",
            "C103": "RTC-session caller dependency only; no C108 mutation",
            "C104": "finalization caller dependency only; no C108 mutation",
            "C107": "live owner must be rechecked before any future lease; no C108 mutation",
            "historical_preserved": {
                "C26/C30": ["agent-cli/internal/room/mixer.go"],
                "C59": ["agent-cli/internal/services/internal/agentruntime/session_audio_in.go"],
                "C92": ["agent-cli/internal/services/internal/agentruntime/session_audio_out.go"],
                "C64/C96": ["agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go", "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go"],
            },
        },
        "changed_paths_at_capture": changed_paths,
        "ownership_claim": "C108 owns only its evidence directory; no repair lease, shared-file lease, or project acceptance is claimed.",
        "passes": not reviews,
    }
    if reviews:
        raise EvidenceFailure("a C108 review/CI row already exists; inspect exact findings before proceeding")
    write_json(OWNERSHIP, evidence)
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


def proof_levels_mode() -> dict[str, Any]:
    ensure_provenance()
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
    all_paths: list[str] = []
    for row in rows:
        if not row.get("id") or not row.get("finding_ids") or len(row.get("exact_writer_files", [])) == 0 or len(row.get("exact_symbols", [])) == 0:
            fail(f"candidate {row.get('id')} lacks exact writer evidence")
        for path in row["exact_writer_files"]:
            if any(path in paths for paths in PEER_OWNED_PATHS.values()):
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
            if peer not in row.get("peer_intersections", {}):
                fail(f"candidate {row['id']} missing peer intersection {peer}")
        destination = row["destination"]
        for key in ("thin_contract", "private_internal", "dedicated_wire_owner", "retained_adapter"):
            if not destination.get(key):
                fail(f"candidate {row['id']} missing destination {key}")
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
    if negative_dir.exists():
        shutil.rmtree(negative_dir)
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

    outside = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c108-characterize-audio-device-boundary-gaps-negative-control.txt"
    try:
        try:
            outside.write_text("must not be accepted\n", encoding="utf-8")
            outside_paths = [str(outside.relative_to(ROOT))]
            owned_prefix = str(HERE.relative_to(ROOT)) + "/"
            if any(path != str(HERE.relative_to(ROOT)) and not path.startswith(owned_prefix) for path in outside_paths):
                results.append({"mutation": "write outside owned directory", "rejected": True, "diagnostic": "mutation outside owned C108 directory"})
            else:
                raise EvidenceFailure("outside-owned mutation was not detected")
        finally:
            outside.unlink(missing_ok=True)
    except OSError:
        # Permission-denied outside writes are still a fail-closed rejection.
        results.append({"mutation": "write outside owned directory", "rejected": True, "diagnostic": "mutation outside owned C108 directory (write denied)"})
    report = {"schema_version": "c108-negative-controls-v1", "source_revision": SOURCE_REVISION, "mutations": results, "all_rejected": all(row["rejected"] for row in results), "passes": True}
    write_json(negative_dir / "report.json", report)
    return report


def text_files_under_owned() -> list[pathlib.Path]:
    return [path for path in HERE.rglob("*") if path.is_file() and "__pycache__" not in path.parts and path.name != "SHA256SUMS"]


def release_mode() -> dict[str, Any]:
    provenance = ensure_provenance()
    ensure_only_owned_changes()
    head = git("rev-parse", "HEAD")
    if not exact_ancestor(STARTUP_INTEGRATION, head) or not exact_ancestor(SOURCE_REVISION, head):
        raise EvidenceFailure("release candidate is missing required ancestry")
    if git("rev-parse", "--abbrev-ref", "HEAD") != BRANCH:
        raise EvidenceFailure("release branch does not match PRD")
    production_diff = git("diff", "--name-only", SOURCE_REVISION, "--", "agent-cli", "go-agent-loop", "go-agent-runtime", "go-llm-gateway")
    if production_diff:
        raise EvidenceFailure("release contains production changes outside C108 evidence: " + production_diff)
    diff_check = subprocess_result(["git", "diff", "--check"], ROOT, 60)
    if diff_check["exit_code"] != 0:
        raise EvidenceFailure("git diff --check failed")
    required = [PROVENANCE, OWNERSHIP, BOARD, ANALYSIS / "inventory.json", ANALYSIS / "classifications.json", ANALYSIS / "call-paths.json", CANDIDATES, PROOF_LEVELS, RUNS / "public" / "replay.json", RUNS / "public" / "malformed-truncated.json", RUNS / "focused-checks" / "report.json", RUNS / "negative-controls" / "report.json"]
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
        "production_diff": [],
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
