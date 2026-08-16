import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "setup-workspace.py"
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location(
    "setup_workspace_for_hygiene",
    SCRIPT_PATH,
)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


class WorktreeHygieneTests(unittest.TestCase):
    def test_clean_and_modified_worktrees_are_distinguished(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_path = Path(temp_dir)
            self._run_git(repo_path, "init")
            self._run_git(repo_path, "config", "user.name", "Factory Tests")
            self._run_git(repo_path, "config", "user.email", "factory-tests@example.com")

            tracked_path = repo_path / "tracked.txt"
            tracked_path.write_text("clean\n", encoding="utf-8")
            self._run_git(repo_path, "add", "tracked.txt")
            self._run_git(repo_path, "commit", "-m", "initial test commit")

            self.assertFalse(SCRIPT_MODULE.working_tree_has_local_changes(repo_path))
            tracked_path.write_text("modified\n", encoding="utf-8")
            self.assertTrue(SCRIPT_MODULE.working_tree_has_local_changes(repo_path))

    def test_untracked_paths_exclude_ignored_bytecode_artifacts(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo_path = Path(temp_dir)
            self._run_git(repo_path, "init")
            self._run_git(repo_path, "config", "user.name", "Factory Tests")
            self._run_git(repo_path, "config", "user.email", "factory-tests@example.com")

            (repo_path / ".gitignore").write_text("__pycache__/\n", encoding="utf-8")
            (repo_path / "kept.txt").write_text("keep\n", encoding="utf-8")
            cache_path = repo_path / "__pycache__"
            cache_path.mkdir()
            (cache_path / "module.pyc").write_bytes(b"bytecode")

            self.assertEqual(
                SCRIPT_MODULE.untracked_paths(repo_path),
                [".gitignore", "kept.txt"],
            )

    def _run_git(self, repo_path, *args):
        result = subprocess.run(
            ["git", *args],
            cwd=repo_path,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
