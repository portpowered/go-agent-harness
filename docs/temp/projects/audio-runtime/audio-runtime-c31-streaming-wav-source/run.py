#!/usr/bin/env python3
"""Bounded C31 source characterization and exact-source evidence runner."""

from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.util
import json
import os
import pathlib
import signal
import struct
import subprocess
import sys
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
CONSUMER = HERE / "consumer"
ARTIFACTS = HERE / "artifacts"
RUNS = HERE / "runs"
TIMEOUT_SECONDS = 60
C21_VERIFY = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/verify.py"
C21_FIXTURE_DIR = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"
WORKFLOW_SAMPLES = [-32768, -12345, -1, 0, 1, 12345, 32767]
WORKFLOW_SOURCE_FIXTURE = ROOT / "agent-cli/test/integration/testdata/s2s-e2e-vision-describe/s2s_e2e_vision_describe.session.json"
WORKFLOW_OUTPUT_PCM = struct.pack("<" + ("h" * 480), *([1234] + ([0] * 479)))
BUILD_INPUT_GROUPS = {
    "consumer": ("go-audio", "docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/consumer"),
    "yui": (
        "agent-cli",
        "go-agent-loop",
        "go-agent-runtime",
        "go-audio",
        "go-device-gateway",
        "go-llm-gateway",
        "go.work",
        "go.work.sum",
    ),
}
PROVENANCE_PATHS = tuple(dict.fromkeys(path for prefixes in BUILD_INPUT_GROUPS.values() for path in prefixes))


class EvidenceError(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_pcm16_wav(path: pathlib.Path, samples: list[int], sample_rate: int = 16000) -> bytes:
    payload = struct.pack("<" + ("h" * len(samples)), *samples)
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF",
        36 + len(payload),
        b"WAVE",
        b"fmt ",
        16,
        1,
        1,
        sample_rate,
        sample_rate * 2,
        2,
        16,
        b"data",
        len(payload),
    )
    path.write_bytes(header + payload)
    return payload


def load_json(path: pathlib.Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvidenceError(f"could not read JSON descriptor {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise EvidenceError(f"JSON descriptor {path} is not an object")
    return value


def write_workflow_capture(path: pathlib.Path, source: pathlib.Path, input_frame: bytes, output_pcm: bytes) -> None:
    capture = json.loads(source.read_text(encoding="utf-8"))
    records = []
    for record in capture.get("records", []):
        if record.get("type") == "conversation.item.create":
            content = record.get("payload", {}).get("item", {}).get("content", [])
            if any(item.get("type") == "input_image" for item in content):
                continue
        if record.get("direction") == "client_to_server" and record.get("type") == "input_audio_buffer.append":
            record["payload_type"] = "websocket_message"
            record["payload"] = {
                "type": "input_audio_buffer.append",
                "audio": base64.b64encode(input_frame).decode("ascii"),
            }
            record.pop("data", None)
        if record.get("direction") == "server_to_client" and record.get("type") == "response.output_audio.delta":
            record["payload_type"] = "websocket_message"
            record["payload"] = {
                "type": "response.output_audio.delta",
                "delta": base64.b64encode(output_pcm).decode("ascii"),
            }
            record.pop("data", None)
        records.append(record)
    if not any(record.get("type") == "input_audio_buffer.append" for record in records):
        raise EvidenceError(f"workflow fixture {source} has no audio append slot")
    capture["records"] = records
    for sequence, record in enumerate(capture["records"], 1):
        record["sequence"] = sequence
    coverage = {
        "version": capture["version"],
        "provider": capture["provider"],
        "session": capture["session"],
        "records": capture["records"],
    }
    if capture.get("ends_with_disconnect"):
        coverage["ends_with_disconnect"] = True
    digest = hashlib.sha256(json.dumps(coverage, separators=(",", ":"), ensure_ascii=False).encode("utf-8")).hexdigest()
    capture["integrity"] = {
        "algorithm": "sha256",
        "coverage": "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
        "digest": digest,
    }
    path.write_text(json.dumps(capture, indent=2) + "\n", encoding="utf-8")


def source_revision() -> str:
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, text=True, capture_output=True
    )
    return result.stdout.strip()


def source_identity() -> str:
    revision = source_revision()
    diff = subprocess.run(
        ["git", "diff", "--no-ext-diff", "--binary", "HEAD", "--", *PROVENANCE_PATHS],
        cwd=ROOT,
        check=True,
        capture_output=True,
    ).stdout
    if not diff:
        return revision
    return f"{revision}+dirty-{hashlib.sha256(diff).hexdigest()[:16]}"


def build_input_paths(prefixes: tuple[str, ...]) -> list[pathlib.Path]:
    result = subprocess.run(
        ["git", "ls-files", "-z", "--", *prefixes], cwd=ROOT, check=True, capture_output=True
    )
    paths = [pathlib.Path(raw) for raw in result.stdout.decode().split("\0") if raw]
    if not paths:
        raise EvidenceError(f"no tracked build inputs found for {prefixes}")
    return paths


def build_input_hash(prefixes: tuple[str, ...]) -> str:
    digest = hashlib.sha256()
    for relative in build_input_paths(prefixes):
        digest.update(relative.as_posix().encode("utf-8"))
        digest.update(b"\0")
        digest.update((ROOT / relative).read_bytes())
    return digest.hexdigest()


def build_input_manifest() -> dict[str, dict[str, Any]]:
    return {
        name: {
            "path_prefixes": list(prefixes),
            "file_count": len(build_input_paths(prefixes)),
            "sha256": build_input_hash(prefixes),
        }
        for name, prefixes in BUILD_INPUT_GROUPS.items()
    }


def bounded_process(name: str, argv: list[str], cwd: pathlib.Path, env: dict[str, str] | None = None) -> dict[str, Any]:
    started = time.monotonic()
    process_env = os.environ.copy()
    if env:
        process_env.update(env)
    process_env["GODEBUG"] = "gctrace=0"
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=process_env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        text=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
        raise EvidenceError(f"{name} exceeded {TIMEOUT_SECONDS}s process deadline")
    return {
        "name": name,
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": stdout,
        "stderr": stderr,
    }


def save_run(name: str, result: dict[str, Any]) -> pathlib.Path:
    RUNS.mkdir(parents=True, exist_ok=True)
    safe = name.replace("/", "-")
    path = RUNS / f"{safe}.json"
    path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return path


def build() -> dict[str, Any]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    consumer_path = ARTIFACTS / "wav-consumer"
    yui_path = ARTIFACTS / "yui"
    consumer_result = bounded_process(
        "build-consumer",
        ["go", "build", "-trimpath", "-o", str(consumer_path), str(CONSUMER / "main.go")],
        ROOT / "go-audio",
        {"GOWORK": "off"},
    )
    save_run("build-consumer", consumer_result)
    if consumer_result["exit_code"] != 0:
        raise EvidenceError(f"consumer build failed: {consumer_result['stderr']}")
    yui_result = bounded_process(
        "build-yui",
        ["go", "build", "-trimpath", "-o", str(yui_path), "./agent-cli/cmd/yui"],
        ROOT,
    )
    save_run("build-yui", yui_result)
    if yui_result["exit_code"] != 0:
        raise EvidenceError(f"yui build failed: {yui_result['stderr']}")
    tested_source = source_identity()
    inputs = build_input_manifest()
    manifest = {
        "schema": "audio-runtime-c31-artifact-manifest.v2",
        "source": tested_source,
        "tested_source_revision": tested_source,
        "build_inputs": inputs,
        "go_version": subprocess.run(["go", "version"], check=True, text=True, capture_output=True).stdout.strip(),
        "artifacts": {
            "consumer": {
                "path": str(consumer_path),
                "sha256": sha256(consumer_path),
                "tested_source_revision": tested_source,
                "build_inputs_sha256": inputs["consumer"]["sha256"],
            },
            "yui": {
                "path": str(yui_path),
                "sha256": sha256(yui_path),
                "tested_source_revision": tested_source,
                "build_inputs_sha256": inputs["yui"]["sha256"],
            },
        },
    }
    (HERE / "artifact-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest


def ensure_consumer() -> pathlib.Path:
    path = ARTIFACTS / "wav-consumer"
    manifest_path = HERE / "artifact-manifest.json"
    artifact_source = ""
    if manifest_path.is_file():
        try:
            artifact_source = load_json(manifest_path).get("source", "")
        except EvidenceError:
            artifact_source = ""
    if not path.exists() or artifact_source != source_identity():
        build()
    return path


def consumer_case(case: str, label: str) -> tuple[dict[str, Any], dict[str, Any]]:
    consumer = ensure_consumer()
    revision = source_identity()
    result = bounded_process(
        f"consumer-{label}",
        [str(consumer), "--case", case],
        HERE,
        {
            "C31_SOURCE_REVISION": revision,
            "C31_SOURCE_INPUTS_SHA256": build_input_hash(BUILD_INPUT_GROUPS["consumer"]),
        },
    )
    save_run(f"consumer-{label}", result)
    try:
        report = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceError(f"consumer {label} did not emit JSON: {exc}; stderr={result['stderr']}") from exc
    (HERE / f"{label}.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report, result


def characterize(label: str) -> None:
    report, result = consumer_case("characterize", f"characterize-{label}")
    allocations = report.get("allocations", {})
    oracle = allocations.get("oracle", {})
    if result["exit_code"] != 0:
        raise EvidenceError(f"characterization child failed: {result['stderr']}")
    if label == "before" and oracle.get("pass") is not False:
        raise EvidenceError("pre-fix characterization did not fail the frozen allocation oracle")
    if label == "after" and oracle.get("pass") is not True:
        raise EvidenceError("repaired characterization did not pass the frozen allocation oracle")


def positive() -> None:
    report, result = consumer_case("verify", "positive")
    if result["exit_code"] != 0 or report.get("io", {}).get("pass") is not True:
        raise EvidenceError(f"positive consumer failed: {result['stderr'] or report.get('error', '')}")
    failed = [item for item in report.get("checks", []) if item.get("pass") is not True]
    if failed:
        raise EvidenceError(f"positive source checks failed: {failed}")


def negative_control() -> None:
    report, result = consumer_case("negative-control", "negative-control")
    negative = report.get("negative_control", {})
    if result["exit_code"] == 0:
        raise EvidenceError("negative control was accepted")
    if negative.get("pass") is True or "mutated expectation" not in report.get("error", ""):
        raise EvidenceError(f"negative control failed without a causal mismatch: {report}")


def workflow() -> None:
    yui = build().get("artifacts", {}).get("yui", {}).get("path")
    if not yui:
        raise EvidenceError("workflow has no same-source yui artifact")
    yui_path = pathlib.Path(yui)
    RUNS.mkdir(parents=True, exist_ok=True)
    case_dir = RUNS / f"workflow-{time.time_ns()}"
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    input_path = case_dir / "input.wav"
    input_payload = write_pcm16_wav(input_path, WORKFLOW_SAMPLES)
    padding_bytes = 480 * 2 - len(input_payload)
    if padding_bytes < 0:
        raise EvidenceError("workflow fixture payload is larger than one 480-sample frame")
    input_frame = input_payload + (b"\x00" * padding_bytes)
    fixture_source = WORKFLOW_SOURCE_FIXTURE
    fixture = case_dir / "audio-input.session.json"
    write_workflow_capture(fixture, fixture_source, input_frame, WORKFLOW_OUTPUT_PCM)
    record_dir = case_dir / "recording"
    audio_out = case_dir / "output.wav"
    result = bounded_process(
        "yui-file-input-workflow",
        [
            "rtk",
            "proxy",
            str(yui_path),
            "session",
            "--replay",
            str(fixture),
            "--audio-in",
            str(input_path),
            "--audio-out",
            str(audio_out),
            "--record-dir",
            str(record_dir),
            "--trace-audio",
            "--max-duration",
            "60s",
            "--workdir",
            str(case_dir),
            "--allow-path",
            str(case_dir),
        ],
        case_dir,
    )
    run_path = save_run("workflow-yui-file-input", result)
    if result["exit_code"] != 0:
        raise EvidenceError(f"file-input workflow failed: {result['stderr'] or result['stdout']}")
    manifest_path = record_dir / "manifest.json"
    if not manifest_path.is_file():
        raise EvidenceError("file-input workflow exited cleanly without a recording manifest")
    input_files = sorted((record_dir / "audio").glob("in-*.pcm"))
    recorded_input = b"".join(path.read_bytes() for path in input_files)
    if recorded_input != input_frame:
        raise EvidenceError(
            "file-input workflow changed the literal input payload: "
            f"expected={len(input_frame)}:{hashlib.sha256(input_frame).hexdigest()} "
            f"actual={len(recorded_input)}:{hashlib.sha256(recorded_input).hexdigest()}"
        )
    if not audio_out.is_file() or audio_out.stat().st_size <= 44:
        raise EvidenceError("file-input workflow exited cleanly without non-empty audio output")
    output_bytes = audio_out.read_bytes()
    if output_bytes[:4] != b"RIFF" or output_bytes[8:12] != b"WAVE" or output_bytes[36:40] != b"data":
        raise EvidenceError("file-input workflow output is not a canonical PCM16 WAV")
    declared_output_bytes = struct.unpack_from("<I", output_bytes, 40)[0]
    observed_output_pcm = output_bytes[44:]
    if declared_output_bytes != len(observed_output_pcm):
        raise EvidenceError(
            "file-input workflow output data size disagrees with payload: "
            f"declared={declared_output_bytes} actual={len(observed_output_pcm)}"
        )
    expected_output_sha256 = hashlib.sha256(WORKFLOW_OUTPUT_PCM).hexdigest()
    observed_output_sha256 = hashlib.sha256(observed_output_pcm).hexdigest()
    if observed_output_pcm != WORKFLOW_OUTPUT_PCM:
        raise EvidenceError(
            "file-input workflow changed the literal output payload: "
            f"expected={len(WORKFLOW_OUTPUT_PCM)}:{expected_output_sha256} "
            f"actual={len(observed_output_pcm)}:{observed_output_sha256}"
        )
    manifest = load_json(manifest_path)
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list):
        raise EvidenceError("file-input workflow manifest has no artifact list")
    expected_input_relative = [f"audio/{path.name}" for path in input_files]
    manifest_paths = [item.get("path") for item in artifacts if isinstance(item, dict)]
    if not set(expected_input_relative).issubset(manifest_paths):
        raise EvidenceError(f"file-input workflow manifest omitted input artifacts: {manifest_paths}")
    summary = {
        "schema": "audio-runtime-c31-file-input-workflow.v1",
        "source": source_identity(),
        "tested_source_revision": source_identity(),
        "yui_build_inputs_sha256": build_input_hash(BUILD_INPUT_GROUPS["yui"]),
        "fixture": str(fixture),
        "fixture_sha256": sha256(fixture),
        "fixture_source": str(fixture_source),
        "fixture_source_sha256": sha256(fixture_source),
        "command_run": str(run_path),
        "exit_code": result["exit_code"],
        "clean_shutdown": True,
        "input": {
            "path": str(input_path),
            "sample_rate": 16000,
            "samples": WORKFLOW_SAMPLES,
            "payload_bytes": len(input_payload),
            "payload_sha256": hashlib.sha256(input_payload).hexdigest(),
            "frame_bytes": len(input_frame),
            "frame_sha256": hashlib.sha256(input_frame).hexdigest(),
            "recorded_files": [str(path) for path in input_files],
            "recorded_payload_bytes": len(recorded_input),
            "recorded_payload_sha256": hashlib.sha256(recorded_input).hexdigest(),
            "exact": True,
        },
        "recording": {
            "path": str(record_dir),
            "manifest": str(manifest_path),
            "manifest_sha256": sha256(manifest_path),
            "terminal": manifest.get("terminal"),
        },
        "output": {
            "path": str(audio_out),
            "bytes": len(output_bytes),
            "sha256": hashlib.sha256(output_bytes).hexdigest(),
            "expected_pcm_payload_bytes": len(WORKFLOW_OUTPUT_PCM),
            "expected_pcm_payload_sha256": expected_output_sha256,
            "observed_pcm_payload_bytes": len(observed_output_pcm),
            "observed_pcm_payload_sha256": observed_output_sha256,
            "pcm_payload_sha256": observed_output_sha256,
            "pcm_payload_exact": True,
        },
    }
    (HERE / "workflow.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")


def load_c21_controls() -> Any:
    spec = importlib.util.spec_from_file_location("audio_runtime_c21_controls", C21_VERIFY)
    if spec is None or spec.loader is None:
        raise EvidenceError(f"could not load read-only regression controls: {C21_VERIFY}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.YUI = ARTIFACTS / "yui"
    return module


def regression() -> None:
    build()
    controls = load_c21_controls()
    if not pathlib.Path(controls.YUI).is_file():
        raise EvidenceError("regression has no same-source yui artifact")
    RUNS.mkdir(parents=True, exist_ok=True)
    run_dir = RUNS / f"regression-{time.time_ns()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    cases = controls.run_parity(run_dir)
    summary = {
        "schema": "audio-runtime-c31-read-only-c21-regression.v1",
        "source": source_identity(),
        "tested_source_revision": source_identity(),
        "yui_build_inputs_sha256": build_input_hash(BUILD_INPUT_GROUPS["yui"]),
        "controls": str(C21_VERIFY),
        "fixtures": {
            label: {
                "path": str(C21_FIXTURE_DIR / f"{name}.session.json"),
                "sha256": sha256(C21_FIXTURE_DIR / f"{name}.session.json"),
            }
            for label, name in (("audio-tool", "c16-audio-tool"), ("interruption", "c16-interruption"))
        },
        "cases": cases,
        "assertions": [
            "exact PCM bytes and SHA256",
            "provider fixture bytes and event order",
            "tool event sequence, arguments, marker and continuation",
            "transcript/session-log terminal outcomes",
            "recording manifest artifact hashes",
            "healthy interruption tail bytes and SHA256",
        ],
        "read_only_fixture_reuse": True,
    }
    (HERE / "regression.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", default=".")
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--characterize", action="store_true")
    parser.add_argument("--label", choices=("before", "after"))
    parser.add_argument("--positive", action="store_true")
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument("--workflow", action="store_true")
    parser.add_argument("--regression", action="store_true")
    args = parser.parse_args()

    if pathlib.Path(args.source_root).resolve() != ROOT.resolve():
        raise EvidenceError(f"source root must be the isolated worktree root: {ROOT}")
    selected = sum(bool(value) for value in (args.build, args.characterize, args.positive, args.negative_control, args.workflow, args.regression))
    if selected != 1:
        raise EvidenceError("select exactly one evidence action")
    if args.build:
        build()
    elif args.characterize:
        if not args.label:
            raise EvidenceError("--characterize requires --label before or after")
        characterize(args.label)
    elif args.positive:
        positive()
    elif args.negative_control:
        negative_control()
    elif args.workflow:
        workflow()
    elif args.regression:
        regression()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceError as error:
        print(f"c31 evidence: {error}", file=sys.stderr)
        raise SystemExit(1)
