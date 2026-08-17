# Testing tiers

This repository uses explicit test tiers so local checks, pull-request CI, and
hardware/provider acceptance work have predictable boundaries.

| Tier | Exact command | Where it runs | Run policy |
| --- | --- | --- | --- |
| T0 unit | `make test` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T0 functional | `make test-integration` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T0 hermetic | `make test-hermetic` | Contributor machines and the hermetic Linux CI leg; this is the local Windows command | Every PR and local Windows validation |
| T1 probe-replay | `make test-regressions` | Contributor machines and the ordinary Linux CI leg | Every PR |
| T2 probe-live | `agent probe run <scenario> --transport live` | A per-vertical acceptance environment with live services | Per-vertical acceptance; never PR CI |
| T2 device | `agent probe run <scenario> --devices real` | An acceptance host with the required real audio device | Per-vertical acceptance; never PR CI |
| T3 fleet + fault | `agent probe fleet` | An operator-controlled fleet environment | On demand |

The combined T0 unit, T0 functional, T0 hermetic, and T1 probe-replay work has
a 60-second budget for every PR. T2 live/device checks and T3 fleet/fault
checks are deliberately outside that PR budget.

## Microphone build configurations

The ordinary Linux CI leg runs `make ci` without a `nomicrophone` tag, so its Go
tests compile the CGO/malgo microphone implementation. The separate hermetic
Linux leg runs `make test-hermetic`; that target invokes each workspace module
with `CGO_ENABLED=0` and `-tags=nomicrophone`. The target prints the module and
the hermetic environment before each invocation, making a failed module
identifiable in local and CI output.

On Windows, run `make test-hermetic` from the repository root to exercise the
complete no-CGO/no-microphone suite without depending on a C compiler or a
microphone device.

## Scope and evidence

T0 and T1 checks are deterministic and suitable for pull-request gating. T2
and T3 checks may require credentials, live services, devices, or fleet
access; their results belong to the corresponding acceptance or operations
workflow rather than the default PR pipeline.
