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
            root = Path(temp_dir)
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

    def test_loader_error_fails_even_when_analyzer_returns_zero(self):
        with tempfile.TemporaryDirectory(prefix="golangci-working-tree-loader-") as temp_dir:
            root = Path(temp_dir)
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
