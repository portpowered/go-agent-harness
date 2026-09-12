import datetime as dt
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "worktree-cleanup.py"
SPEC = importlib.util.spec_from_file_location("worktree_cleanup", SCRIPT_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class WorktreeCleanupTests(unittest.TestCase):
    def test_factory_guard_uses_remote_live_session_snapshot(self):
        calls = []
        response = {
            "scope": "live",
            "sessions": [
                {
                    "id": "session-1",
                    "factoryDir": "/repo/factory",
                    "folderPath": "/repo/factory",
                    "runtime": {
                        "lifecycleControlStatus": "RUNNING",
                        "status": "ACTIVE",
                        "progress": {"factoryState": "RUNNING", "inFlightCount": 0},
                        "petri": {"marking": []},
                    },
                }
            ],
        }

        def runner(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, json.dumps(response), "")

        guard = MODULE.factory_guard("you", "http://127.0.0.1:7439", runner=runner)

        self.assertEqual(guard["activeWorkerCount"], 0)
        self.assertIn("--remote", calls[0])
        self.assertEqual(calls[0][-3:], ["session", "list", "--live-only"])

    def test_factory_guard_falls_back_to_authoritative_http_snapshot(self):
        response = {
            "scope": "live",
            "sessions": [
                {
                    "id": "session-1",
                    "factoryDir": "/repo/factory",
                    "folderPath": "/repo/factory",
                    "runtime": {
                        "lifecycleControlStatus": "RUNNING",
                        "status": "ACTIVE",
                        "progress": {"factoryState": "RUNNING", "inFlightCount": 2},
                        "petri": {"marking": []},
                    },
                }
            ],
        }

        def runner(command, **kwargs):
            return subprocess.CompletedProcess(command, 1, "", "compatibility route failed")

        http_response = mock.MagicMock()
        http_response.__enter__.return_value = http_response
        http_response.__exit__.return_value = False
        with mock.patch.object(MODULE.urllib.request, "urlopen", return_value=http_response) as urlopen:
            with mock.patch.object(MODULE.json, "load", return_value=response):
                guard = MODULE.factory_guard(
                    "you", "http://127.0.0.1:7439", runner=runner
                )

        self.assertEqual(guard["activeWorkerCount"], 2)
        urlopen.assert_called_once_with(
            "http://127.0.0.1:7439/factory-sessions",
            timeout=MODULE.COMMAND_TIMEOUT_SECONDS,
        )

    def test_discovers_ignored_go_tools_in_any_managed_clone(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            owner = Path(temp_dir) / "clone" / ".claude" / "worktrees" / "lane"
            cache = owner / ".cache" / "go-tools"
            cache.mkdir(parents=True)
            (cache / "tool").write_bytes(b"binary")
            record = {"path": str(owner), "head": "a" * 40}

            def runner(command, **kwargs):
                if command[:2] == ["git", "status"]:
                    return subprocess.CompletedProcess(
                        command, 0, "!! .cache/go-tools/tool\n", ""
                    )
                raise AssertionError(f"unexpected command: {command}")

            with mock.patch.object(MODULE, "list_worktrees", return_value=[record]):
                candidates = MODULE.build_cache_candidates(
                    Path(temp_dir) / "other-clone",
                    Path(temp_dir) / "common.git",
                    {"boardWorkNames": [], "activeWorkerNames": []},
                    now=dt.datetime.now(dt.timezone.utc),
                    minimum_age_hours=0,
                    runner=runner,
                )

            self.assertEqual(len(candidates), 1)
            self.assertTrue(candidates[0]["eligible"])
            self.assertEqual(candidates[0]["outputType"], "go-tools")
            self.assertEqual(candidates[0]["path"], str(cache.resolve()))

    def test_coverage_manifest_is_not_a_generated_coverage_output(self):
        self.assertTrue(MODULE._is_coverage_output(Path("coverage")))
        self.assertTrue(MODULE._is_coverage_output(Path("coverage.operator")))
        self.assertFalse(MODULE._is_coverage_output(Path("coverage-manifest")))

    def test_factory_worktrees_are_recognized_across_clone_locations(self):
        self.assertTrue(
            MODULE._is_managed_factory_worktree(
                Path("/outside/clone/.claude/worktrees/finished-lane")
            )
        )
        self.assertFalse(
            MODULE._is_managed_factory_worktree(Path("/outside/codex/worktrees/task"))
        )


if __name__ == "__main__":
    unittest.main()
