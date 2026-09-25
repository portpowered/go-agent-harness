#!/usr/bin/env bash
# Delete this job's older main-branch Go caches once a newer one is saved.
#
# Keys look like go-<job>-<os>-<arch>-go<version>-<go.sum hash>-<sha>. Every
# main entry sharing the go-<job>-<os>-<arch>-go<version>- prefix other than
# the key just saved is superseded: restores fall back to the newest entry
# anyway, so the older ones only consume the repository's 10 GB cache quota.
#
# Usage: delete_superseded_caches.sh PRIMARY_KEY
# Env:   GH_TOKEN (actions: write), GITHUB_REPOSITORY, CACHE_REF (default
#        refs/heads/main), DRY_RUN=1 to print instead of delete.
# Failures are reported but never fail the job.
set -uo pipefail

key="${1:?primary cache key is required}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
ref="${CACHE_REF:-refs/heads/main}"

# Strip the trailing -<go.sum hash>-<sha> to get the job family prefix.
prefix="${key%-*}"
prefix="${prefix%-*}-"
case "$prefix" in
	go-*-go*-) ;;
	*) echo "::warning::unexpected cache key '$key'; not deleting superseded caches"; exit 0 ;;
esac

if ! listing="$(gh api --paginate "repos/$repo/actions/caches?key=$prefix&ref=$ref&per_page=100" \
	--jq '.actions_caches[] | "\(.id)\t\(.key)"')"; then
	echo "::warning::could not list caches with prefix $prefix; not deleting superseded caches"
	exit 0
fi

deleted=0
while IFS=$'\t' read -r id cache_key; do
	[ -n "$id" ] || continue
	# The key filter is a prefix match; keep only this job family and never
	# the entry just saved.
	case "$cache_key" in "$prefix"*) ;; *) continue ;; esac
	[ "$cache_key" != "$key" ] || continue
	if [ "${DRY_RUN:-0}" = "1" ]; then
		echo "would delete $cache_key (id $id)"
	elif gh api -X DELETE "repos/$repo/actions/caches/$id" >/dev/null; then
		echo "deleted superseded cache $cache_key"
	else
		echo "::warning::could not delete superseded cache $cache_key (id $id)"
		continue
	fi
	deleted=$((deleted + 1))
done <<<"$listing"
echo "superseded $prefix caches on $ref: $deleted"
exit 0
