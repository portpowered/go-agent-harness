# WebMCP O0 evidence

Status: stories `webmcp-o0-toolchain-gate-001` and
`webmcp-o0-toolchain-gate-002` complete; WebMCP availability, external-target
ownership, and fixture trials remain pending.

## Run context

The compatibility probe was run on 2026-08-28 at 08:05:20 UTC on the target
machine:

| Field | Observed value |
| --- | --- |
| OS | macOS 15.6 (build 24G84) |
| Kernel/CPU | Darwin 24.6.0, `darwin/arm64` |
| Installed default Go | `go1.24.5` (`go version go1.24.5 darwin/arm64`) |
| Probe effective Go | `go1.26.0` (`go env GOVERSION` with automatic toolchain selection) |
| Probe module | `github.com/portpowered/go-agent-harness/scripts/webmcp-o0` |
| Workspace mode | `GOWORK=off` |

The probe module is outside the `go.work` module list. Its commands force
`GOWORK=off`, and no existing `go.mod`, `go.sum`, or `go.work` was changed.

## Story 001: Go and binding compatibility

### Selected pins and integrity

The standalone module selects the following exact direct dependencies:

| Module | Version | `go.mod` checksum | Module checksum |
| --- | --- | --- | --- |
| `github.com/chromedp/chromedp` | `v0.16.0` | `h1:rbuGKFT1vMcFcFqKfPIO1GpX/N+2s8onm2qMxZLbU5U=` | `h1:rOO4deOm4CbZgBCa8mD9g2rDyIoNs0BkgvNrlbp5ouk=` |
| `github.com/chromedp/cdproto` | `v0.0.0-20260714215040-dc233986426f` | `h1:RwFsSODCtFExll+GhHM6R92SARHR3Z3oipaxLHj46C0=` | `h1:0Z1zcSLEmnj2c2CmJYBqewtS6pxhB39bNWUSEUAWjgk=` |

The complete checksum record is [scripts/webmcp-o0/go.sum](../../scripts/webmcp-o0/go.sum).
The resolved graph from `GOWORK=off go list -m all` was:

```text
github.com/portpowered/go-agent-harness/scripts/webmcp-o0
github.com/chromedp/cdproto v0.0.0-20260714215040-dc233986426f
github.com/chromedp/chromedp v0.16.0
github.com/chromedp/sysutil v1.1.0
github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68
github.com/gobwas/httphead v0.1.0
github.com/gobwas/pool v0.2.1
github.com/gobwas/ws v1.4.0
github.com/ledongthuc/pdf v0.0.0-20220302134840-0c2507a12d80
github.com/orisano/pixelmatch v0.0.0-20220722002657-fb0b55479cde
golang.org/x/sys v0.47.0
```

Both direct modules declare `go 1.26` in their module metadata. The probe
module therefore also resolves to `go 1.26`; this is an observation and not a
workspace or production Go-version change.

### Reproduction commands

From `scripts/webmcp-o0/`:

```sh
./probe.sh metadata
./probe.sh test
./probe.sh typecheck
./probe.sh smoke
./probe.sh go1.24.2
```

`metadata` prints the effective toolchain, platform, module graph, and
`go mod verify` result. `test` and `typecheck` both passed, and `smoke`
executed the report-producing binary. `go mod verify` reported `all modules
verified`.

### Observed smoke evidence

The executable constructed all four generated command parameter types and all
four generated event types, and constructed/cancelled a `chromedp` remote
allocator pointed at the loopback endpoint without contacting a browser:

```json
{
  "goVersion": "go1.26.0",
  "goos": "darwin",
  "goarch": "arm64",
  "chromedpVersion": "v0.16.0",
  "cdprotoVersion": "v0.0.0-20260714215040-dc233986426f",
  "commands": ["WebMCP.enable", "WebMCP.disable", "WebMCP.invokeTool", "WebMCP.cancelInvocation"],
  "eventTypes": ["EventToolsAdded", "EventToolsRemoved", "EventToolInvoked", "EventToolResponded"],
  "allocatorURL": "http://127.0.0.1:9222",
  "checks": {"generatedWebMCP": "constructed", "remoteAllocator": "constructed-and-cancelled"}
}
```

The exact baseline check was:

```text
$ GOWORK=off GOTOOLCHAIN=go1.24.2 go test ./...
go: go.mod requires go >= 1.26 (running go 1.24.2; GOTOOLCHAIN=go1.24.2)
```

The command intentionally disables automatic toolchain switching and exits
successfully only when this expected diagnostic is observed. This preserves
the negative result instead of silently testing with a newer Go release.

### Verdict and Lane A consequence

**FAIL for the existing Go 1.24.2 baseline; PASS for the selected binding
surface under Go 1.26.0.** The pinned `chromedp`/`cdproto` pair cannot be
compiled by Go 1.24.2, and the minimum version stated by both module manifests
is Go 1.26. Lane A must either upgrade the whole workspace to one exact Go
1.26 patch release or select and re-prove an older compatible binding set. This
story records that downstream decision only; it does not change `go.work`, an
existing `go.mod`, or production code.

## Story 002: Pin and launch Chrome for Testing

### Research input and locked artifact

The [Chrome for Testing availability dashboard](https://googlechromelabs.github.io/chrome-for-testing/)
publishes the channel manifest used here:
`https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json`.
The manifest was retrieved on 2026-08-28 at 08:19:47 UTC. The following is the
committed lock in [scripts/webmcp-o0/chrome-for-testing.json](../../scripts/webmcp-o0/chrome-for-testing.json),
not an ambient installed browser:

| Field | Locked value |
| --- | --- |
| Channel | `Stable` |
| Platform | `mac-arm64` |
| Version | `152.0.7977.64` |
| Revision | `1669021` |
| Download URL | `https://storage.googleapis.com/chrome-for-testing-public/152.0.7977.64/mac-arm64/chrome-mac-arm64.zip` |
| Archive SHA-256 | `10033804338bd0a5aa098149a8dd64f3f2e0e8b201bf3d400d7c17d067ff696f` |
| Executable | `Google Chrome for Testing 152.0.7977.64` |

The script re-fetches the channel manifest and rejects a channel/version,
revision, or download URL mismatch. It downloads the archive, computes
`shasum -a 256`, compares it to the lock, and only then extracts or executes
the binary. A later rerun stops with an actionable lock-refresh error if the
Stable channel has advanced.

### Reproduction command and observed launch

From `scripts/webmcp-o0/`:

```sh
./probe.sh chrome
```

The launch uses a newly-created temporary profile and these exact browser
flags:

```text
--headless=new
--disable-gpu
--disable-background-networking
--disable-component-update
--disable-extensions
--disable-sync
--no-default-browser-check
--no-first-run
--remote-debugging-address=127.0.0.1
--remote-debugging-port=0
--user-data-dir=<temporary profile>
about:blank
```

The command passed on the target machine at 2026-08-28 08:20:11 UTC. Its
concise report was:

```json
{
  "channel": "Stable",
  "platform": "mac-arm64",
  "version": "152.0.7977.64",
  "revision": "1669021",
  "archiveSHA256": "10033804338bd0a5aa098149a8dd64f3f2e0e8b201bf3d400d7c17d067ff696f",
  "executableVersion": "Google Chrome for Testing 152.0.7977.64",
  "remoteDebuggingAddress": "127.0.0.1",
  "remoteDebuggingPort": 54222,
  "websocketURL": "ws://127.0.0.1:54222/devtools/browser/c5be83e9-f812-45fa-bccc-acaca5e78327",
  "httpVersionEndpoint": {"Browser": "Chrome/152.0.7977.64", "ProtocolVersion": "1.3"},
  "cdpBrowserGetVersion": {"product": "Chrome/152.0.7977.64", "protocolVersion": "1.3"},
  "checks": {
    "manifestPin": "matched",
    "archiveIntegrity": "matched",
    "executableVersion": "matched",
    "loopbackEndpoint": "matched",
    "cdpBrowserGetVersion": "matched"
  }
}
```

The port is intentionally assigned by Chrome with `--remote-debugging-port=0`;
the script deterministically discovers the `ws://127.0.0.1:<port>/devtools/browser/...`
URL from Chrome's startup line, then cross-checks it against
`http://127.0.0.1:<port>/json/version`. The Go check invokes CDP
`Browser.getVersion` through the pinned `chromedp`/`cdproto` module. The shell
trap sends termination only to the Chrome PID it started, waits, and removes
only its exact temporary root; the remote allocator does not issue
`Browser.close`. This proves pinned launch and generic CDP reachability, not
native WebMCP availability.

### Verdict and downstream consequence

**PASS for the pinned Chrome-for-Testing launch assumption.** Stable
`152.0.7977.64` is obtainable for `darwin/arm64`, its archive and executable
identity match the lock, and both the HTTP version endpoint and CDP
`Browser.getVersion` report the same build over loopback. Lane A can use this
artifact pin while preserving the separate Go 1.24.2 compatibility finding;
Lane D/I1 may treat this as the browser acquisition and generic CDP launch
baseline, but it does not authorize a native WebMCP claim or change the
detach-ownership rule that story 004 must prove.

## Remaining O0 assumptions

The remaining rows will be filled by the later stories using the same dated
observed-versus-researched distinction:

| Assumption | Verdict | Story |
| --- | --- | --- |
| Pinned Chrome for Testing launch | PASS | `webmcp-o0-toolchain-gate-002` |
| Native WebMCP page/CDP surface and binding coverage | Pending | `webmcp-o0-toolchain-gate-003` |
| Detach-only preservation of an external visible tab | Pending | `webmcp-o0-toolchain-gate-004` |
| Hermetic loopback fixture control | Pending | `webmcp-o0-toolchain-gate-005` |
