import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "project_admission.py"
SCRIPT_MODULE_SPEC = importlib.util.spec_from_file_location(
    "project_admission",
    SCRIPT_PATH,
)
SCRIPT_MODULE = importlib.util.module_from_spec(SCRIPT_MODULE_SPEC)
assert SCRIPT_MODULE_SPEC.loader is not None
SCRIPT_MODULE_SPEC.loader.exec_module(SCRIPT_MODULE)


class GitFixture:
    """Small repository fixture that also exposes a linked worktree."""

    def __init__(self, base_dir):
        self.base = Path(base_dir)
        self.repo = self.base / "repo"
        self._run(self.base, "init", self.repo.name)
        self._run(self.repo, "config", "user.name", "Admission Tests")
        self._run(self.repo, "config", "user.email", "admission-tests@example.com")
        (self.repo / "README.md").write_text("admission\n", encoding="utf-8")
        self._run(self.repo, "add", "README.md")
        self._run(self.repo, "commit", "-m", "initial")

    @staticmethod
    def _run(cwd, *args):
        result = subprocess.run(
            ["git", *args],
            cwd=cwd,
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode:
            raise AssertionError(result.stderr.strip())
        return result

    def worktree(self):
        path = self.base / "linked"
        self._run(self.repo, "worktree", "add", "-b", "linked-admission", str(path))
        return path


class ProjectAdmissionTests(unittest.TestCase):
    def make_store(self, directory):
        directory = Path(directory)
        return SCRIPT_MODULE.ProjectAdmission(
            state_path=directory / "admission.json",
            lock_path=directory / "admission.lock",
        )

    def test_admission_is_idempotent_and_rejects_different_identity(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = self.make_store(temp_dir)
            first = store.Admit("project-a", "contract-1")
            second = store.Admit("project-a", "contract-1")

            self.assertEqual(first, second)
            self.assertEqual(first["status"], "active")
            with self.assertRaises(SCRIPT_MODULE.AdmissionConflictError):
                store.Admit("project-b", "contract-1")
            with self.assertRaises(SCRIPT_MODULE.AdmissionConflictError):
                store.Admit("project-a", "contract-2")
            self.assertEqual(store.Status()["project_id"], "project-a")

    def test_launcher_callable_surface_uses_manifest_identity_names(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = GitFixture(temp_dir).repo
            owner = SCRIPT_MODULE.admit(repo, "project-a", "contract-1")
            self.assertEqual(owner["project"], "project-a")
            self.assertEqual(owner["contractRevision"], "contract-1")
            bound = SCRIPT_MODULE.bind_session(
                repo,
                "project-a",
                "contract-1",
                "http://127.0.0.1:7439",
                "session-a",
            )
            self.assertEqual(bound["server"], "http://127.0.0.1:7439")
            self.assertEqual(bound["sessionId"], "session-a")
            self.assertEqual(SCRIPT_MODULE.status(repo)["project"], "project-a")
            released = SCRIPT_MODULE.release(
                repo,
                "project-a",
                "contract-1",
                {"outcome": "complete"},
                server="http://127.0.0.1:7439",
                session_id="session-a",
            )
            self.assertEqual(released["status"], "released")
            self.assertIsNone(SCRIPT_MODULE.status(repo))


    def test_common_directory_is_shared_by_linked_worktrees_and_state_is_private(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            linked = fixture.worktree()
            root_store = SCRIPT_MODULE.ProjectAdmission(fixture.repo)
            linked_store = SCRIPT_MODULE.ProjectAdmission(linked)

            self.assertEqual(root_store.state_path, linked_store.state_path)
            self.assertEqual(root_store.lock_path, linked_store.lock_path)
            root_store.Admit("project-a", "contract-1")
            with self.assertRaises(SCRIPT_MODULE.AdmissionConflictError):
                linked_store.Admit("project-b", "contract-1")
            self.assertEqual(
                stat.S_IMODE(root_store.state_path.stat().st_mode),
                0o600,
            )

    def test_release_requires_matching_identity_and_terminal_evidence(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = self.make_store(temp_dir)
            store.Admit("project-a", "contract-1")
            with self.assertRaises(SCRIPT_MODULE.AdmissionValidationError):
                store.Release("project-a", None)
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.Release("project-b", {"outcome": "complete"})
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.Release(
                    "project-a",
                    {"outcome": "complete"},
                    contract_revision="contract-2",
                )
            self.assertEqual(store.Status()["status"], "active")

            released = store.Release("project-a", {"outcome": "complete"})
            self.assertEqual(released["status"], "released")
            self.assertEqual(released["release_evidence"], {"outcome": "complete"})
            self.assertFalse(store.Status()["admitted"])
            self.assertEqual(store.Status()["release_evidence"]["outcome"], "complete")

            # Release makes the owner available only through this explicit
            # transition; a later project can then take the slot.
            store.Admit("project-b", "contract-1")
            self.assertEqual(store.Status()["project_id"], "project-b")

    def test_blocked_record_retains_ownership_until_release(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = self.make_store(temp_dir)
            store.Admit("project-a", "contract-1")
            state = json.loads(store.state_path.read_text(encoding="utf-8"))
            state["status"] = "blocked"
            store.state_path.write_text(json.dumps(state), encoding="utf-8")
            os.chmod(store.state_path, 0o600)

            self.assertTrue(store.Status()["admitted"])
            self.assertEqual(store.Admit("project-a", "contract-1")["status"], "blocked")
            with self.assertRaises(SCRIPT_MODULE.AdmissionConflictError):
                store.Admit("project-b", "contract-1")
            store.Release("project-a", {"outcome": "blocked-resolved"})
            store.Admit("project-b", "contract-1")

    def test_endpoint_binding_is_exact_and_checks_embedded_owner(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = self.make_store(temp_dir)
            store.Admit("project-a", "contract-1")
            endpoint = {
                "id": "endpoint-a",
                "project_id": "project-a",
                "session_id": "session-a",
            }
            bound = store.BindEndpoint("project-a", "session-a", endpoint)
            self.assertEqual(bound["session"]["endpoint"], endpoint)
            self.assertEqual(
                store.BindEndpoint("project-a", "session-a", endpoint),
                bound,
            )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.BindEndpoint("project-a", "session-b", endpoint)
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.BindEndpoint(
                    "project-a",
                    "session-a",
                    {"id": "endpoint-b", "project_id": "project-b"},
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.BindEndpoint(
                    "project-a",
                    "session-a",
                    {
                        "id": "endpoint-c",
                        "project_id": "project-a",
                        "projectID": "project-b",
                    },
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                store.BindEndpoint("project-a", "session-a", "endpoint-b")

    def test_resume_session_is_compare_and_swap_and_keeps_admission(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = GitFixture(temp_dir).repo
            SCRIPT_MODULE.admit(repo, "project-a", "contract-1")
            SCRIPT_MODULE.bind_session(
                repo, "project-a", "contract-1", "server-a", "session-old"
            )
            evidence = {
                "recording_id": "recording-old",
                "sha256": "a" * 64,
                "project": "project-a",
                "source_session_id": "session-old",
            }
            resumed = SCRIPT_MODULE.resume_session(
                repo,
                "project-a",
                "contract-1",
                "server-a",
                "session-old",
                "session-new",
                evidence,
            )
            self.assertEqual(resumed["sessionId"], "session-new")
            self.assertEqual(resumed["server"], "server-a")
            self.assertEqual(
                resumed["session"]["resume"]["recording_evidence"], evidence
            )
            self.assertEqual(SCRIPT_MODULE.status(repo)["project"], "project-a")

            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                SCRIPT_MODULE.resume_session(
                    repo,
                    "project-a",
                    "contract-1",
                    "server-a",
                    "session-old",
                    "session-third",
                    evidence,
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                SCRIPT_MODULE.resume_session(
                    repo,
                    "project-b",
                    "contract-1",
                    "server-a",
                    "session-new",
                    "session-third",
                    evidence,
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                SCRIPT_MODULE.resume_session(
                    repo,
                    "project-a",
                    "contract-1",
                    "server-a",
                    "session-new",
                    "session-third",
                    {"recording_id": "other", "project": "project-b"},
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionValidationError):
                SCRIPT_MODULE.resume_session(
                    repo,
                    "project-a",
                    "contract-1",
                    "server-a",
                    "session-new",
                    "",
                    {},
                )
            with self.assertRaises(SCRIPT_MODULE.AdmissionIdentityMismatchError):
                SCRIPT_MODULE.resume_session(
                    repo,
                    "project-a",
                    "contract-1",
                    "server-b",
                    "session-new",
                    "session-third",
                    evidence,
                )

    def test_malformed_state_fails_closed_and_crash_leftovers_do_not_release(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            store = self.make_store(temp_dir)
            store.state_path.write_text('{"project_id":', encoding="utf-8")
            os.chmod(store.state_path, 0o600)
            with self.assertRaises(SCRIPT_MODULE.AdmissionStateError):
                store.Admit("project-a", "contract-1")
            self.assertEqual(store.state_path.read_text(encoding="utf-8"), '{"project_id":')

            store.state_path.unlink()
            leftover = store.state_path.with_name(
                f".{store.state_path.name}.crashed.tmp"
            )
            leftover.write_text('{"status":"active"}', encoding="utf-8")
            store.Admit("project-a", "contract-1")
            self.assertEqual(store.Status()["project_id"], "project-a")
            self.assertTrue(leftover.exists())

            # A stale lock file represents a crashed owner process, not a
            # released admission.  The OS lock itself remains reusable.
            store.lock_path.touch(exist_ok=True)
            with self.assertRaises(SCRIPT_MODULE.AdmissionConflictError):
                store.Admit("project-b", "contract-1")

    def test_symlinked_state_or_lock_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            directory = Path(temp_dir)
            state_target = directory / "state-target.json"
            state_target.write_text("{}", encoding="utf-8")
            state_link = directory / "admission.json"
            state_link.symlink_to(state_target)
            store = SCRIPT_MODULE.ProjectAdmission(
                state_path=state_link,
                lock_path=directory / "admission.lock",
            )
            with self.assertRaises(SCRIPT_MODULE.AdmissionStateError):
                store.Status()

            state_link.unlink()
            lock_target = directory / "lock-target"
            lock_target.touch()
            lock_link = directory / "admission.lock"
            lock_link.symlink_to(lock_target)
            with self.assertRaises(SCRIPT_MODULE.AdmissionStateError):
                store.Admit("project-a", "contract-1")

    def test_concurrent_processes_admit_only_one_different_project(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            directory = Path(temp_dir)
            state_path = directory / "admission.json"
            lock_path = directory / "admission.lock"
            worker = (
                "import sys; "
                "sys.path.insert(0, sys.argv[1]); "
                "from project_admission import ProjectAdmission; "
                "s=ProjectAdmission(state_path=sys.argv[2], lock_path=sys.argv[3]); "
                "p=sys.argv[4]; "
                "\ntry: s.Admit(p, 'contract-1'); print('ok')\n"
                "except Exception as exc: print(type(exc).__name__)"
            )
            processes = [
                subprocess.Popen(
                    [
                        sys.executable,
                        "-B",
                        "-c",
                        worker,
                        str(SCRIPT_PATH.parent),
                        str(state_path),
                        str(lock_path),
                        f"project-{index}",
                    ],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                )
                for index in range(8)
            ]
            outputs = [process.communicate(timeout=10) for process in processes]
            successes = [stdout.strip() for stdout, _ in outputs if stdout.strip() == "ok"]
            self.assertEqual(len(successes), 1, outputs)
            state = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertTrue(state["project_id"].startswith("project-"))

    def test_cli_operations_emit_json_and_status_is_read_only(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            fixture = GitFixture(temp_dir)
            state_path = SCRIPT_MODULE.admission_state_path(fixture.repo)
            lock_path = SCRIPT_MODULE.admission_lock_path(fixture.repo)
            command = [sys.executable, "-B", str(SCRIPT_PATH), "--repo", str(fixture.repo)]

            status = subprocess.run(
                [*command, "status"], capture_output=True, text=True, check=False
            )
            self.assertEqual(status.returncode, 0, status.stderr)
            self.assertEqual(json.loads(status.stdout)["status"], "free")
            self.assertFalse(state_path.exists())
            self.assertFalse(lock_path.exists())

            admitted = subprocess.run(
                [*command, "admit", "--project-id", "project-a", "--contract-revision", "contract-1"],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(admitted.returncode, 0, admitted.stderr)
            self.assertEqual(json.loads(admitted.stdout)["project_id"], "project-a")
            bound = subprocess.run(
                [
                    *command,
                    "bind-endpoint",
                    "--project-id",
                    "project-a",
                    "--contract-revision",
                    "contract-1",
                    "--session-id",
                    "session-a",
                    "--endpoint",
                    "endpoint-a",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(bound.returncode, 0, bound.stderr)
            self.assertEqual(json.loads(bound.stdout)["session"]["id"], "session-a")
            resumed = subprocess.run(
                [
                    *command,
                    "resume-session",
                    "--project-id",
                    "project-a",
                    "--contract-revision",
                    "contract-1",
                    "--server",
                    "endpoint-a",
                    "--previous-session-id",
                    "session-a",
                    "--new-session-id",
                    "session-b",
                    "--recording-evidence",
                    '{"recording_id":"recording-a","sha256":"' + "a" * 64 + '"}',
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(resumed.returncode, 0, resumed.stderr)
            self.assertEqual(json.loads(resumed.stdout)["session"]["id"], "session-b")
            released = subprocess.run(
                [
                    *command,
                    "release",
                    "--project-id",
                    "project-a",
                    "--contract-revision",
                    "contract-1",
                    "--session-id",
                    "session-b",
                    "--endpoint",
                    "endpoint-a",
                    "--terminal-evidence",
                    '{"outcome":"complete"}',
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(released.returncode, 0, released.stderr)
            self.assertEqual(json.loads(released.stdout)["status"], "released")


if __name__ == "__main__":
    unittest.main()
