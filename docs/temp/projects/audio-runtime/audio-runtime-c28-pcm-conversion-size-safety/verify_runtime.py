#!/usr/bin/env python3
"""Bounded C28 public consumer, characterization, and replay verifier."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import signal
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from collections import Counter
from pathlib import Path


EVIDENCE_DIR = Path(__file__).resolve().parent
CONSUMER_SOURCE = EVIDENCE_DIR / "consumer" / "main.go"
ARTIFACT_DIR = EVIDENCE_DIR / "artifacts"
EXPECTED_PATH = EVIDENCE_DIR / "expected-effects.json"
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", ""))
if not FACTORY_ROOT:
    FACTORY_ROOT = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip())
REPO_ROOT = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE_DIR, text=True).strip())
DEFAULT_FIXTURE = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected/evidence/captures/c07-audio-tool-traced.session.json"
CHARACTERIZE_TIMEOUT_SECONDS = 10
BUILD_TIMEOUT_SECONDS = 60
REPLAY_TIMEOUT_SECONDS = 60


class VerificationError(RuntimeError):
    pass


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run_bounded(command: list[str], cwd: Path, timeout_seconds: float, env: dict[str, str] | None = None) -> dict[str, object]:
    started = time.monotonic()
    child_env = os.environ.copy()
    if env:
        child_env.update(env)
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=child_env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or ""
        stderr = exc.stderr or ""
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        tail_stdout, tail_stderr = process.communicate()
        stdout += tail_stdout
        stderr += tail_stderr
    return {
        "command": shlex.join(command),
        "cwd": str(cwd),
        "exit_code": process.returncode if not timed_out else -signal.SIGKILL,
        "timed_out": timed_out,
        "duration_ms": round((time.monotonic() - started) * 1000),
        "stdout": stdout,
        "stderr": stderr,
    }


def save_process_result(name: str, result: dict[str, object]) -> None:
    run_dir = EVIDENCE_DIR / "runs"
    run_dir.mkdir(parents=True, exist_ok=True)
    (run_dir / f"{name}.stdout.log").write_text(str(result["stdout"]), encoding="utf-8")
    (run_dir / f"{name}.stderr.log").write_text(str(result["stderr"]), encoding="utf-8")


def require_success(result: dict[str, object], description: str) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise VerificationError(
            f"{description} failed: exit={result['exit_code']} timeout={result['timed_out']} "
            f"stderr={str(result['stderr']).strip()[:1000]}"
        )


def load_expected() -> dict[str, object]:
    try:
        value = json.loads(EXPECTED_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationError(f"expected effects are unavailable or malformed: {exc}") from exc
    if value.get("schema") != "audio-runtime-c28-expected-effects.v1":
        raise VerificationError("expected effects schema is not the C28 immutable schema")
    return value


def source_revision_or_head(value: str | None) -> str:
    if value:
        revision = value
    else:
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, text=True).strip()
    try:
        resolved = subprocess.check_output(["git", "rev-parse", "--verify", f"{revision}^{{commit}}"], cwd=REPO_ROOT, text=True).strip()
    except subprocess.CalledProcessError as exc:
        raise VerificationError(f"source revision is not a commit: {revision}") from exc
    return resolved


def extract_source(revision: str, destination: Path) -> None:
    archive = destination / "source.tar"
    result = subprocess.run(["git", "archive", "--format=tar", "--output", str(archive), revision], cwd=REPO_ROOT, text=True, capture_output=True)
    if result.returncode != 0:
        raise VerificationError(f"could not archive source {revision}: {result.stderr.strip()}")
    with tarfile.open(archive, "r") as stream:
        stream.extractall(destination)


def build_from_source(revision: str, consumer_output: Path, yui_output: Path | None = None) -> dict[str, object]:
    if not CONSUMER_SOURCE.is_file():
        raise VerificationError(f"consumer source is missing: {CONSUMER_SOURCE}")
    ARTIFACT_DIR.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c28-source-") as temporary:
        source_root = Path(temporary)
        extract_source(revision, source_root)
        copied_consumer = source_root / "consumer" / "main.go"
        copied_consumer.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(CONSUMER_SOURCE, copied_consumer)
        build_env = os.environ.copy()
        build_env.pop("GOWORK", None)
        consumer_command = ["go", "build", "-o", str(consumer_output), "../consumer/main.go"]
        consumer_result = run_bounded(consumer_command, source_root / "go-audio", BUILD_TIMEOUT_SECONDS, build_env)
        save_process_result(f"build-consumer-{revision[:12]}", consumer_result)
        require_success(consumer_result, "exported-API consumer build")
        result: dict[str, object] = {"consumer_build": consumer_result}
        if yui_output is not None:
            yui_command = ["go", "build", "-o", str(yui_output), "./cmd/yui"]
            yui_result = run_bounded(yui_command, source_root / "agent-cli", BUILD_TIMEOUT_SECONDS, build_env)
            save_process_result(f"build-yui-{revision[:12]}", yui_result)
            require_success(yui_result, "same-source yui build")
            result["yui_build"] = yui_result
        return result


def consumer_json(binary: Path, case_name: str, run_name: str, source_revision: str) -> tuple[dict[str, object], dict[str, object]]:
    result = run_bounded([str(binary), "--case", case_name], EVIDENCE_DIR, CHARACTERIZE_TIMEOUT_SECONDS, {"C28_SOURCE_REVISION": source_revision})
    save_process_result(run_name, result)
    require_success(result, f"consumer {case_name}")
    try:
        report = json.loads(str(result["stdout"]))
    except json.JSONDecodeError as exc:
        raise VerificationError(f"consumer {case_name} did not emit JSON: {exc}") from exc
    return report, result


def validate_consumer_report(report: dict[str, object], expected: dict[str, object], source_revision: str) -> None:
    if report.get("schema") != "audio-runtime-c28-pcm-consumer.v1":
        raise VerificationError("consumer report schema mismatch")
    if report.get("source") != source_revision:
        raise VerificationError(f"consumer source mismatch: {report.get('source')} != {source_revision}")
    if report.get("clean_shutdown") is not True or report.get("panicked") is True:
        raise VerificationError("consumer did not report clean bounded shutdown")
    if int(report.get("duration_ms", CHARACTERIZE_TIMEOUT_SECONDS * 1000 + 1)) > CHARACTERIZE_TIMEOUT_SECONDS * 1000:
        raise VerificationError("consumer exceeded its ten-second watchdog")

    cases = {case["name"]: case for case in report.get("cases", [])}
    known_answers = expected["consumer"]["known_answers"]
    names = {
        "supported-rate-known-answer": known_answers["supported_rate"],
        "arbitrary-rate-known-answer": known_answers["arbitrary_rate"],
        "negative-half-rounding": known_answers["negative_half_rounding"],
        "stereo-downmix": known_answers["stereo_downmix"],
        "mono-stereo-arbitrary-rate": known_answers["mono_stereo_arbitrary_rate"],
        "extra-channel-repeat": known_answers["extra_channel_repeat"],
        "signed-extrema-identity": known_answers["signed_extrema_identity"],
        "empty-input": known_answers["empty_input"],
    }
    for name, wanted in names.items():
        case = cases.get(name)
        if case is None or case.get("passed") is not True:
            raise VerificationError(f"consumer known-answer case failed: {name}")
        if case.get("actual_samples", []) != wanted:
            raise VerificationError(f"consumer samples for {name}: {case.get('actual_samples')} != {wanted}")
        if case.get("panicked") is not False or case.get("actual_error", "") != "":
            raise VerificationError(f"consumer unexpected failure for {name}")

    for name in expected["consumer"]["required_invalid_cases"]:
        case = cases.get(name)
        if case is None or case.get("passed") is not True or case.get("error_is_expected") is not True or case.get("returned_nil") is not True or case.get("panicked") is not False:
            raise VerificationError(f"consumer invalid-dimension case failed: {name}")
    ownership = {entry["name"]: entry for entry in report.get("ownership", [])}
    for name in expected["consumer"]["required_ownership_cases"]:
        entry = ownership.get(name)
        if entry is None or entry.get("passed") is not True or entry.get("output_independent_after_input_mutation") is not True:
            raise VerificationError(f"consumer ownership case failed: {name}")
    for entry in report.get("dimensions", []):
        if entry["name"] == "source-frame-boundary" and entry.get("doubled_source_representable") is not False:
            raise VerificationError("source frame boundary oracle is not rejecting the first invalid channel count")
        if entry["name"] == "source-frame-boundary" and entry.get("positive_zero_wrap_reachable") is not False:
            raise VerificationError("source frame zero-wrap proof is missing")
        if entry["name"] == "target-sample-byte-boundary" and entry.get("output_bytes_representable") is not False:
            raise VerificationError("target sample-byte boundary oracle is not rejecting the first invalid channel count")


def characterize(revision_argument: str) -> dict[str, object]:
    revision = source_revision_or_head(revision_argument)
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c28-characterize-") as temporary:
        binary = Path(temporary) / "pcm-consumer"
        build_from_source(revision, binary)
        report, process = consumer_json(binary, "characterize", f"characterize-{revision[:12]}", revision)
        cases = {case["name"]: case for case in report.get("cases", [])}
        pre_fix_defects = {
            "source_frame_empty_or_tiny": any(
                cases[name].get("panicked") is True or not cases[name].get("error_is_expected", False)
                for name in ("empty-max-source-channels", "tiny-max-source-channels")
            ),
            "wrapped_source_frame_error": cases["tiny-first-wrapped-frame-size"].get("actual_error", "") != "" and not cases["tiny-first-wrapped-frame-size"].get("error_is_expected", False),
            "float_length_failure": cases["float-length-boundary"].get("panicked") is True or not cases["float-length-boundary"].get("error_is_expected", False),
        }
        if not any(pre_fix_defects.values()):
            raise VerificationError("pre-fix characterization did not observe a defect or required wrong error")
        evidence = {
            "schema": "audio-runtime-c28-characterization.v1",
            "mode": "characterize",
            "source_revision": revision,
            "consumer_sha256": sha256(binary),
            "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
            "command": process["command"],
            "result": process,
            "report": report,
            "pre_fix_defects": pre_fix_defects,
            "hazardous_target_allocation_called": False,
        }
    write_json(EVIDENCE_DIR / f"characterize-{revision[:12]}.json", evidence)
    return evidence


def load_fixture(path_argument: str | None, expected: dict[str, object]) -> Path:
    fixture = Path(path_argument).resolve() if path_argument else DEFAULT_FIXTURE.resolve()
    if not fixture.is_file():
        raise VerificationError(f"replay fixture prerequisite is unavailable: {fixture}")
    expected_hash = expected["replay"]["fixture_sha256"]
    actual_hash = sha256(fixture)
    if actual_hash != expected_hash:
        raise VerificationError(f"replay fixture hash mismatch: {actual_hash} != {expected_hash}")
    return fixture


def expected_pcm(path_name: str, expected: dict[str, object]) -> bytes:
    path = (EXPECTED_PATH.parent / path_name).resolve()
    if not path.is_file():
        raise VerificationError(f"expected PCM prerequisite is unavailable: {path}")
    actual_hash = sha256(path)
    replay = expected["replay"]
    if path_name == replay["expected_rendered_pcm_file"]:
        wanted_hash = replay["expected_rendered_pcm_sha256"]
        wanted_bytes = replay["expected_rendered_pcm_bytes"]
    else:
        wanted_hash = replay["expected_provider_pcm_sha256"]
        wanted_bytes = replay["expected_provider_pcm_bytes"]
    if actual_hash != wanted_hash or path.stat().st_size != wanted_bytes:
        raise VerificationError(f"expected PCM provenance mismatch for {path}")
    return path.read_bytes()


def check_timeline(bundle: Path, expected: dict[str, object]) -> dict[str, object]:
    timeline_path = bundle / "audio-trace" / "timeline.jsonl"
    if not timeline_path.is_file():
        raise VerificationError("replay bundle is missing audio-trace/timeline.jsonl")
    entries = [json.loads(line) for line in timeline_path.read_text(encoding="utf-8").splitlines() if line.strip()]
    kinds = Counter(entry.get("runtime_kind") for entry in entries if entry.get("kind") == "runtime")
    audio = [entry for entry in entries if entry.get("kind") == "audio"]
    timeline_expected = expected["replay"]["timeline"]
    if kinds["provider_wire_send"] + kinds["provider_wire_receive"] != timeline_expected["provider_wire_events"]:
        raise VerificationError(f"provider wire event count mismatch: {kinds}")
    if kinds["tool_call"] != timeline_expected["tool_calls"] or kinds["tool_result"] != timeline_expected["tool_results"]:
        raise VerificationError(f"tool event count mismatch: {kinds}")
    if len(audio) != timeline_expected["audio_frames"]:
        raise VerificationError(f"audio frame count mismatch: {len(audio)}")
    if any(entry.get("sample_rate") != timeline_expected["audio_sample_rate"] or entry.get("sample_count") != timeline_expected["audio_sample_count"] for entry in audio):
        raise VerificationError("audio frame format mismatch")
    starts = [entry.get("start_sample") for entry in audio]
    if starts != timeline_expected["audio_start_samples"]:
        raise VerificationError(f"audio frame start samples mismatch: {starts}")
    if not any(entry.get("kind") == "recording_closed" for entry in entries):
        raise VerificationError("replay bundle has no clean recording_closed event")
    return {"entries": len(entries), "runtime_kinds": dict(kinds), "audio_frames": len(audio), "audio_start_samples": starts}


def replay_workflow(yui: Path, fixture: Path, expected: dict[str, object], run_name: str) -> dict[str, object]:
    rendered_expected = expected_pcm(expected["replay"]["expected_rendered_pcm_file"], expected)
    provider_expected = expected_pcm(expected["replay"]["expected_provider_pcm_file"], expected)
    fixture_root = fixture.parents[2]
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c28-replay-", dir=ARTIFACT_DIR) as temporary:
        root = Path(temporary)
        rendered = root / "rendered.pcm"
        bundle = root / "bundle"
        capture_command = [str(yui), "session", "--replay", str(fixture), "--audio-out", str(rendered), "--record-dir", str(bundle), "--trace-audio"]
        capture = run_bounded(capture_command, fixture_root, REPLAY_TIMEOUT_SECONDS)
        save_process_result(f"{run_name}-capture", capture)
        require_success(capture, "credential-free audio/tool replay")
        stdout = str(capture["stdout"])
        for token in expected["replay"]["stdout_tokens"]:
            if token not in stdout:
                raise VerificationError(f"replay stdout is missing expected token {token!r}")
        actual_rendered = rendered.read_bytes() if rendered.is_file() else b""
        if actual_rendered != rendered_expected:
            raise VerificationError("rendered replay PCM differs from the full expected PCM artifact")
        provider_path = bundle / "audio" / "out-000.pcm"
        actual_provider = provider_path.read_bytes() if provider_path.is_file() else b""
        if actual_provider != provider_expected:
            raise VerificationError("recorded provider PCM differs from the full expected PCM artifact")
        timeline = check_timeline(bundle, expected)
        if not (bundle / "provider.json").is_file() or not (bundle / "session-log.jsonl").is_file() or not (bundle / "audio-trace" / "speaker-enqueued.wav").is_file():
            raise VerificationError("replay bundle is missing a required public evidence artifact")

        directory_command = [str(yui), "session", "replay", str(bundle)]
        directory_replay = run_bounded(directory_command, fixture_root, REPLAY_TIMEOUT_SECONDS)
        save_process_result(f"{run_name}-directory-replay", directory_replay)
        require_success(directory_replay, "recorded-bundle directory replay")
        directory_stdout = str(directory_replay["stdout"])
        for token in expected["replay"]["directory_replay_tokens"]:
            if token not in directory_stdout:
                raise VerificationError(f"directory replay stdout is missing expected token {token!r}")
        return {
            "fixture": str(fixture),
            "fixture_sha256": sha256(fixture),
            "capture": capture,
            "directory_replay": directory_replay,
            "rendered_pcm_bytes": len(actual_rendered),
            "rendered_pcm_sha256": hashlib.sha256(actual_rendered).hexdigest(),
            "provider_pcm_bytes": len(actual_provider),
            "provider_pcm_sha256": hashlib.sha256(actual_provider).hexdigest(),
            "timeline": timeline,
            "clean_shutdown": True,
        }


def update_artifact_metadata(source_revision: str, consumer: Path, yui: Path) -> dict[str, object]:
    expected = load_expected()
    artifacts = {
        "source_revision": source_revision,
        "consumer_sha256": sha256(consumer),
        "yui_sha256": sha256(yui),
        "consumer_source_sha256": sha256(CONSUMER_SOURCE),
    }
    expected["artifacts"] = artifacts
    write_json(EXPECTED_PATH, expected)
    write_json(EVIDENCE_DIR / "artifact-manifest.json", {"schema": "audio-runtime-c28-artifact-manifest.v1", **artifacts, "expected_effects_sha256": sha256(EXPECTED_PATH)})
    return artifacts


def verify(source_argument: str | None) -> dict[str, object]:
    source_revision = source_revision_or_head(source_argument)
    consumer = ARTIFACT_DIR / "pcm-consumer"
    yui = ARTIFACT_DIR / "yui"
    build = build_from_source(source_revision, consumer, yui)
    expected = load_expected()
    consumer_report, consumer_process = consumer_json(consumer, "verify", f"verify-consumer-{source_revision[:12]}", source_revision)
    validate_consumer_report(consumer_report, expected, source_revision)
    fixture = load_fixture(None, expected)
    replay = replay_workflow(yui, fixture, expected, f"verify-replay-{source_revision[:12]}")
    artifacts = update_artifact_metadata(source_revision, consumer, yui)
    evidence = {
        "schema": "audio-runtime-c28-verify.v1",
        "mode": "verify",
        "source_revision": source_revision,
        "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
        "build": build,
        "artifacts": artifacts,
        "consumer": {"path": str(consumer), "sha256": sha256(consumer), "process": consumer_process, "report": consumer_report},
        "replay": replay,
        "clean_shutdown": True,
    }
    write_json(EVIDENCE_DIR / "verify.json", evidence)
    return evidence


def probe(consumer_argument: str, yui_argument: str, effects_argument: str, fixture_argument: str | None) -> dict[str, object]:
    consumer = Path(consumer_argument).resolve()
    yui = Path(yui_argument).resolve()
    effects_path = Path(effects_argument).resolve()
    if effects_path != EXPECTED_PATH.resolve():
        raise VerificationError("probe effects must be the immutable C28 expected-effects.json")
    if not consumer.is_file() or not yui.is_file():
        raise VerificationError("probe consumer or yui prerequisite is unavailable")
    expected = load_expected()
    artifacts = expected.get("artifacts")
    if not isinstance(artifacts, dict) or not artifacts.get("source_revision"):
        raise VerificationError("probe artifact source/hash provenance is unavailable")
    if sha256(consumer) != artifacts.get("consumer_sha256") or sha256(yui) != artifacts.get("yui_sha256"):
        raise VerificationError("probe executable hash does not match the immutable artifact manifest")
    if sha256(CONSUMER_SOURCE) != artifacts.get("consumer_source_sha256"):
        raise VerificationError("probe consumer source hash does not match the immutable artifact manifest")
    consumer_report, consumer_process = consumer_json(consumer, "verify", "probe-consumer", str(artifacts["source_revision"]))
    validate_consumer_report(consumer_report, expected, str(artifacts["source_revision"]))
    fixture = load_fixture(fixture_argument, expected)
    replay = replay_workflow(yui, fixture, expected, "probe-replay")
    evidence = {
        "schema": "audio-runtime-c28-probe.v1",
        "mode": "probe",
        "source_revision": artifacts["source_revision"],
        "artifacts": artifacts,
        "consumer": {"path": str(consumer), "process": consumer_process, "report": consumer_report},
        "replay": replay,
        "clean_shutdown": True,
    }
    write_json(EVIDENCE_DIR / "probe.json", evidence)
    return evidence


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("characterize", "verify", "probe"), required=True)
    parser.add_argument("--source-revision")
    parser.add_argument("--consumer")
    parser.add_argument("--yui")
    parser.add_argument("--effects")
    parser.add_argument("--fixture")
    args = parser.parse_args()
    try:
        if args.mode == "characterize":
            result = characterize(args.source_revision or "")
        elif args.mode == "verify":
            result = verify(args.source_revision)
        else:
            if not args.consumer or not args.yui or not args.effects:
                raise VerificationError("probe requires --consumer, --yui, and --effects")
            result = probe(args.consumer, args.yui, args.effects, args.fixture)
    except (OSError, subprocess.SubprocessError, VerificationError) as exc:
        print(f"C28 verification failed: {exc}", file=sys.stderr)
        return 1
    print(json.dumps({"mode": args.mode, "source_revision": result.get("source_revision"), "clean_shutdown": result.get("clean_shutdown"), "evidence": str(EVIDENCE_DIR)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
