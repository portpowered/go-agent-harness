#!/usr/bin/env python3
"""Bounded C74 admission, public-consumer, parity, and size controls."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
CONSUMER = HERE / "external-consumer"
SOURCE = ROOT / "agent-cli/internal/services/internal/agentruntime/session_options.go"
ACCEPTED_MAIN = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
BASELINE_LINES = 930
MAX_CANDIDATE_LINES = 680
MIN_RETIRED_LINES = 250


class VerificationFailure(RuntimeError):
    pass


def run(argv: list[str], *, cwd: pathlib.Path = ROOT, env: dict[str, str] | None = None, timeout: int = 120) -> dict[str, Any]:
    selected_env = os.environ.copy()
    if env:
        selected_env.update(env)
    started = time.monotonic()
    try:
        completed = subprocess.run(argv, cwd=cwd, env=selected_env, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        raise VerificationFailure(f"timeout after {timeout}s: {' '.join(argv)}") from exc
    return {
        "argv": argv,
        "cwd": str(cwd),
        "returncode": completed.returncode,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


def require(result: dict[str, Any], label: str) -> dict[str, Any]:
    if result["returncode"] != 0:
        raise VerificationFailure(
            f"{label} failed with exit {result['returncode']}:\n{result['stdout']}\n{result['stderr']}"
        )
    return result


def inventory(
    baseline_lines: int = BASELINE_LINES,
    max_lines: int = MAX_CANDIDATE_LINES,
    min_retired_lines: int = MIN_RETIRED_LINES,
) -> dict[str, Any]:
    current_lines = len(SOURCE.read_text(encoding="utf-8").splitlines())
    if current_lines > max_lines:
        raise VerificationFailure(f"candidate session_options.go has {current_lines} lines; max is {max_lines}")
    retired = baseline_lines - current_lines
    if retired < min_retired_lines:
        raise VerificationFailure(f"retired {retired} lines from the accepted {baseline_lines}-line baseline; minimum is {min_retired_lines}")
    baseline = require(run(["git", "show", f"{ACCEPTED_MAIN}:{SOURCE.relative_to(ROOT)}"]), "accepted-main source lookup")
    observed_baseline = len(baseline["stdout"].splitlines())
    if observed_baseline != baseline_lines:
        raise VerificationFailure(f"accepted-main source is {observed_baseline} lines; expected frozen baseline {baseline_lines}")

    public_files = [
        ROOT / "go-agent-runtime/services/sessionconfig/contract.go",
        ROOT / "go-agent-runtime/services/sessionconfig/internal/service/service.go",
        ROOT / "go-agent-runtime/services/sessionconfig/wire/providers.go",
        ROOT / "go-agent-runtime/services/sessionconfig/wire/wire_gen.go",
    ]
    forbidden_imports = (
        "agent-cli",
        "providers/openai",
        "providers/grok",
        "net/http",
        "os/exec",
    )
    for path in public_files:
        text = path.read_text(encoding="utf-8")
        if "func init(" in text or "os.Getenv(" in text or "os.LookupEnv(" in text:
            raise VerificationFailure(f"hidden initialization or environment read in {path}")
        imports = [line.strip().strip('"') for line in text.splitlines() if line.strip().startswith('"')]
        for imported in imports:
            if any(forbidden in imported for forbidden in forbidden_imports):
                raise VerificationFailure(f"forbidden public-service import {imported!r} in {path}")
    return {
        "status": "ok",
        "accepted_main": ACCEPTED_MAIN,
        "accepted_baseline_lines": observed_baseline,
        "candidate_lines": current_lines,
        "retired_lines": retired,
        "public_files": [str(path.relative_to(ROOT)) for path in public_files],
    }


def consumer() -> dict[str, Any]:
    test_result = require(
        run(["go", "test", ".", "-count=1"], cwd=CONSUMER, env={"GOWORK": "off"}),
        "GOWORK=off external consumer test",
    )
    valid = require(run(["go", "run", ".", "valid"], cwd=CONSUMER, env={"GOWORK": "off"}), "external valid consumer")
    replay = require(run(["go", "run", ".", "replay"], cwd=CONSUMER, env={"GOWORK": "off"}), "external replay consumer")
    if '"default_transport":"ws"' not in valid["stdout"] or '"explicit_transport":"webrtc"' not in valid["stdout"]:
        raise VerificationFailure(f"external consumer transport oracle missing: {valid['stdout']}")
    if '"status":"replay-ok"' not in replay["stdout"]:
        raise VerificationFailure(f"external consumer replay oracle missing: {replay['stdout']}")

    invalid_model = run(["go", "run", ".", "invalid-model"], cwd=CONSUMER, env={"GOWORK": "off"})
    if invalid_model["returncode"] == 0 or "not realtime-capable" not in invalid_model["stdout"] + invalid_model["stderr"]:
        raise VerificationFailure(f"invalid model negative control did not reject causally: {invalid_model}")
    invalid_transport = run(["go", "run", ".", "invalid-transport"], cwd=CONSUMER, env={"GOWORK": "off"})
    if invalid_transport["returncode"] == 0 or "conflicting session signaling endpoints" not in invalid_transport["stdout"] + invalid_transport["stderr"]:
        raise VerificationFailure(f"invalid transport negative control did not reject causally: {invalid_transport}")
    return {
        "status": "ok",
        "test": test_result,
        "valid": valid,
        "replay": replay,
        "negative_controls": {"invalid_model": invalid_model, "invalid_transport": invalid_transport},
    }


def parity() -> dict[str, Any]:
    service = require(run(["go", "test", "./go-agent-runtime/services/sessionconfig/...", "-count=1"]), "sessionconfig normal tests")
    race = require(run(["go", "test", "-race", "./go-agent-runtime/services/sessionconfig/...", "-count=1"], timeout=180), "sessionconfig race tests")
    cli = require(
        run(
            [
                "go",
                "test",
                "./agent-cli/internal/services/internal/agentruntime",
                "-run",
                "Test(ValidateSessionRunOptions|ResolveOpenAIRealtimeSessionConfig|PlanSessionRuntime|RunSession_InvalidVoiceFailsBeforeReplayConsumption)",
                "-count=1",
            ],
            timeout=180,
        ),
        "focused CLI causal regressions",
    )
    return {"status": "ok", "service": service, "race": race, "cli": cli}


def frozen_option_matrix() -> dict[str, Any]:
    return {
        "inventory": inventory(),
        "literal_matrix": require(
            run(
                [
                    "go",
                    "test",
                    "./go-agent-runtime/services/sessionconfig/...",
                    "-run",
                    "Test(IndependentLiteralTransportMatrix|IndependentLiteralCaptureMatrix|IndependentLiteralProviderDefaults|IndependentLiteralProviderRejections|IndependentLiteralGrokResolution|IndependentLiteralCloneAndCapabilities|PublicTypedErrorsHandleNilAndEmptyCauses)$",
                    "-count=1",
                ],
            ),
            "independent literal option matrix",
        ),
        "consumer": consumer(),
    }


def isolated_mutation(
    *,
    control: str,
    relative_path: str,
    original: str,
    mutated: str,
    oracle: list[str],
    oracle_label: str,
    failure_marker: str,
    positive: dict[str, Any],
) -> dict[str, Any]:
    clean_before = require(run(["git", "status", "--porcelain"]), f"{control} clean-tree precondition")
    if clean_before["stdout"].strip():
        raise VerificationFailure(f"{control} requires a clean working tree before isolated mutation")

    with tempfile.TemporaryDirectory(prefix=f"audio-runtime-c74-{control}-") as temporary:
        mutation_root = pathlib.Path(temporary) / "worktree"
        worktree_added = False
        try:
            require(
                run(["git", "worktree", "add", "--detach", str(mutation_root), "HEAD"]),
                f"{control} isolated worktree setup",
            )
            worktree_added = True
            source = mutation_root / relative_path
            source_text = source.read_text(encoding="utf-8")
            if source_text.count(original) != 1:
                raise VerificationFailure(f"{control} mutation anchor was not unique in {relative_path}")
            source.write_text(source_text.replace(original, mutated, 1), encoding="utf-8")
            changed = require(
                run(["git", "status", "--porcelain", "--", relative_path], cwd=mutation_root),
                f"{control} isolated mutation status",
            )
            if not changed["stdout"].strip():
                raise VerificationFailure(f"{control} isolated source mutation was not observed")
            negative = run(oracle, cwd=mutation_root, timeout=180)
            output = negative["stdout"] + negative["stderr"]
            if negative["returncode"] == 0 or failure_marker not in output:
                raise VerificationFailure(
                    f"{control} mutation was not rejected by {oracle_label}: {negative}"
                )
        finally:
            if worktree_added:
                require(
                    run(["git", "worktree", "remove", "--force", str(mutation_root)]),
                    f"{control} isolated worktree cleanup",
                )

    clean_after = require(run(["git", "status", "--porcelain"]), f"{control} clean-tree postcondition")
    if clean_after["stdout"].strip():
        raise VerificationFailure(f"{control} left the implementation tree dirty")
    return {
        "status": "mutation-rejected",
        "control": control,
        "mutated_path": relative_path,
        "positive": positive,
        "negative": negative,
        "tree_clean": True,
    }


def mutation_control(name: str) -> dict[str, Any]:
    # These controls deliberately mutate a temporary detached worktree. The
    # current implementation tree is clean before and after each mutation;
    # the independent literal oracle must fail against the mutated policy.
    if name == "mutation-accept-conflicting-transports":
        positive = require(
            run(
                [
                    "go",
                    "test",
                    "./go-agent-runtime/services/sessionconfig/internal/service",
                    "-run",
                    "TestIndependentLiteralTransportMatrix/alias_conflict$",
                    "-count=1",
                ]
            ),
            "conflicting-alias positive oracle",
        )
        return isolated_mutation(
            control=name,
            relative_path="go-agent-runtime/services/sessionconfig/internal/service/service.go",
            original='\tif signaling != "" && signaling != request.SignalingEndpoint {\n',
            mutated='\tif false && signaling != "" && signaling != request.SignalingEndpoint {\n',
            oracle=[
                "go",
                "test",
                "./go-agent-runtime/services/sessionconfig/internal/service",
                "-run",
                "TestIndependentLiteralTransportMatrix/alias_conflict$",
                "-count=1",
            ],
            oracle_label="the conflicting-alias literal oracle",
            failure_marker="alias_conflict",
            positive=positive,
        )
    if name == "mutation-alias-turn-detection":
        positive = require(
            run(
                [
                    "go",
                    "test",
                    "./go-agent-runtime/services/sessionconfig/internal/service",
                    "-run",
                    "TestIndependentLiteralCloneAndCapabilities$",
                    "-count=1",
                ]
            ),
            "turn-detection alias positive oracle",
        )
        return isolated_mutation(
            control=name,
            relative_path="go-agent-runtime/services/sessionconfig/internal/service/service.go",
            original="\treturn &copy\n",
            mutated="\treturn policy\n",
            oracle=[
                "go",
                "test",
                "./go-agent-runtime/services/sessionconfig/internal/service",
                "-run",
                "TestIndependentLiteralCloneAndCapabilities$",
                "-count=1",
            ],
            oracle_label="the turn-detection clone literal oracle",
            failure_marker="turn detection was not deeply cloned",
            positive=positive,
        )
    raise VerificationFailure(f"unknown mutation control {name}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--mode",
        choices=(
            "inventory",
            "cli-retirement",
            "public-consumer",
            "consumer",
            "parity",
            "all",
            "frozen-option-matrix",
            "mutation-accept-conflicting-transports",
            "mutation-alias-turn-detection",
        ),
        default="all",
    )
    parser.add_argument("--expect-failure", action="store_true")
    parser.add_argument("--baseline-lines", type=int, default=BASELINE_LINES)
    parser.add_argument("--max-lines", type=int, default=MAX_CANDIDATE_LINES)
    parser.add_argument("--min-retired-lines", type=int, default=MIN_RETIRED_LINES)
    args = parser.parse_args()
    try:
        if args.mode in ("inventory", "cli-retirement"):
            result = inventory(args.baseline_lines, args.max_lines, args.min_retired_lines)
        elif args.mode in ("consumer", "public-consumer"):
            result = consumer()
        elif args.mode == "frozen-option-matrix":
            result = frozen_option_matrix()
        elif args.mode.startswith("mutation-"):
            result = mutation_control(args.mode)
        elif args.mode == "parity":
            result = parity()
        else:
            result = {
                "frozen_option_matrix": frozen_option_matrix(),
                "parity": parity(),
                "mutations": {
                    "conflicting_transports": mutation_control("mutation-accept-conflicting-transports"),
                    "turn_detection": mutation_control("mutation-alias-turn-detection"),
                },
            }
        print(json.dumps({"status": "ACCEPTED", "mode": args.mode, "result": result}, sort_keys=True))
        return 0
    except (OSError, VerificationFailure) as exc:
        print(json.dumps({"status": "FAILED", "mode": args.mode, "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
