#!/usr/bin/env python3
"""Run bounded, credential-free shipped-YUI terminal diagnostics evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import tempfile
import time
from typing import Any


EVIDENCE = Path(__file__).resolve().parent
ROOT = next(parent for parent in EVIDENCE.parents if (parent / "go.work").is_file())
YUI = EVIDENCE / "artifacts/yui"
RUNS = EVIDENCE / "runs"

BASELINE_REVISION = "3963bc3566da24f8214634c17a9d0f79a6724171"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SESSION_FIXTURE_ROOT = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
REPLAY_FIXTURE = SESSION_FIXTURE_ROOT / "c16-interruption.session.json"
AUDIO_TOOL_FIXTURE = SESSION_FIXTURE_ROOT / "c16-audio-tool.session.json"
PROVIDER_ERROR_FIXTURE = ROOT / "agent-cli/test/integration/testdata/openai_realtime_error.session.json"

FIXTURE_HASHES = {
    REPLAY_FIXTURE: "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
    AUDIO_TOOL_FIXTURE: "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
    PROVIDER_ERROR_FIXTURE: "45477407488f387b2ff9651f1e7f2337df2ca5788ed8a47b8eaa0ca0b21da1c3",
}
CASES = ("replay-terminal", "user-cancellation", "provider-error", "existing-audio-tool-replay")

CHILD_OUTPUT_BYTES = 1 << 20
TERMINATION_GRACE_SECONDS = 5.0
CANCELLATION_TIME_SCALE = 20
CANCELLATION_AFTER_SECONDS = 1.5
EXPECTED_AUDIO_TOOL_PCM_SHA = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_value(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
        timeout=10,
    )
    require(result.returncode == 0, f"git {' '.join(args)} failed: {result.stdout.strip()}")
    return result.stdout.strip()


def is_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(
        ["git", "merge-base", "--is-ancestor", ancestor, descendant],
        cwd=ROOT,
        check=False,
        timeout=10,
    ).returncode == 0


def load_json(path: Path) -> dict[str, Any]:
    require(path.is_file(), f"missing JSON evidence artifact: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {error}") from error
    require(isinstance(value, dict), f"JSON artifact is not an object: {path}")
    return value


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def relative(path: Path, root: Path = EVIDENCE) -> str:
    try:
        return str(path.resolve().relative_to(root.resolve()))
    except ValueError:
        return str(path)


def process_group_alive(process_group_id: int) -> bool:
    try:
        os.killpg(process_group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def signal_process_group(process_group_id: int, signal_number: signal.Signals) -> bool:
    try:
        os.killpg(process_group_id, signal_number)
    except ProcessLookupError:
        return False
    except PermissionError:
        return False
    return True


def child_environment(run_directory: Path) -> dict[str, str]:
    home = run_directory / "home"
    temporary = run_directory / "tmp"
    cache = run_directory / "cache"
    for path in (home, temporary, cache):
        path.mkdir(parents=True, exist_ok=True)
    return {
        "PATH": "/usr/bin:/bin",
        "HOME": str(home),
        "TMPDIR": str(temporary),
        "GOCACHE": str(cache),
        "GOWORK": "off",
        "LANG": "C",
        "LC_ALL": "C",
    }


def run_child(
    command: list[str],
    cwd: Path,
    run_directory: Path,
    timeout_seconds: float,
    *,
    interrupt_after: float | None = None,
) -> dict[str, Any]:
    require(0 < timeout_seconds <= 60, "child timeout must be between 1 and 60 seconds")
    if interrupt_after is not None:
        require(0 < interrupt_after < timeout_seconds, "interrupt deadline must be inside child timeout")
    run_directory.mkdir(parents=True, exist_ok=True)
    stdout_path = run_directory / "stdout.log"
    stderr_path = run_directory / "stderr.log"
    environment = child_environment(run_directory)
    started = time.monotonic()
    process: subprocess.Popen[bytes] | None = None
    selector: selectors.BaseSelector | None = None
    output_files = {
        "stdout": stdout_path.open("wb"),
        "stderr": stderr_path.open("wb"),
    }
    observed = {"stdout": 0, "stderr": 0}
    truncated = {"stdout": False, "stderr": False}
    cleanup_errors: list[str] = []
    timed_out = False
    interrupt_sent = False
    term_sent = False
    kill_sent = False
    reap_timed_out = False
    parent_reaped = False

    def unregister(stream: Any) -> None:
        if selector is None:
            return
        try:
            selector.unregister(stream)
        except (KeyError, ValueError):
            pass

    def drain(stream: Any, label: str) -> None:
        while True:
            try:
                data = os.read(stream.fileno(), 64 * 1024)
            except BlockingIOError:
                return
            except OSError as error:
                cleanup_errors.append(f"read {label}: {error}")
                unregister(stream)
                return
            if not data:
                unregister(stream)
                return
            previous = observed[label]
            observed[label] += len(data)
            available = max(0, CHILD_OUTPUT_BYTES - previous)
            if available:
                output_files[label].write(data[:available])
            if len(data) > available:
                truncated[label] = True

    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        selector = selectors.DefaultSelector()
        require(process.stdout is not None and process.stderr is not None, "child pipes were not created")
        os.set_blocking(process.stdout.fileno(), False)
        os.set_blocking(process.stderr.fileno(), False)
        selector.register(process.stdout, selectors.EVENT_READ, "stdout")
        selector.register(process.stderr, selectors.EVENT_READ, "stderr")
        deadline = started + timeout_seconds
        interrupt_deadline = started + interrupt_after if interrupt_after is not None else None
        cleanup_deadline: float | None = None
        kill_deadline: float | None = None

        while True:
            now = time.monotonic()
            if interrupt_deadline is not None and not interrupt_sent and now >= interrupt_deadline:
                try:
                    process.send_signal(signal.SIGINT)
                    interrupt_sent = True
                except OSError as error:
                    cleanup_errors.append(f"send SIGINT: {error}")
                    interrupt_sent = True

            if not timed_out and now >= deadline:
                timed_out = True
                cleanup_deadline = now + TERMINATION_GRACE_SECONDS
                term_sent = signal_process_group(process.pid, signal.SIGTERM)
                if not term_sent:
                    try:
                        process.terminate()
                        term_sent = True
                    except OSError as error:
                        cleanup_errors.append(f"terminate child: {error}")

            if timed_out and cleanup_deadline is not None and now >= cleanup_deadline and not kill_sent:
                kill_sent = signal_process_group(process.pid, signal.SIGKILL)
                if not kill_sent:
                    try:
                        process.kill()
                        kill_sent = True
                    except OSError as error:
                        cleanup_errors.append(f"kill child: {error}")
                kill_deadline = now + TERMINATION_GRACE_SECONDS

            if timed_out and kill_sent and kill_deadline is not None and now >= kill_deadline:
                reap_timed_out = process.poll() is None
                break

            if process.poll() is not None and selector is not None and not selector.get_map():
                break

            next_deadline = deadline
            if interrupt_deadline is not None and not interrupt_sent:
                next_deadline = min(next_deadline, interrupt_deadline)
            if timed_out and cleanup_deadline is not None:
                next_deadline = min(next_deadline, cleanup_deadline)
            if timed_out and kill_deadline is not None:
                next_deadline = min(next_deadline, kill_deadline)
            wait_for = min(0.1, max(0.01, next_deadline - now))
            if selector is not None:
                for key, _ in selector.select(wait_for):
                    drain(key.fileobj, str(key.data))

        if process.poll() is None:
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                reap_timed_out = True
        else:
            parent_reaped = True
    except (OSError, subprocess.SubprocessError) as error:
        cleanup_errors.append(str(error))
    finally:
        if selector is not None:
            for key in list(selector.get_map().values()):
                drain(key.fileobj, str(key.data))
            selector.close()
        for stream in (process.stdout, process.stderr) if process is not None else ():
            if stream is not None:
                try:
                    stream.close()
                except OSError:
                    pass
        for output_file in output_files.values():
            output_file.close()

    returncode = process.returncode if process is not None else None
    if process is not None and returncode is not None:
        parent_reaped = True
    group_alive_after = process_group_alive(process.pid) if process is not None else False
    if group_alive_after and process is not None:
        if signal_process_group(process.pid, signal.SIGKILL):
            kill_sent = True
        else:
            cleanup_errors.append("process group remained alive after bounded cleanup")
        end = time.monotonic() + 1
        while process_group_alive(process.pid) and time.monotonic() < end:
            time.sleep(0.05)
        group_alive_after = process_group_alive(process.pid)

    return {
        "command": command,
        "cwd": str(cwd),
        "environment": {"GOWORK": "off", "credential_free_allowlist": True},
        "returncode": returncode,
        "timed_out": timed_out,
        "interrupt_sent": interrupt_sent,
        "stdout": relative(stdout_path),
        "stderr": relative(stderr_path),
        "stdout_bytes": observed["stdout"],
        "stderr_bytes": observed["stderr"],
        "stdout_truncated": truncated["stdout"],
        "stderr_truncated": truncated["stderr"],
        "descendants_reaped": not group_alive_after and not reap_timed_out,
        "cleanup": {
            "parent_reaped": parent_reaped,
            "group_alive_after": group_alive_after,
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "reap_timed_out": reap_timed_out,
            "errors": cleanup_errors,
        },
        "duration_seconds": round(time.monotonic() - started, 6),
    }


def output_text(process: dict[str, Any]) -> str:
    stdout = (EVIDENCE / process["stdout"]).read_text(encoding="utf-8", errors="replace")
    stderr = (EVIDENCE / process["stderr"]).read_text(encoding="utf-8", errors="replace")
    return f"{stdout}\n{stderr}"


def write_cancellation_fixture(path: Path) -> dict[str, Any]:
    path.parent.mkdir(parents=True, exist_ok=True)
    value = load_json(REPLAY_FIXTURE)
    for record in value["records"]:
        record["timestamp_ms"] *= CANCELLATION_TIME_SCALE
    coverage = {key: value[key] for key in ("version", "provider", "session", "records")}
    digest = hashlib.sha256(json.dumps(coverage, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
    value["integrity"]["digest"] = digest
    path.write_text(json.dumps(value, separators=(",", ":"), ensure_ascii=False) + "\n", encoding="utf-8")
    return {
        "source": relative(REPLAY_FIXTURE),
        "source_sha256": sha256_file(REPLAY_FIXTURE),
        "time_scale": CANCELLATION_TIME_SCALE,
        "interrupt_after_seconds": CANCELLATION_AFTER_SECONDS,
        "sha256": sha256_file(path),
        "path": relative(path),
    }


def validate_recording(case_directory: Path) -> dict[str, Any]:
    recording = case_directory / "record"
    manifest = load_json(recording / "manifest.json")
    session_log_path = recording / "session-log.jsonl"
    lines = [line for line in session_log_path.read_text(encoding="utf-8").splitlines() if line.strip()]
    require(lines, "session accounting is empty")
    accounting_records = [json.loads(line) for line in lines]
    for accounting in accounting_records:
        require(isinstance(accounting, dict), "session accounting record is not an object")
        require(isinstance(accounting.get("turn_index"), int), "session accounting lacks turn_index")
        response = accounting.get("response")
        require(isinstance(response, dict), "session accounting lacks response snapshot")
        for field in ("text", "complete", "audio_offset_bytes", "audio_bytes"):
            require(field in response, f"session accounting response lacks {field}")

    artifacts = manifest.get("artifacts")
    require(isinstance(artifacts, list) and artifacts, "recording manifest lacks artifacts")
    artifact_hashes: dict[str, str] = {}
    for artifact in artifacts:
        require(isinstance(artifact, dict), "recording manifest has malformed artifact")
        artifact_path = artifact.get("path")
        expected_hash = artifact.get("sha256")
        require(isinstance(artifact_path, str) and isinstance(expected_hash, str), "recording artifact lacks path/hash")
        file_path = recording / artifact_path
        require(file_path.is_file(), f"recording artifact is missing: {file_path}")
        actual_hash = sha256_file(file_path)
        require(actual_hash == expected_hash, f"recording artifact hash changed: {artifact_path}")
        artifact_hashes[artifact_path] = actual_hash
    return {
        "manifest": relative(recording / "manifest.json"),
        "manifest_terminal": manifest.get("terminal"),
        "artifact_hashes": artifact_hashes,
        "accounting": accounting_records[-1],
        "accounting_records": accounting_records,
    }


def terminal_line(process: dict[str, Any]) -> dict[str, str]:
    matches = re.findall(r"\[session terminal: ([^\]]+)\]", output_text(process))
    require(len(matches) == 1, f"expected exactly one session terminal diagnostic, got {len(matches)}")
    fields: dict[str, str] = {}
    for item in matches[0].split():
        key, separator, value = item.partition("=")
        if separator:
            fields[key] = value
    return fields


def validate_common(process: dict[str, Any], recording: dict[str, Any], expected_terminal: dict[str, str]) -> dict[str, Any]:
    require(process["timed_out"] is False, "shipped YUI child timed out")
    require(process["descendants_reaped"] is True, "shipped YUI left a process-group member")
    require(process["stdout_truncated"] is False and process["stderr_truncated"] is False, "shipped YUI output exceeded the evidence bound")
    observed_terminal = terminal_line(process)
    require(observed_terminal == expected_terminal, f"terminal diagnostic changed: {observed_terminal}")
    manifest_terminal = recording["manifest_terminal"]
    require(isinstance(manifest_terminal, dict), "recording terminal manifest is missing")
    require(all(manifest_terminal.get(key) == value for key, value in expected_terminal.items()), f"recording terminal manifest changed: {manifest_terminal}")
    return {"terminal": observed_terminal, "recording": recording}


def run_yui_case(
    case_name: str,
    fixture: Path,
    yui: Path,
    root: Path,
    child_timeout: float,
    *,
    timing: str = "immediate",
    interrupt_after: float | None = None,
    fixture_metadata: dict[str, Any] | None = None,
) -> dict[str, Any]:
    case_directory = root / case_name
    (case_directory / "evidence/runs").mkdir(parents=True, exist_ok=True)
    audio_output = case_directory / "audio.pcm"
    record_directory = case_directory / "record"
    command = [
        str(yui),
        "--workdir",
        str(case_directory),
        "--allow-path",
        str(case_directory),
        "session",
        "--replay",
        str(fixture),
        "--replay-timing",
        timing,
        "--audio-out",
        str(audio_output),
        "--record-dir",
        str(record_directory),
        "--trace-audio",
    ]
    process = run_child(command, case_directory, case_directory / "process", child_timeout, interrupt_after=interrupt_after)
    require(process["returncode"] is not None, f"{case_name} did not produce an exit code")
    recording = validate_recording(case_directory)
    result: dict[str, Any] = {
        "fixture": relative(fixture),
        "fixture_sha256": sha256_file(fixture),
        "fixture_metadata": fixture_metadata or {},
        "process": process,
        "audio_output": {"path": relative(audio_output), "bytes": audio_output.stat().st_size if audio_output.is_file() else 0, "sha256": sha256_file(audio_output) if audio_output.is_file() else None},
    }
    result.update(validate_case(case_name, process, recording, case_directory))
    return result


def validate_case(case_name: str, process: dict[str, Any], recording: dict[str, Any], case_directory: Path) -> dict[str, Any]:
    combined = output_text(process)
    if case_name == "replay-terminal":
        require(process["returncode"] == 0, "replay-terminal exited nonzero")
        require(combined.count("[session replay complete]") == 1, "replay-terminal did not complete exactly once")
        require("replay mismatch" not in combined.lower(), "replay-terminal reported a replay mismatch")
        expected = {"classification": "replay_complete", "terminal_reason": "replay_complete", "terminal_provenance": "replay", "output_state": "complete"}
        result = validate_common(process, recording, expected)
        require(all(item["response"]["complete"] is True for item in recording["accounting_records"]), "replay-terminal final accounting is incomplete")
        return result

    if case_name == "user-cancellation":
        require(process["returncode"] == 0, "user-cancellation exited nonzero")
        require(process["interrupt_sent"] is True, "user-cancellation did not send SIGINT")
        require("[session replay complete]" not in combined, "user-cancellation completed instead of stopping")
        expected = {"classification": "user_cancelled", "terminal_reason": "cancellation", "terminal_provenance": "cli", "output_state": "partial"}
        result = validate_common(process, recording, expected)
        response = recording["accounting"]["response"]
        require(response["complete"] is False and response["audio_bytes"] > 0, "user-cancellation did not retain partial output accounting")
        return result

    if case_name == "provider-error":
        require(process["returncode"] != 0, "provider-error unexpectedly succeeded")
        require("Realtime fixture rejected request" in combined, "provider-error omitted its causal provider message")
        expected = {"classification": "terminal_failure", "terminal_reason": "terminal_failure", "terminal_provenance": "session", "output_state": "none"}
        result = validate_common(process, recording, expected)
        require(all(item["response"]["complete"] is False for item in recording["accounting_records"]), "provider-error final accounting was marked complete")
        return result

    if case_name == "existing-audio-tool-replay":
        require(process["returncode"] == 0, "existing-audio-tool-replay exited nonzero")
        require(combined.count("[session closed: fixture_complete]") == 1, "audio/tool replay did not close exactly once")
        require(combined.count("PROBE_TOOL_MARKER_9182") >= 1, "audio/tool replay omitted tool output")
        require(combined.count("strict replay continuation") == 1, "audio/tool replay omitted assistant output")
        marker = case_directory / "evidence/runs/exec-invocations-v4.log"
        require(marker.is_file() and "PROBE_TOOL_MARKER_9182" in marker.read_text(encoding="utf-8"), "audio/tool replay omitted tool side effect")
        expected = {"classification": "provider_close", "terminal_reason": "provider_close", "terminal_provenance": "provider", "output_state": "not_applicable"}
        result = validate_common(process, recording, expected)
        response = recording["accounting"]["response"]
        require(response["complete"] is True and response["audio_bytes"] == 4800, "audio/tool replay final accounting changed")
        require(recording["artifact_hashes"].get("audio/out-000.pcm") == EXPECTED_AUDIO_TOOL_PCM_SHA, "audio/tool replay PCM changed")
        tool_events = recording["accounting"].get("tool_events")
        require(isinstance(tool_events, list) and len(tool_events) == 2, "audio/tool replay tool accounting changed")
        return result

    raise EvidenceFailure(f"unknown case {case_name}")


def pinned_fixture(path: Path) -> None:
    require(path.is_file(), f"pinned fixture is missing: {path}")
    expected = FIXTURE_HASHES[path]
    require(sha256_file(path) == expected, f"pinned fixture hash changed: {path}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", action="append", choices=CASES, required=True)
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    parser.add_argument("--yui", type=Path, default=YUI)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    require(0 < args.child_timeout <= 60, "child timeout must be between 1 and 60 seconds")
    require(0 < args.aggregate_timeout <= 300, "aggregate timeout must be between 1 and 300 seconds")

    RUNS.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix="session-terminal-", dir=RUNS))
    started = time.monotonic()
    report: dict[str, Any] = {
        "schema": "audio-runtime-c118-shipped-yui-terminal/v1",
        "work": "audio-runtime-c118-retire-cli-session-terminal-diagnostics",
        "status": "failed",
        "run_directory": relative(run_root),
        "requested_cases": args.case,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
    }
    try:
        source_revision = git_value("rev-parse", "HEAD")
        origin_main = git_value("rev-parse", "origin/main")
        require(is_ancestor(origin_main, source_revision), "candidate does not contain fetched origin/main")
        require(is_ancestor(STARTUP_REVISION, source_revision), "candidate lost startup integration ancestry")
        require(is_ancestor(BASELINE_REVISION, source_revision), "candidate lost admitted baseline ancestry")
        yui = args.yui.resolve()
        require(yui.is_file() and os.access(yui, os.X_OK), f"shipped yui executable is missing or not executable: {yui}")
        for fixture in FIXTURE_HASHES:
            pinned_fixture(fixture)
        report.update({
            "source_revision": source_revision,
            "origin_main": origin_main,
            "contains_origin_main": is_ancestor(origin_main, source_revision),
            "contains_startup_revision": True,
            "contains_baseline_revision": True,
            "source_dirty": git_value("status", "--short", "--untracked-files=all"),
            "yui": {"path": str(yui), "sha256": sha256_file(yui)},
            "fixtures": {relative(path): sha256_file(path) for path in FIXTURE_HASHES},
            "child_environment": {"GOWORK": "off", "credential_free_allowlist": True, "ambient_credentials_forwarded": False},
        })

        cases: dict[str, Any] = {}
        for case_name in args.case:
            require(time.monotonic() - started < args.aggregate_timeout, "aggregate evidence timeout exceeded")
            if case_name == "user-cancellation":
                cancellation_path = run_root / case_name / "cancel.session.json"
                metadata = write_cancellation_fixture(cancellation_path)
                cases[case_name] = run_yui_case(case_name, cancellation_path, yui, run_root, args.child_timeout, timing="recorded", interrupt_after=CANCELLATION_AFTER_SECONDS, fixture_metadata=metadata)
            elif case_name == "replay-terminal":
                cases[case_name] = run_yui_case(case_name, REPLAY_FIXTURE, yui, run_root, args.child_timeout)
            elif case_name == "provider-error":
                cases[case_name] = run_yui_case(case_name, PROVIDER_ERROR_FIXTURE, yui, run_root, args.child_timeout)
            else:
                cases[case_name] = run_yui_case(case_name, AUDIO_TOOL_FIXTURE, yui, run_root, args.child_timeout)
        require(time.monotonic() - started < args.aggregate_timeout, "aggregate evidence timeout exceeded")
        report["cases"] = cases
        report["elapsed_seconds"] = round(time.monotonic() - started, 6)
        report["status"] = "passed"
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as error:
        report["error"] = str(error)
        report["elapsed_seconds"] = round(time.monotonic() - started, 6)

    output = args.output.resolve() if args.output else run_root / "report.json"
    write_json(output, report)
    print(json.dumps({"status": report["status"], "report": relative(output)}, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
