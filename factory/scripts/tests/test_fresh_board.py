"""Explicit board replacement keeps ownership and a verifiable handoff."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "factory/scripts"))
import project_admission
spec = importlib.util.spec_from_file_location("fresh_launcher", ROOT / "factory/scripts/run-factory.py")
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class FreshBoardTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.context = self.root / "handoff.md"
        self.context.write_text("Preserve existing candidate and failures.\n")
        for args in (("init", "-q"), ("add", "handoff.md"),
                     ("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "context")):
            subprocess.run(["git", "-C", str(self.root), *args], check=True)
        self.common, self.record_path = launcher.paths(self.root)
        (self.common / "factory-runs").mkdir()
        self.recording = self.common / "factory-runs/original.json"
        self.recording.write_text('{"events":[]}')
        self.record = {"root":str(self.root), "server":launcher.SERVER, "status":"stopped",
                       "sessionId":"previous", "recording":str(self.recording),
                       "recordingSha256":launcher.digest(self.recording),
                       "integrationRevision":"baseline", "definitionSha256":"old-graph"}
        launcher.write_record(self.record_path, self.record)
        project_admission.admit(self.root, "audio-runtime", "audio-runtime-v1")
        project_admission.bind_session(self.root, "audio-runtime", "audio-runtime-v1", launcher.SERVER, "previous")
        self.env = patch.object(launcher, "runtime_environment", return_value={})
        self.env.start(); self.addCleanup(self.env.stop)
        self.started = patch.object(launcher, "start", return_value={"running":True})
        self.start_mock = self.started.start(); self.addCleanup(self.started.stop)

    def invoke(self):
        real = subprocess.run
        def command(args, **kwargs):
            if args[0] == "you": return subprocess.CompletedProcess(args, 0)
            return real(args, **kwargs)
        with patch.object(launcher.subprocess, "run", side_effect=command):
            return launcher.fresh_board(self.root, self.context)

    def test_explicit_fresh_board_archives_runtime_and_keeps_admission(self):
        self.assertTrue(self.invoke()["running"])
        updated = launcher.read_json(self.record_path)
        self.assertEqual(updated["launchMode"], "fresh")
        self.assertEqual(updated["integrationRevision"], "baseline")
        self.assertEqual(updated["freshBoardContext"]["sha256"], launcher.digest(self.context))
        self.assertEqual(updated["freshBoardPredecessor"]["sha256"], launcher.digest(self.recording))
        archive = list((self.common / "factory-runs").glob("*-before-fresh-board.json"))
        self.assertEqual(len(archive), 1)
        self.assertEqual(launcher.read_json(archive[0]), self.record)
        self.assertEqual(project_admission.status(self.root)["sessionId"], "previous")

    def test_uncommitted_context_does_not_replace_board(self):
        self.context.write_text("Unreviewed change")
        with self.assertRaisesRegex(ValueError, "commit the exact"):
            self.invoke()
        self.assertEqual(launcher.read_json(self.record_path), self.record)
        self.start_mock.assert_not_called()

    def test_changed_recording_does_not_replace_board(self):
        self.recording.write_text('{"changed":true}')
        with self.assertRaisesRegex(ValueError, "stopped recording changed"):
            self.invoke()
        self.assertEqual(launcher.read_json(self.record_path), self.record)
        self.start_mock.assert_not_called()
