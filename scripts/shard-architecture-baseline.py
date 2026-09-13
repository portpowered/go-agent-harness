#!/usr/bin/env python3
"""Convert a reviewed architecture baseline into deterministic owner fragments."""

import argparse
import json
from pathlib import Path


def _safe_relative(value, label):
    path = Path(value)
    if not value or path.is_absolute() or ".." in path.parts:
        raise ValueError(f"unsafe {label}: {value!r}")
    return path


def issue_key(entry):
    return "\0".join(entry.get(field, "") for field in ("rule", "module", "package", "file", "symbol"))


def fragment_path(entry):
    module = entry.get("module", "")
    package = entry.get("package", "")
    module_path = _safe_relative(module, "module")
    if entry.get("file"):
        return module_path / Path(str(_safe_relative(entry["file"], "file")) + ".json")
    if package != module and not package.startswith(module + "/"):
        raise ValueError(f"package {package!r} is outside module {module!r}")
    relative_package = package[len(module):].lstrip("/")
    package_path = _safe_relative(relative_package, "package") if relative_package else Path()
    return module_path / package_path / "_package.json"


def shard(source, destination, replace=False):
    if destination.name != "baselines":
        raise ValueError("destination directory must be named 'baselines'")
    document = json.loads(source.read_text())
    if document.get("version") != 1 or not isinstance(document.get("entries"), list):
        raise ValueError("source must be a version 1 architecture baseline")
    groups = {}
    entry_paths = {}
    for entry in document["entries"]:
        key = issue_key(entry)
        if key in entry_paths:
            raise ValueError(f"duplicate source entry {key!r}")
        path = fragment_path(entry)
        entry_paths[key] = path
        groups.setdefault(path, {"entries": [], "renames": []})["entries"].append(entry)
    for rename in document.get("renames", []):
        target_path = entry_paths.get(rename.get("to", ""))
        if target_path is None:
            raise ValueError(f"rename target is not a baseline entry: {rename.get('to')!r}")
        groups[target_path]["renames"].append(rename)

    if destination.exists():
        if not replace:
            raise ValueError(f"destination exists; pass --replace to synchronize it: {destination}")
        files = [path for path in destination.rglob("*") if path.is_file()]
        unexpected = [path for path in files if path.suffix != ".json"]
        if unexpected:
            raise ValueError(f"destination contains non-JSON files: {unexpected}")
        for path in files:
            path.unlink()
        for directory in sorted(
            (path for path in destination.rglob("*") if path.is_dir()),
            key=lambda path: len(path.parts),
            reverse=True,
        ):
            directory.rmdir()
    destination.mkdir(parents=True)
    for relative in sorted(groups, key=lambda path: path.as_posix()):
        group = groups[relative]
        group["entries"].sort(key=issue_key)
        group["renames"].sort(key=lambda item: (item["from"], item["to"]))
        fragment = {
            "version": 1,
            "source_commit": document.get("source_commit", ""),
            "entries": group["entries"],
        }
        if group["renames"]:
            fragment["renames"] = group["renames"]
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(fragment, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--replace", action="store_true")
    args = parser.parse_args()
    try:
        shard(args.source.resolve(), args.destination.resolve(), args.replace)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
