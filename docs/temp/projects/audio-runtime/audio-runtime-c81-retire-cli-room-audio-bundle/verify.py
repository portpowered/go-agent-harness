#!/usr/bin/env python3
"""Verify C81 evidence reports, mutation controls, retirement, and scope."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from typing import Any


EVIDENCE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["rtk", "proxy", "git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, check=True, capture_output=True, text=True).stdout.strip()).resolve()
REPORTS = EVIDENCE / "reports"
PROVENANCE = EVIDENCE / "provenance.json"
BRANCH = "codex/audio-runtime-c81-retire-cli-room-audio-bundle"
BASELINE = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
NON_ROOM_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
BASELINE_FILES = (
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_bundle.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_bundle_streams.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_bundle_profile.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_bundle_annotations.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_bundle_wav.go",
)
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/roomaudio/",
    "coverage-manifest/go-agent-runtime/services/roomaudio/",
    "docs/temp/projects/audio-runtime/audio-runtime-c81-retire-cli-room-audio-bundle/",
)
ALLOWED_EXACT = set(BASELINE_FILES) | {
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_golden_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_overlap_golden_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_audio_policy_test.go",
}


class VerificationFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def load(path: Path) -> dict[str, Any]:
    require(path.is_file() and not path.is_symlink(), f"missing evidence: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise VerificationFailure(f"invalid JSON evidence {path}: {error}") from error
    require(isinstance(value, dict), f"evidence is not an object: {path}")
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git(*args: str) -> str:
    result = subprocess.run(["rtk", "proxy", "git", *args], cwd=ROOT, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def execution_ok(value: dict[str, Any], label: str, expected: int = 0) -> None:
    require(value.get("returncode") == expected, f"{label} returned {value.get('returncode')}, want {expected}")
    require(value.get("timed_out") is False and value.get("output_bounded") is True and value.get("disk_bounded") is True, f"{label} exceeded a process/output bound")
    cleanup = value.get("cleanup", {})
    require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("reader_threads_stopped") is True and cleanup.get("group_survivor_detected") is not True, f"{label} left a process or reader survivor")
    require(int(value.get("elapsed_ms", 10**9)) <= 60000, f"{label} exceeded the 60 second child limit")


def verify_artifact(build: dict[str, Any], label: str) -> None:
    artifact = build.get("artifact", {})
    require(isinstance(artifact, dict), f"{label} artifact provenance is missing")
    path = EVIDENCE / str(artifact.get("path", ""))
    require(path.is_file() and not path.is_symlink(), f"{label} artifact is missing")
    require(artifact.get("bytes") == path.stat().st_size and artifact.get("sha256") == sha256_file(path), f"{label} artifact hash is stale")
    require(isinstance(artifact.get("build_input_manifest_sha256"), str) and artifact["build_input_manifest_sha256"], f"{label} build-input binding is missing")


def candidate_bound(value: dict[str, Any], candidate: str, label: str) -> None:
    require(value.get("candidate_revision") == candidate, f"{label} is not bound to candidate {candidate}")


def verify_provenance() -> dict[str, Any]:
    provenance = load(PROVENANCE)
    require(provenance.get("schema") == "audio-runtime.c81.provenance.v1", "provenance schema mismatch")
    require(provenance.get("project") == "audio-runtime" and provenance.get("task") == "audio-runtime-c81-retire-cli-room-audio-bundle" and provenance.get("contract_revision") == "audio-runtime-v1", "provenance admission identity mismatch")
    require(provenance.get("branch") == BRANCH and git("branch", "--show-current") == BRANCH, "provenance branch mismatch")
    candidate = git("rev-parse", "HEAD")
    require(provenance.get("candidate_revision") == candidate, "provenance candidate revision is stale")
    require(provenance.get("baseline_revision") == BASELINE and provenance.get("accepted_main_revision") == BASELINE, "accepted-main provenance is stale")
    require(provenance.get("startup_integration_revision") == STARTUP_INTEGRATION, "startup ancestry provenance is stale")
    require(provenance.get("origin_main_at_run") == git("rev-parse", "origin/main"), "review-time origin/main provenance is stale")
    for ancestor in (STARTUP_INTEGRATION, BASELINE, candidate):
        require(subprocess.run(["rtk", "proxy", "git", "merge-base", "--is-ancestor", ancestor, candidate], cwd=ROOT).returncode == 0, f"missing required ancestry: {ancestor}")
    require(provenance.get("credential_environment", "").startswith("API keys"), "credential scrubbing provenance is missing")
    return provenance


def verify_room_bundle(candidate: str) -> None:
    report = load(REPORTS / "room-audio-bundle.json")
    require(report.get("schema") == "audio-runtime.c81.room-audio-bundle.v1" and report.get("credentials") == "not_used", "room bundle report schema or credential status is invalid")
    candidate_bound(report, candidate, "room bundle report")
    public = report.get("public_bundle", {})
    require(public.get("schema") == "audio-runtime.c81.public-roomaudio-probe.v1" and public.get("workflow") == "public roomaudio/wire", "public roomaudio report is missing")
    require(public.get("participants") == ["alpha", "beta"], "public participant order changed")
    require(public.get("stream_order") == ["alpha:output", "alpha:sent", "alpha:received", "beta:output", "beta:sent", "beta:received", "room:mix"], "public stream order changed")
    require(public.get("sample_counts") == {"alpha:output": 4, "alpha:received": 4, "alpha:sent": 4, "beta:output": 4, "beta:received": 4, "beta:sent": 4, "room:mix": 4}, "public sample counts changed")
    require(public.get("alpha_wav_samples") == [100, 200, 300, 400] and public.get("room_mix_samples") == [900, 1000, 1100, 1200], "public samples changed")
    require(public.get("alpha_delta_ids") == ["alpha-delta-0", "alpha-delta-1"], "public delta order changed")
    require(public.get("overlap_count") == 1 and public.get("barge_in_count") == 1 and public.get("loudness_count") == 1, "public annotation effects are incomplete")
    require(public.get("immutable_views") is True and public.get("clean_shutdown") is True, "public immutability/shutdown effects are incomplete")
    yui = report.get("yui_example", {})
    require(yui.get("schema_version") == 1 and len(yui.get("participants", [])) >= 2, "candidate yui room entrypoint report is incomplete")
    verify_artifact(report.get("build_probe", {}), "roomaudio probe")
    verify_artifact(report.get("build_yui", {}), "yui")
    for execution in report.get("executions", []):
        execution_ok(execution, execution.get("label", "room bundle execution"))


def verify_corrupted(candidate: str) -> None:
    report = load(REPORTS / "corrupted-bundle.json")
    require(report.get("schema") == "audio-runtime.c81.corrupted-bundle.v1" and report.get("credentials") == "not_used", "corrupted report schema or credential status is invalid")
    candidate_bound(report, candidate, "corrupted report")
    value = report.get("report", {})
    require(value.get("rejected") is True and value.get("typed_reconstruction") is True and value.get("participant") == "alpha" and value.get("stream") == "alpha:output", "corrupted bundle rejection lost typed identity")
    require(value.get("first_divergent_byte") == 0 and value.get("clean_shutdown") is True, "corrupted bundle first-divergence/shutdown proof is incomplete")
    verify_artifact(report.get("probe_build", {}), "corrupted probe")
    execution_ok(report.get("execution", {}), "corrupted bundle execution")


def verify_non_room(candidate: str) -> None:
    report = load(REPORTS / "non-room-audio-tool.json")
    require(report.get("schema") == "audio-runtime.c81.non-room-audio-tool.v1" and report.get("credentials") == "not_used", "non-room report schema or credential status is invalid")
    candidate_bound(report, candidate, "non-room report")
    fixture = report.get("fixture", {})
    require(fixture.get("path") == str(NON_ROOM_FIXTURE.relative_to(ROOT)) and fixture.get("bytes") == NON_ROOM_FIXTURE.stat().st_size and fixture.get("sha256") == sha256_file(NON_ROOM_FIXTURE), "non-room fixture provenance changed")
    execution = report.get("execution", {})
    execution_ok(execution, "non-room audio/tool replay")
    output = str(execution.get("stdout", "")) + str(execution.get("stderr", ""))
    require("PROBE_TOOL_MARKER_9182" in output, "non-room audio/tool effect is missing")
    require(isinstance(report.get("record_manifest_sha256"), str) and isinstance(report.get("session_log_sha256"), str), "non-room terminal artifacts are not bound")
    verify_artifact(report.get("build_yui", {}), "non-room yui")


def run_mutation_test(label: str, pattern: str) -> None:
    result = subprocess.run(
        ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/roomaudio/internal/service", "-run", pattern, "-count=1", "-timeout=60s"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        timeout=60,
    )
    require(result.returncode == 0, f"{label} causal test failed: {(result.stdout + result.stderr)[-4000:]}")


def verify_mutation_mode(mode: str, expect_failure: bool) -> None:
    require(expect_failure, f"{mode} must be invoked with --expect-failure")
    patterns = {
        "mutate-audio": "TestLoadRoomReplayAudioBundleRejectsEveryDeltaReconstructionMutation",
        "mutate-format": "TestLoadRoomReplayAudioBundleRejectsMissingHashTimelineAndFormatEvidence",
        "mutate-timeline": "TestLoadRoomReplayAudioBundleRejectsMissingHashTimelineAndFormatEvidence",
        "mutate-tolerance-and-alias": "TestLoadRoomReplayAudioBundleEnforcesAnnotationIdentityAndToleranceBounds",
    }
    run_mutation_test(mode, patterns[mode])


def current_lines(path: str) -> int:
    value = subprocess.run(["rtk", "proxy", "git", "show", f"HEAD:{path}"], cwd=ROOT, check=False, capture_output=True, text=True)
    if value.returncode != 0:
        return 0
    return len(value.stdout.splitlines())


def verify_retirement_and_scope(candidate: str) -> None:
    baseline_counts = []
    for path in BASELINE_FILES:
        result = subprocess.run(["rtk", "proxy", "git", "show", f"{BASELINE}:{path}"], cwd=ROOT, check=True, capture_output=True, text=True)
        baseline_counts.append(len(result.stdout.splitlines()))
    require(baseline_counts == [234, 882, 233, 344, 132] and sum(baseline_counts) == 1825, f"baseline line census changed: {baseline_counts}")
    retained = sum(current_lines(path) for path in BASELINE_FILES)
    require(retained <= 925 and sum(baseline_counts) - retained >= 900, f"CLI retirement is outside the declared floor: retained={retained}")
    adapter = ROOT / BASELINE_FILES[0]
    adapter_text = adapter.read_text(encoding="utf-8") if adapter.is_file() else ""
    require("Deprecated:" in adapter_text, "retained CLI compatibility surface is not explicitly deprecated")
    for marker in ("parseRoomReplay", "decodeRoomReplay", "reconstructRoomReplay", "validateRoomReplay", "mergeRoomReplay", "normalizeRoomReplay"):
        require(marker not in adapter_text, f"retired CLI policy marker remains in adapter: {marker}")

    changed = set(path for path in git("diff", "--name-only", f"{BASELINE}...HEAD").splitlines() if path)
    forbidden = {"scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"}
    require(not changed & forbidden, f"leased shared paths were changed: {sorted(changed & forbidden)}")
    unowned = {path for path in changed if path not in ALLOWED_EXACT and not any(path.startswith(prefix) for prefix in ALLOWED_PREFIXES)}
    require(not unowned, f"candidate changed unowned paths: {sorted(unowned)}")
    candidate_bound(load(REPORTS / "room-audio-bundle.json"), candidate, "room report")
    candidate_bound(load(REPORTS / "corrupted-bundle.json"), candidate, "corruption report")
    candidate_bound(load(REPORTS / "non-room-audio-tool.json"), candidate, "non-room report")


def verify_secret_free() -> None:
    text = "\n".join(path.read_text(encoding="utf-8", errors="replace") for path in [PROVENANCE, *REPORTS.glob("*.json")]).lower()
    for marker in ("sk-", "authorization:", "realtime_api_key"):
        require(marker not in text, f"credential marker retained in evidence: {marker}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("positive-behavior", "mutate-audio", "mutate-format", "mutate-timeline", "mutate-tolerance-and-alias", "retirement-and-scope"))
    parser.add_argument("--expect-failure", action="store_true")
    args = parser.parse_args()
    provenance = verify_provenance()
    candidate = provenance["candidate_revision"]
    if args.mode == "positive-behavior":
        verify_room_bundle(candidate)
    elif args.mode.startswith("mutate-"):
        verify_mutation_mode(args.mode, args.expect_failure)
    else:
        verify_room_bundle(candidate)
        verify_corrupted(candidate)
        verify_non_room(candidate)
        verify_retirement_and_scope(candidate)
        verify_secret_free()
    print(json.dumps({"schema": "audio-runtime.c81.verification.v1", "passed": True, "mode": args.mode, "candidate_revision": candidate}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationFailure, OSError, subprocess.SubprocessError) as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
