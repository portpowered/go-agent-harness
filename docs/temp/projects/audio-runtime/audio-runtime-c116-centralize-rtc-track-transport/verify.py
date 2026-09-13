#!/usr/bin/env python3
"""Fail-closed C116 checks that do not poll CI or mutate the checkout."""

from __future__ import annotations

import argparse
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
BASELINE_IN = 453
BASELINE_OUT = 418
EXTERNAL = HERE / "external-consumer"
OWNED_PREFIXES = (
    "agent-cli/internal/services/internal/devices/service_test.go",
    "agent-cli/internal/services/wire/wire.go",
    "agent-cli/internal/transport/cli/probe_v9_webrtc_device_test.go",
    "agent-cli/internal/wire/rtc_runtime.go",
    "coverage-manifest/go-agent-runtime/services/rtctransport/",
    "docs/temp/projects/audio-runtime/audio-runtime-c116-centralize-rtc-track-transport/",
    "go-agent-runtime/services/devices/internal/probe/probe.go",
    "go-agent-runtime/services/devices/internal/probe/probe_test.go",
    "go-agent-runtime/services/devices/internal/probe/resources.go",
    "go-agent-runtime/services/devices/internal/probe/rtc.go",
    "go-agent-runtime/services/devices/internal/probe/service.go",
    "go-agent-runtime/services/devices/wire/providers.go",
    "go-agent-runtime/services/devices/wire/wire_gen.go",
    "go-agent-runtime/services/rtctransport/",
    "go-llm-gateway/pkg/transport/rtc/track_in.go",
    "go-llm-gateway/pkg/transport/rtc/track_in_test.go",
    "go-llm-gateway/pkg/transport/rtc/track_out.go",
    "go-llm-gateway/pkg/transport/rtc/track_out_test.go",
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
    changed = [
        path for path in command(["git", "diff", "--name-only", ACCEPTED_MAIN, "HEAD"]).splitlines()
        if path and path != "Changes:"
    ]
    outside = [path for path in changed if not any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES)]
    if outside:
        raise VerificationFailure("candidate changed paths outside C116 scope: " + ", ".join(outside))


def final_scope() -> None:
    command(["git", "fetch", "origin", "main"], timeout=60)
    command(["git", "merge-base", "--is-ancestor", STARTUP_REVISION, "HEAD"])
    command(["git", "merge-base", "--is-ancestor", ACCEPTED_MAIN, "HEAD"])
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
