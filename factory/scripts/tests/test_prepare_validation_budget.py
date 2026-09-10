"""Public subprocess regressions for bounded validation budgets."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest


SCRIPTS_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = SCRIPTS_DIR.parents[1]
PREPARE_SCRIPT = SCRIPTS_DIR / "prepare-validation.py"
BEFORE_SCRIPT = (
    REPO_ROOT
    / "docs"
    / "temp"
    / "projects"
    / "audio-runtime"
    / "audio-runtime-c29-bounded-validation-budget"
    / "prepare-validation-before.py"
)
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract


class IsolatedValidation:
    """A temporary same-project admission used only by a child process."""

    project = "audio-runtime"
    contract_revision = "audio-runtime-v1"

    def __init__(self, base, label):
        self.root = (Path(base) / label / "repo").resolve()
        self.root.mkdir(parents=True)
        self._run("git", "init", "-q", "-b", "main", str(self.root))
        source = REPO_ROOT / "factory" / "projects" / self.project
        destination = self.root / "factory" / "projects" / self.project
        destination.parent.mkdir(parents=True)
        shutil.copytree(source, destination)
        (self.root / "docs" / "temp" / "projects" / self.project).mkdir(
            parents=True,
        )
        (self.root / "docs" / "temp" / "probes").mkdir(parents=True)
        self.admission = project_admission.ProjectAdmission(self.root)
        self.admission.Admit(self.project, self.contract_revision)
        self.contract = project_contract.manifest(self.root)
        self.build_path = self.root / "artifacts" / "build.bin"
        self.fixture_path = self.root / "artifacts" / "fixture.pcm"
        self.build_path.parent.mkdir(parents=True, exist_ok=True)
        self.build_path.write_bytes(b"meaningful executable fixture\n")
        self.fixture_path.write_bytes(bytes(range(32)))
        for path in (self.build_path, self.fixture_path):
            path.chmod(0o444)

    @staticmethod
    def _run(*command):
        result = subprocess.run(
            list(command),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode:
            raise AssertionError(result.stderr.strip())
        return result

    @staticmethod
    def _digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def artifact(self, path, identity):
        return {
            "identity": identity,
            "path": str(path),
            "sha256": self._digest(path),
        }

    def packet(
        self,
        name,
        *,
        time_seconds=900,
        scope="vertical",
        criterion_ids=None,
    ):
        report = (
            self.root
            / "docs"
            / "temp"
            / "projects"
            / self.project
            / f"{name}.json"
        )
        criteria = self.contract["criteria"]
        if criterion_ids is not None:
            criteria = [entry for entry in criteria if entry["id"] in criterion_ids]
        packet = {
            "project": self.project,
            "contractRevision": self.contract_revision,
            "role": "engineering",
            "scope": scope,
            "vertical": "audio-runtime-c29",
            "sourceRevision": "candidate-source",
            "criteria": [
                {"id": entry["id"], "rubric": entry["rubric"]}
                for entry in criteria
            ],
            "budget": {
                "timeSeconds": time_seconds,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Run the bounded validation control.",
            "reportPath": str(report),
            "build": self.artifact(self.build_path, "build-fixture-v1"),
            "fixtures": [self.artifact(self.fixture_path, "pcm-fixture-v1")],
        }
        if scope == "project":
            packet.pop("vertical")
            packet.pop("sourceRevision")
        return packet

    def run(self, name, packet, script=PREPARE_SCRIPT):
        environment = {
            key: value
            for key, value in os.environ.items()
            if not key.startswith("FACTORY_")
        }
        existing_pythonpath = environment.get("PYTHONPATH")
        environment["FACTORY_ROOT"] = str(self.root)
        environment["PYTHONPATH"] = os.pathsep.join(
            value
            for value in (str(SCRIPTS_DIR), existing_pythonpath)
            if value
        )
        environment["LC_ALL"] = "C"
        environment["LANG"] = "C"
        return subprocess.run(
            [
                sys.executable,
                str(script),
                name,
                json.dumps(packet, sort_keys=True),
            ],
            cwd=self.root,
            env=environment,
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )

    def mission_path(self, name):
        return self.root / "docs" / "temp" / "probes" / name / "mission.json"


class PrepareValidationBudgetTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory(prefix="c29-budget-tests-")
        self.addCleanup(self.temp_dir.cleanup)

    def fixture(self, label):
        return IsolatedValidation(self.temp_dir.name, label)

    def assert_failure(self, fixture, name, packet, *, text=None):
        result = fixture.run(name, packet)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(result.stdout, "")
        if text is not None:
            self.assertIn(text, result.stderr)
        self.assertFalse(fixture.mission_path(name).exists())
        return result

    def test_before_repair_rejects_900_and_accepts_1800(self):
        self.assertTrue(BEFORE_SCRIPT.is_file())
        fixture = self.fixture("before")

        rejected_name = "audio-runtime-c29-before-900"
        rejected = fixture.run(
            rejected_name,
            fixture.packet(rejected_name, time_seconds=900),
            script=BEFORE_SCRIPT,
        )
        self.assertNotEqual(rejected.returncode, 0)
        self.assertEqual(
            rejected.stderr.strip(),
            "mission requires a 1800-second budget and description",
        )
        self.assertFalse(fixture.mission_path(rejected_name).exists())

        ready_name = "audio-runtime-c29-before-1800"
        ready = fixture.run(
            ready_name,
            fixture.packet(ready_name, time_seconds=1800),
            script=BEFORE_SCRIPT,
        )
        self.assertEqual(ready.returncode, 0, ready.stderr)
        self.assertEqual(json.loads(ready.stdout)["status"], "ready")
        self.assertTrue(fixture.mission_path(ready_name).is_file())

    def test_integer_budget_matrix_preserves_values_and_staging_protections(self):
        fixture = self.fixture("positive")
        before_build = fixture.build_path.read_bytes()
        before_fixture = fixture.fixture_path.read_bytes()
        before_admission = fixture.admission.Status()

        for time_seconds in (1, 900, 1800):
            name = f"audio-runtime-c29-positive-{time_seconds}"
            packet = fixture.packet(name, time_seconds=time_seconds)
            result = fixture.run(name, packet)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stderr, "")
            envelope = json.loads(result.stdout)
            self.assertEqual(envelope["status"], "ready")
            target = Path(envelope["directory"])
            mission = json.loads(
                (target / "mission.json").read_text(encoding="utf-8")
            )
            self.assertEqual(mission["budget"], packet["budget"])
            self.assertEqual(mission["scope"], packet["scope"])
            self.assertEqual(mission["criteria"], packet["criteria"])
            self.assertEqual(mission["authority"], fixture.contract["authority"])
            self.assertEqual(mission["reportPath"], packet["reportPath"])
            self.assertFalse(Path(packet["reportPath"]).exists())
            self.assertEqual(
                Path(mission["build"]["path"]).parent,
                target,
            )
            self.assertEqual(
                [Path(item["path"]).parent for item in mission["fixtures"]],
                [target],
            )
            self.assertEqual(
                self._digest(Path(mission["build"]["path"])),
                packet["build"]["sha256"],
            )
            self.assertEqual(
                self._digest(Path(mission["fixtures"][0]["path"])),
                packet["fixtures"][0]["sha256"],
            )
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE((target / "mission.json").stat().st_mode), 0o400)
            self.assertEqual(
                stat.S_IMODE(Path(mission["build"]["path"]).stat().st_mode),
                0o500,
            )
            self.assertEqual(
                stat.S_IMODE(Path(mission["fixtures"][0]["path"]).stat().st_mode),
                0o400,
            )

        self.assertEqual(fixture.build_path.read_bytes(), before_build)
        self.assertEqual(fixture.fixture_path.read_bytes(), before_fixture)
        self.assertEqual(fixture.admission.Status(), before_admission)
        self.assertEqual(stat.S_IMODE(fixture.build_path.stat().st_mode), 0o444)
        self.assertEqual(stat.S_IMODE(fixture.fixture_path.stat().st_mode), 0o444)

    def test_invalid_budget_values_and_description_fail_closed(self):
        fixture = self.fixture("invalid-budget")
        invalid = [
            ("true", True),
            ("false", False),
            ("float", 900.0),
            ("fraction", 900.5),
            ("string", "900"),
            ("zero", 0),
            ("negative", -1),
            ("excessive", 1801),
        ]
        for label, value in invalid:
            name = f"audio-runtime-c29-invalid-{label}"
            packet = fixture.packet(name, time_seconds=value)
            self.assert_failure(
                fixture,
                name,
                packet,
                text="mission requires an integer budget from 1 to 1800 seconds and description",
            )

        missing_name = "audio-runtime-c29-invalid-missing"
        missing = fixture.packet(missing_name)
        del missing["budget"]["timeSeconds"]
        self.assert_failure(
            fixture,
            missing_name,
            missing,
            text="mission requires an integer budget from 1 to 1800 seconds and description",
        )

        absent_name = "audio-runtime-c29-invalid-budget-absent"
        absent = fixture.packet(absent_name)
        del absent["budget"]
        self.assert_failure(
            fixture,
            absent_name,
            absent,
            text="mission requires an integer budget from 1 to 1800 seconds and description",
        )

        malformed_name = "audio-runtime-c29-invalid-budget-object"
        malformed = fixture.packet(malformed_name)
        malformed["budget"] = []
        self.assert_failure(
            fixture,
            malformed_name,
            malformed,
            text="mission requires an integer budget from 1 to 1800 seconds and description",
        )

        description_name = "audio-runtime-c29-invalid-description"
        description = fixture.packet(description_name)
        description["mission"] = None
        self.assert_failure(
            fixture,
            description_name,
            description,
            text="mission requires an integer budget from 1 to 1800 seconds and description",
        )

    def test_scope_admission_authority_artifact_and_report_controls_remain_closed(self):
        scenarios = {
            "wrong-project": lambda fixture, packet: packet.update(project="other-project"),
            "missing-admission": lambda fixture, packet: fixture.admission.Release(
                fixture.project,
                {"outcome": "temporary-control"},
            ),
            "stale-authority": lambda fixture, packet: (
                fixture.root.joinpath(
                    "factory/projects/audio-runtime/source-plan.md"
                ).write_text("stale\n", encoding="utf-8")
            ),
            "invalid-digest": lambda fixture, packet: packet["build"].update(
                sha256="0" * 64
            ),
            "realtime-sessions": lambda fixture, packet: packet["budget"].update(
                realtimeSessions=4
            ),
            "realtime-seconds": lambda fixture, packet: packet["budget"].update(
                realtimeSeconds=121
            ),
            "realtime-type": lambda fixture, packet: packet["budget"].update(
                realtimeSeconds=1.0
            ),
            "altered-criterion": lambda fixture, packet: packet["criteria"][0].update(
                rubric="altered"
            ),
            "duplicate-criterion": lambda fixture, packet: packet["criteria"].append(
                dict(packet["criteria"][0])
            ),
            "project-subset": lambda fixture, packet: (
                packet.update(scope="project"),
                packet["criteria"].__setitem__(slice(None), packet["criteria"][:1]),
            ),
            "invalid-scope": lambda fixture, packet: packet.update(scope="other"),
            "missing-vertical": lambda fixture, packet: packet.pop("vertical"),
            "missing-source": lambda fixture, packet: packet.pop("sourceRevision"),
            "outside-report": lambda fixture, packet: packet.update(
                reportPath=str(fixture.root / "docs/temp/projects/outside.json")
            ),
            "non-json-report": lambda fixture, packet: packet.update(
                reportPath=str(
                    fixture.root
                    / "docs/temp/projects/audio-runtime/not-json.txt"
                )
            ),
        }

        for label, mutate in scenarios.items():
            with self.subTest(label=label):
                fixture = self.fixture(f"controls-{label}")
                name = f"audio-runtime-c29-control-{label}"
                packet = fixture.packet(name)
                original_build = fixture.build_path.read_bytes()
                original_fixture = fixture.fixture_path.read_bytes()
                mutate(fixture, packet)
                self.assert_failure(fixture, name, packet)
                self.assertEqual(fixture.build_path.read_bytes(), original_build)
                self.assertEqual(fixture.fixture_path.read_bytes(), original_fixture)

        fixture = self.fixture("existing-report")
        name = "audio-runtime-c29-control-existing-report"
        packet = fixture.packet(name)
        report = Path(packet["reportPath"])
        report.parent.mkdir(parents=True, exist_ok=True)
        report.write_text('{"preserve":true}\n', encoding="utf-8")
        original_report = report.read_bytes()
        self.assert_failure(fixture, name, packet)
        self.assertEqual(report.read_bytes(), original_report)

    @staticmethod
    def _digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()


if __name__ == "__main__":
    unittest.main()
