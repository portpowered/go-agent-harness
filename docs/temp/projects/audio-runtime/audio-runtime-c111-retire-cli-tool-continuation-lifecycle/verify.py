#!/usr/bin/env python3
"""Run bounded C111 ownership, contract, and accumulated regression checks."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time
from typing import Any


EVIDENCE_DIR = Path(__file__).resolve().parent
ROOT = EVIDENCE_DIR.parents[4]
AGENT_CLI = ROOT / "agent-cli"
RUNTIME = ROOT / "go-agent-runtime"
CONSUMER = EVIDENCE_DIR / "external-consumer"
ADMISSION = EVIDENCE_DIR / "admission.json"
LEGACY = ROOT / "agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go"
BASELINE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
INTEGRATED_MAIN = "1a8467246c6607a06ffc7289075da2595724ce8b"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_LINES = 147
BASELINE_SHA256 = "48000dcd73b85ebbe93fd40a3996529878a99c799b67b0779d60f55dbb43fc1e"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c111-retire-cli-tool-continuation-lifecycle/"
ARCHITECTURE_POLICY = "docs/architecture/architecture-policy.json"
FORBIDDEN_PATHS = {
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go",
}
FORBIDDEN_ENV = {"OPENAI_API_KEY", "REALTIME_API_KEY", "YUI_API_KEY", "ANTHROPIC_API_KEY"}


class VerificationFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def scrubbed_environment() -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        if name in FORBIDDEN_ENV or name.endswith("_API_KEY"):
            del environment[name]
    environment["GOWORK"] = "off"
    return environment


def git(*args: str) -> str:
    result = subprocess.run(
        ["rtk", "proxy", "git", *args],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    require(result.returncode == 0, result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def run_command(label: str, argv: list[str], cwd: Path, timeout: float = 360.0) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=scrubbed_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or ""
        stderr = exc.stderr or ""
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            more_stdout, more_stderr = process.communicate(timeout=3)
            stdout, stderr = more_stdout, more_stderr
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
    if isinstance(stdout, bytes):
        stdout = stdout.decode(errors="replace")
    if isinstance(stderr, bytes):
        stderr = stderr.decode(errors="replace")
    return {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "duration_seconds": round(time.monotonic() - started, 6),
        "stdout": stdout[:65536],
        "stderr": stderr[:65536],
        "output_capped": len(stdout) > 65536 or len(stderr) > 65536,
        "reaped": process.poll() is not None,
    }


def verify_admission() -> dict[str, Any]:
    require(ADMISSION.is_file(), f"missing admission evidence: {ADMISSION}")
    admission = json.loads(ADMISSION.read_text(encoding="utf-8"))
    require(admission.get("project") == "audio-runtime", "admission project changed")
    require(admission.get("contractRevision") == "audio-runtime-v1", "contract revision changed")
    require(admission.get("verifyWork", {}).get("status") == "admitted", "project admission is not admitted")
    require(admission.get("secondProject") is False, "second project waiver detected")
    require(admission.get("acceptanceWaiver") is False, "acceptance waiver detected")
    require(admission.get("requiredAncestry", {}).get("startupIsAncestor") is True, "startup ancestry evidence missing")
    require(admission.get("requiredAncestry", {}).get("acceptedMainIsAncestor") is True, "accepted-main ancestry evidence missing")
    require(admission.get("immutableSource", {}).get("physicalLines") == BASELINE_LINES, "baseline line census changed")
    require(admission.get("immutableSource", {}).get("sha256") == BASELINE_SHA256, "baseline source hash changed")
    prd = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    branch = git("branch", "--show-current")
    require(prd.get("branchName") == branch, f"branch mismatch: prd={prd.get('branchName')} current={branch}")
    require(admission.get("branchMatchesPRD") is True, "pre-mutation branch evidence did not match PRD")
    return {"project": admission["project"], "contractRevision": admission["contractRevision"], "branch": branch}


def verify_source_and_scope() -> dict[str, Any]:
    baseline = subprocess.run(
        ["rtk", "proxy", "git", "show", f"{BASELINE}:agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go"],
        cwd=ROOT,
        capture_output=True,
        check=True,
    ).stdout
    current = LEGACY.read_bytes()
    require(len(baseline.splitlines()) == BASELINE_LINES and digest(baseline) == BASELINE_SHA256, "immutable baseline source census failed")
    require(len(current.splitlines()) <= 57, f"legacy adapter has {len(current.splitlines())} physical lines")
    # The accepted main integration is an input to this candidate, not a C111
    # mutation. Compare scope from that exact merged-main revision so the
    # verifier still rejects unrelated changes while allowing the one
    # demonstrated generated-file registration below.
    changed = set(filter(None, git("diff", "--name-only", INTEGRATED_MAIN).splitlines()))
    changed.update(filter(None, git("ls-files", "--others", "--exclude-standard").splitlines()))
    forbidden = sorted(path for path in changed if path in FORBIDDEN_PATHS)
    allowed = {
        "agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go",
        "agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle_c111_test.go",
        "go-agent-runtime/services/sessioncontinuation/",
        "coverage-manifest/go-agent-runtime/services/sessioncontinuation/",
        ARCHITECTURE_POLICY,
        OWNED_PREFIX,
    }
    outside = sorted(
        path for path in changed
        if not any(path == item or path.startswith(item) for item in allowed)
    )
    require(not forbidden, f"forbidden shared path changed: {forbidden}")
    require(not outside, f"changed path outside C111 ownership: {outside}")
    base_policy = json.loads(git("show", f"{INTEGRATED_MAIN}:{ARCHITECTURE_POLICY}"))
    current_policy = json.loads((ROOT / ARCHITECTURE_POLICY).read_text(encoding="utf-8"))
    continuation_rule = {
        "module": "go-agent-runtime",
        "pattern": "services/sessioncontinuation/wire/wire_gen.go",
        "generator": "wire",
        "header": "// Code generated by Wire. DO NOT EDIT.",
    }
    base_generated = base_policy.get("generated_files", [])
    current_generated = current_policy.get("generated_files", [])
    require(continuation_rule not in base_generated, "integrated main already contains the C111 Wire registration")
    require(current_generated.count(continuation_rule) == 1, "C111 Wire registration is missing or duplicated")
    without_continuation = [rule for rule in current_generated if rule != continuation_rule]
    require(without_continuation == base_generated, "architecture policy changed beyond the C111 Wire registration")
    for required in (
        RUNTIME / "services/sessioncontinuation/contract.go",
        RUNTIME / "services/sessioncontinuation/internal/service/service.go",
        RUNTIME / "services/sessioncontinuation/wire/wire.go",
        RUNTIME / "services/sessioncontinuation/wire/wire_gen.go",
        RUNTIME / "services/sessioncontinuation/wire/alias.go",
    ):
        require(required.is_file(), f"required continuation file missing: {required}")
    source_text = "\n".join(path.read_text(encoding="utf-8") for path in RUNTIME.glob("services/sessioncontinuation/**/*.go"))
    require("agent-cli" not in source_text, "reusable continuation package imports CLI code")
    require("os.Getenv" not in source_text and "os.LookupEnv" not in source_text, "continuation package reads environment")
    require("func init(" not in source_text and "time.Sleep" not in source_text, "continuation package contains hidden or sleeping policy")
    return {
        "baseline_lines": BASELINE_LINES,
        "baseline_sha256": BASELINE_SHA256,
        "integrated_main": INTEGRATED_MAIN,
        "final_adapter_lines": len(current.splitlines()),
        "changed_paths": sorted(changed),
    }


def focused_checks() -> list[dict[str, Any]]:
    commands = [
        (
            "runtime-contract",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./services/sessioncontinuation/...", "-run", "Contract|Normalize|Unresolved|Image|Tool|Metadata|Join|Precedence|Immutable|Independent|Concurrent|Cancel|ZeroSurvivors|Wire", "-count=1", "-timeout=180s"],
            RUNTIME,
            180.0,
        ),
        (
            "runtime-race",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "-race", "./services/sessioncontinuation/...", "-run", "Immutable|Independent|Concurrent|Join|Precedence|Cancel|ZeroSurvivors", "-count=3", "-timeout=300s"],
            RUNTIME,
            300.0,
        ),
        (
            "external-consumer",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=120s"],
            CONSUMER,
            120.0,
        ),
        (
            "four-accumulated-regressions",
            ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./test/integration", "-run", "TestSessionToolResultConversationMissingContinuationIsBounded|TestSessionToolResultConversationCorruptAudioDeltaIsRejected|TestSessionCommand_FollowOnToolCallWaitsForResultBeforeClientClose|TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle", "-count=1", "-timeout=360s"],
            AGENT_CLI,
            360.0,
        ),
    ]
    records = []
    for label, argv, cwd, timeout in commands:
        record = run_command(label, argv, cwd, timeout)
        require(record["exit_code"] == 0 and not record["timed_out"] and record["reaped"], f"{label} failed: {record}")
        records.append(record)
    return records


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("retirement-and-owned-paths", "positive-and-negative-controls"), default="retirement-and-owned-paths")
    args = parser.parse_args()
    started = time.monotonic()
    try:
        report: dict[str, Any] = {"schema": "audio-runtime.c111.verification.v1", "mode": args.mode, "source": verify_source_and_scope(), "admission": verify_admission()}
        if args.mode == "positive-and-negative-controls":
            report["checks"] = focused_checks()
        report["status"] = "accepted"
        report["duration_seconds"] = round(time.monotonic() - started, 6)
        artifacts = EVIDENCE_DIR / "artifacts"
        artifacts.mkdir(parents=True, exist_ok=True)
        output = artifacts / f"verify-{int(time.time())}-{os.getpid()}.json"
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        report["artifact"] = str(output)
        print(json.dumps(report, sort_keys=True))
        return 0
    except (OSError, json.JSONDecodeError, subprocess.SubprocessError, VerificationFailure) as exc:
        print(json.dumps({"schema": "audio-runtime.c111.verification.v1", "mode": args.mode, "status": "failed", "error": str(exc)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
