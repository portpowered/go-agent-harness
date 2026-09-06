#!/usr/bin/env python3
"""Prepare one admitted task without switching or resetting the host checkout."""
import json
from pathlib import Path
import shutil
import subprocess
import sys

from project_contract import ContractError, manifest, root_path, task_packet, work_name


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def prepare(root: Path, name: str) -> dict:
    contract = manifest(root)
    packet = task_packet(root, name, contract)
    branch = packet["branchName"]
    directory = root / ".claude/worktrees" / work_name(name)
    if directory.exists():
        if not (directory / ".git").is_file():
            raise ContractError("existing task path is not a managed worktree; nothing removed")
        if git(directory, "branch", "--show-current") != branch:
            raise ContractError("existing worktree has a different branch; nothing changed")
        existing = directory / "prd.json"
        if existing.exists() and json.loads(existing.read_text()) != packet:
            raise ContractError("existing PRD differs; preserve it and issue a new task")
        reused = True
    else:
        git(root, "fetch", "--no-tags", "origin", "main")
        directory.parent.mkdir(parents=True, exist_ok=True)
        found = subprocess.run(["git", "-C", str(root), "show-ref", "--verify", "--quiet",
                                "refs/heads/" + branch], check=False)
        if found.returncode == 0:
            git(root, "worktree", "add", str(directory), branch)
        elif found.returncode == 1:
            git(root, "worktree", "add", "-b", branch, str(directory), "origin/main")
        else:
            raise ContractError("could not inspect branch")
        reused = False
    source = root / "tasks/todo" / name
    shutil.copy2(source.with_suffix(".json"), directory / "prd.json")
    if source.with_suffix(".md").is_file():
        shutil.copy2(source.with_suffix(".md"), directory / "prd.md")
    progress = directory / "progress.txt"
    if not progress.exists():
        progress.write_text("# Task progress\n\nProject: " + contract["project"] + "\n")
    return {"status": "ready", "worktree": str(directory), "branch": branch,
            "baseRevision": git(directory, "rev-parse", "HEAD"), "reused": reused}


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ContractError("usage: prepare-workspace.py <work-name>")
        print(json.dumps(prepare(root_path(), sys.argv[1])))
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
