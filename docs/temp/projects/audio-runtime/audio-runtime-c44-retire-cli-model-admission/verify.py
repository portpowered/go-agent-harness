#!/usr/bin/env python3
"""Bounded public admission, CLI side-effect, replay, and cleanup controls for C44."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import select
import signal
import socket
import subprocess
import sys
import threading
import time
from typing import Any, Callable


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
CONSUMER_SOURCE = HERE / "consumer" / "main.go"
CONSUMER_MODULE = CONSUMER_SOURCE.parent
ARTIFACTS = HERE / "artifacts"
RUNS = HERE / "runs"
FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
CONSUMER = ARTIFACTS / "model-admission-consumer"
YUI = ARTIFACTS / "yui"


class EvidenceFailure(RuntimeError):
    pass


class StorageBlocked(EvidenceFailure):
    """A truthful storage prerequisite failure, distinct from a product failure."""


CAPTURE_CHUNK_BYTES = 64 * 1024
CLEANUP_GRACE_SECONDS = 2.0
RESERVE_SAMPLE_INTERVAL_SECONDS = 0.1


class OutputBudget:
    """A cumulative stdout/stderr budget shared by every child in one mission."""

    def __init__(self, limit_bytes: int) -> None:
        if limit_bytes <= 0:
            raise ValueError("output budget must be positive")
        self.limit_bytes = limit_bytes
        self.used_bytes = 0
        self._lock = threading.Lock()

    @property
    def remaining_bytes(self) -> int:
        with self._lock:
            return max(0, self.limit_bytes - self.used_bytes)

    def require_available(self, label: str) -> None:
        with self._lock:
            if self.used_bytes >= self.limit_bytes:
                raise EvidenceFailure(
                    f"aggregate private output limit exhausted before launching {label}: "
                    f"{self.used_bytes} >= {self.limit_bytes}"
                )

    def reserve(self, observed_bytes: int) -> tuple[int, bool]:
        if observed_bytes < 0:
            raise ValueError("observed output cannot be negative")
        with self._lock:
            available = max(0, self.limit_bytes - self.used_bytes)
            accepted = min(available, observed_bytes)
            self.used_bytes += accepted
            return accepted, observed_bytes > accepted


class _CaptureState:
    def __init__(self) -> None:
        self.failure: dict[str, Any] | None = None
        self._failure_lock = threading.Lock()
        self.stop = threading.Event()
        self.done = threading.Event()
        self.captured_bytes = {"stdout": 0, "stderr": 0}
        self.high_water_bytes = 0
        self.output_overflow = False

    def fail(self, kind: str, message: str, **details: Any) -> None:
        with self._failure_lock:
            if self.failure is None:
                self.failure = {"kind": kind, "message": message, **details}
        self.stop.set()


WRONG_PCM_ORACLE_DIAGNOSTIC = "wrong PCM oracle rejected"
WRONG_MARKER_ORACLE_DIAGNOSTIC = "wrong marker oracle rejected"


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def artifact_facts(path: pathlib.Path) -> dict[str, Any]:
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": sha256(path)}


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "GOWORK", "GOFLAGS", "GOTOOLCHAIN", "FACTORY_ROOT", "FACTORY_SERVER_URL")
    return {key: env.get(key, "") for key in keys}


def process_group_pids(pgid: int) -> list[int]:
    try:
        completed = subprocess.run(
            ["ps", "-eo", "pid=,pgid="],
            check=True,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise EvidenceFailure(f"process inspection unavailable for pgid {pgid}: {exc}") from exc
    lines = completed.stdout.splitlines()
    if not lines:
        raise EvidenceFailure(f"process inspection unavailable for pgid {pgid}: ps returned no rows")
    pids: list[int] = []
    for line in lines:
        fields = line.split()
        if len(fields) != 2 or not all(field.isdigit() for field in fields):
            raise EvidenceFailure(f"process inspection unavailable for pgid {pgid}: malformed ps row {line!r}")
        pid, row_pgid = (int(field) for field in fields)
        if pid <= 0 or row_pgid < 0:
            raise EvidenceFailure(f"process inspection unavailable for pgid {pgid}: invalid ps row {line!r}")
        if row_pgid == pgid:
            pids.append(pid)
    return pids


def _capture_read_size(output_budget: OutputBudget | None) -> int:
    if output_budget is None:
        return CAPTURE_CHUNK_BYTES
    return min(CAPTURE_CHUNK_BYTES, max(1, output_budget.remaining_bytes + 1))


def _capture_outputs(
    process: subprocess.Popen[bytes],
    stdout_path: pathlib.Path,
    stderr_path: pathlib.Path,
    output_budget: OutputBudget | None,
    state: _CaptureState,
) -> None:
    streams: dict[int, tuple[str, Any, Any]] = {}
    try:
        for name, stream, path in (
            ("stdout", process.stdout, stdout_path),
            ("stderr", process.stderr, stderr_path),
        ):
            if stream is None:
                state.fail("capture_setup", f"{name} pipe was not created")
                return
            fd = stream.fileno()
            os.set_blocking(fd, False)
            handle = path.open("wb", buffering=0)
            streams[fd] = (name, stream, handle)
        while streams and not state.stop.is_set():
            try:
                readable, _, _ = select.select(list(streams), [], [], 0.05)
            except (OSError, ValueError) as exc:
                state.fail("capture_read", f"output pipe selection failed: {exc}")
                break
            if not readable:
                continue
            for fd in readable:
                if fd not in streams:
                    continue
                name, stream, handle = streams[fd]
                try:
                    data = os.read(fd, _capture_read_size(output_budget))
                except BlockingIOError:
                    continue
                except OSError as exc:
                    state.fail("capture_read", f"{name} pipe read failed: {exc}", stream=name)
                    break
                if not data:
                    handle.close()
                    stream.close()
                    del streams[fd]
                    continue
                state.high_water_bytes = max(state.high_water_bytes, len(data))
                accepted = len(data)
                overflow = False
                if output_budget is not None:
                    accepted, overflow = output_budget.reserve(len(data))
                if accepted:
                    try:
                        handle.write(data[:accepted])
                    except OSError as exc:
                        state.fail("capture_write", f"{name} diagnostic write failed: {exc}", stream=name)
                        break
                    state.captured_bytes[name] += accepted
                if overflow:
                    state.output_overflow = True
                    limit = output_budget.limit_bytes if output_budget is not None else 0
                    used = output_budget.used_bytes if output_budget is not None else accepted
                    state.fail(
                        "output_overflow",
                        f"aggregate private output limit exceeded while capturing {name}: "
                        f"at least {used + len(data) - accepted} > {limit}",
                        stream=name,
                        observed_chunk_bytes=len(data),
                        accepted_chunk_bytes=accepted,
                    )
                    break
    except BaseException as exc:
        state.fail("capture", f"bounded output capture failed: {exc}")
    finally:
        for _fd, (_name, stream, handle) in list(streams.items()):
            try:
                handle.close()
            except OSError:
                pass
            try:
                stream.close()
            except OSError:
                pass
        state.done.set()


def _bounded_wait(process: subprocess.Popen[bytes], deadline: float) -> bool:
    while process.poll() is None:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False
        try:
            process.wait(timeout=min(0.05, remaining))
        except subprocess.TimeoutExpired:
            continue
    return True


def _attach_result(error: BaseException, result: dict[str, Any]) -> BaseException:
    try:
        setattr(error, "result", result)
    except Exception:
        pass
    return error


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    timeout_seconds: float,
    env: dict[str, str] | None = None,
    output_budget: OutputBudget | None = None,
    reserve_check: Callable[[str], None] | None = None,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if env is None else env)
    started = time.monotonic()
    child_deadline = started + timeout_seconds
    timed_out = False
    term_sent = False
    kill_sent = False
    cleanup_failures: list[dict[str, str]] = []
    primary_exception: BaseException | None = None
    primary_failure: dict[str, Any] | None = None
    survivors_before_final_kill: list[int] = []
    survivors: list[int] = []
    process: subprocess.Popen[bytes] | None = None
    state = _CaptureState()

    def write_not_started(message: str, error: BaseException) -> None:
        stdout_path.write_bytes(b"")
        stderr_path.write_bytes(b"")
        result = {
            "label": label,
            "argv": argv,
            "cwd": str(cwd),
            "environment": selected_environment(selected),
            "deadline_seconds": timeout_seconds,
            "elapsed_seconds": round(time.monotonic() - started, 6),
            "exit_code": None,
            "timed_out": False,
            "term_sent": False,
            "kill_sent": False,
            "parent_reaped": False,
            "surviving_process_group_pids": [],
            "stdout_bytes": 0,
            "stdout_sha256": sha256_bytes(b""),
            "stderr_bytes": 0,
            "stderr_sha256": sha256_bytes(b""),
            "stdout_path": str(stdout_path),
            "stderr_path": str(stderr_path),
            "not_started": True,
            "primary_failure": {"kind": "not_started", "message": message},
            "cleanup_failures": [],
            "output_budget_limit_bytes": output_budget.limit_bytes if output_budget is not None else None,
            "output_budget_used_bytes": output_budget.used_bytes if output_budget is not None else 0,
            "output_budget_remaining_bytes": output_budget.remaining_bytes if output_budget is not None else None,
        }
        record_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
        raise _attach_result(error, result)

    try:
        if output_budget is not None:
            try:
                output_budget.require_available(label)
            except EvidenceFailure as exc:
                write_not_started(str(exc), exc)
        if reserve_check is not None:
            try:
                reserve_check("before-launch")
            except BaseException as exc:
                write_not_started(str(exc), exc)
        process = subprocess.Popen(
            argv,
            cwd=str(cwd),
            env=selected,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        pgid = os.getpgid(process.pid)
        reader = threading.Thread(
            target=_capture_outputs,
            args=(process, stdout_path, stderr_path, output_budget, state),
            name=f"capture-{safe}",
            daemon=True,
        )
        reader.start()
        last_reserve_check = started
        while process.poll() is None:
            if state.failure is not None:
                primary_failure = state.failure
                break
            now = time.monotonic()
            if reserve_check is not None and now - last_reserve_check >= RESERVE_SAMPLE_INTERVAL_SECONDS:
                last_reserve_check = now
                try:
                    reserve_check("during-execution")
                except BaseException as exc:
                    primary_exception = exc
                    primary_failure = {"kind": "reserve", "message": str(exc)}
                    state.stop.set()
                    break
            if now >= child_deadline:
                timed_out = True
                primary_failure = {
                    "kind": "timeout",
                    "message": f"{label} exceeded its {timeout_seconds:g}-second deadline",
                }
                break
            try:
                process.wait(timeout=min(0.05, child_deadline - now))
            except subprocess.TimeoutExpired:
                continue

        if state.failure is not None and primary_failure is None:
            primary_failure = state.failure
        if process.returncode not in (None, 0) and primary_failure is None:
            primary_failure = {
                "kind": "exit_code",
                "message": f"{label} exited with nonzero status {process.returncode}",
                "exit_code": process.returncode,
            }
        cleanup_needed = timed_out or primary_failure is not None
        if not cleanup_needed:
            try:
                survivors_before_final_kill = process_group_pids(pgid)
            except BaseException as exc:
                primary_exception = primary_exception or exc
                primary_failure = primary_failure or {"kind": "process_inspection", "message": str(exc)}
                cleanup_needed = True
            if survivors_before_final_kill:
                cleanup_needed = True
                primary_failure = primary_failure or {
                    "kind": "descendants",
                    "message": f"{label} exited with surviving process-group members",
                }

        if cleanup_needed:
            cleanup_deadline = time.monotonic() + CLEANUP_GRACE_SECONDS
            try:
                os.killpg(pgid, signal.SIGTERM)
                term_sent = True
            except ProcessLookupError:
                pass
            except OSError as exc:
                cleanup_failures.append({"operation": "SIGTERM", "error": str(exc)})
            try:
                process.wait(timeout=min(0.5, max(0.01, cleanup_deadline - time.monotonic())))
            except subprocess.TimeoutExpired:
                pass
            group_deadline = min(cleanup_deadline, time.monotonic() + 0.6)
            group_empty = False
            group_inspection_failed = False
            while time.monotonic() < group_deadline:
                try:
                    group_pids = process_group_pids(pgid)
                except BaseException as exc:
                    cleanup_failures.append({"operation": "post-term-process-inspection", "error": str(exc)})
                    group_inspection_failed = True
                    group_pids = []
                if not group_pids and not group_inspection_failed:
                    group_empty = True
                    break
                time.sleep(0.02)
            if not group_empty:
                try:
                    os.killpg(pgid, signal.SIGKILL)
                    kill_sent = True
                except ProcessLookupError:
                    pass
                except OSError as exc:
                    cleanup_failures.append({"operation": "SIGKILL", "error": str(exc)})
                if not _bounded_wait(process, cleanup_deadline):
                    try:
                        process.kill()
                    except ProcessLookupError:
                        pass
                    except OSError as exc:
                        cleanup_failures.append({"operation": "parent-kill", "error": str(exc)})
                    if not _bounded_wait(process, cleanup_deadline):
                        cleanup_failures.append({"operation": "parent-reap", "error": "parent remained running after bounded cleanup"})
        else:
            _bounded_wait(process, time.monotonic() + 0.1)

        reader.join(timeout=CLEANUP_GRACE_SECONDS)
        if reader.is_alive():
            cleanup_failures.append({"operation": "pipe-reader-join", "error": "reader did not stop within bounded cleanup"})
            state.stop.set()
            reader.join(timeout=0.1)

        if state.failure is not None and primary_failure is None:
            primary_failure = state.failure
        try:
            survivors = process_group_pids(pgid)
        except BaseException as exc:
            cleanup_failures.append({"operation": "final-process-inspection", "error": str(exc)})
            if primary_failure is None:
                primary_exception = primary_exception or exc
                primary_failure = {"kind": "process_inspection", "message": str(exc)}
        if survivors:
            cleanup_failures.append({"operation": "final-process-inspection", "error": f"surviving pids: {survivors}"})
            if primary_failure is None:
                primary_failure = {"kind": "survivors", "message": f"process group survived cleanup: {survivors}"}
    except BaseException as exc:
        if primary_failure is None:
            primary_exception = primary_exception or exc
            primary_failure = {"kind": "runner", "message": str(exc)}
        else:
            cleanup_failures.append({"operation": "runner-cleanup", "error": str(exc)})
        if process is not None:
            state.stop.set()
            try:
                process.kill()
            except (ProcessLookupError, OSError):
                pass
            _bounded_wait(process, time.monotonic() + 0.5)
    finally:
        if process is not None:
            if process.stdout is not None:
                try:
                    process.stdout.close()
                except (OSError, ValueError):
                    pass
            if process.stderr is not None:
                try:
                    process.stderr.close()
                except (OSError, ValueError):
                    pass
        if not stdout_path.exists():
            stdout_path.write_bytes(b"")
        if not stderr_path.exists():
            stderr_path.write_bytes(b"")

    if process is None:
        if primary_exception is not None:
            raise primary_exception
        raise EvidenceFailure(f"{label} did not start")

    stdout_bytes = stdout_path.stat().st_size
    stderr_bytes = stderr_path.stat().st_size
    elapsed = time.monotonic() - started
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": timeout_seconds,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "parent_reaped": process.returncode is not None,
        "surviving_process_group_pids": survivors,
        "survivors_before_final_kill": survivors_before_final_kill,
        "stdout_bytes": stdout_bytes,
        "stdout_sha256": sha256(stdout_path),
        "stderr_bytes": stderr_bytes,
        "stderr_sha256": sha256(stderr_path),
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
        "output_overflow": state.output_overflow,
        "capture_failure": state.failure,
        "primary_failure": primary_failure,
        "cleanup_failures": cleanup_failures,
        "capture_read_chunk_bytes": CAPTURE_CHUNK_BYTES,
        "capture_buffer_high_water_bytes": state.high_water_bytes,
        "retained_output_bytes": stdout_bytes + stderr_bytes,
        "output_budget_limit_bytes": output_budget.limit_bytes if output_budget is not None else None,
        "output_budget_used_bytes": output_budget.used_bytes if output_budget is not None else stdout_bytes + stderr_bytes,
        "output_budget_remaining_bytes": output_budget.remaining_bytes if output_budget is not None else None,
    }
    record_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")

    if primary_exception is not None:
        raise _attach_result(primary_exception, result)
    if primary_failure is not None and (
        state.output_overflow or cleanup_failures or primary_failure["kind"] not in {"timeout", "exit_code"}
    ):
        raise _attach_result(EvidenceFailure(primary_failure["message"]), result)
    if cleanup_failures or survivors:
        raise _attach_result(EvidenceFailure(f"{label} cleanup proof failed; see {record_path}"), result)
    if state.output_overflow:
        raise _attach_result(EvidenceFailure(primary_failure["message"] if primary_failure else f"{label} exceeded output budget"), result)
    return result


def require_ok(result: dict[str, Any]) -> None:
    if (
        result["timed_out"]
        or result["exit_code"] != 0
        or not result["parent_reaped"]
        or result["surviving_process_group_pids"]
        or result.get("output_overflow")
        or result.get("capture_failure")
        or result.get("cleanup_failures")
        or (result.get("primary_failure") and result["primary_failure"].get("kind") != "timeout")
    ):
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def remaining_timeout(started: float, total_timeout: float, child_timeout: float) -> float:
    remaining = total_timeout - (time.monotonic() - started)
    if remaining <= 0:
        raise EvidenceFailure("aggregate deadline exceeded before the next control")
    return min(child_timeout, remaining)


def build_consumer(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    env["GOWORK"] = "off"
    result = run_process(
        "build-external-consumer",
        ["rtk", "proxy", "go", "build", "-o", str(CONSUMER), "."],
        CONSUMER_MODULE,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
        env,
    )
    require_ok(result)
    if not CONSUMER.is_file():
        raise EvidenceFailure(f"external consumer build did not create {CONSUMER}")
    result["artifact"] = artifact_facts(CONSUMER)
    return result


def build_yui(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_process(
        "build-yui-nomicrophone",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-o", str(YUI), "./agent-cli/cmd/yui"],
        ROOT,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
    )
    require_ok(result)
    if not YUI.is_file():
        raise EvidenceFailure(f"yui build did not create {YUI}")
    result["artifact"] = artifact_facts(YUI)
    return result


def run_consumer_tests(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    env = dict(os.environ)
    env["GOWORK"] = "off"
    normal = run_process(
        "external-consumer-test",
        ["rtk", "proxy", "go", "test", "-count=1", "-timeout=120s", "./..."],
        CONSUMER_MODULE,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
        env,
    )
    require_ok(normal)
    race = run_process(
        "external-consumer-race-test",
        ["rtk", "proxy", "go", "test", "-race", "-count=1", "-timeout=180s", "./..."],
        CONSUMER_MODULE,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
        env,
    )
    require_ok(race)
    wrong_env = dict(env)
    wrong_env["C44_WRONG_ORACLE"] = "1"
    wrong_oracle = run_process(
        "external-consumer-wrong-oracle",
        ["rtk", "proxy", "go", "test", "-count=1", "-timeout=120s", "./..."],
        CONSUMER_MODULE,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
        wrong_env,
    )
    require(wrong_oracle["exit_code"] != 0 and not wrong_oracle["timed_out"] and wrong_oracle["parent_reaped"], "wrong-oracle go test did not fail boundedly")
    diagnostic = pathlib.Path(wrong_oracle["stdout_path"]).read_text(encoding="utf-8") + pathlib.Path(wrong_oracle["stderr_path"]).read_text(encoding="utf-8")
    require("wrong oracle" in diagnostic.lower(), "wrong-oracle go test lost its assertion diagnostic")
    return {"normal": normal, "race": race, "wrong_oracle": wrong_oracle}


def load_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"cannot read JSON artifact {path}: {exc}") from exc


def run_public_consumer(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    result = run_process(
        "public-external-consumer",
        ["rtk", "proxy", str(CONSUMER)],
        CONSUMER_MODULE,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        report = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"external consumer did not emit one JSON report: {exc}") from exc
    if report.get("constructed_via") != "providers/wire.NewModelAdmission":
        raise EvidenceFailure(f"consumer did not use the public providers Wire boundary: {report!r}")
    observations = {item.get("name"): item for item in report.get("observations", [])}
    expected = {
        "custom-model-admitted",
        "custom-catalog-rejects-built-in-model",
        "supported-model-snapshot-isolated",
        "nil-catalog-fails-closed",
        "non-openai-provider-remains-unrestricted",
    }
    if set(observations) != expected:
        raise EvidenceFailure(f"consumer observation set changed: {sorted(observations)}")
    rejected = observations["custom-catalog-rejects-built-in-model"]
    if not rejected["errors_is_unsupported"] or rejected["error_kind"] != "UnsupportedRealtimeModelError":
        raise EvidenceFailure(f"consumer lost typed unsupported-model evidence: {rejected}")
    if rejected["model"] != "gpt-realtime" or rejected["supported_ids"] != ["custom-only", "custom-second"]:
        raise EvidenceFailure(f"consumer custom catalog ordering/identity changed: {rejected}")
    if not observations["nil-catalog-fails-closed"]["errors_is_catalog_required"]:
        raise EvidenceFailure(f"consumer lost nil-catalog sentinel: {observations['nil-catalog-fails-closed']}")
    if observations["non-openai-provider-remains-unrestricted"].get("error", ""):
        raise EvidenceFailure(f"non-OpenAI admission changed: {observations['non-openai-provider-remains-unrestricted']}")
    if report.get("resolver") != {"matched": True, "model_id": "custom-only"}:
        raise EvidenceFailure(f"consumer resolver evidence changed: {report.get('resolver')}")
    (run_dir / "consumer-observed.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return {"result": result, "report": report}


class LoopbackProbe:
    def __init__(self) -> None:
        self._server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._server.bind(("127.0.0.1", 0))
        self._server.listen(1)
        self._server.settimeout(0.1)
        self._stop = threading.Event()
        self.accepted = False
        self._thread = threading.Thread(target=self._accept, daemon=True)

    @property
    def url(self) -> str:
        return f"ws://127.0.0.1:{self._server.getsockname()[1]}"

    def _accept(self) -> None:
        while not self._stop.is_set():
            try:
                connection, _ = self._server.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            self.accepted = True
            connection.close()
            return

    def __enter__(self) -> "LoopbackProbe":
        self._thread.start()
        return self

    def __exit__(self, _type: Any, _value: Any, _traceback: Any) -> None:
        self._stop.set()
        self._server.close()
        self._thread.join(timeout=1)


def run_effect_observer_positive(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    with LoopbackProbe() as probe:
        port = str(probe._server.getsockname()[1])
        result = run_process(
            "positive-loopback-effect-sentinel",
            [sys.executable, "-c", "import socket,sys; connection=socket.create_connection(('127.0.0.1', int(sys.argv[1])), timeout=2); connection.close()", port],
            run_dir,
            run_dir,
            remaining_timeout(started, total_timeout, child_timeout),
        )
    require_ok(result)
    require(probe.accepted, "positive loopback effect sentinel was not observable")
    return {"result": result, "observer_detected_effect": probe.accepted}


def run_invalid_cli(
    label: str,
    command: list[str],
    case_dir: pathlib.Path,
    expected_text: str,
    output_target: pathlib.Path | None,
    run_dir: pathlib.Path,
    started: float,
    total_timeout: float,
    child_timeout: float,
) -> dict[str, Any]:
    case_dir.mkdir(parents=True, exist_ok=True)
    with LoopbackProbe() as probe:
        argv = ["rtk", "proxy", str(YUI), "--config-dir", str(case_dir / "config"), "--log-to-stdout", *command, "--base-url", probe.url, "--api-key", "c44-no-network-key"]
        result = run_process(label, argv, case_dir, run_dir, remaining_timeout(started, total_timeout, child_timeout))
    require(
        result["exit_code"] != 0
        and not result["timed_out"]
        and result["parent_reaped"]
        and not result["surviving_process_group_pids"],
        f"{label} unexpectedly succeeded, timed out, or left descendants: {result}",
    )
    output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8") + pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8")
    if expected_text.lower() not in output.lower():
        raise EvidenceFailure(f"{label} omitted {expected_text!r}; see {result['stderr_path']}")
    if probe.accepted:
        raise EvidenceFailure(f"{label} opened the provider endpoint before rejecting the model")
    if output_target is not None and output_target.exists():
        raise EvidenceFailure(f"{label} created its output target before admission: {output_target}")
    return {
        "result": result,
        "expected_text": expected_text,
        "provider_connection_accepted": probe.accepted,
        "output_target_exists": output_target.exists() if output_target is not None else False,
    }


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def run_cli_admission_controls(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    bare = run_invalid_cli(
        "invalid-bare-session-admission",
        ["session", "--provider", "openai", "--model", "c44-unknown-model", "--max-duration", "1s"],
        run_dir / "invalid-bare-session",
        "not realtime-capable",
        None,
        run_dir,
        started,
        total_timeout,
        child_timeout,
    )
    output_target = run_dir / "invalid-self-play" / "self-play-output"
    self_play = run_invalid_cli(
        "invalid-self-play-admission",
        [
            "session",
            "self-play",
            "--provider",
            "openai",
            "--model",
            "c44-unknown-model",
            "--max-duration",
            "1s",
            "--max-turns",
            "1",
            "--output-dir",
            str(output_target),
        ],
        run_dir / "invalid-self-play",
        "self-play model",
        output_target,
        run_dir,
        started,
        total_timeout,
        child_timeout,
    )
    return {"bare_session": bare, "self_play": self_play}


FIXTURE_EXPECTATIONS: dict[str, dict[str, Any]] = {
    "audio-tool": {
        "fixture": "c16-audio-tool.session.json",
        "pcm_bytes": 4800,
        "pcm_sha256": "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502",
        "terminal": {"reason": "fixture_complete", "classification": "provider_close", "terminal_reason": "provider_close", "terminal_provenance": "provider", "output_state": "not_applicable"},
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create", "response.created",
            "response.output_item.added", "response.function_call_arguments.done", "response.done", "conversation.item.create",
            "response.create", "response.created", "response.output_audio.delta", "response.output_audio.delta",
            "response.output_audio.done", "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
        ],
    },
    "interruption": {
        "fixture": "c16-interruption.session.json",
        "pcm_bytes": 3840,
        "pcm_sha256": "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22",
        "healthy_tail_offset_bytes": 1440,
        "healthy_tail_bytes": 2400,
        "healthy_tail_sha256": "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf",
        "terminal": {"reason": "replay_complete", "classification": "replay_complete", "terminal_reason": "replay_complete", "terminal_provenance": "replay", "output_state": "complete"},
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create", "response.created",
            "response.output_audio.delta", "input_audio_buffer.speech_started", "conversation.item.truncate",
            "conversation.item.truncated", "response.output_audio.done", "response.done", "response.created",
            "response.output_audio.delta", "response.output_audio.done", "response.done",
        ],
    },
}


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL {path}:{line_number}: {exc}") from exc
        if not isinstance(value, dict):
            raise EvidenceFailure(f"non-object JSONL record {path}:{line_number}")
        records.append(value)
    if not records:
        raise EvidenceFailure(f"empty JSONL artifact: {path}")
    return records


def validate_session_log(label: str, path: pathlib.Path) -> None:
    turns = load_jsonl(path)
    if label == "audio-tool":
        require(len(turns) == 1, f"{label} session-log turn count changed: {len(turns)}")
        response = turns[0].get("response", {})
        require(response.get("text") == "strict replay continuation", f"{label} continuation changed: {response}")
        require(response.get("complete") is True and response.get("audio_bytes") == 4800, f"{label} response audio changed: {response}")
        events = turns[0].get("tool_events")
        require(isinstance(events, list) and [event.get("type") for event in events] == ["tool_call", "tool_result"], f"{label} tool event order changed: {events}")
        require(events[1].get("content") == "PROBE_TOOL_MARKER_9182\n", f"{label} tool marker changed: {events}")
    else:
        require(len(turns) == 2, f"{label} session-log turn count changed: {len(turns)}")
        require(turns[0].get("response", {}).get("audio_bytes") == 1440 and turns[1].get("response", {}).get("audio_bytes") == 2400, f"{label} interruption boundaries changed")
        require(all(turn.get("tool_events") is None for turn in turns), f"{label} unexpectedly contains tool events")


def validate_replay(label: str, fixture: pathlib.Path, case_dir: pathlib.Path) -> dict[str, Any]:
    expectation = FIXTURE_EXPECTATIONS[label]
    record_dir = case_dir / "tool-record"
    manifest = record_dir / "manifest.json"
    pcm = record_dir / "audio" / "out-000.pcm"
    require(manifest.is_file() and pcm.is_file(), f"{label} replay did not produce manifest and PCM")
    manifest_data = load_json(manifest)
    require(manifest_data.get("terminal") == expectation["terminal"], f"{label} terminal state changed: {manifest_data.get('terminal')}")
    artifact_hashes = {item.get("path"): item.get("sha256") for item in manifest_data.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    require(set(artifact_hashes) == expected_paths, f"{label} artifact set changed: {sorted(artifact_hashes)}")
    for relative in expected_paths:
        artifact = record_dir / relative
        require(artifact.is_file() and artifact_hashes[relative] == sha256(artifact), f"{label} manifest hash mismatch for {relative}")
    for transcript in (record_dir / "client.transcript.jsonl", record_dir / "agent.transcript.jsonl"):
        records = load_jsonl(transcript)
        ticks = [record.get("tick") for record in records]
        require(ticks == list(range(1, len(ticks) + 1)), f"{label} transcript ticks are not contiguous: {ticks}")
    validate_session_log(label, record_dir / "session-log.jsonl")
    fixture_records = load_json(fixture).get("records", [])
    require(sha256(record_dir / "provider.json") == sha256(fixture), f"{label} provider fixture bytes changed")
    require([record.get("type") for record in fixture_records] == expectation["fixture_types"], f"{label} fixture event order changed")
    pcm_bytes = pcm.read_bytes()
    require(len(pcm_bytes) == expectation["pcm_bytes"] and sha256(pcm) == expectation["pcm_sha256"], f"{label} exact PCM changed: {len(pcm_bytes)} bytes / {sha256(pcm)}")
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        tail = pcm_bytes[expectation["healthy_tail_offset_bytes"]:]
        healthy_tail = {"offset_bytes": expectation["healthy_tail_offset_bytes"], "bytes": len(tail), "sha256": sha256_bytes(tail)}
        require(healthy_tail["bytes"] == expectation["healthy_tail_bytes"] and healthy_tail["sha256"] == expectation["healthy_tail_sha256"], f"{label} healthy PCM tail changed: {healthy_tail}")
    return {
        "fixture": str(fixture),
        "fixture_bytes": fixture.stat().st_size,
        "fixture_sha256": sha256(fixture),
        "manifest": str(manifest),
        "pcm": str(pcm),
        "pcm_bytes": len(pcm_bytes),
        "pcm_sha256": sha256(pcm),
        "healthy_tail": healthy_tail,
        "terminal": manifest_data["terminal"],
    }


def validate_replay_output(label: str, stdout: str) -> None:
    if label == "audio-tool":
        require("PROBE_TOOL_MARKER_9182" in stdout and "strict replay continuation" in stdout, "audio-tool replay lost marker or continuation")
    else:
        require("replay mismatch" not in stdout.lower(), "interruption replay reported a mismatch")


def run_replay_controls(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> list[dict[str, Any]]:
    results: list[dict[str, Any]] = []
    for label, expectation in FIXTURE_EXPECTATIONS.items():
        fixture = FIXTURES / expectation["fixture"]
        require(fixture.is_file(), f"reviewed C21 fixture is absent: {fixture}")
        case_dir = run_dir / f"replay-{label}"
        case_dir.mkdir(parents=True, exist_ok=True)
        (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
        record_dir = case_dir / "tool-record"
        audio_out = case_dir / "audio.wav"
        result = run_process(
            f"yui-replay-{label}",
            [
                "rtk", "proxy", str(YUI), "--config-dir", str(case_dir / "config"), "--log-to-stdout", "session",
                "--replay", str(fixture), "--audio-out", str(audio_out), "--record-dir", str(record_dir), "--trace-audio",
                "--workdir", str(case_dir), "--allow-path", str(case_dir),
            ],
            case_dir,
            run_dir,
            remaining_timeout(started, total_timeout, child_timeout),
        )
        require_ok(result)
        stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
        validate_replay_output(label, stdout)
        summary = validate_replay(label, fixture, case_dir)
        summary.update({"label": label, "stdout_path": result["stdout_path"], "stderr_path": result["stderr_path"], "clean_shutdown": result["parent_reaped"] and not result["surviving_process_group_pids"]})
        results.append(summary)
    return results


def run_wrong_replay_oracles(
    run_dir: pathlib.Path,
    started: float,
    total_timeout: float,
    child_timeout: float,
    replay_results: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    audio_tool = next((item for item in replay_results or [] if item.get("label") == "audio-tool"), None)
    case_dir = pathlib.Path(audio_tool["manifest"]).parent.parent if audio_tool is not None else run_dir / "replay-audio-tool"
    stdout_path = pathlib.Path(audio_tool["stdout_path"]) if audio_tool is not None else run_dir / "yui-replay-audio-tool.stdout"
    require(stdout_path.is_file(), f"audio-tool replay stdout is missing: {stdout_path}")
    try:
        stdout_path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise EvidenceFailure(f"audio-tool replay stdout is unreadable: {stdout_path}: {exc}") from exc
    fixture = FIXTURES / FIXTURE_EXPECTATIONS["audio-tool"]["fixture"]
    pcm_script = """
import pathlib
import runpy
import sys

module = runpy.run_path(sys.argv[1])
module["FIXTURE_EXPECTATIONS"]["audio-tool"]["pcm_sha256"] = "0" * 64
try:
    module["validate_replay"]("audio-tool", pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3]))
except module["EvidenceFailure"] as exc:
    if not str(exc).startswith("audio-tool exact PCM changed:"):
        print(f"wrong PCM oracle control failed with unexpected EvidenceFailure: {exc}", file=sys.stderr, flush=True)
        raise SystemExit(2)
    print(f'{module["WRONG_PCM_ORACLE_DIAGNOSTIC"]}: {exc}', flush=True)
    raise SystemExit(1)
except Exception as exc:
    print(f"wrong PCM oracle control failed with unexpected {type(exc).__name__}: {exc}", file=sys.stderr, flush=True)
    raise SystemExit(2)
raise SystemExit("wrong PCM oracle unexpectedly passed")
"""
    wrong_pcm = run_process(
        "negative-wrong-pcm-oracle",
        [sys.executable, "-c", pcm_script, str(HERE / "verify.py"), str(fixture), str(case_dir)],
        run_dir,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
    )
    require_negative_oracle(wrong_pcm, WRONG_PCM_ORACLE_DIAGNOSTIC, "wrong PCM")
    marker_script = """
import pathlib
import runpy
import sys

module = runpy.run_path(sys.argv[1])
try:
    stdout = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
    module["require"]("C44_WRONG_MARKER" in stdout, "wrong marker oracle")
except module["EvidenceFailure"] as exc:
    if str(exc) != "wrong marker oracle":
        print(f"wrong marker oracle control failed with unexpected EvidenceFailure: {exc}", file=sys.stderr, flush=True)
        raise SystemExit(2)
    print(f'{module["WRONG_MARKER_ORACLE_DIAGNOSTIC"]}: {exc}', flush=True)
    raise SystemExit(1)
except Exception as exc:
    print(f"wrong marker oracle control failed with unexpected {type(exc).__name__}: {exc}", file=sys.stderr, flush=True)
    raise SystemExit(2)
raise SystemExit("wrong marker oracle unexpectedly passed")
"""
    wrong_marker = run_process(
        "negative-wrong-marker-oracle",
        [sys.executable, "-c", marker_script, str(HERE / "verify.py"), str(stdout_path)],
        run_dir,
        run_dir,
        remaining_timeout(started, total_timeout, child_timeout),
    )
    require_negative_oracle(wrong_marker, WRONG_MARKER_ORACLE_DIAGNOSTIC, "wrong marker")
    return {"wrong_pcm": wrong_pcm, "wrong_marker": wrong_marker}


def require_negative_oracle(result: dict[str, Any], diagnostic: str, label: str) -> None:
    require(result["exit_code"] == 1 and not result["timed_out"], f"{label} oracle did not return the intended bounded rejection: {result}")
    require(result["parent_reaped"], f"{label} oracle parent was not reaped: {result}")
    require(not result["surviving_process_group_pids"], f"{label} oracle left process-group survivors: {result}")
    try:
        stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
        stderr = pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise EvidenceFailure(f"{label} oracle output is unavailable: {exc}") from exc
    lines = stdout.splitlines()
    require(len(lines) == 1 and lines[0].startswith(f"{diagnostic}: "), f"{label} oracle lost its intended diagnostic: {stdout!r}")
    require(not stderr, f"{label} oracle emitted an unexpected stderr diagnostic: {stderr!r}")


def run_timeout_control(run_dir: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    forked_child = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); time.sleep(30)"
    run_dir.mkdir(parents=True, exist_ok=True)
    result = run_process(
        "negative-forced-timeout-cleanup",
        [sys.executable, "-c", forked_child],
        run_dir,
        run_dir,
        min(2.0, remaining_timeout(started, total_timeout, child_timeout)),
    )
    require(result["timed_out"] and result["term_sent"] and result["parent_reaped"], f"forced timeout control was not bounded: {result}")
    require(not result["surviving_process_group_pids"], f"forced timeout left process-group survivors: {result}")
    return result


def tracked_input_manifest(scope: str, prefixes: list[str]) -> dict[str, Any]:
    try:
        listed = subprocess.run(
            ["rtk", "proxy", "git", "ls-files", "-z", "--", *prefixes],
            cwd=str(ROOT),
            check=True,
            capture_output=True,
            timeout=30,
        ).stdout.decode("utf-8")
    except (OSError, subprocess.SubprocessError, UnicodeError) as exc:
        raise EvidenceFailure(f"{scope} input manifest failed: {exc}") from exc

    entries: list[str] = []
    for relative in sorted(path for path in listed.split("\0") if path):
        path = ROOT / relative
        if not path.is_file():
            raise EvidenceFailure(f"{scope} input manifest references missing file: {relative}")
        entries.append(f"{relative}\0{path.stat().st_size}\0{sha256(path)}\n")
    manifest = "".join(entries).encode("utf-8")
    return {
        "prefixes": prefixes,
        "file_count": len(entries),
        "manifest_sha256": sha256_bytes(manifest),
    }


def source_facts(source: str) -> dict[str, Any]:
    try:
        revision = subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=str(ROOT), check=True, capture_output=True, text=True, timeout=10).stdout.strip()
        status = subprocess.run(["rtk", "proxy", "git", "status", "--porcelain"], cwd=str(ROOT), check=True, capture_output=True, text=True, timeout=10).stdout.splitlines()
        archive = subprocess.run(["rtk", "proxy", "git", "archive", "--format=tar", "HEAD"], cwd=str(ROOT), check=True, capture_output=True, timeout=30).stdout
        go_version = subprocess.run(["rtk", "proxy", "go", "version"], cwd=str(ROOT), check=True, capture_output=True, text=True, timeout=10).stdout.strip()
    except (OSError, subprocess.SubprocessError) as exc:
        raise EvidenceFailure(f"source provenance failed: {exc}") from exc
    shared_prefixes = ["go.work", "go.work.sum", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "go-agent-runtime"]
    return {
        "source": source,
        "revision": revision,
        "source_tree_dirty": bool(status),
        "status": status,
        "source_archive_bytes": len(archive),
        "source_archive_sha256": sha256_bytes(archive),
        "go_version": go_version,
        "build_inputs": {
            "external_consumer": tracked_input_manifest(
                "external consumer",
                shared_prefixes + ["docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/consumer"],
            ),
            "shipped_yui": tracked_input_manifest("shipped yui", shared_prefixes + ["agent-cli"]),
        },
        "consumer_source_sha256": sha256(CONSUMER_SOURCE),
        "consumer_source_bytes": CONSUMER_SOURCE.stat().st_size,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("public", "negative-controls"), default="public")
    parser.add_argument("--source", default="working-tree")
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--total-timeout", type=float, default=600)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 60 or args.total_timeout <= 0 or args.total_timeout > 600:
        print(json.dumps({"decision": "FAILED", "error": "timeouts exceed C44 bounds"}), file=sys.stderr)
        return 1
    started = time.monotonic()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = RUNS / f"verify-{stamp}-{os.getpid()}"
    outcome: dict[str, Any] = {"schema": "audio-runtime-c44-verification/v1", "mode": args.mode, "decision": "FAILED", "run_dir": str(run_dir), "child_timeout_seconds": args.child_timeout, "total_timeout_seconds": args.total_timeout, "steps": []}
    try:
        outcome["steps"].append(build_consumer(run_dir, started, args.total_timeout, args.child_timeout))
        consumer_tests = run_consumer_tests(run_dir, started, args.total_timeout, args.child_timeout)
        outcome["consumer_tests"] = consumer_tests
        outcome["wrong_oracle"] = consumer_tests["wrong_oracle"]
        if args.mode == "public":
            outcome["steps"].append(build_yui(run_dir, started, args.total_timeout, args.child_timeout))
            outcome["public_consumer"] = run_public_consumer(run_dir, started, args.total_timeout, args.child_timeout)
            outcome["effect_observer"] = run_effect_observer_positive(run_dir, started, args.total_timeout, args.child_timeout)
            outcome["cli_admission"] = run_cli_admission_controls(run_dir, started, args.total_timeout, args.child_timeout)
            replay_results = run_replay_controls(run_dir, started, args.total_timeout, args.child_timeout)
            outcome["replay"] = replay_results
            outcome["wrong_replay_oracles"] = run_wrong_replay_oracles(run_dir, started, args.total_timeout, args.child_timeout, replay_results)
            outcome["timeout_cleanup"] = run_timeout_control(run_dir, started, args.total_timeout, args.child_timeout)
        else:
            outcome["timeout_cleanup"] = run_timeout_control(run_dir, started, args.total_timeout, args.child_timeout)
        require(time.monotonic() - started <= args.total_timeout, "aggregate deadline exceeded")
        outcome["decision"] = "ACCEPTED"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
        run_dir.mkdir(parents=True, exist_ok=True)
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (HERE / "latest-outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    outcome["provenance"] = source_facts(args.source)
    run_dir.mkdir(parents=True, exist_ok=True)
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    (HERE / "latest-outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
