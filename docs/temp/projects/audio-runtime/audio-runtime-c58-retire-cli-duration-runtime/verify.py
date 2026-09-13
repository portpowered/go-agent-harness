#!/usr/bin/env python3
"""Independent, source-based checks for the C58 duration-runtime slice.

The checks intentionally use the admitted baseline and the public consumer
module. They do not import candidate implementation packages or trust a
candidate-generated golden file for any oracle.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[5]
TARGET = Path(__file__).resolve().parent
BASELINE = "904e1f4c3be6c1e629138632573bd2fb55d50938"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
IMMUTABLE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
CLI_ROOT = Path("agent-cli/internal/services/internal/agentruntime")
CLI_FILES = [
    CLI_ROOT / "session_duration.go",
    CLI_ROOT / "session_duration_admission.go",
    CLI_ROOT / "session_duration_artifacts.go",
    CLI_ROOT / "session_duration_loop.go",
]
READ_ONLY = [
    CLI_ROOT / "session_audio_out.go",
    CLI_ROOT / "session_recording.go",
    CLI_ROOT / "session_instructions.go",
    CLI_ROOT / "service.go",
    CLI_ROOT / "session_runtime_plan.go",
    CLI_ROOT / "session_duration_terminal.go",
]
RUNTIME = Path("go-agent-runtime/services/duration")


class CheckError(RuntimeError):
    pass


def run(cmd: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None, timeout: int = 180) -> subprocess.CompletedProcess[str]:
    merged = os.environ.copy()
    if env:
        merged.update(env)
    return subprocess.run(cmd, cwd=cwd, env=merged, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise CheckError(message)


def lines(path: Path) -> int:
    return len(path.read_text(encoding="utf-8").splitlines())


def baseline_text(path: Path) -> str:
    result = run(["git", "show", f"{BASELINE}:{path.as_posix()}"], timeout=30)
    require(result.returncode == 0, f"cannot read admitted baseline {path}: {result.stdout}")
    return result.stdout


def baseline_lines(path: Path) -> int:
    return len(baseline_text(path).splitlines())


def git_names(*args: str) -> list[str]:
    result = run(["git", *args], timeout=30)
    require(result.returncode == 0, f"git {' '.join(args)} failed: {result.stdout}")
    return [line for line in result.stdout.splitlines() if line]


def consumer_test(mutation: str | None = None) -> subprocess.CompletedProcess[str]:
    env = {"GOWORK": "off"}
    if mutation:
        env["C58_MUTATION"] = mutation
    return run(["go", "test", "-count=1", "-timeout=120s", "./..."], cwd=TARGET / "consumer", env=env, timeout=150)


def check_baseline_callers_oracles() -> None:
    expected = [255, 405, 445, 534]
    got = [baseline_lines(path) for path in CLI_FILES]
    require(got == expected and sum(got) == 1639, f"baseline census {got} != {expected} / 1639")
    for path in READ_ONLY:
        require(path.exists(), f"read-only caller seam is missing: {path}")
    duration_tests = list((ROOT / CLI_ROOT).glob("session_duration*_test.go"))
    require(duration_tests, "duration regression tests were not discovered")
    oracle_text = "\n".join(path.read_text(encoding="utf-8") for path in duration_tests)
    for marker in ("context.Canceled", "provider", "artifact", "SessionClose", "max duration"):
        require(marker.lower() in oracle_text.lower(), f"independent duration oracle lacks {marker}")
    require((TARGET / "consumer" / "consumer_test.go").exists(), "consumer oracle is missing")
    print(json.dumps({"baseline_files": 4, "baseline_lines": got, "read_only_callers": len(READ_ONLY), "oracles": "present"}, sort_keys=True))


def check_lifecycle_mutations() -> None:
    for path in RUNTIME.rglob("*.go"):
        text = path.read_text(encoding="utf-8")
        require("agent-cli" not in text and "internal/services/internal/agentruntime" not in text, f"runtime imports CLI: {path}")
    authority = "\n".join(
        path.read_text(encoding="utf-8")
        for path in (RUNTIME / "internal/lifecycle").glob("*.go")
    )
    authority += "\n" + (RUNTIME / "contract.go").read_text(encoding="utf-8")
    for marker in ("InvalidDurationError", "timerReady", "drain", "ProviderTerminal", "errors.Join"):
        require(marker in authority, f"lifecycle authority marker missing: {marker}")
    normal = consumer_test()
    require(normal.returncode == 0, f"public consumer failed: {normal.stdout}")
    for mutation in ("terminal", "drain"):
        result = consumer_test(mutation)
        require(result.returncode != 0, f"{mutation} mutation unexpectedly passed:\n{result.stdout}")
    print(json.dumps({"runtime_import_boundary": "clean", "consumer": "pass", "mutation_oracles": ["terminal", "drain"]}, sort_keys=True))


def check_artifact_mutations() -> None:
    artifact = "\n".join(
        (
            (RUNTIME / "contract.go").read_text(encoding="utf-8"),
            (RUNTIME / "internal/artifacts/artifacts.go").read_text(encoding="utf-8"),
        )
    )
    for marker in ("DecodePCM16WithLimit", "Flush", "Close", "RecordingTerminalSummary"):
        require(marker in artifact, f"artifact contract marker missing: {marker}")
    normal = consumer_test()
    require(normal.returncode == 0, f"artifact consumer failed: {normal.stdout}")
    for mutation in ("artifacts", "flush"):
        result = consumer_test(mutation)
        require(result.returncode != 0, f"{mutation} mutation unexpectedly passed:\n{result.stdout}")
    print(json.dumps({"artifact_contract": "pass", "failure_identity": "pass", "mutation_oracles": ["artifacts", "flush"]}, sort_keys=True))


def check_cli_retirement(args: argparse.Namespace) -> None:
    baseline_files = [path for path in CLI_FILES if baseline_text(path)]
    current = [path for path in CLI_FILES if path.exists()]
    current_lines = sum(lines(path) for path in current)
    require(len(baseline_files) == args.baseline_files, f"baseline file count {len(baseline_files)} != {args.baseline_files}")
    require(sum(baseline_lines(path) for path in baseline_files) == args.baseline_lines, "baseline line count changed")
    require(len(current) <= args.max_files, f"retained CLI production files={len(current)}")
    require(current_lines <= args.max_lines, f"retained CLI production lines={current_lines}")
    require(args.baseline_files - len(current) >= args.min_file_reduction, "file retirement floor missed")
    require(args.baseline_lines - current_lines >= args.min_line_reduction, "line retirement floor missed")
    changed_cli = [name for name in git_names("diff", "--name-only", BASELINE) if name.startswith("agent-cli/")]
    allowed = {path.as_posix() for path in CLI_FILES} | {str(CLI_ROOT / "session_duration_admission_compat_test.go")}
    require(set(changed_cli) <= allowed, f"unrelated CLI production growth/change: {sorted(set(changed_cli) - allowed)}")
    print(json.dumps({"baseline": [args.baseline_files, args.baseline_lines], "current": [len(current), current_lines], "retired": [args.baseline_files - len(current), args.baseline_lines - current_lines]}, sort_keys=True))


def check_wire_embedding_scope() -> None:
    providers = (RUNTIME / "wire/providers.go").read_text(encoding="utf-8")
    generated = (RUNTIME / "wire/wire_gen.go").read_text(encoding="utf-8")
    require("//go:generate" in providers and "wire.Build" in providers, "Wire provider is not reproducible")
    require(generated.startswith("// Code generated by Wire. DO NOT EDIT."), "Wire output lacks generated header")
    consumer_go = (TARGET / "consumer/consumer_test.go").read_text(encoding="utf-8")
    require("services/duration/wire" in consumer_go and "services/duration/internal" not in consumer_go and "agent-cli" not in consumer_go, "consumer crosses private/CLI boundary")
    result = run(["go", "list", "-deps", "./..."], cwd=TARGET / "consumer", env={"GOWORK": "off"}, timeout=120)
    require(result.returncode == 0, f"consumer dependency listing failed: {result.stdout}")
    require("/agent-cli" not in result.stdout, "consumer dependency graph imports CLI")
    print(json.dumps({"wire": "generated", "consumer": "GOWORK=off public-only", "cli_dependency": "absent"}, sort_keys=True))


def check_formatting() -> None:
    paths = [
        *[str(path) for path in RUNTIME.rglob("*.go")],
        str(CLI_ROOT / "session_duration.go"),
        str(CLI_ROOT / "session_duration_loop.go"),
        str(CLI_ROOT / "session_duration_admission_compat_test.go"),
        str(TARGET / "consumer/consumer_test.go"),
    ]
    result = run(["gofmt", "-d", *paths], timeout=60)
    require(result.returncode == 0 and not result.stdout, f"gofmt drift:\n{result.stdout}")
    result = run(["git", "diff", "--check"], timeout=60)
    require(result.returncode == 0, f"whitespace errors:\n{result.stdout}")
    print(json.dumps({"gofmt": "clean", "diff_check": "clean"}, sort_keys=True))


def check_final_scope() -> None:
    branch = run(["git", "branch", "--show-current"], timeout=30).stdout.strip()
    manifest = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    require(branch == manifest["branchName"], f"branch {branch!r} does not match manifest")
    for revision in (IMMUTABLE, STARTUP, BASELINE):
        result = run(["git", "merge-base", "--is-ancestor", revision, "HEAD"], timeout=30)
        require(result.returncode == 0, f"required ancestry missing: {revision}")
    result = run(["git", "diff", "--exit-code", BASELINE, "--", *[str(path) for path in READ_ONLY]], timeout=30)
    require(result.returncode == 0, "read-only integration files changed")
    for path in RUNTIME.rglob("*.go"):
        text = path.read_text(encoding="utf-8")
        require("agent-cli" not in text and "internal/services/internal/agentruntime" not in text, f"runtime boundary drift: {path}")
    status = run(["git", "status", "--porcelain=v1", "-uall"], timeout=30).stdout.splitlines()
    allowed_prefixes = (
        "agent-cli/internal/services/internal/agentruntime/session_duration",
        "go-agent-runtime/services/duration/",
        "scripts/wire-packages.txt",
        "coverage-manifest/go-agent-runtime/services/duration/",
        "docs/temp/projects/audio-runtime/audio-runtime-c58-retire-cli-duration-runtime/",
    )
    unexpected = []
    for row in status:
        name = row[3:] if len(row) >= 4 else row
        if not name.startswith(allowed_prefixes):
            unexpected.append(row)
    require(not unexpected, f"unowned scope changes: {unexpected}")
    print(json.dumps({"branch": branch, "ancestry": "present", "read_only": "unchanged", "scope": "owned"}, sort_keys=True))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True)
    parser.add_argument("--baseline-files", type=int, default=4)
    parser.add_argument("--baseline-lines", type=int, default=1639)
    parser.add_argument("--max-files", type=int, default=2)
    parser.add_argument("--max-lines", type=int, default=450)
    parser.add_argument("--min-file-reduction", type=int, default=2)
    parser.add_argument("--min-line-reduction", type=int, default=1189)
    args = parser.parse_args()
    checks = {
        "baseline-callers-oracles": check_baseline_callers_oracles,
        "lifecycle-admission-terminal-mutations": check_lifecycle_mutations,
        "artifacts-failures-mutations": check_artifact_mutations,
        "cli-retirement": lambda: check_cli_retirement(args),
        "wire-embedding-scope": check_wire_embedding_scope,
        "formatting-owned-go": check_formatting,
        "final-scope-provenance-budget": check_final_scope,
    }
    check = checks.get(args.mode)
    if check is None:
        parser.error(f"unknown mode: {args.mode}")
    try:
        check()
    except (CheckError, OSError, subprocess.TimeoutExpired) as exc:
        print(f"C58 verification failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
