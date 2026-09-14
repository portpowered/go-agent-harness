#!/usr/bin/env python3
"""Run bounded credential-free C95 runtime regressions and launch shipped YUI."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import threading
import time


EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "--show-toplevel"],
        cwd=EVIDENCE,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
).resolve()
CLI_ROOT = REPO_ROOT / "agent-cli"
YUI = EVIDENCE / "artifacts" / "yui"
RUNS = EVIDENCE / "runs"
MAX_OUTPUT_BYTES = 64 * 1024
DEFAULT_CHILD_TIMEOUT = 60.0
DEFAULT_AGGREGATE_TIMEOUT = 300.0

CASE_COMMANDS = {
    "room-ingress-replay": [
        "rtk", "proxy", "env", "GOWORK=off", "go", "test",
        "./internal/services/internal/agentruntime",
        "-run", "^TestCleanTurnTakingRoomReplayFixturePassesAudioProperties$",
        "-count=1", "-timeout=60s",
    ],
    "room-ingress-rejection": [
        "rtk", "proxy", "env", "GOWORK=off", "go", "test",
        "./internal/services/internal/agentruntime",
        "-run", "^(TestRunRoom_ProviderInputRejectionPreservesPeerAttributionAndArtifact|TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress)$",
        "-count=1", "-timeout=60s",
    ],
    "non-room-audio-tool": [
        "rtk", "proxy", "env", "GOWORK=off", "go", "test",
        "./internal/services/internal/agentruntime",
        "-run", "^(TestRunSessionWithAudioOut|TestRTCDeviceSinkToolContinuation)",
        "-count=1", "-timeout=60s",
    ],
}


class RunnerError(RuntimeError):
    pass


class CappedOutput:
    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.data = bytearray()
        self.total = 0
        self.truncated = False
        self.error = ""

    def append(self, chunk: bytes) -> None:
        self.total += len(chunk)
        remaining = self.limit - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total > self.limit:
            self.truncated = True

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        if self.truncated:
            value += f"\n[output truncated after {self.limit} bytes]\n"
        if self.error:
            value += f"\n[output reader error: {self.error}]\n"
        return value


def read_stream(stream: object, output: CappedOutput) -> None:
    try:
        while True:
            chunk = stream.read(8192)  # type: ignore[attr-defined]
            if not chunk:
                return
            output.append(chunk)
    except (OSError, ValueError) as error:
        output.error = str(error)


def group_alive(process: subprocess.Popen[bytes]) -> bool:
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def stop_group(process: subprocess.Popen[bytes]) -> list[str]:
    sent: list[str] = []
    if group_alive(process):
        try:
            os.killpg(process.pid, signal.SIGTERM)
            sent.append("SIGTERM")
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        if group_alive(process):
            try:
                os.killpg(process.pid, signal.SIGKILL)
                sent.append("SIGKILL")
            except ProcessLookupError:
                pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass
    return sent


def child_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = "off"
    environment["LC_ALL"] = "C"
    environment["LANG"] = "C"
    return environment


def run_child(label: str, argv: list[str], cwd: Path, child_timeout: float, aggregate_timeout: float, started: float) -> dict[str, object]:
    if time.monotonic() - started >= aggregate_timeout:
        raise RunnerError(f"aggregate timeout expired before {label}")
    output = CappedOutput(MAX_OUTPUT_BYTES)
    error = CappedOutput(MAX_OUTPUT_BYTES)
    child_started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=child_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name != "nt",
    )
    readers = [
        threading.Thread(target=read_stream, args=(process.stdout, output), daemon=True),
        threading.Thread(target=read_stream, args=(process.stderr, error), daemon=True),
    ]
    for reader in readers:
        reader.start()
    timed_out = False
    signals: list[str] = []
    child_budget = min(child_timeout, max(0.1, aggregate_timeout - (time.monotonic() - started)))
    try:
        process.wait(timeout=child_budget)
    except subprocess.TimeoutExpired:
        timed_out = True
        signals = stop_group(process)
    for reader in readers:
        reader.join(timeout=3)
    if process.poll() is None:
        signals.extend(stop_group(process))
    group_after = group_alive(process)
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd.relative_to(REPO_ROOT)),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - child_started) * 1000),
        "stdout": output.text(),
        "stderr": error.text(),
        "stdout_bytes": output.total,
        "stderr_bytes": error.total,
        "output_bounded": not output.truncated and not error.truncated,
        "cleanup": {
            "parent_reaped": process.poll() is not None,
            "reader_threads_joined": all(not reader.is_alive() for reader in readers),
            "group_alive_after": group_after,
            "signals": signals,
        },
    }
    if timed_out or process.returncode != 0 or group_after or not result["cleanup"]["reader_threads_joined"] or not result["output_bounded"]:
        raise RunnerError(f"{label} failed bounded execution: {json.dumps(result, sort_keys=True)}")
    return result


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_yui_room_example(
    run_dir: Path,
    child_timeout: float,
    aggregate_timeout: float,
    started: float,
) -> dict[str, object]:
    """Exercise the built public room command before the focused Go cases."""
    case = run_dir / "yui-room-example"
    config = case / "config"
    config.mkdir(parents=True, exist_ok=True)
    execution = run_child(
        "yui-room-example",
        [
            str(YUI),
            "--config-dir",
            str(config),
            "--workdir",
            str(case),
            "--allow-path",
            str(case),
            "room",
            "run",
            "--example",
        ],
        case,
        child_timeout,
        aggregate_timeout,
        started,
    )
    lines = [line for line in str(execution["stdout"]).splitlines() if line.strip()]
    try:
        manifest = json.loads("\n".join(lines))
    except json.JSONDecodeError as error:
        raise RunnerError(f"yui room example was not JSON: {error}") from error
    participants = manifest.get("participants") if isinstance(manifest, dict) else None
    if not isinstance(manifest, dict) or manifest.get("schema_version") != 1 or not isinstance(participants, list) or len(participants) < 2:
        raise RunnerError("yui room example did not expose a valid participant manifest")
    execution["manifest"] = manifest
    return execution


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=sorted(CASE_COMMANDS), required=True)
    parser.add_argument("--child-timeout", type=float, default=DEFAULT_CHILD_TIMEOUT)
    parser.add_argument("--aggregate-timeout", type=float, default=DEFAULT_AGGREGATE_TIMEOUT)
    args = parser.parse_args()
    if not 0 < args.child_timeout <= DEFAULT_CHILD_TIMEOUT or not 0 < args.aggregate_timeout <= DEFAULT_AGGREGATE_TIMEOUT:
        raise SystemExit("timeouts exceed the C95 bounded runner limits")
    run_dir = RUNS / f"c95-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    started = time.monotonic()
    try:
        YUI.parent.mkdir(parents=True, exist_ok=True)
        build = run_child(
            "build-yui",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(YUI), "./cmd/yui"],
            CLI_ROOT,
            args.child_timeout,
            args.aggregate_timeout,
            started,
        )
        if not YUI.is_file():
            raise RunnerError("YUI build did not produce an artifact")
        cases = [run_yui_room_example(run_dir, args.child_timeout, args.aggregate_timeout, started)]
        for name in args.case:
            case = run_child(name, CASE_COMMANDS[name], CLI_ROOT, args.child_timeout, args.aggregate_timeout, started)
            cases.append(case)
        outcome = {
            "schema": "audio-runtime.c95.runner.v1",
            "passed": True,
            "candidate_revision": subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout.strip(),
            "yui": {"path": str(YUI.relative_to(EVIDENCE)), "bytes": YUI.stat().st_size, "sha256": sha256(YUI), "build": build},
            "cases": cases,
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
        }
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(outcome, sort_keys=True))
        return 0
    except (OSError, subprocess.SubprocessError, RunnerError) as error:
        print(json.dumps({"schema": "audio-runtime.c95.runner.v1", "passed": False, "error": str(error)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
