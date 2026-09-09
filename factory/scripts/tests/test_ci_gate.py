"""Behavioral tests for the script-owned CI decision-envelope adapter."""

import importlib.util
import io
import json
import tempfile
import types
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "ci-gate.py"
HEAD = "0123456789abcdef0123456789abcdef01234567"
NEXT_HEAD = "fedcba9876543210fedcba9876543210fedcba98"
JOB_URL = "https://github.com/example/actions/runs/1/jobs/2?token=do-not-leak"


def _load_module():
    spec = importlib.util.spec_from_file_location("ci_gate", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def _success(head=HEAD, reason="checks-terminal"):
    return {
        "status": "ready",
        "pr": 42,
        "prState": "OPEN" if reason != "pr-merged" else "MERGED",
        "reason": reason,
        "headRefOid": head,
        "checks": 8,
    }


class CIGateTests(unittest.TestCase):
    def setUp(self):
        self.module = _load_module()

    def test_safe_text_strips_terminal_controls_and_redacts_separated_credentials(self):
        unsafe = (
            "\x1b[31mAuthorization: Bearer TOPSECRET\x1b[0m "
            "token=SECONDSECRET api_key: THIRDSECRET\x80"
        )

        safe = self.module._safe_text(unsafe)

        self.assertNotIn("TOPSECRET", safe)
        self.assertNotIn("SECONDSECRET", safe)
        self.assertNotIn("THIRDSECRET", safe)
        self.assertNotIn("\x1b", safe)
        self.assertFalse(any(0 <= ord(character) < 32 for character in safe))
        self.assertFalse(any(0x7F <= ord(character) <= 0x9F for character in safe))
        self.assertIn("Authorization=[redacted]", safe)
        self.assertIn("token=[redacted]", safe)
        self.assertIn("api_key=[redacted]", safe)

    def test_safe_text_redacts_standalone_bearer_and_quoted_secret_values(self):
        safe = self.module._safe_text(
            'request Bearer STANDALONESECRET and "token": "QUOTEDSECRET"'
        )

        self.assertNotIn("STANDALONESECRET", safe)
        self.assertNotIn("QUOTEDSECRET", safe)
        self.assertIn("Bearer [redacted]", safe)
        self.assertIn('"token"=[redacted]', safe)

    def test_failed_checks_route_to_executor_with_bounded_safe_evidence(self):
        failure = types.SimpleNamespace(
            kind="checks-failed",
            reason="required checks failed",
            head_ref_oid=HEAD,
            pr=42,
            checks=[
                {
                    "name": "CI (unit)",
                    "state": "FAILURE",
                    "link": JOB_URL,
                }
            ],
        )
        run = self.module.CIWaitRun(1, "", "token=raw-secret\nfull log", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("CI (unit)=FAILURE", feedback)
        self.assertIn("https://github.com/example/actions/runs/1/jobs/2", feedback)
        self.assertNotIn("token=do-not-leak", feedback)
        self.assertNotIn("raw-secret", feedback)
        self.assertIn("executor", feedback)

    def test_infrastructure_failure_routes_to_meta_without_stderr_dump(self):
        failure = types.SimpleNamespace(
            kind="infrastructure",
            reason="GitHub PR lookup retry budget exhausted",
            head_ref_oid="",
            pr=None,
            checks=(),
        )
        run = self.module.CIWaitRun(1, "", "password=raw-secret\nlog dump", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "FAILED")
        self.assertIn("Meta-planner intervention", feedback)
        self.assertNotIn("password=raw-secret", feedback)
        self.assertNotIn("log dump", feedback)

    def test_current_head_conflict_routes_to_same_executor_with_exact_repair(self):
        failure = types.SimpleNamespace(
            kind="conflicting",
            reason="open PR current head is explicitly mergeable=CONFLICTING",
            head_ref_oid=HEAD,
            pr=412,
            checks=(),
        )
        run = self.module.CIWaitRun(1, "", "", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=HEAD
        ):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("PR #412", feedback)
        self.assertIn(HEAD, feedback)
        self.assertIn("mergeable=CONFLICTING", feedback)
        self.assertIn("fetch origin main", feedback)
        self.assertIn("merge main", feedback)
        self.assertIn("preserving both sides", feedback)
        self.assertIn("focused regressions", feedback)
        self.assertIn("changed head", feedback)
        self.assertIn("same PR", feedback)

    def test_stale_conflict_head_is_rejected_without_claiming_current_conflict(self):
        failure = types.SimpleNamespace(
            kind="conflicting",
            reason="open PR current head is explicitly mergeable=CONFLICTING",
            head_ref_oid=NEXT_HEAD,
            pr=412,
            checks=(),
        )
        run = self.module.CIWaitRun(1, "", "", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=HEAD
        ):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("stale", feedback)
        self.assertIn(NEXT_HEAD, feedback)
        self.assertIn(HEAD, feedback)
        self.assertNotIn("current candidate head is explicitly", feedback)
        self.assertNotIn("fetch origin main", feedback)

    def test_unvalidated_conflict_evidence_fails_closed(self):
        failure = types.SimpleNamespace(
            kind="conflicting",
            reason="untrusted conflict",
            head_ref_oid="short-head",
            pr=412,
            checks=(),
        )
        run = self.module.CIWaitRun(1, "", "", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=HEAD
        ):
            with self.assertRaises(self.module.GateError):
                self.module.evaluate(Path("/managed/worktree"), "sample-work", HEAD)

    def test_local_candidate_drift_does_not_confirm_old_conflict(self):
        failure = types.SimpleNamespace(
            kind="conflicting",
            reason="open PR current head is explicitly mergeable=CONFLICTING",
            head_ref_oid=HEAD,
            pr=412,
            checks=(),
        )
        run = self.module.CIWaitRun(1, "", "", failure)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=NEXT_HEAD
        ):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("changed during conflict observation", feedback)
        self.assertIn("not treated as current", feedback)
        self.assertNotIn("fetch origin main", feedback)

    def test_terminal_success_requires_current_local_head(self):
        run = self.module.CIWaitRun(0, json.dumps(_success()), "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=HEAD
        ) as git_output:
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "ACCEPTED")
        self.assertIn("PR #42", feedback)
        self.assertIn(HEAD, feedback)
        self.assertIn("ready for independent review", feedback)
        git_output.assert_called_once_with(Path("/managed/worktree"), "rev-parse", "HEAD")

    def test_stale_ci_head_routes_back_to_executor(self):
        run = self.module.CIWaitRun(0, json.dumps(_success(NEXT_HEAD)), "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output"
        ) as git_output:
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("stale head", feedback)
        self.assertIn("executor", feedback)
        git_output.assert_not_called()

    def test_local_head_change_during_polling_routes_back_to_executor(self):
        run = self.module.CIWaitRun(0, json.dumps(_success()), "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_git_output", return_value=NEXT_HEAD
        ):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("changed during CI polling", feedback)
        self.assertIn("restart CI", feedback)

    def test_merged_shortcut_requires_pr_head_and_routes_reviewer_reconciliation(self):
        run = self.module.CIWaitRun(0, json.dumps(_success(reason="pr-merged")), "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_read_merged_pr_head", return_value=HEAD
        ), patch.object(self.module, "_git_output", return_value=HEAD):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "ACCEPTED")
        self.assertIn("already merged", feedback)
        self.assertIn("idempotent merged-state reconciliation", feedback)
        self.assertNotIn("CI passed", feedback)
        self.assertNotIn("checks verified", feedback)

    def test_merged_shortcut_with_stale_pr_head_returns_executor_rejection(self):
        run = self.module.CIWaitRun(0, json.dumps(_success(reason="pr-merged")), "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run), patch.object(
            self.module, "_read_merged_pr_head", return_value=NEXT_HEAD
        ):
            decision, feedback = self.module.evaluate(
                Path("/managed/worktree"), "sample-work", HEAD
            )

        self.assertEqual(decision, "REJECTED")
        self.assertIn("stale head", feedback)

    def test_invalid_waiter_success_is_infrastructure_failure(self):
        run = self.module.CIWaitRun(0, "not-json", "", None)

        with patch.object(self.module, "run_ci_wait", return_value=run):
            with self.assertRaises(self.module.GateError):
                self.module.evaluate(Path("/managed/worktree"), "sample-work", HEAD)

    def test_main_emits_one_exit_zero_decision_envelope(self):
        output = io.StringIO()
        with patch.object(self.module, "_factory_root", return_value=Path("/factory")), patch.object(
            self.module, "resolve_worktree", return_value=Path("/factory/.claude/worktrees/sample-work")
        ), patch.object(self.module, "pin_local_head", return_value=HEAD), patch.object(
            self.module,
            "evaluate",
            return_value=("REJECTED", "CI checks failed at the pinned head"),
        ), redirect_stdout(output):
            exit_code = self.module.main(["sample-work"])

        self.assertEqual(exit_code, 0)
        self.assertEqual(
            json.loads(output.getvalue()),
            {"decision": "REJECTED", "feedback": "CI checks failed at the pinned head"},
        )

    def test_resolve_worktree_accepts_only_a_managed_linked_worktree(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            worktree = root / ".claude/worktrees/sample-work"
            worktree.mkdir(parents=True)
            (worktree / ".git").write_text("gitdir: ../.git/worktrees/sample-work\n")

            self.assertEqual(
                self.module.resolve_worktree(root, "sample-work"), worktree.resolve()
            )
            with self.assertRaises(self.module.GateError):
                self.module.resolve_worktree(root, "../outside")


if __name__ == "__main__":
    unittest.main()
