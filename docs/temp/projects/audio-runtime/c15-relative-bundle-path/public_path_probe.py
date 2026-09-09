#!/usr/bin/env python3
"""Bounded public replay-path probe for C15.

The probe copies the immutable C11 audio/tool bundle before every run.  It
records complete argv/cwd/stdout/stderr/exit evidence and distinguishes the
known pre-fix relative-path failure from candidate success.  The provider-only
``session --replay`` route is exercised separately because it consumes the
declared provider capture rather than the recording-directory manifest.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any


FIXTURE_RELATIVE_ROOT = Path(
    "docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4"
) / "evidence/audio-tool-pass"


class ProbeFailure(Exception):
    """An observed public behavior did not match its declared oracle."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_root() -> Path:
    completed = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=Path(__file__).resolve().parent,
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(completed.stdout.strip()).resolve()


def resolve_source_root(path: Path | None, repository: Path) -> Path:
    if path is None:
        factory_root = os.environ.get("FACTORY_ROOT")
        base = Path(factory_root).resolve() if factory_root else repository
        return base / FIXTURE_RELATIVE_ROOT
    path = path.expanduser().resolve()
    if (path / "run" / "bundle").is_dir() and (path / "config").is_dir():
        return path
    if path.name == "bundle" and (path.parent.parent / "config").is_dir():
        return path.parent.parent
    raise ProbeFailure(
        f"fixture root must contain config/ and run/bundle/: {path}"
    )


def run_process(binary: Path, argv: list[str], cwd: Path, timeout: float) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        [str(binary), *argv],
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        stdout, stderr = process.communicate()
    return {
        "argv": [str(binary), *argv],
        "cwd": str(cwd),
        "timeout_seconds": timeout,
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "stdout": stdout,
        "stderr": stderr,
    }


def record_result(
    results: list[dict[str, Any]],
    label: str,
    binary: Path,
    argv: list[str],
    cwd: Path,
    timeout: float,
    check,
) -> None:
    result = run_process(binary, argv, cwd, timeout)
    result["label"] = label
    try:
        check(result)
    except ProbeFailure as error:
        result["passed"] = False
        result["failure"] = str(error)
    else:
        result["passed"] = True
    results.append(result)


def expect_strict_success(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise ProbeFailure(
            f"strict replay did not complete: exit={result['exit_code']} "
            f"timed_out={result['timed_out']} stderr={result['stderr']!r}"
        )
    if "strict replay continuation" not in result["stdout"]:
        raise ProbeFailure("strict replay continuation marker is missing")
    if "Replay verified: 18 wire events, 1 tool calls." not in result["stderr"]:
        raise ProbeFailure("strict replay verification did not report 18 wire events and 1 tool call")


def expect_strict_baseline_relative_failure(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 1:
        raise ProbeFailure(
            f"baseline relative replay changed unexpectedly: exit={result['exit_code']} "
            f"timed_out={result['timed_out']}"
        )
    expected = 'recording artifact "client.transcript.jsonl" resolves outside recording directory'
    if expected not in result["stderr"]:
        raise ProbeFailure(f"baseline relative diagnostic missing: {result['stderr']!r}")
    if "Replay verified:" in result["stderr"]:
        raise ProbeFailure("baseline relative failure falsely reported replay verification")


def expect_provider_success(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise ProbeFailure(
            f"provider replay did not complete: exit={result['exit_code']} "
            f"timed_out={result['timed_out']} stderr={result['stderr']!r}"
        )
    combined = result["stdout"] + result["stderr"]
    for marker in (
        "PROBE_TOOL_MARKER_9182",
        "strict replay continuation",
        "[session terminal:",
    ):
        if marker not in combined:
            raise ProbeFailure(f"provider replay marker is missing: {marker!r}")


def expect_strict_negative(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] == 0:
        raise ProbeFailure(
            f"invalid bundle was accepted: exit={result['exit_code']} "
            f"timed_out={result['timed_out']} stdout={result['stdout']!r}"
        )
    if "Replay verified:" in result["stderr"]:
        raise ProbeFailure("invalid bundle falsely reported replay verification")
    if "replay bundle is incomplete" not in result["stderr"]:
        raise ProbeFailure(f"invalid bundle lost incomplete diagnostic: {result['stderr']!r}")


def relative_path(path: Path, cwd: Path) -> str:
    return os.fspath(path.relative_to(cwd)) if path.is_relative_to(cwd) else os.fspath(os.path.relpath(path, cwd))


def positive_cases(fixture_root: Path) -> list[tuple[str, Path, str, str, bool]]:
    bundle = fixture_root / "run" / "bundle"
    config = fixture_root / "config"
    nested = fixture_root / "nested-working-directory"
    deeper = nested / "deeper"
    deeper.mkdir()
    (nested / "evidence" / "runs").mkdir(parents=True)
    (deeper / "evidence" / "runs").mkdir(parents=True)
    cases: list[tuple[str, Path, str, str, bool]] = []
    for name, cwd, use_relative in (
        ("repository-relative", git_root(), True),
        ("fixture-relative", fixture_root, True),
        ("nested-dot-dot", nested, True),
        ("nested-normalized-dot-dot", nested, True),
        ("deeper-dot-dot", deeper, True),
        ("repository-absolute", git_root(), False),
        ("nested-absolute", nested, False),
    ):
        if use_relative:
            if name == "fixture-relative":
                bundle_arg, config_arg = "./run/./bundle", "./config"
            elif name == "nested-normalized-dot-dot":
                bundle_arg = "../nested-working-directory/../run/bundle"
                config_arg = "../nested-working-directory/../config"
            elif name == "deeper-dot-dot":
                bundle_arg, config_arg = "../../run/bundle", "../../config"
            else:
                bundle_arg = relative_path(bundle, cwd)
                config_arg = relative_path(config, cwd)
        else:
            bundle_arg, config_arg = str(bundle), str(config)
        cases.append((name, cwd, bundle_arg, config_arg, use_relative))
    repository = git_root()
    cases.extend(
        (
            (
                "repository-bundle-relative-config-absolute",
                repository,
                relative_path(bundle, repository),
                str(config),
                True,
            ),
            (
                "nested-bundle-absolute-config-relative",
                nested,
                str(bundle),
                relative_path(config, nested),
                False,
            ),
        )
    )
    return cases


def provider_cases(fixture_root: Path) -> list[tuple[str, Path, str, str, str]]:
    provider = fixture_root / "provider.json"
    config = fixture_root / "config"
    nested = fixture_root / "nested-working-directory"
    deeper = nested / "deeper"
    cases: list[tuple[str, Path, str, str, str]] = []
    for name, cwd, use_relative in (
        ("fixture-relative", fixture_root, True),
        ("fixture-dot", fixture_root, True),
        ("nested-dot-dot", nested, True),
        ("deeper-dot-dot", deeper, True),
        ("nested-absolute", nested, False),
    ):
        if use_relative:
            if name == "fixture-dot":
                provider_arg, config_arg = "./provider.json", "./config"
            else:
                provider_arg = relative_path(provider, cwd)
                config_arg = relative_path(config, cwd)
        else:
            provider_arg, config_arg = str(provider), str(config)
        cases.append((name, cwd, provider_arg, config_arg, str(provider)))
    return cases


def prepare_private_fixture(source_root: Path, evidence: Path) -> Path:
    fixture_root = evidence / "fixture"
    if fixture_root.exists():
        shutil.rmtree(fixture_root)
    fixture_root.mkdir(parents=True)
    shutil.copytree(source_root / "config", fixture_root / "config")
    shutil.copytree(source_root / "run" / "bundle", fixture_root / "run" / "bundle")
    shutil.copy2(source_root / "run" / "bundle" / "provider.json", fixture_root / "provider.json")
    (fixture_root / "evidence" / "runs").mkdir(parents=True)
    (fixture_root / "nested-working-directory").mkdir()
    return fixture_root


def prepare_negative_bundle(fixture_root: Path, evidence: Path, name: str) -> Path:
    negative_root = evidence / "negative" / name
    if negative_root.exists():
        shutil.rmtree(negative_root)
    negative_root.mkdir(parents=True)
    bundle = negative_root / "bundle"
    shutil.copytree(fixture_root / "run" / "bundle", bundle)
    outside = negative_root / "outside"
    if name == "missing":
        (bundle / "client.transcript.jsonl").unlink()
    elif name == "tampered":
        path = bundle / "audio" / "out-000.pcm"
        data = bytearray(path.read_bytes())
        data[0] ^= 1
        path.write_bytes(data)
    elif name == "symlink-artifact":
        outside.mkdir()
        target = outside / "out-000.pcm"
        target.write_bytes((bundle / "audio" / "out-000.pcm").read_bytes())
        (bundle / "audio" / "out-000.pcm").unlink()
        (bundle / "audio" / "out-000.pcm").symlink_to(target)
    elif name == "nonregular-artifact":
        path = bundle / "audio" / "out-000.pcm"
        path.unlink()
        path.mkdir()
    elif name == "parent-symlink-escape":
        outside.mkdir()
        target_dir = outside / "audio"
        target_dir.mkdir()
        (target_dir / "out-000.pcm").write_bytes((bundle / "audio" / "out-000.pcm").read_bytes())
        shutil.rmtree(bundle / "audio")
        (bundle / "audio").symlink_to(target_dir, target_is_directory=True)
    elif name == "traversal":
        manifest_path = bundle / "manifest.json"
        manifest = json.loads(manifest_path.read_text())
        manifest["artifacts"][3]["path"] = "../outside.pcm"
        manifest_path.write_text(json.dumps(manifest, separators=(",", ":")))
    else:
        raise ProbeFailure(f"unknown negative fixture {name}")
    return bundle


def run_probe(mode: str, binary: Path, evidence: Path, source_root: Path, timeout: float) -> dict[str, Any]:
    if not binary.is_file():
        raise ProbeFailure(f"binary does not exist: {binary}")
    evidence.mkdir(parents=True, exist_ok=True)
    fixture_root = prepare_private_fixture(source_root, evidence)
    repository = git_root()
    config = fixture_root / "config"
    results: list[dict[str, Any]] = []

    for name, cwd, bundle_arg, config_arg, bundle_relative in positive_cases(fixture_root):
        strict_check = (
            expect_strict_baseline_relative_failure
            if mode == "baseline" and bundle_relative
            else expect_strict_success
        )
        record_result(
            results,
            f"strict/{name}",
            binary,
            ["-C", config_arg, "session", "replay", bundle_arg],
            cwd,
            timeout,
            strict_check,
        )

    for name, cwd, provider_arg, config_arg, _ in provider_cases(fixture_root):
        record_result(
            results,
            f"provider/{name}",
            binary,
            ["-C", config_arg, "session", "probe PROBE_TOOL_MARKER_9182", "--replay", provider_arg],
            cwd,
            timeout,
            expect_provider_success,
        )

    for name in (
        "missing",
        "tampered",
        "symlink-artifact",
        "nonregular-artifact",
        "parent-symlink-escape",
        "traversal",
    ):
        bundle = prepare_negative_bundle(fixture_root, evidence, name)
        record_result(
            results,
            f"strict-negative/{name}",
            binary,
            ["-C", str(config), "session", "replay", str(bundle)],
            repository,
            timeout,
            expect_strict_negative,
        )

    report = {
        "mode": mode,
        "source_revision": subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=repository, check=True, capture_output=True, text=True
        ).stdout.strip(),
        "binary": str(binary),
        "binary_sha256": sha256_file(binary),
        "source_fixture_root": str(source_root),
        "source_bundle_sha256": sha256_file(source_root / "run" / "bundle" / "manifest.json"),
        "private_fixture_root": str(fixture_root),
        "timeout_seconds": timeout,
        "results": results,
        "passed": all(result["passed"] for result in results),
        "limitations": [
            "session --replay consumes provider.json directly; strict recording-directory manifest and audio-trace admission belong to session replay",
            "software/file-sink replay is not physical speaker consumption or acoustic proof",
        ],
    }
    (evidence / "public-path-probe.json").write_text(json.dumps(report, indent=2) + "\n")
    return report


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("baseline", "candidate"))
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    parser.add_argument(
        "--fixture",
        type=Path,
        help="fixture root containing config/ and run/bundle/ (defaults to the immutable C11 fixture)",
    )
    parser.add_argument("--timeout", type=float, default=60.0)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.timeout <= 0 or args.timeout > 60:
        raise ProbeFailure("--timeout must be within 0 < timeout <= 60 seconds")
    repository = git_root()
    source_root = resolve_source_root(args.fixture, repository)
    binary = args.binary.expanduser().resolve()
    evidence = args.evidence.expanduser().resolve()
    report = run_probe(args.mode, binary, evidence, source_root, args.timeout)
    failed = [result for result in report["results"] if not result["passed"]]
    if failed:
        for result in failed:
            print(f"FAIL {result['label']}: {result.get('failure', 'unspecified failure')}", file=sys.stderr)
        return 1
    print(json.dumps({"mode": args.mode, "passed": True, "results": len(report["results"])}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, ProbeFailure) as error:
        print(f"probe failed: {error}", file=sys.stderr)
        raise SystemExit(1)
