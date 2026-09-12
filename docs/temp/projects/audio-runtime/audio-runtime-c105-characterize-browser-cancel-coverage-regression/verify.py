#!/usr/bin/env python3
"""Verify the internally consistent, evidence-only C105 package."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
from collections import Counter
from typing import Any


TASK_NAME = "audio-runtime-c105-characterize-browser-cancel-coverage-regression"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
BRANCH = "codex/audio-runtime-c105-characterize-browser-cancel-coverage-regression"
MAIN_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C93_REVISION = "35b4752e578dff49744159012982fccfacfb9794"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
TEST_NAME = "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab"
TEST_REGEX = f"^{TEST_NAME}$"
TEST_PACKAGE = "./agent-cli/internal/services/internal/agentruntime"
COVERPKG = "github.com/portpowered/go-agent-harness/agent-cli/...,github.com/portpowered/go-agent-harness/go-agent-runtime/..."
CI_LOG_SHA256 = "244bd0ebbd3219ef85f11e83883290a803ff6aa6aeb193dc07955fdff4441af0"
EVIDENCE = Path(__file__).resolve().parent
REPO = EVIDENCE.parents[4]
PRD = REPO / "prd.json"
SOURCE_PATHS = [
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go",
]
ALLOWED_CLASSIFICATIONS = {"PASS", "PRODUCT_FAILURE"}
CAUSAL_CLASSES = {"C93_REGRESSION", "ACCEPTED_MAIN_BROWSER_FLAKE", "ENVIRONMENT_ONLY_TRIGGER", "INCONCLUSIVE"}


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load(relative: str) -> Any:
    path = EVIDENCE / relative
    if not path.is_file():
        raise AssertionError(f"missing evidence file: {relative}")
    return json.loads(path.read_text(encoding="utf-8"))


def git(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", *args], cwd=REPO, capture_output=True, text=True, check=False)


def git_text(args: list[str]) -> str:
    result = git(args)
    if result.returncode != 0:
        raise AssertionError(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def add(errors: list[str], condition: bool, message: str) -> None:
    if not condition:
        errors.append(message)


def manifest_index(manifest: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {item["path"]: item for item in manifest.get("files", [])}


def check_scope(errors: list[str]) -> None:
    status = git_text(["status", "--porcelain", "--untracked-files=all"])
    evidence_prefix = str(EVIDENCE.relative_to(REPO)) + "/"
    outside: list[str] = []
    for line in status.splitlines():
        path = line[3:] if len(line) >= 3 else line
        if " -> " in path:
            path = path.split(" -> ", 1)[1]
        if path and not path.startswith(evidence_prefix):
            outside.append(path)
    add(errors, not outside, f"worktree changes outside owned evidence path: {outside}")
    diff_check = git(["diff", "--check", "--", str(EVIDENCE.relative_to(REPO))])
    add(errors, diff_check.returncode == 0, f"git diff --check failed: {diff_check.stderr.strip()}")
    forbidden: list[str] = []
    for path in EVIDENCE.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(EVIDENCE).as_posix()
        if "__pycache__" in relative or relative.endswith(".pyc") or relative.endswith(".tar") or relative.endswith("coverage.out"):
            forbidden.append(relative)
    add(errors, not forbidden, f"bulky or scratch artifact found in evidence: {forbidden}")


def check_provenance(errors: list[str]) -> None:
    provenance = load("provenance.json")
    prd = json.loads(PRD.read_text(encoding="utf-8"))
    add(errors, provenance.get("task") == TASK_NAME, "provenance task mismatch")
    add(errors, provenance.get("project") == PROJECT, "provenance project mismatch")
    add(errors, provenance.get("contract") == CONTRACT, "provenance contract mismatch")
    add(errors, provenance.get("admission", {}).get("session") == "~default", "provenance session is not ~default")
    verify = provenance.get("admission", {}).get("project_control_verify_work", {})
    verify_output = verify.get("stdout", {})
    add(errors, verify.get("exit_status") == 0 and verify_output.get("status") == "admitted", "project-control task admission was not recorded as admitted")
    add(errors, verify_output.get("project") == PROJECT and verify_output.get("name") == TASK_NAME, "project-control admission identity mismatch")
    add(errors, provenance.get("prd", {}).get("branch_name") == BRANCH == prd.get("branchName"), "prd.branchName/provenance branch mismatch")
    add(errors, git_text(["branch", "--show-current"]) == BRANCH, "current isolated worktree branch mismatch")
    add(errors, git_text(["rev-parse", "origin/main"]) == MAIN_REVISION, "origin/main no longer matches accepted main pin")
    worktree = provenance.get("worktree", {})
    add(errors, not worktree.get("status_at_runner_start", {}).get("outside_owned_paths"), "runner start had non-owned changes")
    revisions = provenance.get("revisions", {})
    add(errors, revisions.get("accepted_main") == MAIN_REVISION, "accepted main revision mismatch")
    add(errors, revisions.get("c93_head") == C93_REVISION, "C93 revision mismatch")
    add(errors, revisions.get("startup_integration") == INTEGRATION_REVISION, "startup integration revision mismatch")
    objects = revisions.get("objects_available", {})
    add(errors, all(objects.get(key) is True for key in ("accepted_main", "c93_head", "startup_integration")), "one or more pinned git objects were unavailable")
    ancestry = revisions.get("ancestry", {})
    add(errors, all(ancestry.get(key, {}).get("passed") is True for key in ("startup_to_main", "startup_to_c93", "main_to_c93")), "required ancestry is not preserved")
    archives = revisions.get("archive_metadata", {})
    add(errors, archives.get("main", {}).get("revision") == MAIN_REVISION and archives.get("c93", {}).get("revision") == C93_REVISION, "archive metadata does not identify exact revisions")
    add(errors, bool(archives.get("main", {}).get("archive_sha256")) and bool(archives.get("c93", {}).get("archive_sha256")), "archive hashes are missing")
    initial = revisions.get("archive_manifests_initial", {})
    final = revisions.get("archive_manifests_final", {})
    add(errors, bool(initial.get("main", {}).get("tree_sha256")) and bool(initial.get("c93", {}).get("tree_sha256")), "initial archive manifests are missing")
    add(errors, initial == final and revisions.get("archive_stable_through_trials") is True, "archive tree changed or final stability proof is missing")
    cache_stability = provenance.get("cache_seed_stability", {})
    add(errors, set(cache_stability) == {"main-normal", "main-nomicrophone-coverpkg", "c93-normal", "c93-nomicrophone-coverpkg"}, "cache seed stability is missing for one or more revision/mode cells")
    add(errors, all(item.get("cell_isolated") is True and bool(item.get("initial_tree_sha256")) and bool(item.get("final_tree_sha256")) and item.get("mutation_recorded") is (item.get("stable") is False) for item in cache_stability.values()), "cache mutation/isolation provenance is incomplete")
    for label in ("main", "c93"):
        manifest_file = revisions.get("archive_manifest_files", {}).get(label)
        if not manifest_file:
            errors.append(f"archive manifest file missing for {label}")
            continue
        try:
            recorded = load(manifest_file)
        except AssertionError as error:
            errors.append(str(error))
            continue
        add(errors, recorded == initial.get(label), f"archive manifest file differs from provenance for {label}")
        index = manifest_index(recorded)
        for source_path in SOURCE_PATHS:
            source_hash = revisions.get("source_hashes", {}).get(label, {}).get(source_path, {})
            add(errors, source_path in index, f"{label} archive manifest lacks {source_path}")
            add(errors, source_hash.get("sha256") == index.get(source_path, {}).get("sha256"), f"{label} source hash mismatch for {source_path}")
    add(errors, provenance.get("negative_control", {}).get("source_log_sha256") == CI_LOG_SHA256, "preserved CI log hash mismatch")
    add(errors, provenance.get("writes_confined_to_owned_path") is True, "provenance does not assert owned-path confinement")
    add(errors, provenance.get("no_production_or_test_changes") is True, "provenance does not assert no production/test changes")
    add(errors, provenance.get("no_pr_or_merge_mutation") is True, "provenance does not assert no PR/merge mutation")
    add(errors, provenance.get("immutable_project_criteria_remain_open") == ["AUDIO", "DEVICE", "EMBED", "SERVICE", "TRACE", "REPLAY", "FAILURES", "QUALITY", "PARITY"], "immutable criteria are not all recorded OPEN")
    environment = provenance.get("environment", {})
    add(errors, bool(environment.get("go_version")) and bool(environment.get("go_env")), "Go environment provenance is incomplete")
    add(errors, bool(environment.get("os")) and bool(environment.get("architecture")), "OS/architecture provenance is incomplete")


def check_negative_control(errors: list[str]) -> None:
    reference = load("negative-control/ci-reference.json")
    add(errors, reference.get("head_revision") == C93_REVISION, "negative control head revision mismatch")
    add(errors, reference.get("source_log_sha256") == CI_LOG_SHA256, "negative control source log hash mismatch")
    add(errors, reference.get("exit_status") == 1, "negative control exit status is not failure")
    add(errors, reference.get("failing_test") == TEST_NAME, "negative control named test mismatch")
    excerpt = "\n".join(reference.get("literal_failure_excerpt", []))
    literal_alternatives = {
        "Finalized:true": ("Finalized:true",),
        "Mechanical.Passed:false": ("Mechanical.Passed:false", "Mechanical:{Passed:false"),
        "Cancellation.FinalState": ("Cancellation.FinalState",),
        "State:canceled Terminal:false": ("State:canceled Terminal:false", "State:canceled with Terminal:false"),
        "want finalized mechanical pass": ("want finalized mechanical pass",),
    }
    for phrase, alternatives in literal_alternatives.items():
        add(errors, any(candidate in excerpt for candidate in alternatives), f"negative control lacks literal phrase: {phrase}")
    assertion_map = load("negative-control/assertion-map.json")
    assertions = assertion_map.get("unchanged_assertion_proof", [])
    add(errors, len(assertions) == 4, "negative control assertion map does not contain four mapped assertions")
    for assertion in assertions:
        by_revision = assertion.get("lines_by_revision", {})
        add(errors, set(by_revision) == {"main", "c93"}, f"assertion missing both archived revisions: {assertion.get('id')}")
        add(errors, assertion.get("same_text_at_both_revisions") is True, f"assertion text drifted: {assertion.get('id')}")
        add(errors, bool(assertion.get("assertion")) and bool(assertion.get("rejects")), f"assertion proof incomplete: {assertion.get('id')}")
    proof = load("negative-control/proof-table.json")
    bad = proof.get("bad_state", {})
    add(errors, bad.get("result_finalized") is True and bad.get("mechanical_passed") is False, "negative control bad finalized/mechanical state is not recorded")
    add(errors, bad.get("cancellation_final_state") == "" and bad.get("cancel_broker_state") == "canceled" and bad.get("cancel_broker_terminal") is False, "negative control nonterminal cancellation state is not recorded")
    add(errors, len(proof.get("rejections", [])) == 4 and all(item.get("bad_state_rejected") is True for item in proof.get("rejections", [])), "negative control rejection proof is incomplete")


def check_matrix(errors: list[str]) -> None:
    matrix = load("matrix.json")
    add(errors, matrix.get("task") == TASK_NAME, "matrix task mismatch")
    add(errors, matrix.get("revision_order") == ["main", "c93"], "matrix revision order is not fixed")
    add(errors, matrix.get("revisions") == {"main": MAIN_REVISION, "c93": C93_REVISION}, "matrix revisions mismatch")
    add(errors, matrix.get("trials_per_cell") == 10 and matrix.get("trial_count_total") == 40, "matrix is not the fixed 2x2x10 count")
    add(errors, matrix.get("per_command_timeout_seconds", 0) <= 90, "matrix per-command timeout exceeds 90 seconds")
    add(errors, matrix.get("aggregate_timeout_seconds", 0) <= 600, "matrix aggregate timeout exceeds 600 seconds")
    add(errors, matrix.get("no_adaptive_retries") is True and matrix.get("no_live_realtime_or_credentials") is True, "matrix permits adaptive retries or live credentials")
    warmups = matrix.get("cache_warmup_results", {})
    add(errors, set(warmups) == {"main-normal", "main-nomicrophone-coverpkg", "c93-normal", "c93-nomicrophone-coverpkg"}, "matrix cache warmup results are incomplete")
    add(errors, all(item.get("not_a_trial") is True and item.get("exit_status") == 0 and item.get("timed_out") is False for item in warmups.values()), "cache warmup was not a completed non-trial setup")
    add(errors, len({item.get("cache_seed") for item in warmups.values()}) == 4, "revision/mode cache roots are not independent")
    add(errors, len({item.get("module_cache_runtime") for item in warmups.values()}) == 4, "revision/mode module-cache views are not independent")
    test = matrix.get("test", {})
    add(errors, test.get("name") == TEST_NAME and test.get("anchored_regex") == TEST_REGEX and test.get("package") == TEST_PACKAGE, "matrix named test/package/regex mismatch")
    modes = matrix.get("modes", [])
    add(errors, [mode.get("id") for mode in modes] == ["normal", "nomicrophone-coverpkg"], "matrix modes are not predeclared normal then nomicrophone-coverpkg")
    if len(modes) == 2:
        add(errors, modes[0].get("tags") == "" and modes[0].get("coverpkg") == "", "normal mode changed")
        add(errors, modes[1].get("tags") == "nomicrophone" and modes[1].get("coverpkg") == COVERPKG, "nomicrophone coverpkg mode changed")
    isolation = matrix.get("isolation", {})
    for key in ("source", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "GOPATH", "HOME", "coverage"):
        detail = str(isolation.get(key, "")).lower()
        add(errors, key in isolation and any(token in detail for token in ("unique", "independent", "separate")), f"matrix isolation missing unique/independent {key}")


def expected_command(record: dict[str, Any]) -> bool:
    command = record.get("command", [])
    if not isinstance(command, list) or len(command) < 2 or command[:3] != ["go", "test", "-json"]:
        return False
    if "-count=1" not in command or "-timeout" not in command or "80s" not in command:
        return False
    if "-run" not in command or TEST_REGEX not in command or TEST_PACKAGE not in command:
        return False
    mode = record.get("mode")
    if mode == "normal":
        return "-tags" not in command and "-coverpkg" not in command and "-coverprofile" not in command
    if mode == "nomicrophone-coverpkg":
        try:
            return command[command.index("-tags") + 1] == "nomicrophone" and command[command.index("-coverpkg") + 1] == COVERPKG and "-coverprofile" in command
        except (ValueError, IndexError):
            return False
    return False


def check_trial_ledger(errors: list[str]) -> None:
    matrix = load("matrix.json")
    provenance = load("provenance.json")
    ledger = load("trial-ledger.json")
    trials = ledger.get("trials", [])
    add(errors, len(trials) == 40, f"trial ledger contains {len(trials)} trials, expected 40")
    ids = [trial.get("trial_id") for trial in trials]
    add(errors, len(ids) == len(set(ids)), "trial ledger contains duplicate trial IDs")
    counts = Counter((trial.get("revision_label"), trial.get("mode")) for trial in trials)
    for label in ("main", "c93"):
        for mode in ("normal", "nomicrophone-coverpkg"):
            add(errors, counts[(label, mode)] == 10, f"trial ledger count for {label}/{mode} is {counts[(label, mode)]}, expected 10")
    archive_hashes = {label: provenance.get("revisions", {}).get("archive_metadata", {}).get(label, {}).get("archive_sha256") for label in ("main", "c93")}
    for trial in trials:
        trial_id = trial.get("trial_id", "<missing>")
        required = ("revision_label", "revision", "archive_sha256", "mode", "command", "command_string", "started_at", "ended_at", "duration_seconds", "exit_status", "timed_out", "termination_signal", "classification", "assertion_reached", "terminal_state_conclusion", "go_test_events", "raw_log", "stderr_log")
        for field in required:
            add(errors, field in trial, f"{trial_id}: missing {field}")
        label = trial.get("revision_label")
        add(errors, label in archive_hashes and trial.get("archive_sha256") == archive_hashes.get(label), f"{trial_id}: archive hash mismatch")
        add(errors, trial.get("revision") == {"main": MAIN_REVISION, "c93": C93_REVISION}.get(label), f"{trial_id}: revision mismatch")
        add(errors, expected_command(trial), f"{trial_id}: command is not the predeclared named test")
        add(errors, trial.get("classification") in ALLOWED_CLASSIFICATIONS, f"{trial_id}: invalid classification {trial.get('classification')}")
        add(errors, trial.get("timed_out") is False and trial.get("termination_signal") is None, f"{trial_id}: timeout/signal is not a valid bounded trial")
        add(errors, 0 <= trial.get("duration_seconds", -1) <= matrix.get("per_command_timeout_seconds", 90), f"{trial_id}: duration exceeds the per-command bound")
        events = trial.get("go_test_events", {})
        add(errors, events.get("malformed_json_lines") == 0 and events.get("matched_test_count") == 1, f"{trial_id}: structured event stream did not match exactly one named test")
        classification = trial.get("classification")
        if classification == "PASS":
            add(errors, trial.get("exit_status") == 0 and events.get("target_pass_count") == 1, f"{trial_id}: PASS lacks one passing target event")
            add(errors, trial.get("terminal_state_conclusion") == "TERMINAL_CANCELED_ASSERTED", f"{trial_id}: PASS lacks asserted canceled terminal conclusion")
        if classification == "PRODUCT_FAILURE":
            signature = trial.get("failure_signature", {})
            add(errors, signature.get("reproduces_preserved_negative_control") is True, f"{trial_id}: product failure is not the preserved cancellation mismatch")
            add(errors, trial.get("terminal_state_conclusion") == "NONTERMINAL_OR_MISSING_CANCELED_STATE_OBSERVED", f"{trial_id}: product failure lacks nonterminal cancellation conclusion")
            add(errors, events.get("target_fail_count", 0) >= 1, f"{trial_id}: product failure lacks a target fail event")
            add(errors, trial.get("exit_status") == 1, f"{trial_id}: product failure exit status is not 1")
        for log_field in ("raw_log", "stderr_log"):
            log = trial.get(log_field, {})
            path = EVIDENCE / log.get("path", "__missing__")
            add(errors, path.is_file(), f"{trial_id}: missing {log_field}")
            if path.is_file():
                add(errors, sha256_file(path) == log.get("sha256"), f"{trial_id}: {log_field} hash mismatch")
        isolated = trial.get("isolated_paths", {})
        add(errors, all(isolated.get(key) for key in ("work_root", "GOCACHE", "GOCACHE_trial_snapshot", "GOCACHE_seed_source", "GOCACHE_seed_method", "GOMODCACHE", "GOMODCACHE_trial_snapshot", "GOMODCACHE_seed_source", "GOMODCACHE_seed_method", "GOTMPDIR", "GOPATH", "coverage_output")), f"{trial_id}: isolated path identity incomplete")
        add(errors, trial.get("isolated_paths_removed_after_trial") is True, f"{trial_id}: temporary isolation was not cleaned")
        add(errors, not Path(isolated.get("coverage_output", "__missing__")).exists(), f"{trial_id}: coverage scratch output remains")
    recorded_total = provenance.get("matrix", {}).get("total_trials")
    add(errors, recorded_total == len(trials), "provenance trial count does not match ledger")
    add(errors, provenance.get("matrix", {}).get("aggregate_elapsed_seconds", 601) <= 600, "recorded aggregate duration exceeds 600 seconds")
    snapshots = [trial.get("isolated_paths", {}).get("GOCACHE_trial_snapshot") for trial in trials]
    add(errors, len(snapshots) == len(set(snapshots)) == 40, "GOCACHE trial snapshots are not unique")
    add(errors, all(trial.get("isolated_paths", {}).get("GOCACHE_seed_method") == "stable-cell-cache-reuse" for trial in trials), "GOCACHE trial execution did not use the recorded stable cell cache")
    add(errors, all(trial.get("isolated_paths", {}).get("GOMODCACHE_seed_method") == "stable-cell-symlink" for trial in trials), "GOMODCACHE trial execution did not use the recorded stable cell view")
    module_snapshots = [trial.get("isolated_paths", {}).get("GOMODCACHE_trial_snapshot") for trial in trials]
    add(errors, len(module_snapshots) == len(set(module_snapshots)) == 40, "GOMODCACHE trial snapshots are not unique")


def summarize(trials: list[dict[str, Any]], label: str, mode: str) -> dict[str, Any]:
    cell = [trial for trial in trials if trial.get("revision_label") == label and trial.get("mode") == mode]
    classifications = Counter(trial.get("classification") for trial in cell)
    signatures = Counter(trial.get("failure_signature", {}).get("key") for trial in cell if trial.get("classification") == "PRODUCT_FAILURE")
    conclusions = Counter(trial.get("terminal_state_conclusion") for trial in cell)
    return {
        "revision_label": label,
        "mode": mode,
        "trial_count": len(cell),
        "classification_counts": dict(sorted(classifications.items())),
        "pass_count": classifications.get("PASS", 0),
        "product_failure_count": classifications.get("PRODUCT_FAILURE", 0),
        "test_failure_count": classifications.get("TEST_FAILURE", 0),
        "timeout_count": classifications.get("TIMEOUT", 0),
        "infra_count": classifications.get("INFRA", 0),
        "negative_control_signature_counts": dict(sorted(signatures.items())),
        "terminal_conclusion_counts": dict(sorted(conclusions.items())),
    }


def check_classification(errors: list[str]) -> None:
    characterization = load("characterization.json")
    ledger = load("trial-ledger.json")
    classification = characterization.get("classification")
    add(errors, classification in CAUSAL_CLASSES, f"invalid causal classification: {classification}")
    add(errors, isinstance(characterization.get("evidence_threshold"), str) and bool(characterization.get("evidence_threshold")), "classification evidence threshold missing")
    add(errors, isinstance(characterization.get("falsifiers"), list) and bool(characterization.get("falsifiers")), "classification falsifiers missing")
    add(errors, characterization.get("inconclusive_is_not_pass") is (classification == "INCONCLUSIVE"), "INCONCLUSIVE/pass disposition inconsistent")
    expected = [summarize(ledger.get("trials", []), label, mode) for label in ("main", "c93") for mode in ("normal", "nomicrophone-coverpkg")]
    add(errors, characterization.get("cells") == expected, "characterization cell summaries do not match the raw trial ledger")
    add(errors, characterization.get("negative_control_reproduced") is any(cell["product_failure_count"] > 0 for cell in expected), "negative-control aggregate does not match trial ledger")
    report = (EVIDENCE / "report.md").read_text(encoding="utf-8") if (EVIDENCE / "report.md").is_file() else ""
    add(errors, f"Classification: **{classification}**" in report, "report classification does not match machine-readable classification")
    add(errors, "All nine immutable project criteria" in report and "remain `OPEN`" in report, "report does not preserve all nine OPEN criteria")
    add(errors, "does not claim C93 acceptance" in report and "C105 implements none" in report, "report contains an acceptance or implementation claim")
    diff_inventory = load("source-diff-inventory.json")
    add(errors, diff_inventory.get("base_revision") == MAIN_REVISION and diff_inventory.get("candidate_revision") == C93_REVISION, "source diff inventory revisions mismatch")
    add(errors, diff_inventory.get("browser_related_changed_paths") == [], "C93 diff unexpectedly changes a browser-related path")
    add(errors, bool(diff_inventory.get("full_diff_sha256")) and diff_inventory.get("changed_path_count") == len(diff_inventory.get("changed_paths", [])), "source diff inventory is incomplete")


def check_ownership(errors: list[str]) -> None:
    ownership = load("ownership.json")
    owner = ownership.get("canonical_owner", {})
    add(errors, owner.get("task_id") == "work-task-125", "canonical owner is not work-task-125/C83")
    add(errors, owner.get("task_name") == "audio-runtime-c83-retire-cli-browser-scenario-runner", "canonical owner task name mismatch")
    add(errors, owner.get("state") == "FAILED" and "inactive" in owner.get("lease_status", ""), "canonical owner state/lease is not recorded")
    for path in ("agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go", "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go", "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go", "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go"):
        add(errors, path in owner.get("owner_paths", []), f"canonical owner path missing: {path}")
    edge = ownership.get("exact_event_edge", {})
    add(errors, edge.get("owner") == "C83 preserved browser-scenario runner", "exact event edge owner mismatch")
    add(errors, len(edge.get("paths_and_symbols", [])) >= 6, "exact event edge citations incomplete")
    add(errors, "nonterminal" in edge.get("causal_observation", "").lower() and "finalstate" in edge.get("causal_observation", "").lower(), "exact event edge causal observation missing")
    consumer = ownership.get("consumer_not_owner", {})
    add(errors, consumer.get("task_id") == "work-task-34" and "not the observed runner event edge" in consumer.get("role", ""), "C61 consumer/not-owner distinction missing")


def check_recommendation(errors: list[str]) -> None:
    recommendation = load("ownership.json").get("one_recommendation", {})
    add(errors, recommendation.get("kind") == "bounded instrumentation", "recommendation is not one bounded instrumentation action")
    paths = recommendation.get("proposed_paths", [])
    add(errors, paths == [
        "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
        "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
    ], "recommendation proposed paths are not exact")
    add(errors, recommendation.get("implemented_by_c105") is False, "C105 appears to implement its own recommendation")
    add(errors, len(recommendation.get("preserve_assertions", [])) == 2, "recommendation does not preserve both assertion sites")
    add(errors, len(recommendation.get("focused_regressions", [])) == 2, "recommendation lacks the two focused regression shapes")
    add(errors, bool(recommendation.get("dependencies")), "recommendation dependencies missing")


def check_all(errors: list[str]) -> None:
    check_scope(errors)
    check_provenance(errors)
    check_negative_control(errors)
    check_matrix(errors)
    check_trial_ledger(errors)
    check_classification(errors)
    check_ownership(errors)
    check_recommendation(errors)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["provenance", "negative-control", "matrix", "trial-ledger", "classification", "ownership", "recommendation", "all"], default="all")
    parser.add_argument("--record", action="store_true", help="write verifier-results.json in the owned evidence directory")
    args = parser.parse_args()
    errors: list[str] = []
    checks = {
        "provenance": check_provenance,
        "negative-control": check_negative_control,
        "matrix": check_matrix,
        "trial-ledger": check_trial_ledger,
        "classification": check_classification,
        "ownership": check_ownership,
        "recommendation": check_recommendation,
        "all": check_all,
    }
    try:
        checks[args.mode](errors)
    except (AssertionError, KeyError, json.JSONDecodeError, OSError) as error:
        errors.append(str(error))
    result = {
        "version": 1,
        "task": TASK_NAME,
        "mode": args.mode,
        "checked_at": now(),
        "passed": not errors,
        "error_count": len(errors),
        "errors": errors,
        "verifier_environment": {"python": sys.version, "platform": platform.platform(), "cwd": str(REPO)},
    }
    if args.record:
        (EVIDENCE / "verifier-results.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, sort_keys=True))
    return 0 if not errors else 1


if __name__ == "__main__":
    raise SystemExit(main())
