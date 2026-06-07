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

## Public API Surfaces and Candidate Contracts

The repository exports more names than it has stable contracts. For Phase 2 review, use the following distinction.

### `go-agent-loop`

Primary consumer-facing entrypoints:

- `pkg/agentloop.AgenticLoop` is the main runtime contract for turn execution, streaming execution, continuous run control, and interrupt/resume-style message injection.
- `pkg/agentloop.New(...)` is the construction seam because callers supply loop mode and the inferencer/session-inferencer adapters there.
- `pkg/messages` is the cross-module data contract: `Message`, `StreamMessage`, `InferenceRequest`, `InferenceResult`, `Session`, `Inferencer`, and `SessionInferencer`.

Candidate stable contracts:

- `pkg/messages` should be treated as intentionally public because both `go-llm-gateway` and `agent-cli` depend on those types as the shared runtime vocabulary.
- `pkg/agentloop.AgenticLoop` should be treated as intentionally public because it is the loop porcelain surface that downstream composition code uses directly.

Exports that currently look incidental or at least not yet hardened:

- `AgenticLoop` currently combines distinct concerns such as turn execution, long-running control, IO source/sink reconfiguration, and TODO queue mutation on one interface. That shape is exported today, but it is not obviously the final stable contract boundary.
- Concrete option and state helpers under `pkg/agentloop` and lower-level engine/subsystem packages are public in Go visibility terms, but the architecture does not currently claim them as downstream compatibility promises.

Concrete Phase 2 API gap for this module:

- The exported `AgenticLoop` interface does not clearly separate the minimal contract a library caller needs (`Execute`, `ExecuteStreaming`, `Run`, `Send`, `Pause`) from operational helpers (`SetInputs`, `SetOutputs`, TODO queue methods). That ambiguity makes breaking-change review difficult because maintainers cannot tell which methods are essential contract and which are implementation convenience.

### `go-llm-gateway`

Primary consumer-facing entrypoints:

- `pkg/gateway.Gateway` and `pkg/gateway.DefaultGateway` are the normalized request/response seam for stateless inference.
- `pkg/gateway.DefaultSessionGateway` and `pkg/inference.SessionGatewayInferencer` are the current session-mode bridge into `go-agent-loop`.
- `pkg/inference.GatewayInferencer` is the main adapter from gateway requests into `messages.Inferencer`.
- `pkg/providers.Provider` and `pkg/providers.SessionProvider` are the provider-facing construction seams used by the CLI composition layer.

Candidate stable contracts:

- `pkg/gateway` is the most plausible downstream-stable package because it hides provider-specific request shaping behind normalized request types.
- `pkg/inference.GatewayInferencer` and `pkg/inference.SessionGatewayInferencer` are intentionally public adapter types because they are the expected bridge into loop-owned interfaces.
- `pkg/providers.Provider` and `pkg/providers.SessionProvider` are candidate stable construction contracts for adding providers without changing loop code.

Exports that currently look incidental or not yet independent:

- `pkg/models` currently mirrors and re-exports loop-owned message concepts. Consumers can import it, but the package is not yet an independent contract vocabulary because it still tracks `go-agent-loop` naming closely.
- Concrete provider packages such as `pkg/providers/openai`, `pkg/providers/grok`, `pkg/providers/anthropic`, and `pkg/providers/gemini` are intentionally public for the CLI's composition needs, but they should not be mistaken for the cross-provider contract surface.

Concrete Phase 2 API gap for this module:

- `SessionGatewayInferencer` only exposes `WithSessionModel`, `WithSessionVoice`, and `WithSessionInstructions`, while `models.SessionConfig` already carries modalities, audio formats, tools, turn detection, and provider-specific config. That means the exported session adapter surface is narrower than the gateway session contract and forces callers toward provider-specific wiring when they need richer session configuration.

### `agent-cli`

Primary consumer-facing entrypoints:

- The actual supported external contract is the CLI command tree built from `internal/cli.RootCommand`, `internal/cli.Router`, and the generated Cobra commands.
- `internal/agent.Executor` and `internal/services/*` are the application composition seams that tests and command handlers use to assemble loop, provider, replay, and storage behavior.
- `internal/agent.ProviderFactory` is the registry used to map configured provider names to concrete `go-llm-gateway` providers.

Candidate stable contracts:

- The command-line behavior, flags, replay/record file shapes, and emitted session/output behavior are the intended user-facing contracts.
- The internal wiring packages are intentionally reusable inside the application, but because they live under `internal/`, they are not promised as reusable downstream library APIs.

Exports that currently look incidental or application-internal:

- Exported names inside `internal/agent`, `internal/cli`, and `internal/services` are visible to sibling packages for implementation reasons, not because `agent-cli` is offering a general-purpose embedding SDK.
- Direct imports of concrete provider packages in application services are valid composition details, but they are not the compatibility surface reviewers should preserve for external consumers.

Concrete Phase 2 API gap for this module:

- The repository does not currently document one canonical integration boundary for "programmatic CLI embedding" versus "CLI-only behavior". As a result, exported `internal/*` types such as `Executor`, `ProviderFactory`, and command builders can appear stable to maintainers even though the supported contract is really the executable behavior and config/flag surface.

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
