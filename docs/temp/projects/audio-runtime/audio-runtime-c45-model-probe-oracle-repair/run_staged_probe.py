#!/usr/bin/env python3
"""Run C44's public controls against immutable staged artifacts without building."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import runpy
import shutil
import subprocess
import tarfile
import time
from typing import Any, Callable


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
VERIFY = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py"
RUNS = HERE / "runs"
EXPECTED_ARTIFACTS = {
    "artifact-0-yui": {"name": "artifact-0", "sha256": "d8820356f3d1020875c013553aa5614af44f319b8c2b701a36f0e7c6882a8efe", "bytes": 51042338},
    "artifact-1-consumer": {"name": "artifact-1", "sha256": "5d18828a28f7169c06b280ae9b023335126bd8252c9296d8802cab8b2137449b", "bytes": 6403602},
    "artifact-2-source-snapshot": {"name": "artifact-2.tar", "sha256": "9a803dab9a439211ecf90617c5f063d8f3e27a4e1f8950d7006bd729170ff399", "bytes": 245760},
    "artifact-3-build-descriptor": {"name": "artifact-3.json", "sha256": "e62c5da67ff259dfdfe5ade71f2fb2d654b12617b58e84767d853c95a2319d11", "bytes": 50257},
}
REQUIRED_FIXTURES = {
    "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json",
    "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-interruption.session.json",
}


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def artifact_facts(path: pathlib.Path) -> dict[str, Any]:
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": sha256(path)}


def free_bytes(path: pathlib.Path) -> int:
    return shutil.disk_usage(path).free


def tree_bytes(path: pathlib.Path) -> int:
    return sum(item.stat().st_size for item in path.rglob("*") if item.is_file())


def check_staged_artifacts(staged_root: pathlib.Path) -> dict[str, Any]:
    before: dict[str, Any] = {}
    for key, expected in EXPECTED_ARTIFACTS.items():
        path = staged_root / expected["name"]
        if not path.is_file():
            raise RuntimeError(f"missing staged {key}: {path}")
        facts = artifact_facts(path)
        if facts["bytes"] != expected["bytes"] or facts["sha256"] != expected["sha256"]:
            raise RuntimeError(f"staged {key} changed: {facts}, expected {expected}")
        before[key] = facts
    return before


def extract_required_fixtures(archive: pathlib.Path, destination: pathlib.Path) -> pathlib.Path:
    with tarfile.open(archive, "r") as handle:
        members = {member.name: member for member in handle.getmembers()}
        missing = sorted(REQUIRED_FIXTURES - members.keys())
        if missing:
            raise RuntimeError(f"staged source snapshot is missing fixtures: {missing}")
        for name in sorted(REQUIRED_FIXTURES):
            member = members[name]
            if not member.isfile() or pathlib.PurePosixPath(name).is_absolute() or ".." in pathlib.PurePosixPath(name).parts:
                raise RuntimeError(f"unsafe fixture member in staged snapshot: {name}")
            handle.extract(member, destination)
    return destination / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"


def import_verify() -> dict[str, Any]:
    loaded = runpy.run_path(str(VERIFY), run_name="c45_staged_probe_import")
    verifier_globals = loaded["run_wrong_replay_oracles"].__globals__
    forbidden_calls: list[str] = []

    def forbidden(name: str) -> Callable[..., Any]:
        def fail(*_args: Any, **_kwargs: Any) -> Any:
            forbidden_calls.append(name)
            raise RuntimeError(f"forbidden verifier helper called: {name}")

        return fail

    for name in ("main", "source_facts", "build_consumer", "build_yui", "run_consumer_tests"):
        verifier_globals[name] = forbidden(name)
    loaded["_c45_globals"] = verifier_globals
    loaded["_c45_forbidden_calls"] = forbidden_calls
    return loaded


def run_controls(module: dict[str, Any], run_dir: pathlib.Path, fixture_root: pathlib.Path, staged_root: pathlib.Path, child_timeout: float, total_timeout: float, max_output_bytes: int) -> dict[str, Any]:
    started = time.monotonic()
    verifier_globals = module["_c45_globals"]
    verifier_globals["CONSUMER"] = staged_root / "artifact-1"
    verifier_globals["YUI"] = staged_root / "artifact-0"
    verifier_globals["FIXTURES"] = fixture_root
    verifier_globals["ARTIFACTS"] = run_dir / "unused-artifacts"
    verifier_globals["RUNS"] = run_dir / "unused-runs"
    original_run_process = verifier_globals["run_process"]

    def bounded_run_process(*args: Any, **kwargs: Any) -> dict[str, Any]:
        result = original_run_process(*args, **kwargs)
        if result["stdout_bytes"] + result["stderr_bytes"] > max_output_bytes:
            raise verifier_globals["EvidenceFailure"](
                f"{result['label']} exceeded the private output limit: {result['stdout_bytes'] + result['stderr_bytes']} > {max_output_bytes}"
            )
        return result

    verifier_globals["run_process"] = bounded_run_process
    public_consumer = module["run_public_consumer"](run_dir, started, total_timeout, child_timeout)
    observer = module["run_effect_observer_positive"](run_dir, started, total_timeout, child_timeout)
    cli_admission = module["run_cli_admission_controls"](run_dir, started, total_timeout, child_timeout)
    replay = module["run_replay_controls"](run_dir, started, total_timeout, child_timeout)
    wrong_replay_oracles = module["run_wrong_replay_oracles"](run_dir, started, total_timeout, child_timeout, replay)
    timeout_cleanup = module["run_timeout_control"](run_dir, started, total_timeout, child_timeout)
    if module["_c45_forbidden_calls"]:
        raise RuntimeError(f"forbidden verifier helpers called: {module['_c45_forbidden_calls']}")
    return {
        "public_consumer": public_consumer,
        "effect_observer": observer,
        "cli_admission": cli_admission,
        "replay": replay,
        "wrong_replay_oracles": wrong_replay_oracles,
        "timeout_cleanup": timeout_cleanup,
        "elapsed_seconds": round(time.monotonic() - started, 6),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--staged-root", type=pathlib.Path, required=True)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--total-timeout", type=float, default=1500)
    parser.add_argument("--max-output-bytes", type=int, default=64 * 1024 * 1024)
    parser.add_argument("--min-free-bytes", type=int, default=2 * 1024 * 1024 * 1024)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 60:
        raise SystemExit("C45 child bound exceeds 60 seconds")
    if args.total_timeout <= 0 or args.total_timeout > 1500:
        raise SystemExit("C45 aggregate bound exceeds 1500 seconds")
    if args.max_output_bytes <= 0 or args.max_output_bytes > 64 * 1024 * 1024:
        raise SystemExit("C45 output bound exceeds 64 MiB")
    if args.min_free_bytes < 2 * 1024 * 1024 * 1024:
        raise SystemExit("C45 free-space reserve is below 2 GiB")

    staged_root = args.staged_root.resolve()
    run_dir = RUNS / f"staged-probe-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    outcome: dict[str, Any] = {
        "schema": "audio-runtime-c45-staged-probe/v1",
        "decision": "FAILED",
        "staged_root": str(staged_root),
        "run_dir": str(run_dir),
        "child_timeout_seconds": args.child_timeout,
        "total_timeout_seconds": args.total_timeout,
        "max_output_bytes": args.max_output_bytes,
        "min_free_bytes": args.min_free_bytes,
        "forbidden_helpers_called": [],
    }
    started = time.monotonic()
    try:
        free_before = free_bytes(ROOT)
        if free_before < args.min_free_bytes:
            raise RuntimeError(f"free-space prerequisite unavailable before probe: {free_before} < {args.min_free_bytes}")
        staged_before = check_staged_artifacts(staged_root)
        descriptor = json.loads((staged_root / "artifact-3.json").read_text(encoding="utf-8"))
        fixture_root = extract_required_fixtures(staged_root / "artifact-2.tar", run_dir / "staged-source")
        outcome["artifact_hashes_before"] = staged_before
        outcome["verifier_sha256"] = sha256(VERIFY)
        outcome["staged_binary_bytes"] = staged_before["artifact-0-yui"]["bytes"] + staged_before["artifact-1-consumer"]["bytes"]
        outcome["binary_output_bytes"] = 0
        outcome["free_space_before_bytes"] = free_before
        outcome["source_staging_bytes"] = tree_bytes(run_dir / "staged-source")
        outcome["original_artifact_provenance"] = {
            "descriptor_schema": descriptor.get("schema"),
            "descriptor_mode": descriptor.get("mode"),
            "descriptor_decision": descriptor.get("decision"),
            "descriptor_sha256": staged_before["artifact-3-build-descriptor"]["sha256"],
            "source_snapshot_sha256": staged_before["artifact-2-source-snapshot"]["sha256"],
        }
        outcome["toolchain_and_build_flags"] = [
            {key: step.get(key) for key in ("label", "argv", "cwd", "artifact")}
            for step in descriptor.get("steps", [])
            if isinstance(step, dict)
        ]
        module = import_verify()
        controls = run_controls(module, run_dir, fixture_root, staged_root, args.child_timeout, args.total_timeout, args.max_output_bytes)
        outcome["controls"] = controls
        outcome["forbidden_helpers_called"] = module["_c45_forbidden_calls"]
        if outcome["forbidden_helpers_called"]:
            raise RuntimeError(f"forbidden verifier helpers called: {outcome['forbidden_helpers_called']}")
        free_during = free_bytes(ROOT)
        if free_during < args.min_free_bytes:
            raise RuntimeError(f"free-space reserve exhausted during probe: {free_during} < {args.min_free_bytes}")
        staged_after = check_staged_artifacts(staged_root)
        if staged_before != staged_after:
            raise RuntimeError(f"staged artifact changed during probe: before={staged_before}, after={staged_after}")
        free_after = free_bytes(ROOT)
        if free_after < args.min_free_bytes:
            raise RuntimeError(f"free-space reserve exhausted after probe: {free_after} < {args.min_free_bytes}")
        outcome["artifact_hashes_after"] = staged_after
        outcome["artifact_input_equivalence"] = staged_before == staged_after
        outcome["free_space_samples_bytes"] = {"before": free_before, "during": free_during, "after": free_after}
        outcome["fixture_bytes"] = tree_bytes(fixture_root)
        outcome["report_bytes"] = tree_bytes(run_dir)
        outcome["scratch_bytes"] = 0
        outcome["tested_source_revision"] = subprocess.run(
            ["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True, timeout=10
        ).stdout.strip()
        outcome["decision"] = "ACCEPTED"
    except Exception as exc:
        outcome["error"] = f"{type(exc).__name__}: {exc}"
        outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (HERE / "latest-staged-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 1
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    (HERE / "latest-staged-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
