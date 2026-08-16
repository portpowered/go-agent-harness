import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "setup-workspace.py"
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location(
    "setup_workspace",
    SCRIPT_PATH,
)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


class SetupWorkspaceTests(unittest.TestCase):
    def test_normalize_branch_replaces_path_separator(self):
        self.assertEqual(
            SCRIPT_MODULE.normalize_branch("feature/factory-tests"),
            "feature-factory-tests",
        )

    def test_read_prd_loads_json_payload(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            prd_path = Path(temp_dir) / "story.json"
            prd_path.write_text(
                json.dumps({"branchName": "factory-tests", "userStories": []}),
                encoding="utf-8",
            )

            self.assertEqual(
                SCRIPT_MODULE.read_prd(prd_path),
                {"branchName": "factory-tests", "userStories": []},
            )

    def test_worktree_is_valid_requires_git_metadata_and_content(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            worktree_path = Path(temp_dir) / "worktree"
            worktree_path.mkdir()
            self.assertFalse(SCRIPT_MODULE.worktree_is_valid(worktree_path))

            (worktree_path / ".git").write_text("gitdir: ../.git/worktrees/worktree\n")
            self.assertFalse(SCRIPT_MODULE.worktree_is_valid(worktree_path))

            (worktree_path / "README.md").write_text("worktree\n", encoding="utf-8")
            self.assertTrue(SCRIPT_MODULE.worktree_is_valid(worktree_path))


if __name__ == "__main__":
    unittest.main()
