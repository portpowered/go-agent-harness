#!/usr/bin/env python3
"""Bounded C101 acceptance checks and causal mutation controls.

The script deliberately exercises only the provider-session slice and its
independent consumer. It never reads credentials or opens a provider network
connection.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


TASK_DIR = Path(__file__).resolve().parent
ROOT = TASK_DIR.parents[4]
EXTERNAL_CONSUMER = TASK_DIR / "external-consumer"
REPORT = TASK_DIR / "verification-report.json"
BRANCH = "codex/audio-runtime-c101-retire-cli-provider-session-runtime"
BASELINE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MANIFEST_BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"

OPENAI = Path("agent-cli/internal/services/internal/agentruntime/session_runtime_openai.go")
GROK = Path("agent-cli/internal/services/internal/agentruntime/session_runtime_grok.go")
CAUSAL_TEST = "^TestProviderSessionRuntimeService_ReplayPreservesCausalCredentialHandshakeAndBareAudio$"

MUTATIONS = (
    (
        "live-credential",
        Path("go-agent-runtime/services/providersession/internal/service/replay.go"),
        'APIKey: "replay"',
        'APIKey: ""',
    ),
    (
        "rebuilt-initial-session-update",
        Path("go-agent-runtime/services/providersession/internal/service/replay.go"),
        "InitialSessionUpdate: append([]byte(nil), configuration.payload...)",
        "InitialSessionUpdate: nil",
    ),
    (
        "dropped-bare-scheduled-audio",
        Path("go-agent-runtime/services/providersession/internal/service/replay.go"),
        "plan.AudioInputs = prompt.bareAudio",
        "plan.AudioInputs = nil",
    ),
)


def scrub(value: str) -> str:
    value = re.sub(r"(?i)(api[_-]?key|authorization|token|secret|password)=?[^\s,;]+", r"\1=[REDACTED]", value)
    return value[-1200:]


def run(command: list[str], cwd: Path, timeout: int, extra_env: dict[str, str] | None = None) -> dict:
    env = os.environ.copy()
    for key in list(env):
        if re.search(r"(?i)(api[_-]?key|authorization|token|secret|password)", key):
            env.pop(key, None)
    if extra_env:
        env.update(extra_env)
    try:
        completed = subprocess.run(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
        return {
            "command": command,
            "cwd": str(cwd),
            "exit_code": completed.returncode,
            "stdout_tail": scrub(completed.stdout),
            "stderr_tail": scrub(completed.stderr),
            "timed_out": False,
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "command": command,
            "cwd": str(cwd),
            "exit_code": None,
            "stdout_tail": scrub((exc.stdout or "") if isinstance(exc.stdout, str) else ""),
            "stderr_tail": scrub((exc.stderr or "") if isinstance(exc.stderr, str) else ""),
            "timed_out": True,
        }


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def line_count(path: Path) -> int:
    return len(path.read_text(encoding="utf-8").splitlines())


def git(*args: str, cwd: Path = ROOT) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args], cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False
    )


def focused_positive() -> list[dict]:
    results = [
        run(
            [
                "go",
                "test",
                "./go-agent-runtime/services/providersession/...",
                "-count=1",
                "-timeout=240s",
            ],
            ROOT,
            300,
        ),
        run(["go", "test", "./...", "-count=1", "-timeout=240s"], EXTERNAL_CONSUMER, 300, {"GOWORK": "off"}),
    ]
    results[1]["env"] = {"GOWORK": "off"}
    return results


def causal_mutations() -> list[dict]:
    results: list[dict] = []
    with tempfile.TemporaryDirectory(prefix="c101-causal-") as temp:
        worktree = Path(temp) / "repo"
        added = git("worktree", "add", "--detach", str(worktree), "HEAD")
        if added.returncode != 0:
            return [{"name": "worktree", "expected": "created", "result": scrub(added.stderr), "passed": False}]
        try:
            for name, relative, old, new in MUTATIONS:
                target = worktree / relative
                source = target.read_text(encoding="utf-8")
                occurrences = source.count(old)
                if occurrences == 0:
                    results.append({"name": name, "expected": "mutation target present", "passed": False})
                    continue
                target.write_text(source.replace(old, new), encoding="utf-8")
                result = run(
                    [
                        "go",
                        "test",
                        "./agent-cli/internal/services/internal/agentruntime",
                        "-run",
                        CAUSAL_TEST,
                        "-count=1",
                        "-timeout=120s",
                    ],
                    worktree,
                    180,
                )
                result.update({"name": name, "expected": "non-zero causal oracle", "passed": result["exit_code"] != 0})
                results.append(result)
                target.write_text(source, encoding="utf-8")
        finally:
            git("worktree", "remove", "--force", str(worktree))
    return results


def retirement_census() -> dict:
    branch_result = git("branch", "--show-current")
    head_result = git("rev-parse", "HEAD")
    main_result = git("rev-parse", "origin/main")
    changed_result = git("diff", "--name-only", f"{BASELINE}...HEAD")
    changed = [line for line in changed_result.stdout.splitlines() if line]
    owned_prefixes = (
        "agent-cli/internal/services/internal/agentruntime/session_runtime_openai.go",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_grok.go",
        "agent-cli/internal/services/internal/agentruntime/session_provider_runtime_service_test.go",
        "go-agent-runtime/services/providersession/",
        "coverage-manifest/go-agent-runtime/services/providersession/",
        "docs/temp/projects/audio-runtime/audio-runtime-c101-retire-cli-provider-session-runtime/",
    )
    preserved_shared = ("docs/architecture/architecture-policy.json",)
    unexpected = [
        path for path in changed if not path.startswith(owned_prefixes) and path not in preserved_shared
    ]
    callers_result = git(
        "grep",
        "-n",
        "-E",
        "plan(OpenAI|Grok)(Record|Replay)Runtime",
        "--",
        "agent-cli",
    )
    callers = callers_result.stdout.splitlines() if callers_result.returncode == 0 else []
    excluded = {}
    for relative in (
        "agent-cli/internal/services/internal/agentruntime/session_replay.go",
        "agent-cli/internal/services/internal/agentruntime/session_options.go",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_live.go",
    ):
        path = ROOT / relative
        excluded[relative] = sha256(path) if path.is_file() else None

    source = {}
    for relative, before, expected_after in (
        (OPENAI, "499e9c92856e94192d8e462619deeb4d15246190d2b14effa09502c21ddb8198", 100),
        (GROK, "864fa8b8a3f889d883733a4c1b202e0630d0155ce34f03f32beb712085f788d5", 30),
    ):
        path = ROOT / relative
        source[str(relative)] = {
            "before_sha256": before,
            "after_sha256": sha256(path),
            "after_lines": line_count(path),
            "expected_after_lines": expected_after,
        }

    manifest = ROOT / "factory/projects/audio-runtime/manifest.json"
    manifest_json = json.loads(manifest.read_text(encoding="utf-8"))
    ancestry = {
        "baseline_main": git("merge-base", "--is-ancestor", BASELINE, "HEAD").returncode == 0,
        "startup": git("merge-base", "--is-ancestor", STARTUP, "HEAD").returncode == 0,
    }
    return {
        "task": "audio-runtime-c101-retire-cli-provider-session-runtime",
        "project": "audio-runtime",
        "branch": branch_result.stdout.strip(),
        "expected_branch": BRANCH,
        "head": head_result.stdout.strip(),
        "origin_main": main_result.stdout.strip(),
        "baseline_revision": BASELINE,
        "startup_revision": STARTUP,
        "manifest_baseline_revision": manifest_json.get("baselineRevision"),
        "expected_manifest_baseline_revision": MANIFEST_BASELINE,
        "ancestry": ancestry,
        "changed_paths": changed,
        "unexpected_changed_paths": unexpected,
        "source": source,
        "legacy_lines_before": 383,
        "legacy_lines_after": sum(item["after_lines"] for item in source.values()),
        "legacy_lines_reduced": 383 - sum(item["after_lines"] for item in source.values()),
        "legacy_callers": callers,
        "excluded_path_sha256": excluded,
    }


def write_report(payload: dict) -> None:
    REPORT.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(payload, indent=2, sort_keys=True))


def positive_and_mutations() -> int:
    positive = focused_positive()
    mutations = causal_mutations()
    passed = all(result["exit_code"] == 0 for result in positive) and all(result.get("passed") for result in mutations)
    write_report({"mode": "positive-and-three-mutations", "positive": positive, "mutations": mutations, "passed": passed})
    return 0 if passed else 1


def retirement_and_owned_paths() -> int:
    census = retirement_census()
    census["passed"] = (
        census["branch"] == BRANCH
        and census["manifest_baseline_revision"] == MANIFEST_BASELINE
        and census["ancestry"]["baseline_main"]
        and census["ancestry"]["startup"]
        and not census["unexpected_changed_paths"]
        and census["legacy_lines_after"] <= 133
        and census["legacy_lines_reduced"] >= 250
        and all(item["after_lines"] == item["expected_after_lines"] for item in census["source"].values())
    )
    write_report({"mode": "retirement-and-owned-paths", "census": census, "passed": census["passed"]})
    return 0 if census["passed"] else 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("positive-and-three-mutations", "retirement-and-owned-paths"))
    args = parser.parse_args()
    if args.mode == "positive-and-three-mutations":
        return positive_and_mutations()
    return retirement_and_owned_paths()


if __name__ == "__main__":
    raise SystemExit(main())
