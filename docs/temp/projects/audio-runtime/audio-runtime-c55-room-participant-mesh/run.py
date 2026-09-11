#!/usr/bin/env python3
"""Bounded public-mesh, oracle-mutation, and shipped-YUI evidence runner."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import threading
import time
from typing import Any

EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["rtk", "proxy", "git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, check=True, capture_output=True, text=True).stdout.strip()).resolve()
CONSUMER_ROOT = EVIDENCE / "consumer"
CONSUMER_SOURCE = CONSUMER_ROOT / "main.go"
ARTIFACTS = EVIDENCE / "artifacts"
REPORTS = EVIDENCE / "reports"
RUNS = EVIDENCE / "runs"
CONSUMER_BINARY = ARTIFACTS / "mesh-consumer"
YUI_BINARY = ARTIFACTS / "yui"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
FETCHED_MAIN_REVISION = "904e1f4c3be6c1e629138632573bd2fb55d50938"
BRANCH = "codex/audio-runtime-c55-room-participant-mesh"
MAX_OUTPUT_BYTES = 64 * 1024
MAX_ARTIFACT_BYTES = 8 * 1024 * 1024
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
TERM_GRACE_SECONDS = 2.0
KILL_GRACE_SECONDS = 2.0
BUILD_INPUT_GROUPS = ("agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway")
FIXTURES = {
    "audio-tool": REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-audio-tool.session.json",
    "interruption": REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures/c16-interruption.session.json",
}


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
    result = subprocess.run(["rtk", "proxy", "git", *args], cwd=REPO_ROOT, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def clean_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = "off"
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
    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.data = bytearray()
        self.total_bytes = 0
        self.overflow = False
        self.read_error = ""
        self.digest = hashlib.sha256()
        self.done = threading.Event()

    def append(self, chunk: bytes) -> None:
        self.total_bytes += len(chunk)
        self.digest.update(chunk)
        remaining = self.limit - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total_bytes > self.limit:
            self.overflow = True

    def text(self) -> str:
        value = bytes(self.data).decode(errors="replace")
        if self.overflow:
            value += f"\n[output truncated after {self.limit} bytes; read {self.total_bytes} bytes]\n"
        if self.read_error:
            value += f"\n[output reader error: {self.read_error}]\n"
        return value


def read_stream(stream: Any, output: CappedOutput) -> None:
    try:
        while True:
            chunk = stream.read(8192)
            if not chunk:
                return
            output.append(chunk)
    except (OSError, ValueError) as error:
        output.read_error = str(error)
    finally:
        output.done.set()


def tree_bytes(root: Path) -> int:
    if not root.exists():
        return 0
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file() and not path.is_symlink())


def stop_group(process: subprocess.Popen[bytes]) -> list[str]:
    signals: list[str] = []
    def send(name: str, sig: signal.Signals) -> None:
        try:
            os.killpg(process.pid, sig)
            signals.append(name)
        except ProcessLookupError:
            return

    if group_alive(process.pid):
        send("SIGTERM", signal.SIGTERM)
    try:
        process.wait(timeout=TERM_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        send("SIGKILL", signal.SIGKILL)
    if group_alive(process.pid):
        send("SIGKILL", signal.SIGKILL)
    try:
        process.wait(timeout=KILL_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        pass
    return signals


def run_child(label: str, argv: list[str | Path], cwd: Path, run_root: Path, budget: "Budget", expect: int | None = 0) -> dict[str, Any]:
    timeout = min(budget.child_timeout(), budget.remaining())
    require(timeout > 0, f"aggregate deadline expired before {label}")
    command = [str(item) for item in argv]
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path = run_root / f"{label}.stdout.log"
    stderr_path = run_root / f"{label}.stderr.log"
    started = time.monotonic()
    process = subprocess.Popen(command, cwd=cwd, env=clean_environment(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    stdout_capture = CappedOutput(MAX_OUTPUT_BYTES)
    stderr_capture = CappedOutput(MAX_OUTPUT_BYTES)
    readers = [
        threading.Thread(target=read_stream, args=(process.stdout, stdout_capture), daemon=True, name=f"{label}-stdout"),
        threading.Thread(target=read_stream, args=(process.stderr, stderr_capture), daemon=True, name=f"{label}-stderr"),
    ]
    for reader in readers:
        reader.start()
    timed_out = False
    output_overflow = False
    disk_overflow = False
    group_survivor_detected = False
    termination_signals: list[str] = []
    try:
        deadline = time.monotonic() + timeout
        while process.poll() is None:
            if stdout_capture.overflow or stderr_capture.overflow:
                output_overflow = True
                termination_signals = stop_group(process)
                break
            if tree_bytes(run_root) > MAX_ARTIFACT_BYTES:
                disk_overflow = True
                termination_signals = stop_group(process)
                break
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                termination_signals = stop_group(process)
                break
            try:
                process.wait(timeout=min(0.1, remaining))
            except subprocess.TimeoutExpired:
                continue
        if not timed_out and not output_overflow and not disk_overflow and group_alive(process.pid):
            group_survivor_detected = True
            termination_signals = stop_group(process)
    finally:
        if process.poll() is None:
            termination_signals.extend(stop_group(process))
        for reader in readers:
            reader.join(timeout=KILL_GRACE_SECONDS if timed_out or output_overflow or disk_overflow else TERM_GRACE_SECONDS)
        if any(reader.is_alive() for reader in readers):
            for stream in (process.stdout, process.stderr):
                if stream is not None:
                    stream.close()
            for reader in readers:
                reader.join(timeout=0.25)
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
    elapsed_ms = int((time.monotonic() - started) * 1000)
    stdout_path.write_bytes(bytes(stdout_capture.data))
    stderr_path.write_bytes(bytes(stderr_capture.data))
    disk_bytes = tree_bytes(run_root)
    result = {
        "label": label,
        "argv": command,
        "cwd": str(cwd.relative_to(REPO_ROOT)) if cwd.is_relative_to(REPO_ROOT) else str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": stdout_capture.text(),
        "stderr": stderr_capture.text(),
        "stdout_sha256": stdout_capture.digest.hexdigest(),
        "stderr_sha256": stderr_capture.digest.hexdigest(),
        "stdout_bytes": stdout_capture.total_bytes,
        "stderr_bytes": stderr_capture.total_bytes,
        "stdout_truncated": stdout_capture.overflow,
        "stderr_truncated": stderr_capture.overflow,
        "output_bounded": not stdout_capture.overflow and not stderr_capture.overflow and not stdout_capture.read_error and not stderr_capture.read_error,
        "disk_bytes": disk_bytes,
        "disk_bounded": not disk_overflow and disk_bytes <= MAX_ARTIFACT_BYTES,
        "stdout_path": str(stdout_path.relative_to(EVIDENCE)),
        "stderr_path": str(stderr_path.relative_to(EVIDENCE)),
        "cleanup": {"parent_reaped": process.returncode is not None, "group_alive_after": group_alive(process.pid), "group_survivor_detected": group_survivor_detected, "termination_signals": termination_signals, "reader_threads_stopped": not any(reader.is_alive() for reader in readers)},
    }
    if expect is not None:
        require(result["returncode"] == expect and not timed_out and result["output_bounded"] and result["disk_bounded"] and result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"] and not result["cleanup"]["group_survivor_detected"], f"{label} returned {result['returncode']} (expected {expect})")
    return result


class Budget:
    def __init__(self, aggregate_timeout: float, child_timeout: float) -> None:
        self.started = time.monotonic()
        self.aggregate_timeout = aggregate_timeout
        self.child_timeout_limit = child_timeout

    def remaining(self) -> float:
        return self.aggregate_timeout - (time.monotonic() - self.started)

    def child_timeout(self) -> float:
        return min(self.child_timeout_limit, self.remaining())


def parse_json_output(result: dict[str, Any], label: str) -> dict[str, Any]:
    lines = [line for line in result["stdout"].splitlines() if line.strip()]
    require(lines, f"{label} emitted no JSON report")
    try:
        value = json.loads(lines[-1])
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"{label} emitted invalid JSON: {error}") from error
    require(isinstance(value, dict), f"{label} JSON report is not an object")
    return value


def build_input_manifest() -> dict[str, Any]:
    paths = set(git("ls-files", "--", *BUILD_INPUT_GROUPS, "go.work", "go.work.sum").splitlines())
    paths.update(
        f"docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/{name}"
        for name in ("consumer/main.go", "go.mod", "go.sum")
    )
    entries = []
    for relative in sorted(path for path in paths if path):
        path = REPO_ROOT / relative
        require(path.is_file() and not path.is_symlink(), f"build input is unavailable: {relative}")
        entries.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode()
    manifest = {"schema": "audio-runtime.c55.build-inputs.v1", "candidate_revision": git("rev-parse", "HEAD"), "entries": entries, "sha256": hashlib.sha256(encoded).hexdigest()}
    save_report("build-input-manifest.json", manifest)
    return manifest


def artifact_info(path: Path, inputs: dict[str, Any]) -> dict[str, Any]:
    require(path.is_file() and not path.is_symlink(), f"build artifact is unavailable: {path}")
    return {"path": str(path.relative_to(EVIDENCE)), "bytes": path.stat().st_size, "sha256": sha256_file(path), "build_input_manifest_sha256": inputs["sha256"], "build_input_count": len(inputs["entries"])}


def ensure_consumer(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    inputs = build_input_manifest()
    result = run_child("build-consumer", ["rtk", "proxy", "go", "build", "-mod=readonly", "-trimpath", "-o", CONSUMER_BINARY, "./consumer"], EVIDENCE, run_root, budget)
    result["artifact"] = artifact_info(CONSUMER_BINARY, inputs)
    return result


def ensure_yui(budget: Budget, run_root: Path) -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    inputs = build_input_manifest()
    result = run_child("build-yui", ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", YUI_BINARY, "./cmd/yui"], REPO_ROOT / "agent-cli", run_root, budget)
    result["artifact"] = artifact_info(YUI_BINARY, inputs)
    return result


def storage_snapshot(run_root: Path) -> dict[str, Any]:
    return {
        "free_space_bytes": shutil.disk_usage(EVIDENCE).free,
        "staging_bytes": tree_bytes(CONSUMER_ROOT),
        "binary_output_bytes": tree_bytes(ARTIFACTS),
        "report_fixture_bytes": tree_bytes(REPORTS) + sum(path.stat().st_size for path in FIXTURES.values() if path.is_file()),
        "scratch_bytes": tree_bytes(run_root),
        "compilation_reserve_bytes": 2 * 1024 * 1024 * 1024,
    }


def candidate_provenance(builds: dict[str, Any] | None = None, storage: dict[str, Any] | None = None) -> dict[str, Any]:
    owned_sources = [
        "agent-cli/internal/room/mesh.go",
        "agent-cli/internal/room/mesh_test.go",
        "go-agent-runtime/services/rooms/mesh.go",
        "go-agent-runtime/services/rooms/internal/lifecycle/mesh.go",
        "go-agent-runtime/services/rooms/wire/mesh.go",
        "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/consumer/main.go",
        "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/go.mod",
        "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/go.sum",
        "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/run.py",
        "docs/temp/projects/audio-runtime/audio-runtime-c55-room-participant-mesh/verify.py",
    ]
    source_hashes = {path: sha256_file(REPO_ROOT / path) for path in owned_sources if (REPO_ROOT / path).is_file()}
    prior: dict[str, Any] = {}
    if (EVIDENCE / "provenance.json").is_file():
        try:
            prior = json.loads((EVIDENCE / "provenance.json").read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            prior = {}
    merged_builds = dict(prior.get("builds", {}))
    merged_builds.update(builds or {})
    build_manifest = REPORTS / "build-input-manifest.json"
    build_inputs = {}
    if build_manifest.is_file():
        value = json.loads(build_manifest.read_text(encoding="utf-8"))
        build_inputs = {"path": str(build_manifest.relative_to(EVIDENCE)), "sha256": sha256_file(build_manifest), "content_sha256": value.get("sha256"), "file_count": len(value.get("entries", []))}
    return {
        "schema": "audio-runtime.c55.provenance.v1",
        "project": "audio-runtime",
        "task": "audio-runtime-c55-room-participant-mesh",
        "contract_revision": "audio-runtime-v1",
        "branch": git("branch", "--show-current"),
        "candidate_revision": git("rev-parse", "HEAD"),
        "source_revision": FETCHED_MAIN_REVISION,
        "origin_main_at_run": git("rev-parse", "origin/main"),
        "prd_current_main_revision": SOURCE_REVISION,
        "fetched_main_revision": FETCHED_MAIN_REVISION,
        "baseline_revision": BASELINE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "source_hashes": source_hashes,
        "fixture_hashes": {name: sha256_file(path) for name, path in FIXTURES.items() if path.is_file()},
        "builds": merged_builds,
        "build_inputs": build_inputs,
        "storage": storage or prior.get("storage", {}),
        "credential_environment": "API keys, secrets, passwords, and tokens removed from child environments",
    }


def save_report(name: str, value: dict[str, Any]) -> None:
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def save_provenance(builds: dict[str, Any] | None = None, storage: dict[str, Any] | None = None) -> None:
    (EVIDENCE / "provenance.json").write_text(json.dumps(candidate_provenance(builds, storage), indent=2, sort_keys=True) + "\n", encoding="utf-8")


def source_inventory() -> dict[str, Any]:
    path = "agent-cli/internal/room/mesh.go"
    baseline = git("show", f"{FETCHED_MAIN_REVISION}:{path}")
    current = (REPO_ROOT / path).read_text(encoding="utf-8")
    retired = ("participants map", "pairs        map[", "pendingPair", "closing []*meshPair", "mutateMu")
    retained = ("type LoopbackPeerPair", "func NewLoopbackPairFactory", "func NewParticipantMesh", "func NewLoopbackMesh", "type loopbackConn")
    return {"schema": "audio-runtime.c55.source-inventory.v1", "baseline_revision": FETCHED_MAIN_REVISION, "path": path, "baseline_line_count": len(baseline.splitlines()), "final_line_count": len(current.splitlines()), "retired": {symbol: {"baseline_present": symbol in baseline, "final_present": symbol in current} for symbol in retired}, "retained": {symbol: {"baseline_present": symbol in baseline, "final_present": symbol in current} for symbol in retained}}


def external_positive(budget: Budget, run_root: Path) -> None:
    build = ensure_consumer(budget, run_root)
    positive = run_child("external-positive", [CONSUMER_BINARY, "positive"], CONSUMER_ROOT, run_root, budget)
    close_blocked = run_child("external-close-blocked", [CONSUMER_BINARY, "close-blocked"], CONSUMER_ROOT, run_root, budget)
    parent_blocked = run_child("external-parent-blocked", [CONSUMER_BINARY, "parent-blocked"], CONSUMER_ROOT, run_root, budget)
    report = parse_json_output(positive, "external-positive")
    require(report.get("pair_count") == 6 and report.get("participants") == ["alpha", "bravo", "charlie", "delta"], "external positive pair/participant effect changed")
    require(report.get("surviving_pair") is True and report.get("done_after_cleanup") is True, "external positive survivor/cleanup effect missing")
    require(report.get("rollback", {}).get("typed_connect_failure") is True and report.get("rollback", {}).get("no_half_join") is True, "causal rollback effect missing")
    for blocked in (close_blocked, parent_blocked):
        blocked_report = parse_json_output(blocked, blocked["label"])
        require(blocked_report.get("join_terminated") and blocked_report.get("done_after_cleanup"), f"{blocked['label']} did not converge")
    save_report("external-positive.json", {"schema": "audio-runtime.c55.external.v1", "candidate_revision": git("rev-parse", "HEAD"), "report": report, "build": build, "executions": [positive, close_blocked, parent_blocked]})
    save_report("source-inventory.json", source_inventory())
    save_provenance({"consumer": {"path": str(CONSUMER_BINARY.relative_to(EVIDENCE)), "sha256": sha256_file(CONSUMER_BINARY), "build": build}})


def mutated_oracles(budget: Budget, run_root: Path) -> None:
    if not CONSUMER_BINARY.is_file():
        ensure_consumer(budget, run_root)
    controls = []
    for mutation in ("mutate-count", "mutate-order", "mutate-cleanup", "mutate-half-join"):
        execution = run_child(f"negative-{mutation}", [CONSUMER_BINARY, mutation], CONSUMER_ROOT, run_root, budget, expect=None)
        require(execution["returncode"] != 0 and not execution["timed_out"], f"{mutation} unexpectedly passed")
        controls.append({"mutation": mutation, "execution": execution, "failure_artifact": execution["stderr"] or execution["stdout"]})
    save_report("negative-controls.json", {"schema": "audio-runtime.c55.negative.v1", "candidate_revision": git("rev-parse", "HEAD"), "passed": True, "controls": controls})
    save_provenance({"consumer": {"path": str(CONSUMER_BINARY.relative_to(EVIDENCE)), "sha256": sha256_file(CONSUMER_BINARY)}})


def shipped_room_mesh(budget: Budget, run_root: Path) -> None:
    build = ensure_yui(budget, run_root)
    case = run_root / "shipped-room"
    (case / "config").mkdir(parents=True, exist_ok=True)
    room = run_child("shipped-yui-room-example", [YUI_BINARY, "--config-dir", case / "config", "--workdir", case, "--allow-path", case, "room", "run", "--example"], case, case, budget)
    lines = [line for line in room["stdout"].splitlines() if line.strip()]
    try:
        example = json.loads("\n".join(lines))
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"shipped room example was not JSON: {error}") from error
    require(example.get("schema_version") == 1 and len(example.get("participants", [])) >= 2, "shipped YUI room path did not expose a valid participant manifest")
    public = run_child("shipped-room-public-mesh", [CONSUMER_BINARY, "positive"], CONSUMER_ROOT, run_root, budget)
    public_report = parse_json_output(public, "shipped-room-public-mesh")
    require(public_report.get("pair_count") == 6 and public_report.get("done_after_cleanup") is True, "shipped room mesh effect did not complete")
    save_report("shipped-yui-room-mesh.json", {"schema": "audio-runtime.c55.shipped-room.v1", "candidate_revision": git("rev-parse", "HEAD"), "command": "yui room run --example", "manifest": example, "build": build, "executions": [room, public]})
    save_provenance({"yui": {"path": str(YUI_BINARY.relative_to(EVIDENCE)), "sha256": sha256_file(YUI_BINARY), "build": build}, "consumer": {"sha256": sha256_file(CONSUMER_BINARY)}})


def replay_one(name: str, fixture: Path, budget: Budget, run_root: Path) -> dict[str, Any]:
    require(fixture.is_file(), f"missing frozen {name} fixture: {fixture}")
    case = run_root / f"replay-{name}"
    (case / "config").mkdir(parents=True, exist_ok=True)
    (case / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record = case / "record"
    execution = run_child(f"yui-{name}-replay", [YUI_BINARY, "--config-dir", case / "config", "--workdir", case, "--allow-path", case, "session", "--replay", fixture, "--replay-timing", "immediate", "--record-dir", record, "--trace-audio"], case, case, budget)
    require("replay mismatch" not in execution["stdout"].lower() + execution["stderr"].lower(), f"{name} replay reported a mismatch")
    manifest = record / "manifest.json"
    pcm = record / "audio" / "out-000.pcm"
    session_log = record / "session-log.jsonl"
    require(manifest.is_file() and pcm.is_file() and pcm.stat().st_size > 0 and session_log.is_file(), f"{name} replay did not retain complete recording artifacts")
    manifest_data = json.loads(manifest.read_text(encoding="utf-8"))
    if name == "audio-tool":
        marker = case / "evidence" / "runs" / "exec-invocations-v4.log"
        require("PROBE_TOOL_MARKER_9182" in execution["stdout"] and marker.is_file(), "audio-tool replay lost executable tool evidence")
        require(pcm.stat().st_size == 4800 and sha256_file(pcm) == "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502", "audio-tool replay PCM oracle changed")
    require(manifest_data.get("terminal", {}).get("terminal_provenance") in ("provider", "replay"), f"{name} replay terminal provenance missing")
    return {"fixture": str(fixture.relative_to(REPO_ROOT)), "fixture_sha256": sha256_file(fixture), "execution": execution, "manifest_sha256": sha256_file(manifest), "pcm_bytes": pcm.stat().st_size, "pcm_sha256": sha256_file(pcm), "session_log_sha256": sha256_file(session_log), "terminal": manifest_data["terminal"], "record_dir": str(record.relative_to(EVIDENCE))}


def shipped_audio_replay(budget: Budget, run_root: Path) -> None:
    build = ensure_yui(budget, run_root)
    results = [replay_one("audio-tool", FIXTURES["audio-tool"], budget, run_root)]
    save_report("shipped-yui-audio-tool-replay.json", {"schema": "audio-runtime.c55.shipped-replay.v1", "candidate_revision": git("rev-parse", "HEAD"), "build": build, "replays": results, "physical_device": "not_attempted", "acoustic": "not_attempted", "credentials": "not_used"})
    save_provenance({"yui": {"path": str(YUI_BINARY.relative_to(EVIDENCE)), "sha256": sha256_file(YUI_BINARY), "build": build}, "fixtures": {name: sha256_file(path) for name, path in FIXTURES.items()}})


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=("external-positive", "mutated-oracles", "shipped-yui-room-mesh", "shipped-yui-audio-tool-replay"))
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    require(0 < args.child_timeout <= MAX_CHILD_SECONDS, "child timeout must be at most 60 seconds")
    require(0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS, "aggregate timeout must be at most 600 seconds")
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_root = RUNS / f"{stamp}-{os.getpid()}"
    budget = Budget(args.aggregate_timeout, args.child_timeout)
    storage_before = storage_snapshot(run_root)
    try:
        if args.case == "external-positive":
            external_positive(budget, run_root)
        elif args.case == "mutated-oracles":
            mutated_oracles(budget, run_root)
        elif args.case == "shipped-yui-room-mesh":
            shipped_room_mesh(budget, run_root)
        else:
            shipped_audio_replay(budget, run_root)
        require(budget.remaining() >= 0, "aggregate evidence deadline expired")
        storage_after = storage_snapshot(run_root)
        save_provenance(storage={"before": storage_before, "after": storage_after})
        result = {"schema": "audio-runtime.c55.runner.v1", "passed": True, "case": args.case, "run_root": str(run_root.relative_to(EVIDENCE)), "aggregate_elapsed_ms": int((time.monotonic() - budget.started) * 1000), "storage": {"before": storage_before, "after": storage_after}}
        (run_root / "outcome.json").parent.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(result, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as error:
        storage_after = storage_snapshot(run_root)
        save_provenance(storage={"before": storage_before, "after": storage_after})
        result = {"schema": "audio-runtime.c55.runner.v1", "passed": False, "case": args.case, "error": str(error), "run_root": str(run_root.relative_to(EVIDENCE)), "storage": {"before": storage_before, "after": storage_after}}
        run_root.mkdir(parents=True, exist_ok=True)
        (run_root / "outcome.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(result, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
