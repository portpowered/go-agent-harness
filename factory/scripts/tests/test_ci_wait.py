"""Behavioral tests for the bounded CI wait gate.

These tests replace the GitHub CLI and the clock.  They exercise the script's
observable exit/JSON contract without contacting a remote repository or
waiting on wall-clock time.
"""

import importlib.util
import io
import json
import subprocess
import unittest
from contextlib import ExitStack, redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "ci-wait.py"
HEAD = "0123456789abcdef0123456789abcdef01234567"
NEXT_HEAD = "fedcba9876543210fedcba9876543210fedcba98"
CHECK_LINK = "https://github.com/example/actions/runs/1/jobs/2"
REQUIRED_ABSENT = object()


def _rollup(state="SUCCESS", head_name="Verification", link=CHECK_LINK):
    terminal = state not in {
        "PENDING",
        "QUEUED",
        "IN_PROGRESS",
        "WAITING",
        "REQUESTED",
        "EXPECTED",
    }
    return {
        "__typename": "CheckRun",
        "name": head_name,
        "detailsUrl": link,
        "workflowName": "CI",
        "status": "COMPLETED" if terminal else state,
        "conclusion": state if terminal else None,
    }


def _check(state="SUCCESS", head_name="Verification", link=CHECK_LINK):
    pending = state in {
        "PENDING",
        "QUEUED",
        "IN_PROGRESS",
        "WAITING",
        "REQUESTED",
        "EXPECTED",
    }
    bucket = "pending" if pending else "pass" if state == "SUCCESS" else "fail"
    return {
        "name": head_name,
        "state": state,
        "bucket": bucket,
        "link": link,
        "workflow": "CI",
    }


def _view(number=100, head=HEAD, state="OPEN", checks=None):
    return {
        "number": number,
        "state": state,
        "headRefOid": head,
        "statusCheckRollup": [_rollup()] if checks is None else checks,
    }


def _observation(view, checks, after=None, required=REQUIRED_ABSENT):
    return (view, checks, required, view if after is None else after)


def _load_module():
    spec = importlib.util.spec_from_file_location("ci_wait", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class CIWaitTests(unittest.TestCase):
    def setUp(self):
        self.module = _load_module()

    def _invoke(self, pr_number, observations, *, pr_state="OPEN", deadline=300,
                no_checks_grace=300, list_responses=None, policy=("Verification",)):
        responses = [item for observation in observations for item in observation]
        calls = []
        sleeps = []
        list_responses = list(list_responses or [])

        def run_gh(*args):
            calls.append(args)
            if args[1] == "list":
                if list_responses:
                    return list_responses.pop(0)
                return subprocess.CompletedProcess(
                    ["gh", *args],
                    0,
                    stdout=json.dumps([{"number": pr_number, "state": pr_state}]),
                    stderr="",
                )
            response = responses.pop(0)
            if response is REQUIRED_ABSENT:
                return subprocess.CompletedProcess(
                    ["gh", *args],
                    1,
                    stdout="",
                    stderr="no required checks reported",
                )
            if isinstance(response, BaseException):
                raise response
            if isinstance(response, subprocess.CompletedProcess):
                return response
            return subprocess.CompletedProcess(
                ["gh", *args], 0, stdout=json.dumps(response), stderr=""
            )

        stdout = io.StringIO()
        stderr = io.StringIO()
        with ExitStack() as stack:
            stack.enter_context(patch.object(self.module, "run_gh", side_effect=run_gh))
            stack.enter_context(
                patch.object(
                    self.module.sys, "argv", [str(SCRIPT_PATH), "codex/work-ci"]
                )
            )
            stack.enter_context(patch.object(self.module.time, "monotonic", return_value=0))
            stack.enter_context(patch.object(self.module.time, "sleep", side_effect=sleeps.append))
            stack.enter_context(patch.object(self.module, "DEADLINE_SECONDS", deadline))
            stack.enter_context(
                patch.object(self.module, "NO_CHECKS_GRACE_SECONDS", no_checks_grace)
            )
            stack.enter_context(
                patch.object(
                    self.module, "load_required_check_policy", return_value=tuple(policy)
                )
            )
            stack.enter_context(redirect_stdout(stdout))
            stack.enter_context(redirect_stderr(stderr))
            try:
                self.module.main()
            except SystemExit as error:
                exit_code = error.code
            else:
                exit_code = 0
        return exit_code, stdout.getvalue(), stderr.getvalue(), sleeps, calls

    def test_terminal_success_requires_two_stable_current_head_observations(self):
        view = _view()
        result = self._invoke(
            100,
            [_observation(view, [_check()]), _observation(view, [_check()])],
        )

        exit_code, stdout, stderr, sleeps, calls = result
        self.assertEqual(exit_code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["status"], "ready")
        self.assertEqual(payload["reason"], "checks-terminal")
        self.assertEqual(payload["headRefOid"], HEAD)
        self.assertEqual(payload["checkIdentities"][0]["state"], "SUCCESS")
        self.assertIsNone(payload["uncertainty"])
        self.assertEqual(sleeps, [self.module.POLL_INTERVAL_SECONDS])
        self.assertEqual(
            [call[1] for call in calls],
            ["list", "view", "checks", "checks", "view", "view", "checks", "checks", "view"],
        )
        self.assertNotIn("--required", calls[2])
        self.assertIn("--required", calls[3])

    def test_optional_pending_check_does_not_mask_passing_required_set(self):
        view = _view(
            number=110,
            checks=[
                _rollup(),
                _rollup(
                    state="IN_PROGRESS",
                    head_name="Optional documentation",
                    link="https://example.test/optional",
                ),
            ],
        )
        result = self._invoke(
            110,
            [
                _observation(
                    view,
                    [
                        _check(),
                        _check(
                            state="IN_PROGRESS",
                            head_name="Optional documentation",
                            link="https://example.test/optional",
                        ),
                    ],
                ),
                _observation(
                    view,
                    [
                        _check(),
                        _check(
                            state="IN_PROGRESS",
                            head_name="Optional documentation",
                            link="https://example.test/optional",
                        ),
                    ],
                ),
            ],
        )

        exit_code, stdout, stderr, _, _ = result
        self.assertEqual(exit_code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["checks"], 1)
        self.assertEqual(payload["checkIdentities"][0]["name"], "Verification")

    def test_explicit_policy_fallback_passes_when_github_required_set_is_absent(self):
        view = _view(number=111)
        exit_code, stdout, stderr, _, calls = self._invoke(
            111,
            [_observation(view, [_check()]), _observation(view, [_check()])],
            policy=("Verification",),
        )

        self.assertEqual(exit_code, 0, stderr)
        self.assertEqual(json.loads(stdout)["reason"], "checks-terminal")
        self.assertEqual(sum("--required" in call for call in calls), 2)

    def test_github_required_rows_are_added_to_the_authored_policy(self):
        github_only = _rollup(
            head_name="GitHub-only gate", link="https://example.test/github"
        )
        view = _view(number=113, checks=[_rollup(), github_only])
        required = [
            _check(
                head_name="GitHub-only gate", link="https://example.test/github"
            )
        ]
        exit_code, stdout, stderr, _, _ = self._invoke(
            113,
            [
                _observation(view, [_check(), required[0]], required=required),
                _observation(view, [_check(), required[0]], required=required),
            ],
        )

        self.assertEqual(exit_code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["checks"], 2)
        self.assertEqual(
            {check["name"] for check in payload["checkIdentities"]},
            {"Verification", "GitHub-only gate"},
        )

    def test_missing_policy_check_fails_closed(self):
        view = _view(number=114)
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            114,
            [_observation(view, [_check()])],
            deadline=0,
            policy=("Verification", "CI (missing)"),
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("policy-missing-check:CI (missing)", stderr)
        self.assertEqual(sleeps, [])

    def test_repository_policy_contains_the_authored_ci_gate_names(self):
        self.assertEqual(
            self.module.load_required_check_policy(),
            (
                "CI (static)",
                "CI (unit)",
                "CI (integration)",
                "CI (coverage)",
                "CI (race)",
                "CI (hermetic)",
                "CI (WebMCP Chrome)",
                "CI (macOS audio release)",
            ),
        )

    def test_failed_check_is_nonzero_and_never_emits_success_json(self):
        view = _view(number=101, checks=[_rollup(state="FAILURE")])
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            101,
            [_observation(view, [_check(state="FAILURE")])],
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("checks failed", stderr)
        self.assertIn("FAILURE", stderr)
        self.assertEqual(sleeps, [])

    def test_pending_check_times_out_without_becoming_false_green(self):
        view = _view(number=102, checks=[_rollup(state="IN_PROGRESS")])
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            102,
            [_observation(view, [_check(state="IN_PROGRESS")])],
            deadline=0,
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("timed out", stderr)
        self.assertNotIn("checks-terminal", stdout)
        self.assertEqual(sleeps, [])

    def test_empty_checks_fail_even_when_grace_is_zero(self):
        view = _view(number=103, checks=[])
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            103,
            [_observation(view, [])],
            no_checks_grace=0,
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("no required checks reported", stderr)
        self.assertEqual(sleeps, [])

    def test_head_change_during_observation_is_uncertain_and_fails_closed(self):
        before = _view(number=104, head=HEAD)
        after = _view(number=104, head=NEXT_HEAD)
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            104,
            [_observation(before, [_check()], after)],
            deadline=0,
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("head-changed-during-observation", stderr)
        self.assertEqual(sleeps, [])

    def test_check_source_mismatch_cannot_be_terminal(self):
        view = _view(number=105, checks=[_rollup(state="SUCCESS")])
        exit_code, stdout, stderr, sleeps, _ = self._invoke(
            105,
            [_observation(view, [_check(state="FAILURE")])],
            deadline=0,
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("check-state-mismatch", stderr)
        self.assertEqual(sleeps, [])

    def test_merged_pr_is_the_only_success_without_check_evidence(self):
        exit_code, stdout, stderr, sleeps, calls = self._invoke(
            106,
            [],
            pr_state="MERGED",
        )

        self.assertEqual(exit_code, 0, stderr)
        self.assertEqual(
            json.loads(stdout),
            {"status": "ready", "pr": 106, "prState": "MERGED", "reason": "pr-merged"},
        )
        self.assertEqual(sleeps, [])
        self.assertEqual([call[1] for call in calls], ["list"])

    def test_transient_github_lookup_retries_then_fails_nonzero(self):
        unavailable = subprocess.CompletedProcess(
            ["gh"], 1, stdout="", stderr="token=redacted-by-test"
        )
        exit_code, stdout, stderr, sleeps, calls = self._invoke(
            107,
            [],
            list_responses=[unavailable, unavailable, unavailable],
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("retry budget exhausted", stderr)
        self.assertNotIn("token=redacted-by-test", stderr)
        self.assertEqual(
            sleeps,
            [
                self.module.infrastructure_backoff_seconds(attempt)
                for attempt in range(1, self.module.PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS)
            ],
        )
        self.assertEqual(len(calls), self.module.PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS)

    def test_successful_missing_pr_lookups_are_nonzero(self):
        missing = subprocess.CompletedProcess(["gh"], 0, stdout="[]", stderr="")
        exit_code, stdout, stderr, sleeps, calls = self._invoke(
            108,
            [],
            list_responses=[missing] * self.module.PR_LOOKUP_ATTEMPTS,
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("found no PR", stderr)
        self.assertEqual(
            sleeps,
            [self.module.PR_LOOKUP_INTERVAL_SECONDS]
            * (self.module.PR_LOOKUP_ATTEMPTS - 1),
        )
        self.assertEqual(len(calls), self.module.PR_LOOKUP_ATTEMPTS)

    def test_closed_unmerged_pr_is_not_a_success(self):
        exit_code, stdout, stderr, sleeps, calls = self._invoke(
            109,
            [],
            pr_state="CLOSED",
        )

        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout, "")
        self.assertIn("CLOSED", stderr)
        self.assertEqual(sleeps, [])
        self.assertEqual([call[1] for call in calls], ["list"])


if __name__ == "__main__":
    unittest.main()
