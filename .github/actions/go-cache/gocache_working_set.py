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


def main() -> int:
    if len(sys.argv) != 3 or sys.argv[1] not in {"age", "prune"}:
        print("usage: gocache_working_set.py age|prune GOCACHE", file=sys.stderr)
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
