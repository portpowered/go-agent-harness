#!/usr/bin/env python3
"""Production mains must not link test-support packages.

Lists every main package of the workspace modules. A main whose own import
path is test support (under a test/ directory, or a test tool such as
cmd/testtiming) is not a production binary. For each production main, in each
build configuration of CONFIGS (GOOS linux, darwin and windows without cgo,
the native GOOS with cgo, and the CI tag sets wireinject, nomicrophone and
e2e), `go list -deps` gives the linked packages; a repository package whose
import path is test support fails the check, and the report names the import
chain that pulls it in. A main or dependency that does not resolve fails too.

A package is test support when one of its path elements (below the module
directory) is test, tests, testdata, testing, testkit, testutil, fixture,
fixtures or harness; starts with "test", "mock", "fake" or "stub"; or ends
with "test", "stub", "stubs", "fixture", "fixtures" or "harness".
docs/architecture/test-support-packages.md documents the rule.

ALLOWED lists the public library packages that look like test support but
that production code legitimately links; each entry says why.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GO = os.environ.get("GO") or "go"
MODULE_ROOT = "github.com/portpowered/go-agent-harness/"
WORKSPACE_MODULES = (
    "agent-cli",
    "go-agent-runtime",
    "go-agent-loop",
    "go-llm-gateway",
    "go-audio",
    "go-device-gateway",
)
GOOS_LIST = ("linux", "darwin", "windows")
# CI build tag sets checked on linux without cgo.
TAG_SETS = ("wireinject", "nomicrophone", "e2e")
ALLOWED = {
    # The public session-capture fixture contract (go-llm-gateway/pkg/testing,
    # documented for embedders). The production replay and recording services
    # read and write provider captures through it.
    MODULE_ROOT + "go-llm-gateway/pkg/testing": "public session-capture fixture contract used by replay and recording",
}
TEST_ELEMENTS = {"test", "tests", "testdata", "testing", "testkit", "testutil", "fixture", "fixtures", "harness"}
TEST_ELEMENT = re.compile(r"^(test.*|mock.*|fake.*|stub.*|.+(test|stub|stubs|fixture|fixtures|harness))$")


def is_test_support(import_path: str) -> bool:
    """Whether a repository import path names a test-support package."""
    if not import_path.startswith(MODULE_ROOT):
        return False
    elements = import_path[len(MODULE_ROOT):].split("/")[1:]
    return any(element in TEST_ELEMENTS or TEST_ELEMENT.match(element) for element in elements)


def configs() -> list[tuple[str, dict[str, str], str]]:
    """Return (name, environment, tags) for every build configuration checked."""
    out = []
    for goos in GOOS_LIST:
        out.append((goos, cross_env(goos), ""))
    native = subprocess.run([GO, "env", "GOOS", "GOARCH"], cwd=REPO, capture_output=True, text=True).stdout.split()
    if len(native) == 2:
        out.append((f"{native[0]}-cgo", {"GOOS": native[0], "GOARCH": native[1], "CGO_ENABLED": "1"}, ""))
    for tags in TAG_SETS:
        out.append((f"linux-{tags}", cross_env("linux"), tags))
    return out


def cross_env(goos: str) -> dict[str, str]:
    return {"GOOS": goos, "GOARCH": "arm64" if goos == "darwin" else "amd64", "CGO_ENABLED": "0"}


def go_list(args: list[str], env: dict[str, str] | None = None, tags: str = "") -> list[dict]:
    full_env = {**os.environ, "GOFLAGS": "", **(env or {})}
    command = [GO, "list", "-e", "-json=ImportPath,Name,Imports,Error,DepsErrors"]
    if tags:
        command += ["-tags", tags]
    proc = subprocess.run([*command, *args], cwd=REPO, env=full_env, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        raise SystemExit(f"prod-deps-check: go list {' '.join(args)} failed")
    decoder = json.JSONDecoder()
    packages, text, index = [], proc.stdout, 0
    while index < len(text):
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text):
            break
        value, index = decoder.raw_decode(text, index)
        packages.append(value)
    return packages


def resolution_errors(packages: list[dict]) -> list[str]:
    """Return one message per package that go list could not resolve."""
    errors = []
    for package in packages:
        for error in [package.get("Error"), *(package.get("DepsErrors") or [])]:
            if error:
                errors.append(f"{package.get('ImportPath')}: {error.get('Err', error)}")
    return sorted(set(errors))


def production_mains() -> list[str]:
    packages = go_list([f"./{module}/..." for module in WORKSPACE_MODULES])
    return sorted(p["ImportPath"] for p in packages if p.get("Name") == "main" and not is_test_support(p["ImportPath"]))


def chain(graph: dict[str, list[str]], start: str, target: str) -> list[str]:
    """Return the shortest import chain from start to target."""
    previous = {start: ""}
    queue = [start]
    while queue:
        current = queue.pop(0)
        if current == target:
            break
        for imported in graph.get(current, []):
            if imported not in previous:
                previous[imported] = current
                queue.append(imported)
    path = [target]
    while previous.get(path[-1]):
        path.append(previous[path[-1]])
    return list(reversed(path))


def violations(main: str, config: tuple[str, dict[str, str], str]) -> list[str]:
    name, env, tags = config
    packages = go_list(["-deps", main], env, tags)
    problems = [f"{name}: {main} does not resolve: {error}" for error in resolution_errors(packages)]
    graph = {p["ImportPath"]: p.get("Imports") or [] for p in packages}
    for package in sorted(graph):
        if is_test_support(package) and package not in ALLOWED:
            route = " -> ".join(chain(graph, main, package))
            problems.append(f"{name}: {main} links test-support package {package}: {route}")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.parse_args(argv)
    started = time.monotonic()
    mains = production_mains()
    checked = configs()
    jobs = [(main, config) for main in mains for config in checked]
    with concurrent.futures.ThreadPoolExecutor(max_workers=min(8, os.cpu_count() or 1)) as pool:
        results = list(pool.map(lambda job: violations(*job), jobs))
    problems = [problem for result in results for problem in result]
    elapsed = time.monotonic() - started
    for problem in problems:
        print(f"prod-deps-check: {problem}", file=sys.stderr)
    if problems:
        print(f"prod-deps-check: {len(problems)} violation(s); move test support into _test.go files or a "
              "package only _test.go files import (docs/architecture/test-support-packages.md)", file=sys.stderr)
        return 1
    print(f"prod-deps-check: {len(mains)} production mains x {len(checked)} build configurations link no test-support package ({elapsed:.1f}s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
