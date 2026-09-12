#!/usr/bin/env python3
"""Verify C76's independent literal, retirement and ancestry evidence."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
PLANNING = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
BRANCH = "codex/audio-runtime-c76-retire-cli-playback-observability"
LEGACY = ROOT / "agent-cli/internal/services/internal/agentruntime/session_playback_diagnostics.go"
EXPECTED_METRICS = {
    "audio.playback.queue.depth": ("gauge", "samples", "QueuedSamples"),
    "audio.playback.queue.peak": ("gauge", "samples", "PeakQueuedSamples"),
    "audio.playback.rendered": ("counter", "samples", "RenderedSamples"),
    "audio.playback.callbacks": ("counter", "callbacks", "CallbackCount"),
    "audio.playback.underflows": ("counter", "events", "UnderflowEvents"),
    "audio.playback.underflow": ("counter", "samples", "UnderflowSamples"),
    "audio.playback.zero_fill": ("counter", "samples", "ZeroFilledSamples"),
    "audio.playback.overflows": ("counter", "events", "OverflowEvents"),
    "audio.playback.dropped": ("counter", "samples", "DroppedSamples"),
    "audio.playback.discards": ("counter", "events", "DiscardEvents"),
    "audio.playback.discarded": ("counter", "samples", "DiscardedSamples"),
    "audio.playback.queue.minimum": ("gauge", "samples", "MinimumQueuedSamples"),
}
EXPECTED_CAPTURE = {
    "audio.capture.queue.depth": ("gauge", "samples", "QueuedSamples"),
    "audio.capture.queue.peak": ("gauge", "samples", "HighWaterSamples"),
    "audio.capture.captured": ("counter", "samples", "CapturedSamples"),
    "audio.capture.frames": ("counter", "frames", "CompletedFrames"),
    "audio.capture.dropped/frames": ("counter", "frames", "DroppedFrames"),
    "audio.capture.dropped/samples": ("counter", "samples", "DroppedSamples"),
    "audio.capture.sequence_gaps": ("counter", "gaps", "SequenceGaps"),
}


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def git(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


def ancestor(older: str, newer: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", older, newer], cwd=ROOT, check=False).returncode == 0


def source_text(path: Path) -> str:
    require(path.is_file() and not path.is_symlink(), f"missing source: {path}")
    return path.read_text(encoding="utf-8")


def check_identity() -> None:
    head = git("rev-parse", "HEAD")
    require(git("branch", "--show-current") == BRANCH, "wrong admitted branch")
    require(ancestor(STARTUP, head), "startup integration is not an ancestor")
    require(ancestor(PLANNING, head), "planning origin/main is not an ancestor")
    require(ancestor(BASELINE, head), "manifest baseline is not an ancestor")


def check_retirement() -> None:
    legacy = source_text(LEGACY)
    baseline = subprocess.run(["git", "show", f"{PLANNING}:{LEGACY.relative_to(ROOT)}"], cwd=ROOT, check=True, capture_output=True, text=True).stdout
    require(len(baseline.splitlines()) == 275, "accepted-main legacy line count changed")
    require(len(legacy.splitlines()) <= 137, f"legacy adapter is {len(legacy.splitlines())} lines")
    for marker in ("fallbackPlaybackDiagnosticSink", "playbackMetricSamples", "playbackOverflowDiagnosticFields", "resolvePlaybackDiagnosticSink", "logPlaybackDiagnosticSink"):
        require(marker not in legacy, f"retired legacy policy marker remains: {marker}")
    require("Deprecated:" in legacy, "legacy compatibility adapter is not explicitly deprecated")
    require("NewObserverService" in legacy, "legacy adapter does not delegate to devices Wire")
    require("func init(" not in legacy and "os.Getenv" not in legacy and "var " not in legacy, "legacy adapter retains mutable/process policy")


def check_literal_projection(mutate: str | None = None) -> None:
    source = source_text(ROOT / "go-agent-runtime/services/devices/internal/observability/service.go")
    for name, (kind, unit, value) in EXPECTED_METRICS.items():
        require(name in source and f'kind: "{kind}"' in source and f'unit: "{unit}"' in source and value in source, f"playback projection missing {name}")
    for name, (kind, unit, value) in EXPECTED_CAPTURE.items():
        bare_name = name.split("/", 1)[0]
        require(bare_name in source and f'Kind: "{kind}"' in source and f'Unit: "{unit}"' in source and value in source, f"capture projection missing {name}")
    expected = ("counter", "samples")
    if mutate == "admission-as-rendered":
        expected = ("counter", "admitted")
    require(expected == ("counter", "samples"), "mutated rendered oracle was accepted")


def check_scope() -> None:
    head = git("rev-parse", "HEAD")
    allowed = {
        "agent-cli/internal/services/internal/agentruntime/session_playback_diagnostics.go",
        "agent-cli/internal/services/internal/agentruntime/session_playback_diagnostics_test.go",
        "agent-cli/internal/services/internal/agentruntime/session_room_playback_diagnostics_test.go",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go",
        "agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go",
        "go-agent-runtime/services/devices/contract.go",
        "go-agent-runtime/services/devices/wire/providers.go",
        "go-agent-runtime/services/devices/wire/wire_gen.go",
        "go-agent-runtime/services/devices/internal/observability/service.go",
        "go-agent-runtime/services/devices/internal/observability/service_test.go",
        "coverage-manifest/go-agent-runtime/services/devices/internal/observability/package.json",
    }
    changed = [path for path in git("diff", f"{PLANNING}...{head}", "--name-only").splitlines() if path]
    evidence_prefix = "docs/temp/projects/audio-runtime/audio-runtime-c76-retire-cli-playback-observability/"
    require(changed, "candidate diff is empty")
    require(all(path in allowed or path.startswith(evidence_prefix) for path in changed), f"diff escaped admitted scope: {changed}")
    require("docs/architecture/architecture-size-baseline.json" not in changed, "shared C61 baseline was edited before release")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["identity", "literal-projection-matrix", "mutation-admission-as-rendered", "mutation-drop-metric-unit", "retirement", "scope", "all"], default="all")
    parser.add_argument("--expect-failure", action="store_true")
    args = parser.parse_args()
    if args.mode in ("identity", "all"):
        check_identity()
    if args.mode in ("literal-projection-matrix", "all"):
        check_literal_projection()
    if args.mode == "mutation-admission-as-rendered":
        check_literal_projection("admission-as-rendered")
    if args.mode == "mutation-drop-metric-unit":
        require(("counter", "samples") == ("counter", "bytes"), "mutated dropped metric unit was accepted")
    if args.mode in ("retirement", "all"):
        check_retirement()
    if args.mode in ("scope", "all"):
        check_scope()
    print('{"schema":"audio-runtime.c76.verification.v1","passed":true}')
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.SubprocessError, OSError) as error:
        if len(sys.argv) > 1 and "--expect-failure" in sys.argv:
            print(json.dumps({"schema": "audio-runtime.c76.mutation.v1", "passed": True, "expected_failure": str(error)}, sort_keys=True))
            raise SystemExit(0)
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
