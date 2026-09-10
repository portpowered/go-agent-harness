#!/usr/bin/env python3
"""Build and run the bounded exported PCM16 frame-sizing consumer."""

from __future__ import annotations

import argparse
from dataclasses import dataclass, field
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


REPO_ROOT = Path(__file__).resolve().parents[5]
EVIDENCE = Path(__file__).resolve().parent
ARTIFACTS = EVIDENCE / "artifacts"
RUNS = EVIDENCE / "runs"
CONSUMER = EVIDENCE / "consumer"
BINARY = ARTIFACTS / "pcm16-frame-consumer"
GO_AUDIO_MODULE = "github.com/portpowered/go-agent-harness/go-audio"
CHILD_TIMEOUT_SECONDS = 10
CHILD_TERM_GRACE_SECONDS = 2
CHILD_KILL_GRACE_SECONDS = 2
OUTPUT_CAPTURE_LIMIT = 64 * 1024


class VerificationError(RuntimeError):
    pass


@dataclass
class CappedOutput:
    limit: int
    data: bytearray = field(default_factory=bytearray)
    total_bytes: int = 0
    truncated: bool = False
    read_error: str = ""

    def append(self, chunk: bytes) -> None:
        self.total_bytes += len(chunk)
        remaining = self.limit - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total_bytes > self.limit:
            self.truncated = True

    def text(self) -> str:
        output = bytes(self.data).decode("utf-8", errors="replace")
        if self.truncated:
            output += f"\n[output truncated after {self.limit} bytes; read {self.total_bytes} bytes]\n"
        if self.read_error:
            output += f"\n[output reader error: {self.read_error}]\n"
        return output


def read_capped(stream: Any, output: CappedOutput) -> None:
    try:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            output.append(chunk)
    except (OSError, ValueError) as error:
        output.read_error = str(error)


def signal_process_group(process: subprocess.Popen[bytes], sig: signal.Signals) -> bool:
    try:
        if os.name == "posix":
            os.killpg(process.pid, sig)
        elif process.poll() is None:
            process.send_signal(sig)
        else:
            return False
    except ProcessLookupError:
        return False
    return True


def close_process_streams(process: subprocess.Popen[bytes]) -> None:
    for stream in (process.stdout, process.stderr):
        if stream is not None:
            try:
                stream.close()
            except OSError:
                pass


def run_bounded_process(
    command: list[str],
    *,
    cwd: Path,
    env: dict[str, str],
    timeout_seconds: float,
    term_grace_seconds: float = CHILD_TERM_GRACE_SECONDS,
    kill_grace_seconds: float = CHILD_KILL_GRACE_SECONDS,
    output_limit: int = OUTPUT_CAPTURE_LIMIT,
) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=False,
        start_new_session=True,
        preexec_fn=limit_child_resources,
    )
    stdout = CappedOutput(output_limit)
    stderr = CappedOutput(output_limit)
    readers = [
        threading.Thread(target=read_capped, args=(process.stdout, stdout), name="c30-stdout-reader", daemon=True),
        threading.Thread(target=read_capped, args=(process.stderr, stderr), name="c30-stderr-reader", daemon=True),
    ]
    for reader in readers:
        reader.start()

    timed_out = False
    cleanup: dict[str, Any] = {
        "sigterm_sent": False,
        "sigkill_sent": False,
        "reaped_after_sigterm": False,
        "reaped_after_sigkill": False,
        "reader_threads_stopped": False,
    }
    try:
        try:
            process.wait(timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            cleanup["sigterm_sent"] = signal_process_group(process, signal.SIGTERM)
            try:
                process.wait(timeout=term_grace_seconds)
                cleanup["reaped_after_sigterm"] = True
            except subprocess.TimeoutExpired:
                cleanup["sigkill_sent"] = signal_process_group(process, signal.SIGKILL)
                try:
                    process.wait(timeout=kill_grace_seconds)
                    cleanup["reaped_after_sigkill"] = True
                except subprocess.TimeoutExpired:
                    cleanup["reap_error"] = f"process did not exit within {kill_grace_seconds}s after SIGKILL"
    finally:
        for reader in readers:
            reader.join(timeout=kill_grace_seconds if timed_out else term_grace_seconds)
        if any(reader.is_alive() for reader in readers):
            cleanup["reader_threads_stopped"] = False
            close_process_streams(process)
            for reader in readers:
                reader.join(timeout=0.25)
        else:
            cleanup["reader_threads_stopped"] = True
        if process.poll() is None:
            cleanup["reap_error"] = cleanup.get("reap_error", "process remained alive after bounded cleanup")
        close_process_streams(process)

    return {
        "argv": command,
        "cwd": str(cwd),
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "clean_shutdown": not timed_out and process.returncode is not None,
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "stdout_bytes": stdout.total_bytes,
        "stderr_bytes": stderr.total_bytes,
        "stdout_truncated": stdout.truncated,
        "stderr_truncated": stderr.truncated,
        "output_limit_bytes": output_limit,
        "cleanup": cleanup,
    }


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
    process_record = run_bounded_process(
        command,
        cwd=run_dir,
        env=env,
        timeout_seconds=CHILD_TIMEOUT_SECONDS,
    )
    record = {
        "argv": command,
        "cwd": str(run_dir),
        "source_root": str(source_root),
        "source_module": str(source_root / "go-audio"),
        "source_revision": build_record["source_revision"],
        "duration_seconds": process_record["duration_seconds"],
        "exit_code": process_record["exit_code"],
        "stdout": process_record["stdout"],
        "stderr": process_record["stderr"],
        "stdout_bytes": process_record["stdout_bytes"],
        "stderr_bytes": process_record["stderr_bytes"],
        "stdout_truncated": process_record["stdout_truncated"],
        "stderr_truncated": process_record["stderr_truncated"],
        "output_limit_bytes": process_record["output_limit_bytes"],
        "timed_out": process_record["timed_out"],
        "clean_shutdown": process_record["clean_shutdown"],
        "cleanup": process_record["cleanup"],
        "executable": str(BINARY),
        "executable_sha256": sha256_file(BINARY),
        "fixture_sha256": fixture_hashes(),
    }
    (run_dir / "result.json").write_text(json.dumps(record, indent=2) + "\n")
    if process_record["timed_out"]:
        raise VerificationError(
            f"{mode} timed out after {CHILD_TIMEOUT_SECONDS}s; "
            f"bounded cleanup={process_record['cleanup']}; "
            f"stdout={process_record['stdout']}; stderr={process_record['stderr']}"
        )
    if process_record["exit_code"] is None:
        raise VerificationError(f"{mode} process was not reaped after bounded cleanup: {process_record['cleanup']}")
    return record


def run_timeout_control() -> dict[str, Any]:
    if os.name != "posix":
        raise VerificationError("timeout-control requires POSIX process-group signals")
    RUNS.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix="timeout-control-", dir=RUNS))
    child_script = (
        "import signal, sys, time; "
        "signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "payload = b'x' * (1024 * 1024); "
        "sys.stdout.buffer.write(payload); sys.stdout.buffer.flush(); "
        "sys.stderr.buffer.write(payload); sys.stderr.buffer.flush(); "
        "time.sleep(60)"
    )
    command = [sys.executable, "-c", child_script]
    process_record = run_bounded_process(
        command,
        cwd=run_dir,
        env=os.environ.copy(),
        timeout_seconds=0.25,
        term_grace_seconds=0.25,
        kill_grace_seconds=0.5,
        output_limit=4096,
    )
    record = {
        "control": "ignored-SIGTERM child with 1MiB stdout/stderr",
        "argv": command,
        "cwd": str(run_dir),
        **process_record,
    }
    cleanup = process_record["cleanup"]
    checks = {
        "timed_out": process_record["timed_out"],
        "stdout_capped": process_record["stdout_truncated"] and process_record["stdout_bytes"] >= 1024 * 1024,
        "stderr_capped": process_record["stderr_truncated"] and process_record["stderr_bytes"] >= 1024 * 1024,
        "sigterm_sent": cleanup.get("sigterm_sent", False),
        "sigkill_sent": cleanup.get("sigkill_sent", False),
        "reaped_after_sigkill": cleanup.get("reaped_after_sigkill", False),
        "reader_threads_stopped": cleanup.get("reader_threads_stopped", False),
        "process_reaped": process_record["exit_code"] is not None,
        "bounded_duration": process_record["duration_seconds"] < 5,
    }
    record["checks"] = checks
    (run_dir / "result.json").write_text(json.dumps(record, indent=2) + "\n")
    if not all(checks.values()):
        raise VerificationError(f"timeout control failed: {record}")
    print(json.dumps({"status": "pass", "timeout_control": record}, indent=2))
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
    parser.add_argument("--timeout-control", action="store_true")
    parser.add_argument("--source-root", type=Path, default=REPO_ROOT)
    args = parser.parse_args()
    source_root = validate_source_root(args.source_root)
    selected_modes = sum(bool(mode) for mode in (args.build, args.positive, args.negative_control, args.timeout_control))
    if selected_modes == 0:
        raise VerificationError("choose --build, --positive, --negative-control, or --timeout-control")
    if selected_modes > 1:
        raise VerificationError("choose exactly one runner mode")

    if args.timeout_control:
        run_timeout_control()
        return 0

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
