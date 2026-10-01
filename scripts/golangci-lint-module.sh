#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat >&2 <<'USAGE'
usage: scripts/golangci-lint-module.sh --analyzer PATH [--repo DIR] [--module DIR] [--config FILE] [-- ARG ...]

Run the pinned golangci-lint binary on every file of one module (there is no
new-code filter: the configuration is enforced on all code).

--config selects the golangci-lint configuration (repository-relative or
absolute); without it golangci-lint discovers the nearest .golangci.yml.
Arguments after -- are passed to `golangci-lint run`.

The run fails closed on loader and type-check diagnostics even when the
analyzer exits successfully.
USAGE
}

repo_dir="."
module_dir="."
analyzer=""
config_file=""
run_args=()

while (($# > 0)); do
	case "$1" in
		--repo)
			(($# >= 2)) || { usage; exit 2; }
			repo_dir="$2"
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

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/golangci-lint-module.XXXXXX")"
cleanup() {
	rm -rf -- "$temporary_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

cd "$module_path"
lint_output="$temporary_dir/lint-output"
set +e
"$analyzer" run ${config_args[@]+"${config_args[@]}"} ${run_args[@]+"${run_args[@]}"} >"$lint_output" 2>&1
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
