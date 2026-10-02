#!/usr/bin/env python3
"""Production mains must not link test-support packages.

Lists every main package of the workspace modules. A main whose own import
path is test support (under a test/ directory, or a test tool such as
cmd/testtiming) is not a production binary. For each production main, for
GOOS linux, darwin and windows, `go list -deps` gives the linked packages; a
repository package whose import path is test support fails the check, and the
report names the import chain that pulls it in.

A package is test support when one of its path elements is test, tests,
testdata, testing, testkit or testutil, starts with "test", ends with "test"
(clitest, servicetest, webmcptest), or starts with "mock" or "fake".

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
ALLOWED = {
    # The public session-capture fixture contract (go-llm-gateway/pkg/testing,
    # documented for embedders). The production replay and recording services
    # read and write provider captures through it.
    MODULE_ROOT + "go-llm-gateway/pkg/testing": "public session-capture fixture contract used by replay and recording",
}
TEST_ELEMENTS = {"test", "tests", "testdata", "testing", "testkit", "testutil"}
TEST_ELEMENT = re.compile(r"^(test.*|.+test|mock.*|fake.*)$")


def is_test_support(import_path: str) -> bool:
    """Whether a repository import path names a test-support package."""
    if not import_path.startswith(MODULE_ROOT):
        return False
    elements = import_path[len(MODULE_ROOT):].split("/")[1:]
    return any(element in TEST_ELEMENTS or TEST_ELEMENT.match(element) for element in elements)


def go_list(args: list[str], goos: str | None = None) -> list[dict]:
    env = {**os.environ, "GOFLAGS": ""}
    if goos:
        env.update({"GOOS": goos, "GOARCH": "arm64" if goos == "darwin" else "amd64", "CGO_ENABLED": "0"})
    proc = subprocess.run([GO, "list", "-e", "-json=ImportPath,Name,Imports,Error,DepsErrors", *args],
                          cwd=REPO, env=env, capture_output=True, text=True)
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


def violations(main: str, goos: str) -> list[str]:
    packages = go_list(["-deps", main], goos)
    graph = {p["ImportPath"]: p.get("Imports") or [] for p in packages}
    problems = []
    for package in sorted(graph):
        if is_test_support(package) and package not in ALLOWED:
            route = " -> ".join(chain(graph, main, package))
            problems.append(f"{goos}: {main} links test-support package {package}: {route}")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.parse_args(argv)
    started = time.monotonic()
    mains = production_mains()
    jobs = [(main, goos) for main in mains for goos in GOOS_LIST]
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
    print(f"prod-deps-check: {len(mains)} production mains x {len(GOOS_LIST)} GOOS link no test-support package ({elapsed:.1f}s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
