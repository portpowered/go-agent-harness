#!/usr/bin/env python3
"""Bounded C46 verifier for Wire, the public consumer, and live retry.

The public check runs the shipped yui executable against a credential-free
loopback WebSocket provider. The provider emits one bounded rate-limit failure
and then a healthy response, so the check observes the real CLI scheduling
boundary rather than calling the private policy package directly.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[4]
WIRE_DIR = REPO_ROOT / "go-agent-runtime/services/providers/wire"
WIRE_SOURCE = WIRE_DIR / "terminal_policy.go"
WIRE_GENERATED = WIRE_DIR / "terminal_policy_gen.go"
WIRE_GENERIC = WIRE_DIR / "wire_gen.go"
WIRE_GENERATOR = HERE / "wire_generate.py"
CONSUMER_DIR = HERE / "consumer"
MAX_CHILD_SECONDS = 60.0
MAX_TOTAL_SECONDS = 600.0
NEGATIVE_TIMEOUT_SECONDS = 0.5
NEGATIVE_CLEANUP_SECONDS = 1.0
NEGATIVE_OUTPUT_LIMIT = 16 * 1024
HEALTHY_MARKER = "c46 retry recovered"


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def clipped(value: str, limit: int = 16000) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + "\n...[clipped]"


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
        raise EvidenceFailure(f"process inspection failed for pgid {pgid}: {exc}") from exc
    pids: list[int] = []
    for line in completed.stdout.splitlines():
        fields = line.split()
        if len(fields) != 2 or not all(field.isdigit() for field in fields):
            continue
        pid, row_pgid = (int(field) for field in fields)
        if row_pgid == pgid:
            pids.append(pid)
    return pids


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    if os.name == "nt":
        process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()


def bounded_synthetic_process(
    runner: Runner,
    label: str,
    argv: list[str],
    *,
    timeout: float,
    output_limit: int,
) -> dict[str, Any]:
    """Exercise bounded output capture and process-group cleanup without yui."""

    index = len(runner.steps) + 1
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = runner.run_dir / f"{index:03d}-{safe}.stdout.log"
    stderr_path = runner.run_dir / f"{index:03d}-{safe}.stderr.log"
    process = subprocess.Popen(
        argv,
        cwd=REPO_ROOT,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name != "nt",
    )
    stdout = bytearray()
    stderr = bytearray()
    overflow = threading.Event()

    def drain(stream: Any, output: bytearray) -> None:
        while True:
            chunk = stream.read(4096)
            if not chunk:
                return
            if len(output) < output_limit:
                output.extend(chunk[: output_limit - len(output)])
            if len(output) >= output_limit or len(chunk) > output_limit:
                overflow.set()

    threads = [
        threading.Thread(target=drain, args=(process.stdout, stdout), daemon=True),
        threading.Thread(target=drain, args=(process.stderr, stderr), daemon=True),
    ]
    for thread in threads:
        thread.start()
    started = time.monotonic()
    timed_out = False
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        terminate_group(process)
    finally:
        for thread in threads:
            thread.join(timeout=NEGATIVE_CLEANUP_SECONDS)
        if any(thread.is_alive() for thread in threads):
            raise EvidenceFailure(f"{label} output reader exceeded bounded cleanup")
    surviving: list[int] = []
    deadline = time.monotonic() + NEGATIVE_CLEANUP_SECONDS
    while time.monotonic() < deadline:
        surviving = process_group_pids(process.pid)
        if not surviving:
            break
        time.sleep(0.02)
    stdout_path.write_bytes(bytes(stdout))
    stderr_path.write_bytes(bytes(stderr))
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(REPO_ROOT),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "parent_reaped": process.returncode is not None,
        "surviving_process_group_pids": surviving,
        "output_limit_bytes": output_limit,
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
        "output_overflow": overflow.is_set(),
        "duration_seconds": round(time.monotonic() - started, 6),
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    runner.steps.append(result)
    require(result["parent_reaped"], f"{label} direct child was not reaped")
    require(not surviving, f"{label} left process-group survivors: {surviving}")
    require(len(stdout) <= output_limit and len(stderr) <= output_limit, f"{label} exceeded output cap")
    return result


def run_negative_controls(runner: Runner) -> dict[str, Any]:
    timeout_script = (
        "import os,signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "child=os.fork(); signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"
    )
    timeout = bounded_synthetic_process(
        runner,
        "timeout-group-term-kill-reap",
        [sys.executable, "-c", timeout_script],
        timeout=NEGATIVE_TIMEOUT_SECONDS,
        output_limit=NEGATIVE_OUTPUT_LIMIT,
    )
    require(timeout["timed_out"], "timeout negative control did not reach its deadline")
    overflow = bounded_synthetic_process(
        runner,
        "output-overflow-drain-cap-reap",
        [sys.executable, "-c", "import sys; sys.stdout.buffer.write(b'x' * 131072)"],
        timeout=NEGATIVE_TIMEOUT_SECONDS,
        output_limit=NEGATIVE_OUTPUT_LIMIT,
    )
    require(not overflow["timed_out"], "output-overflow negative control timed out")
    require(overflow["output_overflow"], "output-overflow negative control did not classify overflow")
    return {
        "timeout": timeout,
        "output_overflow": overflow,
        "bounded": {
            "child_seconds": NEGATIVE_TIMEOUT_SECONDS,
            "cleanup_seconds": NEGATIVE_CLEANUP_SECONDS,
            "output_limit_bytes": NEGATIVE_OUTPUT_LIMIT,
        },
    }


class Runner:
    def __init__(self, mode: str) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        self.run_dir = HERE / "runs" / f"verify-{stamp}-{os.getpid()}"
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.mode = mode
        self.started = time.monotonic()
        self.steps: list[dict[str, Any]] = []

    def remaining(self) -> float:
        remaining = MAX_TOTAL_SECONDS - (time.monotonic() - self.started)
        if remaining <= 0:
            raise EvidenceFailure("aggregate verifier deadline exceeded")
        return min(remaining, MAX_CHILD_SECONDS)

    def command(
        self,
        label: str,
        argv: list[str],
        cwd: Path,
        *,
        env: dict[str, str] | None = None,
        expect_success: bool | None = True,
        required_output: str = "",
    ) -> dict[str, Any]:
        index = len(self.steps) + 1
        safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
        stdout_path = self.run_dir / f"{index:03d}-{safe}.stdout.log"
        stderr_path = self.run_dir / f"{index:03d}-{safe}.stderr.log"
        selected_env = dict(os.environ if env is None else env)
        selected_env.pop("OPENAI_API_KEY", None)
        started = time.monotonic()
        timed_out = False
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=selected_env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=os.name != "nt",
        )
        try:
            stdout, stderr = process.communicate(timeout=self.remaining())
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = exc.output or b""
            stderr = exc.stderr or b""
            terminate_group(process)
            try:
                stdout, stderr = process.communicate(timeout=2)
            except subprocess.TimeoutExpired:
                stdout = stdout or b""
                stderr = stderr or b""
        stdout_text = stdout.decode("utf-8", errors="replace")
        stderr_text = stderr.decode("utf-8", errors="replace")
        stdout_path.write_text(stdout_text, encoding="utf-8")
        stderr_path.write_text(stderr_text, encoding="utf-8")
        surviving = process_group_pids(process.pid) if process.returncode is not None else []
        if surviving:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
            surviving = process_group_pids(process.pid)
        step = {
            "label": label,
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": process.returncode,
            "timed_out": timed_out,
            "parent_reaped": process.returncode is not None,
            "surviving_process_group_pids": surviving,
            "duration_seconds": round(time.monotonic() - started, 6),
            "stdout_path": str(stdout_path),
            "stderr_path": str(stderr_path),
            "stdout": clipped(stdout_text),
            "stderr": clipped(stderr_text),
        }
        self.steps.append(step)
        success = not timed_out and process.returncode == 0 and not surviving
        if expect_success is True and not success:
            raise EvidenceFailure(f"{label} failed; see {stderr_path}")
        if expect_success is False:
            require(not success, f"{label} unexpectedly passed")
            combined = stdout_text + "\n" + stderr_text
            if required_output:
                require(required_output in combined, f"{label} omitted {required_output!r}")
        return step

    def save(self, outcome: dict[str, Any]) -> None:
        output = dict(outcome)
        output["mode"] = self.mode
        output["run_dir"] = str(self.run_dir)
        output["steps"] = self.steps
        (self.run_dir / "outcome.json").write_text(json.dumps(output, indent=2) + "\n", encoding="utf-8")


def run_wire(runner: Runner) -> dict[str, Any]:
    before_generic = WIRE_GENERIC.read_bytes()
    runner.command(
        "wire-generate",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "generate", "."],
        WIRE_DIR,
    )
    generated = WIRE_GENERATED.read_bytes()
    require(generated.startswith(b"// Code generated by Wire. DO NOT EDIT.\n"), "dedicated Wire file lacks the generated header")
    require(
        b"//go:generate go run -mod=mod github.com/google/wire/cmd/wire" in generated,
        "dedicated generated file lost its Wire generator directive",
    )
    require(b"NewTerminalPolicy" in generated, "dedicated generated file lacks NewTerminalPolicy")
    for forbidden in (b"NewService", b"NewModelCatalog", b"NewToolRegistry"):
        require(forbidden not in generated, f"dedicated generated file absorbed generic Wire graph: {forbidden!r}")
    require(WIRE_GENERIC.read_bytes() == before_generic, "generic providers Wire output changed")

    mutated = generated.replace(
        b"policy := terminalpolicy.New()",
        b"policy := terminalpolicy.New()\n\t// deliberate C46 generated-artifact mutation",
        1,
    )
    require(mutated != generated, "Wire mutation control did not mutate the generated artifact")
    mutation_repaired = False
    WIRE_GENERATED.write_bytes(mutated)
    try:
        runner.command(
            "wire-regenerate-after-mutation",
            ["rtk", "proxy", "env", "GOWORK=off", "python3", str(WIRE_GENERATOR)],
            REPO_ROOT,
        )
        mutation_repaired = WIRE_GENERATED.read_bytes() == generated
    finally:
        if WIRE_GENERATED.read_bytes() != generated:
            WIRE_GENERATED.write_bytes(generated)
    require(mutation_repaired, "generated-file mutation was not detected and repaired by the isolated generator")
    require(WIRE_GENERIC.read_bytes() == before_generic, "generic providers Wire output changed during mutation control")
    return {
        "generated_sha256": sha256_bytes(generated),
        "generic_wire_sha256": sha256_bytes(before_generic),
        "source_generator": str(WIRE_SOURCE),
        "generator_script": str(WIRE_GENERATOR),
        "mutation_detected_and_repaired": mutation_repaired,
    }


def run_consumer(runner: Runner) -> dict[str, Any]:
    env = dict(os.environ)
    env["GOWORK"] = "off"
    env.pop("C46_WRONG_ORACLE", None)
    normal = runner.command(
        "consumer-test",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-count=1", "-timeout=90s", "./..."],
        CONSUMER_DIR,
        env=env,
    )
    race = runner.command(
        "consumer-race-test",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-race", "-count=1", "-timeout=120s", "./..."],
        CONSUMER_DIR,
        env=env,
    )
    delay_env = dict(env)
    delay_env["C46_WRONG_ORACLE"] = "delay"
    delay = runner.command(
        "consumer-wrong-delay-oracle",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-count=1", "-timeout=90s", "./..."],
        CONSUMER_DIR,
        env=delay_env,
        expect_success=False,
        required_output="delay = 1.668s, want 1.667s",
    )
    eligibility_env = dict(env)
    eligibility_env["C46_WRONG_ORACLE"] = "eligibility"
    eligibility = runner.command(
        "consumer-wrong-eligibility-oracle",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-count=1", "-timeout=90s", "./..."],
        CONSUMER_DIR,
        env=eligibility_env,
        expect_success=False,
        required_output="eligible = true, want false",
    )
    dependencies = runner.command(
        "consumer-dependency-boundary",
        ["rtk", "proxy", "env", "GOWORK=off", "go", "list", "-deps", "."],
        CONSUMER_DIR,
        env=env,
    )
    require("agent-cli" not in dependencies["stdout"], "independent consumer imports agent-cli")
    return {
        "normal": normal,
        "race": race,
        "delay_wrong_oracle": delay,
        "eligibility_wrong_oracle": eligibility,
        "agent_cli_dependency_absent": True,
    }


def run_runtime_regressions(runner: Runner) -> dict[str, Any]:
    pattern = "Test(RateLimitRetryDecision|SessionProgressObserverRateLimitRetry|RateLimitRetryWait|RunAgentLoopSession.*RateLimit|ScheduledFailureRetainsProviderMetadata)"
    result = runner.command(
        "cli-rate-limit-regressions",
        [
            "rtk",
            "proxy",
            "env",
            "GOWORK=off",
            "go",
            "test",
            "-count=1",
            "-timeout=120s",
            "./internal/services/internal/agentruntime",
            "-run",
            pattern,
        ],
        REPO_ROOT / "agent-cli",
    )
    return {"focused_cli_regressions": result, "run_pattern": pattern}


def recv_exact(connection: socket.socket, size: int) -> bytes | None:
    chunks: list[bytes] = []
    remaining = size
    while remaining:
        chunk = connection.recv(remaining)
        if not chunk:
            return None
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_ws_frame(connection: socket.socket) -> tuple[int, bytes] | None:
    first = recv_exact(connection, 2)
    if first is None:
        return None
    opcode = first[0] & 0x0F
    masked = bool(first[1] & 0x80)
    length = first[1] & 0x7F
    if length == 126:
        raw_length = recv_exact(connection, 2)
        if raw_length is None:
            return None
        length = int.from_bytes(raw_length, "big")
    elif length == 127:
        raw_length = recv_exact(connection, 8)
        if raw_length is None:
            return None
        length = int.from_bytes(raw_length, "big")
    require(length <= 4 * 1024 * 1024, f"loopback WebSocket frame too large: {length}")
    mask = recv_exact(connection, 4) if masked else None
    payload = recv_exact(connection, length)
    if payload is None:
        return None
    if mask is not None:
        payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
    return opcode, payload


def write_ws_frame(connection: socket.socket, payload: bytes, opcode: int = 1) -> None:
    length = len(payload)
    if length < 126:
        header = bytes([0x80 | opcode, length])
    elif length < 65536:
        header = bytes([0x80 | opcode, 126]) + length.to_bytes(2, "big")
    else:
        header = bytes([0x80 | opcode, 127]) + length.to_bytes(8, "big")
    connection.sendall(header + payload)


class LoopbackRealtimeServer:
    def __init__(self, run_dir: Path) -> None:
        self.run_dir = run_dir
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(1)
        self.listener.settimeout(0.2)
        self.stop_event = threading.Event()
        self.thread = threading.Thread(target=self._serve, name="c46-loopback", daemon=True)
        self.write_lock = threading.Lock()
        self.connection: socket.socket | None = None
        self.events: list[dict[str, Any]] = []
        self.response_creates = 0
        self.accepted = False
        self.error = ""
        self.started = time.monotonic()

    @property
    def url(self) -> str:
        return f"ws://127.0.0.1:{self.listener.getsockname()[1]}/v1/realtime"

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.stop_event.set()
        try:
            self.listener.close()
        except OSError:
            pass
        if self.connection is not None:
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                self.connection.close()
            except OSError:
                pass
        self.thread.join(timeout=3)

    def _record(self, direction: str, event: dict[str, Any]) -> None:
        self.events.append(
            {
                "sequence": len(self.events) + 1,
                "direction": direction,
                "timestamp_ms": round((time.monotonic() - self.started) * 1000, 3),
                "type": event.get("type", ""),
                "payload": event,
            }
        )

    def _send(self, event: dict[str, Any]) -> None:
        connection = self.connection
        if connection is None:
            raise OSError("loopback connection is not established")
        payload = json.dumps(event, separators=(",", ":")).encode("utf-8")
        with self.write_lock:
            write_ws_frame(connection, payload)
        self._record("server_to_client", event)

    def _handshake(self, connection: socket.socket) -> None:
        request = b""
        while b"\r\n\r\n" not in request:
            chunk = connection.recv(4096)
            if not chunk:
                raise OSError("client closed during WebSocket handshake")
            request += chunk
            require(len(request) <= 64 * 1024, "loopback WebSocket handshake too large")
        headers: dict[str, str] = {}
        for line in request.decode("latin1").split("\r\n")[1:]:
            if ":" in line:
                key, value = line.split(":", 1)
                headers[key.lower().strip()] = value.strip()
        key = headers.get("sec-websocket-key")
        require(key is not None, "client WebSocket handshake omitted Sec-WebSocket-Key")
        accept_source = (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode("ascii")
        accept = base64.b64encode(hashlib.sha1(accept_source).digest()).decode("ascii")
        response = (
            "HTTP/1.1 101 Switching Protocols\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Accept: {accept}\r\n\r\n"
        ).encode("ascii")
        connection.sendall(response)

    def _send_session_start(self) -> None:
        session = {
            "id": "sess-c46-loopback",
            "type": "realtime",
            "model": "gpt-realtime",
            "audio": {
                "input": {"format": {"type": "audio/pcm", "rate": 24000}, "turn_detection": None},
                "output": {"format": {"type": "audio/pcm", "rate": 24000}, "voice": "alloy"},
            },
        }
        self._send({"type": "session.created", "session": session})
        self._send({"type": "session.updated", "session": {"id": session["id"], "model": session["model"]}})

    def _send_response(self) -> None:
        self.response_creates += 1
        response_id = f"resp-c46-{self.response_creates}"
        self._send({"type": "response.created", "response": {"id": response_id, "status": "in_progress"}})
        if self.response_creates == 1:
            self._send(
                {
                    "type": "response.done",
                    "response": {
                        "id": response_id,
                        "status": "failed",
                        "status_details": {
                            "error": {
                                "code": "rate_limit_exceeded",
                                "message": "Please try again in 0.010s.",
                            }
                        },
                    },
                }
            )
            return
        audio = base64.b64encode(b"\x10\x00\x20\x00\x30\x00\x40\x00").decode("ascii")
        self._send({"type": "response.output_audio_transcript.delta", "response_id": response_id, "delta": HEALTHY_MARKER})
        self._send({"type": "response.output_audio_transcript.done", "response_id": response_id, "transcript": HEALTHY_MARKER})
        self._send({"type": "response.output_audio.delta", "response_id": response_id, "delta": audio, "format": "pcm16"})
        self._send({"type": "response.output_audio.done", "response_id": response_id})
        self._send({"type": "response.done", "response": {"id": response_id, "status": "completed"}})
        self._send({"type": "session.closed", "session_id": "sess-c46-loopback", "reason": "c46_fixture_complete"})

    def _serve(self) -> None:
        try:
            while not self.stop_event.is_set():
                try:
                    connection, _ = self.listener.accept()
                except socket.timeout:
                    continue
                self.connection = connection
                self.accepted = True
                connection.settimeout(2.0)
                self._handshake(connection)
                self._send_session_start()
                while not self.stop_event.is_set():
                    frame = read_ws_frame(connection)
                    if frame is None:
                        return
                    opcode, payload = frame
                    if opcode == 8:
                        write_ws_frame(connection, payload[:2], opcode=8)
                        return
                    if opcode == 9:
                        write_ws_frame(connection, payload, opcode=10)
                        continue
                    if opcode != 1:
                        continue
                    event = json.loads(payload.decode("utf-8"))
                    require(isinstance(event, dict), "client WebSocket event was not an object")
                    self._record("client_to_server", event)
                    if event.get("type") == "response.create":
                        self._send_response()
        except (OSError, ValueError, json.JSONDecodeError, EvidenceFailure) as exc:
            if not self.stop_event.is_set():
                self.error = str(exc)
        finally:
            if self.connection is not None:
                try:
                    self.connection.close()
                except OSError:
                    pass

    def write_event_log(self) -> Path:
        path = self.run_dir / "provider-events.jsonl"
        path.write_text("".join(json.dumps(event, sort_keys=True) + "\n" for event in self.events), encoding="utf-8")
        return path


def validate_record_manifest(record_dir: Path) -> dict[str, Any]:
    manifest_path = record_dir / "manifest.json"
    require(manifest_path.is_file(), f"public yui did not publish record manifest: {manifest_path}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    require(isinstance(manifest, dict), "record manifest is not an object")
    artifacts = manifest.get("artifacts", [])
    require(isinstance(artifacts, list) and artifacts, "record manifest has no artifacts")
    checked: list[dict[str, Any]] = []
    for artifact in artifacts:
        relative = artifact.get("path")
        if not isinstance(relative, str) or not relative:
            continue
        path = record_dir / relative
        require(path.is_file(), f"record manifest artifact is missing: {relative}")
        checked.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    require(checked, "record manifest has no file-backed artifacts")
    return {"path": str(manifest_path), "sha256": sha256_file(manifest_path), "artifacts": checked, "terminal": manifest.get("terminal")}


def run_public(runner: Runner, binary: Path) -> dict[str, Any]:
    require(binary.is_file(), f"shipped yui executable is absent: {binary}")
    case_dir = runner.run_dir / "public-loopback"
    record_dir = case_dir / "record-dir"
    config_dir = case_dir / "config"
    input_path = case_dir / "turn.raw"
    case_dir.mkdir(parents=True, exist_ok=True)
    input_path.write_bytes((b"\x01\x00" * 160) + (b"\x02\x00" * 160))
    server = LoopbackRealtimeServer(case_dir)
    provider_endpoint = server.url
    server.start()
    result: dict[str, Any]
    try:
        result = runner.command(
            "public-yui-loopback-retry",
            [
                "rtk",
                "proxy",
                "env",
                str(binary),
                "--config-dir",
                str(config_dir),
                "session",
                "--record-dir",
                str(record_dir),
                "--provider",
                "openai",
                "--model",
                "gpt-realtime",
                "--api-key",
                "c46-loopback-key",
                "--base-url",
                provider_endpoint,
                "--max-duration",
                "12s",
                "--wait-for-close",
                "--no-terminal-tools",
                "--audio-in-turn",
                str(input_path),
            ],
            REPO_ROOT,
            expect_success=None,
        )
    finally:
        server.close()
    event_log = server.write_event_log()
    require(result["exit_code"] == 0 and not result["timed_out"], f"public yui loopback failed; see {result['stderr_path']}")
    require(result["parent_reaped"] and not result["surviving_process_group_pids"], "public yui left a process-group survivor")
    require(server.accepted and not server.error, f"loopback provider lifecycle failed: {server.error}")
    client_creates = [event for event in server.events if event["direction"] == "client_to_server" and event["type"] == "response.create"]
    server_dones = [event["payload"].get("response", {}) for event in server.events if event["direction"] == "server_to_client" and event["type"] == "response.done"]
    statuses = [done.get("status") for done in server_dones]
    require(len(client_creates) == 2, f"scheduled retry emitted {len(client_creates)} response.create events, want failed attempt plus one retry")
    require(statuses == ["failed", "completed"], f"provider response lifecycle = {statuses}, want failed then completed")
    failed = server_dones[0]
    error = failed.get("status_details", {}).get("error", {})
    require(error.get("code") == "rate_limit_exceeded", f"loopback failure code = {error.get('code')!r}")
    healthy_text = [
        event["payload"].get("delta", "")
        for event in server.events
        if event["direction"] == "server_to_client" and event["type"] == "response.output_audio_transcript.delta"
    ]
    require(HEALTHY_MARKER in healthy_text, f"healthy retry marker missing: {healthy_text}")
    outbound_types = [event["type"] for event in server.events if event["direction"] == "client_to_server"]
    require("input_audio_buffer.append" in outbound_types and "input_audio_buffer.commit" in outbound_types, f"scheduled audio boundary missing: {outbound_types}")
    manifest = validate_record_manifest(record_dir)
    report = {
        "binary": {"path": str(binary), "bytes": binary.stat().st_size, "sha256": sha256_file(binary)},
        "source_revision": subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout.strip(),
        "provider_endpoint": provider_endpoint,
        "provider_event_log": {"path": str(event_log), "sha256": sha256_file(event_log), "events": len(server.events)},
        "response_create_count": len(client_creates),
        "response_statuses": statuses,
        "healthy_marker": HEALTHY_MARKER,
        "record_manifest": manifest,
        "child": result,
        "bounded": {"child_seconds": MAX_CHILD_SECONDS, "aggregate_seconds": MAX_TOTAL_SECONDS},
    }
    report_path = case_dir / "public-report.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    report["report_path"] = str(report_path)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=("wire", "consumer", "regressions", "public", "negative-controls", "all"),
        default="all",
    )
    parser.add_argument("--binary", type=Path, help="already-built shipped yui executable for --mode public/all")
    args = parser.parse_args()
    runner = Runner(args.mode)
    outcome: dict[str, Any] = {}
    try:
        if args.mode in ("wire", "all"):
            outcome["wire"] = run_wire(runner)
        if args.mode in ("consumer", "all"):
            outcome["consumer"] = run_consumer(runner)
        if args.mode in ("regressions", "all"):
            outcome["regressions"] = run_runtime_regressions(runner)
        if args.mode in ("negative-controls", "all"):
            outcome["negative_controls"] = run_negative_controls(runner)
        if args.mode in ("public", "all"):
            require(args.binary is not None, "--binary is required for public verification")
            outcome["public"] = run_public(runner, args.binary.resolve())
        runner.save({"status": "passed", "evidence": outcome})
        print(json.dumps({"status": "passed", "run_dir": str(runner.run_dir), "evidence": outcome}, indent=2))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        runner.save({"status": "failed", "error": str(exc), "evidence": outcome})
        print(json.dumps({"status": "failed", "run_dir": str(runner.run_dir), "error": str(exc)}, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
