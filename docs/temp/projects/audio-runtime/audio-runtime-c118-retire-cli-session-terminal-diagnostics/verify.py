#!/usr/bin/env python3
"""C118 positive and causal checks for the terminal-policy retirement."""

from __future__ import annotations

import argparse
import io
import json
import subprocess
import tarfile
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[5]
BASELINE = "3963bc3566da24f8214634c17a9d0f79a6724171"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go"
ADAPTER = "agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal_adapter.go"
SERVICE = "go-agent-runtime/services/sessionterminal/internal/service/service.go"
CLI_PACKAGE = "agent-cli/internal/services/internal/agentruntime"
EXPECTED_SOURCE_LINES = 287
EXPECTED_SOURCE_SHA = "4608c5c35277c97375c89e989e8a9c88dc6376968239ee66d713a0ee3f65b369"


def command(args: list[str], cwd: Path = ROOT, timeout: int = 120) -> tuple[int, str]:
    completed = subprocess.run(
        args,
        cwd=cwd,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=timeout,
        check=False,
    )
    return completed.returncode, completed.stdout


def tail(output: str) -> str:
    return output[-2000:].strip()


def positive_controls() -> list[dict[str, object]]:
    commands = [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessionterminal/...",
            "-run",
            "TestFinalize|TestCancellationOutputState|TestNewService",
            "-count=1",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic|TestSessionProgressObserver_IgnoresNonTerminalProviderDiagnostic|TestSessionCancellation_RecordsTerminalDiagnosticOnce",
            "-count=1",
        ],
    ]
    results = []
    for args in commands:
        code, output = command(args, timeout=360)
        results.append({"command": " ".join(args), "exit": code, "output": tail(output)})
        if code != 0:
            raise SystemExit(json.dumps({"status": "positive-control-failed", "results": results}))
    return results


def archived_candidate() -> tuple[tempfile.TemporaryDirectory[str], Path]:
    archive = subprocess.check_output(["git", "archive", "HEAD"], cwd=ROOT)
    temporary = tempfile.TemporaryDirectory(prefix="c118-mutant-")
    candidate = Path(temporary.name)
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as tar:
        tar.extractall(candidate, filter="data")
    return temporary, candidate


def run_mutant(name: str, mutate) -> dict[str, object]:
    temporary, candidate = archived_candidate()
    try:
        source = candidate / SERVICE
        original = source.read_text()
        mutated = mutate(original)
        source.write_text(mutated)
        code, output = command(
            ["go", "test", "./go-agent-runtime/services/sessionterminal/...", "-run", "TestFinalizeHintsAndDefaults|TestCancellationOutputStateUsesObservedOutputOnly", "-count=1"],
            cwd=candidate,
            timeout=180,
        )
        return {"mutant": name, "exit": code, "rejected": code != 0, "output": tail(output)}
    finally:
        temporary.cleanup()


def causal_mutations() -> list[dict[str, object]]:
    def drop_failure(source: str) -> str:
        needle = "result.Records = append(result.Records, failureRecord(request, failure))"
        if source.count(needle) != 1:
            raise SystemExit("failure mutation target was not unique")
        return source.replace(needle, "result.Records = append(result.Records, metricsRecord(request))", 1)

    def erase_partial_output(source: str) -> str:
        start = source.index("func (*Service) CancellationOutputState")
        end = source.index("\n}\n\nfunc normalizeRequest", start) + 2
        block = source[start:end]
        if block.count("return messages.TerminalOutputPartial") != 1:
            raise SystemExit("output-state mutation target was not unique")
        block = block.replace("return messages.TerminalOutputPartial", "return messages.TerminalOutputNone", 1)
        return source[:start] + block + source[end:]

    return [
        run_mutant("drop-final-failure-record", drop_failure),
        run_mutant("erase-cancellation-partial-output", erase_partial_output),
    ]


def retirement_check() -> dict[str, object]:
    code, source = command(["git", "show", f"{BASELINE}:{LEGACY}"])
    if code != 0:
        raise SystemExit("immutable legacy source could not be read")
    import hashlib

    source_bytes = source.encode()
    current_adapter = (ROOT / ADAPTER).read_text()
    package_files = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", CLI_PACKAGE], cwd=ROOT, text=True).splitlines()
    baseline_cli_lines = sum(
        len(subprocess.check_output(["git", "show", f"{BASELINE}:{path}"], cwd=ROOT, text=True).splitlines())
        for path in package_files
        if path.endswith(".go") and not path.endswith("_test.go")
    )
    candidate_cli_lines = sum(
        len(path.read_text().splitlines())
        for path in (ROOT / CLI_PACKAGE).glob("*.go")
        if not path.name.endswith("_test.go")
    )
    checks = {
        "legacy_absent": not (ROOT / LEGACY).exists(),
        "baseline_line_count": len(source.splitlines()),
        "baseline_sha256": hashlib.sha256(source_bytes).hexdigest(),
        "adapter_delegates": "services/sessionterminal" in current_adapter and "terminalwire.NewService" in current_adapter,
        "adapter_has_no_legacy_policy_import": "go-llm-gateway/pkg/providers" not in current_adapter,
        "public_contract_present": (ROOT / "go-agent-runtime/services/sessionterminal/service.go").exists(),
        "wire_present": (ROOT / "go-agent-runtime/services/sessionterminal/wire/wire_gen.go").exists(),
    }
    checks["baseline_pinned"] = checks["baseline_line_count"] == EXPECTED_SOURCE_LINES and checks["baseline_sha256"] == EXPECTED_SOURCE_SHA
    checks["baseline_cli_production_lines"] = baseline_cli_lines
    checks["candidate_cli_production_lines"] = candidate_cli_lines
    checks["retirement_line_reduction"] = baseline_cli_lines - candidate_cli_lines
    checks["retirement_floor_met"] = checks["retirement_line_reduction"] >= 180
    if not all(value is True for value in checks.values() if isinstance(value, bool)):
        raise SystemExit(json.dumps({"status": "retirement-check-failed", "checks": checks}))
    if not checks["retirement_floor_met"]:
        raise SystemExit(json.dumps({"status": "retirement-floor-failed", "checks": checks}))
    return checks


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["positive-and-causal-mutations", "retirement-callers-and-owned-paths"], required=True)
    args = parser.parse_args()
    if args.mode == "positive-and-causal-mutations":
        result = {"mode": args.mode, "positive": positive_controls(), "mutations": causal_mutations()}
        if not all(item["rejected"] for item in result["mutations"]):
            raise SystemExit(json.dumps({"status": "causal-mutant-escaped", **result}))
    else:
        result = {"mode": args.mode, "retirement": retirement_check()}
    print(json.dumps({"status": "pass", **result}, sort_keys=True))


if __name__ == "__main__":
    main()
