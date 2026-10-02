# Dead-code gate

`make deadcode-check` (CI (static) and the prepush static stage) runs
`scripts/deadcode-check.py`. That script runs the pinned
`golang.org/x/tools/cmd/deadcode` with `-test`. The deadcode version is a
`tool` dependency of `tools/architecturegate`, so its `go.sum` pins it. The
gate fails when the dead functions differ from
[`deadcode-allowlist.txt`](deadcode-allowlist.txt), and when that list grows
against the merge base with `origin/main`. After you delete dead code,
`make deadcode-write` rewrites the list. The output names the merge-base
commit it compared against.

## Configurations analyzed

A function is dead only when every configuration that compiles its file
reports it unreachable from every main and every test.

| Configuration | Purpose |
| --- | --- |
| workspace, GOOS linux / darwin / windows, cgo off, tags `e2e,stress` | the portable baseline |
| workspace, linux + `nomicrophone,wireinject` | the hermetic coverage build and the Wire injector sources (they replace `wire_gen.go`); the two tags select independent files |
| workspace, windows + `nomicrophone` | the windows no-microphone device backend |
| the packages holding other-OS files (`!linux && !darwin && !windows`), as GOOS=js GOARCH=wasm, selected like `make lint-other-os` | the other-OS stubs; only those packages' tests are roots, so a stub whose portable twin is reached only from a main is allowlisted |
| workspace, native host with cgo (darwin or linux) | microphone, CoreAudio and other cgo sources of the host OS |
| `tests/embedding` (GOWORK=off) | proves public runtime API used by the independent consumer |
| `tools/*`, `tests/localai` (GOWORK=off, linux) | the standalone helper modules |

The native cgo configuration is host-specific: darwin hosts analyze the darwin
cgo sources and linux hosts (CI) analyze the linux cgo sources. So that the
result is the same on every host:

- A host-specific run judges only the files that no portable run compiles.
  Every other file gets the same verdict everywhere.
- An allowlist entry whose declaring files only a host-specific run compiles
  is never reported stale on a host that cannot compile those files.

Set `DEADCODE_NATIVE_CGO=0` to skip the native run. Do this on a Linux box
without a C toolchain or the system headers its cgo sources include; cgo
type-checking needs both. The CI ubuntu runners have them.

Each workspace run type-checks every package from source and peaks at about
4 GB of memory. The number of runs at once is therefore bounded by physical
memory (about 4.5 GB per run, at most one per CPU). `DEADCODE_JOBS=N`
overrides that bound.

## Not covered

- The cgo sources of the OS that is not the host are not analyzed on that
  host: darwin cgo sources in CI, and linux cgo sources on a Mac.
- deadcode does not follow `//go:linkname`.
- A function that is reached only from host cgo sources but declared in a
  portable file is reported dead and has to be allowlisted.

## Allowlist policy

The allowlist holds only genuine exceptions:

- `discovery.parseURLError.Error`: the type is returned as a concrete pointer
  and its `Error` method keeps it a well-formed error type, as the errname
  lint expects;
- `dropProbeSession.Done` and `Close` (agent-cli/test/integration): methods
  that `messages.Session` requires but that the probe never calls;
- `devices.openInterruptibleInput` in `interruptible_input_other.go`: the
  other-OS stub of a function reached only from mains, so the js/wasm run,
  whose roots are tests, cannot reach it.

Exported library API that this repository does not call itself has a small
behaviour test next to it (the `*public*_test.go` files), so it is
exercised and stays off the list. Remove an entry by deleting the code, or by
adding a real caller or a test that exercises it.
