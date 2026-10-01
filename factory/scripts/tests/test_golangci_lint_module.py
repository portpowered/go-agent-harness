import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT_PATH = REPO_ROOT / "scripts" / "golangci-lint-module.sh"


class GolangCILintModuleTests(unittest.TestCase):
    def test_passes_config_and_arguments_from_module_directory(self):
        with tempfile.TemporaryDirectory(prefix="golangci-module-argv-") as temp_dir:
            root = Path(temp_dir).resolve()  # macOS: /tmp and /var are symlinks under /private
            self._fixture_repo(root, "alpha")
            (root / "hard.yml").write_text('version: "2"\n', encoding="utf-8")
            recorded = root / "argv.json"
            analyzer = self._fake_analyzer(
                root,
                """import json
import os
import sys
from pathlib import Path

Path(os.environ[\"FAKE_ARGV\"]).write_text(json.dumps({
    \"argv\": sys.argv[1:],
    \"cwd\": os.getcwd(),
}), encoding=\"utf-8\")
print(\"0 issues.\")
""",
            )

            result = self._run(
                root,
                analyzer,
                "--module",
                "alpha",
                "--config",
                "hard.yml",
                "--",
                "./...",
                env={"FAKE_ARGV": str(recorded)},
            )

            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            call = json.loads(recorded.read_text(encoding="utf-8"))
            self.assertEqual(
                call["argv"], ["run", "--config", str(root / "hard.yml"), "./..."]
            )
            self.assertEqual(Path(call["cwd"]).resolve(), root / "alpha")

    def test_untracked_violation_fails(self):
        analyzer = os.environ.get("GOLANGCI_LINT") or shutil.which("golangci-lint")
        if not analyzer:
            self.skipTest("golangci-lint is not installed")

        with tempfile.TemporaryDirectory(prefix="golangci-module-violation-") as temp_dir:
            root = Path(temp_dir).resolve()
            self._fixture_repo(root, ".")
            (root / ".golangci.yml").write_text(
                'version: "2"\nrun:\n  tests: true\nlinters:\n  default: none\n  enable:\n    - errcheck\n',
                encoding="utf-8",
            )
            (root / "fixture" / "new.go").write_text(
                'package fixture\n\nimport "os"\n\nfunc New() {\n\tos.Chdir("/")\n}\n',
                encoding="utf-8",
            )

            result = self._run(root, analyzer, "--module", ".", "--", "./...")

            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0, output)
            self.assertIn("fixture/new.go", output)
            self.assertIn("errcheck", output)

    def test_loader_error_fails_even_when_analyzer_returns_zero(self):
        with tempfile.TemporaryDirectory(prefix="golangci-module-loader-") as temp_dir:
            root = Path(temp_dir).resolve()
            self._fixture_repo(root, ".")
            analyzer = self._fake_analyzer(
                root,
                """import sys
print('level=error msg=\"[linters_context] typechecking error: package graph unavailable\"', file=sys.stderr)
print('0 issues.')
""",
            )

            result = self._run(root, analyzer, "--module", ".")

            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0, output)
            self.assertIn("typechecking error", output)
            self.assertIn("0 issues.", output)

    def _run(self, root, analyzer, *arguments, env=None):
        index_dir = root / "temporary-files"
        index_dir.mkdir(exist_ok=True)
        result = subprocess.run(
            [str(SCRIPT_PATH), "--analyzer", str(analyzer), "--repo", str(root), *arguments],
            cwd=root,
            env={**os.environ, "TMPDIR": str(index_dir), **(env or {})},
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(list(index_dir.iterdir()), [])
        return result

    @staticmethod
    def _fake_analyzer(root, body):
        analyzer = root / "fake-analyzer.py"
        analyzer.write_text("#!/usr/bin/env python3\n" + body, encoding="utf-8")
        analyzer.chmod(0o755)
        return analyzer

    @staticmethod
    def _fixture_repo(root, module):
        module_dir = root / module
        (module_dir / "fixture").mkdir(parents=True)
        (module_dir / "go.mod").write_text(
            "module example.com/lint-fixture\n\ngo 1.24\n", encoding="utf-8"
        )
        (module_dir / "fixture" / "base.go").write_text(
            "package fixture\n\nfunc Base() {}\n", encoding="utf-8"
        )
        subprocess.run(["git", "init", "-q"], cwd=root, check=True)


if __name__ == "__main__":
    unittest.main()
