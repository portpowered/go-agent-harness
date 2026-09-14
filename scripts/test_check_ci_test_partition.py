import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "check_ci_test_partition",
    Path(__file__).with_name("check-ci-test-partition.py"),
)
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


def workflow(*job_targets):
    jobs = ["name: CI", "jobs:"]
    for job, target in job_targets:
        jobs.extend(
            [
                f"  {job}:",
                "    steps:",
                f"      - run: make {target}",
            ]
        )
    return "\n".join(jobs)


class CITestPartitionTest(unittest.TestCase):
    def test_disjoint_coverage_shards_own_full_corpus(self):
        text = workflow(
            ("coverage-agent-cli", "coverage-ci-agent-cli"),
            ("coverage-libraries", "coverage-ci-libraries"),
        )
        self.assertEqual(check.ownership_errors(text), [])

    def test_hermetic_job_duplicates_both_coverage_shards(self):
        text = workflow(
            ("coverage-agent-cli", "coverage-ci-agent-cli"),
            ("coverage-libraries", "coverage-ci-libraries"),
            ("hermetic", "test-hermetic"),
        )
        errors = check.ownership_errors(text)
        self.assertTrue(any("agent-cli/...: multiple CI owners" in error for error in errors))
        self.assertTrue(any("go-agent-loop/...: multiple CI owners" in error for error in errors))

    def test_integration_and_embedding_reintroductions_are_rejected(self):
        text = workflow(
            ("coverage-agent-cli", "coverage-ci-agent-cli"),
            ("coverage-libraries", "coverage-ci-libraries"),
            ("integration", "test-integration"),
            ("unit", "embed-check"),
        )
        errors = check.ownership_errors(text)
        self.assertTrue(any("agent-cli/...: multiple CI owners" in error for error in errors))
        self.assertTrue(any("tests/embedding/...: multiple CI owners" in error for error in errors))

    def test_missing_shard_reports_every_unowned_corpus(self):
        text = workflow(("coverage-agent-cli", "coverage-ci-agent-cli"))
        errors = check.ownership_errors(text)
        self.assertEqual(len(errors), len(check.CANONICAL_CORPORA) - 1)
        self.assertTrue(all("no CI owner" in error for error in errors))

    def test_block_command_with_environment_and_make_flags_is_detected(self):
        text = workflow(
            ("coverage-agent-cli", "coverage-ci-agent-cli"),
            ("coverage-libraries", "coverage-ci-libraries"),
        )
        text += (
            "\n  hidden-hermetic:\n"
            "    steps:\n"
            "      - run: |\n"
            "          CI=1 make --silent test-hermetic\n"
        )
        errors = check.ownership_errors(text)
        self.assertTrue(any("agent-cli/...: multiple CI owners" in error for error in errors))

    def test_make_directory_flag_does_not_hide_target(self):
        self.assertEqual(check.make_target("run: make -C . test-integration"), "test-integration")


if __name__ == "__main__":
    unittest.main()
