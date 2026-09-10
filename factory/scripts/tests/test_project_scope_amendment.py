import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPTS_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = SCRIPTS_DIR.parents[1]
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract
import project_scope_amendment as amendments


def _load_script(filename, module_name):
    spec = importlib.util.spec_from_file_location(module_name, SCRIPTS_DIR / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


PROJECT_CONTROL = _load_script("project-control.py", "scope_project_control_tests")
PREPARE_VALIDATION = _load_script(
    "prepare-validation.py",
    "scope_prepare_validation_tests",
)
PROBE = _load_script(
    REPO_ROOT
    / "docs/temp/projects/audio-runtime/audio-runtime-c39-authorized-scope-amendment/probe.py",
    "scope_c39_probe_tests",
)


class AmendmentFixture:
    project = "audio-runtime"
    contract_revision = "audio-runtime-v1"
    source_revision = "c39-fixture-source"

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
        project_destination = self.root / "factory" / "projects" / self.project
        project_destination.parent.mkdir(parents=True)
        shutil.copytree(project_source, project_destination)
        # The candidate checkout carries the reviewed record as a deliverable;
        # each fixture starts before publication so append/collision behavior is
        # exercised rather than bypassed by the checked-in record.
        shutil.rmtree(project_destination / "amendments", ignore_errors=True)
        (self.root / "docs" / "temp" / "projects" / self.project).mkdir(
            parents=True,
        )
        (self.root / "docs" / "temp" / "probes").mkdir(parents=True)
        self._copy_reviewed_inputs()

        self.admission = project_admission.ProjectAdmission(self.root)
        self.admission.Admit(self.project, self.contract_revision)

    @staticmethod
    def _run(*command, **kwargs):
        result = subprocess.run(
            list(command),
            capture_output=True,
            text=True,
            check=False,
            **kwargs,
        )
        if result.returncode:
            raise AssertionError(
                f"{' '.join(command)} failed: {result.stderr.strip()}"
            )
        return result

    def _copy_reviewed_inputs(self):
        configured_root = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT))
        for relative in (
            amendments.AUTHORIZATION_RELATIVE,
            amendments.HISTORICAL_REPORT_RELATIVE,
        ):
            source = configured_root / relative
            if not source.is_file():
                source = REPO_ROOT / relative
            if not source.is_file():
                raise unittest.SkipTest(
                    "reviewed C39 authorization inputs are unavailable"
                )
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)

    def artifact(self, name="build.bin", content=b"c39 build artifact\n"):
        path = self.root / "artifacts" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return {
            "identity": "c39-build-v1",
            "path": str(path),
            "sha256": hashlib.sha256(content).hexdigest(),
        }

    def record_input(self, name="candidate-record.json"):
        path = self.root / name
        path.write_bytes(amendments.canonical_bytes(amendments.create_record(self.root)))
        return path

    def append(self):
        record_path = self.record_input()
        result = self.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(record_path),
        )
        self.assert_cli_ok(result)
        return json.loads(result.stdout)

    def status(self, amendment_id=None):
        arguments = [
            "amendment-status",
            "--root",
            str(self.root),
        ]
        if amendment_id is not None:
            arguments.extend(["--amendment-id", amendment_id])
        result = self.run_cli(SCRIPTS_DIR / "project-control.py", *arguments)
        self.assert_cli_ok(result)
        return json.loads(result.stdout)

    def run_cli(self, script, *arguments):
        environment = os.environ.copy()
        environment.pop("FACTORY_ROOT", None)
        environment.pop("FACTORY_PROJECT_MANIFEST", None)
        return subprocess.run(
            [sys.executable, str(script), "--root", str(self.root), *arguments]
            if script.name == "project-control.py"
            else [sys.executable, str(script), *arguments],
            cwd=self.root,
            env=environment,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )

    @staticmethod
    def assert_cli_ok(result):
        if result.returncode:
            raise AssertionError(
                f"CLI failed ({result.returncode}): {result.stderr.strip()}"
            )

    def bind_runtime(self):
        project_admission.bind_session(
            self.root,
            self.project,
            self.contract_revision,
            "http://fixture.invalid",
            "fixture-session",
        )
        common = project_admission.common_dir(self.root)
        (common / "factory-runtime.json").write_text(
            json.dumps(
                {
                    "project": self.project,
                    "contractRevision": self.contract_revision,
                    "sessionId": "fixture-session",
                    "server": "http://fixture.invalid",
                }
            ),
            encoding="utf-8",
        )

    def protected_hashes(self):
        return {
            relative: hashlib.sha256((self.root / relative).read_bytes()).hexdigest()
            for relative in (
                amendments.MANIFEST_RELATIVE,
                amendments.AUTHORIZATION_RELATIVE,
                amendments.HISTORICAL_REPORT_RELATIVE,
                "factory/projects/audio-runtime/source-plan.md",
                "factory/projects/audio-runtime/request.md",
                "factory/projects/audio-runtime/acceptance.md",
            )
        }

    def prepare_packet(self, role, suffix):
        contract = project_contract.manifest(self.root)
        build = self.artifact(f"{role}-{suffix}.bin")
        report_path = (
            self.root
            / "docs"
            / "temp"
            / "projects"
            / self.project
            / f"{role}-{suffix}.json"
        )
        return {
            "project": self.project,
            "contractRevision": self.contract_revision,
            "scope": "project",
            "role": role,
            "sourceRevision": self.source_revision,
            "criteria": [copy.deepcopy(entry) for entry in contract["criteria"]],
            "budget": {
                "timeSeconds": 30,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Run the fresh amended project-scope controller mission.",
            "reportPath": str(report_path),
            "build": build,
            "fixtures": [],
            "amendment": amendments.amendment_reference(self.root),
        }

    def prepare_reports(self, realtime_sessions=0, realtime_seconds=0):
        reports = {}
        self.prepared_results = {}
        build = self.artifact("shared-build.bin")
        for role in ("customer", "engineering"):
            packet = self.prepare_packet(role, role)
            packet["build"] = build
            packet["budget"]["realtimeSessions"] = realtime_sessions
            packet["budget"]["realtimeSeconds"] = realtime_seconds
            result = PREPARE_VALIDATION.prepare(
                self.root,
                f"audio-runtime-c39-{role}-mission",
                json.dumps(packet),
            )
            self.prepared_results[role] = result
            mission_path = Path(result["directory"]) / "mission.json"
            mission = json.loads(mission_path.read_text(encoding="utf-8"))
            criteria = {
                entry["id"]: {
                    "rubric": entry["rubric"],
                    "verdict": "PASS",
                    "evidence": (
                        "Fresh retained software evidence for "
                        f"{entry['id']}; authorized physical subproof is OUT OF SCOPE."
                    ),
                }
                for entry in mission["criteria"]
            }
            report = {
                "project": self.project,
                "contractRevision": self.contract_revision,
                "scope": "project",
                "role": role,
                "sourceRevision": mission["sourceRevision"],
                "validationWorkName": mission["validationWorkName"],
                "validationWorkId": f"fixture-validation-{role}",
                "build": build,
                "amendment": mission["amendment"],
                "missionPath": str(mission_path),
                "missionSha256": result["missionSha256"],
                "criteria": criteria,
                "scopeEvidence": {
                    "excludedSubproof": mission["amendment"]["excludedSubproof"],
                    "historicalReport": mission["amendment"]["historicalReport"],
                    "retained": {
                        key: {
                            "verdict": "PASS",
                            "evidence": f"Fresh {key} evidence.",
                        }
                        for key in amendments.RETAINED_EVIDENCE_KEYS
                    },
                },
            }
            report_path = Path(packet["reportPath"])
            report_path.write_text(json.dumps(report), encoding="utf-8")
            reports[role] = report_path
        return build, reports

    def completion(self, build, reports):
        path = self.root / "docs" / "temp" / "projects" / self.project / "completion.json"
        path.write_text(
            json.dumps(
                {
                    "project": self.project,
                    "contractRevision": self.contract_revision,
                    "build": build,
                    "amendment": amendments.amendment_reference(self.root),
                    "reports": {role: str(path) for role, path in reports.items()},
                }
            ),
            encoding="utf-8",
        )
        return path


class ScopeAmendmentTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.fixture = AmendmentFixture(self.temp_dir.name)
        self.root = self.fixture.root

    def test_cli_append_status_is_idempotent_and_preserves_protected_bytes(self):
        before = self.fixture.protected_hashes()
        admission_before = project_admission.status(self.root)
        first = self.fixture.append()
        stored = self.root / amendments.AMENDMENTS_RELATIVE / f"{amendments.AMENDMENT_ID}.json"
        stored_bytes = stored.read_bytes()

        self.assertEqual(first["status"], "appended")
        self.assertEqual(first["path"], f"{amendments.AMENDMENTS_RELATIVE}/{amendments.AMENDMENT_ID}.json")
        self.assertEqual(first["contentSha256"], hashlib.sha256(stored_bytes).hexdigest())
        self.assertEqual(self.fixture.status()["status"], "present")
        self.assertEqual(
            self.fixture.status(amendments.AMENDMENT_ID)["amendment"]["contentSha256"],
            first["contentSha256"],
        )

        second = self.fixture.append()
        self.assertEqual(second["status"], "already-present")
        self.assertEqual(stored_bytes, stored.read_bytes())
        self.assertEqual(before, self.fixture.protected_hashes())
        self.assertEqual(admission_before, project_admission.status(self.root))
        self.assertFalse((self.root / "factory-runtime.json").exists())

    def test_forged_scope_authority_and_budget_inputs_fail_without_append(self):
        base = amendments.create_record(self.root)
        cases = {
            "project": lambda record: record.update(project="other-project"),
            "contract": lambda record: record.update(contractRevision="audio-runtime-v0"),
            "exclusion": lambda record: record["excludedSubproof"].append(
                {
                    "criterionId": "DEVICE",
                    "subproof": "all device consumption",
                    "verdict": "OUT_OF_SCOPE",
                }
            ),
            "criterion": lambda record: record["criteria"][0].update(rubric="weakened"),
            "budget": lambda record: record["realtimeBudget"].update(sessionsPerMission=4),
            "authorization": lambda record: record["authorization"].update(
                sha256="0" * 64
            ),
            "authority": lambda record: record["authority"]["request"].update(
                sha256="0" * 64
            ),
            "unknown": lambda record: record.update(unexpected="waiver"),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                record = copy.deepcopy(base)
                mutate(record)
                candidate = self.root / f"{name}.json"
                candidate.write_bytes(amendments.canonical_bytes(record))
                result = self.fixture.run_cli(
                    SCRIPTS_DIR / "project-control.py",
                    "amendment-append",
                    "--record",
                    str(candidate),
                )
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse(
                    (
                        self.root
                        / amendments.AMENDMENTS_RELATIVE
                        / f"{amendments.AMENDMENT_ID}.json"
                    ).exists()
                )

    def test_unsafe_record_and_anchor_inputs_fail_closed(self):
        valid = amendments.create_record(self.root)
        candidate = self.root / "candidate.json"
        candidate.write_bytes(amendments.canonical_bytes(valid))

        escaped = copy.deepcopy(valid)
        escaped["authorization"]["path"] = "../outside.json"
        escaped_path = self.root / "escaped.json"
        escaped_path.write_bytes(amendments.canonical_bytes(escaped))
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(escaped_path),
        )
        self.assertNotEqual(result.returncode, 0)

        symlink_path = self.root / "record-symlink.json"
        symlink_path.symlink_to(candidate)
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(symlink_path),
        )
        self.assertNotEqual(result.returncode, 0)

        anchor = self.root / amendments.AUTHORIZATION_RELATIVE
        anchor.unlink()
        anchor.symlink_to(Path(os.environ["FACTORY_ROOT"]) / amendments.AUTHORIZATION_RELATIVE)
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(candidate),
        )
        self.assertNotEqual(result.returncode, 0)

    def test_oversized_and_duplicate_json_records_are_rejected(self):
        oversized = self.root / "oversized.json"
        oversized.write_bytes(b"{" + b'"x":1,' * (amendments.MAX_JSON_BYTES // 4))
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(oversized),
        )
        self.assertNotEqual(result.returncode, 0)

        duplicate = self.root / "duplicate.json"
        duplicate.write_text('{"schema":"one","schema":"two"}', encoding="utf-8")
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(duplicate),
        )
        self.assertNotEqual(result.returncode, 0)

    def test_collision_cannot_replace_the_published_record(self):
        self.fixture.append()
        stored = self.root / amendments.AMENDMENTS_RELATIVE / f"{amendments.AMENDMENT_ID}.json"
        before = stored.read_bytes()
        forged = amendments.create_record(self.root)
        forged["retainedProof"] = list(reversed(forged["retainedProof"]))
        candidate = self.root / "conflict.json"
        candidate.write_bytes(amendments.canonical_bytes(forged))
        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-append",
            "--record",
            str(candidate),
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, stored.read_bytes())

    def test_amended_missions_and_reports_bind_exact_staged_identity(self):
        self.fixture.append()
        build, reports = self.fixture.prepare_reports()
        amendment = amendments.amendment_reference(self.root)
        for role, report_path in reports.items():
            prepared = self.fixture.prepared_results[role]
            self.assertEqual(prepared["project"], self.fixture.project)
            self.assertEqual(prepared["buildIdentity"], build["identity"])
            self.assertEqual(prepared["build"]["identity"], build["identity"])
            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(report["amendment"], amendment)
            self.assertEqual(report["build"], build)
            mission = json.loads(Path(report["missionPath"]).read_text(encoding="utf-8"))
            self.assertEqual(mission["amendment"], amendment)
            self.assertEqual(report["missionSha256"], hashlib.sha256(Path(report["missionPath"]).read_bytes()).hexdigest())
            self.assertEqual(report["validationWorkName"], mission["validationWorkName"])

        self.fixture.bind_runtime()
        self.fixture.completion(build, reports)
        with mock.patch.object(
            PROJECT_CONTROL,
            "completed_validation",
            side_effect=lambda *args, **kwargs: None,
        ):
            result = PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)
        self.assertEqual(result["status"], "verified")

    def test_amended_completion_preserves_original_realtime_maxima(self):
        self.fixture.append()
        build, reports = self.fixture.prepare_reports(
            realtime_sessions=3,
            realtime_seconds=120,
        )
        self.fixture.bind_runtime()
        self.fixture.completion(build, reports)
        with mock.patch.object(
            PROJECT_CONTROL,
            "completed_validation",
            side_effect=lambda *args, **kwargs: None,
        ):
            result = PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)
        self.assertEqual(result["status"], "verified")

    def test_amended_preparation_rejects_alternate_manifest_criteria(self):
        self.fixture.append()
        contract = project_contract.manifest(self.root)
        alternate = self.root / "alternate-manifest.json"
        alternate.write_text(
            json.dumps(
                {
                    "version": contract["version"],
                    "project": contract["project"],
                    "contractRevision": contract["contractRevision"],
                    "authority": contract["authority"],
                    "criteria": [copy.deepcopy(contract["criteria"][0])],
                    "realtimeBudget": contract["realtimeBudget"],
                }
            ),
            encoding="utf-8",
        )
        packet = self.fixture.prepare_packet("engineering", "alternate-manifest")
        packet["criteria"] = [copy.deepcopy(contract["criteria"][0])]

        with mock.patch.dict(
            os.environ,
            {"FACTORY_PROJECT_MANIFEST": str(alternate)},
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "alternate project manifest override is not permitted",
            ):
                PREPARE_VALIDATION.prepare(
                    self.root,
                    "audio-runtime-c39-alternate-manifest-prepare",
                    json.dumps(packet),
                )

    def test_amended_completion_rejects_alternate_manifest_criteria(self):
        self.fixture.append()
        build, reports = self.fixture.prepare_reports()
        for report_path in reports.values():
            report = json.loads(report_path.read_text(encoding="utf-8"))
            report["criteria"] = {"AUDIO": report["criteria"]["AUDIO"]}
            report_path.write_text(json.dumps(report), encoding="utf-8")
        self.fixture.bind_runtime()
        self.fixture.completion(build, reports)

        contract = project_contract.manifest(self.root)
        alternate = self.root / "alternate-completion-manifest.json"
        alternate.write_text(
            json.dumps(
                {
                    "version": contract["version"],
                    "project": contract["project"],
                    "contractRevision": contract["contractRevision"],
                    "authority": contract["authority"],
                    "criteria": [copy.deepcopy(contract["criteria"][0])],
                    "realtimeBudget": contract["realtimeBudget"],
                }
            ),
            encoding="utf-8",
        )
        with mock.patch.dict(
            os.environ,
            {"FACTORY_PROJECT_MANIFEST": str(alternate)},
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "alternate project manifest override is not permitted",
            ):
                with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                    PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

    def test_present_null_amendment_is_rejected(self):
        self.fixture.append()
        packet = self.fixture.prepare_packet("engineering", "null-amendment")
        packet["amendment"] = None
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "amendment reference must be an object",
        ):
            PREPARE_VALIDATION.prepare(
                self.root,
                "audio-runtime-c39-null-amendment",
                json.dumps(packet),
            )

        self.fixture.bind_runtime()
        build, reports = self.fixture.prepare_reports()
        completion = self.fixture.completion(build, reports)
        record = json.loads(completion.read_text(encoding="utf-8"))
        record["amendment"] = None
        completion.write_text(json.dumps(record), encoding="utf-8")
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "amendment reference must be an object",
        ):
            PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

    def test_completed_validation_rejects_unrelated_work_name_and_mission(self):
        def response(work):
            return subprocess.CompletedProcess(
                args=["you"],
                returncode=0,
                stdout=json.dumps(work),
                stderr="",
            )

        expected = {
            "workId": "validation-customer",
            "name": "audio-runtime-c39-customer-mission",
            "project": "audio-runtime",
            "workTypeName": "validation",
            "state": {"name": "complete"},
            "tags": {
                "_last_output": json.dumps(
                    {
                        "project": "audio-runtime",
                        "directory": "/tmp/c39-mission",
                        "validationWorkName": "audio-runtime-c39-customer-mission",
                        "missionSha256": "mission-sha",
                        "build": {
                            "identity": "build-v1",
                            "sha256": "artifact-sha",
                        },
                    }
                )
            },
        }
        with mock.patch.object(
            PROJECT_CONTROL.subprocess,
            "run",
            return_value=response({**expected, "name": "audio-runtime-c39-other-mission"}),
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "canonical validation Work name mismatch",
            ):
                PROJECT_CONTROL.completed_validation(
                    "validation-customer",
                    "session",
                    "http://fixture.invalid",
                    project="audio-runtime",
                    work_name="audio-runtime-c39-customer-mission",
                    mission_path="/tmp/c39-mission/mission.json",
                    mission_sha256="mission-sha",
                    artifact_sha256="artifact-sha",
                    artifact_identity="build-v1",
                )

        wrong_project = copy.deepcopy(expected)
        wrong_project["project"] = "other-project"
        with mock.patch.object(
            PROJECT_CONTROL.subprocess,
            "run",
            return_value=response(wrong_project),
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "canonical validation Work project mismatch",
            ):
                PROJECT_CONTROL.completed_validation(
                    "validation-customer",
                    "session",
                    "http://fixture.invalid",
                    project="audio-runtime",
                    work_name="audio-runtime-c39-customer-mission",
                    mission_path="/tmp/c39-mission/mission.json",
                    mission_sha256="mission-sha",
                    artifact_sha256="artifact-sha",
                    artifact_identity="build-v1",
                )

        wrong_mission = copy.deepcopy(expected)
        wrong_mission["tags"]["_last_output"] = json.dumps(
            {
                        "project": "audio-runtime",
                        "directory": "/tmp/c39-mission",
                        "validationWorkName": "audio-runtime-c39-customer-mission",
                        "missionSha256": "wrong-mission-sha",
                        "build": {
                            "identity": "build-v1",
                            "sha256": "artifact-sha",
                        },
                    }
                )
        with mock.patch.object(
            PROJECT_CONTROL.subprocess,
            "run",
            return_value=response(wrong_mission),
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "canonical validation Work mission digest mismatch",
            ):
                PROJECT_CONTROL.completed_validation(
                    "validation-customer",
                    "session",
                    "http://fixture.invalid",
                    project="audio-runtime",
                    work_name="audio-runtime-c39-customer-mission",
                    mission_path="/tmp/c39-mission/mission.json",
                    mission_sha256="mission-sha",
                    artifact_sha256="artifact-sha",
                    artifact_identity="build-v1",
                )

        missing_project = copy.deepcopy(expected)
        del missing_project["project"]
        with mock.patch.object(
            PROJECT_CONTROL.subprocess,
            "run",
            return_value=response(missing_project),
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "canonical validation Work project mismatch",
            ):
                PROJECT_CONTROL.completed_validation(
                    "validation-customer",
                    "session",
                    "http://fixture.invalid",
                    project="audio-runtime",
                    work_name="audio-runtime-c39-customer-mission",
                    mission_path="/tmp/c39-mission/mission.json",
                    mission_sha256="mission-sha",
                    artifact_sha256="artifact-sha",
                    artifact_identity="build-v1",
                )

        mismatched_artifact = copy.deepcopy(expected)
        staged = json.loads(mismatched_artifact["tags"]["_last_output"])
        staged["build"]["identity"] = "different-build"
        mismatched_artifact["tags"]["_last_output"] = json.dumps(staged)
        with mock.patch.object(
            PROJECT_CONTROL.subprocess,
            "run",
            return_value=response(mismatched_artifact),
        ):
            with self.assertRaisesRegex(
                project_contract.ContractError,
                "canonical validation Work artifact mismatch",
            ):
                PROJECT_CONTROL.completed_validation(
                    "validation-customer",
                    "session",
                    "http://fixture.invalid",
                    project="audio-runtime",
                    work_name="audio-runtime-c39-customer-mission",
                    mission_path="/tmp/c39-mission/mission.json",
                    mission_sha256="mission-sha",
                    artifact_sha256="artifact-sha",
                    artifact_identity="build-v1",
                )

    def test_published_amendment_must_retain_canonical_bytes(self):
        self.fixture.append()
        stored = (
            self.root
            / amendments.AMENDMENTS_RELATIVE
            / f"{amendments.AMENDMENT_ID}.json"
        )
        original = stored.read_bytes()
        stored.chmod(0o600)
        stored.write_bytes(original + b" \n")
        stored.chmod(0o400)

        result = self.fixture.run_cli(
            SCRIPTS_DIR / "project-control.py",
            "amendment-status",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("amendment record is not canonical", result.stderr)

    def test_probe_bounded_output_and_failure_evidence(self):
        yui = Path(self.temp_dir.name) / "controller-only-yui"
        yui.write_bytes(b"controller-only executable placeholder")
        provenance = PROBE._input_provenance("c39-test-source", yui, [])
        self.assertEqual(provenance["replayInputs"], [])

        result = PROBE.run_bounded(
            [
                sys.executable,
                "-c",
                "import sys; sys.stdout.write('o' * 100000); sys.stderr.write('e' * 100000)",
            ],
            timeout=10,
        )
        self.assertEqual(result["exitCode"], 0)
        self.assertEqual(result["stdoutBytesRetained"], PROBE.MAX_OUTPUT_BYTES)
        self.assertEqual(result["stderrBytesRetained"], PROBE.MAX_OUTPUT_BYTES)
        self.assertTrue(result["stdoutTruncated"])
        self.assertTrue(result["stderrTruncated"])

        output = Path(self.temp_dir.name) / "failed-probe"
        failed = subprocess.run(
            [
                sys.executable,
                str(REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c39-authorized-scope-amendment/probe.py"),
                "--source-revision",
                "not-the-current-revision",
                "--yui",
                str(REPO_ROOT / "missing-yui"),
                "--output",
                str(output),
            ],
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertNotEqual(failed.returncode, 0)
        report = json.loads((output / "probe-report.json").read_text(encoding="utf-8"))
        self.assertEqual(report["status"], "FAILED")
        self.assertIn("source revision mismatch", report["error"])

    def test_amended_completion_rejects_stale_or_incomplete_identity(self):
        self.fixture.append()
        build, reports = self.fixture.prepare_reports()
        completion = self.fixture.completion(build, reports)
        self.fixture.bind_runtime()
        report_path = reports["engineering"]
        original = json.loads(report_path.read_text(encoding="utf-8"))

        report = copy.deepcopy(original)
        report["scope"] = "vertical"
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "amended completion requires explicit project-scope reports",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

        report = copy.deepcopy(original)
        del report["scopeEvidence"]["retained"]["hermeticCaptureEnergyCodec"]
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "missing retained software/device proof",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

        report = copy.deepcopy(original)
        report["criteria"]["AUDIO"]["rubric"] = "weakened"
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "incomplete criterion evidence",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)

        report_path.write_text(json.dumps(original), encoding="utf-8")
        mission_path = Path(original["missionPath"])
        mission_bytes = mission_path.read_bytes()
        os.chmod(mission_path, 0o600)
        mission_path.write_bytes(mission_bytes + b" ")
        os.chmod(mission_path, 0o400)
        with self.assertRaisesRegex(
            project_contract.ContractError,
            "staged mission digest mismatch",
        ):
            with mock.patch.object(PROJECT_CONTROL, "completed_validation"):
                PROJECT_CONTROL.verify_completion(self.root, self.fixture.project)
        self.assertTrue(completion.is_file())

    def test_missing_amendment_keeps_legacy_validation_behavior(self):
        contract = project_contract.manifest(self.root)
        build = self.fixture.artifact()
        packet = {
            "project": self.fixture.project,
            "contractRevision": self.fixture.contract_revision,
            "role": "engineering",
            "criteria": [copy.deepcopy(entry) for entry in contract["criteria"]],
            "budget": {
                "timeSeconds": 30,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Legacy fixture mission.",
            "reportPath": str(
                self.root
                / "docs"
                / "temp"
                / "projects"
                / self.fixture.project
                / "legacy.json"
            ),
            "build": build,
            "fixtures": [],
        }
        result = PREPARE_VALIDATION.prepare(
            self.root,
            "audio-runtime-c39-legacy-mission",
            json.dumps(packet),
        )
        mission = json.loads(
            (Path(result["directory"]) / "mission.json").read_text(encoding="utf-8")
        )
        self.assertNotIn("amendment", mission)
        self.assertNotIn("manifestSha256", mission)


if __name__ == "__main__":
    unittest.main()
