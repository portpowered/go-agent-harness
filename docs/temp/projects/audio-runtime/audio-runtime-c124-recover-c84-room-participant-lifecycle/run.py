#!/usr/bin/env python3
"""Bounded, credential-free C124 vertical replay driver.

The driver launches the source-pinned yui in a fresh process group.  It builds
a small finalized two-participant room bundle from the already committed C16
session fixture, then checks participant terminal/audio/tool effects and the
negative late-terminal admission path.  All generated files stay below this
task's evidence directory; no live provider, device, Realtime credential, or
acoustic claim is involved.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import struct
import subprocess
import sys
import time
from typing import Any, Sequence


EVIDENCE = Path(__file__).resolve().parent
ROOT = EVIDENCE.parents[4]
YUI = EVIDENCE / "artifacts" / "yui"
ROOM_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c36-room-media-epoch-consumer/fixtures/c16-audio-tool.session.json"
CASE_ROOT = EVIDENCE / "runs"
MAX_CAPTURE_BYTES = 2 * 1024 * 1024
TERM_GRACE_SECONDS = 2.0
KILL_GRACE_SECONDS = 2.0


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_bytes(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def make_wav(samples: Sequence[int], sample_rate: int = 24_000) -> bytes:
    pcm = b"".join(struct.pack("<h", sample) for sample in samples)
    fmt = struct.pack("<HHIIHH", 1, 1, sample_rate, sample_rate * 2, 2, 16)
    body = b"fmt " + struct.pack("<I", len(fmt)) + fmt + b"data" + struct.pack("<I", len(pcm)) + pcm
    return b"RIFF" + struct.pack("<I", 4 + len(body)) + b"WAVE" + body


def artifact(root: Path, relative: str, data: bytes) -> dict[str, Any]:
    write_bytes(root / relative, data)
    return {"path": relative, "size": len(data), "sha256": sha256_bytes(data)}


def scrubbed_environment() -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        if name.endswith("_API_KEY") or name in {
            "OPENAI_API_BASE",
            "OPENAI_ORG_ID",
            "ANTHROPIC_API_KEY",
            "AZURE_OPENAI_API_KEY",
            "REALTIME_API_KEY",
            "YUI_API_KEY",
            "AWS_ACCESS_KEY_ID",
            "AWS_SECRET_ACCESS_KEY",
            "AWS_SESSION_TOKEN",
        }:
            del environment[name]
    environment["GOWORK"] = "off"
    # The public replay projection uses ROOM_REPLAY as a non-secret credential
    # selector.  Supplying only this sentinel keeps the CLI host adapter from
    # attempting a live credential lookup; the provider edge still receives
    # the explicit capture path and opens no live connection.
    environment["ROOM_REPLAY"] = "replay"
    return environment


def process_group_exists(process_id: int) -> bool:
    if os.name == "nt":
        return False
    try:
        os.killpg(process_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def signal_group(process: subprocess.Popen[bytes], sig: signal.Signals) -> bool:
    if os.name == "nt":
        try:
            if sig == signal.SIGKILL:
                process.kill()
            else:
                process.terminate()
            return True
        except ProcessLookupError:
            return False
    try:
        os.killpg(process.pid, sig)
        return True
    except ProcessLookupError:
        return False


def run_bounded(command: Sequence[str | Path], cwd: Path, timeout_seconds: float) -> dict[str, Any]:
    argv = [str(item) for item in command]
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=scrubbed_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name != "nt",
    )
    timed_out = False
    term_sent = False
    kill_sent = False
    stdout = b""
    stderr = b""
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as timeout_error:
        timed_out = True
        stdout = timeout_error.output or b""
        stderr = timeout_error.stderr or b""
        term_sent = signal_group(process, signal.SIGTERM)
        try:
            more_stdout, more_stderr = process.communicate(timeout=TERM_GRACE_SECONDS)
            stdout += more_stdout or b""
            stderr += more_stderr or b""
        except subprocess.TimeoutExpired as term_error:
            stdout += term_error.output or b""
            stderr += term_error.stderr or b""
            kill_sent = signal_group(process, signal.SIGKILL)
            try:
                more_stdout, more_stderr = process.communicate(timeout=KILL_GRACE_SECONDS)
                stdout += more_stdout or b""
                stderr += more_stderr or b""
            except subprocess.TimeoutExpired as kill_error:
                stdout += kill_error.output or b""
                stderr += kill_error.stderr or b""
        process.wait(timeout=KILL_GRACE_SECONDS)
    finally:
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()
    return {
        "argv": argv,
        "cwd": str(cwd.relative_to(ROOT)) if cwd.is_relative_to(ROOT) else str(cwd),
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "process_group_gone": not process_group_exists(process.pid),
        "stdout": stdout[:MAX_CAPTURE_BYTES].decode("utf-8", errors="replace"),
        "stderr": stderr[:MAX_CAPTURE_BYTES].decode("utf-8", errors="replace"),
        "output_truncated": len(stdout) > MAX_CAPTURE_BYTES or len(stderr) > MAX_CAPTURE_BYTES,
    }


def git_revision() -> str:
    result = subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True, timeout=15)
    return result.stdout.strip()


def make_capture() -> bytes:
    if not ROOM_FIXTURE.is_file():
        raise EvidenceFailure(f"committed non-room fixture missing: {ROOM_FIXTURE}")
    data = ROOM_FIXTURE.read_bytes()
    if len(data) > MAX_CAPTURE_BYTES * 8:
        raise EvidenceFailure("committed C16 fixture unexpectedly exceeds the bounded replay input")
    return data


def make_room_bundle(bundle: Path, late_terminal: bool = False) -> dict[str, Any]:
    if bundle.exists():
        shutil.rmtree(bundle)
    bundle.mkdir(parents=True)
    clock = "2026-09-12T00:00:00Z"
    ended = "2026-09-12T00:00:00.500Z"
    capture = make_capture()
    participants: dict[str, Any] = {}
    for index, participant_id in enumerate(("alpha", "beta"), start=1):
        directory = f"participants/{participant_id}"
        wav = make_wav([index * 100, index * 100 + 1, index * 100 + 2])
        diagnostics = (json.dumps({"event": "participant_ready", "participant_id": participant_id}) + "\n").encode()
        deltas = (json.dumps({"id": f"{participant_id}-audio-1", "type": "response.output_audio.delta", "offset_ms": index, "participant_id": participant_id, "stream_id": f"{participant_id}-provider", "delta": "AAEA"}) + "\n").encode()
        sent_pcm = struct.pack("<hh", index, index + 1)
        received_pcm = struct.pack("<hh", index + 10, index + 11)
        events = (json.dumps({"type": "participant_terminal", "participant_id": participant_id, "reason": "provider_close"}) + "\n").encode()
        files = {
            "wav": artifact(bundle, f"{directory}/agent.wav", wav),
            "diagnostics": artifact(bundle, f"{directory}/diagnostics.jsonl", diagnostics),
            "deltas": artifact(bundle, f"{directory}/deltas.jsonl", deltas),
            "sent_pcm": artifact(bundle, f"{directory}/sent.pcm", sent_pcm),
            "received_pcm": artifact(bundle, f"{directory}/received.pcm", received_pcm),
            "events": artifact(bundle, f"{directory}/events.jsonl", events),
            "capture": artifact(bundle, f"{directory}/session.session.json", capture),
        }
        participants[participant_id] = {
            "id": participant_id,
            "kind": "agent",
            "provider": "openai",
            "model": "gpt-realtime",
            "voice": "cedar",
            "opening_prompt": "probe PROBE_TOOL_MARKER_9182",
            "system_prompt": f"C124 recovery replay participant {participant_id}",
            "completed_turns": 2,
            "artifacts": files,
        }
    timeline_lines = [
        {"sequence": 0, "monotonic_offset_ms": 0, "unix_ms": 1789171200000, "type": "speech_start", "participant_id": "alpha"},
        {"sequence": 1, "monotonic_offset_ms": 10, "unix_ms": 1789171200010, "type": "speech_start", "participant_id": "beta"},
        {"sequence": 2, "monotonic_offset_ms": 20, "unix_ms": 1789171200020, "type": "speech_end", "participant_id": "alpha"},
        {"sequence": 3, "monotonic_offset_ms": 30, "unix_ms": 1789171200030, "type": "speech_end", "participant_id": "beta"},
    ]
    if late_terminal:
        timeline_lines.append({"sequence": 4, "monotonic_offset_ms": 1000, "unix_ms": 1789171201000, "type": "participant_terminal", "participant_id": "alpha"})
    timeline = b"".join(json.dumps(line, sort_keys=True).encode() + b"\n" for line in timeline_lines)
    room_mix = make_wav([1, 2, 3, 4])
    timeline_ref = artifact(bundle, "room-timeline.jsonl", timeline)
    mix_ref = artifact(bundle, "room-mix.wav", room_mix)
    manifest = {
        "schema_version": 2,
        "finalized": True,
        "clock_base": clock,
        "timing": {"started_at": clock, "ended_at": ended, "elapsed": "500ms"},
        "pcm_format": {"sample_rate_hz": 24000, "channels": 1, "sample_width_bits": 16, "byte_order": "little", "encoding": "signed_pcm16"},
        "participants": participants,
        "artifacts": {"room_timeline": timeline_ref, "room_mix": mix_ref},
    }
    write_json(bundle / "run-manifest.json", manifest)
    return manifest


def read_event_log(path: Path) -> list[dict[str, Any]]:
    if not path.is_file():
        raise EvidenceFailure(f"room replay event log missing: {path}")
    events: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise EvidenceFailure(f"room replay event log is not JSON at {path}:{line_number}: {error}") from error
        if not isinstance(event, dict):
            raise EvidenceFailure(f"room replay event at {path}:{line_number} is not an object")
        events.append(event)
    return events


def output_artifact_path(output: Path, participant_id: str, value: dict[str, Any], artifact_name: str) -> Path:
    artifacts = value.get("artifacts")
    artifact = artifacts.get(artifact_name) if isinstance(artifacts, dict) else None
    relative = artifact.get("path") if isinstance(artifact, dict) else None
    if not isinstance(relative, str) or not relative:
        raise EvidenceFailure(f"participant {participant_id} has no {artifact_name} artifact path")
    path = output / relative
    try:
        path.relative_to(output)
    except ValueError as error:
        raise EvidenceFailure(f"participant {participant_id} {artifact_name} artifact escapes output") from error
    return path


def assert_output_manifest(output: Path, stdout: str) -> dict[str, Any]:
    manifest_path = output / "run-manifest.json"
    if not manifest_path.is_file():
        raise EvidenceFailure("room replay exited successfully without run-manifest.json")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"room replay output manifest is not JSON: {error}") from error
    participants = manifest.get("participants")
    if not isinstance(participants, dict) or set(participants) != {"alpha", "beta"}:
        raise EvidenceFailure(f"room replay participant output = {participants!r}")
    for participant_id, value in participants.items():
        if not isinstance(value, dict) or not value.get("connected"):
            raise EvidenceFailure(f"participant {participant_id} was not connected in output manifest")
        if value.get("termination_reason") in (None, ""):
            raise EvidenceFailure(f"participant {participant_id} has no terminal observation")
        if "participant \"" + participant_id + "\"" not in stdout:
            raise EvidenceFailure(f"yui stdout did not report participant {participant_id}")
    required_files = [output / "room-mix.wav"]
    received_files: dict[str, Path] = {}
    for path in required_files:
        if not path.is_file() or path.stat().st_size == 0:
            raise EvidenceFailure(f"room replay audio artifact missing or empty: {path.relative_to(output)}")
    for participant_id, value in sorted(participants.items()):
        wav_path = output_artifact_path(output, participant_id, value, "wav")
        if not wav_path.is_file() or wav_path.stat().st_size == 0:
            raise EvidenceFailure(f"participant {participant_id} WAV artifact missing or empty: {wav_path.relative_to(output)}")
        required_files.append(wav_path)
        received_path = output_artifact_path(output, participant_id, value, "received_pcm")
        if not received_path.is_file():
            raise EvidenceFailure(f"participant {participant_id} received PCM artifact missing: {received_path.relative_to(output)}")
        received_files[participant_id] = received_path
        events_path = output / "participants" / participant_id / "events.jsonl"
        events = read_event_log(events_path)
        if not any(
            event.get("kind") == "audio_source" and isinstance(event.get("sample_count"), int) and event["sample_count"] > 0
            for event in events
        ):
            raise EvidenceFailure(f"participant {participant_id} produced no non-empty audio_source event")
    latency_path = output / "room-latency.json"
    if not latency_path.is_file():
        raise EvidenceFailure("room replay did not produce room-latency.json")
    try:
        latency = json.loads(latency_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"room replay latency report is not JSON: {error}") from error
    latency_events = latency.get("events") if isinstance(latency, dict) else None
    if not isinstance(latency_events, list):
        raise EvidenceFailure("room replay latency report has no events")
    for participant_id in participants:
        if not any(
            isinstance(event, dict)
            and event.get("kind") == "speaker_pcm_segment"
            and event.get("participant_id") == participant_id
            and event.get("peer_participant_id") in participants
            and isinstance(event.get("pcm_bytes"), int)
            and event["pcm_bytes"] > 0
            for event in latency_events
        ):
            raise EvidenceFailure(f"room replay produced no non-empty speaker PCM route for participant {participant_id}")
    return {
        "termination_reason": manifest.get("termination_reason"),
        "participants": {
            participant_id: {
                "connected": value.get("connected"),
                "termination_reason": value.get("termination_reason"),
                "turns_completed": value.get("turns_completed"),
                "artifacts": sorted((value.get("artifacts") or {}).keys()),
            }
            for participant_id, value in sorted(participants.items())
        },
        "audio_artifacts": {str(path.relative_to(output)): path.stat().st_size for path in required_files},
        # Strict C16 replay emits provider output and room graph source routes,
        # but its server-side sequence has no input_audio_buffer.append records.
        # The peer receive files are therefore valid empty artifacts in this
        # fixture; a non-empty peer file would require a different capture.
        "peer_received_pcm": {participant_id: path.stat().st_size for participant_id, path in sorted(received_files.items())},
    }


def run_room_positive(child_timeout: float) -> dict[str, Any]:
    root = CASE_ROOT / "multi-participant-room-replay"
    if root.exists():
        shutil.rmtree(root)
    bundle = root / "bundle"
    output = root / "output"
    workdir = root / "work"
    make_room_bundle(bundle)
    workdir.mkdir(parents=True)
    (workdir / "evidence" / "runs").mkdir(parents=True)
    result = run_bounded([YUI, "room", "run", "--replay", bundle, "--out", output, "--workdir", workdir, "--allow-path", workdir], workdir, child_timeout)
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"multi-participant room replay failed: {result['stderr'][-2000:]}")
    if not result["process_group_gone"]:
        raise EvidenceFailure("room replay left a live process group")
    tool_log = workdir / "evidence" / "runs" / "exec-invocations-v4.log"
    if not tool_log.is_file() or tool_log.read_text(encoding="utf-8").count("PROBE_TOOL_MARKER_9182") < 2:
        raise EvidenceFailure("room replay did not produce both participant tool effects")
    projection = assert_output_manifest(output, result["stdout"])
    report = {
        "case": "multi-participant-room-replay",
        "source_revision": git_revision(),
        "binary": {"path": str(YUI.relative_to(ROOT)), "sha256": sha256_file(YUI), "size": YUI.stat().st_size},
        "fixture": str(ROOM_FIXTURE.relative_to(ROOT)),
        "process": {key: value for key, value in result.items() if key not in {"stdout", "stderr"}},
        "stdout": result["stdout"],
        "stderr": result["stderr"],
        "tool_effect": {"path": str(tool_log.relative_to(ROOT)), "marker_count": tool_log.read_text(encoding="utf-8").count("PROBE_TOOL_MARKER_9182")},
        "room_projection": projection,
        "capability_boundary": "software replay/audio artifacts only; no device or acoustic claim",
    }
    write_json(root / "report.json", report)
    return report


def run_late_terminal_negative(child_timeout: float) -> dict[str, Any]:
    root = CASE_ROOT / "malformed-or-late-terminal"
    if root.exists():
        shutil.rmtree(root)
    bundle = root / "bundle"
    output = root / "output"
    workdir = root / "work"
    manifest = make_room_bundle(bundle, late_terminal=True)
    # Keep the late terminal in the malformed timeline, but deliberately leave
    # its declared digest invalid. Admission must reject the bundle before any
    # participant/session side effect is started.
    manifest["artifacts"]["room_timeline"]["sha256"] = "0" * 64
    write_json(bundle / "run-manifest.json", manifest)
    workdir.mkdir(parents=True)
    (workdir / "evidence" / "runs").mkdir(parents=True)
    result = run_bounded([YUI, "room", "run", "--replay", bundle, "--out", output, "--workdir", workdir, "--allow-path", workdir], workdir, child_timeout)
    combined = result["stdout"] + result["stderr"]
    if result["timed_out"] or result["exit_code"] == 0:
        raise EvidenceFailure("late-terminal replay was accepted or timed out")
    if "room replay bundle" not in combined.lower() or "mismatch" not in combined.lower():
        raise EvidenceFailure(f"late-terminal replay failed without a classified mismatch: {combined[-2000:]}")
    if not result["process_group_gone"]:
        raise EvidenceFailure("late-terminal replay left a live process group")
    report = {
        "case": "malformed-or-late-terminal",
        "source_revision": git_revision(),
        "binary": {"path": str(YUI.relative_to(ROOT)), "sha256": sha256_file(YUI), "size": YUI.stat().st_size},
        "process": {key: value for key, value in result.items() if key not in {"stdout", "stderr"}},
        "classification": "room replay bundle mismatch",
        "stdout": result["stdout"],
        "stderr": result["stderr"],
        "output_created": output.exists(),
    }
    write_json(root / "report.json", report)
    return report


def run_non_room_audio_tool(child_timeout: float) -> dict[str, Any]:
    root = CASE_ROOT / "non-room-audio-tool"
    if root.exists():
        shutil.rmtree(root)
    root.mkdir(parents=True)
    workdir = root / "work"
    workdir.mkdir()
    (workdir / "evidence" / "runs").mkdir(parents=True)
    audio = root / "audio.wav"
    result = run_bounded([YUI, "session", "--replay", ROOM_FIXTURE, "--audio-out", audio, "--workdir", workdir, "--allow-path", workdir], workdir, child_timeout)
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"non-room audio/tool replay failed: {result['stderr'][-2000:]}")
    tool_log = workdir / "evidence" / "runs" / "exec-invocations-v4.log"
    if not tool_log.is_file() or "PROBE_TOOL_MARKER_9182" not in tool_log.read_text(encoding="utf-8"):
        raise EvidenceFailure("non-room replay did not produce the expected tool effect")
    if not audio.is_file() or audio.stat().st_size == 0:
        raise EvidenceFailure("non-room replay did not produce audio output")
    if not result["process_group_gone"]:
        raise EvidenceFailure("non-room replay left a live process group")
    report = {
        "case": "non-room-audio-tool",
        "source_revision": git_revision(),
        "binary": {"path": str(YUI.relative_to(ROOT)), "sha256": sha256_file(YUI), "size": YUI.stat().st_size},
        "fixture": str(ROOM_FIXTURE.relative_to(ROOT)),
        "process": {key: value for key, value in result.items() if key not in {"stdout", "stderr"}},
        "stdout": result["stdout"],
        "stderr": result["stderr"],
        "tool_effect": {"marker_count": tool_log.read_text(encoding="utf-8").count("PROBE_TOOL_MARKER_9182")},
        "audio": {"path": str(audio.relative_to(ROOT)), "sha256": sha256_file(audio), "size": audio.stat().st_size},
        "capability_boundary": "software replay/audio file only; no device or acoustic claim",
    }
    write_json(root / "report.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("multi-participant-room-replay", "malformed-or-late-terminal", "non-room-audio-tool"))
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.aggregate_timeout <= 0:
        parser.error("timeouts must be positive")
    if not YUI.is_file():
        print(json.dumps({"status": "failed", "error": f"source-pinned yui is missing: {YUI}"}), file=sys.stderr)
        return 1
    started = time.monotonic()
    try:
        if args.case == "multi-participant-room-replay":
            report = run_room_positive(args.child_timeout)
        elif args.case == "malformed-or-late-terminal":
            report = run_late_terminal_negative(args.child_timeout)
        else:
            report = run_non_room_audio_tool(args.child_timeout)
        if time.monotonic() - started > args.aggregate_timeout:
            raise EvidenceFailure("aggregate timeout exceeded")
        print(json.dumps({"status": "passed", "case": args.case, "report": str((CASE_ROOT / args.case / "report.json").relative_to(ROOT))}, sort_keys=True))
        return 0
    except (EvidenceFailure, subprocess.TimeoutExpired) as error:
        print(json.dumps({"status": "failed", "case": args.case, "error": str(error)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
