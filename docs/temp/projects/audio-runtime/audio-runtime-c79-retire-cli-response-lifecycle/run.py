#!/usr/bin/env python3
"""Bounded credential-free software replay for the C79 lifecycle candidate."""
from __future__ import annotations

import argparse
import base64
import errno
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
EVIDENCE_ROOT = HERE / "evidence"
EVIDENCE = HERE / "evidence" / "runs"

CASES = {
    "scheduled-tool-continuation": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "^TestToolContinuationRemainsOneScheduledLifecycle$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Test(SessionProgressObserver_ChainedToolContinuation|RunAgentLoopSessionRetriesScheduledToolContinuationOnce)$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "^TestSessionCommand_CreditsConsecutiveScheduledToolContinuations$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "terminal-and-malformed": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "Test(Response|Malformed|Duplicate|Wrong|Terminal|Close|Order|Reset)",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Session(Diagnostics|Terminal|Cancellation)|SessionDiagnostics",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "credential-free-audio-tool-replay": [
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "Test(InteractionReplay_PrintsNormalizedEventsAsNDJSON|InteractionCommand_HelpDocumentsReplayOutputAndCredentialFreeBehavior|SessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI)$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
}

HEALTHY_REPLAY_FIXTURE = ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
ERROR_REPLAY_FIXTURE = ROOT / "agent-cli/test/integration/testdata/openai_realtime_error.session.json"
TOOL_REPLAY_FIXTURE = ROOT / "agent-cli/testdata/s2s-e2e-read-image/read_image_positive.session.json"
TOOL_IMAGE_FIXTURE = ROOT / "agent-cli/testdata/images/fixture.png"
SCHEDULED_AUDIO_FIXTURE_SOURCE = ROOT / "agent-cli/test/integration/session_audio_turn_replay_fixture_test.go"
REQUIRED_ANCESTRY = {
    "startup_integration": "8bdafc7f947a3a2c9856220abdc539437035bd21",
    "accepted_main": "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06",
    "planning_origin": "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f",
}
TOOL_PROMPT_TEMPLATE = "Please inspect the image at {image_path} without relying on its filename."
DEFAULT_TOOL_IDS = (
    "exec",
    "read_file",
    "read_image",
    "write_file",
    "edit_file",
    "append_file",
    "list_dir",
    "web_fetch",
    "web_search",
    "show",
    "mouse",
    "load_skill",
    "sleep",
)


def clean_environment() -> dict[str, str]:
    env = os.environ.copy()
    for name in list(env):
        if name.endswith(("_API_KEY", "_TOKEN", "_SECRET")) or name in {"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"}:
            env.pop(name, None)
    env["GOCACHE"] = "/tmp/go-build-audio-runtime-c79-replay"
    return env


def source_provenance() -> dict:
    revision = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    status_result = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=all"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    # Evidence is deliberately written by each bounded run. Treat every
    # descendant of the owned evidence root as a diagnostic output, while
    # still requiring all executable source and fixture inputs to be clean.
    evidence_prefix = str(EVIDENCE_ROOT.relative_to(ROOT)) + "/"
    status = []
    ignored_evidence_changes = []
    for line in status_result.stdout.splitlines():
        path = line[3:] if len(line) >= 3 else line
        if " -> " in path:
            path = path.rsplit(" -> ", 1)[-1]
        if path.startswith(evidence_prefix):
            ignored_evidence_changes.append(line)
        else:
            status.append(line)
    tracked = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, check=True, capture_output=True
    ).stdout.split(b"\0")
    evidence_prefix = str(EVIDENCE_ROOT.relative_to(ROOT)) + "/"
    digest = hashlib.sha256()
    input_count = 0
    non_file_paths = []
    for raw_path in tracked:
        if not raw_path:
            continue
        relative_path = os.fsdecode(raw_path)
        if relative_path.startswith(evidence_prefix):
            # These ledgers are outputs of this runner, not inputs to the
            # executable. Excluding them keeps the source digest stable when
            # multiple cases refresh their own evidence files.
            continue
        path = ROOT / os.fsdecode(raw_path)
        digest.update(raw_path)
        digest.update(b"\0")
        if path.is_file():
            digest.update(hashlib.sha256(path.read_bytes()).digest())
        else:
            # Gitlinks are tracked entries but materialize as directories in
            # this worktree. Include their index record instead of attempting
            # to read directory bytes.
            entry = subprocess.run(
                ["git", "ls-files", "--stage", "--", os.fsdecode(raw_path)],
                cwd=ROOT,
                check=True,
                capture_output=True,
            ).stdout
            digest.update(b"gitlink\0")
            digest.update(entry)
            non_file_paths.append(relative_path)
        input_count += 1
    go_version = subprocess.run(
        ["go", "version"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    branch = subprocess.run(
        ["git", "branch", "--show-current"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    prd = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    prd_branch = prd.get("branchName", "")
    if branch != prd_branch:
        raise RuntimeError(f"runner branch {branch!r} does not match prd.branchName {prd_branch!r}")
    origin_main = subprocess.run(
        ["git", "rev-parse", "origin/main"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    ancestry = {}
    for name, required_revision in {**REQUIRED_ANCESTRY, "origin_main": origin_main}.items():
        check = subprocess.run(
            ["git", "merge-base", "--is-ancestor", required_revision, "HEAD"],
            cwd=ROOT,
            capture_output=True,
        )
        ancestry[name] = {"revision": required_revision, "is_ancestor": check.returncode == 0}
    if not all(item["is_ancestor"] for item in ancestry.values()):
        raise RuntimeError(f"required ancestry is not present: {json.dumps(ancestry, sort_keys=True)}")
    return {
        "revision": revision,
        "branch": branch,
        "prd_branch": prd_branch,
        "origin_main": origin_main,
        "required_ancestry": ancestry,
        "status": "\n".join(status),
        "ignored_evidence_changes": ignored_evidence_changes,
        "tracked_input_count": input_count,
        "tracked_input_sha256": digest.hexdigest(),
        "tracked_non_file_paths": non_file_paths,
        "go_version": go_version,
    }


def file_provenance(path: Path) -> dict:
    data = path.read_bytes()
    return {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def display_path(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def output_text(value: object) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return str(value)


def process_group_exists(process_group_id: int) -> bool:
    try:
        os.killpg(process_group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    except OSError as exc:
        if exc.errno == errno.ESRCH:
            return False
        return True
    return True


def signal_process_group(process_group_id: int, signum: signal.Signals) -> bool:
    try:
        os.killpg(process_group_id, signum)
    except ProcessLookupError:
        return False
    return True


def run_child(argv: list[str], *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    remaining = max(0.1, deadline - time.monotonic())
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=ROOT,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    stdout = ""
    stderr = ""
    timed_out = False
    term_sent = False
    kill_sent = False
    cleanup_error = ""
    try:
        stdout, stderr = process.communicate(timeout=min(timeout, remaining))
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = output_text(exc.stdout)
        stderr = output_text(exc.stderr)
        try:
            term_sent = signal_process_group(process.pid, signal.SIGTERM)
            try:
                term_stdout, term_stderr = process.communicate(timeout=1.0)
                stdout = output_text(term_stdout) or stdout
                stderr = output_text(term_stderr) or stderr
            except subprocess.TimeoutExpired as term_exc:
                stdout = output_text(term_exc.stdout) or stdout
                stderr = output_text(term_exc.stderr) or stderr
                kill_sent = signal_process_group(process.pid, signal.SIGKILL)
                try:
                    kill_stdout, kill_stderr = process.communicate(timeout=2.0)
                    stdout = output_text(kill_stdout) or stdout
                    stderr = output_text(kill_stderr) or stderr
                except subprocess.TimeoutExpired as kill_exc:
                    stdout = output_text(kill_exc.stdout) or stdout
                    stderr = output_text(kill_exc.stderr) or stderr
                    # The process group should already be gone after SIGKILL;
                    # retain one final bounded reap attempt for a platform
                    # that delayed delivery of the group signal.
                    try:
                        process.kill()
                        kill_sent = True
                        final_stdout, final_stderr = process.communicate(timeout=1.0)
                        stdout = output_text(final_stdout) or stdout
                        stderr = output_text(final_stderr) or stderr
                    except (OSError, subprocess.TimeoutExpired) as final_exc:
                        cleanup_error = str(final_exc)
        except OSError as cleanup_exc:
            cleanup_error = str(cleanup_exc)
    finally:
        reaped = process.poll() is not None
        if not reaped:
            try:
                process.kill()
                kill_sent = True
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=1.0)
            except subprocess.TimeoutExpired as wait_exc:
                cleanup_error = cleanup_error or str(wait_exc)
            reaped = process.poll() is not None
        survivors = process_group_exists(process.pid)
    result = {
        "argv": argv,
        "returncode": 124 if timed_out else process.returncode,
        "stdout": output_text(stdout)[-8000:],
        "stderr": output_text(stderr)[-8000:],
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "cleanup": {
            "process_group_id": process.pid,
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "reaped": reaped,
            "survivors": survivors,
        },
    }
    if cleanup_error:
        result["cleanup"]["error"] = cleanup_error
    if timed_out:
        result["timeout"] = True
    return result


def run_shipped_workflow(binary: Path, config_dir: Path, fixture: Path, *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    check = run_child(
        [str(binary), "--config-dir", str(config_dir), "session", "--replay", str(fixture)],
        timeout=timeout,
        deadline=deadline,
        env=env,
    )
    combined = check["stdout"] + check["stderr"]
    check["fixture"] = display_path(fixture)
    check["fixture_provenance"] = file_provenance(fixture)
    check["observations"] = {
        "first_assistant_output": "Assistant: Hello there" in combined,
        "second_assistant_output": "Assistant: Second turn reply" in combined,
        "session_closed": "[session closed: healthy_complete]" in combined,
        "terminal_observed": "[session terminal:" in combined,
    }
    return check


def seal_capture(capture: dict) -> dict:
    capture["version"] = 2
    capture.pop("integrity", None)
    coverage = {key: capture[key] for key in ("version", "provider", "session", "records")}
    if capture.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = capture["ends_with_disconnect"]
    digest = hashlib.sha256(
        json.dumps(coverage, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    ).hexdigest()
    capture["integrity"] = {
        "algorithm": "sha256",
        "coverage": "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
        "digest": digest,
    }
    return capture


def write_cli_config(config_dir: Path, *, read_image: bool) -> None:
    config_dir.mkdir(parents=True, exist_ok=True)
    lines = [
        "model:",
        "  provider: openai",
        "  openai:",
        "    model: gpt-realtime",
        "tools:",
        "  list:",
    ]
    for tool_id in DEFAULT_TOOL_IDS:
        enabled = read_image and tool_id == "read_image"
        lines.extend([f"    - id: {tool_id}", f"      enabled: {'true' if enabled else 'false'}"])
    (config_dir / "config.yaml").write_text("\n".join(lines) + "\n", encoding="utf-8")


def fixture_contract(capture: dict) -> dict:
    tool_calls = 0
    tool_results = 0
    response_creates = 0
    audio_appends = 0
    audio_turns = 0
    output_markers = []
    for record in capture.get("records", []):
        payload = record.get("payload") or {}
        record_type = record.get("type")
        if record_type == "response.output_item.added" and payload.get("item", {}).get("type") == "function_call":
            tool_calls += 1
        if record_type == "conversation.item.create" and payload.get("item", {}).get("type") == "function_call_output":
            tool_results += 1
        if record_type == "response.create" and record.get("direction") == "client_to_server":
            response_creates += 1
        if record_type == "input_audio_buffer.append" and record.get("direction") == "client_to_server":
            audio_appends += 1
        if record_type == "response.output_audio_transcript.done":
            transcript = payload.get("transcript")
            if transcript:
                output_markers.append(transcript)
    client_records = [
        record
        for record in capture.get("records", [])
        if record.get("direction") == "client_to_server"
    ]
    position = 0
    while position < len(client_records):
        if client_records[position].get("type") != "input_audio_buffer.append":
            position += 1
            continue
        audio_turns += 1
        while position < len(client_records) and client_records[position].get("type") == "input_audio_buffer.append":
            position += 1
        if position < len(client_records) and client_records[position].get("type") == "input_audio_buffer.commit":
            position += 1
        if position < len(client_records) and client_records[position].get("type") == "response.create":
            position += 1
    return {
        "tool_calls": tool_calls,
        "tool_results": tool_results,
        "response_create_events": response_creates,
        "continuation_response_creates": max(0, response_creates - 1),
        "audio_append_events": audio_appends,
        "audio_turns": audio_turns,
        "output_markers": output_markers,
        "session_closed": any(record.get("type") == "session.closed" for record in capture.get("records", [])),
    }


def materialize_tool_fixture(directory: Path) -> dict:
    image_dir = directory / "tool-image"
    image_dir.mkdir(parents=True, exist_ok=True)
    image_path = image_dir / "fixture.png"
    image_bytes = TOOL_IMAGE_FIXTURE.read_bytes()
    image_path.write_bytes(image_bytes)
    data_url = "data:image/png;base64," + base64.b64encode(image_bytes).decode("ascii")
    result = json.dumps(
        {
            "version": 2,
            "status": "success",
            "mime_type": "image/png",
            "byte_length": len(image_bytes),
            "sha256": hashlib.sha256(image_bytes).hexdigest(),
            "typed_projection": "input_image",
        },
        separators=(",", ":"),
    )
    capture = json.loads(TOOL_REPLAY_FIXTURE.read_text(encoding="utf-8"))

    def rewrite(value: object) -> object:
        if isinstance(value, dict):
            return {key: rewrite(child) for key, child in value.items()}
        if isinstance(value, list):
            return [rewrite(child) for child in value]
        if isinstance(value, str):
            return (
                value.replace("__READ_IMAGE_PATH__", str(image_path))
                .replace("__READ_IMAGE_DATA_URL__", data_url)
                .replace("__READ_IMAGE_RESULT__", result)
            )
        return value

    capture = rewrite(capture)
    assert isinstance(capture, dict)
    seal_capture(capture)
    fixture = directory / "tool-continuation.session.json"
    fixture.write_text(json.dumps(capture, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    malformed_capture = json.loads(fixture.read_text(encoding="utf-8"))
    mutated = False
    for record in malformed_capture.get("records", []):
        payload = record.get("payload") or {}
        item = payload.get("item") or {}
        if item.get("type") == "function_call_output":
            item["output"] = ""
            mutated = True
            break
    if not mutated:
        raise RuntimeError("tool continuation fixture has no function_call_output mutation target")
    seal_capture(malformed_capture)
    malformed_fixture = directory / "tool-continuation-malformed.session.json"
    malformed_fixture.write_text(json.dumps(malformed_capture, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return {
        "config_dir": directory / "tool-config",
        "fixture": fixture,
        "malformed_fixture": malformed_fixture,
        "image_dir": image_dir,
        "image": image_path,
        "prompt": TOOL_PROMPT_TEMPLATE.format(image_path=image_path),
        "contract": fixture_contract(capture),
        "template": file_provenance(TOOL_REPLAY_FIXTURE),
    }


def run_shipped_tool_workflow(binary: Path, prepared: dict, *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    write_cli_config(prepared["config_dir"], read_image=True)
    check = run_child(
        [
            str(binary),
            "--config-dir",
            str(prepared["config_dir"]),
            "--workdir",
            str(prepared["image_dir"]),
            "session",
            "--replay",
            str(prepared["fixture"]),
            "--provider",
            "openai",
            "--model",
            "gpt-realtime",
            prepared["prompt"],
        ],
        timeout=timeout,
        deadline=deadline,
        env=env,
    )
    combined = check["stdout"] + check["stderr"]
    contract = prepared["contract"]
    check["fixture"] = display_path(prepared["fixture"])
    check["fixture_provenance"] = file_provenance(prepared["fixture"])
    check["template_provenance"] = prepared["template"]
    check["image_provenance"] = file_provenance(prepared["image"])
    check["contract"] = contract
    check["observations"] = {
        "tool_call_recorded": contract["tool_calls"] == 1,
        "tool_result_recorded": contract["tool_results"] == 1,
        "continuation_recorded": contract["continuation_response_creates"] == 1,
        "grounded_assistant_output": "The image is a one-by-one image with a single indigo pixel." in combined,
        "shutdown_diagnostic": "[session closed:" in combined and "[session terminal:" in combined,
    }
    return check


def run_shipped_malformed_tool_workflow(binary: Path, prepared: dict, *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    check = run_child(
        [
            str(binary),
            "--config-dir",
            str(prepared["config_dir"]),
            "--workdir",
            str(prepared["image_dir"]),
            "session",
            "--replay",
            str(prepared["malformed_fixture"]),
            "--provider",
            "openai",
            "--model",
            "gpt-realtime",
            prepared["prompt"],
        ],
        timeout=timeout,
        deadline=deadline,
        env=env,
    )
    combined = check["stdout"] + check["stderr"]
    lowered = combined.lower()
    check["fixture"] = display_path(prepared["malformed_fixture"])
    check["fixture_provenance"] = file_provenance(prepared["malformed_fixture"])
    check["expected_failure"] = True
    check["observations"] = {
        "nonzero_exit": check["returncode"] != 0,
        "replay_mismatch_diagnostic": "replay" in lowered and ("mismatch" in lowered or "diverg" in lowered),
        "grounded_output_absent": "The image is a one-by-one image with a single indigo pixel." not in combined,
        "shutdown_diagnostic": "[session terminal:" in combined,
    }
    return check


def materialize_scheduled_audio_fixture(directory: Path) -> dict:
    directory.mkdir(parents=True, exist_ok=True)
    source = SCHEDULED_AUDIO_FIXTURE_SOURCE.read_text(encoding="utf-8")
    marker = "const audioTurnReplayFixtureJSON = `"
    start = source.find(marker)
    if start < 0:
        raise RuntimeError(f"scheduled audio fixture marker is absent from {SCHEDULED_AUDIO_FIXTURE_SOURCE}")
    start += len(marker)
    end = source.find("`", start)
    if end < 0:
        raise RuntimeError(f"scheduled audio fixture terminator is absent from {SCHEDULED_AUDIO_FIXTURE_SOURCE}")
    capture = json.loads(source[start:end])
    capture["session"]["fixture_provenance"] = "synthetic"
    seal_capture(capture)
    fixture = directory / "scheduled-tool-continuation.session.json"
    fixture.write_text(json.dumps(capture, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return {
        "fixture": fixture,
        "source": file_provenance(SCHEDULED_AUDIO_FIXTURE_SOURCE),
        "contract": fixture_contract(capture),
    }


def run_shipped_scheduled_workflow(binary: Path, directory: Path, *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    prepared = materialize_scheduled_audio_fixture(directory)
    config_dir = directory / "scheduled-config"
    write_cli_config(config_dir, read_image=False)
    check = run_child(
        [str(binary), "--config-dir", str(config_dir), "session", "--replay", str(prepared["fixture"])],
        timeout=timeout,
        deadline=deadline,
        env=env,
    )
    combined = check["stdout"] + check["stderr"]
    contract = prepared["contract"]
    check["fixture"] = display_path(prepared["fixture"])
    check["fixture_provenance"] = file_provenance(prepared["fixture"])
    check["source_provenance"] = prepared["source"]
    check["contract"] = contract
    check["observations"] = {
        "two_audio_turns_recorded": contract["audio_turns"] == 2,
        "first_scheduled_output": "Carrot grows underground." in combined,
        "follow_on_scheduled_output": any("Six letters in" in marker for marker in contract["output_markers"]),
        "replay_completion_diagnostic": "[session replay complete]" in combined,
        "shutdown_diagnostic": "[session terminal:" in combined,
    }
    return check


def run_cleanup_control(*, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    child = (
        "import signal, subprocess, sys, time\n"
        "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
        "subprocess.Popen([sys.executable, '-c', 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)'])\n"
        "time.sleep(30)\n"
    )
    check = run_child([sys.executable, "-c", child], timeout=timeout, deadline=deadline, env=env)
    cleanup = check.get("cleanup", {})
    check["control"] = "process-group-term-kill-reap"
    check["expected_timeout"] = True
    check["observations"] = {
        "timeout_observed": check.get("timeout", False),
        "term_sent": cleanup.get("term_sent", False),
        "kill_sent": cleanup.get("kill_sent", False),
        "reaped": cleanup.get("reaped", False),
        "no_survivors": cleanup.get("survivors") is False,
    }
    check["cleanup_verified"] = all(check["observations"].values()) and "error" not in cleanup
    return check


def check_passed(check: dict) -> bool:
    if check.get("expected_failure"):
        return check.get("returncode") != 0 and all(check.get("observations", {}).values())
    if check.get("expected_timeout"):
        return check.get("cleanup_verified", False)
    return check.get("returncode") == 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=sorted(CASES))
    parser.add_argument("--child-timeout", type=int, default=90)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    args = parser.parse_args()

    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    env = clean_environment()
    source = source_provenance()
    if source["status"]:
        raise RuntimeError(f"source worktree is not clean before shipped replay: {source['status']!r}")
    checks = []
    shipped = None
    malformed = None
    cleanup = None
    prepared_tool = None
    artifact = None
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c79-yui-") as directory:
        directory_path = Path(directory)
        binary = directory_path / "yui"
        config_dir = directory_path / "config"
        build = run_child(["go", "build", "-o", str(binary), "./agent-cli/cmd/yui"], timeout=args.child_timeout, deadline=deadline, env=env)
        checks.append(build)
        if build["returncode"] == 0:
            artifact = file_provenance(binary)
            artifact["built_from_revision"] = source["revision"]
            artifact["built_from_origin_main"] = source["origin_main"]
            checks.append(run_child([str(binary), "interaction", "--help"], timeout=args.child_timeout, deadline=deadline, env=env))
        if time.monotonic() < deadline:
            cleanup = run_cleanup_control(timeout=min(args.child_timeout, 10), deadline=deadline, env=env)
            checks.append(cleanup)
        if build["returncode"] == 0 and time.monotonic() < deadline:
            healthy = run_shipped_workflow(binary, config_dir, HEALTHY_REPLAY_FIXTURE, timeout=args.child_timeout, deadline=deadline, env=env)
            prepared_tool = materialize_tool_fixture(directory_path / "tool")
            tool = run_shipped_tool_workflow(binary, prepared_tool, timeout=args.child_timeout, deadline=deadline, env=env)
            scheduled = run_shipped_scheduled_workflow(binary, directory_path / "scheduled", timeout=args.child_timeout, deadline=deadline, env=env)
            shipped = {
                "healthy": healthy,
                "tool_continuation": tool,
                "scheduled_follow_on": scheduled,
            }
            checks.extend([healthy, tool, scheduled])
            if args.case == "terminal-and-malformed" and time.monotonic() < deadline:
                malformed_provider = run_child(
                    [str(binary), "--config-dir", str(config_dir), "session", "--replay", str(ERROR_REPLAY_FIXTURE)],
                    timeout=args.child_timeout,
                    deadline=deadline,
                    env=env,
                )
                provider_combined = malformed_provider["stdout"] + malformed_provider["stderr"]
                malformed_provider["fixture"] = display_path(ERROR_REPLAY_FIXTURE)
                malformed_provider["fixture_provenance"] = file_provenance(ERROR_REPLAY_FIXTURE)
                malformed_provider["expected_failure"] = True
                malformed_provider["observations"] = {
                    "nonzero_exit": malformed_provider["returncode"] != 0,
                    "terminal_failure_observed": "terminal_failure" in provider_combined,
                    "shutdown_diagnostic": "[session terminal:" in provider_combined,
                }
                malformed_tool = run_shipped_malformed_tool_workflow(binary, prepared_tool, timeout=args.child_timeout, deadline=deadline, env=env)
                malformed = {
                    "tool_continuation": malformed_tool,
                    "provider_error": malformed_provider,
                }
                checks.extend([malformed_tool, malformed_provider])
            for command in CASES[args.case]:
                if time.monotonic() >= deadline:
                    checks.append({"argv": command, "returncode": 124, "timeout": True, "error": "aggregate timeout exhausted"})
                    break
                checks.append(run_child(command, timeout=args.child_timeout, deadline=deadline, env=env))

    if shipped is None:
        raise RuntimeError("shipped binary workflows did not run")
    for workflow_name, workflow in shipped.items():
        if workflow["returncode"] != 0 or not all(workflow["observations"].values()):
            raise RuntimeError(f"shipped {workflow_name} workflow did not prove its lifecycle contract: {json.dumps(workflow, sort_keys=True)}")
    if malformed is not None:
        for workflow_name, workflow in malformed.items():
            if not check_passed(workflow):
                raise RuntimeError(f"shipped malformed {workflow_name} workflow did not fail closed: {json.dumps(workflow, sort_keys=True)}")
    post_revision = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    if post_revision != source["revision"]:
        raise RuntimeError(f"candidate revision changed during replay: started {source['revision']}, ended {post_revision}")
    result = {
        "schema": "audio-runtime-c79-replay/v2",
        "case": args.case,
        "credential_free": True,
        "software_replay_only": True,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "source": source,
        "final_head": post_revision,
        "final_head_matches_built_source": artifact is not None and artifact.get("built_from_revision") == post_revision,
        "artifact": artifact,
        "fixtures": {
            "healthy": file_provenance(HEALTHY_REPLAY_FIXTURE),
            "malformed": file_provenance(ERROR_REPLAY_FIXTURE),
            "tool_template": file_provenance(TOOL_REPLAY_FIXTURE),
            "tool_image": file_provenance(TOOL_IMAGE_FIXTURE),
            "scheduled_fixture_source": file_provenance(SCHEDULED_AUDIO_FIXTURE_SOURCE),
        },
        "shipped_workflow": shipped,
        "malformed_workflow": malformed,
        "cleanup_control": cleanup,
        "checks": checks,
    }
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    (EVIDENCE / f"{args.case}.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if all(check_passed(check) for check in checks) else 1


if __name__ == "__main__":
    raise SystemExit(main())
