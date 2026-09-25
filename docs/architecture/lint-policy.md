# golangci-lint policy

`make lint` runs the pinned golangci-lint (v2.9.0, through the Makefile's
analyzer resolver) twice for every `LINT_MODULES` module, then once more for
Wire injector packages. Production and test files are both checked, including
files behind opt-in build tags and platform-specific files (see
[Build tags](#build-tags) and [Operating systems](#operating-systems)). These
limits are separate from the stricter [architecture gate budgets](size-baselines.md)
and do not replace them.

## Hard pass: all code

[`.golangci.yml`](../../.golangci.yml) runs with `--all-code`. It has no
`new-from-rev` filter and no baseline, so any finding in any file fails lint.

| Check | Limit |
| --- | --- |
| `linters.default: standard` | `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused` |
| `errcheck` | also checks blank assignments and type assertions; only Win32 `LazyProc.Call`, `Proc.Call` and `SyscallN` are excluded |
| `revive` `file-length-limit` | 1,000 physical lines per file |
| `funlen` | 100 lines per function (statements are not counted) |
| `gocyclo`, `gocognit` | 30 |
| Also enforced | `goconst`, `nilerr`, `bodyclose`, `durationcheck`, `gochecknoinits`, `nolintlint` |

Every finding is reported (`uniq-by-line: false`). A `//nolint` directive must
name one linter and give a reason. An unused directive is itself a finding.

Some errors are safe to discard, for example a `Close` failure after a
successful read. Discard them with a documented helper instead of a blank
assignment. Do not turn a success into a failure because cleanup failed.

## New-code pass: changed code only

[`.golangci.new.yml`](../../.golangci.new.yml) runs with
`--new-from-rev $(LINT_BASE)` (default `origin/main`). It covers linters that
still have legacy findings: `errorlint`, `contextcheck`, `mnd`, `exhaustive`,
`gochecknoglobals` and `forbidigo`. New and modified lines must be clean.

## Build tags

Both configurations set `run.build-tags` to the opt-in test tags `live`, `e2e`,
`e2e_internal`, `stress` and `sessioncapacityramp`. Files behind these tags are
held to the same limits as default-tag code even though ordinary `go test`
never compiles them. The tags load together because `e2e_internal` files use
helpers defined in `live` files. A new opt-in test tag must be added to both
configurations.

`wireinject` cannot join that list: a Wire injector file replaces its package's
`!wireinject` files, so loading both sets would redeclare every injector.
`make lint-wireinject` (run by `make lint`) lints only the packages that hold a
`//go:build wireinject` file, with `--build-tags wireinject`, through both
passes. The generated `wire_gen.go` is excluded as generated code; `make
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
| Linux (cgo on) | `make lint` | default and opt-in tags; linux cgo files; new-code pass; wireinject |
| Windows cross | `make lint-cross LINT_CROSS_GOOS=windows` | `GOOS=windows CGO_ENABLED=0`, hard pass (WASAPI, Win32 syscalls) |
| Darwin cross | `make lint-cross LINT_CROSS_GOOS=darwin` | `GOOS=darwin CGO_ENABLED=0` plus `nomicrophone`, hard pass (darwin files without cgo, cgo and microphone stubs) |
| Darwin cgo | `make lint-darwin-cgo` (macOS only) | hard pass with cgo on, for packages holding a cgo-constrained file (CoreAudio capture, display permission) |

`make lint-cross` runs both cross lanes by default. Cross-linting needs no C
toolchain because cgo is disabled. Darwin cgo files need the macOS SDK, so
`make lint-darwin-cgo` refuses to run elsewhere. It selects packages by their
`//go:build ... cgo` constraints, so a new cgo package is picked up without
configuration changes. The cross lanes run only the hard pass. The new-code
pass runs on Linux.

`LINT_SHARD=agent-cli|libraries` limits `make lint` and `make lint-cross` to
agent-cli or to every other lint module. CI uses it to keep each lane short.
`make lint` and `make lint-cross` lint `LINT_JOBS` modules at once (default
4). Each module's output prints as one block when that module finishes.

The new-code pass skips a module that has no Go file in `git diff
$(LINT_BASE)`, untracked files included. `--new-from-rev` reports only issues
on lines in that diff, so such a module cannot report anything. On a `main`
push, where `origin/main` is the checked-out commit, the pass has nothing to
check.

## go vet and staticcheck

CI does not run `go vet` or the standalone `staticcheck`. `make vet` and
`make staticcheck` remain for local use. golangci-lint's `standard` set covers
both on every lane, OS and build tag above:

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

## Promoting a linter to the hard pass

1. Measure its repository-wide count without `new-from-rev` on `GOOS=linux`,
   `darwin` and `windows`, with the configured build tags and in a
   `wireinject` pass.
2. Fix every finding.
3. Move the linter and its settings from `.golangci.new.yml` to `.golangci.yml`
   in the same change.

## Run locally

```sh
make lint             # Linux/host: default + opt-in tags, new-code pass, wireinject
make lint-cross       # GOOS=windows and GOOS=darwin, cgo disabled
make lint-darwin-cgo  # macOS only: cgo-constrained packages
# One module, hard pass only:
scripts/golangci-lint-working-tree.sh --analyzer <pinned golangci-lint> \
  --all-code --config .golangci.yml --module agent-cli --repo . -- ./...
# One module, new-code pass only:
scripts/golangci-lint-working-tree.sh --analyzer <pinned golangci-lint> \
  --config .golangci.new.yml --base origin/main --module agent-cli --repo . -- ./...
```

Findings depend on the target platform. When you change platform-tagged files,
run `make lint-cross` as well as `make lint`, and run `make lint-darwin-cgo` on
macOS when you change cgo files. CI runs every lane: `CI (static lint
agent-cli)`, `CI (static lint libraries)`, `CI (static lint windows)`,
`CI (static lint darwin)` and `CI (static lint darwin cgo)`. All of them are
aggregated under the required `CI (static)` check. The `golangci-lint` on PATH
may be a different version, so use the Makefile resolver.

Each CI lane keeps its own Go build cache and golangci-lint cache in one
`actions/cache` entry. golangci-lint loads dependencies from compiled export
data, so a cold lane spends most of its time compiling, not analyzing. `main`
pushes prune the build cache to the entries the run used
(`scripts/prune-go-build-cache.sh`) and save it. Pull requests restore the
newest `main` entry. They save only when nothing could be restored.
