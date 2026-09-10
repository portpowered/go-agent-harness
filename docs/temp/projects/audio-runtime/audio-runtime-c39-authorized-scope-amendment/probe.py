#!/usr/bin/env python3
"""Bounded, credential-free C39 controller and public-process probe.

The controller portion always runs in a temporary Git repository with a local
admission record and a fake canonical Work responder.  It never contacts the
factory server and never edits the active checkout.  A supplied yui is only
launched as an immutable process input; repeatable fixture/config pairs enable
the existing software replay regressions when primary has staged them.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


EVIDENCE_DIR = Path(__file__).resolve().parent
REPO_ROOT = EVIDENCE_DIR.parents[4]
SCRIPTS_DIR = REPO_ROOT / "factory" / "scripts"
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract
import project_scope_amendment as amendments


MAX_CHILD_SECONDS = 60
MAX_PROBE_SECONDS = 600
MAX_CAPTURE_BYTES = 8 * 1024 * 1024
MAX_OUTPUT_BYTES = 32 * 1024
OUTPUT_READ_CHUNK = 8 * 1024
CREDENTIAL_ENVIRONMENT_NAMES = {
    "OPENAI_API_KEY",
    "OPENROUTER_API_KEY",
    "ANTHROPIC_API_KEY",
    "GOOGLE_API_KEY",
    "GROK_API_KEY",
    "YOU_API_KEY",
}
REPLAY_CASES = {
    "audio-tool": {
        "fixtureSha256": "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
        "rendered": (3200, "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"),
        "provider": (4800, "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"),
        "replayPhrase": "Replay verified: 18 wire events, 1 tool calls",
    },
    "interruption": {
        "fixtureSha256": "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
        "rendered": (3360, "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"),
        "provider": (3840, "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"),
        "replayPhrase": "Replay verified: 15 wire events, 0 tool calls",
    },
}
PROTECTED_RELATIVES = (
    amendments.MANIFEST_RELATIVE,
    amendments.AUTHORIZATION_RELATIVE,
    amendments.HISTORICAL_REPORT_RELATIVE,
    "factory/projects/audio-runtime/source-plan.md",
    "factory/projects/audio-runtime/request.md",
    "factory/projects/audio-runtime/acceptance.md",
)


class ProbeError(RuntimeError):
    pass


def _sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def _json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n"


def _credential_free_environment(home: Path) -> dict[str, str]:
    environment = os.environ.copy()
    environment["HOME"] = str(home)
    for name in CREDENTIAL_ENVIRONMENT_NAMES:
        environment.pop(name, None)
    return environment


def _regular_file(path: Path, label: str) -> Path:
    if path.is_symlink() or not path.is_file():
        raise ProbeError(f"{label} must be a regular file")
    return path


def _regular_directory(path: Path, label: str) -> Path:
    if path.is_symlink() or not path.is_dir():
        raise ProbeError(f"{label} must be a regular directory")
    return path


def _bounded_bytes(path: Path, label: str) -> bytes:
    _regular_file(path, label)
    if path.stat().st_size > MAX_CAPTURE_BYTES:
        raise ProbeError(f"{label} exceeds the bounded capture size")
    with path.open("rb") as stream:
        data = stream.read(MAX_CAPTURE_BYTES + 1)
    if len(data) > MAX_CAPTURE_BYTES:
        raise ProbeError(f"{label} exceeds the bounded capture size")
    return data


def _protected_hashes(root: Path) -> dict[str, str]:
    return {relative: _sha256(root / relative) for relative in PROTECTED_RELATIVES}


def _assert_protected_hashes(
    before: dict[str, str], after: dict[str, str]
) -> None:
    if before != after:
        changed = sorted(
            key
            for key in set(before) | set(after)
            if before.get(key) != after.get(key)
        )
        raise ProbeError(
            "protected input hashes changed during controller probe: "
            + ", ".join(changed)
        )


def _copy_config(source: Path, destination: Path) -> dict[str, str]:
    _regular_directory(source, "replay config")
    destination.mkdir(mode=0o700, parents=True)
    copied = {}
    for entry in sorted(source.iterdir(), key=lambda item: item.name):
        _regular_file(entry, "replay config entry")
        target = destination / entry.name
        shutil.copy2(entry, target)
        copied[entry.name] = _sha256(target)
    if not copied:
        raise ProbeError("replay config directory is empty")
    return copied


def _config_hashes(source: Path) -> dict[str, str]:
    _regular_directory(source, "replay config")
    hashes = {}
    for entry in sorted(source.iterdir(), key=lambda item: item.name):
        _regular_file(entry, "replay config entry")
        hashes[entry.name] = _sha256(entry)
    if not hashes:
        raise ProbeError("replay config directory is empty")
    return hashes


def _copy_fixture(source: Path, destination: Path, expected_sha256: str) -> str:
    _regular_file(source, "replay fixture")
    actual = _sha256(source)
    if actual != expected_sha256:
        raise ProbeError(f"replay fixture digest mismatch: got {actual}, want {expected_sha256}")
    shutil.copy2(source, destination)
    if _sha256(destination) != actual:
        raise ProbeError("replay fixture changed while staging")
    return actual


def _capture_summary(path: Path, expected: tuple[int, str], label: str) -> dict[str, Any]:
    data = _bounded_bytes(path, label)
    digest = hashlib.sha256(data).hexdigest()
    size, expected_digest = expected
    if len(data) != size or digest != expected_digest:
        raise ProbeError(
            f"{label} mismatch: got {len(data)} bytes/{digest}, "
            f"want {size} bytes/{expected_digest}"
        )
    return {"path": str(path), "bytes": len(data), "sha256": digest}


def _session_log(bundle: Path) -> list[dict[str, Any]]:
    data = _bounded_bytes(bundle / "session-log.jsonl", "session log")
    records = []
    try:
        for line in data.decode("utf-8").splitlines():
            if line.strip():
                value = json.loads(line)
                if not isinstance(value, dict):
                    raise ProbeError("session log entry must be an object")
                records.append(value)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ProbeError("session log is not bounded valid JSONL") from error
    return records


def _replay_command(
    yui: Path,
    config: Path,
    fixture: Path,
    workdir: Path,
    bundle: Path,
    audio_out: Path,
) -> list[str]:
    return [
        str(yui),
        "-C",
        str(config),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(audio_out),
        "--record-dir",
        str(bundle),
        "--trace-audio",
        "--workdir",
        str(workdir),
        "--allow-path",
        str(workdir),
        "--max-duration",
        "60s",
    ]


def _directory_replay(
    yui: Path,
    bundle: Path,
    config_source: Path,
    parent: Path,
    phrase: str,
    label: str,
) -> dict[str, Any]:
    workdir = parent / f"{label}-directory-replay"
    (workdir / "evidence/runs").mkdir(parents=True)
    (workdir / "home").mkdir()
    config = workdir / "config"
    config_source_hashes = _config_hashes(config_source)
    config_hashes = _copy_config(config_source, config)
    result = run_bounded(
        [str(yui), "-C", str(config), "session", "replay", str(bundle)],
        cwd=workdir,
        env=_credential_free_environment(workdir / "home"),
        timeout=MAX_CHILD_SECONDS,
    )
    combined = f"{result['stdout']}\n{result['stderr']}"
    if result["exitCode"] != 0 or phrase not in combined:
        raise ProbeError(
            f"{label} directory replay failed: exit={result['exitCode']} "
            f"stdout={result['stdout']} stderr={result['stderr']}"
        )
    return {
        "result": result,
        "phrase": phrase,
        "configPath": str(config_source),
        "configHashes": config_hashes,
        "configSourceHashes": config_source_hashes,
    }


def _missing_timeline_replay(
    yui: Path,
    bundle: Path,
    config_source: Path,
    parent: Path,
    label: str,
) -> dict[str, Any]:
    missing_bundle = parent / f"{label}-missing-timeline-bundle"
    shutil.copytree(bundle, missing_bundle)
    timeline = missing_bundle / "audio-trace" / "timeline.jsonl"
    _regular_file(timeline, "generated timeline")
    timeline.unlink()
    workdir = parent / f"{label}-missing-timeline-replay"
    (workdir / "evidence/runs").mkdir(parents=True)
    (workdir / "home").mkdir()
    config = workdir / "config"
    config_source_hashes = _config_hashes(config_source)
    config_hashes = _copy_config(config_source, config)
    result = run_bounded(
        [str(yui), "-C", str(config), "session", "replay", str(missing_bundle)],
        cwd=workdir,
        env=_credential_free_environment(workdir / "home"),
        timeout=MAX_CHILD_SECONDS,
    )
    combined = f"{result['stdout']}\n{result['stderr']}".lower()
    if result["exitCode"] == 0 or "timeline" not in combined:
        raise ProbeError(
            f"{label} missing-timeline control failed: exit={result['exitCode']} "
            f"stdout={result['stdout']} stderr={result['stderr']}"
        )
    return {
        "result": result,
        "expected": "nonzero missing timeline",
        "configPath": str(config_source),
        "configHashes": config_hashes,
        "configSourceHashes": config_source_hashes,
    }


def _terminate_group(process: subprocess.Popen[str], sig: int) -> None:
    if process.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(process.pid, sig)
        else:  # pragma: no cover - exercised by the Windows validation host.
            process.send_signal(sig)
    except ProcessLookupError:
        pass


def run_bounded(
    command: list[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    timeout: float = MAX_CHILD_SECONDS,
) -> dict[str, Any]:
    """Run one child with bounded pipe draining and TERM/KILL/reap cleanup."""

    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=str(cwd) if cwd else None,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name == "posix",
    )
    captured: dict[str, bytes] = {"stdout": b"", "stderr": b""}
    truncated: dict[str, bool] = {"stdout": False, "stderr": False}
    read_errors: dict[str, BaseException] = {}

    def drain(name: str, stream) -> None:
        retained = bytearray()
        try:
            while True:
                chunk = stream.read(OUTPUT_READ_CHUNK)
                if not chunk:
                    break
                if len(retained) + len(chunk) > MAX_OUTPUT_BYTES:
                    truncated[name] = True
                retained.extend(chunk)
                if len(retained) > MAX_OUTPUT_BYTES:
                    del retained[:-MAX_OUTPUT_BYTES]
        except (OSError, ValueError) as error:
            read_errors[name] = error
        captured[name] = bytes(retained)

    readers = [
        threading.Thread(
            target=drain,
            args=(name, stream),
            name=f"c39-probe-{name}-reader",
            daemon=True,
        )
        for name, stream in (("stdout", process.stdout), ("stderr", process.stderr))
    ]
    for reader in readers:
        reader.start()
    timed_out = False
    cleanup = "none"
    wait_error: BaseException | None = None
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        cleanup = "term"
        _terminate_group(process, signal.SIGTERM)
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            cleanup = "kill"
            _terminate_group(process, signal.SIGKILL)
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired as reap_error:
                wait_error = ProbeError("child did not terminate after bounded KILL/reap")
                wait_error.__cause__ = reap_error
    finally:
        if process.poll() is None:
            cleanup = "kill"
            _terminate_group(process, signal.SIGKILL)
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired as reap_error:
                wait_error = ProbeError("child did not terminate after bounded KILL/reap")
                wait_error.__cause__ = reap_error
        for reader in readers:
            reader.join(timeout=2)
            if reader.is_alive() and wait_error is None:
                wait_error = ProbeError(
                    f"{reader.name} did not finish bounded pipe draining"
                )
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
    duration = time.monotonic() - started
    if process.poll() is None:
        raise ProbeError("child remained alive after bounded wait")
    if wait_error is not None:
        raise wait_error
    if read_errors:
        name, error = next(iter(read_errors.items()))
        raise ProbeError(f"cannot drain child {name}: {error}")
    stdout = captured["stdout"].decode("utf-8", errors="replace")
    stderr = captured["stderr"].decode("utf-8", errors="replace")
    return {
        "command": command,
        "exitCode": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
        "stdoutBytesRetained": len(captured["stdout"]),
        "stderrBytesRetained": len(captured["stderr"]),
        "stdoutTruncated": truncated["stdout"],
        "stderrTruncated": truncated["stderr"],
        "durationSeconds": round(duration, 6),
        "timedOut": timed_out,
        "cleanup": cleanup,
        "reaped": process.poll() is not None,
    }


def _run_git(root: Path, *arguments: str) -> None:
    result = subprocess.run(
        ["git", "-C", str(root), *arguments],
        capture_output=True,
        text=True,
        check=False,
        timeout=MAX_CHILD_SECONDS,
    )
    if result.returncode:
        raise ProbeError(f"git {' '.join(arguments)} failed: {result.stderr.strip()}")


def _source_for(relative: str) -> Path:
    candidates = [REPO_ROOT / relative]
    configured = os.environ.get("FACTORY_ROOT")
    if configured:
        candidates.append(Path(configured).resolve() / relative)
    for candidate in candidates:
        if candidate.is_file() and not candidate.is_symlink():
            return candidate
    raise ProbeError(f"reviewed input is unavailable: {relative}")


def _input_provenance(
    source_revision: str,
    yui: Path,
    replay_pairs: list[tuple[Path, Path]],
) -> dict[str, Any]:
    """Record every controller/probe executable input before it is launched."""

    source_inputs = {}
    for relative in (
        amendments.MANIFEST_RELATIVE,
        "factory/projects/audio-runtime/source-plan.md",
        "factory/projects/audio-runtime/request.md",
        "factory/projects/audio-runtime/acceptance.md",
        "factory/projects/audio-runtime/amendments/user-windows-hardware-scope-20260910.json",
    ):
        path = _regular_file(REPO_ROOT / relative, relative)
        source_inputs[relative] = {"path": str(path), "sha256": _sha256(path)}
    script_inputs = {}
    for relative in (
        "factory/scripts/project-control.py",
        "factory/scripts/prepare-validation.py",
        "factory/scripts/project_scope_amendment.py",
    ):
        path = _regular_file(REPO_ROOT / relative, relative)
        script_inputs[relative] = {"path": str(path), "sha256": _sha256(path)}
    reviewed_inputs = {}
    for relative in (
        amendments.AUTHORIZATION_RELATIVE,
        amendments.HISTORICAL_REPORT_RELATIVE,
    ):
        path = _source_for(relative)
        reviewed_inputs[relative] = {"path": str(path), "sha256": _sha256(path)}
    replay_inputs = []
    if replay_pairs:
        for label, (fixture, config) in zip(
            ("audio-tool", "interruption"), replay_pairs, strict=True
        ):
            fixture_path = _regular_file(fixture, f"{label} replay fixture")
            replay_inputs.append(
                {
                    "label": label,
                    "fixture": {
                        "path": str(fixture_path),
                        "sha256": _sha256(fixture_path),
                    },
                    "config": {
                        "path": str(_regular_directory(config, f"{label} replay config")),
                        "files": _config_hashes(config),
                    },
                }
            )
    yui_path = _regular_file(yui, "yui")
    return {
        "sourceRevision": source_revision,
        "sourceRepository": str(REPO_ROOT),
        "probeScript": {
            "path": str(Path(__file__).resolve()),
            "sha256": _sha256(Path(__file__).resolve()),
        },
        "sourceInputs": source_inputs,
        "controllerScripts": script_inputs,
        "reviewedInputs": reviewed_inputs,
        "yui": {"path": str(yui_path), "sha256Before": _sha256(yui_path)},
        "replayInputs": replay_inputs,
    }


def _copy_reviewed_inputs(root: Path) -> None:
    for relative in (
        amendments.AUTHORIZATION_RELATIVE,
        amendments.HISTORICAL_REPORT_RELATIVE,
    ):
        destination = root / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(_source_for(relative), destination)


def _make_fixture(parent: Path) -> Path:
    root = (parent / "controller-fixture").resolve()
    root.mkdir()
    _run_git(root.parent, "init", "-q", "-b", "main", str(root))
    _run_git(root, "config", "user.name", "C39 isolated probe")
    _run_git(root, "config", "user.email", "c39-probe@example.invalid")
    source = REPO_ROOT / "factory" / "projects" / amendments.PROJECT
    destination = root / "factory" / "projects" / amendments.PROJECT
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(source, destination)
    # The candidate record is a deliverable, but this isolated fixture must
    # start before publication so it exercises the fresh append path.
    shutil.rmtree(destination / "amendments", ignore_errors=True)
    (root / "docs" / "temp" / "projects" / amendments.PROJECT).mkdir(parents=True)
    (root / "docs" / "temp" / "probes").mkdir(parents=True)
    _copy_reviewed_inputs(root)
    project_admission.ProjectAdmission(root).Admit(
        amendments.PROJECT,
        amendments.CONTRACT_REVISION,
    )
    return root


def _controller_env(fake_you: Path | None = None) -> dict[str, str]:
    environment = os.environ.copy()
    environment.pop("FACTORY_ROOT", None)
    environment.pop("FACTORY_PROJECT_MANIFEST", None)
    if fake_you is not None:
        environment["PATH"] = str(fake_you.parent) + os.pathsep + environment.get("PATH", "")
    return environment


def _controller(
    script: str,
    root: Path,
    *arguments: str,
    fake_you: Path | None = None,
) -> dict[str, Any]:
    return run_bounded(
        [sys.executable, str(SCRIPTS_DIR / script), "--root", str(root), *arguments],
        cwd=root,
        env=_controller_env(fake_you),
    )


def _record_controller_effects(fixture: Path, output: Path) -> dict[str, Any]:
    protected_before = _protected_hashes(fixture)
    record_input = fixture / "candidate-record.json"
    record_input.write_bytes(
        amendments.canonical_bytes(amendments.create_record(fixture))
    )
    append = _controller(
        "project-control.py",
        fixture,
        "amendment-append",
        "--record",
        str(record_input),
    )
    if append["exitCode"] != 0:
        raise ProbeError(f"authorized append failed: {append['stderr']}")
    append_repeat = _controller(
        "project-control.py",
        fixture,
        "amendment-append",
        "--record",
        str(record_input),
    )
    if append_repeat["exitCode"] != 0:
        raise ProbeError(f"idempotent append failed: {append_repeat['stderr']}")
    repeat_value = json.loads(append_repeat["stdout"])
    if repeat_value.get("status") != "already-present":
        raise ProbeError("idempotent append did not preserve the published record")
    status = _controller(
        "project-control.py",
        fixture,
        "amendment-status",
        "--amendment-id",
        amendments.AMENDMENT_ID,
    )
    if status["exitCode"] != 0:
        raise ProbeError(f"amendment status failed: {status['stderr']}")
    status_value = json.loads(status["stdout"])
    reference = status_value["amendment"]

    build_path = fixture / "artifacts" / "c39-build.bin"
    build_path.parent.mkdir()
    build_path.write_bytes(b"c39 controller artifact\n")
    build = {
        "identity": "c39-controller-build",
        "path": str(build_path),
        "sha256": _sha256(build_path),
    }
    prepared = {}
    reports = {}
    contract = project_contract.manifest(fixture)
    for role in ("customer", "engineering"):
        work_name = f"audio-runtime-c39-{role}-mission"
        report_path = (
            fixture
            / "docs"
            / "temp"
            / "projects"
            / amendments.PROJECT
            / f"{role}-report.json"
        )
        packet = {
            "project": amendments.PROJECT,
            "contractRevision": amendments.CONTRACT_REVISION,
            "scope": "project",
            "role": role,
            "sourceRevision": "c39-controller-fixture-source",
            "criteria": copy.deepcopy(contract["criteria"]),
            "budget": {
                "timeSeconds": 30,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Fresh amended project-scope controller fixture.",
            "reportPath": str(report_path),
            "build": build,
            "fixtures": [],
            "amendment": reference,
        }
        prepared_result = _controller(
            "prepare-validation.py",
            fixture,
            work_name,
            json.dumps(packet),
        )
        if prepared_result["exitCode"] != 0:
            raise ProbeError(f"amended preparation failed: {prepared_result['stderr']}")
        prepared[role] = json.loads(prepared_result["stdout"])
        mission_path = Path(prepared[role]["directory"]) / "mission.json"
        mission = json.loads(mission_path.read_text(encoding="utf-8"))
        criteria = {
            entry["id"]: {
                "rubric": entry["rubric"],
                "verdict": "PASS",
                "evidence": (
                    f"Fresh retained software evidence for {entry['id']}; "
                    "authorized physical subproof is OUT OF SCOPE."
                ),
            }
            for entry in mission["criteria"]
        }
        reports[role] = report_path
        report_path.write_text(
            _json(
                {
                    "project": amendments.PROJECT,
                    "contractRevision": amendments.CONTRACT_REVISION,
                    "scope": "project",
                    "role": role,
                    "sourceRevision": mission["sourceRevision"],
                    "validationWorkName": mission["validationWorkName"],
                    "validationWorkId": f"c39-controller-validation-{role}",
                    "build": build,
                    "amendment": reference,
                    "missionPath": str(mission_path),
                    "missionSha256": prepared[role]["missionSha256"],
                    "criteria": criteria,
                    "scopeEvidence": {
                        "excludedSubproof": reference["excludedSubproof"],
                        "historicalReport": reference["historicalReport"],
                        "retained": {
                            key: {
                                "verdict": "PASS",
                                "evidence": f"Fresh {key} proof.",
                            }
                            for key in amendments.RETAINED_EVIDENCE_KEYS
                        },
                    },
                }
            ),
            encoding="utf-8",
        )

    project_admission.bind_session(
        fixture,
        amendments.PROJECT,
        amendments.CONTRACT_REVISION,
        "http://fixture.invalid",
        "c39-controller-session",
    )
    common = project_admission.common_dir(fixture)
    (common / "factory-runtime.json").write_text(
        _json(
            {
                "project": amendments.PROJECT,
                "contractRevision": amendments.CONTRACT_REVISION,
                "sessionId": "c39-controller-session",
                "server": "http://fixture.invalid",
            }
        ),
        encoding="utf-8",
    )
    fake_bin = fixture / "fake-bin"
    fake_bin.mkdir()
    work_responses = {}
    for role, report_path in reports.items():
        report = json.loads(report_path.read_text(encoding="utf-8"))
        mission_path = Path(report["missionPath"])
        mission = json.loads(mission_path.read_text(encoding="utf-8"))
        work_id = report["validationWorkId"]
        work_responses[work_id] = {
            "workId": work_id,
            "name": report["validationWorkName"],
            "project": amendments.PROJECT,
            "workTypeName": "validation",
            "state": {"name": "complete"},
            "tags": {
                "_work_name": report["validationWorkName"],
                "_last_output": json.dumps(
                    {
                        "status": "ready",
                        "directory": str(mission_path.parent),
                        "project": mission["project"],
                        "build": mission["build"],
                        "validationWorkName": mission["validationWorkName"],
                        "missionSha256": report["missionSha256"],
                    }
                ),
            },
        }
    work_map = fake_bin / "works.json"
    work_map.write_text(_json(work_responses), encoding="utf-8")
    fake_you = fake_bin / "you"
    fake_you.write_text(
        "#!/usr/bin/env python3\n"
        "import json\n"
        "import pathlib\n"
        "import sys\n"
        "work_id = sys.argv[sys.argv.index('show') + 1]\n"
        "works = json.loads((pathlib.Path(__file__).with_name('works.json')).read_text())\n"
        "print(json.dumps(works[work_id]))\n",
        encoding="utf-8",
    )
    fake_you.chmod(0o700)
    completion_path = (
        fixture
        / "docs"
        / "temp"
        / "projects"
        / amendments.PROJECT
        / "completion.json"
    )
    completion_path.write_text(
        _json(
            {
                "project": amendments.PROJECT,
                "contractRevision": amendments.CONTRACT_REVISION,
                "build": build,
                "amendment": reference,
                "reports": {role: str(path) for role, path in reports.items()},
            }
        ),
        encoding="utf-8",
    )
    completion = _controller(
        "project-control.py",
        fixture,
        "verify-completion",
        "--name",
        amendments.PROJECT,
        fake_you=fake_you,
    )
    if completion["exitCode"] != 0:
        raise ProbeError(f"amended completion failed: {completion['stderr']}")

    forged = copy.deepcopy(amendments.create_record(fixture))
    forged["excludedSubproof"].append(
        {
            "criterionId": "DEVICE",
            "subproof": "all device consumption",
            "verdict": "OUT_OF_SCOPE",
        }
    )
    forged_path = fixture / "forged-record.json"
    forged_path.write_bytes(amendments.canonical_bytes(forged))
    rejection = _controller(
        "project-control.py",
        fixture,
        "amendment-append",
        "--record",
        str(forged_path),
    )
    if rejection["exitCode"] == 0:
        raise ProbeError("expanded exclusion was accepted")

    protected_after = _protected_hashes(fixture)
    _assert_protected_hashes(protected_before, protected_after)
    return {
        "append": json.loads(append["stdout"]),
        "appendRepeat": repeat_value,
        "status": status_value,
        "prepared": prepared,
        "completion": completion,
        "negativeExpandedExclusion": rejection,
        "protectedHashes": protected_after,
        "protectedHashesBefore": protected_before,
        "protectedHashesAfter": protected_after,
        "protectedHashesUnchanged": True,
        "admission": project_admission.status(fixture),
    }


def _public_replay_case(
    yui: Path,
    output: Path,
    fixture: Path,
    config_source: Path,
    label: str,
) -> dict[str, Any]:
    expectations = REPLAY_CASES[label]
    run_dir = output / f"public-replay-{label}"
    (run_dir / "evidence/runs").mkdir(parents=True)
    (run_dir / "home").mkdir()
    config = run_dir / "config"
    config_source_hashes = _config_hashes(config_source)
    config_hashes = _copy_config(config_source, config)
    staged_fixture = run_dir / f"{label}.fixture.json"
    fixture_hash = _copy_fixture(
        fixture,
        staged_fixture,
        expectations["fixtureSha256"],
    )
    bundle = run_dir / "bundle"
    replay = run_bounded(
        _replay_command(
            yui,
            config,
            staged_fixture,
            run_dir,
            bundle,
            run_dir / "rendered.pcm",
        ),
        cwd=run_dir,
        env=_credential_free_environment(run_dir / "home"),
        timeout=MAX_CHILD_SECONDS,
    )
    if replay["exitCode"] != 0:
        raise ProbeError(
            f"{label} replay failed: exit={replay['exitCode']} "
            f"stdout={replay['stdout']} stderr={replay['stderr']}"
        )
    rendered = _capture_summary(
        run_dir / "rendered.pcm",
        expectations["rendered"],
        f"{label} rendered PCM",
    )
    provider = _capture_summary(
        bundle / "audio" / "out-000.pcm",
        expectations["provider"],
        f"{label} provider PCM",
    )
    _regular_file(bundle / "audio-trace" / "timeline.jsonl", f"{label} audio timeline")
    session_log = _session_log(bundle)
    if label == "audio-tool":
        combined = f"{replay['stdout']}\n{replay['stderr']}\n{json.dumps(session_log)}"
        if "PROBE_TOOL_MARKER_9182" not in combined or "strict replay continuation" not in combined:
            raise ProbeError(f"{label} replay omitted the tool marker or continuation")
        marker = _capture_summary(
            run_dir / "evidence" / "runs" / "exec-invocations-v4.log",
            (23, "f91134b50758e6d4418ab08f6a3afa9f2acceb91117c3f67d0db515b762eb43e"),
            f"{label} tool side effect",
        )
        if len(session_log) != 1 or len(session_log[0].get("tool_events", [])) != 2:
            raise ProbeError(f"{label} session log omitted the exact tool lifecycle")
        extra = {"toolSideEffect": marker}
    else:
        audio_bytes = [
            entry.get("response", {}).get("audio_bytes") for entry in session_log
        ]
        if len(session_log) != 2 or audio_bytes != [1440, 2400]:
            raise ProbeError(
                f"{label} did not preserve the cancelled/follow-on audio sequence: "
                f"{audio_bytes!r}"
            )
        if not all(entry.get("response", {}).get("complete") for entry in session_log):
            raise ProbeError(f"{label} response sequence did not complete cleanly")
        provider_bytes = _bounded_bytes(
            bundle / "audio" / "out-000.pcm",
            f"{label} provider PCM",
        )
        healthy_tail = provider_bytes[-2400:]
        healthy_hash = hashlib.sha256(healthy_tail).hexdigest()
        expected_healthy_hash = "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"
        if healthy_hash != expected_healthy_hash:
            raise ProbeError(
                f"{label} healthy follow-on tail mismatch: got {healthy_hash}, "
                f"want {expected_healthy_hash}"
            )
        extra = {
            "audioBytes": audio_bytes,
            "healthyFollowOnTail": {
                "bytes": len(healthy_tail),
                "sha256": healthy_hash,
            },
        }
    directory_replay = _directory_replay(
        yui,
        bundle,
        config_source,
        run_dir,
        expectations["replayPhrase"],
        label,
    )
    missing_timeline = _missing_timeline_replay(
        yui,
        bundle,
        config_source,
        run_dir,
        label,
    )
    if _sha256(fixture) != fixture_hash:
        raise ProbeError(f"{label} replay fixture changed during the probe")
    return {
        "label": label,
        "fixture": str(fixture),
        "fixtureSha256": fixture_hash,
        "config": str(config_source),
        "configHashes": config_hashes,
        "configSourceHashes": config_source_hashes,
        "capture": replay,
        "rendered": rendered,
        "provider": provider,
        "sessionLog": session_log,
        "directoryReplay": directory_replay,
        "missingTimelineControl": missing_timeline,
        **extra,
    }


def _yui_probe(
    yui: Path,
    output: Path,
    replay_pairs: list[tuple[Path, Path]],
) -> dict[str, Any]:
    _regular_file(yui, "yui")
    before = _sha256(yui)
    smoke_dir = output / "public-help"
    smoke_dir.mkdir()
    (smoke_dir / "home").mkdir()
    result: dict[str, Any] = {
        "path": str(yui),
        "sha256Before": before,
        "help": run_bounded(
            [str(yui), "--help"],
            cwd=smoke_dir,
            env=_credential_free_environment(smoke_dir / "home"),
            timeout=MAX_CHILD_SECONDS,
        ),
    }
    if result["help"]["exitCode"] != 0:
        raise ProbeError(
            f"supplied yui --help failed: stdout={result['help']['stdout']} "
            f"stderr={result['help']['stderr']}"
        )
    if replay_pairs:
        result["replays"] = {
            label: _public_replay_case(yui, output, fixture, config, label)
            for label, (fixture, config) in zip(
                ("audio-tool", "interruption"), replay_pairs, strict=True
            )
        }
    result["sha256After"] = _sha256(yui)
    if result["sha256After"] != before:
        raise ProbeError("supplied yui changed during probe")
    return result


def _write_failure_report(
    output: Path,
    source_revision: str,
    error: BaseException,
    elapsed: float,
) -> Path | None:
    """Publish bounded failure evidence for a fresh output directory."""

    if output.is_symlink():
        return None
    if not output.exists():
        output.mkdir(mode=0o700, parents=True)
    if not output.is_dir():
        return None
    report_path = output / "probe-report.json"
    if report_path.exists():
        report_path = output / "probe-failure.json"
    report = {
        "schema": "audio-runtime-c39-controller-probe.v1",
        "status": "FAILED",
        "sourceRevision": source_revision,
        "error": str(error)[:4096],
        "limits": {
            "childSeconds": MAX_CHILD_SECONDS,
            "totalSeconds": MAX_PROBE_SECONDS,
            "realtimeSessions": 0,
            "realtimeSeconds": 0,
        },
        "durationSeconds": round(elapsed, 6),
    }
    try:
        with report_path.open("x", encoding="utf-8") as stream:
            stream.write(_json(report))
    except FileExistsError:
        return None
    return report_path


def run(args: argparse.Namespace) -> dict[str, Any]:
    started = time.monotonic()
    source_revision = args.source_revision.strip()
    if not source_revision:
        raise ProbeError("source revision is required")
    actual_revision = subprocess.check_output(
        ["git", "-C", str(REPO_ROOT), "rev-parse", "HEAD"],
        text=True,
        timeout=MAX_CHILD_SECONDS,
    ).strip()
    if actual_revision != source_revision:
        raise ProbeError(
            f"source revision mismatch: expected {source_revision}, observed {actual_revision}"
        )
    output = Path(args.output).expanduser().resolve()
    if not output.is_absolute() or output.exists():
        raise ProbeError("output must be a fresh directory")
    output.mkdir(mode=0o700, parents=True)
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c39-probe-") as temporary:
        fixture = _make_fixture(Path(temporary))
        controller = _record_controller_effects(fixture, output)
    timeout_control = run_bounded(
        [sys.executable, "-c", "import time; time.sleep(120)"],
        timeout=0.5,
    )
    if not timeout_control["timedOut"] or not timeout_control["reaped"]:
        raise ProbeError("deterministic timeout cleanup control did not time out and reap")
    replay_fixtures = [Path(value).expanduser() for value in (args.replay_fixture or [])]
    replay_configs = [Path(value).expanduser() for value in (args.replay_config or [])]
    if len(replay_fixtures) != len(replay_configs):
        raise ProbeError("each replay fixture requires one replay config directory")
    yui = Path(args.yui).expanduser()
    replay_pairs = list(zip(replay_fixtures, replay_configs, strict=True))
    input_provenance = _input_provenance(source_revision, yui, replay_pairs)
    yui_result = _yui_probe(yui, output, replay_pairs)
    input_provenance["yui"]["sha256After"] = yui_result["sha256After"]
    elapsed = time.monotonic() - started
    if elapsed > MAX_PROBE_SECONDS:
        raise ProbeError("probe exceeded the 600 second total deadline")
    report = {
        "schema": "audio-runtime-c39-controller-probe.v1",
        "status": "ACCEPTED",
        "sourceRevision": source_revision,
        "sourceRepository": str(REPO_ROOT),
        "controller": controller,
        "timeoutControl": timeout_control,
        "yui": yui_result,
        "inputProvenance": input_provenance,
        "limits": {
            "childSeconds": MAX_CHILD_SECONDS,
            "totalSeconds": MAX_PROBE_SECONDS,
            "realtimeSessions": 0,
            "realtimeSeconds": 0,
        },
        "residuals": [
            "This controller probe does not prove native Windows endpoint consumption or physical acoustic output.",
            "The historical C32 report remains FAILED with DEVICE BLOCKED and is not relabeled.",
            "A replay result is software/file evidence only; final project acceptance still needs fresh customer and engineering reports.",
        ],
        "durationSeconds": round(elapsed, 6),
    }
    (output / "probe-report.json").write_text(_json(report), encoding="utf-8")
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--yui", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--replay-fixture", action="append")
    parser.add_argument("--replay-config", action="append")
    args = parser.parse_args()
    output = Path(args.output).expanduser().resolve()
    output_preexisted = output.exists()
    started = time.monotonic()
    try:
        report = run(args)
    except (OSError, ProbeError, subprocess.SubprocessError, ValueError) as error:
        try:
            if not output_preexisted:
                _write_failure_report(
                    output,
                    args.source_revision,
                    error,
                    time.monotonic() - started,
                )
        except OSError:
            pass
        print(str(error), file=sys.stderr)
        return 1
    print(json.dumps({"status": report["status"], "report": str(Path(args.output).resolve() / "probe-report.json")}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
