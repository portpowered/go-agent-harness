#!/usr/bin/env python3
"""Run bounded, credential-free C115 shipped and accumulated regressions."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import signal
import struct
import subprocess
import sys
import time
import wave
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
PROJECT_CONTROL = FACTORY_ROOT / "factory/scripts/project-control.py"
TASK = "audio-runtime-c115-centralize-filesystem-audio-codec"
BRANCH = "codex/audio-runtime-c115-centralize-filesystem-audio-codec"
C21_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
C21_CONSUMER = HERE / "external-consumer"
EXPECTED_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
OUTPUT_CAP = 512 * 1024
CASE_ORDER = (
    "shipped-audio-tool",
    "malformed-truncated",
    "fake-output-overflow",
    "c21-consumption-replay",
    "c50-public-replay",
)


class EvidenceFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def reference(path: pathlib.Path) -> str:
    try:
        return path.relative_to(HERE).as_posix()
    except ValueError:
        return str(path)


def path_from_reference(value: str) -> pathlib.Path:
    path = pathlib.Path(value)
    return path if path.is_absolute() else HERE / path


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def safe_environment(extra: dict[str, str] | None = None) -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    environment = {
        key: value
        for key, value in os.environ.items()
        if not any(part in key.lower() for part in blocked)
    }
    environment.update({"CI": "1", "NO_COLOR": "1"})
    if extra:
        environment.update(extra)
    return environment


def process_group_gone(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def terminate_group(pid: int, sig: signal.Signals) -> None:
    try:
        os.killpg(pid, sig)
    except ProcessLookupError:
        pass


def run_command(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    output_dir: pathlib.Path,
    timeout: int,
    env_extra: dict[str, str] | None = None,
) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    stdout_path = output_dir / f"{label}.stdout"
    stderr_path = output_dir / f"{label}.stderr"
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=safe_environment(env_extra),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    stdout = b""
    stderr = b""
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        terminate_group(process.pid, signal.SIGTERM)
        try:
            tail_stdout, tail_stderr = process.communicate(timeout=3)
            stdout += tail_stdout or b""
            stderr += tail_stderr or b""
        except subprocess.TimeoutExpired as second:
            stdout += second.stdout or b""
            stderr += second.stderr or b""
            terminate_group(process.pid, signal.SIGKILL)
            tail_stdout, tail_stderr = process.communicate(timeout=3)
            stdout += tail_stdout or b""
            stderr += tail_stderr or b""

    stdout = stdout or b""
    stderr = stderr or b""
    output_limited = len(stdout) > OUTPUT_CAP or len(stderr) > OUTPUT_CAP
    stdout_path.write_bytes(stdout[:OUTPUT_CAP])
    stderr_path.write_bytes(stderr[:OUTPUT_CAP])
    return {
        "label": label,
        "argv": argv,
        "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
        "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "stdout_path": reference(stdout_path),
        "stderr_path": reference(stderr_path),
        "stdout_sha256": sha256(stdout_path),
        "stderr_sha256": sha256(stderr_path),
        "process_group_gone": process_group_gone(process.pid),
    }


def output_text(result: dict[str, Any]) -> str:
    return "\n".join(
        path_from_reference(result[key]).read_text(encoding="utf-8", errors="replace")
        for key in ("stdout_path", "stderr_path")
    )


def require_process(result: dict[str, Any], *, exit_code: int = 0) -> None:
    if result["timed_out"] or result["exit_code"] != exit_code or not result["process_group_gone"]:
        raise EvidenceFailure(f"{result['label']} did not finish with a reaped process group: {result}")


def git_value(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        env=safe_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def verify_preflight(run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    branch = git_value("branch", "--show-current")
    if branch != BRANCH:
        raise EvidenceFailure(f"candidate branch {branch!r} does not match PRD branch {BRANCH!r}")
    head = git_value("rev-parse", "HEAD")
    origin_main = git_value("rev-parse", "origin/main")
    ancestry = subprocess.run(
        ["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"],
        cwd=ROOT,
        env=safe_environment(),
        stdin=subprocess.DEVNULL,
        check=False,
    )
    if ancestry.returncode != 0:
        raise EvidenceFailure(f"origin/main {origin_main} is not an ancestor of candidate {head}")

    if not PROJECT_CONTROL.is_file():
        raise EvidenceFailure(f"admission control is unavailable: {PROJECT_CONTROL}")
    admission = run_command(
        "verify-work",
        [
            sys.executable,
            str(PROJECT_CONTROL),
            "verify-work",
            "--type",
            "task",
            "--name",
            TASK,
            "--root",
            str(FACTORY_ROOT),
        ],
        FACTORY_ROOT,
        run_dir / "provenance",
        child_timeout,
    )
    require_process(admission)
    admission_document: dict[str, Any] | None = None
    for line in reversed(output_text(admission).splitlines()):
        try:
            candidate = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(candidate, dict):
            admission_document = candidate
            break
    if not admission_document or admission_document.get("status") != "admitted" or admission_document.get("project") != "audio-runtime":
        raise EvidenceFailure(f"task admission did not prove audio-runtime admission: {admission_document}")

    diff_check = run_command("diff-check", ["git", "diff", "--check"], ROOT, run_dir / "provenance", child_timeout)
    require_process(diff_check)
    return {
        "task": TASK,
        "project": "audio-runtime",
        "branch": branch,
        "candidate_revision": head,
        "origin_main": origin_main,
        "origin_main_is_ancestor": True,
        "admission": admission,
        "admission_document": admission_document,
        "diff_check": diff_check,
    }


def build_artifact(run_dir: pathlib.Path, child_timeout: int) -> tuple[pathlib.Path, dict[str, Any]]:
    artifact = HERE / "artifacts" / "yui"
    artifact.parent.mkdir(parents=True, exist_ok=True)
    build = run_command(
        "build-yui",
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(artifact), "./agent-cli/cmd/yui"],
        ROOT,
        run_dir / "build",
        child_timeout,
    )
    require_process(build)
    if not artifact.is_file() or artifact.stat().st_size == 0:
        raise EvidenceFailure(f"shipped artifact was not produced: {artifact}")
    build["artifact_path"] = reference(artifact)
    build["artifact_sha256"] = sha256(artifact)
    build["artifact_bytes"] = artifact.stat().st_size
    return artifact, build


def write_valid_wav(path: pathlib.Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    samples = [1000 if index % 2 else -1000 for index in range(1600)]
    frames = b"".join(struct.pack("<h", sample) for sample in samples)
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(16000)
        handle.writeframes(frames)


def tool_argv(binary: pathlib.Path, case_dir: pathlib.Path, config_dir: pathlib.Path, args: list[str]) -> list[str]:
    return [
        str(binary),
        "--config-dir",
        str(config_dir),
        "--workdir",
        str(case_dir),
        "--allow-path",
        str(case_dir),
        "tool",
        *args,
    ]


def case_shipped_audio_tool(binary: pathlib.Path, run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    case_dir = run_dir / "shipped-audio-tool"
    config_dir = case_dir / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    valid = case_dir / "valid.wav"
    write_valid_wav(valid)
    marker = "C115_SHIPPED_TEXT_MARKER"
    (case_dir / "marker.txt").write_text(marker + "\n", encoding="utf-8")
    commands = [
        run_command("tool-list", tool_argv(binary, case_dir, config_dir, ["--list"]), case_dir, case_dir / "commands", child_timeout),
        run_command("text-read", tool_argv(binary, case_dir, config_dir, ["read_file", "path=marker.txt"]), case_dir, case_dir / "commands", child_timeout),
        run_command("audio-read", tool_argv(binary, case_dir, config_dir, ["read_file", "path=valid.wav"]), case_dir, case_dir / "commands", child_timeout),
    ]
    for command in commands:
        require_process(command)
    listing = output_text(commands[0])
    text_output = output_text(commands[1])
    audio_output = output_text(commands[2]).lower()
    if "read_file" not in listing:
        raise EvidenceFailure("shipped tool list omitted read_file")
    if marker not in text_output:
        raise EvidenceFailure("shipped text read omitted its positive marker")
    if "audio codec service is not configured" in audio_output:
        raise EvidenceFailure("shipped valid WAV read used an uncomposed runtime service")
    return {
        "passed": True,
        "commands": commands,
        "valid_wav": {"path": reference(valid), "bytes": valid.stat().st_size, "sha256": sha256(valid)},
        "positive_text_marker": marker,
        "positive_audio_read": True,
        "composed_service_diagnostic_absent": True,
        "clean_shutdown": all(command["process_group_gone"] for command in commands),
    }


def case_malformed_truncated(binary: pathlib.Path, run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    case_dir = run_dir / "malformed-truncated"
    config_dir = case_dir / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    valid = case_dir / "source.wav"
    write_valid_wav(valid)
    truncated = case_dir / "truncated.wav"
    truncated.write_bytes(valid.read_bytes()[:12])
    command = run_command(
        "truncated-read",
        tool_argv(binary, case_dir, config_dir, ["read_file", "path=truncated.wav"]),
        case_dir,
        case_dir / "commands",
        child_timeout,
    )
    # read_file deliberately returns model-facing tool failures as a text
    # message, so the process remains zero while the diagnostic is observable.
    require_process(command)
    output = output_text(command)
    lowered = output.lower()
    if "read audio" not in lowered or not any(token in lowered for token in ("decoder rejected", "invalid data", "unsupported format")):
        raise EvidenceFailure(f"truncated WAV did not expose a bounded decoder diagnostic: {output!r}")
    if "audio codec service is not configured" in lowered:
        raise EvidenceFailure("truncated WAV reached an uncomposed runtime service")
    return {
        "passed": True,
        "commands": [command],
        "truncated_input": {"path": reference(truncated), "bytes": truncated.stat().st_size, "sha256": sha256(truncated)},
        "rejection_observed": True,
        "tool_error_as_message_exit_code": command["exit_code"],
        "accepted_pcm_receipt": False,
        "clean_shutdown": command["process_group_gone"],
    }


def case_fake_output_overflow(run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    regex = "^(TestConvertReportsTemporaryCleanupFailure|TestProcessRunnerEnforcesOutputAndStderrBounds)$"
    command = run_command(
        "codec-causal-tests",
        [
            "go",
            "test",
            "-race",
            "./go-agent-runtime/services/audiocodec/internal/service",
            "-run",
            regex,
            "-count=1",
            "-v",
        ],
        ROOT,
        run_dir / "fake-output-overflow",
        child_timeout,
    )
    require_process(command)
    output = output_text(command)
    expected = ["TestConvertReportsTemporaryCleanupFailure", "TestProcessRunnerEnforcesOutputAndStderrBounds"]
    missing = [name for name in expected if f"--- PASS: {name}" not in output]
    if missing:
        raise EvidenceFailure(f"codec causal tests omitted passing tests {missing}: {output}")
    return {
        "passed": True,
        "commands": [command],
        "tests": expected,
        "cleanup_error_identity_asserted": True,
        "stdout_overflow_termination_asserted": True,
        "stderr_overflow_termination_asserted": True,
        "clean_shutdown": command["process_group_gone"],
    }


def fixture_replay(binary: pathlib.Path, case_dir: pathlib.Path, label: str, child_timeout: int) -> dict[str, Any]:
    if not C21_FIXTURE.is_file():
        raise EvidenceFailure(f"pinned C21 fixture is missing: {C21_FIXTURE}")
    replay_dir = case_dir / "replay"
    config_dir = replay_dir / "config"
    record_dir = replay_dir / "tool-record"
    audio_out = replay_dir / "audio.wav"
    (replay_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    config_dir.mkdir(parents=True, exist_ok=True)
    command = run_command(
        label,
        [
            str(binary),
            "--config-dir",
            str(config_dir),
            "--workdir",
            str(replay_dir),
            "--allow-path",
            str(replay_dir),
            "session",
            "--replay",
            str(C21_FIXTURE),
            "--audio-out",
            str(audio_out),
            "--record-dir",
            str(record_dir),
            "--trace-audio",
        ],
        replay_dir,
        replay_dir / "process",
        child_timeout,
    )
    require_process(command)
    output = output_text(command)
    if "PROBE_TOOL_MARKER_9182" not in output or "strict replay continuation" not in output:
        raise EvidenceFailure(f"{label} lost pinned C21 tool/response markers")
    raw_audio = record_dir / "audio" / "out-000.pcm"
    manifest_path = record_dir / "manifest.json"
    session_log = record_dir / "session-log.jsonl"
    if not raw_audio.is_file() or raw_audio.stat().st_size != 4800 or sha256(raw_audio) != EXPECTED_PCM_SHA256:
        raise EvidenceFailure(f"{label} changed the pinned PCM receipt")
    if not audio_out.is_file() or audio_out.stat().st_size <= 44:
        raise EvidenceFailure(f"{label} omitted a non-empty WAV receipt")
    if not manifest_path.is_file() or not session_log.is_file():
        raise EvidenceFailure(f"{label} omitted session manifest or session log")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    terminal = manifest.get("terminal", {})
    if terminal.get("reason") != "fixture_complete" or terminal.get("classification") != "provider_close":
        raise EvidenceFailure(f"{label} terminal evidence changed: {terminal}")
    log_text = session_log.read_text(encoding="utf-8")
    return {
        "process": command,
        "fixture": reference(C21_FIXTURE),
        "fixture_sha256": sha256(C21_FIXTURE),
        "raw_pcm": {"path": reference(raw_audio), "bytes": raw_audio.stat().st_size, "sha256": sha256(raw_audio)},
        "wav_receipt": {"path": reference(audio_out), "bytes": audio_out.stat().st_size, "sha256": sha256(audio_out)},
        "manifest": {"path": reference(manifest_path), "sha256": sha256(manifest_path), "terminal": terminal},
        "session_log": {"path": reference(session_log), "sha256": sha256(session_log), "markers_present": "PROBE_TOOL_MARKER_9182" in log_text and "strict replay continuation" in log_text},
        "clean_shutdown": command["process_group_gone"],
        "credential_free": True,
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
    }


def case_c21_consumption_replay(binary: pathlib.Path, run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    case_dir = run_dir / "c21-consumption-replay"
    consumer_binary = case_dir / "consumer"
    consumer_build = run_command(
        "build-consumer",
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(consumer_binary), "main.go"],
        C21_CONSUMER,
        case_dir / "consumer-build",
        child_timeout,
        env_extra={"GOWORK": "off"},
    )
    require_process(consumer_build)
    if not consumer_binary.is_file():
        raise EvidenceFailure("C115 external consumer artifact was not built")
    consumer_run = run_command("run-consumer", [str(consumer_binary)], case_dir, case_dir / "consumer-run", child_timeout)
    require_process(consumer_run)
    c21_tests = run_command(
        "c21-runtime-tests",
        [
            "go",
            "test",
            "./go-device-gateway/pkg/runtime",
            "-run",
            "^(TestC21ConsumptionBoundary|TestC21ConsumptionInterruptionAndRate|TestC21ConsumptionInterruptionAtIncompleteSpanBoundary|TestC21ObservationBoundsStalledConsumer|TestC21DelayedRenderObservationDoesNotDoubleCountDiscardedPCM|TestC21ObservationIdentityAndLifecycleControls|TestC21ObservationUnsupportedBackendControl)$",
            "-count=1",
            "-v",
        ],
        ROOT,
        case_dir / "runtime-tests",
        child_timeout,
    )
    require_process(c21_tests)
    c21_output = output_text(c21_tests)
    if "--- PASS: TestC21ConsumptionBoundary" not in c21_output or "--- PASS: TestC21ObservationBoundsStalledConsumer" not in c21_output:
        raise EvidenceFailure("C21 runtime regressions did not report their causal boundary tests")
    replay = fixture_replay(binary, case_dir, "c21-yui-replay", child_timeout)
    return {
        "passed": True,
        "commands": [consumer_build, consumer_run, c21_tests, replay["process"]],
        "external_consumer": {"path": reference(consumer_binary), "sha256": sha256(consumer_binary), "bytes": consumer_binary.stat().st_size},
        "runtime_tests": {"command": c21_tests, "required_tests": ["TestC21ConsumptionBoundary", "TestC21ObservationBoundsStalledConsumer"]},
        "replay": replay,
        "credential_free": True,
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
    }


def case_c50_public_replay(binary: pathlib.Path, run_dir: pathlib.Path, child_timeout: int) -> dict[str, Any]:
    case_dir = run_dir / "c50-public-replay"
    config_dir = case_dir / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    top_help = run_command("top-help", [str(binary), "--help"], case_dir, case_dir / "commands", child_timeout)
    session_help = run_command("session-help", [str(binary), "session", "--help"], case_dir, case_dir / "commands", child_timeout)
    tool_list = run_command(
        "public-tool-list",
        tool_argv(binary, case_dir, config_dir, ["--list"]),
        case_dir,
        case_dir / "commands",
        child_timeout,
    )
    for command in (top_help, session_help, tool_list):
        require_process(command)
    top_output = output_text(top_help)
    session_output = output_text(session_help)
    list_output = output_text(tool_list)
    top_literals = ["Available Commands:", "session", "--workdir", "--allow-path"]
    session_literals = ["Usage:", "--replay", "--record-dir", "--audio-in-turn", "--trace-audio"]
    if any(literal not in top_output for literal in top_literals):
        raise EvidenceFailure("C50 top-level public help omitted a required literal")
    if any(literal not in session_output for literal in session_literals):
        raise EvidenceFailure("C50 session public help omitted a required literal")
    if "read_file" not in list_output:
        raise EvidenceFailure("C50 public tool list omitted read_file")
    replay = fixture_replay(binary, case_dir, "c50-local-replay", child_timeout)
    return {
        "passed": True,
        "commands": [top_help, session_help, tool_list, replay["process"]],
        "top_level_help": {"command": top_help, "required_literals": top_literals},
        "session_help": {"command": session_help, "required_literals": session_literals},
        "tool_list": {"command": tool_list, "read_file_present": True},
        "public_replay": replay,
        "credential_free": True,
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
    }


def new_run_dir() -> pathlib.Path:
    base = HERE / "runs"
    base.mkdir(parents=True, exist_ok=True)
    stem = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + f"-{os.getpid()}"
    candidate = base / stem
    suffix = 0
    while candidate.exists():
        suffix += 1
        candidate = base / f"{stem}-{suffix}"
    candidate.mkdir()
    return candidate


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", dest="cases", action="append", choices=CASE_ORDER)
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--aggregate-timeout", type=int, default=360)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.aggregate_timeout <= 0 or args.child_timeout > args.aggregate_timeout:
        parser.error("timeouts must be positive and child-timeout cannot exceed aggregate-timeout")
    cases = list(dict.fromkeys(args.cases or CASE_ORDER))
    started = time.monotonic()
    run_dir = new_run_dir()
    report: dict[str, Any] = {
        "schema_version": "c115-run-v1",
        "task": TASK,
        "run_directory": reference(run_dir),
        "requested_cases": cases,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "decision": "FAILED",
        "script_ci": "not polled; ACCEPTED only hands this exact candidate to the script CI gate",
        "independent_review": "not performed by executor",
        "project_acceptance": "not claimed",
    }
    try:
        report["preflight"] = verify_preflight(run_dir, args.child_timeout)
        needs_binary = any(case in cases for case in ("shipped-audio-tool", "malformed-truncated", "c21-consumption-replay", "c50-public-replay"))
        binary: pathlib.Path | None = None
        if needs_binary:
            binary, build = build_artifact(run_dir, args.child_timeout)
            report["artifact"] = {
                "path": reference(binary),
                "sha256": build["artifact_sha256"],
                "bytes": build["artifact_bytes"],
                "source_revision": report["preflight"]["candidate_revision"],
                "build": build,
            }
            write_json(HERE / "artifacts" / "manifest.json", report["artifact"])
        for case in cases:
            if time.monotonic() - started > args.aggregate_timeout:
                raise EvidenceFailure("aggregate timeout exceeded before the next case")
            if case == "shipped-audio-tool":
                assert binary is not None
                result = case_shipped_audio_tool(binary, run_dir, args.child_timeout)
            elif case == "malformed-truncated":
                assert binary is not None
                result = case_malformed_truncated(binary, run_dir, args.child_timeout)
            elif case == "fake-output-overflow":
                result = case_fake_output_overflow(run_dir, args.child_timeout)
            elif case == "c21-consumption-replay":
                assert binary is not None
                result = case_c21_consumption_replay(binary, run_dir, args.child_timeout)
            elif case == "c50-public-replay":
                assert binary is not None
                result = case_c50_public_replay(binary, run_dir, args.child_timeout)
            else:
                raise EvidenceFailure(f"unknown case: {case}")
            report.setdefault("cases", {})[case] = result
        if time.monotonic() - started > args.aggregate_timeout:
            raise EvidenceFailure("aggregate timeout exceeded after the final case")
        report["decision"] = "ACCEPTED"
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        report["error"] = str(exc)
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
    report_path = run_dir / "report.json"
    write_json(report_path, report)
    write_json(HERE / "runs" / "latest.json", report)
    print(json.dumps({"status": "ok" if report["decision"] == "ACCEPTED" else "failed", "report": reference(report_path), "decision": report["decision"], "candidate_revision": report.get("preflight", {}).get("candidate_revision", "")}, sort_keys=True))
    return 0 if report["decision"] == "ACCEPTED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
