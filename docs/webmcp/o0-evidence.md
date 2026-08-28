# WebMCP O0 evidence

Status: stories `webmcp-o0-toolchain-gate-001` through
`webmcp-o0-toolchain-gate-004` complete; the hermetic fixture trial and final
consequence consolidation remain pending.

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

## Story 003: Determine native WebMCP surface availability

### Researched requirements

The [Chrome WebMCP documentation](https://developer.chrome.com/docs/ai/webmcp/)
describes the local-development flag as
`chrome://flags/#enable-webmcp-testing`, and says WebMCP is gated by origin
isolation and the `tools` Permissions Policy. Its documented origin trial starts
in Chrome 149; the [origin-trial announcement](https://developer.chrome.com/blog/ai-webmcp-origin-trial)
does not provide a token for this local fixture. The [WebMCP draft](https://webmachinelearning.github.io/webmcp/)
defines the producer API on `document.modelContext`, while the
[CDP WebMCP domain](https://chromedevtools.github.io/devtools-protocol/tot/WebMCP/)
advertises `enable`, `disable`, `invokeTool`, `cancelInvocation` and the four
tool lifecycle events. These are research inputs, not claims about the pinned
binary until the matrix below observes them.

### Reproduction command and matrix row

From `scripts/webmcp-o0/`:

```sh
./probe.sh webmcp
```

The command reuses the verified Stable artifact from story 002 and runs one
row on the target machine at `2026-08-28T08:41:12Z`:

| Field | Observed value |
| --- | --- |
| Channel / version / revision | `Stable` / `152.0.7977.64` / `1669021` |
| Platform | `mac-arm64` |
| Feature flags | `--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport` |
| Other launch flags | `--headless=new`, loopback `--remote-debugging-address=127.0.0.1`, `--remote-debugging-port=0`, isolated temporary profile, and the story 002 network/first-run flags |
| Origin-trial state | No `Origin-Trial` response header and no token; local flag path only |
| Fixture origin | Fresh `http://127.0.0.1:<ephemeral-port>/`, with `Origin-Agent-Cluster: ?1` and `Permissions-Policy: tools=(self)` |
| Artifact integrity | Story 002 lock and archive SHA-256 revalidated before launch |

The exact archive URL and SHA-256 remain the locked values in story 002. The
matrix report includes the ephemeral fixture origin and the complete flag list;
the port is intentionally not treated as a stable identifier.

### Observed page surface

The fixture was secure-context eligible (`isSecureContext: true`), origin-keyed
(`originAgentCluster: true`), and permitted the `tools` feature. The page
exposed `document.modelContext` and did not expose
`navigator.modelContext` or `navigator.modelContextTesting`:

```json
{
  "documentModelContext": {
    "present": true,
    "methods": {
      "registerTool": {"present": true, "length": 1},
      "getTools": {"present": true, "length": 0},
      "executeTool": {"present": true, "length": 2},
      "listTools": {"present": false},
      "callTool": {"present": false},
      "unregisterTool": {"present": false},
      "clearContext": {"present": false}
    }
  },
  "navigatorModelContext": {"present": false},
  "navigatorModelContextTesting": {"present": false},
  "descriptors": {
    "Document.prototype.modelContext": {"present": true, "hasGetter": true},
    "Navigator.prototype.modelContext": {"present": false},
    "Navigator.prototype.modelContextTesting": {"present": false}
  }
}
```

The fixture registered `webmcp_o0_probe_tool`; `document.modelContext.getTools()`
returned its name, description, origin, and serialized input schema. A direct
page invocation also completed with
`{"content":[{"type":"text","text":"webmcp-o0:producer"}]}` when the
input argument was passed as a JSON string. Passing an object instead produced
the observed `UnknownError: Failed to parse input arguments`; the probe keeps
that implementation detail visible and uses the successful serialized form.

### Observed CDP surface and binding coverage

The script fetched the browser's `/json/protocol` endpoint rather than
assuming a domain from the Go package. The pinned browser advertised:

```text
WebMCP methods: cancelInvocation, disable, enable, invokeTool
WebMCP events:  toolInvoked, toolResponded, toolsAdded, toolsRemoved
```

The selected `cdproto` revision has typed constructors for all four commands
(`WebMCP.enable`, `disable`, `invokeTool`, `cancelInvocation`) and typed event
structures for all four advertised events. `WebMCP.enable` succeeded through
the target executor; `toolsAdded` reported `webmcp_o0_probe_tool`, and the
typed `WebMCP.invokeTool` call produced `toolInvoked`, `toolResponded` with
status `Completed`, and output
`{"content":[{"type":"text","text":"webmcp-o0:cdp"}]}`. No raw CDP command
or unversioned binding was needed. Page evaluation is used for surface
inspection only; the adapter access path can use generated `cdproto/webmcp`.

### Verdict and downstream consequence

**PASS for native WebMCP availability and binding coverage on this row.** The
pinned Stable headless build exposed the native document producer API on a
real loopback origin, and the same page tool completed through the advertised
CDP domain and the selected generated Go bindings. This is native Chrome
support, not a shim or polyfill; `navigator.modelContextTesting` was absent and
is not substituted into the result. Lane D can target the generated CDP
WebMCP adapter with `WebMCP,WebMCPTesting,DevToolsWebMCPSupport` enabled and
must preserve the real-origin, origin-isolated, `tools`-permitted fixture
constraints. Lane I1 may use the same native path for its integration gate;
the page-side serialized-input behavior is an observed compatibility detail
for diagnostics, while CDP invocation remains the preferred adapter path.
No fallback is claimed or needed for this tested native row; a future pinned
build that loses the row must reopen the gate rather than silently labeling a
shim native.

## Story 004: Prove detach preserves an external visible tab

### Reproduction command and ownership boundary

From `scripts/webmcp-o0/`:

```sh
./probe.sh detach
```

The launcher built the isolated module with `GOWORK=off`, started the embedded
fixture server on `127.0.0.1`, launched the locked Chrome for Testing build
headful (there was no `--headless` flag), and discovered the fixture page with
`GET /json/list` before starting either Go client probe. The Go probe received
the browser websocket endpoint, the explicit page target ID, and a phase
(`initial` or `reattach`); it did not receive a Chrome executable, profile,
browser PID, or target selected by chromedp.

The Chrome row was the same verified Stable `152.0.7977.64` / revision
`1669021` / `mac-arm64` artifact from story 002, with archive SHA-256
`10033804338bd0a5aa098149a8dd64f3f2e0e8b201bf3d400d7c17d067ff696f`. The
headful launch flags were:

```text
--disable-background-networking
--disable-component-update
--disable-extensions
--disable-sync
--no-default-browser-check
--no-first-run
--remote-debugging-address=127.0.0.1
--remote-debugging-port=0
--user-data-dir=<temporary profile>
<fixture-url>
```

The observed run completed at `2026-08-28T08:57:02Z` with fixture
`http://127.0.0.1:57259/`, browser websocket
`ws://127.0.0.1:57271/devtools/browser/ed661df7-84c1-42b6-b5c3-4fb875d6a2e6`,
and target ID `71FE8876A15FC47D206447F69DEC1D0B`. The target was a page with
title `WebMCP O0 external target fixture` and the same URL at every independent
`/json/list` observation.

### Lifecycle API and observed result

The client used `chromedp.NewRemoteAllocator(endpoint, chromedp.NoModifyURL)`
and `chromedp.NewContext(..., chromedp.WithTargetID(targetID))`, then used
`chromedp.Run`/`chromedp.Evaluate` only against that existing target. The
detach path is deliberately not `chromedp.Cancel`: in the selected
`chromedp v0.16.0`, ordinary target-context cancellation also issues
`Target.closeTarget`. The probe instead:

1. reads the visible `initial` sentinel and changes it to `attached`;
2. calls typed `target.DetachFromTarget().WithSessionID(sessionID)` through the
   browser executor;
3. clears the client target reference before calling `cancelTarget`, then calls
   `cancelAllocator` to close only the client-side remote connection; and
4. leaves `Target.closeTarget` and `Browser.close` unissued.

The shell then independently queried `/json/list` and found the same target ID
and URL. A fresh Go client reattached with the same explicit target ID, observed
the retained `attached` sentinel, changed it to `reattached`, and detached by
the same path. A final independent `/json/list` query still found the same
target. The concise probe evidence was:

```text
initial client:  initial sentinel -> attached visible sentinel
after detach:   /json/list target present, same ID 71FE8876A15FC47D206447F69DEC1D0B
reattach client: attached sentinel -> reattached visible sentinel
after reattach: /json/list target present, same ID 71FE8876A15FC47D206447F69DEC1D0B
initial lifecycle:  Target.detachFromTarget; targetCloseIssued=false; browserCloseIssued=false
reattach lifecycle: Target.detachFromTarget; targetCloseIssued=false; browserCloseIssued=false
```

The experiment's JSON report records both CDP sessions, the target-list
snapshots, the exact loopback fixture, and `verdict: "PASS"`. The launcher
terminates only the Chrome PID and fixture-server PID it created and removes
only their temporary root after the independent checks finish.

### Verdict and downstream consequence

**PASS for detach-only preservation of an external visible tab.** A client may
attach to a caller-supplied target ID, change state, detach, and reconnect
without destroying the page or its state. Lane D must accept an explicit
remote endpoint and target ID, use a remote allocator, and implement the
detach-first/target-reference-clearing rule; it must never use a context
cancellation path that can close an externally owned target. Lane I1 can use
the same lifecycle assertion and must keep browser-process/profile cleanup
outside the client session. The result proves ownership-safe generic CDP
attachment; it does not by itself add a production session API or change the
Go-version finding from story 001.

## Remaining O0 assumptions

The remaining rows will be filled by the later stories using the same dated
observed-versus-researched distinction:

| Assumption | Verdict | Story |
| --- | --- | --- |
| Pinned Chrome for Testing launch | PASS | `webmcp-o0-toolchain-gate-002` |
| Native WebMCP page/CDP surface and binding coverage | PASS | `webmcp-o0-toolchain-gate-003` |
| Detach-only preservation of an external visible tab | PASS | `webmcp-o0-toolchain-gate-004` |
| Hermetic loopback fixture control | Pending | `webmcp-o0-toolchain-gate-005` |
