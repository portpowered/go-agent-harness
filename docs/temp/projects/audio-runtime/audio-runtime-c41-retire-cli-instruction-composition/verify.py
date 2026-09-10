#!/usr/bin/env python3
"""Run bounded C41 public-contract and process-cleanup evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parent
REPO_ROOT = ROOT.parents[4]
CONSUMER = ROOT / "consumer"
ARTIFACTS = ROOT / "artifacts"
BINARY = ARTIFACTS / "instruction-consumer"
REPORTS = ROOT / "reports"
MAX_CHILD_SECONDS = 60.0
CLEANUP_SECONDS = 5.0


class VerificationError(Exception):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command_environment(*, for_go: bool = False) -> dict[str, str]:
    """Use an isolated, credential-free environment for every child."""

    sandbox = ARTIFACTS / "sandbox"
    sandbox.mkdir(parents=True, exist_ok=True)
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(sandbox / "home"),
        "TMPDIR": str(sandbox / "tmp"),
        "LANG": "C",
        "LC_ALL": "C",
        "GOWORK": "off",
    }
    for directory in ("home", "tmp"):
        (sandbox / directory).mkdir(parents=True, exist_ok=True)
    if for_go:
        environment["GOCACHE"] = str(sandbox / "go-cache")
        for key in ("GOMODCACHE", "GOTOOLCHAIN"):
            if os.environ.get(key):
                environment[key] = os.environ[key]
    return environment


def normalize_output(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def run_command(
    argv: list[str],
    cwd: Path,
    *,
    timeout: float = MAX_CHILD_SECONDS,
    environment: dict[str, str] | None = None,
) -> dict:
    require(timeout > 0 and timeout <= MAX_CHILD_SECONDS, f"invalid child timeout {timeout}")
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=environment or command_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        text=True,
    )
    timed_out = False
    cleanup_errors: list[str] = []
    stdout = ""
    stderr = ""
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        stdout = normalize_output(error.output)
        stderr = normalize_output(error.stderr)
        deadline = time.monotonic() + CLEANUP_SECONDS
        try:
            if os.name == "nt":
                subprocess.run(
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=max(0.1, deadline - time.monotonic()),
                    check=False,
                )
            else:
                os.killpg(process.pid, signal.SIGKILL)
        except (OSError, subprocess.SubprocessError) as error:
            cleanup_errors.append(str(error))
        try:
            more_stdout, more_stderr = process.communicate(timeout=max(0.1, deadline - time.monotonic()))
            stdout += normalize_output(more_stdout)
            stderr += normalize_output(more_stderr)
        except subprocess.TimeoutExpired:
            cleanup_errors.append("bounded final wait expired")
    duration = time.monotonic() - started
    return {
        "argv": argv,
        "cwd": str(cwd),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
        "duration_seconds": round(duration, 6),
        "timed_out": timed_out,
        "reaped": process.poll() is not None,
        "cleanup_bounded": not cleanup_errors,
        "cleanup_errors": cleanup_errors,
    }


def go_list_inputs() -> dict:
    result = run_command(["go", "list", "-m", "-json", "all"], CONSUMER, environment=command_environment(for_go=True))
    require(result["exit_code"] == 0 and not result["timed_out"], f"go module graph failed: {result}")
    graph = result["stdout"]
    return {"sha256": hashlib.sha256(graph.encode()).hexdigest(), "stdout": graph}


def direct_imports() -> dict:
    result = run_command(["go", "list", "-json", "."], CONSUMER, environment=command_environment(for_go=True))
    require(result["exit_code"] == 0 and not result["timed_out"], f"consumer import inspection failed: {result}")
    package = json.loads(result["stdout"])
    imports = sorted(package.get("Imports", []))
    forbidden = [item for item in imports if "agent-cli" in item or "/internal/" in item]
    require(not forbidden, f"consumer has forbidden direct imports: {forbidden}")
    return {"imports": imports, "forbidden": forbidden}


def build() -> dict:
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    result = run_command(
        ["go", "build", "-trimpath", "-o", str(BINARY), "."],
        CONSUMER,
        environment=command_environment(for_go=True),
    )
    require(result["exit_code"] == 0 and not result["timed_out"], f"consumer build failed: {result}")
    require(BINARY.is_file(), "consumer binary was not produced")
    return {
        "command": result,
        "sha256": sha256_file(BINARY),
        "size": BINARY.stat().st_size,
        "path": str(BINARY),
    }


def parse_stdout(result: dict) -> dict:
    try:
        return json.loads(result["stdout"])
    except json.JSONDecodeError as error:
        raise VerificationError(f"consumer stdout is not JSON: {error}; result={result}") from error


def run_positive(binary: Path, mode: str) -> dict:
    result = run_command([str(binary), "--mode", mode], ROOT)
    require(result["exit_code"] == 0 and not result["timed_out"], f"{mode} failed: {result}")
    report = parse_stdout(result)
    require(report.get("status") == "accepted", f"{mode} status={report}")
    require(report.get("cli_imports") is False and report.get("private_imports") is False, "consumer imported a forbidden package")
    require(report.get("credentials") is False and report.get("hidden_global_io") is False, "consumer used forbidden host state")
    return {"command": result, "report": report}


def run_negative(binary: Path) -> dict:
    result = run_command([str(binary), "--mode", "consumer-negative"], ROOT)
    require(result["exit_code"] == 1 and not result["timed_out"], f"negative control exit={result}")
    report = parse_stdout(result)
    require("policy oracle mismatch" in report.get("error", ""), f"negative control did not fail on the mutated oracle: {report}")
    return {"command": result, "report": report}


def run_cleanup(binary: Path) -> dict:
    result = run_command([str(binary), "--mode", "cleanup-child"], ROOT, timeout=0.25)
    require(result["timed_out"] is True, f"cleanup child unexpectedly completed: {result}")
    require(result["reaped"] is True and result["cleanup_bounded"] is True, f"cleanup was not bounded/reaped: {result}")
    return {
        "command": result,
        "report": {
            "timeout_cleanup": True,
            "reaped": result["reaped"],
            "cleanup_bounded": result["cleanup_bounded"],
        },
    }


def git_revision() -> str:
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
        env=command_environment(),
    )
    return result.stdout.strip()


def git_status() -> str:
    result = subprocess.run(
        ["git", "status", "--short", "--untracked-files=all"],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
        env=command_environment(),
    )
    return result.stdout


def input_descriptors() -> list[dict[str, str | int]]:
    paths = [CONSUMER / "go.mod", CONSUMER / "expected.json", CONSUMER / "main.go", CONSUMER / "main_test.go"]
    descriptors = []
    for path in paths:
        descriptors.append({"path": str(path.relative_to(ROOT)), "size": path.stat().st_size, "sha256": sha256_file(path)})
    return descriptors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=("consumer", "consumer-negative", "public-session", "regression", "cleanup-negative"),
        default="consumer",
    )
    args = parser.parse_args()
    started = time.monotonic()
    result: dict = {
        "schema": "audio-runtime-c41-instruction-evidence/v1",
        "mode": args.mode,
        "source_revision": git_revision(),
        "worktree_status_at_start": git_status(),
        "input_files": input_descriptors(),
    }
    try:
        result["module_graph"] = go_list_inputs()
        result["direct_imports"] = direct_imports()
        result["build"] = build()
        binary = BINARY.resolve()
        if args.mode in ("consumer", "public-session", "regression"):
            result[args.mode.replace("-", "_")] = run_positive(binary, args.mode)
        elif args.mode == "consumer-negative":
            result["negative"] = run_negative(binary)
        else:
            result["cleanup"] = run_cleanup(binary)
        require(time.monotonic() - started <= 600, "overall evidence watchdog exceeded")
        result["status"] = "accepted"
    except (OSError, subprocess.SubprocessError, VerificationError) as error:
        result["status"] = "rejected"
        result["error"] = str(error)
    REPORTS.mkdir(parents=True, exist_ok=True)
    output = REPORTS / f"{args.mode}.json"
    output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result.get("status") == "accepted" else 1


if __name__ == "__main__":
    sys.exit(main())
