#!/usr/bin/env python3
"""Reproduce and verify the C51 audio/device boundary characterization.

The verifier is intentionally standard-library-only.  It keeps subprocesses
bounded, records exact source and authority hashes, and treats queue admission
and device-callback consumption as different observations.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import selectors
import signal
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import wave
from pathlib import Path

try:
    import resource
except ImportError:  # pragma: no cover - Windows software validation has no RLIMIT_FSIZE.
    resource = None


EVIDENCE_ROOT = Path(__file__).resolve().parent
REPO_ROOT = EVIDENCE_ROOT.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
TASK = "audio-runtime-c51-audio-device-boundary-characterization"
OWNED_REL = Path("docs/temp/projects/audio-runtime") / TASK
SOURCE_REVISION = "bb29005d0bb545db5e08ffda0205929b021d4fc1"
PLANNING_MAIN_REVISION = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
EXPECTED_BRANCH = "codex/audio-runtime-c51-audio-device-boundary-characterization"
MAX_OUTPUT = 64 * 1024
REQUIRED_RESERVE_BYTES = 2 * 1024 * 1024 * 1024
MAX_ARCHIVE_BYTES = 1024 * 1024 * 1024
MAX_WAV_BYTES = 8 * 1024 * 1024
MAX_RUN_DISK_BYTES = 32 * 1024 * 1024
DISK_POLL_INTERVAL = 0.01
BUILD_INPUT_ROOTS = (
    "agent-cli",
    "go-agent-loop",
    "go-agent-runtime",
    "go-audio",
    "go-device-gateway",
    "go-llm-gateway",
)

MODULES = {
    "go-agent-loop": {
        "root": Path("go-agent-loop"),
        "path": "github.com/portpowered/go-agent-harness/go-agent-loop",
    },
    "go-llm-gateway": {
        "root": Path("go-llm-gateway"),
        "path": "github.com/portpowered/go-agent-harness/go-llm-gateway",
    },
    "go-audio": {
        "root": Path("go-audio"),
        "path": "github.com/portpowered/go-agent-harness/go-audio",
    },
    "go-device-gateway": {
        "root": Path("go-device-gateway"),
        "path": "github.com/portpowered/go-agent-harness/go-device-gateway",
    },
    "go-agent-runtime": {
        "root": Path("go-agent-runtime"),
        "path": "github.com/portpowered/go-agent-harness/go-agent-runtime",
    },
    "agent-cli": {
        "root": Path("agent-cli"),
        "path": "github.com/portpowered/go-agent-harness/agent-cli",
    },
}

CONSUMPTION_LEVELS = [
    "QUEUE_ADMISSION",
    "BUFFER_RECEIPT",
    "FILE_OR_SOFTWARE_RECEIPT",
    "SOFTWARE_DEVICE_CALLBACK_CONSUMPTION",
    "PHYSICAL_HARDWARE_CONSUMPTION",
]


def die(message: str) -> None:
    raise SystemExit(f"C51 verification failed: {message}")


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        die(f"read JSON {path}: {exc}")


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def trim_output(data: bytes, limit: int) -> tuple[str, bool]:
    overflow = len(data) > limit
    return data[:limit].decode("utf-8", errors="replace"), overflow


def _drain(stream, sink: list[bytes], limit: int, overflow: list[bool]) -> None:
    retained = 0
    while True:
        chunk = stream.read(8192)
        if not chunk:
            return
        if len(chunk) > max(0, limit - retained):
            overflow[0] = True
        if retained < limit:
            keep = chunk[: limit - retained]
            sink.append(keep)
            retained += len(keep)


def _ps_group_has_live_member(pgid: int) -> bool | None:
    """Distinguish live group members from unreaped zombies on POSIX hosts."""

    try:
        result = subprocess.run(
            ["ps", "-axo", "pid=,pgid=,stat="],
            capture_output=True,
            text=True,
            timeout=1.0,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    found = False
    for record in result.stdout.splitlines():
        fields = record.split()
        if len(fields) < 3:
            continue
        try:
            record_pgid = int(fields[1])
        except ValueError:
            continue
        if record_pgid != pgid:
            continue
        found = True
        if not fields[2].startswith("Z"):
            return True
    return False if found else None


def process_group_is_alive(pgid: int) -> bool:
    """Return whether the POSIX process group still has a live member."""

    if not hasattr(os, "killpg"):
        return False
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    ps_state = _ps_group_has_live_member(pgid)
    return True if ps_state is None else ps_state


def _signal_process_group(process: subprocess.Popen, sig: signal.Signals) -> None:
    if hasattr(os, "killpg"):
        os.killpg(process.pid, sig)
    else:  # Windows software validation still gets bounded parent cleanup.
        process.send_signal(sig)


def terminate_process_group(
    process: subprocess.Popen,
    *,
    term_timeout: float = 1.0,
    kill_timeout: float = 2.0,
    deadline: float | None = None,
) -> None:
    """Terminate the complete process group, including descendants.

    A parent may have exited while a descendant still owns the stdout pipe. Do
    not use ``process.poll`` as a proxy for group liveness: always signal the
    original process group and then verify the group itself is gone.
    """

    def bounded_timeout(limit: float) -> float:
        if deadline is None:
            return max(0.0, limit)
        return max(0.0, min(limit, deadline - time.monotonic()))

    try:
        _signal_process_group(process, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=bounded_timeout(term_timeout))
    except subprocess.TimeoutExpired:
        pass
    if not process_group_is_alive(process.pid):
        return
    try:
        _signal_process_group(process, signal.SIGKILL)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=bounded_timeout(kill_timeout))
    except subprocess.TimeoutExpired:
        pass
    if process_group_is_alive(process.pid):
        raise RuntimeError(f"process group survived TERM/KILL: {process.args}")


def directory_size(root: Path) -> int:
    total = 0
    if not root.exists():
        return 0
    for directory, _, names in os.walk(root):
        for name in names:
            path = Path(directory) / name
            try:
                stat = path.lstat()
            except FileNotFoundError:
                continue
            if not path.is_symlink():
                total += stat.st_size
    return total


def assert_disk_budget(root: Path, before: int, limit: int, label: str) -> tuple[int, int]:
    after = directory_size(root)
    delta = max(0, after - before)
    if delta > limit:
        die(f"{label} grew by {delta} bytes, exceeding owned limit {limit}")
    return after, delta


def pid_is_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    try:
        result = subprocess.run(["ps", "-p", str(pid), "-o", "stat="], capture_output=True, text=True, timeout=1.0, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return True
    states = result.stdout.split()
    return not (result.returncode == 0 and states and states[0].startswith("Z"))


def wait_for_pid_exit(pid: int, timeout: float = 2.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not pid_is_alive(pid):
            return True
        time.sleep(0.01)
    return not pid_is_alive(pid)


def run_bounded(
    argv: list[str],
    *,
    cwd: Path = REPO_ROOT,
    timeout: float = 60.0,
    output_limit: int = MAX_OUTPUT,
    env: dict[str, str] | None = None,
    deadline: float | None = None,
    disk_root: Path | None = None,
    disk_limit: int | None = None,
    file_limit: int | None = None,
    inject_reader_start_failure: bool = False,
) -> dict:
    """Run a process group with bounded output, time, disk, and group cleanup."""

    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    if deadline is not None:
        timeout = min(timeout, deadline - time.monotonic())
    if timeout <= 0:
        die(f"aggregate deadline exhausted before starting {' '.join(argv)}")
    disk_before = directory_size(disk_root) if disk_root is not None else None
    started = time.monotonic()
    process_deadline = started + timeout
    effective_deadline = process_deadline if deadline is None else min(process_deadline, deadline)

    def limit_child_file_size() -> None:
        if file_limit is None or resource is None or not hasattr(resource, "RLIMIT_FSIZE"):
            return
        current_soft, current_hard = resource.getrlimit(resource.RLIMIT_FSIZE)
        requested = int(file_limit)
        if current_hard != resource.RLIM_INFINITY:
            requested = min(requested, current_hard)
        resource.setrlimit(resource.RLIMIT_FSIZE, (requested, requested))

    try:
        popen_args = {
            "cwd": str(cwd),
            "env": merged_env,
            "stdin": subprocess.DEVNULL,
            "stdout": subprocess.PIPE,
            "stderr": subprocess.PIPE,
            "start_new_session": True,
        }
        if file_limit is not None and resource is not None and os.name == "posix":
            popen_args["preexec_fn"] = limit_child_file_size
        process = subprocess.Popen(argv, **popen_args)
    except OSError as exc:
        die(f"start {' '.join(argv)}: {exc}")

    stdout_parts: list[bytes] = []
    stderr_parts: list[bytes] = []
    stdout_overflow = [False]
    stderr_overflow = [False]
    stdout_thread: threading.Thread | None = None
    stderr_thread: threading.Thread | None = None
    timed_out = False
    disk_limit_exceeded = False
    return_code: int | None = None
    try:
        if inject_reader_start_failure:
            time.sleep(0.1)
            raise RuntimeError("injected output-reader start failure")
        stdout_thread = threading.Thread(target=_drain, args=(process.stdout, stdout_parts, output_limit, stdout_overflow), daemon=True)
        stderr_thread = threading.Thread(target=_drain, args=(process.stderr, stderr_parts, output_limit, stderr_overflow), daemon=True)
        stdout_thread.start()
        stderr_thread.start()
        while True:
            if disk_root is not None and disk_limit is not None:
                current_disk = directory_size(disk_root)
                current_delta = max(0, current_disk - (disk_before or 0))
                if current_delta > disk_limit:
                    disk_limit_exceeded = True
                    try:
                        terminate_process_group(process, deadline=deadline)
                    except RuntimeError as exc:
                        die(str(exc))
                    return_code = process.returncode
                    break
            if process.poll() is not None:
                return_code = process.wait()
                break
            remaining = effective_deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                try:
                    terminate_process_group(process, deadline=deadline)
                except RuntimeError as exc:
                    die(str(exc))
                return_code = process.returncode
                break
            try:
                return_code = process.wait(timeout=min(DISK_POLL_INTERVAL, remaining))
                break
            except subprocess.TimeoutExpired:
                continue
    except BaseException:
        # This cleanup runs before any reader-survival error is reported. A
        # reader-start failure must not leave a forked descendant behind.
        try:
            terminate_process_group(process, deadline=deadline)
        except RuntimeError as exc:
            die(str(exc))
        raise
    finally:
        # Always clean the group, even after the parent has exited. A child
        # that inherited stdout/stderr can otherwise keep readers alive.
        try:
            terminate_process_group(process, deadline=deadline)
        except RuntimeError as exc:
            die(str(exc))
        join_deadline = time.monotonic() + 2.0
        if deadline is not None:
            join_deadline = min(join_deadline, deadline)
        if stdout_thread is not None:
            stdout_thread.join(timeout=max(0.0, join_deadline - time.monotonic()))
        if stderr_thread is not None:
            stderr_thread.join(timeout=max(0.0, join_deadline - time.monotonic()))
        if (stdout_thread is not None and stdout_thread.is_alive()) or (stderr_thread is not None and stderr_thread.is_alive()):
            # Re-assert group cleanup before surfacing a reader failure.
            try:
                terminate_process_group(process, deadline=deadline)
            except RuntimeError as exc:
                die(str(exc))
            die(f"output reader survived process cleanup: {' '.join(argv)}")
    if disk_root is not None and disk_limit is not None:
        disk_after = directory_size(disk_root)
        disk_delta = max(0, disk_after - (disk_before or 0))
        disk_limit_exceeded = disk_limit_exceeded or disk_delta > disk_limit
    else:
        disk_after, disk_delta = None, None
    group_alive = process_group_is_alive(process.pid)
    elapsed = time.monotonic() - started
    stdout, stdout_truncated = trim_output(b"".join(stdout_parts), output_limit)
    stderr, stderr_truncated = trim_output(b"".join(stderr_parts), output_limit)
    return {
        "argv": argv,
        "cwd": str(cwd),
        "returnCode": process.returncode if return_code is None else return_code,
        "timedOut": timed_out,
        "durationSeconds": round(elapsed, 6),
        "stdout": stdout,
        "stderr": stderr,
        "stdoutTruncated": stdout_overflow[0] or stdout_truncated,
        "stderrTruncated": stderr_overflow[0] or stderr_truncated,
        "processGroupAlive": group_alive,
        "diskBytesBefore": disk_before,
        "diskBytesAfter": disk_after,
        "diskBytesDelta": disk_delta,
        "diskLimitBytes": disk_limit,
        "diskLimitExceeded": disk_limit_exceeded,
        "fileLimitBytes": file_limit,
    }


def git(*args: str, timeout: float = 30.0) -> str:
    result = run_bounded(["git", *args], timeout=timeout)
    if result["returnCode"] != 0:
        die(f"git {' '.join(args)} failed: {result['stderr'][-2000:]}")
    return result["stdout"].strip()


def git_bytes_at(revision: str, relative: str) -> bytes:
    result = run_bounded(["git", "show", f"{revision}:{relative}"], output_limit=16 * 1024 * 1024)
    if result["returnCode"] != 0:
        die(f"missing {relative} at {revision}: {result['stderr'][-1000:]}")
    if result["stdoutTruncated"]:
        die(f"source file is too large to validate safely: {relative}")
    return result["stdout"].encode("utf-8")


def git_blob_hash(revision: str, relative: str) -> tuple[str, int]:
    """Hash a Git blob without decoding or retaining binary build inputs."""

    try:
        process = subprocess.Popen(
            ["git", "cat-file", "blob", f"{revision}:{relative}"],
            cwd=str(REPO_ROOT),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as exc:
        die(f"start git blob read for {relative}: {exc}")
    digest = hashlib.sha256()
    total = 0
    stderr_parts: list[bytes] = []
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ, "stdout")
    selector.register(process.stderr, selectors.EVENT_READ, "stderr")
    deadline = time.monotonic() + 60.0
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                terminate_process_group(process)
                die(f"git blob read timed out for {relative}")
            events = selector.select(timeout=min(0.25, remaining))
            if not events:
                continue
            for key, _ in events:
                data = key.fileobj.read(1024 * 1024)
                if not data:
                    selector.unregister(key.fileobj)
                    continue
                if key.data == "stdout":
                    digest.update(data)
                    total += len(data)
                elif sum(len(part) for part in stderr_parts) < MAX_OUTPUT:
                    stderr_parts.append(data[: MAX_OUTPUT - sum(len(part) for part in stderr_parts)])
        return_code = process.wait(timeout=max(0.0, deadline - time.monotonic()))
    except subprocess.TimeoutExpired:
        terminate_process_group(process)
        die(f"git blob read timed out for {relative}")
    finally:
        selector.close()
        terminate_process_group(process)
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()
    if return_code != 0:
        stderr = b"".join(stderr_parts).decode("utf-8", errors="replace")
        die(f"missing {relative} at {revision}: {stderr[-1000:]}")
    return digest.hexdigest(), total


def git_archive_hash(revision: str) -> tuple[str, int]:
    try:
        process = subprocess.Popen(
            ["git", "archive", "--format=tar", revision],
            cwd=str(REPO_ROOT),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as exc:
        die(f"start git archive: {exc}")
    digest = hashlib.sha256()
    archive_bytes = 0
    stderr_parts: list[bytes] = []
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ, "stdout")
    selector.register(process.stderr, selectors.EVENT_READ, "stderr")
    deadline = time.monotonic() + 120.0
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                terminate_process_group(process)
                die(f"git archive timed out for {revision}")
            events = selector.select(timeout=min(0.25, remaining))
            if not events:
                continue
            for key, _ in events:
                data = key.fileobj.read(1024 * 1024)
                if not data:
                    selector.unregister(key.fileobj)
                    continue
                if key.data == "stdout":
                    archive_bytes += len(data)
                    if archive_bytes > MAX_ARCHIVE_BYTES:
                        terminate_process_group(process)
                        die(f"source archive exceeded {MAX_ARCHIVE_BYTES}-byte bound during streaming")
                    digest.update(data)
                elif sum(len(part) for part in stderr_parts) < MAX_OUTPUT:
                    stderr_parts.append(data[: MAX_OUTPUT - sum(len(part) for part in stderr_parts)])
        return_code = process.wait(timeout=max(0.0, deadline - time.monotonic()))
    except subprocess.TimeoutExpired:
        terminate_process_group(process)
        die(f"git archive timed out for {revision}")
    finally:
        selector.close()
        terminate_process_group(process)
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()
    if return_code != 0:
        stderr = b"".join(stderr_parts).decode("utf-8", errors="replace")
        die(f"git archive failed: {stderr[-2000:]}")
    return digest.hexdigest(), archive_bytes


def source_files() -> list[tuple[str, str, Path]]:
    result: list[tuple[str, str, Path]] = []
    for module_name, metadata in MODULES.items():
        root = REPO_ROOT / metadata["root"]
        for path in sorted(root.rglob("*.go")):
            if any(part in {"vendor", ".git"} for part in path.parts):
                continue
            relative = path.relative_to(REPO_ROOT).as_posix()
            in_test_tree = path.name.endswith("_test.go") or "/test/" in f"/{relative}/" or relative.startswith("test/")
            role = "test" if in_test_tree else "tool" if "/cmd/" in f"/{relative}" or "/tools/" in f"/{relative}" else "production"
            result.append((module_name, role, path))
    return result


def build_analysis_inputs() -> dict:
    files = []
    for module_name, metadata in MODULES.items():
        go_mod = REPO_ROOT / metadata["root"] / "go.mod"
        relative = go_mod.relative_to(REPO_ROOT).as_posix()
        files.append({
            "path": relative,
            "role": "module-metadata",
            "module": module_name,
            "sha256": sha256_bytes(git_bytes_at(SOURCE_REVISION, relative)),
        })
    for module_name, role, path in source_files():
        relative = path.relative_to(REPO_ROOT).as_posix()
        files.append({
            "path": relative,
            "role": role,
            "module": module_name,
            "sha256": sha256_bytes(git_bytes_at(SOURCE_REVISION, relative)),
        })
    return {
        "schema": "audio-runtime-c51.analysis-inputs.v1",
        "sourceRevision": SOURCE_REVISION,
        "files": sorted(files, key=lambda item: item["path"]),
    }


def command_record(argv: list[str], *, cwd: Path = REPO_ROOT, timeout: float = 60.0, output_limit: int = MAX_OUTPUT, env: dict[str, str] | None = None) -> dict:
    result = run_bounded(argv, cwd=cwd, timeout=timeout, output_limit=output_limit, env=env)
    record = {
        "argv": argv,
        "cwd": str(cwd),
        "returnCode": result["returnCode"],
        "timedOut": result["timedOut"],
        "processGroupAlive": result["processGroupAlive"],
        "stdout": result["stdout"],
        "stderr": result["stderr"],
        "stdoutTruncated": result["stdoutTruncated"],
        "stderrTruncated": result["stderrTruncated"],
    }
    if result["returnCode"] == 0 and not result["timedOut"]:
        try:
            record["json"] = json.loads(result["stdout"])
        except json.JSONDecodeError:
            pass
    return record


def project_control_record() -> dict:
    argv = [
        "python3",
        "factory/scripts/project-control.py",
        "verify-work",
        "--type",
        "task",
        "--name",
        TASK,
        "--root",
        str(FACTORY_ROOT),
    ]
    result = command_record(argv, cwd=FACTORY_ROOT, timeout=30.0)
    validate_project_control_record(result)
    return result


def validate_project_control_record(record: dict) -> None:
    expected_argv = [
        "python3",
        "factory/scripts/project-control.py",
        "verify-work",
        "--type",
        "task",
        "--name",
        TASK,
        "--root",
        str(FACTORY_ROOT),
    ]
    if record.get("argv") != expected_argv or record.get("cwd") != str(FACTORY_ROOT):
        die(f"project-control command identity is incomplete: {record}")
    if record.get("returnCode") != 0 or record.get("timedOut") or record.get("processGroupAlive"):
        die(f"project admission verification was not bounded and successful: {record}")
    if record.get("stdoutTruncated") or record.get("stderrTruncated") or record.get("stderr"):
        die(f"project admission output was truncated or noisy: {record}")
    if record.get("json") != {"status": "admitted", "project": "audio-runtime", "name": TASK}:
        die(f"project admission identity is not the admitted audio-runtime task: {record}")


def go_toolchain_record() -> dict:
    version = command_record(["go", "version"], timeout=30.0)
    env_result = run_bounded(
        ["go", "env", "GOOS", "GOARCH", "GOVERSION", "GOTOOLCHAIN"],
        timeout=30.0,
        output_limit=4096,
        env={"GOWORK": "off"},
    )
    if version["returnCode"] != 0 or env_result["returnCode"] != 0:
        die(f"Go toolchain identification failed: version={version} env={env_result}")
    names = ["GOOS", "GOARCH", "GOVERSION", "GOTOOLCHAIN"]
    values = env_result["stdout"].splitlines()
    if len(values) != len(names):
        die(f"unexpected go env identity output: {env_result['stdout']!r}")
    if any(not value.strip() for value in values) or not version["stdout"].strip().startswith("go version "):
        die(f"Go toolchain identity is incomplete: version={version} env={env_result}")
    return {
        "version": version["stdout"].strip(),
        "env": dict(zip(names, values)),
        "environment": {"GOWORK": "off"},
    }


def is_ancestor(ancestor: str, descendant: str) -> bool:
    return run_bounded(["git", "merge-base", "--is-ancestor", ancestor, descendant], timeout=30.0)["returnCode"] == 0


def build_input_manifest(revision: str) -> dict:
    pathspecs = ["go.work", "go.work.sum", *BUILD_INPUT_ROOTS]
    listing = run_bounded(
        ["git", "ls-tree", "-r", "--name-only", revision, *pathspecs],
        timeout=60.0,
        output_limit=16 * 1024 * 1024,
    )
    if listing["returnCode"] != 0:
        die(f"cannot enumerate Go build inputs at {revision}: {listing}")
    if listing["stdoutTruncated"]:
        die(f"build-input listing exceeded the safe enumeration bound at {revision}")
    paths = []
    for path in listing["stdout"].splitlines():
        if path == "go.work" or path == "go.work.sum" or any(path.startswith(f"{root}/") for root in BUILD_INPUT_ROOTS):
            paths.append(path)
    if not paths:
        die(f"empty Go build input manifest at {revision}")
    paths = sorted(set(paths))
    if "go-agent-loop/pkg/probe/testdata/goal_catalog.json" not in paths:
        die("transitive yui build input goal_catalog.json is absent from the manifest")
    if not any(path.startswith("agent-cli/internal/webmcp/siteadapter/extensions/") and path.endswith(".js") for path in paths):
        die("transitive yui siteadapter extension inputs are absent from the manifest")
    files = []
    for path in paths:
        digest, size = git_blob_hash(revision, path)
        files.append({"path": path, "sha256": digest, "bytes": size})
    canonical = "".join(f"{item['path']}\t{item['sha256']}\n" for item in files).encode("utf-8")
    non_go_inputs = [item for item in files if not item["path"].endswith((".go", "/go.mod", "/go.sum")) and item["path"] not in {"go.work", "go.work.sum"}]
    return {
        "sourceRevision": revision,
        "selection": {
            "method": "git ls-tree all tracked files under the local yui workspace roots",
            "roots": list(BUILD_INPUT_ROOTS),
            "workspaceFiles": ["go.work", "go.work.sum"],
            "reason": "conservatively includes transitive Go embed and local JS/JSON/other asset inputs",
        },
        "files": files,
        "nonGoInputs": non_go_inputs,
        "inputTreeSha256": sha256_bytes(canonical),
    }


def replay_fixture_inputs() -> list[dict]:
    relatives = [
        "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json",
        "go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json",
    ]
    result = []
    for relative in relatives:
        path = REPO_ROOT / relative
        if not path.is_file():
            die(f"missing shipped replay fixture: {path}")
        result.append({"path": relative, "sha256": sha256_file(path), "bytes": path.stat().st_size})
    return result


def write_build_manifest(binary: str) -> None:
    path = Path(binary).resolve()
    if not path.is_file() or not os.access(path, os.X_OK):
        die(f"cannot write build manifest for non-executable binary: {path}")
    status = git("status", "--short")
    if status:
        die(f"build manifest must be captured from a clean candidate:\n{status}")
    revision = git("rev-parse", "HEAD")
    if not is_ancestor(SOURCE_REVISION, revision):
        die("build candidate is not descended from the characterized source revision")
    inputs = build_input_manifest(revision)
    manifest = {
        "schema": "audio-runtime-c51.shipped-build.v2",
        "testedRevision": revision,
        "branch": git("branch", "--show-current"),
        "workingTreeStatusAtCapture": status,
        "sourceRevision": SOURCE_REVISION,
        "inputSelection": inputs["selection"],
        "inputTreeSha256": inputs["inputTreeSha256"],
        "inputs": inputs["files"],
        "nonGoInputs": inputs["nonGoInputs"],
        "toolchain": go_toolchain_record(),
        "build": {
            "command": ["go", "build", "-trimpath", "-o", "<C51_YUI>", "./cmd/yui"],
            "cwd": "agent-cli",
            "environment": {"GOWORK": "off"},
        },
        "binary": {"path": str(path), "sha256": sha256_file(path), "bytes": path.stat().st_size},
        "replayFixtures": replay_fixture_inputs(),
        "retention": "External exact-head executable retained at the recorded path; evidence-only descendants must prove identical Go build inputs.",
    }
    write_json(EVIDENCE_ROOT / "evidence/shipped-build.json", manifest)
    print(json.dumps({"status": "written", "testedRevision": revision, "binarySHA256": manifest["binary"]["sha256"], "inputTreeSha256": manifest["inputTreeSha256"]}, sort_keys=True))


def verify_build_manifest(binary: Path, manifest_path: Path) -> dict:
    manifest = read_json(manifest_path)
    if manifest.get("schema") != "audio-runtime-c51.shipped-build.v2":
        die("shipped build manifest schema mismatch")
    if manifest.get("workingTreeStatusAtCapture") != "":
        die("shipped build manifest was not captured from a clean candidate")
    if manifest.get("branch") != EXPECTED_BRANCH:
        die("shipped build manifest branch does not match the admitted candidate")
    tested_revision = manifest.get("testedRevision")
    head = git("rev-parse", "HEAD")
    if not isinstance(tested_revision, str) or not is_ancestor(tested_revision, head):
        die("shipped build tested revision is not an ancestor of the candidate head")
    if not is_ancestor(SOURCE_REVISION, tested_revision):
        die("shipped build tested revision does not contain the characterized source")
    if manifest.get("sourceRevision") != SOURCE_REVISION:
        die("shipped build source revision mismatch")
    expected_tested_inputs = build_input_manifest(tested_revision)
    expected_head_inputs = build_input_manifest(head)
    for label, expected in (("tested source", expected_tested_inputs), ("candidate head", expected_head_inputs)):
        if expected["selection"] != manifest.get("inputSelection"):
            die(f"{label} build-input selection differs from the pinned shipped-build manifest")
        if expected["inputTreeSha256"] != manifest.get("inputTreeSha256") or expected["files"] != manifest.get("inputs"):
            die(f"{label} build inputs differ from the pinned shipped-build manifest")
        if expected["nonGoInputs"] != manifest.get("nonGoInputs"):
            die(f"{label} non-Go/embedded asset inputs differ from the pinned shipped-build manifest")
    build = manifest.get("build", {})
    if build.get("command") != ["go", "build", "-trimpath", "-o", "<C51_YUI>", "./cmd/yui"] or build.get("cwd") != "agent-cli" or build.get("environment") != {"GOWORK": "off"}:
        die("shipped build command or GOWORK identity is incomplete")
    recorded_binary = manifest.get("binary", {})
    if Path(recorded_binary.get("path", "")).resolve() != binary:
        die("supplied shipped binary does not match the pinned build manifest path")
    if not binary.is_file() or sha256_file(binary) != recorded_binary.get("sha256") or binary.stat().st_size != recorded_binary.get("bytes"):
        die("supplied shipped binary does not match the pinned build manifest hash/size")
    toolchain = go_toolchain_record()
    if toolchain != manifest.get("toolchain"):
        die("current Go toolchain identity differs from the pinned shipped-build manifest")
    current_fixtures = replay_fixture_inputs()
    if current_fixtures != manifest.get("replayFixtures"):
        die("replay fixture inputs differ from the pinned shipped-build manifest")
    return manifest


def parse_imports(path: Path) -> list[dict]:
    lines = path.read_text(encoding="utf-8").splitlines()
    imports: list[dict] = []
    in_block = False
    for index, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("import ("):
            in_block = True
            continue
        if in_block and stripped == ")":
            in_block = False
            continue
        candidate = stripped if in_block else stripped.removeprefix("import ") if stripped.startswith("import ") else ""
        if not candidate or candidate.startswith("//"):
            continue
        first_quote = candidate.find('"')
        if first_quote < 0:
            continue
        second_quote = candidate.find('"', first_quote + 1)
        if second_quote < 0:
            continue
        imports.append({"path": candidate[first_quote + 1 : second_quote], "line": index})
    return imports


def module_for_import(import_path: str) -> str | None:
    matches = [name for name, metadata in MODULES.items() if import_path == metadata["path"] or import_path.startswith(metadata["path"] + "/")]
    return max(matches, key=lambda name: len(MODULES[name]["path"])) if matches else None


def build_import_graph(analysis_inputs: dict) -> dict:
    package_metadata = {}
    for module_name, metadata in MODULES.items():
        result = run_bounded(["go", "list", "-json", "./..."], cwd=REPO_ROOT / metadata["root"], timeout=120.0, output_limit=2 * 1024 * 1024, env={"GOWORK": "off"})
        if result["returnCode"] != 0:
            die(f"GOWORK=off go list for {module_name} failed: {result['stderr'][-2000:]}")
        decoder = json.JSONDecoder()
        offset = 0
        packages = []
        listing = result["stdout"]
        while offset < len(listing):
            while offset < len(listing) and listing[offset].isspace():
                offset += 1
            if offset >= len(listing):
                break
            try:
                package, next_offset = decoder.raw_decode(listing, offset)
            except json.JSONDecodeError as exc:
                die(f"parse go list metadata for {module_name} at byte {offset}: {exc}")
            offset = next_offset
            if not isinstance(package, dict) or not package.get("ImportPath"):
                continue
            packages.append({
                "importPath": package["ImportPath"],
                "dir": str(Path(package.get("Dir", "")).relative_to(REPO_ROOT)) if package.get("Dir", "").startswith(str(REPO_ROOT)) else package.get("Dir", ""),
                "goFiles": sorted(package.get("GoFiles", [])),
                "cgoFiles": sorted(package.get("CgoFiles", [])),
                "testGoFiles": sorted(package.get("TestGoFiles", [])),
                "xtestGoFiles": sorted(package.get("XTestGoFiles", [])),
            })
        package_metadata[module_name] = sorted(packages, key=lambda item: item["importPath"])

    edge_citations: dict[tuple[str, str, str, str], list[dict]] = {}
    for from_module, role, path in source_files():
        relative = path.relative_to(REPO_ROOT).as_posix()
        for imported in parse_imports(path):
            to_module = module_for_import(imported["path"])
            if to_module is None or to_module == from_module:
                continue
            key = (role, from_module, to_module, imported["path"])
            edge_citations.setdefault(key, []).append({"file": relative, "line": imported["line"]})

    edges = []
    for (role, from_module, to_module, import_path), citations in sorted(edge_citations.items()):
        edges.append({
            "from": from_module,
            "to": to_module,
            "importPath": import_path,
            "role": role,
            "citations": sorted(citations, key=lambda item: (item["file"], item["line"])),
        })
    production_edges = [edge for edge in edges if edge["role"] == "production"]
    adjacency = {name: set() for name in MODULES}
    for edge in production_edges:
        adjacency[edge["from"]].add(edge["to"])
    closure: dict[str, list[str]] = {}
    for start in MODULES:
        seen: set[str] = set()
        pending = sorted(adjacency[start])
        while pending:
            current = pending.pop(0)
            if current in seen:
                continue
            seen.add(current)
            pending.extend(sorted(adjacency[current] - seen))
        closure[start] = sorted(seen)
    forbidden = []
    for edge in production_edges:
        if edge["from"] in {"go-agent-loop", "go-audio"} and edge["to"] == "go-device-gateway":
            forbidden.append(edge)
    return {
        "schema": "audio-runtime-c51.import-graph.v1",
        "sourceRevision": SOURCE_REVISION,
        "modules": {name: metadata["path"] for name, metadata in MODULES.items()},
        "moduleMetadata": [item for item in analysis_inputs["files"] if item["role"] == "module-metadata"],
        "packages": package_metadata,
        "productionEdges": production_edges,
        "nonProductionEdges": [edge for edge in edges if edge["role"] != "production"],
        "transitiveProductionClosure": closure,
        "forbiddenProductionImports": forbidden,
    }


def authority_hashes() -> list[dict]:
    relatives = [
        "factory/docs/operating-policy.md",
        "factory/docs/implementation-handoff.md",
        "factory/projects/audio-runtime/manifest.json",
        "factory/projects/audio-runtime/request.md",
        "factory/projects/audio-runtime/acceptance.md",
        "factory/projects/audio-runtime/source-plan.md",
        "factory/projects/audio-runtime/amendments/user-windows-hardware-scope-20260910.json",
    ]
    result = []
    for relative in relatives:
        path = FACTORY_ROOT / relative
        if not path.is_file():
            die(f"authority input is unavailable: {path}")
        result.append({"path": relative, "sha256": sha256_file(path)})
    return result


def write_provenance(analysis_inputs: dict) -> dict:
    current_main = git("rev-parse", "origin/main")
    branch = git("branch", "--show-current")
    candidate = git("rev-parse", "HEAD")
    candidate_status = git("status", "--short")
    if candidate_status:
        die(f"provenance must be captured from a clean candidate:\n{candidate_status}")
    archive_hash, archive_bytes = git_archive_hash(SOURCE_REVISION)
    manifest = read_json(FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json")
    authority = authority_hashes()
    analysis_path = EVIDENCE_ROOT / "analysis-inputs.json"
    control = project_control_record()
    toolchain = go_toolchain_record()
    build_manifest_path = EVIDENCE_ROOT / "evidence/shipped-build.json"
    if not build_manifest_path.is_file():
        die("provenance requires the shipped-build manifest to exist")
    build_manifest = read_json(build_manifest_path)
    binary_path = Path(build_manifest.get("binary", {}).get("path", "")).resolve()
    if not binary_path.is_file():
        die(f"provenance requires the retained shipped binary: {binary_path}")
    verify_build_manifest(binary_path, build_manifest_path)
    boundary_path = EVIDENCE_ROOT / "boundary-map.json"
    return {
        "schema": "audio-runtime-c51.provenance.v2",
        "project": "audio-runtime",
        "contractRevision": manifest.get("contractRevision"),
        "task": TASK,
        "admission": {
            "verifyCommand": "python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c51-audio-device-boundary-characterization --root $FACTORY_ROOT",
            "status": "admitted",
            "project": "audio-runtime",
            "name": TASK,
            "workId": "work-task-22",
            "projectControl": control,
        },
        "toolchain": toolchain,
        "source": {
            "branch": branch,
            "expectedBranch": EXPECTED_BRANCH,
            "sourceRevision": SOURCE_REVISION,
            "planningMainRevision": PLANNING_MAIN_REVISION,
            "currentMainRevision": current_main,
            "startupIntegrationRevision": STARTUP_INTEGRATION_REVISION,
            "baselineRevision": BASELINE_REVISION,
            "cleanCandidateSHA": candidate,
            "candidateStatusAtCapture": candidate_status,
            "candidateWasCleanAtCapture": candidate_status == "",
            "fetchedMain": {"ref": "origin/main", "revision": current_main, "identityVerified": True},
            "requiredAncestry": {
                "startupIntegration": {"revision": STARTUP_INTEGRATION_REVISION, "isAncestor": is_ancestor(STARTUP_INTEGRATION_REVISION, candidate)},
                "planningMain": {"revision": PLANNING_MAIN_REVISION, "isAncestor": is_ancestor(PLANNING_MAIN_REVISION, candidate)},
                "fetchedMain": {"revision": current_main, "isAncestor": is_ancestor(current_main, candidate)},
            },
            "sourceArchive": {
                "command": "git archive --format=tar <sourceRevision>",
                "sha256": archive_hash,
                "bytes": archive_bytes,
                "maxBytes": MAX_ARCHIVE_BYTES,
            },
        },
        "authorityInputs": authority,
        "analysisInputs": {
            "path": analysis_path.relative_to(REPO_ROOT).as_posix(),
            "sha256": sha256_file(analysis_path),
            "fileCount": len(analysis_inputs["files"]),
        },
        "boundaryMap": {
            "path": boundary_path.relative_to(REPO_ROOT).as_posix(),
            "sha256": sha256_file(boundary_path),
            "boundaryCount": len(read_json(boundary_path).get("boundaries", [])),
        },
        "scripts": [{"path": __file__.replace(str(REPO_ROOT) + "/", ""), "sha256": sha256_file(Path(__file__))}],
        "shippedBuildManifest": {
            "path": build_manifest_path.relative_to(REPO_ROOT).as_posix(),
            "sha256": sha256_file(build_manifest_path),
            "testedRevision": build_manifest["testedRevision"],
            "sourceRevision": build_manifest["sourceRevision"],
            "inputTreeSha256": build_manifest["inputTreeSha256"],
            "inputCount": len(build_manifest.get("inputs", [])),
            "nonGoInputCount": len(build_manifest.get("nonGoInputs", [])),
        } if build_manifest_path.is_file() else None,
        "scope": {
            "ownedPath": OWNED_REL.as_posix(),
            "productionEdits": "none",
            "physicalHardware": "OUT_OF_SCOPE",
            "physicalAcousticProof": "OUT_OF_SCOPE",
            "windowsNativeEndpoints": "OUT_OF_SCOPE",
            "windowsSoftwareCompileAndHermeticValidation": "retained",
        },
        "storage": {
            "freeBytesAtGeneration": shutil.disk_usage(REPO_ROOT).free,
            "requiredCompileReserveBytes": REQUIRED_RESERVE_BYTES,
        },
    }


def mode_generate() -> None:
    analysis = build_analysis_inputs()
    write_json(EVIDENCE_ROOT / "analysis-inputs.json", analysis)
    graph = build_import_graph(analysis)
    write_json(EVIDENCE_ROOT / "import-graph.json", graph)
    provenance = write_provenance(analysis)
    write_json(EVIDENCE_ROOT / "provenance.json", provenance)
    print(json.dumps({"status": "generated", "analysisFiles": len(analysis["files"]), "sourceRevision": SOURCE_REVISION}, sort_keys=True))


def verify_provenance() -> dict:
    path = EVIDENCE_ROOT / "provenance.json"
    provenance = read_json(path)
    if provenance.get("schema") != "audio-runtime-c51.provenance.v2":
        die("provenance schema mismatch")
    if git("branch", "--show-current") != EXPECTED_BRANCH:
        die("branch does not match the admitted PRD branch")
    current_main = git("rev-parse", "origin/main")
    if current_main != SOURCE_REVISION:
        die("origin/main moved after the fetched integration checkpoint; fetch and integrate before handoff")
    status = git("status", "--short")
    if status:
        die(f"worktree is not clean:\n{status}")
    head = git("rev-parse", "HEAD")
    if not is_ancestor(SOURCE_REVISION, head):
        die("candidate head is not descended from the characterized source revision")
    changed = git("diff", "--name-only", f"{SOURCE_REVISION}..{head}")
    changed_paths = [Path(line) for line in changed.splitlines() if line]
    if any(path != OWNED_REL and OWNED_REL not in path.parents for path in changed_paths):
        die(f"candidate changes outside owned evidence path: {changed_paths}")
    prd = read_json(REPO_ROOT / "prd.json")
    if prd.get("branchName") != EXPECTED_BRANCH:
        die("prd.json.branchName does not match the isolated branch")
    admission = provenance.get("admission", {})
    control = admission.get("projectControl", {})
    expected_verify_command = "python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c51-audio-device-boundary-characterization --root $FACTORY_ROOT"
    if admission.get("verifyCommand") != expected_verify_command or admission.get("workId") != "work-task-22" or admission.get("status") != "admitted" or admission.get("project") != "audio-runtime" or admission.get("name") != TASK:
        die("provenance admission identity is incomplete")
    validate_project_control_record(control)
    current_control = project_control_record()
    if current_control != control:
        die("project-control admission output changed since provenance capture")
    recorded = provenance.get("source", {})
    if recorded.get("branch") != EXPECTED_BRANCH or recorded.get("expectedBranch") != EXPECTED_BRANCH:
        die("provenance source branch identity is incomplete")
    if recorded.get("sourceRevision") != SOURCE_REVISION or recorded.get("planningMainRevision") != PLANNING_MAIN_REVISION or recorded.get("currentMainRevision") != current_main or recorded.get("startupIntegrationRevision") != STARTUP_INTEGRATION_REVISION or recorded.get("baselineRevision") != BASELINE_REVISION:
        die("provenance revision pins do not match the fetched integrated main")
    fetched_main = recorded.get("fetchedMain", {})
    if fetched_main.get("ref") != "origin/main" or fetched_main.get("revision") != current_main or fetched_main.get("identityVerified") is not True:
        die("provenance fetched-main identity does not match origin/main")
    candidate = recorded.get("cleanCandidateSHA")
    if not candidate or not recorded.get("candidateWasCleanAtCapture") or recorded.get("candidateStatusAtCapture") != "":
        die("provenance does not record a clean candidate SHA")
    if not is_ancestor(candidate, head):
        die("clean candidate SHA is not an ancestor of the submitted head")
    required_ancestry = recorded.get("requiredAncestry", {})
    for key, revision in (("startupIntegration", STARTUP_INTEGRATION_REVISION), ("planningMain", PLANNING_MAIN_REVISION), ("fetchedMain", SOURCE_REVISION)):
        entry = required_ancestry.get(key, {})
        if entry.get("revision") != revision or entry.get("isAncestor") is not True or not is_ancestor(revision, head):
            die(f"required ancestry is not pinned and verified for {key}")
    if provenance.get("toolchain") != go_toolchain_record():
        die("toolchain/GOOS/GOARCH identity changed since provenance capture")
    archive_hash, archive_bytes = git_archive_hash(SOURCE_REVISION)
    source_archive = recorded.get("sourceArchive", {})
    if source_archive.get("command") != "git archive --format=tar <sourceRevision>" or source_archive.get("maxBytes") != MAX_ARCHIVE_BYTES or archive_bytes > MAX_ARCHIVE_BYTES or archive_hash != source_archive.get("sha256") or archive_bytes != source_archive.get("bytes"):
        die("source archive hash changed for the pinned revision")
    analysis_ref = provenance.get("analysisInputs", {})
    analysis_path = REPO_ROOT / analysis_ref.get("path", "")
    if analysis_ref.get("path") != (OWNED_REL / "analysis-inputs.json").as_posix() or not analysis_path.is_file() or sha256_file(analysis_path) != analysis_ref.get("sha256"):
        die("analysis-inputs.json hash does not match provenance")
    analysis = read_json(analysis_path)
    if analysis.get("schema") != "audio-runtime-c51.analysis-inputs.v1" or analysis.get("sourceRevision") != SOURCE_REVISION or analysis_ref.get("fileCount") != len(analysis.get("files", [])):
        die("analysis-inputs.json identity is incomplete")
    for item in analysis["files"]:
        actual = sha256_bytes(git_bytes_at(SOURCE_REVISION, item["path"]))
        if actual != item["sha256"]:
            die(f"analysis input changed at {item['path']}")
    boundary_ref = provenance.get("boundaryMap", {})
    boundary_path = REPO_ROOT / boundary_ref.get("path", "")
    if boundary_ref.get("path") != (OWNED_REL / "boundary-map.json").as_posix() or not boundary_path.is_file() or sha256_file(boundary_path) != boundary_ref.get("sha256"):
        die("boundary-map.json hash does not match provenance")
    boundary = read_json(boundary_path)
    if boundary.get("sourceRevision") != SOURCE_REVISION or boundary_ref.get("boundaryCount") != len(boundary.get("boundaries", [])):
        die("boundary-map.json identity is incomplete")
    for item in provenance["authorityInputs"]:
        actual = sha256_file(FACTORY_ROOT / item["path"])
        if actual != item["sha256"]:
            die(f"authority input changed at {item['path']}")
    for item in provenance["scripts"]:
        actual = sha256_file(REPO_ROOT / item["path"])
        if actual != item["sha256"]:
            die(f"analysis script changed at {item['path']}")
    build_manifest = provenance.get("shippedBuildManifest")
    if not build_manifest:
        die("provenance does not pin the shipped build-input manifest")
    expected_build_path = (OWNED_REL / "evidence/shipped-build.json").as_posix()
    build_manifest_path = REPO_ROOT / build_manifest.get("path", "")
    if build_manifest.get("path") != expected_build_path or not build_manifest_path.is_file() or sha256_file(build_manifest_path) != build_manifest.get("sha256"):
        die("shipped build-input manifest is missing or changed")
    shipped_build = read_json(build_manifest_path)
    if build_manifest.get("testedRevision") != shipped_build.get("testedRevision") or build_manifest.get("sourceRevision") != shipped_build.get("sourceRevision") or build_manifest.get("inputTreeSha256") != shipped_build.get("inputTreeSha256") or build_manifest.get("inputCount") != len(shipped_build.get("inputs", [])) or build_manifest.get("nonGoInputCount") != len(shipped_build.get("nonGoInputs", [])):
        die("provenance shipped-build summary differs from the manifest")
    binary = Path(shipped_build.get("binary", {}).get("path", "")).resolve()
    verify_build_manifest(binary, build_manifest_path)
    storage = provenance.get("storage", {})
    if storage.get("requiredCompileReserveBytes") != REQUIRED_RESERVE_BYTES or storage.get("freeBytesAtGeneration", 0) < REQUIRED_RESERVE_BYTES:
        die("provenance does not record the required compile storage reserve")
    free = shutil.disk_usage(REPO_ROOT).free
    if free < REQUIRED_RESERVE_BYTES:
        die(f"free storage {free} is below the required 2 GiB compile reserve")
    print(json.dumps({"status": "verified", "head": head, "sourceRevision": SOURCE_REVISION, "changedPaths": [p.as_posix() for p in changed_paths], "freeBytes": free, "buildInputTreeSha256": shipped_build["inputTreeSha256"]}, sort_keys=True))
    return provenance


def verify_imports() -> None:
    analysis = read_json(EVIDENCE_ROOT / "analysis-inputs.json")
    if analysis.get("sourceRevision") != SOURCE_REVISION:
        die("analysis input source revision mismatch")
    expected = build_import_graph(analysis)
    actual = read_json(EVIDENCE_ROOT / "import-graph.json")
    if actual != expected:
        die("import-graph.json is not reproducible from the pinned production source")
    if actual["forbiddenProductionImports"]:
        die(f"forbidden production import edges found: {actual['forbiddenProductionImports']}")
    print(json.dumps({"status": "verified", "productionEdges": len(actual["productionEdges"]), "nonProductionEdges": len(actual["nonProductionEdges"]), "closure": actual["transitiveProductionClosure"]}, sort_keys=True))


def citation_text(citation: dict) -> str:
    return f"{citation.get('path')}:{citation.get('line')} ({citation.get('symbol')})"


FUNCTION_DECL_RE = re.compile(r"^\s*func\s+(?:\([^)]*\)\s*)?(?P<name>[A-Za-z_]\w*)\s*\(")
TYPE_DECL_RE = re.compile(r"^\s*type\s+(?P<name>[A-Za-z_]\w*)\s+")


def go_brace_delta(line: str) -> int:
    """Count Go braces outside the common quoted/comment portions of a line."""

    without_strings = re.sub(r'"(?:\\.|[^"\\])*"', '""', line)
    without_strings = re.sub(r'`[^`]*`', "``", without_strings)
    return without_strings.split("//", 1)[0].count("{") - without_strings.split("//", 1)[0].count("}")


def enclosing_function(source: list[str], line_number: int) -> str | None:
    """Find the exact Go function body containing a 1-based source line."""

    declarations = [
        (index, match.group("name"))
        for index, line in enumerate(source)
        if (match := FUNCTION_DECL_RE.match(line)) is not None and index < line_number
    ]
    for declaration_index, name in reversed(declarations):
        depth = 0
        opened = False
        for index in range(declaration_index, line_number):
            depth += go_brace_delta(source[index])
            opened = opened or "{" in source[index]
            if opened and depth <= 0:
                break
        if opened and depth > 0:
            return name
    return None


def source_role_by_path() -> dict[str, str]:
    return {
        path.relative_to(REPO_ROOT).as_posix(): role
        for _, role, path in source_files()
    }


def verify_source_citation(citation: dict, *, boundary_id: str, production_caller: bool = False) -> None:
    required = {"path", "line", "symbol", "kind", "needle"}
    if not required.issubset(citation):
        die(f"boundary {boundary_id} citation lacks schema fields: {citation}")
    if citation["kind"] not in {"owner_api", "production_caller", "downstream_consumer", "timing_source", "trace_observation"}:
        die(f"boundary {boundary_id} citation has unknown kind: {citation}")
    relative = citation["path"]
    roles = source_role_by_path()
    if not isinstance(relative, str) or relative.startswith(("/", "../")) or "/../" in relative or relative not in roles or roles[relative] != "production":
        die(f"boundary {boundary_id} citation is not a pinned production-source path: {citation_text(citation)}")
    source = git_bytes_at(SOURCE_REVISION, relative).decode("utf-8", errors="replace").splitlines()
    try:
        line_number = int(citation["line"])
    except (TypeError, ValueError):
        die(f"boundary {boundary_id} citation line is not an integer: {citation_text(citation)}")
    if isinstance(citation["line"], bool):
        die(f"boundary {boundary_id} citation line is not an integer: {citation_text(citation)}")
    if line_number < 1 or line_number > len(source):
        die(f"boundary citation line is out of range: {citation_text(citation)}")
    line = source[line_number - 1]
    symbol = citation["symbol"]
    needle = citation["needle"]
    if not isinstance(symbol, str) or not symbol.strip() or not isinstance(needle, str) or not needle.strip():
        die(f"boundary {boundary_id} citation symbol/needle is not a non-empty string: {citation_text(citation)}")
    stripped = line.strip()
    if not stripped or stripped.startswith(("//", "/*", "*", "*/")) or needle not in line or not re.search(rf"\b{re.escape(symbol)}\b", line):
        die(f"boundary citation does not identify the claimed source symbol: {citation_text(citation)} line={line!r}")
    if production_caller:
        caller = citation.get("caller")
        if not isinstance(caller, str) or not caller.strip():
            die(f"boundary {boundary_id} production caller lacks an enclosing caller name: {citation_text(citation)}")
        if FUNCTION_DECL_RE.match(line):
            die(f"boundary {boundary_id} production caller points at a declaration, not a call: {citation_text(citation)}")
        needle_end = line.find(needle) + len(needle)
        if "(" not in needle and not re.match(r"\s*\(", line[needle_end:]):
            die(f"boundary {boundary_id} production caller needle is not a call expression: {citation_text(citation)} line={line!r}")
        enclosing = enclosing_function(source, line_number)
        if enclosing != caller:
            die(f"boundary {boundary_id} production caller is not inside {caller}: found {enclosing!r} at {citation_text(citation)}")
    elif citation["kind"] in {"owner_api", "timing_source", "trace_observation"}:
        declaration = FUNCTION_DECL_RE.match(line) or TYPE_DECL_RE.match(line)
        if declaration is None or declaration.group("name") != symbol:
            die(f"boundary {boundary_id} API/timing/trace citation is not an exact declaration: {citation_text(citation)} line={line!r}")


def validate_boundary_map(boundary: dict) -> None:
    if boundary.get("sourceRevision") != SOURCE_REVISION:
        die("boundary map source revision mismatch")
    required = {"packet_parsing", "format_negotiation", "clock_and_timing", "dsp_and_resampling", "bounded_buffers", "core_loop_boundary", "device_selection_and_lifecycle", "runtime_device_adapter", "trace_and_replay"}
    actual = {item.get("id") for item in boundary.get("boundaries", [])}
    if actual != required:
        die(f"boundary map responsibility set = {sorted(actual)}, want {sorted(required)}")
    for item in boundary["boundaries"]:
        boundary_id = item.get("id")
        if not isinstance(boundary_id, str) or not item.get("owner") or not item.get("consumers"):
            die(f"boundary {boundary_id} lacks owner/consumer labels")
        if not item.get("productionCallers"):
            die(f"boundary {boundary_id} lacks explicit production callers")
        if not item.get("timingDomain") or not item.get("evidenceStrength"):
            die(f"boundary {boundary_id} lacks timing domain/evidence strength")
        for citation in item.get("citations", []):
            verify_source_citation(citation, boundary_id=boundary_id)
        for citation in item["productionCallers"]:
            if citation.get("kind") != "production_caller":
                die(f"boundary {boundary_id} production caller has wrong citation kind")
            verify_source_citation(citation, boundary_id=boundary_id, production_caller=True)
        if not item.get("evidence"):
            die(f"boundary {boundary_id} has no evidence disposition")


def verify_boundaries() -> None:
    boundary = read_json(EVIDENCE_ROOT / "boundary-map.json")
    validate_boundary_map(boundary)
    print(json.dumps({"status": "verified", "boundaries": len(boundary["boundaries"]), "citations": sum(len(i["citations"]) for i in boundary["boundaries"])}, sort_keys=True))


def verify_boundary_negative_controls() -> None:
    boundary = read_json(EVIDENCE_ROOT / "boundary-map.json")
    validate_boundary_map(boundary)
    controls = []
    field_control = json.loads(json.dumps(boundary))
    field_caller = field_control["boundaries"][0]["productionCallers"][0]
    field_caller.update({"line": 42, "symbol": "ImpulseResponseQ15", "needle": "ImpulseResponseQ15"})
    try:
        validate_boundary_map(field_control)
    except SystemExit as exc:
        controls.append({"name": "field_is_not_a_production_call", "rejected": True, "error": str(exc)})
    else:
        die("boundary negative control accepted a struct field as a production caller")

    declaration_control = json.loads(json.dumps(boundary))
    declaration_caller = declaration_control["boundaries"][0]["productionCallers"][0]
    declaration_caller.update({"line": 214, "symbol": "Open", "needle": "func (r *SimulatedDuplexRegistry) Open("})
    try:
        validate_boundary_map(declaration_control)
    except SystemExit as exc:
        controls.append({"name": "declaration_is_not_a_production_call", "rejected": True, "error": str(exc)})
    else:
        die("boundary negative control accepted a function declaration as a production caller")

    report = {"status": "verified", "sourceRevision": SOURCE_REVISION, "controls": controls}
    write_json(EVIDENCE_ROOT / "evidence/boundary-negative-controls.json", report)
    print(json.dumps({"status": "verified", "controls": len(controls)}, sort_keys=True))


def verify_consumption_levels() -> None:
    boundary = read_json(EVIDENCE_ROOT / "boundary-map.json")
    declared = boundary.get("consumptionLevels")
    if declared != CONSUMPTION_LEVELS:
        die(f"consumption levels = {declared}, want {CONSUMPTION_LEVELS}")
    observations = boundary.get("observations", {})
    for level in CONSUMPTION_LEVELS:
        if level not in observations:
            die(f"missing consumption disposition {level}")
        disposition = observations[level]
        if not disposition.get("status") or not disposition.get("evidence"):
            die(f"consumption disposition {level} lacks status/evidence")
    if observations["PHYSICAL_HARDWARE_CONSUMPTION"]["status"] != "OUT_OF_SCOPE":
        die("physical hardware consumption must remain OUT_OF_SCOPE under the admitted amendment")
    if observations["SOFTWARE_DEVICE_CALLBACK_CONSUMPTION"]["status"] != "OBSERVED":
        die("software callback consumption was not labeled observed")
    print(json.dumps({"status": "verified", "levels": observations}, sort_keys=True))


def consumer_dir() -> Path:
    return EVIDENCE_ROOT / "consumer"


def run_consumer_test(race: bool = False) -> dict:
    args = ["go", "test", "-count=1", "-timeout", "90s"]
    if race:
        args.append("-race")
    args.extend(["./..."])
    return run_bounded(args, cwd=consumer_dir(), timeout=120.0, env={"GOWORK": "off"})


def verify_consumer() -> None:
    deps = run_bounded(["go", "list", "-deps", "./..."], cwd=consumer_dir(), timeout=120.0, output_limit=2 * 1024 * 1024, env={"GOWORK": "off"})
    if deps["returnCode"] != 0:
        die(f"consumer GOWORK=off dependency listing failed: {deps['stderr'][-2000:]}")
    (EVIDENCE_ROOT / "consumer-deps.txt").write_text(deps["stdout"], encoding="utf-8")
    if "github.com/portpowered/go-agent-harness/agent-cli" in deps["stdout"]:
        die("external consumer transitively imports agent-cli")
    normal = run_consumer_test()
    if normal["returnCode"] != 0 or normal["timedOut"] or normal["processGroupAlive"]:
        die(f"consumer test failed: {normal}")
    race = run_consumer_test(race=True)
    if race["returnCode"] != 0 or race["timedOut"] or race["processGroupAlive"]:
        die(f"consumer race test failed: {race}")
    write_json(EVIDENCE_ROOT / "evidence/consumer-test.json", {"status": "passed", "sourceRevision": SOURCE_REVISION, "dependencyCommand": deps, "normal": normal, "race": race})
    print(json.dumps({"status": "verified", "normal": normal["stdout"].strip(), "race": race["stdout"].strip(), "dependencyLines": len(deps["stdout"].splitlines())}, sort_keys=True))


def verify_wrong_oracle() -> None:
    result = run_bounded(["go", "test", "-count=1", "-timeout", "60s", "-run", "^TestPublicDeviceBoundary$", "./..."], cwd=consumer_dir(), timeout=90.0, env={"GOWORK": "off", "C51_WRONG_ORACLE": "1"})
    combined = result["stdout"] + result["stderr"]
    if result["returnCode"] == 0 or result["timedOut"] or result["processGroupAlive"]:
        die(f"wrong-consumption oracle unexpectedly passed or was unbounded: {result}")
    marker = "wrong consumption oracle: expected queue admission to equal callback consumption before Advance"
    if marker not in combined:
        die(f"wrong-consumption oracle failed without its deliberate marker: {result}")
    write_json(EVIDENCE_ROOT / "evidence/wrong-consumption-oracle.json", {"status": "rejected_as_expected", "marker": marker, "result": result})
    print(json.dumps({"status": "verified", "expectedFailure": True, "marker": marker}, sort_keys=True))


def wav_summary(path: Path) -> dict:
    raw = path.read_bytes()
    with wave.open(str(path), "rb") as stream:
        pcm = stream.readframes(stream.getnframes())
        return {
            "fileBytes": len(raw),
            "pcmBytes": len(pcm),
            "wavSHA256": sha256_bytes(raw),
            "pcmSHA256": sha256_bytes(pcm),
            "channels": stream.getnchannels(),
            "sampleWidth": stream.getsampwidth(),
            "sampleRate": stream.getframerate(),
            "frames": stream.getnframes(),
        }


def remaining_budget(deadline: float, label: str) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        die(f"aggregate {label} deadline exhausted")
    return remaining


def verify_shipped_regression(yui: str, build_manifest: str, child_timeout: float = 60.0, total_timeout: float = 600.0) -> None:
    binary = Path(yui).resolve()
    if not binary.is_file():
        die(f"shipped yui binary is missing: {binary}")
    provenance = verify_provenance()
    pinned_build = verify_build_manifest(binary, Path(build_manifest).resolve())
    expected_manifest_path = (OWNED_REL / "evidence/shipped-build.json").as_posix()
    if Path(build_manifest).resolve() != (REPO_ROOT / expected_manifest_path).resolve():
        die(f"shipped regression must use the admitted build manifest {expected_manifest_path}")
    fixture = REPO_ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
    tool_fixture = REPO_ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json"
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c51-") as temporary:
        temp = Path(temporary)
        disk_before = directory_size(temp)
        deadline = time.monotonic() + total_timeout
        output = temp / "healthy.wav"
        healthy = run_bounded(
            [str(binary), "--config-dir", str(temp / "config"), "session", "--replay", str(fixture), "--audio-out", str(output)],
            timeout=min(child_timeout, 60.0),
            deadline=deadline,
            output_limit=MAX_OUTPUT,
            disk_root=temp,
            disk_limit=MAX_RUN_DISK_BYTES,
            file_limit=MAX_WAV_BYTES,
        )
        if healthy["returnCode"] != 0 or healthy["timedOut"] or healthy["processGroupAlive"] or healthy["diskLimitExceeded"] or healthy["fileLimitBytes"] != MAX_WAV_BYTES:
            die(f"shipped healthy audio replay failed: {healthy}")
        if not output.is_file():
            die("shipped healthy audio replay did not write WAV output")
        if output.stat().st_size > MAX_WAV_BYTES:
            die(f"healthy replay WAV exceeded {MAX_WAV_BYTES}-byte bound")
        healthy_wav = wav_summary(output)
        expected_wav = {
            "pcmSHA256": "df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119",
            "wavSHA256": "3e9bdf4cffd0b09b1ce550cf199542cd54fdaf69bf4f53d2e1b8be88c02f5aa3",
            "pcmBytes": 4,
            "channels": 1,
            "sampleWidth": 2,
            "sampleRate": 16000,
            "frames": 2,
        }
        if any(healthy_wav.get(key) != value for key, value in expected_wav.items()):
            die(f"shipped healthy audio PCM/WAV oracle changed: got {healthy_wav}, want {expected_wav}")
        lifecycle_markers = [
            "Assistant: Hello thereSecond turn reply",
            "[session closed: healthy_complete]",
            "[session terminal: classification=transport terminal_reason=provider_close terminal_provenance=provider output_state=not_applicable]",
        ]
        for marker in lifecycle_markers:
            if marker not in healthy["stdout"]:
                die(f"shipped replay missing lifecycle marker {marker!r}: {healthy['stdout']}")

        tool_output = temp / "tool.wav"
        tool = run_bounded(
            [str(binary), "--config-dir", str(temp / "tool-config"), "session", "--replay", str(tool_fixture), "--audio-out", str(tool_output)],
            timeout=min(child_timeout, 60.0, remaining_budget(deadline, "shipped replay")),
            deadline=deadline,
            output_limit=MAX_OUTPUT,
            disk_root=temp,
            disk_limit=MAX_RUN_DISK_BYTES,
            file_limit=MAX_WAV_BYTES,
        )
        tool_combined = tool["stdout"] + tool["stderr"]
        tool_marker = "tool results were not delivered for 1 unresolved call(s): call_weather_001"
        if tool["returnCode"] == 0 or tool["timedOut"] or tool["processGroupAlive"] or tool["diskLimitExceeded"] or tool["fileLimitBytes"] != MAX_WAV_BYTES or tool_marker not in tool_combined:
            die(f"shipped tool lifecycle negative control changed: {tool}")
        if tool_output.is_file() and tool_output.stat().st_size > MAX_WAV_BYTES:
            die(f"tool negative-control WAV exceeded {MAX_WAV_BYTES}-byte bound")
        disk_after, disk_delta = assert_disk_budget(temp, disk_before, MAX_RUN_DISK_BYTES, "shipped replay temporary outputs")
        report = {
            "status": "verified",
            "classification": "SOFTWARE_REPLAY_PROCESS_ONLY",
            "sourceRevision": SOURCE_REVISION,
            "testedSourceRevision": pinned_build["testedRevision"],
            "candidateHeadAtVerification": git("rev-parse", "HEAD"),
            "executableInputEquivalence": {
                "testedRevision": pinned_build["testedRevision"],
                "candidateRevision": git("rev-parse", "HEAD"),
                "inputTreeSha256": pinned_build["inputTreeSha256"],
                "identical": True,
                "basis": "build-input manifest compared every tracked file under the local yui workspace roots, including Go, module, embedded JSON and local JS inputs; only owned evidence descendants differ",
            },
            "provenance": {
                "path": (OWNED_REL / "provenance.json").as_posix(),
                "sha256": sha256_file(EVIDENCE_ROOT / "provenance.json"),
                "cleanCandidateSHA": provenance["source"]["cleanCandidateSHA"],
                "fetchedMainRevision": provenance["source"]["fetchedMain"]["revision"],
            },
            "buildManifest": {
                "path": str(Path(build_manifest).resolve()),
                "sha256": sha256_file(Path(build_manifest).resolve()),
                "testedRevision": pinned_build["testedRevision"],
                "sourceRevision": pinned_build["sourceRevision"],
                "inputTreeSha256": pinned_build["inputTreeSha256"],
                "inputSelection": pinned_build["inputSelection"],
                "inputCount": len(pinned_build["inputs"]),
                "nonGoInputs": pinned_build["nonGoInputs"],
                "toolchain": pinned_build["toolchain"],
                "replayFixtures": pinned_build["replayFixtures"],
            },
            "binary": {"path": str(binary), "sha256": sha256_file(binary), "bytes": binary.stat().st_size},
            "limits": {
                "childTimeoutSeconds": child_timeout,
                "aggregateTimeoutSeconds": total_timeout,
                "outputBytesPerStream": MAX_OUTPUT,
                "wavBytes": MAX_WAV_BYTES,
                "ownedTemporaryGrowthBytes": MAX_RUN_DISK_BYTES,
                "aggregateDeadlineEnforced": True,
            },
            "temporaryOutput": {"bytesBefore": disk_before, "bytesAfter": disk_after, "bytesDelta": disk_delta},
                "healthyReplay": {"fixture": str(fixture.relative_to(REPO_ROOT)), "result": healthy, "wav": healthy_wav, "expectedWav": expected_wav, "markers": lifecycle_markers},
            "toolLifecycleNegativeControl": {"fixture": str(tool_fixture.relative_to(REPO_ROOT)), "result": tool, "expectedMarker": tool_marker, "audioOutputWritten": tool_output.is_file()},
        }
    write_json(EVIDENCE_ROOT / "evidence/shipped-regression.json", report)
    print(json.dumps({"status": "verified", "binarySHA256": report["binary"]["sha256"], "healthyPCM": healthy_wav["pcmSHA256"], "toolNegativeControl": True}, sort_keys=True))


def verify_runner_negative_controls(child_timeout: float = 60.0, total_timeout: float = 600.0) -> None:
    deadline = time.monotonic() + total_timeout
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c51-runner-") as temporary:
        temp = Path(temporary)
        disk_before = directory_size(temp)
        timeout_control = run_bounded(
            [sys.executable, "-c", "import time; print('C51_TIMEOUT_CONTROL', flush=True); time.sleep(5)"],
            timeout=min(0.25, child_timeout),
            deadline=deadline,
            output_limit=1024,
            disk_root=temp,
            disk_limit=MAX_RUN_DISK_BYTES,
        )
        if not timeout_control["timedOut"] or timeout_control["processGroupAlive"] or "C51_TIMEOUT_CONTROL" not in timeout_control["stdout"]:
            die(f"bounded timeout/reap control failed: {timeout_control}")
        overflow_control = run_bounded(
            [sys.executable, "-c", "import sys; sys.stdout.write('x' * 10000)"],
            timeout=min(5.0, child_timeout, remaining_budget(deadline, "runner controls")),
            deadline=deadline,
            output_limit=1024,
            disk_root=temp,
            disk_limit=MAX_RUN_DISK_BYTES,
        )
        if overflow_control["returnCode"] != 0 or not overflow_control["stdoutTruncated"] or overflow_control["processGroupAlive"]:
            die(f"bounded output control failed: {overflow_control}")
        disk_limit_control = 256 * 1024
        disk_overflow_script = "import os, pathlib, sys, time; root=pathlib.Path(sys.argv[1]); fd=os.open(root / 'overflow.bin', os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0o600); chunk=b'x' * 65536; [(os.write(fd, chunk), time.sleep(0.005)) for _ in range(1024)]; os.close(fd); pathlib.Path(root / 'complete').write_text('unexpected completion')"
        disk_overflow = run_bounded(
            [sys.executable, "-c", disk_overflow_script, str(temp)],
            timeout=min(10.0, child_timeout, remaining_budget(deadline, "disk bound")),
            deadline=deadline,
            output_limit=1024,
            disk_root=temp,
            disk_limit=disk_limit_control,
            file_limit=MAX_RUN_DISK_BYTES,
        )
        if not disk_overflow["diskLimitExceeded"] or disk_overflow["timedOut"] or disk_overflow["processGroupAlive"] or (temp / "complete").exists():
            die(f"live disk bound control failed: {disk_overflow}")
        parent_exit_pid_file = temp / "parent-exit-child.pid"
        parent_exit_script = "import pathlib, subprocess, sys; child=subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)']); pathlib.Path(sys.argv[1]).write_text(str(child.pid))"
        parent_exit = run_bounded(
            [sys.executable, "-c", parent_exit_script, str(parent_exit_pid_file)],
            timeout=min(5.0, child_timeout, remaining_budget(deadline, "parent exit cleanup")),
            deadline=deadline,
            output_limit=1024,
            disk_root=temp,
            disk_limit=MAX_RUN_DISK_BYTES,
        )
        if parent_exit["returnCode"] != 0 or parent_exit["timedOut"] or parent_exit["processGroupAlive"]:
            die(f"parent-exit descendant cleanup control failed: {parent_exit}")
        if not parent_exit_pid_file.is_file():
            die("parent-exit descendant control did not create its PID marker")
        parent_exit_pid = int(parent_exit_pid_file.read_text(encoding="utf-8"))
        if not wait_for_pid_exit(parent_exit_pid):
            die(f"parent-exit cleanup left descendant alive: pid={parent_exit_pid}")
        descendant_pid_file = temp / "child.pid"
        descendant_script = "import pathlib, subprocess, sys, time; child=subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)']); pathlib.Path(sys.argv[1]).write_text(str(child.pid)); time.sleep(30)"
        reader_failure = {"status": "caught", "error": "injected output-reader start failure"}
        try:
            run_bounded(
                [sys.executable, "-c", descendant_script, str(descendant_pid_file)],
                timeout=min(5.0, child_timeout, remaining_budget(deadline, "reader cleanup")),
                deadline=deadline,
                output_limit=1024,
                disk_root=temp,
                disk_limit=MAX_RUN_DISK_BYTES,
                inject_reader_start_failure=True,
            )
            die("injected reader-start failure unexpectedly returned")
        except RuntimeError as exc:
            if str(exc) != "injected output-reader start failure":
                raise
            reader_failure["error"] = str(exc)
        if not descendant_pid_file.is_file():
            die("reader-start failure control did not create its descendant marker")
        descendant_pid = int(descendant_pid_file.read_text(encoding="utf-8"))
        if not wait_for_pid_exit(descendant_pid):
            die(f"reader-start failure left descendant alive: pid={descendant_pid}")
        disk_after, disk_delta = assert_disk_budget(temp, disk_before, MAX_RUN_DISK_BYTES, "runner control temporary outputs")
        report = {
            "status": "verified",
            "schema": "audio-runtime-c51.runner-negative-controls.v2",
            "limits": {
                "childTimeoutSeconds": child_timeout,
                "aggregateTimeoutSeconds": total_timeout,
                "outputBytesPerStream": 1024,
                "diskOverflowControlBytes": disk_limit_control,
                "perFileBytes": MAX_RUN_DISK_BYTES,
                "ownedTemporaryGrowthBytes": MAX_RUN_DISK_BYTES,
                "aggregateDeadlineEnforced": True,
            },
            "timeoutAndReap": timeout_control,
            "outputBound": overflow_control,
            "liveDiskBound": {"result": disk_overflow, "completedMarkerPresent": (temp / "complete").exists()},
            "parentExitDescendantGroupCleanup": {"result": parent_exit, "descendantPID": parent_exit_pid, "descendantAliveAfterCleanup": False},
            "readerStartFailureGroupCleanup": {**reader_failure, "descendantPID": descendant_pid, "descendantAliveAfterCleanup": False},
            "ownedDisk": {"bytesBefore": disk_before, "bytesAfter": disk_after, "bytesDelta": disk_delta},
        }
    write_json(EVIDENCE_ROOT / "evidence/runner-negative-controls.json", report)
    print(json.dumps({"status": "verified", "timeoutReaped": True, "outputBounded": True, "diskBounded": True, "parentExitDescendantReaped": True, "readerFailureDescendantReaped": True}, sort_keys=True))


def verify_repairs() -> None:
    repairs = read_json(EVIDENCE_ROOT / "repair-candidates.json")
    if repairs.get("sourceRevision") != SOURCE_REVISION:
        die("repair-candidates source revision mismatch")
    for candidate in repairs.get("candidates", []):
        paths = candidate.get("paths", [])
        if not paths:
            die(f"repair candidate {candidate.get('id')} has no exact path")
        for path in paths:
            if path.startswith("go-audio/") or path.startswith("go-device-gateway/") or path.startswith("go-agent-runtime/") or path.startswith("go-agent-loop/") or path.startswith("go-llm-gateway/") or path.startswith("agent-cli/"):
                die(f"repair candidate proposes an unauthorized production path: {path}")
        if candidate.get("status") not in {"none", "deferred", "out_of_scope"}:
            die(f"repair candidate {candidate.get('id')} is not explicitly deferred/absent")
        if not candidate.get("evidence") or not candidate.get("owner"):
            die(f"repair candidate {candidate.get('id')} lacks evidence/owner disposition")
    print(json.dumps({"status": "verified", "candidateCount": len(repairs.get("candidates", [])), "readyCandidates": [c["id"] for c in repairs.get("candidates", []) if c.get("status") == "ready"]}, sort_keys=True))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["generate", "provenance", "imports", "boundaries", "boundary-negative-controls", "consumption-levels", "consumer", "wrong-consumption-oracle", "write-build-manifest", "shipped-regression", "runner-negative-controls", "repair-candidates"])
    parser.add_argument("--binary", "--yui", dest="yui", help="exact shipped yui binary for --mode shipped-regression")
    parser.add_argument("--build-manifest", default=str(EVIDENCE_ROOT / "evidence/shipped-build.json"), help="pinned build-input manifest for shipped-regression")
    parser.add_argument("--child-timeout", type=float, default=60.0, help="maximum seconds for one child process")
    parser.add_argument("--total-timeout", type=float, default=600.0, help="maximum seconds for the aggregate regression")
    args = parser.parse_args()
    if args.mode == "generate":
        mode_generate()
    elif args.mode == "provenance":
        verify_provenance()
    elif args.mode == "imports":
        verify_imports()
    elif args.mode == "boundaries":
        verify_boundaries()
    elif args.mode == "boundary-negative-controls":
        verify_boundary_negative_controls()
    elif args.mode == "consumption-levels":
        verify_consumption_levels()
    elif args.mode == "consumer":
        verify_consumer()
    elif args.mode == "wrong-consumption-oracle":
        verify_wrong_oracle()
    elif args.mode == "write-build-manifest":
        if not args.yui:
            die("--binary is required for write-build-manifest")
        write_build_manifest(args.yui)
    elif args.mode == "shipped-regression":
        if not args.yui:
            die("--yui is required for shipped-regression")
        verify_shipped_regression(args.yui, args.build_manifest, args.child_timeout, args.total_timeout)
    elif args.mode == "runner-negative-controls":
        verify_runner_negative_controls(args.child_timeout, args.total_timeout)
    elif args.mode == "repair-candidates":
        verify_repairs()


if __name__ == "__main__":
    main()
