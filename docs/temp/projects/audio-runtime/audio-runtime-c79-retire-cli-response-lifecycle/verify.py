#!/usr/bin/env python3
"""Bounded C79 lifecycle, retirement, scope, and mutation evidence."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
EVIDENCE = HERE / "evidence"
SERVICE_FILE = ROOT / "go-agent-runtime/services/sessiondiagnostics/internal/service/scheduled.go"
LEGACY_FILES = [
    ROOT / "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go",
    ROOT / "agent-cli/internal/services/internal/agentruntime/session_diagnostics_response.go",
]
ALLOWED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_response.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_test.go",
    "go-agent-runtime/services/sessiondiagnostics/",
    "coverage-manifest/go-agent-runtime/services/sessiondiagnostics/",
    "docs/temp/projects/audio-runtime/audio-runtime-c79-retire-cli-response-lifecycle/",
)
# The current implementation handoff released these shared static files to C79
# for the demonstrated Wire registration and baseline repair already preserved
# on this branch.
RELEASED_SHARED_PATHS = (
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-policy.json",
    "docs/architecture/architecture-size-baseline.json",
)
ALLOWED_PREFIXES += RELEASED_SHARED_PATHS


def command(argv: list[str], *, cwd: Path = ROOT, timeout: int = 240, env: dict[str, str] | None = None) -> dict:
    merged = os.environ.copy()
    merged.update(env or {})
    merged["GOCACHE"] = "/tmp/go-build-audio-runtime-c79-evidence"
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=cwd, env=merged, capture_output=True, text=True, timeout=timeout)
        return {"argv": argv, "returncode": result.returncode, "stdout": result.stdout[-12000:], "stderr": result.stderr[-12000:], "elapsed_seconds": round(time.monotonic() - started, 3)}
    except subprocess.TimeoutExpired as exc:
        return {"argv": argv, "returncode": 124, "stdout": str(exc.stdout or "")[-12000:], "stderr": str(exc.stderr or "")[-12000:], "elapsed_seconds": round(time.monotonic() - started, 3), "timeout": True}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def record(mode: str, payload: dict) -> dict:
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    result = {"schema": "audio-runtime-c79-evidence/v1", "mode": mode, "candidate": command(["git", "rev-parse", "HEAD"])["stdout"].strip(), "payload": payload}
    (EVIDENCE / f"latest-{mode}.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return result


def line_evidence() -> dict:
    counts = {str(path.relative_to(ROOT)): len(path.read_text(encoding="utf-8").splitlines()) for path in LEGACY_FILES}
    total = sum(counts.values())
    return {"files": counts, "total": total, "baseline": 1387, "retired": 1387 - total, "maximum": 787, "minimum_retired": 600}


def scope_evidence() -> dict:
    result = command(["git", "diff", "--name-only", "84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f..HEAD"])
    paths = [line for line in result["stdout"].splitlines() if line]
    outside = [path for path in paths if not any(path == prefix or path.startswith(prefix) for prefix in ALLOWED_PREFIXES)]
    released = [path for path in paths if path in RELEASED_SHARED_PATHS]
    return {"paths": paths, "outside_owned_paths": outside, "released_shared_paths": released}


def focused() -> dict:
    checks = [
        command(["go", "test", "./go-agent-runtime/services/sessiondiagnostics/...", "-run", "Open|Adopt|End|Missing|Duplicate|Wrong|Purpose|Terminal|Scheduled|Tool|Continuation|Retry|Failure|Cancel|Close|Order|Reset", "-count=1", "-timeout=180s"]),
        command(["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "Session(ProgressObserver|Diagnostics)|Scheduled|Continuation|Terminal|Cancellation", "-count=1", "-timeout=210s"]),
        command(["go", "test", "./...", "-count=1", "-timeout=90s"], cwd=HERE / "external-consumer", env={"GOWORK": "off"}),
    ]
    require(all(check["returncode"] == 0 for check in checks), "one or more focused checks failed: " + json.dumps(checks))
    return {"checks": checks, "lines": line_evidence(), "scope": scope_evidence()}


def retirement() -> dict:
    lines = line_evidence()
    scope = scope_evidence()
    response = (LEGACY_FILES[1]).read_text(encoding="utf-8")
    service_root = ROOT / "go-agent-runtime/services/sessiondiagnostics"
    service_text = "\n".join(path.read_text(encoding="utf-8") for path in service_root.rglob("*.go"))
    diff_check = command(["git", "diff", "--check"])
    require(lines["total"] <= 787 and lines["retired"] >= 600, f"retirement threshold failed: {lines}")
    require(not scope["outside_owned_paths"], f"scope failed: {scope}")
    require("Deprecated:" in response, "legacy adapter lacks an explicit Deprecated marker")
    require(not any(token in service_text for token in ("func init(", "os.Getenv", "os.LookupEnv", "time.Sleep")), "service boundary contains forbidden initialization or sleeping")
    require(diff_check["returncode"] == 0, f"diff check failed: {diff_check}")
    return {"lines": lines, "scope": scope, "diff_check": diff_check, "service_forbidden_token_scan": "pass"}


def mutation(mode: str) -> dict:
    original = SERVICE_FILE.read_text(encoding="utf-8")
    if mode == "mutation-remove-continuation-owner":
        needle = "if index, ok := r.pendingContinuationIndexLocked(); ok {"
        replacement = "if index, ok := r.pendingContinuationIndexLocked(); false && ok { // mutation removes continuation ownership\n"
        expected = "consumed wrong scheduled slot"
    else:
        needle = "if lifecycle.Disposition != sessiondiagnostics.DispositionPending {"
        replacement = "if false { // mutation permits duplicate scheduled disposition\n"
        expected = "duplicate disposition was accepted"
    require(original.count(needle) == 1, f"mutation anchor count was not one for {mode}")
    mutated = original.replace(needle, replacement, 1)
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c79-mutation-") as directory:
        mutated_path = Path(directory) / "service.go"
        mutated_path.write_text(mutated, encoding="utf-8")
        overlay_path = Path(directory) / "overlay.json"
        overlay_path.write_text(json.dumps({"Replace": {str(SERVICE_FILE): str(mutated_path)}}), encoding="utf-8")
        result = command(["go", "test", "-overlay", str(overlay_path), "./go-agent-runtime/services/sessiondiagnostics/wire", "-run", "TestToolContinuationRemainsOneScheduledLifecycle", "-count=1", "-timeout=120s"])
    combined = result["stdout"] + result["stderr"]
    require(result["returncode"] != 0, f"{mode} unexpectedly passed")
    require(expected in combined, f"{mode} failed without its intended assertion: {combined[-2000:]}")
    return {"mutation": mode, "anchor": needle, "result": result, "intended_failure": expected}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["response-matrix", "retirement-and-scope", "mutation-remove-continuation-owner", "mutation-duplicate-scheduled-disposition"])
    parser.add_argument("--expect-failure", action="store_true")
    args = parser.parse_args()
    try:
        if args.mode.startswith("mutation-"):
            require(args.expect_failure, "mutation evidence requires --expect-failure")
            payload = mutation(args.mode)
        elif args.mode == "response-matrix":
            payload = focused()
        else:
            payload = retirement()
        output = record(args.mode, payload)
        print(json.dumps(output, indent=2, sort_keys=True))
        return 0
    except Exception as exc:
        print(json.dumps({"schema": "audio-runtime-c79-evidence/v1", "mode": args.mode, "status": "failed", "error": str(exc)}, indent=2), flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
