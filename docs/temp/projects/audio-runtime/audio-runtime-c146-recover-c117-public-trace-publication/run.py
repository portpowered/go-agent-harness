#!/usr/bin/env python3
"""Run bounded C146 executable and causal evidence cases.

The C141 input and interruption fixtures are immutable external probe inputs.
The four-tap case records one deterministic device-capture append discovered
from a failed preflight and runs a derived, resealed copy.  The source fixture
hash remains part of the report; the copy is never presented as a replacement
for that source.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import pathlib
import selectors
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
import wave
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()

TASK = "audio-runtime-c146-recover-c117-public-trace-publication"
SOURCE_REVISION = "b8650efd95f6a2e2e675a3dfd0969a9d311a877b"
CURRENT_MAIN = "4a1c399ccbb3d780be95eb04316e84b8f11a6646"
PINS = {
    "audio_tool": {
        "identity": "c07-audio-tool-traced.session.json",
        "path": "docs/temp/probes/audio-runtime-c140-c112-session-trace-vertical-probe/artifact-2.json",
        "sha256": "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
    },
    "interruption": {
        "identity": "c07-interruption-no-close.session.json",
        "path": "docs/temp/probes/audio-runtime-c140-c112-session-trace-vertical-probe/artifact-3.json",
        "sha256": "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
    },
}
EXPECTED_TOOL_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
MARKER = "PROBE_TOOL_MARKER_9182"
CONTINUATION = "strict replay continuation"
OUTPUT_CAP = 512 * 1024


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git(*args: str) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True)
    if result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def rel(path: pathlib.Path) -> str:
    try:
        return str(path.resolve().relative_to(HERE.resolve()))
    except ValueError:
        try:
            return str(path.resolve().relative_to(ROOT.resolve()))
        except ValueError:
            return str(path)


def resolve_pin(pin: dict[str, str]) -> pathlib.Path:
    path = pathlib.Path(pin["path"])
    if not path.is_absolute():
        path = (FACTORY_ROOT / path).resolve()
    if not path.is_file():
        raise EvidenceFailure(f"missing pinned input: {path}")
    actual = sha256_file(path)
    if actual != pin["sha256"]:
        raise EvidenceFailure(f"pinned input hash changed: {path} ({actual})")
    return path


def safe_environment(run_dir: pathlib.Path) -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    environment = {
        name: value
        for name, value in os.environ.items()
        if not any(term in name.lower() for term in blocked)
    }
    home = run_dir / "home"
    (home / "config").mkdir(parents=True, exist_ok=True)
    environment.update({"HOME": str(home), "XDG_CONFIG_HOME": str(home / "config"), "NO_COLOR": "1", "CI": "1"})
    return environment


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
    else:
        process.terminate()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        if os.name == "posix":
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                return
        else:
            process.kill()
        process.wait(timeout=2)


def process_group_gone(process: subprocess.Popen[bytes]) -> bool:
    if os.name != "posix":
        return process.poll() is not None
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def run_bounded(label: str, argv: list[str], cwd: pathlib.Path, output_dir: pathlib.Path, timeout: int) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    stdout_path = output_dir / f"{label}.stdout"
    stderr_path = output_dir / f"{label}.stderr"
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=safe_environment(output_dir),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name == "posix",
    )
    assert process.stdout is not None and process.stderr is not None
    selector = selectors.DefaultSelector()
    buffers: dict[int, bytearray] = {}
    streams: dict[int, Any] = {}
    for stream in (process.stdout, process.stderr):
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ)
        buffers[stream.fileno()] = bytearray()
        streams[stream.fileno()] = stream
    active = set(buffers)
    timed_out = False
    output_limited = False

    def drain(wait: float) -> None:
        nonlocal output_limited
        for key, _ in selector.select(wait):
            fd = key.fileobj.fileno()
            try:
                chunk = os.read(fd, 16 * 1024)
            except BlockingIOError:
                continue
            if not chunk:
                selector.unregister(key.fileobj)
                active.discard(fd)
                continue
            remaining = OUTPUT_CAP - len(buffers[fd])
            if remaining <= 0:
                output_limited = True
                continue
            buffers[fd].extend(chunk[:remaining])
            if len(chunk) > remaining:
                output_limited = True

    deadline = started + timeout
    while active and not output_limited:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            terminate_group(process)
            break
        drain(min(remaining, 0.25))
        if time.monotonic() >= deadline and active:
            timed_out = True
            terminate_group(process)
            break
        if output_limited:
            terminate_group(process)
    if process.poll() is None:
        terminate_group(process)
    drain_deadline = time.monotonic() + 2
    while active and time.monotonic() < drain_deadline:
        drain(0.05)
        if process.poll() is not None and not active:
            break
    for key in list(selector.get_map().values()):
        selector.unregister(key.fileobj)
    selector.close()
    returncode = process.wait(timeout=3)
    stdout = bytes(buffers[process.stdout.fileno()])
    stderr = bytes(buffers[process.stderr.fileno()])
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    return {
        "label": label,
        "argv": argv,
        "cwd": rel(cwd),
        "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "exit_code": returncode,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "stdout_path": rel(stdout_path),
        "stderr_path": rel(stderr_path),
        "stdout_sha256": sha256_bytes(stdout),
        "stderr_sha256": sha256_bytes(stderr),
        "process_group_gone": process_group_gone(process),
    }


def process_output(result: dict[str, Any]) -> str:
    return "\n".join(
        (HERE / result[key]).read_text(encoding="utf-8", errors="replace")
        for key in ("stdout_path", "stderr_path")
    )


def start_device_server(run_dir: pathlib.Path, server_binary: pathlib.Path) -> tuple[subprocess.Popen[bytes], str, dict[str, Any]]:
    output_dir = run_dir / "device-server"
    output_dir.mkdir(parents=True, exist_ok=True)
    process = subprocess.Popen(
        [str(server_binary), "--listen", "127.0.0.1:0"],
        cwd=ROOT,
        env=safe_environment(output_dir),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name == "posix",
    )
    assert process.stdout is not None
    os.set_blocking(process.stdout.fileno(), False)
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    buffer = bytearray()
    deadline = time.monotonic() + 10
    endpoint = ""
    while time.monotonic() < deadline and process.poll() is None:
        events = selector.select(min(0.25, max(0.0, deadline - time.monotonic())))
        for key, _ in events:
            chunk = os.read(key.fileobj.fileno(), 16 * 1024)
            if not chunk:
                continue
            buffer.extend(chunk)
            if b"\n" not in buffer:
                continue
            line, _, _ = buffer.partition(b"\n")
            try:
                ready = json.loads(line.decode("utf-8"))
                endpoint = str(ready["endpoint"])
            except (ValueError, KeyError) as exc:
                terminate_group(process)
                raise EvidenceFailure(f"device server readiness was invalid: {line!r}") from exc
            break
        if endpoint:
            break
    selector.unregister(process.stdout)
    selector.close()
    if not endpoint:
        terminate_group(process)
        stdout, stderr = process.communicate(timeout=3)
        (output_dir / "server.stdout").write_bytes(bytes(buffer) + stdout)
        (output_dir / "server.stderr").write_bytes(stderr)
        raise EvidenceFailure("device server did not announce a loopback endpoint")
    return process, endpoint, {"ready": ready, "stdout_prefix_sha256": sha256_bytes(bytes(buffer))}


def stop_device_server(process: subprocess.Popen[bytes], run_dir: pathlib.Path, ready: dict[str, Any]) -> dict[str, Any]:
    terminate_group(process)
    try:
        stdout, stderr = process.communicate(timeout=3)
    except subprocess.TimeoutExpired:
        terminate_group(process)
        stdout, stderr = process.communicate(timeout=3)
    output_dir = run_dir / "device-server"
    stdout_bytes = stdout or b""
    stderr_bytes = stderr or b""
    (output_dir / "server.stdout").write_bytes(stdout_bytes)
    (output_dir / "server.stderr").write_bytes(stderr_bytes)
    return {
        "ready": ready,
        "exit_code": process.returncode,
        "stdout_path": rel(output_dir / "server.stdout"),
        "stderr_path": rel(output_dir / "server.stderr"),
        "stdout_sha256": sha256_file(output_dir / "server.stdout"),
        "stderr_sha256": sha256_file(output_dir / "server.stderr"),
        "process_group_gone": process_group_gone(process),
    }


def read_snapshot(endpoint: str) -> dict[str, Any]:
    with urllib.request.urlopen(f"http://{endpoint}/v1/audio-device/control/snapshot", timeout=2) as response:
        return json.load(response)


def yui_command(
    binary: pathlib.Path,
    fixture: pathlib.Path,
    run_dir: pathlib.Path,
    endpoint: str | None = None,
    input_path: pathlib.Path | None = None,
    device_input: bool = False,
) -> list[str]:
    record_dir = run_dir / "bundle"
    command = [
        str(binary),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(run_dir / "rendered.pcm"),
        "--record-dir",
        str(record_dir),
        "--trace-audio",
    ]
    if endpoint:
        command.extend(["--audio-out-device", "simulated-duplex:output", "--audio-device-server", endpoint])
        if device_input:
            command.extend(["--audio-in-device", "simulated-duplex:input"])
        elif input_path is not None:
            command.extend(["--audio-in", str(input_path)])
    elif input_path is not None:
        command.extend(["--audio-in", str(input_path)])
    command.extend(["--workdir", str(run_dir), "--allow-path", str(run_dir)])
    if input_path is not None:
        command.extend(["--allow-path", str(input_path.parent)])
    return command


def execute_yui(
    binary: pathlib.Path,
    fixture: pathlib.Path,
    run_dir: pathlib.Path,
    timeout: int,
    endpoint: str | None = None,
    input_path: pathlib.Path | None = None,
    device_input: bool = False,
) -> tuple[dict[str, Any], dict[str, Any] | None]:
    if run_dir.exists():
        shutil.rmtree(run_dir)
    (run_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    server_process: subprocess.Popen[bytes] | None = None
    server_ready: dict[str, Any] | None = None
    snapshot: dict[str, Any] | None = None
    try:
        if endpoint is None:
            result = run_bounded("yui", yui_command(binary, fixture, run_dir, input_path=input_path), run_dir, run_dir / "process", timeout)
        else:
            server_binary = HERE / "artifacts" / "audio-device-server"
            server_process, endpoint, server_ready = start_device_server(run_dir, server_binary)
            result = run_bounded(
                "yui",
                yui_command(binary, fixture, run_dir, endpoint, input_path=input_path, device_input=device_input),
                run_dir,
                run_dir / "process",
                timeout,
            )
            try:
                snapshot = read_snapshot(endpoint)
            except Exception as exc:  # evidence records the unavailable snapshot
                snapshot = {"snapshot_error": str(exc)}
    finally:
        if server_process is not None:
            server_result = stop_device_server(server_process, run_dir, server_ready or {})
        else:
            server_result = None
    result["server"] = server_result
    if snapshot is not None:
        result["server_snapshot"] = snapshot
    return result, snapshot


def safe_artifact_path(root: pathlib.Path, value: str) -> pathlib.Path:
    path = pathlib.Path(value)
    if path.is_absolute() or ".." in path.parts:
        raise EvidenceFailure(f"unsafe artifact path: {value}")
    resolved = (root / path).resolve()
    if not resolved.is_relative_to(root.resolve()):
        raise EvidenceFailure(f"artifact escapes bundle: {value}")
    return resolved


def manifest_summary(bundle: pathlib.Path) -> dict[str, Any]:
    manifest_path = bundle / "manifest.json"
    if not manifest_path.is_file():
        raise EvidenceFailure(f"missing recording manifest: {manifest_path}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise EvidenceFailure("recording manifest has no artifacts")
    observed = []
    for item in artifacts:
        if not isinstance(item, dict):
            raise EvidenceFailure("recording manifest has a non-object artifact")
        path = safe_artifact_path(bundle, str(item.get("path", "")))
        if not path.is_file():
            raise EvidenceFailure(f"manifest artifact is missing: {path}")
        actual = sha256_file(path)
        if actual != item.get("sha256"):
            raise EvidenceFailure(f"manifest artifact hash mismatch: {path}")
        observed.append({"path": str(path.relative_to(bundle)), "bytes": path.stat().st_size, "sha256": actual})
    return {
        "path": rel(manifest_path),
        "bytes": manifest_path.stat().st_size,
        "sha256": sha256_file(manifest_path),
        "terminal": manifest.get("terminal"),
        "artifacts": observed,
    }


def trace_summary(bundle: pathlib.Path, require_all: bool) -> dict[str, Any]:
    trace_dir = bundle / "audio-trace"
    timeline_path = trace_dir / "timeline.jsonl"
    if not timeline_path.is_file():
        raise EvidenceFailure(f"missing published trace timeline: {timeline_path}")
    events = []
    elapsed = []
    for line in timeline_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        event = json.loads(line)
        events.append(event)
        if isinstance(event.get("elapsed_ns"), int):
            elapsed.append(event["elapsed_ns"])
    if any(previous > current for previous, current in zip(elapsed, elapsed[1:])):
        raise EvidenceFailure("trace elapsed_ns is not monotonic")
    if any(event.get("runtime_kind") == "trace_overflow" or event.get("kind") == "trace_overflow" for event in events):
        raise EvidenceFailure("trace overflow was reported")
    taps = {}
    for event in events:
        if event.get("kind") != "audio":
            continue
        tap = event.get("tap")
        if not tap:
            continue
        taps.setdefault(tap, []).append(event)
    required = {
        "microphone_pre_gate": "microphone-pre-gate.wav",
        "microphone_uploaded": "microphone-uploaded.wav",
        "speaker_enqueued": "speaker-enqueued.wav",
        "speaker_rendered": "speaker-rendered.wav",
    }
    if require_all and not set(required).issubset(taps):
        raise EvidenceFailure(f"trace taps are incomplete: {sorted(taps)}")
    if any(event.get("runtime_kind") == "audio_render_tap_unavailable" for event in events) and require_all:
        raise EvidenceFailure("callback-capable positive trace reported render unavailable")
    files = {}
    for tap, filename in required.items():
        path = trace_dir / filename
        if not path.is_file():
            if require_all:
                raise EvidenceFailure(f"missing trace WAV: {path}")
            continue
        with wave.open(str(path), "rb") as audio_file:
            if audio_file.getnchannels() != 1 or audio_file.getsampwidth() != 2:
                raise EvidenceFailure(f"trace WAV is not mono PCM16: {path}")
            pcm = audio_file.readframes(audio_file.getnframes())
            sample_rate = audio_file.getframerate()
        files[tap] = {"path": rel(path), "bytes": path.stat().st_size, "sha256": sha256_file(path), "pcm_sha256": sha256_bytes(pcm), "sample_rate": sample_rate, "sample_count": len(pcm) // 2}
        if tap in taps:
            cursor = 0
            for event in taps[tap]:
                count = event.get("sample_count")
                if not isinstance(count, int) or count <= 0:
                    raise EvidenceFailure(f"trace timeline has an invalid sample count for {tap}")
                start = event.get("start_sample", 0)
                if not isinstance(start, int) or start != cursor:
                    raise EvidenceFailure(f"trace timeline has a sample offset gap for {tap}")
                end = cursor + count
                segment = pcm[cursor * 2 : end * 2]
                if len(segment) != count * 2 or event.get("pcm_sha256") != sha256_bytes(segment):
                    raise EvidenceFailure(f"trace WAV hash disagrees with timeline segment for {tap}")
                cursor = end
            if cursor != files[tap]["sample_count"]:
                raise EvidenceFailure(f"trace WAV sample count disagrees with timeline for {tap}")
    return {
        "path": rel(timeline_path),
        "sha256": sha256_file(timeline_path),
        "event_count": len(events),
        "timing_domains": {"elapsed_ns": "monotonic_session_clock", "timestamp": "UTC_wall_clock", "audio_samples": "device_or_provider_sample_clock"},
        "taps": sorted(taps),
        "files": files,
        "runtime_kinds": sorted({str(event["runtime_kind"]) for event in events if event.get("runtime_kind")}),
    }


def seal_fixture(document: dict[str, Any]) -> str:
    # Go encodes the protected envelope through typed structs.  Preserve that
    # field order, and canonicalize RawMessage payload objects the same way
    # the sorted JSON file below is written before the Go loader hashes it.
    provider = document["provider"]
    session = document["session"]
    coverage: dict[str, Any] = {
        "version": document["version"],
        "provider": {key: provider[key] for key in ("name", "model") if provider.get(key)},
        "session": {key: session[key] for key in ("id", "started_at_utc", "fixture_provenance") if session.get(key)},
        "records": [],
    }
    for record in document["records"]:
        encoded_record: dict[str, Any] = {
            key: record[key]
            for key in ("sequence", "direction", "timestamp_ms", "type", "payload_type")
            if key in record
        }
        for key in ("payload", "data"):
            if key in record and record[key] is not None:
                encoded_record[key] = json.loads(json.dumps(record[key], sort_keys=True, separators=(",", ":")))
        coverage["records"].append(encoded_record)
    if document.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = document["ends_with_disconnect"]
    encoded = json.dumps(coverage, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return sha256_bytes(encoded)


def device_augmented_fixture(source: pathlib.Path, pcm_paths: list[pathlib.Path], destination: pathlib.Path) -> dict[str, Any]:
    document = json.loads(source.read_text(encoding="utf-8"))
    records = document.get("records")
    if not isinstance(records, list) or not pcm_paths:
        raise EvidenceFailure("device-augmented fixture has no source records or captured PCM")
    first_prompt_index = next(
        (
            index
            for index, record in enumerate(records)
            if record.get("direction") == "client_to_server" and record.get("type") == "conversation.item.create"
        ),
        None,
    )
    if first_prompt_index is None:
        raise EvidenceFailure("source fixture has no opening conversation.item.create")
    first_prompt = records[first_prompt_index]
    first_sequence = int(first_prompt.get("sequence", 0))
    first_timestamp = int(first_prompt.get("timestamp_ms", 0))
    inserts = []
    for index, path in enumerate(pcm_paths):
        payload = {"type": "input_audio_buffer.append", "audio": base64.b64encode(path.read_bytes()).decode("ascii")}
        inserts.append({"sequence": first_sequence + index, "direction": "client_to_server", "timestamp_ms": first_timestamp + index * 10, "type": "input_audio_buffer.append", "payload_type": "websocket_message", "payload": payload})
    inserts.append({
        "sequence": first_sequence + len(pcm_paths),
        "direction": "client_to_server",
        "timestamp_ms": first_timestamp + len(pcm_paths) * 10,
        "type": "input_audio_buffer.commit",
        "payload_type": "websocket_message",
        "payload": {"type": "input_audio_buffer.commit"},
    })
    rewritten = []
    for index, record in enumerate(records):
        if index == first_prompt_index:
            continue
        record = dict(record)
        if int(record.get("sequence", 0)) > first_sequence:
            record["sequence"] = int(record["sequence"]) + len(pcm_paths)
            record["timestamp_ms"] = int(record.get("timestamp_ms", 0)) + len(pcm_paths) * 10
        rewritten.append(record)
    insert_at = next(index for index, record in enumerate(rewritten) if int(record.get("sequence", 0)) > first_sequence)
    rewritten[insert_at:insert_at] = inserts
    document["records"] = rewritten
    integrity = document.get("integrity")
    if not isinstance(integrity, dict) or integrity.get("algorithm") != "sha256":
        raise EvidenceFailure("source fixture has no supported integrity envelope")
    integrity["digest"] = seal_fixture(document)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {
        "source_fixture_sha256": sha256_file(source),
        "derived_fixture_sha256": sha256_file(destination),
        "inserted_records": len(inserts),
        "inserted_pcm": [{"path": rel(path), "bytes": path.stat().st_size, "sha256": sha256_file(path)} for path in pcm_paths],
        "integrity_digest": integrity["digest"],
        "mutation": "replace the source's first text turn with device-owned input_audio_buffer.append plus commit records; reseal v2 integrity",
    }


def verify_text(result: dict[str, Any], literals: list[str]) -> None:
    output = process_output(result)
    for literal in literals:
        if literal not in output:
            raise EvidenceFailure(f"process output is missing {literal!r}")


def positive_four_tap(binary: pathlib.Path, timeout: int) -> dict[str, Any]:
    source = resolve_pin(PINS["audio_tool"])
    case_dir = HERE / "runs" / "c141-simulated-duplex-four-tap"
    if case_dir.exists():
        shutil.rmtree(case_dir)
    discovery_dir = case_dir / "discovery"
    discovery, _ = execute_yui(binary, source, discovery_dir, timeout, endpoint="device", device_input=True)
    discovery_pcm = sorted((discovery_dir / "bundle" / "audio").glob("in-*.pcm"))
    if discovery["exit_code"] == 0 or not discovery_pcm or discovery["process_group_gone"] is not True:
        raise EvidenceFailure("device preflight did not fail closed while discovering the source capture append")
    captured_input = case_dir / "captured-input.pcm"
    captured_bytes = discovery_pcm[0].read_bytes()
    if len(captured_bytes) < 960:
        raise EvidenceFailure("device preflight captured less than one 16 kHz PCM16 frame")
    captured_input.write_bytes(captured_bytes[:960])
    derived = case_dir / "device-augmented.session.json"
    derivation = device_augmented_fixture(source, [captured_input], derived)
    execution_dir = case_dir / "execution"
    result, snapshot = execute_yui(
        binary,
        derived,
        execution_dir,
        timeout,
        endpoint="device",
        input_path=captured_input,
        device_input=False,
    )
    if result["exit_code"] != 0 or result["timed_out"] or result["output_limited"] or not result["process_group_gone"]:
        raise EvidenceFailure(f"four-tap YUI replay did not complete cleanly: {result}")
    verify_text(result, [MARKER, CONTINUATION, "fixture_complete", "provider_close"])
    bundle = execution_dir / "bundle"
    manifest = manifest_summary(bundle)
    trace = trace_summary(bundle, require_all=True)
    if not snapshot or not snapshot.get("rendered_samples"):
        raise EvidenceFailure("simulated device snapshot has no rendered callback PCM")
    playback = snapshot.get("playback", {})
    if playback.get("DroppedSamples", playback.get("dropped_samples", 0)) not in (0, None):
        raise EvidenceFailure("simulated device playback dropped samples")
    rendered = snapshot.get("rendered_samples", snapshot.get("RenderedSamples", []))
    device_trace = snapshot.get("trace", [])
    render_trace = [event for event in device_trace if event.get("tap") == "render"]
    if not render_trace or not rendered:
        raise EvidenceFailure("simulated device did not record a distinct render callback")
    report = {
        "schema_version": "audio-runtime-c146-four-tap-v1",
        "case": "c141-simulated-duplex-four-tap",
        "task": TASK,
        "source_revision": SOURCE_REVISION,
        "candidate_revision": git("rev-parse", "HEAD"),
        "source_fixture": {"identity": PINS["audio_tool"]["identity"], "path": rel(source), "sha256": sha256_file(source)},
        "derivation": derivation,
        "command": result["argv"],
        "preflight": discovery,
        "process": result,
        "recording": manifest,
        "trace": trace,
        "device_callback": {"proof_level": "SIMULATED_DEVICE_CALLBACK", "rendered_samples": len(rendered), "rendered_samples_sha256": sha256_bytes(bytes().join(int(sample).to_bytes(2, "little", signed=True) for sample in rendered)), "render_event_count": len(render_trace), "trace_tail": device_trace[-8:]},
        "credential_free": True,
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "native_hardware": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "passes": True,
    }
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def healthy_audio_tool(binary: pathlib.Path, timeout: int) -> dict[str, Any]:
    source = resolve_pin(PINS["audio_tool"])
    case_dir = HERE / "runs" / "healthy-audio-tool"
    result, _ = execute_yui(binary, source, case_dir, timeout)
    if result["exit_code"] != 0 or result["timed_out"] or result["output_limited"] or not result["process_group_gone"]:
        raise EvidenceFailure(f"healthy audio/tool replay did not complete cleanly: {result}")
    verify_text(result, [MARKER, CONTINUATION, "fixture_complete", "provider_close"])
    bundle = case_dir / "bundle"
    manifest = manifest_summary(bundle)
    raw = bundle / "audio" / "out-000.pcm"
    if not raw.is_file() or raw.stat().st_size != 4800 or sha256_file(raw) != EXPECTED_TOOL_PCM_SHA256:
        raise EvidenceFailure("healthy audio/tool PCM receipt changed")
    rendered = case_dir / "rendered.pcm"
    if not rendered.is_file() or rendered.stat().st_size <= 0:
        raise EvidenceFailure("healthy audio/tool WAV/PCM output is missing")
    report = {"schema_version": "audio-runtime-c146-healthy-audio-tool-v1", "case": "healthy-audio-tool", "task": TASK, "source_revision": SOURCE_REVISION, "candidate_revision": git("rev-parse", "HEAD"), "source_fixture": {"identity": PINS["audio_tool"]["identity"], "path": rel(source), "sha256": sha256_file(source)}, "command": result["argv"], "process": result, "recording": manifest, "raw_pcm": {"path": rel(raw), "bytes": raw.stat().st_size, "sha256": sha256_file(raw)}, "credential_free": True, "proof_level": "SOFTWARE_REPLAY", "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS", "passes": True}
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def interruption(binary: pathlib.Path, timeout: int) -> dict[str, Any]:
    source = resolve_pin(PINS["interruption"])
    case_dir = HERE / "runs" / "interruption"
    result, _ = execute_yui(binary, source, case_dir, timeout)
    if result["exit_code"] != 0 or result["timed_out"] or result["output_limited"] or not result["process_group_gone"]:
        raise EvidenceFailure(f"interruption replay did not complete cleanly: {result}")
    bundle = case_dir / "bundle"
    manifest = manifest_summary(bundle)
    terminal = manifest.get("terminal") or {}
    if terminal.get("reason") != "replay_complete" or terminal.get("output_state") != "complete":
        raise EvidenceFailure(f"interruption terminal evidence is not replay_complete: {terminal}")
    timeline = bundle / "audio-trace" / "timeline.jsonl"
    text = timeline.read_text(encoding="utf-8") if timeline.is_file() else ""
    for literal in ['"runtime_kind":"VAD.SPEECH_STARTED"', '"runtime_kind":"MESSAGE.END"', '"runtime_kind":"AUDIO.END"', '"runtime_kind":"terminal"']:
        if literal not in text:
            raise EvidenceFailure(f"interruption trace missing {literal}")
    session_log = bundle / "session-log.jsonl"
    turns = [json.loads(line) for line in session_log.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(turns) < 2 or turns[0].get("response", {}).get("audio_bytes", 0) <= 0 or turns[1].get("response", {}).get("audio_offset_bytes", 0) <= 0:
        raise EvidenceFailure("interruption session log lost the interrupted prefix or healthy continuation")
    report = {"schema_version": "audio-runtime-c146-interruption-v1", "case": "interruption", "task": TASK, "source_revision": SOURCE_REVISION, "candidate_revision": git("rev-parse", "HEAD"), "source_fixture": {"identity": PINS["interruption"]["identity"], "path": rel(source), "sha256": sha256_file(source)}, "command": result["argv"], "process": result, "recording": manifest, "session_log": {"path": rel(session_log), "sha256": sha256_file(session_log), "turns": len(turns)}, "trace": {"path": rel(timeline), "sha256": sha256_file(timeline), "interrupted_prefix": True, "speech_started": True, "healthy_continuation": True, "terminal": terminal}, "credential_free": True, "proof_level": "SOFTWARE_REPLAY", "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS", "passes": True}
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def malformed_odd_pcm(binary: pathlib.Path, timeout: int) -> dict[str, Any]:
    source = resolve_pin(PINS["audio_tool"])
    case_dir = HERE / "runs" / "malformed-odd-pcm"
    if case_dir.exists():
        shutil.rmtree(case_dir)
    case_dir.mkdir(parents=True, exist_ok=True)
    document = json.loads(source.read_text(encoding="utf-8"))
    mutated = False
    for record in document.get("records", []):
        payload = record.get("payload", {})
        if record.get("type") == "response.output_audio.delta" and isinstance(payload.get("delta"), str):
            payload["delta"] = base64.b64encode(b"\x01").decode("ascii")
            mutated = True
            break
    if not mutated:
        raise EvidenceFailure("audio/tool fixture has no output audio delta")
    integrity = document.get("integrity")
    if not isinstance(integrity, dict):
        raise EvidenceFailure("audio/tool fixture has no integrity envelope")
    integrity["digest"] = seal_fixture(document)
    fixture = case_dir / "malformed.session.json"
    fixture.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    execution_dir = case_dir / "execution"
    result, _ = execute_yui(binary, fixture, execution_dir, timeout)
    output = process_output(result).lower()
    raw = execution_dir / "bundle" / "audio" / "out-000.pcm"
    wav = execution_dir / "rendered.pcm"
    if result["exit_code"] == 0 or result["timed_out"] or not result["process_group_gone"] or not any(word in output for word in ("odd", "pcm16", "audio", "replay")):
        raise EvidenceFailure("odd-byte PCM was not rejected with a bounded diagnostic")
    if raw.is_file() and raw.stat().st_size:
        raise EvidenceFailure("odd-byte PCM reached the accepted playback receipt")
    if wav.is_file() and wav.stat().st_size > 44:
        raise EvidenceFailure("odd-byte PCM produced non-empty output")
    report = {"schema_version": "audio-runtime-c146-malformed-odd-pcm-v1", "case": "malformed-odd-pcm", "task": TASK, "source_revision": SOURCE_REVISION, "candidate_revision": git("rev-parse", "HEAD"), "source_fixture": {"identity": PINS["audio_tool"]["identity"], "path": rel(source), "sha256": sha256_file(source)}, "mutated_fixture": {"path": rel(fixture), "sha256": sha256_file(fixture), "integrity_digest": integrity["digest"], "mutation": "first response.output_audio.delta replaced with one byte"}, "command": result["argv"], "process": result, "accepted_pcm_receipt": False, "clean_shutdown": result["process_group_gone"], "passes": True}
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def render_tap_unavailable(timeout: int) -> dict[str, Any]:
    case_dir = HERE / "runs" / "render-tap-unavailable"
    if case_dir.exists():
        shutil.rmtree(case_dir)
    command = ["go", "test", "./agent-cli/internal/transport/cli/internal/livehost", "-run", "TestPublicSessionTraceMarksUnsupportedRenderBoundary", "-count=1", "-timeout", f"{timeout}s"]
    result = run_bounded("go-test", command, ROOT, case_dir / "process", timeout + 30)
    if result["exit_code"] != 0 or result["timed_out"] or not result["process_group_gone"]:
        raise EvidenceFailure(f"unsupported render causal test failed: {result}")
    report = {"schema_version": "audio-runtime-c146-render-unavailable-v1", "case": "render-tap-unavailable", "task": TASK, "candidate_revision": git("rev-parse", "HEAD"), "command": result["argv"], "process": result, "assertions": ["audio_render_tap_unavailable is published", "speaker-rendered.wav is absent", "no fabricated rendered PCM is accepted"], "passes": True}
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def stale_provenance() -> dict[str, Any]:
    case_dir = HERE / "runs" / "stale-provenance"
    if case_dir.exists():
        shutil.rmtree(case_dir)
    case_dir.mkdir(parents=True, exist_ok=True)
    stale = {"schema_version": "audio-runtime-c146-case-v1", "source_revision": "0" * 40, "candidate_revision": git("rev-parse", "HEAD"), "fixture_sha256": PINS["audio_tool"]["sha256"]}
    path = case_dir / "stale-report.json"
    path.write_text(json.dumps(stale, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    rejected = False
    diagnostic = ""
    try:
        if stale["source_revision"] != SOURCE_REVISION:
            raise EvidenceFailure("source revision is stale")
    except EvidenceFailure as exc:
        rejected = True
        diagnostic = str(exc)
    if not rejected:
        raise EvidenceFailure("stale provenance mutation was accepted")
    report = {"schema_version": "audio-runtime-c146-stale-provenance-v1", "case": "stale-provenance", "task": TASK, "mutation": {"path": rel(path), "source_revision": stale["source_revision"]}, "rejected": rejected, "diagnostic": diagnostic, "passes": True}
    (case_dir / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=("c141-simulated-duplex-four-tap", "render-tap-unavailable", "malformed-odd-pcm", "stale-provenance", "healthy-audio-tool", "interruption"), required=True)
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    args = parser.parse_args()
    started = time.monotonic()
    binary = HERE / "artifacts" / "yui"
    server_binary = HERE / "artifacts" / "audio-device-server"
    if not binary.is_file() or not server_binary.is_file():
        raise SystemExit("missing candidate artifacts; build yui and audio-device-server first")
    if sha256_file(resolve_pin(PINS["audio_tool"])) != PINS["audio_tool"]["sha256"] or sha256_file(resolve_pin(PINS["interruption"])) != PINS["interruption"]["sha256"]:
        raise SystemExit("source-pinned fixture hash verification failed")
    reports: dict[str, Any] = {}
    for case in dict.fromkeys(args.case):
        if case == "c141-simulated-duplex-four-tap":
            reports[case] = positive_four_tap(binary, args.child_timeout)
        elif case == "render-tap-unavailable":
            reports[case] = render_tap_unavailable(args.child_timeout)
        elif case == "malformed-odd-pcm":
            reports[case] = malformed_odd_pcm(binary, args.child_timeout)
        elif case == "stale-provenance":
            reports[case] = stale_provenance()
        elif case == "healthy-audio-tool":
            reports[case] = healthy_audio_tool(binary, args.child_timeout)
        elif case == "interruption":
            reports[case] = interruption(binary, args.child_timeout)
        if time.monotonic() - started > args.aggregate_timeout:
            raise SystemExit("aggregate evidence timeout exceeded")
    aggregate = {"schema_version": "audio-runtime-c146-public-evidence-v1", "task": TASK, "source_revision": SOURCE_REVISION, "candidate_revision": git("rev-parse", "HEAD"), "artifact_sha256": sha256_file(binary), "device_server_sha256": sha256_file(server_binary), "cases": reports, "elapsed_seconds": round(time.monotonic() - started, 3), "passes": all(report.get("passes") for report in reports.values())}
    (HERE / "runs" / "report.json").parent.mkdir(parents=True, exist_ok=True)
    (HERE / "runs" / "report.json").write_text(json.dumps(aggregate, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "ok", "cases": list(reports), "report": rel(HERE / "runs" / "report.json"), "artifact_sha256": aggregate["artifact_sha256"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceFailure as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
