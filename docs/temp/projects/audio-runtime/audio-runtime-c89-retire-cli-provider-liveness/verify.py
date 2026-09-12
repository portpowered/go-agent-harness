#!/usr/bin/env python3
"""Run the bounded C89 retirement, scope, and fail-closed mutation oracles."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Callable


TASK_NAME = "audio-runtime-c89-retire-cli-provider-liveness"
LEGACY_PATH = Path("agent-cli/internal/services/internal/agentruntime/session_liveness.go")
BASELINE_PATH = LEGACY_PATH.as_posix()
OWNED_PREFIXES = (
    LEGACY_PATH.as_posix(),
    "agent-cli/internal/services/internal/agentruntime/session_liveness_test.go",
    "go-agent-runtime/services/providerliveness/",
    "coverage-manifest/go-agent-runtime/services/providerliveness/",
    f"docs/temp/projects/audio-runtime/{TASK_NAME}/",
)
SHARED_PATHS = (
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
)


def repository_root() -> Path:
    for parent in Path(__file__).resolve().parents:
        if (parent / "go.work").is_file():
            return parent
    raise RuntimeError("could not locate repository root")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(command: list[str], *, cwd: Path, timeout: int = 180) -> dict[str, object]:
    environment = os.environ.copy()
    environment["GOWORK"] = "off"
    completed = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout, env=environment)
    return {
        "command": command,
        "cwd": str(cwd),
        "exit_code": completed.returncode,
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


def write_evidence(task_dir: Path, name: str, report: dict[str, object]) -> None:
    evidence = task_dir / "evidence"
    evidence.mkdir(parents=True, exist_ok=True)
    (evidence / name).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def retirement_and_adapter(root: Path, task_dir: Path) -> dict[str, object]:
    current = root / LEGACY_PATH
    baseline_result = subprocess.run(
        ["git", "show", f"origin/main:{BASELINE_PATH}"],
        cwd=root,
        capture_output=True,
        text=True,
        check=True,
    )
    baseline = baseline_result.stdout
    source_files = sorted((root / "go-agent-runtime/services/providerliveness").rglob("*.go"))
    source_text = "\n".join(path.read_text(encoding="utf-8") for path in source_files)
    forbidden_legacy = (
        "livenessGeneration",
        "livenessTimer",
        "livenessWatcher",
        "latchLivenessFailure",
        "watchProviderProgress",
        "expireProviderProgress",
        "localToolDepth",
        "type SessionLivenessError struct",
    )
    forbidden_service_imports = (
        "agent-cli/",
        "go-agent-loop/pkg/messages",
        "go-audio/",
        "os.Getenv",
        "flag.",
        "func init(",
    )
    report = {
        "baseline_sha256": hashlib.sha256(baseline.encode()).hexdigest(),
        "baseline_lines": len(baseline.splitlines()),
        "candidate_sha256": sha256(current),
        "candidate_lines": len(current.read_text(encoding="utf-8").splitlines()),
        "retired_lines": len(baseline.splitlines()) - len(current.read_text(encoding="utf-8").splitlines()),
        "legacy_forbidden_symbols": [symbol for symbol in forbidden_legacy if symbol in current.read_text(encoding="utf-8")],
        "service_forbidden_imports_or_initializers": [symbol for symbol in forbidden_service_imports if symbol in source_text],
        "deprecated_alias_count": current.read_text(encoding="utf-8").count("// Deprecated:"),
    }
    report["passed"] = (
        report["baseline_lines"] == 569
        and report["candidate_lines"] <= 190
        and report["retired_lines"] >= 379
        and not report["legacy_forbidden_symbols"]
        and not report["service_forbidden_imports_or_initializers"]
        and report["deprecated_alias_count"] >= 3
    )
    write_evidence(task_dir, "retirement.json", report)
    return report


def changed_paths(root: Path) -> list[str]:
    result = subprocess.run(["git", "status", "--short"], cwd=root, capture_output=True, text=True, check=True)
    paths = []
    for line in result.stdout.splitlines():
        if not line:
            continue
        paths.append(line[3:] if len(line) >= 3 else line)
    return sorted(paths)


def owned_and_excluded_paths(root: Path, task_dir: Path) -> dict[str, object]:
    paths = changed_paths(root)
    unowned = [path for path in paths if not any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES)]
    shared = [path for path in paths if path in SHARED_PATHS]
    runtime_dir = root / "agent-cli/internal/services/internal/agentruntime"
    byte_drift = []
    for path in sorted(runtime_dir.glob("*.go")):
        relative = path.relative_to(root).as_posix()
        if relative in (LEGACY_PATH.as_posix(), "agent-cli/internal/services/internal/agentruntime/session_liveness_test.go"):
            continue
        comparison = subprocess.run(["git", "diff", "--quiet", "origin/main", "--", relative], cwd=root)
        if comparison.returncode != 0:
            byte_drift.append(relative)
    report = {
        "changed_paths": paths,
        "unowned_changed_paths": unowned,
        "shared_registry_changes": shared,
        "unowned_runtime_byte_drift": byte_drift,
        "passed": not unowned and not shared and not byte_drift,
    }
    write_evidence(task_dir, "scope.json", report)
    return report


def test_tree(root: Path, destination: Path, service_source: str) -> None:
    (destination / "services/providerliveness/internal/service").mkdir(parents=True)
    (destination / "services/providerliveness").mkdir(parents=True, exist_ok=True)
    (destination / "go.mod").write_text("module github.com/portpowered/go-agent-harness/go-agent-runtime\n\ngo 1.26.7\n", encoding="utf-8")
    for relative in (
        "go-agent-runtime/services/providerliveness/contract.go",
        "go-agent-runtime/services/providerliveness/bridge.go",
        "go-agent-runtime/services/providerliveness/internal/service/service_test.go",
    ):
        target = destination / relative.removeprefix("go-agent-runtime/")
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(root / relative, target)
    (destination / "services/providerliveness/internal/service/service.go").write_text(service_source, encoding="utf-8")


def mutation_case(root: Path, label: str, needle: str, replacement: str, test_name: str, failure_marker: str) -> dict[str, object]:
    source_path = root / "go-agent-runtime/services/providerliveness/internal/service/service.go"
    clean_source = source_path.read_text(encoding="utf-8")
    if needle not in clean_source:
        raise RuntimeError(f"mutation anchor missing for {label}")
    mutated_source = clean_source.replace(needle, replacement, 1)
    with tempfile.TemporaryDirectory(prefix=f"providerliveness-{label}-") as directory:
        temporary = Path(directory)
        test_tree(root, temporary, mutated_source)
        result = run(
            ["go", "test", "./services/providerliveness/internal/service", "-run", test_name, "-count=1", "-timeout=30s"],
            cwd=temporary,
            timeout=60,
        )
    combined = f"{result['stdout']}\n{result['stderr']}"
    result.update({
        "label": label,
        "clean_source_passed_before_mutation": True,
        "mutated_source_failed": result["exit_code"] != 0,
        "intended_assertion_observed": failure_marker in combined,
    })
    return result


def mutations(root: Path, task_dir: Path) -> dict[str, object]:
    clean_source = (root / "go-agent-runtime/services/providerliveness/internal/service/service.go").read_text(encoding="utf-8")
    with tempfile.TemporaryDirectory(prefix="providerliveness-clean-") as directory:
        temporary = Path(directory)
        test_tree(root, temporary, clean_source)
        clean = run(
            ["go", "test", "./services/providerliveness/internal/service", "-run", "TestService(EmptyResponseExclusions|ArmingResetAndStaleGeneration)$", "-count=1", "-timeout=30s"],
            cwd=temporary,
            timeout=60,
        )
    cases = [
        mutation_case(
            root,
            "cancelled-empty-response",
            "if s == nil || end.OutputPresent || end.ToolObligation || responseCancellationBoundary(end) {",
            "if s == nil || end.OutputPresent || end.ToolObligation {",
            "^TestServiceEmptyResponseExclusions/loop-cancel$",
            "excluded response published",
        ),
        mutation_case(
            root,
            "obsolete-generation",
            "if s.stopped || s.failure != nil || (requireGeneration && (!s.armed || s.generation != generation)) {",
            "if s.stopped || s.failure != nil || (requireGeneration && !s.armed) {",
            "^TestServiceArmingResetAndStaleGeneration$",
            "obsolete timer generation published",
        ),
    ]
    report = {"clean": clean, "mutations": cases}
    report["passed"] = (
        clean["exit_code"] == 0
        and all(case["mutated_source_failed"] and case["intended_assertion_observed"] for case in cases)
    )
    write_evidence(task_dir, "mutations.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("retirement-and-adapter", "mutations", "owned-and-excluded-paths"), required=True)
    args = parser.parse_args()
    root = repository_root()
    task_dir = Path(__file__).resolve().parent
    handlers: dict[str, Callable[[Path, Path], dict[str, object]]] = {
        "retirement-and-adapter": retirement_and_adapter,
        "mutations": mutations,
        "owned-and-excluded-paths": owned_and_excluded_paths,
    }
    report = handlers[args.mode](root, task_dir)
    print(json.dumps({"mode": args.mode, "passed": report["passed"]}, sort_keys=True))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
