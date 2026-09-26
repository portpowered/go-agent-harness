import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT_PATH = REPO_ROOT / "scripts" / "prepush.sh"
# The gate's stages, in order. Phases inside a stage are independent; with
# PREPUSH_JOBS=1 they run serially in this order.
FORMAT_STAGE = ("fmt",)
STATIC_STAGE = (
    "lint",
    "verify-architecture",
    "build",
    "coverage-registration",
    "check-ci-test-partition",
    "verify-standalone-checkout",
)
TEST_STAGE = ("coverage", "test-cgo-delta", "test-tools")
PHASES = FORMAT_STAGE + STATIC_STAGE + TEST_STAGE


class PrepushTargetTests(unittest.TestCase):
    def test_target_runs_all_phases_in_order_and_times_them(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(fake_make, log_path, env={"PREPUSH_JOBS": "1"})
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

    def test_tests_run_once_through_the_coverage_pass(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(fake_make, log_path)
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertEqual(result.returncode, 0, result.output)
            self.assertEqual(sorted(phase_log), sorted(PHASES))
            for duplicate in ("test", "embed-check", "coverage-changed", "test-architecture-gate"):
                self.assertNotIn(duplicate, phase_log)
            arguments = self._arguments(log_path)
            self.assertIn("TEST_TOOLS_ARCHITECTURE_GATE=0 test-tools", arguments)
            # lint type-checks every library package; build only links binaries.
            self.assertIn("BUILD_LIBRARY_PACKAGES=0 build", arguments)

    def test_scope_defaults_to_changed_and_full_is_selectable(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(fake_make, log_path)
            self.assertEqual(result.returncode, 0, result.output)
            self.assertIn("COVERAGE_SCOPE=changed coverage", self._arguments(log_path))
            self.assertIn("scope changed", result.output)

        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(fake_make, log_path, make_arguments=["PREPUSH_SCOPE=full"])
            self.assertEqual(result.returncode, 0, result.output)
            self.assertIn("COVERAGE_SCOPE=full coverage", self._arguments(log_path))

    def test_factory_script_tests_join_the_static_stage_when_selected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make, log_path, env={"PREPUSH_JOBS": "1", "PREPUSH_FACTORY_SCRIPTS": "always"}
            )
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertEqual(result.returncode, 0, result.output)
            self.assertEqual(
                phase_log,
                list(FORMAT_STAGE + STATIC_STAGE + ("test-factory-scripts",) + TEST_STAGE),
            )

    def test_passed_phases_are_cached_for_the_same_content(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            cache_env = {
                "PREPUSH_JOBS": "1",
                "PREPUSH_CACHE": "1",
                "PREPUSH_CACHE_DIR": str(Path(temp_dir) / "cache"),
                "PREPUSH_FAIL_PHASE": "coverage",
            }
            first = self._run_pre_push(fake_make, log_path, env=dict(cache_env))
            self.assertNotEqual(first.returncode, 0, first.output)
            self.assertEqual(log_path.read_text(encoding="utf-8").splitlines(), list(PHASES[: PHASES.index("coverage") + 1]))

            log_path.unlink()
            cache_env.pop("PREPUSH_FAIL_PHASE")
            second = self._run_pre_push(fake_make, log_path, env=dict(cache_env))
            self.assertEqual(second.returncode, 0, second.output)
            # Only the phases that had not passed run again.
            self.assertEqual(log_path.read_text(encoding="utf-8").splitlines(), list(TEST_STAGE))
            self.assertIn("==> prepush phase lint cached: passed for this content", second.output)

            log_path.unlink()
            third = self._run_pre_push(
                fake_make, log_path, env=dict(cache_env), make_arguments=["PREPUSH_SCOPE=full"]
            )
            self.assertEqual(third.returncode, 0, third.output)
            # The scope is part of the key.
            self.assertEqual(log_path.read_text(encoding="utf-8").splitlines(), list(PHASES))

    def test_invalid_scope_and_jobs_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            for env in ({"PREPUSH_SCOPE": "some"}, {"PREPUSH_JOBS": "0"}):
                result = self._run_script(fake_make, log_path, env)
                self.assertEqual(result.returncode, 2, result.output)
                self.assertFalse(log_path.exists(), result.output)

    def test_target_stops_at_first_failed_phase_and_reports_status(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make,
                log_path,
                env={
                    "PREPUSH_JOBS": "1",
                    "PREPUSH_FAIL_PHASE": "build",
                    "PREPUSH_FAIL_STATUS": "23",
                },
            )
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertNotEqual(result.returncode, 0, result.output)
            self.assertEqual(phase_log, list(PHASES[: PHASES.index("build") + 1]))
            self.assertIn("==> prepush failed at phase build", result.output)
            self.assertIn("exit 23", result.output)
            self.assertRegex(
                result.output,
                r"==> prepush phase build completed in \d+s",
            )
            self.assertRegex(result.output, r"==> prepush total completed in \d+s")
            self.assertNotIn("==> prepush phase: coverage-registration", result.output)

    def test_concurrent_static_failure_blocks_the_test_stage(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_pre_push(
                fake_make,
                log_path,
                env={
                    "PREPUSH_JOBS": str(len(STATIC_STAGE)),
                    "PREPUSH_FAIL_PHASE": "verify-architecture",
                    "PREPUSH_FAIL_STATUS": "19",
                },
            )
            phase_log = log_path.read_text(encoding="utf-8").splitlines()

            self.assertNotEqual(result.returncode, 0, result.output)
            self.assertEqual(sorted(phase_log), sorted(FORMAT_STAGE + STATIC_STAGE))
            self.assertIn("failed at phase verify-architecture (exit 19)", result.output)
            for phase in TEST_STAGE:
                self.assertNotRegex(result.output, rf"(?m)^==> prepush phase: {phase}( \(started\))?$")

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
            self.assertNotIn("==> prepush phase: vet", result.output)

    def test_changed_content_or_environment_invalidates_the_phase_cache(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_make, log_path = self._fake_make(root)
            repo = root / "repo"
            repo.mkdir()
            tracked = repo / "tracked.txt"
            tracked.write_text("one\n", encoding="utf-8")
            for arguments in (
                ["init", "-q"],
                ["config", "user.email", "prepush-test@example.test"],
                ["config", "user.name", "prepush test"],
                # No detached auto-maintenance after commit: it would
                # write objects/maintenance.lock while the test inspects
                # the object directory.
                ["config", "maintenance.auto", "false"],
                ["config", "gc.auto", "0"],
                ["add", "-A"],
                ["commit", "-qm", "baseline"],
            ):
                subprocess.run(["git", "-C", str(repo), *arguments], check=True)
            objects_before = self._loose_objects(repo)
            env = {"PREPUSH_JOBS": "1", "PREPUSH_CACHE": "1", "PREPUSH_CACHE_DIR": str(root / "cache")}

            def run(extra=None):
                if log_path.exists():
                    log_path.unlink()
                result = self._run_script(fake_make, log_path, {**env, **(extra or {})}, cwd=repo)
                self.assertEqual(result.returncode, 0, result.output)
                ran = log_path.read_text(encoding="utf-8").splitlines() if log_path.exists() else []
                return ran, result.output

            self.assertEqual(run()[0], list(PHASES))
            self.assertEqual(run()[0], [])

            tracked.write_text("two\n", encoding="utf-8")
            self.assertEqual(run()[0], list(PHASES), "a tracked edit must invalidate the cache")
            (repo / "untracked.txt").write_text("new\n", encoding="utf-8")
            self.assertEqual(run()[0], list(PHASES), "an untracked file must invalidate the cache")
            self.assertEqual(run({"GOFLAGS": "-mod=mod"})[0], list(PHASES), "GOFLAGS must be part of the key")
            self.assertEqual(run({"MAKEFLAGS": "LINT_SHARD=agent-cli"})[0], list(PHASES), "Make variables must be part of the key")

            # Content edited while a stage runs: that stage is not recorded,
            # so after reverting the edit only that stage runs again.
            env["PREPUSH_CACHE_DIR"] = str(root / "cache-mid-run")
            ran, output = run({"PREPUSH_EDIT_PHASE": "coverage", "PREPUSH_EDIT_FILE": str(tracked)})
            self.assertEqual(ran, list(PHASES))
            self.assertIn("content changed while the gate ran", output)
            tracked.write_text("two\n", encoding="utf-8")
            (repo / "untracked.txt").write_text("new\n", encoding="utf-8")
            self.assertEqual(run()[0], list(TEST_STAGE))

            self.assertEqual(self._loose_objects(repo), objects_before, "hashing must not write objects into the repository")

    def test_racily_clean_same_size_edit_invalidates_the_phase_cache(self):
        # A same-size edit made in the second the index was written keeps the
        # entry's stat data (mtime seconds, size, inode); git then re-hashes
        # the file only because the entry is not older than the index file.
        # Pin every timestamp so that case holds regardless of the clock.
        # core.trustctime=false removes the ctime the test cannot set.
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_make, log_path = self._fake_make(root)
            repo = root / "repo"
            repo.mkdir()
            tracked = repo / "tracked.txt"
            tracked.write_text("one\n", encoding="utf-8")
            written = 1_600_000_000
            os.utime(tracked, (written, written))
            for arguments in (
                ["init", "-q"],
                ["config", "user.email", "prepush-test@example.test"],
                ["config", "user.name", "prepush test"],
                # No detached auto-maintenance after commit: it would
                # write objects/maintenance.lock while the test inspects
                # the object directory.
                ["config", "maintenance.auto", "false"],
                ["config", "gc.auto", "0"],
                ["config", "core.trustctime", "false"],
                ["add", "-A"],
                ["commit", "-qm", "baseline"],
            ):
                subprocess.run(["git", "-C", str(repo), *arguments], check=True)
            index = repo / ".git" / "index"
            os.utime(index, (written, written))
            env = {"PREPUSH_JOBS": "1", "PREPUSH_CACHE": "1", "PREPUSH_CACHE_DIR": str(root / "cache")}

            def run():
                if log_path.exists():
                    log_path.unlink()
                result = self._run_script(fake_make, log_path, env, cwd=repo)
                self.assertEqual(result.returncode, 0, result.output)
                return log_path.read_text(encoding="utf-8").splitlines() if log_path.exists() else []

            self.assertEqual(run(), list(PHASES))
            os.utime(index, (written, written))
            tracked.write_text("two\n", encoding="utf-8")
            os.utime(tracked, (written, written))
            self.assertEqual(run(), list(PHASES), "a racily clean edit must invalidate the cache")

    @staticmethod
    def _loose_objects(repo):
        objects = repo / ".git" / "objects"
        return sorted(str(path.relative_to(objects)) for path in objects.rglob("*") if path.is_file())

    def test_runner_returns_the_failing_phase_status(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_make, log_path = self._fake_make(Path(temp_dir))
            result = self._run_script(
                fake_make,
                log_path,
                {"PREPUSH_FAIL_PHASE": "lint", "PREPUSH_FAIL_STATUS": "23"},
            )

            self.assertEqual(result.returncode, 23, result.output)
            phase_log = log_path.read_text(encoding="utf-8").splitlines()
            self.assertIn("lint", phase_log)
            self.assertFalse(set(TEST_STAGE) & set(phase_log), result.output)

    def _run_script(self, fake_make, log_path, env, cwd=REPO_ROOT):
        process_env = os.environ.copy()
        process_env.pop("PREPUSH_SCOPE", None)
        process_env.pop("PREPUSH_JOBS", None)
        process_env["PREPUSH_FACTORY_SCRIPTS"] = "never"
        process_env["PREPUSH_CACHE"] = "0"
        for inherited in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL", "COVERAGE_BASE"):
            process_env.pop(inherited, None)
        process_env.update(
            {"PREPUSH_MAKE": str(fake_make), "PREPUSH_LOG": str(log_path), **env}
        )
        result = subprocess.run(
            [str(SCRIPT_PATH)],
            cwd=cwd,
            capture_output=True,
            text=True,
            env=process_env,
            check=False,
        )
        return _CommandResult(result.returncode, result.stdout + result.stderr)

    def _run_pre_push(self, fake_make, log_path, env=None, make_arguments=()):
        process_env = os.environ.copy()
        process_env.pop("PREPUSH_SCOPE", None)
        process_env.pop("PREPUSH_JOBS", None)
        process_env["PREPUSH_FACTORY_SCRIPTS"] = "never"
        process_env["PREPUSH_CACHE"] = "0"
        for inherited in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL", "COVERAGE_BASE"):
            process_env.pop(inherited, None)
        env = dict(env or {})
        # Make variables are passed on the command line, like a developer would.
        arguments = [f"{name}={env.pop(name)}" for name in ("PREPUSH_JOBS",) if name in env]
        process_env.update({"PREPUSH_LOG": str(log_path), **env})
        result = subprocess.run(
            ["make", "--no-print-directory", "prepush", f"PREPUSH_MAKE={fake_make}", *arguments, *make_arguments],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            env=process_env,
            check=False,
        )
        return _CommandResult(result.returncode, result.stdout + result.stderr)

    @staticmethod
    def _arguments(log_path):
        return Path(str(log_path) + ".args").read_text(encoding="utf-8").splitlines()

    def _fake_make(self, temp_dir):
        log_path = temp_dir / "phases.log"
        fake_make = temp_dir / "fake-make"
        fake_make.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            "phase=\"\"\n"
            "for argument in \"$@\"; do phase=\"$argument\"; done\n"
            "printf '%s\\n' \"$phase\" >> \"$PREPUSH_LOG\"\n"
            "shift\n"
            "printf '%s\\n' \"$*\" >> \"$PREPUSH_LOG.args\"\n"
            "if [ \"${PREPUSH_FORMAT_DIAGNOSTIC:-0}\" = 1 ] && [ \"$phase\" = fmt ]; then\n"
            "  echo \"gofmt drift detected in fixture.go\" >&2\n"
            "  echo \"Run 'make fmt-fix' to rewrite files before rerunning 'make prepush'.\" >&2\n"
            "fi\n"
            "if [ \"${PREPUSH_EDIT_PHASE:-}\" = \"$phase\" ]; then\n"
            "  printf 'edited mid-run\\n' >> \"$PREPUSH_EDIT_FILE\"\n"
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
