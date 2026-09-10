#!/usr/bin/env python3
"""Bounded causal verifier for the external C36 room-media consumer."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import signal
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
PROJECT_ROOT = HERE.parents[5]
MODULE_PATH = pathlib.Path("docs/temp/projects/audio-runtime/audio-runtime-c36-room-media-epoch-consumer")
FIXTURES = HERE / "fixtures"
MAX_CAPTURE_BYTES = 4 * 1024 * 1024
WORKSPACE_MODULES = (
    pathlib.Path("agent-cli"),
    pathlib.Path("go-agent-loop"),
    pathlib.Path("go-agent-runtime"),
    pathlib.Path("go-audio"),
    pathlib.Path("go-device-gateway"),
    pathlib.Path("go-llm-gateway"),
)
BUILD_FLAGS = {
    "room_media": ["-trimpath"],
    "yui": ["-tags=nomicrophone", "-trimpath"],
}
BUILD_PACKAGES = {
    "room_media": "./cmd/room-media",
    "yui": "./agent-cli/cmd/yui",
}
TOOL_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
INTERRUPTION_FIXTURE_SHA256 = "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206"
TOOL_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
INTERRUPTION_PCM_SHA256 = "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22"
HEALTHY_TAIL_SHA256 = "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"


class EvidenceFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def selected_environment(env: dict[str, str]) -> dict[str, str]:
    keys = ("PATH", "HOME", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL")
    return {key: env.get(key, "") for key in keys}


def bounded_remaining(deadline: float, child_timeout: float) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise EvidenceFailure("aggregate verifier deadline exhausted")
    return min(child_timeout, remaining)


def drain_stream(stream: Any, limit: int, result: dict[str, bytes], key: str) -> None:
    chunks: list[bytes] = []
    total = 0
    while True:
        block = stream.read(64 * 1024)
        if not block:
            break
        if total < limit:
            chunks.append(block[: limit - total])
            total += len(chunks[-1])
    result[key] = b"".join(chunks)


def process_group_alive(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def wait_for_process_group_gone(pgid: int, deadline: float) -> bool:
    while time.monotonic() < deadline:
        if not process_group_alive(pgid):
            return True
        time.sleep(min(0.02, max(0.001, deadline - time.monotonic())))
    return not process_group_alive(pgid)


def run_process(
    label: str,
    argv: list[str],
    cwd: pathlib.Path,
    run_dir: pathlib.Path,
    input_bytes: bytes | None,
    deadline: float,
    child_timeout: float,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    run_dir.mkdir(parents=True, exist_ok=True)
    safe = "".join(char if char.isalnum() or char in "-_." else "_" for char in label)
    stdout_path = run_dir / f"{safe}.stdout"
    stderr_path = run_dir / f"{safe}.stderr"
    record_path = run_dir / f"{safe}.json"
    selected = dict(os.environ if env is None else env)
    process = subprocess.Popen(
        argv,
        cwd=str(cwd),
        env=selected,
        stdin=subprocess.PIPE if input_bytes is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    captured: dict[str, bytes] = {}
    out_thread = threading.Thread(target=drain_stream, args=(process.stdout, MAX_CAPTURE_BYTES, captured, "stdout"), daemon=True)
    err_thread = threading.Thread(target=drain_stream, args=(process.stderr, MAX_CAPTURE_BYTES, captured, "stderr"), daemon=True)
    out_thread.start()
    err_thread.start()
    if input_bytes is not None and process.stdin is not None:
        try:
            process.stdin.write(input_bytes)
            process.stdin.close()
        except BrokenPipeError:
            pass
    started = time.monotonic()
    timed_out = False
    term_sent = False
    kill_sent = False
    try:
        process.wait(timeout=bounded_remaining(deadline, child_timeout))
    except subprocess.TimeoutExpired:
        timed_out = True
        term_sent = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=min(2.0, bounded_remaining(deadline, 2.0)))
        except subprocess.TimeoutExpired:
            kill_sent = True
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=min(2.0, bounded_remaining(deadline, 2.0)))
            except subprocess.TimeoutExpired as exc:
                raise EvidenceFailure(f"{label} did not exit after bounded TERM/KILL cleanup") from exc
    out_thread.join(timeout=min(2.0, max(0.1, deadline - time.monotonic())))
    err_thread.join(timeout=min(2.0, max(0.1, deadline - time.monotonic())))
    stdout = captured.get("stdout", b"")
    stderr = captured.get("stderr", b"")
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    record = {
        "label": label,
        "argv": argv,
        "cwd": str(cwd),
        "environment": selected_environment(selected),
        "child_timeout_seconds": child_timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "term_sent": term_sent,
        "kill_sent": kill_sent,
        "reaped": process.poll() is not None,
        "process_group_id": process.pid,
        "process_group_gone": wait_for_process_group_gone(process.pid, min(deadline, time.monotonic() + 0.5)),
        "output_drained": not out_thread.is_alive() and not err_thread.is_alive(),
        "stdout_path": str(stdout_path),
        "stderr_path": str(stderr_path),
    }
    write_json(record_path, record)
    return record


def require_ok(result: dict[str, Any]) -> None:
    if (
        result["timed_out"]
        or result["exit_code"] != 0
        or not result["reaped"]
        or not result["process_group_gone"]
        or not result["output_drained"]
    ):
        raise EvidenceFailure(f"{result['label']} failed; see {result['stderr_path']} and {result['stdout_path']}")


def source_root(source: pathlib.Path) -> pathlib.Path:
    source = source.resolve()
    if (source / "go.work").is_file():
        if (source / MODULE_PATH).is_dir():
            return source
        raise EvidenceFailure(f"--source workspace does not contain the admitted consumer: {source}")
    if source.name == MODULE_PATH.name and (source / "go.mod").is_file():
        return PROJECT_ROOT
    raise EvidenceFailure(f"--source must be the admitted workspace root: {source}")


def module_dir(root: pathlib.Path) -> pathlib.Path:
    path = root / MODULE_PATH
    if not path.is_dir():
        raise EvidenceFailure(f"admitted consumer module is missing: {path}")
    return path


def git_revision(root: pathlib.Path) -> str:
    result = subprocess.run(["git", "-C", str(root), "rev-parse", "HEAD"], text=True, capture_output=True, check=True)
    return result.stdout.strip()


def build_source_archive(root: pathlib.Path, manifest: dict[str, Any], evidence: pathlib.Path) -> dict[str, Any]:
    archive = evidence / "artifacts" / "build-inputs.tar"
    archive.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "w") as handle:
        for entry in manifest["files"]:
            relative = pathlib.Path(entry["path"])
            path = root / relative
            if not path.is_file():
                raise EvidenceFailure(f"build input disappeared while archiving: {relative}")
            info = handle.gettarinfo(str(path), arcname=str(relative))
            info.mtime = 0
            info.uid = 0
            info.gid = 0
            info.uname = ""
            info.gname = ""
            with path.open("rb") as stream:
                handle.addfile(info, stream)
    return {"path": str(archive), "sha256": sha256(archive), "revision": git_revision(root)}


def manifest_file_entries(root: pathlib.Path, relative_root: pathlib.Path, role: str) -> list[dict[str, str]]:
    base = root / relative_root
    if not base.is_dir():
        raise EvidenceFailure(f"build input directory is missing: {relative_root}")
    entries: list[dict[str, str]] = []
    for path in sorted(base.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(root)
        if relative.parts and relative.parts[0] in {"evidence", "artifacts", "runs"}:
            continue
        if any(part in {"evidence", "artifacts", "runs"} for part in relative.parts):
            continue
        entries.append({"path": str(relative), "sha256": sha256(path), "role": role})
    return entries


def input_manifest(root: pathlib.Path, module: pathlib.Path, evidence: pathlib.Path) -> dict[str, Any]:
    entries: list[dict[str, str]] = []
    fixture_prefix = str(MODULE_PATH / "fixtures") + "/"
    entries.extend(
        entry for entry in manifest_file_entries(root, MODULE_PATH, "consumer")
        if not entry["path"].startswith(fixture_prefix)
    )
    for workspace_module in WORKSPACE_MODULES:
        entries.extend(manifest_file_entries(root, workspace_module, "workspace-module"))
    for name in ("go.work", "go.work.sum", "Makefile"):
        path = root / name
        if path.is_file():
            entries.append({"path": name, "sha256": sha256(path), "role": "workspace-config"})
    for path in sorted(FIXTURES.glob("*.json")):
        entries.append({"path": str(path.relative_to(root)), "sha256": sha256(path), "role": "fixture"})
    entries.sort(key=lambda entry: entry["path"])
    manifest = {
        "schema": "c36-build-inputs-v2",
        "module": str(MODULE_PATH),
        "workspace_modules": [str(path) for path in WORKSPACE_MODULES],
        "files": entries,
    }
    manifest["manifest_sha256"] = hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()
    write_json(evidence / "artifacts" / "input-manifest.json", manifest)
    return manifest


def resolve_binary(value: str | None, default: pathlib.Path) -> pathlib.Path:
    path = pathlib.Path(value) if value else default
    if not path.is_absolute():
        path = (pathlib.Path.cwd() / path).resolve()
    if not path.is_file():
        raise EvidenceFailure(f"binary is missing: {path}")
    return path


def collect_toolchains(
    root: pathlib.Path,
    module: pathlib.Path,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    contexts = {
        "room_media": (module, {**os.environ, "GOWORK": "off"}),
        "yui": (root, dict(os.environ)),
    }
    toolchains: dict[str, Any] = {}
    for name, (cwd, env) in contexts.items():
        version = run_process(
            f"toolchain-{name}-version",
            ["rtk", "proxy", "go", "version"],
            cwd,
            evidence / "runs",
            None,
            deadline,
            bounded_remaining(deadline, child_timeout),
            env,
        )
        require_ok(version)
        go_env = run_process(
            f"toolchain-{name}-env",
            [
                "rtk", "proxy", "go", "env", "-json",
                "GOVERSION", "GOROOT", "GOOS", "GOARCH", "CGO_ENABLED",
                "GOFLAGS", "GOWORK", "GOMOD",
            ],
            cwd,
            evidence / "runs",
            None,
            deadline,
            bounded_remaining(deadline, child_timeout),
            env,
        )
        require_ok(go_env)
        try:
            env_data = json.loads(pathlib.Path(go_env["stdout_path"]).read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"{name} go env did not emit JSON: {exc}") from exc
        toolchains[name] = {
            "cwd": str(cwd.relative_to(root)),
            "go_version": pathlib.Path(version["stdout_path"]).read_text(encoding="utf-8").strip(),
            "go_env": env_data,
            "environment": selected_environment(env),
            "version_run": version["label"],
            "env_run": go_env["label"],
        }
    write_json(evidence / "artifacts" / "toolchain.json", toolchains)
    return toolchains


def prepare_binaries(
    root: pathlib.Path,
    module: pathlib.Path,
    evidence: pathlib.Path,
    build_context: dict[str, Any],
    room_binary: str | None,
    yui_binary: str | None,
    no_build: bool,
    need_yui: bool,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    artifacts = evidence / "artifacts"
    artifacts.mkdir(parents=True, exist_ok=True)
    build_path = artifacts / "build.json"
    common = {
        "schema": "c36-build-binding-v2",
        "source_revision": build_context["source_revision"],
        "source_archive": build_context["source_archive"],
        "source_archive_sha256": build_context["source_archive"]["sha256"],
        "input_manifest_sha256": build_context["input_manifest_sha256"],
        "toolchains": build_context["toolchains"],
        "flags": BUILD_FLAGS,
        "packages": BUILD_PACKAGES,
        "workspace_modules": [str(path) for path in WORKSPACE_MODULES],
    }
    if no_build:
        if not build_path.is_file():
            raise EvidenceFailure("--no-build requires an existing verified artifacts/build.json binding")
        previous = load_json(build_path)
        if previous.get("schema") != common["schema"]:
            raise EvidenceFailure("--no-build build.json is not a C36 verified build binding")
        for key in ("source_revision", "source_archive_sha256", "input_manifest_sha256", "toolchains", "flags", "packages", "workspace_modules"):
            if previous.get(key) != common[key]:
                raise EvidenceFailure(f"--no-build binding does not match current inputs: {key}")
        previous_archive = previous.get("source_archive")
        if not isinstance(previous_archive, dict) or previous_archive.get("sha256") != common["source_archive"]["sha256"]:
            raise EvidenceFailure("--no-build source archive is not bound to the current inputs")
        verified_from = sha256(build_path)
        build = dict(previous)
        build["mode"] = "external-verified"
        build["verified_from_build_record_sha256"] = verified_from
        build["verification"] = {
            "source_revision": common["source_revision"],
            "source_archive_sha256": common["source_archive_sha256"],
            "input_manifest_sha256": common["input_manifest_sha256"],
        }
        binaries: dict[str, pathlib.Path] = {}
        for name, supplied in (("room_media", room_binary), ("yui", yui_binary)):
            if name == "yui" and not need_yui:
                continue
            prior_binary = build.get("binaries", {}).get(name)
            if not isinstance(prior_binary, dict) or prior_binary.get("source") not in {"built", "external-verified"}:
                raise EvidenceFailure(f"--no-build has no verified binary binding for {name}")
            default = pathlib.Path(str(prior_binary.get("path", "")))
            candidate = resolve_binary(supplied, default)
            if candidate.resolve() != default.resolve():
                raise EvidenceFailure(f"--no-build binary {name} is not the path from verified build.json")
            if sha256(candidate) != prior_binary.get("sha256"):
                raise EvidenceFailure(f"--no-build binary {name} hash does not match verified build.json")
            binaries[name] = candidate
        write_json(build_path, build)
        return {"room": binaries["room_media"], "yui": binaries.get("yui"), "build": build}

    build = {**common, "mode": "built", "binaries": {}}
    room = artifacts / "room-media"
    room_command = ["rtk", "proxy", "go", "build", *BUILD_FLAGS["room_media"], "-o", str(room), BUILD_PACKAGES["room_media"]]
    room_result = run_process(
        "build-room-media",
        room_command,
        module,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(room_result)
    build["binaries"]["room_media"] = {
        "path": str(room), "sha256": sha256(room), "source": "built",
        "package": BUILD_PACKAGES["room_media"], "flags": BUILD_FLAGS["room_media"],
        "build_command": room_command, "build_run": room_result["label"],
        "toolchain": build_context["toolchains"]["room_media"],
    }

    yui: pathlib.Path | None = None
    if need_yui:
        yui_command = ["rtk", "proxy", "go", "build", *BUILD_FLAGS["yui"], "-o", str(artifacts / "yui"), BUILD_PACKAGES["yui"]]
        yui = artifacts / "yui"
        yui_result = run_process(
            "build-yui",
            yui_command,
            root,
            evidence / "runs",
            None,
            deadline,
            bounded_remaining(deadline, child_timeout),
            dict(os.environ),
        )
        require_ok(yui_result)
        build["binaries"]["yui"] = {
            "path": str(yui), "sha256": sha256(yui), "source": "built",
            "package": BUILD_PACKAGES["yui"], "flags": BUILD_FLAGS["yui"],
            "build_command": yui_command, "build_run": yui_result["label"],
            "toolchain": build_context["toolchains"]["yui"],
        }
    write_json(build_path, build)
    return {"room": room, "yui": yui, "build": build}


def room_run(
    binary: pathlib.Path,
    mode: str,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> tuple[dict[str, Any], dict[str, Any]]:
    output_dir = evidence / "runs" / f"room-{mode}"
    payload = json.dumps({"mode": mode, "output_dir": str(output_dir)}, separators=(",", ":")).encode() + b"\n"
    result = run_process(
        f"room-{mode}",
        [str(binary)],
        binary.parent,
        evidence / "runs",
        payload,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        report = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"room-{mode} did not emit one JSON report: {exc}") from exc
    if not isinstance(report, dict) or report.get("status") != "complete":
        raise EvidenceFailure(f"room-{mode} report is not complete: {report!r}")
    write_json(evidence / "artifacts" / f"room-{mode}.report.json", report)
    return report, result


def expect_equal(label: str, actual: Any, expected: Any) -> None:
    if actual != expected:
        raise EvidenceFailure(f"{label}: got {actual!r}, want {expected!r}")


def normalized_frame(value: dict[str, Any]) -> dict[str, Any]:
    return {
        "epoch": value.get("epoch", 0),
        "sequence": value.get("sequence", 0),
        "samples": value.get("samples", []),
        "end_of_response": value.get("end_of_response", False),
    }


def all_zero_samples(samples: Any) -> bool:
    return isinstance(samples, list) and all(sample == 0 for sample in samples)


def validate_raw_observations(report: dict[str, Any]) -> None:
    raw_events = report.get("raw_events")
    if not isinstance(raw_events, list) or not raw_events:
        raise EvidenceFailure("raw media observation ledger is missing")
    if any(event.get("after_terminal") is True for event in raw_events if isinstance(event, dict)):
        raise EvidenceFailure("raw media ledger contains an event after terminal")

    expected_inputs = {
        "alice": [
            {"epoch": 1, "sequence": 1, "samples": [101, 102], "end_of_response": False},
            {"epoch": 2, "sequence": 2, "samples": [111, 112, 113, 114], "end_of_response": True},
        ],
        "bob": [
            {"epoch": 1, "sequence": 1, "samples": [201, 202], "end_of_response": False},
            {"epoch": 2, "sequence": 2, "samples": [211, 212, 213, 214], "end_of_response": True},
        ],
    }
    for participant, expected in expected_inputs.items():
        source_events = [
            normalized_frame(event) for event in raw_events
            if isinstance(event, dict)
            and event.get("kind") == "source_audio"
            and event.get("participant") == participant
        ]
        expect_equal(f"raw {participant} source frames", source_events, expected)

    expected_outputs = {
        "alice": [211, 212, 213, 214],
        "bob": [111, 112, 113, 114],
    }
    terminal_indexes: dict[str, int] = {}
    source_end_indexes: dict[str, int] = {}
    output_end_indexes: dict[str, int] = {}
    for index, event in enumerate(raw_events):
        if not isinstance(event, dict):
            raise EvidenceFailure(f"raw event {index} is not an object")
        participant = event.get("participant")
        if event.get("kind") == "source_audio" and event.get("end_of_response") is True:
            source_end_indexes[participant] = index
        if event.get("kind") == "peer_output":
            if event.get("target") != participant:
                raise EvidenceFailure(f"peer output {index} has inconsistent participant/target")
            if event.get("end_of_response") is True:
                output_end_indexes[participant] = index
        if event.get("kind") == "terminal":
            terminal_indexes[participant] = index

    for target, expected_samples in expected_outputs.items():
        peer_events = [
            event for event in raw_events
            if isinstance(event, dict)
            and event.get("kind") == "peer_output"
            and event.get("participant") == target
        ]
        if not peer_events:
            raise EvidenceFailure(f"raw peer output for {target} is missing")
        nonempty = [
            event.get("samples") for event in peer_events
            if isinstance(event.get("samples"), list)
            and event.get("samples")
            and not all_zero_samples(event.get("samples"))
        ]
        expect_equal(f"raw {target} nonzero peer output", nonempty, [expected_samples])
        if not any(event.get("end_of_response") is True for event in peer_events):
            raise EvidenceFailure(f"raw peer output for {target} lacks end_of_response")
        source_samples = {
            tuple(frame["samples"])
            for frame in expected_inputs[target]
        }
        if any(tuple(samples) in source_samples for samples in nonempty):
            raise EvidenceFailure(f"raw peer output for {target} contains self-echo source PCM")
        if target not in source_end_indexes or target not in output_end_indexes or target not in terminal_indexes:
            raise EvidenceFailure(f"raw {target} observation is missing source/output/terminal ordering")
        if not source_end_indexes[target] < terminal_indexes[target]:
            raise EvidenceFailure(f"raw {target} source end is not before terminal")
        if not output_end_indexes[target] < terminal_indexes[target]:
            raise EvidenceFailure(f"raw {target} output end is not before terminal")

    playback = report.get("playback")
    observed = playback.get("observed") if isinstance(playback, dict) else None
    if not isinstance(observed, list) or not observed:
        raise EvidenceFailure("software playback did not retain observed public frames")
    nonempty_observed = [
        normalized_frame(frame) for frame in observed
        if isinstance(frame, dict)
        and isinstance(frame.get("samples"), list)
        and frame.get("samples")
        and not all_zero_samples(frame.get("samples"))
    ]
    expect_equal("software playback observed nonzero frames", nonempty_observed, [{
        "epoch": 1, "sequence": 0, "samples": [322, 324, 326, 328], "end_of_response": False,
    }])


def validate_playback_boundary(report: dict[str, Any]) -> None:
    boundary = report.get("playback_boundary")
    if not isinstance(boundary, dict):
        raise EvidenceFailure("playback boundary evidence is missing")
    expected = {
        "domain": "software-playback-boundary",
        "admitted": [41, 42],
        "consumed": [41, 42, 0, 0],
        "underflow": [0, 0],
        "discarded_stale": [7, 8, 9],
        "rendered_before_callback": 0,
        "callback_count": 1,
        "rendered_samples": 4,
        "underflow_samples": 2,
        "discarded_samples": 3,
        "admitted_samples": 2,
        "consumed_samples": 2,
        "zero_filled_samples": 2,
        "physical_device": False,
    }
    for key, value in expected.items():
        expect_equal(f"playback boundary {key}", boundary.get(key), value)
    if not isinstance(boundary.get("capability_gap"), str) or "no physical playback device" not in boundary["capability_gap"]:
        raise EvidenceFailure("playback boundary did not disclose the physical-device capability gap")


def validate_report(report: dict[str, Any], require_boundary: bool = True) -> None:
    expect_equal("schema version", report.get("schema_version"), 1)
    expect_equal("status", report.get("status"), "complete")
    expected_inputs = [
        {"participant": "alice", "epoch": 1, "sequence": 1, "samples": [101, 102], "end_of_response": False},
        {"participant": "alice", "epoch": 2, "sequence": 2, "samples": [111, 112, 113, 114], "end_of_response": True},
        {"participant": "bob", "epoch": 1, "sequence": 1, "samples": [201, 202], "end_of_response": False},
        {"participant": "bob", "epoch": 2, "sequence": 2, "samples": [211, 212, 213, 214], "end_of_response": True},
    ]
    expect_equal("source input order", report.get("source_inputs"), expected_inputs)
    expect_equal("peer-only outputs", report.get("peer_outputs"), {"alice": [211, 212, 213, 214], "bob": [111, 112, 113, 114]})
    expect_equal("epoch stale pending samples", report.get("epochs", {}).get("stale_pending_samples"), 4)
    expect_equal("epoch healthy tail", report.get("epochs", {}).get("healthy_tail"), [111, 112, 113, 114])
    expect_equal("end precedes terminal", report.get("epochs", {}).get("end_of_response_before_terminal"), True)
    expect_equal("provider frame admission", report.get("provider_admission"), {"frames": 4, "samples": 12})
    playback = report.get("playback", {})
    expect_equal("listener playback domain", playback.get("domain"), "software-playback")
    expect_equal("listener admitted PCM", playback.get("admitted"), [322, 324, 326, 328])
    expect_equal("listener consumed PCM", playback.get("consumed"), [322, 324, 326, 328])
    expect_equal("listener underflow", playback.get("underflow"), [0, 0])
    expect_equal("listener callback count", playback.get("callback_count"), 2)
    expect_equal("listener rendered samples", playback.get("rendered_samples"), 6)
    expect_equal("listener underflow samples", playback.get("underflow_samples"), 2)
    expect_equal("listener consumed samples", playback.get("consumed_samples"), 4)
    expect_equal("listener zero-filled samples", playback.get("zero_filled_samples"), 2)
    expect_equal("listener stale discard", playback.get("discarded_stale"), [7, 8, 9])
    expect_equal("listener discarded samples", playback.get("discarded_samples"), 3)
    expect_equal("listener physical device", playback.get("physical_device"), False)
    if not isinstance(playback.get("capability_gap"), str) or "no physical" not in playback["capability_gap"]:
        raise EvidenceFailure("listener playback did not disclose software-only evidence")
    terminals = report.get("terminal")
    expected_terminal = {
        "kind": "terminal",
        "sequence": 99,
        "reason": "fixture_complete",
        "classification": "fixture_complete",
        "terminal_reason": "provider_close",
        "provenance": "provider",
        "output_state": "complete",
    }
    expect_equal("terminal participants", sorted(terminals or {}), ["alice", "bob"])
    expect_equal("alice terminal", terminals.get("alice"), expected_terminal)
    expect_equal("bob terminal", terminals.get("bob"), expected_terminal)
    expect_equal("lifecycle", report.get("lifecycle"), {
        "opened": 2, "started": 2, "waited": 2, "closed": 2,
        "repeated_close_ok": True, "workers_joined": True, "natural_exit": True,
    })
    expect_equal("recording", report.get("recording"), {
        "state": "partial", "provider_trace": "unavailable",
        "reason": "fixture live handles intentionally do not emit provider capture artifacts",
        "pcm_bytes": 24, "replayable": False,
    })
    expect_equal("replay rejected", report.get("replay_rejected"), True)
    if "run-manifest.json" not in str(report.get("replay_reject_reason", "")):
        raise EvidenceFailure("partial recording was not rejected with a missing bundle artifact")
    room = report.get("room", {})
    expect_equal("room termination", room.get("termination_reason"), "stopped")
    expect_equal("room participants", room.get("participants"), {"alice": "ended", "bob": "ended", "listener": "ended"})
    validate_raw_observations(report)
    if require_boundary:
        validate_playback_boundary(report)


def dependency_gate(root: pathlib.Path, module: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    result = run_process(
        "go-list-deps",
        ["rtk", "proxy", "go", "list", "-deps", "./cmd/room-media"],
        module,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(result)
    deps = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").splitlines()
    modules_result = run_process(
        "go-list-modules",
        ["rtk", "proxy", "go", "list", "-m", "all"],
        module,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    require_ok(modules_result)
    modules = pathlib.Path(modules_result["stdout_path"]).read_text(encoding="utf-8").splitlines()
    local_replacements = [
        line for line in modules
        if any(f"github.com/portpowered/go-agent-harness/{name}" in line and "=>" in line for name in (
            "go-agent-loop", "go-agent-runtime", "go-audio", "go-device-gateway", "go-llm-gateway",
        ))
    ]
    if len(local_replacements) != 5:
        raise EvidenceFailure(f"standalone module did not resolve all local replacements: {local_replacements}")
    if any("/agent-cli" in line or line.endswith("agent-cli") for line in deps):
        raise EvidenceFailure("GOWORK=off dependency graph unexpectedly includes agent-cli")
    source_files = sorted(module.rglob("*.go"))
    private_imports: list[dict[str, Any]] = []
    for path in source_files:
        for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if '"' in line and ("/internal/" in line or '"os/exec"' in line):
                private_imports.append({"path": str(path.relative_to(root)), "line": line_number, "text": line.strip()})
    if private_imports:
        raise EvidenceFailure(f"consumer imports private/CLI execution APIs: {private_imports}")
    outcome = {
        "deps": deps, "modules": modules, "local_replacements": local_replacements,
        "agent_cli_absent": True, "private_imports": private_imports,
        "go_list": result, "go_list_modules": modules_result,
    }
    write_json(evidence / "artifacts" / "dependency-gate.json", outcome)
    return outcome


PARITY_EXPECTATIONS: dict[str, dict[str, Any]] = {
    "audio-tool": {
        "fixture_sha256": TOOL_FIXTURE_SHA256,
        "pcm_bytes": 4800,
        "pcm_sha256": TOOL_PCM_SHA256,
        "terminal": {
            "reason": "fixture_complete",
            "classification": "provider_close",
            "terminal_reason": "provider_close",
            "terminal_provenance": "provider",
            "output_state": "not_applicable",
        },
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_item.added", "response.function_call_arguments.done",
            "response.done", "conversation.item.create", "response.create", "response.created",
            "response.output_audio.delta", "response.output_audio.delta", "response.output_audio.done",
            "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
        ],
    },
    "interruption": {
        "fixture_sha256": INTERRUPTION_FIXTURE_SHA256,
        "pcm_bytes": 3840,
        "pcm_sha256": INTERRUPTION_PCM_SHA256,
        "healthy_tail_offset_bytes": 1440,
        "healthy_tail_bytes": 2400,
        "healthy_tail_sha256": HEALTHY_TAIL_SHA256,
        "terminal": {
            "reason": "replay_complete",
            "classification": "replay_complete",
            "terminal_reason": "replay_complete",
            "terminal_provenance": "replay",
            "output_state": "complete",
        },
        "fixture_types": [
            "session.update", "session.created", "conversation.item.create", "response.create",
            "response.created", "response.output_audio.delta", "input_audio_buffer.speech_started",
            "conversation.item.truncate", "conversation.item.truncated", "response.output_audio.done",
            "response.done", "response.created", "response.output_audio.delta",
            "response.output_audio.done", "response.done",
        ],
    },
}


def load_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError) as exc:
        raise EvidenceFailure(f"invalid JSON artifact {path}: {exc}") from exc


def load_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    if not path.is_file():
        raise EvidenceFailure(f"missing JSONL artifact: {path}")
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvidenceFailure(f"invalid JSONL at {path}:{line_number}: {exc}") from exc
        if not isinstance(value, dict):
            raise EvidenceFailure(f"non-object JSONL record at {path}:{line_number}")
        records.append(value)
    if not records:
        raise EvidenceFailure(f"empty JSONL artifact: {path}")
    return records


def validate_parity_recording(label: str, fixture: pathlib.Path, record_dir: pathlib.Path, pcm: pathlib.Path, manifest: pathlib.Path) -> dict[str, Any]:
    expectation = PARITY_EXPECTATIONS[label]
    manifest_data = load_json(manifest)
    expect_equal(f"{label} terminal", manifest_data.get("terminal"), expectation["terminal"])
    artifacts = {
        artifact.get("path"): artifact.get("sha256")
        for artifact in manifest_data.get("artifacts", [])
        if isinstance(artifact, dict)
    }
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    expect_equal(f"{label} manifest artifact set", set(artifacts), expected_paths)
    for relative in expected_paths:
        artifact_path = record_dir / relative
        if not artifact_path.is_file() or artifacts[relative] != sha256(artifact_path):
            raise EvidenceFailure(f"{label} manifest hash does not match {relative}")
    for transcript_name in ("client.transcript.jsonl", "agent.transcript.jsonl"):
        transcript = load_jsonl(record_dir / transcript_name)
        ticks = [item.get("tick") for item in transcript]
        if ticks != list(range(1, len(ticks) + 1)):
            raise EvidenceFailure(f"{label} {transcript_name} ticks are not contiguous: {ticks}")
    fixture_data = load_json(fixture)
    provider = record_dir / "provider.json"
    expect_equal(f"{label} provider fixture bytes", sha256(provider), sha256(fixture))
    expect_equal(f"{label} provider event types", [item.get("type") for item in fixture_data.get("records", [])], expectation["fixture_types"])
    pcm_bytes = pcm.read_bytes()
    expect_equal(f"{label} PCM byte count", len(pcm_bytes), expectation["pcm_bytes"])
    expect_equal(f"{label} PCM hash", sha256(pcm), expectation["pcm_sha256"])
    healthy_tail: dict[str, Any] = {}
    if "healthy_tail_offset_bytes" in expectation:
        offset = expectation["healthy_tail_offset_bytes"]
        tail = pcm_bytes[offset:]
        healthy_tail = {"offset_bytes": offset, "bytes": len(tail), "sha256": hashlib.sha256(tail).hexdigest()}
        expect_equal(f"{label} healthy tail", healthy_tail, {
            "offset_bytes": offset,
            "bytes": expectation["healthy_tail_bytes"],
            "sha256": expectation["healthy_tail_sha256"],
        })
    session_log = load_jsonl(record_dir / "session-log.jsonl")
    if label == "audio-tool":
        expect_equal("audio-tool session turn count", len(session_log), 1)
        expect_equal("audio-tool session input", session_log[0].get("input"), {
            "text": "probe PROBE_TOOL_MARKER_9182", "audio_offset_bytes": 0, "audio_bytes": 0, "committed": False,
        })
        expect_equal("audio-tool session response", session_log[0].get("response"), {
            "text": "strict replay continuation", "complete": True, "audio_offset_bytes": 0,
            "audio_bytes": 4800, "audio_segments": ["audio/out-000.pcm"],
        })
        tool_events = session_log[0].get("tool_events")
        if not isinstance(tool_events, list) or len(tool_events) != 2:
            raise EvidenceFailure("audio-tool tool event sequence is missing")
        if "PROBE_TOOL_MARKER_9182" not in json.dumps(tool_events):
            raise EvidenceFailure("audio-tool tool marker is missing from the replay trace")
    else:
        expect_equal("interruption session turn count", len(session_log), 2)
        expect_equal("interruption first response", session_log[0].get("response"), {
            "text": "", "complete": True, "audio_offset_bytes": 0,
            "audio_bytes": 1440, "audio_segments": ["audio/out-000.pcm"],
        })
        expect_equal("interruption healthy response", session_log[1].get("response"), {
            "text": "", "complete": True, "audio_offset_bytes": 1440,
            "audio_bytes": 2400, "audio_segments": ["audio/out-000.pcm"],
        })
        if any(item.get("tool_events") is not None for item in session_log):
            raise EvidenceFailure("interruption replay unexpectedly contains tool events")
    return {"terminal": manifest_data["terminal"], "healthy_tail": healthy_tail, "session_log": str(record_dir / "session-log.jsonl")}


def parity_run(
    label: str,
    fixture: pathlib.Path,
    yui: pathlib.Path,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    case_dir = evidence / "runs" / f"parity-{label}"
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    record_dir = case_dir / "tool-record"
    audio_out = case_dir / "audio.wav"
    result = run_process(
        f"yui-{label}",
        [
            str(yui), "--config-dir", str(case_dir / "config"), "--workdir", str(case_dir),
            "--allow-path", str(case_dir), "session", "--replay", str(fixture),
            "--audio-out", str(audio_out), "--record-dir", str(record_dir), "--trace-audio",
        ],
        case_dir,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        dict(os.environ),
    )
    require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8")
    if label == "audio-tool":
        if "PROBE_TOOL_MARKER_9182" not in stdout or "strict replay continuation" not in stdout:
            raise EvidenceFailure("audio-tool replay did not preserve marker and continuation output")
        marker = case_dir / "evidence" / "runs" / "exec-invocations-v4.log"
        if not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8"):
            raise EvidenceFailure("audio-tool replay did not preserve executable tool evidence")
    elif "replay mismatch" in stdout.lower():
        raise EvidenceFailure("interruption replay reported a mismatch")
    manifest = record_dir / "manifest.json"
    pcm = record_dir / "audio" / "out-000.pcm"
    if not manifest.is_file() or not pcm.is_file() or pcm.stat().st_size == 0:
        raise EvidenceFailure(f"{label} replay did not produce complete recording artifacts")
    parity = validate_parity_recording(label, fixture, record_dir, pcm, manifest)
    replay_case = case_dir / "generated-bundle-replay"
    replay_case.mkdir(parents=True, exist_ok=True)
    (replay_case / "config").mkdir(parents=True, exist_ok=True)
    generated_replay = run_process(
        f"yui-{label}-generated-bundle",
        [
            str(yui), "--config-dir", str(replay_case / "config"),
            "--workdir", str(replay_case), "--allow-path", str(replay_case),
            "session", "replay", str(record_dir),
        ],
        replay_case,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        dict(os.environ),
    )
    require_ok(generated_replay)
    replay_output = (
        pathlib.Path(generated_replay["stdout_path"]).read_text(encoding="utf-8")
        + pathlib.Path(generated_replay["stderr_path"]).read_text(encoding="utf-8")
    )
    replay_match = re.search(r"Replay verified:\s*(\d+) wire events,\s*(\d+) tool calls", replay_output)
    if replay_match is None:
        raise EvidenceFailure(f"{label} generated bundle replay did not emit strict verification evidence")
    replay_counts = {
        "wire_events": int(replay_match.group(1)),
        "tool_calls": int(replay_match.group(2)),
    }
    if replay_counts["wire_events"] <= 0:
        raise EvidenceFailure(f"{label} generated bundle replay verified no wire events")
    if label == "audio-tool" and replay_counts["tool_calls"] != 1:
        raise EvidenceFailure(f"audio-tool generated bundle replay tool-call count = {replay_counts['tool_calls']}, want 1")
    strict_replay = {
        "command": generated_replay["argv"],
        "bundle": str(record_dir),
        "counts": replay_counts,
        "verified": True,
        "run": generated_replay,
    }
    summary = {
        "label": label,
        "fixture": str(fixture),
        "fixture_sha256": sha256(fixture),
        "stdout_path": result["stdout_path"],
        "stderr_path": result["stderr_path"],
        "manifest": str(manifest),
        "manifest_sha256": sha256(manifest),
        "pcm": str(pcm),
        "pcm_bytes": pcm.stat().st_size,
        "pcm_sha256": sha256(pcm),
        "audio_out": str(audio_out),
        "audio_out_bytes": audio_out.stat().st_size if audio_out.exists() else 0,
        "terminal": parity["terminal"],
        "healthy_tail": parity["healthy_tail"],
        "strict_bundle_replay": strict_replay,
    }
    write_json(case_dir / "summary.json", summary)
    return summary


def expect_child_failure(
    label: str,
    binary: pathlib.Path,
    payload: bytes,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    result = run_process(
        label,
        [str(binary)],
        binary.parent,
        evidence / "runs",
        payload,
        deadline,
        bounded_remaining(deadline, child_timeout),
        {**os.environ, "GOWORK": "off"},
    )
    if (
        result["timed_out"]
        or result["exit_code"] == 0
        or not result["reaped"]
        or not result["process_group_gone"]
        or not result["output_drained"]
    ):
        raise EvidenceFailure(f"{label} was accepted or was not bounded: {result}")
    return result


def run_boundary(
    root: pathlib.Path,
    module: pathlib.Path,
    room: pathlib.Path,
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    report, positive = room_run(room, "positive", evidence, deadline, child_timeout)
    validate_report(report)
    dependency = dependency_gate(root, module, evidence, deadline, child_timeout)
    negatives = [
        expect_child_failure(
            "room-unknown-field",
            room,
            json.dumps({"mode": "positive", "output_dir": str(evidence / "runs" / "negative-unknown"), "unknown": True}).encode() + b"\n",
            evidence, deadline, child_timeout,
        ),
        expect_child_failure(
            "room-trailing-json",
            room,
            json.dumps({"mode": "positive", "output_dir": str(evidence / "runs" / "negative-trailing")}).encode() + b"\n{}\n",
            evidence, deadline, child_timeout,
        ),
        expect_child_failure(
            "room-missing-output",
            room,
            b'{"mode":"positive"}\n',
            evidence, deadline, child_timeout,
        ),
    ]
    outcome = {"positive": positive, "report": report, "dependency": dependency, "negative_controls": negatives}
    write_json(evidence / "artifacts" / "boundary.json", outcome)
    return outcome


def run_provenance(
    root: pathlib.Path,
    module: pathlib.Path,
    evidence: pathlib.Path,
    build: dict[str, Any],
) -> dict[str, Any]:
    if build.get("schema") != "c36-build-binding-v2":
        raise EvidenceFailure("build record is not the C36 input-bound schema")
    source_archive_data = build.get("source_archive")
    if not isinstance(source_archive_data, dict):
        raise EvidenceFailure("build record is missing source archive binding")
    source_archive = pathlib.Path(str(source_archive_data.get("path", "")))
    manifest = load_json(evidence / "artifacts" / "input-manifest.json")
    if not source_archive.is_file():
        raise EvidenceFailure("source archive is missing")
    expected_revision = git_revision(root)
    expect_equal("build source revision", build.get("source_revision"), expected_revision)
    expect_equal("archive source revision", source_archive_data.get("revision"), expected_revision)
    expected_archive_hash = build.get("source_archive_sha256")
    if not isinstance(expected_archive_hash, str) or source_archive_data.get("sha256") != expected_archive_hash or sha256(source_archive) != expected_archive_hash:
        raise EvidenceFailure("source archive hash does not match the admitted build record")
    if manifest.get("schema") != "c36-build-inputs-v2":
        raise EvidenceFailure("input manifest schema is not bound to C36")
    manifest_hash = manifest.get("manifest_sha256")
    manifest_without_hash = {key: value for key, value in manifest.items() if key != "manifest_sha256"}
    if not isinstance(manifest_hash, str) or hashlib.sha256(json.dumps(manifest_without_hash, sort_keys=True).encode()).hexdigest() != manifest_hash:
        raise EvidenceFailure("input manifest self-hash is invalid")
    expect_equal("build input manifest hash", build.get("input_manifest_sha256"), manifest_hash)
    expect_equal("build flags", build.get("flags"), BUILD_FLAGS)
    expect_equal("build packages", build.get("packages"), BUILD_PACKAGES)
    expect_equal("build workspace modules", build.get("workspace_modules"), [str(path) for path in WORKSPACE_MODULES])
    files = manifest.get("files")
    if not isinstance(files, list) or not files:
        raise EvidenceFailure("input manifest has no files")
    paths = [entry.get("path") for entry in files if isinstance(entry, dict)]
    if len(paths) != len(set(paths)):
        raise EvidenceFailure("input manifest contains duplicate paths")
    for entry in manifest.get("files", []):
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str) or not isinstance(entry.get("sha256"), str):
            raise EvidenceFailure(f"invalid input manifest entry: {entry!r}")
        path = root / entry["path"]
        if not path.is_file() or sha256(path) != entry["sha256"]:
            raise EvidenceFailure(f"input hash changed after admission: {entry['path']}")
    with tarfile.open(source_archive, "r") as archive:
        archive_files = sorted(member.name for member in archive.getmembers() if member.isfile())
    expect_equal("source archive input set", archive_files, sorted(paths))
    fixture_hashes = {
        path.name: sha256(path)
        for path in sorted(FIXTURES.glob("*.json"))
    }
    expect_equal("tool fixture hash", fixture_hashes.get("c16-audio-tool.session.json"), TOOL_FIXTURE_SHA256)
    expect_equal("interruption fixture hash", fixture_hashes.get("c16-interruption.session.json"), INTERRUPTION_FIXTURE_SHA256)

    binaries = build.get("binaries")
    if not isinstance(binaries, dict) or "room_media" not in binaries:
        raise EvidenceFailure("build record has no room-media binary binding")
    for name, binary in binaries.items():
        if not isinstance(binary, dict):
            raise EvidenceFailure(f"binary binding for {name} is not an object")
        path = pathlib.Path(str(binary.get("path", "")))
        expected_hash = binary.get("sha256")
        if not path.is_file() or not isinstance(expected_hash, str) or sha256(path) != expected_hash:
            raise EvidenceFailure(f"binary hash does not match the admitted build record: {name}")
        expect_equal(f"{name} package", binary.get("package"), BUILD_PACKAGES[name])
        expect_equal(f"{name} flags", binary.get("flags"), BUILD_FLAGS[name])
        if binary.get("source") not in {"built", "external-verified"}:
            raise EvidenceFailure(f"binary {name} was not produced by a verified build")
    if build.get("mode") == "external-verified" and not isinstance(build.get("verified_from_build_record_sha256"), str):
        raise EvidenceFailure("external binary run is missing its prior verified build-record binding")
    toolchains = build.get("toolchains")
    if not isinstance(toolchains, dict) or set(toolchains) != {"room_media", "yui"}:
        raise EvidenceFailure("build record is missing the selected room/yui toolchain contexts")
    for name, context in toolchains.items():
        if not isinstance(context, dict) or not isinstance(context.get("go_version"), str) or not context["go_version"].startswith("go"):
            raise EvidenceFailure(f"toolchain context is incomplete for {name}")
        if not isinstance(context.get("go_env"), dict) or not isinstance(context.get("environment"), dict):
            raise EvidenceFailure(f"toolchain environment is incomplete for {name}")

    def reject_mutated_file(label: str, original: pathlib.Path, expected_hash: str) -> dict[str, Any]:
        with tempfile.TemporaryDirectory(prefix="c36-provenance-") as temp:
            mutated = pathlib.Path(temp) / original.name
            shutil.copyfile(original, mutated)
            with mutated.open("ab") as handle:
                handle.write(b"\nC36_MUTATION_CONTROL\n")
            mutated_hash = sha256(mutated)
            if mutated_hash == expected_hash:
                raise EvidenceFailure(f"{label} mutation did not change the bound hash")
            try:
                if sha256(mutated) != expected_hash:
                    raise EvidenceFailure(f"{label} hash differs from the admitted binding")
            except EvidenceFailure as exc:
                return {"original": str(original), "mutated_sha256": mutated_hash, "rejected": True, "reason": str(exc)}
            raise EvidenceFailure(f"{label} mutation was accepted")

    source_file = root / MODULE_PATH / "scenario.go"
    room_binding = binaries["room_media"]
    mutation_controls = {
        "input_source": reject_mutated_file(
            "consumer source", source_file,
            next(entry["sha256"] for entry in files if entry.get("path") == str(MODULE_PATH / "scenario.go")),
        ),
        "source_archive": reject_mutated_file("source archive", source_archive, expected_archive_hash),
        "room_binary": reject_mutated_file("room binary", pathlib.Path(room_binding["path"]), room_binding["sha256"]),
    }
    if "yui" in binaries:
        yui_binding = binaries["yui"]
        mutation_controls["yui_binary"] = reject_mutated_file(
            "yui binary", pathlib.Path(yui_binding["path"]), yui_binding["sha256"],
        )
    result = {
        "revision": expected_revision,
        "source_archive": {"path": str(source_archive), "sha256": sha256(source_archive)},
        "input_manifest_sha256": manifest_hash,
        "binary_hashes": {name: value.get("sha256") for name, value in binaries.items()},
        "fixture_hashes": fixture_hashes,
        "toolchains": toolchains,
        "flags": BUILD_FLAGS,
        "packages": BUILD_PACKAGES,
        "mutation_controls": mutation_controls,
    }
    write_json(evidence / "artifacts" / "provenance.json", result)
    return result


def run_routing_epochs(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "routing-epochs", evidence, deadline, child_timeout)
    validate_report(report)
    outcome = {"run": run, "report": report}
    write_json(evidence / "artifacts" / "routing-epochs.json", outcome)
    return outcome


def run_mutations(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    cases = ["peer-participant-key", "source-order", "epoch", "pcm", "terminal"]
    rejected: dict[str, Any] = {}
    for label in cases:
        output_dir = evidence / "runs" / f"room-mutation-{label}"
        payload = json.dumps({
            "mode": "mutations", "mutation": label, "output_dir": str(output_dir),
        }, separators=(",", ":")).encode() + b"\n"
        result = expect_child_failure(
            f"room-mutation-{label}", room, payload, evidence, deadline, child_timeout,
        )
        stderr = pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8")
        if "literal oracle mismatch" not in stderr:
            raise EvidenceFailure(f"mutation {label} failed without the rejecting literal oracle: {stderr!r}")
        rejected[label] = {
            "rejected": True,
            "error": stderr.strip(),
            "run": result,
        }
    outcome = {
        "control": "independent room subprocesses",
        "rejected_mutations": list(rejected),
        "runs": rejected,
    }
    write_json(evidence / "artifacts" / "mutations.json", outcome)
    return outcome


def run_partial_recording(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "partial-recording", evidence, deadline, child_timeout)
    validate_report(report)
    output_dir = pathlib.Path(report["output_dir"])
    if (output_dir / "run-manifest.json").exists() or list(output_dir.rglob("provider*.json")):
        raise EvidenceFailure("partial recording unexpectedly exposed a replay/provider trace")
    outcome = {"run": run, "report": report, "provider_trace_files": [], "replay_bundle_present": False}
    write_json(evidence / "artifacts" / "partial-recording.json", outcome)
    return outcome


def run_consumption(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "consumption", evidence, deadline, child_timeout)
    validate_report(report)
    outcome = {"run": run, "report": report, "physical_device_opened": report["playback_boundary"]["physical_device"]}
    write_json(evidence / "artifacts" / "consumption.json", outcome)
    return outcome


def run_lifecycle(room: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    report, run = room_run(room, "lifecycle", evidence, deadline, child_timeout)
    validate_report(report)
    cancellations: dict[str, Any] = {}
    for mode in ("cancel-before-start", "cancel-active"):
        cancellation, cancellation_run = room_run(room, mode, evidence, deadline, child_timeout)
        observed = cancellation.get("cancellation")
        if not isinstance(observed, dict) or observed.get("joined") is not True:
            raise EvidenceFailure(f"{mode} did not prove worker join")
        if observed.get("first_close_error") or observed.get("second_close_error") or observed.get("wait_error"):
            raise EvidenceFailure(f"{mode} close/wait was not idempotent: {observed}")
        if mode == "cancel-before-start" and not observed.get("start_error"):
            raise EvidenceFailure("cancel-before-start unexpectedly started")
        if mode == "cancel-active" and observed.get("start_error"):
            raise EvidenceFailure(f"cancel-active failed before cancellation: {observed}")
        cancellations[mode] = {"run": cancellation_run, "report": cancellation}
    outcome = {"run": run, "report": report, "cancellations": cancellations}
    write_json(evidence / "artifacts" / "lifecycle.json", outcome)
    return outcome


def run_hang_control(evidence: pathlib.Path, deadline: float) -> dict[str, Any]:
    code = (
        "import json,os,signal,subprocess,sys,time; "
        "signal.signal(signal.SIGTERM, signal.SIG_IGN); "
        "child=subprocess.Popen([sys.executable,'-c',\"import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)\"]); "
        "print(json.dumps({'child_pid':child.pid,'child_pgid':os.getpgid(child.pid)}),flush=True); "
        "time.sleep(30)"
    )
    result = run_process(
        "hang-control",
        [sys.executable, "-c", code],
        evidence / "runs",
        evidence / "runs",
        None,
        deadline,
        min(0.25, bounded_remaining(deadline, 0.25)),
        dict(os.environ),
    )
    if (
        not result["timed_out"] or not result["term_sent"] or not result["kill_sent"]
        or not result["reaped"] or not result["process_group_gone"] or not result["output_drained"]
    ):
        raise EvidenceFailure(f"hang control did not prove TERM/KILL/reap: {result}")
    output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8").strip()
    try:
        descendant = json.loads(output.splitlines()[0])
    except (IndexError, json.JSONDecodeError) as exc:
        raise EvidenceFailure("hang control did not start a descendant in the bounded process group") from exc
    if not isinstance(descendant, dict) or not isinstance(descendant.get("child_pid"), int) or not isinstance(descendant.get("child_pgid"), int):
        raise EvidenceFailure(f"hang control descendant evidence is malformed: {descendant!r}")
    if descendant["child_pgid"] != result["process_group_id"]:
        raise EvidenceFailure(f"hang control descendant escaped the process group: {descendant!r} vs {result!r}")
    result["descendant"] = {
        "pid": descendant["child_pid"],
        "process_group_id": descendant["child_pgid"],
        "same_process_group": True,
        "process_group_gone": result["process_group_gone"],
    }
    write_json(evidence / "artifacts" / "hang-control.json", result)
    return result


def run_parity(yui: pathlib.Path, evidence: pathlib.Path, deadline: float, child_timeout: float) -> dict[str, Any]:
    runs = [
        parity_run("audio-tool", FIXTURES / "c16-audio-tool.session.json", yui, evidence, deadline, child_timeout),
        parity_run("interruption", FIXTURES / "c16-interruption.session.json", yui, evidence, deadline, child_timeout),
    ]
    negative_dir = evidence / "runs" / "parity-negative"
    negative_dir.mkdir(parents=True, exist_ok=True)
    bad_fixture = negative_dir / "missing-timeline.session.json"
    data = load_json(FIXTURES / "c16-interruption.session.json")
    records = data.get("records")
    if not isinstance(records, list) or len(records) < 2:
        raise EvidenceFailure("interruption fixture is too short for missing-timeline control")
    data["records"] = records[:-1]
    write_json(bad_fixture, data)
    negative_case = negative_dir / "case"
    negative_case.mkdir(parents=True, exist_ok=True)
    (negative_case / "config").mkdir(parents=True, exist_ok=True)
    negative = run_process(
        "yui-missing-timeline",
        [
            str(yui), "--config-dir", str(negative_case / "config"), "--workdir", str(negative_case),
            "--allow-path", str(negative_case), "session", "--replay", str(bad_fixture),
            "--audio-out", str(negative_case / "audio.wav"), "--record-dir", str(negative_case / "tool-record"),
            "--trace-audio",
        ],
        negative_case,
        evidence / "runs",
        None,
        deadline,
        bounded_remaining(deadline, child_timeout),
        dict(os.environ),
    )
    if (
        negative["timed_out"] or negative["exit_code"] == 0 or not negative["reaped"]
        or not negative["process_group_gone"] or not negative["output_drained"]
    ):
        raise EvidenceFailure(f"missing-timeline replay was not rejected: {negative}")
    outcome = {"parity": runs, "missing_timeline_rejected": negative}
    write_json(evidence / "artifacts" / "parity.json", outcome)
    return outcome


def run_actions(
    action: str,
    root: pathlib.Path,
    module: pathlib.Path,
    binaries: dict[str, Any],
    evidence: pathlib.Path,
    deadline: float,
    child_timeout: float,
) -> dict[str, Any]:
    room = binaries["room"]
    yui = binaries.get("yui")
    results: dict[str, Any] = {}
    selected = [
        "boundary", "provenance", "routing-epochs", "mutations", "partial-recording",
        "consumption", "lifecycle", "hang-control", "parity",
    ] if action == "all" else [action]
    for name in selected:
        if name == "boundary":
            results[name] = run_boundary(root, module, room, evidence, deadline, child_timeout)
        elif name == "provenance":
            results[name] = run_provenance(root, module, evidence, binaries["build"])
        elif name == "routing-epochs":
            results[name] = run_routing_epochs(room, evidence, deadline, child_timeout)
        elif name == "mutations":
            results[name] = run_mutations(room, evidence, deadline, child_timeout)
        elif name == "partial-recording":
            results[name] = run_partial_recording(room, evidence, deadline, child_timeout)
        elif name == "consumption":
            results[name] = run_consumption(room, evidence, deadline, child_timeout)
        elif name == "lifecycle":
            results[name] = run_lifecycle(room, evidence, deadline, child_timeout)
        elif name == "hang-control":
            results[name] = run_hang_control(evidence, deadline)
        elif name == "parity":
            if yui is None:
                raise EvidenceFailure("parity action requires a yui binary")
            results[name] = run_parity(yui, evidence, deadline, child_timeout)
        else:
            raise EvidenceFailure(f"unsupported verifier action {name}")
        write_json(evidence / "artifacts" / "progress.json", {"completed": list(results), "action": action})
    return results


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--action", required=True, choices=[
        "boundary", "provenance", "routing-epochs", "mutations", "partial-recording",
        "consumption", "lifecycle", "hang-control", "parity", "all",
    ])
    parser.add_argument("--source", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--room-binary")
    parser.add_argument("--yui-binary")
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=600.0)
    args = parser.parse_args(argv)
    if args.child_timeout <= 0 or args.aggregate_timeout <= 0:
        parser.error("timeouts must be positive")
    started = time.monotonic()
    deadline = started + args.aggregate_timeout
    root = source_root(pathlib.Path(args.source))
    module = module_dir(root)
    evidence = pathlib.Path(args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    manifest = input_manifest(root, module, evidence)
    archive = build_source_archive(root, manifest, evidence)
    toolchains = collect_toolchains(root, module, evidence, deadline, args.child_timeout)
    build_context = {
        "source_revision": git_revision(root),
        "source_archive": archive,
        "input_manifest_sha256": manifest["manifest_sha256"],
        "toolchains": toolchains,
    }
    need_yui = args.action in ("parity", "all")
    binaries = prepare_binaries(
        root, module, evidence, build_context, args.room_binary, args.yui_binary, args.no_build,
        need_yui, deadline, args.child_timeout,
    )
    outcome = {
        "status": "accepted",
        "action": args.action,
        "elapsed_seconds_before_actions": round(time.monotonic() - started, 6),
        "source": str(root),
        "module": str(module),
        "revision": git_revision(root),
        "source_archive": archive,
        "input_manifest_sha256": manifest["manifest_sha256"],
        "build": binaries["build"],
    }
    outcome["results"] = run_actions(args.action, root, module, binaries, evidence, deadline, args.child_timeout)
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    write_json(evidence / "artifacts" / "verdict.json", outcome)
    print(json.dumps({"status": "accepted", "action": args.action, "evidence": str(evidence), "elapsed_seconds": outcome["elapsed_seconds"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except EvidenceFailure as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
