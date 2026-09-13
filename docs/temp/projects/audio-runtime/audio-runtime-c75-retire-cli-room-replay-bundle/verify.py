#!/usr/bin/env python3
"""Verify committed C75 evidence without rerunning the expensive tests."""

from __future__ import annotations

import json
from pathlib import Path
import subprocess


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
SOURCE = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
BRANCH = "codex/audio-runtime-c75-retire-cli-room-replay-bundle"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def git(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


def main() -> int:
    summary = json.loads((HERE / "verification-summary.json").read_text(encoding="utf-8"))
    provenance = json.loads((HERE / "provenance.json").read_text(encoding="utf-8"))
    require(summary["schema"] == "audio-runtime.c75.verification-summary.v1", "wrong verification schema")
    require(provenance["schema"] == "audio-runtime.c75.provenance.v1", "wrong provenance schema")
    require(git("branch", "--show-current") == BRANCH, "wrong admitted branch")
    head = git("rev-parse", "HEAD")
    candidate = provenance["candidate_revision"]
    require(git("merge-base", "--is-ancestor", candidate, head) == "", "final head does not descend from evidence candidate")
    require(summary["source_revision"] == SOURCE and provenance["source_revision"] == SOURCE, "source revision drifted")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", SOURCE, head], cwd=ROOT).returncode == 0, "source is not an ancestor")
    require(summary["passed"] is True, "required C75 evidence did not pass")
    for command in summary["commands"]:
        if command["label"] in {"wire-check-active-lease", "architecture-check-active-lease"}:
            continue
        require(command["passed"] is True, f"evidence command failed: {command['label']}")
    require(summary["cli_production_lines"] == {
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_bundle.go": 88,
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_manifest.go": 219,
        "agent-cli/internal/services/internal/agentruntime/session_room_replay_integrity.go": 6,
    }, "CLI retirement line census changed")
    retired = 1899 - sum(summary["cli_production_lines"].values())
    require(retired >= 1200, f"retired production lines below threshold: {retired}")
    require(summary["shared_leases"] == {"architecture_baseline": True, "owner": "C57/C61", "wire_registry": True}, "shared lease evidence changed")
    require("C75_ROOMREPLAYBUNDLE_CONSUMER PASS" in next(item["output_tail"] for item in summary["commands"] if item["label"] == "external-consumer"), "consumer marker missing")
    changed = set(git("diff", f"{SOURCE}...{head}", "--name-only").splitlines())
    require("scripts/wire-packages.txt" not in changed and "docs/architecture/architecture-size-baseline.json" not in changed, "peer-owned shared file changed")
    print(json.dumps({"schema": "audio-runtime.c75.final-gate.v1", "passed": True, "candidate_revision": candidate, "head": head}, sort_keys=True))
    return 0


if __name__ == "__main__":
    main()
