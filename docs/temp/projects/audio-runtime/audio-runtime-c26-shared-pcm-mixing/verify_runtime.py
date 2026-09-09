#!/usr/bin/env python3
"""Run the C26 public consumer and the accepted credential-free yui regressions."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any


REPO_ROOT = Path(__file__).resolve().parents[5]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT.parents[2])).resolve()
EVIDENCE = Path(__file__).resolve().parent
ARTIFACTS = EVIDENCE / "artifacts"
RUNS = EVIDENCE / "runs"
TOOL_FIXTURE = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe/artifact-2.json"
INTERRUPTION_FIXTURE = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe/artifact-3.json"

EXPECTED_FIXTURES = {
    TOOL_FIXTURE: "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
    INTERRUPTION_FIXTURE: "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
}
EXPECTED_PCM = {
    "tool_rendered": (3200, "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"),
    "tool_provider": (4800, "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"),
    "interruption_rendered": (3360, "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"),
    "interruption_provider": (3840, "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"),
}


class VerificationError(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_hash(path: Path, expected: str) -> str:
    if not path.is_file():
        raise VerificationError(f"missing required file: {path}")
    actual = sha256_file(path)
    if actual != expected:
        raise VerificationError(f"hash mismatch for {path}: got {actual}, want {expected}")
    return actual


def git_output(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def run_process(command: list[str], cwd: Path, timeout_seconds: float) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as exc:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
        raise VerificationError(
            f"timeout after {timeout_seconds:.1f}s: {' '.join(command)}\n"
            f"stdout={stdout}\nstderr={stderr}"
        ) from exc
    duration = time.monotonic() - started
    return {
        "argv": command,
        "cwd": str(cwd),
        "duration_seconds": round(duration, 6),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
    }


def require_success(record: dict[str, Any], label: str) -> None:
    if record["exit_code"] != 0:
        raise VerificationError(
            f"{label} exited {record['exit_code']}\n"
            f"stdout={record['stdout']}\nstderr={record['stderr']}"
        )


def make_run_dir(label: str) -> Path:
    RUNS.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix=f"{label}-", dir=RUNS))
    (directory / "evidence/runs").mkdir(parents=True)
    (directory / "config").mkdir()
    return directory


def yui_command(
    yui: Path,
    fixture: Path,
    output: Path,
    bundle: Path,
    config: Path,
    trace_audio: bool,
) -> list[str]:
    command = [
        str(yui),
        "-C",
        str(config),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(output),
        "--record-dir",
        str(bundle),
        "--max-duration",
        "60s",
    ]
    if trace_audio:
        command.append("--trace-audio")
    return command


def load_session_log(bundle: Path) -> list[dict[str, Any]]:
    path = bundle / "session-log.jsonl"
    if not path.is_file():
        raise VerificationError(f"missing session log: {path}")
    records = []
    for line in path.read_text().splitlines():
        if line.strip():
            records.append(json.loads(line))
    return records


def require_pcm(path: Path, expectation: tuple[int, str], label: str) -> dict[str, Any]:
    expected_size, expected_hash = expectation
    actual_hash = require_hash(path, expected_hash)
    actual_size = path.stat().st_size
    if actual_size != expected_size:
        raise VerificationError(f"{label} size={actual_size}, want {expected_size}")
    return {"path": str(path), "bytes": actual_size, "sha256": actual_hash}


def verify_tool_case(yui: Path, trace_audio: bool) -> dict[str, Any]:
    label = "tool-trace" if trace_audio else "tool-no-trace"
    directory = make_run_dir(label)
    output = directory / "rendered.pcm"
    bundle = directory / "bundle"
    record = run_process(
        yui_command(yui, TOOL_FIXTURE, output, bundle, directory / "config", trace_audio),
        directory,
        60,
    )
    require_success(record, label)
    session_log = load_session_log(bundle)
    if len(session_log) != 1:
        raise VerificationError(f"{label} session records={len(session_log)}, want 1")
    tool_events = session_log[0].get("tool_events", [])
    if len(tool_events) != 2 or tool_events[1].get("status") != "completed":
        raise VerificationError(f"{label} tool lifecycle={tool_events!r}")
    if "strict replay continuation" not in json.dumps(session_log) or "PROBE_TOOL_MARKER_9182" not in json.dumps(session_log):
        raise VerificationError(f"{label} missing expected tool marker or continuation")
    rendered = require_pcm(output, EXPECTED_PCM["tool_rendered"], f"{label} rendered PCM")
    provider = require_pcm(bundle / "audio/out-000.pcm", EXPECTED_PCM["tool_provider"], f"{label} provider PCM")
    if trace_audio:
        timeline = bundle / "audio-trace/timeline.jsonl"
        if not timeline.is_file():
            raise VerificationError(f"{label} missing audio timeline")
    elif (bundle / "audio-trace").exists():
        raise VerificationError(f"{label} unexpectedly created audio-trace")
    return {"capture": record, "run_dir": str(directory), "rendered": rendered, "provider": provider}


def replay_bundle(yui: Path, bundle: Path, expected_phrase: str, label: str) -> dict[str, Any]:
    directory = make_run_dir(f"{label}-directory-replay")
    command = [
        str(yui),
        "-C",
        str(directory / "config"),
        "session",
        "replay",
        str(bundle),
    ]
    record = run_process(command, directory, 60)
    require_success(record, label)
    combined = f"{record['stdout']}\n{record['stderr']}"
    if expected_phrase not in combined:
        raise VerificationError(f"{label} missing {expected_phrase!r}: {combined}")
    return {"replay": record, "run_dir": str(directory)}


def verify_interruption_case(yui: Path) -> dict[str, Any]:
    directory = make_run_dir("interruption")
    output = directory / "rendered.pcm"
    bundle = directory / "bundle"
    record = run_process(
        yui_command(yui, INTERRUPTION_FIXTURE, output, bundle, directory / "config", True),
        directory,
        60,
    )
    require_success(record, "interruption")
    session_log = load_session_log(bundle)
    if len(session_log) != 2:
        raise VerificationError(f"interruption session records={len(session_log)}, want 2")
    if [entry.get("response", {}).get("audio_bytes") for entry in session_log] != [1440, 2400]:
        raise VerificationError(f"interruption audio lifecycle={session_log!r}")
    if not all(entry.get("response", {}).get("complete") for entry in session_log):
        raise VerificationError(f"interruption did not complete both responses: {session_log!r}")
    rendered = require_pcm(output, EXPECTED_PCM["interruption_rendered"], "interruption rendered PCM")
    provider = require_pcm(bundle / "audio/out-000.pcm", EXPECTED_PCM["interruption_provider"], "interruption provider PCM")
    if not (bundle / "audio-trace/timeline.jsonl").is_file():
        raise VerificationError("interruption missing audio timeline")
    replay = replay_bundle(yui, bundle, "Replay verified: 15 wire events, 0 tool calls", "interruption")
    return {"capture": record, "run_dir": str(directory), "rendered": rendered, "provider": provider, **replay}


def verify_no_trace_rejection(yui: Path, no_trace: dict[str, Any]) -> dict[str, Any]:
    bundle = Path(no_trace["run_dir"]) / "bundle"
    directory = make_run_dir("no-trace-strict-control")
    command = [
        str(yui),
        "-C",
        str(directory / "config"),
        "session",
        "replay",
        str(bundle),
    ]
    record = run_process(command, directory, 60)
    combined = f"{record['stdout']}\n{record['stderr']}".lower()
    if record["exit_code"] == 0 or "timeline" not in combined:
        raise VerificationError(f"no-trace strict control did not reject missing timeline: {record}")
    return {"replay": record, "run_dir": str(directory), "expected": "missing timeline rejection"}


def main() -> int:
    report: dict[str, Any] = {
        "project": "audio-runtime",
        "work": "audio-runtime-c26-shared-pcm-mixing",
        "scope": "standalone public consumer plus accepted software regressions",
        "source_revision": None,
        "commands": [],
        "fixtures": {},
        "source_hashes": {},
        "results": {},
    }
    try:
        if not REPO_ROOT.is_dir():
            raise VerificationError(f"repository root not found: {REPO_ROOT}")
        report["source_revision"] = git_output("rev-parse", "HEAD")
        source_paths = [
            "go-audio/pkg/mixer/mixer.go",
            "go-audio/pkg/mixer/pcm_mix.go",
            "agent-cli/internal/room/mixer.go",
            str(Path(__file__).relative_to(REPO_ROOT)),
            str((EVIDENCE / "consumer/main.go").relative_to(REPO_ROOT)),
        ]
        for relative in source_paths:
            path = REPO_ROOT / relative
            report["source_hashes"][relative] = require_hash(path, sha256_file(path))
        for fixture, expected in EXPECTED_FIXTURES.items():
            report["fixtures"][str(fixture)] = require_hash(fixture, expected)

        ARTIFACTS.mkdir(parents=True, exist_ok=True)
        consumer = ARTIFACTS / "pcm-consumer"
        yui = ARTIFACTS / "yui"
        build_consumer = ["go", "build", "-o", str(consumer), "./docs/temp/projects/audio-runtime/audio-runtime-c26-shared-pcm-mixing/consumer/main.go"]
        build_yui = ["go", "build", "-o", str(yui), "./agent-cli/cmd/yui"]
        consumer_build = run_process(build_consumer, REPO_ROOT, 60)
        report["commands"].append(consumer_build)
        require_success(consumer_build, "public consumer build")
        yui_build = run_process(build_yui, REPO_ROOT, 60)
        report["commands"].append(yui_build)
        require_success(yui_build, "yui build")
        report["binaries"] = {
            "consumer": {"path": str(consumer), "sha256": require_hash(consumer, sha256_file(consumer))},
            "yui": {"path": str(yui), "sha256": require_hash(yui, sha256_file(yui))},
        }

        consumer_run = run_process([str(consumer)], REPO_ROOT, 60)
        report["commands"].append(consumer_run)
        require_success(consumer_run, "public consumer")
        try:
            consumer_json = json.loads(consumer_run["stdout"])
        except json.JSONDecodeError as exc:
            raise VerificationError(f"public consumer did not emit JSON: {consumer_run['stdout']}") from exc
        if consumer_json.get("canonical_pcm") != [32766, 3, -32767, 0]:
            raise VerificationError(f"consumer canonical PCM={consumer_json.get('canonical_pcm')!r}")
        if consumer_json.get("canonical_sources") != ["alpha", "beta", "gamma"]:
            raise VerificationError(f"consumer source attribution={consumer_json.get('canonical_sources')!r}")
        if consumer_json.get("tail_pcm") != [7, 8] or not consumer_json.get("tail_end"):
            raise VerificationError(f"consumer tail={consumer_json!r}")
        if consumer_json.get("boundary_pcm") != [] or not consumer_json.get("boundary_end"):
            raise VerificationError(f"consumer boundary={consumer_json!r}")
        if not consumer_json.get("input_isolation") or not consumer_json.get("wall_time_did_not_advance") or not consumer_json.get("clean_shutdown"):
            raise VerificationError(f"consumer lifecycle controls={consumer_json!r}")
        if not consumer_json.get("source_limit_error"):
            raise VerificationError("consumer did not report source-limit failure")
        report["results"]["consumer"] = consumer_json

        tool_trace = verify_tool_case(yui, True)
        report["results"]["tool_trace"] = tool_trace
        report["results"]["tool_trace_directory_replay"] = replay_bundle(
            yui,
            Path(tool_trace["run_dir"]) / "bundle",
            "Replay verified: 18 wire events, 1 tool calls",
            "tool-trace",
        )
        tool_no_trace = verify_tool_case(yui, False)
        report["results"]["tool_no_trace"] = tool_no_trace
        report["results"]["tool_no_trace_strict_control"] = verify_no_trace_rejection(yui, tool_no_trace)
        report["results"]["interruption"] = verify_interruption_case(yui)

        for fixture, expected in EXPECTED_FIXTURES.items():
            actual = require_hash(fixture, expected)
            if report["fixtures"][str(fixture)] != actual:
                raise VerificationError(f"fixture changed during verification: {fixture}")
        report["decision"] = "PASS"
        report["shutdown"] = "all child processes returned within 60s; process groups were reaped"
    except (OSError, subprocess.SubprocessError, VerificationError) as exc:
        report["decision"] = "FAIL"
        report["error"] = str(exc)
        print(json.dumps(report, indent=2, sort_keys=True), file=sys.stderr)
        (EVIDENCE / "verification-report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        return 1
    (EVIDENCE / "verification-report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
