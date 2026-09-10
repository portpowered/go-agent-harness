#!/usr/bin/env python3
"""Bounded C38 reproduction, causal, regression, and packaging runner.

The runner keeps the failed C30 executable immutable, runs the frozen C21
fixtures through the public yui command, and records enough boundary evidence
to distinguish provider ingress from decoded/output retention.  It never
turns a clean child exit into an audio PASS: PCM bytes, hashes, wire order,
timeline presence, and strict bundle replay are checked independently.
"""

from __future__ import annotations

import argparse
import base64
from dataclasses import dataclass, field
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]


def find_factory_root() -> Path:
    configured = os.environ.get("FACTORY_ROOT", "").strip()
    candidates = [Path(configured).expanduser()] if configured else []
    candidates.extend([ROOT, ROOT.parents[2]])
    for candidate in candidates:
        candidate = candidate.resolve()
        if (candidate / "factory/scripts/project-control.py").is_file():
            return candidate
    return ROOT


FACTORY_ROOT = find_factory_root()
DEFAULT_FIXTURES = HERE / "fixtures"
DEFAULT_EVIDENCE = HERE
LEGACY_SOURCE_REVISION = "2525da44053e5bfe7e2d8fccc55463a645107e29"
LEGACY_YUI_SHA256 = "01864224f0257ad7d934401053e091d40019e74faad37dd69638e3041b7ae446"
LEGACY_CONSUMER_SHA256 = "53c2ff160c262da04ea153a81ba5b0b10f20cc8a66011d50d3c887a1819993d9"
ARCHITECTURE_TOOLS_SHA256 = "debe7ca096d60db699974b7d9a37ba15d146ec3f26a860f45544f96a9e6d547f"
TOOL_PROVIDER_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
TOOL_RENDERED_SHA256 = "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"
INTERRUPTION_PROVIDER_SHA256 = "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"
INTERRUPTION_RENDERED_SHA256 = "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"
HEALTHY_TAIL_SHA256 = "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"
CHILD_TIMEOUT_SECONDS = 60.0
TARGETED_TIMEOUT_SECONDS = 120.0
AGGREGATE_TIMEOUT_SECONDS = 600.0
TERM_GRACE_SECONDS = 2.0
KILL_GRACE_SECONDS = 2.0
OUTPUT_LIMIT_BYTES = 256 * 1024

FIXTURE_TYPES: dict[str, list[str]] = {
    "audio-tool": [
        "session.update",
        "session.created",
        "conversation.item.create",
        "response.create",
        "response.created",
        "response.output_item.added",
        "response.function_call_arguments.done",
        "response.done",
        "conversation.item.create",
        "response.create",
        "response.created",
        "response.output_audio.delta",
        "response.output_audio.delta",
        "response.output_audio.done",
        "response.output_text.delta",
        "response.output_text.done",
        "response.done",
        "session.closed",
    ],
    "interruption": [
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
    ],
}

EXPECTED = {
    "audio-tool": {
        "fixture": "c16-audio-tool.session.json",
        "provider_bytes": 4800,
        "provider_sha256": TOOL_PROVIDER_SHA256,
        "rendered_bytes": 3200,
        "rendered_sha256": TOOL_RENDERED_SHA256,
        "wire_events": 18,
        "tool_calls": 1,
        "terminal": {
            "reason": "fixture_complete",
            "classification": "provider_close",
            "terminal_reason": "provider_close",
            "terminal_provenance": "provider",
            "output_state": "not_applicable",
        },
    },
    "interruption": {
        "fixture": "c16-interruption.session.json",
        "provider_bytes": 3840,
        "provider_sha256": INTERRUPTION_PROVIDER_SHA256,
        "rendered_bytes": 3360,
        "rendered_sha256": INTERRUPTION_RENDERED_SHA256,
        "wire_events": 15,
        "tool_calls": 0,
        "healthy_tail_offset_provider": 1440,
        "healthy_tail_offset_rendered": 960,
        "healthy_tail_bytes": 2400,
        "healthy_tail_sha256": HEALTHY_TAIL_SHA256,
        "terminal": {
            "reason": "replay_complete",
            "classification": "replay_complete",
            "terminal_reason": "replay_complete",
            "terminal_provenance": "replay",
            "output_state": "complete",
        },
    },
}

EXPECTED_CONSUMER_CASES = [
    "8000-mono",
    "8000-stereo",
    "16000-mono",
    "16000-stereo",
    "24000-mono",
    "24000-stereo",
    "44100-mono",
    "44100-stereo",
    "48000-mono",
    "48000-stereo",
    "1hz-one-second",
    "zero-rate",
    "negative-rate",
    "zero-channels",
    "negative-channels",
    "zero-duration",
    "negative-duration",
    "fractional-44100-millisecond",
    "reduced-rate-duration-product",
    "channel-product-overflow",
    "byte-capacity-boundaries",
]


class EvidenceFailure(RuntimeError):
    """A required bounded check did not prove its oracle."""


class RunnerBlocked(RuntimeError):
    """A required host prerequisite is unavailable, not a product failure."""


@dataclass
class CappedOutput:
    limit: int
    data: bytearray = field(default_factory=bytearray)
    total_bytes: int = 0
    truncated: bool = False
    read_error: str = ""

    def append(self, chunk: bytes) -> None:
        self.total_bytes += len(chunk)
        remaining = self.limit - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        if self.total_bytes > self.limit:
            self.truncated = True

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        if self.truncated:
            value += f"\n[output truncated after {self.limit} bytes; read {self.total_bytes} bytes]\n"
        if self.read_error:
            value += f"\n[output reader error: {self.read_error}]\n"
        return value


def read_stream(stream: Any, output: CappedOutput) -> None:
    try:
        while True:
            block = stream.read(8192)
            if not block:
                return
            output.append(block)
    except (OSError, ValueError) as error:
        output.read_error = str(error)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"could not read JSON {path}: {error}") from error


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise EvidenceFailure(f"could not read JSONL {path}: {error}") from error
    records: list[dict[str, Any]] = []
    for number, line in enumerate(lines, 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as error:
            raise EvidenceFailure(f"invalid JSONL {path}:{number}: {error}") from error
        if not isinstance(value, dict):
            raise EvidenceFailure(f"non-object JSONL record {path}:{number}")
        records.append(value)
    if not records:
        raise EvidenceFailure(f"empty JSONL artifact {path}")
    return records


def selected_environment(environment: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "GOWORK", "GOFLAGS", "GOTOOLCHAIN", "FACTORY_ROOT", "CI")
    return {key: environment.get(key, "") for key in keys}


def process_group_pids(pgid: int) -> list[int]:
    if os.name != "posix":
        return []
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
        if len(fields) == 2 and fields[0].isdigit() and fields[1].isdigit() and int(fields[1]) == pgid:
            pids.append(int(fields[0]))
    return pids


def signal_process_group(pgid: int, sig: signal.Signals) -> bool:
    try:
        if os.name == "posix":
            os.killpg(pgid, sig)
            return True
    except ProcessLookupError:
        return False
    return False


def limit_child_resources() -> None:
    if os.name != "posix":
        return
    try:
        import resource

        resource.setrlimit(resource.RLIMIT_CPU, (55, 55))
        resource.setrlimit(resource.RLIMIT_AS, (512 * 1024 * 1024, 512 * 1024 * 1024))
    except (ImportError, OSError, ValueError):
        # The parent watchdog remains mandatory if these limits are absent.
        return


def run_process(
    label: str,
    argv: list[str],
    *,
    cwd: Path,
    run_dir: Path,
    timeout_seconds: float,
    environment: dict[str, str] | None = None,
    term_grace_seconds: float = TERM_GRACE_SECONDS,
    kill_grace_seconds: float = KILL_GRACE_SECONDS,
    output_limit: int = OUTPUT_LIMIT_BYTES,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if environment is None else environment)
    started = time.monotonic()
    try:
        process = subprocess.Popen(
            argv,
            cwd=str(cwd),
            env=selected,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=os.name == "posix",
            preexec_fn=limit_child_resources if os.name == "posix" else None,
        )
    except OSError as error:
        raise RunnerBlocked(f"could not launch {label}: {error}") from error

    stdout = CappedOutput(output_limit)
    stderr = CappedOutput(output_limit)
    readers = [
        threading.Thread(target=read_stream, args=(process.stdout, stdout), daemon=True, name=f"{safe}-stdout"),
        threading.Thread(target=read_stream, args=(process.stderr, stderr), daemon=True, name=f"{safe}-stderr"),
    ]
    for reader in readers:
        reader.start()

    timed_out = False
    cleanup: dict[str, Any] = {
        "sigterm_sent": False,
        "sigkill_sent": False,
        "reaped_after_sigterm": False,
        "reaped_after_sigkill": False,
        "reader_threads_stopped": False,
    }
    pgid = process.pid
    try:
        try:
            process.wait(timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            cleanup["sigterm_sent"] = signal_process_group(pgid, signal.SIGTERM)
            try:
                process.wait(timeout=term_grace_seconds)
                cleanup["reaped_after_sigterm"] = True
            except subprocess.TimeoutExpired:
                cleanup["sigkill_sent"] = signal_process_group(pgid, signal.SIGKILL)
                try:
                    process.wait(timeout=kill_grace_seconds)
                    cleanup["reaped_after_sigkill"] = True
                except subprocess.TimeoutExpired:
                    cleanup["reap_error"] = "parent did not exit after SIGKILL"
    finally:
        survivors_before = process_group_pids(pgid)
        cleanup["survivors_before_final_kill"] = survivors_before
        if survivors_before:
            cleanup["sigkill_sent"] = signal_process_group(pgid, signal.SIGKILL) or cleanup["sigkill_sent"]
        if process.poll() is None:
            try:
                process.wait(timeout=kill_grace_seconds)
                cleanup["reaped_after_sigkill"] = True
            except subprocess.TimeoutExpired:
                cleanup["reap_error"] = "parent remained alive after final SIGKILL"
        for reader in readers:
            reader.join(timeout=kill_grace_seconds if timed_out else term_grace_seconds)
        if any(reader.is_alive() for reader in readers):
            for stream in (process.stdout, process.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except OSError:
                        pass
            for reader in readers:
                reader.join(timeout=0.25)
        cleanup["reader_threads_stopped"] = not any(reader.is_alive() for reader in readers)
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                try:
                    stream.close()
                except OSError:
                    pass

    elapsed = time.monotonic() - started
    survivors = process_group_pids(pgid)
    cleanup["surviving_process_group_pids"] = survivors
    stdout_path.write_bytes(bytes(stdout.data))
    stderr_path.write_bytes(bytes(stderr.data))
    result = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "deadline_seconds": timeout_seconds,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "parent_reaped": process.returncode is not None,
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
        "stdout_bytes": stdout.total_bytes,
        "stderr_bytes": stderr.total_bytes,
        "stdout_truncated": stdout.truncated,
        "stderr_truncated": stderr.truncated,
        "stdout": stdout.text(),
        "stderr": stderr.text(),
        "cleanup": cleanup,
    }
    record_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_exit(result: dict[str, Any], expected: int | None = 0, label: str = "process") -> None:
    if result["timed_out"] or not result["parent_reaped"]:
        raise EvidenceFailure(f"{label} exceeded its bound or was not reaped: {result}")
    if expected is not None and result["exit_code"] != expected:
        raise EvidenceFailure(f"{label} exit={result['exit_code']} want={expected}: {result['stderr']}")


def git_output(source_root: Path, *args: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", str(source_root), *args],
            check=True,
            capture_output=True,
            text=True,
        )
    except (OSError, subprocess.CalledProcessError) as error:
        raise EvidenceFailure(f"git {' '.join(args)} failed in {source_root}: {error}") from error
    return result.stdout.strip()


def validate_source_root(source_root: Path) -> Path:
    source_root = source_root.expanduser().resolve()
    if not (source_root / "go.work").is_file() or not (source_root / "agent-cli" / "go.mod").is_file():
        raise RunnerBlocked(f"source root is not a complete go-agent-harness checkout: {source_root}")
    if Path(git_output(source_root, "rev-parse", "--show-toplevel")).resolve() != source_root:
        raise EvidenceFailure(f"source root is not the checkout root: {source_root}")
    return source_root


def resolve_fixture_dir(path: Path) -> Path:
    path = path.expanduser().resolve()
    if not path.is_dir():
        raise RunnerBlocked(f"fixture directory is unavailable: {path}")
    for filename in ("c16-audio-tool.session.json", "c16-interruption.session.json"):
        if not (path / filename).is_file():
            raise RunnerBlocked(f"frozen fixture is unavailable: {path / filename}")
    expected_hashes = {
        "c16-audio-tool.session.json": "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
        "c16-interruption.session.json": "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
    }
    for filename, expected_hash in expected_hashes.items():
        actual_hash = sha256_file(path / filename)
        if actual_hash != expected_hash:
            raise EvidenceFailure(f"frozen fixture {filename} hash={actual_hash} want={expected_hash}")
    return path


def fixture_inventory(fixtures: Path) -> dict[str, Any]:
    inventory: dict[str, Any] = {}
    for label, expected in EXPECTED.items():
        path = fixtures / expected["fixture"]
        capture = load_json(path)
        records = capture.get("records")
        if not isinstance(records, list):
            raise EvidenceFailure(f"{label} fixture has no records list")
        actual_types = [record.get("type") for record in records]
        if actual_types != FIXTURE_TYPES[label]:
            raise EvidenceFailure(f"{label} fixture order changed: {actual_types}")
        if label == "audio-tool" and not any("PROBE_TOOL_MARKER_9182" in json.dumps(record) for record in records):
            raise EvidenceFailure("audio-tool fixture lost PROBE_TOOL_MARKER_9182")
        inventory[label] = {
            "path": str(path),
            "sha256": sha256_file(path),
            "bytes": path.stat().st_size,
            "wire_events": len(records),
            "types": actual_types,
            "ends_with_disconnect": capture.get("ends_with_disconnect", False),
        }
    return inventory


def candidate_path(candidates: list[Path], description: str) -> Path:
    for candidate in candidates:
        candidate = candidate.expanduser().resolve()
        if candidate.is_file():
            return candidate
    raise RunnerBlocked(f"{description} unavailable; checked: {', '.join(str(path) for path in candidates)}")


def legacy_yui_path() -> Path:
    configured = os.environ.get("C38_ORIGINAL_ARTIFACT", "").strip()
    candidates = [Path(configured)] if configured else []
    candidates.extend(
        [
            FACTORY_ROOT / "docs/temp/probes/audio-runtime-c30-frame-dimension-safety-vertical-probe/artifact-1",
            ROOT / "docs/temp/probes/audio-runtime-c30-frame-dimension-safety-vertical-probe/artifact-1",
        ]
    )
    path = candidate_path(candidates, "immutable C30 yui artifact")
    actual = sha256_file(path)
    if actual != LEGACY_YUI_SHA256:
        raise EvidenceFailure(f"immutable C30 yui hash={actual} want={LEGACY_YUI_SHA256}")
    return path


def legacy_consumer_path() -> Path:
    configured = os.environ.get("C38_ORIGINAL_CONSUMER", "").strip()
    candidates = [Path(configured)] if configured else []
    candidates.extend(
        [
            FACTORY_ROOT / "docs/temp/probes/audio-runtime-c30-frame-dimension-safety-vertical-probe/artifact-0",
            ROOT / "docs/temp/probes/audio-runtime-c30-frame-dimension-safety-vertical-probe/artifact-0",
        ]
    )
    path = candidate_path(candidates, "immutable C30 consumer artifact")
    actual = sha256_file(path)
    if actual != LEGACY_CONSUMER_SHA256:
        raise EvidenceFailure(f"immutable C30 consumer hash={actual} want={LEGACY_CONSUMER_SHA256}")
    return path


def canonical_reference() -> dict[str, Any]:
    report_candidates = [
        FACTORY_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety-vertical-probe.json",
        ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety-vertical-probe.json",
    ]
    decision_candidates = [
        FACTORY_ROOT / "docs/temp/projects/audio-runtime/c30-probe-failure-reconciliation/decision.md",
        ROOT / "docs/temp/projects/audio-runtime/c30-probe-failure-reconciliation/decision.md",
    ]
    report_path = candidate_path(report_candidates, "unchanged C30 canonical vertical report")
    decision_path = candidate_path(decision_candidates, "unchanged C30 failure decision")
    report = load_json(report_path)
    text = decision_path.read_text(encoding="utf-8")
    criteria = report.get("criteria", {})
    replay_evidence = str(criteria.get("REPLAY", {}).get("evidence", ""))
    failures_evidence = str(criteria.get("FAILURES", {}).get("evidence", ""))
    joined = f"{replay_evidence}\n{failures_evidence}\n{text}"
    required = (LEGACY_SOURCE_REVISION, "2400", "3360", HEALTHY_TAIL_SHA256, INTERRUPTION_PROVIDER_SHA256)
    if report.get("sourceRevision") != LEGACY_SOURCE_REVISION or criteria.get("REPLAY", {}).get("verdict") != "FAIL":
        raise EvidenceFailure("C30 canonical report is not the required source-2525 FAILED outcome")
    for marker in required:
        if marker not in joined:
            raise EvidenceFailure(f"C30 canonical failure is missing literal marker {marker!r}")
    return {
        "report_path": str(report_path),
        "report_sha256": sha256_file(report_path),
        "decision_path": str(decision_path),
        "decision_sha256": sha256_file(decision_path),
        "source_revision": report.get("sourceRevision"),
        "replay_verdict": criteria.get("REPLAY", {}).get("verdict"),
        "archived_failure": {
            "provider_bytes": 2400,
            "provider_sha256": HEALTHY_TAIL_SHA256,
            "rendered_bytes": 2400,
            "rendered_sha256": HEALTHY_TAIL_SHA256,
            "expected_provider_bytes": 3840,
            "expected_provider_sha256": INTERRUPTION_PROVIDER_SHA256,
            "expected_rendered_bytes": 3360,
            "expected_rendered_sha256": INTERRUPTION_RENDERED_SHA256,
        },
    }


def architecture_helper_reference() -> dict[str, Any]:
    packaged = HERE / "architecture-tools.tar.gz"
    if not packaged.is_file():
        raise RunnerBlocked(f"packaged architecture helper is unavailable: {packaged}")
    actual = sha256_file(packaged)
    if actual != ARCHITECTURE_TOOLS_SHA256:
        raise EvidenceFailure(f"architecture helper hash={actual} want={ARCHITECTURE_TOOLS_SHA256}")
    descriptor = HERE / "helper-inputs.json"
    if not descriptor.is_file():
        raise EvidenceFailure(f"helper descriptor is unavailable: {descriptor}")
    return {"path": str(packaged), "sha256": actual, "descriptor": str(descriptor), "descriptor_sha256": sha256_file(descriptor)}


def build_consumer_input(source_root: Path) -> tuple[Path, tempfile.TemporaryDirectory[str]]:
    source = HERE / "consumer"
    if not (source / "main.go").is_file() or not (source / "go.mod").is_file() or not (source / "go.sum").is_file():
        raise RunnerBlocked(f"packaged consumer source is incomplete: {source}")
    temporary = tempfile.TemporaryDirectory(prefix="c38-consumer-mod-")
    mod_root = Path(temporary.name)
    module_text = (source / "go.mod").read_text(encoding="utf-8")
    replacement = [line for line in module_text.splitlines() if line.startswith("replace github.com/portpowered/go-agent-harness/go-audio =>")]
    if len(replacement) != 1:
        temporary.cleanup()
        raise EvidenceFailure("C30 consumer go.mod must contain exactly one go-audio replacement")
    lines: list[str] = []
    for line in module_text.splitlines(keepends=True):
        if line.startswith("replace github.com/portpowered/go-agent-harness/go-audio =>"):
            newline = "\n" if line.endswith("\n") else ""
            lines.append(f"replace github.com/portpowered/go-agent-harness/go-audio => {source_root / 'go-audio'}{newline}")
        else:
            lines.append(line)
    (mod_root / "go.mod").write_text("".join(lines), encoding="utf-8")
    (mod_root / "go.sum").write_bytes((source / "go.sum").read_bytes())
    return mod_root / "go.mod", temporary


def run_consumer_controls(consumer: Path, source_root: Path, run_dir: Path) -> dict[str, Any]:
    positive = run_process(
        "consumer-positive",
        [str(consumer), "--mode", "positive"],
        cwd=run_dir,
        run_dir=run_dir,
        timeout_seconds=CHILD_TIMEOUT_SECONDS,
    )
    require_exit(positive, 0, "C30 consumer positive")
    try:
        report = json.loads(positive["stdout"])
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"C30 consumer positive did not emit JSON: {positive['stdout']}") from error
    if report.get("source") != LEGACY_SOURCE_REVISION or not report.get("clean_shutdown") or not report.get("passed"):
        raise EvidenceFailure(f"C30 consumer positive source/clean/pass changed: {report}")
    cases = report.get("cases")
    if [case.get("name") for case in cases or []] != EXPECTED_CONSUMER_CASES or any(not case.get("passed") for case in cases or []):
        raise EvidenceFailure("C30 consumer positive did not retain all 21 literal cases")

    negative = run_process(
        "consumer-negative-control",
        [str(consumer), "--mode", "negative-control"],
        cwd=run_dir,
        run_dir=run_dir,
        timeout_seconds=CHILD_TIMEOUT_SECONDS,
    )
    require_exit(negative, 1, "C30 consumer negative control")
    combined = negative["stdout"] + "\n" + negative["stderr"]
    if "actual_samples=480" not in combined or "mutated_expected=481" not in combined:
        raise EvidenceFailure(f"C30 consumer negative control lost causal mismatch: {combined}")
    try:
        negative_report = json.loads(negative["stdout"])
    except json.JSONDecodeError as error:
        raise EvidenceFailure(f"C30 consumer negative control did not emit JSON: {negative['stdout']}") from error
    control = negative_report.get("negative_control", {})
    if not control.get("passed") or control.get("actual_samples") != 480 or control.get("mutated_expected") != 481:
        raise EvidenceFailure(f"C30 consumer negative control report changed: {negative_report}")
    return {
        "source_root": str(source_root),
        "source_revision": report.get("source"),
        "positive": positive,
        "positive_case_count": len(cases),
        "negative_control": negative,
    }


def wire_types_from_timeline(path: Path) -> tuple[list[str], int]:
    rows = load_jsonl(path)
    if [row.get("sequence") for row in rows] != list(range(1, len(rows) + 1)):
        raise EvidenceFailure(f"timeline sequence is not contiguous: {path}")
    if rows[0].get("kind") != "recording_started" or rows[-1].get("kind") != "recording_closed":
        raise EvidenceFailure(f"timeline does not have recording start/close boundaries: {path}")
    types: list[str] = []
    tool_calls = 0
    for row in rows:
        if row.get("kind") == "runtime" and row.get("runtime_kind") in ("provider_wire_send", "provider_wire_receive"):
            payload = row.get("payload")
            if not isinstance(payload, str) or not payload:
                raise EvidenceFailure(f"timeline has an empty provider wire payload at sequence {row.get('sequence')}")
            try:
                envelope = json.loads(base64.b64decode(payload))
            except (ValueError, UnicodeDecodeError, json.JSONDecodeError) as error:
                raise EvidenceFailure(f"timeline provider payload is not JSON at sequence {row.get('sequence')}: {error}") from error
            event_payload = envelope.get("payload")
            if not isinstance(event_payload, dict):
                raise EvidenceFailure(
                    f"timeline provider payload has no event payload at sequence {row.get('sequence')}"
                )
            event_type = event_payload.get("type")
            if not isinstance(event_type, str):
                raise EvidenceFailure(f"timeline provider payload has no type at sequence {row.get('sequence')}")
            types.append(event_type)
        if row.get("kind") == "runtime" and row.get("runtime_kind") == "tool_call":
            tool_calls += 1
    audio_rows = [row for row in rows if row.get("kind") == "audio"]
    if not audio_rows:
        raise EvidenceFailure(f"timeline has no decoded PCM boundary: {path}")
    next_sample = 0
    for row in audio_rows:
        if row.get("tap") != "speaker_enqueued" or row.get("sample_rate") != 24000:
            raise EvidenceFailure(f"timeline audio boundary changed: {row}")
        count = row.get("sample_count")
        if not isinstance(count, int) or count <= 0:
            raise EvidenceFailure(f"timeline audio sample count is invalid: {row}")
        start = row.get("start_sample", 0)
        if start != next_sample:
            raise EvidenceFailure(f"timeline audio sample order changed: start={start} want={next_sample}")
        next_sample += count
    return types, tool_calls


def validate_transcript(path: Path, label: str) -> int:
    records = load_jsonl(path)
    ticks = [record.get("tick") for record in records]
    if ticks != list(range(1, len(records) + 1)):
        raise EvidenceFailure(f"{label} transcript tick order changed: {ticks[:4]} ... {ticks[-4:]}")
    return len(records)


def validate_session_log(label: str, path: Path, *, allow_historical_failure: bool = False) -> dict[str, Any]:
    turns = load_jsonl(path)
    if label == "audio-tool":
        if len(turns) != 1:
            raise EvidenceFailure(f"{label} session-log turns={len(turns)} want=1")
        turn = turns[0]
        expected_input = {"text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}
        expected_response = {"text": "strict replay continuation", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 4800, "audio_segments": ["audio/out-000.pcm"]}
        if turn.get("input") != expected_input or turn.get("response") != expected_response:
            raise EvidenceFailure(f"{label} session-log input/response changed: {turn}")
        tool_events = turn.get("tool_events")
        if not isinstance(tool_events, list) or len(tool_events) != 2:
            raise EvidenceFailure(f"{label} tool event sequence changed: {tool_events}")
        metadata = [{key: event.get(key) for key in ("sequence", "type", "tool_call_id", "tool_name", "status", "content")} for event in tool_events]
        expected_metadata = [
            {"sequence": 1, "type": "tool_call", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": None, "content": None},
            {"sequence": 2, "type": "tool_result", "tool_call_id": "call-c07-tool", "tool_name": "exec", "status": "completed", "content": "PROBE_TOOL_MARKER_9182\n"},
        ]
        if metadata != expected_metadata:
            raise EvidenceFailure(f"{label} tool event order changed: {metadata}")
        try:
            command = json.loads(tool_events[0]["arguments"])
        except (KeyError, TypeError, json.JSONDecodeError) as error:
            raise EvidenceFailure(f"{label} tool arguments are not JSON: {error}") from error
        expected_command = 'echo PROBE_TOOL_MARKER_9182 >> "evidence/runs/exec-invocations-v4.log"; echo PROBE_TOOL_MARKER_9182'
        if command != {"command": expected_command}:
            raise EvidenceFailure(f"{label} tool command changed: {command}")
    else:
        expected = [
            {"turn_index": 1, "input": {"text": "c07 interruption", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}, "response": {"text": "", "complete": True, "audio_offset_bytes": 0, "audio_bytes": 1440, "audio_segments": ["audio/out-000.pcm"]}},
            {"turn_index": 2, "input": {"text": "", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False}, "response": {"text": "", "complete": True, "audio_offset_bytes": 1440, "audio_bytes": 2400, "audio_segments": ["audio/out-000.pcm"]}},
        ]
        if allow_historical_failure:
            if len(turns) != 2:
                raise EvidenceFailure(f"{label} historical session-log turns={len(turns)} want=2")
            first, second = turns
            expected_inputs = [expected[0]["input"], expected[1]["input"]]
            if [first.get("input"), second.get("input")] != expected_inputs:
                raise EvidenceFailure(f"{label} historical session-log inputs changed: {turns}")
            first_response = first.get("response", {})
            second_response = second.get("response", {})
            first_audio_bytes = first_response.get("audio_bytes")
            first_offset = first_response.get("audio_offset_bytes")
            if first_response.get("text") != "" or first_response.get("complete") is not True:
                raise EvidenceFailure(f"{label} historical first response changed: {first_response}")
            if first_audio_bytes not in (0, 1440) or first_offset != 0:
                raise EvidenceFailure(f"{label} historical first audio boundary changed: {first_response}")
            expected_first_segments = [] if first_audio_bytes == 0 else ["audio/out-000.pcm"]
            if first_response.get("audio_segments", []) != expected_first_segments:
                raise EvidenceFailure(f"{label} historical first audio segments changed: {first_response}")
            expected_second = {
                "text": "",
                "complete": True,
                "audio_offset_bytes": first_audio_bytes,
                "audio_bytes": 2400,
                "audio_segments": ["audio/out-000.pcm"],
            }
            if second_response != expected_second:
                raise EvidenceFailure(f"{label} historical second response changed: {second_response}")
            for actual in turns:
                if actual.get("tool_events") is not None:
                    raise EvidenceFailure(f"{label} historical session-log unexpectedly contains tool events")
            return {
                "turns": len(turns),
                "tool_events": 0,
                "historical_first_audio_bytes": first_audio_bytes,
            }
        if len(turns) != len(expected):
            raise EvidenceFailure(f"{label} session-log turns={len(turns)} want={len(expected)}")
        for actual, want in zip(turns, expected):
            if any(actual.get(key) != value for key, value in want.items()):
                raise EvidenceFailure(f"{label} session-log turn changed: {actual}")
            if actual.get("tool_events") is not None:
                raise EvidenceFailure(f"{label} unexpectedly contains tool events")
    return {"turns": len(turns), "tool_events": len(turns[0].get("tool_events", [])) if turns else 0}


def inspect_public_case(
    label: str,
    fixture: Path,
    case_dir: Path,
    *,
    enforce_pcm: bool,
    allow_historical_failure: bool = False,
) -> dict[str, Any]:
    expected = EXPECTED[label]
    bundle = case_dir / "bundle"
    manifest_path = bundle / "manifest.json"
    provider_path = bundle / "provider.json"
    provider_pcm = bundle / "audio" / "out-000.pcm"
    rendered = case_dir / "rendered.pcm"
    timeline = bundle / "audio-trace" / "timeline.jsonl"
    for path in (manifest_path, provider_path, provider_pcm, rendered, timeline):
        if not path.is_file():
            raise EvidenceFailure(f"{label} missing required public artifact: {path}")
    manifest = load_json(manifest_path)
    if manifest.get("terminal") != expected["terminal"]:
        raise EvidenceFailure(f"{label} terminal state changed: {manifest.get('terminal')}")
    artifact_hashes = {entry.get("path"): entry.get("sha256") for entry in manifest.get("artifacts", [])}
    required_artifacts = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    if set(artifact_hashes) != required_artifacts:
        raise EvidenceFailure(f"{label} manifest artifact set changed: {sorted(artifact_hashes)}")
    for relative in required_artifacts:
        artifact_path = bundle / relative
        if not artifact_path.is_file() or artifact_hashes[relative] != sha256_file(artifact_path):
            raise EvidenceFailure(f"{label} manifest hash mismatch for {relative}")
    transcript_counts = {
        "client": validate_transcript(bundle / "client.transcript.jsonl", f"{label} client"),
        "agent": validate_transcript(bundle / "agent.transcript.jsonl", f"{label} agent"),
    }
    session_log = validate_session_log(
        label,
        bundle / "session-log.jsonl",
        allow_historical_failure=allow_historical_failure,
    )
    fixture_data = load_json(fixture)
    if sha256_file(provider_path) != sha256_file(fixture) or [record.get("type") for record in fixture_data.get("records", [])] != FIXTURE_TYPES[label]:
        raise EvidenceFailure(f"{label} provider recording is not byte-identical to the frozen fixture")
    wire_types, tool_calls = wire_types_from_timeline(timeline)
    if wire_types != FIXTURE_TYPES[label] or tool_calls != expected["tool_calls"]:
        raise EvidenceFailure(f"{label} timeline wire/tool counts changed: wires={wire_types}, tools={tool_calls}")

    provider_data = provider_pcm.read_bytes()
    rendered_data = rendered.read_bytes()
    actual_pcm = {
        "provider_bytes": len(provider_data),
        "provider_sha256": sha256_bytes(provider_data),
        "rendered_bytes": len(rendered_data),
        "rendered_sha256": sha256_bytes(rendered_data),
    }
    provider_match = actual_pcm["provider_bytes"] == expected["provider_bytes"] and actual_pcm["provider_sha256"] == expected["provider_sha256"]
    rendered_match = actual_pcm["rendered_bytes"] == expected["rendered_bytes"] and actual_pcm["rendered_sha256"] == expected["rendered_sha256"]
    historical_failure = (
        allow_historical_failure
        and label == "interruption"
        and actual_pcm["provider_bytes"] == 2400
        and actual_pcm["provider_sha256"] == HEALTHY_TAIL_SHA256
        and actual_pcm["rendered_bytes"] == 2400
        and actual_pcm["rendered_sha256"] == HEALTHY_TAIL_SHA256
    )
    if enforce_pcm and (not provider_match or not rendered_match):
        raise EvidenceFailure(f"{label} PCM oracle mismatch: {actual_pcm}; expected provider/rendered bytes and hashes")
    if allow_historical_failure and not provider_match and not rendered_match and not historical_failure:
        raise EvidenceFailure(f"{label} live historical PCM mismatch is not the archived failure shape: {actual_pcm}")
    if label == "interruption" and len(provider_data) >= 1440:
        provider_tail = provider_data[expected["healthy_tail_offset_provider"]:]
        rendered_tail = rendered_data[expected["healthy_tail_offset_rendered"]:]
        tail = {
            "provider": {"bytes": len(provider_tail), "sha256": sha256_bytes(provider_tail)},
            "rendered": {"bytes": len(rendered_tail), "sha256": sha256_bytes(rendered_tail)},
            "expected_bytes": expected["healthy_tail_bytes"],
            "expected_sha256": expected["healthy_tail_sha256"],
        }
        if historical_failure:
            tail["historical_failure_shape"] = True
            tail["interpretation"] = "healthy replacement tail only; archived C30 failure shape"
        elif len(provider_tail) == expected["healthy_tail_bytes"] and sha256_bytes(provider_tail) != expected["healthy_tail_sha256"]:
            raise EvidenceFailure(f"{label} provider healthy tail hash changed: {tail}")
        if not historical_failure and len(rendered_data) >= expected["healthy_tail_offset_rendered"] and rendered_tail != provider_tail:
            raise EvidenceFailure(f"{label} rendered healthy replacement tail does not match provider tail: {tail}")
    else:
        tail = {}
    return {
        "label": label,
        "fixture": str(fixture),
        "fixture_sha256": sha256_file(fixture),
        "manifest": str(manifest_path),
        "manifest_sha256": sha256_file(manifest_path),
        "timeline": str(timeline),
        "timeline_sha256": sha256_file(timeline),
        "pcm": actual_pcm,
        "pcm_oracle_match": {"provider": provider_match, "rendered": rendered_match},
        "healthy_tail": tail,
        "transcript_counts": transcript_counts,
        "session_log": session_log,
        "wire_events": len(wire_types),
        "tool_calls": tool_calls,
    }


def run_public_case(
    artifact: Path,
    fixture: Path,
    label: str,
    run_dir: Path,
    *,
    allow_historical_failure: bool = False,
) -> tuple[dict[str, Any], dict[str, Any]]:
    case_dir = run_dir / label
    workdir = case_dir / "workdir"
    config = case_dir / "config"
    bundle = case_dir / "bundle"
    rendered = case_dir / "rendered.pcm"
    (workdir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    config.mkdir(parents=True, exist_ok=True)
    argv = [
        str(artifact),
        "-C",
        str(config),
        "--workdir",
        str(workdir),
        "--allow-path",
        str(workdir),
        "session",
        "--replay",
        str(fixture),
        "--audio-out",
        str(rendered),
        "--record-dir",
        str(bundle),
        "--max-duration",
        "60s",
        "--trace-audio",
    ]
    result = run_process(f"yui-{label}", argv, cwd=workdir, run_dir=case_dir, timeout_seconds=CHILD_TIMEOUT_SECONDS)
    require_exit(result, 0, f"yui {label} public replay")
    if label == "audio-tool":
        marker = workdir / "evidence" / "runs" / "exec-invocations-v4.log"
        if "PROBE_TOOL_MARKER_9182" not in result["stdout"] or "strict replay continuation" not in result["stdout"] or not marker.is_file():
            raise EvidenceFailure("audio-tool public replay lost the real tool marker/side effect")
    observation = inspect_public_case(
        label,
        fixture,
        case_dir,
        enforce_pcm=False,
        allow_historical_failure=allow_historical_failure,
    )
    (case_dir / "public-result.json").write_text(json.dumps({"argv": argv, "result": result, "observation": observation}, indent=2) + "\n", encoding="utf-8")
    return result, observation


def run_strict_replay(artifact: Path, case_dir: Path, label: str, run_dir: Path) -> dict[str, Any]:
    config = case_dir / "config"
    workdir = case_dir / "workdir"
    bundle = case_dir / "bundle"
    argv = [str(artifact), "-C", str(config), "--workdir", str(workdir), "--allow-path", str(workdir), "session", "replay", str(bundle)]
    result = run_process(f"strict-{label}", argv, cwd=workdir, run_dir=run_dir, timeout_seconds=CHILD_TIMEOUT_SECONDS)
    require_exit(result, 0, f"strict {label} bundle replay")
    expected = EXPECTED[label]
    if f"Replay verified: {expected['wire_events']} wire events, {expected['tool_calls']} tool calls" not in result["stderr"]:
        raise EvidenceFailure(f"strict {label} replay did not report the expected wire/tool counts: {result['stderr']}")
    return result


def run_missing_timeline_control(artifact: Path, case_dir: Path, label: str, run_dir: Path) -> dict[str, Any]:
    broken = run_dir / f"{label}-missing-timeline-bundle"
    if broken.exists():
        raise EvidenceFailure(f"derived negative-control target already exists: {broken}")
    shutil.copytree(case_dir / "bundle", broken)
    timeline = broken / "audio-trace" / "timeline.jsonl"
    if not timeline.is_file():
        raise EvidenceFailure(f"cannot construct missing-timeline control: {timeline}")
    timeline.unlink()
    config = case_dir / "config"
    workdir = case_dir / "workdir"
    argv = [str(artifact), "-C", str(config), "--workdir", str(workdir), "--allow-path", str(workdir), "session", "replay", str(broken)]
    result = run_process(f"strict-{label}-missing-timeline", argv, cwd=workdir, run_dir=run_dir, timeout_seconds=CHILD_TIMEOUT_SECONDS)
    require_exit(result, None, f"strict {label} missing-timeline control")
    if result["exit_code"] == 0 or "missing timeline.jsonl" not in (result["stdout"] + "\n" + result["stderr"]):
        raise EvidenceFailure(f"missing-timeline strict replay was not causally rejected: {result}")
    return {"result": result, "bundle": str(broken), "mutated": "audio-trace/timeline.jsonl removed"}


def run_public_matrix(artifact: Path, fixtures: Path, run_dir: Path, *, enforce_pcm: bool) -> dict[str, Any]:
    help_result = run_process("yui-help", [str(artifact), "--help"], cwd=run_dir, run_dir=run_dir, timeout_seconds=CHILD_TIMEOUT_SECONDS)
    require_exit(help_result, 0, "yui help")
    cases: dict[str, Any] = {}
    strict: dict[str, Any] = {}
    for label in ("audio-tool", "interruption"):
        fixture = fixtures / EXPECTED[label]["fixture"]
        public_result, observation = run_public_case(
            artifact,
            fixture,
            label,
            run_dir,
            allow_historical_failure=not enforce_pcm,
        )
        if enforce_pcm:
            # Re-run the assertion against the already-recorded bytes without
            # touching the public artifact; this makes the mode's oracle
            # requirement explicit in its own report.
            observation = inspect_public_case(label, fixture, run_dir / label, enforce_pcm=True)
        strict[label] = run_strict_replay(artifact, run_dir / label, label, run_dir)
        cases[label] = {"public": public_result, "observation": observation, "strict": strict[label]}
    missing = run_missing_timeline_control(artifact, run_dir / "interruption", "interruption", run_dir)
    return {"help": help_result, "cases": cases, "missing_timeline_control": missing}


def build_repaired_artifact(source_root: Path, evidence_dir: Path, run_dir: Path) -> tuple[Path, dict[str, Any]]:
    artifacts = evidence_dir / "artifacts"
    artifacts.mkdir(parents=True, exist_ok=True)
    artifact = artifacts / "yui-c38-repaired"
    source_revision = git_output(source_root, "rev-parse", "HEAD")
    build_inputs = build_input_manifest(source_root)
    argv = ["go", "build", "-p=1", "-tags=nomicrophone", "-trimpath", "-o", str(artifact), "./agent-cli/cmd/yui"]
    env = os.environ.copy()
    env["GOWORK"] = ""
    result = run_process("build-repaired-yui", argv, cwd=source_root, run_dir=run_dir, timeout_seconds=TARGETED_TIMEOUT_SECONDS, environment=env)
    require_exit(result, 0, "repaired yui build")
    if not artifact.is_file():
        raise EvidenceFailure(f"repaired yui build did not produce {artifact}")
    record = {
        "argv": argv,
        "source_root": str(source_root),
        "source_revision": source_revision,
        "source_identity": source_identity(source_root),
        "build_inputs": build_inputs,
        "result": result,
        "artifact": str(artifact),
        "artifact_sha256": sha256_file(artifact),
        "artifact_bytes": artifact.stat().st_size,
    }
    (artifacts / "repaired-build.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return artifact, record


def source_identity(source_root: Path) -> str:
    revision = git_output(source_root, "rev-parse", "HEAD")
    diff = subprocess.run(
        ["git", "-C", str(source_root), "diff", "--no-ext-diff", "--binary", "HEAD", "--", "agent-cli/internal/services/internal/agentruntime/session_audio_out.go", "agent-cli/internal/services/internal/agentruntime/session_audio_out_test.go"],
        check=True,
        capture_output=True,
    ).stdout
    if not diff:
        return revision
    return f"{revision}+dirty-{sha256_bytes(diff)[:16]}"


def assert_new_artifact(artifact: Path) -> dict[str, Any]:
    artifact = artifact.expanduser().resolve()
    if not artifact.is_file():
        raise RunnerBlocked(f"repaired yui artifact is unavailable: {artifact}")
    actual = sha256_file(artifact)
    if actual == LEGACY_YUI_SHA256:
        raise EvidenceFailure("repaired mode received the immutable original C30 yui hash")
    return {"path": str(artifact), "sha256": actual, "bytes": artifact.stat().st_size}


def run_original(source_root: Path, fixtures: Path, evidence_dir: Path) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "original")
    archived = canonical_reference()
    helper = architecture_helper_reference()
    yui = legacy_yui_path()
    consumer = legacy_consumer_path()
    consumer_result = run_consumer_controls(consumer, source_root, run_dir / "consumer")
    public = run_public_matrix(yui, fixtures, run_dir / "public", enforce_pcm=False)
    interruption = public["cases"]["interruption"]["observation"]["pcm"]
    public["legacy_live_interruption_observation"] = {
        "pcm": interruption,
        "oracle_match": public["cases"]["interruption"]["observation"]["pcm_oracle_match"],
        "interpretation": "A live legacy pass is retained as scheduling variation; it does not overwrite the archived source-2525 failure or approve the original artifact.",
    }
    return {
        "status": "pass",
        "decision": "HISTORICAL_FAILURE_PRESERVED",
        "run_dir": str(run_dir),
        "source_root": str(source_root),
        "legacy_source_revision": LEGACY_SOURCE_REVISION,
        "legacy_yui": {"path": str(yui), "sha256": sha256_file(yui)},
        "legacy_consumer": {"path": str(consumer), "sha256": sha256_file(consumer)},
        "consumer": consumer_result,
        "public": public,
        "canonical_c30": archived,
        "architecture_helper": helper,
        "note": "The archived C30 FAILED report is the immutable original failure. The live old executable is run once; no retry-until-green is used.",
    }


def run_repaired(source_root: Path, fixtures: Path, evidence_dir: Path, requested_artifact: Path | None) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "repaired")
    if requested_artifact is None:
        artifact, build = build_repaired_artifact(source_root, evidence_dir, run_dir / "build")
    else:
        artifact = requested_artifact.expanduser().resolve()
        build = None
    artifact_info = assert_new_artifact(artifact)
    public = run_public_matrix(artifact, fixtures, run_dir / "public", enforce_pcm=True)
    return {
        "status": "pass",
        "decision": "REPAIRED_ORACLE_PASS",
        "run_dir": str(run_dir),
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
        "source_identity": source_identity(source_root),
        "artifact": artifact_info,
        "build": build,
        "public": public,
        "note": "This is credential-free software/file replay evidence, not physical or acoustic device proof and not script CI green status.",
    }


def run_causal(source_root: Path, fixtures: Path, evidence_dir: Path) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "causal")
    baseline_source = subprocess.run(
        ["git", "show", f"{LEGACY_SOURCE_REVISION}:agent-cli/internal/services/internal/agentruntime/session_audio_out.go"],
        cwd=source_root,
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    current_source = (source_root / "agent-cli/internal/services/internal/agentruntime/session_audio_out.go").read_text(encoding="utf-8")
    expected_markers = (
        "context.WithoutCancel(ctx)",
        "drainAfterCancellation",
        "forwardMessageWithContext",
        "closeRequested",
        "closeInner",
        "innerCloseDone",
    )
    if any(marker not in current_source for marker in expected_markers):
        raise EvidenceFailure("current session audio output source is missing one or more causal repair markers")
    if any(marker in baseline_source for marker in ("drainAfterCancellation", "closeRequested")):
        raise EvidenceFailure("baseline source unexpectedly contains the C38 retention repair")
    test_result = run_process(
        "causal-retention-regression",
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "^(TestRunSessionWithAudioOut_.*|TestSessionAudioOutput_RetainsDelayedDeltaAcrossCancellationBarrier)$",
            "-count=1",
            "-timeout",
            "120s",
        ],
        cwd=source_root,
        run_dir=run_dir,
        timeout_seconds=TARGETED_TIMEOUT_SECONDS,
    )
    require_exit(test_result, 0, "deterministic C38 retention regression")
    inventory = fixture_inventory(fixtures)
    report = {
        "schema": "audio-runtime-c38-causal-boundary.v1",
        "decision": "CAUSAL_PROOF",
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
        "source_identity": source_identity(source_root),
        "named_questions": [
            {
                "question": "Can cancellation drop an accepted first/replacement delta between provider ingress and public receive publication?",
                "boundary": "session_audio_output_session.forward -> forwardMessageWithContext",
                "evidence": "The deterministic barrier regression enqueues the first frame, cancels while its WriteSamples call is held, enqueues the healthy replacement, and observes ordered PCM for both frames.",
                "result": "resolved_in_owned_session_audio_output",
            },
            {
                "question": "Does provider ingress stop before the output wrapper can retain accepted frames?",
                "boundary": "sessionAudioOutputInferencer.ConnectSession",
                "evidence": "The repaired wrapper connects the provider with a non-cancelled ingress context, requests the underlying Close as a terminal barrier, and drains accepted messages until that barrier settles, bounded by wall safety.",
                "result": "resolved_in_owned_session_audio_output",
            },
            {
                "question": "Does PCM reach decoded WriteSamples before public stream publication?",
                "boundary": "sessionAudioOutput.writeDelta -> sessionAudioSink.WriteSamples",
                "evidence": "forwardMessageWithContext writes the delta before best-effort public publication during teardown; retained publication cannot strand the audio write.",
                "result": "resolved_in_owned_session_audio_output",
            },
            {
                "question": "Is stale interrupted output blindly preserved?",
                "boundary": "assistantAudioDelta plus existing clean-interrupt regression",
                "evidence": "Only accepted assistant AUDIO.DELTA messages are drained, existing clean interruption behavior remains in the focused suite, and malformed/write-error paths close the inner session.",
                "result": "scoped_to_accepted_messages",
            },
        ],
        "before_source": {"revision": LEGACY_SOURCE_REVISION, "has_direct_cancellable_connect": "ConnectSession(ctx)" in baseline_source, "has_retention_drain": False},
        "after_source": {"revision": git_output(source_root, "rev-parse", "HEAD"), "markers": list(expected_markers)},
        "regression": test_result,
        "frozen_fixture_inventory": inventory,
        "archived_c30_failure": canonical_reference(),
    }
    (run_dir / "causal-boundary.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return {"status": "pass", "decision": "CAUSAL_PROOF", "run_dir": str(run_dir), "report": report}


def run_mutated_oracle_control(run_dir: Path, pcm_path: Path) -> dict[str, Any]:
    if not pcm_path.is_file():
        raise EvidenceFailure(f"mutated-oracle source PCM is unavailable: {pcm_path}")
    original = pcm_path.read_bytes()
    if not original:
        raise EvidenceFailure(f"mutated-oracle source PCM is empty: {pcm_path}")
    original_path = run_dir / "oracle-input.pcm"
    mutated_path = run_dir / "oracle-mutated.pcm"
    original_path.write_bytes(original)
    mutated = bytearray(original)
    mutated[0] ^= 0x01
    mutated_path.write_bytes(mutated)
    original_sha256 = sha256_bytes(original)
    script = (
        "import hashlib, json, pathlib, sys; "
        "p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); "
        "observed={'bytes':len(b),'sha256':hashlib.sha256(b).hexdigest()}; print(json.dumps(observed)); "
        "raise SystemExit(0 if observed['bytes']==int(sys.argv[2]) and observed['sha256']==sys.argv[3] else 1)"
    )
    result = run_process(
        "mutated-oracle-validator",
        [sys.executable, "-c", script, str(mutated_path), str(len(original)), original_sha256],
        cwd=run_dir,
        run_dir=run_dir,
        timeout_seconds=CHILD_TIMEOUT_SECONDS,
    )
    require_exit(result, 1, "mutated PCM/hash oracle")
    payload = json.loads(result["stdout"])
    mutated_sha256 = sha256_bytes(mutated)
    if payload.get("bytes") != len(original) or payload.get("sha256") != mutated_sha256 or mutated_sha256 == original_sha256:
        raise EvidenceFailure(f"mutated PCM/hash validator did not observe the real mutation: {payload}")
    record = {
        "child": result,
        "source_pcm": str(pcm_path),
        "original": {"path": str(original_path), "bytes": len(original), "sha256": original_sha256},
        "mutated": {"path": str(mutated_path), "bytes": len(mutated), "sha256": mutated_sha256},
        "payload": payload,
        "outer_rejected": True,
        "reason": "the validator rejected a same-length PCM mutation by SHA-256 while preserving the byte-count oracle",
    }
    (run_dir / "mutated-oracle.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return record


def run_negative_controls(source_root: Path, fixtures: Path, evidence_dir: Path) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "negative-controls")
    yui = legacy_yui_path()
    consumer = legacy_consumer_path()
    consumer_result = run_consumer_controls(consumer, source_root, run_dir / "consumer")
    public = run_public_matrix(yui, fixtures, run_dir / "public", enforce_pcm=False)
    interruption_pcm = Path(public["cases"]["interruption"]["observation"]["manifest"]).parent.parent / "rendered.pcm"
    mutated = run_mutated_oracle_control(run_dir, interruption_pcm)
    missing = public["missing_timeline_control"]
    return {
        "status": "pass",
        "decision": "NEGATIVE_CONTROLS_PASS",
        "run_dir": str(run_dir),
        "consumer": consumer_result,
        "mutated_oracle": mutated,
        "missing_timeline": missing,
        "note": "Every negative control is separately labeled; a clean child exit never substitutes for byte/hash or timeline validation.",
    }


def run_cleanup_control(evidence_dir: Path) -> dict[str, Any]:
    if os.name != "posix":
        raise RunnerBlocked("cleanup-control requires POSIX process-group signaling")
    run_dir = make_run_dir(evidence_dir, "cleanup-control")
    script = (
        "import signal,sys,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "chunk=b'x'*1048576; sys.stdout.buffer.write(chunk); sys.stdout.buffer.flush(); "
        "sys.stderr.buffer.write(chunk); sys.stderr.buffer.flush(); time.sleep(60)"
    )
    result = run_process(
        "ignored-term-output-flood",
        [sys.executable, "-c", script],
        cwd=run_dir,
        run_dir=run_dir,
        timeout_seconds=0.25,
        term_grace_seconds=0.25,
        kill_grace_seconds=0.5,
        output_limit=4096,
    )
    cleanup = result["cleanup"]
    checks = {
        "timed_out": result["timed_out"],
        "stdout_capped": result["stdout_truncated"] and result["stdout_bytes"] >= 1024 * 1024,
        "stderr_capped": result["stderr_truncated"] and result["stderr_bytes"] >= 1024 * 1024,
        "sigterm_sent": cleanup.get("sigterm_sent", False),
        "sigkill_sent": cleanup.get("sigkill_sent", False),
        "reaped": result["parent_reaped"],
        "reader_threads_stopped": cleanup.get("reader_threads_stopped", False),
        "no_survivors": not cleanup.get("surviving_process_group_pids"),
        "bounded_duration": result["elapsed_seconds"] < 5,
    }
    record = {"status": "pass", "decision": "CLEANUP_CONTROL_PASS", "run_dir": str(run_dir), "result": result, "checks": checks}
    (run_dir / "result.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    if not all(checks.values()):
        raise EvidenceFailure(f"cleanup-control checks failed: {record}")
    return record


def run_focused_checks(source_root: Path, evidence_dir: Path) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "focused-checks")
    test_pattern = "^(TestRunSessionWithAudioOut_.*|TestSessionAudioOutput_RetainsDelayedDeltaAcrossCancellationBarrier)$"
    commands = [
        ("normal", ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", test_pattern, "-count=1", "-timeout", "120s"]),
        ("race", ["go", "test", "-race", "./agent-cli/internal/services/internal/agentruntime", "-run", test_pattern, "-count=1", "-timeout", "120s"]),
        ("vet", ["go", "vet", "./agent-cli/internal/services/internal/agentruntime"]),
        ("architecture-check", ["make", "-C", str(source_root), "architecture-check"]),
        ("size-check", ["make", "-C", str(source_root), "size-check"]),
        ("wire-check", ["make", "-C", str(source_root), "wire-check"]),
    ]
    results: list[dict[str, Any]] = []
    for label, argv in commands:
        result = run_process(label, argv, cwd=source_root, run_dir=run_dir, timeout_seconds=TARGETED_TIMEOUT_SECONDS)
        require_exit(result, 0, f"focused {label}")
        results.append(result)
    record = {
        "status": "pass",
        "decision": "FOCUSED_CHECKS_PASS",
        "run_dir": str(run_dir),
        "source_root": str(source_root),
        "source_revision": git_output(source_root, "rev-parse", "HEAD"),
        "commands": results,
        "bounds": {"targeted_seconds": TARGETED_TIMEOUT_SECONDS, "aggregate_seconds": AGGREGATE_TIMEOUT_SECONDS},
    }
    (run_dir / "result.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return record


BUILD_INPUT_GROUPS = (
    "Makefile",
    "scripts/check-wire.py",
    "go.work",
    "go.work.sum",
    "agent-cli",
    "go-agent-loop",
    "go-agent-runtime",
    "go-audio",
    "go-device-gateway",
    "go-llm-gateway",
    "docs/architecture/architecture-policy.json",
    "docs/architecture/architecture-size-baseline.json",
    "agent-cli/internal/wire/wire.go",
    "agent-cli/internal/wire/wire_gen.go",
)


def build_input_manifest(source_root: Path) -> dict[str, Any]:
    result = subprocess.run(["git", "-C", str(source_root), "ls-files", "-z", "--", *BUILD_INPUT_GROUPS], check=True, capture_output=True)
    paths = [Path(raw) for raw in result.stdout.decode().split("\0") if raw]
    digest = hashlib.sha256()
    files: list[dict[str, Any]] = []
    for relative in paths:
        path = source_root / relative
        data = path.read_bytes()
        digest.update(relative.as_posix().encode("utf-8"))
        digest.update(b"\0")
        digest.update(data)
        files.append({"path": relative.as_posix(), "bytes": len(data), "sha256": sha256_bytes(data)})
    if not files:
        raise EvidenceFailure("no tracked Go/module/architecture inputs found")
    return {
        "groups": list(BUILD_INPUT_GROUPS),
        "file_count": len(files),
        "sha256": digest.hexdigest(),
        "method": "sha256(relative-path NUL content in lexical git ls-files order)",
        "files": files,
    }


def ancestry(source_root: Path) -> dict[str, Any]:
    refs = {
        "startup_integration": "8bdafc7f947a3a2c9856220abdc539437035bd21",
        "baseline": "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad",
        "planning_origin_main": LEGACY_SOURCE_REVISION,
    }
    checks: dict[str, Any] = {}
    for name, revision in refs.items():
        result = subprocess.run(["git", "-C", str(source_root), "merge-base", "--is-ancestor", revision, "HEAD"], check=False)
        checks[name] = {"revision": revision, "is_ancestor": result.returncode == 0}
    try:
        current_main = git_output(source_root, "rev-parse", "origin/main")
        current_main_result = subprocess.run(["git", "-C", str(source_root), "merge-base", "--is-ancestor", "origin/main", "HEAD"], check=False)
        checks["fresh_origin_main"] = {"revision": current_main, "is_ancestor": current_main_result.returncode == 0}
    except EvidenceFailure as error:
        checks["fresh_origin_main"] = {"revision": "", "is_ancestor": False, "error": str(error)}
    return checks


def run_package(source_root: Path, fixtures: Path, evidence_dir: Path, requested_artifact: Path | None) -> dict[str, Any]:
    run_dir = make_run_dir(evidence_dir, "package")
    source_root = validate_source_root(source_root)
    branch = git_output(source_root, "branch", "--show-current")
    prd = load_json(source_root / "prd.json")
    if branch != prd.get("branchName"):
        raise EvidenceFailure(f"package branch={branch!r} does not match prd.branchName={prd.get('branchName')!r}")
    status = git_output(source_root, "status", "--short", "--untracked-files=all")
    if status:
        raise EvidenceFailure(f"package requires a clean source tree before provenance capture: {status}")
    source_revision = git_output(source_root, "rev-parse", "HEAD")
    source_id = source_identity(source_root)
    if source_id != source_revision:
        raise EvidenceFailure(f"package source identity is unexpectedly dirty: {source_id}")
    if requested_artifact is None:
        raise RunnerBlocked("package mode requires --artifact for the exact rebuilt candidate")
    artifact_info = assert_new_artifact(requested_artifact)
    build_manifest = evidence_dir / "artifacts" / "repaired-build.json"
    if not build_manifest.is_file():
        raise RunnerBlocked(f"repaired build provenance is unavailable: {build_manifest}")
    build_record = load_json(build_manifest)
    if not isinstance(build_record, dict):
        raise EvidenceFailure(f"repaired build provenance is not an object: {build_manifest}")
    inputs = build_input_manifest(source_root)
    if build_record.get("build_inputs") != inputs:
        raise EvidenceFailure("repaired artifact build-input manifest does not match the clean package source")
    if build_record.get("source_revision") != source_revision or build_record.get("source_identity") != source_revision:
        raise EvidenceFailure(f"repaired build provenance source mismatch: {build_record}")
    recorded_artifact = Path(str(build_record.get("artifact", ""))).expanduser().resolve()
    if recorded_artifact != Path(artifact_info["path"]).resolve() or build_record.get("artifact_sha256") != artifact_info["sha256"] or build_record.get("artifact_bytes") != artifact_info["bytes"]:
        raise EvidenceFailure(f"repaired artifact provenance mismatch: build={build_record} package={artifact_info}")
    helper = architecture_helper_reference()
    references = canonical_reference()
    record = {
        "schema": "audio-runtime-c38-package.v1",
        "status": "pass",
        "decision": "PACKAGE_READY",
        "project": prd.get("project"),
        "work": "audio-runtime-c38-interruption-audio-retention",
        "branch": branch,
        "source_root": str(source_root),
        "source_revision": source_revision,
        "source_identity": source_id,
        "source_clean": True,
        "ancestry": ancestry(source_root),
        "working_tree_status": [],
        "owned_paths": prd.get("ownedPaths", []),
        "build_inputs": inputs,
        "architecture_helper": helper,
        "canonical_c30": references,
        "fixtures": fixture_inventory(fixtures),
        "artifact": artifact_info,
        "build_provenance": {"manifest": str(build_manifest), "source_revision": build_record["source_revision"], "build_inputs_sha256": build_record["build_inputs"]["sha256"]},
        "limits": {"child_seconds": CHILD_TIMEOUT_SECONDS, "targeted_seconds": TARGETED_TIMEOUT_SECONDS, "aggregate_seconds": AGGREGATE_TIMEOUT_SECONDS, "realtime_sessions": 0, "physical_device": False},
        "external_gates": ["script current-head CI", "independent Luna review", "guarded merge-reviewed.py", "fresh post-delivery vertical validation"],
    }
    output = evidence_dir / "artifacts" / "package-manifest.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    record["manifest"] = str(output)
    return record


def make_run_dir(evidence_dir: Path, mode: str) -> Path:
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = evidence_dir / "runs" / f"{mode}-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    return run_dir


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("original", "causal", "repaired", "negative-controls", "cleanup-control", "focused-checks", "package"), required=True)
    parser.add_argument("--artifact", type=Path, help="exact yui artifact for repaired mode")
    parser.add_argument("--source-root", type=Path, default=ROOT)
    parser.add_argument("--fixtures", type=Path, default=DEFAULT_FIXTURES)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE)
    args = parser.parse_args()

    try:
        evidence_dir = args.evidence_dir.expanduser().resolve()
        evidence_dir.mkdir(parents=True, exist_ok=True)
        source_root = validate_source_root(args.source_root)
        fixtures = resolve_fixture_dir(args.fixtures)
        inventory = fixture_inventory(fixtures)
        if args.mode == "original":
            outcome = run_original(source_root, fixtures, evidence_dir)
        elif args.mode == "causal":
            outcome = run_causal(source_root, fixtures, evidence_dir)
        elif args.mode == "repaired":
            outcome = run_repaired(source_root, fixtures, evidence_dir, args.artifact)
        elif args.mode == "negative-controls":
            outcome = run_negative_controls(source_root, fixtures, evidence_dir)
        elif args.mode == "cleanup-control":
            outcome = run_cleanup_control(evidence_dir)
        elif args.mode == "focused-checks":
            outcome = run_focused_checks(source_root, evidence_dir)
        else:
            outcome = run_package(source_root, fixtures, evidence_dir, args.artifact)
        outcome["fixture_inventory"] = inventory
        output_dir = Path(outcome.get("run_dir", evidence_dir))
        output_dir.mkdir(parents=True, exist_ok=True)
        (output_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (evidence_dir / "latest-outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 0
    except RunnerBlocked as error:
        outcome = {"status": "blocked", "decision": "BLOCKED", "mode": args.mode, "error": str(error), "fixture_inventory": locals().get("inventory", {})}
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        outcome = {"status": "failed", "decision": "FAILED", "mode": args.mode, "error": str(error), "fixture_inventory": locals().get("inventory", {})}
    if "evidence_dir" not in locals():
        evidence_dir = args.evidence_dir.expanduser().resolve()
        evidence_dir.mkdir(parents=True, exist_ok=True)
    output = evidence_dir / "latest-outcome.json"
    output.write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
