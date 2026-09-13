#!/usr/bin/env python3
"""Run bounded, credential-free C112 tool and interruption replay cases."""

from __future__ import annotations

import argparse
import base64
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import select
import struct
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import wave
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures"
CASES = {
    "trace-audio-tool-replay": {
        "fixture": FIXTURES / "c16-audio-tool.session.json", "fixture_sha256": "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
        "provider_bytes": 4800, "provider_sha256": "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502",
        "rendered_bytes": 3200, "rendered_sha256": "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805", "runtime_min": 1,
        "audio_input": True, "audio_input_sha256": "db9ac5111b2173f5f0e7909539a7dea70134b5736d4f2582c99e25705aab6148", "derived_fixture_sha256": "62835dcb8270ab1dba865d078e79b13a3c6c3c89454c65f5eb7f5bb42586a714",
        "required_taps": {"microphone_pre_gate", "speaker_enqueued"},
        "required_runtime_kinds": {"provider_wire_receive", "provider_wire_send"}, "required_provider_wire_types": {"input_audio_buffer.append", "input_audio_buffer.commit"},
    },
    "interruption-replay": {
        "fixture": FIXTURES / "c16-interruption.session.json", "fixture_sha256": "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
        "provider_bytes": 3840, "provider_sha256": "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22",
        "rendered_bytes": 3360, "rendered_sha256": "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff", "runtime_min": 1,
        "required_taps": {"speaker_enqueued"}, "required_runtime_kinds": {"provider_wire_receive", "provider_wire_send"}, "required_provider_wire_types": set(),
    },
}
SERVICE_BOUNDARY_TAPS = {"microphone_pre_gate", "microphone_uploaded", "speaker_enqueued", "speaker_rendered"}
SECRET_ENV_NAMES = ("OPENAI_API_KEY", "OPENROUTER_API_KEY", "GROK_API_KEY", "ANTHROPIC_API_KEY")
OUTPUT_LIMIT = 256 * 1024
TERM_GRACE = 2.0
KILL_GRACE = 2.0
DEVICE_RATE = 24000
DEVICE_QUANTUM = 480
DEVICE_CALLBACKS = 9


class EvidenceFailure(RuntimeError):
    pass


@dataclass
class CappedOutput:
    data: bytearray
    total: int = 0
    truncated: bool = False

    def append(self, chunk: bytes) -> None:
        self.total += len(chunk)
        remaining = OUTPUT_LIMIT - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        self.truncated = self.truncated or self.total > OUTPUT_LIMIT

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        return value + (f"\n[output truncated after {OUTPUT_LIMIT} bytes]\n" if self.truncated else "")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def read_stream(stream: Any, output: CappedOutput) -> None:
    while chunk := stream.read(8192):
        output.append(chunk)


def is_credential_environment_name(name: str) -> bool:
    return name in SECRET_ENV_NAMES or (name.startswith("AGENT_MODEL__") and name.endswith("__API_KEY"))


def credential_environment_names(environment: dict[str, str]) -> list[str]:
    return sorted(name for name in environment if is_credential_environment_name(name))


def surviving_group(pgid: int) -> list[int]:
    try:
        result = subprocess.run(["ps", "-eo", "pid=,pgid="], capture_output=True, text=True, check=True, timeout=2)
    except (OSError, subprocess.SubprocessError):
        return []
    return [int(fields[0]) for line in result.stdout.splitlines() if len(fields := line.split()) == 2 and fields[1] == str(pgid) and fields[0].isdigit()]


def run_child(
    label: str,
    argv: list[str],
    cwd: Path,
    output_dir: Path,
    timeout: float,
    controller: Any | None = None,
) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ, GOWORK="off")
    removed_credentials = credential_environment_names(environment)
    for name in removed_credentials:
        environment.pop(name, None)
    stdout, stderr = CappedOutput(bytearray()), CappedOutput(bytearray())
    started = time.monotonic()
    try:
        process = subprocess.Popen(argv, cwd=cwd, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    except OSError as error:
        raise EvidenceFailure(f"could not launch {label}: {error}") from error
    assert process.stdout is not None and process.stderr is not None
    readers = [threading.Thread(target=read_stream, args=(process.stdout, stdout), daemon=True), threading.Thread(target=read_stream, args=(process.stderr, stderr), daemon=True)]
    for reader in readers:
        reader.start()
    timed_out = False
    controller_error: BaseException | None = None
    sigterm_sent = sigkill_sent = False
    try:
        if controller is not None:
            try:
                controller(process)
            except BaseException as error:
                controller_error = error
        if controller_error is None:
            try:
                process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                timed_out = True
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                else:
                    sigterm_sent = True
                try:
                    process.wait(timeout=TERM_GRACE)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    else:
                        sigkill_sent = True
                    process.wait(timeout=KILL_GRACE)
        elif process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            else:
                sigterm_sent = True
            try:
                process.wait(timeout=TERM_GRACE)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                else:
                    sigkill_sent = True
                process.wait(timeout=KILL_GRACE)
    finally:
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
                sigkill_sent = True
            except ProcessLookupError:
                pass
            process.wait(timeout=KILL_GRACE)
        for reader in readers:
            reader.join(timeout=KILL_GRACE if timed_out else TERM_GRACE)
        process.stdout.close()
        process.stderr.close()
    result = {
        "label": label, "argv": argv, "cwd": str(cwd), "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6), "exit_code": process.returncode, "timed_out": timed_out,
        "stdout": stdout.text(), "stderr": stderr.text(), "stdout_bytes": stdout.total, "stderr_bytes": stderr.total,
        "stdout_truncated": stdout.truncated, "stderr_truncated": stderr.truncated,
        "cleanup": {"sigterm_sent": sigterm_sent, "sigkill_sent": sigkill_sent, "surviving_process_group_pids": surviving_group(process.pid)},
        "removed_credential_environment_names": removed_credentials,
        "credential_free_environment": not credential_environment_names(environment),
    }
    (output_dir / "process.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    if controller_error is not None:
        if isinstance(controller_error, EvidenceFailure):
            raise controller_error
        raise EvidenceFailure(f"{label} controller failed: {controller_error}") from controller_error
    return result


def require_clean(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} did not exit cleanly: {result}")
    if result["cleanup"]["surviving_process_group_pids"] or not result["credential_free_environment"]:
        raise EvidenceFailure(f"{result['label']} violated cleanup or credential boundary: {result}")
    if result["stdout_truncated"] or result["stderr_truncated"]:
        raise EvidenceFailure(f"{result['label']} exceeded output cap")


def device_request(endpoint: str, path: str, method: str = "GET", body: bytes | None = None) -> Any:
    request = urllib.request.Request(f"http://{endpoint}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json" if path.endswith("/advance") else "application/octet-stream")
    try:
        with urllib.request.urlopen(request, timeout=2.0) as response:
            raw = response.read()
    except (OSError, urllib.error.URLError) as error:
        raise EvidenceFailure(f"audio-device request {method} {path} failed: {error}") from error
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"audio-device request {method} {path} returned invalid JSON: {error}") from error


def device_snapshot(endpoint: str) -> dict[str, Any]:
    snapshot = device_request(endpoint, "/v1/audio-device/control/snapshot")
    if not isinstance(snapshot, dict):
        raise EvidenceFailure(f"audio-device snapshot is not an object: {snapshot!r}")
    return snapshot


def device_inject_capture(endpoint: str, pcm: bytes) -> None:
    device_request(endpoint, "/v1/audio-device/control/inject-capture", "POST", pcm)


def device_advance(endpoint: str) -> None:
    body = json.dumps({"callbacks": 1}, separators=(",", ":")).encode("utf-8")
    device_request(endpoint, "/v1/audio-device/control/advance", "POST", body)


def stat_value(snapshot: dict[str, Any], section: str, key: str) -> Any:
    value = snapshot.get(section, {})
    if not isinstance(value, dict) or key not in value:
        raise EvidenceFailure(f"audio-device snapshot lacks {section}.{key}: {snapshot!r}")
    return value[key]


def pcm16_bytes(samples: list[int]) -> bytes:
    try:
        return struct.pack(f"<{len(samples)}h", *samples)
    except struct.error as error:
        raise EvidenceFailure(f"invalid audio-device PCM samples: {error}") from error


def mix_pcm16(left: bytes, right: bytes) -> bytes:
    if len(left) != len(right) or len(left) % 2:
        raise EvidenceFailure(f"cannot mix unequal PCM16 blocks: {len(left)} != {len(right)}")
    left_samples = struct.unpack(f"<{len(left) // 2}h", left)
    right_samples = struct.unpack(f"<{len(right) // 2}h", right)
    mixed = [max(-32768, min(32767, first + second)) for first, second in zip(left_samples, right_samples)]
    return pcm16_bytes(mixed)


def provider_audio_pcm(capture: dict[str, Any]) -> bytes:
    pcm = bytearray()
    for record in capture.get("records", []):
        if record.get("direction") != "server_to_client" or record.get("type") != "response.output_audio.delta":
            continue
        try:
            pcm.extend(base64.b64decode(record["payload"]["delta"], validate=True))
        except (KeyError, TypeError, ValueError) as error:
            raise EvidenceFailure(f"invalid provider audio fixture delta: {error}") from error
    return bytes(pcm)


def build_device_fixture(source: Path, target: Path, input_pcm: bytes) -> dict[str, Any]:
    capture = json.loads(source.read_text(encoding="utf-8"))
    provider_pcm = provider_audio_pcm(capture)
    block_bytes = DEVICE_QUANTUM * 2
    if len(provider_pcm) == 0 or len(provider_pcm) % block_bytes:
        raise EvidenceFailure(f"provider audio is not aligned to device callbacks: {len(provider_pcm)} bytes")
    if len(input_pcm) < block_bytes:
        raise EvidenceFailure(f"device input is shorter than one callback: {len(input_pcm)} bytes")
    render_blocks = [provider_pcm[offset : offset + block_bytes] for offset in range(0, len(provider_pcm), block_bytes)]
    render_blocks.extend(bytes(block_bytes) for _ in range(DEVICE_CALLBACKS - len(render_blocks)))
    blocks = [mix_pcm16(render_blocks[0], input_pcm[:block_bytes])]
    blocks.extend(render_blocks[1:])
    if len(blocks) != DEVICE_CALLBACKS:
        raise EvidenceFailure(f"device fixture needs {DEVICE_CALLBACKS} callbacks, got {len(blocks)}")

    closing_records = [record for record in capture["records"] if record["direction"] == "server_to_client" and record["type"] == "session.closed"]
    if len(closing_records) != 1:
        raise EvidenceFailure(f"device fixture source has {len(closing_records)} session.closed records: {source}")
    closing_payload = closing_records[0]["payload"]
    records = [record for record in capture["records"] if not (record["direction"] == "server_to_client" and record["type"] == "session.closed")]
    for block in blocks:
        records.append({
            "direction": "client_to_server", "timestamp_ms": 0, "type": "input_audio_buffer.append",
            "payload_type": "websocket_message",
            "payload": {"type": "input_audio_buffer.append", "audio": base64.b64encode(block).decode("ascii")},
        })
    records.append({
        "direction": "server_to_client", "timestamp_ms": 400, "type": "rate_limits.updated",
        "payload_type": "websocket_message", "payload": {"type": "rate_limits.updated", "rate_limits": []},
    })
    records.append({
        "direction": "server_to_client", "timestamp_ms": 410, "type": "session.closed",
        "payload_type": "websocket_message", "payload": closing_payload,
    })
    normalized_records = []
    for sequence, record in enumerate(records, 1):
        normalized_records.append({
            "sequence": sequence, "direction": record["direction"],
            "timestamp_ms": record["timestamp_ms"] if record["timestamp_ms"] != 0 else sequence * 10,
            "type": record["type"], "payload_type": record["payload_type"], "payload": record["payload"],
        })
    capture["records"] = normalized_records
    seal_derived_fixture(capture)
    target.write_text(json.dumps(capture, indent=2) + "\n", encoding="utf-8")
    return {
        "fixture": str(target), "fixture_sha256": sha256_file(target), "provider_pcm": provider_pcm,
        "blocks": blocks, "render_blocks": render_blocks, "input_pcm": input_pcm[:block_bytes],
    }


def provider_wire_audio(events: list[dict[str, Any]], message_type: str) -> list[bytes]:
    audio: list[bytes] = []
    for event in events:
        if event.get("kind") != "runtime" or event.get("runtime_kind") != "provider_wire_send":
            continue
        try:
            envelope = json.loads(base64.b64decode(event.get("payload", "")))
            payload = envelope.get("payload", {})
            if payload.get("type") == message_type:
                audio.append(base64.b64decode(payload["audio"], validate=True))
        except (ValueError, TypeError, KeyError, json.JSONDecodeError):
            continue
    return audio


def live_yui_wire_types(case_dir: Path) -> set[str]:
    types: set[str] = set()
    for timeline in sorted(case_dir.glob(".session-audio-trace-*/timeline.jsonl")):
        try:
            for line in timeline.read_text(encoding="utf-8").splitlines():
                if not line:
                    continue
                try:
                    event = json.loads(line)
                    types.update(provider_wire_types([event]))
                except (json.JSONDecodeError, TypeError, ValueError):
                    continue
        except OSError:
            continue
    return types


def start_device_server(binary: Path, output_dir: Path, deadline: float) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ, GOWORK="off")
    removed_credentials = credential_environment_names(environment)
    for name in removed_credentials:
        environment.pop(name, None)
    stdout, stderr = CappedOutput(bytearray()), CappedOutput(bytearray())
    try:
        process = subprocess.Popen(
            [str(binary), "--listen", "127.0.0.1:0", "--sample-rate", str(DEVICE_RATE),
             "--render-quantum", str(DEVICE_QUANTUM), "--capture-quantum", str(DEVICE_QUANTUM), "--manual-clock"],
            cwd=ROOT, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as error:
        raise EvidenceFailure(f"could not launch audio-device-server: {error}") from error
    assert process.stdout is not None and process.stderr is not None
    stderr_reader = threading.Thread(target=read_stream, args=(process.stderr, stderr), daemon=True)
    stderr_reader.start()
    ready: dict[str, Any] | None = None
    try:
        while time.monotonic() < deadline:
            if process.poll() is not None:
                break
            remaining = max(0.01, min(0.25, deadline - time.monotonic()))
            readable, _, _ = select.select([process.stdout], [], [], remaining)
            if not readable:
                continue
            line = process.stdout.readline()
            if not line:
                break
            stdout.append(line)
            try:
                candidate = json.loads(line.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError) as error:
                raise EvidenceFailure(f"audio-device-server readiness is invalid: {error}") from error
            if not isinstance(candidate, dict) or not isinstance(candidate.get("endpoint"), str):
                raise EvidenceFailure(f"audio-device-server readiness is incomplete: {candidate!r}")
            ready = candidate
            break
        if ready is None:
            raise EvidenceFailure(f"audio-device-server did not become ready: {stderr.text()}")
        stdout_reader = threading.Thread(target=read_stream, args=(process.stdout, stdout), daemon=True)
        stdout_reader.start()
        return {
            "process": process, "endpoint": ready["endpoint"], "ready": ready, "stdout": stdout, "stderr": stderr,
            "readers": [stderr_reader, stdout_reader], "removed_credential_environment_names": removed_credentials,
            "credential_free_environment": not credential_environment_names(environment), "started": time.monotonic(),
            "output_dir": output_dir,
        }
    except BaseException:
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=TERM_GRACE)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=KILL_GRACE)
        stderr_reader.join(timeout=TERM_GRACE)
        process.stdout.close()
        process.stderr.close()
        raise


def stop_device_server(server: dict[str, Any]) -> dict[str, Any]:
    process = server["process"]
    sigterm_sent = sigkill_sent = False
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        else:
            sigterm_sent = True
        try:
            process.wait(timeout=TERM_GRACE)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            else:
                sigkill_sent = True
            process.wait(timeout=KILL_GRACE)
    for reader in server["readers"]:
        reader.join(timeout=KILL_GRACE)
    process.stdout.close()
    process.stderr.close()
    result = {
        "argv": [str(server["process"].args[0]), "--listen", "127.0.0.1:0", "--sample-rate", str(DEVICE_RATE),
                 "--render-quantum", str(DEVICE_QUANTUM), "--capture-quantum", str(DEVICE_QUANTUM), "--manual-clock"],
        "endpoint": server["endpoint"], "ready": server["ready"], "elapsed_seconds": round(time.monotonic() - server["started"], 6),
        "exit_code": process.returncode, "stdout": server["stdout"].text(), "stderr": server["stderr"].text(),
        "stdout_bytes": server["stdout"].total, "stderr_bytes": server["stderr"].total,
        "stdout_truncated": server["stdout"].truncated, "stderr_truncated": server["stderr"].truncated,
        "cleanup": {"sigterm_sent": sigterm_sent, "sigkill_sent": sigkill_sent, "surviving_process_group_pids": surviving_group(process.pid)},
        "removed_credential_environment_names": server["removed_credential_environment_names"],
        "credential_free_environment": server["credential_free_environment"],
    }
    (server["output_dir"] / "process.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    if result["cleanup"]["surviving_process_group_pids"] or not result["credential_free_environment"]:
        raise EvidenceFailure(f"audio-device-server violated cleanup or credential boundary: {result}")
    if result["stdout_truncated"] or result["stderr_truncated"]:
        raise EvidenceFailure("audio-device-server exceeded output cap")
    return result


def control_device_replay(
    process: subprocess.Popen[bytes],
    endpoint: str,
    device_info: dict[str, Any],
    output_dir: Path,
    deadline: float,
) -> dict[str, Any]:
    provider_pcm = device_info["provider_pcm"]
    blocks = device_info["blocks"]
    expected_provider_samples = len(provider_pcm) // 2
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise EvidenceFailure(f"device replay exited before playback was queued: {process.returncode}")
        snapshot = device_snapshot(endpoint)
        if stat_value(snapshot, "playback", "QueuedSamples") >= expected_provider_samples:
            time.sleep(0.5)
            break
        time.sleep(0.01)
    else:
        raise EvidenceFailure("device replay never queued the provider audio before the control deadline")

    device_inject_capture(endpoint, device_info["input_pcm"])
    for callback in range(1, DEVICE_CALLBACKS + 1):
        device_advance(endpoint)
        while time.monotonic() < deadline:
            snapshot = device_snapshot(endpoint)
            output_dir.mkdir(parents=True, exist_ok=True)
            (output_dir / f"callback-{callback}.json").write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8")
            if stat_value(snapshot, "capture", "CompletedFrames") >= callback:
                break
            time.sleep(0.01)
        else:
            raise EvidenceFailure(f"device replay did not drain capture callback {callback}")

    snapshot = device_snapshot(endpoint)
    render_blocks = device_info["render_blocks"]
    expected_rendered = b"".join(render_blocks)
    expected_captured = b"".join(blocks)
    rendered_samples = snapshot.get("rendered_samples")
    captured_samples = snapshot.get("captured_samples")
    if not isinstance(rendered_samples, list) or not isinstance(captured_samples, list):
        raise EvidenceFailure(f"device snapshot lacks PCM evidence: {snapshot!r}")
    rendered_pcm = pcm16_bytes(rendered_samples)
    captured_pcm = pcm16_bytes(captured_samples)
    if rendered_pcm != expected_rendered or captured_pcm != expected_captured:
        raise EvidenceFailure(
            f"device loopback PCM mismatch: rendered={sha256_bytes(rendered_pcm)}, expected_rendered={sha256_bytes(expected_rendered)}, "
            f"captured={sha256_bytes(captured_pcm)}, expected_captured={sha256_bytes(expected_captured)}"
        )
    trace = snapshot.get("trace")
    if not isinstance(trace, list):
        raise EvidenceFailure(f"device snapshot lacks callback trace: {snapshot!r}")
    render_trace = [event for event in trace if event.get("tap") == "render"]
    capture_trace = [event for event in trace if event.get("tap") == "capture"]
    render_hashes = [event.get("payload_sha256") for event in render_trace]
    capture_hashes = [event.get("payload_sha256") for event in capture_trace]
    expected_render_hashes = [sha256_bytes(block) for block in render_blocks]
    expected_capture_hashes = [sha256_bytes(block) for block in blocks]
    if render_hashes != expected_render_hashes or capture_hashes != expected_capture_hashes:
        raise EvidenceFailure(f"device callback trace mismatch: render={render_hashes}, capture={capture_hashes}")
    if len(render_trace) != DEVICE_CALLBACKS or len(capture_trace) != DEVICE_CALLBACKS:
        raise EvidenceFailure(f"device callback trace count mismatch: render={len(render_trace)}, capture={len(capture_trace)}")
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / "snapshot.json").write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8")
    (output_dir / "rendered.pcm").write_bytes(rendered_pcm)
    (output_dir / "captured.pcm").write_bytes(captured_pcm)
    report = {
        "classification": "simulated callback; physical/acoustic hardware evidence is out of scope",
        "endpoint": endpoint, "callbacks": DEVICE_CALLBACKS, "render_taps": len(render_trace), "capture_taps": len(capture_trace),
        "rendered_bytes": len(rendered_pcm), "rendered_sha256": sha256_bytes(rendered_pcm),
        "captured_bytes": len(captured_pcm), "captured_sha256": sha256_bytes(captured_pcm),
        "render_hashes": render_hashes, "capture_hashes": capture_hashes,
        "playback": snapshot["playback"], "capture": snapshot["capture"], "trace": trace,
        "snapshot": str(output_dir / "snapshot.json"), "rendered_pcm": str(output_dir / "rendered.pcm"), "captured_pcm": str(output_dir / "captured.pcm"),
    }
    (output_dir / "control.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report


def deterministic_audio_pcm() -> bytes:
    return b"".join(struct.pack("<h", ((index * 911) % 24000) - 12000) for index in range(720))


def write_audio_input(path: Path, pcm: bytes) -> None:
    with wave.open(str(path), "wb") as output:
        output.setnchannels(1)
        output.setsampwidth(2)
        output.setframerate(24000)
        output.writeframes(pcm)


def seal_derived_fixture(capture: dict[str, Any]) -> None:
    coverage = {
        "version": capture["version"], "provider": capture["provider"], "session": capture["session"],
        "records": capture["records"],
    }
    if capture.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = True
    encoded = json.dumps(coverage, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    capture["integrity"] = {
        "algorithm": "sha256", "coverage": "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
        "digest": hashlib.sha256(encoded).hexdigest(),
    }


def derive_audio_fixture(source: Path, target: Path, pcm: bytes) -> None:
    capture = json.loads(source.read_text(encoding="utf-8"))
    append_payload = {"type": "input_audio_buffer.append", "audio": base64.b64encode(pcm).decode("ascii")}
    commit_payload = {"type": "input_audio_buffer.commit"}
    response_payload = {"type": "response.create"}
    derived: list[dict[str, Any]] = []
    replaced = False
    skip_response_create = False
    for record in capture["records"]:
        if not replaced and record["direction"] == "client_to_server" and record["type"] == "conversation.item.create":
            base = {"direction": "client_to_server", "timestamp_ms": record["timestamp_ms"], "payload_type": record["payload_type"]}
            derived.extend([
                dict(base, type="input_audio_buffer.append", payload=append_payload),
                dict(base, type="input_audio_buffer.commit", payload=commit_payload),
                dict(base, type="response.create", payload=response_payload),
            ])
            replaced = True
            skip_response_create = True
            continue
        if skip_response_create and record["direction"] == "client_to_server" and record["type"] == "response.create":
            skip_response_create = False
            continue
        derived.append(record)
    if not replaced or skip_response_create:
        raise EvidenceFailure(f"audio fixture source has no unambiguous opening text turn: {source}")
    for sequence, record in enumerate(derived, 1):
        normalized = {
            "sequence": sequence, "direction": record["direction"], "timestamp_ms": sequence * 10,
            "type": record["type"], "payload_type": record["payload_type"], "payload": record["payload"],
        }
        derived[sequence - 1] = normalized
    capture["records"] = derived
    seal_derived_fixture(capture)
    target.write_text(json.dumps(capture, indent=2) + "\n", encoding="utf-8")


def replay_argv(
    artifact: Path,
    fixture: Path,
    case_dir: Path,
    audio_input: Path | None = None,
    prompt: str | None = None,
    device_endpoint: str | None = None,
) -> list[str]:
    workdir = case_dir / "workdir"
    (workdir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    argv = [
        str(artifact), "-C", str(case_dir / "config"), "--workdir", str(workdir), "--allow-path", str(case_dir), "session",
        "--replay", str(fixture), "--audio-out", str(case_dir / "rendered.pcm"), "--record-dir", str(case_dir / "bundle"), "--trace-audio",
    ]
    if prompt is not None:
        argv.extend(["--prompt", prompt])
    if device_endpoint is not None:
        argv.extend(["--audio-out-device", "simulated-duplex:output", "--audio-in-device", "simulated-duplex:input", "--audio-device-server", device_endpoint])
    elif audio_input is not None:
        argv.extend(["--audio-in-turn", str(audio_input)])
    return argv


def read_timeline(path: Path) -> list[dict[str, Any]]:
    try:
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid timeline {path}: {error}") from error


def provider_wire_types(events: list[dict[str, Any]]) -> set[str]:
    types: set[str] = set()
    for event in events:
        if event.get("kind") != "runtime" or event.get("runtime_kind") not in {"provider_wire_receive", "provider_wire_send"}:
            continue
        try:
            envelope = json.loads(base64.b64decode(event.get("payload", "")))
            payload = envelope.get("payload", {})
            if isinstance(payload, dict) and isinstance(payload.get("type"), str):
                types.add(payload["type"])
        except (ValueError, TypeError, json.JSONDecodeError):
            continue
    return types


def run_service_boundary_probe(name: str, case_dir: Path, source_timeline: Path, deadline: float) -> dict[str, Any]:
    if time.monotonic() >= deadline:
        raise EvidenceFailure(f"aggregate deadline exceeded before {name} service boundary probe")
    output_root = case_dir / "service-boundary"
    result = run_child(
        f"{name}-service-boundary",
        ["go", "run", "./traceprobe", "--case", name, "--output", str(output_root)],
        HERE / "external-consumer",
        case_dir / "service-boundary-process",
        min(30.0, deadline - time.monotonic()),
    )
    require_clean(result)
    bundle = output_root / "bundle"
    timeline = bundle / "audio-trace/timeline.jsonl"
    for path in (
        bundle / "audio-trace/microphone-pre-gate.wav",
        bundle / "audio-trace/microphone-uploaded.wav",
        bundle / "audio-trace/speaker-enqueued.wav",
        bundle / "audio-trace/speaker-rendered.wav",
        timeline,
    ):
        if not path.is_file():
            raise EvidenceFailure(f"{name} service boundary probe missing artifact: {path}")
    events = read_timeline(timeline)
    taps = {event.get("tap") for event in events if event.get("kind") == "audio"}
    runtimes = [event for event in events if event.get("kind") == "runtime"]
    runtime_kinds = {event.get("runtime_kind") for event in runtimes}
    if not SERVICE_BOUNDARY_TAPS.issubset(taps) or not {"provider_wire_send", "provider_wire_receive", "terminal"}.issubset(runtime_kinds):
        raise EvidenceFailure(
            f"{name} service boundary probe is incomplete: taps={sorted(taps)}, runtime_kinds={sorted(runtime_kinds)}"
        )
    if "c112-traceprobe-secret" in timeline.read_text(encoding="utf-8"):
        raise EvidenceFailure(f"{name} service boundary probe leaked a credential")
    return {
        "process": result,
        "source_yui_timeline": str(source_timeline),
        "bundle": str(bundle),
        "timeline": str(timeline),
        "timeline_events": len(events),
        "runtime_events": len(runtimes),
        "audio_taps": sorted(taps),
        "runtime_kinds": sorted(runtime_kinds),
    }


def build_device_server(case_dir: Path, deadline: float) -> tuple[Path, dict[str, Any]]:
    if time.monotonic() >= deadline:
        raise EvidenceFailure("aggregate deadline exceeded before audio-device-server build")
    binary = case_dir / "audio-device-server"
    result = run_child(
        "audio-device-server-build",
        ["go", "build", "-o", str(binary), "./cmd/audio-device-server"],
        ROOT / "agent-cli",
        case_dir / "device-server-build",
        min(60.0, deadline - time.monotonic()),
    )
    require_clean(result)
    return binary, result


def run_case(name: str, artifact: Path, run_dir: Path, timeout: float, deadline: float) -> dict[str, Any]:
    if time.monotonic() >= deadline:
        raise EvidenceFailure(f"aggregate deadline exceeded before {name}")
    expected = CASES[name]
    source_fixture = expected["fixture"]
    if not source_fixture.is_file() or sha256_file(source_fixture) != expected["fixture_sha256"]:
        raise EvidenceFailure(f"fixture identity changed: {source_fixture}")
    case_dir = run_dir / name
    fixture = source_fixture
    audio_input = None
    audio_pcm = b""
    device_info: dict[str, Any] | None = None
    device_build: dict[str, Any] | None = None
    device_server: dict[str, Any] | None = None
    device_server_stop: dict[str, Any] | None = None
    device_control: dict[str, Any] | None = None
    if expected.get("audio_input"):
        audio_pcm = deterministic_audio_pcm()
        audio_input = case_dir / "input-turn.wav"
        audio_input.parent.mkdir(parents=True, exist_ok=True)
        write_audio_input(audio_input, audio_pcm)
        if sha256_file(audio_input) != expected["audio_input_sha256"]:
            raise EvidenceFailure(f"deterministic audio input changed: {audio_input}")
        fixture = case_dir / "audio-replay.session.json"
        derive_audio_fixture(source_fixture, fixture, audio_pcm)
        if sha256_file(fixture) != expected["derived_fixture_sha256"]:
            raise EvidenceFailure(f"derived replay fixture changed: {fixture}")
        device_fixture = case_dir / "audio-device-replay.session.json"
        device_info = build_device_fixture(source_fixture, device_fixture, audio_pcm)
        if device_info["fixture_sha256"] != "f455ab43d2893d58656fa3f6691117d446d2b8f39e653161bb0b4a24bd318b01":
            raise EvidenceFailure(f"device fixture changed: {device_info['fixture_sha256']}")
        device_binary, device_build = build_device_server(case_dir, deadline)
        device_server = start_device_server(device_binary, case_dir / "device-server", deadline)

        def controller(process: subprocess.Popen[bytes]) -> None:
            nonlocal device_control
            assert device_info is not None and device_server is not None
            device_control = control_device_replay(process, device_server["endpoint"], device_info, case_dir / "device", deadline)

        replay_args = replay_argv(
            artifact, fixture, case_dir, device_endpoint=device_server["endpoint"],
        )
    else:
        controller = None
        replay_args = replay_argv(artifact, fixture, case_dir, audio_input)
    try:
        result = run_child(
            name, replay_args, case_dir / "workdir", case_dir / "process", min(timeout, deadline - time.monotonic()), controller,
        )
    finally:
        if device_server is not None:
            device_server_stop = stop_device_server(device_server)
    require_clean(result)
    combined = result["stdout"] + "\n" + result["stderr"]
    if "replay mismatch" in combined.lower() or not any(marker in combined for marker in ("[session closed:", "[session replay complete]")):
        raise EvidenceFailure(f"{name} did not report a completed replay")
    if name == "trace-audio-tool-replay" and ("PROBE_TOOL_MARKER_9182" not in combined or "strict replay continuation" not in combined):
        raise EvidenceFailure("tool replay lost the credential-free tool continuation")
    marker = case_dir / "workdir/evidence/runs/exec-invocations-v4.log"
    if name == "trace-audio-tool-replay" and (not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8")):
        raise EvidenceFailure("tool replay did not preserve its tool side effect")
    bundle = case_dir / "bundle"
    manifest = bundle / "manifest.json"
    provider = bundle / "audio/out-000.pcm"
    rendered = case_dir / "rendered.pcm"
    timeline = bundle / "audio-trace/timeline.jsonl"
    speaker = bundle / "audio-trace/speaker-enqueued.wav"
    for path in (manifest, provider, rendered, timeline, speaker):
        if not path.is_file():
            raise EvidenceFailure(f"{name} missing finalized artifact: {path}")
    if provider.stat().st_size != expected["provider_bytes"] or sha256_file(provider) != expected["provider_sha256"]:
        raise EvidenceFailure(f"{name} provider PCM oracle changed")
    rendered_bytes = expected["provider_bytes"] if device_info is not None else expected["rendered_bytes"]
    rendered_sha256 = expected["provider_sha256"] if device_info is not None else expected["rendered_sha256"]
    if rendered.stat().st_size != rendered_bytes or sha256_file(rendered) != rendered_sha256:
        raise EvidenceFailure(f"{name} rendered PCM oracle changed")
    events = read_timeline(timeline)
    taps = {event.get("tap") for event in events if event.get("kind") == "audio"}
    runtimes = [event for event in events if event.get("kind") == "runtime"]
    runtime_kinds = {event.get("runtime_kind") for event in runtimes}
    wire_types = provider_wire_types(events)
    required_wire_types = expected["required_provider_wire_types"]
    required_yui_taps = expected["required_taps"]
    if device_info is not None:
        required_yui_taps = required_yui_taps - {"microphone_pre_gate"}
    if (
        not required_yui_taps.issubset(taps)
        or not expected["required_runtime_kinds"].issubset(runtime_kinds)
        or not required_wire_types.issubset(wire_types)
        or len(runtimes) < expected["runtime_min"]
    ):
        raise EvidenceFailure(
            f"{name} trace did not retain its required audio edges and runtime evidence: "
            f"taps={sorted(taps)}, runtime_kinds={sorted(runtime_kinds)}, wire_types={sorted(wire_types)}"
        )
    source_audio_taps = set(taps)
    tap_evidence: dict[str, Any] = {tap: {"source": "shipped YUI timeline", "simulated": False} for tap in sorted(taps)}
    wire_audio: list[bytes] = []
    if device_info is not None:
        wire_audio = provider_wire_audio(events, "input_audio_buffer.append")
        expected_wire_hashes = [sha256_bytes(audio_pcm)]
        actual_wire_hashes = [sha256_bytes(block) for block in wire_audio]
        if actual_wire_hashes != expected_wire_hashes:
            raise EvidenceFailure(
                f"{name} shipped provider-wire audio does not match the device fixture: actual={actual_wire_hashes}, expected={expected_wire_hashes}"
            )
        uploaded_pcm = b"".join(wire_audio)
        (case_dir / "device/provider-wire-uploaded.pcm").write_bytes(uploaded_pcm)
        source_audio_taps.update({"microphone_pre_gate", "microphone_uploaded", "speaker_rendered"})
        tap_evidence.update({
            "microphone_pre_gate": {"source": "loopback audio-device-server capture callback correlated with the shipped YUI process", "simulated": True},
            "microphone_uploaded": {"source": "shipped YUI provider_wire_send input_audio_buffer.append payloads", "simulated": False},
            "speaker_enqueued": {"source": "shipped YUI speaker_enqueued", "simulated": False},
            "speaker_rendered": {"source": "loopback audio-device-server render callback", "simulated": True},
        })
        if device_control is None:
            raise EvidenceFailure(f"{name} has no controlled device callback report")
        device_control["provider_wire_uploaded_bytes"] = len(uploaded_pcm)
        device_control["provider_wire_uploaded_sha256"] = sha256_bytes(uploaded_pcm)
        device_control["provider_wire_uploaded_block_hashes"] = actual_wire_hashes
        (case_dir / "device/control.json").write_text(json.dumps(device_control, indent=2) + "\n", encoding="utf-8")
    if device_info is not None and not SERVICE_BOUNDARY_TAPS.issubset(source_audio_taps):
        raise EvidenceFailure(f"{name} shipped replay lacks correlated C112 audio boundaries: {sorted(source_audio_taps)}")
    service_boundary = run_service_boundary_probe(name, case_dir, timeline, deadline)
    try:
        manifest_value = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid manifest {manifest}: {error}") from error
    if not manifest_value.get("artifacts"):
        raise EvidenceFailure(f"{name} manifest has no artifacts")
    return {
        "case": name, "source_fixture": str(source_fixture), "source_fixture_sha256": expected["fixture_sha256"],
        "fixture": str(fixture), "fixture_sha256": sha256_file(fixture), "process": result,
        "manifest": str(manifest), "manifest_sha256": sha256_file(manifest), "timeline": str(timeline),
        "timeline_events": len(events), "runtime_events": len(runtimes), "provider_bytes": provider.stat().st_size,
        "provider_sha256": sha256_file(provider), "rendered_bytes": rendered.stat().st_size, "rendered_sha256": sha256_file(rendered),
        "speaker_trace_bytes": speaker.stat().st_size, "speaker_trace_sha256": sha256_file(speaker), "source_audio_taps": sorted(taps),
        "audio_taps": sorted(source_audio_taps), "tap_evidence": tap_evidence,
        "runtime_kinds": sorted(runtime_kinds), "provider_wire_types": sorted(wire_types), "audio_input_bytes": len(audio_pcm),
        "audio_input_sha256": sha256_file(audio_input) if audio_input is not None else None,
        "scheduled_fixture_sha256": sha256_file(case_dir / "audio-replay.session.json") if expected.get("audio_input") else None,
        "device_callback_plan_sha256": device_info["fixture_sha256"] if device_info is not None else None,
        "device_build": device_build, "device_control": device_control, "device_server": device_server_stop,
        "provider_wire_audio_hashes": [sha256_bytes(block) for block in wire_audio],
        "causal_service_boundary_control": service_boundary,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", action="append", choices=tuple(CASES), dest="cases")
    parser.add_argument("--artifact", type=Path, default=HERE / "artifacts/yui")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=180.0)
    args = parser.parse_args()
    cases = args.cases or list(CASES)
    started = time.monotonic()
    run_dir = HERE / "runs" / f"vertical-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    try:
        artifact = args.artifact.expanduser().resolve()
        if not artifact.is_file():
            raise EvidenceFailure(f"source-pinned yui artifact is unavailable: {artifact}")
        if subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip() != str(ROOT):
            raise EvidenceFailure(f"probe source root is not the admitted checkout: {ROOT}")
        deadline = started + args.aggregate_timeout
        reports = [run_case(case, artifact, run_dir, args.child_timeout, deadline) for case in cases]
        outcome = {"status": "pass", "decision": "C112_REPLAY_PASS", "source_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(), "artifact": str(artifact), "artifact_sha256": sha256_file(artifact), "cases": reports, "elapsed_seconds": round(time.monotonic() - started, 6), "aggregate_timeout_seconds": args.aggregate_timeout, "physical_acoustic_claim": "OUT_OF_SCOPE"}
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        outcome = {"status": "failed", "decision": "C112_REPLAY_FAIL", "error": str(error), "run_dir": str(run_dir)}
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
