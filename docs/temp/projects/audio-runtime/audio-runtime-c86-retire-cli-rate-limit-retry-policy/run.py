#!/usr/bin/env python3
"""Bounded C86 inventory, consumer, mutation, and shipped-regression runner."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any, Sequence


EVIDENCE = Path(__file__).resolve().parent
ROOT = next(parent for parent in EVIDENCE.parents if (parent / "go.work").is_file())
CONSUMER = EVIDENCE / "external-consumer"
RUNS = EVIDENCE / "runs"
LEGACY_REL = Path("agent-cli/internal/services/internal/agentruntime/session_rate_limit_retry.go")
LEGACY = ROOT / LEGACY_REL
BASELINE_REVISION = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BRANCH = "codex/audio-runtime-c86-retire-cli-rate-limit-retry-policy"
CHILD_TIMEOUT = 60.0
AGGREGATE_TIMEOUT = 600.0
OUTPUT_LIMIT = 1 << 20
FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
EXPECTED_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"


class EvidenceFailure(RuntimeError):
    pass


class Budget:
    def __init__(self, seconds: float = AGGREGATE_TIMEOUT, child_timeout: float = CHILD_TIMEOUT) -> None:
        require(seconds > 0, "aggregate timeout must be positive")
        require(child_timeout > 0, "child timeout must be positive")
        self.deadline = time.monotonic() + seconds
        self.child_timeout = child_timeout

    def remaining(self, label: str) -> float:
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise EvidenceFailure(f"aggregate deadline expired before {label}")
        return remaining


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def clean_environment() -> dict[str, str]:
    environment = dict(os.environ)
    for name in list(environment):
        upper = name.upper()
        if upper.endswith("_API_KEY") or "SECRET" in upper or "PASSWORD" in upper or "TOKEN" in upper:
            environment.pop(name, None)
    environment["GOWORK"] = "off"
    environment["LC_ALL"] = "C"
    environment["LANG"] = "C"
    return environment


def group_alive(process: subprocess.Popen[bytes]) -> bool:
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def stop_group(process: subprocess.Popen[bytes]) -> None:
    if group_alive(process):
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            raise EvidenceFailure(f"process group survived cleanup: {process.args}")


def run_child(label: str, command: Sequence[str | Path], cwd: Path, budget: Budget, expect: int | None = 0) -> dict[str, Any]:
    argv = [str(item) for item in command]
    started = time.monotonic()
    process = subprocess.Popen(argv, cwd=cwd, env=clean_environment(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    try:
        try:
            stdout, stderr = process.communicate(timeout=min(budget.child_timeout, budget.remaining(label)))
        except subprocess.TimeoutExpired:
            timed_out = True
            stop_group(process)
            stdout, stderr = process.communicate(timeout=2)
    finally:
        if process.poll() is None:
            stop_group(process)
    require(len(stdout) <= OUTPUT_LIMIT and len(stderr) <= OUTPUT_LIMIT, f"{label} exceeded the output cap")
    result = {
        "label": label,
        "command": argv,
        "cwd": str(cwd.relative_to(ROOT)) if cwd.is_relative_to(ROOT) else str(cwd),
        "returncode": process.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": stdout.decode(errors="replace"),
        "stderr": stderr.decode(errors="replace"),
        "stdout_sha256": sha256_bytes(stdout),
        "stderr_sha256": sha256_bytes(stderr),
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
        "cleanup": {"parent_reaped": process.returncode is not None, "group_alive_after": group_alive(process)},
    }
    if expect is not None:
        require(process.returncode == expect and not timed_out and result["cleanup"]["parent_reaped"] and not result["cleanup"]["group_alive_after"], f"{label} returned {process.returncode}, expected {expect}")
    return result


def git(*args: str) -> str:
    result = subprocess.run(["rtk", "proxy", "git", "-C", str(ROOT), *args], capture_output=True, text=True, check=False, timeout=20)
    require(result.returncode == 0, result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def git_show(revision: str, path: str) -> bytes:
    result = subprocess.run(["rtk", "proxy", "git", "-C", str(ROOT), "show", f"{revision}:{path}"], capture_output=True, check=False, timeout=20)
    require(result.returncode == 0, result.stderr.decode(errors="replace").strip() or f"git show failed for {revision}:{path}")
    return result.stdout


def write_report(name: str, report: dict[str, Any]) -> dict[str, Any]:
    RUNS.mkdir(parents=True, exist_ok=True)
    path = RUNS / f"{name}-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}.json"
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    report["report"] = str(path.relative_to(EVIDENCE))
    return report


def inventory() -> dict[str, Any]:
    baseline = git_show(BASELINE_REVISION, str(LEGACY_REL))
    current = LEGACY.read_bytes()
    prd = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    require(prd["branchName"] == BRANCH, "prd branchName does not match the isolated branch")
    require(git("branch", "--show-current") == BRANCH, "isolated branch identity changed")
    require(len(baseline.splitlines()) == 103, "legacy baseline is not exactly 103 physical lines")
    require(sha256_bytes(baseline) == "201be9dca620a3ae5a4559c851b17fcd62eb316068d180542a558a446c2ec317", "legacy baseline hash changed")
    require(git("merge-base", "--is-ancestor", STARTUP_REVISION, "HEAD") == "", "startup integration ancestry missing")
    require(git("merge-base", "--is-ancestor", BASELINE_REVISION, "HEAD") == "", "planning main ancestry missing")
    source = current.decode()
    symbols = {name: name in source for name in ("rateLimitRetryDecision", "parseRateLimitRetryDelay", "providerTerminalErrorCode", "providerTerminalErrorMessage", "legacyStatusDetailField", "normalizeTerminalStatus")}
    return {
        "status": "accepted",
        "branch": BRANCH,
        "source_revision": git("rev-parse", "HEAD"),
        "planning_origin_main": BASELINE_REVISION,
        "startup_integration_revision": STARTUP_REVISION,
        "legacy_baseline": {"path": str(LEGACY_REL), "lines": len(baseline.splitlines()), "sha256": sha256_bytes(baseline)},
        "candidate_legacy": {"lines": len(current.splitlines()), "sha256": sha256_bytes(current)},
        "symbols_in_baseline": {name: name in baseline.decode() for name in symbols},
        "symbols_in_candidate_adapter": symbols,
        "shared_paths_untouched": not any(path in git("diff", "--name-only") for path in ("scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json")),
    }


def build_consumer(temporary: Path, budget: Budget) -> Path:
    binary = temporary / "retrypolicy-consumer"
    run_child("consumer-build", ["rtk", "proxy", "go", "build", "-trimpath", "-o", binary, "."], CONSUMER, budget)
    require(binary.is_file(), "consumer binary was not produced")
    return binary


def consumer_mode(budget: Budget) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c86-consumer-") as temp:
        binary = build_consumer(Path(temp), budget)
        positive = run_child("consumer-positive", [binary], CONSUMER, budget)
        wrong = run_child("consumer-wrong-oracle", [binary, "wrong"], CONSUMER, budget, expect=1)
        require('"status":"ok"' in positive["stdout"], "consumer positive status missing")
        require("wrong expectation rejected as intended" in wrong["stderr"], "consumer negative assertion was not reached")
        return {"status": "accepted", "positive": positive, "wrong_oracle": wrong, "binary_sha256": sha256_file(binary)}


def mutation_controls(budget: Budget) -> dict[str, Any]:
    source_root = ROOT / "go-agent-runtime"
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c86-mutations-") as temp_name:
        temp = Path(temp_name)
        shutil.copytree(source_root / "services/retrypolicy", temp / "services/retrypolicy")
        (temp / "go.mod").write_text("module github.com/portpowered/go-agent-harness/go-agent-runtime\n\ngo 1.26.7\n", encoding="utf-8")
        service_path = temp / "services/retrypolicy/internal/service/service.go"
        clean = service_path.read_text(encoding="utf-8")
        cancellation_marker = " || terminal.TerminalReason == cancellationReason"
        require(clean.count(cancellation_marker) == 1, "cancellation mutation marker not unique")
        service_path.write_text(clean.replace(cancellation_marker, " || false"), encoding="utf-8")
        cancellation = run_child("mutation-accept-cancellation", ["rtk", "proxy", "go", "test", "./services/retrypolicy/internal/service", "-run", "TestDecideClassificationMatrix/cancellation_reason_is_excluded", "-count=1"], temp, budget, expect=1)
        service_path.write_text(clean, encoding="utf-8")
        cap_marker = "if seconds > maximumRetryDelay.Seconds() {"
        require(clean.count(cap_marker) == 1, "cap mutation marker not unique")
        service_path.write_text(clean.replace(cap_marker, "if false {"), encoding="utf-8")
        capped = run_child("mutation-bypass-cap", ["rtk", "proxy", "go", "test", "./services/retrypolicy/internal/service", "-run", "TestDecideDelayBoundaries/maximum_cap", "-count=1"], temp, budget, expect=1)
    return {"status": "accepted", "cancellation_mutation_failed": cancellation, "cap_mutation_failed": capped}


def retirement_and_scope() -> dict[str, Any]:
    adapter = LEGACY.read_text(encoding="utf-8")
    require(len(adapter.splitlines()) <= 33, f"legacy adapter has {len(adapter.splitlines())} lines, want at most 33")
    for forbidden in ("regexp", "strconv", "math", "strings", "rateLimitRetryDelayPattern", "maxLegacyStatusDetailBytes"):
        require(forbidden not in adapter, f"legacy adapter retains policy implementation marker {forbidden}")
    service = (ROOT / "go-agent-runtime/services/retrypolicy/internal/service/service.go").read_text(encoding="utf-8")
    for required in ("regexp", "strconv", "maximumRetryDelay", "maximumLegacyDetailLen", "legacyStatusDetailField"):
        require(required in service, f"private service is missing policy owner {required}")
    allowed = {
        "agent-cli/internal/services/internal/agentruntime/session_rate_limit_retry.go",
        "agent-cli/internal/services/internal/agentruntime/session_rate_limit_retry_test.go",
    }
    allowed_prefixes = ("go-agent-runtime/services/retrypolicy/", "coverage-manifest/go-agent-runtime/services/retrypolicy/", "docs/temp/projects/audio-runtime/audio-runtime-c86-retire-cli-rate-limit-retry-policy/")
    names = [line[2:].strip() for line in git("status", "--short", "--untracked-files=all").splitlines() if len(line) > 3 and line[2:].strip()]
    for name in names:
        require(name in allowed or name.startswith(allowed_prefixes), f"unowned path changed: {name}")
    for excluded in ("scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"):
        require(excluded not in git("diff", "--name-only"), f"shared path changed: {excluded}")
    return {"status": "accepted", "legacy_adapter_lines": len(adapter.splitlines()), "changed_paths": names, "shared_paths": "open"}


def shipped_replay(budget: Budget) -> dict[str, Any]:
    require(FIXTURE.is_file(), f"missing credential-free fixture: {FIXTURE}")
    require(sha256_file(FIXTURE) == FIXTURE_SHA256, "credential-free audio/tool fixture hash changed")
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c86-yui-") as temp_name:
        temp = Path(temp_name)
        yui = temp / "yui"
        run_child("yui-build", ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", yui, "./cmd/yui"], ROOT / "agent-cli", budget)
        help_result = run_child("yui-help", [yui, "--help"], temp, budget)
        require("session" in help_result["stdout"].lower(), "yui help did not expose the session workflow")
        case = temp / "audio-tool"
        case.mkdir()
        (case / "evidence/runs").mkdir(parents=True)
        record_dir = case / "record"
        audio_out = case / "audio.wav"
        replay = run_child("yui-audio-tool-replay", [yui, "session", "--replay", FIXTURE, "--audio-out", audio_out, "--record-dir", record_dir, "--trace-audio", "--workdir", case, "--allow-path", case], case, budget)
        pcm = record_dir / "audio/out-000.pcm"
        require(pcm.is_file() and sha256_file(pcm) == EXPECTED_PCM_SHA256 and pcm.stat().st_size == 4800, "credential-free audio/tool PCM oracle changed")
        require((record_dir / "manifest.json").is_file(), "credential-free audio/tool manifest missing")
        return {"status": "accepted", "help": help_result, "replay": replay, "pcm": {"bytes": pcm.stat().st_size, "sha256": sha256_file(pcm)}}


def help_process_smoke(budget: Budget) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c86-help-") as temp_name:
        temp = Path(temp_name)
        yui = temp / "yui"
        run_child("yui-build", ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", yui, "./cmd/yui"], ROOT / "agent-cli", budget)
        help_result = run_child("yui-help", [yui, "--help"], temp, budget)
        require("session" in help_result["stdout"].lower(), "yui help did not expose the session workflow")
        return {"status": "accepted", "help": help_result}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", nargs="?", choices=("inventory", "consumer", "mutations", "retirement-and-scope", "credential-free-audio-tool-replay", "all"))
    parser.add_argument("--case", choices=("credential-free-audio-tool-replay", "help-process-smoke"))
    parser.add_argument("--child-timeout", type=float, default=CHILD_TIMEOUT)
    parser.add_argument("--aggregate-timeout", type=float, default=AGGREGATE_TIMEOUT)
    args = parser.parse_args()
    if args.mode is not None and args.case is not None:
        parser.error("mode and --case cannot be used together")
    mode = args.case or args.mode
    if mode is None:
        parser.error("provide a mode or --case")
    budget = Budget(args.aggregate_timeout, args.child_timeout)
    try:
        if mode == "inventory":
            report = inventory()
        elif mode == "consumer":
            report = consumer_mode(budget)
        elif mode == "mutations":
            report = mutation_controls(budget)
        elif mode == "retirement-and-scope":
            report = retirement_and_scope()
        elif mode == "credential-free-audio-tool-replay":
            report = shipped_replay(budget)
        elif mode == "help-process-smoke":
            report = help_process_smoke(budget)
        else:
            report = {"status": "accepted", "inventory": inventory(), "consumer": consumer_mode(budget), "mutations": mutation_controls(budget), "retirement": retirement_and_scope(), "replay": shipped_replay(budget), "help": help_process_smoke(budget)}
        print(json.dumps(write_report(mode, report), indent=2, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "failed", "mode": mode, "error": str(error)}, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
