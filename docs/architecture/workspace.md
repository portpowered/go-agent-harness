# Go workspace layout

This repository is a multi-module Go workspace. A root `go.work` file is committed so contributors can build and test all libraries from the repository root without publishing pseudo-versions or guessing local `replace` paths.

## Why `go.work` is committed

- **Local cross-module edits**: `agent-cli` depends on `go-agent-loop` and `go-llm-gateway`. A workspace lets those dependencies resolve to the sibling directories in this checkout.
- **Single validation surface**: Root tooling (`make ci`, future CI) can run `go` commands from the repository root and see every module.
- **Reproducible onboarding**: New contributors clone once, run `go work sync` if needed, and work from the root—no manual `replace` setup.

`AGENTS.md` describes this layout; the committed `go.work` is the canonical expression of that decision.

## Modules in the workspace

| Directory        | Module path                          | Role                                      |
|------------------|--------------------------------------|-------------------------------------------|
| `./agent-cli`    | `github.com/portpowered/agent-cli`   | CLI for exercising the agent loop and gateway |
| `./go-agent-loop`| `github.com/portpowered/go-agent-loop` | Tick-based agent execution harness      |
| `./go-llm-gateway` | `github.com/portpowered/go-llm-gateway` | Provider-agnostic LLM gateway        |

The root `go.work` lists these three paths in `use` directives. Other top-level trees (for example `tests/`, `factory/`, `docs/`) are not workspace modules unless explicitly added later.

## Minimum Go version

All workspace modules require **Go 1.24** or newer. Individual `go.mod` files may specify patch-level minimums (for example `go 1.24.2` in `agent-cli`) and optional `toolchain` lines. The root `go.work` `go` directive is set to a version compatible with every module’s `go` line.

Install Go 1.24+ before working in this repo. Run `go version` from the repository root to confirm.

## Relationship to `replace` directives

Some modules still declare `replace` directives pointing at sibling directories:

- **`agent-cli/go.mod`** replaces `github.com/portpowered/go-agent-loop` and `github.com/portpowered/go-llm-gateway` with `../go-agent-loop` and `../go-llm-gateway`.
- **`go-llm-gateway/go.mod`** replaces `github.com/portpowered/go-agent-loop` with `../go-agent-loop`.

When you are **inside the workspace** (repository root with `go.work` present, or `GOWORK` pointing at it), the workspace `use` entries take precedence for local resolution. The `replace` directives remain useful for:

- Tools or editors invoked **outside** workspace mode (for example running `go test` from a single module directory without `GOWORK` set).
- Downstream consumers that copy a module in isolation and rely on documented relative paths.
- CI or scripts that explicitly disable workspace mode.

You do not need to remove `replace` directives to use the workspace; they are complementary, not conflicting, as long as paths stay aligned with the directory layout.

## Common commands

From the repository root:

```bash
go work sync   # sync workspace sum file with module dependencies

# List or build every package in all workspace modules. The repository root is
# not itself a module, so use module-qualified ./... patterns from the root:
go list ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
go build ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
go test ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...
```

From a single module directory (for example `agent-cli/`), `go list ./...` and `go test ./...` apply to that module only. Go still loads the parent `go.work` automatically when present.

## Test tiers and credentials

Default `go test ./...` and root `make test` / `make ci` targets are intended to run **without live provider API keys**.

The root Makefile exposes these deterministic and opt-in test tiers:

| Target | Deterministic | What it runs |
|--------|---------------|--------------|
| `make test` | yes | Per-module `go test ./...` across `agent-cli`, `go-agent-loop`, and `go-llm-gateway`. |
| `make test-integration` | yes | Deterministic integration packages: `agent-cli/test/integration` and `go-agent-loop/test/functional`. |
| `make test-regressions` | yes | Committed replay and fixture regression tests, including Agent CLI replay cases and `go-llm-gateway` replay/fixture packages. |
| `make test-customer-sessions` | no | Local-only placeholder for future private session sweeps. It skips unless you explicitly set `RUN_CUSTOMER_SESSIONS=1`, and it is not part of `make ci`. |

`make test-integration` and `make test-regressions` must complete without live provider credentials. Tests that eventually need real inference credentials or private customer session data stay opt-in and out of the default CI path.
