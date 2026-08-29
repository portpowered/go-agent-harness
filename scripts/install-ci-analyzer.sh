#!/usr/bin/env bash

set -euo pipefail

die() {
	echo "install-ci-analyzer: $*" >&2
	exit 1
}

tool_name="${1:-}"
case "$tool_name" in
	golangci-lint|staticcheck) ;;
	*)
		die "usage: $0 golangci-lint|staticcheck"
		;;
esac

runner_os="${RUNNER_OS:-$(uname -s)}"
runner_arch="${RUNNER_ARCH:-$(uname -m)}"
case "${runner_os}:${runner_arch}" in
	Linux:X64|Linux:x86_64)
		platform_id="linux-amd64"
		;;
	Linux:ARM64|Linux:aarch64)
		platform_id="linux-arm64"
		;;
	macOS:X64|Darwin:x86_64)
		platform_id="darwin-amd64"
		;;
	macOS:ARM64|Darwin:arm64)
		platform_id="darwin-arm64"
		;;
	*)
		die "unsupported runner platform ${runner_os}/${runner_arch}; supported platforms are Linux X64/ARM64 and macOS X64/ARM64"
		;;
esac

case "$tool_name" in
	golangci-lint)
		version="${GOLANGCI_LINT_VERSION:-v2.9.0}"
		version_without_v="${version#v}"
		archive_name="golangci-lint-${version_without_v}-${platform_id}.tar.gz"
		archive_url="https://github.com/golangci/golangci-lint/releases/download/${version}/${archive_name}"
		binary_name="golangci-lint"
		expected_version_marker="golangci-lint has version ${version_without_v}"
		case "${version}:${platform_id}" in
			v2.9.0:linux-amd64) expected_sha256="493aaaca2eba6c8bcef847d92716bbd91bbac4b22cdbb0ab5b6a581b32946091" ;;
			v2.9.0:linux-arm64) expected_sha256="94e80cdb51c73c20a313bd3afa1fb23137728813c19fd730248a1e8678fcc46d" ;;
			v2.9.0:darwin-amd64) expected_sha256="ba29a353be54a74c45946763983808dc8305eeeca73db1761b5ab112f87f8157" ;;
			v2.9.0:darwin-arm64) expected_sha256="a86eabba3507deddd21f2a01a1df2a0ee5bc5c8178d4165cdcaaad8597358760" ;;
			*) die "no pinned SHA-256 is registered for ${tool_name} ${version} on ${platform_id}" ;;
		esac
		;;
	staticcheck)
		version="${STATICCHECK_VERSION:-2026.1}"
		archive_name="staticcheck_${platform_id//-/_}.tar.gz"
		archive_url="https://github.com/dominikh/go-tools/releases/download/${version}/${archive_name}"
		binary_name="staticcheck"
		expected_version_marker="staticcheck ${version}"
		case "${version}:${platform_id}" in
			2026.1:linux-amd64) expected_sha256="9242b4bf5b9f5481fd720ec6d1018b38fbffe0e2730e498923c6e8053e8576be" ;;
			2026.1:linux-arm64) expected_sha256="dde37c023073aff5d910a85536a80b92a2ae0db75f6a89afcec1272c4fabd6fd" ;;
			2026.1:darwin-amd64) expected_sha256="4b1483a2b21d555bc04dedb00823143dca66d5e53ac98db8e55ae6df87bebfad" ;;
			2026.1:darwin-arm64) expected_sha256="f71553886fe4bb313da317d7abc3e16fe3cae2dba54f1e07a94a1ae160beced2" ;;
			*) die "no pinned SHA-256 is registered for ${tool_name} ${version} on ${platform_id}" ;;
		esac
		;;
esac

tool_dir="${CI_ANALYZER_BIN_DIR:-${HOME}/.cache/go-agent-harness/ci-analyzers/bin}"
binary_path="${tool_dir}/${binary_name}"
mkdir -p "$tool_dir"

if [[ ! -e "$binary_path" ]]; then
	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "$tmp_dir"' EXIT
	archive_path="${tmp_dir}/${archive_name}"
	extract_dir="${tmp_dir}/extracted"

	echo "install-ci-analyzer: downloading ${tool_name} ${version} for ${runner_os}/${runner_arch}"
	if ! curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 --retry-all-errors --output "$archive_path" "$archive_url"; then
		die "download failed for ${archive_url}"
	fi

	actual_sha256=""
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sha256="$(sha256sum "$archive_path" | awk '{print $1}')"
	elif command -v shasum >/dev/null 2>&1; then
		actual_sha256="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
	else
		die "neither sha256sum nor shasum is available to verify ${archive_name}"
	fi
	if [[ "$actual_sha256" != "$expected_sha256" ]]; then
		die "integrity check failed for ${archive_name}: expected SHA-256 ${expected_sha256}, got ${actual_sha256}"
	fi
	echo "install-ci-analyzer: verified SHA-256 ${actual_sha256}"

	mkdir -p "$extract_dir"
	if ! tar -xzf "$archive_path" -C "$extract_dir"; then
		die "extraction failed for ${archive_name}"
	fi
	source_binary="$(find "$extract_dir" -type f -name "$binary_name" -print -quit)"
	if [[ -z "$source_binary" ]]; then
		die "extracted ${archive_name} did not contain a ${binary_name} executable"
	fi
	install -m 0755 "$source_binary" "$binary_path"
else
	echo "install-ci-analyzer: using cached ${tool_name} at ${binary_path}"
fi

if [[ ! -x "$binary_path" ]]; then
	die "installed ${tool_name} is not executable at ${binary_path}"
fi

version_output=""
if ! version_output="$("$binary_path" --version 2>&1)"; then
	die "could not execute ${binary_path} --version; output was: ${version_output}"
fi
echo "$version_output"
if [[ "$version_output" != *"$expected_version_marker"* ]]; then
	die "version validation failed: expected ${tool_name} ${version}, output was: ${version_output}"
fi

if [[ -n "${GITHUB_PATH:-}" ]]; then
	printf '%s\n' "$tool_dir" >> "$GITHUB_PATH"
fi
echo "install-ci-analyzer: ${tool_name} ${version} is ready at ${binary_path}"
