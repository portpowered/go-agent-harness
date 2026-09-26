import os
import re
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
    def test_default_selection_includes_the_primary_modules(self):
        # The default suite itself is executed by the enclosing
        # `make test-factory-scripts` run, which fails on zero tests or any
        # failure; re-running it here would only double the target's time.
        modules = self._default_modules()
        for module in PRIMARY_MODULES:
            self.assertIn(module, modules)

    def test_target_runs_selected_modules_without_creating_bytecode(self):
        # The fixture imports every default module, so any bytecode the
        # target's interpreters would write for repository sources (the
        # modules under test and the scripts they load) appears here.
        modules = self._default_modules()
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture_path = Path(temp_dir) / "factory_target_import_fixture.py"
            fixture_path.write_text(
                "import importlib\n"
                "import unittest\n\n"
                f"for name in {modules!r}:\n"
                "    importlib.import_module(name)\n\n\n"
                "class FactoryTargetImports(unittest.TestCase):\n"
                "    def test_imports(self):\n"
                "        pass\n",
                encoding="utf-8",
            )
            python_path = os.pathsep.join(
                [temp_dir, os.environ.get("PYTHONPATH", "")]
            ).rstrip(os.pathsep)
            before = self._bytecode_artifacts()
            result = self._run_target(
                "FACTORY_TEST_MODULES=factory_target_import_fixture",
                env={"PYTHONPATH": python_path},
            )
            after = self._bytecode_artifacts()

        self.assertEqual(result.returncode, 0, result.output)
        self.assertIn("==> test-factory-scripts modules:", result.output)
        self.assertRegex(result.output, r"Ran 1 test in ")
        self.assertEqual(after, before, result.output)

    def test_target_fails_when_a_spawned_interpreter_writes_bytecode(self):
        # The recipe snapshots bytecode across the checkout around the real
        # suite run, so bytecode written by a subprocess a test spawns (not
        # only by the test interpreters) fails the target.
        scripts_dir = REPO_ROOT / "factory" / "scripts"
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture_path = Path(temp_dir) / "factory_target_spawn_fixture.py"
            fixture_path.write_text(
                "import os\n"
                "import subprocess\n"
                "import sys\n"
                "import unittest\n\n\n"
                "class FactoryTargetSpawn(unittest.TestCase):\n"
                "    def test_spawn(self):\n"
                "        env = {k: v for k, v in os.environ.items() if k != 'PYTHONDONTWRITEBYTECODE'}\n"
                f"        env['PYTHONPATH'] = {str(scripts_dir)!r}\n"
                "        subprocess.run([sys.executable, '-c', 'import project_contract'], env=env, check=True)\n",
                encoding="utf-8",
            )
            before = self._bytecode_artifacts()
            try:
                result = self._run_target(
                    "FACTORY_TEST_MODULES=factory_target_spawn_fixture",
                    env={"PYTHONPATH": temp_dir},
                )
                written = self._bytecode_artifacts() - before
            finally:
                self._remove_artifacts(self._bytecode_artifacts() - before)

        self.assertNotEqual(result.returncode, 0, result.output)
        self.assertIn("the tests wrote Python bytecode", result.output)
        self.assertIn("factory/scripts/__pycache__", result.output)
        self.assertIn("factory/scripts/__pycache__", written)

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

    @staticmethod
    def _default_modules():
        # `make -n` expands the recipe, including the configured module
        # list, without running it.
        result = subprocess.run(
            ["make", "-n", "test-factory-scripts", "FACTORY_TEST_CONTRACT_CHILD=1"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        match = re.search(r'==> test-factory-scripts modules: ([^"]*)"', result.stdout)
        if match is None:
            raise AssertionError(f"module list not found in dry run:\n{result.stdout}")
        return match.group(1).split()

    @staticmethod
    def _remove_artifacts(artifacts):
        # Files first, then the directories that held only them.
        for relative in sorted(artifacts, key=len, reverse=True):
            path = REPO_ROOT / relative
            if path.is_file():
                path.unlink()
            elif path.is_dir() and not any(path.iterdir()):
                path.rmdir()

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
