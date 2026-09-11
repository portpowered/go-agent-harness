#!/usr/bin/env python3
"""Capture the requested local room/session lifecycle normal and race checks."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

from run import hermetic_base_environment, run_bounded, tree_bytes


EVIDENCE = Path(__file__).resolve().parent


def clean_parent_exit(process: object) -> bool:
    if not isinstance(process, dict):
        return False
    required_process = ("exit_code", "timed_out", "output_overflow", "reader_survivor", "cleanup")
    if any(key not in process for key in required_process):
        return False
    cleanup = process.get("cleanup")
    if not isinstance(cleanup, dict):
        return False
    required_cleanup = ("reason", "parent_exit_code", "term_sent", "kill_sent", "group_survivor", "term_error", "kill_error")
    if any(key not in cleanup for key in required_cleanup):
        return False
    return (
        process.get("exit_code") == 0
        and process.get("timed_out") is False
        and process.get("output_overflow") is False
        and process.get("reader_survivor") is False
        and cleanup.get("reason") == "parent_exit"
        and cleanup.get("parent_exit_code") == 0
        and cleanup.get("term_sent") is False
        and cleanup.get("kill_sent") is False
        and cleanup.get("group_survivor") is False
        and cleanup.get("term_error") is None
        and cleanup.get("kill_error") is None
    )


def git(repo: Path, *args: str) -> str:
    result = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip())
    return result.stdout.strip()


def write(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def main() -> int:
    repo = Path(git(EVIDENCE, "rev-parse", "--show-toplevel"))
    module = repo / "go-agent-runtime"
    output = EVIDENCE / "regressions"
    free_space_before_run = shutil.disk_usage(EVIDENCE).free
    failed = False
    with tempfile.TemporaryDirectory(prefix="c52-regressions-") as temp_dir:
        cache_root = Path(temp_dir)
        common = hermetic_base_environment(os.environ, cache_root)
        common.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "auto", "GOWORK": "", "GOCACHE": str(cache_root / "gocache"), "GOMODCACHE": str(cache_root / "gomodcache"), "GOTMPDIR": str(cache_root / "gotmp")})
        for key in ("GOCACHE", "GOMODCACHE"):
            Path(common[key]).mkdir(parents=True, exist_ok=True)
        checks = {
            "normal": [
                "go", "test", "-tags=nomicrophone", "-count=1", "-timeout=120s",
                "./services/rooms/internal/lifecycle", "./services/session/internal/live",
            ],
            "race": [
                "go", "test", "-race", "-tags=nomicrophone", "-count=1", "-timeout=180s",
                "./services/rooms/internal/lifecycle",
            ],
        }
        results = {}
        for name, command in checks.items():
            process = run_bounded(command, module, common, 180 if name == "race" else 120, 524288, output / name)
            results[name] = {
                "command": command,
                "cwd": str(module),
                "source_revision": git(repo, "rev-parse", "HEAD"),
                "process": process,
                "passes": clean_parent_exit(process),
            }
            write(output / f"{name}.json", results[name])
            if not results[name]["passes"]:
                failed = True
                break
        free_space_before_cleanup = shutil.disk_usage(EVIDENCE).free
        reproducible_scratch_bytes_before_cleanup = tree_bytes(cache_root)
    free_space_after_cleanup = shutil.disk_usage(EVIDENCE).free
    storage = {
        "source_staging_bytes": 0,
        "binary_output_bytes": 0,
        "fixture_report_bytes": tree_bytes(output),
        "reproducible_scratch_bytes_before_cleanup": reproducible_scratch_bytes_before_cleanup,
        "reproducible_scratch_bytes_retained_after_cleanup": 0,
        "free_space_before_run_bytes": free_space_before_run,
        "free_space_before_cleanup_bytes": free_space_before_cleanup,
        "free_space_after_cleanup_bytes": free_space_after_cleanup,
        "cleanup_verified": not Path(temp_dir).exists(),
    }
    summary = {"schema": "audio-runtime-c52-local-regressions-v1", "checks": results, "passes": not failed, "storage": storage}
    write(output / "summary.json", summary)
    if failed:
        return 1
    print(json.dumps({"status": "PASS", "checks": list(results), "passes": True, "storage_recorded": True}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
