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
TARGET_SECONDS = 180.0
GENERAL_TIMEOUT_SECONDS = 300
AGENT_CLI_TIMEOUT_SECONDS = 480
MAX_DURATION_NANOSECONDS = (1 << 63) - 1
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
        missing = [
            key
            for key in ("active_work", "processes", "load")
            if key not in observation
        ]
        if missing:
            return (
                f"quiet evidence {phase} is missing observations: "
                + ", ".join(missing)
            )
        if not isinstance(observation["active_work"], list):
            return f"quiet evidence {phase}.active_work must be a list"
        if not isinstance(observation["processes"], list):
            return f"quiet evidence {phase}.processes must be a list"
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
        "inventory_commands": [],
        "modules": [],
        "warm": {"status": "NOT_RUN", "commands": [], "test_binaries": []},
        "runs": [],
        "run_groups": [],
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
    manifest: dict[str, Any], *, output_root: Path, output_dir: Path, label: str
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
        "metadata_commands": [head_record, status_record],
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
    manifest["metadata_commands"] = [git_head, dirty_record, go_env_record, version_record]
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
        record["module"] = name
        manifest["inventory_commands"].append(record)
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


def check_ready_manifest(manifest: dict[str, Any]) -> None:
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


def warm(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    manifest_path = Path(args.manifest).resolve()
    manifest = load_manifest(manifest_path)
    check_ready_manifest(manifest)
    root = manifest_root(manifest_path)
    quiet = require_heavy(args, mode="hermetic", root=root)
    warm_root = root / "warm"
    env_overrides = manifest_env(manifest, root=root)
    go = manifest_go(manifest)
    commands: list[dict[str, Any]] = []
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
        record["phase"] = "dependency-download"
        record["module"] = module["name"]
        commands.append(record)
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
                record["phase"] = "test-binary-compile"
                record["module"] = module["name"]
                record["package"] = package["import_path"]
                binary = {
                    "module": module["name"],
                    "package": package["import_path"],
                    "status": record["status"],
                    "command_record": record,
                }
                if binary_path.is_file():
                    binary["path"] = relative_path(binary_path, root)
                    binary["sha256"] = sha256_file(binary_path)
                    binary["bytes"] = binary_path.stat().st_size
                binaries.append(binary)
                commands.append(record)
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
        "commands": commands,
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
            if item.get("phase") == "dependency-download"
        ),
        "test_binary_compile_seconds": sum(
            item["wall_seconds"]
            for item in commands
            if item.get("phase") == "test-binary-compile"
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
        check_ready_manifest(manifest)
        quiet = require_heavy(args, mode="hermetic", root=root)
    if args.repeat > 3:
        raise ProfileError("repeat is capped at three invocations (one plus two repeats)")
    if args.cohort is None and args.repeat != 1:
        raise ProfileError("full inventory runs are single-shot; repeat only a bounded cohort")
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
    source_validations: list[dict[str, Any]] = []
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
                    "module": module["name"],
                    "cohort": cohort_mode,
                    "selected_packages": [
                        package["import_path"] for package in packages
                    ],
                    "expected_package_count": len(packages),
                    "test_result_cache_policy": "disabled-by--count=1",
                }
            )
            records.append(record)
            if source_validation is not None:
                source_validations.append(source_validation)
            if record["status"] != "PASS":
                failures.append(f"{module['name']} repetition {repeat_index}")
                repetition_failed = True
                break
        if repetition_failed:
            break
        repetitions_completed += 1
    group = {
        "schema": "c11-run-group-v1",
        "run_group_id": group_id,
        "source_sha": manifest.get("source_sha"),
        "created_at_utc": utc_now(),
        "cohort": cohort_mode,
        "selected_packages": selected_packages,
        "requested_repetitions": args.repeat,
        "completed_repetitions": repetitions_completed,
        "records": records,
        "source_validations": source_validations,
        "quiet_evidence": quiet,
        "status": "FAILED" if failures else "CAPTURED",
        "failures": failures,
        "note": (
            "The first failed repetition stops unchanged repeats; no retry-until-green."
            if failures
            else "Raw streams are analyzed separately; command success is not performance proof."
        ),
    }
    manifest.setdefault("runs", []).extend(records)
    manifest.setdefault("run_groups", []).append(group)
    manifest["measurement_status"] = "RUN_CAPTURED"
    save_manifest(manifest_path, manifest)
    return {
        "status": group["status"],
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


def command_timing_errors(record: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    starts = record.get("monotonic_start_ns")
    ends = record.get("monotonic_end_ns")
    if isinstance(starts, bool) or not isinstance(starts, int) or starts < 0:
        errors.append("monotonic_start_ns must be a non-negative integer")
    if isinstance(ends, bool) or not isinstance(ends, int) or ends < 0:
        errors.append("monotonic_end_ns must be a non-negative integer")
    if (
        isinstance(starts, int)
        and not isinstance(starts, bool)
        and isinstance(ends, int)
        and not isinstance(ends, bool)
        and ends < starts
    ):
        errors.append("monotonic_end_ns must not precede monotonic_start_ns")
    try:
        parse_elapsed(record.get("wall_seconds"))
    except ValueError as exc:
        errors.append(f"wall_seconds is invalid: {exc}")
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
    expected_source_sha: Any,
    require_source_validation: bool,
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
    if require_source_validation:
        validation = record.get("source_validation")
        if not isinstance(validation, dict) or validation.get("matches_manifest") is not True:
            source_status = "UNVALIDATED"

    base = {
        "record_id": record.get("label"),
        "record": record,
        "source_status": source_status,
        "raw_stdout_retained": False,
        "raw_stderr_retained": False,
        "timing_status": "INVALID",
        "timing_errors": command_timing_errors(record),
        "execution_valid": False,
    }
    base["timing_status"] = "PASS" if not base["timing_errors"] else "INVALID"
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
                stderr_hash_matches = (
                    isinstance(record.get("stderr_sha256"), str)
                    and record.get("stderr_sha256") == stderr_hash
                )
            except OSError as exc:
                stderr_error = f"raw stderr missing: {exc}"
    else:
        stderr_error = "command record has no stderr_path"
    parsed["stderr_hash_matches"] = stderr_hash_matches
    parsed["stderr_error"] = stderr_error
    parsed["process_status"] = (
        "PASS"
        if record.get("status") == "PASS" and record.get("exit_status") == 0
        else "TIMEOUT"
        if record.get("timed_out")
        else "FAIL"
    )
    expected = set(str(item) for item in record.get("selected_packages", []))
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
        and parsed["inventory_complete"]
        and not parsed["package_failures"]
        and not parsed["test_failures"]
        and not parsed["cached_markers"]
        and parsed["stdout_hash_matches"]
        and parsed["stderr_hash_matches"]
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
        ranked.append(
            {
                "package": package,
                "observations": len(durations),
                "durations_seconds": durations,
                "min_seconds": min(durations),
                "max_seconds": max(durations),
                "spread_seconds": max(durations) - min(durations),
                "mean_seconds": sum(durations) / len(durations),
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
    ]
    ends = [
        int(record["monotonic_end_ns"])
        for record in records
        if isinstance(record.get("monotonic_end_ns"), int)
        and not isinstance(record.get("monotonic_end_ns"), bool)
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
        "sum_invocation_wall_seconds": sum(durations),
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

    declared_records = group.get("records")
    if not isinstance(declared_records, list):
        errors.append("run group has no records list")
        declared_labels: list[str] = []
    else:
        declared_labels = [
            str(record.get("label"))
            for record in declared_records
            if isinstance(record, dict) and record.get("label") is not None
        ]
    actual_labels = [
        str(record.get("label"))
        for record in records
        if record.get("label") is not None
    ]
    if sorted(declared_labels) != sorted(actual_labels):
        errors.append("run group records do not match manifest run records")

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


def normalize_run_records(value: Any) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        raise ProfileError("manifest runs must be a list")
    records: list[dict[str, Any]] = []
    for index, record in enumerate(value):
        if isinstance(record, dict):
            records.append(record)
            continue
        records.append(
            {
                "label": f"<malformed-record-{index}>",
                "_schema_error": (
                    f"run record {index} is not a JSON object "
                    f"(got {type(record).__name__})"
                ),
                "selected_packages": [],
            }
        )
    return records


def analyze(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    manifest_path = Path(args.manifest).resolve()
    manifest = load_manifest(manifest_path)
    root = manifest_root(manifest_path)
    all_records = normalize_run_records(manifest.get("runs", []))
    if args.group:
        all_records = [
            record
            for record in all_records
            if record.get("run_group_id") == args.group
        ]
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    if not all_records:
        if manifest.get("fresh_timing_status") == "BLOCKED":
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
            write_json(output / "analysis.json", analysis)
            return {
                "status": "BLOCKED",
                "analysis": str(output / "analysis.json"),
                "source_sha": manifest.get("source_sha"),
            }, 0
        raise ProfileError("manifest contains no captured runs and is not marked BLOCKED")

    require_source_validation = (
        manifest.get("mode") != "synthetic" or "source_repo" in manifest
    )
    parsed_records = [
        parse_record_stream(
            record,
            root=root,
            expected_source_sha=manifest.get("source_sha"),
            require_source_validation=require_source_validation,
        )
        for record in all_records
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
    for parsed in parsed_records:
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
        for record in all_records
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
        for parsed in parsed_records
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

    run_groups = manifest.get("run_groups")
    run_groups_for_analysis = run_groups if isinstance(run_groups, list) else []
    group_validation: dict[str, dict[str, Any]] = {}
    duplicate_group_ids: set[str] = set()
    if isinstance(run_groups, list):
        for index, group in enumerate(run_groups):
            if (
                args.group is not None
                and (
                    not isinstance(group, dict)
                    or group.get("run_group_id") != args.group
                )
            ):
                continue
            if not isinstance(group, dict):
                group_validation[f"<group-{index}>"] = {
                    "status": "INVALID",
                    "valid": False,
                    "errors": ["run group is not an object"],
                    "quiet_evidence": {"status": "INVALID"},
                }
                continue
            group_id = group.get("run_group_id")
            key = group_id if isinstance(group_id, str) and group_id else f"<group-{index}>"
            if key in group_validation:
                duplicate_group_ids.add(key)
                continue
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
            group_validation[key] = {
                **repetition,
                "quiet_evidence": quiet,
                "valid": not errors,
                "errors": errors,
            }
    else:
        group_validation["<manifest>"] = {
            "status": "INVALID",
            "valid": False,
            "errors": ["manifest has no run_groups list"],
            "quiet_evidence": {"status": "INVALID"},
        }
    for duplicate in sorted(duplicate_group_ids):
        group_validation[duplicate] = {
            "status": "INVALID",
            "valid": False,
            "errors": [f"run_group_id {duplicate!r} is duplicated"],
            "quiet_evidence": {"status": "INVALID"},
        }
    for parsed in parsed_records:
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
    execution_valid = all(parsed.get("execution_valid") for parsed in parsed_records)
    stream_failures = [
        {
            "record": parsed.get("record_id"),
            "error": (
                parsed.get("error")
                or parsed.get("stderr_error")
                or "; ".join(
                    parsed.get("timing_errors", [])
                    + parsed.get("no_test_classification_errors", [])
                )
                or None
            ),
            "process_status": parsed.get("process_status"),
            "source_status": parsed.get("source_status"),
            "timing_errors": parsed.get("timing_errors", []),
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
        for parsed in parsed_records
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
    lane = lane_times(all_records)
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
    raw_stdout_retained = bool(parsed_records) and all(
        parsed.get("raw_stdout_retained") is True for parsed in parsed_records
    )
    raw_stderr_retained = bool(parsed_records) and all(
        parsed.get("raw_stderr_retained") is True for parsed in parsed_records
    )
    warm = manifest.get("warm", {})
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
            "package_time_total_seconds": sum(
                observation["elapsed_seconds"]
                for parsed in parsed_records
                for observation in parsed.get("observations", [])
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
            "inventory_cold_setup_seconds": sum(
                record.get("wall_seconds", 0.0)
                for record in manifest.get("inventory_commands", [])
            ),
            "warm_status": warm.get("status", "NOT_RUN"),
            "dependency_download_seconds": sum(
                record.get("wall_seconds", 0.0)
                for record in warm.get("commands", [])
                if record.get("phase") == "dependency-download"
            ),
            "test_binary_compile_seconds": sum(
                record.get("wall_seconds", 0.0)
                for record in warm.get("commands", [])
                if record.get("phase") == "test-binary-compile"
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
                    record.get("label") if isinstance(record, dict) else None
                    for record in group.get("records", [])
                ]
                if isinstance(group.get("records", []), list)
                else [],
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
        },
    }
    write_json(output / "analysis.json", analysis)
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
        print(f"profile.py: error: {exc}", file=sys.stderr)
        return 2
    except (OSError, OverflowError, ValueError, KeyError, TypeError) as exc:
        print(f"profile.py: error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(summary, indent=2, sort_keys=True))
    return status


if __name__ == "__main__":
    raise SystemExit(main())
