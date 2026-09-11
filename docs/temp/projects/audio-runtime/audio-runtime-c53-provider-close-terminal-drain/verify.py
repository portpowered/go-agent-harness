#!/usr/bin/env python3
"""Verify C53 causal, public, provenance, and bounded-cleanup evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
from pathlib import Path


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=OWNED_ROOT, check=True, capture_output=True, text=True).stdout.strip())
OWNED_REL = OWNED_ROOT.relative_to(REPO_ROOT)
PEER_ROOT = REPO_ROOT.parent / "audio-runtime-c23-long-session-tool-characterization"
C05_ROOT = REPO_ROOT.parent / "audio-runtime-c05-provider-terminal-policy"
REPORT_ROOT = OWNED_ROOT / "reports"
ARTIFACT_ROOT = OWNED_ROOT / "artifacts"
PROVENANCE = OWNED_ROOT / "provenance.json"
BASE_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
C23_HEAD = "6677465e00c9fd052a7c6b6bc69cc17f8e32e02f"
C23_FIRST_FAILURE_SHA = "186d2c5902947582202d33afe1fa0d62c61d38d2d15bc3ae3fad8819f3a7b7f5"
C23_CHILD_REPORT_SHA = "d504b8a6f5a00d049c9af7a43649a98ca1080d6baee832e226bbb0e603bb2819"
EXPECTED_BRANCH = "codex/audio-runtime-c53-provider-close-terminal-drain"
EXPECTED_TRACE = ["started", "SESSION.OPEN", "MESSAGE.START", "TEXT.DELTA", "AUDIO.START", "AUDIO.DELTA", "MESSAGE.END", "MESSAGE.START", "AUDIO.START", "AUDIO.DELTA", "AUDIO.END", "TEXT.DELTA", "MESSAGE.END", "SESSION.CLOSE", "terminal"]
SHIPPED_EXPECTED_BYTES = 9120
SHIPPED_EXPECTED_SHA = "9495487706f4a3001922c10d4dd496e7cd29fff9815a76f2f842f8e970725cc9"
SHIPPED_EXPECTED_TYPES = ["session.update", "session.created", "conversation.item.create", "response.create", "response.created", "response.output_audio.delta", "input_audio_buffer.speech_started", "conversation.item.truncate", "conversation.item.truncated", "response.output_audio.done", "response.done", "response.created", "response.output_audio.delta", "response.output_audio.done", "response.done"]


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def load(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing evidence: {path}")
    try:
        value = json.loads(path.read_text())
    except json.JSONDecodeError as error:
        raise VerificationError(f"invalid evidence JSON {path}: {error}") from error
    require(isinstance(value, dict), f"evidence is not an object: {path}")
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_tree(path: Path) -> str:
    digest = hashlib.sha256()
    for child in sorted(path.rglob("*")):
        if child.is_file() and not child.is_symlink():
            digest.update(str(child.relative_to(path)).encode())
            digest.update(sha256_file(child).encode())
    return digest.hexdigest()


def git_value(*args: str, cwd: Path = REPO_ROOT) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def execution_ok(execution: dict, label: str) -> None:
    require(execution.get("returncode") == 0, f"{label} returned {execution.get('returncode')}")
    require(not execution.get("timed_out") and execution.get("output_bounded") and execution.get("disk_bounded"), f"{label} exceeded a process/output bound")
    cleanup = execution.get("cleanup", {})
    require(cleanup.get("parent_reaped") and cleanup.get("reader_threads_joined") and cleanup.get("group_alive_after") is False, f"{label} left a process or reader survivor")
    require(int(execution.get("elapsed_ms", 10**9)) <= 60000, f"{label} exceeded the 60 second child budget")


def positive_report(path: Path) -> dict:
    payload = load(path)
    report = payload.get("report", payload)
    execution = payload.get("execution")
    if execution is not None:
        execution_ok(execution, path.name)
    require(report.get("schema") == "audio-runtime.c53.v1", f"unexpected public report schema: {path}")
    require(report.get("consumer_surface") == "public-live-service", "public evidence did not use LiveHandle.Events")
    trace = report.get("trace", [])
    require([item.get("kind") for item in trace] == EXPECTED_TRACE, "public terminal trace order is not the causal fixture order")
    require([item.get("response_id", "") for item in trace[2:7]] == ["interrupt-resp-1"] * 5, "interrupted response identity changed")
    require([item.get("response_id", "") for item in trace[7:13]] == ["healthy-resp-2"] * 6, "healthy response identity or order changed")
    require(trace[13].get("response_id", "") == "", "provider SESSION.CLOSE correlation was not normalized before loop assembly")
    require(trace[11].get("text") == "healthy-after-cancel", "healthy TEXT.DELTA content changed")
    require(trace[12].get("terminal_source") == "provider", "healthy MESSAGE.END was not provider-authored")
    interruption = report.get("interruption", {})
    require(interruption.get("cancel_sent") and interruption.get("cancel_response_id") == "interrupt-resp-1", "RESPONSE.CANCEL was not observed at the interrupted response")
    require(interruption.get("cancellation_terminal_observed") and interruption.get("delayed_audio_observed") and interruption.get("delayed_audio_bytes") == 4, "delayed cancelled audio/provider terminal evidence is incomplete")
    require(interruption.get("delayed_audio_publicly_emitted") is False and interruption.get("cancelled_output_rejected") is True, "cancelled audio crossed the public output boundary")
    require(interruption.get("healthy_response_id") == "healthy-resp-2" and interruption.get("healthy_tail_bytes") == 8 and interruption.get("healthy_tail_sha256") == "79761b4bfaef74f2e183877f56ec7ccc0adb8b0e21a9dbb4020748ae8eb5ac6b", "healthy PCM tail effect changed")
    provider = report.get("provider", {})
    require(provider.get("controls") == ["RESPONSE.CANCEL", "RESPONSE.CREATE"], "provider control order changed")
    require(provider.get("delayed_audio_observed") and provider.get("closed"), "provider close boundary was not observed")
    require(report.get("terminal") == {"kind": "terminal", "reason": "fixture_complete", "classification": "fixture", "provenance": "provider", "output_state": "complete"}, "provider terminal metadata/output state changed")
    require(report.get("public_pcm", {}).get("bytes") == 12 and report.get("healthy_pcm", {}).get("bytes") == 8, "public PCM byte effects changed")
    require(report.get("clean_shutdown") and report.get("trace_complete"), "public run did not finish with one final terminal")
    require(report.get("lifecycle", {}).get("opened") and report.get("lifecycle", {}).get("started") and report.get("lifecycle", {}).get("close_called") and report.get("lifecycle", {}).get("wait_called"), "public lifecycle join evidence is incomplete")
    require(not report.get("error"), f"public report retained an error: {report.get('error')}")
    return report


def verify_preserved() -> None:
    first = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/first-failures/0304ec692497ddfc7b0c31f7fd67cd17a7bd3a1e-controls.json"
    child = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/runs/20260911T082822Z-controls-interruption/consumer-report.json"
    require(sha256_file(first) == C23_FIRST_FAILURE_SHA and sha256_file(child) == C23_CHILD_REPORT_SHA, "C23 predecessor artifact hashes changed")
    require(git_value("rev-parse", "HEAD", cwd=PEER_ROOT) == C23_HEAD, "C23 predecessor head changed")
    require(not git_value("status", "--porcelain", "--untracked-files=all", cwd=PEER_ROOT), "C23 predecessor worktree changed")


def verify_provenance(final_scope: bool = False) -> dict:
    provenance = load(PROVENANCE)
    require(provenance.get("schema") == "audio-runtime.c53.provenance.v1", "provenance schema is not C53 v1")
    require(provenance.get("project") == "audio-runtime" and provenance.get("task") == "audio-runtime-c53-provider-close-terminal-drain" and provenance.get("contract_revision") == "audio-runtime-v1", "provenance admission identity changed")
    require(provenance.get("branch") == EXPECTED_BRANCH and git_value("branch", "--show-current") == EXPECTED_BRANCH, "candidate branch is not the admitted isolated branch")
    require(provenance.get("source_revision") == BASE_REVISION and git_value("rev-parse", "origin/main") == BASE_REVISION, "fetched current-main source revision is stale")
    head = git_value("rev-parse", "HEAD")
    candidate = provenance.get("candidate_revision")
    require(isinstance(candidate, str) and candidate, "provenance candidate revision is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", candidate, head], cwd=REPO_ROOT).returncode == 0, "provenance candidate is not an ancestor of the submitted head")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", BASE_REVISION, candidate], cwd=REPO_ROOT).returncode == 0, "current-main ancestry is missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", INTEGRATION_REVISION, candidate], cwd=REPO_ROOT).returncode == 0, "startup integration ancestry is missing")
    preserved = provenance.get("preserved", {})
    require(preserved.get("c23", {}).get("head") == C23_HEAD and preserved.get("c23", {}).get("first_failure_sha256") == C23_FIRST_FAILURE_SHA and preserved.get("c23", {}).get("child_report_sha256") == C23_CHILD_REPORT_SHA, "provenance lost exact C23 preservation")
    verify_preserved()
    if C05_ROOT.is_dir():
        require(provenance.get("preserved", {}).get("c05", {}).get("status") == git_value("status", "--porcelain", "--untracked-files=all", cwd=C05_ROOT), "C05 dirty-state preservation record changed")
    if final_scope:
        require(not provenance.get("source_tree_status"), "final provenance was recorded from a dirty source tree")
        require(not git_value("status", "--porcelain", "--untracked-files=all"), "final candidate tree is dirty")
        if candidate != head:
            descendant_changed = set(git_value("diff", "--name-only", f"{candidate}...{head}").splitlines())
            require(descendant_changed and all(path.startswith(str(OWNED_REL) + "/") for path in descendant_changed), "submitted descendant changed code outside the evidence folder")
        changed = set(git_value("diff", "--name-only", f"{BASE_REVISION}...HEAD").splitlines())
        allowed = {"go-agent-runtime/services/session/internal/live/control.go", "go-agent-runtime/services/session/internal/live/lifecycle.go", "go-agent-runtime/services/session/internal/live/observation.go", "go-agent-runtime/services/session/internal/live/policies_test.go", "go-agent-runtime/services/session/internal/live/service.go", "go-agent-runtime/services/session/internal/live/start_support.go"}
        require(all(path in allowed or path.startswith(str(OWNED_REL) + "/") for path in changed), f"out-of-scope candidate path: {sorted(changed - allowed)}")
    for item in provenance.get("build_inputs", []):
        path = REPO_ROOT / item["path"]
        require(path.is_file() and sha256_file(path) == item["sha256"], f"executable/build input changed after provenance: {item['path']}")
    return provenance


def verify_baseline() -> None:
    verify_preserved()
    baseline = load(ARTIFACT_ROOT / "provider-close-overtake/baseline-first-failure.json")
    execution = baseline.get("execution", {})
    require(execution.get("returncode") != 0 and not execution.get("timed_out") and execution.get("output_bounded") and execution.get("disk_bounded"), "base control did not fail within its bounded child run")
    require(execution.get("cleanup", {}).get("parent_reaped") and execution.get("cleanup", {}).get("reader_threads_joined") and execution.get("cleanup", {}).get("group_alive_after") is False, "base control cleanup did not reap its process group")
    require(baseline.get("expected_outcome") == "base-failure" and baseline.get("source_revision") == BASE_REVISION, "base failure provenance is not current-main")
    report = baseline.get("report", {})
    require(report.get("error") and len(report.get("trace", [])) > 2 and report.get("interruption", {}).get("healthy_response_id") == "" and report.get("terminal", {}).get("provenance") == "loop" and report.get("terminal", {}).get("output_state") == "partial", "base control did not preserve the demonstrated public terminal loss")


def verify_negative() -> None:
    controls = load(REPORT_ROOT / "negative-controls.json")
    require(controls.get("passed") is True, "negative-controls aggregate is not passed")
    entries = controls.get("controls", [])
    require({entry.get("mutation") for entry in entries} == {"missing-message-end", "duplicate-message-end", "reordered-session-close", "admitted-cancelled-audio", "mutated-healthy-pcm"}, "negative control matrix is incomplete")
    for entry in entries:
        execution = entry.get("execution", {})
        require(execution.get("returncode") != 0, f"negative control unexpectedly passed: {entry.get('mutation')}")
        require(execution.get("cleanup", {}).get("parent_reaped") and execution.get("cleanup", {}).get("group_alive_after") is False, f"negative control cleanup failed: {entry.get('mutation')}")
        require(entry.get("report", {}).get("error") and len(entry.get("report", {}).get("trace", [])) > 1, f"negative control failed outside its public oracle: {entry.get('mutation')}")


def verify_shipped() -> None:
    result = load(REPORT_ROOT / "shipped-yui-audio-tool-interruption-replay.json")
    require(result.get("passed") is True, "shipped replay is not passed")
    execution_ok(result.get("execution", {}), "shipped YUI replay")
    audio = result.get("audio_output", {})
    require(audio.get("bytes") == SHIPPED_EXPECTED_BYTES and audio.get("sha256") == SHIPPED_EXPECTED_SHA, "shipped replay PCM changed")
    capture = result.get("capture", {})
    require(capture.get("record_count") == 15 and capture.get("ordered_types") == SHIPPED_EXPECTED_TYPES and capture.get("sequence_contiguous") is True, "shipped tool/interruption/terminal order changed")
    inputs = result.get("inputs", {})
    require(inputs.get("source_audio_sha256") == "6bb5a07350bbe1a874b4a28fcc79f7575f272a38a6338aa223c6fa9a99e6b5c6", "shipped source audio hash changed")
    require(result.get("classification", {}).get("physical_device") == "not_attempted" and result.get("classification", {}).get("acoustic") == "not_attempted", "shipped replay overclaimed physical/acoustic evidence")
    provenance = load(PROVENANCE)
    require(result.get("source_revision") == provenance.get("source_revision") and result.get("binary", {}).get("sha256") == provenance.get("builds", {}).get("yui", {}).get("sha256"), "shipped artifact is not bound to candidate provenance")


def verify_resources() -> None:
    reports = []
    for path in REPORT_ROOT.glob("*.json"):
        reports.append(load(path))
    for path in ARTIFACT_ROOT.rglob("*.json"):
        reports.append(load(path))
    total_ms = 0
    for payload in reports:
        executions = []
        if isinstance(payload.get("execution"), dict):
            executions.append(payload["execution"])
        if isinstance(payload.get("build"), dict) and isinstance(payload["build"].get("result"), dict):
            executions.append(payload["build"]["result"])
        for execution in executions:
            require(int(execution.get("elapsed_ms", 10**9)) <= 60000, "a retained child execution exceeds 60 seconds")
            require(execution.get("output_bounded") is not False and execution.get("disk_bounded") is not False and execution.get("cleanup", {}).get("group_alive_after") is not True, "a retained child execution exceeded cleanup/output bounds")
            total_ms += int(execution.get("elapsed_ms", 0))
    require(total_ms < 600000, f"retained child execution aggregate {total_ms}ms exceeds 600 seconds")
    text = "\n".join(path.read_text(errors="replace") for path in [PROVENANCE, *REPORT_ROOT.glob("*.json")])
    lower = text.lower()
    for forbidden in ("openai_api_key", "authorization:", "sk-", "realtime_api_key"):
        require(forbidden not in lower, f"credential material marker found in evidence: {forbidden}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["baseline-and-first-failure", "terminal-order-and-bounded-cleanup", "public-effects", "provenance-and-resources", "final-scope-provenance-and-budget"])
    args = parser.parse_args()
    if args.mode == "baseline-and-first-failure":
        verify_baseline()
    elif args.mode == "terminal-order-and-bounded-cleanup":
        positive_report(ARTIFACT_ROOT / "provider-close-overtake/candidate.json")
    elif args.mode == "public-effects":
        positive_report(REPORT_ROOT / "public-interruption-provider-close.json")
        verify_negative()
        if (REPORT_ROOT / "shipped-yui-audio-tool-interruption-replay.json").is_file():
            verify_shipped()
    elif args.mode == "provenance-and-resources":
        verify_provenance()
        verify_resources()
    else:
        verify_provenance(final_scope=True)
        verify_resources()
        positive_report(REPORT_ROOT / "public-interruption-provider-close.json")
        verify_negative()
        verify_shipped()
    print(json.dumps({"schema": "audio-runtime.c53.verification.v1", "passed": True, "mode": args.mode}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationError as error:
        print(f"verification failure: {error}")
        raise SystemExit(1)
