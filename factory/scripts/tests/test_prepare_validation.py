"""Focused C62 successor-contract and pre-staging validation controls."""

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
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract
import project_scope_amendment as amendment


def _load_script(filename, module_name):
    spec = importlib.util.spec_from_file_location(module_name, SCRIPTS_DIR / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


PREPARE_VALIDATION = _load_script(
    "prepare-validation.py",
    "c62_prepare_validation_tests",
)
PROJECT_CONTROL = _load_script(
    "project-control.py",
    "c62_project_control_tests",
)


class PreStagingBoundary(Exception):
    """Raised by the test before prepare-validation creates a probe directory."""


class SuccessorFixture:
    """Copy the admitted successor inputs into a disposable same-project root."""

    project = "audio-runtime"
    contract_revision = "audio-runtime-v1"

    def __init__(self, temporary_directory):
        self.source_root = FACTORY_ROOT
        self.root = (Path(temporary_directory) / "repo").resolve()
        self.root.mkdir()
        self._run("git", "init", "-q", "-b", "main", str(self.root))
        self._run("git", "-C", str(self.root), "config", "user.name", "C62 Tests")
        self._run(
            "git",
            "-C",
            str(self.root),
            "config",
            "user.email",
            "c62-tests@example.com",
        )

        source_project = self.source_root / "factory" / "projects" / self.project
        if not source_project.is_dir():
            raise AssertionError("admitted audio-runtime project inputs are unavailable")
        destination_project = self.root / "factory" / "projects" / self.project
        destination_project.parent.mkdir(parents=True)
        shutil.copytree(source_project, destination_project)

        for relative in (
            amendment.AUTHORIZATION_RELATIVE,
            amendment.HISTORICAL_REPORT_RELATIVE,
        ):
            source = self.source_root / relative
            if not source.is_file():
                raise AssertionError("admitted amendment input is unavailable: " + relative)
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)

        (self.root / "docs" / "temp" / "projects" / self.project).mkdir(
            parents=True,
            exist_ok=True,
        )
        (self.root / "docs" / "temp" / "probes").mkdir(
            parents=True,
            exist_ok=True,
        )
        self.manifest_path = self.root / amendment.MANIFEST_RELATIVE
        self.batch_path = (
            self.source_root
            / "docs/temp/projects/audio-runtime/c47-c55-vertical-probes-batch.json"
        )
        if not self.batch_path.is_file():
            raise AssertionError("prepared C47/C54/C55 packet batch is unavailable")
        if amendment._digest(self.manifest_path) != amendment.TRUSTED_SUCCESSOR_MANIFEST_SHA256:
            raise AssertionError("fixture did not copy the admitted strengthened manifest")

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

    def environment(self):
        environment = os.environ.copy()
        environment["FACTORY_ROOT"] = str(self.root)
        environment["FACTORY_PROJECT_MANIFEST"] = str(self.manifest_path)
        environment["PYTHONPATH"] = os.pathsep.join(
            value
            for value in (str(SCRIPTS_DIR), environment.get("PYTHONPATH"))
            if value
        )
        return environment

    def batch(self):
        return json.loads(self.batch_path.read_text(encoding="utf-8"))

    def packet(self, item):
        packet = copy.deepcopy(item["payload"])
        # The production packet shape and all immutable fields stay intact; only
        # the fresh report destination is relocated into this isolated root.
        packet["reportPath"] = str(
            self.root
            / "docs/temp/projects/audio-runtime"
            / Path(packet["reportPath"]).name
        )
        return packet

    def report_path(self, packet):
        return Path(packet["reportPath"])

    def target(self, name):
        return self.root / "docs/temp/probes" / name

    def source_snapshot(self):
        paths = [self.batch_path]
        for item in self.batch()["works"]:
            paths.append(Path(item["payload"]["build"]["path"]))
            paths.extend(Path(fixture["path"]) for fixture in item["payload"]["fixtures"])
            paths.append(Path(item["payload"]["reportPath"]))
        snapshot = {}
        for path in paths:
            snapshot[str(path)] = {
                "exists": path.exists(),
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest()
                if path.is_file()
                else None,
            }
        return snapshot


class PrepareValidationSuccessorTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory(prefix="c62-validation-tests-")
        self.addCleanup(self.temp_dir.cleanup)
        self.fixture = SuccessorFixture(self.temp_dir.name)

    def _with_fixture_environment(self):
        return mock.patch.dict(os.environ, self.fixture.environment())

    def _prepare_failure(self, packet, suffix, expected):
        name = "audio-runtime-c62-negative-" + suffix
        target = self.fixture.target(name)
        self.assertFalse(target.exists())
        with self._with_fixture_environment():
            with self.assertRaisesRegex(project_contract.ContractError, expected):
                PREPARE_VALIDATION.prepare(
                    self.fixture.root,
                    name,
                    json.dumps(packet),
                )
        self.assertFalse(target.exists())

    def test_successor_contract_preserves_original_amendment_provenance(self):
        with self._with_fixture_environment():
            contract = amendment.admitted_contract(self.fixture.root)
            self.assertEqual(contract["project"], self.fixture.project)
            self.assertEqual(contract["contractRevision"], self.fixture.contract_revision)
            self.assertEqual(contract["criteria"], amendment.TRUSTED_SUCCESSOR_CRITERIA)
            self.assertEqual(contract["authority"], amendment.TRUSTED_SUCCESSOR_AUTHORITY)
            self.assertEqual(
                contract["criteria"][3]["rubric"],
                amendment.SUCCESSOR_SERVICE_RUBRIC,
            )

            record = amendment.validate_record(
                self.fixture.root,
                "factory/projects/audio-runtime/amendments/"
                "user-windows-hardware-scope-20260910.json",
            )
            self.assertEqual(
                record["record"]["manifest"]["sha256"],
                amendment.TRUSTED_ORIGINAL_MANIFEST_SHA256,
            )
            self.assertEqual(
                record["record"]["criteria"],
                amendment.TRUSTED_ORIGINAL_CRITERIA,
            )
            self.assertEqual(
                record["record"]["historicalReport"]["decision"],
                "FAILED",
            )
            self.assertEqual(
                record["record"]["historicalReport"]["deviceVerdict"],
                "BLOCKED",
            )
            self.assertEqual(
                amendment.canonical_bytes(amendment.create_record(self.fixture.root)),
                record["publishedBytes"],
            )

            result = PROJECT_CONTROL.verify_work(
                self.fixture.root,
                "project",
                self.fixture.project,
                json.dumps(
                    {
                        "project": self.fixture.project,
                        "contractRevision": self.fixture.contract_revision,
                    }
                ),
            )
            self.assertEqual(result["status"], "admitted")

    def test_exact_prepared_c47_c54_c55_packets_stop_before_staging(self):
        before = self.fixture.source_snapshot()
        original_mkdir = Path.mkdir

        for item in self.fixture.batch()["works"]:
            packet = self.fixture.packet(item)
            name = item["name"] + "-c62-preflight"
            target = self.fixture.target(name)
            self.assertFalse(target.exists())

            def guarded_mkdir(path, *args, **kwargs):
                if path == target:
                    raise PreStagingBoundary
                return original_mkdir(path, *args, **kwargs)

            with self._with_fixture_environment():
                with mock.patch.object(Path, "mkdir", new=guarded_mkdir):
                    with mock.patch.object(
                        PREPARE_VALIDATION.shutil,
                        "copy2",
                        side_effect=AssertionError("artifact staging was reached"),
                    ):
                        with self.assertRaises(PreStagingBoundary):
                            PREPARE_VALIDATION.prepare(
                                self.fixture.root,
                                name,
                                json.dumps(packet),
                            )
            self.assertFalse(target.exists())
            self.assertFalse(self.fixture.report_path(packet).exists())

        self.assertEqual(before, self.fixture.source_snapshot())

    def test_packet_contract_mutations_fail_closed_before_staging(self):
        item = self.fixture.batch()["works"][0]
        base = self.fixture.packet(item)

        weakened = copy.deepcopy(base)
        weakened["criteria"][1]["rubric"] = "weakened"
        self._prepare_failure(weakened, "weakened-service", "immutable rubrics")

        removed = copy.deepcopy(base)
        removed["scope"] = "project"
        removed["criteria"] = copy.deepcopy(amendment.TRUSTED_SUCCESSOR_CRITERIA)
        removed["criteria"].pop()
        self._prepare_failure(removed, "removed-criterion", "every criterion")

        wrong_project = copy.deepcopy(base)
        wrong_project["project"] = "other-project"
        self._prepare_failure(wrong_project, "other-project", "conflicting project")

        excessive_budget = copy.deepcopy(base)
        excessive_budget["budget"]["realtimeSeconds"] = 121
        self._prepare_failure(excessive_budget, "realtime-budget", "excessive realtimeSeconds")

        wrong_build = copy.deepcopy(base)
        wrong_build["build"]["sha256"] = "0" * 64
        self._prepare_failure(wrong_build, "artifact-digest", "artifact digest mismatch")

        wrong_fixture = copy.deepcopy(base)
        wrong_fixture["fixtures"][0]["sha256"] = "0" * 64
        self._prepare_failure(
            wrong_fixture,
            "fixture-digest",
            "artifact digest mismatch",
        )

        outside_report = copy.deepcopy(base)
        outside_report["reportPath"] = str(self.fixture.root / "outside.json")
        self._prepare_failure(
            outside_report,
            "outside-report",
            "fresh JSON path under the project evidence directory",
        )

    def test_manifest_override_and_authority_mutations_fail_closed(self):
        alternate = self.fixture.root / "alternate-manifest.json"
        shutil.copy2(self.fixture.manifest_path, alternate)
        with self._with_fixture_environment():
            with mock.patch.dict(
                os.environ,
                {"FACTORY_PROJECT_MANIFEST": str(alternate)},
            ):
                with self.assertRaisesRegex(
                    amendment.ScopeAmendmentError,
                    "alternate project manifest override is not permitted",
                ):
                    amendment.admitted_contract(self.fixture.root)

            original_manifest = self.fixture.manifest_path.read_bytes()
            self.fixture.manifest_path.write_bytes(original_manifest + b"\n")
            with self.assertRaisesRegex(
                amendment.ScopeAmendmentError,
                "(alternate project manifest override|original anchor or reviewed successor)",
            ):
                amendment.admitted_contract(self.fixture.root)
            self.fixture.manifest_path.write_bytes(original_manifest)

            reordered_manifest = json.loads(original_manifest)
            reordered_manifest["criteria"] = list(
                reversed(reordered_manifest["criteria"])
            )
            self.fixture.manifest_path.write_text(
                json.dumps(reordered_manifest) + "\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(
                amendment.ScopeAmendmentError,
                "(alternate project manifest override|original anchor or reviewed successor)",
            ):
                amendment.admitted_contract(self.fixture.root)
            self.fixture.manifest_path.write_bytes(original_manifest)

            acceptance = self.fixture.root / "factory/projects/audio-runtime/acceptance.md"
            original_acceptance = acceptance.read_bytes()
            acceptance.write_bytes(original_acceptance + b"\n")
            with self.assertRaisesRegex(
                amendment.ScopeAmendmentError,
                "immutable authority digest mismatch: acceptance",
            ):
                amendment.admitted_contract(self.fixture.root)
            acceptance.write_bytes(original_acceptance)

            record_path = self.fixture.root / (
                "factory/projects/audio-runtime/amendments/"
                "user-windows-hardware-scope-20260910.json"
            )
            original_record = record_path.read_bytes()
            record_path.chmod(0o600)
            record = json.loads(original_record)
            record["historicalReport"]["decision"] = "PASS"
            record_path.write_bytes(amendment.canonical_bytes(record))
            with self.assertRaisesRegex(
                amendment.ScopeAmendmentError,
                "preserve the FAILED C32 report",
            ):
                amendment.amendment_reference(self.fixture.root)
            record_path.write_bytes(original_record)

    def test_project_scope_amendment_reference_rejects_historical_relabel(self):
        packet = self.fixture.packet(self.fixture.batch()["works"][0])
        packet["scope"] = "project"
        packet["criteria"] = copy.deepcopy(amendment.TRUSTED_SUCCESSOR_CRITERIA)
        with self._with_fixture_environment():
            packet["amendment"] = amendment.amendment_reference(self.fixture.root)

        authority_mutation = copy.deepcopy(packet)
        authority_mutation["amendment"]["authority"]["acceptance"]["sha256"] = "0" * 64
        self._prepare_failure(
            authority_mutation,
            "authority-mutation",
            "amendment reference content or provenance mismatch",
        )

        historical_mutation = copy.deepcopy(packet)
        historical_mutation["amendment"]["historicalReport"]["deviceVerdict"] = "PASS"
        self._prepare_failure(
            historical_mutation,
            "historical-relabel",
            "amendment reference content or provenance mismatch",
        )


if __name__ == "__main__":
    unittest.main()
