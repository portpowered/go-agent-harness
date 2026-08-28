# WebMCP O0 evidence

Status: story `webmcp-o0-toolchain-gate-001` complete; browser launch,
WebMCP availability, external-target ownership, and fixture trials remain
pending.

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

## Remaining O0 assumptions

The remaining rows will be filled by the later stories using the same dated
observed-versus-researched distinction:

| Assumption | Verdict | Story |
| --- | --- | --- |
| Pinned Chrome for Testing launch | Pending | `webmcp-o0-toolchain-gate-002` |
| Native WebMCP page/CDP surface and binding coverage | Pending | `webmcp-o0-toolchain-gate-003` |
| Detach-only preservation of an external visible tab | Pending | `webmcp-o0-toolchain-gate-004` |
| Hermetic loopback fixture control | Pending | `webmcp-o0-toolchain-gate-005` |
