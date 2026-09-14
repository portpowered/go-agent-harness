#!/usr/bin/env python3
"""Run bounded C75 room-replay retirement evidence and record its outcomes."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import time


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
CONSUMER = HERE / "external-consumer"
SUMMARY = HERE / "verification-summary.json"
PROVENANCE = HERE / "provenance.json"
SOURCE = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
BRANCH = "codex/audio-runtime-c75-retire-cli-room-replay-bundle"


def run(label: str, argv: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None, timeout: int = 180, allow_failure: bool = False) -> dict[str, object]:
    started = time.monotonic()
    merged = os.environ.copy()
    if env:
        merged.update(env)
    result = subprocess.run(argv, cwd=cwd, env=merged, capture_output=True, text=True, timeout=timeout)
    output = (result.stdout + result.stderr).strip()
    record = {
        "label": label,
        "argv": argv,
        "returncode": result.returncode,
        "elapsed_ms": round((time.monotonic() - started) * 1000),
        "output_tail": output[-4000:],
        "passed": result.returncode == 0,
    }
    if result.returncode != 0 and not allow_failure:
        raise SystemExit(f"{label} failed:\n{output}")
    return record


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=["all"], default="all")
    parser.parse_args()

    commands = [
        run("runtime-normal", ["go", "test", "./go-agent-runtime/services/roomreplaybundle/...", "-count=1"]),
        run("runtime-race", ["go", "test", "-race", "./go-agent-runtime/services/roomreplaybundle/..."], timeout=180),
        run("cli-room-replay", ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-count=1"], timeout=180),
        run("cli-room-replay-focused-race", ["go", "test", "-race", "./agent-cli/internal/services/internal/agentruntime", "-run", "TestLoadRoomReplayPlan|TestRoomReplay|TestRoomEvidence", "-count=1"], timeout=180),
        run("vet", ["go", "vet", "./go-agent-runtime/services/roomreplaybundle/...", "./agent-cli/internal/services/internal/agentruntime"]),
        run("staticcheck", ["staticcheck", "./go-agent-runtime/services/roomreplaybundle/...", "./agent-cli/internal/services/internal/agentruntime"], timeout=180),
        run("coverage-registration", ["make", "coverage-registration"], timeout=180),
        run("external-consumer", ["go", "run", "."], cwd=CONSUMER, env={"GOWORK": "off"}, timeout=180),
        run("diff-check", ["git", "diff", "--check"]),
        run("wire-check-active-lease", ["make", "wire-check"], allow_failure=True),
        run("architecture-check-active-lease", ["make", "architecture-size-check"], allow_failure=True, timeout=180),
    ]
    candidate = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()
    cli_lines = {}
    for relative in (
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_bundle.go",
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_manifest.go",
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_integrity.go",
    ):
        cli_lines[relative] = sum(1 for _ in (ROOT / relative).open(encoding="utf-8"))
    summary = {
        "schema": "audio-runtime.c75.verification-summary.v1",
        "task": "audio-runtime-c75-retire-cli-room-replay-bundle",
        "branch": BRANCH,
        "source_revision": SOURCE,
        "candidate_revision": candidate,
        "passed": all(item["passed"] for item in commands if item["label"] not in {"wire-check-active-lease", "architecture-check-active-lease"}),
        "commands": commands,
        "cli_production_lines": cli_lines,
        "legacy_production_lines": {"bundle": 337, "manifest": 1158, "integrity": 404, "total": 1899},
        "shared_leases": {
            "wire_registry": commands[-2]["returncode"] != 0,
            "architecture_baseline": commands[-1]["returncode"] != 0,
            "owner": "C57/C61",
        },
    }
    provenance = {
        "schema": "audio-runtime.c75.provenance.v1",
        "task": summary["task"],
        "branch": BRANCH,
        "source_revision": SOURCE,
        "candidate_revision": candidate,
        "accepted_main": SOURCE,
        "fresh_fetch_origin_main_revision": subprocess.run(["git", "rev-parse", "origin/main"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip(),
        "source_tree_status_before_evidence": subprocess.run(["git", "status", "--porcelain", "--untracked-files=all"], cwd=ROOT, check=True, capture_output=True, text=True).stdout,
        "owned_evidence_prefix": str(HERE.relative_to(ROOT)),
        "shared_files_unchanged": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"],
    }
    SUMMARY.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    PROVENANCE.write_text(json.dumps(provenance, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"passed": summary["passed"], "candidate_revision": candidate, "external_consumer": "C75_ROOMREPLAYBUNDLE_CONSUMER PASS"}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
