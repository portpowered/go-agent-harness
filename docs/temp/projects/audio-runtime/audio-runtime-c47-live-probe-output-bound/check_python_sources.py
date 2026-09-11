#!/usr/bin/env python3
"""Compile every changed C47 Python source into task-local bytecode."""

from __future__ import annotations

import hashlib
import json
import pathlib
import py_compile
import sys
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
SOURCES = [
    ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py",
    ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c45-model-probe-oracle-repair/run_staged_probe.py",
    HERE / "check_python_sources.py",
    HERE / "run_c45_controls_private.py",
    HERE / "run_c45_regressions.py",
    HERE / "refresh_live_evidence.py",
    HERE / "test_resource_bounds.py",
]


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main() -> int:
    started = time.monotonic()
    bytecode_root = HERE / "python-check"
    bytecode_root.mkdir(parents=True, exist_ok=True)
    compiled: list[dict[str, Any]] = []
    report: dict[str, Any] = {
        "schema": "audio-runtime-c47-python-source-check/v1",
        "decision": "FAILED",
        "sources": [str(path) for path in SOURCES],
        "bytecode_root": str(bytecode_root),
    }
    try:
        for source in SOURCES:
            if not source.is_file():
                raise FileNotFoundError(source)
            relative = source.relative_to(ROOT)
            destination = bytecode_root / relative.with_suffix(".pyc")
            destination.parent.mkdir(parents=True, exist_ok=True)
            py_compile.compile(str(source), cfile=str(destination), doraise=True)
            compiled.append(
                {
                    "source": str(source),
                    "source_sha256": sha256(source),
                    "bytecode": str(destination),
                    "bytecode_bytes": destination.stat().st_size,
                }
            )
        report["decision"] = "ACCEPTED"
        report["compiled"] = compiled
        report["elapsed_seconds"] = round(time.monotonic() - started, 6)
        (HERE / "python-source-check.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2))
        return 0
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
        report["compiled"] = compiled
        report["elapsed_seconds"] = round(time.monotonic() - started, 6)
        (HERE / "python-source-check.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
