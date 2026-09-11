#!/usr/bin/env python3
"""Run the unchanged C45 controls with every writable root redirected into C47."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import runpy as real_runpy
import subprocess
import sys
import tempfile as real_tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
C45_CONTROLS = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c45-model-probe-oracle-repair/test_oracle_controls.py"
VERIFY = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py"
STAGED_PROBE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c45-model-probe-oracle-repair/run_staged_probe.py"
ORIGINAL_REVISION = "5f14c45313cfdc71e000fda209e3408fcf863faf"


class _PrivateRunpy:
    @staticmethod
    def run_path(path: str, *args: Any, **kwargs: Any) -> dict[str, Any]:
        requested = pathlib.Path(path)
        actual = STAGED_PROBE if requested.name == "run_staged_probe.py" and not requested.is_file() else requested
        loaded = real_runpy.run_path(str(actual), *args, **kwargs)
        if requested.name == "run_staged_probe.py":
            globals_ = loaded["main"].__globals__
            globals_["ROOT"] = ROOT
            globals_["VERIFY"] = VERIFY
        return loaded


class _PrivateTempfile:
    @staticmethod
    def TemporaryDirectory(*args: Any, **kwargs: Any) -> Any:
        private_root = kwargs.pop("dir", None)
        if private_root is None:
            private_root = PRIVATE_ROOT
        pathlib.Path(private_root).mkdir(parents=True, exist_ok=True)
        return real_tempfile.TemporaryDirectory(*args, dir=str(private_root), **kwargs)


PRIVATE_BASE = HERE / "c45-focused"
PRIVATE_ROOT = PRIVATE_BASE


def main() -> int:
    global PRIVATE_BASE, PRIVATE_ROOT
    parser = argparse.ArgumentParser()
    parser.add_argument("--original-revision", default=ORIGINAL_REVISION)
    parser.add_argument("--output-root", type=pathlib.Path)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--total-timeout", type=float, default=300)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 60 or args.total_timeout <= 0 or args.total_timeout > 300:
        raise SystemExit("C47 C45-focused bounds exceeded")

    original_revision = args.original_revision
    PRIVATE_BASE = (args.output_root or HERE / "c45-regressions").resolve()
    try:
        PRIVATE_BASE.relative_to(HERE.resolve())
    except ValueError as exc:
        raise SystemExit(f"C47 private output root must remain inside {HERE}: {PRIVATE_BASE}") from exc
    PRIVATE_BASE.mkdir(parents=True, exist_ok=True)
    PRIVATE_ROOT = PRIVATE_BASE / f"run-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    PRIVATE_ROOT.mkdir(parents=True, exist_ok=False)
    controls = real_runpy.run_path(str(C45_CONTROLS), run_name="c47_c45_controls")
    globals_ = controls["main"].__globals__
    globals_.update(
        {
            "HERE": PRIVATE_ROOT,
            "ROOT": ROOT,
            "VERIFY": VERIFY,
            "PRESERVED_RAW_FAILURE": PRIVATE_ROOT / "preserved-raw-failure.stderr",
            "runpy": _PrivateRunpy,
            "tempfile": _PrivateTempfile,
        }
    )
    started = time.monotonic()
    old_source = globals_["git_show"](original_revision)
    reproduction = globals_["reproduce_original"](original_revision, old_source, PRIVATE_ROOT / "reproduction")
    verify_module = globals_["load_verify"](VERIFY)
    success_path = globals_["run_staged_probe_success_path"]()
    aggregate_output = globals_["run_staged_probe_aggregate_output_control"]()
    deadline = globals_["run_staged_probe_deadline_control"]()
    current_root = PRIVATE_ROOT / "current-controls"
    current_root.mkdir(parents=True, exist_ok=True)
    current = globals_["run_current_controls"](
        verify_module,
        current_root,
        started,
        args.total_timeout,
        args.child_timeout,
    )
    elapsed = time.monotonic() - started
    if elapsed > args.total_timeout:
        raise SystemExit(f"C47 C45-focused aggregate deadline exceeded: {elapsed:.3f}s")
    result = {
        "schema": "audio-runtime-c47-c45-focused-controls/v1",
        "decision": "ACCEPTED",
        "original_revision": original_revision,
        "private_root": str(PRIVATE_ROOT),
        "reproduction": reproduction,
        "staged_probe_success_path": success_path,
        "staged_probe_aggregate_output": aggregate_output,
        "staged_probe_deadline": deadline,
        "controls": current,
        "elapsed_seconds": round(elapsed, 6),
    }
    report = PRIVATE_BASE / "focused-result.json"
    report.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
