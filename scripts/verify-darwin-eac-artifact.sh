#!/usr/bin/env bash
set -euo pipefail

artifact="${1:?artifact path is required}"
target="${2:?darwin target is required}"

case "$target" in
  darwin_amd64*) expected_arch="x86_64" ;;
  darwin_arm64*) expected_arch="arm64" ;;
  *) echo "unexpected native EAC target: $target" >&2; exit 1 ;;
esac

file "$artifact" | grep -q "Mach-O 64-bit executable $expected_arch"
build_info="$(go version -m "$artifact")"
grep -q $'dep\tgithub.com/ebitengine/purego\tv0.11.0' <<<"$build_info"
grep -q $'build\tCGO_ENABLED=1' <<<"$build_info"
grep -q $'build\tGOOS=darwin' <<<"$build_info"

echo "verified $target artifact contains the PureGo AUVoiceIO dependency and CGO CoreAudio pipes"
