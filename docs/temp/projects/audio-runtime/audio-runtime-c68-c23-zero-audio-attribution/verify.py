#!/usr/bin/env python3
"""Fail-closed verifier for the C68 zero-audio attribution evidence."""

from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path
import subprocess
import sys


OWNED_ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("c68_attribute", OWNED_ROOT / "attribute.py")
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load C68 attribution driver")
ATTRIBUTE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ATTRIBUTE)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def load(path: Path) -> dict:
    return ATTRIBUTE.load_json(path)


def git(*args: str) -> str:
    return ATTRIBUTE.git_value(*args)


def verify_scope() -> None:
    ATTRIBUTE.assert_scope()
    require(git("branch", "--show-current") == ATTRIBUTE.BRANCH, "branch does not match the admitted PRD")
    require(git("rev-parse", "origin/main") == ATTRIBUTE.CURRENT_MAIN, "origin/main is not the admitted current main")
    head = git("rev-parse", "HEAD")
    require(ATTRIBUTE.git_is_ancestor(ATTRIBUTE.STARTUP_REVISION, head), "startup revision ancestry is missing")
    require(ATTRIBUTE.git_is_ancestor(ATTRIBUTE.CURRENT_MAIN, head), "current-main ancestry is missing")
    require(ATTRIBUTE.git_is_ancestor(ATTRIBUTE.BASELINE_REVISION, head), "recording baseline ancestry is missing")
    for entry in ATTRIBUTE.current_status():
        path = ATTRIBUTE.status_path(entry)
        require(path == str(ATTRIBUTE.OWNED_REPO_PATH) or path.startswith(str(ATTRIBUTE.OWNED_REPO_PATH) + "/"), f"out-of-scope worktree change: {path}")


def verify_provenance() -> None:
    verify_scope()
    value = load(ATTRIBUTE.PROVENANCE_PATH)
    require(value.get("schema") == "audio-runtime.c68.zero-audio-attribution.provenance.v1", "provenance schema mismatch")
    require(value.get("task") == ATTRIBUTE.TASK and value.get("project") == ATTRIBUTE.PROJECT, "provenance identity mismatch")
    require(value.get("branch") == ATTRIBUTE.BRANCH, "provenance branch mismatch")
    require(value.get("current_main") == ATTRIBUTE.CURRENT_MAIN and value.get("origin_main_after_fetch") == ATTRIBUTE.CURRENT_MAIN, "provenance current-main pin mismatch")
    require(value.get("c23_revision") == ATTRIBUTE.DEFAULT_C23_REVISION, "C23 revision is not the admitted predecessor")
    require(value.get("c56_revision") == ATTRIBUTE.DEFAULT_C56_REVISION, "C56 revision is not the admitted predecessor")
    head = git("rev-parse", "HEAD")
    require(ATTRIBUTE.git_is_ancestor(value["head"], head), "current head does not preserve the prepared checkpoint")
    require(value.get("ancestry") == {"startup": True, "current_main": True, "baseline": True}, "required ancestry evidence is incomplete")
    require(value.get("owned_paths_only") is True and value.get("production_source_changed") is False, "scope evidence is not fail-closed")
    require(value["fixture"]["sha256"] == ATTRIBUTE.fixture_digest(), "fixture changed after provenance preparation")
    for label, source_path, input_path in (("c23", ATTRIBUTE.C23_TASK_PATH, ATTRIBUTE.INPUT_C23), ("c56", ATTRIBUTE.C56_TASK_PATH, ATTRIBUTE.INPUT_C56)):
        files = ATTRIBUTE.source_task_manifest(value[f"{label}_revision"], source_path, input_path)
        require(files == value["inputs"][label]["files"], f"{label} predecessor evidence manifest changed")
        require(ATTRIBUTE.canonical_digest(files) == value["inputs"][label]["manifest_sha256"], f"{label} predecessor manifest digest changed")
    causal = value["causal_trace"]
    require(all(causal["shared_runtime_files_unchanged"].values()), "C23/C56 shared runtime boundary files diverged unexpectedly")
    print(json.dumps({"status": "verified", "mode": "provenance", "branch": ATTRIBUTE.BRANCH, "head": head}, sort_keys=True))


def verify_oracle_controls() -> None:
    verify_scope()
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    build = load(ATTRIBUTE.BUILD_PATH)
    negative = load(ATTRIBUTE.NEGATIVE_PATH)
    require(build.get("fixture_sha256") == ATTRIBUTE.fixture_digest(), "build fixture digest changed")
    require(build.get("consumer_source_sha256") == negative.get("original_consumer_sha256"), "negative control did not derive from the exact positive consumer")
    require(negative.get("positive_fixture_sha256") == ATTRIBUTE.fixture_digest(), "negative control changed the positive fixture")
    require(negative.get("mutation") == "first-provider-audio" and negative.get("classification") == "oracle_control_rejects_malformed_first_provider_audio", "negative mutation identity mismatch")
    require(negative.get("original_consumer_sha256") != negative.get("mutated_consumer_sha256"), "negative mutation did not change the consumer")
    require(negative.get("passed") is True, "negative control did not pass its rejection assertion")
    require(negative["execution"]["returncode"] != 0 and negative["execution"]["cleanup"]["parent_reaped"] and not negative["execution"]["cleanup"]["group_alive_after"], "negative child was not rejected and reaped")
    require(negative["provider_capture"]["nonempty"] is False and negative["public_pcm"]["bytes"] == 0, "negative mutation did not fail at the first audio oracle boundary")
    print(json.dumps({"status": "verified", "mode": "oracle-controls", "mutation": negative["mutation"]}, sort_keys=True))


def verify_boundaries() -> None:
    verify_scope()
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    require(comparison.get("schema") == "audio-runtime.c68.zero-audio-attribution.comparison.v1", "comparison schema mismatch")
    require(comparison.get("positive_execution_count") == 4, "comparison does not contain exactly four positive executions")
    require(comparison.get("recording_modes") == ["off", "on"] and comparison.get("turns") == 1, "comparison input set is not exactly one off/on turn")
    expected = ATTRIBUTE.expected_pcm()
    expected_sha = ATTRIBUTE.sha256_bytes(expected)
    by_source: dict[str, dict[str, dict]] = {"c23": {}, "c56": {}}
    for item in comparison.get("executions", []):
        require(item.get("source") in by_source and item.get("mode") in ATTRIBUTE.RECORDING_MODES, "unexpected comparison case")
        require(item["mode"] not in by_source[item["source"]], "duplicate comparison case")
        execution = item["execution"]
        require(execution["returncode"] == 0 and not execution["timed_out"] and execution["output_bounded"], f"positive child failed: {item['source']}/{item['mode']}")
        require(execution["cleanup"]["parent_reaped"] and not execution["cleanup"]["group_alive_after"], f"positive child cleanup failed: {item['source']}/{item['mode']}")
        boundaries = item["boundaries"]
        provider = boundaries["provider_capture"]
        public = boundaries["public_pcm"]
        require(provider["nonempty"] and provider["audio_delta_records"] == 1 and provider["bytes"] == len(expected), f"provider audio boundary is not exactly one non-empty frame: {item['source']}/{item['mode']}")
        require(public["bytes"] == len(expected) and public["sha256"] == expected_sha and public["ok"], f"public PCM boundary mismatch: {item['source']}/{item['mode']}")
        require(item.get("public_event_audio_delta_count") == 1, f"public event trace did not contain exactly one audio delta: {item['source']}/{item['mode']}")
        usage = boundaries["recording_usage"]
        require(usage["drops_zero"] is True, f"recording drop counters are non-zero: {item['source']}/{item['mode']}")
        if item["mode"] == "on":
            require(usage["enabled"] and usage["available"] and usage["accepted_audio"] == 0 and usage["audio_bytes"] == 0, f"recording-on audio admission changed: {item['source']}")
            require(usage["queue_items_final"] == 0 and usage["queue_bytes_final"] == 0 and usage["processed_items"] >= usage["accepted_items"], f"recording spool did not drain: {item['source']}")
            semantic = boundaries["semantic_artifacts"]
            require(semantic["manifest"] is not None and semantic["audio_path"]["exists"] is False, f"recording-on semantic artifact boundary changed: {item['source']}")
        else:
            require(not usage["enabled"], f"recording-off unexpectedly enabled semantic recording: {item['source']}")
        by_source[item["source"]][item["mode"]] = item
    require(all(set(cases) == set(ATTRIBUTE.RECORDING_MODES) for cases in by_source.values()), "comparison is missing a source/mode case")
    for label, cases in by_source.items():
        require(cases["off"]["boundaries"]["public_pcm"]["sha256"] == cases["on"]["boundaries"]["public_pcm"]["sha256"], f"{label} public PCM differs between recording modes")
        require(cases["off"]["boundaries"]["provider_capture"]["sha256"] == cases["on"]["boundaries"]["provider_capture"]["sha256"], f"{label} provider PCM differs between recording modes")
    require(comparison.get("accepted_as_evidence") is True, "comparison did not complete as evidence")
    print(json.dumps({"status": "verified", "mode": "boundaries", "positive_executions": 4}, sort_keys=True))


def verify_attribution() -> None:
    verify_scope()
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    attribution = comparison.get("attribution", {})
    require(attribution.get("classification") == "C56_RECORDING_DEFECT", "attribution is not the demonstrated C56 recording defect")
    require(attribution.get("exactly_one_classification") is True, "attribution is not exclusive")
    require(attribution.get("first_divergent_boundary") == "recording_observer_admission", "first divergent boundary changed")
    require("observer.go:71-80" in attribution.get("first_divergent_path", ""), "observer admission path is missing")
    require(attribution.get("c56_candidate_restores_audio") is False, "C56 candidate was incorrectly credited with restoring audio")
    require(attribution.get("c23_runner_repair_needed") is False, "C23 fixture was incorrectly blamed")
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    require(all(provenance["causal_trace"]["shared_runtime_files_unchanged"].values()), "the shared runtime comparison is not identical")
    require("terminalDrainSession" in attribution.get("causal_detail", "") and "SetMediaAttached(true)" in attribution.get("causal_detail", ""), "causal explanation omitted the capability-to-observer transition")
    print(json.dumps({"status": "verified", "mode": "attribution", "classification": attribution["classification"]}, sort_keys=True))


def verify_cleanup() -> None:
    verify_scope()
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    for item in comparison.get("executions", []):
        cleanup = item["execution"]["cleanup"]
        require(cleanup["parent_reaped"] and not cleanup["group_alive_after"], f"live child group remains: {item['source']}/{item['mode']}")
        require(item["boundaries"]["recording_usage"]["queue_items_final"] == 0 and item["boundaries"]["recording_usage"]["queue_bytes_final"] == 0, f"recording queue remains: {item['source']}/{item['mode']}")
    negative = load(ATTRIBUTE.NEGATIVE_PATH)
    require(negative["execution"]["cleanup"]["parent_reaped"] and not negative["execution"]["cleanup"]["group_alive_after"], "negative child group remains")
    regressions = load(ATTRIBUTE.REGRESSIONS_PATH)
    require(regressions.get("passed") is True and regressions.get("normal_count") == 5 and regressions.get("race_count") == 1, "C21 regression evidence is incomplete")
    require(regressions["normal"]["returncode"] == 0 and regressions["race"]["returncode"] == 0, "C21 normal/race regression did not pass")
    evidence_roots = [ATTRIBUTE.OWNED_ROOT / "artifacts" / "runs", ATTRIBUTE.OWNED_ROOT / "artifacts" / "negative-control"]
    require(not [path for root in evidence_roots if root.is_dir() for path in root.rglob("*.lock")], "evidence lock file survived finalization")
    require(comparison["aggregate_elapsed_ms"] <= 300_000, "positive comparison exceeded aggregate bound")
    print(json.dumps({"status": "verified", "mode": "cleanup", "positive_executions": 4}, sort_keys=True))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("provenance", "oracle-controls", "boundaries", "attribution", "cleanup", "all"), required=True)
    args = parser.parse_args()
    modes = ("provenance", "oracle-controls", "boundaries", "attribution", "cleanup") if args.mode == "all" else (args.mode,)
    functions = {
        "provenance": verify_provenance,
        "oracle-controls": verify_oracle_controls,
        "boundaries": verify_boundaries,
        "attribution": verify_attribution,
        "cleanup": verify_cleanup,
    }
    try:
        for mode in modes:
            functions[mode]()
    except (VerificationError, ATTRIBUTE.VerificationError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}, sort_keys=True), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
