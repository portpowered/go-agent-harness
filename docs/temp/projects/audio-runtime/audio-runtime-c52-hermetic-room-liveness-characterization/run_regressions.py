#!/usr/bin/env python3
"""Capture the requested local room/session lifecycle normal and race checks."""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

from run import run_bounded


EVIDENCE = Path(__file__).resolve().parent


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
    common = os.environ.copy()
    common.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "auto", "GOWORK": ""})
    for key in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY"):
        common.pop(key, None)
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
            "passes": process.get("exit_code") == 0 and not process.get("timed_out") and not process.get("output_overflow") and not process.get("reader_survivor"),
        }
        write(output / f"{name}.json", results[name])
        if not results[name]["passes"]:
            write(output / "summary.json", {"schema": "audio-runtime-c52-local-regressions-v1", "checks": results, "passes": False})
            return 1
    write(output / "summary.json", {"schema": "audio-runtime-c52-local-regressions-v1", "checks": results, "passes": True})
    print(json.dumps({"status": "PASS", "checks": list(results), "passes": True}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
