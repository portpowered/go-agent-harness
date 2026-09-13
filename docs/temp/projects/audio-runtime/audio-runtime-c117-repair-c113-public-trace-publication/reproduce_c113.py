#!/usr/bin/env python3
"""Reproduce the immutable C113 public trace publication gap.

The command runs one hash-pinned executable against the archived credential-free
fixture.  It records only bounded metadata and hashes in the owned evidence
directory; the replay outputs live in a disposable private directory.
"""

from __future__ import annotations

import argparse
import errno
import hashlib
import json
import os
import pathlib
import selectors
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
C113_SOURCE_REVISION = "3963bc3566da24f8214634c17a9d0f79a6724171"
C113_ARTIFACT_SHA256 = "27896a6907f5c8e02e185acf9338f07d38e56ebe21896d0b1a740ecde1f2a20d"
C113_SOURCE_ARCHIVE_SHA256 = "2df6a0da4b14c40b6bd85cdca4d8cc846377ff8b9962941d1bbbaec2e332b201"
FIXTURE_RELATIVE = "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
RAW_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
MARKER = "PROBE_TOOL_MARKER_9182"
CONTINUATION = "strict replay continuation"
OUTPUT_CAP = 512 * 1024


class ReproductionFailure(RuntimeError):
    pass


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def resolve_external(path: pathlib.Path) -> pathlib.Path:
    if path.is_absolute():
        return path.resolve()
    local = (ROOT / path).resolve()
    if local.exists():
        return local
    factory = (FACTORY_ROOT / path).resolve()
    if factory.exists():
        return factory
    return local


def git_head() -> str | None:
    result = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, capture_output=True)
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def safe_environment(home: pathlib.Path) -> dict[str, str]:
    blocked = ("key", "token", "secret", "password", "credential", "auth")
    environment = {
        name: value
        for name, value in os.environ.items()
        if not any(term in name.lower() for term in blocked)
    }
    environment["HOME"] = str(home)
    environment["XDG_CONFIG_HOME"] = str(home / "config")
    environment.pop("OPENAI_API_KEY", None)
    environment.pop("ANTHROPIC_API_KEY", None)
    return environment


def terminate_group(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
    else:
        process.terminate()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        if os.name == "posix":
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                return
        else:
            process.kill()
        process.wait(timeout=2)


def process_group_gone(process: subprocess.Popen[bytes]) -> bool:
    if os.name != "posix":
        return process.poll() is not None
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def run_bounded(argv: list[str], cwd: pathlib.Path, environment: dict[str, str], timeout: int) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=os.name == "posix",
    )
    selector = selectors.DefaultSelector()
    streams: dict[int, tuple[str, bytearray]] = {}
    assert process.stdout is not None
    assert process.stderr is not None
    active: set[int] = set()
    for name, stream in (("stdout", process.stdout), ("stderr", process.stderr)):
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ, name)
        streams[stream.fileno()] = (name, bytearray())
        active.add(stream.fileno())
    timed_out = False
    output_limited = False
    deadline = started + timeout
    while active:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            terminate_group(process)
            break
        events = selector.select(remaining)
        if not events:
            timed_out = True
            terminate_group(process)
            break
        for key, _ in events:
            fd = key.fileobj.fileno()
            name, buffer = streams[fd]
            try:
                chunk = os.read(fd, 16 * 1024)
            except BlockingIOError:
                continue
            if not chunk:
                selector.unregister(key.fileobj)
                active.discard(fd)
                continue
            buffer.extend(chunk)
            if len(buffer) > OUTPUT_CAP:
                output_limited = True
                terminate_group(process)
                break
        if timed_out or output_limited:
            break
    if streams:
        for key in list(selector.get_map().values()):
            selector.unregister(key.fileobj)
    selector.close()
    if process.poll() is None:
        terminate_group(process)
    returncode = process.wait(timeout=2)
    elapsed = round(time.monotonic() - started, 3)
    return {
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": returncode,
        "elapsed_seconds": elapsed,
        "timed_out": timed_out,
        "output_limited": output_limited,
        "process_group_gone": process_group_gone(process),
        "stdout": bytes(streams[process.stdout.fileno()][1]).decode("utf-8", errors="replace") if process.stdout else "",
        "stderr": bytes(streams[process.stderr.fileno()][1]).decode("utf-8", errors="replace") if process.stderr else "",
    }


def artifact_hashes(record_dir: pathlib.Path) -> tuple[dict[str, Any], dict[str, Any]]:
    manifest_path = record_dir / "manifest.json"
    session_log_path = record_dir / "session-log.jsonl"
    if not manifest_path.is_file() or not session_log_path.is_file():
        raise ReproductionFailure("recording manifest or session log is missing")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ReproductionFailure(f"recording manifest is invalid JSON: {exc}") from exc
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or len(artifacts) != 5:
        raise ReproductionFailure(f"recording manifest artifact count = {len(artifacts) if isinstance(artifacts, list) else 'invalid'}, want 5")
    observed = []
    for item in artifacts:
        relative = pathlib.Path(str(item.get("path", "")))
        if relative.is_absolute() or ".." in relative.parts:
            raise ReproductionFailure("recording manifest contains an unsafe artifact path")
        path = record_dir / relative
        if not path.is_file():
            raise ReproductionFailure(f"recording manifest artifact is missing: {relative}")
        actual = sha256_file(path)
        if actual != item.get("sha256"):
            raise ReproductionFailure(f"recording manifest hash mismatch: {relative}")
        observed.append({"path": str(relative), "bytes": path.stat().st_size, "sha256": actual})
    if not any(item["path"] == "audio/out-000.pcm" for item in observed):
        raise ReproductionFailure("recording manifest does not list the provider PCM")
    raw_pcm = record_dir / "audio" / "out-000.pcm"
    if sha256_file(raw_pcm) != RAW_PCM_SHA256 or raw_pcm.stat().st_size <= 0:
        raise ReproductionFailure("provider PCM receipt is not the pinned positive control")
    return (
        {
            "path": "tool-record/manifest.json",
            "bytes": manifest_path.stat().st_size,
            "sha256": sha256_file(manifest_path),
            "terminal": manifest.get("terminal"),
            "artifacts": observed,
        },
        {
            "path": "tool-record/session-log.jsonl",
            "bytes": session_log_path.stat().st_size,
            "sha256": sha256_file(session_log_path),
        },
    )


def write_report(path: pathlib.Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifact", type=pathlib.Path, required=True)
    parser.add_argument("--expected-sha256", default=C113_ARTIFACT_SHA256)
    parser.add_argument("--child-timeout", type=int, default=60)
    parser.add_argument("--source-archive", type=pathlib.Path)
    parser.add_argument("--report", type=pathlib.Path, default=HERE / "runs" / "c113-reproduction.json")
    args = parser.parse_args()
    report_path = args.report if args.report.is_absolute() else HERE / args.report
    report: dict[str, Any] = {
        "schema_version": "audio-runtime-c117-c113-reproduction-v1",
        "source_revision": C113_SOURCE_REVISION,
        "current_head": git_head(),
        "fixture": FIXTURE_RELATIVE,
        "fixture_sha256": FIXTURE_SHA256,
        "artifact": str(args.artifact),
        "expected_artifact_sha256": args.expected_sha256,
        "source_archive_sha256": C113_SOURCE_ARCHIVE_SHA256,
        "credential_free": True,
        "realtime_used": False,
        "proof_level": "SOFTWARE_REPLAY",
    }
    try:
        artifact = resolve_external(args.artifact)
        if not artifact.is_file():
            raise ReproductionFailure(f"C113 artifact is missing: {artifact}")
        actual_artifact_sha = sha256_file(artifact)
        report["artifact"] = str(artifact)
        report["artifact_sha256"] = actual_artifact_sha
        if actual_artifact_sha != args.expected_sha256 or actual_artifact_sha != C113_ARTIFACT_SHA256:
            raise ReproductionFailure("C113 executable hash does not match the immutable artifact")
        archive = resolve_external(args.source_archive or artifact.with_name("artifact-1.gz"))
        if not archive.is_file() or sha256_file(archive) != C113_SOURCE_ARCHIVE_SHA256:
            raise ReproductionFailure("C113 source archive hash does not match the immutable source")
        report["source_archive"] = str(archive)
        fixture = (ROOT / FIXTURE_RELATIVE).resolve()
        if not fixture.is_file() or sha256_file(fixture) != FIXTURE_SHA256:
            raise ReproductionFailure("credential-free C113 fixture hash changed")

        with tempfile.TemporaryDirectory(prefix="audio-runtime-c117-c113-") as temporary:
            run_root = pathlib.Path(temporary)
            (run_root / "evidence" / "runs").mkdir(parents=True)
            record_dir = run_root / "tool-record"
            audio_out = run_root / "audio.wav"
            command = [
                str(artifact),
                "session",
                "--replay",
                str(fixture),
                "--audio-out",
                str(audio_out),
                "--record-dir",
                str(record_dir),
                "--trace-audio",
                "--workdir",
                str(run_root),
                "--allow-path",
                str(run_root),
            ]
            result = run_bounded(command, run_root, safe_environment(run_root / "home"), args.child_timeout)
            combined = result["stdout"] + result["stderr"]
            report["command"] = command
            report["process"] = {
                key: value for key, value in result.items() if key not in {"stdout", "stderr"}
            }
            report["process"]["stdout"] = result["stdout"][:4096]
            report["process"]["stderr"] = result["stderr"][:4096]
            if result["exit_code"] != 0:
                raise ReproductionFailure(f"C113 replay exited {result['exit_code']}")
            if result["timed_out"] or result["output_limited"] or not result["process_group_gone"]:
                raise ReproductionFailure("C113 replay was not bounded and clean")
            for literal, label in ((MARKER, "tool marker"), (CONTINUATION, "continuation"), ("fixture_complete", "fixture completion"), ("provider_close", "provider close")):
                if literal not in combined:
                    raise ReproductionFailure(f"C113 replay is missing {label}")
            if not audio_out.is_file() or audio_out.stat().st_size <= 44:
                raise ReproductionFailure("C113 replay did not produce a nonempty WAV")
            manifest, session_log = artifact_hashes(record_dir)
            report["audio_output"] = {"bytes": audio_out.stat().st_size, "sha256": sha256_file(audio_out)}
            report["recording"] = {"manifest": manifest, "session_log": session_log}
            trace_path = record_dir / "audio-trace" / "timeline.jsonl"
            report["trace"] = {"timeline": "tool-record/audio-trace/timeline.jsonl", "present": trace_path.exists()}
            if trace_path.exists():
                raise ReproductionFailure("C113 reproduction unexpectedly published audio-trace/timeline.jsonl")
        report["positive_effects"] = [
            "PROBE_TOOL_MARKER_9182",
            "strict replay continuation",
            "nonempty provider PCM",
            "nonempty output WAV",
            "five-artifact recording manifest",
            "fixture_complete",
            "provider_close",
        ]
        report["defect"] = "credential-free live replay exits 0 while omitting audio-trace/timeline.jsonl"
        report["passes"] = True
        write_report(report_path, report)
        print(json.dumps({"status": "ok", "report": str(report_path), "passes": True}, sort_keys=True))
        return 0
    except (OSError, ReproductionFailure, subprocess.SubprocessError) as exc:
        report["passes"] = False
        report["diagnostic"] = str(exc)
        write_report(report_path, report)
        print(json.dumps({"status": "failed", "report": str(report_path), "passes": False, "diagnostic": str(exc)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
