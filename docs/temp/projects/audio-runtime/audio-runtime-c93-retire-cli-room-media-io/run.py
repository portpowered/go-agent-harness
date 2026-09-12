#!/usr/bin/env python3
"""Run bounded, credential-free C93 room and audio/tool executable probes."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from typing import Any


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "--show-toplevel"],
        cwd=TASK_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
ARTIFACTS = TASK_ROOT / "artifacts"
REPORTS = TASK_ROOT / "reports"
RUNS = TASK_ROOT / "runs"
YUI_BINARY = ARTIFACTS / "yui"
CONSUMER_ROOT = TASK_ROOT / "external-consumer"
CONSUMER_BINARY = ARTIFACTS / "roommedia-consumer"
FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 240.0
MAX_OUTPUT_BYTES = 64 * 1024
TERM_GRACE_SECONDS = 2.0
KILL_GRACE_SECONDS = 2.0
PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"


class RunnerError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RunnerError(message)


def git(*args: str) -> str:
    return subprocess.run(
        ["rtk", "proxy", "git", *args],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def source_digest() -> str:
    digest = hashlib.sha256()
    paths = [
        REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
        REPO_ROOT / "go-agent-runtime/services/roommedia/contract.go",
        REPO_ROOT / "go-agent-runtime/services/roommedia/internal/service/service.go",
        REPO_ROOT / "go-agent-runtime/services/roommedia/wire/providers.go",
        REPO_ROOT / "go-agent-runtime/services/roommedia/wire/wire_gen.go",
        TASK_ROOT / "external-consumer/main.go",
        TASK_ROOT / "external-consumer/go.mod",
    ]
    for path in paths:
        require(path.is_file(), f"source input is missing: {path}")
        digest.update(str(path.relative_to(REPO_ROOT)).encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def clean_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment.update({"GOWORK": "off", "LC_ALL": "C", "LANG": "C", "CGO_ENABLED": "0"})
    return environment


def group_alive(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


class Capture:
    def __init__(self) -> None:
        self.data = bytearray()
        self.total = 0
        self.overflow = False
        self.read_error = ""

    def append(self, chunk: bytes) -> None:
        self.total += len(chunk)
        remaining = MAX_OUTPUT_BYTES - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total > MAX_OUTPUT_BYTES:
            self.overflow = True

    def text(self) -> str:
        result = bytes(self.data).decode(errors="replace")
        if self.overflow:
            result += f"\n[output truncated after {MAX_OUTPUT_BYTES} bytes]\n"
        if self.read_error:
            result += f"\n[output reader error: {self.read_error}]\n"
        return result


def drain(stream: Any, capture: Capture) -> None:
    try:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            capture.append(chunk)
    except (OSError, ValueError) as error:
        capture.read_error = str(error)


def terminate_group(process: subprocess.Popen[bytes]) -> list[str]:
    signals: list[str] = []
    if group_alive(process.pid):
        try:
            os.killpg(process.pid, signal.SIGTERM)
            signals.append("SIGTERM")
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=TERM_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
            signals.append("SIGKILL")
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=KILL_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        pass
    return signals


def run_child(label: str, command: list[str | Path], cwd: Path, run_root: Path, budget: "Budget") -> dict[str, Any]:
    timeout = min(budget.child_timeout(), budget.remaining())
    require(timeout > 0, f"aggregate deadline expired before {label}")
    rendered = [str(item) for item in command]
    process = subprocess.Popen(
        rendered,
        cwd=cwd,
        env=clean_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    stdout = Capture()
    stderr = Capture()
    readers = [
        threading.Thread(target=drain, args=(process.stdout, stdout), daemon=True),
        threading.Thread(target=drain, args=(process.stderr, stderr), daemon=True),
    ]
    for reader in readers:
        reader.start()
    started = time.monotonic()
    timed_out = False
    overflow = False
    signals: list[str] = []
    try:
        deadline = time.monotonic() + timeout
        while process.poll() is None:
            if stdout.overflow or stderr.overflow:
                overflow = True
                signals.extend(terminate_group(process))
                break
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                signals.extend(terminate_group(process))
                break
            try:
                process.wait(timeout=min(0.1, remaining))
            except subprocess.TimeoutExpired:
                continue
        if process.poll() is None:
            signals.extend(terminate_group(process))
    finally:
        if process.poll() is None:
            signals.extend(terminate_group(process))
        for reader in readers:
            reader.join(timeout=KILL_GRACE_SECONDS)
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
    result = {
        "label": label,
        "command": rendered,
        "cwd": str(cwd.relative_to(REPO_ROOT)) if cwd.is_relative_to(REPO_ROOT) else str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "stdout_bytes": stdout.total,
        "stderr_bytes": stderr.total,
        "output_bounded": not overflow and not stdout.read_error and not stderr.read_error,
        "cleanup": {"parent_reaped": process.returncode is not None, "group_alive_after": group_alive(process.pid), "termination_signals": signals},
    }
    require(result["returncode"] == 0 and not timed_out and result["output_bounded"], f"{label} failed: {result['returncode']}")
    require(result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"], f"{label} left a process-group survivor")
    return result


class Budget:
    def __init__(self, child_timeout: float, aggregate_timeout: float) -> None:
        self.child_limit = child_timeout
        self.aggregate_limit = aggregate_timeout
        self.started = time.monotonic()

    def remaining(self) -> float:
        return self.aggregate_limit - (time.monotonic() - self.started)

    def child_timeout(self) -> float:
        return min(self.child_limit, self.remaining())


def artifact(path: Path) -> dict[str, Any]:
    require(path.is_file() and not path.is_symlink(), f"artifact is unavailable: {path}")
    return {"path": str(path.relative_to(TASK_ROOT)), "bytes": path.stat().st_size, "sha256": sha256_file(path)}


def build_yui(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_child("build-yui", ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", YUI_BINARY, "./cmd/yui"], REPO_ROOT / "agent-cli", run_root, budget)
    result["artifact"] = artifact(YUI_BINARY)
    return result


def build_consumer(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_child("build-external-consumer", ["rtk", "proxy", "env", "GOWORK=off", "go", "build", "-trimpath", "-o", CONSUMER_BINARY, "."], CONSUMER_ROOT, run_root, budget)
    result["artifact"] = artifact(CONSUMER_BINARY)
    return result


def parse_json_output(result: dict[str, Any], label: str) -> dict[str, Any]:
    lines = [line for line in result["stdout"].splitlines() if line.strip()]
    require(lines, f"{label} emitted no JSON")
    try:
        value = json.loads("\n".join(lines))
    except json.JSONDecodeError as error:
        raise RunnerError(f"{label} emitted invalid JSON: {error}") from error
    require(isinstance(value, dict), f"{label} JSON is not an object")
    return value


def write_report(name: str, payload: dict[str, Any]) -> None:
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / name).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run_room_media(budget: Budget, run_root: Path) -> dict[str, Any]:
    yui_build = build_yui(budget, run_root)
    consumer_build = build_consumer(budget, run_root)
    case = run_root / "room-media-io"
    (case / "config").mkdir(parents=True, exist_ok=True)
    room = run_child("room-media-io-room-example", [YUI_BINARY, "--config-dir", case / "config", "--workdir", case, "--allow-path", case, "room", "run", "--example"], case, run_root, budget)
    manifest = parse_json_output(room, "room-media-io room example")
    require(manifest.get("schema_version") == 1 and len(manifest.get("participants", [])) >= 2, "room example did not expose a participant manifest")
    consumer = run_child("room-media-io-external-consumer", [CONSUMER_BINARY], CONSUMER_ROOT, run_root, budget)
    report = {
        "schema": "audio-runtime.c93.room-media-io.v1",
        "passed": True,
        "candidate_head": git("rev-parse", "HEAD"),
        "source_digest": source_digest(),
        "credentials": "not_used",
        "physical_device": "not_attempted",
        "acoustic": "not_attempted",
        "builds": {"yui": yui_build, "external_consumer": consumer_build},
        "room_manifest": manifest,
        "executions": [room, consumer],
    }
    write_report("room-media-io.json", report)
    return report


def run_audio_tool_replay(budget: Budget, run_root: Path) -> dict[str, Any]:
    yui_build = build_yui(budget, run_root)
    require(FIXTURE.is_file(), f"audio/tool fixture is missing: {FIXTURE}")
    case = run_root / "audio-tool-replay"
    (case / "config").mkdir(parents=True, exist_ok=True)
    (case / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record = case / "record"
    execution = run_child("audio-tool-replay", [YUI_BINARY, "--config-dir", case / "config", "--workdir", case, "--allow-path", case, "session", "--replay", FIXTURE, "--replay-timing", "immediate", "--record-dir", record, "--trace-audio"], case, run_root, budget)
    combined = execution["stdout"].lower() + execution["stderr"].lower()
    require("replay mismatch" not in combined, "audio/tool replay reported a mismatch")
    manifest = record / "manifest.json"
    pcm = record / "audio" / "out-000.pcm"
    session_log = record / "session-log.jsonl"
    marker = case / "evidence" / "runs" / "exec-invocations-v4.log"
    require(manifest.is_file() and pcm.is_file() and session_log.is_file(), "audio/tool replay artifacts are incomplete")
    require(marker.is_file() and "PROBE_TOOL_MARKER_9182" in execution["stdout"], "audio/tool replay lost executable tool evidence")
    require(pcm.stat().st_size == 4800 and sha256_file(pcm) == PCM_SHA256, "audio/tool replay PCM oracle changed")
    manifest_data = json.loads(manifest.read_text(encoding="utf-8"))
    require(manifest_data.get("terminal", {}).get("terminal_provenance") in ("provider", "replay"), "audio/tool terminal provenance is missing")
    report = {
        "schema": "audio-runtime.c93.audio-tool-replay.v1",
        "passed": True,
        "candidate_head": git("rev-parse", "HEAD"),
        "source_digest": source_digest(),
        "fixture": str(FIXTURE.relative_to(REPO_ROOT)),
        "fixture_sha256": sha256_file(FIXTURE),
        "credentials": "not_used",
        "physical_device": "not_attempted",
        "acoustic": "not_attempted",
        "build": yui_build,
        "execution": execution,
        "manifest_sha256": sha256_file(manifest),
        "pcm_bytes": pcm.stat().st_size,
        "pcm_sha256": sha256_file(pcm),
        "session_log_sha256": sha256_file(session_log),
        "terminal": manifest_data["terminal"],
    }
    write_report("audio-tool-replay.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("room-media-io", "audio-tool-replay"))
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    require(0 < args.child_timeout <= MAX_CHILD_SECONDS, "child timeout must be at most 60 seconds")
    require(0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS, "aggregate timeout must be at most 240 seconds")
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_root = RUNS / f"{stamp}-{os.getpid()}"
    budget = Budget(args.child_timeout, args.aggregate_timeout)
    try:
        report = run_room_media(budget, run_root) if args.case == "room-media-io" else run_audio_tool_replay(budget, run_root)
        require(budget.remaining() >= 0, "aggregate evidence deadline expired")
        outcome = {"schema": "audio-runtime.c93.runner.v1", "passed": True, "case": args.case, "run_root": str(run_root.relative_to(TASK_ROOT)), "report": report, "aggregate_elapsed_ms": int((time.monotonic() - budget.started) * 1000)}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(outcome, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(outcome, sort_keys=True))
        return 0
    except (RunnerError, OSError, subprocess.SubprocessError) as error:
        outcome = {"schema": "audio-runtime.c93.runner.v1", "passed": False, "case": args.case, "error": str(error), "run_root": str(run_root.relative_to(TASK_ROOT))}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(outcome, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(outcome, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
