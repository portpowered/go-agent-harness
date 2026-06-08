import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "setup-workspace.py"


class SetupWorkspaceScriptTests(unittest.TestCase):
    def test_setup_workspace_skips_root_pull_and_prune(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-2-factory-worktree-hygiene-repair"
            tasks_dir = repo_root / "tasks" / "todo"
            tasks_dir.mkdir(parents=True)
            prd_payload = {
                "branchName": prd_name,
                "userStories": [],
            }
            (tasks_dir / f"{prd_name}.json").write_text(
                json.dumps(prd_payload),
                encoding="utf-8",
            )
            (tasks_dir / f"{prd_name}.md").write_text(
                "# test prd\n",
                encoding="utf-8",
            )

            log_path = Path(temp_dir) / "git-commands.log"
            git_wrapper_dir = Path(temp_dir) / "bin"
            git_wrapper_dir.mkdir()
            real_git = shutil.which("git")
            self.assertIsNotNone(real_git)
            wrapper_path = git_wrapper_dir / "git"
            wrapper_path.write_text(
                "\n".join(
                    [
                        "#!/bin/sh",
                        f'printf "%s\\n" "$*" >> "{log_path}"',
                        f'exec "{real_git}" "$@"',
                        "",
                    ]
                ),
                encoding="utf-8",
            )
            wrapper_path.chmod(0o755)

            env = os.environ.copy()
            env["PATH"] = f"{git_wrapper_dir}{os.pathsep}{env['PATH']}"

            result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["status"], "ready")
            self.assertEqual(payload["branch"], prd_name)
            self.assertFalse(payload["reused"])

            commands = log_path.read_text(encoding="utf-8").splitlines()
            self.assertFalse(
                any(command == "pull" for command in commands),
                commands,
            )
            self.assertFalse(
                any(command == "worktree prune" for command in commands),
                commands,
            )
            self.assertFalse(
                any(command.endswith(" pull --ff-only") or command == "pull --ff-only" for command in commands),
                commands,
            )

    def test_setup_workspace_recovers_when_parallel_runs_race_on_worktree_creation(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-2-factory-worktree-hygiene-repair"
            self._write_prd(repo_root, prd_name)

            marker_path = Path(temp_dir) / "first-worktree-add.marker"
            git_wrapper_dir = Path(temp_dir) / "bin"
            git_wrapper_dir.mkdir()
            real_git = shutil.which("git")
            self.assertIsNotNone(real_git)
            wrapper_path = git_wrapper_dir / "git"
            wrapper_path.write_text(
                "\n".join(
                    [
                        "#!/bin/sh",
                        'if [ "$1" = "worktree" ] && [ "$2" = "add" ] && [ ! -f "' + str(marker_path) + '" ]; then',
                        '  : > "' + str(marker_path) + '"',
                        "  sleep 1",
                        "fi",
                        f'exec "{real_git}" "$@"',
                        "",
                    ]
                ),
                encoding="utf-8",
            )
            wrapper_path.chmod(0o755)

            env = os.environ.copy()
            env["PATH"] = f"{git_wrapper_dir}{os.pathsep}{env['PATH']}"

            first = subprocess.Popen(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            second = None

            try:
                self._wait_for_file(marker_path)

                second = subprocess.Popen(
                    ["python3", str(SCRIPT_PATH), prd_name],
                    cwd=repo_root,
                    env=env,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                )

                first_stdout, first_stderr = first.communicate(timeout=20)
                second_stdout, second_stderr = second.communicate(timeout=20)
            finally:
                self._terminate_if_running(first)
                self._terminate_if_running(second)

            self.assertEqual(first.returncode, 0, first_stderr)
            self.assertEqual(second.returncode, 0, second_stderr)

            first_payload = json.loads(first_stdout)
            second_payload = json.loads(second_stdout)

            self.assertEqual(first_payload["status"], "ready")
            self.assertEqual(second_payload["status"], "ready")
            self.assertEqual(first_payload["worktree"], second_payload["worktree"])
            self.assertEqual(first_payload["branch"], prd_name)
            self.assertEqual(second_payload["branch"], prd_name)
            self.assertEqual({first_payload["reused"], second_payload["reused"]}, {False, True})

    def _write_prd(self, repo_root: Path, prd_name: str) -> None:
        tasks_dir = repo_root / "tasks" / "todo"
        tasks_dir.mkdir(parents=True)
        prd_payload = {
            "branchName": prd_name,
            "userStories": [],
        }
        (tasks_dir / f"{prd_name}.json").write_text(
            json.dumps(prd_payload),
            encoding="utf-8",
        )
        (tasks_dir / f"{prd_name}.md").write_text(
            "# test prd\n",
            encoding="utf-8",
        )

    def _wait_for_file(self, path: Path, timeout_seconds: float = 5) -> None:
        deadline = time.monotonic() + timeout_seconds
        while time.monotonic() < deadline:
            if path.exists():
                return
            time.sleep(0.05)
        self.fail(f"Timed out waiting for {path}")

    def _terminate_if_running(self, process: subprocess.Popen | None) -> None:
        if process is None or process.poll() is not None:
            return
        process.terminate()
        process.communicate(timeout=5)

    def _init_repo(self, repo_root: Path) -> None:
        repo_root.mkdir(parents=True)
        self._run(["git", "init", "-b", "main"], cwd=repo_root)
        self._run(["git", "config", "user.name", "Test User"], cwd=repo_root)
        self._run(["git", "config", "user.email", "test@example.com"], cwd=repo_root)
        (repo_root / "README.md").write_text("root\n", encoding="utf-8")
        self._run(["git", "add", "README.md"], cwd=repo_root)
        self._run(["git", "commit", "-m", "init"], cwd=repo_root)

    def _run(self, args, cwd: Path) -> None:
        subprocess.run(
            args,
            cwd=cwd,
            capture_output=True,
            text=True,
            check=True,
        )


if __name__ == "__main__":
    unittest.main()
