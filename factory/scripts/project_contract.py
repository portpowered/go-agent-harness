"""Immutable harness project identity and packet preflight."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


class ContractError(ValueError):
    pass


def root_path() -> Path:
    configured = os.environ.get("FACTORY_ROOT")
    if configured:
        return Path(configured).resolve()
    return Path(subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True).strip()).resolve()


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def read_json(path: Path) -> dict:
    if path.stat().st_size > 2 * 1024 * 1024:
        raise ContractError("JSON document exceeds 2 MiB")
    result = json.loads(path.read_text())
    if not isinstance(result, dict):
        raise ContractError("JSON document must be an object")
    return result


def manifest(root: Path | None = None) -> dict:
    root = root or root_path()
    path = Path(os.environ.get("FACTORY_PROJECT_MANIFEST", str(
        root / "factory/projects/audio-runtime/manifest.json")))
    result = read_json(path)
    if result.get("version") != "harness-project.v1":
        raise ContractError("unknown project manifest version")
    for key in ("project", "contractRevision"):
        if not isinstance(result.get(key), str) or not result[key].strip():
            raise ContractError("manifest requires " + key)
    for key in ("sourcePlan", "request", "acceptance"):
        entry = result.get("authority", {}).get(key, {})
        source = (root / entry.get("path", "")).resolve()
        if not source.is_relative_to(root) or not source.is_file():
            raise ContractError("authority path must be a file inside the repository")
        if digest(source) != entry.get("sha256"):
            raise ContractError("immutable authority digest mismatch: " + key)
    criteria = result.get("criteria")
    if not isinstance(criteria, list) or not criteria:
        raise ContractError("manifest requires criteria")
    ids = [item.get("id") for item in criteria if isinstance(item, dict)]
    if len(ids) != len(criteria) or len(set(ids)) != len(ids) or not all(ids):
        raise ContractError("criterion identities must be unique and nonempty")
    return result


def check_packet(packet: dict, contract: dict) -> None:
    if not isinstance(packet, dict):
        raise ContractError("work packet must be an object")
    for key in ("project", "contractRevision"):
        if packet.get(key) != contract[key]:
            raise ContractError("work packet has conflicting " + key)


def work_name(name: str) -> str:
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,119}", name):
        raise ContractError("work name must be a safe lowercase identifier")
    return name


def task_packet(root: Path, name: str, contract: dict) -> dict:
    packet = read_json(root / "tasks/todo" / (work_name(name) + ".json"))
    check_packet(packet, contract)
    if packet.get("branchName") != "codex/" + name:
        raise ContractError("task branchName must be codex/<work-name>")
    return packet


def artifact(value: dict) -> dict:
    if not isinstance(value, dict) or set(value) != {"identity", "path", "sha256"}:
        raise ContractError("artifact requires identity, absolute path and sha256")
    path = Path(value["path"])
    if not value["identity"] or not path.is_absolute() or not path.is_file():
        raise ContractError("artifact identity/path missing")
    if digest(path) != value["sha256"]:
        raise ContractError("artifact digest mismatch")
    return value
