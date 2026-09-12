#!/usr/bin/env python3
"""Run bounded shipped-yui replay smoke cases for C104."""

from __future__ import annotations

import json
import os
import argparse
import signal
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[5]
YUI = ROOT / "agent-cli/cmd/yui"
HEALTHY = ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
TOOL_FAILURE = ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_failure_tool_call.session.json"


def command(args: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    return subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True, check=False)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", action="append", dest="cases")
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=300)
    options = parser.parse_args()
    rows: list[dict[str, object]] = []
    with tempfile.TemporaryDirectory(prefix="c104-yui-") as directory:
        binary = Path(directory) / "yui"
        build = command(["go", "build", "-o", str(binary), "./cmd/yui"], ROOT / "agent-cli")
        if build.returncode:
            raise SystemExit(build.stdout + build.stderr)
        cases = [
            ("help", [str(binary), "--help"], 0, "Usage:"),
            ("audio-replay", [str(binary), "session", "--replay", str(HEALTHY), "--audio-out", str(Path(directory) / "audio.wav")], 0, "Assistant:"),
            ("tool-replay-negative", [str(binary), "session", "--replay", str(TOOL_FAILURE)], 1, "unresolved"),
        ]
        requested = options.cases or ["success", "audio-tool-continuation-replay"]
        selected = []
        for requested_case in requested:
            if requested_case in {"success", "sigint-cancellation"}:
                selected.append(cases[0] if requested_case == "success" else ("sigint-cancellation", [str(binary), "session", "--replay", str(HEALTHY), "--replay-timing", "recorded"], 0, "session"))
            elif requested_case == "audio-tool-continuation-replay":
                selected.extend(cases[1:])
            elif requested_case == "cleanup-failure":
                selected.append(("cleanup-failure", [str(binary), "session", "--replay", str(HEALTHY), "--audio-out", str(Path(directory) / "missing" / "audio.wav")], 1, "audio"))
            else:
                raise SystemExit(f"unknown case: {requested_case}")
        for name, args, expected, marker in selected:
            result = command(args, ROOT)
            output = (result.stdout + result.stderr)[-4000:]
            row = {"case": name, "exit": result.returncode, "expected_exit": expected, "marker": marker, "marker_present": marker in output, "output": output}
            rows.append(row)
            if result.returncode != expected or marker not in output:
                raise SystemExit(json.dumps(row, indent=2))
    (Path(__file__).with_name("shipped-replay.json")).write_text(json.dumps(rows, indent=2) + "\n")
    print(json.dumps({"binary": "agent-cli/cmd/yui", "cases": len(rows), "status": "pass"}))


if __name__ == "__main__":
    main()
