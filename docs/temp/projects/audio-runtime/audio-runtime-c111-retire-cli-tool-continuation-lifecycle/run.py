#!/usr/bin/env python3
"""Run bounded source-pinned C111 replay and causal evidence."""

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
FIXTURE_DIR = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
ARTIFACTS = EVIDENCE_DIR / "artifacts"
BASELINE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
FIXTURES = {
    "audio-tool": (FIXTURE_DIR / "c16-audio-tool.session.json", "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"),
    "interruption": (FIXTURE_DIR / "c16-interruption.session.json", "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206"),
}
MARKER = "PROBE_TOOL_MARKER_9182"
MAX_OUTPUT = 1 << 20
CASES = ("missing-tool-continuation", "corrupt-audio-delta", "non-tool-audio-regression")
API_KEY_NAMES = {"OPENAI_API_KEY", "REALTIME_API_KEY", "YUI_API_KEY", "ANTHROPIC_API_KEY"}


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


def environment(gowork: str | None = None) -> dict[str, str]:
    result = os.environ.copy()
    for name in list(result):
        if name in API_KEY_NAMES or name.endswith("_API_KEY"):
            del result[name]
    result["GOWORK"] = gowork or "off"
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


def run_command(label: str, argv: list[str], cwd: Path, env: dict[str, str], timeout: float, budget: float) -> dict[str, Any]:
    remaining = budget - (time.monotonic() - RUN_STARTED)
    require(remaining > 0, f"aggregate timeout expired before {label}")
    timeout = min(timeout, remaining)
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or ""
        stderr = exc.stderr or ""
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


def run_causal_test(label: str, test_name: str, records: list[dict[str, Any]], child_timeout: float, budget: float) -> None:
    record = run_command(
        label,
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-v", "./test/integration", "-run", f"^{test_name}$", "-count=1", "-timeout=360s"],
        AGENT_CLI,
        environment(),
        child_timeout,
        budget,
    )
    require(record["exit_code"] == 0 and not record["timed_out"], f"{label} failed: {record}")
    require(f"--- PASS: {test_name}" in record["stdout"], f"{label} did not expose its named passing causal test")
    records.append(record)


def build_yui(records: list[dict[str, Any]], child_timeout: float, budget: float) -> Path:
    binary = ARTIFACTS / f"yui-{git('rev-parse', 'HEAD')[:12]}"
    record = run_command(
        "build-source-pinned-yui",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"],
        ROOT,
        environment(str(ROOT / "go.work")),
        child_timeout,
        budget,
    )
    require(record["exit_code"] == 0 and not record["timed_out"] and binary.is_file(), f"source-pinned yui build failed: {record}")
    record.update({"source_revision": git("rev-parse", "HEAD"), "binary": str(binary), "binary_sha256": sha256_file(binary), "binary_bytes": binary.stat().st_size})
    records.append(record)
    return binary


def replay(binary: Path, label: str, fixture_name: str, records: list[dict[str, Any]], child_timeout: float, budget: float) -> dict[str, Any]:
    fixture, expected_digest = FIXTURES[fixture_name]
    require(fixture.is_file() and sha256_file(fixture) == expected_digest, f"accepted fixture changed: {fixture}")
    case_dir = ARTIFACTS / f"{label}-{int(time.time())}"
    record_dir = case_dir / "record"
    audio_out = case_dir / "audio.wav"
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    record = run_command(
        f"yui-{label}",
        [str(binary), "session", "--replay", str(fixture), "--audio-out", str(audio_out), "--record-dir", str(record_dir), "--trace-audio", "--workdir", str(case_dir), "--allow-path", str(case_dir)],
        case_dir,
        environment(str(ROOT / "go.work")),
        child_timeout,
        budget,
    )
    require(record["exit_code"] == 0 and not record["timed_out"], f"{label} replay failed: {record}")
    require(audio_out.is_file() and audio_out.stat().st_size > 44, f"{label} did not produce audio output")
    require((record_dir / "manifest.json").is_file() and (record_dir / "session-log.jsonl").is_file(), f"{label} record bundle is incomplete")
    session_records = []
    for line in (record_dir / "session-log.jsonl").read_text(encoding="utf-8").splitlines():
        session_records.append(json.loads(line))
    if fixture_name == "audio-tool":
        marker_log = case_dir / "evidence/runs/exec-invocations-v4.log"
        require(MARKER in record["stdout"] and "strict replay continuation" in record["stdout"], "tool replay omitted final continuation effects")
        require(marker_log.is_file() and MARKER in marker_log.read_text(encoding="utf-8"), "tool replay omitted marker side effect")
    else:
        require(all(not entry.get("tool_events") for entry in session_records), "non-tool audio replay unexpectedly contained tool events")
    record.update({"fixture": str(fixture), "fixture_sha256": expected_digest, "audio_out": str(audio_out), "audio_bytes": audio_out.stat().st_size, "record_dir": str(record_dir), "session_records": len(session_records)})
    records.append(record)
    return record


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
        require(source_revision != "", "source revision was empty")
        require(git("merge-base", "--is-ancestor", BASELINE, "HEAD") == "", "accepted main is not an ancestor")
        if "missing-tool-continuation" in cases:
            run_causal_test("missing-tool-continuation", "TestSessionToolResultConversationMissingContinuationIsBounded", records, args.child_timeout, args.aggregate_timeout)
        if "corrupt-audio-delta" in cases:
            run_causal_test("corrupt-audio-delta", "TestSessionToolResultConversationCorruptAudioDeltaIsRejected", records, args.child_timeout, args.aggregate_timeout)
        needs_yui = "non-tool-audio-regression" in cases or "missing-tool-continuation" in cases
        if needs_yui:
            ARTIFACTS.mkdir(parents=True, exist_ok=True)
            binary = build_yui(records, args.child_timeout, args.aggregate_timeout)
            if "missing-tool-continuation" in cases:
                replay(binary, "tool-continuation-positive", "audio-tool", records, args.child_timeout, args.aggregate_timeout)
            if "non-tool-audio-regression" in cases:
                replay(binary, "non-tool-audio", "interruption", records, args.child_timeout, args.aggregate_timeout)
        require(git("rev-parse", "HEAD") == source_revision, "source changed during replay evidence")
        report = {"schema": "audio-runtime.c111.replay.v1", "status": "accepted", "cases": cases, "source_revision": source_revision, "credential_free": True, "realtime_sessions": 0, "records": records, "duration_seconds": round(time.monotonic() - RUN_STARTED, 6)}
        output = ARTIFACTS / f"run-{int(time.time())}-{os.getpid()}.json"
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        report["artifact"] = str(output)
        print(json.dumps(report, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        print(json.dumps({"schema": "audio-runtime.c111.replay.v1", "status": "failed", "cases": cases, "error": str(exc), "records": records}, sort_keys=True))
        return 1


RUN_STARTED = time.monotonic()


if __name__ == "__main__":
    raise SystemExit(main())
