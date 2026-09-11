#!/usr/bin/env python3
"""Capture the immutable prior PR438 hermetic observation without clipping it."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path


RUN_ID = "34562579355"
JOB_ID = "103148160889"
HEAD = "823bd350fe5d11782c38bda87d7b7bfd7d89d7cd"


def run(command: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, text=True, capture_output=True, check=False)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=Path(__file__).parent)
    args = parser.parse_args()
    root = args.output.resolve()
    ci = root / "ci"
    ci.mkdir(parents=True, exist_ok=True)

    metadata_command = [
        "gh",
        "run",
        "view",
        RUN_ID,
        "--job",
        JOB_ID,
        "--json",
        "databaseId,headSha,status,conclusion,startedAt,createdAt,updatedAt,url,name,workflowName,attempt",
    ]
    metadata_result = run(metadata_command)
    if metadata_result.returncode != 0:
        sys.stderr.write(metadata_result.stderr)
        return metadata_result.returncode or 1
    try:
        metadata = json.loads(metadata_result.stdout)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid gh metadata: {exc}") from exc
    if metadata.get("databaseId") != int(RUN_ID) or metadata.get("headSha") != HEAD:
        raise SystemExit("prior CI metadata is not the declared PR438 head")

    log_command = ["gh", "run", "view", RUN_ID, "--job", JOB_ID, "--log-failed"]
    log_result = run(log_command)
    log_path = ci / f"run-{RUN_ID}-job-{JOB_ID}.log"
    log_path.write_text(log_result.stdout, encoding="utf-8")
    metadata["capture"] = {
        "capturedAt": datetime.now(timezone.utc).isoformat(),
        "metadataCommand": metadata_command,
        "logCommand": log_command,
        "logExitCode": log_result.returncode,
        "logSha256": sha256(log_path),
        "logBytes": log_path.stat().st_size,
        "logLines": len(log_result.stdout.splitlines()),
        "stderrSha256": hashlib.sha256(log_result.stderr.encode()).hexdigest(),
    }
    metadata_path = ci / f"run-{RUN_ID}-job-{JOB_ID}.json"
    metadata_path.write_text(json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if log_result.returncode != 0:
        sys.stderr.write(log_result.stderr)
        return log_result.returncode
    print(json.dumps({"metadata": str(metadata_path), "log": str(log_path), "sha256": metadata["capture"]["logSha256"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
