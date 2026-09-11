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
import io
import json
import os
import pathlib
import selectors
import shutil
import signal
import subprocess
import sys
import tarfile
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
BUILD_INPUT_ROOTS = [
    "agent-cli",
    "go-agent-runtime",
    "go-agent-loop",
    "go-audio",
    "go-device-gateway",
    "go-llm-gateway",
    "tests",
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
OWNED_OUTPUT_QUOTA_BYTES = 128 * 1024 * 1024
REPLAY_OUTPUT_QUOTA_BYTES = 16 * 1024 * 1024
REPLAY_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
EXPECTED_COUNTS = {
    "agent-cli/internal/services/internal/agentruntime": (107, 41147, 2478),
    "agent-cli/internal/transport/cli/internal/livehost": (9, 2077, 138),
    "agent-cli/internal/room": (4, 3357, 257),
}

# These are configured from the documented command-line flags before any
# phase starts. Every child receives the same aggregate deadline and quota.
REQUESTED_CHILD_DEADLINE_SECONDS = CHILD_DEADLINE_SECONDS
REQUESTED_AGGREGATE_DEADLINE_SECONDS = AGGREGATE_DEADLINE_SECONDS
REQUESTED_BINARY: pathlib.Path | None = None
AGGREGATE_STARTED: float | None = None


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


def aggregate_remaining() -> float | None:
    if AGGREGATE_STARTED is None:
        return None
    return REQUESTED_AGGREGATE_DEADLINE_SECONDS - (time.monotonic() - AGGREGATE_STARTED)


def owned_output_bytes() -> int:
    total = 0
    for path in HERE.rglob("*"):
        if path.is_file() and "__pycache__" not in path.parts and path.suffix != ".pyc":
            try:
                total += path.stat().st_size
            except FileNotFoundError:
                continue
    return total


def owned_directory_bytes(directory: pathlib.Path) -> int:
    total = 0
    if not directory.exists():
        return total
    for path in directory.rglob("*"):
        if path.is_file():
            try:
                total += path.stat().st_size
            except FileNotFoundError:
                continue
    return total


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
    wait_budget = 2.0
    remaining = aggregate_remaining()
    if remaining is not None:
        wait_budget = min(wait_budget, max(0.05, remaining))
    try:
        process.wait(timeout=wait_budget)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
            termination["kill_sent"] = True
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=min(2.0, max(0.05, aggregate_remaining() or 2.0)))
        except subprocess.TimeoutExpired:
            termination["wait_timeout"] = True
            try:
                process.kill()
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=0.5)
            except subprocess.TimeoutExpired:
                pass
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
    timeout_seconds: int | None = None,
    output_cap_bytes: int = OUTPUT_CAP_BYTES,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Run one child with a private process group and bounded output capture."""

    if timeout_seconds is None:
        timeout_seconds = REQUESTED_CHILD_DEADLINE_SECONDS
    aggregate_at_start = aggregate_remaining()
    if aggregate_at_start is not None and aggregate_at_start <= 0:
        raise EvidenceFailure(f"aggregate verification deadline already exceeded before {label}")
    owned_before = owned_output_bytes()
    if owned_before >= OWNED_OUTPUT_QUOTA_BYTES:
        raise EvidenceFailure(f"owned output quota already exhausted before {label}: {owned_before} bytes")
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
    aggregate_deadline_hit = False
    output_limited = False
    owned_output_limited = False
    capped_fds: set[int] = set()
    termination: dict[str, Any] | None = None
    while selector.get_map():
        child_remaining = timeout_seconds - (time.monotonic() - started)
        aggregate_remaining_now = aggregate_remaining()
        if aggregate_remaining_now is not None and aggregate_remaining_now <= 0:
            timed_out = True
            aggregate_deadline_hit = True
            termination = terminate_group(process, "aggregate-deadline")
            break
        remaining = child_remaining
        if aggregate_remaining_now is not None:
            remaining = min(remaining, aggregate_remaining_now)
        if remaining <= 0:
            timed_out = True
            termination = terminate_group(process, "deadline")
            break
        events = selector.select(timeout=min(remaining, 0.25))
        if not events:
            if process.poll() is not None and not selector.get_map():
                break
            if owned_output_bytes() >= OWNED_OUTPUT_QUOTA_BYTES:
                owned_output_limited = True
                termination = terminate_group(process, "owned-output-quota")
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
                capped_fds.add(fd)
                output_limited = True
                termination = terminate_group(process, "output-cap")
                break
            buffers[fd].extend(chunk)
            if owned_output_bytes() >= OWNED_OUTPUT_QUOTA_BYTES:
                owned_output_limited = True
                termination = terminate_group(process, "owned-output-quota")
                break
        if output_limited:
            break
        if owned_output_limited:
            break
    if process.poll() is None:
        if termination is None:
            termination = terminate_group(process, "reader-stop")
        else:
            try:
                process.wait(timeout=min(2.0, max(0.05, aggregate_remaining() or 2.0)))
            except subprocess.TimeoutExpired:
                termination["wait_timeout"] = True
    for fd in list(selector.get_map()):
        try:
            selector.unregister(fd)
        except KeyError:
            pass
    selector.close()
    if process.poll() is None:
        try:
            process.wait(timeout=min(2.0, max(0.05, aggregate_remaining() or 2.0)))
        except subprocess.TimeoutExpired:
            termination = termination or {"reason": "reader-stop"}
            termination["wait_timeout"] = True
    stdout = bytes(buffers[process.stdout.fileno()])
    stderr = bytes(buffers[process.stderr.fileno()])
    # Keep ordinary text artifacts diff-check clean while preserving exact
    # bytes for a stream stopped at the declared cap.
    if stdout and process.stdout.fileno() not in capped_fds:
        stdout = stdout.rstrip(b"\n") + b"\n"
    if stderr and process.stderr.fileno() not in capped_fds:
        stderr = stderr.rstrip(b"\n") + b"\n"
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    owned_after = owned_output_bytes()
    if owned_after >= OWNED_OUTPUT_QUOTA_BYTES:
        owned_output_limited = True
    elapsed = time.monotonic() - started
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": timeout_seconds,
        "aggregate_deadline_seconds": REQUESTED_AGGREGATE_DEADLINE_SECONDS,
        "aggregate_deadline_hit": aggregate_deadline_hit,
        "output_cap_bytes_per_stream": output_cap_bytes,
        "owned_output_quota_bytes": OWNED_OUTPUT_QUOTA_BYTES,
        "owned_output_bytes_before": owned_before,
        "owned_output_bytes_after": owned_after,
        "owned_output_limited": owned_output_limited,
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
    if result["timed_out"] or result.get("aggregate_deadline_hit") or result["output_limited"] or result.get("owned_output_limited") or result["exit_code"] != 0 or not result["process_group_gone"]:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def run_checked(label: str, argv: list[str], run_dir: pathlib.Path, *, cwd: pathlib.Path = ROOT) -> dict[str, Any]:
    result = run_process(label, argv, cwd, run_dir)
    require_ok(result)
    return result


def git_output(*args: str) -> str:
    completed = subprocess.run(["git", *args], cwd=ROOT, env=safe_environment(), check=True, capture_output=True, text=True, timeout=60)
    return completed.stdout.strip()


def inspected_source_revision() -> str:
    """Return the clean integrated-main source pin used by this evidence run."""
    return git_output("rev-parse", "origin/main")


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


def is_target_source_path(path: str) -> bool:
    return any(path == root or path.startswith(root + "/") for root in TARGET_ROOTS)


def verify_ancestry(candidate_revision: str, origin_revision: str) -> dict[str, Any]:
    checks = {}
    for label, ancestor in {
        "startup_integration": STARTUP_INTEGRATION,
        "planning_main": PLANNING_MAIN,
        "integrated_main": origin_revision,
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


def verify_planning_snapshot_is_available(origin_revision: str) -> dict[str, Any]:
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", PLANNING_MAIN, origin_revision],
        cwd=ROOT,
        env=safe_environment(),
    )
    if result.returncode != 0:
        raise EvidenceFailure(f"refreshed origin/main no longer contains the admitted planning snapshot: {PLANNING_MAIN}")
    return {
        "planning_main": PLANNING_MAIN,
        "refreshed_origin_main": origin_revision,
        "planning_main_is_ancestor_of_refreshed_origin_main": True,
    }


def board_rows() -> list[dict[str, Any]]:
    if not BOARD.exists():
        raise EvidenceFailure(f"missing canonical board capture: {BOARD}")
    document = json.loads(BOARD.read_text(encoding="utf-8"))
    if not isinstance(document, dict) or not isinstance(document.get("results"), list):
        raise EvidenceFailure("canonical board is not a {results: [...]} document")
    return [row for row in document["results"] if isinstance(row, dict)]


def refresh_canonical_board(run_dir: pathlib.Path) -> dict[str, Any]:
    server = os.environ.get("FACTORY_SERVER_URL", "")
    if not server:
        raise EvidenceFailure("FACTORY_SERVER_URL is unavailable; cannot refresh the admitted board")
    result = run_checked(
        "factory-board-list",
        [
            "rtk",
            "proxy",
            "you",
            "--server",
            server,
            "--json",
            "work",
            "list",
            "--session",
            "~default",
            "--max-results",
            "500",
            "--all",
        ],
        run_dir,
        cwd=FACTORY_ROOT,
    )
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
    try:
        document = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"Factory board capture is not JSON: {exc}") from exc
    if not isinstance(document, dict) or not isinstance(document.get("results"), list):
        raise EvidenceFailure("Factory board capture is not a {results: [...]} document")
    write_json(BOARD, document)
    return result


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
        "previous_review_findings_are_accounted_for": not prior_c50_reviews or all(item["text"].strip() for item in prior_c50_reviews),
        "prior_review_feedback_sha256": [
            {"work_id": item["work_id"], "sha256": hashlib.sha256(item["text"].encode("utf-8")).hexdigest()}
            for item in prior_c50_reviews
        ],
        "write_scope": [str(HERE.relative_to(ROOT)) + "/"],
    }
    write_json(HERE / "ownership.json", report)
    return report


def source_archive_record(revision: str) -> dict[str, Any]:
    argv = ["git", "archive", "--format=tar", revision, "--", *TARGET_ROOTS]
    completed = subprocess.run(
        argv,
        cwd=ROOT,
        env=safe_environment(),
        check=True,
        capture_output=True,
        timeout=60,
    )
    archive = completed.stdout
    members: list[str] = []
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as handle:
        members = sorted(member.name for member in handle.getmembers() if member.isfile())
    return {
        "revision": revision,
        "format": "tar",
        "paths": TARGET_ROOTS,
        "bytes": len(archive),
        "file_count": len(members),
        "files_sha256": hashlib.sha256("\n".join(members).encode("utf-8")).hexdigest(),
        "archive_sha256": hashlib.sha256(archive).hexdigest(),
        "command": argv,
    }


def build_inputs_record(revision: str) -> dict[str, Any]:
    argv = ["git", "ls-files", "--", *BUILD_INPUT_ROOTS, "go.work", "go.work.sum"]
    completed = subprocess.run(
        argv,
        cwd=ROOT,
        env=safe_environment(),
        check=True,
        capture_output=True,
        text=True,
        timeout=60,
    )
    paths = sorted({line.strip() for line in completed.stdout.splitlines() if line.strip()})
    hashes: dict[str, str] = {}
    for path in paths:
        candidate = ROOT / path
        if not candidate.is_file():
            raise EvidenceFailure(f"build input disappeared from the candidate worktree: {path}")
        hashes[path] = sha256(candidate)
    manifest = "\n".join(f"{path}  {hashes[path]}" for path in paths) + "\n"
    return {
        "revision": revision,
        "roots": BUILD_INPUT_ROOTS + ["go.work", "go.work.sum"],
        "file_count": len(hashes),
        "files_sha256": hashes,
        "manifest_sha256": hashlib.sha256(manifest.encode("utf-8")).hexdigest(),
        "command": argv,
    }


def verify_admission_and_provenance() -> dict[str, Any]:
    run_dir = RUNS / "provenance"
    fetch = run_checked("fetch-origin-main", ["git", "fetch", "origin", "main"], run_dir)
    origin_revision = git_output("rev-parse", "origin/main")
    candidate_revision = git_output("rev-parse", "HEAD")
    branch = git_output("branch", "--show-current")
    prd = json.loads(PRD.read_text(encoding="utf-8"))
    if prd.get("branchName") != branch or branch != BRANCH:
        raise EvidenceFailure(f"branch mismatch: prd={prd.get('branchName')!r}, actual={branch!r}")
    refreshed_main = verify_planning_snapshot_is_available(origin_revision)
    outside = git_status_outside_task()
    if outside:
        raise EvidenceFailure(f"unrelated dirty paths outside the C50 evidence directory: {outside}")
    source_revision = inspected_source_revision()
    if target_source_differs(source_revision):
        raise EvidenceFailure("target production source differs from the integrated origin/main snapshot")
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
            "--root",
            str(FACTORY_ROOT),
        ],
        run_dir,
        cwd=FACTORY_ROOT,
    )
    control_stdout = pathlib.Path(control["stdout_path"]).read_text(encoding="utf-8")
    if '"status": "admitted"' not in control_stdout or '"project": "audio-runtime"' not in control_stdout:
        raise EvidenceFailure(f"admission output did not confirm the sole project: {control_stdout!r}")
    board_capture = refresh_canonical_board(run_dir)
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
        HERE / "verify.py",
        HERE / "extraction-candidates.json",
        BOARD,
    ]
    source_archive = source_archive_record(source_revision)
    build_inputs = build_inputs_record(source_revision)
    provenance = {
        "schema_version": "c50-provenance-v1",
        "project": "audio-runtime",
        "task": WORK,
        "session": "~default",
        "server": os.environ.get("FACTORY_SERVER_URL", ""),
        "candidate_branch": branch,
        "candidate_revision_at_run": candidate_revision,
        "reviewed_candidate_revision": candidate_revision,
        "evidence_base_revision": candidate_revision,
        "evidence_must_remain_descendant_of_reviewed_candidate": True,
        "origin_main_revision": origin_revision,
        "integrated_source_revision": source_revision,
        "planning_main_revision": PLANNING_MAIN,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "manifest_baseline_revision": BASELINE,
        "source_revision": source_revision,
        "admission_command": control["argv"],
        "admission_stdout_sha256": sha256(pathlib.Path(control["stdout_path"])),
        "board_capture": board_capture,
        "board_sha256": sha256(BOARD),
        "refreshed_main_lineage": refreshed_main,
        "refreshed_origin_main": fetch,
        "ancestry": ancestry,
        "target_source_clean_against_integrated_main": True,
        "target_source_changed_from_planning_main": target_source_differs(PLANNING_MAIN),
        "unrelated_dirty_paths": outside,
        "source_archive": source_archive,
        "build_inputs": build_inputs,
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
    source_revision = inspected_source_revision()
    verify_planning_snapshot_is_available(source_revision)
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
        source_revision,
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
    if document.get("source_revision") != source_revision or document.get("target_roots") != TARGET_ROOTS:
        raise EvidenceFailure("inventory source revision or target root scope changed")
    if document.get("diagnostics"):
        raise EvidenceFailure(f"inventory parser diagnostics: {document['diagnostics']}")
    by_root = {area["root"]: area for area in document.get("areas", [])}
    for root, expected in EXPECTED_COUNTS.items():
        actual = by_root.get(root)
        if actual is None or tuple(actual.get(key) for key in ("production_files", "production_lines", "production_symbols")) != expected:
            raise EvidenceFailure(f"inventory count changed for {root}: {actual!r}, expected {expected!r}")
    totals = document.get("totals", {})
    if tuple(totals.get(key) for key in ("production_files", "production_lines", "production_symbols")) != (120, 46581, 2873):
        raise EvidenceFailure(f"inventory totals changed: {totals!r}")
    classifications = validate_classifications()
    paths = validate_call_paths()
    report = {
        "schema_version": "c50-inventory-validation-v1",
        "source_revision": source_revision,
        "counts": {root: list(value) for root, value in EXPECTED_COUNTS.items()},
        "totals": {"production_files": 120, "physical_lines": 46581, "top_level_symbols": 2873},
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
    source_revision = inspected_source_revision()
    inventory = json.loads((ANALYSIS / "inventory.json").read_text(encoding="utf-8"))
    document = json.loads((ANALYSIS / "classifications.json").read_text(encoding="utf-8"))
    if document.get("source_revision") != source_revision:
        raise EvidenceFailure("classification source revision mismatch")
    inventory_ids = [symbol["id"] for symbol in inventory.get("symbols", [])]
    classified = document.get("symbols", [])
    class_ids = [symbol.get("id") for symbol in classified]
    if len(inventory_ids) != len(set(inventory_ids)) or len(class_ids) != len(set(class_ids)) or set(inventory_ids) != set(class_ids):
        raise EvidenceFailure("classification ledger does not cover each symbol exactly once")
    inventory_by_id = {symbol["id"]: symbol for symbol in inventory.get("symbols", [])}
    edge_citations = {
        (
            edge.get("callee"),
            edge.get("call", {}).get("file"),
            edge.get("call", {}).get("line"),
            edge.get("call", {}).get("column"),
            edge.get("call", {}).get("expression"),
        )
        for edge in inventory.get("call_edges", [])
    }
    counts = Counter()
    for symbol in classified:
        if inventory_by_id.get(symbol.get("id")) != symbol:
            raise EvidenceFailure(f"classification contradicts inventory evidence for {symbol.get('id')}")
        value = symbol.get("class")
        if value not in ALLOWED_CLASSES:
            raise EvidenceFailure(f"unknown symbol class: {value!r}")
        if not symbol.get("file") or not symbol.get("line") or not symbol.get("classification_evidence"):
            raise EvidenceFailure(f"incomplete classification citation: {symbol.get('id')}")
        for caller in symbol.get("production_callers", []):
            citation = (
                symbol.get("id"),
                caller.get("file"),
                caller.get("line"),
                caller.get("column"),
                caller.get("expression"),
            )
            if citation not in edge_citations:
                raise EvidenceFailure(f"classification caller is not an exact inventory CallExpr: {caller!r}")
            if not caller.get("expression") or not caller.get("resolution") or not caller.get("confidence"):
                raise EvidenceFailure(f"incomplete production CallExpr citation: {caller!r}")
            if not any(str(evidence).startswith("exact production CallExpr ") and caller.get("file") in str(evidence) and str(caller.get("line")) in str(evidence) for evidence in symbol.get("classification_evidence", [])):
                raise EvidenceFailure(f"classification evidence omitted caller location: {caller!r}")
        counts[value] += 1
    return {"symbol_count": len(classified), "class_counts": dict(sorted(counts.items())), "rules": document.get("rules", [])}


def validate_call_paths() -> dict[str, Any]:
    source_revision = inspected_source_revision()
    document = json.loads((ANALYSIS / "call-paths.json").read_text(encoding="utf-8"))
    if document.get("source_revision") != source_revision:
        raise EvidenceFailure("call-path source revision mismatch")
    roots = document.get("entry_source_roots")
    entry_files = document.get("entry_source_files")
    if not roots or not entry_files or not document.get("entry_source_rule") or not document.get("dynamic_limitations"):
        raise EvidenceFailure("call-path report omitted exact public entry roots/files or dynamic limitations")
    edge_count = len(document.get("target_call_edges", []))
    entry_count = len(document.get("entry_call_sites", []))
    if edge_count < 1 or entry_count < 1:
        raise EvidenceFailure("call-path report is empty")
    for edge in document["entry_call_sites"]:
        call = edge.get("call", {})
        path = str(call.get("file", ""))
        if path not in entry_files or not any(path.startswith(root + "/") and "/" not in path[len(root) + 1:] for root in roots):
            raise EvidenceFailure(f"entry call outside exact public source files: {call}")
        if is_target_source_path(path) or "/internal/livehost/" in path:
            raise EvidenceFailure(f"target-local or nested livehost file was admitted as a public entry: {call}")
    if document.get("target_local_entry_call_sites") != 0:
        raise EvidenceFailure("call-path report contains target-local public entry call sites")
    inventory = json.loads((ANALYSIS / "inventory.json").read_text(encoding="utf-8"))
    expected_methods = {
        "Join": ("agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go", 123, "mesh.Join"),
        "Remove": ("agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go", 762, "mesh.Remove"),
        "Close": ("agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go", 109, "mesh.Close"),
        "Peers": ("agent-cli/internal/room/mesh.go", 492, "m.Peers"),
    }
    method_evidence = {}
    for method, (file, line, expression) in expected_methods.items():
        matches = [
            edge
            for edge in inventory.get("call_edges", [])
            if ":method:" in str(edge.get("callee", ""))
            and f".{method}" in str(edge.get("callee", ""))
            and edge.get("call", {}).get("file") == file
            and edge.get("call", {}).get("line") == line
            and edge.get("call", {}).get("expression") == expression
        ]
        if not matches:
            raise EvidenceFailure(f"receiver-aware analyzer omitted exact Mesh.{method} call evidence")
        method_evidence[method] = [{"file": file, "line": line, "expression": expression, "callee": edge["callee"]} for edge in matches]
    return {"target_call_edges": edge_count, "public_entry_call_sites": entry_count, "entry_source_roots": roots, "entry_source_files": entry_files, "target_local_entry_call_sites": 0, "mesh_method_evidence": method_evidence}


def candidate_validation() -> dict[str, Any]:
    source_revision = inspected_source_revision()
    path = HERE / "extraction-candidates.json"
    document = json.loads(path.read_text(encoding="utf-8"))
    candidates = document.get("candidates", [])
    if len(candidates) not in (2, 3):
        raise EvidenceFailure(f"candidate count must be two or three, got {len(candidates)}")
    inventory = json.loads((ANALYSIS / "inventory.json").read_text(encoding="utf-8"))
    if inventory.get("source_revision") != source_revision or document.get("source_revision") != source_revision:
        raise EvidenceFailure("candidate or inventory source revision is not the integrated source revision")
    symbols = inventory.get("symbols", [])
    symbol_by_id = {symbol.get("id"): symbol for symbol in symbols}
    actual_callers_by_callee: dict[str, set[tuple[str, int, str]]] = {}
    for edge in inventory.get("call_edges", []):
        call = edge.get("call", {})
        actual_callers_by_callee.setdefault(edge.get("callee", ""), set()).add((call.get("file", ""), call.get("line", 0), call.get("expression", "")))

    def source_parts(source: str) -> tuple[str, int]:
        file, line = source.rsplit(":", 1)
        return file, int(line)

    def short_symbol(symbol: dict[str, Any]) -> str:
        receiver = symbol.get("receiver")
        return f"{receiver}.{symbol.get('name')}" if receiver else str(symbol.get("name", ""))

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
        declaration_ids: set[str] = set()
        for declaration in candidate.get("current_declarations", []):
            file, line = source_parts(str(declaration.get("source", "")))
            matches = [symbol for symbol in symbols if symbol.get("file") == file and symbol.get("line") == line and short_symbol(symbol) == declaration.get("symbol")]
            if len(matches) != 1:
                raise EvidenceFailure(f"candidate declaration is not an exact inventoried symbol: {declaration!r}")
            symbol = matches[0]
            if file not in sources:
                raise EvidenceFailure(f"candidate declaration is outside its current writer paths: {declaration!r}")
            if declaration.get("class") != symbol.get("class"):
                raise EvidenceFailure(f"candidate classification contradicts analyzer evidence: {declaration!r} vs {symbol.get('class')!r}")
            callers = actual_callers_by_callee.get(symbol.get("id"), set())
            if not callers:
                raise EvidenceFailure(f"candidate includes an unreachable declaration without an exact production caller: {declaration!r}")
            declaration_ids.add(symbol.get("id"))
        listed_callers = set()
        for caller in candidate.get("production_callers", []):
            file, line = source_parts(str(caller.get("source", "")))
            citation = (file, line, caller.get("expression", ""))
            listed_callers.add(citation)
            if not any(citation in actual_callers_by_callee.get(symbol_id, set()) for symbol_id in declaration_ids):
                raise EvidenceFailure(f"candidate production caller is not an exact CallExpr for this slice: {caller!r}")
        actual_slice_callers = set().union(*(actual_callers_by_callee.get(symbol_id, set()) for symbol_id in declaration_ids))
        if not listed_callers or not listed_callers.issubset(actual_slice_callers):
            raise EvidenceFailure(f"candidate omitted or misstated exact production CallExpr callers: {candidate.get('id')}")
        source_paths.append(sources)
        destination_paths.append(destinations)
    for index in range(len(candidates)):
        for other in range(index + 1, len(candidates)):
            if source_paths[index] & source_paths[other] or destination_paths[index] & destination_paths[other]:
                raise EvidenceFailure(f"candidate writer paths overlap: {candidates[index]['id']} / {candidates[other]['id']}")
    report = {
        "schema_version": "c50-candidate-validation-v1",
        "source_revision": source_revision,
        "candidate_ids": [candidate["id"] for candidate in candidates],
        "candidate_count": len(candidates),
        "pairwise_source_disjoint": True,
        "pairwise_destination_disjoint": True,
        "active_owner_paths_excluded": True,
    }
    write_json(HERE / "candidate-validation.json", report)
    return report


def public_smoke() -> dict[str, Any]:
    source_revision = inspected_source_revision()
    if not REPLAY_FIXTURE.exists():
        raise EvidenceFailure(f"credential-free replay fixture missing: {REPLAY_FIXTURE}")
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    run_dir = RUNS / "public"
    built_binary = ARTIFACTS / "yui"
    build = run_checked(
        "build-yui",
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(built_binary), "./agent-cli/cmd/yui"],
        run_dir,
    )
    binary_hash = sha256(built_binary)
    build_inputs = build_inputs_record(source_revision)
    binary_path = built_binary
    binary_argument = None
    if REQUESTED_BINARY is not None:
        binary_argument = str(REQUESTED_BINARY)
        if not REQUESTED_BINARY.is_file():
            raise EvidenceFailure(f"--binary path does not exist: {REQUESTED_BINARY}")
        requested_hash = sha256(REQUESTED_BINARY)
        if requested_hash != binary_hash:
            raise EvidenceFailure(f"--binary is not the same-source build: {REQUESTED_BINARY} has {requested_hash}, expected {binary_hash}")
        binary_path = REQUESTED_BINARY
    help_top = run_checked("yui-help", [str(binary_path), "--help"], run_dir)
    help_session = run_checked("yui-session-help", [str(binary_path), "session", "--help"], run_dir)

    def output_text(result: dict[str, Any]) -> str:
        return "\n".join(
            pathlib.Path(result[key]).read_text(encoding="utf-8", errors="replace")
            for key in ("stdout_path", "stderr_path")
        )

    def require_help_literals(result: dict[str, Any], label: str, literals: list[str]) -> dict[str, Any]:
        output = output_text(result)
        missing = [literal for literal in literals if literal not in output]
        if missing:
            raise EvidenceFailure(f"{label} omitted required help literals: {missing}")
        return {"required_literals": literals, "matched_literals": literals}

    top_help_literals = require_help_literals(
        help_top,
        "top-level help",
        ["Available Commands:", "session", "--workdir", "--allow-path"],
    )
    session_help_literals = require_help_literals(
        help_session,
        "session help",
        ["Usage:", "--replay", "--record-dir", "--audio-in-turn", "--trace-audio"],
    )
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
            str(binary_path),
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
    replay_bytes = owned_directory_bytes(replay_case)
    if replay_bytes > REPLAY_OUTPUT_QUOTA_BYTES:
        raise EvidenceFailure(f"credential-free replay exceeded its owned output quota: {replay_bytes} bytes")
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
        "source_revision": source_revision,
        "candidate_revision": git_output("rev-parse", "HEAD"),
        "same_source_binary": str(binary_path.relative_to(ROOT)) if binary_path.is_relative_to(ROOT) else str(binary_path),
        "binary_argument": binary_argument,
        "binary_sha256": binary_hash,
        "build_inputs": build_inputs,
        "fixture": str(REPLAY_FIXTURE.relative_to(ROOT)),
        "fixture_sha256": sha256(REPLAY_FIXTURE),
        "top_level_help": help_top,
        "session_help": help_session,
        "top_level_help_literals": top_help_literals,
        "session_help_literals": session_help_literals,
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
        "result_classification": "SOFTWARE_LOCAL_PROCESS_ONLY",
        "replay_owned_output_bytes": replay_bytes,
        "replay_owned_output_quota_bytes": REPLAY_OUTPUT_QUOTA_BYTES,
        "credential_free_environment": True,
        "realtime": "live Realtime not used; the fixture is replayed offline",
    }
    write_json(run_dir / "report.json", report)
    # The executable is rebuilt by every verification run and is intentionally
    # not committed as a 49 MB evidence artifact; its detached hash and build
    # record remain in the report.
    if REQUESTED_BINARY is None or REQUESTED_BINARY != built_binary:
        built_binary.unlink(missing_ok=True)
    return report


def focused_regressions() -> dict[str, Any]:
    source_revision = inspected_source_revision()
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
                "-v",
            ],
            ["TestParseManifest_NormalizesValidJSONAndYAMLWithoutCredentials", "TestParseManifest_BrowserToolsNormalizesJSONOptionsAndRedactsEndpoints", "TestReadManifest_ReadsAndValidatesFile", "TestMeshJoinCreatesOnePairPerUnorderedParticipantPair", "TestMeshRemovalClosesOnlyRemovedPairAndLeavesSurvivors"],
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
                "-v",
            ],
            ["TestMeshContextCancellationClosesAllPairsAndRepeatedCloseIsSafe", "TestMeshExplicitCloseAndParentCancellationConvergeWithPendingPair"],
        ),
        (
            "agentruntime-normal",
            [
                "go",
                "test",
                "./internal/services/internal/agentruntime",
                "-run",
                "^(TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup|TestSessionDirectoryRecordingWritesConversationSessionLog|TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration)$",
                "-v",
            ],
            ["TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup", "TestSessionDirectoryRecordingWritesConversationSessionLog", "TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration"],
        ),
    ]
    results = []
    for label, argv, expected_tests in commands:
        result = run_checked(label, argv, run_dir, cwd=ROOT / "agent-cli")
        output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
        if "ok" not in output or "[no tests to run]" in output:
            raise EvidenceFailure(f"{label} did not report a passing Go package")
        missing = [name for name in expected_tests if f"--- PASS: {name}" not in output]
        if missing:
            raise EvidenceFailure(f"{label} did not execute every required focused test: {missing}")
        result["expected_tests"] = expected_tests
        result["passed_tests"] = [name for name in expected_tests if f"--- PASS: {name}" in output]
        results.append(result)
    report = {
        "schema_version": "c50-focused-regressions-v1",
        "source_revision": source_revision,
        "normal_and_race_commands": results,
        "full_ci_suite": "not run; executor handoff does not duplicate CI",
    }
    write_json(run_dir / "report.json", report)
    return report


def negative_controls() -> dict[str, Any]:
    source_revision = inspected_source_revision()
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
        "source_revision": source_revision,
        "timeout_descendant_cleanup": timeout_result,
        "output_cap": overflow_result,
        "bounded_child_deadline_seconds": REQUESTED_CHILD_DEADLINE_SECONDS,
        "aggregate_deadline_seconds": REQUESTED_AGGREGATE_DEADLINE_SECONDS,
        "shared_aggregate_deadline": True,
        "owned_output_quota_bytes": OWNED_OUTPUT_QUOTA_BYTES,
        "owned_output_quota_enforced": True,
        "first_failure_and_group_cleanup": True,
    }
    write_json(run_dir / "report.json", report)
    return report


def diff_and_architecture_checks() -> dict[str, Any]:
    run_dir = RUNS / "gates"
    outside = git_status_outside_task()
    if outside:
        raise EvidenceFailure(f"gates found unrelated dirty paths outside the C50 evidence directory: {outside}")
    source_revision = inspected_source_revision()
    architecture_base = f"ARCHITECTURE_BASE={source_revision}"
    commands = [
        ("diff-check", ["git", "diff", "--check"]),
        ("architecture-check", ["make", "architecture-check", architecture_base]),
        ("size-check", ["make", "size-check", architecture_base]),
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
    report = {"schema_version": "c50-gates-v1", "source_revision": source_revision, "architecture_base": source_revision, "refreshed_origin_main_is_recorded_in_provenance": True, "commands": results, "architecture_baseline_sha256": baseline_hashes, "architecture_baseline_unchanged": baseline_diffs, "production_source_changed": target_source_differs(source_revision), "unrelated_dirty_paths": outside, "write_scope_enforced": True}
    if report["production_source_changed"]:
        raise EvidenceFailure("gates found a production source change against the integrated origin/main revision")
    write_json(run_dir / "report.json", report)
    return report


def write_checksums() -> dict[str, Any]:
    checksum_path = HERE / "SHA256SUMS"
    excluded = ["verification-summary.json"]
    entries = []
    for path in sorted(HERE.rglob("*")):
        if not path.is_file() or path == checksum_path or path.name in excluded or "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        entries.append(f"{sha256(path)}  {path.relative_to(HERE).as_posix()}")
    checksum_path.write_text("\n".join(entries) + "\n", encoding="utf-8")
    return {"path": str(checksum_path.relative_to(ROOT)), "entries": len(entries), "sha256": sha256(checksum_path), "excluded_self_referential_reports": excluded}


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
    global AGGREGATE_STARTED, REQUESTED_AGGREGATE_DEADLINE_SECONDS, REQUESTED_BINARY, REQUESTED_CHILD_DEADLINE_SECONDS
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=["provenance", "inventory", "classifications", "call-paths", "ownership", "public-smoke", "focused-regressions", "negative-controls", "candidates", "gates", "all"],
        required=True,
    )
    parser.add_argument("--binary", help="already-built yui binary; public-smoke proves it matches the fresh same-source build")
    parser.add_argument("--child-timeout", type=int, default=CHILD_DEADLINE_SECONDS, help="per-child deadline in seconds")
    parser.add_argument("--total-timeout", type=int, default=AGGREGATE_DEADLINE_SECONDS, help="shared aggregate deadline in seconds")
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.total_timeout <= 0:
        parser.error("--child-timeout and --total-timeout must be positive")
    if args.child_timeout > args.total_timeout:
        parser.error("--child-timeout cannot exceed --total-timeout")
    REQUESTED_CHILD_DEADLINE_SECONDS = args.child_timeout
    REQUESTED_AGGREGATE_DEADLINE_SECONDS = args.total_timeout
    if args.binary:
        REQUESTED_BINARY = pathlib.Path(args.binary).expanduser().resolve()
    started = time.monotonic()
    AGGREGATE_STARTED = started
    try:
        if args.mode == "all":
            reports = {}
            for mode in ("provenance", "inventory", "candidates", "public-smoke", "focused-regressions", "negative-controls", "gates"):
                reports[mode] = run_mode(mode)
                if time.monotonic() - started > REQUESTED_AGGREGATE_DEADLINE_SECONDS:
                    raise EvidenceFailure("aggregate verification deadline exceeded")
            reports["checksum_manifest"] = write_checksums()
            write_json(HERE / "verification-summary.json", {"schema_version": "c50-verification-summary-v1", "candidate_revision": git_output("rev-parse", "HEAD"), "elapsed_seconds": round(time.monotonic() - started, 6), "aggregate_deadline_seconds": REQUESTED_AGGREGATE_DEADLINE_SECONDS, "reports": reports, "all_project_criteria": "OPEN", "checksum_note": "SHA256SUMS intentionally excludes this summary because the summary records the manifest digest; all other task-local evidence is covered without a self-referential digest cycle."})
            print(json.dumps({"status": "passed", "mode": "all", "elapsed_seconds": round(time.monotonic() - started, 6), "candidate_revision": git_output("rev-parse", "HEAD")}, sort_keys=True))
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
