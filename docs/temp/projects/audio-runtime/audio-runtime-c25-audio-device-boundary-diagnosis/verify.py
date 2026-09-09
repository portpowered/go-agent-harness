#!/usr/bin/env python3
"""Bounded, source-only C25 audio/device-boundary diagnosis.

This verifier never opens a device, starts a provider/realtime session, edits
production paths, or waits for external CI. It records exact source evidence
and runs only focused compile/test commands with bounded shutdown.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
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
SOURCE_REVISION = "a1156f0c0c6271643578cb37894026944df2a633"
ORIGIN_MAIN = SOURCE_REVISION
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
SOURCE_ARCHIVE_SHA256 = "0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f"
MAX_CHILD_SECONDS = 55
MAX_TOTAL_SECONDS = 600

OWNED = Path(__file__).resolve().parent
REPO = OWNED.parents[4]
OUTPUT_DIR = OWNED
CHILD_TIMEOUT_SECONDS = MAX_CHILD_SECONDS
TOTAL_TIMEOUT_SECONDS = MAX_TOTAL_SECONDS

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
    "go-audio/pkg/audio/device_playback.go",
    "go-audio/pkg/audio/device_playback_test.go",
    "go-audio/pkg/clock/clock.go",
    "go-audio/pkg/clock/clock_test.go",
    "go-audio/pkg/mixer/mixer.go",
    "go-audio/pkg/mixer/input.go",
    "go-agent-runtime/services/rooms/contract.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/graph.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/media.go",
    "go-agent-runtime/services/rooms/internal/lifecycle/participant.go",
    "go-device-gateway/pkg/runtime/rtc_device.go",
    "go-device-gateway/pkg/runtime/rtc_device_sink.go",
    "go-agent-loop/pkg/agentloop/architecture_import_test.go",
]
PUBLIC_ROUTE_PATHS = [
    "agent-cli/internal/transport/cli/room.go",
    "agent-cli/internal/services/wire/wire.go",
    "agent-cli/internal/wire/wire_gen.go",
]
SERVICE_TEST_PATH = "agent-cli/internal/services/servicetest/runtime.go"
ALL_SOURCE_PATHS = LEGACY_PATHS + CANONICAL_PATHS + PUBLIC_ROUTE_PATHS + [SERVICE_TEST_PATH]
OWNED_RELATIVE = "docs/temp/projects/audio-runtime/audio-runtime-c25-audio-device-boundary-diagnosis/"
FIXTURE_PATHS = [
    "fixtures/allowed_room_graph.go",
    "fixtures/forbidden_room_graph.go",
]
SOURCE_ARCHIVE_NAME = "source.tar"
FIXTURE_MANIFEST_NAME = "fixtures/manifest.json"
CANONICAL_FIXTURE_IMPORTS = {
    "context",
    "github.com/portpowered/go-agent-harness/go-audio/pkg/audio",
    "github.com/portpowered/go-agent-harness/go-audio/pkg/clock",
    "github.com/portpowered/go-agent-harness/go-audio/pkg/mixer",
}
FORBIDDEN_FIXTURE_IMPORTS = {
    "time",
    "github.com/portpowered/go-agent-harness/go-audio/pkg/codec",
    "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices",
}
CANONICAL_FIXTURE_CALLS = {"mixer.New", "graph.AddInput"}
FORBIDDEN_FIXTURE_CALLS = {
    "time.NewTicker",
    "codec.EncodePCM16",
    "devicegw.NewDeviceSink",
}
AST_ORACLE_SOURCE = r'''package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
)

type report struct {
	Path          string   `json:"path"`
	Imports       []string `json:"imports"`
	SelectorCalls []string `json:"selector_calls"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "at least one Go source path is required")
		os.Exit(2)
	}
	reports := make([]report, 0, len(os.Args)-1)
	for _, path := range os.Args[1:] {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		imports := map[string]bool{}
		calls := map[string]bool{}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.ImportSpec:
				value, err := strconv.Unquote(current.Path.Value)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: invalid import: %v\n", path, err)
					os.Exit(1)
				}
				imports[value] = true
			case *ast.CallExpr:
				selector, ok := current.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if ok {
					calls[identifier.Name+"."+selector.Sel.Name] = true
				}
			}
			return true
		})
		importsList := make([]string, 0, len(imports))
		for value := range imports {
			importsList = append(importsList, value)
		}
		callsList := make([]string, 0, len(calls))
		for value := range calls {
			callsList = append(callsList, value)
		}
		sort.Strings(importsList)
		sort.Strings(callsList)
		reports = append(reports, report{Path: path, Imports: importsList, SelectorCalls: callsList})
	}
	if err := json.NewEncoder(os.Stdout).Encode(reports); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
'''


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


def utc_timestamp() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def process_group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def line_hits(text: str, pattern: str) -> list[int]:
    compiled = re.compile(pattern)
    return [number for number, line in enumerate(text.splitlines(), 1) if compiled.search(line)]


class Runner:
    def __init__(self) -> None:
        self.started = time.monotonic()
        self.results: list[dict[str, Any]] = []
        self.failure: dict[str, Any] | None = None
        self.shutdown_clean = True
        self.output_dir = OUTPUT_DIR
        self.tmp_dir = self.output_dir / "tmp"
        self.go_cache = self.output_dir / "cache" / "go-build"
        self.go_mod_cache = self.output_dir / "cache" / "go-mod"
        for path in (self.tmp_dir, self.go_cache, self.go_mod_cache):
            path.mkdir(parents=True, exist_ok=True)

    def run(
        self,
        label: str,
        argv: list[str],
        cwd: Path,
        timeout: int | None = None,
        *,
        expect_failure: bool = False,
        require_tests: bool = False,
    ) -> dict[str, Any]:
        elapsed = time.monotonic() - self.started
        child_limit = self.child_timeout if timeout is None else min(timeout, self.child_timeout)
        if elapsed >= self.total_timeout:
            result = {
                "label": label,
                "argv": argv,
                "cwd": str(cwd),
                "returncode": None,
                "timed_out": True,
                "duration_seconds": round(elapsed, 3),
                "stdout": "",
                "stderr": "aggregate verifier budget exhausted before child start",
                "started_at": utc_timestamp(),
                "finished_at": utc_timestamp(),
                "harness_verdict": "FAILED",
            }
            self.results.append(result)
            if not expect_failure:
                self.failure = self.failure or result
            return result

        child_timeout = min(child_limit, max(1, int(self.total_timeout - elapsed)))
        started = time.monotonic()
        started_at = utc_timestamp()
        environment = os.environ.copy()
        environment.update({
            "TMPDIR": str(self.tmp_dir),
            "GOCACHE": str(self.go_cache),
            "GOMODCACHE": str(self.go_mod_cache),
        })
        proc = subprocess.Popen(
            argv,
            cwd=str(cwd),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
            env=environment,
        )
        timed_out = False
        try:
            stdout, stderr = proc.communicate(timeout=child_timeout)
        except subprocess.TimeoutExpired as exc:
            timed_out = True
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

        children_alive = process_group_alive(proc.pid)
        if children_alive:
            self.shutdown_clean = False
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = proc.communicate()
            children_alive = process_group_alive(proc.pid)
        native_returncode = proc.returncode
        harness_error = None
        no_test_markers = ("[no tests to run]", "no tests to run", "no tests matched")
        if require_tests and any(marker in (stdout or "").lower() for marker in no_test_markers):
            harness_error = "zero test discovery"
        result = {
            "label": label,
            "argv": argv,
            "command": shlex.join(argv),
            "cwd": str(cwd),
            "returncode": native_returncode,
            "native_returncode": native_returncode,
            "timed_out": timed_out,
            "children_alive": children_alive,
            "duration_seconds": round(time.monotonic() - started, 3),
            "started_at": started_at,
            "finished_at": utc_timestamp(),
            "stdout": bounded(stdout or ""),
            "stderr": bounded(stderr or ""),
        }
        if harness_error is not None:
            result["harness_error"] = harness_error
            result["harness_verdict"] = "FAILED"
        elif timed_out or native_returncode != 0:
            result["harness_verdict"] = "EXPECTED_FAILURE" if expect_failure else "FAILED"
        else:
            result["harness_verdict"] = "ACCEPTED"
        self.results.append(result)
        if (timed_out or native_returncode != 0 or harness_error is not None) and not expect_failure:
            self.failure = self.failure or result
        return result

    @property
    def child_timeout(self) -> int:
        return CHILD_TIMEOUT_SECONDS

    @property
    def total_timeout(self) -> int:
        return TOTAL_TIMEOUT_SECONDS


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
    service_test = rel(SERVICE_TEST_PATH).read_text(encoding="utf-8")
    lifecycle_mixer_field = line_hits(lifecycle, r"^\s*mixer\s+\*room\.PCM16Mixer\b")
    public_route_sources = {
        path: rel(path).read_text(encoding="utf-8") for path in PUBLIC_ROUTE_PATHS
    }
    public_route_legacy_imports = {
        path: line_hits(text, r"internal/services/internal/agentruntime")
        for path, text in public_route_sources.items()
    }
    public_route_legacy_entrypoints = {
        path: line_hits(text, r"\bRunRoom(?:WithResult)?\s*\(")
        for path, text in public_route_sources.items()
    }
    service_test_run_room_exports = line_hits(
        service_test, r"^\s*(?:func|var)\s+RunRoom(?:WithResult)?\b"
    )

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

    required_evidence = (
        findings[0]["evidence"]["new_ticker"],
        findings[1]["evidence"]["decode"],
        findings[1]["evidence"]["encode"],
        findings[2]["evidence"]["mixer_constructor"],
        findings[2]["evidence"]["source_constructor"],
        findings[2]["evidence"]["sink_constructor"],
        lifecycle_mixer_field,
        findings[3]["evidence"]["input_read"],
        findings[3]["evidence"]["sink_write"],
    )
    if not all(required_evidence):
        raise RuntimeError("gap oracle missing one or more required production edges")

    return {
        "status": "SOURCE_GAP_CONFIRMED",
        "scope": "compiled legacy implementation; no claim of active public room-run reachability",
        "legacy_package": "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime",
        "legacy_entrypoints": {
            "RunRoom": line_hits(orchestration, r"func RunRoom\("),
            "RunRoomWithResult": line_hits(orchestration, r"func RunRoomWithResult\("),
            "lifecycle_mixer_field": lifecycle_mixer_field,
        },
        "findings": findings,
        "public_route": {
            "cli_room_service_type": line_hits(public_cli, r"runtimeRooms\.Service"),
            "wire_room_provider": line_hits(wire, r"NewRoomServiceWithDevices"),
            "generated_room_service": line_hits(wire_gen, r"NewRoomServiceWithDevices"),
            "service_test_legacy_import": line_hits(service_test, r"^import impl .*internal/agentruntime"),
            "public_route_legacy_imports": public_route_legacy_imports,
            "public_route_legacy_entrypoints": public_route_legacy_entrypoints,
            "service_test_run_room_exports": service_test_run_room_exports,
            "legacy_entrypoint_imported_by_public_cli": any(
                bool(hits) for hits in public_route_legacy_entrypoints.values()
            ),
            "legacy_entrypoint_exposed_by_service_test": bool(service_test_run_room_exports),
            "interpretation": "The current yui room run command is wired to go-agent-runtime/services/rooms. servicetest imports the legacy package for other session seams, but runtime.go does not export RunRoom; the old RunRoom path is compiled and exercised by its own internal package tests, not exposed by the service-test API. It is a concrete source/ownership gap and migration hazard, not runtime/acoustic proof.",
        },
            "smallest_bypass": "agent-cli/internal/room/mixer.go plus its legacy agentruntime callers: host ticker + local PCM codec + direct device sink write in one room path.",
    }


def parse_gap_output(raw: str) -> tuple[bool, str]:
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as exc:
        return False, f"invalid JSON: {exc}"
    if not isinstance(payload, dict):
        return False, "root output is not an object"
    required = ("status", "findings", "smallest_bypass")
    missing = [key for key in required if key not in payload]
    if missing:
        return False, f"missing required output fields: {', '.join(missing)}"
    if payload["status"] != "SOURCE_GAP_CONFIRMED":
        return False, f"unexpected status {payload['status']!r}"
    if not payload["findings"] or not payload["smallest_bypass"]:
        return False, "gap output is empty"
    return True, "accepted source-gap output"


def runner_controls(runner: Runner) -> dict[str, Any]:
    snapshot = source_snapshot()
    first_path = sorted(snapshot)[0]
    actual_digest = snapshot[first_path]["sha256"]
    stale_digest = ("0" if actual_digest[0] != "0" else "1") + actual_digest[1:]
    stale_rejected = stale_digest != actual_digest

    accepted_gap = {
        "status": "SOURCE_GAP_CONFIRMED",
        "findings": [{"id": "fixture"}],
        "smallest_bypass": "fixture-edge",
    }
    false_success_ok, false_success_detail = parse_gap_output(json.dumps({"status": "ACCEPTED"}))
    truncated_ok, truncated_detail = parse_gap_output('{"status":"SOURCE_GAP_CONFIRMED"')
    accepted_ok, accepted_detail = parse_gap_output(json.dumps(accepted_gap))

    hang = runner.run(
        "runner-child-hang",
        [sys.executable, "-c", "import time; time.sleep(120)"],
        OUTPUT_DIR,
        timeout=1,
        expect_failure=True,
    )
    cleanup_ok = bool(hang["timed_out"] and not hang["children_alive"] and hang["native_returncode"] is not None)
    no_tests = runner.run(
        "runner-no-tests",
        [
            "go",
            "test",
            "-json",
            "./internal/room",
            "-run",
            "^TestC25NoSuchTest$",
            "-count=1",
            "-timeout=45s",
        ],
        rel("agent-cli"),
        timeout=CHILD_TIMEOUT_SECONDS,
        expect_failure=True,
        require_tests=True,
    )
    no_tests_ok = no_tests.get("harness_error") == "zero test discovery"
    if not stale_rejected or false_success_ok or truncated_ok or not accepted_ok or not cleanup_ok or not no_tests_ok:
        runner.failure = runner.failure or {
            "label": "runner-controls",
            "returncode": 1,
            "stderr": "one or more stale/false-success/truncated/no-test/cleanup controls did not reach the intended oracle",
        }
    return {
        "stale_hash": {
            "verdict": "REJECTED_STALE_HASH" if stale_rejected else "FAILED",
            "path": first_path,
            "actual_digest": actual_digest,
            "stale_digest": stale_digest,
        },
        "false_success_output": {
            "verdict": "REJECTED_FALSE_SUCCESS" if not false_success_ok else "FAILED",
            "detail": false_success_detail,
        },
        "truncated_output": {
            "verdict": "REJECTED_TRUNCATED_OUTPUT" if not truncated_ok else "FAILED",
            "detail": truncated_detail,
        },
        "accepted_output": {
            "verdict": "ACCEPTED" if accepted_ok else "FAILED",
            "detail": accepted_detail,
        },
        "child_hang_cleanup": {
            "verdict": "ACCEPTED" if cleanup_ok else "FAILED",
            "native_returncode": hang["native_returncode"],
            "timed_out": hang["timed_out"],
            "children_alive": hang["children_alive"],
            "harness_verdict": hang["harness_verdict"],
        },
        "zero_test_discovery": {
            "verdict": "ACCEPTED" if no_tests_ok else "FAILED",
            "harness_error": no_tests.get("harness_error"),
            "native_returncode": no_tests.get("native_returncode"),
            "timed_out": no_tests.get("timed_out"),
            "children_alive": no_tests.get("children_alive"),
        },
        "native_oracle_failure": hang,
    }


def fixture_manifest_check() -> dict[str, Any]:
    manifest_path = OWNED / FIXTURE_MANIFEST_NAME
    result: dict[str, Any] = {
        "path": str(manifest_path.relative_to(REPO)),
        "expected_paths": sorted(FIXTURE_PATHS),
        "verdict": "FAILED",
    }
    if not manifest_path.is_file() or manifest_path.is_symlink():
        result["error"] = "fixture manifest is missing or not a regular file"
        return result
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        result["error"] = f"fixture manifest cannot be read: {exc}"
        return result
    if not isinstance(manifest, dict):
        result["error"] = "fixture manifest root is not an object"
        return result
    result["manifest_sha256"] = sha256_file(manifest_path)
    result["manifest_bytes"] = manifest_path.stat().st_size
    if manifest.get("schema") != 1 or manifest.get("task") != TASK or manifest.get("source_revision") != SOURCE_REVISION:
        result["error"] = "fixture manifest identity does not match the pinned task/source"
        return result
    entries = manifest.get("fixtures")
    if not isinstance(entries, list):
        result["error"] = "fixture manifest fixtures is not a list"
        return result
    by_path: dict[str, dict[str, Any]] = {}
    for entry in entries:
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str) or entry["path"] in by_path:
            result["error"] = "fixture manifest has a malformed or duplicate entry"
            return result
        by_path[entry["path"]] = entry
    if sorted(by_path) != sorted(FIXTURE_PATHS):
        result["error"] = "fixture manifest does not enumerate exactly the owned fixtures"
        result["declared_paths"] = sorted(by_path)
        return result
    fixtures: list[dict[str, Any]] = []
    errors: list[str] = []
    for path in sorted(FIXTURE_PATHS):
        fixture_path = OWNED / path
        entry = by_path[path]
        if not fixture_path.is_file() or fixture_path.is_symlink():
            errors.append(f"{path} is missing or not a regular file")
            continue
        actual_sha = sha256_file(fixture_path)
        actual_bytes = fixture_path.stat().st_size
        actual_lines = len(fixture_path.read_text(encoding="utf-8").splitlines())
        fixture = {
            "path": path,
            "expected_sha256": entry.get("sha256"),
            "actual_sha256": actual_sha,
            "expected_bytes": entry.get("bytes"),
            "actual_bytes": actual_bytes,
            "expected_lines": entry.get("lines"),
            "actual_lines": actual_lines,
        }
        fixtures.append(fixture)
        if entry.get("sha256") != actual_sha or entry.get("bytes") != actual_bytes or entry.get("lines") != actual_lines:
            errors.append(f"{path} digest or size metadata does not match")
    result["fixtures"] = fixtures
    result["errors"] = errors
    result["verdict"] = "ACCEPTED" if not errors else "FAILED"
    return result


def source_archive_check(runner: Runner) -> dict[str, Any]:
    archive_path = OWNED / SOURCE_ARCHIVE_NAME
    generated_path = runner.tmp_dir / "source-from-pin.tar"
    generation = runner.run(
        "source-archive-rebuild",
        ["git", "archive", "--format=tar", f"--output={generated_path}", SOURCE_REVISION],
        REPO,
    )
    result: dict[str, Any] = {
        "path": str(archive_path.relative_to(REPO)),
        "expected_sha256": SOURCE_ARCHIVE_SHA256,
        "command_result": generation,
        "generated_path": str(generated_path),
        "verdict": "FAILED",
    }
    generated_ok = generation["returncode"] == 0 and generated_path.is_file() and not generated_path.is_symlink()
    if generated_ok:
        result["generated_sha256"] = sha256_file(generated_path)
        result["generated_bytes"] = generated_path.stat().st_size
    materialized_ok = True
    if archive_path.exists() or archive_path.is_symlink():
        result["materialized"] = True
        if archive_path.is_symlink() or not archive_path.is_file():
            materialized_ok = False
            result["materialized_error"] = "materialized source archive is not a regular file"
        else:
            result["materialized_sha256"] = sha256_file(archive_path)
            result["materialized_bytes"] = archive_path.stat().st_size
            materialized_ok = result["materialized_sha256"] == SOURCE_ARCHIVE_SHA256
    else:
        result["materialized"] = False
        result["materialized_note"] = "reproducible archive was verified from the pinned Git object; no checked-in copy is required"
    result["verdict"] = "ACCEPTED" if generated_ok and result.get("generated_sha256") == SOURCE_ARCHIVE_SHA256 and materialized_ok else "FAILED"
    return result


def changed_path_allowlist(runner: Runner) -> dict[str, Any]:
    committed = runner.run(
        "changed-paths",
        ["git", "diff", "--name-only", SOURCE_REVISION, "HEAD"],
        REPO,
    )
    working = runner.run(
        "working-tree-paths",
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        REPO,
    )
    committed_paths = [line.strip() for line in committed.get("stdout", "").splitlines() if line.strip()]
    working_paths: list[str] = []
    for line in working.get("stdout", "").splitlines():
        if len(line) < 4:
            continue
        path = line[3:]
        if " -> " in path:
            path = path.rsplit(" -> ", 1)[1]
        if path:
            working_paths.append(path)
    all_paths = sorted(set(committed_paths + working_paths))
    outside = [path for path in all_paths if not path.startswith(OWNED_RELATIVE)]
    malformed = [path for path in all_paths if path.startswith("/") or "\x00" in path]
    result = {
        "allowed_prefixes": [OWNED_RELATIVE],
        "committed_paths": sorted(set(committed_paths)),
        "working_tree_paths": sorted(set(working_paths)),
        "observed_paths": all_paths,
        "outside_allowed": outside,
        "malformed_paths": malformed,
        "complete": committed["returncode"] == 0 and working["returncode"] == 0 and not outside and not malformed and bool(all_paths),
        "committed_command": committed,
        "working_tree_command": working,
    }
    result["verdict"] = "ACCEPTED" if result["complete"] else "FAILED"
    return result


def dependency_ast_oracle(runner: Runner, paths: list[Path]) -> dict[str, Any]:
    helper_path = runner.tmp_dir / "dependency-oracle.go"
    binary_path = runner.tmp_dir / "dependency-oracle"
    helper_path.write_text(AST_ORACLE_SOURCE, encoding="utf-8")
    build_result = runner.run(
        "dependency-ast-oracle-build",
        [
            "env",
            "GO111MODULE=off",
            "GOWORK=off",
            "go", "build", "-o", str(binary_path), str(helper_path),
        ],
        REPO,
    )
    command_result = runner.run(
        "dependency-ast-oracle",
        [str(binary_path), *[str(path) for path in paths]],
        REPO,
    ) if build_result["returncode"] == 0 else build_result
    result: dict[str, Any] = {
        "build_result": build_result,
        "command_result": command_result,
        "reports": [],
        "verdict": "FAILED",
    }
    if build_result["returncode"] != 0 or command_result["returncode"] != 0 or command_result.get("timed_out"):
        result["error"] = "AST dependency oracle failed or timed out"
        return result
    try:
        reports = json.loads(command_result.get("stdout", ""))
    except json.JSONDecodeError as exc:
        result["error"] = f"AST dependency oracle returned invalid JSON: {exc}"
        return result
    if not isinstance(reports, list) or len(reports) != len(paths):
        result["error"] = "AST dependency oracle returned an empty or incomplete report"
        return result
    if any(
        not isinstance(report, dict)
        or not isinstance(report.get("path"), str)
        or not isinstance(report.get("imports"), list)
        or not isinstance(report.get("selector_calls"), list)
        for report in reports
    ):
        result["error"] = "AST dependency oracle returned a malformed report"
        return result
    result["reports"] = reports
    result["verdict"] = "ACCEPTED"
    return result


def evaluate_fixture_report(
    report: dict[str, Any],
    *,
    expected_imports: set[str] | None = None,
    required_imports: set[str] | None = None,
    required_calls: set[str] | None = None,
    require_forbidden: bool = False,
) -> dict[str, Any]:
    imports = set(report.get("imports", []))
    calls = set(report.get("selector_calls", []))
    forbidden_imports = sorted(imports & FORBIDDEN_FIXTURE_IMPORTS)
    forbidden_calls = sorted(calls & FORBIDDEN_FIXTURE_CALLS)
    missing_imports = sorted((required_imports or set()) - imports)
    missing_calls = sorted((required_calls or set()) - calls)
    unexpected_imports = sorted(imports - (expected_imports or imports))
    if require_forbidden:
        accepted = bool(forbidden_imports) and bool(forbidden_calls) and not missing_imports and not missing_calls
        verdict = "REJECTED_AS_FORBIDDEN" if accepted else "FAILED"
    else:
        accepted = (
            not forbidden_imports
            and not forbidden_calls
            and not missing_imports
            and not missing_calls
            and not unexpected_imports
        )
        verdict = "ACCEPTED" if accepted else "FAILED"
    return {
        "path": report.get("path"),
        "imports": sorted(imports),
        "selector_calls": sorted(calls),
        "forbidden_imports": forbidden_imports,
        "forbidden_calls": forbidden_calls,
        "missing_imports": missing_imports,
        "missing_calls": missing_calls,
        "unexpected_imports": unexpected_imports,
        "verdict": verdict,
    }


def dependency_controls(runner: Runner) -> dict[str, Any]:
    allowed_path = OWNED / FIXTURE_PATHS[0]
    forbidden_path = OWNED / FIXTURE_PATHS[1]
    allowed = allowed_path.read_text(encoding="utf-8")
    mutation_dir = runner.tmp_dir / "dependency-fixtures"
    mutation_dir.mkdir(parents=True, exist_ok=True)
    allowed_with_forbidden_edge = mutation_dir / "allowed-with-forbidden-edge.go"
    allowed_with_forbidden_edge.write_text(
        allowed.replace("\n)", '\n\t"time"\n)', 1)
        + '\nfunc AllowedWithForbiddenEdge() { _ = time.NewTicker(time.Millisecond) }\n',
        encoding="utf-8",
    )
    allowed_comment_only = mutation_dir / "allowed-comment-only.go"
    allowed_comment_only.write_text(
        allowed + "\n// time.NewTicker, codec.EncodePCM16, and devicegw.NewDeviceSink are forbidden tokens only.\n",
        encoding="utf-8",
    )
    oracle_paths = [allowed_path, forbidden_path, allowed_with_forbidden_edge, allowed_comment_only]
    oracle = dependency_ast_oracle(runner, oracle_paths)
    reports_by_path = {report.get("path"): report for report in oracle.get("reports", [])}

    def report_for(path: Path) -> dict[str, Any]:
        report = reports_by_path.get(str(path))
        return report if isinstance(report, dict) else {}

    positive = evaluate_fixture_report(
        report_for(allowed_path),
        expected_imports=CANONICAL_FIXTURE_IMPORTS,
        required_imports=CANONICAL_FIXTURE_IMPORTS,
        required_calls=CANONICAL_FIXTURE_CALLS,
    )
    negative = evaluate_fixture_report(
        report_for(forbidden_path),
        required_imports=FORBIDDEN_FIXTURE_IMPORTS,
        required_calls=FORBIDDEN_FIXTURE_CALLS,
        require_forbidden=True,
    )
    mutation = evaluate_fixture_report(
        report_for(allowed_with_forbidden_edge),
        expected_imports=CANONICAL_FIXTURE_IMPORTS,
        required_imports=CANONICAL_FIXTURE_IMPORTS,
        required_calls=CANONICAL_FIXTURE_CALLS,
    )
    comment_only = evaluate_fixture_report(
        report_for(allowed_comment_only),
        expected_imports=CANONICAL_FIXTURE_IMPORTS,
        required_imports=CANONICAL_FIXTURE_IMPORTS,
        required_calls=CANONICAL_FIXTURE_CALLS,
    )
    mutation["verdict"] = (
        "REJECTED_AS_FORBIDDEN"
        if mutation["forbidden_imports"] and mutation["forbidden_calls"]
        else "FAILED"
    )
    manifest = fixture_manifest_check()
    positive_ok = oracle["verdict"] == "ACCEPTED" and positive["verdict"] == "ACCEPTED" and manifest["verdict"] == "ACCEPTED"
    negative_ok = negative["verdict"] == "REJECTED_AS_FORBIDDEN"
    mutation_ok = mutation["verdict"] == "REJECTED_AS_FORBIDDEN"
    comment_ok = comment_only["verdict"] == "ACCEPTED"
    if not (positive_ok and negative_ok and mutation_ok and comment_ok):
        runner.failure = runner.failure or {
            "label": "dependency-controls",
            "returncode": 1,
            "stderr": "AST dependency oracle or one of its positive/negative/comment mutation controls failed",
        }
    return {
        "verdict": "ACCEPTED" if positive_ok and negative_ok and mutation_ok and comment_ok else "FAILED",
        "ast_oracle": oracle,
        "fixture_manifest": manifest,
        "positive_control": positive,
        "negative_control": negative,
        "mutation_control": mutation,
        "comment_only_control": comment_only,
        "interpretation": "The AST dependency oracle derives imports and selector calls from parsed Go declarations, so comments cannot create edges and an allowed fixture containing a forbidden edge is rejected. This is a source dependency check, not device playback evidence.",
    }


def write_json(name: str, value: Any) -> None:
    path = OUTPUT_DIR / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_markdown(diagnosis: dict[str, Any]) -> None:
    gap = diagnosis["source_gap"]
    controls = diagnosis["dependency_controls"]
    lines = [
        "# C25 audio/device boundary diagnosis",
        "",
        f"- Task: `{TASK}`",
        f"- Candidate revision: `{diagnosis.get('git_head', 'not captured')}`",
        f"- Source revision: `{SOURCE_REVISION}`",
        f"- Source archive SHA-256: `{SOURCE_ARCHIVE_SHA256}`",
        "- Decision basis: source-only diagnosis; no realtime, provider, physical-device, or acoustic claim.",
        "- The pinned source is required to be an ancestor of the candidate and every audited production path is required to be unchanged from that source.",
        "",
        "## Finding",
        "",
        f"`{gap['status']}`: the smallest remaining bypass is the compiled legacy CLI room path in `agent-cli/internal/room/mixer.go` and its `agent-cli/internal/services/internal/agentruntime` callers.",
        "",
        "That path owns a host `time.Ticker`, local PCM16 encode/decode, direct device construction, and direct sink writes. The current public `yui room run` command is wired to `go-agent-runtime/services/rooms`, so this packet does not claim that the legacy path is the active public workflow. It does establish a concrete production-source ownership gap and migration hazard: the old alternate implementation remains compiled and exercised by its own internal package tests while duplicating the intended audio/device boundaries; `servicetest/runtime.go` imports that package for session helpers but exports no `RunRoom` API.",
        "",
        "## Focused causal evidence",
        "",
        f"- Legacy lifecycle mixer field: `{gap['legacy_entrypoints']['lifecycle_mixer_field']}` (the actual field is at `session_room_lifecycle.go:74`).",
        f"- Service-test import: `{gap['public_route']['service_test_legacy_import']}`; `RunRoom` exports: `{gap['public_route']['service_test_run_room_exports']}`.",
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
        f"- Fixture manifest: `{controls['fixture_manifest']['verdict']}` with exact SHA-256/byte/line checks for both owned fixtures.",
        f"- Pinned source archive: `{diagnosis['source_archive']['verdict']}`; rebuilt Git archive SHA-256 is `{diagnosis['source_archive'].get('generated_sha256', 'not captured')}`.",
        f"- Changed-path allowlist: `{diagnosis['changed_path_allowlist']['verdict']}` for the complete source-to-candidate and working-tree path set.",
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
    path = OUTPUT_DIR / "diagnosis.md"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def run(mode: str) -> int:
    runner = Runner()
    diagnosis: dict[str, Any] = {
        "schema": 1,
        "task": TASK,
        "mode": mode,
        "source_root": str(REPO),
        "output_dir": str(OUTPUT_DIR),
        "source_revision": SOURCE_REVISION,
        "origin_main": ORIGIN_MAIN,
        "baseline_revision": BASELINE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "source_archive_sha256": SOURCE_ARCHIVE_SHA256,
        "owned_path": str(OWNED.relative_to(REPO)),
        "bounded_runner": {"per_child_seconds": CHILD_TIMEOUT_SECONDS, "aggregate_seconds": TOTAL_TIMEOUT_SECONDS, "clean_shutdown": True},
        "source_snapshot": source_snapshot(),
    }

    if mode in ("gap", "all"):
        head = runner.run("git-head", ["git", "rev-parse", "HEAD"], REPO)
        diagnosis["git_head"] = head["stdout"].strip()
        diagnosis["source_archive"] = source_archive_check(runner)
        diagnosis["changed_path_allowlist"] = changed_path_allowlist(runner)
        ancestry = runner.run(
            "source-ancestry",
            ["git", "merge-base", "--is-ancestor", SOURCE_REVISION, "HEAD"],
            REPO,
        )
        diagnosis["source_ancestry"] = ancestry
        source_paths = runner.run(
            "source-path-diff",
            ["git", "diff", "--quiet", SOURCE_REVISION, "--"] + ALL_SOURCE_PATHS,
            REPO,
        )
        diagnosis["source_path_diff"] = source_paths
        diagnosis["source_gap"] = source_gap()
        diagnosis["production_path_status"] = runner.run(
            "production-path-status",
            ["git", "status", "--porcelain", "--untracked-files=all", "--"] + ALL_SOURCE_PATHS,
            REPO,
        )
        if ancestry["returncode"] != 0:
            runner.failure = runner.failure or {
                "label": "source-ancestry",
                "returncode": ancestry["returncode"],
                "stderr": f"pinned source {SOURCE_REVISION} is not an ancestor of candidate {diagnosis['git_head']}",
            }
        if source_paths["returncode"] != 0:
            runner.failure = runner.failure or {
                "label": "source-path-diff",
                "returncode": source_paths["returncode"],
                "stderr": f"inspected production paths differ from pinned source {SOURCE_REVISION}",
            }
        if diagnosis["source_archive"]["verdict"] != "ACCEPTED":
            runner.failure = runner.failure or {
                "label": "source-archive",
                "returncode": 1,
                "stderr": "pinned source archive could not be reproduced and verified",
            }
        if diagnosis["changed_path_allowlist"]["verdict"] != "ACCEPTED":
            runner.failure = runner.failure or {
                "label": "changed-path-allowlist",
                "returncode": 1,
                "stderr": "candidate or working-tree paths escape the owned C25 evidence folder",
            }

    if mode in ("controls", "all"):
        diagnosis["dependency_controls"] = dependency_controls(runner)
        if diagnosis["dependency_controls"].get("verdict") != "ACCEPTED":
            runner.failure = runner.failure or {
                "label": "dependency-controls",
                "returncode": 1,
                "stderr": "positive/negative dependency controls or their fixture manifest did not verify",
            }
        diagnosis["runner_controls"] = runner_controls(runner)
        diagnosis["package_probes"] = [
            package_probe(runner, "agent-cli", "./internal/room"),
            package_probe(runner, "agent-cli", "./internal/services/internal/agentruntime"),
            package_probe(runner, "agent-cli", "./internal/transport/cli"),
            package_probe(runner, "go-audio", "./pkg/mixer"),
            package_probe(runner, "go-agent-runtime", "./services/rooms"),
        ]
        for probe in diagnosis["package_probes"]:
            command_result = probe.get("command_result", {})
            if command_result.get("returncode") != 0 or not command_result.get("stdout", "").strip() or not probe.get("import_path"):
                runner.failure = runner.failure or {
                    "label": probe.get("package", "package-probe"),
                    "returncode": command_result.get("returncode"),
                    "stderr": "package probe failed or returned empty import metadata",
                }

    if mode in ("regressions", "all"):
        tests = [
            ("canonical-mixer-tests", "go-audio", ["go", "test", "-json", "./pkg/mixer", "-run", "^Test(Mixer|PCMAccumulator)", "-count=1", "-timeout=45s"], True),
            ("canonical-playback-boundary-tests", "go-audio", ["go", "test", "-json", "./pkg/audio", "-run", "^Test(Playback|Render)", "-count=1", "-timeout=45s"], True),
            ("canonical-clock-boundary-tests", "go-audio", ["go", "test", "-json", "./pkg/clock", "-run", "^Test(DeterministicTimerFiresAtLogicalDeadline|RequireTimerSourceDoesNotFallbackToHostTime)$", "-count=1", "-timeout=45s"], True),
            ("canonical-room-lifecycle-boundary-tests", "go-agent-runtime", ["go", "test", "-json", "./services/rooms/internal/lifecycle", "-run", "^(TestRoomGraphRoutesEachSourceToPeersOnly|TestRoomGraphRecordsReceivedOnlyAfterProviderAdmission|TestMediaBridgePreservesPCMAndUsesBoundedFrames)$", "-count=1", "-timeout=45s"], True),
            ("loop-ownership-guard", "go-agent-loop", ["go", "test", "-json", "./pkg/agentloop", "-run", "^TestProductionAudioAndDeviceOwnership$", "-count=1", "-timeout=45s"], True),
            ("legacy-mixer-focused-test", "agent-cli", ["go", "test", "-json", "./internal/room", "-run", "^TestPCM16MixerMixesEveryActiveInputAndClips$", "-count=1", "-timeout=45s"], True),
            ("legacy-agent-runtime-focused-test", "agent-cli", ["go", "test", "-json", "./internal/services/internal/agentruntime", "-run", "^TestRunRoom_EmptyResponseDoesNotAdvanceTurnsOrMaxTurns$", "-count=1", "-timeout=45s"], True),
            ("legacy-agent-runtime-focused-test-race", "agent-cli", ["go", "test", "-json", "-race", "./internal/services/internal/agentruntime", "-run", "^TestRunRoom_EmptyResponseDoesNotAdvanceTurnsOrMaxTurns$", "-count=1", "-timeout=45s"], True),
            ("legacy-agent-runtime-vet", "agent-cli", ["go", "vet", "./internal/services/internal/agentruntime"], False),
        ]
        diagnosis["focused_regressions"] = []
        for label, module_dir, command, require_tests in tests:
            diagnosis["focused_regressions"].append(
                runner.run(
                    label,
                    command,
                    rel(module_dir),
                    require_tests=require_tests,
                )
            )

    diagnosis["commands"] = runner.results
    diagnosis["failure"] = runner.failure
    diagnosis["bounded_runner"]["clean_shutdown"] = runner.shutdown_clean
    diagnosis["elapsed_seconds"] = round(time.monotonic() - runner.started, 3)
    diagnosis["decision"] = "ACCEPTED" if runner.failure is None else "CONTINUE"
    write_json("diagnosis.json", diagnosis)
    write_json("provenance.json", {
        "task": TASK,
        "candidate_revision": diagnosis.get("git_head"),
        "source_revision": SOURCE_REVISION,
        "origin_main": ORIGIN_MAIN,
        "baseline_revision": BASELINE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "source_archive_sha256": SOURCE_ARCHIVE_SHA256,
        "source_archive": diagnosis.get("source_archive"),
        "source_ancestry": diagnosis.get("source_ancestry"),
        "source_path_diff": diagnosis.get("source_path_diff"),
        "changed_path_allowlist": diagnosis.get("changed_path_allowlist"),
        "fixture_manifest": diagnosis.get("dependency_controls", {}).get("fixture_manifest"),
        "source_files": diagnosis["source_snapshot"],
        "generated_by": "verify.py",
    })
    if "source_gap" in diagnosis and "dependency_controls" in diagnosis:
        write_markdown(diagnosis)
    return 0 if runner.failure is None else 1


def main() -> int:
    global REPO, SOURCE_REVISION, ORIGIN_MAIN, OUTPUT_DIR, CHILD_TIMEOUT_SECONDS, TOTAL_TIMEOUT_SECONDS
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("gap", "controls", "regressions", "all"), default="all")
    parser.add_argument("--source", type=Path, default=REPO, help="pinned source root to inspect")
    parser.add_argument("--source-revision", default=SOURCE_REVISION, help="exact pinned source revision")
    parser.add_argument("--child-timeout-seconds", type=int, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout-seconds", type=int, default=MAX_TOTAL_SECONDS)
    parser.add_argument("--output", type=Path, default=OWNED, help="owned output/checkpoint directory")
    args = parser.parse_args()
    try:
        REPO = args.source.resolve()
        SOURCE_REVISION = args.source_revision
        ORIGIN_MAIN = SOURCE_REVISION
        if args.child_timeout_seconds <= 0 or args.child_timeout_seconds > 60:
            raise ValueError("--child-timeout-seconds must be between 1 and 60")
        if args.aggregate_timeout_seconds <= 0 or args.aggregate_timeout_seconds > 600:
            raise ValueError("--aggregate-timeout-seconds must be between 1 and 600")
        OUTPUT_DIR = args.output.resolve()
        try:
            OUTPUT_DIR.relative_to(OWNED)
        except ValueError as exc:
            raise ValueError("--output must remain inside the owned C25 evidence folder") from exc
        CHILD_TIMEOUT_SECONDS = args.child_timeout_seconds
        TOTAL_TIMEOUT_SECONDS = args.aggregate_timeout_seconds
        return run(args.mode)
    except Exception as exc:
        failure = {"decision": "CONTINUE", "error": f"{type(exc).__name__}: {exc}"}
        write_json("diagnosis.json", failure)
        print(json.dumps(failure), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
