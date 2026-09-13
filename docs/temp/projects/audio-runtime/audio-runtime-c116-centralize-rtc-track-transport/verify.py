#!/usr/bin/env python3
"""Fail-closed C116 checks that do not poll CI or mutate the checkout."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
WORK = "audio-runtime-c116-centralize-rtc-track-transport"
BRANCH = "codex/audio-runtime-c116-centralize-rtc-track-transport"
ACCEPTED_MAIN = "3963bc3566da24f8214634c17a9d0f79a6724171"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
REJECTED_CANDIDATE = "e367e90d408fca82ca45c60312eaf76205cba4b8"
MANIFEST = ROOT / "factory/projects/audio-runtime/manifest.json"
MANIFEST_SHA256 = "ec1439b3b1edf5ab935a59cfe67756b67f4a27e51c20ffcaad87ffab35acdf3d"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
BASELINE_IN = 453
BASELINE_OUT = 418
EXTERNAL = HERE / "external-consumer"
WIRE_REGISTRY = ROOT / "scripts/wire-packages.txt"
ARCHITECTURE_POLICY = ROOT / "docs/architecture/architecture-policy.json"
RUNTIME_WIRE_PACKAGE = "go-agent-runtime/services/rtctransport/wire"
OWNED_PREFIXES = (
    "agent-cli/internal/wire/rtc_runtime.go",
    "coverage-manifest/go-agent-runtime/services/rtctransport/",
    "docs/temp/projects/audio-runtime/audio-runtime-c116-centralize-rtc-track-transport/",
    "go-agent-runtime/services/rtctransport/",
    "go-llm-gateway/pkg/transport/rtc/track_in.go",
    "go-llm-gateway/pkg/transport/rtc/track_in_test.go",
    "go-llm-gateway/pkg/transport/rtc/track_out.go",
    "go-llm-gateway/pkg/transport/rtc/track_out_test.go",
)
REVIEW_REJECTED_PATHS = (
    "agent-cli/internal/services/internal/devices/service_test.go",
    "agent-cli/internal/services/wire/wire.go",
    "agent-cli/internal/transport/cli/probe_v9_webrtc_device_test.go",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/probe_v9_webrtc_device_test.go.json",
    "go-agent-runtime/services/devices/internal/probe/probe.go",
    "go-agent-runtime/services/devices/internal/probe/probe_test.go",
    "go-agent-runtime/services/devices/internal/probe/resources.go",
    "go-agent-runtime/services/devices/internal/probe/rtc.go",
    "go-agent-runtime/services/devices/internal/probe/service.go",
    "go-agent-runtime/services/devices/wire/providers.go",
    "go-agent-runtime/services/devices/wire/wire_gen.go",
)
TRANSFERRED_SHARED_PATHS = (
    # C79's reviewed guarded merge released these shared files to C116 for
    # exactly one Wire registration and four demonstrated stale deletions.
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-policy.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc/track_in.go.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc/track_in_test.go.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc/track_out.go.json",
    "docs/architecture/baselines/github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc/track_out_test.go.json",
)


class VerificationFailure(RuntimeError):
    pass


def command(argv: list[str], cwd: pathlib.Path = ROOT, timeout: int = 180) -> str:
    result = subprocess.run(
        ["rtk", *argv], cwd=cwd, text=True, capture_output=True, timeout=timeout,
    )
    if result.returncode != 0:
        detail = (result.stdout + result.stderr).strip()
        raise VerificationFailure(f"{' '.join(argv)} failed: {detail[-2000:]}")
    return result.stdout


def require(path: pathlib.Path) -> None:
    if not path.exists():
        raise VerificationFailure(f"missing required path: {path.relative_to(ROOT)}")


def manifest_identity() -> None:
    require(MANIFEST)
    raw = MANIFEST.read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    if digest != MANIFEST_SHA256:
        raise VerificationFailure(
            f"admitted manifest sha256 {digest} does not match pinned {MANIFEST_SHA256}"
        )
    try:
        manifest = json.loads(raw)
    except json.JSONDecodeError as error:
        raise VerificationFailure(f"admitted manifest is not valid JSON: {error}") from error
    if not isinstance(manifest, dict):
        raise VerificationFailure("admitted manifest root must be an object")
    for field, expected in (
        ("project", "audio-runtime"),
        ("contractRevision", "audio-runtime-v1"),
        ("baselineRevision", BASELINE_REVISION),
    ):
        observed = manifest.get(field)
        if observed != expected:
            raise VerificationFailure(
                f"admitted manifest {field} {observed!r} does not match {expected!r}"
            )


def module_boundary() -> None:
    require(ROOT / "go-agent-runtime/services/rtctransport/contract.go")
    require(ROOT / "go-agent-runtime/services/rtctransport/internal/service/inbound.go")
    require(ROOT / "go-agent-runtime/services/rtctransport/internal/service/outbound.go")
    require(ROOT / "go-agent-runtime/services/rtctransport/wire/providers.go")
    require(ROOT / "go-agent-runtime/services/rtctransport/wire/wire_gen.go")
    require(EXTERNAL / "go.mod")
    require(EXTERNAL / "consumer_test.go")

    command(["env", "GOWORK=off", "go", "list", "-mod=mod", "./..."], ROOT / "go-llm-gateway")
    command(["env", "GOWORK=off", "go", "list", "-mod=mod", "./..."], ROOT / "go-agent-runtime")
    command(["env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=180s"], EXTERNAL)

    for path in (ROOT / "go-llm-gateway").rglob("*.go"):
        if "go-agent-runtime" in path.read_text(encoding="utf-8"):
            raise VerificationFailure(f"gateway source imports runtime: {path.relative_to(ROOT)}")
    gateway_mod = ROOT / "go-llm-gateway/go.mod"
    if "go-agent-runtime" in gateway_mod.read_text(encoding="utf-8"):
        raise VerificationFailure("gateway go.mod reverses the runtime module edge")
    deps = command(["env", "GOWORK=off", "go", "list", "-deps", "./services/rtctransport/..."], ROOT / "go-agent-runtime")
    if "agent-cli" in deps or "go-llm-gateway/pkg/transport/rtc" in deps:
        raise VerificationFailure("rtctransport dependency census includes a forbidden application/legacy track")


def focused(mode: str) -> None:
    if mode == "inbound-positive-and-causal-negatives":
        pattern = "InboundTransport|TransportConstruction"
    elif mode == "outbound-positive-and-causal-negatives":
        pattern = "OutboundTransport|TransportConstruction"
    else:
        raise VerificationFailure(f"unsupported focused mode: {mode}")
    command([
        "go", "test", "./go-agent-runtime/services/rtctransport/...",
        "-run", pattern, "-count=3", "-timeout=180s",
    ], ROOT)


def retirement() -> None:
    inbound = int(command(["wc", "-l", "go-llm-gateway/pkg/transport/rtc/track_in.go"]).split()[0])
    outbound = int(command(["wc", "-l", "go-llm-gateway/pkg/transport/rtc/track_out.go"]).split()[0])
    if inbound + outbound >= BASELINE_IN + BASELINE_OUT:
        raise VerificationFailure(
            f"gateway retirement target not met: {inbound}+{outbound} >= {BASELINE_IN + BASELINE_OUT}"
        )
    source = (ROOT / "agent-cli/internal/wire/rtc_runtime.go").read_text(encoding="utf-8")
    if "rtc.NewOutboundTrack" in source or "rtc.NewInboundTrack" in source:
        raise VerificationFailure("owned RTC composition still constructs a gateway track")
    for path in (ROOT / "agent-cli/internal/wire/rtc_runtime.go",):
        text = path.read_text(encoding="utf-8")
        for forbidden in ("unwrapSequence", "DecodePLC", "wavio.Resample", "sampleOffsetDuration"):
            if forbidden in text:
                raise VerificationFailure(f"CLI adapter contains transport policy {forbidden}")
    changed = {
        path for path in command(["git", "diff", "--name-only", "origin/main"]).splitlines()
        if path and path != "Changes:"
    }
    prior_changed = {
        path for path in command(["git", "diff", "--name-only", REJECTED_CANDIDATE]).splitlines()
        if path and path != "Changes:"
    }
    newly_changed = changed & prior_changed
    outside = [
        path for path in newly_changed
        if not any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES)
        and path not in TRANSFERRED_SHARED_PATHS
    ]
    if outside:
        raise VerificationFailure("new candidate paths outside C116 scope: " + ", ".join(sorted(outside)))
    rejected_diffs = command([
        "git", "diff", "--name-only", "origin/main", "--", *REVIEW_REJECTED_PATHS,
    ]).splitlines()
    rejected_diffs = [path for path in rejected_diffs if path and path != "Changes:"]
    if rejected_diffs:
        raise VerificationFailure(
            "review-rejected paths still differ from origin/main: " + ", ".join(rejected_diffs)
        )
    if not WIRE_REGISTRY.exists():
        raise VerificationFailure("shared Wire registry is missing after C79 transfer")
    current_registry = [line.strip() for line in WIRE_REGISTRY.read_text(encoding="utf-8").splitlines() if line.strip() and not line.lstrip().startswith("#")]
    origin_registry = [line.strip() for line in command(["git", "show", f"origin/main:{WIRE_REGISTRY.relative_to(ROOT)}"]).splitlines() if line.strip() and not line.lstrip().startswith("#")]
    expected_registry = origin_registry[:2] + [RUNTIME_WIRE_PACKAGE] + origin_registry[2:]
    if current_registry != expected_registry:
        raise VerificationFailure("shared Wire registry differs from origin/main by more than the ordered rtctransport entry")

    try:
        policy = json.loads(ARCHITECTURE_POLICY.read_text(encoding="utf-8"))
        origin_policy = json.loads(command(["git", "show", f"origin/main:{ARCHITECTURE_POLICY.relative_to(ROOT)}"]))
    except (json.JSONDecodeError, VerificationFailure) as error:
        raise VerificationFailure(f"architecture policy is not valid JSON: {error}") from error
    wanted_generated = {
        "module": "go-agent-runtime",
        "pattern": "services/rtctransport/wire/wire_gen.go",
        "generator": "wire",
        "header": "// Code generated by Wire. DO NOT EDIT.",
    }
    generated = policy.get("generated_files", [])
    if generated.count(wanted_generated) != 1:
        raise VerificationFailure("shared architecture policy must contain exactly one rtctransport Wire entry")
    policy_without_runtime = dict(policy)
    policy_without_runtime["generated_files"] = [entry for entry in generated if entry != wanted_generated]
    if policy_without_runtime != origin_policy:
        raise VerificationFailure("shared architecture policy contains changes beyond the rtctransport Wire entry")
    stale_baselines = [
        path for path in TRANSFERRED_SHARED_PATHS
        if path.endswith(".json") and path.startswith("docs/architecture/baselines/") and (ROOT / path).exists()
    ]
    if stale_baselines:
        raise VerificationFailure("C116 stale RTC baselines still exist: " + ", ".join(stale_baselines))


def final_scope() -> None:
    command(["git", "fetch", "origin", "main"], timeout=60)
    command(["git", "merge-base", "--is-ancestor", STARTUP_REVISION, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", ACCEPTED_MAIN, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"])
    branch = command(["git", "branch", "--show-current"]).strip()
    if branch != BRANCH:
        raise VerificationFailure(f"branch {branch!r} does not match manifest {BRANCH!r}")
    retirement()
    module_boundary()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True)
    args = parser.parse_args()
    try:
        manifest_identity()
        if args.mode == "module-boundary":
            module_boundary()
        elif args.mode in {"inbound-positive-and-causal-negatives", "outbound-positive-and-causal-negatives"}:
            focused(args.mode)
        elif args.mode == "retirement-adapter-callers-and-scope":
            retirement()
        elif args.mode == "final-scope-and-provenance":
            final_scope()
        else:
            raise VerificationFailure(f"unknown mode: {args.mode}")
    except (VerificationFailure, subprocess.TimeoutExpired) as error:
        print(f"C116 verification failed: {error}", file=sys.stderr)
        return 1
    print(f"C116 verification passed: {args.mode}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
