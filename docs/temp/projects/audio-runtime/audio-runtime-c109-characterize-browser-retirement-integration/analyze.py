#!/usr/bin/env python3
"""Produce deterministic, evidence-only C109 integration characterization."""

from __future__ import annotations

import argparse
from functools import lru_cache
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Any, Iterable


PROJECT = "audio-runtime"
TASK = "audio-runtime-c109-characterize-browser-retirement-integration"
CONTRACT = "audio-runtime-v1"
BRANCH = "codex/audio-runtime-c109-characterize-browser-retirement-integration"
STARTUP_INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
ACCEPTED_MAIN = "d4766c3dbbf2c198142047ead4449d58dd47d485"
C61 = "8e8177c031a7b3b9322d712af19970e13fa7a1bc"
C83 = "22cc6769aaf06d1e2c1275b064cc7ec29de3e371"
SHARED_RUNNER = "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go"
OWNED_PREFIX = "docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/"
C79_PATHS = {
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
}
MANIFEST_GATES = (
    "AUDIO",
    "DEVICE",
    "EMBED",
    "SERVICE",
    "TRACE",
    "REPLAY",
    "FAILURES",
    "QUALITY",
    "PARITY",
)
FIXED_DATE = "2020-01-01T00:00:00Z"

CANDIDATE_BRANCHES = {
    "c61": "codex/audio-runtime-c61-retire-cli-browser-scenario-contract",
    "c83": "codex/audio-runtime-c83-retire-cli-browser-scenario-runner",
}
PRESERVED_WORKTREES = {
    "c61": {
        "path": ".claude/worktrees/audio-runtime-c61-retire-cli-browser-scenario-contract",
        "branch": CANDIDATE_BRANCHES["c61"],
        "commit": C61,
    },
    "c83": {
        "path": ".claude/worktrees/audio-runtime-c83-retire-cli-browser-scenario-runner",
        "branch": CANDIDATE_BRANCHES["c83"],
        "commit": C83,
    },
}


class EvidenceError(RuntimeError):
    """Raised when evidence cannot be established without guessing."""


def command_text(argv: Iterable[str]) -> str:
    return " ".join(str(part) for part in argv)


def safe_environment() -> dict[str, str]:
    markers = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    return {
        name: value
        for name, value in os.environ.items()
        if not any(marker in name.upper() for marker in markers)
    }


def run(
    argv: list[str],
    cwd: Path,
    *,
    check: bool = True,
    env: dict[str, str] | None = None,
    timeout: int = 120,
) -> dict[str, Any]:
    merged_env = safe_environment()
    if env:
        merged_env.update(env)
    try:
        completed = subprocess.run(
            argv,
            cwd=cwd,
            env=merged_env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raw_output = exc.stdout or ""
        output = raw_output.decode(errors="replace") if isinstance(raw_output, bytes) else raw_output
        result = {
            "command": command_text(argv),
            "status": "timeout",
            "exit_code": None,
            "output": output,
            "output_bytes": len(output.encode()),
            "output_sha256": hashlib.sha256(output.encode()).hexdigest(),
        }
        if check:
            raise EvidenceError(f"timeout: {command_text(argv)}\n{output[-4000:]}") from exc
        return result
    output = completed.stdout or ""
    result = {
        "command": command_text(argv),
        "status": "passed" if completed.returncode == 0 else "failed",
        "exit_code": completed.returncode,
        "output": output,
        "output_bytes": len(output.encode()),
        "output_sha256": hashlib.sha256(output.encode()).hexdigest(),
    }
    if check and completed.returncode != 0:
        raise EvidenceError(f"command failed: {command_text(argv)}\n{output[-6000:]}")
    return result


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceError(message)


def git(root: Path, args: list[str], *, check: bool = True, cwd: Path | None = None, timeout: int = 120) -> dict[str, Any]:
    return run(["git", *args], cwd=cwd or root, check=check, timeout=timeout)


def git_output(root: Path, args: list[str], *, cwd: Path | None = None, check: bool = True) -> str:
    return git(root, args, cwd=cwd, check=check)["output"].strip()


def repository_root(worktree: Path) -> Path:
    configured = os.environ.get("FACTORY_ROOT")
    if configured:
        configured_path = Path(configured).resolve()
        if (configured_path / ".claude" / "worktrees").is_dir():
            return configured_path
    top = Path(git_output(worktree, ["rev-parse", "--show-toplevel"])).resolve()
    for ancestor in (top, *top.parents):
        if (ancestor / ".claude" / "worktrees").is_dir():
            return ancestor
    common = git_output(worktree, ["rev-parse", "--path-format=absolute", "--git-common-dir"], check=False)
    if common:
        common_path = Path(common).resolve()
        if common_path.name == ".git":
            return common_path.parent
    return Path(git_output(worktree, ["rev-parse", "--show-toplevel"])).resolve()


def revision(root: Path, ref: str, *, cwd: Path | None = None) -> str:
    value = git_output(root, ["rev-parse", f"{ref}^{{commit}}"], cwd=cwd)
    require(bool(re.fullmatch(r"[0-9a-f]{40}", value)), f"non-commit ref resolution for {ref!r}")
    return value


def tree(root: Path, ref: str, *, cwd: Path | None = None) -> str:
    value = git_output(root, ["rev-parse", f"{ref}^{{tree}}"], cwd=cwd)
    require(bool(re.fullmatch(r"[0-9a-f]{40}", value)), f"non-tree ref resolution for {ref!r}")
    return value


def maybe_blob(root: Path, ref: str, path: str, *, cwd: Path | None = None) -> str | None:
    result = git(root, ["rev-parse", f"{ref}:{path}"], cwd=cwd, check=False)
    if result["exit_code"] != 0:
        return None
    value = result["output"].strip()
    return value if re.fullmatch(r"[0-9a-f]{40}", value) else None


def source_text(root: Path, ref: str, path: str) -> str:
    result = git(root, ["show", f"{ref}:{path}"], check=False)
    if result["exit_code"] != 0:
        return ""
    return result["output"]


@lru_cache(maxsize=4096)
def source_model(root: Path, ref: str, path: str) -> tuple[str, list[dict[str, Any]], list[dict[str, Any]], dict[str, Any]]:
    """Cache the complete source/token/scope model for deterministic repeated scans."""
    text = source_text(root, ref, path)
    return text, go_tokens(text), declarations(text), package_scope(text, path)


def status_lines(root: Path, *, cwd: Path | None = None) -> list[str]:
    output = git_output(
        root,
        ["status", "--porcelain=v1", "--untracked-files=all"],
        cwd=cwd,
    )
    return output.splitlines() if output else []


def scoped_ref_names() -> set[str]:
    names = {
        "refs/remotes/origin/main",
        "refs/heads/c109-c61-readonly",
        "refs/heads/c109-c83-readonly",
    }
    for branch in CANDIDATE_BRANCHES.values():
        names.add(f"refs/heads/{branch}")
        names.add(f"refs/remotes/origin/{branch}")
    return names


def ref_snapshot(root: Path) -> list[dict[str, str]]:
    output = git_output(root, ["for-each-ref", "--format=%(refname) %(objectname)"], check=False)
    records: list[dict[str, str]] = []
    wanted = scoped_ref_names()
    for line in output.splitlines():
        name, _, object_id = line.partition(" ")
        if name in wanted and re.fullmatch(r"[0-9a-f]{40}", object_id):
            records.append({"name": name, "object": object_id})
    return sorted(records, key=lambda item: item["name"])


def candidate_ref_values(records: list[dict[str, str]]) -> dict[str, str]:
    return {
        item["name"]: item["object"]
        for item in records
        if item["name"] != "refs/remotes/origin/main"
    }


def parse_worktrees(root: Path) -> list[dict[str, Any]]:
    """Parse the complete porcelain stream before applying evidence scoping."""
    result = git(root, ["worktree", "list", "--porcelain"])
    records: list[dict[str, Any]] = []
    current: dict[str, Any] = {}
    for line in result["output"].splitlines() + [""]:
        if line.startswith("worktree "):
            if current:
                records.append(current)
            current = {"path": line[len("worktree ") :]}
        elif line.startswith("HEAD "):
            current["head"] = line[len("HEAD ") :]
        elif line == "detached":
            current["detached"] = True
        elif line.startswith("branch "):
            current["branch"] = line[len("branch ") :]
        elif line.startswith("locked"):
            current["locked"] = line[len("locked") :].strip() or True
        elif line.startswith("prunable "):
            current["prunable"] = line[len("prunable ") :]
        elif line == "" and current:
            records.append(current)
            current = {}
    return records


def relative_path(path: Path, base: Path) -> str:
    try:
        return path.resolve().relative_to(base.resolve()).as_posix()
    except ValueError:
        return "<outside-project-root>"


def preserved_snapshot(root: Path) -> dict[str, Any]:
    project = repository_root(root)
    inventory = {
        Path(record["path"]).resolve(): record
        for record in parse_worktrees(root)
        if record.get("path")
    }
    result: dict[str, Any] = {"complete": True, "worktrees": {}}
    for name, expected in PRESERVED_WORKTREES.items():
        path = (project / expected["path"]).resolve()
        record = inventory.get(path)
        require(record is not None and path.is_dir(), f"preserved {name} worktree is missing: {path}")
        branch = git_output(root, ["symbolic-ref", "--short", "-q", "HEAD"], cwd=path, check=False)
        head = revision(root, "HEAD", cwd=path)
        status = status_lines(root, cwd=path)
        require(branch == expected["branch"], f"preserved {name} worktree branch changed: {branch!r}")
        require(head == expected["commit"], f"preserved {name} worktree head changed: {head}")
        require(not status, f"preserved {name} worktree is dirty: {status}")
        result["worktrees"][name] = {
            "path": expected["path"],
            "registered": True,
            "present": True,
            "head": head,
            "tree": tree(root, "HEAD", cwd=path),
            "branch": branch,
            "branch_ref": record.get("branch", ""),
            "status": status,
        }
    return result


def scoped_worktree_inventory(root: Path) -> dict[str, Any]:
    project = repository_root(root)
    current = Path(git_output(root, ["rev-parse", "--show-toplevel"])).resolve()
    relevant = {current: "current"}
    for name, expected in PRESERVED_WORKTREES.items():
        relevant[(project / expected["path"]).resolve()] = f"preserved-{name}"
    records: list[dict[str, Any]] = []
    for raw in parse_worktrees(root):
        path = Path(raw["path"]).resolve()
        scope = relevant.get(path)
        if scope is None:
            continue
        records.append(
            {
                "scope": scope,
                "path": relative_path(path, project),
                "head": raw.get("head", ""),
                "branch": raw.get("branch", ""),
                "detached": bool(raw.get("detached", False)),
                "locked": raw.get("locked", False),
                "prunable": raw.get("prunable", ""),
            }
        )
    return {
        "complete": True,
        "normalization": "complete porcelain was parsed; unrelated mutable worktrees are omitted from deterministic evidence",
        "records": sorted(records, key=lambda item: item["path"]),
    }


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def diff_paths(root: Path, base: str, head: str) -> list[str]:
    output = git_output(root, ["diff", "--name-only", f"{base}...{head}"])
    return sorted(line for line in output.splitlines() if line)


GO_IDENTIFIER = re.compile(r"[A-Za-z_]\w*")


def mask_go_non_code(text: str) -> str:
    """Blank comments and literals while preserving newlines and columns."""
    chars = list(text)
    index = 0
    length = len(text)
    while index < length:
        if text.startswith("//", index):
            end = text.find("\n", index)
            end = length if end < 0 else end
            for position in range(index, end):
                chars[position] = " "
            index = end
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = length - 2 if end < 0 else end
            for position in range(index, min(length, end + 2)):
                if text[position] != "\n":
                    chars[position] = " "
            index = min(length, end + 2)
            continue
        if text[index] in {"\"", "'", "`"}:
            delimiter = text[index]
            position = index + 1
            while position < length:
                if delimiter != "`" and text[position] == "\\":
                    position += 2
                    continue
                if text[position] == delimiter:
                    position += 1
                    break
                position += 1
            for blank in range(index, min(length, position)):
                if text[blank] != "\n":
                    chars[blank] = " "
            index = position
            continue
        index += 1
    return "".join(chars)


def go_tokens(text: str) -> list[dict[str, Any]]:
    """Return source-derived Go identifier/punctuation tokens with locations."""
    masked = mask_go_non_code(text)
    tokens: list[dict[str, Any]] = []
    for line_number, line in enumerate(masked.splitlines(), 1):
        for match in GO_IDENTIFIER.finditer(line):
            tokens.append(
                {
                    "value": match.group(0),
                    "line": line_number,
                    "column_start": match.start() + 1,
                    "column_end": match.end(),
                }
            )
        for position, value in enumerate(line, 1):
            if value in "{}().,;=*[]":
                tokens.append({"value": value, "line": line_number, "column_start": position, "column_end": position})
    return sorted(tokens, key=lambda token: (token["line"], token["column_start"]))


def token_in_span(token: dict[str, Any], value: dict[str, Any]) -> bool:
    span_value = value.get("span", {})
    return span_value.get("start_line", 0) <= token.get("line", 0) <= span_value.get("end_line", 0)


def token_span(token: dict[str, Any]) -> dict[str, int]:
    return {
        "start_line": token["line"],
        "end_line": token["line"],
        "line_count": 1,
        "start_column": token["column_start"],
        "end_column": token["column_end"],
    }


def matching_brace_line(tokens: list[dict[str, Any]], start: int) -> int:
    depth = 0
    opened = False
    for token in tokens[start:]:
        if token["value"] == "{":
            depth += 1
            opened = True
        elif token["value"] == "}":
            depth -= 1
        if opened and depth == 0:
            return token["line"]
    return tokens[start]["line"] if start < len(tokens) else 1


def declaration_name_token(tokens: list[dict[str, Any]], index: int, kind: str) -> dict[str, Any] | None:
    if index >= len(tokens):
        return None
    if kind == "func":
        cursor = index + 1
        if cursor < len(tokens) and tokens[cursor]["value"] == "(":
            depth = 0
            while cursor < len(tokens):
                if tokens[cursor]["value"] == "(":
                    depth += 1
                elif tokens[cursor]["value"] == ")":
                    depth -= 1
                    if depth == 0:
                        cursor += 1
                        break
                cursor += 1
        while cursor < len(tokens) and not GO_IDENTIFIER.fullmatch(tokens[cursor]["value"]):
            cursor += 1
        return tokens[cursor] if cursor < len(tokens) else None
    cursor = index + 1
    while cursor < len(tokens) and not GO_IDENTIFIER.fullmatch(tokens[cursor]["value"]):
        cursor += 1
    return tokens[cursor] if cursor < len(tokens) else None


def declarations(text: str) -> list[dict[str, Any]]:
    lines = text.splitlines()
    tokens = go_tokens(text)
    records: list[dict[str, Any]] = []
    for index, token in enumerate(tokens):
        if token["value"] not in {"func", "type"}:
            continue
        kind = "function" if token["value"] == "func" else "type"
        name_token = declaration_name_token(tokens, index, token["value"])
        if name_token is None:
            continue
        name = name_token["value"]
        if kind == "function" and index + 1 < len(tokens) and tokens[index + 1]["value"] == "(":
            receiver_end = index + 2
            depth = 1
            while receiver_end < len(tokens) and depth:
                if tokens[receiver_end]["value"] == "(":
                    depth += 1
                elif tokens[receiver_end]["value"] == ")":
                    depth -= 1
                receiver_end += 1
            receiver_tokens = tokens[index + 2 : receiver_end]
            receiver_identifiers = [item["value"] for item in receiver_tokens if GO_IDENTIFIER.fullmatch(item["value"])]
            if receiver_identifiers:
                name = f"{receiver_identifiers[-1]}.{name}"
        start_line = token["line"]
        end_line = start_line
        brace_index = next((probe for probe in range(index + 1, len(tokens)) if tokens[probe]["value"] == "{"), None)
        if brace_index is not None and tokens[brace_index]["line"] <= start_line + 800:
            end_line = matching_brace_line(tokens, brace_index)
        records.append(
            {
                "symbol": name,
                "kind": kind,
                "span": {"start_line": start_line, "end_line": end_line},
                "declaration": lines[start_line - 1].strip() if start_line <= len(lines) else "",
                "name_span": token_span(name_token),
            }
        )
    unique: dict[tuple[str, int, str], dict[str, Any]] = {}
    for item in records:
        unique[(item["symbol"], item["span"]["start_line"], item["kind"])] = item
    return sorted(unique.values(), key=lambda item: (item["span"]["start_line"], item["symbol"]))


def declaration_for_line(items: list[dict[str, Any]], line_number: int) -> dict[str, Any] | None:
    containing = [
        item
        for item in items
        if item["span"]["start_line"] <= line_number <= item["span"]["end_line"]
    ]
    if containing:
        return min(containing, key=lambda item: item["span"]["end_line"] - item["span"]["start_line"])
    preceding = [item for item in items if item["span"]["start_line"] <= line_number]
    return max(preceding, key=lambda item: item["span"]["start_line"]) if preceding else None


def package_scope(text: str, path: str) -> dict[str, Any]:
    lines = text.splitlines()
    match = re.search(r"(?m)^\s*package\s+([A-Za-z_]\w*)\b", mask_go_non_code(text))
    if match is None:
        return {
            "symbol": f"package:{Path(path).parent.name}",
            "kind": "package",
            "span": {"start_line": 1, "end_line": 1},
            "declaration": lines[0].strip() if lines else "",
        }
    line = text.count("\n", 0, match.start()) + 1
    return {
        "symbol": f"package:{match.group(1)}",
        "kind": "package",
        "span": {"start_line": line, "end_line": line},
        "declaration": lines[line - 1].strip(),
    }


def source_scope(text: str, path: str, line_number: int) -> dict[str, Any]:
    return declaration_for_line(declarations(text), line_number) or package_scope(text, path)


def caller_edges(
    root: Path,
    ref: str,
    path: str,
    symbol: str,
    declaration: dict[str, Any] | None,
    search_paths: list[str],
) -> list[dict[str, Any]]:
    term = symbol.rsplit(".", 1)[-1]
    edges: list[dict[str, Any]] = []
    for search_path in search_paths:
        text, tokens, local_declarations, package = source_model(root, ref, search_path)
        if not text:
            continue
        lines = text.splitlines()
        for token in tokens:
            if token["value"] != term:
                continue
            if declaration and search_path == path and token_in_span(token, declaration) and token.get("column_start") == declaration.get("name_span", {}).get("start_column"):
                continue
            if any(token.get("column_start") == item.get("name_span", {}).get("start_column") and token_in_span(token, item) and item["symbol"].rsplit(".", 1)[-1] == term for item in local_declarations):
                continue
            caller = declaration_for_line(local_declarations, token["line"]) or package
            source_line = lines[token["line"] - 1] if token["line"] <= len(lines) else ""
            caller_span = caller["span"]
            edges.append(
                {
                    "kind": "source-reference",
                    "source_derived": "go-token-v1",
                    "search_term": term,
                    "path": search_path,
                    "line": token["line"],
                    "text": source_line,
                    "source_span": token_span(token),
                    "target": {
                        "symbol": symbol,
                        "path": path,
                        "ref": ref,
                        "declaration": {
                            "path": path,
                            "line": declaration["span"]["start_line"] if declaration else None,
                            "text": declaration["declaration"] if declaration else "",
                            "span": declaration["span"] if declaration else {},
                        },
                    },
                    "caller": {
                        "symbol": caller["symbol"],
                        "kind": caller["kind"],
                        "span": caller_span,
                        "declaration": {
                            "ref": ref,
                            "path": search_path,
                            "line": caller_span["start_line"],
                            "text": caller["declaration"],
                        },
                    },
                    "caller_symbol": caller["symbol"],
                    "caller_span": caller_span,
                }
            )
    return edges


def span(start: int, count: int) -> dict[str, int]:
    return {
        "start_line": start,
        "end_line": start + count - 1 if count else start,
        "line_count": count,
    }


def parse_hunks(patch: str, branch: str, path: str) -> list[dict[str, Any]]:
    lines = patch.splitlines()
    result: list[dict[str, Any]] = []
    current: dict[str, Any] | None = None
    for line in lines:
        match = re.match(r"^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$", line)
        if match:
            if current is not None:
                result.append(current)
            old_count = int(match.group(2) or "1")
            new_count = int(match.group(4) or "1")
            current = {
                "evidence_id": f"{branch}-hunk-{hashlib.sha256(path.encode()).hexdigest()[:10]}-{len(result) + 1:03d}",
                "branch": branch,
                "path": path,
                "old_span": span(int(match.group(1)), old_count),
                "new_span": span(int(match.group(3)), new_count),
                "context": match.group(5).strip(),
                "lines": [line],
                "added_lines": [],
                "deleted_lines": [],
            }
            continue
        if current is None:
            continue
        current["lines"].append(line)
        if line.startswith("+") and not line.startswith("+++"):
            current["added_lines"].append(line[1:])
        elif line.startswith("-") and not line.startswith("---"):
            current["deleted_lines"].append(line[1:])
    if current is not None:
        result.append(current)
    for item in result:
        raw = "\n".join(item.pop("lines")) + "\n"
        item["patch_sha256"] = hashlib.sha256(raw.encode()).hexdigest()
        item["added_line_count"] = len(item["added_lines"])
        item["deleted_line_count"] = len(item["deleted_lines"])
        item["change_kind"] = (
            "added" if not item["deleted_lines"] else "deleted" if not item["added_lines"] else "modified"
        )
    return result


def order_judgment(path: str, branch: str) -> dict[str, Any]:
    if path in C79_PATHS:
        owner = "C79/work-task-114"
    else:
        owner = "C61/work-task-34" if branch == "c61" else "C83/work-task-125"
    return {
        "required_before": False,
        "mechanically_order_independent": True,
        "procedural_delivery_order": ["C61/work-task-34", "C83/work-task-125"],
        "resolution_owner": owner,
        "reason": "C109 characterizes both orders; task ownership and shared-registry serialization remain external prerequisites.",
    }


def branch_owner(branch: str, path: str) -> str:
    if path in C79_PATHS:
        return "C79/work-task-114"
    return "C61/work-task-34" if branch == "c61" else "C83/work-task-125"


def hunk_symbol_evidence(
    root: Path,
    base: str,
    candidate: str,
    branch: str,
    path: str,
    hunk: dict[str, Any],
    search_paths: list[str],
) -> list[dict[str, Any]]:
    base_declarations = source_model(root, base, path)[2]
    candidate_declarations = source_model(root, candidate, path)[2]
    selected: list[tuple[str, dict[str, Any], dict[str, Any] | None]] = []
    for ref_name, items, line_number in (
        ("base", base_declarations, hunk["old_span"]["start_line"]),
        (branch, candidate_declarations, hunk["new_span"]["start_line"]),
    ):
        item = declaration_for_line(items, line_number)
        if item and not any(existing[1]["symbol"] == item["symbol"] for existing in selected):
            selected.append((ref_name, item, item))
    if not selected:
        file_symbol = {
            "symbol": f"file:{path}",
            "kind": "file-hunk",
            "span": hunk["new_span"] if hunk["new_span"]["line_count"] else hunk["old_span"],
            "declaration": {"ref": branch, "path": path, "line": hunk["new_span"]["start_line"], "text": hunk["context"]},
        }
        selected.append((branch, file_symbol, None))
    records: list[dict[str, Any]] = []
    for index, (ref_name, item, declaration) in enumerate(selected, 1):
        symbol = item["symbol"]
        ref_for_callers = base if ref_name == "base" else candidate
        callers = [] if symbol.startswith("file:") else caller_edges(root, ref_for_callers, path, symbol, declaration, search_paths)
        records.append(
            {
                "evidence_id": f"{hunk['evidence_id']}-symbol-{index:02d}",
                "hunk_evidence_id": hunk["evidence_id"],
                "branch": branch,
                "path": path,
                "symbol": symbol,
                "kind": item["kind"],
                "span": item["span"],
                "name_span": item.get("name_span", item["span"]),
                "declaration": {
                    "ref": ref_for_callers,
                    "path": path,
                    "line": item["span"]["start_line"],
                    "text": item["declaration"],
                    "span": item["span"],
                },
                "caller_edges": callers,
                "blob_hashes": {
                    "base": maybe_blob(root, base, path),
                    "candidate": maybe_blob(root, candidate, path),
                },
                "presence": {
                    "base": any(decl["symbol"] == symbol for decl in base_declarations),
                    "candidate": any(decl["symbol"] == symbol for decl in candidate_declarations),
                },
                "conflict_kind": "shared-file-no-content-conflict" if path == SHARED_RUNNER else "independent-file-hunk",
                "resolution_owner": branch_owner(branch, path),
                "order_judgment": order_judgment(path, branch),
                "evidence_ids": [hunk["evidence_id"]],
            }
        )
    return records


def file_record(
    root: Path,
    base: str,
    c61: str,
    c83: str,
    path: str,
    c61_paths: list[str],
    c83_paths: list[str],
    shared_paths: list[str],
) -> dict[str, Any]:
    merge_base = git_output(root, ["merge-base", base, c61])
    base_blob = maybe_blob(root, merge_base, path)
    c61_blob = maybe_blob(root, c61, path)
    c83_blob = maybe_blob(root, c83, path)
    evidence: list[dict[str, Any]] = []
    branch_specs = (("c61", c61, c61_paths), ("c83", c83, c83_paths))
    for branch, candidate, changed_paths in branch_specs:
        if path not in changed_paths:
            continue
        patch = git_output(root, ["diff", "--unified=0", f"{base}...{candidate}", "--", path])
        for hunk in parse_hunks(patch, branch, path):
            hunk["symbols"] = hunk_symbol_evidence(root, merge_base, candidate, branch, path, hunk, sorted(set(c61_paths + c83_paths)))
            evidence.append(hunk)
    owner = "C79/work-task-114" if path in C79_PATHS else (
        "C61/work-task-34 and C83/work-task-125" if path in shared_paths else
        "C61/work-task-34" if path in c61_paths else "C83/work-task-125"
    )
    return {
        "path": path,
        "owner": owner,
        "merge_base": merge_base,
        "blob_hashes": {"base": base_blob, "c61": c61_blob, "c83": c83_blob},
        "base_commit": merge_base,
        "base_blob": base_blob,
        "c61_blob": c61_blob,
        "c83_blob": c83_blob,
        "changed_by_c61": c61_blob != base_blob,
        "changed_by_c83": c83_blob != base_blob,
        "hunks": evidence,
        "symbols": [symbol for hunk in evidence for symbol in hunk["symbols"]],
        "conflict": {
            "kind": "shared-file-no-content-conflict" if path in shared_paths else "none",
            "paths": [path] if path in shared_paths else [],
            "resolution_owner": owner,
            "evidence_ids": [hunk["evidence_id"] for hunk in evidence],
        },
        "order_judgment": {
            "required_before": False,
            "mechanically_order_independent": True,
            "procedural_delivery_order": ["C61/work-task-34", "C83/work-task-125"],
            "reason": "No C109 candidate source is resolved or promoted; procedural ordering is retained for the existing owners.",
        },
    }


def added_diff_lines(root: Path, base: str, candidate: str, path: str) -> list[dict[str, Any]]:
    patch = git_output(root, ["diff", "--unified=0", f"{base}...{candidate}", "--", path])
    matches: list[dict[str, Any]] = []
    new_line = 0
    in_hunk = False
    for line in patch.splitlines():
        header = re.match(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@", line)
        if header:
            new_line = int(header.group(1))
            in_hunk = True
            continue
        if not in_hunk:
            continue
        if line.startswith("+++") or line.startswith("---"):
            continue
        if line.startswith("+"):
            matches.append({"path": path, "line": new_line, "text": line[1:]})
            new_line += 1
        elif line.startswith(" "):
            new_line += 1
    return matches


def module_path(root: Path, ref: str, module_directory: str) -> str:
    text = source_text(root, ref, f"{module_directory}/go.mod")
    match = re.search(r"(?m)^\s*module\s+(\S+)\s*$", text)
    require(match is not None, f"module path is missing from {module_directory}/go.mod at {ref}")
    return match.group(1)


def go_imports(text: str) -> list[dict[str, Any]]:
    """Parse import declarations from source, retaining exact line/column spans."""
    imports: list[dict[str, Any]] = []
    lines = text.splitlines()
    in_group = False
    for line_number, line in enumerate(lines, 1):
        stripped = line.strip()
        if not in_group and re.match(r"^import\s*\(", stripped):
            in_group = True
        elif not in_group and re.match(r"^import\s+", stripped):
            in_group = False
        elif in_group and stripped == ")":
            in_group = False
            continue
        if not in_group and not re.match(r"^import\s+", stripped) and not (stripped.startswith('"') or re.match(r"^[A-Za-z_.]\w*\s+\"", stripped)):
            continue
        for match in re.finditer(r"(?:(?P<alias>[A-Za-z_]\w*|\.)\s+)?\"(?P<path>[^\"]+)\"", line):
            alias = match.group("alias") or match.group("path").rsplit("/", 1)[-1]
            imports.append(
                {
                    "alias": alias,
                    "path": match.group("path"),
                    "line": line_number,
                    "source_span": {
                        "start_line": line_number,
                        "end_line": line_number,
                        "line_count": 1,
                        "start_column": match.start() + 1,
                        "end_column": match.end(),
                    },
                    "text": line,
                }
            )
    return imports


def declaration_identity(ref: str, path: str, item: dict[str, Any]) -> dict[str, Any]:
    return {
        "ref": ref,
        "path": path,
        "line": item["span"]["start_line"],
        "text": item["declaration"],
        "span": item["span"],
    }


def caller_identity(ref: str, path: str, text: str, line_number: int) -> dict[str, Any]:
    item = source_scope(text, path, line_number)
    return {
        "symbol": item["symbol"],
        "kind": item["kind"],
        "span": item["span"],
        "declaration": declaration_identity(ref, path, item),
    }


def api_edge(
    *,
    kind: str,
    target: dict[str, Any],
    ref: str,
    path: str,
    text: str,
    source_span: dict[str, int],
) -> dict[str, Any]:
    line_number = source_span["start_line"]
    caller = caller_identity(ref, path, text, line_number)
    return {
        "evidence_id": "api-edge-" + hashlib.sha256(
            f"{kind}|{path}|{line_number}|{source_span['start_column']}|{target.get('symbol', '')}".encode()
        ).hexdigest()[:20],
        "kind": kind,
        "source_derived": "go-token-v1",
        "path": path,
        "ref": ref,
        "line": line_number,
        "text": text.splitlines()[line_number - 1] if line_number <= len(text.splitlines()) else "",
        "source_span": source_span,
        "target": target,
        "caller": caller,
        "caller_symbol": caller["symbol"],
        "caller_span": caller["span"],
    }


def api_relationship(root: Path, c61: str, c83: str, c61_paths: list[str], c83_paths: list[str]) -> dict[str, Any]:
    """Derive cross-candidate API edges from complete Go source, not a name list."""
    scanned_paths = sorted(path for path in set(c83_paths) if path.endswith(".go"))
    c61_go_paths = sorted(path for path in set(c61_paths) if path.endswith(".go"))
    c61_public_paths = sorted(
        {
            str(Path(path).parent).replace("\\", "/")
            for path in c61_go_paths
            if path.startswith("go-agent-runtime/services/browserscenario/") and "/internal/" not in path
        }
    )
    runtime_module = module_path(root, c61, "go-agent-runtime")
    public_imports = sorted(f"{runtime_module}/{path}" for path in c61_public_paths)
    c61_only_symbols: list[dict[str, Any]] = []
    for path in c61_go_paths:
        candidate_items = source_model(root, c61, path)[2]
        base_items = {item["symbol"] for item in source_model(root, ACCEPTED_MAIN, path)[2]}
        for item in candidate_items:
            if item["symbol"] not in base_items:
                c61_only_symbols.append(
                    {
                        "symbol": item["symbol"],
                        "kind": item["kind"],
                        "path": path,
                        "ref": c61,
                        "span": item["span"],
                        "declaration": declaration_identity(c61, path, item),
                        "public_api": any(path.startswith(prefix + "/") or path == prefix for prefix in c61_public_paths),
                    }
                )
    c61_only_symbols.sort(key=lambda item: (item["path"], item["span"]["start_line"], item["symbol"]))
    symbol_targets = {item["symbol"]: item for item in c61_only_symbols}
    public_targets = {
        (item["path"], item["symbol"]): item
        for item in c61_only_symbols
        if item["public_api"]
    }
    edges: list[dict[str, Any]] = []
    scans: list[dict[str, Any]] = []
    for path in scanned_paths:
        text, tokens, local_declarations, _ = source_model(root, c83, path)
        imports = go_imports(text)
        import_by_alias = {item["alias"]: item for item in imports}
        scans.append(
            {
                "path": path,
                "ref": c83,
                "blob_hash": maybe_blob(root, c83, path),
                "source_sha256": hashlib.sha256(text.encode()).hexdigest(),
                "token_count": len(tokens),
                "imports": imports,
                "declarations": local_declarations,
            }
        )
        for item in imports:
            if item["path"] not in public_imports:
                continue
            target_path = next((candidate for candidate in c61_public_paths if item["path"] == f"{runtime_module}/{candidate}"), c61_public_paths[0] if c61_public_paths else "")
            target_item = next((value for (candidate_path, _), value in public_targets.items() if candidate_path == target_path), None)
            target = {
                "symbol": f"package:{item['path']}",
                "path": target_path,
                "ref": c61,
                "declaration": target_item["declaration"] if target_item else {"ref": c61, "path": f"{target_path}/", "line": None, "text": ""},
            }
            edges.append(api_edge(kind="public-import", target=target, ref=c83, path=path, text=text, source_span=item["source_span"]))
        for index, token in enumerate(tokens):
            if index + 2 >= len(tokens) or tokens[index + 1]["value"] != ".":
                continue
            imported = import_by_alias.get(token["value"])
            target_symbol = tokens[index + 2]["value"]
            if imported is None or imported["path"] not in public_imports:
                continue
            target_path = next((candidate for candidate in c61_public_paths if imported["path"] == f"{runtime_module}/{candidate}"), "")
            target_item = public_targets.get((target_path, target_symbol))
            if target_item is None:
                continue
            edges.append(
                api_edge(
                    kind="qualified-public-symbol",
                    target={
                        "symbol": target_symbol,
                        "path": target_path,
                        "ref": c61,
                        "declaration": target_item["declaration"],
                    },
                    ref=c83,
                    path=path,
                    text=text,
                    source_span={
                        **token_span(tokens[index + 2]),
                        "start_column": token["column_start"],
                        "end_column": tokens[index + 2]["column_end"],
                    },
                )
            )
        declaration_names = {
            (item["span"]["start_line"], item.get("name_span", {}).get("start_column"), item["symbol"].rsplit(".", 1)[-1])
            for item in local_declarations
        }
        for index, token in enumerate(tokens):
            target_item = symbol_targets.get(token["value"])
            if target_item is None or (token["line"], token["column_start"], token["value"]) in declaration_names:
                continue
            if index > 0 and tokens[index - 1]["value"] == ".":
                continue
            edges.append(
                api_edge(
                    kind="same-package-c61-symbol",
                    target={
                        "symbol": target_item["symbol"],
                        "path": target_item["path"],
                        "ref": c61,
                        "declaration": target_item["declaration"],
                    },
                    ref=c83,
                    path=path,
                    text=text,
                    source_span=token_span(token),
                )
            )
    unique_edges: dict[tuple[str, str, int, int, str], dict[str, Any]] = {}
    for edge in edges:
        key = (edge["kind"], edge["path"], edge["line"], edge["source_span"]["start_column"], edge["target"]["symbol"])
        unique_edges[key] = edge
    edges = sorted(unique_edges.values(), key=lambda item: (item["path"], item["line"], item["source_span"]["start_column"], item["kind"], item["target"]["symbol"]))
    return {
        "schema": "audio-runtime-c109-api-relationship-v2",
        "evidence_id": "api-c83-to-c61-source",
        "source_driven": True,
        "base_ref": ACCEPTED_MAIN,
        "c61_ref": c61,
        "c83_ref": c83,
        "c61_public_api_paths": c61_public_paths,
        "c61_public_import_paths": public_imports,
        "c61_only_api_symbols": c61_only_symbols,
        "searched_paths": scanned_paths,
        "scans": scans,
        "edges": edges,
        "c83_requires_c61_api": bool(edges),
        "required_before": False,
        "mechanically_order_independent": not bool(edges),
        "procedural_delivery_order": ["C61/work-task-34", "C83/work-task-125"],
        "reason": (
            "Complete C83 changed Go sources contain no source-derived C61 public import, qualified symbol, or same-package C61-only symbol edge."
            if not edges
            else "C83 has source-derived edges into C61-owned API and must be reconciled by the owning task before delivery."
        ),
    }


def candidate_ref_snapshot(root: Path, ref: str, name: str, key: str) -> dict[str, Any]:
    branch = CANDIDATE_BRANCHES[key]
    return {
        "name": name,
        "ref": ref,
        "commit": revision(root, ref),
        "tree": tree(root, ref),
        "parent_commits": git_output(root, ["rev-list", "--parents", "-n", "1", ref]).split(),
        "branch": branch,
        "branch_ref": f"refs/heads/{branch}",
        "remote_ref": f"refs/remotes/origin/{branch}",
        "status": "preserved-readonly-ref",
    }


def conflict_hunks(root: Path, worktree: Path, paths: list[str]) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    for path in paths:
        diff = git_output(root, ["diff", "--cc", "--", path], cwd=worktree, check=False)
        result.append(
            {
                "path": path,
                "kind": "content-conflict",
                "patch_sha256": hashlib.sha256(diff.encode()).hexdigest(),
                "patch": diff,
                "hunk_count": len(re.findall(r"^@@", diff, flags=re.MULTILINE)),
            }
        )
    return result


def commit_rehearsal(root: Path, base: str, order: list[tuple[str, str]], role: str) -> dict[str, Any]:
    temporary_root = Path(tempfile.mkdtemp(prefix="c109-rehearsal-"))
    worktree = temporary_root / "tree"
    merge_records: list[dict[str, Any]] = []
    cleanup: dict[str, Any] = {
        "worktree_removed": False,
        "temporary_root_removed": False,
        "worktree_remove_command": "git worktree remove --force <temporary-worktree>",
    }
    final: dict[str, Any] = {
        "schema": "audio-runtime-c109-merge-rehearsal-v2",
        "role": role,
        "base": base,
        "base_tree": tree(root, base),
        "order": [label for label, _ in order],
        "merges": merge_records,
        "final_head": None,
        "final_tree": None,
        "final_status": [],
        "worktree_path_recorded": "temporary path intentionally omitted from normalized evidence",
    }
    fixed_env = {
        "GIT_AUTHOR_NAME": "C109 Rehearsal",
        "GIT_AUTHOR_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_COMMITTER_NAME": "C109 Rehearsal",
        "GIT_COMMITTER_EMAIL": "c109-rehearsal@example.invalid",
        "GIT_AUTHOR_DATE": FIXED_DATE,
        "GIT_COMMITTER_DATE": FIXED_DATE,
        "GIT_MERGE_AUTOEDIT": "no",
        "LC_ALL": "C",
    }
    try:
        add = run(["git", "worktree", "add", "--detach", str(worktree), base], cwd=root)
        require(add["status"] == "passed", "cannot create disposable rehearsal worktree")
        for number, (label, ref) in enumerate(order, 1):
            before_head = revision(root, "HEAD", cwd=worktree)
            command = ["git", "merge", "--no-ff", "--no-commit", ref]
            merge = run(command, cwd=worktree, check=False, env=fixed_env)
            conflicted_paths = git_output(root, ["diff", "--name-only", "--diff-filter=U"], cwd=worktree, check=False)
            conflicts = conflict_hunks(root, worktree, conflicted_paths.splitlines() if conflicted_paths else [])
            record: dict[str, Any] = {
                "step": number,
                "label": label,
                "ref": ref,
                "expected_commit": revision(root, ref),
                "pre_merge_head": before_head,
                "command": command_text(command),
                "status": merge["status"],
                "exit_code": merge["exit_code"],
                "conflicted_paths": conflicted_paths.splitlines() if conflicted_paths else [],
                "conflicts": conflicts,
                "conflict_kind": "content-conflict" if conflicts else "none",
                "output_sha256": merge["output_sha256"],
                "output": merge["output"],
                "evidence_ids": [f"{role}-merge-{number:02d}"],
            }
            if merge["exit_code"] == 0:
                commit = run(
                    ["git", "commit", "--no-verify", "-m", f"C109 {role} merge {label}"],
                    cwd=worktree,
                    env=fixed_env,
                )
                commit_head = revision(root, "HEAD", cwd=worktree)
                record.update(
                    {
                        "commit_command": commit["command"],
                        "commit": commit_head,
                        "parents": git_output(root, ["rev-list", "--parents", "-n", "1", "HEAD"], cwd=worktree).split(),
                        "tree": tree(root, "HEAD", cwd=worktree),
                        "post_status": status_lines(root, cwd=worktree),
                        "conflict_resolution": "not-needed",
                        "resolution": {"status": "not-needed", "owner": "C109/none"},
                    }
                )
            else:
                record.update(
                    {
                        "conflict_diff": "\n".join(item["patch"] for item in conflicts),
                        "post_status": status_lines(root, cwd=worktree),
                        "conflict_resolution": "aborted; C109 does not resolve candidate conflicts",
                        "resolution": {"status": "aborted", "owner": "C61/work-task-34 or C83/work-task-125"},
                    }
                )
                git(root, ["merge", "--abort"], cwd=worktree, check=False)
            merge_records.append(record)
            if merge["exit_code"] != 0:
                break
        if merge_records and merge_records[-1]["exit_code"] == 0:
            final.update(
                {
                    "final_head": revision(root, "HEAD", cwd=worktree),
                    "final_tree": tree(root, "HEAD", cwd=worktree),
                    "final_status": status_lines(root, cwd=worktree),
                }
            )
    finally:
        remove = run(["git", "worktree", "remove", "--force", str(worktree)], cwd=root, check=False)
        cleanup["worktree_remove_status"] = remove["status"]
        cleanup["worktree_removed"] = not worktree.exists()
        shutil.rmtree(temporary_root, ignore_errors=True)
        cleanup["temporary_root_removed"] = not temporary_root.exists()
    final["cleanup"] = cleanup
    return final


def historical_c61_finding(root: Path, main: str, c61: str) -> dict[str, Any]:
    fix = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
    test_path = "go-agent-loop/pkg/agentloop/run_delta_barrier_test.go"
    production_path = "go-agent-loop/pkg/agentloop/agent_loop.go"
    fix_ancestor = git(root, ["merge-base", "--is-ancestor", fix, main], check=False)["exit_code"] == 0
    old_blob = maybe_blob(root, c61, test_path)
    main_blob = maybe_blob(root, main, test_path)
    fix_blob = maybe_blob(root, fix, test_path)
    test_declaration = [
        item
        for item in declarations(source_text(root, main, test_path))
        if item["symbol"] == "TestRunJoinsPublishedDeltasBeforeReturningOnEngineError"
    ]
    production_diff = git_output(root, ["show", "--format=fuller", "--stat", "--oneline", fix, "--", production_path, test_path])
    if fix_ancestor and test_declaration and old_blob != main_blob:
        status = "OPEN_RECONCILE_TEST_REWRITTEN"
        conclusion = "The accepted main ancestry contains the production barrier repair, but the focused test blob is not unchanged from C61; the historical C61 finding remains open for task/review reconciliation and is not claimed fixed by C109."
    elif fix_ancestor and test_declaration:
        status = "REPAIRED_WITH_UNCHANGED_TEST"
        conclusion = "The accepted main ancestry contains the repair and the focused test is unchanged."
    else:
        status = "UNRESOLVED_MISSING_REPAIR_OR_SYMBOL"
        conclusion = "The required accepted-main repair or focused symbol evidence is absent."
    return {
        "historical_run": "34658619207",
        "historical_job": "103456263118",
        "historical_failure": "TestRunJoinsPublishedDeltasBeforeReturningOnEngineError lost a published delta after Run returned a terminal engine error.",
        "accepted_main_fix": fix,
        "fix_is_ancestor_of_review_main": fix_ancestor,
        "focused_test_path": test_path,
        "c61_test_blob": old_blob,
        "review_main_test_blob": main_blob,
        "fix_test_blob": fix_blob,
        "focused_test_symbol_evidence": test_declaration,
        "production_and_test_fix_stat": production_diff,
        "status": status,
        "conclusion": conclusion,
        "evidence_id": "c61-historical-delta-barrier-reconciliation",
    }


def ci_attribution() -> dict[str, Any]:
    """Retain the exact historical C83 CI head, jobs, and failure signatures."""
    stale_entries = [
        {
            "kind": "cognitive-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
            "symbol": "*browserConversationInterruptionController.nextEligibleInterruption",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "cyclomatic-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
            "symbol": "*browserConversationInterruptionController.nextEligibleInterruption",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "cognitive-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
            "symbol": "partitionBrowserConversationAudio",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "file-lines",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "file",
            "signature": "baseline entry is stale; the file no longer describes a current violation",
        },
        {
            "kind": "cognitive-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "*browserConversationEvidenceTracker.invocationStep",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "cognitive-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "*browserConversationEvidenceTracker.observe",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "cyclomatic-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "*browserConversationEvidenceTracker.observe",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "function-lines",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "*browserConversationEvidenceTracker.observe",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
        {
            "kind": "function-statements",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
            "symbol": "*browserConversationEvidenceTracker.observe",
            "signature": "baseline entry is stale; the symbol no longer describes a current violation",
        },
    ]
    instrumentation_drifts = [
        {
            "kind": "file-lines",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
            "observed": 995,
            "baseline": 956,
            "signature": "baseline-drift internal/services/internal/agentruntime/session_browser_scenario_run.go (995 > 956)",
        },
        {
            "kind": "file-lines",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
            "observed": 1285,
            "baseline": 1280,
            "signature": "baseline-drift internal/services/internal/agentruntime/session_browser_scenario_runner.go (1285 > 1280)",
        },
        {
            "kind": "cognitive-complexity",
            "path": "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
            "symbol": "*browserConversationBroker.Invoke",
            "observed": 41,
            "baseline": 38,
            "signature": "baseline-drift internal/services/internal/agentruntime/session_browser_scenario_runner.go:*browserConversationBroker.Invoke (41 > 38)",
        },
    ]
    passing_jobs = [
        {"name": "CI (race)", "job_id": "103606090483", "conclusion": "success"},
        {"name": "CI (WebMCP Chrome)", "job_id": "103606090524", "conclusion": "success"},
        {"name": "CI (hermetic)", "job_id": "103606090530", "conclusion": "success"},
        {"name": "CI (coverage)", "job_id": "103606090544", "conclusion": "success"},
        {"name": "CI (Windows audio portable)", "job_id": "103606090574", "conclusion": "success"},
        {"name": "CI (unit)", "job_id": "103606090597", "conclusion": "success"},
        {"name": "CI (macOS audio release)", "job_id": "103606090844", "conclusion": "success"},
    ]
    return {
        "schema": "audio-runtime-c109-ci-attribution-v1",
        "evidence_id": "c83-ci-34713381619",
        "run": "34713381619",
        "repository": "portpowered/go-agent-harness",
        "event": "pull_request",
        "head": {
            "sha": C83,
            "branch": CANDIDATE_BRANCHES["c83"],
            "identity_verified": True,
        },
        "status": "completed",
        "conclusion": "failure",
        "observed_at": "2026-09-12T19:22:11Z",
        "passing_lanes": [job["name"] for job in passing_jobs],
        "passing_jobs": passing_jobs,
        "failed_jobs": [
            {"name": "CI (static)", "job_id": "103606090554", "conclusion": "failure"},
            {"name": "CI (integration)", "job_id": "103606090561", "conclusion": "failure"},
        ],
        "static_findings": {
            "wire_registration": {
                "status": "C79_SHARED_REGISTRY_OWNER",
                "owner": "C79/work-task-114",
                "path": "scripts/wire-packages.txt",
                "job_id": "103606090554",
                "check": "CI (static)",
                "signature": "wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/browserrunner/wire/wire_gen.go'], outside modules=[]",
                "claim": "browserrunner generated Wire package registration is required; C109 does not edit the shared registry.",
                "evidence_id": "c83-static-wire-registration",
            },
            "stale_downward_baseline_entries": {
                "count": 9,
                "status": "C79_SHARED_BASELINE_OWNER",
                "owner": "C79/work-task-114",
                "path": "docs/architecture/architecture-size-baseline.json",
                "job_id": "103606090554",
                "check": "CI (static)",
                "entries": stale_entries,
                "evidence_id": "c83-static-baseline-nine-entries",
            },
            "intentional_instrumentation_drifts": {
                "count": 3,
                "status": "C83_SOURCE_WITH_C79_SHARED_BASELINE_RECONCILIATION",
                "owner": "C83/work-task-125; C79/work-task-114 for shared baseline",
                "job_id": "103606090554",
                "check": "CI (static)",
                "entries": instrumentation_drifts,
                "paths": [entry["path"] for entry in instrumentation_drifts],
                "evidence_id": "c83-static-intentional-instrumentation-three",
            },
        },
        "failures": [
            {
                "id": "c83-static-wire-registration",
                "job": "CI (static)",
                "job_id": "103606090554",
                "kind": "shared-registry",
                "owner": "C79/work-task-114",
                "path": "scripts/wire-packages.txt",
                "signature": "wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/browserrunner/wire/wire_gen.go'], outside modules=[]",
            },
            {
                "id": "c83-static-baseline-nine-entries",
                "job": "CI (static)",
                "job_id": "103606090554",
                "kind": "shared-baseline",
                "owner": "C79/work-task-114",
                "path": "docs/architecture/architecture-size-baseline.json",
                "count": 9,
                "signatures": [entry["signature"] for entry in stale_entries],
            },
            {
                "id": "c83-static-instrumentation-three",
                "job": "CI (static)",
                "job_id": "103606090554",
                "kind": "candidate-instrumentation-baseline-drift",
                "owner": "C83/work-task-125 with C79/work-task-114 baseline serialization",
                "count": 3,
                "signatures": [entry["signature"] for entry in instrumentation_drifts],
            },
            {
                "id": "c83-integration-provider-audio-loss-6400",
                "job": "CI (integration)",
                "job_id": "103606090561",
                "kind": "provider-audio-terminal-drain",
                "owner": "C79/provider-audio",
                "path": "agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:219",
                "signature": "test46_high_rate process-boundary audio verification: remote device rendered 167991/174391 compared samples (lost 6400, 96.3% retained)",
                "do_not_duplicate": True,
            },
        ],
        "integration_loss": {
            "lost_samples": 6400,
            "total_samples": 174391,
            "rendered_samples": 167991,
            "retained_ratio": 0.963,
            "drops": 0,
            "overflows": 0,
            "discards": 0,
            "discard_events": 0,
            "underflow_events": 10,
            "underflow_samples": 4800,
            "zero_filled_samples": 4800,
            "provider_responses_sent": 9,
            "provider_response_creates": 7,
            "provider_tool_results": 7,
            "owner": "C79/provider-audio",
            "status": "EXTERNAL_OWNER_DO_NOT_DUPLICATE_REPAIR_OR_RELABEL",
            "evidence_id": "c83-integration-provider-audio-loss-6400",
        },
        "acceptance": {
            "candidate_head_green": False,
            "reviewed": False,
            "merged": False,
            "vertical_probe": False,
            "project_acceptance": False,
        },
    }


def bounded_sequence(main: str, c61: str, c83: str) -> dict[str, Any]:
    return {
        "schema": "audio-runtime-c109-bounded-sequence-v2",
        "status": "bounded-and-procedural",
        "max_total_timeout_seconds": 1200,
        "inputs": {
            "accepted_main": main,
            "c61": {"task": "work-task-34", "pr": 452, "commit": c61},
            "c83": {"task": "work-task-125", "pr": 473, "commit": c83},
            "shared_owner": "work-task-114",
        },
        "steps": [
            {
                "number": 1,
                "id": "release-shared-registries",
                "owner": "C79/work-task-114",
                "inputs": ["scripts/wire-packages.txt", "docs/architecture/architecture-size-baseline.json", "c83-integration-provider-audio-loss-6400"],
                "preconditions": ["C79 lease is active", "the exact C83 static/integration findings are reproduced"],
                "commands": ["reconcile only the demonstrated C83 Wire/baseline findings", "diagnose the owned provider-audio terminal-drain loss"],
                "expected_observables": ["shared registry and baseline changes are limited to demonstrated findings", "provider-audio attribution remains C79-owned"],
                "timeout_seconds": 300,
                "stop_conditions": ["any candidate source or C109 evidence path is requested", "loss attribution cannot be demonstrated"],
                "rollback": "retain the C79 checkpoint and return the same task with exact failed evidence",
                "checkpoint": "C79 review and guarded merge release the shared paths",
                "gate": "C79 task/review/CI; C109 does not edit or relabel these findings",
            },
            {
                "number": 2,
                "id": "reconcile-c61",
                "owner": "C61/work-task-34",
                "inputs": [f"PR #452 head {c61}", f"accepted main {main}", "c61-historical-delta-barrier-reconciliation"],
                "preconditions": ["C79 shared lease is released or the C61 changes do not touch its paths", "the same work-task-34 is explicitly returned"],
                "commands": ["integrate accepted origin/main", "reconcile OPEN_RECONCILE_TEST_REWRITTEN", "run the named focused normal/race and script-CI gates"],
                "expected_observables": ["the historical test remains unchanged or its repair is proved by C61", "C61 branch and PR identity are retained"],
                "timeout_seconds": 300,
                "stop_conditions": ["the focused test is rewritten without evidence", "a C109 or C83 path is mutated"],
                "rollback": "preserve the existing C61 checkpoint and return the exact failed check to work-task-34",
                "checkpoint": "push the same C61 PR and submit its exact HEAD to script CI",
                "gate": "independent C61 review and script CI; C109 makes no C61 acceptance claim",
            },
            {
                "number": 3,
                "id": "reconcile-c83",
                "owner": "C83/work-task-125",
                "inputs": [f"PR #473 head {c83}", f"accepted main {main}", "c83-ci-34713381619", "c83-integration-provider-audio-loss-6400"],
                "preconditions": ["C79 shared findings are released", "C61 task disposition is recorded", "C83 preserves its tracker/interruption checkpoint"],
                "commands": ["integrate accepted origin/main", "apply only demonstrated C83 Wire/baseline changes", "run focused/race/external-consumer/browser regressions and script CI"],
                "expected_observables": ["C83 owns its instrumentation reconciliation", "the 6,400-sample loss remains with C79/provider-audio", "no C61 API dependency is invented"],
                "timeout_seconds": 420,
                "stop_conditions": ["a shared registry is edited without C79 release", "provider-audio loss is relabeled or duplicated", "browser cancellation ordering regresses"],
                "rollback": "preserve the C83 checkpoint and return the exact failed check to work-task-125",
                "checkpoint": "push the same C83 PR and submit its changed HEAD to script CI",
                "gate": "independent C83 review, guarded merge and later immutable vertical probe; C109 does not accept C83",
            },
            {
                "number": 4,
                "id": "c109-handoff",
                "owner": "C109/work-task-224",
                "inputs": ["this evidence directory", f"required merge order main -> {c61} -> {c83}"],
                "preconditions": ["analyzer and verifier pass from clean temporary roots", "both candidate refs/worktrees remain unchanged"],
                "commands": ["commit only the C109 evidence directory", "push the same admitted branch", "submit exact HEAD to script CI without polling"],
                "expected_observables": ["current-head CI is delegated to the script gate", "fresh review and guarded merge remain external", "C61/C83/project acceptance claims remain false"],
                "timeout_seconds": 180,
                "stop_conditions": ["CI rejects an exact C109 check", "review identifies an actionable evidence defect"],
                "rollback": "retain the predecessor checkpoint and repair the same task; never reset the host checkout",
                "checkpoint": "fresh independent engineering scope=vertical probe after guarded merge",
                "gate": "C109 delivery handoff only; all nine project gates remain OPEN",
            },
        ],
        "prohibited": [
            "merge either candidate from C109",
            "repair candidate source or shared C79 files",
            "claim C61/C83/project acceptance",
            "claim hardware, microphone, acoustic, or vertical-probe coverage",
            "relabel the 6400-sample provider-audio loss",
        ],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--main", default=ACCEPTED_MAIN)
    parser.add_argument("--c61", default=C61)
    parser.add_argument("--c83", default=C83)
    parser.add_argument("--order", choices=("c61-c83", "c83-c61"), required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--expect-control", action="store_true")
    args = parser.parse_args()
    root = Path(git_output(Path.cwd(), ["rev-parse", "--show-toplevel"])).resolve()
    output_dir = args.output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    require(args.main == ACCEPTED_MAIN, f"accepted main input must be {ACCEPTED_MAIN}, got {args.main}")
    require(args.c61 == C61 and args.c83 == C83, "C61/C83 inputs must match the admitted preserved revisions")
    require(not args.expect_control or args.order == "c83-c61", "--expect-control is valid only for the reverse rehearsal")
    require(git_output(root, ["branch", "--show-current"]) == BRANCH, "analyzer must run from the admitted C109 branch")

    source_head = revision(root, "HEAD")
    source_tree = tree(root, "HEAD")
    source_files = ("analyze.py", "verify.py", "run_public_checks.py")
    source_bindings: list[dict[str, Any]] = []
    for filename in source_files:
        relative = OWNED_PREFIX + filename
        path = root / relative
        head_blob = maybe_blob(root, "HEAD", relative)
        require(path.is_file() and head_blob is not None, f"C109 source script is unavailable: {relative}")
        working_blob = git_output(root, ["hash-object", str(path)])
        working_sha256 = sha256_file(path)
        require(working_blob == head_blob, f"C109 source script is dirty relative to committed HEAD: {relative}")
        source_bindings.append(
            {
                "path": relative,
                "head_blob": head_blob,
                "working_blob": working_blob,
                "working_tree_sha256": working_sha256,
                "matches_committed_head": True,
            }
        )
    before_refs = ref_snapshot(root)
    before_host_status = status_lines(root)
    require(all(OWNED_PREFIX in line for line in before_host_status), f"unexpected dirty path before analyzer: {before_host_status}")
    source_status_before = [
        line
        for line in before_host_status
        if any(f"{OWNED_PREFIX}{filename}" in line for filename in source_files)
    ]
    require(not source_status_before, f"C109 source scripts were dirty before analyzer: {source_status_before}")
    before_preserved = preserved_snapshot(root)
    fetch_result = run(["git", "fetch", "origin", "main"], cwd=root, check=False, timeout=120)
    require(fetch_result["status"] == "passed" and fetch_result["exit_code"] == 0, "origin/main fetch failed")
    fetched_main = revision(root, "origin/main")
    requested_main = revision(root, args.main)
    require(requested_main == ACCEPTED_MAIN, "the admitted accepted-main revision cannot be resolved to its exact SHA")
    require(git(root, ["merge-base", "--is-ancestor", requested_main, fetched_main], check=False)["exit_code"] == 0, "origin/main is not descended from the admitted accepted main")
    review_main = fetched_main
    newer_main = review_main != requested_main
    require(git(root, ["merge-base", "--is-ancestor", review_main, source_head], check=False)["exit_code"] == 0, "review-time origin/main is not integrated into the committed C109 HEAD")
    require(revision(root, args.c61) == C61 and revision(root, args.c83) == C83, "candidate refs changed from the admitted exact SHAs")

    c61_paths = diff_paths(root, review_main, args.c61)
    c83_paths = diff_paths(root, review_main, args.c83)
    union_paths = sorted(set(c61_paths) | set(c83_paths))
    shared_paths = sorted(set(c61_paths) & set(c83_paths))
    require(shared_paths == [SHARED_RUNNER], f"unexpected candidate path intersection: {shared_paths}")
    required = args.order == "c61-c83"
    order = [("c61", args.c61), ("c83", args.c83)] if required else [("c83", args.c83), ("c61", args.c61)]
    rehearsal = commit_rehearsal(root, review_main, order, "required" if required else "control")

    after_refs = ref_snapshot(root)
    after_host_status = status_lines(root)
    after_preserved = preserved_snapshot(root)
    preserved_unchanged = before_preserved == after_preserved
    refs_unchanged = candidate_ref_values(before_refs) == candidate_ref_values(after_refs)
    changed_status = sorted(set(before_host_status) ^ set(after_host_status))
    host_scope_clean = all(OWNED_PREFIX in line for line in changed_status)
    require(preserved_unchanged, "preserved predecessor worktree changed during characterization")
    require(refs_unchanged, "candidate branch/ref identity changed during characterization")
    require(host_scope_clean, f"unexpected host checkout mutation: {changed_status}")
    require(revision(root, "HEAD") == source_head and tree(root, "HEAD") == source_tree, "analyzer changed the committed C109 HEAD")

    files = [file_record(root, review_main, args.c61, args.c83, path, c61_paths, c83_paths, shared_paths) for path in union_paths]
    api = api_relationship(root, args.c61, args.c83, c61_paths, c83_paths)
    ci = ci_attribution()
    ledger = {
        "schema": "audio-runtime-c109-ledger-v2",
        "base": {
            "reviewMain": review_main,
            "candidateIntersection": shared_paths,
            "candidateUnionPaths": union_paths,
            "candidateUnionCount": len(union_paths),
            "source_driven": True,
        },
        "files": files,
        "apiConclusion": api,
        "unownedCandidatePaths": [],
        "failClosedRules": [
            "exact admitted SHAs and successful current-main fetch are required",
            "both merge orders and complete conflict evidence are required",
            "candidate refs and attached predecessor worktrees must remain unchanged",
            "every changed hunk has source span, declaration/caller edges, blob hashes and owner",
            "C109 owned paths are evidence-only",
            "unsupported acceptance/fix/probe claims are rejected",
        ],
    }
    provenance = {
        "schema": "audio-runtime-c109-provenance-v2",
        "project": PROJECT,
        "task": TASK,
        "contractRevision": CONTRACT,
        "branch": BRANCH,
        "factorySession": "~default",
        "factoryServer": os.environ.get("FACTORY_SERVER_URL", "$FACTORY_SERVER_URL"),
        "startupIntegrationRevision": STARTUP_INTEGRATION_REVISION,
        "requestedAcceptedMain": requested_main,
        "fetchedOriginMain": fetched_main,
        "reviewMain": review_main,
        "newerMainFetchedAndIntegrated": newer_main,
        "candidates": {
            "c61": candidate_ref_snapshot(root, args.c61, "C61/work-task-34", "c61"),
            "c83": candidate_ref_snapshot(root, args.c83, "C83/work-task-125", "c83"),
        },
        "candidateTrees": {
            "c61": tree(root, args.c61),
            "c83": tree(root, args.c83),
            "reviewMain": tree(root, review_main),
        },
        "fetch": {
            "command": fetch_result["command"],
            "status": fetch_result["status"],
            "exit_code": fetch_result["exit_code"],
            "resolved_origin_main": fetched_main,
            "output_retained": False,
            "reason": "fetch transport output is mutable; commit/ref identity is retained instead",
        },
        "sourceBinding": {
            "committedHead": source_head,
            "committedTree": source_tree,
            "scripts": source_bindings,
            "statusBefore": source_status_before,
            "statusAfter": after_host_status,
            "cleanScripts": True,
        },
        "refs": {"complete": True, "before": before_refs, "after": after_refs, "candidateRefsUnchanged": refs_unchanged},
        "worktreeInventory": scoped_worktree_inventory(root),
        "preservedWorktrees": {
            "before": before_preserved,
            "after": after_preserved,
            "unchanged": preserved_unchanged,
        },
        "hostCheckout": {
            "beforeStatus": before_host_status,
            "afterStatus": after_host_status,
            "changedStatusLines": changed_status,
            "scopeClean": host_scope_clean,
            "branch": git_output(root, ["branch", "--show-current"]),
            "head": revision(root, "HEAD"),
        },
        "refsUnchanged": refs_unchanged,
    }
    sequence = bounded_sequence(review_main, args.c61, args.c83)
    report = {
        "schema": "audio-runtime-c109-v2",
        "project": PROJECT,
        "task": TASK,
        "contractRevision": CONTRACT,
        "role": "required" if required else "control",
        "provenanceFile": "provenance.json",
        "ledgerFile": "ledger.json",
        "mergeOrderFile": "merge-orders.json",
        "sequenceFile": "sequence.json",
        "ciAttributionFile": "ci-attribution.json",
        "provenance": provenance,
        "ledger": ledger,
        "rehearsal": rehearsal,
        "historicalC61Finding": historical_c61_finding(root, review_main, args.c61),
        "ciAttribution": ci,
        "c83CIAttribution": ci,
        "sequence": sequence,
        "deliveryClaims": {
            "C61": {"merged": False, "fixed": False, "probed": False, "accepted": False},
            "C83": {"merged": False, "fixed": False, "probed": False, "accepted": False},
            "project": {"accepted": False, "verticalProbe": False},
        },
        "broadGates": {gate: "OPEN" for gate in MANIFEST_GATES},
        "claims": {
            "mechanicalMergeCompatibility": bool(rehearsal["merges"] and len(rehearsal["merges"]) == 2 and all(item["exit_code"] == 0 for item in rehearsal["merges"])),
            "candidateDeliveryOrder": "not-authorized",
            "softwareEvidenceOnly": True,
            "hardwareOrAcousticEvidence": False,
            "verticalProbe": False,
        },
    }
    require(report["claims"]["mechanicalMergeCompatibility"], "required merge rehearsal did not complete cleanly")
    require(rehearsal["final_status"] == [] and rehearsal["cleanup"]["worktree_removed"] and rehearsal["cleanup"]["temporary_root_removed"], "disposable rehearsal cleanup is incomplete")

    write_json(output_dir / "provenance.json", provenance)
    write_json(output_dir / "ledger.json", ledger)
    write_json(output_dir / "merge-orders.json", rehearsal)
    write_json(output_dir / "sequence.json", sequence)
    write_json(output_dir / "ci-attribution.json", ci)
    write_json(output_dir / "report.json", report)
    write_json(
        output_dir / "run-manifest.json",
        {
            "schema": "audio-runtime-c109-run-manifest-v2",
            "commands": {
                "required": "analyze.py --order c61-c83",
                "control": "analyze.py --order c83-c61 --expect-control",
                "verification": "verify.py --mode all",
            },
            "source": {"main": review_main, "c61": args.c61, "c83": args.c83},
            "outputSha256": {name: sha256_file(output_dir / name) for name in ("ci-attribution.json", "ledger.json", "merge-orders.json", "provenance.json", "report.json", "sequence.json")},
        },
    )
    print(
        json.dumps(
            {
                "status": "passed",
                "role": report["role"],
                "reviewMain": review_main,
                "order": report["rehearsal"]["order"],
                "outputDir": str(output_dir),
                "candidateRefsUnchanged": refs_unchanged,
                "preservedWorktreesUnchanged": preserved_unchanged,
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceError as exc:
        print(f"C109 analyzer failed closed: {exc}", file=sys.stderr)
        raise SystemExit(2)
