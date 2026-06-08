# LLM Gateway Development Guide

This guide is the package-local contributor guide for `libraries/go-llm-gateway`. Read it before changing provider abstractions, inference normalization, streaming, sessions, provider features, or HTTP record/replay support, then apply the shared [Libraries Development Guide](../../../docs/processes/libraries-development.md) and [Library Standards](../../../docs/standards/systems/library-standards.md).

## Purpose

`go-llm-gateway` is the unified inference layer for Port OS agent systems. It normalizes requests and streaming responses across Anthropic, OpenAI, Gemini, Grok, and fal.ai, and exposes adapters that satisfy `go-agent-loop` interfaces.

## Local Architecture

- `pkg/gateway/` owns `Gateway`, `SessionGateway`, default implementations, and top-level request routing.
- `pkg/models/` owns shared model and session types, including re-exports from `go-agent-loop`.
- `pkg/inference/` adapts gateway implementations into `go-agent-loop` inferencers.
- `pkg/providers/` owns provider interfaces and implementations for Anthropic, OpenAI, Gemini, Grok, and fal.ai.
- `pkg/testing/` contains deterministic HTTP record/replay utilities for provider tests.
- `pkg/testing/testdata/session-fixtures/` is the authoritative repository root for committed shared `.session.json` replay fixtures used across modules.

## Development Commands

Run commands from `libraries/go-llm-gateway`.

```bash
make build
make test
make fmt
make vet
make deps
make deps-tidy
```

## Package-Specific Verification

1. Run `make test` for normal gateway, provider, adapter, and model changes.
2. Use `pkg/testing` HTTP record/replay utilities for provider behavior that would otherwise require live network calls.
3. Run adapter tests when changing `pkg/inference`, message conversion, streaming events, or model type re-exports.
4. If a change touches `go-agent-loop` shared message contracts, also run `go-agent-loop` and `agent-cli` checks.
5. Update `pkg/testing/README.md` when record/replay capture format, replay matching, or fixture workflow changes.

## Capability Contract

Provider families that can make static local claims should implement
`providers.CapabilityReporter` and return `providers.ProviderCapabilities`.
Every public stateless or session feature in that report must be marked as one
of:

- `supported`: the wrapper maps the feature locally for the provider family.
- `unsupported`: the wrapper deterministically does not map the feature; include
  a rationale that is useful in `providers.UnsupportedFeatureError`.
- `unknown`: support depends on provider model, endpoint, automatic provider
  behavior, or another runtime fact that the wrapper cannot prove statically;
  include that rationale and let the request reach provider runtime.

Current capability semantics:

| Provider family | Supported | Unsupported | Unknown |
| --- | --- | --- | --- |
| Anthropic | Stateless inference, streaming, tools, image input, reasoning, prompt caching | Native audio input, video input/output, raw provider config, sessions | None currently reported |
| OpenAI-compatible | Stateless inference, streaming, tools, image input, audio input; OpenAI Realtime sessions, tools, text/audio modalities, known realtime audio formats, turn detection | Stateless video output, reasoning options, raw provider config; raw realtime session config | Video input for compatible transports; prompt caching behavior |
| Gemini | Stateless inference, streaming, tools, image input, audio input | Video input/output, reasoning options, prompt caching, raw provider config, sessions | None currently reported |
| Grok | Realtime sessions, tools, text/audio modalities, known realtime audio formats, turn detection | Stateless inference/streaming and raw realtime session config | None currently reported |
| fal.ai | Sync stateless inference, image input, audio input, video output, raw provider config | Streaming, tools, video input, reasoning options, prompt caching, sessions | None currently reported |

Gateway validation rejects only deterministic `unsupported` states before
provider side effects. It must not silently treat `unknown` as supported or
unsupported; unknown behavior remains a provider/model runtime decision.

Credential-free tests should prove capability reports and local rejection at
observable public seams:

- `gateway.DefaultGateway` for stateless inference and streaming.
- `gateway.DefaultSessionGateway` before websocket or session connection setup.
- `inference.GatewayInferencer` or `inference.SessionGatewayInferencer` when the
  loop adapter behavior is the user-visible surface.
- Direct provider methods when a provider has a deterministic unsupported seam,
  such as fal.ai streaming.

Use fake providers with capability reports and side-effect counters for local
validation tests. Assert returned `*providers.UnsupportedFeatureError` fields,
interaction terminal event details, emitted stream/session errors, or provider
call counts rather than scanning source files or route inventories.

## Local Gotchas

- Provider-specific features should flow through normalized request config where possible, not leak provider-only types into callers.
- Anthropic extended thinking and prompt caching map through shared config fields.
- OpenAI-compatible providers may use a base URL override.
- OpenAI and Grok realtime sessions use the session gateway path rather than stateless inference.
- OpenAI Realtime uses `WithRealtimeBaseURL` for WebSocket endpoints and sends the GA nested `session.update` shape by default; use `WithLegacyRealtimeSessionUpdate` only for older replay fixtures or compatible providers that still expect flat audio fields.
- OpenAI Realtime session behavior should be checked against the official OpenAI Realtime guide and API reference before changing model routing, session fields, or event normalization. Current gateway behavior sends `session.update` before user input, sends `response.create` after text input, and normalizes asynchronous server events such as `session.created`, `response.output_text.delta`, `response.output_audio.delta`, tool-call events, and `error`.
- OpenAI Realtime `session.closed` provider events should normalize to shared `SESSION.CLOSE` so replay and Agent CLI consumers can verify graceful shutdown without provider-specific EOF handling.
- Streaming normalization should preserve the shared `messages.StreamMessage` contract expected by `go-agent-loop`.
- fal.ai streaming is intentionally unsupported in the public capability contract; `InferStream` should return `providers.UnsupportedFeatureError` before HTTP work rather than a clean empty stream.
- Provider tests should avoid live API calls in CI by using recorded HTTP fixtures.
- This package owns the committed shared session fixture contract for Phase 2 boundary cleanup. Agent CLI and other modules may consume shared fixtures from `pkg/testing/testdata/session-fixtures`, but package-private `testdata` directories are not a cross-module API.

## Related Docs

- [LLM Gateway README](../README.md)
- [HTTP Record/Replay Testing](../pkg/testing/README.md)
- [LLM Gateway Intent](../../../docs/intents/llm-gateway.md)
- [Library Standards](../../../docs/standards/systems/library-standards.md)
- [Go Agent Loop Development Guide](../../go-agent-loop/docs/development.md)
