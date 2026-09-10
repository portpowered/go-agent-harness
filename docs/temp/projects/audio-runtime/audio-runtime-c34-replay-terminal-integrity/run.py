#!/usr/bin/env python3
"""Exercise replay lifecycle admission through a public Go consumer."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import wave


BASELINE_REVISION = "1f82284abee0bd31a6680310444cea2e4c16ef00"
SCHEMA_VERSION = 1
CHILD_TIMEOUT_SECONDS = 30
REPLAY_BUNDLE = Path(
    "docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe"
) / "evidence/runs/interruption/bundle"
EXPECTED_RENDERED_PCM = (
    3360,
    "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff",
)

REPO_ROOT = next(
    parent for parent in Path(__file__).resolve().parents if (parent / "go.work").is_file()
)
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
EVIDENCE = Path(__file__).resolve().parent
CONSUMER_SOURCE = EVIDENCE / "consumer/main.go"


class VerificationError(RuntimeError):
    pass


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_output(source_root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(source_root), *args],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    if result.returncode != 0:
        raise VerificationError(result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def run_process(command: list[str], cwd: Path, timeout_seconds: int, *, env: dict[str, str] | None = None) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
    return {
        "command": command,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "duration_seconds": round(time.monotonic() - started, 6),
        "stdout": stdout,
        "stderr": stderr,
    }


def save_process(record: dict[str, object], directory: Path, label: str) -> dict[str, object]:
    stdout_path = directory / f"{label}.stdout.txt"
    stderr_path = directory / f"{label}.stderr.txt"
    stdout_path.write_text(str(record["stdout"]), encoding="utf-8")
    stderr_path.write_text(str(record["stderr"]), encoding="utf-8")
    saved = dict(record)
    saved["stdout_path"] = str(stdout_path)
    saved["stderr_path"] = str(stderr_path)
    return saved


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def timestamp(base: datetime, elapsed_ns: int) -> str:
    return (base + timedelta(microseconds=elapsed_ns // 1000)).isoformat(timespec="microseconds").replace("+00:00", "Z")


def event(base: datetime, kind: str, elapsed_ns: int = 0, *, clean: bool | None = None, **fields: object) -> dict[str, object]:
    value: dict[str, object] = {
        "version": SCHEMA_VERSION,
        "sequence": 0,
        "elapsed_ns": elapsed_ns,
        "timestamp": timestamp(base, elapsed_ns),
        "kind": kind,
    }
    if clean is not None:
        value["clean"] = clean
    value.update(fields)
    return value


def write_timeline(directory: Path, events: list[dict[str, object]]) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    for sequence, value in enumerate(events, start=1):
        value["version"] = SCHEMA_VERSION
        value["sequence"] = sequence
    timeline = "".join(json.dumps(value, separators=(",", ":")) + "\n" for value in events)
    (directory / "timeline.jsonl").write_text(timeline, encoding="utf-8")


def write_wav(path: Path, samples: list[int], sample_rate: int = 16000) -> str:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = b"".join(struct.pack("<h", sample) for sample in samples)
    with wave.open(str(path), "wb") as stream:
        stream.setnchannels(1)
        stream.setsampwidth(2)
        stream.setframerate(sample_rate)
        stream.writeframes(payload)
    return hashlib.sha256(payload).hexdigest()


def fixture_hashes(root: Path) -> dict[str, str]:
    return {
        path.relative_to(root).as_posix(): sha256(path)
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def make_fixtures(run_directory: Path) -> tuple[Path, dict[str, dict[str, object]]]:
    root = run_directory / "fixtures"
    root.mkdir()
    base = datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc)
    samples = [32767, -1234, 0, 42]
    pcm_hash = hashlib.sha256(b"".join(struct.pack("<h", sample) for sample in samples)).hexdigest()

    valid_empty = root / "valid-empty"
    write_timeline(
        valid_empty,
        [
            event(base, "recording_started"),
            event(base, "runtime", 1_000_000, runtime_kind="fixture"),
            {**event(base, "recording_closed", 2_000_000, clean=True), "timestamp": ""},
        ],
    )

    valid_audio = root / "valid-audio-runtime"
    write_wav(valid_audio / "microphone-pre-gate.wav", samples)
    write_timeline(
        valid_audio,
        [
            event(base, "recording_started"),
            event(
                base,
                "audio",
                tap="microphone_pre_gate",
                sample_rate=16000,
                start_sample=0,
                sample_count=len(samples),
                pcm_sha256=pcm_hash,
                duration_ns=250_000,
            ),
            event(base, "runtime", 1_000_000, runtime_kind="fixture"),
            event(base, "recording_closed", 2_000_000, clean=True),
        ],
    )

    def empty_case(name: str, events: list[dict[str, object]]) -> None:
        write_timeline(root / name, events)

    empty_case(
        "baseline-first-final-only",
        [
            event(base, "recording_started"),
            event(base, "recording_closed", 1_000_000, clean=True),
            event(base, "runtime", 1_000_000, runtime_kind="post-close-runtime"),
            event(base, "recording_closed", 2_000_000, clean=True),
        ],
    )
    empty_case(
        "duplicate-start",
        [event(base, "recording_started"), event(base, "recording_started"), event(base, "recording_closed", 1_000_000, clean=True)],
    )
    empty_case(
        "duplicate-close",
        [event(base, "recording_started"), event(base, "recording_closed", 1_000_000, clean=True), event(base, "recording_closed", 1_000_000, clean=True)],
    )
    empty_case(
        "trailing-event-after-close",
        [event(base, "recording_started"), event(base, "recording_closed", 1_000_000, clean=True), event(base, "runtime", 1_000_000, runtime_kind="trailing")],
    )
    empty_case(
        "missing-leading-start",
        [event(base, "runtime", runtime_kind="before-start"), event(base, "recording_closed", 1_000_000, clean=True)],
    )
    empty_case(
        "missing-close",
        [event(base, "recording_started"), event(base, "runtime", 1_000_000, runtime_kind="unfinished")],
    )
    empty_case(
        "unclean-close",
        [event(base, "recording_started"), event(base, "recording_closed", 1_000_000, clean=False)],
    )

    audio_after_close = root / "audio-after-close"
    audio_after_close.mkdir()
    shutil.copy2(valid_audio / "microphone-pre-gate.wav", audio_after_close / "microphone-pre-gate.wav")
    write_timeline(
        audio_after_close,
        [
            event(base, "recording_started"),
            event(base, "recording_closed", 1_000_000, clean=True),
            event(
                base,
                "audio",
                1_000_000,
                tap="microphone_pre_gate",
                sample_rate=16000,
                start_sample=0,
                sample_count=len(samples),
                pcm_sha256=pcm_hash,
                duration_ns=250_000,
            ),
            event(base, "recording_closed", 2_000_000, clean=True),
        ],
    )

    cases = {}
    for directory in sorted(root.iterdir()):
        cases[directory.name] = {
            "path": str(directory),
            "files": fixture_hashes(directory),
        }
    manifest = {"base": base.isoformat(), "audio_samples": samples, "audio_pcm_sha256": pcm_hash, "cases": cases}
    (root / "fixture-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return root, cases


def build_consumer(source_root: Path, run_directory: Path) -> tuple[Path, dict[str, object]]:
    require(CONSUMER_SOURCE.is_file(), f"public consumer source missing: {CONSUMER_SOURCE}")
    build_root = run_directory / "build"
    build_root.mkdir()
    shutil.copy2(CONSUMER_SOURCE, build_root / "main.go")
    module_file = build_root / "go.mod"
    module_file.write_text(
        "module example.com/audio-runtime-c34-replay-consumer\n\n"
        "go 1.26.7\n\n"
        "require github.com/portpowered/go-agent-harness/go-audio v0.0.0\n\n"
        f"replace github.com/portpowered/go-agent-harness/go-audio => {source_root / 'go-audio'}\n",
        encoding="utf-8",
    )
    binary = run_directory / "replay-consumer"
    command = ["go", "build", "-mod=mod", "-trimpath", "-o", str(binary), "./"]
    environment = os.environ.copy()
    environment["GOWORK"] = "off"
    record = run_process(command, build_root, 60, env=environment)
    record["environment_controls"] = {"GOWORK": "off", "source_root": str(source_root)}
    record = save_process(record, run_directory, "consumer-build")
    require(record["exit_code"] == 0 and not record["timed_out"], f"consumer build failed: {record}")
    return binary, {
        "path": str(binary),
        "sha256": sha256(binary),
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
        "consumer_source_sha256": sha256(CONSUMER_SOURCE),
        "build": record,
    }


def parse_consumer_output(record: dict[str, object]) -> dict[str, object] | None:
    lines = [line for line in str(record["stdout"]).splitlines() if line.strip()]
    if not lines:
        return None
    try:
        value = json.loads(lines[-1])
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def run_consumer(binary: Path, fixture: Path, run_directory: Path, label: str) -> dict[str, object]:
    record = run_process([str(binary), str(fixture)], run_directory, CHILD_TIMEOUT_SECONDS)
    saved = save_process(record, run_directory, label)
    saved["fixture"] = str(fixture)
    saved["fixture_sha256"] = fixture_hashes(fixture)
    saved["consumer"] = str(binary)
    saved["json"] = parse_consumer_output(saved)
    return saved


def verify_consumer(binary: Path, fixtures: Path, run_directory: Path, mode: str, negative_control: bool) -> dict[str, object]:
    valid_names = ["valid-empty", "valid-audio-runtime"]
    invalid_names = [
        "baseline-first-final-only",
        "duplicate-start",
        "duplicate-close",
        "trailing-event-after-close",
        "missing-leading-start",
        "missing-close",
        "unclean-close",
        "audio-after-close",
    ]
    records: dict[str, dict[str, object]] = {}
    for name in valid_names + invalid_names:
        records[name] = run_consumer(binary, fixtures / name, run_directory, f"consumer-{name}")

    def successful(record: dict[str, object]) -> bool:
        return record["exit_code"] == 0 and not record["timed_out"]

    if mode == "characterize":
        baseline = records["baseline-first-final-only"]
        require(successful(baseline), f"before-repair reproduction did not false-pass: {baseline}")
        value = baseline["json"]
        require(isinstance(value, dict) and value.get("accepted") is True, f"baseline did not expose accepted replay: {baseline}")
        return {
            "mode": mode,
            "before_repair_observation": {
                "timeline_kinds": value.get("event_kinds"),
                "post_close_runtime_accepted": True,
                "error": None,
            },
            "cases": records,
        }

    for name in valid_names:
        record = records[name]
        value = record["json"]
        require(successful(record), f"valid fixture {name} failed: {record}")
        require(isinstance(value, dict) and value.get("accepted") is True and value.get("eof") is True, f"valid fixture {name} result invalid: {value}")
    audio_value = records["valid-audio-runtime"]["json"]
    require(isinstance(audio_value, dict) and audio_value.get("audio_frames") == 1 and audio_value.get("total_samples") == 4, f"valid audio result invalid: {audio_value}")

    for name in invalid_names:
        record = records[name]
        value = record["json"]
        require(not successful(record), f"malformed fixture {name} unexpectedly succeeded: {record}")
        require(isinstance(value, dict) and value.get("accepted") is not True and value.get("replay_exposed") is False, f"malformed fixture {name} exposed replay: {value}")
        require(value.get("err_incomplete") is True, f"malformed fixture {name} did not return ErrIncomplete: {value}")
    return {"mode": mode, "cases": records, "negative_control": negative_control}


def runtime_regression(yui: Path, run_directory: Path) -> dict[str, object]:
    require(yui.is_file(), f"yui executable missing: {yui}")
    source_bundle = (FACTORY_ROOT / REPLAY_BUNDLE).resolve()
    require(source_bundle.is_dir(), f"preserved software replay bundle missing: {source_bundle}")
    bundle = run_directory / "runtime-bundle"
    shutil.copytree(source_bundle, bundle)
    config = run_directory / "config"
    config.mkdir()
    output = run_directory / "rendered-output.pcm"
    commands = [
        [str(yui), "-C", str(config), "session", "replay", str(bundle)],
        [str(yui), "-C", str(config), "session", "--replay", str(bundle), "--audio-out", str(output), "--max-duration", "60s"],
    ]
    records = []
    for index, command in enumerate(commands, start=1):
        record = run_process(command, run_directory, 60)
        records.append(save_process(record, run_directory, f"runtime-regression-{index}"))
    directory_output = f"{records[0]['stdout']}\n{records[0]['stderr']}"
    flag_output = f"{records[1]['stdout']}\n{records[1]['stderr']}"
    require(records[0]["exit_code"] == 0 and "Replay verified: 15 wire events, 0 tool calls" in directory_output, f"strict directory replay failed: {records[0]}")
    require(records[1]["exit_code"] == 0, f"strict flag replay failed: {records[1]}")
    for marker in ("classification=replay_complete", "output_state=complete", "[session replay complete]"):
        require(marker in flag_output, f"strict flag replay omitted {marker!r}: {records[1]}")
    require(output.is_file(), f"strict flag replay did not write {output}")
    pcm = {"bytes": output.stat().st_size, "sha256": sha256(output)}
    require(tuple((pcm["bytes"], pcm["sha256"])) == EXPECTED_RENDERED_PCM, f"strict replay PCM changed: {pcm}")
    return {
        "yui": str(yui),
        "yui_sha256": sha256(yui),
        "source_bundle": str(source_bundle),
        "source_bundle_hashes": fixture_hashes(source_bundle),
        "private_bundle": str(bundle),
        "commands": records,
        "rendered_pcm": pcm,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", type=Path, default=REPO_ROOT)
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--mode", choices=("characterize", "verify"), default="verify")
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument("--runtime-regression", action="store_true")
    parser.add_argument("--yui", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()

    EVIDENCE.joinpath("runs").mkdir(parents=True, exist_ok=True)
    run_directory = Path(tempfile.mkdtemp(prefix="replay-lifecycle-", dir=EVIDENCE / "runs"))
    report: dict[str, object] = {
        "project": "audio-runtime",
        "work": "audio-runtime-c34-replay-terminal-integrity",
        "run_directory": str(run_directory),
        "source_root": str(args.source_root.resolve()),
        "mode": args.mode,
        "status": "failed",
    }
    try:
        source_root = args.source_root.resolve()
        require((source_root / "go-audio/pkg/recording/replay.go").is_file(), f"invalid source root: {source_root}")
        report["source_revision"] = git_output(source_root, "rev-parse", "HEAD")
        report["source_dirty"] = git_output(source_root, "status", "--short")
        if args.runtime_regression:
            require(args.yui is not None, "--runtime-regression requires --yui")
            report["runtime_regression"] = runtime_regression(args.yui.resolve(), run_directory)
        if args.build or not args.runtime_regression:
            binary, build_info = build_consumer(source_root, run_directory)
            report["consumer"] = build_info
            fixtures, cases = make_fixtures(run_directory)
            report["fixtures"] = {"root": str(fixtures), "cases": cases}
            report["consumer_verification"] = verify_consumer(binary, fixtures, run_directory, args.mode, args.negative_control)
        report["status"] = "passed"
    except (OSError, subprocess.SubprocessError, VerificationError) as error:
        report["error"] = str(error)

    output = args.output.resolve() if args.output else run_directory / "report.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
