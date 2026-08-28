# Testing tiers

This repository uses explicit test tiers so local checks, pull-request CI, and
hardware/provider acceptance work have predictable boundaries.

| Tier | Exact command | Where it runs | Run policy |
| --- | --- | --- | --- |
| T0 unit | `make test` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T0 functional | `make test-integration` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T0 hermetic | `make test-hermetic` | Contributor machines and the hermetic Linux CI leg; also the command for any host without a working CGO toolchain | Every PR and local validation on such hosts |
| T1 probe-replay | `make test-regressions` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T2 probe-live | `agent probe run <scenario> --transport live` | A per-vertical acceptance environment with live services | Per-vertical acceptance; never PR CI |
| T2 device | `agent probe run <scenario> --devices real` | An acceptance host with the required real audio device | Per-vertical acceptance; never PR CI |
| T3 fleet + fault | `agent probe fleet` | An operator-controlled fleet environment | On demand |

T0 unit, T0 functional, T0 hermetic, and T1 probe-replay checks are
pull-request gates with the package and target budgets documented below. T2
live/device checks and T3 fleet/fault checks are deliberately outside those
pull-request gates.

## Credential-free agent-cli integration budget

`agent-cli/test/integration` is a complete package-level integration suite. Its
`TestMain` builds one shipped `agent` binary and the tests then exercise that
binary, replay fixtures, and test-owned child processes. The package therefore
needs its own finite budget rather than inheriting the general package timeout.

The selected default for the root-target contract is **180 seconds**. The
budget is based on the package's Go test elapsed time, which is the scope owned
by `go test -timeout` and includes the shared `TestMain` setup. The fresh-main
baseline at `66c3ff8` reached a maximum successful cold CI-shaped package
duration of 110.788 seconds; the required safety calculation is
`110.788 × 1.5 = 166.182 seconds`, rounded up to 180 seconds. The outer shell
wall time includes compilation; it is measured separately when collecting
evidence and does not change the package-level calculation.

The root `Makefile` exposes this contract as
`AGENT_CLI_INTEGRATION_TIMEOUT`, whose default is the finite `180s` value and
whose effective value is printed by every affected target. The direct package
paths in `make test-integration`, `make test-regressions`, and `make test-budget`
use this setting for `agent-cli/test/integration`; `make coverage` and
`make test-hermetic` use it for the same package under their existing coverage
and `CGO_ENABLED=0`/`nomicrophone` modes. `make test` and those target-wide
module invocations necessarily apply it to all packages in the `agent-cli`
module, which is the narrowly scoped module-level consequence of one
`go test ./...` invocation. Other modules and package paths retain the general
`GO_TEST_TIMEOUT`. The composite `make ci` inherits these same settings from
the targets it composes.

The integration budget is distinct from the GitHub job's 45-minute limit and
must remain finite. It does not replace command-level deadlines inside the
integration tests. Override `AGENT_CLI_INTEGRATION_TIMEOUT` only for local
diagnostics, keeping the value finite when exercising the root contract. Full
repetition logs, JSON inventories, and CI status belong in review
conversation/run artifacts, not in the repository.

Every root target that executes the `agent-cli` module uses
`agent-cli/cmd/testtimeout` as the outer termination boundary. The runner
passes through the target's existing `go test -timeout` and applies the same
finite value to its own process-group watchdog. On timeout it terminates the
whole command group, including test-owned descendants, then returns a
non-zero result with the command PID and cleanup diagnostic. This protects the
budget from a blocked child keeping Go test output pipes open; it does not
change the integration tests' command-level deadlines or selection.

The runner's focused cleanup contract lives in
`agent-cli/internal/testtimeout`. Its intentionally blocking fixture is under
`internal/testtimeout/testdata/blockedchild`, which Go excludes from ordinary
`./...` package discovery. The contract invokes that fixture explicitly with
a short two-second test-only watchdog (with startup headroom for cold or
contended workers), checks the active test and child identities, and verifies
that the child and grandchild no longer run. The success control uses the same
runner and reports its executed fixture test.

## Microphone build configurations

The ordinary Linux CI leg runs `make ci` without a `nomicrophone` tag, so its Go
tests compile the CGO/malgo microphone implementation. The separate hermetic
Linux leg runs `make test-hermetic`; that target invokes each workspace module
with `CGO_ENABLED=0` and `-tags=nomicrophone`. The target prints the module and
the hermetic environment before each invocation, making a failed module
identifiable in local and CI output.

On any host without a working CGO toolchain — for example Windows, or a
darwin/arm64 machine with no configured C toolchain — run `make test-hermetic`
from the repository root to exercise the complete no-CGO/no-microphone suite
without depending on a C compiler or a microphone device.

## Scope and evidence

T0 and T1 checks are deterministic and suitable for pull-request gating. T2
and T3 checks may require credentials, live services, devices, or fleet
access; their results belong to the corresponding acceptance or operations
workflow rather than the default PR pipeline.
