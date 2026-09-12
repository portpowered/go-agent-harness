#!/usr/bin/env python3
"""Fail-closed verifier for C109 analyzer evidence and negative fixtures."""

from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from typing import Any


PROJECT = "audio-runtime"
TASK = "audio-runtime-c109-characterize-browser-retirement-integration"
CONTRACT = "audio-runtime-v1"
ACCEPTED_MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C61 = "8e8177c031a7b3b9322d712af19970e13fa7a1bc"
C83 = "22cc6769aaf06d1e2c1275b064cc7ec29de3e371"
SHARED_RUNNER = "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/"
C79_PATHS = {"scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"}


class VerificationError(RuntimeError):
    pass


def load(path: Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationError(f"cannot load {path}: {exc}") from exc


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def report_dir(path: Path) -> dict[str, Any]:
    report_path = path / "report.json" if path.is_dir() else path
    report = load(report_path)
    require(report.get("project") == PROJECT, "wrong project")
    require(report.get("task") == TASK, "wrong task")
    require(report.get("contractRevision") == CONTRACT, "wrong contract revision")
    return report


def validate_report(report: dict[str, Any], *, expected_role: str | None = None) -> None:
    require(report.get("schema") == "audio-runtime-c109-v1", "unsupported report schema")
    role = report.get("role")
    require(role in {"required", "control"}, "report role is not required/control")
    if expected_role:
        require(role == expected_role, f"expected {expected_role} report, got {role}")
    provenance = report.get("provenance")
    require(isinstance(provenance, dict), "missing provenance")
    require(provenance.get("requestedAcceptedMain") == ACCEPTED_MAIN, "stale or changed accepted-main SHA")
    require(provenance.get("reviewMain") == ACCEPTED_MAIN, "review main is not the admitted accepted main")
    require(provenance.get("candidates", {}).get("c61", {}).get("commit") == C61, "changed C61 SHA")
    require(provenance.get("candidates", {}).get("c83", {}).get("commit") == C83, "changed C83 SHA")
    require(provenance.get("candidateTrees", {}).get("c61"), "missing C61 tree")
    require(provenance.get("candidateTrees", {}).get("c83"), "missing C83 tree")
    require(provenance.get("candidateTrees", {}).get("reviewMain"), "missing main tree")
    require(provenance.get("refsUnchanged") is True, "candidate refs or worktree refs changed")
    preserved = provenance.get("preservedWorktrees", {})
    require(preserved.get("unchanged") is True, "preserved predecessor worktree changed")
    host = provenance.get("hostCheckout", {})
    require(host.get("scopeClean") is True, "host checkout scope is not clean")
    require(all(OWNED_PREFIX not in line for line in host.get("changedStatusLines", [])), "analyzer changed an unexpected host path")

    rehearsal = report.get("rehearsal", {})
    expected_order = ["c61", "c83"] if role == "required" else ["c83", "c61"]
    require(rehearsal.get("order") == expected_order, "rehearsal order is reversed or missing")
    merges = rehearsal.get("merges")
    require(isinstance(merges, list) and len(merges) == 2, "both merge steps are required")
    for index, merge in enumerate(merges):
        require(merge.get("label") == expected_order[index], "merge label/order mismatch")
        require(merge.get("expected_commit") in {C61, C83}, "merge uses an unsupported candidate SHA")
        require(merge.get("status") == "passed" and merge.get("exit_code") == 0, "merge rehearsal failed")
        require("conflicted_paths" in merge, "conflict evidence is missing")
        require(merge.get("conflicted_paths") == [], "merge conflict was hidden or unresolved")
        require(merge.get("conflict_resolution") == "not-needed", "unsupported conflict resolution claim")
        require(len(merge.get("parents", [])) == 3, "merge parents were not recorded")
        require(merge.get("tree"), "merge tree was not recorded")
        require(merge.get("post_status") == [], "synthetic merge tree is dirty")
    cleanup = rehearsal.get("cleanup", {})
    require(cleanup.get("worktree_removed") is True, "temporary worktree was not removed")
    require(cleanup.get("temporary_root_removed") is True, "temporary rehearsal directory was not removed")
    require(rehearsal.get("final_status") == [], "final synthetic tree is dirty")

    ledger = report.get("ledger", {})
    require(ledger.get("base", {}).get("candidateIntersection") == [SHARED_RUNNER], "unexpected candidate path intersection")
    require(ledger.get("unownedCandidatePaths") == [], "candidate path was attributed to C109")
    require(ledger.get("apiConclusion", {}).get("c83_requires_c61_api") is False, "C83 API dependency claim is unsupported")
    files = ledger.get("files", [])
    shared = next((item for item in files if item.get("path") == SHARED_RUNNER and "symbols" in item), None)
    require(shared is not None, "shared runner symbol ledger is missing")
    require(shared.get("base_blob") and shared.get("c61_blob") and shared.get("c83_blob"), "shared runner blob hashes are incomplete")
    for item in files:
        path = item.get("path", "")
        require(path, "ledger entry has no path")
        require(item.get("owner"), f"ledger entry has no owner: {path}")
        if path in C79_PATHS:
            require("C79" in item["owner"], f"C79 shared path attributed to candidate: {path}")
        elif path != SHARED_RUNNER:
            require("C109" not in item["owner"], f"candidate path was incorrectly attributed to C109: {path}")

    historical = report.get("historicalC61Finding", {})
    require(historical.get("historical_run") == "34658619207", "C61 historical run missing")
    require(historical.get("historical_job") == "103456263118", "C61 historical job missing")
    require(historical.get("fix_is_ancestor_of_review_main") is True, "accepted-main C61 repair ancestry is missing")
    require(historical.get("focused_test_symbol_evidence"), "historical C61 focused symbol evidence is missing")
    require(historical.get("status") == "OPEN_RECONCILE_TEST_REWRITTEN", "C61 rewritten-test finding was incorrectly claimed resolved")
    require(historical.get("c61_test_blob") != historical.get("review_main_test_blob"), "rewritten-test distinction was not recorded")

    ci = report.get("c83CIAttribution", {})
    require(ci.get("run") == "34713381619", "C83 current CI run missing")
    require(len(ci.get("passing_lanes", [])) == 7, "C83 seven passing lanes were not recorded")
    static = ci.get("static_findings", {})
    require(static.get("wire_registration", {}).get("owner") == "C79/work-task-114", "Wire registration owner drifted")
    require(static.get("stale_downward_baseline_entries", {}).get("count") == 9, "C83 stale-entry count drifted")
    require(static.get("intentional_instrumentation_drifts", {}).get("count") == 3, "C83 instrumentation drift count drifted")
    loss = ci.get("integration_loss", {})
    require(loss.get("lost_samples") == 6400 and loss.get("total_samples") == 174391, "provider-audio loss evidence drifted")
    require(loss.get("owner") == "C79/provider-audio", "provider-audio loss was relabeled")

    sequence = report.get("sequence", {})
    steps = sequence.get("steps", [])
    require([step.get("owner") for step in steps[:3]] == ["C79/work-task-114", "C61/work-task-34", "C83/work-task-125"], "bounded owner sequence is wrong")
    require(steps[-1].get("owner") == "C109/work-task-224", "C109 handoff step is missing")
    claims = report.get("deliveryClaims", {})
    require(claims.get("C61") == {"merged": False, "fixed": False, "probed": False, "accepted": False}, "C61 acceptance claim is forbidden")
    require(claims.get("C83") == {"merged": False, "fixed": False, "probed": False, "accepted": False}, "C83 acceptance claim is forbidden")
    require(claims.get("project") == {"accepted": False, "verticalProbe": False}, "project acceptance claim is forbidden")
    require(all(value == "OPEN" for value in report.get("broadGates", {}).values()), "broad gate was silently closed")
    report_claims = report.get("claims", {})
    require(report_claims.get("softwareEvidenceOnly") is True, "software-only classification missing")
    require(report_claims.get("hardwareOrAcousticEvidence") is False, "hardware/acoustic claim is forbidden")
    require(report_claims.get("verticalProbe") is False, "vertical probe claim is forbidden")
    require(report_claims.get("candidateDeliveryOrder") == "not-authorized", "candidate delivery authorization claim is forbidden")


def verify_pair(required_path: Path, control_path: Path) -> dict[str, Any]:
    required = report_dir(required_path)
    control = report_dir(control_path)
    validate_report(required, expected_role="required")
    validate_report(control, expected_role="control")
    for key in ("project", "task", "contractRevision", "provenance", "ledger", "historicalC61Finding", "c83CIAttribution", "sequence", "deliveryClaims", "broadGates", "claims"):
        require(required.get(key) == control.get(key), f"required/control evidence differs for {key}")
    require(required["rehearsal"]["final_tree"] == control["rehearsal"]["final_tree"], "merge orders did not converge to the same tree")
    return {"required": required, "control": control}


def run_analyzer_twice(root: Path) -> None:
    analyzer = Path(__file__).with_name("analyze.py")
    with tempfile.TemporaryDirectory(prefix="c109-determinism-") as temp:
        first = Path(temp) / "first"
        second = Path(temp) / "second"
        common = [
            sys.executable,
            str(analyzer),
            "--main",
            ACCEPTED_MAIN,
            "--c61",
            C61,
            "--c83",
            C83,
            "--order",
            "c61-c83",
        ]
        for output in (first, second):
            completed = subprocess.run([*common, "--output-dir", str(output)], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
            require(completed.returncode == 0, f"determinism analyzer run failed: {completed.stdout[-2000:]}")
        for name in ("provenance.json", "ledger.json", "merge-orders.json", "sequence.json", "report.json"):
            require((first / name).read_bytes() == (second / name).read_bytes(), f"nondeterministic evidence: {name}")


def mutate(report: dict[str, Any], fixture: str) -> dict[str, Any]:
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
    else:
        raise VerificationError(f"unknown negative fixture {fixture}")
    return mutated


def negative(report: dict[str, Any], fixture: str) -> dict[str, Any]:
    mutated = mutate(report, fixture)
    try:
        validate_report(mutated)
    except VerificationError as exc:
        return {"fixture": fixture, "status": "rejected-as-required", "diagnostic": str(exc)}
    raise VerificationError(f"negative fixture was accepted: {fixture}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("provenance-and-scope", "merge-orders", "determinism", "behavior-and-attribution", "handoff-sequence", "negative", "all"), required=True)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--required", type=Path)
    parser.add_argument("--control", type=Path)
    parser.add_argument("--fixture", choices=("changed-sha", "reversed-order", "wrong-owner", "forbidden-claim", "missing-conflict"))
    parser.add_argument("--write", type=Path)
    args = parser.parse_args()
    root = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()

    required_path = args.required or root / "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/runs/final/required"
    control_path = args.control or root / "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/runs/final/control"
    result: dict[str, Any] = {"mode": args.mode, "checks": []}
    pair_mode = args.mode in {"merge-orders", "behavior-and-attribution", "handoff-sequence", "all"} or (args.required is not None and args.control is not None)
    if pair_mode:
        pair = verify_pair(required_path, control_path)
        result["checks"].append("provenance-and-scope")
        result["checks"].append("merge-orders")
        if args.mode in {"behavior-and-attribution", "all"}:
            result["checks"].append("behavior-and-attribution")
        if args.mode in {"handoff-sequence", "all"}:
            result["checks"].append("handoff-sequence")
        base_report = pair["required"]
    else:
        base_report = report_dir(args.report or required_path)
        validate_report(base_report)
        result["checks"].append("provenance-and-scope")
    if args.mode == "determinism":
        run_analyzer_twice(root)
        result["checks"].append("determinism")
    elif args.mode == "negative":
        require(args.fixture is not None, "--fixture is required for negative mode")
        result["negative"] = negative(base_report, args.fixture)
        result["checks"].append(f"negative:{args.fixture}")
    elif args.mode == "all":
        run_analyzer_twice(root)
        result["checks"].append("determinism")
        result["negative"] = {fixture: negative(base_report, fixture) for fixture in ("changed-sha", "reversed-order", "wrong-owner", "forbidden-claim", "missing-conflict")}
        result["checks"].append("negative-fixtures")
        public_dir = root / "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/runs/final/public"
        for name in ("browser-audio-tool.json", "malformed-or-canceled.json"):
            data = load(public_dir / name)
            require(data.get("status") == "passed", f"public check failed: {name}")
        result["checks"].append("public-checks")
    if args.write:
        args.write.parent.mkdir(parents=True, exist_ok=True)
        args.write.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "passed", **result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.CalledProcessError) as exc:
        print(f"C109 verifier failed closed: {exc}", file=sys.stderr)
        raise SystemExit(2)
