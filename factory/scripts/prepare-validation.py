#!/usr/bin/env python3
"""Stage a fresh, immutable validation mission after project preflight."""
import json
from pathlib import Path
import shutil
import sys

import project_admission
import project_scope_amendment
from project_contract import ContractError, artifact, check_packet, digest, root_path, work_name


MAX_ARTIFACT_BYTES = 2 * 1024 * 1024 * 1024


def _text(value):
    return isinstance(value, str) and bool(value.strip())


def _validation_scope(packet):
    """Validate and normalize the scope metadata for a validation mission."""
    scope = packet.get("scope", "project")
    if not isinstance(scope, str) or scope not in {"project", "vertical"}:
        raise ContractError("validation scope must be project or vertical")

    if scope == "vertical":
        names = [packet[key] for key in ("vertical", "verticalName") if key in packet]
        if not names or any(not _text(value) for value in names) or len(set(names)) != 1:
            raise ContractError("vertical validation requires one nonempty vertical name")
        revisions = [
            packet[key]
            for key in ("sourceRevision", "mergedRevision")
            if key in packet
        ]
        if not revisions or any(not _text(value) for value in revisions) or len(set(revisions)) != 1:
            raise ContractError(
                "vertical validation requires one nonempty source or merged revision"
            )
        # Keep stable names for validators even when a caller uses the more
        # descriptive aliases.  Existing fields remain in the packet too.
        packet["vertical"] = names[0]
        packet["sourceRevision"] = revisions[0]

    packet["scope"] = scope
    return scope


def _criteria(packet, contract, scope):
    allowed = {entry["id"]: entry["rubric"] for entry in contract["criteria"]}
    criteria = packet.get("criteria")
    if not isinstance(criteria, list) or not criteria or any(
        not isinstance(item, dict)
        or not isinstance(item.get("id"), str)
        or item.get("id") not in allowed
        or item.get("rubric") != allowed.get(item.get("id"))
        for item in criteria
    ):
        raise ContractError("validation criteria must preserve immutable rubrics")
    ids = [item["id"] for item in criteria]
    if len(set(ids)) != len(ids):
        raise ContractError("duplicate validation criterion")
    if scope == "project" and set(ids) != set(allowed):
        raise ContractError(
            "validation criteria must preserve immutable rubrics and every criterion"
        )
    if scope == "vertical" and not set(ids).issubset(allowed):
        raise ContractError("validation criteria must preserve immutable rubrics")


def prepare(root, name, payload):
    try:
        contract = project_scope_amendment.admitted_contract(root)
    except project_scope_amendment.ScopeAmendmentError as error:
        raise ContractError(str(error)) from error
    check_packet(project_admission.status(root), contract)
    work_name(name)
    if not name.startswith(contract["project"] + "-c"):
        raise ContractError("validation name belongs to another project")
    packet = json.loads(payload)
    check_packet(packet, contract)
    amendment_scope_explicit = "scope" in packet and "amendment" in packet
    if packet.get("role") not in {"customer", "engineering", "retrospective"}:
        raise ContractError("unknown validation role")
    scope = _validation_scope(packet)
    _criteria(packet, contract, scope)
    amendment = None
    if "amendment" in packet:
        if not amendment_scope_explicit:
            raise ContractError(
                "scope amendments require an explicit project-scope mission"
            )
        try:
            amendment = project_scope_amendment.normalize_packet_amendment(root, packet)
        except project_scope_amendment.ScopeAmendmentError as error:
            raise ContractError(str(error)) from error
    budget = packet.get("budget", {})
    time_seconds = budget.get("timeSeconds") if isinstance(budget, dict) else None
    if (
        not isinstance(budget, dict)
        or isinstance(time_seconds, bool)
        or not isinstance(time_seconds, int)
        or not 1 <= time_seconds <= 1800
        or not isinstance(packet.get("mission"), str)
    ):
        raise ContractError(
            "mission requires an integer budget from 1 to 1800 seconds and description"
        )
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
        packet["validationWorkName"] = name
        if amendment is not None:
            packet["manifestSha256"] = amendment["manifestSha256"]
        (target / "mission.json").write_text(json.dumps(packet, indent=2) + "\n")
        (target / "mission.json").chmod(0o400)
        report.parent.mkdir(parents=True, exist_ok=True)
    except Exception:
        # Keep the uniquely owned failed directory as evidence, but publish no mission.
        (target / "mission.json").unlink(missing_ok=True)
        raise
    return {
        "status": "ready",
        "project": packet["project"],
        "directory": str(target),
        "build": packet["build"],
        "buildIdentity": packet["build"]["identity"],
        "validationWorkName": name,
        "missionSha256": digest(target / "mission.json"),
        **({"amendment": amendment} if amendment is not None else {}),
    }


if __name__ == "__main__":
    try:
        import argparse

        parser = argparse.ArgumentParser()
        parser.add_argument("--root")
        parser.add_argument("name")
        parser.add_argument("payload")
        args = parser.parse_args()
        root = Path(args.root).resolve() if args.root else root_path()
        print(json.dumps(prepare(root, args.name, args.payload)))
    except (ValueError, OSError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
