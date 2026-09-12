#!/usr/bin/env python3
"""Run the two bounded C108 public executable checks.

The replay uses the already shipped credential-free fixture.  The negative
copies that fixture in a temporary owned run directory and changes one audio
delta to a one-byte PCM16 payload, which must be rejected before playback.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import pathlib
import signal
import shutil
import subprocess
import tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
EXPECTED_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
CHILD_TIMEOUT_SECONDS = 60
OUTPUT_CAP = 512 * 1024


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def safe_environment() -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "authorization", "api")
    environment = {
        key: value
        for key, value in os.environ.items()
        if not any(part in key.lower() for part in blocked)
    }
    environment.update({"NO_COLOR": "1", "CI": "1"})
    return environment


def process_group_gone(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def run_command(label: str, argv: list[str], cwd: pathlib.Path, output_dir: pathlib.Path, timeout: int) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    stdout_path = output_dir / f"{label}.stdout"
    stderr_path = output_dir / f"{label}.stderr"
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=safe_environment(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    output_limited = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        process.terminate()
        try:
            stdout, stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            stdout, stderr = process.communicate(timeout=3)
        stdout = (exc.stdout or b"") + (stdout or b"")
        stderr = (exc.stderr or b"") + (stderr or b"")
    stdout = stdout or b""
    stderr = stderr or b""
    if len(stdout) > OUTPUT_CAP:
        stdout = stdout[:OUTPUT_CAP]
        output_limited = True
    if len(stderr) > OUTPUT_CAP:
        stderr = stderr[:OUTPUT_CAP]
        output_limited = True
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    stdout_ref = stdout_path.relative_to(HERE) if stdout_path.is_relative_to(HERE) else stdout_path
    stderr_ref = stderr_path.relative_to(HERE) if stderr_path.is_relative_to(HERE) else stderr_path
    return {
        "label": label,
        "argv": argv,
        "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
        "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "stdout_path": str(stdout_ref),
        "stderr_path": str(stderr_ref),
        "stdout_sha256": sha256(stdout_path),
        "stderr_sha256": sha256(stderr_path),
        "process_group_gone": process_group_gone(process.pid),
    }


def output_text(run: dict[str, Any]) -> str:
    return "\n".join(
        (HERE / run[key]).read_text(encoding="utf-8", errors="replace")
        for key in ("stdout_path", "stderr_path")
    )


def build_yui(build_dir: pathlib.Path) -> tuple[pathlib.Path, dict[str, Any]]:
    build_dir.mkdir(parents=True, exist_ok=True)
    binary = build_dir / "yui"
    result = run_command(
        "build-yui",
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"],
        ROOT,
        build_dir,
        300,
    )
    if result["exit_code"] != 0 or result["timed_out"] or not binary.exists():
        raise RuntimeError(f"shipped executable build failed: {result}")
    result["binary_sha256"] = sha256(binary)
    result["binary_bytes"] = binary.stat().st_size
    return binary, result


def replay(binary: pathlib.Path, run_dir: pathlib.Path, build: dict[str, Any]) -> dict[str, Any]:
    if run_dir.exists():
        shutil.rmtree(run_dir)
    (run_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record_dir = run_dir / "tool-record"
    audio_out = run_dir / "audio.wav"
    result = run_command(
        "yui-replay",
        [
            str(binary),
            "session",
            "--replay",
            str(FIXTURE),
            "--audio-out",
            str(audio_out),
            "--record-dir",
            str(record_dir),
            "--trace-audio",
            "--workdir",
            str(run_dir),
            "--allow-path",
            str(run_dir),
        ],
        run_dir,
        run_dir,
        CHILD_TIMEOUT_SECONDS,
    )
    combined = output_text(result)
    if result["exit_code"] != 0 or result["timed_out"] or not result["process_group_gone"]:
        raise RuntimeError(f"credential-free replay did not shut down cleanly: {result}")
    if "PROBE_TOOL_MARKER_9182" not in combined or "strict replay continuation" not in combined:
        raise RuntimeError("credential-free replay lost the pinned tool/response markers")
    raw_audio = record_dir / "audio" / "out-000.pcm"
    manifest_path = record_dir / "manifest.json"
    session_log = record_dir / "session-log.jsonl"
    marker = run_dir / "evidence" / "runs" / "exec-invocations-v4.log"
    if not raw_audio.exists() or raw_audio.stat().st_size != 4800 or sha256(raw_audio) != EXPECTED_PCM_SHA256:
        raise RuntimeError("credential-free replay PCM receipt changed")
    if not marker.exists() or not manifest_path.exists() or not session_log.exists() or not audio_out.exists() or audio_out.stat().st_size <= 44:
        raise RuntimeError("credential-free replay omitted a required observable effect")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    terminal = manifest.get("terminal", {})
    if terminal.get("reason") != "fixture_complete" or terminal.get("classification") != "provider_close":
        raise RuntimeError(f"credential-free replay terminal evidence changed: {terminal}")
    turns = [json.loads(line) for line in session_log.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(turns) != 1 or turns[0].get("input", {}).get("text") != "probe PROBE_TOOL_MARKER_9182" or turns[0].get("response", {}).get("text") != "strict replay continuation":
        raise RuntimeError("credential-free replay session log changed")
    return {
        "schema_version": "c108-public-replay-v1",
        "case": "replay",
        "source_revision": BUILD_SOURCE,
        "candidate_revision": git("rev-parse", "HEAD"),
        "binary_sha256": build["binary_sha256"],
        "binary_bytes": build["binary_bytes"],
        "fixture": str(FIXTURE.relative_to(ROOT)),
        "fixture_sha256": sha256(FIXTURE),
        "command": result["argv"],
        "process": result,
        "raw_pcm_bytes": raw_audio.stat().st_size,
        "raw_pcm_sha256": sha256(raw_audio),
        "audio_output_bytes": audio_out.stat().st_size,
        "audio_output_sha256": sha256(audio_out),
        "manifest_sha256": sha256(manifest_path),
        "session_log_sha256": sha256(session_log),
        "terminal": terminal,
        "observable_effects": ["tool_marker", "strict_replay_continuation", "audio_pcm_receipt", "audio_wav_receipt", "fixture_complete", "provider_close"],
        "proof_level": "SOFTWARE_REPLAY",
        "queue_admission": "not claimed as playback consumption",
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "credential_free": True,
        "realtime_used": False,
    }


def mutate_fixture(destination: pathlib.Path) -> dict[str, Any]:
    document = json.loads(FIXTURE.read_text(encoding="utf-8"))
    mutated = False
    for record in document.get("records", []):
        payload = record.get("payload", {})
        if record.get("type") == "response.output_audio.delta" and isinstance(payload.get("delta"), str):
            payload["delta"] = base64.b64encode(b"\x01").decode("ascii")
            mutated = True
            break
    if not mutated:
        raise RuntimeError("fixture has no audio delta to mutate")
    destination.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {"mutation": "first response.output_audio.delta replaced with one-byte PCM16", "fixture_sha256": sha256(FIXTURE), "mutated_fixture_sha256": sha256(destination)}


def malformed_truncated(binary: pathlib.Path, run_dir: pathlib.Path, build: dict[str, Any]) -> dict[str, Any]:
    if run_dir.exists():
        shutil.rmtree(run_dir)
    run_dir.mkdir(parents=True, exist_ok=True)
    mutated_fixture = run_dir / "malformed.session.json"
    mutation = mutate_fixture(mutated_fixture)
    record_dir = run_dir / "tool-record"
    audio_out = run_dir / "audio.wav"
    result = run_command(
        "yui-malformed-truncated",
        [
            str(binary),
            "session",
            "--replay",
            str(mutated_fixture),
            "--audio-out",
            str(audio_out),
            "--record-dir",
            str(record_dir),
            "--trace-audio",
            "--workdir",
            str(run_dir),
            "--allow-path",
            str(run_dir),
        ],
        run_dir,
        run_dir,
        CHILD_TIMEOUT_SECONDS,
    )
    combined = output_text(result)
    raw_audio = record_dir / "audio" / "out-000.pcm"
    rejection_literals = ["odd", "PCM16", "audio", "replay"]
    if result["exit_code"] == 0 or result["timed_out"] or not result["process_group_gone"]:
        raise RuntimeError(f"malformed/truncated replay did not fail closed: {result}")
    if not any(literal.lower() in combined.lower() for literal in rejection_literals):
        raise RuntimeError("malformed/truncated replay omitted its bounded audio rejection diagnostic")
    if raw_audio.exists() and raw_audio.stat().st_size:
        raise RuntimeError("malformed/truncated replay accepted a PCM playback receipt")
    return {
        "schema_version": "c108-public-malformed-truncated-v1",
        "case": "malformed-truncated",
        "source_revision": BUILD_SOURCE,
        "candidate_revision": git("rev-parse", "HEAD"),
        "binary_sha256": build["binary_sha256"],
        "binary_bytes": build["binary_bytes"],
        "fixture": str(FIXTURE.relative_to(ROOT)),
        **mutation,
        "command": result["argv"],
        "process": result,
        "rejection_literals": rejection_literals,
        "accepted_pcm_receipt": False,
        "clean_shutdown": result["process_group_gone"],
        "proof_level": "PROCESS_SMOKE",
        "queue_admission": "none observed",
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "credential_free": True,
        "realtime_used": False,
    }


def main() -> int:
    global BUILD_SOURCE
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--case", choices=("replay", "malformed-truncated"), required=True)
    args = parser.parse_args()
    BUILD_SOURCE = args.source
    if not FIXTURE.exists():
        raise SystemExit(f"missing pinned fixture: {FIXTURE}")
    if git("diff", "--quiet", args.source, "--", "agent-cli", "go-agent-loop", "go-agent-runtime", "go-llm-gateway") != "":
        raise SystemExit("production source differs from requested source revision")

    run_root = HERE / "runs" / "public"
    run_root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="c108-yui-") as temp:
        binary, build = build_yui(pathlib.Path(temp))
        if args.case == "replay":
            report = replay(binary, run_root / "replay", build)
            target = run_root / "replay.json"
        else:
            report = malformed_truncated(binary, run_root / "malformed-truncated", build)
            target = run_root / "malformed-truncated.json"
    target.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    aggregate = run_root / "report.json"
    existing: dict[str, Any] = {}
    if aggregate.exists():
        existing = json.loads(aggregate.read_text(encoding="utf-8"))
    existing[args.case] = report
    existing["schema_version"] = "c108-public-checks-v1"
    aggregate.write_text(json.dumps(existing, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "ok", "case": args.case, "report": str(target.relative_to(HERE)), "binary_sha256": report["binary_sha256"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
