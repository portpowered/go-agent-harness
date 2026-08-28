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
