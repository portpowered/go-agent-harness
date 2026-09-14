#!/usr/bin/env python3
"""Bounded, fail-closed evidence checks for the C66 extraction slice."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parent
REPO_ROOT = ROOT.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(REPO_ROOT))).resolve()
BASELINE = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
IMMUTABLE_BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
TASK = "audio-runtime-c66-retire-cli-dynamic-tool-publication"
BRANCH = "codex/audio-runtime-c66-retire-cli-dynamic-tool-publication"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_dynamic_tool_publisher.go"
CALLERS = (
    "agent-cli/internal/services/internal/agentruntime/session_live.go",
    "agent-cli/internal/services/internal/agentruntime/session_duration_loop.go",
)
OWNED_PREFIXES = (
    LEGACY,
    "agent-cli/internal/services/internal/agentruntime/session_dynamic_tool_publisher_test.go",
    "go-agent-runtime/services/toolpublication/",
    "coverage-manifest/go-agent-runtime/services/toolpublication/",
    "docs/temp/projects/audio-runtime/audio-runtime-c66-retire-cli-dynamic-tool-publication/",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def command(argv: list[str], cwd: Path = REPO_ROOT, timeout: float = 60.0) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            argv,
            cwd=cwd,
            env={**os.environ, "GOWORK": "off"} if argv and argv[0] == "go" else None,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as error:
        raise VerificationError(f"timed out after {timeout}s: {' '.join(argv)}") from error


def git(*args: str, timeout: float = 30.0) -> str:
    result = command(["git", *args], timeout=timeout)
    require(result.returncode == 0, f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def go_test(cwd: Path, race: bool = False, pattern: str = "./...") -> dict[str, object]:
    argv = ["go", "test"]
    if race:
        argv.append("-race")
    argv += ["-count=1", "-timeout=180s", pattern]
    result = command(argv, cwd=cwd, timeout=240.0)
    require(result.returncode == 0, f"focused Go test failed in {cwd}: {result.stdout}\n{result.stderr}")
    return {"cwd": str(cwd.relative_to(REPO_ROOT)), "race": race, "stdout": result.stdout.strip()}


def admission() -> dict[str, object]:
    control = FACTORY_ROOT / "factory/scripts/project-control.py"
    result = command(
        [
            sys.executable,
            str(control),
            "verify-work",
            "--type",
            "task",
            "--name",
            TASK,
            "--root",
            str(FACTORY_ROOT),
        ],
        timeout=30.0,
    )
    require(result.returncode == 0, f"task admission failed: {result.stdout}\n{result.stderr}")
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise VerificationError(f"task admission was not JSON: {result.stdout}") from error
    require(payload.get("status") == "admitted", f"unexpected task admission: {payload}")
    return payload


def ancestry() -> dict[str, object]:
    require(git("rev-parse", "--abbrev-ref", "HEAD") == BRANCH, "branch does not match prd.json")
    require(command(["git", "merge-base", "--is-ancestor", INTEGRATION, "HEAD"]).returncode == 0, "integration ancestor missing")
    require(command(["git", "merge-base", "--is-ancestor", BASELINE, "HEAD"]).returncode == 0, "planning baseline ancestor missing")
    execution_main = git("rev-parse", "origin/main")
    return {
        "branch": BRANCH,
        "head": git("rev-parse", "HEAD"),
        "integration_revision": INTEGRATION,
        "planning_main": BASELINE,
        "fetched_origin_main": execution_main,
    }


def baseline_inventory() -> dict[str, object]:
    raw = git("show", f"{BASELINE}:{LEGACY}")
    lines = len(raw.splitlines())
    require(lines == 593, f"baseline legacy line count={lines}, want 593")
    declarations = [
        "ErrSessionDynamicToolPublication",
        "sessionDynamicToolPublicationSettleWindow",
        "SessionDynamicToolPublicationLifecycle",
        "SessionDynamicToolPublicationState",
        "sessionDynamicToolPublicationEvent",
        "sessionDynamicToolPublisher",
        "sessionDynamicToolPublicationWallTimerFactory",
        "sessionDynamicToolPublicationWallTimer",
        "SessionDynamicToolPublicationError",
        "newSessionDynamicToolPublisher",
        "newSessionDynamicToolPublisherWithTimer",
        "startSessionDynamicToolPublisher",
		"stateSnapshot",
        "run",
        "consumeEvent",
        "refreshAndPublish",
        "commitSuccessfulPublication",
        "samePublicationTarget",
        "latestEventSequence",
        "setLifecycle",
        "fail",
        "Error",
        "ErrString",
        "Unwrap",
        "mergeSessionToolDefinitionBase",
        "sessionToolDefinitionDigest",
		"sessionToolDefinitionDigestEntry",
    ]
    missing = [name for name in declarations if name not in raw]
    require(not missing, f"baseline inventory omitted declarations: {missing}")
    for caller in CALLERS:
        result = command(["git", "diff", "--quiet", BASELINE, "--", caller])
        require(result.returncode == 0, f"caller changed from accepted baseline: {caller}")
    return {
        "baseline": BASELINE,
        "immutable_baseline": IMMUTABLE_BASELINE,
        "legacy_path": LEGACY,
        "physical_lines": lines,
        "declarations": declarations,
        "callers_byte_identical": True,
    }


def service_policy() -> dict[str, object]:
    service_root = REPO_ROOT / "go-agent-runtime/services/toolpublication"
    source_paths = sorted(service_root.rglob("*.go"))
    require(source_paths, "toolpublication service has no Go sources")
    forbidden_tokens = ("agent-cli", "internal/webmcp", "pkg/agentloop", "pflag", "cobra")
    violations: list[dict[str, str]] = []
    for path in source_paths:
        text = path.read_text(encoding="utf-8")
        for token in forbidden_tokens:
            if token in text:
                violations.append({"path": str(path.relative_to(REPO_ROOT)), "token": token})
    require(not violations, f"transport-specific service import/policy found: {violations}")
    tests = [
        go_test(REPO_ROOT / "go-agent-runtime", pattern="./services/toolpublication/..."),
        go_test(REPO_ROOT / "go-agent-runtime", race=True, pattern="./services/toolpublication/..."),
    ]
    return {"source_count": len(source_paths), "forbidden_tokens": violations, "tests": tests}


def adapter_retirement(baseline_lines: int) -> dict[str, object]:
    path = REPO_ROOT / LEGACY
    lines = len(path.read_text(encoding="utf-8").splitlines())
    require(lines < baseline_lines, f"adapter line count={lines}, baseline={baseline_lines}")
    text = path.read_text(encoding="utf-8")
    policy_tokens = ("sha256", "mergeDefinitions", "settleTimer", "pendingRefresh", "sync.Mutex")
    retained = [token for token in policy_tokens if token in text]
    require(not retained, f"adapter retains policy tokens: {retained}")
    for caller in CALLERS:
        result = command(["git", "diff", "--quiet", BASELINE, "--", caller])
        require(result.returncode == 0, f"caller changed from accepted baseline: {caller}")
    return {
        "baseline_lines": baseline_lines,
        "final_lines": lines,
        "net_retired_lines": baseline_lines - lines,
        "policy_tokens_in_adapter": retained,
        "callers_byte_identical": True,
    }


def cleanup_negative_control() -> dict[str, object]:
    process = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(30)"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    try:
        process.communicate(timeout=0.05)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=2.0)
    require(timed_out and process.poll() is not None, "cleanup negative control was not bounded and reaped")
    return {"timed_out_as_expected": timed_out, "reaped": process.poll() is not None}


def runner_controls() -> dict[str, object]:
    consumer = ROOT / "consumer"
    normal = go_test(consumer)
    race = go_test(consumer, race=True)
    return {"consumer_normal": normal, "consumer_race": race, "cleanup_negative": cleanup_negative_control()}


def final_scope() -> dict[str, object]:
    changed_result = command(["git", "diff", "--name-only", BASELINE])
    require(changed_result.returncode == 0, f"git diff scope failed: {changed_result.stderr.strip()}")
    changed = set(filter(None, changed_result.stdout.splitlines()))
    for arguments in (
        ["git", "ls-files", "--others", "--exclude-standard"],
        [
            "git",
            "ls-files",
            "--others",
            "--ignored",
            "--exclude-standard",
            "--",
            "docs/temp/projects/audio-runtime/audio-runtime-c66-retire-cli-dynamic-tool-publication/",
        ],
    ):
        result = command(arguments)
        require(result.returncode == 0, f"git untracked scope failed: {result.stderr.strip()}")
        changed.update(filter(None, result.stdout.splitlines()))
    changed = sorted(changed)
    outside = [path for path in changed if not any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES)]
    require(not outside, f"changed paths outside admitted ownership: {outside}")
    return {"head": git("rev-parse", "HEAD"), "changed_paths": changed, "outside_owned_paths": outside, "ownership_clean": True}


def run_mode(mode: str, baseline_lines: int) -> dict[str, object]:
    if mode == "baseline-inventory-oracles":
        return {"admission": admission(), "ancestry": ancestry(), "baseline": baseline_inventory()}
    if mode == "service-policy-mutations":
        return service_policy()
    if mode == "adapter-embedding-retirement":
        return adapter_retirement(baseline_lines)
    if mode == "runner-negative-cleanup-controls":
        return runner_controls()
    if mode == "final-scope-provenance":
        return {"ancestry": ancestry(), "scope": final_scope()}
    raise VerificationError(f"unknown mode: {mode}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        required=True,
        choices=(
            "baseline-inventory-oracles",
            "service-policy-mutations",
            "adapter-embedding-retirement",
            "runner-negative-cleanup-controls",
            "final-scope-provenance",
        ),
    )
    parser.add_argument("--baseline-lines", type=int, default=593)
    args = parser.parse_args()
    started = time.monotonic()
    try:
        report = run_mode(args.mode, args.baseline_lines)
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}, sort_keys=True), file=sys.stderr)
        return 1
    print(json.dumps({"status": "passed", "mode": args.mode, "elapsed_seconds": round(time.monotonic() - started, 6), "report": report}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
