#!/usr/bin/env python3
"""Authorize the reviewed stopped-board graph migration."""
import hashlib
import json
from pathlib import Path
import subprocess
import uuid

import project_admission
from project_contract import root_path, digest

BASE_REVISION = "b11f91448"
SOURCE_DEFINITION_SHA256 = (
    "3bae337e36d2d8a22919c62ad8dcb7e33fc9a25ba352eb6c67345099c26a7304"
)


def migrate(root):
    common = project_admission.common_dir(root)
    path = common / "factory-runtime.json"
    record = json.loads(path.read_text())
    if record.get("status") != "stopped" or record.get("root") != str(root):
        raise ValueError("stop the owned runtime before migrating its graph")
    old = subprocess.check_output(["git", "show", BASE_REVISION + ":factory/factory.json"], cwd=root)
    old_digest = hashlib.sha256(old).hexdigest()
    if old_digest != SOURCE_DEFINITION_SHA256:
        raise ValueError("reviewed migration source revision has unexpected graph")
    if record.get("definitionSha256") != SOURCE_DEFINITION_SHA256:
        raise ValueError("saved graph is not the reviewed migration source")
    new = json.loads((root / "factory/factory.json").read_text())
    expected = json.loads(old)
    # This source already has the reviewed CI lane, eight shared slots and Sol
    # medium planning. Only remove model validation and raise Luna execution and
    # review from max to xhigh.
    expected["resources"] = [
        item for item in expected["resources"] if item["name"] != "validation-slot"
    ]
    expected["workTypes"] = [
        item for item in expected["workTypes"] if item["name"] != "validation"
    ]
    expected["workers"] = [
        item
        for item in expected["workers"]
        if item["name"] not in {"validation-setup", "validator"}
    ]
    expected["workstations"] = [
        item
        for item in expected["workstations"]
        if item["name"] not in {"prepare-validation", "validate"}
    ]
    for resource in expected["resources"]:
        if resource["name"] == "executor-slot":
            resource["capacity"] = 8
    for worker in expected["workers"]:
        if worker["name"] in {"ideafier", "planner"}:
            worker["model"] = "gpt-5.6-sol"
            worker["reasoningEffort"] = "medium"
        elif worker["name"] in {"processor", "reviewer"}:
            worker["model"] = "gpt-5.6-luna"
            worker["reasoningEffort"] = "xhigh"
    if new != expected:
        raise ValueError("graph changes exceed the reviewed stopped-board migration")
    recording = Path(record["recording"])
    if digest(recording) != record.get("recordingSha256"):
        raise ValueError("stopped recording changed")
    backup = common / "factory-runs" / (uuid.uuid4().hex + "-before-ci-graph.json")
    backup.write_text(json.dumps(record, indent=2) + "\n")
    record["definitionSha256"] = digest(root / "factory/factory.json")
    record["graphMigration"] = {
        "sourceRevision": BASE_REVISION,
        "backup": str(backup),
        "kind": "remove-model-validation-use-luna-xhigh",
    }
    temporary = path.with_suffix(".migration.tmp")
    temporary.write_text(json.dumps(record, indent=2) + "\n")
    temporary.replace(path)
    return record["graphMigration"]


if __name__ == "__main__":
    print(json.dumps(migrate(root_path())))
