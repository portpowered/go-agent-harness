#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat >&2 <<'USAGE'
usage: scripts/golangci-lint-working-tree.sh --analyzer PATH [--repo DIR] [--base REF] [--module DIR] [--config FILE] [--all-code] [--run-if-changed PATH]... [-- ARG ...]

Run the pinned golangci-lint binary for one module.

By default the run is limited to new code: it uses a temporary Git index that
includes the current module's Go working tree and passes --new-from-rev, so
modified, deleted, and non-ignored untracked Go files are compared with the
base without changing the user's index or writing unrelated working-tree
blobs to the repository. A module with no Go file in that diff is skipped
without running the analyzer, because no issue could pass the filter. The
module still runs when its go.mod or go.sum, the --config file, or any
repository-relative --run-if-changed path (for example the lint tooling)
differs from the base, so a broken configuration or tooling change is always
loaded and reported.

--all-code lints every file in the module with no new-from-rev filter. This is
the hard pass that enforces .golangci.yml on legacy and new code alike.
--config selects the golangci-lint configuration (repository-relative or
absolute); without it golangci-lint discovers the nearest .golangci.yml.
USAGE
}

repo_dir="."
module_dir="."
base_ref="${LINT_BASE:-origin/main}"
analyzer=""
config_file=""
all_code=0
run_if_changed=()
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
		--config)
			(($# >= 2)) || { usage; exit 2; }
			config_file="$2"
			shift 2
			;;
		--all-code)
			all_code=1
			shift
			;;
		--run-if-changed)
			(($# >= 2)) || { usage; exit 2; }
			run_if_changed+=("$2")
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
config_args=()
if [[ -n "$config_file" ]]; then
	if [[ "$config_file" != /* ]]; then
		config_file="$repo_root/$config_file"
	fi
	if [[ ! -f "$config_file" ]]; then
		echo "golangci-lint config does not exist: $config_file" >&2
		exit 2
	fi
	config_args=(--config "$config_file")
fi

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/golangci-lint-index.XXXXXX")"
cleanup() {
	rm -rf -- "$temporary_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

new_code_args=()
if ((all_code == 0)); then
	new_code_args=(--new-from-rev "$base_ref")
	temporary_index="$temporary_dir/index"
	temporary_objects="$temporary_dir/objects"
	pathspec_file="$temporary_dir/go-pathspecs"
	mkdir -p -- "$temporary_objects"
	# Resolve the repository object database before changing Git's object
	# directory environment. This path remains the read-only alternate for HEAD
	# and the existing history.
	repository_objects="$(git -C "$repo_root" rev-parse --path-format=absolute --git-path objects)"

	export GIT_INDEX_FILE="$temporary_index"
	# Keep newly staged blobs in the temporary object database. Existing commits
	# remain readable through the alternate object database, while unrelated
	# working-tree files never enter either the temporary index or the
	# repository object store.
	export GIT_OBJECT_DIRECTORY="$temporary_objects"
	export GIT_ALTERNATE_OBJECT_DIRECTORIES="$repository_objects"
	git -C "$repo_root" read-tree HEAD
	# Build a NUL-delimited pathspec list from the module's tracked and
	# non-ignored untracked files, retaining only Go sources. The cached list
	# is the HEAD tree loaded above, so deleted tracked Go files are included.
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

	# --new-from-rev reports only issues on lines that `git diff <base>` shows
	# as added or changed, and this temporary index is what that diff sees.
	# When the module has no such Go file, no issue can survive the filter, so
	# skip loading and analyzing the module, unless an input that decides
	# whether the run itself is valid changed: the module's go.mod/go.sum, the
	# configuration, or a --run-if-changed path. An unresolvable base still
	# runs so golangci-lint reports the bad revision itself.
	if git -C "$repo_root" rev-parse --verify --quiet "${base_ref}^{commit}" >/dev/null; then
		changed_go="$(git -C "$repo_root" diff --name-only --no-renames "$base_ref" -- "$module_dir" | grep -E '\.go$' || true)"
		run_inputs=("$module_dir/go.mod" "$module_dir/go.sum" ${run_if_changed[@]+"${run_if_changed[@]}"})
		if [[ "$config_file" == "$repo_root"/* ]]; then
			run_inputs+=("${config_file#"$repo_root"/}")
		fi
		changed_inputs="$(git -C "$repo_root" diff --name-only --no-renames "$base_ref" -- "${run_inputs[@]}")"
		if [[ -z "$changed_go" && -z "$changed_inputs" ]]; then
			echo "no Go changes in $module_dir since $base_ref; new-code lint has nothing to check"
			exit 0
		fi
	fi
fi

cd "$module_path"
lint_output="$temporary_dir/lint-output"
set +e
"$analyzer" run ${config_args[@]+"${config_args[@]}"} ${new_code_args[@]+"${new_code_args[@]}"} ${run_args[@]+"${run_args[@]}"} >"$lint_output" 2>&1
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
