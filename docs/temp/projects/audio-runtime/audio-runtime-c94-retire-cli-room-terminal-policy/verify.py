#!/usr/bin/env python3
"""Verify C94 ownership, physical retirement, public boundary, and mutation evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


EVIDENCE = Path(__file__).resolve().parent
ROOT = next(parent for parent in EVIDENCE.parents if (parent / "go.work").is_file())
BASELINE = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
BRANCH = "codex/audio-runtime-c94-retire-cli-room-terminal-policy"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_room_terminal.go"
ALLOWED_FILES = {
    LEGACY,
    "agent-cli/internal/services/internal/agentruntime/session_room_terminal_test.go",
}
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/roomterminal/",
    "coverage-manifest/go-agent-runtime/services/roomterminal/",
    "docs/temp/projects/audio-runtime/audio-runtime-c94-retire-cli-room-terminal-policy/",
)


class VerificationFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def git(*args: str) -> str:
    result = subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True, check=False, timeout=20)
    require(result.returncode == 0, result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def sha256(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def changed_paths() -> set[str]:
    paths = set(filter(None, git("diff", "--name-only", BASELINE).splitlines()))
    for line in git("status", "--porcelain", "--untracked-files=all").splitlines():
        if line:
            paths.add(line[2:].lstrip())
    return paths


def integrated_main_paths() -> set[str]:
    """Return accepted-main files already present in the candidate ancestry."""
    return set(filter(None, git("diff", "--name-only", BASELINE, "origin/main").splitlines()))


def verify_admission_and_scope() -> None:
    require(git("branch", "--show-current") == BRANCH, "candidate branch does not match prd.branchName")
    allowed = integrated_main_paths() | {
        path for path in changed_paths() if path in ALLOWED_FILES or path.startswith(ALLOWED_PREFIXES)
    }
    unexpected = changed_paths() - allowed
    require(not unexpected, f"candidate changed an unowned path: {sorted(unexpected)}")
    baseline = subprocess.run(["git", "-C", str(ROOT), "show", f"{BASELINE}:{LEGACY}"], capture_output=True, check=False)
    require(baseline.returncode == 0, "planning baseline legacy file is unavailable")
    require(len(baseline.stdout.splitlines()) == 234 and sha256(baseline.stdout) == "50c9c02f61a822c5e20c97dad59b8627c171f9ba85a85d2bb3d496fb2b760b57", "legacy baseline is not the admitted 234-line source")
    current = (ROOT / LEGACY).read_bytes()
    require(len(current.splitlines()) <= 84, f"legacy adapter is {len(current.splitlines())} lines, want <=84")
    require("Deprecated" in current.decode(errors="replace"), "legacy adapter does not identify retained compatibility symbols")
    for revision in ("8bdafc7f947a3a2c9856220abdc539437035bd21", git("rev-parse", "origin/main")):
        result = subprocess.run(["git", "-C", str(ROOT), "merge-base", "--is-ancestor", revision, "HEAD"], check=False)
        require(result.returncode == 0, f"candidate is missing required ancestry {revision}")


def verify_source_boundary() -> None:
    public = (ROOT / "go-agent-runtime/services/roomterminal/contract.go").read_text(encoding="utf-8")
    private = (ROOT / "go-agent-runtime/services/roomterminal/internal/service/service.go").read_text(encoding="utf-8")
    wire = (ROOT / "go-agent-runtime/services/roomterminal/wire/wire.go").read_text(encoding="utf-8")
    generated = (ROOT / "go-agent-runtime/services/roomterminal/wire/wire_gen.go").read_text(encoding="utf-8")
    require("agent-cli" not in public and "flag" not in public and "os.Getenv" not in public, "public contract imports host concerns")
    require("agent-cli" not in private and "os.Getenv" not in private and "os.LookupEnv" not in private, "private policy imports host concerns")
    require("agent-cli" not in wire and "os.Getenv" not in wire and "os.LookupEnv" not in wire and "func init(" not in wire, "Wire imports host concerns or hidden initialization")
    require("wire.Build" in wire and "func NewService" in wire and "func NewService" in generated, "dedicated Wire graph is incomplete")
    require("switch messages.TerminalReason" in private and "func (s *Service) Provenance" in private and "func (s *Service) BoundTrigger" in private and "func (s *Service) RecordBound" in private, "private implementation does not own extracted decisions")
    for path in (ROOT / "go-agent-runtime/services/roomterminal").rglob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        require("agent-cli" not in text and "os.Getenv" not in text and "os.LookupEnv" not in text and "time.Sleep" not in text, f"roomterminal boundary leaked host state: {path}")
    cli = (ROOT / LEGACY).read_text(encoding="utf-8")
    for forbidden in ("provider_authored_completion", "loop_synthesized_completion", "max_duration_reached_mid_response", "strings.HasPrefix", "providers.ErrorClass"):
        require(forbidden not in cli, f"legacy adapter retained terminal policy literal {forbidden}")
    imports = set(re.findall(r'"([^"]+)"', (EVIDENCE / "external-consumer/main.go").read_text(encoding="utf-8")))
    runtime_imports = {path for path in imports if path.startswith("github.com/portpowered/go-agent-harness/")}
    require(runtime_imports <= {"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages", "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal", "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal/wire"}, f"external consumer imports private or host runtime paths: {sorted(runtime_imports)}")


def run_consumer() -> None:
    environment = {key: value for key, value in dict(os.environ).items() if not key.upper().endswith("_API_KEY")}
    environment["GOWORK"] = "off"
    command = ["rtk", "proxy", "env", "GOWORK=off", "go", "run", "."]
    positive = subprocess.run(command, cwd=EVIDENCE / "external-consumer", capture_output=True, text=True, timeout=120, env=environment)
    require(positive.returncode == 0 and "C94_ROOM_TERMINAL_EXTERNAL" in positive.stdout and '"status":"ok"' in positive.stdout, f"external positive failed: {positive.stderr or positive.stdout}")
    mutation = subprocess.run(command + ["wrong-oracle"], cwd=EVIDENCE / "external-consumer", capture_output=True, text=True, timeout=120, env=environment)
    require(mutation.returncode != 0 and "wrong-oracle assertion" in mutation.stderr + mutation.stdout, "causal mutation did not compile and fail its semantic oracle")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("retirement-owned-paths-and-mutation",))
    args = parser.parse_args()
    verify_admission_and_scope()
    verify_source_boundary()
    run_consumer()
    print(json.dumps({"schema": "audio-runtime.c94.verification.v1", "passed": True, "mode": args.mode, "legacy_lines": len((ROOT / LEGACY).read_text(encoding="utf-8").splitlines()), "baseline_lines": 234, "mutation": "compiled_and_rejected"}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except VerificationFailure as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
