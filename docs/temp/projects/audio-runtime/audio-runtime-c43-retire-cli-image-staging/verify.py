#!/usr/bin/env python3
"""Bounded C43 evidence runner.

The runner checks the private staging controls, the public Wire consumer, the
shipped CLI image workflow, and a credential-free tool replay regression. The
wrong-oracle control is expected to fail; it is recorded as a passing negative
control only when the public consumer rejects the deliberately wrong bytes.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from typing import Any


EVIDENCE_DIR = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=EVIDENCE_DIR,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
).resolve()
CONSUMER_DIR = EVIDENCE_DIR / "consumer"
MAX_CHILD_SECONDS = 60.0
MAX_TOTAL_SECONDS = 600.0


class EvidenceFailure(RuntimeError):
    pass


def clipped(value: str, limit: int = 16000) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + "\n...[clipped]"


def terminate_group(process: subprocess.Popen[str]) -> None:
    if os.name == "nt":
        process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=3)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()


class Runner:
    def __init__(self, mode: str) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        self.run_dir = EVIDENCE_DIR / "runs" / f"verify-{stamp}-{os.getpid()}"
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.mode = mode
        self.started = time.monotonic()
        self.sequence = 0
        self.steps: list[dict[str, Any]] = []

    def remaining(self) -> float:
        value = MAX_TOTAL_SECONDS - (time.monotonic() - self.started)
        if value <= 0:
            raise EvidenceFailure("aggregate verifier deadline exceeded")
        return min(value, MAX_CHILD_SECONDS)

    def command(
        self,
        label: str,
        argv: list[str],
        cwd: Path,
        *,
        expect_success: bool = True,
        required_output: str = "",
    ) -> dict[str, Any]:
        self.sequence += 1
        safe_label = "".join(char if char.isalnum() or char in "-_" else "_" for char in label)
        stdout_path = self.run_dir / f"{self.sequence:03d}-{safe_label}.stdout.log"
        stderr_path = self.run_dir / f"{self.sequence:03d}-{safe_label}.stderr.log"
        environment = os.environ.copy()
        if cwd == CONSUMER_DIR:
            environment["GOWORK"] = "off"
        started = time.monotonic()
        timed_out = False
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=environment,
            start_new_session=os.name != "nt",
        )
        try:
            stdout, stderr = process.communicate(timeout=self.remaining())
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = exc.stdout or ""
            stderr = exc.stderr or ""
            terminate_group(process)
        duration = time.monotonic() - started
        stdout_path.write_text(stdout, encoding="utf-8", errors="replace")
        stderr_path.write_text(stderr, encoding="utf-8", errors="replace")
        step = {
            "label": label,
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": process.returncode,
            "timed_out": timed_out,
            "duration_seconds": round(duration, 6),
            "stdout_log": str(stdout_path),
            "stderr_log": str(stderr_path),
            "stdout": clipped(stdout),
            "stderr": clipped(stderr),
        }
        self.steps.append(step)
        success = not timed_out and process.returncode == 0
        if expect_success and not success:
            raise EvidenceFailure(f"{label} failed with exit {process.returncode}; see {stderr_path}")
        if not expect_success:
            if success:
                raise EvidenceFailure(f"{label} unexpectedly passed; wrong-oracle control was not active")
            combined = stdout + "\n" + stderr
            if required_output and required_output not in combined:
                raise EvidenceFailure(f"{label} failed without required negative-control evidence {required_output!r}")
        return step

    def save(self, outcome: dict[str, Any]) -> None:
        (self.run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")


def consumer_steps(runner: Runner) -> None:
    runner.command(
        "consumer-build-test",
        ["rtk", "proxy", "go", "test", "./...", "-count=1", "-timeout=45s"],
        CONSUMER_DIR,
    )
    runner.command(
        "consumer-positive",
        ["rtk", "proxy", "go", "run", ".", "positive"],
        CONSUMER_DIR,
    )
    runner.command(
        "consumer-wrong-oracle-negative",
        ["rtk", "proxy", "go", "run", ".", "negative"],
        CONSUMER_DIR,
        expect_success=False,
        required_output="wrong PNG oracle",
    )
    runner.command(
        "consumer-cleanup-negative",
        ["rtk", "proxy", "go", "run", ".", "cleanup-negative"],
        CONSUMER_DIR,
    )


def focused_runtime_steps(runner: Runner) -> None:
    runner.command(
        "private-image-staging",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "./go-agent-runtime/services/tools/internal/imagestaging",
            "./go-agent-runtime/services/tools/wire",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "private-image-staging-race",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "-race",
            "./go-agent-runtime/services/tools/internal/imagestaging",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "cli-image-adapter",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./internal/services/internal/agentruntime -run TestPrepareSessionImageToolAccess -count=1 -timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "cli-image-adapter-race",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -race -tags=nomicrophone ./internal/services/internal/agentruntime -run TestPrepareSessionImageToolAccess -count=1 -timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "tools-regression",
        ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/tools/...", "-count=1", "-timeout=55s"],
        REPO_ROOT,
    )


def public_cli_steps(runner: Runner) -> None:
    runner.command(
        "shipped-cli-image-workflow",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./test/integration -run '^TestSessionCommandImageAndScheduledAudioUsesExactStagedImagePath$' -count=1 -timeout=55s",
        ],
        REPO_ROOT,
    )


def replay_steps(runner: Runner) -> None:
    runner.command(
        "credential-free-tool-replay",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./test/integration -run '^TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay$' -count=1 -timeout=55s",
        ],
        REPO_ROOT,
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("focused", "consumer", "public-image", "regression"), default="focused")
    args = parser.parse_args()
    runner = Runner(args.mode)
    outcome: dict[str, Any] = {
        "schema": "audio-runtime-c43-retire-cli-image-staging-evidence.v1",
        "mode": args.mode,
        "repo_root": str(REPO_ROOT),
        "evidence_dir": str(EVIDENCE_DIR),
        "steps": runner.steps,
        "status": "FAILED",
    }
    try:
        if args.mode in ("focused", "consumer"):
            consumer_steps(runner)
        if args.mode == "focused":
            focused_runtime_steps(runner)
            public_cli_steps(runner)
            replay_steps(runner)
        elif args.mode == "public-image":
            public_cli_steps(runner)
        elif args.mode == "regression":
            replay_steps(runner)
        outcome["status"] = "PASS"
        outcome["steps"] = runner.steps
        runner.save(outcome)
        print(json.dumps(outcome, indent=2))
        return 0
    except (EvidenceFailure, OSError) as exc:
        outcome["error"] = str(exc)
        outcome["steps"] = runner.steps
        runner.save(outcome)
        print(json.dumps(outcome, indent=2))
        return 1


if __name__ == "__main__":
    sys.exit(main())
