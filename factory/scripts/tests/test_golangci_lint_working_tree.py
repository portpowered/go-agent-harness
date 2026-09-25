import os
import hashlib
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from uuid import uuid4


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT_PATH = REPO_ROOT / "scripts" / "golangci-lint-working-tree.sh"


class GolangCILintWorkingTreeTests(unittest.TestCase):
    def test_snapshot_only_contains_module_go_and_uses_temporary_objects(self):
        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-snapshot-") as temp_dir:
            outer = Path(temp_dir)
            root = outer / "repo"
            root.mkdir()
            (root / "fixture").mkdir()
            index_dir = outer / "temporary-indexes"
            index_dir.mkdir()
            inspection = outer / "inspection.json"
            (root / "go.mod").write_text(
                "module example.com/architecture-gate-fixture\n\ngo 1.24\n",
                encoding="utf-8",
            )
            (root / ".gitignore").write_text("ignored.go\n", encoding="utf-8")
            (root / "fixture" / "base.go").write_text(
                "package fixture\n\nfunc Base() {}\n", encoding="utf-8"
            )
            self._git(root, "init", "-q")
            self._git(root, "config", "user.email", "architecture-gate@example.com")
            self._git(root, "config", "user.name", "Architecture Gate")
            self._git(root, "add", ".")
            self._git(root, "commit", "-qm", "fixture baseline")
            baseline_tree = self._git(root, "write-tree").stdout.strip()
            repository_objects = Path(
                self._git(root, "rev-parse", "--git-path", "objects").stdout.strip()
            )

            new_source = (
                "package fixture\n\nfunc New() {}\n".encode("utf-8")
            )
            non_go = f"unique non-Go payload {uuid4()}\n".encode("utf-8")
            (root / "fixture" / "new.go").write_bytes(new_source)
            (root / "fixture" / "notes.txt").write_bytes(non_go)
            (root / "ignored.go").write_text(
                "package fixture\n\nfunc Ignored() {}\n", encoding="utf-8"
            )

            analyzer = root / "fake-analyzer.py"
            analyzer.write_text(
                """#!/usr/bin/env python3
import hashlib
import json
import os
import subprocess
from pathlib import Path

repo = Path(os.environ[\"FAKE_REPO\"])
def blob_id(data):
    return hashlib.sha1(b\"blob \" + str(len(data)).encode() + b\"\\0\" + data).hexdigest()
def object_path(root, digest):
    return Path(root) / digest[:2] / digest[2:]
files = subprocess.check_output([\"git\", \"-C\", str(repo), \"ls-files\", \"-z\"]).split(b\"\\0\")
files = [item.decode() for item in files if item]
new_id = blob_id((repo / \"fixture/new.go\").read_bytes())
notes_id = blob_id((repo / \"fixture/notes.txt\").read_bytes())
new_index = subprocess.run([\"git\", \"-C\", str(repo), \"cat-file\", \"-e\", \":fixture/new.go\"], check=False).returncode == 0
notes_index = subprocess.run([\"git\", \"-C\", str(repo), \"cat-file\", \"-e\", \":fixture/notes.txt\"], check=False).returncode == 0
Path(os.environ[\"FAKE_RESULT\"]).write_text(json.dumps({
    \"files\": files,
    \"new_index\": new_index,
    \"notes_index\": notes_index,
    \"new_object\": object_path(os.environ[\"GIT_OBJECT_DIRECTORY\"], new_id).exists(),
    \"notes_object\": object_path(os.environ[\"GIT_OBJECT_DIRECTORY\"], notes_id).exists(),
}), encoding=\"utf-8\")
""",
                encoding="utf-8",
            )
            analyzer.chmod(0o755)

            result = subprocess.run(
                [
                    str(SCRIPT_PATH),
                    "--analyzer",
                    str(analyzer),
                    "--repo",
                    str(root),
                    "--base",
                    "HEAD",
                    "--module",
                    ".",
                ],
                cwd=root,
                env={
                    **os.environ,
                    "TMPDIR": str(index_dir),
                    "FAKE_REPO": str(root),
                    "FAKE_RESULT": str(inspection),
                },
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            snapshot = json.loads(inspection.read_text(encoding="utf-8"))
            self.assertIn("fixture/new.go", snapshot["files"])
            self.assertNotIn("fixture/notes.txt", snapshot["files"])
            self.assertNotIn("ignored.go", snapshot["files"])
            self.assertTrue(snapshot["new_index"])
            self.assertFalse(snapshot["notes_index"])
            self.assertTrue(snapshot["new_object"])
            self.assertFalse(snapshot["notes_object"])
            notes_id = self._blob_id(non_go)
            self.assertFalse((repository_objects / notes_id[:2] / notes_id[2:]).exists())
            self.assertEqual(self._git(root, "write-tree").stdout.strip(), baseline_tree)
            self.assertEqual(self._git(root, "diff", "--cached", "--quiet").returncode, 0)
            self.assertIn(
                "fixture/new.go",
                self._git(root, "ls-files", "--others", "--exclude-standard").stdout,
            )
            self.assertIn(
                "fixture/notes.txt",
                self._git(root, "ls-files", "--others", "--exclude-standard").stdout,
            )
            self.assertEqual(list(index_dir.iterdir()), [])

    def test_untracked_violation_is_checked_without_mutating_index(self):
        analyzer = os.environ.get("GOLANGCI_LINT") or shutil.which("golangci-lint")
        if not analyzer:
            self.skipTest("golangci-lint is not installed")

        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-test-") as temp_dir:
            root = Path(temp_dir).resolve()  # macOS: /tmp and /var are symlinks under /private
            (root / "fixture").mkdir()
            index_dir = root / "temporary-indexes"
            index_dir.mkdir()
            (root / "go.mod").write_text(
                "module example.com/architecture-gate-fixture\n\ngo 1.24\n",
                encoding="utf-8",
            )
            (root / ".golangci.yml").write_text(
                """version: \"2\"\nrun:\n  tests: true\nlinters:\n  default: none\n  enable:\n    - errcheck\nissues:\n  new-from-rev: HEAD\n""",
                encoding="utf-8",
            )
            (root / ".gitignore").write_text("ignored.go\n", encoding="utf-8")
            (root / "fixture" / "base.go").write_text(
                "package fixture\n\nfunc Base() {}\n", encoding="utf-8"
            )
            self._git(root, "init", "-q")
            self._git(root, "config", "user.email", "architecture-gate@example.com")
            self._git(root, "config", "user.name", "Architecture Gate")
            self._git(root, "add", ".")
            self._git(root, "commit", "-qm", "fixture baseline")
            baseline_tree = self._git(root, "write-tree").stdout.strip()

            (root / "fixture" / "new.go").write_text(
                """package fixture\n\nimport \"os\"\n\nfunc New() {\n\tos.Chdir(\"/\")\n}\n""",
                encoding="utf-8",
            )
            (root / "ignored.go").write_text(
                "package fixture\n\nfunc Ignored() {}\n", encoding="utf-8"
            )

            result = subprocess.run(
                [
                    str(SCRIPT_PATH),
                    "--analyzer",
                    analyzer,
                    "--repo",
                    str(root),
                    "--base",
                    "HEAD",
                    "--module",
                    ".",
                ],
                cwd=root,
                env={**os.environ, "TMPDIR": str(index_dir)},
                capture_output=True,
                text=True,
                check=False,
            )

            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0, output)
            self.assertIn("fixture/new.go", output)
            self.assertIn("errcheck", output)
            self.assertEqual(self._git(root, "write-tree").stdout.strip(), baseline_tree)
            self.assertEqual(self._git(root, "diff", "--cached", "--quiet").returncode, 0)
            self.assertIn("fixture/new.go", self._git(root, "ls-files", "--others", "--exclude-standard").stdout)
            self.assertNotIn("ignored.go", self._git(root, "ls-files", "--others", "--exclude-standard").stdout)
            self.assertEqual(list(index_dir.iterdir()), [])

    def test_config_selects_new_code_or_all_code_arguments(self):
        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-modes-") as temp_dir:
            root = Path(temp_dir).resolve()  # macOS: /tmp and /var are symlinks under /private
            (root / "fixture").mkdir()
            index_dir = root / "temporary-indexes"
            index_dir.mkdir()
            (root / "go.mod").write_text(
                "module example.com/architecture-gate-modes\n\ngo 1.24\n",
                encoding="utf-8",
            )
            (root / "hard.yml").write_text('version: "2"\n', encoding="utf-8")
            (root / "fixture" / "base.go").write_text(
                "package fixture\n\nfunc Base() {}\n", encoding="utf-8"
            )
            self._git(root, "init", "-q")
            self._git(root, "config", "user.email", "architecture-gate@example.com")
            self._git(root, "config", "user.name", "Architecture Gate")
            self._git(root, "add", ".")
            self._git(root, "commit", "-qm", "fixture baseline")
            (root / "fixture" / "new.go").write_text(
                "package fixture\n\nfunc New() {}\n", encoding="utf-8"
            )
            recorded = root / "argv.json"
            analyzer = root / "fake-argv-analyzer.py"
            analyzer.write_text(
                """#!/usr/bin/env python3
import json
import os
import sys
from pathlib import Path

Path(os.environ[\"FAKE_ARGV\"]).write_text(json.dumps({
    \"argv\": sys.argv[1:],
    \"index\": os.environ.get(\"GIT_INDEX_FILE\", \"\"),
}), encoding=\"utf-8\")
print(\"0 issues.\")
""",
                encoding="utf-8",
            )
            analyzer.chmod(0o755)

            def run(*mode):
                result = subprocess.run(
                    [
                        str(SCRIPT_PATH),
                        "--analyzer",
                        str(analyzer),
                        "--repo",
                        str(root),
                        "--base",
                        "HEAD",
                        "--module",
                        ".",
                        "--config",
                        "hard.yml",
                        *mode,
                        "--",
                        "./...",
                    ],
                    cwd=root,
                    env={**os.environ, "TMPDIR": str(index_dir), "FAKE_ARGV": str(recorded)},
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                return json.loads(recorded.read_text(encoding="utf-8"))

            config_path = str(root / "hard.yml")
            new_code = run()
            self.assertEqual(
                new_code["argv"],
                ["run", "--config", config_path, "--new-from-rev", "HEAD", "./..."],
            )
            self.assertNotEqual(new_code["index"], "")

            all_code = run("--all-code")
            self.assertEqual(all_code["argv"], ["run", "--config", config_path, "./..."])
            self.assertEqual(all_code["index"], "")
            self.assertEqual(self._git(root, "diff", "--cached", "--quiet").returncode, 0)
            self.assertEqual(list(index_dir.iterdir()), [])

    def test_new_code_pass_skips_module_without_go_changes(self):
        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-skip-") as temp_dir:
            root = Path(temp_dir).resolve()  # macOS: /tmp and /var are symlinks under /private
            for module in ("alpha", "beta"):
                (root / module).mkdir()
                (root / module / "go.mod").write_text(
                    f"module example.com/{module}\n\ngo 1.24\n", encoding="utf-8"
                )
                (root / module / "base.go").write_text(
                    f"package {module}\n\nfunc Base() {{}}\n", encoding="utf-8"
                )
            index_dir = root / "temporary-indexes"
            index_dir.mkdir()
            self._git(root, "init", "-q")
            self._git(root, "config", "user.email", "architecture-gate@example.com")
            self._git(root, "config", "user.name", "Architecture Gate")
            self._git(root, "add", ".")
            self._git(root, "commit", "-qm", "fixture baseline")
            # alpha: only a non-Go change. beta: an untracked Go file.
            (root / "alpha" / "notes.txt").write_text("notes\n", encoding="utf-8")
            (root / "beta" / "new.go").write_text(
                "package beta\n\nfunc New() {}\n", encoding="utf-8"
            )
            recorded = root / "invocations.txt"
            analyzer = root / "fake-recording-analyzer.py"
            analyzer.write_text(
                """#!/usr/bin/env python3
import os
from pathlib import Path

with open(os.environ[\"FAKE_INVOCATIONS\"], \"a\", encoding=\"utf-8\") as handle:
    handle.write(Path.cwd().name + \"\\n\")
print(\"0 issues.\")
""",
                encoding="utf-8",
            )
            analyzer.chmod(0o755)

            def run(module, *mode):
                return subprocess.run(
                    [
                        str(SCRIPT_PATH),
                        "--analyzer",
                        str(analyzer),
                        "--repo",
                        str(root),
                        "--base",
                        "HEAD",
                        "--module",
                        module,
                        *mode,
                        "--",
                        "./...",
                    ],
                    cwd=root,
                    env={**os.environ, "TMPDIR": str(index_dir), "FAKE_INVOCATIONS": str(recorded)},
                    capture_output=True,
                    text=True,
                    check=False,
                )

            skipped = run("alpha")
            self.assertEqual(skipped.returncode, 0, skipped.stdout + skipped.stderr)
            self.assertIn("no Go changes in alpha since HEAD", skipped.stdout)
            self.assertFalse(recorded.exists())

            linted = run("beta")
            self.assertEqual(linted.returncode, 0, linted.stdout + linted.stderr)
            self.assertEqual(recorded.read_text(encoding="utf-8").splitlines(), ["beta"])

            # The all-code pass never skips.
            all_code = run("alpha", "--all-code")
            self.assertEqual(all_code.returncode, 0, all_code.stdout + all_code.stderr)
            self.assertEqual(recorded.read_text(encoding="utf-8").splitlines(), ["beta", "alpha"])

            # A change to the configuration, a --run-if-changed input or the
            # module's go.mod forces the new-code pass even without Go changes.
            (root / "new.yml").write_text('version: "2"\n', encoding="utf-8")
            (root / "tool.sh").write_text("true\n", encoding="utf-8")
            self._git(root, "add", "new.yml", "tool.sh")
            self._git(root, "commit", "-qm", "lint inputs")
            recorded.unlink()
            unchanged = run("alpha", "--config", "new.yml", "--run-if-changed", "tool.sh")
            self.assertEqual(unchanged.returncode, 0, unchanged.stdout + unchanged.stderr)
            self.assertIn("no Go changes in alpha", unchanged.stdout)
            self.assertFalse(recorded.exists())
            for path, content in (
                ("new.yml", 'version: "2"\nlinters:\n  default: none\n'),
                ("tool.sh", "false\n"),
                ("alpha/go.mod", "module example.com/alpha\n\ngo 1.25\n"),
            ):
                original = (root / path).read_text(encoding="utf-8")
                (root / path).write_text(content, encoding="utf-8")
                forced = run("alpha", "--config", "new.yml", "--run-if-changed", "tool.sh")
                self.assertEqual(forced.returncode, 0, forced.stdout + forced.stderr)
                self.assertNotIn("no Go changes", forced.stdout, path)
                (root / path).write_text(original, encoding="utf-8")
            self.assertEqual(recorded.read_text(encoding="utf-8").splitlines(), ["alpha"] * 3)
            self.assertEqual(self._git(root, "diff", "--cached", "--quiet").returncode, 0)
            self.assertEqual(list(index_dir.iterdir()), [])

    def test_loader_error_fails_even_when_analyzer_returns_zero(self):
        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-loader-") as temp_dir:
            root = Path(temp_dir).resolve()  # macOS: /tmp and /var are symlinks under /private
            (root / "fixture").mkdir()
            index_dir = root / "temporary-indexes"
            index_dir.mkdir()
            (root / "go.mod").write_text(
                "module example.com/architecture-gate-loader-fixture\n\ngo 1.24\n",
                encoding="utf-8",
            )
            (root / "fixture" / "base.go").write_text(
                "package fixture\n\nfunc Base() {}\n", encoding="utf-8"
            )
            self._git(root, "init", "-q")
            self._git(root, "config", "user.email", "architecture-gate@example.com")
            self._git(root, "config", "user.name", "Architecture Gate")
            self._git(root, "add", ".")
            self._git(root, "commit", "-qm", "fixture baseline")
            (root / "fixture" / "new.go").write_text(
                "package fixture\n\nfunc New() {}\n", encoding="utf-8"
            )

            analyzer = root / "fake-loader-analyzer.py"
            analyzer.write_text(
                """#!/usr/bin/env python3
import sys
print('level=error msg=\"[linters_context] typechecking error: package graph unavailable\"', file=sys.stderr)
print('0 issues.')
""",
                encoding="utf-8",
            )
            analyzer.chmod(0o755)

            result = subprocess.run(
                [
                    str(SCRIPT_PATH),
                    "--analyzer",
                    str(analyzer),
                    "--repo",
                    str(root),
                    "--base",
                    "HEAD",
                    "--module",
                    ".",
                ],
                cwd=root,
                env={**os.environ, "TMPDIR": str(index_dir)},
                capture_output=True,
                text=True,
                check=False,
            )

            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0, output)
            self.assertIn("typechecking error", output)
            self.assertIn("0 issues.", output)
            self.assertEqual(self._git(root, "diff", "--cached", "--quiet").returncode, 0)
            self.assertEqual(list(index_dir.iterdir()), [])

    @staticmethod
    def _git(root, *arguments):
        return subprocess.run(
            ["git", *arguments],
            cwd=root,
            capture_output=True,
            text=True,
            check=True if arguments[:2] != ("diff", "--cached") else False,
        )

    @staticmethod
    def _blob_id(data):
        return hashlib.sha1(
            b"blob " + str(len(data)).encode("utf-8") + b"\0" + data
        ).hexdigest()


if __name__ == "__main__":
    unittest.main()
