#!/usr/bin/env python3
"""Admission and completion checks at the factory's public work boundaries."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

import project_admission
import project_scope_amendment
from project_contract import ContractError, artifact, check_packet, manifest, read_json, root_path, task_packet, work_name


def runtime_record(root):
    common = Path(subprocess.check_output(["git", "-C", str(root), "rev-parse",
                  "--path-format=absolute", "--git-common-dir"], text=True).strip())
    path = common / "factory-runtime.json"
    return read_json(path) if path.exists() else None


def owner(root, contract):
    record = project_admission.status(root)
    if record is None:
        raise ContractError("no project admitted")
    check_packet(record, contract)
    return record


def verify_work(root, kind, name, payload):
    contract = manifest(root)
    owner(root, contract)
    work_name(name)
    if kind == "project":
        if name != contract["project"]:
            raise ContractError("only the admitted project may execute")
    elif not name.startswith(contract["project"] + "-c"):
        raise ContractError("child work must use the admitted project cycle prefix")
    if kind == "task":
        packet = task_packet(root, name, contract)
    else:
        # Public Work moves / restored tokens can omit the old input payload.
        # Project identity is already uniquely fixed by admission and its name.
        if kind == "project" and not payload.strip():
            packet = {"project":contract["project"], "contractRevision":contract["contractRevision"]}
        else:
            packet = json.loads(payload)
        check_packet(packet, contract)
    return {"status": "admitted", "project": contract["project"], "name": name}


def _staged_work_output(work):
    """Decode the bounded prepare-validation result retained by canonical Work."""
    candidates = []
    tags = work.get("tags")
    if isinstance(tags, dict) and isinstance(tags.get("_last_output"), str):
        candidates.append(tags["_last_output"])
    content = work.get("content")
    if isinstance(content, list):
        for item in content:
            if isinstance(item, dict) and isinstance(item.get("text"), str):
                candidates.append(item["text"])
    for candidate in candidates:
        if len(candidate.encode("utf-8")) > 2 * 1024 * 1024:
            raise ContractError("validation Work output exceeds the bounded response limit")
        try:
            value = json.loads(candidate)
        except (TypeError, json.JSONDecodeError):
            continue
        if isinstance(value, dict) and "missionSha256" in value:
            return value
    return None


def completed_validation(
    work_id,
    session_id,
    server,
    *,
    project=None,
    work_name=None,
    mission_path=None,
    mission_sha256=None,
    artifact_sha256=None,
):
    result = subprocess.run(["you", "--server", server, "--json", "work", "show",
                             work_id, "--session", session_id], capture_output=True,
                            text=True, timeout=60, check=True)
    work = json.loads(result.stdout)
    # The public show response may wrap the returned Work.
    if isinstance(work, dict) and isinstance(work.get("work"), dict):
        work = work["work"]
    state = work.get("state", {})
    if isinstance(state, dict):
        state = state.get("name")
    kind = work.get("workTypeName") or work.get("workType")
    if kind != "validation" or state != "complete":
        raise ContractError("validation Work has not completed in canonical runtime state")
    if work_name is None:
        return
    if work.get("workId") != work_id:
        raise ContractError("canonical validation Work identity mismatch")
    if work.get("name") != work_name:
        raise ContractError("canonical validation Work name mismatch")
    if not isinstance(project, str) or not work_name.startswith(project + "-c"):
        raise ContractError("canonical validation Work project mismatch")
    tags = work.get("tags")
    if isinstance(tags, dict) and tags.get("_work_name") not in {None, work_name}:
        raise ContractError("canonical validation Work tag name mismatch")
    staged = _staged_work_output(work)
    if staged is None:
        raise ContractError("canonical validation Work lacks staged mission identity")
    if staged.get("validationWorkName") != work_name:
        raise ContractError("canonical validation Work mission name mismatch")
    if staged.get("project") not in {None, project}:
        raise ContractError("canonical validation Work mission project mismatch")
    if staged.get("missionSha256") != mission_sha256:
        raise ContractError("canonical validation Work mission digest mismatch")
    if not isinstance(mission_path, str) or not isinstance(staged.get("directory"), str):
        raise ContractError("canonical validation Work mission path is missing")
    if Path(staged["directory"]).resolve() != Path(mission_path).resolve().parent:
        raise ContractError("canonical validation Work staged directory mismatch")
    staged_build = staged.get("build")
    if (
        not isinstance(staged_build, dict)
        or staged_build.get("sha256") != artifact_sha256
    ):
        raise ContractError("canonical validation Work artifact mismatch")


def validate_report(report, contract, role, build, expected, *, root=None, amendment=None, report_path=None):
    """Validate one final project-scope report without trusting its claims."""
    check_packet(report, contract)
    # A missing scope is the legacy final-report shape.  Explicit vertical
    # reports must never satisfy the whole-project completion gate, even when a
    # worker accidentally reports every criterion.
    if amendment is None and report.get("scope", "project") != "project":
        raise ContractError("project completion requires scope=project validation reports")
    report_build = report.get("build")
    if (
        report.get("role") != role
        or not isinstance(report_build, dict)
        or report_build.get("sha256") != build["sha256"]
    ):
        raise ContractError("validation role/artifact mismatch")
    if amendment is not None:
        if root is None or report_path is None:
            raise ContractError("amended report validation is missing its repository path")
        try:
            project_scope_amendment.validate_amended_report(
                root,
                report,
                role=role,
                build=build,
                expected_criteria=expected,
                amendment=amendment,
                report_path=report_path,
            )
        except project_scope_amendment.ScopeAmendmentError as error:
            raise ContractError(str(error)) from error
        return
    criteria = report.get("criteria", {})
    if not isinstance(criteria, dict) or set(criteria) != expected or any(
        not isinstance(value, dict) or value.get("verdict") != "PASS" or
        not str(value.get("evidence", "")).strip() for value in criteria.values()
    ):
        raise ContractError("all immutable criteria need independent PASS evidence")


def verify_completion(root, name):
    contract = manifest(root)
    owner(root, contract)
    if name != contract["project"]:
        raise ContractError("completion is for a different project")
    path = root / "docs/temp/projects" / name / "completion.json"
    record = read_json(path)
    check_packet(record, contract)
    expected = {entry["id"] for entry in contract["criteria"]}
    build = artifact(record.get("build"))
    amendment = None
    if "amendment" in record:
        try:
            amendment = project_scope_amendment.amendment_reference(
                root,
                record["amendment"],
            )
        except project_scope_amendment.ScopeAmendmentError as error:
            raise ContractError(str(error)) from error
    runtime = runtime_record(root)
    if not runtime or runtime.get("project") != name:
        raise ContractError("runtime identity is missing")
    seen = set()
    for role in ("customer", "engineering"):
        report_path = Path(record.get("reports", {}).get(role, "")).resolve()
        if not report_path.is_relative_to(root / "docs/temp/projects" / name):
            raise ContractError("report is outside the admitted project")
        report = read_json(report_path)
        validate_report(
            report,
            contract,
            role,
            build,
            expected,
            root=root,
            amendment=amendment,
            report_path=report_path,
        )
        work_id = report.get("validationWorkId")
        if not isinstance(work_id, str) or not work_id or work_id in seen:
            raise ContractError("validation must use distinct canonical Work identities")
        if amendment is None:
            completed_validation(work_id, runtime["sessionId"], runtime["server"])
        else:
            completed_validation(
                work_id,
                runtime["sessionId"],
                runtime["server"],
                project=name,
                work_name=report["validationWorkName"],
                mission_path=report["missionPath"],
                mission_sha256=report["missionSha256"],
                artifact_sha256=build["sha256"],
            )
        seen.add(work_id)
    return {"status": "verified", "project": name, "build": build}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "operation",
        choices=[
            "status",
            "verify-work",
            "verify-completion",
            "amendment-append",
            "amendment-status",
            "scope-amendment-append",
            "scope-amendment-status",
            "append-amendment",
            "status-amendment",
            "amendment",
        ],
    )
    parser.add_argument("action", nargs="?", choices=["append", "status"])
    parser.add_argument("--type", choices=["project", "idea", "task"])
    parser.add_argument("--name")
    parser.add_argument("--payload", default="{}")
    parser.add_argument("--record")
    parser.add_argument("--amendment-id")
    parser.add_argument("--root")
    args = parser.parse_args()
    root = Path(args.root).resolve() if args.root else root_path()
    operation = args.operation
    if operation == "amendment":
        if args.action == "append":
            operation = "amendment-append"
        elif args.action == "status":
            operation = "amendment-status"
        else:
            parser.error("amendment requires append or status")
    if operation in {
        "amendment-append",
        "scope-amendment-append",
        "append-amendment",
    }:
        if not args.record:
            parser.error("amendment append requires --record")
        contract = manifest(root)
        owner(root, contract)
        result = project_scope_amendment.append_record(root, args.record)
    elif operation in {
        "amendment-status",
        "scope-amendment-status",
        "status-amendment",
    }:
        contract = manifest(root)
        owner(root, contract)
        result = project_scope_amendment.list_status(root, args.amendment_id)
    elif operation == "status":
        result = {"manifest": manifest(root), "admission": project_admission.status(root),
                  "runtime": runtime_record(root)}
    elif args.operation == "verify-work":
        if not args.type or not args.name:
            parser.error("verify-work requires --type and --name")
        result = verify_work(root, args.type, args.name, args.payload)
    else:
        if not args.name:
            parser.error("verify-completion requires --name")
        result = verify_completion(root, args.name)
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
