#!/usr/bin/env python3
"""Bounded independent verifier for the C32 sample/energy public consumer."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from typing import Any


EVIDENCE = Path(__file__).resolve().parent
EXPECTED = EVIDENCE / "expected.json"
RUNS = EVIDENCE / "runs"
DEADLINE_SECONDS = 10
MAX_OUTPUT_BYTES = 1 << 20
SOURCE_FILES = (
    "go-audio/pkg/codec/sample_value.go",
    "go-audio/pkg/audio/packet_energy.go",
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


def as_text(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode(errors="replace")
    return value


def run_bounded(consumer: Path, source_revision: str, run_dir: Path) -> dict[str, Any]:
    started = time.monotonic()
    environment = os.environ.copy()
    environment["C32_SOURCE_REVISION"] = source_revision
    process = subprocess.Popen(
        [str(consumer)],
        cwd=EVIDENCE,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    stdout = ""
    stderr = ""
    try:
        stdout, stderr = process.communicate(timeout=DEADLINE_SECONDS)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = as_text(exc.stdout)
        stderr = as_text(exc.stderr)
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            tail_stdout, tail_stderr = process.communicate(timeout=2)
            stdout += as_text(tail_stdout)
            stderr += as_text(tail_stderr)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            tail_stdout, tail_stderr = process.communicate()
            stdout += as_text(tail_stdout)
            stderr += as_text(tail_stderr)
    elapsed = time.monotonic() - started
    result = {
        "argv": [str(consumer)],
        "cwd": str(EVIDENCE),
        "deadline_seconds": DEADLINE_SECONDS,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--consumer", required=True, type=Path)
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument("--source-revision", default="")
    args = parser.parse_args()

    consumer = args.consumer.resolve()
    if not consumer.is_file():
        raise VerificationFailure(f"consumer executable is unavailable: {consumer}")
    expected = load_expected()
    source_revision = args.source_revision or os.environ.get("C32_SOURCE_REVISION") or git_output("rev-parse", "HEAD")
    run_name = f"{time.strftime('verify-%Y%m%dT%H%M%SZ', time.gmtime())}-{time.time_ns()}"
    run_dir = RUNS / run_name
    run_dir.mkdir(parents=True, exist_ok=False)
    process = run_bounded(consumer, source_revision, run_dir)
    if process["timed_out"] or process["exit_code"] != 0:
        raise VerificationFailure(
            f"consumer failed: exit={process['exit_code']} timeout={process['timed_out']} stderr={process['stderr'].strip()}"
        )
    if len(process["stdout"].encode()) > MAX_OUTPUT_BYTES or len(process["stderr"].encode()) > MAX_OUTPUT_BYTES:
        raise VerificationFailure("consumer output exceeded the bounded verifier output limit")
    try:
        report = json.loads(process["stdout"])
    except json.JSONDecodeError as exc:
        raise VerificationFailure(f"consumer stdout is not JSON: {exc}") from exc
    if not isinstance(report, dict):
        raise VerificationFailure("consumer JSON is not an object")
    validate_report(report, expected, source_revision)

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
            "packet_scratch_allocation": "none in canonical helpers",
        },
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
