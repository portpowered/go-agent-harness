#!/usr/bin/env python3
"""Bounded, source-pinned evidence checks for C77 replay retirement."""

from __future__ import annotations

import argparse
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
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = next(parent for parent in (HERE, *HERE.parents) if (parent / "go.work").is_file())
FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
EXTERNAL = HERE / "external-consumer"
RUNS = HERE / "runs"
LEGACY = ROOT / "agent-cli/internal/services/internal/agentruntime/session_replay.go"
BASELINE = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
MAX_OUTPUT_BYTES = 2 * 1024 * 1024


PARITY_EXPECTATIONS: dict[str, dict[str, Any]] = {
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
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_item.added", "response.function_call_arguments.done",
            "response.done", "conversation.item.create", "response.create", "response.created",
            "response.output_audio.delta", "response.output_audio.delta", "response.output_audio.done",
            "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
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
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_audio.delta", "input_audio_buffer.speech_started",
            "conversation.item.truncate", "conversation.item.truncated", "response.output_audio.done",
            "response.done", "response.created", "response.output_audio.delta",
            "response.output_audio.done", "response.done",
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


def load_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {exc}") from exc


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    if not path.is_file():
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


def safe_environment(gowork: str | None = None) -> dict[str, str]:
    """Keep build/runtime context while excluding credential-bearing variables."""
    sensitive_tokens = ("KEY", "TOKEN", "PASSWORD", "SECRET", "CREDENTIAL")
    safe_keys = {
        "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TERM", "GOWORK", "GOFLAGS",
        "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "GOTOOLCHAIN", "GOPROXY",
        "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "CGO_ENABLED",
    }
    environment = {
        key: value
        for key, value in os.environ.items()
        if (key in safe_keys or key.startswith("GO")) and not any(token in key.upper() for token in sensitive_tokens)
    }
    if gowork is not None:
        environment["GOWORK"] = gowork
    return environment


def _read_available(selector: selectors.BaseSelector, buffers: dict[int, bytearray], truncated: set[int]) -> None:
    for key, _ in selector.select(timeout=0):
        stream = key.fileobj
        try:
            chunk = os.read(stream.fileno(), 64 * 1024)
        except OSError:
            chunk = b""
        if not chunk:
            selector.unregister(stream)
            stream.close()
            continue
        buffer = buffers[stream.fileno()]
        if len(buffer) < MAX_OUTPUT_BYTES:
            remaining = MAX_OUTPUT_BYTES - len(buffer)
            buffer.extend(chunk[:remaining])
            if len(chunk) > remaining:
                truncated.add(stream.fileno())
        else:
            truncated.add(stream.fileno())


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    *,
    timeout_seconds: float = 120,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Run one child in its own process group with bounded output and cleanup."""
    run_dir.mkdir(parents=True, exist_ok=True)
    safe_label = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe_label}.stdout"
    stderr_path = run_dir / f"{safe_label}.stderr"
    result_path = run_dir / f"{safe_label}.json"
    selected_env = dict(safe_environment() if env is None else env)
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected_env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    assert process.stdout is not None and process.stderr is not None
    stdout_fd = process.stdout.fileno()
    stderr_fd = process.stderr.fileno()
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    selector.register(process.stderr, selectors.EVENT_READ)
    buffers = {stdout_fd: bytearray(), stderr_fd: bytearray()}
    truncated: set[int] = set()
    started = time.monotonic()
    timed_out = False
    terminated_group = False
    killed_group = False

    while selector.get_map() or process.poll() is None:
        remaining = timeout_seconds - (time.monotonic() - started)
        if remaining <= 0 and (process.poll() is None or selector.get_map()):
            timed_out = True
            terminated_group = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            if process.poll() is None:
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    killed_group = True
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait()
            else:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            if process.poll() is None:
                killed_group = True
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait()
        _read_available(selector, buffers, truncated)
        if selector.get_map():
            selector.select(timeout=min(0.2, max(0.0, remaining)))
        elif process.poll() is not None:
            break
    while selector.get_map():
        _read_available(selector, buffers, truncated)
        if selector.get_map():
            selector.select(timeout=0.05)
    exit_code = process.wait()
    elapsed = time.monotonic() - started
    stdout = bytes(buffers[stdout_fd])
    stderr = bytes(buffers[stderr_fd])
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment_keys": sorted(selected_env),
        "timeout_seconds": timeout_seconds,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": exit_code,
        "timed_out": timed_out,
        "terminated_process_group": terminated_group,
        "killed_process_group": killed_group,
        "output_truncated": bool(truncated),
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    result_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_ok(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")
    if result["output_truncated"]:
        raise EvidenceFailure(f"{result['label']} exceeded the evidence output cap")


def require_rejected(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] == 0:
        raise EvidenceFailure(f"{result['label']} unexpectedly succeeded or timed out")
    output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
    output += pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8", errors="replace")
    if not any(word in output.lower() for word in ("replay", "record", "hash", "mismatch", "artifact", "invalid")):
        raise EvidenceFailure(f"{result['label']} failed without a stable replay/recording diagnostic")


def require_test_events(result: dict[str, Any]) -> None:
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
    observed = set()
    for line in stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        name = event.get("Test")
        if isinstance(name, str) and name:
            observed.add(name)
    if not observed:
        raise EvidenceFailure(f"{result['label']} matched no Go tests; see {result['stdout_path']}")


def validate_fixture_matrix() -> dict[str, Any]:
    observed: dict[str, Any] = {}
    for label, expectation in PARITY_EXPECTATIONS.items():
        fixture = FIXTURES / expectation["fixture"]
        data = load_json(fixture)
        records = data.get("records")
        if not isinstance(records, list):
            raise EvidenceFailure(f"{label} fixture has no records array")
        types = [record.get("type") for record in records]
        if types != expectation["fixture_types"]:
            raise EvidenceFailure(f"{label} fixture event order changed: {types!r}")
        sequences = [record.get("sequence") for record in records]
        if sequences != list(range(1, len(records) + 1)):
            raise EvidenceFailure(f"{label} fixture sequence is not contiguous: {sequences!r}")
        observed[label] = {"sha256": sha256(fixture), "records": len(records), "types": types}
    return observed


def validate_transcript(path: pathlib.Path, label: str) -> None:
    records = load_jsonl(path)
    ticks = [record.get("tick") for record in records]
    if any(not isinstance(tick, int) for tick in ticks) or ticks != list(range(1, len(ticks) + 1)):
        raise EvidenceFailure(f"{label} transcript order is not contiguous")


def validate_session_log(label: str, path: pathlib.Path, expect_tool: bool) -> None:
    turns = load_jsonl(path)
    if expect_tool:
        expected = [{
            "turn_index": 1,
            "input": {"text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False},
            "response": {"text": "strict replay continuation", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 4800, "audio_segments": ["audio/out-000.pcm"]},
        }]
    else:
        expected = [
            {"turn_index": 1, "input": {"text": "c07 interruption", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}, "response": {"text": "", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 1440, "audio_segments": ["audio/out-000.pcm"]}},
            {"turn_index": 2, "input": {"text": "", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}, "response": {"text": "", "complete": True, "audio_offset_bytes": 1440, "audio_bytes": 2400, "audio_segments": ["audio/out-000.pcm"]}},
        ]
    if len(turns) != len(expected):
        raise EvidenceFailure(f"{label} session-log turn count changed")
    for actual, want in zip(turns, expected):
        for key, value in want.items():
            if actual.get(key) != value:
                raise EvidenceFailure(f"{label} session-log {key} changed: {actual.get(key)!r}")
    if expect_tool:
        events = turns[0].get("tool_events")
        metadata = [
            {key: event.get(key) for key in ("sequence", "type", "tool_call_id", "tool_name", "status", "content")}
            for event in events or []
        ]
        if metadata != [
            {"sequence": 1, "type": "tool_call", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": None, "content": None},
            {"sequence": 2, "type": "tool_result", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": "completed", "content": "PROBE_TOOL_MARKER_9182\n"},
        ]:
            raise EvidenceFailure(f"{label} tool event order/content changed")
        arguments = json.loads(events[0]["arguments"])
        expected_command = 'echo PROBE_TOOL_MARKER_9182 >> "evidence/runs/exec-invocations-v4.log"; echo PROBE_TOOL_MARKER_9182'
        if arguments != {"command": expected_command}:
            raise EvidenceFailure(f"{label} tool command changed")
    elif any(turn.get("tool_events") is not None for turn in turns):
        raise EvidenceFailure(f"{label} interruption unexpectedly contains tool events")


def validate_parity_artifacts(label: str, fixture: pathlib.Path, record_dir: pathlib.Path) -> dict[str, Any]:
    expectation = PARITY_EXPECTATIONS[label]
    manifest = load_json(record_dir / "manifest.json")
    if manifest.get("terminal") != expectation["terminal"]:
        raise EvidenceFailure(f"{label} terminal state changed: {manifest.get('terminal')!r}")
    artifact_hashes = {artifact.get("path"): artifact.get("sha256") for artifact in manifest.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    if set(artifact_hashes) != expected_paths:
        raise EvidenceFailure(f"{label} manifest artifact set changed: {sorted(artifact_hashes)!r}")
    for relative in expected_paths:
        path = record_dir / relative
        if not path.is_file() or artifact_hashes[relative] != sha256(path):
            raise EvidenceFailure(f"{label} manifest hash does not match {relative}")
    validate_transcript(record_dir / "client.transcript.jsonl", f"{label} client")
    validate_transcript(record_dir / "agent.transcript.jsonl", f"{label} agent")
    validate_session_log(label, record_dir / "session-log.jsonl", label == "audio-tool")
    provider = record_dir / "provider.json"
    if sha256(provider) != sha256(fixture):
        raise EvidenceFailure(f"{label} provider bytes changed")
    pcm = record_dir / "audio/out-000.pcm"
    pcm_bytes = pcm.read_bytes()
    if len(pcm_bytes) != expectation["pcm_bytes"] or sha256(pcm) != expectation["pcm_sha256"]:
        raise EvidenceFailure(f"{label} exact PCM parity changed")
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        offset = expectation["healthy_tail_offset_bytes"]
        tail = pcm_bytes[offset:]
        healthy_tail = {"offset_bytes": offset, "bytes": len(tail), "sha256": hashlib.sha256(tail).hexdigest()}
        if healthy_tail["bytes"] != expectation["healthy_tail_bytes"] or healthy_tail["sha256"] != expectation["healthy_tail_sha256"]:
            raise EvidenceFailure(f"{label} healthy tail parity changed")
    return {"terminal": manifest["terminal"], "pcm_sha256": sha256(pcm), "healthy_tail": healthy_tail}


def run_test_controls(run_dir: pathlib.Path) -> list[dict[str, Any]]:
    controls = [
        ("replay-focused", ["rtk", "proxy", "go", "test", "-json", "./go-agent-runtime/services/replay/...", "-run", "Test(Load|Inspect|Replay|Planner|Audio|ReplayHas|Wrap)", "-count=1", "-timeout=120s"]),
        ("public-consumer", ["rtk", "proxy", "go", "test", "./...", "-count=1", "-timeout=90s"]),
    ]
    results = []
    for label, argv in controls:
        cwd = EXTERNAL if label == "public-consumer" else ROOT
        environment = safe_environment("off") if label == "public-consumer" else safe_environment()
        if label == "public-consumer":
            argv = ["rtk", "proxy", "go", "test", "./...", "-count=1", "-timeout=90s"]
        result = run_process(label, argv, cwd, run_dir, timeout_seconds=180, env=environment)
        require_ok(result)
        if label == "replay-focused":
            require_test_events(result)
        results.append(result)
    return results


def run_negative_controls(mode: str, run_dir: pathlib.Path) -> list[dict[str, Any]]:
    commands: dict[str, list[tuple[str, list[str]]]] = {
        "mutation-disable-outbound-validation": [
            ("mutation-outbound", ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/replay/internal/strict", "-run", "^TestServiceRunRejectsChangedOutbound$", "-count=1", "-timeout=60s"]),
        ],
        "mutation-reorder-tool-audio": [
            ("mutation-audio-boundary", ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/replay/internal/plan", "-run", "^Test(AudioPlanRejectsInvalidBoundariesAndPayloads|AudioPlanNamesUnexpectedNextEventInBoundaryDiagnostic)$", "-count=1", "-timeout=60s"]),
        ],
        "mutation-synthetic-terminal": [
            ("mutation-terminal", ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/replay/internal/strict", "-run", "^TestServiceRejectsMissingTerminalResponseDone$", "-count=1", "-timeout=60s"]),
            ("mutation-terminal-wire", ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/replay", "-run", "^TestStrictEvidenceRejectsMalformedWireAndSkipsPostTerminalFailure$", "-count=1", "-timeout=60s"]),
        ],
    }
    results = []
    for label, argv in commands[mode]:
        result = run_process(label, argv, ROOT, run_dir, timeout_seconds=90)
        require_ok(result)
        results.append(result)
    return results


def changed_paths() -> list[str]:
    result = subprocess.run(["rtk", "proxy", "git", "diff", "--name-only", "origin/main...HEAD"], cwd=ROOT, check=True, capture_output=True, text=True)
    return [line for line in result.stdout.splitlines() if line]


def retirement_scope() -> dict[str, Any]:
    baseline_output = subprocess.run(["rtk", "proxy", "git", "show", f"{BASELINE}:{LEGACY.relative_to(ROOT)}"], cwd=ROOT, check=True, capture_output=True, text=True)
    baseline_lines = len(baseline_output.stdout.splitlines())
    current_lines = len(LEGACY.read_text(encoding="utf-8").splitlines())
    if baseline_lines != 670:
        raise EvidenceFailure(f"planning baseline session_replay.go line count changed: {baseline_lines}")
    if current_lines > 250 or baseline_lines - current_lines < 420:
        raise EvidenceFailure(f"session_replay.go retirement is {baseline_lines} -> {current_lines} lines")
    source = LEGACY.read_text(encoding="utf-8")
    forbidden = ("encoding/json", "json.Unmarshal", "LoadSessionCaptureForReplay", "NewSessionReplayer", "time.Sleep", "os.Getenv", "os.LookupEnv")
    present = [token for token in forbidden if token in source]
    if present:
        raise EvidenceFailure(f"legacy adapter still owns retired implementation symbols: {present}")
    if source.count("Deprecated") < 5:
        raise EvidenceFailure("legacy adapter does not retain explicit deprecation annotations")
    allowed = {
        "agent-cli/internal/services/internal/agentruntime/session_replay.go",
        "agent-cli/internal/services/internal/agentruntime/session_replay_audio_rate_test.go",
        "agent-cli/internal/services/internal/agentruntime/replay_integration_test.go",
        "coverage-manifest/go-agent-runtime/services/replay/package.json",
    }
    c77_prefix = "docs/temp/projects/audio-runtime/audio-runtime-c77-retire-cli-session-replay-runtime/"
    c77_code_prefix = "go-agent-runtime/services/replay/"
    unexpected = [path for path in changed_paths() if path not in allowed and not path.startswith(c77_prefix) and not path.startswith(c77_code_prefix)]
    if unexpected:
        raise EvidenceFailure(f"candidate changed paths outside C77 ownership: {unexpected}")
    diff_check = run_process("diff-check", ["rtk", "proxy", "git", "diff", "--check"], ROOT, RUNS / "scope", timeout_seconds=30)
    require_ok(diff_check)
    return {"baseline_revision": BASELINE, "baseline_lines": baseline_lines, "current_lines": current_lines, "retired_lines": baseline_lines - current_lines, "changed_paths": changed_paths()}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("frozen-capture-matrix", "mutation-disable-outbound-validation", "mutation-reorder-tool-audio", "mutation-synthetic-terminal", "retirement-and-scope"), required=True)
    parser.add_argument("--expect-failure", action="store_true", help="document that the selected mutation is a rejecting control")
    args = parser.parse_args()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = RUNS / f"verify-{args.mode}-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    outcome: dict[str, Any] = {"mode": args.mode, "run_dir": str(run_dir), "source_revision": "", "decision": "FAILED"}
    try:
        outcome["source_revision"] = subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()
        if args.mode == "frozen-capture-matrix":
            outcome["fixtures"] = validate_fixture_matrix()
            outcome["controls"] = run_test_controls(run_dir)
        elif args.mode.startswith("mutation-"):
            if not args.expect_failure:
                raise EvidenceFailure(f"{args.mode} requires --expect-failure to make the mutation intent explicit")
            outcome["negative_controls"] = run_negative_controls(args.mode, run_dir)
        else:
            outcome["retirement"] = retirement_scope()
        outcome["decision"] = "ACCEPTED"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
