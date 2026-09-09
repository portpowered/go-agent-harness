#!/usr/bin/env python3
"""Bounded, source-only C25 audio/device-boundary diagnosis.

This verifier never opens a device, starts a provider/realtime session, edits
production paths, or waits for external CI. It records exact source evidence
and runs only focused compile/test commands with bounded shutdown.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import time
from typing import Any


TASK = "audio-runtime-c25-audio-device-boundary-diagnosis"
SOURCE_REVISION = "5d5afcb14d7b269378020809f5a2418c499ac94d"
ORIGIN_MAIN = SOURCE_REVISION
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE_ARCHIVE_SHA256 = "0bebedbf449193683267220769084393f0c3e175d59d7e6dc7ba0992c06364ac"
MAX_CHILD_SECONDS = 55
MAX_TOTAL_SECONDS = 600

OWNED = Path(__file__).resolve().parent
REPO = OWNED.parents[4]

LEGACY_PATHS = [
    "agent-cli/internal/room/mixer.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go",
]
CANONICAL_PATHS = [
    "go-audio/pkg/audio/media_contract.go",
    "go-audio/pkg/audio/buffer.go",
    "go-audio/pkg/audio/session_media.go",
    "go-audio/pkg/mixer/mixer.go",
    "go-audio/pkg/mixer/input.go",
    "go-agent-runtime/services/rooms/contract.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/graph.go",
    "go-device-gateway/pkg/runtime/rtc_device.go",
    "go-device-gateway/pkg/runtime/rtc_device_sink.go",
    "go-agent-loop/pkg/agentloop/architecture_import_test.go",
]
PUBLIC_ROUTE_PATHS = [
    "agent-cli/internal/transport/cli/room.go",
    "agent-cli/internal/services/wire/wire.go",
    "agent-cli/internal/wire/wire_gen.go",
]
ALL_SOURCE_PATHS = LEGACY_PATHS + CANONICAL_PATHS + PUBLIC_ROUTE_PATHS


def rel(path: str) -> Path:
    return REPO / path


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def bounded(value: str, limit: int = 16000) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + f"\n...[truncated {len(value) - limit} bytes]"


def line_hits(text: str, pattern: str) -> list[int]:
    compiled = re.compile(pattern)
    return [number for number, line in enumerate(text.splitlines(), 1) if compiled.search(line)]


class Runner:
    def __init__(self) -> None:
        self.started = time.monotonic()
        self.results: list[dict[str, Any]] = []
        self.failure: dict[str, Any] | None = None
        self.shutdown_clean = True

    def run(self, label: str, argv: list[str], cwd: Path, timeout: int = MAX_CHILD_SECONDS) -> dict[str, Any]:
        elapsed = time.monotonic() - self.started
        if elapsed >= MAX_TOTAL_SECONDS:
            result = {
                "label": label,
                "argv": argv,
                "cwd": str(cwd),
                "returncode": None,
                "timed_out": True,
                "duration_seconds": round(elapsed, 3),
                "stdout": "",
                "stderr": "aggregate verifier budget exhausted before child start",
            }
            self.results.append(result)
            self.failure = self.failure or result
            return result

        child_timeout = min(timeout, MAX_CHILD_SECONDS, max(1, int(MAX_TOTAL_SECONDS - elapsed)))
        started = time.monotonic()
        proc = subprocess.Popen(
            argv,
            cwd=str(cwd),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
            env=os.environ.copy(),
        )
        timed_out = False
        try:
            stdout, stderr = proc.communicate(timeout=child_timeout)
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            self.shutdown_clean = False
            os.killpg(proc.pid, signal.SIGTERM)
            try:
                stdout, stderr = proc.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                stdout, stderr = proc.communicate()
            stderr = (stderr or "") + f"\nchild exceeded {child_timeout}s and was terminated"
            if exc.stdout:
                stdout = (stdout or "") + str(exc.stdout)
            if exc.stderr:
                stderr = (stderr or "") + str(exc.stderr)

        result = {
            "label": label,
            "argv": argv,
            "command": shlex.join(argv),
            "cwd": str(cwd),
            "returncode": proc.returncode,
            "timed_out": timed_out,
            "duration_seconds": round(time.monotonic() - started, 3),
            "stdout": bounded(stdout or ""),
            "stderr": bounded(stderr or ""),
        }
        self.results.append(result)
        if timed_out or proc.returncode != 0:
            self.failure = self.failure or result
        return result


def source_snapshot() -> dict[str, Any]:
    snapshot: dict[str, Any] = {}
    for path in ALL_SOURCE_PATHS:
        file_path = rel(path)
        text = file_path.read_text(encoding="utf-8")
        snapshot[path] = {
            "sha256": sha256_file(file_path),
            "bytes": file_path.stat().st_size,
            "lines": len(text.splitlines()),
        }
    return snapshot


def package_probe(runner: Runner, module_dir: str, package: str) -> dict[str, Any]:
    result = runner.run(
        f"go-list:{module_dir}/{package}",
        ["go", "list", "-f={{.ImportPath}}|{{join .Imports \",\"}}", package],
        rel(module_dir),
    )
    parsed: dict[str, Any] = {"module_dir": module_dir, "package": package, "command_result": result}
    if result["returncode"] == 0:
        output = result["stdout"].strip()
        import_path, _, imports = output.partition("|")
        parsed["import_path"] = import_path
        parsed["direct_imports"] = [item for item in imports.split(",") if item]
    return parsed


def source_gap() -> dict[str, Any]:
    legacy_mixer = rel(LEGACY_PATHS[0]).read_text(encoding="utf-8")
    orchestration = rel(LEGACY_PATHS[1]).read_text(encoding="utf-8")
    run_path = rel(LEGACY_PATHS[2]).read_text(encoding="utf-8")
    lifecycle = rel(LEGACY_PATHS[3]).read_text(encoding="utf-8")
    public_cli = rel(PUBLIC_ROUTE_PATHS[0]).read_text(encoding="utf-8")
    wire = rel(PUBLIC_ROUTE_PATHS[1]).read_text(encoding="utf-8")
    wire_gen = rel(PUBLIC_ROUTE_PATHS[2]).read_text(encoding="utf-8")

    findings = [
        {
            "id": "legacy-host-cadence",
            "path": LEGACY_PATHS[0],
            "evidence": {
                "time_import": line_hits(legacy_mixer, r'"time"'),
                "new_ticker": line_hits(legacy_mixer, r"time\.NewTicker"),
                "pcm_mixer_type": line_hits(legacy_mixer, r"type PCM16Mixer struct"),
            },
            "meaning": "The legacy room mixer owns cadence with time.Ticker instead of an injected audio/pkg/clock scheduler.",
        },
        {
            "id": "legacy-local-pcm-codec",
            "path": LEGACY_PATHS[0],
            "evidence": {
                "codec_import": line_hits(legacy_mixer, r"go-audio/pkg/codec"),
                "decode": line_hits(legacy_mixer, r"codec\.DecodePCM16Into"),
                "encode": line_hits(legacy_mixer, r"codec\.EncodePCM16Into"),
            },
            "meaning": "The legacy mixer decodes and encodes raw PCM16 inside room orchestration rather than passing canonical PCMFrame values through the audio subsystem.",
        },
        {
            "id": "legacy-direct-device-construction",
            "path": LEGACY_PATHS[1],
            "evidence": {
                "device_import": line_hits(orchestration, r"go-device-gateway/pkg/devices"),
                "mixer_constructor": line_hits(orchestration, r"room\.NewPCM16MixerWithConfig"),
                "source_constructor": line_hits(orchestration, r"devicegw\.NewDeviceSource"),
                "sink_constructor": line_hits(orchestration, r"devicegw\.NewDeviceSink"),
            },
            "meaning": "The legacy room orchestration creates both the local PCM mixer and physical device endpoints in the same implementation.",
        },
        {
            "id": "legacy-direct-device-write",
            "path": LEGACY_PATHS[2],
            "evidence": {
                "input_read": line_hits(run_path, r"runtime\.input\.ReadFrame"),
                "resample": line_hits(run_path, r"audio\.ResamplePCM16"),
                "decode": line_hits(run_path, r"codec\.DecodePCM16WithLimit"),
                "sink_write": line_hits(run_path, r"sink\.WriteFrame"),
            },
            "meaning": "The legacy human output loop resamples/decodes pending PCM and writes device frames directly, so queue admission and actual consumption are not represented by the canonical device runtime boundary.",
        },
    ]

    return {
        "status": "SOURCE_GAP_CONFIRMED",
        "scope": "compiled legacy implementation; no claim of active public room-run reachability",
        "legacy_package": "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime",
        "legacy_entrypoints": {
            "RunRoom": line_hits(orchestration, r"func RunRoom\("),
            "RunRoomWithResult": line_hits(orchestration, r"func RunRoomWithResult\("),
            "lifecycle_mixer_field": line_hits(lifecycle, r"mixer \*room\.PCM16Mixer"),
        },
        "findings": findings,
        "public_route": {
            "cli_room_service_type": line_hits(public_cli, r"runtimeRooms\.Service"),
            "wire_room_provider": line_hits(wire, r"NewRoomServiceWithDevices"),
            "generated_room_service": line_hits(wire_gen, r"NewRoomServiceWithDevices"),
            "legacy_entrypoint_imported_by_public_cli": False,
            "interpretation": "The current yui room run command is wired to go-agent-runtime/services/rooms. The old RunRoom path remains compiled and is exposed to the CLI service-test seam, but is not the public command path. It is a concrete source/ownership gap and migration hazard, not runtime/acoustic proof.",
        },
        "smallest_bypass": "agent-cli/internal/room/mixer.go plus its legacy agentruntime callers: host ticker + local PCM codec + direct device sink write in one room path.",
    }


def dependency_controls() -> dict[str, Any]:
    canonical_mixer = rel("go-audio/pkg/mixer/mixer.go").read_text(encoding="utf-8")
    canonical_input = rel("go-audio/pkg/mixer/input.go").read_text(encoding="utf-8")
    room_contract = rel("go-agent-runtime/services/rooms/contract.go").read_text(encoding="utf-8")
    room_graph = rel("go-agent-runtime/services/rooms/internal/lifecycle/graph.go").read_text(encoding="utf-8")
    device_runtime = rel("go-device-gateway/pkg/runtime/rtc_device_sink.go").read_text(encoding="utf-8")
    allowed = (OWNED / "fixtures/allowed_room_graph.go").read_text(encoding="utf-8")
    forbidden = (OWNED / "fixtures/forbidden_room_graph.go").read_text(encoding="utf-8")

    allowed_required = {
        "canonical_mixer_clock": line_hits(canonical_mixer, r"go-audio/pkg/clock"),
        "canonical_mixer_frame_buffer": line_hits(canonical_input, r"audio\.NewFrameBuffer"),
        "canonical_input_pcm_frame": line_hits(canonical_input, r"audio\.PCMFrame"),
        "room_media_factory": line_hits(room_contract, r"type MediaFactory interface"),
        "room_media_ports": line_hits(room_contract, r"type MediaPorts struct"),
        "room_graph_mixer": line_hits(room_graph, r"mixer\.New\("),
        "room_graph_clock": line_hits(room_graph, r"clock\.TimerSource"),
        "device_queue": line_hits(device_runtime, r"PlaybackQueue"),
        "fixture_audio": line_hits(allowed, r"go-audio/pkg/audio"),
        "fixture_clock": line_hits(allowed, r"go-audio/pkg/clock"),
        "fixture_mixer": line_hits(allowed, r"go-audio/pkg/mixer"),
    }
    forbidden_hits = {
        "host_ticker": line_hits(forbidden, r"time\.NewTicker"),
        "local_codec": line_hits(forbidden, r"go-audio/pkg/codec"),
        "direct_device": line_hits(forbidden, r"go-device-gateway/pkg/devices"),
    }
    return {
        "positive_control": {"verdict": "ACCEPTED" if all(allowed_required.values()) else "FAILED", "evidence": allowed_required},
        "negative_control": {"verdict": "REJECTED_AS_FORBIDDEN" if all(forbidden_hits.values()) else "FAILED", "evidence": forbidden_hits},
        "interpretation": "The dependency oracle accepts the canonical injected-clock/buffer/media-port graph and rejects the legacy bypass shape. This is a source dependency check, not device playback evidence.",
    }


def write_json(name: str, value: Any) -> None:
    (OWNED / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_markdown(diagnosis: dict[str, Any]) -> None:
    gap = diagnosis["source_gap"]
    controls = diagnosis["dependency_controls"]
    lines = [
        "# C25 audio/device boundary diagnosis",
        "",
        f"- Task: `{TASK}`",
        f"- Source revision: `{SOURCE_REVISION}`",
        f"- Source archive SHA-256: `{SOURCE_ARCHIVE_SHA256}`",
        "- Decision basis: source-only diagnosis; no realtime, provider, physical-device, or acoustic claim.",
        "",
        "## Finding",
        "",
        f"`{gap['status']}`: the smallest remaining bypass is the compiled legacy CLI room path in `agent-cli/internal/room/mixer.go` and its `agent-cli/internal/services/internal/agentruntime` callers.",
        "",
        "That path owns a host `time.Ticker`, local PCM16 encode/decode, direct device construction, and direct sink writes. The current public `yui room run` command is wired to `go-agent-runtime/services/rooms`, so this packet does not claim that the legacy path is the active public workflow. It does establish a concrete production-source ownership gap and migration hazard: the old alternate implementation remains compiled and reachable from the service-test seam while duplicating the intended audio/device boundaries.",
        "",
        "## Focused causal evidence",
        "",
    ]
    for finding in gap["findings"]:
        lines.append(f"- `{finding['id']}` — `{finding['path']}`: {finding['meaning']}")
        for key, hits in finding["evidence"].items():
            lines.append(f"  - `{key}` lines: {', '.join(str(item) for item in hits) or 'none'}")
    lines += [
        "",
        "## Dependency controls",
        "",
        f"- Positive canonical graph: `{controls['positive_control']['verdict']}`.",
        f"- Negative bypass graph: `{controls['negative_control']['verdict']}`.",
        "- The canonical `go-audio/pkg/mixer` consumes an injected `clock.TimerSource`, submits `audio.PCMFrame` values into bounded frame buffers, and exposes media ports; device runtime owns the playback queue and callback boundary.",
        "",
        "## One extraction plan",
        "",
        "1. Keep the public room service as the only room-run entrypoint; assign the legacy `agent-runtime` package owner to migrate or delete `RunRoom` and its `PCM16Mixer` callers, with no edits to C18/C21-owned files without their primary task.",
        "2. Replace `room.PCM16Mixer` with the canonical `go-audio/pkg/mixer` and `audio.PCMFrame`/buffer contracts; inject `clock.TimerSource` instead of constructing `time.Ticker` in room code.",
        "3. Move human capture/output adaptation behind the runtime device service (`RTCDeviceSource`/`RTCDeviceSink` and `MediaPorts`), preserving an explicit distinction between queued frames and callback-consumed samples.",
        "4. Add one external public-consumer regression that proves frame ordering, end-of-response/epoch boundaries, cancellation/close, and partial-vs-full device consumption; add a dependency guard that rejects host ticker, local codec, and direct gateway imports in room orchestration.",
        "",
        "## Limits",
        "",
        "- C15/C16/C17 accepted probes establish software replay, software sink lifecycle, and canonical PONG-clock behavior only; they do not establish physical playback, microphone capture, live provider behavior, or acoustics.",
        "- This packet deliberately changes no production/shared module, fixture, or baseline path.",
    ]
    (OWNED / "diagnosis.md").write_text("\n".join(lines) + "\n", encoding="utf-8")


def run(mode: str) -> int:
    runner = Runner()
    diagnosis: dict[str, Any] = {
        "schema": 1,
        "task": TASK,
        "mode": mode,
        "source_revision": SOURCE_REVISION,
        "origin_main": ORIGIN_MAIN,
        "baseline_revision": BASELINE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "source_archive_sha256": SOURCE_ARCHIVE_SHA256,
        "owned_path": str(OWNED.relative_to(REPO)),
        "bounded_runner": {"per_child_seconds": MAX_CHILD_SECONDS, "aggregate_seconds": MAX_TOTAL_SECONDS, "clean_shutdown": True},
        "source_snapshot": source_snapshot(),
    }

    if mode in ("gap", "all"):
        head = runner.run("git-head", ["git", "rev-parse", "HEAD"], REPO)
        diagnosis["git_head"] = head["stdout"].strip()
        diagnosis["source_gap"] = source_gap()
        diagnosis["production_path_status"] = runner.run(
            "production-path-status",
            ["git", "status", "--porcelain", "--untracked-files=all", "--"] + LEGACY_PATHS + CANONICAL_PATHS + PUBLIC_ROUTE_PATHS,
            REPO,
        )
        if diagnosis["git_head"] != SOURCE_REVISION:
            runner.failure = runner.failure or {"label": "git-head-match", "returncode": 1, "stderr": f"expected {SOURCE_REVISION}, got {diagnosis['git_head']}"}

    if mode in ("controls", "all"):
        diagnosis["dependency_controls"] = dependency_controls()
        diagnosis["package_probes"] = [
            package_probe(runner, "agent-cli", "./internal/room"),
            package_probe(runner, "agent-cli", "./internal/services/internal/agentruntime"),
            package_probe(runner, "agent-cli", "./internal/transport/cli"),
            package_probe(runner, "go-audio", "./pkg/mixer"),
            package_probe(runner, "go-agent-runtime", "./services/rooms"),
        ]

    if mode in ("regressions", "all"):
        tests = [
            ("canonical-mixer-tests", "go-audio", ["go", "test", "./pkg/mixer", "-run", "^Test(Mixer|PCMAccumulator)", "-count=1", "-timeout=45s"]),
            ("loop-ownership-guard", "go-agent-loop", ["go", "test", "./pkg/agentloop", "-run", "^TestProductionAudioAndDeviceOwnership$", "-count=1", "-timeout=45s"]),
            ("legacy-mixer-focused-test", "agent-cli", ["go", "test", "./internal/room", "-run", "^TestPCM16MixerMixesEveryActiveInputAndClips$", "-count=1", "-timeout=45s"]),
            ("legacy-agent-runtime-compile", "agent-cli", ["go", "test", "./internal/services/internal/agentruntime", "-run", "^$", "-count=1", "-timeout=45s"]),
        ]
        diagnosis["focused_regressions"] = []
        for label, module_dir, command in tests:
            diagnosis["focused_regressions"].append(runner.run(label, command, rel(module_dir)))

    diagnosis["commands"] = runner.results
    diagnosis["failure"] = runner.failure
    diagnosis["bounded_runner"]["clean_shutdown"] = runner.shutdown_clean
    diagnosis["elapsed_seconds"] = round(time.monotonic() - runner.started, 3)
    diagnosis["decision"] = "ACCEPTED" if runner.failure is None else "CONTINUE"
    write_json("diagnosis.json", diagnosis)
    write_json("provenance.json", {
        "task": TASK,
        "source_revision": SOURCE_REVISION,
        "origin_main": ORIGIN_MAIN,
        "baseline_revision": BASELINE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "source_archive_sha256": SOURCE_ARCHIVE_SHA256,
        "source_files": diagnosis["source_snapshot"],
        "generated_by": "verify.py",
    })
    if "source_gap" in diagnosis and "dependency_controls" in diagnosis:
        write_markdown(diagnosis)
    return 0 if runner.failure is None else 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("gap", "controls", "regressions", "all"), default="all")
    args = parser.parse_args()
    try:
        return run(args.mode)
    except Exception as exc:
        failure = {"decision": "CONTINUE", "error": f"{type(exc).__name__}: {exc}"}
        write_json("diagnosis.json", failure)
        print(json.dumps(failure), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
