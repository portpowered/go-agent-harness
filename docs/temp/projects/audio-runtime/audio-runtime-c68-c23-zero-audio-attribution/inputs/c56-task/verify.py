#!/usr/bin/env python3
"""Task-local, source-bound C56 verification controls."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time


HERE = Path(__file__).resolve().parent
REPO = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
EVIDENCE = HERE / "evidence"
BASELINE_REVISION = "904e1f4c3be6c1e629138632573bd2fb55d50938"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_MANIFEST_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
TASK = "audio-runtime-c56-retire-cli-recording-orchestration"
BRANCH = "codex/audio-runtime-c56-retire-cli-recording-orchestration"

CLI_PRODUCTION = [
    "agent-cli/internal/services/internal/agentruntime/session_recording.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording_finalize.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording_directory_claim.go",
    "agent-cli/internal/services/internal/agentruntime/session_conversation_log.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_recording.go",
]
CLI_TESTS = [
    "agent-cli/internal/services/internal/agentruntime/session_recording_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording_sessions_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording_directory_claim_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_conversation_log_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_recording_test.go",
]
READ_ONLY_CALLERS = [
    "agent-cli/internal/services/internal/agentruntime/service.go",
    "agent-cli/internal/services/internal/agentruntime/session_image.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
    "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go",
]
GENERIC_WIRE = [
    "go-agent-runtime/services/recording/wire/providers.go",
    "go-agent-runtime/services/recording/wire/wire_gen.go",
]
RUNTIME_ROOT = REPO / "go-agent-runtime/services/recording"
SESSION_TEST = RUNTIME_ROOT / "internal/session/session_test.go"
CONSUMER_ROOT = HERE / "consumer"


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def git(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", *args], cwd=REPO, check=check, capture_output=True, text=True)


def git_text(*args: str) -> str:
    return git(*args).stdout.strip()


def write_json(name: str, value: object) -> None:
    path = EVIDENCE / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def run(command: list[str], cwd: Path = REPO, env: dict[str, str] | None = None, timeout: float = 300.0) -> dict:
    started = time.monotonic()
    child_env = os.environ.copy()
    if env:
        child_env.update(env)
    try:
        result = subprocess.run(command, cwd=cwd, env=child_env, capture_output=True, text=True, timeout=timeout)
        timed_out = False
    except subprocess.TimeoutExpired as error:
        return {
            "command": command,
            "cwd": str(cwd),
            "returncode": None,
            "timed_out": True,
            "elapsed_ms": int((time.monotonic() - started) * 1000),
            "stdout": (error.stdout or "")[-65536:],
            "stderr": (error.stderr or "")[-65536:],
        }
    return {
        "command": command,
        "cwd": str(cwd),
        "returncode": result.returncode,
        "timed_out": timed_out,
        "elapsed_ms": int((time.monotonic() - started) * 1000),
        "stdout": result.stdout[-65536:],
        "stderr": result.stderr[-65536:],
    }


def assert_command(result: dict, label: str) -> None:
    require(result["returncode"] == 0 and not result["timed_out"], f"{label} failed: {result}")


def baseline_counts() -> dict[str, int]:
    counts: dict[str, int] = {}
    for path in CLI_PRODUCTION:
        result = subprocess.run(["git", "show", f"{BASELINE_REVISION}:{path}"], cwd=REPO, check=True, capture_output=True)
        counts[path] = len(result.stdout.splitlines())
    return counts


def current_counts() -> dict[str, int]:
    return {path: len((REPO / path).read_text().splitlines()) for path in CLI_PRODUCTION if (REPO / path).is_file()}


def ancestry() -> dict:
    origin = git_text("rev-parse", "origin/main")
    head = git_text("rev-parse", "HEAD")
    return {
        "head": head,
        "origin_main": origin,
        "origin_main_matches_planning_revision": origin == BASELINE_REVISION,
        "origin_main_is_descendant_of_planning_revision": git("merge-base", "--is-ancestor", BASELINE_REVISION, origin, check=False).returncode == 0,
        "origin_main_is_ancestor_of_head": git("merge-base", "--is-ancestor", origin, head, check=False).returncode == 0,
        "startup_is_ancestor": git("merge-base", "--is-ancestor", STARTUP_REVISION, head, check=False).returncode == 0,
        "planning_main_is_ancestor": git("merge-base", "--is-ancestor", BASELINE_REVISION, head, check=False).returncode == 0,
        "branch": git_text("branch", "--show-current"),
    }


def status_paths() -> list[str]:
    output = git("status", "--porcelain", "--untracked-files=all").stdout
    return [line[3:] for line in output.splitlines() if len(line) >= 4]


def mode_baseline() -> None:
    counts = baseline_counts()
    require(counts == {
        CLI_PRODUCTION[0]: 1217,
        CLI_PRODUCTION[1]: 245,
        CLI_PRODUCTION[2]: 261,
        CLI_PRODUCTION[3]: 531,
        CLI_PRODUCTION[4]: 355,
    }, f"baseline line census changed: {counts}")
    facts = ancestry()
    require(facts["origin_main_is_descendant_of_planning_revision"], "origin/main is not a descendant of the admitted planning revision")
    require(facts["origin_main_is_ancestor_of_head"], "current origin/main is not integrated into the candidate")
    for path in GENERIC_WIRE:
        current = (REPO / path).read_bytes()
        baseline = subprocess.run(["git", "show", f"{BASELINE_REVISION}:{path}"], cwd=REPO, check=True, capture_output=True).stdout
        require(current == baseline, f"generic generated Wire input changed: {path}")
    require((REPO / "go-agent-runtime/services/recording/session.go").is_file(), "public recording contract is missing")
    require((REPO / "go-agent-runtime/services/recording/wire/session_gen.go").is_file(), "dedicated generated session constructor is missing")
    source = SESSION_TEST.read_text()
    for literal in ["client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/in-000.pcm", "screenshots/000001.png", "BrowserArtifactDefaultPath", "ErrLiveEvidenceClaimed", "conflicting terminal"]:
        require(literal in source, f"frozen oracle literal missing: {literal}")
    write_json("baseline-and-oracles.json", {
        "schema": "audio-runtime.c56.baseline-and-oracles.v1",
        "task": TASK,
        "branch": BRANCH,
        "baseline_revision": BASELINE_MANIFEST_REVISION,
        "planning_revision": BASELINE_REVISION,
        "startup_revision": STARTUP_REVISION,
        "ancestry": ancestry(),
        "baseline_cli_production_lines": counts,
        "baseline_total_lines": sum(counts.values()),
        "oracle_source": str(SESSION_TEST.relative_to(REPO)),
        "status_paths": status_paths(),
    })


def mode_lifecycle() -> None:
    runtime_files = [path for path in RUNTIME_ROOT.rglob("*.go") if path.is_file()]
    for path in runtime_files:
        source = path.read_text()
        require("agent-cli" not in source and "sessionRecordingClockBase" not in source, f"CLI/private clock leakage in {path.relative_to(REPO)}")
    public_source = (RUNTIME_ROOT / "session.go").read_text()
    require("internal/" not in public_source and "agent-cli" not in public_source, "public recording contract leaks a private import")
    generated = (RUNTIME_ROOT / "wire/session_gen.go").read_text()
    require(generated.startswith("// Code generated by Wire. DO NOT EDIT."), "session constructor is missing the generated header")
    for path in READ_ONLY_CALLERS:
        require(git("diff", "--quiet", BASELINE_REVISION, "--", path, check=False).returncode == 0, f"read-only caller changed: {path}")
    result = run(["go", "test", "-count=1", "-timeout=180s", "./go-agent-runtime/services/recording/..."], timeout=240)
    assert_command(result, "recording runtime normal tests")
    write_json("lifecycle-projections-failures.json", {
        "schema": "audio-runtime.c56.lifecycle-projections-failures.v1",
        "passed": True,
        "runtime_go_files": [str(path.relative_to(REPO)) for path in runtime_files],
        "read_only_callers": READ_ONLY_CALLERS,
        "generated_session_constructor": "go-agent-runtime/services/recording/wire/session_gen.go",
        "test": result,
    })


def mode_cli(args: argparse.Namespace) -> None:
    before = baseline_counts()
    after = current_counts()
    files = len(after)
    lines = sum(after.values())
    require(files <= args.max_files, f"CLI production file census {files} exceeds {args.max_files}: {after}")
    require(lines <= args.max_lines, f"CLI production line census {lines} exceeds {args.max_lines}: {after}")
    require(len(before) - files >= args.min_file_reduction, "CLI file reduction floor is not met")
    require(sum(before.values()) - lines >= args.min_line_reduction, "CLI line reduction floor is not met")
    require(len(before) == args.baseline_files and sum(before.values()) == args.baseline_lines, "caller supplied baseline does not match frozen baseline")
    source = (REPO / CLI_PRODUCTION[0]).read_text() if CLI_PRODUCTION[0] in after else ""
    require("recordingwire.NewSessionService" in source and "runtimerecording.SessionRecorder" in source, "retained CLI file is not a runtime adapter")
    require("OpenLiveEvidence" not in source and "browserMaxEvents" not in source and "sessionRecordingClockBase" not in source, "retained CLI file contains retired recording decisions")
    write_json("cli-retirement.json", {
        "schema": "audio-runtime.c56.cli-retirement.v1",
        "passed": True,
        "baseline_files": args.baseline_files,
        "baseline_lines": args.baseline_lines,
        "baseline_counts": before,
        "current_files": files,
        "current_lines": lines,
        "current_counts": after,
        "reductions": {"files": len(before) - files, "lines": sum(before.values()) - lines},
        "owned_production_paths": CLI_PRODUCTION,
    })


def mode_exact() -> None:
    source = SESSION_TEST.read_text()
    required = [
        "audio/in-000.pcm", "audio/in-001.pcm", "audio/out-000.pcm", "audio/out-001.pcm",
        "session-log.jsonl", "BrowserArtifactDefaultPath", "browserEventsVersion", "ErrLiveEvidenceClaimed", "conflicting terminal",
        "secret=hidden", "SHA256", "audio_segments",
    ]
    for literal in required:
        require(literal in source, f"exact artifact or mutation oracle missing: {literal}")
    result = run(["go", "test", "-count=1", "-timeout=180s", "./go-agent-runtime/services/recording/internal/session"], timeout=240)
    assert_command(result, "private session exact artifact tests")
    write_json("exact-artifacts-and-mutations.json", {"schema": "audio-runtime.c56.exact-artifacts-and-mutations.v1", "passed": True, "required_literals": required, "test": result})


def mode_embedding() -> None:
    banned = []
    for path in CONSUMER_ROOT.rglob("*.go"):
        text = path.read_text()
        for literal in ["agent-cli/", "/internal/", "recording/internal"]:
            if literal in text:
                banned.append({"path": str(path.relative_to(HERE)), "literal": literal})
    require(not banned, f"consumer imports private or CLI code: {banned}")
    env = {"GOWORK": "off"}
    normal = run(["go", "test", "-count=1", "-timeout=180s", "./..."], cwd=CONSUMER_ROOT, env=env, timeout=240)
    assert_command(normal, "GOWORK=off consumer normal tests")
    race = run(["go", "test", "-race", "-count=1", "-timeout=240s", "./..."], cwd=CONSUMER_ROOT, env=env, timeout=300)
    assert_command(race, "GOWORK=off consumer race tests")
    write_json("embedding-public-provenance-cleanup.json", {
        "schema": "audio-runtime.c56.embedding-public-provenance-cleanup.v1",
        "passed": True,
        "gowork": "off",
        "consumer": str(CONSUMER_ROOT.relative_to(REPO)),
        "private_imports": banned,
        "normal": normal,
        "race": race,
    })


def owned_go_files() -> list[Path]:
    roots = [RUNTIME_ROOT]
    paths = []
    for root in roots:
        paths.extend(path for path in root.rglob("*.go") if path.is_file())
    paths.extend(REPO / path for path in CLI_PRODUCTION + CLI_TESTS if (REPO / path).is_file())
    return sorted(set(paths))


def mode_formatting() -> None:
    files = owned_go_files()
    result = subprocess.run(["gofmt", "-l", *[str(path) for path in files]], cwd=REPO, check=False, capture_output=True, text=True)
    require(result.returncode == 0 and not result.stdout.strip(), f"gofmt reported owned files: {result.stdout}")
    vet = run(["go", "vet", "./go-agent-runtime/services/recording/..."], timeout=240)
    assert_command(vet, "recording runtime vet")
    write_json("formatting-owned-go.json", {"schema": "audio-runtime.c56.formatting-owned-go.v1", "passed": True, "files": [str(path.relative_to(REPO)) for path in files], "vet": vet})


def mode_final() -> None:
    facts = ancestry()
    require(facts["branch"] == BRANCH, f"candidate branch is {facts['branch']!r}, expected {BRANCH!r}")
    require(facts["origin_main_is_descendant_of_planning_revision"], "origin/main is not a descendant of the admitted planning revision")
    require(facts["origin_main_is_ancestor_of_head"], "current origin/main is not integrated into the candidate")
    require(facts["startup_is_ancestor"] and facts["planning_main_is_ancestor"], "required ancestry is missing")
    paths = status_paths()
    allowed = set(CLI_PRODUCTION + CLI_TESTS + READ_ONLY_CALLERS + GENERIC_WIRE)
    allowed_prefixes = ["go-agent-runtime/services/recording/", "coverage-manifest/go-agent-runtime/services/recording/internal/session/", "docs/temp/projects/audio-runtime/audio-runtime-c56-retire-cli-recording-orchestration/"]
    unexpected = [path for path in paths if path not in allowed and not any(path.startswith(prefix) for prefix in allowed_prefixes)]
    require(not unexpected, f"unowned working-tree paths: {unexpected}")
    write_json("final-scope-provenance-budget.json", {
        "schema": "audio-runtime.c56.final-scope-provenance-budget.v1",
        "passed": True,
        "task": TASK,
        "branch": BRANCH,
        "head": facts["head"],
        "origin_main": facts["origin_main"],
        "startup_revision": STARTUP_REVISION,
        "baseline_manifest_revision": BASELINE_MANIFEST_REVISION,
        "planning_revision": BASELINE_REVISION,
        "status_paths": paths,
        "unexpected_paths": unexpected,
        "toolchain": subprocess.run(["go", "version"], check=True, capture_output=True, text=True).stdout.strip(),
    })


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["baseline-and-oracles", "lifecycle-projections-failures", "cli-retirement", "exact-artifacts-and-mutations", "embedding-public-provenance-cleanup", "formatting-owned-go", "final-scope-provenance-budget"])
    parser.add_argument("--baseline-files", type=int, default=5)
    parser.add_argument("--baseline-lines", type=int, default=2609)
    parser.add_argument("--max-files", type=int, default=2)
    parser.add_argument("--max-lines", type=int, default=850)
    parser.add_argument("--min-file-reduction", type=int, default=3)
    parser.add_argument("--min-line-reduction", type=int, default=1759)
    args = parser.parse_args()
    {
        "baseline-and-oracles": mode_baseline,
        "lifecycle-projections-failures": mode_lifecycle,
        "cli-retirement": lambda: mode_cli(args),
        "exact-artifacts-and-mutations": mode_exact,
        "embedding-public-provenance-cleanup": mode_embedding,
        "formatting-owned-go": mode_formatting,
        "final-scope-provenance-budget": mode_final,
    }[args.mode]()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.CalledProcessError) as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
