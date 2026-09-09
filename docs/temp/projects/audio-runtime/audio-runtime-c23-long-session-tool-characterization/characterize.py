#!/usr/bin/env python3
"""Bounded C23 characterization driver for the public audio runtime.

The driver owns admission/provenance/process bounds and comparisons. The Go
consumer owns the causal public-runtime fixture. Every child is isolated,
credential-free, process-group bounded, and writes artifacts only below this
task's owned directory.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from typing import Any


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=OWNED_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
FIXTURE_PATH = OWNED_ROOT / "fixtures.json"
CONSUMER_PATH = OWNED_ROOT / "bin" / "c23-consumer"
YUI_PATH = OWNED_ROOT / "bin" / "yui"
PROVENANCE_PATH = OWNED_ROOT / "provenance.json"
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_FIXTURE_BYTES = 2 * 1024 * 1024
MAX_TRACE_EVENTS = 8192
EXPECTED_TURNS = (16, 64, 128, 256)
EXPECTED_RECORDING_MODES = ("off", "on")
MAIN_REVISION = "5d5afcb14d7b269378020809f5a2418c499ac94d"
SESSION_CAPTURE_INTEGRITY_COVERAGE = "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)"


class VerificationError(RuntimeError):
    """A causal verification or admission failure."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def load_json(path: Path) -> dict[str, Any]:
    require(path.is_file(), f"missing JSON artifact: {path}")
    try:
        value = json.loads(path.read_text())
    except json.JSONDecodeError as error:
        raise VerificationError(f"invalid JSON artifact {path}: {error}") from error
    require(isinstance(value, dict), f"JSON artifact {path} is not an object")
    return value


def write_json(path: Path, value: Any, *, sort_keys: bool = True) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=sort_keys) + "\n")


def relative_owned(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(OWNED_ROOT.resolve()))
    except ValueError as error:
        raise VerificationError(f"artifact escaped owned C23 path: {path}") from error


def relative_repo(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(REPO_ROOT.resolve()))
    except ValueError as error:
        raise VerificationError(f"path escaped repository root: {path}") from error


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def seal_session_capture(capture: dict[str, Any]) -> dict[str, Any]:
    coverage = {
        "version": capture["version"],
        "provider": capture["provider"],
        "session": capture["session"],
        "records": capture["records"],
    }
    if capture.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = True
    digest = sha256_bytes(json.dumps(coverage, ensure_ascii=False, separators=(",", ":")).encode())
    sealed = dict(capture)
    sealed["integrity"] = {
        "algorithm": "sha256",
        "coverage": SESSION_CAPTURE_INTEGRITY_COVERAGE,
        "digest": digest,
    }
    return sealed


def fixture_hash() -> str:
    raw = FIXTURE_PATH.read_bytes()
    require(len(raw) <= MAX_FIXTURE_BYTES, "fixture exceeds the bounded fixture volume")
    value = json.loads(raw)
    canonical = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return sha256_bytes(canonical)


def run_capture(command: list[str], cwd: Path = REPO_ROOT) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(command, cwd=cwd, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout).strip()
        raise VerificationError(f"command failed ({completed.returncode}): {' '.join(command)}: {detail[:2000]}")
    return completed


def git_value(*args: str) -> str:
    return run_capture(["git", *args]).stdout.strip()


def git_is_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(
        ["git", "merge-base", "--is-ancestor", ancestor, descendant],
        cwd=REPO_ROOT,
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    ).returncode == 0


def assert_admission(source_revision: str) -> dict[str, str]:
    head = git_value("rev-parse", "HEAD")
    branch = git_value("branch", "--show-current")
    origin_main = git_value("rev-parse", "origin/main")
    status = git_value("status", "--porcelain", "--untracked-files=all")
    require(not status, f"prepare requires a clean source tree; first dirty entry: {status.splitlines()[0] if status else ''}")
    require(source_revision == origin_main, f"source revision {source_revision} is not the fetched origin/main {origin_main}")
    require(git_is_ancestor(source_revision, head), f"candidate HEAD {head} does not preserve source ancestry {source_revision}")
    require(git_value("diff", "--check") == "", "source tree has whitespace errors")
    return {"head": head, "branch": branch, "origin_main": origin_main, "source_revision": source_revision}


def source_files() -> list[dict[str, Any]]:
    files = run_capture(["git", "ls-files", "--", relative_repo(OWNED_ROOT)]).stdout.splitlines()
    result: list[dict[str, Any]] = []
    for relative in files:
        path = REPO_ROOT / relative
        if path.is_file():
            result.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    return result


def prepare(args: argparse.Namespace) -> dict[str, Any]:
    require(len(args.source_revision) == 40, "--source-revision must be a full SHA")
    admission = assert_admission(args.source_revision)
    require(FIXTURE_PATH.is_file(), "C23 fixture is missing")
    require(CONSUMER_PATH.parent.resolve() == (OWNED_ROOT / "bin").resolve(), "consumer output escaped owned bin")
    require(YUI_PATH.parent.resolve() == (OWNED_ROOT / "bin").resolve(), "yui output escaped owned bin")
    CONSUMER_PATH.parent.mkdir(parents=True, exist_ok=True)
    build_consumer = ["go", "build", "-trimpath", "-o", str(CONSUMER_PATH), str(OWNED_ROOT / "consumer" / "main.go")]
    build_yui = ["go", "build", "-trimpath", "-o", str(YUI_PATH), "./agent-cli/cmd/yui"]
    run_capture(build_consumer)
    run_capture(build_yui)
    go_version = run_capture(["go", "version"]).stdout.strip()
    fixture_sha = fixture_hash()
    provenance: dict[str, Any] = {
        "schema": "audio-runtime.c23.provenance.v1",
        "task": "audio-runtime-c23-long-session-tool-characterization",
        "project": "audio-runtime",
        "branch": admission["branch"],
        "candidate_revision": admission["head"],
        "source_revision": admission["source_revision"],
        "origin_main_at_prepare": admission["origin_main"],
        "ancestry_verified": True,
        "fixture": {"path": relative_owned(FIXTURE_PATH), "sha256": fixture_sha, "max_bytes": MAX_FIXTURE_BYTES},
        "toolchain": {"go_version": go_version, "build_flags": ["-trimpath"]},
        "consumer": {"path": relative_owned(CONSUMER_PATH), "sha256": sha256_file(CONSUMER_PATH), "bytes": CONSUMER_PATH.stat().st_size, "command": build_consumer},
        "yui": {"path": relative_owned(YUI_PATH), "sha256": sha256_file(YUI_PATH), "bytes": YUI_PATH.stat().st_size, "command": build_yui},
        "source_files": source_files(),
        "bounds": {"child_timeout_seconds": args.child_timeout_seconds, "total_timeout_seconds": args.total_timeout_seconds, "max_child_output_bytes": MAX_CHILD_OUTPUT_BYTES, "max_trace_events": MAX_TRACE_EVENTS, "max_fixture_bytes": MAX_FIXTURE_BYTES},
        "public_surface": ["messages", "session", "session/wire", "recording", "recording/wire", "audio/clock", "gatewaytesting"],
        "private_imports": False,
        "new_module_manifests": False,
    }
    write_json(PROVENANCE_PATH, provenance)
    return provenance


def sanitized_environment(source_revision: str, run_root: Path) -> dict[str, str]:
    sensitive_markers = ("API_KEY", "TOKEN", "PASSWORD", "SECRET", "CREDENTIAL")
    allowed: dict[str, str] = {}
    for key, value in os.environ.items():
        upper = key.upper()
        if any(marker in upper for marker in sensitive_markers):
            continue
        if key in {"PATH", "SYSTEMROOT", "WINDIR", "SSL_CERT_FILE", "SSL_CERT_DIR"}:
            allowed[key] = value
    home = run_root / "home"
    xdg = run_root / "xdg-config"
    home.mkdir(parents=True, exist_ok=True)
    xdg.mkdir(parents=True, exist_ok=True)
    allowed.update({"HOME": str(home), "USERPROFILE": str(home), "XDG_CONFIG_HOME": str(xdg), "LANG": "C", "LC_ALL": "C", "C23_SOURCE_REVISION": source_revision})
    return allowed


def terminate_process_group(process: subprocess.Popen[Any]) -> None:
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=2)


def run_child(command: list[str], label: str, run_root: Path, source_revision: str, timeout: float) -> dict[str, Any]:
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path = run_root / "stdout.log"
    stderr_path = run_root / "stderr.log"
    environment = sanitized_environment(source_revision, run_root)
    started = time.monotonic()
    timed_out = False
    with stdout_path.open("wb") as stdout, stderr_path.open("wb") as stderr:
        process = subprocess.Popen(command, cwd=REPO_ROOT, env=environment, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            returncode = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            terminate_process_group(process)
            returncode = process.returncode if process.returncode is not None else -signal.SIGKILL
    elapsed_ms = int((time.monotonic() - started) * 1000)
    stdout_bytes = stdout_path.stat().st_size
    stderr_bytes = stderr_path.stat().st_size
    output_bounded = stdout_bytes <= MAX_CHILD_OUTPUT_BYTES and stderr_bytes <= MAX_CHILD_OUTPUT_BYTES
    result = {
        "label": label,
        "command": command,
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": relative_owned(stdout_path),
        "stderr": relative_owned(stderr_path),
        "stdout_bytes": stdout_bytes,
        "stderr_bytes": stderr_bytes,
        "output_bounded": output_bounded,
    }
    require(output_bounded, f"{label} exceeded the bounded child output volume")
    return result


def child_text(execution: dict[str, Any], stream: str) -> str:
    path = OWNED_ROOT / execution[stream]
    return path.read_text(errors="replace")[:MAX_CHILD_OUTPUT_BYTES]


def new_run_root(label: str) -> Path:
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    root = OWNED_ROOT / "artifacts" / "runs" / f"{stamp}-{label}"
    suffix = 0
    candidate = root
    while candidate.exists():
        suffix += 1
        candidate = Path(f"{root}-{suffix}")
    candidate.mkdir(parents=True)
    return candidate


def expected_pcm(turns: int) -> bytes:
    data = bytearray()
    for turn in range(turns):
        for index in range(64):
            value = 100 + turn * 3 + index
            data.extend(int(value).to_bytes(2, "little", signed=True))
    return bytes(data)


def report_normalized(report: dict[str, Any]) -> dict[str, Any]:
    normalized = {key: report.get(key) for key in ("schema", "scenario", "turns", "fixture_sha256", "consumer_surface", "responses", "tool_calls", "tool_results", "pcm", "events", "terminal", "clean_shutdown", "trace_complete")}
    normalized["tool_calls"] = sorted(normalized["tool_calls"] or [], key=lambda item: item.get("id", ""))
    normalized["tool_results"] = sorted(normalized["tool_results"] or [], key=lambda item: item.get("id", ""))
    # The injected timestamp domain and missing-sample classification are
    # semantic evidence. Wall-clock pacing used to keep the recording worker's
    # bounded queue below its admission limit is intentionally not parity data.
    normalized["latency"] = [
        {key: item.get(key) for key in ("turn", "clock_domain", "missing_request", "missing_first_pcm", "missing_terminal")}
        for item in report.get("latency", [])
    ]
    return normalized


def validate_tool_report(report: dict[str, Any], turns: int, fixture_sha: str) -> None:
    require(not report.get("error"), f"consumer reported an error: {report.get('error')}")
    require(report.get("scenario") == "tool-matrix", f"unexpected consumer scenario: {report.get('scenario')}")
    require(report.get("turns") == turns, f"consumer turn count {report.get('turns')} != {turns}")
    require(report.get("fixture_sha256") == fixture_sha, "consumer fixture digest does not match provenance")
    require(report.get("clean_shutdown") is True, "consumer did not prove clean shutdown")
    require(report.get("trace_complete") is True, "consumer trace was truncated")
    events = report.get("events", {})
    require(events.get("overflow_drops") == 0, "consumer live-event overflow was not zero")
    require(events.get("count", 0) > 0 and events.get("count", 0) <= MAX_TRACE_EVENTS, "consumer event trace is outside bounds")
    calls = report.get("tool_calls", [])
    results = report.get("tool_results", [])
    require(len(calls) == turns * 2, f"provider tool call count {len(calls)} != {turns * 2}")
    require(len(results) == turns * 2, f"tool result count {len(results)} != {turns * 2}")
    expected_calls: dict[str, tuple[str, int]] = {}
    for turn in range(turns):
        for suffix, name in (("alpha", "lookup_alpha"), ("beta", "lookup_beta")):
            expected_calls[f"call-{turn:03d}-{suffix}"] = (name, turn)
    actual_calls = {item.get("id"): (item.get("name"), item.get("turn")) for item in calls}
    require(len(actual_calls) == len(calls), "duplicate provider tool identity")
    require(actual_calls == expected_calls, "provider tool identities were reordered, duplicated, or cross-routed")
    actual_results = {item.get("id"): (item.get("name"), item.get("turn"), item.get("content")) for item in results}
    require(len(actual_results) == len(results), "duplicate tool result identity")
    expected_results = {call_id: (name, turn, f"result:{name}:{turn:03d}") for call_id, (name, turn) in expected_calls.items()}
    require(actual_results == expected_results, "tool result content or identity did not match exactly once")
    response_ids = {item.get("id") for item in report.get("responses", [])}
    expected_responses = {f"tool-resp-{turn:03d}" for turn in range(turns)} | {f"final-resp-{turn:03d}" for turn in range(turns)}
    require(response_ids == expected_responses, "response identities are missing, duplicated, or cross-routed")
    pcm = report.get("pcm", {})
    expected = expected_pcm(turns)
    require(pcm.get("format") == "pcm16-le" and pcm.get("sample_rate") == 16000 and pcm.get("channels") == 1 and pcm.get("bit_depth") == 16, "PCM format proof is incomplete")
    require(pcm.get("bytes") == len(expected) and pcm.get("sha256") == sha256_bytes(expected), "PCM byte/SHA oracle mismatch")
    require(pcm.get("frame_samples") == 64, "PCM frame size proof is incomplete")
    latency = report.get("latency", [])
    require(len(latency) == turns, f"latency sample count {len(latency)} != {turns}")
    for sample in latency:
        require(sample.get("clock_domain") == "injected_deterministic_event_timestamp", "latency clock domain was not explicit")
        require(not sample.get("missing_request") and not sample.get("missing_first_pcm") and not sample.get("missing_terminal"), f"latency sample missing for turn {sample.get('turn')}")
        require(sample.get("request_to_first_pcm_ns", -1) >= 0 and sample.get("request_to_terminal_ns", -1) >= 0, "latency sample is negative")


def validate_interruption_report(report: dict[str, Any], fixture_sha: str) -> None:
    require(not report.get("error"), f"interruption consumer reported an error: {report.get('error')}")
    require(report.get("scenario") == "interruption", "interruption scenario identity changed")
    require(report.get("fixture_sha256") == fixture_sha, "interruption fixture digest does not match provenance")
    require(report.get("clean_shutdown") is True and report.get("trace_complete") is True, "interruption did not shut down with complete trace")
    interruption = report.get("interruption", {})
    require(interruption.get("cancel_sent") is True, "response.cancel was not admitted")
    require(interruption.get("cancellation_terminal_observed") is True, "cancelled terminal was not observed")
    require(interruption.get("healthy_response_id") == "healthy-resp-2", "healthy replacement response identity changed")
    require(interruption.get("forbidden_post_cancel_audio") is False, "cancelled response emitted forbidden post-cancel audio")
    require(interruption.get("healthy_tail_nonempty") is True and interruption.get("healthy_tail_bytes", 0) > 0, "healthy replacement has no exact non-empty tail")
    require(interruption.get("clean_terminal") is True, "interruption terminal was not clean")


def run_consumer(source_revision: str, fixture_sha: str, turns: int, recording: bool, label: str, timeout: float) -> dict[str, Any]:
    root = new_run_root(label)
    report_path = root / "consumer-report.json"
    command = [str(CONSUMER_PATH), "--scenario", "tool-matrix", "--turns", str(turns), "--artifact-root", str(root / "consumer-artifacts"), "--output", str(report_path)]
    if recording:
        command.append("--recording")
    execution = run_child(command, label, root / "process", source_revision, timeout)
    require(execution["returncode"] == 0 and not execution["timed_out"], f"{label} child failed: {child_text(execution, 'stderr').strip()}")
    report = load_json(report_path)
    validate_tool_report(report, turns, fixture_sha)
    return {"label": label, "recording": recording, "turns": turns, "execution": execution, "report": report, "report_path": relative_owned(report_path)}


def compare_recording_pair(off: dict[str, Any], on: dict[str, Any]) -> None:
    require(report_normalized(off["report"]) == report_normalized(on["report"]), f"recording off/on semantic or PCM parity failed for {off['turns']} turns")
    artifacts = on["report"].get("artifacts", {})
    semantic = artifacts.get("semantic_root")
    provider = artifacts.get("provider_capture")
    require(semantic and provider, "recording-on report omitted semantic/raw evidence paths")
    require(Path(semantic).is_dir() and Path(provider).is_file(), "recording-on evidence paths are not materialized")


def matrix_spec(args: argparse.Namespace) -> tuple[tuple[int, ...], tuple[str, ...]]:
    try:
        turns = tuple(int(item.strip()) for item in args.turns.split(",") if item.strip())
    except ValueError as error:
        raise VerificationError(f"matrix --turns must be comma-separated integers: {args.turns}") from error
    require(turns == EXPECTED_TURNS, f"matrix --turns must be exactly {','.join(str(turn) for turn in EXPECTED_TURNS)}; got {args.turns}")
    recording_modes = tuple(item.strip() for item in args.recording.split(",") if item.strip())
    require(recording_modes == EXPECTED_RECORDING_MODES, f"matrix --recording must be exactly off,on; got {args.recording}")
    return turns, recording_modes


def controls(args: argparse.Namespace, provenance: dict[str, Any]) -> dict[str, Any]:
    source_revision = provenance["source_revision"]
    fixture_sha = provenance["fixture"]["sha256"]
    started = time.monotonic()
    off = run_consumer(source_revision, fixture_sha, 16, False, "controls-off-16", args.child_timeout_seconds)
    on = run_consumer(source_revision, fixture_sha, 16, True, "controls-on-16", args.child_timeout_seconds)
    compare_recording_pair(off, on)
    interruption_root = new_run_root("controls-interruption")
    report_path = interruption_root / "consumer-report.json"
    command = [str(CONSUMER_PATH), "--scenario", "interruption", "--artifact-root", str(interruption_root / "consumer-artifacts"), "--output", str(report_path)]
    execution = run_child(command, "controls-interruption", interruption_root / "process", source_revision, args.child_timeout_seconds)
    require(execution["returncode"] == 0 and not execution["timed_out"], f"interruption control failed: {child_text(execution, 'stderr').strip()}")
    interruption = load_json(report_path)
    validate_interruption_report(interruption, fixture_sha)
    result = {"schema": "audio-runtime.c23.controls.v1", "passed": True, "elapsed_ms": int((time.monotonic() - started) * 1000), "tool_pair": {"off": off, "on": on}, "interruption": {"execution": execution, "report": interruption, "report_path": relative_owned(report_path)}}
    write_json(OWNED_ROOT / "controls.json", result)
    return result


def matrix(args: argparse.Namespace, provenance: dict[str, Any]) -> dict[str, Any]:
    started = time.monotonic()
    source_revision = provenance["source_revision"]
    fixture_sha = provenance["fixture"]["sha256"]
    runs: list[dict[str, Any]] = []
    pairs: list[dict[str, Any]] = []
    try:
        requested_turns, recording_modes = matrix_spec(args)
        for turns in requested_turns:
            require(time.monotonic() - started <= args.total_timeout_seconds, "matrix exceeded its total timeout before the next pair")
            mode_runs = {
                mode: run_consumer(source_revision, fixture_sha, turns, mode == "on", f"matrix-{mode}-{turns}", args.child_timeout_seconds)
                for mode in recording_modes
            }
            off = mode_runs["off"]
            on = mode_runs["on"]
            compare_recording_pair(off, on)
            runs.extend([off, on])
            pairs.append({"turns": turns, "recording_off": off["report_path"], "recording_on": on["report_path"], "normalized_sha256": sha256_bytes(json.dumps(report_normalized(off["report"]), sort_keys=True, separators=(",", ":")).encode())})
        result = {"schema": "audio-runtime.c23.matrix.v1", "passed": True, "elapsed_ms": int((time.monotonic() - started) * 1000), "turns": list(requested_turns), "recording_modes": list(recording_modes), "runs": runs, "pairs": pairs, "bounds": {"child_timeout_seconds": args.child_timeout_seconds, "total_timeout_seconds": args.total_timeout_seconds}}
        write_json(OWNED_ROOT / "matrix.json", result)
        return result
    except Exception as error:
        partial = {"schema": "audio-runtime.c23.matrix.v1", "passed": False, "elapsed_ms": int((time.monotonic() - started) * 1000), "runs": runs, "pairs": pairs, "error": str(error)}
        write_json(OWNED_ROOT / "matrix.json", partial)
        raise


def test6_capture(path: Path) -> dict[str, Any]:
    audio_source = REPO_ROOT / "agent-cli" / "internal" / "transport" / "cli" / "testdata" / "test6-openai-barge-in.base64"
    require(audio_source.is_file(), f"shipped test6 audio fixture missing: {audio_source}")
    segments = [base64.b64decode(line.strip()) for line in audio_source.read_text().splitlines() if line.strip() and not line.lstrip().startswith("#")]
    require(len(segments) == 2 and all(segments), "shipped test6 audio fixture did not provide two non-empty segments")
    records: list[dict[str, Any]] = []

    def add(direction: str, payload: dict[str, Any]) -> None:
        sequence = len(records) + 1
        timestamp_ms = sequence + (100 if sequence >= 3 else 0)
        records.append({"sequence": sequence, "direction": direction, "timestamp_ms": timestamp_ms, "type": payload["type"], "payload_type": "websocket_message", "payload": payload})

    c2s = "client_to_server"
    s2c = "server_to_client"
    add(c2s, {"type": "session.update", "session": {"model": "gpt-realtime-2.1-mini", "audio": {"input": {"format": {"type": "audio/pcm", "rate": 24000}}, "output": {"format": {"type": "audio/pcm", "rate": 24000}}}}})
    add(s2c, {"type": "session.created", "session": {"id": "sess-test6-barge-in", "type": "realtime", "model": "gpt-realtime-2.1-mini", "audio": {"output": {"format": {"type": "audio/pcm", "rate": 24000}}}}})
    add(c2s, {"type": "conversation.item.create", "item": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "test6 customer barge in"}]}})
    add(c2s, {"type": "response.create"})
    add(s2c, {"type": "response.created", "response": {"id": "resp-test6-interrupted", "status": "in_progress"}})
    add(s2c, {"type": "response.output_audio.delta", "response_id": "resp-test6-interrupted", "item_id": "item-test6-interrupted", "output_index": 0, "content_index": 0, "delta": base64.b64encode(segments[0]).decode()})
    add(s2c, {"type": "input_audio_buffer.speech_started", "audio_start_ms": 98092, "item_id": "item-test6-customer"})
    add(c2s, {"type": "conversation.item.truncate", "item_id": "item-test6-interrupted", "content_index": 0, "audio_end_ms": 0})
    add(s2c, {"type": "conversation.item.truncated", "item_id": "item-test6-interrupted", "content_index": 0, "audio_end_ms": 0})
    add(s2c, {"type": "response.output_audio.done", "response_id": "resp-test6-interrupted", "item_id": "item-test6-interrupted", "output_index": 0, "content_index": 0})
    add(s2c, {"type": "response.done", "response": {"id": "resp-test6-interrupted", "status": "cancelled", "status_details": {"type": "cancelled"}}})
    add(s2c, {"type": "response.created", "response": {"id": "resp-test6-new-assistant", "status": "in_progress"}})
    add(s2c, {"type": "response.output_audio.delta", "response_id": "resp-test6-new-assistant", "item_id": "item-test6-new-assistant", "output_index": 0, "content_index": 0, "delta": base64.b64encode(segments[1]).decode()})
    add(s2c, {"type": "response.output_audio.done", "response_id": "resp-test6-new-assistant", "item_id": "item-test6-new-assistant", "output_index": 0, "content_index": 0})
    add(s2c, {"type": "response.done", "response": {"id": "resp-test6-new-assistant", "status": "completed"}})
    capture = seal_session_capture({"version": 2, "provider": {"name": "openai", "model": "gpt-realtime-2.1-mini"}, "session": {"id": "sess-test6-barge-in", "started_at_utc": "2026-09-02T18:49:17.635745Z", "fixture_provenance": "shipped-test6-derived"}, "records": records})
    write_json(path, capture, sort_keys=False)
    return {"path": relative_owned(path), "source_audio": relative_repo(audio_source), "source_audio_sha256": sha256_file(audio_source), "segment_bytes": [len(segment) for segment in segments], "healthy_segment_sha256": sha256_bytes(segments[1]), "record_count": len(records), "interrupted_response_id": "resp-test6-interrupted", "healthy_response_id": "resp-test6-new-assistant"}


def shipped_regressions(args: argparse.Namespace, provenance: dict[str, Any]) -> dict[str, Any]:
    require(YUI_PATH.is_file(), "prepare must build yui before shipped regressions")
    source_revision = provenance["source_revision"]
    root = new_run_root("shipped-regressions")
    capture_path = root / "test6.session.json"
    capture = test6_capture(capture_path)
    audio_out = root / "healthy-tail.pcm"
    config_dir = root / "config"
    command = [str(YUI_PATH), "-C", str(config_dir), "session", "--replay", str(capture_path), "--replay-timing", "recorded", "--prompt", "test6 customer barge in", "--audio-out", str(audio_out), "--no-terminal-tools"]
    execution = run_child(command, "shipped-yui-test6", root / "process", source_revision, args.child_timeout_seconds)
    stderr = child_text(execution, "stderr")
    stdout = child_text(execution, "stdout")
    require(execution["returncode"] == 0 and not execution["timed_out"], f"shipped yui replay failed: {(stderr or stdout).strip()}")
    lower_output = (stderr + stdout).lower()
    for forbidden in ("api_key", "openai_api_key", "credential_reference", "unauthorized"):
        require(forbidden not in lower_output, f"shipped yui replay exposed or requested credential material: {forbidden}")
    require(audio_out.is_file() and audio_out.stat().st_size > 0, "shipped yui replay did not produce non-empty audio output")
    result = {"schema": "audio-runtime.c23.shipped-regressions.v1", "passed": True, "fixture": capture, "execution": execution, "audio_output": {"path": relative_owned(audio_out), "bytes": audio_out.stat().st_size, "sha256": sha256_file(audio_out)}, "classification": {"provider_edge": "shipped credential-free replay", "process": "proved", "PCM_file": "proved", "simulated_callback": "proved by replay ordering", "physical_device": "not_attempted", "acoustic": "not_attempted", "host_load": "not_claimed"}, "same_source_yui_sha256": provenance["yui"]["sha256"]}
    write_json(OWNED_ROOT / "shipped-regressions.json", result)
    return result


def verify_provenance(args: argparse.Namespace) -> dict[str, Any]:
    provenance = load_json(PROVENANCE_PATH)
    require(provenance.get("schema") == "audio-runtime.c23.provenance.v1", "provenance schema is not C23 v1")
    require(provenance.get("source_revision") == MAIN_REVISION, "provenance source revision is not the admitted fetched main")
    require(git_is_ancestor(provenance["source_revision"], provenance["candidate_revision"]), "provenance ancestry no longer verifies")
    fixture = provenance["fixture"]
    require(fixture.get("sha256") == fixture_hash(), "fixture changed after prepare")
    consumer = OWNED_ROOT / provenance["consumer"]["path"]
    yui = OWNED_ROOT / provenance["yui"]["path"]
    require(sha256_file(consumer) == provenance["consumer"]["sha256"], "consumer binary changed after prepare")
    require(sha256_file(yui) == provenance["yui"]["sha256"], "yui binary changed after prepare")
    negative: dict[str, Any] = {}
    if args.negative_control == "tampered-fixture":
        tampered = json.loads(FIXTURE_PATH.read_text())
        tampered["seed"] = "c23-tampered-fixture"
        tampered_hash = sha256_bytes(json.dumps(tampered, sort_keys=True, separators=(",", ":")).encode())
        rejected = tampered_hash != fixture["sha256"]
        require(rejected, "tampered fixture was not rejected by the provenance oracle")
        negative["tampered_fixture"] = {"passed": True, "tampered_sha256": tampered_hash, "expected_sha256": fixture["sha256"], "rejection": "fixture SHA-256 mismatch"}
    result = {"schema": "audio-runtime.c23.provenance-verification.v1", "passed": True, "provenance": relative_owned(PROVENANCE_PATH), "negative_controls": negative}
    write_json(OWNED_ROOT / "verify-provenance.json", result)
    return result


def self_check(args: argparse.Namespace, provenance: dict[str, Any]) -> dict[str, Any]:
    matrix_result = load_json(OWNED_ROOT / "matrix.json")
    require(matrix_result.get("passed") is True, "self-check requires a passed matrix")
    base_run = next(item for item in matrix_result["runs"] if item["turns"] == 16 and not item["recording"])
    base_report = base_run["report"]
    fixture_sha = provenance["fixture"]["sha256"]
    controls: list[tuple[str, dict[str, Any]]] = []
    mutated = json.loads(json.dumps(base_report))
    mutated["pcm"]["sha256"] = "0" * 64
    controls.append(("pcm-mismatch", mutated))
    mutated = json.loads(json.dumps(base_report))
    mutated["tool_calls"][0]["id"], mutated["tool_calls"][1]["id"] = mutated["tool_calls"][1]["id"], mutated["tool_calls"][0]["id"]
    controls.append(("identity-swap", mutated))
    mutated = json.loads(json.dumps(base_report))
    mutated["tool_results"] = mutated["tool_results"][1:]
    controls.append(("missing-result", mutated))
    mutated = json.loads(json.dumps(base_report))
    mutated["tool_results"].append(json.loads(json.dumps(mutated["tool_results"][0])))
    controls.append(("duplicate-result", mutated))
    results: list[dict[str, Any]] = []
    requested = tuple(item.strip() for item in args.negative_controls.split(",") if item.strip())
    require(set(requested) == {name for name, _ in controls}, f"self-check controls must be exactly pcm-mismatch,identity-swap,missing-result,duplicate-result; got {requested}")
    for name, report in controls:
        try:
            validate_tool_report(report, 16, fixture_sha)
        except VerificationError as error:
            results.append({"control": name, "passed": True, "rejected": str(error)})
        else:
            results.append({"control": name, "passed": False, "rejected": "negative mutation was accepted"})
    require(all(item["passed"] for item in results), "at least one C23 negative control was accepted")
    result = {"schema": "audio-runtime.c23.self-check.v1", "passed": True, "negative_controls": results, "source_report": base_run["report_path"]}
    write_json(OWNED_ROOT / "self-check.json", result)
    return result


def final_report(args: argparse.Namespace, provenance: dict[str, Any]) -> dict[str, Any]:
    verify = load_json(OWNED_ROOT / "verify-provenance.json")
    controls_result = load_json(OWNED_ROOT / "controls.json")
    matrix_result = load_json(OWNED_ROOT / "matrix.json")
    shipped = load_json(OWNED_ROOT / "shipped-regressions.json")
    require(verify.get("passed") is True and controls_result.get("passed") is True and matrix_result.get("passed") is True and shipped.get("passed") is True, "one required gate artifact is not passed")
    require(matrix_result.get("turns") == list(EXPECTED_TURNS), "matrix does not contain the required long-session turns")
    require(matrix_result.get("recording_modes") == list(EXPECTED_RECORDING_MODES), "matrix does not contain both required recording modes")
    result = {"schema": "audio-runtime.c23.final-report.v1", "passed": True, "ready_for_script_ci": True, "candidate_revision": provenance["candidate_revision"], "source_revision": provenance["source_revision"], "branch": provenance["branch"], "provenance": relative_owned(PROVENANCE_PATH), "gate_evidence": {"verify_provenance": relative_owned(OWNED_ROOT / "verify-provenance.json"), "controls": relative_owned(OWNED_ROOT / "controls.json"), "matrix": relative_owned(OWNED_ROOT / "matrix.json"), "shipped_regressions": relative_owned(OWNED_ROOT / "shipped-regressions.json")}, "criteria_classification": {"deterministic_tool_overlap": "proved", "recording_off_on_semantic_parity": "proved", "pcm_format_frame_byte_sha_parity": "proved", "interruption_recovery": "proved_simulated_provider", "process_bounds": "proved", "heap_goroutine_observations": "reported", "physical_acoustic": "not_attempted", "quiet_180s": "not_claimed"}, "ci_handoff": "ACCEPTED means submit this candidate to script CI; this report does not claim CI green."}
    write_json(OWNED_ROOT / "report.json", result)
    return result


def load_provenance() -> dict[str, Any]:
    return load_json(PROVENANCE_PATH)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    prepare_parser = subparsers.add_parser("prepare", help="build public consumer/yui and write provenance")
    prepare_parser.add_argument("--source-revision", required=True)
    prepare_parser.add_argument("--child-timeout-seconds", type=float, default=60)
    prepare_parser.add_argument("--total-timeout-seconds", type=float, default=600)
    verify_parser = subparsers.add_parser("verify-provenance", help="verify source/binary/fixture provenance")
    verify_parser.add_argument("--negative-control", choices=("tampered-fixture",), required=True)
    controls_parser = subparsers.add_parser("controls", help="run focused causal controls")
    controls_parser.add_argument("--child-timeout-seconds", type=float, default=60)
    controls_parser.add_argument("--total-timeout-seconds", type=float, default=600)
    matrix_parser = subparsers.add_parser("matrix", help="run 16/64/128/256 recording parity matrix")
    matrix_parser.add_argument("--turns", default=",".join(str(turn) for turn in EXPECTED_TURNS), help="required comma-separated turn checkpoints")
    matrix_parser.add_argument("--recording", default=",".join(EXPECTED_RECORDING_MODES), help="required comma-separated recording modes")
    matrix_parser.add_argument("--child-timeout-seconds", type=float, default=60)
    matrix_parser.add_argument("--total-timeout-seconds", type=float, default=600)
    shipped_parser = subparsers.add_parser("shipped-regressions", help="run credential-free shipped yui replay")
    shipped_parser.add_argument("--child-timeout-seconds", type=float, default=60)
    shipped_parser.add_argument("--total-timeout-seconds", type=float, default=600)
    report_parser = subparsers.add_parser("report", help="validate complete gate evidence")
    report_parser.add_argument("--require-complete-provenance", action="store_true")
    self_parser = subparsers.add_parser("self-check", help="run deliberate negative controls")
    self_parser.add_argument("--negative-controls", required=True)
    args = parser.parse_args()
    try:
        if args.command == "prepare":
            result = prepare(args)
        elif args.command == "verify-provenance":
            result = verify_provenance(args)
        else:
            provenance = load_provenance()
            if args.command == "controls":
                result = controls(args, provenance)
            elif args.command == "matrix":
                result = matrix(args, provenance)
            elif args.command == "shipped-regressions":
                result = shipped_regressions(args, provenance)
            elif args.command == "report":
                require(args.require_complete_provenance, "report requires --require-complete-provenance")
                result = final_report(args, provenance)
            elif args.command == "self-check":
                result = self_check(args, provenance)
            else:
                raise VerificationError(f"unsupported command {args.command}")
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"passed": False, "error": str(error)}, sort_keys=True))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
