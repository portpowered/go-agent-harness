#!/usr/bin/env python3
"""Bounded C67 shipped-composition and credential-free replay runner."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = next(parent for parent in HERE.parents if (parent / "go.work").is_file())
CONSUMER = HERE / "consumer"
C40_REPLAY = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py"
RUNS = HERE / "runs"
OUTPUT_CAP = 1 << 20
CHILD_TIMEOUT = 240
TOTAL_TIMEOUT = 600


class RunFailure(RuntimeError):
    pass


def clean_environment() -> dict[str, str]:
    result = dict(os.environ)
    markers = ("API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    for name in list(result):
        if any(marker in name.upper() for marker in markers):
            del result[name]
    result["GOWORK"] = "off"
    return result


def sha256(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def group_gone(process_id: int) -> bool:
    if os.name == "nt":
        return True
    try:
        os.killpg(process_id, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def stop_group(process: subprocess.Popen[bytes]) -> None:
    if os.name == "nt":
        process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=2)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()


class Runner:
    def __init__(self, mode: str) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        self.run_dir = RUNS / f"process-{stamp}-{os.getpid()}"
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.mode = mode
        self.started = time.monotonic()
        self.records: list[dict[str, Any]] = []

    def run(self, label: str, argv: list[str], cwd: Path) -> dict[str, Any]:
        remaining = TOTAL_TIMEOUT - (time.monotonic() - self.started)
        if remaining <= 0:
            raise RunFailure("aggregate runner deadline exceeded")
        started = time.monotonic()
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=clean_environment(),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=os.name != "nt",
        )
        timed_out = False
        try:
            stdout, stderr = process.communicate(timeout=min(CHILD_TIMEOUT, remaining))
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = exc.stdout or b""
            stderr = exc.stderr or b""
            stop_group(process)
            stdout, stderr = process.communicate()
        stdout = stdout if isinstance(stdout, bytes) else stdout.encode()
        stderr = stderr if isinstance(stderr, bytes) else stderr.encode()
        safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
        stdout_path = self.run_dir / f"{len(self.records) + 1:03d}-{safe}.stdout.log"
        stderr_path = self.run_dir / f"{len(self.records) + 1:03d}-{safe}.stderr.log"
        stdout_path.write_bytes(stdout[:OUTPUT_CAP])
        stderr_path.write_bytes(stderr[:OUTPUT_CAP])
        record = {
            "label": label,
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": process.returncode,
            "timed_out": timed_out,
            "stdout_bytes": len(stdout),
            "stderr_bytes": len(stderr),
            "stdout_sha256": sha256(stdout),
            "stderr_sha256": sha256(stderr),
            "stdout_path": str(stdout_path),
            "stderr_path": str(stderr_path),
            "process_group_gone": group_gone(process.pid),
            "elapsed_seconds": round(time.monotonic() - started, 6),
        }
        self.records.append(record)
        if timed_out or len(stdout) > OUTPUT_CAP or len(stderr) > OUTPUT_CAP or process.returncode != 0 or not record["process_group_gone"]:
            raise RunFailure(f"{label} failed bounded shipped-run contract: {record}")
        return record

    def save(self) -> Path:
        path = self.run_dir / "outcome.json"
        path.write_text(json.dumps({
            "schema": "audio-runtime-c67-shipped-run/v1",
            "mode": self.mode,
            "candidate_revision": subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "HEAD"], text=True).strip(),
            "records": self.records,
        }, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return path


def case_commands(case: str) -> list[tuple[str, list[str], Path]]:
    if case == "shipped-image-tool":
        return [
            ("shipped-image-tool", ["go", "test", "./test/integration", "-run", "^TestReadImageCLI_DefaultLifecycleWaitsForStrictContinuation$", "-count=1", "-timeout=180s"], ROOT / "agent-cli"),
        ]
    if case == "invalid-image-negative":
        return [
            ("invalid-image-negative", ["go", "run", ".", "negative"], CONSUMER),
            ("invalid-image-replay-negative", ["go", "test", "./test/integration", "-run", "^TestReadImageCLI_DefaultLifecycleRejectsEmptyFunctionOutput$", "-count=1", "-timeout=180s"], ROOT / "agent-cli"),
        ]
    if case == "recording-directory-regression":
        return [
            ("recording-directory-regression", ["go", "test", "./test/integration", "-run", "^TestSessionCommand_LiveScheduledImageAudioAttachesImagesToFirstTurn$", "-count=1", "-timeout=180s"], ROOT / "agent-cli"),
        ]
    if case == "credential-free-audio-tool-regression":
        return [("credential-free-audio-tool-regression", [sys.executable, str(C40_REPLAY), "replay-regression"], ROOT)]
    raise RunFailure(f"unknown run case {case}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=("shipped-image-tool", "invalid-image-negative", "recording-directory-regression", "credential-free-audio-tool-regression", "all"), required=True)
    args = parser.parse_args()
    cases = ("shipped-image-tool", "invalid-image-negative", "recording-directory-regression", "credential-free-audio-tool-regression") if args.case == "all" else (args.case,)
    runner = Runner(args.case)
    try:
        for case in cases:
            for label, command, cwd in case_commands(case):
                runner.run(label, command, cwd)
        path = runner.save()
        print(json.dumps({"decision": "PASS", "case": args.case, "report": str(path)}, sort_keys=True))
        return 0
    except (RunFailure, OSError, subprocess.SubprocessError) as exc:
        path = runner.save()
        print(json.dumps({"decision": "FAIL", "case": args.case, "report": str(path), "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
