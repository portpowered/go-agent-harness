#!/usr/bin/env python3
"""Reproduce and verify the C51 audio/device boundary characterization.

The verifier is intentionally standard-library-only.  It keeps subprocesses
bounded, records exact source and authority hashes, and treats queue admission
and device-callback consumption as different observations.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import signal
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import wave
from pathlib import Path


EVIDENCE_ROOT = Path(__file__).resolve().parent
REPO_ROOT = EVIDENCE_ROOT.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
TASK = "audio-runtime-c51-audio-device-boundary-characterization"
OWNED_REL = Path("docs/temp/projects/audio-runtime") / TASK
SOURCE_REVISION = "7f73c8b3b4ebc99b55b8bb5e802beff024385407"
PLANNING_MAIN_REVISION = SOURCE_REVISION
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
EXPECTED_BRANCH = "codex/audio-runtime-c51-audio-device-boundary-characterization"
MAX_OUTPUT = 64 * 1024
REQUIRED_RESERVE_BYTES = 2 * 1024 * 1024 * 1024

MODULES = {
    "go-agent-loop": {
        "root": Path("go-agent-loop"),
        "path": "github.com/portpowered/go-agent-harness/go-agent-loop",
    },
    "go-llm-gateway": {
        "root": Path("go-llm-gateway"),
        "path": "github.com/portpowered/go-agent-harness/go-llm-gateway",
    },
    "go-audio": {
        "root": Path("go-audio"),
        "path": "github.com/portpowered/go-agent-harness/go-audio",
    },
    "go-device-gateway": {
        "root": Path("go-device-gateway"),
        "path": "github.com/portpowered/go-agent-harness/go-device-gateway",
    },
    "go-agent-runtime": {
        "root": Path("go-agent-runtime"),
        "path": "github.com/portpowered/go-agent-harness/go-agent-runtime",
    },
    "agent-cli": {
        "root": Path("agent-cli"),
        "path": "github.com/portpowered/go-agent-harness/agent-cli",
    },
}

CONSUMPTION_LEVELS = [
    "QUEUE_ADMISSION",
    "BUFFER_RECEIPT",
    "FILE_OR_SOFTWARE_RECEIPT",
    "SOFTWARE_DEVICE_CALLBACK_CONSUMPTION",
    "PHYSICAL_HARDWARE_CONSUMPTION",
]


def die(message: str) -> None:
    raise SystemExit(f"C51 verification failed: {message}")


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        die(f"read JSON {path}: {exc}")


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def trim_output(data: bytes, limit: int) -> tuple[str, bool]:
    overflow = len(data) > limit
    return data[:limit].decode("utf-8", errors="replace"), overflow


def _drain(stream, sink: list[bytes], limit: int, overflow: list[bool]) -> None:
    retained = 0
    while True:
        chunk = stream.read(8192)
        if not chunk:
            return
        if retained < limit:
            keep = chunk[: limit - retained]
            sink.append(keep)
            retained += len(keep)
        if retained < len(b"".join(sink)) or len(chunk) > max(0, limit - retained):
            overflow[0] = True


def run_bounded(
    argv: list[str],
    *,
    cwd: Path = REPO_ROOT,
    timeout: float = 60.0,
    output_limit: int = MAX_OUTPUT,
    env: dict[str, str] | None = None,
) -> dict:
    """Run a process group with bounded output and TERM/KILL cleanup."""

    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    started = time.monotonic()
    try:
        process = subprocess.Popen(
            argv,
            cwd=str(cwd),
            env=merged_env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as exc:
        die(f"start {' '.join(argv)}: {exc}")

    stdout_parts: list[bytes] = []
    stderr_parts: list[bytes] = []
    stdout_overflow = [False]
    stderr_overflow = [False]
    stdout_thread = threading.Thread(target=_drain, args=(process.stdout, stdout_parts, output_limit, stdout_overflow), daemon=True)
    stderr_thread = threading.Thread(target=_drain, args=(process.stderr, stderr_parts, output_limit, stderr_overflow), daemon=True)
    stdout_thread.start()
    stderr_thread.start()
    timed_out = False
    try:
        return_code = process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=1.0)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2.0)
            except subprocess.TimeoutExpired:
                die(f"process group survived TERM/KILL: {' '.join(argv)}")
        return_code = process.returncode
    stdout_thread.join(timeout=2.0)
    stderr_thread.join(timeout=2.0)
    if stdout_thread.is_alive() or stderr_thread.is_alive():
        die(f"output reader survived process cleanup: {' '.join(argv)}")
    try:
        os.killpg(process.pid, 0)
        group_alive = True
    except ProcessLookupError:
        group_alive = False
    except PermissionError:
        group_alive = True
    elapsed = time.monotonic() - started
    stdout, stdout_truncated = trim_output(b"".join(stdout_parts), output_limit)
    stderr, stderr_truncated = trim_output(b"".join(stderr_parts), output_limit)
    return {
        "argv": argv,
        "cwd": str(cwd),
        "returnCode": return_code,
        "timedOut": timed_out,
        "durationSeconds": round(elapsed, 6),
        "stdout": stdout,
        "stderr": stderr,
        "stdoutTruncated": stdout_overflow[0] or stdout_truncated,
        "stderrTruncated": stderr_overflow[0] or stderr_truncated,
        "processGroupAlive": group_alive,
    }


def git(*args: str, timeout: float = 30.0) -> str:
    result = run_bounded(["git", *args], timeout=timeout)
    if result["returnCode"] != 0:
        die(f"git {' '.join(args)} failed: {result['stderr'][-2000:]}")
    return result["stdout"].strip()


def git_bytes_at(revision: str, relative: str) -> bytes:
    result = run_bounded(["git", "show", f"{revision}:{relative}"], output_limit=16 * 1024 * 1024)
    if result["returnCode"] != 0:
        die(f"missing {relative} at {revision}: {result['stderr'][-1000:]}")
    return result["stdout"].encode("utf-8")


def git_archive_hash(revision: str) -> tuple[str, int]:
    try:
        process = subprocess.Popen(
            ["git", "archive", "--format=tar", revision],
            cwd=str(REPO_ROOT),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError as exc:
        die(f"start git archive: {exc}")
    digest = hashlib.sha256()
    size = 0
    while True:
        chunk = process.stdout.read(1024 * 1024)
        if not chunk:
            break
        digest.update(chunk)
        size += len(chunk)
        if size > 1024 * 1024 * 1024:
            process.kill()
            die("source archive exceeded 1 GiB bound")
    stderr = process.stderr.read().decode("utf-8", errors="replace")
    if process.wait() != 0:
        die(f"git archive failed: {stderr[-2000:]}")
    return digest.hexdigest(), size


def source_files() -> list[tuple[str, str, Path]]:
    result: list[tuple[str, str, Path]] = []
    for module_name, metadata in MODULES.items():
        root = REPO_ROOT / metadata["root"]
        for path in sorted(root.rglob("*.go")):
            if any(part in {"vendor", ".git"} for part in path.parts):
                continue
            relative = path.relative_to(REPO_ROOT).as_posix()
            in_test_tree = path.name.endswith("_test.go") or "/test/" in f"/{relative}/" or relative.startswith("test/")
            role = "test" if in_test_tree else "tool" if "/cmd/" in f"/{relative}" or "/tools/" in f"/{relative}" else "production"
            result.append((module_name, role, path))
    return result


def build_analysis_inputs() -> dict:
    files = []
    for module_name, metadata in MODULES.items():
        go_mod = REPO_ROOT / metadata["root"] / "go.mod"
        relative = go_mod.relative_to(REPO_ROOT).as_posix()
        files.append({
            "path": relative,
            "role": "module-metadata",
            "module": module_name,
            "sha256": sha256_bytes(git_bytes_at(SOURCE_REVISION, relative)),
        })
    for module_name, role, path in source_files():
        relative = path.relative_to(REPO_ROOT).as_posix()
        files.append({
            "path": relative,
            "role": role,
            "module": module_name,
            "sha256": sha256_bytes(git_bytes_at(SOURCE_REVISION, relative)),
        })
    return {
        "schema": "audio-runtime-c51.analysis-inputs.v1",
        "sourceRevision": SOURCE_REVISION,
        "files": sorted(files, key=lambda item: item["path"]),
    }


def parse_imports(path: Path) -> list[dict]:
    lines = path.read_text(encoding="utf-8").splitlines()
    imports: list[dict] = []
    in_block = False
    for index, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("import ("):
            in_block = True
            continue
        if in_block and stripped == ")":
            in_block = False
            continue
        candidate = stripped if in_block else stripped.removeprefix("import ") if stripped.startswith("import ") else ""
        if not candidate or candidate.startswith("//"):
            continue
        first_quote = candidate.find('"')
        if first_quote < 0:
            continue
        second_quote = candidate.find('"', first_quote + 1)
        if second_quote < 0:
            continue
        imports.append({"path": candidate[first_quote + 1 : second_quote], "line": index})
    return imports


def module_for_import(import_path: str) -> str | None:
    matches = [name for name, metadata in MODULES.items() if import_path == metadata["path"] or import_path.startswith(metadata["path"] + "/")]
    return max(matches, key=lambda name: len(MODULES[name]["path"])) if matches else None


def build_import_graph(analysis_inputs: dict) -> dict:
    package_metadata = {}
    for module_name, metadata in MODULES.items():
        result = run_bounded(["go", "list", "-json", "./..."], cwd=REPO_ROOT / metadata["root"], timeout=120.0, output_limit=2 * 1024 * 1024, env={"GOWORK": "off"})
        if result["returnCode"] != 0:
            die(f"GOWORK=off go list for {module_name} failed: {result['stderr'][-2000:]}")
        decoder = json.JSONDecoder()
        offset = 0
        packages = []
        listing = result["stdout"]
        while offset < len(listing):
            while offset < len(listing) and listing[offset].isspace():
                offset += 1
            if offset >= len(listing):
                break
            try:
                package, next_offset = decoder.raw_decode(listing, offset)
            except json.JSONDecodeError as exc:
                die(f"parse go list metadata for {module_name} at byte {offset}: {exc}")
            offset = next_offset
            if not isinstance(package, dict) or not package.get("ImportPath"):
                continue
            packages.append({
                "importPath": package["ImportPath"],
                "dir": str(Path(package.get("Dir", "")).relative_to(REPO_ROOT)) if package.get("Dir", "").startswith(str(REPO_ROOT)) else package.get("Dir", ""),
                "goFiles": sorted(package.get("GoFiles", [])),
                "cgoFiles": sorted(package.get("CgoFiles", [])),
                "testGoFiles": sorted(package.get("TestGoFiles", [])),
                "xtestGoFiles": sorted(package.get("XTestGoFiles", [])),
            })
        package_metadata[module_name] = sorted(packages, key=lambda item: item["importPath"])

    edge_citations: dict[tuple[str, str, str, str], list[dict]] = {}
    for from_module, role, path in source_files():
        relative = path.relative_to(REPO_ROOT).as_posix()
        for imported in parse_imports(path):
            to_module = module_for_import(imported["path"])
            if to_module is None or to_module == from_module:
                continue
            key = (role, from_module, to_module, imported["path"])
            edge_citations.setdefault(key, []).append({"file": relative, "line": imported["line"]})

    edges = []
    for (role, from_module, to_module, import_path), citations in sorted(edge_citations.items()):
        edges.append({
            "from": from_module,
            "to": to_module,
            "importPath": import_path,
            "role": role,
            "citations": sorted(citations, key=lambda item: (item["file"], item["line"])),
        })
    production_edges = [edge for edge in edges if edge["role"] == "production"]
    adjacency = {name: set() for name in MODULES}
    for edge in production_edges:
        adjacency[edge["from"]].add(edge["to"])
    closure: dict[str, list[str]] = {}
    for start in MODULES:
        seen: set[str] = set()
        pending = sorted(adjacency[start])
        while pending:
            current = pending.pop(0)
            if current in seen:
                continue
            seen.add(current)
            pending.extend(sorted(adjacency[current] - seen))
        closure[start] = sorted(seen)
    forbidden = []
    for edge in production_edges:
        if edge["from"] in {"go-agent-loop", "go-audio"} and edge["to"] == "go-device-gateway":
            forbidden.append(edge)
    return {
        "schema": "audio-runtime-c51.import-graph.v1",
        "sourceRevision": SOURCE_REVISION,
        "modules": {name: metadata["path"] for name, metadata in MODULES.items()},
        "moduleMetadata": [item for item in analysis_inputs["files"] if item["role"] == "module-metadata"],
        "packages": package_metadata,
        "productionEdges": production_edges,
        "nonProductionEdges": [edge for edge in edges if edge["role"] != "production"],
        "transitiveProductionClosure": closure,
        "forbiddenProductionImports": forbidden,
    }


def authority_hashes() -> list[dict]:
    relatives = [
        "factory/docs/operating-policy.md",
        "factory/docs/implementation-handoff.md",
        "factory/projects/audio-runtime/manifest.json",
        "factory/projects/audio-runtime/request.md",
        "factory/projects/audio-runtime/acceptance.md",
        "factory/projects/audio-runtime/source-plan.md",
        "factory/projects/audio-runtime/amendments/user-windows-hardware-scope-20260910.json",
    ]
    result = []
    for relative in relatives:
        path = FACTORY_ROOT / relative
        if not path.is_file():
            die(f"authority input is unavailable: {path}")
        result.append({"path": relative, "sha256": sha256_file(path)})
    return result


def write_provenance(analysis_inputs: dict) -> dict:
    current_main = git("rev-parse", "origin/main")
    branch = git("branch", "--show-current")
    archive_hash, archive_bytes = git_archive_hash(SOURCE_REVISION)
    manifest = read_json(FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json")
    authority = authority_hashes()
    analysis_path = EVIDENCE_ROOT / "analysis-inputs.json"
    return {
        "schema": "audio-runtime-c51.provenance.v1",
        "project": "audio-runtime",
        "contractRevision": manifest.get("contractRevision"),
        "task": TASK,
        "admission": {
            "verifyCommand": "python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c51-audio-device-boundary-characterization --root $FACTORY_ROOT",
            "status": "admitted",
            "project": "audio-runtime",
            "name": TASK,
            "workId": "work-task-22",
        },
        "source": {
            "branch": branch,
            "expectedBranch": EXPECTED_BRANCH,
            "sourceRevision": SOURCE_REVISION,
            "planningMainRevision": PLANNING_MAIN_REVISION,
            "currentMainRevision": current_main,
            "startupIntegrationRevision": STARTUP_INTEGRATION_REVISION,
            "baselineRevision": BASELINE_REVISION,
            "sourceArchive": {
                "command": "git archive --format=tar <sourceRevision>",
                "sha256": archive_hash,
                "bytes": archive_bytes,
            },
        },
        "authorityInputs": authority,
        "analysisInputs": {
            "path": analysis_path.relative_to(REPO_ROOT).as_posix(),
            "sha256": sha256_file(analysis_path),
            "fileCount": len(analysis_inputs["files"]),
        },
        "scripts": [{"path": __file__.replace(str(REPO_ROOT) + "/", ""), "sha256": sha256_file(Path(__file__))}],
        "scope": {
            "ownedPath": OWNED_REL.as_posix(),
            "productionEdits": "none",
            "physicalHardware": "OUT_OF_SCOPE",
            "physicalAcousticProof": "OUT_OF_SCOPE",
            "windowsNativeEndpoints": "OUT_OF_SCOPE",
            "windowsSoftwareCompileAndHermeticValidation": "retained",
        },
        "storage": {
            "freeBytesAtGeneration": shutil.disk_usage(REPO_ROOT).free,
            "requiredCompileReserveBytes": REQUIRED_RESERVE_BYTES,
        },
    }


def mode_generate() -> None:
    analysis = build_analysis_inputs()
    write_json(EVIDENCE_ROOT / "analysis-inputs.json", analysis)
    graph = build_import_graph(analysis)
    write_json(EVIDENCE_ROOT / "import-graph.json", graph)
    provenance = write_provenance(analysis)
    write_json(EVIDENCE_ROOT / "provenance.json", provenance)
    print(json.dumps({"status": "generated", "analysisFiles": len(analysis["files"]), "sourceRevision": SOURCE_REVISION}, sort_keys=True))


def verify_provenance() -> None:
    path = EVIDENCE_ROOT / "provenance.json"
    provenance = read_json(path)
    if provenance.get("schema") != "audio-runtime-c51.provenance.v1":
        die("provenance schema mismatch")
    if git("branch", "--show-current") != EXPECTED_BRANCH:
        die("branch does not match the admitted PRD branch")
    if git("rev-parse", "origin/main") != PLANNING_MAIN_REVISION:
        die("origin/main moved after the pinned integration checkpoint; fetch and integrate before handoff")
    status = git("status", "--short")
    if status:
        die(f"worktree is not clean:\n{status}")
    head = git("rev-parse", "HEAD")
    if run_bounded(["git", "merge-base", "--is-ancestor", SOURCE_REVISION, head])["returnCode"] != 0:
        die("candidate head is not descended from the characterized source revision")
    changed = git("diff", "--name-only", f"{SOURCE_REVISION}..{head}")
    changed_paths = [Path(line) for line in changed.splitlines() if line]
    if any(path != OWNED_REL and OWNED_REL not in path.parents for path in changed_paths):
        die(f"candidate changes outside owned evidence path: {changed_paths}")
    prd = read_json(REPO_ROOT / "prd.json")
    if prd.get("branchName") != EXPECTED_BRANCH:
        die("prd.json.branchName does not match the isolated branch")
    recorded = provenance["source"]
    if recorded["sourceRevision"] != SOURCE_REVISION or recorded["currentMainRevision"] != PLANNING_MAIN_REVISION:
        die("provenance revision pins do not match the admitted integration checkpoint")
    archive_hash, archive_bytes = git_archive_hash(SOURCE_REVISION)
    if archive_hash != recorded["sourceArchive"]["sha256"] or archive_bytes != recorded["sourceArchive"]["bytes"]:
        die("source archive hash changed for the pinned revision")
    analysis_path = REPO_ROOT / provenance["analysisInputs"]["path"]
    if sha256_file(analysis_path) != provenance["analysisInputs"]["sha256"]:
        die("analysis-inputs.json hash does not match provenance")
    analysis = read_json(analysis_path)
    for item in analysis["files"]:
        actual = sha256_bytes(git_bytes_at(SOURCE_REVISION, item["path"]))
        if actual != item["sha256"]:
            die(f"analysis input changed at {item['path']}")
    for item in provenance["authorityInputs"]:
        actual = sha256_file(FACTORY_ROOT / item["path"])
        if actual != item["sha256"]:
            die(f"authority input changed at {item['path']}")
    for item in provenance["scripts"]:
        actual = sha256_file(REPO_ROOT / item["path"])
        if actual != item["sha256"]:
            die(f"analysis script changed at {item['path']}")
    free = shutil.disk_usage(REPO_ROOT).free
    if free < REQUIRED_RESERVE_BYTES:
        die(f"free storage {free} is below the required 2 GiB compile reserve")
    print(json.dumps({"status": "verified", "head": head, "sourceRevision": SOURCE_REVISION, "changedPaths": [p.as_posix() for p in changed_paths], "freeBytes": free}, sort_keys=True))


def verify_imports() -> None:
    analysis = read_json(EVIDENCE_ROOT / "analysis-inputs.json")
    if analysis.get("sourceRevision") != SOURCE_REVISION:
        die("analysis input source revision mismatch")
    expected = build_import_graph(analysis)
    actual = read_json(EVIDENCE_ROOT / "import-graph.json")
    if actual != expected:
        die("import-graph.json is not reproducible from the pinned production source")
    if actual["forbiddenProductionImports"]:
        die(f"forbidden production import edges found: {actual['forbiddenProductionImports']}")
    print(json.dumps({"status": "verified", "productionEdges": len(actual["productionEdges"]), "nonProductionEdges": len(actual["nonProductionEdges"]), "closure": actual["transitiveProductionClosure"]}, sort_keys=True))


def citation_text(citation: dict) -> str:
    return f"{citation.get('path')}:{citation.get('line')}"


def verify_boundaries() -> None:
    boundary = read_json(EVIDENCE_ROOT / "boundary-map.json")
    if boundary.get("sourceRevision") != SOURCE_REVISION:
        die("boundary map source revision mismatch")
    required = {"packet_parsing", "format_negotiation", "clock_and_timing", "dsp_and_resampling", "bounded_buffers", "core_loop_boundary", "device_selection_and_lifecycle", "runtime_device_adapter", "trace_and_replay"}
    actual = {item.get("id") for item in boundary.get("boundaries", [])}
    if actual != required:
        die(f"boundary map responsibility set = {sorted(actual)}, want {sorted(required)}")
    for item in boundary["boundaries"]:
        if not item.get("owner") or not item.get("consumers"):
            die(f"boundary {item.get('id')} lacks owner/consumer labels")
        for citation in item.get("citations", []):
            if citation.get("external"):
                continue
            path = REPO_ROOT / citation["path"]
            if not path.is_file():
                die(f"missing boundary citation {citation_text(citation)}")
            lines = path.read_text(encoding="utf-8").splitlines()
            line = int(citation["line"])
            if line < 1 or line > len(lines):
                die(f"boundary citation line is out of range: {citation_text(citation)}")
        if not item.get("evidence"):
            die(f"boundary {item.get('id')} has no evidence disposition")
    print(json.dumps({"status": "verified", "boundaries": len(boundary["boundaries"]), "citations": sum(len(i["citations"]) for i in boundary["boundaries"])}, sort_keys=True))


def verify_consumption_levels() -> None:
    boundary = read_json(EVIDENCE_ROOT / "boundary-map.json")
    declared = boundary.get("consumptionLevels")
    if declared != CONSUMPTION_LEVELS:
        die(f"consumption levels = {declared}, want {CONSUMPTION_LEVELS}")
    observations = boundary.get("observations", {})
    for level in CONSUMPTION_LEVELS:
        if level not in observations:
            die(f"missing consumption disposition {level}")
        disposition = observations[level]
        if not disposition.get("status") or not disposition.get("evidence"):
            die(f"consumption disposition {level} lacks status/evidence")
    if observations["PHYSICAL_HARDWARE_CONSUMPTION"]["status"] != "OUT_OF_SCOPE":
        die("physical hardware consumption must remain OUT_OF_SCOPE under the admitted amendment")
    if observations["SOFTWARE_DEVICE_CALLBACK_CONSUMPTION"]["status"] != "OBSERVED":
        die("software callback consumption was not labeled observed")
    print(json.dumps({"status": "verified", "levels": observations}, sort_keys=True))


def consumer_dir() -> Path:
    return EVIDENCE_ROOT / "consumer"


def run_consumer_test(race: bool = False) -> dict:
    args = ["go", "test", "-count=1", "-timeout", "90s"]
    if race:
        args.append("-race")
    args.extend(["./..."])
    return run_bounded(args, cwd=consumer_dir(), timeout=120.0, env={"GOWORK": "off"})


def verify_consumer() -> None:
    deps = run_bounded(["go", "list", "-deps", "./..."], cwd=consumer_dir(), timeout=120.0, output_limit=2 * 1024 * 1024, env={"GOWORK": "off"})
    if deps["returnCode"] != 0:
        die(f"consumer GOWORK=off dependency listing failed: {deps['stderr'][-2000:]}")
    (EVIDENCE_ROOT / "consumer-deps.txt").write_text(deps["stdout"], encoding="utf-8")
    if "github.com/portpowered/go-agent-harness/agent-cli" in deps["stdout"]:
        die("external consumer transitively imports agent-cli")
    normal = run_consumer_test()
    if normal["returnCode"] != 0 or normal["timedOut"] or normal["processGroupAlive"]:
        die(f"consumer test failed: {normal}")
    race = run_consumer_test(race=True)
    if race["returnCode"] != 0 or race["timedOut"] or race["processGroupAlive"]:
        die(f"consumer race test failed: {race}")
    write_json(EVIDENCE_ROOT / "evidence/consumer-test.json", {"status": "passed", "sourceRevision": SOURCE_REVISION, "dependencyCommand": deps, "normal": normal, "race": race})
    print(json.dumps({"status": "verified", "normal": normal["stdout"].strip(), "race": race["stdout"].strip(), "dependencyLines": len(deps["stdout"].splitlines())}, sort_keys=True))


def verify_wrong_oracle() -> None:
    result = run_bounded(["go", "test", "-count=1", "-timeout", "60s", "-run", "^TestPublicDeviceBoundary$", "./..."], cwd=consumer_dir(), timeout=90.0, env={"GOWORK": "off", "C51_WRONG_ORACLE": "1"})
    combined = result["stdout"] + result["stderr"]
    if result["returnCode"] == 0 or result["timedOut"] or result["processGroupAlive"]:
        die(f"wrong-consumption oracle unexpectedly passed or was unbounded: {result}")
    marker = "wrong consumption oracle: expected queue admission to equal callback consumption before Advance"
    if marker not in combined:
        die(f"wrong-consumption oracle failed without its deliberate marker: {result}")
    write_json(EVIDENCE_ROOT / "evidence/wrong-consumption-oracle.json", {"status": "rejected_as_expected", "marker": marker, "result": result})
    print(json.dumps({"status": "verified", "expectedFailure": True, "marker": marker}, sort_keys=True))


def wav_summary(path: Path) -> dict:
    raw = path.read_bytes()
    with wave.open(str(path), "rb") as stream:
        pcm = stream.readframes(stream.getnframes())
        return {
            "fileBytes": len(raw),
            "pcmBytes": len(pcm),
            "wavSHA256": sha256_bytes(raw),
            "pcmSHA256": sha256_bytes(pcm),
            "channels": stream.getnchannels(),
            "sampleWidth": stream.getsampwidth(),
            "sampleRate": stream.getframerate(),
            "frames": stream.getnframes(),
        }


def verify_shipped_regression(yui: str, child_timeout: float = 60.0, total_timeout: float = 600.0) -> None:
    binary = Path(yui).resolve()
    if not binary.is_file():
        die(f"shipped yui binary is missing: {binary}")
    fixture = REPO_ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"
    tool_fixture = REPO_ROOT / "go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json"
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c51-") as temporary:
        temp = Path(temporary)
        output = temp / "healthy.wav"
        started = time.monotonic()
        healthy = run_bounded([str(binary), "--config-dir", str(temp / "config"), "session", "--replay", str(fixture), "--audio-out", str(output)], timeout=min(child_timeout, 60.0, total_timeout), output_limit=MAX_OUTPUT)
        if healthy["returnCode"] != 0 or healthy["timedOut"] or healthy["processGroupAlive"]:
            die(f"shipped healthy audio replay failed: {healthy}")
        if not output.is_file():
            die("shipped healthy audio replay did not write WAV output")
        healthy_wav = wav_summary(output)
        expected_wav = {
            "pcmSHA256": "df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119",
            "wavSHA256": "3e9bdf4cffd0b09b1ce550cf199542cd54fdaf69bf4f53d2e1b8be88c02f5aa3",
            "pcmBytes": 4,
            "channels": 1,
            "sampleWidth": 2,
            "sampleRate": 16000,
            "frames": 2,
        }
        if any(healthy_wav.get(key) != value for key, value in expected_wav.items()):
            die(f"shipped healthy audio PCM/WAV oracle changed: got {healthy_wav}, want {expected_wav}")
        lifecycle_markers = [
            "Assistant: Hello thereSecond turn reply",
            "[session closed: healthy_complete]",
            "[session terminal: classification=transport terminal_reason=provider_close terminal_provenance=provider output_state=not_applicable]",
        ]
        for marker in lifecycle_markers:
            if marker not in healthy["stdout"]:
                die(f"shipped replay missing lifecycle marker {marker!r}: {healthy['stdout']}")

        tool_output = temp / "tool.wav"
        remaining = max(0.1, total_timeout - (time.monotonic() - started))
        tool = run_bounded([str(binary), "--config-dir", str(temp / "tool-config"), "session", "--replay", str(tool_fixture), "--audio-out", str(tool_output)], timeout=min(child_timeout, 60.0, remaining), output_limit=MAX_OUTPUT)
        tool_combined = tool["stdout"] + tool["stderr"]
        tool_marker = "tool results were not delivered for 1 unresolved call(s): call_weather_001"
        if tool["returnCode"] == 0 or tool["timedOut"] or tool["processGroupAlive"] or tool_marker not in tool_combined:
            die(f"shipped tool lifecycle negative control changed: {tool}")
        report = {
            "status": "verified",
            "sourceRevision": SOURCE_REVISION,
            "binary": {"path": str(binary), "sha256": sha256_file(binary)},
            "healthyReplay": {"fixture": str(fixture.relative_to(REPO_ROOT)), "result": healthy, "wav": healthy_wav, "expectedWav": expected_wav, "markers": lifecycle_markers},
            "toolLifecycleNegativeControl": {"fixture": str(tool_fixture.relative_to(REPO_ROOT)), "result": tool, "expectedMarker": tool_marker, "audioOutputWritten": tool_output.is_file()},
        }
    write_json(EVIDENCE_ROOT / "evidence/shipped-regression.json", report)
    print(json.dumps({"status": "verified", "binarySHA256": report["binary"]["sha256"], "healthyPCM": healthy_wav["pcmSHA256"], "toolNegativeControl": True}, sort_keys=True))


def verify_runner_negative_controls(child_timeout: float = 60.0, total_timeout: float = 600.0) -> None:
    started = time.monotonic()
    timeout_control = run_bounded([sys.executable, "-c", "import time; print('C51_TIMEOUT_CONTROL', flush=True); time.sleep(5)"], timeout=min(0.25, child_timeout, total_timeout), output_limit=1024)
    if not timeout_control["timedOut"] or timeout_control["processGroupAlive"] or "C51_TIMEOUT_CONTROL" not in timeout_control["stdout"]:
        die(f"bounded timeout/reap control failed: {timeout_control}")
    remaining = max(0.1, total_timeout - (time.monotonic() - started))
    overflow_control = run_bounded([sys.executable, "-c", "import sys; sys.stdout.write('x' * 10000)"], timeout=min(5.0, child_timeout, remaining), output_limit=1024)
    if overflow_control["returnCode"] != 0 or not overflow_control["stdoutTruncated"] or overflow_control["processGroupAlive"]:
        die(f"bounded output control failed: {overflow_control}")
    report = {"status": "verified", "timeoutAndReap": timeout_control, "outputBound": overflow_control}
    write_json(EVIDENCE_ROOT / "evidence/runner-negative-controls.json", report)
    print(json.dumps({"status": "verified", "timeoutReaped": True, "outputBounded": True}, sort_keys=True))


def verify_repairs() -> None:
    repairs = read_json(EVIDENCE_ROOT / "repair-candidates.json")
    if repairs.get("sourceRevision") != SOURCE_REVISION:
        die("repair-candidates source revision mismatch")
    for candidate in repairs.get("candidates", []):
        paths = candidate.get("paths", [])
        if not paths:
            die(f"repair candidate {candidate.get('id')} has no exact path")
        for path in paths:
            if path.startswith("go-audio/") or path.startswith("go-device-gateway/") or path.startswith("go-agent-runtime/") or path.startswith("go-agent-loop/") or path.startswith("go-llm-gateway/") or path.startswith("agent-cli/"):
                die(f"repair candidate proposes an unauthorized production path: {path}")
        if candidate.get("status") not in {"none", "deferred", "out_of_scope"}:
            die(f"repair candidate {candidate.get('id')} is not explicitly deferred/absent")
        if not candidate.get("evidence") or not candidate.get("owner"):
            die(f"repair candidate {candidate.get('id')} lacks evidence/owner disposition")
    print(json.dumps({"status": "verified", "candidateCount": len(repairs.get("candidates", [])), "readyCandidates": [c["id"] for c in repairs.get("candidates", []) if c.get("status") == "ready"]}, sort_keys=True))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["generate", "provenance", "imports", "boundaries", "consumption-levels", "consumer", "wrong-consumption-oracle", "shipped-regression", "runner-negative-controls", "repair-candidates"])
    parser.add_argument("--binary", "--yui", dest="yui", help="exact shipped yui binary for --mode shipped-regression")
    parser.add_argument("--child-timeout", type=float, default=60.0, help="maximum seconds for one child process")
    parser.add_argument("--total-timeout", type=float, default=600.0, help="maximum seconds for the aggregate regression")
    args = parser.parse_args()
    if args.mode == "generate":
        mode_generate()
    elif args.mode == "provenance":
        verify_provenance()
    elif args.mode == "imports":
        verify_imports()
    elif args.mode == "boundaries":
        verify_boundaries()
    elif args.mode == "consumption-levels":
        verify_consumption_levels()
    elif args.mode == "consumer":
        verify_consumer()
    elif args.mode == "wrong-consumption-oracle":
        verify_wrong_oracle()
    elif args.mode == "shipped-regression":
        if not args.yui:
            die("--yui is required for shipped-regression")
        verify_shipped_regression(args.yui, args.child_timeout, args.total_timeout)
    elif args.mode == "runner-negative-controls":
        verify_runner_negative_controls(args.child_timeout, args.total_timeout)
    elif args.mode == "repair-candidates":
        verify_repairs()


if __name__ == "__main__":
    main()
