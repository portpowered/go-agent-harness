#!/usr/bin/env python3
"""Run bounded, source-pinned C110 contract, mutation, and retirement gates."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any, Iterable


ROOT = Path(__file__).resolve().parent
REPO_ROOT = next((parent for parent in (ROOT, *ROOT.parents) if (parent / "go.work").is_file()), None)
if REPO_ROOT is None:
    raise RuntimeError("C110 verifier could not locate the go.work repository root")
CONSUMER = ROOT / "consumer"
REPORTS = ROOT / "reports"
LEGACY_REL = Path("agent-cli/internal/services/internal/agentruntime/session_instructions.go")
LEGACY = REPO_ROOT / LEGACY_REL
NEW_PACKAGE = REPO_ROOT / "go-agent-runtime/services/sessioninstructions"
SERVICE = NEW_PACKAGE / "service.go"
BASELINE = ROOT / "baseline.json"
RUNNER = ROOT / "run.py"
MAX_OUTPUT_BYTES = 1 << 20
MUTATION_TIMEOUT_SECONDS = 120

ALLOWED_EXACT = {
    str(LEGACY_REL),
    "agent-cli/internal/services/internal/agentruntime/session_instructions_c110_test.go",
    "go-agent-runtime/services/session/instructions.go",
    "go-agent-runtime/services/session/internal/instructions/service.go",
    "go-agent-runtime/services/session/internal/instructions/service_test.go",
    "go-agent-runtime/services/session/wire/providers.go",
    "go-agent-runtime/services/session/wire/wire_gen.go",
}
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/sessioninstructions/",
    "coverage-manifest/go-agent-runtime/services/sessioninstructions/",
    "docs/temp/projects/audio-runtime/audio-runtime-c110-retire-cli-session-instruction-resolution/",
)
SHARED_LEASE_PATHS = (
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
)
CALLER_PATHS = (
    "agent-cli/internal/services/internal/agentruntime/service.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
    "agent-cli/internal/services/internal/agentruntime/session_image.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording.go",
)


class VerificationError(Exception):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def bounded(value: str, limit: int = MAX_OUTPUT_BYTES) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + "\n[output truncated by verifier]"


def run(argv: list[str], cwd: Path, *, env: dict[str, str] | None = None, timeout: float = 120) -> dict[str, Any]:
    child_env = os.environ.copy()
    child_env.update(env or {})
    child_env["GOWORK"] = "off"
    started = time.monotonic()
    try:
        completed = subprocess.run(
            [str(argument) for argument in argv],
            cwd=cwd,
            env=child_env,
            text=True,
            capture_output=True,
            timeout=timeout,
            check=False,
        )
        return {
            "argv": [str(argument) for argument in argv],
            "cwd": str(cwd),
            "exit_code": completed.returncode,
            "timed_out": False,
            "elapsed_seconds": round(time.monotonic() - started, 6),
            "stdout": bounded(completed.stdout),
            "stderr": bounded(completed.stderr),
        }
    except subprocess.TimeoutExpired as error:
        return {
            "argv": [str(argument) for argument in argv],
            "cwd": str(cwd),
            "exit_code": None,
            "timed_out": True,
            "elapsed_seconds": round(time.monotonic() - started, 6),
            "stdout": bounded((error.stdout or "") if isinstance(error.stdout, str) else ""),
            "stderr": bounded((error.stderr or "") if isinstance(error.stderr, str) else ""),
        }


def json_command(argv: list[str], cwd: Path) -> dict[str, Any]:
    result = run(argv, cwd)
    require(result["exit_code"] == 0 and not result["timed_out"], f"command failed: {result}")
    try:
        value = json.loads(result["stdout"])
    except json.JSONDecodeError as error:
        raise VerificationError(f"command did not emit JSON: {error}; result={result}") from error
    require(isinstance(value, dict), f"command emitted non-object JSON: {value!r}")
    return value


def git_output(args: list[str]) -> str:
    completed = subprocess.run(["git", *args], cwd=REPO_ROOT, text=True, capture_output=True, check=False)
    require(completed.returncode == 0, f"git command failed: {args}: {bounded(completed.stderr)}")
    return completed.stdout.strip()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git_file(revision: str, relative: str | Path) -> bytes:
    path = str(relative)
    completed = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=REPO_ROOT, capture_output=True, check=False)
    require(completed.returncode == 0, f"git show failed for {revision}:{path}: {completed.stderr.decode(errors='replace')}")
    return completed.stdout


def verify_imports() -> dict[str, Any]:
    result = run(["go", "list", "-json", "."], CONSUMER)
    require(result["exit_code"] == 0 and not result["timed_out"], f"consumer import inspection failed: {result}")
    package = json.loads(result["stdout"])
    imports = sorted(package.get("Imports", []))
    forbidden = [item for item in imports if "agent-cli" in item or "/internal/" in item]
    require(not forbidden, f"consumer has forbidden direct imports: {forbidden}")
    return {"package": package.get("ImportPath"), "imports": imports, "forbidden": forbidden}


def verify_legacy_boundary() -> dict[str, Any]:
    text = LEGACY.read_text(encoding="utf-8")
    lines = len(text.splitlines())
    require(lines <= 87, f"legacy file has {lines} lines, want <= 87")
    require("sessioninstructionswire.NewInstructionService" in text, "legacy file does not use dedicated instruction Wire")
    require("Tool-grounding requirements:" not in text, "legacy file retains policy text")
    require("os.ReadFile" not in text, "legacy file retains unbounded file loading")
    source = "\n".join(
        path.read_text(encoding="utf-8")
        for path in NEW_PACKAGE.rglob("*.go")
        if not path.name.endswith("_test.go")
    )
    forbidden = [
        token
        for token in (
            "agent-cli/",
            "flags.",
            "terminal",
            "os.Getenv",
            "os.LookupEnv",
            "credential",
            "globalFlags",
        )
        if token in source
    ]
    require(not forbidden, f"new package has forbidden host coupling: {forbidden}")
    return {"legacy_lines": lines, "forbidden_new_package_tokens": forbidden}


def verify_consumer() -> dict[str, Any]:
    report: dict[str, Any] = {"imports": verify_imports()}
    for mode in ("positive", "invalid", "print"):
        result = json_command(["go", "run", ".", "--mode", mode], CONSUMER)
        require(result.get("status") == "accepted", f"consumer {mode} status={result}")
        report[mode] = result
    return report


def copy_mutation_workspace(destination: Path) -> tuple[Path, Path]:
    ignored = shutil.ignore_patterns(".git", "bin", "coverage", "artifacts", "runs", "node_modules", ".cache")
    modules = ("go-agent-loop", "go-audio", "go-llm-gateway", "go-device-gateway", "go-agent-runtime")
    for module in modules:
        shutil.copytree(REPO_ROOT / module, destination / module, ignore=ignored)
    consumer_copy = destination / CONSUMER.relative_to(REPO_ROOT)
    consumer_copy.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(CONSUMER, consumer_copy, ignore=ignored)
    return destination, consumer_copy


def mutate_and_kill() -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="c110-guard-mutants-") as temporary:
        mutation_root, consumer_copy = copy_mutation_workspace(Path(temporary) / "repo")
        mutant_none = mutation_root / "go-agent-runtime/services/sessioninstructions/service.go"
        source = mutant_none.read_text(encoding="utf-8")
        needle = 'if value == "none" {'
        require(source.count(needle) == 1, "none guard mutant anchor is not unique")
        mutant_none.write_text(source.replace(needle, 'if false && value == "none" {', 1), encoding="utf-8")
        consumer_result = run(["go", "run", ".", "--mode", "positive"], consumer_copy, timeout=MUTATION_TIMEOUT_SECONDS)
        require(consumer_result["exit_code"] != 0 and not consumer_result["timed_out"], f"none mutant survived: {consumer_result}")

        mutant_cancel = mutation_root / "go-agent-runtime/services/sessioninstructions/service.go"
        source = mutant_cancel.read_text(encoding="utf-8")
        guard = (
            "\tsummary, summaryErr := skillsSummary(ctx, loader)\n"
            "\tif err := checkContext(ctx, PhaseSkillsSummary, workspaceDir); err != nil {\n"
            "\t\treturn \"\", err\n\t}\n"
        )
        mutated, count = source.replace(guard, "\tsummary, summaryErr := skillsSummary(ctx, loader)\n", 1), source.count(guard)
        require(count == 1, "post-skills cancellation guard anchor is not unique")
        mutant_cancel.write_text(mutated, encoding="utf-8")
        cancellation_result = run(
            ["go", "test", "./services/sessioninstructions/internal/service", "-run", "TestResolveChecksCancellationAfterLegacyLoaderCalls/skills$"],
            mutation_root / "go-agent-runtime",
            timeout=MUTATION_TIMEOUT_SECONDS,
        )
        require(cancellation_result["exit_code"] != 0 and not cancellation_result["timed_out"], f"cancellation mutant survived: {cancellation_result}")
        return {
            "status": "accepted",
            "none_literal_guard": {
                "mutant": 'if false && value == "none"',
                "killed": True,
                "result": consumer_result,
            },
            "post_legacy_skills_cancellation_guard": {
                "mutant": "removed post-skills checkContext guard",
                "killed": True,
                "result": cancellation_result,
            },
        }


def changed_paths() -> list[str]:
    tracked = git_output(["diff", "--name-only", "origin/main", "--"]).splitlines()
    untracked = git_output(["ls-files", "--others", "--exclude-standard"]).splitlines()
    return sorted(set(tracked) | set(untracked))


def path_allowed(path: str) -> bool:
    return path in ALLOWED_EXACT or any(path.startswith(prefix) for prefix in ALLOWED_PREFIXES)


def physical_go_lines(paths: Iterable[Path]) -> int:
    return sum(len(path.read_text(encoding="utf-8").splitlines()) for path in paths if path.is_file())


def current_runtime_production_lines() -> int:
    return physical_go_lines(
        path
        for path in (REPO_ROOT / "agent-cli/internal/services/internal/agentruntime").rglob("*.go")
        if not path.name.endswith("_test.go")
    )


def baseline_runtime_production_lines(revision: str) -> int:
    prefix = "agent-cli/internal/services/internal/agentruntime/"
    paths = git_output(["ls-tree", "-r", "--name-only", revision, "--", prefix]).splitlines()
    total = 0
    for path in paths:
        if path.endswith("_test.go"):
            continue
        total += len(git_file(revision, path).decode("utf-8").splitlines())
    return total


def source_hashes(paths: Iterable[str]) -> dict[str, str]:
    result: dict[str, str] = {}
    for relative in paths:
        path = REPO_ROOT / relative
        require(path.is_file(), f"required source evidence file is missing: {relative}")
        result[relative] = sha256_file(path)
    return result


def verify_ancestry(baseline: dict[str, Any]) -> dict[str, Any]:
    branch = git_output(["branch", "--show-current"])
    require(branch == baseline["branch"], f"branch {branch!r} does not match admitted branch {baseline['branch']!r}")
    origin_main = git_output(["rev-parse", "origin/main"])
    require(origin_main == baseline["origin_main"], f"origin/main changed from admitted revision: {origin_main}")
    head = git_output(["rev-parse", "HEAD"])
    revisions = {
        "startup_ancestor": baseline["startup_ancestor"],
        "accepted_main": baseline["accepted_main"],
        "origin_main": origin_main,
    }
    checks: dict[str, Any] = {}
    for label, revision in revisions.items():
        result = subprocess.run(["git", "merge-base", "--is-ancestor", revision, head], cwd=REPO_ROOT, check=False)
        checks[label] = {"revision": revision, "is_ancestor": result.returncode == 0}
        require(result.returncode == 0, f"required ancestry missing: {label}={revision}")
    return {"branch": branch, "head": head, "origin_main": origin_main, "required_ancestors": checks}


def verify_retirement() -> dict[str, Any]:
    baseline = json.loads(BASELINE.read_text(encoding="utf-8"))
    ancestry = verify_ancestry(baseline)
    baseline_legacy = git_file("origin/main", LEGACY_REL)
    require(len(baseline_legacy.decode("utf-8").splitlines()) == baseline["legacy_baseline_lines"], "legacy baseline line oracle changed")
    require(sha256_bytes(baseline_legacy) == baseline["legacy_baseline_sha256"], "legacy baseline hash oracle changed")
    current_legacy = LEGACY.read_bytes()
    current_lines = len(current_legacy.decode("utf-8").splitlines())
    require(current_lines <= 87, f"candidate legacy file has {current_lines} lines, want <= 87")
    baseline_total = baseline_runtime_production_lines("origin/main")
    current_total = current_runtime_production_lines()
    legacy_delta = baseline["legacy_baseline_lines"] - current_lines
    package_delta = baseline_total - current_total
    require(legacy_delta >= 190, f"legacy retirement is {legacy_delta} lines, want >= 190")
    require(package_delta >= 190, f"agent runtime production retirement is {package_delta} lines, want >= 190")

    paths = changed_paths()
    unexpected = [path for path in paths if not path_allowed(path)]
    require(not unexpected, f"changed paths outside C110 ownership: {unexpected}")
    shared_changes = [path for path in paths if path in SHARED_LEASE_PATHS]
    require(not shared_changes, f"C79-owned shared paths changed before guarded release: {shared_changes}")
    require(not (REPO_ROOT / "go-agent-runtime/services/session/internal/instructions/service.go").exists(), "old duplicate instruction service still exists")

    callers: list[dict[str, Any]] = []
    for relative in CALLER_PATHS:
        path = REPO_ROOT / relative
        require(path.is_file(), f"caller source missing: {relative}")
        text = path.read_text(encoding="utf-8")
        symbols = sorted(set(re.findall(r"RunSessionWithInstructions\w*", text)))
        require(symbols, f"no instruction wrapper caller found in {relative}")
        callers.append({"path": relative, "sha256": sha256_file(path), "symbols": symbols})
    compatibility = REPO_ROOT / "go-agent-runtime/services/session/wire/providers.go"
    compatibility_text = compatibility.read_text(encoding="utf-8")
    require("func NewInstructionService()" in compatibility_text, "session Wire compatibility constructor is missing")
    require("sessioninstructions.Factory{}.Build()" in compatibility_text, "session Wire compatibility constructor does not use the public stateless bridge")

    evidence_paths = [
        str(LEGACY_REL),
        "go-agent-runtime/services/sessioninstructions/contract.go",
        "go-agent-runtime/services/sessioninstructions/service.go",
        "go-agent-runtime/services/sessioninstructions/internal/service/service.go",
        "go-agent-runtime/services/sessioninstructions/wire/wire.go",
        "go-agent-runtime/services/sessioninstructions/wire/wire_gen.go",
        "go-agent-runtime/services/session/wire/providers.go",
        "go-agent-runtime/services/session/wire/wire_gen.go",
        str(ROOT.relative_to(REPO_ROOT) / "verify.py"),
        str(ROOT.relative_to(REPO_ROOT) / "run.py"),
    ]
    return {
        "ancestry": ancestry,
        "legacy": {
            "baseline_lines": baseline["legacy_baseline_lines"],
            "baseline_sha256": baseline["legacy_baseline_sha256"],
            "candidate_lines": current_lines,
            "candidate_sha256": sha256_bytes(current_legacy),
            "retired_lines": legacy_delta,
        },
        "agentruntime_production_lines": {
            "origin_main": baseline_total,
            "candidate": current_total,
            "retired_lines": package_delta,
        },
        "changed_paths": paths,
        "ownership": {
            "allowed_exact": sorted(ALLOWED_EXACT),
            "allowed_prefixes": list(ALLOWED_PREFIXES),
            "unexpected": unexpected,
            "caller_evidence": callers,
            "compatibility_api": {
                "path": str(compatibility.relative_to(REPO_ROOT)),
                "constructor": "NewInstructionService",
                "delegates_to": "sessioninstructions.Factory{}.Build",
            },
        },
        "c79_lease": {
            "work_id": "work-task-114",
            "review_id": "work-review-246",
            "state_at_admission_check": "in-review",
            "shared_paths": list(SHARED_LEASE_PATHS),
            "candidate_changes": shared_changes,
            "guard": "unchanged until C79 guarded release",
        },
        "source_hashes": source_hashes(evidence_paths),
    }


def verify(mode: str) -> dict[str, Any]:
    report: dict[str, Any] = {"mode": mode, "status": "accepted", "boundary": verify_legacy_boundary()}
    report["consumer"] = verify_consumer()
    if mode == "positive-and-guard-mutations":
        report["guard_mutations"] = mutate_and_kill()
    if mode == "retirement-and-owned-paths":
        report["retirement"] = verify_retirement()
    return report


def write_report(report: dict[str, Any]) -> None:
    REPORTS.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(report, indent=2, sort_keys=True) + "\n"
    (REPORTS / "c110-public-contract.json").write_text(payload, encoding="utf-8")
    (REPORTS / f"c110-{report['mode']}.json").write_text(payload, encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", default="positive-and-guard-mutations", choices=("positive", "positive-and-guard-mutations", "retirement-and-owned-paths"))
    parser.add_argument("--report", action="store_true")
    args = parser.parse_args()
    report = verify(args.mode)
    if args.report:
        write_report(report)
    print(json.dumps({"status": report["status"], "mode": report["mode"], "legacy_lines": report["boundary"]["legacy_lines"]}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, OSError, subprocess.TimeoutExpired, json.JSONDecodeError) as error:
        print(f"verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
