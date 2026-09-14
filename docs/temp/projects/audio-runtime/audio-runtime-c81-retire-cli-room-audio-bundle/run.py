#!/usr/bin/env python3
"""Run bounded C81 public roomaudio and shipped CLI evidence."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from typing import Any


EVIDENCE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["rtk", "proxy", "git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, check=True, capture_output=True, text=True).stdout.strip()).resolve()
CONSUMER = EVIDENCE / "external-consumer"
ARTIFACTS = EVIDENCE / "artifacts"
REPORTS = EVIDENCE / "reports"
RUNS = EVIDENCE / "runs"
PROBE = ARTIFACTS / "roomaudio-probe"
YUI = ARTIFACTS / "yui"
NON_ROOM_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json"
BRANCH = "codex/audio-runtime-c81-retire-cli-room-audio-bundle"
BASELINE = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
MAX_OUTPUT_BYTES = 64 * 1024
MAX_DISK_BYTES = 8 * 1024 * 1024
TERM_GRACE_SECONDS = 2.0


class EvidenceFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git(*args: str) -> str:
    result = subprocess.run(["rtk", "proxy", "git", *args], cwd=ROOT, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def clean_environment(gowork: str) -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = gowork
    environment["LC_ALL"] = "C"
    environment["LANG"] = "C"
    return environment


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


class CappedOutput:
    def __init__(self) -> None:
        self.data = bytearray()
        self.total_bytes = 0
        self.overflow = False
        self.read_error = ""
        self.digest = hashlib.sha256()

    def append(self, chunk: bytes) -> None:
        self.total_bytes += len(chunk)
        self.digest.update(chunk)
        remaining = MAX_OUTPUT_BYTES - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total_bytes > MAX_OUTPUT_BYTES:
            self.overflow = True

    def text(self) -> str:
        value = bytes(self.data).decode(errors="replace")
        if self.overflow:
            value += f"\n[output truncated after {MAX_OUTPUT_BYTES} bytes; read {self.total_bytes} bytes]\n"
        if self.read_error:
            value += f"\n[output reader error: {self.read_error}]\n"
        return value


def drain(stream: Any, output: CappedOutput) -> None:
    try:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            output.append(chunk)
    except (OSError, ValueError) as error:
        output.read_error = str(error)


def tree_bytes(root: Path) -> int:
    if not root.exists():
        return 0
    total = 0
    for path in root.rglob("*"):
        if path.is_file() and not path.is_symlink():
            try:
                total += path.stat().st_size
            except OSError:
                pass
    return total


def stop_group(process: subprocess.Popen[bytes]) -> list[str]:
    signals: list[str] = []
    if group_alive(process.pid):
        try:
            os.killpg(process.pid, signal.SIGTERM)
            signals.append("SIGTERM")
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=TERM_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        if group_alive(process.pid):
            try:
                os.killpg(process.pid, signal.SIGKILL)
                signals.append("SIGKILL")
            except ProcessLookupError:
                pass
    try:
        process.wait(timeout=TERM_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        pass
    return signals


class Budget:
    def __init__(self, aggregate_seconds: float, child_seconds: float) -> None:
        self.started = time.monotonic()
        self.aggregate_seconds = aggregate_seconds
        self.child_seconds = child_seconds

    def remaining(self) -> float:
        return self.aggregate_seconds - (time.monotonic() - self.started)

    def child_deadline(self) -> float:
        return min(self.child_seconds, self.remaining())


def run_child(
    label: str,
    argv: list[str | Path],
    cwd: Path,
    run_root: Path,
    budget: Budget,
    gowork: str,
    expected: int | None = 0,
) -> dict[str, Any]:
    timeout = budget.child_deadline()
    require(0 < timeout <= MAX_CHILD_SECONDS, f"{label} has no time left inside the bounded budget")
    command = [str(item) for item in argv]
    run_root.mkdir(parents=True, exist_ok=True)
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=clean_environment(gowork),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    stdout = CappedOutput()
    stderr = CappedOutput()
    readers = [
        threading.Thread(target=drain, args=(process.stdout, stdout), daemon=True, name=f"{label}-stdout"),
        threading.Thread(target=drain, args=(process.stderr, stderr), daemon=True, name=f"{label}-stderr"),
    ]
    for reader in readers:
        reader.start()
    timed_out = False
    disk_overflow = False
    output_overflow = False
    survivor_detected = False
    termination_signals: list[str] = []
    deadline = time.monotonic() + timeout
    try:
        while process.poll() is None:
            if stdout.overflow or stderr.overflow:
                output_overflow = True
                termination_signals.extend(stop_group(process))
                break
            if tree_bytes(run_root) > MAX_DISK_BYTES:
                disk_overflow = True
                termination_signals.extend(stop_group(process))
                break
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                termination_signals.extend(stop_group(process))
                break
            try:
                process.wait(timeout=min(0.1, remaining))
            except subprocess.TimeoutExpired:
                pass
        if not timed_out and not output_overflow and not disk_overflow and group_alive(process.pid):
            survivor_detected = True
            termination_signals.extend(stop_group(process))
    finally:
        if process.poll() is None:
            termination_signals.extend(stop_group(process))
        for reader in readers:
            reader.join(timeout=TERM_GRACE_SECONDS)
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
    elapsed_ms = int((time.monotonic() - started) * 1000)
    stdout_path = run_root / "stdout.log"
    stderr_path = run_root / "stderr.log"
    stdout_path.write_bytes(bytes(stdout.data))
    stderr_path.write_bytes(bytes(stderr.data))
    disk_bytes = tree_bytes(run_root)
    result: dict[str, Any] = {
        "label": label,
        "argv": command,
        "cwd": str(cwd.relative_to(ROOT)) if cwd.is_relative_to(ROOT) else str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "stdout_sha256": stdout.digest.hexdigest(),
        "stderr_sha256": stderr.digest.hexdigest(),
        "stdout_bytes": stdout.total_bytes,
        "stderr_bytes": stderr.total_bytes,
        "output_bounded": not stdout.overflow and not stderr.overflow and not stdout.read_error and not stderr.read_error,
        "disk_bytes": disk_bytes,
        "disk_bounded": not disk_overflow and disk_bytes <= MAX_DISK_BYTES,
        "stdout_path": str(stdout_path.relative_to(EVIDENCE)),
        "stderr_path": str(stderr_path.relative_to(EVIDENCE)),
        "cleanup": {
            "parent_reaped": process.returncode is not None,
            "group_alive_after": group_alive(process.pid),
            "group_survivor_detected": survivor_detected,
            "reader_threads_stopped": not any(reader.is_alive() for reader in readers),
            "termination_signals": termination_signals,
        },
    }
    if expected is not None:
        require(result["returncode"] == expected and not timed_out and result["output_bounded"] and result["disk_bounded"], f"{label} returned {result['returncode']} (expected {expected})")
        require(result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"] and not result["cleanup"]["group_survivor_detected"] and result["cleanup"]["reader_threads_stopped"], f"{label} left a process or reader survivor")
    return result


def write_report(name: str, value: dict[str, Any]) -> None:
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def parse_last_json(execution: dict[str, Any], label: str) -> dict[str, Any]:
    try:
        value = json.loads(execution["stdout"].strip())
        if isinstance(value, dict):
            return value
    except json.JSONDecodeError:
        pass
    lines = [line for line in execution["stdout"].splitlines() if line.strip()]
    require(lines, f"{label} emitted no JSON")
    try:
        value = json.loads(lines[-1])
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"{label} emitted invalid JSON: {error}") from error
    require(isinstance(value, dict), f"{label} JSON is not an object")
    return value


def build_inputs() -> dict[str, Any]:
    paths = set(git("ls-files", "--", "agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "go.work", "go.work.sum").splitlines())
    paths.update(
        str(path.relative_to(ROOT))
        for path in (CONSUMER / "go.mod", CONSUMER / "go.sum", CONSUMER / "cmd/roomaudio-probe/main.go")
    )
    entries = []
    for relative in sorted(path for path in paths if path):
        path = ROOT / relative
        require(path.is_file() and not path.is_symlink(), f"build input is unavailable: {relative}")
        entries.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode()
    manifest = {
        "schema": "audio-runtime.c81.build-inputs.v1",
        "candidate_revision": git("rev-parse", "HEAD"),
        "entries": entries,
        "sha256": hashlib.sha256(encoded).hexdigest(),
    }
    write_report("build-input-manifest.json", manifest)
    return manifest


def artifact_info(path: Path, inputs: dict[str, Any]) -> dict[str, Any]:
    require(path.is_file() and not path.is_symlink(), f"build artifact is missing: {path}")
    return {
        "path": str(path.relative_to(EVIDENCE)),
        "bytes": path.stat().st_size,
        "sha256": sha256_file(path),
        "build_input_manifest_sha256": inputs["sha256"],
        "build_input_count": len(inputs["entries"]),
    }


def build_probe(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    inputs = build_inputs()
    execution = run_child(
        "build-roomaudio-probe",
        ["rtk", "proxy", "go", "build", "-mod=readonly", "-trimpath", "-o", PROBE, "./cmd/roomaudio-probe"],
        CONSUMER,
        run_root,
        budget,
        "off",
    )
    return {"execution": execution, "artifact": artifact_info(PROBE, inputs)}


def build_yui(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    inputs = build_inputs()
    execution = run_child(
        "build-yui",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", YUI, "./cmd/yui"],
        ROOT / "agent-cli",
        run_root,
        budget,
        str(ROOT / "go.work"),
    )
    return {"execution": execution, "artifact": artifact_info(YUI, inputs)}


def save_provenance(builds: dict[str, Any], run_root: Path) -> None:
    REPORTS.mkdir(parents=True, exist_ok=True)
    value = {
        "schema": "audio-runtime.c81.provenance.v1",
        "project": "audio-runtime",
        "task": "audio-runtime-c81-retire-cli-room-audio-bundle",
        "contract_revision": "audio-runtime-v1",
        "branch": git("branch", "--show-current"),
        "candidate_revision": git("rev-parse", "HEAD"),
        "baseline_revision": BASELINE,
        "startup_integration_revision": STARTUP_INTEGRATION,
        "accepted_main_revision": BASELINE,
        "origin_main_at_run": git("rev-parse", "origin/main"),
        "builds": builds,
        "run_root": str(run_root.relative_to(EVIDENCE)),
        "credential_environment": "API keys, secrets, passwords, and tokens removed from child environments",
        "physical_device": "not_attempted; software replay only",
        "acoustic": "not_attempted; software replay is not acoustic proof",
    }
    (EVIDENCE / "provenance.json").write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def room_audio_bundle(budget: Budget, run_root: Path) -> None:
    probe_build = build_probe(budget, run_root / "build-probe")
    positive = run_child("public-roomaudio-positive", [PROBE, "positive"], CONSUMER, run_root / "public-positive", budget, "off")
    positive_report = parse_last_json(positive, "public roomaudio positive")
    require(positive_report.get("participants") == ["alpha", "beta"], "public roomaudio participant order changed")
    require(positive_report.get("stream_order") == ["alpha:output", "alpha:sent", "alpha:received", "beta:output", "beta:sent", "beta:received", "room:mix"], "public roomaudio stream order changed")
    require(positive_report.get("alpha_wav_samples") == [100, 200, 300, 400] and positive_report.get("room_mix_samples") == [900, 1000, 1100, 1200], "public roomaudio samples changed")
    require(positive_report.get("alpha_delta_ids") == ["alpha-delta-0", "alpha-delta-1"], "public roomaudio delta ordering changed")
    require(positive_report.get("overlap_count") == 1 and positive_report.get("barge_in_count") == 1 and positive_report.get("loudness_count") == 1, "public roomaudio annotations are incomplete")
    require(positive_report.get("immutable_views") is True and positive_report.get("clean_shutdown") is True, "public roomaudio immutability/shutdown proof is incomplete")

    yui_build = build_yui(budget, run_root / "build-yui")
    example = run_child("shipped-yui-room-example", [YUI, "room", "run", "--example"], ROOT / "agent-cli", run_root / "yui-example", budget, str(ROOT / "go.work"))
    example_report = parse_last_json(example, "shipped yui room example")
    require(example_report.get("schema_version") == 1 and len(example_report.get("participants", [])) >= 2, "candidate yui did not expose a valid room entrypoint")
    report = {
        "schema": "audio-runtime.c81.room-audio-bundle.v1",
        "candidate_revision": git("rev-parse", "HEAD"),
        "workflow": "public roomaudio/wire plus candidate-built yui room entrypoint",
        "public_bundle": positive_report,
        "yui_example": example_report,
        "build_probe": probe_build,
        "build_yui": yui_build,
        "executions": [positive, example],
        "credentials": "not_used",
        "physical_device": "not_attempted",
        "acoustic": "not_attempted",
    }
    write_report("room-audio-bundle.json", report)
    save_provenance({"probe": probe_build, "yui": yui_build}, run_root)


def corrupted_bundle(budget: Budget, run_root: Path) -> None:
    probe_build = build_probe(budget, run_root / "build-probe")
    execution = run_child("public-roomaudio-corrupted", [PROBE, "corrupted"], CONSUMER, run_root / "corrupted", budget, "off")
    report = parse_last_json(execution, "public roomaudio corrupted")
    require(report.get("rejected") is True and report.get("typed_reconstruction") is True, "corrupted public roomaudio bundle was accepted")
    require(report.get("participant") == "alpha" and report.get("stream") == "alpha:output" and report.get("first_divergent_byte") == 0, "corruption report lost first-divergence identity")
    require(report.get("clean_shutdown") is True, "corrupted public roomaudio process did not close cleanly")
    write_report("corrupted-bundle.json", {
        "schema": "audio-runtime.c81.corrupted-bundle.v1",
        "candidate_revision": git("rev-parse", "HEAD"),
        "probe_build": probe_build,
        "report": report,
        "execution": execution,
        "credentials": "not_used",
    })
    save_provenance({"probe": probe_build}, run_root)


def non_room_audio_tool(budget: Budget, run_root: Path) -> None:
    require(NON_ROOM_FIXTURE.is_file(), f"missing accumulated non-room fixture: {NON_ROOM_FIXTURE}")
    yui_build = build_yui(budget, run_root / "build-yui")
    case = run_root / "non-room-audio-tool"
    case.mkdir(parents=True, exist_ok=True)
    (case / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record = case / "record"
    execution = run_child(
        "shipped-yui-non-room-audio-tool",
        [
            YUI,
            "--config-dir", case / "config",
            "--log-to-stdout",
            "session", "--replay", NON_ROOM_FIXTURE,
            "--replay-timing", "immediate",
            "--record-dir", record,
            "--trace-audio",
            "--workdir", case,
            "--allow-path", case,
        ],
        case,
        case / "process",
        budget,
        str(ROOT / "go.work"),
    )
    output = execution["stdout"] + execution["stderr"]
    require("PROBE_TOOL_MARKER_9182" in output, "existing non-room audio/tool replay lost its tool effect")
    require((record / "manifest.json").is_file() and (record / "session-log.jsonl").is_file(), "existing non-room replay did not retain terminal recording artifacts")
    write_report("non-room-audio-tool.json", {
        "schema": "audio-runtime.c81.non-room-audio-tool.v1",
        "candidate_revision": git("rev-parse", "HEAD"),
        "fixture": {"path": str(NON_ROOM_FIXTURE.relative_to(ROOT)), "bytes": NON_ROOM_FIXTURE.stat().st_size, "sha256": sha256_file(NON_ROOM_FIXTURE)},
        "build_yui": yui_build,
        "execution": execution,
        "record_manifest_sha256": sha256_file(record / "manifest.json"),
        "session_log_sha256": sha256_file(record / "session-log.jsonl"),
        "credentials": "not_used",
    })
    save_provenance({"yui": yui_build}, run_root)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("room-audio-bundle", "corrupted-bundle", "non-room-audio-tool"))
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    require(0 < args.child_timeout <= MAX_CHILD_SECONDS, "child timeout must be at most 60 seconds")
    require(0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS, "aggregate timeout must be at most 600 seconds")
    RUNS.mkdir(parents=True, exist_ok=True)
    run_root = RUNS / f"{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{args.case}-{os.getpid()}"
    budget = Budget(args.aggregate_timeout, args.child_timeout)
    try:
        if args.case == "room-audio-bundle":
            room_audio_bundle(budget, run_root)
        elif args.case == "corrupted-bundle":
            corrupted_bundle(budget, run_root)
        else:
            non_room_audio_tool(budget, run_root)
        require(budget.remaining() >= 0, "aggregate evidence deadline expired")
        outcome = {"schema": "audio-runtime.c81.runner.v1", "passed": True, "case": args.case, "candidate_revision": git("rev-parse", "HEAD"), "run_root": str(run_root.relative_to(EVIDENCE)), "aggregate_elapsed_ms": int((time.monotonic() - budget.started) * 1000)}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(outcome, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(outcome, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as error:
        outcome = {"schema": "audio-runtime.c81.runner.v1", "passed": False, "case": args.case, "candidate_revision": git("rev-parse", "HEAD"), "error": str(error), "run_root": str(run_root.relative_to(EVIDENCE))}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(outcome, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(outcome, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
