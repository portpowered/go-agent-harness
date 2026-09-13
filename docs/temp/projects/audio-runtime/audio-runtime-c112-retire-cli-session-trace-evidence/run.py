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
import struct
import signal
import subprocess
import sys
import threading
import time
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


def run_child(label: str, argv: list[str], cwd: Path, output_dir: Path, timeout: float) -> dict[str, Any]:
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
    sigterm_sent = sigkill_sent = False
    try:
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
                sigterm_sent = True
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=TERM_GRACE)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                    sigkill_sent = True
                except ProcessLookupError:
                    pass
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
    return result


def require_clean(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} did not exit cleanly: {result}")
    if result["cleanup"]["surviving_process_group_pids"] or not result["credential_free_environment"]:
        raise EvidenceFailure(f"{result['label']} violated cleanup or credential boundary: {result}")
    if result["stdout_truncated"] or result["stderr_truncated"]:
        raise EvidenceFailure(f"{result['label']} exceeded output cap")


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


def replay_argv(artifact: Path, fixture: Path, case_dir: Path, audio_input: Path | None = None) -> list[str]:
    workdir = case_dir / "workdir"
    (workdir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    argv = [
        str(artifact), "-C", str(case_dir / "config"), "--workdir", str(workdir), "--allow-path", str(case_dir), "session",
        "--replay", str(fixture), "--audio-out", str(case_dir / "rendered.pcm"), "--record-dir", str(case_dir / "bundle"), "--trace-audio",
    ]
    if audio_input is not None:
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
    result = run_child(name, replay_argv(artifact, fixture, case_dir, audio_input), case_dir / "workdir", case_dir / "process", min(timeout, deadline - time.monotonic()))
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
    if rendered.stat().st_size != expected["rendered_bytes"] or sha256_file(rendered) != expected["rendered_sha256"]:
        raise EvidenceFailure(f"{name} rendered PCM oracle changed")
    events = read_timeline(timeline)
    taps = {event.get("tap") for event in events if event.get("kind") == "audio"}
    runtimes = [event for event in events if event.get("kind") == "runtime"]
    runtime_kinds = {event.get("runtime_kind") for event in runtimes}
    wire_types = provider_wire_types(events)
    if (
        not expected["required_taps"].issubset(taps)
        or not expected["required_runtime_kinds"].issubset(runtime_kinds)
        or not expected["required_provider_wire_types"].issubset(wire_types)
        or len(runtimes) < expected["runtime_min"]
    ):
        raise EvidenceFailure(
            f"{name} trace did not retain its required audio edges and runtime evidence: "
            f"taps={sorted(taps)}, runtime_kinds={sorted(runtime_kinds)}, wire_types={sorted(wire_types)}"
        )
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
        "speaker_trace_bytes": speaker.stat().st_size, "speaker_trace_sha256": sha256_file(speaker), "audio_taps": sorted(taps),
        "runtime_kinds": sorted(runtime_kinds), "provider_wire_types": sorted(wire_types), "audio_input_bytes": len(audio_pcm),
        "audio_input_sha256": sha256_file(audio_input) if audio_input is not None else None,
        "service_boundary": service_boundary,
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
