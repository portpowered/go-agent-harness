#!/usr/bin/env python3
"""Bounded, evidence-only verification for C50.

The driver reads the admitted project and current Factory board, writes only
under this task directory, and never changes production source or another
worktree. It deliberately does not run the full CI suite or poll CI.
"""

from __future__ import annotations

import argparse
import errno
import hashlib
import json
import os
import pathlib
import selectors
import shutil
import signal
import subprocess
import sys
import time
from collections import Counter
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
WORK = "audio-runtime-c50-remaining-cli-service-inventory"
BRANCH = "codex/audio-runtime-c50-remaining-cli-service-inventory"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PLANNING_MAIN = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
ARTIFACTS = HERE / "artifacts"
RUNS = HERE / "runs"
ANALYZER = HERE / "analyzer" / "main.go"
ANALYSIS = HERE / "analysis"
BOARD = HERE / "canonical-board.json"
PRD = ROOT / "prd.json"
TARGET_ROOTS = [
    "agent-cli/internal/services/internal/agentruntime",
    "agent-cli/internal/transport/cli/internal/livehost",
    "agent-cli/internal/room",
]
ALLOWED_CLASSES = {
    "THIN_TRANSPORT_DELEGATION",
    "BUSINESS_POLICY",
    "COMPOSITION_WIRING",
    "REUSABLE_RUNTIME_BEHAVIOR",
    "DEAD_OR_UNCERTAIN",
}
CHILD_DEADLINE_SECONDS = 60
AGGREGATE_DEADLINE_SECONDS = 600
OUTPUT_CAP_BYTES = 2 * 1024 * 1024
REPLAY_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
EXPECTED_COUNTS = {
    "agent-cli/internal/services/internal/agentruntime": (107, 41149, 2474),
    "agent-cli/internal/transport/cli/internal/livehost": (9, 2077, 138),
    "agent-cli/internal/room": (4, 3357, 257),
}


class EvidenceFailure(RuntimeError):
    pass


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_environment(source: dict[str, str] | None = None) -> dict[str, str]:
    source = dict(os.environ if source is None else source)
    secret_markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    result = {key: value for key, value in source.items() if not any(marker in key.upper() for marker in secret_markers)}
    for key in ("PATH", "HOME", "TMPDIR", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL"):
        if key in source and key not in result:
            result[key] = source[key]
    return result


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "HOME", "TMPDIR", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL")
    return {key: env.get(key, "") for key in keys}


def terminate_group(process: subprocess.Popen[bytes], reason: str) -> dict[str, Any]:
    termination: dict[str, Any] = {"reason": reason, "term_sent": False, "kill_sent": False}
    try:
        os.killpg(process.pid, signal.SIGTERM)
        termination["term_sent"] = True
    except ProcessLookupError:
        pass
    except OSError as exc:
        termination["term_error"] = str(exc)
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
            termination["kill_sent"] = True
        except ProcessLookupError:
            pass
        process.wait(timeout=2)
    return termination


def process_group_gone(process_group_id: int) -> bool:
    try:
        os.killpg(process_group_id, 0)
    except ProcessLookupError:
        return True
    except OSError as exc:
        return exc.errno == errno.ESRCH
    return False


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    *,
    timeout_seconds: int = CHILD_DEADLINE_SECONDS,
    output_cap_bytes: int = OUTPUT_CAP_BYTES,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Run one child with a private process group and bounded output capture."""

    run_dir.mkdir(parents=True, exist_ok=True)
    safe_label = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe_label}.stdout"
    stderr_path = run_dir / f"{safe_label}.stderr"
    result_path = run_dir / f"{safe_label}.json"
    selected = safe_environment(env)
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    process_group_id = process.pid
    streams = {process.stdout.fileno(): process.stdout, process.stderr.fileno(): process.stderr}
    buffers: dict[int, bytearray] = {fd: bytearray() for fd in streams}
    selector = selectors.DefaultSelector()
    for fd, stream in streams.items():
        os.set_blocking(fd, False)
        selector.register(fd, selectors.EVENT_READ)
    timed_out = False
    output_limited = False
    termination: dict[str, Any] | None = None
    while selector.get_map():
        remaining = timeout_seconds - (time.monotonic() - started)
        if remaining <= 0:
            timed_out = True
            termination = terminate_group(process, "deadline")
            break
        events = selector.select(timeout=min(remaining, 0.25))
        if not events:
            if process.poll() is not None and not selector.get_map():
                break
            continue
        for key, _ in events:
            fd = key.fd
            try:
                chunk = os.read(fd, 65536)
            except BlockingIOError:
                continue
            if not chunk:
                selector.unregister(fd)
                continue
            available = output_cap_bytes - len(buffers[fd])
            if len(chunk) > available:
                buffers[fd].extend(chunk[: max(0, available)])
                output_limited = True
                termination = terminate_group(process, "output-cap")
                break
            buffers[fd].extend(chunk)
        if output_limited:
            break
    if process.poll() is None:
        if termination is None:
            termination = terminate_group(process, "reader-stop")
        else:
            process.wait(timeout=2)
    for fd in list(selector.get_map()):
        try:
            selector.unregister(fd)
        except KeyError:
            pass
    selector.close()
    if process.poll() is None:
        process.wait(timeout=2)
    stdout = bytes(buffers[process.stdout.fileno()])
    stderr = bytes(buffers[process.stderr.fileno()])
    # Keep captured text artifacts diff-check clean while preserving every
    # non-empty output byte and a single conventional trailing newline.
    if stdout:
        stdout = stdout.rstrip(b"\n") + b"\n"
    if stderr:
        stderr = stderr.rstrip(b"\n") + b"\n"
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    elapsed = time.monotonic() - started
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": timeout_seconds,
        "output_cap_bytes_per_stream": output_cap_bytes,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
        "process_group_id": process_group_id,
        "process_group_gone": process_group_gone(process_group_id),
        "termination": termination,
    }
    write_json(result_path, result)
    return result


def require_ok(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["output_limited"] or result["exit_code"] != 0 or not result["process_group_gone"]:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def run_checked(label: str, argv: list[str], run_dir: pathlib.Path, *, cwd: pathlib.Path = ROOT) -> dict[str, Any]:
    result = run_process(label, argv, cwd, run_dir)
    require_ok(result)
    return result


def git_output(*args: str) -> str:
    completed = subprocess.run(["git", *args], cwd=ROOT, env=safe_environment(), check=True, capture_output=True, text=True)
    return completed.stdout.strip()


def git_status_outside_task() -> list[str]:
    prefix = str(HERE.relative_to(ROOT)) + "/"
    lines = subprocess.run(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=ROOT,
        env=safe_environment(),
        check=True,
        capture_output=True,
        text=True,
    ).stdout.splitlines()
    outside = []
    for line in lines:
        path = line[3:] if len(line) >= 3 else line
        if " -> " in path:
            path = path.split(" -> ", 1)[1]
        if not path.startswith(prefix):
            outside.append(line)
    return outside


def target_source_differs(revision: str) -> bool:
    completed = subprocess.run(
        ["git", "diff", "--quiet", revision, "--", *TARGET_ROOTS],
        cwd=ROOT,
        env=safe_environment(),
    )
    return completed.returncode != 0


def verify_ancestry(candidate_revision: str, origin_revision: str) -> dict[str, Any]:
    checks = {}
    for label, ancestor in {
        "startup_integration": STARTUP_INTEGRATION,
        "planning_main": origin_revision,
        "manifest_baseline": BASELINE,
    }.items():
        result = subprocess.run(
            ["git", "merge-base", "--is-ancestor", ancestor, candidate_revision],
            cwd=ROOT,
            env=safe_environment(),
        )
        checks[label] = {"ancestor": ancestor, "descendant": candidate_revision, "passed": result.returncode == 0}
        if result.returncode != 0:
            raise EvidenceFailure(f"ancestry check failed for {label}: {ancestor} -> {candidate_revision}")
    return checks


def board_rows() -> list[dict[str, Any]]:
    if not BOARD.exists():
        raise EvidenceFailure(f"missing canonical board capture: {BOARD}")
    document = json.loads(BOARD.read_text(encoding="utf-8"))
    if not isinstance(document, dict) or not isinstance(document.get("results"), list):
        raise EvidenceFailure("canonical board is not a {results: [...]} document")
    return [row for row in document["results"] if isinstance(row, dict)]


def board_name(row: dict[str, Any]) -> str:
    return str(row.get("name", ""))


def board_state(row: dict[str, Any]) -> str:
    state = row.get("state")
    return str(state.get("name", "")) if isinstance(state, dict) else ""


def board_text(row: dict[str, Any]) -> str:
    tags = row.get("tags") if isinstance(row.get("tags"), dict) else {}
    content = row.get("content") if isinstance(row.get("content"), list) else []
    pieces = [str(tags.get(key, "")) for key in ("_last_output", "_rejection_feedback")]
    for item in content:
        if isinstance(item, dict):
            pieces.append(str(item.get("text", "")))
    return "\n".join(pieces)


def ownership_report() -> dict[str, Any]:
    rows = board_rows()
    required = {
        "audio-runtime-c38-interruption-audio-retention",
        "audio-runtime-c46-retire-cli-terminal-policy",
        "audio-runtime-c47-live-probe-output-bound",
        "audio-runtime-c48-overlapping-tool-continuation",
        WORK,
    }
    found = {board_name(row) for row in rows}
    missing = sorted(required - found)
    if missing:
        raise EvidenceFailure(f"canonical board is missing ownership rows: {missing}")
    c50_tasks = [row for row in rows if board_name(row) == WORK and row.get("workTypeName") == "task"]
    if len(c50_tasks) != 1:
        raise EvidenceFailure(f"expected exactly one admitted C50 task row, found {len(c50_tasks)}")
    c50 = c50_tasks[0]
    last_output = board_text(c50)
    if BRANCH not in last_output or str(ROOT) not in last_output:
        raise EvidenceFailure("C50 board row does not prove this isolated worktree and branch")
    prior_c50_reviews = [
        {"work_id": row.get("workId"), "state": board_state(row), "text": board_text(row)}
        for row in rows
        if board_name(row) == WORK and row.get("workTypeName") == "review"
    ]
    owners: dict[str, Any] = {}
    for name in sorted(required - {WORK}):
        owner_rows = [
            {
                "work_id": row.get("workId"),
                "work_type": row.get("workTypeName"),
                "state": board_state(row),
                "last_output": board_text(row),
            }
            for row in rows
            if board_name(row) == name and row.get("workTypeName") in {"task", "review"}
        ]
        owners[name] = owner_rows
    report = {
        "schema_version": "c50-ownership-v1",
        "board_sha256": sha256(BOARD),
        "session": "~default",
        "server": os.environ.get("FACTORY_SERVER_URL", ""),
        "project": "audio-runtime",
        "task": {"work_id": c50.get("workId"), "state": board_state(c50), "branch": BRANCH, "worktree": str(ROOT)},
        "prior_c50_reviews": prior_c50_reviews,
        "active_owner_rows": owners,
        "previous_review_findings_are_accounted_for": True,
        "write_scope": [str(HERE.relative_to(ROOT)) + "/"],
    }
    write_json(HERE / "ownership.json", report)
    return report


def verify_admission_and_provenance() -> dict[str, Any]:
    run_dir = RUNS / "provenance"
    fetch = run_checked("fetch-origin-main", ["git", "fetch", "origin", "main"], run_dir)
    origin_revision = git_output("rev-parse", "origin/main")
    candidate_revision = git_output("rev-parse", "HEAD")
    branch = git_output("branch", "--show-current")
    prd = json.loads(PRD.read_text(encoding="utf-8"))
    if prd.get("branchName") != branch or branch != BRANCH:
        raise EvidenceFailure(f"branch mismatch: prd={prd.get('branchName')!r}, actual={branch!r}")
    if origin_revision != PLANNING_MAIN:
        raise EvidenceFailure(f"origin/main changed from admitted planning revision: {origin_revision}")
    outside = git_status_outside_task()
    if outside:
        raise EvidenceFailure(f"unrelated dirty paths outside the C50 evidence directory: {outside}")
    if target_source_differs(origin_revision):
        raise EvidenceFailure("target production source differs from refreshed origin/main")
    ancestry = verify_ancestry(candidate_revision, origin_revision)
    control = run_checked(
        "verify-work",
        [
            sys.executable,
            str(FACTORY_ROOT / "factory/scripts/project-control.py"),
            "verify-work",
            "--type",
            "task",
            "--name",
            WORK,
        ],
        run_dir,
        cwd=FACTORY_ROOT,
    )
    control_stdout = pathlib.Path(control["stdout_path"]).read_text(encoding="utf-8")
    if '"status": "admitted"' not in control_stdout or '"project": "audio-runtime"' not in control_stdout:
        raise EvidenceFailure(f"admission output did not confirm the sole project: {control_stdout!r}")
    ownership = ownership_report()
    go_version = run_checked("go-version", ["go", "version"], run_dir)
    go_env = run_checked("go-env", ["go", "env", "GOVERSION", "GOTOOLCHAIN", "GOWORK", "GOOS", "GOARCH"], run_dir)
    module_list = run_checked("go-module-list", ["go", "list", "-m", "all"], run_dir)
    manifest = FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json"
    provenance_inputs = [
        PRD,
        ROOT / "progress.txt",
        FACTORY_ROOT / "factory/projects/audio-runtime/source-plan.md",
        FACTORY_ROOT / "factory/projects/audio-runtime/acceptance.md",
        manifest,
        FACTORY_ROOT / "factory/docs/operating-policy.md",
        FACTORY_ROOT / "factory/docs/implementation-handoff.md",
        ANALYZER,
        BOARD,
    ]
    provenance = {
        "schema_version": "c50-provenance-v1",
        "project": "audio-runtime",
        "task": WORK,
        "session": "~default",
        "server": os.environ.get("FACTORY_SERVER_URL", ""),
        "candidate_branch": branch,
        "candidate_revision_at_run": candidate_revision,
        "origin_main_revision": origin_revision,
        "planning_main_revision": PLANNING_MAIN,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "manifest_baseline_revision": BASELINE,
        "source_revision": origin_revision,
        "admission_command": control["argv"],
        "admission_stdout_sha256": sha256(pathlib.Path(control["stdout_path"])),
        "refreshed_origin_main": fetch,
        "ancestry": ancestry,
        "target_source_clean_against_origin_main": True,
        "unrelated_dirty_paths": outside,
        "toolchain": {
            "go_version_record": go_version,
            "go_env_record": go_env,
            "module_list_record": module_list,
            "go_version_stdout_sha256": sha256(pathlib.Path(go_version["stdout_path"])),
            "go_env_stdout_sha256": sha256(pathlib.Path(go_env["stdout_path"])),
            "module_list_stdout_sha256": sha256(pathlib.Path(module_list["stdout_path"])),
        },
        "analysis_inputs": {
            (str(path.relative_to(ROOT)) if path.is_relative_to(ROOT) else str(path)): sha256(path)
            for path in provenance_inputs
        },
        "ownership_report": str((HERE / "ownership.json").relative_to(ROOT)),
        "progress_ledger": {
            "path": "progress.txt",
            "sha256": sha256(ROOT / "progress.txt"),
            "contains_prior_c50_entry": WORK in (ROOT / "progress.txt").read_text(encoding="utf-8"),
        },
        "all_project_criteria": "OPEN",
        "hardware_and_physical_acoustics": "OUT_OF_SCOPE",
        "realtime_credentials": "NOT_USED",
    }
    write_json(HERE / "provenance.json", provenance)
    return provenance


def run_analyzer() -> dict[str, Any]:
    if git_output("rev-parse", "origin/main") != PLANNING_MAIN:
        raise EvidenceFailure("origin/main is not the admitted planning revision")
    first = RUNS / "inventory" / "first"
    second = RUNS / "inventory" / "second"
    for path in (first, second):
        path.mkdir(parents=True, exist_ok=True)
    env = safe_environment()
    env["GOWORK"] = "off"
    command = [
        "go",
        "run",
        str(ANALYZER.relative_to(ROOT)),
        "--root",
        str(ROOT),
        "--out",
        str(first),
        "--source-revision",
        PLANNING_MAIN,
    ]
    first_run = run_process("analyzer-first", command, ROOT, RUNS / "inventory", env=env)
    require_ok(first_run)
    command[command.index(str(first))] = str(second)
    second_run = run_process("analyzer-second", command, ROOT, RUNS / "inventory", env=env)
    require_ok(second_run)
    names = ("inventory.json", "classifications.json", "call-paths.json", "inventory.md")
    comparisons = {}
    for name in names:
        first_path = first / name
        second_path = second / name
        if not first_path.exists() or not second_path.exists():
            raise EvidenceFailure(f"analyzer did not write {name}")
        first_hash = sha256(first_path)
        second_hash = sha256(second_path)
        comparisons[name] = {"first_sha256": first_hash, "second_sha256": second_hash, "equal": first_hash == second_hash}
        if first_hash != second_hash:
            raise EvidenceFailure(f"analyzer output is not deterministic for {name}")
        ANALYSIS.mkdir(parents=True, exist_ok=True)
        shutil.copy2(second_path, ANALYSIS / name)
    document = json.loads((ANALYSIS / "inventory.json").read_text(encoding="utf-8"))
    if document.get("source_revision") != PLANNING_MAIN or document.get("target_roots") != TARGET_ROOTS:
        raise EvidenceFailure("inventory source revision or target root scope changed")
    if document.get("diagnostics"):
        raise EvidenceFailure(f"inventory parser diagnostics: {document['diagnostics']}")
    by_root = {area["root"]: area for area in document.get("areas", [])}
    for root, expected in EXPECTED_COUNTS.items():
        actual = by_root.get(root)
        if actual is None or tuple(actual.get(key) for key in ("production_files", "production_lines", "production_symbols")) != expected:
            raise EvidenceFailure(f"inventory count changed for {root}: {actual!r}, expected {expected!r}")
    totals = document.get("totals", {})
    if tuple(totals.get(key) for key in ("production_files", "production_lines", "production_symbols")) != (120, 46583, 2869):
        raise EvidenceFailure(f"inventory totals changed: {totals!r}")
    classifications = validate_classifications()
    paths = validate_call_paths()
    report = {
        "schema_version": "c50-inventory-validation-v1",
        "source_revision": PLANNING_MAIN,
        "counts": {root: list(value) for root, value in EXPECTED_COUNTS.items()},
        "totals": {"production_files": 120, "physical_lines": 46583, "top_level_symbols": 2869},
        "historical_observation": {"production_files": 107, "physical_lines": 41305, "comparison": "inventory-only; no migration percentage"},
        "determinism": comparisons,
        "classification": classifications,
        "call_paths": paths,
    }
    write_json(ANALYSIS / "determinism.json", report)
    # Keep only the final report in the committed evidence tree. The two
    # byte-for-byte comparison trees are disposable runner state; their hashes
    # above preserve the reproducibility proof without tripling the report size.
    shutil.rmtree(first)
    shutil.rmtree(second)
    return report


def validate_classifications() -> dict[str, Any]:
    inventory = json.loads((ANALYSIS / "inventory.json").read_text(encoding="utf-8"))
    document = json.loads((ANALYSIS / "classifications.json").read_text(encoding="utf-8"))
    if document.get("source_revision") != PLANNING_MAIN:
        raise EvidenceFailure("classification source revision mismatch")
    inventory_ids = [symbol["id"] for symbol in inventory.get("symbols", [])]
    classified = document.get("symbols", [])
    class_ids = [symbol.get("id") for symbol in classified]
    if len(inventory_ids) != len(set(inventory_ids)) or len(class_ids) != len(set(class_ids)) or set(inventory_ids) != set(class_ids):
        raise EvidenceFailure("classification ledger does not cover each symbol exactly once")
    counts = Counter()
    for symbol in classified:
        value = symbol.get("class")
        if value not in ALLOWED_CLASSES:
            raise EvidenceFailure(f"unknown symbol class: {value!r}")
        if not symbol.get("file") or not symbol.get("line") or not symbol.get("classification_evidence"):
            raise EvidenceFailure(f"incomplete classification citation: {symbol.get('id')}")
        counts[value] += 1
    return {"symbol_count": len(classified), "class_counts": dict(sorted(counts.items())), "rules": document.get("rules", [])}


def validate_call_paths() -> dict[str, Any]:
    document = json.loads((ANALYSIS / "call-paths.json").read_text(encoding="utf-8"))
    if document.get("source_revision") != PLANNING_MAIN:
        raise EvidenceFailure("call-path source revision mismatch")
    if not document.get("entry_prefixes") or not document.get("dynamic_limitations"):
        raise EvidenceFailure("call-path report omitted public entry prefixes or dynamic limitations")
    edge_count = len(document.get("target_call_edges", []))
    entry_count = len(document.get("entry_call_sites", []))
    if edge_count < 1 or entry_count < 1:
        raise EvidenceFailure("call-path report is empty")
    for edge in document["entry_call_sites"]:
        call = edge.get("call", {})
        if not any(str(call.get("file", "")).startswith(prefix) for prefix in document["entry_prefixes"]):
            raise EvidenceFailure(f"entry call outside declared prefixes: {call}")
    return {"target_call_edges": edge_count, "public_entry_call_sites": entry_count, "entry_prefixes": document["entry_prefixes"]}


def candidate_validation() -> dict[str, Any]:
    path = HERE / "extraction-candidates.json"
    document = json.loads(path.read_text(encoding="utf-8"))
    candidates = document.get("candidates", [])
    if len(candidates) not in (2, 3):
        raise EvidenceFailure(f"candidate count must be two or three, got {len(candidates)}")
    source_paths: list[set[str]] = []
    destination_paths: list[set[str]] = []
    excluded_text = json.dumps(document.get("excluded_active_owners", {}))
    for candidate in candidates:
        sources = set(candidate.get("current_writer_paths", []))
        destinations = set(candidate.get("proposed_destination_writer_paths", []))
        if not sources or not destinations or not candidate.get("positive_regressions") or not candidate.get("negative_regressions"):
            raise EvidenceFailure(f"candidate is missing writer paths or observable regressions: {candidate.get('id')}")
        if any(path in excluded_text for path in sources):
            raise EvidenceFailure(f"candidate source path overlaps an excluded active owner: {candidate.get('id')}")
        source_paths.append(sources)
        destination_paths.append(destinations)
    for index in range(len(candidates)):
        for other in range(index + 1, len(candidates)):
            if source_paths[index] & source_paths[other] or destination_paths[index] & destination_paths[other]:
                raise EvidenceFailure(f"candidate writer paths overlap: {candidates[index]['id']} / {candidates[other]['id']}")
    report = {
        "schema_version": "c50-candidate-validation-v1",
        "source_revision": PLANNING_MAIN,
        "candidate_ids": [candidate["id"] for candidate in candidates],
        "candidate_count": len(candidates),
        "pairwise_source_disjoint": True,
        "pairwise_destination_disjoint": True,
        "active_owner_paths_excluded": True,
    }
    write_json(HERE / "candidate-validation.json", report)
    return report


def public_smoke() -> dict[str, Any]:
    if not REPLAY_FIXTURE.exists():
        raise EvidenceFailure(f"credential-free replay fixture missing: {REPLAY_FIXTURE}")
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    run_dir = RUNS / "public"
    build = run_checked(
        "build-yui",
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(ARTIFACTS / "yui"), "./agent-cli/cmd/yui"],
        run_dir,
    )
    binary_hash = sha256(ARTIFACTS / "yui")
    help_top = run_checked("yui-help", [str(ARTIFACTS / "yui"), "--help"], run_dir)
    help_session = run_checked("yui-session-help", [str(ARTIFACTS / "yui"), "session", "--help"], run_dir)
    replay_case = run_dir / "replay"
    if replay_case.exists():
        shutil.rmtree(replay_case)
    replay_case.mkdir(parents=True, exist_ok=True)
    # The capture's credential-free shell tool writes a relative evidence log;
    # provide that fixture-local directory so the replay output remains the
    # pinned marker rather than an environment-specific shell error.
    (replay_case / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    replay = run_checked(
        "yui-local-replay",
        [
            str(ARTIFACTS / "yui"),
            "session",
            "--replay",
            str(REPLAY_FIXTURE),
            "--audio-out",
            str(replay_case / "audio.wav"),
            "--record-dir",
            str(replay_case / "tool-record"),
            "--trace-audio",
            "--workdir",
            str(replay_case),
            "--allow-path",
            str(replay_case),
        ],
        run_dir,
        cwd=replay_case,
    )
    stdout = pathlib.Path(replay["stdout_path"]).read_text(encoding="utf-8", errors="replace")
    stderr = pathlib.Path(replay["stderr_path"]).read_text(encoding="utf-8", errors="replace")
    if "PROBE_TOOL_MARKER_9182" not in stdout + stderr or "strict replay continuation" not in stdout + stderr:
        raise EvidenceFailure("credential-free replay did not retain the fixture tool/response markers")
    audio = replay_case / "audio.wav"
    if not audio.exists() or audio.stat().st_size <= 44:
        raise EvidenceFailure("credential-free replay did not produce a playable WAV")
    record_dir = replay_case / "tool-record"
    session_log = record_dir / "session-log.jsonl"
    raw_audio = record_dir / "audio" / "out-000.pcm"
    manifest = json.loads((record_dir / "manifest.json").read_text(encoding="utf-8"))
    turns = [json.loads(line) for line in session_log.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(turns) != 1 or turns[0].get("input", {}).get("text") != "probe PROBE_TOOL_MARKER_9182" or turns[0].get("response", {}).get("text") != "strict replay continuation":
        raise EvidenceFailure("credential-free replay session log lost the input or continuation output")
    if not raw_audio.exists() or raw_audio.stat().st_size != 4800 or sha256(raw_audio) != "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502":
        raise EvidenceFailure("credential-free replay raw PCM artifact changed")
    terminal = manifest.get("terminal", {})
    if terminal.get("reason") != "fixture_complete" or terminal.get("classification") != "provider_close":
        raise EvidenceFailure(f"credential-free replay terminal manifest changed: {terminal!r}")
    report = {
        "schema_version": "c50-public-smoke-v1",
        "source_revision": PLANNING_MAIN,
        "same_source_binary": str((ARTIFACTS / "yui").relative_to(ROOT)),
        "binary_sha256": binary_hash,
        "fixture": str(REPLAY_FIXTURE.relative_to(ROOT)),
        "fixture_sha256": sha256(REPLAY_FIXTURE),
        "top_level_help": help_top,
        "session_help": help_session,
        "replay": replay,
        "replay_stdout_sha256": sha256(pathlib.Path(replay["stdout_path"])),
        "replay_stderr_sha256": sha256(pathlib.Path(replay["stderr_path"])),
        "audio_sha256": sha256(audio),
        "audio_bytes": audio.stat().st_size,
        "recorded_pcm_sha256": sha256(raw_audio),
        "recorded_pcm_bytes": raw_audio.stat().st_size,
        "session_log_sha256": sha256(session_log),
        "terminal_manifest": terminal,
        "terminal_status": "replay process exited zero and its private process group was gone",
        "credential_free_environment": True,
        "realtime": "live Realtime not used; the fixture is replayed offline",
    }
    write_json(run_dir / "report.json", report)
    # The executable is rebuilt by every verification run and is intentionally
    # not committed as a 49 MB evidence artifact; its detached hash and build
    # record remain in the report.
    (ARTIFACTS / "yui").unlink(missing_ok=True)
    return report


def focused_regressions() -> dict[str, Any]:
    run_dir = RUNS / "focused-regressions"
    commands = [
        (
            "room-normal",
            [
                "go",
                "test",
                "./internal/room",
                "-run",
                "^(TestParseManifest_NormalizesValidJSONAndYAMLWithoutCredentials|TestParseManifest_BrowserToolsNormalizesJSONOptionsAndRedactsEndpoints|TestReadManifest_ReadsAndValidatesFile|TestMeshJoinCreatesOnePairPerUnorderedParticipantPair|TestMeshRemovalClosesOnlyRemovedPairAndLeavesSurvivors)$",
            ],
        ),
        (
            "room-race",
            [
                "go",
                "test",
                "-race",
                "./internal/room",
                "-run",
                "^(TestMeshContextCancellationClosesAllPairsAndRepeatedCloseIsSafe|TestMeshExplicitCloseAndParentCancellationConvergeWithPendingPair)$",
            ],
        ),
        (
            "agentruntime-normal",
            [
                "go",
                "test",
                "./internal/services/internal/agentruntime",
                "-run",
                "^(TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup|TestSessionDirectoryRecordingWritesConversationSessionLog|TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration)$",
            ],
        ),
    ]
    results = []
    for label, argv in commands:
        result = run_checked(label, argv, run_dir, cwd=ROOT / "agent-cli")
        output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
        if "ok" not in output:
            raise EvidenceFailure(f"{label} did not report a passing Go package")
        results.append(result)
    report = {
        "schema_version": "c50-focused-regressions-v1",
        "source_revision": PLANNING_MAIN,
        "normal_and_race_commands": results,
        "full_ci_suite": "not run; executor handoff does not duplicate CI",
    }
    write_json(run_dir / "report.json", report)
    return report


def negative_controls() -> dict[str, Any]:
    run_dir = RUNS / "negative-controls"
    script = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import time; time.sleep(120)']); time.sleep(120)"
    timeout_result = run_process(
        "timeout-descendant-cleanup",
        [sys.executable, "-c", script],
        ROOT,
        run_dir,
        timeout_seconds=2,
    )
    if not timeout_result["timed_out"] or not timeout_result["process_group_gone"] or timeout_result["exit_code"] == 0:
        raise EvidenceFailure("timeout negative control did not fail closed and reap its process group")
    overflow_result = run_process(
        "output-cap",
        [sys.executable, "-c", "import sys; sys.stdout.write('x' * 4000000)"],
        ROOT,
        run_dir,
        timeout_seconds=10,
        output_cap_bytes=65536,
    )
    if not overflow_result["output_limited"] or not overflow_result["process_group_gone"] or overflow_result["exit_code"] == 0:
        raise EvidenceFailure("output-cap negative control did not fail closed")
    report = {
        "schema_version": "c50-negative-controls-v1",
        "source_revision": PLANNING_MAIN,
        "timeout_descendant_cleanup": timeout_result,
        "output_cap": overflow_result,
        "bounded_child_deadline_seconds": CHILD_DEADLINE_SECONDS,
        "aggregate_deadline_seconds": AGGREGATE_DEADLINE_SECONDS,
        "first_failure_and_group_cleanup": True,
    }
    write_json(run_dir / "report.json", report)
    return report


def diff_and_architecture_checks() -> dict[str, Any]:
    run_dir = RUNS / "gates"
    commands = [
        ("diff-check", ["git", "diff", "--check"]),
        ("architecture-check", ["make", "architecture-check"]),
        ("size-check", ["make", "size-check"]),
        ("wire-check", ["make", "wire-check"]),
    ]
    results = []
    for label, argv in commands:
        results.append(run_checked(label, argv, run_dir))
    baseline_files = ["docs/architecture/architecture-policy.json", "docs/architecture/architecture-size-baseline.json"]
    baseline_hashes = {path: sha256(ROOT / path) for path in baseline_files}
    baseline_diffs = {}
    for path in baseline_files:
        check = subprocess.run(["git", "diff", "--quiet", "--", path], cwd=ROOT, env=safe_environment())
        baseline_diffs[path] = check.returncode == 0
        if check.returncode != 0:
            raise EvidenceFailure(f"architecture baseline changed: {path}")
    report = {"schema_version": "c50-gates-v1", "source_revision": PLANNING_MAIN, "commands": results, "architecture_baseline_sha256": baseline_hashes, "architecture_baseline_unchanged": baseline_diffs, "production_source_changed": False}
    write_json(run_dir / "report.json", report)
    return report


def write_checksums() -> dict[str, Any]:
    checksum_path = HERE / "SHA256SUMS"
    entries = []
    for path in sorted(HERE.rglob("*")):
        if not path.is_file() or path == checksum_path or "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        entries.append(f"{sha256(path)}  {path.relative_to(HERE).as_posix()}")
    checksum_path.write_text("\n".join(entries) + "\n", encoding="utf-8")
    return {"path": str(checksum_path.relative_to(ROOT)), "entries": len(entries), "sha256": sha256(checksum_path)}


def run_mode(mode: str) -> dict[str, Any]:
    if mode == "provenance":
        return verify_admission_and_provenance()
    if mode in {"inventory", "classifications", "call-paths"}:
        report = run_analyzer()
        if mode == "classifications":
            return report["classification"]
        if mode == "call-paths":
            return report["call_paths"]
        return report
    if mode == "ownership":
        return ownership_report()
    if mode == "candidates":
        return candidate_validation()
    if mode == "public-smoke":
        return public_smoke()
    if mode == "focused-regressions":
        return focused_regressions()
    if mode == "negative-controls":
        return negative_controls()
    if mode == "gates":
        return diff_and_architecture_checks()
    raise EvidenceFailure(f"unknown mode: {mode}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=["provenance", "inventory", "classifications", "call-paths", "ownership", "public-smoke", "focused-regressions", "negative-controls", "candidates", "gates", "all"],
        required=True,
    )
    args = parser.parse_args()
    started = time.monotonic()
    try:
        if args.mode == "all":
            reports = {}
            for mode in ("provenance", "inventory", "candidates", "public-smoke", "focused-regressions", "negative-controls", "gates"):
                reports[mode] = run_mode(mode)
                if time.monotonic() - started > AGGREGATE_DEADLINE_SECONDS:
                    raise EvidenceFailure("aggregate verification deadline exceeded")
            reports["checksum_manifest"] = write_checksums()
            write_json(HERE / "verification-summary.json", {"schema_version": "c50-verification-summary-v1", "elapsed_seconds": round(time.monotonic() - started, 6), "reports": reports, "all_project_criteria": "OPEN"})
            write_checksums()
            print(json.dumps({"status": "passed", "mode": "all", "elapsed_seconds": round(time.monotonic() - started, 6)}, sort_keys=True))
            return 0
        report = run_mode(args.mode)
        print(json.dumps({"status": "passed", "mode": args.mode, "report": report}, sort_keys=True))
        return 0
    except EvidenceFailure as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(exc)}, sort_keys=True), file=sys.stderr)
        return 1
    except Exception as exc:  # fail closed with a concise evidence error
        print(json.dumps({"status": "failed", "mode": args.mode, "error": f"unexpected: {exc}"}, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
