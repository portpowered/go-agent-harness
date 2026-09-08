#!/usr/bin/env python3
"""Run the bounded public recording-directory integrity comparison.

The probe always copies the preserved C12 interruption bundle before making a
same-size PCM mutation.  It never edits the source fixture or the executable.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import signal
import shutil
import subprocess
import sys
import time
from pathlib import Path


PCM_PATH = Path("audio/out-000.pcm")
SOURCE_RELATIVE_BUNDLE = Path(
    "docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe"
) / "evidence/runs/interruption/bundle"


def repository_root() -> Path:
    for parent in Path(__file__).resolve().parents:
        if (parent / "go.work").is_file():
            return parent
    raise RuntimeError("could not locate repository root from probe path")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(64 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def mutate_one_byte(source: Path, destination: Path) -> None:
    shutil.copytree(source, destination)
    target = destination / PCM_PATH
    with target.open("r+b") as stream:
        first = stream.read(1)
        if not first:
            raise RuntimeError(f"cannot mutate empty PCM artifact: {target}")
        stream.seek(0)
        stream.write(bytes((first[0] ^ 1,)))
        stream.flush()
    if target.stat().st_size != (source / PCM_PATH).stat().st_size:
        raise RuntimeError("PCM mutation changed the artifact size")


def run_public_command(
    command: list[str],
    *,
    cwd: Path,
    stdout_path: Path,
    stderr_path: Path,
    timeout_seconds: int = 60,
) -> dict[str, object]:
    started = time.time()
    stdout_path.parent.mkdir(parents=True, exist_ok=True)
    with stdout_path.open("wb") as stdout, stderr_path.open("wb") as stderr:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            stdout=stdout,
            stderr=stderr,
            start_new_session=True,
        )
        timed_out = False
        try:
            process.wait(timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
    return {
        "command": command,
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "elapsed_seconds": round(time.time() - started, 3),
        "stdout": str(stdout_path),
        "stderr": str(stderr_path),
    }


def text_at(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace")


def run_probe(mode: str, binary: Path, evidence: Path) -> dict[str, object]:
    root = repository_root()
    factory_root = Path(os.environ.get("FACTORY_ROOT", root)).resolve()
    source = factory_root / SOURCE_RELATIVE_BUNDLE
    if not source.is_dir():
        raise RuntimeError(f"preserved C12 source bundle is unavailable: {source}")
    if not (source / PCM_PATH).is_file():
        raise RuntimeError(f"preserved C12 PCM artifact is unavailable: {source / PCM_PATH}")

    evidence = evidence.resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    run_root = evidence / f"{mode}-{os.getpid()}-{int(time.time())}"
    run_root.mkdir()
    config_dir = run_root / "config"
    config_dir.mkdir()
    untouched = run_root / "untouched"
    mutated = run_root / "mutated"
    shutil.copytree(source, untouched)
    mutate_one_byte(source, mutated)

    source_manifest = source / "manifest.json"
    source_provider = source / "provider.json"
    manifest_hash = sha256(source_manifest)
    provider_hash = sha256(source_provider)
    untouched_pcm_hash = sha256(untouched / PCM_PATH)
    mutated_pcm_hash = sha256(mutated / PCM_PATH)
    if sha256(mutated / "manifest.json") != manifest_hash:
        raise RuntimeError("mutated copy changed manifest.json")
    if sha256(mutated / "provider.json") != provider_hash:
        raise RuntimeError("mutated copy changed provider.json")

    cases: list[dict[str, object]] = []
    for label, bundle in (("untouched", untouched), ("mutated", mutated)):
        replay_sink = run_root / f"{label}-session-output.pcm"
        commands = [
            (
                "session_replay",
                [
                    str(binary),
                    "--config-dir",
                    str(config_dir),
                    "session",
                    "replay",
                    str(bundle),
                ],
            ),
            (
                "session_replay_flag",
                [
                    str(binary),
                    "--config-dir",
                    str(config_dir),
                    "session",
                    "--replay",
                    str(bundle),
                    "--audio-out",
                    str(replay_sink),
                    "--max-duration",
                    "60s",
                ],
            ),
        ]
        for route, command in commands:
            result = run_public_command(
                command,
                cwd=root,
                stdout_path=run_root / f"{label}-{route}.stdout",
                stderr_path=run_root / f"{label}-{route}.stderr",
            )
            result["label"] = label
            result["route"] = route
            result["bundle"] = str(bundle)
            result["combined_output"] = text_at(Path(result["stdout"])) + text_at(
                Path(result["stderr"])
            )
            cases.append(result)

    def successful(case: dict[str, object]) -> bool:
        return case["exit_code"] == 0 and not case["timed_out"]

    untouched_cases = [case for case in cases if case["label"] == "untouched"]
    mutated_cases = [case for case in cases if case["label"] == "mutated"]
    if not all(successful(case) for case in untouched_cases):
        raise RuntimeError("untouched public recording bundle did not pass")
    if mode == "baseline":
        if not all(successful(case) for case in mutated_cases):
            raise RuntimeError("baseline mutated control did not reproduce the false pass")
    else:
        for case in mutated_cases:
            if successful(case):
                raise RuntimeError(f"candidate accepted mutated control: {case['route']}")
            output = str(case["combined_output"])
            if "audio/out-000.pcm" not in output:
                raise RuntimeError(
                    f"candidate mutation failure omitted artifact-specific path: {case['route']}"
                )

    result = {
        "mode": mode,
        "source_bundle": str(source),
        "binary": str(binary.resolve()),
        "binary_sha256": sha256(binary),
        "run_root": str(run_root),
        "manifest_sha256": manifest_hash,
        "provider_sha256": provider_hash,
        "untouched_pcm_sha256": untouched_pcm_hash,
        "mutated_pcm_sha256": mutated_pcm_hash,
        "pcm_size": (source / PCM_PATH).stat().st_size,
        "cases": cases,
    }
    (run_root / "result.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("baseline", "candidate"))
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    try:
        result = run_probe(args.mode, args.binary.resolve(), args.evidence)
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "error": str(error)}), file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
