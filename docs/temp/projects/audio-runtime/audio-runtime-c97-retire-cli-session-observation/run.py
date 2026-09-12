#!/usr/bin/env python3
"""Bounded, credential-free C97 replay and continuation evidence runner."""

from __future__ import annotations

import argparse
import copy
import errno
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import threading
import time


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=OWNED_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
PEER_ROOT = REPO_ROOT.parent / "audio-runtime-c23-long-session-tool-characterization"
PEER_TASK_ROOT = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization"
CAPTURE = PEER_TASK_ROOT / "artifacts/runs/20260910T234434Z-shipped-regressions/test6.session.json"
CONFIG = PEER_TASK_ROOT / "artifacts/runs/20260910T234434Z-shipped-regressions/config"
SHIPPED_MANIFEST = PEER_TASK_ROOT / "shipped-regressions.json"
INPUT_FIXTURE = PEER_TASK_ROOT / "fixtures.json"
SOURCE_AUDIO = REPO_ROOT / "agent-cli/internal/transport/cli/testdata/test6-openai-barge-in.base64"
ARTIFACT_ROOT = OWNED_ROOT / "artifacts"
EVIDENCE_ROOT = OWNED_ROOT / "evidence"
YUI = ARTIFACT_ROOT / "bin/yui"

MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 300.0
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_RETAINED_DISK_BYTES = 8 * 1024 * 1024
EXPECTED_AUDIO_BYTES = 9120
EXPECTED_AUDIO_SHA256 = "9495487706f4a3001922c10d4dd496e7cd29fff9815a76f2f842f8e970725cc9"
EXPECTED_SOURCE_AUDIO_SHA256 = "6bb5a07350bbe1a874b4a28fcc79f7575f272a38a6338aa223c6fa9a99e6b5c6"
EXPECTED_EVENT_TYPES = [
    "session.update",
    "session.created",
    "conversation.item.create",
    "response.create",
    "response.created",
    "response.output_audio.delta",
    "input_audio_buffer.speech_started",
    "conversation.item.truncate",
    "conversation.item.truncated",
    "response.output_audio.done",
    "response.done",
    "response.created",
    "response.output_audio.delta",
    "response.output_audio.done",
    "response.done",
]
AUDIO_TOOL_CONTINUATION_TEST = (
    "^TestAgentBinaryNaturalCloseDrainsRemoteDevicePCM$/"
    "provider_close_tool_continuation$"
)


class EvidenceError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_tree(path: Path) -> str:
    digest = hashlib.sha256()
    for child in sorted(path.rglob("*")):
        if child.is_file() and not child.is_symlink():
            digest.update(str(child.relative_to(path)).encode())
            digest.update(sha256_file(child).encode())
    return digest.hexdigest()


def load_json(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing JSON input: {path}")
    try:
        value = json.loads(path.read_text())
    except json.JSONDecodeError as error:
        raise EvidenceError(f"invalid JSON input {path}: {error}") from error
    require(isinstance(value, dict), f"JSON input is not an object: {path}")
    return value


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def git_value(*args: str, cwd: Path = REPO_ROOT) -> str:
    return subprocess.run(
        ["git", *args], cwd=cwd, check=True, capture_output=True, text=True
    ).stdout.strip()


def is_ancestor(ancestor: str, descendant: str) -> bool:
    return (
        subprocess.run(
            ["git", "merge-base", "--is-ancestor", ancestor, descendant],
            cwd=REPO_ROOT,
            check=False,
        ).returncode
        == 0
    )


def owned_label(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(OWNED_ROOT.resolve()))
    except ValueError:
        try:
            return str(path.resolve().relative_to(REPO_ROOT.resolve()))
        except ValueError:
            return str(path)


def peer_snapshot() -> dict:
    require(PEER_ROOT.is_dir(), f"missing C23 predecessor worktree: {PEER_ROOT}")
    status = git_value("status", "--porcelain", "--untracked-files=all", cwd=PEER_ROOT)
    require(not status, "C23 predecessor worktree is not clean")
    return {
        "worktree": str(PEER_ROOT),
        "branch": git_value("branch", "--show-current", cwd=PEER_ROOT),
        "head": git_value("rev-parse", "HEAD", cwd=PEER_ROOT),
        "status": "clean",
        "capture_sha256": sha256_file(CAPTURE),
        "config_sha256": sha256_tree(CONFIG),
        "fixture_sha256": sha256_file(INPUT_FIXTURE),
        "manifest_sha256": sha256_file(SHIPPED_MANIFEST),
    }


def validate_shipped_inputs() -> tuple[dict, dict]:
    capture = load_json(CAPTURE)
    manifest = load_json(SHIPPED_MANIFEST)
    require(CONFIG.is_dir(), f"missing replay config directory: {CONFIG}")
    require(INPUT_FIXTURE.is_file(), f"missing C23 input fixture: {INPUT_FIXTURE}")
    require(SOURCE_AUDIO.is_file(), f"missing source audio fixture: {SOURCE_AUDIO}")
    records = capture.get("records")
    require(isinstance(records, list), "shipped capture has no records list")
    types = [record.get("payload", {}).get("type") for record in records]
    sequences = [record.get("sequence") for record in records]
    require(types == EXPECTED_EVENT_TYPES, "shipped capture event order changed")
    require(sequences == list(range(1, len(records) + 1)), "shipped capture sequence changed")
    expected = manifest.get("audio_output", {})
    require(expected.get("bytes") == EXPECTED_AUDIO_BYTES, "shipped audio byte oracle changed")
    require(
        expected.get("expected_sha256") == EXPECTED_AUDIO_SHA256,
        "shipped audio SHA oracle changed",
    )
    require(
        sha256_file(SOURCE_AUDIO) == EXPECTED_SOURCE_AUDIO_SHA256,
        "source audio fixture changed",
    )
    return capture, manifest


def go_mod_cache() -> str:
    return subprocess.run(
        ["go", "env", "GOMODCACHE"], check=True, capture_output=True, text=True
    ).stdout.strip()


def child_environment(
    source_revision: str,
    candidate_revision: str,
    run_root: Path,
    gowork: str,
    extras: dict[str, str] | None = None,
) -> dict[str, str]:
    for path in (run_root / "home", run_root / "tmp", run_root / "gocache"):
        path.mkdir(parents=True, exist_ok=True)
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(run_root / "home"),
        "TMPDIR": str(run_root / "tmp"),
        "GOCACHE": str(run_root / "gocache"),
        "GOMODCACHE": go_mod_cache(),
        "GOWORK": gowork,
        "LANG": "C",
        "LC_ALL": "C",
        "C97_SOURCE_REVISION": source_revision,
        "C97_CANDIDATE_REVISION": candidate_revision,
    }
    if extras:
        environment.update(extras)
    return environment


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except OSError as error:
        return error.errno != errno.ESRCH
    return True


def run_child(
    command: list[str],
    label: str,
    cwd: Path,
    run_root: Path,
    source_revision: str,
    candidate_revision: str,
    timeout: float,
    gowork: str,
    extras: dict[str, str] | None = None,
) -> dict:
    require(0 < timeout <= MAX_CHILD_SECONDS, f"{label} exceeds the 60 second child bound")
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path = run_root / "stdout.log"
    stderr_path = run_root / "stderr.log"
    observed = {"stdout": 0, "stderr": 0}
    overflow = {"stdout": False, "stderr": False}

    def drain(stream, path: Path, name: str) -> None:
        try:
            with path.open("wb") as output:
                while True:
                    chunk = stream.read(8192)
                    if not chunk:
                        return
                    previous = observed[name]
                    observed[name] += len(chunk)
                    keep = max(0, min(len(chunk), MAX_CHILD_OUTPUT_BYTES - previous))
                    if keep:
                        output.write(chunk[:keep])
                    if observed[name] > MAX_CHILD_OUTPUT_BYTES:
                        overflow[name] = True
        except (OSError, ValueError):
            overflow[name] = True

    started = time.monotonic()
    process = None
    threads: list[threading.Thread] = []
    returncode: int | None = None
    timed_out = False
    term_sent = False
    kill_sent = False
    parent_reaped = False
    setup_error = ""
    before_alive = False
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=child_environment(
                source_revision, candidate_revision, run_root, gowork, extras
            ),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        before_alive = group_alive(process.pid)
        threads = [
            threading.Thread(
                target=drain, args=(process.stdout, stdout_path, "stdout"), daemon=True
            ),
            threading.Thread(
                target=drain, args=(process.stderr, stderr_path, "stderr"), daemon=True
            ),
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
                try:
                    returncode = process.wait(timeout=2)
                    parent_reaped = True
                except subprocess.TimeoutExpired:
                    returncode = process.poll()
    except OSError as error:
        setup_error = str(error)
    finally:
        for thread in threads:
            thread.join(timeout=2)

    elapsed_ms = int((time.monotonic() - started) * 1000)
    after_alive = group_alive(process.pid) if process is not None else False
    disk_bytes = sum(
        path.stat().st_size for path in (stdout_path, stderr_path) if path.is_file()
    )
    return {
        "label": label,
        "command": command,
        "cwd": owned_label(cwd),
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": owned_label(stdout_path),
        "stderr": owned_label(stderr_path),
        "stdout_bytes": observed["stdout"],
        "stderr_bytes": observed["stderr"],
        "output_bounded": not any(overflow.values()),
        "disk_bytes": disk_bytes,
        "disk_bounded": disk_bytes <= MAX_RETAINED_DISK_BYTES,
        "cleanup": {
            "parent_reaped": parent_reaped,
            "group_alive_before": before_alive,
            "group_alive_after": after_alive,
            "reader_threads_joined": not any(thread.is_alive() for thread in threads),
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "setup_error": setup_error,
        },
    }


def build_yui(source_revision: str, candidate_revision: str, root: Path) -> tuple[dict, str]:
    result = run_child(
        [
            "go",
            "build",
            "-mod=readonly",
            "-trimpath",
            "-o",
            str(YUI),
            "./agent-cli/cmd/yui",
        ],
        "build-source-pinned-yui",
        REPO_ROOT,
        root / "build",
        source_revision,
        candidate_revision,
        MAX_CHILD_SECONDS,
        str(REPO_ROOT / "go.work"),
    )
    require(result["returncode"] == 0 and result["cleanup"]["parent_reaped"], f"YUI build failed: {result}")
    require(YUI.is_file(), "YUI build produced no binary")
    return result, sha256_file(YUI)


def replay_command(yui: Path, capture: Path, output: Path) -> list[str]:
    return [
        str(yui),
        "-C",
        str(CONFIG),
        "session",
        "--replay",
        str(capture),
        "--replay-timing",
        "recorded",
        "--prompt",
        "test6 customer barge in",
        "--audio-out",
        str(output),
        "--no-terminal-tools",
    ]


def case_ordered(
    source_revision: str, candidate_revision: str, peer: dict, started: float, timeout: float
) -> None:
    capture, manifest = validate_shipped_inputs()
    root = ARTIFACT_ROOT / "ordered-observation-replay"
    output = root / "healthy-tail.pcm"
    output.unlink(missing_ok=True)
    build, yui_sha = build_yui(source_revision, candidate_revision, root)
    execution = run_child(
        replay_command(YUI, CAPTURE, output),
        "ordered-observation-replay",
        REPO_ROOT,
        root / "process",
        source_revision,
        candidate_revision,
        timeout,
        "off",
    )
    require(
        execution["returncode"] == 0
        and not execution["timed_out"]
        and execution["cleanup"]["parent_reaped"]
        and not execution["cleanup"]["group_alive_after"],
        f"ordered replay failed: {execution}",
    )
    require(output.is_file(), "ordered replay produced no PCM output")
    output_bytes = output.stat().st_size
    output_sha = sha256_file(output)
    require(
        output_bytes == EXPECTED_AUDIO_BYTES and output_sha == EXPECTED_AUDIO_SHA256,
        "ordered replay PCM oracle changed",
    )
    write_json(
        EVIDENCE_ROOT / "ordered-observation-replay.json",
        {
            "schema": "audio-runtime.c97.ordered-observation-replay.v1",
            "passed": True,
            "source_revision": source_revision,
            "candidate_revision": candidate_revision,
            "source_pinned": is_ancestor(source_revision, candidate_revision),
            "peer": peer,
            "build": {"sha256": yui_sha, "execution": build},
            "inputs": {
                "capture": owned_label(CAPTURE),
                "capture_sha256": sha256_file(CAPTURE),
                "config_sha256": sha256_tree(CONFIG),
                "fixture_sha256": sha256_file(INPUT_FIXTURE),
                "manifest_sha256": sha256_file(SHIPPED_MANIFEST),
                "source_audio_sha256": sha256_file(SOURCE_AUDIO),
                "ordered_event_types": [record["payload"]["type"] for record in capture["records"]],
                "record_count": len(capture["records"]),
                "manifest_audio_output": manifest["audio_output"],
            },
            "execution": execution,
            "audio_output": {
                "path": owned_label(output),
                "bytes": output_bytes,
                "sha256": output_sha,
            },
            "classification": {
                "provider_edge": "credential-free shipped replay",
                "audio_tool_interruption_order": "proved",
                "physical_device": "not_attempted",
                "acoustic": "not_attempted",
            },
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
        },
    )


def case_malformed(
    source_revision: str, candidate_revision: str, peer: dict, started: float, timeout: float
) -> None:
    capture, _ = validate_shipped_inputs()
    root = ARTIFACT_ROOT / "malformed-or-out-of-order-replay"
    mutated_path = root / "out-of-order.session.json"
    output = root / "unexpected.pcm"
    output.unlink(missing_ok=True)
    mutated = copy.deepcopy(capture)
    mutated["records"][2], mutated["records"][3] = (
        mutated["records"][3],
        mutated["records"][2],
    )
    root.mkdir(parents=True, exist_ok=True)
    mutated_path.write_text(json.dumps(mutated, indent=2, sort_keys=True) + "\n")
    build, yui_sha = build_yui(source_revision, candidate_revision, root)
    execution = run_child(
        replay_command(YUI, mutated_path, output),
        "malformed-or-out-of-order-replay",
        REPO_ROOT,
        root / "process",
        source_revision,
        candidate_revision,
        timeout,
        "off",
    )
    require(
        execution["returncode"] is not None
        and execution["returncode"] != 0
        and not execution["timed_out"]
        and execution["cleanup"]["parent_reaped"]
        and not execution["cleanup"]["group_alive_after"],
        f"out-of-order replay was not rejected: {execution}",
    )
    write_json(
        EVIDENCE_ROOT / "malformed-or-out-of-order-replay.json",
        {
            "schema": "audio-runtime.c97.malformed-or-out-of-order-replay.v1",
            "passed": True,
            "expected_outcome": "strict replay admission rejects swapped records",
            "source_revision": source_revision,
            "candidate_revision": candidate_revision,
            "source_pinned": is_ancestor(source_revision, candidate_revision),
            "peer": peer,
            "build": {"sha256": yui_sha, "execution": build},
            "mutation": {
                "kind": "out-of-order",
                "swapped_record_indexes": [2, 3],
                "integrity": "original digest retained intentionally; malformed input must be rejected",
                "capture_sha256": sha256_file(mutated_path),
            },
            "execution": execution,
            "classification": {
                "provider_edge": "credential-free replay admission",
                "physical_device": "not_attempted",
                "acoustic": "not_attempted",
            },
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
        },
    )


def case_audio_tool_continuation(
    source_revision: str, candidate_revision: str, peer: dict, started: float, timeout: float
) -> None:
    root = ARTIFACT_ROOT / "audio-tool-continuation"
    command = [
        "go",
        "test",
        "./test/integration",
        "-run",
        AUDIO_TOOL_CONTINUATION_TEST,
        "-count=1",
        "-timeout=60s",
    ]
    execution = run_child(
        command,
        "audio-tool-continuation",
        REPO_ROOT / "agent-cli",
        root / "process",
        source_revision,
        candidate_revision,
        timeout,
        str(REPO_ROOT / "go.work"),
        extras={"CGO_ENABLED": "0"},
    )
    require(
        execution["returncode"] == 0
        and not execution["timed_out"]
        and execution["cleanup"]["parent_reaped"]
        and not execution["cleanup"]["group_alive_after"],
        f"audio/tool continuation regression failed: {execution}",
    )
    write_json(
        EVIDENCE_ROOT / "audio-tool-continuation.json",
        {
            "schema": "audio-runtime.c97.audio-tool-continuation.v1",
            "passed": True,
            "source_revision": source_revision,
            "candidate_revision": candidate_revision,
            "source_pinned": is_ancestor(source_revision, candidate_revision),
            "peer": peer,
            "test": AUDIO_TOOL_CONTINUATION_TEST,
            "execution": execution,
            "credential_free": True,
            "classification": {
                "provider_edge": "local fixture-controlled WebSocket",
                "audio_tool_continuation": "proved",
                "physical_device": "not_attempted",
                "acoustic": "not_attempted",
            },
            "aggregate_elapsed_ms": int((time.monotonic() - started) * 1000),
        },
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--case",
        required=True,
        choices=[
            "ordered-observation-replay",
            "malformed-or-out-of-order-replay",
            "audio-tool-continuation",
        ],
    )
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    require(0 < args.child_timeout <= MAX_CHILD_SECONDS, "child timeout must be at most 60 seconds")
    require(0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS, "aggregate timeout must be at most 300 seconds")
    started = time.monotonic()
    source_revision = git_value("rev-parse", "origin/main")
    candidate_revision = git_value("rev-parse", "HEAD")
    require(is_ancestor(source_revision, candidate_revision), "candidate does not contain fetched origin/main")
    peer = peer_snapshot()
    if args.case == "ordered-observation-replay":
        case_ordered(source_revision, candidate_revision, peer, started, args.child_timeout)
    elif args.case == "malformed-or-out-of-order-replay":
        case_malformed(source_revision, candidate_revision, peer, started, args.child_timeout)
    else:
        case_audio_tool_continuation(
            source_revision, candidate_revision, peer, started, args.child_timeout
        )
    elapsed = time.monotonic() - started
    require(elapsed <= args.aggregate_timeout, f"aggregate timeout exceeded: {elapsed:.3f}s")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (EvidenceError, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print(f"verification failure: {error}")
        raise SystemExit(1)
