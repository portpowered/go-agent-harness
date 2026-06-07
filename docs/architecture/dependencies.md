# Dependency Direction and Integration Seams

This repository is a Go workspace with three module-level roles:

- `go-agent-loop` is the reusable runtime loop library. It owns the execution loop, shared message/session contracts, and the interfaces that outside inference implementations plug into.
- `go-llm-gateway` is the reusable provider gateway library. It adapts concrete providers to the contracts declared by `go-agent-loop`.
- `agent-cli` is the executable application. It composes the two libraries with filesystem, terminal, config, session storage, and local tool wiring.

## Intended Dependency Direction

The intended module dependency graph is:

```text
agent-cli
  ├── go-agent-loop
  └── go-llm-gateway
          └── go-agent-loop
```

Allowed dependency direction:

- `go-agent-loop` must remain reusable as the lowest-level runtime contract package. It should not import `go-llm-gateway`, `agent-cli`, or application-specific wiring.
- `go-llm-gateway` may depend on `go-agent-loop` because it implements loop-owned contracts such as `messages.Inferencer`, `messages.SessionInferencer`, `messages.Session`, and the shared `messages.StreamMessage` model.
- `agent-cli` may depend on both libraries because it is the composition layer that chooses providers, builds loops, connects IO, and persists sessions.

Reviewer rule of thumb:

- A new import from `go-agent-loop` into `go-llm-gateway` is expected when it consumes loop-owned contracts or shared message models.
- A new import from `go-agent-loop` into `agent-cli` is expected when the CLI is configuring or driving loop behavior.
- A new import from `go-llm-gateway` into `agent-cli` is expected when the CLI is choosing or configuring concrete provider adapters.
- A new import from `go-agent-loop` into `agent-cli` or `go-llm-gateway` is not symmetric. Imports in the reverse direction would violate the intended layering.

## Current Contract Surfaces

`go-agent-loop` owns the core runtime-facing interfaces and shared types:

- `pkg/agentloop.AgenticLoop` is the main loop API for execute, streaming execute, run, pause, state inspection, and IO configuration.
- `pkg/messages` defines the shared `Message`, `StreamMessage`, tool payload, and session contracts used across modules.
- `pkg/messages.SessionInferencer` and `pkg/messages.Session` are explicitly declared in the loop module so realtime/session implementations depend on loop contracts rather than the reverse.

`go-llm-gateway` owns provider normalization and adapter implementations:

- `pkg/gateway.Gateway` is the stateless inference boundary for normalized requests and responses.
- `pkg/inference.GatewayInferencer` and `pkg/inference.SessionGatewayInferencer` are the intended adapters from gateway/provider code into loop-owned interfaces.
- `pkg/providers` owns provider-specific transports and request shaping behind gateway-facing contracts.

`agent-cli` owns application wiring:

- `internal/agent.Executor` builds loops, loads config, and selects the active provider implementation.
- `internal/services/session.go` assembles session-mode runtime behavior by combining `agentloop`, gateway session inferencers, replay helpers, and CLI output handling.
- `internal/tools`, `internal/session`, `internal/config`, and `internal/workspace` are application concerns that should stay above the reusable libraries.

## Intended Adapter Seams

The intended seams reviewers should preserve are:

- `go-agent-loop/pkg/messages.SessionInferencer` implemented by `go-llm-gateway` session adapters.
- `go-agent-loop/pkg/messages.Inferencer` implemented by `go-llm-gateway/pkg/inference.GatewayInferencer`.
- `go-llm-gateway/pkg/gateway.Gateway` implemented by gateway/provider composition inside `go-llm-gateway`.
- `agent-cli` selecting concrete providers and passing the resulting inferencer/session-inferencer into `go-agent-loop`.

These seams are the architectural contract because they let provider code change without forcing `go-agent-loop` to know provider APIs and let `agent-cli` remain the only module that owns local process and user-environment concerns.

## Hidden Coupling That Is Not the Contract

The repository also has coupling that should not be mistaken for the intended architecture:

- `go-llm-gateway/pkg/models` currently re-exports message types from `go-agent-loop`. That is acceptable as an implementation convenience, but it means gateway model naming is not yet an independent contract surface.
- `go-llm-gateway/internal/sessionfixturevalidator` reaches into `../../../agent-cli/test/integration/testdata`. That is a test-time convenience, not a supported module dependency direction.
- `agent-cli/internal/services/session.go` imports concrete provider packages such as `pkg/providers/grok` and `pkg/providers/openai` directly. That is valid application wiring, but those concrete imports are not a reusable library seam and should not move downward into `go-agent-loop`.

When reviewing future changes, treat those examples as current coupling to manage carefully, not as permission to add reverse imports or cross-module constructor ownership in reusable packages.

## Decision Checklist For Future Changes

Before approving a new dependency or exported constructor, check:

1. Does the change keep `go-agent-loop` free of provider-specific, CLI-specific, filesystem, or process-local imports?
2. Does provider integration enter the loop through a loop-owned interface or shared message contract rather than a concrete provider type?
3. Does `agent-cli` remain the place where config, local tools, storage, and environment wiring are composed?
4. If a package crosses these boundaries directly, is it an intentional adapter seam or only incidental coupling that should be documented as such?

If the answer to item 1 is no, or item 2 requires a concrete provider type inside `go-agent-loop`, the change does not fit the intended architecture.
