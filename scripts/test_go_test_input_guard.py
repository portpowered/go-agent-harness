"""Tests for scripts/go-test-input-guard.py through real `go test -exec` runs."""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

GUARD = Path(__file__).resolve().parent / "go-test-input-guard.py"


@unittest.skipIf(shutil.which("go") is None, "go is not installed")
class GoTestInputGuardTest(unittest.TestCase):
    def setUp(self) -> None:
        self.repo = Path(tempfile.mkdtemp(prefix="guard-repo-")).resolve()
        self.addCleanup(shutil.rmtree, self.repo, ignore_errors=True)
        (self.repo / "other").mkdir()
        (self.repo / "other" / "data.txt").write_text("other module data\n")
        self.module = self.repo / "mod"
        self.module.mkdir()
        (self.module / "go.mod").write_text("module example.com/mod\n\ngo 1.22\n")
        (self.module / "own.txt").write_text("own data\n")
        # Go does not cache a run that read a file modified in the last 2s.
        os.utime(self.module / "own.txt", (1_000_000_000, 1_000_000_000))

    def go_test(self, body: str, imports: str = '"os"') -> subprocess.CompletedProcess[str]:
        (self.module / "mod_test.go").write_text(
            textwrap.dedent(
                f"""\
                package mod

                import (
                \t"testing"
                \t{imports}
                )

                func TestInputs(t *testing.T) {{
                {textwrap.indent(textwrap.dedent(body), chr(9))}
                }}
                """
            )
        )
        env = dict(os.environ, GO_TEST_GUARD_REPO=str(self.repo), GOWORK="off", GOFLAGS="")
        return subprocess.run(
            ["go", "test", f"-exec=python3 {GUARD}", "./..."],
            cwd=self.module,
            env=env,
            capture_output=True,
            text=True,
        )

    def test_reading_own_module_passes_and_is_cached(self) -> None:
        body = """
        if _, err := os.ReadFile("own.txt"); err != nil {
            t.Fatal(err)
        }
        """
        first = self.go_test(body)
        self.assertEqual(first.returncode, 0, first.stdout + first.stderr)
        second = self.go_test(body)
        self.assertEqual(second.returncode, 0, second.stdout + second.stderr)
        self.assertIn("(cached)", second.stdout)

    def test_reading_another_module_fails(self) -> None:
        result = self.go_test(
            """
            if _, err := os.ReadFile("../other/data.txt"); err != nil {
                t.Fatal(err)
            }
            """
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("go-test-input-guard:", result.stdout)
        self.assertIn("open other/data.txt", result.stdout)

    def test_exec_of_go_in_the_repository_fails(self) -> None:
        result = self.go_test(
            """
            if out, err := exec.Command("go", "list", ".").CombinedOutput(); err != nil {
                t.Fatal(err, string(out))
            }
            """,
            imports='"os/exec"',
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exec in", result.stdout)
        self.assertIn("go list .", result.stdout)

    def test_exec_of_go_before_m_run_fails(self) -> None:
        (self.module / "main_test.go").write_text(
            textwrap.dedent(
                """\
                package mod

                import (
                \t"os"
                \t"os/exec"
                \t"testing"
                )

                func TestMain(m *testing.M) {
                \tif err := exec.Command("go", "env", "GOOS").Run(); err != nil {
                \t\tpanic(err)
                \t}
                \tos.Exit(m.Run())
                }
                """
            )
        )
        result = self.go_test("t.Log(os.Getpid())")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("go env GOOS", result.stdout)

    def test_failing_test_keeps_its_status(self) -> None:
        result = self.go_test('t.Fatal("boom")\n_ = os.Getpid')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("boom", result.stdout)
        self.assertNotIn("go-test-input-guard:", result.stdout)


if __name__ == "__main__":
    unittest.main()
