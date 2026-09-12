#!/usr/bin/env python3
"""Fail-closed verifier for the bounded C106 task evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


TASK = "audio-runtime-c106-repair-c68-cross-rate-probe-oracle"
SOURCE_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
STARTUP_ANCESTOR = "8bdafc7f947a3a2c9856220abdc539437035bd21"
TEST_RELATIVE = Path("agent-cli/internal/services/internal/agentruntime/session_replay_rate_domain_contract_test.go")
C102_RELATIVE = Path("docs/temp/projects/audio-runtime/audio-runtime-c102-c68-zero-audio-attribution-vertical-probe.json")
C102_SHA256 = "be9653bda364f5017516d049c06e13ebae6527759b6a4f5eb549b31c03f0001e"
TEST_SHA256 = "2ac6f9d7fee9752f04a14a9fed991a2eaf4b4e9d3085cfb95434daa42b1c6524"


def fail(message: str) -> None:
    raise SystemExit(f"C106 verifier FAILED: {message}")


def load_json(path: Path) -> dict:
    try:
        with path.open(encoding="utf-8") as stream:
            value = json.load(stream)
    except Exception as exc:  # pragma: no cover - bounded CLI diagnostic
        fail(f"read {path}: {exc}")
    if not isinstance(value, dict):
        fail(f"{path} is not a JSON object")
    return value


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
    except OSError as exc:
        fail(f"read {path}: {exc}")
    return digest.hexdigest()


def rtk(*args: str, allow_failure: bool = False) -> str:
    result = subprocess.run(["rtk", *args], capture_output=True, text=True)
    if result.returncode and not allow_failure:
        fail(f"rtk {' '.join(args)} exited {result.returncode}: {result.stderr.strip()}")
    return result.stdout.strip()


def rtk_succeeds(*args: str) -> bool:
    result = subprocess.run(["rtk", *args], capture_output=True, text=True)
    return result.returncode == 0


def repository_root() -> Path:
    return Path(__file__).resolve().parents[5]


def factory_root() -> Path:
    configured = os.environ.get("FACTORY_ROOT")
    if configured:
        return Path(configured).resolve()
    return Path("/Users/abdifamily/.codex/worktrees/af44/go-agent-harness")


def check_provenance(root: Path, evidence: Path) -> None:
    provenance = load_json(evidence / "provenance.json")
    if provenance.get("project") != "audio-runtime" or provenance.get("work") != TASK:
        fail("provenance project/work does not match the admitted task")
    branch = rtk("git", "rev-parse", "--abbrev-ref", "HEAD")
    if branch != provenance.get("branch") or branch != provenance.get("prdBranchName"):
        fail(f"branch mismatch: current={branch!r} evidence={provenance.get('branch')!r}")
    if provenance.get("sourceRevision") != SOURCE_REVISION:
        fail("source revision differs from the admitted current main")
    if rtk("git", "rev-parse", "origin/main") != provenance.get("fetchedOriginMain"):
        fail("origin/main moved after the recorded fetch")
    for ancestor in (STARTUP_ANCESTOR, SOURCE_REVISION):
        if not rtk_succeeds("git", "merge-base", "--is-ancestor", ancestor, "HEAD"):
            fail(f"required ancestor check failed: {ancestor}")
    test_path = root / TEST_RELATIVE
    if sha256(test_path) != TEST_SHA256:
        fail("owned regression source hash changed without refreshed evidence")
    c102_path = factory_root() / C102_RELATIVE
    if sha256(c102_path) != C102_SHA256:
        fail("canonical C102 report hash changed or is unavailable")
    c102 = load_json(c102_path)
    if c102.get("decision") != "FAILED" or c102.get("sourceRevision") != SOURCE_REVISION:
        fail("canonical C102 report is not the immutable FAILED report admitted by C106")


def check_cross_rate(root: Path, evidence: Path) -> None:
    comparison = load_json(evidence / "comparison.json")
    provider = comparison.get("provider", {})
    public = comparison.get("public", {})
    if provider != {
        "sampleRateHz": 24000,
        "samples": 2400,
        "bytes": 4800,
        "sha256": "92b09984282be8a9cf7900c6659b5ee3c42fbe496eb38cd22f2457a34d1d6fd7",
    }:
        fail("provider cross-rate evidence does not match the focused run")
    if public != {
        "sampleRateHz": 16000,
        "samples": 1600,
        "bytes": 3200,
        "sha256": "f8e8572a333e96b573b345430748df9bba3e9b39251f23fcd690607c1b343cc8",
    }:
        fail("public cross-rate evidence does not match the focused run")
    oracle = comparison.get("oracle", {})
    if not oracle.get("exactSampleEquality") or oracle.get("rawUnequalRateByteIdentity"):
        fail("cross-rate evidence permits non-exact or raw unequal-rate comparison")
    source = (root / TEST_RELATIVE).read_text(encoding="utf-8")
    for required in ("NewPCM16Resampler", "Process(providerSamples, true)", "errC106OracleSampleMismatch"):
        if required not in source:
            fail(f"oracle source is missing fail-closed marker {required!r}")
    negatives = load_json(evidence / "negative-controls.json").get("controls", [])
    if len(negatives) != 4 or not all(control.get("expectedRejection") for control in negatives):
        fail("not all four negative controls are fail-closed")


def check_same_rate(evidence: Path) -> None:
    comparison = load_json(evidence / "comparison.json")
    same_rate = comparison.get("sameRateControl", {})
    if same_rate.get("providerRateHz") != 16000 or same_rate.get("publicRateHz") != 16000:
        fail("same-rate control rates are not both 16 kHz")
    if same_rate.get("providerBytes") != 128 or same_rate.get("publicBytes") != 128:
        fail("same-rate control is not the exact 128-byte contract")
    if not same_rate.get("recordingOffEqualsRecordingOn"):
        fail("same-rate recording-mode identity is not proven")
    observer = comparison.get("recordingObserver", {})
    if observer.get("status") != "RECORDING_OBSERVER_OMISSION_UNRESOLVED":
        fail("C68 observer omission was incorrectly relabeled")
    if observer.get("acceptedAudio") != 0 or observer.get("audioBytes") != 0:
        fail("C68 observer omission counters are not zero")


def check_cleanup(evidence: Path) -> None:
    terminal = load_json(evidence / "comparison.json").get("terminal", {})
    if not terminal.get("replayDone") or terminal.get("survivingReplayWorkers") != 0:
        fail("bounded replay cleanup evidence is incomplete")


def check_scope(root: Path) -> None:
    changed = [
        line
        for line in rtk("git", "diff", "--name-only", f"{SOURCE_REVISION}...HEAD").splitlines()
        if line and line != "Changes:"
    ]
    allowed_prefixes = (
        str(TEST_RELATIVE),
        "docs/temp/projects/audio-runtime/audio-runtime-c106-repair-c68-cross-rate-probe-oracle/",
    )
    outside = [path for path in changed if not path.startswith(allowed_prefixes)]
    if outside:
        fail(f"changed paths outside C106 ownership: {outside}")
    status_lines = [
        line
        for line in rtk("git", "status", "--porcelain", "--untracked-files=all").splitlines()
        if line and line not in {"Changes:", "clean — nothing to commit"}
    ]
    if status_lines:
        fail("worktree is not clean at staged-artifact verification")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("provenance", "cross-rate", "same-rate", "cleanup", "vertical-report", "staged-artifact", "all"), required=True)
    args = parser.parse_args()
    root = repository_root()
    evidence = Path(__file__).resolve().parent
    if args.mode in {"provenance", "vertical-report", "staged-artifact", "all"}:
        check_provenance(root, evidence)
    if args.mode in {"cross-rate", "vertical-report", "staged-artifact", "all"}:
        check_cross_rate(root, evidence)
    if args.mode in {"same-rate", "vertical-report", "staged-artifact", "all"}:
        check_same_rate(evidence)
    if args.mode in {"cleanup", "vertical-report", "staged-artifact", "all"}:
        check_cleanup(evidence)
    if args.mode in {"staged-artifact", "all"}:
        check_scope(root)
    print(f"C106 verifier PASS mode={args.mode} task={TASK}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
