#!/bin/sh
set -eu

probe_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
lock_file=${WEBMCP_O0_CHROME_LOCK:-$probe_dir/chrome-for-testing.json}
export GOWORK=off
tmp_root=
chrome_pid=

cleanup() {
	status=$?

	if [ -n "$chrome_pid" ] && kill -0 "$chrome_pid" 2>/dev/null; then
		kill -TERM "$chrome_pid" 2>/dev/null || true
		attempt=0
		while kill -0 "$chrome_pid" 2>/dev/null && [ "$attempt" -lt 40 ]; do
			attempt=$((attempt + 1))
			sleep 0.25
		done
		if kill -0 "$chrome_pid" 2>/dev/null; then
			kill -KILL "$chrome_pid" 2>/dev/null || true
		fi
		wait "$chrome_pid" 2>/dev/null || true
	fi

	if [ -n "$tmp_root" ] && [ -d "$tmp_root" ]; then
		rm -rf -- "$tmp_root"
	fi

	exit "$status"
}
trap cleanup EXIT

fail() {
	echo "ERROR: $*" >&2
	exit 1
}

for required_command in curl jq unzip shasum awk tail go; do
	command -v "$required_command" >/dev/null 2>&1 || fail "required command not found: $required_command"
done

case "$(uname -s)/$(uname -m)" in
	Darwin/arm64) ;;
	*) fail "this pinned artifact is for Darwin arm64; observed $(uname -s)/$(uname -m)" ;;
esac

[ -r "$lock_file" ] || fail "Chrome for Testing lock file is not readable: $lock_file"

channel=$(jq -er '.channel' "$lock_file") || fail "lock file has no channel"
platform=$(jq -er '.platform' "$lock_file") || fail "lock file has no platform"
expected_version=$(jq -er '.version' "$lock_file") || fail "lock file has no version"
expected_revision=$(jq -er '.revision' "$lock_file") || fail "lock file has no revision"
manifest_url=$(jq -er '.manifestURL' "$lock_file") || fail "lock file has no manifest URL"
manifest_retrieved_at=$(jq -er '.manifestRetrievedAt' "$lock_file") || fail "lock file has no manifest retrieval date"
expected_download_url=$(jq -er '.downloadURL' "$lock_file") || fail "lock file has no download URL"
expected_archive_sha=$(jq -er '.archiveSHA256' "$lock_file") || fail "lock file has no archive SHA-256"
executable_relative_path=$(jq -er '.executable' "$lock_file") || fail "lock file has no executable path"

[ "$platform" = "mac-arm64" ] || fail "unsupported lock platform: $platform"
case "$manifest_url" in
	https://googlechromelabs.github.io/chrome-for-testing/*) ;;
	*) fail "manifest URL is not an official Chrome for Testing URL: $manifest_url" ;;
esac
case "$expected_download_url" in
	https://storage.googleapis.com/chrome-for-testing-public/*) ;;
	*) fail "download URL is not an official Chrome for Testing URL: $expected_download_url" ;;
esac

tmp_root=$(mktemp -d "${TMPDIR:-/tmp}/webmcp-o0-chrome.XXXXXX")
manifest_file=$tmp_root/channel-manifest.json
archive_file=$tmp_root/chrome-mac-arm64.zip
extract_dir=$tmp_root/extracted
profile_dir=$(mktemp -d "$tmp_root/profile.XXXXXX")
stdout_log=$tmp_root/chrome.stdout
stderr_log=$tmp_root/chrome.stderr

curl -fsSL --retry 2 --max-time 60 "$manifest_url" -o "$manifest_file" || fail "could not download release manifest: $manifest_url"

manifest_channel=$(jq -er --arg channel "$channel" '.channels[$channel].channel' "$manifest_file") || fail "release manifest has no channel named $channel"
manifest_version=$(jq -er --arg channel "$channel" '.channels[$channel].version' "$manifest_file") || fail "release manifest has no version for channel $channel"
manifest_revision=$(jq -er --arg channel "$channel" '.channels[$channel].revision' "$manifest_file") || fail "release manifest has no revision for channel $channel"
manifest_download_url=$(jq -er --arg channel "$channel" --arg platform "$platform" '.channels[$channel].downloads.chrome[] | select(.platform == $platform) | .url' "$manifest_file") || fail "release manifest has no Chrome download for $channel/$platform"

[ "$manifest_channel" = "$channel" ] || fail "manifest channel mismatch: got $manifest_channel, want $channel"
[ "$manifest_version" = "$expected_version" ] || fail "locked $channel version $expected_version is no longer the manifest's current version ($manifest_version); refresh the lock deliberately"
[ "$manifest_revision" = "$expected_revision" ] || fail "manifest revision mismatch: got $manifest_revision, want $expected_revision"
[ "$manifest_download_url" = "$expected_download_url" ] || fail "manifest download URL differs from the locked URL"

curl -fsSL --retry 2 --max-time 300 -o "$archive_file" "$expected_download_url" || fail "could not download Chrome for Testing archive"
actual_archive_sha=$(shasum -a 256 "$archive_file" | awk '{print $1}')
[ "$actual_archive_sha" = "$expected_archive_sha" ] || fail "archive SHA-256 mismatch: got $actual_archive_sha, want $expected_archive_sha"

mkdir "$extract_dir"
unzip -q "$archive_file" -d "$extract_dir" || fail "could not extract verified Chrome archive"
chrome_binary=$extract_dir/$executable_relative_path
[ -x "$chrome_binary" ] || fail "verified archive did not contain executable: $executable_relative_path"

extracted_version=$("$chrome_binary" --version 2>&1) || fail "Chrome executable did not return a version"
case "$extracted_version" in
	*"$expected_version"*) ;;
	*) fail "Chrome executable version mismatch: got $extracted_version, want $expected_version" ;;
esac

# This process owns the temporary profile and is the only browser PID this
# script terminates. --remote-debugging-port=0 lets Chrome choose a free port;
# the DevTools websocket line is the deterministic handoff to the client.
"$chrome_binary" \
	--headless=new \
	--disable-gpu \
	--disable-background-networking \
	--disable-component-update \
	--disable-extensions \
	--disable-sync \
	--no-default-browser-check \
	--no-first-run \
	--remote-debugging-address=127.0.0.1 \
	--remote-debugging-port=0 \
	--user-data-dir="$profile_dir" \
	about:blank >"$stdout_log" 2>"$stderr_log" &
chrome_pid=$!

browser_websocket=
debug_port=
version_json=
attempt=0
while [ "$attempt" -lt 120 ]; do
	if ! kill -0 "$chrome_pid" 2>/dev/null; then
		echo "Chrome exited before exposing DevTools; stderr follows:" >&2
		sed -n '1,120p' "$stderr_log" >&2 || true
		fail "Chrome startup failed"
	fi

	browser_websocket=$(sed -n 's#.*DevTools listening on \(ws://127\.0\.0\.1:[0-9][0-9]*/devtools/browser/[^[:space:]]*\).*#\1#p' "$stderr_log" | tail -n 1 || true)
	if [ -n "$browser_websocket" ]; then
		debug_port=$(printf '%s\n' "$browser_websocket" | sed -n 's#ws://127\.0\.0\.1:\([0-9][0-9]*\)/.*#\1#p')
		if [ -n "$debug_port" ]; then
			if version_json=$(curl -fsS --max-time 2 "http://127.0.0.1:$debug_port/json/version" 2>/dev/null); then
				break
			fi
		fi
	fi

	attempt=$((attempt + 1))
	sleep 0.25
done

[ -n "$browser_websocket" ] || fail "Chrome startup timed out waiting for a loopback DevTools websocket"
[ -n "$version_json" ] || fail "Chrome startup timed out waiting for http://127.0.0.1:$debug_port/json/version"

http_browser=$(printf '%s' "$version_json" | jq -er '.Browser') || fail "Chrome version endpoint returned invalid JSON"
http_protocol_version=$(printf '%s' "$version_json" | jq -er '."Protocol-Version"') || fail "Chrome version endpoint omitted Protocol-Version"
http_websocket=$(printf '%s' "$version_json" | jq -er '.webSocketDebuggerUrl') || fail "Chrome version endpoint omitted websocket URL"
case "$http_browser" in
	*"$expected_version"*) ;;
	*) fail "HTTP browser version mismatch: got $http_browser, want $expected_version" ;;
esac
[ "$http_websocket" = "$browser_websocket" ] || fail "discovered websocket differs from /json/version"

cdp_report=$(GOWORK=off go run . cdp-version "$browser_websocket") || fail "CDP Browser.getVersion check failed"
cdp_product=$(printf '%s' "$cdp_report" | jq -er '.product') || fail "CDP report omitted product"
cdp_protocol_version=$(printf '%s' "$cdp_report" | jq -er '.protocolVersion') || fail "CDP report omitted protocolVersion"
case "$cdp_product" in
	*"$expected_version"*) ;;
	*) fail "CDP browser version mismatch: got $cdp_product, want $expected_version" ;;
esac

observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
jq -n \
	--arg observedAt "$observed_at" \
	--arg channel "$channel" \
	--arg manifestChannel "$manifest_channel" \
	--arg manifestURL "$manifest_url" \
	--arg manifestRetrievedAt "$manifest_retrieved_at" \
	--arg platform "$platform" \
	--arg version "$expected_version" \
	--arg revision "$expected_revision" \
	--arg archiveSHA256 "$actual_archive_sha" \
	--arg executableVersion "$extracted_version" \
	--arg remoteDebuggingAddress "127.0.0.1" \
	--argjson remoteDebuggingPort "$debug_port" \
	--arg websocketURL "$browser_websocket" \
	--arg httpBrowser "$http_browser" \
	--arg httpProtocolVersion "$http_protocol_version" \
	--arg cdpProduct "$cdp_product" \
	--arg cdpProtocolVersion "$cdp_protocol_version" \
	--argjson cdp "$cdp_report" \
'
{
  observedAt: $observedAt,
  channel: $channel,
  manifestChannel: $manifestChannel,
  manifestURL: $manifestURL,
  manifestRetrievedAt: $manifestRetrievedAt,
  platform: $platform,
  version: $version,
  revision: $revision,
  archiveSHA256: $archiveSHA256,
  executableVersion: $executableVersion,
  flags: [
    "--headless=new",
    "--disable-gpu",
    "--disable-background-networking",
    "--disable-component-update",
    "--disable-extensions",
    "--disable-sync",
    "--no-default-browser-check",
    "--no-first-run",
    "--remote-debugging-address=127.0.0.1",
    "--remote-debugging-port=0",
    "--user-data-dir=<temporary profile>"
  ],
  remoteDebuggingAddress: $remoteDebuggingAddress,
  remoteDebuggingPort: $remoteDebuggingPort,
  websocketURL: $websocketURL,
  httpVersionEndpoint: {
    Browser: $httpBrowser,
    ProtocolVersion: $httpProtocolVersion
  },
  cdpBrowserGetVersion: {
    product: $cdpProduct,
    protocolVersion: $cdpProtocolVersion,
    revision: $cdp.revision,
    goVersion: $cdp.goVersion
  },
  checks: {
    manifestPin: "matched",
    archiveIntegrity: "matched",
    executableVersion: "matched",
    loopbackEndpoint: "matched",
    cdpBrowserGetVersion: "matched"
  }
}'
