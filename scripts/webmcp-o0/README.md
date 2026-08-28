# WebMCP O0 binding probe

This is a standalone Go module for the first WebMCP external-assumption gate.
It is deliberately not listed in the repository `go.work` and does not change
any existing module manifest. The selected pins are:

| Module | Pin |
| --- | --- |
| `github.com/chromedp/chromedp` | `v0.16.0` |
| `github.com/chromedp/cdproto` | `v0.0.0-20260714215040-dc233986426f` |

Run commands from this directory. `probe.sh` changes into its own directory
and forces `GOWORK=off` for every Go command.

```sh
./probe.sh metadata
./probe.sh test
./probe.sh typecheck
./probe.sh smoke
./probe.sh chrome
./probe.sh webmcp
./probe.sh detach
./probe.sh hermetic
./probe.sh go1.24.2
```

`test`, `typecheck`, and `smoke` use the normal selected toolchain. Because
both pinned modules declare `go 1.26`, the Go 1.24.2 command intentionally
uses `GOTOOLCHAIN=go1.24.2` (without automatic switching) and succeeds only
when it produces the expected `requires go >= 1.26` diagnostic. The command
may download the exact Go 1.24.2 toolchain through Go's normal toolchain
distribution before running the check.

`metadata` prints the effective toolchain, platform, complete resolved module
graph, and `go mod verify` result. The committed `go.sum` is the integrity
record used by the module; browser artifacts are intentionally out of scope
for this compatibility-only probe.

`chrome` downloads the Stable `mac-arm64` pin in
`chrome-for-testing.json`, rechecks the pin against the official Chrome for
Testing channel manifest, verifies the archive SHA-256, checks the extracted
executable version, and launches only with a temporary profile and
`127.0.0.1` remote debugging. It discovers the browser websocket from
Chrome's startup output, checks `/json/version`, and issues CDP
`Browser.getVersion` through the pinned Go bindings. The script owns and
terminates only the Chrome PID it starts and removes its exact temporary
directory. If Stable advances beyond the lock, it stops with an actionable
refresh-the-pin error rather than silently changing the experiment.

`webmcp` repeats the verified launch with the exact feature flags
`WebMCP,WebMCPTesting,DevToolsWebMCPSupport`, serves `fixture.html` from a
fresh `127.0.0.1` origin, and runs the Go page/protocol matrix. The fixture
sets `Origin-Agent-Cluster: ?1` and `Permissions-Policy: tools=(self)`. No
Origin-Trial token is supplied: this is the documented local-development flag
path. The JSON report records the page-visible `document.modelContext` /
`navigator.modelContextTesting` surfaces separately from the advertised CDP
`WebMCP` domain and the generated binding calls.

`detach` builds the probe once, starts its fixture server on `127.0.0.1`, and
launches the pinned Chrome for Testing binary headful with a temporary profile
and loopback-only debugging. It discovers the fixture page through
`GET /json/list` before the Go client starts, passes only the browser websocket
and explicit target ID to `detach-probe`, and never uses an allocator that
starts or owns the browser process. The client observes the initial sentinel, changes
it to `attached`, calls the typed `Target.detachFromTarget`, clears the
chromedp target reference before canceling client contexts, and exits without
`Target.closeTarget` or `Browser.close`. The shell independently checks the
same target in `/json/list`, starts a fresh client to reattach and change the
sentinel to `reattached`, and checks the target again. The launcher terminates
only the Chrome and fixture-server PIDs it started.

`hermetic` launches the same pinned browser with an isolated temporary profile,
starts an embedded fixture server on `127.0.0.1` from the Go probe, navigates a
temporary chromedp target to it, waits for the fixture's `#ready` signal, and
performs a visible `initial` -> `transitioned` state change. A separate CDP
evaluation reads the final state and the shell validates the expected JSON
report. The Go probe closes its own fixture server and temporary client target;
the launcher owns only the browser process and profile. This row proves generic
CDP fixture control and deliberately does not relabel that path as native or
shim WebMCP.
