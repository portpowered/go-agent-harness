"""Behavioral checks for the small, meta-planner-owned factory graph."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
COMMON = Path(
    subprocess.check_output(
        ["git", "-C", str(ROOT), "rev-parse", "--path-format=absolute", "--git-common-dir"],
        text=True,
    ).strip()
)
YOU = str(COMMON / "factory-bin/you") if (COMMON / "factory-bin/you").is_file() else shutil.which("you")


MOCK_WORKER = r'''
import json
import os
from pathlib import Path
import sys


root = Path(os.environ["FACTORY_SMOKE_ROOT"])
role = sys.argv[1]
counts_path = root / "counts.json"
counts = json.loads(counts_path.read_text()) if counts_path.exists() else {}
counts[role] = counts.get(role, 0) + 1
counts_path.write_text(json.dumps(counts), encoding="utf-8")

events_path = root / "meta-events.jsonl"


def emit(value):
    print(
        json.dumps(
            {
                "type": "item.completed",
                "item": {
                    "id": "message-final",
                    "type": "agent_message",
                    "text": json.dumps(value),
                },
            }
        ),
        flush=True,
    )


def packet(request_id, works):
    return {"requestId": request_id, "type": "FACTORY_REQUEST_BATCH", "works": works, "relations": []}


def work(name, work_type, payload):
    return {"name": name, "workTypeName": work_type, "payload": payload}


project = "audio-runtime"
revision = "audio-runtime-v1"
idea_name = "audio-runtime-c01-smoke"
validation_name = "audio-runtime-c01-validation-smoke"
payload = {"project": project, "contractRevision": revision, "purpose": "native graph smoke"}

if role == "meta":
    wake_number = counts[role]
    if wake_number == 1:
        with events_path.open("a", encoding="utf-8") as stream:
            stream.write("idea\n")
        emit({"request": packet("graph-smoke-idea", [work(idea_name, "idea", payload)])})
    elif wake_number == 2:
        with events_path.open("a", encoding="utf-8") as stream:
            stream.write("validation\n")
        emit({"request": packet("graph-smoke-validation", [work(validation_name, "validation", payload)])})
    else:
        with events_path.open("a", encoding="utf-8") as stream:
            stream.write("idle\n")
        emit({"summary": "no dispatch required"})
else:
    emit({"decision": "ACCEPTED", "feedback": role + " smoke passed"})
'''


def factory_definition():
    return json.loads((ROOT / "factory/factory.json").read_text(encoding="utf-8"))


class FactoryGraphTest(unittest.TestCase):
    def test_meta_owned_roles_cadence_and_vertical_routes(self):
        definition = factory_definition()
        workers = {worker["name"]: worker for worker in definition["workers"]}
        workstations = {station["name"]: station for station in definition["workstations"]}
        work_types = {work_type["name"] for work_type in definition["workTypes"]}

        self.assertEqual(
            set(workers),
            {"ideafier", "planner", "processor", "reviewer", "validator", "workspace-setup", "validation-setup"},
        )
        self.assertNotIn("project-cycle", work_types)
        for removed in ("project-lead", "project-reconcile", "project-reconciler", "ci-wait", "ci-waiter"):
            self.assertNotIn(removed, workers)
            self.assertNotIn(removed, workstations)

        for role in ("ideafier", "planner"):
            self.assertEqual(workers[role]["model"], "gpt-6-astra")
            self.assertEqual(workers[role]["reasoningEffort"], "medium")
            self.assertEqual(workers[role]["timeout"], "4h")
        for role in ("processor", "reviewer", "validator"):
            self.assertEqual(workers[role]["model"], "gpt-5.6-luna")
            self.assertEqual(workers[role]["reasoningEffort"], "max")
            self.assertEqual(workers[role]["timeout"], "4h")

        self.assertEqual(workstations["though-retrigger"]["cron"]["schedule"], "0 */4 * * *")
        self.assertEqual(workstations["ideafy"]["worker"], "ideafier")
        self.assertEqual(workstations["plan"]["worker"], "planner")
        self.assertEqual(workstations["setup-workspace"]["worker"], "workspace-setup")
        self.assertEqual(workstations["process"]["worker"], "processor")
        self.assertEqual(workstations["review"]["worker"], "reviewer")
        self.assertEqual(workstations["validate"]["worker"], "validator")

        self.assertEqual(
            {(item["workType"], item["state"]) for item in workstations["consume"]["inputs"]},
            {("idea", "to-complete"), ("task", "to-complete")},
        )
        self.assertEqual(
            {(item["workType"], item["state"]) for item in workstations["consume"]["outputs"]},
            {("idea", "complete"), ("task", "complete"), ("thoughts", "init")},
        )

    @unittest.skipUnless(YOU, "installed factory runtime is required")
    def test_native_mock_delivery_wakes_meta_and_runs_validation(self):
        with tempfile.TemporaryDirectory(prefix="harness-factory-graph-") as temporary:
            root = Path(temporary).resolve()
            shutil.copytree(ROOT / "factory", root / "factory")

            fixture = root / "mock-worker.py"
            fixture.write_text(MOCK_WORKER, encoding="utf-8")
            script_entries = []
            for worker, role in (
                ("ideafier", "meta"),
                ("planner", "planner"),
                ("processor", "processor"),
                ("reviewer", "reviewer"),
                ("validator", "validator"),
            ):
                script_entries.append(
                    {
                        "workerName": worker,
                        "runType": "script",
                        "scriptConfig": {
                            "command": sys.executable,
                            "args": [str(fixture), role],
                            "env": {"FACTORY_SMOKE_ROOT": str(root)},
                        },
                    }
                )
            mock_config = root / "mock-workers.json"
            mock_config.write_text(
                json.dumps(
                    {
                        "unmatchedDispatchPolicy": "accept",
                        "mockWorkers": script_entries
                        + [
                            {"workerName": "workspace-setup", "runType": "accept"},
                            {"workerName": "validation-setup", "runType": "accept"},
                        ],
                    }
                ),
                encoding="utf-8",
            )
            initial = root / "initial.json"
            initial.write_text(
                json.dumps(
                    {
                        "requestId": "graph-smoke-bootstrap",
                        "type": "FACTORY_REQUEST_BATCH",
                        "works": [
                            {
                                "name": "meta-bootstrap",
                                "workTypeName": "thoughts",
                                "payload": {
                                    "project": "audio-runtime",
                                    "contractRevision": "audio-runtime-v1",
                                    "trigger": "native smoke",
                                },
                            }
                        ],
                        "relations": [],
                    }
                ),
                encoding="utf-8",
            )
            (root / ".claude/worktrees/audio-runtime-c01-smoke").mkdir(parents=True)
            (root / "docs/temp/probes/audio-runtime-c01-validation-smoke").mkdir(parents=True)
            recording = root / "recording.json"
            environment = dict(os.environ, FACTORY_SMOKE_ROOT=str(root))
            command = [
                YOU,
                "run",
                "--dir",
                str(root / "factory"),
                "--work",
                str(initial),
                "--with-mock-workers",
                str(mock_config),
                "--record",
                str(recording),
                "--quiet",
            ]
            try:
                result = subprocess.run(
                    command,
                    cwd=root,
                    env=environment,
                    capture_output=True,
                    text=True,
                    timeout=90,
                    check=False,
                )
            except subprocess.TimeoutExpired as error:
                self.fail("native mock graph did not become idle: " + str(error))
            self.assertEqual(result.returncode, 0, result.stdout + "\n" + result.stderr)

            counts = json.loads((root / "counts.json").read_text(encoding="utf-8"))
            self.assertEqual(
                counts,
                {"meta": 3, "planner": 1, "processor": 1, "reviewer": 1, "validator": 1},
                result.stdout + "\n" + result.stderr,
            )
            self.assertEqual(
                (root / "meta-events.jsonl").read_text(encoding="utf-8").splitlines(),
                ["idea", "validation", "idle"],
            )
            self.assertTrue(recording.is_file())
            events = json.loads(recording.read_text(encoding="utf-8"))["events"]
            transitions = {
                event["payload"].get("transitionId")
                for event in events
                if event["id"].startswith("factory-event/dispatch-completed/")
            }
            self.assertTrue(
                {
                    "ideafy",
                    "plan",
                    "setup-workspace",
                    "process",
                    "review",
                    "consume",
                    "prepare-validation",
                    "validate",
                } <= transitions,
                transitions,
            )


if __name__ == "__main__":
    unittest.main()
