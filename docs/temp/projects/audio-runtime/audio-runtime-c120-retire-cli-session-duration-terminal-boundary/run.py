#!/usr/bin/env python3
"""Run the source-pinned, credential-free C120 yui vertical probe."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures"
TOOL_FIXTURE = FIXTURES / "c16-audio-tool.session.json"
INTERRUPTION_FIXTURE = FIXTURES / "c16-interruption.session.json"
TOOL_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
INTERRUPTION_FIXTURE_SHA256 = "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206"
TOOL_PROVIDER_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
TOOL_RENDERED_SHA256 = "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"
INTERRUPTION_RENDERED_SHA256 = "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"
OUTPUT_LIMIT = 256 * 1024
TERM_GRACE = 2.0
KILL_GRACE = 2.0
SECRET_ENV_NAMES = (
    "OPENAI_API_KEY",
    "OPENROUTER_API_KEY",
    "GROK_API_KEY",
    "ANTHROPIC_API_KEY",
)


class EvidenceFailure(RuntimeError):
    pass


@dataclass
class CappedOutput:
    data: bytearray
    total: int = 0
    truncated: bool = False

    def append(self, chunk: bytes) -> None:
        self.total += len(chunk)
        remaining = OUTPUT_LIMIT - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total > OUTPUT_LIMIT:
            self.truncated = True

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        if self.truncated:
            value += f"\n[output truncated after {OUTPUT_LIMIT} bytes]\n"
        return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_stream(stream: Any, output: CappedOutput) -> None:
    while True:
        chunk = stream.read(8192)
        if not chunk:
            return
        output.append(chunk)


def process_group_pids(pgid: int) -> list[int]:
    try:
        result = subprocess.run(
            ["ps", "-eo", "pid=,pgid="],
            check=True,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError):
        return []
    pids: list[int] = []
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1] == str(pgid):
            try:
                pids.append(int(fields[0]))
            except ValueError:
                pass
    return pids


def signal_group(pgid: int, signum: signal.Signals) -> bool:
    try:
        os.killpg(pgid, signum)
        return True
    except ProcessLookupError:
        return False


def run_child(label: str, argv: list[str], cwd: Path, output_dir: Path, timeout: float) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ)
    for name in SECRET_ENV_NAMES:
        environment.pop(name, None)
    environment["GOWORK"] = "off"
    stdout = CappedOutput(bytearray())
    stderr = CappedOutput(bytearray())
    started = time.monotonic()
    try:
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as error:
        raise EvidenceFailure(f"could not launch {label}: {error}") from error
    assert process.stdout is not None
    assert process.stderr is not None
    readers = [
        threading.Thread(target=read_stream, args=(process.stdout, stdout), daemon=True),
        threading.Thread(target=read_stream, args=(process.stderr, stderr), daemon=True),
    ]
    for reader in readers:
        reader.start()
    timed_out = False
    sigterm_sent = False
    sigkill_sent = False
    try:
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            sigterm_sent = signal_group(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=TERM_GRACE)
            except subprocess.TimeoutExpired:
                sigkill_sent = signal_group(process.pid, signal.SIGKILL)
                process.wait(timeout=KILL_GRACE)
        if process.poll() is None:
            sigkill_sent = signal_group(process.pid, signal.SIGKILL) or sigkill_sent
            process.wait(timeout=KILL_GRACE)
    finally:
        for reader in readers:
            reader.join(timeout=KILL_GRACE if timed_out else TERM_GRACE)
        for stream in (process.stdout, process.stderr):
            stream.close()
    survivors = process_group_pids(process.pid)
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "deadline_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "stdout_bytes": stdout.total,
        "stderr_bytes": stderr.total,
        "stdout_truncated": stdout.truncated,
        "stderr_truncated": stderr.truncated,
        "cleanup": {
            "sigterm_sent": sigterm_sent,
            "sigkill_sent": sigkill_sent,
            "surviving_process_group_pids": survivors,
        },
        "credential_free_environment": all(name not in environment for name in SECRET_ENV_NAMES),
    }
    (output_dir / "process.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_process(result: dict[str, Any], expected_exit: int) -> None:
    if result["timed_out"] or result["exit_code"] != expected_exit:
        raise EvidenceFailure(
            f"{result['label']} exit={result['exit_code']} timed_out={result['timed_out']} "
            f"want exit={expected_exit}: {result['stderr']}"
        )
    if result["cleanup"]["surviving_process_group_pids"]:
        raise EvidenceFailure(f"{result['label']} left process-group survivors: {result['cleanup']}")
    if not result["credential_free_environment"] or result["stdout_truncated"] or result["stderr_truncated"]:
        raise EvidenceFailure(f"{result['label']} violated credential/output bounds")


def manifest(case_dir: Path) -> dict[str, Any]:
    path = case_dir / "bundle/manifest.json"
    if not path.is_file():
        raise EvidenceFailure(f"missing terminal replay artifact: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid terminal replay artifact {path}: {error}") from error
    terminal = value.get("terminal")
    if not isinstance(terminal, dict):
        raise EvidenceFailure(f"terminal metadata missing from {path}")
    names = {item.get("path") for item in value.get("artifacts", []) if isinstance(item, dict)}
    if not {"session-log.jsonl", "provider.json"}.issubset(names):
        raise EvidenceFailure(f"terminal replay artifact is incomplete: {value}")
    return value


def common_argv(artifact: Path, fixture: Path, case_dir: Path, *, max_duration: str, recorded: bool) -> list[str]:
    workdir = case_dir / "workdir"
    (workdir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    argv = [
        str(artifact),
        "-C",
        str(case_dir / "config"),
        "--workdir",
        str(workdir),
        "--allow-path",
        str(workdir),
        "session",
        "--replay",
        str(fixture),
        "--prompt",
        "probe PROBE_TOOL_MARKER_9182",
    ]
    if recorded:
        argv.extend(["--replay-timing", "recorded"])
    argv.extend(
        [
            "--audio-out",
            str(case_dir / "rendered.pcm"),
            "--record-dir",
            str(case_dir / "bundle"),
            "--max-duration",
            max_duration,
            "--trace-audio",
        ]
    )
    return argv


def check_terminal(value: dict[str, Any], expected: dict[str, str], label: str) -> None:
    actual = value.get("terminal", {})
    for key, want in expected.items():
        if actual.get(key) != want:
            raise EvidenceFailure(f"{label} terminal {key}={actual.get(key)!r}, want {want!r}")


def run_case(case: str, artifact: Path, run_dir: Path, timeout: float, deadline: float) -> dict[str, Any]:
    if time.monotonic() >= deadline:
        raise EvidenceFailure(f"aggregate probe deadline exceeded before {case}")
    case_dir = run_dir / case
    if case in {"max-duration-replay", "loop-close-negative"}:
        fixture = TOOL_FIXTURE
        argv = common_argv(artifact, fixture, case_dir, max_duration="45ms", recorded=True)
        expected_exit = 1
    elif case in {"bounded-provider-close", "healthy-audio-tool-continuation"}:
        fixture = TOOL_FIXTURE
        argv = common_argv(artifact, fixture, case_dir, max_duration="60s", recorded=False)
        expected_exit = 0
    else:
        raise EvidenceFailure(f"unknown case {case}")
    result = run_child(case, argv, case_dir / "workdir", case_dir / "process", min(timeout, deadline - time.monotonic()))
    require_process(result, expected_exit)
    value = manifest(case_dir)
    terminal = value["terminal"]
    if case == "max-duration-replay":
        check_terminal(value, {"reason": "max_duration", "classification": "max_duration", "terminal_reason": "max_duration", "terminal_provenance": "loop", "output_state": "partial"}, case)
        combined = result["stdout"] + "\n" + result["stderr"]
        if "terminal_provenance=provider" in combined or "live session exceeded maximum duration" not in combined:
            raise EvidenceFailure(f"{case} was not a bounded loop-close negative: {result}")
    elif case == "loop-close-negative":
        check_terminal(value, {"reason": "max_duration", "classification": "max_duration", "terminal_reason": "max_duration", "terminal_provenance": "loop", "output_state": "partial"}, case)
        if terminal.get("terminal_provenance") == "provider" or "terminal_provenance=provider" in result["stdout"]:
            raise EvidenceFailure(f"{case} misclassified loop shutdown as provider evidence")
    else:
        check_terminal(value, {"reason": "fixture_complete", "classification": "provider_close", "terminal_reason": "provider_close", "terminal_provenance": "provider", "output_state": "not_applicable"}, case)
        marker = case_dir / "workdir/evidence/runs/exec-invocations-v4.log"
        rendered = case_dir / "rendered.pcm"
        if "PROBE_TOOL_MARKER_9182" not in result["stdout"] or "strict replay continuation" not in result["stdout"]:
            raise EvidenceFailure(f"{case} did not expose the healthy tool continuation")
        if not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8"):
            raise EvidenceFailure(f"{case} did not preserve the tool side effect")
        if not rendered.is_file() or rendered.stat().st_size != 3200 or sha256_file(rendered) != TOOL_RENDERED_SHA256:
            raise EvidenceFailure(f"{case} rendered PCM changed: {rendered}")
        provider = case_dir / "bundle/audio/out-000.pcm"
        if not provider.is_file() or provider.stat().st_size != 4800 or sha256_file(provider) != TOOL_PROVIDER_SHA256:
            raise EvidenceFailure(f"{case} provider PCM artifact changed: {provider}")
    return {
        "case": case,
        "terminal": terminal,
        "process": result,
        "manifest": str(case_dir / "bundle/manifest.json"),
        "fixture": str(fixture),
        "fixture_sha256": sha256_file(fixture),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", action="append", choices=("max-duration-replay", "bounded-provider-close", "loop-close-negative", "healthy-audio-tool-continuation"), dest="cases")
    parser.add_argument("--artifact", type=Path, default=HERE / "artifacts/yui")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=300.0)
    args = parser.parse_args()
    cases = args.cases or ["max-duration-replay", "bounded-provider-close", "loop-close-negative", "healthy-audio-tool-continuation"]
    started = time.monotonic()
    run_dir = HERE / "runs" / f"vertical-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    try:
        artifact = args.artifact.expanduser().resolve()
        if not artifact.is_file():
            raise EvidenceFailure(f"source-pinned yui artifact is unavailable: {artifact}")
        if subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip() != str(ROOT):
            raise EvidenceFailure(f"probe source root is not the admitted checkout: {ROOT}")
        if not TOOL_FIXTURE.is_file() or sha256_file(TOOL_FIXTURE) != TOOL_FIXTURE_SHA256:
            raise EvidenceFailure(f"source-pinned tool fixture changed: {TOOL_FIXTURE}")
        if not INTERRUPTION_FIXTURE.is_file() or sha256_file(INTERRUPTION_FIXTURE) != INTERRUPTION_FIXTURE_SHA256:
            raise EvidenceFailure(f"source-pinned interruption fixture changed: {INTERRUPTION_FIXTURE}")
        deadline = started + args.aggregate_timeout
        reports = [run_case(case, artifact, run_dir, args.child_timeout, deadline) for case in cases]
        outcome = {
            "status": "pass",
            "decision": "VERTICAL_PROBE_PASS",
            "source_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(),
            "artifact": str(artifact),
            "artifact_sha256": sha256_file(artifact),
            "fixture_hashes": {"audio_tool": TOOL_FIXTURE_SHA256, "interruption": INTERRUPTION_FIXTURE_SHA256},
            "cases": reports,
            "elapsed_seconds": round(time.monotonic() - started, 6),
            "aggregate_timeout_seconds": args.aggregate_timeout,
        }
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        outcome = {"status": "failed", "decision": "VERTICAL_PROBE_FAIL", "error": str(error), "run_dir": str(run_dir)}
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
