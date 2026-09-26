import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest


RUNNER = Path(__file__).with_name("unittest-parallel.py")

FIXTURE = textwrap.dedent(
    """
    import os
    import unittest


    class Independent(unittest.TestCase):
        def test_passes(self):
            pass

        def test_fails(self):
            self.fail("intentional parallel runner failure")

        @unittest.skip("fixture skip")
        def test_skipped(self):
            pass


    class SharedFixture(unittest.TestCase):
        @classmethod
        def setUpClass(cls):
            cls.pid = os.getpid()
            cls.seen = []

        def test_a(self):
            self.seen.append(os.getpid())

        def test_b(self):
            self.seen.append(os.getpid())
            self.assertEqual(self.seen, [self.pid, self.pid])
    """
)


class UnittestParallelTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        (self.directory / "parallel_fixture.py").write_text(FIXTURE)

    def run_runner(self, *names, options=()):
        env = dict(os.environ, PYTHONPATH=str(self.directory), PYTHONDONTWRITEBYTECODE="1")
        return subprocess.run(
            [sys.executable, "-B", str(RUNNER), "-v", "-j", "4", *options, *names],
            cwd=self.directory, env=env, capture_output=True, text=True, check=False,
        )

    def test_reports_every_test_once_and_fails_on_failure(self):
        result = self.run_runner("parallel_fixture")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("Ran 5 tests in ", result.stderr)
        self.assertIn("FAILED (failures=1, skipped=1)", result.stderr)
        self.assertIn("intentional parallel runner failure", result.stderr)
        self.assertEqual(result.stderr.count("test_passes (parallel_fixture.Independent.test_passes) ... ok"), 1)
        # Both SharedFixture tests ran after one setUpClass, in one process.
        self.assertIn("test_b (parallel_fixture.SharedFixture.test_b) ... ok", result.stderr)

    def test_passing_selection_exits_zero(self):
        result = self.run_runner("parallel_fixture.SharedFixture", "parallel_fixture.Independent.test_passes")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Ran 3 tests in ", result.stderr)
        self.assertTrue(result.stderr.rstrip().endswith("OK"), result.stderr)

    def test_missing_module_is_an_error(self):
        result = self.run_runner("parallel_fixture_missing")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("ModuleNotFoundError", result.stderr)
        self.assertIn("FAILED (errors=1)", result.stderr)

    def test_empty_selection_runs_nothing(self):
        result = self.run_runner()
        self.assertEqual(result.returncode, 5, result.stderr)
        self.assertIn("Ran 0 tests in ", result.stderr)
        self.assertIn("NO TESTS RAN", result.stderr)

    def test_forbid_bytecode_catches_bytecode_from_spawned_interpreters(self):
        (self.directory / "helper_module.py").write_text("VALUE = 1\n")
        (self.directory / "spawning_fixture.py").write_text(textwrap.dedent(
            """
            import os
            import subprocess
            import sys
            import unittest


            class Spawns(unittest.TestCase):
                def test_spawn_without_dont_write_bytecode(self):
                    env = {k: v for k, v in os.environ.items() if k != "PYTHONDONTWRITEBYTECODE"}
                    subprocess.run([sys.executable, "-c", "import helper_module"], env=env, check=True)
            """
        ))
        options = ("--forbid-bytecode", str(self.directory))
        clean = self.run_runner("parallel_fixture.SharedFixture", options=options)
        self.assertEqual(clean.returncode, 0, clean.stderr)
        result = self.run_runner("spawning_fixture", options=options)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("Ran 1 test in ", result.stderr)
        self.assertIn("the tests wrote Python bytecode", result.stderr)
        self.assertIn("__pycache__", result.stderr)


if __name__ == "__main__":
    unittest.main()
