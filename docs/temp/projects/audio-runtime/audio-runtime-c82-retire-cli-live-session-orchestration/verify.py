#!/usr/bin/env python3
"""Bounded C82 behavior, mutation, retirement, and scope verification."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


EVIDENCE_ROOT = Path(__file__).resolve().parent
REPO_ROOT = next(parent for parent in EVIDENCE_ROOT.parents if (parent / "go.work").is_file())
BASE_REVISION = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
EXPECTED_BRANCH = "codex/audio-runtime-c82-retire-cli-live-session-orchestration"
CLI_FILES = {
    "agent-cli/internal/services/internal/agentruntime/session_live.go",
    "agent-cli/internal/services/internal/agentruntime/session_live_setup.go",
}
ALLOWED_PREFIXES = (
    "go-agent-runtime/services/sessionlive/",
    "coverage-manifest/go-agent-runtime/services/sessionlive/",
    "docs/temp/projects/audio-runtime/audio-runtime-c82-retire-cli-live-session-orchestration/",
)
ALLOWED_FILES = CLI_FILES | {"docs/architecture/architecture-policy.json"}
EXCLUDED = (
    "session_runtime_plan.go",
    "session_room_coordinator.go",
    "session_options.go",
    "session_tools.go",
    "session_diagnostics",
    "rtc_device_runtime.go",
    "go-agent-runtime/services/session/internal/live/lifecycle.go",
    "docs/architecture/architecture-size-baseline.json",
    "scripts/wire-packages.txt",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def command_result(argv: list[str], cwd: Path = REPO_ROOT, timeout: float = 120, env: dict[str, str] | None = None) -> dict:
    process_env = os.environ.copy()
    if env:
        process_env.update(env)
    try:
        completed = subprocess.run(argv, cwd=cwd, env=process_env, capture_output=True, text=True, timeout=timeout, check=False)
        output = (completed.stdout + completed.stderr).rstrip()
        return {"argv": argv, "returncode": completed.returncode, "timed_out": False, "output": output[-65536:]}
    except subprocess.TimeoutExpired as error:
        output = (error.stdout or "") + (error.stderr or "")
        return {"argv": argv, "returncode": None, "timed_out": True, "output": str(output)[-65536:]}


def checked(argv: list[str], cwd: Path = REPO_ROOT, timeout: float = 120, env: dict[str, str] | None = None) -> dict:
    result = command_result(argv, cwd, timeout, env)
    require(result["returncode"] == 0 and not result["timed_out"], f"command failed: {' '.join(argv)}\n{result['output']}")
    return result


def git(*args: str) -> str:
    result = checked(["git", *args], timeout=30)
    return result["output"]


def physical_lines(path: Path) -> int:
    data = path.read_bytes()
    return data.count(b"\n") + (0 if not data or data.endswith(b"\n") else 1)


def base_lines(path: str) -> int:
    data = subprocess.run(["git", "show", f"{BASE_REVISION}:{path}"], cwd=REPO_ROOT, capture_output=True, check=True).stdout
    return data.count(b"\n") + (0 if data.endswith(b"\n") else 1)


def status_paths() -> list[str]:
    status = git("status", "--porcelain", "--untracked-files=all")
    return [line[3:] for line in status.splitlines() if len(line) >= 4]


def verify_scope() -> dict:
    require(git("branch", "--show-current") == EXPECTED_BRANCH, "candidate branch does not match the admitted branch")
    changed = status_paths()
    for path in changed:
        require(path in ALLOWED_FILES or any(path.startswith(prefix) for prefix in ALLOWED_PREFIXES), f"out-of-scope changed path: {path}")
        require(not any(item in path for item in EXCLUDED), f"excluded path changed: {path}")
    checked(["git", "diff", "--check"], timeout=30)
    baseline = {
        "session_live.go": base_lines("agent-cli/internal/services/internal/agentruntime/session_live.go"),
        "session_live_setup.go": base_lines("agent-cli/internal/services/internal/agentruntime/session_live_setup.go"),
    }
    current = {
        "session_live.go": physical_lines(REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_live.go"),
        "session_live_setup.go": physical_lines(REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_live_setup.go"),
    }
    require(baseline == {"session_live.go": 1061, "session_live_setup.go": 72}, f"baseline census changed: {baseline}")
    total = sum(current.values())
    require(total <= 633, f"CLI live adapter is {total} lines, ceiling is 633")
    retired = sum(baseline.values()) - total
    require(retired >= 500, f"retirement is {retired} lines, floor is 500")
    service_root = REPO_ROOT / "go-agent-runtime/services/sessionlive"
    forbidden = ("agent-cli", "internal/services/internal/agentruntime", "func init(", "os.Getenv", "os.LookupEnv", "session_runtime_plan", "session_room_coordinator", "session_options", "session_tools", "session_diagnostics", "rtc_device_runtime")
    for path in service_root.rglob("*.go"):
        source = path.read_text(encoding="utf-8")
        require(not any(token in source for token in forbidden), f"host or excluded dependency leaked into {path.relative_to(REPO_ROOT)}")
    return {"baseline": baseline, "current": current, "retired": retired, "changed": changed}


def verify_behavior() -> list[dict]:
    results = [
        checked(["go", "test", "./go-agent-runtime/services/sessionlive/...", "-count=1", "-timeout=120s"], timeout=150),
        checked(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "RunAgentLoopSessionTerminalOutcomesAlwaysDrainAcceptedDelta|ScheduledAudio|SessionProgressObserver", "-count=1", "-timeout=300s"], timeout=330),
    ]
    consumer = EVIDENCE_ROOT / "external-consumer"
    results.append(checked(["go", "test", "./...", "-count=1", "-timeout=120s"], cwd=consumer, timeout=150, env={"GOWORK": "off"}))
    return results


def mutation_workspace() -> tuple[Path, tempfile.TemporaryDirectory[str]]:
    holder = tempfile.TemporaryDirectory(prefix="c82-mutation-")
    worktree = Path(holder.name) / "repo"
    checked(["git", "worktree", "add", "--detach", str(worktree), "HEAD"], timeout=60)
    source = REPO_ROOT / "go-agent-runtime/services/sessionlive"
    target = worktree / "go-agent-runtime/services/sessionlive"
    shutil.copytree(source, target, dirs_exist_ok=True)
    shutil.copy2(REPO_ROOT / "go.work", worktree / "go.work")
    if (REPO_ROOT / "go.work.sum").is_file():
        shutil.copy2(REPO_ROOT / "go.work.sum", worktree / "go.work.sum")
    return worktree, holder


def verify_mutation(kind: str) -> dict:
    pattern = "TestFlushPublishedDrainsAcceptedDelta|TestRunStopsDeadlineTimer"
    positive = checked(["go", "test", "./go-agent-runtime/services/sessionlive/internal/service", "-run", pattern, "-count=1", "-timeout=60s"], timeout=90)
    discovery = checked(["go", "test", "./go-agent-runtime/services/sessionlive/internal/service", "-list", pattern], timeout=90)
    require("TestFlushPublishedDrainsAcceptedDelta" in discovery["output"] and "TestRunStopsDeadlineTimer" in discovery["output"], "mutation controls did not prove test discovery")
    worktree, holder = mutation_workspace()
    try:
        if kind == "mutation-post-done-drain":
            path = worktree / "go-agent-runtime/services/sessionlive/internal/service/termination.go"
            source = path.read_text(encoding="utf-8")
            needle = "func flushPublished(ctx context.Context, opts sessionlive.RunOptions, loop *sessionlive.Loop) error {\n\tfor {"
            require(needle in source, "post-done drain mutation anchor changed")
            path.write_text(source.replace(needle, needle.replace("\n\tfor {", "\n\tfor false {"), 1), encoding="utf-8")
        else:
            path = worktree / "go-agent-runtime/services/sessionlive/internal/service/run.go"
            source = path.read_text(encoding="utf-8")
            needle = "\t\tr.stopDeadline()"
            require(needle in source, "deadline cleanup mutation anchor changed")
            path.write_text(source.replace(needle, "\t\tr.stopDeadline = nil", 1), encoding="utf-8")
        mutated = command_result(["go", "test", "./services/sessionlive/internal/service", "-run", pattern, "-count=1", "-timeout=60s"], cwd=worktree / "go-agent-runtime", timeout=90, env={"GOWORK": "off"})
        output = mutated["output"].lower()
        require(mutated["returncode"] not in (None, 0) and not mutated["timed_out"], f"{kind} unexpectedly passed or timed out: {mutated}")
        require("fail" in output and "no tests to run" not in output and "build failed" not in output and "workspace modules" not in output and "updates to go.mod needed" not in output, f"{kind} failed without exercising the behavioral oracle: {mutated['output']}")
        return {"positive": positive, "discovery": discovery, "mutation": mutated}
    finally:
        command_result(["git", "worktree", "remove", "--force", str(worktree)], timeout=60)
        holder.cleanup()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["behavior-matrix", "retirement-and-scope", "mutation-post-done-drain", "mutation-deadline-cleanup"])
    parser.add_argument("--expect-failure", action="store_true")
    args = parser.parse_args()
    if args.mode == "behavior-matrix":
        payload = {"mode": args.mode, "passed": True, "commands": verify_behavior()}
    elif args.mode == "retirement-and-scope":
        payload = {"mode": args.mode, "passed": True, **verify_scope()}
    else:
        payload = {"mode": args.mode, "passed": True, **verify_mutation(args.mode)}
    print(json.dumps(payload, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.CalledProcessError) as error:
        print(f"verification failure: {error}")
        raise SystemExit(1)
