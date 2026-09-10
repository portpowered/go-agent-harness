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


def build(source_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    started = time.monotonic()
    env = os.environ.copy()
    env["GOWORK"] = "off"
    command = ["go", "build", "-trimpath", "-o", str(BINARY), "."]
    result = subprocess.run(command, cwd=CONSUMER, env=env, check=False, capture_output=True, text=True)
    record = {
        "argv": command,
        "cwd": str(CONSUMER),
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
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
    if not BINARY.is_file():
        raise VerificationError(f"missing consumer executable: {BINARY}; run --build first")
    RUNS.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix=f"{mode}-", dir=RUNS))
    command = [str(BINARY), "--mode", mode]
    env = os.environ.copy()
    env["C30_SOURCE_REVISION"] = git_output(source_root, "rev-parse", "HEAD")
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
        "source_revision": env["C30_SOURCE_REVISION"],
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


def verify_positive(record: dict[str, Any]) -> None:
    if record["exit_code"] != 0:
        raise VerificationError(f"positive exited {record['exit_code']}: {record['stderr']}")
    try:
        payload = json.loads(record["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationError(f"positive did not produce JSON: {record['stdout']}") from exc
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
    source_root = args.source_root.resolve()
    if not (source_root / "go.work").is_file() or not (source_root / "go-audio").is_dir():
        raise VerificationError(f"source root is not the go-agent-harness checkout: {source_root}")
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
