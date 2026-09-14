#!/usr/bin/env python3
"""Regression tests for the source-derived C109 Go resolver."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ANALYZER_PATH = Path(__file__).with_name("analyze.py")
PUBLIC_RUNNER_PATH = Path(__file__).with_name("run_public_checks.py")
SPEC = importlib.util.spec_from_file_location("c109_analyze", ANALYZER_PATH)
assert SPEC is not None and SPEC.loader is not None
ANALYZER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ANALYZER)

sys.path.insert(0, str(PUBLIC_RUNNER_PATH.parent))
RUNNER_SPEC = importlib.util.spec_from_file_location("c109_public_runner", PUBLIC_RUNNER_PATH)
assert RUNNER_SPEC is not None and RUNNER_SPEC.loader is not None
RUNNER = importlib.util.module_from_spec(RUNNER_SPEC)
RUNNER_SPEC.loader.exec_module(RUNNER)

VERIFY_PATH = PUBLIC_RUNNER_PATH.with_name("verify.py")
VERIFY_SPEC = importlib.util.spec_from_file_location("c109_verify", VERIFY_PATH)
assert VERIFY_SPEC is not None and VERIFY_SPEC.loader is not None
VERIFY = importlib.util.module_from_spec(VERIFY_SPEC)
VERIFY_SPEC.loader.exec_module(VERIFY)


def git(root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=root,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=True,
    )
    return result.stdout.strip()


class AnalyzerResolutionTests(unittest.TestCase):
    def test_anonymous_functions_do_not_create_keyword_declarations(self) -> None:
        source = """package sample

func Outer() {
	go func() {
		for {
			break
		}
	}()
}
"""
        symbols = [item["symbol"] for item in ANALYZER.declarations(source)]
        self.assertEqual(symbols, ["Outer"])
        self.assertIn(".", [item["value"] for item in ANALYZER.go_tokens("package sample\nx.Call()")])

    def test_api_resolution_uses_module_paths_and_package_identity(self) -> None:
        with tempfile.TemporaryDirectory(prefix="c109-analyzer-test-") as temporary:
            root = Path(temporary)
            git(root, "init", "--quiet")
            git(root, "config", "user.name", "C109 test")
            git(root, "config", "user.email", "c109-test@example.invalid")
            module = root / "go-agent-runtime" / "go.mod"
            module.parent.mkdir(parents=True)
            module.write_text("module github.com/example/runtime\n\n go 1.23\n", encoding="utf-8")
            git(root, "add", ".")
            git(root, "commit", "--quiet", "-m", "base")
            base = git(root, "rev-parse", "HEAD")

            public = root / "go-agent-runtime" / "services" / "browserscenario"
            public.mkdir(parents=True)
            (public / "service.go").write_text(
                """package browserscenario

func NewService() {}
func Shared() {}
func Added() {
	go func() {
		for {
			break
		}
	}()
}
""",
                encoding="utf-8",
            )
            git(root, "add", ".")
            git(root, "commit", "--quiet", "-m", "c61")
            c61 = git(root, "rev-parse", "HEAD")

            consumer = root / "consumer" / "consumer.go"
            consumer.parent.mkdir(parents=True)
            consumer.write_text(
                """package consumer

import browser "github.com/example/runtime/services/browserscenario"

func Use() {
	browser.NewService()
}
""",
                encoding="utf-8",
            )
            other = root / "other" / "other.go"
            other.parent.mkdir(parents=True)
            other.write_text(
                """package other

func Shared() {}

func Use() {
	Shared()
	go func() {
		for {
			break
		}
	}()
}
""",
                encoding="utf-8",
            )
            git(root, "add", ".")
            git(root, "commit", "--quiet", "-m", "c83")
            c83 = git(root, "rev-parse", "HEAD")

            original_main = ANALYZER.ACCEPTED_MAIN
            ANALYZER.ACCEPTED_MAIN = base
            ANALYZER.source_model.cache_clear()
            ANALYZER.ref_go_paths.cache_clear()
            try:
                c61_paths = ["go-agent-runtime/services/browserscenario/service.go"]
                c83_paths = ["consumer/consumer.go", "other/other.go"]
                resolved = ANALYZER.api_relationship(root, c61, c83, c61_paths, c83_paths)
                self.assertEqual(
                    resolved["c61_public_import_paths"],
                    ["github.com/example/runtime/services/browserscenario"],
                )
                qualified = [item for item in resolved["edges"] if item["kind"] == "qualified-public-symbol"]
                self.assertEqual(len(qualified), 1)
                self.assertEqual(qualified[0]["target"]["symbol"], "NewService")
                self.assertTrue(resolved["c83_requires_c61_api"])
                self.assertFalse(any(item["target"]["symbol"] == "for" for item in resolved["edges"]))

                standalone = ANALYZER.api_relationship(root, c61, c83, c61_paths, ["other/other.go"])
                self.assertEqual(standalone["edges"], [])
                self.assertFalse(standalone["c83_requires_c61_api"])
            finally:
                ANALYZER.ACCEPTED_MAIN = original_main
                ANALYZER.source_model.cache_clear()
                ANALYZER.ref_go_paths.cache_clear()


class PublicRunnerArgumentTests(unittest.TestCase):
    def test_declared_public_command_accepts_omitted_output_path(self) -> None:
        with tempfile.TemporaryDirectory(prefix="c109-public-runner-test-") as temporary:
            missing_tree = Path(temporary) / "missing-tree"
            completed = subprocess.run(
                [
                    "python3",
                    str(PUBLIC_RUNNER_PATH),
                    "--case",
                    "malformed-or-canceled",
                    "--tree",
                    str(missing_tree),
                    "--child-timeout",
                    "1",
                    "--aggregate-timeout",
                    "1",
                ],
                cwd=PUBLIC_RUNNER_PATH.parents[5],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                check=False,
            )
            self.assertEqual(completed.returncode, 2)
            self.assertIn("provided synthetic tree does not exist", completed.stdout)
            self.assertNotIn("the following arguments are required: --output", completed.stdout)

    def test_output_path_inside_caller_tree_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="c109-public-runner-tree-") as temporary:
            caller_tree = Path(temporary) / "tree"
            output = caller_tree / "report.json"
            with self.assertRaises(RUNNER.PublicCheckError):
                RUNNER.validate_output_path(output, caller_tree)

    def test_short_platform_controls_precede_long_integration_stress(self) -> None:
        labels = [item[3] for item in RUNNER.command_set("browser-audio-tool", Path("."), 90)]
        devices = labels.index("accumulated session CI regressions race devices")
        openai = labels.index("accumulated session CI regressions race openai")
        stress = labels.index("accumulated session CI regressions race integration stress")
        self.assertLess(devices, stress)
        self.assertLess(openai, stress)


class PublicRunnerCleanupTests(unittest.TestCase):
    def test_cleanup_reaps_surviving_group_after_leader_closes_stdout(self) -> None:
        silent_descendant = "import os,time; os.close(1); os.close(2); time.sleep(30)"
        leader = "import subprocess,sys; subprocess.Popen([sys.executable, '-c', " + repr(silent_descendant) + "])"

        result = RUNNER.bounded(
            [sys.executable, "-c", leader],
            Path.cwd(),
            1,
            label="exiting leader with silent descendant regression",
        )

        self.assertEqual(result["status"], "timeout")
        self.assertTrue(result["timed_out"])
        self.assertTrue(result["cleanup"]["term_sent"] or result["cleanup"]["kill_sent"])
        self.assertTrue(result["process_group_gone"])


class PublicRunnerExceptionCleanupTests(unittest.TestCase):
    def test_caller_tree_is_reported_and_preserved_on_exception(self) -> None:
        with tempfile.TemporaryDirectory(prefix="c109-caller-exception-") as temporary:
            temporary_root, worktree, expected_tree = RUNNER.create_required_tree()
            output = Path(temporary) / "exception-report.json"
            original_command_set = RUNNER.command_set
            original_argv = sys.argv
            try:
                RUNNER.command_set = lambda *_args: (_ for _ in ()).throw(RUNNER.PublicCheckError("forced caller-tree exception"))
                sys.argv = [
                    str(PUBLIC_RUNNER_PATH),
                    "--case",
                    "malformed-or-canceled",
                    "--tree",
                    str(worktree),
                    "--output",
                    str(output),
                    "--child-timeout",
                    "1",
                    "--aggregate-timeout",
                    "1",
                ]
                self.assertEqual(RUNNER.main(), 2)
                report = json.loads(output.read_text(encoding="utf-8"))
                cleanup = report["cleanup"]
                self.assertEqual(cleanup["mode"], "caller-owned-tree-preserved")
                self.assertEqual(cleanup["status"], "passed")
                self.assertTrue(cleanup["tree_preserved"])
                self.assertTrue(cleanup["identity_unchanged"])
            finally:
                sys.argv = original_argv
                RUNNER.command_set = original_command_set
                cleanup = RUNNER.cleanup_tree(
                    temporary_root,
                    worktree,
                    expected_binding={"head": RUNNER.revision(RUNNER.ROOT, "HEAD", cwd=worktree), "final_tree": expected_tree, "status": []},
                )
                self.assertEqual(cleanup["status"], "passed")


class PublicReportValidationTests(unittest.TestCase):
    def test_incomplete_public_report_is_rejected(self) -> None:
        child_timeout = 90
        expected = RUNNER.command_set("browser-audio-tool", Path("."), child_timeout)
        report = {
            "case": "browser-audio-tool",
            "bounded": {"child_timeout_seconds": child_timeout, "aggregate_timeout_seconds": 300},
            "checks": [{"label": label, "command": " ".join(argv)} for argv, _cwd, _env, label, _timeout in expected],
        }
        mutated = VERIFY.mutate_public(report, "incomplete-public-report")
        with self.assertRaises(VERIFY.VerificationError):
            VERIFY.validate_public_check_set(mutated)


if __name__ == "__main__":
    unittest.main()
