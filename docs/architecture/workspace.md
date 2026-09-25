# Go workspace layout

This repository is a multi-module Go workspace. A root `go.work` file is committed so contributors can build and test all libraries from the repository root without publishing pseudo-versions or guessing local module paths.

## Why `go.work` is committed

- **Local cross-module edits**: `agent-cli` depends on `go-agent-loop` and `go-llm-gateway`. A workspace lets those dependencies resolve to the sibling directories in this checkout.
- **Single validation surface**: Root tooling (`make ci`, future CI) can run `go` commands from the repository root and see every module.
- **One CI contract**: GitHub Actions delegates to the same root Makefile targets contributors run locally (individually, or together via `make ci`), so workflow YAML does not drift from module validation behavior.
- **Reproducible onboarding**: New contributors clone once, run `go work sync` if needed, and work from the root with no manual module-path setup.

`AGENTS.md` describes this layout; the committed `go.work` is the canonical expression of that decision.

## Modules in the workspace

| Directory        | Module path                          | Role                                      |
|------------------|--------------------------------------|-------------------------------------------|
| `./agent-cli`    | `github.com/portpowered/go-agent-harness/agent-cli`   | CLI for exercising the agent loop and gateway |
| `./go-agent-loop`| `github.com/portpowered/go-agent-harness/go-agent-loop` | Tick-based agent execution harness      |
| `./go-llm-gateway` | `github.com/portpowered/go-agent-harness/go-llm-gateway` | Provider-agnostic LLM gateway        |

The root `go.work` lists these three paths in `use` directives. Other top-level trees (for example `tests/`, `factory/`, `docs/`) are not workspace modules unless explicitly added later.

## Minimum Go version

All workspace modules require **Go 1.24** or newer. Individual `go.mod` files may specify patch-level minimums (for example `go 1.24.2` in `agent-cli`) and optional `toolchain` lines. The root `go.work` `go` directive is set to a version compatible with every module’s `go` line.

Install Go 1.24+ before working in this repo. Run `go version` from the repository root to confirm.

## Relationship to Workspace Replaces

Released module `go.mod` files do not declare local `replace` directives.
Instead, the root `go.work` file owns local development resolution for the
workspace's first public module version:

- `github.com/portpowered/go-agent-harness/go-agent-loop v0.0.1` resolves to
  `./go-agent-loop`
- `github.com/portpowered/go-agent-harness/go-llm-gateway v0.0.1` resolves to
  `./go-llm-gateway`

When you are **inside the workspace** (repository root with `go.work` present,
or `GOWORK` pointing at it), the workspace `use` entries and version-specific
workspace replaces take precedence for local resolution. Published consumers do
not see `go.work`; they resolve the module-prefixed git tags instead.

The first public release uses these module tags:

```text
agent-cli/v0.0.1
go-agent-loop/v0.0.1
go-llm-gateway/v0.0.1
```

## Common commands

From the repository root:

```bash
go work sync   # sync workspace sum file with module dependencies

# List or build every package in all workspace modules. The repository root is
# not itself a package-bearing module, so use module-qualified ./... patterns
# from the root instead of a bare `go list ./...`:
go list ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
go build ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
go test ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
```

From a single module directory (for example `agent-cli/`), `go list ./...` and `go test ./...` apply to that module only. Go still loads the parent `go.work` automatically when present.

A bare `go list ./...` from the repository root is not a valid cross-module workspace contract in this layout: the pattern is evaluated relative to the root directory, which is not one of the workspace modules. Root automation should either invoke per-module commands or use module-qualified patterns from the root, which is what the repository `Makefile` does.

## Test tiers and credentials

Default `go test ./...` and root `make test` / `make ci` targets are intended to run **without live provider API keys**.

The root Makefile exposes these deterministic and opt-in test tiers:

| Target | Deterministic | What it runs |
|--------|---------------|--------------|
| `make test` | yes | Per-module `go test ./...` across `agent-cli`, `go-agent-loop`, and `go-llm-gateway`. |
| `make test-rtc-race` | yes, supported Linux only | Focused `go-llm-gateway/pkg/transport/rtc` S8 cases with CGO, the race detector, `nomicrophone`, fresh execution, a finite timeout, and fail-closed JSON event verification. |
| `make test-factory-scripts` | yes | Factory runtime coverage for `factory/scripts/setup-workspace.py`, executed with bytecode writes disabled so the root checkout stays clean. |
| `make test-integration` | yes | Deterministic integration packages: `agent-cli/test/integration` and `go-agent-loop/test/functional`. |
| `make test-regressions` | yes | Committed replay and fixture regression tests, including Agent CLI replay cases and `go-llm-gateway` replay/fixture packages. |
| `make test-customer-sessions` | no | Local-only placeholder for future private session sweeps. It skips unless you explicitly set `RUN_CUSTOMER_SESSIONS=1`, and it is not part of `make ci`. |

`make test-integration` and `make test-regressions` must complete without live provider credentials. Tests that eventually need real inference credentials or private customer session data stay opt-in and out of the default CI path.

## GitHub Actions

Repository CI lives in `.github/workflows/ci.yml`. It runs on pull requests
and pushes to `main`, and every job starts at once (no `needs:`):

| Job | Runs |
| --- | --- |
| `CI (static)` (required) | `fmt`, `check-ci-test-partition`, `architecture-size-check`, then waits for every lint lane |
| `CI (static lint …)` | ten golangci-lint lanes (linux, windows and darwin by agent-cli, runtime and support, plus darwin cgo on macOS); see [lint-policy.md](lint-policy.md) |
| `CI (unit)` (required) | `test-tools`, `test-factory-scripts`, the agent-cli build, the standalone-checkout check, `wire-check` |
| `CI (coverage agent-cli unit)` | agent-cli coverage without `test/integration` |
| `CI (coverage libraries)` | every other module's and the embedding consumer's coverage |
| `CI (coverage)` (required) | agent-cli `test/integration` (sharded on one runner), then the coverage gate over every profile |
| `CI (race)` (required) | the race-detector targets and the native Linux device backend |
| `CI (macOS audio release)` (required) | macOS audio units, darwin release cross-build and GoReleaser check |
| `CI (WebMCP Chrome)` (required) | the pinned mac-arm64 Chrome cancellation acceptance |
| `CI (Windows audio portable)` | the Windows device backend regressions |

Each job must finish within three minutes with a cold build cache, which is
what bounds merging jobs further: the two non-integration coverage jobs merged
into one took 143-183s cold, and `CI (coverage)`, which waits for it,
183-194s. A new or renamed job lists the jobs it replaced in `go-cache`'s
`fallback-cache-names`, so only a toolchain bump leaves it truly cold. A
required check that aggregates other jobs (`CI (static)` over the lint lanes,
`CI (coverage)` over the other two coverage jobs) waits for them at its end
with `scripts/ci-await-jobs.sh` rather than through a relay job. `make check-ci-test-partition` keeps each Linux test
corpus owned by exactly one job. Every job restores a per-job Go build and
module cache (`.github/actions/go-cache`) that main pushes save as the job's
exact working set. `CI (WebMCP Chrome)` runs on its own macOS runner beside
`CI (macOS audio release)`: the two share no build outputs and together
exceeded the three-minute budget with a cold cache. The race-detector steps
are intentionally Linux-only, since the current Windows `runtime/cgo: cgo.exe:
exit status 2` failure is an environment limitation and Ubuntu CI is the
authoritative reproduction environment.
