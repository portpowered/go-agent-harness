#!/usr/bin/env python3
"""Fail-closed C112 provenance, ownership, service, and mutation checks."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", "")).expanduser()
WORK = "audio-runtime-c112-retire-cli-session-trace-evidence"
BRANCH = "codex/audio-runtime-c112-retire-cli-session-trace-evidence"
BASE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
TRACE = "agent-cli/internal/services/internal/agentruntime/trace.go"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go"
SESSION_OBSERVATION = "agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go"
ORIGINAL_LINES = 119
ORIGINAL_SHA256 = "db9fab41dd02578db5e7b024af869eb762b21ec14c48c64442d4ad9ca3149710"

ALLOWED_EXACT = {
    "agent-cli/internal/services/internal/agentruntime/service.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_trace_test.go",
    SESSION_OBSERVATION,
    TRACE,
    "agent-cli/internal/services/internal/agentruntime/trace_c112_test.go",
    "docs/architecture/architecture-policy.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/trace.go.json",
    "scripts/wire-packages.txt",
}
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/sessiontrace/",
    "coverage-manifest/go-agent-runtime/services/sessiontrace/",
    "docs/temp/projects/audio-runtime/audio-runtime-c112-retire-cli-session-trace-evidence/",
)


class VerificationFailure(RuntimeError):
    pass


def command(args: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    merged = dict(os.environ)
    if env:
        merged.update(env)
    print("==>", " ".join(args), flush=True)
    result = subprocess.run(args, cwd=cwd, env=merged, text=True, capture_output=True)
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)
    if result.returncode:
        raise VerificationFailure(f"command exited {result.returncode}: {' '.join(args)}")
    return result


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def git_show(revision: str, path: str) -> bytes:
    return subprocess.run(["git", "show", f"{revision}:{path}"], cwd=ROOT, capture_output=True, check=True).stdout


def verify_provenance() -> None:
    if not FACTORY_ROOT or not (FACTORY_ROOT / "factory/scripts/project-control.py").is_file():
        raise VerificationFailure("FACTORY_ROOT is unavailable or lacks project-control.py")
    admission = command([
        sys.executable, str(FACTORY_ROOT / "factory/scripts/project-control.py"), "verify-work",
        "--type", "task", "--name", WORK, "--root", str(FACTORY_ROOT),
    ], cwd=FACTORY_ROOT).stdout.strip()
    if json.loads(admission) != {"status": "admitted", "project": "audio-runtime", "name": WORK}:
        raise VerificationFailure(f"unexpected admission payload: {admission}")
    manifest = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    if manifest.get("branchName") != BRANCH:
        raise VerificationFailure(f"prd.branchName={manifest.get('branchName')!r}, want {BRANCH!r}")
    branch = command(["git", "rev-parse", "--abbrev-ref", "HEAD"]).stdout.strip()
    if branch != BRANCH:
        raise VerificationFailure(f"branch={branch!r}, want {BRANCH!r}")
    command(["git", "merge-base", "--is-ancestor", STARTUP, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", BASE, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"])
    original = git_show(BASE, TRACE)
    if len(original.splitlines()) != ORIGINAL_LINES or sha256(original) != ORIGINAL_SHA256:
        raise VerificationFailure("immutable 119-line CLI trace identity changed")


def changed_paths() -> list[str]:
    return [line for line in command(["git", "diff", "--name-only", "origin/main...HEAD"]).stdout.splitlines() if line]


def verify_ownership() -> None:
    for path in changed_paths():
        if path not in ALLOWED_EXACT and not any(path.startswith(prefix) for prefix in ALLOWED_PREFIXES):
            raise VerificationFailure(f"path outside admitted C112 ownership: {path}")
    trace = ROOT / TRACE
    if not trace.is_file() or len(trace.read_text(encoding="utf-8").splitlines()) > 49:
        raise VerificationFailure("retired CLI trace adapter is missing or exceeds 49 physical lines")
    trace_text = trace.read_text(encoding="utf-8")
    if any(token in trace_text for token in ("go-audio/pkg/recording", "os.", "errors.", "fmt.")):
        raise VerificationFailure("CLI trace seam still owns recording or lifecycle policy")
    if (ROOT / LEGACY).exists():
        raise VerificationFailure("legacy session_audio_trace.go still exists")
    evidence = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c112-retire-cli-session-trace-evidence"
    old_consumer = evidence / "consumer"
    if (old_consumer.is_dir() and any(old_consumer.iterdir())) or not (evidence / "external-consumer/go.mod").is_file():
        raise VerificationFailure("required external-consumer path is not the admitted evidence path")
    for path in (ROOT / "go-agent-runtime/services/sessiontrace").rglob("*.go"):
        text = path.read_text(encoding="utf-8")
        if any(token in text for token in ("agent-cli", "internal/agentruntime", "pflag", "cobra", "os/exec", "syscall")):
            raise VerificationFailure(f"host-bound implementation import in {path}")
    if "RuntimeObserver: adaptSessionTraceObserver(options.RuntimeObserver)" not in trace_text:
        raise VerificationFailure("CLI did not pass the prior observer through the service request")
    service = (ROOT / "go-agent-runtime/services/sessiontrace/internal/service/service.go").read_text(encoding="utf-8")
    required = ("runtime := combineObservers(request.RuntimeObserver, observer)", "copyObservation.Payload = append([]byte(nil), observation.Payload...)", "audio evidence retained at %s")
    if any(needle not in service for needle in required):
        raise VerificationFailure("sessiontrace service is missing a required ownership seam")


def go_tests() -> None:
    env = {"GOWORK": "off"}
    command(["go", "test", "./services/sessiontrace/...", "-count=1"], cwd=ROOT / "go-agent-runtime", env=env)
    command(["go", "test", "-race", "./services/sessiontrace/...", "-count=1"], cwd=ROOT / "go-agent-runtime", env=env)
    pattern = "^(TestC112TraceAdapterRequiresInjectedClock|TestC112TraceAdapterKeepsDeviceErrorsAndObserverPolicy)$"
    command(["go", "test", "./internal/services/internal/agentruntime", "-run", pattern, "-count=1"], cwd=ROOT / "agent-cli", env=env)
    command(["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", pattern, "-count=1"], cwd=ROOT / "agent-cli", env=env)
    consumer = HERE / "external-consumer"
    command(["go", "test", "./...", "-count=1"], cwd=consumer, env=env)
    command(["go", "build", "."], cwd=consumer, env=env)


def expect_mutant_failure(source_rel: str, needle: str, replacement: str, cwd_rel: str, package: str, test_name: str) -> None:
    source_path = ROOT / source_rel
    source = source_path.read_text(encoding="utf-8")
    if source.count(needle) != 1:
        raise VerificationFailure(f"mutation needle is not unique in {source_rel}: {needle}")
    with tempfile.TemporaryDirectory(prefix="c112-mutant-") as directory:
        mutant = Path(directory) / source_path.name
        mutant.write_text(source.replace(needle, replacement, 1), encoding="utf-8")
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source_path): str(mutant)}}), encoding="utf-8")
        result = subprocess.run(
            ["go", "test", "-overlay", str(overlay), package, "-run", f"^{test_name}$", "-count=1"],
            cwd=ROOT / cwd_rel, env=dict(os.environ, GOWORK="off"), text=True, capture_output=True,
        )
        print(result.stdout, end="")
        print(result.stderr, end="", file=sys.stderr)
        if result.returncode == 0 or test_name not in result.stdout + result.stderr:
            raise VerificationFailure(f"mutation did not fail causally: {source_rel}::{test_name}")


def verify_mutations() -> None:
    expect_mutant_failure(
        "go-agent-runtime/services/sessiontrace/internal/service/service.go",
        "if !published {", "if bundle != \"\" && !published {", "go-agent-runtime", "./services/sessiontrace/internal/service",
        "TestFinishUnpublishedEmptyBundleRetainsCloseCauseAndStagedPath",
    )
    expect_mutant_failure(
        TRACE, "RuntimeObserver: adaptSessionTraceObserver(options.RuntimeObserver),",
        "// RuntimeObserver dropped by mutation,", "agent-cli", "./internal/services/internal/agentruntime",
        "TestC112TraceAdapterKeepsDeviceErrorsAndObserverPolicy",
    )
    expect_mutant_failure(
        "go-agent-runtime/services/sessiontrace/internal/service/service.go",
        "copyObservation.Payload = append([]byte(nil), observation.Payload...)",
        "copyObservation.Payload = observation.Payload", "go-agent-runtime", "./services/sessiontrace/internal/service",
        "TestPreparedCapturesEdgesRedactsAndPublishes",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", required=True, choices=("provenance", "retirement-and-owned-paths", "positive-and-three-mutations", "final", "all"))
    mode = parser.parse_args().mode
    try:
        if mode in {"provenance", "retirement-and-owned-paths", "final", "all"}:
            verify_provenance()
        if mode in {"retirement-and-owned-paths", "final", "all"}:
            verify_ownership()
        if mode in {"positive-and-three-mutations", "final", "all"}:
            go_tests()
            verify_mutations()
        print(f"C112 {mode}: PASS")
        return 0
    except (OSError, subprocess.CalledProcessError, json.JSONDecodeError, VerificationFailure) as error:
        print(f"C112 {mode}: FAIL: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
