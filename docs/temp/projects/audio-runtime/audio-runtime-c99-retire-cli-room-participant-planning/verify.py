#!/usr/bin/env python3
"""Run the bounded C99 public-boundary and causal-mutation evidence checks."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile


EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["rtk", "proxy", "git", "rev-parse", "--show-toplevel"], cwd=EVIDENCE, check=True, capture_output=True, text=True).stdout.strip()).resolve()
REPORTS = EVIDENCE / "reports"
BASELINE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
BRANCH = "codex/audio-runtime-c99-retire-cli-room-participant-planning"
SOURCE = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_planning.go"
ORCHESTRATION = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go"
SHARED = ("scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json")


class VerificationFailure(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def run(command: list[str], cwd: Path = REPO_ROOT, timeout: int = 360) -> dict:
    try:
        result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        return {"command": command, "returncode": None, "timed_out": True, "stdout": error.stdout or "", "stderr": error.stderr or ""}
    return {"command": command, "returncode": result.returncode, "timed_out": False, "stdout": result.stdout, "stderr": result.stderr}


def git(*args: str) -> str:
    result = run(["rtk", "proxy", "git", *args])
    require(result["returncode"] == 0, f"git {' '.join(args)} failed: {result['stderr']}")
    return result["stdout"].strip()


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def check_command(result: dict, label: str) -> None:
    require(result["returncode"] == 0 and not result["timed_out"], f"{label} failed: {result['stderr'][-4000:]}")


def positive_matrix() -> list[dict]:
    commands = [
        ("runtime-positive", ["rtk", "go", "test", "./go-agent-runtime/services/roomplanning/...", "-count=1", "-timeout=120s"]),
        ("adapter-positive", ["rtk", "go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "Room.*(Planning|Tools|Browser|Replay|Lifecycle)|Participant.*(Plan|Ready|Connection)|BuildRoom", "-count=1", "-timeout=360s"]),
        ("external-consumer", ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=120s"], EVIDENCE / "external-consumer"),
    ]
    reports = []
    for item in commands:
        label, command, *cwd = item
        result = run(command, cwd[0] if cwd else REPO_ROOT)
        check_command(result, label)
        reports.append({"label": label, "returncode": result["returncode"], "stdout": result["stdout"][-2000:]})
    return reports


def causal_mutation(label: str, path: Path, needle: str, replacement: str, test_args: list[str]) -> dict:
    original = path.read_text(encoding="utf-8")
    require(original.count(needle) == 1, f"{label} mutation needle count is not one")
    with tempfile.TemporaryDirectory(prefix="c99-mutant-") as directory:
        mutant = Path(directory) / path.name
        mutant.write_text(original.replace(needle, replacement, 1), encoding="utf-8")
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(path): str(mutant)}}), encoding="utf-8")
        result = run(["rtk", "proxy", "go", "test", "-overlay", str(overlay), *test_args], timeout=120)
    output = result["stdout"] + result["stderr"]
    compile_markers = ("build failed", "undefined:", "syntax error", "cannot use", "no required module")
    compiled = not any(marker in output for marker in compile_markers)
    require(compiled, f"{label} did not compile as a causal mutant:\n{output[-4000:]}")
    require(result["returncode"] != 0 and not result["timed_out"], f"{label} unexpectedly passed or timed out")
    return {"label": label, "compiled": compiled, "rejected": True, "output": output[-3000:]}


def mutation_matrix() -> list[dict]:
    positive = positive_matrix()
    mutations = [
        causal_mutation(
            "replay-credential-consultation",
            REPO_ROOT / "go-agent-runtime/services/roomplanning/internal/service/service.go",
            "\tif options.ReplayPlanner == nil {\n",
            '\tif options.LookupCredential != nil {\n\t\t_, _ = options.LookupCredential("mutant-replay")\n\t}\n\tif options.ReplayPlanner == nil {\n',
            ["./go-agent-runtime/services/roomplanning", "-run", "TestPlanReplayNeverConsultsLiveSeams", "-count=1", "-timeout=30s"],
        ),
        causal_mutation(
            "admission-before-readiness",
            REPO_ROOT / "go-agent-runtime/services/roomplanning/internal/service/admission.go",
            "\t\tif allReady {\n\t\t\treturn nil\n\t\t}",
            "\t\tif allReady || true {\n\t\t\treturn nil\n\t\t}",
            ["./go-agent-runtime/services/roomplanning", "-run", "TestAwaitWaitsForReadinessAfterConnection", "-count=1", "-timeout=30s"],
        ),
        causal_mutation(
            "whole-room-local-failure",
            REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_planning_adapter.go",
            "func (c roomPlanningCoordinator) FailParticipant(id string, err error) {\n\tc.inner.failParticipant(id, err)\n}",
            "func (c roomPlanningCoordinator) FailParticipant(id string, err error) {\n\tc.inner.fail(err)\n}",
            ["./agent-cli/internal/services/internal/agentruntime", "-run", "TestRunRoom_StartupParticipantFailurePreservesViableParticipants", "-count=1", "-timeout=60s"],
        ),
    ]
    return [{"positive": positive, "mutations": mutations}]


def retirement_matrix() -> dict:
    baseline_source = subprocess.run(["rtk", "proxy", "git", "show", f"{BASELINE}:agent-cli/internal/services/internal/agentruntime/session_room_planning.go"], cwd=REPO_ROOT, check=True, capture_output=True, text=True).stdout
    current = SOURCE.read_text(encoding="utf-8")
    require(git("branch", "--show-current") == BRANCH, "branch does not match admitted PRD")
    require(len(baseline_source.splitlines()) == 529 and hashlib.sha256(baseline_source.encode()).hexdigest() == "6af84c56c1a507bc9927b15a22b28a13b514dad3d40c467a3059bd8e58b119d2", "immutable source baseline changed")
    require(len(current.splitlines()) <= 229 and current.count("Deprecated:") >= 4, "legacy planning file is not a deprecated adapter")
    require(run(["rtk", "proxy", "git", "diff", "--quiet", "origin/main", "--", str(ORCHESTRATION.relative_to(REPO_ROOT))])["returncode"] == 0, "room orchestration changed")
    for path in SHARED:
        require(run(["rtk", "proxy", "git", "diff", "--quiet", "origin/main", "--", path])["returncode"] == 0, f"leased shared path changed: {path}")
    public = "\n".join(path.read_text(encoding="utf-8") for path in (REPO_ROOT / "go-agent-runtime/services/roomplanning").glob("*.go"))
    forbidden = ("agent-cli", "internal/services/internal/agentruntime", "os." + "Getenv", "os." + "LookupEnv", "func init(", "time" + ".Sleep")
    for forbidden_text in forbidden:
        require(forbidden_text not in public, f"public roomplanning package leaks forbidden host concern: {forbidden_text}")
    diff_check = run(["rtk", "git", "diff", "--check"])
    check_command(diff_check, "diff check")
    consumer = run(["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=120s"], EVIDENCE / "external-consumer")
    check_command(consumer, "GOWORK=off external consumer")
    return {"baseline_revision": BASELINE, "baseline_lines": len(baseline_source.splitlines()), "baseline_sha256": hashlib.sha256(baseline_source.encode()).hexdigest(), "current_lines": len(current.splitlines()), "branch": BRANCH, "orchestration_unchanged": True, "shared_lease_unchanged": True, "consumer": consumer["stdout"][-2000:]}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("positive-and-three-mutations", "retirement-and-owned-paths"))
    args = parser.parse_args()
    REPORTS.mkdir(parents=True, exist_ok=True)
    try:
        if args.mode == "positive-and-three-mutations":
            report = {"schema": "audio-runtime.c99.mutations.v1", "candidate_revision": git("rev-parse", "HEAD"), "matrix": mutation_matrix()}
            output = REPORTS / "mutation-report.json"
        else:
            report = {"schema": "audio-runtime.c99.retirement.v1", "candidate_revision": git("rev-parse", "HEAD"), "report": retirement_matrix()}
            output = REPORTS / "retirement-report.json"
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(report, sort_keys=True))
        return 0
    except (OSError, subprocess.SubprocessError, VerificationFailure) as error:
        print(f"verification failed: {error}")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
