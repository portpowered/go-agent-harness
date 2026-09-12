#!/usr/bin/env python3
"""Run bounded, fail-closed checks for the C88 retirement vertical."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Iterable


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
RUNTIME = ROOT / "go-agent-runtime"
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(ROOT)))
BASE = "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f"
INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BRANCH = "codex/audio-runtime-c88-retire-cli-room-tool-capabilities"
TASK = "audio-runtime-c88-retire-cli-room-tool-capabilities"
LEGACY = {
    "agent-cli/internal/services/internal/agentruntime/session_room_tools.go": 183,
    "agent-cli/internal/services/internal/agentruntime/session_room_browser_capabilities.go": 151,
}
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c88-retire-cli-room-tool-capabilities/"
OWNED_FILES = {
    *LEGACY,
    "agent-cli/internal/services/internal/agentruntime/session_room_tools_test.go",
    "coverage-manifest/go-agent-runtime/services/roomcapabilities/",
    "go-agent-runtime/services/roomcapabilities/",
}
EXCLUDED = {
    "agent-cli/internal/services/internal/agentruntime/session_room.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_planning.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
    "agent-cli/internal/transport/cli/room_browser_capabilities.go",
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
}


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True, timeout=20).stdout.strip()


def git_bytes(*args: str) -> bytes:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True).stdout


def ancestor(older: str, newer: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", older, newer], cwd=ROOT, check=False, timeout=20).returncode == 0


def current_status() -> str:
    return git("status", "--porcelain", "--untracked-files=all")


def physical_lines(path: Path) -> int:
    return len(path.read_bytes().splitlines())


def path_is_owned(path: str) -> bool:
    if path.startswith(OWNED_PREFIX):
        return True
    if path in OWNED_FILES:
        return True
    return any(path.startswith(prefix) for prefix in ("go-agent-runtime/services/roomcapabilities/", "coverage-manifest/go-agent-runtime/services/roomcapabilities/"))


def changed_paths() -> list[str]:
    tracked = [path for path in git("diff", BASE, "--name-only").splitlines() if path]
    untracked = []
    for line in current_status().splitlines():
        if line.startswith("?? "):
            untracked.append(line[3:])
    return sorted(set(tracked + untracked))


def check_identity_and_ancestry() -> None:
    require(git("branch", "--show-current") == BRANCH, "current branch does not match the admitted C88 branch")
    head = git("rev-parse", "HEAD")
    require(ancestor(INTEGRATION, head), f"HEAD {head} lost startup integration ancestry {INTEGRATION}")
    require(ancestor(BASE, head), f"HEAD {head} lost planning main ancestry {BASE}")
    require(ancestor(BASE, git("rev-parse", "origin/main")), "origin/main is not descended from the admitted planning main")
    control = FACTORY_ROOT / "factory/scripts/project-control.py"
    result = subprocess.run(
        ["python3", str(control), "verify-work", "--type", "task", "--name", TASK, "--root", str(FACTORY_ROOT)],
        cwd=FACTORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
        timeout=30,
    )
    require(result.returncode == 0 and '"status": "admitted"' in result.stdout, f"admission verification failed: {result.stdout.strip()} {result.stderr.strip()}")


def check_scope() -> None:
    changed = changed_paths()
    require(changed, "candidate has no changed or untracked C88 paths")
    outside = [path for path in changed if not path_is_owned(path)]
    require(not outside, f"candidate escaped C88 ownership: {outside}")
    for path in EXCLUDED:
        current = ROOT / path
        require(current.is_file(), f"excluded path disappeared: {path}")
        require(current.read_bytes() == git_bytes("show", f"{BASE}:{path}"), f"excluded path changed before lease release: {path}")


def check_retirement() -> None:
    counts = {path: physical_lines(ROOT / path) for path in LEGACY}
    total = sum(counts.values())
    baseline = sum(LEGACY.values())
    require(total <= 154, f"retired CLI production files total {total} lines; maximum is 154")
    require(baseline - total >= 180, f"retired only {baseline - total} of {baseline} baseline lines")
    require(all(count >= 1 for count in counts.values()), f"retired file disappeared: {counts}")


def check_boundary() -> None:
    runtime_files = [path for path in (RUNTIME / "services/roomcapabilities").rglob("*.go") if not path.name.endswith("_test.go")]
    runtime_text = "\n".join(path.read_text(encoding="utf-8") for path in runtime_files)
    for forbidden in ("agent-cli/internal", "internal/webmcp", "internal/room", "os.Stderr", "os.Getenv", "ConfigStorage", "func init("):
        require(forbidden not in runtime_text, f"runtime roomcapabilities boundary contains forbidden host dependency {forbidden!r}")
    tools_text = (ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_tools.go").read_text(encoding="utf-8")
    browser_text = (ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_browser_capabilities.go").read_text(encoding="utf-8")
    require("roomcapabilities/wire" in tools_text and "config.NewDefaultConfigStorage" in tools_text and "os.Stderr" in tools_text, "tools adapter no longer shows explicit host-edge delegation")
    require("roomcapabilities/wire" in browser_text and "webmcp.BrokerEvent" in browser_text and "webmcp.BrowserEvent" in browser_text, "browser adapter no longer shows explicit WebMCP adaptation")
    require("orderedRoomToolDefinitions" not in tools_text and "cloneRoomToolDefinition(" not in tools_text, "legacy CLI ordering/clone implementation remains")
    require("messages.CanonicalToolDefinitions" not in tools_text and "strings.Join" not in tools_text, "legacy CLI decision logic remains in the tools adapter")


def run_command(args: list[str], cwd: Path, *, timeout: float) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    environment["GOWORK"] = "off"
    return subprocess.run(args, cwd=cwd, env=environment, capture_output=True, text=True, timeout=timeout)


def check_mutation(label: str, needle: str, replacement: str, test_name: str) -> dict[str, str]:
    with tempfile.TemporaryDirectory(prefix=f"c88-{label}-") as directory:
        temporary_runtime = Path(directory) / "go-agent-runtime"
        shutil.copytree(RUNTIME, temporary_runtime)
        temporary_go_mod = temporary_runtime / "go.mod"
        temporary_go_mod_text = temporary_go_mod.read_text(encoding="utf-8")
        for module in ("go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway"):
            temporary_go_mod_text = temporary_go_mod_text.replace(f"=> ../{module}", f"=> {ROOT / module}")
        temporary_go_mod.write_text(temporary_go_mod_text, encoding="utf-8")
        source = temporary_runtime / "services/roomcapabilities/internal/service/service.go"
        text = source.read_text(encoding="utf-8")
        require(text.count(needle) == 1, f"{label} mutation target count is not one")
        source.write_text(text.replace(needle, replacement), encoding="utf-8")
        result = run_command(
            ["go", "test", "./services/roomcapabilities/...", "-run", f"^{test_name}$", "-count=1", "-timeout=60s"],
            temporary_runtime,
            timeout=75,
        )
        output = result.stdout + result.stderr
        require(result.returncode != 0, f"{label} mutation survived")
        require(test_name in output and "FAIL" in output, f"{label} mutation failed outside its intended oracle: {output[-2000:]}")
        return {"mutation": label, "oracle": test_name, "observed": "intended test failed"}


def check_mutations() -> list[dict[str, str]]:
    before = current_status()
    clean = run_command(
        ["go", "test", "./services/roomcapabilities/...", "-run", "^(TestValidationNegativeCasesAndValidControl|TestBrowserCompositionBaseRefreshAndDispatch)$", "-count=1", "-timeout=60s"],
        RUNTIME,
        timeout=75,
    )
    require(clean.returncode == 0, f"clean mutation oracle did not pass: {(clean.stdout + clean.stderr)[-2000:]}")
    results = [
        check_mutation(
            "unrequested-tool",
            "\t\tif _, ok := requested[definition.Name]; !ok {",
            "\t\tif false {",
            "TestValidationNegativeCasesAndValidControl",
        ),
        check_mutation(
            "refresh-drops-static",
            "refreshed, resolveErr := composeSurface(staticSnapshot.Executor, staticSnapshot.Definitions, browserSnapshot.Executor, browserDefinitions)",
            "refreshed, resolveErr := composeSurface(nil, nil, browserSnapshot.Executor, browserDefinitions)",
            "TestBrowserCompositionBaseRefreshAndDispatch",
        ),
    ]
    require(current_status() == before, "mutation runner did not restore the candidate worktree")
    return results


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["mutations", "retirement-and-adapter", "owned-and-excluded-paths", "all"], default="all")
    args = parser.parse_args()
    check_identity_and_ancestry()
    check_scope()
    if args.mode in ("retirement-and-adapter", "all"):
        check_retirement()
        check_boundary()
    mutation_results: Iterable[dict[str, str]] = []
    if args.mode in ("mutations", "all"):
        mutation_results = check_mutations()
    print(json.dumps({"task": TASK, "mode": args.mode, "passed": True, "head": git("rev-parse", "HEAD"), "changed_paths": changed_paths(), "mutations": list(mutation_results)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(f"verification failure: {error}")
        raise SystemExit(1)
