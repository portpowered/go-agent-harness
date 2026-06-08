import importlib.util
import sys
import unittest
from pathlib import Path


SCRIPT_PATH = (
    Path(__file__).resolve().parents[1] / "validate_worktree_hygiene_convergence.py"
)
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location(
    "validate_worktree_hygiene_convergence",
    SCRIPT_PATH,
)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
sys.modules[SCRIPT_MODULE_SPEC.name] = SCRIPT_MODULE
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


class ValidateWorktreeHygieneConvergenceTests(unittest.TestCase):
    def test_find_session_id_matches_project_root_row(self):
        output = "\n".join(
            [
                "SESSION ID\tPROJECT\tFOLDER PATH\tFACTORY DIR\tDEFAULT\tTARGET KIND\tTARGET NAME",
                "~default\tfactory\t/Users/example/infinite-you/factory\t/Users/example/infinite-you/factory\tyes\tdefault\t",
                "session-123\tUNDEFINED\t/Users/example/go-agent-harness\t/Users/example/go-agent-harness/factory\tno\tnamed\tfactory",
            ]
        )

        session_id = SCRIPT_MODULE.find_session_id(
            output, Path("/Users/example/go-agent-harness")
        )

        self.assertEqual(session_id, "session-123")

    def test_evaluate_queue_recovery_passes_when_repair_items_are_terminal(self):
        session_list_result = SCRIPT_MODULE.CommandResult(
            args=["you", "session", "list"],
            returncode=0,
            stdout="ok",
            stderr="",
        )
        work_list_result = SCRIPT_MODULE.CommandResult(
            args=["you", "work", "list", "--session", "session-123"],
            returncode=0,
            stdout="\n".join(
                [
                    "WORK ID\tNAME\tWORK TYPE\tSTATE NAME\tSTATE TYPE\tRELATIONS",
                    "repair-idea\tphase-2-factory-worktree-hygiene-repair\tidea\tcomplete\tTERMINAL\tnone",
                    "repair-task\tphase-2-factory-worktree-hygiene-repair\ttask\tcomplete\tTERMINAL\tnone",
                    "repair-plan\tphase-2-factory-worktree-hygiene-repair\tplan\tcomplete\tTERMINAL\tnone",
                ]
            ),
            stderr="",
        )

        finding = SCRIPT_MODULE.evaluate_queue_recovery(
            project_root=Path("/Users/example/go-agent-harness"),
            session_list_result=session_list_result,
            work_list_result=work_list_result,
            session_id="session-123",
        )

        self.assertEqual(finding.outcome, "pass")
        self.assertIn("no manual `you work move` recovery is required", finding.required_follow_up)

    def test_render_report_includes_generated_command_and_verdict(self):
        findings = [
            SCRIPT_MODULE.Finding(
                group="checklist-convergence",
                subject="CTRL-FAC-01",
                outcome="pass",
                evidence="evidence",
                affected_surfaces="surfaces",
                required_follow_up="none",
            ),
            SCRIPT_MODULE.Finding(
                group="setup-workspace-behavior",
                subject="setup",
                outcome="pass",
                evidence="evidence",
                affected_surfaces="surfaces",
                required_follow_up="none",
            ),
            SCRIPT_MODULE.Finding(
                group="durable-queue-recovery",
                subject="queue",
                outcome="pass",
                evidence="evidence",
                affected_surfaces="surfaces",
                required_follow_up="none",
            ),
        ]

        report = SCRIPT_MODULE.render_report(
            generated_at="2026-06-08 02:00:00Z",
            session_id="session-123",
            findings=findings,
        )

        self.assertIn(
            "validate_worktree_hygiene_convergence.py --write-report docs/internal/phase-2-factory-worktree-hygiene-convergence-report.md",
            report,
        )
        self.assertIn("`pass`", report)


if __name__ == "__main__":
    unittest.main()
