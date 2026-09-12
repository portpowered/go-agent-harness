#!/usr/bin/env python3
"""Produce deterministic, evidence-only C109 integration characterization."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from typing import Any, Iterable


PROJECT = "audio-runtime"
TASK = "audio-runtime-c109-characterize-browser-retirement-integration"
CONTRACT = "audio-runtime-v1"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
ACCEPTED_MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C61 = "8e8177c031a7b3b9322d712af19970e13fa7a1bc"
C83 = "22cc6769aaf06d1e2c1275b064cc7ec29de3e371"
SHARED_RUNNER = "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/"
C79_PATHS = {
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
}
PRESERVED_WORKTREES = {
    "c61": ".claude/worktrees/audio-runtime-c61-retire-cli-browser-scenario-contract",
    "c83": ".claude/worktrees/audio-runtime-c83-retire-cli-browser-scenario-runner",
}
FIXED_DATE = "2020-01-01T00:00:00Z"


class EvidenceError(RuntimeError):
    pass


def command_text(argv: Iterable[str]) -> str:
    return " ".join(str(part) for part in argv)


def run(
    argv: list[str],
    cwd: Path,
    *,
    check: bool = True,
    env: dict[str, str] | None = None,
    timeout: int = 120,
) -> dict[str, Any]:
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    try:
        completed = subprocess.run(
            argv,
            cwd=cwd,
            env=merged_env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        output = (exc.stdout or "") if isinstance(exc.stdout, str) else ""
        if check:
            raise EvidenceError(f"timeout: {command_text(argv)}\n{output[-4000:]}") from exc
        return {
            "command": command_text(argv),
            "status": "timeout",
            "exit_code": None,
            "output": output[-4000:],
        }
    result = {
        "command": command_text(argv),
        "status": "passed" if completed.returncode == 0 else "failed",
        "exit_code": completed.returncode,
        "output": completed.stdout[-12000:],
    }
    if check and completed.returncode != 0:
        raise EvidenceError(f"command failed: {command_text(argv)}\n{completed.stdout[-6000:]}")
    return result


def git(root: Path, args: list[str], *, check: bool = True, cwd: Path | None = None, timeout: int = 120) -> dict[str, Any]:
    return run(["git", *args], cwd=cwd or root, check=check, timeout=timeout)


def git_output(root: Path, args: list[str], *, cwd: Path | None = None, check: bool = True) -> str:
    return git(root, args, cwd=cwd, check=check)["output"].strip()


def revision(root: Path, ref: str, *, cwd: Path | None = None) -> str:
    return git_output(root, ["rev-parse", f"{ref}^{{commit}}"], cwd=cwd)


def tree(root: Path, ref: str, *, cwd: Path | None = None) -> str:
    return git_output(root, ["rev-parse", f"{ref}^{{tree}}"], cwd=cwd)


def maybe_blob(root: Path, ref: str, path: str, *, cwd: Path | None = None) -> str | None:
    result = git(root, ["rev-parse", f"{ref}:{path}"], cwd=cwd, check=False)
    if result["exit_code"] != 0:
        return None
    return result["output"].strip()


def status_lines(root: Path, *, cwd: Path | None = None) -> list[str]:
    output = git_output(
        root,
        ["status", "--porcelain=v1", "--untracked-files=all"],
        cwd=cwd,
    )
    return output.splitlines() if output else []


def show_refs(root: Path) -> list[str]:
    output = git_output(root, ["show-ref"], check=False)
    return output.splitlines() if output else []


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_worktrees(root: Path) -> list[dict[str, str]]:
    output = git_output(root, ["worktree", "list", "--porcelain"])
    records: list[dict[str, str]] = []
    current: dict[str, str] = {}
    for line in output.splitlines() + [""]:
        if line.startswith("worktree "):
            if current:
                records.append(current)
            current = {"path": line[9:]}
        elif line.startswith("HEAD "):
            current["head"] = line[5:]
        elif line.startswith("branch "):
            current["branch"] = line[7:]
        elif line == "" and current:
            records.append(current)
            current = {}
    return records


def preserved_snapshot(root: Path) -> dict[str, Any]:
    result: dict[str, Any] = {"refs": show_refs(root), "worktrees": {}}
    for name, relative in PRESERVED_WORKTREES.items():
        path = root / relative
        if not path.exists():
            result["worktrees"][name] = {"path": relative, "present": False}
            continue
        result["worktrees"][name] = {
            "path": relative,
            "present": True,
            "head": revision(root, "HEAD", cwd=path),
            "tree": tree(root, "HEAD", cwd=path),
            "branch": git_output(root, ["symbolic-ref", "--short", "-q", "HEAD"], cwd=path, check=False),
            "status": status_lines(root, cwd=path),
        }
    return result


def diff_paths(root: Path, base: str, head: str) -> list[str]:
    output = git_output(root, ["diff", "--name-only", f"{base}...{head}"])
    return sorted(line for line in output.splitlines() if line)


def line_matches(root: Path, ref: str, pattern: str, *, limit: int = 20) -> list[str]:
    result = git(root, ["grep", "-n", "-F", pattern, ref], check=False)
    if result["exit_code"] not in (0, 1):
        return []
    matches: list[str] = []
    for line in result["output"].splitlines():
        # `git grep ref -- pattern` is awkward to make stable across versions;
        # normalize the ref prefix if present and retain source line evidence.
        if line.startswith(f"{ref}:"):
            line = line[len(ref) + 1 :]
        matches.append(line)
        if len(matches) == limit:
            break
    return matches


def symbol_locations(root: Path, ref: str, symbol: str) -> dict[str, Any]:
    matches = line_matches(root, ref, symbol)
    declarations = [line for line in matches if "func " in line or "type " in line]
    return {"symbol": symbol, "declarations": declarations, "references": matches}


def candidate_ref_snapshot(root: Path, ref: str, name: str) -> dict[str, Any]:
    commit = revision(root, ref)
    return {
        "name": name,
        "ref": ref,
        "commit": commit,
        "tree": tree(root, ref),
        "parent_commits": git_output(root, ["rev-list", "--parents", "-n", "1", ref]).split(),
        "status": "preserved-readonly-ref",
    }


def commit_rehearsal(root: Path, base: str, order: list[tuple[str, str]], role: str) -> dict[str, Any]:
    temporary_root = Path(tempfile.mkdtemp(prefix="c109-rehearsal-"))
    worktree = temporary_root / "tree"
    merge_records: list[dict[str, Any]] = []
    cleanup: dict[str, Any] = {"worktree_removed": False, "temporary_root_removed": False}
    final: dict[str, Any] = {
        "role": role,
        "base": base,
        "base_tree": tree(root, base),
        "order": [label for label, _ in order],
        "merges": merge_records,
        "final_head": None,
        "final_tree": None,
        "final_status": [],
        "worktree_path_recorded": "temporary path intentionally omitted from normalized evidence",
    }
    fixed_env = {
        "GIT_AUTHOR_NAME": "C109 Rehearsal",
        "GIT_AUTHOR_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_COMMITTER_NAME": "C109 Rehearsal",
        "GIT_COMMITTER_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_AUTHOR_DATE": FIXED_DATE,
        "GIT_COMMITTER_DATE": FIXED_DATE,
        "GIT_MERGE_AUTOEDIT": "no",
        "LC_ALL": "C",
    }
    try:
        add = run(["git", "worktree", "add", "--detach", str(worktree), base], cwd=root)
        for label, ref in order:
            command = ["git", "merge", "--no-ff", "--no-commit", ref]
            merge = run(command, cwd=worktree, check=False, env=fixed_env)
            conflicts = git_output(root, ["diff", "--name-only", "--diff-filter=U"], cwd=worktree, check=False)
            record: dict[str, Any] = {
                "label": label,
                "ref": ref,
                "expected_commit": revision(root, ref),
                "command": command_text(command),
                "status": merge["status"],
                "exit_code": merge["exit_code"],
                "conflicted_paths": conflicts.splitlines() if conflicts else [],
                "output": merge["output"][-4000:],
            }
            if merge["exit_code"] == 0:
                commit = run(
                    ["git", "commit", "--no-verify", "-m", f"C109 {role} merge {label}"],
                    cwd=worktree,
                    env=fixed_env,
                )
                record.update(
                    {
                        "commit_command": commit["command"],
                        "commit": revision(root, "HEAD", cwd=worktree),
                        "parents": git_output(root, ["rev-list", "--parents", "-n", "1", "HEAD"], cwd=worktree).split(),
                        "tree": tree(root, "HEAD", cwd=worktree),
                        "post_status": status_lines(root, cwd=worktree),
                        "conflict_resolution": "not-needed",
                    }
                )
            else:
                record.update(
                    {
                        "conflict_diff": git_output(root, ["diff", "--cc"], cwd=worktree, check=False)[-12000:],
                        "post_status": status_lines(root, cwd=worktree),
                        "conflict_resolution": "aborted; C109 does not resolve candidate conflicts",
                    }
                )
                git(root, ["merge", "--abort"], cwd=worktree, check=False)
            merge_records.append(record)
            if merge["exit_code"] != 0:
                break
        final.update(
            {
                "final_head": revision(root, "HEAD", cwd=worktree) if merge_records and merge_records[-1]["exit_code"] == 0 else None,
                "final_tree": tree(root, "HEAD", cwd=worktree) if merge_records and merge_records[-1]["exit_code"] == 0 else None,
                "final_status": status_lines(root, cwd=worktree),
                "add_command": "git worktree add --detach <temporary-worktree> <base>",
            }
        )
    finally:
        remove = run(["git", "worktree", "remove", "--force", str(worktree)], cwd=root, check=False)
        cleanup["worktree_remove_command"] = "git worktree remove --force <temporary-worktree>"
        cleanup["worktree_remove_status"] = remove["status"]
        cleanup["worktree_removed"] = not worktree.exists()
        shutil.rmtree(temporary_root, ignore_errors=True)
        cleanup["temporary_root_removed"] = not temporary_root.exists()
    final["cleanup"] = cleanup
    return final


def blob_record(root: Path, base: str, c61: str, c83: str, path: str) -> dict[str, Any]:
    base_commit = git_output(root, ["merge-base", base, c61])
    base_blob = maybe_blob(root, base_commit, path)
    c61_blob = maybe_blob(root, c61, path)
    c83_blob = maybe_blob(root, c83, path)
    if path in C79_PATHS:
        owner = "C79/shared-registry-owner"
    elif path == SHARED_RUNNER:
        owner = "C61/C83-separate-hunks; C109 owns characterization only"
    elif path in diff_paths(root, base, c61):
        owner = "C61/work-task-34"
    else:
        owner = "C83/work-task-125"
    return {
        "path": path,
        "owner": owner,
        "base_commit": base_commit,
        "base_blob": base_blob,
        "c61_blob": c61_blob,
        "c83_blob": c83_blob,
        "changed_by_c61": c61_blob != base_blob,
        "changed_by_c83": c83_blob != base_blob,
    }


def shared_symbol_ledger(root: Path, base: str, c61: str, c83: str) -> dict[str, Any]:
    c61_patch = git_output(root, ["diff", "--unified=0", f"{base}...{c61}", "--", SHARED_RUNNER])
    c83_patch = git_output(root, ["diff", "--unified=0", f"{base}...{c83}", "--", SHARED_RUNNER])
    symbols = [
        {
            **symbol_locations(root, c61, "browserConversationTurnsForStep"),
            "owner": "C61/work-task-34",
            "effect": "deleted dead helper; absent from C61 commit",
        },
        {
            **symbol_locations(root, c61, "decodeBrowserConversationJSON"),
            "owner": "C61/work-task-34",
            "effect": "deleted dead helper; absent from C61 commit",
        },
        {
            **symbol_locations(root, c83, "browserConversationBroker.Invoke"),
            "owner": "C83/work-task-125",
            "effect": "records ordered wait-invocation observation",
        },
        {
            **symbol_locations(root, c83, "browserConversationBroker.Cancel"),
            "owner": "C83/work-task-125",
            "effect": "records cancellation observation and preserves cancellation ordering",
        },
        {
            **symbol_locations(root, c83, "record_cancellation"),
            "owner": "C83/work-task-125",
            "effect": "records cancellation evidence in the shared runner",
        },
    ]
    return {
        "path": SHARED_RUNNER,
        "owner": "C61/C83 separate hunk owners; no C109 source ownership",
        "base_blob": maybe_blob(root, git_output(root, ["merge-base", base, c61]), SHARED_RUNNER),
        "c61_blob": maybe_blob(root, c61, SHARED_RUNNER),
        "c83_blob": maybe_blob(root, c83, SHARED_RUNNER),
        "c61_patch_unified_zero": c61_patch,
        "c83_patch_unified_zero": c83_patch,
        "symbols": symbols,
        "api_relationship": {
            "c83_requires_c61_api": False,
            "reason": "C83 imports browserrunner and instruments the existing internal runner; it does not import the C61 public browserscenario package or any C61-only symbol.",
            "order_independent_mechanically": True,
            "delivery_order_required_procedurally": True,
            "delivery_order_reason": "C61 and C83 are separate admitted tasks and C79 owns the shared registry/baseline lease; merge-order evidence is characterization, not authorization to merge either candidate.",
        },
    }


def historical_c61_finding(root: Path, main: str, c61: str) -> dict[str, Any]:
    fix = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
    test_path = "go-agent-loop/pkg/agentloop/run_delta_barrier_test.go"
    production_path = "go-agent-loop/pkg/agentloop/agent_loop.go"
    fix_ancestor = git(root, ["merge-base", "--is-ancestor", fix, main], check=False)["exit_code"] == 0
    old_blob = maybe_blob(root, c61, test_path)
    main_blob = maybe_blob(root, main, test_path)
    fix_blob = maybe_blob(root, fix, test_path)
    test_symbol = line_matches(root, main, "TestRunJoinsPublishedDeltasBeforeReturningOnEngineError")
    production_diff = git_output(root, ["show", "--format=fuller", "--stat", "--oneline", fix, "--", production_path, test_path])
    if fix_ancestor and test_symbol and old_blob != main_blob:
        status = "OPEN_RECONCILE_TEST_REWRITTEN"
        conclusion = "The accepted main ancestry contains the production barrier repair, but the focused test blob is not unchanged from C61; the historical C61 finding remains open for task/review reconciliation and is not claimed fixed by C109."
    elif fix_ancestor and test_symbol:
        status = "REPAIRED_WITH_UNCHANGED_TEST"
        conclusion = "The accepted main ancestry contains the repair and the focused test is unchanged."
    else:
        status = "UNRESOLVED_MISSING_REPAIR_OR_SYMBOL"
        conclusion = "The required accepted-main repair or focused symbol evidence is absent."
    return {
        "historical_run": "34658619207",
        "historical_job": "103456263118",
        "historical_failure": "TestRunJoinsPublishedDeltasBeforeReturningOnEngineError lost a published delta after Run returned a terminal engine error.",
        "accepted_main_fix": fix,
        "fix_is_ancestor_of_review_main": fix_ancestor,
        "focused_test_path": test_path,
        "c61_test_blob": old_blob,
        "review_main_test_blob": main_blob,
        "fix_test_blob": fix_blob,
        "focused_test_symbol_evidence": test_symbol,
        "production_and_test_fix_stat": production_diff,
        "status": status,
        "conclusion": conclusion,
    }


def c83_ci_attribution() -> dict[str, Any]:
    return {
        "run": "34713381619",
        "passing_lanes": [
            "unit",
            "coverage",
            "race",
            "hermetic",
            "WebMCP Chrome",
            "macOS audio software",
            "Windows portable software",
        ],
        "static_findings": {
            "wire_registration": {
                "status": "C79_SHARED_REGISTRY_OWNER",
                "owner": "C79/work-task-114",
                "path": "scripts/wire-packages.txt",
                "claim": "browserrunner generated Wire package registration is required; C109 does not edit the shared registry.",
            },
            "stale_downward_baseline_entries": {
                "count": 9,
                "status": "C79_SHARED_BASELINE_OWNER",
                "owner": "C79/work-task-114",
                "path": "docs/architecture/architecture-size-baseline.json",
            },
            "intentional_instrumentation_drifts": {
                "count": 3,
                "status": "C83_OR_SHARED_RECONCILE",
                "owner": "C83/work-task-125 with C79 shared baseline reconciliation",
                "paths": [
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
                    SHARED_RUNNER,
                    SHARED_RUNNER + ":*browserConversationBroker.Invoke",
                ],
            },
        },
        "integration_loss": {
            "lost_samples": 6400,
            "total_samples": 174391,
            "drops": 0,
            "overflows": 0,
            "discards": 0,
            "owner": "C79/provider-audio",
            "status": "EXTERNAL_OWNER_DO_NOT_DUPLICATE_REPAIR_OR_RELABEL",
        },
    }


def bounded_sequence() -> dict[str, Any]:
    return {
        "status": "bounded-and-procedural",
        "steps": [
            {
                "number": 1,
                "owner": "C79/work-task-114",
                "action": "Resolve shared scripts/wire-packages.txt registration, architecture-size-baseline.json stale entries, and the provider-audio integration-loss ownership under the existing C79 lease.",
                "gate": "C79 evidence/review/CI decision; C109 does not edit or relabel these findings.",
            },
            {
                "number": 2,
                "owner": "C61/work-task-34",
                "action": "Return the same C61 task on its existing PR #452, reconcile the historical hermetic delta-barrier finding against accepted main 3d3e7278 and the rewritten focused test, then run its own task/review/CI gates.",
                "gate": "independent review and script CI; no C109 acceptance claim",
            },
            {
                "number": 3,
                "owner": "C83/work-task-125",
                "action": "After C79 shared resolution and C61 dependency disposition, return the same C83 task on PR #473, reconcile Wire/static findings and run its own task/review/CI gates.",
                "gate": "independent review and script CI; no C109 acceptance claim",
            },
            {
                "number": 4,
                "owner": "C109/work-task-224",
                "action": "Keep this immutable analyzer/report/source/fixture evidence available for review-time main integration and later fresh-scope vertical-probe staging.",
                "gate": "C109 handoff only; no C61/C83/project acceptance or hardware/acoustic claim",
            },
        ],
        "prohibited": [
            "merge either candidate from C109",
            "repair candidate source or shared C79 files",
            "claim C61/C83/project acceptance",
            "claim hardware, microphone, acoustic, or vertical-probe coverage",
            "relabel the 6400-sample provider-audio loss",
        ],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--main", default=ACCEPTED_MAIN)
    parser.add_argument("--c61", default=C61)
    parser.add_argument("--c83", default=C83)
    parser.add_argument("--order", choices=("c61-c83", "c83-c61"), required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--expect-control", action="store_true")
    args = parser.parse_args()
    root = Path(git_output(Path.cwd(), ["rev-parse", "--show-toplevel"])).resolve()
    output_dir = args.output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    if args.main != ACCEPTED_MAIN:
        raise EvidenceError(f"accepted main input must be {ACCEPTED_MAIN}, got {args.main}")
    if args.c61 != C61 or args.c83 != C83:
        raise EvidenceError("C61/C83 inputs must match the admitted preserved revisions")
    if args.expect_control and args.order != "c83-c61":
        raise EvidenceError("--expect-control is valid only for the reverse c83-c61 rehearsal")

    before_refs = show_refs(root)
    before_host_status = status_lines(root)
    before_preserved = preserved_snapshot(root)
    fetch = run(["git", "fetch", "origin", "main"], cwd=root, check=False, timeout=120)
    fetched_main = revision(root, "origin/main")
    requested_main = revision(root, args.main)
    if requested_main != ACCEPTED_MAIN:
        raise EvidenceError("the admitted accepted-main revision cannot be resolved to its exact SHA")
    if git(root, ["merge-base", "--is-ancestor", requested_main, fetched_main], check=False)["exit_code"] != 0:
        raise EvidenceError("origin/main is not descended from the admitted accepted main")
    review_main = fetched_main
    newer_main = review_main != requested_main
    if revision(root, args.c61) != C61 or revision(root, args.c83) != C83:
        raise EvidenceError("candidate refs changed from the admitted exact SHAs")

    c61_paths = diff_paths(root, review_main, args.c61)
    c83_paths = diff_paths(root, review_main, args.c83)
    union_paths = sorted(set(c61_paths) | set(c83_paths))
    shared_paths = sorted(set(c61_paths) & set(c83_paths))
    if shared_paths != [SHARED_RUNNER]:
        raise EvidenceError(f"unexpected candidate path intersection: {shared_paths}")
    unowned_candidate_paths = sorted(path for path in union_paths if path.startswith(OWNED_PREFIX))

    required = args.order == "c61-c83"
    order = [("c61", args.c61), ("c83", args.c83)] if required else [("c83", args.c83), ("c61", args.c61)]
    rehearsal = commit_rehearsal(root, review_main, order, "required" if required else "control")

    after_refs = show_refs(root)
    after_host_status = status_lines(root)
    after_preserved = preserved_snapshot(root)
    preserved_unchanged = before_preserved == after_preserved
    refs_unchanged = before_refs == after_refs
    allowed_host_changes = sorted(
        set(before_host_status) ^ set(after_host_status)
    )
    host_scope_clean = not allowed_host_changes or all(OWNED_PREFIX in line for line in allowed_host_changes)

    files = [blob_record(root, review_main, args.c61, args.c83, path) for path in union_paths]
    files.append(shared_symbol_ledger(root, review_main, args.c61, args.c83))
    provenance = {
        "project": PROJECT,
        "task": TASK,
        "contractRevision": CONTRACT,
        "factorySession": "~default",
        "factoryServer": os.environ.get("FACTORY_SERVER_URL", "$FACTORY_SERVER_URL"),
        "startupIntegrationRevision": STARTUP_INTEGRATION_REVISION,
        "requestedAcceptedMain": requested_main,
        "fetchedOriginMain": fetched_main,
        "reviewMain": review_main,
        "newerMainFetchedAndIntegrated": newer_main,
        "candidates": {
            "c61": candidate_ref_snapshot(root, args.c61, "C61/work-task-34"),
            "c83": candidate_ref_snapshot(root, args.c83, "C83/work-task-125"),
        },
        "candidateTrees": {
            "c61": tree(root, args.c61),
            "c83": tree(root, args.c83),
            "reviewMain": tree(root, review_main),
        },
        "fetch": fetch,
        "worktreeInventory": parse_worktrees(root),
        "preservedWorktrees": {
            "before": before_preserved,
            "after": after_preserved,
            "unchanged": preserved_unchanged,
        },
        "hostCheckout": {
            "beforeStatus": before_host_status,
            "afterStatus": after_host_status,
            "changedStatusLines": allowed_host_changes,
            "scopeClean": host_scope_clean,
            "branch": git_output(root, ["branch", "--show-current"]),
            "head": revision(root, "HEAD"),
        },
        "refsUnchanged": refs_unchanged,
    }
    ledger = {
        "base": {
            "reviewMain": review_main,
            "candidateIntersection": shared_paths,
            "candidateUnionCount": len(union_paths),
        },
        "files": files,
        "apiConclusion": files[-1]["api_relationship"],
        "unownedCandidatePaths": unowned_candidate_paths,
        "failClosedRules": [
            "exact admitted SHAs are required",
            "both merge orders are required",
            "candidate refs and preserved worktrees must remain unchanged",
            "C109 owned paths are evidence-only",
            "unsupported acceptance/fix/probe claims are rejected",
        ],
    }
    report = {
        "schema": "audio-runtime-c109-v1",
        "project": PROJECT,
        "task": TASK,
        "contractRevision": CONTRACT,
        "role": "required" if required else "control",
        "provenanceFile": "provenance.json",
        "ledgerFile": "ledger.json",
        "mergeOrderFile": "merge-orders.json",
        "sequenceFile": "sequence.json",
        "provenance": provenance,
        "ledger": ledger,
        "rehearsal": rehearsal,
        "historicalC61Finding": historical_c61_finding(root, review_main, args.c61),
        "c83CIAttribution": c83_ci_attribution(),
        "sequence": bounded_sequence(),
        "deliveryClaims": {
            "C61": {"merged": False, "fixed": False, "probed": False, "accepted": False},
            "C83": {"merged": False, "fixed": False, "probed": False, "accepted": False},
            "project": {"accepted": False, "verticalProbe": False},
        },
        "broadGates": {
            "AUDIO": "OPEN",
            "BROWSER": "OPEN",
            "LIFECYCLE": "OPEN",
            "OBSERVABILITY": "OPEN",
            "SAFETY": "OPEN",
            "PARITY": "OPEN",
        },
        "claims": {
            "mechanicalMergeCompatibility": rehearsal["merges"][-1]["exit_code"] == 0 if rehearsal["merges"] else False,
            "candidateDeliveryOrder": "not-authorized",
            "softwareEvidenceOnly": True,
            "hardwareOrAcousticEvidence": False,
            "verticalProbe": False,
        },
    }
    write_json(output_dir / "provenance.json", provenance)
    write_json(output_dir / "ledger.json", ledger)
    write_json(output_dir / "merge-orders.json", rehearsal)
    write_json(output_dir / "sequence.json", bounded_sequence())
    write_json(output_dir / "report.json", report)
    write_json(output_dir / "run-manifest.json", {
        "commands": {
            "required": "analyze.py --order c61-c83",
            "control": "analyze.py --order c83-c61 --expect-control",
            "verification": "verify.py --mode all",
        },
        "outputSha256": {name: sha256_file(output_dir / name) for name in sorted(path.name for path in output_dir.glob("*.json"))},
    })
    print(json.dumps({
        "status": "passed" if report["claims"]["mechanicalMergeCompatibility"] and refs_unchanged and preserved_unchanged and host_scope_clean else "failed",
        "role": report["role"],
        "reviewMain": review_main,
        "order": report["rehearsal"]["order"],
        "outputDir": str(output_dir),
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceError as exc:
        print(f"C109 analyzer failed closed: {exc}", file=sys.stderr)
        raise SystemExit(2)
