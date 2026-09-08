import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT_PATH = REPO_ROOT / "scripts" / "prepush.sh"
PHASES = (
    "fmt",
    "verify-architecture",
    "vet",
    "lint",
    "staticcheck",
    "build",
    "embed-check",
    "test",
    "coverage-registration",
    "coverage-changed",
)


class PrepushTargetTests(unittest.TestCase):
    def test_target_runs_all_phases_in_order_and_times_them(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(fake_make, log_path)
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertEqual(result.returncode, 0, result.output)
            self.assertEqual(phase_log, list(PHASES))
            for phase in PHASES:
                self.assertRegex(
                    result.output,
                    rf"==> prepush phase {phase} completed in \d+s",
                )
            self.assertIn("==> prepush passed", result.output)
            self.assertRegex(result.output, r"==> prepush total completed in \d+s")
            self.assertNotIn("\ncoverage\n", "\n" + "\n".join(phase_log) + "\n")

    def test_target_stops_at_first_failed_phase_and_reports_status(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make,
                log_path,
                env={"PREPUSH_FAIL_PHASE": "staticcheck", "PREPUSH_FAIL_STATUS": "23"},
            )
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertNotEqual(result.returncode, 0, result.output)
            self.assertEqual(phase_log, list(PHASES[: PHASES.index("staticcheck") + 1]))
            self.assertIn("==> prepush failed at phase staticcheck", result.output)
            self.assertIn("exit 23", result.output)
            self.assertRegex(
                result.output,
                r"==> prepush phase staticcheck completed in \d+s",
            )
            self.assertRegex(result.output, r"==> prepush total completed in \d+s")
            self.assertNotIn("==> prepush phase build", result.output)

    def test_format_failure_preserves_actionable_fix_hint_and_is_timed(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make,
                log_path,
                env={
                    "PREPUSH_FAIL_PHASE": "fmt",
                    "PREPUSH_FORMAT_DIAGNOSTIC": "1",
                },
            )
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertNotEqual(result.returncode, 0, result.output)
            self.assertEqual(phase_log, ["fmt"])
            self.assertIn("gofmt drift detected", result.output)
            self.assertIn("Run 'make fmt-fix'", result.output)
            self.assertRegex(result.output, r"==> prepush phase fmt completed in \d+s")
            self.assertNotIn("==> prepush phase vet", result.output)

    def test_runner_returns_the_failing_phase_status(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            process_env = os.environ.copy()
            process_env.update(
                {
                    "PREPUSH_MAKE": str(fake_make),
                    "PREPUSH_LOG": str(log_path),
                    "PREPUSH_FAIL_PHASE": "lint",
                    "PREPUSH_FAIL_STATUS": "23",
                }
            )
            result = subprocess.run(
                [str(SCRIPT_PATH)],
                cwd=REPO_ROOT,
                capture_output=True,
                text=True,
                env=process_env,
                check=False,
            )

            self.assertEqual(result.returncode, 23, result.stdout + result.stderr)
            self.assertEqual(
                log_path.read_text(encoding="utf-8").splitlines(),
                list(PHASES[: PHASES.index("lint") + 1]),
            )

    def test_architecture_failure_blocks_build_and_embedding(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make,
                log_path,
                env={"PREPUSH_FAIL_PHASE": "verify-architecture", "PREPUSH_FAIL_STATUS": "19"},
            )
            self.assertNotEqual(result.returncode, 0, result.output)
            self.assertEqual(log_path.read_text().splitlines(), ["fmt", "verify-architecture"])
            self.assertIn("failed at phase verify-architecture", result.output)
            self.assertNotIn("==> prepush phase build", result.output)
            self.assertNotIn("==> prepush phase embed-check", result.output)

    def _run_pre_push(self, fake_make, log_path, env=None):
        process_env = os.environ.copy()
        process_env.update({"PREPUSH_LOG": str(log_path), **(env or {})})
        result = subprocess.run(
            ["make", "--no-print-directory", "prepush", f"PREPUSH_MAKE={fake_make}"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            env=process_env,
            check=False,
        )
        return _CommandResult(result.returncode, result.stdout + result.stderr)

    def _fake_make(self, temp_dir):
        log_path = temp_dir / "phases.log"
        fake_make = temp_dir / "fake-make"
        fake_make.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            "phase=\"\"\n"
            "for argument in \"$@\"; do phase=\"$argument\"; done\n"
            "printf '%s\\n' \"$phase\" >> \"$PREPUSH_LOG\"\n"
            "if [ \"${PREPUSH_FORMAT_DIAGNOSTIC:-0}\" = 1 ] && [ \"$phase\" = fmt ]; then\n"
            "  echo \"gofmt drift detected in fixture.go\" >&2\n"
            "  echo \"Run 'make fmt-fix' to rewrite files before rerunning 'make prepush'.\" >&2\n"
            "fi\n"
            "if [ \"${PREPUSH_FAIL_PHASE:-}\" = \"$phase\" ]; then\n"
            "  exit \"${PREPUSH_FAIL_STATUS:-1}\"\n"
            "fi\n",
            encoding="utf-8",
        )
        fake_make.chmod(fake_make.stat().st_mode | stat.S_IXUSR)
        return fake_make, log_path


class _CommandResult:
    def __init__(self, returncode, output):
        self.returncode = returncode
        self.output = output


if __name__ == "__main__":
    unittest.main()
