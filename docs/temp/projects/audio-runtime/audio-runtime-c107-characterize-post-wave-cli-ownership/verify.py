#!/usr/bin/env python3
"""Bounded, fail-closed evidence driver for C107.

The driver reads the admitted board and open PR metadata, analyzes only the
immutable accepted-main source, and writes only below this task directory. It
does not poll CI, use Realtime credentials, edit production, or touch a peer
worktree.
"""

from __future__ import annotations

import argparse
import datetime as dt
import errno
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
from collections import Counter
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
FACTORY_SERVER = os.environ.get("FACTORY_SERVER_URL", "")
WORK = "audio-runtime-c107-characterize-post-wave-cli-ownership"
BRANCH = "codex/audio-runtime-c107-characterize-post-wave-cli-ownership"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
SOURCE_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
TARGET_ROOT = "agent-cli/internal/services/internal/agentruntime"
SHARED_PATHS = {
    "scripts/wire-packages.txt": "C79 exclusive shared Wire registry lease; later dependency, never C107 ownership",
    "docs/architecture/architecture-size-baseline.json": "C79 exclusive architecture baseline lease; later dependency, never C107 ownership",
}
REPLAY_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
ANALYZER = HERE / "analyze.py"
PRD = ROOT / "prd.json"
MAX_OUTPUT_BYTES = 2 * 1024 * 1024
MAX_TASK_OUTPUT_BYTES = 128 * 1024 * 1024
CHILD_TIMEOUT = 60
TOTAL_TIMEOUT = 600
AGGREGATE_STARTED = 0.0
REQUESTED_CHILD_TIMEOUT = CHILD_TIMEOUT
REQUESTED_TOTAL_TIMEOUT = TOTAL_TIMEOUT
CANONICAL_INVENTORY_SHAPE: dict[str, Any] | None = None
SOURCE_SERVICE_DEPENDENCIES: dict[str, list[str] | str] = {}

REQUIRED_ACTIVE_NAMES = {
    "audio-runtime-c79-retire-cli-response-lifecycle",
    "audio-runtime-c83-retire-cli-browser-scenario-runner",
    "audio-runtime-c84-retire-cli-room-participant-lifecycle",
    "audio-runtime-c99-retire-cli-room-participant-planning",
    "audio-runtime-c101-retire-cli-provider-session-runtime",
    "audio-runtime-c103-retire-cli-rtc-session-runtime",
    "audio-runtime-c104-retire-cli-session-finalization-boundary",
}

ALLOWED_CLASSES = {
    "thin_cli_transport_presentation",
    "deprecated_adapter",
    "reusable_runtime_behavior",
    "business_policy",
    "composition_ownership",
    "device_audio_implementation",
    "dead_uncertain",
}
SHA256_RE = re.compile(r"^[0-9a-f]{40}$")
RETIREMENT_KEYWORDS = ("retire", "extract", "runtime", "audio")


class EvidenceFailure(RuntimeError):
    pass


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def stable_json(value: Any) -> str:
    return json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(stable_json(value), encoding="utf-8")


def read_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"invalid JSON evidence: {path}") from exc


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def evidence_path(path: pathlib.Path) -> str:
    path = path.resolve()
    for root, label in ((ROOT, "worktree"), (FACTORY_ROOT, "factory")):
        try:
            return f"{label}/{path.relative_to(root).as_posix()}"
        except ValueError:
            continue
    return str(path)


def redact_text(value: str) -> str:
    value = re.sub(r"(?i)(api[_-]?key|token|secret|password|credential)\s*[:=]\s*[^\s,}]+", r"\1=<redacted>", value)
    value = re.sub(r"\bsk-[A-Za-z0-9_-]{12,}\b", "<redacted-api-key>", value)
    return value


def sanitize(value: Any, key: str = "") -> Any:
    lowered = key.lower()
    if any(marker in lowered for marker in ("apikey", "api_key", "token", "secret", "password", "credential")):
        if isinstance(value, (str, int, float, bool)) or value is None:
            return "<redacted>"
    if isinstance(value, dict):
        return {str(k): sanitize(v, str(k)) for k, v in value.items()}
    if isinstance(value, list):
        return [sanitize(item, key) for item in value]
    if isinstance(value, str):
        return redact_text(value)
    return value


def safe_environment() -> dict[str, str]:
    secret_markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    result = {key: value for key, value in os.environ.items() if not any(marker in key.upper() for marker in secret_markers)}
    for key in ("PATH", "HOME", "TMPDIR", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL"):
        if key in os.environ:
            result[key] = os.environ[key]
    return result


def remaining_time() -> float:
    return REQUESTED_TOTAL_TIMEOUT - (time.monotonic() - AGGREGATE_STARTED)


def require_time(label: str) -> None:
    if AGGREGATE_STARTED and remaining_time() <= 0:
        raise EvidenceFailure(f"aggregate deadline exhausted before {label}")


def git(*args: str, text: bool = True) -> str | bytes:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=text,
        timeout=min(120, max(1, int(max(1, remaining_time())))),
    )
    return result.stdout.strip() if text else result.stdout


def git_show(revision: str, path: str) -> bytes:
    result = subprocess.run(
        ["git", "show", f"{revision}:{path}"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        timeout=min(120, max(1, int(max(1, remaining_time())))),
    )
    return result.stdout


def process_group_gone(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except OSError as exc:
        return exc.errno == errno.ESRCH
    return False


def wait_group_gone(pid: int, timeout: float) -> bool:
    deadline = time.monotonic() + max(0.0, timeout)
    while True:
        if process_group_gone(pid):
            return True
        left = deadline - time.monotonic()
        if left <= 0:
            return False
        time.sleep(min(0.05, left))


def terminate_group(process: subprocess.Popen[bytes], reason: str) -> dict[str, Any]:
    result: dict[str, Any] = {"reason": reason, "term_sent": False, "kill_sent": False}
    try:
        os.killpg(process.pid, signal.SIGTERM)
        result["term_sent"] = True
    except ProcessLookupError:
        pass
    except OSError as exc:
        result["term_error"] = str(exc)
    try:
        process.wait(timeout=min(1.0, max(0.05, remaining_time())))
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
            result["kill_sent"] = True
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=min(2.0, max(0.05, remaining_time())))
        except subprocess.TimeoutExpired:
            result["wait_timeout"] = True
    if not wait_group_gone(process.pid, min(1.0, max(0.05, remaining_time()))):
        result["residual_group_detected"] = True
        try:
            os.killpg(process.pid, signal.SIGKILL)
            result["residual_kill_sent"] = True
        except ProcessLookupError:
            pass
        result["residual_group_gone"] = wait_group_gone(process.pid, min(2.0, max(0.05, remaining_time())))
    else:
        result["residual_group_gone"] = True
    return result


def run_bounded(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    *,
    timeout: int | None = None,
    env: dict[str, str] | None = None,
    output_cap: int = MAX_OUTPUT_BYTES,
) -> dict[str, Any]:
    require_time(label)
    timeout = REQUESTED_CHILD_TIMEOUT if timeout is None else timeout
    timeout = min(timeout, max(1, int(max(1, remaining_time()))))
    run_dir.mkdir(parents=True, exist_ok=True)
    safe_label = re.sub(r"[^A-Za-z0-9_.-]", "_", label)
    stdout_path = run_dir / f"{safe_label}.stdout"
    stderr_path = run_dir / f"{safe_label}.stderr"
    result_path = run_dir / f"{safe_label}.json"
    selected_env = safe_environment()
    if env:
        selected_env.update(env)
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected_env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    termination: dict[str, Any] | None = None
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        termination = terminate_group(process, "child-deadline")
        stdout, stderr = process.communicate(timeout=min(2.0, max(0.05, remaining_time())))
        if exc.stdout:
            stdout = exc.stdout if isinstance(exc.stdout, bytes) else str(exc.stdout).encode()
        if exc.stderr:
            stderr = exc.stderr if isinstance(exc.stderr, bytes) else str(exc.stderr).encode()
    stdout = redact_text(stdout.decode("utf-8", errors="replace"))
    stderr = redact_text(stderr.decode("utf-8", errors="replace"))
    output_limited = len(stdout.encode()) > output_cap or len(stderr.encode()) > output_cap
    if output_limited:
        stdout = stdout[:output_cap]
        stderr = stderr[:output_cap]
        if termination is None:
            termination = terminate_group(process, "output-cap")
    stdout_path.write_text(stdout, encoding="utf-8")
    stderr_path.write_text(stderr, encoding="utf-8")
    gone = process_group_gone(process.pid)
    if not gone:
        residual = terminate_group(process, "residual-process-group")
        termination = {**(termination or {}), **residual}
        gone = process_group_gone(process.pid)
    record = {
        "schema_version": "c107-command-run-v1",
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": {key: selected_env.get(key, "") for key in ("PATH", "HOME", "TMPDIR", "GOWORK", "FACTORY_ROOT", "FACTORY_SERVER_URL")},
        "deadline_seconds": timeout,
        "aggregate_deadline_seconds": REQUESTED_TOTAL_TIMEOUT,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "stdout_bytes": len(stdout.encode()),
        "stderr_bytes": len(stderr.encode()),
        "stdout_path": str(stdout_path.relative_to(HERE)),
        "stderr_path": str(stderr_path.relative_to(HERE)),
        "process_group_id": process.pid,
        "process_group_gone": gone,
        "termination": termination,
    }
    write_json(result_path, record)
    return record


def require_success(record: dict[str, Any], label: str | None = None) -> None:
    name = label or str(record.get("label"))
    if record.get("timed_out") or record.get("output_limited") or record.get("exit_code") != 0 or not record.get("process_group_gone"):
        raise EvidenceFailure(f"{name} failed; inspect {record.get('stderr_path')} and {record.get('stdout_path')}")


def command_json(argv: list[str], *, timeout: int = 90, env: dict[str, str] | None = None) -> Any:
    require_time(" ".join(argv[:3]))
    selected_env = dict(os.environ if env is None else env)
    result = subprocess.run(argv, cwd=ROOT, env=selected_env, capture_output=True, text=True, timeout=min(timeout, max(1, int(max(1, remaining_time())))))
    if result.returncode != 0:
        raise EvidenceFailure(f"command failed ({result.returncode}): {' '.join(argv)}: {redact_text(result.stderr).strip()}")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"command did not return JSON: {' '.join(argv)}") from exc


def source_production_paths() -> list[str]:
    raw = str(git("ls-tree", "-r", "--name-only", SOURCE_REVISION, "--", TARGET_ROOT))
    paths: list[str] = []
    for path in raw.splitlines():
        if not path.endswith(".go") or path.endswith("_test.go") or path.endswith("_gen.go"):
            continue
        try:
            prefix = git_show(SOURCE_REVISION, path)[:4096].decode("utf-8", errors="replace")
        except subprocess.CalledProcessError as exc:
            raise EvidenceFailure(f"accepted source path disappeared: {path}") from exc
        if "Code generated" in prefix:
            continue
        paths.append(path)
    return sorted(paths)


def source_counts() -> dict[str, Any]:
    paths = source_production_paths()
    lines = 0
    bytes_count = 0
    for path in paths:
        data = git_show(SOURCE_REVISION, path)
        lines += len(data.splitlines())
        bytes_count += len(data)
    return {"production_files": len(paths), "physical_lines": lines, "bytes": bytes_count, "paths": paths}


def source_service_dependencies(path: str) -> list[str] | str:
    """Derive runtime-service imports from the pinned source, not evidence rows."""
    cached = SOURCE_SERVICE_DEPENDENCIES.get(path)
    if cached is not None:
        return cached
    source = git_show(SOURCE_REVISION, path).decode("utf-8", errors="replace").splitlines()
    imports: set[str] = set()
    index = 0
    while index < len(source):
        match = re.match(r"^[ \t]*import[ \t]+(.*)$", source[index])
        if not match:
            index += 1
            continue
        declaration = match.group(1)
        if declaration.lstrip().startswith("("):
            block = [declaration[declaration.index("(") + 1 :]]
            index += 1
            while index < len(source):
                if re.match(r"^[ \t]*\)", source[index]):
                    break
                block.append(source[index])
                index += 1
            declaration = "\n".join(block)
        for literal in re.finditer(r'"([^"\\]*(?:\\.[^"\\]*)*)"|`([^`]*)`', declaration):
            imported = literal.group(1) if literal.group(1) is not None else literal.group(2)
            if "/go-agent-runtime/services/" in imported:
                imports.add(imported)
        index += 1
    result: list[str] | str = sorted(imports) if imports else "NONE"
    SOURCE_SERVICE_DEPENDENCIES[path] = result
    return result


def factory_status() -> Any:
    command = [sys.executable, str(FACTORY_ROOT / "factory/scripts/project-control.py"), "status"]
    return command_json(command)


def verify_work() -> Any:
    command = [
        sys.executable,
        str(FACTORY_ROOT / "factory/scripts/project-control.py"),
        "verify-work",
        "--type",
        "task",
        "--name",
        WORK,
    ]
    result = command_json(command)
    if result != {"status": "admitted", "project": PROJECT, "name": WORK}:
        raise EvidenceFailure(f"unexpected admission identity: {result!r}")
    return result


def admitted_prd_identity() -> dict[str, Any]:
    document = read_json(PRD)
    identity = {"project": document.get("project"), "branchName": document.get("branchName")}
    if identity != {"project": PROJECT, "branchName": BRANCH}:
        raise EvidenceFailure(f"prd.json project/branch identity mismatch: {identity!r}")
    return identity


def retirement_classification(item: dict[str, Any], source_paths: set[str]) -> tuple[bool, dict[str, Any]]:
    files = item.get("files")
    if not isinstance(files, list) or any(not isinstance(path, str) or not path for path in files):
        raise EvidenceFailure(f"open PR has incomplete file inventory: {item.get('number')!r}")
    source_targets = sorted(set(files) & source_paths)
    title_branch = f"{item.get('title', '')} {item.get('headRefName', item.get('head_branch', ''))}".lower()
    keyword_matches = sorted(keyword for keyword in RETIREMENT_KEYWORDS if keyword in title_branch)
    classification = {
        "accepted_main_target_paths": source_targets,
        "title_branch_keywords": keyword_matches,
    }
    return bool(source_targets or keyword_matches), classification


def validate_pr_inventory(prs: dict[str, Any], source_paths: set[str]) -> dict[int, dict[str, Any]]:
    """Validate the complete open-PR identity set before using it for subtraction.

    The subtraction ledger is derived from this document, so the document must
    carry a complete, unique and reproducible identity set.  In particular,
    `is_retirement_checkpoint` is only a cached result; it is re-derived from
    the observed file list and title/branch rather than trusted as an input.
    """
    if prs.get("schema_version") != "c107-open-pr-inventory-v1":
        raise EvidenceFailure("open PR inventory schema is not pinned")
    records = prs.get("prs")
    if not isinstance(records, list):
        raise EvidenceFailure("open PR inventory omitted its complete records")
    by_number: dict[int, dict[str, Any]] = {}
    for item in records:
        if not isinstance(item, dict) or not isinstance(item.get("number"), int):
            raise EvidenceFailure(f"open PR has invalid numeric identity: {item!r}")
        number = item["number"]
        if number in by_number:
            raise EvidenceFailure(f"open PR inventory duplicated identity: {number}")
        for key in ("head_branch", "base_branch", "head", "base"):
            value = item.get(key)
            if not isinstance(value, str) or not value:
                raise EvidenceFailure(f"open PR {number} is missing {key}")
        if not SHA256_RE.fullmatch(item["head"]) or not SHA256_RE.fullmatch(item["base"]):
            raise EvidenceFailure(f"open PR {number} has an unpinned head/base SHA")
        files = item.get("files")
        if not isinstance(files, list) or files != sorted(set(files)):
            raise EvidenceFailure(f"open PR {number} has incomplete or unstable file identity")
        is_retirement, classification = retirement_classification(item, source_paths)
        if item.get("accepted_main_changed_production_paths") != classification["accepted_main_target_paths"]:
            raise EvidenceFailure(f"open PR {number} changed-source inventory is not derived from its files")
        if item.get("is_retirement_checkpoint") is not is_retirement:
            raise EvidenceFailure(f"open PR {number} retirement classification is inconsistent")
        recorded_classification = item.get("retirement_classification")
        if recorded_classification != classification:
            raise EvidenceFailure(f"open PR {number} retirement classification evidence is missing or stale")
        by_number[number] = item
    all_numbers = sorted(by_number)
    if prs.get("all_open_pr_numbers") != all_numbers:
        raise EvidenceFailure("open PR inventory all_open_pr_numbers is incomplete")
    retirement_numbers = sorted(number for number, item in by_number.items() if item["is_retirement_checkpoint"])
    if prs.get("all_open_retirement_pr_numbers") != retirement_numbers:
        raise EvidenceFailure("open PR inventory all_open_retirement_pr_numbers is incomplete")
    return by_number


def capture_board() -> tuple[dict[str, Any], pathlib.Path]:
    if not FACTORY_SERVER:
        raise EvidenceFailure("FACTORY_SERVER_URL is unavailable; cannot capture canonical board")
    command = ["you", "--server", FACTORY_SERVER, "--json", "work", "list", "--session", "~default", "--max-results", "500", "--all"]
    result = command_json(command)
    if not isinstance(result, dict) or not isinstance(result.get("results"), list):
        raise EvidenceFailure("canonical board is not a {results:[...]} document")
    capture = {"schema_version": "c107-canonical-board-v1", "observed_at": now(), "command": command, "results": sanitize(result["results"])}
    path = HERE / "canonical-board.json"
    write_json(path, capture)
    return capture, path


def capture_pr_inventory(source_paths: set[str], board_results: list[dict[str, Any]]) -> tuple[dict[str, Any], pathlib.Path]:
    list_command = ["gh", "pr", "list", "--repo", "portpowered/go-agent-harness", "--state", "open", "--limit", "500", "--json", "number,title,headRefName,baseRefName,headRefOid,baseRefOid,isDraft,url"]
    prs = command_json(list_command)
    if not isinstance(prs, list):
        raise EvidenceFailure("open PR inventory is not a list")
    task_rows = {row.get("name"): row for row in board_results if row.get("workTypeName") == "task"}
    records: list[dict[str, Any]] = []
    for item in sorted(prs, key=lambda row: int(row.get("number", 0))):
        number = item.get("number")
        if not isinstance(number, int) or not item.get("headRefOid") or not item.get("baseRefOid"):
            raise EvidenceFailure(f"open PR lacks pinned identity: {item!r}")
        view_command = ["gh", "pr", "view", str(number), "--repo", "portpowered/go-agent-harness", "--json", "files"]
        detail = command_json(view_command)
        files = sorted({row.get("path") for row in detail.get("files", []) if isinstance(row, dict) and isinstance(row.get("path"), str)})
        branch = str(item.get("headRefName"))
        candidate_name = branch.removeprefix("codex/")
        task = task_rows.get(candidate_name)
        is_retirement, classification = retirement_classification({**item, "files": files}, source_paths)
        records.append(
            {
                "number": number,
                "title": item.get("title"),
                "state": "OPEN",
                "head_branch": branch,
                "base_branch": item.get("baseRefName"),
                "head": item.get("headRefOid"),
                "base": item.get("baseRefOid"),
                "draft": bool(item.get("isDraft")),
                "url": item.get("url"),
                "files": files,
                "accepted_main_changed_production_paths": classification["accepted_main_target_paths"],
                "is_retirement_checkpoint": is_retirement,
                "retirement_classification": classification,
                "task_work_id": task.get("workId") if task else "NONE",
                "task_name": candidate_name if task else "NONE",
                "task_state": task.get("state") if task else "NONE",
                "observation_commands": [list_command, view_command],
            }
        )
    capture = {
        "schema_version": "c107-open-pr-inventory-v1",
        "observed_at": now(),
        "commands": [list_command],
        "prs": records,
        "all_open_pr_numbers": sorted(record["number"] for record in records),
        "all_open_retirement_pr_numbers": sorted(record["number"] for record in records if record["is_retirement_checkpoint"]),
    }
    validate_pr_inventory(capture, source_paths)
    path = HERE / "pr-inventory.json"
    write_json(path, capture)
    return capture, path


def validate_current_pr_identity(prs: dict[str, Any]) -> dict[str, Any]:
    """Require the captured open PR to be the exact local submitted head.

    Evidence is only reviewable when the checked-in PR inventory describes the
    branch being handed to the script gate.  Cached inventory from a previous
    evidence commit must fail closed instead of allowing a clean-but-stale
    release to be submitted.
    """
    candidate = str(git("rev-parse", "HEAD"))
    branch = str(git("branch", "--show-current"))
    matches = [
        row
        for row in prs.get("prs", [])
        if isinstance(row, dict) and (row.get("task_name") == WORK or row.get("head_branch") == BRANCH)
    ]
    if len(matches) != 1:
        raise EvidenceFailure(f"expected exactly one open PR for {WORK}, found {len(matches)}")
    row = matches[0]
    if row.get("task_name") != WORK or row.get("head_branch") != BRANCH:
        raise EvidenceFailure(f"candidate PR identity is not bound to {WORK}: {row!r}")
    if row.get("state") != "OPEN" or row.get("base_branch") != "main":
        raise EvidenceFailure(f"candidate PR is not an open main PR: {row!r}")
    if row.get("head") != candidate:
        raise EvidenceFailure(
            f"candidate PR head {row.get('head')} does not match local HEAD {candidate}; push the exact submitted head before release"
        )
    if branch != BRANCH:
        raise EvidenceFailure(f"candidate PR branch mismatch: {branch!r} != {BRANCH!r}")
    return {
        "number": row.get("number"),
        "head": row.get("head"),
        "base": row.get("base"),
        "head_branch": row.get("head_branch"),
        "base_branch": row.get("base_branch"),
        "task_name": row.get("task_name"),
        "task_work_id": row.get("task_work_id"),
    }


def active_task_rows(board_results: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows = []
    for row in board_results:
        if row.get("workTypeName") != "task" or not str(row.get("name", "")).startswith("audio-runtime-"):
            continue
        state = row.get("state") or {}
        if state.get("type") == "TERMINAL" or state.get("name") in {"failed", "fin", "complete"}:
            continue
        rows.append(row)
    return sorted(rows, key=lambda row: str(row.get("name", "")))


def review_findings(board_results: list[dict[str, Any]]) -> dict[str, Any]:
    rows = []
    for row in board_results:
        if row.get("workTypeName") not in {"review", "task"}:
            continue
        tags = row.get("tags") if isinstance(row.get("tags"), dict) else {}
        feedback = tags.get("_rejection_feedback")
        if feedback or tags.get("_last_output"):
            rows.append(
                {
                    "work_id": row.get("workId"),
                    "name": row.get("name"),
                    "work_type": row.get("workTypeName"),
                    "state": row.get("state"),
                    "rejection_feedback": feedback or "",
                    "last_output": tags.get("_last_output", ""),
                }
            )
    return {"schema_version": "c107-previous-review-findings-v1", "observed_at": now(), "findings": sorted(rows, key=lambda row: (str(row.get("name")), str(row.get("work_id"))))}


def board_checkpoint(row: dict[str, Any]) -> dict[str, str]:
    candidates = []
    tags = row.get("tags") if isinstance(row.get("tags"), dict) else {}
    if isinstance(tags.get("_last_output"), str):
        candidates.append(tags["_last_output"])
    for item in row.get("content", []):
        if isinstance(item, dict) and isinstance(item.get("text"), str):
            candidates.append(item["text"])
    for value in candidates:
        try:
            payload = json.loads(value)
        except json.JSONDecodeError:
            continue
        if isinstance(payload, dict) and payload.get("branch"):
            return {
                "worktree": str(payload.get("worktree", "NONE")),
                "branch": str(payload.get("branch")),
                "baseRevision": str(payload.get("baseRevision", "NONE")),
                "head": str(payload.get("head", payload.get("checkpoint_sha", "NONE"))),
                "pr_number": str(payload.get("pr_number", "NONE")),
            }
    return {"worktree": "NONE", "branch": "NONE", "baseRevision": "NONE", "head": "NONE", "pr_number": "NONE"}


def active_task_identity_records(
    board_results: list[dict[str, Any]], prs: dict[str, Any], source_paths: set[str]
) -> list[dict[str, Any]]:
    """Reconstruct every active task row from the board and observed PR pins."""
    tasks = active_task_rows(board_results)
    task_by_name = {row.get("name"): row for row in tasks}
    if len(task_by_name) != len(tasks):
        raise EvidenceFailure("canonical board has duplicate active task identities")
    pr_by_name: dict[str, dict[str, Any]] = {}
    for pr in prs.get("prs", []):
        name = pr.get("task_name")
        if name not in task_by_name:
            continue
        if name in pr_by_name:
            raise EvidenceFailure(f"active task has multiple open PR identities: {name}")
        if pr.get("task_work_id") != task_by_name[name].get("workId"):
            raise EvidenceFailure(f"active task PR work identity mismatch: {name}")
        pr_by_name[name] = pr

    records: list[dict[str, Any]] = []
    for row in tasks:
        name = row.get("name")
        checkpoint = board_checkpoint(row)
        pr = pr_by_name.get(name)
        if pr:
            # The board baseRevision is the task's isolation checkpoint. GitHub's
            # PR base may advance independently when main moves, so retain the
            # complete board checkpoint but do not require those two bases to be
            # byte-for-byte equal.
            for key, observed, expected in (
                ("branch", checkpoint.get("branch"), pr.get("head_branch")),
                ("head", checkpoint.get("head"), pr.get("head")),
                ("pr_number", checkpoint.get("pr_number"), str(pr.get("number"))),
            ):
                if observed not in {None, "", "NONE"} and observed != expected:
                    raise EvidenceFailure(f"active task board/PR {key} mismatch: {name}")
            branch = pr.get("head_branch")
            pr_number: int | str = pr.get("number")
            head = pr.get("head")
            base = pr.get("base")
            checkpoint_sha = head
            observation_command = "canonical board row + gh pr view --json files"
            changed = sorted(pr.get("accepted_main_changed_production_paths", []))
        else:
            branch = checkpoint.get("branch") if checkpoint.get("branch") not in {None, "", "NONE"} else f"codex/{name}"
            pr_number = "NONE"
            head = checkpoint.get("head", "NONE")
            base = checkpoint.get("baseRevision", "NONE")
            checkpoint_sha = head if head not in {None, "", "NONE"} else base
            observation_command = "canonical board row content/tags; no unmerged PR"
            changed = []
        leases = list(changed)
        if name == "audio-runtime-c79-retire-cli-response-lifecycle":
            leases.extend(sorted(SHARED_PATHS))
        records.append(
            {
                "name": name,
                "work_id": row.get("workId"),
                "state": row.get("state"),
                "branch": branch,
                "pr_number": pr_number,
                "head": head,
                "base": base,
                "checkpoint_sha": checkpoint_sha,
                "observation_command": observation_command,
                "worktree": checkpoint.get("worktree", "NONE"),
                "board_checkpoint": checkpoint,
                "accepted_main_changed_paths": changed,
                "lease_paths": sorted(set(leases)),
            }
        )
    return sorted(records, key=lambda row: str(row.get("name", "")))


def build_subtraction(board: dict[str, Any], prs: dict[str, Any], source_paths: set[str]) -> dict[str, Any]:
    board_results = board["results"]
    tasks = active_task_rows(board_results)
    retirement_prs = validate_pr_inventory(prs, source_paths)
    task_by_name = {row.get("name"): row for row in tasks}
    if len(task_by_name) != len(tasks):
        raise EvidenceFailure("canonical board has duplicate active task identities")
    all_task_rows = {
        row.get("name"): row
        for row in board_results
        if row.get("workTypeName") == "task" and row.get("name")
    }
    pr_by_name = {row.get("task_name"): row for row in prs["prs"] if row.get("task_name") != "NONE"}
    missing = sorted(REQUIRED_ACTIVE_NAMES - (set(all_task_rows) | set(pr_by_name)))
    if missing:
        raise EvidenceFailure(f"required active/preserved checkpoint identities missing: {missing}")
    path_owners: dict[str, list[dict[str, Any]]] = {path: [] for path in sorted(source_paths)}
    preserved: list[dict[str, Any]] = []
    for pr in prs["prs"]:
        paths = sorted(set(pr.get("accepted_main_changed_production_paths", [])) & source_paths)
        if not paths and pr["number"] not in retirement_prs:
            continue
        owner_name = pr.get("task_name") if pr.get("task_name") != "NONE" else f"PR#{pr['number']}"
        owner_work_id = pr.get("task_work_id", "NONE")
        owner_row = {
            "owner": owner_name,
            "work_id": owner_work_id,
            "state": "active-task" if owner_name in task_by_name else "preserved-unmerged-pr",
            "pr_number": pr["number"],
            "branch": pr["head_branch"],
            "head": pr["head"],
            "base": pr["base"],
            "source_revision": SOURCE_REVISION,
            "accepted_main_changed_paths": paths,
            "observation_commands": pr["observation_commands"],
        }
        preserved.append(owner_row)
        for path in paths:
            path_owners[path].append(owner_row)
    for path in path_owners:
        path_owners[path] = sorted(path_owners[path], key=lambda row: (row["owner"], row["pr_number"], row["head"]))
    shared_owners = []
    c79 = pr_by_name.get("audio-runtime-c79-retire-cli-response-lifecycle")
    c79_task = task_by_name.get("audio-runtime-c79-retire-cli-response-lifecycle")
    for path, reason in sorted(SHARED_PATHS.items()):
        shared_owners.append(
            {
                "path": path,
                "owner": "audio-runtime-c79-retire-cli-response-lifecycle",
                "work_id": c79_task.get("workId") if c79_task else "NONE",
                "pr_number": c79.get("number") if c79 else "NONE",
                "head": c79.get("head") if c79 else "NONE",
                "base": c79.get("base") if c79 else "NONE",
                "reason": reason,
            }
        )
    active = active_task_identity_records(board_results, prs, source_paths)
    return {
        "schema_version": "c107-subtraction-v1",
        "project": PROJECT,
        "contract_revision": CONTRACT,
        "source_revision": SOURCE_REVISION,
        "captured_at": now(),
        "canonical_board_sha256": sha256_file(HERE / "canonical-board.json"),
        "open_pr_inventory_sha256": sha256_file(HERE / "pr-inventory.json"),
        "required_active_task_names": sorted(REQUIRED_ACTIVE_NAMES),
        "required_checkpoint_names": sorted(REQUIRED_ACTIVE_NAMES),
        "active_tasks": active,
        "preserved_unmerged_checkpoints": preserved,
        "path_owners": path_owners,
        "shared_path_dependencies": shared_owners,
        "candidate_rule": "a candidate path must be an accepted-main production path with zero path_owners and must not be a shared path dependency",
        "all_open_pr_numbers": sorted(pr["number"] for pr in prs["prs"]),
        "all_open_retirement_pr_numbers": sorted(retirement_prs),
    }


def capture_state() -> dict[str, Any]:
    status = factory_status()
    if status.get("manifest", {}).get("project") != PROJECT or status.get("manifest", {}).get("contractRevision") != CONTRACT:
        raise EvidenceFailure("factory manifest is not the admitted audio-runtime contract")
    admission = verify_work()
    board, board_path = capture_board()
    source = set(source_production_paths())
    prs, pr_path = capture_pr_inventory(source, board["results"])
    candidate_pr = validate_current_pr_identity(prs)
    subtraction = build_subtraction(board, prs, source)
    write_json(HERE / "factory-status.json", {"schema_version": "c107-factory-status-v1", "observed_at": now(), "status": sanitize(status), "admission": admission})
    write_json(HERE / "previous-review-findings.json", review_findings(board["results"]))
    write_json(HERE / "subtraction.json", subtraction)
    capture = {
        "schema_version": "c107-state-capture-v1",
        "observed_at": now(),
        "project": PROJECT,
        "contract_revision": CONTRACT,
        "session": "~default",
        "server": FACTORY_SERVER,
        "board_path": str(board_path.relative_to(ROOT)),
        "pr_inventory_path": str(pr_path.relative_to(ROOT)),
        "subtraction_path": str((HERE / "subtraction.json").relative_to(ROOT)),
        "board_sha256": sha256_file(board_path),
        "pr_inventory_sha256": sha256_file(pr_path),
        "subtraction_sha256": sha256_file(HERE / "subtraction.json"),
        "active_task_count": len(active_task_rows(board["results"])),
        "open_pr_count": len(prs["prs"]),
        "open_retirement_pr_count": len([pr for pr in prs["prs"] if pr.get("is_retirement_checkpoint")]),
        "candidate_head": str(git("rev-parse", "HEAD")),
        "candidate_branch": str(git("branch", "--show-current")),
        "candidate_pr": candidate_pr,
        "prd_identity": admitted_prd_identity(),
    }
    write_json(HERE / "state-capture.json", capture)
    return capture


def ancestry(candidate: str, origin: str) -> dict[str, Any]:
    checks = {}
    for label, ancestor in (("baseline", BASELINE_REVISION), ("startup_integration", STARTUP_INTEGRATION), ("accepted_source", SOURCE_REVISION), ("current_origin_main", origin)):
        result = subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, candidate], cwd=ROOT, capture_output=True, timeout=60)
        checks[label] = {"ancestor": ancestor, "descendant": candidate, "exit_code": result.returncode, "passed": result.returncode == 0}
    return checks


def provenance() -> dict[str, Any]:
    require_time("provenance")
    fetch = subprocess.run(["git", "fetch", "origin", "main"], cwd=ROOT, env=os.environ.copy(), capture_output=True, text=True, timeout=min(120, max(1, int(max(1, remaining_time())))))
    if fetch.returncode != 0:
        raise EvidenceFailure(f"git fetch origin main failed: {redact_text(fetch.stderr).strip()}")
    candidate = str(git("rev-parse", "HEAD"))
    branch = str(git("branch", "--show-current"))
    origin = str(git("rev-parse", "origin/main"))
    if branch != BRANCH:
        raise EvidenceFailure(f"isolated branch mismatch: {branch!r} != {BRANCH!r}")
    source_check = subprocess.run(["git", "merge-base", "--is-ancestor", SOURCE_REVISION, candidate], cwd=ROOT, capture_output=True, timeout=60)
    if source_check.returncode != 0:
        raise EvidenceFailure("candidate no longer contains the immutable characterization source")
    checks = ancestry(candidate, origin)
    if not all(check["passed"] for check in checks.values()):
        raise EvidenceFailure("candidate is missing required startup/baseline/current-main ancestry; integrate current origin/main in this isolated worktree")
    source_archive = git("archive", "--format=tar", SOURCE_REVISION, text=False)
    pr_inventory = read_json(HERE / "pr-inventory.json")
    candidate_pr = validate_current_pr_identity(pr_inventory)
    source_plan = FACTORY_ROOT / "factory/projects/audio-runtime/source-plan.md"
    request = FACTORY_ROOT / "factory/projects/audio-runtime/request.md"
    acceptance = FACTORY_ROOT / "factory/projects/audio-runtime/acceptance.md"
    manifest = FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json"
    files = {evidence_path(path): sha256_file(path) for path in (PRD, source_plan, request, acceptance, manifest)}
    result = {
        "schema_version": "c107-provenance-v1",
        "observed_at": now(),
        "project": PROJECT,
        "contract_revision": CONTRACT,
        "factory_session": "~default",
        "factory_server": FACTORY_SERVER,
        "admission": verify_work(),
        "prd_identity": admitted_prd_identity(),
        "branch": branch,
        "candidate_revision": candidate,
        "candidate_pr": candidate_pr,
        "accepted_source_revision": SOURCE_REVISION,
        "current_origin_main": origin,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "baseline_revision": BASELINE_REVISION,
        "ancestry": checks,
        "source_archive_sha256": sha256_bytes(source_archive),
        "authority_file_sha256": files,
        "fetch": {"command": ["git", "fetch", "origin", "main"], "exit_code": fetch.returncode, "stdout_sha256": sha256_bytes(fetch.stdout.encode()), "stderr_sha256": sha256_bytes(fetch.stderr.encode())},
        "credentials": "not used; command environment for public replay removes credential variables",
        "realtime": "not used",
        "acceptance_waiver": "none",
        "project_completion": "not claimed; C107 is scoped characterization only",
    }
    write_json(HERE / "provenance.json", result)
    return result


def normalize_timestamps(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: normalize_timestamps(item) for key, item in value.items() if key not in {"run_timestamp"}}
    if isinstance(value, list):
        return [normalize_timestamps(item) for item in value]
    return value


def run_analyzer_once(output: pathlib.Path) -> dict[str, Any]:
    command = [sys.executable, str(ANALYZER), "--root", str(ROOT), "--source", SOURCE_REVISION, "--output-dir", str(output), "--subtraction", str(HERE / "subtraction.json")]
    return run_bounded("analyzer", command, ROOT, HERE / "runs/inventory", timeout=240)


def canonical_inventory_shape() -> dict[str, Any]:
    """Derive the accepted-main inventory independently of checked-in evidence.

    Inventory validation must not trust the submitted rows or their self-reported
    totals to establish completeness.  Re-run the pinned source census in a
    disposable directory and retain only the stable file/symbol/edge shape used
    for comparisons.  The cache is process-local and never becomes evidence.
    """
    global CANONICAL_INVENTORY_SHAPE
    if CANONICAL_INVENTORY_SHAPE is not None:
        return CANONICAL_INVENTORY_SHAPE
    require_time("canonical inventory completeness")
    with tempfile.TemporaryDirectory(prefix="c107-canonical-inventory-") as temp:
        output = pathlib.Path(temp) / "inventory"
        command = [sys.executable, str(ANALYZER), "--root", str(ROOT), "--source", SOURCE_REVISION, "--output-dir", str(output)]
        result = subprocess.run(
            command,
            cwd=ROOT,
            env=safe_environment(),
            capture_output=True,
            text=True,
            timeout=min(240, max(1, int(max(1, remaining_time())))),
        )
        if result.returncode != 0:
            raise EvidenceFailure(f"canonical source census failed: {redact_text(result.stderr or result.stdout).strip()}")
        canonical = read_json(output / "inventory.json")
    if canonical.get("source_revision") != SOURCE_REVISION or canonical.get("target_root") != TARGET_ROOT:
        raise EvidenceFailure("canonical source census is not pinned")
    files = canonical.get("files")
    symbols = canonical.get("symbols")
    call_edges = canonical.get("call_edges")
    if not isinstance(files, list) or not isinstance(symbols, list) or not isinstance(call_edges, list):
        raise EvidenceFailure("canonical source census omitted completeness rows")
    canonical_files = {row.get("path"): row for row in files if isinstance(row, dict) and row.get("path")}
    canonical_symbols = {row.get("id"): row for row in symbols if isinstance(row, dict) and row.get("id")}
    if len(canonical_files) != len(files) or len(canonical_symbols) != len(symbols):
        raise EvidenceFailure("canonical source census contains duplicate file or symbol identities")
    expected_paths = set(source_production_paths())
    if set(canonical_files) != expected_paths:
        missing = sorted(expected_paths - set(canonical_files))
        extra = sorted(set(canonical_files) - expected_paths)
        raise EvidenceFailure(f"canonical source census file coverage mismatch; missing={missing[:4]} extra={extra[:4]}")
    for path, row in canonical_files.items():
        if row.get("kind") != "production" or not isinstance(row.get("top_level_symbol_ids"), list):
            raise EvidenceFailure(f"canonical source census file row is incomplete: {path}")
        source = git_show(SOURCE_REVISION, path)
        if row.get("bytes") != len(source) or row.get("physical_lines") != len(source.splitlines()):
            raise EvidenceFailure(f"canonical source census file dimensions are not source-derived: {path}")
        if len(row["top_level_symbol_ids"]) != len(set(row["top_level_symbol_ids"])):
            raise EvidenceFailure(f"canonical source census contains duplicate file symbol IDs: {path}")
    CANONICAL_INVENTORY_SHAPE = {
        "files": canonical_files,
        "file_symbol_ids": {path: list(row.get("top_level_symbol_ids") or []) for path, row in canonical_files.items()},
        "symbols": canonical_symbols,
        "call_edges": call_edges,
        "totals": canonical.get("totals") or {},
    }
    return CANONICAL_INVENTORY_SHAPE


def symbol_shape(symbol: dict[str, Any]) -> dict[str, Any]:
    return {
        key: symbol.get(key)
        for key in (
            "id",
            "name",
            "qualified_name",
            "kind",
            "receiver",
            "exported",
            "package",
            "import_path",
            "file",
            "line",
            "end_line",
            "signature",
        )
    }


def validate_inventory(path: pathlib.Path, *, require_subtraction: bool = True) -> dict[str, Any]:
    inventory = read_json(path)
    if inventory.get("source_revision") != SOURCE_REVISION or inventory.get("target_root") != TARGET_ROOT:
        raise EvidenceFailure("inventory source or target root is not pinned")
    expected = source_counts()
    canonical = canonical_inventory_shape()
    files = inventory.get("files")
    symbols = inventory.get("symbols")
    if not isinstance(files, list) or not isinstance(symbols, list):
        raise EvidenceFailure("inventory omitted files or symbols")
    paths = [row.get("path") for row in files]
    if len(paths) != len(set(paths)) or set(paths) != set(expected["paths"]):
        missing = sorted(set(expected["paths"]) - set(paths))
        extra = sorted(set(paths) - set(expected["paths"]))
        raise EvidenceFailure(f"inventory file coverage mismatch; missing={missing[:4]} extra={extra[:4]}")
    symbol_ids = [row.get("id") for row in symbols]
    if len(symbol_ids) != len(set(symbol_ids)):
        raise EvidenceFailure("inventory has duplicate stable symbol IDs")
    expected_symbol_ids = set(canonical["symbols"])
    actual_symbol_ids = set(symbol_ids)
    if actual_symbol_ids != expected_symbol_ids:
        missing = sorted(expected_symbol_ids - actual_symbol_ids)
        extra = sorted(actual_symbol_ids - expected_symbol_ids)
        raise EvidenceFailure(f"inventory symbol coverage mismatch; missing={missing[:4]} extra={extra[:4]}")
    file_map = {row["path"]: row for row in files}
    class_counts: Counter[str] = Counter()
    for file in files:
        if file.get("kind") != "production" or not isinstance(file.get("subtraction"), dict):
            raise EvidenceFailure(f"file row is incomplete: {file.get('path')}")
        source = git_show(SOURCE_REVISION, file["path"])
        line_count = len(source.splitlines())
        if file.get("physical_lines") != line_count or file.get("bytes") != len(source):
            raise EvidenceFailure(f"source-derived file dimensions mismatch: {file['path']}")
        ids = file.get("top_level_symbol_ids")
        if not isinstance(ids, list) or len(ids) != len(set(ids)):
            raise EvidenceFailure(f"file symbol index is incomplete: {file['path']}")
        expected_ids = canonical["file_symbol_ids"].get(file["path"])
        if ids != expected_ids:
            raise EvidenceFailure(f"file symbol coverage mismatch: {file['path']}")
    for symbol in symbols:
        if symbol.get("file") not in file_map or not symbol.get("id") or not symbol.get("name"):
            raise EvidenceFailure(f"symbol has no accepted-main file or stable identity: {symbol!r}")
        expected_symbol = canonical["symbols"].get(symbol["id"])
        if expected_symbol is None or symbol_shape(symbol) != symbol_shape(expected_symbol):
            raise EvidenceFailure(f"inventory symbol identity/span mismatch: {symbol.get('id')}")
        for key in ("caller_edges", "caller_edge_status", "dynamic_interface_reflection_generated_uncertainty"):
            if symbol.get(key) != expected_symbol.get(key):
                raise EvidenceFailure(f"inventory symbol evidence mismatch: {symbol.get('id')}")
        derived_dependency = source_service_dependencies(symbol["file"])
        if expected_symbol.get("existing_destination_service_dependency") != derived_dependency:
            raise EvidenceFailure(f"canonical source census service dependency mismatch: {symbol.get('id')}")
        if symbol.get("existing_destination_service_dependency") != derived_dependency:
            raise EvidenceFailure(f"inventory service dependency is not source-derived: {symbol.get('id')}")
        if symbol.get("class") not in ALLOWED_CLASSES:
            raise EvidenceFailure(f"symbol classification missing or invalid: {symbol.get('id')}")
        if symbol.get("class") == "deprecated_adapter":
            lines = git_show(SOURCE_REVISION, symbol["file"]).decode("utf-8", errors="replace").splitlines()
            segment = "\n".join(lines[max(0, int(symbol.get("line", 1)) - 8) : int(symbol.get("end_line", symbol.get("line", 1)))])
            if "Deprecated:" not in segment:
                raise EvidenceFailure(f"deprecated_adapter lacks explicit Deprecated: marker: {symbol['id']}")
        callers = symbol.get("caller_edges")
        if not isinstance(callers, list) or symbol.get("caller_edge_status") not in {"exact-static", "no-static-caller"}:
            raise EvidenceFailure(f"caller evidence missing for {symbol.get('id')}")
        if symbol["caller_edge_status"] == "exact-static":
            for caller in callers:
                if not caller.get("file") or not caller.get("line") or not caller.get("expression") or not caller.get("resolution"):
                    raise EvidenceFailure(f"caller row is not exact: {symbol['id']}")
        uncertainty = symbol.get("dynamic_interface_reflection_generated_uncertainty")
        if not isinstance(uncertainty, dict) or any(key not in uncertainty for key in ("dynamic_reachability", "interface_dispatch", "function_value", "reflection", "generated", "notes")):
            raise EvidenceFailure(f"uncertainty fields missing: {symbol.get('id')}")
        if "existing_destination_service_dependency" not in symbol or "subtraction" not in symbol:
            raise EvidenceFailure(f"dependency/subtraction evidence missing: {symbol.get('id')}")
        class_counts[symbol["class"]] += 1
    totals = inventory.get("totals") or {}
    canonical_totals = canonical["totals"]
    expected_totals = {
        "production_files": expected["production_files"],
        "physical_lines": expected["physical_lines"],
        "bytes": expected["bytes"],
        "top_level_symbols": len(canonical["symbols"]),
        "exact_static_call_edges": len(canonical["call_edges"]),
        "no_static_caller_symbols": canonical_totals.get("no_static_caller_symbols"),
    }
    actual_totals = {key: totals.get(key) for key in expected_totals}
    if actual_totals != expected_totals:
        raise EvidenceFailure(f"inventory totals mismatch: {actual_totals!r} expected={expected_totals!r}")
    if totals.get("class_counts") != dict(sorted(class_counts.items())):
        raise EvidenceFailure("inventory aggregate call/class totals are inconsistent")
    valid_ids = set(symbol_ids)
    for edge in inventory.get("call_edges", []):
        if edge.get("callee") not in valid_ids or not edge.get("caller") or not isinstance(edge.get("call"), dict):
            raise EvidenceFailure(f"call edge is not tied to an emitted symbol: {edge!r}")
    if inventory.get("call_edges") != canonical["call_edges"]:
        raise EvidenceFailure("inventory call-edge coverage mismatch")
    if require_subtraction:
        subtraction = read_json(HERE / "subtraction.json")
        owner_map = subtraction.get("path_owners") or {}
        for file in files:
            expected_state = "subtracted" if owner_map.get(file["path"]) else "unsubtracted"
            if file["subtraction"].get("state") != expected_state:
                raise EvidenceFailure(f"inventory subtraction state disagrees with ledger: {file['path']}")
    return {"files": len(files), "symbols": len(symbols), "physical_lines": expected["physical_lines"], "class_counts": dict(sorted(class_counts.items())), "call_edges": len(inventory.get("call_edges", []))}


def compare_inventory_outputs(first_dir: pathlib.Path, second_dir: pathlib.Path, *, require_subtraction: bool) -> dict[str, Any]:
    for directory in (first_dir, second_dir):
        validate_inventory(directory / "inventory.json", require_subtraction=require_subtraction)
    comparisons = {}
    for name in ("inventory.json", "inventory.md", "classifications.json", "call-paths.json"):
        first_value = read_json(first_dir / name) if name.endswith(".json") else (first_dir / name).read_text(encoding="utf-8")
        second_value = read_json(second_dir / name) if name.endswith(".json") else (second_dir / name).read_text(encoding="utf-8")
        first_normal = normalize_timestamps(first_value)
        second_normal = normalize_timestamps(second_value)
        first_bytes = stable_json(first_normal).encode() if isinstance(first_normal, (dict, list)) else str(first_normal).encode()
        second_bytes = stable_json(second_normal).encode() if isinstance(second_normal, (dict, list)) else str(second_normal).encode()
        comparisons[name] = {
            "first_sha256": sha256_bytes(first_bytes),
            "second_sha256": sha256_bytes(second_bytes),
            "byte_identical_after_run_timestamp_normalization": first_normal == second_normal,
        }
        if not comparisons[name]["byte_identical_after_run_timestamp_normalization"]:
            raise EvidenceFailure(f"analyzer output is not deterministic: {name}")
    return {
        "schema_version": "c107-determinism-v1",
        "source_revision": SOURCE_REVISION,
        "first_directory": str(first_dir),
        "second_directory": str(second_dir),
        "comparisons": comparisons,
        "normalization": "remove only run_timestamp keys; wall durations remain in analysis-run.json",
    }


def run_inventory() -> dict[str, Any]:
    first_dir = pathlib.Path(tempfile.mkdtemp(prefix="c107-run-1-"))
    second_dir = pathlib.Path(tempfile.mkdtemp(prefix="c107-run-2-"))
    try:
        first_result = run_analyzer_once(first_dir)
        second_result = run_analyzer_once(second_dir)
        for result, directory in ((first_result, first_dir), (second_result, second_dir)):
            require_success(result, "analyzer")
            validate_inventory(directory / "inventory.json")
        comparison_report = compare_inventory_outputs(first_dir, second_dir, require_subtraction=True)
        comparisons = comparison_report["comparisons"]
        analysis_dir = HERE / "analysis"
        if analysis_dir.exists():
            shutil.rmtree(analysis_dir)
        shutil.copytree(first_dir, analysis_dir)
        # The command-run files belong under runs; retain only the analyzer's
        # stable reports in analysis and keep its actual wall timing in the
        # inventory run records.
        for child in list(analysis_dir.iterdir()):
            if child.name not in {"inventory.json", "inventory.md", "classifications.json", "call-paths.json", "analysis-run.json"}:
                if child.is_dir():
                    shutil.rmtree(child)
                else:
                    child.unlink()
        report = {"schema_version": "c107-determinism-v1", "source_revision": SOURCE_REVISION, "runs": [first_result, second_result], "comparisons": comparisons, "normalization": "remove only run_timestamp keys; wall durations remain in analysis-run.json"}
        write_json(HERE / "runs/inventory/determinism.json", report)
        return report
    finally:
        shutil.rmtree(first_dir, ignore_errors=True)
        shutil.rmtree(second_dir, ignore_errors=True)


def external_determinism(first_dir: pathlib.Path, second_dir: pathlib.Path) -> dict[str, Any]:
    report = compare_inventory_outputs(first_dir, second_dir, require_subtraction=False)
    write_json(HERE / "runs/inventory/external-determinism.json", report)
    return report


CANDIDATE_CONFIG = [
    {
        "id": "session-instruction-resolution",
        "rank": 1,
        "source_files": [f"{TARGET_ROOT}/session_instructions.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessioninstructions/contract.go: normalized instruction request, loader capability and composition result",
            "private_internal": "go-agent-runtime/services/sessioninstructions/internal/service/: filesystem/config resolution, bounded composition and error attribution",
            "wire": "go-agent-runtime/services/sessioninstructions/wire/: dedicated constructor for the private service and existing injected loader/config dependencies",
        },
        "physical_line_floor": 190,
        "public_workflow": "yui session instruction/text-seed admission and the existing RunSessionWithInstructions* path",
        "effects": "preserve resolved prompt text, tool grounding, loader errors, provider selection and the existing no-credential CLI output",
        "focused_checks": ["TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration", "TestSessionDirectoryRecordingWritesConversationSessionLog"],
        "race_checks": ["same instruction admission and composition tests under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessioninstructions and verifies normalized request plus invalid loader rejection without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all (credential-free replay/tool and instruction-adjacent paths)",
        "negative_control": "malformed/oversized instruction document and missing loader capability must fail before provider construction; no fallback to an unvalidated prompt",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "QUALITY", "EMBED", "PARITY"],
    },
    {
        "id": "session-terminal-diagnostics",
        "rank": 2,
        "source_files": [f"{TARGET_ROOT}/session_diagnostics_terminal.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessionterminal/contract.go: terminal classification, output state, cancellation and metrics projections",
            "private_internal": "go-agent-runtime/services/sessionterminal/internal/service/: terminal precedence, failure facts, continuation metadata and final accounting",
            "wire": "go-agent-runtime/services/sessionterminal/wire/: dedicated terminal-observation construction with typed diagnostics and clock-independent counters",
        },
        "physical_line_floor": 180,
        "public_workflow": "credential-free yui session replay and cancellation/error workflows that publish one terminal diagnostic and metrics record",
        "effects": "retain terminal precedence, provider/error classification, output-state semantics, unresolved continuation metadata, cancellation identity and final token/audio accounting",
        "focused_checks": ["TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic", "TestSessionProgressObserver_IgnoresNonTerminalProviderDiagnostic", "TestSessionCancellation_RecordsTerminalDiagnosticOnce"],
        "race_checks": ["terminal failure, cancellation and metrics publication controls under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessionterminal and checks typed terminal classifications, output states and error identity without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including credential-free replay, interruption, tool-continuation and terminal-error controls",
        "negative_control": "drop the final failure or mutate cancellation/output-state precedence; the bounded workflow must reject missing terminal evidence and never silently publish success",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "TRACE", "REPLAY", "FAILURES", "QUALITY", "PARITY"],
    },
    {
        "id": "session-trace-evidence",
        "rank": 3,
        "source_files": [f"{TARGET_ROOT}/trace.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessiontrace/contract.go: bounded trace request, clock source and publication result",
            "private_internal": "go-agent-runtime/services/sessiontrace/internal/service/: capture taps, credential redaction and atomic trace attachment",
            "wire": "go-agent-runtime/services/sessiontrace/wire/: dedicated constructor for the private trace service and clock/device observers",
        },
        "physical_line_floor": 70,
        "public_workflow": "yui session --record-dir/--trace-audio and the existing credential-free audio/tool replay record",
        "effects": "preserve microphone/provider/rendered trace events, credential redaction, atomic destination attachment and retained failure evidence",
        "focused_checks": ["TestSessionRecordedPCMIntegrity", "TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact"],
        "race_checks": ["recording/trace package and the same replay-integrity controls under -race"],
        "external_consumer": "GOWORK=off Go consumer constructs services/sessiontrace with a deterministic clock and verifies close/attach behavior",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including PCM integrity and audio interrupt ordering controls",
        "negative_control": "nil clock, duplicate trace destination or same-length trace mutation must fail closed before publication and retain the original error",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["TRACE", "REPLAY", "SERVICE", "QUALITY", "PARITY"],
    },
    {
        "id": "session-failure-projection",
        "rank": 4,
        "source_files": [f"{TARGET_ROOT}/session_diagnostics_failure.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessionfailure/contract.go: typed failure facts, terminal reason and output-state projection",
            "private_internal": "go-agent-runtime/services/sessionfailure/internal/service/: provider/session failure normalization and cancellation-safe publication",
            "wire": "go-agent-runtime/services/sessionfailure/wire/: dedicated constructor for failure projection and diagnostic sinks",
        },
        "physical_line_floor": 160,
        "public_workflow": "credential-free yui session replay and provider/session failure workflows that publish typed terminal evidence",
        "effects": "retain failure classification, terminal reason/provenance, output state, cancellation boundaries and the original provider error",
        "focused_checks": ["TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic", "TestSessionCancellation_RecordsTerminalDiagnosticOnce"],
        "race_checks": ["failure observation, cancellation and terminal publication controls under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessionfailure and verifies typed failure projection without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including credential-free replay, interruption and terminal-error controls",
        "negative_control": "drop terminal failure facts or convert cancellation into failure; the bounded workflow must reject missing/error-classified terminal evidence",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "TRACE", "REPLAY", "FAILURES", "QUALITY", "PARITY"],
    },
    {
        "id": "session-duration-terminal-boundary",
        "rank": 5,
        "source_files": [f"{TARGET_ROOT}/session_duration_terminal.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessionduration/contract.go: bounded terminal admission, output state and lifecycle result",
            "private_internal": "go-agent-runtime/services/sessionduration/internal/service/: provider-terminal observation, max-duration close and transport errors",
            "wire": "go-agent-runtime/services/sessionduration/wire/: duration terminal coordinator and replay artifact writer",
        },
        "physical_line_floor": 90,
        "public_workflow": "credential-free yui session --max-duration replay and bounded provider-close workflows",
        "effects": "preserve provider-terminal versus loop-close precedence, output-state projection, terminal replay artifacts and joined lifecycle errors",
        "focused_checks": ["TestRunSessionWithMaxDuration_PreservesProviderTerminalDuringShutdown", "TestRunSessionWithMaxDuration_FinalizesRealArtifactsAndRejectsLateFrame"],
        "race_checks": ["bounded duration terminal admission and artifact publication controls under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessionduration and verifies terminal admission/output state without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including credential-free replay, max-duration and interruption controls",
        "negative_control": "admit a loop shutdown close as provider evidence or drop the bounded terminal artifact; the replay verifier must fail closed",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "REPLAY", "FAILURES", "QUALITY", "PARITY"],
    },
    {
        "id": "session-interactive-tool-policy",
        "rank": 6,
        "source_files": [f"{TARGET_ROOT}/session_interactive_policy.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessioninteractive/contract.go: per-session tool class, timeout and acknowledgement policy",
            "private_internal": "go-agent-runtime/services/sessioninteractive/internal/service/: policy validation, cloning and runtime compatibility conversion",
            "wire": "go-agent-runtime/services/sessioninteractive/wire/: injected interactive policy construction and browser tool-name mapping",
        },
        "physical_line_floor": 100,
        "public_workflow": "credential-free yui tool replay with fast-read and bounded-long-running tool calls",
        "effects": "preserve request-scoped tool classes, timeout bounds, acknowledgement thresholds, validation and browser-tool mapping",
        "focused_checks": ["TestInteractiveToolPolicyDefaultsAndClassSelection", "TestInteractiveToolPolicyHonorsOverrides", "TestInteractiveToolExecutorUsesIndependentSessionBudgets"],
        "race_checks": ["per-session policy cloning and tool timeout decisions under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessioninteractive and validates policy settings without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including credential-free tool continuation and cancellation controls",
        "negative_control": "accept an unbounded or invalid tool policy; the consumer and replay controls must reject it before tool execution",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "REPLAY", "QUALITY", "PARITY"],
    },
    {
        "id": "session-room-error-sanitization",
        "rank": 7,
        "source_files": [f"{TARGET_ROOT}/session_room_errors.go"],
        "destination": {
            "public_contract": "go-agent-runtime/services/sessionroomerrors/contract.go: participant failure identity, sanitized cause and room result",
            "private_internal": "go-agent-runtime/services/sessionroomerrors/internal/service/: secret-aware error unwrapping and termination mapping",
            "wire": "go-agent-runtime/services/sessionroomerrors/wire/: room failure projection and credential-free diagnostic construction",
        },
        "physical_line_floor": 70,
        "public_workflow": "credential-free multi-participant yui session failure and disconnect replay",
        "effects": "retain participant identity and termination reason while sanitizing secrets, preserving error identity and projecting a stable room result",
        "focused_checks": ["TestRunRoom_PreservesFailedEvidenceAndRedactsSecrets", "TestRoomEvidence_RecordingHealthRetainsFirstSanitizedFailure"],
        "race_checks": ["participant failure projection and room result publication under -race"],
        "external_consumer": "GOWORK=off Go consumer imports services/sessionroomerrors and verifies sanitized failure identity without agent-cli",
        "accumulated_regression": "scripts/test-session-ci-regressions.sh all, including credential-free room failure and interruption controls",
        "negative_control": "publish a raw secret or erase participant failure identity; the room replay verifier must reject the result",
        "shared_dependencies": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
        "remaining_criteria": ["SERVICE", "FAILURES", "QUALITY", "PARITY"],
    },
]


def materialize_candidates() -> dict[str, Any]:
    inventory = read_json(HERE / "analysis/inventory.json")
    subtraction = read_json(HERE / "subtraction.json")
    owned_source_paths = {
        path for path, owners in subtraction.get("path_owners", {}).items() if isinstance(owners, list) and owners
    }
    excluded_configs = [
        config["id"]
        for config in CANDIDATE_CONFIG
        if owned_source_paths.intersection(config["source_files"])
    ]
    selected_configs = [
        config
        for config in CANDIDATE_CONFIG
        if not owned_source_paths.intersection(config["source_files"])
    ][:3]
    if len(selected_configs) < 3:
        raise EvidenceFailure(
            f"fewer than three unowned retirement candidates remain after subtraction: {len(selected_configs)}"
        )
    symbols_by_file: dict[str, list[dict[str, Any]]] = {}
    for symbol in inventory.get("symbols", []):
        symbols_by_file.setdefault(symbol["file"], []).append(symbol)
    candidates = []
    for rank, config in enumerate(selected_configs, 1):
        file_rows = [next((row for row in inventory["files"] if row["path"] == path), None) for path in config["source_files"]]
        if any(row is None for row in file_rows):
            raise EvidenceFailure(f"candidate source file is absent from inventory: {config['id']}")
        symbols = []
        for path in config["source_files"]:
            for symbol in symbols_by_file.get(path, []):
                symbols.append(
                    {
                        "id": symbol["id"],
                        "name": symbol["name"],
                        "kind": symbol["kind"],
                        "span": {"file": symbol["file"], "line": symbol["line"], "end_line": symbol["end_line"]},
                        "class": symbol["class"],
                        "caller_edge_status": symbol["caller_edge_status"],
                        "production_callers": symbol["caller_edges"],
                        "uncertainty": symbol["dynamic_interface_reflection_generated_uncertainty"],
                    }
                )
        candidates.append(
            {
                "id": config["id"],
                "rank": rank,
                "decision": "independently executable residual boundary; characterization grants no mutation lease",
                "source_revision": SOURCE_REVISION,
                "current_writer_paths": config["source_files"],
                "current_file_baseline": [{"path": row["path"], "physical_lines": row["physical_lines"], "bytes": row["bytes"]} for row in file_rows],
                "current_symbols": symbols,
                "destination": config["destination"],
                "retirement_accounting": {
                    "legacy_production_files_removed_floor": len(config["source_files"]),
                    "legacy_physical_lines_removed_floor": config["physical_line_floor"],
                    "counted_scope": "only removed accepted-main agentruntime production files/physical lines",
                    "excluded_from_credit": ["new runtime lines", "generated Wire", "tests", "fixtures", "evidence", "wrapper-only movement"],
                },
                "public_workflow": config["public_workflow"],
                "expected_observable_effects": config["effects"],
                "focused_normal_checks": config["focused_checks"],
                "focused_race_checks": config["race_checks"],
                "external_consumer_check": config["external_consumer"],
                "accumulated_credential_free_regression": config["accumulated_regression"],
                "causal_negative_control": config["negative_control"],
                "shared_file_dependencies": config["shared_dependencies"],
                "remaining_immutable_criteria": config["remaining_criteria"],
                "reliance_on_future_peer_deletion": False,
                "c107_mutation_lease": False,
            }
        )
    document = {
        "schema_version": "c107-candidates-v1",
        "project": PROJECT,
        "source_revision": SOURCE_REVISION,
        "selection_rule": "ranked by independent value; preferred configurations whose source paths are subtraction-owned are excluded, then the first three eligible configurations must have pairwise-disjoint writer and destination paths",
        "candidate_selection": {
            "subtraction_owned_source_paths_excluded": sorted(owned_source_paths),
            "excluded_configurations": excluded_configs,
            "selected_configurations": [config["id"] for config in selected_configs],
        },
        "candidates": candidates,
        "all_project_criteria": "OPEN; C107 characterizes only",
    }
    write_json(HERE / "candidates.json", document)
    report_lines = [
        "# C107 candidate report",
        "",
        f"Accepted-main source: `{SOURCE_REVISION}`",
        "",
        "These are ordered, pairwise-disjoint future retirement candidates. C107 has no implementation lease and does not edit their source, destination, Wire registry, or architecture baseline.",
        "",
    ]
    for candidate in candidates:
        report_lines.extend(
            [
                f"## {candidate['rank']}. {candidate['id']}",
                "",
                f"Writer paths: {', '.join(f'`{path}`' for path in candidate['current_writer_paths'])}",
                f"Baseline: {candidate['current_file_baseline']}",
                f"Retirement floor: {candidate['retirement_accounting']['legacy_production_files_removed_floor']} file(s), {candidate['retirement_accounting']['legacy_physical_lines_removed_floor']} physical line(s). New runtime/tests/evidence and wrappers receive no credit.",
                f"Public workflow/effects: {candidate['public_workflow']}; {candidate['expected_observable_effects']}",
                f"Destination: public `{candidate['destination']['public_contract']}`; private `{candidate['destination']['private_internal']}`; Wire `{candidate['destination']['wire']}`.",
                f"Negative control: {candidate['causal_negative_control']}",
                f"Open criteria: {', '.join(candidate['remaining_immutable_criteria'])}",
                "",
                "Exact current symbols and callers are in `candidates.json` and the pinned inventory.",
                "",
            ]
        )
    (HERE / "report.md").write_text("\n".join(report_lines), encoding="utf-8")
    return document


def validate_subtraction_document(
    subtraction: dict[str, Any], prs: dict[str, Any], board: dict[str, Any], source_paths: set[str] | None = None
) -> dict[str, Any]:
    source_paths = source_paths or set(source_production_paths())
    expected_by_number = validate_pr_inventory(prs, source_paths)
    path_owners = subtraction.get("path_owners")
    if subtraction.get("source_revision") != SOURCE_REVISION or not isinstance(path_owners, dict):
        raise EvidenceFailure("subtraction ledger is not pinned or has no path_owners")
    if set(path_owners) != source_paths:
        missing = sorted(source_paths - set(path_owners))
        extra = sorted(set(path_owners) - source_paths)
        raise EvidenceFailure(f"subtraction path coverage mismatch; missing={missing[:4]} extra={extra[:4]}")
    for path, owners in path_owners.items():
        if not isinstance(owners, list):
            raise EvidenceFailure(f"subtraction path owner list is invalid: {path}")
        seen_path_owners: set[int] = set()
        for owner in owners:
            try:
                number = int(owner.get("pr_number"))
            except (TypeError, ValueError) as exc:
                raise EvidenceFailure(f"subtraction path owner has invalid PR identity: {owner!r}") from exc
            if number in seen_path_owners:
                raise EvidenceFailure(f"subtraction path has duplicate PR owner: {path} PR {number}")
            seen_path_owners.add(number)
            expected = expected_by_number.get(number)
            expected_owner = expected.get("task_name") if expected and expected.get("task_name") != "NONE" else f"PR#{number}"
            expected_paths = sorted(expected.get("accepted_main_changed_production_paths", [])) if expected else []
            if (
                expected is None
                or owner.get("owner") != expected_owner
                or owner.get("work_id") != expected.get("task_work_id")
                or owner.get("branch") != expected.get("head_branch")
                or owner.get("head") != expected.get("head")
                or owner.get("base") != expected.get("base")
                or sorted(owner.get("accepted_main_changed_paths", [])) != expected_paths
                or path not in expected_paths
            ):
                raise EvidenceFailure(f"subtraction PR head/path mismatch for {path}: PR {owner.get('pr_number')}")
    expected_retirement_prs = {number: row for number, row in expected_by_number.items() if row["is_retirement_checkpoint"]}
    expected_all_numbers = sorted(expected_by_number)
    recorded_all_numbers = subtraction.get("all_open_pr_numbers")
    if recorded_all_numbers != expected_all_numbers:
        raise EvidenceFailure("subtraction all_open_pr_numbers is incomplete")
    preserved = subtraction.get("preserved_unmerged_checkpoints")
    if not isinstance(preserved, list):
        raise EvidenceFailure("subtraction omitted preserved checkpoint rows")
    preserved_by_number: dict[int, dict[str, Any]] = {}
    for owner in preserved:
        try:
            number = int(owner.get("pr_number"))
        except (TypeError, ValueError) as exc:
            raise EvidenceFailure(f"subtraction preserved checkpoint has invalid PR identity: {owner!r}") from exc
        if number in preserved_by_number:
            raise EvidenceFailure(f"subtraction duplicated preserved PR identity: {number}")
        expected = expected_retirement_prs.get(number)
        if expected is None:
            raise EvidenceFailure(f"subtraction contains an unobserved retirement PR identity: {number}")
        expected_paths = sorted(expected.get("accepted_main_changed_production_paths", []))
        expected_owner = expected.get("task_name") if expected.get("task_name") != "NONE" else f"PR#{number}"
        if owner.get("owner") != expected_owner or owner.get("work_id") != expected.get("task_work_id") or owner.get("head") != expected.get("head") or owner.get("base") != expected.get("base") or owner.get("branch") != expected.get("head_branch") or sorted(owner.get("accepted_main_changed_paths", [])) != expected_paths:
            raise EvidenceFailure(f"subtraction preserved checkpoint identity mismatch: PR {number}")
        preserved_by_number[number] = owner
    expected_numbers = sorted(expected_retirement_prs)
    recorded_numbers = sorted(preserved_by_number)
    if recorded_numbers != expected_numbers:
        missing = sorted(set(expected_numbers) - set(recorded_numbers))
        extra = sorted(set(recorded_numbers) - set(expected_numbers))
        raise EvidenceFailure(f"subtraction omitted open retirement PR identities: missing={missing} extra={extra}")

    expected_active_records = active_task_identity_records(board.get("results", []), prs, source_paths)
    actual_active_records = subtraction.get("active_tasks")
    if not isinstance(actual_active_records, list):
        raise EvidenceFailure("subtraction omitted active-task identity records")
    expected_active_by_name = {row["name"]: row for row in expected_active_records}
    actual_active_by_name: dict[str, dict[str, Any]] = {}
    for row in actual_active_records:
        if not isinstance(row, dict) or not isinstance(row.get("name"), str):
            raise EvidenceFailure("subtraction active-task identity row is invalid")
        if row["name"] in actual_active_by_name:
            raise EvidenceFailure(f"subtraction duplicated active-task identity: {row['name']}")
        actual_active_by_name[row["name"]] = row
    if set(actual_active_by_name) != set(expected_active_by_name):
        missing = sorted(set(expected_active_by_name) - set(actual_active_by_name))
        extra = sorted(set(actual_active_by_name) - set(expected_active_by_name))
        raise EvidenceFailure(f"subtraction active-task identity set mismatch: missing={missing} extra={extra}")
    identity_keys = {
        "name",
        "work_id",
        "state",
        "branch",
        "pr_number",
        "head",
        "base",
        "checkpoint_sha",
        "observation_command",
        "worktree",
        "board_checkpoint",
        "accepted_main_changed_paths",
        "lease_paths",
    }
    for name, expected in expected_active_by_name.items():
        actual = actual_active_by_name[name]
        mismatches = sorted(key for key in identity_keys if actual.get(key) != expected.get(key))
        extra = sorted(set(actual) - identity_keys)
        if mismatches or extra:
            fields = mismatches + [f"extra:{key}" for key in extra]
            raise EvidenceFailure(f"subtraction active-task identity mismatch: {name} fields={fields}")
    expected_active_names = set(expected_active_by_name)
    active_names = set(actual_active_by_name)
    preserved_names = {row.get("owner") for row in subtraction.get("preserved_unmerged_checkpoints", [])}
    represented_names = active_names | preserved_names
    if active_names != expected_active_names:
        missing = sorted(expected_active_names - active_names)
        extra = sorted(active_names - expected_active_names)
        raise EvidenceFailure(f"subtraction active-task identity set mismatch: missing={missing} extra={extra}")
    missing = sorted(REQUIRED_ACTIVE_NAMES - represented_names)
    if missing:
        raise EvidenceFailure(f"subtraction omitted required active/preserved checkpoints: {missing}")
    if sorted(subtraction.get("required_checkpoint_names", [])) != sorted(REQUIRED_ACTIVE_NAMES):
        raise EvidenceFailure("subtraction required checkpoint identity set is not pinned")
    if sorted(SHARED_PATHS) != sorted(row.get("path") for row in subtraction.get("shared_path_dependencies", [])):
        raise EvidenceFailure("shared Wire/architecture dependencies are not explicit")
    if subtraction.get("all_open_retirement_pr_numbers") != expected_numbers:
        raise EvidenceFailure("subtraction all_open_retirement_pr_numbers is incomplete")
    return {"source_revision": SOURCE_REVISION, "owned_source_paths": len([path for path, owners in path_owners.items() if owners]), "unsubtracted_source_paths": len([path for path, owners in path_owners.items() if not owners]), "active_tasks": len(subtraction.get("active_tasks", [])), "preserved_checkpoints": len(subtraction.get("preserved_unmerged_checkpoints", [])), "retirement_pr_numbers": expected_numbers}


def validate_subtraction() -> dict[str, Any]:
    subtraction = read_json(HERE / "subtraction.json")
    prs = read_json(HERE / "pr-inventory.json")
    board = read_json(HERE / "canonical-board.json")
    return validate_subtraction_document(subtraction, prs, board)


def validate_candidates() -> dict[str, Any]:
    inventory = read_json(HERE / "analysis/inventory.json")
    subtraction = read_json(HERE / "subtraction.json")
    document = read_json(HERE / "candidates.json")
    if document.get("source_revision") != SOURCE_REVISION:
        raise EvidenceFailure("candidate source revision is not the accepted-main pin")
    candidates = document.get("candidates")
    if not isinstance(candidates, list) or len(candidates) < 3:
        raise EvidenceFailure("fewer than three retirement candidates were specified")
    symbol_by_id = {symbol["id"]: symbol for symbol in inventory.get("symbols", [])}
    file_by_path = {file["path"]: file for file in inventory.get("files", [])}
    used_files: set[str] = set()
    used_destinations: set[str] = set()
    for candidate in candidates:
        paths = candidate.get("current_writer_paths")
        if not isinstance(paths, list) or not paths:
            raise EvidenceFailure(f"candidate has no current writer paths: {candidate.get('id')}")
        if used_files.intersection(paths):
            raise EvidenceFailure(f"candidate writer paths overlap: {candidate.get('id')}")
        used_files.update(paths)
        for path in paths:
            if path not in file_by_path:
                raise EvidenceFailure(f"candidate path is not an accepted-main production file: {path}")
            if subtraction.get("path_owners", {}).get(path):
                raise EvidenceFailure(f"candidate path intersects subtraction ownership: {path}")
            if path in SHARED_PATHS:
                raise EvidenceFailure(f"candidate path is shared: {path}")
        destination = candidate.get("destination") or {}
        if not all(isinstance(destination.get(key), str) and destination.get(key) for key in ("public_contract", "private_internal", "wire")):
            raise EvidenceFailure(f"candidate lacks public/private/Wire destination: {candidate.get('id')}")
        destinations = set(re.findall(r"go-agent-runtime/services/[A-Za-z0-9_/-]+", " ".join(destination.values())))
        if used_destinations.intersection(destinations):
            raise EvidenceFailure(f"candidate destination paths overlap: {candidate.get('id')}")
        used_destinations.update(destinations)
        accounting = candidate.get("retirement_accounting") or {}
        if int(accounting.get("legacy_production_files_removed_floor", 0)) <= 0 or int(accounting.get("legacy_physical_lines_removed_floor", 0)) <= 0:
            raise EvidenceFailure(f"candidate lacks positive retirement floor: {candidate.get('id')}")
        if candidate.get("reliance_on_future_peer_deletion") or candidate.get("c107_mutation_lease"):
            raise EvidenceFailure(f"candidate contains an unauthorized dependency/lease: {candidate.get('id')}")
        symbols = candidate.get("current_symbols")
        if not isinstance(symbols, list) or not symbols:
            raise EvidenceFailure(f"candidate omits exact source symbols: {candidate.get('id')}")
        for row in symbols:
            actual = symbol_by_id.get(row.get("id"))
            if actual is None or actual["file"] not in paths or row.get("name") != actual.get("name") or row.get("class") != actual.get("class") or row.get("production_callers") != actual.get("caller_edges") or row.get("caller_edge_status") != actual.get("caller_edge_status"):
                raise EvidenceFailure(f"candidate symbol/caller evidence disagrees with inventory: {row.get('id')}")
        required = ("public_workflow", "expected_observable_effects", "focused_normal_checks", "focused_race_checks", "external_consumer_check", "accumulated_credential_free_regression", "causal_negative_control", "shared_file_dependencies", "remaining_immutable_criteria")
        if any(not candidate.get(key) for key in required):
            raise EvidenceFailure(f"candidate is missing an observable/check/dependency field: {candidate.get('id')}")
    return {"candidate_count": len(candidates), "source_files": len(used_files), "destination_groups": len(used_destinations), "pairwise_disjoint": True}


def validate_copy(inventory_path: pathlib.Path, subtraction_path: pathlib.Path, pr_path: pathlib.Path) -> None:
    inventory = read_json(inventory_path)
    subtraction = read_json(subtraction_path)
    prs = read_json(pr_path)
    validate_inventory(inventory_path, require_subtraction=False)
    validate_subtraction_document(subtraction, prs, read_json(HERE / "canonical-board.json"))


def negative_controls() -> dict[str, Any]:
    require_time("negative controls")
    original_inventory = HERE / "analysis/inventory.json"
    original_subtraction = HERE / "subtraction.json"
    original_pr = HERE / "pr-inventory.json"
    with tempfile.TemporaryDirectory(prefix="c107-negative-") as temp:
        temp_path = pathlib.Path(temp)
        mutated_subtraction = temp_path / "subtraction-mutated.json"
        subtraction = read_json(original_subtraction)
        first_owner = next((owner for owners in subtraction["path_owners"].values() for owner in owners), None)
        if first_owner is None:
            raise EvidenceFailure("negative control has no pinned PR owner to mutate")
        expected_head = first_owner["head"]
        first_owner["head"] = "0" * 40 if expected_head != "0" * 40 else "1" * 40
        write_json(mutated_subtraction, subtraction)
        head_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(original_inventory), "--subtraction", str(mutated_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))
        mutated_inventory = temp_path / "inventory-mutated.json"
        inventory = read_json(original_inventory)
        deleted_id = inventory["symbols"][0]["id"]
        del inventory["symbols"][0]["class"]
        write_json(mutated_inventory, inventory)
        class_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(mutated_inventory), "--subtraction", str(original_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))

        mutated_dependency_inventory = temp_path / "inventory-dependency-mutated.json"
        dependency_inventory = read_json(original_inventory)
        dependency_symbol = next(
            (symbol for symbol in dependency_inventory["symbols"] if symbol.get("existing_destination_service_dependency") != "NONE"),
            None,
        )
        if dependency_symbol is None:
            raise EvidenceFailure("negative control has no service dependency row to mutate")
        dependency = dependency_symbol["existing_destination_service_dependency"]
        if isinstance(dependency, list):
            dependency_symbol["existing_destination_service_dependency"] = sorted(set(dependency + ["github.com/fabricated/c107-service"]))
        else:
            dependency_symbol["existing_destination_service_dependency"] = ["github.com/fabricated/c107-service"]
        write_json(mutated_dependency_inventory, dependency_inventory)
        dependency_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(mutated_dependency_inventory), "--subtraction", str(original_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))

        mutated_row_inventory = temp_path / "inventory-row-deleted.json"
        row_deleted_inventory = read_json(original_inventory)
        deleted_symbol = row_deleted_inventory["symbols"].pop(0)
        totals = row_deleted_inventory["totals"]
        totals["top_level_symbols"] -= 1
        totals["exact_static_call_edges"] -= sum(1 for edge in row_deleted_inventory.get("call_edges", []) if edge.get("callee") == deleted_symbol["id"])
        totals["no_static_caller_symbols"] -= int(deleted_symbol.get("caller_edge_status") == "no-static-caller")
        class_counts = totals.get("class_counts", {})
        class_counts[deleted_symbol["class"]] -= 1
        row_deleted_inventory["call_edges"] = [edge for edge in row_deleted_inventory.get("call_edges", []) if edge.get("callee") != deleted_symbol["id"]]
        write_json(mutated_row_inventory, row_deleted_inventory)
        row_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(mutated_row_inventory), "--subtraction", str(original_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))

        mutated_checkpoint_subtraction = temp_path / "subtraction-checkpoint-deleted.json"
        checkpoint_subtraction = read_json(original_subtraction)
        preserved_rows = checkpoint_subtraction.get("preserved_unmerged_checkpoints", [])
        target_checkpoint = next((row for row in preserved_rows if int(row.get("pr_number", -1)) == 438), None)
        if target_checkpoint is None:
            target_checkpoint = preserved_rows[0] if preserved_rows else None
        if target_checkpoint is None:
            raise EvidenceFailure("negative control has no preserved checkpoint row to delete")
        deleted_pr_number = int(target_checkpoint["pr_number"])
        checkpoint_subtraction["preserved_unmerged_checkpoints"] = [row for row in preserved_rows if int(row.get("pr_number", -1)) != deleted_pr_number]
        checkpoint_subtraction["all_open_retirement_pr_numbers"] = [
            number for number in checkpoint_subtraction.get("all_open_retirement_pr_numbers", []) if int(number) != deleted_pr_number
        ]
        write_json(mutated_checkpoint_subtraction, checkpoint_subtraction)
        checkpoint_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(original_inventory), "--subtraction", str(mutated_checkpoint_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))

        mutated_active_subtraction = temp_path / "subtraction-active-task-mutated.json"
        active_subtraction = read_json(original_subtraction)
        active_target = next(
            (row for row in active_subtraction.get("active_tasks", []) if isinstance(row, dict) and isinstance(row.get("head"), str) and len(row["head"]) == 40),
            None,
        )
        if active_target is None:
            raise EvidenceFailure("negative control has no active task head to mutate")
        expected_active_head = active_target["head"]
        active_target["head"] = "0" * 40 if expected_active_head != "0" * 40 else "1" * 40
        write_json(mutated_active_subtraction, active_subtraction)
        active_result = subprocess.run([sys.executable, str(__file__), "--mode", "validate-copy", "--inventory", str(original_inventory), "--subtraction", str(mutated_active_subtraction), "--pr-inventory", str(original_pr)], cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=min(60, max(1, int(max(1, remaining_time())))))

        for label, result, expected_text in (
            ("pinned-pr-head", head_result, "subtraction PR head/path mismatch"),
            ("symbol-classification", class_result, "symbol classification missing or invalid"),
            ("service-dependency", dependency_result, "inventory service dependency is not source-derived"),
            ("symbol-row-deletion", row_result, "inventory symbol coverage mismatch"),
            ("preserved-pr-deletion", checkpoint_result, "subtraction omitted open retirement PR identities"),
            ("active-task-head", active_result, "subtraction active-task identity mismatch"),
        ):
            combined = redact_text(result.stdout + result.stderr)
            if result.returncode == 0 or expected_text not in combined:
                raise EvidenceFailure(f"{label} mutation did not fail closed with stable diagnostic")
        report = {
            "schema_version": "c107-negative-controls-v3",
            "source_revision": SOURCE_REVISION,
            "pr_head_mutation": {"exit_code": head_result.returncode, "diagnostic": redact_text(head_result.stderr.strip() or head_result.stdout.strip()), "mutated_from": expected_head, "mutated_to": first_owner["head"]},
            "symbol_classification_deletion": {"exit_code": class_result.returncode, "diagnostic": redact_text(class_result.stderr.strip() or class_result.stdout.strip()), "deleted_symbol_id": deleted_id},
            "service_dependency_mutation": {"exit_code": dependency_result.returncode, "diagnostic": redact_text(dependency_result.stderr.strip() or dependency_result.stdout.strip()), "mutated_symbol_id": dependency_symbol["id"], "fabricated_dependency": "github.com/fabricated/c107-service"},
            "symbol_row_deletion": {"exit_code": row_result.returncode, "diagnostic": redact_text(row_result.stderr.strip() or row_result.stdout.strip()), "deleted_symbol_id": deleted_symbol["id"], "self_reported_totals_adjusted": True},
            "preserved_pr_deletion": {
                "exit_code": checkpoint_result.returncode,
                "diagnostic": redact_text(checkpoint_result.stderr.strip() or checkpoint_result.stdout.strip()),
                "deleted_pr_number": deleted_pr_number,
                "self_reported_retirement_list_adjusted": True,
            },
            "active_task_head_mutation": {
                "exit_code": active_result.returncode,
                "diagnostic": redact_text(active_result.stderr.strip() or active_result.stdout.strip()),
                "task_name": active_target["name"],
                "mutated_from": expected_active_head,
                "mutated_to": active_target["head"],
            },
            "pristine_validation": validate_subtraction(),
        }
    write_json(HERE / "runs/negative-controls/report.json", report)
    return report


def extract_source_archive(temp_root: pathlib.Path) -> tuple[pathlib.Path, str]:
    archive = git("archive", "--format=tar", SOURCE_REVISION, text=False)
    extracted = temp_root / "archive"
    extracted.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=__import__("io").BytesIO(archive), mode="r:") as tar:
        try:
            tar.extractall(extracted, filter="data")
        except TypeError:  # pragma: no cover
            tar.extractall(extracted)
    direct = extracted / "agent-cli"
    if direct.is_dir():
        return extracted, sha256_bytes(archive)
    roots = sorted({path.parent.parent for path in extracted.rglob("go.mod") if (path.parent.parent / "agent-cli").is_dir()})
    if len(roots) != 1:
        raise EvidenceFailure("accepted source archive root is ambiguous")
    return roots[0], sha256_bytes(archive)


def build_inputs(snapshot: pathlib.Path) -> dict[str, Any]:
    roots = ["agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "tests"]
    entries = []
    for root_name in roots:
        root = snapshot / root_name
        if not root.is_dir():
            raise EvidenceFailure(f"build input root missing from accepted archive: {root_name}")
        for path in sorted(root.rglob("*")):
            if not path.is_file() or ".git" in path.parts:
                continue
            entries.append({"path": path.relative_to(snapshot).as_posix(), "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    manifest = stable_json(entries).encode()
    return {"roots": roots, "files": len(entries), "entries_sha256": sha256_bytes(manifest), "entries": entries}


def public_smoke() -> dict[str, Any]:
    require_time("public smoke")
    if not REPLAY_FIXTURE.exists():
        raise EvidenceFailure(f"credential-free replay fixture is absent from the repository: {REPLAY_FIXTURE}")
    public_dir = HERE / "runs/public"
    public_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="c107-yui-") as temp:
        temp_path = pathlib.Path(temp)
        snapshot, archive_hash = extract_source_archive(temp_path)
        binary = temp_path / "yui"
        build = run_bounded("build-yui", ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./cmd/yui"], snapshot / "agent-cli", public_dir, timeout=240)
        require_success(build)
        if not binary.is_file():
            raise EvidenceFailure("accepted-main yui build did not produce an executable")
        binary_hash = sha256_file(binary)
        inputs = build_inputs(snapshot)
        help_top = run_bounded("yui-help", [str(binary), "--help"], snapshot, public_dir)
        help_session = run_bounded("yui-session-help", [str(binary), "session", "--help"], snapshot, public_dir)
        require_success(help_top)
        require_success(help_session)
        def command_output(record: dict[str, Any]) -> str:
            return "\n".join((HERE / record["stdout_path"]).read_text(encoding="utf-8", errors="replace") for _ in [0]) + "\n" + (HERE / record["stderr_path"]).read_text(encoding="utf-8", errors="replace")
        top_text = command_output(help_top)
        session_text = command_output(help_session)
        for literal in ("Available Commands:", "session", "--workdir", "--allow-path"):
            if literal not in top_text:
                raise EvidenceFailure(f"top-level yui help omitted required literal: {literal}")
        for literal in ("Usage:", "--replay", "--record-dir", "--trace-audio"):
            if literal not in session_text:
                raise EvidenceFailure(f"session yui help omitted required literal: {literal}")
        replay_dir = public_dir / "replay"
        if replay_dir.exists():
            shutil.rmtree(replay_dir)
        replay_dir.mkdir(parents=True, exist_ok=True)
        (replay_dir / "evidence/runs").mkdir(parents=True, exist_ok=True)
        fixture = snapshot / REPLAY_FIXTURE.relative_to(ROOT)
        replay = run_bounded(
            "yui-local-replay",
            [str(binary), "session", "--replay", str(fixture), "--audio-out", str(replay_dir / "audio.wav"), "--record-dir", str(replay_dir / "tool-record"), "--trace-audio", "--workdir", str(replay_dir), "--allow-path", str(replay_dir)],
            replay_dir,
            public_dir,
            timeout=240,
        )
        require_success(replay)
        replay_text = command_output(replay)
        for literal in ("PROBE_TOOL_MARKER_9182", "strict replay continuation"):
            if literal not in replay_text:
                raise EvidenceFailure(f"credential-free replay omitted expected marker: {literal}")
        audio = replay_dir / "audio.wav"
        raw_pcm = replay_dir / "tool-record/audio/out-000.pcm"
        session_log = replay_dir / "tool-record/session-log.jsonl"
        manifest = replay_dir / "tool-record/manifest.json"
        if not audio.is_file() or audio.stat().st_size <= 44:
            raise EvidenceFailure("credential-free replay did not produce playable audio")
        if not raw_pcm.is_file() or raw_pcm.stat().st_size != 4800 or sha256_file(raw_pcm) != "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502":
            raise EvidenceFailure("credential-free replay raw PCM oracle changed")
        if not session_log.is_file() or not manifest.is_file():
            raise EvidenceFailure("credential-free replay omitted session evidence")
        turns = [json.loads(line) for line in session_log.read_text(encoding="utf-8").splitlines() if line.strip()]
        if len(turns) != 1 or turns[0].get("input", {}).get("text") != "probe PROBE_TOOL_MARKER_9182" or turns[0].get("response", {}).get("text") != "strict replay continuation":
            raise EvidenceFailure("credential-free replay session log lost the expected tool continuation")
        terminal = read_json(manifest).get("terminal", {})
        if terminal.get("reason") != "fixture_complete" or terminal.get("classification") != "provider_close":
            raise EvidenceFailure(f"credential-free replay terminal evidence changed: {terminal!r}")
        owned_bytes = sum(path.stat().st_size for path in replay_dir.rglob("*") if path.is_file())
        if owned_bytes > 16 * 1024 * 1024:
            raise EvidenceFailure("credential-free replay exceeded its bounded output quota")
        report = {
            "schema_version": "c107-public-smoke-v1",
            "observed_at": now(),
            "source_revision": SOURCE_REVISION,
            "source_archive_sha256": archive_hash,
            "candidate_revision_at_run": str(git("rev-parse", "HEAD")),
            "binary_sha256": binary_hash,
            "binary_bytes": binary.stat().st_size,
            "build_inputs": {"files": inputs["files"], "entries_sha256": inputs["entries_sha256"], "roots": inputs["roots"]},
            "fixture": str(REPLAY_FIXTURE.relative_to(ROOT)),
            "fixture_sha256": sha256_file(REPLAY_FIXTURE),
            "help": {"top": help_top, "session": help_session},
            "replay": replay,
            "audio": {"path": str(audio.relative_to(HERE)), "bytes": audio.stat().st_size, "sha256": sha256_file(audio)},
            "raw_pcm": {"path": str(raw_pcm.relative_to(HERE)), "bytes": raw_pcm.stat().st_size, "sha256": sha256_file(raw_pcm)},
            "session_log_sha256": sha256_file(session_log),
            "terminal": terminal,
            "owned_output_bytes": owned_bytes,
            "credential_free": True,
            "realtime": "not used; offline fixture replay",
            "result_classification": "SOFTWARE_LOCAL_PROCESS_ONLY",
        }
    write_json(public_dir / "report.json", report)
    return report


def focused_regressions() -> dict[str, Any]:
    run_dir = HERE / "runs/focused-regressions"
    pattern = "^(TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup|TestSessionDirectoryRecordingWritesConversationSessionLog|TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration)$"
    results = []
    for label, extra in (("normal", []), ("race", ["-race"])):
        command = ["go", "test", "./internal/services/internal/agentruntime", "-tags=nomicrophone", "-count=1", "-timeout=120s", "-run", pattern, "-v", *extra]
        result = run_bounded(f"agentruntime-{label}", command, ROOT / "agent-cli", run_dir, timeout=150)
        require_success(result)
        output = (HERE / result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
        expected = ["TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup", "TestSessionDirectoryRecordingWritesConversationSessionLog", "TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration"]
        missing = [name for name in expected if f"--- PASS: {name}" not in output]
        if missing:
            raise EvidenceFailure(f"focused {label} regression did not execute all tests: {missing}")
        result["expected_tests"] = expected
        results.append(result)
    report = {"schema_version": "c107-focused-regressions-v1", "source_revision": SOURCE_REVISION, "results": results, "ci_polling": "not performed"}
    write_json(run_dir / "report.json", report)
    return report


def accumulated_regressions() -> dict[str, Any]:
    run_dir = HERE / "runs/accumulated-regressions"
    result = run_bounded("session-ci-regressions-count-1-all", ["bash", "scripts/test-session-ci-regressions.sh", "all"], ROOT, run_dir, timeout=540, env={"COUNT": "1"}, output_cap=MAX_OUTPUT_BYTES)
    require_success(result)
    report = {"schema_version": "c107-accumulated-regressions-v1", "source_revision": SOURCE_REVISION, "candidate_revision_at_run": str(git("rev-parse", "HEAD")), "count": 1, "result": result, "command": "COUNT=1 scripts/test-session-ci-regressions.sh all", "ci_polling": "not performed", "credentials": "not used"}
    write_json(run_dir / "report.json", report)
    return report


def scope_checks() -> dict[str, Any]:
    origin = str(git("rev-parse", "origin/main"))
    tracked = str(git("diff", "--name-only", f"{origin}...HEAD"))
    untracked = str(git("ls-files", "--others", "--exclude-standard"))
    paths = sorted(set(tracked.splitlines() + untracked.splitlines()))
    allowed = str(HERE.relative_to(ROOT)) + "/"
    outside = [path for path in paths if path != str(HERE.relative_to(ROOT)) and not path.startswith(allowed)]
    if outside:
        raise EvidenceFailure(f"executor diff contains paths outside C107 evidence scope: {outside[:20]}")
    diff_check = subprocess.run(["git", "diff", "--check", "--", str(HERE.relative_to(ROOT))], cwd=ROOT, capture_output=True, text=True, timeout=60)
    if diff_check.returncode != 0:
        raise EvidenceFailure(f"git diff --check failed: {diff_check.stdout}{diff_check.stderr}")
    source_dirty = subprocess.run(["git", "diff", "--quiet", origin, "--", "agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "tests", "go.work", "go.work.sum"], cwd=ROOT, capture_output=True, timeout=60)
    if source_dirty.returncode != 0:
        raise EvidenceFailure("production/build inputs differ from fetched origin/main; C107 may not mutate them")
    for path in SHARED_PATHS:
        if subprocess.run(["git", "diff", "--quiet", origin, "--", path], cwd=ROOT, capture_output=True, timeout=60).returncode != 0:
            raise EvidenceFailure(f"shared path changed by C107: {path}")
    return {"schema_version": "c107-scope-check-v1", "origin_main": origin, "executor_diff_paths": paths, "outside_owned_scope": outside, "git_diff_check": True, "production_inputs_unchanged_from_origin_main": True, "shared_paths_unchanged": True}


def checksums() -> dict[str, Any]:
    checksum_path = HERE / "SHA256SUMS"
    entries = []
    for path in sorted(HERE.rglob("*")):
        if not path.is_file() or path == checksum_path or path.name == "verification-summary.json" or "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        entries.append(f"{sha256_file(path)}  {path.relative_to(HERE).as_posix()}")
    checksum_path.write_text("\n".join(entries) + "\n", encoding="utf-8")
    return {"path": str(checksum_path.relative_to(ROOT)), "entries": len(entries), "sha256": sha256_file(checksum_path), "excluded": ["SHA256SUMS", "verification-summary.json", "__pycache__", "*.pyc"]}


def run_mode(mode: str, first: pathlib.Path | None = None, second: pathlib.Path | None = None) -> dict[str, Any]:
    if mode == "provenance":
        return provenance()
    if mode == "state":
        return capture_state()
    if mode == "inventory":
        if first and second:
            return external_determinism(first, second)
        return run_inventory()
    if mode == "determinism":
        if not first or not second:
            raise EvidenceFailure("determinism requires --first and --second analyzer directories")
        return external_determinism(first, second)
    if mode == "subtraction":
        return validate_subtraction()
    if mode == "candidates":
        return validate_candidates()
    if mode == "disjointness":
        result = validate_candidates()
        result["check"] = "pairwise file and destination disjointness"
        return result
    if mode == "retirement-floors":
        result = validate_candidates()
        document = read_json(HERE / "candidates.json")
        result["retirement_floors"] = [
            {
                "id": candidate["id"],
                "legacy_production_files_removed_floor": candidate["retirement_accounting"]["legacy_production_files_removed_floor"],
                "legacy_physical_lines_removed_floor": candidate["retirement_accounting"]["legacy_physical_lines_removed_floor"],
                "counted_scope": candidate["retirement_accounting"]["counted_scope"],
            }
            for candidate in document["candidates"]
        ]
        return result
    if mode == "negative-controls":
        return negative_controls()
    if mode == "public":
        return public_smoke()
    if mode == "focused-regressions":
        return focused_regressions()
    if mode == "accumulated-regressions":
        return accumulated_regressions()
    if mode == "scope":
        return scope_checks()
    if mode == "all":
        external = external_determinism(first, second) if first and second else None
        capture_state()
        reports: dict[str, Any] = {}
        if external:
            reports["external_determinism"] = external
        reports["provenance"] = provenance()
        reports["inventory"] = run_inventory()
        materialize_candidates()
        reports["subtraction"] = validate_subtraction()
        reports["candidates"] = validate_candidates()
        reports["negative_controls"] = negative_controls()
        reports["public"] = public_smoke()
        reports["focused_regressions"] = focused_regressions()
        reports["accumulated_regressions"] = accumulated_regressions()
        reports["scope"] = scope_checks()
        reports["checksums"] = checksums()
        summary = {"schema_version": "c107-verification-summary-v1", "observed_at": now(), "candidate_revision": str(git("rev-parse", "HEAD")), "reports": reports, "all_project_criteria": "OPEN", "c107_result": "scoped characterization only; no retirement or project acceptance"}
        write_json(HERE / "verification-summary.json", summary)
        return {"status": "passed", "mode": mode, "candidate_revision": summary["candidate_revision"], "reports": {key: "passed" for key in reports}}
    if mode == "release":
        capture_state()
        reports = {
            "provenance": provenance(),
            "inventory": run_inventory(),
        }
        materialize_candidates()
        reports["subtraction"] = validate_subtraction()
        reports["candidates"] = validate_candidates()
        reports["negative_controls"] = negative_controls()
        reports["public"] = public_smoke()
        reports["focused_regressions"] = focused_regressions()
        reports["accumulated_regressions"] = accumulated_regressions()
        reports["scope"] = scope_checks()
        reports["checksums"] = checksums()
        summary = {"schema_version": "c107-release-summary-v1", "observed_at": now(), "candidate_revision": str(git("rev-parse", "HEAD")), "reports": reports, "all_project_criteria": "OPEN", "ci": "submitted by script gate after this handoff; not polled by executor", "review": "independent review remains external"}
        write_json(HERE / "verification-summary.json", summary)
        return {"status": "passed", "mode": mode, "candidate_revision": summary["candidate_revision"], "reports": {key: "passed" for key in reports}}
    if mode == "validate-copy":
        raise EvidenceFailure("validate-copy requires explicit input paths and is dispatched by negative-controls")
    raise EvidenceFailure(f"unknown mode: {mode}")


def main() -> int:
    global AGGREGATE_STARTED, REQUESTED_CHILD_TIMEOUT, REQUESTED_TOTAL_TIMEOUT
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["state", "provenance", "inventory", "determinism", "subtraction", "candidates", "disjointness", "retirement-floors", "negative-controls", "public", "focused-regressions", "accumulated-regressions", "scope", "all", "release", "validate-copy"], required=True)
    parser.add_argument("--child-timeout", type=int, default=CHILD_TIMEOUT)
    parser.add_argument("--total-timeout", type=int, default=TOTAL_TIMEOUT)
    parser.add_argument("--inventory", type=pathlib.Path)
    parser.add_argument("--subtraction", type=pathlib.Path)
    parser.add_argument("--pr-inventory", type=pathlib.Path)
    parser.add_argument("--first", type=pathlib.Path)
    parser.add_argument("--second", type=pathlib.Path)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.total_timeout <= 0 or args.child_timeout > args.total_timeout:
        parser.error("positive --child-timeout must not exceed --total-timeout")
    REQUESTED_CHILD_TIMEOUT = args.child_timeout
    REQUESTED_TOTAL_TIMEOUT = args.total_timeout
    AGGREGATE_STARTED = time.monotonic()
    try:
        if args.mode == "validate-copy":
            if not args.inventory or not args.subtraction or not args.pr_inventory:
                parser.error("validate-copy requires --inventory, --subtraction and --pr-inventory")
            validate_copy(args.inventory.resolve(), args.subtraction.resolve(), args.pr_inventory.resolve())
            print(json.dumps({"status": "passed", "mode": args.mode}, sort_keys=True))
            return 0
        result = run_mode(args.mode, args.first.resolve() if args.first else None, args.second.resolve() if args.second else None)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": redact_text(str(exc))}, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
