# golangci-lint policy

`make lint` runs the pinned golangci-lint (v2.9.0, through the Makefile's
analyzer resolver) once for every `LINT_MODULES` module, then once more for
Wire injector packages and once for packages holding other-OS files.
Production and test files are both checked, including files behind opt-in
build tags and platform-specific files (see
[Build tags](#build-tags) and [Operating systems](#operating-systems)).
golangci-lint is the only owner of size, complexity, package-global and `init`
limits and of every import rule depguard can express; the architecture gate
checks only service shape and the boundaries golangci-lint cannot express (see
[Size and complexity](#size-and-complexity) and
[Boundaries: depguard and the architecture gate](#boundaries-depguard-and-the-architecture-gate)).

## One policy, all code

[`.golangci.yml`](../../.golangci.yml) is the only configuration. It has no
`new-from-rev` filter, no new-code-only pass and no baseline: every enabled
linter applies to every file, and any finding fails lint.

| Check | Limit |
| --- | --- |
| `linters.default: standard` | `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused` |
| `errcheck` | also checks blank assignments and type assertions; only Win32 `LazyProc.Call`, `Proc.Call` and `SyscallN` are excluded |
| `revive` `file-length-limit` | 978 physical lines per file (comments and blank lines count) |
| `funlen` | 100 lines and 78 statements per function |
| `gocyclo`, `gocognit` | 30 |
| `nestif` | 5 |
| `dupl` | 150 tokens |
| Correctness and error flow | `nilerr`, `nilnesserr`, `bodyclose`, `durationcheck`, `errorlint`, `contextcheck`, `containedctx`, `noctx`, `fatcontext`, `gocritic` (diagnostic tag), `gosec` (selected rules), `makezero`, `reassign`, `asasalint`, `bidichk`, `wastedassign`, `unparam`, `recvcheck`, `errname`, `predeclared`, `iface` |
| Policy values and finite state | `goconst`, `mnd`, `exhaustive` |
| Package state and prohibited effects | `gochecknoglobals`, `gochecknoinits`, `forbidigo`, `depguard`, `godox` |
| Modern standard-library idioms | `exptostd`, `canonicalheader`, `usestdlibvars`, `mirror`, `copyloopvar`, `intrange`, `misspell`, `nakedret` |
| Tests | `testifylint`, `usetesting`, `tparallel`, `thelper` |
| Suppression hygiene | `nolintlint` |

`forbidigo` runs with `analyze-types`, so patterns match the resolved package
and receiver type: `os.Getwd`, `os.UserHomeDir`, `fmt.Print*`, `time.Sleep`,
`context.Background`/`TODO`, `panic`, and `Skip*` on `testing.T`, `B`, `TB`
and `F` (through `testing.common`) are forbidden. Context roots and panics are
allowed in `cmd/` packages, `main.go` files and tests. Generated files (for
example Wire's `wire_gen.go`) are excluded, and only with the standard
`// Code generated ... DO NOT EDIT.` header (`exclusions.generated: strict`).

Every finding is reported (`uniq-by-line: false`). A `//nolint` directive must
name one linter and give a reason. An unused directive is itself a finding.

Some errors are safe to discard, for example a `Close` failure after a
successful read. Discard them with a documented helper instead of a blank
assignment. Do not turn a success into a failure because cleanup failed.

Paths in findings and in exclusion rules are relative to the repository root
(`run.relative-path-mode: gitroot`), for example `agent-cli/internal/...`,
even though golangci-lint runs once per module directory.

## Size and complexity

These limits apply to production and test code alike, on every lint lane, with
no baseline and no exclusion. Each one is the tightest value every current
file passes on every lane (linux, windows, darwin, the opt-in test tags,
wireinject and darwin cgo), measured when the architecture gate's size rules
were retired. They only go down: lower a value when the code allows it, and
never raise one above the hard caps of 1,000 lines per file and 100 lines per
function. Split code by responsibility rather than suppressing a finding.

| Linter | Value | Findings at the next tighter values |
| --- | --- | --- |
| `revive` `file-length-limit` | 978 | 977: 2, 950: 12, 900: 27, 800: 53, 600: 101, 400: 344 |
| `funlen` `lines` | 100 | 99: 3, 95: 21, 90: 61, 80: 144 |
| `funlen` `statements` | 78 | 77: 1, 70: 3, 60: 13, 50: 56 |
| `gocyclo` | 30 | 29: 2, 25: 34, 20: 125, 15: 481 |
| `gocognit` | 30 | 29: 6, 25: 63, 20: 224, 15: 695 |
| `nestif` | 5 | 4: 51 |

golangci-lint cannot give tests looser limits than production code without an
exclusion, so one value covers both. Function literals count toward their
enclosing function. There is no files-per-package limit.

`gochecknoglobals` and `gochecknoinits` own package state: every package
variable and `init` function needs a specific `//nolint` with a reason, except
what `gochecknoglobals` allows (`Err*` sentinel errors, `regexp.MustCompile`
results, `version` and `//go:embed` variables).

## Boundaries: depguard and the architecture gate

`depguard` holds every import rule it can express: a list of files (globs
anchored at the repository root with `${config-path}`, optionally excluding
tests with `!$test`) and a deny list of import-path prefixes (exact with a
trailing `$`). The lists are:

| List | Files | Denied imports |
| --- | --- | --- |
| `reusable-modules` | go-agent-loop, go-audio, go-device-gateway, go-llm-gateway | go-agent-runtime, agent-cli |
| `runtime-module` | go-agent-runtime | agent-cli |
| `agent-loop` | go-agent-loop | go-device-gateway, go-llm-gateway |
| `agent-loop-production` | go-agent-loop, non-test | `encoding/binary`, the retired loop clock package |
| `go-audio` | go-audio | agent-cli, go-agent-loop, go-agent-runtime, go-device-gateway, go-llm-gateway |
| `gateway-functional-tests` | go-llm-gateway/test/functional | go-agent-loop except `pkg/messages` (`list-mode: lax`) |
| `session-contract` | agentsession/interface.go | go-device-gateway, go-llm-gateway |
| `device-contract` | devices/interface.go and the device list/probe transports | go-device-gateway |
| `agent-cli-production` | agent-cli/internal, non-test | agent-cli/internal/audio*, the retired wavio and loop clock packages |
| `agent-cli-binary` | agent-cli/internal, non-test, except webmcp/testkit | `encoding/binary` |

`TestDepguardImportRulesRejectViolations` in tools/architecturegate runs the
pinned depguard analyzer over these lists with a violating and an allowed
import for each.

`make architecture-check` (tools/architecturegate) keeps what depguard cannot
express: the service shape and contract rules, the service-boundary import
rules (they follow the gate's service classifier and composition registry),
public-surface leaks, the session-wrapper rule, `forbidden_source_patterns`,
generated-file registration, and the `forbidden_imports` rules whose import
pattern has a wildcard in the middle: `**/services/internal/**` (the tool and
device contracts, the device list/probe transports and every CLI transport),
`**/services/servicetest` and `go-agent-runtime/services/**/wire`. depguard
matches only import-path prefixes, so it could deny today's one
`agent-cli/internal/services/internal` prefix but not a private service tree
added elsewhere. See the
[gate README](../../tools/architecturegate/README.md).

## Round-3 cleanup (temporary exclusions)

The single policy replaced a two-pass setup (hard limits for all code plus a
`.golangci.new.yml` pass for changed lines only) and added linters that still
had findings. To land it without a flag day, `.golangci.yml` ends with
temporary `exclusions.rules`:

- one rule per module group, tagged `# round-3 cleanup: lint-<group> track`
  (`agent-cli`, `go-agent-runtime`, `loop-gateway` for go-agent-loop and
  go-llm-gateway, `media-tools` for go-audio, go-device-gateway and the
  tools, scripts and test modules). Each rule lists only the linters that
  had findings in that module when the policy landed;
- file-scoped rules tagged `# owned by <track>` for paths that a refactor
  track is rewriting.

These rules only shrink. A track removes linters from its rule as it fixes
them and deletes the rule when it is empty. Do not add a linter or a path to a
temporary rule; fix the finding or justify a specific `//nolint` instead.

## Build tags

The configuration sets `run.build-tags` to the opt-in test tags `e2e` and
`stress`. Files behind these tags are held to the same limits as default-tag
code even though ordinary `go test` never compiles them. A new opt-in test tag
must be added to it, and every tagged suite must run in a CI job.

`wireinject` cannot join that list: a Wire injector file replaces its package's
`!wireinject` files, so loading both sets would redeclare every injector.
`make lint-wireinject` (run by `make lint`) lints only the packages that hold a
`//go:build wireinject` file, with `--build-tags wireinject`. The generated `wire_gen.go` is excluded as generated code; `make
wire-check` keeps it in sync with the injectors.

`nomicrophone` selects the microphone stubs. Most stubs also build with cgo
disabled, so the cross-OS lanes below cover them. Files that build only with
the tag (for example `go-agent-loop/test/functional/sessions/session_harness_test.go`
and `go-device-gateway/pkg/devices/device_platform_nomicrophone_test.go`) are
covered by the darwin cross lane, which appends `nomicrophone`
(`LINT_CROSS_TAGS_darwin`). With cgo disabled on darwin that adds files and
removes none, because every `!nomicrophone` darwin file also requires cgo.

Test data directories are outside `./...`. Code that generates committed test
data belongs in a linted package; for example the room-audio replay fixtures
are produced by `go-agent-runtime/services/roomreplay/internal/roomaudiofixture`,
and `testdata/room-audio/generate.go` is only a thin entry point that keeps
the recorded regeneration command stable.

## Operating systems

| Lane | Command | Covers |
| --- | --- | --- |
| Linux (cgo on) | `make lint` | default and opt-in tags; linux cgo files; wireinject; other-OS stubs (`make lint-other-os`, below) |
| Windows cross | `make lint-cross LINT_CROSS_GOOS=windows` | `GOOS=windows CGO_ENABLED=0` (WASAPI, Win32 syscalls) |
| Darwin cross | `make lint-cross LINT_CROSS_GOOS=darwin` | `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0` plus `nomicrophone` (darwin files without cgo, and cgo and microphone stubs) |
| Darwin cgo | `make lint-darwin-cgo` (macOS only) | cgo on, for packages holding a cgo-constrained file (CoreAudio capture, display permission) |

The darwin lane sets `GOARCH=arm64` (`LINT_CROSS_GOARCH_darwin`) on every
host, so any file constrained to `darwin && arm64` is linted; no file is
constrained to `darwin && amd64`.

`make lint-other-os` (run by `make lint`) lints, as `GOOS=js GOARCH=wasm`
with cgo disabled, only the packages holding a file whose build constraint
excludes linux, darwin and windows (the unsupported-platform stubs). No
other lane builds those files.

`make lint-cross` runs both cross lanes by default. Cross-linting needs no C
toolchain because cgo is disabled. Darwin cgo files need the macOS SDK, so
`make lint-darwin-cgo` refuses to run elsewhere. It selects packages by their
`//go:build ... cgo` constraints, so a new cgo package is picked up without
configuration changes.

`LINT_SHARD` limits `make lint` and `make lint-cross` to `agent-cli`, to
`libraries` (every other lint module), or to one half of the libraries:
`runtime` (`go-agent-runtime`, `go-agent-loop`, `go-audio`, `tests/embedding`)
or `support` (the gateways, tools and scripts).
`make lint` and `make lint-cross` lint `LINT_JOBS` modules at once (default
4). Each module's output prints as one block when that module finishes.

Every lint entry point (`make lint`, `make lint-cross`, `make
lint-darwin-cgo`) first loads the configuration with `golangci-lint
linters`, including on `main` pushes. This rejects unknown linters and
configurations that fail to load, and works offline. `golangci-lint config
verify` is not used because it downloads its JSON schema from GitHub. Unknown
keys under a linter's settings are therefore not rejected, as before.

## go vet and staticcheck

There is no separate `go vet` or standalone `staticcheck` step, in CI or in
the Makefile. golangci-lint's `standard` set covers both on every lane, OS and
build tag above:

- `govet` runs the same analyzers as `go vet` in Go 1.26. It skips
  `loopclosure` for Go 1.22+ modules, where `go vet`'s `loopclosure` reports
  nothing.
- `staticcheck` runs every SA, S, ST and QF check except ST1000, ST1003,
  ST1016, ST1020, ST1021 and ST1022. Standalone staticcheck 2026.1 runs the same
  checks except SA9003 and ST1023, and never runs QF checks. golangci-lint's
  set is therefore a superset. golangci-lint v2.9.0 bundles staticcheck
  2025.1.1, which has the same 149 checks as 2026.1.
- `unused` is staticcheck's U1000.

A fixture with deliberate `go vet` and staticcheck violations gave the same 8
`go vet` findings and all 18 standalone staticcheck findings under
`.golangci.yml`, plus SA9003 and QF1003.

## Adding a linter

1. Measure its repository-wide count on `GOOS=linux`, `darwin` and `windows`,
   with the configured build tags, in a `wireinject` pass and with
   `make lint-darwin-cgo`.
2. Fix every finding.
3. Enable the linter in `.golangci.yml` in the same change.

## Run locally

```sh
make lint             # Linux/host: default + opt-in tags, wireinject
make lint-cross       # GOOS=windows and GOOS=darwin, cgo disabled
make lint-darwin-cgo  # macOS only: cgo-constrained packages
make lint-module LINT_MODULE=agent-cli   # one module
# or directly:
scripts/golangci-lint-module.sh --analyzer <pinned golangci-lint> \
  --config .golangci.yml --module agent-cli --repo . -- ./...
```

Findings depend on the target platform. When you change platform-tagged files,
run `make lint-cross` as well as `make lint`, and run `make lint-darwin-cgo` on
macOS when you change cgo files. The `golangci-lint` on PATH may be a
different version, so use the Makefile resolver.

## CI lanes

Every CI job must finish within three minutes even with a cold build cache
(target: 2.5 minutes), so the lanes are sized for a cold run (80-140s each):

| Lane | Command |
| --- | --- |
| `CI (static lint linux agent-cli)` | `make lint LINT_SHARD=agent-cli` |
| `CI (static lint linux runtime)` | `make lint LINT_SHARD=runtime` |
| `CI (static lint linux support)` | `make lint LINT_SHARD=support` |
| `CI (static lint windows agent-cli)` | `make lint-cross LINT_CROSS_GOOS=windows LINT_SHARD=agent-cli` |
| `CI (static lint windows runtime)` | `make lint-cross LINT_CROSS_GOOS=windows LINT_SHARD=runtime` |
| `CI (static lint windows support)` | `make lint-cross LINT_CROSS_GOOS=windows LINT_SHARD=support` |
| `CI (static lint darwin agent-cli)` | `make lint-cross LINT_CROSS_GOOS=darwin LINT_SHARD=agent-cli` |
| `CI (static lint darwin runtime)` | `make lint-cross LINT_CROSS_GOOS=darwin LINT_SHARD=runtime` |
| `CI (static lint darwin support)` | `make lint-cross LINT_CROSS_GOOS=darwin LINT_SHARD=support` |

`make lint-darwin-cgo` needs the macOS SDK, so it runs in the required
`CI (WebMCP Chrome)` job, which already runs on macOS (124-141s cold for
both in real CI runs), rather than on a macOS lane of its own. The job caches
the golangci-lint directory through `extra-paths`, which changed its cache
version: the first run after that change restored nothing and ran cold.

Merged lanes were measured cold (empty Go build, module and golangci-lint
caches) in #591, #601 and earlier and do not fit: `make lint
LINT_SHARD=libraries` 131-223s, `make lint LINT_SHARD=all` 243-250s, one lane
per operating system 176-243s, `make lint-cross LINT_CROSS_GOOS=windows
LINT_SHARD=libraries` 142-179s and the darwin equivalent 127-177s, `make
lint-cross LINT_CROSS_GOOS="windows darwin"` 158-173s for
`LINT_SHARD=agent-cli` and 144-192s for `runtime` or `support`, linux
agent-cli plus support 174s, and `CI (static)`'s own checks plus any linux lane
163-170s. A lane's cold time is roughly the compile and analysis CPU of its
modules on the 4-vCPU runner and varies by up to a third between runs, so a
merge needs a wide margin. Warm, every one of them takes under 60s, so the
lanes can merge once cold compiles shrink: they are one matrix in
`.github/workflows/ci.yml`, and merging two lanes is a change to one entry's
`LINT_SHARD` or `LINT_CROSS_GOOS` (for example `LINT_CROSS_GOOS="windows
darwin"`) plus the lane count in `CI (static)`'s `--min-matches` (list the
replaced lanes' caches in go-cache's `fallback-cache-names` so the merged
lane's first run is warm).

The required `CI (static)` check runs `make fmt`, `make
check-ci-test-partition` and `make architecture-check` (75-100s cold before
the size rules moved here;
`make wire-check`, another 40-55s cold, runs in `CI (unit)`), then
waits for every job named `CI (static lint *` and fails if any of them failed
or fewer than `--min-matches` lanes exist (`scripts/ci-await-jobs.sh`), so
adding, removing or merging a lane needs only that count updated. Every lane
runs on ubuntu, so the wait never sits behind the macOS runner queue; the
waiter gives up after 25 minutes (`--timeout`), inside the job's 30-minute
limit. `make lint` and `make lint-cross` start the modules with the
longest cold lint first (`LINT_SCHEDULE`).

Each lane restores its own Go build, module and golangci-lint caches through
`.github/actions/go-cache` (the golangci-lint cache is passed as
`extra-paths`). golangci-lint loads dependencies from compiled export data, so
a cold lane spends most of its time compiling, not analyzing.

