#!/usr/bin/env python3
"""Bounded independent software verifier for C42 capture-energy coverage."""

from __future__ import annotations

import copy
import errno
import hashlib
import json
import math
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import time
import argparse
from typing import Any, Sequence


EVIDENCE = Path(__file__).resolve().parent
RUNS = EVIDENCE / "runs"
C32_EXPECTED = EVIDENCE / "c32-original-expected.json"
C42_EXPECTED = EVIDENCE / "c42-expected.json"
MAX_OUTPUT_BYTES = 1 << 20
READ_CHUNK_BYTES = 64 * 1024
CONSUMER_DEADLINE_SECONDS = 10.0
YUI_DEADLINE_SECONDS = 60.0
TOTAL_DEADLINE_SECONDS = 600.0
TERMINATE_GRACE_SECONDS = 2.0
REAP_TIMEOUT_SECONDS = 2.0
CONTROL_TIMEOUT_SECONDS = 0.25
CONTROL_TERMINATE_GRACE_SECONDS = 0.05
MARKER = b"PROBE_TOOL_MARKER_9182\n"
MARKER_SHA256 = "f91134b50758e6d4418ab08f6a3afa9f2acceb91117c3f67d0db515b762eb43e"
RENDERED_AUDIO_SHA256 = "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"
PROVIDER_AUDIO_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
INTERRUPTION_RENDERED_SHA256 = "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff"
INTERRUPTION_PROVIDER_SHA256 = "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"
INTERRUPTION_TAIL_SHA256 = "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"
ORIGINAL_FAILED_REPORT_SHA256 = "0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960"
SINGLE_SQUARE_INPUT_HEX = "000000000000e05f"
SINGLE_SQUARE_SAMPLE = float.fromhex("0x1p+511")
SINGLE_SQUARE_ENERGY = float.fromhex("0x1p+1022")
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PLANNING_MAIN_REVISION = "926ded7bfa8f3c3e42115192d03aa1240c4806db"
SOURCE_FILES = (
    "go-audio/pkg/codec/sample_value.go",
    "go-audio/pkg/codec/sample_value_test.go",
    "go-device-gateway/pkg/devices/device_windows.go",
    "go-device-gateway/pkg/devices/device_windows_test.go",
    "docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/consumer/main.go",
    "docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/consumer/go.mod",
    "docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/consumer/go.sum",
    "docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/run.py",
)
FIXTURE_FILES = (
    "fixtures/c16-audio-tool.session.json",
    "fixtures/c16-interruption.session.json",
    "fixtures/yui-config/config.yaml",
    "fixtures/yui-config/models.yaml",
)
CREDENTIAL_ENV_NAMES = {
    "OPENAI_API_KEY",
    "OPENAI_API_BASE",
    "OPENAI_ORG_ID",
    "ANTHROPIC_API_KEY",
    "AZURE_OPENAI_API_KEY",
    "REALTIME_API_KEY",
    "YUI_API_KEY",
    "OPENROUTER_API_KEY",
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_SESSION_TOKEN",
}


class VerificationFailure(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    try:
        if path.stat().st_size > MAX_OUTPUT_BYTES:
            raise VerificationFailure(f"JSON input exceeds {MAX_OUTPUT_BYTES} bytes: {path}")
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationFailure(f"JSON input unavailable or malformed: {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise VerificationFailure(f"JSON input is not an object: {path}")
    return value


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


class BoundedCapture:
    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.data = bytearray()
        self.exceeded = False

    def append(self, data: bytes) -> None:
        remaining = self.limit - len(self.data)
        if remaining <= 0:
            self.exceeded = bool(data)
            return
        if len(data) > remaining:
            self.data.extend(data[:remaining])
            self.exceeded = True
            return
        self.data.extend(data)

    def text(self) -> str:
        return bytes(self.data).decode(errors="replace")


def close_stream(selector: selectors.BaseSelector, stream: Any) -> None:
    try:
        selector.unregister(stream)
    except (KeyError, ValueError):
        pass
    try:
        stream.close()
    except OSError:
        pass


def read_ready(selector: selectors.BaseSelector, stream: Any, capture: BoundedCapture) -> None:
    remaining = capture.limit - len(capture.data)
    read_size = min(READ_CHUNK_BYTES, max(1, remaining + 1))
    try:
        data = os.read(stream.fileno(), read_size)
    except BlockingIOError:
        return
    except OSError as exc:
        if exc.errno not in (errno.EBADF, errno.EIO):
            raise
        data = b""
    if not data:
        close_stream(selector, stream)
        return
    capture.append(data)
    if capture.exceeded:
        close_stream(selector, stream)


def pump_output(
    process: subprocess.Popen[bytes],
    selector: selectors.BaseSelector,
    captures: dict[str, BoundedCapture],
    deadline: float,
    *,
    fail_on_output: bool = True,
) -> str:
    while True:
        if fail_on_output and any(capture.exceeded for capture in captures.values()):
            return "output_exceeded"
        now = time.monotonic()
        if now >= deadline:
            return "deadline"
        streams = selector.get_map()
        if not streams:
            if process.poll() is not None:
                return "complete"
            time.sleep(min(0.05, max(0.001, deadline - now)))
            continue
        try:
            events = selector.select(min(0.1, max(0.001, deadline - now)))
        except InterruptedError:
            continue
        for key, _ in events:
            read_ready(selector, key.fileobj, captures[key.data])
        if fail_on_output and any(capture.exceeded for capture in captures.values()):
            return "output_exceeded"
        if process.poll() is not None and not selector.get_map():
            return "complete"


def signal_process_group(process: subprocess.Popen[bytes], sig: signal.Signals) -> None:
    if os.name == "nt":
        if sig == signal.SIGKILL:
            process.kill()
        else:
            process.terminate()
        return
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass


def stop_and_reap(
    process: subprocess.Popen[bytes],
    selector: selectors.BaseSelector,
    captures: dict[str, BoundedCapture],
    terminate_grace_seconds: float,
) -> tuple[list[str], bool]:
    signals_sent: list[str] = []
    signal_process_group(process, signal.SIGTERM)
    signals_sent.append(signal.Signals(signal.SIGTERM).name)
    pump_output(
        process,
        selector,
        captures,
        time.monotonic() + terminate_grace_seconds,
        fail_on_output=False,
    )
    if process.poll() is None or selector.get_map():
        signal_process_group(process, signal.SIGKILL)
        signals_sent.append(signal.Signals(signal.SIGKILL).name)
        pump_output(
            process,
            selector,
            captures,
            time.monotonic() + REAP_TIMEOUT_SECONDS,
            fail_on_output=False,
        )
    # poll() is the bounded reap check. There is deliberately no unbounded
    # wait() or communicate() after a kill; inherited pipes cannot extend it.
    return signals_sent, process.poll() is not None


def sanitized_environment(extra: dict[str, str] | None = None) -> tuple[dict[str, str], list[str]]:
    environment = os.environ.copy()
    removed: list[str] = []
    for name in list(environment):
        if name in CREDENTIAL_ENV_NAMES or name.endswith("_API_KEY"):
            removed.append(name)
            del environment[name]
    if extra:
        environment.update(extra)
    return environment, sorted(removed)


def run_bounded(
    argv: Sequence[str | Path],
    run_dir: Path,
    *,
    cwd: Path,
    environment: dict[str, str],
    timeout_seconds: float,
    terminate_grace_seconds: float = TERMINATE_GRACE_SECONDS,
) -> dict[str, Any]:
    started = time.monotonic()
    command = [str(argument) for argument in argv]
    popen_kwargs: dict[str, Any] = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        popen_kwargs["start_new_session"] = True
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        bufsize=0,
        **popen_kwargs,
    )
    captures = {
        "stdout": BoundedCapture(MAX_OUTPUT_BYTES),
        "stderr": BoundedCapture(MAX_OUTPUT_BYTES),
    }
    selector = selectors.DefaultSelector()
    for name, stream in (("stdout", process.stdout), ("stderr", process.stderr)):
        if stream is None:
            raise VerificationFailure(f"child {name} pipe was not created")
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ, name)

    timed_out = False
    output_exceeded = False
    signals_sent: list[str] = []
    reap_bounded = False
    try:
        outcome = pump_output(
            process,
            selector,
            captures,
            time.monotonic() + timeout_seconds,
        )
        if outcome == "output_exceeded":
            output_exceeded = True
            signals_sent, reap_bounded = stop_and_reap(
                process, selector, captures, terminate_grace_seconds
            )
        elif outcome == "deadline":
            timed_out = True
            signals_sent, reap_bounded = stop_and_reap(
                process, selector, captures, terminate_grace_seconds
            )
        else:
            reap_bounded = process.poll() is not None
    except BaseException:
        try:
            signals_sent, reap_bounded = stop_and_reap(
                process, selector, captures, terminate_grace_seconds
            )
        except BaseException:
            pass
        raise
    finally:
        for stream in list(selector.get_map().values()):
            close_stream(selector, stream.fileobj)
        selector.close()

    elapsed = time.monotonic() - started
    result = {
        "argv": command,
        "cwd": str(cwd),
        "deadline_seconds": timeout_seconds,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "output_exceeded": output_exceeded,
        "termination_signals": signals_sent,
        "reaped": process.poll() is not None,
        "reap_bounded": reap_bounded,
        "captured_output_bytes": {
            "stdout": len(captures["stdout"].data),
            "stderr": len(captures["stderr"].data),
        },
        "stdout": captures["stdout"].text(),
        "stderr": captures["stderr"].text(),
    }
    write_json(run_dir / "process.json", result)
    (run_dir / "stdout.log").write_bytes(bytes(captures["stdout"].data))
    (run_dir / "stderr.log").write_bytes(bytes(captures["stderr"].data))
    return result


def require_equal(actual: Any, expected: Any, label: str) -> None:
    if actual != expected:
        raise VerificationFailure(f"{label}: got {actual!r}, want {expected!r}")


def load_oracles() -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    c32 = load_json(C32_EXPECTED)
    require_equal(c32.get("schema"), "audio-runtime-c32-capture-sample-energy-expected.v1", "C32 oracle schema")
    if not isinstance(c32.get("cases"), dict) or len(c32["cases"]) != 40:
        raise VerificationFailure("C32 oracle must retain exactly 40 cases")
    c42 = load_json(C42_EXPECTED)
    require_equal(c42.get("schema"), "audio-runtime-c42-capture-energy-expected.v1", "C42 oracle schema")
    if not isinstance(c42.get("cases"), dict) or len(c42["cases"]) != 13:
        raise VerificationFailure("C42 oracle must retain exactly 13 disjoint cases")
    overlap = set(c32["cases"]) & set(c42["cases"])
    if overlap:
        raise VerificationFailure(f"C42 oracle cases overlap frozen C32 cases: {sorted(overlap)!r}")
    c32_oracle = c42.get("c32_oracle")
    if not isinstance(c32_oracle, dict):
        raise VerificationFailure("C42 oracle does not bind the C32 oracle")
    require_equal(c32_oracle.get("sha256"), sha256_file(C32_EXPECTED), "C32 oracle hash")
    require_equal(c32_oracle.get("case_count"), 40, "C32 oracle case count")
    combined = {**c32["cases"], **c42["cases"]}
    if len(combined) != 53:
        raise VerificationFailure(f"combined oracle case count is {len(combined)}, want 53")
    return c32, c42, {"schema": "combined", "cases": combined}


def validate_finite_square_control(report: dict[str, Any]) -> None:
    controls = report.get("controls")
    if not isinstance(controls, list) or len(controls) != 1:
        raise VerificationFailure("consumer must report exactly one independent C42 square control")
    control = controls[0]
    if not isinstance(control, dict):
        raise VerificationFailure("C42 square control is malformed")
    for field, expected in (
        ("name", "c42-float64-single-square-finite"),
        ("api", "codec.PacketEnergy"),
        ("input_hex", SINGLE_SQUARE_INPUT_HEX),
        ("encoding", "ieee-float"),
        ("bits_per_sample", 64),
        ("valid_bits_per_sample", 64),
        ("frames", 1),
        ("channels", 1),
        ("frame_stride", 8),
        ("expected_samples", [SINGLE_SQUARE_SAMPLE]),
        ("expected_energy", SINGLE_SQUARE_ENERGY),
    ):
        require_equal(control.get(field), expected, f"C42 square control {field}")
    if control.get("panicked") is not False:
        raise VerificationFailure(f"C42 square control panicked: {control.get('panic')!r}")
    if control.get("input_unchanged") is not True:
        raise VerificationFailure("C42 square control mutated its input")
    require_equal(control.get("actual_error", ""), "", "C42 square control error")
    require_equal(control.get("error_kind", ""), "", "C42 square control error kind")
    require_equal(control.get("actual_samples"), [SINGLE_SQUARE_SAMPLE], "C42 square control samples")
    require_equal(control.get("actual_energy"), SINGLE_SQUARE_ENERGY, "C42 square control energy")


def validate_report(report: dict[str, Any], expected: dict[str, Any], source_revision: str) -> None:
    require_equal(report.get("schema"), "audio-runtime-c42-capture-energy-consumer.v1", "consumer schema")
    require_equal(report.get("source"), source_revision, "consumer source revision")
    if report.get("clean_shutdown") is not True:
        raise VerificationFailure("consumer did not report clean shutdown")
    if report.get("error"):
        raise VerificationFailure(f"consumer reported an internal error: {report['error']}")
    duration_ms = report.get("duration_ms")
    if not isinstance(duration_ms, int) or duration_ms < 0 or duration_ms > CONSUMER_DEADLINE_SECONDS * 1000:
        raise VerificationFailure(f"consumer duration is outside the bound: {duration_ms!r}")
    if not isinstance(report.get("goos"), str) or not isinstance(report.get("goarch"), str):
        raise VerificationFailure("consumer did not report its software platform")
    validate_finite_square_control(report)

    actual_cases = report.get("cases")
    if not isinstance(actual_cases, list):
        raise VerificationFailure("consumer cases are not a list")
    by_name = {case.get("name"): case for case in actual_cases if isinstance(case, dict)}
    if len(by_name) != len(actual_cases):
        raise VerificationFailure("consumer case names are duplicated or malformed")
    wanted_cases = expected["cases"]
    require_equal(set(by_name), set(wanted_cases), "consumer case names")
    int_bits = report.get("int_bits")
    if int_bits not in (32, 64):
        raise VerificationFailure(f"unsupported consumer int width: {int_bits!r}")
    maximum_int = (1 << (int_bits - 1)) - 1

    for name, wanted in wanted_cases.items():
        actual = by_name[name]
        for field in ("api", "input_hex", "bits_per_sample", "valid_bits_per_sample"):
            require_equal(actual.get(field), wanted.get(field), f"{name}.{field}")
        if "encoding" in wanted:
            require_equal(actual.get("encoding"), wanted["encoding"], f"{name}.encoding")
        if "frames" in wanted:
            expected_frames = maximum_int if wanted["frames"] == "max_int" else wanted["frames"]
            require_equal(actual.get("frames"), expected_frames, f"{name}.frames")
            for field in ("channels", "frame_stride"):
                require_equal(actual.get(field), wanted.get(field), f"{name}.{field}")
        if actual.get("panicked") is not False:
            raise VerificationFailure(f"{name} panicked: {actual.get('panic')!r}")
        if actual.get("input_unchanged") is not True:
            raise VerificationFailure(f"{name} mutated its input")

        error_kind = wanted.get("expected_error_kind", "")
        if error_kind:
            require_equal(actual.get("error_kind"), error_kind, f"{name}.error_kind")
            if not actual.get("actual_error"):
                raise VerificationFailure(f"{name} returned no error text")
            if actual.get("actual_energy") is not None:
                raise VerificationFailure(f"{name} reported energy despite an expected error")
            continue

        if actual.get("actual_error", ""):
            raise VerificationFailure(f"{name} returned unexpected error: {actual['actual_error']}")
        require_equal(actual.get("error_kind", ""), "", f"{name}.error_kind")
        samples = actual.get("actual_samples", [])
        for sample in samples:
            if not isinstance(sample, (int, float)) or not math.isfinite(sample):
                raise VerificationFailure(f"{name} emitted a non-finite sample")
        require_equal(samples, wanted.get("expected_samples", []), f"{name}.samples")
        require_equal(actual.get("negative_zero", []), wanted.get("expected_negative_zero", []), f"{name}.negative_zero")
        if "expected_energy" in wanted:
            require_equal(actual.get("actual_energy"), wanted["expected_energy"], f"{name}.energy")
        elif actual.get("actual_energy") is not None:
            raise VerificationFailure(f"{name} reported unexpected energy")


def demonstrate_wrong_energy(report: dict[str, Any], expected: dict[str, Any], source_revision: str) -> dict[str, Any]:
    mutated = copy.deepcopy(expected)
    case_name = "c42-pcm16-three-channel-padded-valid12"
    mutated["cases"][case_name]["expected_energy"] = 0.0
    try:
        validate_report(report, mutated, source_revision)
    except VerificationFailure as exc:
        return {
            "changed_case": case_name,
            "changed_expected_energy": 0.0,
            "rejected": True,
            "reason": str(exc),
        }
    raise VerificationFailure("negative control accepted after changing a C42 energy oracle to 0.0")


def bounded_control_summary(result: dict[str, Any]) -> dict[str, Any]:
    return {
        key: result[key]
        for key in (
            "argv",
            "cwd",
            "deadline_seconds",
            "elapsed_seconds",
            "exit_code",
            "timed_out",
            "output_exceeded",
            "termination_signals",
            "reaped",
            "reap_bounded",
            "captured_output_bytes",
        )
    }


def run_bounded_controls(run_dir: Path, environment: dict[str, str], cwd: Path) -> dict[str, Any]:
    overflow_code = (
        f"import sys; payload = b'x' * ({MAX_OUTPUT_BYTES} + 4096); "
        "sys.stdout.buffer.write(payload); sys.stdout.buffer.flush(); "
        "sys.stderr.buffer.write(payload); sys.stderr.buffer.flush()"
    )
    overflow = run_bounded(
        [sys.executable, "-c", overflow_code],
        run_dir / "bounded-output-control",
        cwd=cwd,
        environment=environment,
        timeout_seconds=CONSUMER_DEADLINE_SECONDS,
        terminate_grace_seconds=CONTROL_TERMINATE_GRACE_SECONDS,
    )
    if not overflow["output_exceeded"]:
        raise VerificationFailure("bounded output control did not trip the per-stream cap")
    if not overflow["reaped"] or not overflow["reap_bounded"]:
        raise VerificationFailure("bounded output control was not reaped within its cleanup bound")
    if any(size > MAX_OUTPUT_BYTES for size in overflow["captured_output_bytes"].values()):
        raise VerificationFailure("bounded output control captured more than its configured limit")

    stubborn_code = "import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)"
    stubborn = run_bounded(
        [sys.executable, "-c", stubborn_code],
        run_dir / "bounded-sigkill-control",
        cwd=cwd,
        environment=environment,
        timeout_seconds=CONTROL_TIMEOUT_SECONDS,
        terminate_grace_seconds=CONTROL_TERMINATE_GRACE_SECONDS,
    )
    if not stubborn["timed_out"]:
        raise VerificationFailure("SIGKILL reap control did not reach its deadline")
    if "SIGKILL" not in stubborn["termination_signals"]:
        raise VerificationFailure("SIGKILL reap control did not require SIGKILL")
    if not stubborn["reaped"] or not stubborn["reap_bounded"]:
        raise VerificationFailure("SIGKILL reap control was not reaped within its cleanup bound")
    return {
        "per_stream_output_limit_bytes": MAX_OUTPUT_BYTES,
        "output_overflow": bounded_control_summary(overflow),
        "sigkill_reap": bounded_control_summary(stubborn),
    }


def process_failure(label: str, process: dict[str, Any]) -> str:
    details = [
        f"exit={process['exit_code']}",
        f"timeout={process['timed_out']}",
        f"output_exceeded={process['output_exceeded']}",
        f"reaped={process['reaped']}",
        f"reap_bounded={process['reap_bounded']}",
    ]
    stderr = process.get("stderr", "").strip()
    if stderr:
        details.append(f"stderr={stderr[-4096:]}")
    return f"{label} failed: " + " ".join(details)


def git_command(source: Path, run_dir: Path, args: Sequence[str]) -> str:
    environment, _ = sanitized_environment()
    process = run_bounded(
        ["git", *args],
        run_dir,
        cwd=source,
        environment=environment,
        timeout_seconds=5.0,
        terminate_grace_seconds=0.2,
    )
    if process["exit_code"] != 0 or process["timed_out"] or process["output_exceeded"]:
        raise VerificationFailure(process_failure("git " + " ".join(args), process))
    return process["stdout"].strip()


def verify_provenance(source: Path, run_dir: Path) -> dict[str, Any]:
    source = source.resolve()
    if not source.is_dir():
        raise VerificationFailure(f"source directory is unavailable: {source}")
    source_revision = git_command(source, run_dir / "source-revision", ("rev-parse", "HEAD"))
    origin_main = git_command(source, run_dir / "origin-main", ("rev-parse", "origin/main"))
    base_revision = git_command(source, run_dir / "base-revision", ("merge-base", "HEAD", "origin/main"))
    ancestors = {
        "baselineRevision": BASELINE_REVISION,
        "startupIntegrationRevision": STARTUP_INTEGRATION_REVISION,
        "planningMain": PLANNING_MAIN_REVISION,
        "fetchedOriginMain": origin_main,
    }
    ancestor_results: dict[str, Any] = {}
    for label, revision in ancestors.items():
        process = run_bounded(
            ["git", "merge-base", "--is-ancestor", revision, "HEAD"],
            run_dir / "ancestors" / label,
            cwd=source,
            environment=sanitized_environment()[0],
            timeout_seconds=5.0,
            terminate_grace_seconds=0.2,
        )
        is_ancestor = process["exit_code"] == 0 and not process["timed_out"] and not process["output_exceeded"]
        ancestor_results[label] = {"revision": revision, "is_ancestor": is_ancestor}
        if not is_ancestor:
            raise VerificationFailure(f"required ancestor is missing: {label}={revision}")
    return {
        "source_revision": source_revision,
        "origin_main": origin_main,
        "base_revision": base_revision,
        "required_ancestors": ancestor_results,
    }


def verify_source_files(source: Path) -> dict[str, str]:
    hashes: dict[str, str] = {}
    for relative in SOURCE_FILES:
        path = source / relative
        if not path.is_file():
            raise VerificationFailure(f"source file is unavailable: {path}")
        hashes[relative] = sha256_file(path)
    for relative in FIXTURE_FILES:
        path = EVIDENCE / relative
        if not path.is_file():
            raise VerificationFailure(f"fixture is unavailable: {path}")
        hashes["evidence/" + relative] = sha256_file(path)
    original_expected = EVIDENCE / "c32-original-expected.json"
    source_expected = source / "docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/expected.json"
    if not source_expected.is_file() or original_expected.read_bytes() != source_expected.read_bytes():
        raise VerificationFailure("C32 original oracle was not retained byte-for-byte")
    hashes["evidence/c32-original-expected.json"] = sha256_file(original_expected)
    historical_report = EVIDENCE / "c32-original-failed-report.json"
    historical_report_hash = sha256_file(historical_report)
    require_equal(historical_report_hash, ORIGINAL_FAILED_REPORT_SHA256, "C32 original failed report hash")
    hashes["evidence/c32-original-failed-report.json"] = historical_report_hash
    hashes["evidence/c42-expected.json"] = sha256_file(C42_EXPECTED)
    return hashes


def parse_consumer_output(process: dict[str, Any]) -> dict[str, Any]:
    if not process["reaped"] or not process["reap_bounded"]:
        raise VerificationFailure(process_failure("consumer", process))
    if process["output_exceeded"]:
        raise VerificationFailure("consumer output exceeded the bounded verifier limit")
    if process["timed_out"] or process["exit_code"] != 0:
        raise VerificationFailure(process_failure("consumer", process))
    if any(size > MAX_OUTPUT_BYTES for size in process["captured_output_bytes"].values()):
        raise VerificationFailure("consumer output capture exceeded its configured limit")
    try:
        report = json.loads(process["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationFailure(f"consumer stdout is not JSON: {exc}") from exc
    if not isinstance(report, dict):
        raise VerificationFailure("consumer JSON is not an object")
    return report


def run_consumer(
    source: Path,
    consumer: Path,
    source_revision: str,
    expected: dict[str, Any],
    run_dir: Path,
    total_deadline: float,
) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    if not consumer.is_file():
        raise VerificationFailure(f"consumer executable is unavailable: {consumer}")
    remaining = max(0.1, min(CONSUMER_DEADLINE_SECONDS, total_deadline - time.monotonic()))
    environment, removed_credentials = sanitized_environment(
        {"C42_SOURCE_REVISION": source_revision, "GOWORK": "off"}
    )
    process = run_bounded(
        [consumer],
        run_dir / "consumer",
        cwd=EVIDENCE / "consumer",
        environment=environment,
        timeout_seconds=remaining,
    )
    report = parse_consumer_output(process)
    validate_report(report, expected, source_revision)
    return report, process, removed_credentials


def copy_private_config(session_dir: Path) -> Path:
    config = session_dir / "config"
    config.mkdir(parents=True, exist_ok=False)
    for name in ("config.yaml", "models.yaml"):
        source = EVIDENCE / "fixtures" / "yui-config" / name
        if not source.is_file():
            raise VerificationFailure(f"credential-free YUI config is unavailable: {source}")
        (config / name).write_bytes(source.read_bytes())
    return config


def validate_session_bundle(
    label: str,
    fixture: Path,
    session_dir: Path,
    record_dir: Path,
    rendered: Path,
) -> dict[str, Any]:
    provider_pcm = record_dir / "audio" / "out-000.pcm"
    timeline = record_dir / "audio-trace" / "timeline.jsonl"
    session_log = record_dir / "session-log.jsonl"
    manifest = record_dir / "manifest.json"
    for path in (provider_pcm, timeline, session_log, manifest, rendered):
        if not path.is_file():
            raise VerificationFailure(f"{label} bundle is missing {path.relative_to(session_dir)}")
    if label == "audio-tool":
        expected_provider_size, expected_provider_hash = 4800, PROVIDER_AUDIO_SHA256
        expected_rendered_size, expected_rendered_hash = 3200, RENDERED_AUDIO_SHA256
    else:
        expected_provider_size, expected_provider_hash = 3840, INTERRUPTION_PROVIDER_SHA256
        expected_rendered_size, expected_rendered_hash = 3360, INTERRUPTION_RENDERED_SHA256
    if provider_pcm.stat().st_size != expected_provider_size or sha256_file(provider_pcm) != expected_provider_hash:
        raise VerificationFailure(f"{label} provider PCM does not match the frozen fixture output")
    if rendered.stat().st_size != expected_rendered_size or sha256_file(rendered) != expected_rendered_hash:
        raise VerificationFailure(f"{label} rendered PCM does not match the frozen output")

    session_records = []
    for line in session_log.read_text(encoding="utf-8").splitlines():
        try:
            session_records.append(json.loads(line))
        except json.JSONDecodeError as exc:
            raise VerificationFailure(f"{label} session log is malformed: {exc}") from exc
    if label == "audio-tool":
        if len(session_records) != 1:
            raise VerificationFailure(f"{label} session log has {len(session_records)} records, want one")
        record = session_records[0]
        response = record.get("response")
        if not isinstance(response, dict) or response.get("complete") is not True:
            raise VerificationFailure(f"{label} session response was not a clean complete response")
        if response.get("audio_bytes") != 4800 or len(record.get("tool_events", [])) != 2:
            raise VerificationFailure("audio-tool session did not retain the expected tool/audio record")
    else:
        if len(session_records) != 2:
            raise VerificationFailure(f"{label} session log has {len(session_records)} records, want two")
        responses = [record.get("response") for record in session_records]
        if any(not isinstance(response, dict) or response.get("complete") is not True for response in responses):
            raise VerificationFailure(f"{label} session responses were not clean complete responses")
        if [response.get("audio_bytes") for response in responses] != [1440, 2400]:
            raise VerificationFailure("interruption session did not retain the expected interrupted and healthy audio records")
        if any(response.get("audio_segments") != ["audio/out-000.pcm"] for response in responses):
            raise VerificationFailure("interruption session did not retain the expected audio segment")
        tail = provider_pcm.read_bytes()[1440:]
        if len(tail) != 2400 or hashlib.sha256(tail).hexdigest() != INTERRUPTION_TAIL_SHA256:
            raise VerificationFailure("interruption healthy audio tail does not match the frozen hash")
        record = session_records[-1]

    timeline_records = []
    for line in timeline.read_text(encoding="utf-8").splitlines():
        try:
            timeline_records.append(json.loads(line))
        except json.JSONDecodeError as exc:
            raise VerificationFailure(f"{label} timeline is malformed: {exc}") from exc
    if not timeline_records or timeline_records[0].get("kind") != "recording_started":
        raise VerificationFailure(f"{label} timeline does not start with recording_started")
    if timeline_records[-1].get("kind") != "recording_closed" or timeline_records[-1].get("clean") is not True:
        raise VerificationFailure(f"{label} timeline does not end with clean recording_closed")
    manifest_value = load_json(manifest)
    return {
        "fixture": str(fixture.relative_to(EVIDENCE)),
        "fixture_sha256": sha256_file(fixture),
        "provider_pcm": {
            "path": str(provider_pcm.relative_to(session_dir)),
            "bytes": provider_pcm.stat().st_size,
            "sha256": sha256_file(provider_pcm),
        },
        "rendered_pcm": {
            "path": str(rendered.relative_to(session_dir)),
            "bytes": rendered.stat().st_size,
            "sha256": sha256_file(rendered),
        },
        "marker": {
            "path": "work/evidence/runs/exec-invocations-v4.log" if label == "audio-tool" else None,
            "sha256": MARKER_SHA256 if label == "audio-tool" else None,
        },
        "session_records": len(session_records),
        "tool_event_count": len(record.get("tool_events", [])),
        "timeline_records": len(timeline_records),
        "manifest_keys": sorted(manifest_value),
    }


def run_public_capture(
    label: str,
    yui: Path,
    fixture: Path,
    run_dir: Path,
    total_deadline: float,
    environment: dict[str, str],
) -> tuple[dict[str, Any], Path, Path, dict[str, Any]]:
    session_dir = run_dir / label
    session_dir.mkdir(parents=True, exist_ok=False)
    work = session_dir / "work"
    work.mkdir()
    (work / "evidence").mkdir()
    (work / "evidence" / "runs").mkdir()
    config = copy_private_config(session_dir)
    record_dir = session_dir / "bundle"
    rendered = session_dir / "rendered.pcm"
    command = [
        yui,
        "--workdir",
        work,
        "--allow-path",
        "evidence",
        "-C",
        config,
        "session",
        "--replay",
        fixture,
        "--audio-out",
        rendered,
        "--record-dir",
        record_dir,
        "--trace-audio",
        "--max-duration",
        "60s",
    ]
    remaining = max(0.1, min(YUI_DEADLINE_SECONDS, total_deadline - time.monotonic()))
    process = run_bounded(
        command,
        session_dir / "capture-process",
        cwd=work,
        environment=environment,
        timeout_seconds=remaining,
    )
    if not process["reaped"] or not process["reap_bounded"] or process["timed_out"] or process["exit_code"] != 0:
        raise VerificationFailure(process_failure(label + " YUI capture", process))
    if process["output_exceeded"]:
        raise VerificationFailure(f"{label} YUI capture exceeded the output cap")
    if label == "audio-tool":
        marker_path = work / "evidence" / "runs" / "exec-invocations-v4.log"
        if not marker_path.is_file() or marker_path.read_bytes() != MARKER or sha256_file(marker_path) != MARKER_SHA256:
            raise VerificationFailure("audio-tool marker side effect is missing or changed")
        if MARKER.decode().strip() not in process["stdout"]:
            raise VerificationFailure("audio-tool marker is absent from YUI output")
    bundle = validate_session_bundle(label, fixture, session_dir, record_dir, rendered)
    bundle["capture_process"] = bounded_control_summary(process)
    return bundle, session_dir, record_dir, {"stdout": process["stdout"], "stderr": process["stderr"]}


def run_directory_replay(
    label: str,
    yui: Path,
    session_dir: Path,
    record_dir: Path,
    config: Path,
    environment: dict[str, str],
    total_deadline: float,
    *,
    expect_success: bool,
    expected_text: str,
) -> dict[str, Any]:
    replay_work = session_dir / ("replay" if expect_success else "replay-missing-timeline")
    replay_work.mkdir()
    command = [yui, "--workdir", replay_work, "-C", config, "session", "replay", record_dir]
    remaining = max(0.1, min(YUI_DEADLINE_SECONDS, total_deadline - time.monotonic()))
    process = run_bounded(
        command,
        session_dir / ("replay-process" if expect_success else "missing-timeline-process"),
        cwd=replay_work,
        environment=environment,
        timeout_seconds=remaining,
    )
    output = process["stdout"] + process["stderr"]
    if not process["reaped"] or not process["reap_bounded"] or process["timed_out"]:
        raise VerificationFailure(process_failure(label + " directory replay", process))
    if any(size > MAX_OUTPUT_BYTES for size in process["captured_output_bytes"].values()):
        raise VerificationFailure(f"{label} directory replay exceeded the output cap")
    if expect_success:
        if process["exit_code"] != 0 or expected_text not in output:
            raise VerificationFailure(process_failure(label + " directory replay", process))
    else:
        if process["exit_code"] == 0 or expected_text not in output:
            raise VerificationFailure(f"{label} missing-timeline replay was accepted: {output[-4096:]}")
    return bounded_control_summary(process) | {"expected_text": expected_text}


def run_public_software(
    yui: Path,
    run_dir: Path,
    total_deadline: float,
) -> dict[str, Any]:
    environment, removed_credentials = sanitized_environment()
    audio_fixture = EVIDENCE / "fixtures" / "c16-audio-tool.session.json"
    interruption_fixture = EVIDENCE / "fixtures" / "c16-interruption.session.json"
    audio, audio_dir, audio_bundle, _ = run_public_capture(
        "audio-tool", yui, audio_fixture, run_dir, total_deadline, environment
    )
    audio["directory_replay"] = run_directory_replay(
        "audio-tool", yui, audio_dir, audio_bundle, audio_dir / "config", environment, total_deadline,
        expect_success=True, expected_text="Replay verified: 18 wire events, 1 tool calls.",
    )
    interruption, interruption_dir, interruption_bundle, _ = run_public_capture(
        "interruption", yui, interruption_fixture, run_dir, total_deadline, environment
    )
    interruption["directory_replay"] = run_directory_replay(
        "interruption", yui, interruption_dir, interruption_bundle, interruption_dir / "config", environment, total_deadline,
        expect_success=True, expected_text="Replay verified: 15 wire events, 0 tool calls.",
    )
    return {
        "credential_env_removed": removed_credentials,
        "live_provider": False,
        "audio_tool": audio,
        "interruption": interruption,
    }


def run_negative_public_control(
    yui: Path,
    run_dir: Path,
    total_deadline: float,
) -> dict[str, Any]:
    environment, removed_credentials = sanitized_environment()
    fixture = EVIDENCE / "fixtures" / "c16-audio-tool.session.json"
    capture, session_dir, record_dir, _ = run_public_capture(
        "audio-tool", yui, fixture, run_dir, total_deadline, environment
    )
    timeline = record_dir / "audio-trace" / "timeline.jsonl"
    if not timeline.is_file():
        raise VerificationFailure("negative replay control could not locate timeline.jsonl")
    timeline.unlink()
    rejected = run_directory_replay(
        "negative-audio-tool", yui, session_dir, record_dir, session_dir / "config", environment, total_deadline,
        expect_success=False, expected_text="missing timeline.jsonl",
    )
    return {
        "credential_env_removed": removed_credentials,
        "captured_before_mutation": capture,
        "removed": "bundle/audio-trace/timeline.jsonl",
        "rejected": True,
        "replay": rejected,
    }


def make_run_dir() -> Path:
    RUNS.mkdir(parents=True, exist_ok=True)
    run_dir = RUNS / f"verify-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{time.time_ns()}"
    run_dir.mkdir(parents=False, exist_ok=False)
    return run_dir


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("software", "negative-control"), required=True)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--consumer", type=Path, required=True)
    parser.add_argument("--yui", type=Path, required=True)
    args = parser.parse_args()

    source = args.source.resolve()
    consumer = args.consumer.resolve()
    yui = args.yui.resolve()
    if not yui.is_file():
        raise VerificationFailure(f"YUI executable is unavailable: {yui}")
    run_dir = make_run_dir()
    started = time.monotonic()
    total_deadline = started + TOTAL_DEADLINE_SECONDS
    provenance = verify_provenance(source, run_dir / "provenance")
    source_hashes = verify_source_files(source)
    _, _, combined_expected = load_oracles()
    report, consumer_process, removed_credentials = run_consumer(
        source, consumer, provenance["source_revision"], combined_expected, run_dir, total_deadline
    )
    controls_environment, _ = sanitized_environment()
    bounded_controls = run_bounded_controls(run_dir, controls_environment, EVIDENCE)
    summary: dict[str, Any] = {
        "schema": "audio-runtime-c42-capture-energy-verification.v1",
        "mode": args.mode,
        "source_revision": provenance["source_revision"],
        "provenance": provenance,
        "source_sha256": source_hashes,
        "consumer": {
            "path": str(consumer),
            "sha256": sha256_file(consumer),
            "process": bounded_control_summary(consumer_process),
        },
        "yui": {"path": str(yui), "sha256": sha256_file(yui)},
        "case_count": len(report["cases"]),
        "control_count": len(report["controls"]),
        "independent_controls": report["controls"],
        "clean_shutdown": report["clean_shutdown"],
        "credential_env_removed": sorted(set(removed_credentials)),
        "resource_bounds": {
            "child_deadline_seconds": CONSUMER_DEADLINE_SECONDS,
            "yui_deadline_seconds": YUI_DEADLINE_SECONDS,
            "total_deadline_seconds": TOTAL_DEADLINE_SECONDS,
            "max_output_bytes_per_stream": MAX_OUTPUT_BYTES,
            "terminate_grace_seconds": TERMINATE_GRACE_SECONDS,
            "reap_timeout_seconds": REAP_TIMEOUT_SECONDS,
        },
        "bounded_controls": bounded_controls,
        "run_dir": str(run_dir.relative_to(EVIDENCE)),
        "hardware_acoustic_evidence": "OUT OF SCOPE under the effective 2026-09-10 amendment; this is credential-free software replay and Windows software/cross-build coverage",
    }
    if args.mode == "software":
        summary["public_replay"] = run_public_software(yui, run_dir, total_deadline)
        summary["elapsed_seconds"] = round(time.monotonic() - started, 6)
        write_json(EVIDENCE / "verification-report.json", summary)
    else:
        summary["wrong_energy_oracle"] = demonstrate_wrong_energy(report, combined_expected, provenance["source_revision"])
        summary["missing_timeline_replay"] = run_negative_public_control(yui, run_dir, total_deadline)
        summary["elapsed_seconds"] = round(time.monotonic() - started, 6)
        write_json(run_dir / "negative-control-report.json", summary)
    if summary["elapsed_seconds"] > TOTAL_DEADLINE_SECONDS:
        raise VerificationFailure("verification exceeded the total deadline")
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationFailure, OSError, subprocess.SubprocessError) as exc:
        print(f"verification failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
