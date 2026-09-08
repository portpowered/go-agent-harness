import hashlib
import importlib.util
import json
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = SCRIPTS_DIR.parents[1]
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract


def _load_script(filename, module_name):
    spec = importlib.util.spec_from_file_location(
        module_name,
        SCRIPTS_DIR / filename,
    )
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


PROJECT_CONTROL = _load_script("project-control.py", "project_control_tests")
PREPARE_VALIDATION = _load_script(
    "prepare-validation.py",
    "prepare_validation_tests",
)


class ProjectFixture:
    project = "audio-runtime"
    contract_revision = "audio-runtime-v1"
    server = "http://127.0.0.1:7439"
    session_id = "session-a"

    def __init__(self, temporary_directory):
        self.root = (Path(temporary_directory) / "repo").resolve()
        self.root.mkdir()
        self._run("git", "init", "-q", "-b", "main", str(self.root))
        self._run("git", "-C", str(self.root), "config", "user.name", "Factory Tests")
        self._run(
            "git",
            "-C",
            str(self.root),
            "config",
            "user.email",
            "factory-tests@example.com",
        )

        project_source = REPO_ROOT / "factory" / "projects" / self.project
        project_destination = (
            self.root / "factory" / "projects" / self.project
        )
        project_destination.parent.mkdir(parents=True)
        shutil.copytree(project_source, project_destination)
        (self.root / "docs" / "temp" / "projects" / self.project).mkdir(
            parents=True,
        )
        (self.root / "docs" / "temp" / "probes").mkdir(parents=True)

        self.admission = project_admission.ProjectAdmission(self.root)
        self.admission.Admit(self.project, self.contract_revision)

    @staticmethod
    def _run(*command):
        result = subprocess.run(
            list(command),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode:
            raise AssertionError(
                f"{' '.join(command)} failed: {result.stderr.strip()}"
            )
        return result

    def bind_runtime(self):
        project_admission.bind_session(
            self.root,
            self.project,
            self.contract_revision,
            self.server,
            self.session_id,
        )
        common = project_admission.common_dir(self.root)
        (common / "factory-runtime.json").write_text(
            json.dumps(
                {
                    "project": self.project,
                    "contractRevision": self.contract_revision,
                    "sessionId": self.session_id,
                    "server": self.server,
                }
            ),
            encoding="utf-8",
        )

    def artifact(self, name="build.bin", content=b"build artifact\n"):
        path = self.root / "artifacts" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return {
            "identity": "build-v1",
            "path": str(path),
            "sha256": hashlib.sha256(content).hexdigest(),
        }

    def validation_packet(
        self,
        name="audio-runtime-c1-customer",
        role="customer",
        *,
        scope=None,
        criterion_ids=None,
        vertical=None,
        source_revision=None,
        merged_revision=None,
    ):
        contract = project_contract.manifest(self.root)
        build = self.artifact()
        report_path = (
            self.root
            / "docs"
            / "temp"
            / "projects"
            / self.project
            / f"{name}.json"
        )
        packet = {
            "project": self.project,
            "contractRevision": self.contract_revision,
            "role": role,
            "criteria": [
                {"id": entry["id"], "rubric": entry["rubric"]}
                for entry in contract["criteria"]
            ],
            "budget": {
                "timeSeconds": 1800,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Run the independent validation mission.",
            "reportPath": str(report_path),
            "build": build,
            "fixtures": [],
        }
        if criterion_ids is not None:
            packet["criteria"] = [
                {"id": entry["id"], "rubric": entry["rubric"]}
                for entry in contract["criteria"]
                if entry["id"] in criterion_ids
            ]
        if scope is not None:
            packet["scope"] = scope
        if vertical is not None:
            packet["vertical"] = vertical
        if source_revision is not None:
            packet["sourceRevision"] = source_revision
        if merged_revision is not None:
            packet["mergedRevision"] = merged_revision
        return packet

    def completion_record(
        self,
        build,
        *,
        missing_role=None,
        same_work_id=False,
        report_scope=None,
    ):
        contract = project_contract.manifest(self.root)
        criteria = {
            entry["id"]: {
                "verdict": "PASS",
                "evidence": f"independent evidence for {entry['id']}",
            }
            for entry in contract["criteria"]
        }
        reports = {}
        for role in ("customer", "engineering"):
            report_path = (
                self.root
                / "docs"
                / "temp"
                / "projects"
                / self.project
                / f"{role}-report.json"
            )
            report_criteria = dict(criteria)
            if role == missing_role:
                report_criteria.pop("PARITY")
            reports[role] = {
                "project": self.project,
                "contractRevision": self.contract_revision,
                "role": role,
                "build": build,
                "criteria": report_criteria,
                "validationWorkId": (
                    "validation-customer"
                    if same_work_id or role == "customer"
                    else "validation-engineering"
                ),
            }
            if report_scope is not None:
                reports[role]["scope"] = report_scope
            report_path.write_text(
                json.dumps(reports[role]),
                encoding="utf-8",
            )
            reports[role] = str(report_path)
        record = {
            "project": self.project,
            "contractRevision": self.contract_revision,
            "build": build,
            "reports": reports,
        }
        completion_path = (
            self.root
            / "docs"
            / "temp"
            / "projects"
            / self.project
            / "completion.json"
        )
        completion_path.write_text(json.dumps(record), encoding="utf-8")
        return completion_path


class ProjectControlAndValidationTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.fixture = ProjectFixture(self.temp_dir.name)
        self.root = self.fixture.root

    def test_cross_project_work_is_rejected_at_both_public_boundaries(self):
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "only the admitted project",
        ):
            PROJECT_CONTROL.verify_work(
                self.root,
                "project",
                "other-project",
                "{}",
            )

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "admitted project cycle prefix",
        ):
            PROJECT_CONTROL.verify_work(
                self.root,
                "idea",
                "other-project-c1",
                json.dumps(
                    {
                        "project": self.fixture.project,
                        "contractRevision": self.fixture.contract_revision,
                    }
                ),
            )

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "belongs to another project",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "other-project-c1-validation",
                "{}",
            )

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "different project",
        ):
            PROJECT_CONTROL.verify_completion(self.root, "other-project")

    def test_project_reentry_reconstructs_empty_payload_from_durable_owner(self):
        result = PROJECT_CONTROL.verify_work(self.root, "project", self.fixture.project, "")
        self.assertEqual(result["status"], "admitted")
        with self.assertRaises(project_contract.ContractError):
            PROJECT_CONTROL.verify_work(self.root, "project", "another-project", "")

    def test_artifact_tamper_is_rejected_before_staging_and_completion(self):
        packet = self.fixture.validation_packet()
        build_path = Path(packet["build"]["path"])
        build_path.write_bytes(b"tampered build\n")

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "artifact digest mismatch",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-customer",
                json.dumps(packet),
            )

        completion_path = (
            self.root
            / "docs"
            / "temp"
            / "projects"
            / self.fixture.project
            / "completion.json"
        )
        completion_path.write_text(
            json.dumps(
                {
                    "project": self.fixture.project,
                    "contractRevision": self.fixture.contract_revision,
                    "build": packet["build"],
                }
            ),
            encoding="utf-8",
        )
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "artifact digest mismatch",
        ):
            PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

    def test_stale_authority_manifest_is_rejected_before_dispatch(self):
        packet = self.fixture.validation_packet()
        authority_path = (
            self.root
            / "factory"
            / "projects"
            / self.fixture.project
            / "source-plan.md"
        )
        authority_path.write_text(
            authority_path.read_text(encoding="utf-8") + "\nstale edit\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "immutable authority digest mismatch: sourcePlan",
        ):
            PROJECT_CONTROL.verify_work(
                self.root,
                "idea",
                "audio-runtime-c1",
                json.dumps(
                    {
                        "project": self.fixture.project,
                        "contractRevision": self.fixture.contract_revision,
                    }
                ),
            )
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "immutable authority digest mismatch: sourcePlan",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-customer",
                json.dumps(packet),
            )

    def test_prepare_validation_stages_a_valid_immutable_contract(self):
        name = "audio-runtime-c1-customer"
        packet = self.fixture.validation_packet(name)

        result = PREPARE_VALIDATION.prepare(self.root, name, json.dumps(packet))

        target = Path(result["directory"])
        self.assertEqual(result["status"], "ready")
        self.assertEqual(
            stat.S_IMODE(target.stat().st_mode),
            0o700,
        )
        staged_build = Path(result["build"]["path"])
        self.assertTrue(staged_build.is_file())
        self.assertEqual(
            hashlib.sha256(staged_build.read_bytes()).hexdigest(),
            packet["build"]["sha256"],
        )
        self.assertEqual(stat.S_IMODE(staged_build.stat().st_mode), 0o500)
        mission = json.loads((target / "mission.json").read_text(encoding="utf-8"))
        self.assertEqual(mission["project"], self.fixture.project)
        self.assertEqual(
            mission["authority"],
            project_contract.manifest(self.root)["authority"],
        )
        self.assertEqual(mission["scope"], "project")
        self.assertEqual(mission["build"]["path"], str(staged_build))

    def test_prepare_validation_accepts_a_scoped_vertical_probe(self):
        name = "audio-runtime-c1-vertical"
        packet = self.fixture.validation_packet(
            name,
            scope="vertical",
            criterion_ids=["AUDIO"],
            vertical="audio-runtime-c1",
            merged_revision="merge-c1",
        )

        result = PREPARE_VALIDATION.prepare(self.root, name, json.dumps(packet))

        mission = json.loads(
            (Path(result["directory"]) / "mission.json").read_text(encoding="utf-8")
        )
        self.assertEqual(mission["scope"], "vertical")
        self.assertEqual(mission["vertical"], "audio-runtime-c1")
        self.assertEqual(mission["sourceRevision"], "merge-c1")
        self.assertEqual([item["id"] for item in mission["criteria"]], ["AUDIO"])

    def test_vertical_probe_requires_name_and_source_revision(self):
        missing_name = self.fixture.validation_packet(
            "audio-runtime-c1-no-vertical",
            scope="vertical",
            criterion_ids=["AUDIO"],
            source_revision="merge-c1",
        )
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "vertical validation requires one nonempty vertical name",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-no-vertical",
                json.dumps(missing_name),
            )

        missing_revision = self.fixture.validation_packet(
            "audio-runtime-c1-no-revision",
            scope="vertical",
            criterion_ids=["AUDIO"],
            vertical="audio-runtime-c1",
        )
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "vertical validation requires one nonempty source or merged revision",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-no-revision",
                json.dumps(missing_revision),
            )

    def test_project_scope_requires_every_immutable_criterion(self):
        packet = self.fixture.validation_packet(
            "audio-runtime-c1-project-subset",
            scope="project",
            criterion_ids=["AUDIO"],
        )

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "validation criteria must preserve immutable rubrics and every criterion",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-project-subset",
                json.dumps(packet),
            )

    def test_completion_accepts_two_independent_validation_evidence_records(self):
        self.fixture.bind_runtime()
        build = self.fixture.artifact()
        self.fixture.completion_record(build)
        calls = []

        with mock.patch.object(
            PROJECT_CONTROL,
            "completed_validation",
            side_effect=lambda work_id, session_id, server: calls.append(
                (work_id, session_id, server)
            ),
        ):
            result = PROJECT_CONTROL.verify_completion(
                self.root,
                self.fixture.project,
            )

        self.assertEqual(result["status"], "verified")
        self.assertEqual(result["project"], self.fixture.project)
        self.assertEqual(
            calls,
            [
                ("validation-customer", self.fixture.session_id, self.fixture.server),
                (
                    "validation-engineering",
                    self.fixture.session_id,
                    self.fixture.server,
                ),
            ],
        )

    def test_completion_rejects_missing_criterion_evidence(self):
        packet = self.fixture.validation_packet()
        packet["criteria"].pop()
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "validation criteria must preserve immutable rubrics",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c1-missing-criteria",
                json.dumps(packet),
            )

        self.fixture.bind_runtime()
        build = self.fixture.artifact()
        self.fixture.completion_record(build, missing_role="engineering")

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "all immutable criteria need independent PASS evidence",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(
                    self.root,
                    self.fixture.project,
                )

    def test_completion_rejects_vertical_report_even_with_all_criteria(self):
        self.fixture.bind_runtime()
        build = self.fixture.artifact()
        self.fixture.completion_record(build, report_scope="vertical")

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "project completion requires scope=project validation reports",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(
                    self.root,
                    self.fixture.project,
                )

    def test_completion_rejects_reused_validation_work_identity(self):
        self.fixture.bind_runtime()
        build = self.fixture.artifact()
        self.fixture.completion_record(build, same_work_id=True)
        calls = []

        with self.assertRaisesRegex(
            project_contract.ContractError,
            "distinct canonical Work identities",
        ):
            with mock.patch.object(
                PROJECT_CONTROL,
                "completed_validation",
                side_effect=lambda *args: calls.append(args),
            ):
                PROJECT_CONTROL.verify_completion(
                    self.root,
                    self.fixture.project,
                )
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
