#!/usr/bin/env python3
"""Fail-closed verifier for the C127 ordered causal run."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(
    ["git", "rev-parse", "--show-toplevel"], cwd=TASK_ROOT, check=True, capture_output=True, text=True
).stdout.strip())
PINNED_CURRENT_MAIN = "09c70f51243caeaf1184c4806b99bbf7749e3044"
ACCEPTED_C64 = "59af6325614d80173447fe2018a0471e27b4e7b1"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BRANCH = "codex/audio-runtime-c127-repair-current-main-terminal-drain-regression"
LIVE_TEST_FILE = REPO_ROOT / "go-agent-runtime/services/session/internal/live/service_test.go"
ALLOWED_EXACT = {
    "go-agent-runtime/services/session/internal/live/replay.go",
    "go-agent-runtime/services/session/internal/live/service.go",
    "go-agent-runtime/services/session/internal/live/service_test.go",
    "go-agent-runtime/services/session/internal/live/start_support.go",
}
ALLOWED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c127-repair-current-main-terminal-drain-regression/"


class VerificationError(RuntimeError):
    pass


def git_value(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout.strip()


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def require_process(value: object, label: str, expected_returncode: int) -> dict:
    require(isinstance(value, dict), f"{label} execution record is missing")
    require(value.get("returncode") == expected_returncode, f"{label} return code is not {expected_returncode}")
    require(value.get("timed_out") is False, f"{label} timed out")
    require(value.get("output_bounded") is True, f"{label} output was not bounded")
    cleanup = value.get("cleanup")
    require(isinstance(cleanup, dict), f"{label} cleanup is missing")
    require(cleanup.get("parent_reaped") is True, f"{label} parent was not reaped")
    require(cleanup.get("group_alive_after") is False, f"{label} process group survived")
    return value


def verify_trace(trace: object) -> None:
    require(isinstance(trace, list) and len(trace) == 6, "candidate trace must contain six events")
    wanted = ["rtc_forwarding", "provider_receipt", "sink_admission", "device_render", "response_terminal", "graceful_drain"]
    for index, (event, boundary) in enumerate(zip(trace, wanted, strict=True), start=1):
        require(isinstance(event, dict), f"trace event {index} is not an object")
        require(event.get("sequence") == index, f"trace sequence {index} is not explicit")
        require(event.get("boundary") == boundary, f"trace boundary {index} is {event.get('boundary')!r}")
        require(event.get("timing_domain") == "monotonic-process", f"trace timing domain {index} is missing")
        require(isinstance(event.get("elapsed_nanos"), int) and event["elapsed_nanos"] >= 0, f"trace elapsed time {index} is invalid")
        start, end = event.get("sample_start"), event.get("sample_end")
        require(isinstance(start, int) and isinstance(end, int) and 0 <= start <= end <= 6400, f"trace sample range {index} is invalid")


def verify_c64(control: dict) -> None:
    require_process(control, "accepted C64 control", 0)
    require(control.get("source_revision") == ACCEPTED_C64, "accepted C64 source revision changed")
    require(control.get("sequence") == {"provider_events": 17, "tool_calls": 2, "continuation": "two-tool", "final_response": "terminal-drain-final-response", "order": "tool-turn-before-final-audio"}, "C64 provider/tool sequence changed")
    require(control.get("render") == {"provider_samples": 9600, "admitted_samples": 6400, "consumed_samples": 6400, "rendered_samples": 6720, "queued_samples": 0, "underflow_samples": 320, "callback_count": 14, "shutdown": "complete"}, "C64 accounting changed")


def verify_public(report: dict, head: str, origin_main: str) -> None:
    require(report.get("schema") == "audio-runtime.c127.public-run.v1", "public report schema changed")
    report_revision = report.get("candidate_revision")
    require(isinstance(report_revision, str), "public report candidate revision is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", report_revision, head], cwd=REPO_ROOT).returncode == 0, "public report candidate is not an ancestor of HEAD")
    require(report.get("origin_main_at_run") == origin_main, "public report is not bound to fetched origin/main")
    elapsed = report.get("bounds", {}).get("aggregate_elapsed_ms")
    require(isinstance(elapsed, int) and elapsed <= 300000, "public aggregate bound exceeded")
    cases = report.get("cases")
    require(isinstance(cases, dict), "public cases are missing")
    for case_name in ("software-device-tool-drain", "credential-free-audio-tool-replay"):
        case = cases.get(case_name)
        require(isinstance(case, dict), f"public case missing: {case_name}")
        execution = require_process(case.get("execution"), f"public case {case_name}", 0)
        require(execution.get("output_bounded") is True, f"public case {case_name} output was not bounded")
        effects = case.get("effects")
        require(isinstance(effects, dict), f"public effects missing: {case_name}")
        require(effects.get("marker") == "PROBE_TOOL_MARKER_9182", f"public marker changed: {case_name}")
        require(effects.get("continuation") == "strict replay continuation", f"public continuation changed: {case_name}")
        require(effects.get("terminal") == "[session closed: fixture_complete]", f"public terminal changed: {case_name}")
        require(effects.get("provider_terminal") == "provider_close", f"public provider terminal changed: {case_name}")
        require(effects.get("output_pcm_bytes") == 4800, f"public PCM accounting changed: {case_name}")
        require(effects.get("terminal_queue") == 0, f"public terminal queue changed: {case_name}")
        artifact = case.get("artifact")
        require(isinstance(artifact, dict), f"public artifact missing: {case_name}")
        require(artifact.get("sha256") == "8e8db1f19527d10cc7ea53653db95a790f6852199784efe10e011be5f238ab1d", f"public artifact hash changed: {case_name}")
        require(artifact.get("bytes") == 51101938, f"public artifact size changed: {case_name}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("first-divergence", "repair-and-exclusions", "strict-regressions-and-public-runtime", "final"))
    parser.add_argument("--require-current-unmodified", action="store_true")
    parser.add_argument("--require-matched-control", action="store_true")
    parser.add_argument("--base", default=PINNED_CURRENT_MAIN)
    parser.add_argument("--report", type=Path, default=TASK_ROOT / "causal-run.json")
    args = parser.parse_args()
    report = json.loads(args.report.read_text(encoding="utf-8"))
    require(report.get("schema") == "audio-runtime.c127.ordered-causal-run.v1", "causal report schema changed")
    require(report.get("branch") == BRANCH, "causal report branch changed")
    require(git_value("branch", "--show-current") == BRANCH, "isolated branch does not match the manifest")
    require(git_value("status", "--porcelain", "--untracked-files=all") == "", "verification requires a clean committed candidate")
    head = git_value("rev-parse", "HEAD")
    origin_main = git_value("rev-parse", "origin/main")
    report_revision = report.get("candidate_revision")
    require(isinstance(report_revision, str), "causal report candidate revision is missing")
    require(
        subprocess.run(["git", "merge-base", "--is-ancestor", report_revision, head], cwd=REPO_ROOT).returncode == 0,
        "causal report candidate revision is not an ancestor of HEAD",
    )
    require(report.get("origin_main_at_run") == origin_main, "causal report is not bound to fetched origin/main")
    require(report.get("pinned_current_main") == PINNED_CURRENT_MAIN, "pinned current-main control changed")
    require(report.get("accepted_c64") == ACCEPTED_C64, "accepted C64 control changed")
    for ancestor in (PINNED_CURRENT_MAIN, ACCEPTED_C64, STARTUP_INTEGRATION, origin_main):
        require(subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, head], cwd=REPO_ROOT).returncode == 0, f"missing required ancestry {ancestor}")
    elapsed = report.get("bounds", {}).get("aggregate_elapsed_ms")
    require(isinstance(elapsed, int) and elapsed <= 600000, "aggregate causal bound exceeded")

    controls = report.get("controls")
    require(isinstance(controls, dict), "causal controls are missing")
    candidate = require_process(controls.get("candidate_repaired"), "candidate ordered boundary control", 0)
    verify_trace(candidate.get("trace"))
    baseline = require_process(controls.get("current_main_unmodified"), "unmodified current-main negative control", 1)
    require(baseline.get("source_revision") == PINNED_CURRENT_MAIN, "negative control source changed")
    require(baseline.get("test_overlay_sha256") == hashlib.sha256(LIVE_TEST_FILE.read_bytes()).hexdigest(), "negative-control overlay is not the reviewed live test")
    require(baseline.get("failure_marker") in str(baseline.get("output_tail")), "negative control did not prove the first divergence")
    verify_c64(controls.get("accepted_c64_healthy"))
    first = report.get("first_divergence")
    require(isinstance(first, dict) and first.get("boundary") == "rtc_forwarding", "first divergence is not the preclaim RTC boundary")
    require(isinstance(first.get("later_layer_rejected"), list) and len(first["later_layer_rejected"]) >= 3, "later-layer rejection evidence is incomplete")
    allowed_diff = set(filter(None, git_value("diff", "--name-only", "origin/main..HEAD").splitlines()))
    outside = [path for path in allowed_diff if path not in ALLOWED_EXACT and not path.startswith(ALLOWED_PREFIX)]
    require(not outside, "candidate changed paths outside the transferred C127 lease: " + ", ".join(sorted(outside)))
    if args.mode in ("strict-regressions-and-public-runtime", "final"):
        public_path = TASK_ROOT / "public-run.json"
        require(public_path.is_file(), "public report is missing")
        verify_public(json.loads(public_path.read_text(encoding="utf-8")), head, origin_main)
    print(f"C127 ordered causal verification passed: first divergence=rtc_forwarding, candidate={head}, origin/main={origin_main}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError, VerificationError) as error:
        raise SystemExit(f"C127 verification failed: {error}") from error
