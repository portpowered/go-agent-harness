import importlib.util
import sys
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("deadcode_check", Path(__file__).with_name("deadcode-check.py"))
deadcode_check = importlib.util.module_from_spec(spec)
sys.modules["deadcode_check"] = deadcode_check  # dataclasses resolve their module
spec.loader.exec_module(deadcode_check)

PKG = "example.com/m/p"


class CombineTest(unittest.TestCase):
    def test_dead_only_when_every_run_compiling_the_file_reports_it(self):
        loaded = {"linux": {"p/a.go", "p/linux.go"}, "windows": {"p/a.go", "p/windows.go"}}
        dead = {
            "linux": {("p/a.go", "F"): (PKG, 3), ("p/a.go", "G"): (PKG, 9), ("p/linux.go", "L"): (PKG, 1)},
            "windows": {("p/a.go", "F"): (PKG, 3)},
        }
        # G is live on windows; L is dead in the only run that compiles it.
        self.assertEqual(
            deadcode_check.combine(loaded, dead),
            {f"{PKG} F": "p/a.go:3", f"{PKG} L": "p/linux.go:1"},
        )

    def test_reported_but_uncompiled_file_counts_as_dead(self):
        # A run that reports a function compiled its file even if go list did
        # not list it (for example a generated test main).
        loaded = {"linux": set()}
        dead = {"linux": {("p/a.go", "F"): (PKG, 3)}}
        self.assertEqual(deadcode_check.combine(loaded, dead), {f"{PKG} F": "p/a.go:3"})


    def test_host_only_runs_judge_only_files_no_portable_run_compiles(self):
        loaded = {"linux": {"p/a.go"}, "darwin-cgo": {"p/a.go", "p/cgo.go"}}
        dead = {
            "linux": {("p/a.go", "F"): (PKG, 3)},
            # The host cgo run reaches F, but a.go is judged by linux alone so
            # every host computes the same result for it.
            "darwin-cgo": {("p/cgo.go", "C"): (PKG, 7)},
        }
        self.assertEqual(
            deadcode_check.combine(loaded, dead, frozenset({"darwin-cgo"})),
            {f"{PKG} F": "p/a.go:3", f"{PKG} C": "p/cgo.go:7"},
        )


class CompareTest(unittest.TestCase):
    def test_unjudgeable_stale_entry_is_skipped(self):
        self.assertEqual(deadcode_check.compare(set(), {"a C"}, None, lambda entry: False), [])

    def test_exact_match_passes(self):
        self.assertEqual(deadcode_check.compare({"a F"}, {"a F"}, {"a F", "a G"}), [])

    def test_new_dead_code_fails(self):
        self.assertEqual(
            deadcode_check.compare({"a F", "a G"}, {"a F"}, None),
            ["new dead code (delete it): a G"],
        )

    def test_stale_entry_fails(self):
        self.assertEqual(
            deadcode_check.compare(set(), {"a F"}, None),
            ["stale allowlist entry (no longer dead; delete the line): a F"],
        )

    def test_growth_against_the_base_fails(self):
        self.assertEqual(
            deadcode_check.compare({"a F", "a G"}, {"a F", "a G"}, {"a F"}),
            ["allowlist grew (it may only shrink): a G"],
        )


class AllowlistTest(unittest.TestCase):
    def test_comments_and_blank_lines_are_ignored(self):
        self.assertEqual(deadcode_check.read_allowlist("# header\n\na F\n  b G  \n"), {"a F", "b G"})


if __name__ == "__main__":
    unittest.main()
