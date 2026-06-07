# Contract Gap Audit

This document records the current contract gaps that Phase 2 needs to harden. It starts with the hidden-coupling and dependency-injection findings that block clean separation between the reusable libraries and the CLI composition layer.

Use [`dependencies.md`](./dependencies.md) for the intended dependency direction. Use this audit for the places where the codebase still relies on convenience coupling or constructor ownership that is broader than the intended architecture.

## Intended Adapters vs Hidden Coupling

Intended adapter seams:

- `go-agent-loop/pkg/messages.Inferencer` implemented by `go-llm-gateway/pkg/inference.GatewayInferencer`
- `go-agent-loop/pkg/messages.SessionInferencer` implemented by `go-llm-gateway/pkg/inference.SessionGatewayInferencer`
- `go-llm-gateway/pkg/providers.Provider` and `pkg/providers.SessionProvider` selected by `agent-cli`

Hidden coupling:

- code in one module reaches into another module's testdata, file layout, or provider-specific helpers instead of depending only on the declared interface
- constructors create live transports or default executors internally instead of requiring the caller to own those dependencies
- application services build provider-specific runtime wiring directly, which makes the application layer the only place where richer behavior exists

The findings below are written so a reviewer can distinguish "this is the contract" from "this happens to work today because the current packages know too much about each other".

## Hidden Coupling Findings

### HC-01: `go-llm-gateway` test hygiene depends on `agent-cli` fixture layout

- Affected boundary: `go-llm-gateway/internal/sessionfixturevalidator` -> `agent-cli/test/integration/testdata`
- Evidence: `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go` hard-codes `../../../agent-cli/test/integration/testdata` as a committed fixture root.
- Observable impact:
  - a test-only change to `agent-cli` fixture locations can fail `go-llm-gateway` without any gateway contract change
  - the gateway module cannot validate its own fixture hygiene in isolation from the CLI worktree layout
  - reviewers can mistake cross-module fixture reuse for an intended dependency direction
- Why this is hidden coupling instead of an adapter:
  - the intended seam is shared session-capture shape under `go-llm-gateway/pkg/testing`, not a relative path from the gateway module into the CLI module
- Recommended Phase 2 hardening:
  - move shared committed session fixtures under a gateway-owned shared fixture package or shared workspace testdata root with an explicit ownership comment
  - keep `agent-cli` consuming those fixtures, rather than the gateway test suite walking into CLI-owned paths

### HC-02: `go-llm-gateway/pkg/models` is a naming facade over loop-owned message contracts

- Affected boundary: `go-llm-gateway/pkg/models` -> `go-agent-loop/pkg/messages`
- Evidence: `go-llm-gateway/pkg/models/message.go` aliases `messages.Message`, `messages.ToolDefinition`, `messages.TokenUsage`, and related types directly.
- Observable impact:
  - gateway consumers can import `pkg/models` and believe they are using an independent gateway contract, while the real compatibility anchor is still `go-agent-loop/pkg/messages`
  - contract review becomes harder because renames or field changes in loop-owned message types automatically leak into gateway-facing types
  - the gateway package naming suggests a separate vocabulary even though the two packages currently move in lockstep
- Why this is hidden coupling instead of an adapter:
  - an adapter would translate between loop and gateway vocabularies; the current code exposes the same types under two package paths
- Recommended Phase 2 hardening:
  - either document `pkg/models` as a deliberate alias layer with no independent compatibility promise, or define a truly gateway-owned model surface and add explicit translation at the boundary

### HC-03: session command behavior in `agent-cli` depends on provider-specific runtime helpers

- Affected boundary: `agent-cli/internal/services/session.go` -> `go-llm-gateway/pkg/providers/grok` and `pkg/providers/openai`
- Evidence:
  - `RunSession` and its helpers choose between Grok and OpenAI branches directly
  - replay and record flows construct `grok.NewDefaultWebSocketDialer`, `NewGrokSessionInferencerWithOptions`, `NewOpenAIRealtimeSessionInferencerWithOptions`, and an OpenAI-specific dialer adapter
- Observable impact:
  - richer session behavior exists only in the CLI composition layer, not behind one reusable session contract
  - adding a new realtime provider requires editing CLI service logic, not only registering a provider
  - provider-specific replay and recording semantics are harder to test as generic session behavior
- Why this is hidden coupling instead of an adapter:
  - the intended adapter seam is `messages.SessionInferencer`; branching on concrete provider packages inside the service is application wiring convenience above that seam
- Recommended Phase 2 hardening:
  - introduce a CLI-owned session runtime registry or factory interface that selects provider-specific session behavior behind one composition boundary
  - keep the provider-specific branch logic in one constructor-focused package instead of scattering it through service flow control

## Dependency-Injection Findings

### DI-01: `agentloop.New` creates a default tool executor when the caller omits one

- Affected boundary: `go-agent-loop/pkg/agentloop.New` constructor ownership
- Evidence: `go-agent-loop/pkg/agentloop/agent_loop.go` injects `&messages.DefaultToolExecutor{}` when `cfg.ToolExecutor` is nil.
- Observable impact:
  - loop construction succeeds even when the runtime has no real tool executor, and the failure shifts to the first tool call at runtime
  - callers cannot tell from construction-time behavior whether tool execution is intentionally disabled or accidentally missing
  - tests that forget to inject a tool executor may still build a loop that only fails later
- Why this is an injection gap:
  - the reusable loop owns fallback dependency creation instead of making the caller own the execution capability decision
- Recommended Phase 2 hardening:
  - decide explicitly whether tool execution is optional
  - if optional, model the absence as a declared no-tools mode
  - if required when tools are configured, fail construction when tools are present but no executor is injected

### DI-02: provider builders own HTTP transport creation and capture wiring

- Affected boundary: `agent-cli/internal/agent/provider_openai.go` and `provider_fal.go`
- Evidence:
  - builders construct `http.Client` values internally
  - record mode wraps `http.DefaultTransport` with `testing.NewRecordRoundTripper`
  - replay mode swaps in `testing.NewReplayRoundTripper`
- Observable impact:
  - transport policy, retry behavior, and recording concerns are coupled to provider construction instead of being injected as a separate dependency
  - future providers are likely to duplicate the same capture-wiring logic
  - callers cannot reuse one transport policy across providers without editing provider-specific builders
- Why this is an injection gap:
  - the factory API takes high-level config only, so transport ownership stays hidden inside each builder
- Recommended Phase 2 hardening:
  - inject transport or HTTP client policy through `ProviderBuildContext`, or add a narrower shared factory helper for record/replay transport ownership
  - keep provider builders responsible for provider options, not generic transport assembly

### DI-03: `Executor.loadSystemPrompt` mixes prompt resolution with filesystem and workspace side effects

- Affected boundary: `agent-cli/internal/agent.Executor`
- Evidence:
  - `loadSystemPrompt` reads arbitrary files, calls `workspace.EnsureAgentsMD`, reads `AGENTS.md`, loads config again for system info, and loads skills metadata
- Observable impact:
  - prompt resolution cannot be exercised as pure contract logic because it implicitly depends on filesystem state, workspace initialization, and configuration loading
  - a caller asking only for the resolved system prompt can still create files on disk
  - testing and future embedding become harder because one method owns multiple dependency types
- Why this is an injection gap:
  - the composition layer has not separated pure prompt assembly from IO-backed prompt source discovery
- Recommended Phase 2 hardening:
  - split prompt assembly into pure string composition plus injected loaders for filesystem-backed prompt sources, system info, and skills summaries
  - make `EnsureAgentsMD` an explicit composition step before prompt resolution instead of a side effect of reading the prompt

### DI-04: session service helpers load config and choose live dialers internally

- Affected boundary: `agent-cli/internal/services/session.go`
- Evidence:
  - `effectiveSessionProvider`, `resolveGrokSessionConfig`, and `resolveOpenAIRealtimeSessionConfig` call `config.NewDefaultConfigStorage(...).Load()` internally
  - live record paths create `grok.NewDefaultWebSocketDialer()` when no dialer is injected
- Observable impact:
  - session command behavior is harder to test as a pure service because config storage and network dialer ownership are embedded in helper functions
  - record/replay paths have different injection affordances: tests can pass a dialer, but production code silently falls back to a live default
  - cancellation and timeout policy for the session command is mixed with dependency creation rather than composed at one entrypoint
- Why this is an injection gap:
  - service-level runtime orchestration and dependency acquisition are still combined
- Recommended Phase 2 hardening:
  - introduce a session runtime dependency bundle for config loading, dialer selection, and provider-specific inferencer construction
  - keep `RunSession` responsible for command semantics, not for discovering defaults from disk and transport packages

## Phase 2 Entry Point After This Story

The most bounded enabling work from the findings above is:

1. remove testdata path coupling by giving session fixtures one explicit owner
2. make constructor ownership explicit for tool execution and transport/dialer dependencies
3. centralize CLI session provider selection behind one factory boundary

Those changes reduce accidental cross-module knowledge before Phase 2 starts tightening context, error, lifecycle, and compatibility contracts.
