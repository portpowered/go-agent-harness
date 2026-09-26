#!/usr/bin/env python3
"""Fail a cached test package that depends on inputs Go's test cache cannot see.

scripts/go-test-fresh-split.sh runs every cacheable `go test` through this
guard (`go test -exec`). Go replays a cached test result only when the test
binary, its flags, and the environment variables and files the test process
read are unchanged, but it records files only inside the package's own
module, and never sees what a subprocess reads. So a cached package must not:

- open or stat a repository file outside its own module (another workspace
  module, the repository root; stat'ing the directories above the module, as
  filepath.EvalSymlinks does, is fine), per the binary's test log
  (-test.testlogfile, which `go test -exec` leaves to the wrapper: the guard
  writes it where `go test` reads it to cache the result); or
- exec `go`, `git` or another tool that reads repository sources
  (SHIMMED_TOOLS) from inside the repository or on a repository path. The
  guard puts logging shims for those tools first on PATH, so this also
  catches a TestMain or init() that builds binaries or runs `go list` before
  m.Run, which the test log does not cover.

A package that does either fails here (after its tests passed) with the
offending paths or commands: list it in scripts/go-test-fresh-packages.txt
so it always runs fresh. A failed run is never cached, so no cached result
can have skipped the guard; the guard's own content hash is part of the
exec command, and so of every cached result's key.

Not covered: files a test reads before m.Run in-process (TestMain, init),
and tools exec'd by absolute path. The fresh list's header states the rule.

Usage (as `go test -exec`): go-test-input-guard.py [--rev REV] TEST_BINARY ARG...
"""

from __future__ import annotations

import os
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

# The repository whose files count as inputs (GO_TEST_GUARD_REPO for tests).
REPO = Path(os.environ.get("GO_TEST_GUARD_REPO") or Path(__file__).resolve().parent.parent)
SHIMMED_TOOLS = (
    "go", "gofmt", "git", "python", "python3", "node", "npm", "npx",
    "ffmpeg", "ffprobe", "golangci-lint", "staticcheck", "wire", "make",
)
SHIM = """#!/bin/sh
printf '%s\\t%s' "$PWD" "{tool}" >>"$GO_TEST_GUARD_EXEC_LOG"
for arg in "$@"; do printf '\\t%s' "$arg" >>"$GO_TEST_GUARD_EXEC_LOG"; done
printf '\\n' >>"$GO_TEST_GUARD_EXEC_LOG"
PATH="$GO_TEST_GUARD_REAL_PATH"
export PATH
exec {tool} "$@"
"""


def real(path: str | Path) -> Path:
    return Path(os.path.realpath(path))


def inside(path: Path, root: Path) -> bool:
    return path == root or root in path.parents


def module_root(directory: Path) -> Path:
    for candidate in (directory, *directory.parents):
        if (candidate / "go.mod").is_file():
            return candidate
    return directory


def make_shims(directory: Path) -> None:
    for tool in SHIMMED_TOOLS:
        if shutil.which(tool) is None:
            continue
        shim = directory / tool
        shim.write_text(SHIM.format(tool=tool))
        shim.chmod(shim.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def file_violations(testlog: Path, package_dir: Path, module: Path, repo: Path) -> list[str]:
    try:
        lines = testlog.read_text(errors="replace").splitlines()
    except FileNotFoundError:
        return []
    found = []
    pwd = package_dir
    for line in lines:
        op, _, name = line.partition(" ")
        if op == "chdir":
            pwd = Path(name)
            continue
        # A name with a NUL byte fails in the os package without touching a file.
        if op not in {"open", "stat"} or not name or "\0" in name:
            continue
        path = real(pwd / name)
        # filepath.EvalSymlinks, os.MkdirAll and similar stat every ancestor
        # of a path; stat'ing a directory above the module reads no content.
        if op == "stat" and path in module.parents:
            continue
        if inside(path, repo) and not inside(path, module):
            found.append(f"{op} {path.relative_to(repo)}")
    return found


def exec_violations(log: Path, repo: Path) -> list[str]:
    try:
        lines = log.read_text(errors="replace").splitlines()
    except FileNotFoundError:
        return []
    found = []
    for line in lines:
        cwd, *command = line.split("\t")
        in_repo = inside(real(cwd), repo) or any(
            os.path.isabs(arg) and inside(real(arg), repo) for arg in command[1:]
        )
        if in_repo:
            found.append(f"exec in {cwd}: {' '.join(command)}")
    return found


def main() -> int:
    args = sys.argv[1:]
    if args[:1] == ["--rev"]:
        args = args[2:]
    if not args:
        print("usage: go-test-input-guard.py [--rev REV] TEST_BINARY ARG...", file=sys.stderr)
        return 2
    testlog = next((Path(a.split("=", 1)[1]) for a in args if a.startswith("-test.testlogfile=")), None)
    if testlog is None:
        # `go test -exec` does not pass -test.testlogfile, but still reads
        # testlog.txt from the run's work directory (the test binary's) to
        # cache the result; write it there, as `go test` would have asked.
        testlog = Path(args[0]).resolve().parent / "testlog.txt"
        args = [args[0], f"-test.testlogfile={testlog}", *args[1:]]
    package_dir = real(os.getcwd())
    repo = real(REPO)
    with tempfile.TemporaryDirectory(prefix="go-test-guard-") as scratch:
        shims = Path(scratch) / "bin"
        shims.mkdir()
        make_shims(shims)
        exec_log = Path(scratch) / "exec.log"
        env = dict(os.environ)
        env["GO_TEST_GUARD_REAL_PATH"] = env.get("PATH", "")
        env["GO_TEST_GUARD_EXEC_LOG"] = str(exec_log)
        env["PATH"] = f"{shims}{os.pathsep}{env.get('PATH', '')}"
        status = subprocess.call(args, env=env)
        if status != 0:
            return status
        violations = exec_violations(exec_log, repo)
    violations += file_violations(testlog, package_dir, module_root(package_dir), repo)
    if not violations:
        return 0
    print(
        f"go-test-input-guard: {package_dir.relative_to(repo) if inside(package_dir, repo) else package_dir} "
        "passed, but read inputs Go's test cache cannot see, so a cached result could be stale. "
        "List it in scripts/go-test-fresh-packages.txt (it then always runs with -count=1):"
    )
    for violation in sorted(set(violations))[:20]:
        print(f"  {violation}")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
