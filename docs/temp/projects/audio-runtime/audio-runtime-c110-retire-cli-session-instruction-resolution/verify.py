#!/usr/bin/env python3
"""Run bounded C110 public-contract and retirement evidence."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parent
REPO_ROOT = ROOT.parents[4]
CONSUMER = ROOT / "consumer"
REPORTS = ROOT / "reports"
LEGACY = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_instructions.go"
NEW_PACKAGE = REPO_ROOT / "go-agent-runtime/services/sessioninstructions"


class VerificationError(Exception):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def run(argv: list[str], cwd: Path, *, env: dict[str, str] | None = None) -> dict:
    child_env = os.environ.copy()
    child_env.update(env or {})
    child_env["GOWORK"] = "off"
    completed = subprocess.run(argv, cwd=cwd, env=child_env, text=True, capture_output=True, timeout=120, check=False)
    return {"argv": argv, "cwd": str(cwd), "exit_code": completed.returncode, "stdout": completed.stdout, "stderr": completed.stderr}


def json_command(argv: list[str], cwd: Path) -> dict:
    result = run(argv, cwd)
    require(result["exit_code"] == 0, f"command failed: {result}")
    try:
        return json.loads(result["stdout"])
    except json.JSONDecodeError as error:
        raise VerificationError(f"command did not emit JSON: {error}; result={result}") from error


def verify_imports() -> dict:
    result = run(["go", "list", "-json", "."], CONSUMER)
    require(result["exit_code"] == 0, f"consumer import inspection failed: {result}")
    package = json.loads(result["stdout"])
    imports = sorted(package.get("Imports", []))
    forbidden = [item for item in imports if "agent-cli" in item or "/internal/" in item]
    require(not forbidden, f"consumer has forbidden direct imports: {forbidden}")
    return {"imports": imports, "forbidden": forbidden}


def verify_legacy_boundary() -> dict:
    text = LEGACY.read_text(encoding="utf-8")
    lines = len(text.splitlines())
    require(lines <= 87, f"legacy file has {lines} lines, want <= 87")
    require("sessioninstructionswire.NewInstructionService" in text, "legacy file does not use dedicated instruction Wire")
    require("Tool-grounding requirements:" not in text, "legacy file retains policy text")
    require("os.ReadFile" not in text, "legacy file retains unbounded file loading")
    go_files = [path for path in NEW_PACKAGE.rglob("*.go") if not path.name.endswith("_test.go")]
    source = "\n".join(path.read_text(encoding="utf-8") for path in go_files)
    forbidden = [token for token in ("agent-cli/", "services/session/internal", "os.Getenv", "func init(") if token in source]
    require(not forbidden, f"new package has forbidden host coupling: {forbidden}")
    return {"legacy_lines": lines, "forbidden_new_package_tokens": forbidden}


def verify(mode: str) -> dict:
    report = {"mode": mode, "status": "accepted", "consumer": {}, "boundary": {}}
    report["consumer"]["imports"] = verify_imports()
    report["boundary"] = verify_legacy_boundary()
    positive = json_command(["go", "run", ".", "--mode", "positive"], CONSUMER)
    require(positive.get("status") == "accepted", f"positive consumer status={positive}")
    report["consumer"]["positive"] = positive
    invalid = json_command(["go", "run", ".", "--mode", "invalid"], CONSUMER)
    require(invalid.get("status") == "accepted", f"invalid consumer status={invalid}")
    report["consumer"]["invalid"] = invalid
    printed = json_command(["go", "run", ".", "--mode", "print"], CONSUMER)
    require(printed.get("status") == "accepted", f"print consumer status={printed}")
    report["consumer"]["print"] = printed
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", default="positive-and-guard-mutations")
    parser.add_argument("--report", action="store_true")
    args = parser.parse_args()
    if args.mode not in {"positive", "positive-and-guard-mutations"}:
        raise VerificationError(f"unsupported mode {args.mode!r}")
    report = verify(args.mode)
    if args.report:
        REPORTS.mkdir(parents=True, exist_ok=True)
        (REPORTS / "c110-public-contract.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"status": report["status"], "mode": report["mode"], "legacy_lines": report["boundary"]["legacy_lines"]}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.TimeoutExpired) as error:
        print(f"verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
