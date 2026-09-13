#!/usr/bin/env python3
"""Run bounded, credential-free C119 session and replay evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time
from typing import Any


EVIDENCE_DIR = Path(__file__).resolve().parent
ROOT = EVIDENCE_DIR.parents[4]
AGENT_CLI = ROOT / "agent-cli"
ARTIFACTS = EVIDENCE_DIR / "artifacts"
YUI = ARTIFACTS / "yui"
BASELINE = "3963bc3566da24f8214634c17a9d0f79a6724171"
MAX_OUTPUT = 1 << 20
CASES = ("provider-error", "synthesized-provider-close", "cancellation-negative", "healthy-audio-tool-continuation")
FIXTURES = {
    "provider-error": (ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_failure_auth.session.json", "1ae17d868b52b3eea6e33f9118d6fde51af9bd870012e2eacc2856169386022e"),
    "synthesized-provider-close": (ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6b-error-disconnect-mid-session.session.json", "e9c4f23ff3e53b9a482437d968c222102a745910c050fe66341fdc6eac7fa706"),
    "healthy-audio-tool-continuation": (ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json", "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"),
}
MARKER = "PROBE_TOOL_MARKER_9182"


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def environment(gowork: str) -> dict[str, str]:
    result = os.environ.copy()
    for name in list(result):
        if name.endswith("_API_KEY") or name in {"OPENAI_API_KEY", "REALTIME_API_KEY", "YUI_API_KEY"}:
            del result[name]
    result["GOWORK"] = gowork
    return result


def git(*args: str) -> str:
    result = subprocess.run(["rtk", "proxy", "git", *args], cwd=ROOT, capture_output=True, text=True, check=False, timeout=30)
    require(result.returncode == 0, result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def group_alive(group_id: int) -> bool:
    try:
        os.killpg(group_id, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def run_command(label: str, argv: list[str], cwd: Path, gowork: str, timeout: float, budget: float) -> dict[str, Any]:
    remaining = budget - (time.monotonic() - RUN_STARTED)
    require(remaining > 0, f"aggregate timeout expired before {label}")
    started = time.monotonic()
    process = subprocess.Popen(argv, cwd=cwd, env=environment(gowork), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=min(timeout, remaining))
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout, stderr = exc.stdout or "", exc.stderr or ""
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
    if isinstance(stdout, bytes):
        stdout = stdout.decode(errors="replace")
    if isinstance(stderr, bytes):
        stderr = stderr.decode(errors="replace")
    output_capped = len(stdout) > MAX_OUTPUT or len(stderr) > MAX_OUTPUT
    record = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "duration_seconds": round(time.monotonic() - started, 6),
        "stdout": stdout[:MAX_OUTPUT],
        "stderr": stderr[:MAX_OUTPUT],
        "output_capped": output_capped,
        "reaped": process.poll() is not None,
    }
    require(not output_capped, f"{label} produced unbounded output")
    require(record["reaped"], f"{label} process was not reaped")
    record["group_alive_after_exit"] = group_alive(process.pid)
    require(not record["group_alive_after_exit"], f"{label} left a surviving process group")
    return record


def case_directory(label: str) -> Path:
    path = ARTIFACTS / f"{label}-{time.time_ns()}-{os.getpid()}"
    (path / "evidence/runs").mkdir(parents=True, exist_ok=False)
    return path


def load_manifest(record_dir: Path) -> dict[str, Any]:
    manifest = record_dir / "manifest.json"
    require(manifest.is_file(), f"record bundle is missing {manifest}")
    data = json.loads(manifest.read_text(encoding="utf-8"))
    require((record_dir / "client.transcript.jsonl").is_file(), "client transcript is missing")
    require((record_dir / "agent.transcript.jsonl").is_file(), "agent transcript is missing")
    return data


def replay_case(label: str, binary: Path, records: list[dict[str, Any]], child_timeout: float, budget: float) -> None:
    fixture, expected_digest = FIXTURES[label]
    require(fixture.is_file() and sha256_file(fixture) == expected_digest, f"accepted fixture changed: {fixture}")
    case_dir = case_directory(label)
    record_dir = case_dir / "record"
    audio_out = case_dir / "audio.wav"
    argv = [str(binary), "session", "--replay", str(fixture), "--audio-out", str(audio_out), "--record-dir", str(record_dir), "--workdir", str(case_dir), "--allow-path", str(case_dir)]
    if label == "healthy-audio-tool-continuation":
        argv.insert(2, "--trace-audio")
    record = run_command(label, argv, case_dir, str(ROOT / "go.work"), child_timeout, budget)
    combined = record["stdout"] + record["stderr"]
    manifest = load_manifest(record_dir)
    terminal = manifest.get("terminal", {})
    if label == "provider-error":
        require(record["exit_code"] != 0 and not record["timed_out"], "provider error unexpectedly completed cleanly")
        require(terminal == {"reason": "terminal_failure", "classification": "terminal_failure", "terminal_reason": "terminal_failure", "terminal_provenance": "session", "output_state": "none"}, f"provider error terminal drifted: {terminal}")
        require("terminal_failure" in combined, "provider error emitted no visible terminal evidence")
    elif label == "synthesized-provider-close":
        require(record["exit_code"] != 0 and not record["timed_out"], "provider close did not surface its audio/session failure")
        require(terminal == {"reason": "provider_closed", "classification": "transport", "terminal_reason": "provider_close", "terminal_provenance": "provider", "output_state": "partial"}, f"provider close terminal drifted: {terminal}")
        require("v6b midstream answer cut off when the transport dropped" in combined and "provider_closed" in combined, "provider close omitted observable partial output")
    else:
        require(record["exit_code"] == 0 and not record["timed_out"], "healthy replay failed")
        require(audio_out.is_file() and audio_out.stat().st_size > 44, "healthy replay produced no audio samples")
        marker_log = case_dir / "evidence/runs/exec-invocations-v4.log"
        require(MARKER in record["stdout"] and "strict replay continuation" in record["stdout"], "healthy replay omitted continuation output")
        require(marker_log.is_file() and MARKER in marker_log.read_text(encoding="utf-8"), "healthy replay omitted tool effect")
    record.update({"fixture": str(fixture), "fixture_sha256": expected_digest, "record_dir": str(record_dir), "audio_out": str(audio_out), "audio_bytes": audio_out.stat().st_size if audio_out.is_file() else 0, "terminal": terminal})
    records.append(record)


def cancellation_case(records: list[dict[str, Any]], child_timeout: float, budget: float) -> None:
    test_name = "TestSessionSIGINTCancellationDoesNotSuppressIndependentFailure"
    record = run_command("cancellation-negative", ["rtk", "proxy", "go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", f"^{test_name}$", "-count=1", "-timeout=240s"], ROOT, str(ROOT / "go.work"), child_timeout, budget)
    require(record["exit_code"] == 0 and not record["timed_out"], f"cancellation negative failed: {record}")
    require(f"ok  \tgithub.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime" in record["stdout"], "cancellation negative did not expose a passing oracle")
    records.append(record)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", choices=CASES, dest="cases")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=240.0)
    args = parser.parse_args()
    require(args.child_timeout > 0 and args.aggregate_timeout >= args.child_timeout, "invalid finite timeout budget")
    cases = args.cases or list(CASES)
    global RUN_STARTED
    RUN_STARTED = time.monotonic()
    records: list[dict[str, Any]] = []
    try:
        source_revision = git("rev-parse", "HEAD")
        require(source_revision != "" and git("merge-base", "--is-ancestor", BASELINE, "HEAD") == "", "source is not based on accepted main")
        require(YUI.is_file(), f"source-pinned yui is missing: {YUI}")
        binary_digest = sha256_file(YUI)
        if "provider-error" in cases:
            replay_case("provider-error", YUI, records, args.child_timeout, args.aggregate_timeout)
        if "synthesized-provider-close" in cases:
            replay_case("synthesized-provider-close", YUI, records, args.child_timeout, args.aggregate_timeout)
        if "cancellation-negative" in cases:
            cancellation_case(records, args.child_timeout, args.aggregate_timeout)
        if "healthy-audio-tool-continuation" in cases:
            replay_case("healthy-audio-tool-continuation", YUI, records, args.child_timeout, args.aggregate_timeout)
        require(git("rev-parse", "HEAD") == source_revision, "source changed during replay evidence")
        report = {"schema": "audio-runtime.c119.replay.v1", "status": "accepted", "cases": cases, "source_revision": source_revision, "binary": str(YUI), "binary_sha256": binary_digest, "credential_free": True, "software_replay": True, "realtime_sessions": 0, "records": records, "duration_seconds": round(time.monotonic() - RUN_STARTED, 6)}
        output = ARTIFACTS / f"run-{int(time.time())}-{os.getpid()}.json"
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        report["artifact"] = str(output)
        print(json.dumps(report, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        print(json.dumps({"schema": "audio-runtime.c119.replay.v1", "status": "failed", "cases": cases, "error": str(exc), "records": records}, sort_keys=True))
        return 1


RUN_STARTED = time.monotonic()


if __name__ == "__main__":
    raise SystemExit(main())
