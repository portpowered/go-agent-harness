#!/usr/bin/env python3
"""Run bounded shipped-YUI room-terminal and audio/tool replay controls."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time


EVIDENCE = Path(__file__).resolve().parent
ROOT = next(parent for parent in EVIDENCE.parents if (parent / "go.work").is_file())
ROOM_FIXTURE = ROOT / "agent-cli/internal/services/testdata/room-audio/long-conversation-termination"
ROOM_CAPTURE_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-interruption.session.json"
AUDIO_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
CHILD_TIMEOUT = 60.0
AGGREGATE_TIMEOUT = 300.0
OUTPUT_LIMIT = 1 << 20


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def seal_capture(capture: dict[str, object]) -> None:
    coverage = {key: capture[key] for key in ("version", "provider", "session", "records", "ends_with_disconnect") if key in capture and capture[key] is not None}
    encoded = json.dumps(coverage, separators=(",", ":"), ensure_ascii=False).encode()
    capture["integrity"]["digest"] = hashlib.sha256(encoded).hexdigest()


def clean_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = "off"
    # RoomReplayPlan.Manifest currently uses ROOM_REPLAY as its provider
    # credential selector. This fixed non-secret sentinel admits the replay
    # path while the runtime's capture-backed provider never dials a network.
    environment["ROOM_REPLAY"] = "replay"
    environment["LC_ALL"] = "C"
    environment["LANG"] = "C"
    return environment


def run_child(label: str, command: list[str | Path], cwd: Path, deadline: float, expected: int = 0) -> dict[str, object]:
    require(time.monotonic() < deadline, f"aggregate deadline expired before {label}")
    started = time.monotonic()
    process = subprocess.Popen([str(item) for item in command], cwd=cwd, env=clean_environment(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    try:
        remaining = min(CHILD_TIMEOUT, max(0.1, deadline - started))
        stdout, stderr = process.communicate(timeout=remaining)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
    stdout = stdout[:OUTPUT_LIMIT]
    stderr = stderr[:OUTPUT_LIMIT]
    result = {
        "label": label,
        "command": [str(item) for item in command],
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": stdout.decode(errors="replace"),
        "stderr": stderr.decode(errors="replace"),
        "output_bounded": len(stdout) <= OUTPUT_LIMIT and len(stderr) <= OUTPUT_LIMIT,
    }
    require(not timed_out and process.returncode == expected, f"{label} returned {process.returncode}, want {expected}; stderr={result['stderr']}")
    return result


def parse_json_tail(execution: dict[str, object], label: str) -> dict[str, object]:
    lines = [line for line in str(execution["stdout"]).splitlines() if line.strip()]
    for line in reversed(lines):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            return value
    raise EvidenceFailure(f"{label} did not emit a JSON object: {execution['stdout']}")


def build_yui(root: Path, deadline: float) -> tuple[Path, dict[str, object]]:
    binary = root / "yui"
    build = run_child("build-yui", ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", binary, "./cmd/yui"], ROOT / "agent-cli", deadline)
    require(binary.is_file() and binary.stat().st_size > 0, "source-pinned yui binary is missing")
    build["binary_sha256"] = sha256_file(binary)
    build["binary_bytes"] = binary.stat().st_size
    return binary, build


def prepare_room_bundle(destination: Path) -> Path:
    """Adapt the committed golden's legacy capture shape to shipped replay."""
    shutil.copytree(ROOM_FIXTURE, destination)
    manifest_path = destination / "run-manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for participant_id, participant in manifest.get("participants", {}).items():
        capture = participant.pop("capture", None)
        if capture is not None:
            participant.setdefault("artifacts", {})["capture"] = capture
        participant["provider"] = "openai" if participant_id == "agent-a" else ""
        participant["model"] = "gpt-realtime" if participant_id == "agent-a" else ""
        participant["opening_prompt"] = ""
        if participant_id == "agent-b":
            # Keep a second admitted room participant without creating a
            # second provider/media graph that could diverge the session
            # replay's captured outbound control sequence.
            participant["kind"] = "human"
            participant["voice"] = ""
        capture_path = destination / "participants" / participant_id / "capture.session.json"
        shutil.copyfile(ROOM_CAPTURE_FIXTURE, capture_path)
        capture_data = json.loads(capture_path.read_text(encoding="utf-8"))
        records = capture_data["records"][:2]
        records.append({"sequence": 3, "direction": "server_to_client", "timestamp_ms": 20, "type": "session.closed", "payload_type": "websocket_message", "payload": {"type": "session.closed"}})
        for sequence, record in enumerate(records, start=1):
            record["sequence"] = sequence
        capture_data["records"] = records
        seal_capture(capture_data)
        capture_path.write_text(json.dumps(capture_data, indent=2) + "\n", encoding="utf-8")
        capture_ref = participant["artifacts"]["capture"]
        capture_ref["size"] = capture_path.stat().st_size
        capture_ref["sha256"] = sha256_file(capture_path)
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return destination


def room_terminal(binary: Path, root: Path, deadline: float) -> dict[str, object]:
    case = root / "room-terminal"
    output = case / "output"
    config = case / "config"
    bundle = prepare_room_bundle(case / "bundle")
    config.mkdir(parents=True)
    execution = run_child("room-terminal", [binary, "--config-dir", config, "--workdir", case, "--allow-path", case, "room", "run", "--replay", bundle, "--out", output], case, deadline)
    text = str(execution["stdout"]) + str(execution["stderr"])
    require("agent-a" in text and "agent-b" in text and "classification=" in text, "room-terminal output omitted participant terminal metadata")
    require("credential" not in text.lower() and "api_key" not in text.lower(), "room-terminal output exposed credential-shaped text")
    recorded = json.loads((output / "run-manifest.json").read_text(encoding="utf-8"))
    agent = recorded["participants"]["agent-a"]
    require(agent["terminal_provenance"] == "provider", "provider provenance was not preserved")
    require(agent["terminal_reason"] == "provider_close", "provider close reason was not preserved")
    require(agent["output_state"] == "not_applicable", "provider output state was not preserved")
    require(agent["classification"] == "provider_close", "provider classification was not preserved")
    human = recorded["participants"]["agent-b"]
    require(human["kind"] == "human" and human["reason"] == "ended", "human participant termination was not preserved")
    require(human["termination_reason"] == "ended" and human["termination_trigger"] == "ended", "human participant termination trigger was not preserved")
    return {"execution": execution, "fixture_sha256": sha256_file(ROOM_FIXTURE / "run-manifest.json"), "capture_fixture_sha256": sha256_file(ROOM_CAPTURE_FIXTURE), "runtime_schema_adapter": "direct_capture_to_artifacts.capture_plus_valid_session_replay", "metadata_observed": True, "observed_participants": recorded.get("participants", {})}


def malformed_terminal(binary: Path, root: Path, deadline: float) -> dict[str, object]:
    case = root / "malformed-terminal"
    bundle = case / "bundle"
    output = case / "output"
    config = case / "config"
    prepare_room_bundle(bundle)
    manifest_path = bundle / "run-manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["finalized"] = False
    manifest["participants"]["agent-a"]["terminal_provenance"] = "contradictory-provider-secret"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    config.mkdir(parents=True)
    execution = run_child("malformed-terminal", [binary, "--config-dir", config, "--workdir", case, "--allow-path", case, "room", "run", "--replay", bundle, "--out", output], case, deadline, expected=1)
    text = (str(execution["stdout"]) + str(execution["stderr"])).lower()
    require("finalized" in text or "incomplete" in text or "replay" in text, "malformed terminal fixture did not fail closed with bounded replay admission evidence")
    require("contradictory-provider-secret" not in text, "malformed terminal fixture leaked its injected detail")
    return {"execution": execution, "fail_closed": True, "injected_terminal_contradiction": True}


def audio_tool_replay(binary: Path, root: Path, deadline: float) -> dict[str, object]:
    case = root / "audio-tool-replay"
    record = case / "record"
    config = case / "config"
    config.mkdir(parents=True)
    (case / "evidence" / "runs").mkdir(parents=True)
    execution = run_child("audio-tool-replay", [binary, "--config-dir", config, "--workdir", case, "--allow-path", case, "session", "--replay", AUDIO_FIXTURE, "--replay-timing", "immediate", "--record-dir", record, "--trace-audio"], case, deadline)
    require("PROBE_TOOL_MARKER_9182" in str(execution["stdout"]), "audio/tool continuation did not execute its credential-free tool marker")
    pcm = record / "audio" / "out-000.pcm"
    require(pcm.is_file() and pcm.stat().st_size == 4800, "audio/tool continuation PCM oracle changed")
    return {"execution": execution, "fixture_sha256": sha256_file(AUDIO_FIXTURE), "pcm_bytes": pcm.stat().st_size, "pcm_sha256": sha256_file(pcm), "credentials": "not_used", "physical_device": "not_attempted", "acoustic": "not_attempted"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", required=True, choices=("room-terminal", "malformed-terminal", "audio-tool-replay"))
    parser.add_argument("--child-timeout", type=float, default=CHILD_TIMEOUT)
    parser.add_argument("--aggregate-timeout", type=float, default=AGGREGATE_TIMEOUT)
    args = parser.parse_args()
    require(0 < args.child_timeout <= CHILD_TIMEOUT, "child timeout must be at most 60 seconds")
    require(0 < args.aggregate_timeout <= AGGREGATE_TIMEOUT, "aggregate timeout must be at most 300 seconds")
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c94-") as scratch:
        scratch_root = Path(scratch)
        deadline = started + args.aggregate_timeout
        binary, build = build_yui(scratch_root, deadline)
        results: dict[str, object] = {"build": build}
        for case in args.case:
            if case == "room-terminal":
                results[case] = room_terminal(binary, scratch_root, deadline)
            elif case == "malformed-terminal":
                results[case] = malformed_terminal(binary, scratch_root, deadline)
            else:
                results[case] = audio_tool_replay(binary, scratch_root, deadline)
        result = {"schema": "audio-runtime.c94.shipped-run.v1", "passed": True, "source_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(), "binary_sha256": build["binary_sha256"], "cases": results, "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000), "child_timeout_seconds": args.child_timeout, "aggregate_timeout_seconds": args.aggregate_timeout, "credentials": "not_used", "physical_device": "not_attempted", "acoustic": "not_attempted"}
        print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceFailure as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
