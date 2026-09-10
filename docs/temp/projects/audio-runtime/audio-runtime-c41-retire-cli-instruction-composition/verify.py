#!/usr/bin/env python3
"""Run bounded C41 public-contract and process-cleanup evidence."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import sys
import threading
import time


ROOT = Path(__file__).resolve().parent
REPO_ROOT = ROOT.parents[4]
CONSUMER = ROOT / "consumer"
ARTIFACTS = ROOT / "artifacts"
BINARY = ARTIFACTS / "instruction-consumer"
AGENT_CLI = REPO_ROOT / "agent-cli"
YUI_BINARY = ARTIFACTS / "yui"
REPORTS = ROOT / "reports"
REPLAY_FIXTURE = REPO_ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_text_reply.session.json"
MAX_CHILD_SECONDS = 60.0
CLEANUP_SECONDS = 5.0
PUBLIC_PROMPT = "C41 public workflow: inspect the marker file"
PUBLIC_SYSTEM_PROMPT = "literal prompt"
PUBLIC_MARKER = "C41_PUBLIC_TOOL_EFFECT_9182"
PUBLIC_MARKER_FILE = "c41-tool-marker.txt"


class VerificationError(Exception):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command_environment(*, for_go: bool = False) -> dict[str, str]:
    """Use an isolated, credential-free environment for every child."""

    sandbox = ARTIFACTS / "sandbox"
    sandbox.mkdir(parents=True, exist_ok=True)
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(sandbox / "home"),
        "TMPDIR": str(sandbox / "tmp"),
        "LANG": "C",
        "LC_ALL": "C",
        "GOWORK": "off",
        "NO_PROXY": "127.0.0.1,localhost",
        "no_proxy": "127.0.0.1,localhost",
    }
    for directory in ("home", "tmp"):
        (sandbox / directory).mkdir(parents=True, exist_ok=True)
    if for_go:
        environment["GOCACHE"] = str(sandbox / "go-cache")
        module_cache = os.environ.get("GOMODCACHE") or str(Path.home() / "go/pkg/mod")
        environment["GOMODCACHE"] = module_cache
        if os.environ.get("GOTOOLCHAIN"):
            environment["GOTOOLCHAIN"] = os.environ["GOTOOLCHAIN"]
    return environment


def normalize_output(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def run_command(
    argv: list[str],
    cwd: Path,
    *,
    timeout: float = MAX_CHILD_SECONDS,
    environment: dict[str, str] | None = None,
) -> dict:
    require(timeout > 0 and timeout <= MAX_CHILD_SECONDS, f"invalid child timeout {timeout}")
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=environment or command_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        text=True,
    )
    timed_out = False
    cleanup_errors: list[str] = []
    stdout = ""
    stderr = ""
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        partial_stdout = normalize_output(error.output)
        partial_stderr = normalize_output(error.stderr)
        stdout = ""
        stderr = ""
        deadline = time.monotonic() + CLEANUP_SECONDS
        try:
            if os.name == "nt":
                subprocess.run(
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=max(0.1, deadline - time.monotonic()),
                    check=False,
                )
            else:
                os.killpg(process.pid, signal.SIGKILL)
        except (OSError, subprocess.SubprocessError) as error:
            cleanup_errors.append(str(error))
        try:
            more_stdout, more_stderr = process.communicate(timeout=max(0.1, deadline - time.monotonic()))
            # communicate() returns the complete captured stream after the
            # kill. Do not append it to TimeoutExpired.output, which is also
            # a prefix of that stream.
            stdout = normalize_output(more_stdout)
            stderr = normalize_output(more_stderr)
        except subprocess.TimeoutExpired:
            cleanup_errors.append("bounded final wait expired")
            stdout = partial_stdout
            stderr = partial_stderr
    duration = time.monotonic() - started
    return {
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
        "duration_seconds": round(duration, 6),
        "timed_out": timed_out,
        "reaped": process.poll() is not None,
        "cleanup_bounded": not cleanup_errors,
        "cleanup_errors": cleanup_errors,
    }


def go_list_inputs() -> dict:
    result = run_command(["go", "list", "-m", "-json", "all"], CONSUMER, environment=command_environment(for_go=True))
    require(result["exit_code"] == 0 and not result["timed_out"], f"go module graph failed: {result}")
    graph = result["stdout"]
    return {"sha256": hashlib.sha256(graph.encode()).hexdigest(), "stdout": graph}


def go_list_module_inputs(directory: Path) -> dict:
    result = run_command(["go", "list", "-m", "-json", "all"], directory, environment=command_environment(for_go=True))
    require(result["exit_code"] == 0 and not result["timed_out"], f"go module graph failed for {directory}: {result}")
    graph = result["stdout"]
    return {"sha256": hashlib.sha256(graph.encode()).hexdigest(), "stdout": graph}


def direct_imports() -> dict:
    result = run_command(["go", "list", "-json", "."], CONSUMER, environment=command_environment(for_go=True))
    require(result["exit_code"] == 0 and not result["timed_out"], f"consumer import inspection failed: {result}")
    package = json.loads(result["stdout"])
    imports = sorted(package.get("Imports", []))
    forbidden = [item for item in imports if "agent-cli" in item or "/internal/" in item]
    require(not forbidden, f"consumer has forbidden direct imports: {forbidden}")
    return {"imports": imports, "forbidden": forbidden}


def build() -> dict:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_command(
        ["go", "build", "-trimpath", "-o", str(BINARY), "."],
        CONSUMER,
        environment=command_environment(for_go=True),
    )
    require(result["exit_code"] == 0 and not result["timed_out"], f"consumer build failed: {result}")
    require(BINARY.is_file(), "consumer binary was not produced")
    return {
        "command": result,
        "sha256": sha256_file(BINARY),
        "size": BINARY.stat().st_size,
        "path": str(BINARY),
    }


def build_yui() -> dict:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_command(
        ["go", "build", "-trimpath", "-o", str(YUI_BINARY), "./cmd/yui"],
        AGENT_CLI,
        environment=command_environment(for_go=True),
    )
    require(result["exit_code"] == 0 and not result["timed_out"], f"shipped yui build failed: {result}")
    require(YUI_BINARY.is_file(), "shipped yui binary was not produced")
    return {
        "command": result,
        "sha256": sha256_file(YUI_BINARY),
        "size": YUI_BINARY.stat().st_size,
        "path": str(YUI_BINARY),
    }


def parse_stdout(result: dict) -> dict:
    try:
        return json.loads(result["stdout"])
    except json.JSONDecodeError as error:
        raise VerificationError(f"consumer stdout is not JSON: {error}; result={result}") from error


def run_positive(binary: Path, mode: str) -> dict:
    result = run_command([str(binary), "--mode", mode], ROOT)
    require(result["exit_code"] == 0 and not result["timed_out"], f"{mode} failed: {result}")
    report = parse_stdout(result)
    require(report.get("status") == "accepted", f"{mode} status={report}")
    require(report.get("cli_imports") is False and report.get("private_imports") is False, "consumer imported a forbidden package")
    require(report.get("credentials") is False and report.get("hidden_global_io") is False, "consumer used forbidden host state")
    return {"command": result, "report": report}


def run_print(binary: Path) -> dict:
    result = run_command([str(binary), "--mode", "print"], ROOT)
    require(result["exit_code"] == 0 and not result["timed_out"], f"print oracle failed: {result}")
    report = parse_stdout(result)
    composition = report.get("composition", "")
    require(composition.startswith("customer instructions"), "print oracle did not return the composed policy")
    return {"command": result, "report": report}


def policy_block_from_print_oracle(print_report: dict) -> str:
    composition = print_report["report"].get("composition", "")
    start_heading = "Tool-grounding requirements:"
    end_heading = "\n\nWebMCP tab selection calibration:"
    start = composition.find(start_heading)
    require(start >= 0, "print oracle is missing the tool-grounding policy")
    end = composition.find(end_heading, start)
    if end < 0:
        end = len(composition)
    block = composition[start:end]
    require(block.count(start_heading) == 1, "print oracle has an invalid tool-grounding policy block")
    return block


def run_instruction_controls() -> dict:
    commands = [
        (
            [
                "go",
                "test",
                "./internal/transport/cli/internal/livehost",
                "-run",
                "TestBuildRequest(UsesWireInstructionCompositionBeforeProviderStartup|PreservesEmptyAndRejectsInvalidWorkspace)$",
                "-count=1",
                "-timeout=120s",
            ],
            AGENT_CLI,
        ),
        (
            [
                "go",
                "test",
                "./services/session/internal/instructions",
                "-run",
                "TestServiceComposeUsesNormalizedToolIdentifierAndBrowserState$",
                "-count=1",
                "-timeout=120s",
            ],
            REPO_ROOT / "go-agent-runtime",
        ),
    ]
    results = []
    for argv, cwd in commands:
        result = run_command(argv, cwd, environment=command_environment(for_go=True))
        require(result["exit_code"] == 0 and not result["timed_out"], f"focused instruction control failed: {result}")
        results.append(result)
    return {"status": "accepted", "commands": results}


class DeterministicRealtimeProvider:
    """Minimal local RFC 6455 provider for the shipped CLI evidence path."""

    websocket_guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

    def __init__(self, workspace: Path, policy_block: str) -> None:
        self.workspace = workspace.resolve()
        self.policy_block = policy_block
        self.expected_resolved = (
            f"{PUBLIC_SYSTEM_PROMPT}\n\nFilesystem scope: workdir={self.workspace}; "
            "additional_allowed_roots=none. Relative filesystem-tool paths resolve from this workdir."
        )
        self.expected_composed = self.expected_resolved + "\n\n" + policy_block
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(1)
        self.listener.settimeout(0.2)
        self.url = f"ws://127.0.0.1:{self.listener.getsockname()[1]}/v1/realtime"
        self.stop_event = threading.Event()
        self.done_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.connection: socket.socket | None = None
        self.send_lock = threading.Lock()
        self.error = ""
        self.scenario_complete = False
        self.events: list[dict] = []
        self.handshake: dict = {}
        self.bootstrap_session_update: dict = {}
        self.session_update: dict = {}
        self.tool_result: dict = {}
        self.user_turn: dict = {}
        self.response_create_count = 0
        self.initial_instructions = ""

    def start(self) -> None:
        self.thread = threading.Thread(target=self._serve, name="c41-deterministic-provider", daemon=True)
        self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        for connection in (self.connection, self.listener):
            if connection is None:
                continue
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                connection.close()
            except OSError:
                pass

    def join(self, timeout: float = CLEANUP_SECONDS) -> None:
        if self.thread is not None:
            self.thread.join(timeout=timeout)
        require(self.thread is None or not self.thread.is_alive(), "deterministic provider thread did not stop")

    def snapshot(self) -> dict:
        return {
            "url": self.url,
            "handshake": self.handshake,
            "events": self.events,
            "bootstrap_session_update": self.bootstrap_session_update,
            "session_update": self.session_update,
            "user_turn": self.user_turn,
            "tool_result": self.tool_result,
            "response_create_count": self.response_create_count,
            "instruction_observation": {
                "route": "shipped-yui-live-host-bootstrap",
                "mode": "unchanged-output",
                "initial_session_update_before_user_turn": True,
                "initial_instructions": self.initial_instructions,
                "initial_instructions_sha256": hashlib.sha256(self.initial_instructions.encode()).hexdigest(),
                "expected_composed_instructions_sha256": hashlib.sha256(self.expected_composed.encode()).hexdigest(),
                "composed_instructions_match": self.initial_instructions == self.expected_composed,
                "standalone_c41_policy_oracle_sha256": hashlib.sha256(self.policy_block.encode()).hexdigest(),
                "standalone_c41_policy_oracle_used_for_live_request": self.initial_instructions == self.expected_composed,
            },
            "standalone_expected_resolved": self.expected_resolved,
            "scenario_complete": self.scenario_complete,
            "error": self.error,
        }

    def _serve(self) -> None:
        try:
            while not self.stop_event.is_set():
                try:
                    connection, address = self.listener.accept()
                except socket.timeout:
                    continue
                self.connection = connection
                connection.settimeout(0.2)
                self._handle(connection, address)
                return
            raise VerificationError("deterministic provider stopped before accepting the CLI")
        except VerificationError as error:
            self.error = str(error)
        except (OSError, ValueError, json.JSONDecodeError) as error:
            if not self.scenario_complete and not self.stop_event.is_set():
                self.error = f"deterministic provider failed: {error}"
        finally:
            if self.connection is not None:
                try:
                    self.connection.close()
                except OSError:
                    pass
            try:
                self.listener.close()
            except OSError:
                pass
            self.done_event.set()

    def _handle(self, connection: socket.socket, address: tuple[str, int]) -> None:
        self._handshake(connection, address)
        update = self._read_event(connection)
        require(update.get("type") == "session.update", f"first provider client event was {update.get('type')!r}")
        self.bootstrap_session_update = update
        self.session_update = update
        self._validate_session_update(update)
        self._send_json(connection, {"type": "session.created", "session": {"id": "c41-public", "model": "gpt-realtime-2.1"}})

        user_event = self._read_event(connection)
        require(user_event.get("type") == "conversation.item.create", f"expected first user event, got {user_event.get('type')!r}")
        user_item = user_event.get("item")
        require(isinstance(user_item, dict) and user_item.get("type") == "message", "first user event was not a message")
        content = user_item.get("content")
        require(
            isinstance(content, list)
            and len(content) == 1
            and isinstance(content[0], dict)
            and content[0].get("text") == PUBLIC_PROMPT,
            f"public prompt did not reach provider intact: {user_event}",
        )
        self.user_turn = user_event

        response_request = self._read_event(connection)
        require(response_request.get("type") == "response.create", f"expected response.create, got {response_request.get('type')!r}")
        self.response_create_count += 1
        call_id = "call-c41-read-file"
        response_id = "response-c41-tool"
        self._send_json(connection, {"type": "response.created", "response": {"id": response_id}})
        self._send_json(
            connection,
            {
                "type": "response.output_item.added",
                "response_id": response_id,
                "output_index": 0,
                "item": {
                    "id": "item-c41-tool",
                    "type": "function_call",
                    "call_id": call_id,
                    "name": "read_file",
                },
            },
        )
        arguments = json.dumps({"path": PUBLIC_MARKER_FILE}, separators=(",", ":"))
        self._send_json(
            connection,
            {
                "type": "response.function_call_arguments.done",
                "response_id": response_id,
                "call_id": call_id,
                "name": "read_file",
                "arguments": arguments,
            },
        )
        self._send_json(connection, {"type": "response.done", "response": {"id": response_id, "status": "completed"}})

        tool_event = self._read_event(connection)
        require(tool_event.get("type") == "conversation.item.create", f"expected tool result, got {tool_event.get('type')!r}")
        tool_item = tool_event.get("item")
        require(isinstance(tool_item, dict) and tool_item.get("type") == "function_call_output", "provider received a non-tool result")
        require(tool_item.get("call_id") == call_id, f"tool result call id = {tool_item.get('call_id')!r}, want {call_id!r}")
        tool_output = tool_item.get("output", "")
        require(isinstance(tool_output, str) and PUBLIC_MARKER in tool_output, f"read_file result omitted marker: {tool_output!r}")
        self.tool_result = tool_event

        continuation = self._read_event(connection)
        require(continuation.get("type") == "response.create", f"expected tool continuation response.create, got {continuation.get('type')!r}")
        self.response_create_count += 1
        continuation_id = "response-c41-tool-continuation"
        self._send_json(connection, {"type": "response.created", "response": {"id": continuation_id}})
        self._send_json(connection, {"type": "response.output_text.delta", "response_id": continuation_id, "delta": f"C41_TOOL_EFFECT:{PUBLIC_MARKER}"})
        self._send_json(connection, {"type": "response.output_text.done", "response_id": continuation_id})
        self._send_json(connection, {"type": "response.done", "response": {"id": continuation_id, "status": "completed"}})
        self._send_json(connection, {"type": "session.closed", "session_id": "c41-public", "reason": "fixture_complete"})
        self._send_frame(connection, 0x8, struct.pack("!H", 1000))
        self.scenario_complete = True

    def _handshake(self, connection: socket.socket, address: tuple[str, int]) -> None:
        data = bytearray()
        while b"\r\n\r\n" not in data:
            try:
                chunk = connection.recv(4096)
            except socket.timeout:
                if self.stop_event.is_set():
                    raise VerificationError("provider stopped during WebSocket handshake")
                continue
            if not chunk:
                raise VerificationError("CLI closed before completing WebSocket handshake")
            data.extend(chunk)
            require(len(data) <= 64 * 1024, "WebSocket handshake exceeded bounded header size")
        lines = data.decode("iso-8859-1").split("\r\n")
        require(lines and lines[0].startswith("GET "), f"unexpected WebSocket request line: {lines[0] if lines else '<empty>'}")
        headers: dict[str, str] = {}
        for line in lines[1:]:
            if not line:
                break
            if ":" not in line:
                continue
            key, value = line.split(":", 1)
            headers[key.strip().lower()] = value.strip()
        key = headers.get("sec-websocket-key", "")
        require(key != "", "WebSocket handshake omitted Sec-WebSocket-Key")
        authorization = headers.get("authorization", "")
        require(authorization == "Bearer hermetic-key", "deterministic provider received an unexpected authorization header")
        request_parts = lines[0].split()
        self.handshake = {
            "address": f"{address[0]}:{address[1]}",
            "request_target": request_parts[1] if len(request_parts) > 1 else "",
            "authorization_valid": True,
            "upgrade": headers.get("upgrade", "").lower(),
        }
        accept = base64.b64encode(hashlib.sha1((key + self.websocket_guid).encode("ascii")).digest()).decode("ascii")
        connection.sendall(
            (
                "HTTP/1.1 101 Switching Protocols\r\n"
                "Upgrade: websocket\r\n"
                "Connection: Upgrade\r\n"
                f"Sec-WebSocket-Accept: {accept}\r\n\r\n"
            ).encode("ascii")
        )

    def _recv_exact(self, connection: socket.socket, size: int) -> bytes | None:
        data = bytearray()
        while len(data) < size:
            try:
                chunk = connection.recv(size - len(data))
            except socket.timeout:
                if self.stop_event.is_set():
                    return None
                continue
            if not chunk:
                return None
            data.extend(chunk)
        return bytes(data)

    def _read_frame(self, connection: socket.socket) -> bytes | None:
        fragments = bytearray()
        message_opcode = None
        while True:
            header = self._recv_exact(connection, 2)
            if header is None:
                return None
            first, second = header
            fin = bool(first & 0x80)
            opcode = first & 0x0F
            masked = bool(second & 0x80)
            length = second & 0x7F
            if length == 126:
                extended = self._recv_exact(connection, 2)
                if extended is None:
                    return None
                length = struct.unpack("!H", extended)[0]
            elif length == 127:
                extended = self._recv_exact(connection, 8)
                if extended is None:
                    return None
                length = struct.unpack("!Q", extended)[0]
            require(length <= 4 * 1024 * 1024, "WebSocket frame exceeded bounded provider payload size")
            mask_key = self._recv_exact(connection, 4) if masked else None
            if masked:
                require(mask_key is not None, "masked WebSocket frame omitted its mask key")
            payload = self._recv_exact(connection, length)
            if payload is None:
                return None
            if masked:
                payload = bytes(value ^ mask_key[index % 4] for index, value in enumerate(payload))
            if opcode == 0x9:
                self._send_frame(connection, 0xA, payload)
                continue
            if opcode == 0x8:
                return None
            if message_opcode is None:
                require(opcode in (0x1, 0x2), f"unsupported WebSocket frame opcode={opcode} fin={fin}")
                message_opcode = opcode
            else:
                require(opcode == 0x0, f"unsupported fragmented WebSocket frame opcode={opcode}")
            fragments.extend(payload)
            require(len(fragments) <= 4 * 1024 * 1024, "WebSocket message exceeded bounded provider payload size")
            if fin:
                return bytes(fragments)

    def _read_event(self, connection: socket.socket) -> dict:
        payload = self._read_frame(connection)
        require(payload is not None, "CLI closed the provider connection before the expected event")
        try:
            event = json.loads(payload.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise VerificationError(f"provider received invalid JSON WebSocket payload: {error}") from error
        require(isinstance(event, dict), "provider received a non-object JSON event")
        event_type = event.get("type", "")
        require(isinstance(event_type, str) and event_type != "", "provider received an event without type")
        self.events.append({"direction": "client_to_server", "sequence": len(self.events) + 1, "type": event_type, "payload": event})
        return event

    def _send_frame(self, connection: socket.socket, opcode: int, payload: bytes) -> None:
        first = 0x80 | opcode
        length = len(payload)
        if length < 126:
            header = bytes((first, length))
        elif length <= 0xFFFF:
            header = bytes((first, 126)) + struct.pack("!H", length)
        else:
            header = bytes((first, 127)) + struct.pack("!Q", length)
        with self.send_lock:
            connection.sendall(header + payload)

    def _send_json(self, connection: socket.socket, event: dict) -> None:
        event_type = event.get("type", "")
        self.events.append({"direction": "server_to_client", "sequence": len(self.events) + 1, "type": event_type, "payload": event})
        self._send_frame(connection, 0x1, json.dumps(event, separators=(",", ":"), sort_keys=True).encode("utf-8"))

    def _validate_session_update(self, event: dict) -> None:
        session = event.get("session")
        require(isinstance(session, dict), "session.update did not contain a session object")
        instructions = session.get("instructions")
        self.initial_instructions = instructions if isinstance(instructions, str) else ""
        require(
            instructions == self.expected_composed,
            "shipped CLI bootstrap session.update did not use the Wire-composed instructions",
        )
        require("Tool-grounding requirements:" in instructions, "shipped CLI bootstrap omitted the composed tool policy")
        require(session.get("model") == "gpt-realtime-2.1", f"shipped CLI selected an unexpected model: {session.get('model')!r}")
        tools = session.get("tools")
        require(isinstance(tools, list) and tools, "shipped CLI session.update did not advertise tools")
        names = [tool.get("name") for tool in tools if isinstance(tool, dict)]
        require("read_file" in names, f"shipped CLI session.update omitted read_file: {names}")


def run_negative(binary: Path) -> dict:
    result = run_command([str(binary), "--mode", "consumer-negative"], ROOT)
    require(result["exit_code"] == 1 and not result["timed_out"], f"negative control exit={result}")
    report = parse_stdout(result)
    require("policy oracle mismatch" in report.get("error", ""), f"negative control did not fail on the mutated oracle: {report}")
    return {"command": result, "report": report}


def run_cleanup(binary: Path) -> dict:
    result = run_command([str(binary), "--mode", "cleanup-child"], ROOT, timeout=0.25)
    require(result["timed_out"] is True, f"cleanup child unexpectedly completed: {result}")
    require(result["reaped"] is True and result["cleanup_bounded"] is True, f"cleanup was not bounded/reaped: {result}")
    return {
        "command": result,
        "report": {
            "timeout_cleanup": True,
            "reaped": result["reaped"],
            "cleanup_bounded": result["cleanup_bounded"],
        },
    }


def run_yui_public_session(binary: Path, policy_block: str) -> dict:
    public_root = ARTIFACTS / "public-session"
    workspace = public_root / "workspace"
    config_dir = public_root / "config"
    capture = public_root / "session.capture.json"
    workspace.mkdir(parents=True, exist_ok=True)
    config_dir.mkdir(parents=True, exist_ok=True)
    for stale_capture in (capture, capture.with_suffix(".jsonl")):
        try:
            stale_capture.unlink()
        except FileNotFoundError:
            pass
    (workspace / PUBLIC_MARKER_FILE).write_text(PUBLIC_MARKER + "\n", encoding="utf-8")
    provider = DeterministicRealtimeProvider(workspace, policy_block)
    provider.start()
    result = None
    try:
        result = run_command(
            [
                str(binary),
                "--config-dir",
                str(config_dir),
                "--workdir",
                str(workspace),
                "session",
                "--record",
                str(capture),
                "--provider",
                "openai",
                "--model",
                "gpt-realtime-2.1",
                "--api-key",
                "hermetic-key",
                "--base-url",
                provider.url,
                "--system-prompt",
                PUBLIC_SYSTEM_PROMPT,
                "--prompt",
                PUBLIC_PROMPT,
                "--wait-for-close",
                "--max-duration",
                "30s",
            ],
            ROOT,
            timeout=MAX_CHILD_SECONDS,
            environment=command_environment(for_go=False),
        )
    finally:
        provider.stop()
        provider.join()
    observation = provider.snapshot()
    require(result is not None, "shipped yui public workflow did not produce a process result")
    require(result["exit_code"] == 0 and not result["timed_out"], f"shipped yui public workflow failed: {result}; provider={observation}")
    require(observation["error"] == "", f"deterministic provider rejected the shipped yui workflow: {observation}")
    require(observation["scenario_complete"] is True, f"deterministic provider did not complete the public workflow: {observation}")
    require(result["reaped"] is True and result["cleanup_bounded"] is True, f"shipped yui process was not cleanly reaped: {result}")
    require(f"C41_TOOL_EFFECT:{PUBLIC_MARKER}" in result["stdout"], "shipped yui output omitted the provider-confirmed tool effect")
    require(capture.is_file(), "shipped yui did not write the requested session capture")
    return {
        "command": result,
        "provider": observation,
        "standalone_policy_oracle": {
            "resolved_instructions": observation["standalone_expected_resolved"],
            "composed_instructions_sha256": hashlib.sha256((observation["standalone_expected_resolved"] + "\n\n" + policy_block).encode()).hexdigest(),
            "policy_block_sha256": hashlib.sha256(policy_block.encode()).hexdigest(),
            "used_for_shipped_live_request": observation["instruction_observation"]["standalone_c41_policy_oracle_used_for_live_request"],
        },
        "shipped_bootstrap": {
            "instructions": observation["instruction_observation"]["initial_instructions"],
            "instructions_sha256": observation["instruction_observation"]["initial_instructions_sha256"],
            "expected_composed_instructions_sha256": observation["instruction_observation"]["expected_composed_instructions_sha256"],
            "composed_instructions_match": observation["instruction_observation"]["composed_instructions_match"],
            "observed_before_user_turn": observation["instruction_observation"]["initial_session_update_before_user_turn"],
            "tool_name": "read_file",
            "prompt": PUBLIC_PROMPT,
        },
        "tool_effect": {
            "marker": PUBLIC_MARKER,
            "marker_file": str(workspace / PUBLIC_MARKER_FILE),
            "provider_received_marker": True,
            "cli_output_marker": True,
        },
        "capture": {
            "path": str(capture),
            "sha256": sha256_file(capture),
            "size": capture.stat().st_size,
        },
        "clean_shutdown": {
            "provider_session_closed_sent": True,
            "process_exit_code": result["exit_code"],
            "process_reaped": result["reaped"],
            "cleanup_bounded": result["cleanup_bounded"],
            "provider_thread_stopped": True,
        },
    }


def run_yui_regression(binary: Path) -> dict:
    regression_root = ARTIFACTS / "regression"
    workspace = regression_root / "workspace"
    config_dir = regression_root / "config"
    workspace.mkdir(parents=True, exist_ok=True)
    config_dir.mkdir(parents=True, exist_ok=True)
    result = run_command(
        [
            str(binary),
            "--config-dir",
            str(config_dir),
            "--workdir",
            str(workspace),
            "session",
            "--replay",
            str(REPLAY_FIXTURE),
        ],
        ROOT,
        timeout=MAX_CHILD_SECONDS,
        environment=command_environment(for_go=False),
    )
    expected_text = "Hello! How can I help you today?"
    require(result["exit_code"] == 0 and not result["timed_out"], f"shipped yui replay regression failed: {result}")
    require(expected_text in result["stdout"], "shipped yui replay regression omitted the committed text response")
    require(result["reaped"] is True and result["cleanup_bounded"] is True, f"shipped yui replay was not cleanly reaped: {result}")
    return {
        "command": result,
        "fixture": {"path": str(REPLAY_FIXTURE), "sha256": sha256_file(REPLAY_FIXTURE), "size": REPLAY_FIXTURE.stat().st_size},
        "assertions": {"expected_text": expected_text, "text_observed": True, "network": "offline_replay"},
        "clean_shutdown": {"process_exit_code": result["exit_code"], "process_reaped": result["reaped"], "cleanup_bounded": result["cleanup_bounded"]},
    }


def git_revision() -> str:
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
        env=command_environment(),
    )
    return result.stdout.strip()


def git_status() -> str:
    result = subprocess.run(
        ["git", "status", "--short", "--untracked-files=all"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
        env=command_environment(),
    )
    report_prefix = str(REPORTS.relative_to(REPO_ROOT)) + "/"
    lines = [line for line in result.stdout.splitlines() if not line[3:].startswith(report_prefix)]
    return "\n".join(lines) + ("\n" if lines else "")


def tracked_tree_descriptor(prefix: str) -> dict[str, str | int]:
    result = subprocess.run(
        ["git", "ls-files", "-z", "--", prefix],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        env=command_environment(),
    )
    paths = [item for item in result.stdout.decode("utf-8").split("\0") if item]
    digest = hashlib.sha256()
    for relative in paths:
        path = REPO_ROOT / relative
        require(path.is_file(), f"tracked build input is not a regular file: {relative}")
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(bytes.fromhex(sha256_file(path)))
        digest.update(b"\0")
    return {"prefix": prefix, "file_count": len(paths), "sha256": digest.hexdigest()}


def build_input_descriptors() -> dict:
    module_files = []
    for relative in ("go.work", "go.work.sum", "agent-cli/go.mod", "agent-cli/go.sum"):
        path = REPO_ROOT / relative
        if path.is_file():
            module_files.append({"path": relative, "size": path.stat().st_size, "sha256": sha256_file(path)})
    return {
        "module_files": module_files,
        "source_trees": [
            tracked_tree_descriptor("agent-cli"),
            tracked_tree_descriptor("go-agent-runtime"),
            tracked_tree_descriptor("go-agent-loop"),
            tracked_tree_descriptor("go-audio"),
            tracked_tree_descriptor("go-device-gateway"),
            tracked_tree_descriptor("go-llm-gateway"),
        ],
    }


def input_descriptors() -> list[dict[str, str | int]]:
    paths = [CONSUMER / "go.mod", CONSUMER / "expected.json", CONSUMER / "main.go", CONSUMER / "main_test.go"]
    descriptors = []
    for path in paths:
        descriptors.append({"path": str(path.relative_to(ROOT)), "size": path.stat().st_size, "sha256": sha256_file(path)})
    return descriptors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=("consumer", "consumer-negative", "public-session", "regression", "cleanup-negative"),
        default="consumer",
    )
    args = parser.parse_args()
    started = time.monotonic()
    result: dict = {
        "schema": "audio-runtime-c41-instruction-evidence/v1",
        "mode": args.mode,
        "source_revision": git_revision(),
        "worktree_status_at_start": git_status(),
        "input_files": input_descriptors(),
        "build_inputs": build_input_descriptors(),
    }
    try:
        result["module_graph"] = {
            "consumer": go_list_inputs(),
            "agent_cli": go_list_module_inputs(AGENT_CLI),
        }
        result["direct_imports"] = direct_imports()
        result["build"] = build()
        result["focused_instruction_controls"] = run_instruction_controls()
        yui_binary = None
        if args.mode in ("public-session", "regression"):
            result["cli_build"] = build_yui()
            yui_binary = YUI_BINARY.resolve()
        binary = BINARY.resolve()
        if args.mode == "public-session":
            result["consumer_contract"] = run_positive(binary, args.mode)
            print_oracle = run_print(binary)
            result["policy_oracle"] = print_oracle
            result["public_session"] = run_yui_public_session(yui_binary, policy_block_from_print_oracle(print_oracle))
        elif args.mode == "regression":
            result["regression_contract"] = run_positive(binary, args.mode)
            result["regression"] = run_yui_regression(yui_binary)
        elif args.mode == "consumer":
            result[args.mode.replace("-", "_")] = run_positive(binary, args.mode)
        elif args.mode == "consumer-negative":
            result["negative"] = run_negative(binary)
        else:
            result["cleanup"] = run_cleanup(binary)
        require(time.monotonic() - started <= 600, "overall evidence watchdog exceeded")
        result["status"] = "accepted"
    except (OSError, subprocess.SubprocessError, VerificationError) as error:
        result["status"] = "rejected"
        result["error"] = str(error)
    REPORTS.mkdir(parents=True, exist_ok=True)
    output = REPORTS / f"{args.mode}.json"
    output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result.get("status") == "accepted" else 1


if __name__ == "__main__":
    sys.exit(main())
