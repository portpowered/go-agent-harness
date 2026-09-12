#!/usr/bin/env python3
"""Bounded, independent checks for the C73 conversation-log extraction."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from typing import Any


TASK_DIR = Path(__file__).resolve().parent
REPO_ROOT = TASK_DIR.parents[4]
RUNTIME_DIR = REPO_ROOT / "go-agent-runtime"
SERVICE_SOURCE = RUNTIME_DIR / "services/conversationlog/internal/service/service.go"
CLI_SOURCE = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_conversation_log.go"
MAX_OUTPUT_BYTES = 64 * 1024


def bounded(text: str) -> str:
    encoded = text.encode("utf-8", errors="replace")
    if len(encoded) <= MAX_OUTPUT_BYTES:
        return text
    return encoded[:MAX_OUTPUT_BYTES].decode("utf-8", errors="replace") + "\n...[output capped]"


def command_result(command: list[str], cwd: Path, timeout: float, overlay: Path | None = None) -> dict[str, Any]:
    env = os.environ.copy()
    env["GOWORK"] = "off"
    if overlay is not None:
        command = [*command, "-overlay", str(overlay)]
    try:
        completed = subprocess.run(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        output = bounded((exc.stdout or "") if isinstance(exc.stdout, str) else "")
        return {"command": command, "cwd": str(cwd), "status": "timeout", "output": output}
    return {
        "command": command,
        "cwd": str(cwd),
        "status": "passed" if completed.returncode == 0 else "failed",
        "exit_code": completed.returncode,
        "output": bounded(completed.stdout),
    }


def focused_control(timeout: float) -> dict[str, Any]:
    return command_result(
        [
            "go",
            "test",
            "./services/conversationlog",
            "-run",
            "TestPublicServiceCorrelatesLateAndEmptyTranscripts|TestPublicServiceBoundsValuesAndDeeplySnapshotsState",
            "-count=1",
            "-timeout=90s",
        ],
        RUNTIME_DIR,
        timeout,
    )


def frozen(args: argparse.Namespace) -> dict[str, Any]:
    checks = [
        command_result(
            ["go", "test", "./services/conversationlog/...", "-count=1", "-timeout=120s"],
            RUNTIME_DIR,
            args.child_timeout,
        ),
        command_result(
            ["go", "test", "./...", "-count=1", "-timeout=90s"],
            TASK_DIR / "external-consumer",
            args.child_timeout,
        ),
    ]
    passed = all(check["status"] == "passed" for check in checks)
    return {"mode": "frozen-jsonl-and-timing", "status": "passed" if passed else "failed", "checks": checks}


def mutation(args: argparse.Namespace, kind: str) -> dict[str, Any]:
    control = focused_control(args.child_timeout)
    if control["status"] != "passed":
        return {
            "mode": kind,
            "status": "failed",
            "reason": "unmutated control did not pass",
            "control": control,
        }

    source = SERVICE_SOURCE.read_text(encoding="utf-8")
    if kind == "mutation-arrival-order":
        old = "s.inputOrdinalForItem(transcript.ItemID)"
        replacement = "s.current.inputOrdinal"
        expected_test = "TestPublicServiceCorrelatesLateAndEmptyTranscripts"
        expected_reason = "late item transcription is assigned by current/arrival order"
    else:
        replacements = {
            "boundValue(call.Arguments)": "call.Arguments",
            "boundValue(response.Content)": "response.Content",
        }
        mutated = source
        for old, replacement in replacements.items():
            if mutated.count(old) != 1:
                return {"mode": kind, "status": "failed", "reason": f"mutation target discovery failed: {old}"}
            mutated = mutated.replace(old, replacement)
        old = replacement = ""
        expected_test = "TestPublicServiceBoundsValuesAndDeeplySnapshotsState"
        expected_reason = "64 KiB argument/result bound is removed"

    if kind == "mutation-arrival-order":
        if source.count(old) != 2:
            return {"mode": kind, "status": "failed", "reason": f"mutation target discovery failed: {old}"}
        mutated = source.replace(old, replacement)

    with tempfile.TemporaryDirectory(prefix="c73-mutation-") as temporary:
        mutated_source = Path(temporary) / "service.go"
        overlay = Path(temporary) / "overlay.json"
        mutated_source.write_text(mutated, encoding="utf-8")
        overlay.write_text(
            json.dumps({"Replace": {str(SERVICE_SOURCE): str(mutated_source)}}),
            encoding="utf-8",
        )
        result = command_result(
            [
                "go",
                "test",
                "./services/conversationlog",
                "-run",
                expected_test,
                "-count=1",
                "-timeout=90s",
            ],
            RUNTIME_DIR,
            args.child_timeout,
            overlay,
        )

    failed_for_oracle = (
        result["status"] == "failed"
        and expected_test in result.get("output", "")
        and "build failed" not in result.get("output", "").lower()
        and "cannot find package" not in result.get("output", "").lower()
    )
    expected_failure = bool(args.expect_failure)
    passed = failed_for_oracle if expected_failure else not failed_for_oracle
    return {
        "mode": kind,
        "status": "passed" if passed else "failed",
        "expected_failure": expected_failure,
        "reason": expected_reason,
        "control": control,
        "mutation": result,
    }


def cli_retirement(args: argparse.Namespace) -> dict[str, Any]:
    current_lines = len(CLI_SOURCE.read_text(encoding="utf-8").splitlines())
    baseline_command = [
        "git",
        "show",
        f"{args.baseline_revision}:agent-cli/internal/services/internal/agentruntime/session_conversation_log.go",
    ]
    baseline = subprocess.run(
        baseline_command,
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
    )
    baseline_lines = len(baseline.stdout.splitlines()) if baseline.returncode == 0 else -1
    retired = baseline_lines - current_lines
    source = CLI_SOURCE.read_text(encoding="utf-8")
    aliases = source.count("type sessionConversation")
    forbidden_policy = [
        "encoding/json",
        "strings.Builder",
        "inputOrdinalForItem",
        "nextToolSequence",
        "boundSessionToolEventValue",
    ]
    forbidden_hits = [token for token in forbidden_policy if token in source]
    passed = (
        baseline_lines == args.baseline_lines
        and current_lines <= args.max_lines
        and retired >= args.min_retired_lines
        and aliases >= 6
        and not forbidden_hits
    )
    return {
        "mode": "cli-retirement",
        "status": "passed" if passed else "failed",
        "baseline_revision": args.baseline_revision,
        "baseline_lines": baseline_lines,
        "current_lines": current_lines,
        "retired_lines": retired,
        "alias_count": aliases,
        "forbidden_policy_tokens": forbidden_hits,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        required=True,
        choices=("frozen-jsonl-and-timing", "mutation-arrival-order", "mutation-no-bound", "cli-retirement", "all"),
    )
    parser.add_argument("--expect-failure", action="store_true")
    parser.add_argument("--child-timeout", type=float, default=120.0)
    parser.add_argument("--baseline-revision", default="d5d6f84363d8569d5dc1a59985f8d45cf50e1d06")
    parser.add_argument("--baseline-lines", type=int, default=531)
    parser.add_argument("--max-lines", type=int, default=265)
    parser.add_argument("--min-retired-lines", type=int, default=266)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 120:
        parser.error("--child-timeout must be between 1 and 120 seconds")

    if args.mode == "frozen-jsonl-and-timing":
        result: dict[str, Any] = frozen(args)
    elif args.mode in ("mutation-arrival-order", "mutation-no-bound"):
        result = mutation(args, args.mode)
    elif args.mode == "cli-retirement":
        result = cli_retirement(args)
    else:
        results = [
            frozen(args),
            mutation(args, "mutation-arrival-order"),
            mutation(args, "mutation-no-bound"),
            cli_retirement(args),
        ]
        result = {"mode": "all", "status": "passed" if all(item["status"] == "passed" for item in results) else "failed", "checks": results}
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
