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

This 180-second package budget is distinct from the GitHub job's 45-minute
outer limit and must remain finite. It does not replace command-level
deadlines inside the integration tests, and the general `GO_TEST_TIMEOUT`
continues to govern unrelated packages until a root target explicitly opts
into the integration-specific setting. Full repetition logs, JSON inventories,
and CI status belong in review conversation/run artifacts, not in the
repository.

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
