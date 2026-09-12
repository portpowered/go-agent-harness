#!/usr/bin/env python3
"""Fail-closed verifier for C109 analyzer evidence and negative fixtures."""

from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any


PROJECT = "audio-runtime"
TASK = "audio-runtime-c109-characterize-browser-retirement-integration"
CONTRACT = "audio-runtime-v1"
BRANCH = "codex/audio-runtime-c109-characterize-browser-retirement-integration"
ACCEPTED_MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C61 = "8e8177c031a7b3b9322d712af19970e13fa7a1bc"
C83 = "22cc6769aaf06d1e2c1275b064cc7ec29de3e371"
SHARED_RUNNER = "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/"
C79_PATHS = {"scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"}
MANIFEST_GATES = {"AUDIO", "DEVICE", "EMBED", "SERVICE", "TRACE", "REPLAY", "FAILURES", "QUALITY", "PARITY"}
CANDIDATE_BRANCHES = {
    "c61": "codex/audio-runtime-c61-retire-cli-browser-scenario-contract",
    "c83": "codex/audio-runtime-c83-retire-cli-browser-scenario-runner",
}
PRESERVED_PATHS = {
    "c61": ".claude/worktrees/audio-runtime-c61-retire-cli-browser-scenario-contract",
    "c83": ".claude/worktrees/audio-runtime-c83-retire-cli-browser-scenario-runner",
}


class VerificationError(RuntimeError):
    pass


def load(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationError(f"cannot load {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise VerificationError(f"expected object in {path}")
    return value


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def hash_or_none(value: Any, label: str) -> None:
    require(value is None or (isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value)), f"invalid {label} hash")


def report_bundle(path: Path) -> tuple[dict[str, Any], Path]:
    directory = path if path.is_dir() else path.parent
    report = load(directory / "report.json")
    companions = {
        "provenance": "provenance.json",
        "ledger": "ledger.json",
        "rehearsal": "merge-orders.json",
        "sequence": "sequence.json",
    }
    for field, filename in companions.items():
        companion = load(directory / filename)
        require(report.get(field) == companion, f"report/{filename} content mismatch")
    manifest = load(directory / "run-manifest.json")
    require(manifest.get("schema") == "audio-runtime-c109-run-manifest-v2", "run manifest schema missing")
    for filename, digest in manifest.get("outputSha256", {}).items():
        data = (directory / filename).read_bytes()
        import hashlib

        require(hashlib.sha256(data).hexdigest() == digest, f"run manifest hash mismatch: {filename}")
    return report, directory


def current_root() -> Path:
    return Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()


def git_ok(root: Path, args: list[str]) -> bool:
    return subprocess.run(["git", *args], cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False).returncode == 0


def validate_preserved(preserved: dict[str, Any]) -> None:
    require(preserved.get("unchanged") is True, "preserved predecessor worktree changed")
    before = preserved.get("before", {}).get("worktrees", {})
    after = preserved.get("after", {}).get("worktrees", {})
    require(preserved.get("before", {}).get("complete") is True and preserved.get("after", {}).get("complete") is True, "preserved worktree snapshot is incomplete")
    require(before == after, "preserved predecessor before/after identities differ")
    for key, expected_commit in (("c61", C61), ("c83", C83)):
        item = before.get(key, {})
        require(item.get("path") == PRESERVED_PATHS[key], f"preserved {key} path missing")
        require(item.get("registered") is True and item.get("present") is True, f"preserved {key} worktree is not present")
        require(item.get("head") == expected_commit, f"preserved {key} worktree head changed")
        require(item.get("branch") == CANDIDATE_BRANCHES[key], f"preserved {key} branch changed")
        require(item.get("branch_ref") == f"refs/heads/{CANDIDATE_BRANCHES[key]}", f"preserved {key} branch ref missing")
        require(item.get("status") == [], f"preserved {key} worktree is dirty")
        hash_or_none(item.get("tree"), f"preserved {key} tree")


def validate_refs(provenance: dict[str, Any]) -> None:
    refs = provenance.get("refs", {})
    require(refs.get("candidateRefsUnchanged") is True, "candidate ref identity changed")
    before = {item.get("name"): item.get("object") for item in refs.get("before", [])}
    after = {item.get("name"): item.get("object") for item in refs.get("after", [])}
    candidate_names = set()
    for key, commit in (("c61", C61), ("c83", C83)):
        branch = CANDIDATE_BRANCHES[key]
        candidate_names.update(
            {
                f"refs/heads/{branch}",
                f"refs/remotes/origin/{branch}",
                f"refs/heads/c109-{key}-readonly",
            }
        )
        for name in candidate_names:
            if name.endswith(branch) or name.endswith(f"c109-{key}-readonly"):
                require(before.get(name) == commit and after.get(name) == commit, f"candidate ref is not pinned: {name}")
    require({name: before.get(name) for name in candidate_names} == {name: after.get(name) for name in candidate_names}, "candidate refs changed during run")
    fetch = provenance.get("fetch", {})
    require(fetch.get("status") == "passed" and fetch.get("exit_code") == 0, "origin/main fetch was not successful")
    require(fetch.get("resolved_origin_main") == provenance.get("fetchedOriginMain"), "fetch identity is incomplete")
    require(provenance.get("reviewMain") == provenance.get("fetchedOriginMain"), "review-time main is not the fetched main")
    require(provenance.get("requestedAcceptedMain") == ACCEPTED_MAIN, "stale or changed accepted-main SHA")
    require(isinstance(provenance.get("fetchedOriginMain"), str) and re.fullmatch(r"[0-9a-f]{40}", provenance["fetchedOriginMain"]), "fetched main SHA missing")


def validate_inventory(provenance: dict[str, Any]) -> None:
    inventory = provenance.get("worktreeInventory", {})
    require(inventory.get("complete") is True, "worktree porcelain inventory is incomplete")
    records = {item.get("scope"): item for item in inventory.get("records", [])}
    require({"current", "preserved-c61", "preserved-c83"}.issubset(records), "scoped worktree inventory omitted a required worktree")
    require(records["current"].get("branch") == f"refs/heads/{BRANCH}", "current worktree branch identity missing")
    for key, commit in (("c61", C61), ("c83", C83)):
        item = records[f"preserved-{key}"]
        require(item.get("path") == PRESERVED_PATHS[key], f"inventory path mismatch for {key}")
        require(item.get("head") == commit, f"inventory head mismatch for {key}")
        require(item.get("branch") == f"refs/heads/{CANDIDATE_BRANCHES[key]}", f"inventory branch mismatch for {key}")
        require(item.get("prunable", "") == "", f"inventory reports prunable {key} worktree")


def validate_ledger(ledger: dict[str, Any]) -> None:
    require(ledger.get("schema") == "audio-runtime-c109-ledger-v2", "ledger schema missing")
    require(ledger.get("base", {}).get("source_driven") is True, "ledger is not source-driven")
    require(ledger.get("base", {}).get("candidateIntersection") == [SHARED_RUNNER], "unexpected candidate path intersection")
    union_paths = ledger.get("base", {}).get("candidateUnionPaths", [])
    require(union_paths == sorted(union_paths) and union_paths, "candidate union paths are incomplete")
    require(ledger.get("base", {}).get("candidateUnionCount") == len(union_paths), "candidate union count mismatch")
    require(ledger.get("unownedCandidatePaths") == [], "candidate path was attributed to C109")
    files = ledger.get("files", [])
    require([item.get("path") for item in files] == union_paths, "ledger file order or membership is not exact")
    evidence_ids: set[str] = set()
    for item in files:
        path = item.get("path", "")
        require(path, "ledger entry has no path")
        require(item.get("owner"), f"ledger entry has no owner: {path}")
        if path in C79_PATHS:
            require("C79" in item["owner"], f"C79 shared path attributed to candidate: {path}")
        elif path == SHARED_RUNNER:
            require("C61" in item["owner"] and "C83" in item["owner"], "shared runner ownership is incomplete")
        else:
            require("C109" not in item["owner"], f"candidate path was incorrectly attributed to C109: {path}")
        require(item.get("merge_base"), f"merge base missing: {path}")
        for label in ("base_blob", "c61_blob", "c83_blob"):
            hash_or_none(item.get(label), f"{path} {label}")
        hunks = item.get("hunks", [])
        require(hunks, f"source hunk evidence is missing: {path}")
        flattened: list[dict[str, Any]] = []
        for hunk in hunks:
            branch = hunk.get("branch")
            require(branch in {"c61", "c83"} and hunk.get("path") == path, f"hunk identity is incomplete: {path}")
            hunk_id = hunk.get("evidence_id")
            require(isinstance(hunk_id, str) and hunk_id not in evidence_ids, f"duplicate hunk evidence id: {path}")
            evidence_ids.add(hunk_id)
            for span_name in ("old_span", "new_span"):
                span = hunk.get(span_name, {})
                require(isinstance(span.get("start_line"), int) and isinstance(span.get("line_count"), int), f"{span_name} is not exact: {path}")
                require(span.get("line_count") >= 0, f"negative {span_name} count: {path}")
            require(isinstance(hunk.get("patch_sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", hunk["patch_sha256"]), f"hunk hash missing: {path}")
            require(isinstance(hunk.get("added_line_count"), int) and isinstance(hunk.get("deleted_line_count"), int), f"hunk counts missing: {path}")
            require(hunk.get("change_kind") in {"added", "deleted", "modified"}, f"hunk change kind missing: {path}")
            symbols = hunk.get("symbols")
            require(isinstance(symbols, list) and symbols, f"symbol evidence is missing: {hunk_id}")
            flattened.extend(symbols)
            for symbol in symbols:
                symbol_id = symbol.get("evidence_id")
                require(isinstance(symbol_id, str) and symbol_id not in evidence_ids, f"duplicate symbol evidence id: {symbol_id}")
                evidence_ids.add(symbol_id)
                require(symbol.get("hunk_evidence_id") == hunk_id, f"symbol/hunk edge mismatch: {symbol_id}")
                require(symbol.get("branch") == branch and symbol.get("path") == path, f"symbol identity missing: {symbol_id}")
                require(symbol.get("symbol"), f"symbol name missing: {symbol_id}")
                symbol_span = symbol.get("span", {})
                require(isinstance(symbol_span.get("start_line"), int) and isinstance(symbol_span.get("end_line"), int), f"symbol span missing: {symbol_id}")
                declaration = symbol.get("declaration", {})
                require(declaration.get("ref") and declaration.get("path") == path and isinstance(declaration.get("line"), int) and declaration.get("text") is not None, f"declaration evidence missing: {symbol_id}")
                require(isinstance(symbol.get("caller_edges"), list), f"caller edges missing: {symbol_id}")
                for edge in symbol["caller_edges"]:
                    require(edge.get("path") and isinstance(edge.get("line"), int) and edge.get("text") is not None, f"caller edge is incomplete: {symbol_id}")
                blobs = symbol.get("blob_hashes", {})
                require(set(blobs) == {"base", "candidate"}, f"symbol blob hashes missing: {symbol_id}")
                hash_or_none(blobs.get("base"), f"symbol {symbol_id} base")
                hash_or_none(blobs.get("candidate"), f"symbol {symbol_id} candidate")
                require(isinstance(symbol.get("presence"), dict), f"symbol presence missing: {symbol_id}")
                require(symbol.get("conflict_kind"), f"symbol conflict kind missing: {symbol_id}")
                require(symbol.get("resolution_owner") in {"C61/work-task-34", "C83/work-task-125", "C79/work-task-114"}, f"symbol owner is unsupported: {symbol_id}")
                judgment = symbol.get("order_judgment", {})
                require(judgment.get("required_before") is False and judgment.get("mechanically_order_independent") is True, f"symbol order judgment missing: {symbol_id}")
                require(isinstance(symbol.get("evidence_ids"), list) and hunk_id in symbol["evidence_ids"], f"symbol evidence link missing: {symbol_id}")
        require(item.get("symbols") == flattened, f"file symbol projection mismatch: {path}")
        conflict = item.get("conflict", {})
        require(conflict.get("kind") in {"none", "shared-file-no-content-conflict"}, f"file conflict kind missing: {path}")
        require(isinstance(conflict.get("evidence_ids"), list), f"file conflict evidence missing: {path}")
        judgment = item.get("order_judgment", {})
        require(judgment.get("required_before") is False and judgment.get("mechanically_order_independent") is True, f"file order judgment missing: {path}")
    api = ledger.get("apiConclusion", {})
    require(api.get("evidence_id") == "api-c83-to-c61-search", "API search evidence missing")
    require(isinstance(api.get("searched_paths"), list) and isinstance(api.get("edges"), list), "API edge ledger is incomplete")
    require(api.get("c83_requires_c61_api") is False, "C83 API dependency claim is unsupported")
    require(api.get("mechanically_order_independent") is True, "API order conclusion is unsupported")


def validate_rehearsal(report: dict[str, Any]) -> None:
    rehearsal = report.get("rehearsal", {})
    role = report.get("role")
    expected_order = ["c61", "c83"] if role == "required" else ["c83", "c61"]
    require(rehearsal.get("schema") == "audio-runtime-c109-merge-rehearsal-v2", "merge rehearsal schema missing")
    require(rehearsal.get("order") == expected_order, "rehearsal order is reversed or missing")
    require(rehearsal.get("base") == report.get("provenance", {}).get("reviewMain"), "rehearsal base is not review-time main")
    merges = rehearsal.get("merges")
    require(isinstance(merges, list) and len(merges) == 2, "both merge steps are required")
    previous = rehearsal["base"]
    for index, merge in enumerate(merges):
        label = expected_order[index]
        expected_commit = C61 if label == "c61" else C83
        require(merge.get("step") == index + 1 and merge.get("label") == label, "merge label/order mismatch")
        require(merge.get("expected_commit") == expected_commit, "merge uses an unsupported candidate SHA")
        require(merge.get("pre_merge_head") == previous, f"merge {label} does not preserve ancestry")
        require(merge.get("status") == "passed" and merge.get("exit_code") == 0, "merge rehearsal failed")
        require("conflicted_paths" in merge and isinstance(merge["conflicted_paths"], list), "complete conflict path evidence is missing")
        require(merge.get("conflicted_paths") == [], "merge conflict was hidden or unresolved")
        require(merge.get("conflicts") == [] and merge.get("conflict_kind") == "none", "merge conflict evidence is inconsistent")
        require(isinstance(merge.get("output_sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", merge["output_sha256"]), "merge output identity missing")
        require(merge.get("conflict_resolution") == "not-needed", "unsupported conflict resolution claim")
        parents = merge.get("parents", [])
        require(len(parents) == 3 and parents[1] == previous and parents[2] == expected_commit, f"merge parents are incomplete: {label}")
        require(isinstance(merge.get("tree"), str) and re.fullmatch(r"[0-9a-f]{40}", merge["tree"]), "merge tree was not recorded")
        require(merge.get("post_status") == [], "synthetic merge tree is dirty")
        require(isinstance(merge.get("evidence_ids"), list) and merge["evidence_ids"], "merge evidence id is missing")
        previous = merge["commit"]
    require(rehearsal.get("final_head") == previous and rehearsal.get("final_tree"), "final synthetic tree identity is missing")
    require(rehearsal.get("final_status") == [], "final synthetic tree is dirty")
    cleanup = rehearsal.get("cleanup", {})
    require(cleanup.get("worktree_remove_status") == "passed", "temporary worktree removal failed")
    require(cleanup.get("worktree_removed") is True and cleanup.get("temporary_root_removed") is True, "temporary rehearsal directory was not removed")


def validate_sequence(sequence: dict[str, Any], review_main: str) -> None:
    require(sequence.get("schema") == "audio-runtime-c109-bounded-sequence-v2", "bounded sequence schema missing")
    require(sequence.get("status") == "bounded-and-procedural", "bounded sequence status missing")
    require(sequence.get("inputs", {}).get("accepted_main") == review_main, "sequence main input is stale")
    require(sequence.get("inputs", {}).get("c61", {}).get("commit") == C61 and sequence.get("inputs", {}).get("c83", {}).get("commit") == C83, "sequence candidate inputs are stale")
    steps = sequence.get("steps", [])
    require(len(steps) == 4, "bounded sequence must include shared, C61, C83 and handoff steps")
    require([step.get("owner") for step in steps[:3]] == ["C79/work-task-114", "C61/work-task-34", "C83/work-task-125"], "bounded owner sequence is wrong")
    require(steps[-1].get("owner") == "C109/work-task-224", "C109 handoff step is missing")
    total = 0
    for index, step in enumerate(steps, 1):
        require(step.get("number") == index and step.get("id"), "sequence step identity is missing")
        for field in ("inputs", "preconditions", "commands", "expected_observables", "stop_conditions", "rollback", "checkpoint", "gate"):
            value = step.get(field)
            require(value not in (None, [], ""), f"sequence {field} is missing at step {index}")
        require(isinstance(step.get("timeout_seconds"), int) and step["timeout_seconds"] > 0, f"sequence timeout missing at step {index}")
        total += step["timeout_seconds"]
    require(total <= sequence.get("max_total_timeout_seconds", 0), "sequence timeout budget is exceeded")


def validate_report(report: dict[str, Any], *, expected_role: str | None = None) -> None:
    require(report.get("schema") == "audio-runtime-c109-v2", "unsupported report schema")
    require(report.get("project") == PROJECT and report.get("task") == TASK and report.get("contractRevision") == CONTRACT, "wrong project/task identity")
    role = report.get("role")
    require(role in {"required", "control"}, "report role is not required/control")
    if expected_role:
        require(role == expected_role, f"expected {expected_role} report, got {role}")
    provenance = report.get("provenance")
    require(isinstance(provenance, dict), "missing provenance")
    require(provenance.get("schema") == "audio-runtime-c109-provenance-v2", "provenance schema missing")
    require(provenance.get("project") == PROJECT and provenance.get("task") == TASK and provenance.get("contractRevision") == CONTRACT, "provenance identity missing")
    require(provenance.get("branch") == BRANCH, "provenance branch mismatch")
    require(provenance.get("factorySession") == "~default" and provenance.get("factoryServer"), "factory session/server provenance missing")
    require(provenance.get("startupIntegrationRevision") == "8bdafc7f947a3a2c9856220abdc539437035bd21", "startup integration revision missing")
    for name, value in provenance.get("candidateTrees", {}).items():
        require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value), f"candidate tree missing: {name}")
    candidates = provenance.get("candidates", {})
    for key, commit in (("c61", C61), ("c83", C83)):
        item = candidates.get(key, {})
        require(item.get("commit") == commit, f"changed {key.upper()} SHA")
        require(item.get("branch") == CANDIDATE_BRANCHES[key], f"{key.upper()} branch identity missing")
        require(item.get("branch_ref") == f"refs/heads/{CANDIDATE_BRANCHES[key]}", f"{key.upper()} branch ref missing")
        require(item.get("remote_ref") == f"refs/remotes/origin/{CANDIDATE_BRANCHES[key]}", f"{key.upper()} remote ref missing")
        hash_or_none(item.get("tree"), f"{key.upper()} tree")
    validate_refs(provenance)
    validate_preserved(provenance.get("preservedWorktrees", {}))
    validate_inventory(provenance)
    host = provenance.get("hostCheckout", {})
    require(host.get("branch") == BRANCH and host.get("scopeClean") is True, "host checkout scope is not clean")
    require(all(OWNED_PREFIX not in line for line in host.get("changedStatusLines", [])), "analyzer changed an unexpected host path")
    require(provenance.get("refsUnchanged") is True, "candidate refs or worktree refs changed")
    validate_rehearsal(report)
    validate_ledger(report.get("ledger", {}))
    historical = report.get("historicalC61Finding", {})
    require(historical.get("historical_run") == "34658619207" and historical.get("historical_job") == "103456263118", "C61 historical run/job missing")
    require(historical.get("fix_is_ancestor_of_review_main") is True and historical.get("focused_test_symbol_evidence"), "accepted-main C61 repair evidence is missing")
    require(historical.get("status") == "OPEN_RECONCILE_TEST_REWRITTEN", "C61 rewritten-test finding was incorrectly claimed resolved")
    require(historical.get("c61_test_blob") != historical.get("review_main_test_blob"), "rewritten-test distinction was not recorded")
    ci = report.get("c83CIAttribution", {})
    require(ci.get("evidence_id") == "c83-ci-34713381619" and ci.get("run") == "34713381619", "C83 current CI evidence missing")
    require(len(ci.get("passing_lanes", [])) == 7, "C83 seven passing lanes were not recorded")
    static = ci.get("static_findings", {})
    require(static.get("wire_registration", {}).get("owner") == "C79/work-task-114", "Wire registration owner drifted")
    require(static.get("stale_downward_baseline_entries", {}).get("count") == 9, "C83 stale-entry count drifted")
    require(static.get("intentional_instrumentation_drifts", {}).get("count") == 3, "C83 instrumentation drift count drifted")
    loss = ci.get("integration_loss", {})
    require(loss.get("lost_samples") == 6400 and loss.get("total_samples") == 174391, "provider-audio loss evidence drifted")
    require(loss.get("owner") == "C79/provider-audio" and loss.get("status") == "EXTERNAL_OWNER_DO_NOT_DUPLICATE_REPAIR_OR_RELABEL", "provider-audio loss was relabeled")
    validate_sequence(report.get("sequence", {}), provenance["reviewMain"])
    claims = report.get("deliveryClaims", {})
    require(claims.get("C61") == {"merged": False, "fixed": False, "probed": False, "accepted": False}, "C61 acceptance claim is forbidden")
    require(claims.get("C83") == {"merged": False, "fixed": False, "probed": False, "accepted": False}, "C83 acceptance claim is forbidden")
    require(claims.get("project") == {"accepted": False, "verticalProbe": False}, "project acceptance claim is forbidden")
    gates = report.get("broadGates", {})
    require(set(gates) == MANIFEST_GATES and all(value == "OPEN" for value in gates.values()), "manifest gate set was silently changed or closed")
    report_claims = report.get("claims", {})
    require(report_claims.get("softwareEvidenceOnly") is True and report_claims.get("hardwareOrAcousticEvidence") is False and report_claims.get("verticalProbe") is False, "software-only classification is invalid")
    require(report_claims.get("candidateDeliveryOrder") == "not-authorized", "candidate delivery authorization claim is forbidden")


def validate_public_report(data: dict[str, Any], expected_case: str | None = None) -> None:
    require(data.get("schema") == "audio-runtime-c109-public-checks-v2", "public check schema missing")
    if expected_case:
        require(data.get("case") == expected_case, "public check case mismatch")
    require(data.get("status") == "passed", "public check did not pass")
    synthetic = data.get("synthetic", {})
    require(synthetic.get("base") == ACCEPTED_MAIN and synthetic.get("order") == ["c61", "c83"], "public check uses the wrong synthetic ancestry")
    require(isinstance(synthetic.get("tree"), str) and re.fullmatch(r"[0-9a-f]{40}", synthetic["tree"]), "public synthetic tree hash is missing")
    require(synthetic.get("status") == [] and synthetic.get("tree_binding", {}).get("valid") is True, "public evidence is not bound to a clean synthetic tree")
    binding = synthetic["tree_binding"]
    require(binding.get("final_tree") == synthetic.get("tree"), "public tree hash is detached from its binding")
    require(binding.get("base") == ACCEPTED_MAIN and binding.get("candidate_commits") == [C61, C83], "public tree binding inputs are stale")
    require(binding.get("first_parent_order") == ["main", "c61", "c83"], "public tree binding order is incomplete")
    bounded = data.get("bounded", {})
    require(bounded.get("process_groups_clean") is True and bounded.get("elapsed_seconds", 10**9) <= bounded.get("aggregate_timeout_seconds", 0), "public aggregate bound or process cleanup failed")
    cleanup = data.get("cleanup", {})
    require(cleanup.get("attempted") is True and cleanup.get("status") == "passed" and cleanup.get("worktree_removed") is True and cleanup.get("temporary_root_removed") is True, "public temporary-tree cleanup is incomplete")
    require(data.get("credential_free") is True, "public credential-free proof is missing")
    checks = data.get("checks", [])
    require(checks and all(item.get("status") == "passed" and item.get("exit_code") == 0 and item.get("timed_out") is False and item.get("process_group_gone") is True and item.get("credential_environment_scrubbed") is True and item.get("credential_output_markers_absent") is True for item in checks), "public child evidence is incomplete")
    for item in checks:
        if "go test" in item.get("command", ""):
            discovery = item.get("test_discovery", {})
            require(discovery.get("required") is True and discovery.get("tests_discovered", 0) > 0, f"zero-test discovery accepted: {item.get('label')}")
    effects = data.get("effects", {})
    require(effects.get("cleanup_claim") is True and effects.get("artifacts"), "public effects/artifact evidence is incomplete")
    if data.get("case") == "browser-audio-tool":
        require(effects.get("observable_output"), "shipped public observable effects are missing")
        shipped = next((item for item in checks if item.get("label") == "shipped credential-free browser/audio/tool workflow"), None)
        require(shipped is not None and isinstance(shipped.get("workflow_report"), dict), "detailed shipped workflow report is missing")
        detail = shipped["workflow_report"]
        require(detail.get("result_classification") == "SOFTWARE_LOCAL_PROCESS_ONLY", "shipped workflow classification is invalid")
        require(detail.get("binary_sha256") and detail.get("recorded_pcm_sha256") and detail.get("terminal_manifest"), "shipped artifact/effect hashes are missing")
    else:
        require(effects.get("negative_control") is True, "negative public control classification is missing")


def verify_pair(required_path: Path, control_path: Path) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    required, required_dir = report_bundle(required_path)
    control, _ = report_bundle(control_path)
    validate_report(required, expected_role="required")
    validate_report(control, expected_role="control")
    for key in ("project", "task", "contractRevision", "provenance", "ledger", "historicalC61Finding", "c83CIAttribution", "sequence", "deliveryClaims", "broadGates", "claims"):
        require(required.get(key) == control.get(key), f"required/control evidence differs for {key}")
    require(required["rehearsal"]["final_tree"] == control["rehearsal"]["final_tree"], "merge orders did not converge to the same tree")
    return required, control, {"required_dir": str(required_dir)}


def run_analyzer_twice(root: Path) -> None:
    analyzer = Path(__file__).with_name("analyze.py")
    with tempfile.TemporaryDirectory(prefix="c109-determinism-") as temporary:
        first = Path(temporary) / "first"
        second = Path(temporary) / "second"
        common = [sys.executable, str(analyzer), "--main", ACCEPTED_MAIN, "--c61", C61, "--c83", C83, "--order", "c61-c83"]
        for output in (first, second):
            completed = subprocess.run([*common, "--output-dir", str(output)], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
            require(completed.returncode == 0, f"determinism analyzer run failed: {completed.stdout[-2000:]}")
        for name in ("provenance.json", "ledger.json", "merge-orders.json", "sequence.json", "report.json", "run-manifest.json"):
            require((first / name).read_bytes() == (second / name).read_bytes(), f"nondeterministic evidence: {name}")


def mutate_report(report: dict[str, Any], fixture: str) -> dict[str, Any]:
    mutated = copy.deepcopy(report)
    if fixture == "changed-sha":
        mutated["provenance"]["candidates"]["c61"]["commit"] = "0" * 40
    elif fixture == "reversed-order":
        mutated["rehearsal"]["order"] = list(reversed(mutated["rehearsal"]["order"]))
    elif fixture == "wrong-owner":
        for item in mutated["ledger"]["files"]:
            if item.get("path") != SHARED_RUNNER:
                item["owner"] = "C109/work-task-224"
                break
    elif fixture == "forbidden-claim":
        mutated["deliveryClaims"]["C61"]["merged"] = True
    elif fixture == "missing-conflict":
        del mutated["rehearsal"]["merges"][0]["conflicted_paths"]
    elif fixture == "missing-preserved":
        mutated["provenance"]["preservedWorktrees"]["before"]["worktrees"]["c61"]["present"] = False
    elif fixture == "truncated-ref":
        mutated["provenance"]["worktreeInventory"]["complete"] = False
    elif fixture == "heuristic-ledger":
        del mutated["ledger"]["files"][0]["hunks"][0]["symbols"][0]["caller_edges"]
    elif fixture == "incomplete-sequence":
        del mutated["sequence"]["steps"][1]["expected_observables"]
    elif fixture == "non-manifest-gate":
        mutated["broadGates"]["BROWSER"] = "OPEN"
    else:
        raise VerificationError(f"unknown report negative fixture {fixture}")
    return mutated


def mutate_public(data: dict[str, Any], fixture: str) -> dict[str, Any]:
    mutated = copy.deepcopy(data)
    if fixture == "public-unbound-tree":
        mutated["synthetic"]["tree"] = "0" * 40
    elif fixture == "cleanup-failure":
        mutated["cleanup"]["status"] = "failed"
    elif fixture == "zero-test-discovery":
        for item in mutated["checks"]:
            if "go test" in item.get("command", ""):
                item["test_discovery"]["tests_discovered"] = 0
                break
    else:
        raise VerificationError(f"unknown public negative fixture {fixture}")
    return mutated


def negative(report: dict[str, Any], public: dict[str, dict[str, Any]], fixture: str) -> dict[str, Any]:
    try:
        if fixture in {"public-unbound-tree", "cleanup-failure", "zero-test-discovery"}:
            case = "browser-audio-tool" if fixture != "zero-test-discovery" else "browser-audio-tool"
            validate_public_report(mutate_public(public[case], fixture), expected_case=case)
        else:
            validate_report(mutate_report(report, fixture), expected_role="required")
    except (VerificationError, KeyError) as exc:
        return {"fixture": fixture, "status": "rejected-as-required", "diagnostic": str(exc)}
    raise VerificationError(f"negative fixture was accepted: {fixture}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("provenance-and-scope", "merge-orders", "determinism", "behavior-and-attribution", "handoff-sequence", "negative", "all"), required=True)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--required", type=Path)
    parser.add_argument("--control", type=Path)
    parser.add_argument("--fixture", choices=("changed-sha", "reversed-order", "wrong-owner", "forbidden-claim", "missing-conflict", "missing-preserved", "truncated-ref", "heuristic-ledger", "incomplete-sequence", "non-manifest-gate", "public-unbound-tree", "cleanup-failure", "zero-test-discovery"))
    parser.add_argument("--write", type=Path)
    args = parser.parse_args()
    root = current_root()
    required_path = args.required or root / f"{OWNED_PREFIX}runs/final/required"
    control_path = args.control or root / f"{OWNED_PREFIX}runs/final/control"
    result: dict[str, Any] = {"mode": args.mode, "checks": []}
    public: dict[str, dict[str, Any]] = {}
    pair_mode = args.mode in {"merge-orders", "behavior-and-attribution", "handoff-sequence", "all"} or (args.required is not None and args.control is not None)
    if pair_mode:
        required, control, _ = verify_pair(required_path, control_path)
        result["checks"].extend(["provenance-and-scope", "merge-orders"])
        if args.mode in {"behavior-and-attribution", "all"}:
            result["checks"].append("behavior-and-attribution")
        if args.mode in {"handoff-sequence", "all"}:
            result["checks"].append("handoff-sequence")
        base_report = required
    else:
        base_report, _ = report_bundle(args.report or required_path)
        validate_report(base_report)
        result["checks"].append("provenance-and-scope")
    if args.mode == "determinism":
        run_analyzer_twice(root)
        result["checks"].append("determinism")
    elif args.mode == "negative":
        require(args.fixture is not None, "--fixture is required for negative mode")
        public_dir = root / f"{OWNED_PREFIX}runs/final/public"
        for case in ("browser-audio-tool", "malformed-or-canceled"):
            public[case] = load(public_dir / f"{case}.json")
        result["negative"] = negative(base_report, public, args.fixture)
        result["checks"].append(f"negative:{args.fixture}")
    elif args.mode == "all":
        run_analyzer_twice(root)
        result["checks"].append("determinism")
        fixtures = ("changed-sha", "reversed-order", "wrong-owner", "forbidden-claim", "missing-conflict", "missing-preserved", "truncated-ref", "heuristic-ledger", "incomplete-sequence", "non-manifest-gate")
        result["negative"] = {fixture: negative(base_report, {}, fixture) for fixture in fixtures}
        public_dir = root / f"{OWNED_PREFIX}runs/final/public"
        for case in ("browser-audio-tool", "malformed-or-canceled"):
            public[case] = load(public_dir / f"{case}.json")
            validate_public_report(public[case], expected_case=case)
        result["negative"].update({fixture: negative(base_report, public, fixture) for fixture in ("public-unbound-tree", "cleanup-failure", "zero-test-discovery")})
        result["checks"].extend(["negative-fixtures", "public-checks"])
    result["status"] = "passed"
    if args.write:
        args.write.parent.mkdir(parents=True, exist_ok=True)
        args.write.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.CalledProcessError) as exc:
        print(f"C109 verifier failed closed: {exc}", file=sys.stderr)
        raise SystemExit(2)
