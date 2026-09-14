#!/usr/bin/env python3
"""Bounded C80 service and retirement checks.

The negative modes succeed only when the named negative assertion is reached;
they do not invert an arbitrary command failure.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys


PROJECT = Path(__file__).resolve().parent


def repo_root() -> Path:
    for candidate in (PROJECT, *PROJECT.parents):
        if (candidate / "go.work").is_file():
            return candidate
    raise RuntimeError("could not locate repository go.work")


ROOT = repo_root()
CONSUMER = PROJECT / "external-consumer"


def run(command: list[str], cwd: Path, timeout: int) -> None:
    env = os.environ.copy()
    if cwd == CONSUMER:
        env["GOWORK"] = "off"
    try:
        completed = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        raise SystemExit(f"timeout after {timeout}s: {' '.join(command)}") from exc
    if completed.returncode:
        sys.stderr.write(completed.stdout)
        sys.stderr.write(completed.stderr)
        raise SystemExit(f"command failed ({completed.returncode}): {' '.join(command)}")
    sys.stdout.write(completed.stdout)
    sys.stdout.write(completed.stderr)


def consumer_test(name: str) -> None:
    run(["go", "test", "./...", "-run", f"^{name}$", "-count=1", "-timeout=90s"], CONSUMER, 120)


def evidence_matrix() -> None:
    run(
        ["go", "test", "./go-agent-runtime/services/roomevidence/...", "-run", "Service|Wire|Integrity|Error|Finalize", "-count=1", "-timeout=240s"],
        ROOT,
        300,
    )
    run(["go", "test", "./...", "-count=1", "-timeout=90s"], CONSUMER, 120)


def retirement_and_scope() -> None:
    owned = [
        ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_evidence.go",
        ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_evidence_capture.go",
        ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_evidence_integrity.go",
    ]
    line_count = sum(len(path.read_text(encoding="utf-8").splitlines()) for path in owned)
    if line_count > 994:
        raise SystemExit(f"retired CLI evidence files have {line_count} lines, limit is 994")
    adapter_text = owned[0].read_text(encoding="utf-8")
    if "compatibility adapter" not in adapter_text:
        raise SystemExit("legacy evidence adapter is not documented")
    runtime_files = list((ROOT / "go-agent-runtime/services/roomevidence").rglob("*.go"))
    forbidden = re.compile(r"agent-cli|internal/services/internal/agentruntime|func init\(|os\.(Getenv|LookupEnv)|time\.Sleep")
    for path in runtime_files:
        match = forbidden.search(path.read_text(encoding="utf-8"))
        if match:
            raise SystemExit(f"forbidden host dependency {match.group(0)!r} in {path}")
    project_imports = set()
    for path in CONSUMER.glob("*.go"):
        for value in re.findall(r'"(github\.com/portpowered/go-agent-harness/[^\"]+)"', path.read_text(encoding="utf-8")):
            project_imports.add(value)
    allowed = {
        "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence",
        "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire",
    }
    if project_imports != allowed:
        raise SystemExit(f"consumer project imports = {sorted(project_imports)}, want {sorted(allowed)}")
    shared = {
        "scripts/wire-packages.txt",
        "docs/architecture/architecture-size-baseline.json",
    }
    status = subprocess.run(["git", "status", "--short"], cwd=ROOT, text=True, capture_output=True, check=True).stdout
    touched = {line[3:].strip() for line in status.splitlines() if len(line) >= 4}
    if touched & shared:
        raise SystemExit(f"leased shared files changed: {sorted(touched & shared)}")
    print(f"retirement lines={line_count}; shared leases untouched; consumer imports are public-only")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=[
        "evidence-matrix",
        "negative-traversal-symlink",
        "negative-post-finalize",
        "negative-artifact-mutation",
        "negative-redaction-bounds",
        "retirement-and-scope",
    ])
    parser.add_argument("--expect-failure", action="store_true", help="document that the selected mode is a negative control")
    args = parser.parse_args()
    if args.mode == "evidence-matrix":
        evidence_matrix()
    elif args.mode == "retirement-and-scope":
        retirement_and_scope()
    else:
        names = {
            "negative-traversal-symlink": "TestExternalConsumerRejectsSymlinkTarget",
            "negative-post-finalize": "TestExternalConsumerUsesOnlyPublicRoomEvidencePorts",
            "negative-artifact-mutation": "TestExternalConsumerDetectsSameLengthArtifactMutation",
            "negative-redaction-bounds": "TestExternalConsumerRedactsAndBoundsDiagnosticRecords",
        }
        consumer_test(names[args.mode])
        print(f"negative control reached: {args.mode}")


if __name__ == "__main__":
    main()
