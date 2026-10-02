import importlib.util
from pathlib import Path
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("prod_deps_check", Path(__file__).with_name("prod-deps-check.py"))
prod_deps_check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prod_deps_check)

ROOT = prod_deps_check.MODULE_ROOT


class ProdDepsCheckTest(unittest.TestCase):
    def test_classifies_test_support_paths(self):
        support = [
            "agent-cli/internal/webmcp/webmcptest",
            "agent-cli/internal/transport/cli/clitest",
            "agent-cli/test/integration",
            "go-agent-loop/test/functional/timeharness",
            "go-llm-gateway/pkg/testing",
            "agent-cli/internal/webmcp/testkit",
            "agent-cli/cmd/testtiming",
            "go-agent-loop/test/test_logging",
            "agent-cli/test/integration/testcmd/mocktool",
            "go-agent-loop/test/functional/internal/sessionmock",
            "agent-cli/internal/fakebrowser",
            "agent-cli/internal/stubprovider",
            "agent-cli/internal/providerstubs",
            "go-agent-loop/pkg/audiofixture",
            "agent-cli/internal/webmcp/fixtures",
            "go-agent-loop/test/functional/timeharness",
        ]
        for path in support:
            self.assertTrue(prod_deps_check.is_test_support(ROOT + path), path)
        production = [
            "agent-cli/internal/webmcp/hermetic",
            "agent-cli/cmd/agent",
            "go-agent-runtime/services/rooms",
            "go-agent-loop/pkg/probe",
            "go-llm-gateway/pkg/transport/rtc",
            "go-llm-gateway/internal/sessionfixturevalidator",
            "go-llm-gateway/cmd/session-fixture-validator",
            "agent-cli/internal/probe/customersim",
        ]
        for path in production:
            self.assertFalse(prod_deps_check.is_test_support(ROOT + path), path)
        # The module directory itself never counts, and other modules are out of scope.
        self.assertFalse(prod_deps_check.is_test_support("github.com/stretchr/testify/assert"))
        self.assertFalse(prod_deps_check.is_test_support("testing"))

    def test_reports_the_import_chain_and_honours_the_allowlist(self):
        main = ROOT + "agent-cli/cmd/agent"
        packages = [
            {"ImportPath": main, "Imports": [ROOT + "agent-cli/internal/a", ROOT + "go-llm-gateway/pkg/testing"]},
            {"ImportPath": ROOT + "agent-cli/internal/a", "Imports": [ROOT + "agent-cli/internal/webmcp/webmcptest"]},
            {"ImportPath": ROOT + "agent-cli/internal/webmcp/webmcptest", "Imports": []},
            {"ImportPath": ROOT + "go-llm-gateway/pkg/testing", "Imports": []},
        ]
        with mock.patch.object(prod_deps_check, "go_list", return_value=packages):
            problems = prod_deps_check.violations(main, ("linux", {}, ""))
        self.assertEqual(len(problems), 1)
        self.assertIn("links test-support package " + ROOT + "agent-cli/internal/webmcp/webmcptest", problems[0])
        self.assertIn(main + " -> " + ROOT + "agent-cli/internal/a -> " + ROOT + "agent-cli/internal/webmcp/webmcptest", problems[0])

    def test_unresolved_packages_fail(self):
        main = ROOT + "agent-cli/cmd/agent"
        packages = [
            {"ImportPath": main, "Imports": [], "DepsErrors": [{"Err": "cannot find package"}]},
            {"ImportPath": ROOT + "agent-cli/internal/gone", "Error": {"Err": "no Go files"}},
        ]
        with mock.patch.object(prod_deps_check, "go_list", return_value=packages):
            problems = prod_deps_check.violations(main, ("linux-e2e", {}, "e2e"))
        self.assertEqual(len(problems), 2)
        self.assertTrue(all("linux-e2e: " + main + " does not resolve" in problem for problem in problems), problems)

    def test_configs_cover_cgo_and_ci_tag_sets(self):
        names = [name for name, _, _ in prod_deps_check.configs()]
        self.assertTrue({"linux", "darwin", "windows", "linux-wireinject", "linux-nomicrophone", "linux-e2e"} <= set(names), names)
        self.assertTrue(any(name.endswith("-cgo") for name in names), names)

    def test_production_mains_exclude_test_tool_mains(self):
        packages = [
            {"ImportPath": ROOT + "agent-cli/cmd/agent", "Name": "main"},
            {"ImportPath": ROOT + "agent-cli/cmd/testtiming", "Name": "main"},
            {"ImportPath": ROOT + "agent-cli/test/integration/testcmd/mock-tool-agent", "Name": "main"},
            {"ImportPath": ROOT + "agent-cli/internal/webmcp", "Name": "webmcp"},
        ]
        with mock.patch.object(prod_deps_check, "go_list", return_value=packages):
            self.assertEqual(prod_deps_check.production_mains(), [ROOT + "agent-cli/cmd/agent"])

    def test_main_fails_on_a_violation(self):
        with mock.patch.object(prod_deps_check, "production_mains", return_value=[ROOT + "agent-cli/cmd/agent"]), \
                mock.patch.object(prod_deps_check, "configs", return_value=[("linux", {}, ""), ("darwin", {}, "")]), \
                mock.patch.object(prod_deps_check, "violations", side_effect=lambda main, config: [f"{config[0]}: bad"] if config[0] == "linux" else []), \
                mock.patch("sys.stderr"):
            self.assertEqual(prod_deps_check.main([]), 1)
        with mock.patch.object(prod_deps_check, "production_mains", return_value=[ROOT + "agent-cli/cmd/agent"]), \
                mock.patch.object(prod_deps_check, "violations", return_value=[]), \
                mock.patch("sys.stdout"):
            self.assertEqual(prod_deps_check.main([]), 0)


if __name__ == "__main__":
    unittest.main()
