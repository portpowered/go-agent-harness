#!/usr/bin/env python3
"""Bounded C124 recovery provenance, retirement, and mutation verifier."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
CONSUMER = HERE / "external-consumer"
EXPECTED_PROJECT = "audio-runtime"
EXPECTED_CONTRACT = "audio-runtime-v1"
EXPECTED_BRANCH = "codex/audio-runtime-c124-recover-c84-room-participant-lifecycle"
PRESERVED_CANDIDATE = "3e9aee5920e78126480d4fc438f688dc39216c1d"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PLANNING_MAIN = "09c70f51243caeaf1184c4806b99bbf7749e3044"
MANIFEST_BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
LEGACY = (
    pathlib.Path("agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go"),
    pathlib.Path("agent-cli/internal/services/internal/agentruntime/session_room_tracked_session.go"),
)
OWNED = {
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_tracked_session.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle_test.go",
    "go-agent-runtime/services/rooms/participant_lifecycle.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant_tracker.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant_tracker_test.go",
    "go-agent-runtime/services/rooms/wire/participant_lifecycle.go",
    "go-agent-runtime/services/rooms/wire/wire_gen.go",
    "coverage-manifest/go-agent-runtime/services/rooms/internal/lifecycle/package.json",
    "docs/architecture/architecture-policy.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_room_tracked_session.go.json",
}
EVIDENCE_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c124-recover-c84-room-participant-lifecycle/"
PROGRESS_LEDGER = "progress.txt"
BASELINE_LEGACY_LINES = 1391
RETAINED_LEGACY_LINES = 522
RETIRED_LEGACY_LINES = 869

# These files are the preserved C84 extraction. They are intentionally carried
# into the recovery candidate even though C124's new delta owns only the
# reconciliation paths listed in OWNED. The provenance check keeps that
# distinction machine-readable rather than silently broadening the repair.
PRESERVED_C84 = {
    "go-agent-runtime/services/rooms/internal/lifecycle/browser_validation_test.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/graph_media.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/media.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant_session.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant_tracker.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant_tracker_test.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/result.go",
    "go-agent-runtime/services/rooms/participant_lifecycle.go",
}


class EvidenceFailure(RuntimeError):
    pass


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run(command: list[str], cwd: pathlib.Path, timeout: float = 120.0) -> dict[str, Any]:
    try:
        completed = subprocess.run(command, cwd=cwd, env={**os.environ, "GOWORK": "off"}, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        return {"command": command, "timed_out": True, "exit_code": None, "stdout": exc.stdout or "", "stderr": exc.stderr or ""}
    return {"command": command, "timed_out": False, "exit_code": completed.returncode, "stdout": completed.stdout, "stderr": completed.stderr}


def require_passed(result: dict[str, Any], label: str) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{label} failed: {result['stderr'][-2000:]}")


def require_failed_assertion(result: dict[str, Any], label: str) -> None:
    if result["timed_out"] or result["exit_code"] == 0:
        raise EvidenceFailure(f"{label} did not fail its intended oracle")
    combined = result["stdout"] + result["stderr"]
    if "FAIL" not in combined and "failed" not in combined.lower():
        raise EvidenceFailure(f"{label} failed without an assertion/test failure: {combined[-2000:]}")


def consumer_test(label: str) -> dict[str, Any]:
    return run(["rtk", "proxy", "go", "test", "-mod=readonly", "./...", "-count=1", "-timeout=90s"], CONSUMER)


def mutation_control(name: str, source: pathlib.Path, original: str, mutated: str) -> dict[str, Any]:
    text = source.read_text(encoding="utf-8")
    if text.count(original) != 1:
        raise EvidenceFailure(f"{name}: expected exactly one mutation anchor")
    positive = consumer_test(f"{name}-positive")
    require_passed(positive, f"{name} positive control")
    record: dict[str, Any] = {"name": name, "positive": positive, "mutation": {"source": str(source.relative_to(ROOT)), "old": original, "new": mutated}}
    try:
        source.write_text(text.replace(original, mutated, 1), encoding="utf-8")
        negative = consumer_test(f"{name}-mutation")
        require_failed_assertion(negative, name)
        record["negative"] = negative
    finally:
        source.write_text(text, encoding="utf-8")
    restored = source.read_text(encoding="utf-8")
    if restored != text:
        raise EvidenceFailure(f"{name}: source was not restored byte-for-byte")
    restored_positive = consumer_test(f"{name}-restored")
    require_passed(restored_positive, f"{name} restored positive control")
    record["restored"] = restored_positive
    return record


def current_paths() -> list[str]:
    result = run(["git", "status", "--short", "--untracked-files=all"], ROOT)
    require_passed(result, "git status")
    paths: list[str] = []
    for line in result["stdout"].splitlines():
        if len(line) < 4:
            continue
        value = line[3:]
        if " -> " in value:
            value = value.rsplit(" -> ", 1)[1]
        paths.append(value)
    return paths


def committed_paths() -> list[str]:
    current_main = run(["git", "rev-parse", "origin/main"], ROOT)
    require_passed(current_main, "origin/main revision")
    diff = run(["git", "diff", "--name-only", f"{current_main['stdout'].strip()}...HEAD"], ROOT)
    require_passed(diff, "committed candidate scope")
    return [line for line in diff["stdout"].splitlines() if line]


def verify_retirement() -> dict[str, Any]:
    paths = sorted(set(committed_paths() + current_paths()))
    unexpected = [
        path
        for path in paths
        if path != PROGRESS_LEDGER and path not in OWNED and path not in PRESERVED_C84 and not path.startswith(EVIDENCE_PREFIX)
    ]
    if unexpected:
        raise EvidenceFailure(f"unowned changed paths: {unexpected}")
    counts = {str(path): len((ROOT / path).read_text(encoding="utf-8").splitlines()) for path in LEGACY}
    total = sum(counts.values())
    if total != RETAINED_LEGACY_LINES:
        raise EvidenceFailure(f"legacy files total {total}, want exactly {RETAINED_LEGACY_LINES}")
    retired = BASELINE_LEGACY_LINES - total
    if retired != RETIRED_LEGACY_LINES:
        raise EvidenceFailure(f"retired {retired}, want exactly {RETIRED_LEGACY_LINES}")
    for path in LEGACY:
        contents = (ROOT / path).read_text(encoding="utf-8")
        if "Deprecated:" not in contents:
            raise EvidenceFailure(f"{path} has no explicit Deprecated adapter marker")
    retained = {
        "roomParticipantPlan": "legacy caller data shape",
        "roomParticipantRuntime": "legacy worker handles and completion channels",
        "roomCleanupWaiter": "legacy bounded cleanup timer adapter",
        "roomLifecycleWorkError": "legacy result/error formatting adapter",
        "roomParticipantLifecycle": "Deprecated runtime lifecycle façade",
        "roomTrackedSession": "Deprecated runtime tracked-session façade",
        "roomConnectTrackingInferencer": "legacy admission-barrier façade",
    }
    report = {
        "changed_paths": sorted(paths),
        "ledger_paths": [path for path in paths if path == PROGRESS_LEDGER],
        "legacy_line_counts": counts,
        "legacy_total": total,
        "retired_lines": retired,
        "baseline_lines": BASELINE_LEGACY_LINES,
        "adapter_policy": "explicit Deprecated decision-free adapters only",
        "retained_symbols": retained,
    }
    write_json(HERE / "retirement-and-owned-paths.json", report)
    return report


def git_tree_paths(prefix: str) -> list[str]:
    result = run(["git", "ls-tree", "-r", "--name-only", "HEAD", "--", prefix], ROOT)
    require_passed(result, f"git tree {prefix}")
    return [line for line in result["stdout"].splitlines() if line]


def verify_excluded_bytes() -> dict[str, Any]:
    current_main = run(["git", "rev-parse", "origin/main"], ROOT)
    require_passed(current_main, "origin/main revision")
    current_main_revision = current_main["stdout"].strip()
    ancestry = run(["git", "merge-base", "--is-ancestor", current_main_revision, "HEAD"], ROOT)
    require_passed(ancestry, "current origin/main ancestry")
    candidates = git_tree_paths("agent-cli/internal/services/internal/agentruntime")
    candidates += git_tree_paths("go-agent-runtime/services/rooms")
    candidates += ["scripts/wire-packages.txt"]
    allowed = OWNED | PRESERVED_C84
    checked: list[str] = []
    changed: list[str] = []
    for path in sorted(set(candidates)):
        if path in allowed:
            continue
        baseline = run(["git", "show", f"{current_main_revision}:{path}"], ROOT)
        require_passed(baseline, f"current-main bytes {path}")
        current_path = ROOT / path
        if not current_path.is_file() or baseline["stdout"].encode() != current_path.read_bytes():
            changed.append(path)
        checked.append(path)
    if changed:
        raise EvidenceFailure(f"excluded bytes changed: {changed}")
    report = {
        "project": EXPECTED_PROJECT,
        "contract_revision": EXPECTED_CONTRACT,
        "accepted_main": PLANNING_MAIN,
        "baseline": current_main_revision,
        "checked_paths": checked,
        "changed_paths": changed,
    }
    write_json(HERE / "excluded-path-byte-check.json", report)
    return report


def git_value(arguments: list[str], label: str) -> str:
    result = run(["git", *arguments], ROOT)
    require_passed(result, label)
    return result["stdout"].strip()


def require_ancestor(ancestor: str, descendant: str, label: str) -> None:
    result = run(["git", "merge-base", "--is-ancestor", ancestor, descendant], ROOT)
    require_passed(result, label)


def final_allowed(path: str) -> bool:
    return path in OWNED or path in PRESERVED_C84 or path.startswith(EVIDENCE_PREFIX)


def verify_provenance() -> dict[str, Any]:
    branch = git_value(["branch", "--show-current"], "branch identity")
    if branch != EXPECTED_BRANCH:
        raise EvidenceFailure(f"branch {branch!r} does not match {EXPECTED_BRANCH!r}")
    current_main = git_value(["rev-parse", "origin/main"], "origin/main revision")
    require_ancestor(PLANNING_MAIN, current_main, "planning main ancestry")
    require_ancestor(PRESERVED_CANDIDATE, "HEAD", "preserved candidate ancestry")
    require_ancestor(STARTUP_INTEGRATION, "HEAD", "startup integration ancestry")
    require_ancestor(current_main, "HEAD", "current origin/main ancestry")

    committed = committed_paths()
    current = current_paths()
    final_paths = sorted(set(committed + current))
    unexpected = [path for path in final_paths if not final_allowed(path)]
    if unexpected:
        raise EvidenceFailure(f"unowned final paths: {unexpected}")

    delta = git_value(["diff", "--name-only", f"{PRESERVED_CANDIDATE}..HEAD"], "preserved candidate delta")
    classifications: list[dict[str, str]] = []
    reconciliation = {
        "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go.json",
        "go-agent-runtime/services/rooms/wire/participant_lifecycle.go",
        "go-agent-runtime/services/rooms/wire/wire_gen.go",
    }
    for path in [item for item in delta.splitlines() if item]:
        if path.startswith(EVIDENCE_PREFIX):
            category = "evidence relocation/refresh"
        elif path in reconciliation:
            category = "deterministic owned Wire/architecture reconciliation"
        elif path == PROGRESS_LEDGER or path in PRESERVED_C84 or path in OWNED:
            category = "accepted-main integration or preserved predecessor adoption"
        else:
            category = "accepted-main integration"
        classifications.append({"path": path, "classification": category})

    diff_check = run(["git", "diff", "--check"], ROOT)
    require_passed(diff_check, "candidate diff check")
    report = {
        "project": EXPECTED_PROJECT,
        "contract_revision": EXPECTED_CONTRACT,
        "branch": branch,
        "manifest_baseline": MANIFEST_BASELINE,
        "startup_integration": STARTUP_INTEGRATION,
        "preserved_candidate": PRESERVED_CANDIDATE,
        "planning_main": PLANNING_MAIN,
        "origin_main": current_main,
        "committed_final_paths": committed,
        "current_paths": current,
        "final_paths": final_paths,
        "candidate_delta_classification": classifications,
        "diff_check": "passed",
    }
    write_json(HERE / "provenance.json", report)
    write_json(HERE / "delta-classification.json", classifications)
    return report


def verify_architecture_delta() -> dict[str, Any]:
    baseline_path = ROOT / "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go.json"
    try:
        baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"lifecycle baseline is unreadable: {error}") from error
    entries = baseline.get("entries")
    if not isinstance(entries, list):
        raise EvidenceFailure("lifecycle baseline entries are not a list")
    retained = [
        (entry.get("rule"), entry.get("symbol"), entry.get("value"))
        for entry in entries
    ]
    expected = {
        ("cognitive-complexity", "roomParticipantOutstandingWork", 21),
        ("cyclomatic-complexity", "roomParticipantOutstandingWork", 21),
        ("file-lines", None, 427),
    }
    observed = set(retained)
    if observed != expected or len(entries) != len(expected):
        raise EvidenceFailure(f"lifecycle baseline entries = {retained!r}, want {sorted(expected)!r}")
    for stale in (
        "*roomConnectTrackingInferencer.ConnectSession",
        "*roomParticipantLifecycle.markCoordinatorStopping",
        "*roomParticipantLifecycle.observe",
        "*roomParticipantLifecycle.observeTerminal",
        "*roomParticipantLifecycle.terminalObservationSnapshot",
    ):
        if any(entry.get("symbol") == stale for entry in entries):
            raise EvidenceFailure(f"stale C84 baseline entry remains: {stale}")
    gate = run(["rtk", "make", "architecture-size-check"], ROOT, timeout=300.0)
    require_passed(gate, "architecture-size-check")
    report = {
        "baseline_path": str(baseline_path.relative_to(ROOT)),
        "retained_entries": retained,
        "removed_stale_symbols": [
            "*roomConnectTrackingInferencer.ConnectSession",
            "*roomParticipantLifecycle.markCoordinatorStopping",
            "*roomParticipantLifecycle.observe",
            "*roomParticipantLifecycle.observeTerminal",
            "*roomParticipantLifecycle.terminalObservationSnapshot",
        ],
        "gate": {"exit_code": gate["exit_code"], "stdout": gate["stdout"][-4000:], "stderr": gate["stderr"][-4000:]},
    }
    write_json(HERE / "architecture-delta.json", report)
    return report


def verify_delivery_provenance() -> dict[str, Any]:
    report = verify_provenance()
    if report["origin_main"] != PLANNING_MAIN:
        # A later main is valid only when it remains an ancestor of the
        # candidate; the exact refreshed identity is retained in the report.
        require_ancestor(report["origin_main"], "HEAD", "refreshed origin/main ancestry")
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        required=True,
        choices=(
            "provenance-and-owned-deltas",
            "excluded-path-byte-check",
            "mutation-post-bound-tool",
            "mutation-double-close",
            "retirement-and-adapter-policy",
            "architecture-delta",
            "delivery-provenance",
        ),
    )
    parser.add_argument("--expect-failure", action="store_true")
    args = parser.parse_args()
    try:
        if args.mode == "provenance-and-owned-deltas":
            report = verify_provenance()
        elif args.mode == "retirement-and-adapter-policy":
            report = verify_retirement()
        elif args.mode == "excluded-path-byte-check":
            report = verify_excluded_bytes()
        elif args.mode == "architecture-delta":
            report = verify_architecture_delta()
        elif args.mode == "delivery-provenance":
            report = verify_delivery_provenance()
        elif args.mode == "mutation-post-bound-tool":
            if not args.expect_failure:
                raise EvidenceFailure("mutation mode requires --expect-failure")
            source = ROOT / "go-agent-runtime/services/rooms/internal/lifecycle/participant_session.go"
            original = "return s.lifecycle != nil && s.lifecycle.AdmitCompleteToolResultAfterBound(msg)"
            report = mutation_control(args.mode, source, original, "return false")
        else:
            if not args.expect_failure:
                raise EvidenceFailure("mutation mode requires --expect-failure")
            source = ROOT / "go-agent-runtime/services/rooms/internal/lifecycle/participant_session.go"
            original = "s.once.Do(s.closeOnce)"
            mutated = "s.closeOnce()"
            report = mutation_control(args.mode, source, original, mutated)
        if args.mode.startswith("mutation-"):
            write_json(HERE / f"{args.mode}.json", report)
        print(json.dumps({"status": "passed", "mode": args.mode, "report": report}, sort_keys=True))
        return 0
    except EvidenceFailure as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
