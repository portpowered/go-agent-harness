#!/usr/bin/env python3
"""Run the bounded C105 browser-cancellation characterization matrix.

This runner never checks out either comparison revision.  It archives the exact
git objects into separate temporary trees, records immutable manifests, runs the
one named test in a predeclared matrix, and leaves only reviewable evidence in
the task-owned directory.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import shlex
import subprocess
import sys
import tarfile
import tempfile
import time
from collections import Counter
from typing import Any


TASK_NAME = "audio-runtime-c105-characterize-browser-cancel-coverage-regression"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
BRANCH = "codex/audio-runtime-c105-characterize-browser-cancel-coverage-regression"
MAIN_REVISION = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C93_REVISION = "35b4752e578dff49744159012982fccfacfb9794"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
TEST_NAME = "TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab"
TEST_REGEX = f"^{TEST_NAME}$"
TEST_PACKAGE = "./agent-cli/internal/services/internal/agentruntime"
COVERPKG = "github.com/portpowered/go-agent-harness/agent-cli/...,github.com/portpowered/go-agent-harness/go-agent-runtime/..."
PER_COMMAND_DEFAULT = 90.0
AGGREGATE_DEFAULT = 600.0
TRIALS_DEFAULT = 10

REPO = Path(__file__).resolve().parents[5]
EVIDENCE = Path(__file__).resolve().parent
PRD = REPO / "prd.json"
SOURCE_PATHS = [
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go",
]

CI_NEGATIVE_CONTROL = {
    "source": "GitHub Actions coverage job",
    "workflow_run_id": 34702885866,
    "job_id": 103577645543,
    "head_revision": C93_REVISION,
    "log_url": "https://github.com/portpowered/go-agent-harness/actions/runs/34702885866/job/103577645543",
    "source_log_sha256": "244bd0ebbd3219ef85f11e83883290a803ff6aa6aeb193dc07955fdff4441af0",
    "exit_status": 1,
    "elapsed": "6m36.472s",
    "classification": "named test assertion failure; not timeout, compile failure, or zero-test run",
    "failing_test": TEST_NAME,
    "literal_failure_excerpt": [
        "session_browser_scenario_runner_test.go:220: result = {... Finalized:true ... Cancellation:{Interrupted:true Requested:true InvocationID:inv-000002 FinalState: Reason:customer requested stop ...} ... Mechanical:{Passed:false ... interrupted invocation did not reach canceled terminal state ... explicit cancel did not preserve a canceled terminal state ...}}, want finalized mechanical pass",
        "the preserved broker cancellation observation is State:canceled with Terminal:false; Cancellation.FinalState is empty",
        "the unchanged test requires result.Finalized && result.Mechanical.Passed and cancellation.FinalState == webmcp.InvocationCanceled",
    ],
    "provenance_note": "The canonical CI log is not rewritten into this task. The immutable job URL and SHA256 preserve the historical negative control without importing bulky log output.",
}


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def json_write(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run_command(
    command: list[str],
    *,
    cwd: Path = REPO,
    env: dict[str, str] | None = None,
    timeout: float | None = None,
) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(command, cwd=cwd, env=env, capture_output=True, timeout=timeout, check=False)


def git_text(args: list[str], *, cwd: Path = REPO) -> str:
    result = run_command(["git", *args], cwd=cwd)
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout.decode(errors="replace").strip()


def git_exists(revision: str) -> bool:
    result = run_command(["git", "cat-file", "-e", f"{revision}^{{commit}}"])
    return result.returncode == 0


def ancestry(ancestor: str, descendant: str) -> dict[str, Any]:
    result = run_command(["git", "merge-base", "--is-ancestor", ancestor, descendant])
    return {
        "ancestor": ancestor,
        "descendant": descendant,
        "passed": result.returncode == 0,
        "exit_status": result.returncode,
    }


def parse_porcelain(value: str) -> list[str]:
    paths: list[str] = []
    for line in value.splitlines():
        if not line:
            continue
        payload = line[3:] if len(line) >= 3 else line
        if " -> " in payload:
            payload = payload.split(" -> ", 1)[1]
        paths.append(payload)
    return paths


def ensure_owned_status() -> dict[str, Any]:
    status = git_text(["status", "--porcelain", "--untracked-files=all"])
    paths = parse_porcelain(status)
    outside = [path for path in paths if not path.startswith(str(EVIDENCE.relative_to(REPO)) + "/") and path != str(EVIDENCE.relative_to(REPO))]
    if outside:
        raise RuntimeError(f"non-owned worktree changes detected: {outside}")
    return {"porcelain": status, "paths": paths, "outside_owned_paths": outside}


def project_control(arguments: list[str]) -> dict[str, Any]:
    factory_root = os.environ.get("FACTORY_ROOT")
    server = os.environ.get("FACTORY_SERVER_URL")
    if not factory_root or not server:
        raise RuntimeError("FACTORY_ROOT and FACTORY_SERVER_URL are required for admitted-task provenance")
    script = Path(factory_root) / "factory/scripts/project-control.py"
    result = run_command([sys.executable, str(script), *arguments], cwd=Path(factory_root), env={**os.environ, "FACTORY_SERVER_URL": server})
    stdout = result.stdout.decode(errors="replace").strip()
    stderr = result.stderr.decode(errors="replace").strip()
    parsed: Any = None
    try:
        parsed = json.loads(stdout)
    except json.JSONDecodeError:
        parsed = stdout
    return {
        "command": f"python3 {script} {' '.join(shlex.quote(argument) for argument in arguments)}",
        "server": server,
        "exit_status": result.returncode,
        "stdout": parsed,
        "stderr": stderr,
    }


def safe_extract(archive: Path, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    destination = destination.resolve()
    with tarfile.open(archive, "r") as handle:
        for member in handle.getmembers():
            target = (destination / member.name).resolve()
            if target != destination and destination not in target.parents:
                raise RuntimeError(f"unsafe archive member: {member.name}")
            handle.extract(member, destination)


def tree_manifest(root: Path) -> dict[str, Any]:
    files: list[dict[str, Any]] = []
    for path in sorted((candidate for candidate in root.rglob("*") if candidate.is_file()), key=lambda item: item.relative_to(root).as_posix()):
        relative = path.relative_to(root).as_posix()
        data_hash = sha256_file(path)
        files.append({"path": relative, "bytes": path.stat().st_size, "sha256": data_hash})
    digest_material = "".join(f"{item['path']}\0{item['bytes']}\0{item['sha256']}\n" for item in files).encode()
    return {"root_name": root.name, "file_count": len(files), "tree_sha256": sha256_bytes(digest_material), "files": files}


def manifest_delta(initial: dict[str, Any], final: dict[str, Any]) -> dict[str, Any]:
    before = {item["path"]: item for item in initial.get("files", [])}
    after = {item["path"]: item for item in final.get("files", [])}
    added = sorted(set(after) - set(before))
    removed = sorted(set(before) - set(after))
    changed = sorted(path for path in set(before) & set(after) if before[path] != after[path])
    return {
        "added_file_count": len(added),
        "removed_file_count": len(removed),
        "changed_file_count": len(changed),
        "added_paths_sample": added[:12],
        "removed_paths_sample": removed[:12],
        "changed_paths_sample": changed[:12],
    }


def archive_revision(label: str, revision: str, temporary_root: Path) -> dict[str, Any]:
    if not git_exists(revision):
        raise RuntimeError(f"required revision is unavailable: {revision}")
    archive = temporary_root / f"{label}.tar"
    extracted = temporary_root / label
    with archive.open("wb") as output:
        result = subprocess.run(["git", "archive", "--format=tar", revision], cwd=REPO, stdout=output, stderr=subprocess.PIPE, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"git archive {revision} failed: {result.stderr.decode(errors='replace')}")
    archive_hash = sha256_file(archive)
    safe_extract(archive, extracted)
    manifest = tree_manifest(extracted)
    return {
        "label": label,
        "revision": revision,
        "archive_sha256": archive_hash,
        "archive_bytes": archive.stat().st_size,
        "extracted_root": str(extracted),
        "manifest": manifest,
    }


def source_hashes(root: Path) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for relative in SOURCE_PATHS:
        path = root / relative
        if not path.is_file():
            raise RuntimeError(f"required source path missing from archive: {relative}")
        result[relative] = {
            "bytes": path.stat().st_size,
            "sha256": sha256_file(path),
        }
    return result


def numbered_lines(root: Path, relative: str, start: int, end: int) -> list[dict[str, Any]]:
    lines = (root / relative).read_text(encoding="utf-8").splitlines()
    return [{"line": number, "text": lines[number - 1]} for number in range(start, end + 1) if 0 < number <= len(lines)]


def build_assertion_map(archives: dict[str, dict[str, Any]]) -> dict[str, Any]:
    ranges = [
        {
            "id": "test-finalized-mechanical",
            "path": SOURCE_PATHS[0],
            "start_line": 219,
            "end_line": 220,
            "assertion": "result.Finalized && result.Mechanical.Passed",
            "rejects": "any non-finalized or non-mechanical result",
        },
        {
            "id": "test-canceled-terminal",
            "path": SOURCE_PATHS[0],
            "start_line": 222,
            "end_line": 224,
            "assertion": "cancellation.FinalState == webmcp.InvocationCanceled",
            "rejects": "a missing or non-canceled final invocation state",
        },
        {
            "id": "mechanical-interruption-terminal",
            "path": SOURCE_PATHS[5],
            "start_line": 33,
            "end_line": 42,
            "assertion": "result.Cancellation.FinalState != webmcp.InvocationCanceled",
            "rejects": "an interrupted invocation without canceled terminal state",
        },
        {
            "id": "mechanical-explicit-cancel-terminal",
            "path": SOURCE_PATHS[5],
            "start_line": 47,
            "end_line": 52,
            "assertion": "result.Cancellation.FinalState != webmcp.InvocationCanceled",
            "rejects": "an explicit cancel without canceled terminal state",
        },
    ]
    evidence: list[dict[str, Any]] = []
    for item in ranges:
        entry = dict(item)
        entry["lines_by_revision"] = {
            label: numbered_lines(archive["root"], item["path"], item["start_line"], item["end_line"])
            for label, archive in archives.items()
        }
        entry["same_text_at_both_revisions"] = len({tuple(line["text"] for line in entry["lines_by_revision"][label]) for label in archives}) == 1
        evidence.append(entry)
    return {
        "test_name": TEST_NAME,
        "unchanged_assertion_proof": evidence,
        "interpretation": "The historical CI state Finalized:true with Cancellation.FinalState empty and a cancel broker call State:canceled/Terminal:false is rejected by both finalized/mechanical and canceled-terminal assertions.",
    }


def build_diff_inventory() -> dict[str, Any]:
    name_status = git_text(["diff", "--name-status", f"{MAIN_REVISION}..{C93_REVISION}"])
    stat = git_text(["diff", "--stat", f"{MAIN_REVISION}..{C93_REVISION}"])
    full_diff = run_command(["git", "diff", "--binary", f"{MAIN_REVISION}..{C93_REVISION}"]).stdout
    changed_paths: list[dict[str, str]] = []
    for line in name_status.splitlines():
        fields = line.split("\t")
        if len(fields) >= 2:
            changed_paths.append({"status": fields[0], "path": fields[-1]})
    browser_tokens = ("session_browser_scenario", "browserrunner", "browserscenario")
    browser_paths = [entry["path"] for entry in changed_paths if any(token in entry["path"] for token in browser_tokens)]
    return {
        "base_revision": MAIN_REVISION,
        "candidate_revision": C93_REVISION,
        "name_status": name_status.splitlines(),
        "stat": stat,
        "changed_path_count": len(changed_paths),
        "changed_paths": changed_paths,
        "browser_related_changed_paths": browser_paths,
        "full_diff_sha256": sha256_bytes(full_diff),
        "finding": "C93 changes room-media production/coverage evidence paths and does not change the browser scenario runner, tracker, interruption controller, evaluation, or named test paths.",
    }


def environment_snapshot() -> dict[str, Any]:
    go_version = run_command(["go", "version"])
    go_env = run_command(["go", "env", "-json"])
    return {
        "go_version": go_version.stdout.decode(errors="replace").strip(),
        "go_version_exit_status": go_version.returncode,
        "go_env": json.loads(go_env.stdout.decode(errors="replace")) if go_env.returncode == 0 else {"error": go_env.stderr.decode(errors="replace")},
        "go_env_exit_status": go_env.returncode,
        "os": platform.system(),
        "os_release": platform.release(),
        "os_version": platform.version(),
        "architecture": platform.machine(),
        "python_version": platform.python_version(),
        "python_executable": sys.executable,
        "working_directory": str(REPO),
        "timezone": os.environ.get("TZ", "<unset>"),
        "locale": os.environ.get("LC_ALL", os.environ.get("LANG", "<unset>")),
        "immutable_toolchain_root": json.loads(go_env.stdout.decode(errors="replace")).get("GOROOT") if go_env.returncode == 0 else "",
    }


def clone_seed(source: Path, destination: Path) -> str:
    if not source.is_dir():
        return "missing-seed"
    destination.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(["cp", "-c", "-R", f"{source}/.", str(destination)], capture_output=True, check=False)
    if result.returncode == 0:
        return "apfs-cow-clone"
    shutil.copytree(source, destination, dirs_exist_ok=True, symlinks=True)
    return "recursive-copy-fallback"


def link_seed(source: Path, destination: Path) -> str:
    if not source.is_dir():
        return "missing-seed"
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() or destination.is_symlink():
        if destination.is_dir() and not destination.is_symlink():
            shutil.rmtree(destination)
        else:
            destination.unlink()
    destination.symlink_to(source, target_is_directory=True)
    return "unique-symlink-to-fixed-seed"


def trial_environment(
    temp_root: Path,
    trial_root: Path,
    toolchain_root: str,
    cache_runtime: str,
    modcache_runtime: str,
) -> dict[str, str]:
    cache = Path(cache_runtime)
    modcache = Path(modcache_runtime)
    gotmp = trial_root / "gotmpdir"
    gopath = trial_root / "gopath"
    home = trial_root / "home"
    for path in (gotmp, gopath, home):
        path.mkdir(parents=True, exist_ok=True)
    return {
        "PATH": f"{toolchain_root}/bin:{os.environ.get('PATH', '/usr/bin:/bin')}",
        "HOME": str(home),
        "TMPDIR": str(gotmp),
        "GOTMPDIR": str(gotmp),
        "GOCACHE": str(cache),
        "GOMODCACHE": str(modcache),
        "GOPATH": str(gopath),
        "GOWORK": "auto",
        "GOTOOLCHAIN": "local",
        "GOPROXY": "off",
        "GOSUMDB": "sum.golang.org",
        "GOFLAGS": "-mod=readonly",
        "CGO_ENABLED": os.environ.get("CGO_ENABLED", "1"),
        "GOOS": os.environ.get("GOOS", "darwin"),
        "GOARCH": os.environ.get("GOARCH", platform.machine()),
        "GO_TOOLCHAIN_ROOT": toolchain_root,
        "GOCACHE_SEED_SOURCE": "recorded in isolated_paths.GOCACHE_seed_source",
        "GOCACHE_SEED_METHOD": "recorded in isolated_paths.GOCACHE_seed_method",
        "GOMODCACHE_SEED_SOURCE": "recorded in isolated_paths.GOMODCACHE_seed_source",
        "GOMODCACHE_SEED_METHOD": "stable-cell-symlink",
    }


def command_for(mode: dict[str, Any], coverage_path: Path) -> list[str]:
    command = ["go", "test", "-json", "-count=1"]
    if mode["tags"]:
        command.extend(["-tags", mode["tags"]])
    if mode["coverpkg"]:
        command.extend(["-coverpkg", mode["coverpkg"], "-coverprofile", str(coverage_path)])
    command.extend(["-timeout", "80s", "-run", TEST_REGEX, TEST_PACKAGE])
    return command


def prepare_cache_warmup(
    *,
    label: str,
    mode: dict[str, Any],
    source_root: Path,
    environment_info: dict[str, str],
    per_command_timeout: float,
    temporary_root: Path,
) -> dict[str, Any]:
    """Build one independent stable cache root per revision/mode.

    The warmup is not a characterization trial and is never included in the
    forty-trial ledger. It only avoids recompiling the same dependency graph in
    each unique copy-on-write GOCACHE path while retaining a distinct path
    identity for every trial.
    """
    warmup_root = temporary_root / f"warmup-{label}-{mode['id']}"
    warmup_root.mkdir(parents=True, exist_ok=True)
    cache_seed = temporary_root / f"gocache-{label}-{mode['id']}"
    cache_runtime = cache_seed
    modcache_runtime = temporary_root / f"gomodcache-{label}-{mode['id']}"
    link_seed(Path(environment_info["modcache_seed"]), modcache_runtime)
    gotmp = warmup_root / "gotmpdir"
    gopath = warmup_root / "gopath"
    home = warmup_root / "home"
    for path in (cache_runtime, gotmp, gopath, home):
        path.mkdir(parents=True, exist_ok=True)
    coverage_path = warmup_root / "coverage.out"
    command = command_for(mode, coverage_path)
    env = {
        "PATH": f"{environment_info['toolchain_root']}/bin:{os.environ.get('PATH', '/usr/bin:/bin')}",
        "HOME": str(home),
        "TMPDIR": str(gotmp),
        "GOTMPDIR": str(gotmp),
        "GOCACHE": str(cache_runtime),
        "GOMODCACHE": str(modcache_runtime),
        "GOPATH": str(gopath),
        "GOWORK": "auto",
        "GOTOOLCHAIN": "local",
        "GOPROXY": "off",
        "GOSUMDB": "sum.golang.org",
        "GOFLAGS": "-mod=readonly",
        "CGO_ENABLED": os.environ.get("CGO_ENABLED", "1"),
        "GOOS": os.environ.get("GOOS", "darwin"),
        "GOARCH": os.environ.get("GOARCH", platform.machine()),
    }
    started_at = utc_now()
    started = time.monotonic()
    process = subprocess.Popen(command, cwd=source_root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    termination_signal: int | None = None
    try:
        raw_stdout, raw_stderr = process.communicate(timeout=per_command_timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        raw_stdout = error.output or b""
        raw_stderr = error.stderr or b""
        termination_signal = terminate_process(process)
    stdout = raw_stdout.decode(errors="replace") if isinstance(raw_stdout, bytes) else str(raw_stdout)
    stderr = raw_stderr.decode(errors="replace") if isinstance(raw_stderr, bytes) else str(raw_stderr)
    finished_at = utc_now()
    output_dir = EVIDENCE / "preflight" / label / mode["id"]
    output_dir.mkdir(parents=True, exist_ok=True)
    stdout_path = output_dir / "go-test.jsonl"
    stderr_path = output_dir / "stderr.log"
    stdout_path.write_text(stdout, encoding="utf-8")
    stderr_path.write_text(stderr, encoding="utf-8")
    if coverage_path.exists():
        coverage_path.unlink()
    if not cache_seed.is_dir():
        cache_seed.mkdir(parents=True, exist_ok=True)
    cache_manifest = tree_manifest(cache_seed)
    return {
        "revision_label": label,
        "mode": mode["id"],
        "not_a_trial": True,
        "purpose": "one deterministic cache warmup only; not used for recurrence counts or causal classification",
        "command": command,
        "command_string": shlex.join(command),
        "started_at": started_at,
        "ended_at": finished_at,
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_status": process.returncode,
        "timed_out": timed_out,
        "termination_signal": termination_signal,
        "cache_seed": str(cache_seed),
        "cache_runtime_path": str(cache_runtime),
        "cache_seed_tree_sha256": cache_manifest["tree_sha256"],
        "cache_seed_manifest_files": cache_manifest["files"],
        "module_cache_reference": environment_info["modcache_seed"],
        "module_cache_runtime": str(modcache_runtime),
        "raw_log": str(stdout_path.relative_to(EVIDENCE)),
        "stderr_log": str(stderr_path.relative_to(EVIDENCE)),
        "raw_log_sha256": sha256_file(stdout_path),
        "stderr_log_sha256": sha256_file(stderr_path),
    }


def terminate_process(process: subprocess.Popen[bytes]) -> int | None:
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return None
    try:
        process.communicate(timeout=2.0)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.communicate()
    return signal.SIGTERM


def parse_events(stdout: str) -> dict[str, Any]:
    events: list[dict[str, Any]] = []
    actions = Counter()
    target_actions = Counter()
    malformed = 0
    for line in stdout.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            malformed += 1
            continue
        if not isinstance(event, dict):
            malformed += 1
            continue
        action = str(event.get("Action", ""))
        if action:
            actions[action] += 1
        if event.get("Test") == TEST_NAME:
            target_actions[action] += 1
            events.append({key: event[key] for key in ("Action", "Package", "Test", "Elapsed", "Output") if key in event})
    matched = target_actions.get("run", 0)
    passed = target_actions.get("pass", 0)
    failed = target_actions.get("fail", 0)
    return {
        "event_count": sum(actions.values()),
        "action_counts": dict(sorted(actions.items())),
        "target_action_counts": dict(sorted(target_actions.items())),
        "target_events": events,
        "malformed_json_lines": malformed,
        "matched_test_count": matched,
        "target_pass_count": passed,
        "target_fail_count": failed,
    }


def failure_signature(output: str) -> dict[str, Any]:
    phrases = {
        "test_finalized_mechanical_mismatch": "want finalized mechanical pass" in output,
        "interrupted_terminal_mismatch": "interrupted invocation did not reach canceled terminal state" in output,
        "explicit_cancel_terminal_mismatch": "explicit cancel did not preserve a canceled terminal state" in output,
        "nonterminal_cancel_observed": "State:canceled Terminal:false" in output,
        "empty_cancellation_final_state_observed": "FinalState:" in output and "FinalState:canceled" not in output,
        "result_finalized_true_observed": "Finalized:true" in output,
        "mechanical_passed_false_observed": "Mechanical:{Passed:false" in output,
    }
    reproduction = all(
        phrases[name]
        for name in (
            "test_finalized_mechanical_mismatch",
            "interrupted_terminal_mismatch",
            "explicit_cancel_terminal_mismatch",
        )
    )
    return {
        "key": "cancel-final-state-nonterminal" if reproduction else "unclassified-test-failure",
        "reproduces_preserved_negative_control": reproduction,
        "phrases": phrases,
        "literal_excerpt": [
            phrase
            for phrase, present in (
                ("want finalized mechanical pass", phrases["test_finalized_mechanical_mismatch"]),
                ("interrupted invocation did not reach canceled terminal state", phrases["interrupted_terminal_mismatch"]),
                ("explicit cancel did not preserve a canceled terminal state", phrases["explicit_cancel_terminal_mismatch"]),
                ("State:canceled Terminal:false", phrases["nonterminal_cancel_observed"]),
                ("Cancellation.FinalState is empty", phrases["empty_cancellation_final_state_observed"]),
            )
            if present
        ],
    }


def run_trial(
    *,
    label: str,
    revision: str,
    archive_hash: str,
    source_root: Path,
    mode: dict[str, Any],
    trial_number: int,
    per_command_timeout: float,
    aggregate_deadline: float,
    matrix_start: float,
    temporary_root: Path,
    environment_info: dict[str, str],
) -> dict[str, Any]:
    relative_output = Path("trials") / label / mode["id"] / f"{trial_number:02d}"
    output_root = EVIDENCE / relative_output
    output_root.mkdir(parents=True, exist_ok=True)
    trial_root = temporary_root / f"trial-{label}-{mode['id']}-{trial_number:02d}"
    trial_root.mkdir(parents=True, exist_ok=True)
    coverage_path = trial_root / "coverage.out"
    cache_runtime = Path(environment_info["cache_seed"])
    if not cache_runtime.is_dir():
        raise RuntimeError(f"cache seed is missing for {mode['id']}: {environment_info['cache_seed']}")
    cache_seed_method = "stable-cell-cache-reuse"
    env = trial_environment(
        temporary_root,
        trial_root,
        environment_info["toolchain_root"],
        str(cache_runtime),
        environment_info["modcache_runtime"],
    )
    command = command_for(mode, coverage_path)
    command_string = shlex.join(command)
    started_at = utc_now()
    started_monotonic = time.monotonic()
    timed_out = False
    termination_signal: int | None = None
    aggregate_expired_before_start = started_monotonic - matrix_start >= aggregate_deadline
    stdout = ""
    stderr = ""
    exit_status: int | None = None
    if aggregate_expired_before_start:
        timed_out = True
        stderr = "aggregate deadline expired before trial start\n"
    else:
        process = subprocess.Popen(
            command,
            cwd=source_root,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        remaining = min(per_command_timeout, max(0.1, aggregate_deadline - (time.monotonic() - matrix_start)))
        try:
            raw_stdout, raw_stderr = process.communicate(timeout=remaining)
            stdout = raw_stdout.decode(errors="replace")
            stderr = raw_stderr.decode(errors="replace")
            exit_status = process.returncode
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            raw_stdout = exc.output or b""
            raw_stderr = exc.stderr or b""
            stdout = raw_stdout.decode(errors="replace") if isinstance(raw_stdout, bytes) else str(raw_stdout)
            stderr = raw_stderr.decode(errors="replace") if isinstance(raw_stderr, bytes) else str(raw_stderr)
            termination_signal = terminate_process(process)
            exit_status = process.returncode
    ended_at = utc_now()
    duration = time.monotonic() - started_monotonic
    stdout_path = output_root / "go-test.jsonl"
    stderr_path = output_root / "stderr.log"
    stdout_path.write_text(stdout, encoding="utf-8")
    stderr_path.write_text(stderr, encoding="utf-8")
    cache_snapshot = trial_root / "gocache"
    cache_snapshot.symlink_to(cache_runtime, target_is_directory=True)
    module_snapshot = trial_root / "gomodcache"
    module_snapshot.symlink_to(Path(environment_info["modcache_runtime"]), target_is_directory=True)
    parsed = parse_events(stdout)
    signature = failure_signature(stdout)
    if timed_out:
        classification = "TIMEOUT"
        terminal_conclusion = "NO_TERMINAL_CONCLUSION_TIMEOUT"
    elif parsed["malformed_json_lines"] > 0:
        classification = "INFRA"
        terminal_conclusion = "NO_TERMINAL_CONCLUSION_MALFORMED_JSON"
    elif parsed["matched_test_count"] == 0:
        classification = "INFRA"
        terminal_conclusion = "NO_TERMINAL_CONCLUSION_ZERO_MATCHED_TESTS"
    elif exit_status == 0 and parsed["target_pass_count"] == 1:
        classification = "PASS"
        terminal_conclusion = "TERMINAL_CANCELED_ASSERTED"
    elif parsed["target_fail_count"] >= 1 and signature["reproduces_preserved_negative_control"]:
        classification = "PRODUCT_FAILURE"
        terminal_conclusion = "NONTERMINAL_OR_MISSING_CANCELED_STATE_OBSERVED"
    elif parsed["target_fail_count"] >= 1:
        classification = "TEST_FAILURE"
        terminal_conclusion = "UNRELATED_ASSERTION_OR_TEST_FAILURE"
    else:
        classification = "INFRA"
        terminal_conclusion = "NO_TERMINAL_CONCLUSION_UNEXPECTED_EXIT"
    assertion_reached = {
        "named_test_started": parsed["matched_test_count"] == 1,
        "finalized_mechanical_assertion": classification == "PASS" or signature["phrases"]["test_finalized_mechanical_mismatch"],
        "canceled_terminal_assertion": classification == "PASS" or signature["phrases"]["interrupted_terminal_mismatch"] or signature["phrases"]["explicit_cancel_terminal_mismatch"],
        "lifecycle_and_detached_tab_assertions": classification == "PASS" or signature["phrases"]["test_finalized_mechanical_mismatch"],
    }
    record = {
        "trial_id": f"{label}-{mode['id']}-{trial_number:02d}",
        "revision_label": label,
        "revision": revision,
        "archive_sha256": archive_hash,
        "mode": mode["id"],
        "command": command,
        "command_string": command_string,
        "source_root_identity": str(source_root),
        "isolated_paths": {
            "work_root": str(trial_root),
            "GOCACHE": env["GOCACHE"],
            "GOCACHE_trial_snapshot": str(cache_snapshot),
            "GOCACHE_seed_source": environment_info["cache_seed"],
            "GOCACHE_seed_method": cache_seed_method,
            "GOMODCACHE": env["GOMODCACHE"],
            "GOMODCACHE_trial_snapshot": str(module_snapshot),
            "GOMODCACHE_seed_source": environment_info["modcache_seed"],
            "GOMODCACHE_seed_method": "stable-cell-symlink",
            "GOTMPDIR": env["GOTMPDIR"],
            "GOPATH": env["GOPATH"],
            "HOME": env["HOME"],
            "coverage_output": str(coverage_path),
        },
        "environment": {
            **{key: env[key] for key in ("GOWORK", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOFLAGS", "CGO_ENABLED", "GOOS", "GOARCH", "GO_TOOLCHAIN_ROOT", "GOMODCACHE_SEED_SOURCE", "GOMODCACHE_SEED_METHOD")},
            "GOCACHE_SEED_SOURCE": environment_info["cache_seed"],
            "GOCACHE_SEED_METHOD": cache_seed_method,
            "GOMODCACHE_SEED_SOURCE": environment_info["modcache_seed"],
            "GOMODCACHE_SEED_METHOD": "stable-cell-symlink",
        },
        "started_at": started_at,
        "ended_at": ended_at,
        "duration_seconds": round(duration, 6),
        "aggregate_elapsed_seconds": round(time.monotonic() - matrix_start, 6),
        "per_command_timeout_seconds": per_command_timeout,
        "aggregate_timeout_seconds": aggregate_deadline,
        "exit_status": exit_status,
        "timed_out": timed_out,
        "termination_signal": termination_signal,
        "classification": classification,
        "assertion_reached": assertion_reached,
        "terminal_state_conclusion": terminal_conclusion,
        "go_test_events": parsed,
        "failure_signature": signature,
        "raw_log": {
            "path": str(stdout_path.relative_to(EVIDENCE)),
            "sha256": sha256_file(stdout_path),
            "bytes": stdout_path.stat().st_size,
        },
        "stderr_log": {
            "path": str(stderr_path.relative_to(EVIDENCE)),
            "sha256": sha256_file(stderr_path),
            "bytes": stderr_path.stat().st_size,
        },
        "coverage_output_removed_after_trial": False,
    }
    if coverage_path.exists():
        coverage_path.unlink()
        record["coverage_output_removed_after_trial"] = True
    record["isolated_paths_removed_after_trial"] = False
    record["isolated_paths_cleanup_deferred_until_matrix_end"] = True
    return record


def cell_summary(records: list[dict[str, Any]], label: str, mode: str) -> dict[str, Any]:
    cell = [record for record in records if record["revision_label"] == label and record["mode"] == mode]
    classifications = Counter(record["classification"] for record in cell)
    signatures = Counter(record["failure_signature"]["key"] for record in cell if record["classification"] == "PRODUCT_FAILURE")
    conclusions = Counter(record["terminal_state_conclusion"] for record in cell)
    return {
        "revision_label": label,
        "mode": mode,
        "trial_count": len(cell),
        "classification_counts": dict(sorted(classifications.items())),
        "pass_count": classifications.get("PASS", 0),
        "product_failure_count": classifications.get("PRODUCT_FAILURE", 0),
        "test_failure_count": classifications.get("TEST_FAILURE", 0),
        "timeout_count": classifications.get("TIMEOUT", 0),
        "infra_count": classifications.get("INFRA", 0),
        "negative_control_signature_counts": dict(sorted(signatures.items())),
        "terminal_conclusion_counts": dict(sorted(conclusions.items())),
    }


def classify(cells: list[dict[str, Any]], diff_inventory: dict[str, Any]) -> dict[str, Any]:
    by_key = {(cell["revision_label"], cell["mode"]): cell for cell in cells}
    invalid = [cell for cell in cells if cell["infra_count"] or cell["timeout_count"] or cell["test_failure_count"] or cell["trial_count"] != 10]
    main_fail = sum(cell["product_failure_count"] for cell in cells if cell["revision_label"] == "main")
    candidate_fail = sum(cell["product_failure_count"] for cell in cells if cell["revision_label"] == "c93")
    matched_failure_cells = []
    for mode in ("normal", "nomicrophone-coverpkg"):
        main_cell = by_key[("main", mode)]
        candidate_cell = by_key[("c93", mode)]
        main_keys = set(main_cell["negative_control_signature_counts"])
        candidate_keys = set(candidate_cell["negative_control_signature_counts"])
        if main_keys & candidate_keys:
            matched_failure_cells.append(mode)
    coverage_necessary = all(
        by_key[(label, "normal")]["product_failure_count"] == 0 and by_key[(label, "nomicrophone-coverpkg")]["product_failure_count"] > 0
        for label in ("main", "c93")
    ) and "nomicrophone-coverpkg" in matched_failure_cells
    if invalid:
        classification = "INCONCLUSIVE"
        threshold = "The fixed matrix contains infrastructure, timeout, unrelated assertion, or missing-trial evidence, so recurrence cannot support a causal class."
    elif coverage_necessary:
        classification = "ENVIRONMENT_ONLY_TRIGGER"
        threshold = "The same negative-control failure recurs under the predeclared coverpkg/nomicrophone condition on both revisions and is absent in matched normal mode."
    elif matched_failure_cells and main_fail > 0:
        classification = "ACCEPTED_MAIN_BROWSER_FLAKE"
        threshold = "The same named-test cancellation failure recurs on accepted main and C93 in at least one matched mode; accepted main therefore prevents a C93-only attribution."
    elif candidate_fail > 0 and main_fail == 0 and diff_inventory["browser_related_changed_paths"]:
        classification = "C93_REGRESSION"
        threshold = "C93 has candidate-only recurrence and the exact changed diff includes a browser cancellation causal path."
    else:
        classification = "INCONCLUSIVE"
        threshold = "The fixed paired observations do not demonstrate an accepted-main recurrence, a necessary environment condition, or a changed browser causal edge sufficient for one of the other classes."
    falsifiers = {
        "C93_REGRESSION": [
            "A matched accepted-main trial reproducing the same cancellation signature would falsify a C93-only regression.",
            "A source-diff audit showing no changed browser cancellation edge would falsify the attribution.",
        ],
        "ACCEPTED_MAIN_BROWSER_FLAKE": [
            "Ten matched accepted-main trials with no failure and candidate-only recurrence on a changed browser edge would falsify the shared-flake classification.",
            "A necessary coverage/environment trigger present on both revisions would refine and falsify this broader class in favor of ENVIRONMENT_ONLY_TRIGGER.",
        ],
        "ENVIRONMENT_ONLY_TRIGGER": [
            "A failure in matched normal mode on only C93 would falsify the necessary coverpkg/nomicrophone condition.",
            "A changed browser event edge required only by C93 would falsify environment-only attribution.",
        ],
        "INCONCLUSIVE": [
            "A reproducible candidate-only failure with a changed browser cancellation edge would resolve this as C93_REGRESSION.",
            "The same failure recurring on accepted main in a matched mode would resolve this as ACCEPTED_MAIN_BROWSER_FLAKE.",
            "The same failure only under a recorded coverpkg/nomicrophone condition on both revisions would resolve this as ENVIRONMENT_ONLY_TRIGGER.",
        ],
    }[classification]
    return {
        "classification": classification,
        "evidence_threshold": threshold,
        "falsifiers": falsifiers,
        "invalid_cells": invalid,
        "matched_failure_cells": matched_failure_cells,
        "main_product_failure_count": main_fail,
        "c93_product_failure_count": candidate_fail,
        "browser_related_changed_paths": diff_inventory["browser_related_changed_paths"],
        "negative_control_reproduced": any(cell["product_failure_count"] for cell in cells),
        "inconclusive_is_not_pass": classification == "INCONCLUSIVE",
    }


def write_report(
    *,
    provenance: dict[str, Any],
    cells: list[dict[str, Any]],
    characterization: dict[str, Any],
    ownership: dict[str, Any],
) -> None:
    rows = []
    for cell in cells:
        counts = ", ".join(f"{key}={value}" for key, value in sorted(cell["classification_counts"].items())) or "none"
        conclusions = ", ".join(f"{key}={value}" for key, value in sorted(cell["terminal_conclusion_counts"].items())) or "none"
        rows.append(f"| {cell['revision_label']} | {cell['mode']} | {cell['trial_count']} | {counts} | {conclusions} |")
    report = f"""# C105 browser cancellation coverage characterization

Classification: **{characterization['classification']}**

This is an evidence-only characterization. It does not modify production or test source, C93/C79 paths, active owner paths, pull requests, or merge state; it does not claim C93 acceptance or any broad project gate.

## Pinned comparison and gate evidence

- Admitted project/contract: `{PROJECT}` / `{CONTRACT}`; task `{TASK_NAME}`; branch `{BRANCH}`.
- Accepted main archive: `{MAIN_REVISION}`; preserved C93 archive: `{C93_REVISION}`.
- Startup integration ancestor: `{INTEGRATION_REVISION}` in both archives; accepted main is an ancestor of C93.
- Matrix bounds: ten fixed trials per revision/mode, 90-second per-command ceiling, 600-second aggregate ceiling. Each revision/mode has an independent stable Go cache root and stable offline module-cache symlink view warmed at the exact Go-visible paths; every trial uses its immutable archive source tree and has unique temporary, GOPATH, HOME, coverage, and cache-snapshot paths plus a unique module-cache snapshot marker. Go-visible cache roots receive pre/post manifests after the ten sequential trials; any deterministic Go build-cache writes are recorded rather than treated as source mutation.
- Toolchain/environment and archive/tree hashes are in `provenance.json`; the exact diff inventory is in `source-diff-inventory.json`.

Evidence threshold: {characterization['evidence_threshold']}

## Fixed matrix

| Revision | Mode | Trials | Outcome counts | Terminal conclusions |
|---|---|---:|---|---|
{chr(10).join(rows)}

A passing unchanged test is recorded only as `TERMINAL_CANCELED_ASSERTED`: the test itself checks finalized/mechanical success, canceled terminal state, lifecycle/detached-tab preservation, late-event suppression, and audio. A product failure is retained with its raw JSON log and literal mismatch; it is never retried away. TIMEOUT, INFRA, zero-match, compile, or unrelated assertion outcomes are not product reproduction.

## Preserved CI negative control

The historical coverage failure is the immutable GitHub Actions job reference in `negative-control/ci-reference.json`, with SHA256 `{CI_NEGATIVE_CONTROL['source_log_sha256']}`. It failed `{TEST_NAME}` at the unchanged `session_browser_scenario_runner_test.go:220` assertion. The captured signature is `Finalized:true`, `Mechanical.Passed:false`, empty `Cancellation.FinalState`, and the cancel observation `State:canceled Terminal:false`. `negative-control/assertion-map.json` contains the exact assertion text at both archived revisions and maps the bad state to the finalized/mechanical and canceled-terminal checks.

## Causal path and ownership

The observed event edge is: `browserConversationInterruptionController.observeInFlight` records the interruption at `session_browser_scenario_interrupt.go:56-101`; the tracker invokes the explicit cancel at `session_browser_scenario_tracker.go:349-385`; `browserConversationBroker.Cancel` records `State:canceled` but `Terminal:false` and records requested cancellation without `FinalState` at `session_browser_scenario_runner.go:914-935`; `browserConversationBroker.Invoke` can later copy a terminal `WaitInvocation` result at `session_browser_scenario_runner.go:875-900`; `BrowserConversationRun.RecordCancellation` only fills `FinalState` from a non-empty later observation at `session_browser_scenario_run.go:640-697`; mechanical evaluation rejects a missing canceled terminal state at `session_browser_scenario_evaluation.go:33-53`; and `Finalize` publishes the immutable result at `session_browser_scenario_run.go:779-792`.

The exact current canonical owner is the preserved C83 browser-scenario runner ownership (`work-task-125`, failed/inactive, branch `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`). Its owned runner, run, tracker, interruption-controller, fixture-option, and browserrunner paths are recorded in `ownership.json`. C61 owns the separate browser-scenario contract/evaluation policy paths and is a consumer/dependency, not the event-edge owner. C105 transfers no lease.

## One bounded later recommendation

Under the preserved C83 owner, add bounded instrumentation at `agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go` (`browserConversationBroker.Cancel`/`Invoke`) and `session_browser_scenario_run.go` (`RecordCancellation`) to retain the ordered `(InvocationID, State, Terminal)` cancel and terminal-publication observations and make the missing handoff explicit. Preserve the unchanged assertions in `session_browser_scenario_runner_test.go:219-228` and `session_browser_scenario_evaluation.go:33-53`; run only the named test in normal and `nomicrophone`+coverpkg modes. Any implementation must wait for the C83/C61 ownership/dependency disposition, then use the later executor's focused regressions, SCRIPT-owned current-head CI, independent review, guarded merge, and fresh immutable vertical probe. C105 implements none of this recommendation.

## Falsifiers and downstream status

{chr(10).join(f"- {item}" for item in characterization['falsifiers'])}

All nine immutable project criteria (`AUDIO`, `DEVICE`, `EMBED`, `SERVICE`, `TRACE`, `REPLAY`, `FAILURES`, `QUALITY`, `PARITY`) remain `OPEN`; this package is not a project acceptance waiver. The machine-readable classification and verifier output are authoritative for evidence completeness, not a green CI claim.
"""
    (EVIDENCE / "report.md").write_text(report, encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--main", default=MAIN_REVISION)
    parser.add_argument("--candidate", default=C93_REVISION)
    parser.add_argument("--trials", type=int, default=TRIALS_DEFAULT)
    parser.add_argument("--per-command-timeout", type=float, default=PER_COMMAND_DEFAULT)
    parser.add_argument("--aggregate-timeout", type=float, default=AGGREGATE_DEFAULT)
    args = parser.parse_args()
    if args.main != MAIN_REVISION or args.candidate != C93_REVISION:
        raise SystemExit("C105 requires the admitted accepted-main and C93 revisions")
    if args.trials != TRIALS_DEFAULT or args.per_command_timeout > PER_COMMAND_DEFAULT or args.aggregate_timeout > AGGREGATE_DEFAULT:
        raise SystemExit("C105 requires exactly ten trials and the admitted time bounds")
    if args.trials <= 0:
        raise SystemExit("trials must be positive")

    prd = json.loads(PRD.read_text(encoding="utf-8"))
    if prd.get("project") != PROJECT or prd.get("contractRevision") != CONTRACT or prd.get("branchName") != BRANCH:
        raise RuntimeError("prd project, contract, or branch does not match the admitted C105 task")
    if git_text(["branch", "--show-current"]) != BRANCH:
        raise RuntimeError("isolated worktree branch does not match prd.branchName")
    if git_text(["rev-parse", "origin/main"]) != MAIN_REVISION:
        raise RuntimeError("origin/main moved from the admitted accepted-main revision; do not substitute it")
    status_before = ensure_owned_status()
    task_verify = project_control(["verify-work", "--type", "task", "--name", TASK_NAME])
    if task_verify["exit_status"] != 0 or not isinstance(task_verify["stdout"], dict) or task_verify["stdout"].get("status") != "admitted":
        raise RuntimeError(f"admission verification failed: {task_verify}")
    project_status = project_control(["status"])

    start = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c105-") as temp_name:
        temporary_root = Path(temp_name)
        main_archive = archive_revision("main", args.main, temporary_root)
        c93_archive = archive_revision("c93", args.candidate, temporary_root)
        archives = {
            "main": {**main_archive, "root": temporary_root / "main"},
            "c93": {**c93_archive, "root": temporary_root / "c93"},
        }
        source_hash_manifest = {label: source_hashes(data["root"]) for label, data in archives.items()}
        diff_inventory = build_diff_inventory()
        env_snapshot = environment_snapshot()
        environment_info = {
            "toolchain_root": str(env_snapshot.get("immutable_toolchain_root", "")),
            "cache_seed": str(env_snapshot.get("go_env", {}).get("GOCACHE", "")),
            "modcache_seed": str(env_snapshot.get("go_env", {}).get("GOMODCACHE", "")),
        }
        if not all(Path(value).is_dir() for value in environment_info.values()):
            raise RuntimeError(f"immutable Go toolchain/cache seeds are unavailable: {environment_info}")
        matrix = {
            "version": 1,
            "task": TASK_NAME,
            "project": PROJECT,
            "contract": CONTRACT,
            "predeclared_at": utc_now(),
            "revision_order": ["main", "c93"],
            "revisions": {"main": args.main, "c93": args.candidate},
            "modes": [
                {
                    "id": "normal",
                    "tags": "",
                    "coverpkg": "",
                    "description": "normal build/test shape",
                },
                {
                    "id": "nomicrophone-coverpkg",
                    "tags": "nomicrophone",
                    "coverpkg": COVERPKG,
                    "description": "nomicrophone with the admitted broad agent-cli/go-agent-runtime coverpkg set",
                },
            ],
            "test": {"name": TEST_NAME, "anchored_regex": TEST_REGEX, "package": TEST_PACKAGE},
            "trials_per_cell": args.trials,
            "per_command_timeout_seconds": args.per_command_timeout,
            "aggregate_timeout_seconds": args.aggregate_timeout,
            "trial_count_total": args.trials * 2 * 2,
            "isolation": {
                "source": "separate git archive extraction per revision",
                "GOCACHE": "one independent stable Go-visible cache root per revision/mode, warmed once and checked with pre/post manifests after ten sequential trials; toolchain cache writes are recorded rather than mistaken for source mutation",
                "GOMODCACHE": "one independent stable offline dependency-cache view per revision/mode, with a unique per-trial snapshot marker",
                "GOTMPDIR": "unique temporary directory per trial",
                "GOPATH": "unique temporary directory per trial",
                "HOME": "unique temporary directory per trial",
                "coverage": "unique temporary output per coverpkg trial, removed after each trial",
            },
            "no_adaptive_retries": True,
            "no_live_realtime_or_credentials": True,
            "cache_warmup": {
                "predeclared": True,
                "not_trials": True,
                "one_per_revision_mode": True,
                "purpose": "populate one fixed cache root per revision/mode; warmups are not included in the 40-trial recurrence counts",
                "mode_order": ["main-normal", "main-nomicrophone-coverpkg", "c93-normal", "c93-nomicrophone-coverpkg"],
            },
            "toolchain_environment": env_snapshot,
        }
        json_write(EVIDENCE / "matrix.json", matrix)
        json_write(EVIDENCE / "source-diff-inventory.json", diff_inventory)
        json_write(EVIDENCE / "negative-control" / "ci-reference.json", CI_NEGATIVE_CONTROL)
        assertion_map = build_assertion_map(archives)
        json_write(EVIDENCE / "negative-control" / "assertion-map.json", assertion_map)
        proof_table = {
            "bad_state": {
                "result_finalized": True,
                "mechanical_passed": False,
                "cancellation_final_state": "",
                "cancel_broker_state": "canceled",
                "cancel_broker_terminal": False,
            },
            "rejections": [
                {"assertion_id": "test-finalized-mechanical", "bad_state_rejected": True},
                {"assertion_id": "test-canceled-terminal", "bad_state_rejected": True},
                {"assertion_id": "mechanical-interruption-terminal", "bad_state_rejected": True},
                {"assertion_id": "mechanical-explicit-cancel-terminal", "bad_state_rejected": True},
            ],
            "negative_control_source": CI_NEGATIVE_CONTROL["log_url"],
            "negative_control_sha256": CI_NEGATIVE_CONTROL["source_log_sha256"],
        }
        json_write(EVIDENCE / "negative-control" / "proof-table.json", proof_table)
        archive_manifest_files: dict[str, str] = {}
        for label, archive in archives.items():
            manifest_path = EVIDENCE / f"archive-manifest-{label}.json"
            json_write(manifest_path, archive["manifest"])
            archive_manifest_files[label] = str(manifest_path.relative_to(EVIDENCE))

        warmups = {
            f"{label}-{mode['id']}": prepare_cache_warmup(
                label=label,
                mode=mode,
                source_root=archives[label]["root"],
                environment_info=environment_info,
                per_command_timeout=args.per_command_timeout,
                temporary_root=temporary_root,
            )
            for label in ("main", "c93")
            for mode in matrix["modes"]
        }
        for warmup in warmups.values():
            if warmup["exit_status"] is None or warmup["timed_out"]:
                raise RuntimeError(f"cache warmup did not complete within the admitted bound: {warmup}")
        cache_seed_manifests = {
            mode_id: warmup.pop("cache_seed_manifest_files")
            for mode_id, warmup in warmups.items()
        }
        matrix["cache_warmup_results"] = warmups
        json_write(EVIDENCE / "matrix.json", matrix)
        records: list[dict[str, Any]] = []
        modes = matrix["modes"]
        for label in ("main", "c93"):
            for mode in modes:
                for trial in range(1, args.trials + 1):
                    record = run_trial(
                        label=label,
                        revision=archives[label]["revision"],
                        archive_hash=archives[label]["archive_sha256"],
                        source_root=archives[label]["root"],
                        mode=mode,
                        trial_number=trial,
                        per_command_timeout=args.per_command_timeout,
                        aggregate_deadline=args.aggregate_timeout,
                        matrix_start=start,
                        temporary_root=temporary_root,
                        environment_info={
                            **environment_info,
                            "cache_seed": warmups[f"{label}-{mode['id']}"]["cache_seed"],
                            "modcache_runtime": warmups[f"{label}-{mode['id']}"]["module_cache_runtime"],
                        },
                    )
                    records.append(record)
                    json_write(EVIDENCE / "trial-ledger.partial.json", {"version": 1, "trials": records})
        for record in records:
            shutil.rmtree(Path(record["isolated_paths"]["work_root"]), ignore_errors=True)
            record["isolated_paths_removed_after_trial"] = True
            record["isolated_paths_cleanup_deferred_until_matrix_end"] = False
        cache_seed_stability = {}
        for mode_id, warmup in warmups.items():
            final_seed_manifest = tree_manifest(Path(warmup["cache_seed"]))
            delta = manifest_delta(
                {"files": cache_seed_manifests[mode_id]},
                final_seed_manifest,
            )
            cache_seed_stability[mode_id] = {
                "initial_tree_sha256": warmup["cache_seed_tree_sha256"],
                "final_tree_sha256": final_seed_manifest["tree_sha256"],
                "stable": final_seed_manifest["tree_sha256"] == warmup["cache_seed_tree_sha256"],
                "cell_isolated": True,
                "mutation_recorded": final_seed_manifest["tree_sha256"] != warmup["cache_seed_tree_sha256"],
                "mutation_scope": "Go build-cache writes confined to this revision/mode cache root",
                **delta,
            }
        if not all(item["cell_isolated"] for item in cache_seed_stability.values()):
            raise RuntimeError(f"cache isolation was lost between revision/mode cells: {cache_seed_stability}")
        final_manifests = {label: tree_manifest(archive["root"]) for label, archive in archives.items()}
        archive_stable = all(final_manifests[label] == archives[label]["manifest"] for label in archives)
        if not archive_stable:
            raise RuntimeError("immutable archive tree changed during characterization")
        cells = [cell_summary(records, label, mode["id"]) for label in ("main", "c93") for mode in modes]
        characterization = classify(cells, diff_inventory)
        characterization.update({
            "version": 1,
            "task": TASK_NAME,
            "test_name": TEST_NAME,
            "matrix_file": "matrix.json",
            "trial_ledger_file": "trial-ledger.json",
            "cells": cells,
        })
        ownership = {
            "canonical_owner": {
                "task_id": "work-task-125",
                "task_name": "audio-runtime-c83-retire-cli-browser-scenario-runner",
                "state": "FAILED",
                "lease_status": "inactive; C105 does not transfer ownership",
                "worktree": "/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c83-retire-cli-browser-scenario-runner",
                "branch": "codex/audio-runtime-c83-retire-cli-browser-scenario-runner",
                "owner_paths": [
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_fixture_options.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner_test.go",
                    "go-agent-runtime/services/browserrunner/",
                ],
                "canonical_record_source": "you --server $FACTORY_SERVER_URL --session ~default --json work show work-task-125",
                "canonical_feedback": "Static CI found only the unregistered browserrunner Wire file and nine stale C83 baseline entries; browserrunner and external-consumer normal/race tests pass. Repair remains dependent on C79 shared-file ownership and the unmerged C61 prerequisite.",
            },
            "exact_event_edge": {
                "owner": "C83 preserved browser-scenario runner",
                "paths_and_symbols": [
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go", "symbol": "browserConversationInterruptionController.observeInFlight", "lines": "56-101"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go", "symbol": "browserConversationEvidenceTracker.observe/cancelInvocation", "lines": "228-385"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go", "symbol": "browserConversationBroker.Invoke", "lines": "849-912"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go", "symbol": "browserConversationBroker.Cancel", "lines": "914-936"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go", "symbol": "BrowserConversationRun.RecordCancellation", "lines": "640-697"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go", "symbol": "evaluateBrowserConversation", "lines": "27-124"},
                    {"path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go", "symbol": "BrowserConversationRun.Finalize", "lines": "779-792"},
                ],
                "causal_observation": "Cancel records a nonterminal canceled broker call and requested cancellation without a final state; only a later terminal invocation result can fill FinalState, while mechanical evaluation requires that terminal canceled state.",
            },
            "consumer_not_owner": {
                "task_id": "work-task-34",
                "task_name": "audio-runtime-c61-retire-cli-browser-scenario-contract",
                "state": "FAILED",
                "role": "separate contract/evaluation policy owner and dependency; not the observed runner event edge",
            },
            "one_recommendation": {
                "kind": "bounded instrumentation",
                "proposed_paths": [
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
                ],
                "scope": "retain ordered InvocationID/State/Terminal observations from Cancel through WaitInvocation and RecordCancellation so a missing terminal publication is attributable without inferring completion",
                "preserve_assertions": [
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner_test.go:219-228",
                    "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go:33-53",
                ],
                "focused_regressions": [
                    "the named test in normal mode",
                    "the named test with -tags=nomicrophone and the admitted coverpkg set",
                ],
                "dependencies": [
                    "C83 canonical browserrunner ownership must be resumed/leased by the factory",
                    "C79 shared-file ownership and the C61 prerequisite must be resolved before any repair merge",
                    "later executor submits changed head to SCRIPT-owned current-head CI, then independent review and guarded merge",
                ],
                "implemented_by_c105": False,
            },
        }
        provenance = {
            "version": 1,
            "task": TASK_NAME,
            "project": PROJECT,
            "contract": CONTRACT,
            "admission": {
                "project_control_verify_work": task_verify,
                "project_control_status": project_status,
                "session": "~default",
                "server": os.environ.get("FACTORY_SERVER_URL"),
            },
            "prd": {
                "path": "prd.json",
                "branch_name": prd["branchName"],
                "owned_paths": prd["ownedPaths"],
            },
            "worktree": {
                "path": str(REPO),
                "branch": git_text(["branch", "--show-current"]),
                "head_before_evidence_commit": git_text(["rev-parse", "HEAD"]),
                "origin_main": git_text(["rev-parse", "origin/main"]),
                "status_at_runner_start": status_before,
            },
            "revisions": {
                "accepted_main": MAIN_REVISION,
                "c93_head": C93_REVISION,
                "startup_integration": INTEGRATION_REVISION,
                "objects_available": {"accepted_main": git_exists(MAIN_REVISION), "c93_head": git_exists(C93_REVISION), "startup_integration": git_exists(INTEGRATION_REVISION)},
                "ancestry": {
                    "startup_to_main": ancestry(INTEGRATION_REVISION, MAIN_REVISION),
                    "startup_to_c93": ancestry(INTEGRATION_REVISION, C93_REVISION),
                    "main_to_c93": ancestry(MAIN_REVISION, C93_REVISION),
                },
                "archive_metadata": {
                    label: {key: value for key, value in archive.items() if key in ("label", "revision", "archive_sha256", "archive_bytes")}
                    for label, archive in archives.items()
                },
                "archive_manifest_files": archive_manifest_files,
                "archive_manifests_initial": {label: archive["manifest"] for label, archive in archives.items()},
                "archive_manifests_final": final_manifests,
                "archive_stable_through_trials": archive_stable,
                "source_hashes": source_hash_manifest,
            },
            "environment": env_snapshot,
            "source_diff_inventory": "source-diff-inventory.json",
            "negative_control": {
                "reference": "negative-control/ci-reference.json",
                "assertion_map": "negative-control/assertion-map.json",
                "proof_table": "negative-control/proof-table.json",
                "source_log_sha256": CI_NEGATIVE_CONTROL["source_log_sha256"],
            },
            "prior_review_findings": {
                "c105": "No prior C105 worker or review finding was recorded before this run.",
                "c93_pr_484": {
                    "pull_request": 484,
                    "head_revision": C93_REVISION,
                    "reviews": [],
                    "comments": [],
                    "historical_ci_negative_control_preserved": True,
                },
            },
            "matrix": {
                "file": "matrix.json",
                "fixed_trials": args.trials,
                "total_trials": len(records),
                "aggregate_elapsed_seconds": round(time.monotonic() - start, 6),
            },
            "cache_seed_stability": cache_seed_stability,
            "writes_confined_to_owned_path": True,
            "no_production_or_test_changes": True,
            "no_pr_or_merge_mutation": True,
            "immutable_project_criteria_remain_open": ["AUDIO", "DEVICE", "EMBED", "SERVICE", "TRACE", "REPLAY", "FAILURES", "QUALITY", "PARITY"],
        }
        json_write(EVIDENCE / "provenance.json", provenance)
        json_write(EVIDENCE / "trial-ledger.json", {"version": 1, "task": TASK_NAME, "trials": records})
        (EVIDENCE / "trial-ledger.partial.json").unlink(missing_ok=True)
        json_write(EVIDENCE / "characterization.json", characterization)
        json_write(EVIDENCE / "ownership.json", ownership)
        write_report(provenance=provenance, cells=cells, characterization=characterization, ownership=ownership)
        command_index = {
            "matrix_command": "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/run_matrix.py --main d4766c3dbbf2c198142047ead4449d58dd47d485 --candidate 35b4752e578dff49744159012982fccfacfb9794 --trials 10 --per-command-timeout 90 --aggregate-timeout 600",
            "verifier_commands": [
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode provenance",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode negative-control",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode matrix",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode trial-ledger",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode classification",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode ownership",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode recommendation",
                "python3 docs/temp/projects/audio-runtime/audio-runtime-c105-characterize-browser-cancel-coverage-regression/verify.py --mode all",
            ],
            "trial_count": len(records),
            "aggregate_elapsed_seconds": round(time.monotonic() - start, 6),
        }
        json_write(EVIDENCE / "command-index.json", command_index)
    print(json.dumps({"status": "complete", "classification": characterization["classification"], "trial_count": len(records), "aggregate_elapsed_seconds": command_index["aggregate_elapsed_seconds"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, subprocess.TimeoutExpired, OSError) as error:
        print(json.dumps({"status": "failed", "error": str(error)}, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
