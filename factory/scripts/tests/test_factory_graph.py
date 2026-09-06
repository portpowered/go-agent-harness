"""Exercise the authored delivery graph through the installed runtime, without models."""
import contextlib
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request


ROOT = Path(__file__).resolve().parents[3]
COMMON = Path(subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "--path-format=absolute", "--git-common-dir"], text=True).strip())
YOU = str(COMMON / "factory-bin/you") if (COMMON / "factory-bin/you").is_file() else shutil.which("you")
FIXTURE = r'''import json, os, pathlib, sys
root = pathlib.Path(os.environ["FACTORY_ROOT"])
role = sys.argv[1]
def emit(value):
    print(json.dumps({"type":"item.completed", "item":{"id":"message-final", "type":"agent_message", "text":json.dumps(value)}}))
counts = root / "counts.json"
data = json.loads(counts.read_text()) if counts.exists() else {}
data[role] = data.get(role, 0) + 1
counts.write_text(json.dumps(data))
name = "audio-runtime-c01-smoke"
packet = {"project":"audio-runtime", "contractRevision":"audio-runtime-v1"}
if role == "lead":
    works = [{"name":"audio-runtime", "workTypeName":"project-cycle", "payload":"blocked"}]
    relations = []
    if data[role] == 1:
        works[0]["payload"] = "continue"
        works.insert(0, {"name":name, "workTypeName":"idea", "payload":packet})
        relations = [{"type":"DEPENDS_ON", "sourceWorkName":"audio-runtime", "targetWorkName":name, "requiredState":"complete"}]
    emit({"request":{"requestId":"smoke-cycle-"+str(data[role]), "type":"FACTORY_REQUEST_BATCH", "works":works, "relations":relations}})
elif role == "plan":
    target = root / "tasks/todo" / (name + ".json")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps({**packet, "branchName":"codex/"+name}))
    (root / ".claude/worktrees" / name).mkdir(parents=True, exist_ok=True)
    emit({"decision":"ACCEPTED", "feedback":"planned"})
elif role == "review" and data[role] == 1:
    emit({"decision":"REJECTED", "feedback":"one bounded correction"})
else:
    emit({"decision":"ACCEPTED", "feedback":"verified"})
'''


def get(server, path):
    with urllib.request.urlopen(server + path, timeout=2) as response:
        return json.load(response)


@unittest.skipUnless(YOU, "installed factory runtime is required")
class FactoryGraphTest(unittest.TestCase):
    def test_authored_roles_cadence_and_consumed_work_lifecycle(self):
        definition = json.loads((ROOT / "factory/factory.json").read_text())
        workers = {worker["name"]:worker for worker in definition["workers"]}
        for role in ("project-lead", "planner", "ideafier"):
            self.assertEqual((workers[role]["model"], workers[role]["reasoningEffort"]), ("gpt-6-astra", "medium"))
        for role in ("processor", "reviewer", "validator"):
            self.assertEqual((workers[role]["model"], workers[role]["reasoningEffort"]), ("gpt-5.6-luna", "max"))
        stations = {station["name"]:station for station in definition["workstations"]}
        self.assertEqual(stations["though-retrigger"]["cron"]["schedule"], "0 */4 * * *")
        states = {kind["name"]:{state["name"]:state["type"] for state in kind["states"]} for kind in definition["workTypes"]}
        # A consumed nonterminal Work without an output has no place occupancy;
        # the runtime cannot restore it from a recording after process restart.
        for station in stations.values():
            for outcome in ("outputs", "onFailure", "onRejection", "onContinue"):
                if outcome not in station:
                    continue
                outputs = {value["workType"] for value in station[outcome]}
                for value in station["inputs"]:
                    if states[value["workType"]][value["state"]] in ("INITIAL", "PROCESSING"):
                        self.assertIn(value["workType"], outputs, (station["name"], outcome))

    def test_owned_launcher_start_stop_and_resume(self):
        with tempfile.TemporaryDirectory(prefix="harness-factory-launcher-") as temporary:
            root = Path(temporary).resolve()
            shutil.copytree(ROOT / "factory", root / "factory", ignore=shutil.ignore_patterns("__pycache__"))
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture"], check=True)
            fixture = root / "worker.py"
            fixture.write_text(FIXTURE)
            entries = [{"workerName":worker, "runType":"script", "scriptConfig":{
                "command":sys.executable, "args":[str(fixture), role]}} for worker,role in
                [("project-lead","lead"),("planner","plan"),("processor","process"),("reviewer","review"),("ideafier","meta")]]
            entries += [{"workerName":worker,"runType":"accept"} for worker in ("workspace-setup","ci-waiter","project-reconciler")]
            mock = root / "mock.json"
            mock.write_text(json.dumps({"unmatchedDispatchPolicy":"passthrough","mockWorkers":entries}))
            private = root / ".git/factory-bin"
            private.mkdir()
            executable = private / "you"
            executable.write_text("#!/usr/bin/env python3\nimport os,sys\na=sys.argv[1:]\nif a and a[0]=='run': a += ['--with-mock-workers',"+repr(str(mock))+"]\nos.execv("+repr(YOU)+",["+repr(YOU)+"]+a)\n")
            executable.write_text(executable.read_text().replace("a=sys.argv[1:]\n", "a=sys.argv[1:]\nif '--resume' in a and os.path.exists("+repr(str(root / "fail-resume"))+"): sys.exit(23)\n"))
            executable.chmod(0o700)
            (private / "runtime.json").write_text(json.dumps({"sourceRevision":"test-only-mock-wrapper","sha256":hashlib.sha256(executable.read_bytes()).hexdigest()}))
            with socket.socket() as sock:
                sock.bind(("127.0.0.1",0))
                port = sock.getsockname()[1]
            launcher_path = root / "factory/scripts/run-factory.py"
            launcher_path.write_text(launcher_path.read_text().replace("7439",str(port)))
            sys.path.insert(0, str(ROOT / "factory/scripts"))
            try:
                spec = importlib.util.spec_from_file_location("factory_launcher_smoke", launcher_path)
                launcher = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(launcher)
            finally:
                sys.path.pop(0)
            try:
                started = launcher.start(root)
                self.assertTrue(started["running"])
                saved_manifest = root/"factory/projects/audio-runtime/manifest.json"
                original_manifest = saved_manifest.read_text()
                changed = json.loads(original_manifest)
                changed["contractRevision"] = "conflicting-revision"
                saved_manifest.write_text(json.dumps(changed))
                with self.assertRaisesRegex(ValueError,"ownership is mismatched"):
                    launcher.start(root)
                saved_manifest.write_text(original_manifest)
                first_session = started["runtime"]["sessionId"]
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline:
                    board = get(launcher.SERVER, "/factory-sessions/"+first_session+"/work?maxResults=100&includeSuperseded=true")
                    if any(w.get("workTypeName")=="project" and w.get("state",{}).get("name")=="blocked" for w in board["results"]):
                        break
                    time.sleep(0.1)
                else:
                    self.fail("mock delivery did not block before launcher restart")
                self.assertFalse(launcher.stop(root)["running"])
                (root/"fail-resume").touch()
                with self.assertRaisesRegex(ValueError, "factory supervisor failed"):
                    launcher.start(root)
                failed = launcher.status(root)["runtime"]
                self.assertEqual(failed["sessionId"],first_session)
                self.assertIn("recoveryInput",failed)
                (root/"fail-resume").unlink()
                resumed = launcher.start(root)
                self.assertTrue(resumed["running"])
                self.assertNotEqual(resumed["runtime"]["sessionId"],first_session)
                self.assertEqual(resumed["admission"]["sessionId"],resumed["runtime"]["sessionId"])
                self.assertEqual(json.loads((root/"counts.json").read_text())["lead"],2)
                project_id = "batch-admit-audio-runtime-audio-runtime-v1-audio-runtime"
                subprocess.run([YOU,"--server",launcher.SERVER,"work","move",project_id,"init",
                                "--session",resumed["runtime"]["sessionId"],"--request-id","smoke-operator-correction"],
                               check=True,capture_output=True)
                deadline = time.monotonic()+20
                while time.monotonic()<deadline:
                    board=get(launcher.SERVER,"/factory-sessions/"+resumed["runtime"]["sessionId"]+"/work?maxResults=100&includeSuperseded=true")
                    if (json.loads((root/"counts.json").read_text()).get("lead",0)>=3 and
                        any(w.get("workId")==project_id and w.get("state",{}).get("name")=="blocked" for w in board["results"])):
                        break
                    time.sleep(0.1)
                else:
                    self.fail("operator correction did not finish its project cycle")
                launcher.stop(root)
                after_move=launcher.start(root)
                self.assertTrue(after_move["running"])
                self.assertEqual(json.loads((root/"counts.json").read_text())["lead"],3)
                launcher.stop(root)
                runtime_path = root/".git/factory-runtime.json"
                retained = runtime_path.read_text()
                runtime_path.unlink()
                with self.assertRaisesRegex(ValueError,"refusing to seed another project"):
                    launcher.start(root)
                runtime_path.write_text(retained)
            except Exception as error:
                self.fail(str(error)+"\n"+(root/".git/factory-supervisor.log").read_text()[-15000:])
            finally:
                launcher.stop(root)

    def test_project_delivery_rejection_and_blocked_escalation(self):
        with (contextlib.nullcontext(os.environ["FACTORY_SMOKE_ROOT"]) if os.environ.get("FACTORY_SMOKE_ROOT") else tempfile.TemporaryDirectory(prefix="harness-factory-graph-")) as temporary:
            root = Path(temporary)
            shutil.copytree(ROOT / "factory", root / "factory", ignore=shutil.ignore_patterns("__pycache__"))
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            sys.path.insert(0, str(ROOT / "factory/scripts"))
            try:
                import project_admission
                project_admission.admit(root, "audio-runtime", "audio-runtime-v1")
            finally:
                sys.path.pop(0)
            fixture = root / "worker.py"
            fixture.write_text(FIXTURE)
            entries = []
            for worker, role in [("project-lead", "lead"), ("planner", "plan"),
                                 ("processor", "process"), ("reviewer", "review"),
                                 ("ideafier", "meta")]:
                entries.append({"workerName":worker, "runType":"script", "scriptConfig":{
                    "command":sys.executable, "args":[str(fixture), role]}})
            for worker in ("workspace-setup", "ci-waiter", "project-reconciler"):
                entries.append({"workerName":worker, "runType":"accept"})
            mock = root / "mock.json"
            mock.write_text(json.dumps({"unmatchedDispatchPolicy":"passthrough", "mockWorkers":entries}))
            initial = root / "initial.json"
            initial.write_text(json.dumps({"requestId":"smoke-admission", "type":"FACTORY_REQUEST_BATCH",
                "works":[{"name":"audio-runtime", "workTypeName":"project", "payload":{
                    "project":"audio-runtime", "contractRevision":"audio-runtime-v1"}}]}))
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            server = "http://127.0.0.1:" + str(port)
            env = dict(os.environ, PATH=str(Path(YOU).parent)+os.pathsep+os.environ.get("PATH", ""), FACTORY_ROOT=str(root), FACTORY_SERVER_URL=server,
                       FACTORY_PROJECT_MANIFEST=str(root / "factory/projects/audio-runtime/manifest.json"))
            log = root / "runtime.log"
            with log.open("w") as output:
                process = subprocess.Popen([YOU, "--verbose", "run", "--continuously", "--with-server",
                    "--listen", "127.0.0.1:"+str(port), "--dir", str(root / "factory"),
                    "--with-mock-workers", str(mock), "--work", str(initial),
                    "--record", str(root / "recording.json")], cwd=root, env=env,
                    stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                deadline = time.monotonic() + 40
                observed = None
                while time.monotonic() < deadline and process.poll() is None:
                    try:
                        board = get(server, "/factory-sessions/~default/work?maxResults=100&includeSuperseded=true")
                        works = board.get("results", [])
                        for work in works:
                            state = work.get("state", {})
                            if work.get("workTypeName") == "project" and state.get("name") == "blocked":
                                observed = works
                                break
                        if observed is not None:
                            break
                    except (OSError, ValueError):
                        pass
                    time.sleep(0.1)
                self.assertIsNotNone(observed, json.dumps(board) + "\n" + log.read_text()[-14000:])
                counts = json.loads((root / "counts.json").read_text())
                self.assertEqual(counts["lead"], 2, json.dumps(counts) + "\n" + json.dumps(observed) + "\n" + log.read_text()[-20000:])
                self.assertEqual(counts["plan"], 1)
                self.assertEqual(counts["process"], 2)
                self.assertEqual(counts["review"], 2)
                states = {(w.get("workTypeName"), w.get("name")): w.get("state", {}).get("name") for w in observed}
                self.assertEqual(states[("idea", "audio-runtime-c01-smoke")], "complete")
                self.assertEqual(states[("task", "audio-runtime-c01-smoke")], "complete")
                self.assertIsNotNone(project_admission.status(root))
                session_rows = get(server, "/factory-sessions")
                (root / "session-list.json").write_text(json.dumps(session_rows))
                session_id = session_rows["sessions"][0]["id"]
                project_admission.bind_session(root, "audio-runtime", "audio-runtime-v1", server, session_id)
                original_ids = {w["workId"] for w in observed if w.get("workTypeName") == "project"}
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=10)
                recording = root / "recording.json"
                with log.open("a") as output:
                    process = subprocess.Popen([YOU, "run", "--continuously", "--with-server",
                        "--listen", "127.0.0.1:"+str(port), "--dir", str(root / "factory"),
                        "--with-mock-workers", str(mock), "--resume", str(recording),
                        "--record", str(root / "successor.json")], cwd=root, env=env,
                        stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
                deadline = time.monotonic() + 20
                resumed = None
                while time.monotonic() < deadline and process.poll() is None:
                    try:
                        new_session = get(server, "/factory-sessions")["sessions"][0]["id"]
                        board = get(server, "/factory-sessions/"+new_session+"/work?maxResults=100&includeSuperseded=true")
                        if any(w.get("workTypeName") == "project" for w in board.get("results", [])):
                            resumed = board["results"]
                            break
                    except (OSError, ValueError, IndexError):
                        pass
                    time.sleep(0.1)
                self.assertIsNotNone(resumed, log.read_text()[-10000:])
                self.assertEqual({w["workId"] for w in resumed if w.get("workTypeName") == "project"}, original_ids)
                self.assertEqual(json.loads((root / "counts.json").read_text())["lead"], 2)
                self.assertNotEqual(new_session, session_id)
                project_admission.resume_session(root, "audio-runtime", "audio-runtime-v1", server,
                    session_id, new_session, {"path":str(recording), "sha256":hashlib.sha256(recording.read_bytes()).hexdigest()})
                self.assertEqual(project_admission.status(root)["sessionId"], new_session)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
