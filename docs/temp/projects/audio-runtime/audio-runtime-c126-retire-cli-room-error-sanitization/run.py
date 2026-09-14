#!/usr/bin/env python3
"""Run C126's bounded shipped-yui room failure/disconnect and audio replay."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
EXPECTED_AUDIO_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
MAX_OUTPUT_BYTES = 256 * 1024
MAX_ARTIFACT_BYTES = 32 * 1024 * 1024


class EvidenceFailure(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def tree_bytes(root: Path) -> int:
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file() and not path.is_symlink()) if root.exists() else 0


def clean_environment() -> dict[str, str]:
    blocked = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    environment = {key: value for key, value in os.environ.items() if not any(marker in key.upper() for marker in blocked)}
    # Replay manifests intentionally project provider credentials to this
    # host-owned selector. The value is a fixed non-secret sentinel; the
    # capture path still owns all provider traffic and never dials live.
    environment["ROOM_REPLAY"] = "replay"
    return environment


class Capture:
    def __init__(self) -> None:
        self.data = bytearray()
        self.total = 0
        self.overflow = False

    def read(self, stream: Any) -> None:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            self.total += len(chunk)
            remaining = MAX_OUTPUT_BYTES - len(self.data)
            if remaining > 0:
                self.data.extend(chunk[:remaining])
            if self.total > MAX_OUTPUT_BYTES:
                self.overflow = True

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        return value + ("\n[output truncated]\n" if self.overflow else "")


def group_exists(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except OSError:
        return False
    return True


def run_child(label: str, argv: list[str | Path], cwd: Path, timeout: float) -> dict[str, Any]:
    command = [str(value) for value in argv]
    process = subprocess.Popen(command, cwd=cwd, env=clean_environment(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    assert process.stdout is not None and process.stderr is not None
    stdout, stderr = Capture(), Capture()
    threads = [threading.Thread(target=stdout.read, args=(process.stdout,), daemon=True), threading.Thread(target=stderr.read, args=(process.stderr,), daemon=True)]
    for thread in threads:
        thread.start()
    started = time.monotonic()
    timed_out = False
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=3)
    for thread in threads:
        thread.join(timeout=3)
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=3)
    result = {
        "label": label,
        "argv": command,
        "cwd": str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "stdout_bytes": stdout.total,
        "stderr_bytes": stderr.total,
        "stdout_truncated": stdout.overflow,
        "stderr_truncated": stderr.overflow,
        "survivors": group_exists(process.pid),
    }
    if result["survivors"]:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        result["survivors"] = group_exists(process.pid)
    if stdout.overflow or stderr.overflow or result["survivors"]:
        raise EvidenceFailure(f"{label} violated output or process-group bounds: {result}")
    return result


def record(sequence: int, direction: str, event_type: str, payload: dict[str, Any], timestamp_ms: int) -> dict[str, Any]:
    return {"sequence": sequence, "direction": direction, "timestamp_ms": timestamp_ms, "type": event_type, "payload_type": "websocket_message", "payload": payload}


def capture(participant: str, mode: str) -> dict[str, Any]:
    records = [
        record(1, "client_to_server", "session.update", {"type": "session.update", "session": {"type": "realtime", "model": "gpt-realtime"}}, 10),
        record(2, "server_to_client", "session.created", {"type": "session.created", "session": {"id": f"sess-c126-{participant}", "model": "gpt-realtime"}}, 20),
    ]
    if mode == "failure":
        records.extend([
            record(3, "server_to_client", "error", {"type": "error", "error": {"type": "server_error", "code": "room_failure", "message": "first provider failure: synthetic-room-marker"}}, 30),
            record(4, "server_to_client", "error", {"type": "error", "error": {"type": "server_error", "code": "room_failure_late", "message": "later failure must not replace first"}}, 40),
        ])
    elif mode == "survivor":
        records.extend([
            record(3, "client_to_server", "conversation.item.create", {"type": "conversation.item.create", "item": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "begin room"}]}}, 25),
            record(4, "client_to_server", "response.create", {"type": "response.create"}, 26),
            record(5, "server_to_client", "response.created", {"type": "response.created", "response": {"id": f"resp-c126-{participant}"}}, 30),
            record(6, "server_to_client", "response.done", {"type": "response.done", "response": {"id": f"resp-c126-{participant}", "status": "completed"}}, 40),
            record(7, "server_to_client", "session.closed", {"type": "session.closed"}, 50),
        ])
    capture_value: dict[str, Any] = {
        "version": 2,
        "provider": {"name": "openai", "model": "gpt-realtime"},
        "session": {"id": f"sess-c126-{participant}", "fixture_provenance": "synthetic"},
        "records": records,
    }
    if mode == "disconnect":
        capture_value["ends_with_disconnect"] = True
    coverage = {key: capture_value[key] for key in ("version", "provider", "session", "records")}
    if capture_value.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = True
    digest = hashlib.sha256(json.dumps(coverage, ensure_ascii=True, separators=(",", ":")).encode()).hexdigest()
    capture_value["integrity"] = {
        "algorithm": "sha256",
        "coverage": "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
        "digest": digest,
    }
    return capture_value


def artifact(path: str, data: bytes) -> dict[str, Any]:
    value: dict[str, Any] = {"path": path, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
    if not data:
        value["empty"] = True
    return value


def create_room_bundle(root: Path) -> Path:
    bundle = root / "room-source-bundle"
    bundle.mkdir(parents=True, exist_ok=False)
    clock = "2026-09-13T12:00:00Z"
    participant_modes = {"failed-agent": "failure", "disconnect-agent": "disconnect", "survivor-agent": "survivor"}
    participants: dict[str, Any] = {}
    room_artifacts: dict[str, Any] = {}
    for participant, mode in participant_modes.items():
        directory = bundle / "participants" / participant
        directory.mkdir(parents=True)
        files = {
            "agent.wav": b"RIFF-c126-room",
            "diagnostics.jsonl": b"{\"event\":\"room-replay\"}\n",
            "deltas.jsonl": b"",
            "sent.pcm": b"",
            "received.pcm": b"",
            "events.jsonl": b"",
        }
        refs: dict[str, Any] = {}
        role_names = {"agent.wav": "wav", "diagnostics.jsonl": "diagnostics", "deltas.jsonl": "deltas", "sent.pcm": "sent_pcm", "received.pcm": "received_pcm", "events.jsonl": "events"}
        for filename, data in files.items():
            path = directory / filename
            path.write_bytes(data)
            refs[role_names[filename]] = artifact(f"participants/{participant}/{filename}", data)
        capture_path = directory / "session.session.json"
        capture_data = json.dumps(capture(participant, mode), ensure_ascii=True, separators=(",", ":")).encode() + b"\n"
        capture_path.write_bytes(capture_data)
        refs["capture"] = artifact(f"participants/{participant}/session.session.json", capture_data)
        participant_value: dict[str, Any] = {"id": participant, "kind": "agent", "provider": "openai", "model": "gpt-realtime", "system_prompt": f"synthetic {participant}", "artifacts": refs}
        if mode == "survivor":
            participant_value["opening_prompt"] = "begin room"
        participants[participant] = participant_value

    timeline_data = b"".join([
        b'{"sequence":0,"monotonic_offset_ms":0,"unix_ms":1789291200000,"type":"speech_start","participant_id":"survivor-agent"}\n',
        b'{"sequence":1,"monotonic_offset_ms":10,"unix_ms":1789291200010,"type":"speech_end","participant_id":"survivor-agent"}\n',
    ])
    (bundle / "room-timeline.jsonl").write_bytes(timeline_data)
    room_mix = b"RIFF-c126-room-mix"
    (bundle / "room-mix.wav").write_bytes(room_mix)
    room_artifacts["room_timeline"] = artifact("room-timeline.jsonl", timeline_data)
    room_artifacts["room_mix"] = artifact("room-mix.wav", room_mix)
    manifest = {
        "schema_version": 2,
        "finalized": True,
        "clock_base": clock,
        "timing": {"started_at": clock, "ended_at": "2026-09-13T12:00:01Z", "elapsed": "1s"},
        "pcm_format": {"sample_rate_hz": 24000, "channels": 1, "sample_width_bits": 16, "byte_order": "little", "encoding": "signed_pcm16"},
        "participants": participants,
        "artifacts": room_artifacts,
    }
    (bundle / "run-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return bundle


def inspect_room_output(output: Path) -> dict[str, Any]:
    manifest_path = output / "run-manifest.json"
    if not manifest_path.is_file():
        raise EvidenceFailure(f"room replay did not finalize run-manifest.json: {output}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    participants = manifest.get("participants")
    if not isinstance(participants, dict) or set(participants) != {"failed-agent", "disconnect-agent", "survivor-agent"}:
        raise EvidenceFailure(f"room replay participant evidence is incomplete: {participants}")
    if manifest.get("finalized") is not True:
        raise EvidenceFailure(f"room replay bundle was not finalized: {manifest.get('finalized')}")
    failed = participants["failed-agent"]
    disconnected = participants["disconnect-agent"]
    survivor = participants["survivor-agent"]
    if failed.get("termination_reason") != "error" or disconnected.get("termination_reason") not in ("error", "disconnected"):
        raise EvidenceFailure(f"room replay termination projection changed: failed={failed} disconnected={disconnected}")
    if "authorization: Bearer" in json.dumps(manifest) or "c126-room-secret" in json.dumps(manifest):
        raise EvidenceFailure("room replay manifest leaked a credential-shaped value")
    if failed.get("error", "") == "" or "later failure" in failed.get("error", ""):
        raise EvidenceFailure(f"room replay did not preserve a stable first failure projection: {failed}")
    if disconnected.get("termination_reason") == "error" and "EOF" not in disconnected.get("error", ""):
        raise EvidenceFailure(f"room replay omitted the disconnect fallback: {disconnected}")
    if survivor.get("termination_reason") not in ("ended", "stopped", "max_turns_reached"):
        raise EvidenceFailure(f"room replay survivor did not complete: {survivor}")
    timeline = output / "room-timeline.jsonl"
    timeline_lines = timeline.read_text(encoding="utf-8").splitlines() if timeline.is_file() else []
    if not any("participant_media_failed" in line for line in timeline_lines) or not any("participant_terminated" in line for line in timeline_lines):
        raise EvidenceFailure("room replay timeline omitted participant failure/terminal evidence")
    evidence_text = "\n".join(path.read_text(encoding="utf-8", errors="replace") for path in output.rglob("*.jsonl") if path.is_file())
    if "authorization: Bearer" in evidence_text or "c126-room-secret" in evidence_text:
        raise EvidenceFailure("room replay evidence leaked a credential-shaped value")
    artifact_count = sum(1 for path in output.rglob("*") if path.is_file())
    if tree_bytes(output) > MAX_ARTIFACT_BYTES:
        raise EvidenceFailure("room replay output exceeded bounded artifact quota")
    return {"manifest": str(manifest_path), "participants": participants, "artifact_count": artifact_count, "bytes": tree_bytes(output)}


def run_room_case(artifact_path: Path, run_root: Path, timeout: float) -> dict[str, Any]:
    source = create_room_bundle(run_root)
    disconnect_capture = json.loads((source / "participants/disconnect-agent/session.session.json").read_text(encoding="utf-8"))
    if disconnect_capture.get("ends_with_disconnect") is not True:
        raise EvidenceFailure("room replay source omitted the explicit disconnect marker")
    failure_records = json.loads((source / "participants/failed-agent/session.session.json").read_text(encoding="utf-8"))["records"]
    failure_types = [record.get("type") for record in failure_records]
    if failure_types[-2:] != ["error", "error"]:
        raise EvidenceFailure(f"room replay source omitted the duplicate/late failure sequence: {failure_types}")
    case = run_root / "room-failure-disconnect"
    workdir = case / "workdir"
    config = case / "config"
    output = case / "output"
    workdir.mkdir(parents=True)
    config.mkdir()
    output.mkdir()
    result = run_child("room-participant-failure-disconnect", [artifact_path, "-C", config, "--workdir", workdir, "--allow-path", workdir, "room", "run", "--replay", source, "--out", output], workdir, timeout)
    if result["returncode"] != 0:
        raise EvidenceFailure(f"room replay returned {result['returncode']}: {result['stderr']}\n{result['stdout']}")
    result["evidence"] = inspect_room_output(output)
    result["source_bundle_bytes"] = tree_bytes(source)
    return result


def run_audio_case(artifact_path: Path, run_root: Path, timeout: float) -> dict[str, Any]:
    case = run_root / "non-room-audio-tool-replay"
    workdir = case / "workdir"
    config = case / "config"
    record_dir = case / "record"
    audio_out = case / "audio.wav"
    workdir.mkdir(parents=True)
    config.mkdir()
    (workdir / "evidence/runs").mkdir(parents=True)
    result = run_child("non-room-audio-tool-replay", [artifact_path, "-C", config, "--workdir", workdir, "--allow-path", workdir, "session", "--replay", FIXTURE, "--audio-out", audio_out, "--record-dir", record_dir, "--trace-audio"], workdir, timeout)
    if result["returncode"] != 0:
        raise EvidenceFailure(f"non-room replay returned {result['returncode']}: {result['stderr']}\n{result['stdout']}")
    marker = workdir / "evidence/runs/exec-invocations-v4.log"
    pcm = record_dir / "audio/out-000.pcm"
    manifest = record_dir / "manifest.json"
    if "PROBE_TOOL_MARKER_9182" not in result["stdout"] or "strict replay continuation" not in result["stdout"] or not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8"):
        raise EvidenceFailure("non-room replay omitted the ordered tool marker/result")
    if not pcm.is_file() or pcm.stat().st_size != 4800 or sha256_file(pcm) != EXPECTED_AUDIO_PCM_SHA256 or not manifest.is_file():
        raise EvidenceFailure("non-room replay audio/evidence oracle changed")
    return {"process": result, "fixture": str(FIXTURE), "fixture_sha256": sha256_file(FIXTURE), "pcm_sha256": sha256_file(pcm), "pcm_bytes": pcm.stat().st_size, "manifest": str(manifest), "marker": str(marker)}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=("room-participant-failure-disconnect", "non-room-audio-tool-replay"), dest="cases")
    parser.add_argument("--artifact", type=Path, default=HERE / "artifacts/yui")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    started = time.monotonic()
    run_root = HERE / "runs" / f"shipped-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    try:
        if args.child_timeout <= 0 or args.child_timeout > 60 or args.aggregate_timeout <= 0 or args.aggregate_timeout > 300:
            raise EvidenceFailure("bounded timeout arguments are outside the admitted limits")
        artifact_path = args.artifact.expanduser().resolve()
        if not artifact_path.is_file() or not FIXTURE.is_file():
            raise EvidenceFailure(f"missing shipped artifact or fixed fixture: artifact={artifact_path} fixture={FIXTURE}")
        run_root.mkdir(parents=True, exist_ok=False)
        cases = args.cases or ["room-participant-failure-disconnect", "non-room-audio-tool-replay"]
        reports: dict[str, Any] = {"artifact": str(artifact_path), "artifact_sha256": sha256_file(artifact_path), "candidate_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(), "cases": {}, "credential_free": True, "realtime": "not used; offline fixtures only"}
        deadline = started + args.aggregate_timeout
        with tempfile.TemporaryDirectory(prefix="c126-runtime-") as temporary:
            staging = Path(temporary)
            if "room-participant-failure-disconnect" in cases:
                reports["cases"]["room-participant-failure-disconnect"] = run_room_case(artifact_path, run_root, min(args.child_timeout, deadline - time.monotonic()))
            if "non-room-audio-tool-replay" in cases:
                reports["cases"]["non-room-audio-tool-replay"] = run_audio_case(artifact_path, run_root, min(args.child_timeout, deadline - time.monotonic()))
            _ = staging
        reports["elapsed_seconds"] = round(time.monotonic() - started, 6)
        (run_root / "outcome.json").write_text(json.dumps(reports, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        (HERE / "latest-runtime-probe.json").write_text(json.dumps(reports, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps({"status": "passed", "run": str(run_root.relative_to(ROOT)), "artifact_sha256": reports["artifact_sha256"], "elapsed_seconds": reports["elapsed_seconds"]}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        failure = {"status": "failed", "error": str(error), "run": str(run_root.relative_to(ROOT))}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(failure, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        (HERE / "latest-runtime-probe.json").write_text(json.dumps(failure, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(failure, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
