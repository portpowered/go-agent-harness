#!/usr/bin/env python3
"""Bounded, credential-free C56 executable and runner-control evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time


HERE = Path(__file__).resolve().parent
REPO = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
RUN_ROOT = HERE / "runs"
BASELINE_REVISION = "904e1f4c3be6c1e629138632573bd2fb55d50938"
TASK = "audio-runtime-c56-retire-cli-recording-orchestration"
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_RETAINED_DISK_BYTES = 8 * 1024 * 1024
REPLAY_SOURCE = REPO / "docs/temp/projects/audio-runtime/audio-runtime-c50-remaining-cli-service-inventory/runs/public/replay/tool-record/provider.json"
REPLAY_SOURCE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
NON_RECORDING_FIXTURE = REPO / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_text_reply.session.json"


class RunnerError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RunnerError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def relative(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(HERE.resolve()))
    except ValueError:
        return str(path)


def go_mod_cache() -> str:
    return subprocess.run(["go", "env", "GOMODCACHE"], check=True, capture_output=True, text=True).stdout.strip()


def child_environment(run_root: Path) -> dict[str, str]:
    home = run_root / "home"
    temporary = run_root / "tmp"
    cache = run_root / "gocache"
    for path in (home, temporary, cache):
        path.mkdir(parents=True, exist_ok=True)
    return {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(home),
        "TMPDIR": str(temporary),
        "GOCACHE": str(cache),
        "GOMODCACHE": go_mod_cache(),
        "GOWORK": "off",
        "LANG": "C",
        "LC_ALL": "C",
        "C56_SOURCE_REVISION": BASELINE_REVISION,
    }


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def run_child(command: list[str], label: str, cwd: Path, run_root: Path, timeout: float) -> dict:
    require(0 < timeout <= 60.0, f"{label} timeout exceeds the 60 second child bound")
    process_root = run_root / "process"
    process_root.mkdir(parents=True, exist_ok=True)
    stdout_path = process_root / "stdout.log"
    stderr_path = process_root / "stderr.log"
    observed = {"stdout": 0, "stderr": 0}
    overflow = {"stdout": False, "stderr": False}

    def drain(stream, destination: Path, stream_name: str) -> None:
        try:
            with destination.open("wb") as output:
                while True:
                    chunk = stream.read(8192)
                    if not chunk:
                        return
                    previous = observed[stream_name]
                    observed[stream_name] += len(chunk)
                    retained = max(0, min(len(chunk), MAX_CHILD_OUTPUT_BYTES - previous))
                    if retained:
                        output.write(chunk[:retained])
                    if observed[stream_name] > MAX_CHILD_OUTPUT_BYTES:
                        overflow[stream_name] = True
        except (OSError, ValueError):
            overflow[stream_name] = True

    started = time.monotonic()
    process = None
    threads: list[threading.Thread] = []
    returncode: int | None = None
    timed_out = False
    term_sent = False
    kill_sent = False
    parent_reaped = False
    setup_error = ""
    group_before = False
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=child_environment(run_root),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        group_before = group_alive(process.pid)
        threads = [
            threading.Thread(target=drain, args=(process.stdout, stdout_path, "stdout"), daemon=True),
            threading.Thread(target=drain, args=(process.stderr, stderr_path, "stderr"), daemon=True),
        ]
        for thread in threads:
            thread.start()
        try:
            returncode = process.wait(timeout=timeout)
            parent_reaped = True
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
                term_sent = True
            except ProcessLookupError:
                pass
            try:
                returncode = process.wait(timeout=2)
                parent_reaped = True
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                    kill_sent = True
                except ProcessLookupError:
                    pass
                returncode = process.wait(timeout=2)
                parent_reaped = True
        if timed_out and group_alive(process.pid):
            try:
                os.killpg(process.pid, signal.SIGKILL)
                kill_sent = True
            except ProcessLookupError:
                pass
        for thread in threads:
            thread.join(timeout=2)
    except OSError as error:
        setup_error = str(error)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    group_after = group_alive(process.pid) if process is not None else False
    retained_disk = sum(path.stat().st_size for path in (stdout_path, stderr_path) if path.is_file())
    return {
        "label": label,
        "command": command,
        "cwd": str(cwd),
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": relative(stdout_path),
        "stderr": relative(stderr_path),
        "stdout_bytes_observed": observed["stdout"],
        "stderr_bytes_observed": observed["stderr"],
        "output_bounded": not any(overflow.values()),
        "retained_process_output_bytes": retained_disk,
        "disk_bounded": retained_disk <= MAX_RETAINED_DISK_BYTES,
        "cleanup": {
            "parent_reaped": parent_reaped,
            "group_alive_before": group_before,
            "group_alive_after": group_after,
            "reader_threads_joined": not any(thread.is_alive() for thread in threads),
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "setup_error": setup_error,
        },
    }


def stdout(result: dict) -> str:
    path = Path(result["stdout"])
    return (path if path.is_absolute() else HERE / path).read_text(errors="replace")


def stderr(result: dict) -> str:
    path = Path(result["stderr"])
    return (path if path.is_absolute() else HERE / path).read_text(errors="replace")


def require_clean_child(result: dict, label: str) -> None:
    require(result["returncode"] == 0 and not result["timed_out"], f"{label} failed: {result}")
    require(result["output_bounded"] and result["disk_bounded"], f"{label} exceeded output/disk bound: {result}")
    require(result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"], f"{label} left a process group: {result}")


def tree_bytes(root: Path) -> int:
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file() and not path.is_symlink())


def manifest_artifacts(recording: Path) -> dict:
    manifest_path = recording / "manifest.json"
    require(manifest_path.is_file(), f"recording manifest is missing: {manifest_path}")
    manifest = json.loads(manifest_path.read_text())
    artifacts = manifest.get("artifacts", [])
    require(isinstance(artifacts, list) and artifacts, "recording manifest has no artifacts")
    for artifact in artifacts:
        path = recording / artifact["path"]
        require(path.is_file(), f"manifest artifact is missing: {path}")
        require(sha256_file(path) == artifact["sha256"], f"manifest digest mismatch: {artifact['path']}")
    return manifest


def case_record_finalize_replay(binary: Path, child_timeout: float) -> dict:
    require(binary.is_file(), f"YUI binary is missing: {binary}")
    require(REPLAY_SOURCE.is_file(), f"immutable replay source is missing: {REPLAY_SOURCE}")
    require(sha256_file(REPLAY_SOURCE) == REPLAY_SOURCE_SHA256, "immutable replay source digest changed")
    run_root = Path(tempfile.mkdtemp(prefix="record-finalize-replay-", dir=RUN_ROOT))
    work = run_root / "work"
    (work / "evidence/runs").mkdir(parents=True, exist_ok=True)
    recording = run_root / "recording"
    command = [
        str(binary.resolve()), "session", "--replay", str(REPLAY_SOURCE), "--record-dir", str(recording),
        "--trace-audio", "--provider", "openai", "--model", "gpt-realtime",
    ]
    recorded = run_child(command, "record-fresh-directory", work, run_root / "record", child_timeout)
    require_clean_child(recorded, "fresh directory recording")
    manifest = manifest_artifacts(recording)
    names = [artifact["path"] for artifact in manifest["artifacts"]]
    expected_names = ["client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"]
    require(names == expected_names, f"fresh recording artifact order changed: {names}")
    for name in expected_names:
        require(name in names, f"fresh recording omitted {name}")
    require(manifest.get("terminal", {}).get("classification") == "provider_close", "fresh recording terminal classification changed")
    require(sha256_file(recording / "provider.json") == sha256_file(REPLAY_SOURCE), "fresh provider capture was relabeled or changed")
    session_log = [json.loads(line) for line in (recording / "session-log.jsonl").read_text().splitlines() if line.strip()]
    require(session_log and session_log[0]["response"]["text"] == "strict replay continuation", "recorded assistant text oracle changed")
    require(session_log[0]["response"]["audio_segments"] == ["audio/out-000.pcm"], "recorded audio segment order changed")
    require(len(session_log[0]["tool_events"]) == 2 and session_log[0]["tool_events"][1]["status"] == "completed", "recorded tool continuation oracle changed")
    require((recording / "audio/out-000.pcm").stat().st_size == 4800, "recorded output PCM byte oracle changed")
    replay = run_child([str(binary.resolve()), "session", "replay", str(recording)], "replay-finalized-directory", work, run_root / "replay", child_timeout)
    require_clean_child(replay, "finalized directory replay")
    replay_output = stdout(replay) + stderr(replay)
    require("Replay verified" in replay_output and "1 tool calls" in replay_output and "strict replay continuation" in replay_output, "replay continuation/tool oracle missing")
    require((recording / "audio-trace/timeline.jsonl").is_file(), "finalized audio timeline is missing")
    require(tree_bytes(run_root) <= MAX_RETAINED_DISK_BYTES, "retained record/replay evidence exceeded disk bound")
    result = {
        "schema": "audio-runtime.c56.record-finalize-replay.v1",
        "passed": True,
        "source_revision": BASELINE_REVISION,
        "binary": {"path": str(binary), "sha256": sha256_file(binary)},
        "immutable_replay_source": {"path": str(REPLAY_SOURCE), "sha256": sha256_file(REPLAY_SOURCE), "expected_sha256": REPLAY_SOURCE_SHA256},
        "recording": {"path": relative(recording), "manifest": manifest, "session_log": session_log, "execution": recorded},
        "replay": {"execution": replay, "stdout": replay_output},
        "bounds": {"child_timeout_seconds": child_timeout, "aggregate_timeout_seconds": 600, "max_child_output_bytes": MAX_CHILD_OUTPUT_BYTES, "max_retained_disk_bytes": MAX_RETAINED_DISK_BYTES, "retained_run_bytes": tree_bytes(run_root)},
        "credential_free": True,
        "network": "not_requested",
    }
    write_json(HERE / "evidence/record-finalize-replay.json", result)
    return result


def case_non_recording(binary: Path, child_timeout: float) -> dict:
    require(binary.is_file(), f"YUI binary is missing: {binary}")
    run_root = Path(tempfile.mkdtemp(prefix="non-recording-", dir=RUN_ROOT))
    work = run_root / "work"
    work.mkdir(parents=True, exist_ok=True)
    result = run_child([str(binary.resolve()), "session", "--replay", str(NON_RECORDING_FIXTURE), "--no-terminal-tools"], "non-recording-replay", work, run_root, child_timeout)
    require_clean_child(result, "non-recording session")
    output = stdout(result)
    require("Hello! How can I help you today?" in output, "non-recording session response oracle changed")
    forbidden = ["manifest.json", "provider.json", "session-log.jsonl", "client.transcript.jsonl", "agent.transcript.jsonl"]
    created = [str(path.relative_to(work)) for path in work.rglob("*") if path.is_file()]
    require(not any(Path(path).name in forbidden for path in created), f"non-recording workflow created recording artifacts: {created}")
    report = {"schema": "audio-runtime.c56.non-recording-regression.v1", "passed": True, "execution": result, "stdout": output, "work_files": created, "recording_side_effects": False}
    write_json(HERE / "evidence/non-recording-regression.json", report)
    return report


def case_negative(child_timeout: float) -> dict:
    run_root = Path(tempfile.mkdtemp(prefix="runner-negative-controls-", dir=RUN_ROOT))
    overflow_code = "import sys; sys.stdout.write('x' * 131072); sys.stdout.flush()"
    overflow = run_child([sys.executable, "-c", overflow_code], "output-overflow-control", REPO, run_root / "overflow", min(child_timeout, 10.0))
    require(overflow["returncode"] == 0 and not overflow["output_bounded"], "output overflow control did not retain a bounded causal failure")
    timeout_code = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)']); time.sleep(30)"
    timeout = run_child([sys.executable, "-c", timeout_code], "process-group-timeout-control", REPO, run_root / "timeout", min(child_timeout, 2.0))
    require(timeout["timed_out"] and timeout["cleanup"]["parent_reaped"] and not timeout["cleanup"]["group_alive_after"], "timeout control did not prove process-group cleanup")
    report = {
        "schema": "audio-runtime.c56.runner-negative-controls.v1",
        "passed": True,
        "controls": {"output_overflow": overflow, "process_group_timeout": timeout},
        "causal_controls": {"output_cap_bytes": MAX_CHILD_OUTPUT_BYTES, "process_group_signal": "SIGTERM_then_SIGKILL", "retry": False},
    }
    write_json(HERE / "evidence/runner-negative-controls.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=["record-finalize-replay", "non-recording-regression", "runner-negative-controls"])
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=600.0)
    args = parser.parse_args()
    require(0 < args.child_timeout <= 60.0, "child timeout must be in (0, 60]")
    require(0 < args.aggregate_timeout <= 600.0, "aggregate timeout must be in (0, 600]")
    started = time.monotonic()
    RUN_ROOT.mkdir(parents=True, exist_ok=True)
    if args.case in {"record-finalize-replay", "non-recording-regression"}:
        require(args.binary is not None, f"--binary is required for {args.case}")
    if args.case == "record-finalize-replay":
        report = case_record_finalize_replay(args.binary, args.child_timeout)
    elif args.case == "non-recording-regression":
        report = case_non_recording(args.binary, args.child_timeout)
    else:
        report = case_negative(args.child_timeout)
    elapsed = time.monotonic() - started
    require(elapsed <= args.aggregate_timeout, f"aggregate evidence timeout exceeded: {elapsed:.3f}s")
    report["elapsed_ms"] = int(elapsed * 1000)
    report["aggregate_timeout_seconds"] = args.aggregate_timeout
    write_json(HERE / f"evidence/{args.case}.summary.json", report)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RunnerError, subprocess.CalledProcessError) as error:
        print(f"runner failure: {error}", file=sys.stderr)
        raise SystemExit(1)
