#!/bin/sh
set -eu

probe_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
lock_file=${WEBMCP_O0_CHROME_LOCK:-$probe_dir/chrome-for-testing.json}
export GOWORK=off
tmp_root=
chrome_pid=
fixture_pid=

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

	if [ -n "$fixture_pid" ] && kill -0 "$fixture_pid" 2>/dev/null; then
		kill -TERM "$fixture_pid" 2>/dev/null || true
		attempt=0
		while kill -0 "$fixture_pid" 2>/dev/null && [ "$attempt" -lt 40 ]; do
			attempt=$((attempt + 1))
			sleep 0.25
		done
		if kill -0 "$fixture_pid" 2>/dev/null; then
			kill -KILL "$fixture_pid" 2>/dev/null || true
		fi
		wait "$fixture_pid" 2>/dev/null || true
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

mode=${1:-chrome}
case "$mode" in
	chrome|webmcp|detach) ;;
	*) fail "usage: ./chrome-launch.sh [webmcp|detach]" ;;
esac

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

probe_binary=
fixture_url=
if [ "$mode" = "detach" ]; then
	probe_binary=$tmp_root/webmcp-o0-probe
	GOWORK=off go build -o "$probe_binary" . || fail "could not build the isolated probe binary"
	fixture_log=$tmp_root/detach-fixture.log
	"$probe_binary" serve-detach-fixture >"$fixture_log" 2>&1 &
	fixture_pid=$!
	fixture_url=
	attempt=0
	while [ "$attempt" -lt 80 ]; do
		if ! kill -0 "$fixture_pid" 2>/dev/null; then
			echo "Detach fixture server exited; log follows:" >&2
			sed -n '1,80p' "$fixture_log" >&2 || true
			fail "detach fixture server failed to start"
		fi
		fixture_url=$(sed -n 's/^fixtureURL=//p' "$fixture_log" | tail -n 1 || true)
		if [ -n "$fixture_url" ]; then
			break
		fi
		attempt=$((attempt + 1))
		sleep 0.25
	done
	[ -n "$fixture_url" ] || fail "detach fixture server startup timed out"
fi

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
# The empty expansion is omitted for the ordinary launch. WebMCP mode uses the
# command-line equivalents of the local WebMCP testing flag and DevTools
# WebMCP domain enablement.
webmcp_features=
if [ "$mode" = "webmcp" ]; then
	webmcp_features=--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport
fi

display_args=
chrome_start_url=about:blank
if [ "$mode" = "detach" ]; then
	chrome_start_url=$fixture_url
else
	display_args="--headless=new --disable-gpu"
fi
"$chrome_binary" \
	${display_args} \
	--disable-background-networking \
	--disable-component-update \
	--disable-extensions \
	--disable-sync \
	--no-default-browser-check \
	--no-first-run \
	--remote-debugging-address=127.0.0.1 \
	--remote-debugging-port=0 \
	${webmcp_features} \
	--user-data-dir="$profile_dir" \
	"$chrome_start_url" >"$stdout_log" 2>"$stderr_log" &
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

if [ "$mode" = "detach" ]; then
	target_list_before=
	target_before=
	target_id=
	attempt=0
	while [ "$attempt" -lt 80 ]; do
		if target_list_before=$(curl -fsS --max-time 2 "http://127.0.0.1:$debug_port/json/list"); then
			target_id=$(printf '%s' "$target_list_before" | jq -er --arg url "$fixture_url" '
				[.[] | select(.type == "page" and .url == $url)]
				| if length == 1 then .[0].id else empty end' 2>/dev/null || true)
			if [ -n "$target_id" ]; then
				target_before=$(printf '%s' "$target_list_before" | jq -cer --arg id "$target_id" '.[] | select(.id == $id)') || fail "could not capture the pre-attach target"
				break
			fi
		fi
		attempt=$((attempt + 1))
		sleep 0.25
	done
	[ -n "$target_id" ] || fail "could not identify the externally-created fixture target before client attach"
fi

cdp_report=$(GOWORK=off go run . cdp-version "$browser_websocket") || fail "CDP Browser.getVersion check failed"
cdp_product=$(printf '%s' "$cdp_report" | jq -er '.product') || fail "CDP report omitted product"
cdp_protocol_version=$(printf '%s' "$cdp_report" | jq -er '.protocolVersion') || fail "CDP report omitted protocolVersion"
case "$cdp_product" in
	*"$expected_version"*) ;;
	*) fail "CDP browser version mismatch: got $cdp_product, want $expected_version" ;;
esac

if [ "$mode" = "detach" ]; then
	[ -n "$probe_binary" ] || fail "detach probe binary was not built"

	detach_report=$(GOWORK=off "$probe_binary" detach-probe "$browser_websocket" "$target_id" initial) || fail "initial external-target detach probe failed"
	printf '%s' "$detach_report" | jq -e '
		.verdict == "PASS" and
		.phase == "initial" and
		.before.sentinel == "initial" and
		.after.sentinel == "attached" and
		.after.visibleText == "attached" and
		.lifecycle.api == "Target.detachFromTarget" and
		.lifecycle.targetCloseIssued == false and
		.lifecycle.browserCloseIssued == false' >/dev/null || fail "initial detach report did not prove detach-only cleanup"

	target_list_after_detach=
	target_after_detach=
	attempt=0
	while [ "$attempt" -lt 80 ]; do
		if target_list_after_detach=$(curl -fsS --max-time 2 "http://127.0.0.1:$debug_port/json/list"); then
			target_after_detach=$(printf '%s' "$target_list_after_detach" | jq -cer --arg id "$target_id" --arg url "$fixture_url" '
				[.[] | select(.type == "page" and .id == $id and .url == $url)]
				| if length == 1 then .[0] else empty end' 2>/dev/null || true)
			if [ -n "$target_after_detach" ]; then
				break
			fi
		fi
		attempt=$((attempt + 1))
		sleep 0.25
	done
	[ -n "$target_after_detach" ] || fail "external target disappeared after client detach"

	reattach_report=$(GOWORK=off "$probe_binary" detach-probe "$browser_websocket" "$target_id" reattach) || fail "reattach external-target probe failed"
	printf '%s' "$reattach_report" | jq -e '
		.verdict == "PASS" and
		.phase == "reattach" and
		.before.sentinel == "attached" and
		.after.sentinel == "reattached" and
		.after.visibleText == "reattached" and
		.lifecycle.api == "Target.detachFromTarget" and
		.lifecycle.targetCloseIssued == false and
		.lifecycle.browserCloseIssued == false' >/dev/null || fail "reattach report did not prove preserved state and detach-only cleanup"

	target_list_after_reattach=
	target_after_reattach=
	attempt=0
	while [ "$attempt" -lt 80 ]; do
		if target_list_after_reattach=$(curl -fsS --max-time 2 "http://127.0.0.1:$debug_port/json/list"); then
			target_after_reattach=$(printf '%s' "$target_list_after_reattach" | jq -cer --arg id "$target_id" --arg url "$fixture_url" '
				[.[] | select(.type == "page" and .id == $id and .url == $url)]
				| if length == 1 then .[0] else empty end' 2>/dev/null || true)
			if [ -n "$target_after_reattach" ]; then
				break
			fi
		fi
		attempt=$((attempt + 1))
		sleep 0.25
	done
	[ -n "$target_after_reattach" ] || fail "external target disappeared after reattach client detached"

	jq -n \
		--arg observedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--arg channel "$channel" \
		--arg manifestURL "$manifest_url" \
		--arg manifestRetrievedAt "$manifest_retrieved_at" \
		--arg platform "$platform" \
		--arg version "$expected_version" \
		--arg revision "$expected_revision" \
		--arg archiveSHA256 "$actual_archive_sha" \
		--arg executableVersion "$extracted_version" \
		--arg fixtureURL "$fixture_url" \
		--arg targetID "$target_id" \
		--arg websocketURL "$browser_websocket" \
		--arg httpBrowser "$http_browser" \
		--arg httpProtocolVersion "$http_protocol_version" \
		--argjson cdpBrowserGetVersion "$cdp_report" \
		--argjson targetBefore "$target_before" \
		--argjson targetAfterDetach "$target_after_detach" \
		--argjson targetAfterReattach "$target_after_reattach" \
		--argjson initialClient "$detach_report" \
		--argjson reattachClient "$reattach_report" \
'
{
  observedAt: $observedAt,
  channel: $channel,
  manifestURL: $manifestURL,
  manifestRetrievedAt: $manifestRetrievedAt,
  platform: $platform,
  version: $version,
  revision: $revision,
  archiveSHA256: $archiveSHA256,
  executableVersion: $executableVersion,
  headful: true,
  flags: [
    "--disable-background-networking",
    "--disable-component-update",
    "--disable-extensions",
    "--disable-sync",
    "--no-default-browser-check",
    "--no-first-run",
    "--remote-debugging-address=127.0.0.1",
    "--remote-debugging-port=0",
    "--user-data-dir=<temporary profile>",
    "<fixture-url>"
  ],
  remoteDebuggingAddress: "127.0.0.1",
  websocketURL: $websocketURL,
  httpVersionEndpoint: {
    Browser: $httpBrowser,
    ProtocolVersion: $httpProtocolVersion
  },
  cdpBrowserGetVersion: $cdpBrowserGetVersion,
  fixture: {
    origin: $fixtureURL,
    url: $fixtureURL,
    server: "probe-owned loopback server"
  },
  target: {
    id: $targetID,
    discoveredBeforeClientAttach: $targetBefore,
    afterDetach: $targetAfterDetach,
    afterReattach: $targetAfterReattach,
    sameTargetID: ($targetBefore.id == $targetID and
      $targetAfterDetach.id == $targetID and
      $targetAfterReattach.id == $targetID)
  },
  client: {
    initial: $initialClient,
    reattach: $reattachClient
  },
  independentObservation: {
    method: "GET /json/list",
    afterDetachTargetPresent: true,
    afterReattachTargetPresent: true,
    retainedTargetID: $targetID
  },
  verdict: "PASS"
}'
	exit 0
fi

if [ "$mode" = "webmcp" ]; then
	matrix_report=$(GOWORK=off go run . webmcp-matrix "$browser_websocket") || fail "WebMCP capability matrix failed"
	jq -n \
		--arg observedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--arg channel "$channel" \
		--arg manifestChannel "$manifest_channel" \
		--arg manifestURL "$manifest_url" \
		--arg manifestRetrievedAt "$manifest_retrieved_at" \
		--arg platform "$platform" \
		--arg version "$expected_version" \
		--arg revision "$expected_revision" \
		--arg archiveSHA256 "$actual_archive_sha" \
		--arg executableVersion "$extracted_version" \
		--arg websocketURL "$browser_websocket" \
		--arg httpBrowser "$http_browser" \
		--arg httpProtocolVersion "$http_protocol_version" \
		--argjson launchCDP "$cdp_report" \
		--argjson matrix "$matrix_report" \
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
    "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
    "--user-data-dir=<temporary profile>"
  ],
  originTrial: {
    state: "not supplied",
    token: "none",
    localFlag: "chrome://flags/#enable-webmcp-testing"
  },
  remoteDebuggingAddress: "127.0.0.1",
  websocketURL: $websocketURL,
  httpVersionEndpoint: {
    Browser: $httpBrowser,
    ProtocolVersion: $httpProtocolVersion
  },
  cdpBrowserGetVersion: $launchCDP,
  fixtureOrigin: $matrix.fixtureOrigin,
  matrix: $matrix,
  verdict: $matrix.verdict
}'
	exit 0
fi

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
