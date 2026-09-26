#!/usr/bin/env python3
"""Keep a restored Go build cache to the entries one CI job actually uses.

`age` runs right after the cache is restored: it backdates every entry by
two hours. The go command refreshes an entry's mtime whenever it reuses an
entry whose mtime is more than an hour old, so after the job every reused
entry is fresh again while unused ones stay backdated.

`prune` runs before the cache is saved: it deletes entries still older than
ninety minutes, i.e. entries this job never touched. The saved cache is then
exactly this job's working set instead of growing with every commit until
Go's own five-day trim catches up.

`snapshot MANIFEST DIR...` runs after the restore and records the file names
under each cached directory (plus the newline-separated directories in
$EXTRA_PATHS, go-cache's extra-paths); `changed MANIFEST DIR...` runs before the save
and prints `save=true` when any directory gained a file since (a new build,
test result, module or lint analysis entry) and `save=false` otherwise. A
cache that gained nothing would save the same entries it restored, so
go-cache-save skips the upload (entries it would prune stay until the next
save that has something new; they are what the job restored, so the cache
does not grow).
"""

from __future__ import annotations

import os
import sys
import time
from pathlib import Path

AGE_SECONDS = 2 * 60 * 60
UNUSED_AFTER_SECONDS = 90 * 60


def entries(cache_dir: Path):
    # Cache entries live in two-hex-digit shard directories; leave README,
    # trim.txt and anything else the go command owns alone.
    for shard in cache_dir.iterdir():
        if shard.is_dir() and len(shard.name) == 2:
            yield from (entry for entry in shard.iterdir() if entry.is_file())


def file_names(directories: list[str]) -> set[str]:
    names = set()
    for index, directory in enumerate(directories):
        for parent, _, files in os.walk(directory):
            relative = os.path.relpath(parent, directory)
            names.update(f"{index}/{relative}/{name}" for name in files)
    return names


def manifest(mode: str, path: str, directories: list[str]) -> int:
    directories = directories + [line.strip() for line in os.environ.get("EXTRA_PATHS", "").splitlines() if line.strip()]
    if mode == "snapshot":
        Path(path).write_text("\n".join(sorted(file_names(directories))) + "\n")
        print(f"snapshot: recorded the files of {len(directories)} cached directories")
        return 0
    try:
        before = set(Path(path).read_text().splitlines())
    except FileNotFoundError:
        print("save=true")
        return 0
    added = file_names(directories) - before
    print(f"changed: {len(added)} new cached files", file=sys.stderr)
    print(f"save={'true' if added else 'false'}")
    return 0


def main() -> int:
    if len(sys.argv) >= 3 and sys.argv[1] in {"snapshot", "changed"}:
        return manifest(sys.argv[1], sys.argv[2], sys.argv[3:])
    if len(sys.argv) != 3 or sys.argv[1] not in {"age", "prune"}:
        print("usage: gocache_working_set.py age|prune GOCACHE | snapshot|changed MANIFEST DIR...", file=sys.stderr)
        return 2
    mode, cache_dir = sys.argv[1], Path(sys.argv[2])
    if not cache_dir.is_dir():
        print(f"{mode}: no Go build cache at {cache_dir}")
        return 0
    now = time.time()
    count = 0
    kept = 0
    if mode == "age":
        backdated = now - AGE_SECONDS
        for entry in entries(cache_dir):
            os.utime(entry, (backdated, backdated))
            count += 1
        print(f"age: backdated {count} Go build cache entries")
        return 0
    cutoff = now - UNUSED_AFTER_SECONDS
    for entry in entries(cache_dir):
        if entry.stat().st_mtime < cutoff:
            entry.unlink()
            count += 1
        else:
            kept += 1
    print(f"prune: removed {count} unused Go build cache entries, kept {kept}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
