#!/usr/bin/env python3
"""Admission and completion checks at the factory's public work boundaries."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

import project_admission
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
        packet = json.loads(payload)
        check_packet(packet, contract)
    return {"status": "admitted", "project": contract["project"], "name": name}


def completed_validation(work_id, session_id, server):
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
    runtime = runtime_record(root)
    if not runtime or runtime.get("project") != name:
        raise ContractError("runtime identity is missing")
    seen = set()
    for role in ("customer", "engineering"):
        report_path = Path(record.get("reports", {}).get(role, "")).resolve()
        if not report_path.is_relative_to(root / "docs/temp/projects" / name):
            raise ContractError("report is outside the admitted project")
        report = read_json(report_path)
        check_packet(report, contract)
        if report.get("role") != role or report.get("build", {}).get("sha256") != build["sha256"]:
            raise ContractError("validation role/artifact mismatch")
        criteria = report.get("criteria", {})
        if set(criteria) != expected or any(
            not isinstance(value, dict) or value.get("verdict") != "PASS" or
            not str(value.get("evidence", "")).strip() for value in criteria.values()
        ):
            raise ContractError("all immutable criteria need independent PASS evidence")
        work_id = report.get("validationWorkId")
        if not isinstance(work_id, str) or not work_id or work_id in seen:
            raise ContractError("validation must use distinct canonical Work identities")
        completed_validation(work_id, runtime["sessionId"], runtime["server"])
        seen.add(work_id)
    return {"status": "verified", "project": name, "build": build}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("operation", choices=["status", "verify-work", "verify-completion"])
    parser.add_argument("--type", choices=["project", "idea", "task"])
    parser.add_argument("--name")
    parser.add_argument("--payload", default="{}")
    args = parser.parse_args()
    root = root_path()
    if args.operation == "status":
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
