#!/usr/bin/env python3
"""Build and run the bounded exported PCM16 frame-sizing consumer."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any


REPO_ROOT = Path(__file__).resolve().parents[5]
EVIDENCE = Path(__file__).resolve().parent
ARTIFACTS = EVIDENCE / "artifacts"
RUNS = EVIDENCE / "runs"
CONSUMER = EVIDENCE / "consumer"
BINARY = ARTIFACTS / "pcm16-frame-consumer"
GO_AUDIO_MODULE = "github.com/portpowered/go-agent-harness/go-audio"


class VerificationError(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def fixture_hashes() -> dict[str, str]:
    return {
        str(path.relative_to(EVIDENCE)): sha256_file(path)
        for path in sorted(CONSUMER.iterdir())
        if path.is_file()
    }


def git_output(source_root: Path, *args: str) -> str:
    result = subprocess.run(["git", "-C", str(source_root), *args], check=True, capture_output=True, text=True)
    return result.stdout.strip()


def validate_source_root(source_root: Path) -> Path:
    source_root = source_root.resolve()
    if not (source_root / "go.work").is_file() or not (source_root / "go-audio" / "go.mod").is_file():
        raise VerificationError(f"source root is not the go-agent-harness checkout: {source_root}")
    try:
        git_root = Path(git_output(source_root, "rev-parse", "--show-toplevel")).resolve()
    except subprocess.CalledProcessError as error:
        raise VerificationError(f"source root is not a Git checkout: {source_root}") from error
    if git_root != source_root:
        raise VerificationError(f"source root must be the checkout root: {source_root}")
    return source_root


def create_source_modfile(source_root: Path) -> tempfile.TemporaryDirectory[str]:
    temporary = tempfile.TemporaryDirectory(prefix="c30-mod-")
    temporary_root = Path(temporary.name)
    module_text = (CONSUMER / "go.mod").read_text()
    replacement_prefix = f"replace {GO_AUDIO_MODULE} =>"
    replacement_lines = [line for line in module_text.splitlines() if line.startswith(replacement_prefix)]
    if len(replacement_lines) != 1:
        temporary.cleanup()
        raise VerificationError(f"consumer go.mod must contain exactly one {GO_AUDIO_MODULE} replacement")
    rewritten = []
    for line in module_text.splitlines(keepends=True):
        if line.startswith(replacement_prefix):
            newline = "\n" if line.endswith("\n") else ""
            rewritten.append(f"replace {GO_AUDIO_MODULE} => {source_root / 'go-audio'}{newline}")
        else:
            rewritten.append(line)
    (temporary_root / "go.mod").write_text("".join(rewritten))
    (temporary_root / "go.sum").write_bytes((CONSUMER / "go.sum").read_bytes())
    return temporary


def build(source_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    source_revision = git_output(source_root, "rev-parse", "HEAD")
    started = time.monotonic()
    env = os.environ.copy()
    env["GOWORK"] = "off"
    with create_source_modfile(source_root) as temporary_mod_root, tempfile.TemporaryDirectory(prefix="c30-build-") as temporary_build_root:
        temporary_binary = Path(temporary_build_root) / BINARY.name
        modfile = Path(temporary_mod_root) / "go.mod"
        command = [
            "go",
            "build",
            "-trimpath",
            f"-ldflags=-X main.compiledSourceRevision={source_revision}",
            "-modfile",
            str(modfile),
            "-o",
            str(temporary_binary),
            ".",
        ]
        result = subprocess.run(command, cwd=CONSUMER, env=env, check=False, capture_output=True, text=True)
        if result.returncode == 0:
            os.replace(temporary_binary, BINARY)
    record = {
        "argv": command,
        "cwd": str(CONSUMER),
        "source_root": str(source_root),
        "source_module": str(source_root / "go-audio"),
        "source_revision": source_revision,
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": result.returncode,
        "stdout": result.stdout,
        "stderr": result.stderr,
        "fixture_sha256": fixture_hashes(),
    }
    if result.returncode != 0:
        raise VerificationError(f"build exited {result.returncode}: {result.stderr}")
    record["executable"] = str(BINARY)
    record["executable_sha256"] = sha256_file(BINARY)
    record["executable_bytes"] = BINARY.stat().st_size
    (ARTIFACTS / "build.json").write_text(json.dumps(record, indent=2) + "\n")
    return record


def limit_child_resources() -> None:
    try:
        import resource

        memory = 256 * 1024 * 1024
        resource.setrlimit(resource.RLIMIT_AS, (memory, memory))
        resource.setrlimit(resource.RLIMIT_CPU, (30, 30))
    except (ImportError, OSError, ValueError):
        # The watchdog remains mandatory on platforms without these limits.
        pass


def run_child(source_root: Path, mode: str) -> dict[str, Any]:
    build_record = require_matching_build(source_root)
    RUNS.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix=f"{mode}-", dir=RUNS))
    command = [str(BINARY), "--mode", mode]
    env = os.environ.copy()
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=run_dir,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
        preexec_fn=limit_child_resources,
    )
    try:
        stdout, stderr = process.communicate(timeout=10)
    except subprocess.TimeoutExpired as exc:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
        raise VerificationError(f"{mode} timed out after 10s; stdout={stdout}; stderr={stderr}") from exc
    record = {
        "argv": command,
        "cwd": str(run_dir),
        "source_root": str(source_root),
        "source_module": str(source_root / "go-audio"),
        "source_revision": build_record["source_revision"],
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
        "clean_shutdown": process.returncode != -signal.SIGKILL,
        "executable": str(BINARY),
        "executable_sha256": sha256_file(BINARY),
        "fixture_sha256": fixture_hashes(),
    }
    (run_dir / "result.json").write_text(json.dumps(record, indent=2) + "\n")
    return record


def load_build_record() -> dict[str, Any] | None:
    path = ARTIFACTS / "build.json"
    if not path.is_file():
        return None
    try:
        record = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return None
    return record if isinstance(record, dict) else None


def require_matching_build(source_root: Path) -> dict[str, Any]:
    record = load_build_record()
    if record is None:
        raise VerificationError("missing or invalid artifacts/build.json; run --build first")
    source_revision = git_output(source_root, "rev-parse", "HEAD")
    expected_root = str(source_root)
    expected_module = str(source_root / "go-audio")
    if record.get("source_root") != expected_root or record.get("source_module") != expected_module:
        raise VerificationError(
            "build provenance source root mismatch: "
            f"built_root={record.get('source_root')!r} requested_root={expected_root!r}"
        )
    if record.get("source_revision") != source_revision:
        raise VerificationError(
            "build provenance revision mismatch: "
            f"built_revision={record.get('source_revision')!r} requested_revision={source_revision!r}"
        )
    if record.get("fixture_sha256") != fixture_hashes():
        raise VerificationError("build provenance fixture hash mismatch; run --build again")
    if not BINARY.is_file():
        raise VerificationError(f"missing consumer executable: {BINARY}; run --build first")
    executable_sha256 = sha256_file(BINARY)
    if record.get("executable_sha256") != executable_sha256:
        raise VerificationError("build provenance executable hash mismatch; run --build again")
    return record


def verify_positive(record: dict[str, Any]) -> None:
    if record["exit_code"] != 0:
        raise VerificationError(f"positive exited {record['exit_code']}: {record['stderr']}")
    try:
        payload = json.loads(record["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationError(f"positive did not produce JSON: {record['stdout']}") from exc
    if payload.get("source") != record.get("source_revision"):
        raise VerificationError(
            f"positive source mismatch: child={payload.get('source')!r} expected={record.get('source_revision')!r}"
        )
    if not payload.get("passed") or not payload.get("clean_shutdown"):
        raise VerificationError(f"positive report failed: {payload}")
    if not payload.get("cases") or any(not case.get("passed") for case in payload["cases"]):
        raise VerificationError(f"positive case report failed: {payload}")


def verify_negative(record: dict[str, Any]) -> None:
    if record["exit_code"] == 0:
        raise VerificationError("negative control was accepted with exit code 0")
    combined = f"{record['stdout']}\n{record['stderr']}"
    for expected in ("actual_samples=480", "mutated_expected=481"):
        if expected not in combined:
            raise VerificationError(f"negative control missing {expected!r}: {combined}")
    try:
        payload = json.loads(record["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationError(f"negative control did not produce JSON: {record['stdout']}") from exc
    if payload.get("source") != record.get("source_revision"):
        raise VerificationError(
            f"negative source mismatch: child={payload.get('source')!r} expected={record.get('source_revision')!r}"
        )
    if not payload.get("negative_control", {}).get("passed"):
        raise VerificationError(f"negative control report did not prove mismatch: {payload}")


def write_manifest(source_root: Path, build_record: dict[str, Any] | None, run_record: dict[str, Any] | None) -> None:
    if build_record is None:
        build_record = load_build_record()
    manifest = {
        "schema": "audio-runtime-c30-frame-dimension-artifact.v1",
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
        "go_version": subprocess.run(["go", "version"], check=True, capture_output=True, text=True).stdout.strip(),
        "platform": {"python": sys.version, "os": os.name},
        "executable": {
            "path": str(BINARY),
            "bytes": BINARY.stat().st_size if BINARY.is_file() else 0,
            "sha256": sha256_file(BINARY) if BINARY.is_file() else "",
        },
        "fixtures": fixture_hashes(),
        "build": build_record,
        "run": run_record,
    }
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    (ARTIFACTS / "artifact-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--positive", action="store_true")
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument("--source-root", type=Path, default=REPO_ROOT)
    args = parser.parse_args()
    source_root = validate_source_root(args.source_root)
    if not (args.build or args.positive or args.negative_control):
        raise VerificationError("choose --build, --positive, or --negative-control")

    build_record = build(source_root) if args.build else None
    run_record = None
    if args.positive and args.negative_control:
        raise VerificationError("choose one child mode per invocation")
    if args.positive or args.negative_control:
        if not BINARY.is_file() and args.build:
            build_record = build(source_root)
        run_record = run_child(source_root, "positive" if args.positive else "negative-control")
        if args.positive:
            verify_positive(run_record)
        else:
            verify_negative(run_record)
    write_manifest(source_root, build_record, run_record)
    print(json.dumps({"status": "pass", "source_revision": git_output(source_root, "rev-parse", "HEAD"), "run": run_record}, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationError as error:
        print(json.dumps({"status": "failed", "error": str(error)}, indent=2))
        raise SystemExit(1)
