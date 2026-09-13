#!/usr/bin/env python3
"""Verify the public consumer and three causal session-observation mutants."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip())
CONSUMER_ROOT = ROOT / "external-consumer"
SERVICE_SOURCE = REPO_ROOT / "go-agent-runtime/services/sessionobservation/internal/service/service.go"
REPORT_PATH = ROOT / "evidence/mutation-results.json"
RETIREMENT_SCOPE_BASE = "a8b52cfa63f27a51183fed9abe344ec4fb076965"
LEGACY_FILES = {
    "agent-cli/internal/services/agentruntime/observations.go": 93,
    "agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go": 294,
}
LEGACY_LINE_LIMIT = 127
ALLOWED_EXACT_PATHS = {
    "agent-cli/internal/services/agentruntime/observations.go",
    "agent-cli/internal/services/agentruntime/observations_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go",
    "agent-cli/internal/services/internal/agentruntime/session_runtime_observation_test.go",
}
ALLOWED_PATH_PREFIXES = (
    "go-agent-runtime/services/sessionobservation/",
    "coverage-manifest/go-agent-runtime/services/sessionobservation/",
    "docs/temp/projects/audio-runtime/audio-runtime-c97-retire-cli-session-observation/",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def run(command: list[str], cwd: Path, env: dict[str, str], overlay: Path | None = None) -> subprocess.CompletedProcess[str]:
    full = list(command)
    if overlay is not None:
        full[2:2] = ["-overlay", str(overlay)]
    return subprocess.run(full, cwd=cwd, env=env, capture_output=True, text=True, timeout=120)


def test_environment() -> dict[str, str]:
    environment = dict(os.environ)
    environment["GOWORK"] = "off"
    return environment


def test_command(overlay: Path | None = None) -> subprocess.CompletedProcess[str]:
    return run(["go", "test", "./...", "-count=1", "-timeout=120s"], CONSUMER_ROOT, test_environment(), overlay)


def output(result: subprocess.CompletedProcess[str]) -> str:
    return result.stdout + result.stderr


def failing_tests(text: str) -> list[str]:
    return re.findall(r"--- FAIL: (Test[A-Za-z0-9_]+)", text)


def verify_public_graph() -> dict:
    result = run(["go", "list", "-deps", "./..."], CONSUMER_ROOT, test_environment())
    require(result.returncode == 0, f"GOWORK=off dependency listing failed:\n{output(result)}")
    dependencies = result.stdout.splitlines()
    require(not any("agent-cli" in dependency for dependency in dependencies), "public consumer dependency graph imports agent-cli")
    return {"dependency_count": len(dependencies), "cli_imported": False}


def git_changed_paths(base: str) -> list[str]:
    result = subprocess.run(
        ["git", "diff", "--name-only", f"{base}..HEAD"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return [path for path in result.stdout.splitlines() if path]


def verify_retirement_and_scope() -> dict:
    counts = {}
    for relative_path, baseline in LEGACY_FILES.items():
        path = REPO_ROOT / relative_path
        require(path.is_file(), f"retired legacy file is missing: {relative_path}")
        source = path.read_text()
        line_count = len(source.splitlines())
        counts[relative_path] = {"baseline": baseline, "current": line_count}
        require(line_count < baseline, f"legacy file did not shrink: {relative_path}")
        require("sessionobservation" in source, f"legacy file is not routed to the public service: {relative_path}")
        for marker in ("context", "encoding/json", "sync", "sync/atomic", "json.Marshal", "terminalOnce", "inputCommits", "inputMu", "retainCommitPayload"):
            require(marker not in source, f"legacy adapter retains implementation policy {marker!r}: {relative_path}")

    current_lines = sum(item["current"] for item in counts.values())
    baseline_lines = sum(item["baseline"] for item in counts.values())
    retired_lines = baseline_lines - current_lines
    require(current_lines <= LEGACY_LINE_LIMIT, f"legacy production lines = {current_lines}, want <= {LEGACY_LINE_LIMIT}")
    require(retired_lines >= 260, f"retired production lines = {retired_lines}, want >= 260")

    changed_paths = git_changed_paths(RETIREMENT_SCOPE_BASE)
    disallowed_paths = [
        path
        for path in changed_paths
        if path not in ALLOWED_EXACT_PATHS and not any(path.startswith(prefix) for prefix in ALLOWED_PATH_PREFIXES)
    ]
    require(not disallowed_paths, f"candidate changed paths outside C97 lease: {disallowed_paths}")
    require("go-agent-runtime/services/sessionobservation/wire/wire_gen.go" in changed_paths, "dedicated sessionobservation Wire path is missing")
    require("sessionobservationwire.NewService" in (REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go").read_text(), "legacy adapter does not use dedicated Wire construction")

    return {
        "schema": "audio-runtime.c97.sessionobservation.retirement.v1",
        "candidate_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout.strip(),
        "scope_base": RETIREMENT_SCOPE_BASE,
        "changed_paths": changed_paths,
        "legacy_files": counts,
        "baseline_lines": baseline_lines,
        "current_lines": current_lines,
        "retired_lines": retired_lines,
        "passed": True,
    }


def make_overlay(temp: Path, mutation: str) -> Path:
    source = SERVICE_SOURCE.read_text()
    if mutation == "reused-commit-payload":
        old = "s.inputPayload = append(s.inputPayload, payload...)"
        new = "s.inputPayload = payload"
    elif mutation == "duplicate-terminal":
        old = "\ts.terminalOnce.Do(func() {\n\t\ts.ObserveFinal(sessionobservation.SessionRuntimeObservationTerminal, nil, turns, 0, runErr == nil, runErr, accounting)\n\t})"
        new = "\ts.ObserveFinal(sessionobservation.SessionRuntimeObservationTerminal, nil, turns, 0, runErr == nil, runErr, accounting)"
    elif mutation == "rejected-playback":
        old = "s.Observe(sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt, payload, 0, false, receipt.Err)"
        new = "s.Observe(sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt, payload, 0, true, receipt.Err)"
    else:
        raise VerificationError(f"unknown mutation {mutation}")
    require(source.count(old) == 1, f"mutation anchor is not unique for {mutation}")
    mutant = temp / f"{mutation}.go"
    mutant.write_text(source.replace(old, new, 1))
    overlay = temp / f"{mutation}.overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(SERVICE_SOURCE): str(mutant)}}))
    return overlay


def verify_mutations() -> dict:
    expected = {
        "reused-commit-payload": "TestCommitPayloadIsCopiedBeforeCommit",
        "duplicate-terminal": "TestTerminalPublishesExactlyOnce",
        "rejected-playback": "TestRejectedPlaybackIsNotClean",
    }
    results = {}
    with tempfile.TemporaryDirectory(prefix="c97-session-observation-mutants-") as directory:
        temp = Path(directory)
        for mutation, intended_test in expected.items():
            result = test_command(make_overlay(temp, mutation))
            text = output(result)
            failed = failing_tests(text)
            require(result.returncode != 0, f"causal mutant unexpectedly passed: {mutation}")
            require(failed == [intended_test], f"{mutation} failed tests {failed}, expected only {intended_test}\n{text}")
            results[mutation] = {"returncode": result.returncode, "failed_test": intended_test, "output": text[-4000:]}
    return results


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["positive-and-three-mutations", "retirement-and-owned-paths"], required=True)
    args = parser.parse_args()
    if args.mode == "retirement-and-owned-paths":
        report = verify_retirement_and_scope()
        print(json.dumps(report, sort_keys=True))
        return 0

    graph = verify_public_graph()
    positive = test_command()
    require(positive.returncode == 0, f"positive public consumer failed:\n{output(positive)}")
    mutations = verify_mutations()
    report = {
        "schema": "audio-runtime.c97.sessionobservation.mutation.v1",
        "candidate_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout.strip(),
        "gowork": "off",
        "positive": {"returncode": positive.returncode},
        "public_graph": graph,
        "mutations": mutations,
        "passed": True,
    }
    REPORT_PATH.parent.mkdir(parents=True, exist_ok=True)
    REPORT_PATH.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"passed": True, "schema": report["schema"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print(f"verification failure: {error}")
        raise SystemExit(1)
