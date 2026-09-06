#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat >&2 <<'USAGE'
usage: scripts/golangci-lint-working-tree.sh --analyzer PATH [--repo DIR] [--base REF] [--module DIR] [-- ARG ...]

Run the pinned golangci-lint binary with a temporary Git index that includes
the current module's Go working tree. This makes issues.new-from-rev include
modified, deleted, and non-ignored untracked Go files without changing the
user's index or writing unrelated working-tree blobs to the repository.
USAGE
}

repo_dir="."
module_dir="."
base_ref="${LINT_BASE:-origin/main}"
analyzer=""
run_args=()

while (($# > 0)); do
	case "$1" in
		--repo)
			(($# >= 2)) || { usage; exit 2; }
			repo_dir="$2"
			shift 2
			;;
		--base)
			(($# >= 2)) || { usage; exit 2; }
			base_ref="$2"
			shift 2
			;;
		--module)
			(($# >= 2)) || { usage; exit 2; }
			module_dir="$2"
			shift 2
			;;
		--analyzer)
			(($# >= 2)) || { usage; exit 2; }
			analyzer="$2"
			shift 2
			;;
		--)
			shift
			run_args=("$@")
			break
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "unknown option: $1" >&2
			usage
			exit 2
			;;
	esac
done

if [[ -z "$analyzer" ]]; then
	echo "--analyzer is required" >&2
	usage
	exit 2
fi
if [[ -z "$base_ref" ]]; then
	echo "--base must be non-empty" >&2
	exit 2
fi

repo_root="$(git -C "$repo_dir" rev-parse --show-toplevel)"
module_path="$repo_root/$module_dir"
if [[ ! -d "$module_path" ]]; then
	echo "module directory does not exist: $module_dir" >&2
	exit 2
fi
if [[ ! -x "$analyzer" && "$analyzer" != */* ]]; then
	if ! command -v "$analyzer" >/dev/null 2>&1; then
		echo "golangci-lint analyzer not found: $analyzer" >&2
		exit 2
	fi
fi
if [[ "$analyzer" == */* && ! -x "$analyzer" ]]; then
	echo "golangci-lint analyzer is not executable: $analyzer" >&2
	exit 2
fi

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/golangci-lint-index.XXXXXX")"
temporary_index="$temporary_dir/index"
temporary_objects="$temporary_dir/objects"
pathspec_file="$temporary_dir/go-pathspecs"
mkdir -p -- "$temporary_objects"
# Resolve the repository object database before changing Git's object
# directory environment. This path remains the read-only alternate for HEAD
# and the existing history.
repository_objects="$(git -C "$repo_root" rev-parse --path-format=absolute --git-path objects)"
cleanup() {
	rm -rf -- "$temporary_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

export GIT_INDEX_FILE="$temporary_index"
# Keep newly staged blobs in the temporary object database. Existing commits
# remain readable through the alternate object database, while unrelated
# working-tree files never enter either the temporary index or the repository
# object store.
export GIT_OBJECT_DIRECTORY="$temporary_objects"
export GIT_ALTERNATE_OBJECT_DIRECTORIES="$repository_objects"
git -C "$repo_root" read-tree HEAD
# Build a NUL-delimited pathspec list from the module's tracked and
# non-ignored untracked files, retaining only Go sources. The cached list is
# the HEAD tree loaded above, so deleted tracked Go files are included too.
{
	git -C "$repo_root" ls-files --cached -z -- "$module_dir"
	git -C "$repo_root" ls-files --others --exclude-standard -z -- "$module_dir"
} | while IFS= read -r -d '' path; do
	case "$path" in
		*.go) printf '%s\0' "$path" ;;
	esac
done >"$pathspec_file"

if [[ -s "$pathspec_file" ]]; then
	# Git's normal ignore rules apply. The temporary index captures tracked
	# modifications, deletions, and non-ignored untracked Go files together.
	git -C "$repo_root" add --all --pathspec-from-file="$pathspec_file" --pathspec-file-nul
fi

cd "$module_path"
lint_output="$temporary_dir/lint-output"
set +e
if ((${#run_args[@]} > 0)); then
	"$analyzer" run --new-from-rev "$base_ref" "${run_args[@]}" >"$lint_output" 2>&1
else
	"$analyzer" run --new-from-rev "$base_ref" >"$lint_output" 2>&1
fi
lint_status=$?
set -e
cat -- "$lint_output"

# Some golangci-lint loader failures have historically printed an error while
# still returning success when no lint issues were emitted. A successful
# `0 issues` line cannot make an uncompilable package a valid lint result, so
# fail closed on loader/type-check diagnostics independently of the analyzer's
# exit status. Keep the pattern narrow to diagnostics emitted by the loader;
# ordinary source text and linter findings remain governed by lint_status.
if grep -Eiq 'typechecking error|failed to load (package|packages)|could not load (package|packages)|cannot load (package|packages)' "$lint_output"; then
	if ((lint_status == 0)); then
		lint_status=1
	fi
fi
exit "$lint_status"
