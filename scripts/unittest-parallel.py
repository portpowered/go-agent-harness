#!/usr/bin/env python3
"""Run unittest test names across worker processes with unittest's output.

    python3 -B scripts/unittest-parallel.py [-v] [-j N] [--forbid-bytecode ROOT] NAME...

NAME is anything `python -m unittest NAME` accepts (module, class or test
method). Every selected test runs exactly once; only the wall time changes.
Each test is an independent unit sent to a pool of worker processes, except
that tests sharing a class fixture (setUpClass/tearDownClass) or a module
fixture (setUpModule/tearDownModule) stay in one unit so the fixture runs
once around them, as it does under unittest. Selection errors (a missing
module, a failed import) are reported as unittest reports them.

Per-test lines and failure details are printed in the original test order
once the run finishes, followed by unittest's "Ran N tests" summary and
OK/FAILED line. The exit status matches `python -m unittest`: 0 on success,
1 on any failure or error, 5 when no tests ran. A worker process that dies
outright (os._exit, a signal) fails the run with a BrokenProcessPool
traceback and exit status 1 rather than its own status.

--forbid-bytecode ROOT snapshots every __pycache__ directory and .pyc file
under ROOT before the run and fails the run (exit 1) if any appeared or
changed afterwards, including bytecode written by subprocesses the tests
spawn. Nested git checkouts and .git directories are not scanned.
"""

import argparse
import concurrent.futures
import io
import os
import sys
import time
import unittest

NO_TESTS_STATUS = 5


def _flatten(suite):
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            yield from _flatten(test)
        else:
            yield test


def _overrides(cls, name):
    return getattr(cls, name).__func__ is not getattr(unittest.TestCase, name).__func__


def _unit_key(test):
    """Tests with the same key must run together, in order, in one process."""
    cls = type(test)
    module = sys.modules.get(cls.__module__)
    if module is not None and (hasattr(module, "setUpModule") or hasattr(module, "tearDownModule")):
        return ("module", cls.__module__)
    if _overrides(cls, "setUpClass") or _overrides(cls, "tearDownClass"):
        return ("class", cls.__module__, cls.__qualname__)
    return ("test", test.id())


def _loadable(test):
    """Whether a worker can re-load this test from its id.

    Selection errors are synthetic tests in unittest.loader; they fail
    immediately and are run in the parent process.
    """
    return isinstance(test, unittest.TestCase) and type(test).__module__ != "unittest.loader"


def plan(names):
    """Return the ordered units: lists of test ids, or a local TestSuite."""
    suite = unittest.defaultTestLoader.loadTestsFromNames(names)
    units = []
    index_by_key = {}
    for test in _flatten(suite):
        if not _loadable(test):
            units.append(unittest.TestSuite([test]))
            continue
        key = _unit_key(test)
        if key in index_by_key:
            units[index_by_key[key]].append(test.id())
        else:
            index_by_key[key] = len(units)
            units.append([test.id()])
    return units


def _run_suite(suite, verbosity):
    stream = io.StringIO()
    result = unittest.TextTestResult(unittest.runner._WritelnDecorator(stream), True, verbosity)
    suite(result)
    details = io.StringIO()
    result.stream = unittest.runner._WritelnDecorator(details)
    result.printErrorList("ERROR", result.errors)
    result.printErrorList("FAIL", result.failures)
    for test in result.unexpectedSuccesses:
        result.stream.writeln(result.separator1)
        result.stream.writeln(f"UNEXPECTED SUCCESS: {result.getDescription(test)}")
    return {
        "output": stream.getvalue(),
        "details": details.getvalue(),
        "run": result.testsRun,
        "failures": len(result.failures),
        "errors": len(result.errors),
        "skipped": len(result.skipped),
        "expected_failures": len(result.expectedFailures),
        "unexpected_successes": len(result.unexpectedSuccesses),
    }


def run_unit(ids, verbosity):
    """Worker entry point: load the unit's tests by id and run them."""
    return _run_suite(unittest.defaultTestLoader.loadTestsFromNames(ids), verbosity)


def _summary(totals):
    details = [
        f"{label}={totals[key]}"
        for key, label in (
            ("failures", "failures"),
            ("errors", "errors"),
            ("skipped", "skipped"),
            ("expected_failures", "expected failures"),
            ("unexpected_successes", "unexpected successes"),
        )
        if totals[key]
    ]
    failed = totals["failures"] or totals["errors"] or totals["unexpected_successes"]
    status = "FAILED" if failed else "OK"
    if totals["run"] == 0 and not failed:
        status = "NO TESTS RAN"
    return status + (f" ({', '.join(details)})" if details else ""), bool(failed)


def bytecode_snapshot(root):
    """Map each __pycache__ directory and .pyc file under root to its mtime."""
    snapshot = {}
    for directory, subdirectories, files in os.walk(root):
        # Skip git metadata and other checkouts (worktrees, fixtures) nested
        # in the tree: they are not this repository's sources.
        subdirectories[:] = [
            name for name in subdirectories
            if name != ".git" and not os.path.exists(os.path.join(directory, name, ".git"))
        ]
        if os.path.basename(directory) == "__pycache__":
            snapshot[os.path.relpath(directory, root)] = None
        for name in files:
            if name.endswith(".pyc"):
                path = os.path.join(directory, name)
                try:
                    snapshot[os.path.relpath(path, root)] = os.stat(path).st_mtime_ns
                except FileNotFoundError:
                    continue
    return snapshot


def new_bytecode(before, after):
    return sorted(path for path, stamp in after.items() if path not in before or before[path] != stamp)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("-v", "--verbose", action="store_const", const=2, default=1, dest="verbosity")
    parser.add_argument("-j", "--jobs", type=int, default=os.cpu_count() or 1)
    parser.add_argument("--forbid-bytecode", metavar="ROOT")
    parser.add_argument("names", nargs="*")
    args = parser.parse_args(argv)
    # Match `python -m unittest`, which imports names relative to the
    # working directory rather than this script's directory.
    script_directory = os.path.dirname(os.path.abspath(__file__))
    sys.path[:] = [entry for entry in sys.path if os.path.abspath(entry or ".") != script_directory]
    sys.path.insert(0, os.getcwd())

    before = bytecode_snapshot(args.forbid_bytecode) if args.forbid_bytecode else None
    started = time.perf_counter()
    units = plan(args.names)
    results = [None] * len(units)
    remote = [index for index, unit in enumerate(units) if isinstance(unit, list)]
    for index, unit in enumerate(units):
        if not isinstance(unit, list):
            results[index] = _run_suite(unit, args.verbosity)
    if remote:
        workers = max(1, min(args.jobs, len(remote)))
        with concurrent.futures.ProcessPoolExecutor(max_workers=workers) as pool:
            futures = {index: pool.submit(run_unit, units[index], args.verbosity) for index in remote}
            for index, future in futures.items():
                results[index] = future.result()
    elapsed = time.perf_counter() - started

    totals = {key: sum(result[key] for result in results) for key in (
        "run", "failures", "errors", "skipped", "expected_failures", "unexpected_successes")}
    for result in results:
        sys.stderr.write(result["output"])
    if args.verbosity > 1 or any(result["details"] for result in results):
        sys.stderr.write("\n")
    for result in results:
        sys.stderr.write(result["details"])
    status, failed = _summary(totals)
    sys.stderr.write("-" * 70 + "\n")
    sys.stderr.write(f"Ran {totals['run']} test{'s' if totals['run'] != 1 else ''} in {elapsed:.3f}s\n\n{status}\n")
    if before is not None:
        written = new_bytecode(before, bytecode_snapshot(args.forbid_bytecode))
        if written:
            sys.stderr.write(f"FAILED: the tests wrote Python bytecode under {args.forbid_bytecode}:\n")
            sys.stderr.writelines(f"  {path}\n" for path in written)
            return 1
    if failed:
        return 1
    return NO_TESTS_STATUS if totals["run"] == 0 else 0


if __name__ == "__main__":
    sys.exit(main())
