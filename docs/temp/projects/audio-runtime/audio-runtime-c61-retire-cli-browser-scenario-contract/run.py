#!/usr/bin/env python3
"""Run the bounded, credential-free shipped YUI replay for C61."""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import signal
import subprocess
import tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
RUNS = HERE / "runs"
FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
OUTPUT_CAP = 2 * 1024 * 1024
REPLAY_QUOTA = 16 * 1024 * 1024
COMMAND_TIMEOUT = 180


class ReplayFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_environment() -> dict[str, str]:
    markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    return {
        name: value
        for name, value in os.environ.items()
        if not any(marker in name.upper() for marker in markers)
    }


def process_group_gone(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except OSError as exc:
        return exc.errno == 3
    return False


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired as exc:
            raise ReplayFailure("replay process group did not terminate after SIGKILL") from exc


def run_command(label: str, argv: list[str], cwd: pathlib.Path) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=safe_environment(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=COMMAND_TIMEOUT)
    except subprocess.TimeoutExpired as exc:
        terminate_group(process)
        raise ReplayFailure(f"{label} exceeded {COMMAND_TIMEOUT}s") from exc
    if not process_group_gone(process.pid):
        terminate_group(process)
        raise ReplayFailure(f"{label} left a live process group")
    if len(stdout) > OUTPUT_CAP or len(stderr) > OUTPUT_CAP:
        raise ReplayFailure(f"{label} exceeded the {OUTPUT_CAP}-byte output cap")
    result = {
        "label": label,
        "argv": argv,
        "exit_code": process.returncode,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
        "stdout_sha256": hashlib.sha256(stdout).hexdigest(),
        "stderr_sha256": hashlib.sha256(stderr).hexdigest(),
        "process_group_gone": True,
        "credential_free_environment": True,
    }
    if process.returncode != 0:
        raise ReplayFailure(f"{label} failed with exit code {process.returncode}: {stderr[-2000:].decode(errors='replace')}")
    result["stdout"] = stdout
    result["stderr"] = stderr
    return result


def owned_bytes(path: pathlib.Path) -> int:
    return sum(item.stat().st_size for item in path.rglob("*") if item.is_file())


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ReplayFailure(message)


def replay() -> dict[str, Any]:
    require(FIXTURE.is_file(), f"replay fixture is missing: {FIXTURE}")
    source_revision = run_command("source-revision", ["git", "rev-parse", "HEAD"], ROOT)
    source_revision_text = source_revision["stdout"].decode().strip()
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c61-yui-") as temporary:
        temporary_root = pathlib.Path(temporary)
        binary = temporary_root / "yui"
        build = run_command("build-yui", ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"], ROOT)
        binary_hash = sha256(binary)
        top_help = run_command("yui-help", [str(binary), "--help"], ROOT)
        session_help = run_command("yui-session-help", [str(binary), "session", "--help"], ROOT)
        top_output = top_help["stdout"] + top_help["stderr"]
        session_output = session_help["stdout"] + session_help["stderr"]
        require(all(literal.encode() in top_output for literal in ("Available Commands:", "session", "--workdir", "--allow-path")), "top-level YUI help lost a required literal")
        require(all(literal.encode() in session_output for literal in ("Usage:", "--replay", "--record-dir", "--audio-in-turn", "--trace-audio")), "session YUI help lost a required literal")

        replay_dir = temporary_root / "replay"
        (replay_dir / "evidence" / "runs").mkdir(parents=True)
        record_dir = replay_dir / "tool-record"
        replay_result = run_command(
            "yui-local-replay",
            [
                str(binary), "session", "--replay", str(FIXTURE),
                "--audio-out", str(replay_dir / "audio.wav"),
                "--record-dir", str(record_dir), "--trace-audio",
                "--workdir", str(replay_dir), "--allow-path", str(replay_dir),
            ],
            replay_dir,
        )
        combined_output = replay_result["stdout"] + replay_result["stderr"]
        require(b"PROBE_TOOL_MARKER_9182" in combined_output, "replay lost the credential-free tool marker")
        require(b"strict replay continuation" in combined_output, "replay lost the strict continuation marker")
        forbidden = (b"api_key", b"authorization:", b"bearer ", b"sk-", b"client_secret", b"password")
        require(not any(marker in combined_output.lower() for marker in forbidden), "replay output contained a credential marker")

        audio = replay_dir / "audio.wav"
        session_log = record_dir / "session-log.jsonl"
        raw_audio = record_dir / "audio" / "out-000.pcm"
        manifest_path = record_dir / "manifest.json"
        require(audio.is_file() and audio.stat().st_size > 44, "replay did not produce a playable WAV")
        require(session_log.is_file() and raw_audio.is_file() and manifest_path.is_file(), "replay omitted a required recording artifact")
        require(owned_bytes(replay_dir) <= REPLAY_QUOTA, "replay artifacts exceeded the task-local quota")
        lines = [json.loads(line) for line in session_log.read_text(encoding="utf-8").splitlines() if line.strip()]
        require(len(lines) == 1, "replay session log did not contain exactly one turn")
        require(lines[0].get("input", {}).get("text") == "probe PROBE_TOOL_MARKER_9182", "replay input text changed")
        require(lines[0].get("response", {}).get("text") == "strict replay continuation", "replay response text changed")
        require(raw_audio.stat().st_size == 4800, "replay PCM length changed")
        require(sha256(raw_audio) == "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502", "replay PCM hash changed")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        terminal = manifest.get("terminal", {})
        require(terminal.get("reason") == "fixture_complete" and terminal.get("classification") == "provider_close", "replay terminal manifest changed")

        report = {
            "schema_version": "c61-shipped-yui-browser-audio-tool-replay-v1",
            "source_revision": source_revision_text,
            "fixture": str(FIXTURE.relative_to(ROOT)),
            "fixture_sha256": sha256(FIXTURE),
            "binary_sha256": binary_hash,
            "build": {key: value for key, value in build.items() if key not in ("stdout", "stderr")},
            "top_level_help": {key: value for key, value in top_help.items() if key not in ("stdout", "stderr")},
            "session_help": {key: value for key, value in session_help.items() if key not in ("stdout", "stderr")},
            "replay": {key: value for key, value in replay_result.items() if key not in ("stdout", "stderr")},
            "audio_sha256": sha256(audio),
            "audio_bytes": audio.stat().st_size,
            "recorded_pcm_sha256": sha256(raw_audio),
            "recorded_pcm_bytes": raw_audio.stat().st_size,
            "session_log_sha256": sha256(session_log),
            "terminal_manifest": terminal,
            "replay_owned_bytes": owned_bytes(replay_dir),
            "replay_owned_quota": REPLAY_QUOTA,
            "realtime": "not used; local credential-free fixture replay only",
            "result_classification": "SOFTWARE_LOCAL_PROCESS_ONLY",
        }
    RUNS.mkdir(parents=True, exist_ok=True)
    (RUNS / "shipped-yui-browser-audio-tool-replay.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def main() -> int:
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True)
    args = parser.parse_args()
    if args.mode != "shipped-yui-browser-audio-tool-replay":
        print(json.dumps({"status": "failed", "error": f"unknown mode: {args.mode}"}))
        return 1
    try:
        report = replay()
    except (OSError, ReplayFailure, ValueError) as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(exc)}))
        return 1
    print(json.dumps({"status": "passed", "mode": args.mode, "source_revision": report["source_revision"], "binary_sha256": report["binary_sha256"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
