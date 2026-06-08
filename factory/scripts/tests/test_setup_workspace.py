import json
import os
import shutil
import subprocess
import tempfile
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
