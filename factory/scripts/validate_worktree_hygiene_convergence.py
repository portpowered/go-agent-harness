#!/usr/bin/env python3
"""Generate a reviewer-facing convergence report for factory worktree hygiene."""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


REPAIRED_SLICE = "phase-2-factory-worktree-hygiene-repair"
VALIDATOR_BRANCH = "phase-2-factory-worktree-hygiene-validator"
DEFAULT_REPORT_PATH = "docs/internal/phase-2-factory-worktree-hygiene-convergence-report.md"
CTRL_FAC_ROWS = ("CTRL-FAC-01", "CTRL-FAC-02", "CTRL-FAC-03")
SETUP_TEST_MODULE = "factory/scripts/tests/test_setup_workspace.py"


@dataclass
class CommandResult:
    args: list[str]
    returncode: int
    stdout: str
    stderr: str


@dataclass
class Finding:
    group: str
    subject: str
    outcome: str
    evidence: str
    affected_surfaces: str
    required_follow_up: str


@dataclass
class WorkItem:
    work_id: str
    name: str
    work_type: str
    state_name: str
    state_type: str
    relations: str


QUEUE_RELEVANT_WORK_TYPES = {"plan", "task", "thoughts"}


def run_command(args: list[str], cwd: Path) -> CommandResult:
    result = subprocess.run(
        args,
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    return CommandResult(
        args=args,
        returncode=result.returncode,
        stdout=result.stdout,
        stderr=result.stderr,
    )


def repo_root() -> Path:
    result = run_command(["git", "rev-parse", "--show-toplevel"], Path.cwd())
    if result.returncode != 0:
        raise RuntimeError(f"failed to discover repo root: {result.stderr.strip()}")
    return Path(result.stdout.strip())


def project_root_from_repo_root(root: Path) -> Path:
    marker = f"{Path('.claude') / 'worktrees'}"
    root_text = root.as_posix()
    if marker in root_text:
        prefix, _, _ = root_text.partition(marker)
        return Path(prefix.rstrip("/"))
    return root


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def has_git_subcommand(script_text: str, subcommand: str) -> bool:
    return re.search(rf'["\']{re.escape(subcommand)}["\']', script_text) is not None


def find_session_id(session_list_output: str, project_root: Path) -> str | None:
    rows = parse_tabular_output(session_list_output)
    for row in rows:
        if row.get("FOLDER PATH") == project_root.as_posix():
            return row.get("SESSION ID")
    return None


def parse_tabular_output(output: str) -> list[dict[str, str]]:
    lines = [line for line in output.splitlines() if line.strip()]
    if len(lines) < 2:
        return []
    headers = lines[0].split("\t")
    rows = []
    for line in lines[1:]:
        parts = line.split("\t")
        if len(parts) < len(headers):
            parts.extend([""] * (len(headers) - len(parts)))
        rows.append({header: parts[index] for index, header in enumerate(headers)})
    return rows


def parse_work_items(output: str) -> list[WorkItem]:
    return [
        WorkItem(
            work_id=row["WORK ID"],
            name=row["NAME"],
            work_type=row["WORK TYPE"],
            state_name=row["STATE NAME"],
            state_type=row["STATE TYPE"],
            relations=row["RELATIONS"],
        )
        for row in parse_tabular_output(output)
    ]


def is_direct_repaired_slice_item(item: WorkItem) -> bool:
    return item.name == REPAIRED_SLICE or item.work_id.endswith(f"-{REPAIRED_SLICE}")


def is_related_repaired_slice_item(item: WorkItem) -> bool:
    return REPAIRED_SLICE in item.relations and not is_direct_repaired_slice_item(item)


def recommended_recovery_action(session_id: str, item: WorkItem) -> str:
    if item.work_type == "thoughts":
        return (
            f"if `{item.work_id}` is idle and should rerun the ideafy loop, run "
            f"`you work move --session {session_id} {item.work_id} init`"
            if item.state_name != "init"
            else (
                f"`{item.work_id}` is already at `thoughts:init`; confirm it is not in an active dispatch before "
                "attempting any manual repair"
            )
        )

    if item.work_type == "plan":
        return (
            f"if `{item.work_id}` is idle and needs setup-workspace retried, run "
            f"`you work move --session {session_id} {item.work_id} init`"
            if item.state_name != "init"
            else (
                f"`{item.work_id}` is already at `plan:init`; confirm it is not in an active dispatch before "
                "attempting any manual repair"
            )
        )

    if item.work_type == "task":
        if item.state_name in {"failed", "in-review", "to-complete"}:
            return (
                f"if `{item.work_id}` is idle and should re-enter executor processing, run "
                f"`you work move --session {session_id} {item.work_id} init`"
            )
        return (
            f"`{item.work_id}` is already at `task:init`; confirm it is not in an active dispatch before "
            "attempting any manual repair"
        )

    return f"inspect `{item.work_id}` manually; no validator recovery rule exists for `{item.work_type}`"


def collect_setup_runtime_evidence(root: Path) -> CommandResult:
    return run_command(
        ["python3", "-B", "-m", "unittest", "-v", SETUP_TEST_MODULE],
        root,
    )


def evaluate_checklist_rows(
    checklist_text: str,
    overview_text: str,
    setup_script_text: str,
    setup_tests_result: CommandResult,
) -> list[Finding]:
    findings: list[Finding] = []
    tests_passed = setup_tests_result.returncode == 0
    test_output = combined_output(setup_tests_result)

    has_pull = has_git_subcommand(setup_script_text, "pull")
    has_prune = re.search(r'["\']worktree["\']\s*,\s*["\']prune["\']', setup_script_text) is not None
    allows_checklist = "docs/internal/checklist.md" in setup_script_text
    allows_progress = "docs/internal/progress.txt" in setup_script_text

    ctrl_fac_01_ok = (
        "CTRL-FAC-01" in checklist_text
        and "`git pull`" in overview_text
        and "does not own" in overview_text
        and not has_pull
        and tests_passed
        and "test_setup_workspace_skips_root_pull_and_prune" in test_output
    )
    findings.append(
        Finding(
            group="checklist-convergence",
            subject="CTRL-FAC-01 root sync ownership stays outside `setup-workspace`",
            outcome="pass" if ctrl_fac_01_ok else "fail",
            evidence=(
                "`docs/internal/checklist.md` includes `CTRL-FAC-01`; "
                "`factory/docs/overview.md` states setup does not own root-checkout `git pull`; "
                "`factory/scripts/setup-workspace.py` contains no `pull` git subcommand; "
                "the current validator run executed "
                "`python3 -B -m unittest -v factory/scripts/tests/test_setup_workspace.py` "
                f"with exit code `{setup_tests_result.returncode}` and included "
                "`test_setup_workspace_skips_root_pull_and_prune`."
            ),
            affected_surfaces=(
                "`docs/internal/checklist.md`, `factory/docs/overview.md`, "
                "`factory/scripts/setup-workspace.py`, "
                "`factory/scripts/tests/test_setup_workspace.py`"
            ),
            required_follow_up="none" if ctrl_fac_01_ok else "restore the documented no-`git pull` setup contract and passing runtime proof",
        )
    )

    ctrl_fac_02_ok = (
        "CTRL-FAC-02" in checklist_text
        and "`git worktree prune`" in overview_text
        and "does not own" in overview_text
        and not has_prune
        and tests_passed
        and "test_setup_workspace_skips_root_pull_and_prune" in test_output
    )
    findings.append(
        Finding(
            group="checklist-convergence",
            subject="CTRL-FAC-02 worktree maintenance ownership stays outside `setup-workspace`",
            outcome="pass" if ctrl_fac_02_ok else "fail",
            evidence=(
                "`docs/internal/checklist.md` includes `CTRL-FAC-02`; "
                "`factory/docs/overview.md` states setup does not own root `git worktree prune`; "
                "`factory/scripts/setup-workspace.py` contains no `worktree prune` git subcommand; "
                "the current validator run included "
                "`test_setup_workspace_skips_root_pull_and_prune` with the setup runtime suite."
            ),
            affected_surfaces=(
                "`docs/internal/checklist.md`, `factory/docs/overview.md`, "
                "`factory/scripts/setup-workspace.py`, "
                "`factory/scripts/tests/test_setup_workspace.py`"
            ),
            required_follow_up="none" if ctrl_fac_02_ok else "remove root `git worktree prune` ownership from setup and restore deterministic proof",
        )
    )

    ctrl_fac_03_ok = (
        "CTRL-FAC-03" in checklist_text
        and "docs/internal/checklist.md" in overview_text
        and "docs/internal/progress.txt" in overview_text
        and "tolerated" in overview_text
        and allows_checklist
        and allows_progress
        and tests_passed
        and "test_setup_workspace_allows_planner_owned_dirty_root_files" in test_output
        and "test_setup_workspace_reuses_existing_worktree_with_planner_owned_dirty_root_files" in test_output
        and "test_setup_workspace_fails_for_non_planner_owned_dirty_root_state" in test_output
    )
    findings.append(
        Finding(
            group="checklist-convergence",
            subject="CTRL-FAC-03 planner-owned dirty root tolerance remains explicit and bounded",
            outcome="pass" if ctrl_fac_03_ok else "fail",
            evidence=(
                "`docs/internal/checklist.md` includes `CTRL-FAC-03`; "
                "`factory/docs/overview.md` documents tolerated planner-owned dirty paths; "
                "`factory/scripts/setup-workspace.py` allows `docs/internal/checklist.md` and "
                "`docs/internal/progress.txt`; the current validator run included "
                "`test_setup_workspace_allows_planner_owned_dirty_root_files`, "
                "`test_setup_workspace_reuses_existing_worktree_with_planner_owned_dirty_root_files`, "
                "and `test_setup_workspace_fails_for_non_planner_owned_dirty_root_state`."
            ),
            affected_surfaces=(
                "`docs/internal/checklist.md`, `factory/docs/overview.md`, "
                "`factory/scripts/setup-workspace.py`, "
                "`factory/scripts/tests/test_setup_workspace.py`"
            ),
            required_follow_up="none" if ctrl_fac_03_ok else "restore bounded planner-owned dirty-root tolerance and explicit unsafe-state failure coverage",
        )
    )
    return findings


def evaluate_setup_behavior(setup_tests_result: CommandResult) -> Finding:
    tests_passed = setup_tests_result.returncode == 0
    test_output = combined_output(setup_tests_result)
    race_test_present = (
        "test_setup_workspace_recovers_when_parallel_runs_race_on_worktree_creation"
        in test_output
    )
    outcome = "pass" if tests_passed and race_test_present else "fail"
    return Finding(
        group="setup-workspace-behavior",
        subject="Previously reproduced concurrent setup race for the same PRD branch",
        outcome=outcome,
        evidence=(
            "The validator directly exercised the repaired setup path by running "
            "`python3 -B -m unittest -v factory/scripts/tests/test_setup_workspace.py`. "
            f"That run exited `{setup_tests_result.returncode}` and included "
            "`test_setup_workspace_recovers_when_parallel_runs_race_on_worktree_creation`, "
            "which deterministically launches overlapping setup runs for "
            f"`{REPAIRED_SLICE}` and asserts both return `status=ready` against the same worktree."
        ),
        affected_surfaces=(
            "`factory/scripts/setup-workspace.py`, "
            "`factory/scripts/tests/test_setup_workspace.py`, "
            f"requested branch `{REPAIRED_SLICE}`"
        ),
        required_follow_up="none" if outcome == "pass" else "repair the concurrent setup reuse path until the deterministic race test passes again",
    )


def evaluate_queue_recovery(
    project_root: Path,
    session_list_result: CommandResult,
    work_list_result: CommandResult | None,
    session_id: str | None,
) -> Finding:
    if session_list_result.returncode != 0:
        return Finding(
            group="durable-queue-recovery",
            subject="Live durable queue inspection for stranded plan, task, or thoughts work",
            outcome="uncertain",
            evidence=f"`you session list` failed with exit code `{session_list_result.returncode}`: {combined_output(session_list_result).strip()}",
            affected_surfaces="`you session list`",
            required_follow_up="rerun the validator where `you` session inspection is available",
        )

    if session_id is None:
        return Finding(
            group="durable-queue-recovery",
            subject="Live durable queue inspection for stranded plan, task, or thoughts work",
            outcome="uncertain",
            evidence=(
                "The validator could not match the current project root "
                f"`{project_root}` to a row in `you session list`."
            ),
            affected_surfaces="`you session list`",
            required_follow_up="provide or restore the live project session so queue state can be inspected",
        )

    if work_list_result is None or work_list_result.returncode != 0:
        detail = "missing work list result"
        if work_list_result is not None:
            detail = f"`you work list --session {session_id}` exited `{work_list_result.returncode}`: {combined_output(work_list_result).strip()}"
        return Finding(
            group="durable-queue-recovery",
            subject="Live durable queue inspection for stranded plan, task, or thoughts work",
            outcome="uncertain",
            evidence=detail,
            affected_surfaces=f"`you work list --session {session_id}`",
            required_follow_up="rerun the validator with queue access to determine whether manual recovery is still required",
        )

    work_items = parse_work_items(work_list_result.stdout)
    direct_repair_items = [
        item for item in work_items if is_direct_repaired_slice_item(item)
    ]
    related_repair_items = [
        item for item in work_items if is_related_repaired_slice_item(item)
    ]
    terminal_repair_items = [
        item
        for item in direct_repair_items
        if item.work_type in {"idea", "plan", "task"} and item.state_type == "TERMINAL"
    ]
    non_terminal_repair_items = [
        item
        for item in direct_repair_items
        if item.work_type in QUEUE_RELEVANT_WORK_TYPES and item.state_type != "TERMINAL"
    ]
    outcome = "pass" if terminal_repair_items and not non_terminal_repair_items else "uncertain"

    evidence_lines = [
        f"`you session list` matched project root `{project_root}` to session `{session_id}`.",
        f"`you work list --session {session_id}` returned `{len(work_items)}` rows.",
    ]
    if terminal_repair_items:
        evidence_lines.append(
            "Terminal repaired-slice items observed: "
            + "; ".join(
                f"`{item.name}` ({item.work_type}/{item.state_name})"
                for item in terminal_repair_items
            )
            + "."
        )
    if related_repair_items:
        evidence_lines.append(
            "Related non-slice items that still reference the repaired slice were observed but not classified as repaired-slice queue damage: "
            + "; ".join(
                f"`{item.work_id}` ({item.work_type}/{item.state_name})"
                for item in related_repair_items
            )
            + "."
        )
    if non_terminal_repair_items:
        evidence_lines.append(
            "Non-terminal repaired-slice queue items requiring operator review: "
            + "; ".join(
                f"`{item.work_id}` ({item.work_type}/{item.state_name}/{item.state_type})"
                for item in non_terminal_repair_items
            )
            + "."
        )
    else:
        evidence_lines.append(
            "All repaired-slice `plan`, `task`, and `thoughts` items are terminal; no manual recovery remains."
        )

    if outcome == "pass":
        follow_up = "no manual `you work move` recovery is required for the repaired slice"
    else:
        follow_up = "; ".join(
            f"`{item.work_id}`: {recommended_recovery_action(session_id, item)}"
            for item in non_terminal_repair_items
        )
    return Finding(
        group="durable-queue-recovery",
        subject="Live durable queue inspection for stranded plan, task, or thoughts work",
        outcome=outcome,
        evidence=" ".join(evidence_lines),
        affected_surfaces=f"`you session list`, `you work list --session {session_id}`",
        required_follow_up=follow_up,
    )


def combined_output(result: CommandResult) -> str:
    parts = []
    if result.stdout:
        parts.append(result.stdout.strip())
    if result.stderr:
        parts.append(result.stderr.strip())
    return "\n".join(parts)


def overall_verdict(findings: list[Finding]) -> str:
    outcomes = {finding.outcome for finding in findings}
    if "fail" in outcomes:
        return "fail"
    if "uncertain" in outcomes:
        return "uncertain"
    return "pass"


def render_findings_table(findings: list[Finding]) -> str:
    lines = [
        "| group | subject | outcome | evidence | affectedFilesOrSurfaces | requiredFollowUp |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for finding in findings:
        lines.append(
            "| "
            + " | ".join(
                [
                    f"`{finding.group}`",
                    finding.subject,
                    f"`{finding.outcome}`",
                    finding.evidence,
                    finding.affected_surfaces,
                    finding.required_follow_up,
                ]
            )
            + " |"
        )
    return "\n".join(lines)


def render_report(generated_at: str, session_id: str | None, findings: list[Finding]) -> str:
    checklist_findings = [finding for finding in findings if finding.group == "checklist-convergence"]
    setup_findings = [finding for finding in findings if finding.group == "setup-workspace-behavior"]
    queue_findings = [finding for finding in findings if finding.group == "durable-queue-recovery"]
    verdict = overall_verdict(findings)
    session_line = f"- Live project session inspected: `{session_id}`" if session_id else "- Live project session inspected: unavailable"
    return f"""# Phase 2 Factory Worktree Hygiene Convergence Report

_Generated by `python3 factory/scripts/validate_worktree_hygiene_convergence.py --write-report {DEFAULT_REPORT_PATH}` on {generated_at}._

## Subject Under Review

- Validator branch: `{VALIDATOR_BRANCH}`
- Repaired branch under review: `{REPAIRED_SLICE}`
{session_line}

This report is generated from the current repository state and validator run. It
stays scoped to checklist convergence, repaired `setup-workspace` behavior, and
live durable-queue recovery status.

## Checklist Convergence Findings

{render_findings_table(checklist_findings)}

## Repaired Setup-Workspace Behavior Findings

{render_findings_table(setup_findings)}

## Durable-Queue Recovery Status

{render_findings_table(queue_findings)}

## Final Overall Convergence Verdict

Final overall verdict for Phase 2 factory worktree hygiene convergence:
`{verdict}`.
"""


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--write-report",
        default=DEFAULT_REPORT_PATH,
        help="Path to write the generated markdown report.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root = repo_root()
    project_root = project_root_from_repo_root(root)
    generated_at = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%SZ")

    checklist_text = read_text(root / "docs" / "internal" / "checklist.md")
    overview_text = read_text(root / "factory" / "docs" / "overview.md")
    setup_script_text = read_text(root / "factory" / "scripts" / "setup-workspace.py")

    setup_tests_result = collect_setup_runtime_evidence(root)
    session_list_result = run_command(["you", "session", "list"], root)
    session_id = find_session_id(session_list_result.stdout, project_root)
    work_list_result = None
    if session_id is not None:
        work_list_result = run_command(["you", "work", "list", "--session", session_id], root)

    findings = []
    findings.extend(
        evaluate_checklist_rows(
            checklist_text=checklist_text,
            overview_text=overview_text,
            setup_script_text=setup_script_text,
            setup_tests_result=setup_tests_result,
        )
    )
    findings.append(evaluate_setup_behavior(setup_tests_result))
    findings.append(
        evaluate_queue_recovery(
            project_root=project_root,
            session_list_result=session_list_result,
            work_list_result=work_list_result,
            session_id=session_id,
        )
    )

    report = render_report(generated_at=generated_at, session_id=session_id, findings=findings)
    report_path = root / args.write_report
    report_path.write_text(report, encoding="utf-8")
    sys.stdout.write(report)
    return 0 if overall_verdict(findings) == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
