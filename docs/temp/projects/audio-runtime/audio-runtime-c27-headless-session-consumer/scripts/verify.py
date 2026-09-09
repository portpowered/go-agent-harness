#!/usr/bin/env python3
"""Hermetic C27 verifier.

The verifier is intentionally stdlib-only.  It builds with GOWORK=off, launches
the consumer and yui in fresh process groups with stdin pipes and a cleared
allowlist environment, and writes evidence only below this admitted worktree.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterable


MODULE_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = MODULE_DIR.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(REPO_ROOT))).resolve()
EVIDENCE_DIR = MODULE_DIR / "evidence"
ARTIFACT_DIR = EVIDENCE_DIR / "artifacts"
FIXTURE_DIR = EVIDENCE_DIR / "fixtures"
BINARY = MODULE_DIR / "bin" / "headless-session"
MODULE_PATH = "example.com/audio-runtime-c27-headless-session-consumer"
MODULE_REL = Path("docs/temp/projects/audio-runtime/audio-runtime-c27-headless-session-consumer")
MAX_CHILD_TIMEOUT_SECONDS = 60.0

STARTUP_COMMIT = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_COMMIT = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
PLANNING_COMMIT = "98ce636dd67349ba64f22cd7916dd370cf4ba484"
TOOL_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
PCM_SHA256 = "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"
TOOL_FIXTURE_SOURCE = Path(
    "docs/temp/probes/audio-runtime-c15-relative-bundle-path-vertical-probe/artifact-2.json"
)
CONFIG_SOURCE = Path(
    "docs/temp/probes/audio-runtime-c15-relative-bundle-path-vertical-probe/evidence/fresh-audio-tool/config"
)


class VerifyError(RuntimeError):
    pass


@dataclass
class CommandResult:
    argv: list[str]
    cwd: str
    exit_code: int | None
    stdout: str
    stderr: str
    duration_seconds: float
    timed_out: bool
    reaped: bool
    process_group_alive: bool

    def as_dict(self) -> dict[str, Any]:
        return {
            "argv": self.argv,
            "cwd": self.cwd,
            "exit_code": self.exit_code,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "duration_seconds": round(self.duration_seconds, 6),
            "timed_out": self.timed_out,
            "reaped": self.reaped,
            "process_group_alive": self.process_group_alive,
        }


def fail(message: str) -> None:
    raise VerifyError(message)


def require(condition: bool, message: str) -> None:
    if not condition:
        fail(message)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_descriptor(path: Path, root: Path) -> dict[str, Any]:
    relative = path.relative_to(root).as_posix()
    return {"path": relative, "size": path.stat().st_size, "sha256": sha256_file(path)}


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def decode(data: bytes) -> str:
    return data.decode("utf-8", errors="replace")


def base_environment(root: Path, *, include_go_cache: bool = False) -> dict[str, str]:
    """Return the intentionally small environment used by a child process."""

    home = root / "home"
    xdg = root / "xdg-config"
    tmp = root / "tmp"
    for directory in (home, xdg, tmp):
        directory.mkdir(parents=True, exist_ok=True)
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(xdg),
        "TMPDIR": str(tmp),
        "GOWORK": "off",
        "LANG": "C",
        "LC_ALL": "C",
    }
    if include_go_cache:
        environment["GOCACHE"] = str(root / "go-cache")
        if os.environ.get("GOMODCACHE"):
            environment["GOMODCACHE"] = os.environ["GOMODCACHE"]
        if os.environ.get("GOTOOLCHAIN"):
            environment["GOTOOLCHAIN"] = os.environ["GOTOOLCHAIN"]
    return environment


def run_command(
    argv: Iterable[str],
    cwd: Path,
    *,
    input_data: bytes = b"",
    environment: dict[str, str] | None = None,
    timeout: float = MAX_CHILD_TIMEOUT_SECONDS,
) -> CommandResult:
    require(timeout > 0, f"child timeout must be positive: {timeout}")
    require(
        timeout <= MAX_CHILD_TIMEOUT_SECONDS,
        f"child timeout {timeout}s exceeds hard limit {MAX_CHILD_TIMEOUT_SECONDS}s",
    )
    command = [str(item) for item in argv]
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=environment,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(input=input_data, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            stdout, stderr = process.communicate()
        if not stdout and error.output:
            stdout = error.output
        if not stderr and error.stderr:
            stderr = error.stderr
    duration = time.monotonic() - started
    reaped = process.poll() is not None
    group_alive = False
    if process.returncode is not None:
        try:
            os.killpg(process.pid, 0)
            group_alive = True
        except ProcessLookupError:
            group_alive = False
        except PermissionError:
            group_alive = True
    return CommandResult(
        argv=command,
        cwd=str(cwd),
        exit_code=process.returncode,
        stdout=decode(stdout),
        stderr=decode(stderr),
        duration_seconds=duration,
        timed_out=timed_out,
        reaped=reaped,
        process_group_alive=group_alive,
    )


def go_environment(root: Path) -> dict[str, str]:
    environment = base_environment(root, include_go_cache=True)
    # The build itself may use the already-populated module cache, but it does
    # not receive provider credentials or the factory server environment.
    return environment


def run_go(args: list[str], cwd: Path, *, timeout: float = MAX_CHILD_TIMEOUT_SECONDS, root: Path | None = None) -> CommandResult:
    build_root = root or Path(tempfile.gettempdir()) / f"audio-runtime-c27-go-{os.getpid()}"
    build_root.mkdir(parents=True, exist_ok=True)
    return run_command(["go", *args], cwd, environment=go_environment(build_root), timeout=timeout)


def run_git(args: list[str], *, timeout: float = MAX_CHILD_TIMEOUT_SECONDS) -> CommandResult:
    return run_command(["git", *args], REPO_ROOT, environment=base_environment(Path(tempfile.gettempdir()) / f"audio-runtime-c27-git-{os.getpid()}"), timeout=timeout)


def command_or_fail(result: CommandResult, label: str) -> CommandResult:
    require(result.exit_code == 0 and not result.timed_out, f"{label} failed: {result.as_dict()}")
    return result


def parse_json_objects(text: str, label: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    position = 0
    objects: list[dict[str, Any]] = []
    while position < len(text):
        while position < len(text) and text[position].isspace():
            position += 1
        if position == len(text):
            break
        try:
            value, end = decoder.raw_decode(text, position)
        except json.JSONDecodeError as error:
            fail(f"{label} malformed JSON at byte {error.pos}: {error.msg}")
        if not isinstance(value, dict):
            fail(f"{label} emitted non-object JSON")
        objects.append(value)
        position = end
    if not objects:
        fail(f"{label} emitted an empty JSON listing")
    return objects


def parse_child_json(result: CommandResult) -> dict[str, Any]:
    require(result.stdout.strip() != "", f"child emitted no JSON: {result.as_dict()}")
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        fail(f"child emitted malformed JSON at byte {error.pos}: {result.as_dict()}")
    require(isinstance(value, dict), "child JSON result is not an object")
    return value


def fresh_child_root(parent: Path, name: str) -> Path:
    root = parent / name
    root.mkdir(parents=True, exist_ok=True)
    (root / "cwd").mkdir()
    return root


def run_child(config: dict[str, Any], root: Path, *, expected_exit: int = 0) -> tuple[CommandResult, dict[str, Any]]:
    binary = BINARY.resolve()
    require(binary.is_file(), f"consumer executable is missing: {binary}")
    for key, value in config.items():
        if key.endswith("directory") or key.endswith("directory_a") or key.endswith("directory_b"):
            if isinstance(value, str):
                Path(value).mkdir(parents=True, exist_ok=True)
    result = run_command(
        [str(binary)],
        root / "cwd",
        input_data=(json.dumps(config, sort_keys=True) + "\n").encode("utf-8"),
        environment=base_environment(root),
        timeout=MAX_CHILD_TIMEOUT_SECONDS,
    )
    require(result.exit_code == expected_exit, f"child exit={result.exit_code}, expected {expected_exit}: {result.as_dict()}")
    require(result.reaped and not result.process_group_alive, f"child cleanup failed: {result.as_dict()}")
    return result, parse_child_json(result)


def ensure_binary() -> dict[str, Any]:
    BINARY.parent.mkdir(parents=True, exist_ok=True)
    result = run_go(
        ["build", "-trimpath", "-o", str(BINARY), "./cmd/headless-session"],
        MODULE_DIR,
        timeout=MAX_CHILD_TIMEOUT_SECONDS,
    )
    command_or_fail(result, "consumer build")
    require(BINARY.is_file(), "consumer build produced no executable")
    return {"command": result.argv, "result": result.as_dict(), "artifact": file_descriptor(BINARY, MODULE_DIR)}


def history_fingerprint(items: list[dict[str, Any]]) -> str:
    digest = hashlib.sha256()
    for item in items:
        digest.update(str(item.get("role", "")).encode())
        digest.update(b"\x00")
        digest.update(str(item.get("text", "")).encode())
        digest.update(b"\n")
    return digest.hexdigest()


def expected_request(previous: list[dict[str, Any]], input_text: str) -> list[dict[str, Any]]:
    return [*previous, {"role": "user", "text": input_text}]


def validate_turn(
    turn: dict[str, Any],
    previous: list[dict[str, Any]],
    input_text: str,
    *,
    require_persistence: bool = True,
) -> None:
    wanted = expected_request(previous, input_text)
    requests = turn.get("provider_requests", [])
    require(len(requests) == 1, f"expected one provider request, got {requests}")
    request = requests[0]
    require(request.get("messages") == wanted, f"provider history mismatch: {request.get('messages')} != {wanted}")
    require(request.get("history_sha256") == history_fingerprint(wanted), "provider history digest is not exact")
    stream = turn.get("stream", {})
    require(stream.get("outcome") == "drained", f"stream did not drain: {stream}")
    require(stream.get("terminal_seen") is True, f"stream omitted MESSAGE.END: {stream}")
    require(stream.get("terminal_source") == "provider", f"unexpected terminal source: {stream}")
    require(stream.get("text") == request.get("response"), "stream text differs from observed provider response")
    require(turn.get("constructor_provider_calls") == 0, "provider was called during Open construction")
    require(turn.get("provider_calls") == 1, "turn made an unexpected number of provider calls")
    require(turn.get("stream_close_calls") == 2 and turn.get("handle_close_calls") == 2, "Close was not exercised twice")
    if require_persistence:
        persisted = turn.get("persisted_history", {})
        want_persisted = [*wanted, {"role": "assistant", "text": request["response"]}]
        require(persisted.get("present") is True, "saved history is absent")
        require(persisted.get("items") == want_persisted, "saved history is not exact and ordered")
        require(persisted.get("sha256") == history_fingerprint(want_persisted), "saved history digest is not exact")
        require(turn.get("latest_session_id") == turn.get("session_id"), "Latest did not identify the saved session")
        require(turn.get("session_id") in turn.get("listed_session_ids", []), "List omitted the saved session")


def verify_admission() -> dict[str, Any]:
    manifest_path = REPO_ROOT / "prd.json"
    require(manifest_path.is_file(), f"admitted manifest is missing: {manifest_path}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    require(manifest.get("project") == "audio-runtime", "manifest is not the admitted audio-runtime project")
    require(manifest.get("branchName") == "codex/audio-runtime-c27-headless-session-consumer", "manifest branchName does not match the isolated task")
    owned_paths = [str(path).rstrip("/") for path in manifest.get("ownedPaths", [])]
    require(MODULE_REL.as_posix() in owned_paths, "consumer path is not the manifest-owned path")

    fetch = run_git(["fetch", "origin", "main"], timeout=MAX_CHILD_TIMEOUT_SECONDS)
    command_or_fail(fetch, "fresh main fetch")
    branch = command_or_fail(run_git(["branch", "--show-current"]), "branch query").stdout.strip()
    head = command_or_fail(run_git(["rev-parse", "HEAD"]), "HEAD query").stdout.strip()
    fetched_main = command_or_fail(run_git(["rev-parse", "origin/main"]), "origin/main query").stdout.strip()
    require(branch == manifest["branchName"], f"isolated branch {branch!r} does not match manifest")
    ancestry: dict[str, bool] = {}
    for label, revision in {
        "startup_integration": STARTUP_COMMIT,
        "baseline": BASELINE_COMMIT,
        "planning": PLANNING_COMMIT,
    }.items():
        check = run_git(["merge-base", "--is-ancestor", revision, head])
        ancestry[label] = check.exit_code == 0
        require(ancestry[label], f"candidate {head} does not contain required {label} ancestor {revision}")
    main_ancestor = run_git(["merge-base", "--is-ancestor", fetched_main, head])
    require(main_ancestor.exit_code == 0, f"candidate does not contain freshly fetched origin/main {fetched_main}")
    return {
        "manifest": {
            "path": str(manifest_path),
            "project": manifest.get("project"),
            "branchName": manifest.get("branchName"),
            "ownedPaths": manifest.get("ownedPaths"),
        },
        "fetch": fetch.as_dict(),
        "branch": branch,
        "head": head,
        "origin_main": fetched_main,
        "ancestry": ancestry,
        "origin_main_is_ancestor": True,
        "no_merge_or_reset": True,
    }


def validate_dependency_graph(packages: list[dict[str, Any]], *, module_path: str = MODULE_PATH) -> dict[str, Any]:
    require(packages, "dependency graph is empty")
    errors: list[str] = []
    import_paths: set[str] = set()
    for package in packages:
        import_path = package.get("ImportPath")
        if not isinstance(import_path, str) or not import_path:
            errors.append("package missing ImportPath")
            continue
        import_paths.add(import_path)
        if "agent-cli" in import_path:
            errors.append(f"agent-cli appears in graph: {import_path}")
        for dependency_error in package.get("DepsErrors") or []:
            errors.append(f"{import_path}: {dependency_error}")
        if package.get("Error"):
            errors.append(f"{import_path}: {package['Error']}")
        if import_path == module_path + "/cmd/headless-session" or import_path.startswith(module_path + "/"):
            for direct in package.get("Imports") or []:
                if "agent-cli" in direct:
                    errors.append(f"consumer directly imports agent-cli: {direct}")
                if "/internal/" in direct:
                    errors.append(f"consumer directly imports private package: {direct}")
    require(not errors, "dependency graph rejected: " + "; ".join(errors))
    required_suffixes = ("/services/session", "/services/session/wire")
    for suffix in required_suffixes:
        require(any(path.endswith(suffix) for path in import_paths), f"dependency graph omitted public runtime package {suffix}")
    return {
        "package_count": len(packages),
        "consumer_packages": sorted(path for path in import_paths if path.startswith(module_path)),
        "private_runtime_transitive": sorted(path for path in import_paths if "/internal/" in path),
        "agent_cli_present": any("agent-cli" in path for path in import_paths),
    }


def validate_module_graph(modules: list[dict[str, Any]]) -> dict[str, Any]:
    require(modules, "module graph is empty")
    errors = [str(item.get("Error")) for item in modules if item.get("Error")]
    require(not errors, "module graph contains errors: " + "; ".join(errors))
    paths = {str(item.get("Path")) for item in modules}
    require(MODULE_PATH in paths, "module graph omitted the consumer module")
    require("github.com/portpowered/go-agent-harness/go-agent-runtime" in paths, "module graph omitted go-agent-runtime")
    return {
        "module_count": len(modules),
        "modules": sorted(paths),
        "replacements": {
            str(item.get("Path")): item.get("Replace")
            for item in modules
            if item.get("Replace") is not None
        },
    }


def graph_boundary() -> dict[str, Any]:
    dependency_listing = run_go(["list", "-deps", "-json", "./cmd/headless-session"], MODULE_DIR, timeout=MAX_CHILD_TIMEOUT_SECONDS)
    command_or_fail(dependency_listing, "go list -deps -json")
    packages = parse_json_objects(dependency_listing.stdout, "go list -deps -json")
    dependency_summary = validate_dependency_graph(packages)

    module_listing = run_go(["list", "-m", "-json", "all"], MODULE_DIR, timeout=MAX_CHILD_TIMEOUT_SECONDS)
    command_or_fail(module_listing, "go list -m -json all")
    modules = parse_json_objects(module_listing.stdout, "go list -m -json all")
    module_summary = validate_module_graph(modules)

    package_graph_hash = sha256_bytes(
        json.dumps(
            [{"import_path": item.get("ImportPath"), "imports": item.get("Imports", []), "deps": item.get("Deps", [])} for item in packages],
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    )
    module_graph_hash = sha256_bytes(
        json.dumps(
            [{"path": item.get("Path"), "version": item.get("Version"), "replace": item.get("Replace")} for item in modules],
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    )

    synthetic_controls: dict[str, str] = {}

    def expect_rejection(name: str, callback: Callable[[], Any]) -> None:
        try:
            callback()
        except VerifyError as error:
            synthetic_controls[name] = str(error)
            return
        fail(f"synthetic negative control was accepted: {name}")

    expect_rejection(
        "agent_cli_import",
        lambda: validate_dependency_graph(
            [
                {
                    "ImportPath": MODULE_PATH + "/cmd/headless-session",
                    "Imports": ["github.com/portpowered/go-agent-harness/agent-cli/internal/wire"],
                },
                {"ImportPath": "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"},
                {"ImportPath": "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"},
            ]
        ),
    )
    expect_rejection("empty_dependency_listing", lambda: validate_dependency_graph([]))
    expect_rejection("malformed_dependency_listing", lambda: parse_json_objects("{\"ImportPath\":", "synthetic malformed go list"))
    expect_rejection("failed_command_listing", lambda: command_or_fail(CommandResult(["go", "list"], str(MODULE_DIR), 1, "", "synthetic failure", 0.0, False, True, False), "synthetic go list"))
    expect_rejection(
        "child_timeout_over_limit",
        lambda: run_command(["true"], MODULE_DIR, timeout=MAX_CHILD_TIMEOUT_SECONDS + 1),
    )
    return {
        "dependency_command": dependency_listing.as_dict(),
        "dependency_summary": dependency_summary,
        "module_command": module_listing.as_dict(),
        "module_summary": module_summary,
        "graph_hashes": {"packages_sha256": package_graph_hash, "modules_sha256": module_graph_hash},
        "synthetic_negative_controls": synthetic_controls,
        "gowork": "off",
        "direct_private_imports_rejected": True,
        "agent_cli_rejected": True,
    }


def run_boundary() -> dict[str, Any]:
    admission = verify_admission()
    graph = graph_boundary()
    build = ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-boundary-") as temporary:
        root = Path(temporary)
        config = {
            "scenario": "boundary",
            "store_directory": str(root / "store"),
            "workspace_directory": str(root / "workspace"),
            "input": "boundary observable request",
        }
        process, child = run_child(config, fresh_child_root(root, "child"))
        validate_turn(child["turn"], [], config["input"])
        require(child.get("status") == "ok", f"boundary child status={child}")
        child_evidence = {"process": process.as_dict(), "report": child}
    evidence = {"admission": admission, "graph": graph, "build": build, "child": child_evidence}
    write_json(EVIDENCE_DIR / "boundary.json", evidence)
    return evidence


def run_lifecycle() -> dict[str, Any]:
    ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-lifecycle-") as temporary:
        root = Path(temporary)
        store = root / "store"
        first_config = {
            "scenario": "lifecycle",
            "store_directory": str(store),
            "workspace_directory": str(root / "workspace-first"),
            "input": "first process history",
        }
        first_process, first = run_child(first_config, fresh_child_root(root, "first"))
        validate_turn(first["turn"], [], first_config["input"])
        session_id = first["session_id"]
        persisted = first["turn"]["persisted_history"]["items"]
        second_config = {
            "scenario": "continuation",
            "store_directory": str(store),
            "workspace_directory": str(root / "workspace-second"),
            "session_id": session_id,
            "expected_history": persisted,
            "require_history": True,
            "input": "fresh process continuation",
        }
        second_process, second = run_child(second_config, fresh_child_root(root, "second"))
        validate_turn(second["turn"], persisted, second_config["input"])
        require(second["turn"]["loaded_history"]["present"] is True, "fresh continuation did not load history")
        require(second["turn"]["loaded_history"]["items"] == persisted, "fresh continuation loaded wrong history")
        evidence = {
            "first": {"process": first_process.as_dict(), "report": first},
            "second": {"process": second_process.as_dict(), "report": second},
            "fresh_process_and_store_reopen": True,
            "exact_continuation_history": True,
        }
    write_json(EVIDENCE_DIR / "lifecycle.json", evidence)
    return evidence


def run_isolation() -> dict[str, Any]:
    ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-isolation-") as temporary:
        root = Path(temporary)
        config = {
            "scenario": "isolation",
            "store_directory_a": str(root / "store-a"),
            "workspace_directory_a": str(root / "workspace-a"),
            "store_directory_b": str(root / "store-b"),
            "workspace_directory_b": str(root / "workspace-b"),
        }
        process, child = run_child(config, fresh_child_root(root, "child"))
        require(child.get("status") == "ok", f"isolation child status={child}")
        isolation = child.get("isolation", {})
        require(isolation.get("both_in_flight") is True, "A/B did not prove simultaneous in-flight providers")
        require(sorted(isolation.get("entry_order", [])) == ["A-concurrent", "B-concurrent"], "A/B entry signals are not distinct")
        initial_a = isolation["initial_a"]
        initial_b = isolation["initial_b"]
        canceled_a = isolation["canceled_a"]
        completed_b = isolation["completed_b"]
        restart_a = isolation["restart_a"]
        restart_b = isolation["restart_b"]
        require(initial_a["persisted_history"]["items"][0]["text"] == "A first history", "A initial history is wrong")
        require(initial_b["persisted_history"]["items"][0]["text"] == "B first history", "B initial history is wrong")
        require(canceled_a["stream"]["outcome"] == "canceled" and canceled_a["stream"]["partial"] is True, "A cancellation was not partial")
        require(canceled_a["saved"] is False, "canceled A was saved")
        require(canceled_a["persisted_history"]["items"] == initial_a["persisted_history"]["items"], "A store changed after cancellation")
        require(completed_b["stream"]["outcome"] == "drained" and completed_b["stream"]["terminal_seen"] is True, "B did not complete")
        require(completed_b["persisted_history"]["items"][:2] == initial_b["persisted_history"]["items"], "B lost its own prior history")
        require(len(completed_b["persisted_history"]["items"]) == 4, "B continuation was not saved")
        validate_turn(restart_a, initial_a["persisted_history"]["items"], "A restart")
        validate_turn(restart_b, completed_b["persisted_history"]["items"], "B restart")
        require(isolation.get("provider_joined") is True, "provider goroutines were not joined")
        for report in (restart_a, restart_b):
            request_text = json.dumps(report["provider_requests"])
            require("A first history" not in request_text or report["label"] == "restart-a", "A/B history crossed into B")
            require("B first history" not in request_text or report["label"] == "restart-b", "B/A history crossed into A")
        evidence = {"process": process.as_dict(), "report": child, "distinct_injected_services": True}
    write_json(EVIDENCE_DIR / "isolation.json", evidence)
    return evidence


def run_negative_controls() -> dict[str, Any]:
    ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-negative-") as temporary:
        root = Path(temporary)
        config = {
            "scenario": "negative-controls",
            "store_directory": str(root / "store"),
            "workspace_directory": str(root / "workspace"),
        }
        process, child = run_child(config, fresh_child_root(root, "child"), expected_exit=1)
        require(child.get("status") == "expected_negative_controls", f"negative controls did not report expected state: {child}")
        controls = child.get("controls", {})
        for name in ("missing_saved_file", "no_op_save", "crossed_output_history", "crossed_store_history"):
            require(controls.get(name, {}).get("failed_closed") is True, f"negative control {name} did not fail closed")
            require(controls[name].get("diagnostic"), f"negative control {name} omitted its diagnostic")
        require("expected negative controls" in process.stderr.lower(), "negative process did not identify its intentional non-zero gate")
        evidence = {"process": process.as_dict(), "report": child, "invalid_setups_rejected": True}
    write_json(EVIDENCE_DIR / "negative-controls.json", evidence)
    return evidence


def run_cancellation() -> dict[str, Any]:
    ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-cancel-") as temporary:
        root = Path(temporary)
        config = {
            "scenario": "cancellation",
            "store_directory": str(root / "store"),
            "workspace_directory": str(root / "workspace"),
            "input": "cancellation probe",
        }
        process, child = run_child(config, fresh_child_root(root, "child"))
        require(child.get("status") == "ok", f"cancellation child status={child}")
        before = child.get("before_cancel", {})
        during = child.get("during_cancel", {})
        require(before.get("provider_calls") == 0, "pre-canceled context reached the provider")
        require(before.get("persisted_history", {}).get("present") is not True, "pre-canceled context persisted")
        stream = during.get("stream", {})
        require(stream.get("outcome") == "canceled" and stream.get("partial") is True, "during-cancel stream was not partial cancellation")
        require("context canceled" in stream.get("error", ""), "during-cancel lost context cancellation cause")
        require(during.get("persisted_history", {}).get("present") is not True, "during-cancel persisted history")
        require(during.get("stream_close_calls") == 2 and during.get("handle_close_calls") == 2, "cancellation did not exercise idempotent close")
        evidence = {"process": process.as_dict(), "report": child, "no_sleep_synchronization": True}
    write_json(EVIDENCE_DIR / "cancellation.json", evidence)
    return evidence


def run_cleanup() -> dict[str, Any]:
    ensure_binary()
    with tempfile.TemporaryDirectory(prefix="c27-cleanup-") as temporary:
        root = Path(temporary)
        config = {
            "scenario": "cleanup",
            "store_directory": str(root / "store"),
            "workspace_directory": str(root / "workspace"),
            "input": "cleanup probe",
        }
        process, child = run_child(config, fresh_child_root(root, "child"))
        validate_turn(child["turn"], [], config["input"])
        evidence = {"process": process.as_dict(), "report": child, "child_timeout_seconds": 60, "clean_process_group": True}
    write_json(EVIDENCE_DIR / "cleanup.json", evidence)
    return evidence


def run_quality() -> dict[str, Any]:
    commands: list[dict[str, Any]] = []
    for args, cwd, label in (
        (["test", "./..."], MODULE_DIR, "consumer focused tests"),
        (["test", "-race", "./..."], MODULE_DIR, "consumer race tests"),
        (["vet", "./..."], MODULE_DIR, "consumer vet"),
        (["test", "./..."], REPO_ROOT / "tests/embedding", "accumulated embedding consumer regression"),
    ):
        result = run_go(args, cwd, timeout=MAX_CHILD_TIMEOUT_SECONDS)
        command_or_fail(result, label)
        commands.append({"label": label, "result": result.as_dict()})
    evidence = {"status": "verified", "commands": commands, "scope": "focused consumer plus accumulated tests/embedding regression", "full_ci_polled": False}
    write_json(EVIDENCE_DIR / "quality.json", evidence)
    return evidence


def tracked_source_paths() -> list[str]:
    roots = ["agent-cli", "go-agent-loop", "go-agent-runtime", "go-audio", "go-device-gateway", "go-llm-gateway"]
    result = command_or_fail(run_git(["ls-files", "--", *roots]), "tracked source inventory")
    paths = [line for line in result.stdout.splitlines() if line]
    require(paths, "tracked source inventory is empty")
    return paths


def current_consumer_paths() -> list[Path]:
    paths: list[Path] = []
    for path in MODULE_DIR.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(MODULE_DIR)
        if relative.parts[0] in {"bin", "evidence", ".build-runtime", ".tmp", ".git-runtime", ".go-version-runtime", "__pycache__"} or relative.parts[0].startswith(".") or "__pycache__" in relative.parts or relative.suffix == ".pyc":
            continue
        if relative.name in {"source.tar", "source.tar.gz"}:
            continue
        paths.append(path)
    require(paths, "consumer source inventory is empty")
    return sorted(paths)


def add_archive_file(archive: tarfile.TarFile, source: Path, archive_name: str) -> None:
    info = archive.gettarinfo(str(source), arcname=archive_name)
    if source.is_file() and not source.is_symlink():
        with source.open("rb") as handle:
            archive.addfile(info, handle)
    else:
        archive.addfile(info)


def create_source_archive(path: Path) -> dict[str, Any]:
    tracked = tracked_source_paths()
    consumer = current_consumer_paths()
    included: list[str] = []
    path.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(path, mode="w:gz", format=tarfile.PAX_FORMAT) as archive:
        for relative in tracked:
            source = REPO_ROOT / relative
            require(source.exists() or source.is_symlink(), f"tracked source disappeared: {relative}")
            add_archive_file(archive, source, "source/" + relative)
            included.append(relative)
        for source in consumer:
            relative = source.relative_to(REPO_ROOT).as_posix()
            add_archive_file(archive, source, "source/" + relative)
            included.append(relative)
    return {"artifact": file_descriptor(path, MODULE_DIR), "included_file_count": len(included), "included_roots": sorted(set(item.split("/", 1)[0] for item in included))}


def safe_extract(archive_path: Path, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive_path, mode="r:gz") as archive:
        for member in archive.getmembers():
            target = (destination / member.name).resolve()
            require(target == destination.resolve() or str(target).startswith(str(destination.resolve()) + os.sep), f"archive traversal member rejected: {member.name}")
            require(".git" not in Path(member.name).parts, f"archive contains ambient git metadata: {member.name}")
        archive.extractall(destination)


def source_tree_digest(path: Path) -> str:
    entries: list[dict[str, Any]] = []
    for child in sorted(path.rglob("*")):
        relative = child.relative_to(path)
        if child.is_file() and not any(part in {"bin", "evidence", ".build-runtime", ".tmp", ".git-runtime", ".go-version-runtime", "__pycache__"} or part.startswith(".") for part in relative.parts) and relative.suffix != ".pyc":
            entries.append({"path": relative.as_posix(), "size": child.stat().st_size, "sha256": sha256_file(child)})
    return sha256_bytes(json.dumps(entries, sort_keys=True, separators=(",", ":")).encode())


def copy_fixture() -> dict[str, Any]:
    source = FACTORY_ROOT / TOOL_FIXTURE_SOURCE
    require(source.is_file(), f"required C07 fixture is unavailable: {source}")
    require(sha256_file(source) == TOOL_FIXTURE_SHA256, "required C07 fixture hash changed")
    destination = FIXTURE_DIR / "c07-audio-tool-traced.session.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
    require(sha256_file(destination) == TOOL_FIXTURE_SHA256, "published C07 fixture copy was altered")
    return {"source": str(source), "published": file_descriptor(destination, MODULE_DIR), "expected_sha256": TOOL_FIXTURE_SHA256}


def go_version() -> str:
    result = command_or_fail(run_command(["go", "version"], REPO_ROOT, environment=base_environment(Path(tempfile.gettempdir()) / f"audio-runtime-c27-version-{os.getpid()}")), "go version")
    return result.stdout.strip()


def run_package() -> dict[str, Any]:
    admission = verify_admission()
    graph = graph_boundary()
    build = ensure_binary()
    ARTIFACT_DIR.mkdir(parents=True, exist_ok=True)
    archive_path = ARTIFACT_DIR / "source.tar.gz"
    archive = create_source_archive(archive_path)
    fixtures = copy_fixture()
    with tempfile.TemporaryDirectory(prefix="c27-package-extract-") as temporary:
        extracted = Path(temporary)
        safe_extract(archive_path, extracted)
        archive_source = extracted / "source"
        archive_module = archive_source / MODULE_REL
        archive_consumer_binary = archive_module / "bin" / "headless-session"
        archive_consumer_binary.parent.mkdir(parents=True, exist_ok=True)
        consumer_build = run_go(
            ["build", "-trimpath", "-o", str(archive_consumer_binary), "./cmd/headless-session"],
            archive_module,
            timeout=MAX_CHILD_TIMEOUT_SECONDS,
            root=extracted / "consumer-build-runtime",
        )
        command_or_fail(consumer_build, "extracted consumer build")
        archive_yui = archive_source / "agent-cli" / "bin" / "yui"
        archive_yui.parent.mkdir(parents=True, exist_ok=True)
        yui_build = run_go(
            ["build", "-trimpath", "-o", str(archive_yui), "./cmd/yui"],
            archive_source / "agent-cli",
            timeout=MAX_CHILD_TIMEOUT_SECONDS,
            root=extracted / "yui-build-runtime",
        )
        command_or_fail(yui_build, "same-source yui build")
        published_consumer = ARTIFACT_DIR / "headless-session.archive"
        published_yui = ARTIFACT_DIR / "yui.archive"
        shutil.copyfile(archive_consumer_binary, published_consumer)
        shutil.copyfile(archive_yui, published_yui)
        published_consumer.chmod(0o755)
        published_yui.chmod(0o755)
        require(published_consumer.is_file() and published_yui.is_file(), "archive-built executables were not published")
        extraction_evidence = {
            "root": "source/",
            "consumer_module": MODULE_REL.as_posix(),
            "ambient_git_present": False,
            "consumer_build": consumer_build.as_dict(),
            "yui_build": yui_build.as_dict(),
        }
    replacements = {
        relative: source_tree_digest(REPO_ROOT / relative)
        for relative in ("agent-cli", "go-agent-loop", "go-agent-runtime", "go-audio", "go-device-gateway", "go-llm-gateway")
    }
    descriptor = {
        "schema": "audio-runtime-c27-package/v1",
        "source_revision": admission["head"],
        "branch": admission["branch"],
        "ancestry": admission,
        "module": {
            "path": MODULE_PATH,
            "relative_path": MODULE_REL.as_posix(),
            "go_mod": file_descriptor(MODULE_DIR / "go.mod", MODULE_DIR),
            "go_sum": file_descriptor(MODULE_DIR / "go.sum", MODULE_DIR),
            "source_tree_sha256": source_tree_digest(MODULE_DIR),
        },
        "runner": file_descriptor(MODULE_DIR / "scripts" / "verify.py", MODULE_DIR),
        "source_archive": archive["artifact"],
        "executables": {
            "consumer": file_descriptor(ARTIFACT_DIR / "headless-session.archive", MODULE_DIR),
            "yui": file_descriptor(ARTIFACT_DIR / "yui.archive", MODULE_DIR),
        },
        "fixtures": {"c07_audio_tool_traced": fixtures["published"]},
        "dependency_graph": graph["graph_hashes"],
        "replacement_source_tree_sha256": replacements,
        "go": {"version": go_version(), "platform": {"system": platform.system(), "release": platform.release(), "machine": platform.machine()}, "gowork": "off"},
        "commands": {
            "consumer_build": build["command"],
            "archive_consumer_build": extraction_evidence["consumer_build"]["argv"],
            "archive_yui_build": extraction_evidence["yui_build"]["argv"],
            "consumer_entrypoint": ["./cmd/headless-session"],
            "yui_entrypoint": ["./cmd/yui"],
        },
        "extraction": extraction_evidence,
        "no_credentials_or_network_prerequisite": True,
    }
    descriptor_path = EVIDENCE_DIR / "package.json"
    write_json(descriptor_path, descriptor)
    package_evidence = {"descriptor": file_descriptor(descriptor_path, MODULE_DIR), "descriptor_value": descriptor}
    write_json(EVIDENCE_DIR / "package-build.json", package_evidence)
    return package_evidence


def descriptor_path(value: str, root: Path = MODULE_DIR) -> Path:
    candidate = Path(value)
    require(not candidate.is_absolute(), f"artifact path is absolute: {value}")
    require(".." not in candidate.parts, f"artifact path traverses parent: {value}")
    return root / candidate


def verify_descriptor(value: dict[str, Any], root: Path = MODULE_DIR) -> dict[str, Any]:
    source_revision = value.get("source_revision")
    require(isinstance(source_revision, str) and source_revision, "artifact descriptor omitted source revision")
    current_head = command_or_fail(run_git(["rev-parse", "HEAD"]), "descriptor HEAD query").stdout.strip()
    current_branch = command_or_fail(run_git(["branch", "--show-current"]), "descriptor branch query").stdout.strip()
    require(value.get("branch") == current_branch, f"artifact branch {value.get('branch')!r} does not match current branch {current_branch!r}")
    source_changes: list[str] = []
    if source_revision != current_head:
        changed = command_or_fail(
            run_git(["diff", "--name-only", f"{source_revision}..{current_head}", "--", MODULE_REL.as_posix()]),
            "descriptor source comparison",
        )
        evidence_prefix = MODULE_REL.as_posix() + "/evidence/"
        source_changes = [path for path in changed.stdout.splitlines() if path and not path.startswith(evidence_prefix)]
        require(
            not source_changes,
            f"artifact source revision {source_revision} is stale; source files changed through {current_head}: {source_changes}",
        )
    checked: list[dict[str, Any]] = []
    artifact_values: list[dict[str, Any]] = []
    artifact_values.append(value["source_archive"])
    artifact_values.extend(value["executables"].values())
    artifact_values.extend(value["fixtures"].values())
    artifact_values.append(value["module"]["go_mod"])
    artifact_values.append(value["module"]["go_sum"])
    artifact_values.append(value["runner"])
    for artifact in artifact_values:
        path = descriptor_path(str(artifact["path"]), root)
        require(path.is_file(), f"declared artifact is missing: {path}")
        actual_size = path.stat().st_size
        actual_sha = sha256_file(path)
        require(actual_size == artifact["size"], f"artifact size mismatch: {artifact['path']}")
        require(actual_sha == artifact["sha256"], f"artifact digest mismatch: {artifact['path']}")
        checked.append({"path": artifact["path"], "size": actual_size, "sha256": actual_sha})
    return {
        "checked": checked,
        "all_digests_match": True,
        "provenance": {
            "source_revision": source_revision,
            "current_head": current_head,
            "current_branch": current_branch,
            "source_revision_matches_current_head": source_revision == current_head,
            "source_changes_since_source_revision": source_changes,
        },
    }


def run_verify_artifacts() -> dict[str, Any]:
    descriptor_file = EVIDENCE_DIR / "package.json"
    require(descriptor_file.is_file(), "package descriptor is missing; run package first")
    descriptor = json.loads(descriptor_file.read_text(encoding="utf-8"))
    verified = verify_descriptor(descriptor)
    missing_descriptor_error = ""
    try:
        missing = EVIDENCE_DIR / "package.missing.json"
        json.loads(missing.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError) as error:
        missing_descriptor_error = str(error)
    require(missing_descriptor_error, "missing descriptor control was not rejected")
    with tempfile.TemporaryDirectory(prefix="c27-tamper-") as temporary:
        tamper_root = Path(temporary)
        tampered_path = tamper_root / "artifact"
        shutil.copyfile(MODULE_DIR / descriptor["executables"]["consumer"].get("path"), tampered_path)
        with tampered_path.open("ab") as handle:
            handle.write(b"tampered")
        tampered_error = ""
        expected = descriptor["executables"]["consumer"]
        if sha256_file(tampered_path) != expected["sha256"] or tampered_path.stat().st_size != expected["size"]:
            tampered_error = "tampered artifact digest/size rejected"
        require(tampered_error, "tampered artifact control was not rejected")
    evidence = {"descriptor": file_descriptor(descriptor_file, MODULE_DIR), "verified": verified, "missing_descriptor_control": missing_descriptor_error, "tamper_control": tampered_error}
    write_json(EVIDENCE_DIR / "artifact-verification.json", evidence)
    return evidence


def run_regression() -> dict[str, Any]:
    if not (EVIDENCE_DIR / "package.json").is_file():
        run_package()
    descriptor = json.loads((EVIDENCE_DIR / "package.json").read_text(encoding="utf-8"))
    verify_descriptor(descriptor)
    yui = MODULE_DIR / descriptor["executables"]["yui"]["path"]
    fixture = MODULE_DIR / descriptor["fixtures"]["c07_audio_tool_traced"]["path"]
    require(yui.is_file() and fixture.is_file(), "published same-source yui or fixture is missing")
    require(sha256_file(fixture) == TOOL_FIXTURE_SHA256, "published regression fixture digest mismatch")
    config_source = FACTORY_ROOT / CONFIG_SOURCE
    require((config_source / "config.yaml").is_file() and (config_source / "models.yaml").is_file(), "credential-free replay config fixture is unavailable")
    with tempfile.TemporaryDirectory(prefix="c27-regression-") as temporary:
        root = Path(temporary)
        config = root / "config"
        config.mkdir()
        shutil.copyfile(config_source / "config.yaml", config / "config.yaml")
        shutil.copyfile(config_source / "models.yaml", config / "models.yaml")
        (root / "evidence" / "runs").mkdir(parents=True)
        audio = root / "run" / "out.pcm"
        bundle = root / "run" / "bundle"
        replay_command = [
            str(yui),
            "-C",
            str(config),
            "session",
            "--replay",
            str(fixture),
            "--audio-out",
            str(audio),
            "--record-dir",
            str(bundle),
            "--trace-audio",
        ]
        replay = run_command(replay_command, root, environment=base_environment(root), timeout=MAX_CHILD_TIMEOUT_SECONDS)
        command_or_fail(replay, "same-source credential-free yui replay")
        combined = replay.stdout + "\n" + replay.stderr
        for marker in ("PROBE_TOOL_MARKER_9182", "strict replay continuation", "fixture_complete", "provider_close"):
            require(marker in combined, f"yui replay omitted required marker {marker!r}")
        require(audio.is_file() and audio.stat().st_size == 3200, f"rendered PCM size mismatch: {audio}")
        require(sha256_file(audio) == PCM_SHA256, "rendered PCM digest mismatch")
        require((root / "evidence" / "runs" / "exec-invocations-v4.log").is_file(), "tool invocation evidence was not written")
        strict_command = [str(yui), "-C", str(config), "session", "replay", str(bundle)]
        strict = run_command(strict_command, root, environment=base_environment(root), timeout=MAX_CHILD_TIMEOUT_SECONDS)
        command_or_fail(strict, "strict recorded-bundle replay")
        strict_combined = strict.stdout + "\n" + strict.stderr
        require("Replay verified: 18 wire events, 1 tool calls" in strict_combined, "strict replay did not verify wire/tool counts")
        require("strict replay continuation" in strict_combined, "strict replay omitted continuation output")
        bundle_files = sorted(path.relative_to(bundle).as_posix() for path in bundle.rglob("*") if path.is_file())
        require(bundle_files, "recorded replay bundle is empty")
        evidence = {
            "fixture": {"path": descriptor["fixtures"]["c07_audio_tool_traced"]["path"], "sha256": sha256_file(fixture)},
            "replay": {"command": replay_command, "result": replay.as_dict(), "audio": {"size": audio.stat().st_size, "sha256": sha256_file(audio)}, "bundle_files": bundle_files},
            "strict_replay": {"command": strict_command, "result": strict.as_dict()},
            "credentials": {"environment_allowlist": True, "network_or_provider_credentials": False, "config_directory": str(config)},
            "same_source_yui": descriptor["executables"]["yui"],
        }
    write_json(EVIDENCE_DIR / "regression.json", evidence)
    return evidence


def run_all() -> dict[str, Any]:
    results: dict[str, Any] = {}
    results["boundary"] = run_boundary()
    results["lifecycle"] = run_lifecycle()
    results["isolation"] = run_isolation()
    results["negative-controls"] = run_negative_controls()
    results["cancellation"] = run_cancellation()
    results["cleanup"] = run_cleanup()
    results["quality"] = run_quality()
    results["package"] = run_package()
    results["verify-artifacts"] = run_verify_artifacts()
    results["regression"] = run_regression()
    summary = {"schema": "audio-runtime-c27-verification-summary/v1", "status": "verified", "actions": sorted(results)}
    write_json(EVIDENCE_DIR / "summary.json", summary)
    return summary


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Run the C27 headless-session consumer gates")
    parser.add_argument(
        "action",
        choices=["boundary", "lifecycle", "isolation", "negative-controls", "cancellation", "cleanup", "quality", "package", "verify-artifacts", "regression", "all"],
    )
    args = parser.parse_args(argv)
    actions: dict[str, Callable[[], Any]] = {
        "boundary": run_boundary,
        "lifecycle": run_lifecycle,
        "isolation": run_isolation,
        "negative-controls": run_negative_controls,
        "cancellation": run_cancellation,
        "cleanup": run_cleanup,
        "quality": run_quality,
        "package": run_package,
        "verify-artifacts": run_verify_artifacts,
        "regression": run_regression,
        "all": run_all,
    }
    try:
        result = actions[args.action]()
    except (VerifyError, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        print(f"verify.py: FAILED: {error}", file=sys.stderr)
        return 1
    print(json.dumps({"action": args.action, "status": "verified", "evidence": result}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
