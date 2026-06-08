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

## Phase 3 Shared-Contract Decision

For `P3-CORE-01` and the scoped `P3-CORE-02` boundary work, the authoritative
shared contract boundary is `go-agent-loop/pkg/messages`.

That package owns the cross-library message, stream, tool, token-usage,
inference, and session interfaces that both `go-llm-gateway` and `agent-cli`
compose against. The current repository already uses that package as the real
compatibility anchor: session interfaces are declared there, and gateway-facing
shared-message types already track it directly.

This Phase 3 slice keeps the shared boundary in `go-agent-loop/pkg/messages`
instead of introducing a new shared module because the repository does not yet
show a lower-risk alternative:

- the loop package already defines the contracts that adapters implement
- the gateway already depends on those contracts without reverse imports
- extracting a new shared module here would add migration and naming churn
  without reducing an active dependency risk in the current codebase

Reviewer-citable rule for this phase:

- `go-agent-loop/pkg/messages` is the source of truth for shared runtime
  contracts
- `go-llm-gateway` may adapt to or alias those contracts, but it does not own a
  second shared core surface
- any future proposal to extract a separate shared module should justify what
  concrete risk cannot be managed at the current boundary
- `go-agent-loop/test/architecture/dependency_direction_test.go` is the
  automated proof for this slice: it requires gateway adapters to import
  loop-owned contracts and fails if any `go-agent-loop` package starts
  depending on `go-llm-gateway`

Reviewer rule of thumb:

- A new import from `go-agent-loop` into `go-llm-gateway` is expected when it consumes loop-owned contracts or shared message models.
- A new import from `go-agent-loop` into `agent-cli` is expected when the CLI is configuring or driving loop behavior.
- A new import from `go-llm-gateway` into `agent-cli` is expected when the CLI is choosing or configuring concrete provider adapters.
- A new import from `go-agent-loop` into `agent-cli` or `go-llm-gateway` is not symmetric. Imports in the reverse direction would violate the intended layering.

## Current Contract Surfaces

`go-agent-loop` owns the core runtime-facing interfaces and shared types:

- `pkg/agentloop.AgenticLoop` is the main loop API for execute, streaming execute, run, pause, state inspection, and IO configuration.
- `pkg/messages` is the authoritative shared contract boundary for Phase 3. It defines the shared `Message`, `StreamMessage`, tool payload, token-usage, inference, and session contracts used across modules.
- `pkg/messages.SessionInferencer` and `pkg/messages.Session` are explicitly declared in the loop module so realtime/session implementations depend on loop contracts rather than the reverse.

`go-llm-gateway` owns provider normalization and adapter implementations:

- `pkg/gateway.Gateway` is the stateless inference boundary for normalized requests and responses.
- `pkg/inference.GatewayInferencer` and `pkg/inference.SessionGatewayInferencer` are the intended adapters from gateway/provider code into loop-owned interfaces.
- `pkg/providers` owns provider-specific request shaping and provider option translation, but not generic live/record/replay transport policy.

`agent-cli` owns application wiring:

- `internal/agent.Executor` builds loops, loads config, and selects the active provider implementation.
- `internal/agent.Executor` and `internal/agent.buildProviderHTTPRuntime(...)` own the shared stateless provider HTTP runtime decision for live, record, and replay modes before provider-specific builders run.
- `internal/agent.ProviderBuildContext` is the constructor seam that passes that owned runtime dependency into concrete provider builders.
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

Constructor ownership contract after this Phase 2 step:

- Tool execution capability is a caller-owned decision at `agentloop.New(...)`.
- `WithTools(...)` advertises tool definitions only when the caller also makes an explicit capability choice with `WithToolExecutor(...)` or `WithToolExecutionDisabled()`.
- The reusable loop no longer silently creates a default tool executor behind the caller's back.

Exports that currently look incidental or at least not yet hardened:

- `AgenticLoop` currently combines distinct concerns such as turn execution, long-running control, IO source/sink reconfiguration, and TODO queue mutation on one interface. That shape is exported today, but it is not obviously the final stable contract boundary.
- Concrete option and state helpers under `pkg/agentloop` and lower-level engine/subsystem packages are public in Go visibility terms, but the architecture does not currently claim them as downstream compatibility promises.

Concrete Phase 2 API gap for this module:

- The exported `AgenticLoop` interface does not clearly separate the minimal contract a library caller needs (`Execute`, `ExecuteStreaming`, `Run`, `Send`, `Pause`) from operational helpers (`SetInputs`, `SetOutputs`, TODO queue methods). That ambiguity makes breaking-change review difficult because maintainers cannot tell which methods are essential contract and which are implementation convenience.

### `go-llm-gateway`

Primary consumer-facing entrypoints:

- `pkg/gateway.Gateway` and `pkg/gateway.DefaultGateway` are the normalized request/response seam for stateless inference.
- `pkg/gateway.DefaultSessionGateway` is the gateway-side bridge that accepts gateway-owned `models.SessionConfig` before returning the loop-owned `messages.Session` contract.
- `pkg/inference.SessionGatewayInferencer` is the public session-mode bridge into `go-agent-loop` and should be described as an adapter, not as an independent shared-session surface.
- `pkg/inference.GatewayInferencer` is the public stateless adapter from gateway requests into the loop-owned `messages.Inferencer` contract.
- `pkg/providers.Provider` and `pkg/providers.SessionProvider` are the provider-facing construction seams used by the CLI composition layer.

Candidate stable contracts:

- `pkg/gateway` is the most plausible downstream-stable package because it hides provider-specific request shaping behind normalized request types.
- `pkg/inference.GatewayInferencer` and `pkg/inference.SessionGatewayInferencer` are intentionally public adapter types because they are the expected bridge into loop-owned interfaces rather than a second shared core.
- `pkg/providers.Provider` and `pkg/providers.SessionProvider` are candidate stable construction contracts for adding providers without changing loop code.

Constructor ownership contract after this Phase 2 step:

- Generic stateless HTTP runtime policy for live, record, and replay belongs to `agent-cli`, not to individual provider builders.
- Provider builders consume injected runtime dependencies through `internal/agent.ProviderBuildContext` and remain focused on provider-specific option wiring.
- Reviewers should treat implicit `http.DefaultTransport` selection or record/replay capture assembly inside provider builders as an architectural regression.

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

For the concrete gap inventory behind those examples, see [`contract-gap-audit.md`](./contract-gap-audit.md).

## Phase 2 Constructor Ownership Boundary Status

The constructor ownership enabling step from [`contract-gap-audit.md`](./contract-gap-audit.md) is now satisfied for the two scoped boundaries in this phase:

- DI-01 tool execution ownership is explicit at `go-agent-loop/pkg/agentloop.New(...)`.
- DI-02 stateless provider live/record/replay transport ownership is centralized in `agent-cli/internal/agent` and injected into provider builders.

Future Phase 2 work should preserve those ownership boundaries instead of reintroducing constructor-side defaults in reusable packages.

## Phase 2 Session Runtime Ownership Status

The remaining session-mode constructor/runtime ownership gap for the scoped
Grok and OpenAI record/replay paths is now narrowed by the delivered repair:

- `agent-cli/internal/services/session_runtime.go` is the explicit CLI-owned
  composition seam for session provider selection, config resolution, live
  versus replay dialer choice, and provider-specific runtime wiring.
- `go-llm-gateway/pkg/providers/grok` and
  `go-llm-gateway/pkg/providers/openai` now treat missing session dialers as a
  contract error at the provider session boundary instead of silently creating
  live defaults in the reviewed runtime paths.
- `go-llm-gateway/pkg/testing.SessionRecorder` and
  `go-llm-gateway/pkg/testing.SessionReplayer` preserve the owned lifecycle
  context for relay writes, which keeps cancellation ownership aligned with the
  same seam that owns dialer injection.

For reviewer-facing convergence evidence, cite
[`docs/internal/phase-2-session-runtime-ownership-validator.md`](../internal/phase-2-session-runtime-ownership-validator.md)
alongside the session-runtime checklist rows `P2-SRO-01` through
`P2-GATE-01`, and the broader constructor-ownership row `P2-COB-04` that this
slice advances.

## Decision Checklist For Future Changes

Before approving a new dependency or exported constructor, check:

1. Does the change keep `go-agent-loop` free of provider-specific, CLI-specific, filesystem, or process-local imports?
2. Does provider integration enter the loop through a loop-owned interface or shared message contract rather than a concrete provider type?
3. Does `agent-cli` remain the place where config, local tools, storage, and environment wiring are composed?
4. If a package crosses these boundaries directly, is it an intentional adapter seam or only incidental coupling that should be documented as such?

If the answer to item 1 is no, or item 2 requires a concrete provider type inside `go-agent-loop`, the change does not fit the intended architecture.
