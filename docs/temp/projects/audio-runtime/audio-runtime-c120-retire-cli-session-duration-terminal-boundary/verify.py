#!/usr/bin/env python3
"""Fail-closed C120 provenance, ownership, service, and mutation checks."""

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
WORK = "audio-runtime-c120-retire-cli-session-duration-terminal-boundary"
BRANCH = "codex/audio-runtime-c120-retire-cli-session-duration-terminal-boundary"
BASE = "3963bc3566da24f8214634c17a9d0f79a6724171"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
TARGET = "agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go"
LOOP = "agent-cli/internal/services/internal/agentruntime/session_duration_loop.go"
ADAPTER = "agent-cli/internal/services/internal/agentruntime/session_duration_terminal_adapter.go"
LOOP_BASELINE = "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_duration_loop.go.json"
TARGET_SHA256 = "82917c1a30ceed01da41109e368c2c6851b2ed6c5e5b689959f9d8faa5f88b88"
BASE_LOOP_SHA256 = "2bf32471d10af9117600c5006d453fb12beb6a134e0461f0e6b895005270591f"
REPAIRED_LOOP_SHA256 = "cd65dff97f8d58def4bac1dc0cf81b869a392ac366e6a19dee54359ff6e637ba"

ALLOWED_EXACT = {TARGET, LOOP, LOOP_BASELINE, ADAPTER, "agent-cli/internal/services/internal/agentruntime/session_duration_terminal_service_test.go", "docs/architecture/architecture-policy.json"}
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/sessionduration/",
    "coverage-manifest/go-agent-runtime/services/sessionduration/",
    "docs/temp/projects/audio-runtime/audio-runtime-c120-retire-cli-session-duration-terminal-boundary/",
)
FORBIDDEN_PEER_PREFIXES = (
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_failure.go",
    "agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go",
)


class VerificationFailure(RuntimeError):
    pass


def command(args: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None, expect: int = 0) -> subprocess.CompletedProcess[str]:
    merged = dict(os.environ)
    if env:
        merged.update(env)
    print("==>", " ".join(args), flush=True)
    result = subprocess.run(args, cwd=cwd, env=merged, text=True, capture_output=True)
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)
    if result.returncode != expect:
        raise VerificationFailure(f"command exited {result.returncode}, expected {expect}: {' '.join(args)}")
    return result


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def git_show(path: str) -> bytes:
    result = subprocess.run(["git", "show", f"{BASE}:{path}"], cwd=ROOT, capture_output=True, check=True)
    return result.stdout


def verify_provenance() -> None:
    if not FACTORY_ROOT or not (FACTORY_ROOT / "factory/scripts/project-control.py").is_file():
        raise VerificationFailure("FACTORY_ROOT is unavailable or lacks project-control.py")
    admission = command([
        sys.executable,
        str(FACTORY_ROOT / "factory/scripts/project-control.py"),
        "verify-work",
        "--type",
        "task",
        "--name",
        WORK,
        "--root",
        str(FACTORY_ROOT),
    ], cwd=FACTORY_ROOT).stdout.strip()
    payload = json.loads(admission)
    if payload != {"status": "admitted", "project": "audio-runtime", "name": WORK}:
        raise VerificationFailure(f"unexpected admission payload: {payload}")
    branch = command(["git", "rev-parse", "--abbrev-ref", "HEAD"]).stdout.strip()
    if branch != BRANCH:
        raise VerificationFailure(f"branch {branch!r} does not match prd.branchName {BRANCH!r}")
    command(["git", "merge-base", "--is-ancestor", STARTUP, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", BASE, "HEAD"])
    original = git_show(TARGET)
    if len(original.splitlines()) != 131 or sha256(original) != TARGET_SHA256:
        raise VerificationFailure("immutable 131-line terminal source identity changed")
    base_loop = git_show(LOOP)
    if sha256(base_loop) != BASE_LOOP_SHA256:
        raise VerificationFailure("immutable pre-mutation duration loop identity changed")
    verify_repaired_loop()


def verify_repaired_loop() -> None:
    loop = (ROOT / LOOP).read_bytes()
    if sha256(loop) != REPAIRED_LOOP_SHA256:
        raise VerificationFailure("duration loop differs from the authorized terminal-state repair")


def verify_repaired_baseline() -> None:
    result = subprocess.run(
        ["git", "diff", "--unified=0", "origin/main...HEAD", "--", LOOP_BASELINE],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=True,
    )
    actual = [
        line
        for line in result.stdout.splitlines()
        if line.startswith(("-", "+")) and not line.startswith(("---", "+++"))
    ]
    expected = [
        '-      "value": 534,',
        '+      "value": 533,',
        '-      "value": 285,',
        '+      "value": 284,',
        '-      "value": 203,',
        '+      "value": 202,',
    ]
    if actual != expected:
        raise VerificationFailure(f"duration baseline diff is not the authorized downward repair: {actual}")


def diff_base() -> str:
    result = subprocess.run(["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"], cwd=ROOT)
    return "origin/main" if result.returncode == 0 else BASE


def changed_paths() -> list[str]:
    base = diff_base()
    output = command(["git", "diff", "--name-only", f"{base}...HEAD"]).stdout
    return [line for line in output.splitlines() if line]


def verify_ownership() -> None:
    changed = changed_paths()
    for path in changed:
        if path in FORBIDDEN_PEER_PREFIXES or any(path.startswith(prefix) for prefix in FORBIDDEN_PEER_PREFIXES):
            raise VerificationFailure(f"C120 changed an excluded peer path: {path}")
        if path != TARGET and path not in ALLOWED_EXACT and not any(path.startswith(prefix) for prefix in ALLOWED_PREFIXES):
            raise VerificationFailure(f"path outside admitted C120 ownership: {path}")
    verify_repaired_loop()
    verify_repaired_baseline()
    if (ROOT / TARGET).exists():
        raise VerificationFailure("legacy session_duration_terminal.go still exists")
    adapter = ROOT / ADAPTER
    if not adapter.is_file() or len(adapter.read_text(encoding="utf-8").splitlines()) > 41:
        raise VerificationFailure("compatibility adapter is missing or exceeds 41 physical lines")
    if "Deprecated" not in adapter.read_text(encoding="utf-8"):
        raise VerificationFailure("adapter lacks an explicit Deprecated compatibility marker")
    for path in (ROOT / "go-agent-runtime/services/sessionduration").rglob("*.go"):
        imports = [line.strip() for line in path.read_text(encoding="utf-8").splitlines() if line.strip().startswith('"') or line.strip().startswith("`")]
        forbidden = ("agent-cli", "internal/agentruntime", "pflag", "cobra", "syscall", "os/exec")
        if any(token in line for line in imports for token in forbidden):
            raise VerificationFailure(f"host-bound import in sessionduration service: {path}")


def go_tests() -> None:
    env = {"GOWORK": "off"}
    command(["go", "test", "./services/sessionduration/...", "-count=1"], cwd=ROOT / "go-agent-runtime", env=env)
    command(["go", "test", "-race", "./services/sessionduration/...", "-count=1"], cwd=ROOT / "go-agent-runtime", env=env)
    pattern = "^(TestRunSessionWithMaxDuration_PreservesProviderTerminalDuringShutdown|TestRunAgentLoopSessionWithDuration_ProviderDoneDrainsAcceptedOutput|TestRunAgentLoopSessionWithDuration_PreservesFailureIdentity|TestRunSessionDurationPlan_PreservesFlushAndFinalizeFailures|TestRunAgentLoopSessionWithDurationTerminalOutcomesAlwaysDrainAcceptedDelta|TestRunAgentLoopSessionTerminalOutcomesAlwaysDrainAcceptedDelta)$"
    command(["go", "test", "./internal/services/internal/agentruntime", "-run", pattern, "-count=1", "-timeout", "180s"], cwd=ROOT / "agent-cli", env=env)
    command(["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", pattern, "-count=1", "-timeout", "180s"], cwd=ROOT / "agent-cli", env=env)
    consumer = HERE / "external-consumer"
    command(["go", "test", "./...", "-count=1"], cwd=consumer, env=env)


def expect_mutant_failure(needle: str, replacement: str, test_name: str) -> None:
    source_path = ROOT / "go-agent-runtime/services/sessionduration/internal/service/service.go"
    source = source_path.read_text(encoding="utf-8")
    if source.count(needle) != 1:
        raise VerificationFailure(f"mutation needle is not unique: {needle}")
    with tempfile.TemporaryDirectory(prefix="c120-mutant-") as directory:
        mutant = Path(directory) / "service.go"
        mutant.write_text(source.replace(needle, replacement, 1), encoding="utf-8")
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source_path): str(mutant)}}), encoding="utf-8")
        result = subprocess.run(
            ["go", "test", "-overlay", str(overlay), "./services/sessionduration/internal/service", "-run", f"^{test_name}$", "-count=1"],
            cwd=ROOT / "go-agent-runtime",
            env=dict(os.environ, GOWORK="off"),
            text=True,
            capture_output=True,
        )
        print(result.stdout, end="")
        print(result.stderr, end="", file=sys.stderr)
        if result.returncode == 0:
            raise VerificationFailure(f"mutant unexpectedly passed: {test_name}")


def verify_mutations() -> None:
    expect_mutant_failure(
        "if s.source.Matches != nil && s.source.Matches(msg) {",
        "if false {",
        "TestStateAdmitsProviderTerminalBeforeLoopClose",
    )
    expect_mutant_failure(
        "if publication.Artifacts != nil {",
        "if false {",
        "TestStatePublishesProviderTerminalOnceInArtifactThenOutputOrder",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", required=True, choices=("provenance", "excluded-paths-and-adapter", "positive-and-two-mutations", "retirement-and-owned-paths", "final", "all"))
    args = parser.parse_args()
    try:
        if args.mode in {"provenance", "final", "all"}:
            verify_provenance()
        if args.mode in {"excluded-paths-and-adapter", "final", "all"}:
            verify_ownership()
        if args.mode in {"positive-and-two-mutations", "final", "all"}:
            go_tests()
            verify_mutations()
        if args.mode in {"retirement-and-owned-paths", "final", "all"}:
            verify_provenance()
            verify_ownership()
        print(f"C120 {args.mode}: PASS")
        return 0
    except (OSError, subprocess.CalledProcessError, json.JSONDecodeError, VerificationFailure) as error:
        print(f"C120 {args.mode}: FAIL: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
