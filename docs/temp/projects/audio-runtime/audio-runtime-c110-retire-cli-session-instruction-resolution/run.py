#!/usr/bin/env python3
"""Run bounded C110 CLI, replay, pre-provider, and provenance evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import time
from typing import Any, Sequence


EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = next((parent for parent in (EVIDENCE, *EVIDENCE.parents) if (parent / "go.work").is_file()), None)
if REPO_ROOT is None:
    raise RuntimeError("C110 runner could not locate the go.work repository root")
RUNS = EVIDENCE / "runs"
ARTIFACTS = EVIDENCE / "artifacts"
FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
MARKER = "PROBE_TOOL_MARKER_9182"
CHILD_TIMEOUT_SECONDS = 120.0
TOTAL_TIMEOUT_SECONDS = 600.0
TERMINATE_GRACE_SECONDS = 2.0
REAP_TIMEOUT_SECONDS = 2.0
OUTPUT_LIMIT = 1 << 20
SOURCE_FILES = (
    "agent-cli/internal/services/internal/agentruntime/session_instructions.go",
    "agent-cli/internal/services/internal/agentruntime/session_instructions_c110_test.go",
    "go-agent-runtime/services/sessioninstructions/contract.go",
    "go-agent-runtime/services/sessioninstructions/contract_test.go",
    "go-agent-runtime/services/sessioninstructions/internal/service/service.go",
    "go-agent-runtime/services/sessioninstructions/internal/service/service_test.go",
    "go-agent-runtime/services/sessioninstructions/wire/wire.go",
    "go-agent-runtime/services/sessioninstructions/wire/wire_gen.go",
    "go-agent-runtime/services/session/wire/providers.go",
    "go-agent-runtime/services/session/wire/wire_gen.go",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/verify.py",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/run.py",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/consumer/go.mod",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/consumer/main.go",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/baseline.json",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/admission.json",
)


class EvidenceFailure(RuntimeError):
    pass


class Budget:
    def __init__(self, seconds: float) -> None:
        self.started = time.monotonic()
        self.deadline = self.started + seconds

    def remaining(self, label: str) -> float:
        value = self.deadline - time.monotonic()
        if value <= 0:
            raise EvidenceFailure(f"aggregate deadline exceeded before {label}")
        return value


class Capture:
    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.data = bytearray()
        self.exceeded = False

    def append(self, data: bytes) -> None:
        remaining = self.limit - len(self.data)
        if remaining <= 0:
            self.exceeded = bool(data)
            return
        self.data.extend(data[:remaining])
        self.exceeded = self.exceeded or len(data) > remaining

    def text(self) -> str:
        return bytes(self.data).decode(errors="replace")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {error}") from error
    require(isinstance(value, dict), f"JSON artifact is not an object: {path}")
    return value


def sanitized_environment(extra: dict[str, str] | None = None) -> tuple[dict[str, str], list[str]]:
    environment = os.environ.copy()
    removed: list[str] = []
    names = {
        "OPENAI_API_KEY", "OPENAI_API_BASE", "OPENAI_ORG_ID", "ANTHROPIC_API_KEY",
        "AZURE_OPENAI_API_KEY", "REALTIME_API_KEY", "YUI_API_KEY", "OPENROUTER_API_KEY",
        "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
    }
    for name in list(environment):
        if name in names or name.endswith("_API_KEY"):
            removed.append(name)
            del environment[name]
    if extra:
        environment.update(extra)
    return environment, sorted(removed)


def close_stream(selector: selectors.BaseSelector, stream: Any) -> None:
    try:
        selector.unregister(stream)
    except (KeyError, ValueError):
        pass
    try:
        stream.close()
    except OSError:
        pass


def drain(selector: selectors.BaseSelector, streams: dict[str, Capture], until: float) -> None:
    while selector.get_map() and time.monotonic() < until:
        try:
            events = selector.select(min(0.1, max(0.001, until - time.monotonic())))
        except InterruptedError:
            continue
        for key, _ in events:
            stream = key.fileobj
            capture = streams[key.data]
            try:
                data = os.read(stream.fileno(), min(65536, capture.limit - len(capture.data) + 1))
            except (BlockingIOError, OSError):
                data = b""
            if not data:
                close_stream(selector, stream)
            else:
                capture.append(data)
                if capture.exceeded:
                    close_stream(selector, stream)


def terminate_group(process: subprocess.Popen[bytes], selector: selectors.BaseSelector, streams: dict[str, Capture]) -> list[str]:
    signals_sent: list[str] = []
    if os.name == "nt":
        process.terminate()
        signals_sent.append("SIGTERM")
    else:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            signals_sent.append("SIGTERM")
        except ProcessLookupError:
            pass
    drain(selector, streams, time.monotonic() + TERMINATE_GRACE_SECONDS)
    if process.poll() is None or selector.get_map():
        if os.name == "nt":
            process.kill()
            signals_sent.append("SIGKILL")
        else:
            try:
                os.killpg(process.pid, signal.SIGKILL)
                signals_sent.append("SIGKILL")
            except ProcessLookupError:
                pass
        drain(selector, streams, time.monotonic() + REAP_TIMEOUT_SECONDS)
    return signals_sent


def run_bounded(label: str, argv: Sequence[str | Path], cwd: Path, environment: dict[str, str], budget: Budget, timeout: float = CHILD_TIMEOUT_SECONDS) -> dict[str, Any]:
    command = [str(argument) for argument in argv]
    run_dir = RUNS / CURRENT_RUN / label
    run_dir.mkdir(parents=True, exist_ok=True)
    budget.remaining(label)
    deadline = min(time.monotonic() + timeout, budget.deadline)
    started = time.monotonic()
    kwargs: dict[str, Any] = {"stdin": subprocess.DEVNULL, "stdout": subprocess.PIPE, "stderr": subprocess.PIPE, "cwd": cwd, "env": environment, "bufsize": 0}
    if os.name != "nt":
        kwargs["start_new_session"] = True
    process = subprocess.Popen(command, **kwargs)
    streams = {"stdout": Capture(OUTPUT_LIMIT), "stderr": Capture(OUTPUT_LIMIT)}
    selector = selectors.DefaultSelector()
    for name, stream in (("stdout", process.stdout), ("stderr", process.stderr)):
        require(stream is not None, f"{label} {name} pipe was not created")
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ, name)
    timed_out = False
    output_exceeded = False
    signals_sent: list[str] = []
    try:
        while selector.get_map() or process.poll() is None:
            if time.monotonic() >= deadline:
                timed_out = True
                signals_sent = terminate_group(process, selector, streams)
                break
            events = selector.select(min(0.1, max(0.001, deadline - time.monotonic())))
            for key, _ in events:
                stream = key.fileobj
                capture = streams[key.data]
                try:
                    data = os.read(stream.fileno(), min(65536, capture.limit - len(capture.data) + 1))
                except (BlockingIOError, OSError):
                    data = b""
                if not data:
                    close_stream(selector, stream)
                else:
                    capture.append(data)
                    if capture.exceeded:
                        output_exceeded = True
                        signals_sent = terminate_group(process, selector, streams)
                        break
            if timed_out or output_exceeded:
                break
        drain(selector, streams, time.monotonic() + REAP_TIMEOUT_SECONDS)
        reaped = process.poll() is not None
        if not reaped:
            signals_sent.extend(terminate_group(process, selector, streams))
            reaped = process.poll() is not None
    finally:
        for key in list(selector.get_map().values()):
            close_stream(selector, key.fileobj)
        selector.close()
    result = {
        "label": label,
        "argv": command,
        "cwd": str(cwd),
        "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_exceeded": output_exceeded,
        "termination_signals": signals_sent,
        "reaped": reaped,
        "stdout": streams["stdout"].text(),
        "stderr": streams["stderr"].text(),
        "removed_credential_env": [],
    }
    write_json(run_dir / "process.json", result)
    (run_dir / "stdout.log").write_bytes(bytes(streams["stdout"].data))
    (run_dir / "stderr.log").write_bytes(bytes(streams["stderr"].data))
    return result


def command_ok(result: dict[str, Any], label: str) -> None:
    require(result["exit_code"] == 0 and not result["timed_out"] and not result["output_exceeded"], f"{label} failed: {result}")
    require(result["reaped"], f"{label} was not reaped within the cleanup bound")


def build_yui(budget: Budget) -> tuple[Path, dict[str, Any]]:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    binary = ARTIFACTS / "yui"
    environment, removed = sanitized_environment({"GOWORK": str(REPO_ROOT / "go.work")})
    result = run_bounded("yui-build", ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", binary, "./agent-cli/cmd/yui"], REPO_ROOT, environment, budget)
    result["removed_credential_env"] = removed
    write_json(RUNS / CURRENT_RUN / "yui-build/process.json", result)
    command_ok(result, "YUI build")
    require(binary.is_file() and binary.stat().st_size > 0, "YUI build did not produce an artifact")
    artifact = {
        "path": str(binary.relative_to(REPO_ROOT)),
        "bytes": binary.stat().st_size,
        "sha256": sha256_file(binary),
        "source_revision": git_output(["rev-parse", "HEAD"]),
        "source_tree_scope": "C110 evidence build; no CI/review/merge claim",
        "process": result,
    }
    write_json(ARTIFACTS / "yui-build.json", artifact)
    return binary, artifact


def git_output(args: list[str]) -> str:
    result = subprocess.run(["git", *args], cwd=REPO_ROOT, text=True, capture_output=True, check=False)
    require(result.returncode == 0, f"git command failed: {args}: {result.stderr}")
    return result.stdout.strip()


def validate_replay(case_dir: Path, fixture: Path, result: dict[str, Any], label: str) -> dict[str, Any]:
    command_ok(result, label)
    require("replay mismatch" not in (result["stdout"] + result["stderr"]).lower(), f"{label} reported replay mismatch")
    record_dir = case_dir / "tool-record"
    manifest_path = record_dir / "manifest.json"
    pcm_path = record_dir / "audio" / "out-000.pcm"
    provider_path = record_dir / "provider.json"
    session_log = record_dir / "session-log.jsonl"
    require(all(path.is_file() for path in (manifest_path, pcm_path, provider_path, session_log)), f"{label} record bundle is incomplete")
    manifest = read_json(manifest_path)
    require(manifest.get("terminal") == {
        "reason": "fixture_complete",
        "classification": "provider_close",
        "terminal_reason": "provider_close",
        "terminal_provenance": "provider",
        "output_state": "not_applicable",
    }, f"{label} terminal diagnostics changed")
    artifacts = {item.get("path"): item.get("sha256") for item in manifest.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    require(set(artifacts) == expected_paths, f"{label} manifest artifact set changed: {set(artifacts)}")
    for relative, digest in artifacts.items():
        artifact = record_dir / relative
        require(artifact.is_file() and sha256_file(artifact) == digest, f"{label} artifact hash mismatch: {relative}")
    require(sha256_file(provider_path) == sha256_file(fixture), f"{label} provider fixture was changed")
    pcm = pcm_path.read_bytes()
    require(len(pcm) == 4800 and sha256_bytes(pcm) == PCM_SHA256, f"{label} provider PCM oracle changed")
    log_text = session_log.read_text(encoding="utf-8")
    require(MARKER in result["stdout"] and "strict replay continuation" in result["stdout"], f"{label} marker or continuation missing")
    require(MARKER in log_text and "strict replay continuation" in log_text, f"{label} session log marker or continuation missing")
    marker_path = case_dir / "evidence/runs/exec-invocations-v4.log"
    require(marker_path.is_file() and MARKER in marker_path.read_text(encoding="utf-8"), f"{label} tool side effect is missing")
    return {
        "label": label,
        "fixture": str(fixture.relative_to(REPO_ROOT)),
        "fixture_sha256": sha256_file(fixture),
        "manifest": str(manifest_path.relative_to(REPO_ROOT)),
        "manifest_sha256": sha256_file(manifest_path),
        "provider_sha256": sha256_file(provider_path),
        "pcm_bytes": len(pcm),
        "pcm_sha256": sha256_bytes(pcm),
        "terminal": manifest["terminal"],
        "clean_shutdown": result["reaped"] and not result["timed_out"],
        "process": result,
    }


def run_replay(label: str, yui: Path, budget: Budget, *, text_seed: bool = False) -> dict[str, Any]:
    require(FIXTURE.is_file() and sha256_file(FIXTURE) == FIXTURE_SHA256, "accepted replay fixture hash changed")
    case_dir = RUNS / CURRENT_RUN / label
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    record_dir = case_dir / "tool-record"
    audio_out = case_dir / "rendered.wav"
    environment, removed = sanitized_environment()
    argv: list[str | Path] = [yui, "session", "--replay", FIXTURE, "--audio-out", audio_out, "--record-dir", record_dir, "--trace-audio", "--workdir", case_dir, "--allow-path", case_dir]
    if text_seed:
        argv.extend(["--prompt", f"probe {MARKER}"])
    result = run_bounded(label, argv, case_dir, environment, budget)
    result["removed_credential_env"] = removed
    write_json(RUNS / CURRENT_RUN / f"{label}/process.json", result)
    return validate_replay(case_dir, FIXTURE, result, label)


def run_invalid_instruction(budget: Budget) -> dict[str, Any]:
    label = "invalid-instruction"
    environment, removed = sanitized_environment({"GOWORK": str(REPO_ROOT / "go.work")})
    result = run_bounded(
        label,
        ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "^TestC110InstructionResolutionRejectsMalformedAndOversizedFilesBeforePlan$", "-count=1"],
        REPO_ROOT,
        environment,
        budget,
    )
    result["removed_credential_env"] = removed
    write_json(RUNS / CURRENT_RUN / f"{label}/process.json", result)
    command_ok(result, "invalid instruction pre-plan regression")
    return {
        "label": label,
        "cases": ["malformed NUL", "oversized"],
        "rejected_before_provider": True,
        "rejected_before_session_plan": True,
        "clean_shutdown": result["reaped"],
        "process": result,
    }


def run_cli_instruction_positive(budget: Budget) -> dict[str, Any]:
    environment, removed = sanitized_environment({"GOWORK": str(REPO_ROOT / "go.work")})
    result = run_bounded(
        "cli-instruction-positive",
        ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "^TestRunSessionWithInstructions_SourceMatrix$", "-count=1"],
        REPO_ROOT,
        environment,
        budget,
    )
    result["removed_credential_env"] = removed
    write_json(RUNS / CURRENT_RUN / "cli-instruction-positive/process.json", result)
    command_ok(result, "CLI instruction positive")
    return {"test": "TestRunSessionWithInstructions_SourceMatrix", "source_pinned": True, "process": result}


def source_provenance(artifact: dict[str, Any], results: dict[str, Any]) -> dict[str, Any]:
    hashes: dict[str, str] = {}
    for relative in SOURCE_FILES:
        path = REPO_ROOT / relative
        require(path.is_file(), f"provenance source is missing: {relative}")
        hashes[relative] = sha256_file(path)
    return {
        "schema": "audio-runtime-c110-source-provenance.v1",
        "source_revision": git_output(["rev-parse", "HEAD"]),
        "branch": git_output(["branch", "--show-current"]),
        "origin_main": git_output(["rev-parse", "origin/main"]),
        "fixture": {"path": str(FIXTURE.relative_to(REPO_ROOT)), "sha256": sha256_file(FIXTURE)},
        "yui_artifact": artifact,
        "source_hashes": hashes,
        "evidence_results": results,
        "claims": {
            "credential_free": True,
            "bounded_child_deadline_seconds": CHILD_TIMEOUT_SECONDS,
            "bounded_aggregate_deadline_seconds": TOTAL_TIMEOUT_SECONDS,
            "clean_shutdown_is_process_reap_only": True,
            "ci_green": False,
            "independent_review": False,
            "merge": False,
            "project_acceptance": False,
        },
    }


def run_all() -> dict[str, Any]:
    budget = Budget(TOTAL_TIMEOUT_SECONDS)
    yui, artifact = build_yui(budget)
    results = {
        "cli_instruction_positive": run_cli_instruction_positive(budget),
        "audio_tool_replay": run_replay("audio-tool-replay", yui, budget),
        "instruction_text_seed": run_replay("instruction-text-seed", yui, budget, text_seed=True),
        "invalid_instruction": run_invalid_instruction(budget),
    }
    report = {
        "schema": "audio-runtime-c110-run.v1",
        "status": "accepted",
        "source_revision": git_output(["rev-parse", "HEAD"]),
        "aggregate_elapsed_seconds": round(time.monotonic() - budget.started, 6),
        "aggregate_deadline_seconds": TOTAL_TIMEOUT_SECONDS,
        "yui_artifact": artifact,
        "results": results,
        "provenance": source_provenance(artifact, results),
    }
    write_json(RUNS / CURRENT_RUN / "summary.json", report)
    write_json(EVIDENCE / "provenance.json", report["provenance"])
    return report


def main() -> int:
    global CURRENT_RUN
    parser = argparse.ArgumentParser()
    parser.add_argument("cases", nargs="*", choices=("all", "instruction", "replay", "invalid"), default=["all"])
    args = parser.parse_args()
    CURRENT_RUN = f"run-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    if args.cases != ["all"]:
        raise EvidenceFailure("C110 runner keeps one aggregate deadline; use the all case set")
    report = run_all()
    print(json.dumps({"status": report["status"], "source_revision": report["source_revision"], "run": CURRENT_RUN, "yui_sha256": report["yui_artifact"]["sha256"]}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (EvidenceFailure, OSError, subprocess.TimeoutExpired) as error:
        print(f"evidence failed: {error}", file=sys.stderr)
        raise SystemExit(1)
