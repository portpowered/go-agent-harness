# Go maintainability gates and legacy debt

The six product modules are listed in the root Makefile. The maintained module
inventory, service ownership rules, generated-file registry, and stronger size
budgets are declared in [architecture-policy.json](architecture-policy.json).
The architecture tool also covers its own implementation and the independent
runtime consumer module. Test files and inactive platform source files are
included in its physical source inventory.

The older root `.golangci.yml` remains an additional production-only ceiling.
The Makefile pins golangci-lint v2.9.0 and Staticcheck 2026.1; it resolves and
checks those versions before running them. Do not infer current maximum holders
from historical filenames: extraction changes both owners and measurements.

| Gate | Production budget | Test budget |
| --- | ---: | ---: |
| Handwritten Go files per package directory | 15, including tests and platform variants | Same combined count |
| Physical file lines | 400 | 600 |
| Function lines | 80 | 120 |
| Function statements | 50 | 80 |
| Cognitive complexity | 15 | 20 |
| Cyclomatic complexity | 15 | 20 |

Physical line counts include comments and blanks. Function literals are measured
independently, and enclosing functions retain their nested-body costs. Cognitive
and cyclomatic scores use the pinned libraries in `tools/architecturegate/go.mod`.
Only registered, recognized generated output is excluded. These budgets do not
establish correctness; behavioral, race, replay, and platform checks remain required.

The retained golangci-lint limits are 1,307 physical file lines, 296 function
lines, and 124 cognitive-complexity points. They do not override the stronger
architecture gate or permit new code to grow to those ceilings.

An availability check against the repository-cached v2.9.0 binary confirmed
`errcheck`, `bodyclose`, `contextcheck`, `durationcheck`, `errorlint`, `exhaustive`,
`goconst`, `mnd`, `nilerr`, and `nolintlint`. Availability is separate from
enforcement: the root configuration still enables the three legacy linters.
The runtime plan tracks staged configuration and no-new-debt enforcement for
additional checks. The `golangci-lint` executable on PATH may be older; use the
Makefile resolver rather than assuming PATH matches the repository pin.

## Exact baseline and ratchet

[architecture-size-baseline.json](architecture-size-baseline.json) records exact
pre-existing size and architecture debt. Each entry identifies its rule, module,
package, file or symbol, measured ceiling or diagnostic, rationale, and migration
phase. The initial baseline is validated against the source at the merge base;
it is not permission to accept the current checkout's violations.

Run `make architecture-check` and `make size-check`. Both compare their baseline
lane with `ARCHITECTURE_BASE` (default `origin/main`). New violations and growth
fail. Resolved entries must be removed, reduced measurements must lower their
ceilings, and stale exemptions fail. Explicit one-to-one rename mappings cannot
multiply debt or raise its ceiling. New runtime packages receive no copied-code
exemptions.

`make test-architecture-gate` verifies the rules and baseline behavior with
positive and negative fixtures. `make verify-architecture` adds generated Wire
drift checking. The baseline and full repository gates are still being reconciled
during the [runtime refactor](embeddable-agent-runtime-refactor-plan.md); passing
tool fixtures alone does not mean the repository satisfies the policy.

Do not raise global limits, add broad path exemptions, or suppress findings to
preserve an oversized implementation. Decompose by responsibility while retaining
the relevant tests and public behavior.
