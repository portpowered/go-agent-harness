#!/usr/bin/env python3
"""Small subprocess fixture used only by the profiling controls."""

from __future__ import annotations

import argparse
import json
import sys
import time


def event(
    action: str,
    package: str,
    *,
    test: str = "",
    elapsed: float | int | None = None,
    output: str | None = None,
) -> dict[str, object]:
    value: dict[str, object] = {"Action": action, "Package": package}
    if test:
        value["Test"] = test
    if elapsed is not None:
        value["Elapsed"] = elapsed
    if output is not None:
        value["Output"] = output
    return value


def write_events(values: list[dict[str, object]], *, delay: float) -> None:
    for value in values:
        sys.stdout.write(json.dumps(value, sort_keys=True) + "\n")
        sys.stdout.flush()
        if delay:
            time.sleep(delay)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--scenario",
        required=True,
        choices=(
            "pass",
            "fail",
            "nonzero-pass",
            "truncated",
            "malformed",
            "empty",
            "missing",
            "cached",
            "no-test",
            "overlap",
            "cross-package",
            "repeated-package",
            "aggregate-overflow",
            "oversized-duration",
            "oversized-integer",
        ),
    )
    parser.add_argument("--delay", type=float, default=0.0)
    args = parser.parse_args()

    if args.scenario == "truncated":
        sys.stdout.write('{"Action":"start","Package":"example/truncated"')
        sys.stdout.flush()
        return 0
    if args.scenario == "malformed":
        sys.stdout.write("this is not json\n")
        sys.stdout.flush()
        return 0
    if args.scenario == "empty":
        return 0

    if args.scenario == "pass" or args.scenario == "nonzero-pass":
        write_events(
            [
                event("start", "example/pass"),
                event("run", "example/pass", test="TestPass"),
                event("pass", "example/pass", test="TestPass", elapsed=0.01),
                event("pass", "example/pass", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 7 if args.scenario == "nonzero-pass" else 0

    if args.scenario == "fail":
        write_events(
            [
                event("start", "example/fail"),
                event("run", "example/fail", test="TestFailure"),
                event("fail", "example/fail", test="TestFailure", elapsed=0.01),
                event("fail", "example/fail", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 1

    if args.scenario == "missing":
        write_events(
            [
                event("start", "example/observed"),
                event("pass", "example/observed", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "cached":
        write_events(
            [
                event("start", "example/cached"),
                event("output", "example/cached", output="ok (cached)\n"),
                event("pass", "example/cached", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "no-test":
        write_events(
            [
                event("start", "example/no-test"),
                event("output", "example/no-test", output="? [no test files]\n"),
                event("skip", "example/no-test", elapsed=0.0),
                event("start", "example/pass"),
                event("pass", "example/pass", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "overlap":
        write_events(
            [
                event("start", "example/overlap"),
                event("run", "example/overlap", test="TestOverlap/a"),
                event("run", "example/overlap", test="TestOverlap/b"),
                event("pass", "example/overlap", test="TestOverlap/b", elapsed=0.01),
                event("pass", "example/overlap", test="TestOverlap/a", elapsed=0.01),
                event("pass", "example/overlap", elapsed=0.03),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "cross-package":
        write_events(
            [
                event("start", "example/package-a"),
                event("run", "example/package-a", test="TestPackageA"),
                event("start", "example/package-b"),
                event("run", "example/package-b", test="TestPackageB"),
                event("pass", "example/package-b", test="TestPackageB", elapsed=0.01),
                event("pass", "example/package-a", test="TestPackageA", elapsed=0.01),
                event("pass", "example/package-a", elapsed=0.02),
                event("pass", "example/package-b", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "repeated-package":
        write_events(
            [
                event("start", "example/repeated"),
                event("pass", "example/repeated", elapsed=0.01),
                event("start", "example/repeated"),
                event("pass", "example/repeated", elapsed=0.02),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "aggregate-overflow":
        write_events(
            [
                event("start", "example/overflow-a"),
                event("pass", "example/overflow-a", elapsed=9_000_000_000),
                event("start", "example/overflow-b"),
                event("pass", "example/overflow-b", elapsed=9_000_000_000),
                event("start", "example/overflow-c"),
                event("pass", "example/overflow-c", elapsed=9_000_000_000),
                event("start", "example/overflow-d"),
                event("pass", "example/overflow-d", elapsed=9_000_000_000),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "oversized-duration":
        write_events(
            [
                event("start", "example/oversized"),
                event("pass", "example/oversized", elapsed=1_000_000_000_000),
            ],
            delay=args.delay,
        )
        return 0

    if args.scenario == "oversized-integer":
        write_events(
            [
                event("start", "example/oversized"),
                event("pass", "example/oversized", elapsed=10**400),
            ],
            delay=args.delay,
        )
        return 0

    raise AssertionError(f"unhandled scenario {args.scenario}")


if __name__ == "__main__":
    raise SystemExit(main())
