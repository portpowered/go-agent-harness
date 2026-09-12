#!/usr/bin/env python3
"""Bounded C68 comparison driver for the preserved C23/C56 audio paths.

This driver is intentionally evidence-only.  It archives the two admitted
source revisions into task-local scratch space, builds the unchanged C23
public consumer against each source, runs exactly one recording-off and one
recording-on turn per source, and records every causal boundary.  It never
edits production source, C23, or C56 predecessor paths.
"""

from __future__ import annotations

import argparse
import base64
from contextlib import suppress
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import struct
import subprocess
import sys
import tarfile
import threading
import time
from typing import Any, Iterable


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=OWNED_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
OWNED_REPO_PATH = Path("docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution")
TASK = "audio-runtime-c68-c23-zero-audio-attribution"
PROJECT = "audio-runtime"
BRANCH = "codex/audio-runtime-c68-c23-zero-audio-attribution"
CURRENT_MAIN = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
DEFAULT_C23_REVISION = "b2fb41401cd0934378b9ff1bc131532fdc19f614"
DEFAULT_C56_REVISION = "85710a53a2e9449884fb81f979df0599269ef3b3"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
C23_TASK_PATH = "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization"
C56_TASK_PATH = "docs/temp/projects/audio-runtime/audio-runtime-c56-retire-cli-recording-orchestration"
INPUT_C23 = OWNED_ROOT / "inputs" / "c23-task"
INPUT_C56 = OWNED_ROOT / "inputs" / "c56-task"
FIXTURE_PATH = INPUT_C23 / "fixtures.json"
PROVENANCE_PATH = OWNED_ROOT / "provenance.json"
BUILD_PATH = OWNED_ROOT / "build-manifest.json"
COMPARISON_PATH = OWNED_ROOT / "comparison.json"
NEGATIVE_PATH = OWNED_ROOT / "negative-control.json"
REGRESSIONS_PATH = OWNED_ROOT / "c21-regressions.json"
FIRST_FAILURE_PATH = OWNED_ROOT / "artifacts" / "first-failures" / "recording-observer-admission.json"
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_FIXTURE_BYTES = 2 * 1024 * 1024
MAX_POSITIVE_RUNS = 4
CHILD_TIMEOUT_MAX = 60.0
TOTAL_TIMEOUT_MAX = 300.0
RECORDING_MODES = ("off", "on")
SOURCE_FILES_FOR_CAUSAL_TRACE = (
    "go-agent-runtime/services/session/internal/live/service.go",
    "go-agent-runtime/services/session/internal/live/lifecycle.go",
    "go-agent-runtime/services/session/internal/live/session_adapter.go",
    "go-agent-runtime/services/session/internal/live/observations/observer.go",
    "go-agent-runtime/services/recording/internal/evidence/directory.go",
)


class VerificationError(RuntimeError):
    """A bounded evidence precondition or causal assertion failed."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def canonical_digest(value: Any) -> str:
    return sha256_bytes(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode())


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n")


def load_json(path: Path) -> dict[str, Any]:
    require(path.is_file(), f"missing JSON evidence: {owned_rel(path)}")
    try:
        value = json.loads(path.read_text())
    except json.JSONDecodeError as error:
        raise VerificationError(f"invalid JSON evidence {owned_rel(path)}: {error}") from error
    require(isinstance(value, dict), f"JSON evidence is not an object: {owned_rel(path)}")
    return value


def owned_rel(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(OWNED_ROOT.resolve()))
    except ValueError as error:
        raise VerificationError(f"path escaped C68 owned root: {path}") from error


def repo_rel(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(REPO_ROOT.resolve()))
    except ValueError as error:
        raise VerificationError(f"path escaped repository root: {path}") from error


def run_command(command: list[str], *, cwd: Path = REPO_ROOT, timeout: float = 60.0) -> subprocess.CompletedProcess[str]:
    try:
        result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, check=False, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        raise VerificationError(f"command exceeded {timeout:g}s: {' '.join(command)}") from error
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip().replace("\x00", " ")
        raise VerificationError(f"command failed ({result.returncode}): {' '.join(command)}: {detail[:3000]}")
    return result


def git_value(*args: str) -> str:
    return run_command(["git", *args]).stdout.strip()


def git_is_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(
        ["git", "merge-base", "--is-ancestor", ancestor, descendant],
        cwd=REPO_ROOT,
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    ).returncode == 0


def current_status() -> list[str]:
    return git_value("status", "--porcelain", "--untracked-files=all").splitlines()


def status_path(line: str) -> str:
    value = line[3:] if len(line) >= 3 else line
    if " -> " in value:
        value = value.split(" -> ", 1)[1]
    return value


def assert_scope() -> None:
    branch = git_value("branch", "--show-current")
    require(branch == BRANCH, f"isolated branch {branch!r} does not match admitted PRD branch {BRANCH!r}")
    for entry in current_status():
        path = status_path(entry)
        require(
            path == str(OWNED_REPO_PATH) or path.startswith(str(OWNED_REPO_PATH) + "/"),
            f"unrelated worktree change is present; preserving it and stopping: {path}",
        )
    require(git_value("diff", "--check") == "", "worktree has whitespace errors")


def assert_commit(revision: str) -> None:
    require(len(revision) == 40, f"revision is not a full SHA: {revision}")
    result = subprocess.run(["git", "cat-file", "-e", revision + "^{commit}"], cwd=REPO_ROOT, check=False)
    require(result.returncode == 0, f"revision is not available as a commit: {revision}")


def fixture_value() -> dict[str, Any]:
    raw = FIXTURE_PATH.read_bytes()
    require(len(raw) <= MAX_FIXTURE_BYTES, "fixture exceeds bounded fixture volume")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as error:
        raise VerificationError(f"invalid fixture JSON: {error}") from error
    require(isinstance(value, dict), "fixture must be a JSON object")
    return value


def fixture_digest() -> str:
    return canonical_digest(fixture_value())


def source_task_manifest(revision: str, source_path: str, destination: Path) -> list[dict[str, Any]]:
    names = run_command(["git", "ls-tree", "-r", "--name-only", revision, "--", source_path]).stdout.splitlines()
    require(names, f"source task path is empty at {revision}: {source_path}")
    result: list[dict[str, Any]] = []
    prefix = source_path.rstrip("/") + "/"
    for name in names:
        require(name.startswith(prefix), f"unexpected source task path entry: {name}")
        relative = name[len(prefix) :]
        target = destination / relative
        require(target.is_file(), f"copied predecessor evidence is missing: {owned_rel(target)}")
        expected = subprocess.run(["git", "show", f"{revision}:{name}"], cwd=REPO_ROOT, check=True, capture_output=True).stdout
        actual = target.read_bytes()
        require(actual == expected, f"copied predecessor evidence changed: {owned_rel(target)}")
        result.append({"path": relative, "bytes": len(actual), "sha256": sha256_bytes(actual)})
    return result


def git_blob_sha(revision: str, path: str) -> str:
    data = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=REPO_ROOT, check=True, capture_output=True).stdout
    return sha256_bytes(data)


def archive_source(revision: str, label: str) -> dict[str, Any]:
    scratch = OWNED_ROOT / "scratch" / "sources"
    archive_dir = OWNED_ROOT / "scratch" / "archives"
    source_root = scratch / label
    archive_path = archive_dir / f"{label}-{revision}.tar"
    source_root.mkdir(parents=True, exist_ok=True)
    archive_dir.mkdir(parents=True, exist_ok=True)
    marker = source_root / ".extracted-revision"
    if marker.is_file() and marker.read_text().strip() == revision and (source_root / "go.work").is_file():
        archive_hash = sha256_file(archive_path) if archive_path.is_file() else ""
    else:
        if archive_path.exists():
            archive_path.unlink()
        with archive_path.open("wb") as output:
            result = subprocess.run(["git", "archive", "--format=tar", revision], cwd=REPO_ROOT, stdout=output, stderr=subprocess.PIPE, check=False, text=False)
        require(result.returncode == 0, f"git archive failed for {revision}: {result.stderr.decode(errors='replace')[:2000]}")
        if source_root.exists():
            for child in source_root.iterdir():
                if child.name != ".extracted-revision":
                    if child.is_dir() and not child.is_symlink():
                        shutil.rmtree(child)
                    else:
                        child.unlink()
        source_root.mkdir(parents=True, exist_ok=True)
        with tarfile.open(archive_path, "r") as archive:
            for member in archive:
                target = (source_root / member.name).resolve()
                require(target == source_root.resolve() or source_root.resolve() in target.parents, f"unsafe archive member: {member.name}")
                archive.extract(member, source_root)
        marker.write_text(revision + "\n")
        archive_hash = sha256_file(archive_path)
    require((source_root / "go.work").is_file(), f"archived source has no go.work: {source_root}")
    return {
        "revision": revision,
        "archive": {"path": owned_rel(archive_path), "bytes": archive_path.stat().st_size, "sha256": archive_hash},
        "root": owned_rel(source_root),
        "go_work_sha256": sha256_file(source_root / "go.work"),
    }


def copy_build_main(source_meta: dict[str, Any], label: str) -> Path:
    source_root = OWNED_ROOT / source_meta["root"]
    destination = source_root / "__c68_input__" / label / "main.go"
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(INPUT_C23 / "consumer" / "main.go", destination)
    require(destination.is_file(), f"source consumer copy was not created: {destination}")
    return destination


def prepare(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    c23_revision = args.c23_revision
    c56_revision = args.c56_revision
    assert_commit(c23_revision)
    assert_commit(c56_revision)
    require(args.child_timeout_seconds > 0 and args.child_timeout_seconds <= CHILD_TIMEOUT_MAX, "child timeout must be within (0, 60]")
    require(args.total_timeout_seconds > 0 and args.total_timeout_seconds <= TOTAL_TIMEOUT_MAX, "total timeout must be within (0, 300]")
    head = git_value("rev-parse", "HEAD")
    origin_main = git_value("rev-parse", "origin/main")
    require(origin_main == CURRENT_MAIN, f"fetched origin/main moved from admitted current main: {origin_main}")
    require(git_is_ancestor(STARTUP_REVISION, head), "startup integration ancestry is missing")
    require(git_is_ancestor(CURRENT_MAIN, head), "current-main ancestry is missing")
    require(git_is_ancestor(BASELINE_REVISION, head), "recording baseline ancestry is missing")
    require(git_is_ancestor(c23_revision, c23_revision), "C23 revision is not self-available")
    c23_source = archive_source(c23_revision, "c23")
    c56_source = archive_source(c56_revision, "c56")
    c23_files = source_task_manifest(c23_revision, C23_TASK_PATH, INPUT_C23)
    c56_files = source_task_manifest(c56_revision, C56_TASK_PATH, INPUT_C56)
    causal = {
        "source_files": list(SOURCE_FILES_FOR_CAUSAL_TRACE),
        "c23_sha256": {path: git_blob_sha(c23_revision, path) for path in SOURCE_FILES_FOR_CAUSAL_TRACE},
        "c56_sha256": {path: git_blob_sha(c56_revision, path) for path in SOURCE_FILES_FOR_CAUSAL_TRACE},
        "shared_runtime_files_unchanged": {
            path: git_blob_sha(c23_revision, path) == git_blob_sha(c56_revision, path)
            for path in SOURCE_FILES_FOR_CAUSAL_TRACE
        },
        "source_observation": {
            "terminal_drain_wrapper": "service.go:338-362 wraps every provider session in terminalDrainSession",
            "empty_media_facade": "lifecycle.go:387-400 exposes MediaSession methods and returns empty endpoints when the inner provider has no media",
            "false_positive_media_attachment": "session_adapter.go:45-55 sees the wrapper as MediaSession; with no device requirements, satisfiedBy(empty) is true and onMediaAttached(true) runs",
            "recording_fallback_guard": "observations/observer.go:71-80 calls messageAudio only when mediaAttached is false",
            "recording_admission": "recording/internal/evidence/directory.go:160-207 drops zero-sample non-terminal frames and enqueues accepted PCM items",
        },
    }
    provenance = {
        "schema": "audio-runtime.c68.zero-audio-attribution.provenance.v1",
        "task": TASK,
        "project": PROJECT,
        "contract": "audio-runtime-v1",
        "branch": git_value("branch", "--show-current"),
        "head": head,
        "origin_main_after_fetch": origin_main,
        "current_main": CURRENT_MAIN,
        "startup_revision": STARTUP_REVISION,
        "baseline_revision": BASELINE_REVISION,
        "c23_revision": c23_revision,
        "c56_revision": c56_revision,
        "ancestry": {"startup": True, "current_main": True, "baseline": True},
        "fixture": {"path": owned_rel(FIXTURE_PATH), "bytes": FIXTURE_PATH.stat().st_size, "sha256": fixture_digest()},
        "inputs": {
            "c23": {"root": owned_rel(INPUT_C23), "files": c23_files, "manifest_sha256": canonical_digest(c23_files)},
            "c56": {"root": owned_rel(INPUT_C56), "files": c56_files, "manifest_sha256": canonical_digest(c56_files)},
        },
        "sources": {"c23": c23_source, "c56": c56_source},
        "causal_trace": causal,
        "bounds": {
            "child_timeout_seconds": args.child_timeout_seconds,
            "total_timeout_seconds": args.total_timeout_seconds,
            "max_child_output_bytes": MAX_CHILD_OUTPUT_BYTES,
            "positive_executions": MAX_POSITIVE_RUNS,
            "recording_modes": list(RECORDING_MODES),
            "no_realtime_or_hardware_claim": True,
        },
        "owned_paths_only": True,
        "production_source_changed": False,
    }
    write_json(PROVENANCE_PATH, provenance)
    print(json.dumps({"status": "prepared", "c23": c23_revision, "c56": c56_revision, "source_archives": True}, sort_keys=True))
    return provenance


def source_root(meta: dict[str, Any]) -> Path:
    root = OWNED_ROOT / meta["root"]
    require(root.is_dir(), f"missing extracted source root: {owned_rel(root)}")
    return root


def parse_json_stream(output: str) -> Iterable[dict[str, Any]]:
    decoder = json.JSONDecoder()
    cursor = 0
    while cursor < len(output):
        while cursor < len(output) and output[cursor].isspace():
            cursor += 1
        if cursor >= len(output):
            return
        value, cursor = decoder.raw_decode(output, cursor)
        if isinstance(value, dict):
            yield value


def build_input_manifest(root: Path, main: Path, timeout: float) -> list[dict[str, Any]]:
    paths: set[Path] = {main}
    output = run_command(["go", "list", "-deps", "-json", str(main)], cwd=root, timeout=timeout).stdout
    fields = (
        "GoFiles", "CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles", "SFiles",
        "SwigFiles", "SwigCXXFiles", "SysoFiles", "EmbedFiles",
    )
    for package in parse_json_stream(output):
        package_dir = Path(package.get("Dir", ""))
        for field in fields:
            for filename in package.get(field, []) or []:
                candidate = package_dir / filename
                try:
                    candidate.resolve().relative_to(root.resolve())
                except ValueError:
                    continue
                if candidate.is_file():
                    paths.add(candidate)
    for pattern in ("go.mod", "go.sum"):
        paths.update(root.rglob(pattern))
    if (root / "go.work").is_file():
        paths.add(root / "go.work")
    if (root / "go.work.sum").is_file():
        paths.add(root / "go.work.sum")
    result = []
    for path in sorted(paths):
        if not path.is_file():
            continue
        result.append({"path": str(path.resolve().relative_to(root.resolve())), "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    require(any(item["path"].endswith("main.go") for item in result), f"go build input manifest omitted consumer: {main}")
    return result


def build(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    provenance = load_json(PROVENANCE_PATH)
    require(provenance["c23_revision"] == args.c23_revision and provenance["c56_revision"] == args.c56_revision, "build revisions differ from prepared provenance")
    build_root = OWNED_ROOT / "scratch" / "build"
    build_root.mkdir(parents=True, exist_ok=True)
    records: dict[str, Any] = {}
    for label in ("c23", "c56"):
        meta = provenance["sources"][label]
        root = source_root(meta)
        main = copy_build_main(meta, label)
        binary = build_root / f"{label}-consumer"
        command = ["go", "build", "-trimpath", "-o", str(binary), str(main)]
        result = run_command(command, cwd=root, timeout=args.child_timeout_seconds)
        del result
        require(binary.is_file(), f"go build did not produce {label} consumer")
        inputs = build_input_manifest(root, main, args.child_timeout_seconds)
        records[label] = {
            "revision": meta["revision"],
            "source_root": owned_rel(root),
            "consumer_source": owned_rel(main),
            "consumer_source_sha256": sha256_file(INPUT_C23 / "consumer" / "main.go"),
            "binary": {"path": owned_rel(binary), "bytes": binary.stat().st_size, "sha256": sha256_file(binary)},
            "command": command,
            "build_inputs": inputs,
            "build_inputs_sha256": canonical_digest(inputs),
        }
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.build.v1",
        "task": TASK,
        "go_version": run_command(["go", "version"], timeout=args.child_timeout_seconds).stdout.strip(),
        "flags": ["-trimpath"],
        "consumer_source_sha256": sha256_file(INPUT_C23 / "consumer" / "main.go"),
        "fixture_sha256": fixture_digest(),
        "builds": records,
    }
    write_json(BUILD_PATH, value)
    print(json.dumps({"status": "built", "binaries": {key: value["binary"]["sha256"] for key, value in records.items()}}, sort_keys=True))
    return value


def sanitized_environment(source_revision: str, run_root: Path) -> dict[str, str]:
    allowed: dict[str, str] = {}
    sensitive = ("API_KEY", "TOKEN", "PASSWORD", "SECRET", "CREDENTIAL", "ACCESS_KEY")
    for key, value in os.environ.items():
        upper = key.upper()
        if any(marker in upper for marker in sensitive):
            continue
        if key in {"PATH", "SYSTEMROOT", "WINDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "GOROOT"}:
            allowed[key] = value
    home = run_root / "home"
    config = run_root / "config"
    home.mkdir(parents=True, exist_ok=True)
    config.mkdir(parents=True, exist_ok=True)
    allowed.update({
        "HOME": str(home), "USERPROFILE": str(home), "XDG_CONFIG_HOME": str(config),
        "LANG": "C", "LC_ALL": "C", "C68_SOURCE_REVISION": source_revision,
        # The preserved C23 consumer's provenance contract is intentionally
        # retained while it is executed against both exact source revisions.
        "C23_SOURCE_REVISION": source_revision,
    })
    # Keep the child HOME credential-free without forcing every Go regression
    # run to redownload the workspace toolchain and module graph.  These are
    # build caches only; no provider credentials are admitted.
    cache = subprocess.run(["go", "env", "GOMODCACHE", "GOCACHE"], capture_output=True, text=True, check=False)
    if cache.returncode == 0:
        values = cache.stdout.splitlines()
        if len(values) >= 2:
            allowed.update({"GOMODCACHE": values[0], "GOCACHE": values[1]})
    for key in ("GOTOOLCHAIN", "GOPROXY"):
        if os.environ.get(key):
            allowed[key] = os.environ[key]
    return allowed


def _read_pipe(stream: Any, retained: bytearray, observed: list[int], overflow: list[bool]) -> None:
    try:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            observed[0] += len(chunk)
            keep = MAX_CHILD_OUTPUT_BYTES - len(retained)
            if keep > 0:
                retained.extend(chunk[:keep])
            if observed[0] > MAX_CHILD_OUTPUT_BYTES:
                overflow[0] = True
    finally:
        with suppress(OSError):
            stream.close()


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except (ProcessLookupError, OSError):
        return False
    return True


def stop_group(process: subprocess.Popen[Any]) -> dict[str, Any]:
    before = group_alive(process.pid)
    term_sent = False
    kill_sent = False
    errors: list[str] = []
    if before:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            term_sent = True
        except ProcessLookupError:
            pass
        except OSError as error:
            errors.append(f"SIGTERM: {error}")
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
        if process.poll() is None and group_alive(process.pid):
            try:
                os.killpg(process.pid, signal.SIGKILL)
                kill_sent = True
            except ProcessLookupError:
                pass
            except OSError as error:
                errors.append(f"SIGKILL: {error}")
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
    if process.poll() is None:
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
    after = group_alive(process.pid)
    return {"group_alive_before": before, "group_alive_after": after, "term_sent": term_sent, "kill_sent": kill_sent, "parent_reaped": process.returncode is not None, "errors": errors}


def run_child(command: list[str], label: str, cwd: Path, source_revision: str, run_root: Path, timeout: float) -> dict[str, Any]:
    require(timeout > 0, f"{label} has no remaining child budget")
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path = run_root / "stdout.log"
    stderr_path = run_root / "stderr.log"
    started = time.monotonic()
    retained_out = bytearray()
    retained_err = bytearray()
    observed_out = [0]
    observed_err = [0]
    overflow_out = [False]
    overflow_err = [False]
    process = subprocess.Popen(
        command, cwd=cwd, env=sanitized_environment(source_revision, run_root),
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0, start_new_session=True,
    )
    out_thread = threading.Thread(target=_read_pipe, args=(process.stdout, retained_out, observed_out, overflow_out), daemon=True)
    err_thread = threading.Thread(target=_read_pipe, args=(process.stderr, retained_err, observed_err, overflow_err), daemon=True)
    out_thread.start()
    err_thread.start()
    deadline = time.monotonic() + timeout
    timed_out = False
    overflow = False
    while process.poll() is None:
        if overflow_out[0] or overflow_err[0]:
            overflow = True
            break
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            break
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=min(remaining, 0.05))
    cleanup = stop_group(process)
    out_thread.join(timeout=2)
    err_thread.join(timeout=2)
    stdout_path.write_bytes(retained_out)
    stderr_path.write_bytes(retained_err)
    return {
        "label": label, "command": command, "cwd": repo_rel(cwd), "source_revision": source_revision,
        "returncode": process.returncode, "timed_out": timed_out, "output_overflow": overflow or overflow_out[0] or overflow_err[0],
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": owned_rel(stdout_path), "stderr": owned_rel(stderr_path),
        "stdout_bytes": len(retained_out), "stderr_bytes": len(retained_err),
        "stdout_observed_bytes": observed_out[0], "stderr_observed_bytes": observed_err[0],
        "output_bounded": not (overflow or overflow_out[0] or overflow_err[0]),
        "cleanup": cleanup,
    }


def read_child_text(execution: dict[str, Any], stream: str) -> str:
    return (OWNED_ROOT / execution[stream]).read_text(errors="replace")[:MAX_CHILD_OUTPUT_BYTES]


def expected_pcm() -> bytes:
    fixture = fixture_value()
    count = int(fixture["audio"]["samples_per_turn"])
    return b"".join(struct.pack("<h", 100 + index) for index in range(count))


def recursive_dicts(value: Any) -> Iterable[dict[str, Any]]:
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from recursive_dicts(child)
    elif isinstance(value, list):
        for child in value:
            yield from recursive_dicts(child)


def first_string(value: Any, key: str) -> str | None:
    for item in recursive_dicts(value):
        found = item.get(key)
        if isinstance(found, str):
            return found
    return None


def provider_audio(artifact_root: Path) -> dict[str, Any]:
    candidates = sorted(artifact_root.rglob("provider.session.json"))
    require(candidates, f"provider capture was not finalized below {owned_rel(artifact_root)}")
    path = candidates[0]
    capture = json.loads(path.read_text())
    audio_records: list[bytes] = []
    # The capture stores the outer session record and the nested stream
    # message, both of which carry type=AUDIO.DELTA.  Count only the outer
    # records so one provider emission cannot be attributed twice.
    for item in capture.get("records", []) if isinstance(capture, dict) and isinstance(capture.get("records"), list) else []:
        if item.get("type") != "AUDIO.DELTA":
            continue
        content = first_string(item.get("payload", item), "content")
        if content is None:
            continue
        try:
            audio_records.append(base64.b64decode(content, validate=True))
        except (ValueError, base64.binascii.Error):
            audio_records.append(b"")
    data = b"".join(audio_records)
    return {
        "path": owned_rel(path), "audio_delta_records": len(audio_records), "nonempty_records": sum(bool(item) for item in audio_records),
        "bytes": len(data), "sha256": sha256_bytes(data) if data else sha256_bytes(b""),
        "nonempty": bool(data), "recorded_bytes": [len(item) for item in audio_records],
    }


def semantic_root(report: dict[str, Any], artifact_root: Path) -> Path | None:
    value = report.get("artifacts", {}).get("semantic_root") if isinstance(report.get("artifacts"), dict) else None
    if isinstance(value, str) and value:
        candidate = Path(value)
        if not candidate.is_absolute():
            candidate = REPO_ROOT / candidate
        if candidate.is_dir():
            return candidate
    candidate = artifact_root / "semantic"
    return candidate if candidate.is_dir() else None


def recording_usage(report: dict[str, Any]) -> dict[str, Any]:
    value = report.get("recording_usage")
    require(isinstance(value, dict), "consumer report omitted recording_usage")
    usage = value.get("usage")
    return {"record": value, "usage": usage if isinstance(usage, dict) else {}}


def manifest_artifacts(manifest: dict[str, Any]) -> list[str]:
    result: list[str] = []
    for item in manifest.get("artifacts", []) if isinstance(manifest.get("artifacts"), list) else []:
        if isinstance(item, dict) and isinstance(item.get("path"), str):
            result.append(item["path"])
        elif isinstance(item, str):
            result.append(item)
    return result


def execution_evidence(execution: dict[str, Any], report_path: Path, artifact_root: Path, source_label: str, mode: str) -> dict[str, Any]:
    require(execution["returncode"] == 0, f"{source_label}/{mode} child failed: {read_child_text(execution, 'stderr')[-2000:]}")
    require(not execution["timed_out"], f"{source_label}/{mode} child timed out")
    require(execution["output_bounded"], f"{source_label}/{mode} child exceeded output cap")
    require(execution["cleanup"]["parent_reaped"] and not execution["cleanup"]["group_alive_after"], f"{source_label}/{mode} child cleanup failed")
    report = load_json(report_path)
    require(report.get("schema") == "c23.v1", f"{source_label}/{mode} report schema mismatch")
    require(report.get("scenario") == "tool-matrix" and report.get("turns") == 1, f"{source_label}/{mode} was not the one-turn matrix")
    require(report.get("source_revision") == execution["source_revision"], f"{source_label}/{mode} source identity drifted")
    require(report.get("fixture_sha256") == fixture_digest(), f"{source_label}/{mode} fixture digest drifted")
    require(report.get("trace_complete") is True, f"{source_label}/{mode} trace is incomplete")
    require(report.get("events", {}).get("overflow_drops") == 0, f"{source_label}/{mode} public event trace overflowed")
    require(report.get("clean_shutdown") is True, f"{source_label}/{mode} was not a clean shutdown")
    # The preserved tool-matrix report does not populate the explicit
    # lifecycle struct; clean_shutdown and the terminal event are its
    # lifecycle evidence for this public RunLive path.  The interruption
    # scenario is the predecessor's explicit lifecycle probe and is outside
    # C68's four positive executions.
    expected = expected_pcm()
    pcm = report.get("pcm") if isinstance(report.get("pcm"), dict) else {}
    public_bytes = int(pcm.get("bytes", 0) or 0)
    public_sha = str(pcm.get("sha256", ""))
    public_ok = public_bytes == len(expected) and public_sha == sha256_bytes(expected) and int(pcm.get("sample_rate", 0) or 0) == int(fixture_value()["audio"]["sample_rate"])
    provider = provider_audio(artifact_root)
    provider_ok = provider["nonempty"] and provider["bytes"] == len(expected) and provider["audio_delta_records"] == 1
    usage_record = recording_usage(report)
    usage = usage_record["usage"]
    root = semantic_root(report, artifact_root)
    semantic: dict[str, Any] = {"enabled": mode == "on", "root": owned_rel(root) if root else None, "manifest": None, "declared_artifacts": [], "audio_path": None}
    if root:
        manifest_path = root / "manifest.json"
        semantic["manifest"] = owned_rel(manifest_path) if manifest_path.is_file() else None
        if manifest_path.is_file():
            manifest = load_json(manifest_path)
            semantic["declared_artifacts"] = manifest_artifacts(manifest)
        audio_path = root / "audio" / "out-000.pcm"
        semantic["audio_path"] = {"path": owned_rel(audio_path), "exists": audio_path.is_file(), "bytes": audio_path.stat().st_size if audio_path.is_file() else 0, "sha256": sha256_file(audio_path) if audio_path.is_file() else None}
        semantic["terminal"] = manifest.get("terminal") if root and isinstance(locals().get("manifest"), dict) else None
    recording_enabled = bool(usage_record["record"].get("enabled"))
    recording_available = bool(usage_record["record"].get("available"))
    recording_audio = int(usage.get("accepted_audio", 0) or 0)
    recording_audio_bytes = int(usage.get("audio_bytes", 0) or 0)
    drops = usage_record["record"].get("drops", {})
    drops_zero = all(int(value or 0) == 0 for value in drops.values())
    recording_ok = (mode == "off" and not recording_enabled) or (
        mode == "on" and recording_enabled and recording_available and recording_audio == 0 and recording_audio_bytes == 0
    )
    public_audio_delta_count = int(report.get("events", {}).get("by_kind", {}).get("AUDIO.DELTA", 0) or 0)
    terminal = report.get("terminal") if isinstance(report.get("terminal"), dict) else {}
    boundary_status = {
        "provider_capture": {**provider, "expected_bytes": len(expected), "ok": provider_ok},
        "public_pcm": {"format": pcm.get("format"), "bytes": public_bytes, "sha256": public_sha, "expected_bytes": len(expected), "expected_sha256": sha256_bytes(expected), "ok": public_ok},
        "recording_usage": {
            "enabled": recording_enabled, "available": recording_available,
            "accepted_audio": recording_audio, "audio_bytes": recording_audio_bytes,
            "accepted_items": int(usage.get("accepted_items", 0) or 0), "accepted_messages": int(usage.get("accepted_messages", 0) or 0), "accepted_events": int(usage.get("accepted_events", 0) or 0),
            "queue_items_final": int(usage.get("queue_items", 0) or 0), "queue_bytes_final": int(usage.get("queue_bytes", 0) or 0),
            "processed_items": int(usage.get("processed_items", 0) or 0), "drops": drops, "drops_zero": drops_zero,
            "ok": recording_ok,
        },
        "semantic_artifacts": semantic,
        "finalization": {
            "clean_shutdown": report.get("clean_shutdown"), "terminal": terminal,
            "manifest_present": bool(semantic.get("manifest")), "audio_present": bool(semantic.get("audio_path", {}).get("exists")) if isinstance(semantic.get("audio_path"), dict) else False,
            "ok": report.get("clean_shutdown") is True and bool(semantic.get("manifest")) if mode == "on" else True,
        },
    }
    require(provider_ok and public_ok and recording_ok and public_audio_delta_count == 1, f"{source_label}/{mode} causal boundaries did not match expected evidence")
    if mode == "on":
        require(semantic["manifest"] is not None, f"{source_label}/{mode} semantic manifest is missing")
        require(not semantic["audio_path"]["exists"], f"{source_label}/{mode} unexpectedly produced audio; attribution must be re-planned")
        require(boundary_status["recording_usage"]["queue_items_final"] == 0 and boundary_status["recording_usage"]["queue_bytes_final"] == 0, f"{source_label}/{mode} recording spool did not drain")
    return {
        "source": source_label, "mode": mode, "execution": execution, "report": owned_rel(report_path),
        "artifact_root": owned_rel(artifact_root), "boundaries": boundary_status,
        "public_event_audio_delta_count": public_audio_delta_count,
        "recording_report": report.get("recording_usage", {}),
    }


def run_matrix(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    build_manifest = load_json(BUILD_PATH)
    provenance = load_json(PROVENANCE_PATH)
    require(provenance["c23_revision"] == args.c23_revision and provenance["c56_revision"] == args.c56_revision, "comparison revisions differ from provenance")
    require(args.turns == 1, "C68 comparison is exactly one logical turn per execution")
    require(tuple(args.recording) == RECORDING_MODES, "C68 comparison recording modes must be exactly off,on")
    started = time.monotonic()
    executions: list[dict[str, Any]] = []
    evidence: list[dict[str, Any]] = []
    run_stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + f"-{time.time_ns() % 1_000_000_000:09d}"
    for source_label in ("c23", "c56"):
        source_revision = provenance[f"{source_label}_revision"]
        root = source_root(provenance["sources"][source_label])
        binary = OWNED_ROOT / build_manifest["builds"][source_label]["binary"]["path"]
        for mode in RECORDING_MODES:
            require(len(evidence) < MAX_POSITIVE_RUNS, "positive execution count exceeded four")
            artifact_root = OWNED_ROOT / "artifacts" / "runs" / f"{run_stamp}-{source_label}-{mode}"
            report_path = artifact_root / "report.json"
            command = [str(binary), "-scenario=tool-matrix", "-fixture=" + str(FIXTURE_PATH), "-turns=1", "-recording=" + ("true" if mode == "on" else "false"), "-artifact-root=" + str(artifact_root), "-output=" + str(report_path)]
            execution = run_child(command, f"{source_label}-{mode}", root, source_revision, artifact_root / "process", args.child_timeout_seconds)
            executions.append(execution)
            evidence.append(execution_evidence(execution, report_path, artifact_root, source_label, mode))
            require(time.monotonic() - started <= args.total_timeout_seconds, "C68 positive comparison exceeded aggregate timeout")
    require(len(evidence) == MAX_POSITIVE_RUNS, "comparison did not execute exactly four positive cases")
    by_source = {label: {item["mode"]: item for item in evidence if item["source"] == label} for label in ("c23", "c56")}
    for label in by_source:
        require(set(by_source[label]) == set(RECORDING_MODES), f"{label} is missing a recording mode")
        off = by_source[label]["off"]
        on = by_source[label]["on"]
        require(off["boundaries"]["public_pcm"]["sha256"] == on["boundaries"]["public_pcm"]["sha256"], f"{label} off/on public PCM differs")
        require(off["boundaries"]["provider_capture"]["sha256"] == on["boundaries"]["provider_capture"]["sha256"], f"{label} off/on provider PCM differs")
    classification = "C56_RECORDING_DEFECT"
    comparison = {
        "schema": "audio-runtime.c68.zero-audio-attribution.comparison.v1",
        "task": TASK, "c23_revision": args.c23_revision, "c56_revision": args.c56_revision,
        "turns": args.turns, "recording_modes": list(RECORDING_MODES), "positive_execution_count": len(evidence),
        "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000), "aggregate_timeout_seconds": args.total_timeout_seconds,
        "executions": evidence,
        "attribution": {
            "classification": classification,
            "negative_control_required": True,
            "first_divergent_boundary": "recording_observer_admission",
            "first_divergent_api": "session.LiveRecorder.RecordAudio is never invoked for normalized provider AUDIO.DELTA in this no-provider-media fixture",
            "first_divergent_path": "go-agent-runtime/services/session/internal/live/observations/observer.go:71-80, reached through terminalDrainSession capability façade at service.go:338-362 and lifecycle.go:387-400",
            "causal_detail": "terminalDrainSession always implements MediaSession; its empty endpoints satisfy no-device media requirements, so capturingInferencer calls SetMediaAttached(true), skipping Observer.messageAudio. The provider and public PCM boundaries remain non-empty, but recording accepted_audio/audio_bytes stay zero and finalization has no audio/out-000.pcm.",
            "smallest_existing_c56_executor_repair": "preserve provider-media capability absence at the terminalDrainSession/capturingInferencer handoff (service.go:338-362; lifecycle.go:387-400; session_adapter.go:45-55) so the public observer fallback reaches RecordAudio; this is outside C68 owned paths and is not a C23 fixture/oracle repair",
            "c23_runner_repair_needed": False,
            "c56_candidate_restores_audio": False,
            "exactly_one_classification": True,
        },
        "accepted_as_evidence": True,
        "ci_status": "not_polled; submit candidate to external script CI gate",
    }
    write_json(COMPARISON_PATH, comparison)
    # Preserve the first demonstrated causal failure as a stable pointer. The
    # full raw report and semantic tree remain under the corresponding run;
    # this small record keeps the handoff reviewable without duplicating them.
    first_failure = next(item for item in evidence if item["source"] == "c23" and item["mode"] == "on")
    write_json(FIRST_FAILURE_PATH, {
        "schema": "audio-runtime.c68.zero-audio-attribution.first-failure.v1",
        "boundary": "recording_observer_admission",
        "classification": classification,
        "source": "c23",
        "source_revision": args.c23_revision,
        "run": first_failure["artifact_root"],
        "report": first_failure["report"],
        "evidence": first_failure["boundaries"],
        "preserved": True,
    })
    print(json.dumps({"status": "compared", "positive_executions": len(evidence), "classification": classification}, sort_keys=True))
    return comparison


def make_negative_source() -> tuple[Path, str, str]:
    source = INPUT_C23 / "consumer" / "main.go"
    original = source.read_text()
    old = "messages.NewAudioDeltaValueWithMediaType(pcm, \"audio/pcm;rate=16000\")"
    new = "messages.NewAudioDeltaValueWithMediaType(pcm[:0], \"audio/pcm;rate=16000\")"
    require(original.count(old) == 1, "negative control could not identify exactly one provider audio emission")
    mutated = original.replace(old, new)
    negative = OWNED_ROOT / "consumer-negative" / "main.go"
    negative.parent.mkdir(parents=True, exist_ok=True)
    negative.write_text(mutated)
    return negative, sha256_file(source), sha256_file(negative)


def negative_control(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    require(args.mutation == "first-provider-audio", "C68 only admits the first-provider-audio negative mutation")
    provenance = load_json(PROVENANCE_PATH)
    build_manifest = load_json(BUILD_PATH)
    source_revision = provenance["c56_revision"]
    root = source_root(provenance["sources"]["c56"])
    negative, original_hash, negative_hash = make_negative_source()
    binary = OWNED_ROOT / "scratch" / "build" / "c56-negative-consumer"
    command = ["go", "build", "-trimpath", "-o", str(binary), str(root / "__c68_input__" / "negative" / "main.go")]
    negative_source = root / "__c68_input__" / "negative" / "main.go"
    negative_source.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(negative, negative_source)
    run_command(command, cwd=root, timeout=args.child_timeout_seconds)
    require(binary.is_file(), "negative control build did not produce a binary")
    artifact_root = OWNED_ROOT / "artifacts" / "negative-control" / time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    report_path = artifact_root / "report.json"
    child_command = [str(binary), "-scenario=tool-matrix", "-fixture=" + str(FIXTURE_PATH), "-turns=1", "-recording=false", "-artifact-root=" + str(artifact_root), "-output=" + str(report_path)]
    execution = run_child(child_command, "negative-first-provider-audio", root, source_revision, artifact_root / "process", args.child_timeout_seconds)
    require(execution["returncode"] != 0, "negative control unexpectedly passed malformed provider audio")
    require(report_path.is_file(), "negative control did not preserve its report")
    report = load_json(report_path)
    require(report.get("source_revision") == source_revision and report.get("fixture_sha256") == fixture_digest(), "negative control source or fixture identity drifted")
    pcm = report.get("pcm", {})
    provider = provider_audio(artifact_root)
    passed = int(pcm.get("bytes", 0) or 0) == 0 and not provider["nonempty"]
    require(passed, "negative control did not fail at the intended provider/public audio oracle boundary")
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.negative.v1", "task": TASK,
        "mutation": args.mutation, "positive_fixture_sha256": fixture_digest(),
        "original_consumer_sha256": original_hash, "mutated_consumer_sha256": negative_hash,
        "mutated_source": owned_rel(negative), "binary": {"path": owned_rel(binary), "sha256": sha256_file(binary), "bytes": binary.stat().st_size},
        "execution": execution, "report": owned_rel(report_path), "provider_capture": provider,
        "public_pcm": {"bytes": int(pcm.get("bytes", 0) or 0), "sha256": pcm.get("sha256"), "expected_rejection": True},
        "classification": "oracle_control_rejects_malformed_first_provider_audio",
        "passed": True,
    }
    write_json(NEGATIVE_PATH, value)
    print(json.dumps({"status": "negative-control-passed", "mutation": args.mutation, "returncode": execution["returncode"]}, sort_keys=True))
    return value


def c21_regressions(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    pattern = "^TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls$"
    package = "./test/integration"
    cwd = REPO_ROOT / "agent-cli"
    normal_root = OWNED_ROOT / "artifacts" / "c21" / "normal"
    race_root = OWNED_ROOT / "artifacts" / "c21" / "race"
    normal_cmd = ["go", "test", package, "-run", pattern, "-count=5", "-timeout=60s"]
    race_cmd = ["go", "test", "-race", package, "-run", pattern, "-count=1", "-timeout=60s"]
    normal = run_child(normal_cmd, "c21-normal-count-5", cwd, git_value("rev-parse", "HEAD"), normal_root, args.child_timeout_seconds)
    race = run_child(race_cmd, "c21-race-count-1", cwd, git_value("rev-parse", "HEAD"), race_root, args.child_timeout_seconds)
    for label, result, marker in (("normal", normal, "ok"), ("race", race, "ok")):
        require(result["returncode"] == 0 and not result["timed_out"] and result["output_bounded"], f"C21 {label} regression failed: {read_child_text(result, 'stderr')[-2000:]}")
        require(result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"], f"C21 {label} cleanup failed")
        del marker
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.c21.v1", "task": TASK,
        "source_revision": git_value("rev-parse", "HEAD"), "test": "TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls",
        "normal": normal, "race": race, "normal_count": 5, "race_count": 1, "passed": True,
        "scope": "focused accumulated C21 normal/race regression only",
    }
    write_json(REGRESSIONS_PATH, value)
    print(json.dumps({"status": "c21-regressions-passed", "normal_count": 5, "race_count": 1}, sort_keys=True))
    return value


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    def revisions(command_parser: argparse.ArgumentParser) -> None:
        command_parser.add_argument("--c23-revision", default=DEFAULT_C23_REVISION)
        command_parser.add_argument("--c56-revision", default=DEFAULT_C56_REVISION)
        command_parser.add_argument("--child-timeout-seconds", type=float, default=60.0)
        command_parser.add_argument("--total-timeout-seconds", type=float, default=300.0)

    prepare_parser = sub.add_parser("prepare")
    revisions(prepare_parser)
    build_parser = sub.add_parser("build")
    revisions(build_parser)
    negative_parser = sub.add_parser("negative-control")
    revisions(negative_parser)
    negative_parser.add_argument("--mutation", required=True)
    compare_parser = sub.add_parser("compare")
    revisions(compare_parser)
    compare_parser.add_argument("--turns", type=int, required=True)
    compare_parser.add_argument("--recording", nargs="+", required=True)
    c21_parser = sub.add_parser("c21-regressions")
    revisions(c21_parser)

    args = parser.parse_args()
    try:
        if args.command == "prepare":
            prepare(args)
        elif args.command == "build":
            build(args)
        elif args.command == "negative-control":
            negative_control(args)
        elif args.command == "compare":
            run_matrix(args)
        elif args.command == "c21-regressions":
            c21_regressions(args)
        else:
            raise VerificationError(f"unsupported command: {args.command}")
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "error": str(error)}, sort_keys=True), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
