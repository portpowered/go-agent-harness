#!/usr/bin/env python3
"""Bounded C40 policy, consumer, and strict replay-regression evidence."""

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
from typing import Any, Sequence


EVIDENCE = Path(__file__).resolve().parent
ROOT = next(parent for parent in EVIDENCE.parents if (parent / "go.work").is_file())
CONSUMER_DIR = EVIDENCE / "consumer"
CONSUMER_SOURCE = CONSUMER_DIR / "main.go"
RUNS = EVIDENCE / "runs"
C16_FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"

BASELINE_REVISION = "926ded7bfa8f3c3e42115192d03aa1240c4806db"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MARKER = "PROBE_TOOL_MARKER_9182"
CHILD_TIMEOUT_SECONDS = 60
TOTAL_TIMEOUT_SECONDS = 600
TERMINATE_GRACE_SECONDS = 3
OUTPUT_LIMIT = 1 << 20

AUDIO_FIXTURE = C16_FIXTURES / "c16-audio-tool.session.json"
INTERRUPTION_FIXTURE = C16_FIXTURES / "c16-interruption.session.json"
FIXTURE_HASHES = {
    AUDIO_FIXTURE: "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
    INTERRUPTION_FIXTURE: "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
}
PCM_EXPECTATIONS = {
    "audio-tool": {
        "bytes": 4800,
        "sha256": "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502",
        "terminal": {
            "reason": "fixture_complete",
            "classification": "provider_close",
            "terminal_reason": "provider_close",
            "terminal_provenance": "provider",
            "output_state": "not_applicable",
        },
    },
    "interruption": {
        "bytes": 3840,
        "sha256": "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22",
        "tail_offset": 1440,
        "tail_bytes": 2400,
        "tail_sha256": "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf",
        "terminal": {
            "reason": "replay_complete",
            "classification": "replay_complete",
            "terminal_reason": "replay_complete",
            "terminal_provenance": "replay",
            "output_state": "complete",
        },
    },
}
EXPECTED_TYPES = {
    "audio-tool": [
        "session.update", "session.created", "conversation.item.create", "response.create",
        "response.created", "response.output_item.added", "response.function_call_arguments.done",
        "response.done", "conversation.item.create", "response.create", "response.created",
        "response.output_audio.delta", "response.output_audio.delta", "response.output_audio.done",
        "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
    ],
    "interruption": [
        "session.update", "session.created", "conversation.item.create", "response.create",
        "response.created", "response.output_audio.delta", "input_audio_buffer.speech_started",
        "conversation.item.truncate", "conversation.item.truncated", "response.output_audio.done",
        "response.done", "response.created", "response.output_audio.delta", "response.output_audio.done",
        "response.done",
    ],
}

CREDENTIAL_ENV_NAMES = {
    "OPENAI_API_KEY", "OPENAI_API_BASE", "OPENAI_ORG_ID", "ANTHROPIC_API_KEY",
    "AZURE_OPENAI_API_KEY", "REALTIME_API_KEY", "YUI_API_KEY", "OPENROUTER_API_KEY",
    "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
}


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {exc}") from exc


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def git_output(*args: str) -> str:
    result = subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True, check=False, timeout=10)
    if result.returncode != 0:
        raise EvidenceFailure(result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def git_show(revision: str, path: str) -> bytes | None:
    result = subprocess.run(["git", "-C", str(ROOT), "show", f"{revision}:{path}"], capture_output=True, check=False, timeout=10)
    if result.returncode != 0:
        return None
    return result.stdout


def sanitized_environment() -> tuple[dict[str, str], list[str]]:
    environment = os.environ.copy()
    removed: list[str] = []
    for name in list(environment):
        if name in CREDENTIAL_ENV_NAMES or name.endswith("_API_KEY"):
            removed.append(name)
            del environment[name]
    environment["GOWORK"] = "off"
    return environment, sorted(removed)


def process_group_alive(group_id: int) -> bool:
    try:
        os.killpg(group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def signal_group(group_id: int, sig: signal.Signals) -> None:
    try:
        os.killpg(group_id, sig)
    except ProcessLookupError:
        pass


def run_command(
    label: str,
    argv: Sequence[str | Path],
    cwd: Path,
    run_dir: Path,
    *,
    timeout_seconds: float = CHILD_TIMEOUT_SECONDS,
    input_text: str | None = None,
    environment: dict[str, str] | None = None,
    removed_credentials: list[str] | None = None,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    command = [str(item) for item in argv]
    env = dict(environment) if environment is not None else sanitized_environment()[0]
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=env,
        stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        text=True,
    )
    timed_out = False
    termination_signals: list[str] = []
    stdout = ""
    stderr = ""
    try:
        try:
            stdout, stderr = process.communicate(input=input_text, timeout=timeout_seconds)
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = exc.stdout or ""
            stderr = exc.stderr or ""
            signal_group(process.pid, signal.SIGTERM)
            termination_signals.append("SIGTERM")
            try:
                trailing_stdout, trailing_stderr = process.communicate(timeout=TERMINATE_GRACE_SECONDS)
                stdout += trailing_stdout or ""
                stderr += trailing_stderr or ""
            except subprocess.TimeoutExpired as grace_exc:
                stdout += grace_exc.stdout or ""
                stderr += grace_exc.stderr or ""
                signal_group(process.pid, signal.SIGKILL)
                termination_signals.append("SIGKILL")
                try:
                    trailing_stdout, trailing_stderr = process.communicate(timeout=TERMINATE_GRACE_SECONDS)
                    stdout += trailing_stdout or ""
                    stderr += trailing_stderr or ""
                except subprocess.TimeoutExpired as reap_exc:
                    stdout += reap_exc.stdout or ""
                    stderr += reap_exc.stderr or ""
        if process.poll() is None:
            signal_group(process.pid, signal.SIGKILL)
            if "SIGKILL" not in termination_signals:
                termination_signals.append("SIGKILL")
            try:
                trailing_stdout, trailing_stderr = process.communicate(timeout=TERMINATE_GRACE_SECONDS)
                stdout += trailing_stdout or ""
                stderr += trailing_stderr or ""
            except subprocess.TimeoutExpired:
                pass
    finally:
        if process.stdin is not None:
            process.stdin.close()
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()

    stdout_truncated = len(stdout.encode()) > OUTPUT_LIMIT
    stderr_truncated = len(stderr.encode()) > OUTPUT_LIMIT
    if stdout_truncated:
        stdout = stdout.encode()[:OUTPUT_LIMIT].decode(errors="replace")
    if stderr_truncated:
        stderr = stderr.encode()[:OUTPUT_LIMIT].decode(errors="replace")
    record = {
        "label": label,
        "argv": command,
        "cwd": str(cwd),
        "environment": {key: env.get(key, "") for key in ("PATH", "GOWORK", "GOFLAGS", "GOTOOLCHAIN")},
        "removed_credentials": removed_credentials or [],
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "termination_signals": termination_signals,
        "descendants_reaped": not process_group_alive(process.pid),
        "stdout_truncated": stdout_truncated,
        "stderr_truncated": stderr_truncated,
        "elapsed_seconds": round(time.monotonic() - started, 6),
    }
    stdout_path = run_dir / f"{label}.stdout.txt"
    stderr_path = run_dir / f"{label}.stderr.txt"
    stdout_path.write_text(stdout, encoding="utf-8")
    stderr_path.write_text(stderr, encoding="utf-8")
    record["stdout_path"] = str(stdout_path)
    record["stderr_path"] = str(stderr_path)
    write_json(run_dir / f"{label}.json", record)
    return record


def require_command_ok(record: dict[str, Any]) -> None:
    require(record["timed_out"] is False, f"{record['label']} exceeded {CHILD_TIMEOUT_SECONDS}s")
    require(record["descendants_reaped"] is True, f"{record['label']} left a live process group")
    require(record["stdout_truncated"] is False and record["stderr_truncated"] is False, f"{record['label']} exceeded output cap")
    require(record["exit_code"] == 0, f"{record['label']} exited {record['exit_code']}; see {record['stderr_path']}")


def settings_from_output(report: dict[str, Any], key: str) -> dict[str, str]:
    value = report.get(key)
    require(isinstance(value, dict), f"consumer omitted {key}")
    return value


def build_consumer(run_dir: Path, temporary: Path) -> tuple[Path, dict[str, Any]]:
    binary = temporary / "interactive-policy-consumer"
    environment, removed = sanitized_environment()
    record = run_command(
        "consumer-build",
        ["rtk", "proxy", "go", "build", "-trimpath", "-o", binary, "."],
        CONSUMER_DIR,
        run_dir,
        environment=environment,
        removed_credentials=removed,
    )
    require_command_ok(record)
    require(binary.is_file(), "consumer build did not produce an executable")
    return binary, record


def run_consumer(binary: Path, run_dir: Path, *, wrong_oracle: bool = False) -> dict[str, Any]:
    environment, removed = sanitized_environment()
    args: list[str | Path] = ["rtk", "proxy", binary]
    if wrong_oracle:
        args.append("wrong-oracle")
    record = run_command(
        "consumer-wrong-oracle" if wrong_oracle else "consumer-run",
        args,
        CONSUMER_DIR,
        run_dir,
        environment=environment,
        removed_credentials=removed,
    )
    return record


def parse_consumer_output(record: dict[str, Any]) -> dict[str, Any]:
    output = Path(record["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        value = json.loads(output)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"consumer did not emit JSON: {exc}") from exc
    require(isinstance(value, dict) and value.get("status") == "ok", f"consumer report was not successful: {value!r}")
    require(value.get("marker") == "C40_POLICY_CONSUMER", "consumer marker changed")
    require(settings_from_output(value, "defaults") == {
        "fast_read_timeout": "5s",
        "long_running_timeout": "20s",
        "acknowledgement_threshold": "2s",
    }, "consumer default literal oracle changed")
    require(settings_from_output(value, "overrides") == {
        "fast_read_timeout": "7s",
        "long_running_timeout": "15s",
        "acknowledgement_threshold": "1.2s",
    }, "consumer override literal oracle changed")
    classes = value.get("classes", {})
    require(classes.get("read_file") == "fast/read", "read_file class changed")
    require(classes.get("exec") == "bounded-long-running", "exec class changed")
    require(classes.get("sleep") == "bounded-long-running", "sleep class changed")
    require(classes.get("browser_select") == "bounded-long-running", "explicit browser class changed")
    require(classes.get("catalog_page") == "bounded-long-running", "base/full class changed")
    require(classes.get("dynamic_page") == "bounded-long-running", "dynamic class changed")
    require(value.get("timeouts", {}).get("read_file") == "5s", "fast/read deadline changed")
    require(value.get("timeouts", {}).get("exec") == "20s", "long-running deadline changed")
    require(value.get("timeouts", {}).get("catalog_page") == "20s", "catalog deadline changed")
    require(value.get("snapshot_input_isolation") is True, "input snapshot isolation failed")
    require(value.get("snapshot_clone_isolation") is True, "clone snapshot isolation failed")
    require(value.get("invalid_rejected_before_effects") is True, "invalid policy was not rejected")
    require(value.get("provider_setup_calls") == 0, "invalid policy reached provider setup")
    require(value.get("correlated_continuation") == "exec -> completed -> continuation", "continuation trace changed")
    return value


def run_consumer_mode(run_dir: Path, *, wrong_oracle: bool = False) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c40-consumer-") as temporary_name:
        binary, build_record = build_consumer(run_dir, Path(temporary_name))
        record = run_consumer(binary, run_dir, wrong_oracle=wrong_oracle)
        if wrong_oracle:
            require(record["timed_out"] is False and record["descendants_reaped"] is True, "wrong-oracle control did not shut down cleanly")
            require(record["exit_code"] != 0, "deliberate wrong-oracle control unexpectedly passed")
            stderr = Path(record["stderr_path"]).read_text(encoding="utf-8")
            require("wrong-oracle assertion" in stderr, "wrong-oracle failure was not at the intended assertion")
            return {"build": build_record, "control": record, "proof": "literal class decision mismatch rejected"}
        require_command_ok(record)
        return {"build": build_record, "run": record, "consumer": parse_consumer_output(record)}


def build_yui(run_dir: Path, temporary: Path) -> tuple[Path, dict[str, Any]]:
    binary = temporary / "yui"
    environment, removed = sanitized_environment()
    environment["GOWORK"] = str(ROOT / "go.work")
    record = run_command(
        "yui-build",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", binary, "./agent-cli/cmd/yui"],
        ROOT,
        run_dir,
        environment=environment,
        removed_credentials=removed,
    )
    require_command_ok(record)
    require(binary.is_file(), "YUI build did not produce an executable")
    return binary, record


def load_json_lines(path: Path) -> list[dict[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise EvidenceFailure(f"missing JSONL artifact {path}: {exc}") from exc
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(lines, 1):
        try:
            record = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL {path}:{line_number}: {exc}") from exc
        require(isinstance(record, dict), f"non-object JSONL record {path}:{line_number}")
        records.append(record)
    require(bool(records), f"empty JSONL artifact {path}")
    return records


def validate_session_log(label: str, path: Path) -> None:
    turns = load_json_lines(path)
    if label == "audio-tool":
        require(len(turns) == 1, "audio-tool turn count changed")
        response = turns[0].get("response", {})
        require(response.get("text") == "strict replay continuation", "tool continuation response changed")
        require(response.get("audio_bytes") == 4800, "tool response PCM size changed")
        events = turns[0].get("tool_events")
        require(isinstance(events, list) and len(events) == 2, "tool event count changed")
        require(events[0].get("tool_name") == "exec" and events[0].get("status") is None, "tool call identity changed")
        require(events[1].get("tool_name") == "exec" and events[1].get("status") == "completed", "tool completion changed")
        require(events[1].get("content") == MARKER + "\n", "tool marker changed")
    else:
        require(len(turns) == 2, "interruption turn count changed")
        require(turns[0].get("response", {}).get("audio_bytes") == 1440, "interruption prefix changed")
        require(turns[1].get("response", {}).get("audio_bytes") == 2400, "interruption healthy tail changed")
        require(all("tool_events" not in turn or turn.get("tool_events") in (None, []) for turn in turns), "interruption unexpectedly contained tools")


def validate_record_artifacts(label: str, fixture: Path, record_dir: Path) -> dict[str, Any]:
    expectation = PCM_EXPECTATIONS[label]
    manifest_path = record_dir / "manifest.json"
    pcm_path = record_dir / "audio" / "out-000.pcm"
    provider_path = record_dir / "provider.json"
    session_log = record_dir / "session-log.jsonl"
    require(manifest_path.is_file() and pcm_path.is_file() and provider_path.is_file() and session_log.is_file(), f"{label} bundle is incomplete")
    manifest = load_json(manifest_path)
    require(manifest.get("terminal") == expectation["terminal"], f"{label} terminal state changed")
    artifacts = {item.get("path"): item.get("sha256") for item in manifest.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    require(set(artifacts) == expected_paths, f"{label} manifest artifact set changed")
    for relative, digest in artifacts.items():
        artifact = record_dir / relative
        require(artifact.is_file() and sha256_file(artifact) == digest, f"{label} manifest hash mismatch for {relative}")
    require(sha256_file(provider_path) == sha256_file(fixture), f"{label} provider fixture changed")
    fixture_records = load_json(fixture).get("records", [])
    require([record.get("type") for record in fixture_records] == EXPECTED_TYPES[label], f"{label} provider event order changed")
    pcm = pcm_path.read_bytes()
    require(len(pcm) == expectation["bytes"] and sha256_bytes(pcm) == expectation["sha256"], f"{label} exact PCM changed")
    tail: dict[str, Any] = {}
    if "tail_offset" in expectation:
        tail_bytes = pcm[expectation["tail_offset"]:]
        tail = {"offset": expectation["tail_offset"], "bytes": len(tail_bytes), "sha256": sha256_bytes(tail_bytes)}
        require(tail["bytes"] == expectation["tail_bytes"] and tail["sha256"] == expectation["tail_sha256"], f"{label} healthy tail changed")
    validate_session_log(label, session_log)
    return {
        "fixture": str(fixture),
        "fixture_sha256": sha256_file(fixture),
        "manifest": str(manifest_path),
        "manifest_sha256": sha256_file(manifest_path),
        "pcm": str(pcm_path),
        "pcm_bytes": len(pcm),
        "pcm_sha256": sha256_bytes(pcm),
        "healthy_tail": tail,
        "terminal": manifest["terminal"],
    }


def run_yui_case(label: str, fixture: Path, yui: Path, run_dir: Path) -> dict[str, Any]:
    require(fixture.is_file(), f"missing accepted fixture {fixture}")
    require(sha256_file(fixture) == FIXTURE_HASHES[fixture], f"accepted fixture hash changed: {fixture}")
    case_dir = run_dir / f"yui-{label}"
    record_dir = case_dir / "tool-record"
    audio_out = case_dir / "audio.wav"
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    case_dir.mkdir(parents=True, exist_ok=True)
    environment, removed = sanitized_environment()
    record = run_command(
        f"yui-replay-{label}",
        [
            "rtk", "proxy", yui, "session", "--replay", fixture, "--audio-out", audio_out,
            "--record-dir", record_dir, "--trace-audio", "--workdir", case_dir, "--allow-path", case_dir,
        ],
        case_dir,
        run_dir,
        environment=environment,
        removed_credentials=removed,
    )
    require_command_ok(record)
    stdout = Path(record["stdout_path"]).read_text(encoding="utf-8")
    require("replay mismatch" not in stdout.lower(), f"{label} replay mismatch reported")
    marker_path = case_dir / "evidence" / "runs" / "exec-invocations-v4.log"
    if label == "audio-tool":
        require(MARKER in stdout and "strict replay continuation" in stdout and marker_path.is_file(), "tool replay marker/continuation missing")
        require(MARKER in marker_path.read_text(encoding="utf-8"), "tool marker side effect missing")
    else:
        require("replay_complete" in stdout or "session closed" in stdout.lower(), "interruption replay did not terminate")
    parity = validate_record_artifacts(label, fixture, record_dir)
    parity.update({"run": record, "audio_out": str(audio_out), "audio_out_sha256": sha256_file(audio_out) if audio_out.is_file() else ""})
    return parity


def expect_strict_failure(label: str, action: Any) -> dict[str, Any]:
    try:
        action()
    except EvidenceFailure as exc:
        return {"label": label, "rejected": True, "error": str(exc)}
    raise EvidenceFailure(f"negative control {label} unexpectedly passed")


def strict_negative_controls(label: str, fixture: Path, valid_record_dir: Path, run_dir: Path) -> list[dict[str, Any]]:
    controls: list[dict[str, Any]] = []
    with tempfile.TemporaryDirectory(prefix=f"audio-runtime-c40-{label}-negative-") as temporary_name:
        root = Path(temporary_name)

        missing = root / "missing"
        shutil.copytree(valid_record_dir, missing)
        (missing / "audio" / "out-000.pcm").unlink()
        controls.append(expect_strict_failure("missing-pcm", lambda: validate_record_artifacts(label, fixture, missing)))

        truncated = root / "truncated"
        shutil.copytree(valid_record_dir, truncated)
        pcm = truncated / "audio" / "out-000.pcm"
        pcm.write_bytes(pcm.read_bytes()[:2])
        controls.append(expect_strict_failure("truncated-pcm", lambda: validate_record_artifacts(label, fixture, truncated)))

        wrong_marker = root / "wrong-marker"
        shutil.copytree(valid_record_dir, wrong_marker)
        if label == "audio-tool":
            session_log = wrong_marker / "session-log.jsonl"
            session_log.write_text(session_log.read_text(encoding="utf-8").replace(MARKER, "WRONG_MARKER"), encoding="utf-8")
            manifest = load_json(wrong_marker / "manifest.json")
            for item in manifest["artifacts"]:
                if item["path"] == "session-log.jsonl":
                    item["sha256"] = sha256_file(session_log)
            write_json(wrong_marker / "manifest.json", manifest)
            controls.append(expect_strict_failure("wrong-marker", lambda: validate_record_artifacts(label, fixture, wrong_marker)))
        else:
            manifest = load_json(wrong_marker / "manifest.json")
            manifest["terminal"]["reason"] = "wrong-terminal"
            write_json(wrong_marker / "manifest.json", manifest)
            controls.append(expect_strict_failure("wrong-terminal", lambda: validate_record_artifacts(label, fixture, wrong_marker)))

        tampered = root / "tampered-bundle"
        shutil.copytree(valid_record_dir, tampered)
        tampered_manifest = tampered / "manifest.json"
        tampered_manifest.write_text(tampered_manifest.read_text(encoding="utf-8").replace("provider.json", "provider-tampered.json"), encoding="utf-8")
        controls.append(expect_strict_failure("tampered-bundle", lambda: validate_record_artifacts(label, fixture, tampered)))
    write_json(run_dir / f"{label}-negative-controls.json", controls)
    return controls


def replay_regression(run_dir: Path) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c40-yui-") as temporary_name:
        yui, build_record = build_yui(run_dir, Path(temporary_name))
        audio = run_yui_case("audio-tool", AUDIO_FIXTURE, yui, run_dir)
        interruption = run_yui_case("interruption", INTERRUPTION_FIXTURE, yui, run_dir)
        negatives = strict_negative_controls("audio-tool", AUDIO_FIXTURE, Path(audio["manifest"]).parent, run_dir)
        interruption_negatives = strict_negative_controls("interruption", INTERRUPTION_FIXTURE, Path(interruption["manifest"]).parent, run_dir)
        return {"build": build_record, "audio_tool": audio, "interruption": interruption, "negative_controls": negatives + interruption_negatives}


def public_policy(run_dir: Path) -> dict[str, Any]:
    consumer = run_consumer_mode(run_dir)
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c40-public-") as temporary_name:
        yui, build_record = build_yui(run_dir, Path(temporary_name))
        replay = run_yui_case("audio-tool", AUDIO_FIXTURE, yui, run_dir)
        return {
            "consumer": consumer,
            "yui_build": build_record,
            "controlled_tool_request": replay,
            "source_call_chain": [
                "agent-cli/internal/services/internal/agentruntime.NewInteractiveToolPolicyForSession",
                "go-agent-runtime/services/tools/wire.NewInteractiveToolPolicy",
                "go-agent-runtime/services/tools/internal/policy.Factory.Resolve",
                "go-agent-runtime/services/tools.InteractiveToolPolicy.ClassForTool/TimeoutForTool",
                "agent-cli/internal/services/internal/agentruntime.sessionToolExecutor",
            ],
            "invalid_policy_before_provider_effects": consumer["consumer"]["invalid_rejected_before_effects"],
            "replay_is_not_the_only_policy_proof": True,
        }


def count_lines(value: bytes | None) -> int:
    return 0 if not value else len(value.splitlines())


def candidate_line_count(path: str) -> int:
    return count_lines((ROOT / path).read_bytes())


def production_inventory() -> dict[str, Any]:
    production_paths = [
        "agent-cli/internal/services/internal/agentruntime/session_interactive_policy.go",
        "go-agent-runtime/services/tools/interactive_policy.go",
        "go-agent-runtime/services/tools/internal/policy/policy.go",
        "go-agent-runtime/services/tools/wire/wire.go",
        "go-agent-runtime/services/tools/wire/wire_gen.go",
    ]
    per_file: dict[str, Any] = {}
    for path in production_paths:
        before = git_show(BASELINE_REVISION, path)
        after = (ROOT / path).read_bytes()
        per_file[path] = {
            "baseline_revision": BASELINE_REVISION,
            "baseline_exists": before is not None,
            "baseline_lines": count_lines(before),
            "candidate_lines": count_lines(after),
            "baseline_sha256": sha256_bytes(before) if before is not None else None,
            "candidate_sha256": sha256_bytes(after),
            "line_method": "physical lines from bytes.splitlines(), including comments and blank lines",
        }

    aggregate_path = "agent-cli/internal/services/internal/agentruntime"
    baseline_files = [
        path for path in git_output("ls-tree", "-r", "--name-only", BASELINE_REVISION, "--", aggregate_path).splitlines()
        if path.endswith(".go") and not path.endswith("_test.go")
    ]
    candidate_files = [
        str(path.relative_to(ROOT)) for path in (ROOT / aggregate_path).glob("*.go") if not path.name.endswith("_test.go")
    ]
    per_file["aggregate_cli_agentruntime_production"] = {
        "baseline_revision": BASELINE_REVISION,
        "baseline_files": sorted(baseline_files),
        "candidate_files": sorted(candidate_files),
        "baseline_lines": sum(count_lines(git_show(BASELINE_REVISION, path)) for path in baseline_files),
        "candidate_lines": sum(candidate_line_count(path) for path in candidate_files),
        "line_method": "all non-test .go files directly under agent-cli/internal/services/internal/agentruntime; physical lines including comments and blanks",
    }

    baseline_cli = (git_show(BASELINE_REVISION, "agent-cli/internal/services/internal/agentruntime/session_interactive_policy.go") or b"").decode(errors="replace")
    candidate_cli = (ROOT / "agent-cli/internal/services/internal/agentruntime/session_interactive_policy.go").read_text(encoding="utf-8")
    symbols = {
        "NewInteractiveToolPolicy": "retained CLI constructor wrapper -> runtime Wire factory",
        "NewInteractiveToolPolicyForSession": "retained CLI compatibility wrapper -> runtime request conversion",
        "ResolveInteractiveToolPolicy": "retained raw cfg.Tools.Interactive conversion -> runtime defaults/validation",
        "Clone": "retained CLI value method -> runtime snapshot Clone",
        "ClassForTool": "retained CLI value method -> runtime snapshot ClassForTool",
        "TimeoutForTool": "retained CLI value method -> runtime snapshot TimeoutForTool",
        "Validate": "retained CLI value method -> runtime factory/policy Validate",
        "interactiveToolClassForName": "retired from CLI; runtime private classForName owns name decisions",
        "toolClasses": "retired from CLI; runtime private snapshot owns class map",
        "dynamicLongRunning": "retired from CLI; runtime private snapshot owns dynamic fallback",
    }
    symbol_records = {}
    for symbol, mapping in symbols.items():
        symbol_records[symbol] = {
            "baseline_occurrences": baseline_cli.count(symbol),
            "candidate_occurrences": candidate_cli.count(symbol),
            "mapping": mapping,
        }

    residual: list[dict[str, Any]] = []
    for path in sorted((ROOT / aggregate_path).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if "ResolveInteractiveToolConfig" in line:
                residual.append({"path": str(path.relative_to(ROOT)), "line": line_number, "owner": "existing CLI config-loading transport boundary"})
    return {
        "baseline_revision": BASELINE_REVISION,
        "startup_revision": STARTUP_REVISION,
        "origin_main": git_output("rev-parse", "origin/main"),
        "candidate_head": git_output("rev-parse", "HEAD"),
        "ancestry": {
            "baseline_is_ancestor": subprocess.run(["git", "-C", str(ROOT), "merge-base", "--is-ancestor", BASELINE_REVISION, "HEAD"], check=False).returncode == 0,
            "startup_is_ancestor": subprocess.run(["git", "-C", str(ROOT), "merge-base", "--is-ancestor", STARTUP_REVISION, "HEAD"], check=False).returncode == 0,
            "origin_main_is_ancestor": subprocess.run(["git", "-C", str(ROOT), "merge-base", "--is-ancestor", "origin/main", "HEAD"], check=False).returncode == 0,
        },
        "production_files": per_file,
        "symbols": symbol_records,
        "config_residual": residual,
        "runtime_forbidden_import_scan": {
            "paths": ["go-agent-runtime/services/tools/interactive_policy.go", "go-agent-runtime/services/tools/internal/policy", "go-agent-runtime/services/tools/wire"],
            "forbidden": ["agent-cli", "internal/config", "webmcp"],
            "method": "source import scan",
            "passed": not any(token in (ROOT / path).read_text(encoding="utf-8") for path in ["go-agent-runtime/services/tools/interactive_policy.go", "go-agent-runtime/services/tools/internal/policy/policy.go", "go-agent-runtime/services/tools/wire/wire.go"] for token in ["agent-cli", "internal/config", "webmcp"]),
        },
    }


def inventory(run_dir: Path) -> dict[str, Any]:
    report = production_inventory()
    require(report["ancestry"]["baseline_is_ancestor"], "required baseline is not an ancestor")
    require(report["ancestry"]["startup_is_ancestor"], "startup integration revision is not an ancestor")
    require(report["ancestry"]["origin_main_is_ancestor"], "origin/main is not an ancestor of candidate")
    require(report["runtime_forbidden_import_scan"]["passed"], "forbidden runtime import found")
    write_json(run_dir / "inventory.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("inventory", "consumer", "wrong-oracle", "public-policy", "replay-regression", "all"))
    args = parser.parse_args()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = RUNS / f"run-{args.mode}-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    outcome: dict[str, Any] = {
        "schema": "audio-runtime-c40-interactive-policy/v1",
        "mode": args.mode,
        "run_dir": str(run_dir),
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "decision": "FAILED",
        "aggregate_deadline_seconds": TOTAL_TIMEOUT_SECONDS,
    }
    try:
        if args.mode == "inventory":
            outcome["inventory"] = inventory(run_dir)
        elif args.mode == "consumer":
            outcome["consumer"] = run_consumer_mode(run_dir)
        elif args.mode == "wrong-oracle":
            outcome["wrong_oracle"] = run_consumer_mode(run_dir, wrong_oracle=True)
        elif args.mode == "public-policy":
            outcome["public_policy"] = public_policy(run_dir)
        elif args.mode == "replay-regression":
            outcome["replay_regression"] = replay_regression(run_dir)
        else:
            outcome["inventory"] = inventory(run_dir)
            outcome["consumer"] = run_consumer_mode(run_dir)
            outcome["wrong_oracle"] = run_consumer_mode(run_dir, wrong_oracle=True)
            outcome["public_policy"] = public_policy(run_dir)
            outcome["replay_regression"] = replay_regression(run_dir)
        outcome["decision"] = "ACCEPTED"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        write_json(run_dir / "outcome.json", outcome)
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    write_json(run_dir / "outcome.json", outcome)
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
