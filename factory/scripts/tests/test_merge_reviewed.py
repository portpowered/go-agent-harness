"""Behavioral tests for the fail-closed reviewed-head merge helper."""

import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "merge-reviewed.py"
HEAD = "0123456789abcdef0123456789abcdef01234567"
MAIN = "fedcba9876543210fedcba9876543210fedcba98"
MERGED = "89abcdef0123456789abcdef0123456789abcdef"
PR = 526
REPO = "portpowered/go-agent-harness"


def _load_module():
    spec = importlib.util.spec_from_file_location("merge_reviewed", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def _view(module, *, state="OPEN", head=HEAD, mergeable="MERGEABLE", merge_state="CLEAN", draft=False):
    return {
        "number": PR,
        "state": state,
        "isDraft": draft,
        "headRefOid": head,
        "baseRefName": "main",
        "mergeable": mergeable,
        "mergeStateStatus": merge_state,
        "statusCheckRollup": [{"name": "CI (unit)"}],
    }


def _checks(module, state="SUCCESS"):
    return [
        {"name": name, "state": state, "bucket": "pass" if state == "SUCCESS" else "pending"}
        for name in module._load_required_checks()
    ]


class MergeReviewedTests(unittest.TestCase):
    def setUp(self):
        self.module = _load_module()

    def _repo_path(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        path = Path(temporary.name) / "repo"
        path.mkdir()
        subprocess.run(["git", "init", "-q", str(path)], check=True)
        return path.resolve()

    def _fake_command(self, view_rows, checks=None, required=None, merge_rc=0):
        checks = checks if checks is not None else _checks(self.module)
        required = required if required is not None else checks
        commands = []
        view_index = 0
        merged = False

        def fake(args, **kwargs):
            nonlocal merged, view_index
            commands.append(list(args))
            if args[:3] == ["git", "-C", str(self.repo_path)]:
                git_args = args[3:]
                if git_args[:2] == ["rev-parse", "--git-common-dir"]:
                    return subprocess.CompletedProcess(args, 0, ".git\n", "")
                if git_args[:2] == ["rev-parse", "origin/main"]:
                    revision = MERGED if merged else MAIN
                    return subprocess.CompletedProcess(args, 0, f"{revision}\n", "")
                if git_args[:2] == ["merge-base", "--is-ancestor"]:
                    return subprocess.CompletedProcess(args, 0, "", "")
                return subprocess.CompletedProcess(args, 0, "", "")
            if args[:4] == ["gh", "pr", "view", str(PR)]:
                if "mergeCommit" in args[-1]:
                    value = {"number": PR, "state": "MERGED", "mergeCommit": {"oid": MERGED}, "headRefOid": HEAD}
                else:
                    value = view_rows[min(view_index, len(view_rows) - 1)]
                    view_index += 1
                return subprocess.CompletedProcess(args, 0, json.dumps(value), "")
            if args[:4] == ["gh", "pr", "checks", str(PR)]:
                value = required if "--required" in args else checks
                return subprocess.CompletedProcess(args, 0, json.dumps(value), "")
            if args[:4] == ["gh", "pr", "merge", str(PR)]:
                merged = merge_rc == 0
                return subprocess.CompletedProcess(args, merge_rc, "", "merge failed" if merge_rc else "")
            raise AssertionError(args)

        return fake, commands

    def _no_github_required_checks(self, fake):
        def command(args, **kwargs):
            result = fake(args, **kwargs)
            if args[:4] == ["gh", "pr", "checks", str(PR)] and "--required" in args:
                return subprocess.CompletedProcess(
                    args,
                    1,
                    "",
                    "no required checks reported on the test branch",
                )
            return result

        return command

    def test_exact_green_head_is_merged_with_guard_and_verified_revision(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command([_view(self.module), _view(self.module)])
        with patch.object(self.module, "_command", side_effect=fake):
            merged = self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)

        self.assertEqual(merged, MERGED)
        merge_commands = [command for command in commands if command[:4] == ["gh", "pr", "merge", str(PR)]]
        self.assertEqual(len(merge_commands), 1)
        self.assertIn("--match-head-commit", merge_commands[0])
        self.assertIn(HEAD, merge_commands[0])
        self.assertIn("--merge", merge_commands[0])

    def test_stale_reviewed_head_fails_before_merge(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command([_view(self.module, head=MAIN)])
        with patch.object(self.module, "_command", side_effect=fake):
            with self.assertRaises(self.module.GuardError):
                self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)
        self.assertFalse(any(command[:4] == ["gh", "pr", "merge", str(PR)] for command in commands))

    def test_pending_required_check_fails_before_merge(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command(
            [_view(self.module), _view(self.module)],
            checks=_checks(self.module, state="PENDING"),
        )
        with patch.object(self.module, "_command", side_effect=fake):
            with self.assertRaises(self.module.GuardError):
                self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)
        self.assertFalse(any(command[:4] == ["gh", "pr", "merge", str(PR)] for command in commands))

    def test_no_github_required_checks_still_merges_with_policy_checks_green(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command([_view(self.module), _view(self.module)])
        with patch.object(self.module, "_command", side_effect=self._no_github_required_checks(fake)):
            merged = self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)

        self.assertEqual(merged, MERGED)
        required_commands = [
            command
            for command in commands
            if command[:4] == ["gh", "pr", "checks", str(PR)] and "--required" in command
        ]
        self.assertEqual(len(required_commands), 1)

    def test_no_github_required_checks_does_not_skip_policy_checks(self):
        self.repo_path = self._repo_path()
        policy_checks = _checks(self.module)
        fake, commands = self._fake_command(
            [_view(self.module), _view(self.module)],
            checks=policy_checks[:-1],
        )
        with patch.object(self.module, "_command", side_effect=self._no_github_required_checks(fake)):
            with self.assertRaises(self.module.GuardError) as raised:
                self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)
        self.assertEqual(
            str(raised.exception),
            "required check is missing: " + policy_checks[-1]["name"],
        )
        self.assertFalse(any(command[:4] == ["gh", "pr", "merge", str(PR)] for command in commands))

    def test_unrecognized_empty_github_required_response_fails_closed(self):
        args = ["gh", "pr", "checks", str(PR), "--required", "--json", self.module.CHECK_FIELDS]
        result = subprocess.CompletedProcess(args, 1, "", "permission denied")
        with patch.object(self.module, "_command", return_value=result):
            with self.assertRaisesRegex(self.module.GuardError, "command returned no JSON: gh"):
                self.module._gh_checks(REPO, PR, required=True)

    def test_conflicting_or_unknown_mergeability_fails_closed(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command(
            [_view(self.module, mergeable="UNKNOWN", merge_state="UNKNOWN")]
        )
        with patch.object(self.module, "_command", side_effect=fake):
            with self.assertRaises(self.module.GuardError):
                self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)
        self.assertFalse(any(command[:4] == ["gh", "pr", "merge", str(PR)] for command in commands))

    def test_lock_contention_returns_exit_75_without_remote_mutation(self):
        self.repo_path = self._repo_path()
        lock_path = self.repo_path / ".git" / "factory-final-merge.lock"
        lock_path.parent.mkdir(parents=True, exist_ok=True)
        with lock_path.open("a+") as held:
            self.module.fcntl.flock(held.fileno(), self.module.fcntl.LOCK_EX | self.module.fcntl.LOCK_NB)
            with patch.object(self.module, "_common_git_dir", return_value=lock_path.parent), patch.object(
                self.module, "_command"
            ) as command:
                result = self.module.main(["--repo", REPO, "--pr", str(PR), "--head", HEAD])
        self.assertEqual(result, self.module.LOCK_BUSY_EXIT)
        command.assert_not_called()

    def test_post_merge_verification_failure_is_not_success(self):
        self.repo_path = self._repo_path()
        fake, commands = self._fake_command([_view(self.module), _view(self.module)], merge_rc=0)

        def bad_merge_command(args, **kwargs):
            result = fake(args, **kwargs)
            if args[:4] == ["gh", "pr", "view", str(PR)] and "mergeCommit" in args[-1]:
                result.stdout = json.dumps({"number": PR, "state": "OPEN"})
            return result

        with patch.object(self.module, "_command", side_effect=bad_merge_command):
            with self.assertRaises(self.module.GuardError):
                self.module.guarded_merge(REPO, PR, HEAD, repo_path=self.repo_path)
        self.assertTrue(any(command[:4] == ["gh", "pr", "merge", str(PR)] for command in commands))


if __name__ == "__main__":
    unittest.main()
