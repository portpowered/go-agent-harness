import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "setup-workspace.py"
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location(
    "setup_workspace",
    SCRIPT_PATH,
)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


class GitFixture:
    """Deterministic real-git sandbox: a bare origin plus a working clone."""

    def __init__(self, base_dir):
        self.base = Path(base_dir)
        self.origin_path = self.base / "origin.git"
        self.repo_path = self.base / "repo"
        self._run(self.base, "init", "--bare", self.origin_path.name)
        self._run(self.base, "clone", self.origin_path.name, self.repo_path.name)
        self._run(self.repo_path, "config", "user.name", "Factory Tests")
        self._run(self.repo_path, "config", "user.email", "factory-tests@example.com")
        self._run(self.repo_path, "checkout", "-B", "main")
        self.commit("initial commit", files={"README.md": "root\n"})
        self.push("-u", "main")

    def _run(self, cwd, *args, check=True):
        result = subprocess.run(
            ["git", *args],
            cwd=cwd,
            capture_output=True,
            text=True,
            check=False,
        )
        if check and result.returncode != 0:
            raise AssertionError(
                f"git {' '.join(args)} failed (exit {result.returncode}): "
                f"{result.stderr.strip()}"
            )
        return result

    def git(self, *args, check=True):
        return self._run(self.repo_path, *args, check=check)

    def commit(self, message, files=None):
        for relative_path, content in (files or {}).items():
            path = self.repo_path / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
            self.git("add", relative_path)
        self.git("commit", "-m", message)
        return self.rev("HEAD")

    def rev(self, revision, repo=None):
        result = self._run(repo or self.repo_path, "rev-parse", revision)
        return result.stdout.strip()

    def push(self, *args):
        return self.git("push", "origin", *args)

    def checked_out_worktree(self, branch):
        """Check out an existing local branch in the managed worktrees tree."""
        worktree_path = self.repo_path / ".claude" / "worktrees" / branch
        self.git("worktree", "add", str(worktree_path), branch)
        return worktree_path

    def twin_clone(self, name="twin"):
        """Clone origin independently so pushes never touch the main clone."""
        twin_path = self.base / name
        self._run(self.base, "clone", self.origin_path.name, twin_path.name)
        self._run(twin_path, "config", "user.name", "Factory Tests")
        self._run(twin_path, "config", "user.email", "factory-tests@example.com")
        return twin_path


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

    def test_foreign_checkout_collision_raises_and_preserves_external_worktree(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.git("branch", "shared-lane")
            external_path = fixture.base / "external-wt"
            fixture.git("worktree", "add", str(external_path), "shared-lane")

            readme_before = (external_path / "README.md").read_text(encoding="utf-8")
            head_before = fixture.rev("refs/heads/shared-lane")
            managed_path = (
                fixture.repo_path / ".claude" / "worktrees" / "shared-lane"
            )

            with self.assertRaises(
                SCRIPT_MODULE.WorktreePreparationError
            ) as raised:
                SCRIPT_MODULE.create_or_reuse_worktree(
                    fixture.repo_path, "shared-lane", managed_path,
                )

            self.assertIsInstance(raised.exception, RuntimeError)
            message = str(raised.exception)
            self.assertIn(str(external_path), message)
            self.assertIn("remove", message.lower())
            self.assertIn("repoint", message.lower())
            self.assertFalse(managed_path.exists())

            # The external worktree's contents and checkout state are untouched.
            self.assertEqual(
                (external_path / "README.md").read_text(encoding="utf-8"),
                readme_before,
            )
            self.assertEqual(
                fixture.rev("refs/heads/shared-lane", repo=external_path),
                head_before,
            )
            self.assertEqual(
                fixture._run(
                    external_path, "rev-parse", "--abbrev-ref", "HEAD",
                ).stdout.strip(),
                "shared-lane",
            )
            self.assertEqual(
                fixture._run(external_path, "status", "--porcelain").stdout,
                "",
            )

            # After removing the blocker, the same call creates the worktree.
            fixture.git("worktree", "remove", str(external_path))
            SCRIPT_MODULE.prune_worktrees(fixture.repo_path)
            reused = SCRIPT_MODULE.create_or_reuse_worktree(
                fixture.repo_path, "shared-lane", managed_path,
            )
            self.assertFalse(reused)
            self.assertTrue((managed_path / ".git").exists())

    def test_registered_but_missing_worktree_does_not_block_creation(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.git("branch", "ghost-lane")
            ghost_path = fixture.base / "ghost-wt"
            fixture.git("worktree", "add", str(ghost_path), "ghost-lane")
            shutil.rmtree(ghost_path)

            # A registration whose directory vanished never raises; the prune
            # step owns it and creation proceeds normally afterwards.
            self.assertIsNone(
                SCRIPT_MODULE.external_worktree_blocking_branch(
                    fixture.repo_path, "ghost-lane",
                ),
            )
            SCRIPT_MODULE.prune_worktrees(fixture.repo_path)

            managed_path = (
                fixture.repo_path / ".claude" / "worktrees" / "ghost-lane"
            )
            reused = SCRIPT_MODULE.create_or_reuse_worktree(
                fixture.repo_path, "ghost-lane", managed_path,
            )
            self.assertFalse(reused)
            self.assertTrue((managed_path / "README.md").exists())

    def test_deleted_upstream_is_pruned_before_existence_guard(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.git("branch", "sync-lane")
            fixture.push("-u", "sync-lane")
            worktree_path = fixture.checked_out_worktree("sync-lane")
            local_sha = fixture.rev("refs/heads/sync-lane")

            # Delete the branch from an independent clone so this clone keeps
            # the stale refs/remotes/origin/sync-lane tracking ref.
            twin = fixture.twin_clone()
            fixture._run(twin, "push", "origin", "--delete", "sync-lane")
            # The stale tracking ref masks the deletion before the refresh.
            self.assertTrue(
                SCRIPT_MODULE.branch_exists_on_remote(
                    fixture.repo_path, "sync-lane",
                ),
            )

            outcome = SCRIPT_MODULE.sync_reused_worktree_branch(
                fixture.repo_path, worktree_path, "sync-lane",
            )

            self.assertEqual(outcome, "skipped (branch has no origin ref)")
            # fetch --prune refreshed the tracking state before the guard.
            self.assertFalse(
                SCRIPT_MODULE.branch_exists_on_remote(
                    fixture.repo_path, "sync-lane",
                ),
            )
            self.assertEqual(
                fixture.rev("refs/heads/sync-lane", repo=worktree_path),
                local_sha,
            )

    def test_pull_failure_for_deleted_upstream_returns_skip_outcome(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            other_origin = fixture.base / "other-origin.git"
            fixture._run(fixture.base, "init", "--bare", other_origin.name)

            fixture.git("branch", "gone-lane")
            fixture.push("-u", "gone-lane")
            worktree_path = fixture.checked_out_worktree("gone-lane")
            local_sha = fixture.rev("refs/heads/gone-lane")

            # Point the worktree branch's upstream at a reachable remote whose
            # merged ref does not exist, reproducing the "no such ref was
            # fetched" pull failure caused by a branch deleted on origin.
            fixture.git("remote", "add", "origin2", str(other_origin))
            fixture._run(
                fixture.repo_path,
                "update-ref", "refs/remotes/origin2/deleted-tip", local_sha,
            )
            fixture._run(
                worktree_path, "config", "branch.gone-lane.remote", "origin2",
            )
            fixture._run(
                worktree_path,
                "config", "branch.gone-lane.merge", "refs/heads/deleted-tip",
            )

            outcome = SCRIPT_MODULE.sync_reused_worktree_branch(
                fixture.repo_path, worktree_path, "gone-lane",
            )

            self.assertEqual(outcome, "skipped (upstream branch deleted)")
            self.assertEqual(
                fixture.rev("refs/heads/gone-lane", repo=worktree_path),
                local_sha,
            )

    def test_upstream_force_move_resets_to_new_upstream_tip(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.commit("force-move base", {"fm.txt": "base\n"})
            fixture.git("branch", "fm-lane")
            fixture.push("-u", "fm-lane")
            worktree_path = fixture.checked_out_worktree("fm-lane")
            old_sha = fixture.rev("refs/heads/fm-lane")

            # Rewrite origin's fm-lane from an independent clone so the main
            # clone's tracking ref stays stale until the sync fetches.
            twin = fixture.twin_clone()
            fixture._run(twin, "checkout", "--detach", "origin/fm-lane")
            (twin / "fm.txt").write_text("rewritten\n", encoding="utf-8")
            fixture._run(twin, "commit", "-a", "--amend", "-m", "rewritten")
            new_sha = fixture.rev("HEAD", repo=twin)
            fixture._run(
                twin, "push", "--force", "origin", "HEAD:refs/heads/fm-lane",
            )

            outcome = SCRIPT_MODULE.sync_reused_worktree_branch(
                fixture.repo_path, worktree_path, "fm-lane",
            )

            self.assertIn("force-move", outcome)
            self.assertEqual(
                fixture.rev("refs/heads/fm-lane", repo=worktree_path), new_sha,
            )
            self.assertEqual(fixture.rev("HEAD", repo=worktree_path), new_sha)
            self.assertNotEqual(new_sha, old_sha)

    def test_unique_local_commits_raise_classified_error_without_reset(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.commit("diverge base", {"dv.txt": "base\n"})
            fixture.git("branch", "dv-lane")
            fixture.push("-u", "dv-lane")
            worktree_path = fixture.checked_out_worktree("dv-lane")
            upstream_base = fixture.rev("refs/heads/dv-lane")

            (worktree_path / "dv.txt").write_text("local work\n", encoding="utf-8")
            fixture._run(
                worktree_path, "commit", "-a", "-m", "unique unpushed work",
            )
            local_sha = fixture.rev("refs/heads/dv-lane")
            self.assertNotEqual(local_sha, upstream_base)

            twin = fixture.twin_clone()
            fixture._run(twin, "checkout", "--detach", "origin/dv-lane")
            (twin / "dv.txt").write_text("rewritten\n", encoding="utf-8")
            fixture._run(twin, "commit", "-a", "--amend", "-m", "rewritten")
            moved_sha = fixture.rev("HEAD", repo=twin)
            fixture._run(
                twin, "push", "--force", "origin", "HEAD:refs/heads/dv-lane",
            )

            with self.assertRaises(
                SCRIPT_MODULE.WorktreePreparationError
            ) as raised:
                SCRIPT_MODULE.sync_reused_worktree_branch(
                    fixture.repo_path, worktree_path, "dv-lane",
                )

            message = str(raised.exception)
            self.assertIn(local_sha, message)
            self.assertIn(moved_sha, message)
            # No reset executed: the local ref keeps its unique commit.
            self.assertEqual(
                fixture.rev("refs/heads/dv-lane", repo=worktree_path), local_sha,
            )
            self.assertEqual(fixture.rev("HEAD", repo=worktree_path), local_sha)

    def test_plain_fast_forward_still_fast_forwards_from_upstream(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            fixture.git("branch", "ff-lane")
            fixture.push("-u", "ff-lane")
            worktree_path = fixture.checked_out_worktree("ff-lane")
            behind_sha = fixture.rev("refs/heads/ff-lane")

            twin = fixture.twin_clone()
            fixture._run(twin, "checkout", "-B", "ff-lane", "origin/ff-lane")
            (twin / "advanced.txt").write_text("ahead\n", encoding="utf-8")
            fixture._run(twin, "add", "advanced.txt")
            fixture._run(twin, "commit", "-m", "advance ff-lane")
            ahead_sha = fixture.rev("HEAD", repo=twin)
            fixture._run(twin, "push", "origin", "ff-lane")

            outcome = SCRIPT_MODULE.sync_reused_worktree_branch(
                fixture.repo_path, worktree_path, "ff-lane",
            )

            self.assertEqual(outcome, "fast-forwarded from upstream")
            self.assertEqual(
                fixture.rev("HEAD", repo=worktree_path), ahead_sha,
            )
            self.assertNotEqual(ahead_sha, behind_sha)

    def test_workspace_setup_reaches_ready_when_upstream_was_deleted(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            prd_name = "e2e-deleted-upstream"
            todo_dir = fixture.repo_path / "tasks" / "todo"
            todo_dir.mkdir(parents=True)
            (todo_dir / f"{prd_name}.json").write_text(
                json.dumps({"branchName": prd_name}),
                encoding="utf-8",
            )
            fixture.git("branch", prd_name)
            fixture.push("-u", prd_name)
            worktree_path = fixture.checked_out_worktree(prd_name)
            fixture._run(
                fixture.twin_clone(), "push", "origin", "--delete", prd_name,
            )

            original_argv = sys.argv
            original_cwd = os.getcwd()
            try:
                sys.argv = ["setup-workspace.py", prd_name]
                os.chdir(fixture.repo_path)
                stdout_buffer = io.StringIO()
                stderr_buffer = io.StringIO()
                with redirect_stdout(stdout_buffer), redirect_stderr(stderr_buffer):
                    SCRIPT_MODULE.main()
            finally:
                os.chdir(original_cwd)
                sys.argv = original_argv

            result = json.loads(stdout_buffer.getvalue())
            self.assertEqual(result["status"], "ready")
            self.assertTrue(result["reused"])
            self.assertEqual(result["branch"], prd_name)
            self.assertEqual(Path(result["worktree"]), worktree_path.resolve())
            self.assertIn(
                "Worktree branch sync: skipped (branch has no origin ref)",
                stderr_buffer.getvalue(),
            )


if __name__ == "__main__":
    unittest.main()
