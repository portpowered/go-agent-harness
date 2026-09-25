#!/usr/bin/env bash
# Prune a restored Go build cache to the entries the current job used.
#
# Usage: scripts/prune-go-build-cache.sh GOCACHE_DIR [MINUTES]
#
# The go command refreshes an entry's mtime when it uses an entry whose mtime
# is more than an hour old, and a restored cache keeps the mtimes it was saved
# with. After a job, every entry older than MINUTES (default 60) was therefore
# not used by the job and is removed, so a cache saved after this step holds
# the job's working set instead of growing with every saved run. Like the go
# command's own trim, only NN/<hash>-a and NN/<hash>-d entries are removed; a
# directory entry (a cached executable) is judged by its own mtime.
set -euo pipefail

cache_dir="${1:-}"
minutes="${2:-60}"
if [[ -z "$cache_dir" ]]; then
	sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
fi
case "$minutes" in
'' | *[!0-9]*) echo "prune-go-build-cache: MINUTES must be a non-negative integer, got '$minutes'" >&2; exit 2 ;;
esac
if [[ ! -d "$cache_dir" ]]; then
	echo "prune-go-build-cache: $cache_dir does not exist; nothing to prune"
	exit 0
fi

before="$(du -sk "$cache_dir" | cut -f1)"
find "$cache_dir" -mindepth 2 -maxdepth 2 \( -name '*-a' -o -name '*-d' \) -mmin "+$minutes" -exec rm -rf {} +
after="$(du -sk "$cache_dir" | cut -f1)"
echo "prune-go-build-cache: $cache_dir ${before}KiB -> ${after}KiB (removed entries unused for ${minutes}m)"
