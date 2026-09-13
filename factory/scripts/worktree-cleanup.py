#!/usr/bin/env python3
"""Audit and optionally remove abandoned factory worktree scratch.

The command is deliberately conservative.  It asks the live Factory Session
for ownership before inspecting any candidate, keeps the current checkout and
managed Codex worktrees out of scope, and removes only ignored Python scratch,
    recognized build/test outputs in inactive managed worktrees, or old,
    clean, merged worktrees under approved worktree roots.

The default is a dry run.  Use ``--apply`` only after reviewing the report.
An unavailable or incomplete factory observation is a hard stop for every
mutation.  The script is intentionally standard-library-only so a daily
heartbeat can invoke it without a project build.
"""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
from typing import Any, Callable, Iterable, Mapping, Sequence
import urllib.error
import urllib.request


DEFAULT_SERVER = "http://127.0.0.1:7439"
DEFAULT_STATE = "docs/temp/projects/audio-runtime/worktree-cleanup-state.json"
DEFAULT_REPORT = "docs/temp/projects/audio-runtime/worktree-cleanup-report.json"
DEFAULT_YOU = "you"
COMMAND_TIMEOUT_SECONDS = 30
WORKTREE_AGE_HOURS = 1
SCRATCH_IDLE_HOURS = 1
MAX_WORKTREE_ENTRIES = 1000
MAX_REFERENCE_ROOTS = (
    "docs",
    "factory",
    "agent-cli",
    "go-agent-loop",
    "go-llm-gateway",
    "tests",
)
SCRATCH_DIR_NAMES = frozenset(
    {"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"}
)
GO_BUILD_CACHE_NAME = "go-build"
GO_BUILD_CACHE_MARKER = "cached build artifacts from the Go build system"
GO_TOOLS_CACHE_NAME = "go-tools"
STATICCHECK_CACHE_NAME = "staticcheck"
GLOBAL_CACHE_RETENTION_HOURS = 2
CACHE_KEY_LENGTH = 64
CACHE_ENTRY_SUFFIXES = ("-a", "-d")
OUTPUT_COVERAGE_PREFIX = "coverage"
SAFE_WORKTREE_PREFIXES = ("/private/tmp/", "/tmp/")
ACTIVE_WORKER_STATES = frozenset({"RESERVED", "STARTING", "RUNNING", "PAUSED"})
TERMINAL_STATE_NAMES = frozenset({"complete", "failed", "fin", "terminal"})


class CleanupBlocked(RuntimeError):
    """Raised when the safety boundary cannot be established."""


CommandRunner = Callable[..., subprocess.CompletedProcess[str]]


def utc_now() -> dt.datetime:
    """Return a timezone-aware UTC timestamp."""

    return dt.datetime.now(dt.timezone.utc)


def timestamp(value: dt.datetime | None = None) -> str:
    """Render timestamps consistently in reports and state."""

    return (value or utc_now()).isoformat(timespec="seconds").replace("+00:00", "Z")


def _text(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _run(
    command: Sequence[str],
    *,
    cwd: Path | None = None,
    timeout: int = COMMAND_TIMEOUT_SECONDS,
    runner: CommandRunner = subprocess.run,
) -> subprocess.CompletedProcess[str]:
    """Run a bounded command and convert launch failures into a hard stop."""

    try:
        return runner(
            list(command),
            cwd=str(cwd) if cwd is not None else None,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise CleanupBlocked(f"could not execute {' '.join(command[:2])}: {error}") from error


def _json_command(
    command: Sequence[str],
    *,
    cwd: Path | None = None,
    runner: CommandRunner = subprocess.run,
) -> Any:
    result = _run(command, cwd=cwd, runner=runner)
    if result.returncode != 0:
        details = (result.stderr or result.stdout or "").strip()
        if len(details) > 400:
            details = details[:400] + "..."
        raise CleanupBlocked(
            f"{' '.join(command[:5])} failed with exit {result.returncode}"
            + (f": {details}" if details else "")
        )
    try:
        return json.loads(result.stdout or "")
    except json.JSONDecodeError as error:
        raise CleanupBlocked(
            f"{' '.join(command[:5])} returned invalid JSON"
        ) from error


def _live_session_snapshot(
    you: str,
    server: str,
    *,
    runner: CommandRunner = subprocess.run,
) -> Any:
    """Read live sessions, bypassing a broken CLI compatibility route if needed."""

    command = [
        you,
        "--json",
        "--remote",
        "--server",
        server,
        "session",
        "list",
        "--live-only",
    ]
    try:
        return _json_command(command, runner=runner)
    except CleanupBlocked as cli_error:
        endpoint = server.rstrip("/") + "/factory-sessions"
        try:
            with urllib.request.urlopen(endpoint, timeout=COMMAND_TIMEOUT_SECONDS) as response:
                return json.load(response)
        except (OSError, urllib.error.URLError, ValueError) as http_error:
            raise CleanupBlocked(
                f"live Factory Session observation failed through CLI ({cli_error}) "
                f"and HTTP ({http_error})"
            ) from http_error


def repo_root(path: Path, *, runner: CommandRunner = subprocess.run) -> Path:
    """Resolve a checkout or linked worktree to its top-level path."""

    result = _run(
        ["git", "rev-parse", "--show-toplevel"], cwd=path, runner=runner
    )
    if result.returncode != 0 or not result.stdout.strip():
        raise CleanupBlocked("could not resolve the requested Git checkout")
    return Path(result.stdout.strip()).resolve()


def common_git_dir(root: Path, *, runner: CommandRunner = subprocess.run) -> Path:
    result = _run(
        ["git", "rev-parse", "--git-common-dir"], cwd=root, runner=runner
    )
    if result.returncode != 0 or not result.stdout.strip():
        raise CleanupBlocked("could not resolve the shared Git directory")
    path = Path(result.stdout.strip())
    if not path.is_absolute():
        path = root / path
    return path.resolve()


def _parse_worktrees(raw: str) -> list[dict[str, Any]]:
    """Parse Git's porcelain worktree records."""

    records: list[dict[str, Any]] = []
    current: dict[str, Any] | None = None
    for line in raw.splitlines() + [""]:
        if line.startswith("worktree "):
            if current is not None:
                records.append(current)
            current = {"path": line[len("worktree ") :]}
            continue
        if not line.strip():
            if current is not None:
                records.append(current)
                current = None
            continue
        if current is None:
            raise CleanupBlocked("git worktree list returned an invalid record")
        if line.startswith("HEAD "):
            current["head"] = line[len("HEAD ") :]
        elif line.startswith("branch "):
            current["branch"] = line[len("branch ") :]
        elif line == "detached":
            current["detached"] = True
        elif line.startswith("locked"):
            current["locked"] = True
        elif line.startswith("prunable"):
            current["prunable"] = True
    if len(records) > MAX_WORKTREE_ENTRIES:
        raise CleanupBlocked("git worktree list exceeded its safety bound")
    for record in records:
        if not _text(record.get("path")) or not _text(record.get("head")):
            raise CleanupBlocked("git worktree list omitted path or HEAD")
    return records


def list_worktrees(root: Path, *, runner: CommandRunner = subprocess.run) -> list[dict[str, Any]]:
    result = _run(
        ["git", "worktree", "list", "--porcelain"], cwd=root, runner=runner
    )
    if result.returncode != 0:
        raise CleanupBlocked("could not inspect Git worktrees")
    return _parse_worktrees(result.stdout)


def _state_name(marking: Mapping[str, Any]) -> str:
    place = _text(marking.get("placeId"))
    return place.rsplit(":", 1)[-1].lower() if place else ""


def _marking_names(session: Mapping[str, Any]) -> tuple[set[str], bool]:
    """Return non-terminal Work names and whether the board was complete."""

    runtime = session.get("runtime")
    if not isinstance(runtime, Mapping):
        raise CleanupBlocked("live session omitted runtime state")
    petri = runtime.get("petri")
    if not isinstance(petri, Mapping):
        raise CleanupBlocked("live session omitted the Petri board")
    marking = petri.get("marking")
    if not isinstance(marking, list):
        raise CleanupBlocked("live session omitted a valid Petri marking")
    names: set[str] = set()
    for item in marking:
        if not isinstance(item, Mapping):
            raise CleanupBlocked("live session returned an invalid board token")
        name = _text(item.get("name"))
        if not name:
            # Resource places are represented in the Petri marking as tokens
            # with workId/id but no Work name.  They carry capacity state, not
            # ownership of a checkout.  Every actual Work token still needs a
            # name so an active/queued worktree cannot be mistaken for idle.
            token_id = _text(item.get("id"))
            work_type = _text(item.get("workType"))
            place_id = _text(item.get("placeId"))
            if ":resource:" in token_id or place_id.endswith(":available"):
                continue
            raise CleanupBlocked("live board token omitted Work name")
        if _state_name(item) not in TERMINAL_STATE_NAMES:
            names.add(name)
    return names, True


def _session_running(session: Mapping[str, Any]) -> bool:
    runtime = session.get("runtime")
    if not isinstance(runtime, Mapping):
        raise CleanupBlocked("live session omitted runtime state")
    control = _text(runtime.get("lifecycleControlStatus")).upper()
    status = _text(runtime.get("status")).upper()
    progress = runtime.get("progress")
    factory_state = ""
    if isinstance(progress, Mapping):
        factory_state = _text(progress.get("factoryState")).upper()
    observed = {value for value in (control, status, factory_state) if value}
    if not observed:
        raise CleanupBlocked("live session omitted a liveness state")
    if "FAILED" in observed or "STOPPED" in observed or "TERMINATED" in observed:
        return False
    return bool(observed & {"RUNNING", "STARTING", "READY", "IDLE"})


def _worker_sessions(
    you: str,
    server: str,
    *,
    runner: CommandRunner = subprocess.run,
) -> list[Mapping[str, Any]]:
    response = _json_command(
        [you, "--json", "--server", server, "worker-sessions", "list"],
        runner=runner,
    )
    if not isinstance(response, Mapping):
        raise CleanupBlocked("Worker Session list returned a non-object")
    sessions = response.get("sessions")
    if not isinstance(sessions, list) or any(not isinstance(item, Mapping) for item in sessions):
        raise CleanupBlocked("Worker Session list returned an invalid sessions array")
    return list(sessions)


def _worker_names(sessions: Iterable[Mapping[str, Any]]) -> tuple[set[str], bool, int]:
    names: set[str] = set()
    unknown_active = False
    active_count = 0
    for session in sessions:
        state = _text(session.get("state")).upper()
        if state not in ACTIVE_WORKER_STATES:
            continue
        active_count += 1
        name = _text(session.get("workName"))
        ids = set()
        for key in ("workId",):
            value = _text(session.get(key))
            if value:
                ids.add(value)
        work_ids = session.get("workIds")
        if isinstance(work_ids, list):
            ids.update(value for value in work_ids if isinstance(value, str) and value.strip())
        if name:
            names.add(name)
        if not name and not ids:
            unknown_active = True
    return names, unknown_active, active_count


def factory_guard(
    you: str,
    server: str,
    *,
    runner: CommandRunner = subprocess.run,
) -> dict[str, Any]:
    """Read live session, board, and worker ownership as one safety snapshot."""

    response = _live_session_snapshot(you, server, runner=runner)
    if not isinstance(response, Mapping) or response.get("scope") != "live":
        raise CleanupBlocked("factory session list did not return live scope")
    sessions = response.get("sessions")
    if not isinstance(sessions, list) or not sessions:
        raise CleanupBlocked("no live factory session is available")
    live_sessions = [item for item in sessions if isinstance(item, Mapping)]
    if len(live_sessions) != len(sessions):
        raise CleanupBlocked("factory session list contained an invalid session")
    if not all(_session_running(session) for session in live_sessions):
        raise CleanupBlocked("a listed factory session is not in a running state")

    board_names: set[str] = set()
    for session in live_sessions:
        names, _ = _marking_names(session)
        board_names.update(names)

    # The live Session response contains the authoritative board and its
    # in-flight count.  The separate fleet Worker Session endpoint is not
    # required for checkout ownership and is intentionally avoided here: on
    # older runtimes it may be unavailable even while the live board remains
    # healthy.  Every non-terminal board token is conservatively treated as
    # queued/live ownership.
    active_workers = 0
    for session in live_sessions:
        runtime = session.get("runtime")
        progress = runtime.get("progress") if isinstance(runtime, Mapping) else None
        if isinstance(progress, Mapping):
            in_flight = progress.get("inFlightCount", 0)
            if isinstance(in_flight, bool) or not isinstance(in_flight, int) or in_flight < 0:
                raise CleanupBlocked("live session returned an invalid in-flight count")
            active_workers += in_flight
    worker_names = board_names

    serialized = json.dumps(response, sort_keys=True)
    return {
        "observedAt": timestamp(),
        "sessions": [
            {
                "id": _text(session.get("id")),
                "factoryDir": _text(session.get("factoryDir")),
                "folderPath": _text(session.get("folderPath")),
            }
            for session in live_sessions
        ],
        "boardWorkNames": sorted(board_names),
        "activeWorkerNames": sorted(worker_names),
        "activeWorkerCount": active_workers,
        "serializedSessionState": serialized,
    }


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.relative_to(parent)
        return True
    except ValueError:
        return False


def _is_managed_factory_worktree(path: Path) -> bool:
    """Recognize a Factory checkout without assuming which clone owns it."""

    return path.parent.name == "worktrees" and path.parent.parent.name == ".claude"


def _is_coverage_output(path: Path) -> bool:
    """Match generated coverage directories without matching coverage-manifest."""

    return path.name == OUTPUT_COVERAGE_PREFIX or path.name.startswith(
        OUTPUT_COVERAGE_PREFIX + "."
    )


def _apparent_size(path: Path) -> int:
    if path.is_symlink():
        return path.lstat().st_size
    if path.is_file():
        return path.stat().st_size
    total = 0
    for child in path.rglob("*"):
        try:
            if child.is_symlink():
                total += child.lstat().st_size
            elif child.is_file():
                total += child.stat().st_size
        except OSError as error:
            raise CleanupBlocked(f"could not measure {path}: {error}") from error
    return total


def _latest_mtime(path: Path) -> float:
    latest = path.stat().st_mtime
    for child in path.rglob("*"):
        try:
            latest = max(latest, child.lstat().st_mtime)
        except OSError as error:
            raise CleanupBlocked(f"could not inspect mtime for {path}: {error}") from error
    return latest


def _is_cache_entry(path: Path) -> bool:
    """Recognize content-addressed Go-style cache entries only."""

    name = path.name
    if len(name) != CACHE_KEY_LENGTH + 2 or name[-2:] not in CACHE_ENTRY_SUFFIXES:
        return False
    return all(character in "0123456789abcdefABCDEF" for character in name[:-2])


def _cache_entries(path: Path) -> Iterable[Path]:
    """Yield cache entries without following symlinked buckets or entries."""

    if path.is_symlink() or not path.is_dir():
        raise CleanupBlocked(f"global cache changed before inspection: {path}")
    try:
        buckets = list(path.iterdir())
    except OSError as error:
        raise CleanupBlocked(f"could not inspect global cache {path}: {error}") from error
    for bucket in buckets:
        if (
            bucket.is_symlink()
            or not bucket.is_dir()
            or len(bucket.name) != 2
            or any(character not in "0123456789abcdefABCDEF" for character in bucket.name)
        ):
            continue
        try:
            entries = list(bucket.iterdir())
        except OSError as error:
            raise CleanupBlocked(f"could not inspect global cache bucket {bucket}: {error}") from error
        for entry in entries:
            if _is_cache_entry(entry) and not entry.is_symlink():
                yield entry


def _cache_entry_size(path: Path, file_info: os.stat_result) -> int:
    """Measure one cache entry without following a symlink at its root."""

    if stat.S_ISREG(file_info.st_mode):
        return file_info.st_size
    if stat.S_ISDIR(file_info.st_mode):
        return _apparent_size(path)
    return 0


def _cache_entry_stats(
    path: Path,
    *,
    now: dt.datetime,
    minimum_age_hours: float,
) -> dict[str, int]:
    """Return total and safely reclaimable bytes in one shared cache."""

    if minimum_age_hours < 0:
        raise ValueError("minimum cache age must be non-negative")
    cutoff = now.timestamp() - (minimum_age_hours * 3600)
    total_bytes = 0
    reclaimable_bytes = 0
    reclaimable_count = 0
    entry_count = 0
    for entry in _cache_entries(path):
        try:
            file_info = entry.lstat()
            size = _cache_entry_size(entry, file_info)
        except FileNotFoundError:
            # Go may complete a concurrent cache write between enumeration and
            # inspection.  The next cleanup pass will see the final entry.
            continue
        except OSError as error:
            raise CleanupBlocked(f"could not inspect cache entry {entry}: {error}") from error
        entry_count += 1
        total_bytes += size
        if file_info.st_mtime < cutoff:
            reclaimable_bytes += size
            reclaimable_count += 1
    return {
        "totalBytes": total_bytes,
        "reclaimableBytes": reclaimable_bytes,
        "reclaimableCount": reclaimable_count,
        "entryCount": entry_count,
    }


def _ignored_clean(path: Path, root: Path, *, runner: CommandRunner = subprocess.run) -> tuple[bool, str]:
    """Require an ignored candidate with no tracked, untracked, or symlink files."""

    if path.is_symlink() or not path.is_dir():
        return False, "candidate is not a real directory"
    result = _run(
        [
            "git",
            "status",
            "--porcelain=v1",
            "--untracked-files=all",
            "--ignored",
            "--",
            str(path.relative_to(root)),
        ],
        cwd=root,
        runner=runner,
    )
    if result.returncode != 0:
        return False, "Git status failed"
    lines = [line for line in result.stdout.splitlines() if line.strip()]
    if any(not line.startswith("!! ") for line in lines):
        return False, "candidate contains tracked or non-ignored changes"
    if not lines:
        return False, "candidate is not ignored by Git"
    for child in path.rglob("*"):
        if child.is_symlink():
            return False, "candidate contains a symlink"
    return True, "ignored regenerable scratch"


def _contains_symlink(path: Path) -> bool:
    """Return whether a candidate contains a symlink at any depth."""

    if path.is_symlink():
        return True
    for child in path.rglob("*"):
        if child.is_symlink():
            return True
    return False


def _source_worktree_clean(
    path: Path,
    *,
    runner: CommandRunner = subprocess.run,
) -> tuple[bool, str]:
    """Require no tracked or untracked source changes in an owning worktree."""

    result = _run(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=path,
        runner=runner,
    )
    if result.returncode != 0:
        return False, "owning worktree status could not be inspected"
    if result.stdout.strip():
        return False, "owning worktree contains tracked or untracked changes"
    return True, "owning worktree has no tracked or untracked changes"


def scratch_candidates(
    root: Path,
    *,
    now: dt.datetime,
    runner: CommandRunner = subprocess.run,
) -> list[dict[str, Any]]:
    """Find old, ignored Python test scratch under the factory scripts tree."""

    scripts = root / "factory" / "scripts"
    if not scripts.is_dir():
        return []
    candidates: list[dict[str, Any]] = []
    for path in scripts.rglob("*"):
        if not path.is_dir() or path.name not in SCRATCH_DIR_NAMES or path.is_symlink():
            continue
        try:
            ok, reason = _ignored_clean(path, root, runner=runner)
            size = _apparent_size(path)
            age_hours = (now.timestamp() - _latest_mtime(path)) / 3600
        except (CleanupBlocked, OSError, ValueError) as error:
            candidates.append(
                {
                    "kind": "scratch",
                    "path": str(path),
                    "sizeBytes": 0,
                    "eligible": False,
                    "reason": f"inspection failed: {error}",
                }
            )
            continue
        if age_hours < SCRATCH_IDLE_HOURS:
            ok = False
            reason = f"modified {age_hours:.1f}h ago; idle threshold is {SCRATCH_IDLE_HOURS}h"
        candidates.append(
            {
                "kind": "scratch",
                "path": str(path),
                "sizeBytes": size,
                "eligible": ok,
                "reason": reason,
                "ageHours": round(age_hours, 1),
            }
        )
    return candidates


def _is_safe_temp_path(path: Path) -> bool:
    return any(str(path).startswith(prefix) for prefix in SAFE_WORKTREE_PREFIXES)


def _head_merged(
    head: str,
    base_ref: str,
    root: Path,
    *,
    runner: CommandRunner = subprocess.run,
) -> bool:
    result = _run(
        ["git", "merge-base", "--is-ancestor", head, base_ref],
        cwd=root,
        runner=runner,
    )
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    raise CleanupBlocked("could not establish worktree ancestry")


def _path_references(
    root: Path,
    common_dir: Path,
    path: Path,
    *,
    needles: Sequence[str] | None = None,
    runner: CommandRunner = subprocess.run,
) -> list[str]:
    """Search report-bearing text roots for candidate-specific references."""

    search_needles = list(needles or (str(path), path.name))
    roots = [root / relative for relative in MAX_REFERENCE_ROOTS if (root / relative).exists()]
    runs_dir = common_dir / "factory-runs"
    if runs_dir.exists():
        roots.append(runs_dir)
    references: set[str] = set()
    for needle in search_needles:
        command = [
            "rg",
            "--hidden",
            "-l",
            "-F",
            "--glob",
            "!.git/**",
            "--glob",
            "!.claude/**",
            "--glob",
            "!.cache/**",
            "--glob",
            "!.you-agent-factory/**",
            needle,
            *[str(item) for item in roots],
        ]
        result = _run(command, cwd=root, timeout=COMMAND_TIMEOUT_SECONDS, runner=runner)
        if result.returncode not in (0, 1):
            raise CleanupBlocked("artifact reference scan failed")
        references.update(line for line in result.stdout.splitlines() if line.strip())
    return sorted(references)


def build_cache_candidates(
    root: Path,
    common_dir: Path,
    guard: Mapping[str, Any],
    *,
    now: dt.datetime,
    minimum_age_hours: float = WORKTREE_AGE_HOURS,
    base_ref: str = "origin/main",
    runner: CommandRunner = subprocess.run,
) -> list[dict[str, Any]]:
    """Find recognized build/test outputs in inactive managed worktrees.

    Outputs are kept separate from whole-worktree removal. A managed worktree
    may still contain valuable reports, dirty source, or an unmerged branch;
    deleting a recognized ignored output does not discard any of those.
    Module/download caches are intentionally outside this daily boundary.
    """

    board_names = set(guard.get("boardWorkNames", []))
    board_names.update(guard.get("activeWorkerNames", []))
    result: list[dict[str, Any]] = []
    for record in list_worktrees(root, runner=runner):
        owner = Path(record["path"]).expanduser().resolve()
        if not _is_managed_factory_worktree(owner):
            continue
        if not owner.is_dir() or owner.is_symlink():
            continue
        try:
            outputs: list[tuple[Path, str]] = []
            outputs.extend(
                (path, "go-build")
                for path in owner.rglob(GO_BUILD_CACHE_NAME)
                if path.parent.name == "cache"
            )
            tools = owner / ".cache" / GO_TOOLS_CACHE_NAME
            if tools.is_dir():
                outputs.append((tools, "go-tools"))
            outputs.extend(
                (path, "coverage")
                for path in owner.glob(OUTPUT_COVERAGE_PREFIX + "*")
                if path.is_dir() and _is_coverage_output(path)
            )
        except OSError as error:
            result.append(
                {
                    "kind": "build-cache",
                    "path": str(owner),
                    "ownerPath": str(owner),
                    "sizeBytes": 0,
                    "eligible": False,
                    "reason": f"output discovery failed: {error}",
                }
            )
            continue
        for cache, output_type in sorted(set(outputs)):
            entry: dict[str, Any] = {
                "kind": "build-cache",
                "outputType": output_type,
                "path": str(cache),
                "ownerPath": str(owner),
                "ownerHead": record.get("head"),
                "sizeBytes": 0,
                "eligible": False,
            }
            if record.get("locked"):
                entry["reason"] = "owning worktree is locked"
                result.append(entry)
                continue
            if record.get("prunable"):
                entry["reason"] = "owning worktree is prunable or incompletely registered"
                result.append(entry)
                continue
            if owner.name in board_names:
                entry["reason"] = "owning Work name is live or queued in the factory"
                result.append(entry)
                continue
            if not cache.is_dir() or cache.is_symlink():
                entry["reason"] = "Go build cache is missing or symlinked"
                result.append(entry)
                continue
            try:
                entry["sizeBytes"] = _apparent_size(cache)
                observed_age_hours = (now.timestamp() - _latest_mtime(cache)) / 3600
                entry["ageHours"] = round(observed_age_hours, 1)
                if output_type == "go-build":
                    marker_path = cache / "README"
                    marker = marker_path.read_text(encoding="utf-8", errors="replace")
                    if GO_BUILD_CACHE_MARKER not in marker:
                        entry["reason"] = "cache lacks the Go regeneration marker"
                        result.append(entry)
                        continue
                ignored, ignored_reason = _ignored_clean(cache, owner, runner=runner)
                if not ignored:
                    entry["reason"] = ignored_reason
                    result.append(entry)
                    continue
                if observed_age_hours < minimum_age_hours:
                    entry["reason"] = (
                        f"modified {observed_age_hours:.1f}h ago; age threshold is "
                        f"{minimum_age_hours}h"
                    )
                    result.append(entry)
                    continue
                entry["eligible"] = True
                entry["reason"] = f"inactive worktree ignored {output_type} output"
            except (CleanupBlocked, OSError, ValueError) as error:
                entry["reason"] = f"inspection failed: {error}"
            result.append(entry)
    return result


def global_cache_candidates(
    root: Path,
    *,
    now: dt.datetime | None = None,
    minimum_age_hours: float = GLOBAL_CACHE_RETENTION_HOURS,
    cache_root: Path | None = None,
    runner: CommandRunner = subprocess.run,
) -> list[dict[str, Any]]:
    """Find old entries in known regenerable caches under the approved root."""

    now = now or utc_now()
    approved_root = (cache_root or (Path.home() / "Library" / "Caches")).resolve()
    configured_go_cache = os.environ.get("FACTORY_GOCACHE") or os.environ.get("GOCACHE")
    if configured_go_cache:
        go_cache_value = configured_go_cache
    else:
        go_cache_result = _run(["go", "env", "GOCACHE"], cwd=root, runner=runner)
        go_cache_value = (
            go_cache_result.stdout.strip() if go_cache_result.returncode == 0 else ""
        )
    paths: list[tuple[Path, str]] = []
    if go_cache_value:
        paths.append((Path(go_cache_value), "go-build"))
    paths.append((approved_root / STATICCHECK_CACHE_NAME, "staticcheck"))
    candidates: list[dict[str, Any]] = []
    for raw_path, output_type in paths:
        path = raw_path.expanduser().resolve()
        entry = {
            "kind": "global-build-cache",
            "outputType": output_type,
            "path": str(path),
            "sizeBytes": 0,
            "eligible": False,
        }
        if raw_path.expanduser().is_symlink():
            entry["reason"] = "cache is symlinked"
        elif path.parent != approved_root or path.name not in {
            GO_BUILD_CACHE_NAME,
            STATICCHECK_CACHE_NAME,
        }:
            entry["reason"] = "cache path is outside the approved user cache root"
        elif not path.is_dir():
            entry["reason"] = "cache is absent or not a directory"
        elif output_type == "go-build":
            try:
                marker = (path / "README").read_text(encoding="utf-8", errors="replace")
            except OSError as error:
                entry["reason"] = f"could not read the Go regeneration marker: {error}"
            else:
                if GO_BUILD_CACHE_MARKER not in marker:
                    entry["reason"] = "cache lacks the Go regeneration marker"
        if "reason" not in entry:
            try:
                stats = _cache_entry_stats(
                    path,
                    now=now,
                    minimum_age_hours=minimum_age_hours,
                )
            except (CleanupBlocked, OSError, ValueError) as error:
                entry["reason"] = f"inspection failed: {error}"
            else:
                entry["sizeBytes"] = stats["reclaimableBytes"]
                entry["totalSizeBytes"] = stats["totalBytes"]
                entry["cacheEntryCount"] = stats["entryCount"]
                entry["reclaimableEntryCount"] = stats["reclaimableCount"]
                entry["retentionHours"] = minimum_age_hours
                if stats["reclaimableCount"]:
                    entry["eligible"] = True
                    entry["reason"] = (
                        f"prune {stats['reclaimableCount']} unused {output_type} "
                        f"entries older than {minimum_age_hours:g}h under disk pressure"
                    )
                else:
                    entry["reason"] = (
                        f"no {output_type} entries older than "
                        f"{minimum_age_hours:g}h"
                    )
        candidates.append(entry)
    return candidates


def worktree_candidates(
    root: Path,
    common_dir: Path,
    guard: Mapping[str, Any],
    *,
    now: dt.datetime,
    minimum_age_hours: float = WORKTREE_AGE_HOURS,
    base_ref: str = "origin/main",
    runner: CommandRunner = subprocess.run,
) -> list[dict[str, Any]]:
    """Return only old, clean, merged, unreferenced temporary worktrees."""

    board_names = set(guard.get("boardWorkNames", []))
    board_names.update(guard.get("activeWorkerNames", []))
    result: list[dict[str, Any]] = []
    for record in list_worktrees(root, runner=runner):
        path = Path(record["path"]).expanduser().resolve()
        managed_factory = _is_managed_factory_worktree(path)
        entry: dict[str, Any] = {
            "kind": "worktree",
            "path": str(path),
            "head": record.get("head"),
            "branch": record.get("branch"),
            "sizeBytes": 0,
            "eligible": False,
        }
        if path == root or _is_within(path, root / ".you-agent-factory"):
            entry["reason"] = "managed/current checkout"
            result.append(entry)
            continue
        if not managed_factory and not _is_safe_temp_path(path):
            entry["reason"] = "outside temporary or Factory-managed worktree roots"
            result.append(entry)
            continue
        if record.get("locked"):
            entry["reason"] = "worktree is locked"
            result.append(entry)
            continue
        if record.get("prunable"):
            entry["reason"] = "prunable or incomplete Git registration"
            result.append(entry)
            continue
        if not path.is_dir() or path.is_symlink():
            entry["reason"] = "worktree path is missing or symlinked"
            result.append(entry)
            continue
        if path.name in board_names:
            entry["reason"] = "Work name is live or queued in the factory"
            result.append(entry)
            continue
        try:
            entry["sizeBytes"] = _apparent_size(path)
            observed_age_hours = (now.timestamp() - _latest_mtime(path)) / 3600
            entry["ageHours"] = round(observed_age_hours, 1)
            if observed_age_hours < minimum_age_hours:
                entry["reason"] = (
                    f"modified {observed_age_hours:.1f}h ago; age threshold is "
                    f"{minimum_age_hours}h"
                )
                result.append(entry)
                continue
            status = _run(
                ["git", "status", "--porcelain=v1", "--untracked-files=all", "--ignored"],
                cwd=path,
                runner=runner,
            )
            if status.returncode != 0:
                entry["reason"] = "Git status could not inspect worktree"
                result.append(entry)
                continue
            if status.stdout.strip():
                entry["reason"] = "dirty or ignored worktree content"
                result.append(entry)
                continue
            if not _head_merged(record["head"], base_ref, root, runner=runner):
                entry["reason"] = f"HEAD is not an ancestor of {base_ref}"
                result.append(entry)
                continue
            references = _path_references(root, common_dir, path, runner=runner)
            entry["references"] = references
            if references:
                entry["reason"] = "path or basename is referenced by evidence/runtime text"
                result.append(entry)
                continue
            entry["eligible"] = True
            entry["reason"] = "clean merged inactive temporary worktree"
        except (CleanupBlocked, OSError, ValueError) as error:
            entry["reason"] = f"inspection failed: {error}"
        result.append(entry)
    return result


def _disk_free(path: Path) -> int:
    try:
        return shutil.disk_usage(path).free
    except OSError as error:
        raise CleanupBlocked(f"could not measure free disk space: {error}") from error


def _remove_scratch(
    path: Path,
    root: Path,
    *,
    runner: CommandRunner = subprocess.run,
) -> None:
    if not _is_within(path.resolve(), root / "factory" / "scripts"):
        raise CleanupBlocked(f"scratch path escaped factory/scripts: {path}")
    if path.is_symlink() or not path.is_dir():
        raise CleanupBlocked(f"scratch path changed before removal: {path}")
    clean, reason = _ignored_clean(path, root, runner=runner)
    if not clean:
        raise CleanupBlocked(f"scratch path changed before removal: {reason}")
    shutil.rmtree(path)


def _remove_build_cache(
    path: Path,
    owner: Path,
    root: Path,
    output_type: str,
    *,
    runner: CommandRunner = subprocess.run,
) -> None:
    """Remove one recognized Go build cache after an ownership recheck."""

    owner = owner.resolve()
    path = path.resolve()
    if not _is_managed_factory_worktree(owner) or not _is_within(path, owner):
        raise CleanupBlocked(f"build cache escaped its managed worktree: {path}")
    if path.is_symlink() or not path.is_dir():
        raise CleanupBlocked(f"build/test output changed before removal: {path}")
    if output_type == "go-build":
        if path.name != GO_BUILD_CACHE_NAME or path.parent.name != "cache":
            raise CleanupBlocked(f"Go build cache identity changed: {path}")
        marker_path = path / "README"
        try:
            marker = marker_path.read_text(encoding="utf-8", errors="replace")
        except OSError as error:
            raise CleanupBlocked(f"could not recheck Go build cache marker: {error}") from error
        if GO_BUILD_CACHE_MARKER not in marker:
            raise CleanupBlocked("Go build cache regeneration marker changed")
    elif output_type == "go-tools":
        if path != owner / ".cache" / GO_TOOLS_CACHE_NAME:
            raise CleanupBlocked(f"Go tools cache identity changed: {path}")
    elif output_type == "coverage":
        if path.parent != owner or not _is_coverage_output(path):
            raise CleanupBlocked(f"coverage output identity changed: {path}")
    else:
        raise CleanupBlocked(f"unknown build/test output type: {output_type}")
    ignored, reason = _ignored_clean(path, owner, runner=runner)
    if not ignored:
        raise CleanupBlocked(f"build/test output changed before removal: {reason}")
    shutil.rmtree(path)


def _remove_global_cache(
    path: Path,
    output_type: str,
    *,
    now: dt.datetime | None = None,
    minimum_age_hours: float = GLOBAL_CACHE_RETENTION_HOURS,
    cache_root: Path | None = None,
) -> int:
    """Prune old entries from one shared cache while retaining recent entries."""

    approved_root = (cache_root or (Path.home() / "Library" / "Caches")).resolve()
    raw_path = path.expanduser()
    if raw_path.is_symlink():
        raise CleanupBlocked(f"global cache changed to a symlink: {path}")
    path = raw_path.resolve()
    expected_name = GO_BUILD_CACHE_NAME if output_type == "go-build" else STATICCHECK_CACHE_NAME
    if path.parent != approved_root or path.name != expected_name:
        raise CleanupBlocked(f"global cache escaped its approved root: {path}")
    if not path.is_dir():
        raise CleanupBlocked(f"global cache changed before pruning: {path}")
    if output_type == "go-build":
        try:
            marker = (path / "README").read_text(encoding="utf-8", errors="replace")
        except OSError as error:
            raise CleanupBlocked(f"could not recheck Go build cache marker: {error}") from error
        if GO_BUILD_CACHE_MARKER not in marker:
            raise CleanupBlocked("Go build cache regeneration marker changed")
    now = now or utc_now()
    cutoff = now.timestamp() - (minimum_age_hours * 3600)
    reclaimed_bytes = 0
    for entry in list(_cache_entries(path)):
        try:
            file_info = entry.lstat()
            if file_info.st_mtime >= cutoff:
                continue
            size = _cache_entry_size(entry, file_info)
            if stat.S_ISDIR(file_info.st_mode):
                shutil.rmtree(entry)
            elif stat.S_ISREG(file_info.st_mode):
                entry.unlink()
            else:
                continue
        except FileNotFoundError:
            # Concurrent Go writers may finish or replace an entry between
            # the snapshot and removal.  Never turn that normal race into a
            # factory-wide cleanup failure.
            continue
        except OSError as error:
            raise CleanupBlocked(f"could not prune cache entry {entry}: {error}") from error
        reclaimed_bytes += size
    return reclaimed_bytes


def _remove_worktree(path: Path, root: Path, *, runner: CommandRunner = subprocess.run) -> None:
    if (
        not _is_safe_temp_path(path)
        and not _is_managed_factory_worktree(path)
    ) or path == root:
        raise CleanupBlocked(f"worktree path is outside the cleanup boundary: {path}")
    result = _run(["git", "worktree", "remove", str(path)], cwd=root, runner=runner)
    if result.returncode != 0:
        details = (result.stderr or result.stdout or "").strip()
        raise CleanupBlocked(
            f"Git refused to remove {path}" + (f": {details[:300]}" if details else "")
        )


def _atomic_json(path: Path, value: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=path.parent
    )
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(value, stream, indent=2, sort_keys=True)
            stream.write("\n")
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def _initial_report(
    *,
    root: Path,
    server: str,
    guard: Mapping[str, Any] | None,
    scratch: list[dict[str, Any]],
    build_caches: list[dict[str, Any]],
    worktrees: list[dict[str, Any]],
    mode: str,
    now: dt.datetime,
    free_before: int,
    blocked_reason: str | None = None,
) -> dict[str, Any]:
    candidates = scratch + build_caches + worktrees
    eligible = [item for item in candidates if item.get("eligible")]
    return {
        "schemaVersion": 1,
        "generatedAt": timestamp(now),
        "mode": mode,
        "root": str(root),
        "server": server,
        "factory": {
            "available": guard is not None,
            "observedAt": guard.get("observedAt") if guard else None,
            "sessionIds": [item.get("id") for item in (guard or {}).get("sessions", [])],
            "activeWorkerCount": (guard or {}).get("activeWorkerCount"),
            "boardWorkNameCount": len((guard or {}).get("boardWorkNames", [])),
        },
        "blockedReason": blocked_reason,
        "freeBeforeBytes": free_before,
        "candidateCount": len(candidates),
        "eligibleCount": len(eligible),
        "candidates": candidates,
        "deleted": [],
        "apparentBytesConsidered": sum(int(item.get("sizeBytes", 0)) for item in eligible),
        "freeAfterBytes": None,
        "freeDeltaBytes": None,
    }


def _refresh_guard(
    you: str,
    server: str,
    *,
    runner: CommandRunner = subprocess.run,
) -> dict[str, Any]:
    return factory_guard(you, server, runner=runner)


def execute(
    *,
    root: Path,
    you: str,
    server: str,
    base_ref: str,
    apply: bool,
    pressure_trigger_bytes: int | None = None,
    pressure_target_bytes: int | None = None,
    worktree_age_hours: float = WORKTREE_AGE_HOURS,
    now: dt.datetime | None = None,
    runner: CommandRunner = subprocess.run,
) -> dict[str, Any]:
    """Perform one bounded audit and optional apply, returning its report."""

    now = now or utc_now()
    root = root.resolve()
    free_before = _disk_free(root)
    if apply and pressure_trigger_bytes is not None and free_before >= pressure_trigger_bytes:
        report = _initial_report(
            root=root,
            server=server,
            guard=None,
            scratch=[],
            build_caches=[],
            worktrees=[],
            mode="pressure-skip",
            now=now,
            free_before=free_before,
        )
        report["freeAfterBytes"] = free_before
        report["freeDeltaBytes"] = 0
        report["pressure"] = {
            "triggerFreeBytes": pressure_trigger_bytes,
            "targetFreeBytes": pressure_target_bytes,
        }
        return report
    try:
        guard = _refresh_guard(you, server, runner=runner)
    except CleanupBlocked as error:
        report = _initial_report(
            root=root,
            server=server,
            guard=None,
            scratch=[],
            build_caches=[],
            worktrees=[],
            mode="apply" if apply else "dry-run",
            now=now,
            free_before=free_before,
            blocked_reason=str(error),
        )
        return report

    common_dir = common_git_dir(root, runner=runner)
    global_caches = (
        global_cache_candidates(
            root,
            now=now,
            minimum_age_hours=GLOBAL_CACHE_RETENTION_HOURS,
            runner=runner,
        )
        if pressure_trigger_bytes is not None
        else []
    )
    scratch = scratch_candidates(root, now=now, runner=runner)
    build_caches = build_cache_candidates(
        root,
        common_dir,
        guard,
        now=now,
        minimum_age_hours=worktree_age_hours,
        base_ref=base_ref,
        runner=runner,
    )
    worktrees = worktree_candidates(
        root,
        common_dir,
        guard,
        now=now,
        minimum_age_hours=worktree_age_hours,
        base_ref=base_ref,
        runner=runner,
    )
    report = _initial_report(
        root=root,
        server=server,
        guard=guard,
        scratch=global_caches + scratch,
        build_caches=build_caches,
        worktrees=worktrees,
        mode="apply" if apply else "dry-run",
        now=now,
        free_before=free_before,
    )
    if pressure_trigger_bytes is not None:
        report["pressure"] = {
            "triggerFreeBytes": pressure_trigger_bytes,
            "targetFreeBytes": pressure_target_bytes,
        }
    if not apply:
        return report

    # The factory state is independently re-read after the candidate scan and
    # immediately before any destructive operation.  A transient API failure
    # therefore leaves every not-yet-removed candidate untouched.
    try:
        _refresh_guard(you, server, runner=runner)
    except CleanupBlocked as error:
        report["blockedReason"] = str(error)
        report["freeAfterBytes"] = _disk_free(root)
        report["freeDeltaBytes"] = report["freeAfterBytes"] - free_before
        return report
    for candidate in report["candidates"]:
        if pressure_target_bytes is not None and _disk_free(root) >= pressure_target_bytes:
            break
        if not candidate.get("eligible"):
            continue
        try:
            _refresh_guard(you, server, runner=runner)
        except CleanupBlocked as error:
            report["blockedReason"] = str(error)
            break
        path = Path(candidate["path"])
        reclaimed_size: int | None = None
        try:
            if candidate["kind"] == "scratch":
                _remove_scratch(path, root, runner=runner)
            elif candidate["kind"] == "global-build-cache":
                reclaimed_size = _remove_global_cache(
                    path,
                    candidate["outputType"],
                    now=now,
                    minimum_age_hours=GLOBAL_CACHE_RETENTION_HOURS,
                )
                if reclaimed_size == 0:
                    candidate["eligible"] = False
                    candidate["reason"] = (
                        "no eligible old cache entries remained before pruning"
                    )
                    continue
            elif candidate["kind"] == "build-cache":
                _remove_build_cache(
                    path,
                    Path(candidate["ownerPath"]),
                    root,
                    candidate["outputType"],
                    runner=runner,
                )
            else:
                _remove_worktree(path, root, runner=runner)
        except CleanupBlocked as error:
            candidate["eligible"] = False
            candidate["reason"] = str(error)
            continue
        if reclaimed_size is not None:
            candidate["sizeBytes"] = reclaimed_size
        report["deleted"].append(
            {
                "kind": candidate["kind"],
                "path": candidate["path"],
                "sizeBytes": candidate.get("sizeBytes", 0),
            }
        )
    free_after = _disk_free(root)
    report["freeAfterBytes"] = free_after
    report["freeDeltaBytes"] = free_after - free_before
    return report


def _state_from_report(report: Mapping[str, Any], report_path: Path) -> dict[str, Any]:
    return {
        "schemaVersion": 1,
        "lastCleanupAt": report.get("generatedAt"),
        "lastReportAt": report.get("generatedAt"),
        "lastReport": str(report_path),
        "mode": report.get("mode"),
        "blockedReason": report.get("blockedReason"),
        "deleted": report.get("deleted", []),
        "deletedCount": len(report.get("deleted", [])),
        "deletedApparentBytes": sum(
            int(item.get("sizeBytes", 0)) for item in report.get("deleted", [])
        ),
        "freeBeforeBytes": report.get("freeBeforeBytes"),
        "freeAfterBytes": report.get("freeAfterBytes"),
        "freeDeltaBytes": report.get("freeDeltaBytes"),
    }


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--you", default=DEFAULT_YOU, help="you CLI path")
    parser.add_argument("--server", default=DEFAULT_SERVER)
    parser.add_argument("--base-ref", default="origin/main")
    parser.add_argument(
        "--worktree-age-hours",
        type=float,
        default=WORKTREE_AGE_HOURS,
        help="minimum idle age for removable worktrees (default: %(default)s)",
    )
    parser.add_argument("--state", type=Path, default=None)
    parser.add_argument("--report", type=Path, default=None)
    parser.add_argument(
        "--apply",
        action="store_true",
        help="apply eligible deletions; the default is a dry-run",
    )
    parser.add_argument(
        "--pressure-trigger-free-gib",
        type=float,
        default=None,
        help="with --apply, skip the audit when free space is at or above this threshold",
    )
    parser.add_argument(
        "--pressure-target-free-gib",
        type=float,
        default=None,
        help="stop deleting after free space reaches this higher hysteresis target",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="explicitly request the default non-mutating mode",
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.apply and args.dry_run:
        print("--apply and --dry-run are mutually exclusive", file=sys.stderr)
        return 2
    if args.worktree_age_hours < 0:
        print("--worktree-age-hours must be non-negative", file=sys.stderr)
        return 2
    if (args.pressure_trigger_free_gib is None) != (args.pressure_target_free_gib is None):
        print("pressure trigger and target must be specified together", file=sys.stderr)
        return 2
    if args.pressure_trigger_free_gib is not None and (
        args.pressure_trigger_free_gib < 0
        or args.pressure_target_free_gib <= args.pressure_trigger_free_gib
    ):
        print("pressure target must be greater than the non-negative trigger", file=sys.stderr)
        return 2
    root = repo_root(args.root)
    state_path = (args.state or root / DEFAULT_STATE).expanduser().resolve()
    report_path = (args.report or root / DEFAULT_REPORT).expanduser().resolve()
    common_dir = common_git_dir(root)
    lock_path = common_dir / "factory-cleanup.lock"
    with lock_path.open("a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("another factory cleanup is already running", file=sys.stderr)
            return 3
        gib = 1024 ** 3
        report = execute(
            root=root,
            you=args.you,
            server=args.server,
            base_ref=args.base_ref,
            apply=args.apply,
            pressure_trigger_bytes=(
                int(args.pressure_trigger_free_gib * gib)
                if args.pressure_trigger_free_gib is not None
                else None
            ),
            pressure_target_bytes=(
                int(args.pressure_target_free_gib * gib)
                if args.pressure_target_free_gib is not None
                else None
            ),
            worktree_age_hours=args.worktree_age_hours,
        )
        _atomic_json(report_path, report)
        if args.apply and report.get("blockedReason") is None:
            _atomic_json(state_path, _state_from_report(report, report_path))
    print(json.dumps(report, indent=2, sort_keys=True))
    if report.get("blockedReason"):
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
