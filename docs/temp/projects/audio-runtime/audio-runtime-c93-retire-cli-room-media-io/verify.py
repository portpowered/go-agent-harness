#!/usr/bin/env python3
"""Verify the C93 retirement, ownership, and public-boundary controls."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "--show-toplevel"],
        cwd=TASK_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
)
EXPECTED_BRANCH = "codex/audio-runtime-c93-retire-cli-room-media-io"
PLANNING_REVISION = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE = Path("agent-cli/internal/services/internal/agentruntime/session_room_run.go")
BASELINE_LINES = 1192
MAX_LINES = 972
OWNED_EXACT = {SOURCE.as_posix()}
OWNED_PREFIXES = (
    "go-agent-runtime/services/roommedia/",
    "coverage-manifest/go-agent-runtime/services/roommedia/",
    "docs/temp/projects/audio-runtime/audio-runtime-c93-retire-cli-room-media-io/",
)
FORBIDDEN_PEER_PREFIXES = (
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle",
    "agent-cli/internal/services/internal/agentruntime/session_room_replay_scheduler",
    "agent-cli/internal/services/internal/agentruntime/session_audio_out",
)
RETIRED_DECLARATIONS = (
    r"\bfunc\s+roomHumanOutputClock\b",
    r"\bfunc\s+encodeRoomPCM16\b",
    r"\btype\s+roomHumanOutputBuffer\b",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git(*args: str) -> str:
    return subprocess.run(
        ["rtk", "proxy", "git", *args],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def ancestor(revision: str, head: str) -> bool:
    return subprocess.run(
        ["rtk", "proxy", "git", "merge-base", "--is-ancestor", revision, head],
        cwd=REPO_ROOT,
    ).returncode == 0


def changed_paths() -> set[str]:
    paths = set(filter(None, git("diff", "--name-only", "origin/main...HEAD").splitlines()))
    paths.update(filter(None, git("diff", "--name-only").splitlines()))
    paths.update(filter(None, git("diff", "--cached", "--name-only").splitlines()))
    paths.update(filter(None, git("ls-files", "--others", "--exclude-standard").splitlines()))
    return paths


def is_owned(path: str) -> bool:
    return path in OWNED_EXACT or any(path.startswith(prefix) for prefix in OWNED_PREFIXES)


def verify_identity(head: str) -> dict[str, str]:
    branch = git("branch", "--show-current")
    require(branch == EXPECTED_BRANCH, f"branch mismatch: {branch!r}")
    require(ancestor(STARTUP_REVISION, head), "startup integration ancestry is missing")
    require(ancestor(PLANNING_REVISION, head), "planning origin ancestry is missing")
    origin_main = git("rev-parse", "origin/main")
    require(ancestor(origin_main, head), f"current origin/main is not in candidate ancestry: {origin_main}")
    baseline = git("show", f"{PLANNING_REVISION}:{SOURCE.as_posix()}").encode()
    require(len(baseline.splitlines()) == BASELINE_LINES, "planning source baseline is not 1,192 lines")
    return {
        "branch": branch,
        "head": head,
        "planning_origin_main": PLANNING_REVISION,
        "startup_integration": STARTUP_REVISION,
        "origin_main": origin_main,
        "baseline_sha256": sha256_bytes(baseline),
    }


def verify_source() -> dict[str, object]:
    current = (REPO_ROOT / SOURCE).read_bytes()
    text = current.decode()
    line_count = len(text.splitlines())
    require(line_count <= MAX_LINES, f"session_room_run.go has {line_count} lines; maximum is {MAX_LINES}")
    require(BASELINE_LINES - line_count >= 220, "retirement floor is not met")
    for declaration in RETIRED_DECLARATIONS:
        require(re.search(declaration, text) is None, f"retired declaration remains: {declaration}")
    for symbol in ("pumpRoomMixer", "roomProviderInputPCM", "runRoomHumanCapture", "pumpRoomHumanOutput"):
        require(re.search(rf"\bfunc\s+{symbol}\b", text) is not None, f"CLI adapter disappeared: {symbol}")
    census = TASK_ROOT / "source-census.md"
    require(census.is_file(), "pre-mutation source census is missing")
    census_text = census.read_text(encoding="utf-8")
    require("exactly 1,192 physical lines" in census_text, "source census does not pin the 1,192-line baseline")
    return {
        "path": SOURCE.as_posix(),
        "baseline_lines": BASELINE_LINES,
        "current_lines": line_count,
        "retired_lines": BASELINE_LINES - line_count,
        "current_sha256": sha256_bytes(current),
        "retired_declarations": ["roomHumanOutputClock", "encodeRoomPCM16", "roomHumanOutputBuffer"],
    }


def verify_scope() -> list[str]:
    changed = sorted(changed_paths())
    disallowed = [path for path in changed if not is_owned(path)]
    require(not disallowed, f"unowned changed paths: {disallowed}")
    peer_changes = [path for path in changed if any(path.startswith(prefix) for prefix in FORBIDDEN_PEER_PREFIXES)]
    require(not peer_changes, f"peer/shared ownership was touched: {peer_changes}")
    return changed


def verify_public_boundary() -> dict[str, object]:
    source_files = sorted((REPO_ROOT / "go-agent-runtime/services/roommedia").rglob("*.go"))
    require(source_files, "roommedia source is missing")
    text = "\n".join(path.read_text(encoding="utf-8") for path in source_files)
    forbidden = {
        "agent-cli": "CLI dependency",
        "os.Getenv": "ambient environment read",
        "os.LookupEnv": "ambient environment read",
        "func init(": "hidden initialization",
        "time.Sleep": "unbounded sleep",
        "Realtime": "provider-specific runtime coupling",
    }
    failures = [label for marker, label in forbidden.items() if marker in text]
    require(not failures, f"public roommedia boundary leaked: {failures}")
    return {
        "go_files": [path.relative_to(REPO_ROOT).as_posix() for path in source_files],
        "forbidden_markers": sorted(forbidden),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("retirement-and-owned-paths",))
    args = parser.parse_args()
    del args
    head = git("rev-parse", "HEAD")
    identity = verify_identity(head)
    source = verify_source()
    changed = verify_scope()
    boundary = verify_public_boundary()
    result = {
        "schema": "audio-runtime.c93.retirement-verification.v1",
        "passed": True,
        "identity": identity,
        "source": source,
        "changed_paths": changed,
        "public_boundary": boundary,
    }
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"schema": "audio-runtime.c93.retirement-verification.v1", "passed": False, "error": str(error)}, sort_keys=True))
        raise SystemExit(1)
