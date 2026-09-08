#!/usr/bin/env python3
"""Run the C09 baseline-history controls against a public gate executable."""

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


TIMEOUT_SECONDS = 120

CASES = (
    ("inherited-baseline-after-main-advance", "inherited-baseline-pass", True, (), None, ()),
    ("added-exemption", "addition-fails", False, ("baseline-history-add",), None, ()),
    ("ceiling-growth", "ceiling-growth-fails", False, ("baseline-history-increase",), None, ()),
    ("source-replacement", "source-replacement-fails", False, ("baseline-history-source",), None, ()),
    ("source-removal", "source-removal-fails", False, ("baseline-history-source",), None, ()),
    (
        "unsupported-historical-version",
        "historical-version-999-fails",
        False,
        ("baseline-history",),
        "version 999",
        (),
    ),
    ("retained-rename-source", "retained-rename-fails", False, ("baseline-history-rename",), None, ()),
    ("unknown-rename-source", "unknown-rename-fails", False, ("baseline-history-rename",), None, ()),
    ("missing-rename-target", "missing-target-rename-fails", False, ("baseline-history-rename",), None, ()),
    (
        "colliding-rename-sources",
        "colliding-rename-sources-fails",
        False,
        (),
        "appears more than once",
        (),
    ),
    (
        "persisted-target-ceiling-growth",
        "persisted-rename-growth-fails",
        False,
        ("baseline-history-increase",),
        None,
        ("baseline-history-rename",),
    ),
    ("persisted-one-to-one-rename", "persisted-rename-pass", True, (), None, ()),
    ("baseline-ceiling-reduction", "reduction-pass", True, (), None, ()),
    ("baseline-exemption-deletion", "deletion-pass", True, (), None, ()),
    ("bootstrap-retained-rename-source", "bootstrap-retained-source-fails", False, ("baseline-history-rename",), None, ()),
    ("bootstrap-missing-rename-target", "bootstrap-missing-target-fails", False, ("baseline-history-rename",), None, ()),
    ("bootstrap-one-to-one-rename", "bootstrap-valid-rename-pass", True, (), None, ()),
)

VARIANTS = (
    ("all", ("-check", "all")),
    ("explicit-scope-all", ("-module-dir", "mod", "-pattern", "./...", "-check", "all")),
    ("architecture-scope", ("-module-dir", "mod", "-pattern", "./...", "-check", "architecture")),
    ("size-scope", ("-module-dir", "mod", "-pattern", "./...", "-check", "size")),
)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def fixture_digests(root: Path) -> dict[str, str]:
    files = sorted(path for path in root.rglob("*") if path.is_file())
    return {path.relative_to(root).as_posix(): sha256(path) for path in files}


def run_case(binary: Path, fixture: Path, args: tuple[str, ...], run_dir: Path) -> dict:
    command = [
        str(binary),
        "-repo",
        str(fixture),
        "-manifest",
        "manifest.json",
        "-baseline",
        "baseline.json",
        "-baseline-base",
        "mainline",
        "-format",
        "json",
        *args,
    ]
    started = time.monotonic()
    timed_out = False
    process = subprocess.Popen(
        command,
        cwd=fixture,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=(os.name != "nt"),
    )
    try:
        stdout, stderr = process.communicate(timeout=TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        timed_out = True
        if os.name != "nt":
            os.killpg(process.pid, signal.SIGKILL)
        else:
            process.kill()
        stdout, stderr = process.communicate()
    elapsed = time.monotonic() - started
    stdout_text = stdout.decode("utf-8", errors="replace")
    stderr_text = stderr.decode("utf-8", errors="replace")
    (run_dir / "stdout.txt").write_text(stdout_text, encoding="utf-8")
    (run_dir / "stderr.txt").write_text(stderr_text, encoding="utf-8")
    report = None
    if stdout_text.strip():
        try:
            report = json.loads(stdout_text)
        except json.JSONDecodeError:
            report = None
    issues = report.get("issues", []) if isinstance(report, dict) else []
    rules = sorted({issue.get("rule", "") for issue in issues if isinstance(issue, dict)})
    messages = [issue.get("message", "") for issue in issues if isinstance(issue, dict)]
    messages.extend(line for line in stderr_text.splitlines() if line.strip())
    result = {
        "command": command,
        "cwd": str(fixture),
        "exit_code": process.returncode,
        "elapsed_seconds": elapsed,
        "timed_out": timed_out,
        "observed_rules": rules,
        "observed_messages": messages,
        "stdout": "stdout.txt",
        "stderr": "stderr.txt",
    }
    (run_dir / "result.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return result


def matches(result: dict, expected_pass: bool, expected_rules: tuple[str, ...], expected_message: str | None, forbidden_rules: tuple[str, ...]) -> bool:
    if result["timed_out"]:
        return False
    exit_code = result["exit_code"]
    if expected_pass and exit_code != 0:
        return False
    if not expected_pass and exit_code == 0:
        return False
    rules = set(result["observed_rules"])
    if expected_pass and rules:
        return False
    if any(rule not in rules for rule in expected_rules):
        return False
    if any(rule in rules for rule in forbidden_rules):
        return False
    if expected_message and not any(expected_message in message for message in result["observed_messages"]):
        return False
    return True


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--fixtures", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    binary = args.binary.resolve()
    fixtures = args.fixtures.resolve()
    output = args.output.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise SystemExit(f"binary is not executable: {binary}")
    if not fixtures.is_dir():
        raise SystemExit(f"fixtures directory does not exist: {fixtures}")
    if output == fixtures or fixtures in output.parents:
        raise SystemExit("output must be outside the preserved fixture repositories")
    output.mkdir(parents=True, exist_ok=True)
    before = fixture_digests(fixtures)
    summary = {
        "binary": str(binary),
        "binary_sha256": sha256(binary),
        "fixtures": str(fixtures),
        "fixture_digests_before": before,
        "cases": [],
    }
    for name, fixture_name, expected_pass, expected_rules, expected_message, forbidden_rules in CASES:
        fixture = fixtures / fixture_name
        if not fixture.is_dir():
            raise SystemExit(f"missing fixture for {name}: {fixture}")
        for variant, variant_args in VARIANTS:
            case_dir = output / "runs" / f"{name}--{variant}"
            case_dir.mkdir(parents=True, exist_ok=True)
            result = run_case(binary, fixture, variant_args, case_dir)
            result.update(
                {
                    "name": name,
                    "fixture": fixture_name,
                    "variant": variant,
                    "expected_pass": expected_pass,
                    "expected_rules": expected_rules,
                    "expected_message": expected_message,
                    "forbidden_rules": forbidden_rules,
                }
            )
            result["passed"] = matches(result, expected_pass, expected_rules, expected_message, forbidden_rules)
            summary["cases"].append(result)
    after = fixture_digests(fixtures)
    summary["fixture_digests_after"] = after
    summary["fixtures_unchanged"] = before == after
    summary["passed"] = summary["fixtures_unchanged"] and all(case["passed"] for case in summary["cases"])
    (output / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"passed": summary["passed"], "cases": len(summary["cases"]), "fixtures_unchanged": summary["fixtures_unchanged"]}, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
