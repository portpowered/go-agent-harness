#!/usr/bin/env python3
"""setup-workspace.py — Create or reuse a git worktree for a PRD.

Usage: python scripts/agents/setup-workspace.py <prd-name>

Reads tasks/todo/<prd-name>.json, uses <prd-name> as the branch/worktree name,
creates or reuses a git worktree, copies the PRD (and optional .md) into the
worktree root, and prints a JSON result to stdout.

Exit 0 on success (stdout = JSON blob), exit 1 on failure (stderr = error).
"""

import json
import shutil
import subprocess
import sys
import time
from pathlib import Path

PLANNER_OWNED_DIRTY_PATHS = {
    "docs/internal/checklist.md",
    "docs/internal/progress.txt",
}


def run_git(*args, cwd=None, check=True):
    """Run a git command, returning stdout. Raises on failure if check=True."""
    result = subprocess.run(
        ["git"] + list(args),
        cwd=cwd,
        capture_output=True,
        text=True,
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"git {' '.join(args)} failed (exit {result.returncode}): {result.stderr.strip()}"
        )
    return result


def get_repo_root():
    """Discover the repository root via git."""
    result = run_git("rev-parse", "--show-toplevel")
    return Path(result.stdout.strip())


def read_prd(prd_path):
    """Read and parse a PRD JSON file. Returns the parsed dict."""
    with open(prd_path, "r", encoding="utf-8") as f:
        return json.load(f)


def normalize_branch(branch_name):
    """Convert branch name to a filesystem-safe directory name."""
    return branch_name.replace("/", "-")


def worktree_is_valid(worktree_path):
    """Check if an existing worktree path is valid and has content."""
    git_file = worktree_path / ".git"
    if not git_file.exists():
        return False
    # Check for non-.git content.
    entries = [e for e in worktree_path.iterdir() if e.name != ".git"]
    return len(entries) > 0


def list_registered_worktrees(repo_root):
    """Return registered worktrees keyed by path."""
    result = run_git("worktree", "list", "--porcelain", cwd=repo_root)
    worktrees = {}
    current = None
    for raw_line in result.stdout.splitlines():
        line = raw_line.strip()
        if not line:
            current = None
            continue
        key, _, value = line.partition(" ")
        if key == "worktree":
            current = Path(value).resolve()
            worktrees[current] = {}
            continue
        if current is None:
            continue
        worktrees[current][key] = value
    return worktrees


def registered_branch_for_path(repo_root, worktree_path):
    """Return the branch name registered for a worktree path, if any."""
    worktrees = list_registered_worktrees(repo_root)
    branch_ref = worktrees.get(worktree_path.resolve(), {}).get("branch")
    if not branch_ref or not branch_ref.startswith("refs/heads/"):
        return None
    return branch_ref.removeprefix("refs/heads/")


def list_root_status_entries(repo_root):
    """Return parsed git status entries for the repository root."""
    result = run_git(
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
        "-z",
        cwd=repo_root,
    )
    raw_entries = result.stdout.split("\0")
    entries = []
    index = 0
    while index < len(raw_entries):
        entry = raw_entries[index]
        index += 1
        if not entry:
            continue
        status = entry[:2]
        path = entry[3:]
        original_path = None
        if "R" in status or "C" in status:
            if index >= len(raw_entries):
                raise RuntimeError("git status returned an incomplete rename entry")
            original_path = raw_entries[index]
            index += 1
        entries.append(
            {
                "status": status,
                "path": Path(path).as_posix(),
                "original_path": Path(original_path).as_posix() if original_path else None,
            }
        )
    return entries


def planner_owned_status_is_tolerated(status):
    """Return whether a planner-owned dirty entry is safe to ignore during setup."""
    if status == "??":
        return True
    return all(flag in {" ", "M", "A"} for flag in status)


def allowed_dirty_paths_for_setup(prd_name):
    """Return dirty paths that are safe for setup to ignore."""
    return PLANNER_OWNED_DIRTY_PATHS | {
        f"tasks/todo/{prd_name}.json",
        f"tasks/todo/{prd_name}.md",
    }


def validate_root_dirty_state(repo_root, prd_name):
    """Allow setup inputs and planner-owned dirty files while rejecting other dirty root state."""
    allowed_dirty_paths = allowed_dirty_paths_for_setup(prd_name)
    unsafe_entries = []
    for entry in list_root_status_entries(repo_root):
        path = entry["path"]
        if path in allowed_dirty_paths and planner_owned_status_is_tolerated(entry["status"]):
            continue
        unsafe_entries.append(entry)

    if not unsafe_entries:
        return

    rendered_entries = []
    for entry in unsafe_entries:
        rendered = f"{entry['status']} {entry['path']}"
        if entry["original_path"]:
            rendered = f"{rendered} <- {entry['original_path']}"
        rendered_entries.append(rendered)
    joined_entries = ", ".join(rendered_entries)
    allowed_paths = ", ".join(sorted(allowed_dirty_paths))
    raise RuntimeError(
        "root checkout has unsupported dirty state outside planner-owned files "
        "and requested setup artifacts; "
        f"allowed dirty paths are {allowed_paths}; found {joined_entries}"
    )


def wait_for_reusable_worktree(repo_root, branch, worktree_path, timeout_seconds=5):
    """Wait briefly for a concurrently-created worktree to become reusable."""
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        if worktree_path.exists() and worktree_is_valid(worktree_path):
            registered_branch = registered_branch_for_path(repo_root, worktree_path)
            if registered_branch == branch:
                return True
            if registered_branch and registered_branch != branch:
                raise RuntimeError(
                    f"worktree path {worktree_path} is already registered to branch "
                    f"{registered_branch}, expected {branch}"
                )
        time.sleep(0.1)
    return False


def branch_exists_locally(repo_root, branch):
    """Check if a branch exists as a local ref."""
    result = run_git(
        "rev-parse", "--verify", f"refs/heads/{branch}",
        cwd=repo_root, check=False,
    )
    return result.returncode == 0


def branch_exists_on_remote(repo_root, branch):
    """Check if a branch exists on origin."""
    result = run_git(
        "rev-parse", "--verify", f"refs/remotes/origin/{branch}",
        cwd=repo_root, check=False,
    )
    return result.returncode == 0


def create_or_reuse_worktree(repo_root, branch, worktree_path):
    """Create a new worktree or reuse an existing one. Returns reused flag."""
    if wait_for_reusable_worktree(repo_root, branch, worktree_path, timeout_seconds=0.2):
        return True

    if worktree_path.exists():
        registered_branch = registered_branch_for_path(repo_root, worktree_path)
        if registered_branch:
            raise RuntimeError(
                f"worktree path {worktree_path} is registered to branch "
                f"{registered_branch} but is not reusable"
            )

    # Remove stale path if it exists but is invalid and unregistered.
    if worktree_path.exists():
        shutil.rmtree(worktree_path)

    # Create new worktree.
    worktree_path.parent.mkdir(parents=True, exist_ok=True)

    try:
        if branch_exists_locally(repo_root, branch):
            run_git(
                "worktree", "add", str(worktree_path), branch,
                cwd=repo_root,
            )
        elif branch_exists_on_remote(repo_root, branch):
            run_git(
                "worktree", "add", "--track", "-b", branch,
                str(worktree_path), f"origin/{branch}",
                cwd=repo_root,
            )
        else:
            run_git(
                "worktree", "add", "-b", branch, str(worktree_path), "main",
                cwd=repo_root,
            )
    except RuntimeError:
        if wait_for_reusable_worktree(repo_root, branch, worktree_path):
            return True
        raise

    return False


def copy_prd_files(prd_json_path, prd_md_path, worktree_path):
    """Copy PRD files into the worktree root."""
    dest_json = worktree_path / "prd.json"
    shutil.copy2(str(prd_json_path), str(dest_json))

    dest_md = None
    if prd_md_path and prd_md_path.exists():
        dest_md = worktree_path / "prd.md"
        shutil.copy2(str(prd_md_path), str(dest_md))

    return dest_json, dest_md


def main():
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <prd-name>", file=sys.stderr)
        sys.exit(1)

    prd_name = sys.argv[1]

    try:
        repo_root = get_repo_root()
    except RuntimeError as e:
        print(f"Failed to discover repo root: {e}", file=sys.stderr)
        sys.exit(1)

    # Locate PRD files.
    prd_json_path = repo_root / "tasks" / "todo" / f"{prd_name}.json"
    if not prd_json_path.exists():
        print(f"PRD not found: {prd_json_path}", file=sys.stderr)
        sys.exit(1)

    prd_md_path = repo_root / "tasks" / "todo" / f"{prd_name}.md"
    if not prd_md_path.exists():
        prd_md_path = None

    # Read the PRD to catch malformed input; the branch name is the work item name.
    try:
        read_prd(prd_json_path)
    except (json.JSONDecodeError, OSError) as e:
        print(f"Failed to read PRD: {e}", file=sys.stderr)
        sys.exit(1)

    try:
        validate_root_dirty_state(repo_root, prd_name)
    except RuntimeError as e:
        print(f"Worktree setup failed: {e}", file=sys.stderr)
        sys.exit(1)

    branch = f"{prd_name}"
    if not branch:
        print("PRD name must not be empty", file=sys.stderr)
        sys.exit(1)

    # Create or reuse worktree.
    worktree_dir = repo_root / ".claude" / "worktrees" / normalize_branch(branch)
    try:
        reused = create_or_reuse_worktree(repo_root, branch, worktree_dir)
    except RuntimeError as e:
        print(f"Worktree setup failed: {e}", file=sys.stderr)
        sys.exit(1)

    # Copy PRD files into worktree.
    try:
        dest_json, dest_md = copy_prd_files(prd_json_path, prd_md_path, worktree_dir)
    except OSError as e:
        print(f"Failed to copy PRD files: {e}", file=sys.stderr)
        sys.exit(1)

    # Output result.
    result = {
        "status": "ready",
        "worktree": str(worktree_dir),
        "branch": branch,
        "prd_path": str(dest_json),
        "prd_md_path": str(dest_md) if dest_md else None,
        "reused": reused,
    }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
