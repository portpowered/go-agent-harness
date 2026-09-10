#!/usr/bin/env python3
"""Bounded C35 canonical PCM byte-mixing verifier.

The verifier keeps source, consumer, and public replay evidence separate. It
never polls CI and every child is launched in a new process group with bounded
timeout cleanup.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any


EVIDENCE_DIR = Path(__file__).resolve().parent


def git_top_level(path: Path) -> Path:
    result = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=path,
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(result.stdout.strip()).resolve()


REPO_ROOT = git_top_level(EVIDENCE_DIR)
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(REPO_ROOT))).resolve()
EVIDENCE_REL = EVIDENCE_DIR.relative_to(REPO_ROOT)
CONSUMER_DIR_REL = EVIDENCE_REL / "consumer"
MAX_CHILD_TIMEOUT_SECONDS = 60.0
MAX_AGGREGATE_TIMEOUT_SECONDS = 600.0
CLEANUP_TIMEOUT_SECONDS = 5.0

STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
PLANNING_REVISION = "c61ee2774986c896560ee40a92441c914976000d"
OWNED_PATHS = {
    "go-audio/pkg/mixer/pcm_mix.go",
    "go-audio/pkg/mixer/pcm_mix_test.go",
    "agent-cli/internal/room/mixer.go",
    "agent-cli/internal/room/mixer_test.go",
}

TOOL_FIXTURE = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4/artifact-2.json"
INTERRUPTION_FIXTURE = FACTORY_ROOT / "docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4/artifact-3.json"
FIXTURE_HASHES = {
    TOOL_FIXTURE: "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
    INTERRUPTION_FIXTURE: "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
}
PCM_EXPECTATIONS = {
    "tool_provider": (4800, "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"),
    "tool_rendered": (3200, "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"),
    "interruption_provider": (3840, "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"),
    "interruption_rendered": (3360, "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"),
    "interruption_healthy_tail": (2400, "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"),
}


class VerificationError(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_file(path: Path, label: str) -> Path:
    if not path.is_file():
        raise VerificationError(f"missing {label}: {path}")
    return path


def require_hash(path: Path, expected: str, label: str) -> dict[str, Any]:
    require_file(path, label)
    actual = sha256_file(path)
    if actual != expected:
        raise VerificationError(f"{label} hash={actual}, want {expected}")
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": actual}


def clip(value: str, limit: int = 16000) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + "\n...[clipped]"


def process_group_alive(pid: int) -> bool:
    if os.name == "nt":
        return False
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def send_group_signal(pid: int, sig: signal.Signals) -> None:
    if os.name == "nt":
        return
    try:
        os.killpg(pid, sig)
    except ProcessLookupError:
        pass


class Runner:
    def __init__(self, output_root: Path, child_timeout: float, aggregate_timeout: float) -> None:
        if child_timeout <= 0 or child_timeout > MAX_CHILD_TIMEOUT_SECONDS:
            raise VerificationError(f"child timeout must be in (0,{MAX_CHILD_TIMEOUT_SECONDS}]")
        if aggregate_timeout <= 0 or aggregate_timeout > MAX_AGGREGATE_TIMEOUT_SECONDS:
            raise VerificationError(f"aggregate timeout must be in (0,{MAX_AGGREGATE_TIMEOUT_SECONDS}]")
        self.output_root = output_root
        self.log_root = output_root / "logs"
        self.log_root.mkdir(parents=True, exist_ok=True)
        self.child_timeout = child_timeout
        self.aggregate_timeout = aggregate_timeout
        self.started = time.monotonic()
        self.counter = 0
        self.commands: list[dict[str, Any]] = []

    def _remaining(self) -> float:
        remaining = self.aggregate_timeout - (time.monotonic() - self.started)
        if remaining <= 0:
            raise VerificationError("aggregate verifier deadline exceeded")
        return remaining

    def command(
        self,
        argv: list[str],
        cwd: Path,
        label: str,
        *,
        timeout: float | None = None,
        environment: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        requested = self.child_timeout if timeout is None else timeout
        if requested <= 0 or requested > MAX_CHILD_TIMEOUT_SECONDS:
            raise VerificationError(f"{label} requested timeout {requested}, maximum is {MAX_CHILD_TIMEOUT_SECONDS}")
        effective = min(requested, self._remaining())
        self.counter += 1
        safe_label = "".join(character if character.isalnum() or character in "-_" else "_" for character in label)
        stdout_path = self.log_root / f"{self.counter:03d}-{safe_label}.stdout.log"
        stderr_path = self.log_root / f"{self.counter:03d}-{safe_label}.stderr.log"
        env = os.environ.copy()
        if environment:
            env.update(environment)
        started = time.monotonic()
        timed_out = False
        cleanup_bounded = True
        with stdout_path.open("wb") as stdout_stream, stderr_path.open("wb") as stderr_stream:
            process = subprocess.Popen(
                argv,
                cwd=str(cwd),
                stdin=subprocess.DEVNULL,
                stdout=stdout_stream,
                stderr=stderr_stream,
                env=env,
                start_new_session=(os.name != "nt"),
            )
            try:
                process.wait(timeout=effective)
            except subprocess.TimeoutExpired:
                timed_out = True
                send_group_signal(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=CLEANUP_TIMEOUT_SECONDS)
                except subprocess.TimeoutExpired:
                    send_group_signal(process.pid, signal.SIGKILL)
                    try:
                        process.wait(timeout=CLEANUP_TIMEOUT_SECONDS)
                    except subprocess.TimeoutExpired:
                        cleanup_bounded = False
                        process.kill()
                        try:
                            process.wait(timeout=1.0)
                        except subprocess.TimeoutExpired:
                            cleanup_bounded = False
        duration = time.monotonic() - started
        group_alive = process_group_alive(process.pid)
        if group_alive:
            send_group_signal(process.pid, signal.SIGKILL)
            cleanup_bounded = False
            try:
                process.wait(timeout=CLEANUP_TIMEOUT_SECONDS)
            except subprocess.TimeoutExpired:
                cleanup_bounded = False
            group_alive = process_group_alive(process.pid)
        stdout = stdout_path.read_text(errors="replace")
        stderr = stderr_path.read_text(errors="replace")
        result = {
            "argv": argv,
            "cwd": str(cwd),
            "label": label,
            "duration_seconds": round(duration, 6),
            "exit_code": process.returncode,
            "timed_out": timed_out,
            "cleanup_bounded": cleanup_bounded,
            "process_group_alive": group_alive,
            "stdout_log": str(stdout_path),
            "stderr_log": str(stderr_path),
            "stdout": clip(stdout),
            "stderr": clip(stderr),
        }
        self.commands.append(result)
        if time.monotonic() - self.started > self.aggregate_timeout:
            raise VerificationError(f"{label} exceeded aggregate verifier deadline")
        return result


def require_success(record: dict[str, Any], label: str) -> None:
    if record["exit_code"] != 0:
        raise VerificationError(
            f"{label} exited {record['exit_code']}\nstdout={record['stdout']}\nstderr={record['stderr']}"
        )
    if record["timed_out"] or record["process_group_alive"]:
        raise VerificationError(f"{label} did not shut down cleanly: {record}")


def git_output(runner: Runner, source_root: Path, *args: str, label: str) -> str:
    record = runner.command(["git", *args], source_root, label)
    require_success(record, label)
    return record["stdout"].strip()


def source_revision(runner: Runner, source_root: Path, requested: str | None) -> str:
    revision = requested or git_output(runner, source_root, "rev-parse", "HEAD", label="git-rev-parse-head")
    verified = git_output(runner, source_root, "rev-parse", "--verify", f"{revision}^{{commit}}", label="git-verify-source")
    if verified != revision:
        raise VerificationError(f"source revision resolved to {verified}, want exact {revision}")
    status = git_output(runner, source_root, "status", "--porcelain=v1", "--untracked-files=all", label="git-source-status")
    if status:
        raise VerificationError(f"source root is not clean at {revision}: {status}")
    return revision


def require_ancestry(runner: Runner, source_root: Path, revision: str, ancestor: str, label: str) -> None:
    record = runner.command(["git", "merge-base", "--is-ancestor", ancestor, revision], source_root, label)
    require_success(record, label)


def changed_path_allowlist(
    runner: Runner, source_root: Path, source_revision: str, candidate_revision: str
) -> dict[str, Any]:
    record = runner.command(
        ["git", "diff", "--name-only", source_revision, candidate_revision],
        source_root,
        "git-changed-paths",
    )
    require_success(record, "git changed paths")
    changed = [line.strip() for line in record["stdout"].splitlines() if line.strip()]
    evidence_prefix = f"{EVIDENCE_REL.as_posix()}/"
    allowed = sorted(OWNED_PATHS | {evidence_prefix})
    outside = [
        path
        for path in changed
        if path not in OWNED_PATHS and not path.startswith(evidence_prefix)
    ]
    malformed = [path for path in changed if path.startswith("/") or "\x00" in path]
    if outside or malformed:
        raise VerificationError(
            f"changed-path allowlist violation: outside={outside}, malformed={malformed}, changed={changed}"
        )
    return {"changed": changed, "allowed": allowed, "outside": outside, "malformed": malformed}


def require_input_equality(
    runner: Runner, source_root: Path, source_revision: str, candidate_revision: str, paths: list[Path]
) -> dict[str, Any]:
    relative = [str(path.relative_to(source_root)) for path in paths]
    record = runner.command(
        ["git", "diff", "--quiet", source_revision, candidate_revision, "--", *relative],
        source_root,
        "git-evidence-input-equality",
    )
    if record["exit_code"] != 0:
        raise VerificationError(
            f"evidence-only candidate changed executable inputs: {relative}; {record}"
        )
    return {"paths": relative, "equal": True, "command": record}


def source_inputs(source_root: Path) -> list[Path]:
    relative_paths = [
        Path("go-audio/go.mod"),
        Path("go-audio/go.sum"),
        Path("go-audio/pkg/codec/pcm16.go"),
        Path("go-audio/pkg/mixer/pcm_mix.go"),
        Path("go-audio/pkg/mixer/mixer.go"),
        Path("agent-cli/go.mod"),
        Path("agent-cli/go.sum"),
        Path("agent-cli/internal/room/mixer.go"),
        Path("agent-cli/cmd/yui/main.go"),
        CONSUMER_DIR_REL / "go.mod",
        CONSUMER_DIR_REL / "cmd/pcm/main.go",
    ]
    return [source_root / path for path in relative_paths if (source_root / path).is_file()]


def inspect_source(source_root: Path) -> dict[str, Any]:
    room_path = source_root / "agent-cli/internal/room/mixer.go"
    mix_path = source_root / "go-audio/pkg/mixer/pcm_mix.go"
    room = require_file(room_path, "legacy room mixer").read_text()
    mix = require_file(mix_path, "canonical mixer").read_text()
    if "MixPCM16Bytes(" not in room:
        raise VerificationError("legacy room mixer does not call MixPCM16Bytes")
    if "MixPCM16Samples(" in room:
        raise VerificationError("legacy room mixer still calls MixPCM16Samples directly")
    if "go-audio/pkg/codec" in room:
        raise VerificationError("legacy room mixer still owns a direct codec import")
    for required in ("codec.DecodePCM16Into", "codec.EncodePCM16Into", "MixPCM16Samples"):
        if required not in mix:
            raise VerificationError(f"canonical byte operation missing {required}")
    return {
        "room_mixer": str(room_path),
        "canonical_mixer": str(mix_path),
        "room_calls_canonical_bytes": True,
        "room_direct_codec_import": False,
        "room_direct_sample_mix": False,
        "canonical_reuses_codec_and_sample_mix": True,
    }


def build_source_archive(runner: Runner, source_root: Path, revision: str, output_root: Path) -> dict[str, Any]:
    archive = output_root / "artifacts/source.tar"
    archive.parent.mkdir(parents=True, exist_ok=True)
    record = runner.command(
        ["git", "archive", "--format=tar", f"--output={archive}", revision],
        source_root,
        "git-archive-source",
    )
    require_success(record, "git archive source")
    return {"revision": revision, "path": str(archive), "bytes": archive.stat().st_size, "sha256": sha256_file(archive)}


def build_consumer(runner: Runner, source_root: Path, output_root: Path) -> dict[str, Any]:
    consumer_dir = source_root / CONSUMER_DIR_REL
    require_file(consumer_dir / "go.mod", "consumer go.mod")
    binary = output_root / "artifacts/pcm-consumer"
    binary.parent.mkdir(parents=True, exist_ok=True)
    record = runner.command(
        ["go", "build", "-trimpath", "-o", str(binary), "./cmd/pcm"],
        consumer_dir,
        "build-pcm-consumer",
        environment={"GOWORK": "off"},
    )
    require_success(record, "build PCM consumer")
    return {"path": str(binary), "sha256": sha256_file(binary), "build": record}


def build_yui(runner: Runner, source_root: Path, output_root: Path) -> dict[str, Any]:
    binary = output_root / "artifacts/yui"
    binary.parent.mkdir(parents=True, exist_ok=True)
    record = runner.command(
        ["go", "build", "-trimpath", "-o", str(binary), "./cmd/yui"],
        source_root / "agent-cli",
        "build-yui",
        environment={"GOWORK": "off"},
    )
    require_success(record, "build yui")
    return {"path": str(binary), "sha256": sha256_file(binary), "build": record}


def parse_json_output(record: dict[str, Any], label: str) -> dict[str, Any]:
    try:
        value = json.loads(record["stdout"])
    except json.JSONDecodeError as error:
        raise VerificationError(f"{label} did not emit one JSON report: {error}: {record['stdout']}") from error
    if not isinstance(value, dict):
        raise VerificationError(f"{label} emitted non-object JSON: {value!r}")
    return value


def require_test_discovery(record: dict[str, Any], label: str) -> None:
    discovered = False
    for line in record["stdout"].splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Action") == "run" and event.get("Test"):
            discovered = True
            break
    if not discovered:
        raise VerificationError(f"{label} discovered no individual tests: {record['stdout']}")


def run_consumer(runner: Runner, binary: Path, mode: str, expected_exit: int, label: str) -> tuple[dict[str, Any], dict[str, Any]]:
    record = runner.command([str(binary), "--mode", mode], binary.parent, label)
    if record["exit_code"] != expected_exit:
        raise VerificationError(f"{label} exit={record['exit_code']}, want {expected_exit}: {record}")
    report = parse_json_output(record, label)
    if record["process_group_alive"]:
        raise VerificationError(f"{label} left a process group alive")
    return report, record


def run_consumer_controls(runner: Runner, consumer: dict[str, Any]) -> dict[str, Any]:
    binary = Path(consumer["path"])
    positive, positive_record = run_consumer(runner, binary, "controls", 0, "pcm-consumer-controls")
    required_cases = {"signed-extrema-final-clip", "short-tail-silence", "empty-output"}
    actual_cases = {case.get("name") for case in positive.get("cases", [])}
    if not required_cases.issubset(actual_cases):
        raise VerificationError(f"consumer omitted positive cases: got {sorted(actual_cases)}")
    required_negative = {"negative-output", "oversized-output", "odd-source", "overlong-source", "source-limit"}
    actual_negative = set(positive.get("negative_controls", {}))
    if not required_negative.issubset(actual_negative):
        raise VerificationError(f"consumer omitted negative controls: got {sorted(actual_negative)}")
    if positive.get("clean_shutdown") is not True or positive.get("input_unchanged") is not True or positive.get("output_independent") is not True:
        raise VerificationError(f"consumer positive report omitted isolation/shutdown evidence: {positive}")

    mutated, mutated_record = run_consumer(runner, binary, "mutated", 1, "pcm-consumer-mutated-oracle")
    if mutated.get("expected_failure") is not True or "mutated-expected-sample" not in mutated.get("negative_controls", {}):
        raise VerificationError(f"mutated oracle report did not record intentional rejection: {mutated}")
    if "mutated oracle rejected as expected" not in mutated_record["stderr"]:
        raise VerificationError(f"mutated oracle did not preserve its causal diagnostic: {mutated_record}")
    return {"positive": positive, "positive_process": positive_record, "mutated": mutated, "mutated_process": mutated_record}


def load_jsonl(path: Path, label: str) -> list[dict[str, Any]]:
    require_file(path, label)
    records: list[dict[str, Any]] = []
    for line in path.read_text().splitlines():
        if line.strip():
            value = json.loads(line)
            if not isinstance(value, dict):
                raise VerificationError(f"{label} contains non-object record: {value!r}")
            records.append(value)
    return records


def require_pcm(path: Path, expectation: tuple[int, str], label: str) -> dict[str, Any]:
    expected_bytes, expected_hash = expectation
    result = require_hash(path, expected_hash, label)
    if result["bytes"] != expected_bytes:
        raise VerificationError(f"{label} bytes={result['bytes']}, want {expected_bytes}")
    return result


def make_public_directory(output_root: Path, label: str) -> Path:
    directory = output_root / "runs" / label
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "config").mkdir(exist_ok=True)
    # The admitted tool fixture appends its marker to this relative path.
    # Seed the hermetic replay directory so the accepted command has the same
    # successful filesystem side effect as the captured run.
    invocation_log = directory / "evidence/runs/exec-invocations-v4.log"
    invocation_log.parent.mkdir(parents=True, exist_ok=True)
    invocation_log.touch()
    return directory


def run_yui_capture(
    runner: Runner,
    yui: Path,
    fixture: Path,
    output_root: Path,
    label: str,
    trace_audio: bool,
) -> dict[str, Any]:
    directory = make_public_directory(output_root, label)
    output = directory / "rendered.pcm"
    bundle = directory / "bundle"
    argv = [
        str(yui),
        "-C",
        str(directory / "config"),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(output),
        "--record-dir",
        str(bundle),
        "--max-duration",
        "60s",
    ]
    if trace_audio:
        argv.append("--trace-audio")
    process = runner.command(argv, directory, f"yui-{label}-capture")
    require_success(process, f"yui {label} capture")
    session_log = load_jsonl(bundle / "session-log.jsonl", f"{label} session log")
    return {"directory": directory, "output": output, "bundle": bundle, "process": process, "session_log": session_log}


def run_directory_replay(runner: Runner, yui: Path, bundle: Path, output_root: Path, label: str) -> dict[str, Any]:
    directory = make_public_directory(output_root, f"{label}-directory-replay")
    process = runner.command(
        [str(yui), "-C", str(directory / "config"), "session", "replay", str(bundle)],
        directory,
        f"yui-{label}-directory-replay",
    )
    require_success(process, f"yui {label} directory replay")
    if "replay verified" not in f"{process['stdout']}\n{process['stderr']}".lower():
        raise VerificationError(f"yui {label} replay omitted verification output: {process}")
    return process


def verify_tool_replay(runner: Runner, yui: Path, output_root: Path) -> dict[str, Any]:
    fixture = require_hash(TOOL_FIXTURE, FIXTURE_HASHES[TOOL_FIXTURE], "accepted tool fixture")
    capture = run_yui_capture(runner, yui, TOOL_FIXTURE, output_root, "tool-trace", True)
    if len(capture["session_log"]) != 1:
        raise VerificationError(f"tool session records={len(capture['session_log'])}, want 1")
    serialized = json.dumps(capture["session_log"], sort_keys=True)
    if "PROBE_TOOL_MARKER_9182" not in serialized or "strict replay continuation" not in serialized:
        raise VerificationError("tool replay omitted marker or continuation")
    tool_events = capture["session_log"][0].get("tool_events", [])
    if len(tool_events) != 2 or tool_events[1].get("status") != "completed":
        raise VerificationError(f"tool lifecycle={tool_events!r}")
    provider = require_pcm(capture["bundle"] / "audio/out-000.pcm", PCM_EXPECTATIONS["tool_provider"], "tool provider PCM")
    rendered = require_pcm(capture["output"], PCM_EXPECTATIONS["tool_rendered"], "tool rendered PCM")
    timeline = require_file(capture["bundle"] / "audio-trace/timeline.jsonl", "tool audio timeline")
    replay = run_directory_replay(runner, yui, capture["bundle"], output_root, "tool")

    tampered_bundle = output_root / "runs/tool-tampered/bundle"
    tampered_bundle.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(capture["bundle"], tampered_bundle)
    tampered_pcm = tampered_bundle / "audio/out-000.pcm"
    tampered_data = bytearray(tampered_pcm.read_bytes())
    if not tampered_data:
        raise VerificationError("tool provider PCM was empty before tamper control")
    tampered_data[0] ^= 0x01
    tampered_pcm.write_bytes(tampered_data)
    tampered_directory = make_public_directory(output_root, "tool-tampered-replay")
    tampered = runner.command(
        [str(yui), "-C", str(tampered_directory / "config"), "session", "replay", str(tampered_bundle)],
        tampered_directory,
        "yui-tool-tampered-replay",
    )
    if tampered["exit_code"] == 0:
        raise VerificationError(f"tampered PCM bundle was accepted: {tampered}")
    diagnostic = f"{tampered['stdout']}\n{tampered['stderr']}".lower()
    if not any(word in diagnostic for word in ("integrity", "hash", "pcm", "digest")):
        raise VerificationError(f"tampered PCM bundle failed without an integrity diagnostic: {tampered}")
    return {
        "fixture": fixture,
        "capture": capture["process"],
        "session_records": len(capture["session_log"]),
        "provider": provider,
        "rendered": rendered,
        "timeline": str(timeline),
        "directory_replay": replay,
        "tampered_replay": tampered,
    }


def verify_interruption_replay(runner: Runner, yui: Path, output_root: Path) -> dict[str, Any]:
    fixture = require_hash(INTERRUPTION_FIXTURE, FIXTURE_HASHES[INTERRUPTION_FIXTURE], "accepted interruption fixture")
    capture = run_yui_capture(runner, yui, INTERRUPTION_FIXTURE, output_root, "interruption-trace", True)
    session_log = capture["session_log"]
    if len(session_log) != 2:
        raise VerificationError(f"interruption session records={len(session_log)}, want 2")
    if [entry.get("response", {}).get("audio_bytes") for entry in session_log] != [1440, 2400]:
        raise VerificationError(f"interruption response audio boundaries={session_log!r}")
    if not all(entry.get("response", {}).get("complete") for entry in session_log):
        raise VerificationError(f"interruption responses did not complete: {session_log!r}")
    provider_path = capture["bundle"] / "audio/out-000.pcm"
    provider = require_pcm(provider_path, PCM_EXPECTATIONS["interruption_provider"], "interruption provider PCM")
    rendered = require_pcm(capture["output"], PCM_EXPECTATIONS["interruption_rendered"], "interruption rendered PCM")
    provider_data = provider_path.read_bytes()
    healthy_tail_path = capture["directory"] / "healthy-tail.pcm"
    healthy_tail_path.write_bytes(provider_data[1440:])
    healthy_tail = require_pcm(healthy_tail_path, PCM_EXPECTATIONS["interruption_healthy_tail"], "interruption healthy PCM tail")
    timeline = require_file(capture["bundle"] / "audio-trace/timeline.jsonl", "interruption audio timeline")
    replay = run_directory_replay(runner, yui, capture["bundle"], output_root, "interruption")
    return {
        "fixture": fixture,
        "capture": capture["process"],
        "session_records": len(session_log),
        "provider": provider,
        "rendered": rendered,
        "healthy_tail": healthy_tail,
        "timeline": str(timeline),
        "directory_replay": replay,
    }


def run_public(runner: Runner, yui: dict[str, Any], output_root: Path) -> dict[str, Any]:
    binary = Path(yui["path"])
    help_dir = make_public_directory(output_root, "yui-help")
    help_process = runner.command(
        [str(binary), "session", "--help"],
        help_dir,
        "yui-session-help",
    )
    require_success(help_process, "yui session help")
    tool = verify_tool_replay(runner, binary, output_root)
    interruption = verify_interruption_replay(runner, binary, output_root)
    return {"help": help_process, "tool": tool, "interruption": interruption, "software_replay_only": True}


def run_cleanup_control(runner: Runner, output_root: Path) -> dict[str, Any]:
    directory = make_public_directory(output_root, "intentional-child-hang")
    record = runner.command(
        [sys.executable, "-c", "import time; time.sleep(30)"],
        directory,
        "intentional-child-hang",
        timeout=0.25,
    )
    if not record["timed_out"]:
        raise VerificationError(f"intentional hang control did not time out: {record}")
    if record["process_group_alive"] or not record["cleanup_bounded"]:
        raise VerificationError(f"intentional hang control cleanup failed: {record}")
    return record


def run_focused(runner: Runner, source_root: Path) -> dict[str, Any]:
    commands: list[dict[str, Any]] = []
    for argv, label in (
        (["go", "test", "-json", "-count=1", "-timeout=45s", "./go-audio/pkg/mixer", "./agent-cli/internal/room"], "focused-normal-tests"),
        (["go", "test", "-json", "-race", "-count=1", "-timeout=45s", "./go-audio/pkg/mixer", "./agent-cli/internal/room"], "focused-race-tests"),
        (["go", "vet", "./go-audio/pkg/mixer", "./agent-cli/internal/room"], "focused-vet"),
        (["make", "architecture-size-check"], "architecture-size-check"),
        (["git", "diff", "--check"], "git-diff-check"),
    ):
        record = runner.command(argv, source_root, label)
        require_success(record, label)
        if label.endswith("tests"):
            require_test_discovery(record, label)
        commands.append(record)
    return {"commands": commands, "bounded_child_seconds": 60, "aggregate_seconds": 600}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("focused", "controls", "public", "all"), default="focused")
    parser.add_argument("--source", type=Path, default=REPO_ROOT)
    parser.add_argument("--source-revision")
    parser.add_argument("--yui", type=Path)
    parser.add_argument("--child-timeout-seconds", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout-seconds", type=float, default=600.0)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    source_root = args.source.resolve()
    output_path = args.output.resolve() if args.output else Path(tempfile.gettempdir()) / "audio-runtime-c35-verification.json"
    output_path.parent.mkdir(parents=True, exist_ok=True)
    run_parent = output_path.parent / "runs"
    run_parent.mkdir(parents=True, exist_ok=True)
    output_root = Path(tempfile.mkdtemp(prefix=f"{output_path.stem}-", dir=run_parent))
    try:
        report: dict[str, Any] = {
            "schema": "audio-runtime-c35-canonical-pcm-byte-mixing-verification.v1",
            "project": "audio-runtime",
            "work": "audio-runtime-c35-canonical-pcm-byte-mixing",
            "mode": args.mode,
            "source_root": str(source_root),
            "source_revision": None,
            "candidate_revision": None,
            "startup_integration_revision": STARTUP_INTEGRATION_REVISION,
            "baseline_revision": BASELINE_REVISION,
            "planning_revision": PLANNING_REVISION,
            "commands": [],
            "decision": "FAILED",
        }
        runner = Runner(output_root, args.child_timeout_seconds, args.aggregate_timeout_seconds)
        try:
            if not source_root.is_dir():
                raise VerificationError(f"source root not found: {source_root}")
            tested_revision = source_revision(runner, source_root, args.source_revision)
            report["source_revision"] = tested_revision
            report["candidate_revision"] = git_output(runner, source_root, "rev-parse", "HEAD", label="git-rev-parse-candidate")
            report["changed_path_allowlist"] = changed_path_allowlist(
                runner, source_root, tested_revision, report["candidate_revision"]
            )
            report["toolchain"] = {}
            for argv, key in ((["go", "version"], "go"), ([sys.executable, "--version"], "python")):
                version = runner.command(argv, source_root, f"version-{key}")
                require_success(version, f"{key} version")
                report["toolchain"][key] = {
                    "stdout": version["stdout"],
                    "stderr": version["stderr"],
                }
            require_ancestry(runner, source_root, tested_revision, BASELINE_REVISION, "baseline-ancestry")
            require_ancestry(runner, source_root, tested_revision, STARTUP_INTEGRATION_REVISION, "startup-ancestry")
            require_ancestry(runner, source_root, tested_revision, PLANNING_REVISION, "planning-ancestry")
            input_paths = source_inputs(source_root)
            if report["candidate_revision"] != tested_revision:
                report["evidence_input_equality"] = require_input_equality(
                    runner, source_root, tested_revision, report["candidate_revision"], input_paths
                )
            report["ancestry"] = "accepted"
            report["source_inspection"] = inspect_source(source_root)
            report["source_archive"] = build_source_archive(runner, source_root, tested_revision, output_root)
            report["build_inputs"] = {
                str(path.relative_to(source_root)): sha256_file(path) for path in input_paths
            }
            consumer = build_consumer(runner, source_root, output_root)
            report["consumer_artifact"] = {"path": consumer["path"], "sha256": consumer["sha256"]}
            report["consumer_controls"] = run_consumer_controls(runner, consumer)
            if args.mode in ("focused", "all"):
                report["focused"] = run_focused(runner, source_root)
                report["cleanup_control"] = run_cleanup_control(runner, output_root)
            if args.mode in ("public", "all"):
                yui = {"path": str(args.yui.resolve()), "sha256": sha256_file(args.yui.resolve())} if args.yui else build_yui(runner, source_root, output_root)
                require_file(Path(yui["path"]), "yui binary")
                if args.yui:
                    report["yui_artifact"] = yui
                else:
                    report["yui_artifact"] = yui
                report["public"] = run_public(runner, yui, output_root)
            report["commands"] = runner.commands
            report["duration_seconds"] = round(time.monotonic() - runner.started, 6)
            report["candidate_is_evidence_only_descendant"] = report["candidate_revision"] != report["source_revision"]
            report["realtime_sessions"] = 0
            report["physical_or_acoustic_evidence"] = "not claimed"
            report["decision"] = "ACCEPTED"
        except (OSError, VerificationError, subprocess.SubprocessError) as error:
            report["error"] = str(error)
            report["commands"] = runner.commands
            report["duration_seconds"] = round(time.monotonic() - runner.started, 6)
        output_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print(json.dumps({"decision": report["decision"], "output": str(output_path), "run_root": str(output_root), "source_revision": report["source_revision"], "candidate_revision": report["candidate_revision"], "error": report.get("error")}, sort_keys=True))
        return 0 if report["decision"] == "ACCEPTED" else 1
    except OSError as error:
        fallback = {"decision": "FAILED", "error": str(error), "work": "audio-runtime-c35-canonical-pcm-byte-mixing"}
        output_path.write_text(json.dumps(fallback, indent=2, sort_keys=True) + "\n")
        print(json.dumps(fallback, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
