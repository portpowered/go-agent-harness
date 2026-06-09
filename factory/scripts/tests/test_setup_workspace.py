import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
import importlib.util
from pathlib import Path
from unittest import mock


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "setup-workspace.py"
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location("setup_workspace", SCRIPT_PATH)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


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

    def test_create_or_reuse_waits_for_registered_same_branch_worktree_to_become_reusable(self):
        repo_root = Path("/tmp/repo-root")
        worktree_path = Path("/tmp/repo-root/.claude/worktrees/phase-2-factory-worktree-hygiene-repair")
        branch = "phase-2-factory-worktree-hygiene-repair"

        with mock.patch.object(SCRIPT_MODULE, "registered_branch_for_path", return_value=branch), \
             mock.patch.object(SCRIPT_MODULE, "branch_exists_locally") as branch_exists_locally, \
             mock.patch.object(SCRIPT_MODULE, "branch_exists_on_remote") as branch_exists_on_remote, \
             mock.patch.object(SCRIPT_MODULE, "run_git") as run_git, \
             mock.patch.object(SCRIPT_MODULE.shutil, "rmtree") as rmtree, \
             mock.patch.object(
                 SCRIPT_MODULE,
                 "wait_for_reusable_worktree",
                 side_effect=[False, True],
             ) as wait_for_reusable_worktree:
            reused = SCRIPT_MODULE.create_or_reuse_worktree(repo_root, branch, worktree_path)

        self.assertTrue(reused)
        branch_exists_locally.assert_not_called()
        branch_exists_on_remote.assert_not_called()
        run_git.assert_not_called()
        rmtree.assert_not_called()
        self.assertEqual(
            wait_for_reusable_worktree.call_args_list,
            [
                mock.call(repo_root, branch, worktree_path, timeout_seconds=0.2),
                mock.call(repo_root, branch, worktree_path),
            ],
        )

    def test_setup_workspace_allows_planner_owned_dirty_root_files(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-2-factory-worktree-hygiene-repair"
            self._write_prd(repo_root, prd_name)
            self._commit_planner_owned_files(repo_root)

            checklist_path = repo_root / "docs" / "internal" / "checklist.md"
            checklist_path.write_text("# checklist\nupdated\n", encoding="utf-8")
            progress_path = repo_root / "docs" / "internal" / "progress.txt"
            progress_path.write_text("planner progress\n", encoding="utf-8")

            result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["status"], "ready")
            self.assertEqual(payload["branch"], prd_name)
            self.assertFalse(payload["reused"])

    def test_setup_workspace_allows_planner_owned_batch_request_artifacts(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-4-audit-validator-015-reconciliation"
            self._write_prd(repo_root, prd_name)
            self._commit_planner_owned_files(repo_root)

            batch_path = (
                repo_root
                / "docs"
                / "internal"
                / "phase-4-api-contract-convergence-repair-batch-017.json"
            )
            batch_path.write_text(
                json.dumps(
                    {
                        "requestId": "phase-4-api-contract-convergence-repair-batch-017",
                        "type": "FACTORY_REQUEST_BATCH",
                        "works": [],
                    }
                ),
                encoding="utf-8",
            )
            self._run(
                [
                    "git",
                    "add",
                    "docs/internal/phase-4-api-contract-convergence-repair-batch-017.json",
                ],
                cwd=repo_root,
            )

            result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["status"], "ready")
            self.assertEqual(payload["branch"], prd_name)

    def test_setup_workspace_reuses_existing_worktree_with_planner_owned_dirty_root_files(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-2-factory-worktree-hygiene-repair"
            self._write_prd(repo_root, prd_name)
            self._commit_planner_owned_files(repo_root)

            first_result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(first_result.returncode, 0, first_result.stderr)
            first_payload = json.loads(first_result.stdout)

            checklist_path = repo_root / "docs" / "internal" / "checklist.md"
            checklist_path.write_text("# checklist\nupdated\n", encoding="utf-8")
            progress_path = repo_root / "docs" / "internal" / "progress.txt"
            progress_path.write_text("planner progress\n", encoding="utf-8")

            second_result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(second_result.returncode, 0, second_result.stderr)
            second_payload = json.loads(second_result.stdout)
            self.assertEqual(second_payload["status"], "ready")
            self.assertEqual(second_payload["branch"], prd_name)
            self.assertTrue(second_payload["reused"])
            self.assertEqual(second_payload["worktree"], first_payload["worktree"])
            self.assertEqual(second_payload["prd_path"], first_payload["prd_path"])
            self.assertEqual(second_payload["prd_md_path"], first_payload["prd_md_path"])

    def test_setup_workspace_fails_for_non_planner_owned_dirty_root_state(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_root = Path(temp_dir) / "repo"
            self._init_repo(repo_root)

            prd_name = "phase-2-factory-worktree-hygiene-repair"
            self._write_prd(repo_root, prd_name)

            (repo_root / "notes.txt").write_text("unexpected dirty file\n", encoding="utf-8")

            result = subprocess.run(
                ["python3", str(SCRIPT_PATH), prd_name],
                cwd=repo_root,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn(
                "unsupported dirty state outside planner-owned files and requested setup artifacts",
                result.stderr,
            )
            self.assertIn("?? notes.txt", result.stderr)

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

    def _commit_planner_owned_files(self, repo_root: Path) -> None:
        docs_internal_dir = repo_root / "docs" / "internal"
        docs_internal_dir.mkdir(parents=True, exist_ok=True)
        (docs_internal_dir / "checklist.md").write_text("# checklist\n", encoding="utf-8")
        (docs_internal_dir / "progress.txt").write_text("initial planner progress\n", encoding="utf-8")
        self._run(
            ["git", "add", "docs/internal/checklist.md", "docs/internal/progress.txt"],
            cwd=repo_root,
        )
        self._run(["git", "commit", "-m", "add planner-owned docs"], cwd=repo_root)

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
        (repo_root / ".gitignore").write_text(".claude/\n", encoding="utf-8")
        self._run(["git", "add", "README.md", ".gitignore"], cwd=repo_root)
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
