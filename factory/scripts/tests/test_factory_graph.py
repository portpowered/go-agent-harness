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
    if role == "ci-gate":
        print(json.dumps(value), flush=True)
        return
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
payload = {"project": project, "contractRevision": revision, "purpose": "native graph smoke"}

if role == "ci-gate" and os.environ.get("FACTORY_SMOKE_FAILURE") == "1":
    emit({"decision": "FAILED", "feedback": "bounded infrastructure outage"})
    sys.exit(0)

if role == "meta":
    wake_number = counts[role]
    if wake_number == 1:
        with events_path.open("a", encoding="utf-8") as stream:
            stream.write("idea\n")
        emit({"request": packet("graph-smoke-idea", [work(idea_name, "idea", payload)])})
    else:
        with events_path.open("a", encoding="utf-8") as stream:
            stream.write("idle\n")
        emit({"summary": "no dispatch required"})
elif role == "ci-gate" and counts[role] == 1:
    emit({"decision": "REJECTED", "feedback": "required CI regression; repair same task"})
elif role == "processor" and counts[role] == 1:
    emit({"decision": "CONTINUE", "feedback": "checkpoint committed; required repair remains"})
else:
    emit({"decision": "ACCEPTED", "feedback": role + " smoke passed"})
'''


def factory_definition():
    return json.loads((ROOT / "factory/factory.json").read_text(encoding="utf-8"))


class FactoryGraphTest(unittest.TestCase):
    def test_role_instructions_require_complete_vertical_retirement(self):
        instruction_paths = [
            "factory/docs/operating-policy.md",
            "factory/docs/meta-planner-handoff.md",
            "factory/workstations/ideafy/AGENTS.md",
            "factory/workstations/plan/AGENTS.md",
            "factory/workstations/process/AGENTS.md",
            "factory/workstations/review/AGENTS.md",
        ]
        instructions = {
            path: (ROOT / path).read_text(encoding="utf-8")
            for path in instruction_paths
        }
        for path, text in instructions.items():
            with self.subTest(path=path):
                self.assertIn("services/<vertical>/internal", text)
                self.assertIn(
                    "agent-cli/internal/services/internal/agentruntime", text
                )
                self.assertIn("two production files", text)
                self.assertIn("300 physical", text)
                self.assertIn("full functional CI", text)

        policy = instructions["factory/docs/operating-policy.md"]
        self.assertIn("names every caller it will cut", policy)
        self.assertIn("exact legacy production files it will delete", policy)
        self.assertIn("A new service beside retained duplicate legacy logic", policy)
        self.assertIn("external-consumer fixtures", policy)
        self.assertIn("arbitrary net-line floor", policy)

        reviewer = instructions["factory/workstations/review/AGENTS.md"]
        self.assertIn("qualitative source inspection", reviewer)
        self.assertIn("independent code review", reviewer)

        meta = instructions["factory/workstations/ideafy/AGENTS.md"]
        self.assertIn("On every wake", meta)
        self.assertIn("Compare this measurement with", meta)
        self.assertIn("the prior wake", meta)
        self.assertIn("exact legacy production files", meta)
        self.assertIn("Do not report the factory healthy", meta)
        self.assertIn("specific user attention needed", meta)

    def test_delivery_roles_require_local_pre_pr_gate(self):
        required = {
            "factory/docs/operating-policy.md": ("make prepush", "Before the first PR"),
            "factory/workstations/ideafy/AGENTS.md": (
                "make prepush",
                "before opening or",
            ),
            "factory/workstations/plan/AGENTS.md": (
                "make prepush",
                "executor opens a PR",
            ),
            "factory/workstations/process/AGENTS.md": (
                "make prepush",
                "Before the first PR",
            ),
            "factory/workstations/review/AGENTS.md": ("make prepush", "REJECTED"),
        }
        for relative, phrases in required.items():
            with self.subTest(path=relative):
                text = (ROOT / relative).read_text(encoding="utf-8")
                for phrase in phrases:
                    self.assertIn(phrase, text)

    def test_meta_owned_roles_cadence_and_hermetic_delivery_route(self):
        definition = factory_definition()
        resources = {r["name"]: r["capacity"] for r in definition["resources"]}
        self.assertEqual(resources["executor-slot"], 8)
        review = next(s for s in definition["workstations"] if s["name"] == "review")
        self.assertEqual(review["resources"], [{"name": "executor-slot", "capacity": 1}])
        workers = {worker["name"]: worker for worker in definition["workers"]}
        workstations = {station["name"]: station for station in definition["workstations"]}
        work_types = {work_type["name"] for work_type in definition["workTypes"]}

        self.assertEqual(
            set(workers),
            {"ideafier", "planner", "processor", "reviewer", "workspace-setup", "ci-gate"},
        )
        self.assertNotIn("project-cycle", work_types)
        self.assertNotIn("validation", work_types)
        self.assertNotIn("validation-slot", resources)
        self.assertNotIn("validator", workers)
        self.assertNotIn("validation-setup", workers)
        self.assertNotIn("prepare-validation", workstations)
        self.assertNotIn("validate", workstations)
        for removed in ("project-lead", "project-reconcile", "project-reconciler", "ci-wait", "ci-waiter"):
            self.assertNotIn(removed, workers)
            self.assertNotIn(removed, workstations)
        for removed in ("executor-loop-breaker", "review-loop-breaker"):
            self.assertNotIn(removed, workstations)

        self.assertEqual(workstations["process"]["behavior"], "REPEATER")
        self.assertEqual(
            workstations["process"]["onContinue"],
            [{"state": "init", "workType": "task"}],
        )

        for role in ("ideafier", "planner"):
            self.assertEqual(workers[role]["model"], "gpt-5.6-sol")
            self.assertEqual(workers[role]["reasoningEffort"], "medium")
            self.assertEqual(workers[role]["timeout"], "4h")
        for role in ("processor", "reviewer"):
            self.assertEqual(workers[role]["model"], "gpt-5.6-luna")
            self.assertEqual(workers[role]["reasoningEffort"], "xhigh")
            self.assertEqual(workers[role]["timeout"], "4h")

        self.assertEqual(workstations["though-retrigger"]["cron"]["schedule"], "0 */4 * * *")
        self.assertEqual(workstations["ideafy"]["worker"], "ideafier")
        self.assertEqual(workstations["plan"]["worker"], "planner")
        self.assertEqual(workstations["setup-workspace"]["worker"], "workspace-setup")
        self.assertEqual(workstations["process"]["worker"], "processor")
        self.assertEqual(workstations["review"]["worker"], "reviewer")

        self.assertEqual(
            {(item["workType"], item["state"]) for item in workstations["consume"]["inputs"]},
            {("idea", "to-complete"), ("task", "to-complete")},
        )
        self.assertEqual(
            {(item["workType"], item["state"]) for item in workstations["consume"]["outputs"]},
            {("idea", "complete"), ("task", "complete"), ("thoughts", "init")},
        )

    @unittest.skipUnless(YOU, "installed factory runtime is required")
    def test_native_mock_delivery_wakes_meta_after_hermetic_ci_and_review(self):
        self.run_native_graph(False)

    @unittest.skipUnless(YOU, "installed factory runtime is required")
    def test_native_ci_infrastructure_failure_wakes_meta(self):
        self.run_native_graph(True)

    def run_native_graph(self, infrastructure_failure):
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
                ("ci-gate", "ci-gate"),
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
            recording = root / "recording.json"
            environment = dict(os.environ, FACTORY_SMOKE_ROOT=str(root), FACTORY_SMOKE_FAILURE="1" if infrastructure_failure else "0")
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
            self.assertEqual(result.returncode, 1 if infrastructure_failure else 0, result.stdout + "\n" + result.stderr)

            counts = json.loads((root / "counts.json").read_text(encoding="utf-8"))
            if infrastructure_failure:
                self.assertEqual(counts, {"meta": 2, "planner": 1, "processor": 2, "ci-gate": 1})
                self.assertEqual((root / "meta-events.jsonl").read_text().splitlines(), ["idea", "idle"])
                return
            self.assertEqual(
                counts,
                {"meta": 2, "planner": 1, "processor": 3, "ci-gate": 2, "reviewer": 1},
                result.stdout + "\n" + result.stderr,
            )
            self.assertEqual(
                (root / "meta-events.jsonl").read_text(encoding="utf-8").splitlines(),
                ["idea", "idle"],
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
                    "ci-gate",
                    "process",
                    "review",
                    "consume",
                } <= transitions,
                transitions,
            )


if __name__ == "__main__":
    unittest.main()
