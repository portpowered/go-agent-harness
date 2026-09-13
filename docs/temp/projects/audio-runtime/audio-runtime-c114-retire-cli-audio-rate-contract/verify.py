#!/usr/bin/env python3
"""Bounded, fail-closed evidence checks for the C114 audio-rate slice."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
WORK = "audio-runtime-c114-retire-cli-audio-rate-contract"
BRANCH = "codex/audio-runtime-c114-retire-cli-audio-rate-contract"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
ACCEPTED_MAIN = "3963bc3566da24f8214634c17a9d0f79a6724171"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
RUNS = HERE / "runs"
RATE_FILE = "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go"
FORMAT_FILE = "agent-cli/internal/services/internal/agentruntime/session_audio_format.go"
OWNED_PREFIX = str(HERE.relative_to(ROOT)) + "/"
LEGACY = {
    RATE_FILE: (68, "4b4d0e9ff4d9bc150ad68a4ea55b74f3d538f4069fe09a84d5a3eeecfb50d3f3"),
    FORMAT_FILE: (84, "086ecad62dcb61fd98ac8884da2fcaf79b5d5b8a967c6f03a5592f5373dac114"),
}
CALLERS = [
    "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_dispatch.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_planning.go",
]


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git(*args: str) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True, check=False, timeout=20)
    require(result.returncode == 0, f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def git_bytes(*args: str) -> bytes:
    result = subprocess.run(["git", *args], cwd=ROOT, capture_output=True, check=False, timeout=20)
    require(result.returncode == 0, f"git {' '.join(args)} failed: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout


def ancestor(revision: str, descendant: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", revision, descendant], cwd=ROOT, check=False).returncode == 0


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def run_command(label: str, args: list[str], cwd: Path = ROOT, timeout: float = 60, workspace: bool = True) -> dict[str, Any]:
    env = os.environ.copy()
    if workspace:
        env.pop("GOWORK", None)
    else:
        env["GOWORK"] = "off"
    started = time.monotonic()
    process = subprocess.Popen(
        ["rtk", "proxy", *args],
        cwd=cwd,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        os.killpg(process.pid, signal.SIGTERM)
        try:
            tail_out, tail_err = process.communicate(timeout=3)
            stdout += tail_out
            stderr += tail_err
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            tail_out, tail_err = process.communicate()
            stdout += tail_out
            stderr += tail_err
    return {
        "label": label,
        "argv": args,
        "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
        "exitCode": process.returncode,
        "timedOut": timed_out,
        "elapsedSeconds": round(time.monotonic() - started, 3),
        "stdout": stdout.decode(errors="replace")[-1 << 20 :],
        "stderr": stderr.decode(errors="replace")[-1 << 20 :],
    }


def require_success(result: dict[str, Any]) -> None:
    require(not result["timedOut"] and result["exitCode"] == 0, f"{result['label']} failed: {result['stderr'][-1200:]}")


def common_scope() -> dict[str, Any]:
    prd = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    require(prd["project"] == "audio-runtime" and prd["contractRevision"] == "audio-runtime-v1", "PRD identity mismatch")
    require(prd["branchName"] == BRANCH and git("branch", "--show-current") == BRANCH, "branch does not match the admitted PRD")
    head = git("rev-parse", "HEAD")
    origin_main = git("rev-parse", "origin/main")
    require(ancestor(STARTUP, head), "startup integration ancestry is missing")
    require(ancestor(ACCEPTED_MAIN, head), "accepted main ancestry is missing")
    require(ancestor(origin_main, head), "fresh origin/main ancestry is missing")
    require(ancestor(BASELINE, head), "manifest baseline ancestry is missing")
    admission = run_command(
        "admission",
        [
            "python3",
            str(FACTORY_ROOT / "factory/scripts/project-control.py"),
            "verify-work",
            "--type",
            "task",
            "--name",
            WORK,
            "--root",
            str(FACTORY_ROOT),
        ],
        timeout=30,
    )
    require_success(admission)
    require('"status": "admitted"' in admission["stdout"], "task admission did not return admitted")

    changed = [path for path in git("diff", "--name-only", ACCEPTED_MAIN, head).splitlines() if path]
    outside = [path for path in changed if not is_owned(path)]
    require(not outside, "committed mutation outside C114 ownership: " + ", ".join(outside))
    status = []
    for line in git("status", "--porcelain=v1", "--untracked-files=all").splitlines():
        if not line:
            continue
        path = line[2:]
        if path.startswith(" "):
            path = path[1:]
        status.append(path)
    outside = [path for path in status if not is_owned(path)]
    require(not outside, "working-tree mutation outside C114 ownership: " + ", ".join(outside))
    for shared in ("scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json"):
        result = subprocess.run(["git", "diff", "--quiet", ACCEPTED_MAIN, head, "--", shared], cwd=ROOT, check=False)
        require(result.returncode == 0, f"shared C79 path changed: {shared}")

    legacy = {}
    total_before = 0
    total_after = 0
    for path, (before_lines, before_sha) in LEGACY.items():
        accepted = git_bytes("show", f"{ACCEPTED_MAIN}:{path}")
        current = (ROOT / path).read_bytes()
        require(accepted.count(b"\n") == before_lines and digest(accepted) == before_sha, f"accepted baseline drift: {path}")
        after_lines = current.count(b"\n")
        legacy[path] = {
            "baselineLines": before_lines,
            "baselineSHA256": before_sha,
            "candidateLines": after_lines,
            "candidateSHA256": digest(current),
        }
        total_before += before_lines
        total_after += after_lines
    require(total_after < total_before, "legacy production line reduction is not positive")

    service_root = ROOT / "go-agent-runtime/services/audiorate"
    go_files = [path for path in service_root.rglob("*.go") if "_test.go" not in path.name]
    forbidden = re.compile(r"agent-cli|flag|func\s+init\s*\(|os\.(Getenv|LookupEnv)|net\.|credential|device\s+io", re.IGNORECASE)
    violations = [str(path.relative_to(ROOT)) for path in go_files if forbidden.search(path.read_text(encoding="utf-8"))]
    require(not violations, "host-specific audiorate source marker: " + ", ".join(violations))
    for path in CALLERS:
        result = subprocess.run(["git", "diff", "--quiet", ACCEPTED_MAIN, head, "--", path], cwd=ROOT, check=False)
        require(result.returncode == 0, f"read-only caller changed: {path}")
    for manifest in (
        ROOT / "coverage-manifest/go-agent-runtime/services/audiorate/package.json",
        ROOT / "coverage-manifest/go-agent-runtime/services/audiorate/internal/service/package.json",
        ROOT / "coverage-manifest/go-agent-runtime/services/audiorate/wire/package.json",
    ):
        require(manifest.is_file(), f"coverage registration missing: {manifest.relative_to(ROOT)}")
    summary = json.loads((HERE / "verification-summary.json").read_text(encoding="utf-8"))
    require(ancestor(summary["candidateRevision"], head), "verification summary is not bound to the candidate")
    return {
        "head": head,
        "originMain": origin_main,
        "admission": admission,
        "legacy": legacy,
        "netProductionLineReduction": total_before - total_after,
        "changedPaths": changed,
    }


def is_owned(path: str) -> bool:
    return path in {"agent-cli/internal/services/internal/agentruntime/session_audio_rate.go", FORMAT_FILE} or path.startswith(OWNED_PREFIX) or path.startswith("go-agent-runtime/services/audiorate/") or path.startswith("coverage-manifest/go-agent-runtime/services/audiorate/")


def verify_mode(mode: str) -> dict[str, Any]:
    scope = common_scope()
    checks: list[dict[str, Any]] = []
    if mode == "positive-and-causal-negative-controls":
        checks.append(run_command("external-consumer", ["go", "run", "."], HERE / "external-consumer", workspace=False))
        require_success(checks[-1])
        require("external-consumer-ok" in checks[-1]["stdout"], "consumer omitted its observable effects")
        checks.append(run_command("audiorate-causal", ["go", "test", "./go-agent-runtime/services/audiorate/...", "-run", "Test(Convert|Resolve|Configure|Service|NewService)", "-count=1", "-timeout=180s"], timeout=240))
        require_success(checks[-1])
    elif mode == "retirement-callers-and-c101-readonly":
        checks.append(run_command("cli-audio-regressions", ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "Test(StreamSessionAudioInputResamples16kHzAtProviderBoundary|RoomProviderInputPCMResamples16kHzMixerTo24kHzContract|ConvertSessionAudioPCMIdentityAndFailures|ConvertScheduledAudioInputsUsesDeclaredSourceRate|ConfigureSessionAudioContract|LoadReplaySessionConfigurationExtractsDuplexAudioRates|ReplayPlannersRejectCapturedAsymmetricAudioRatesBeforeDialerConstruction|LiveRecordRuntimeScheduledAudio)", "-count=1", "-timeout=240s"], timeout=300))
        require_success(checks[-1])
        require(not any("c101" in path.lower() for path in scope["changedPaths"]), "C101 path appeared in the candidate diff")
    elif mode == "final-scope-and-provenance":
        checks.append(run_command("external-consumer", ["go", "run", "."], HERE / "external-consumer", workspace=False))
        require_success(checks[-1])
        checks.append(run_command("cli-audio-regressions", ["go", "test", "./agent-cli/internal/services/internal/agentruntime", "-run", "Test(StreamSessionAudioInputResamples16kHzAtProviderBoundary|RoomProviderInputPCMResamples16kHzMixerTo24kHzContract|ConvertSessionAudioPCMIdentityAndFailures|ConvertScheduledAudioInputsUsesDeclaredSourceRate|ConfigureSessionAudioContract|LoadReplaySessionConfigurationExtractsDuplexAudioRates|ReplayPlannersRejectCapturedAsymmetricAudioRatesBeforeDialerConstruction|LiveRecordRuntimeScheduledAudio)", "-count=1", "-timeout=240s"], timeout=300))
        require_success(checks[-1])
    else:
        raise VerificationError(f"unsupported mode: {mode}")
    return {"status": "ACCEPTED", "mode": mode, "scope": scope, "checks": checks}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["positive-and-causal-negative-controls", "retirement-callers-and-c101-readonly", "final-scope-and-provenance"])
    args = parser.parse_args()
    started = time.monotonic()
    try:
        report = verify_mode(args.mode)
    except (OSError, VerificationError, json.JSONDecodeError) as exc:
        print(json.dumps({"status": "FAILED", "mode": args.mode, "error": str(exc)}, sort_keys=True))
        return 1
    RUNS.mkdir(parents=True, exist_ok=True)
    report["elapsedSeconds"] = round(time.monotonic() - started, 3)
    report_path = RUNS / f"verify-{args.mode}.json"
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "ACCEPTED", "mode": args.mode, "report": str(report_path.relative_to(HERE))}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
