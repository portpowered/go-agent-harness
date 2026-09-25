#!/usr/bin/env bash
# Prove agent-cli builds from a standalone checkout (GOWORK=off) without
# compiling it a second time.
#
# The workspace build (`make build`) already compiles and links
# agent-cli/cmd/yui. A standalone build differs from it only in module
# resolution, so it is proven by showing that, without go.work:
#   1. the sibling library modules resolve to this checkout's directories;
#   2. every module in the binary's dependency graph resolves (go.mod and
#      go.sum are complete, checked by `go list` in readonly mode); and
#   3. each module is selected at the same version as in the workspace, so
#      the standalone compile sees exactly the sources the workspace build
#      compiled.
# If the selections ever diverge, the script falls back to the full
# standalone build instead of failing, so divergence is still validated.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root/agent-cli"
go_cmd="${GO:-go}"
main_package="./cmd/yui"

for module in go-agent-loop go-llm-gateway go-audio go-device-gateway go-agent-runtime; do
	replaced="$(GOWORK=off "$go_cmd" list -m -f '{{.Replace.Dir}}' "github.com/portpowered/go-agent-harness/$module")"
	expected="$(cd "../$module" && pwd)"
	if [ "$replaced" != "$expected" ]; then
		echo "standalone agent-cli resolves $module to '$replaced', want the adjacent checkout '$expected'" >&2
		exit 1
	fi
done

# Workspace and replaced-by-directory modules print as "local"; every other
# module prints its selected version.
format='{{with .Module}}{{.Path}}@{{if .Replace}}{{if .Replace.Version}}{{.Replace.Path}}@{{.Replace.Version}}{{else}}local{{end}}{{else if .Main}}local{{else}}{{.Version}}{{end}}{{end}}'
standalone="$(GOWORK=off "$go_cmd" list -mod=readonly -deps -test -f "$format" "$main_package" | sort -u)"
workspace="$("$go_cmd" list -deps -test -f "$format" "$main_package" | sort -u)"

if [ "$standalone" = "$workspace" ]; then
	echo "standalone agent-cli selects the workspace module versions for $main_package ($(wc -l <<<"$standalone" | tr -d ' ') modules)"
	exit 0
fi

echo "standalone and workspace module selections differ; building $main_package standalone:"
diff <(printf '%s\n' "$workspace") <(printf '%s\n' "$standalone") || true
output_dir="$(mktemp -d)"
trap 'rm -rf "$output_dir"' EXIT
GOWORK=off "$go_cmd" build -o "$output_dir/yui-standalone" "$main_package"
