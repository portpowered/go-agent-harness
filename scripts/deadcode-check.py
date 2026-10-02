#!/usr/bin/env python3
"""Dead-code guard: no Go function may become unreachable.

Runs the pinned golang.org/x/tools/cmd/deadcode (a tool dependency of
tools/architecturegate, so go.sum pins it) with -test over the build
configurations listed in docs/architecture/deadcode-gate.md: the workspace for
linux, darwin and windows with cgo off, linux with nomicrophone and
wireinject, windows with nomicrophone, the other-OS stub packages as
js/wasm, the host's own cgo build, the tests/embedding consumer,
and the standalone tools/* and tests/localai modules. A function is dead only
when every configuration that compiles its file finds it unreachable from
every main and every test.

The dead functions must match the checked-in allowlist exactly:
- a dead function missing from the allowlist fails (new dead code);
- an allowlisted function that is no longer dead fails (stale entry: delete
  the line, the allowlist only shrinks);
- with --base REV, an allowlist entry absent from the allowlist at the merge
  base with REV fails, so the allowlist cannot grow.

Entries are "<import path> <function>", where <function> is deadcode's name
(Func or Type.Method), so moving a function between files of one package
keeps its entry.

--write rewrites the allowlist to the current dead set (to shrink it after
deleting dead code). --no-test reports the functions reachable only from
tests (an analysis aid, never part of the gate).
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GO = os.environ.get("GO") or "go"
DEFAULT_ALLOWLIST = REPO / "docs/architecture/deadcode-allowlist.txt"
TOOL_MODULE = "tools/architecturegate"
TOOL_PACKAGE = "golang.org/x/tools/cmd/deadcode"
WORKSPACE_MODULES = (
    "agent-cli",
    "go-agent-runtime",
    "go-agent-loop",
    "go-llm-gateway",
    "go-audio",
    "go-device-gateway",
)
# Modules outside go.work, each analyzed alone (GOWORK=off) for its own code.
STANDALONE_MODULES = (
    "tools/analyzergate",
    "tools/architecturegate",
    "tools/coveragegate",
    "tools/racegate",
    "tools/timingate",
    "tests/localai",
)
# Matches every module of this repository, so the embedding consumer's run
# reports (and so proves reachable) library functions.
REPO_FILTER = r"^github\.com/portpowered/go-agent-harness/"
TAGS = "e2e,stress"
GOOS_LIST = ("linux", "darwin", "windows")
ALLOWLIST_HEADER = """\
# Functions golang.org/x/tools/cmd/deadcode -test finds unreachable from every
# main and every test, in every build configuration (docs/architecture/deadcode-gate.md).
# One "<import path> <function>" per line, sorted. This list may only shrink:
# delete dead code instead of adding it here. `make deadcode-check` fails on
# a dead function missing from this list and on an entry that is no longer
# dead; `make deadcode-write` rewrites the list after dead code is deleted.
"""


@dataclass(frozen=True)
class Run:
    name: str
    cwd: Path
    patterns: tuple[str, ...]
    env: dict[str, str]
    filter: str | None


def cross_env(goos: str, extra_tags: str = "") -> dict[str, str]:
    env = {"GOOS": goos, "GOARCH": "arm64" if goos == "darwin" else "amd64", "CGO_ENABLED": "0"}
    if extra_tags:
        env["DEADCODE_EXTRA_TAGS"] = extra_tags
    return env


def native_cgo_env() -> dict[str, str] | None:
    """The host's own cgo configuration (darwin or linux microphone backends),
    when the host can build cgo; None otherwise or when DEADCODE_NATIVE_CGO=0."""
    if os.environ.get("DEADCODE_NATIVE_CGO", "1") == "0":
        return None
    probe = subprocess.run([GO, "env", "GOOS", "CGO_ENABLED"], capture_output=True, text=True)
    if probe.returncode != 0:
        return None
    goos, cgo = (probe.stdout.split() + ["", ""])[:2]
    if goos not in ("darwin", "linux") or cgo != "1":
        return None
    return {"CGO_ENABLED": "1", "DEADCODE_HOST_ONLY": goos}


def other_os_packages() -> tuple[str, ...]:
    """Workspace package directories holding other-OS (!linux && !darwin &&
    !windows) files, selected like `make lint-other-os` does."""
    listed = subprocess.run(
        ["git", "grep", "-l", "--all-match", "-e", "^//go:build .*!linux", "-e", "^//go:build .*!darwin",
         "-e", "^//go:build .*!windows", "--", "*.go", ":!:**/testdata/**"],
        cwd=REPO, capture_output=True, text=True,
    ).stdout.split()
    dirs = {"./" + str(Path(path).parent) for path in listed if path.split("/", 1)[0] in WORKSPACE_MODULES}
    return tuple(sorted(dirs))


def runs() -> list[Run]:
    workspace = tuple(f"./{m}/..." for m in WORKSPACE_MODULES)
    out = [Run(f"workspace-{goos}", REPO, workspace, cross_env(goos), REPO_FILTER) for goos in GOOS_LIST]
    # The hermetic coverage pass builds with nomicrophone (and windows has a
    # nomicrophone-only device backend); wireinject selects the Wire injector
    # sources.
    # The two tags select independent files, so one linux run covers both.
    out.append(
        Run("workspace-linux-nomicrophone-wireinject", REPO, workspace, cross_env("linux", "nomicrophone,wireinject"), REPO_FILTER)
    )
    out.append(Run("workspace-windows-nomicrophone", REPO, workspace, cross_env("windows", "nomicrophone"), REPO_FILTER))
    # The other-OS stubs build only for a GOOS the whole workspace does not,
    # so (like `make lint-other-os`) only their packages are analyzed, as
    # js/wasm; their tests are the roots.
    other_os = other_os_packages()
    if other_os:
        env = {"GOOS": "js", "GOARCH": "wasm", "CGO_ENABLED": "0"}
        out.append(Run("other-os-js-wasm", REPO, other_os, env, REPO_FILTER))
    native = native_cgo_env()
    if native is not None:
        out.append(Run(f"workspace-{native['DEADCODE_HOST_ONLY']}-cgo", REPO, workspace, native, REPO_FILTER))
    out.append(
        Run(
            "embedding-linux",
            REPO / "tests/embedding",
            ("./...",),
            {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOWORK": "off"},
            REPO_FILTER,
        )
    )
    for module in STANDALONE_MODULES:
        out.append(
            Run(
                module,
                REPO / module,
                ("./...",),
                {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOWORK": "off"},
                None,
            )
        )
    return out


def build_tool(out_dir: Path) -> Path:
    binary = out_dir / "deadcode"
    subprocess.run(
        [GO, "build", "-o", str(binary), TOOL_PACKAGE],
        cwd=REPO / TOOL_MODULE,
        env={**os.environ, "GOWORK": "off", "GOOS": "", "GOARCH": "", "CGO_ENABLED": "0"},
        check=True,
    )
    return binary


def run_tags(run: Run) -> str:
    extra = run.env.get("DEADCODE_EXTRA_TAGS")
    return f"{TAGS},{extra}" if extra else TAGS


def run_env(run: Run) -> dict[str, str]:
    return {**os.environ, **run.env, "GOFLAGS": ""}


def relative(path: str) -> str:
    try:
        return str(Path(path).resolve().relative_to(REPO))
    except ValueError:
        return path


def loaded_files(run: Run, test: bool) -> set[str]:
    """Return the repository files the run compiles (its source universe)."""
    args = [GO, "list", "-e", "-deps", "-tags", run_tags(run), "-f",
            "{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}\n{{end}}"
            "{{range .TestGoFiles}}{{$d}}/{{.}}\n{{end}}"
            "{{range .XTestGoFiles}}{{$d}}/{{.}}\n{{end}}"]
    if test:
        args.append("-test")
    proc = subprocess.run(args + list(run.patterns), cwd=run.cwd, env=run_env(run),
                          capture_output=True, text=True, check=True)
    files = set()
    for line in proc.stdout.splitlines():
        line = line.strip()
        if line:
            rel = relative(line)
            if not rel.startswith("/"):
                files.add(rel)
    return files


def dead_functions(binary: Path, run: Run, test: bool) -> dict[tuple[str, str], tuple[str, int]]:
    """Return {(file, function): (import path, line)} of the run's dead functions."""
    args = [str(binary), "-json", "-tags", run_tags(run)]
    if test:
        args.append("-test")
    if run.filter is not None:
        args.append(f"-filter={run.filter}")
    proc = subprocess.run(args + list(run.patterns), cwd=run.cwd, env=run_env(run),
                          capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        raise SystemExit(f"deadcode-check: deadcode failed for {run.name}")
    dead = {}
    for package in json.loads(proc.stdout or "null") or []:
        for function in package["Funcs"]:
            if function.get("Generated"):
                continue
            key = (relative(function["Position"]["File"]), function["Name"])
            dead[key] = (package["Path"], function["Position"]["Line"])
    return dead


# A workspace deadcode run type-checks every package from source and peaks at
# about 4 GB of memory, so concurrent runs are bounded by physical memory.
DEADCODE_RUN_BYTES = 4_500_000_000


def deadcode_jobs() -> int:
    configured = os.environ.get("DEADCODE_JOBS", "")
    if configured.isdigit() and int(configured) > 0:
        return int(configured)
    try:
        memory = os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")
    except (ValueError, OSError, AttributeError):
        return 2
    return max(1, min(os.cpu_count() or 1, memory // DEADCODE_RUN_BYTES))


def analyze(binary: Path, test: bool = True) -> tuple[dict[str, str], set[str]]:
    """Return ({"<import path> <function>": "file:line"} dead in every run that
    loads them, the files at least one run compiled)."""
    selected = runs()
    with (
        concurrent.futures.ThreadPoolExecutor(max_workers=len(selected)) as pool,
        concurrent.futures.ThreadPoolExecutor(max_workers=deadcode_jobs()) as heavy,
    ):
        loaded_jobs = {run.name: pool.submit(loaded_files, run, test) for run in selected}
        dead_jobs = {run.name: heavy.submit(dead_functions, binary, run, test) for run in selected}
        loaded = {name: job.result() for name, job in loaded_jobs.items()}
        dead = {name: job.result() for name, job in dead_jobs.items()}
    host_only = frozenset(run.name for run in selected if "DEADCODE_HOST_ONLY" in run.env)
    portable_files = set().union(*(files for name, files in loaded.items() if name not in host_only))
    return combine(loaded, dead, host_only), portable_files | set().union(*loaded.values())


MODULE_ROOT = "github.com/portpowered/go-agent-harness/"


def declaring_files(entry: str) -> list[str]:
    """Return the repository files that declare an allowlist entry's function."""
    package, function = entry.split(" ", 1)
    if not package.startswith(MODULE_ROOT):
        return []
    directory = REPO / package[len(MODULE_ROOT):]
    if "." in function:
        receiver, name = function.split(".", 1)
        pattern = re.compile(r"^func \(\w+ \*?" + re.escape(receiver) + r"(\[[^]]*\])?\) " + re.escape(name) + r"\b", re.M)
    else:
        pattern = re.compile(r"^func " + re.escape(function) + r"\b", re.M)
    return [relative(str(path)) for path in sorted(directory.glob("*.go")) if pattern.search(path.read_text())]


def judgeable(entry: str, compiled: set[str]) -> bool:
    """An entry is judged on this host unless every file declaring it was
    compiled only by a host-specific run that did not run here (for example
    darwin cgo sources on a linux host)."""
    files = declaring_files(entry)
    return not files or any(path in compiled for path in files)


def combine(
    loaded: dict[str, set[str]],
    dead: dict[str, dict[tuple[str, str], tuple[str, int]]],
    host_only: frozenset[str] = frozenset(),
) -> dict[str, str]:
    """A function is dead when every run that compiles its file reports it dead.

    Host-specific runs (named in host_only) judge only files no portable run
    compiles, so the result for every other file is the same on every host.
    """
    candidates: dict[tuple[str, str], tuple[str, int]] = {}
    for per_run in dead.values():
        candidates.update(per_run)
    result = {}
    for key, (path, line) in candidates.items():
        file_name, function = key
        compiling = [name for name, files in loaded.items() if file_name in files]
        portable = [name for name in compiling if name not in host_only]
        judges = portable or compiling
        if all(key in dead[name] for name in judges):
            result[f"{path} {function}"] = f"{file_name}:{line}"
    return result


def read_allowlist(text: str) -> set[str]:
    entries = set()
    for line in text.splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            entries.add(line)
    return entries


def base_allowlist(base: str, allowlist: Path) -> set[str] | None:
    merge_base = subprocess.run(["git", "merge-base", base, "HEAD"], cwd=REPO, capture_output=True, text=True)
    if merge_base.returncode != 0:
        raise SystemExit(f"deadcode-check: no merge base with {base}; fetch full history to check allowlist growth")
    sha = merge_base.stdout.strip()
    rel = allowlist.resolve().relative_to(REPO)
    shown = subprocess.run(["git", "show", f"{sha}:{rel}"], cwd=REPO, capture_output=True, text=True)
    if shown.returncode != 0:
        print(f"deadcode-check: growth base {base} merge base {sha} has no allowlist (new on this branch)")
        return None
    print(f"deadcode-check: growth base {base} merge base {sha}")
    return read_allowlist(shown.stdout)


def compare(dead: set[str], allowed: set[str], base: set[str] | None, judged=lambda entry: True) -> list[str]:
    problems = []
    for entry in sorted(dead - allowed):
        problems.append(f"new dead code (delete it): {entry}")
    for entry in sorted(allowed - dead):
        if judged(entry):
            problems.append(f"stale allowlist entry (no longer dead; delete the line): {entry}")
    if base is not None:
        for entry in sorted(allowed - base):
            problems.append(f"allowlist grew (it may only shrink): {entry}")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--allowlist", type=Path, default=DEFAULT_ALLOWLIST)
    parser.add_argument("--base", default="", help="git revision whose merge base bounds the allowlist")
    parser.add_argument("--write", action="store_true", help="rewrite the allowlist to the current dead set")
    parser.add_argument("--no-test", action="store_true", help="list functions reachable only from tests")
    parser.add_argument("--tool-dir", type=Path, default=None, help="directory for the built deadcode binary")
    args = parser.parse_args(argv)

    tool_dir = args.tool_dir or (REPO / ".cache/deadcode")
    tool_dir.mkdir(parents=True, exist_ok=True)
    binary = build_tool(tool_dir)

    if args.no_test:
        live_in_tests, _ = analyze(binary, test=False)
        dead_in_tests, _ = analyze(binary, test=True)
        for entry in sorted(set(live_in_tests) - set(dead_in_tests)):
            print(f"{live_in_tests[entry]}: {entry}")
        return 0

    positions, compiled = analyze(binary)
    dead = set(positions)
    allowed = read_allowlist(args.allowlist.read_text()) if args.allowlist.exists() else set()
    if args.write:
        # Keep entries this host cannot judge (host-specific cgo sources).
        dead |= {entry for entry in allowed if not judgeable(entry, compiled)}
        args.allowlist.write_text(ALLOWLIST_HEADER + "".join(f"{e}\n" for e in sorted(dead)))
        print(f"deadcode-check: wrote {len(dead)} entries to {relative(str(args.allowlist))}")
        return 0
    base = base_allowlist(args.base, args.allowlist) if args.base else None
    problems = compare(dead, allowed, base, lambda entry: judgeable(entry, compiled))
    for problem in problems:
        entry = problem.rsplit(": ", 1)[-1]
        where = f" ({positions[entry]})" if entry in positions else ""
        print(f"deadcode-check: {problem}{where}", file=sys.stderr)
    if problems:
        return 1
    print(f"deadcode-check: ok ({len(dead)} allowlisted dead functions)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
