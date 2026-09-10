#!/usr/bin/env python3
"""Bounded public timing, parity, cleanup, and provenance verification for C37."""

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
C21_FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
DEFAULT_CONSUMER_SOURCE = HERE / "cmd/transcript-clocks/main.go"
DEFAULT_CONSUMER = HERE / "artifacts/transcript-clocks"
DEFAULT_YUI = HERE / "artifacts/yui"
DEFAULT_RUNS = HERE / "runs"

BASE_TIMESTAMP = "2026-01-02T03:04:05Z"
ADVANCED_TIMESTAMP = "2026-01-02T03:04:05.02Z"
FIXTURE_EXPECTATIONS: dict[str, dict[str, Any]] = {
    "audio-tool": {
        "fixture": "c16-audio-tool.session.json",
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
        "fixture": "c16-interruption.session.json",
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


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "GOWORK", "GOFLAGS", "GOTOOLCHAIN", "FACTORY_ROOT")
    return {key: env.get(key, "") for key in keys}


def process_group_pids(pgid: int) -> list[int]:
    try:
        completed = subprocess.run(
            ["ps", "-eo", "pid=,pgid="],
            check=True,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError):
        return []
    pids: list[int] = []
    for line in completed.stdout.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1].isdigit() and int(fields[1]) == pgid and fields[0].isdigit():
            pids.append(int(fields[0]))
    return pids


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    timeout_seconds: float,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if env is None else env)
    started = time.monotonic()
    timed_out = False
    term_sent = False
    kill_sent = False
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
    pgid = os.getpgid(process.pid)
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        os.killpg(pgid, signal.SIGTERM)
        term_sent = True
        try:
            tail_out, tail_err = process.communicate(timeout=2)
            stdout += tail_out
            stderr += tail_err
        except subprocess.TimeoutExpired:
            os.killpg(pgid, signal.SIGKILL)
            kill_sent = True
            tail_out, tail_err = process.communicate()
            stdout += tail_out
            stderr += tail_err
    survivors_before_final_kill = process_group_pids(pgid)
    if survivors_before_final_kill:
        try:
            os.killpg(pgid, signal.SIGKILL)
            kill_sent = True
        except ProcessLookupError:
            pass
        if process.poll() is None:
            tail_out, tail_err = process.communicate()
            stdout += tail_out
            stderr += tail_err
        time.sleep(0.05)
    elapsed = time.monotonic() - started
    survivors = process_group_pids(pgid)
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": timeout_seconds,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "parent_reaped": process.returncode is not None,
        "surviving_process_group_pids": survivors,
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    record_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_ok(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")
    if not result["parent_reaped"] or result["surviving_process_group_pids"]:
        raise EvidenceFailure(f"{result['label']} did not shut down cleanly: {result}")


def read_json_output(result: dict[str, Any]) -> dict[str, Any]:
    output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        decoded = json.loads(output)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"{result['label']} did not emit one JSON object: {exc}") from exc
    if not isinstance(decoded, dict):
        raise EvidenceFailure(f"{result['label']} emitted {type(decoded).__name__}, want object")
    return decoded


def run_consumer_action(action: str, consumer: pathlib.Path, run_dir: pathlib.Path, timeout_seconds: float) -> tuple[dict[str, Any], dict[str, Any] | None]:
    result = run_process(
        f"consumer-{action}",
        [str(consumer), "--action", action],
        ROOT,
        run_dir,
        timeout_seconds,
    )
    if action == "hang-control":
        if not result["timed_out"] or not result["term_sent"] or not result["parent_reaped"]:
            raise EvidenceFailure(f"hang-control did not hit the bounded timeout/cleanup path: {result}")
        if result["surviving_process_group_pids"]:
            raise EvidenceFailure(f"hang-control left descendants: {result}")
        return result, None
    require_ok(result)
    return result, read_json_output(result)


def verify_timing(output: dict[str, Any]) -> None:
    if output.get("action") != "timing" or output.get("ok") is not True:
        raise EvidenceFailure(f"timing positive output is not accepted: {output}")
    observations = output.get("observations")
    if observations != [
        {"side": "client", "tick": 0, "timestamp": BASE_TIMESTAMP, "peer": "client", "direction": "in", "stream": "device-in", "payload_hex": "00ff01"},
        {"side": "client", "tick": 1, "timestamp": ADVANCED_TIMESTAMP, "peer": "client", "direction": "out", "stream": "ws", "payload_hex": "7f80"},
        {"side": "agent", "tick": 0, "timestamp": BASE_TIMESTAMP, "peer": "agent", "direction": "in", "stream": "ws", "payload_hex": "1000"},
        {"side": "agent", "tick": 1, "timestamp": ADVANCED_TIMESTAMP, "peer": "agent", "direction": "out", "stream": "ws", "payload_hex": "01fe"},
    ]:
        raise EvidenceFailure(f"timing observations changed: {observations!r}")
    if output.get("advance_delta") != "20ms":
        raise EvidenceFailure(f"timing advance delta changed: {output.get('advance_delta')!r}")
    default = output.get("default_controls", {})
    if default.get("client_tick") != 0 or default.get("agent_ticks") != [1, 2] or default.get("host_timestamp_utc") is not True:
        raise EvidenceFailure(f"default timing compatibility changed: {default!r}")
    now_only = output.get("now_only", {})
    if now_only != {"tick": 1, "timestamp": "2026-01-02T03:04:05.123Z"}:
        raise EvidenceFailure(f"Now-only timing compatibility changed: {now_only!r}")


def verify_parity(output: dict[str, Any]) -> None:
    if output.get("action") != "parity" or output.get("ok") is not True:
        raise EvidenceFailure(f"parity output is not accepted: {output}")
    expected = {
        "client_partial": {"accepted": 2, "payload_hex": "01ff"},
        "client_zero_record_count": 0,
        "client_rejected_record_count": 0,
        "client_close_calls": 1,
        "client_copy_payload_hex": "1100ee",
        "agent_partial_record_count": 0,
        "agent_complete_record_count": 1,
        "agent_reporter_calls": 1,
        "nil_sink_clock_reads": 0,
        "close_error_authoritative": True,
        "sink_failure_isolated": True,
    }
    for key, value in expected.items():
        if output.get(key) != value:
            raise EvidenceFailure(f"parity field {key} = {output.get(key)!r}, want {value!r}")


def verify_mutations(consumer: pathlib.Path, run_dir: pathlib.Path, timeout_seconds: float) -> list[dict[str, Any]]:
    results: list[dict[str, Any]] = []
    for action, marker in (("mutate-timestamp", "expected timestamp mismatch"), ("mutate-tick", "expected tick mismatch")):
        result = run_process(f"consumer-{action}", [str(consumer), "--action", action], ROOT, run_dir, timeout_seconds)
        if result["timed_out"] or result["exit_code"] == 0 or not result["parent_reaped"]:
            raise EvidenceFailure(f"{action} did not fail as an oracle mutation: {result}")
        stderr = pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8")
        if marker not in stderr or result["surviving_process_group_pids"]:
            raise EvidenceFailure(f"{action} failure was not causal/bounded: {result}")
        results.append(result)
    return results


def load_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"cannot read JSON {path}: {exc}") from exc


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    if not path.is_file():
        raise EvidenceFailure(f"missing JSONL artifact: {path}")
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL {path}:{line_number}: {exc}") from exc
        if not isinstance(value, dict):
            raise EvidenceFailure(f"non-object JSONL {path}:{line_number}")
        records.append(value)
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
        if len(turns) != 1:
            raise EvidenceFailure(f"{label} turn count = {len(turns)}, want 1")
        turn = turns[0]
        response = turn.get("response", {})
        if response.get("text") != "strict replay continuation" or response.get("complete") is not True or response.get("audio_bytes") != 4800:
            raise EvidenceFailure(f"{label} response oracle changed: {response!r}")
        events = turn.get("tool_events")
        if not isinstance(events, list) or len(events) != 2:
            raise EvidenceFailure(f"{label} tool events missing: {events!r}")
        if [event.get("type") for event in events] != ["tool_call", "tool_result"]:
            raise EvidenceFailure(f"{label} tool event order changed: {events!r}")
        if events[1].get("content") != "PROBE_TOOL_MARKER_9182\n":
            raise EvidenceFailure(f"{label} tool marker changed: {events!r}")
    else:
        if len(turns) != 2:
            raise EvidenceFailure(f"{label} turn count = {len(turns)}, want 2")
        first, second = turns
        if first.get("response", {}).get("audio_bytes") != 1440 or second.get("response", {}).get("audio_bytes") != 2400:
            raise EvidenceFailure(f"{label} interruption audio boundaries changed: {turns!r}")
        if any(turn.get("tool_events") is not None for turn in turns):
            raise EvidenceFailure(f"{label} interruption unexpectedly contains tool events")


def validate_parity_artifacts(label: str, fixture: pathlib.Path, case_dir: pathlib.Path) -> dict[str, Any]:
    expectation = FIXTURE_EXPECTATIONS[label]
    record_dir = case_dir / "tool-record"
    manifest = record_dir / "manifest.json"
    pcm = record_dir / "audio" / "out-000.pcm"
    if not manifest.is_file() or not pcm.is_file() or pcm.stat().st_size == 0:
        raise EvidenceFailure(f"{label} replay did not produce manifest and PCM")
    manifest_data = load_json(manifest)
    if manifest_data.get("terminal") != expectation["terminal"]:
        raise EvidenceFailure(f"{label} terminal state changed: {manifest_data.get('terminal')!r}")
    artifacts = {item.get("path"): item.get("sha256") for item in manifest_data.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    if set(artifacts) != expected_paths:
        raise EvidenceFailure(f"{label} manifest paths changed: {sorted(artifacts)}")
    for relative in expected_paths:
        path = record_dir / relative
        if not path.is_file() or artifacts[relative] != sha256(path):
            raise EvidenceFailure(f"{label} manifest hash mismatch for {relative}")
    validate_transcript(record_dir / "client.transcript.jsonl", f"{label} client")
    validate_transcript(record_dir / "agent.transcript.jsonl", f"{label} agent")
    validate_session_log(label, record_dir / "session-log.jsonl", label == "audio-tool")
    fixture_records = load_json(fixture).get("records", [])
    if sha256(record_dir / "provider.json") != sha256(fixture) or [record.get("type") for record in fixture_records] != expectation["fixture_types"]:
        raise EvidenceFailure(f"{label} provider fixture bytes/order changed")
    pcm_bytes = pcm.read_bytes()
    if len(pcm_bytes) != expectation["pcm_bytes"] or sha256(pcm) != expectation["pcm_sha256"]:
        raise EvidenceFailure(f"{label} PCM oracle changed: {len(pcm_bytes)} bytes / {sha256(pcm)}")
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        offset = expectation["healthy_tail_offset_bytes"]
        tail = pcm_bytes[offset:]
        healthy_tail = {"offset_bytes": offset, "bytes": len(tail), "sha256": sha256_bytes(tail)}
        if healthy_tail["bytes"] != expectation["healthy_tail_bytes"] or healthy_tail["sha256"] != expectation["healthy_tail_sha256"]:
            raise EvidenceFailure(f"{label} healthy tail oracle changed: {healthy_tail}")
    return {
        "fixture": str(fixture),
        "fixture_sha256": sha256(fixture),
        "manifest": str(manifest),
        "manifest_sha256": sha256(manifest),
        "pcm": str(pcm),
        "pcm_bytes": len(pcm_bytes),
        "pcm_sha256": sha256(pcm),
        "healthy_tail": healthy_tail,
        "terminal": manifest_data["terminal"],
    }


def run_parity(yui: pathlib.Path, run_dir: pathlib.Path, timeout_seconds: float, fixture_dir: pathlib.Path) -> list[dict[str, Any]]:
    results: list[dict[str, Any]] = []
    for label, expectation in FIXTURE_EXPECTATIONS.items():
        fixture = fixture_dir / expectation["fixture"]
        if not fixture.is_file():
            raise EvidenceFailure(f"required reviewed C21 fixture is absent: {fixture}")
        case_dir = run_dir / f"parity-{label}"
        (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
        record_dir = case_dir / "tool-record"
        audio_out = case_dir / "audio.wav"
        result = run_process(
            f"yui-{label}",
            [
                str(yui),
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
            timeout_seconds,
        )
        require_ok(result)
        stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
        if label == "audio-tool" and ("PROBE_TOOL_MARKER_9182" not in stdout or "strict replay continuation" not in stdout):
            raise EvidenceFailure("audio-tool replay lost the tool marker or strict continuation")
        if label == "interruption" and "replay mismatch" in stdout.lower():
            raise EvidenceFailure("interruption replay reported a mismatch")
        summary = validate_parity_artifacts(label, fixture, case_dir)
        summary.update({
            "label": label,
            "stdout_path": result["stdout_path"],
            "stderr_path": result["stderr_path"],
            "audio_out": str(audio_out),
            "audio_out_bytes": audio_out.stat().st_size if audio_out.is_file() else 0,
            "audio_out_sha256": sha256(audio_out) if audio_out.is_file() else "",
            "clean_shutdown": result["exit_code"] == 0 and result["parent_reaped"],
        })
        (case_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
        results.append(summary)
    return results


def command_output(argv: list[str], cwd: pathlib.Path) -> bytes:
    try:
        completed = subprocess.run(argv, cwd=str(cwd), check=True, capture_output=True, timeout=60)
    except (OSError, subprocess.SubprocessError) as exc:
        raise EvidenceFailure(f"command failed: {argv}: {exc}") from exc
    return completed.stdout


def git_value(*args: str) -> str:
    return command_output(["rtk", "proxy", "git", *args], ROOT).decode().strip()


def source_provenance(source_arg: str, consumer_source: pathlib.Path, consumer: pathlib.Path, yui: pathlib.Path | None, fixture_dir: pathlib.Path) -> dict[str, Any]:
    source_path = pathlib.Path(source_arg)
    source_descriptor: dict[str, Any] = {"argument": source_arg}
    if source_path.is_file():
        source_descriptor.update({"path": str(source_path), "sha256": sha256(source_path), "bytes": source_path.stat().st_size})
    elif source_path.is_dir():
        entries: list[dict[str, Any]] = []
        for path in sorted(source_path.rglob("*")):
            if path.is_file():
                entries.append({"path": str(path.relative_to(source_path)), "sha256": sha256(path), "bytes": path.stat().st_size})
        source_descriptor.update({"path": str(source_path), "files": entries, "tree_sha256": sha256_bytes(json.dumps(entries, sort_keys=True).encode())})
    else:
        source_descriptor["revision_or_label"] = source_arg
    source_files = [str(path.relative_to(ROOT)) for path in sorted((HERE / "cmd").rglob("*.go"))]
    input_manifest: dict[str, Any] = {
        "consumer_source_files": source_files,
        "consumer_source_sha256": sha256(consumer_source),
        "go_work_sha256": sha256(ROOT / "go.work"),
        "module_manifests": {
            str(path.relative_to(ROOT)): sha256(path)
            for path in (ROOT / "go-agent-loop/go.mod", ROOT / "go-audio/go.mod")
        },
        "toolchain": command_output(["rtk", "proxy", "go", "version"], ROOT).decode().strip(),
        "consumer_binary_sha256": sha256(consumer) if consumer.is_file() else "",
        "yui_binary_sha256": sha256(yui) if yui and yui.is_file() else "",
    }
    input_manifest["consumer_go_list_sha256"] = sha256_bytes(command_output(["rtk", "proxy", "go", "list", "-deps", "-json", *source_files], ROOT))
    if yui and yui.is_file():
        input_manifest["yui_go_list_sha256"] = sha256_bytes(command_output(["rtk", "proxy", "go", "list", "-deps", "-json", "./agent-cli/cmd/yui"], ROOT))
    fixture_hashes: dict[str, str] = {}
    for path in sorted(fixture_dir.iterdir()):
        if path.is_file():
            fixture_hashes[path.name] = sha256(path)
    return {
        "source_revision": git_value("rev-parse", "HEAD"),
        "source_archive": {
            "format": "tar",
            "sha256": sha256_bytes(command_output(["rtk", "proxy", "git", "archive", "--format=tar", "HEAD"], ROOT)),
        },
        "source_tree_dirty": bool(git_value("status", "--porcelain")),
        "source_descriptor": source_descriptor,
        "consumer_binary": str(consumer),
        "yui_binary": str(yui) if yui else "",
        "build_inputs": input_manifest,
        "fixture_dir": str(fixture_dir),
        "fixture_sha256": fixture_hashes,
    }


def build_consumer(consumer_source: pathlib.Path, consumer: pathlib.Path, run_dir: pathlib.Path, timeout_seconds: float) -> dict[str, Any]:
    result = run_process(
        "build-consumer",
        ["rtk", "proxy", "go", "build", "-o", str(consumer), str(consumer_source)],
        ROOT,
        run_dir,
        timeout_seconds,
    )
    require_ok(result)
    return result


def build_yui(yui: pathlib.Path, run_dir: pathlib.Path, timeout_seconds: float) -> dict[str, Any]:
    result = run_process(
        "build-yui",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-o", str(yui), "./agent-cli/cmd/yui"],
        ROOT,
        run_dir,
        timeout_seconds,
    )
    require_ok(result)
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--action", choices=("all", "timing", "mutations", "hang-control", "parity", "provenance"), default="all")
    parser.add_argument("--source", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--consumer-binary", required=True)
    parser.add_argument("--yui-binary", required=True)
    parser.add_argument("--fixture-dir", default=str(C21_FIXTURES))
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=600)
    args = parser.parse_args()

    evidence = pathlib.Path(args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    runs = evidence / "runs"
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = runs / f"verify-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    consumer_source = DEFAULT_CONSUMER_SOURCE
    consumer = pathlib.Path(args.consumer_binary).resolve()
    yui = pathlib.Path(args.yui_binary).resolve()
    fixture_dir = pathlib.Path(args.fixture_dir).resolve()
    started = time.monotonic()
    outcome: dict[str, Any] = {
        "action": args.action,
        "decision": "FAILED",
        "run_dir": str(run_dir),
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "steps": [],
    }
    try:
        if args.child_timeout <= 0 or args.aggregate_timeout <= 0 or args.child_timeout > 60 or args.aggregate_timeout > 600:
            raise EvidenceFailure("requested timeout exceeds the immutable C37 bounds")
        if not args.no_build:
            outcome["steps"].append(build_consumer(consumer_source, consumer, run_dir, args.child_timeout))
            if args.action in ("all", "parity"):
                outcome["steps"].append(build_yui(yui, run_dir, args.child_timeout))
        if not consumer.is_file():
            raise EvidenceFailure(f"consumer binary is absent: {consumer}")
        if args.action in ("all", "timing"):
            _, timing = run_consumer_action("timing", consumer, run_dir, args.child_timeout)
            assert timing is not None
            verify_timing(timing)
            outcome["timing"] = timing
        if args.action in ("all", "mutations"):
            outcome["mutations"] = verify_mutations(consumer, run_dir, args.child_timeout)
        if args.action in ("all", "parity"):
            _, parity = run_consumer_action("parity", consumer, run_dir, args.child_timeout)
            assert parity is not None
            verify_parity(parity)
            outcome["consumer_parity"] = parity
            if not yui.is_file():
                raise EvidenceFailure(f"yui binary is absent: {yui}")
            outcome["yui_parity"] = run_parity(yui, run_dir, args.child_timeout, fixture_dir)
        if args.action in ("all", "hang-control"):
            hang_result, _ = run_consumer_action("hang-control", consumer, run_dir, args.child_timeout)
            outcome["hang_control"] = hang_result
        if args.action in ("all", "provenance"):
            provenance = source_provenance(args.source, consumer_source, consumer, yui if yui.is_file() else None, fixture_dir)
            (run_dir / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
            outcome["provenance"] = provenance
        if time.monotonic() - started > args.aggregate_timeout:
            raise EvidenceFailure("aggregate deadline exceeded")
        outcome["decision"] = "ACCEPTED"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    report_files = {
        "timing": "timing.json",
        "mutations": "mutations.json",
        "consumer_parity": "consumer-parity.json",
        "yui_parity": "yui-parity.json",
        "hang_control": "hang-control.json",
        "provenance": "provenance.json",
    }
    for key, filename in report_files.items():
        if key in outcome:
            (evidence / filename).write_text(json.dumps(outcome[key], indent=2) + "\n", encoding="utf-8")
    (evidence / "latest-outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
