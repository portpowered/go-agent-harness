#!/usr/bin/env python3
"""Bounded causal verifier for the external C36 room-media consumer."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import pathlib
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
PROJECT_ROOT = HERE.parents[4]
MODULE_PATH = pathlib.Path("docs/temp/projects/audio-runtime/audio-runtime-c36-room-media-epoch-consumer")
FIXTURES = HERE / "fixtures"
MAX_CAPTURE_BYTES = 4 * 1024 * 1024
TOOL_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
INTERRUPTION_FIXTURE_SHA256 = "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206"
TOOL_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
INTERRUPTION_PCM_SHA256 = "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"
HEALTHY_TAIL_SHA256 = "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"


class EvidenceFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "HOME", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL")
    return {key: env.get(key, "") for key in keys}


def bounded_remaining(deadline: float, child_timeout: float) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise EvidenceFailure("aggregate verifier deadline exhausted")
    return min(child_timeout, remaining)


def drain_stream(stream: Any, limit: int, result: dict[str, bytes], key: str) -> None:
    chunks: list[bytes] = []
    total = 0
    while True:
        block = stream.read(64 * 1024)
        if not block:
            break
        if total < limit:
            chunks.append(block[: limit - total])
            total += len(chunks[-1])
    result[key] = b"".join(chunks)


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    input_bytes: bytes | None,
    deadline: float,
    child_timeout: float,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if env is None else env)
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected,
        stdin=subprocess.PIPE if input_bytes is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    captured: dict[str, bytes] = {}
    out_thread = threading.Thread(target=drain_stream, args=(process.stdout, MAX_CAPTURE_BYTES, captured, "stdout"), daemon=True)
    err_thread = threading.Thread(target=drain_stream, args=(process.stderr, MAX_CAPTURE_BYTES, captured, "stderr"), daemon=True)
    out_thread.start()
    err_thread.start()
    if input_bytes is not None and process.stdin is not None:
        try:
            process.stdin.write(input_bytes)
            process.stdin.close()
        except BrokenPipeError:
            pass
    started = time.monotonic()
    timed_out = False
    term_sent = False
    kill_sent = False
    try:
        process.wait(timeout=bounded_remaining(deadline, child_timeout))
    except subprocess.TimeoutExpired:
        timed_out = True
        term_sent = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=min(2.0, bounded_remaining(deadline, 2.0)))
        except subprocess.TimeoutExpired:
            kill_sent = True
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=min(2.0, bounded_remaining(deadline, 2.0)))
    out_thread.join(timeout=min(2.0, max(0.1, deadline - time.monotonic())))
    err_thread.join(timeout=min(2.0, max(0.1, deadline - time.monotonic())))
    stdout = captured.get("stdout", b"")
    stderr = captured.get("stderr", b"")
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    record = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "child_timeout_seconds": child_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "reaped": process.poll() is not None,
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    write_json(record_path, record)
    return record


def require_ok(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0 or not result["reaped"]:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def source_root(source: pathlib.Path) -> pathlib.Path:
    source = source.resolve()
    if (source / "go.work").is_file():
        return source
    if source.name == MODULE_PATH.name and (source / "go.mod").is_file():
        return PROJECT_ROOT
    raise EvidenceFailure(f"--source must be the admitted workspace root: {source}")


def module_dir(root: pathlib.Path) -> pathlib.Path:
    path = root / MODULE_PATH
    if not path.is_dir():
        raise EvidenceFailure(f"admitted consumer module is missing: {path}")
    return path


def git_revision(root: pathlib.Path) -> str:
    result = subprocess.run(["git", "-C", str(root), "rev-parse", "HEAD"], text=True, capture_output=True, check=True)
    return result.stdout.strip()


def build_source_archive(root: pathlib.Path, evidence: pathlib.Path) -> dict[str, Any]:
    module = module_dir(root)
    archive = evidence / "artifacts" / "source-module.tar"
    archive.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "w") as handle:
        for path in sorted(module.rglob("*")):
            relative = path.relative_to(module)
            if relative.parts and relative.parts[0] in {"evidence", "artifacts", "runs"}:
                continue
            info = handle.gettarinfo(str(path), arcname=str(MODULE_PATH / relative))
            info.mtime = 0
            info.uid = 0
            info.gid = 0
            info.uname = ""
            info.gname = ""
            if path.is_file():
                with path.open("rb") as stream:
                    handle.addfile(info, stream)
            else:
                handle.addfile(info)
    return {"path": str(archive), "sha256": sha256(archive), "revision": git_revision(root)}


def input_manifest(module: pathlib.Path, evidence: pathlib.Path) -> dict[str, Any]:
    entries: list[dict[str, str]] = []
    for path in sorted(module.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(module)
        if relative.parts and relative.parts[0] in {"evidence", "artifacts", "runs"}:
            continue
        entries.append({"path": str(relative), "sha256": sha256(path)})
    manifest = {"module": str(MODULE_PATH), "files": entries}
    manifest["manifest_sha256"] = hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()
    write_json(evidence / "artifacts" / "input-manifest.json", manifest)
    return manifest


def resolve_binary(value: str | None, default: pathlib.Path) -> pathlib.Path:
    path = pathlib.Path(value) if value else default
    if not path.is_absolute():
        path = (pathlib.Path.cwd() / path).resolve()
    if not path.is_file():
        raise EvidenceFailure(f"binary is missing: {path}")
    return path


def prepare_binaries(
    root: pathlib.Path,
    module: pathlib.Path,
    evidence: pathlib.Path,
    room_binary: str | None,
    yui_binary: str | None,
    no_build: bool,
    need_yui: bool,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    artifacts = evidence / "artifacts"
    artifacts.mkdir(parents=True, exist_ok=True)
    build: dict[str, Any] = {"mode": "external" if no_build else "built", "binaries": {}}
    if no_build:
        room = resolve_binary(room_binary, artifacts / "room-media")
        build["binaries"]["room_media"] = {"path": str(room), "sha256": sha256(room), "source": "supplied"}
        if need_yui:
            yui = resolve_binary(yui_binary, artifacts / "yui")
            build["binaries"]["yui"] = {"path": str(yui), "sha256": sha256(yui), "source": "supplied"}
        write_json(artifacts / "build.json", build)
        return {"room": room, "yui": resolve_binary(yui_binary, artifacts / "yui") if need_yui else None, "build": build}

    room = artifacts / "room-media"
    room_result = run_process(
        "build-room-media",
        ["rtk", "proxy", "go", "build", "-trimpath", "-o", str(room), "./cmd/room-media"],
        module,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(room_result)
    build["binaries"]["room_media"] = {"path": str(room), "sha256": sha256(room), "build_run": room_result["label"]}

    yui: pathlib.Path | None = None
    if need_yui:
        yui = artifacts / "yui"
        yui_result = run_process(
            "build-yui",
            ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(yui), "./agent-cli/cmd/yui"],
            root,
            evidence / "runs",
            None,
            deadline,
            bounded_remaining(deadline, child_timeout),
            dict(os.environ),
        )
        require_ok(yui_result)
        build["binaries"]["yui"] = {"path": str(yui), "sha256": sha256(yui), "build_run": yui_result["label"]}
    write_json(artifacts / "build.json", build)
    return {"room": room, "yui": yui, "build": build}


def room_run(
    binary: pathlib.Path,
    mode: str,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> tuple[dict[str, Any], dict[str, Any]]:
    output_dir = evidence / "runs" / f"room-{mode}"
    payload = json.dumps({"mode": mode, "output_dir": str(output_dir)}, separators=(",", ":")).encode() + b"\n"
    result = run_process(
        f"room-{mode}",
        [str(binary)],
        binary.parent,
        evidence / "runs",
        payload,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        report = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"room-{mode} did not emit one JSON report: {exc}") from exc
    if not isinstance(report, dict) or report.get("status") != "complete":
        raise EvidenceFailure(f"room-{mode} report is not complete: {report!r}")
    write_json(evidence / "artifacts" / f"room-{mode}.report.json", report)
    return report, result


def expect_equal(label: str, actual: Any, expected: Any) -> None:
    if actual != expected:
        raise EvidenceFailure(f"{label}: got {actual!r}, want {expected!r}")


def validate_playback_boundary(report: dict[str, Any]) -> None:
    boundary = report.get("playback_boundary")
    if not isinstance(boundary, dict):
        raise EvidenceFailure("playback boundary evidence is missing")
    expected = {
        "domain": "software-playback-boundary",
        "admitted": [41, 42],
        "consumed": [41, 42, 0, 0],
        "underflow": [0, 0],
        "discarded_stale": [7, 8, 9],
        "rendered_before_callback": 0,
        "callback_count": 1,
        "rendered_samples": 4,
        "underflow_samples": 2,
        "discarded_samples": 3,
        "admitted_samples": 2,
        "consumed_samples": 2,
        "zero_filled_samples": 2,
        "physical_device": False,
    }
    for key, value in expected.items():
        expect_equal(f"playback boundary {key}", boundary.get(key), value)
    if not isinstance(boundary.get("capability_gap"), str) or "no physical playback device" not in boundary["capability_gap"]:
        raise EvidenceFailure("playback boundary did not disclose the physical-device capability gap")


def validate_report(report: dict[str, Any], require_boundary: bool = True) -> None:
    expect_equal("schema version", report.get("schema_version"), 1)
    expect_equal("status", report.get("status"), "complete")
    expected_inputs = [
        {"participant": "alice", "epoch": 1, "sequence": 1, "samples": [101, 102], "end_of_response": False},
        {"participant": "alice", "epoch": 2, "sequence": 2, "samples": [111, 112, 113, 114], "end_of_response": True},
        {"participant": "bob", "epoch": 1, "sequence": 1, "samples": [201, 202], "end_of_response": False},
        {"participant": "bob", "epoch": 2, "sequence": 2, "samples": [211, 212, 213, 214], "end_of_response": True},
    ]
    expect_equal("source input order", report.get("source_inputs"), expected_inputs)
    expect_equal("peer-only outputs", report.get("peer_outputs"), {"alice": [211, 212, 213, 214], "bob": [111, 112, 113, 114]})
    expect_equal("epoch stale pending samples", report.get("epochs", {}).get("stale_pending_samples"), 4)
    expect_equal("epoch healthy tail", report.get("epochs", {}).get("healthy_tail"), [111, 112, 113, 114])
    expect_equal("end precedes terminal", report.get("epochs", {}).get("end_of_response_before_terminal"), True)
    expect_equal("provider frame admission", report.get("provider_admission"), {"frames": 4, "samples": 12})
    playback = report.get("playback", {})
    expect_equal("listener playback domain", playback.get("domain"), "software-playback")
    expect_equal("listener admitted PCM", playback.get("admitted"), [322, 324, 326, 328])
    expect_equal("listener consumed PCM", playback.get("consumed"), [322, 324, 326, 328])
    expect_equal("listener underflow", playback.get("underflow"), [0, 0])
    expect_equal("listener callback count", playback.get("callback_count"), 2)
    expect_equal("listener rendered samples", playback.get("rendered_samples"), 6)
    expect_equal("listener underflow samples", playback.get("underflow_samples"), 2)
    expect_equal("listener consumed samples", playback.get("consumed_samples"), 4)
    expect_equal("listener zero-filled samples", playback.get("zero_filled_samples"), 2)
    expect_equal("listener stale discard", playback.get("discarded_stale"), [7, 8, 9])
    expect_equal("listener discarded samples", playback.get("discarded_samples"), 3)
    expect_equal("listener physical device", playback.get("physical_device"), False)
    if not isinstance(playback.get("capability_gap"), str) or "no physical" not in playback["capability_gap"]:
        raise EvidenceFailure("listener playback did not disclose software-only evidence")
    terminals = report.get("terminal")
    expected_terminal = {
        "kind": "terminal",
        "sequence": 99,
        "reason": "fixture_complete",
        "classification": "fixture_complete",
        "terminal_reason": "provider_close",
        "provenance": "provider",
        "output_state": "complete",
    }
    expect_equal("terminal participants", sorted(terminals or {}), ["alice", "bob"])
    expect_equal("alice terminal", terminals.get("alice"), expected_terminal)
    expect_equal("bob terminal", terminals.get("bob"), expected_terminal)
    expect_equal("lifecycle", report.get("lifecycle"), {
        "opened": 2, "started": 2, "waited": 2, "closed": 2,
        "repeated_close_ok": True, "workers_joined": True, "natural_exit": True,
    })
    expect_equal("recording", report.get("recording"), {
        "state": "partial", "provider_trace": "unavailable",
        "reason": "fixture live handles intentionally do not emit provider capture artifacts",
        "pcm_bytes": 24, "replayable": False,
    })
    expect_equal("replay rejected", report.get("replay_rejected"), True)
    if "run-manifest.json" not in str(report.get("replay_reject_reason", "")):
        raise EvidenceFailure("partial recording was not rejected with a missing bundle artifact")
    room = report.get("room", {})
    expect_equal("room termination", room.get("termination_reason"), "stopped")
    expect_equal("room participants", room.get("participants"), {"alice": "ended", "bob": "ended", "listener": "ended"})
    if require_boundary:
        validate_playback_boundary(report)


def dependency_gate(root: pathlib.Path, module: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    result = run_process(
        "go-list-deps",
        ["rtk", "proxy", "go", "list", "-deps", "./cmd/room-media"],
        module,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(result)
    deps = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").splitlines()
    if any("/agent-cli" in line or line.endswith("agent-cli") for line in deps):
        raise EvidenceFailure("GOWORK=off dependency graph unexpectedly includes agent-cli")
    source_files = sorted(module.rglob("*.go"))
    private_imports: list[dict[str, Any]] = []
    for path in source_files:
        for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if '"' in line and ("/internal/" in line or '"os/exec"' in line):
                private_imports.append({"path": str(path.relative_to(root)), "line": line_number, "text": line.strip()})
    if private_imports:
        raise EvidenceFailure(f"consumer imports private/CLI execution APIs: {private_imports}")
    outcome = {"deps": deps, "agent_cli_absent": True, "private_imports": private_imports, "go_list": result}
    write_json(evidence / "artifacts" / "dependency-gate.json", outcome)
    return outcome


PARITY_EXPECTATIONS: dict[str, dict[str, Any]] = {
    "audio-tool": {
        "fixture_sha256": TOOL_FIXTURE_SHA256,
        "pcm_bytes": 4800,
        "pcm_sha256": TOOL_PCM_SHA256,
        "terminal": {
            "reason": "fixture_complete",
            "classification": "provider_close",
            "terminal_reason": "provider_close",
            "terminal_provenance": "provider",
            "output_state": "not_applicable",
        },
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_item.added", "response.function_call_arguments.done",
            "response.done", "conversation.item.create", "response.create", "response.created",
            "response.output_audio.delta", "response.output_audio.delta", "response.output_audio.done",
            "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
        ],
    },
    "interruption": {
        "fixture_sha256": INTERRUPTION_FIXTURE_SHA256,
        "pcm_bytes": 3840,
        "pcm_sha256": INTERRUPTION_PCM_SHA256,
        "healthy_tail_offset_bytes": 1440,
        "healthy_tail_bytes": 2400,
        "healthy_tail_sha256": HEALTHY_TAIL_SHA256,
        "terminal": {
            "reason": "replay_complete",
            "classification": "replay_complete",
            "terminal_reason": "replay_complete",
            "terminal_provenance": "replay",
            "output_state": "complete",
        },
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_audio.delta", "input_audio_buffer.speech_started",
            "conversation.item.truncate", "conversation.item.truncated", "response.output_audio.done",
            "response.done", "response.created", "response.output_audio.delta",
            "response.output_audio.done", "response.done",
        ],
    },
}


def load_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {exc}") from exc


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    if not path.is_file():
        raise EvidenceFailure(f"missing JSONL artifact: {path}")
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL at {path}:{line_number}: {exc}") from exc
        if not isinstance(value, dict):
            raise EvidenceFailure(f"non-object JSONL record at {path}:{line_number}")
        records.append(value)
    if not records:
        raise EvidenceFailure(f"empty JSONL artifact: {path}")
    return records


def validate_parity_recording(label: str, fixture: pathlib.Path, record_dir: pathlib.Path, pcm: pathlib.Path, manifest: pathlib.Path) -> dict[str, Any]:
    expectation = PARITY_EXPECTATIONS[label]
    manifest_data = load_json(manifest)
    expect_equal(f"{label} terminal", manifest_data.get("terminal"), expectation["terminal"])
    artifacts = {
        artifact.get("path"): artifact.get("sha256")
        for artifact in manifest_data.get("artifacts", [])
        if isinstance(artifact, dict)
    }
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    expect_equal(f"{label} manifest artifact set", set(artifacts), expected_paths)
    for relative in expected_paths:
        artifact_path = record_dir / relative
        if not artifact_path.is_file() or artifacts[relative] != sha256(artifact_path):
            raise EvidenceFailure(f"{label} manifest hash does not match {relative}")
    for transcript_name in ("client.transcript.jsonl", "agent.transcript.jsonl"):
        transcript = load_jsonl(record_dir / transcript_name)
        ticks = [item.get("tick") for item in transcript]
        if ticks != list(range(1, len(ticks) + 1)):
            raise EvidenceFailure(f"{label} {transcript_name} ticks are not contiguous: {ticks}")
    fixture_data = load_json(fixture)
    provider = record_dir / "provider.json"
    expect_equal(f"{label} provider fixture bytes", sha256(provider), sha256(fixture))
    expect_equal(f"{label} provider event types", [item.get("type") for item in fixture_data.get("records", [])], expectation["fixture_types"])
    pcm_bytes = pcm.read_bytes()
    expect_equal(f"{label} PCM byte count", len(pcm_bytes), expectation["pcm_bytes"])
    expect_equal(f"{label} PCM hash", sha256(pcm), expectation["pcm_sha256"])
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        offset = expectation["healthy_tail_offset_bytes"]
        tail = pcm_bytes[offset:]
        healthy_tail = {"offset_bytes": offset, "bytes": len(tail), "sha256": hashlib.sha256(tail).hexdigest()}
        expect_equal(f"{label} healthy tail", healthy_tail, {
            "offset_bytes": offset,
            "bytes": expectation["healthy_tail_bytes"],
            "sha256": expectation["healthy_tail_sha256"],
        })
    session_log = load_jsonl(record_dir / "session-log.jsonl")
    if label == "audio-tool":
        expect_equal("audio-tool session turn count", len(session_log), 1)
        expect_equal("audio-tool session input", session_log[0].get("input"), {
            "text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False,
        })
        expect_equal("audio-tool session response", session_log[0].get("response"), {
            "text": "strict replay continuation", "complete": True, "audio_offset_bytes": 0,
            "audio_bytes": 4800, "audio_segments": ["audio/out-000.pcm"],
        })
        tool_events = session_log[0].get("tool_events")
        if not isinstance(tool_events, list) or len(tool_events) != 2:
            raise EvidenceFailure("audio-tool tool event sequence is missing")
        if "PROBE_TOOL_MARKER_9182" not in json.dumps(tool_events):
            raise EvidenceFailure("audio-tool tool marker is missing from the replay trace")
    else:
        expect_equal("interruption session turn count", len(session_log), 2)
        expect_equal("interruption first response", session_log[0].get("response"), {
            "text": "", "complete": True, "audio_offset_bytes": 0,
            "audio_bytes": 1440, "audio_segments": ["audio/out-000.pcm"],
        })
        expect_equal("interruption healthy response", session_log[1].get("response"), {
            "text": "", "complete": True, "audio_offset_bytes": 1440,
            "audio_bytes": 2400, "audio_segments": ["audio/out-000.pcm"],
        })
        if any(item.get("tool_events") is not None for item in session_log):
            raise EvidenceFailure("interruption replay unexpectedly contains tool events")
    return {"terminal": manifest_data["terminal"], "healthy_tail": healthy_tail, "session_log": str(record_dir / "session-log.jsonl")}


def parity_run(
    label: str,
    fixture: pathlib.Path,
    yui: pathlib.Path,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    case_dir = evidence / "runs" / f"parity-{label}"
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    record_dir = case_dir / "tool-record"
    audio_out = case_dir / "audio.wav"
    result = run_process(
        f"yui-{label}",
        [
            str(yui), "--config-dir", str(case_dir / "config"), "--workdir", str(case_dir),
            "--allow-path", str(case_dir), "session", "--replay", str(fixture),
            "--audio-out", str(audio_out), "--record-dir", str(record_dir), "--trace-audio",
        ],
        case_dir,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        dict(os.environ),
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
    if label == "audio-tool":
        if "PROBE_TOOL_MARKER_9182" not in stdout or "strict replay continuation" not in stdout:
            raise EvidenceFailure("audio-tool replay did not preserve marker and continuation output")
        marker = case_dir / "evidence" / "runs" / "exec-invocations-v4.log"
        if not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8"):
            raise EvidenceFailure("audio-tool replay did not preserve executable tool evidence")
    elif "replay mismatch" in stdout.lower():
        raise EvidenceFailure("interruption replay reported a mismatch")
    manifest = record_dir / "manifest.json"
    pcm = record_dir / "audio" / "out-000.pcm"
    if not manifest.is_file() or not pcm.is_file() or pcm.stat().st_size == 0:
        raise EvidenceFailure(f"{label} replay did not produce complete recording artifacts")
    parity = validate_parity_recording(label, fixture, record_dir, pcm, manifest)
    summary = {
        "label": label,
        "fixture": str(fixture),
        "fixture_sha256": sha256(fixture),
        "stdout_path": result["stdout_path"],
        "stderr_path": result["stderr_path"],
        "manifest": str(manifest),
        "manifest_sha256": sha256(manifest),
        "pcm": str(pcm),
        "pcm_bytes": pcm.stat().st_size,
        "pcm_sha256": sha256(pcm),
        "audio_out": str(audio_out),
        "audio_out_bytes": audio_out.stat().st_size if audio_out.exists() else 0,
        "terminal": parity["terminal"],
        "healthy_tail": parity["healthy_tail"],
    }
    write_json(case_dir / "summary.json", summary)
    return summary


def expect_child_failure(
    label: str,
    binary: pathlib.Path,
    payload: bytes,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    result = run_process(
        label,
        [str(binary)],
        binary.parent,
        evidence / "runs",
        payload,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    if result["timed_out"] or result["exit_code"] == 0 or not result["reaped"]:
        raise EvidenceFailure(f"{label} was accepted or was not bounded: {result}")
    return result


def run_boundary(
    root: pathlib.Path,
    module: pathlib.Path,
    room: pathlib.Path,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    report, positive = room_run(room, "positive", evidence, deadline, child_timeout)
    validate_report(report)
    dependency = dependency_gate(root, module, evidence, deadline, child_timeout)
    negatives = [
        expect_child_failure(
            "room-unknown-field",
            room,
            json.dumps({"mode": "positive", "output_dir": str(evidence / "runs" / "negative-unknown"), "unknown": True}).encode() + b"\n",
            evidence, deadline, child_timeout,
        ),
        expect_child_failure(
            "room-trailing-json",
            room,
            json.dumps({"mode": "positive", "output_dir": str(evidence / "runs" / "negative-trailing")}).encode() + b"\n{}\n",
            evidence, deadline, child_timeout,
        ),
        expect_child_failure(
            "room-missing-output",
            room,
            b'{"mode":"positive"}\n',
            evidence, deadline, child_timeout,
        ),
    ]
    outcome = {"positive": positive, "report": report, "dependency": dependency, "negative_controls": negatives}
    write_json(evidence / "artifacts" / "boundary.json", outcome)
    return outcome


def run_provenance(
    root: pathlib.Path,
    module: pathlib.Path,
    evidence: pathlib.Path,
    build: dict[str, Any],
) -> dict[str, Any]:
    source_archive = evidence / "artifacts" / "source-module.tar"
    manifest = load_json(evidence / "artifacts" / "input-manifest.json")
    if not source_archive.is_file():
        raise EvidenceFailure("source archive is missing")
    expected_archive_hash = build.get("source_archive_sha256")
    if not isinstance(expected_archive_hash, str) or sha256(source_archive) != expected_archive_hash:
        raise EvidenceFailure("source archive hash does not match the admitted build record")
    for entry in manifest.get("files", []):
        path = module / entry["path"]
        if not path.is_file() or sha256(path) != entry["sha256"]:
            raise EvidenceFailure(f"input hash changed after admission: {entry['path']}")
    fixture_hashes = {
        path.name: sha256(path)
        for path in sorted(FIXTURES.glob("*.json"))
    }
    expect_equal("tool fixture hash", fixture_hashes.get("c16-audio-tool.session.json"), TOOL_FIXTURE_SHA256)
    expect_equal("interruption fixture hash", fixture_hashes.get("c16-interruption.session.json"), INTERRUPTION_FIXTURE_SHA256)
    with tempfile.TemporaryDirectory(prefix="c36-provenance-") as temp:
        mutated = pathlib.Path(temp) / "mutated-fixture.json"
        mutated.write_bytes((FIXTURES / "c16-audio-tool.session.json").read_bytes() + b"\n")
        if sha256(mutated) == TOOL_FIXTURE_SHA256:
            raise EvidenceFailure("fixture mutation did not change the provenance hash")
        mutation = {"original": TOOL_FIXTURE_SHA256, "mutated": sha256(mutated), "rejected": True}
    result = {
        "revision": git_revision(root),
        "source_archive": {"path": str(source_archive), "sha256": sha256(source_archive)},
        "input_manifest_sha256": manifest.get("manifest_sha256"),
        "binary_hashes": {name: value.get("sha256") for name, value in build.get("binaries", {}).items()},
        "fixture_hashes": fixture_hashes,
        "mutation_control": mutation,
    }
    write_json(evidence / "artifacts" / "provenance.json", result)
    return result


def run_routing_epochs(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "routing-epochs", evidence, deadline, child_timeout)
    validate_report(report)
    outcome = {"run": run, "report": report}
    write_json(evidence / "artifacts" / "routing-epochs.json", outcome)
    return outcome


def run_mutations(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "mutations", evidence, deadline, child_timeout)
    validate_report(report)
    cases: list[tuple[str, Any]] = [
        ("peer-participant-key", lambda value: value["peer_outputs"].update({"mallory": [111, 112, 113, 114]})),
        ("source-order", lambda value: value["source_inputs"].reverse()),
        ("epoch", lambda value: value["epochs"].__setitem__("stale_pending_samples", 0)),
        ("pcm", lambda value: value["peer_outputs"]["alice"].__setitem__(0, 999)),
        ("terminal", lambda value: value["terminal"]["alice"].__setitem__("provenance", "replay")),
    ]
    rejected: list[str] = []
    for label, mutate in cases:
        candidate = copy.deepcopy(report)
        mutate(candidate)
        try:
            validate_report(candidate)
        except EvidenceFailure:
            rejected.append(label)
        else:
            raise EvidenceFailure(f"mutation control accepted altered {label} evidence")
    outcome = {"run": run, "rejected_mutations": rejected}
    write_json(evidence / "artifacts" / "mutations.json", outcome)
    return outcome


def run_partial_recording(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "partial-recording", evidence, deadline, child_timeout)
    validate_report(report)
    output_dir = pathlib.Path(report["output_dir"])
    if (output_dir / "run-manifest.json").exists() or list(output_dir.rglob("provider*.json")):
        raise EvidenceFailure("partial recording unexpectedly exposed a replay/provider trace")
    outcome = {"run": run, "report": report, "provider_trace_files": [], "replay_bundle_present": False}
    write_json(evidence / "artifacts" / "partial-recording.json", outcome)
    return outcome


def run_consumption(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "consumption", evidence, deadline, child_timeout)
    validate_report(report)
    outcome = {"run": run, "report": report, "physical_device_opened": report["playback_boundary"]["physical_device"]}
    write_json(evidence / "artifacts" / "consumption.json", outcome)
    return outcome


def run_lifecycle(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "lifecycle", evidence, deadline, child_timeout)
    validate_report(report)
    cancellations: dict[str, Any] = {}
    for mode in ("cancel-before-start", "cancel-active"):
        cancellation, cancellation_run = room_run(room, mode, evidence, deadline, child_timeout)
        observed = cancellation.get("cancellation")
        if not isinstance(observed, dict) or observed.get("joined") is not True:
            raise EvidenceFailure(f"{mode} did not prove worker join")
        if observed.get("first_close_error") or observed.get("second_close_error") or observed.get("wait_error"):
            raise EvidenceFailure(f"{mode} close/wait was not idempotent: {observed}")
        if mode == "cancel-before-start" and not observed.get("start_error"):
            raise EvidenceFailure("cancel-before-start unexpectedly started")
        if mode == "cancel-active" and observed.get("start_error"):
            raise EvidenceFailure(f"cancel-active failed before cancellation: {observed}")
        cancellations[mode] = {"run": cancellation_run, "report": cancellation}
    outcome = {"run": run, "report": report, "cancellations": cancellations}
    write_json(evidence / "artifacts" / "lifecycle.json", outcome)
    return outcome


def run_hang_control(evidence: pathlib.Path, deadline: float) -> dict[str, Any]:
    code = "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"
    result = run_process(
        "hang-control",
        [sys.executable, "-c", code],
        evidence / "runs",
        evidence / "runs",
        None,
        deadline,
        min(0.25, bounded_remaining(deadline, 0.25)),
        dict(os.environ),
    )
    if not result["timed_out"] or not result["term_sent"] or not result["kill_sent"] or not result["reaped"]:
        raise EvidenceFailure(f"hang control did not prove TERM/KILL/reap: {result}")
    write_json(evidence / "artifacts" / "hang-control.json", result)
    return result


def run_parity(yui: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    runs = [
        parity_run("audio-tool", FIXTURES / "c16-audio-tool.session.json", yui, evidence, deadline, child_timeout),
        parity_run("interruption", FIXTURES / "c16-interruption.session.json", yui, evidence, deadline, child_timeout),
    ]
    negative_dir = evidence / "runs" / "parity-negative"
    negative_dir.mkdir(parents=True, exist_ok=True)
    bad_fixture = negative_dir / "missing-timeline.session.json"
    data = load_json(FIXTURES / "c16-interruption.session.json")
    records = data.get("records")
    if not isinstance(records, list) or len(records) < 2:
        raise EvidenceFailure("interruption fixture is too short for missing-timeline control")
    data["records"] = records[:-1]
    write_json(bad_fixture, data)
    negative_case = negative_dir / "case"
    negative_case.mkdir(parents=True, exist_ok=True)
    (negative_case / "config").mkdir(parents=True, exist_ok=True)
    negative = run_process(
        "yui-missing-timeline",
        [
            str(yui), "--config-dir", str(negative_case / "config"), "--workdir", str(negative_case),
            "--allow-path", str(negative_case), "session", "--replay", str(bad_fixture),
            "--audio-out", str(negative_case / "audio.wav"), "--record-dir", str(negative_case / "tool-record"),
            "--trace-audio",
        ],
        negative_case,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        dict(os.environ),
    )
    if negative["timed_out"] or negative["exit_code"] == 0 or not negative["reaped"]:
        raise EvidenceFailure(f"missing-timeline replay was not rejected: {negative}")
    outcome = {"parity": runs, "missing_timeline_rejected": negative}
    write_json(evidence / "artifacts" / "parity.json", outcome)
    return outcome


def run_actions(
    action: str,
    root: pathlib.Path,
    module: pathlib.Path,
    binaries: dict[str, Any],
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    room = binaries["room"]
    yui = binaries.get("yui")
    results: dict[str, Any] = {}
    selected = [
        "boundary", "provenance", "routing-epochs", "mutations", "partial-recording",
        "consumption", "lifecycle", "hang-control", "parity",
    ] if action == "all" else [action]
    for name in selected:
        if name == "boundary":
            results[name] = run_boundary(root, module, room, evidence, deadline, child_timeout)
        elif name == "provenance":
            results[name] = run_provenance(root, module, evidence, binaries["build"])
        elif name == "routing-epochs":
            results[name] = run_routing_epochs(room, evidence, deadline, child_timeout)
        elif name == "mutations":
            results[name] = run_mutations(room, evidence, deadline, child_timeout)
        elif name == "partial-recording":
            results[name] = run_partial_recording(room, evidence, deadline, child_timeout)
        elif name == "consumption":
            results[name] = run_consumption(room, evidence, deadline, child_timeout)
        elif name == "lifecycle":
            results[name] = run_lifecycle(room, evidence, deadline, child_timeout)
        elif name == "hang-control":
            results[name] = run_hang_control(evidence, deadline)
        elif name == "parity":
            if yui is None:
                raise EvidenceFailure("parity action requires a yui binary")
            results[name] = run_parity(yui, evidence, deadline, child_timeout)
        else:
            raise EvidenceFailure(f"unsupported verifier action {name}")
        write_json(evidence / "artifacts" / "progress.json", {"completed": list(results), "action": action})
    return results


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--action", required=True, choices=[
        "boundary", "provenance", "routing-epochs", "mutations", "partial-recording",
        "consumption", "lifecycle", "hang-control", "parity", "all",
    ])
    parser.add_argument("--source", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--room-binary")
    parser.add_argument("--yui-binary")
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=600.0)
    args = parser.parse_args(argv)
    if args.child_timeout <= 0 or args.aggregate_timeout <= 0:
        parser.error("timeouts must be positive")
    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    root = source_root(pathlib.Path(args.source))
    module = module_dir(root)
    evidence = pathlib.Path(args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    archive = build_source_archive(root, evidence)
    manifest = input_manifest(module, evidence)
    need_yui = args.action in ("parity", "all")
    binaries = prepare_binaries(
        root, module, evidence, args.room_binary, args.yui_binary, args.no_build,
        need_yui, deadline, args.child_timeout,
    )
    binaries["build"]["source_archive_sha256"] = archive["sha256"]
    binaries["build"]["source_archive"] = archive
    binaries["build"]["input_manifest_sha256"] = manifest["manifest_sha256"]
    write_json(evidence / "artifacts" / "build.json", binaries["build"])
    outcome = {
        "status": "accepted",
        "action": args.action,
        "elapsed_seconds_before_actions": round(time.monotonic() - started, 6),
        "source": str(root),
        "module": str(module),
        "revision": git_revision(root),
        "source_archive": archive,
        "input_manifest_sha256": manifest["manifest_sha256"],
        "build": binaries["build"],
    }
    outcome["results"] = run_actions(args.action, root, module, binaries, evidence, deadline, args.child_timeout)
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    write_json(evidence / "artifacts" / "verdict.json", outcome)
    print(json.dumps({"status": "accepted", "action": args.action, "evidence": str(evidence), "elapsed_seconds": outcome["elapsed_seconds"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except EvidenceFailure as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
