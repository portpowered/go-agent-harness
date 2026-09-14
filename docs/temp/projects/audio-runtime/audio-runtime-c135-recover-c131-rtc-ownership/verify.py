#!/usr/bin/env python3
"""Fail-closed C135 ownership, causal, scope, and shipped-effect checks."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
PRD = ROOT / "prd.json"
C130_REPORT = HERE / "evidence" / "c130-c116-rtc-track-transport-vertical-probe.json"
RUN_REPORT = HERE / "evidence" / "run-report.json"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PRESERVED_REVISION = "071b0abfd67501db61e3c1929971c6dd6e77eb62"
PLANNING_MAIN = "bd6a1289218d1bef1a3af36e64e9d4496062416f"
C130_REPORT_SHA256 = "a18cbe6fd21d06f83d913d71307ff629b2fcc4a3fa2830690df1fe33f2b4afcf"
REQUIRED_CASES = {
    "help",
    "credential-free-audio-tool",
    "interruption",
    "software-device",
    "external-media",
    "c21-consumption",
}
GATEWAY_FILES = (
    ROOT / "go-llm-gateway/pkg/transport/rtc/track_in.go",
    ROOT / "go-llm-gateway/pkg/transport/rtc/track_out.go",
)
OWNED_PREFIXES = (
    "agent-cli/internal/transport/cli/probe_v9_webrtc_device_test.go",
    "agent-cli/internal/wire/rtc_runtime.go",
    "go-llm-gateway/pkg/transport/rtc/track_in.go",
    "go-llm-gateway/pkg/transport/rtc/track_in_test.go",
    "go-llm-gateway/pkg/transport/rtc/track_out.go",
    "go-llm-gateway/pkg/transport/rtc/track_out_test.go",
    "go-agent-runtime/services/rtctransport/",
    "go-audio/pkg/rtctransport/",
    "go-audio/go.mod",
    "go-audio/go.sum",
    "coverage-manifest/go-agent-runtime/services/rtctransport/",
    "coverage-manifest/go-audio/pkg/rtctransport/",
    "docs/temp/projects/audio-runtime/audio-runtime-c131-repair-c116-gateway-rtc-ownership/",
    "docs/temp/projects/audio-runtime/audio-runtime-c135-recover-c131-rtc-ownership/",
    # C152's caller repair requires the public service to be supplied at the
    # application composition boundary rather than constructed by the probe.
    "agent-cli/internal/services/internal/devices/service_test.go",
    "agent-cli/internal/services/wire/wire.go",
    "agent-cli/internal/transport/cli/device_probe_test.go",
    "agent-cli/internal/wire/wire.go",
    "agent-cli/internal/wire/wire_gen.go",
    "go-agent-runtime/services/devices/internal/probe/",
    "go-agent-runtime/services/devices/wire/",
)

class VerificationFailure(RuntimeError):
    pass


def command(argv: list[str], cwd: Path = ROOT, timeout: int = 240) -> str:
    result = subprocess.run(
        ["rtk", *argv],
        cwd=cwd,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        detail = (result.stdout + result.stderr).strip()
        raise VerificationFailure(
            f"{' '.join(argv)} failed with {result.returncode}: {detail[-2500:]}"
        )
    return result.stdout


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationFailure(message)


def relative(path: Path) -> str:
    return str(path.relative_to(ROOT))


def manifest_and_c130() -> dict:
    require(PRD.is_file(), "missing admitted prd.json")
    manifest = json.loads(PRD.read_text(encoding="utf-8"))
    branch = command(["git", "branch", "--show-current"]).strip()
    require(branch == manifest.get("branchName"), f"branch {branch!r} != prd.branchName {manifest.get('branchName')!r}")
    require(manifest.get("project") == "audio-runtime", "prd project is not audio-runtime")
    require(manifest.get("contractRevision") == "audio-runtime-v1", "prd contract revision changed")
    require(C130_REPORT.is_file(), "preserved C130 report is missing from owned evidence")
    report = json.loads(C130_REPORT.read_text(encoding="utf-8"))
    snapshot = C130_REPORT.read_bytes()
    require(
        hashlib.sha256(snapshot[:-1] if snapshot.endswith(b"\n") else snapshot).hexdigest() == C130_REPORT_SHA256,
        "preserved C130 report bytes do not match the canonical SHA-256",
    )
    require(report.get("decision") == "FAILED", "C130 historical decision was relabeled")
    verdicts = {
        name: item.get("verdict")
        for name, item in report.get("criteria", {}).items()
        if isinstance(item, dict)
    }
    require(verdicts.get("AUDIO") == "FAIL", "C130 AUDIO failure evidence is missing")
    require(verdicts.get("SERVICE") == "FAIL", "C130 SERVICE failure evidence is missing")
    require(verdicts.get("QUALITY") == "FAIL", "C130 QUALITY failure evidence is missing")
    text = C130_REPORT.read_text(encoding="utf-8")
    require("268+247=515" in text, "C130 515-line baseline finding is missing")
    return {
        "branch": branch,
        "prd_branch": manifest["branchName"],
        "c130_decision": report["decision"],
        "c130_gate_verdicts": verdicts,
        "c130_preserved_source_sha256": C130_REPORT_SHA256,
        "c130_candidate_copy_sha256": hashlib.sha256(snapshot).hexdigest(),
    }


def ancestry() -> dict:
    command(["git", "fetch", "origin", "main"], timeout=60)
    origin_main = command(["git", "rev-parse", "origin/main"]).strip()
    revisions = {
        "startup_integration": STARTUP_REVISION,
        "preserved_c131": PRESERVED_REVISION,
        "planning_origin_main": PLANNING_MAIN,
        "review_time_origin_main": origin_main,
    }
    result = {}
    for name, revision in revisions.items():
        check = subprocess.run(
            ["rtk", "git", "merge-base", "--is-ancestor", revision, "HEAD"],
            cwd=ROOT,
            capture_output=True,
        )
        result[name] = {"revision": revision, "is_ancestor": check.returncode == 0}
        require(check.returncode == 0, f"required ancestry missing: {name} {revision}")
    return result


def owner_and_dag() -> dict:
    for path in GATEWAY_FILES:
        text = path.read_text(encoding="utf-8")
        require("Deprecated:" in text, f"{relative(path)} has no Deprecated marker")
        forbidden = (
            "sync.",
            "wavio.",
            "DecodePLC",
            "unwrapSequence",
            "sampleOffsetDuration",
            "func init(",
            " go ",
        )
        found = [token for token in forbidden if token in text]
        require(not found, f"{relative(path)} retains transport policy/state tokens: {found}")
        require('"github.com/portpowered/go-agent-harness/go-agent-runtime' not in text, f"{relative(path)} reverses the module DAG")
    core = ROOT / "go-audio/pkg/rtctransport"
    source = "\n".join(path.read_text(encoding="utf-8") for path in core.glob("*.go") if not path.name.endswith("_test.go"))
    require(source, "core transport source is empty")
    require(len(re.findall(r"^func NewInboundTrack\(", source, re.MULTILINE)) == 1, "core inbound construction is not unique")
    require(len(re.findall(r"^func NewOutboundTrack\(", source, re.MULTILINE)) == 1, "core outbound construction is not unique")
    runtime_service = ROOT / "go-agent-runtime/services/rtctransport/internal/service"
    runtime_files = sorted(
        relative(path)
        for path in runtime_service.glob("*.go")
        if not path.name.endswith("_test.go")
    )
    require(runtime_files == ["go-agent-runtime/services/rtctransport/internal/service/service.go"], f"runtime private shell contains extra implementation files: {runtime_files}")
    require("go-audio/pkg/rtctransport" in (runtime_service / "service.go").read_text(encoding="utf-8"), "runtime shell does not delegate to core")
    require("go-agent-runtime" not in "\n".join(path.read_text(encoding="utf-8") for path in core.glob("*.go")), "core imports runtime")
    audio_deps = command(["proxy", "bash", "-lc", "cd go-audio && GOWORK=off go list -deps ./pkg/rtctransport/..."])
    gateway_deps = command(["proxy", "bash", "-lc", "cd go-llm-gateway && GOWORK=off go list -deps ./pkg/transport/rtc"])
    runtime_deps = command(["proxy", "bash", "-lc", "cd go-agent-runtime && GOWORK=off go list -deps ./services/rtctransport/..."])
    require("go-agent-runtime" not in audio_deps, "go-audio dependency graph imports runtime")
    require("go-agent-runtime" not in gateway_deps, "gateway dependency graph imports runtime")
    require("go-llm-gateway/pkg/transport/rtc" not in runtime_deps, "runtime transport graph imports legacy gateway track package")
    return {
        "gateway_lines": {relative(path): len(path.read_text(encoding="utf-8").splitlines()) for path in GATEWAY_FILES},
        "core_constructors": {"inbound": 1, "outbound": 1},
        "runtime_private_files": runtime_files,
        "dag": "go-agent-runtime -> go-llm-gateway -> go-audio; no reverse transport edge",
    }


def behavioral() -> dict:
    normal = command([
        "go", "test",
        "./go-audio/pkg/rtctransport/...",
        "./go-llm-gateway/pkg/transport/rtc",
        "./go-agent-runtime/services/rtctransport/...",
        "-run", "Inbound|Outbound|RTP|Sequence|Timestamp|PLC|Resample|Ownership|Error|Cancel|Overflow|Close",
        "-count=1", "-timeout=300s",
    ], timeout=360)
    race = command([
        "go", "test", "-race",
        "./go-audio/pkg/rtctransport/...",
        "./go-llm-gateway/pkg/transport/rtc",
        "./go-agent-runtime/services/rtctransport/...",
        "-run", "Inbound|Outbound|Concurrent|Ownership|Error|Cancel|Overflow|Close",
        "-count=5", "-timeout=600s",
    ], timeout=900)
    consumer = command([
        "proxy", "bash",
        "docs/temp/projects/audio-runtime/audio-runtime-c131-repair-c116-gateway-rtc-ownership/verify-external-consumer.sh",
    ], timeout=240)
    diff_check = command(["git", "diff", "--check"])
    return {
        "normal": normal[-2000:],
        "race": race[-2000:],
        "external_consumer": consumer[-2000:],
        "diff_check": diff_check,
    }


def shipped() -> dict:
    require(RUN_REPORT.is_file(), "run.py evidence/run-report.json is missing")
    report = json.loads(RUN_REPORT.read_text(encoding="utf-8"))
    cases = report.get("cases", {})
    require(set(cases) == REQUIRED_CASES, f"shipped case set {sorted(cases)} != {sorted(REQUIRED_CASES)}")
    for name, result in cases.items():
        require(result.get("returncode") == 0, f"shipped case failed: {name}: {result.get('stderr', '')[-1000:]}")
        require(not result.get("timed_out"), f"shipped case timed out: {name}")
        cleanup = result.get("cleanup", {})
        require(cleanup.get("reaped") is True, f"shipped case was not reaped: {name}")
        require(cleanup.get("survivors") is False, f"shipped case left a process group: {name}")
    artifact = report.get("artifact", {})
    require(len(artifact.get("sha256", "")) == 64, "shipped artifact hash is missing")
    require(report.get("hardware_acoustics") == "out_of_scope", "hardware/acoustic evidence was not kept out of scope")
    leaked = re.compile(r"(?i)(sk-[a-z0-9]{8,}|bearer\s+[a-z0-9._-]{16,}|-----begin [a-z ]+ private key-----)")
    serialized = json.dumps(report)
    require(not leaked.search(serialized), "shipped evidence contains credential-like material")
    return {
        "artifact": artifact,
        "cases": sorted(cases),
        "process_cleanup": "all requested process groups reaped with no survivors",
        "hardware_acoustics": "out_of_scope",
    }


def final_scope() -> dict:
    admission = manifest_and_c130()
    history = ancestry()
    changed = [
        line.strip()
        for line in command(["git", "diff", "--name-only", "origin/main...HEAD"]).splitlines()
        if line.strip() and line.strip() != "Changes:"
    ]
    outside = [
        path for path in changed
        if not any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES)
    ]
    require(not outside, "candidate changed paths outside the C135/C152 recovery scope: " + ", ".join(outside))
    counts = {relative(path): len(path.read_text(encoding="utf-8").splitlines()) for path in GATEWAY_FILES}
    require(sum(counts.values()) <= 503, f"gateway line budget failed: {counts}")
    diff_check = command(["git", "diff", "--check"])
    return {
        "admission": admission,
        "ancestry": history,
        "changed_paths": changed,
        "gateway_lines": counts,
        "gateway_total": sum(counts.values()),
        "outside_scope": outside,
        "diff_check": diff_check,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=[
        "ownership-dag-and-api",
        "behavioral-and-causal-negatives",
        "shipped-effects-hashes-and-cleanup",
        "final-scope-provenance-and-size",
    ])
    args = parser.parse_args()
    try:
        if args.mode == "ownership-dag-and-api":
            payload = {"admission_and_history": manifest_and_c130(), "ownership": owner_and_dag()}
        elif args.mode == "behavioral-and-causal-negatives":
            payload = behavioral()
        elif args.mode == "shipped-effects-hashes-and-cleanup":
            payload = shipped()
        else:
            payload = final_scope()
        print(json.dumps({"mode": args.mode, "status": "passed", "payload": payload}, indent=2, sort_keys=True))
        return 0
    except (VerificationFailure, subprocess.TimeoutExpired, OSError, json.JSONDecodeError) as error:
        print(json.dumps({"mode": args.mode, "status": "failed", "error": str(error)}, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
