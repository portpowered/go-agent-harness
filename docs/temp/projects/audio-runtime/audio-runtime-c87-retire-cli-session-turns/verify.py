#!/usr/bin/env python3
"""Verify C87 source ownership, retirement, and mutation controls."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys


HERE = Path(__file__).resolve().parent
ROOT = next(parent for parent in HERE.parents if (parent / "go.work").is_file())
BRANCH = "codex/audio-runtime-c87-retire-cli-session-turns"
BASE = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
ADAPTER = "agent-cli/internal/services/internal/agentruntime/session_turns.go"
ADAPTER_TEST = "agent-cli/internal/services/internal/agentruntime/session_turns_test.go"
RUNTIME = "go-agent-runtime/services/sessionturns"
EVIDENCE = "docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns"
SHARED_EXCLUSIONS = {
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
}


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git(*args: str, check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True, check=False, timeout=30)
    if check:
        require(result.returncode == 0, result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def changed_paths() -> set[str]:
    paths = set(git("diff", "--name-only", "origin/main...HEAD").splitlines())
    paths.update(git("diff", "--name-only").splitlines())
    paths.update(git("diff", "--cached", "--name-only").splitlines())
    paths.update(git("ls-files", "--others", "--exclude-standard").splitlines())
    return {path for path in paths if path}


def run_test(cwd: Path, package: str, pattern: str) -> None:
    environment = {**__import__("os").environ, "GOWORK": "off"}
    result = subprocess.run(
        ["go", "test", package, "-run", pattern, "-count=1"],
        cwd=cwd,
        env=environment,
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )
    require(result.returncode == 0, result.stdout + result.stderr)


def check_scope() -> None:
    paths = changed_paths()
    for path in paths:
        allowed = path == ADAPTER or path == ADAPTER_TEST or path.startswith(RUNTIME + "/") or path.startswith("coverage-manifest/go-agent-runtime/services/sessionturns/") or path.startswith(EVIDENCE + "/")
        require(allowed, f"unowned changed path: {path}")
    require(not (paths & SHARED_EXCLUSIONS), "shared registry or architecture baseline changed before its lease release")


def check_retirement() -> dict[str, object]:
    adapter_lines = len((ROOT / ADAPTER).read_text(encoding="utf-8").splitlines())
    baseline = git("show", f"{BASE}:{ADAPTER}")
    require(len(baseline.splitlines()) == 243, "planning-main session_turns.go is not the 243-line baseline")
    require(adapter_lines <= 45, f"CLI adapter has {adapter_lines} lines; maximum is 45")
    adapter = (ROOT / ADAPTER).read_text(encoding="utf-8")
    require("Deprecated:" in adapter and "wire.NewService" in adapter, "CLI file is not an explicit deprecated Wire delegation")
    require("sync" not in adapter and "messages" not in adapter, "CLI adapter retains state or protocol implementation")
    return {"baseline_lines": 243, "adapter_lines": adapter_lines}


def check_contract() -> dict[str, object]:
    public_files = [path for path in (ROOT / RUNTIME).glob("*.go") if not path.name.endswith("_test.go")]
    public_text = "\n".join(path.read_text(encoding="utf-8") for path in public_files)
    require("type Service interface" in public_text and "type SessionTurns struct" not in public_text, "public package exposes implementation state")
    require("internal/service" not in public_text, "public contract imports private implementation")
    wire = (ROOT / RUNTIME / "wire").read_text(encoding="utf-8") if (ROOT / RUNTIME / "wire").is_file() else ""
    wire_gen = ROOT / RUNTIME / "wire" / "wire_gen.go"
    require(wire_gen.is_file(), "dedicated generated Wire constructor is missing")
    require("wireinject" in (ROOT / RUNTIME / "wire" / "wire.go").read_text(encoding="utf-8"), "Wire injector source is missing its build tag")
    require("NewService" in wire_gen.read_text(encoding="utf-8") and "sessionturns.Service" in wire_gen.read_text(encoding="utf-8"), "Wire does not return the public service")
    return {"public_contract": True, "generated_wire": True}


def check_mutations() -> dict[str, object]:
    service = (ROOT / RUNTIME / "internal" / "service" / "service.go").read_text(encoding="utf-8")
    protocol = (ROOT / RUNTIME / "internal" / "service" / "protocol.go").read_text(encoding="utf-8")
    tests = (ROOT / RUNTIME / "internal" / "service" / "service_test.go").read_text(encoding="utf-8")
    require("tick <= active.StartTick" in service and "tick <= s.history" in service, "monotonic tick guards are missing")
    require("IsNonTerminal" in protocol and "StreamTypeMessageEnd" in protocol, "terminal response guard is missing")
    require("InvalidTransitionsPreserveState" in tests and "NonTerminalResponseIsSkipped" in tests and "TerminalResponsePreservesIdentity" in tests, "mutation regression tests are missing")
    run_test(ROOT / "go-agent-runtime", "./services/sessionturns/internal/service", "TestService(InvalidTransitionsPreserveState|NonTerminalResponseIsSkipped|TerminalResponsePreservesIdentity|ResponseCancellationClearsActive|DeepCopiesResponseBytes)")
    return {"monotonic_tick_guard": True, "terminal_boundary_guard": True, "regression_tests": True}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["mutations", "retirement-and-adapter", "owned-and-excluded-paths"], required=True)
    args = parser.parse_args()
    check_scope()
    checks: dict[str, object] = {}
    if args.mode in {"retirement-and-adapter", "owned-and-excluded-paths"}:
        checks["retirement"] = check_retirement()
    if args.mode == "retirement-and-adapter":
        checks["contract"] = check_contract()
        run_test(ROOT / "agent-cli", "./internal/services/internal/agentruntime", "TestDeprecatedSessionTurns")
        run_test(HERE / "external-consumer", "./...", "TestExternalConsumerUsesPublicSessionTurnsContract")
    if args.mode == "mutations":
        checks["mutations"] = check_mutations()
    print(json.dumps({"schema": "audio-runtime.c87.verification/v1", "mode": args.mode, "passed": True, "branch": git("branch", "--show-current"), "head": git("rev-parse", "HEAD"), "checks": checks}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
