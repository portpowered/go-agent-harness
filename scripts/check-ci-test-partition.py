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
    "coverage-agent-cli-shard": {"agent-cli/..."},
    "coverage-ci-libraries": CANONICAL_CORPORA - {"agent-cli/..."},
    "coverage": CANONICAL_CORPORA,
    "embed-check": {"tests/embedding/..."},
    "test": CANONICAL_CORPORA - {"tests/embedding/..."},
    "test-audio-device-server-integration": {"agent-cli/..."},
    "test-audio-stress": {"agent-cli/..."},
    "test-audio-stability": {
        "agent-cli/...",
        "go-audio/...",
        "go-device-gateway/...",
    },
    "test-hermetic": CANONICAL_CORPORA - {"tests/embedding/..."},
    "test-integration": {"agent-cli/...", "go-agent-loop/..."},
    "test-regressions": {"agent-cli/...", "go-llm-gateway/..."},
}

# A corpus may instead be owned as disjoint parts, each by exactly one job:
# the agent-cli coverage target runs either every package except
# test/integration (AGENT_CLI_COVERAGE_SHARD=unit) or test/integration
# (AGENT_CLI_COVERAGE_SHARD=integration). Any other shard value (all, a
# matrix expression, integration-K) counts as the whole corpus.
CORPUS_PARTS = {
    "agent-cli/...": ("agent-cli unit packages", "agent-cli/test/integration"),
}
TARGET_PARTS = {
    target: ("AGENT_CLI_COVERAGE_SHARD", {"unit": "agent-cli unit packages", "integration": "agent-cli/test/integration"})
    for target in ("coverage-ci-agent-cli", "coverage-agent-cli-shard")
}

JOB_RE = re.compile(r"^  ([a-zA-Z0-9_-]+):\s*$")


def make_invocation(command: str) -> tuple[str, dict[str, str]] | None:
    """Return the first target and the VAR=value arguments of a Make invocation."""
    try:
        tokens = shlex.split(command, comments=True)
    except ValueError:
        return None
    for index, token in enumerate(tokens):
        if token.rsplit("/", 1)[-1] != "make":
            continue
        index += 1
        target = None
        variables: dict[str, str] = {}
        while index < len(tokens):
            candidate = tokens[index]
            if candidate in {"-C", "--directory", "-f", "--file", "-I", "--include-dir"}:
                index += 2
                continue
            if "=" in candidate and not candidate.startswith("-"):
                name, value = candidate.split("=", 1)
                variables[name] = value.rstrip(";|&")
            elif not candidate.startswith("-") and target is None:
                target = candidate.rstrip(";|&")
            index += 1
        if target is not None:
            return target, variables
    return None


def make_target(command: str) -> str | None:
    """Return the first target from a shell line containing a Make invocation."""
    invocation = make_invocation(command)
    return invocation[0] if invocation else None


def workflow_make_invocations(workflow: str) -> list[tuple[str, str, dict[str, str]]]:
    """Return (job, target, variables) for single-line Make invocations."""
    in_jobs = False
    current_job = "<unknown>"
    calls: list[tuple[str, str, dict[str, str]]] = []
    for line in workflow.splitlines():
        if line == "jobs:":
            in_jobs = True
            continue
        if not in_jobs:
            continue
        if match := JOB_RE.match(line):
            current_job = match.group(1)
            continue
        if invocation := make_invocation(line):
            calls.append((current_job, *invocation))
    return calls


def workflow_make_targets(workflow: str) -> list[tuple[str, str]]:
    """Return (job, target) pairs for single-line Make invocations."""
    return [(job, target) for job, target, _ in workflow_make_invocations(workflow)]


def invocation_corpora(target: str, variables: dict[str, str]) -> set[str]:
    """Return the corpora (or corpus parts) one Make invocation owns."""
    if target in TARGET_PARTS:
        variable, parts = TARGET_PARTS[target]
        if (part := parts.get(variables.get(variable, ""))) is not None:
            return {part}
    return TARGET_CORPORA.get(target, set())


def ownership_errors(workflow: str) -> list[str]:
    owners: dict[str, list[str]] = defaultdict(list)
    for job, target, variables in workflow_make_invocations(workflow):
        for corpus in invocation_corpora(target, variables):
            owners[corpus].append(f"{job} (make {target})")

    errors: list[str] = []
    for corpus in sorted(CANONICAL_CORPORA):
        corpus_owners = owners[corpus]
        parts = CORPUS_PARTS.get(corpus, ())
        part_owners = [owner for part in parts for owner in owners[part]]
        if corpus_owners and part_owners:
            errors.append(f"{corpus}: multiple CI owners: {', '.join(corpus_owners + part_owners)}")
        elif part_owners:
            for part in parts:
                if not owners[part]:
                    errors.append(f"{corpus}: no CI owner for {part}")
                elif len(owners[part]) > 1:
                    errors.append(f"{corpus}: multiple CI owners for {part}: {', '.join(owners[part])}")
        elif not corpus_owners:
            errors.append(f"{corpus}: no CI owner")
        elif len(corpus_owners) > 1:
            errors.append(f"{corpus}: multiple CI owners: {', '.join(corpus_owners)}")
    return errors


def corpus_owner_jobs(workflow: str) -> dict[str, list[str]]:
    """Return the owning jobs of each canonical corpus, part by part."""
    owners: dict[str, list[str]] = defaultdict(list)
    for job, target, variables in workflow_make_invocations(workflow):
        for corpus in invocation_corpora(target, variables):
            owners[corpus].append(job)
    result: dict[str, list[str]] = {}
    for corpus in sorted(CANONICAL_CORPORA):
        if owners[corpus]:
            result[corpus] = owners[corpus]
        else:
            result[corpus] = [f"{owner} ({part})" for part in CORPUS_PARTS.get(corpus, ()) for owner in owners[part]]
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("workflow", type=Path)
    args = parser.parse_args()
    workflow = args.workflow.read_text()
    errors = ownership_errors(workflow)
    if errors:
        raise SystemExit("invalid CI test partition:\n  " + "\n  ".join(errors))

    print("CI test partition has one owner per canonical Linux corpus:")
    for corpus, jobs in corpus_owner_jobs(workflow).items():
        print(f"  {corpus}: {', '.join(jobs)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
