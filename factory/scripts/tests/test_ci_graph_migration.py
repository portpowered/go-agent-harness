import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "factory/scripts"))
spec = importlib.util.spec_from_file_location("ci_migration", ROOT / "factory/scripts/migrate-ci-gate.py")
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)
OLD = subprocess.check_output(["git", "show", migration.BASE_REVISION + ":factory/factory.json"], cwd=ROOT)


class MigrationTest(unittest.TestCase):
    def test_reviewed_source_matches_live_saved_definition(self):
        self.assertEqual(
            migration.hashlib.sha256(OLD).hexdigest(),
            migration.SOURCE_DEFINITION_SHA256,
        )

    def test_preserves_board_and_rejects_running_or_unrelated_graph(self):
        for scenario in ("valid", "running", "unrelated"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                (root / "factory").mkdir()
                common = root / "common"
                (common / "factory-runs").mkdir(parents=True)
                graph = json.loads((ROOT / "factory/factory.json").read_text())
                if scenario == "unrelated":
                    graph["resources"][0]["capacity"] = 10
                (root / "factory/factory.json").write_text(json.dumps(graph))
                recording = common / "factory-runs/board.json"
                recording.write_text('{"events":[]}')
                record = {"root":str(root),"status":"running" if scenario == "running" else "stopped",
                          "recording":str(recording),"recordingSha256":migration.digest(recording),
                          "definitionSha256":migration.hashlib.sha256(OLD).hexdigest()}
                runtime = common / "factory-runtime.json"
                runtime.write_text(json.dumps(record))
                before = runtime.read_bytes()
                with patch.object(migration.project_admission, "common_dir", return_value=common), patch.object(migration.subprocess, "check_output", return_value=OLD):
                    if scenario != "valid":
                        with self.assertRaises(ValueError):
                            migration.migrate(root)
                        self.assertEqual(runtime.read_bytes(), before)
                    else:
                        result = migration.migrate(root)
                        updated = json.loads(runtime.read_text())
                        self.assertEqual(updated["recordingSha256"], record["recordingSha256"])
                        self.assertEqual(json.loads(Path(result["backup"]).read_text()), record)
                        self.assertEqual(updated["definitionSha256"], migration.digest(root / "factory/factory.json"))
                        self.assertEqual(
                            result["kind"], "remove-model-validation-use-luna-xhigh"
                        )

                        resources = {item["name"]: item for item in graph["resources"]}
                        workers = {item["name"]: item for item in graph["workers"]}
                        work_types = {item["name"] for item in graph["workTypes"]}
                        stations = {item["name"] for item in graph["workstations"]}
                        self.assertEqual(resources["executor-slot"]["capacity"], 8)
                        self.assertNotIn("validation-slot", resources)
                        self.assertNotIn("validation", work_types)
                        self.assertNotIn("validation-setup", workers)
                        self.assertNotIn("validator", workers)
                        self.assertNotIn("prepare-validation", stations)
                        self.assertNotIn("validate", stations)
                        for role in ("ideafier", "planner"):
                            self.assertEqual(workers[role]["model"], "gpt-5.6-sol")
                            self.assertEqual(workers[role]["reasoningEffort"], "medium")
                        for role in ("processor", "reviewer"):
                            self.assertEqual(workers[role]["model"], "gpt-5.6-luna")
                            self.assertEqual(workers[role]["reasoningEffort"], "xhigh")
                self.assertEqual(recording.read_text(), '{"events":[]}')


if __name__ == "__main__":
    unittest.main()
