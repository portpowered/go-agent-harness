#!/usr/bin/env python3
"""Build the C107 accepted-main ownership census.

This task is deliberately evidence-only.  The source is read from a pinned
git archive, never from a moving branch, and the only files written are below
the caller supplied output directory.  The small C50 AST census is a
read-only, accepted-main parser dependency; this driver owns the C107 schema
and the source/subtraction/classification rules.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from collections import Counter
from typing import Any


WORK = "audio-runtime-c107-characterize-post-wave-cli-ownership"
TARGET_ROOT = "agent-cli/internal/services/internal/agentruntime"
C50_ANALYZER = "docs/temp/projects/audio-runtime/audio-runtime-c50-remaining-cli-service-inventory/analyzer/main.go"
ALLOWED_CLASSES = {
    "thin_cli_transport_presentation",
    "deprecated_adapter",
    "reusable_runtime_behavior",
    "business_policy",
    "composition_ownership",
    "device_audio_implementation",
    "dead_uncertain",
}


class AnalysisError(RuntimeError):
    pass


def stable_json(value: Any) -> str:
    return json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(stable_json(value), encoding="utf-8")


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git(root: pathlib.Path, *args: str, text: bool = True) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=root,
        check=True,
        capture_output=True,
        text=text,
        timeout=120,
    )
    return result.stdout.strip() if text else result.stdout


def checked_revision(root: pathlib.Path, revision: str) -> str:
    try:
        resolved = git(root, "rev-parse", "--verify", f"{revision}^{{commit}}")
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        raise AnalysisError(f"source revision is not a commit: {revision}") from exc
    if resolved != revision and re.fullmatch(r"[0-9a-f]{40}", revision):
        raise AnalysisError(f"source revision resolved unexpectedly: {revision} -> {resolved}")
    return resolved


def archive_source(root: pathlib.Path, revision: str, destination: pathlib.Path) -> tuple[pathlib.Path, str]:
    archive = git(root, "archive", "--format=tar", revision, text=False)
    archive_hash = sha256_bytes(archive)
    extracted = destination / "archive"
    extracted.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=__import__("io").BytesIO(archive), mode="r:") as tar:
        try:
            tar.extractall(extracted, filter="data")
        except TypeError:  # pragma: no cover - compatibility with older Python.
            tar.extractall(extracted)
    direct = extracted / C50_ANALYZER
    if direct.is_file():
        snapshot = extracted
    else:
        candidates = []
        expected_parts = pathlib.PurePosixPath(C50_ANALYZER).parts
        for candidate in extracted.rglob("main.go"):
            try:
                relative = candidate.relative_to(extracted).as_posix()
            except ValueError:
                continue
            if relative != C50_ANALYZER and not relative.endswith("/" + C50_ANALYZER):
                continue
            prefix = candidate.parents[len(expected_parts) - 1]
            candidates.append(prefix)
        if len(candidates) != 1 or not (candidates[0] / C50_ANALYZER).is_file():
            raise AnalysisError("source archive did not contain the pinned parser at its expected path")
        snapshot = candidates[0]
    return snapshot, archive_hash


def command_version(argv: list[str], cwd: pathlib.Path) -> str:
    result = subprocess.run(argv, cwd=cwd, check=True, capture_output=True, text=True, timeout=30)
    return result.stdout.strip()


def run_c50(snapshot: pathlib.Path, raw_output: pathlib.Path, revision: str) -> dict[str, Any]:
    analyzer = snapshot / C50_ANALYZER
    if not analyzer.is_file():
        raise AnalysisError(f"pinned parser is absent from source archive: {C50_ANALYZER}")
    actual_command = [
        "go",
        "run",
        str(analyzer),
        "--root",
        str(snapshot),
        "--out",
        str(raw_output),
        "--source-revision",
        revision,
    ]
    started = time.monotonic()
    result = subprocess.run(
        actual_command,
        cwd=snapshot,
        capture_output=True,
        text=True,
        timeout=240,
        env=safe_environment(),
    )
    duration = time.monotonic() - started
    if result.returncode != 0:
        raise AnalysisError(
            "pinned AST census failed: " + (result.stderr.strip() or result.stdout.strip() or "unknown failure")
        )
    return {
        "command_template": [
            "go",
            "run",
            "<source-archive>/" + C50_ANALYZER,
            "--root",
            "<source-archive>",
            "--out",
            "<run-output>",
            "--source-revision",
            revision,
        ],
        "command": actual_command,
        "go_version": command_version(["go", "version"], snapshot),
        "exit_code": result.returncode,
        "duration_seconds": round(duration, 6),
        "stdout_sha256": sha256_bytes(result.stdout.encode()),
        "stderr_sha256": sha256_bytes(result.stderr.encode()),
        "analyzer_source_sha256": sha256_file(analyzer),
    }


def safe_environment() -> dict[str, str]:
    secret_markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    result: dict[str, str] = {}
    for key, value in os.environ.items():
        if any(marker in key.upper() for marker in secret_markers):
            continue
        result[key] = value
    for key in ("PATH", "HOME", "TMPDIR", "GOWORK", "GOFLAGS", "FACTORY_ROOT", "FACTORY_SERVER_URL"):
        if key in os.environ:
            result[key] = os.environ[key]
    return result


def read_subtraction(path: pathlib.Path | None) -> dict[str, list[dict[str, Any]]]:
    if path is None:
        return {}
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise AnalysisError(f"cannot read subtraction ledger: {path}") from exc
    owners = document.get("path_owners")
    if not isinstance(owners, dict):
        raise AnalysisError("subtraction ledger has no path_owners object")
    normalized: dict[str, list[dict[str, Any]]] = {}
    for source_path, rows in owners.items():
        if not isinstance(rows, list):
            raise AnalysisError(f"subtraction owners for {source_path} are not a list")
        normalized[source_path] = sorted(rows, key=lambda row: (str(row.get("owner", "")), str(row.get("head", ""))))
    return normalized


def subtraction_state(path: str, owners: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    if not owners:
        return {"state": "not-provided", "owners": []}
    rows = owners.get(path, [])
    return {"state": "subtracted" if rows else "unsubtracted", "owners": rows}


def source_segment(snapshot: pathlib.Path, path: str, line: int, end_line: int) -> str:
    lines = (snapshot / path).read_text(encoding="utf-8", errors="replace").splitlines()
    start = max(0, line - 8)
    end = min(len(lines), max(line, end_line))
    return "\n".join(lines[start:end])


def classify(raw: dict[str, Any], source_text: str) -> tuple[str, list[str]]:
    raw_class = raw.get("class")
    mapping = {
        "THIN_TRANSPORT_DELEGATION": "thin_cli_transport_presentation",
        "BUSINESS_POLICY": "business_policy",
        "COMPOSITION_WIRING": "composition_ownership",
        "REUSABLE_RUNTIME_BEHAVIOR": "reusable_runtime_behavior",
        "DEAD_OR_UNCERTAIN": "dead_uncertain",
    }
    path = str(raw.get("file", ""))
    basename = pathlib.PurePosixPath(path).name
    evidence = list(raw.get("classification_evidence") or [])
    if re.search(r"\bDeprecated:", source_text):
        return "deprecated_adapter", evidence + ["explicit Deprecated: marker appears in the declaration documentation"]
    if basename.startswith("rtc_device_") or basename.startswith("session_audio_") or basename.startswith("session_runtime_rtc"):
        return "device_audio_implementation", evidence + ["source file owns RTC/device/audio implementation surface"]
    result = mapping.get(raw_class, "dead_uncertain")
    if result == "dead_uncertain" and raw_class not in mapping:
        evidence.append(f"unknown parser class {raw_class!r}; fail-closed mapping to dead_uncertain")
    return result, evidence


def uncertainty(raw: dict[str, Any], source_text: str) -> dict[str, Any]:
    evidence = " ".join(str(item) for item in (raw.get("classification_evidence") or []))
    lower = source_text.lower()
    notes: list[str] = []
    dynamic = any(token in (evidence + lower) for token in ("dynamic", "reflection", "generated", "function value"))
    interface_dispatch = "interface" in lower or "interface" in evidence.lower()
    function_value = bool(re.search(r"\bfunc\s*\([^)]*\)\s*[^;{]*\b", source_text)) or "function value" in evidence.lower()
    reflection = "reflect." in lower or "reflection" in evidence.lower()
    generated = "code generated" in lower or pathlib.PurePosixPath(str(raw.get("file", ""))).name.endswith("_gen.go")
    if raw.get("class") == "DEAD_OR_UNCERTAIN":
        notes.append("unresolved static reachability is retained as uncertainty; no caller is inferred")
    if interface_dispatch:
        notes.append("interface dispatch may bypass direct AST caller resolution")
    if function_value:
        notes.append("function values/closures may bypass direct AST caller resolution")
    if reflection:
        notes.append("reflection may bypass direct AST caller resolution")
    if generated:
        notes.append("generated-code reachability is not inferred")
    return {
        "dynamic_reachability": bool(dynamic),
        "interface_dispatch": bool(interface_dispatch),
        "function_value": bool(function_value),
        "reflection": bool(reflection),
        "generated": bool(generated),
        "notes": sorted(set(notes)),
    }


def service_dependencies(file_record: dict[str, Any]) -> list[str] | str:
    dependencies: set[str] = set()
    for item in file_record.get("imports") or []:
        path = item.get("path") if isinstance(item, dict) else item
        if isinstance(path, str) and "/go-agent-runtime/services/" in path:
            dependencies.add(path)
    return sorted(dependencies) if dependencies else "NONE"


def normalize_inventory(raw: dict[str, Any], snapshot: pathlib.Path, revision: str, archive_hash: str, engine: dict[str, Any], subtraction: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    files_by_path = {
        item["path"]: item
        for item in raw.get("files", [])
        if item.get("root") == TARGET_ROOT and str(item.get("path", "")).startswith(TARGET_ROOT + "/")
    }
    file_paths = sorted(files_by_path)
    raw_symbols = [item for item in raw.get("symbols", []) if str(item.get("file", "")).startswith(TARGET_ROOT + "/")]
    raw_symbols.sort(key=lambda item: (item.get("file", ""), int(item.get("line", 0)), item.get("id", "")))
    symbol_by_id: dict[str, dict[str, Any]] = {}
    symbols: list[dict[str, Any]] = []
    for item in raw_symbols:
        path = str(item["file"])
        segment = source_segment(snapshot, path, int(item.get("line", 1)), int(item.get("end_line", item.get("line", 1))))
        classification, classification_evidence = classify(item, segment)
        caller_rows = sorted(
            [dict(caller) for caller in (item.get("production_callers") or [])],
            key=lambda caller: (caller.get("file", ""), int(caller.get("line", 0)), caller.get("expression", "")),
        )
        file_record = files_by_path[path]
        symbol = {
            "id": item["id"],
            "name": item.get("name"),
            "qualified_name": item.get("qualified_name"),
            "kind": item.get("kind"),
            "receiver": item.get("receiver", ""),
            "exported": bool(item.get("exported")),
            "package": item.get("package"),
            "import_path": item.get("import_path"),
            "file": path,
            "line": int(item.get("line", 0)),
            "end_line": int(item.get("end_line", 0)),
            "signature": item.get("signature", ""),
            "caller_edges": caller_rows,
            "caller_edge_status": "exact-static" if caller_rows else "no-static-caller",
            "dynamic_interface_reflection_generated_uncertainty": uncertainty(item, segment),
            "existing_destination_service_dependency": service_dependencies(file_record),
            "subtraction": subtraction_state(path, subtraction),
            "class": classification,
            "classification_evidence": sorted(set(classification_evidence)),
        }
        symbols.append(symbol)
        symbol_by_id[symbol["id"]] = symbol

    files: list[dict[str, Any]] = []
    for path in file_paths:
        item = files_by_path[path]
        file_symbols = [symbol for symbol in symbols if symbol["file"] == path]
        files.append(
            {
                "path": path,
                "root": TARGET_ROOT,
                "package": item.get("package"),
                "import_path": item.get("import_path"),
                "kind": "production",
                "physical_lines": int(item.get("physical_lines", 0)),
                "bytes": int(item.get("bytes", 0)),
                "imports": item.get("imports", []),
                "top_level_symbol_ids": [symbol["id"] for symbol in file_symbols],
                "top_level_symbol_count": len(file_symbols),
                "subtraction": subtraction_state(path, subtraction),
            }
        )

    symbol_ids = set(symbol_by_id)
    call_edges: list[dict[str, Any]] = []
    for edge in raw.get("call_edges", []):
        if edge.get("callee") not in symbol_ids:
            continue
        call = dict(edge.get("call") or {})
        call_edges.append({"caller": edge.get("caller"), "callee": edge.get("callee"), "call": call})
    call_edges.sort(key=lambda edge: (edge.get("callee", ""), (edge.get("call") or {}).get("file", ""), int((edge.get("call") or {}).get("line", 0)), (edge.get("call") or {}).get("expression", "")))

    class_counts = Counter(symbol["class"] for symbol in symbols)
    physical_lines = sum(file["physical_lines"] for file in files)
    byte_count = sum(file["bytes"] for file in files)
    deterministic_work_seconds = round((byte_count + len(symbols) * 64 + len(call_edges) * 32) / 10_000_000, 6)
    inventory = {
        "schema_version": "c107-inventory-v1",
        "task": WORK,
        "source_revision": revision,
        "source_archive_sha256": archive_hash,
        "target_root": TARGET_ROOT,
        "analysis_engine": {
            "name": "c107-source-pinning-and-schema-driver",
            "parser": "accepted-main C50 standard-library Go AST census",
            "parser_source": C50_ANALYZER,
            "parser_source_sha256": engine["analyzer_source_sha256"],
            "command_template": engine["command_template"],
            "go_version": engine["go_version"],
            "exit_code": engine["exit_code"],
            "duration_seconds": deterministic_work_seconds,
            "duration_kind": "deterministic work estimate; wall duration is retained in analysis-run.json",
        },
        "files": files,
        "symbols": symbols,
        "call_edges": call_edges,
        "totals": {
            "production_files": len(files),
            "physical_lines": physical_lines,
            "bytes": byte_count,
            "top_level_symbols": len(symbols),
            "exact_static_call_edges": len(call_edges),
            "no_static_caller_symbols": sum(symbol["caller_edge_status"] == "no-static-caller" for symbol in symbols),
            "class_counts": dict(sorted(class_counts.items())),
        },
        "rules": {
            "production_file_rule": "accepted-main .go under target root excluding _test.go and generated files",
            "physical_line_rule": "all physical lines including comments and blanks",
            "symbol_rule": "every top-level type, const, var, func and method declaration emitted by the pinned AST census",
            "caller_rule": "exact production AST caller rows from the pinned census; no-static-caller is explicit and does not imply unreachable",
            "deprecated_rule": "deprecated_adapter is legal only when the declaration context contains an explicit Deprecated: marker",
            "uncertainty_rule": "interface, function-value, reflection and generated reachability are recorded as uncertainty rather than guessed",
        },
    }
    return inventory


def markdown(inventory: dict[str, Any]) -> str:
    totals = inventory["totals"]
    lines = [
        "# C107 accepted-main ownership inventory",
        "",
        f"Source revision: `{inventory['source_revision']}`  ",
        f"Source archive SHA-256: `{inventory['source_archive_sha256']}`  ",
        f"Target root: `{inventory['target_root']}`",
        "",
        "The report is generated from a pinned source archive. It is a census, not a migration percentage or acceptance waiver.",
        "",
        "## Totals",
        "",
        "| production files | physical lines | bytes | top-level symbols | exact static call edges | no-static-caller symbols |",
        "|---:|---:|---:|---:|---:|---:|",
        f"| {totals['production_files']} | {totals['physical_lines']} | {totals['bytes']} | {totals['top_level_symbols']} | {totals['exact_static_call_edges']} | {totals['no_static_caller_symbols']} |",
        "",
        "## Class counts",
        "",
        "| class | symbols |",
        "|---|---:|",
    ]
    for name, count in sorted(totals["class_counts"].items()):
        lines.append(f"| `{name}` | {count} |")
    lines.extend(["", "## Files", "", "| file | physical lines | symbols | subtraction |", "|---|---:|---:|---|"])
    for file in inventory["files"]:
        subtraction = file["subtraction"]
        owners = ", ".join(row.get("owner", "") for row in subtraction["owners"]) or "NONE"
        lines.append(f"| `{file['path']}` | {file['physical_lines']} | {file['top_level_symbol_count']} | {subtraction['state']}: {owners} |")
    lines.extend(["", "## Symbols", "", "| stable ID | kind | location | class | callers | destination service dependency | subtraction | uncertainty |", "|---|---|---|---|---|---|---|---|"])
    for symbol in inventory["symbols"]:
        callers = "; ".join(f"`{caller.get('file')}:{caller.get('line')}` {caller.get('expression')}" for caller in symbol["caller_edges"]) or "no-static-caller"
        dependency = ", ".join(symbol["existing_destination_service_dependency"]) if isinstance(symbol["existing_destination_service_dependency"], list) else symbol["existing_destination_service_dependency"]
        subtraction = symbol["subtraction"]
        owners = ", ".join(row.get("owner", "") for row in subtraction["owners"]) or "NONE"
        uncertainty_flags = ", ".join(key for key, value in symbol["dynamic_interface_reflection_generated_uncertainty"].items() if isinstance(value, bool) and value) or "none"
        values = [
            symbol["id"],
            symbol["kind"],
            f"{symbol['file']}:{symbol['line']}-{symbol['end_line']}",
            symbol["class"],
            callers,
            dependency,
            f"{subtraction['state']}: {owners}",
            uncertainty_flags,
        ]
        lines.append("| " + " | ".join(str(value).replace("|", "\\|") for value in values) + " |")
    return "\n".join(lines) + "\n"


def write_outputs(output: pathlib.Path, inventory: dict[str, Any], engine: dict[str, Any], wall_duration: float, run_timestamp: str) -> None:
    output.mkdir(parents=True, exist_ok=True)
    write_json(output / "inventory.json", inventory)
    write_json(
        output / "classifications.json",
        {
            "schema_version": "c107-classifications-v1",
            "source_revision": inventory["source_revision"],
            "symbols": [
                {
                    "id": symbol["id"],
                    "class": symbol["class"],
                    "classification_evidence": symbol["classification_evidence"],
                    "uncertainty": symbol["dynamic_interface_reflection_generated_uncertainty"],
                }
                for symbol in inventory["symbols"]
            ],
        },
    )
    write_json(
        output / "call-paths.json",
        {
            "schema_version": "c107-call-paths-v1",
            "source_revision": inventory["source_revision"],
            "target_root": TARGET_ROOT,
            "call_edges": inventory["call_edges"],
            "no_static_caller": [
                {"id": symbol["id"], "file": symbol["file"], "line": symbol["line"], "name": symbol["name"]}
                for symbol in inventory["symbols"]
                if symbol["caller_edge_status"] == "no-static-caller"
            ],
        },
    )
    (output / "inventory.md").write_text(markdown(inventory), encoding="utf-8")
    write_json(
        output / "analysis-run.json",
        {
            "schema_version": "c107-analysis-run-v1",
            "run_timestamp": run_timestamp,
            "source_revision": inventory["source_revision"],
            "source_archive_sha256": inventory["source_archive_sha256"],
            "command": engine["command"],
            "command_template": engine["command_template"],
            "go_version": engine["go_version"],
            "exit_code": engine["exit_code"],
            "wall_duration_seconds": round(wall_duration, 6),
            "parser_source_sha256": engine["analyzer_source_sha256"],
            "inventory_sha256": sha256_file(output / "inventory.json"),
            "markdown_sha256": sha256_file(output / "inventory.md"),
        },
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, help="full immutable source commit")
    parser.add_argument("--output-dir", required=True, type=pathlib.Path)
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parent.parents[4])
    parser.add_argument("--subtraction", type=pathlib.Path)
    args = parser.parse_args()
    root = args.root.resolve()
    output = args.output_dir.resolve()
    started = time.monotonic()
    timestamp = dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    try:
        revision = checked_revision(root, args.source)
        with tempfile.TemporaryDirectory(prefix="c107-source-") as temp_dir:
            snapshot, archive_hash = archive_source(root, revision, pathlib.Path(temp_dir))
            raw_output = pathlib.Path(temp_dir) / "raw-c50"
            engine = run_c50(snapshot, raw_output, revision)
            raw_inventory_path = raw_output / "inventory.json"
            try:
                raw_inventory = json.loads(raw_inventory_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                raise AnalysisError("pinned AST census did not produce valid inventory.json") from exc
            subtraction = read_subtraction(args.subtraction.resolve() if args.subtraction else None)
            inventory = normalize_inventory(raw_inventory, snapshot, revision, archive_hash, engine, subtraction)
            if output.exists():
                for child in output.iterdir():
                    if child.is_dir():
                        shutil.rmtree(child)
                    else:
                        child.unlink()
            write_outputs(output, inventory, engine, time.monotonic() - started, timestamp)
        print(json.dumps({"status": "passed", "source_revision": revision, "output_dir": str(output), "files": inventory["totals"]["production_files"], "symbols": inventory["totals"]["top_level_symbols"]}, sort_keys=True))
        return 0
    except (AnalysisError, subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError) as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
