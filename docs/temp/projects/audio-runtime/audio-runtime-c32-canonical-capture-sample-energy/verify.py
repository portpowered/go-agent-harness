#!/usr/bin/env python3
"""Bounded independent verifier for the C32 sample/energy public consumer."""

from __future__ import annotations

import argparse
import copy
import errno
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
EXPECTED = EVIDENCE / "expected.json"
RUNS = EVIDENCE / "runs"
DEADLINE_SECONDS = 10
MAX_OUTPUT_BYTES = 1 << 20
READ_CHUNK_BYTES = 64 * 1024
TERMINATE_GRACE_SECONDS = 2
REAP_TIMEOUT_SECONDS = 2
CONTROL_TIMEOUT_SECONDS = 0.25
CONTROL_TERMINATE_GRACE_SECONDS = 0.05
SOURCE_FILES = (
    "go-audio/pkg/codec/sample_value.go",
    "go-device-gateway/pkg/devices/device_windows.go",
    "docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/consumer/main.go",
)


class VerificationFailure(RuntimeError):
    pass


def repo_root() -> Path:
    output = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, text=True
    )
    return Path(output.strip()).resolve()


ROOT = repo_root()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git_output(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def load_expected() -> dict[str, Any]:
    try:
        value = json.loads(EXPECTED.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationFailure(f"expected oracle unavailable or malformed: {exc}") from exc
    if value.get("schema") != "audio-runtime-c32-capture-sample-energy-expected.v1":
        raise VerificationFailure("expected oracle schema mismatch")
    if not isinstance(value.get("cases"), dict) or not value["cases"]:
        raise VerificationFailure("expected oracle has no cases")
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


def read_ready(
    selector: selectors.BaseSelector,
    stream: Any,
    capture: BoundedCapture,
) -> None:
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
        # Stop reading this stream as soon as the cap is proven. The bounded
        # cleanup path still drains or closes the other stream and kills the
        # process group, so a noisy child cannot fill a pipe indefinitely.
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
            try:
                process.wait(timeout=min(deadline - now, 0.1))
            except subprocess.TimeoutExpired:
                continue
            return "complete"
        try:
            events = selector.select(min(deadline - now, 0.1))
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
    # poll() is the bounded wait/reap check. Never call communicate() or wait()
    # without a timeout after SIGKILL: an inherited pipe held by a descendant
    # must not turn verifier cleanup into an unbounded operation.
    return signals_sent, process.poll() is not None


def run_bounded(
    argv: Sequence[str],
    source_revision: str,
    run_dir: Path,
    *,
    cwd: Path = EVIDENCE,
    timeout_seconds: float = DEADLINE_SECONDS,
    terminate_grace_seconds: float = TERMINATE_GRACE_SECONDS,
) -> dict[str, Any]:
    started = time.monotonic()
    environment = os.environ.copy()
    environment["C32_SOURCE_REVISION"] = source_revision
    popen_kwargs: dict[str, Any] = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    else:
        popen_kwargs["start_new_session"] = True
    process = subprocess.Popen(
        [str(argument) for argument in argv],
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
                process,
                selector,
                captures,
                terminate_grace_seconds,
            )
        elif outcome == "deadline":
            timed_out = True
            signals_sent, reap_bounded = stop_and_reap(
                process,
                selector,
                captures,
                terminate_grace_seconds,
            )
        else:
            reap_bounded = process.poll() is not None
    except BaseException:
        try:
            signals_sent, reap_bounded = stop_and_reap(
                process,
                selector,
                captures,
                terminate_grace_seconds,
            )
        except BaseException:
            pass
        raise
    finally:
        for stream in list(selector.get_map().values()):
            close_stream(selector, stream.fileobj)
        selector.close()

    elapsed = time.monotonic() - started
    stdout = captures["stdout"].text()
    stderr = captures["stderr"].text()
    result = {
        "argv": [str(argument) for argument in argv],
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
        "stdout": stdout,
        "stderr": stderr,
    }
    write_json(run_dir / "consumer-process.json", result)
    (run_dir / "consumer.stdout.log").write_text(stdout, encoding="utf-8")
    (run_dir / "consumer.stderr.log").write_text(stderr, encoding="utf-8")
    return result


def require_equal(actual: Any, expected: Any, label: str) -> None:
    if actual != expected:
        raise VerificationFailure(f"{label}: got {actual!r}, want {expected!r}")


def validate_report(report: dict[str, Any], expected: dict[str, Any], source_revision: str) -> None:
    require_equal(report.get("schema"), "audio-runtime-c32-capture-sample-energy-consumer.v1", "consumer schema")
    require_equal(report.get("source"), source_revision, "consumer source revision")
    if report.get("clean_shutdown") is not True:
        raise VerificationFailure("consumer did not report clean shutdown")
    if report.get("error"):
        raise VerificationFailure(f"consumer reported an internal error: {report['error']}")
    duration_ms = report.get("duration_ms")
    if not isinstance(duration_ms, int) or duration_ms < 0 or duration_ms > DEADLINE_SECONDS * 1000:
        raise VerificationFailure(f"consumer duration is outside the bound: {duration_ms!r}")

    actual_cases = report.get("cases")
    if not isinstance(actual_cases, list):
        raise VerificationFailure("consumer cases are not a list")
    by_name = {case.get("name"): case for case in actual_cases if isinstance(case, dict)}
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
        require_equal(actual.get("actual_samples", []), wanted.get("expected_samples", []), f"{name}.samples")
        require_equal(actual.get("negative_zero", []), wanted.get("expected_negative_zero", []), f"{name}.negative_zero")
        if "expected_energy" in wanted:
            require_equal(actual.get("actual_energy"), wanted["expected_energy"], f"{name}.energy")
        elif actual.get("actual_energy") is not None:
            raise VerificationFailure(f"{name} reported unexpected energy")


def demonstrate_negative_control(report: dict[str, Any], expected: dict[str, Any], source_revision: str) -> dict[str, Any]:
    mutated = copy.deepcopy(expected)
    mutated["cases"]["pcm16-stereo-padded-energy"]["expected_energy"] = 1.25
    try:
        validate_report(report, mutated, source_revision)
    except VerificationFailure as exc:
        return {
            "changed_case": "pcm16-stereo-padded-energy",
            "changed_expected_energy": 1.25,
            "rejected": True,
            "reason": str(exc),
        }
    raise VerificationFailure("negative control was accepted after changing expected energy to 1.25")


def bounded_control_summary(result: dict[str, Any]) -> dict[str, Any]:
    return {
        "argv": result["argv"],
        "cwd": result["cwd"],
        "deadline_seconds": result["deadline_seconds"],
        "elapsed_seconds": result["elapsed_seconds"],
        "exit_code": result["exit_code"],
        "timed_out": result["timed_out"],
        "output_exceeded": result["output_exceeded"],
        "termination_signals": result["termination_signals"],
        "reaped": result["reaped"],
        "reap_bounded": result["reap_bounded"],
        "captured_output_bytes": result["captured_output_bytes"],
    }


def run_bounded_controls(run_dir: Path) -> dict[str, Any]:
    overflow_code = (
        f"import sys; payload = b'x' * ({MAX_OUTPUT_BYTES} + 4096); "
        "sys.stdout.buffer.write(payload); sys.stdout.buffer.flush(); "
        "sys.stderr.buffer.write(payload); sys.stderr.buffer.flush()"
    )
    overflow = run_bounded(
        [sys.executable, "-c", overflow_code],
        "bounded-output-control",
        run_dir / "bounded-output-control",
        timeout_seconds=DEADLINE_SECONDS,
        terminate_grace_seconds=CONTROL_TERMINATE_GRACE_SECONDS,
    )
    if not overflow["output_exceeded"]:
        raise VerificationFailure("bounded output control did not trip the per-stream cap")
    if not overflow["reaped"] or not overflow["reap_bounded"]:
        raise VerificationFailure("bounded output control was not reaped within its cleanup bound")
    if any(size > MAX_OUTPUT_BYTES for size in overflow["captured_output_bytes"].values()):
        raise VerificationFailure("bounded output control captured more than the configured limit")

    stubborn_code = (
        "import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "time.sleep(60)"
    )
    stubborn = run_bounded(
        [sys.executable, "-c", stubborn_code],
        "bounded-sigkill-control",
        run_dir / "bounded-sigkill-control",
        timeout_seconds=CONTROL_TIMEOUT_SECONDS,
        terminate_grace_seconds=CONTROL_TERMINATE_GRACE_SECONDS,
    )
    if not stubborn["timed_out"]:
        raise VerificationFailure("SIGKILL reap control did not reach its deterministic deadline")
    if "SIGKILL" not in stubborn["termination_signals"]:
        raise VerificationFailure("SIGKILL reap control did not require SIGKILL")
    if not stubborn["reaped"] or not stubborn["reap_bounded"]:
        raise VerificationFailure("SIGKILL reap control was not reaped within its cleanup bound")

    return {
        "per_stream_output_limit_bytes": MAX_OUTPUT_BYTES,
        "output_overflow": bounded_control_summary(overflow),
        "sigkill_reap": bounded_control_summary(stubborn),
    }


def process_failure(process: dict[str, Any]) -> str:
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
    return "consumer failed: " + " ".join(details)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--consumer", type=Path)
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument(
        "--bounds-only",
        action="store_true",
        help="run deterministic output-cap and SIGKILL-reap controls without a consumer",
    )
    parser.add_argument("--source-revision", default="")
    args = parser.parse_args()

    source_revision = args.source_revision or os.environ.get("C32_SOURCE_REVISION") or git_output("rev-parse", "HEAD")
    run_name = f"{time.strftime('verify-%Y%m%dT%H%M%SZ', time.gmtime())}-{time.time_ns()}"
    run_dir = RUNS / run_name
    run_dir.mkdir(parents=True, exist_ok=False)
    if args.bounds_only:
        if args.consumer is not None or args.negative_control:
            raise VerificationFailure("--bounds-only cannot be combined with a consumer or negative control")
        controls = run_bounded_controls(run_dir)
        print(json.dumps({"bounded_controls": controls}, indent=2, sort_keys=True))
        return 0

    if args.consumer is None:
        parser.error("--consumer is required unless --bounds-only is used")
    consumer = args.consumer.resolve()
    if not consumer.is_file():
        raise VerificationFailure(f"consumer executable is unavailable: {consumer}")
    expected = load_expected()
    process = run_bounded([str(consumer)], source_revision, run_dir)
    if not process["reaped"] or not process["reap_bounded"]:
        raise VerificationFailure(process_failure(process))
    if process["output_exceeded"]:
        raise VerificationFailure("consumer output exceeded the bounded verifier output limit")
    if process["timed_out"] or process["exit_code"] != 0:
        raise VerificationFailure(process_failure(process))
    if any(size > MAX_OUTPUT_BYTES for size in process["captured_output_bytes"].values()):
        raise VerificationFailure("consumer output capture exceeded its configured limit")
    try:
        report = json.loads(process["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationFailure(f"consumer stdout is not JSON: {exc}") from exc
    if not isinstance(report, dict):
        raise VerificationFailure("consumer JSON is not an object")
    validate_report(report, expected, source_revision)
    bounded_controls = run_bounded_controls(run_dir)

    negative_control = None
    if args.negative_control:
        negative_control = demonstrate_negative_control(report, expected, source_revision)

    source_hashes = {}
    for relative in SOURCE_FILES:
        path = ROOT / relative
        if not path.is_file():
            raise VerificationFailure(f"source file is unavailable: {path}")
        source_hashes[relative] = sha256_file(path)
    try:
        origin_main = git_output("rev-parse", "origin/main")
        base_revision = git_output("merge-base", "HEAD", "origin/main")
    except subprocess.CalledProcessError as exc:
        raise VerificationFailure(f"required main ancestry is unavailable: {exc}") from exc

    required_ancestors = {
        "baselineRevision": "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad",
        "startupIntegrationRevision": "8bdafc7f947a3a2c9856220abdc539437035bd21",
        "planningMain": "1f82284abee0bd31a6680310444cea2e4c16ef00",
        "fetchedOriginMain": origin_main,
    }
    ancestor_results = {}
    for label, revision in required_ancestors.items():
        result = subprocess.run(
            ["git", "merge-base", "--is-ancestor", revision, "HEAD"],
            cwd=ROOT,
            check=False,
        )
        ancestor_results[label] = {"revision": revision, "is_ancestor": result.returncode == 0}
        if result.returncode != 0:
            raise VerificationFailure(f"required ancestor is missing: {label}={revision}")

    evidence = {
        "schema": "audio-runtime-c32-capture-sample-energy-verification.v1",
        "source_revision": source_revision,
        "origin_main": origin_main,
        "base_revision": base_revision,
        "required_ancestors": ancestor_results,
        "consumer": {
            "path": str(consumer),
            "sha256": sha256_file(consumer),
            "source_sha256": source_hashes[SOURCE_FILES[-1]],
        },
        "source_sha256": source_hashes,
        "expected_sha256": sha256_file(EXPECTED),
        "process": {key: value for key, value in process.items() if key not in {"stdout", "stderr"}},
        "case_count": len(report["cases"]),
        "clean_shutdown": report["clean_shutdown"],
        "resource_bounds": {
            "child_deadline_seconds": DEADLINE_SECONDS,
            "max_output_bytes": MAX_OUTPUT_BYTES,
            "terminate_grace_seconds": TERMINATE_GRACE_SECONDS,
            "reap_timeout_seconds": REAP_TIMEOUT_SECONDS,
            "packet_scratch_allocation": "none in canonical helpers",
        },
        "bounded_controls": bounded_controls,
    }
    if negative_control is not None:
        evidence["negative_control"] = negative_control
    write_json(EVIDENCE / "verification-report.json", evidence)
    print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationFailure, OSError, subprocess.SubprocessError) as exc:
        print(f"verification failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
