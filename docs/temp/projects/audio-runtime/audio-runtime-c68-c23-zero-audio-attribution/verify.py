#!/usr/bin/env python3
"""Fail-closed verifier for the C68 zero-audio attribution evidence."""

from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path
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
    require(len(head) == 40, "current head is not a full commit SHA")
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
    require(value.get("task") == ATTRIBUTE.TASK and value.get("project") == ATTRIBUTE.PROJECT and value.get("contract") == "audio-runtime-v1", "provenance identity mismatch")
    require(value.get("branch") == ATTRIBUTE.BRANCH, "provenance branch mismatch")
    for key in ("head", "current_main", "origin_main_after_fetch", "startup_revision", "baseline_revision", "c23_revision", "c56_revision"):
        require(isinstance(value.get(key), str) and len(value[key]) == 40, f"provenance {key} is not a full SHA")
        ATTRIBUTE.assert_commit(value[key])
    head = git("rev-parse", "HEAD")
    require(ATTRIBUTE.git_is_ancestor(value["head"], head), "current head does not preserve the prepared checkpoint")
    require(value["current_main"] == ATTRIBUTE.CURRENT_MAIN and value["origin_main_after_fetch"] == ATTRIBUTE.CURRENT_MAIN, "provenance current-main pin mismatch")
    require(value["c23_revision"] == ATTRIBUTE.DEFAULT_C23_REVISION and value["c56_revision"] == ATTRIBUTE.DEFAULT_C56_REVISION, "predecessor revision is not the admitted source")
    require(value.get("ancestry") == {"startup": True, "current_main": True, "baseline": True}, "required ancestry evidence is incomplete")
    require(value.get("owned_paths_only") is True and value.get("production_source_changed") is False, "scope evidence is not fail-closed")
    require(value["fixture"]["sha256"] == ATTRIBUTE.fixture_digest() and value["fixture"]["bytes"] == ATTRIBUTE.FIXTURE_PATH.stat().st_size, "fixture changed after provenance preparation")
    for label, source_path, input_path in (("c23", ATTRIBUTE.C23_TASK_PATH, ATTRIBUTE.INPUT_C23), ("c56", ATTRIBUTE.C56_TASK_PATH, ATTRIBUTE.INPUT_C56)):
        source = ATTRIBUTE.validate_source_binding(value["sources"][label])
        require(source["revision"] == value[f"{label}_revision"], f"{label} source revision binding changed")
        require(source["archive_sha256"] == value["sources"][label]["archive"]["sha256"] and source["tree_sha256"] == value["sources"][label]["tree_sha256"], f"{label} source archive/tree binding changed")
        files = ATTRIBUTE.source_task_manifest(value[f"{label}_revision"], source_path, input_path)
        require(files == value["inputs"][label]["files"], f"{label} predecessor evidence manifest changed")
        require(ATTRIBUTE.canonical_digest(files) == value["inputs"][label]["manifest_sha256"], f"{label} predecessor manifest digest changed")
    causal = value["causal_trace"]
    require(isinstance(causal.get("shared_runtime_files_unchanged"), dict) and causal["shared_runtime_files_unchanged"] and all(causal["shared_runtime_files_unchanged"].values()), "C23/C56 shared runtime boundary files diverged unexpectedly")
    bounds = value.get("bounds", {})
    require(bounds.get("child_timeout_seconds", 0) > 0 and bounds.get("child_timeout_seconds", 0) <= ATTRIBUTE.CHILD_TIMEOUT_MAX, "child deadline is outside the admitted bound")
    require(bounds.get("total_timeout_seconds", 0) > 0 and bounds.get("total_timeout_seconds", 0) <= ATTRIBUTE.TOTAL_TIMEOUT_MAX, "aggregate deadline is outside the admitted bound")
    print(json.dumps({"status": "verified", "mode": "provenance", "branch": ATTRIBUTE.BRANCH, "head": head}, sort_keys=True))


def verify_build_bindings() -> dict[str, dict]:
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    ATTRIBUTE.validate_frozen_inputs(provenance)
    build = load(ATTRIBUTE.BUILD_PATH)
    require(build.get("schema") == "audio-runtime.c68.zero-audio-attribution.build.v1" and build.get("task") == ATTRIBUTE.TASK, "build manifest identity mismatch")
    require(build.get("runner_sha256") == ATTRIBUTE.sha256_file(Path(ATTRIBUTE.__file__)), "build manifest was not produced by the current attribution driver")
    require(build.get("fixture_sha256") == ATTRIBUTE.fixture_digest(), "build fixture digest changed")
    expected_consumer = ATTRIBUTE.sha256_file(ATTRIBUTE.INPUT_C23 / "consumer" / "main.go")
    require(build.get("consumer_source_sha256") == expected_consumer, "build manifest consumer is not the frozen C23 consumer")
    bindings: dict[str, dict] = {}
    for label in ("c23", "c56"):
        record = build.get("builds", {}).get(label) if isinstance(build.get("builds"), dict) else None
        require(isinstance(record, dict), f"{label} build record is missing")
        binding = ATTRIBUTE.validate_build_binding(label, provenance, record, ATTRIBUTE.CHILD_TIMEOUT_MAX, consumer_sha256=expected_consumer)
        require(binding["binary_sha256"] == record["binary"]["sha256"] and binding["build_inputs_sha256"] == record["build_inputs_sha256"], f"{label} executable binding is not reproducible")
        bindings[label] = binding
    require(build.get("binding_guard") == ATTRIBUTE.executable_binding_guard(bindings), "build manifest executable swap guard is missing or stale")
    return bindings


def verify_oracle_controls() -> None:
    verify_scope()
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    bindings = verify_build_bindings()
    negative = load(ATTRIBUTE.NEGATIVE_PATH)
    require(negative.get("schema") == "audio-runtime.c68.zero-audio-attribution.negative.v1" and negative.get("task") == ATTRIBUTE.TASK, "negative control identity mismatch")
    require(negative.get("mutation") == "first-provider-audio" and negative.get("classification") == "oracle_control_rejects_malformed_first_provider_audio", "negative mutation identity mismatch")
    require(negative.get("positive_fixture_sha256") == ATTRIBUTE.fixture_digest(), "negative control changed the positive fixture")
    expected_consumer = ATTRIBUTE.sha256_file(ATTRIBUTE.INPUT_C23 / "consumer" / "main.go")
    require(negative.get("original_consumer_sha256") == expected_consumer and negative.get("original_consumer_sha256") != negative.get("mutated_consumer_sha256"), "negative consumer derivation is not bound to the frozen consumer")
    negative_build = negative.get("build")
    require(isinstance(negative_build, dict), "negative control omitted build-input binding")
    negative_binding = ATTRIBUTE.validate_build_binding("c56", provenance, negative_build, ATTRIBUTE.CHILD_TIMEOUT_MAX, consumer_sha256=negative["mutated_consumer_sha256"])
    stored_binding = negative.get("build_binding")
    require(isinstance(stored_binding, dict) and stored_binding.get("verified") is True and stored_binding.get("binary_sha256") == negative_binding["binary_sha256"], "negative executable binding is not fail-closed")
    execution = negative.get("execution")
    require(isinstance(execution, dict), "negative control omitted execution evidence")
    require(execution.get("binary_binding", {}).get("verified") is True and execution["binary_binding"].get("binary_sha256") == negative_binding["binary_sha256"], "negative execution is not tied to its binary")
    report_path = ATTRIBUTE.owned_path(negative.get("report", ""))
    report = load(report_path)
    artifact_root = report_path.parent
    require(report.get("schema") == "c23.v1" and report.get("scenario") == "tool-matrix" and report.get("turns") == 1 and report.get("recording") is not True, "negative control report shape changed")
    require(report.get("source_revision") == ATTRIBUTE.UNBOUND_SOURCE_REVISION and report.get("fixture_sha256") == ATTRIBUTE.fixture_digest(), "negative control trusted its consumer source identity")
    require(isinstance(report.get("error"), str) and report["error"].startswith("PCM oracle mismatch:"), "negative control did not fail at the public PCM oracle")
    require(report.get("trace_complete") is True and report.get("clean_shutdown") is True, "negative control stopped before the public audio boundary")
    require(execution.get("returncode") == 1 and execution.get("timed_out") is False and execution.get("output_bounded") is True, "negative control accepted an arbitrary nonzero or timeout result")
    cleanup = execution.get("cleanup", {})
    require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("pipes_reaped") is True and not cleanup.get("group_members_after") and cleanup.get("descendant_processes_reaped") is True and not cleanup.get("descendants_after") and not cleanup.get("errors"), "negative child cleanup was incomplete")
    provider = negative.get("provider_capture")
    require(isinstance(provider, dict), "negative control omitted provider capture evidence")
    require(ATTRIBUTE.owned_path(provider.get("path", "")).parent == artifact_root, "negative provider capture is outside its execution root")
    actual_provider = ATTRIBUTE.provider_audio(artifact_root)
    require(actual_provider == provider, "negative provider capture evidence changed")
    pcm = report.get("pcm") if isinstance(report.get("pcm"), dict) else {}
    events = report.get("events") if isinstance(report.get("events"), dict) else {}
    by_kind = events.get("by_kind") if isinstance(events.get("by_kind"), dict) else {}
    require(pcm.get("bytes") == 0 and pcm.get("sha256") == ATTRIBUTE.sha256_bytes(b""), "negative public PCM is not empty at the rejection boundary")
    require(provider.get("audio_delta_records") == 1 and provider.get("nonempty_records") == 0 and provider.get("recorded_bytes") == [0] and provider.get("bytes") == 0 and by_kind.get("AUDIO.DELTA") == 1, "negative control accepted absent or arbitrary provider capture")
    require(ATTRIBUTE.owned_path(negative.get("mutated_source", "")).read_bytes() == ATTRIBUTE.expected_negative_consumer_bytes(), "negative control source is not the exact admitted mutation")
    require(negative.get("mutated_consumer_sha256") == ATTRIBUTE.sha256_file(ATTRIBUTE.owned_path(negative.get("mutated_source", ""))), "negative control source hash is stale")
    require(negative.get("passed") is True, "negative control did not pass its exact rejection assertion")
    disk = ATTRIBUTE.artifact_disk_usage(artifact_root)
    require(negative.get("artifact_disk") == disk and disk.get("bounded") is True, "negative artifact disk evidence is not bounded")
    require(bindings["c56"]["source_revision"] == provenance["c56_revision"], "positive C56 binding disappeared while validating negative control")
    print(json.dumps({"status": "verified", "mode": "oracle-controls", "mutation": negative["mutation"]}, sort_keys=True))


def verify_boundaries() -> None:
    verify_scope()
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    bindings = verify_build_bindings()
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    require(comparison.get("schema") == "audio-runtime.c68.zero-audio-attribution.comparison.v1" and comparison.get("task") == ATTRIBUTE.TASK, "comparison schema or identity mismatch")
    require(comparison.get("positive_execution_count") == 4 and comparison.get("recording_modes") == ["off", "on"] and comparison.get("turns") == 1, "comparison input set is not exactly one off/on turn")
    require(comparison.get("runner_sha256") == ATTRIBUTE.sha256_file(Path(ATTRIBUTE.__file__)), "comparison was not produced by the current attribution driver")
    require(comparison.get("build_bindings") == bindings, "comparison build bindings changed after execution")
    require(comparison.get("binding_guard") == ATTRIBUTE.executable_binding_guard(bindings), "comparison executable swap guard is missing or stale")
    require(isinstance(comparison.get("run_id"), str) and comparison["run_id"], "comparison run identity is missing")
    require(comparison.get("duplicate_guard", {}).get("enabled") is True and comparison["duplicate_guard"].get("current_run_id") == comparison["run_id"], "comparison duplicate guard is missing")
    ledger = load(ATTRIBUTE.COMPARISON_LEDGER_PATH)
    attempts = ledger.get("attempts")
    require(ledger.get("schema") == "audio-runtime.c68.zero-audio-attribution.comparison-runs.v1" and isinstance(attempts, list), "comparison run ledger is missing")
    stored_inventory = ledger.get("positive_artifact_inventory")
    require(isinstance(stored_inventory, list), "comparison ledger does not account for retained positive artifacts")
    stored_by_root = {}
    for item in stored_inventory:
        require(isinstance(item, dict) and isinstance(item.get("root"), str) and item["root"] not in stored_by_root, "comparison ledger has duplicate positive artifact roots")
        stored_by_root[item["root"]] = item
    actual_inventory = ATTRIBUTE.positive_artifact_inventory()
    require({item["root"] for item in actual_inventory} == set(stored_by_root), "comparison ledger has unaccounted positive artifacts")
    for item in actual_inventory:
        recorded = stored_by_root[item["root"]]
        require({key: recorded.get(key) for key in item} == item, f"positive artifact inventory changed: {item['root']}")
    require(ledger.get("duplicate_guard", {}).get("all_retained_positive_roots_accounted") is True and ledger["duplicate_guard"].get("inventory_sha256") == ATTRIBUTE.canonical_digest(stored_inventory), "comparison duplicate guard is not fail-closed")
    current_attempts = [item for item in attempts if item.get("run_id") == comparison["run_id"]]
    require(len(current_attempts) == 1 and current_attempts[0].get("state") == "complete" and current_attempts[0].get("runner_sha256") == comparison["runner_sha256"], "comparison run was not completed under the duplicate guard")
    current_roots = comparison.get("duplicate_guard", {}).get("artifact_roots")
    require(comparison.get("duplicate_guard", {}).get("positive_artifact_roots_accounted") is True and isinstance(current_roots, list) and len(current_roots) == 4, "comparison duplicate guard did not bind its positive roots")
    require(current_attempts[0].get("artifact_roots") == current_roots, "comparison attempt roots are not bound to the candidate")
    current_inventory = [item for item in actual_inventory if item["root"] in set(current_roots)]
    require(comparison["duplicate_guard"].get("positive_artifact_inventory_sha256") == ATTRIBUTE.canonical_digest(current_inventory), "comparison positive artifact inventory binding changed")
    require(current_attempts[0].get("positive_artifact_inventory_sha256") == ATTRIBUTE.canonical_digest(current_inventory), "comparison attempt artifact inventory binding changed")
    require(len({item.get("run_id") for item in attempts}) == len(attempts), "comparison ledger contains duplicate run identities")
    require(ledger.get("duplicate_guard", {}).get("enabled") is True and ledger["duplicate_guard"].get("unchanged_driver_rejected") is True, "comparison ledger does not fail closed on duplicate drivers")
    require(comparison["duplicate_guard"].get("prior_attempt_preserved") is True and ledger["duplicate_guard"].get("history_preserved") is True, "prior comparison attempt was discarded")
    expected = ATTRIBUTE.expected_pcm()
    expected_sha = ATTRIBUTE.sha256_bytes(expected)
    by_source: dict[str, dict[str, dict]] = {"c23": {}, "c56": {}}
    executions = comparison.get("executions")
    require(isinstance(executions, list) and len(executions) == 4, "comparison execution ledger is not exactly four cases")
    for item in executions:
        require(isinstance(item, dict) and item.get("source") in by_source and item.get("mode") in ATTRIBUTE.RECORDING_MODES, "unexpected comparison case")
        require(item["mode"] not in by_source[item["source"]], "duplicate comparison case")
        label = item["source"]
        execution = item.get("execution", {})
        binding = execution.get("binary_binding")
        require(binding == bindings[label] and binding.get("verified") is True, f"{label}/{item['mode']} executable binding changed")
        require(execution.get("source_revision") == provenance[f"{label}_revision"] and execution.get("reported_source_revision") == ATTRIBUTE.UNBOUND_SOURCE_REVISION, f"{label}/{item['mode']} source identity drifted")
        attestation = execution.get("source_attestation", {})
        require(attestation.get("revision") == bindings[label]["source_revision"] and attestation.get("source_archive_sha256") == bindings[label]["source_archive_sha256"] and attestation.get("source_tree_sha256") == bindings[label]["source_tree_sha256"] and attestation.get("build_inputs_sha256") == bindings[label]["build_inputs_sha256"] and attestation.get("binary_sha256") == bindings[label]["binary_sha256"] and attestation.get("report_source_revision_untrusted") is True, f"{label}/{item['mode']} source attestation is missing")
        require(execution.get("returncode") == 0 and execution.get("timed_out") is False and execution.get("output_bounded") is True, f"positive child failed: {label}/{item['mode']}")
        cleanup = execution.get("cleanup", {})
        require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("pipes_reaped") is True and not cleanup.get("group_members_after") and cleanup.get("descendant_processes_reaped") is True and not cleanup.get("descendants_after") and not cleanup.get("errors"), f"positive child cleanup failed: {label}/{item['mode']}")
        run_root = ATTRIBUTE.owned_path(item.get("artifact_root", ""))
        report_path = ATTRIBUTE.owned_path(item.get("report", ""))
        require(report_path.is_file() and report_path.parent == run_root, f"{label}/{item['mode']} report is outside its execution root")
        report = load(report_path)
        require(report.get("source_revision") == ATTRIBUTE.UNBOUND_SOURCE_REVISION and report.get("fixture_sha256") == ATTRIBUTE.fixture_digest(), f"{label}/{item['mode']} report trusted a source identity")
        require(report.get("schema") == "c23.v1" and report.get("scenario") == "tool-matrix" and report.get("turns") == 1 and report.get("trace_complete") is True and report.get("clean_shutdown") is True, f"{label}/{item['mode']} public report is incomplete")
        require(report.get("events", {}).get("overflow_drops") == 0 and report.get("events", {}).get("by_kind", {}).get("AUDIO.DELTA") == 1, f"{label}/{item['mode']} public audio event boundary is incomplete")
        provider = item["boundaries"]["provider_capture"]
        actual_provider = ATTRIBUTE.provider_audio(run_root)
        require({key: provider.get(key) for key in actual_provider} == actual_provider, f"{label}/{item['mode']} provider evidence changed")
        require(provider.get("expected_bytes") == len(expected) and provider.get("ok") is True, f"{label}/{item['mode']} provider expectation evidence changed")
        require(provider.get("nonempty") is True and provider.get("audio_delta_records") == 1 and provider.get("nonempty_records") == 1 and provider.get("recorded_bytes") == [len(expected)] and provider.get("bytes") == len(expected) and provider.get("sha256") == expected_sha, f"{label}/{item['mode']} provider audio boundary is not exactly one non-empty frame")
        pcm = report.get("pcm") if isinstance(report.get("pcm"), dict) else {}
        public = item["boundaries"]["public_pcm"]
        require(public.get("bytes") == len(expected) and public.get("sha256") == expected_sha and public.get("ok") is True and pcm.get("bytes") == len(expected) and pcm.get("sha256") == expected_sha, f"{label}/{item['mode']} public PCM boundary mismatch")
        require(item.get("public_event_audio_delta_count") == 1, f"{label}/{item['mode']} public event trace did not contain exactly one audio delta")
        usage = item["boundaries"]["recording_usage"]
        require(usage.get("drops_zero") is True and usage.get("ok") is True, f"{label}/{item['mode']} recording drop counters are non-zero")
        if item["mode"] == "on":
            require(usage.get("enabled") is True and usage.get("available") is True and usage.get("accepted_audio") == 0 and usage.get("audio_bytes") == 0, f"{label} recording-on audio admission changed")
            require(usage.get("queue_items_final") == 0 and usage.get("queue_bytes_final") == 0 and usage.get("processed_items", 0) >= usage.get("accepted_items", 0), f"{label} recording spool did not drain")
            semantic = item["boundaries"].get("semantic_artifacts", {})
            manifest = ATTRIBUTE.owned_path(semantic.get("manifest", ""))
            require(semantic.get("enabled") is True and manifest.is_file() and semantic.get("audio_path", {}).get("exists") is False, f"{label} recording-on semantic artifact boundary changed")
            require(set(("client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "provider.json")).issubset(set(semantic.get("declared_artifacts", []))), f"{label} recording manifest declaration is incomplete")
        else:
            require(usage.get("enabled") is False and item["boundaries"].get("semantic_artifacts", {}).get("manifest") is None, f"{label} recording-off unexpectedly enabled semantic recording")
        require(item["boundaries"]["finalization"].get("ok") is True, f"{label}/{item['mode']} finalization evidence is incomplete")
        disk = ATTRIBUTE.artifact_disk_usage(run_root)
        require(item.get("artifact_disk") == disk and execution.get("artifact_disk") == disk and disk.get("bounded") is True, f"{label}/{item['mode']} artifact disk evidence is not bounded")
        by_source[label][item["mode"]] = item
    require(all(set(cases) == set(ATTRIBUTE.RECORDING_MODES) for cases in by_source.values()), "comparison is missing a source/mode case")
    for label, cases in by_source.items():
        require(cases["off"]["boundaries"]["public_pcm"]["bytes"] == cases["on"]["boundaries"]["public_pcm"]["bytes"] and cases["off"]["boundaries"]["public_pcm"]["sha256"] == cases["on"]["boundaries"]["public_pcm"]["sha256"], f"{label} public PCM differs between recording modes")
        require(cases["off"]["boundaries"]["provider_capture"]["bytes"] == cases["on"]["boundaries"]["provider_capture"]["bytes"] and cases["off"]["boundaries"]["provider_capture"]["sha256"] == cases["on"]["boundaries"]["provider_capture"]["sha256"], f"{label} provider PCM differs between recording modes")
    aggregate = comparison.get("aggregate_elapsed_ms", 0)
    require(isinstance(aggregate, int) and aggregate > 0 and aggregate <= ATTRIBUTE.TOTAL_TIMEOUT_MAX * 1000 and comparison.get("aggregate_timeout_seconds", 0) <= ATTRIBUTE.TOTAL_TIMEOUT_MAX, "positive comparison exceeded its aggregate bound")
    require(comparison.get("artifact_disk", {}).get("bounded") is True and comparison["artifact_disk"].get("bytes", 0) <= ATTRIBUTE.MAX_RETAINED_ARTIFACT_BYTES, "positive comparison disk bound is missing")
    require(comparison.get("accepted_as_evidence") is True, "comparison did not complete as evidence")
    print(json.dumps({"status": "verified", "mode": "boundaries", "positive_executions": 4}, sort_keys=True))


def verify_attribution() -> None:
    verify_scope()
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    attribution = load(ATTRIBUTE.ATTRIBUTION_PATH)
    require(attribution.get("schema") == "audio-runtime.c68.zero-audio-attribution.attribution.v1" and attribution.get("task") == ATTRIBUTE.TASK and attribution.get("project") == ATTRIBUTE.PROJECT and attribution.get("contract") == "audio-runtime-v1", "canonical attribution identity mismatch")
    source = comparison.get("attribution", {})
    for key in ("classification", "exactly_one_classification", "first_divergent_boundary", "first_divergent_api", "first_divergent_path", "causal_detail", "smallest_existing_c56_executor_repair", "c23_runner_repair_needed", "c56_candidate_restores_audio"):
        require(attribution.get(key) == source.get(key), f"canonical attribution field changed: {key}")
    require(attribution.get("classification") in {"C56_RESTORES_RECORDED_AUDIO", "C56_RECORDING_DEFECT", "C23_FIXTURE_OR_ORACLE_DEFECT"} and attribution.get("exactly_one_classification") is True, "attribution classification is not exclusive")
    require(attribution.get("classification") == "C56_RECORDING_DEFECT" and attribution.get("first_divergent_boundary") == "recording_observer_admission", "attribution is not the demonstrated C56 recording defect")
    require("observer.go:71-80" in attribution.get("first_divergent_path", "") and "terminalDrainSession" in attribution.get("causal_detail", "") and "SetMediaAttached(true)" in attribution.get("causal_detail", ""), "causal attribution omitted the capability-to-observer transition")
    require(attribution.get("c23_revision") == comparison.get("c23_revision") and attribution.get("c56_revision") == comparison.get("c56_revision"), "canonical attribution source revisions changed")
    source_binding = attribution.get("source_binding", {})
    require(source_binding.get("independent_of_consumer_report") is True and source_binding.get("report_source_revision_is_untrusted") is True, "canonical attribution trusts the consumer source label")
    require(attribution.get("comparison", {}).get("sha256") == ATTRIBUTE.canonical_digest(comparison) and attribution["comparison"].get("path") == ATTRIBUTE.owned_rel(ATTRIBUTE.COMPARISON_PATH), "canonical attribution is not bound to comparison evidence")
    require(attribution.get("provenance", {}).get("sha256") == ATTRIBUTE.sha256_file(ATTRIBUTE.PROVENANCE_PATH) and attribution.get("build", {}).get("sha256") == ATTRIBUTE.sha256_file(ATTRIBUTE.BUILD_PATH), "canonical attribution provenance/build binding changed")
    require(attribution.get("build", {}).get("bindings") == comparison.get("build_bindings"), "canonical attribution executable bindings changed")
    owner = attribution.get("owner_action", {})
    require(owner.get("owner") == "audio-runtime-c56-retire-cli-recording-orchestration" and owner.get("scope") == "existing C56 executor; C68 performs no production mutation", "canonical attribution owner action is missing")
    require(attribution.get("evidence_ready") is True and isinstance(attribution.get("limitations"), list) and all("C68 does not claim" in item or "script CI" in item for item in attribution["limitations"]), "canonical attribution limitations are not explicit")
    c21 = attribution.get("c21_regressions", {})
    require(c21.get("path") == ATTRIBUTE.owned_rel(ATTRIBUTE.REGRESSIONS_PATH) and c21.get("sha256") == ATTRIBUTE.sha256_file(ATTRIBUTE.REGRESSIONS_PATH) and c21.get("passed") is True, "canonical attribution is missing final C21 evidence binding")
    print(json.dumps({"status": "verified", "mode": "attribution", "classification": attribution["classification"]}, sort_keys=True))


def verify_cleanup() -> None:
    verify_scope()
    provenance = load(ATTRIBUTE.PROVENANCE_PATH)
    comparison = load(ATTRIBUTE.COMPARISON_PATH)
    for item in comparison.get("executions", []):
        cleanup = item.get("execution", {}).get("cleanup", {})
        require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("pipes_reaped") is True and not cleanup.get("group_members_after") and cleanup.get("descendant_processes_reaped") is True and not cleanup.get("descendants_after") and not cleanup.get("errors"), f"live child group remains: {item.get('source')}/{item.get('mode')}")
    negative = load(ATTRIBUTE.NEGATIVE_PATH)
    cleanup = negative.get("execution", {}).get("cleanup", {})
    require(cleanup.get("parent_reaped") is True and cleanup.get("group_alive_after") is False and cleanup.get("pipes_reaped") is True and not cleanup.get("group_members_after") and cleanup.get("descendant_processes_reaped") is True and not cleanup.get("descendants_after") and not cleanup.get("errors"), "negative child group remains")
    cleanup_control = load(ATTRIBUTE.CLEANUP_PATH)
    require(cleanup_control.get("schema") == "audio-runtime.c68.zero-audio-attribution.cleanup.v1" and cleanup_control.get("passed") is True and cleanup_control.get("runner_sha256") == ATTRIBUTE.sha256_file(Path(ATTRIBUTE.__file__)), "grandchild cleanup control is missing or stale")
    control_execution = cleanup_control.get("execution", {})
    control_cleanup = control_execution.get("cleanup", {})
    require(control_execution.get("timed_out") is True and control_cleanup.get("group_alive_before") is True and control_cleanup.get("term_sent") is True and control_cleanup.get("kill_sent") is True and control_cleanup.get("parent_reaped") is True and control_cleanup.get("pipes_reaped") is True and control_cleanup.get("group_alive_after") is False and not control_cleanup.get("group_members_after") and not control_cleanup.get("errors"), "grandchild cleanup control did not prove process-group reaping")
    control_root = ATTRIBUTE.owned_path(control_execution.get("stdout", "")).parent.parent
    require(cleanup_control.get("artifact_disk") == ATTRIBUTE.artifact_disk_usage(control_root), "grandchild cleanup disk evidence changed")
    regressions = load(ATTRIBUTE.REGRESSIONS_PATH)
    tested = regressions.get("source_revision")
    require(regressions.get("schema") == "audio-runtime.c68.zero-audio-attribution.c21.v1" and regressions.get("passed") is True and isinstance(tested, str) and len(tested) == 40 and tested != ATTRIBUTE.CURRENT_MAIN and regressions.get("candidate_head") == tested and tested == provenance.get("head"), "C21 regression evidence is not tied to the prepared final candidate")
    head = git("rev-parse", "HEAD")
    require(ATTRIBUTE.git_is_ancestor(tested, head), "final head does not preserve the tested C21 candidate")
    changed_after_test = [line for line in git("diff", "--name-only", f"{tested}..{head}").splitlines() if line]
    require(all(path == str(ATTRIBUTE.OWNED_REPO_PATH) or path.startswith(str(ATTRIBUTE.OWNED_REPO_PATH) + "/") for path in changed_after_test), "C21 evidence has executable changes after its tested candidate")
    require(regressions.get("runner_sha256") == ATTRIBUTE.sha256_file(Path(ATTRIBUTE.__file__)), "C21 evidence was not produced by the current attribution driver")
    for label, artifact_root in (("normal", ATTRIBUTE.OWNED_ROOT / "artifacts" / "c21" / "normal"), ("race", ATTRIBUTE.OWNED_ROOT / "artifacts" / "c21" / "race")):
        result = regressions.get(label, {})
        check = result.get("cleanup", {})
        require(result.get("returncode") == 0 and result.get("timed_out") is False and result.get("output_bounded") is True and check.get("parent_reaped") is True and check.get("group_alive_after") is False and check.get("pipes_reaped") is True and not check.get("group_members_after") and check.get("descendant_processes_reaped") is True and not check.get("descendants_after") and not check.get("errors"), f"C21 {label} regression did not pass")
        require(result.get("artifact_disk") == ATTRIBUTE.artifact_disk_usage(artifact_root), f"C21 {label} disk evidence changed")
    retained = ATTRIBUTE.retained_artifact_usage()
    require(retained.get("bounded") is True, "retained artifact disk/file bound is exceeded")
    require(not ATTRIBUTE.COMPARISON_LOCK_PATH.exists(), "comparison lock survived finalization")
    require(comparison.get("aggregate_elapsed_ms", 0) <= ATTRIBUTE.TOTAL_TIMEOUT_MAX * 1000, "positive comparison exceeded aggregate bound")
    first_failure = load(ATTRIBUTE.FIRST_FAILURE_PATH)
    require(first_failure.get("preserved") is True and first_failure.get("classification") == "C56_RECORDING_DEFECT" and ATTRIBUTE.owned_path(first_failure.get("report", "")).is_file(), "first C68 recording failure was not retained")
    print(json.dumps({"status": "verified", "mode": "cleanup", "positive_executions": 4, "retained_artifact_bytes": retained["bytes"]}, sort_keys=True))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("provenance", "oracle-controls", "boundaries", "attribution", "cleanup", "all"), required=True)
    args = parser.parse_args()
    modes = ("provenance", "oracle-controls", "boundaries", "attribution", "cleanup") if args.mode == "all" else (args.mode,)
    functions = {"provenance": verify_provenance, "oracle-controls": verify_oracle_controls, "boundaries": verify_boundaries, "attribution": verify_attribution, "cleanup": verify_cleanup}
    try:
        for mode in modes:
            functions[mode]()
    except Exception as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}, sort_keys=True), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
