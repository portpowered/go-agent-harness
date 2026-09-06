#!/usr/bin/env python3
"""Start/stop the owned harness factory; preserve admission and recovery recordings."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time
import uuid
import urllib.request

import project_admission
from project_contract import ContractError, digest, manifest, read_json, root_path


SERVER = "http://127.0.0.1:7439"
LISTEN = "127.0.0.1:7439"
_STARTED_PROCESSES = {}


def runtime_environment(root):
    common = project_admission.common_dir(root)
    binary = common / "factory-bin/you"
    proof = common / "factory-bin/runtime.json"
    if not binary.is_file() or not proof.is_file():
        raise ContractError("pinned factory runtime is missing; follow factory/docs/overview.md")
    record = read_json(proof)
    if record.get("sha256") != digest(binary):
        raise ContractError("pinned factory runtime hash differs from build evidence")
    return dict(os.environ, PATH=str(binary.parent) + os.pathsep + os.environ.get("PATH", ""))


def paths(root):
    common = project_admission.common_dir(root)
    return common, common / "factory-runtime.json"


def write_record(path, record):
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    with temporary.open("x", encoding="utf-8") as stream:
        os.chmod(temporary, 0o600)
        json.dump(record, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def process_start(pid):
    if not isinstance(pid, int) or pid <= 1:
        return ""
    result = subprocess.run(["ps", "-p", str(pid), "-o", "lstart="],
                            capture_output=True, text=True, check=False)
    return result.stdout.strip() if result.returncode == 0 else ""


def running(record):
    owned = _STARTED_PROCESSES.get(record.get("pid")) if record else None
    if owned is not None and owned.poll() is not None:
        return False
    return bool(record and record.get("processStart") and
                process_start(record.get("pid")) == record["processStart"])


def status(root):
    _, path = paths(root)
    record = read_json(path) if path.exists() else None
    return {"running": running(record), "runtime": record,
            "admission": project_admission.status(root)}


def sessions(server):
    with urllib.request.urlopen(server + "/factory-sessions", timeout=2) as response:
        result = json.load(response)
    rows = result.get("sessions", [])
    if not isinstance(rows, list):
        raise ContractError("invalid factory session response")
    return rows


def session_identity(server):
    rows = sessions(server)
    if len(rows) != 1:
        raise ContractError("owned endpoint must contain exactly one factory session")
    row = rows[0]
    value = row.get("sessionId") or row.get("id")
    if not isinstance(value, str) or not value:
        raise ContractError("factory omitted session identity")
    return value


def stop(root):
    observed = status(root)
    record = observed["runtime"]
    if not observed["running"]:
        return observed
    if record.get("root") != str(root) or record.get("server") != SERVER:
        raise ContractError("refusing to stop a factory owned by another root or endpoint")
    pid = record["pid"]
    if os.getpgid(pid) != pid:
        raise ContractError("owned supervisor no longer owns its process group")
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 30
    while running(record) and time.monotonic() < deadline:
        time.sleep(0.1)
    if running(record):
        raise ContractError("factory did not stop; preserve evidence and inspect before retry")
    process = _STARTED_PROCESSES.pop(pid, None)
    if process is not None:
        process.wait(timeout=1)
    return status(root)


def serve(root):
    os.umask(0o077)
    contract = manifest(root)
    common, record_path = paths(root)
    lock = (common / "factory-runtime.lock").open("a+")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as error:
        raise ContractError("a factory supervisor already owns this repository") from error
    previous = read_json(record_path) if record_path.exists() else None
    if previous is None and project_admission.status(root) is not None:
        raise ContractError("admission exists without runtime evidence; refusing to seed another project")
    if running(previous):
        raise ContractError("an owned factory process is already running")
    if previous and (previous.get("project") != contract["project"] or
                     previous.get("root") != str(root) or previous.get("server") != SERVER):
        raise ContractError("saved runtime belongs to another project/root/endpoint")
    definition_hash = digest(root / "factory/factory.json")
    if previous and previous.get("definitionSha256") != definition_hash:
        raise ContractError("saved graph differs; migrate recorded work before restarting changed configuration")
    with socket.socket() as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        probe.bind(("127.0.0.1", 7439))
    project_admission.admit(root, contract["project"], contract["contractRevision"])
    runs = common / "factory-runs"
    runs.mkdir(mode=0o700, exist_ok=True)
    recording = runs / (uuid.uuid4().hex + ".json")
    startup = runs / (uuid.uuid4().hex + "-startup.json")
    command = ["you", "run", "--continuously", "--with-server", "--listen", LISTEN,
               "--record", str(recording)]
    evidence = None
    if previous:
        recovery = previous.get("recoveryInput") if previous.get("status") in {"starting", "failed"} else None
        source = Path(recovery["path"] if recovery else previous.get("recording", "")).resolve()
        if not source.is_relative_to(runs.resolve()) or not source.is_file():
            raise ContractError("owned recovery recording is unavailable; do not resubmit work")
        source_hash = digest(source)
        expected_hash = recovery.get("sha256") if recovery else previous.get("recordingSha256")
        if expected_hash and expected_hash != source_hash:
            raise ContractError("owned recovery recording has changed since it was saved")
        evidence = {"path": str(source), "sha256": source_hash}
        command += ["--resume", str(source)]
    else:
        batch = {"requestId": "admit-" + contract["project"] + "-" + contract["contractRevision"],
                 "type": "FACTORY_REQUEST_BATCH", "works": [{"name": contract["project"],
                 "workTypeName": "project", "payload": {"project": contract["project"],
                 "contractRevision": contract["contractRevision"]}}]}
        write_record(startup, batch)
        command += ["--work", str(startup)]
    environment = dict(runtime_environment(root), FACTORY_ROOT=str(root), FACTORY_SERVER_URL=SERVER,
                       FACTORY_PROJECT_MANIFEST=str(root / "factory/projects/audio-runtime/manifest.json"))
    child = None
    stopped = False
    def terminate(signum, frame):
        nonlocal stopped
        stopped = True
        if child is not None and child.poll() is None:
            child.send_signal(signal.SIGINT)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    record = {"project": contract["project"], "contractRevision": contract["contractRevision"],
              "root": str(root), "server": SERVER, "pid": os.getpid(),
              "processStart": process_start(os.getpid()), "childPid": None,
              "recording": str(recording), "definitionSha256": definition_hash,
              "runtimeBuild": read_json(common / "factory-bin/runtime.json"),
              "integrationRevision": previous["integrationRevision"] if previous else subprocess.check_output(["git", "-C", str(root),
                                         "rev-parse", "HEAD"], text=True).strip(),
              "status": "starting"}
    if previous and previous.get("sessionId"):
        record["sessionId"] = previous["sessionId"]
    if evidence:
        record["recoveryInput"] = evidence
    write_record(record_path, record)
    try:
        if stopped:
            raise ContractError("startup cancelled before runtime activation")
        child = subprocess.Popen(command, cwd=root, env=environment)
        record["childPid"] = child.pid
        write_record(record_path, record)
        deadline = time.monotonic() + 45
        session_id = None
        while not stopped and child.poll() is None and time.monotonic() < deadline:
            try:
                session_id = session_identity(SERVER)
                break
            except (OSError, ValueError):
                time.sleep(0.2)
        if not session_id:
            raise ContractError("factory did not expose one ready session within 45 seconds")
        if previous and previous.get("sessionId"):
            project_admission.resume_session(root, contract["project"], contract["contractRevision"],
                                             SERVER, previous["sessionId"], session_id, evidence)
        else:
            project_admission.bind_session(root, contract["project"], contract["contractRevision"], SERVER, session_id)
        record.update(status="running", sessionId=session_id)
        record.pop("recoveryInput", None)
        if previous:
            record["predecessor"] = evidence
        write_record(record_path, record)
        result = child.wait()
        record.update(status="stopped" if stopped else "exited", exitCode=result)
    except BaseException as error:
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=20)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        record.update(status="failed", error=str(error))
        raise
    finally:
        if recording.is_file():
            record["recordingSha256"] = digest(recording)
        write_record(record_path, record)
        fcntl.flock(lock, fcntl.LOCK_UN)
        lock.close()


def start(root):
    contract = manifest(root)
    observed = status(root)
    if observed["running"]:
        record, owner = observed["runtime"], observed["admission"]
        if (record.get("root") != str(root) or record.get("server") != SERVER or
            record.get("project") != contract["project"] or
            record.get("contractRevision") != contract["contractRevision"] or
            record.get("status") != "running" or not owner or
            owner.get("project") != contract["project"] or
            owner.get("contractRevision") != contract["contractRevision"] or
            owner.get("sessionId") != record.get("sessionId")):
            raise ContractError("live runtime ownership is mismatched or startup is incomplete")
        return observed
    if observed["runtime"] is None and observed["admission"] is not None:
        raise ContractError("admission exists without runtime evidence; refusing to seed another project")
    subprocess.run(["you", "factory", "config", "validate", str(root / "factory/factory.json")],
                   cwd=root, env=runtime_environment(root), check=True, capture_output=True)
    manifest(root)
    common, _ = paths(root)
    log = common / "factory-supervisor.log"
    with log.open("a") as stream:
        os.chmod(log, 0o600)
        process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()),
                                    "serve", "--root", str(root)], cwd=root,
                                   stdout=stream, stderr=subprocess.STDOUT,
                                   start_new_session=True)
    _STARTED_PROCESSES[process.pid] = process
    deadline = time.monotonic() + 50
    while time.monotonic() < deadline:
        observed = status(root)
        record = observed["runtime"] or {}
        if record.get("pid") == process.pid and record.get("status") == "running":
            return observed
        if process.poll() is not None:
            raise ContractError("factory supervisor failed; inspect " + str(log))
        time.sleep(0.2)
    raise ContractError("factory startup not confirmed; inspect " + str(log))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("operation", choices=["start", "stop", "restart", "status", "serve"])
    parser.add_argument("--root", type=Path, default=root_path())
    args = parser.parse_args()
    root = args.root.resolve()
    if args.operation == "serve":
        serve(root)
        return
    if args.operation == "restart":
        stop(root)
        result = start(root)
    else:
        result = {"start": start, "stop": stop, "status": status}[args.operation](root)
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
