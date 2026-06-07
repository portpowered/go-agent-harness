# go-agent-harness

`go-agent-harness` is a multi-module Go workspace for building agent runtimes,
provider gateways, and a reference CLI on top of them. Start at the root when
you want the package map and shared validation commands, then drop into the
module README that matches your use case.

## Workspace Map

This repository currently contains three consumer-facing modules:

| Module | Role | Start here when you want... |
| --- | --- | --- |
| [`agent-cli`](./agent-cli/README.md) | Reference executable that composes the loop and gateway libraries into a CLI agent. | A ready-to-run binary for `ask`, `chat`, tool debugging, or session workflows. |
| [`go-agent-loop`](./go-agent-loop/README.md) | Tick-driven agent runtime library with explicit subsystem orchestration. | A Go package for building your own agent runtime or integrating custom subsystems. |
| [`go-llm-gateway`](./go-llm-gateway/README.md) | Multi-provider inference gateway with adapters that plug into the loop. | A Go package for stateless or session-based LLM provider access. |

## Getting Started

Choose the entrypoint that matches the surface you need:

- CLI consumer: start with [`agent-cli/README.md`](./agent-cli/README.md)
- Agent runtime library consumer: start with [`go-agent-loop/README.md`](./go-agent-loop/README.md)
- Provider gateway library consumer: start with [`go-llm-gateway/README.md`](./go-llm-gateway/README.md)

## Composition Boundaries

The modules are related, but they are not fully independent in the current
workspace layout:

- `agent-cli` is the top-level executable and currently depends on both
  `go-agent-loop` and `go-llm-gateway` through local `replace` directives during
  workspace development.
- `go-agent-loop` is the runtime library. It defines the loop, tick model, and
  subsystem-facing contracts that agent integrations build around.
- `go-llm-gateway` provides provider adapters plus loop-facing inferencer
  adapters. It also uses a local `replace` directive to develop against the
  checked-out `go-agent-loop` module.

That means the workspace should be described as a composed multi-module repo,
not as three completely isolated packages.

Each module README below focuses on its current supported consumer-facing
surface. Internal directories may exist for implementation, but they should not
be treated as public contracts unless the package README calls them out
explicitly.

## Root Validation Commands

The root `Makefile` provides deterministic entrypoints that delegate to the
module-level `Makefile`s:

```bash
make deps
make typecheck
make lint
make test
make validate
```

These targets are intended for repository-wide validation:

- `make deps`: download module dependencies for all three modules
- `make typecheck`: build every module from the root to catch compile-time
  breakage
- `make lint`: run `go vet` in every module
- `make test`: run each module's package test suite
- `make validate`: run `typecheck`, `lint`, and `test` in sequence

If you are working inside a single module, its README also documents the
package-local commands.

## Repository Layout

The documentation refresh in this phase is scoped to the active modules in this
worktree:

- `agent-cli/`: CLI application and user-facing command workflows
- `go-agent-loop/`: agent runtime library
- `go-llm-gateway/`: provider gateway library and loop adapters
- `factory/`: agent-factory support code used to drive this workflow

## Development Notes

The root workspace currently coordinates module-level builds rather than a
single `go.work` file. For consumer guidance, prefer the root `make` targets for
cross-module validation and the package README for module-specific setup.
