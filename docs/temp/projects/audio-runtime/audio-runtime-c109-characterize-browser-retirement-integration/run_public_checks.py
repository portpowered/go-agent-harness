#!/usr/bin/env python3
"""Run bounded software-only C109 checks in an exact synthetic merge tree."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any

from analyze import C61, C83, FIXED_DATE, run as run_command, revision, status_lines, tree


ROOT = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()
MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
AGENT_LOOP_MODULE = "github.com/portpowered/go-agent-harness/go-agent-loop"
AGENT_RUNTIME_MODULE = "github.com/portpowered/go-agent-harness/go-agent-runtime"
AGENT_CLI_MODULE = "github.com/portpowered/go-agent-harness/agent-cli"
C61_DIR = "docs/temp/projects/audio-runtime/audio-runtime-c61-retire-cli-browser-scenario-contract"
C83_DIR = "docs/temp/projects/audio-runtime/audio-runtime-c83-retire-cli-browser-scenario-runner"
SHIPPED_REPORT = f"{C61_DIR}/runs/shipped-yui-browser-audio-tool-replay.json"
OUTPUT_CAP = 2 * 1024 * 1024
SHIPPED_OUTPUT_MARKERS = ("PROBE_TOOL_MARKER_9182", "strict replay continuation")


class PublicCheckError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise PublicCheckError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_environment(extra: dict[str, str] | None = None) -> tuple[dict[str, str], list[str]]:
    markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    env = {
        name: value
        for name, value in os.environ.items()
        if not any(marker in name.upper() for marker in markers)
    }
    removed = sorted(set(os.environ) - set(env))
    if extra:
        require(not any(any(marker in name.upper() for marker in markers) for name in extra), "credential-bearing override is forbidden")
        env.update(extra)
    return env, removed


def append_output(left: str, right: str | bytes | None) -> str:
    if right is None:
        return left
    if isinstance(right, bytes):
        right = right.decode(errors="replace")
    return left + right


def terminate_group(process: subprocess.Popen[str]) -> dict[str, Any]:
    result = {"term_sent": False, "kill_sent": False, "wait_status": "not-needed"}
    if process.poll() is not None:
        result["wait_status"] = "already-exited"
        return result
    try:
        os.killpg(process.pid, signal.SIGTERM)
        result["term_sent"] = True
    except ProcessLookupError:
        result["wait_status"] = "group-already-gone"
        return result
    try:
        process.wait(timeout=1)
        result["wait_status"] = "terminated-after-term"
        return result
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(process.pid, signal.SIGKILL)
        result["kill_sent"] = True
    except ProcessLookupError:
        result["wait_status"] = "group-gone-before-kill"
        return result
    try:
        process.wait(timeout=2)
        result["wait_status"] = "terminated-after-kill"
    except subprocess.TimeoutExpired:
        result["wait_status"] = "wait-timeout"
    return result


def process_group_gone(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except OSError as exc:
        return exc.errno == 3
    return False


def test_discovery(command: str, output: str) -> dict[str, Any]:
    required = "go test" in command
    if not required:
        return {"required": False, "tests_discovered": None, "package_ok_lines": 0, "pass_lines": 0}
    package_ok_lines = len(re.findall(r"(?m)^ok\s+\S+", output))
    pass_lines = len(re.findall(r"(?m)^--- PASS:", output))
    no_test_packages = len(re.findall(r"(?m)^\?\s+\S+.*\[no test files\]", output))
    return {
        "required": True,
        "tests_discovered": pass_lines or package_ok_lines,
        "package_ok_lines": package_ok_lines,
        "pass_lines": pass_lines,
        "no_test_packages": no_test_packages,
    }


def bounded(argv: list[str], cwd: Path, timeout: int, *, env_extra: dict[str, str] | None = None, label: str) -> dict[str, Any]:
    env, removed = safe_environment(env_extra)
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    output = ""
    timed_out = False
    cleanup = {"term_sent": False, "kill_sent": False, "wait_status": "not-needed", "drain_status": "not-needed"}
    try:
        output, _ = process.communicate(timeout=max(1, timeout))
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        output = append_output(output, exc.stdout)
        cleanup.update(terminate_group(process))
        try:
            tail, _ = process.communicate(timeout=1)
            output = append_output(output, tail)
            cleanup["drain_status"] = "drained-after-termination"
        except subprocess.TimeoutExpired as drain_exc:
            output = append_output(output, drain_exc.stdout)
            cleanup["drain_status"] = "bounded-drain-timeout"
            if process.stdout is not None:
                process.stdout.close()
    elapsed = round(time.monotonic() - started, 3)
    process_group_clean = process_group_gone(process.pid)
    output_bytes = len(output.encode())
    output_capped = output_bytes > OUTPUT_CAP
    if output_capped:
        output = output[-16000:]
    forbidden_output_markers = ("OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "C109_SECRET_MARKER", "PROBE_CREDENTIAL_MARKER")
    discovery = test_discovery(" ".join(argv), output)
    status = "passed" if process.returncode == 0 and not timed_out and process_group_clean and not output_capped else ("timeout" if timed_out else "failed")
    return {
        "label": label,
        "command": " ".join(argv),
        "working_tree": "synthetic-required-merge-tree",
        "status": status,
        "exit_code": process.returncode,
        "timeout_seconds": timeout,
        "elapsed_seconds": elapsed,
        "timed_out": timed_out,
        "process_group_gone": process_group_clean,
        "cleanup": cleanup,
        "output_bytes": output_bytes,
        "output_sha256": hashlib.sha256(output.encode()).hexdigest(),
        "output_capped": output_capped,
        "credential_environment_scrubbed": True,
        "removed_credential_environment_names": removed,
        "credential_output_markers_absent": not any(marker in output for marker in forbidden_output_markers),
        "test_discovery": discovery,
        "output": output,
    }


def merge_parents(tree_root: Path, ref: str) -> list[str]:
    result = run_command(["git", "rev-list", "--parents", "-n", "1", ref], cwd=tree_root)
    return result["output"].strip().split()


def synthetic_tree_binding(tree_root: Path, expected_tree: str | None = None) -> dict[str, Any]:
    require(not status_lines(ROOT, cwd=tree_root), "synthetic tree is dirty before public checks")
    head = revision(ROOT, "HEAD", cwd=tree_root)
    final_parents = merge_parents(tree_root, "HEAD")
    require(len(final_parents) == 3, "synthetic HEAD is not the second merge commit")
    c61_merge = final_parents[1]
    c83_parents = final_parents
    first_parents = merge_parents(tree_root, c61_merge)
    require(len(first_parents) == 3, "synthetic first merge is not a merge commit")
    require(first_parents[1] == MAIN and first_parents[2] == C61, "synthetic first merge does not bind main then C61")
    require(c83_parents[1] == c61_merge and c83_parents[2] == C83, "synthetic second merge does not bind C83 after C61")
    actual_tree = tree(ROOT, "HEAD", cwd=tree_root)
    if expected_tree is not None:
        require(actual_tree == expected_tree, "provided tree is not the exact required synthetic tree")
    return {
        "valid": True,
        "base": MAIN,
        "candidate_commits": [C61, C83],
        "first_parent_order": ["main", "c61", "c83"],
        "head": head,
        "merge_commits": [c61_merge, head],
        "parents": {"c61_merge": first_parents, "c83_merge": final_parents},
        "final_tree": actual_tree,
    }


def create_required_tree() -> tuple[Path, Path, str]:
    temporary_root = Path(tempfile.mkdtemp(prefix="c109-public-")).resolve()
    worktree = temporary_root / "tree"
    fixed_env = {
        "GIT_AUTHOR_NAME": "C109 Rehearsal",
        "GIT_AUTHOR_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_COMMITTER_NAME": "C109 Rehearsal",
        "GIT_COMMITTER_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_AUTHOR_DATE": FIXED_DATE,
        "GIT_COMMITTER_DATE": FIXED_DATE,
        "GIT_MERGE_AUTOEDIT": "no",
        "LC_ALL": "C",
    }
    try:
        result = run_command(["git", "worktree", "add", "--detach", str(worktree), MAIN], cwd=ROOT)
        require(result["status"] == "passed", "cannot create public synthetic tree")
        for ref, label in ((C61, "c61"), (C83, "c83")):
            run_command(["git", "merge", "--no-ff", "--no-commit", ref], cwd=worktree, env=fixed_env)
            run_command(["git", "commit", "--no-verify", "-m", f"C109 public required merge {label}"], cwd=worktree, env=fixed_env)
        binding = synthetic_tree_binding(worktree)
        return temporary_root, worktree, binding["final_tree"]
    except Exception:
        if worktree.exists():
            run_command(["git", "merge", "--abort"], cwd=worktree, check=False)
            run_command(["git", "worktree", "remove", "--force", str(worktree)], cwd=ROOT, check=False)
        if temporary_root.exists():
            for child in temporary_root.iterdir():
                if child.is_dir():
                    shutil.rmtree(child, ignore_errors=True)
            try:
                temporary_root.rmdir()
            except OSError:
                pass
        raise


def normalize_detail(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: normalize_detail(item) for key, item in value.items()}
    if isinstance(value, list):
        return [normalize_detail(item) for item in value]
    if isinstance(value, str):
        value = re.sub(r"/(?:[^/\\\s]+/)*audio-runtime-c61-yui-[^/\\\s]+", "/<replay-temp>", value)
    return value


def attach_workflow_detail(check: dict[str, Any], tree_root: Path) -> None:
    report_path = tree_root / SHIPPED_REPORT
    require(report_path.is_file(), "shipped workflow did not emit its detailed report")
    detail = normalize_detail(json.loads(report_path.read_text(encoding="utf-8")))
    serialized = json.dumps(detail, sort_keys=True)
    forbidden = ("OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "C109_SECRET_MARKER", "PROBE_CREDENTIAL_MARKER")
    require(not any(marker in serialized for marker in forbidden), "shipped detailed report contains a credential marker")
    check["workflow_report"] = detail
    check["workflow_report_sha256"] = hashlib.sha256(serialized.encode()).hexdigest()
    check["observable_effects"] = {
        "output_markers": list(SHIPPED_OUTPUT_MARKERS),
        "output_markers_asserted_by": "C61 run.py replay assertions before report emission",
        "recorded_audio_bytes": detail.get("recorded_pcm_bytes"),
        "recorded_audio_sha256": detail.get("recorded_pcm_sha256"),
        "rendered_audio_bytes": detail.get("audio_bytes"),
        "rendered_audio_sha256": detail.get("audio_sha256"),
        "terminal": detail.get("terminal_manifest"),
        "process_group_gone": detail.get("replay", {}).get("process_group_gone"),
        "classification": detail.get("result_classification"),
    }
    require(check["observable_effects"]["output_markers"] == list(SHIPPED_OUTPUT_MARKERS), "shipped observable output markers are missing")


def command_set(case: str, tree_root: Path, child_timeout: int) -> list[tuple[list[str], Path, dict[str, str], str, int]]:
    runtime = tree_root / "go-agent-runtime"
    cli = tree_root / "agent-cli"
    loop = tree_root / "go-agent-loop"
    c61 = tree_root / C61_DIR
    c83 = tree_root / C83_DIR / "external-consumer"
    package_pattern = "Browser|Scenario|Runner|Order|Interrupt|Cancel|Terminal"
    coverpkg = ",".join(
        (
            f"{AGENT_CLI_MODULE}/internal/services/internal/agentruntime",
            f"{AGENT_RUNTIME_MODULE}/services/browserscenario/...",
            f"{AGENT_RUNTIME_MODULE}/services/browserrunner/...",
        )
    )
    if case == "browser-audio-tool":
        return [
            (["go", "test", "./services/browserscenario/...", "./services/browserrunner/...", "-run", package_pattern, "-count=1", "-timeout=300s"], runtime, {}, "browserrunner+browserscenario normal", 300),
            (["go", "test", "-race", "./services/browserscenario/...", "./services/browserrunner/...", "-run", package_pattern, "-count=1", "-timeout=420s"], runtime, {}, "browserrunner+browserscenario race", 420),
            (["go", "test", "./...", "-count=1", "-timeout=180s"], c61 / "consumer", {"GOWORK": "off"}, "C61 external consumer normal GOWORK=off", 180),
            (["go", "test", "-race", "./...", "-count=1", "-timeout=240s"], c61 / "consumer", {"GOWORK": "off"}, "C61 external consumer race GOWORK=off", 240),
            (["go", "test", "./...", "-count=1", "-timeout=180s"], c83, {"GOWORK": "off"}, "C83 external consumer normal GOWORK=off", 180),
            (["go", "test", "-race", "./...", "-count=1", "-timeout=240s"], c83, {"GOWORK": "off"}, "C83 external consumer race GOWORK=off", 240),
            (["go", "test", "./pkg/agentloop", "-run", "^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$", "-count=3", "-timeout=120s"], loop, {}, "accepted-main delta barrier regression", 120),
            (["go", "test", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=180s"], cli, {}, "CLI cancellation normal", 180),
            (["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=240s"], cli, {}, "CLI cancellation race", 240),
            (["go", "test", "-tags=nomicrophone", "-count=10", "-timeout=300s", f"-coverpkg={coverpkg}", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation"], cli, {"CGO_ENABLED": "0", "GOWORK": "off"}, "nomicrophone cross-module coverpkg cancellation", 300),
            (["bash", "scripts/test-session-ci-regressions.sh", "all"], tree_root, {"COUNT": "1"}, "accumulated session CI regressions all", 480),
            (["python3", f"{C61_DIR}/run.py", "--mode", "shipped-yui-browser-audio-tool-replay"], tree_root, {}, "shipped credential-free browser/audio/tool workflow", 180),
        ]
    if case == "malformed-or-canceled":
        return [
            (["go", "run", f"{C61_DIR}/verify.go", "--mode", "malformed-credential-overflow-timeout-cleanup"], tree_root, {}, "malformed credential overflow timeout cleanup", 180),
            (["go", "test", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=180s"], cli, {}, "canceled browser scenario normal", 180),
            (["go", "test", "-race", "./internal/services/internal/agentruntime", "-run", "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab|TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation", "-count=3", "-timeout=240s"], cli, {}, "canceled browser scenario race", 240),
        ]
    raise PublicCheckError(f"unsupported case: {case}")


def cleanup_tree(temporary_root: Path | None, worktree: Path) -> dict[str, Any]:
    if temporary_root is None:
        return {
            "attempted": True,
            "status": "passed" if worktree.exists() else "failed",
            "mode": "caller-owned-tree-preserved",
            "worktree_removed": False,
            "temporary_root_removed": True,
            "tree_preserved": worktree.exists(),
        }
    remove = run_command(["git", "worktree", "remove", "--force", str(worktree)], cwd=ROOT, check=False)
    worktree_removed = not worktree.exists()
    temporary_root_removed = False
    if temporary_root.exists():
        try:
            temporary_root.rmdir()
            temporary_root_removed = True
        except OSError:
            temporary_root_removed = False
    else:
        temporary_root_removed = True
    status = "passed" if remove["status"] == "passed" and worktree_removed and temporary_root_removed else "failed"
    return {
        "attempted": True,
        "status": status,
        "mode": "task-owned-temporary-tree",
        "remove_command": "git worktree remove --force <temporary-worktree>",
        "remove_exit_code": remove["exit_code"],
        "worktree_removed": worktree_removed,
        "temporary_root_removed": temporary_root_removed,
        "tree_preserved": False,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=("browser-audio-tool", "malformed-or-canceled"), required=True)
    parser.add_argument("--tree", type=Path, help="caller-provided exact main -> C61 -> C83 tree; arbitrary trees are rejected")
    parser.add_argument("--child-timeout", type=int, default=480)
    parser.add_argument("--aggregate-timeout", type=int, default=900)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    require(args.child_timeout > 0 and args.aggregate_timeout > 0, "timeouts must be positive")
    temporary_root: Path | None = None
    worktree: Path | None = None
    checks: list[dict[str, Any]] = []
    started = time.monotonic()
    result: dict[str, Any] = {}
    try:
        if args.tree:
            worktree = args.tree.resolve()
            require(worktree.is_dir(), f"provided synthetic tree does not exist: {worktree}")
            expected_root, expected_worktree, expected_tree = create_required_tree()
            try:
                synthetic = synthetic_tree_binding(worktree, expected_tree)
            finally:
                cleanup_tree(expected_root, expected_worktree)
        else:
            temporary_root, worktree, expected_tree = create_required_tree()
            synthetic = synthetic_tree_binding(worktree, expected_tree)
        require(worktree is not None, "synthetic tree was not created")
        for argv, cwd, extra_env, label, command_timeout in command_set(args.case, worktree, args.child_timeout):
            remaining = args.aggregate_timeout - (time.monotonic() - started)
            if remaining <= 0:
                checks.append({"label": label, "status": "skipped-aggregate-timeout", "command": " ".join(argv), "timed_out": True, "process_group_gone": True, "credential_environment_scrubbed": True, "credential_output_markers_absent": True, "test_discovery": {"required": "go test" in " ".join(argv), "tests_discovered": 0}})
                continue
            timeout = max(1, min(command_timeout, args.child_timeout, int(remaining)))
            check = bounded(argv, cwd, timeout, env_extra=extra_env, label=label)
            if label == "shipped credential-free browser/audio/tool workflow" and check["status"] == "passed":
                attach_workflow_detail(check, worktree)
            checks.append(check)
            if time.monotonic() - started > args.aggregate_timeout:
                break
        tree_status_before_cleanup = status_lines(ROOT, cwd=worktree)
        cleanup = cleanup_tree(temporary_root, worktree)
        elapsed = round(time.monotonic() - started, 3)
        passed = bool(checks) and all(item.get("status") == "passed" for item in checks)
        process_groups_clean = all(item.get("process_group_gone", False) for item in checks)
        credential_free = all(item.get("credential_environment_scrubbed", False) and item.get("credential_output_markers_absent", False) for item in checks)
        effects: dict[str, Any] = {
            "workflow": "local synthetic fixture/process only",
            "external_browser_or_microphone": False,
            "observable_output": sorted({marker for item in checks for marker in item.get("observable_effects", {}).get("output_markers", [])}),
            "artifacts": [
                {
                    "kind": "shipped-workflow-report",
                    "sha256": item["workflow_report_sha256"],
                    "classification": item["workflow_report"].get("result_classification"),
                }
                for item in checks
                if "workflow_report_sha256" in item
            ],
            "negative_control": args.case == "malformed-or-canceled",
            "cleanup_claim": cleanup.get("status") == "passed" and not tree_status_before_cleanup,
        }
        if not effects["artifacts"]:
            effects["artifacts"].append({"kind": "negative-control-output", "sha256": hashlib.sha256("\n".join(item.get("output", "") for item in checks).encode()).hexdigest(), "classification": "SOFTWARE_LOCAL_PROCESS_ONLY"})
        result = {
            "schema": "audio-runtime-c109-public-checks-v2",
            "case": args.case,
            "status": "passed" if passed and process_groups_clean and credential_free and cleanup.get("status") == "passed" and elapsed <= args.aggregate_timeout else "failed",
            "synthetic": {
                "base": MAIN,
                "order": ["c61", "c83"],
                "tree": synthetic["final_tree"],
                "status": tree_status_before_cleanup,
                "software_only": True,
                "hardware_or_acoustic_claim": False,
                "tree_binding": synthetic,
            },
            "bounded": {
                "child_timeout_seconds": args.child_timeout,
                "aggregate_timeout_seconds": args.aggregate_timeout,
                "elapsed_seconds": elapsed,
                "process_groups_clean": process_groups_clean,
            },
            "credential_free": credential_free,
            "effects": effects,
            "cleanup": cleanup,
            "checks": checks,
        }
    except Exception as exc:
        if worktree is not None and temporary_root is not None:
            cleanup_tree(temporary_root, worktree)
        result = {
            "schema": "audio-runtime-c109-public-checks-v2",
            "case": args.case,
            "status": "failed",
            "error": str(exc),
            "checks": checks,
        }
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps({"case": args.case, "status": "failed", "error": str(exc)}, sort_keys=True))
        return 2
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"case": args.case, "status": result["status"], "output": str(args.output)}, sort_keys=True))
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
