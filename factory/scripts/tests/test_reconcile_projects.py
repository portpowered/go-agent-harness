"""Behavioral tests for bounded, owner-scoped project reconciliation."""

import importlib.util
import json
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock


sys.dont_write_bytecode = True

SCRIPT_PATH = Path(__file__).resolve().parents[1] / "reconcile-projects.py"
SPEC = importlib.util.spec_from_file_location("reconcile_projects", SCRIPT_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


SERVER = "http://127.0.0.1:7439"
SESSION = "session-1"


def project(name="demo", state="waiting", work_id="project-1"):
    state_type = "FAILED" if state == "blocked" else "PROCESSING"
    return {
        "name": name,
        "workId": work_id,
        "workTypeName": "project",
        "state": {"name": state, "type": state_type},
        "payload": {"projectRoot": "docs/temp/projects/" + name},
    }


def child(name="demo-c01-lane", state="failed", work_type="idea"):
    state_type = "FAILED" if state in {"failed", "blocked"} else (
        "INITIAL" if state == "init" else "TERMINAL"
    )
    return {
        "name": name,
        "workId": name + "-work",
        "workTypeName": work_type,
        "state": {"name": state, "type": state_type},
        "payload": {"project": "demo"},
    }


def cycle(name="demo", state="continue", work_id="cycle-1", superseded_by=None):
    state_type = "PROCESSING" if state == "continue" else "TERMINAL"
    result = {
        "name": name,
        "workId": work_id,
        "workTypeName": "project-cycle",
        "state": {"name": state, "type": state_type},
        "payload": "continue",
    }
    if superseded_by is not None:
        result["supersededBy"] = superseded_by
    return result


class ReconcileProjectsTests(unittest.TestCase):
    def setUp(self):
        self.commands = []

    def running_session(self):
        return {
            "runtime": {
                "progress": {"factoryState": "RUNNING"},
                "lifecycle": {"updatedAt": "2026-09-06T00:00:00Z"},
            }
        }

    def runner(self, responses):
        def run(command):
            self.commands.append(command)
            key = tuple(command[4:])
            response = responses.get(key)
            if response is None and command[4:6] == ["worker-sessions", "list"]:
                response = {"sessions": []}
            if response is None and command[4:6] == ["work", "move"]:
                response = {"workId": command[6]}
            if response is None:
                self.fail("unexpected command: " + " ".join(command))
            return subprocess.CompletedProcess(
                command, 0, stdout=json.dumps(response), stderr=""
            )

        return run

    def legacy_responses(self, works):
        return {
            ("session", "show", SESSION): self.running_session(),
            ("work", "list", "--session", SESSION): {"results": works},
        }

    def paged_runner(self, first_page, later_pages):
        pages = {"": first_page}
        pages.update(later_pages)

        def run(command):
            self.commands.append(command)
            if command[4:7] == ["session", "show", SESSION]:
                response = self.running_session()
            elif command[4:6] == ["work", "list"]:
                token = ""
                if "--next-token" in command:
                    token = command[command.index("--next-token") + 1]
                if token not in pages:
                    self.fail("unexpected continuation token: " + token)
                response = pages[token]
            elif command[4:6] == ["worker-sessions", "list"]:
                response = {"sessions": []}
            elif command[4:6] == ["work", "move"]:
                response = {"workId": command[6]}
            else:
                self.fail("unexpected command: " + " ".join(command))
            return subprocess.CompletedProcess(
                command, 0, stdout=json.dumps(response), stderr=""
            )

        return run

    def test_cycle_on_second_page_prevents_move(self):
        first_page = {
            "results": [project(), child()],
            "counts": {"total": 3},
            "paginationContext": {"maxResults": 500, "nextToken": "page-2"},
        }
        second_page = {
            "results": [cycle()],
            "counts": {"total": 3},
            "paginationContext": {"maxResults": 500},
        }
        result = MODULE.reconcile(
            server=SERVER,
            session_id=SESSION,
            owner_project_name="demo",
            runner=self.paged_runner(first_page, {"page-2": second_page}),
        )

        self.assertEqual(result["moved"], [])
        self.assertEqual(
            result["skipped"],
            [{"name": "demo", "reason": "cycle-in-progress"}],
        )
        self.assertFalse(any(command[4:6] == ["work", "move"] for command in self.commands))
        list_commands = [command for command in self.commands if command[4:6] == ["work", "list"]]
        self.assertEqual(len(list_commands), 2)
        self.assertIn("--max-results", list_commands[0])
        self.assertEqual(
            list_commands[0][list_commands[0].index("--max-results") + 1],
            "500",
        )
        self.assertIn("--all", list_commands[0])

    def test_superseded_historical_cycle_does_not_block_reconciliation(self):
        works = [project(), child(), cycle(state="failed", superseded_by="cycle-2")]
        result = MODULE.reconcile(
            server=SERVER,
            session_id=SESSION,
            owner_project_name="demo",
            runner=self.paged_runner(
                {
                    "results": works,
                    "counts": {"total": len(works)},
                    "paginationContext": {"maxResults": 500},
                },
                {},
            ),
        )

        self.assertEqual(result["moved"][0]["reason"], "missing-cycle")

    def test_blocked_project_is_inspect_only(self):
        result = MODULE.reconcile(
            server=SERVER,
            session_id=SESSION,
            runner=self.runner(self.legacy_responses([project(state="blocked"), child()])),
        )

        self.assertEqual(result["moved"], [])
        self.assertEqual(
            result["skipped"],
            [{"name": "demo", "reason": "blocked-inspect-only"}],
        )
        self.assertFalse(any(command[4:6] == ["work", "move"] for command in self.commands))

    def test_wrong_owner_has_no_move(self):
        responses = {
            "": {
                "results": [project(name="other", work_id="project-other"), child(name="other-c01-lane")],
                "counts": {"total": 2},
                "paginationContext": {"maxResults": 500},
            }
        }
        with self.assertRaises(MODULE.ReconcileError):
            MODULE.reconcile(
                server=SERVER,
                session_id=SESSION,
                owner_project_name="demo",
                runner=self.paged_runner(responses[""], {}),
            )
        self.assertFalse(any(command[4:6] == ["work", "move"] for command in self.commands))

    def test_repeated_continuation_fails_closed(self):
        page = {
            "results": [project()],
            "paginationContext": {"maxResults": 500, "nextToken": "repeat"},
        }
        with self.assertRaisesRegex(MODULE.ReconcileError, "repeated a continuation"):
            MODULE._work_list(
                self.paged_runner(page, {"repeat": page}),
                SERVER,
                SESSION,
                bounded=True,
                include_superseded=True,
            )

    def test_more_than_twenty_pages_fails_closed(self):
        pages = {}
        for index in range(MODULE.MAX_WORK_LIST_PAGES):
            token = "page-" + str(index + 1)
            next_token = "page-" + str(index + 2)
            pages[token] = {
                "results": [],
                "paginationContext": {"maxResults": 500, "nextToken": next_token},
            }
        first = {
            "results": [],
            "paginationContext": {"maxResults": 500, "nextToken": "page-1"},
        }
        with self.assertRaisesRegex(MODULE.ReconcileError, "exceeded 20 pages"):
            MODULE._work_list(
                self.paged_runner(first, pages),
                SERVER,
                SESSION,
                bounded=True,
                include_superseded=True,
            )

    def test_cli_guard_checks_owner_session_and_server(self):
        record = {
            "status": "active",
            "project": "demo",
            "contractRevision": "contract-1",
            "session": {"id": SESSION, "endpoint": {"server": SERVER}},
            "sessionId": SESSION,
            "server": SERVER,
        }
        with mock.patch.object(MODULE, "root_path", return_value=Path("/tmp/repo")), \
             mock.patch.object(MODULE, "manifest", return_value={"project": "demo", "contractRevision": "contract-1"}), \
             mock.patch.object(MODULE.project_admission, "status", return_value=record):
            self.assertEqual(
                MODULE.verify_admission(server=SERVER, session_id=SESSION),
                "demo",
            )
            with self.assertRaises(MODULE.ReconcileError):
                MODULE.verify_admission(server=SERVER + "/other", session_id=SESSION)
            with self.assertRaises(MODULE.ReconcileError):
                MODULE.verify_admission(server=SERVER, session_id="other-session")


if __name__ == "__main__":
    unittest.main()
