#!/usr/bin/env python3
"""Bounded credential-free software replay for the C79 lifecycle candidate."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
EVIDENCE_ROOT = HERE / "evidence"
EVIDENCE = HERE / "evidence" / "runs"

CASES = {
    "scheduled-tool-continuation": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "^TestToolContinuationRemainsOneScheduledLifecycle$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Test(SessionProgressObserver_ChainedToolContinuation|RunAgentLoopSessionRetriesScheduledToolContinuationOnce)$",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "^TestSessionCommand_CreditsConsecutiveScheduledToolContinuations$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "terminal-and-malformed": [
        [
            "go",
            "test",
            "./go-agent-runtime/services/sessiondiagnostics/wire",
            "-run",
            "Test(Response|Malformed|Duplicate|Wrong|Terminal|Close|Order|Reset)",
            "-count=1",
            "-timeout=25s",
        ],
        [
            "go",
            "test",
            "./agent-cli/internal/services/internal/agentruntime",
            "-run",
            "Session(Diagnostics|Terminal|Cancellation)|SessionDiagnostics",
            "-count=1",
            "-timeout=25s",
        ],
    ],
    "credential-free-audio-tool-replay": [
        [
            "go",
            "test",
            "./agent-cli/test/integration",
            "-run",
            "Test(InteractionReplay_PrintsNormalizedEventsAsNDJSON|InteractionCommand_HelpDocumentsReplayOutputAndCredentialFreeBehavior|SessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI)$",
            "-count=1",
            "-timeout=25s",
        ],
    ],
}

HEALTHY_REPLAY_FIXTURE = ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
ERROR_REPLAY_FIXTURE = ROOT / "agent-cli/test/integration/testdata/openai_realtime_error.session.json"


def clean_environment() -> dict[str, str]:
    env = os.environ.copy()
    for name in list(env):
        if name.endswith(("_API_KEY", "_TOKEN", "_SECRET")) or name in {"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"}:
            env.pop(name, None)
    env["GOCACHE"] = "/tmp/go-build-audio-runtime-c79-replay"
    return env


def source_provenance() -> dict:
    revision = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    status_result = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=all"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    # Evidence is deliberately written by each bounded run. Treat every
    # descendant of the owned evidence root as a diagnostic output, while
    # still requiring all executable source and fixture inputs to be clean.
    evidence_prefix = str(EVIDENCE_ROOT.relative_to(ROOT)) + "/"
    status = []
    ignored_evidence_changes = []
    for line in status_result.stdout.splitlines():
        path = line[3:] if len(line) >= 3 else line
        if " -> " in path:
            path = path.rsplit(" -> ", 1)[-1]
        if path.startswith(evidence_prefix):
            ignored_evidence_changes.append(line)
        else:
            status.append(line)
    tracked = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, check=True, capture_output=True
    ).stdout.split(b"\0")
    digest = hashlib.sha256()
    input_count = 0
    non_file_paths = []
    for raw_path in tracked:
        if not raw_path:
            continue
        path = ROOT / os.fsdecode(raw_path)
        digest.update(raw_path)
        digest.update(b"\0")
        if path.is_file():
            digest.update(hashlib.sha256(path.read_bytes()).digest())
        else:
            # Gitlinks are tracked entries but materialize as directories in
            # this worktree. Include their index record instead of attempting
            # to read directory bytes.
            entry = subprocess.run(
                ["git", "ls-files", "--stage", "--", os.fsdecode(raw_path)],
                cwd=ROOT,
                check=True,
                capture_output=True,
            ).stdout
            digest.update(b"gitlink\0")
            digest.update(entry)
            non_file_paths.append(os.fsdecode(raw_path))
        input_count += 1
    go_version = subprocess.run(
        ["go", "version"], cwd=ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    return {
        "revision": revision,
        "status": "\n".join(status),
        "ignored_evidence_changes": ignored_evidence_changes,
        "tracked_input_count": input_count,
        "tracked_input_sha256": digest.hexdigest(),
        "tracked_non_file_paths": non_file_paths,
        "go_version": go_version,
    }


def file_provenance(path: Path) -> dict:
    data = path.read_bytes()
    return {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def run_child(argv: list[str], *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    remaining = max(0.1, deadline - time.monotonic())
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=ROOT, env=env, capture_output=True, text=True, timeout=min(timeout, remaining))
        return {
            "argv": argv,
            "returncode": result.returncode,
            "stdout": result.stdout[-8000:],
            "stderr": result.stderr[-8000:],
            "elapsed_seconds": round(time.monotonic() - started, 3),
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "argv": argv,
            "returncode": 124,
            "stdout": str(exc.stdout or "")[-8000:],
            "stderr": str(exc.stderr or "")[-8000:],
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "timeout": True,
        }


def run_shipped_workflow(binary: Path, config_dir: Path, fixture: Path, *, timeout: int, deadline: float, env: dict[str, str]) -> dict:
    check = run_child(
        [str(binary), "--config-dir", str(config_dir), "session", "--replay", str(fixture)],
        timeout=timeout,
        deadline=deadline,
        env=env,
    )
    combined = check["stdout"] + check["stderr"]
    check["fixture"] = str(fixture.relative_to(ROOT))
    check["observations"] = {
        "first_assistant_output": "Assistant: Hello there" in combined,
        "second_assistant_output": "Assistant: Second turn reply" in combined,
        "session_closed": "[session closed: healthy_complete]" in combined,
        "terminal_observed": "[session terminal:" in combined,
    }
    return check


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=sorted(CASES))
    parser.add_argument("--child-timeout", type=int, default=90)
    parser.add_argument("--aggregate-timeout", type=int, default=300)
    args = parser.parse_args()

    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    env = clean_environment()
    source = source_provenance()
    if source["status"]:
        raise RuntimeError(f"source worktree is not clean before shipped replay: {source['status']!r}")
    checks = []
    shipped = None
    malformed = None
    artifact = None
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c79-yui-") as directory:
        directory_path = Path(directory)
        binary = directory_path / "yui"
        config_dir = directory_path / "config"
        checks.append(run_child(["go", "build", "-o", str(binary), "./agent-cli/cmd/yui"], timeout=args.child_timeout, deadline=deadline, env=env))
        if checks[-1]["returncode"] == 0:
            artifact = file_provenance(binary)
            checks.append(run_child([str(binary), "interaction", "--help"], timeout=args.child_timeout, deadline=deadline, env=env))
            shipped = run_shipped_workflow(binary, config_dir, HEALTHY_REPLAY_FIXTURE, timeout=args.child_timeout, deadline=deadline, env=env)
            checks.append(shipped)
            if args.case == "terminal-and-malformed" and time.monotonic() < deadline:
                malformed = run_child(
                    [str(binary), "--config-dir", str(config_dir), "session", "--replay", str(ERROR_REPLAY_FIXTURE)],
                    timeout=args.child_timeout,
                    deadline=deadline,
                    env=env,
                )
                malformed["fixture"] = str(ERROR_REPLAY_FIXTURE.relative_to(ROOT))
                malformed["expected_failure"] = True
                malformed["terminal_failure_observed"] = "terminal_failure" in malformed["stdout"] + malformed["stderr"]
                checks.append(malformed)
        for command in CASES[args.case]:
            if time.monotonic() >= deadline:
                checks.append({"argv": command, "returncode": 124, "timeout": True, "error": "aggregate timeout exhausted"})
                break
            checks.append(run_child(command, timeout=args.child_timeout, deadline=deadline, env=env))

    if shipped is not None and (shipped["returncode"] != 0 or not all(shipped["observations"].values())):
        raise RuntimeError(f"shipped healthy replay did not prove lifecycle output: {json.dumps(shipped, sort_keys=True)}")
    if malformed is not None and (malformed["returncode"] == 0 or not malformed["terminal_failure_observed"]):
        raise RuntimeError(f"shipped malformed replay did not fail closed: {json.dumps(malformed, sort_keys=True)}")
    result = {
        "schema": "audio-runtime-c79-replay/v2",
        "case": args.case,
        "credential_free": True,
        "software_replay_only": True,
        "child_timeout_seconds": args.child_timeout,
        "aggregate_timeout_seconds": args.aggregate_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "source": source,
        "artifact": artifact,
        "fixtures": {
            "healthy": file_provenance(HEALTHY_REPLAY_FIXTURE),
            "malformed": file_provenance(ERROR_REPLAY_FIXTURE),
        },
        "shipped_workflow": shipped,
        "malformed_workflow": malformed,
        "checks": checks,
    }
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    (EVIDENCE / f"{args.case}.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if all(check.get("returncode") == 0 or check is malformed for check in checks) else 1


if __name__ == "__main__":
    raise SystemExit(main())
