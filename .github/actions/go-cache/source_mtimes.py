#!/usr/bin/env python3
"""Give every checked-out file and directory an mtime derived from its content.

actions/checkout writes every file with the current time. Go's test result
cache keys each file a test opened or stat'd on its size and mtime (it does
not hash file contents), and refuses to cache a result that read a file
modified less than two seconds earlier, so a fresh checkout makes every such
test miss the cache on every run.

This sets each tracked file's mtime from its git blob id and each directory's
from its git tree id: the same content always gets the same mtime, on every
runner and branch, and changed content gets a different one (the mtime spans
about 2^59 nanosecond values, and a stale reuse would also need an equal
size). Unlike a constant epoch, a content change can never leave the
(size, mtime) key unchanged; unlike per-file last-commit times it needs no
history (a depth-1 blobless clone has every tree) and gives identical keys
for identical content regardless of which commit last touched it.

Paths outside the sparse checkout are skipped. Symlinks and submodules are
left alone.
"""

from __future__ import annotations

import os
import subprocess
import sys

# 2001-09-09T01:46:40Z, far in the past so no mtime is "too new", plus up to
# about twenty years.
BASE_NS = 1_000_000_000 * 1_000_000_000
SPAN_NS = 20 * 365 * 24 * 60 * 60 * 1_000_000_000


def mtime_ns(object_id: str) -> int:
    return BASE_NS + int(object_id[:16], 16) % SPAN_NS


def main() -> int:
    root = sys.argv[1] if len(sys.argv) > 1 else "."
    listing = subprocess.run(
        ["git", "-C", root, "ls-tree", "-r", "-t", "-z", "HEAD"],
        check=True,
        capture_output=True,
    ).stdout
    files = dirs = 0
    directories = [("", None)]
    for record in listing.split(b"\0"):
        if not record:
            continue
        meta, path = record.split(b"\t", 1)
        mode, kind, object_id = meta.decode().split(" ")
        name = os.path.join(root, os.fsdecode(path))
        if kind == "tree":
            directories.append((name, object_id))
            continue
        if kind != "blob" or mode == "120000":
            continue
        stamp = mtime_ns(object_id)
        try:
            os.utime(name, ns=(stamp, stamp))
        except FileNotFoundError:
            continue
        files += 1
    root_tree = subprocess.run(
        ["git", "-C", root, "rev-parse", "HEAD^{tree}"], check=True, capture_output=True, text=True
    ).stdout.strip()
    # Setting a file's mtime leaves its directory's alone, so directories go
    # last; the deepest first is not needed for the same reason.
    for name, object_id in directories:
        stamp = mtime_ns(object_id or root_tree)
        try:
            os.utime(name or root, ns=(stamp, stamp))
        except FileNotFoundError:
            continue
        dirs += 1
    print(f"source-mtimes: set {files} file and {dirs} directory mtimes from git object ids")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
