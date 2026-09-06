#!/usr/bin/env python3
"""Stage a fresh, immutable validation mission after project preflight."""
import json
from pathlib import Path
import shutil
import sys

import project_admission
from project_contract import ContractError, artifact, check_packet, digest, manifest, root_path, work_name


MAX_ARTIFACT_BYTES = 2 * 1024 * 1024 * 1024


def prepare(root, name, payload):
    contract = manifest(root)
    check_packet(project_admission.status(root), contract)
    work_name(name)
    if not name.startswith(contract["project"] + "-c"):
        raise ContractError("validation name belongs to another project")
    packet = json.loads(payload)
    check_packet(packet, contract)
    if packet.get("role") not in {"customer", "engineering", "retrospective"}:
        raise ContractError("unknown validation role")
    allowed = {entry["id"]: entry["rubric"] for entry in contract["criteria"]}
    criteria = packet.get("criteria")
    if not isinstance(criteria, list) or not criteria or any(
        not isinstance(item, dict) or not isinstance(item.get("id"), str) or item.get("id") not in allowed or item.get("rubric") != allowed.get(item.get("id"))
        for item in criteria
    ):
        raise ContractError("validation criteria must preserve immutable rubrics")
    if {item["id"] for item in criteria} != set(allowed):
        raise ContractError("validation criteria must preserve immutable rubrics and every criterion")
    if len({item["id"] for item in criteria}) != len(criteria):
        raise ContractError("duplicate validation criterion")
    budget = packet.get("budget", {})
    if budget.get("timeSeconds") != 1800 or not isinstance(packet.get("mission"), str):
        raise ContractError("mission requires a 1800-second budget and description")
    for key, maximum in (("realtimeSessions", 3), ("realtimeSeconds", 120)):
        value = budget.get(key)
        if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value <= maximum:
            raise ContractError("invalid or excessive " + key + " budget")
    report = Path(packet.get("reportPath", ""))
    report_root = (root / "docs/temp/projects" / contract["project"]).resolve()
    if not report.is_absolute() or not report.resolve().is_relative_to(report_root) or report.suffix != ".json" or report.exists():
        raise ContractError("report must be a fresh JSON path under the project evidence directory")
    build = artifact(packet.get("build"))
    fixtures = packet.get("fixtures", [])
    if not isinstance(fixtures, list):
        raise ContractError("fixtures must be a list")
    fixtures = [artifact(item) for item in fixtures]
    if sum(Path(item["path"]).stat().st_size for item in [build, *fixtures]) > MAX_ARTIFACT_BYTES:
        raise ContractError("validation artifacts exceed 2 GiB")
    target = root / "docs/temp/probes" / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.mkdir(mode=0o700)
    try:
        staged = []
        for index, value in enumerate([build, *fixtures]):
            source = Path(value["path"])
            destination = target / ("artifact-" + str(index) + source.suffix)
            shutil.copy2(source, destination)
            if digest(destination) != value["sha256"]:
                raise ContractError("artifact changed while staging")
            destination.chmod(0o500 if index == 0 else 0o400)
            staged.append({**value, "path": str(destination)})
        packet["build"], packet["fixtures"] = staged[0], staged[1:]
        packet["authority"] = contract["authority"]
        (target / "mission.json").write_text(json.dumps(packet, indent=2) + "\n")
        (target / "mission.json").chmod(0o400)
        report.parent.mkdir(parents=True, exist_ok=True)
    except Exception:
        # Keep the uniquely owned failed directory as evidence, but publish no mission.
        (target / "mission.json").unlink(missing_ok=True)
        raise
    return {"status": "ready", "directory": str(target), "build": packet["build"]}


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ContractError("usage: prepare-validation.py <name> <payload-json>")
        print(json.dumps(prepare(root_path(), sys.argv[1], sys.argv[2])))
    except (ValueError, OSError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
