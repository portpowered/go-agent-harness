#!/usr/bin/env python3
"""Bounded software/file replay controls for the C60 handoff."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
C52_EVIDENCE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization"
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", "")).resolve()
ARTIFACT = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c52-hermetic-room-liveness-characterization-corrected-vertical-probe/artifact-0"
SOURCE_REPORT = FACTORY_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization-corrected-vertical-probe.json"
CAPTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
CONFIG = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/fixtures/yui-config"
EXPECTED_ARTIFACT_SHA256 = "ab4d99ed1e7aa3fd96d643d09f703fb5a6d46e1d34e5a5ed6a4c10c82621b9b4"
EXPECTED_REPORT_SHA256 = "8b4f23539aceb6f687e4adafef711b4fc65cc91862a0af5670add5f7b7379a25"
EXPECTED_CAPTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
EXPECTED_CONFIG_SHA256 = "b48242a57dd47ad90a32b6ecab83513768a268cee272c62021cfdd53df19ddbd"
EXPECTED_MODELS_SHA256 = "cd5c7765b3a4ffe1e2996879ed80c480153bb20a13a426fab0f4d0c3d99ddff1"
EXPECTED_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
EXPECTED_SESSION_LOG_SHA256 = "039d49adc0239cb447de0bf28e3fe009a4601e1618ebd5449553e2d903cb4cf3"
EXPECTED_MARKER = "PROBE_TOOL_MARKER_9182"
EXPECTED_CONTINUATION = "strict replay continuation"
SECRET_KEYS = (
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "OPENROUTER_API_KEY",
)
EXPECTED_TERMINAL = {
    "reason": "fixture_complete",
    "classification": "provider_close",
    "terminal_reason": "provider_close",
    "terminal_provenance": "provider",
    "output_state": "not_applicable",
}
EXPECTED_ARTIFACTS = {
    "client.transcript.jsonl",
    "agent.transcript.jsonl",
    "session-log.jsonl",
    "audio/out-000.pcm",
    "provider.json",
}


class ReplayFailure(RuntimeError):
    pass


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ReplayFailure(f"cannot load JSON {path}: {exc}") from exc


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def relative_or_absolute(path: Path) -> str:
    try:
        return path.resolve().relative_to(ROOT.resolve()).as_posix()
    except ValueError:
        return str(path.resolve())


def require_file(path: Path, expected: str | None = None) -> None:
    if not path.is_file():
        raise ReplayFailure(f"required replay input is missing: {path}")
    if expected is not None and sha256(path) != expected:
        raise ReplayFailure(f"replay input hash changed: {path}")


def sanitized_environment(root: Path) -> dict[str, str]:
    home = root / "home"
    temporary = root / "tmp"
    home.mkdir(parents=True, exist_ok=True)
    temporary.mkdir(parents=True, exist_ok=True)
    path = os.environ.get("PATH", "/usr/bin:/bin")
    return {
        "PATH": path,
        "HOME": str(home),
        "TMPDIR": str(temporary),
        "LANG": "C",
        "LC_ALL": "C",
        "TZ": "UTC",
        "NO_COLOR": "1",
        "GOWORK": "",
    }


def process_summary(result: dict[str, object], expected_exit: int | None = None) -> dict[str, object]:
    cleanup = result.get("cleanup")
    if not isinstance(cleanup, dict):
        raise ReplayFailure(f"{result.get('label', 'replay')} has no cleanup record")
    if result.get("timed_out") is not False or result.get("output_overflow") is not False or result.get("reader_survivor") is not False:
        raise ReplayFailure(f"{result.get('label', 'replay')} violated bounded process controls")
    if cleanup.get("group_survivor") is not False:
        raise ReplayFailure(f"{result.get('label', 'replay')} left a process-group survivor")
    if expected_exit is not None and result.get("exit_code") != expected_exit:
        raise ReplayFailure(f"{result.get('label', 'replay')} exit={result.get('exit_code')!r}, expected {expected_exit}")
    return {
        "label": result.get("label"),
        "command": result.get("command"),
        "cwd": result.get("cwd"),
        "started_at": result.get("started_at"),
        "ended_at": result.get("ended_at"),
        "duration_seconds": result.get("duration_seconds"),
        "exit_code": result.get("exit_code"),
        "timed_out": result.get("timed_out"),
        "output_overflow": result.get("output_overflow"),
        "reader_survivor": result.get("reader_survivor"),
        "stdout_bytes": result.get("stdout_bytes"),
        "stderr_bytes": result.get("stderr_bytes"),
        "stdout_sha256": result.get("stdout_sha256"),
        "stderr_sha256": result.get("stderr_sha256"),
        "cleanup": cleanup,
        "stdout_path": result.get("stdout_path"),
        "stderr_path": result.get("stderr_path"),
        "process_record_path": result.get("process_record_path"),
    }


def run_process(
    label: str,
    command: list[str],
    cwd: Path,
    env: dict[str, str],
    output_dir: Path,
    timeout: float,
) -> dict[str, object]:
    sys.path.insert(0, str(C52_EVIDENCE))
    try:
        import run as c52_runner
    except ImportError as exc:
        raise ReplayFailure(f"C52 bounded process helper is unavailable: {exc}") from exc
    result = c52_runner.run_bounded(command, cwd, env, timeout, 2 * 1024 * 1024, output_dir)
    result["label"] = label
    result["stdout_path"] = str(output_dir / "stdout.log")
    result["stderr_path"] = str(output_dir / "stderr.log")
    result["environment"] = {key: env.get(key, "") for key in ("PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "GOWORK")}
    process_path = output_dir / "process.json"
    if process_path.is_file():
        process_value = load_json(process_path)
        if isinstance(process_value, dict):
            process_value["label"] = label
            process_value["environment"] = result["environment"]
            write_json(process_path, process_value)
    result["process_record_path"] = str(process_path)
    return result


def output_text(result: dict[str, object]) -> str:
    values = []
    for key in ("stdout_path", "stderr_path"):
        path = result.get(key)
        if isinstance(path, str) and Path(path).is_file():
            values.append(Path(path).read_text(encoding="utf-8", errors="replace"))
    return "\n".join(values)


def validate_help(result: dict[str, object], literals: list[str]) -> dict[str, object]:
    process_summary(result, expected_exit=0)
    output = output_text(result)
    missing = [literal for literal in literals if literal not in output]
    if missing:
        raise ReplayFailure(f"{result.get('label')} omitted help literals: {missing}")
    return {"required_literals": literals, "matched_literals": literals}


def validate_transcript(path: Path, label: str) -> None:
    if not path.is_file():
        raise ReplayFailure(f"{label} transcript is missing")
    ticks: list[int] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        try:
            item = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ReplayFailure(f"{label} transcript line {line_number} is not JSON: {exc}") from exc
        if not isinstance(item, dict) or isinstance(item.get("tick"), bool) or not isinstance(item.get("tick"), int):
            raise ReplayFailure(f"{label} transcript line {line_number} has no integer tick")
        ticks.append(item["tick"])
    if ticks != list(range(1, len(ticks) + 1)):
        raise ReplayFailure(f"{label} transcript ticks are not contiguous: {ticks[:4]}...{ticks[-4:]}")


def validate_session_log(path: Path) -> None:
    if not path.is_file():
        raise ReplayFailure("replay session-log.jsonl is missing")
    rows = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(rows) != 1 or not isinstance(rows[0], dict):
        raise ReplayFailure(f"replay session log has {len(rows)} turns, expected one")
    turn = rows[0]
    expected_input = {"text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}
    expected_response = {
        "text": "strict replay continuation",
        "complete": True,
        "audio_offset_bytes": 0,
        "audio_bytes": 4800,
        "audio_segments": ["audio/out-000.pcm"],
    }
    if turn.get("turn_index") != 1 or turn.get("input") != expected_input or turn.get("response") != expected_response:
        raise ReplayFailure("replay session log input/response changed")
    expected_command = 'echo PROBE_TOOL_MARKER_9182 >> "evidence/runs/exec-invocations-v4.log"; echo PROBE_TOOL_MARKER_9182'
    events = turn.get("tool_events")
    if not isinstance(events, list) or len(events) != 2:
        raise ReplayFailure("replay session log tool lifecycle is missing")
    metadata = [{key: event.get(key) for key in ("sequence", "type", "tool_call_id", "tool_name", "status", "content")} for event in events]
    if metadata != [
        {"sequence": 1, "type": "tool_call", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": None, "content": None},
        {"sequence": 2, "type": "tool_result", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": "completed", "content": "PROBE_TOOL_MARKER_9182\n"},
    ]:
        raise ReplayFailure(f"replay tool lifecycle changed: {metadata!r}")
    try:
        arguments = json.loads(events[0]["arguments"])
    except (KeyError, TypeError, json.JSONDecodeError) as exc:
        raise ReplayFailure("replay tool call arguments are not valid JSON") from exc
    if arguments != {"command": expected_command}:
        raise ReplayFailure(f"replay tool command changed: {arguments!r}")


def validate_recorded_output(case_dir: Path, fixture: Path) -> dict[str, object]:
    record_dir = case_dir / "tool-record"
    manifest_path = record_dir / "manifest.json"
    pcm_path = record_dir / "audio" / "out-000.pcm"
    audio_out = case_dir / "audio.wav"
    for path in (manifest_path, pcm_path, audio_out):
        if not path.is_file():
            raise ReplayFailure(f"replay artifact is missing: {path}")
    manifest = load_json(manifest_path)
    if not isinstance(manifest, dict) or manifest.get("terminal") != EXPECTED_TERMINAL:
        raise ReplayFailure(f"replay terminal manifest changed: {manifest.get('terminal') if isinstance(manifest, dict) else manifest!r}")
    artifacts = manifest.get("artifacts")
    artifact_hashes = {item.get("path"): item.get("sha256") for item in artifacts if isinstance(item, dict)} if isinstance(artifacts, list) else {}
    if set(artifact_hashes) != EXPECTED_ARTIFACTS:
        raise ReplayFailure(f"replay manifest artifact set changed: {sorted(artifact_hashes)}")
    for relative in EXPECTED_ARTIFACTS:
        path = record_dir / relative
        if not path.is_file() or artifact_hashes[relative] != sha256(path):
            raise ReplayFailure(f"replay manifest hash mismatch: {relative}")
    validate_transcript(record_dir / "client.transcript.jsonl", "client")
    validate_transcript(record_dir / "agent.transcript.jsonl", "agent")
    validate_session_log(record_dir / "session-log.jsonl")
    if sha256(record_dir / "provider.json") != sha256(fixture):
        raise ReplayFailure("replay provider fixture bytes changed")
    if pcm_path.stat().st_size != 4800 or sha256(pcm_path) != EXPECTED_PCM_SHA256:
        raise ReplayFailure(f"replay PCM changed: bytes={pcm_path.stat().st_size}, sha256={sha256(pcm_path)}")
    if audio_out.stat().st_size <= 44:
        raise ReplayFailure("replay audio-out WAV is empty")
    marker = case_dir / "evidence/runs/exec-invocations-v4.log"
    if not marker.is_file() or EXPECTED_MARKER not in marker.read_text(encoding="utf-8", errors="replace"):
        raise ReplayFailure("replay tool marker file is missing or changed")
    return {
        "record_dir": relative_or_absolute(record_dir),
        "manifest": relative_or_absolute(manifest_path),
        "manifest_sha256": sha256(manifest_path),
        "pcm": relative_or_absolute(pcm_path),
        "pcm_bytes": pcm_path.stat().st_size,
        "pcm_sha256": sha256(pcm_path),
        "audio_out": relative_or_absolute(audio_out),
        "audio_out_bytes": audio_out.stat().st_size,
        "audio_out_sha256": sha256(audio_out),
        "session_log": relative_or_absolute(record_dir / "session-log.jsonl"),
        "session_log_sha256": sha256(record_dir / "session-log.jsonl"),
        "provider_sha256": sha256(record_dir / "provider.json"),
        "terminal": manifest["terminal"],
        "marker": relative_or_absolute(marker),
        "marker_sha256": sha256(marker),
        "artifact_paths": sorted(EXPECTED_ARTIFACTS),
    }


def copy_config(destination: Path) -> Path:
    target = destination / "config"
    shutil.copytree(CONFIG, target)
    return target


def replay_command(config_dir: Path, fixture: Path, case_dir: Path, record_dir: Path, audio_out: Path) -> list[str]:
    return [
        str(ARTIFACT),
        "-C",
        str(config_dir),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(audio_out),
        "--record-dir",
        str(record_dir),
        "--trace-audio",
        "--workdir",
        str(case_dir),
        "--allow-path",
        str(case_dir),
    ]


def git_revision() -> str:
    result = subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise ReplayFailure(f"candidate revision is unavailable: {result.stderr.strip()}")
    return result.stdout.strip()


def run_replay(child_timeout: float, aggregate_timeout: float) -> dict[str, object]:
    if not FACTORY_ROOT.is_dir():
        raise ReplayFailure("FACTORY_ROOT is unavailable")
    for path, expected in (
        (ARTIFACT, EXPECTED_ARTIFACT_SHA256),
        (SOURCE_REPORT, EXPECTED_REPORT_SHA256),
        (CAPTURE, EXPECTED_CAPTURE_SHA256),
        (CONFIG / "config.yaml", EXPECTED_CONFIG_SHA256),
        (CONFIG / "models.yaml", EXPECTED_MODELS_SHA256),
    ):
        require_file(path, expected)
    source_report = load_json(SOURCE_REPORT)
    if not isinstance(source_report, dict) or source_report.get("decision") != "FAILED":
        raise ReplayFailure("immutable source report is not the preserved FAILED report")
    fixture = load_json(CAPTURE)
    if not isinstance(fixture, dict) or not isinstance(fixture.get("records"), list):
        raise ReplayFailure("C16 replay fixture is malformed")
    run_id = "replay-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + f"-{os.getpid()}"
    run_dir = HERE / "runs" / run_id
    if run_dir.exists():
        raise ReplayFailure(f"replay run directory already exists: {run_dir}")
    run_dir.mkdir(parents=True)
    env = sanitized_environment(run_dir)
    started = time.monotonic()
    help_root = run_process("artifact-help", [str(ARTIFACT), "--help"], ROOT, env, run_dir / "help-root", child_timeout)
    help_session = run_process("artifact-session-help", [str(ARTIFACT), "-C", str(copy_config(run_dir / "help-session")), "session", "--help"], ROOT, env, run_dir / "help-session-process", child_timeout)
    help_evidence = {
        "root": validate_help(help_root, ["Available Commands:", "session", "--workdir", "--allow-path"]),
        "session": validate_help(help_session, ["Usage:", "--replay", "--record-dir", "--trace-audio"]),
    }
    if time.monotonic() - started >= aggregate_timeout:
        raise ReplayFailure("aggregate replay deadline exceeded before positive replay")

    positive_dir = run_dir / "positive"
    positive_config = copy_config(positive_dir)
    (positive_dir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    positive_record = positive_dir / "tool-record"
    positive_audio = positive_dir / "audio.wav"
    positive_process = run_process(
        "artifact-positive-replay",
        replay_command(positive_config, CAPTURE, positive_dir, positive_record, positive_audio),
        positive_dir,
        env,
        positive_dir / "process",
        child_timeout,
    )
    process_summary(positive_process, expected_exit=0)
    positive_output = output_text(positive_process)
    if EXPECTED_MARKER not in positive_output or EXPECTED_CONTINUATION not in positive_output:
        raise ReplayFailure("positive replay did not preserve marker and continuation output")
    positive_artifacts = validate_recorded_output(positive_dir, CAPTURE)
    if positive_artifacts["session_log_sha256"] != EXPECTED_SESSION_LOG_SHA256:
        raise ReplayFailure("positive replay session-log hash changed")

    if time.monotonic() - started >= aggregate_timeout:
        raise ReplayFailure("aggregate replay deadline exceeded before negative replay")
    negative_dir = run_dir / "negative"
    negative_config = copy_config(negative_dir)
    (negative_dir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    negative_fixture = negative_dir / "mutated.session.json"
    mutated_fixture = copy.deepcopy(fixture)
    records = mutated_fixture.get("records")
    if not isinstance(records, list) or len(records) < 2 or not isinstance(records[1], dict):
        raise ReplayFailure("C16 fixture has no mutable order record for the negative control")
    records[1]["sequence"] = 999
    write_json(negative_fixture, mutated_fixture)
    negative_process = run_process(
        "artifact-mutated-fixture-rejection",
        replay_command(negative_config, negative_fixture, negative_dir, negative_dir / "tool-record", negative_dir / "audio.wav"),
        negative_dir,
        env,
        negative_dir / "process",
        child_timeout,
    )
    process_summary(negative_process)
    if not isinstance(negative_process.get("exit_code"), int) or negative_process.get("exit_code") == 0:
        raise ReplayFailure("mutated replay fixture was unexpectedly accepted")
    negative_output = output_text(negative_process).lower()
    if "sequence" not in negative_output and "fixture" not in negative_output and "replay" not in negative_output:
        raise ReplayFailure("mutated replay rejection did not identify the fixture/order failure")

    elapsed = time.monotonic() - started
    if elapsed >= aggregate_timeout:
        raise ReplayFailure(f"aggregate replay deadline exceeded: {elapsed:.3f}s >= {aggregate_timeout:.3f}s")
    return {
        "schema": "audio-runtime-c60-software-file-replay-v1",
        "passed": True,
        "candidate_revision": git_revision(),
        "observed_at": datetime.now(timezone.utc).isoformat(),
        "run_id": run_id,
        "aggregate_timeout_seconds": aggregate_timeout,
        "aggregate_elapsed_seconds": round(elapsed, 6),
        "aggregate_deadline_met": elapsed < aggregate_timeout,
        "child_timeout_seconds": child_timeout,
        "inputs": {
            "immutable_source_report": {"path": relative_or_absolute(SOURCE_REPORT), "sha256": sha256(SOURCE_REPORT), "decision": source_report.get("decision")},
            "artifact": {"path": relative_or_absolute(ARTIFACT), "sha256": sha256(ARTIFACT), "bytes": ARTIFACT.stat().st_size, "identity": "immutable staged artifact-0"},
            "capture": {"path": relative_or_absolute(CAPTURE), "sha256": sha256(CAPTURE)},
            "config": {"path": relative_or_absolute(CONFIG / "config.yaml"), "sha256": sha256(CONFIG / "config.yaml")},
            "models": {"path": relative_or_absolute(CONFIG / "models.yaml"), "sha256": sha256(CONFIG / "models.yaml")},
        },
        "environment": {
            "credential_free": True,
            "forwarded_keys": sorted(env),
            "removed_secret_keys": list(SECRET_KEYS),
            "realtime_network": "not used; staged artifact replayed an offline file fixture",
        },
        "help": help_evidence,
        "positive": {"process": process_summary(positive_process, expected_exit=0), "artifacts": positive_artifacts, "stdout_contains": [EXPECTED_MARKER, EXPECTED_CONTINUATION]},
        "negative": {
            "fixture": relative_or_absolute(negative_fixture),
            "mutation": {"records[1].sequence": 999, "purpose": "real replay fixture order/digest rejection"},
            "process": process_summary(negative_process),
            "rejected": True,
        },
        "limitations": [
            "This is software/file replay evidence only; it does not prove native-device or acoustic behavior.",
            "No live Realtime provider or credential was used.",
        ],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("all",), default="all")
    parser.add_argument("--child-timeout", type=float, default=59.0)
    parser.add_argument("--aggregate-timeout", type=float, default=1800.0)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout >= 60:
        raise SystemExit("--child-timeout must be >0 and <60 seconds")
    if args.aggregate_timeout <= 0 or args.aggregate_timeout > 1800:
        raise SystemExit("--aggregate-timeout must be >0 and <=1800 seconds")
    try:
        report = run_replay(args.child_timeout, args.aggregate_timeout)
        write_json(HERE / "replay-report.json", report)
        print(json.dumps({"status": "PASS", "report": "replay-report.json", "run_id": report["run_id"], "elapsed_seconds": report["aggregate_elapsed_seconds"]}, sort_keys=True))
        return 0
    except (ReplayFailure, OSError, subprocess.SubprocessError) as exc:
        print(json.dumps({"status": "FAIL", "error": str(exc)}, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
