#!/usr/bin/env python3
"""Opt-in, fail-closed profiling for the hermetic Go test lane.

The default command surface is deliberately small:

  inventory  discover the six hermetic modules and write a run manifest
  warm       download dependencies and compile test binaries without running tests
  run        execute one inventory or a bounded package cohort
  analyze    inspect captured JSON offline and produce an honest report

Inventory, warm and run never start unless the caller supplies both
--allow-heavy and current evidence for a dedicated runner.  The synthetic
control manifests used by controls.py are the only exception; they run small
Python fixtures and never invoke Go.
"""

from __future__ import annotations

import argparse
import datetime as _datetime
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import sys
import time
import uuid
from typing import Any, Iterable


SCHEMA = "c11-hermetic-profile-manifest-v1"
ANALYSIS_SCHEMA = "c11-hermetic-profile-analysis-v1"
QUIET_SCHEMA = "c11-quiet-evidence-v1"
COMMAND_RECORD_SCHEMA = "c11-command-record-v1"
TARGET_SECONDS = 180.0
GENERAL_TIMEOUT_SECONDS = 300
AGENT_CLI_TIMEOUT_SECONDS = 480
MAX_DURATION_NANOSECONDS = (1 << 63) - 1
MAX_DURATION_SECONDS = MAX_DURATION_NANOSECONDS / 1_000_000_000.0
MAX_MONOTONIC_NANOSECONDS = (1 << 63) - 1
DEFAULT_PARALLELISM = max(1, os.cpu_count() or 1)
MODULES = (
    ("agent-cli", "agent-cli"),
    ("go-agent-loop", "go-agent-loop"),
    ("go-llm-gateway", "go-llm-gateway"),
    ("go-audio", "go-audio"),
    ("go-device-gateway", "go-device-gateway"),
    ("go-agent-runtime", "go-agent-runtime"),
)
PACKAGE_TERMINALS = {"pass", "fail", "skip"}
TEST_TERMINALS = {"pass", "fail", "skip"}
SAFE_LABEL = re.compile(r"[^A-Za-z0-9_.-]+")
COMMAND_PHASES = {"metadata", "inventory", "warm", "full", "cohort"}
RUN_PHASES = {"full", "cohort"}
COMMAND_ROLES = {
    "source-rev-parse",
    "source-status",
    "go-env",
    "go-version",
    "go-list",
    "go-mod-download",
    "go-test-binary-compile",
    "go-test",
    "synthetic-test",
}
LEGACY_COMMAND_COLLECTIONS = (
    "metadata_commands",
    "inventory_commands",
    "runs",
    "run_groups",
)


class ProfileError(Exception):
    """An actionable input or provenance error at the command boundary."""


def utc_now() -> str:
    return _datetime.datetime.now(_datetime.timezone.utc).isoformat(
        timespec="milliseconds"
    ).replace("+00:00", "Z")


def parse_utc(value: Any) -> _datetime.datetime | None:
    if not isinstance(value, str) or not value:
        return None
    try:
        text = value.replace("Z", "+00:00")
        parsed = _datetime.datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=_datetime.timezone.utc)
    return parsed.astimezone(_datetime.timezone.utc)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )


def write_json_atomic(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    write_json(temporary, value)
    os.replace(temporary, path)


def safe_label(value: str) -> str:
    cleaned = SAFE_LABEL.sub("-", value).strip("-")
    return cleaned or "command"


def relative_path(path: Path, root: Path) -> str:
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except ValueError as exc:
        raise ProfileError(f"artifact path {path} is outside output root {root}") from exc


def artifact_path(root: Path, value: str) -> Path:
    root_path = root.resolve()
    path = Path(value)
    resolved = (path if path.is_absolute() else root_path / path).resolve()
    try:
        resolved.relative_to(root_path)
    except ValueError as exc:
        raise ProfileError(
            f"artifact path {value!r} resolves outside output root {root_path}"
        ) from exc
    return resolved


def decode_output(value: bytes) -> str:
    return value.decode("utf-8", errors="replace")


def _kill_process_tree(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    else:
        try:
            process.kill()
        except ProcessLookupError:
            pass


def command_record(
    argv: list[str],
    *,
    cwd: Path,
    env_overrides: dict[str, str],
    timeout_seconds: float,
    output_root: Path,
    output_dir: Path,
    label: str,
    stdout_suffix: str = "stdout.log",
) -> tuple[dict[str, Any], bytes, bytes]:
    """Run one bounded argv without a shell and retain both output streams."""

    output_dir.mkdir(parents=True, exist_ok=True)
    prefix = safe_label(label)
    stdout_path = output_dir / f"{prefix}.{stdout_suffix}"
    stderr_path = output_dir / f"{prefix}.stderr.log"
    started_at = utc_now()
    started_mono = time.monotonic_ns()
    timed_out = False
    spawn_error: str | None = None
    stdout = b""
    stderr = b""
    exit_status: int | None = None
    process: subprocess.Popen[bytes] | None = None

    effective_env = os.environ.copy()
    effective_env.update(env_overrides)
    try:
        process = subprocess.Popen(
            argv,
            cwd=str(cwd),
            env=effective_env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=(os.name == "posix"),
        )
        try:
            stdout, stderr = process.communicate(timeout=timeout_seconds)
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            _kill_process_tree(process)
            tail_stdout, tail_stderr = process.communicate()
            stdout = tail_stdout or (
                exc.output if isinstance(exc.output, bytes) else b""
            )
            stderr = tail_stderr or (
                exc.stderr if isinstance(exc.stderr, bytes) else b""
            )
        exit_status = process.returncode
    except OSError as exc:
        spawn_error = str(exc)
    ended_mono = time.monotonic_ns()
    ended_at = utc_now()

    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    record: dict[str, Any] = {
        "schema": "c11-command-record-v1",
        "record_id": label,
        "label": label,
        "argv": [str(item) for item in argv],
        "cwd": str(cwd.resolve()),
        "env_overrides": {key: env_overrides[key] for key in sorted(env_overrides)},
        "timeout_seconds": timeout_seconds,
        "started_at_utc": started_at,
        "ended_at_utc": ended_at,
        "monotonic_start_ns": started_mono,
        "monotonic_end_ns": ended_mono,
        "wall_seconds": (ended_mono - started_mono) / 1_000_000_000.0,
        "timed_out": timed_out,
        "exit_status": exit_status,
        "signal": (-exit_status if exit_status is not None and exit_status < 0 else None),
        "spawn_error": spawn_error,
        "stdout_path": relative_path(stdout_path, output_root),
        "stdout_sha256": sha256_bytes(stdout),
        "stdout_bytes": len(stdout),
        "stderr_path": relative_path(stderr_path, output_root),
        "stderr_sha256": sha256_bytes(stderr),
        "stderr_bytes": len(stderr),
        "status": (
            "SPAWN_ERROR"
            if spawn_error
            else "TIMEOUT"
            if timed_out
            else "PASS"
            if exit_status == 0
            else "FAIL"
        ),
    }
    return record, stdout, stderr


def set_command_context(
    record: dict[str, Any],
    *,
    phase: str,
    module: str,
    role: str,
    trial: int | None,
    selected_packages: Iterable[str] = (),
) -> dict[str, Any]:
    """Attach the one canonical command-record context before storing a record."""

    record.update(
        {
            "record_id": record.get("record_id") or record.get("label"),
            "phase": phase,
            "module": module,
            "role": role,
            "trial": trial,
            "selected_packages": list(selected_packages),
        }
    )
    return record


def require_positive(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be a positive integer") from exc
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ProfileError(f"JSON file not found: {path}") from exc
    except (OSError, json.JSONDecodeError) as exc:
        raise ProfileError(f"cannot read JSON file {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ProfileError(f"JSON file {path} must contain an object")
    return value


def runner_metadata(go_version: str | None = None) -> dict[str, Any]:
    cpu_model = platform.processor() or "unknown"
    if sys.platform == "darwin":
        try:
            cpu_model = (
                subprocess.check_output(
                    ["sysctl", "-n", "machdep.cpu.brand_string"],
                    stderr=subprocess.DEVNULL,
                    timeout=3,
                )
                .decode("utf-8", errors="replace")
                .strip()
                or cpu_model
            )
        except (OSError, subprocess.SubprocessError):
            pass
    elif Path("/proc/cpuinfo").is_file():
        try:
            for line in Path("/proc/cpuinfo").read_text(
                encoding="utf-8", errors="replace"
            ).splitlines():
                if line.lower().startswith("model name"):
                    cpu_model = line.split(":", 1)[-1].strip() or cpu_model
                    break
        except OSError:
            pass
    result: dict[str, Any] = {
        "os": platform.system() or "unknown",
        "os_release": platform.release() or "unknown",
        "architecture": platform.machine() or "unknown",
        "cpu": cpu_model,
        "logical_cpus": os.cpu_count() or 1,
        "python_version": platform.python_version(),
    }
    if go_version is not None:
        result["go_version"] = go_version
    return result


def parse_go_env(stdout: bytes) -> dict[str, str]:
    text = decode_output(stdout).strip()
    if not text:
        return {}
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        parsed = None
    if isinstance(parsed, dict):
        return {str(key): str(value) for key, value in parsed.items()}
    values = text.splitlines()
    keys = (
        "GOOS",
        "GOARCH",
        "GOVERSION",
        "GOMODCACHE",
        "GOCACHE",
        "GOPATH",
        "GOWORK",
    )
    return {
        key: values[index]
        for index, key in enumerate(keys)
        if index < len(values)
    }


def quiet_evidence_contract_error(evidence: Any, *, mode: str) -> str | None:
    if not isinstance(evidence, dict):
        return "quiet evidence is not a JSON object"
    if evidence.get("schema") != QUIET_SCHEMA:
        return (
            f"quiet evidence has schema {evidence.get('schema')!r}, "
            f"want {QUIET_SCHEMA!r}"
        )
    if evidence.get("valid") is not True:
        reason = evidence.get("reason") or "valid=false"
        return f"quiet evidence is not valid: {reason}"
    isolation = evidence.get("isolation")
    allowed = {"synthetic-control"} if mode == "synthetic" else {"isolated", "dedicated"}
    if isolation not in allowed:
        return (
            f"quiet evidence isolation {isolation!r} is not allowed for {mode}; "
            f"required one of {sorted(allowed)}"
        )
    captured = parse_utc(evidence.get("captured_at_utc"))
    if captured is None:
        return "quiet evidence captured_at_utc is not an ISO timestamp"
    expires = parse_utc(evidence.get("valid_until_utc"))
    if expires is None:
        return "quiet evidence valid_until_utc is not an ISO timestamp"
    now = _datetime.datetime.now(_datetime.timezone.utc)
    if captured > now:
        return "quiet evidence captured_at_utc is in the future"
    if expires <= now:
        return "quiet evidence has expired"
    if expires <= captured:
        return "quiet evidence valid_until_utc is not after captured_at_utc"
    runner = evidence.get("runner")
    if not isinstance(runner, dict):
        return "quiet evidence is missing runner metadata"
    missing = [
        key
        for key in ("os", "architecture", "cpu", "go_version")
        if not str(runner.get(key, "")).strip()
    ]
    if missing:
        return "quiet evidence is missing runner fields: " + ", ".join(missing)
    for phase in ("before", "after"):
        observation = evidence.get(phase)
        if not isinstance(observation, dict):
            return f"quiet evidence must include {phase} load/lease observations"
        missing = [key for key in ("active_work", "load") if key not in observation]
        if "processes" not in observation and "process_activity" not in observation:
            missing.append("processes/process_activity")
        if missing:
            return (
                f"quiet evidence {phase} is missing observations: "
                + ", ".join(missing)
            )
        if not isinstance(observation["active_work"], list):
            return f"quiet evidence {phase}.active_work must be a list"
        process_key = (
            "processes" if "processes" in observation else "process_activity"
        )
        process_observation = observation[process_key]
        if process_observation is None or (
            isinstance(process_observation, str) and not process_observation.strip()
        ):
            return f"quiet evidence {phase}.{process_key} must be an observation"
        if observation["load"] is None:
            return f"quiet evidence {phase}.load must be an observation"
    return None


def validate_quiet_evidence(
    path: Path, *, root: Path, mode: str
) -> dict[str, Any]:
    resolved_path = artifact_path(root, str(path))
    evidence = load_json(resolved_path)
    error = quiet_evidence_contract_error(evidence, mode=mode)
    if error:
        raise ProfileError(error)
    isolation = evidence["isolation"]
    expires = evidence["valid_until_utc"]
    return {
        "path": str(resolved_path),
        "sha256": sha256_file(resolved_path),
        "isolation": isolation,
        "captured_at_utc": evidence.get("captured_at_utc"),
        "valid_until_utc": expires,
        "runner": evidence.get("runner"),
        "active_work": evidence.get("before", {}).get("active_work")
        if isinstance(evidence.get("before"), dict)
        else None,
    }


def require_heavy(
    args: argparse.Namespace, *, mode: str, root: Path
) -> dict[str, Any]:
    if not args.allow_heavy:
        raise ProfileError(
            f"{args.command} is opt-in; pass --allow-heavy only on the documented "
            "quiet/dedicated runner"
        )
    if not args.quiet_evidence:
        raise ProfileError(
            f"{args.command} requires --quiet-evidence; elapsed time does not prove quiet"
        )
    return validate_quiet_evidence(
        Path(args.quiet_evidence), root=root, mode=mode
    )


def package_arg(module_root: Path, package_dir: Path) -> str:
    relative = os.path.relpath(package_dir, module_root)
    if relative == ".":
        return "."
    return "./" + Path(relative).as_posix()


def parse_concatenated_json(stdout: bytes, *, label: str) -> list[dict[str, Any]]:
    text = decode_output(stdout)
    decoder = json.JSONDecoder()
    values: list[dict[str, Any]] = []
    index = 0
    while index < len(text):
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text):
            break
        try:
            value, end = decoder.raw_decode(text, index)
        except json.JSONDecodeError as exc:
            raise ProfileError(
                f"{label} emitted malformed concatenated JSON at offset {exc.pos}: "
                f"{exc.msg}"
            ) from exc
        if not isinstance(value, dict):
            raise ProfileError(f"{label} emitted a non-object package record")
        values.append(value)
        index = end
    if not values:
        raise ProfileError(f"{label} emitted an empty package inventory")
    return values


def make_package_record(
    module: dict[str, str], package: dict[str, Any]
) -> dict[str, Any]:
    import_path = package.get("ImportPath")
    package_dir = package.get("Dir")
    if not isinstance(import_path, str) or not import_path:
        raise ProfileError(f"{module['name']} inventory record has no ImportPath")
    if not isinstance(package_dir, str) or not package_dir:
        error = package.get("Error")
        raise ProfileError(
            f"{module['name']} package {import_path} has no Dir"
            + (f": {error}" if error else "")
        )
    module_root = Path(module["path"]).resolve()
    resolved_dir = Path(package_dir).resolve()
    try:
        resolved_dir.relative_to(module_root)
    except ValueError as exc:
        raise ProfileError(
            f"package {import_path} directory {resolved_dir} escapes module {module_root}"
        ) from exc
    test_files = [
        *package.get("TestGoFiles", []),
        *package.get("XTestGoFiles", []),
    ]
    go_files = [
        *package.get("GoFiles", []),
        *package.get("CgoFiles", []),
        *package.get("IgnoredGoFiles", []),
    ]
    return {
        "import_path": import_path,
        "dir": str(resolved_dir),
        "package_arg": package_arg(module_root, resolved_dir),
        "has_tests": bool(test_files),
        "test_files": sorted(str(item) for item in test_files),
        "go_files": sorted(str(item) for item in go_files),
        "for_test": package.get("ForTest"),
    }


def initial_manifest(
    *,
    repo: Path,
    output: Path,
    source_sha: str,
    source_dirty: list[str],
    runner: dict[str, Any],
    go_env: dict[str, str],
    go_path: str,
    gomaxprocs: int,
    parallelism: int,
    general_timeout: int,
    agent_timeout: int,
    source_plan: Path | None,
) -> dict[str, Any]:
    cache_root = output / "cache"
    cache_root.mkdir(parents=True, exist_ok=True)
    source_plan_hash = None
    if source_plan is not None:
        if not source_plan.is_file():
            raise ProfileError(f"source plan not found: {source_plan}")
        source_plan_hash = sha256_file(source_plan)
    return {
        "schema": SCHEMA,
        "project": "audio-runtime",
        "work": "audio-runtime-c11-hermetic-package-profile",
        "mode": "hermetic",
        "created_at_utc": utc_now(),
        "repo": str(repo),
        "source_sha": source_sha,
        "source_dirty_paths": source_dirty,
        "runner": runner,
        "go": {
            "executable": go_path,
            "version": runner.get("go_version", "unknown"),
            "env": go_env,
        },
        "flags": {
            "cgo_enabled": "0",
            "tags": ["nomicrophone"],
            "count": 1,
            "gomaxprocs": gomaxprocs,
            "go_test_p": parallelism,
            "general_timeout_seconds": general_timeout,
            "agent_cli_timeout_seconds": agent_timeout,
            "agent_cli_wrapper": "./cmd/testtimeout",
        },
        "cache_paths": {
            "root": str(cache_root.resolve()),
            "gocache": str((cache_root / "gocache").resolve()),
            "gomodcache": str((cache_root / "gomodcache").resolve()),
        },
        "paths": {
            "manifest": str((output / "manifest.json").resolve()),
            "inventory": str((output / "inventory").resolve()),
            "warm": str((output / "warm").resolve()),
            "runs": str((output / "runs").resolve()),
        },
        "source_plan": (
            {"path": str(source_plan.resolve()), "sha256": source_plan_hash}
            if source_plan is not None
            else None
        ),
        "inventory_status": "STARTED",
        "metadata_command_ids": [],
        "inventory_command_ids": [],
        "commands": [],
        "modules": [],
        "warm": {"status": "NOT_RUN", "command_ids": [], "test_binaries": []},
        "measurement_status": "UNMEASURED",
    }


def manifest_root(manifest_path: Path) -> Path:
    return manifest_path.resolve().parent


def parse_git_status_paths(stdout: bytes) -> list[str]:
    paths = []
    for line in decode_output(stdout).splitlines():
        if not line.strip():
            continue
        path = line[3:] if len(line) >= 3 else line
        if " -> " in path:
            path = path.rsplit(" -> ", 1)[-1]
        paths.append(path)
    return sorted(set(paths))


def source_paths_outside_output(
    paths: Iterable[str], *, repo: Path, output_root: Path
) -> list[str]:
    repo_root = repo.resolve()
    output_path = output_root.resolve()
    retained = []
    for path_value in paths:
        candidate = (repo_root / path_value).resolve()
        try:
            candidate.relative_to(output_path)
        except ValueError:
            retained.append(path_value)
    return sorted(set(retained))


def validate_source_state(
    manifest: dict[str, Any],
    *,
    output_root: Path,
    output_dir: Path,
    label: str,
    command_sink: list[dict[str, Any]],
    module: str,
    trial: int,
) -> dict[str, Any] | None:
    source_repo_value = manifest.get("source_repo")
    if not source_repo_value and manifest.get("mode") == "synthetic":
        return None
    repo_value = source_repo_value or manifest.get("repo")
    if not isinstance(repo_value, str) or not repo_value:
        raise ProfileError("manifest has no source repository")
    repo = Path(repo_value).resolve()
    if not repo.is_dir():
        raise ProfileError(f"source repository directory not found: {repo}")
    expected_sha = manifest.get("source_sha")
    if not isinstance(expected_sha, str) or not expected_sha:
        raise ProfileError("manifest has no source SHA")
    expected_dirty = manifest.get("source_dirty_paths", [])
    if not isinstance(expected_dirty, list):
        raise ProfileError("manifest source_dirty_paths must be a list")

    validation_dir = output_dir / "source-validation"
    head_record, head_stdout, _ = command_record(
        ["git", "-C", str(repo), "rev-parse", "HEAD"],
        cwd=repo,
        env_overrides={},
        timeout_seconds=30,
        output_root=output_root,
        output_dir=validation_dir,
        label=f"{label}-source-rev-parse",
        stdout_suffix="stdout.txt",
    )
    set_command_context(
        head_record,
        phase="metadata",
        module=module,
        role="source-rev-parse",
        trial=trial,
    )
    head_record["scope"] = "run"
    head_record["source_repo"] = str(repo)
    if head_record["status"] != "PASS":
        raise ProfileError("cannot identify current source SHA; see run evidence")
    current_sha = decode_output(head_stdout).strip()
    if not re.fullmatch(r"[0-9a-fA-F]{7,64}", current_sha):
        raise ProfileError(f"git returned invalid current source SHA: {current_sha!r}")
    status_record, status_stdout, _ = command_record(
        ["git", "-C", str(repo), "status", "--porcelain"],
        cwd=repo,
        env_overrides={},
        timeout_seconds=30,
        output_root=output_root,
        output_dir=validation_dir,
        label=f"{label}-source-status",
        stdout_suffix="stdout.txt",
    )
    set_command_context(
        status_record,
        phase="metadata",
        module=module,
        role="source-status",
        trial=trial,
    )
    status_record["scope"] = "run"
    status_record["source_repo"] = str(repo)
    if status_record["status"] != "PASS":
        raise ProfileError("cannot inspect current source dirtiness; see run evidence")
    current_dirty_all = parse_git_status_paths(status_stdout)
    current_dirty = source_paths_outside_output(
        current_dirty_all, repo=repo, output_root=output_root
    )
    expected_dirty_outside_output = source_paths_outside_output(
        [str(item) for item in expected_dirty],
        repo=repo,
        output_root=output_root,
    )
    matches = current_sha == expected_sha and current_dirty == expected_dirty_outside_output
    command_sink.extend((head_record, status_record))
    if not matches:
        raise ProfileError(
            "source changed since inventory: "
            f"expected HEAD {expected_sha!r}, current {current_sha!r}; "
            f"expected dirty paths {expected_dirty_outside_output!r}, "
            f"current {current_dirty!r}"
        )
    return {
        "repo": str(repo),
        "head": current_sha,
        "dirty_paths": current_dirty,
        "expected_head": expected_sha,
        "expected_dirty_paths": expected_dirty_outside_output,
        "matches_manifest": True,
        "metadata_record_ids": [head_record["record_id"], status_record["record_id"]],
    }


def save_manifest(manifest_path: Path, manifest: dict[str, Any]) -> None:
    manifest["updated_at_utc"] = utc_now()
    write_json_atomic(manifest_path, manifest)


def inventory(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    repo = Path(args.repo).resolve()
    output = Path(args.output).resolve()
    if not repo.is_dir():
        raise ProfileError(f"repository directory not found: {repo}")
    output.mkdir(parents=True, exist_ok=True)
    quiet = require_heavy(args, mode="hermetic", root=output)
    gomaxprocs = args.gomaxprocs or DEFAULT_PARALLELISM
    parallelism = args.parallelism or gomaxprocs
    cache_root = output / "cache"
    metadata_cache = {
        "CGO_ENABLED": "0",
        "GOMAXPROCS": str(gomaxprocs),
        "GOCACHE": str((cache_root / "gocache").resolve()),
        "GOMODCACHE": str((cache_root / "gomodcache").resolve()),
    }
    (cache_root / "gocache").mkdir(parents=True, exist_ok=True)
    (cache_root / "gomodcache").mkdir(parents=True, exist_ok=True)
    git_dir = output / "commands" / "metadata"
    git_head, git_head_stdout, _ = command_record(
        ["git", "-C", str(repo), "rev-parse", "HEAD"],
        cwd=repo,
        env_overrides={},
        timeout_seconds=30,
        output_root=output,
        output_dir=git_dir,
        label="source-rev-parse",
        stdout_suffix="stdout.txt",
    )
    if git_head["status"] != "PASS":
        raise ProfileError("cannot identify source SHA; see inventory command evidence")
    source_sha = decode_output(git_head_stdout).strip()
    if not re.fullmatch(r"[0-9a-fA-F]{7,64}", source_sha):
        raise ProfileError(f"git returned invalid source SHA: {source_sha!r}")
    dirty_record, dirty_stdout, _ = command_record(
        ["git", "-C", str(repo), "status", "--porcelain"],
        cwd=repo,
        env_overrides={},
        timeout_seconds=30,
        output_root=output,
        output_dir=git_dir,
        label="source-status",
        stdout_suffix="stdout.txt",
    )
    if dirty_record["status"] != "PASS":
        raise ProfileError("cannot inspect source dirtiness; see inventory command evidence")
    dirty_paths = parse_git_status_paths(dirty_stdout)
    go_env_record, go_env_stdout, _ = command_record(
        [args.go, "env", "-json", "GOOS", "GOARCH", "GOVERSION", "GOMODCACHE", "GOCACHE", "GOPATH", "GOWORK"],
        cwd=repo,
        env_overrides=metadata_cache,
        timeout_seconds=30,
        output_root=output,
        output_dir=git_dir,
        label="go-env",
        stdout_suffix="stdout.json",
    )
    if go_env_record["status"] != "PASS":
        raise ProfileError("go env failed; see inventory command evidence")
    go_env = parse_go_env(go_env_stdout)
    version_record, version_stdout, _ = command_record(
        [args.go, "version"],
        cwd=repo,
        env_overrides=metadata_cache,
        timeout_seconds=30,
        output_root=output,
        output_dir=git_dir,
        label="go-version",
        stdout_suffix="stdout.txt",
    )
    if version_record["status"] != "PASS":
        raise ProfileError("go version failed; see inventory command evidence")
    go_version = decode_output(version_stdout).strip()
    runner = runner_metadata(go_version)
    manifest_path = output / "manifest.json"
    manifest = initial_manifest(
        repo=repo,
        output=output,
        source_sha=source_sha,
        source_dirty=dirty_paths,
        runner=runner,
        go_env=go_env,
        go_path=args.go,
        gomaxprocs=gomaxprocs,
        parallelism=parallelism,
        general_timeout=args.general_timeout,
        agent_timeout=args.agent_timeout,
        source_plan=Path(args.source_plan).resolve() if args.source_plan else None,
    )
    manifest["quiet_evidence"] = quiet
    for record, role in (
        (git_head, "source-rev-parse"),
        (dirty_record, "source-status"),
        (go_env_record, "go-env"),
        (version_record, "go-version"),
    ):
        set_command_context(
            record,
            phase="metadata",
            module="manifest",
            role=role,
            trial=1,
        )
        record["scope"] = "manifest"
    manifest["commands"].extend(
        (git_head, dirty_record, go_env_record, version_record)
    )
    manifest["metadata_command_ids"] = [
        record["record_id"]
        for record in (git_head, dirty_record, go_env_record, version_record)
    ]
    save_manifest(manifest_path, manifest)

    env_overrides = {
        "CGO_ENABLED": "0",
        "GOMAXPROCS": str(gomaxprocs),
        "GOCACHE": manifest["cache_paths"]["gocache"],
        "GOMODCACHE": manifest["cache_paths"]["gomodcache"],
    }
    for name, relative in MODULES:
        module = {"name": name, "path": str((repo / relative).resolve()), "relative": relative}
        require_heavy(args, mode="hermetic", root=output)
        record, stdout, _ = command_record(
            [args.go, "list", "-json", "-tags=nomicrophone", "./..."],
            cwd=Path(module["path"]),
            env_overrides=env_overrides,
            timeout_seconds=args.agent_timeout if name == "agent-cli" else args.general_timeout,
            output_root=output,
            output_dir=output / "commands" / "inventory" / safe_label(name),
            label=f"inventory-{name}",
            stdout_suffix="stdout.json",
        )
        set_command_context(
            record,
            phase="inventory",
            module=name,
            role="go-list",
            trial=1,
        )
        record["scope"] = "manifest"
        manifest["commands"].append(record)
        manifest["inventory_command_ids"].append(record["record_id"])
        if record["status"] != "PASS":
            manifest["inventory_status"] = "FAILED"
            save_manifest(manifest_path, manifest)
            return {"status": "FAILED", "manifest": str(manifest_path), "failed_module": name}, 1
        try:
            package_values = parse_concatenated_json(stdout, label=f"go list {name}")
            packages = [
                make_package_record(module, package_value)
                for package_value in package_values
                if not package_value.get("ForTest")
            ]
        except ProfileError as exc:
            manifest["inventory_status"] = "FAILED"
            manifest.setdefault("inventory_errors", []).append(str(exc))
            save_manifest(manifest_path, manifest)
            return {"status": "FAILED", "manifest": str(manifest_path), "error": str(exc)}, 1
        packages.sort(key=lambda item: item["import_path"])
        module["packages"] = packages
        module["package_count"] = len(packages)
        module["test_package_count"] = sum(1 for item in packages if item["has_tests"])
        module["no_test_package_count"] = module["package_count"] - module["test_package_count"]
        manifest["modules"].append(module)
        save_manifest(manifest_path, manifest)

    manifest["inventory_status"] = "PASS"
    manifest["measurement_status"] = "INVENTORIED"
    save_manifest(manifest_path, manifest)
    summary = {
        "status": "PASS",
        "manifest": str(manifest_path),
        "source_sha": source_sha,
        "package_count": sum(item["package_count"] for item in manifest["modules"]),
        "test_package_count": sum(
            item["test_package_count"] for item in manifest["modules"]
        ),
        "no_test_package_count": sum(
            item["no_test_package_count"] for item in manifest["modules"]
        ),
        "quiet_evidence_sha256": quiet["sha256"],
    }
    return summary, 0


def load_manifest(path: Path) -> dict[str, Any]:
    manifest = load_json(path)
    if manifest.get("schema") != SCHEMA:
        raise ProfileError(f"manifest {path} is not {SCHEMA}")
    if not isinstance(manifest.get("modules"), list):
        raise ProfileError(f"manifest {path} has no module inventory")
    return manifest


def manifest_env(manifest: dict[str, Any], *, root: Path) -> dict[str, str]:
    flags = manifest.get("flags", {})
    cache = manifest.get("cache_paths", {})
    required = ("gomaxprocs", "go_test_p", "cgo_enabled")
    missing = [key for key in required if key not in flags]
    if missing:
        raise ProfileError("manifest is missing flags: " + ", ".join(missing))
    if str(flags["cgo_enabled"]) != "0":
        raise ProfileError("manifest must pin CGO_ENABLED=0")
    try:
        if int(flags["gomaxprocs"]) <= 0 or int(flags["go_test_p"]) <= 0:
            raise ValueError
    except (TypeError, ValueError) as exc:
        raise ProfileError("manifest must pin positive GOMAXPROCS and go test -p") from exc
    tags = flags.get("tags", [])
    if "nomicrophone" not in tags:
        raise ProfileError("manifest must pin the nomicrophone build tag")
    if flags.get("count") != 1:
        raise ProfileError("manifest must pin go test -count=1")
    cache_values = {
        "GOCACHE": cache.get("gocache"),
        "GOMODCACHE": cache.get("gomodcache"),
    }
    resolved_cache_values: dict[str, str] = {}
    for name, value in cache_values.items():
        if not isinstance(value, str) or not value or not Path(value).is_absolute():
            raise ProfileError(
                "manifest must pin absolute isolated GOCACHE and GOMODCACHE paths"
            )
        try:
            resolved_cache_values[name] = str(artifact_path(root, value))
        except ProfileError as exc:
            raise ProfileError(
                f"manifest {name} must be contained within manifest output root {root}"
            ) from exc
    return {
        "CGO_ENABLED": "0",
        "GOMAXPROCS": str(flags["gomaxprocs"]),
        "GOCACHE": resolved_cache_values["GOCACHE"],
        "GOMODCACHE": resolved_cache_values["GOMODCACHE"],
    }


def manifest_go(manifest: dict[str, Any]) -> str:
    go = manifest.get("go", {}).get("executable")
    if not isinstance(go, str) or not go:
        raise ProfileError("manifest has no Go executable")
    return go


def check_ready_manifest(manifest: dict[str, Any], *, require_warm: bool = False) -> None:
    if manifest.get("inventory_status") != "PASS":
        raise ProfileError(
            f"manifest inventory status is {manifest.get('inventory_status')!r}; "
            "run inventory successfully first"
        )
    if manifest.get("mode") != "hermetic":
        raise ProfileError("warm requires a hermetic inventory manifest")
    names = [module.get("name") for module in manifest["modules"]]
    expected = [name for name, _ in MODULES]
    if names != expected:
        raise ProfileError(
            "manifest module inventory is not the admitted six-module hermetic lane"
        )
    if require_warm and manifest.get("warm", {}).get("status") != "PASS":
        raise ProfileError(
            "manifest warm status is not PASS; complete the explicit warm phase "
            "before running hermetic tests"
        )


def warm(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    manifest_path = Path(args.manifest).resolve()
    manifest = load_manifest(manifest_path)
    check_ready_manifest(manifest)
    root = manifest_root(manifest_path)
    quiet = require_heavy(args, mode="hermetic", root=root)
    warm_root = root / "warm"
    env_overrides = manifest_env(manifest, root=root)
    go = manifest_go(manifest)
    commands_sink = manifest.setdefault("commands", [])
    if not isinstance(commands_sink, list):
        raise ProfileError("manifest commands must be a list")
    if any(
        isinstance(command, dict) and command.get("phase") == "warm"
        for command in commands_sink
    ):
        raise ProfileError("manifest already contains warm records; run inventory again")
    commands: list[dict[str, Any]] = []
    command_ids: list[str] = []
    binaries: list[dict[str, Any]] = []
    failures: list[str] = []
    started_at = utc_now()
    for module in manifest["modules"]:
        require_heavy(args, mode="hermetic", root=root)
        timeout = (
            manifest["flags"]["agent_cli_timeout_seconds"]
            if module["name"] == "agent-cli"
            else manifest["flags"]["general_timeout_seconds"]
        )
        record, _, _ = command_record(
            [go, "mod", "download"],
            cwd=Path(module["path"]),
            env_overrides=env_overrides,
            timeout_seconds=timeout,
            output_root=root,
            output_dir=warm_root / "commands" / "download",
            label=f"warm-download-{module['name']}",
            stdout_suffix="stdout.txt",
        )
        set_command_context(
            record,
            phase="warm",
            module=module["name"],
            role="go-mod-download",
            trial=1,
        )
        record["scope"] = "manifest"
        record["package"] = None
        commands.append(record)
        commands_sink.append(record)
        command_ids.append(record["record_id"])
        if record["status"] != "PASS":
            failures.append(f"{module['name']}: dependency download")
            break
    if not failures:
        for module in manifest["modules"]:
            timeout = (
                manifest["flags"]["agent_cli_timeout_seconds"]
                if module["name"] == "agent-cli"
                else manifest["flags"]["general_timeout_seconds"]
            )
            for package in module.get("packages", []):
                if not package.get("has_tests"):
                    binaries.append(
                        {
                            "module": module["name"],
                            "package": package["import_path"],
                            "status": "SKIPPED_NO_TEST_FILES",
                            "reason": "inventory has no TestGoFiles or XTestGoFiles",
                        }
                    )
                    continue
                require_heavy(args, mode="hermetic", root=root)
                binary_name = (
                    f"{safe_label(module['name'])}-"
                    f"{hashlib.sha256(package['import_path'].encode()).hexdigest()[:16]}.test"
                )
                binary_path = warm_root / "test-binaries" / binary_name
                record, _, _ = command_record(
                    [
                        go,
                        "test",
                        "-tags=nomicrophone",
                        "-run",
                        "^$",
                        "-count=1",
                        "-p",
                        str(manifest["flags"]["go_test_p"]),
                        "-timeout",
                        f"{timeout}s",
                        "-c",
                        "-o",
                        str(binary_path),
                        package["package_arg"],
                    ],
                    cwd=Path(module["path"]),
                    env_overrides=env_overrides,
                    timeout_seconds=timeout,
                    output_root=root,
                    output_dir=warm_root / "commands" / "compile" / safe_label(module["name"]),
                    label=f"warm-compile-{module['name']}-{package['import_path']}",
                    stdout_suffix="stdout.txt",
                )
                set_command_context(
                    record,
                    phase="warm",
                    module=module["name"],
                    role="go-test-binary-compile",
                    trial=1,
                    selected_packages=[package["import_path"]],
                )
                record["scope"] = "manifest"
                record["package"] = package["import_path"]
                record["binary_path"] = relative_path(binary_path, root)
                binary = {
                    "module": module["name"],
                    "package": package["import_path"],
                    "status": record["status"],
                    "command_id": record["record_id"],
                }
                if binary_path.is_file():
                    binary["path"] = relative_path(binary_path, root)
                    binary["sha256"] = sha256_file(binary_path)
                    binary["bytes"] = binary_path.stat().st_size
                binaries.append(binary)
                commands.append(record)
                commands_sink.append(record)
                command_ids.append(record["record_id"])
                if record["status"] != "PASS":
                    failures.append(
                        f"{module['name']}:{package['import_path']}: test binary compile"
                    )
                    break
            if failures:
                break
    status = "FAILED" if failures else "PASS"
    manifest["warm"] = {
        "status": status,
        "started_at_utc": started_at,
        "ended_at_utc": utc_now(),
        "quiet_evidence": quiet,
        "command_ids": command_ids,
        "test_binaries": binaries,
        "failures": failures,
    }
    manifest["measurement_status"] = "WARMED" if not failures else "WARM_FAILED"
    save_manifest(manifest_path, manifest)
    return {
        "status": status,
        "manifest": str(manifest_path),
        "dependency_download_seconds": sum(
            item["wall_seconds"]
            for item in commands
            if item.get("role") == "go-mod-download"
        ),
        "test_binary_compile_seconds": sum(
            item["wall_seconds"]
            for item in commands
            if item.get("role") == "go-test-binary-compile"
        ),
        "failure_count": len(failures),
    }, 0 if not failures else 1


def read_cohort(value: str) -> list[str]:
    candidate = Path(value)
    if candidate.is_file():
        try:
            text = candidate.read_text(encoding="utf-8")
        except OSError as exc:
            raise ProfileError(f"cannot read cohort file {candidate}: {exc}") from exc
        values = text.splitlines()
    else:
        values = value.split(",")
    result = []
    for item in values:
        token = item.strip()
        if token and not token.startswith("#"):
            result.append(token)
    if not result:
        raise ProfileError("cohort is empty")
    return list(dict.fromkeys(result))


def select_packages(
    manifest: dict[str, Any], cohort: str | None
) -> tuple[dict[str, list[dict[str, Any]]], bool, list[str]]:
    by_module: dict[str, list[dict[str, Any]]] = {}
    all_packages: dict[str, tuple[str, dict[str, Any]]] = {}
    for module in manifest["modules"]:
        by_module[module["name"]] = []
        for package in module.get("packages", []):
            all_packages[package["import_path"]] = (module["name"], package)
            all_packages[f"{module['name']}:{package['package_arg']}"] = (
                module["name"],
                package,
            )
    if cohort is None:
        for module in manifest["modules"]:
            by_module[module["name"]] = list(module.get("packages", []))
        return by_module, False, [
            package["import_path"]
            for module in manifest["modules"]
            for package in module.get("packages", [])
        ]
    selected = []
    for token in read_cohort(cohort):
        match = all_packages.get(token)
        if match is None:
            raise ProfileError(f"cohort package is not in inventory: {token}")
        module_name, package = match
        if package not in by_module[module_name]:
            by_module[module_name].append(package)
        selected.append(package["import_path"])
    for packages in by_module.values():
        packages.sort(key=lambda item: item["import_path"])
    if len(selected) > 5:
        raise ProfileError("cohort may contain at most five packages")
    return by_module, True, list(dict.fromkeys(selected))


def run_profile(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    manifest_path = Path(args.manifest).resolve()
    manifest = load_manifest(manifest_path)
    root = manifest_root(manifest_path)
    if manifest.get("mode") == "synthetic":
        quiet = require_heavy(args, mode="synthetic", root=root)
    else:
        check_ready_manifest(manifest, require_warm=True)
        quiet = require_heavy(args, mode="hermetic", root=root)
    if args.repeat > 3:
        raise ProfileError("repeat is capped at three invocations (one plus two repeats)")
    if args.cohort is None and args.repeat != 1:
        raise ProfileError("full inventory runs are single-shot; repeat only a bounded cohort")
    existing_commands = manifest.get("commands")
    if not isinstance(existing_commands, list):
        raise ProfileError("manifest has no canonical commands list")
    existing_run_records = [
        command
        for command in existing_commands
        if isinstance(command, dict) and command.get("phase") in RUN_PHASES
    ]
    if manifest.get("mode") != "synthetic":
        full_records = [
            command for command in existing_run_records if command.get("phase") == "full"
        ]
        if args.cohort is None and full_records:
            raise ProfileError(
                "manifest already contains the full inventory trial; "
                "do not retry unchanged full timing"
            )
        if args.cohort is not None and not full_records:
            raise ProfileError(
                "cohort timing requires the completed full inventory trial first"
            )
    runs_root = root / "runs"
    env_overrides = manifest_env(manifest, root=root)
    synthetic = manifest.get("mode") == "synthetic"
    go = None if synthetic else manifest_go(manifest)
    selected_by_module, cohort_mode, selected_packages = select_packages(
        manifest, args.cohort
    )
    group_id = uuid.uuid4().hex[:12]
    group_root = runs_root / group_id
    records: list[dict[str, Any]] = []
    commands_sink = existing_commands
    failures: list[str] = []
    repetitions_completed = 0
    for repeat_index in range(1, args.repeat + 1):
        repetition_failed = False
        for module in manifest["modules"]:
            packages = selected_by_module[module["name"]]
            if not packages:
                continue
            timeout = (
                manifest["flags"]["agent_cli_timeout_seconds"]
                if module["name"] == "agent-cli"
                else manifest["flags"]["general_timeout_seconds"]
            )
            if cohort_mode:
                package_args = [package["package_arg"] for package in packages]
            else:
                package_args = ["./..."]
            if synthetic:
                configured_command = module.get("command")
                if not isinstance(configured_command, list) or not configured_command:
                    raise ProfileError(
                        f"synthetic module {module['name']} has no command"
                    )
                argv = [str(item) for item in configured_command]
            else:
                test_args = [
                    *package_args,
                    "-json",
                    "-count=1",
                    "-tags=nomicrophone",
                    "-p",
                    str(manifest["flags"]["go_test_p"]),
                    "-timeout",
                    f"{timeout}s",
                ]
                if module["name"] == "agent-cli":
                    argv = [
                        go,
                        "run",
                        "./cmd/testtimeout",
                        "--timeout",
                        f"{timeout}s",
                        "--",
                        go,
                        "test",
                        *test_args,
                    ]
                else:
                    argv = [go, "test", *test_args]
            source_validation = validate_source_state(
                manifest,
                output_root=root,
                output_dir=group_root / f"r{repeat_index}" / safe_label(module["name"]),
                label=f"run-{group_id[:6]}-r{repeat_index}-{safe_label(module['name'])}",
                command_sink=commands_sink,
                module=module["name"],
                trial=repeat_index,
            )
            require_heavy(
                args,
                mode="synthetic" if manifest.get("mode") == "synthetic" else "hermetic",
                root=root,
            )
            record, _, _ = command_record(
                argv,
                cwd=Path(module["path"]),
                env_overrides=env_overrides,
                timeout_seconds=timeout,
                output_root=root,
                output_dir=group_root / f"r{repeat_index}" / safe_label(module["name"]),
                label=f"run-{group_id[:6]}-r{repeat_index}-{safe_label(module['name'])}",
                stdout_suffix="stdout.jsonl",
            )
            set_command_context(
                record,
                phase="cohort" if cohort_mode else "full",
                module=module["name"],
                role="synthetic-test" if synthetic else "go-test",
                trial=repeat_index,
                selected_packages=[package["import_path"] for package in packages],
            )
            record["scope"] = "run"
            record.update(
                {
                    "source_sha": (
                        source_validation["head"]
                        if source_validation is not None
                        else manifest.get("source_sha")
                    ),
                    "source_validation": source_validation,
                    "run_group_id": group_id,
                    "repeat_index": repeat_index,
                    "requested_repetitions": args.repeat,
                    "cohort": cohort_mode,
                    "expected_package_count": len(packages),
                    "test_result_cache_policy": "disabled-by--count=1",
                    "quiet_evidence": quiet,
                }
            )
            records.append(record)
            commands_sink.append(record)
            if record["status"] != "PASS":
                failures.append(f"{module['name']} repetition {repeat_index}")
                repetition_failed = True
                break
        if repetition_failed:
            break
        repetitions_completed += 1
    group_status = "FAILED" if failures else "CAPTURED"
    manifest["measurement_status"] = "RUN_CAPTURED"
    save_manifest(manifest_path, manifest)
    return {
        "status": group_status,
        "manifest": str(manifest_path),
        "run_group_id": group_id,
        "completed_repetitions": repetitions_completed,
        "requested_repetitions": args.repeat,
        "record_count": len(records),
        "failure_count": len(failures),
        "source_sha": manifest.get("source_sha"),
    }, 0 if not failures else 1


def parse_elapsed(value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError("Elapsed must be a finite JSON number")
    try:
        result = float(value)
    except (OverflowError, ValueError) as exc:
        raise ValueError("Elapsed must be a finite JSON number") from exc
    if not math.isfinite(result) or result < 0:
        raise ValueError("Elapsed must be a finite non-negative JSON number")
    if result * 1_000_000_000.0 > float(MAX_DURATION_NANOSECONDS):
        raise ValueError("Elapsed duration is too large for time.Duration")
    return result


def checked_duration_sum(values: Iterable[float], *, label: str) -> float:
    try:
        total = math.fsum(values)
    except (OverflowError, ValueError) as exc:
        raise ValueError(f"{label} is not a finite time.Duration") from exc
    if not math.isfinite(total) or total < 0:
        raise ValueError(f"{label} is not a finite non-negative time.Duration")
    if total > MAX_DURATION_SECONDS:
        raise ValueError(f"{label} exceeds time.Duration")
    return total


def command_record_schema_errors(record: Any, *, label: str) -> list[str]:
    if not isinstance(record, dict):
        return [f"{label} is not a JSON object"]
    errors: list[str] = []
    required = (
        "schema",
        "record_id",
        "label",
        "phase",
        "module",
        "role",
        "trial",
        "selected_packages",
        "argv",
        "cwd",
        "env_overrides",
        "timeout_seconds",
        "started_at_utc",
        "ended_at_utc",
        "monotonic_start_ns",
        "monotonic_end_ns",
        "wall_seconds",
        "timed_out",
        "exit_status",
        "signal",
        "spawn_error",
        "stdout_path",
        "stdout_sha256",
        "stdout_bytes",
        "stderr_path",
        "stderr_sha256",
        "stderr_bytes",
        "status",
    )
    for field in required:
        if field not in record:
            errors.append(f"{label} is missing required field {field}")
    if record.get("schema") != COMMAND_RECORD_SCHEMA:
        errors.append(
            f"{label} schema is {record.get('schema')!r}, want {COMMAND_RECORD_SCHEMA!r}"
        )
    if not isinstance(record.get("record_id"), str) or not record.get("record_id"):
        errors.append(f"{label} record_id must be a non-empty string")
    elif record.get("record_id") != record.get("label"):
        errors.append(f"{label} record_id must match label")
    if not isinstance(record.get("label"), str) or not record.get("label"):
        errors.append(f"{label} label must be a non-empty string")
    if record.get("phase") not in COMMAND_PHASES:
        errors.append(f"{label} phase is not recognized")
    if not isinstance(record.get("module"), str) or not record.get("module"):
        errors.append(f"{label} module must be a non-empty string")
    if record.get("role") not in COMMAND_ROLES:
        errors.append(f"{label} role is not recognized")
    trial = record.get("trial")
    if trial is not None and (
        isinstance(trial, bool) or not isinstance(trial, int) or trial < 1
    ):
        errors.append(f"{label} trial must be a positive integer or null")
    selected_packages = record.get("selected_packages")
    if not isinstance(selected_packages, list) or not all(
        isinstance(item, str) and item for item in selected_packages
    ):
        errors.append(f"{label} selected_packages must be a list of strings")
    elif len(set(selected_packages)) != len(selected_packages):
        errors.append(f"{label} selected_packages contains duplicates")
    argv = record.get("argv")
    if not isinstance(argv, list) or not argv or not all(
        isinstance(item, str) for item in argv
    ):
        errors.append(f"{label} argv must be a non-empty list of strings")
    if not isinstance(record.get("cwd"), str) or not record.get("cwd"):
        errors.append(f"{label} cwd must be a non-empty string")
    env_overrides = record.get("env_overrides")
    if not isinstance(env_overrides, dict) or not all(
        isinstance(key, str) and isinstance(value, str)
        for key, value in env_overrides.items()
    ):
        errors.append(f"{label} env_overrides must be a string map")
    try:
        timeout = record.get("timeout_seconds")
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)):
            raise ValueError("must be a finite JSON number")
        if parse_elapsed(timeout) <= 0:
            raise ValueError("must be positive")
    except ValueError as exc:
        errors.append(f"{label} timeout_seconds is invalid: {exc}")
    for field in ("started_at_utc", "ended_at_utc"):
        if parse_utc(record.get(field)) is None:
            errors.append(f"{label} {field} must be an ISO timestamp")
    if not isinstance(record.get("timed_out"), bool):
        errors.append(f"{label} timed_out must be a boolean")
    for field in ("exit_status", "signal"):
        value = record.get(field)
        if value is not None and (
            isinstance(value, bool) or not isinstance(value, int)
        ):
            errors.append(f"{label} {field} must be an integer or null")
    if record.get("spawn_error") is not None and not isinstance(
        record.get("spawn_error"), str
    ):
        errors.append(f"{label} spawn_error must be a string or null")
    for field in ("stdout_path", "stderr_path"):
        if not isinstance(record.get(field), str) or not record.get(field):
            errors.append(f"{label} {field} must be a non-empty string")
    for field in ("stdout_sha256", "stderr_sha256"):
        value = record.get(field)
        if not isinstance(value, str) or not re.fullmatch(r"[0-9a-fA-F]{64}", value):
            errors.append(f"{label} {field} must be a SHA-256 digest")
    for field in ("stdout_bytes", "stderr_bytes"):
        value = record.get(field)
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            errors.append(f"{label} {field} must be a non-negative integer")
    if record.get("status") not in {"PASS", "FAIL", "TIMEOUT", "SPAWN_ERROR"}:
        errors.append(f"{label} status is not a recognized command status")
    return errors


def command_record_artifact_errors(
    record: dict[str, Any], *, root: Path, label: str
) -> list[str]:
    errors: list[str] = []
    for stream in ("stdout", "stderr"):
        path_field = f"{stream}_path"
        hash_field = f"{stream}_sha256"
        bytes_field = f"{stream}_bytes"
        path_value = record.get(path_field)
        if not isinstance(path_value, str):
            continue
        try:
            path = artifact_path(root, path_value)
            raw = path.read_bytes()
        except (OSError, ProfileError) as exc:
            errors.append(f"{label} {stream} artifact is unreadable: {exc}")
            continue
        if sha256_bytes(raw) != record.get(hash_field):
            errors.append(f"{label} {stream}_sha256 does not match captured artifact")
        if len(raw) != record.get(bytes_field):
            errors.append(f"{label} {stream}_bytes does not match captured artifact")
    return errors


def validate_metadata_commands(
    value: Any, *, root: Path, label: str
) -> list[str]:
    if not isinstance(value, list) or not value:
        return [f"{label} metadata_commands must be a non-empty list"]
    errors: list[str] = []
    for index, command in enumerate(value):
        command_label = f"{label} metadata command {index}"
        errors.extend(command_record_schema_errors(command, label=command_label))
        if not isinstance(command, dict):
            continue
        errors.extend(command_timing_errors(command))
        errors.extend(command_record_artifact_errors(command, root=root, label=command_label))
        if command.get("status") != "PASS" or command.get("exit_status") != 0:
            errors.append(f"{command_label} did not complete successfully")
    return errors


def validate_metadata_outputs(
    value: Any,
    *,
    root: Path,
    expected_repo: Any,
    expected_head: Any,
    expected_dirty: Any,
    label: str,
) -> list[str]:
    errors = validate_metadata_commands(value, root=root, label=label)
    if errors or not isinstance(value, list):
        return errors
    if not isinstance(expected_repo, str) or not expected_repo:
        return [f"{label} has no expected source repository"]
    expected_repo = str(Path(expected_repo).resolve())
    head_commands = [
        command
        for command in value
        if isinstance(command, dict)
        and command.get("role") == "source-rev-parse"
    ]
    status_commands = [
        command
        for command in value
        if isinstance(command, dict)
        and command.get("role") == "source-status"
    ]
    if len(head_commands) != 1:
        errors.append(f"{label} must contain one source-rev-parse command")
    if len(status_commands) != 1:
        errors.append(f"{label} must contain one source-status command")
    if errors:
        return errors
    head_command = head_commands[0]
    status_command = status_commands[0]
    expected_commands = {
        "source-rev-parse": ["git", "-C", expected_repo, "rev-parse", "HEAD"],
        "source-status": ["git", "-C", expected_repo, "status", "--porcelain"],
    }
    for role, command in (
        ("source-rev-parse", head_command),
        ("source-status", status_command),
    ):
        if command.get("argv") != expected_commands[role]:
            errors.append(
                f"{label} {role} command argv does not match the expected Git invocation"
            )
        if command.get("cwd") != expected_repo:
            errors.append(f"{label} {role} command cwd does not match the source repository")
        if command.get("env_overrides") != {}:
            errors.append(
                f"{label} {role} command env_overrides must be empty for Git provenance"
            )
    if errors:
        return errors
    try:
        actual_head = decode_output(
            command_output(head_command, root=root, stream="stdout")
        ).strip()
        actual_dirty = parse_git_status_paths(
            command_output(status_command, root=root, stream="stdout")
        )
    except (OSError, ProfileError) as exc:
        return [f"{label} output cannot be read: {exc}"]
    if actual_head != expected_head:
        errors.append(f"{label} source-rev-parse output does not match captured head")
    if actual_dirty != expected_dirty:
        errors.append(f"{label} source-status output does not match captured dirty paths")
    return errors


def command_output(record: dict[str, Any], *, root: Path, stream: str) -> bytes:
    path = artifact_path(root, str(record[f"{stream}_path"]))
    return path.read_bytes()


def command_timing_errors(record: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    starts = record.get("monotonic_start_ns")
    ends = record.get("monotonic_end_ns")
    if isinstance(starts, bool) or not isinstance(starts, int) or starts < 0:
        errors.append("monotonic_start_ns must be a non-negative integer")
    elif starts > MAX_MONOTONIC_NANOSECONDS:
        errors.append("monotonic_start_ns exceeds signed 64-bit range")
    if isinstance(ends, bool) or not isinstance(ends, int) or ends < 0:
        errors.append("monotonic_end_ns must be a non-negative integer")
    elif ends > MAX_MONOTONIC_NANOSECONDS:
        errors.append("monotonic_end_ns exceeds signed 64-bit range")
    if (
        isinstance(starts, int)
        and not isinstance(starts, bool)
        and isinstance(ends, int)
        and not isinstance(ends, bool)
        and ends < starts
    ):
        errors.append("monotonic_end_ns must not precede monotonic_start_ns")
    started_at = parse_utc(record.get("started_at_utc"))
    ended_at = parse_utc(record.get("ended_at_utc"))
    if started_at is not None and ended_at is not None and ended_at < started_at:
        errors.append("ended_at_utc must not precede started_at_utc")
    try:
        parse_elapsed(record.get("wall_seconds"))
    except ValueError as exc:
        errors.append(f"wall_seconds is invalid: {exc}")
    return errors


def _module_index(manifest: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {
        module["name"]: module
        for module in manifest.get("modules", [])
        if isinstance(module, dict) and isinstance(module.get("name"), str)
    }


def _package_index(manifest: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {
        package["import_path"]: package
        for module in manifest.get("modules", [])
        if isinstance(module, dict)
        for package in module.get("packages", [])
        if isinstance(package, dict) and isinstance(package.get("import_path"), str)
    }


def expected_test_argv(
    manifest: dict[str, Any], *, module: dict[str, Any], record: dict[str, Any]
) -> list[str] | None:
    """Build the exact test invocation represented by a canonical record."""

    phase = record.get("phase")
    selected = record.get("selected_packages")
    if not isinstance(selected, list) or not all(isinstance(item, str) for item in selected):
        return None
    if phase == "full":
        package_args = ["./..."]
    elif phase == "cohort":
        package_index = _package_index(manifest)
        packages = [package_index.get(item) for item in selected]
        if any(package is None for package in packages):
            return None
        module_packages = [
            package
            for package in packages
            if isinstance(package, dict)
        ]
        package_args = [package["package_arg"] for package in module_packages]
    else:
        return None
    timeout = (
        manifest.get("flags", {}).get("agent_cli_timeout_seconds")
        if module.get("name") == "agent-cli"
        else manifest.get("flags", {}).get("general_timeout_seconds")
    )
    if not isinstance(timeout, int) or isinstance(timeout, bool) or timeout <= 0:
        return None
    go = manifest.get("go", {}).get("executable")
    if not isinstance(go, str) or not go:
        return None
    test_args = [
        *package_args,
        "-json",
        "-count=1",
        "-tags=nomicrophone",
        "-p",
        str(manifest.get("flags", {}).get("go_test_p")),
        "-timeout",
        f"{timeout}s",
    ]
    if module.get("name") == "agent-cli":
        return [
            go,
            "run",
            "./cmd/testtimeout",
            "--timeout",
            f"{timeout}s",
            "--",
            go,
            "test",
            *test_args,
        ]
    return [go, "test", *test_args]


def command_identity_errors(
    record: dict[str, Any], *, manifest: dict[str, Any], root: Path
) -> list[str]:
    """Validate the command's declared identity, not just a descriptive label."""

    errors: list[str] = []
    phase = record.get("phase")
    role = record.get("role")
    module_name = record.get("module")
    modules = _module_index(manifest)
    module = modules.get(module_name) if isinstance(module_name, str) else None
    env: dict[str, str] | None = None
    if (
        phase == "metadata"
        and role in {"source-rev-parse", "source-status"}
        and manifest.get("source_repo")
    ):
        source_repo = record.get("source_repo") or manifest.get("source_repo")
        if not isinstance(source_repo, str) or not source_repo:
            return [f"{record.get('label')}: source Git command has no repository"]
        source_repo = str(Path(source_repo).resolve())
        expected = (
            ["git", "-C", source_repo, "rev-parse", "HEAD"]
            if role == "source-rev-parse"
            else ["git", "-C", source_repo, "status", "--porcelain"]
        )
        if record.get("argv") != expected:
            errors.append(f"{record.get('label')}: {role} argv is not the expected Git invocation")
        if record.get("cwd") != source_repo:
            errors.append(f"{record.get('label')}: {role} cwd does not match the source repository")
        if record.get("env_overrides") != {}:
            errors.append(f"{record.get('label')}: {role} env_overrides must be empty")
        return errors
    if manifest.get("mode") == "synthetic":
        if phase in RUN_PHASES and isinstance(module, dict):
            configured = module.get("command")
            if isinstance(configured, list) and record.get("argv") != [str(item) for item in configured]:
                errors.append(f"{record.get('label')}: synthetic argv does not match module command")
        return errors

    try:
        env = manifest_env(manifest, root=root)
    except (ProfileError, TypeError, ValueError) as exc:
        return [f"manifest command environment is invalid: {exc}"]
    repo_value = manifest.get("repo")
    repo = str(Path(repo_value).resolve()) if isinstance(repo_value, str) and repo_value else None
    argv = record.get("argv")
    cwd = record.get("cwd")
    if phase == "metadata":
        if role in {"source-rev-parse", "source-status"}:
            source_repo = record.get("source_repo") or repo
            if not isinstance(source_repo, str) or not source_repo:
                return [f"{record.get('label')}: source Git command has no repository"]
            source_repo = str(Path(source_repo).resolve())
            expected = (
                ["git", "-C", source_repo, "rev-parse", "HEAD"]
                if role == "source-rev-parse"
                else ["git", "-C", source_repo, "status", "--porcelain"]
            )
            if argv != expected:
                errors.append(f"{record.get('label')}: {role} argv is not the expected Git invocation")
            if cwd != source_repo:
                errors.append(f"{record.get('label')}: {role} cwd does not match the source repository")
            if record.get("env_overrides") != {}:
                errors.append(f"{record.get('label')}: {role} env_overrides must be empty")
        elif role == "go-env":
            expected = [
                str(manifest.get("go", {}).get("executable")),
                "env",
                "-json",
                "GOOS",
                "GOARCH",
                "GOVERSION",
                "GOMODCACHE",
                "GOCACHE",
                "GOPATH",
                "GOWORK",
            ]
            if argv != expected:
                errors.append(f"{record.get('label')}: go env argv is not the expected invocation")
            if cwd != repo:
                errors.append(f"{record.get('label')}: go env cwd does not match the repository")
            if record.get("env_overrides") != env:
                errors.append(f"{record.get('label')}: go env environment overrides changed")
        elif role == "go-version":
            expected = [str(manifest.get("go", {}).get("executable")), "version"]
            if argv != expected:
                errors.append(f"{record.get('label')}: go version argv is not the expected invocation")
            if cwd != repo:
                errors.append(f"{record.get('label')}: go version cwd does not match the repository")
            if record.get("env_overrides") != env:
                errors.append(f"{record.get('label')}: go version environment overrides changed")
        else:
            errors.append(f"{record.get('label')}: unsupported metadata command role {role!r}")
        return errors
    if not isinstance(module, dict):
        return [f"{record.get('label')}: command module {module_name!r} is not in the inventory"]
    module_path = str(Path(str(module.get("path"))).resolve())
    if cwd != module_path:
        errors.append(f"{record.get('label')}: cwd does not match module inventory")
    if record.get("env_overrides") != env:
        errors.append(f"{record.get('label')}: environment overrides changed")
    if phase == "inventory" and role == "go-list":
        expected = [str(manifest.get("go", {}).get("executable")), "list", "-json", "-tags=nomicrophone", "./..."]
        if argv != expected:
            errors.append(f"{record.get('label')}: inventory argv is not the expected go list invocation")
    elif phase == "warm" and role == "go-mod-download":
        expected = [str(manifest.get("go", {}).get("executable")), "mod", "download"]
        if argv != expected:
            errors.append(f"{record.get('label')}: warm download argv is not the expected invocation")
    elif phase == "warm" and role == "go-test-binary-compile":
        package_index = _package_index(manifest)
        package_name = record.get("package")
        package = package_index.get(package_name) if isinstance(package_name, str) else None
        binary_value = record.get("binary_path")
        try:
            binary_path = artifact_path(root, str(binary_value))
        except ProfileError:
            binary_path = None
        if not isinstance(package, dict) or binary_path is None:
            errors.append(f"{record.get('label')}: warm compile package or binary path is invalid")
        else:
            timeout = (
                manifest["flags"]["agent_cli_timeout_seconds"]
                if module_name == "agent-cli"
                else manifest["flags"]["general_timeout_seconds"]
            )
            expected = [
                str(manifest["go"]["executable"]),
                "test",
                "-tags=nomicrophone",
                "-run",
                "^$",
                "-count=1",
                "-p",
                str(manifest["flags"]["go_test_p"]),
                "-timeout",
                f"{timeout}s",
                "-c",
                "-o",
                str(binary_path),
                package["package_arg"],
            ]
            if argv != expected:
                errors.append(f"{record.get('label')}: warm compile argv is not the expected invocation")
    elif phase in RUN_PHASES and role == "go-test":
        expected = expected_test_argv(manifest, module=module, record=record)
        if expected is None or argv != expected:
            errors.append(f"{record.get('label')}: test argv is not the expected invocation")
    else:
        errors.append(f"{record.get('label')}: phase/role combination is not supported")
    return errors


def command_context_errors(
    record: dict[str, Any], *, manifest: dict[str, Any]
) -> list[str]:
    """Validate phase ownership and context before records are aggregated."""

    phase = record.get("phase")
    role = record.get("role")
    module = record.get("module")
    scope = record.get("scope")
    trial = record.get("trial")
    selected = record.get("selected_packages")
    errors: list[str] = []

    def require(condition: bool, message: str) -> None:
        if not condition:
            errors.append(f"{record.get('label')}: {message}")

    require(isinstance(selected, list), "selected_packages must be a list")
    selected_values = selected if isinstance(selected, list) else []
    trial_is_positive = (
        isinstance(trial, int) and not isinstance(trial, bool) and trial > 0
    )
    source_roles = {"source-rev-parse", "source-status"}

    if manifest.get("mode") == "synthetic":
        if phase == "metadata" and manifest.get("source_repo"):
            require(role in source_roles, "synthetic source metadata has an unsupported role")
            require(module == "synthetic", "synthetic source metadata must use module synthetic")
            require(scope == "run", "synthetic source metadata must use run scope")
            require(trial_is_positive, "synthetic source metadata must have a positive trial")
            require(not selected_values, "source metadata must not select packages")
        elif phase in RUN_PHASES:
            require(role == "synthetic-test", "synthetic run records must use synthetic-test")
            require(module == "synthetic", "synthetic run records must use module synthetic")
            require(scope == "run", "synthetic run records must use run scope")
            require(trial_is_positive, "synthetic run records must have a positive trial")
            require(bool(selected_values), "synthetic run records must select packages")
        return errors

    expected_modules = {name for name, _ in MODULES}
    if phase == "metadata":
        if role in source_roles:
            require(scope in {"manifest", "run"}, "source metadata has an invalid scope")
            if scope == "manifest":
                require(module == "manifest", "manifest source metadata must use module manifest")
                require(trial == 1, "manifest source metadata must use trial 1")
            else:
                require(module in expected_modules, "run source metadata has an unknown module")
                require(trial_is_positive, "run source metadata must have a positive trial")
            require(not selected_values, "source metadata must not select packages")
        else:
            require(role in {"go-env", "go-version"}, "manifest metadata has an unsupported role")
            require(scope == "manifest", "manifest metadata must use manifest scope")
            require(module == "manifest", "manifest metadata must use module manifest")
            require(trial == 1, "manifest metadata must use trial 1")
            require(not selected_values, "manifest metadata must not select packages")
    elif phase == "inventory":
        require(role == "go-list", "inventory records must use go-list")
        require(module in expected_modules, "inventory record has an unknown module")
        require(scope == "manifest", "inventory records must use manifest scope")
        require(trial == 1, "inventory records must use trial 1")
        require(not selected_values, "inventory records must not select packages")
    elif phase == "warm":
        require(role in {"go-mod-download", "go-test-binary-compile"}, "warm record has an unsupported role")
        require(module in expected_modules, "warm record has an unknown module")
        require(scope == "manifest", "warm records must use manifest scope")
        require(trial == 1, "warm records must use trial 1")
        if role == "go-mod-download":
            require(not selected_values, "dependency download must not select packages")
            require(record.get("package") is None, "dependency download must not name a package")
        elif role == "go-test-binary-compile":
            package = record.get("package")
            require(isinstance(package, str) and package, "warm compile must name a package")
            require(selected_values == [package], "warm compile must select its package")
    elif phase in RUN_PHASES:
        require(role == "go-test", "hermetic run records must use go-test")
        require(module in expected_modules, "run record has an unknown module")
        require(scope == "run", "run records must use run scope")
        require(trial_is_positive, "run records must have a positive trial")
        require(bool(selected_values), "run records must select packages")
    return errors


def parse_timing_stream(raw: bytes, *, label: str) -> dict[str, Any]:
    lines = raw.splitlines()
    states: dict[str, dict[str, bool]] = {}
    observations: list[dict[str, Any]] = []
    package_events: dict[str, list[dict[str, Any]]] = {}
    test_counts: dict[str, int] = {}
    test_terminals: dict[str, int] = {}
    no_test_markers: set[str] = set()
    package_failures: list[str] = []
    test_failures: list[str] = []
    cached_markers: list[str] = []
    malformed: str | None = None
    active_tests_by_package: dict[str, set[str]] = {}
    subtest_overlap = False
    line_number = 0

    for raw_line in lines:
        line_number += 1
        line = raw_line.strip()
        if not line:
            continue
        try:
            event = json.loads(line.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            malformed = f"line {line_number}: malformed JSON: {exc}"
            break
        if not isinstance(event, dict):
            malformed = f"line {line_number}: event is not a JSON object"
            break
        action = event.get("Action", "")
        package = event.get("Package", "")
        test = event.get("Test", "")
        if not isinstance(action, str) or not isinstance(package, str) or not isinstance(test, str):
            malformed = f"line {line_number}: Action, Package and Test must be strings"
            break
        if "Elapsed" in event:
            try:
                parse_elapsed(event["Elapsed"])
            except ValueError as exc:
                malformed = f"line {line_number}: elapsed: {exc}"
                break
        output_text = event.get("Output", "")
        if isinstance(output_text, str):
            if "[no test files]" in output_text:
                no_test_markers.add(package)
            if "(cached)" in output_text.lower():
                cached_markers.append(f"line {line_number}: {package or '<none>'}")
        if package:
            package_events.setdefault(package, []).append(event)
        if test:
            test_counts[package] = test_counts.get(package, 0) + 1
            active_tests = active_tests_by_package.setdefault(package, set())
            if action in {"run", "cont", "start"}:
                if active_tests and test not in active_tests:
                    subtest_overlap = True
                active_tests.add(test)
            elif action == "pause":
                active_tests.discard(test)
            elif action in TEST_TERMINALS:
                test_terminals[package] = test_terminals.get(package, 0) + 1
                active_tests.discard(test)
                if action == "fail":
                    test_failures.append(f"{package}:{test}")
        if action == "fail":
            if test:
                if f"{package}:{test}" not in test_failures:
                    test_failures.append(f"{package}:{test}")
            elif package:
                package_failures.append(package)

        if not package:
            continue
        state = states.get(package, {"pending": False, "completed": False})
        if action == "start":
            if state["pending"]:
                malformed = f"line {line_number}: package {package!r} started twice before completion"
                break
            active_tests_by_package[package] = set()
            states[package] = {"pending": True, "completed": False}
            continue
        if package not in states:
            state["pending"] = True
        if action in PACKAGE_TERMINALS and not test:
            if "Elapsed" not in event:
                malformed = f"line {line_number}: package {package!r} terminal has no Elapsed"
                break
            try:
                elapsed = parse_elapsed(event["Elapsed"])
            except ValueError as exc:
                malformed = f"line {line_number}: elapsed: {exc}"
                break
            observations.append(
                {
                    "package": package,
                    "elapsed_seconds": elapsed,
                    "action": action,
                    "line": line_number,
                }
            )
            state["pending"] = False
            state["completed"] = True
            states[package] = state
            continue
        if not state["completed"]:
            state["pending"] = True
        states[package] = state

    pending = sorted(
        package for package, state in states.items() if state.get("pending")
    )
    if malformed is None and pending:
        malformed = f"package {pending[0]!r} did not produce a timed terminal record"
    if malformed is None and not observations:
        malformed = "timing input contained no package completions"
    return {
        "label": label,
        "stream_status": "INVALID" if malformed else "PASS",
        "error": malformed,
        "observations": observations,
        "terminal_packages": sorted({item["package"] for item in observations}),
        "package_event_count": sum(len(events) for events in package_events.values()),
        "package_event_packages": sorted(package_events),
        "test_event_count": sum(test_counts.values()),
        "test_terminal_count": sum(test_terminals.values()),
        "test_events_by_package": test_counts,
        "no_test_markers": sorted(item for item in no_test_markers if item),
        "package_failures": sorted(set(package_failures)),
        "test_failures": sorted(set(test_failures)),
        "cached_markers": cached_markers,
        "subtest_overlap": subtest_overlap,
    }


def parse_record_stream(
    record: dict[str, Any],
    *,
    root: Path,
    manifest: dict[str, Any],
    expected_source_sha: Any,
    require_source_validation: bool,
    commands_by_id: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    schema_error = record.get("_schema_error")
    if isinstance(schema_error, str) and schema_error:
        return {
            "record_id": record.get("label"),
            "record": record,
            "source_status": "INVALID",
            "raw_stdout_retained": False,
            "raw_stderr_retained": False,
            "timing_status": "INVALID",
            "timing_errors": [schema_error],
            "source_validation_errors": [],
            "stream_status": "INVALID",
            "error": schema_error,
            "execution_valid": False,
        }
    record_source_sha = record.get("source_sha")
    if not isinstance(record_source_sha, str) or not record_source_sha:
        source_status = "MISSING"
    elif not isinstance(expected_source_sha, str) or not expected_source_sha:
        source_status = "MANIFEST_MISSING"
    elif record_source_sha != expected_source_sha:
        source_status = "MISMATCH"
    else:
        source_status = "PASS"
    source_validation_errors: list[str] = []
    if require_source_validation:
        validation = record.get("source_validation")
        if not isinstance(validation, dict) or validation.get("matches_manifest") is not True:
            source_validation_errors.append(
                "source validation is missing or does not match the manifest"
            )
        else:
            expected_repo_value = manifest.get("source_repo") or manifest.get("repo")
            expected_repo = (
                str(Path(expected_repo_value).resolve())
                if isinstance(expected_repo_value, str) and expected_repo_value
                else None
            )
            if expected_repo is None:
                source_validation_errors.append(
                    "manifest has no source repository for validation"
                )
            elif validation.get("repo") != expected_repo:
                source_validation_errors.append(
                    "source validation repo does not match manifest source repository"
                )

            expected_manifest_sha = expected_source_sha
            validation_head = validation.get("head")
            if not isinstance(validation_head, str) or not re.fullmatch(
                r"[0-9a-fA-F]{7,64}", validation_head
            ):
                source_validation_errors.append(
                    "source validation head is not a valid commit SHA"
                )
            elif validation_head != record_source_sha:
                source_validation_errors.append(
                    "source validation head does not match recorded source_sha"
                )
            if validation.get("expected_head") != expected_manifest_sha:
                source_validation_errors.append(
                    "source validation expected_head does not match manifest source_sha"
                )

            expected_dirty_value = manifest.get("source_dirty_paths", [])
            if not isinstance(expected_dirty_value, list) or not all(
                isinstance(item, str) for item in expected_dirty_value
            ):
                source_validation_errors.append(
                    "manifest source_dirty_paths must be a list of strings"
                )
            elif expected_repo is not None:
                expected_dirty = source_paths_outside_output(
                    expected_dirty_value,
                    repo=Path(expected_repo),
                    output_root=root,
                )
                for field in ("dirty_paths", "expected_dirty_paths"):
                    value = validation.get(field)
                    if not isinstance(value, list) or not all(
                        isinstance(item, str) for item in value
                    ):
                        source_validation_errors.append(
                            f"source validation {field} must be a list of strings"
                        )
                    elif value != sorted(set(value)):
                        source_validation_errors.append(
                            f"source validation {field} is not normalized"
                        )
                    elif value != expected_dirty:
                        source_validation_errors.append(
                            f"source validation {field} does not match manifest source_dirty_paths"
                        )
                metadata_ids = validation.get("metadata_record_ids")
                if not isinstance(metadata_ids, list) or not all(
                    isinstance(item, str) for item in metadata_ids
                ):
                    source_validation_errors.append(
                        "source validation metadata_record_ids must be a list of strings"
                    )
                else:
                    metadata_records = [commands_by_id.get(item) for item in metadata_ids]
                    if any(record_value is None for record_value in metadata_records):
                        source_validation_errors.append(
                            "source validation references an unknown metadata record"
                        )
                    else:
                        source_validation_errors.extend(
                            validate_metadata_outputs(
                                metadata_records,
                                root=root,
                                expected_repo=validation.get("repo"),
                                expected_head=validation_head,
                                expected_dirty=validation.get("dirty_paths"),
                                label="source validation",
                            )
                        )
        if source_validation_errors:
            source_status = "UNVALIDATED"

    base = {
        "record_id": record.get("label"),
        "record": record,
        "source_status": source_status,
        "raw_stdout_retained": False,
        "raw_stderr_retained": False,
        "timing_status": "INVALID",
        "timing_errors": command_timing_errors(record),
        "record_schema_errors": command_record_schema_errors(
            record, label=f"run record {record.get('label')!r}"
        ),
        "artifact_errors": [],
        "source_validation_errors": source_validation_errors,
        "execution_valid": False,
    }
    base["artifact_errors"] = command_record_artifact_errors(
        record, root=root, label=f"run record {record.get('label')!r}"
    )
    base["timing_status"] = "PASS" if not base["timing_errors"] else "INVALID"
    if base["record_schema_errors"]:
        base.update(
            {
                "stream_status": "INVALID",
                "error": "; ".join(base["record_schema_errors"]),
            }
        )
        return base
    path_value = record.get("stdout_path")
    if not isinstance(path_value, str):
        base.update(
            {
                "stream_status": "INVALID",
                "error": "command record has no stdout_path",
            }
        )
        return base
    try:
        path = artifact_path(root, path_value)
    except ProfileError as exc:
        base.update({"stream_status": "INVALID", "error": str(exc)})
        return base
    try:
        raw = path.read_bytes()
    except OSError as exc:
        base.update(
            {"stream_status": "INVALID", "error": f"raw stdout missing: {exc}"}
        )
        return base

    parsed = parse_timing_stream(raw, label=str(record.get("label", path)))
    parsed.update(base)
    parsed["raw_stdout_path"] = str(path)
    parsed["raw_stdout_sha256"] = sha256_bytes(raw)
    parsed["raw_stdout_retained"] = True
    parsed["stdout_bytes_matches"] = len(raw) == record.get("stdout_bytes")
    parsed["stdout_hash_matches"] = (
        isinstance(record.get("stdout_sha256"), str)
        and record.get("stdout_sha256") == parsed["raw_stdout_sha256"]
    )
    stderr_hash_matches = False
    stderr_error = None
    stderr_path_value = record.get("stderr_path")
    if isinstance(stderr_path_value, str):
        try:
            stderr_path = artifact_path(root, stderr_path_value)
        except ProfileError as exc:
            stderr_error = str(exc)
        else:
            try:
                stderr_hash = sha256_file(stderr_path)
                parsed["raw_stderr_retained"] = True
                parsed["stderr_bytes_matches"] = (
                    len(stderr_path.read_bytes()) == record.get("stderr_bytes")
                )
                stderr_hash_matches = (
                    isinstance(record.get("stderr_sha256"), str)
                    and record.get("stderr_sha256") == stderr_hash
                )
            except OSError as exc:
                stderr_error = f"raw stderr missing: {exc}"
    else:
        stderr_error = "command record has no stderr_path"
    parsed["stderr_hash_matches"] = stderr_hash_matches
    parsed.setdefault("stderr_bytes_matches", False)
    parsed["stderr_error"] = stderr_error
    parsed["process_status"] = (
        "PASS"
        if record.get("status") == "PASS" and record.get("exit_status") == 0
        else "TIMEOUT"
        if record.get("timed_out")
        else "FAIL"
    )
    selected_value = record.get("selected_packages")
    expected = set(
        str(item) for item in selected_value
    ) if isinstance(selected_value, list) else set()
    observed = set(parsed.get("terminal_packages", []))
    parsed["expected_packages"] = sorted(expected)
    parsed["missing_packages"] = sorted(expected - observed)
    parsed["unexpected_packages"] = sorted(observed - expected)
    parsed["inventory_complete"] = (
        not parsed["missing_packages"] and not parsed["unexpected_packages"]
    )
    parsed["execution_valid"] = (
        parsed["stream_status"] == "PASS"
        and parsed["process_status"] == "PASS"
        and parsed["source_status"] == "PASS"
        and parsed["timing_status"] == "PASS"
        and not parsed["record_schema_errors"]
        and not parsed["artifact_errors"]
        and parsed["inventory_complete"]
        and not parsed["package_failures"]
        and not parsed["test_failures"]
        and not parsed["cached_markers"]
        and parsed["stdout_hash_matches"]
        and parsed["stdout_bytes_matches"]
        and parsed["stderr_hash_matches"]
        and parsed["stderr_bytes_matches"]
        and record.get("test_result_cache_policy") == "disabled-by--count=1"
    )
    if not parsed["inventory_complete"]:
        parsed["inventory_error"] = {
            "missing": parsed["missing_packages"],
            "unexpected": parsed["unexpected_packages"],
        }
    return parsed


def package_rank(parsed_records: Iterable[dict[str, Any]]) -> list[dict[str, Any]]:
    values: dict[str, list[float]] = {}
    failures: dict[str, set[str]] = {}
    for parsed in parsed_records:
        for observation in parsed.get("observations", []):
            package = observation["package"]
            values.setdefault(package, []).append(observation["elapsed_seconds"])
        for package in parsed.get("package_failures", []):
            failures.setdefault(package, set()).add("package-fail")
        for package in parsed.get("test_failures", []):
            failures.setdefault(package.split(":", 1)[0], set()).add("test-fail")
    ranked = []
    for package, durations in values.items():
        duration_total = checked_duration_sum(
            durations, label=f"package {package!r} duration total"
        )
        ranked.append(
            {
                "package": package,
                "observations": len(durations),
                "durations_seconds": durations,
                "min_seconds": min(durations),
                "max_seconds": max(durations),
                "spread_seconds": max(durations) - min(durations),
                "mean_seconds": duration_total / len(durations),
                "failure_kinds": sorted(failures.get(package, set())),
            }
        )
    ranked.sort(
        key=lambda item: (-item["max_seconds"], -item["mean_seconds"], item["package"])
    )
    return ranked


def lane_times(records: list[dict[str, Any]]) -> dict[str, Any]:
    starts = [
        int(record["monotonic_start_ns"])
        for record in records
        if isinstance(record.get("monotonic_start_ns"), int)
        and not isinstance(record.get("monotonic_start_ns"), bool)
        and 0 <= record["monotonic_start_ns"] <= MAX_MONOTONIC_NANOSECONDS
    ]
    ends = [
        int(record["monotonic_end_ns"])
        for record in records
        if isinstance(record.get("monotonic_end_ns"), int)
        and not isinstance(record.get("monotonic_end_ns"), bool)
        and 0 <= record["monotonic_end_ns"] <= MAX_MONOTONIC_NANOSECONDS
    ]
    durations = []
    for record in records:
        try:
            durations.append(parse_elapsed(record.get("wall_seconds")))
        except ValueError:
            continue
    lane_wall = None
    if starts and ends:
        lane_wall = (max(ends) - min(starts)) / 1_000_000_000.0
    return {
        "lane_wall_seconds": lane_wall,
        "sum_invocation_wall_seconds": checked_duration_sum(
            durations, label="invocation duration total"
        ),
        "invocation_count": len(records),
        "definition": (
            "max(monotonic_end)-min(monotonic_start); the sum is retained only "
            "as a diagnostic and is never reported as lane wall"
        ),
    }


def validate_recorded_quiet_evidence(
    value: Any, *, root: Path, mode: str
) -> dict[str, Any]:
    if not isinstance(value, dict):
        return {"status": "INVALID", "error": "run group has no quiet evidence"}
    path_value = value.get("path")
    expected_sha = value.get("sha256")
    if not isinstance(path_value, str) or not path_value:
        return {"status": "INVALID", "error": "quiet evidence has no path"}
    if not isinstance(expected_sha, str) or not expected_sha:
        return {"status": "INVALID", "error": "quiet evidence has no SHA-256"}
    try:
        path = artifact_path(root, path_value)
        evidence = load_json(path)
        actual_sha = sha256_file(path)
    except ProfileError as exc:
        return {
            "status": "INVALID",
            "path": path_value,
            "error": str(exc),
        }
    except OSError as exc:
        return {
            "status": "INVALID",
            "path": path_value,
            "error": f"quiet evidence cannot be read: {exc}",
        }
    if actual_sha != expected_sha:
        return {
            "status": "INVALID",
            "path": str(path),
            "error": "quiet evidence SHA-256 does not match the captured provenance",
        }
    error = quiet_evidence_contract_error(evidence, mode=mode)
    if error:
        return {"status": "INVALID", "path": str(path), "error": error}
    if value.get("path") != str(path):
        return {
            "status": "INVALID",
            "path": str(path),
            "error": "quiet evidence path changed after capture",
        }
    if value.get("isolation") != evidence.get("isolation"):
        return {
            "status": "INVALID",
            "path": str(path),
            "error": "quiet evidence isolation changed after capture",
        }
    for key in ("captured_at_utc", "valid_until_utc", "runner"):
        if value.get(key) != evidence.get(key):
            return {
                "status": "INVALID",
                "path": str(path),
                "error": f"quiet evidence {key} changed after capture",
            }
    before = evidence.get("before")
    if value.get("active_work") != (
        before.get("active_work") if isinstance(before, dict) else None
    ):
        return {
            "status": "INVALID",
            "path": str(path),
            "error": "quiet evidence active-work provenance changed after capture",
        }
    return {
        "status": "PASS",
        "path": str(path),
        "sha256": actual_sha,
        "isolation": evidence.get("isolation"),
    }


def validate_repetition_group(
    group: dict[str, Any],
    records: list[dict[str, Any]],
    manifest: dict[str, Any],
) -> dict[str, Any]:
    errors: list[str] = []
    group_id = group.get("run_group_id")
    if not isinstance(group_id, str) or not group_id:
        errors.append("run group has no run_group_id")
        group_id = "<missing>"
    requested = group.get("requested_repetitions")
    completed = group.get("completed_repetitions")
    if (
        isinstance(requested, bool)
        or not isinstance(requested, int)
        or requested < 1
        or requested > 3
    ):
        errors.append("requested_repetitions is not an integer in the range 1..3")
        requested_count = 0
    else:
        requested_count = requested
    if isinstance(completed, bool) or not isinstance(completed, int) or completed < 0:
        errors.append("completed_repetitions is not a non-negative integer")
        completed_count = -1
    else:
        completed_count = completed
    if requested_count and completed_count != requested_count:
        errors.append(
            "completed_repetitions does not equal requested_repetitions; "
            "incomplete repeats cannot produce fresh timing PASS"
        )
    if group.get("status") != "CAPTURED":
        errors.append(f"run group status is {group.get('status')!r}")

    for duplicate_field in ("records", "source_validations"):
        if duplicate_field in group:
            errors.append(
                f"run group contains duplicate {duplicate_field}; "
                "derive it from the canonical commands array"
            )
    package_to_module: dict[str, str] = {}
    for module in manifest.get("modules", []):
        if not isinstance(module, dict) or not isinstance(module.get("name"), str):
            continue
        for package in module.get("packages", []):
            if isinstance(package, dict) and isinstance(package.get("import_path"), str):
                package_to_module[package["import_path"]] = module["name"]
    selected_value = group.get("selected_packages")
    if not isinstance(selected_value, list) or not selected_value:
        errors.append("run group has no selected package inventory")
        selected_packages: list[str] = []
    else:
        selected_packages = [str(item) for item in selected_value]
        if len(set(selected_packages)) != len(selected_packages):
            errors.append("run group selected package inventory contains duplicates")
    selected_by_module: dict[str, set[str]] = {}
    for package in selected_packages:
        module_name = package_to_module.get(package)
        if module_name is None:
            errors.append(f"selected package {package!r} is not in the manifest inventory")
            continue
        selected_by_module.setdefault(module_name, set()).add(package)
    expected_modules = set(selected_by_module)
    if not expected_modules:
        errors.append("run group has no module with selected packages")

    records_by_repeat: dict[int, list[dict[str, Any]]] = {}
    for record in records:
        if record.get("run_group_id") != group_id:
            errors.append(f"record {record.get('label')!r} has the wrong run_group_id")
        module_name = record.get("module")
        repeat_index = record.get("repeat_index")
        if module_name not in expected_modules:
            errors.append(f"record {record.get('label')!r} has an unexpected module")
        if (
            isinstance(repeat_index, bool)
            or not isinstance(repeat_index, int)
            or repeat_index < 1
            or (requested_count and repeat_index > requested_count)
        ):
            errors.append(f"record {record.get('label')!r} has an invalid repeat_index")
            continue
        record_packages = record.get("selected_packages")
        if not isinstance(record_packages, list):
            errors.append(f"record {record.get('label')!r} has no selected packages")
        elif set(str(item) for item in record_packages) != selected_by_module.get(
            module_name, set()
        ):
            errors.append(
                f"record {record.get('label')!r} selected package scope changed"
            )
        records_by_repeat.setdefault(repeat_index, []).append(record)

    if requested_count:
        for repeat_index in range(1, requested_count + 1):
            repeat_records = records_by_repeat.get(repeat_index, [])
            repeat_modules = {record.get("module") for record in repeat_records}
            if repeat_modules != expected_modules or len(repeat_records) != len(expected_modules):
                errors.append(
                    f"repeat {repeat_index} has {len(repeat_records)} module records; "
                    f"expected {len(expected_modules)}"
                )

    return {
        "status": "PASS" if not errors else "INVALID",
        "run_group_id": group_id,
        "requested_repetitions": requested,
        "completed_repetitions": completed,
        "expected_records": (
            requested_count * len(expected_modules) if requested_count else None
        ),
        "actual_records": len(records),
        "errors": errors,
    }


def derive_run_groups(run_records: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Derive repetition summaries from the canonical command stream."""

    grouped: dict[str, list[dict[str, Any]]] = {}
    for record in run_records:
        group_id = record.get("run_group_id")
        if isinstance(group_id, str) and group_id:
            grouped.setdefault(group_id, []).append(record)
    groups: list[dict[str, Any]] = []
    for group_id, records in grouped.items():
        requested_values = {
            record.get("requested_repetitions")
            for record in records
            if isinstance(record.get("requested_repetitions"), int)
            and not isinstance(record.get("requested_repetitions"), bool)
        }
        cohort_values = {
            record.get("cohort")
            for record in records
            if isinstance(record.get("cohort"), bool)
        }
        quiet_values = [record.get("quiet_evidence") for record in records]
        first_quiet = quiet_values[0] if quiet_values else None
        quiet_consistent = bool(quiet_values) and all(
            value == first_quiet for value in quiet_values
        )
        repeat_values = {
            record.get("repeat_index")
            for record in records
            if isinstance(record.get("repeat_index"), int)
            and not isinstance(record.get("repeat_index"), bool)
        }
        groups.append(
            {
                "schema": "c11-run-group-derived-v1",
                "run_group_id": group_id,
                "source_sha": records[0].get("source_sha"),
                "created_at_utc": records[0].get("started_at_utc"),
                "cohort": next(iter(cohort_values)) if len(cohort_values) == 1 else None,
                "selected_packages": sorted(
                    {
                        package
                        for record in records
                        for package in record.get("selected_packages", [])
                        if isinstance(package, str)
                    }
                ),
                "requested_repetitions": (
                    next(iter(requested_values)) if len(requested_values) == 1 else None
                ),
                "completed_repetitions": len(repeat_values),
                "quiet_evidence": first_quiet,
                "quiet_evidence_consistent": quiet_consistent,
                "status": (
                    "CAPTURED"
                    if records and all(record.get("status") == "PASS" for record in records)
                    else "FAILED"
                ),
                "failures": [
                    f"{record.get('module')} repetition {record.get('repeat_index')}"
                    for record in records
                    if record.get("status") != "PASS"
                ],
            }
        )
    groups.sort(key=lambda item: str(item.get("run_group_id")))
    return groups


def validate_capture(
    manifest: dict[str, Any], *, root: Path
) -> dict[str, Any]:
    """Validate every manifest record before any group or display filtering."""

    errors: list[str] = []
    for legacy in LEGACY_COMMAND_COLLECTIONS:
        if legacy in manifest:
            errors.append(
                f"manifest contains legacy duplicate command collection {legacy}; "
                "use the canonical commands array"
            )
    commands_value = manifest.get("commands")
    if not isinstance(commands_value, list):
        return {
            "errors": ["manifest commands must be a list"],
            "commands": [],
            "commands_by_id": {},
            "run_records": [],
            "run_groups": [],
            "metadata_errors": [],
        }
    warm_value = manifest.get("warm")
    if isinstance(warm_value, dict) and "commands" in warm_value:
        errors.append(
            "manifest warm contains duplicate command records; use the canonical commands array"
        )
    commands_by_id: dict[str, dict[str, Any]] = {}
    command_indexes: dict[str, int] = {}
    phase_records: dict[str, list[dict[str, Any]]] = {
        phase: [] for phase in COMMAND_PHASES
    }
    for index, command in enumerate(commands_value):
        label = f"command {index}"
        errors.extend(command_record_schema_errors(command, label=label))
        if not isinstance(command, dict):
            continue
        errors.extend(command_timing_errors(command))
        errors.extend(command_record_artifact_errors(command, root=root, label=label))
        errors.extend(command_context_errors(command, manifest=manifest))
        errors.extend(command_identity_errors(command, manifest=manifest, root=root))
        record_id = command.get("record_id")
        if isinstance(record_id, str) and record_id:
            if record_id in commands_by_id:
                errors.append(f"command record_id {record_id!r} is duplicated")
            else:
                commands_by_id[record_id] = command
                command_indexes[record_id] = index
        phase = command.get("phase")
        if phase in phase_records:
            phase_records[phase].append(command)

    mode = manifest.get("mode")
    metadata_records = phase_records["metadata"]
    inventory_records = phase_records["inventory"]
    warm_records = phase_records["warm"]
    run_records = [
        command
        for phase in ("full", "cohort")
        for command in phase_records[phase]
    ]

    if mode == "synthetic":
        if inventory_records or warm_records:
            errors.append("synthetic captures must not contain inventory or warm commands")
        if metadata_records and not manifest.get("source_repo"):
            errors.append(
                "synthetic captures without source_repo must not contain metadata commands"
            )
    else:
        expected_modules = [name for name, _ in MODULES]
        actual_modules = [
            module.get("name")
            for module in manifest.get("modules", [])
            if isinstance(module, dict)
        ]
        if actual_modules != expected_modules:
            errors.append("manifest module inventory is not the admitted six-module hermetic lane")
        if manifest.get("inventory_status") != "PASS":
            errors.append("manifest inventory_status must be PASS before analysis")
        manifest_metadata_records = [
            record for record in metadata_records if record.get("scope") == "manifest"
        ]
        metadata_ids = manifest.get("metadata_command_ids")
        actual_metadata_ids = [
            record.get("record_id") for record in manifest_metadata_records
        ]
        if metadata_ids != actual_metadata_ids:
            errors.append("manifest metadata_command_ids do not match canonical metadata records")
        expected_metadata_context = [
            ("source-rev-parse", "manifest"),
            ("source-status", "manifest"),
            ("go-env", "manifest"),
            ("go-version", "manifest"),
        ]
        actual_metadata_context = [
            (record.get("role"), record.get("scope"))
            for record in manifest_metadata_records
        ]
        if actual_metadata_context != expected_metadata_context:
            errors.append(
                "manifest metadata records must be source Git, go env, and go version in order"
            )
        inventory_ids = manifest.get("inventory_command_ids")
        actual_inventory_ids = [record.get("record_id") for record in inventory_records]
        if inventory_ids != actual_inventory_ids:
            errors.append("manifest inventory_command_ids do not match canonical inventory records")
        actual_inventory_context = [
            (record.get("module"), record.get("role"), record.get("scope"))
            for record in inventory_records
        ]
        expected_inventory_context = [
            (module_name, "go-list", "manifest") for module_name in expected_modules
        ]
        if actual_inventory_context != expected_inventory_context:
            errors.append(
                "manifest inventory records must contain one ordered go-list command per module"
            )
        errors.extend(
            validate_metadata_outputs(
                manifest_metadata_records,
                root=root,
                expected_repo=manifest.get("repo"),
                expected_head=manifest.get("source_sha"),
                expected_dirty=manifest.get("source_dirty_paths"),
                label="manifest",
            )
        )
        if len(inventory_records) != len(expected_modules):
            errors.append(
                f"manifest must contain one inventory command per module; found {len(inventory_records)}"
            )
        warm = manifest.get("warm")
        if not isinstance(warm, dict):
            errors.append("manifest warm summary must be an object")
        else:
            if warm.get("status") != "PASS":
                errors.append("manifest warm status must be PASS before analysis")
            warm_ids = warm.get("command_ids")
            actual_warm_ids = [record.get("record_id") for record in warm_records]
            if warm_ids != actual_warm_ids:
                errors.append("manifest warm command_ids do not match canonical warm records")
            expected_warm_context = [
                (module_name, "go-mod-download", None)
                for module_name in expected_modules
            ] + [
                (module["name"], "go-test-binary-compile", package["import_path"])
                for module in manifest.get("modules", [])
                if isinstance(module, dict)
                for package in module.get("packages", [])
                if isinstance(package, dict) and package.get("has_tests")
            ]
            actual_warm_context = [
                (record.get("module"), record.get("role"), record.get("package"))
                for record in warm_records
            ]
            if actual_warm_context != expected_warm_context:
                errors.append(
                    "manifest warm records must contain ordered downloads and test-binary compiles"
                )
            binaries = warm.get("test_binaries", [])
            if not isinstance(binaries, list):
                errors.append("manifest warm test_binaries must be a list")
                binaries = []
            expected_binaries = [
                (module["name"], package["import_path"], bool(package.get("has_tests")))
                for module in manifest.get("modules", [])
                if isinstance(module, dict)
                for package in module.get("packages", [])
                if isinstance(package, dict)
            ]
            actual_binaries = [
                (binary.get("module"), binary.get("package"), binary.get("status") != "SKIPPED_NO_TEST_FILES")
                for binary in binaries
                if isinstance(binary, dict)
            ]
            if actual_binaries != expected_binaries:
                errors.append("manifest warm test_binaries do not match the package inventory")
            for binary in binaries:
                if not isinstance(binary, dict):
                    errors.append("manifest warm test_binaries contains a non-object")
                    continue
                package_index = _package_index(manifest)
                package = package_index.get(binary.get("package"))
                if not isinstance(package, dict):
                    errors.append("manifest warm test binary names an unknown package")
                    continue
                if not package.get("has_tests"):
                    if binary.get("status") != "SKIPPED_NO_TEST_FILES":
                        errors.append("warm no-test package must be explicitly skipped")
                    if "command_id" in binary:
                        errors.append("warm no-test package must not reference a compile command")
                    continue
                if "command_record" in binary:
                    errors.append("manifest warm test_binaries contains a duplicate command record")
                command_id = binary.get("command_id")
                command = commands_by_id.get(command_id)
                if not isinstance(command, dict):
                    errors.append("manifest warm test binary references an unknown command")
                    continue
                if command.get("role") != "go-test-binary-compile" or command.get("package") != binary.get("package"):
                    errors.append("manifest warm test binary references the wrong compile command")
                if binary.get("status") != command.get("status"):
                    errors.append("manifest warm test binary status does not match its command")
                if warm.get("status") == "PASS" and not all(
                    field in binary for field in ("path", "sha256", "bytes")
                ):
                    errors.append("successful warm compile must retain its test binary artifact")

        full_records = phase_records["full"]
        cohort_records = phase_records["cohort"]
        full_groups = derive_run_groups(full_records)
        cohort_groups = derive_run_groups(cohort_records)
        if len(full_groups) != 1:
            errors.append("hermetic schedule requires exactly one full inventory group")
        elif full_groups[0].get("requested_repetitions") != 1:
            errors.append("full inventory group must request exactly one repetition")
        if not cohort_groups:
            errors.append("hermetic schedule requires a selected slow cohort after the full trial")
        elif len(cohort_groups) != 1:
            errors.append("hermetic schedule permits exactly one selected cohort group")
        elif cohort_groups[0].get("requested_repetitions") != 2:
            errors.append("selected cohort group must request two repetitions")
        if full_records and cohort_records:
            last_full_index = max(
                command_indexes.get(record.get("record_id"), -1)
                for record in full_records
            )
            first_cohort_index = min(
                command_indexes.get(record.get("record_id"), 1 << 30)
                for record in cohort_records
            )
            if first_cohort_index <= last_full_index:
                errors.append("cohort commands must follow the full inventory commands")

    for record in run_records:
        phase = record.get("phase")
        required = (
            "source_sha",
            "source_validation",
            "run_group_id",
            "repeat_index",
            "requested_repetitions",
            "cohort",
            "expected_package_count",
            "test_result_cache_policy",
            "quiet_evidence",
        )
        for field in required:
            if field not in record:
                errors.append(f"run record {record.get('record_id')!r} is missing {field}")
        if record.get("phase") == "full" and record.get("cohort") is not False:
            errors.append(f"run record {record.get('record_id')!r} full phase must set cohort=false")
        if record.get("phase") == "cohort" and record.get("cohort") is not True:
            errors.append(f"run record {record.get('record_id')!r} cohort phase must set cohort=true")
        expected_count = record.get("expected_package_count")
        selected = record.get("selected_packages")
        if (
            isinstance(expected_count, int)
            and not isinstance(expected_count, bool)
            and isinstance(selected, list)
            and expected_count != len(selected)
        ):
            errors.append(f"run record {record.get('record_id')!r} package count does not match selection")
        requested = record.get("requested_repetitions")
        if (
            isinstance(requested, bool)
            or not isinstance(requested, int)
            or requested < 1
            or requested > 3
        ):
            errors.append(f"run record {record.get('record_id')!r} has invalid requested_repetitions")
        repeat_index = record.get("repeat_index")
        if repeat_index != record.get("trial"):
            errors.append(f"run record {record.get('record_id')!r} trial does not match repeat_index")
        if mode != "synthetic":
            validation = record.get("source_validation")
            if not isinstance(validation, dict):
                errors.append(f"run record {record.get('record_id')!r} has no source validation object")
            else:
                metadata_ids = validation.get("metadata_record_ids")
                if not isinstance(metadata_ids, list) or len(metadata_ids) != 2:
                    errors.append(
                        f"run record {record.get('record_id')!r} source validation must reference two metadata records"
                    )
                else:
                    metadata = [commands_by_id.get(item) for item in metadata_ids]
                    if any(item is None for item in metadata):
                        errors.append(f"run record {record.get('record_id')!r} references unknown source metadata")
                    else:
                        expected_roles = ["source-rev-parse", "source-status"]
                        actual_roles = [item.get("role") for item in metadata]
                        if actual_roles != expected_roles:
                            errors.append(
                                f"run record {record.get('record_id')!r} source metadata roles are not Git HEAD/status"
                            )
                        for item in metadata:
                            if item.get("phase") != "metadata":
                                errors.append(
                                    f"run record {record.get('record_id')!r} source metadata is not a metadata-phase command"
                                )
                            if item.get("scope") != "run":
                                errors.append(
                                    f"run record {record.get('record_id')!r} source metadata is not run-scoped"
                                )
                            if item.get("module") != record.get("module"):
                                errors.append(
                                    f"run record {record.get('record_id')!r} source metadata module does not match the run"
                                )
                            if item.get("trial") != record.get("trial"):
                                errors.append(
                                    f"run record {record.get('record_id')!r} source metadata trial does not match the run"
                                )
                        errors.extend(
                            validate_metadata_outputs(
                                metadata,
                                root=root,
                                expected_repo=validation.get("repo"),
                                expected_head=validation.get("head"),
                                expected_dirty=validation.get("dirty_paths"),
                                label=f"run record {record.get('record_id')!r} source validation",
                            )
                        )

    run_groups = derive_run_groups(run_records)
    for group in run_groups:
        if not group.get("quiet_evidence_consistent"):
            errors.append(
                f"run group {group.get('run_group_id')!r} has inconsistent quiet evidence references"
            )
    return {
        "errors": errors,
        "commands": [command for command in commands_value if isinstance(command, dict)],
        "commands_by_id": commands_by_id,
        "command_indexes": command_indexes,
        "run_records": run_records,
        "run_groups": run_groups,
        "metadata_errors": errors,
    }


def invalid_analysis(
    output: Path, *, manifest_path: Path, error: Exception
) -> tuple[dict[str, Any], int]:
    output.mkdir(parents=True, exist_ok=True)
    artifact = output / "analysis.json"
    analysis = {
        "schema": ANALYSIS_SCHEMA,
        "status": "INVALID",
        "fresh_timing": "INVALID",
        "manifest": str(manifest_path),
        "reason": f"{type(error).__name__}: {error}",
        "provenance": {
            "analysis_input_valid": False,
            "invalid_artifact_written_atomically": True,
        },
    }
    write_json_atomic(artifact, analysis)
    return {
        "status": "INVALID",
        "fresh_timing": "INVALID",
        "analysis": str(artifact),
        "error": str(error),
    }, 1


def analyze(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    manifest_path = Path(args.manifest).resolve()
    manifest = load_manifest(manifest_path)
    root = manifest_root(manifest_path)
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    if (
        manifest.get("fresh_timing_status") == "BLOCKED"
        and "commands" not in manifest
    ):
        analysis = {
            "schema": ANALYSIS_SCHEMA,
            "status": "BLOCKED",
            "fresh_timing": "BLOCKED",
            "source_sha": manifest.get("source_sha"),
            "reason": manifest.get("fresh_timing_reason"),
            "ci_evidence": manifest.get("ci_evidence", []),
            "inventory": {
                "expected_packages": sum(
                    len(module.get("packages", []))
                    for module in manifest.get("modules", [])
                    if isinstance(module, dict)
                ),
                "observed_packages": None,
                "coverage": "UNKNOWN",
            },
            "lane": {
                "lane_wall_seconds": None,
                "sum_invocation_wall_seconds": None,
                "definition": "No fresh run was authorized.",
            },
            "failure_references": manifest.get("failure_references", []),
            "provenance": manifest.get("provenance", {}),
        }
        write_json_atomic(output / "analysis.json", analysis)
        return {
            "status": "BLOCKED",
            "analysis": str(output / "analysis.json"),
            "source_sha": manifest.get("source_sha"),
        }, 0

    capture = validate_capture(manifest, root=root)
    capture_errors = list(capture.get("errors", []))
    all_records = list(capture.get("run_records", []))
    selected_record_indices = [
        index
        for index, record in enumerate(all_records)
        if args.group is None or record.get("run_group_id") == args.group
    ]
    records_for_analysis = [all_records[index] for index in selected_record_indices]
    selection_error = None
    if not all_records:
        if capture_errors:
            return invalid_analysis(
                output,
                manifest_path=manifest_path,
                error=ProfileError("; ".join(capture_errors)),
            )
        raise ProfileError("manifest contains no captured runs and is not marked BLOCKED")
    if not records_for_analysis:
        selection_error = f"no captured runs match --group {args.group!r}"

    require_source_validation = (
        manifest.get("mode") != "synthetic" or "source_repo" in manifest
    )
    parsed_all_records = [
        parse_record_stream(
            record,
            root=root,
            manifest=manifest,
            expected_source_sha=manifest.get("source_sha"),
            require_source_validation=require_source_validation,
            commands_by_id=capture["commands_by_id"],
        )
        for record in all_records
    ]
    parsed_records = [
        parsed_all_records[index] for index in selected_record_indices
    ]
    inventory_test_flags: dict[str, bool] = {}
    for module in manifest.get("modules", []):
        if not isinstance(module, dict):
            continue
        for package in module.get("packages", []):
            if not isinstance(package, dict):
                continue
            import_path = package.get("import_path")
            has_tests = package.get("has_tests")
            if isinstance(import_path, str) and isinstance(has_tests, bool):
                inventory_test_flags[import_path] = has_tests
    for parsed in parsed_all_records:
        no_test_errors = [
            (
                f"no-test marker for {package!r} conflicts with inventory "
                "has_tests=true"
                if inventory_test_flags.get(package) is True
                else f"no-test marker for {package!r} is absent from the package inventory"
            )
            for package in parsed.get("no_test_markers", [])
            if inventory_test_flags.get(package) is not False
        ]
        parsed["no_test_classification_errors"] = no_test_errors
        if no_test_errors:
            parsed["execution_valid"] = False
    expected_by_record = {
        record.get("label"): set(record.get("selected_packages", []))
        for record in records_for_analysis
    }
    all_expected = sorted(
        {package for packages in expected_by_record.values() for package in packages}
    )
    all_observed = sorted(
        {
            package
            for parsed in parsed_records
            for package in parsed.get("terminal_packages", [])
        }
    )
    source_values = sorted(
        {
            record["source_sha"]
            for record in all_records
            if isinstance(record.get("source_sha"), str)
            and record.get("source_sha")
        }
    )
    missing_source_records = [
        record.get("label")
        for record in all_records
        if not isinstance(record.get("source_sha"), str)
        or not record.get("source_sha")
    ]
    mismatched_source_records = [
        record.get("label")
        for record in all_records
        if isinstance(record.get("source_sha"), str)
        and record.get("source_sha") != manifest.get("source_sha")
    ]
    unvalidated_source_records = [
        parsed.get("record_id")
        for parsed in parsed_all_records
        if parsed.get("source_status") == "UNVALIDATED"
    ]
    source_identity = {
        "manifest": manifest.get("source_sha"),
        "record_values": source_values,
        "missing_records": missing_source_records,
        "mismatched_records": mismatched_source_records,
        "unvalidated_records": unvalidated_source_records,
        "validation_required": require_source_validation,
        "all_match": (
            isinstance(manifest.get("source_sha"), str)
            and bool(all_records)
            and not missing_source_records
            and not mismatched_source_records
            and not unvalidated_source_records
            and all(
                value == manifest.get("source_sha") for value in source_values
            )
        ),
    }
    manifest_metadata_errors: list[str] = capture_errors
    run_groups_for_analysis = capture.get("run_groups", [])
    group_validation: dict[str, dict[str, Any]] = {}
    for group in run_groups_for_analysis:
        group_id = group.get("run_group_id")
        group_records = [
            record
            for record in all_records
            if record.get("run_group_id") == group_id
        ]
        repetition = validate_repetition_group(group, group_records, manifest)
        quiet = validate_recorded_quiet_evidence(
            group.get("quiet_evidence"),
            root=root,
            mode=str(manifest.get("mode", "hermetic")),
        )
        errors = list(repetition.get("errors", []))
        if quiet.get("status") != "PASS":
            errors.append(str(quiet.get("error", "quiet evidence is invalid")))
        group_validation[str(group_id)] = {
            **repetition,
            "quiet_evidence": quiet,
            "valid": not errors,
            "errors": errors,
        }
    for parsed in parsed_all_records:
        record_value = parsed.get("record")
        group_id = (
            record_value.get("run_group_id")
            if isinstance(record_value, dict)
            else None
        )
        if not isinstance(group_id, str):
            group_id = None
        validation = group_validation.get(group_id)
        parsed["run_group_validation"] = validation
        if not isinstance(validation, dict) or validation.get("valid") is not True:
            parsed["execution_valid"] = False
    execution_valid = (
        bool(parsed_all_records)
        and selection_error is None
        and not manifest_metadata_errors
        and bool(group_validation)
        and all(
            validation.get("valid") is True
            for validation in group_validation.values()
        )
        and all(parsed.get("execution_valid") for parsed in parsed_all_records)
    )
    stream_failures = [
        {
            "record": parsed.get("record_id"),
            "error": (
                parsed.get("error")
                or parsed.get("stderr_error")
                or "; ".join(
                    parsed.get("timing_errors", [])
                    + parsed.get("record_schema_errors", [])
                    + parsed.get("artifact_errors", [])
                    + parsed.get("no_test_classification_errors", [])
                    + parsed.get("source_validation_errors", [])
                )
                or None
            ),
            "process_status": parsed.get("process_status"),
            "source_status": parsed.get("source_status"),
            "timing_errors": parsed.get("timing_errors", []),
            "record_schema_errors": parsed.get("record_schema_errors", []),
            "artifact_errors": parsed.get("artifact_errors", []),
            "source_validation_errors": parsed.get(
                "source_validation_errors", []
            ),
            "no_test_classification_errors": parsed.get(
                "no_test_classification_errors", []
            ),
            "missing_packages": parsed.get("missing_packages", []),
            "unexpected_packages": parsed.get("unexpected_packages", []),
            "cached_markers": parsed.get("cached_markers", []),
            "run_group_errors": (
                parsed.get("run_group_validation", {}).get("errors", [])
                if isinstance(parsed.get("run_group_validation"), dict)
                else ["record is not attached to a valid run group"]
            ),
        }
        for parsed in parsed_all_records
        if not parsed.get("execution_valid")
    ]
    for group_id, validation in sorted(group_validation.items()):
        if validation.get("valid") is not True:
            stream_failures.append(
                {
                    "record": f"run_group:{group_id}",
                    "error": "; ".join(validation.get("errors", [])),
                    "process_status": None,
                    "source_status": None,
                    "missing_packages": [],
                    "unexpected_packages": [],
                    "cached_markers": [],
                    "run_group_errors": validation.get("errors", []),
                }
            )
    if manifest_metadata_errors:
        stream_failures.append(
            {
                "record": "manifest:commands",
                "error": "; ".join(manifest_metadata_errors),
                "process_status": None,
                "source_status": None,
                "missing_packages": [],
                "unexpected_packages": [],
                "cached_markers": [],
                "run_group_errors": manifest_metadata_errors,
            }
        )
    if selection_error is not None:
        stream_failures.append(
            {
                "record": "analysis:group-filter",
                "error": selection_error,
                "process_status": None,
                "source_status": None,
                "missing_packages": [],
                "unexpected_packages": [],
                "cached_markers": [],
                "run_group_errors": [selection_error],
            }
        )
    no_test_packages = sorted(
        {
            package["import_path"]
            for module in manifest.get("modules", [])
            for package in module.get("packages", [])
            if not package.get("has_tests")
        }
    )
    subtest_records = [
        parsed.get("record_id")
        for parsed in parsed_records
        if parsed.get("subtest_overlap")
    ]
    lane = lane_times(records_for_analysis)
    target_comparison = {
        "target_seconds": TARGET_SECONDS,
        "lane_wall_seconds": lane["lane_wall_seconds"],
        "observed_under_target": (
            lane["lane_wall_seconds"] is not None
            and lane["lane_wall_seconds"] < TARGET_SECONDS
        ),
        "status": "OBSERVATION_ONLY_NOT_A_GATE",
    }
    fresh_timing = "PASS" if execution_valid and source_identity["all_match"] else "INVALID"
    raw_stdout_retained = bool(parsed_all_records) and all(
        parsed.get("raw_stdout_retained") is True for parsed in parsed_all_records
    )
    raw_stderr_retained = bool(parsed_all_records) and all(
        parsed.get("raw_stderr_retained") is True for parsed in parsed_all_records
    )
    warm = manifest.get("warm", {})
    warm_commands = [
        command
        for command in capture.get("commands", [])
        if command.get("phase") == "warm"
    ]
    inventory_commands = [
        command
        for command in capture.get("commands", [])
        if command.get("phase") == "inventory"
    ]
    analysis = {
        "schema": ANALYSIS_SCHEMA,
        "status": "PASS" if fresh_timing == "PASS" else "INVALID",
        "fresh_timing": fresh_timing,
        "source": {
            "sha": manifest.get("source_sha"),
            "repo": manifest.get("repo"),
            "runner": manifest.get("runner"),
            "flags": manifest.get("flags"),
            "source_identity": source_identity,
        },
        "inventory": {
            "expected_packages": len(all_expected),
            "observed_terminal_packages": len(all_observed),
            "expected_package_paths": all_expected,
            "observed_package_paths": all_observed,
            "missing_package_paths": sorted(set(all_expected) - set(all_observed)),
            "unexpected_package_paths": sorted(set(all_observed) - set(all_expected)),
            "no_test_packages": no_test_packages,
            "coverage": (
                len(set(all_expected) & set(all_observed)) / len(all_expected)
                if all_expected
                else 0.0
            ),
            "notes": (
                "No-test packages are retained as a separate inventory class; "
                "they do not make a missing or failed test stream pass."
            ),
        },
        "lane": lane,
        "target_comparison": target_comparison,
        "package_execution": {
            "ranked_packages": package_rank(parsed_records),
            "package_time_total_seconds": checked_duration_sum(
                (
                    observation["elapsed_seconds"]
                    for parsed in parsed_records
                    for observation in parsed.get("observations", [])
                ),
                label="package execution duration total",
            ),
            "package_budget_policy": {
                "tool": "tools/timingate",
                "budget_seconds": 60,
                "status": "REFERENCE_ONLY",
                "note": (
                    "The canonical PR-tier package-budget evaluator remains in "
                    "tools/timingate; offline analysis does not duplicate or "
                    "execute that policy."
                ),
            },
            "subtest_overlap_records": subtest_records,
            "subtest_overlap_count": len(subtest_records),
        },
        "setup_and_build": {
            "inventory_cold_setup_seconds": checked_duration_sum(
                (
                    record.get("wall_seconds", 0.0)
                    for record in inventory_commands
                ),
                label="inventory setup duration total",
            ),
            "warm_status": warm.get("status", "NOT_RUN"),
            "dependency_download_seconds": checked_duration_sum(
                (
                    record.get("wall_seconds", 0.0)
                    for record in warm_commands
                    if record.get("role") == "go-mod-download"
                ),
                label="dependency download duration total",
            ),
            "test_binary_compile_seconds": checked_duration_sum(
                (
                    record.get("wall_seconds", 0.0)
                    for record in warm_commands
                    if record.get("role") == "go-test-binary-compile"
                ),
                label="test binary compile duration total",
            ),
            "test_build_attribution": (
                "RESIDUAL_ESTIMATE: go test execution can include package/test "
                "binary builds; warm compile timings are separate evidence and "
                "are not subtracted from lane wall."
            ),
        },
        "repetitions": [
            {
                "run_group_id": group.get("run_group_id")
                if isinstance(group, dict)
                else None,
                "cohort": group.get("cohort"),
                "requested_repetitions": group.get("requested_repetitions"),
                "completed_repetitions": group.get("completed_repetitions"),
                "status": group.get("status"),
                "records": [
                    record.get("label")
                    for record in records_for_analysis
                    if isinstance(group, dict)
                    and record.get("run_group_id") == group.get("run_group_id")
                ],
                "validation": group_validation.get(
                    group.get("run_group_id")
                    if isinstance(group, dict)
                    and isinstance(group.get("run_group_id"), str)
                    else None,
                    {
                        "status": "INVALID",
                        "valid": False,
                        "errors": ["run group is not attached to the analysis"],
                    },
                ),
            }
            for group in run_groups_for_analysis
            if isinstance(group, dict)
            and (args.group is None or group.get("run_group_id") == args.group)
        ],
        "failure_references": stream_failures,
        "provenance": {
            "raw_stdout_retained": raw_stdout_retained,
            "raw_stderr_retained": raw_stderr_retained,
            "raw_artifacts_retained": raw_stdout_retained and raw_stderr_retained,
            "test_result_cache_policy": "every run command contains -count=1",
            "cached_output_is_invalid": True,
            "lane_wall_not_sum_of_packages": True,
            "fresh_timing_requires_quiet_evidence": True,
            "source_identity_required_per_record": True,
            "run_group_repetition_counts_validated": True,
            "quiet_evidence_provenance_validated": True,
            "analysis_artifact_written_atomically": True,
            "invalid_artifact_written_atomically": fresh_timing != "PASS",
        },
    }
    write_json_atomic(output / "analysis.json", analysis)
    return {
        "status": analysis["status"],
        "fresh_timing": fresh_timing,
        "analysis": str(output / "analysis.json"),
        "source_sha": manifest.get("source_sha"),
        "lane_wall_seconds": lane["lane_wall_seconds"],
        "ranked_package_count": len(analysis["package_execution"]["ranked_packages"]),
        "failure_count": len(stream_failures),
    }, 0 if analysis["status"] == "PASS" else 1


def add_heavy_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--allow-heavy",
        action="store_true",
        help="explicitly opt into Go/download/test work after isolation is proven",
    )
    parser.add_argument(
        "--quiet-evidence",
        required=True,
        help="JSON evidence for a dedicated quiet runner (synthetic controls may use a fixture evidence file)",
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Profile the existing CGO-disabled nomicrophone hermetic lane. "
            "Heavy commands are opt-in and fail closed without dedicated-runner evidence."
        )
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    inventory_parser = subparsers.add_parser(
        "inventory", help="discover the six hermetic module package inventories"
    )
    inventory_parser.add_argument("--repo", required=True, help="isolated repository root")
    inventory_parser.add_argument("--output", required=True, help="owned output directory")
    inventory_parser.add_argument("--go", default="go", help="Go executable")
    inventory_parser.add_argument(
        "--gomaxprocs",
        type=require_positive,
        default=None,
        help=f"explicit GOMAXPROCS (default: {DEFAULT_PARALLELISM})",
    )
    inventory_parser.add_argument(
        "--parallelism",
        type=require_positive,
        default=None,
        help="explicit go test -p (default: GOMAXPROCS)",
    )
    inventory_parser.add_argument(
        "--general-timeout",
        type=require_positive,
        default=GENERAL_TIMEOUT_SECONDS,
        help="unchanged timeout for general modules, in seconds",
    )
    inventory_parser.add_argument(
        "--agent-timeout",
        type=require_positive,
        default=AGENT_CLI_TIMEOUT_SECONDS,
        help="unchanged target-wide agent-cli timeout, in seconds",
    )
    inventory_parser.add_argument(
        "--source-plan", help="optional source-plan file to hash into the manifest"
    )
    add_heavy_arguments(inventory_parser)

    warm_parser = subparsers.add_parser(
        "warm", help="download dependencies and compile test binaries without running tests"
    )
    warm_parser.add_argument("--manifest", required=True, help="inventory manifest")
    add_heavy_arguments(warm_parser)

    run_parser = subparsers.add_parser(
        "run", help="capture one uncached inventory or at most five-package cohort"
    )
    run_parser.add_argument("--manifest", required=True, help="inventory manifest")
    run_parser.add_argument(
        "--cohort",
        help="package import paths, module:./path tokens, comma list, or one token per file line",
    )
    run_parser.add_argument(
        "--repeat",
        type=require_positive,
        default=1,
        help="number of unchanged cohort/inventory invocations (default: 1)",
    )
    add_heavy_arguments(run_parser)

    analyze_parser = subparsers.add_parser(
        "analyze", help="analyze retained raw JSON/stderr completely offline"
    )
    analyze_parser.add_argument("--manifest", required=True, help="inventory/run manifest")
    analyze_parser.add_argument("--output", required=True, help="owned analysis output directory")
    analyze_parser.add_argument("--group", help="analyze one run_group_id only")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "inventory":
            summary, status = inventory(args)
        elif args.command == "warm":
            summary, status = warm(args)
        elif args.command == "run":
            summary, status = run_profile(args)
        elif args.command == "analyze":
            summary, status = analyze(args)
        else:
            raise ProfileError(f"unsupported command {args.command!r}")
    except ProfileError as exc:
        if args.command == "analyze":
            summary, status = invalid_analysis(
                Path(args.output).resolve(),
                manifest_path=Path(args.manifest).resolve(),
                error=exc,
            )
        else:
            print(f"profile.py: error: {exc}", file=sys.stderr)
            return 2
    except (OSError, OverflowError, ValueError, KeyError, TypeError) as exc:
        if args.command == "analyze":
            summary, status = invalid_analysis(
                Path(args.output).resolve(),
                manifest_path=Path(args.manifest).resolve(),
                error=exc,
            )
        else:
            print(f"profile.py: error: {exc}", file=sys.stderr)
            return 2
    except Exception as exc:
        if args.command != "analyze":
            raise
        summary, status = invalid_analysis(
            Path(args.output).resolve(),
            manifest_path=Path(args.manifest).resolve(),
            error=exc,
        )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return status


if __name__ == "__main__":
    raise SystemExit(main())
