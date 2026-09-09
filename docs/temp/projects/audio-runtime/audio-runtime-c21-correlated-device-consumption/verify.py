#!/usr/bin/env python3
"""Bounded C21 evidence runner for the public consumer and C16 parity controls."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
ARTIFACTS = HERE / "artifacts"
FIXTURES = HERE / "fixtures"
RUNS = HERE / "runs"
CONSUMER_SOURCE = HERE / "consumer" / "main.go"
CONSUMER = ARTIFACTS / "consumption-consumer"
YUI = ARTIFACTS / "yui"
DEADLINE_SECONDS = 60

PARITY_EXPECTATIONS: dict[str, dict[str, Any]] = {
    "audio-tool": {
        "pcm_bytes": 4800,
        "pcm_sha256": "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502",
        "terminal": {
            "reason": "fixture_complete",
            "classification": "provider_close",
            "terminal_reason": "provider_close",
            "terminal_provenance": "provider",
            "output_state": "not_applicable",
        },
        "fixture_types": [
            "session.update",
            "session.created",
            "conversation.item.create",
            "response.create",
            "response.created",
            "response.output_item.added",
            "response.function_call_arguments.done",
            "response.done",
            "conversation.item.create",
            "response.create",
            "response.created",
            "response.output_audio.delta",
            "response.output_audio.delta",
            "response.output_audio.done",
            "response.output_text.delta",
            "response.output_text.done",
            "response.done",
            "session.closed",
        ],
    },
    "interruption": {
        "pcm_bytes": 3840,
        "pcm_sha256": "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22",
        "healthy_tail_offset_bytes": 1440,
        "healthy_tail_bytes": 2400,
        "healthy_tail_sha256": "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf",
        "terminal": {
            "reason": "replay_complete",
            "classification": "replay_complete",
            "terminal_reason": "replay_complete",
            "terminal_provenance": "replay",
            "output_state": "complete",
        },
        "fixture_types": [
            "session.update",
            "session.created",
            "conversation.item.create",
            "response.create",
            "response.created",
            "response.output_audio.delta",
            "input_audio_buffer.speech_started",
            "conversation.item.truncate",
            "conversation.item.truncated",
            "response.output_audio.done",
            "response.done",
            "response.created",
            "response.output_audio.delta",
            "response.output_audio.done",
            "response.done",
        ],
    },
}


class EvidenceFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "HOME", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL")
    return {key: env.get(key, "") for key in keys}


def run_process(label: str, argv: list[str], cwd: pathlib.Path, run_dir: pathlib.Path, env: dict[str, str] | None = None) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if env is None else env)
    started = time.monotonic()
    timed_out = False
    exit_code: int | None = None
    stdout = b""
    stderr = b""
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=DEADLINE_SECONDS)
        exit_code = process.returncode
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        os.killpg(process.pid, signal.SIGTERM)
        try:
            tail_out, tail_err = process.communicate(timeout=2)
            stdout += tail_out
            stderr += tail_err
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            tail_out, tail_err = process.communicate()
            stdout += tail_out
            stderr += tail_err
    elapsed = time.monotonic() - started
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": DEADLINE_SECONDS,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": exit_code,
        "timed_out": timed_out,
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    record_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_ok(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def build_artifacts(run_dir: pathlib.Path, consumer: bool, yui: bool) -> list[dict[str, Any]]:
    results = []
    if consumer:
        result = run_process(
            "build-consumer",
            ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-o", str(CONSUMER), str(CONSUMER_SOURCE.relative_to(ROOT))],
            ROOT,
            run_dir,
        )
        require_ok(result)
        results.append(result)
    if yui:
        result = run_process(
            "build-yui",
            ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-o", str(YUI), "./agent-cli/cmd/yui"],
            ROOT,
            run_dir,
        )
        require_ok(result)
        results.append(result)
    return results


def load_json(path: pathlib.Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    if not path.exists():
        raise EvidenceFailure(f"missing JSONL artifact: {path}")
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            record = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL at {path}:{line_number}: {exc}") from exc
        if not isinstance(record, dict):
            raise EvidenceFailure(f"non-object JSONL record at {path}:{line_number}")
        records.append(record)
    if not records:
        raise EvidenceFailure(f"empty JSONL artifact: {path}")
    return records


def validate_transcript(path: pathlib.Path, label: str) -> None:
    records = load_jsonl(path)
    ticks = [record.get("tick") for record in records]
    if any(not isinstance(tick, int) for tick in ticks) or ticks != list(range(1, len(ticks) + 1)):
        raise EvidenceFailure(f"{label} transcript order is not contiguous: {ticks[:5]}...{ticks[-5:]}")


def validate_session_log(label: str, path: pathlib.Path, expect_tool: bool) -> None:
    turns = load_jsonl(path)
    if expect_tool:
        expected = [
            {
                "turn_index": 1,
                "input": {"text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False},
                "response": {"text": "strict replay continuation", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 4800, "audio_segments": ["audio/out-000.pcm"]},
            }
        ]
    else:
        expected = [
            {
                "turn_index": 1,
                "input": {"text": "c07 interruption", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False},
                "response": {"text": "", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 1440, "audio_segments": ["audio/out-000.pcm"]},
            },
            {
                "turn_index": 2,
                "input": {"text": "", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False},
                "response": {"text": "", "complete": True, "audio_offset_bytes": 1440, "audio_bytes": 2400, "audio_segments": ["audio/out-000.pcm"]},
            },
        ]
    if len(turns) != len(expected):
        raise EvidenceFailure(f"{label} session-log turn count = {len(turns)}, want {len(expected)}")
    for actual, want in zip(turns, expected):
        for key, value in want.items():
            if actual.get(key) != value:
                raise EvidenceFailure(f"{label} session-log {key} changed: {actual.get(key)!r}")
    if expect_tool:
        tool_events = turns[0].get("tool_events")
        if not isinstance(tool_events, list) or len(tool_events) != 2:
            raise EvidenceFailure(f"{label} tool event sequence missing: {tool_events!r}")
        metadata = [
            {key: event.get(key) for key in ("sequence", "type", "tool_call_id", "tool_name", "status", "content")}
            for event in tool_events
        ]
        if metadata != [
            {"sequence": 1, "type": "tool_call", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": None, "content": None},
            {"sequence": 2, "type": "tool_result", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": "completed", "content": "PROBE_TOOL_MARKER_9182\n"},
        ]:
            raise EvidenceFailure(f"{label} tool event order/content changed: {metadata!r}")
        try:
            arguments = json.loads(tool_events[0]["arguments"])
        except (KeyError, TypeError, json.JSONDecodeError) as exc:
            raise EvidenceFailure(f"{label} tool call arguments are not valid JSON") from exc
        expected_command = 'echo PROBE_TOOL_MARKER_9182 >> "evidence/runs/exec-invocations-v4.log"; echo PROBE_TOOL_MARKER_9182'
        if arguments != {"command": expected_command}:
            raise EvidenceFailure(f"{label} tool command changed: {arguments!r}")
    elif any(turn.get("tool_events") is not None for turn in turns):
        raise EvidenceFailure(f"{label} interruption unexpectedly contains tool events")


def validate_parity_artifacts(label: str, fixture: pathlib.Path, record_dir: pathlib.Path, pcm: pathlib.Path, manifest: pathlib.Path, expect_tool: bool) -> dict[str, Any]:
    expectation = PARITY_EXPECTATIONS[label]
    manifest_data = load_json(manifest)
    if manifest_data.get("terminal") != expectation["terminal"]:
        raise EvidenceFailure(f"{label} terminal state changed: {manifest_data.get('terminal')!r}")
    artifacts = {artifact.get("path"): artifact.get("sha256") for artifact in manifest_data.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    if set(artifacts) != expected_paths:
        raise EvidenceFailure(f"{label} manifest artifact set changed: {sorted(artifacts)}")
    for relative in expected_paths:
        artifact_path = record_dir / relative
        if not artifact_path.is_file() or artifacts[relative] != sha256(artifact_path):
            raise EvidenceFailure(f"{label} manifest hash does not match {relative}")
    validate_transcript(record_dir / "client.transcript.jsonl", f"{label} client")
    validate_transcript(record_dir / "agent.transcript.jsonl", f"{label} agent")
    validate_session_log(label, record_dir / "session-log.jsonl", expect_tool)
    provider = record_dir / "provider.json"
    if sha256(provider) != sha256(fixture) or [record.get("type") for record in load_json(fixture).get("records", [])] != expectation["fixture_types"]:
        raise EvidenceFailure(f"{label} provider fixture order or bytes changed")
    pcm_bytes = pcm.read_bytes()
    if len(pcm_bytes) != expectation["pcm_bytes"] or sha256(pcm) != expectation["pcm_sha256"]:
        raise EvidenceFailure(f"{label} exact PCM parity changed: bytes={len(pcm_bytes)} sha256={sha256(pcm)}")
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        offset = expectation["healthy_tail_offset_bytes"]
        tail = pcm_bytes[offset:]
        healthy_tail = {"offset_bytes": offset, "bytes": len(tail), "sha256": hashlib.sha256(tail).hexdigest()}
        if healthy_tail["bytes"] != expectation["healthy_tail_bytes"] or healthy_tail["sha256"] != expectation["healthy_tail_sha256"]:
            raise EvidenceFailure(f"{label} healthy tail parity changed: {healthy_tail}")
    return {"terminal": manifest_data["terminal"], "session_log": str(record_dir / "session-log.jsonl"), "healthy_tail": healthy_tail}


def validate_consumer(run_dir: pathlib.Path) -> dict[str, Any]:
    result = run_process("run-public-consumer", ["rtk", "proxy", str(CONSUMER)], ROOT, run_dir)
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        observed = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"public consumer did not emit JSON: {exc}") from exc
    (run_dir / "consumer-observed.json").write_text(json.dumps(observed, indent=2) + "\n", encoding="utf-8")
    expected = load_json(FIXTURES / "expected.json")
    if observed["paused_device_samples"] != expected["paused_device_samples"]:
        raise EvidenceFailure("public consumer fabricated consumption while callbacks were paused")
    if observed["device_rate"] != expected["device_rate"]:
        raise EvidenceFailure("public consumer reported the wrong device rate")
    kinds = [event["kind"] for event in observed["observations"]]
    if kinds != expected["primary_kinds"]:
        raise EvidenceFailure(f"primary observation kind sequence changed: {kinds}")
    consumed = [
        {"item_id": event.get("item_id", ""), "start": event["start_sample"], "end": event["end_sample"], "pcm": event.get("pcm", [])}
        for event in observed["observations"]
        if event["kind"] == "consumed"
    ]
    if consumed != expected["primary_consumed"]:
        raise EvidenceFailure("response-correlated consumed PCM/ranges changed")
    underflows = [
        {"start": event["start_sample"], "end": event["end_sample"], "pcm": event.get("pcm", [])}
        for event in observed["observations"]
        if event["kind"] == "underflow"
    ]
    if underflows != expected["underflow_ranges"]:
        raise EvidenceFailure("underflow ranges or zero-filled PCM changed")
    discard = next(event for event in observed["observations"] if event["kind"] == "discard")
    if {"item_id": discard["item_id"], "start": discard["start_sample"], "end": discard["end_sample"], "count": discard["sample_count"]} != expected["discard"]:
        raise EvidenceFailure("interruption discard receipt changed")
    if observed["resampled_pcm"] != expected["rate_pcm"]:
        raise EvidenceFailure("canonical 24 kHz to device-rate PCM changed")
    stalled = observed["stalled_stats"]
    stalled_expectation = expected["stalled"]
    stalled_actual = {
        "device_samples": stalled["DeviceSamples"],
        "published_observations": stalled["PublishedObservations"],
        "dropped_observations": stalled["DroppedObservations"],
        "dropped_samples": stalled["DroppedSamples"],
        "metadata_lost_samples": stalled["MetadataLostSamples"],
        "last_sequence": stalled["LastSequence"],
    }
    if stalled_actual != stalled_expectation or not stalled["Closed"]:
        raise EvidenceFailure(f"stalled-consumer accounting changed: {stalled}")
    if not observed["close_eof"] or not observed["primary_stats"]["Closed"]:
        raise EvidenceFailure("primary observation stream did not close and drain cleanly")
    return observed


def parity_run(label: str, fixture: pathlib.Path, run_dir: pathlib.Path, expect_tool: bool) -> dict[str, Any]:
    case_dir = run_dir / f"parity-{label}"
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record_dir = case_dir / "tool-record"
    audio_out = case_dir / "audio.wav"
    result = run_process(
        f"yui-{label}",
        [
            "rtk",
            "proxy",
            str(YUI),
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
        ],
        case_dir,
        run_dir,
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
    if expect_tool:
        marker = case_dir / "evidence" / "runs" / "exec-invocations-v4.log"
        if "PROBE_TOOL_MARKER_9182" not in stdout or "strict replay continuation" not in stdout or not marker.exists():
            raise EvidenceFailure(f"{label} tool replay did not preserve marker/continuation evidence")
    else:
        if "replay mismatch" in stdout.lower():
            raise EvidenceFailure(f"{label} interruption replay reported a mismatch")
    manifest = record_dir / "manifest.json"
    pcm = record_dir / "audio" / "out-000.pcm"
    if not manifest.exists() or not pcm.exists() or pcm.stat().st_size == 0:
        raise EvidenceFailure(f"{label} replay did not produce a complete recording")
    parity = validate_parity_artifacts(label, fixture, record_dir, pcm, manifest, expect_tool)
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
        "audio_out_sha256": sha256(audio_out) if audio_out.exists() else "",
        "terminal": parity["terminal"],
        "session_log": parity["session_log"],
        "healthy_tail": parity["healthy_tail"],
    }
    (case_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    return summary


def run_parity(run_dir: pathlib.Path) -> list[dict[str, Any]]:
    return [
        parity_run("audio-tool", FIXTURES / "c16-audio-tool.session.json", run_dir, True),
        parity_run("interruption", FIXTURES / "c16-interruption.session.json", run_dir, False),
    ]


def require_test_events(result: dict[str, Any], expected_prefixes: tuple[str, ...]) -> None:
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
    test_names: set[str] = set()
    for line in stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        test_name = event.get("Test")
        if isinstance(test_name, str) and test_name:
            test_names.add(test_name)
    if not any(any(name.startswith(prefix) for prefix in expected_prefixes) for name in test_names):
        raise EvidenceFailure(f"{result['label']} matched no expected tests; observed {sorted(test_names)!r}; see {result['stdout_path']}")


def run_controls(run_dir: pathlib.Path) -> list[dict[str, Any]]:
    commands = [
        ("c21-focused", ["rtk", "proxy", "go", "test", "-json", "-tags=nomicrophone", "./go-device-gateway/pkg/runtime", "-run", "^TestC21", "-count=1", "-timeout=45s"], ("TestC21",)),
        ("c21-race", ["rtk", "proxy", "go", "test", "-json", "-race", "-tags=nomicrophone", "./go-device-gateway/pkg/runtime", "-run", "^TestC21", "-count=1", "-timeout=45s"], ("TestC21",)),
        ("runtime-regressions", ["rtk", "proxy", "go", "test", "-json", "-tags=nomicrophone", "./go-device-gateway/pkg/runtime", "-run", "TestRTCDeviceSink|TestPlaybackBufferSnapshot", "-count=1", "-timeout=45s"], ("TestRTCDeviceSink", "TestPlaybackBufferSnapshot")),
        ("device-sink-regressions", ["rtk", "proxy", "go", "test", "-json", "-tags=nomicrophone", "./go-device-gateway/pkg/devices", "-run", "TestDeviceSink", "-count=1", "-timeout=45s"], ("TestDeviceSink",)),
        ("runtime-vet", ["rtk", "proxy", "go", "vet", "-tags=nomicrophone", "./go-device-gateway/pkg/runtime"], None),
        ("diff-check", ["rtk", "proxy", "git", "diff", "--check"], None),
    ]
    results = []
    for label, argv, expected_prefixes in commands:
        result = run_process(label, argv, ROOT, run_dir)
        require_ok(result)
        if expected_prefixes is not None:
            require_test_events(result, expected_prefixes)
        results.append(result)
    return results


def provenance() -> dict[str, Any]:
    git_head = subprocess.check_output(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    return {
        "source_revision": git_head,
        "consumer_source_sha256": sha256(CONSUMER_SOURCE),
        "consumer_binary_sha256": sha256(CONSUMER) if CONSUMER.exists() else "",
        "yui_binary_sha256": sha256(YUI) if YUI.exists() else "",
        "fixture_sha256": {
            path.name: sha256(path)
            for path in sorted(FIXTURES.iterdir())
            if path.is_file()
        },
        "go_work": str(ROOT / "go.work"),
        "toolchain": os.environ.get("GOTOOLCHAIN", ""),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("all", "public-consumer", "parity"), default="all")
    args = parser.parse_args()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = RUNS / f"verify-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    outcome: dict[str, Any] = {"mode": args.mode, "run_dir": str(run_dir), "steps": [], "decision": "FAILED"}
    try:
        if args.mode in ("all", "public-consumer"):
            outcome["steps"].extend(build_artifacts(run_dir, consumer=True, yui=args.mode == "all"))
            outcome["consumer"] = validate_consumer(run_dir)
        if args.mode == "all":
            outcome["controls"] = run_controls(run_dir)
            outcome["parity"] = run_parity(run_dir)
        elif args.mode == "parity":
            outcome["steps"].extend(build_artifacts(run_dir, consumer=False, yui=True))
            outcome["parity"] = run_parity(run_dir)
        outcome["provenance"] = provenance()
        outcome["decision"] = "ACCEPTED"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        outcome["provenance"] = provenance() if CONSUMER.exists() else {}
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    (HERE / "latest-outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
