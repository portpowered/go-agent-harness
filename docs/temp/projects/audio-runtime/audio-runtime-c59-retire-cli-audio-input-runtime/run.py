#!/usr/bin/env python3
"""Run the built YUI binary through hermetic text, finite-audio, and negative paths."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


EVIDENCE_ROOT = Path(__file__).resolve().parent
ROOT = EVIDENCE_ROOT.parents[4]

POSITIVE_SCENARIO = ROOT / "agent-cli/internal/transport/cli/testdata/probe-scenarios/s2s-v2a-audio-in-basic.scenario.json"
POSITIVE_FIXTURE = ROOT / "agent-cli/internal/transport/cli/testdata/probe-fixtures/s2s_v2a_audio_in_basic.session.json"
NEGATIVE_SCENARIO = ROOT / "agent-cli/internal/transport/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-uncommitted-negative.scenario.json"
NEGATIVE_FIXTURE = ROOT / "agent-cli/internal/transport/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_uncommitted.session.json"


def fail(message: str) -> None:
    raise SystemExit(f"YUI shipped-run verification failed: {message}")


def clean_environment() -> dict[str, str]:
    """Prevent ambient credentials from changing a hermetic evidence run."""
    environment = os.environ.copy()
    for name in ("OPENAI_API_KEY", "XAI_API_KEY", "GROK_API_KEY", "ANTHROPIC_API_KEY"):
        environment.pop(name, None)
    return environment


def run_child(binary: Path, args: list[str], timeout: float) -> dict[str, object]:
    command = [str(binary), *args]
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=ROOT,
        env=clean_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    group_killed = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGKILL)
            group_killed = True
        except ProcessLookupError:
            pass
        stdout, stderr = process.communicate()
    elapsed_ms = round((time.monotonic() - started) * 1000)
    return {
        "argv": args,
        "returncode": process.returncode,
        "timed_out": timed_out,
        "process_group_killed": group_killed,
        "elapsed_ms": elapsed_ms,
        "stdout": stdout,
        "stderr": stderr,
    }


def json_lines(stdout: str) -> list[dict[str, object]]:
    lines = [line for line in stdout.splitlines() if line.strip()]
    try:
        return [json.loads(line) for line in lines]
    except json.JSONDecodeError as error:
        fail(f"YUI emitted non-JSON probe output: {error}: {stdout!r}")
    return []


def first_json_line(output: str, label: str) -> dict[str, object]:
    for line in output.splitlines():
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            return value
    fail(f"{label} did not emit a structured JSON summary: {output!r}")
    return {}


def assert_not_timed_out(result: dict[str, object], label: str) -> None:
    if result["timed_out"]:
        fail(f"{label} exceeded its child timeout and required process-group teardown")
    if result["returncode"] is None:
        fail(f"{label} did not exit")


def run_text_only(binary: Path, timeout: float) -> dict[str, object]:
    result = run_child(binary, ["session", "--help"], timeout)
    assert_not_timed_out(result, "text-only session help")
    if result["returncode"] != 0:
        fail(f"text-only session help exited {result['returncode']}: {result['stderr']}")
    output = str(result["stdout"])
    for marker in ("--audio-in", "--audio-in-turn", "--transport"):
        if marker not in output:
            fail(f"text-only help omitted shipped flag {marker}")
    return {"name": "text-only", "result": result, "assertions": ["help exits zero", "audio flags are discoverable"]}


def run_finite_audio(binary: Path, timeout: float) -> dict[str, object]:
    result = run_child(
        binary,
        [
            "probe",
            "run",
            str(POSITIVE_SCENARIO),
            "--replay",
            str(POSITIVE_FIXTURE),
            "--json",
        ],
        timeout,
    )
    assert_not_timed_out(result, "finite fixture audio")
    if result["returncode"] != 0:
        fail(f"finite fixture audio exited {result['returncode']}: {result['stderr']}")
    lines = json_lines(str(result["stdout"]))
    summaries = json_lines(str(result["stderr"]))
    if len(lines) != 1 or lines[0].get("pass") is not True or len(summaries) != 1 or summaries[0].get("status") != "pass":
        fail(f"finite fixture audio did not produce one passing result and summary: stdout={lines!r} stderr={summaries!r}")
    if int(lines[0].get("frames", 0)) <= 0 or int(lines[0].get("ticks", 0)) <= 0:
        fail(f"finite fixture audio did not exercise frames/ticks: {lines[0]!r}")
    return {
        "name": "finite-audio",
        "result": result,
        "assertions": ["replay is finite and exits zero", "audio fixture produced frames", "probe result passes"],
    }


def run_negative(binary: Path, timeout: float) -> dict[str, object]:
    fixture_result = run_child(
        binary,
        [
            "probe",
            "run",
            str(NEGATIVE_SCENARIO),
            "--replay",
            str(NEGATIVE_FIXTURE),
            "--json",
        ],
        timeout,
    )
    assert_not_timed_out(fixture_result, "uncommitted-buffer negative control")
    if fixture_result["returncode"] == 0:
        fail("uncommitted-buffer negative control unexpectedly passed")
    lines = json_lines(str(fixture_result["stdout"]))
    summary = first_json_line(str(fixture_result["stderr"]), "uncommitted-buffer negative control")
    if len(lines) != 1 or lines[0].get("pass") is not False or summary.get("status") != "fail":
        fail(f"uncommitted-buffer negative control had the wrong structured result: stdout={lines!r} stderr={summary!r}")
    expectations = lines[0].get("expectations")
    if not isinstance(expectations, list) or not any(str(item.get("actual", "")).strip('"') == "uncommitted" for item in expectations if isinstance(item, dict)):
        fail(f"negative control did not expose the uncommitted disposition: {lines[0]!r}")

    webrtc_result = run_child(
        binary,
        ["session", "--transport", "webrtc", "--signaling", "ws://127.0.0.1:1"],
        timeout,
    )
    assert_not_timed_out(webrtc_result, "unsupported WebRTC negative control")
    if webrtc_result["returncode"] == 0 or "not yet customer-usable" not in str(webrtc_result["stderr"]):
        fail(f"unsupported WebRTC path was not rejected deterministically: {webrtc_result!r}")
    return {
        "name": "negative-controls",
        "results": {"uncommitted_buffer": fixture_result, "unsupported_webrtc": webrtc_result},
        "assertions": ["uncommitted fixture exits nonzero with structured failure", "unsupported WebRTC exits before network setup"],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path, help="built yui executable")
    parser.add_argument("--case", choices=("text-only", "finite-audio", "negative-controls", "all"), default="all")
    parser.add_argument("--child-timeout", type=float, default=30.0)
    parser.add_argument("--aggregate-timeout", type=float, default=120.0)
    args = parser.parse_args()

    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        fail(f"binary is missing or not executable: {binary}")
    for path in (POSITIVE_SCENARIO, POSITIVE_FIXTURE, NEGATIVE_SCENARIO, NEGATIVE_FIXTURE):
        if not path.is_file():
            fail(f"fixture is missing: {path}")

    selected = [args.case] if args.case != "all" else ["text-only", "finite-audio", "negative-controls"]
    started = time.monotonic()
    cases: list[dict[str, object]] = []
    for case in selected:
        if time.monotonic() - started > args.aggregate_timeout:
            fail(f"aggregate timeout exceeded before {case}")
        if case == "text-only":
            cases.append(run_text_only(binary, args.child_timeout))
        elif case == "finite-audio":
            cases.append(run_finite_audio(binary, args.child_timeout))
        else:
            cases.append(run_negative(binary, args.child_timeout))

    evidence = {
        "status": "pass",
        "binary": str(binary),
        "case": args.case,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "cases": cases,
    }
    output_dir = EVIDENCE_ROOT / "evidence"
    output_dir.mkdir(parents=True, exist_ok=True)
    output = output_dir / f"yui-{args.case}.json"
    output.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "pass", "case": args.case, "evidence": str(output.relative_to(ROOT))}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
