#!/usr/bin/env python3
"""Reject overlapping ownership of the canonical Linux Go test corpus."""

from __future__ import annotations

import argparse
from collections import defaultdict
from pathlib import Path
import re
import shlex


CANONICAL_CORPORA = {
    "agent-cli/...",
    "go-agent-loop/...",
    "go-llm-gateway/...",
    "go-audio/...",
    "go-device-gateway/...",
    "go-agent-runtime/...",
    "tests/embedding/...",
}

# These are effective package owners, including compatibility targets that
# must not be added back to CI beside the coverage shards. Race-detector and
# operating-system-specific invocations are separate execution variants.
TARGET_CORPORA = {
    "coverage-ci-agent-cli": {"agent-cli/..."},
    "coverage-ci-libraries": CANONICAL_CORPORA - {"agent-cli/..."},
    "coverage": CANONICAL_CORPORA,
    "embed-check": {"tests/embedding/..."},
    "test": CANONICAL_CORPORA - {"tests/embedding/..."},
    "test-audio-device-server-integration": {"agent-cli/..."},
    "test-audio-stability": {
        "agent-cli/...",
        "go-audio/...",
        "go-device-gateway/...",
    },
    "test-hermetic": CANONICAL_CORPORA - {"tests/embedding/..."},
    "test-integration": {"agent-cli/...", "go-agent-loop/..."},
    "test-regressions": {"agent-cli/...", "go-llm-gateway/..."},
}

JOB_RE = re.compile(r"^  ([a-zA-Z0-9_-]+):\s*$")


def make_target(command: str) -> str | None:
    """Return the first target from a shell line containing a Make invocation."""
    try:
        tokens = shlex.split(command, comments=True)
    except ValueError:
        return None
    for index, token in enumerate(tokens):
        if token.rsplit("/", 1)[-1] != "make":
            continue
        index += 1
        while index < len(tokens):
            candidate = tokens[index]
            if candidate in {"-C", "--directory", "-f", "--file", "-I", "--include-dir"}:
                index += 2
                continue
            if candidate.startswith("-") or "=" in candidate:
                index += 1
                continue
            return candidate.rstrip(";|&")
    return None


def workflow_make_targets(workflow: str) -> list[tuple[str, str]]:
    """Return (job, target) pairs for single-line Make invocations."""
    in_jobs = False
    current_job = "<unknown>"
    calls: list[tuple[str, str]] = []
    for line in workflow.splitlines():
        if line == "jobs:":
            in_jobs = True
            continue
        if not in_jobs:
            continue
        if match := JOB_RE.match(line):
            current_job = match.group(1)
            continue
        if target := make_target(line):
            calls.append((current_job, target))
    return calls


def ownership_errors(workflow: str) -> list[str]:
    owners: dict[str, list[str]] = defaultdict(list)
    for job, target in workflow_make_targets(workflow):
        for corpus in TARGET_CORPORA.get(target, set()):
            owners[corpus].append(f"{job} (make {target})")

    errors: list[str] = []
    for corpus in sorted(CANONICAL_CORPORA):
        corpus_owners = owners[corpus]
        if not corpus_owners:
            errors.append(f"{corpus}: no CI owner")
        elif len(corpus_owners) > 1:
            errors.append(f"{corpus}: multiple CI owners: {', '.join(corpus_owners)}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("workflow", type=Path)
    args = parser.parse_args()
    workflow = args.workflow.read_text()
    errors = ownership_errors(workflow)
    if errors:
        raise SystemExit("invalid CI test partition:\n  " + "\n  ".join(errors))

    owners = defaultdict(list)
    for job, target in workflow_make_targets(workflow):
        for corpus in TARGET_CORPORA.get(target, set()):
            owners[corpus].append(job)
    print("CI test partition has one owner per canonical Linux corpus:")
    for corpus in sorted(CANONICAL_CORPORA):
        print(f"  {corpus}: {owners[corpus][0]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
