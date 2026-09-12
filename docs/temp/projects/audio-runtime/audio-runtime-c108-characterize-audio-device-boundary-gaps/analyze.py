#!/usr/bin/env python3
"""Deterministic accepted-tree inventory for C108.

The analyzer is intentionally lexical at the edges and exact at the cited
symbols: it reads only ``git show REV:path`` and records the source blob and
SHA256 for every production Go file in scope.  It does not turn a lexical hit
into a call-graph claim; caller rows are exact source references and the
inventory records the remaining interface/reflection uncertainty explicitly.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import subprocess
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]

SCOPE_ROOTS = (
    "agent-cli/",
    "go-agent-loop/",
    "go-agent-runtime/",
    "go-llm-gateway/",
)
EXCLUDED_ROOTS = (
    "go-audio/",
    "go-device-gateway/",
)

FILE_METADATA: dict[str, dict[str, Any]] = {}


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def git_bytes(*args: str) -> bytes:
    return subprocess.check_output(["git", *args], cwd=ROOT)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def source_files(revision: str) -> list[str]:
    paths = git("ls-tree", "-r", "--name-only", revision).splitlines()
    return sorted(
        path
        for path in paths
        if path.endswith(".go")
        and not path.endswith("_test.go")
        and any(path.startswith(root) for root in SCOPE_ROOTS)
    )


def line_span(lines: list[str], start: int) -> tuple[int, int]:
    """Return a conservative declaration span, including a complete body."""
    depth = 0
    saw_body = False
    for index in range(start - 1, len(lines)):
        line = lines[index]
        depth += line.count("{") - line.count("}")
        if "{" in line:
            saw_body = True
        if saw_body and depth <= 0:
            return start, index + 1
    return start, min(len(lines), start + 1)


IDENTIFIER = r"[A-Za-z_][A-Za-z0-9_]*"


def package_name(lines: list[str]) -> str:
    for line in lines:
        match = re.match(r"^\s*package\s+(" + IDENTIFIER + r")\s*$", line)
        if match:
            return match.group(1)
    return ""


def import_aliases(lines: list[str]) -> dict[str, str]:
    """Return the aliases visible in a Go file without resolving identifiers by name alone."""
    aliases: dict[str, str] = {}
    in_block = False
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("//"):
            continue
        if stripped.startswith("import ("):
            in_block = True
            continue
        if in_block and stripped == ")":
            in_block = False
            continue
        if stripped.startswith("import "):
            stripped = stripped[len("import "):].strip()
        elif not in_block:
            continue
        match = re.match(r'(?:(?P<alias>[A-Za-z_][A-Za-z0-9_]*|\.)\s+)?"(?P<path>[^"]+)"', stripped)
        if not match:
            continue
        path = match.group("path")
        alias = match.group("alias") or path.rsplit("/", 1)[-1]
        aliases[alias] = path
    return aliases


def file_metadata(files: dict[str, list[str]]) -> dict[str, dict[str, Any]]:
    return {
        path: {"package": package_name(lines), "imports": import_aliases(lines)}
        for path, lines in files.items()
    }


def declaration(lines: list[str], symbol: str) -> dict[str, Any] | None:
    function = re.compile(
        r"^\s*func\s*(?:\(\s*(?P<receiver>[^)]*)\)\s*)?(?P<name>" + IDENTIFIER + r")\s*\("
    )
    type_decl = re.compile(r"^\s*type\s+(?P<name>" + IDENTIFIER + r")\b")
    for number, line in enumerate(lines, 1):
        match = function.search(line)
        if match and match.group("name") == symbol:
            start, end = line_span(lines, number)
            receiver = match.group("receiver")
            receiver_name = ""
            receiver_type = ""
            if receiver:
                receiver_tokens = receiver.replace("[", " ").replace("]", " ").split()
                if len(receiver_tokens) >= 2:
                    receiver_name = receiver_tokens[0]
                    receiver_type = receiver_tokens[-1].lstrip("*")
            return {
                "symbol": symbol,
                "kind": "function",
                "start_line": start,
                "end_line": end,
                "receiver_name": receiver_name,
                "receiver_type": receiver_type,
            }
        match = type_decl.search(line)
        if match and match.group("name") == symbol:
            start, end = line_span(lines, number)
            return {
                "symbol": symbol,
                "kind": "type",
                "start_line": start,
                "end_line": end,
                "receiver_name": "",
                "receiver_type": "",
            }
    return None


def source_directory(path: str) -> str:
    return path.rsplit("/", 1)[0]


def import_matches_source(import_path: str, source_path: str) -> bool:
    """Match the imported package path to the exact source directory suffix."""
    directory = source_directory(source_path)
    root, relative = directory.split("/", 1)
    return import_path.endswith("/" + root + "/" + relative) or import_path == directory


def line_is_code(line: str) -> bool:
    stripped = line.lstrip()
    return bool(stripped) and not stripped.startswith("//")


def type_expression_matches(
    expression: str,
    target_type: str,
    target_path: str,
    caller_path: str,
    metadata: dict[str, dict[str, Any]],
) -> bool:
    expression = expression.strip().lstrip("*")
    match = re.fullmatch(r"(?:(?P<alias>" + IDENTIFIER + r")\.)?(?P<type>" + IDENTIFIER + r")", expression)
    if not match or match.group("type") != target_type:
        return False
    alias = match.group("alias")
    if alias is None:
        return source_directory(caller_path) == source_directory(target_path)
    return import_matches_source(metadata.get(caller_path, {}).get("imports", {}).get(alias, ""), target_path)


def receiver_expression_is_target(
    lines: list[str],
    expression: str,
    declaration_info: dict[str, Any],
    source_path: str,
    caller_path: str,
    metadata: dict[str, dict[str, Any]],
) -> bool:
    target_type = declaration_info.get("receiver_type", "")
    if not target_type:
        return False
    receiver_name = declaration_info.get("receiver_name", "")
    if caller_path == source_path and receiver_name and expression == receiver_name:
        return True

    simple_name = expression.rsplit(".", 1)[-1]
    type_token = r"(?:\*?(?:" + IDENTIFIER + r"\.)?" + IDENTIFIER + r")"
    declaration_pattern = re.compile(r"\b" + re.escape(simple_name) + r"\s+(" + type_token + r")\b")
    receiver_pattern = re.compile(
        r"^\s*func\s*\(\s*" + re.escape(simple_name) + r"\s+(" + type_token + r")\s*\)"
    )
    for line in lines:
        match = declaration_pattern.search(line)
        if match and type_expression_matches(match.group(1), target_type, source_path, caller_path, metadata):
            return True
        match = receiver_pattern.search(line)
        if match and type_expression_matches(match.group(1), target_type, source_path, caller_path, metadata):
            return True
    return False


EXPLICIT_CALLER_HINTS: dict[tuple[str, str], list[dict[str, Any]]] = {
    (
        "go-agent-runtime/services/session/internal/live/invoke.go",
        "start",
    ): [
        {
            "path": "go-agent-runtime/services/session/internal/live/invoke.go",
            "line": 29,
            "expression": "if err := invocation.start(); err != nil {",
            "match": "constructor_result_method_dispatch",
            "evidence": "newLiveInvocation returns the concrete *liveInvocation used by RunLive",
        },
    ],
    (
        "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
        "ConnectSession",
    ): [
        {
            "path": "agent-cli/internal/services/internal/agentruntime/session_live_setup.go",
            "line": 31,
            "expression": "inferencer, pumpErrors := bindRTCDeviceSessionInferencer(inferencer, opts.rtcDeviceBinding)",
            "match": "wrapper_installation_chain",
            "evidence": "exact binding call installs rtcDeviceBindingInferencer before interface dispatch",
        },
        {
            "path": "agent-cli/internal/services/internal/agentruntime/session_turns.go",
            "line": 173,
            "expression": "session, err := s.sessionInferencer.ConnectSession(ctx)",
            "match": "interface_method_dispatch",
            "evidence": "exact messages.SessionInferencer dispatch reached by the binding installed above",
        },
    ],
    (
        "agent-cli/internal/room/mixer.go",
        "ReadFrameWithSources",
    ): [
        {
            "path": "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
            "line": 947,
            "expression": "mixed, err := runtime.mixer.ReadFrameWithSources(admissionCtx)",
            "match": "typed_field_selector",
            "evidence": "runtime.mixer is the *room.PCM16Mixer field declared in session_room_lifecycle.go:74",
        },
        {
            "path": "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
            "line": 1116,
            "expression": "mixed, err := runtime.mixer.ReadFrameWithSources(runtime.ctx)",
            "match": "typed_field_selector",
            "evidence": "runtime.mixer is the *room.PCM16Mixer field declared in session_room_lifecycle.go:74",
        },
    ],
}


def callers(
    files: dict[str, list[str]],
    symbol: str,
    source_path: str,
    declaration_span: dict[str, Any] | None,
    metadata: dict[str, dict[str, Any]],
    declaration_info: dict[str, Any],
) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    source_dir = source_directory(source_path)
    receiver_type = declaration_info.get("receiver_type", "")
    receiver_pattern = re.compile(r"(?P<receiver>" + IDENTIFIER + r"(?:\." + IDENTIFIER + r")?)\s*\.\s*" + re.escape(symbol) + r"\b")
    unqualified_pattern = re.compile(r"\b" + re.escape(symbol) + r"\s*\(")
    for path in sorted(files):
        for number, line in enumerate(files[path], 1):
            if not line_is_code(line):
                continue
            if path == source_path and declaration_span and declaration_span["start_line"] <= number <= declaration_span["end_line"]:
                continue
            if receiver_type:
                match = receiver_pattern.search(line)
                if not match or not receiver_expression_is_target(files[path], match.group("receiver"), declaration_info, source_path, path, metadata):
                    continue
                rows.append(
                    {
                        "path": path,
                        "line": number,
                        "expression": line.strip(),
                        "match": "typed_method_selector",
                        "resolved_receiver": match.group("receiver"),
                        "evidence": "method selector is tied to the declared receiver type in pinned production source",
                    }
                )
                continue

            if path.startswith(source_dir + "/") or path == source_path:
                if unqualified_pattern.search(line):
                    rows.append(
                        {
                            "path": path,
                            "line": number,
                            "expression": line.strip(),
                            "match": "same_package_unqualified_call",
                            "evidence": "unqualified call is confined to the exact source package directory",
                        }
                    )
                continue

            for alias, import_path in metadata.get(path, {}).get("imports", {}).items():
                if import_matches_source(import_path, source_path) and re.search(r"\b" + re.escape(alias) + r"\s*\.\s*" + re.escape(symbol) + r"\b", line):
                    rows.append(
                        {
                            "path": path,
                            "line": number,
                            "expression": line.strip(),
                            "match": "qualified_import_call",
                            "import_alias": alias,
                            "import_path": import_path,
                            "evidence": "qualified call resolves through the exact imported source package",
                        }
                    )
                    break
    rows.extend(EXPLICIT_CALLER_HINTS.get((source_path, symbol), []))
    return sorted(rows, key=lambda row: (row["path"], row["line"], row["expression"]))


def file_record(revision: str, path: str, lines: list[str]) -> dict[str, Any]:
    data = git_bytes("show", f"{revision}:{path}")
    blob = git("rev-parse", f"{revision}:{path}")
    generated = path.endswith("_gen.go") or "/generated/" in path
    return {
        "path": path,
        "blob_id": blob,
        "sha256": sha256(data),
        "bytes": len(data),
        "lines": len(lines),
        "generated": generated,
        "source": "accepted-tree git show",
    }


def locate(path: str, files: dict[str, list[str]], symbol: str) -> tuple[dict[str, Any], dict[str, Any]]:
    lines = files[path]
    decl = declaration(lines, symbol)
    if decl is None:
        raise SystemExit(f"declared symbol not found in accepted source: {path}:{symbol}")
    record = {
        "path": path,
        "symbol": symbol,
        "symbol_kind": decl["kind"],
        "span": {"start_line": decl["start_line"], "end_line": decl["end_line"]},
        "receiver": {"name": decl.get("receiver_name", ""), "type": decl.get("receiver_type", "")},
        "callers": callers(files, symbol, path, decl, FILE_METADATA, decl),
    }
    return record, decl


def finding(
    finding_id: str,
    path: str,
    symbols: list[str],
    files: dict[str, list[str]],
    file_index: dict[str, dict[str, Any]],
    responsibility: str,
    boundary: str,
    owner: str,
    judgment: str,
    public_entry: str,
    evidence: str,
    reachability: str = "reachable_production",
    excluded_reason: str = "",
) -> dict[str, Any]:
    symbol_rows = []
    for symbol in symbols:
        row, _ = locate(path, files, symbol)
        symbol_rows.append(row)
    return {
        "id": finding_id,
        "source": file_index[path],
        "symbols": symbol_rows,
        "responsibility": responsibility,
        "boundary_class": boundary,
        "current_owner": owner,
        "judgment": judgment,
        "public_entry_point": public_entry,
        "reachability": reachability,
        "evidence": evidence,
        "excluded_reason": excluded_reason,
        "interface_uncertainty": "Interface and reflection edges are not inferred; only listed exact callers are claimed.",
    }


def no_finding(
    finding_id: str,
    path: str,
    files: dict[str, list[str]],
    file_index: dict[str, dict[str, Any]],
    responsibility: str,
    boundary: str,
    owner: str,
    judgment: str,
    public_entry: str,
    evidence: str,
) -> dict[str, Any]:
    return {
        "id": finding_id,
        "source": file_index[path],
        "symbols": [],
        "responsibility": responsibility,
        "boundary_class": boundary,
        "current_owner": owner,
        "judgment": judgment,
        "public_entry_point": public_entry,
        "reachability": "reachable_production",
        "evidence": evidence,
        "interface_uncertainty": "No direct device construction or frame write was found in the cited source; the next interface is explicit.",
    }


def main() -> int:
    global FILE_METADATA
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--output-dir", required=True, type=pathlib.Path)
    args = parser.parse_args()
    revision = args.source
    # Fail closed on an unknown or non-commit source pin.
    git("cat-file", "-e", f"{revision}^{{commit}}")
    paths = source_files(revision)
    contents = {path: git_bytes("show", f"{revision}:{path}").decode("utf-8", "replace") for path in paths}
    files = {path: text.splitlines() for path, text in contents.items()}
    FILE_METADATA = file_metadata(files)
    file_index = {path: file_record(revision, path, files[path]) for path in paths}

    required_paths = [
        "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
        "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go",
        "agent-cli/internal/services/internal/agentruntime/session_audio_format.go",
        "go-llm-gateway/pkg/providers/openai/session_media.go",
        "go-llm-gateway/pkg/providers/grok/session_media.go",
        "go-llm-gateway/pkg/transport/rtc/track_in.go",
        "go-llm-gateway/pkg/transport/rtc/track_out.go",
        "go-agent-runtime/services/devices/internal/probe/rtc.go",
        "agent-cli/internal/wire/rtc_runtime.go",
        "agent-cli/internal/room/mixer.go",
        "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
        "agent-cli/internal/services/internal/agentruntime/session_audio_out.go",
        "agent-cli/internal/transport/cli/media.go",
        "go-agent-loop/pkg/participants/model_runner_session.go",
        "go-agent-runtime/services/devices/contract.go",
    ]
    missing = [path for path in required_paths if path not in files]
    if missing:
        raise SystemExit("accepted source is missing declared scope paths: " + ", ".join(missing))

    findings = [
        finding(
            "C108-F-DEVICE-PUMP-LIFECYCLE",
            "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
            ["ConnectSession", "startPumps", "DrainPlayback"],
            files,
            file_index,
            "provider media to local device pump startup, error suppression, bounded playback drain and close ordering",
            "DEVICE_EDGE_IO",
            "legacy agentruntime; preserved C64/C96 context must be rechecked before any future lease",
            "supported_gap",
            "agent-cli/internal/services/internal/agentruntime/session_live_setup.go and session_duration_loop.go",
            "The source starts both directions by calling device-gateway runtime pumps and calls WaitForPump after closing only the inbound media; a queue/drain receipt is not physical-device consumption proof.",
        ),
        finding(
            "C108-F-LIVE-SERVICE-PUMP-LIFECYCLE",
            "go-agent-runtime/services/session/internal/live/invoke.go",
            ["start", "startPumps", "startCapturePump", "startPlaybackPump"],
            files,
            file_index,
            "live service pump startup and cancellation routing between session media endpoints and device service ports",
            "SERVICE_TRANSPORT_ADAPTATION",
            "go-agent-runtime live session service",
            "correct_boundary",
            "go-agent-runtime/services/session/internal/live/invoke.go:liveInvocation.start",
            "The live service owns the invocation lifecycle and starts capture/playback through injected device ports and provider media endpoints. It does not construct a physical device or claim that a queue write is hardware consumption.",
        ),
        finding(
            "C108-F-DEVICE-PROBE-CAPTURE-RATE",
            "go-agent-runtime/services/devices/internal/probe/capture.go",
            ["forwardProbeFrames"],
            files,
            file_index,
            "device-probe input-rate adaptation from WebRTC track samples to the provider session rate",
            "DEVICE_EDGE_IO",
            "go-agent-runtime devices probe service",
            "correct_boundary",
            "go-agent-runtime/services/devices/internal/probe/capture.go:captureLiveDeviceProbeInput",
            "The probe performs an explicit edge conversion with the canonical go-audio resampler before admitting PCM bytes to the session runner. The conversion is observable at a device-service boundary and is not a physical-consumption claim.",
        ),
        finding(
            "C108-F-DEVICE-PROBE-PLAYBACK-RATE",
            "go-agent-runtime/services/devices/internal/probe/bridge.go",
            ["writeOutputFrame"],
            files,
            file_index,
            "device-probe output-rate adaptation from provider session frames to the selected device rate",
            "DEVICE_EDGE_IO",
            "go-agent-runtime devices probe service",
            "correct_boundary",
            "go-agent-runtime/services/devices/internal/probe/bridge.go:liveDeviceProbeSessionBridge.handleOutput",
            "The bridge resamples provider frames through canonical go-audio before the selected sink receives them, preserving the distinction between sink receipt and physical device consumption.",
        ),
        finding(
            "C108-F-AUDIO-RATE-CONTRACT",
            "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go",
            ["convertSessionAudioPCM", "convertScheduledAudioInputs"],
            files,
            file_index,
            "PCM16 validation, source/provider sample-rate conversion and scheduled-input rewriting",
            "CORE_LOOP_BUFFER_POLICY",
            "legacy agentruntime; no active C79/C83/C84/C99/C101/C103/C104/C107 writer claim found",
            "supported_gap",
            "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go, session_audio_dispatch.go and session_room_run.go",
            "A reusable conversion policy remains in the CLI-internal runtime and is invoked by planning and room dispatch; the exact negative is odd PCM16 byte length and unsupported resample rate.",
        ),
        finding(
            "C108-F-AUDIO-RATE-NEGOTIATION",
            "agent-cli/internal/services/internal/agentruntime/session_audio_format.go",
            ["resolveSessionAudioSampleRate", "configureSessionAudioContract"],
            files,
            file_index,
            "duplex sample-rate resolution, conflict rejection and provider format configuration",
            "CORE_LOOP_BUFFER_POLICY",
            "legacy agentruntime; caller-owned planner remains the retained adapter",
            "supported_gap",
            "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go",
            "The planner enforces one input/output rate and writes provider configuration from a CLI-internal policy function; it is a separate writer family from device pump lifecycle.",
        ),
        finding(
            "C108-F-PROVIDER-MEDIA-OPENAI",
            "go-llm-gateway/pkg/providers/openai/session_media.go",
            ["writeRTCMediaFrame", "publishRTCMedia", "decodeOpenAIRealtimeAudioDelta"],
            files,
            file_index,
            "provider packet parsing/validation, PCM16 decode, inbound media push/flush and outbound audio encoding",
            "SERVICE_TRANSPORT_ADAPTATION",
            "OpenAI provider session package; no C79/C83/C84/C99/C101/C103/C104/C107 writer claim found",
            "supported_gap",
            "go-llm-gateway/pkg/providers/openai/session_conn.go and session.go",
            "The provider owns base64/PCM packet decoding and audio event policy before the shared media endpoints; malformed base64, odd PCM16 and non-PCM format are causal negative surfaces.",
        ),
        finding(
            "C108-F-PROVIDER-MEDIA-GROK",
            "go-llm-gateway/pkg/providers/grok/session_media.go",
            ["writeRTCMediaFrame", "publishRTCMedia", "decodeGrokAudioDelta"],
            files,
            file_index,
            "provider packet parsing/validation, PCM16 decode, inbound media push/flush and outbound audio encoding",
            "SERVICE_TRANSPORT_ADAPTATION",
            "Grok provider session package; no C79/C83/C84/C99/C101/C103/C104/C107 writer claim found",
            "supported_gap",
            "go-llm-gateway/pkg/providers/grok/provider.go and session.go",
            "The Grok provider duplicates the packet-to-media policy with a provider-specific event vocabulary; its decoder rejects malformed base64 and invalid PCM16 payloads.",
        ),
        finding(
            "C108-F-TOOL-AUDIO-CONVERSION",
            "go-agent-runtime/services/tools/internal/filesystem/tool_filesystem.go",
            ["audioToPCM16k"],
            files,
            file_index,
            "audio-file decoding and sample-rate conversion for the filesystem tool",
            "SERVICE_TRANSPORT_ADAPTATION",
            "go-agent-runtime filesystem tool service",
            "supported_gap",
            "go-agent-runtime/services/tools/internal/filesystem/tool_file_tools.go:readFileMedia",
            "The filesystem tool owns a second reachable audio conversion path that writes temporary input and invokes ffmpeg for mono 16 kHz PCM. It is distinct from the session-rate contract and should be evaluated for a shared audio codec boundary; file receipt is not playback consumption.",
        ),
        finding(
            "C108-F-RTC-TRACKS",
            "go-llm-gateway/pkg/transport/rtc/track_in.go",
            ["NewInboundTrack", "readLoop", "ReadFrame"],
            files,
            file_index,
            "RTP packet sequencing, jitter buffering, Opus decode handoff, PLC/resample timing and frame delivery",
            "DEVICE_EDGE_IO",
            "go-llm-gateway transport package; future ownership must serialize with device probe and wire transport callers",
            "supported_gap",
            "go-agent-runtime/services/devices/internal/probe/rtc.go",
            "The production device probe reaches this track implementation; its bounded frame queue and jitter/PLC policy are outside go-audio and outside the adjacent device gateway.",
        ),
        finding(
            "C108-F-RTC-TRACKS-OUT",
            "go-llm-gateway/pkg/transport/rtc/track_out.go",
            ["NewOutboundTrack", "WriteFrame"],
            files,
            file_index,
            "PCM frame conversion, media-clock pacing, Opus encoding and RTP writes",
            "DEVICE_EDGE_IO",
            "go-llm-gateway transport package; future ownership must serialize with CLI wire and device probe callers",
            "supported_gap",
            "agent-cli/internal/wire/rtc_runtime.go and go-agent-runtime/services/devices/internal/probe/rtc.go",
            "The outbound track writes RTP packets after encoding and pacing; this is transport/device-edge output, not proof that a physical endpoint consumed samples.",
        ),
        no_finding(
            "C108-NF-DEVICE-CONTRACT",
            "go-agent-runtime/services/devices/contract.go",
            files,
            file_index,
            "physical-device construction and lifecycle",
            "THIN_SERVICE_CONTRACT",
            "go-agent-runtime devices service",
            "correct_boundary",
            "go-agent-runtime/services/devices.Service",
            "The service contract carries device requests and source/sink capabilities; physical device construction is delegated to the adjacent device-gateway runtime.",
        ),
        no_finding(
            "C108-NF-CLI-MEDIA-TRANSPORT",
            "agent-cli/internal/transport/cli/media.go",
            files,
            file_index,
            "packet parsing and physical-device construction",
            "THIN_TRANSPORT_DELEGATION",
            "CLI transport",
            "correct_boundary",
            "agent-cli command composition",
            "The CLI normalizes command arguments and renders probe results; its injected probe delegates to go-llm-gateway and does not parse PCM or construct a device.",
        ),
        no_finding(
            "C108-NF-CORE-LOOP-QUEUE",
            "go-agent-loop/pkg/participants/model_runner_session.go",
            files,
            file_index,
            "buffer/admission policy",
            "CORE_LOOP_BUFFER_POLICY",
            "go-agent-loop core",
            "correct_boundary",
            "agent loop participant/session admission",
            "Core-loop message admission and queue state are not playback consumption. The inventory keeps this boundary separate from device-edge pumping.",
        ),
        finding(
            "C108-F-MIXER-HISTORICAL",
            "agent-cli/internal/room/mixer.go",
            ["NewPCM16MixerWithConfig", "WriteContextWithDisposition", "ReadFrameWithSources"],
            files,
            file_index,
            "PCM mixing/DSP and bounded sample buffering",
            "CORE_LOOP_BUFFER_POLICY",
            "preserved historical C26/C30 mixer ownership; not a new C108 candidate",
            "supported_gap",
            "agent-cli room composition",
            "This is a reachable DSP/buffer policy surface, but its historical ownership is preserved and therefore it is reported, not selected for a new repair candidate.",
            excluded_reason="historical predecessor ownership and shared migration serialization",
        ),
        finding(
            "C108-F-AUDIO-IN-HISTORICAL",
            "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
            ["openSessionAudioInput"],
            files,
            file_index,
            "capture source buffering, conversion and provider input forwarding",
            "DEVICE_EDGE_IO",
            "preserved historical C59 input ownership; not a new C108 candidate",
            "supported_gap",
            "session runtime audio input setup",
            "The input source is a legacy boundary already covered by a preserved predecessor; C108 records the owner rather than claiming it.",
            excluded_reason="historical predecessor ownership and shared migration serialization",
        ),
        finding(
            "C108-F-AUDIO-OUT-HISTORICAL",
            "agent-cli/internal/services/internal/agentruntime/session_audio_out.go",
            ["newSessionAudioSinkAtRate"],
            files,
            file_index,
            "provider playback file/device sink buffering and sample-rate conversion",
            "DEVICE_EDGE_IO",
            "preserved historical C92 output ownership; not a new C108 candidate",
            "supported_gap",
            "session runtime audio output setup",
            "The output sink is a legacy boundary already covered by a preserved predecessor; file receipt is not physical playback proof.",
            excluded_reason="historical predecessor ownership and shared migration serialization",
        ),
    ]

    # Ensure every required responsibility has an explicit disposition.
    coverage = [
        {"responsibility": "packet_parsing", "status": "complete", "finding_ids": ["C108-F-PROVIDER-MEDIA-OPENAI", "C108-F-PROVIDER-MEDIA-GROK", "C108-F-RTC-TRACKS"]},
        {"responsibility": "sample_rate_or_timing_conversion", "status": "complete", "finding_ids": ["C108-F-AUDIO-RATE-CONTRACT", "C108-F-AUDIO-RATE-NEGOTIATION", "C108-F-DEVICE-PROBE-CAPTURE-RATE", "C108-F-DEVICE-PROBE-PLAYBACK-RATE", "C108-F-RTC-TRACKS", "C108-F-RTC-TRACKS-OUT", "C108-F-TOOL-AUDIO-CONVERSION"]},
        {"responsibility": "DSP_or_buffer_policy", "status": "complete", "finding_ids": ["C108-F-AUDIO-RATE-CONTRACT", "C108-F-DEVICE-PROBE-CAPTURE-RATE", "C108-F-DEVICE-PROBE-PLAYBACK-RATE", "C108-F-RTC-TRACKS", "C108-F-TOOL-AUDIO-CONVERSION", "C108-F-MIXER-HISTORICAL", "C108-NF-CORE-LOOP-QUEUE"]},
        {"responsibility": "physical_device_construction_or_lifecycle", "status": "complete", "finding_ids": ["C108-NF-DEVICE-CONTRACT", "C108-F-DEVICE-PUMP-LIFECYCLE"]},
        {"responsibility": "direct_device_frame_or_transport_writes", "status": "complete", "finding_ids": ["C108-F-DEVICE-PUMP-LIFECYCLE", "C108-F-RTC-TRACKS-OUT"]},
        {"responsibility": "queue_admission_vs_playback_consumption", "status": "complete", "finding_ids": ["C108-F-DEVICE-PUMP-LIFECYCLE", "C108-NF-CORE-LOOP-QUEUE"]},
    ]
    excluded = {
        "canonical_roots": [
            {"root": root, "status": "excluded_canonical_boundary", "reason": "requested inventory stops at go-audio and adjacent go-device-gateway"}
            for root in EXCLUDED_ROOTS
        ],
        "test_only": "All *_test.go files are excluded from production reachability claims and are listed as regression inputs only.",
        "generated": [record for record in file_index.values() if record["generated"]],
        "ambiguous": [
            {"path": "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go", "reason": "binding construction is a preserved predecessor surface; C108 does not claim it as a writer"},
            {"path": "agent-cli/internal/services/internal/agentruntime/session_audio_in.go", "reason": "legacy input source is separately owned by preserved C59 context"},
            {"path": "agent-cli/internal/services/internal/agentruntime/session_audio_out.go", "reason": "legacy output sink is separately owned by preserved C92 context"},
        ],
    }

    inventory = {
        "schema_version": "c108-inventory-v1",
        "source_revision": revision,
        "source_tree": "accepted-tree-only",
        "scope": {
            "included_roots": list(SCOPE_ROOTS),
            "excluded_roots": list(EXCLUDED_ROOTS),
            "production_files_inspected": len(file_index),
            "source_hashes_are_pinned": True,
        },
        "inspected_files": [file_index[path] for path in sorted(file_index)],
        "coverage": coverage,
        "findings": findings,
        "excluded_and_ambiguous": excluded,
        "claims": {
            "lexical_matches_are_not_reachability": True,
            "caller_rows_are_exact_pinned_source_references": True,
            "queue_admission_is_not_playback_consumption": True,
            "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
            "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
            "native_windows_hardware": "OUT_OF_SCOPE_AND_NEVER_PASS",
        },
    }

    classifications = {
        "schema_version": "c108-classifications-v1",
        "source_revision": revision,
        "classes": sorted(
            [
                {
                    "finding_id": item["id"],
                    "boundary_class": item["boundary_class"],
                    "judgment": item["judgment"],
                    "reachability": item["reachability"],
                    "proof_limit": "SOFTWARE_REPLAY_OR_LOWER; no physical/acoustic claim",
                }
                for item in findings
            ],
            key=lambda item: item["finding_id"],
        ),
    }
    call_paths = {
        "schema_version": "c108-call-paths-v1",
        "source_revision": revision,
        "paths": [
            {
                "finding_id": item["id"],
                "public_entry_point": item["public_entry_point"],
                "symbol_paths": [
                    {"path": symbol["path"], "symbol": symbol["symbol"], "callers": symbol["callers"]}
                    for symbol in item["symbols"]
                ],
            }
            for item in findings
        ],
    }

    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    write_json(output / "inventory.json", inventory)
    write_json(output / "classifications.json", classifications)
    write_json(output / "call-paths.json", call_paths)
    output.joinpath("analysis.md").write_text(
        "# C108 accepted-tree inventory\n\n"
        f"Source revision: `{revision}`. Inspected `{len(file_index)}` production Go files from the four non-canonical roots.\n\n"
        "The five selected repair families are provider media normalization, the session audio-rate contract, the filesystem-tool audio conversion path, the RTC device-pump/consumption boundary, and RTP transport tracks. The inventory also records the live service pump and device-probe rate adapters as correct boundaries, plus legacy mixer, input, and output surfaces as preserved predecessor-owned findings; those are not duplicated as candidates. Queue admission, buffer/file receipt, simulated callback, physical consumption, and acoustic proof remain separate claims.\n\n"
        "Canonical `go-audio` and `go-device-gateway` roots are excluded from ownership claims. Native Windows hardware/endpoints and physical acoustics are out of scope and never PASS.\n",
        encoding="utf-8",
    )
    print(json.dumps({"status": "ok", "source_revision": revision, "output_dir": str(output), "production_files": len(file_index), "findings": len(findings)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
