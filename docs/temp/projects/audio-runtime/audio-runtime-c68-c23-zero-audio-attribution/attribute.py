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
ATTRIBUTION_PATH = OWNED_ROOT / "attribution.json"
COMPARISON_LEDGER_PATH = OWNED_ROOT / "comparison-runs.json"
COMPARISON_LOCK_PATH = OWNED_ROOT / "artifacts" / ".comparison.lock"
NEGATIVE_PATH = OWNED_ROOT / "negative-control.json"
REGRESSIONS_PATH = OWNED_ROOT / "c21-regressions.json"
CLEANUP_PATH = OWNED_ROOT / "cleanup-control.json"
FIRST_FAILURE_PATH = OWNED_ROOT / "artifacts" / "first-failures" / "recording-observer-admission.json"
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_FIXTURE_BYTES = 2 * 1024 * 1024
MAX_RUN_ARTIFACT_BYTES = 4 * 1024 * 1024
MAX_RETAINED_ARTIFACT_BYTES = 64 * 1024 * 1024
MAX_RETAINED_ARTIFACT_FILES = 4096
MAX_POSITIVE_RUNS = 4
CHILD_TIMEOUT_MAX = 60.0
TOTAL_TIMEOUT_MAX = 300.0
UNBOUND_SOURCE_REVISION = "unbound-working-tree"
NEGATIVE_MUTATION_OLD = 'messages.NewAudioDeltaValueWithMediaType(pcm, "audio/pcm;rate=16000")'
NEGATIVE_MUTATION_NEW = 'messages.NewAudioDeltaValueWithMediaType(pcm[:0], "audio/pcm;rate=16000")'
_GIT_ARCHIVE_HASHES: dict[str, str] = {}
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


def owned_path(value: str | Path) -> Path:
    if not isinstance(value, (str, Path)):
        raise VerificationError(f"owned path is malformed: {value!r}")
    candidate = Path(value)
    if not candidate.is_absolute():
        candidate = OWNED_ROOT / candidate
    owned_rel(candidate)
    return candidate


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
    # Do not use git_value here: its .strip() would remove the first
    # porcelain status column from the first changed path.
    return run_command(["git", "status", "--porcelain", "--untracked-files=all"]).stdout.splitlines()


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


def validate_frozen_inputs(provenance: dict[str, Any]) -> None:
    """Recheck the committed predecessor copies before every derived run."""
    for label, source_path, input_path in (
        ("c23", C23_TASK_PATH, INPUT_C23),
        ("c56", C56_TASK_PATH, INPUT_C56),
    ):
        revision = provenance.get(f"{label}_revision")
        require(isinstance(revision, str) and len(revision) == 40, f"{label} predecessor revision is not bound")
        recorded = provenance.get("inputs", {}).get(label, {})
        expected_files = recorded.get("files") if isinstance(recorded, dict) else None
        require(isinstance(expected_files, list), f"{label} predecessor manifest is missing")
        actual_files = source_task_manifest(revision, source_path, input_path)
        require(actual_files == expected_files, f"{label} predecessor evidence copy changed")
        require(canonical_digest(actual_files) == recorded.get("manifest_sha256"), f"{label} predecessor manifest digest changed")


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
    expected_paths = {item["path"] for item in result}
    actual_paths = {
        str(path.relative_to(destination))
        for path in destination.rglob("*")
        if path.is_file() and not path.is_symlink()
    }
    require(actual_paths == expected_paths, f"copied predecessor evidence has extra or missing files: {owned_rel(destination)}")
    require(not [path for path in destination.rglob("*") if path.is_symlink()], f"copied predecessor evidence contains a symlink: {owned_rel(destination)}")
    return result


def git_blob_sha(revision: str, path: str) -> str:
    data = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=REPO_ROOT, check=True, capture_output=True).stdout
    return sha256_bytes(data)


def git_archive_sha256(revision: str) -> str:
    cached = _GIT_ARCHIVE_HASHES.get(revision)
    if cached:
        return cached
    process = subprocess.Popen(
        ["git", "archive", "--format=tar", revision],
        cwd=REPO_ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    digest = hashlib.sha256()
    require(process.stdout is not None and process.stderr is not None, "git archive pipes were not created")
    while True:
        chunk = process.stdout.read(1024 * 1024)
        if not chunk:
            break
        digest.update(chunk)
    process.stdout.close()
    error = process.stderr.read().decode(errors="replace")
    process.stderr.close()
    returncode = process.wait()
    require(returncode == 0, f"git archive hash failed for {revision}: {error[:2000]}")
    value = digest.hexdigest()
    _GIT_ARCHIVE_HASHES[revision] = value
    return value


def archive_source(revision: str, label: str) -> dict[str, Any]:
    scratch = OWNED_ROOT / "scratch" / "sources"
    archive_dir = OWNED_ROOT / "scratch" / "archives"
    source_root = scratch / label
    archive_path = archive_dir / f"{label}-{revision}.tar"
    source_root.mkdir(parents=True, exist_ok=True)
    archive_dir.mkdir(parents=True, exist_ok=True)
    marker = source_root / ".extracted-revision"
    expected_archive_hash = git_archive_sha256(revision)
    if (
        marker.is_file()
        and marker.read_text().strip() == revision
        and archive_path.is_file()
        and sha256_file(archive_path) == expected_archive_hash
        and (source_root / "go.work").is_file()
    ):
        archive_hash = sha256_file(archive_path)
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
    require(archive_path.is_file() and archive_path.stat().st_size > 0, f"source archive is missing: {archive_path}")
    require(archive_hash == expected_archive_hash, f"source archive is not the exact Git archive for {revision}")
    tree_digest, tree_files = source_tree_digest(archive_path, source_root)
    return {
        "revision": revision,
        "archive": {"path": owned_rel(archive_path), "bytes": archive_path.stat().st_size, "sha256": archive_hash, "git_archive_sha256": expected_archive_hash},
        "root": owned_rel(source_root),
        "go_work_sha256": sha256_file(source_root / "go.work"),
        "tree_sha256": tree_digest,
        "tree_files": tree_files,
    }


def source_tree_digest(archive_path: Path, source_root: Path) -> tuple[str, int]:
    """Bind every extracted archive file to the exact Git archive bytes."""
    expected: list[dict[str, Any]] = []
    expected_paths: set[str] = set()
    with tarfile.open(archive_path, "r") as archive:
        for member in archive:
            if not member.isfile():
                continue
            relative = Path(member.name)
            target = (source_root / relative).resolve()
            require(target == source_root.resolve() or source_root.resolve() in target.parents, f"archive file escaped source root: {member.name}")
            require(not target.is_symlink() and target.is_file(), f"extracted archive file is missing or symlinked: {member.name}")
            stream = archive.extractfile(member)
            require(stream is not None, f"archive file cannot be read: {member.name}")
            expected_bytes = stream.read()
            actual_bytes = target.read_bytes()
            require(actual_bytes == expected_bytes, f"extracted archive file changed: {member.name}")
            expected_paths.add(member.name)
            expected.append({"path": member.name, "bytes": len(actual_bytes), "sha256": sha256_bytes(actual_bytes)})
    for path in source_root.rglob("*"):
        if path.is_symlink():
            relative = path.relative_to(source_root)
            require(relative == Path(".extracted-revision") or (relative.parts and relative.parts[0] == "__c68_input__"), f"unexpected source symlink: {owned_rel(path)}")
            continue
        if not path.is_file():
            continue
        relative = str(path.relative_to(source_root))
        if relative == ".extracted-revision" or relative.split("/", 1)[0] == "__c68_input__":
            continue
        require(relative in expected_paths, f"unaccounted file in extracted source: {owned_rel(path)}")
    expected.sort(key=lambda item: item["path"])
    return canonical_digest(expected), len(expected)


def validate_source_binding(source_meta: dict[str, Any]) -> dict[str, Any]:
    revision = source_meta.get("revision")
    require(isinstance(revision, str) and len(revision) == 40, "source binding revision is not a full SHA")
    assert_commit(revision)
    require(isinstance(source_meta.get("root"), str), "source root binding is malformed")
    root = owned_path(source_meta["root"])
    require(root.is_dir(), f"source binding root is missing: {owned_rel(root)}")
    archive_meta = source_meta.get("archive")
    require(isinstance(archive_meta, dict), f"source archive binding is missing for {revision}")
    archive = owned_path(archive_meta.get("path", ""))
    require(archive.is_file(), f"source archive is missing: {owned_rel(archive)}")
    require(archive.stat().st_size == int(archive_meta.get("bytes", -1)), f"source archive size changed: {owned_rel(archive)}")
    archive_sha = sha256_file(archive)
    require(archive_sha == archive_meta.get("sha256"), f"source archive hash changed: {owned_rel(archive)}")
    require(archive_meta.get("git_archive_sha256") == git_archive_sha256(revision) and archive_sha == archive_meta.get("git_archive_sha256"), f"source archive is not the exact Git archive for {revision}")
    marker = root / ".extracted-revision"
    require(marker.is_file() and marker.read_text().strip() == revision, f"source extraction marker does not bind {owned_rel(root)} to {revision}")
    tree_sha, tree_files = source_tree_digest(archive, root)
    require(tree_sha == source_meta.get("tree_sha256") and tree_files == int(source_meta.get("tree_files", -1)), f"extracted source tree changed: {owned_rel(root)}")
    go_work = root / "go.work"
    require(sha256_file(go_work) == source_meta.get("go_work_sha256"), f"archived go.work changed: {owned_rel(go_work)}")
    return {"revision": revision, "root": owned_rel(root), "archive_sha256": archive_sha, "tree_sha256": tree_sha, "tree_files": tree_files, "verified": True}


def copy_build_main(source_meta: dict[str, Any], label: str) -> Path:
    source_root = OWNED_ROOT / source_meta["root"]
    destination = source_root / "__c68_input__" / label / "main.go"
    destination.parent.mkdir(parents=True, exist_ok=True)
    require(not destination.is_symlink(), f"build consumer destination is a symlink: {owned_rel(destination)}")
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
    require(isinstance(meta.get("root"), str), "source root binding is malformed")
    root = owned_path(meta["root"])
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


def validate_build_binding(label: str, provenance: dict[str, Any], build_record: dict[str, Any], timeout: float, *, consumer_sha256: str | None = None) -> dict[str, Any]:
    """Recompute source, input and executable identity; never trust child labels."""
    source_meta = provenance.get("sources", {}).get(label) if isinstance(provenance.get("sources"), dict) else None
    require(isinstance(source_meta, dict), f"missing {label} source binding")
    source = validate_source_binding(source_meta)
    revision = source["revision"]
    require(build_record.get("revision") == revision, f"{label} build revision is not bound to its source archive")
    require(build_record.get("source_archive_sha256") == source["archive_sha256"], f"{label} build archive hash is not bound to the source archive")
    require(build_record.get("source_tree_sha256") == source["tree_sha256"] and int(build_record.get("source_tree_files", -1)) == source["tree_files"], f"{label} build source tree binding changed")
    root = owned_path(build_record.get("source_root", ""))
    require(root == owned_path(source_meta.get("root", "")), f"{label} build root differs from the bound source root")
    main = owned_path(build_record.get("consumer_source", ""))
    require(main.is_file() and not main.is_symlink(), f"{label} build consumer source is missing")
    require(sha256_file(main) == build_record.get("consumer_source_sha256"), f"{label} consumer source hash changed")
    if consumer_sha256 is not None:
        require(sha256_file(main) == consumer_sha256, f"{label} consumer source is not the expected consumer")
    inputs = build_input_manifest(root, main, timeout)
    require(inputs == build_record.get("build_inputs"), f"{label} build input files or hashes changed")
    require(canonical_digest(inputs) == build_record.get("build_inputs_sha256"), f"{label} build input digest changed")
    binary_meta = build_record.get("binary")
    require(isinstance(binary_meta, dict), f"{label} executable binding is missing")
    binary = owned_path(binary_meta.get("path", ""))
    require(binary.is_file() and not binary.is_symlink(), f"{label} executable is missing")
    actual_sha = sha256_file(binary)
    require(binary.stat().st_size == int(binary_meta.get("bytes", -1)) and actual_sha == binary_meta.get("sha256"), f"{label} executable hash or size changed")
    return {
        "label": label,
        "source_revision": revision,
        "source_root": owned_rel(root),
        "source_archive_sha256": source["archive_sha256"],
        "source_tree_sha256": source["tree_sha256"],
        "build_inputs_sha256": build_record["build_inputs_sha256"],
        "binary_path": owned_rel(binary),
        "binary_sha256": actual_sha,
        "binary_bytes": binary.stat().st_size,
        "verified": True,
    }


def executable_binding_guard(bindings: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """Prove that a sibling revision's binary cannot launch under this label."""
    cases: list[dict[str, Any]] = []
    for expected_label, actual_label in (("c23", "c56"), ("c56", "c23")):
        expected = bindings[expected_label]
        actual = bindings[actual_label]
        expected_path = owned_path(expected["binary_path"])
        actual_path = owned_path(actual["binary_path"])
        require(expected_path.resolve() != actual_path.resolve(), "C23 and C56 executable bindings unexpectedly share a path")
        try:
            run_child(
                [str(actual_path)],
                f"binding-swap-{actual_label}-as-{expected_label}",
                REPO_ROOT,
                expected["source_revision"],
                OWNED_ROOT / "artifacts" / "binding-guard" / f"{expected_label}-as-{actual_label}",
                1.0,
                binary_binding=expected,
            )
        except VerificationError as error:
            require("not the bound binary" in str(error), f"unexpected executable swap rejection: {error}")
            cases.append({"expected_label": expected_label, "actual_label": actual_label, "rejected": True})
        else:
            raise VerificationError(f"executable swap was not rejected: {actual_label} launched as {expected_label}")
    return {"enabled": True, "swapped_binary_rejected": True, "cases": cases}


def build(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    provenance = load_json(PROVENANCE_PATH)
    require(provenance["c23_revision"] == args.c23_revision and provenance["c56_revision"] == args.c56_revision, "build revisions differ from prepared provenance")
    validate_frozen_inputs(provenance)
    build_root = OWNED_ROOT / "scratch" / "build"
    build_root.mkdir(parents=True, exist_ok=True)
    records: dict[str, Any] = {}
    for label in ("c23", "c56"):
        meta = provenance["sources"][label]
        source_binding = validate_source_binding(meta)
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
            "source_archive_sha256": source_binding["archive_sha256"],
            "source_tree_sha256": source_binding["tree_sha256"],
            "source_tree_files": source_binding["tree_files"],
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
        "runner_sha256": sha256_file(Path(__file__)),
        "builds": records,
    }
    bindings = {}
    for label in ("c23", "c56"):
        bindings[label] = validate_build_binding(label, provenance, value["builds"][label], args.child_timeout_seconds, consumer_sha256=sha256_file(INPUT_C23 / "consumer" / "main.go"))
    value["binding_guard"] = executable_binding_guard(bindings)
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
        "LANG": "C", "LC_ALL": "C",
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
    if not hasattr(os, "killpg"):
        return process_alive(pid)
    try:
        os.killpg(pid, 0)
    except (ProcessLookupError, OSError):
        return False
    return True


def group_members(pgid: int) -> list[int]:
    """Return observable process-group members when the host exposes /proc."""
    proc_root = Path("/proc")
    if not proc_root.is_dir():
        return [] if not group_alive(pgid) else [-1]
    members: list[int] = []
    for entry in proc_root.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            stat = (entry / "stat").read_text()
            _, remainder = stat.split(") ", 1)
            fields = remainder.split()
            if len(fields) >= 4 and int(fields[2]) == pgid:
                members.append(int(entry.name))
        except (OSError, ValueError):
            continue
    return sorted(members)


def process_snapshot() -> dict[int, dict[str, int | str]]:
    """Read a bounded process table for descendants that changed groups."""
    if os.name == "nt":
        return {}
    result = subprocess.run(
        ["ps", "-axo", "pid=,ppid=,pgid=,stat="],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return {}
    snapshot: dict[int, dict[str, int | str]] = {}
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) < 3:
            continue
        try:
            pid, ppid, pgid = (int(value) for value in fields[:3])
        except ValueError:
            continue
        snapshot[pid] = {"ppid": ppid, "pgid": pgid, "stat": fields[3] if len(fields) > 3 else ""}
    return snapshot


def descendant_pids(root_pid: int, snapshot: dict[int, dict[str, int | str]] | None = None) -> list[int]:
    snapshot = process_snapshot() if snapshot is None else snapshot
    children: dict[int, list[int]] = {}
    for pid, details in snapshot.items():
        ppid = details.get("ppid")
        if isinstance(ppid, int):
            children.setdefault(ppid, []).append(pid)
    pending = list(children.get(root_pid, []))
    descendants: list[int] = []
    seen: set[int] = set()
    while pending:
        pid = pending.pop(0)
        if pid in seen or pid == root_pid:
            continue
        seen.add(pid)
        descendants.append(pid)
        pending.extend(children.get(pid, []))
    return sorted(descendants)


def process_alive(pid: int, snapshot: dict[int, dict[str, int | str]] | None = None) -> bool:
    if pid <= 0:
        return False
    if snapshot is not None:
        details = snapshot.get(pid)
        if details is None:
            return False
        stat = str(details.get("stat", ""))
        if stat.startswith("Z"):
            return False
    try:
        os.kill(pid, 0)
    except (ProcessLookupError, PermissionError, OSError):
        return False
    return True


def wait_for_process_exit(pids: Iterable[int], timeout: float) -> list[int]:
    tracked = sorted({pid for pid in pids if pid > 0})
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        snapshot = process_snapshot()
        survivors = [pid for pid in tracked if process_alive(pid, snapshot if snapshot else None)]
        if not survivors:
            return []
        time.sleep(0.05)
    snapshot = process_snapshot()
    return [pid for pid in tracked if process_alive(pid, snapshot if snapshot else None)]


def signal_pid(pid: int, value: signal.Signals, errors: list[str]) -> bool:
    try:
        os.kill(pid, value)
        return True
    except ProcessLookupError:
        return False
    except OSError as error:
        errors.append(f"{value.name} pid {pid}: {error}")
        return False


def signal_group(pgid: int, value: signal.Signals, errors: list[str]) -> bool:
    if not hasattr(os, "killpg"):
        return False
    try:
        os.killpg(pgid, value)
        return True
    except ProcessLookupError:
        return False
    except OSError as error:
        errors.append(f"{value.name} group {pgid}: {error}")
        return False


def wait_for_group_exit(pgid: int, timeout: float) -> list[int]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        members = group_members(pgid)
        if not group_alive(pgid) and not members:
            return []
        time.sleep(0.05)
    return group_members(pgid) if group_alive(pgid) else []


def stop_group(process: subprocess.Popen[Any]) -> dict[str, Any]:
    pgid = process.pid
    before_members = group_members(pgid)
    before = group_alive(pgid)
    before_descendants = descendant_pids(process.pid)
    tracked_descendants = set(before_descendants)
    term_sent = False
    kill_sent = False
    errors: list[str] = []
    if before:
        term_sent = signal_group(pgid, signal.SIGTERM, errors) or term_sent
    # A descendant can call setsid() and escape the original process group.
    # Signal the exact snapshot of descendants as well as the group, before
    # the leader can reparent them and make their ownership ambiguous.
    for pid in sorted(tracked_descendants):
        if process_alive(pid):
            term_sent = signal_pid(pid, signal.SIGTERM, errors) or term_sent
    if before or process.poll() is None or tracked_descendants:
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
        tracked_descendants.update(descendant_pids(process.pid))
        if group_alive(pgid):
            kill_sent = signal_group(pgid, signal.SIGKILL, errors) or kill_sent
        for pid in sorted(tracked_descendants):
            if process_alive(pid):
                kill_sent = signal_pid(pid, signal.SIGKILL, errors) or kill_sent
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
    if process.poll() is None:
        with suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)
    survivors = wait_for_group_exit(pgid, 2.0)
    descendant_survivors = wait_for_process_exit(tracked_descendants, 2.0)
    after = group_alive(pgid) or bool(survivors) or bool(descendant_survivors)
    return {
        "pgid": pgid,
        "group_alive_before": before,
        "group_members_before": before_members,
        "group_alive_after": after,
        "group_members_after": survivors,
        "descendants_before": before_descendants,
        "descendants_after": descendant_survivors,
        "descendant_processes_reaped": not descendant_survivors,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "parent_reaped": process.returncode is not None,
        "errors": errors,
    }


def artifact_disk_usage(root: Path) -> dict[str, Any]:
    require(root.is_dir(), f"artifact root is missing: {owned_rel(root)}")
    total = 0
    files = 0
    for path in root.rglob("*"):
        if path.is_symlink():
            require(path.resolve() == root.resolve() or root.resolve() in path.resolve().parents, f"artifact symlink escaped owned root: {owned_rel(path)}")
            require(False, f"artifact symlinks are not allowed: {owned_rel(path)}")
        if not path.is_file():
            continue
        files += 1
        total += path.stat().st_size
        require(files <= MAX_RETAINED_ARTIFACT_FILES, f"artifact file count exceeded {MAX_RETAINED_ARTIFACT_FILES}: {owned_rel(root)}")
        require(total <= MAX_RUN_ARTIFACT_BYTES, f"artifact disk cap exceeded {MAX_RUN_ARTIFACT_BYTES} bytes: {owned_rel(root)}")
    return {"bytes": total, "files": files, "cap_bytes": MAX_RUN_ARTIFACT_BYTES, "bounded": total <= MAX_RUN_ARTIFACT_BYTES}


def artifact_inventory(root: Path) -> dict[str, Any]:
    """Bind an artifact root without retaining its contents in the ledger."""
    disk = artifact_disk_usage(root)
    entries: list[dict[str, Any]] = []
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink():
            entries.append({"path": str(path.relative_to(root)), "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    return {
        "root": owned_rel(root),
        "bytes": disk["bytes"],
        "files": disk["files"],
        "cap_bytes": disk["cap_bytes"],
        "tree_sha256": canonical_digest(entries),
    }


def positive_artifact_inventory() -> list[dict[str, Any]]:
    root = OWNED_ROOT / "artifacts" / "runs"
    if not root.is_dir():
        return []
    result: list[dict[str, Any]] = []
    for child in sorted(root.iterdir()):
        require(not child.is_symlink(), f"positive artifact root is a symlink: {owned_rel(child)}")
        if child.is_dir():
            result.append(artifact_inventory(child))
        else:
            require(False, f"positive artifact runs contains an unaccounted file: {owned_rel(child)}")
    return result


def sync_positive_artifact_inventory(ledger: dict[str, Any], complete_roots: Iterable[str] = ()) -> list[dict[str, Any]]:
    """Account for every retained positive root and reject changed history."""
    complete = set(complete_roots)
    stored = ledger.get("positive_artifact_inventory", [])
    require(isinstance(stored, list), "positive artifact inventory is malformed")
    known: dict[str, dict[str, Any]] = {}
    for item in stored:
        require(isinstance(item, dict) and isinstance(item.get("root"), str), "positive artifact inventory entry is malformed")
        root = item["root"]
        require(root not in known, f"positive artifact root is recorded more than once: {root}")
        known[root] = item
    actual = positive_artifact_inventory()
    for item in actual:
        root = item["root"]
        prior = known.get(root)
        if prior is not None:
            require(prior == item or {key: prior.get(key) for key in item} == item, f"retained positive artifact changed: {root}")
            continue
        item = {**item, "state": "complete" if root in complete else "historical"}
        known[root] = item
    result = sorted(known.values(), key=lambda item: item["root"])
    ledger["positive_artifact_inventory"] = result
    ledger["duplicate_guard"] = {
        **(ledger.get("duplicate_guard") if isinstance(ledger.get("duplicate_guard"), dict) else {}),
        "enabled": True,
        "all_retained_positive_roots_accounted": True,
        "inventory_sha256": canonical_digest(result),
    }
    return result


def retained_artifact_usage() -> dict[str, Any]:
    roots = [OWNED_ROOT / "artifacts" / "runs", OWNED_ROOT / "artifacts" / "negative-control", OWNED_ROOT / "artifacts" / "cleanup-control"]
    total = 0
    files = 0
    per_root: list[dict[str, Any]] = []
    for root in roots:
        if not root.is_dir():
            continue
        children = [child for child in sorted(root.iterdir()) if child.is_dir()]
        for child in children:
            inventory = artifact_inventory(child)
            per_root.append(inventory)
            files += inventory["files"]
            total += inventory["bytes"]
        for path in root.iterdir():
            if path.is_symlink():
                require(False, f"retained artifact symlink is not allowed: {owned_rel(path)}")
            if path.is_file():
                files += 1
                total += path.stat().st_size
    return {"bytes": total, "files": files, "cap_bytes": MAX_RETAINED_ARTIFACT_BYTES, "cap_files": MAX_RETAINED_ARTIFACT_FILES, "bounded": total <= MAX_RETAINED_ARTIFACT_BYTES and files <= MAX_RETAINED_ARTIFACT_FILES, "per_root": per_root}


def run_child(command: list[str], label: str, cwd: Path, source_revision: str, run_root: Path, timeout: float, *, binary_binding: dict[str, Any] | None = None) -> dict[str, Any]:
    require(timeout > 0, f"{label} has no remaining child budget")
    if binary_binding is not None:
        executable = owned_path(binary_binding.get("binary_path", ""))
        require(command and Path(command[0]).resolve() == executable.resolve(), f"{label} command executable is not the bound binary")
        require(executable.is_file() and sha256_file(executable) == binary_binding.get("binary_sha256"), f"{label} executable changed after binding")
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
    cleanup["pipes_reaped"] = not out_thread.is_alive() and not err_thread.is_alive()
    if not cleanup["pipes_reaped"]:
        cleanup["errors"].append("output pipe reader did not terminate")
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
        "binary_binding": binary_binding,
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
    require(len(candidates) == 1, f"provider capture count is not exactly one below {owned_rel(artifact_root)}")
    path = candidates[0]
    require(path.resolve().parent == artifact_root.resolve(), f"provider capture is not at the execution boundary: {owned_rel(path)}")
    capture = json.loads(path.read_text())
    require(isinstance(capture, dict) and isinstance(capture.get("records"), list), f"provider capture is not a structured session record: {owned_rel(path)}")
    audio_records: list[bytes] = []
    # The capture stores the outer session record and the nested stream
    # message, both of which carry type=AUDIO.DELTA.  Count only the outer
    # records so one provider emission cannot be attributed twice.
    for item in capture["records"]:
        require(isinstance(item, dict), f"provider capture contains a malformed record: {owned_rel(path)}")
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
        if candidate.is_dir() and artifact_root.resolve() in candidate.resolve().parents:
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
    require(execution["cleanup"].get("pipes_reaped") is True and not execution["cleanup"].get("group_members_after") and execution["cleanup"].get("descendant_processes_reaped") is True and not execution["cleanup"].get("descendants_after"), f"{source_label}/{mode} child descendants or pipes survived")
    binding = execution.get("binary_binding")
    require(isinstance(binding, dict) and binding.get("verified") is True and binding.get("label") == source_label, f"{source_label}/{mode} executable binding is missing")
    require(binding.get("source_revision") == execution["source_revision"], f"{source_label}/{mode} executable source identity drifted")
    report = load_json(report_path)
    require(report.get("schema") == "c23.v1", f"{source_label}/{mode} report schema mismatch")
    require(report.get("scenario") == "tool-matrix" and report.get("turns") == 1, f"{source_label}/{mode} was not the one-turn matrix")
    # The frozen consumer's source_revision field is an untrusted diagnostic
    # populated from its environment.  Do not inject an expected revision or
    # let that field attest the binary; the archive/tree/build/binary binding
    # above is the independent source attestation.
    require(report.get("source_revision") == UNBOUND_SOURCE_REVISION, f"{source_label}/{mode} consumer reported a trusted source identity")
    execution["reported_source_revision"] = report.get("source_revision")
    execution["source_attestation"] = {
        "revision": binding["source_revision"],
        "source_archive_sha256": binding["source_archive_sha256"],
        "source_tree_sha256": binding["source_tree_sha256"],
        "build_inputs_sha256": binding["build_inputs_sha256"],
        "binary_sha256": binding["binary_sha256"],
        "report_source_revision_untrusted": True,
    }
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
    provider_ok = provider["nonempty"] and provider["bytes"] == len(expected) and provider["audio_delta_records"] == 1 and provider["nonempty_records"] == 1 and provider["recorded_bytes"] == [len(expected)]
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
    require(isinstance(drops, dict) and "live_event_overflow" in drops, f"{source_label}/{mode} recording drop accounting is unavailable")
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
    disk = artifact_disk_usage(artifact_root)
    execution["artifact_disk"] = disk
    return {
        "source": source_label, "mode": mode, "execution": execution, "report": owned_rel(report_path),
        "artifact_root": owned_rel(artifact_root), "boundaries": boundary_status,
        "public_event_audio_delta_count": public_audio_delta_count,
        "recording_report": report.get("recording_usage", {}),
        "artifact_disk": disk,
    }


def new_run_id(prefix: str) -> str:
    return time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + f"-{time.time_ns() % 1_000_000_000:09d}-{prefix}"


def reserve_comparison_run(driver_sha256: str) -> tuple[str, dict[str, Any]]:
    """Atomically reserve one comparison attempt and reject unchanged retries."""
    if COMPARISON_LOCK_PATH.exists():
        lock = load_json(COMPARISON_LOCK_PATH)
        raise VerificationError(f"C68 comparison is already reserved: {lock.get('run_id', 'unknown')}")
    prior = load_json(COMPARISON_PATH) if COMPARISON_PATH.is_file() else None
    if prior and prior.get("accepted_as_evidence") is True and prior.get("runner_sha256") == driver_sha256:
        raise VerificationError("C68 comparison already completed with this driver; unchanged duplicate run rejected")
    ledger = load_json(COMPARISON_LEDGER_PATH) if COMPARISON_LEDGER_PATH.is_file() else {
        "schema": "audio-runtime.c68.zero-audio-attribution.comparison-runs.v1",
        "task": TASK,
        "attempts": [],
    }
    require(ledger.get("schema") == "audio-runtime.c68.zero-audio-attribution.comparison-runs.v1", "comparison run ledger schema mismatch")
    require(not any(item.get("runner_sha256") == driver_sha256 for item in ledger.get("attempts", [])), "C68 comparison driver was already attempted; unchanged duplicate run rejected")
    sync_positive_artifact_inventory(ledger)
    run_id = new_run_id("comparison")
    COMPARISON_LOCK_PATH.parent.mkdir(parents=True, exist_ok=True)
    try:
        descriptor = os.open(COMPARISON_LOCK_PATH, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError as error:
        raise VerificationError("C68 comparison reservation raced with another run") from error
    with os.fdopen(descriptor, "w") as stream:
        json.dump({"schema": "audio-runtime.c68.zero-audio-attribution.comparison-lock.v1", "task": TASK, "run_id": run_id, "runner_sha256": driver_sha256, "state": "running"}, stream, indent=2, sort_keys=True)
        stream.write("\n")
    prior_record = None
    if prior:
        prior_record = {
            "source": "comparison.json",
            "sha256": sha256_file(COMPARISON_PATH),
            "run_id": prior.get("run_id", "legacy-unidentified"),
            "runner_sha256": prior.get("runner_sha256"),
            "positive_execution_count": prior.get("positive_execution_count"),
            "state": "historical",
        }
    attempt = {"run_id": run_id, "runner_sha256": driver_sha256, "started_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "state": "running"}
    if prior_record:
        attempt["prior_comparison"] = prior_record
    ledger.setdefault("attempts", []).append(attempt)
    ledger["current_run_id"] = run_id
    ledger["duplicate_guard"] = {
        **(ledger.get("duplicate_guard") if isinstance(ledger.get("duplicate_guard"), dict) else {}),
        "enabled": True,
        "atomic_lock": owned_rel(COMPARISON_LOCK_PATH),
        "unchanged_driver_rejected": True,
        "history_preserved": bool(prior_record),
        "run_ids_unique": True,
    }
    write_json(COMPARISON_LEDGER_PATH, ledger)
    return run_id, prior_record or {}


def finish_comparison_run(run_id: str, state: str, summary: dict[str, Any]) -> None:
    ledger = load_json(COMPARISON_LEDGER_PATH)
    attempts = ledger.get("attempts")
    require(isinstance(attempts, list), "comparison run ledger attempts are malformed")
    matches = [item for item in attempts if item.get("run_id") == run_id]
    require(len(matches) == 1, f"comparison run ledger is missing {run_id}")
    matches[0].update(summary)
    matches[0]["state"] = state
    matches[0]["finished_at_utc"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    complete_roots = summary.get("artifact_roots", []) if isinstance(summary.get("artifact_roots", []), list) else []
    inventory = sync_positive_artifact_inventory(ledger, complete_roots)
    matches[0]["positive_artifact_inventory_sha256"] = canonical_digest([item for item in inventory if item.get("root") in complete_roots])
    write_json(COMPARISON_LEDGER_PATH, ledger)
    if COMPARISON_LOCK_PATH.exists():
        COMPARISON_LOCK_PATH.unlink()


def write_attribution(comparison: dict[str, Any]) -> dict[str, Any]:
    provenance = load_json(PROVENANCE_PATH)
    attribution = comparison["attribution"]
    value: dict[str, Any] = {
        "schema": "audio-runtime.c68.zero-audio-attribution.attribution.v1",
        "task": TASK,
        "project": PROJECT,
        "contract": "audio-runtime-v1",
        "classification": attribution["classification"],
        "exactly_one_classification": attribution["exactly_one_classification"],
        "c23_revision": comparison["c23_revision"],
        "c56_revision": comparison["c56_revision"],
        "comparison": {"run_id": comparison["run_id"], "sha256": canonical_digest(comparison), "path": owned_rel(COMPARISON_PATH)},
        "provenance": {"path": owned_rel(PROVENANCE_PATH), "sha256": sha256_file(PROVENANCE_PATH), "prepared_head": provenance["head"], "current_main": provenance["current_main"]},
        "build": {"path": owned_rel(BUILD_PATH), "sha256": sha256_file(BUILD_PATH), "bindings": comparison["build_bindings"]},
        "source_binding": {"independent_of_consumer_report": True, "report_source_revision_is_untrusted": True, "executions": [{"source": item["source"], "mode": item["mode"], "binary_sha256": item["execution"]["binary_binding"]["binary_sha256"], "build_inputs_sha256": item["execution"]["binary_binding"]["build_inputs_sha256"]} for item in comparison["executions"]]},
        "first_divergent_boundary": attribution["first_divergent_boundary"],
        "first_divergent_api": attribution["first_divergent_api"],
        "first_divergent_path": attribution["first_divergent_path"],
        "causal_detail": attribution["causal_detail"],
        "smallest_existing_c56_executor_repair": attribution["smallest_existing_c56_executor_repair"],
        "owner_action": {"owner": "audio-runtime-c56-retire-cli-recording-orchestration", "action": "preserve provider-media capability absence at the terminalDrainSession/capturingInferencer handoff so the public observer reaches RecordAudio", "scope": "existing C56 executor; C68 performs no production mutation"},
        "c23_runner_repair_needed": attribution["c23_runner_repair_needed"],
        "c56_candidate_restores_audio": attribution["c56_candidate_restores_audio"],
        "limitations": ["C68 does not claim C56 delivery or C23 delivery", "C68 does not claim device consumption or acoustic output", "script CI, independent review, guarded merge and post-merge vertical probing remain external"],
        "evidence_ready": True,
    }
    if REGRESSIONS_PATH.is_file():
        regressions = load_json(REGRESSIONS_PATH)
        value["c21_regressions"] = {"path": owned_rel(REGRESSIONS_PATH), "sha256": sha256_file(REGRESSIONS_PATH), "tested_source_revision": regressions.get("source_revision"), "passed": regressions.get("passed") is True}
    write_json(ATTRIBUTION_PATH, value)
    return value


def run_matrix(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    provenance = load_json(PROVENANCE_PATH)
    require(provenance["c23_revision"] == args.c23_revision and provenance["c56_revision"] == args.c56_revision, "comparison revisions differ from provenance")
    validate_frozen_inputs(provenance)
    require(args.turns == 1, "C68 comparison is exactly one logical turn per execution")
    require(tuple(args.recording) == RECORDING_MODES, "C68 comparison recording modes must be exactly off,on")
    driver_sha256 = sha256_file(Path(__file__))
    run_id, prior = reserve_comparison_run(driver_sha256)
    started = time.monotonic()
    evidence: list[dict[str, Any]] = []
    try:
        build_manifest = load_json(BUILD_PATH)
        bindings = {label: validate_build_binding(label, provenance, build_manifest["builds"][label], args.child_timeout_seconds, consumer_sha256=sha256_file(INPUT_C23 / "consumer" / "main.go")) for label in ("c23", "c56")}
        binding_guard = executable_binding_guard(bindings)
        require(build_manifest.get("binding_guard") == binding_guard, "build manifest executable swap guard changed")
        for source_label in ("c23", "c56"):
            source_revision = provenance[f"{source_label}_revision"]
            root = source_root(provenance["sources"][source_label])
            binary = owned_path(build_manifest["builds"][source_label]["binary"]["path"])
            for mode in RECORDING_MODES:
                require(len(evidence) < MAX_POSITIVE_RUNS, "positive execution count exceeded four")
                artifact_root = OWNED_ROOT / "artifacts" / "runs" / f"{run_id}-{source_label}-{mode}"
                require(not artifact_root.exists(), f"comparison artifact root already exists: {owned_rel(artifact_root)}")
                report_path = artifact_root / "report.json"
                command = [str(binary), "-scenario=tool-matrix", "-fixture=" + str(FIXTURE_PATH), "-turns=1", "-recording=" + ("true" if mode == "on" else "false"), "-artifact-root=" + str(artifact_root), "-output=" + str(report_path)]
                execution = run_child(command, f"{source_label}-{mode}", root, source_revision, artifact_root / "process", args.child_timeout_seconds, binary_binding=bindings[source_label])
                evidence.append(execution_evidence(execution, report_path, artifact_root, source_label, mode))
                require(time.monotonic() - started <= args.total_timeout_seconds, "C68 positive comparison exceeded aggregate timeout")
        require(len(evidence) == MAX_POSITIVE_RUNS, "comparison did not execute exactly four positive cases")
        by_source = {label: {item["mode"]: item for item in evidence if item["source"] == label} for label in ("c23", "c56")}
        for label in by_source:
            require(set(by_source[label]) == set(RECORDING_MODES), f"{label} is missing a recording mode")
            off = by_source[label]["off"]
            on = by_source[label]["on"]
            require(off["boundaries"]["public_pcm"]["bytes"] == on["boundaries"]["public_pcm"]["bytes"] and off["boundaries"]["public_pcm"]["sha256"] == on["boundaries"]["public_pcm"]["sha256"], f"{label} off/on public PCM differs")
            require(off["boundaries"]["provider_capture"]["bytes"] == on["boundaries"]["provider_capture"]["bytes"] and off["boundaries"]["provider_capture"]["sha256"] == on["boundaries"]["provider_capture"]["sha256"], f"{label} off/on provider PCM differs")
        classification = "C56_RECORDING_DEFECT"
        artifact_disk_bytes = sum(item["artifact_disk"]["bytes"] for item in evidence)
        require(artifact_disk_bytes <= MAX_RETAINED_ARTIFACT_BYTES, "C68 retained positive artifact disk cap exceeded")
        comparison = {
            "schema": "audio-runtime.c68.zero-audio-attribution.comparison.v1",
            "task": TASK, "c23_revision": args.c23_revision, "c56_revision": args.c56_revision,
            "turns": args.turns, "recording_modes": list(RECORDING_MODES), "positive_execution_count": len(evidence),
            "run_id": run_id, "runner_sha256": driver_sha256,
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000), "aggregate_timeout_seconds": args.total_timeout_seconds,
            "artifact_disk": {"bytes": artifact_disk_bytes, "cap_bytes": MAX_RETAINED_ARTIFACT_BYTES, "bounded": artifact_disk_bytes <= MAX_RETAINED_ARTIFACT_BYTES},
            "duplicate_guard": {
                "enabled": True,
                "ledger": owned_rel(COMPARISON_LEDGER_PATH),
                "prior_attempt_preserved": bool(prior),
                "current_run_id": run_id,
                "positive_artifact_roots_accounted": True,
                "artifact_roots": [item["artifact_root"] for item in evidence],
                "positive_artifact_inventory_sha256": canonical_digest([artifact_inventory(owned_path(item["artifact_root"])) for item in evidence]),
            },
            "binding_guard": binding_guard,
            "build_bindings": bindings,
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
        # Preserve the first demonstrated causal failure as a stable pointer.
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
        write_attribution(comparison)
        finish_comparison_run(
            run_id,
            "complete",
            {
                "positive_execution_count": len(evidence),
                "elapsed_ms": comparison["aggregate_elapsed_ms"],
                "comparison_sha256": canonical_digest(comparison),
                "artifact_roots": [item["artifact_root"] for item in evidence],
            },
        )
        print(json.dumps({"status": "compared", "positive_executions": len(evidence), "classification": classification, "run_id": run_id}, sort_keys=True))
        return comparison
    except Exception as error:
        with suppress(Exception):
            finish_comparison_run(run_id, "failed", {"error": str(error)[:2000]})
        raise


def make_negative_source() -> tuple[Path, str, str]:
    source = INPUT_C23 / "consumer" / "main.go"
    original = source.read_bytes()
    old = NEGATIVE_MUTATION_OLD.encode()
    new = NEGATIVE_MUTATION_NEW.encode()
    require(original.count(old) == 1, "negative control could not identify exactly one provider audio emission")
    mutated = original.replace(old, new)
    negative = OWNED_ROOT / "consumer-negative" / "main.go"
    negative.parent.mkdir(parents=True, exist_ok=True)
    require(not negative.is_symlink(), f"negative consumer destination is a symlink: {owned_rel(negative)}")
    negative.write_bytes(mutated)
    return negative, sha256_file(source), sha256_file(negative)


def expected_negative_consumer_bytes() -> bytes:
    original = (INPUT_C23 / "consumer" / "main.go").read_bytes()
    old = NEGATIVE_MUTATION_OLD.encode()
    new = NEGATIVE_MUTATION_NEW.encode()
    require(original.count(old) == 1, "frozen consumer does not contain exactly one negative-control audio emission")
    return original.replace(old, new)


def negative_control(args: argparse.Namespace) -> dict[str, Any]:
    assert_scope()
    require(args.mutation == "first-provider-audio", "C68 only admits the first-provider-audio negative mutation")
    provenance = load_json(PROVENANCE_PATH)
    validate_frozen_inputs(provenance)
    build_manifest = load_json(BUILD_PATH)
    source_revision = provenance["c56_revision"]
    validate_build_binding("c56", provenance, build_manifest["builds"]["c56"], args.child_timeout_seconds, consumer_sha256=sha256_file(INPUT_C23 / "consumer" / "main.go"))
    root = source_root(provenance["sources"]["c56"])
    negative, original_hash, negative_hash = make_negative_source()
    expected_negative = expected_negative_consumer_bytes()
    require(negative.read_bytes() == expected_negative, "negative control is not the admitted exact first-provider mutation")
    binary = OWNED_ROOT / "scratch" / "build" / "c56-negative-consumer"
    command = ["go", "build", "-trimpath", "-o", str(binary), str(root / "__c68_input__" / "negative" / "main.go")]
    negative_source = root / "__c68_input__" / "negative" / "main.go"
    negative_source.parent.mkdir(parents=True, exist_ok=True)
    require(not negative_source.is_symlink(), f"negative build source is a symlink: {owned_rel(negative_source)}")
    shutil.copyfile(negative, negative_source)
    run_command(command, cwd=root, timeout=args.child_timeout_seconds)
    require(binary.is_file(), "negative control build did not produce a binary")
    source_binding = validate_source_binding(provenance["sources"]["c56"])
    negative_inputs = build_input_manifest(root, negative_source, args.child_timeout_seconds)
    negative_build = {
        "revision": source_revision,
        "source_root": owned_rel(root),
        "consumer_source": owned_rel(negative_source),
        "consumer_source_sha256": negative_hash,
        "source_archive_sha256": source_binding["archive_sha256"],
        "source_tree_sha256": source_binding["tree_sha256"],
        "source_tree_files": source_binding["tree_files"],
        "binary": {"path": owned_rel(binary), "sha256": sha256_file(binary), "bytes": binary.stat().st_size},
        "build_inputs": negative_inputs,
        "build_inputs_sha256": canonical_digest(negative_inputs),
    }
    binding = validate_build_binding("c56", provenance, negative_build, args.child_timeout_seconds, consumer_sha256=negative_hash)
    binding["label"] = "c56-negative"
    artifact_root = OWNED_ROOT / "artifacts" / "negative-control" / new_run_id("negative")
    require(not artifact_root.exists(), f"negative-control artifact root already exists: {owned_rel(artifact_root)}")
    report_path = artifact_root / "report.json"
    child_command = [str(binary), "-scenario=tool-matrix", "-fixture=" + str(FIXTURE_PATH), "-turns=1", "-recording=false", "-artifact-root=" + str(artifact_root), "-output=" + str(report_path)]
    execution = run_child(child_command, "negative-first-provider-audio", root, source_revision, artifact_root / "process", args.child_timeout_seconds, binary_binding=binding)
    require(execution["returncode"] == 1 and not execution["timed_out"] and execution["output_bounded"], "negative control did not fail with the expected bounded oracle error")
    require(execution["cleanup"]["parent_reaped"] and not execution["cleanup"]["group_alive_after"] and execution["cleanup"].get("pipes_reaped") is True and not execution["cleanup"].get("group_members_after"), "negative control cleanup was incomplete")
    require(report_path.is_file(), "negative control did not preserve its report")
    report = load_json(report_path)
    require(report.get("schema") == "c23.v1" and report.get("scenario") == "tool-matrix" and report.get("turns") == 1 and report.get("recording") is not True, "negative control report shape changed")
    require(report.get("source_revision") == UNBOUND_SOURCE_REVISION and report.get("fixture_sha256") == fixture_digest(), "negative control trusted its consumer source identity")
    require(isinstance(report.get("error"), str) and report["error"].startswith("PCM oracle mismatch:"), "negative control did not fail at the public PCM oracle")
    require(report.get("trace_complete") is True and report.get("clean_shutdown") is True, "negative control stopped before the public audio boundary")
    pcm = report.get("pcm") if isinstance(report.get("pcm"), dict) else {}
    provider = provider_audio(artifact_root)
    events = report.get("events") if isinstance(report.get("events"), dict) else {}
    by_kind = events.get("by_kind") if isinstance(events.get("by_kind"), dict) else {}
    passed = int(pcm.get("bytes", 0) or 0) == 0 and pcm.get("sha256") == sha256_bytes(b"") and provider["audio_delta_records"] == 1 and provider["nonempty_records"] == 0 and provider["recorded_bytes"] == [0] and provider["bytes"] == 0 and by_kind.get("AUDIO.DELTA") == 1
    require(passed, "negative control did not fail at the intended provider/public audio oracle boundary")
    disk = artifact_disk_usage(artifact_root)
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.negative.v1", "task": TASK,
        "mutation": args.mutation, "positive_fixture_sha256": fixture_digest(),
        "original_consumer_sha256": original_hash, "mutated_consumer_sha256": negative_hash,
        "mutated_source": owned_rel(negative), "binary": {"path": owned_rel(binary), "sha256": sha256_file(binary), "bytes": binary.stat().st_size},
        "build_binding": binding, "build": negative_build,
        "execution": execution, "report": owned_rel(report_path), "provider_capture": provider, "artifact_disk": disk,
        "public_pcm": {"bytes": int(pcm.get("bytes", 0) or 0), "sha256": pcm.get("sha256"), "expected_rejection": True},
        "classification": "oracle_control_rejects_malformed_first_provider_audio",
        "passed": True,
    }
    write_json(NEGATIVE_PATH, value)
    print(json.dumps({"status": "negative-control-passed", "mutation": args.mutation, "returncode": execution["returncode"]}, sort_keys=True))
    return value


def cleanup_control(args: argparse.Namespace) -> dict[str, Any]:
    """Prove that a surviving grandchild is killed with its process group."""
    assert_scope()
    probe_root = OWNED_ROOT / "artifacts" / "cleanup-control" / new_run_id("cleanup")
    require(not probe_root.exists(), f"cleanup control output already exists: {owned_rel(probe_root)}")
    grandchild = "import os,signal,time; os.setsid(); signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"
    parent = "import subprocess,sys,time; p=subprocess.Popen([sys.executable,'-c',sys.argv[1]]); print(p.pid,flush=True); time.sleep(30)"
    command = [sys.executable, "-c", parent, grandchild]
    execution = run_child(command, "cleanup-grandchild", REPO_ROOT, git_value("rev-parse", "HEAD"), probe_root / "process", min(args.child_timeout_seconds, 2.0))
    disk = artifact_disk_usage(probe_root)
    execution["artifact_disk"] = disk
    passed = (
        execution["timed_out"] is True
        and execution["cleanup"]["group_alive_before"] is True
        and execution["cleanup"]["term_sent"] is True
        and execution["cleanup"]["kill_sent"] is True
        and execution["cleanup"]["parent_reaped"] is True
        and execution["cleanup"].get("pipes_reaped") is True
        and execution["cleanup"]["group_alive_after"] is False
        and not execution["cleanup"].get("group_members_after")
        and execution["cleanup"].get("descendants_before")
        and execution["cleanup"].get("descendant_processes_reaped") is True
        and not execution["cleanup"].get("descendants_after")
        and disk["bounded"]
    )
    require(passed, "cleanup control did not prove bounded process-group descendant reaping")
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.cleanup.v1",
        "task": TASK,
        "runner_sha256": sha256_file(Path(__file__)),
        "execution": execution,
        "artifact_disk": disk,
        "passed": True,
        "negative_case": "parent exits on TERM while grandchild ignores TERM; group KILL and reap are required",
    }
    write_json(CLEANUP_PATH, value)
    print(json.dumps({"status": "cleanup-control-passed", "kill_sent": True, "survivors": 0}, sort_keys=True))
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
    tested_source_revision = git_value("rev-parse", "HEAD")
    require(tested_source_revision != CURRENT_MAIN, "C21 evidence must run against the changed candidate, not base current main")
    normal = run_child(normal_cmd, "c21-normal-count-5", cwd, tested_source_revision, normal_root, args.child_timeout_seconds)
    race = run_child(race_cmd, "c21-race-count-1", cwd, tested_source_revision, race_root, args.child_timeout_seconds)
    for label, result, artifact_root in (("normal", normal, normal_root), ("race", race, race_root)):
        require(result["returncode"] == 0 and not result["timed_out"] and result["output_bounded"], f"C21 {label} regression failed: {read_child_text(result, 'stderr')[-2000:]}")
        require(result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"] and result["cleanup"].get("pipes_reaped") is True and not result["cleanup"].get("group_members_after") and result["cleanup"].get("descendant_processes_reaped") is True and not result["cleanup"].get("descendants_after"), f"C21 {label} cleanup failed")
        result["artifact_disk"] = artifact_disk_usage(artifact_root)
    value = {
        "schema": "audio-runtime.c68.zero-audio-attribution.c21.v1", "task": TASK,
        "source_revision": tested_source_revision, "candidate_head": tested_source_revision, "runner_sha256": sha256_file(Path(__file__)), "test": "TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls",
        "normal": normal, "race": race, "normal_count": 5, "race_count": 1, "passed": True,
        "scope": "focused accumulated C21 normal/race regression only",
    }
    write_json(REGRESSIONS_PATH, value)
    comparison = load_json(COMPARISON_PATH)
    write_attribution(comparison)
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
    cleanup_parser = sub.add_parser("cleanup-control")
    revisions(cleanup_parser)
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
        elif args.command == "cleanup-control":
            cleanup_control(args)
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
