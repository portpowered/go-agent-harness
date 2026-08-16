import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
TARGET_COMMAND = ["make", "test-factory-scripts"]
PRIMARY_MODULES = (
    "factory.scripts.tests.test_setup_workspace",
    "factory.scripts.tests.test_validate_worktree_hygiene_convergence",
)


class FactoryScriptTargetTests(unittest.TestCase):
    def test_target_runs_expected_suite_without_creating_bytecode(self):
        before = self._bytecode_artifacts()
        result = self._run_target()
        after = self._bytecode_artifacts()

        self.assertEqual(result.returncode, 0, result.output)
        self.assertIn("==> test-factory-scripts modules:", result.output)
        for module in PRIMARY_MODULES:
            self.assertIn(module, result.output)
        self.assertRegex(result.output, r"Ran 5 tests? in ")
        self.assertEqual(after, before, result.output)

    def test_target_rejects_empty_selection(self):
        result = self._run_target("FACTORY_TEST_MODULES=")

        self.assertNotEqual(result.returncode, 0, result.output)
        self.assertIn(
            "test-factory-scripts selected zero tests from .",
            result.output,
        )

    def test_target_reports_missing_module_load_error(self):
        missing_module = "factory.scripts.tests.test_factory_script_missing"
        result = self._run_target(f"FACTORY_TEST_MODULES={missing_module}")

        self.assertNotEqual(result.returncode, 0, result.output)
        self.assertIn("ModuleNotFoundError", result.output)
        self.assertIn(
            "test-factory-scripts failed while loading or executing",
            result.output,
        )

    def test_target_propagates_failing_fixture(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture_path = Path(temp_dir) / "factory_target_failure_fixture.py"
            fixture_path.write_text(
                "import unittest\n\n"
                "class FactoryTargetFailure(unittest.TestCase):\n"
                "    def test_failure(self):\n"
                "        self.fail('intentional factory target failure')\n",
                encoding="utf-8",
            )
            python_path = os.pathsep.join(
                [temp_dir, os.environ.get("PYTHONPATH", "")]
            ).rstrip(os.pathsep)

            result = self._run_target(
                "FACTORY_TEST_MODULES=factory_target_failure_fixture",
                env={"PYTHONPATH": python_path},
            )

        self.assertNotEqual(result.returncode, 0, result.output)
        self.assertIn("FAIL", result.output)
        self.assertIn("intentional factory target failure", result.output)
        self.assertIn(
            "test-factory-scripts failed while loading or executing",
            result.output,
        )

    def _run_target(self, make_variable=None, env=None):
        command = TARGET_COMMAND.copy()
        if make_variable is not None:
            command.append(make_variable)
        process_env = os.environ.copy()
        process_env["FACTORY_TEST_CONTRACT_CHILD"] = "1"
        if env:
            process_env.update(env)
        result = subprocess.run(
            command,
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            env=process_env,
            check=False,
        )
        return _CommandResult(result.returncode, result.stdout + result.stderr)

    def _bytecode_artifacts(self):
        artifacts = set()
        for path in REPO_ROOT.rglob("__pycache__"):
            if path.is_dir():
                artifacts.add(path.relative_to(REPO_ROOT).as_posix())
        for path in REPO_ROOT.rglob("*.pyc"):
            if path.is_file():
                artifacts.add(path.relative_to(REPO_ROOT).as_posix())
        return artifacts


class _CommandResult:
    def __init__(self, returncode, output):
        self.returncode = returncode
        self.output = output


if __name__ == "__main__":
    unittest.main()
