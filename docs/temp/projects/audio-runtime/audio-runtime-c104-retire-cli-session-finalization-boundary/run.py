#!/usr/bin/env python3
"""Run bounded, credential-free replays with the source-pinned yui binary."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import selectors
import shutil
import signal
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any, Callable, Sequence


ROOT = Path(__file__).resolve().parents[5]
TASK_ROOT = Path(__file__).resolve().parent
ARTIFACT = TASK_ROOT / "artifacts/yui"
HEALTHY = ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
INTERRUPTION = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-interruption.session.json"
AUDIO_TOOL = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"

OUTPUT_LIMIT = 64 * 1024
TERMINATION_GRACE_SECONDS = 2.0
SIGINT_AFTER_SECONDS = 0.08


class EvidenceFailure(RuntimeError):
    """Raised when shipped evidence is incomplete or outside its budget."""


class RunBudget:
    """One monotonic deadline shared by every selected replay case."""

    def __init__(self, seconds: float) -> None:
        self.started = time.monotonic()
        self.deadline = self.started + seconds

    def remaining(self) -> float:
        return self.deadline - time.monotonic()

    def require_time(self, label: str) -> None:
        if self.remaining() <= 0:
            raise EvidenceFailure(f"aggregate deadline exceeded before {label}")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def clean_environment() -> tuple[dict[str, str], list[str]]:
    environment = os.environ.copy()
    removed: list[str] = []
    credential_names = {
        "OPENAI_API_KEY",
        "OPENAI_API_BASE",
        "OPENAI_ORG_ID",
        "ANTHROPIC_API_KEY",
        "AZURE_OPENAI_API_KEY",
        "REALTIME_API_KEY",
        "YUI_API_KEY",
        "OPENROUTER_API_KEY",
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "AWS_SESSION_TOKEN",
    }
    for name in list(environment):
        if name in credential_names or name.endswith("_API_KEY"):
            removed.append(name)
            del environment[name]
    environment["GOWORK"] = "off"
    return environment, sorted(removed)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def scrub_temporary(value: Any, temporary: Path) -> Any:
    """Keep committed evidence stable after the temporary run tree is removed."""
    if isinstance(value, dict):
        return {key: scrub_temporary(item, temporary) for key, item in value.items()}
    if isinstance(value, list):
        return [scrub_temporary(item, temporary) for item in value]
    if isinstance(value, str):
        path = str(temporary)
        private_path = path.replace("/var/", "/private/var/")
        return value.replace(private_path, "$TMP").replace(path, "$TMP")
    return value


def git_revision() -> str:
    result = subprocess.run(
        ["git", "-C", str(ROOT), "rev-parse", "HEAD"],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    require(result.returncode == 0, result.stderr.strip() or "cannot identify candidate revision")
    return result.stdout.strip()


def process_group_alive(group_id: int) -> bool:
    if os.name != "posix":
        return False
    try:
        os.killpg(group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def signal_group(group_id: int, sig: signal.Signals) -> None:
    if os.name != "posix":
        return
    try:
        os.killpg(group_id, sig)
    except ProcessLookupError:
        pass


def run_command(
    label: str,
    argv: Sequence[str | Path],
    cwd: Path,
    environment: dict[str, str],
    removed_credentials: list[str],
    budget: RunBudget,
    child_timeout: float,
    *,
    on_tick: Callable[[], None] | None = None,
    sigint_after: float | None = None,
) -> dict[str, Any]:
    budget.require_time(label)
    command = [str(item) for item in argv]
    started = time.monotonic()
    timeout = min(child_timeout, budget.remaining())
    process = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name == "posix",
        text=False,
    )
    selector = selectors.DefaultSelector()
    buffers = {"stdout": bytearray(), "stderr": bytearray()}
    truncated = {"stdout": False, "stderr": False}
    termination_signals: list[str] = []
    timed_out = False
    cleanup_timed_out = False
    sigint_sent = False

    def close_stream(stream: Any) -> None:
        try:
            selector.unregister(stream)
        except (KeyError, ValueError):
            pass
        try:
            stream.close()
        except OSError:
            pass

    def append_output(kind: str, data: bytes) -> None:
        target = buffers[kind]
        available = OUTPUT_LIMIT - len(target)
        if available > 0:
            target.extend(data[:available])
        if len(data) > max(available, 0):
            truncated[kind] = True

    def poll_hooks() -> None:
        nonlocal sigint_sent
        if on_tick is not None:
            on_tick()
        if sigint_after is not None and not sigint_sent and process.poll() is None:
            if time.monotonic() - started >= sigint_after:
                signal_group(process.pid, signal.SIGINT)
                termination_signals.append(signal.SIGINT.name)
                sigint_sent = True

    def drain_until(deadline: float) -> bool:
        while True:
            poll_hooks()
            if process.poll() is not None and not selector.get_map():
                return True
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            events = selector.select(min(remaining, 0.05))
            if not events:
                continue
            for key, _ in events:
                stream = key.fileobj
                kind = key.data
                try:
                    data = os.read(stream.fileno(), 64 * 1024)
                except BlockingIOError:
                    continue
                except OSError:
                    data = b""
                if not data:
                    close_stream(stream)
                else:
                    append_output(kind, data)

    for stream, kind in ((process.stdout, "stdout"), (process.stderr, "stderr")):
        if stream is not None:
            os.set_blocking(stream.fileno(), False)
            selector.register(stream, selectors.EVENT_READ, kind)

    deadline = started + timeout
    if sigint_after is not None:
        deadline = min(deadline, started + sigint_after + 10.0)
    try:
        drained = drain_until(deadline)
        if not drained and process.poll() is None:
            timed_out = True
            signal_group(process.pid, signal.SIGTERM)
            termination_signals.append(signal.SIGTERM.name)
            drained = drain_until(time.monotonic() + TERMINATION_GRACE_SECONDS)
            if not drained or process.poll() is None or process_group_alive(process.pid):
                cleanup_timed_out = True
                signal_group(process.pid, signal.SIGKILL)
                termination_signals.append(signal.SIGKILL.name)
                drain_until(time.monotonic() + TERMINATION_GRACE_SECONDS)
        elif process.poll() is not None and (selector.get_map() or process_group_alive(process.pid)):
            drained = drain_until(time.monotonic() + TERMINATION_GRACE_SECONDS)
            if not drained or process_group_alive(process.pid):
                cleanup_timed_out = True
                signal_group(process.pid, signal.SIGKILL)
                termination_signals.append(signal.SIGKILL.name)
                drain_until(time.monotonic() + TERMINATION_GRACE_SECONDS)
        if process.poll() is None:
            cleanup_timed_out = True
            signal_group(process.pid, signal.SIGKILL)
            termination_signals.append(signal.SIGKILL.name)
            try:
                process.wait(timeout=TERMINATION_GRACE_SECONDS)
            except subprocess.TimeoutExpired:
                pass
    finally:
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                close_stream(stream)
        selector.close()

    try:
        process.wait(timeout=0)
    except subprocess.TimeoutExpired:
        cleanup_timed_out = True

    return {
        "label": label,
        "argv": command,
        "cwd": str(cwd),
        "removed_credentials": removed_credentials,
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "cleanup_timed_out": cleanup_timed_out,
        "termination_signals": termination_signals,
        "descendants_reaped": not process_group_alive(process.pid),
        "stdout_truncated": truncated["stdout"],
        "stderr_truncated": truncated["stderr"],
        "stdout_bytes": len(buffers["stdout"]),
        "stderr_bytes": len(buffers["stderr"]),
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "stdout": bytes(buffers["stdout"]).decode("utf-8", errors="replace"),
        "stderr": bytes(buffers["stderr"]).decode("utf-8", errors="replace"),
    }


def base_command(binary: Path, config_dir: Path, workdir: Path) -> list[str]:
    return [
        str(binary),
        "--config-dir",
        str(config_dir),
        "--workdir",
        str(workdir),
        "session",
    ]


def finish_case(
    record: dict[str, Any],
    *,
    expected_exit_codes: set[int],
    markers: Sequence[str],
    files: Sequence[Path] = (),
) -> dict[str, Any]:
    require(record["timed_out"] is False, f"{record['label']} exceeded its child deadline")
    require(record["cleanup_timed_out"] is False, f"{record['label']} cleanup exceeded its deadline")
    require(record["descendants_reaped"] is True, f"{record['label']} left a live process group")
    require(record["stdout_truncated"] is False and record["stderr_truncated"] is False, f"{record['label']} exceeded output cap")
    require(record["exit_code"] in expected_exit_codes, f"{record['label']} exited {record['exit_code']}, expected {sorted(expected_exit_codes)}")
    output = record["stdout"] + record["stderr"]
    missing = [marker for marker in markers if marker not in output]
    require(not missing, f"{record['label']} missing output markers {missing}")
    for path in files:
        require(path.is_file(), f"{record['label']} did not produce {path}")
        require(path.stat().st_size > 44, f"{record['label']} produced an empty audio container {path}")
    record["markers"] = list(markers)
    record["markers_present"] = True
    record["audio_files"] = [{"path": str(path), "bytes": path.stat().st_size} for path in files]
    return record


def case_paths(root: Path, name: str) -> tuple[Path, Path, Path]:
    case_root = root / name
    config_dir = case_root / "config"
    workdir = case_root / "workdir"
    config_dir.mkdir(parents=True)
    workdir.mkdir()
    return case_root, config_dir, workdir


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", dest="cases")
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=300)
    options = parser.parse_args()
    require(options.child_timeout > 0, "--child-timeout must be positive")
    require(options.aggregate_timeout > 0, "--aggregate-timeout must be positive")
    requested = options.cases or ["success", "sigint-cancellation", "cleanup-failure", "audio-tool-continuation-replay"]
    known = {"success", "sigint-cancellation", "cleanup-failure", "audio-tool-continuation-replay"}
    require(set(requested) <= known, f"unknown case(s): {sorted(set(requested) - known)}")
    require(ARTIFACT.is_file(), f"source-pinned artifact is missing: {ARTIFACT}; run the prescribed go build gate first")
    require(HEALTHY.is_file() and INTERRUPTION.is_file() and AUDIO_TOOL.is_file(), "required source-pinned replay fixture is missing")

    environment, removed_credentials = clean_environment()
    budget = RunBudget(options.aggregate_timeout)
    rows: list[dict[str, Any]] = []
    with tempfile.TemporaryDirectory(prefix="c104-yui-") as directory:
        temporary = Path(directory)
        artifact = {
            "path": str(ARTIFACT.relative_to(ROOT)),
            "sha256": sha256_file(ARTIFACT),
            "bytes": ARTIFACT.stat().st_size,
            "candidate_revision": git_revision(),
        }

        for name in requested:
            case_root, config_dir, workdir = case_paths(temporary, name)
            args = base_command(ARTIFACT, config_dir, workdir)
            audio_path = case_root / "assistant.wav"
            if name == "success":
                record = run_command(
                    name,
                    args + ["--replay", str(HEALTHY), "--audio-out", str(audio_path)],
                    workdir,
                    environment,
                    removed_credentials,
                    budget,
                    options.child_timeout,
                )
                rows.append(finish_case(record, expected_exit_codes={0}, markers=("Assistant: Hello there", "healthy_complete"), files=(audio_path,)))
            elif name == "sigint-cancellation":
                signal_audio_path = case_root / "assistant.pcm"
                record = run_command(
                    name,
                    args + ["--replay", str(INTERRUPTION), "--replay-timing", "recorded", "--audio-out", str(signal_audio_path)],
                    workdir,
                    environment,
                    removed_credentials,
                    budget,
                    options.child_timeout,
                    sigint_after=SIGINT_AFTER_SECONDS,
                )
                require("SIGINT" in record["termination_signals"], "sigint-cancellation did not deliver SIGINT")
                rows.append(finish_case(record, expected_exit_codes={0, 1, 130, -signal.SIGINT}, markers=("classification=user_cancelled", "terminal_reason=cancellation"), files=()))
            elif name == "cleanup-failure":
                record_dir = case_root / "recording"
                record_dir.mkdir()
                state = {"claim_observed": False, "destination_replaced": False}
                lock_path = Path(str(record_dir) + ".lock")

                def sabotage() -> None:
                    if lock_path.is_file():
                        state["claim_observed"] = True
                        if not state["destination_replaced"] and record_dir.is_dir():
                            shutil.rmtree(record_dir)
                            record_dir.write_text("injected finalization collision\n", encoding="utf-8")
                            state["destination_replaced"] = True

                record = run_command(
                    name,
                    args + [
                        "--replay",
                        str(INTERRUPTION),
                        "--replay-timing",
                        "recorded",
                        "--record-dir",
                        str(record_dir),
                        "--audio-out",
                        str(audio_path),
                    ],
                    workdir,
                    environment,
                    removed_credentials,
                    budget,
                    options.child_timeout,
                    on_tick=sabotage,
                )
                require(state["claim_observed"], "cleanup-failure never reached the recording claim barrier")
                require(state["destination_replaced"], "cleanup-failure injection did not replace the claimed temporary destination")
                require(record_dir.is_file(), "cleanup-failure destination collision did not survive until finalization")
                require(not lock_path.exists(), "cleanup-failure left the recording claim sidecar behind")
                record["injection"] = {
                    "barrier": "recording-directory-claim-sidecar",
                    "claim_observed": state["claim_observed"],
                    "destination_replaced": state["destination_replaced"],
                    "claim_released": not lock_path.exists(),
                }
                rows.append(finish_case(record, expected_exit_codes={1}, markers=("recording", "destination"), files=(audio_path,)))
            elif name == "audio-tool-continuation-replay":
                (workdir / "evidence/runs").mkdir(parents=True)
                record = run_command(
                    name,
                    args + ["--replay", str(AUDIO_TOOL), "--replay-timing", "recorded", "--audio-out", str(audio_path)],
                    workdir,
                    environment,
                    removed_credentials,
                    budget,
                    options.child_timeout,
                )
                rows.append(finish_case(record, expected_exit_codes={0}, markers=("PROBE_TOOL_MARKER_9182", "strict replay continuation"), files=(audio_path,)))

    rows = [scrub_temporary(row, temporary) for row in rows]
    report = {
        "status": "pass",
        "binary": artifact,
        "credential_environment_removed": removed_credentials,
        "cases": rows,
    }
    (TASK_ROOT / "shipped-replay.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"binary": artifact["path"], "artifact_sha256": artifact["sha256"], "cases": len(rows), "status": "pass"}))


if __name__ == "__main__":
    try:
        main()
    except EvidenceFailure as exc:
        raise SystemExit(str(exc)) from exc
