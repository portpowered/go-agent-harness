#!/usr/bin/env python3
"""Merge one independently reviewed pull request under a final-head guard.

The command is deliberately small and fail-closed.  It performs the live
verification while holding the shared repository lock, passes GitHub the exact
reviewed head, and verifies the resulting integrated revision before it can
report success.
"""

import argparse
import fcntl
import json
import os
import re
import subprocess
import sys
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
REQUIRED_CHECKS_PATH = SCRIPT_DIR.parent / "docs" / "required-checks.json"
SHA_PATTERN = re.compile(r"^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$")
COMMAND_TIMEOUT_SECONDS = 45
LOCK_BUSY_EXIT = 75
CHECK_FIELDS = "name,state,bucket,link,workflow,startedAt,completedAt"
VIEW_FIELDS = (
    "number,state,isDraft,headRefOid,baseRefName,mergeable,"
    "mergeStateStatus,statusCheckRollup"
)
MERGED_FIELDS = "number,state,mergeCommit,headRefOid"


class GuardError(RuntimeError):
    """A live merge precondition or verification failed."""


class LockBusy(GuardError):
    """Another final merge is holding the shared lock."""


def _valid_sha(value):
    return isinstance(value, str) and bool(SHA_PATTERN.fullmatch(value.strip()))


def _command(args, *, cwd=None, check=True):
    try:
        result = subprocess.run(
            list(args),
            cwd=str(cwd) if cwd is not None else None,
            capture_output=True,
            text=True,
            timeout=COMMAND_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise GuardError(f"command unavailable: {args[0]}") from error
    if check and result.returncode != 0:
        raise GuardError(f"command failed: {args[0]}")
    return result


def _json_command(
    args,
    *,
    cwd=None,
    allow_empty=False,
    allow_return_codes=(),
    allow_empty_error_prefix=None,
):
    result = _command(args, cwd=cwd, check=False)
    stdout = (result.stdout or "").strip()
    if not stdout:
        if allow_empty and result.returncode == 0:
            return []
        stderr = (result.stderr or "").strip().lower()
        if (
            allow_empty
            and result.returncode == 1
            and allow_empty_error_prefix
            and stderr.startswith(allow_empty_error_prefix.lower())
        ):
            return []
        raise GuardError(f"command returned no JSON: {args[0]}")
    try:
        value = json.loads(stdout)
    except (TypeError, json.JSONDecodeError) as error:
        raise GuardError(f"command returned malformed JSON: {args[0]}") from error
    if result.returncode != 0 and result.returncode not in allow_return_codes:
        raise GuardError(f"command failed: {args[0]}")
    return value


def _git(repo_path, *args, check=True):
    return _command(["git", "-C", str(repo_path), *args], check=check)


def _git_output(repo_path, *args):
    result = _git(repo_path, *args)
    value = (result.stdout or "").strip()
    if not value:
        raise GuardError(f"git returned no value: {args[0]}")
    return value


def _load_required_checks(path=REQUIRED_CHECKS_PATH):
    try:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise GuardError("required-check policy is unavailable") from error
    names = document.get("requiredChecks") if isinstance(document, dict) else None
    if not isinstance(names, list) or not names:
        raise GuardError("required-check policy is empty or malformed")
    normalized = []
    seen = set()
    for name in names:
        if not isinstance(name, str) or not name.strip() or name.strip() in seen:
            raise GuardError("required-check policy contains an invalid or duplicate name")
        name = name.strip()
        normalized.append(name)
        seen.add(name)
    return tuple(normalized)


def _gh_view(repo, pr, fields=VIEW_FIELDS):
    value = _json_command(
        ["gh", "pr", "view", str(pr), "--repo", repo, "--json", fields]
    )
    if not isinstance(value, dict):
        raise GuardError("GitHub PR view was not an object")
    return value


def _gh_checks(repo, pr, *, required=False):
    args = ["gh", "pr", "checks", str(pr), "--repo", repo]
    if required:
        args.append("--required")
    args.extend(["--json", CHECK_FIELDS])
    value = _json_command(
        args,
        allow_empty=required,
        allow_return_codes=(8,),
        allow_empty_error_prefix="no required checks reported",
    )
    if not isinstance(value, list) or any(not isinstance(row, dict) for row in value):
        raise GuardError("GitHub PR checks were malformed")
    return value


def _require_open_reviewed_view(view, pr, head):
    if view.get("number") != pr or view.get("state") != "OPEN":
        raise GuardError("pull request is not open")
    if view.get("isDraft") is not False:
        raise GuardError("pull request is draft or draft state is unavailable")
    if view.get("baseRefName") != "main":
        raise GuardError("pull request does not target main")
    remote_head = view.get("headRefOid")
    if not _valid_sha(remote_head) or remote_head.lower() != head.lower():
        raise GuardError("pull request head does not match reviewed head")
    if view.get("mergeable") != "MERGEABLE":
        raise GuardError("pull request mergeability is not clean")
    if view.get("mergeStateStatus") != "CLEAN":
        raise GuardError("pull request merge state is not clean")
    if not isinstance(view.get("statusCheckRollup"), list):
        raise GuardError("pull request status check rollup is unavailable")


def _check_state(row):
    state = row.get("state")
    bucket = row.get("bucket")
    if isinstance(state, str) and state.upper() == "SUCCESS":
        return "pass" if bucket is None else str(bucket).lower()
    return "unknown"


def _check_name(row):
    name = row.get("name") or row.get("context")
    return name.strip() if isinstance(name, str) else ""


def _require_green_checks(all_checks, github_required, policy_names):
    required_names = list(policy_names)
    for row in github_required:
        name = _check_name(row)
        if not name:
            raise GuardError("GitHub required check has no name")
        if name not in required_names:
            required_names.append(name)
    if not required_names:
        raise GuardError("no required checks were observed")

    for required_name in required_names:
        matches = [row for row in all_checks if _check_name(row) == required_name]
        if not matches:
            raise GuardError(f"required check is missing: {required_name}")
        if any(_check_state(row) != "pass" for row in matches):
            raise GuardError(f"required check is not successful: {required_name}")


def _fresh_main(repo_path):
    _git(repo_path, "fetch", "origin", "main:refs/remotes/origin/main")
    main = _git_output(repo_path, "rev-parse", "origin/main")
    if not _valid_sha(main):
        raise GuardError("fresh origin/main is not a complete commit")
    return main.lower()


def _is_ancestor(repo_path, ancestor, descendant):
    result = _git(
        repo_path,
        "merge-base",
        "--is-ancestor",
        ancestor,
        descendant,
        check=False,
    )
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    raise GuardError("git could not verify ancestry")


def _common_git_dir(repo_path):
    value = _git_output(repo_path, "rev-parse", "--git-common-dir")
    path = Path(value)
    if not path.is_absolute():
        path = repo_path / path
    return path.resolve()


class FinalMergeLock:
    """Non-blocking lock shared by all worktrees of this repository."""

    def __init__(self, path):
        self.path = Path(path)
        self.handle = None

    def __enter__(self):
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.handle = self.path.open("a+")
        try:
            fcntl.flock(self.handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            self.handle.close()
            self.handle = None
            raise LockBusy("final merge lock is held") from error
        return self

    def __exit__(self, exc_type, exc, traceback):
        if self.handle is not None:
            fcntl.flock(self.handle.fileno(), fcntl.LOCK_UN)
            self.handle.close()
            self.handle = None


def _merge_method_flag(method):
    flags = {"merge": "--merge", "squash": "--squash", "rebase": "--rebase"}
    try:
        return flags[method]
    except KeyError as error:
        raise GuardError("unsupported merge method") from error


def guarded_merge(repo, pr, head, method="merge", *, repo_path=None, policy_path=None):
    """Perform the guarded merge and return the verified merged revision."""
    if not isinstance(repo, str) or not repo.strip():
        raise GuardError("repository is required")
    if not isinstance(pr, int) or isinstance(pr, bool) or pr <= 0:
        raise GuardError("pull request number is invalid")
    if not _valid_sha(head):
        raise GuardError("reviewed head is not a complete commit")
    if method not in {"merge", "squash", "rebase"}:
        raise GuardError("unsupported merge method")

    worktree = Path(repo_path or os.getcwd()).resolve()
    reviewed_head = head.lower()
    policy = _load_required_checks(policy_path or REQUIRED_CHECKS_PATH)
    lock_path = _common_git_dir(worktree) / "factory-final-merge.lock"

    with FinalMergeLock(lock_path):
        main = _fresh_main(worktree)
        if not _is_ancestor(worktree, main, reviewed_head):
            raise GuardError("reviewed head does not contain fresh origin/main")

        before = _gh_view(repo, pr)
        _require_open_reviewed_view(before, pr, reviewed_head)
        all_checks = _gh_checks(repo, pr)
        github_required = _gh_checks(repo, pr, required=True)
        _require_green_checks(all_checks, github_required, policy)

        after = _gh_view(repo, pr)
        _require_open_reviewed_view(after, pr, reviewed_head)
        if after.get("headRefOid", "").lower() != before.get("headRefOid", "").lower():
            raise GuardError("pull request head changed during final verification")

        merge_result = _command(
            [
                "gh",
                "pr",
                "merge",
                str(pr),
                "--repo",
                repo,
                _merge_method_flag(method),
                "--match-head-commit",
                reviewed_head,
            ],
            check=False,
        )
        if merge_result.returncode != 0:
            raise GuardError("GitHub merge was rejected")

        merged = _gh_view(repo, pr, MERGED_FIELDS)
        if merged.get("number") != pr or merged.get("state") != "MERGED":
            raise GuardError("merged pull request could not be verified")
        merged_head = merged.get("headRefOid")
        if not _valid_sha(merged_head) or merged_head.lower() != reviewed_head:
            raise GuardError("merged pull request does not retain reviewed head")
        merge_commit = merged.get("mergeCommit")
        if not isinstance(merge_commit, dict) or not _valid_sha(merge_commit.get("oid")):
            raise GuardError("merged revision is unavailable")
        merged_revision = merge_commit["oid"].lower()
        if method == "merge" and not _is_ancestor(worktree, reviewed_head, merged_revision):
            raise GuardError("merged revision does not contain reviewed head")

        integrated_main = _fresh_main(worktree)
        if integrated_main != merged_revision:
            raise GuardError("origin/main does not resolve to verified merged revision")

    return merged_revision


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True, help="GitHub OWNER/REPO")
    parser.add_argument("--pr", required=True, type=int, help="pull request number")
    parser.add_argument("--head", required=True, help="exact independently reviewed head SHA")
    parser.add_argument(
        "--method",
        choices=("merge", "squash", "rebase"),
        default="merge",
        help="GitHub merge method",
    )
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    try:
        merged_revision = guarded_merge(
            args.repo,
            args.pr,
            args.head,
            args.method,
        )
    except LockBusy as error:
        print(f"merge-reviewed: {error}", file=sys.stderr)
        return LOCK_BUSY_EXIT
    except GuardError as error:
        print(f"merge-reviewed: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "status": "merged",
                "repo": args.repo,
                "pr": args.pr,
                "reviewedHead": args.head.lower(),
                "mergedRevision": merged_revision,
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
