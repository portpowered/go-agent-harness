#!/usr/bin/env python3
"""Exercise prepare-validation.py through isolated public subprocesses."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import subprocess
import sys
import tempfile


REPO_ROOT = Path(__file__).resolve().parents[5]
SCRIPTS_DIR = REPO_ROOT / "factory" / "scripts"
PROJECT = "audio-runtime"
CONTRACT_REVISION = "audio-runtime-v1"
MAX_OUTPUT_BYTES = 64 * 1024
CHILD_TIMEOUT_SECONDS = 10

if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

import project_admission
import project_contract


def _sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _source_revision():
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or "could not resolve source revision")
    return result.stdout.strip()


def _child_environment(root):
    environment = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith("FACTORY_")
    }
    old_pythonpath = environment.get("PYTHONPATH")
    environment["FACTORY_ROOT"] = str(root)
    environment["PYTHONPATH"] = os.pathsep.join(
        value for value in (str(SCRIPTS_DIR), old_pythonpath) if value
    )
    environment["LC_ALL"] = "C"
    environment["LANG"] = "C"
    environment["PYTHONNOUSERSITE"] = "1"
    return environment


def _run_child(command, cwd, environment):
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=CHILD_TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
    if len(stdout.encode()) > MAX_OUTPUT_BYTES or len(stderr.encode()) > MAX_OUTPUT_BYTES:
        raise RuntimeError("public child output exceeded the bounded capture")
    if timed_out:
        raise RuntimeError(
            f"child exceeded {CHILD_TIMEOUT_SECONDS}s: stdout={stdout!r} stderr={stderr!r}"
        )
    return process.returncode, stdout, stderr


class Fixture:
    """A temporary Git/admission root using the admitted project identity."""

    def __init__(self, parent, label):
        self.root = (Path(parent) / label / "repo").resolve()
        self.root.mkdir(parents=True)
        self._run("git", "init", "-q", "-b", "main", str(self.root))
        source = REPO_ROOT / "factory" / "projects" / PROJECT
        destination = self.root / "factory" / "projects" / PROJECT
        destination.parent.mkdir(parents=True)
        shutil.copytree(source, destination)
        (self.root / "docs" / "temp" / "projects" / PROJECT).mkdir(parents=True)
        (self.root / "docs" / "temp" / "probes").mkdir(parents=True)
        self.admission = project_admission.ProjectAdmission(self.root)
        self.admission.Admit(PROJECT, CONTRACT_REVISION)
        self.contract = project_contract.manifest(self.root)
        self.build = self.root / "artifacts" / "build.bin"
        self.fixture = self.root / "artifacts" / "fixture.pcm"
        self.build.parent.mkdir(parents=True, exist_ok=True)
        self.build.write_bytes(b"meaningful executable fixture\n")
        self.fixture.write_bytes(bytes(range(32)))
        self.build.chmod(0o444)
        self.fixture.chmod(0o444)

    @staticmethod
    def _run(*command):
        result = subprocess.run(
            list(command),
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )
        if result.returncode:
            raise RuntimeError(result.stderr.strip() or "fixture command failed")
        return result

    def artifact(self, path, identity):
        return {
            "identity": identity,
            "path": str(path),
            "sha256": _sha256(path),
        }

    def packet(self, name, *, time_seconds=900, scope="vertical"):
        criteria = [
            {"id": item["id"], "rubric": item["rubric"]}
            for item in self.contract["criteria"]
        ]
        packet = {
            "project": PROJECT,
            "contractRevision": CONTRACT_REVISION,
            "role": "engineering",
            "scope": scope,
            "criteria": criteria,
            "budget": {
                "timeSeconds": time_seconds,
                "realtimeSessions": 0,
                "realtimeSeconds": 0,
            },
            "mission": "Run the bounded validation control.",
            "reportPath": str(
                self.root
                / "docs"
                / "temp"
                / "projects"
                / PROJECT
                / f"{name}.json"
            ),
            "build": self.artifact(self.build, "build-fixture-v1"),
            "fixtures": [self.artifact(self.fixture, "pcm-fixture-v1")],
        }
        if scope == "vertical":
            packet["vertical"] = "audio-runtime-c29"
            packet["sourceRevision"] = "candidate-source"
        return packet

    def run(self, script, name, packet):
        payload = json.dumps(packet, sort_keys=True)
        command = [sys.executable, str(script), name, payload]
        returncode, stdout, stderr = _run_child(
            command,
            self.root,
            _child_environment(self.root),
        )
        mission = self.root / "docs" / "temp" / "probes" / name / "mission.json"
        return {
            "command": command,
            "packet": packet,
            "returncode": returncode,
            "stdout": stdout,
            "stderr": stderr,
            "missionPath": str(mission),
            "missionExists": mission.is_file(),
        }


def _assert_failure(outcome, label):
    if outcome["returncode"] == 0:
        raise AssertionError(f"{label}: invalid packet unexpectedly succeeded: {outcome}")
    if outcome["stdout"]:
        raise AssertionError(f"{label}: failure wrote stdout: {outcome}")
    if outcome["missionExists"]:
        raise AssertionError(f"{label}: failure published mission: {outcome}")


def _read_ready(outcome, label):
    if outcome["returncode"] != 0 or outcome["stderr"]:
        raise AssertionError(f"{label}: expected ready, got {outcome}")
    lines = outcome["stdout"].splitlines()
    if len(lines) != 1:
        raise AssertionError(f"{label}: expected one JSON envelope: {outcome}")
    envelope = json.loads(lines[0])
    if envelope.get("status") != "ready" or not outcome["missionExists"]:
        raise AssertionError(f"{label}: invalid ready envelope: {outcome}")
    return envelope


def _assert_ready_contract(fixture, packet, outcome, label):
    envelope = _read_ready(outcome, label)
    target = Path(envelope["directory"])
    mission_path = target / "mission.json"
    mission = json.loads(mission_path.read_text(encoding="utf-8"))
    if mission["budget"] != packet["budget"]:
        raise AssertionError(f"{label}: budget was changed: {mission}")
    if mission["project"] != PROJECT or mission["contractRevision"] != CONTRACT_REVISION:
        raise AssertionError(f"{label}: project identity changed: {mission}")
    if mission["scope"] != packet["scope"] or mission["criteria"] != packet["criteria"]:
        raise AssertionError(f"{label}: scope or criteria changed: {mission}")
    if mission["authority"] != fixture.contract["authority"]:
        raise AssertionError(f"{label}: authority changed: {mission}")
    if mission["reportPath"] != packet["reportPath"] or Path(packet["reportPath"]).exists():
        raise AssertionError(f"{label}: report path was not fresh: {mission}")
    if Path(mission["build"]["path"]).parent != target:
        raise AssertionError(f"{label}: build escaped staging root: {mission}")
    if any(Path(item["path"]).parent != target for item in mission["fixtures"]):
        raise AssertionError(f"{label}: fixture escaped staging root: {mission}")
    if _sha256(Path(mission["build"]["path"])) != packet["build"]["sha256"]:
        raise AssertionError(f"{label}: staged build digest changed: {mission}")
    if _sha256(Path(mission["fixtures"][0]["path"])) != packet["fixtures"][0]["sha256"]:
        raise AssertionError(f"{label}: staged fixture digest changed: {mission}")
    expected_modes = {
        target: 0o700,
        mission_path: 0o400,
        Path(mission["build"]["path"]): 0o500,
        Path(mission["fixtures"][0]["path"]): 0o400,
    }
    for path, mode in expected_modes.items():
        if stat.S_IMODE(path.stat().st_mode) != mode:
            raise AssertionError(f"{label}: wrong mode for {path}: {oct(path.stat().st_mode)}")
    return {"envelope": envelope, "mission": mission}


def _run_legacy(script):
    outcomes = {}
    with tempfile.TemporaryDirectory(prefix="c29-legacy-") as temporary:
        fixture = Fixture(temporary, "legacy")
        for seconds in (900, 1800):
            name = f"audio-runtime-c29-before-{seconds}"
            packet = fixture.packet(name, time_seconds=seconds)
            outcome = fixture.run(script, name, packet)
            outcomes[str(seconds)] = outcome
            if seconds == 900:
                _assert_failure(outcome, "legacy-900")
                if outcome["stderr"].strip() != "mission requires a 1800-second budget and description":
                    raise AssertionError(f"legacy-900: unexpected diagnostic: {outcome}")
            else:
                _assert_ready_contract(fixture, packet, outcome, "legacy-1800")
        return {
            "sourceScript": str(script),
            "sourceScriptSha256": _sha256(script),
            "buildSha256": _sha256(fixture.build),
            "fixtureSha256": _sha256(fixture.fixture),
            "outcomes": outcomes,
        }


def _run_bounded(script):
    report = {"positive": {}, "negative": {}, "mutationControls": {}}
    with tempfile.TemporaryDirectory(prefix="c29-bounded-") as temporary:
        for seconds in (1, 900, 1800):
            fixture = Fixture(temporary, f"positive-{seconds}")
            name = f"audio-runtime-c29-positive-{seconds}"
            packet = fixture.packet(name, time_seconds=seconds)
            before = {
                "build": fixture.build.read_bytes(),
                "fixture": fixture.fixture.read_bytes(),
                "admission": fixture.admission.Status(),
            }
            outcome = fixture.run(script, name, packet)
            verified = _assert_ready_contract(
                fixture,
                packet,
                outcome,
                f"positive-{seconds}",
            )
            if fixture.build.read_bytes() != before["build"] or fixture.fixture.read_bytes() != before["fixture"]:
                raise AssertionError(f"positive-{seconds}: source artifact changed")
            if fixture.admission.Status() != before["admission"]:
                raise AssertionError(f"positive-{seconds}: admission changed")
            report["positive"][str(seconds)] = {
                "outcome": outcome,
                "verified": verified,
                "sourceModes": {
                    "build": oct(stat.S_IMODE(fixture.build.stat().st_mode)),
                    "fixture": oct(stat.S_IMODE(fixture.fixture.stat().st_mode)),
                },
            }

        project_fixture = Fixture(temporary, "positive-project")
        project_name = "audio-runtime-c29-positive-project"
        project_packet = project_fixture.packet(project_name, scope="project")
        project_outcome = project_fixture.run(script, project_name, project_packet)
        report["positive"]["project"] = {
            "outcome": project_outcome,
            "verified": _assert_ready_contract(
                project_fixture,
                project_packet,
                project_outcome,
                "positive-project",
            ),
        }

        invalid_values = (
            ("true", True),
            ("false", False),
            ("float", 900.0),
            ("fraction", 900.5),
            ("string", "900"),
            ("zero", 0),
            ("negative", -1),
            ("excessive", 1801),
        )
        for label, value in invalid_values:
            fixture = Fixture(temporary, f"negative-{label}")
            name = f"audio-runtime-c29-invalid-{label}"
            packet = fixture.packet(name, time_seconds=value)
            outcome = fixture.run(script, name, packet)
            _assert_failure(outcome, label)
            report["negative"][label] = outcome

        fixture = Fixture(temporary, "negative-missing-budget")
        name = "audio-runtime-c29-invalid-missing-budget"
        packet = fixture.packet(name)
        del packet["budget"]["timeSeconds"]
        outcome = fixture.run(script, name, packet)
        _assert_failure(outcome, "missing-budget")
        report["negative"]["missing-budget"] = outcome

        fixture = Fixture(temporary, "negative-absent-budget")
        name = "audio-runtime-c29-invalid-absent-budget"
        packet = fixture.packet(name)
        del packet["budget"]
        outcome = fixture.run(script, name, packet)
        _assert_failure(outcome, "absent-budget")
        report["negative"]["absent-budget"] = outcome

        fixture = Fixture(temporary, "negative-malformed-budget")
        name = "audio-runtime-c29-invalid-malformed-budget"
        packet = fixture.packet(name)
        packet["budget"] = []
        outcome = fixture.run(script, name, packet)
        _assert_failure(outcome, "malformed-budget")
        report["negative"]["malformed-budget"] = outcome

        fixture = Fixture(temporary, "negative-description")
        name = "audio-runtime-c29-invalid-description"
        packet = fixture.packet(name)
        packet["mission"] = None
        outcome = fixture.run(script, name, packet)
        _assert_failure(outcome, "description")
        report["negative"]["description"] = outcome

        mutations = {
            "wrong-project": lambda fixture, packet: packet.update(project="other-project"),
            "missing-admission": lambda fixture, packet: fixture.admission.Release(
                PROJECT,
                {"outcome": "public-control"},
            ),
            "stale-authority": lambda fixture, packet: fixture.root.joinpath(
                "factory/projects/audio-runtime/source-plan.md"
            ).write_text("stale\n", encoding="utf-8"),
            "invalid-artifact-digest": lambda fixture, packet: packet["build"].update(
                sha256="0" * 64
            ),
            "excessive-realtime-sessions": lambda fixture, packet: packet["budget"].update(
                realtimeSessions=4
            ),
            "excessive-realtime-seconds": lambda fixture, packet: packet["budget"].update(
                realtimeSeconds=121
            ),
            "invalid-realtime-type": lambda fixture, packet: packet["budget"].update(
                realtimeSeconds=1.0
            ),
            "invalid-realtime-bool": lambda fixture, packet: packet["budget"].update(
                realtimeSessions=True
            ),
            "altered-criterion": lambda fixture, packet: packet["criteria"][0].update(
                rubric="altered"
            ),
            "duplicate-criterion": lambda fixture, packet: packet["criteria"].append(
                dict(packet["criteria"][0])
            ),
            "project-scope-missing-criteria": lambda fixture, packet: (
                packet.update(scope="project"),
                packet["criteria"].__setitem__(slice(None), packet["criteria"][:1]),
            ),
            "invalid-scope": lambda fixture, packet: packet.update(scope="other"),
            "missing-vertical": lambda fixture, packet: packet.pop("vertical"),
            "missing-source": lambda fixture, packet: packet.pop("sourceRevision"),
            "outside-report": lambda fixture, packet: packet.update(
                reportPath=str(fixture.root / "docs/temp/projects/outside.json")
            ),
            "non-json-report": lambda fixture, packet: packet.update(
                reportPath=str(
                    fixture.root
                    / "docs/temp/projects/audio-runtime/not-json.txt"
                )
            ),
        }
        for label, mutate in mutations.items():
            fixture = Fixture(temporary, "negative-control-" + label)
            name = "audio-runtime-c29-control-" + label
            packet = fixture.packet(name)
            before_build = fixture.build.read_bytes()
            before_fixture = fixture.fixture.read_bytes()
            mutate(fixture, packet)
            outcome = fixture.run(script, name, packet)
            _assert_failure(outcome, label)
            if fixture.build.read_bytes() != before_build or fixture.fixture.read_bytes() != before_fixture:
                raise AssertionError(f"{label}: source artifact changed")
            report["negative"][label] = outcome

        fixture = Fixture(temporary, "negative-existing-report")
        name = "audio-runtime-c29-control-existing-report"
        packet = fixture.packet(name)
        report_path = Path(packet["reportPath"])
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text('{"preserve":true}\n', encoding="utf-8")
        before_report = report_path.read_bytes()
        outcome = fixture.run(script, name, packet)
        _assert_failure(outcome, "existing-report")
        if report_path.read_bytes() != before_report:
            raise AssertionError("existing-report: report was changed")
        report["negative"]["existing-report"] = outcome

        report["mutationControls"] = _run_mutation_controls(script, temporary)
    return report


def _mutated_script(script, directory, label, replacements):
    path = Path(directory) / f"prepare-validation-{label}.py"
    source = script.read_text(encoding="utf-8")
    for old, new in replacements:
        if source.count(old) != 1:
            raise AssertionError(f"{label}: mutation marker is not unique: {old!r}")
        source = source.replace(old, new, 1)
    path.write_text(source, encoding="utf-8")
    path.chmod(script.stat().st_mode)
    return path


def _run_mutation_controls(script, parent):
    controls = {}
    with tempfile.TemporaryDirectory(prefix="c29-mutations-", dir=parent) as temporary:
        equality = _mutated_script(
            script,
            temporary,
            "equality",
            [("not 1 <= time_seconds <= 1800", "time_seconds != 1800")],
        )
        fixture = Fixture(temporary, "equality-fixture")
        name = "audio-runtime-c29-mutation-equality"
        outcome = fixture.run(script=equality, name=name, packet=fixture.packet(name))
        if outcome["returncode"] == 0:
            raise AssertionError("equality-to-1800 mutation survived the 900 control")
        controls["equalityTo1800"] = {
            "status": "killed",
            "outcome": outcome,
        }

        coercion = _mutated_script(
            script,
            temporary,
            "coercion",
            [("not isinstance(time_seconds, int)", "not isinstance(time_seconds, (int, float))")],
        )
        fixture = Fixture(temporary, "coercion-fixture")
        name = "audio-runtime-c29-mutation-coercion"
        outcome = fixture.run(
            script=coercion,
            name=name,
            packet=fixture.packet(name, time_seconds=900.5),
        )
        if outcome["returncode"] == 0:
            controls["coercion"] = {"status": "killed", "outcome": outcome}
        else:
            raise AssertionError("fractional-budget coercion mutation was not detected")
    return controls


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--script", required=True, type=Path)
    parser.add_argument("--expect", choices=("legacy", "bounded"), required=True)
    parser.add_argument("--child-timeout-seconds", type=float, default=10)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args(argv)
    if args.child_timeout_seconds > CHILD_TIMEOUT_SECONDS:
        raise SystemExit("child-timeout-seconds cannot exceed 10")
    if args.child_timeout_seconds <= 0:
        raise SystemExit("child-timeout-seconds must be positive")
    script = args.script.resolve()
    if not script.is_file():
        raise SystemExit(f"script does not exist: {script}")
    report = {
        "status": "passed",
        "expectation": args.expect,
        "sourceRevision": _source_revision(),
        "script": {"path": str(script), "sha256": _sha256(script)},
        "manifest": {
            "path": str(REPO_ROOT / "factory/projects/audio-runtime/manifest.json"),
            "sha256": _sha256(REPO_ROOT / "factory/projects/audio-runtime/manifest.json"),
        },
        "childTimeoutSeconds": CHILD_TIMEOUT_SECONDS,
        "isolatedAdmission": {
            "project": PROJECT,
            "contractRevision": CONTRACT_REVISION,
            "productionAdmissionMutated": False,
        },
    }
    if args.expect == "legacy":
        report["legacy"] = _run_legacy(script)
    else:
        report["bounded"] = _run_bounded(script)
    report_path = args.report.resolve() if args.report else Path(__file__).with_name(
        "public-controls-report.json"
    )
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "passed", "report": str(report_path), "expect": args.expect}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
