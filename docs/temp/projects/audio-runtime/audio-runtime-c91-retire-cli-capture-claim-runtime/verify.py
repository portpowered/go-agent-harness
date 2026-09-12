#!/usr/bin/env python3
"""Fail-closed C91 boundary, behavior, mutation, and scope checks."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path


EVIDENCE = Path(__file__).resolve().parent
ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=EVIDENCE,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
TASK = "audio-runtime-c91-retire-cli-capture-claim-runtime"
BRANCH = "codex/audio-runtime-c91-retire-cli-capture-claim-runtime"
BASELINE = "59af6325614d80173447fe2018a0471e27b4e7b1"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_capture_claim.go"
LEGACY_SHA256 = "1747b40578f4788b1b698495652f7e8f85abbff278eab904581cd38edcc8b6cc"
EVIDENCE_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c91-retire-cli-capture-claim-runtime/"
CAPTURE_PREFIX = "go-agent-runtime/services/captureclaim/"
COVERAGE_PREFIX = "coverage-manifest/go-agent-runtime/services/captureclaim/"

EXCLUDED = (
    "agent-cli/internal/services/internal/agentruntime/session.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_out.go",
    "agent-cli/internal/services/internal/agentruntime/session_duration.go",
    "agent-cli/internal/services/internal/agentruntime/session_image.go",
    "agent-cli/internal/services/internal/agentruntime/session_instructions.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording_directory_claim.go",
    "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go",
    "agent-cli/internal/services/internal/agentruntime/session_text.go",
    "agent-cli/internal/services/internal/agentruntime/session_options.go",
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def run(command: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None, timeout: int = 300) -> str:
    result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, check=False)
    if result.returncode != 0:
        detail = (result.stdout + "\n" + result.stderr).strip()
        raise VerificationError(f"command failed ({result.returncode}): {' '.join(command)}\n{detail[-4000:]}")
    return result.stdout.strip()


def git(*args: str) -> str:
    return run(["git", *args], timeout=30)


def file_sha(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_file_sha(revision: str, path: str) -> str:
    result = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=ROOT, capture_output=True, check=False)
    require(result.returncode == 0, f"missing baseline file: {path}")
    return hashlib.sha256(result.stdout).hexdigest()


def ancestor(older: str, newer: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", older, newer], cwd=ROOT, check=False, timeout=30).returncode == 0


def current_changed_paths() -> set[str]:
	paths = set(filter(None, git("diff", "--name-only", BASELINE, "HEAD").splitlines()))
	paths.update(filter(None, git("diff", "--name-only").splitlines()))
	paths.update(filter(None, git("diff", "--cached", "--name-only").splitlines()))
	paths.update(filter(None, git("ls-files", "--others", "--exclude-standard").splitlines()))
	return paths


def check_boundaries() -> None:
    require(git("branch", "--show-current") == BRANCH, "branch does not match the admitted worktree")
    prd = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    require(prd.get("project") == "audio-runtime", "prd project changed")
    require(prd.get("branchName") == BRANCH, "prd branchName does not match the isolated branch")
    require(ancestor(BASELINE, "HEAD"), "candidate does not preserve accepted main ancestry")
    require(ancestor(STARTUP_INTEGRATION, "HEAD"), "candidate does not preserve startup integration ancestry")
    require(ancestor(BASELINE, "origin/main"), "origin/main is not the admitted baseline descendant")

    baseline = subprocess.run(["git", "show", f"{BASELINE}:{LEGACY}"], cwd=ROOT, capture_output=True, check=True).stdout
    require(len(baseline.splitlines()) == 317, "accepted legacy baseline is not exactly 317 lines")
    require(hashlib.sha256(baseline).hexdigest() == LEGACY_SHA256, "accepted legacy baseline hash changed")
    legacy = ROOT / LEGACY
    require(legacy.is_file(), "legacy adapter disappeared unexpectedly")
    legacy_source = legacy.read_text(encoding="utf-8")
    require(len(legacy_source.splitlines()) <= 77, "legacy adapter exceeds the 77-line retirement boundary")
    require(317 - len(legacy_source.splitlines()) >= 240, "legacy adapter retired fewer than 240 lines")
    forbidden_imports = (
        '"encoding/json"',
        '"errors"',
        '"fmt"',
        '"os"',
        '"path/filepath"',
        '"sync"',
        '"time"',
    )
    require(not any(marker in legacy_source for marker in forbidden_imports), "legacy adapter retained filesystem or policy imports")
    require("captureclaimwire.NewService" in legacy_source and "captureclaim.Err" in legacy_source, "legacy adapter is not wired to the public contract")

    contract = (ROOT / CAPTURE_PREFIX / "contract.go").read_text(encoding="utf-8")
    implementation = "\n".join(
        path.read_text(encoding="utf-8")
        for path in sorted((ROOT / CAPTURE_PREFIX / "internal/service").glob("*.go"))
        if not path.name.endswith("_test.go")
    )
    wire_provider = (ROOT / CAPTURE_PREFIX / "wire/providers.go").read_text(encoding="utf-8")
    wire_generated = (ROOT / CAPTURE_PREFIX / "wire/wire_gen.go").read_text(encoding="utf-8")
    for marker in ("type Claim interface", "type Service interface", "type Dependencies struct", "ErrClaimLost"):
        require(marker in contract, f"public contract marker missing: {marker}")
    for marker in ("O_EXCL", "CreateTemp", "SameFile", "ObserveHolder", "func (c *claim) Publish", "func (c *claim) Release"):
        require(marker in implementation, f"private implementation marker missing: {marker}")
    require("internal/service" in wire_provider and "wire.Build" in wire_provider, "dedicated Wire provider is missing private construction")
    require("Code generated by Wire" in wire_generated and "service.New" in wire_generated, "generated Wire output is stale or absent")
    require("agent-cli" not in contract + implementation + wire_provider + wire_generated, "runtime captureclaim imports agent-cli")

    consumer = EVIDENCE / "external-consumer/consumer_test.go"
    consumer_source = consumer.read_text(encoding="utf-8")
    imports = set(re.findall(r'"([^\"]+)"', consumer_source))
    require("github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim" in imports, "consumer does not import the public contract")
    require("github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim/wire" in imports, "consumer does not import the public Wire")
    require(not any("agent-cli" in value or "/internal/" in value for value in imports), "consumer imports a private or CLI package")

    for path in ("package.json", "internal/service/package.json", "wire/package.json"):
        manifest = json.loads((ROOT / COVERAGE_PREFIX / path).read_text(encoding="utf-8"))
        require(isinstance(manifest.get("minimum"), (int, float)), f"coverage floor missing: {path}")
        require(manifest["minimum"] >= 80, f"coverage floor is unexpectedly low: {path}")


def check_scope() -> None:
    changed = current_changed_paths()
    require(changed, "candidate diff is empty")
    allowed_exact = {LEGACY, "agent-cli/internal/services/internal/agentruntime/session_capture_claim_test.go"}
    require(
        all(path in allowed_exact or path.startswith(CAPTURE_PREFIX) or path.startswith(COVERAGE_PREFIX) or path.startswith(EVIDENCE_PREFIX) for path in changed),
        f"candidate escaped C91 ownership: {sorted(changed)}",
    )
    for path in EXCLUDED:
        require(not ancestor(BASELINE, "HEAD") or git("diff", "--quiet", BASELINE, "HEAD", "--", path) == "", f"excluded path changed: {path}")
        require(git("diff", "--quiet", BASELINE, "--", path) == "", f"excluded worktree path changed: {path}")
    require("scripts/wire-packages.txt" not in changed, "C79 Wire registry was edited before release")
    require("docs/architecture/architecture-size-baseline.json" not in changed, "C79 architecture baseline was edited before release")


def check_failure_matrix() -> None:
    selector = "Acquire|Publish|Release|ObserveHolder|MutationOracle|Contract|Independent|Competing|Destination|Replaced|Sync"
    run(["go", "test", "./go-agent-runtime/services/captureclaim/...", "-run", selector, "-count=1", "-timeout", "240s"], timeout=300)
    run(["go", "test", "-race", "./go-agent-runtime/services/captureclaim/...", "-run", selector, "-count=1", "-timeout", "300s"], timeout=360)
    consumer_env = os.environ.copy()
    consumer_env["GOWORK"] = "off"
    run(["go", "test", "./...", "-count=1", "-timeout", "120s"], cwd=EVIDENCE / "external-consumer", env=consumer_env, timeout=180)


def check_retirement_and_adapter() -> None:
    run(
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "SessionRecordingClaim",
            "-count=1",
            "-timeout",
            "240s",
        ],
        timeout=300,
    )


def check_mutations() -> None:
    run(
        [
            "go",
            "test",
            "./go-agent-runtime/services/captureclaim/internal/service",
            "-run",
            "MutationOracle",
            "-count=1",
            "-timeout",
            "120s",
        ],
        timeout=180,
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=("boundaries", "failure-matrix", "retirement-and-adapter", "mutations", "owned-and-excluded-paths", "all"),
        default="all",
    )
    mode = parser.parse_args().mode
    check_boundaries()
    if mode in ("owned-and-excluded-paths", "all"):
        check_scope()
    if mode in ("failure-matrix", "all"):
        check_failure_matrix()
    if mode in ("retirement-and-adapter", "all"):
        check_retirement_and_adapter()
    if mode in ("mutations", "all"):
        check_mutations()
    print(json.dumps({"task": TASK, "mode": mode, "passed": True, "head": git("rev-parse", "HEAD")}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.SubprocessError, VerificationError, json.JSONDecodeError) as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
