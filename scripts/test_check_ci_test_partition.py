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

    def test_disjoint_agent_cli_shard_jobs_own_agent_cli_together(self):
        text = workflow(
            ("coverage-agent-cli-unit", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=unit"),
            ("coverage", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=integration"),
            ("coverage-libraries", "coverage-ci-libraries"),
        )
        self.assertEqual(check.ownership_errors(text), [])
        self.assertEqual(
            check.corpus_owner_jobs(text)["agent-cli/..."],
            ["coverage-agent-cli-unit (agent-cli unit packages)", "coverage (agent-cli/test/integration)"],
        )

    def test_missing_or_duplicated_agent_cli_part_is_rejected(self):
        only_unit = workflow(
            ("coverage-agent-cli-unit", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=unit"),
            ("coverage-libraries", "coverage-ci-libraries"),
        )
        self.assertEqual(
            check.ownership_errors(only_unit),
            ["agent-cli/...: no CI owner for agent-cli/test/integration"],
        )
        twice = workflow(
            ("a", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=unit"),
            ("b", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=integration"),
            ("c", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=integration"),
            ("coverage-libraries", "coverage-ci-libraries"),
        )
        self.assertTrue(any("multiple CI owners for agent-cli/test/integration" in error for error in check.ownership_errors(twice)))

    def test_agent_cli_part_beside_a_whole_corpus_owner_is_rejected(self):
        text = workflow(
            ("coverage-agent-cli-unit", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=unit"),
            ("coverage", "coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD=integration"),
            ("coverage-libraries", "coverage-ci-libraries"),
            ("stress", "test-audio-stress"),
        )
        errors = check.ownership_errors(text)
        self.assertEqual(len(errors), 1)
        self.assertIn("agent-cli/...: multiple CI owners", errors[0])

    def test_matrix_or_single_integration_shard_counts_as_whole_corpus(self):
        for shard in ("${{ matrix.shard }}", "integration-2", "all"):
            with self.subTest(shard=shard):
                text = workflow(
                    ("coverage-agent-cli", f"coverage-ci-agent-cli AGENT_CLI_COVERAGE_SHARD={shard}"),
                    ("coverage-libraries", "coverage-ci-libraries"),
                )
                self.assertEqual(check.ownership_errors(text), [])

    def test_make_directory_flag_does_not_hide_target(self):
        self.assertEqual(check.make_target("run: make -C . test-integration"), "test-integration")


if __name__ == "__main__":
    unittest.main()
