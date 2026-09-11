#!/usr/bin/env python3
"""Refresh dependent hashes and byte counts after normalizing retained evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
MAX_STABILIZATION_PASSES = 16


def tree_bytes(path: pathlib.Path) -> int:
    total = 0
    for item in path.rglob("*"):
        if item.is_symlink():
            raise ValueError(f"refusing symlink in C47 evidence tree: {item}")
        if item.is_file():
            total += item.stat().st_size
    return total


def _path_is_within(path: pathlib.Path, parent: pathlib.Path, *, allow_equal: bool = True) -> bool:
    if not allow_equal and path == parent:
        return False
    try:
        path.relative_to(parent)
    except ValueError:
        return False
    return True


def _resolve_c47_path(path: pathlib.Path, label: str, *, allow_root: bool = False) -> pathlib.Path:
    root = HERE.resolve()
    try:
        resolved = path.resolve()
    except (OSError, RuntimeError) as exc:
        raise ValueError(f"unable to resolve {label}: {path}: {exc}") from exc
    if not _path_is_within(resolved, root, allow_equal=allow_root):
        raise ValueError(f"{label} must remain within the C47-owned tree: {path}")
    if not resolved.exists():
        raise ValueError(f"{label} does not exist in the C47-owned tree: {path}")
    return resolved


def _owned_file(path: pathlib.Path, label: str) -> pathlib.Path:
    if path.is_symlink():
        raise ValueError(f"refusing symlink for {label}: {path}")
    resolved = _resolve_c47_path(path, label)
    if not resolved.is_file():
        raise ValueError(f"{label} is not a regular file: {path}")
    return resolved


def _json_object(path: pathlib.Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ValueError(f"unable to read {label}: {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError(f"{label} must contain a JSON object: {path}")
    return value


def validate_run_paths(run_dir: pathlib.Path) -> tuple[pathlib.Path, pathlib.Path, pathlib.Path]:
    """Return canonical C47 run/output paths, rejecting peer and symlink escapes."""

    run_dir = _resolve_c47_path(run_dir, "run directory", allow_root=False)
    if not run_dir.is_dir():
        raise ValueError(f"run directory is not a directory: {run_dir}")
    if run_dir.parent.name != "runs":
        raise ValueError(f"run directory must use the C47-owned runs/<run> layout: {run_dir}")

    output_root = _resolve_c47_path(run_dir.parent.parent, "output root", allow_root=True)
    if not output_root.is_dir():
        raise ValueError(f"output root is not a directory: {output_root}")

    # A run tree is retained evidence, so a symlink anywhere in it is an
    # ambiguous write target even when it currently resolves inside C47.
    for item in run_dir.rglob("*"):
        if item.is_symlink():
            raise ValueError(f"refusing symlink in C47 evidence tree: {item}")

    stderr_path = _owned_file(run_dir / "invalid-self-play-admission.stderr", "stderr evidence")
    _owned_file(run_dir / "invalid-self-play-admission.json", "admission evidence")
    outcome_path = _owned_file(run_dir / "outcome.json", "outcome evidence")
    latest_path = _owned_file(output_root / "latest-staged-probe.json", "latest report")
    return run_dir, outcome_path, latest_path


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
    run_dir, outcome_path, latest_path = validate_run_paths(run_dir)
    stderr_path = run_dir / "invalid-self-play-admission.stderr"
    normalized = stderr_path.read_bytes().rstrip(b"\n") + b"\n"
    stderr_path.write_bytes(normalized)
    stderr_bytes = len(normalized)
    stderr_sha256 = hashlib.sha256(normalized).hexdigest()

    admission_path = run_dir / "invalid-self-play-admission.json"
    admission = _json_object(admission_path, "admission evidence")
    update_stderr_metadata(admission, stderr_path, stderr_bytes, stderr_sha256)
    write_json(admission_path, admission)

    outcome = _json_object(outcome_path, "outcome evidence")
    update_stderr_metadata(outcome, stderr_path, stderr_bytes, stderr_sha256)

    latest = _json_object(latest_path, "latest report")
    update_stderr_metadata(latest, stderr_path, stderr_bytes, stderr_sha256)
    for _ in range(MAX_STABILIZATION_PASSES):
        write_json(outcome_path, outcome)
        write_json(latest_path, latest)
        measured = tree_bytes(run_dir)
        latest_measured = latest_path.stat().st_size
        changed = False
        if outcome.get("report_bytes") != measured:
            outcome["report_bytes"] = measured
            changed = True
        if outcome.get("latest_report_bytes") != latest_measured:
            outcome["latest_report_bytes"] = latest_measured
            changed = True
        if latest.get("report_bytes") != measured:
            latest["report_bytes"] = measured
            changed = True
        if latest.get("latest_report_bytes") != latest_measured:
            latest["latest_report_bytes"] = latest_measured
            changed = True
        if not changed:
            break
    else:
        raise RuntimeError(f"report sizes did not stabilize: {outcome_path}, {latest_path}")
    return {
        "run_dir": str(run_dir),
        "stderr_bytes": stderr_bytes,
        "stderr_sha256": stderr_sha256,
        "report_bytes": measured,
        "latest_report_bytes": latest_measured,
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
