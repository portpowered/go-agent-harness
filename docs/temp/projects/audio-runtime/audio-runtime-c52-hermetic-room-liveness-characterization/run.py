#!/usr/bin/env python3
"""Run the frozen C52 comparison in clean, instrumented scratch archives."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from collections.abc import Mapping
from datetime import datetime, timezone
from pathlib import Path


EVIDENCE = Path(__file__).resolve().parent
DEFAULT_MATRIX = EVIDENCE / "matrix.json"
PR438 = "823bd350fe5d11782c38bda87d7b7bfd7d89d7cd"
PLANNING_MAIN = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MAX_AGGREGATE_SECONDS = 900
DEFAULT_OUTPUT_CAP = 524288
MATRIX_ENV_KEYS = frozenset({
    "CGO_ENABLED",
    "GOFLAGS",
    "GOMAXPROCS",
    "GOTRACEBACK",
    "GOTOOLCHAIN",
    "GOSUMDB",
    "C52_CASE_ORDER",
    "C52_SHUFFLE_SEED",
})
MATRIX_BASE_ONLY_KEYS = frozenset({"C52_TAGS"})
FIXED_ENV_KEYS = frozenset({
    "PATH",
    "HOME",
    "TMPDIR",
    "LANG",
    "LC_ALL",
    "TZ",
    "GOENV",
    "GOPROXY",
    "GOPRIVATE",
    "GONOPROXY",
    "GONOSUMDB",
})
RUNNER_ENV_KEYS = frozenset({
    "C52_REVISION",
    "C52_MATRIX_CELL",
    "C52_TRACE_PATH",
    "C52_OVERLAY_HASH",
    "GOCACHE",
    "GOMODCACHE",
    "GOTMPDIR",
    "GOWORK",
})
PATH_FALLBACKS = (
    "/usr/local/go/bin",
    "/usr/local/bin",
    "/opt/homebrew/bin",
    "/usr/bin",
    "/bin",
    "/usr/sbin",
    "/sbin",
)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def json_write(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def command_result(command: list[str], cwd: Path, env: dict[str, str] | None = None, timeout: float = 30.0) -> dict[str, object]:
    started = time.monotonic()
    try:
        result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, check=False)
        return {
            "command": command,
            "cwd": str(cwd),
            "exit_code": result.returncode,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "duration_seconds": time.monotonic() - started,
            "timed_out": False,
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "command": command,
            "cwd": str(cwd),
            "exit_code": None,
            "stdout": (exc.stdout or "") if isinstance(exc.stdout, str) else "",
            "stderr": (exc.stderr or "") if isinstance(exc.stderr, str) else "",
            "duration_seconds": time.monotonic() - started,
            "timed_out": True,
        }


def git_output(repo: Path, *args: str) -> str:
    result = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def archive_content_sha256(path: Path) -> str:
    entries: list[dict[str, object]] = []
    with tarfile.open(path, "r:*") as stream:
        for member in stream.getmembers():
            entry: dict[str, object] = {
                "name": member.name,
                "mode": member.mode,
                "size": member.size,
            }
            if member.isdir():
                entry["type"] = "directory"
            elif member.isfile():
                entry["type"] = "file"
                source = stream.extractfile(member)
                if source is None:
                    raise RuntimeError(f"archive member has no file content: {path}:{member.name}")
                digest = hashlib.sha256()
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    digest.update(chunk)
                entry["sha256"] = digest.hexdigest()
            elif member.issym() or member.islnk():
                entry["type"] = "symlink" if member.issym() else "hardlink"
                entry["linkname"] = member.linkname
            else:
                raise RuntimeError(f"unsupported archive member type: {path}:{member.name}")
            entries.append(entry)
    encoded = json.dumps(sorted(entries, key=lambda item: str(item["name"])), sort_keys=True, separators=(",", ":")).encode()
    return sha256_bytes(encoded)


def archive_revision(repo: Path, revision: str, destination: Path) -> dict[str, object]:
    destination.parent.mkdir(parents=True, exist_ok=True)
    resolved = git_output(repo, "rev-parse", revision)
    if resolved != revision:
        raise RuntimeError(f"unexpected revision resolution for {revision}: {resolved}")
    # Generate a fresh archive on every invocation. A retained archive is
    # evidence input, not a cache: it may only be reused after its bytes have
    # been compared with the exact fixed revision requested by this run.
    with tempfile.NamedTemporaryFile(dir=destination.parent, prefix=f".{destination.name}.", suffix=".partial", delete=False) as stream:
        partial = Path(stream.name)
    try:
        with partial.open("wb") as stream:
            result = subprocess.run(["git", "-C", str(repo), "archive", "--format=tar", revision], stdout=stream, stderr=subprocess.PIPE, check=False)
        if result.returncode != 0:
            raise RuntimeError(f"git archive {revision} failed: {result.stderr.decode(errors='replace')}")
        generated_sha256 = sha256_file(partial)
        generated_content_sha256 = archive_content_sha256(partial)
        if destination.exists():
            if destination.is_symlink() or not destination.is_file():
                raise RuntimeError(f"pre-existing source archive is not a regular file: {destination}")
            existing_content_sha256 = archive_content_sha256(destination)
            if existing_content_sha256 != generated_content_sha256:
                raise RuntimeError(
                    f"pre-existing source archive contents do not match fixed revision {revision}: "
                    f"{destination} has content {existing_content_sha256}, generated {generated_content_sha256}"
                )
        else:
            partial.replace(destination)
        # Re-read the retained path after the reuse decision.  This closes the
        # validation window if an external process replaces the archive while
        # the scratch tree is being prepared.
        retained_content_sha256 = archive_content_sha256(destination)
        if retained_content_sha256 != generated_content_sha256:
            raise RuntimeError(
                f"retained source archive changed after fixed-revision validation for {revision}: "
                f"{destination} has content {retained_content_sha256}, generated {generated_content_sha256}"
            )
    finally:
        partial.unlink(missing_ok=True)
    return {
        "revision": revision,
        "resolved_revision": resolved,
        "path": str(destination.relative_to(EVIDENCE)),
        "bytes": destination.stat().st_size,
        "sha256": sha256_file(destination),
        "content_sha256": archive_content_sha256(destination),
    }


def remove_readonly_path(function: object, path: str, _exc_info: object) -> None:
    try:
        os.chmod(path, 0o700)
        function(path)
    except OSError:
        # The caller verifies the path after rmtree and fails closed if this
        # retry did not remove it.
        return


def remove_run_cache(cache_root: Path) -> None:
    for _ in range(3):
        if cache_root.exists():
            shutil.rmtree(cache_root, onerror=remove_readonly_path)
        if not cache_root.exists():
            return
        time.sleep(0.05)
    raise RuntimeError(f"run-local cache was not removed after child cleanup: {cache_root}")


def safe_extract(archive: Path, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    root = destination.resolve()
    with tarfile.open(archive, "r:*") as stream:
        for member in stream.getmembers():
            target = (destination / member.name).resolve()
            if root != target and root not in target.parents:
                raise RuntimeError(f"archive path escapes scratch root: {member.name}")
            stream.extract(member, destination)


def file_digest(path: Path) -> str:
    return sha256_file(path) if path.is_file() else ""


def tree_manifest(root: Path) -> dict[str, object]:
    files: list[dict[str, object]] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        files.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    encoded = json.dumps(files, sort_keys=True, separators=(",", ":")).encode()
    return {"root": str(root), "file_count": len(files), "files_sha256": sha256_bytes(encoded), "files": files}


class CappedReader:
    def __init__(self, stream, cap: int) -> None:
        self.stream = stream
        self.cap = cap
        self.data = bytearray()
        self.total = 0
        self.overflow = False
        self.done = threading.Event()

    def read(self) -> None:
        try:
            while True:
                chunk = self.stream.read(65536)
                if not chunk:
                    return
                self.total += len(chunk)
                if len(self.data) < self.cap:
                    remaining = self.cap - len(self.data)
                    self.data.extend(chunk[:remaining])
                if self.total > self.cap:
                    self.overflow = True
        finally:
            self.done.set()


def group_exists(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def signal_group(pgid: int, signum: signal.Signals, attempts: int = 1) -> tuple[bool, str | None]:
    for attempt in range(attempts):
        try:
            os.killpg(pgid, signum)
            return True, None
        except ProcessLookupError:
            return False, None
        except PermissionError as exc:
            if attempt + 1 < attempts:
                time.sleep(0.05)
                continue
            return False, f"{type(exc).__name__}: {exc}"
    return False, None


def stop_group(process: subprocess.Popen[bytes], reason: str) -> dict[str, object]:
    pgid = process.pid
    term_sent, term_error = signal_group(pgid, signal.SIGTERM, attempts=3)
    term_deadline = time.monotonic() + 0.75
    while group_exists(pgid) and time.monotonic() < term_deadline:
        time.sleep(0.02)
    kill_sent = False
    kill_error = None
    if group_exists(pgid):
        kill_sent, kill_error = signal_group(pgid, signal.SIGKILL, attempts=8)
    try:
        process.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        signal_group(pgid, signal.SIGKILL, attempts=8)
        try:
            process.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            pass
    settle_deadline = time.monotonic() + 0.5
    while group_exists(pgid) and time.monotonic() < settle_deadline:
        time.sleep(0.02)
    group_survivor = group_exists(pgid)
    cleanup = {
        "reason": reason,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "parent_exit_code": process.returncode,
        "group_survivor": group_survivor,
        # Keep both error fields present even when no error occurred.  The
        # evidence verifier must be able to distinguish an observed null from
        # a field that was removed from a copied/tampered process record.
        "term_error": term_error,
        "kill_error": kill_error,
    }
    return cleanup


def run_bounded(command: list[str], cwd: Path, env: dict[str, str], timeout: float, output_cap: int, result_dir: Path) -> dict[str, object]:
    result_dir.mkdir(parents=True, exist_ok=True)
    started_at = utc_now()
    started = time.monotonic()
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    assert process.stdout is not None and process.stderr is not None
    stdout = CappedReader(process.stdout, output_cap)
    stderr = CappedReader(process.stderr, output_cap)
    stdout_thread = threading.Thread(target=stdout.read, name="c52-stdout", daemon=True)
    stderr_thread = threading.Thread(target=stderr.read, name="c52-stderr", daemon=True)
    stdout_thread.start()
    stderr_thread.start()
    timeout_reason = ""
    while process.poll() is None:
        if stdout.overflow or stderr.overflow:
            timeout_reason = "output_cap_exceeded"
            break
        if time.monotonic() - started >= timeout:
            timeout_reason = "child_timeout"
            break
        time.sleep(0.02)
    cleanup: dict[str, object] = {}
    if timeout_reason:
        cleanup = stop_group(process, timeout_reason)
    else:
        process.wait()
        cleanup = stop_group(process, "parent_exit")
    stdout_thread.join(timeout=2.0)
    stderr_thread.join(timeout=2.0)
    if not stdout.done.is_set() or not stderr.done.is_set():
        cleanup = {**cleanup, "reader_survivor": True}
    stdout_bytes = bytes(stdout.data)
    stderr_bytes = bytes(stderr.data)
    (result_dir / "stdout.log").write_bytes(stdout_bytes)
    (result_dir / "stderr.log").write_bytes(stderr_bytes)
    result = {
        "command": command,
        "cwd": str(cwd),
        "started_at": started_at,
        "ended_at": utc_now(),
        "duration_seconds": time.monotonic() - started,
        "exit_code": process.returncode,
        "timed_out": timeout_reason == "child_timeout",
        "output_overflow": stdout.overflow or stderr.overflow,
        "stdout_bytes": len(stdout_bytes),
        "stderr_bytes": len(stderr_bytes),
        "stdout_sha256": sha256_bytes(stdout_bytes),
        "stderr_sha256": sha256_bytes(stderr_bytes),
        "cleanup": cleanup,
        "reader_survivor": bool(cleanup.get("reader_survivor", False)),
        "first_failure": first_failure(stdout_bytes, stderr_bytes),
    }
    json_write(result_dir / "process.json", result)
    return result


def first_failure(stdout: bytes, stderr: bytes) -> dict[str, object] | None:
    for raw in stdout.decode(errors="replace").splitlines():
        try:
            event = json.loads(raw)
        except json.JSONDecodeError:
            continue
        if event.get("Action") == "fail" or (event.get("Action") == "output" and "browser_parity_test.go:" in event.get("Output", "")):
            return {"stream": "stdout", "event": event}
    for index, line in enumerate(stderr.decode(errors="replace").splitlines(), start=1):
        if line.strip():
            return {"stream": "stderr", "line": index, "text": line}
    return None


def parse_go_json(stdout_path: Path, required_tests: list[str], package: str, expected_count: int | None = None) -> dict[str, object]:
    events: list[dict[str, object]] = []
    invalid_lines: list[str] = []
    for raw in stdout_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            value = json.loads(raw)
        except json.JSONDecodeError:
            invalid_lines.append(raw)
            continue
        if isinstance(value, dict):
            events.append(value)
    test_events: dict[str, dict[str, int]] = {}
    for event in events:
        test = event.get("Test")
        if not isinstance(test, str) or not test:
            continue
        action = str(event.get("Action", ""))
        counts = test_events.setdefault(test, {"run": 0, "pass": 0, "fail": 0, "skip": 0})
        if action in counts:
            counts[action] += 1
    selected = {test: test_events.get(test, {}).get("run", 0) for test in required_tests}
    relevant = [name for name in test_events if name.startswith("TestRunnerRoutesTypedLivenessFaultAndPreservesPeer")]
    package_events = [event for event in events if event.get("Package") == "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle" and not event.get("Test")]
    output = "\n".join(str(event.get("Output", "")) for event in events if event.get("Action") == "output")
    requested_count = expected_count
    required_pass_counts = {test: test_events.get(test, {}).get("pass", 0) for test in required_tests}
    required_fail_counts = {test: test_events.get(test, {}).get("fail", 0) for test in required_tests}
    required_skip_counts = {test: test_events.get(test, {}).get("skip", 0) for test in required_tests}
    selection_valid = (
        requested_count is not None
        and requested_count > 0
        and not invalid_lines
        and "(cached)" not in output
        and all(test_events.get(test, {}).get("run", 0) == requested_count for test in required_tests)
        and all(required_pass_counts[test] == requested_count for test in required_tests)
        and all(required_fail_counts[test] == 0 for test in required_tests)
        and all(required_skip_counts[test] == 0 for test in required_tests)
    )
    return {
        "json_line_count": len(events),
        "invalid_json_lines": len(invalid_lines),
        "invalid_json_samples": invalid_lines[:3],
        "test_events": test_events,
        "required_test_run_counts": selected,
        "required_test_pass_counts": required_pass_counts,
        "required_test_fail_counts": required_fail_counts,
        "required_test_skip_counts": required_skip_counts,
        "requested_count": requested_count,
        "selection_valid": selection_valid,
        "relevant_test_names": sorted(relevant),
        "package_events": package_events,
        "cached_marker": "(cached)" in output,
        "no_tests_marker": "[no tests to run]" in output or any(event.get("Action") == "output" and "no tests to run" in str(event.get("Output", "")) for event in events),
        "first_assertion": next((line for line in output.splitlines() if "browser_parity_test.go:" in line), ""),
        "package": package,
    }


def overlay_replacements() -> list[tuple[str, list[tuple[str, str]]]]:
    return [
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/events.go",
            [
                (
                    "\tdrain := &eventDrain{queue: make(chan session.LiveEvent, eventQueueCapacity), stop: make(chan struct{}), done: make(chan struct{})}\n\tdrain.on = eventObserver(ctx, participantID, diagnosticCallback, sink, onTurn, now, onTerminal, onError)",
                    "\tdrain := &eventDrain{queue: make(chan session.LiveEvent, eventQueueCapacity), stop: make(chan struct{}), done: make(chan struct{})}\n\tc52Trace(\"event_drain_created\", participantID, map[string]string{\"queue_capacity\": c52Int(eventQueueCapacity)})\n\tdrain.on = eventObserver(ctx, participantID, diagnosticCallback, sink, onTurn, now, onTerminal, onError)",
                ),
                (
                    "\t\tif onTerminal != nil && isTerminalEvent(event) {\n\t\t\tonTerminal(event)\n\t\t}\n\t\tpublishEvent(ctx, participantID, event, sink, onError)\n\t\tnotifyTurnComplete(event, onTurn)",
                    "\t\tc52TraceEvent(\"live_event_received\", participantID, event)\n\t\tif onTerminal != nil && isTerminalEvent(event) {\n\t\t\tc52TraceEvent(\"terminal_observed\", participantID, event)\n\t\t\tonTerminal(event)\n\t\t\tc52TraceEvent(\"terminal_latch_callback_returned\", participantID, event)\n\t\t}\n\t\tc52TraceEvent(\"sink_publish_before\", participantID, event)\n\t\tpublishEvent(ctx, participantID, event, sink, onError)\n\t\tc52TraceEvent(\"sink_publish_returned\", participantID, event)\n\t\tnotifyTurnComplete(event, onTurn)",
                ),
            ],
        ),
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/participant.go",
            [
                (
                    "\tif err := handle.Start(ctx); err != nil {\n\t\treturn nil, closeStartedParticipant(active, release, fmt.Errorf(\"start live participant: %w\", err))\n\t}\n\tstartCancellationWatcher(ctx, active)",
                    "\tif err := handle.Start(ctx); err != nil {\n\t\treturn nil, closeStartedParticipant(active, release, fmt.Errorf(\"start live participant: %w\", err))\n\t}\n\tc52Trace(\"participant_started\", participant.ID, nil)\n\tstartCancellationWatcher(ctx, active)",
                ),
            ],
        ),
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/state.go",
            [
                (
                    "\tif previous, ok := s.terminals[id]; !ok || previous.classification == \"\" && value.classification != \"\" {\n\t\ts.terminals[id] = value\n\t}\n\ts.mu.Unlock()\n}\n\nfunc terminalMetadataFromEvent",
                    "\tif previous, ok := s.terminals[id]; !ok || previous.classification == \"\" && value.classification != \"\" {\n\t\ts.terminals[id] = value\n\t}\n\ts.mu.Unlock()\n\tc52Trace(\"terminal_metadata_latched\", id, map[string]string{\"classification\": value.classification, \"terminal_reason\": value.reason, \"terminal_provenance\": value.provenance, \"output_state\": value.outputState})\n}\n\nfunc terminalMetadataFromEvent",
                ),
                (
                    "\tif value.handle != nil {\n\t\tvalue.handle.Cancel(err)\n\t}\n}\n\nfunc (s *runState) noteTurn",
                    "\tif value.handle != nil {\n\t\tc52Trace(\"liveness_cancel_requested\", id, map[string]string{\"error\": err.Error()})\n\t\tvalue.handle.Cancel(err)\n\t}\n}\n\nfunc (s *runState) noteTurn",
                ),
                (
                    "\tvar err error\n\tif value.handle != nil {\n\t\terr = value.handle.Wait()\n\t} else {",
                    "\tvar err error\n\tc52Trace(\"participant_wait_started\", value.participant.ID, nil)\n\tif value.handle != nil {\n\t\terr = value.handle.Wait()\n\t\tc52Trace(\"participant_wait_returned\", value.participant.ID, map[string]string{\"error\": c52ErrorString(err)})\n\t} else {",
                ),
                (
                    "\tif value.handle != nil {\n\t\tcloseErr = value.handle.Close()\n\t}\n\tmediaErr := value.closeMedia()",
                    "\tif value.handle != nil {\n\t\tcloseErr = value.handle.Close()\n\t\tc52Trace(\"participant_handle_closed\", value.participant.ID, map[string]string{\"error\": c52ErrorString(closeErr)})\n\t}\n\tmediaErr := value.closeMedia()",
                ),
                (
                    "\tif value.events != nil {\n\t\tvalue.events.Stop()\n\t\teventErr = value.events.Wait()\n\t}\n\treturn errors.Join(err, closeErr, mediaErr, eventErr)",
                    "\tif value.events != nil {\n\t\tvalue.events.Stop()\n\t\teventErr = value.events.Wait()\n\t\tc52Trace(\"participant_event_drain_waited\", value.participant.ID, map[string]string{\"error\": c52ErrorString(eventErr)})\n\t}\n\tc52Trace(\"participant_wait_completed\", value.participant.ID, map[string]string{\"error\": c52ErrorString(errors.Join(err, closeErr, mediaErr, eventErr))})\n\treturn errors.Join(err, closeErr, mediaErr, eventErr)",
                ),
                (
                    "\ts.mu.Unlock()\n\ts.stopWhenAgentsDone()\n}\n\nconst (",
                    "\ts.mu.Unlock()\n\tc52Trace(\"participant_result_recorded\", participant.ID, map[string]string{\"classification\": value.Classification, \"termination_reason\": string(value.TerminationReason), \"error\": value.Error, \"connected\": c52Bool(value.Connected)})\n\ts.stopWhenAgentsDone()\n}\n\nconst (",
                ),
            ],
        ),
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/runner.go",
            [
                (
                    "\tstate.waitAll(runCtx, request, r.currentTime)\n\treturn r.finishRun(runCtx, state, graph, manifest, request, recorder)",
                    "\tstate.waitAll(runCtx, request, r.currentTime)\n\tresult, runErr := r.finishRun(runCtx, state, graph, manifest, request, recorder)\n\tc52TraceRoomResult(\"room_run_return\", result, runErr)\n\treturn result, runErr",
                ),
                (
                    "\tresult, runErr := state.result(ctx)\n\tfinishMissingParticipants(state, result, manifest)",
                    "\tresult, runErr := state.result(ctx)\n\tc52TraceRoomResult(\"room_result_snapshot\", result, runErr)\n\tfinishMissingParticipants(state, result, manifest)",
                ),
                (
                    "\tresult, refreshedErr = state.result(ctx)\n\trunErr = errors.Join(runErr, refreshedErr)",
                    "\tresult, refreshedErr = state.result(ctx)\n\tc52TraceRoomResult(\"room_result_after_missing_participants\", result, refreshedErr)\n\trunErr = errors.Join(runErr, refreshedErr)",
                ),
                (
                    "func (r Runner) finalizeRun(result rooms.RoomResult, runErr error, manifest rooms.Manifest, request rooms.RoomRunOptions, recorder *evidence.Recorder) (rooms.RoomResult, error) {\n\tif request.OnParticipantTerminated != nil {",
                    "func (r Runner) finalizeRun(result rooms.RoomResult, runErr error, manifest rooms.Manifest, request rooms.RoomRunOptions, recorder *evidence.Recorder) (rooms.RoomResult, error) {\n\tc52TraceRoomResult(\"room_finalize_input\", result, runErr)\n\tif request.OnParticipantTerminated != nil {",
                ),
            ],
        ),
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/browser_parity_test.go",
            [
                (
                    "func TestRunnerRoutesTypedLivenessFaultAndPreservesPeer(t *testing.T) {\n\tfor _, test := range []struct {\n\t\tname           string\n\t\tclassification string\n\t}{\n\t\t{name: \"empty response\", classification: \"silent_provider_empty_response\"},\n\t\t{name: \"provider timeout\", classification: \"silent_provider_timeout\"},\n\t} {",
                    "func TestRunnerRoutesTypedLivenessFaultAndPreservesPeer(t *testing.T) {\n\tcases := []struct {\n\t\tname           string\n\t\tclassification string\n\t}{\n\t\t{name: \"empty response\", classification: \"silent_provider_empty_response\"},\n\t\t{name: \"provider timeout\", classification: \"silent_provider_timeout\"},\n\t}\n\tif c52ShouldSwapCases() {\n\t\tcases[0], cases[1] = cases[1], cases[0]\n\t}\n\tfor _, test := range cases {",
                ),
                (
                    "func runTypedLivenessCase(t *testing.T, classification string) {\n\tsilent := newFakeLiveHandle()",
                    "func runTypedLivenessCase(t *testing.T, classification string) {\n\tc52Trace(\"case_started\", typedLivenessSilentID, map[string]string{\"classification\": classification})\n\tsilent := newFakeLiveHandle()",
                ),
                (
                    "\tresultCh := startTypedLivenessRun(ctx, &runner, sink, faultSeen, &faultOnce)\n\twaitForTypedLivenessFault(t, faultSeen)\n\tif got := peer.cancelCallsSnapshot(); got != 0 {",
                    "\tresultCh := startTypedLivenessRun(ctx, &runner, sink, faultSeen, &faultOnce)\n\troomResultObserved := false\n\tdefer func() {\n\t\tif roomResultObserved {\n\t\t\treturn\n\t\t}\n\t\tc52Trace(\"room_context_cancel_requested\", \"room\", map[string]string{\"reason\": \"test_deferred_cleanup_after_assertion\"})\n\t\tcancel()\n\t\tselect {\n\t\tcase deferredOutcome := <-resultCh:\n\t\t\tc52TraceRoomResult(\"test_deferred_outcome\", deferredOutcome.result, deferredOutcome.err)\n\t\tcase <-time.After(2 * time.Second):\n\t\t\tc52Trace(\"test_deferred_outcome_timeout\", \"room\", map[string]string{\"reason\": \"bounded_deferred_cleanup\"})\n\t\t}\n\t}()\n\twaitForTypedLivenessFault(t, faultSeen)\n\tc52Trace(\"fault_observed\", typedLivenessSilentID, map[string]string{\"classification\": classification})\n\tpeerCancelCalls := peer.cancelCallsSnapshot()\n\tc52Trace(\"peer_cancel_snapshot\", \"peer\", map[string]string{\"count\": c52Int(peerCancelCalls), \"before\": \"external_room_cancel\"})\n\tif got := peerCancelCalls; got != 0 {",
                ),
                (
                    "\tcancel()\n\toutcome := waitForRoomResult(t, resultCh)\n\tassertTypedLivenessResult(t, outcome, classification)",
                    "\tc52Trace(\"room_context_cancel_requested\", \"room\", map[string]string{\"reason\": \"test_external_cancel\"})\n\tcancel()\n\toutcome := waitForRoomResult(t, resultCh)\n\troomResultObserved = true\n\tc52TraceRoomResult(\"test_outcome\", outcome.result, outcome.err)\n\tassertTypedLivenessResult(t, outcome, classification)",
                ),
                (
                    "\tgo func() {\n\t\tresult, err := runner.Run(ctx, nil, rooms.RoomRunOptions{",
                    "\tgo func() {\n\t\tc52Trace(\"runner_run_started\", \"room\", nil)\n\t\tresult, err := runner.Run(ctx, nil, rooms.RoomRunOptions{",
                ),
                (
                    "\t\t\t},\n\t\t})\n\t\tresultCh <- roomRunOutcome{result: result, err: err}",
                    "\t\t\t},\n\t\t})\n\t\tc52TraceRoomResult(\"runner_run_goroutine_result\", result, err)\n\t\tresultCh <- roomRunOutcome{result: result, err: err}",
                ),
                (
                    "\t\t\tOnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {\n\t\t\t\tif participantID == typedLivenessSilentID && record.Event == \"live_liveness_fault\" {",
                    "\t\t\tOnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {\n\t\t\t\tc52TraceDiagnostic(participantID, record)\n\t\t\t\tif participantID == typedLivenessSilentID && record.Event == \"live_liveness_fault\" {",
                ),
            ],
        ),
        (
            "go-agent-runtime/services/rooms/internal/lifecycle/runner_test.go",
            [
                (
                    "\thandle := s.handles[request.SessionID]\n\tif handle != nil {\n\t\thandle.mu.Lock()\n\t\thandle.capturePath = request.Replay.OutputCapturePath\n\t\thandle.mu.Unlock()\n\t}\n\treturn handle, nil",
                    "\thandle := s.handles[request.SessionID]\n\tif handle != nil {\n\t\thandle.mu.Lock()\n\t\thandle.capturePath = request.Replay.OutputCapturePath\n\t\thandle.traceParticipant = request.SessionID\n\t\thandle.mu.Unlock()\n\t\tc52Trace(\"live_open\", request.SessionID, map[string]string{\"provider\": request.Provider, \"model\": request.Model})\n\t}\n\treturn handle, nil",
                ),
                (
                    "\tstartEvents []session.LiveEvent\n\tcapturePath string\n}",
                    "\tstartEvents      []session.LiveEvent\n\tcapturePath      string\n\ttraceParticipant string\n}",
                ),
                (
                    "\th.mu.Lock()\n\th.startCount++\n\tstartEvents := append([]session.LiveEvent(nil), h.startEvents...)\n\th.mu.Unlock()\n\tfor _, event := range startEvents {",
                    "\th.mu.Lock()\n\th.startCount++\n\tstartEvents := append([]session.LiveEvent(nil), h.startEvents...)\n\tparticipant := h.traceParticipant\n\tstartCount := h.startCount\n\th.mu.Unlock()\n\tc52Trace(\"handle_start\", participant, map[string]string{\"count\": c52Int(startCount)})\n\tfor _, event := range startEvents {",
                ),
                (
                    "func (h *fakeLiveHandle) Cancel(err error) {\n\th.mu.Lock()\n\th.cancelCount++\n\th.cancelErr = err\n\th.mu.Unlock()",
                    "func (h *fakeLiveHandle) Cancel(err error) {\n\th.mu.Lock()\n\th.cancelCount++\n\th.cancelErr = err\n\tparticipant := h.traceParticipant\n\tcancelCount := h.cancelCount\n\th.mu.Unlock()\n\tc52Trace(\"handle_cancel\", participant, map[string]string{\"count\": c52Int(cancelCount), \"cause\": c52ErrorString(err), \"cancel_kind\": c52CancelKind(err)})",
                ),
                (
                    "func (h *fakeLiveHandle) Wait() error {\n\t<-h.done\n\th.mu.Lock()\n\tdefer h.mu.Unlock()\n\treturn h.cancelErr\n}",
                    "func (h *fakeLiveHandle) Wait() error {\n\t<-h.done\n\th.mu.Lock()\n\tparticipant := h.traceParticipant\n\terr := h.cancelErr\n\th.mu.Unlock()\n\tc52Trace(\"handle_wait_returned\", participant, map[string]string{\"error\": c52ErrorString(err)})\n\treturn err\n}",
                ),
                (
                    "\th.mu.Lock()\n\th.closeCount++\n\tcapturePath := h.capturePath\n\th.mu.Unlock()\n\tif capturePath != \"\" {",
                    "\th.mu.Lock()\n\th.closeCount++\n\tcapturePath := h.capturePath\n\tparticipant := h.traceParticipant\n\tcloseCount := h.closeCount\n\th.mu.Unlock()\n\tc52Trace(\"handle_close_returned\", participant, map[string]string{\"count\": c52Int(closeCount)})\n\tif capturePath != \"\" {",
                ),
                (
                    "func (s *recordingRoomEventSink) Publish(_ context.Context, participantID string, event session.LiveEvent) error {\n\ts.mu.Lock()",
                    "func (s *recordingRoomEventSink) Publish(_ context.Context, participantID string, event session.LiveEvent) error {\n\tc52TraceEvent(\"sink_publish\", participantID, event)\n\ts.mu.Lock()",
                ),
            ],
        ),
    ]


def apply_overlay(scratch: Path, run_dir: Path) -> dict[str, object]:
    overlay_source = EVIDENCE / "overlay" / "c52_overlay.go"
    if not overlay_source.is_file():
        raise RuntimeError("missing committed overlay source")
    overlay_destination = scratch / "go-agent-runtime/services/rooms/internal/lifecycle/c52_overlay.go"
    overlay_destination.parent.mkdir(parents=True, exist_ok=True)
    overlay_destination.write_bytes(overlay_source.read_bytes())
    records: list[dict[str, object]] = [{"path": str(overlay_destination.relative_to(scratch)), "before_sha256": None, "after_sha256": sha256_file(overlay_destination), "kind": "generated_overlay"}]
    for relative, replacements in overlay_replacements():
        path = scratch / relative
        original = path.read_text(encoding="utf-8")
        updated = original
        for old, new in replacements:
            count = updated.count(old)
            if count != 1:
                raise RuntimeError(f"overlay anchor count for {relative} is {count}, expected 1")
            updated = updated.replace(old, new, 1)
        path.write_text(updated, encoding="utf-8")
        records.append({"path": relative, "before_sha256": sha256_bytes(original.encode()), "after_sha256": sha256_file(path), "kind": "scratch_rewrite"})
    go_files = [scratch / item["path"] for item in records if str(item["path"]).endswith(".go")]
    fmt = subprocess.run(["gofmt", "-w", *[str(path) for path in go_files]], cwd=scratch, capture_output=True, text=True, check=False)
    if fmt.returncode != 0:
        raise RuntimeError(f"gofmt overlay failed: {fmt.stderr}")
    for item in records:
        item["after_sha256"] = sha256_file(scratch / str(item["path"]))
    manifest = {
        "schema": "audio-runtime-c52-overlay-v1",
        "generated_at": utc_now(),
        "source_sha256": sha256_file(overlay_source),
        "files": records,
        "gofmt_command": ["gofmt", "-w", *[str(path.relative_to(scratch)) for path in go_files]],
    }
    json_write(run_dir / "overlay-manifest.json", manifest)
    return manifest


def allowed_worktree_dirty(repo: Path) -> list[str]:
    result = subprocess.run(["git", "-C", str(repo), "status", "--porcelain", "--untracked-files=all"], capture_output=True, text=True, check=False)
    paths: list[str] = []
    for line in result.stdout.splitlines():
        if len(line) >= 4:
            paths.append(line[3:])
    return paths


def hermetic_tool_path(host_environment: Mapping[str, str]) -> str:
    """Resolve required tools without forwarding the host PATH or its values."""
    ambient_path = host_environment.get("PATH", os.defpath)
    directories: list[str] = []
    for tool in ("go", "git", "clang", "gcc"):
        resolved = shutil.which(tool, path=ambient_path)
        if resolved:
            directories.append(str(Path(resolved).resolve().parent))
    directories.extend(PATH_FALLBACKS)
    unique = list(dict.fromkeys(path for path in directories if Path(path).is_dir()))
    if not unique:
        raise RuntimeError("no hermetic tool path is available")
    return os.pathsep.join(unique)


def hermetic_base_environment(host_environment: Mapping[str, str], cache_root: Path) -> dict[str, str]:
    home = cache_root / "home"
    temp = cache_root / "gotmp"
    home.mkdir(parents=True, exist_ok=True)
    temp.mkdir(parents=True, exist_ok=True)
    return {
        "PATH": hermetic_tool_path(host_environment),
        "HOME": str(home),
        "TMPDIR": str(temp),
        "LANG": "C",
        "LC_ALL": "C",
        "TZ": "UTC",
        "GOENV": "off",
        "GOPROXY": "https://proxy.golang.org,direct",
        "GOPRIVATE": "",
        "GONOPROXY": "",
        "GONOSUMDB": "",
    }


def apply_declared_environment(env: dict[str, str], values: object, origin: str, *, allow_base_tags: bool = False) -> None:
    if not isinstance(values, dict):
        raise RuntimeError(f"{origin} must be an object")
    allowed = MATRIX_ENV_KEYS | (MATRIX_BASE_ONLY_KEYS if allow_base_tags else frozenset())
    unknown = set(str(key) for key in values) - allowed
    if unknown:
        raise RuntimeError(f"{origin} contains undeclared environment keys: {sorted(unknown)}")
    for key, value in values.items():
        key = str(key)
        if key in MATRIX_BASE_ONLY_KEYS:
            continue
        env[key] = str(value)


def environment_for(
    matrix: dict[str, object],
    cell: dict[str, object],
    revision: str,
    trace_path: Path,
    cache_root: Path,
    overlay_hash: str,
    host_environment: Mapping[str, str] | None = None,
) -> dict[str, str]:
    host = os.environ if host_environment is None else host_environment
    env = hermetic_base_environment(host, cache_root)
    base = matrix.get("base_environment", {})
    apply_declared_environment(env, base, "matrix.base_environment", allow_base_tags=True)
    apply_declared_environment(env, cell.get("env", {}), f"matrix.cell[{cell.get('id')}].env")
    env["C52_REVISION"] = revision
    env["C52_MATRIX_CELL"] = str(cell["id"])
    env["C52_TRACE_PATH"] = str(trace_path)
    env["C52_OVERLAY_HASH"] = overlay_hash
    env["GOCACHE"] = str(cache_root / "gocache")
    env["GOMODCACHE"] = str(cache_root / "gomodcache")
    env["GOTMPDIR"] = str(cache_root / "gotmp")
    env["GOWORK"] = ""
    return env


def actual_command(cell: dict[str, object], matrix: dict[str, object]) -> list[str]:
    args = [str(value) for value in cell.get("args", [])]
    if not args or args[0] != "test":
        raise RuntimeError(f"matrix cell {cell.get('id')} is not a go test command")
    tags = str(matrix.get("base_environment", {}).get("C52_TAGS", "")) if isinstance(matrix.get("base_environment"), dict) else ""
    if tags:
        args = [args[0], f"-tags={tags}", *args[1:]]
    return ["go", *args]


def required_tests_for(cell: dict[str, object]) -> list[str]:
    args = [str(value) for value in cell.get("args", [])]
    run = args[args.index("-run") + 1] if "-run" in args else ""
    if run.endswith("/provider_timeout$"):
        return ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"]
    if run.endswith("/empty_response$"):
        return ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/empty_response"]
    return [
        "TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout",
        "TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/empty_response",
    ]


def requested_count_for(cell: dict[str, object]) -> int | None:
    args = [str(value) for value in cell.get("args", [])]
    values = [value.split("=", 1)[1] for value in args if value.startswith("-count=")]
    if len(values) != 1 or not values[0].isdigit() or int(values[0]) <= 0:
        return None
    return int(values[0])


def run_matrix(matrix: dict[str, object], repo: Path, run_dir: Path, aggregate_timeout: float) -> dict[str, object]:
    started = time.monotonic()
    deadline = started + aggregate_timeout
    cache_root = run_dir / "cache"
    cache_root.mkdir(parents=True, exist_ok=True)
    for cache_name in ("gocache", "gomodcache", "gotmp"):
        (cache_root / cache_name).mkdir(parents=True, exist_ok=True)
    archives: dict[str, dict[str, object]] = {}
    scratch_roots: dict[str, Path] = {}
    for label, revision in (("pr438", PR438), ("planning-main", PLANNING_MAIN)):
        archive_path = EVIDENCE / "inputs/source-archives" / f"{label}-{revision[:12]}.tar.gz"
        archives[label] = archive_revision(repo, revision, archive_path)
        scratch = run_dir / "scratch" / label
        safe_extract(archive_path, scratch)
        scratch_roots[label] = scratch
        apply_overlay(scratch, run_dir / f"overlay-{label}")
    overlay_hash = sha256_file(EVIDENCE / "overlay/c52_overlay.go")
    cells: list[dict[str, object]] = []
    matrix_cells = matrix.get("cells")
    if not isinstance(matrix_cells, list):
        raise RuntimeError("matrix cells must be a list")
    for label, revision in (("pr438", PR438), ("planning-main", PLANNING_MAIN)):
        scratch = scratch_roots[label]
        module_root = scratch / "go-agent-runtime"
        if not module_root.is_dir():
            raise RuntimeError(f"missing go-agent-runtime module in extracted {label} archive")
        for cell_value in matrix_cells:
            if not isinstance(cell_value, dict):
                raise RuntimeError("matrix cell must be an object")
            cell = dict(cell_value)
            if time.monotonic() >= deadline:
                cells.append({"revision_label": label, "revision": revision, "cell_id": cell.get("id"), "status": "NOT_RUN_AGGREGATE_DEADLINE"})
                continue
            cell_dir = run_dir / "cells" / label / str(cell["id"])
            trace_path = cell_dir / "trace.jsonl"
            env = environment_for(matrix, cell, revision, trace_path, cache_root, overlay_hash)
            command = actual_command(cell, matrix)
            timeout = float(matrix.get("race_child_timeout_seconds", 180) if cell.get("kind") == "race" else matrix.get("child_timeout_seconds", 90))
            remaining = max(1.0, deadline - time.monotonic())
            timeout = min(timeout, max(1.0, remaining - 0.25))
            process = run_bounded(command, module_root, env, timeout, int(matrix.get("output_cap_bytes", DEFAULT_OUTPUT_CAP)), cell_dir)
            parsed = parse_go_json(
                cell_dir / "stdout.log",
                required_tests_for(cell),
                "./services/rooms/internal/lifecycle",
                requested_count_for(cell),
            )
            record = {
                "attempt": 1,
                "revision_label": label,
                "revision": revision,
                "cell": cell,
                "command": command,
                "environment": {key: env[key] for key in sorted(env)},
                "process": process,
                "selection": parsed,
                "trace_path": str(trace_path.relative_to(EVIDENCE)) if trace_path.exists() else None,
                "trace_sha256": sha256_file(trace_path) if trace_path.exists() else None,
                "status": "PASS" if process.get("exit_code") == 0 and not process.get("timed_out") and not process.get("output_overflow") and parsed.get("selection_valid") is True else "FAIL",
            }
            json_write(cell_dir / "cell.json", record)
            cells.append(record)
    negative = run_negative_controls(matrix, scratch_roots["planning-main"], run_dir, cache_root, overlay_hash, deadline)
    for scratch in scratch_roots.values():
        shutil.rmtree(scratch, ignore_errors=True)
    # All child processes have been reaped before this point; retain reports and
    # hashes but remove the run-local tool caches from the evidence bundle.
    remove_run_cache(cache_root)
    result = {
        "schema": "audio-runtime-c52-matrix-run-v1",
        "run_id": run_dir.name,
        "started_at": datetime.fromtimestamp(time.time() - (time.monotonic() - started), timezone.utc).isoformat(),
        "ended_at": utc_now(),
        "duration_seconds": time.monotonic() - started,
        "aggregate_timeout_seconds": aggregate_timeout,
        "aggregate_deadline_met": time.monotonic() <= deadline,
        "matrix_sha256": sha256_file(EVIDENCE / "matrix.json"),
        "archives": archives,
        "overlay_sha256": overlay_hash,
        "cell_count": len(cells),
        "cells": cells,
        "negative_controls": negative,
    }
    json_write(run_dir / "run.json", result)
    json_write(EVIDENCE / "matrix-results.json", result)
    # Recheck after report writes so a late cache recreation cannot be
    # mistaken for a clean retained evidence bundle.
    remove_run_cache(cache_root)
    return result


def run_negative_controls(matrix: dict[str, object], scratch: Path, run_dir: Path, cache_root: Path, overlay_hash: str, deadline: float) -> dict[str, object]:
    controls_dir = run_dir / "negative-controls"
    module_root = scratch / "go-agent-runtime"
    if not module_root.is_dir():
        raise RuntimeError("missing go-agent-runtime module for negative controls")
    base_cell = {
        "id": "negative",
        "env": {},
    }
    env = environment_for(matrix, base_cell, PLANNING_MAIN, controls_dir / "unused-trace.jsonl", cache_root, overlay_hash)
    controls: dict[str, object] = {}
    probe_host = dict(os.environ)
    probe_host["C52_AMBIENT_SENTINEL"] = "must-not-forward"
    probe_env = environment_for(matrix, base_cell, PLANNING_MAIN, controls_dir / "unused-trace.jsonl", cache_root, overlay_hash, probe_host)
    allowed_keys = FIXED_ENV_KEYS | MATRIX_ENV_KEYS | RUNNER_ENV_KEYS
    controls["environment_allowlist"] = {
        "sentinel_key": "C52_AMBIENT_SENTINEL",
        "sentinel_forwarded": "C52_AMBIENT_SENTINEL" in probe_env,
        "unexpected_keys": sorted(set(probe_env) - allowed_keys),
        "rejected": "C52_AMBIENT_SENTINEL" not in probe_env and not (set(probe_env) - allowed_keys),
    }
    if time.monotonic() < deadline:
        zero_dir = controls_dir / "zero-test-selection"
        zero_command = ["go", "test", "-json", "-tags=nomicrophone", "-count=1", "-run", "^C52NoSuchTest$", "./services/rooms/internal/lifecycle"]
        zero = run_bounded(zero_command, module_root, env, 45, int(matrix.get("output_cap_bytes", DEFAULT_OUTPUT_CAP)), zero_dir)
        zero_parsed = parse_go_json(zero_dir / "stdout.log", ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"], "./services/rooms/internal/lifecycle", 1)
        controls["zero_test_selection"] = {"process": zero, "selection": zero_parsed, "rejected": int(zero_parsed["required_test_run_counts"]["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"]) == 0}
    if time.monotonic() < deadline:
        wrong_dir = controls_dir / "wrong-package-selection"
        wrong_command = ["go", "test", "-json", "-tags=nomicrophone", "-count=1", "-run", "^TestRunnerRoutesTypedLivenessFaultAndPreservesPeer$", "./services/session/internal/live"]
        wrong = run_bounded(wrong_command, module_root, env, 45, int(matrix.get("output_cap_bytes", DEFAULT_OUTPUT_CAP)), wrong_dir)
        wrong_parsed = parse_go_json(wrong_dir / "stdout.log", ["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"], "./services/session/internal/live", 1)
        controls["wrong_test_selection"] = {"process": wrong, "selection": wrong_parsed, "rejected": int(wrong_parsed["required_test_run_counts"]["TestRunnerRoutesTypedLivenessFaultAndPreservesPeer/provider_timeout"]) == 0}
    if time.monotonic() < deadline:
        survivor_dir = controls_dir / "survivor-process"
        script = "import os,time; child=os.fork(); time.sleep(30) if child==0 else time.sleep(30)"
        survivor_command = [sys.executable, "-c", script]
        survivor = run_bounded(survivor_command, module_root, env, 0.4, 16384, survivor_dir)
        survivor_cleanup = survivor.get("cleanup", {}) if isinstance(survivor.get("cleanup"), dict) else {}
        controls["survivor_process"] = {
            "process": survivor,
            "rejected": (
                survivor.get("timed_out") is True
                and survivor_cleanup.get("group_survivor") is False
                and "term_error" in survivor_cleanup
                and "kill_error" in survivor_cleanup
            ),
        }
    if time.monotonic() < deadline:
        normal_dir = controls_dir / "normal-exit-survivor"
        script = "import os,time; child=os.fork(); (os.close(1), os.close(2), time.sleep(30)) if child==0 else os._exit(0)"
        normal_command = [sys.executable, "-c", script]
        normal = run_bounded(normal_command, module_root, env, 5.0, 16384, normal_dir)
        cleanup = normal.get("cleanup", {}) if isinstance(normal.get("cleanup"), dict) else {}
        controls["normal_exit_survivor"] = {
            "process": normal,
            "rejected": (
                normal.get("exit_code") == 0
                and cleanup.get("reason") == "parent_exit"
                and cleanup.get("term_sent") is True
                and cleanup.get("group_survivor") is False
                and normal.get("reader_survivor") is False
                and "term_error" in cleanup
                and "kill_error" in cleanup
                and cleanup.get("term_error") is None
                and cleanup.get("kill_error") is None
            ),
        }
    result = {
        "schema": "audio-runtime-c52-negative-controls-v2",
        "controls": controls,
        "all_rejected": all(bool(value.get("rejected")) for value in controls.values() if isinstance(value, dict)) and len(controls) == 5,
    }
    json_write(controls_dir / "negative-controls.json", result)
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    parser.add_argument("--aggregate-timeout", type=float, default=900)
    parser.add_argument("--repo-root", type=Path, default=None)
    parser.add_argument("--run-id", default=None)
    args = parser.parse_args()
    if args.aggregate_timeout <= 0 or args.aggregate_timeout > MAX_AGGREGATE_SECONDS:
        raise SystemExit("aggregate timeout must be positive and at most 900 seconds")
    matrix = json.loads(args.matrix.read_text(encoding="utf-8"))
    if not matrix.get("frozen"):
        raise SystemExit("matrix.json must be frozen before execution")
    repo = args.repo_root.resolve() if args.repo_root else Path(git_output(Path.cwd(), "rev-parse", "--show-toplevel"))
    dirty = allowed_worktree_dirty(repo)
    owned_prefix = "docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/"
    outside = [path for path in dirty if not path.startswith(owned_prefix)]
    if outside:
        raise SystemExit(f"unexpected dirty paths outside C52 ownership: {outside}")
    run_id = args.run_id or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    run_dir = EVIDENCE / "runs" / run_id
    if run_dir.exists():
        raise SystemExit(f"run already exists; first result is preserved: {run_dir}")
    run_dir.mkdir(parents=True, exist_ok=False)
    result = run_matrix(matrix, repo, run_dir, args.aggregate_timeout)
    print(json.dumps({"run": str(run_dir.relative_to(EVIDENCE)), "cells": result["cell_count"], "duration_seconds": result["duration_seconds"], "negative_controls": result["negative_controls"]["all_rejected"]}, sort_keys=True))
    return 0 if result["aggregate_deadline_met"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
