# Local validation: `make prepush`

`make prepush` is the gate to run before every push. It runs the checks CI
enforces, each once, in three fail-fast stages:

| Stage | Phases (run concurrently, at most `PREPUSH_JOBS`) |
| --- | --- |
| format | `fmt` |
| static | `lint` (its golangci config enables govet and staticcheck, so `make vet` and `make staticcheck` stay manual-only), `verify-architecture`, `build BUILD_LIBRARY_PACKAGES=0` (links the binaries; lint already type-checks every package), `coverage-registration`, `check-ci-test-partition`, `verify-standalone-checkout`, and `test-factory-scripts` when `factory/` or the `Makefile` changed (always in the full scope) |
| tests | `coverage COVERAGE_SCOPE=<scope>`, `test-cgo-delta`, `test-tools` (it runs a real golangci-lint, whose machine-wide lock must not overlap `lint`) |

A failed stage stops the gate; the phases already running in that stage finish
and their output is printed as one block per phase with its time.

A phase that passed for exactly the same inputs is reported as cached and
skipped, so re-running the gate after a flaky or unrelated failure repeats only
what has not passed (`PREPUSH_CACHE=0` turns this off; results live in
`.cache/prepush/`). The key covers the working tree including untracked files
(hashed through a private index and object directory, so nothing is written to
the repository), the `COVERAGE_BASE` commit, the `go` binary make uses with its
version and `go env` (GOOS, GOARCH, GOFLAGS, CGO_ENABLED, ...), Python, the
platform, `MAKEFLAGS` (command-line Make variables such as the pinned analyzer
versions) and the `GO*`, `CGO_*`, `CI`, `COVERAGE_*`, `LINT_*`, `TEST_*`,
`AGENT_CLI_*`, `SKIP_*`, `BUILD_*` environment variables. A stage's passes are
recorded only if the key is unchanged when the stage ends, so a result is
never cached for content edited while it ran.

## One test pass

The Go tests run once, in the hermetic coverage pass (`CGO_ENABLED=0`,
`-tags=nomicrophone`, the configuration CI tests and gates in). The same run
proves the tests pass and produces the profiles for the coverage gate, so the
gate no longer runs a separate `make test` pass first. `test-cgo-delta` then
runs natively (cgo, the real microphone backend) only the packages whose files
differ between the two builds (today: `go-device-gateway/pkg/devices`, the
darwin display-permission packages, `agent-cli/test/functional` and
`go-agent-loop/test/functional/sessions`); it computes that list with
`go list`, so it follows new build-constrained files automatically.
`embed-check` is not a separate phase because the coverage pass runs the same
`tests/embedding` corpus, and `test-tools` skips the architecture fixtures that
`verify-architecture` already ran.

## Scope: changed (default) or full

`PREPUSH_SCOPE=changed` (the default) tests only what the diff can affect:

- The changed files are the commits since the merge base with `COVERAGE_BASE`
  (default `origin/main`) plus staged, unstaged and untracked files. Each maps
  to the package in its nearest enclosing package directory (so `testdata/`
  and embedded files count); a coverage-manifest fragment maps to its package.
- Every test package whose test binary links a changed package runs, across
  all modules and the `tests/embedding` consumer, with exactly the flags of the
  full run (so both scopes share Go's test cache).
- The coverage gate (`coveragegate --select`) checks the floors of the changed
  packages and of every package importing one. Those floors are exact: any test
  that covers such a package links the changed package and therefore ran.
  Partial profiles go to `coverage/changed/`, never mixed with full ones.
- A change to `go.mod`/`go.sum`/`go.work`, the `Makefile`, `scripts/prepush.sh`,
  `scripts/go-test-shards.sh`, `scripts/run-bounded.sh` or `tools/coveragegate/`,
  or a non-Markdown file inside a module but outside every package, falls back
  to the full scope.
- So does a changed data file (anything other than a package's own Go source,
  e.g. `testdata/`, fixtures, generators under `testdata/`) that a Go file of
  another package names by path: tests that read another package's fixtures
  (`../../wire/testdata/room-audio`, `filepath.Join(root, "go-agent-loop",
  "testdata", "audio")`) do not link its owner, so the dependency closure
  cannot see them. `coveragegate --affected` finds these by resolving every
  string literal and literal `Join` in the modules' Go files relative to the
  file, the repository and each module, and as a multi-segment path suffix.

`make prepush-full` (or `PREPUSH_SCOPE=full`) runs every package like CI. The
scope selection alone is `make coverage-changed` (or
`make coverage COVERAGE_SCOPE=changed`).

## Knobs

| Variable | Default | Effect |
| --- | --- | --- |
| `PREPUSH_SCOPE` | `changed` | `changed` or `full` test and coverage scope |
| `PREPUSH_JOBS` | `4` | phases of one stage run at once; `1` runs them serially with live output |
| `COVERAGE_BASE` | `origin/main` | comparison base for the changed scope |
| `GO_TEST_COUNT` (`COVERAGE_COUNT`) | empty (locally and in CI) | set `1` to bypass Go's test cache and re-run every test |
| `PREPUSH_CACHE` | `1` | skip phases that passed for identical content |
| `PREPUSH_FACTORY_SCRIPTS` | `auto` | `always`/`never` overrides when factory script tests run |
| `TEST_MODULE_JOBS` | `3` | modules the coverage pass runs at once |

Local runs reuse Go's build and test caches (`GOCACHE` is shared by every
worktree of a user; test results are keyed by the worktree path, so a fresh
worktree re-runs its tests once). Keep `TMPDIR` stable between runs: tests
that read it are cached against its value. The agent-cli integration package
runs as sharded processes of one test binary and is never cached, which is why
the changed scope matters most when a diff does not reach it. Packages listed
in `scripts/go-test-fresh-packages.txt` read inputs Go's test cache cannot
see (files in another module, a `go list` subprocess, a host tool), so they
always run with `-count=1`. Cached runs go through
`scripts/go-test-input-guard.py`, which fails an unlisted package that reads
such inputs and names the file or command; `make coverage GO_TEST_COUNT=1` re-runs
everything. CI uses the same test cache, restored with each job's build cache (see
[workspace.md](architecture/workspace.md#github-actions)).

## Differences from CI

- The changed scope does not re-check the floor of a package the diff does not
  touch or import, even when a changed caller stopped exercising it; the full
  scope and CI do.
- Cross-package data references are found only when the path is a string
  literal, a constant, or a literal `filepath.Join`/`path.Join`. A test that
  builds another package's fixture path at runtime (from variables or
  `fmt.Sprintf`) is not detected, so changing that fixture does not widen the
  changed scope; `make prepush-full` and CI still run it. Prefer literal paths
  for shared fixtures.
- CI-only jobs stay CI-only: the race corpus (`test-rtc-race`,
  `test-audio-stability-race`, `test-sessions-race`), `lint-cross`,
  `lint-darwin-cgo`, the WebMCP Chrome and macOS audio release jobs.
- Neither the prepush gate nor pull-request CI runs the fresh-process
  Test45/Test46 audio stress trials (`YUI_AUDIO_STRESS=1`). The scheduled
  Nightly audio stress workflow runs them on main and opens a "Nightly audio
  stress failed" issue when they fail; run them locally with
  `make test-audio-stress AUDIO_STRESS_COUNT=N`.
- `test-cgo-delta` runs the native build of the build-constrained packages;
  packages that merely import them are tested against the microphone stub, as
  in CI.
