#!/usr/bin/env python3
"""Bounded behavioral controls for the public hermetic profiler entry point.

Controls invoke profile.py as a child process and inspect its retained artifacts.
They intentionally do not import implementation helpers and never invoke Go.
"""

from __future__ import annotations

import argparse
import datetime as _datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from typing import Any


PROFILE = Path(__file__).resolve().with_name("profile.py")
FIXTURE = Path(__file__).resolve().parent / "fixtures" / "emit_jsonl.py"
SCHEMA = "c11-hermetic-profile-manifest-v1"
QUIET_SCHEMA = "c11-quiet-evidence-v1"


class ControlError(Exception):
    """A control assertion or child-process failure."""


def utc_now() -> str:
    return _datetime.datetime.now(_datetime.timezone.utc).isoformat(
        timespec="milliseconds"
    ).replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )


def run_child(
    argv: list[str],
    *,
    cwd: Path,
    output_dir: Path,
    name: str,
    env: dict[str, str] | None = None,
    timeout: float = 30.0,
) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    started = time.monotonic_ns()
    try:
        completed = subprocess.run(
            argv,
            cwd=str(cwd),
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
        timed_out = False
    except subprocess.TimeoutExpired as exc:
        completed = None
        timed_out = True
        stdout = exc.output if isinstance(exc.output, bytes) else b""
        stderr = exc.stderr if isinstance(exc.stderr, bytes) else b""
    else:
        stdout = completed.stdout
        stderr = completed.stderr
    ended = time.monotonic_ns()
    stdout_path = output_dir / f"{name}.stdout"
    stderr_path = output_dir / f"{name}.stderr"
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    return {
        "argv": argv,
        "cwd": str(cwd),
        "exit_status": None if completed is None else completed.returncode,
        "timed_out": timed_out,
        "wall_seconds": (ended - started) / 1_000_000_000.0,
        "stdout_path": str(stdout_path),
        "stdout_sha256": sha256_file(stdout_path),
        "stderr_path": str(stderr_path),
        "stderr_sha256": sha256_file(stderr_path),
        "stdout": stdout.decode("utf-8", errors="replace"),
        "stderr": stderr.decode("utf-8", errors="replace"),
    }


def quiet_evidence(path: Path) -> None:
    write_json(
        path,
        {
            "schema": QUIET_SCHEMA,
            "valid": True,
            "isolation": "synthetic-control",
            "captured_at_utc": utc_now(),
            "valid_until_utc": (
                _datetime.datetime.now(_datetime.timezone.utc)
                + _datetime.timedelta(minutes=10)
            ).isoformat(timespec="milliseconds").replace("+00:00", "Z"),
            "runner": {
                "os": "synthetic",
                "architecture": "synthetic",
                "cpu": "synthetic",
                "go_version": "not-used",
            },
            "before": {"active_work": [], "processes": [], "load": "synthetic"},
            "after": {"active_work": [], "processes": [], "load": "synthetic"},
        },
    )


def synthetic_manifest(
    case_dir: Path,
    scenario: str,
    packages: list[dict[str, Any]],
) -> tuple[Path, Path]:
    root = case_dir / "run"
    root.mkdir(parents=True, exist_ok=True)
    quiet = case_dir / "quiet-evidence.json"
    quiet_evidence(quiet)
    manifest = {
        "schema": SCHEMA,
        "project": "audio-runtime",
        "work": "audio-runtime-c11-hermetic-package-profile-controls",
        "mode": "synthetic",
        "created_at_utc": utc_now(),
        "repo": str(case_dir),
        "source_sha": "synthetic-control-source",
        "runner": {
            "os": "synthetic",
            "architecture": "synthetic",
            "cpu": "synthetic",
            "go_version": "not-used",
        },
        "go": {"executable": sys.executable, "version": "not-used", "env": {}},
        "flags": {
            "cgo_enabled": "0",
            "tags": ["nomicrophone"],
            "count": 1,
            "gomaxprocs": 1,
            "go_test_p": 1,
            "general_timeout_seconds": 5,
            "agent_cli_timeout_seconds": 5,
        },
        "cache_paths": {
            "root": str((root / "cache").resolve()),
            "gocache": str((root / "cache" / "gocache").resolve()),
            "gomodcache": str((root / "cache" / "gomodcache").resolve()),
        },
        "paths": {"manifest": str((root / "manifest.json").resolve())},
        "inventory_status": "PASS",
        "inventory_commands": [],
        "modules": [
            {
                "name": "synthetic",
                "path": str(case_dir.resolve()),
                "relative": ".",
                "packages": packages,
                "package_count": len(packages),
                "test_package_count": sum(
                    1 for package in packages if package.get("has_tests")
                ),
                "no_test_package_count": sum(
                    1 for package in packages if not package.get("has_tests")
                ),
                "command": [
                    sys.executable,
                    str(FIXTURE),
                    "--scenario",
                    scenario,
                    "--delay",
                    "0.005",
                ],
            }
        ],
        "warm": {"status": "NOT_RUN", "commands": [], "test_binaries": []},
        "runs": [],
        "run_groups": [],
        "measurement_status": "UNMEASURED",
    }
    manifest_path = root / "manifest.json"
    write_json(manifest_path, manifest)
    return manifest_path, quiet


def parse_driver_json(record: dict[str, Any], label: str) -> dict[str, Any]:
    if record["timed_out"]:
        raise ControlError(f"{label} child timed out")
    text = record["stdout"].strip()
    try:
        value = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ControlError(f"{label} did not emit JSON summary: {exc}") from exc
    if not isinstance(value, dict):
        raise ControlError(f"{label} summary is not an object")
    return value


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ControlError(f"cannot read {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ControlError(f"{path} is not an object")
    return value


def exercise_case(
    output: Path,
    *,
    name: str,
    scenario: str,
    packages: list[dict[str, Any]],
    run_status: int,
    analysis_status: int,
    expect_analysis: str,
    repeat: int = 1,
    expected_checks: dict[str, Any] | None = None,
    remove_stdout_after_run: bool = False,
) -> dict[str, Any]:
    case_dir = output / "cases" / name
    manifest, quiet = synthetic_manifest(case_dir, scenario, packages)
    profile_args = [
        sys.executable,
        str(PROFILE),
        "run",
        "--manifest",
        str(manifest),
        "--allow-heavy",
        "--quiet-evidence",
        str(quiet),
        "--repeat",
        str(repeat),
    ]
    if repeat > 1:
        profile_args.extend(["--cohort", "synthetic:."])
    run_record = run_child(
        profile_args,
        cwd=case_dir,
        output_dir=case_dir / "driver",
        name="run",
    )
    if run_record["exit_status"] != run_status:
        raise ControlError(
            f"{name}: run exit {run_record['exit_status']}, want {run_status}\n"
            f"{run_record['stderr']}"
        )
    run_summary = parse_driver_json(run_record, f"{name} run")
    if remove_stdout_after_run:
        captured_manifest = read_json(manifest)
        captured_records = captured_manifest.get("runs", [])
        if not captured_records:
            raise ControlError(f"{name}: run produced no command record to tamper")
        stdout_path = captured_records[0].get("stdout_path")
        if not isinstance(stdout_path, str):
            raise ControlError(f"{name}: command record has no stdout path")
        artifact = manifest.parent / stdout_path
        try:
            artifact.unlink()
        except OSError as exc:
            raise ControlError(f"{name}: cannot remove raw stdout control artifact: {exc}") from exc
    analysis_dir = case_dir / "analysis"
    analysis_record = run_child(
        [
            sys.executable,
            str(PROFILE),
            "analyze",
            "--manifest",
            str(manifest),
            "--output",
            str(analysis_dir),
        ],
        cwd=case_dir,
        output_dir=case_dir / "driver",
        name="analyze",
    )
    if analysis_record["exit_status"] != analysis_status:
        raise ControlError(
            f"{name}: analyze exit {analysis_record['exit_status']}, want {analysis_status}\n"
            f"{analysis_record['stderr']}"
        )
    analysis_summary = parse_driver_json(analysis_record, f"{name} analyze")
    analysis = read_json(analysis_dir / "analysis.json")
    if analysis.get("status") != expect_analysis:
        raise ControlError(
            f"{name}: analysis status {analysis.get('status')!r}, want {expect_analysis!r}"
        )
    for key, expected in (expected_checks or {}).items():
        current: Any = analysis
        for part in key.split("."):
            if isinstance(current, list):
                current = current[int(part)]
            elif isinstance(current, dict):
                current = current.get(part)
            else:
                current = None
        if current != expected:
            raise ControlError(
                f"{name}: {key} = {current!r}, want {expected!r}"
            )
    return {
        "name": name,
        "scenario": scenario,
        "run": run_record,
        "run_summary": run_summary,
        "analysis": analysis_record,
        "analysis_summary": analysis_summary,
        "analysis_artifact": str(analysis_dir / "analysis.json"),
        "raw_artifacts_retained": True,
    }


def exercise_help_and_offline(output: Path) -> list[dict[str, Any]]:
    sentinel_dir = output / "offline"
    sentinel_dir.mkdir(parents=True, exist_ok=True)
    fake_go = sentinel_dir / "go"
    marker = sentinel_dir / "spawned.marker"
    fake_go.write_text(
        f"#!/bin/sh\nprintf spawned > {marker}\nexit 99\n",
        encoding="utf-8",
    )
    fake_go.chmod(0o755)
    environment = os.environ.copy()
    environment["PATH"] = str(sentinel_dir) + os.pathsep + environment.get("PATH", "")
    help_record = run_child(
        [sys.executable, str(PROFILE), "--help"],
        cwd=sentinel_dir,
        output_dir=sentinel_dir,
        name="help",
        env=environment,
    )
    if help_record["exit_status"] != 0 or marker.exists():
        raise ControlError("profile --help spawned a command or failed")

    blocked_manifest = sentinel_dir / "blocked-manifest.json"
    write_json(
        blocked_manifest,
        {
            "schema": SCHEMA,
            "mode": "hermetic",
            "source_sha": "blocked-source",
            "modules": [],
            "fresh_timing_status": "BLOCKED",
            "fresh_timing_reason": "control-only blocked fallback",
            "ci_evidence": [{"source": "synthetic"}],
            "failure_references": [],
            "provenance": {"raw_logs": "not applicable"},
        },
    )
    analysis_record = run_child(
        [
            sys.executable,
            str(PROFILE),
            "analyze",
            "--manifest",
            str(blocked_manifest),
            "--output",
            str(sentinel_dir / "blocked-analysis"),
        ],
        cwd=sentinel_dir,
        output_dir=sentinel_dir,
        name="offline-analyze",
        env=environment,
    )
    if analysis_record["exit_status"] != 0 or marker.exists():
        raise ControlError("offline analyze spawned a command or failed")
    blocked = read_json(sentinel_dir / "blocked-analysis" / "analysis.json")
    if blocked.get("status") != "BLOCKED":
        raise ControlError("offline blocked analysis did not remain BLOCKED")
    return [
        {"name": "help-no-spawn", "record": help_record},
        {"name": "offline-analyze-no-spawn", "record": analysis_record},
    ]


def exercise_fail_closed(output: Path) -> dict[str, Any]:
    case_dir = output / "fail-closed"
    case_dir.mkdir(parents=True, exist_ok=True)
    invalid_quiet = case_dir / "invalid-quiet.json"
    write_json(
        invalid_quiet,
        {
            "schema": QUIET_SCHEMA,
            "valid": False,
            "isolation": "shared-factory-host",
            "reason": "C08 owns the shared host",
        },
    )
    record = run_child(
        [
            sys.executable,
            str(PROFILE),
            "inventory",
            "--repo",
            str(Path(__file__).resolve().parents[2]),
            "--output",
            str(case_dir / "inventory"),
            "--quiet-evidence",
            str(invalid_quiet),
        ],
        cwd=case_dir,
        output_dir=case_dir,
        name="inventory",
    )
    if record["exit_status"] != 2:
        raise ControlError(
            f"fail-closed inventory exit {record['exit_status']}, want 2"
        )
    if "not valid" not in record["stderr"] and "opt-in" not in record["stderr"]:
        raise ControlError("fail-closed error did not explain the missing prerequisite")
    return {"name": "heavy-without-admission", "record": record}


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Run bounded public-entry-point controls for the hermetic profiler."
    )
    parser.add_argument("--output", required=True, help="owned controls output directory")
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    results: list[dict[str, Any]] = []
    failures: list[str] = []
    cases = [
        (
            "valid-pass-repeat",
            "pass",
            [{"import_path": "example/pass", "package_arg": ".", "has_tests": True}],
            0,
            0,
            "PASS",
            2,
            {
                "repetitions.0.completed_repetitions": 2,
                "lane.invocation_count": 2,
                "provenance.lane_wall_not_sum_of_packages": True,
            },
        ),
        (
            "package-fail-low-duration",
            "fail",
            [{"import_path": "example/fail", "package_arg": ".", "has_tests": True}],
            1,
            1,
            "INVALID",
            1,
            {"failure_references.0.process_status": "FAIL"},
        ),
        (
            "nonzero-apparent-pass",
            "nonzero-pass",
            [{"import_path": "example/pass", "package_arg": ".", "has_tests": True}],
            1,
            1,
            "INVALID",
            1,
            {"failure_references.0.process_status": "FAIL"},
        ),
        (
            "truncated-terminal",
            "truncated",
            [{"import_path": "example/truncated", "package_arg": ".", "has_tests": True}],
            0,
            1,
            "INVALID",
            1,
            {},
        ),
        (
            "malformed-stream",
            "malformed",
            [{"import_path": "example/malformed", "package_arg": ".", "has_tests": True}],
            0,
            1,
            "INVALID",
            1,
            {},
        ),
        (
            "empty-stream",
            "empty",
            [{"import_path": "example/empty", "package_arg": ".", "has_tests": True}],
            0,
            1,
            "INVALID",
            1,
            {},
        ),
        (
            "missing-package",
            "missing",
            [
                {"import_path": "example/observed", "package_arg": ".", "has_tests": True},
                {"import_path": "example/missing", "package_arg": ".", "has_tests": True},
            ],
            0,
            1,
            "INVALID",
            1,
            {},
        ),
        (
            "cached-input",
            "cached",
            [{"import_path": "example/cached", "package_arg": ".", "has_tests": True}],
            0,
            1,
            "INVALID",
            1,
            {},
        ),
        (
            "no-test-classification",
            "no-test",
            [
                {"import_path": "example/no-test", "package_arg": ".", "has_tests": False},
                {"import_path": "example/pass", "package_arg": ".", "has_tests": True},
            ],
            0,
            0,
            "PASS",
            1,
            {"inventory.no_test_packages": ["example/no-test"]},
        ),
        (
            "overlapping-subtests",
            "overlap",
            [{"import_path": "example/overlap", "package_arg": ".", "has_tests": True}],
            0,
            0,
            "PASS",
            1,
            {"package_execution.subtest_overlap_count": 1},
        ),
    ]
    for (
        name,
        scenario,
        packages,
        run_status,
        analysis_status,
        expected_analysis,
        repeat,
        expected_checks,
    ) in cases:
        try:
            results.append(
                exercise_case(
                    output,
                    name=name,
                    scenario=scenario,
                    packages=packages,
                    run_status=run_status,
                    analysis_status=analysis_status,
                    expect_analysis=expected_analysis,
                    repeat=repeat,
                    expected_checks=expected_checks,
                )
            )
        except ControlError as exc:
            failures.append(str(exc))
    try:
        results.extend(exercise_help_and_offline(output))
    except ControlError as exc:
        failures.append(str(exc))
    try:
        results.append(exercise_fail_closed(output))
    except ControlError as exc:
        failures.append(str(exc))

    try:
        results.append(
            exercise_case(
                output,
                name="missing-raw-artifact",
                scenario="pass",
                packages=[
                    {"import_path": "example/missing-raw", "package_arg": ".", "has_tests": True}
                ],
                run_status=0,
                analysis_status=1,
                expect_analysis="INVALID",
                remove_stdout_after_run=True,
            )
        )
    except ControlError as exc:
        failures.append(str(exc))

    report = {
        "schema": "c11-hermetic-profile-controls-v1",
        "created_at_utc": utc_now(),
        "profile": str(PROFILE),
        "fixture": str(FIXTURE),
        "go_invocations": 0,
        "network_invocations": 0,
        "build_invocations": 0,
        "case_count": len(cases) + 1,
        "passed_control_count": len(results),
        "failures": failures,
        "results": results,
        "raw_evidence_retained": True,
    }
    write_json(output / "controls.json", report)
    if failures:
        print(
            json.dumps(
                {"status": "FAILED", "report": str(output / "controls.json"), "failures": failures},
                indent=2,
                sort_keys=True,
            )
        )
        return 1
    print(
        json.dumps(
            {
                "status": "PASS",
                "report": str(output / "controls.json"),
                "cases": len(cases) + 1,
                "go_invocations": 0,
                "network_invocations": 0,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
