#!/usr/bin/env python3
"""Named C107 entry point for the bounded public accepted-main probe."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import time


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
VERIFY = HERE / "verify.py"
SOURCE_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def safe_environment() -> dict[str, str]:
    markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    return {key: value for key, value in os.environ.items() if not any(marker in key.upper() for marker in markers)}


def redact(value: str) -> str:
    value = re.sub(r"(?i)(api[_-]?key|token|secret|password|credential)\s*[:=]\s*[^\s,}]+", r"\1=<redacted>", value)
    return re.sub(r"\bsk-[A-Za-z0-9_-]{12,}\b", "<redacted-api-key>", value)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    args = parser.parse_args()
    if args.source != SOURCE_REVISION:
        print(json.dumps({"status": "failed", "error": "run_public_regressions.py only accepts the immutable C107 source revision"}, sort_keys=True), file=sys.stderr)
        return 1
    command = [sys.executable, str(VERIFY), "--mode", "public", "--child-timeout", "60", "--total-timeout", "300"]
    started = time.monotonic()
    result = subprocess.run(command, cwd=ROOT, env=safe_environment(), capture_output=True, text=True, timeout=300)
    output = redact(result.stdout)
    error = redact(result.stderr)
    report = {
        "schema_version": "c107-public-regressions-entrypoint-v1",
        "source_revision": args.source,
        "command": command,
        "exit_code": result.returncode,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "stdout_sha256": sha256(output.encode()),
        "stderr_sha256": sha256(error.encode()),
        "credential_free": True,
        "realtime": "not used; delegated offline fixture replay",
        "child_report": "runs/public/report.json",
    }
    report_path = HERE / "runs/public/entrypoint.json"
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if result.returncode != 0:
        print(json.dumps({"status": "failed", "report": str(report_path.relative_to(ROOT)), "stderr_sha256": report["stderr_sha256"]}, sort_keys=True), file=sys.stderr)
        return 1
    print(json.dumps({"status": "passed", "report": str(report_path.relative_to(ROOT)), "source_revision": args.source, "elapsed_seconds": report["elapsed_seconds"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
