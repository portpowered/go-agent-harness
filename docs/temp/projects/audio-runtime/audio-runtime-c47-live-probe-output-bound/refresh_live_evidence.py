#!/usr/bin/env python3
"""Refresh dependent hashes and byte counts after normalizing retained evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent


def tree_bytes(path: pathlib.Path) -> int:
    return sum(item.stat().st_size for item in path.rglob("*") if item.is_file())


def update_stderr_metadata(value: Any, stderr_path: pathlib.Path, stderr_bytes: int, stderr_sha256: str) -> None:
    if isinstance(value, dict):
        candidate = value.get("stderr_path")
        if isinstance(candidate, str) and pathlib.Path(candidate).resolve() == stderr_path.resolve():
            value["stderr_bytes"] = stderr_bytes
            value["stderr_sha256"] = stderr_sha256
        for child in value.values():
            update_stderr_metadata(child, stderr_path, stderr_bytes, stderr_sha256)
    elif isinstance(value, list):
        for child in value:
            update_stderr_metadata(child, stderr_path, stderr_bytes, stderr_sha256)


def write_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def refresh_run(run_dir: pathlib.Path) -> dict[str, Any]:
    stderr_path = run_dir / "invalid-self-play-admission.stderr"
    normalized = stderr_path.read_bytes().rstrip(b"\n") + b"\n"
    stderr_path.write_bytes(normalized)
    stderr_bytes = len(normalized)
    stderr_sha256 = hashlib.sha256(normalized).hexdigest()

    admission_path = run_dir / "invalid-self-play-admission.json"
    admission = json.loads(admission_path.read_text(encoding="utf-8"))
    update_stderr_metadata(admission, stderr_path, stderr_bytes, stderr_sha256)
    write_json(admission_path, admission)

    outcome_path = run_dir / "outcome.json"
    outcome = json.loads(outcome_path.read_text(encoding="utf-8"))
    update_stderr_metadata(outcome, stderr_path, stderr_bytes, stderr_sha256)
    for _ in range(8):
        write_json(outcome_path, outcome)
        measured = tree_bytes(run_dir)
        if outcome.get("report_bytes") == measured:
            break
        outcome["report_bytes"] = measured
    else:
        raise RuntimeError(f"report size did not stabilize: {outcome_path}")

    latest_path = run_dir.parents[1] / "latest-staged-probe.json"
    latest = json.loads(latest_path.read_text(encoding="utf-8"))
    update_stderr_metadata(latest, stderr_path, stderr_bytes, stderr_sha256)
    latest["report_bytes"] = outcome["report_bytes"]
    write_json(latest_path, latest)
    return {
        "run_dir": str(run_dir),
        "stderr_bytes": stderr_bytes,
        "stderr_sha256": stderr_sha256,
        "report_bytes": outcome["report_bytes"],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("run_dir", type=pathlib.Path, nargs="+")
    args = parser.parse_args()
    results = [refresh_run(path.resolve()) for path in args.run_dir]
    print(json.dumps({"schema": "audio-runtime-c47-live-evidence-refresh/v1", "results": results}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
