# go-llm-gateway

`go-llm-gateway` is the provider integration library in `go-agent-harness`. It
gives consumers a small stateless gateway API, a separate session gateway for
realtime providers, and adapters that plug those capabilities into
`go-agent-loop`.

Start here when you want provider access from Go code. If you want a ready-made
CLI instead, start with [`agent-cli`](../agent-cli/README.md).

## Supported Package Surface

The current consumer-facing surfaces are:

| Package | Use it for |
| --- | --- |
| `pkg/gateway` | Creating stateless gateways with `NewGateway(...)` and session gateways with `NewSessionGateway(...)` |
| `pkg/inference` | Adapting gateways to `go-agent-loop` via `GatewayInferencer` and `SessionGatewayInferencer` |
| `pkg/models` | Building gateway-owned session values plus compatibility aliases for loop-owned message contracts |
| `pkg/providers/anthropic` | Anthropic stateless inference provider |
| `pkg/providers/openai` | OpenAI-compatible stateless inference plus OpenAI Realtime session provider |
| `pkg/providers/gemini` | Gemini stateless inference provider |
| `pkg/providers/grok` | Grok realtime session provider |
| `pkg/providers/fal` | fal.ai media-oriented stateless provider |
| `pkg/testing` | HTTP and session record/replay helpers for deterministic tests |

Most consumers start with `pkg/gateway`, one provider package, and `pkg/models`.
Use `pkg/inference` only when you are wiring this module into `go-agent-loop`.
Within `pkg/models`, shared message-style types remain compatibility aliases
over `go-agent-loop/pkg/messages`, while session config and session events are
gateway-owned surfaces.

## Constructor Ownership Boundary

`go-llm-gateway` provider builders consume runtime dependencies; they should
not decide generic application transport policy.

- Provider packages own provider-specific request shaping, option parsing, and
  protocol translation.
- The application composition layer, currently `agent-cli`, owns whether a
  stateless provider runs live, record, or replay and builds the shared
  `*http.Client` for that mode before provider construction.
- When the application needs a non-default live transport, it injects that
  transport before provider construction; record mode wraps the same owned base
  transport instead of silently choosing a provider-local transport.
- In this repository, that injected seam is
  `agent-cli/internal/agent.ProviderBuildContext.HTTPClient`.

Treat hidden record/replay transport assembly or implicit `http.DefaultTransport`
selection inside provider builders as a boundary violation.

## Shared Session Fixtures

`pkg/testing` is the authoritative owner for committed shared `.session.json`
replay fixtures in this repository. The canonical shared fixture root is
`pkg/testing/testdata/session-fixtures`.

Use that root for replay captures and fixture-hygiene validation that other
modules may consume. Keep package-private fixtures in the module that owns the
behavior under test, and do not treat sibling-module `testdata` directories as
a shared fixture API.

Cross-module consumers should resolve shared committed fixtures through
`pkg/testing.SharedSessionFixturePath(...)`. Before review, validate committed
fixtures with:

```bash
go run ./cmd/session-fixture-validator ./pkg/testing/testdata/session-fixtures
```

For the full authoring contract, provenance requirements, and sanitization
rules, see
[`pkg/testing/session-fixture-authoring.md`](pkg/testing/session-fixture-authoring.md)
and [`pkg/testing/README.md`](pkg/testing/README.md).

## Install

```bash
go get github.com/portpowered/go-llm-gateway@latest
```

Then import the public packages you need:

```go
import (
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers/openai"
)
```

During workspace development this module currently uses a local `replace`
directive for `github.com/portpowered/go-agent-loop`. That is part of the
current workspace composition, so this module should not be documented as fully
independent from the loop contracts yet.

For this Phase 3 slice, `go-agent-loop/pkg/messages` remains the deliberate
shared runtime contract boundary. Provider logging is a gateway-owned concern
through `pkg/logging`, not a loop runtime dependency.

## Getting Started

### Stateless Inference

This is the main entrypoint for request/response and streaming provider calls:

```go
package main

import (
	"context"
	"fmt"

	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers/openai"
)

func main() {
	provider := openai.New(
		openai.WithAPIKey("sk-..."),
		openai.WithModel("gpt-4o"),
	)

	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		panic(err)
	}

	resp, err := gw.Infer(context.Background(), gateway.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Summarize the number four in one sentence."),
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(resp.Message.TextContent())
}
```

For streaming-capable stateless providers, the same gateway exposes
`InferStream(...)`.

## Error And Terminal Taxonomy

`pkg/gateway` exposes a small typed error taxonomy for caller decisions. Branch
on these classes with `errors.Is`; use `errors.As` when you need structured
details such as provider status code, replay mismatch fields, or replay
incomplete fields. Do not match error message text for control flow.

| Error class | Caller action |
| --- | --- |
| `gateway.ErrAuthentication` | Refresh, replace, or configure provider credentials before retrying |
| `gateway.ErrAuthorization` | Change account permissions, model access, region, or requested operation |
| `gateway.ErrRateLimit` | Back off, retry later, or route work to another allowed provider |
| `gateway.ErrInvalidRequest` | Fix the request payload, parameters, or tool/model inputs before retrying |
| `gateway.ErrUnsupportedModel` | Select a model supported by the provider and requested capability |
| `gateway.ErrProviderHTTPStatus` | Inspect `*gateway.ProviderHTTPStatusError` for provider, status, and body details |
| `gateway.ErrTransport` | Treat as a provider transport failure before a usable provider response was available |
| `gateway.ErrReplayMismatch` | Diagnose deterministic replay fixture or request divergence |
| `gateway.ErrReplayIncomplete` | Diagnose a replay that ended before all required fixture or capture events were consumed |
| `gateway.ErrCancellation` | Handle caller cancellation or timeout separately from provider failures |

Provider and stream adapters serialize the same caller-actionable meanings with
the provider classification strings in `pkg/providers`:

| Classification | Meaning |
| --- | --- |
| `provider_rejected` | The provider rejected the request, with more detail on the typed error when available |
| `authentication` | Credentials are missing, invalid, expired, or rejected |
| `rate_limited` | Provider or gateway throttling; callers may retry with backoff |
| `invalid_request` | The request shape, parameters, model, or tool input should be fixed before retrying |
| `unsupported_request` | The requested provider, model, feature, or mode is unsupported |
| `transport` | The transport failed before a usable provider response or completion was available |
| `cancellation` | Caller cancellation or deadline stopped the operation |
| `replay_mismatch` | Deterministic replay diverged from the committed fixture or capture |
| `replay_incomplete` | Replay closed before all required fixture or capture events were consumed |
| `partial_output` | Caller-visible output was emitted before cancellation or failure |
| `unknown` | No more specific public class was available |

Example:

```go
resp, err := gw.Infer(ctx, req)
if err != nil {
	var statusErr *gateway.ProviderHTTPStatusError
	switch {
	case errors.Is(err, gateway.ErrRateLimit):
		// retry with backoff
	case errors.Is(err, gateway.ErrAuthentication):
		// refresh credentials
	case errors.As(err, &statusErr):
		// log statusErr.Provider, statusErr.StatusCode, and statusErr.Body
	default:
		// generic failure handling
	}
}
_ = resp
```

Stateless streaming, loop streams, sessions, replay helpers, and CLI NDJSON use
an additive terminal-event contract from `go-agent-loop/pkg/messages`. The
serialized fields are:

| Payload | Additive fields |
| --- | --- |
| `MESSAGE.END` | `terminal_reason`, `terminal_provenance`, `output_state` |
| `ERROR` | `classification`, `terminal_reason`, `terminal_provenance`, `output_state` |
| `SESSION.CLOSE` | `classification`, `terminal_reason`, `terminal_provenance`, `output_state` |

Terminal reasons are `provider_authored_completion`,
`loop_synthesized_completion`, `cancellation`, `replay_divergence`,
`replay_incomplete`, `session_close`, `partial_output`, `provider_close`, and
`terminal_failure`. Provenance values are `provider`, `loop`, `gateway`,
`session`, `replay`, and `cli`. Output states are `complete`, `partial`, `none`,
and `not_applicable`.

Returned setup, validation, replay, and provider-open failures are Go errors.
Mid-stream provider/runtime failures, cancellation observed after stream output
starts, provider close, synthesized completion, provider-authored completion,
and session close are emitted in-band on the stream/session/CLI event surface
when that surface is already active. Some in-process stream events expose both:
`messages.ErrorValue.Err` preserves the typed Go error, while the JSON payload
contains the structured classification and terminal fields. `Err` itself is not
serialized.

`messages.ErrorValue.Message` and `SESSION.CLOSE.reason` remain
operator-readable compatibility text. New callers should branch on
`errors.Is`, `errors.As`, `classification`, `terminal_reason`,
`terminal_provenance`, and `output_state`:

```go
for event := range stream {
	if event.Type != messages.StreamTypeError {
		continue
	}
	value, ok := event.Value.(*messages.ErrorValue)
	if ok && errors.Is(value.Err, gateway.ErrTransport) {
		// handle stream transport failure
	}
}
```

Current representative coverage is intentionally additive:

- OpenAI-compatible stateless `Infer` classifies HTTP status failures, common
  status-specific classes such as authentication and rate limit, transport
  failures, and caller cancellation.
- OpenAI-compatible stateless `InferStream` preserves typed stream-open
  failures and stream runtime failures in `ERROR` event values when the error is
  available in-process.
- Gateway direct streams normalize provider and runtime `ERROR` payloads with
  serialized `classification`, `terminal_reason=terminal_failure`,
  gateway/provider provenance, and output state.
- Loop streams distinguish provider-authored completion, loop-synthesized
  completion, provider close, cancellation, terminal failure after partial
  output, and session close through structured terminal fields.
- `pkg/testing` replay helpers classify replay divergence as
  `gateway.ErrReplayMismatch` / `providers.ErrReplayMismatch` and incomplete
  replay as `gateway.ErrReplayIncomplete` / `providers.ErrReplayIncomplete`.
- CLI NDJSON writes the concrete stream/session payload values directly, so the
  additive terminal fields above are visible to CLI consumers without parsing
  text.
- Anthropic, Gemini, fal.ai, and session provider surfaces may still return
  provider-specific or generic errors where typed gateway classification has not
  been wired yet. Treat those surfaces as best-effort until their adapters
  explicitly preserve the public classes.

For the cross-surface contract and category-by-category caller actions, see
[`docs/architecture/stream-terminal-contract.md`](../docs/architecture/stream-terminal-contract.md).

### Session-Based Inference

Realtime sessions are a separate surface. Today they are provider-specific:
OpenAI supports stateless and session APIs, while Grok supports session APIs
only.

```go
sessionProvider := openai.New(
	openai.WithAPIKey("sk-..."),
	openai.WithModel("gpt-realtime"),
)

sessionGW, err := gateway.NewSessionGateway(
	gateway.WithSessionProvider(sessionProvider),
)
if err != nil {
	panic(err)
}

session, err := sessionGW.ConnectSession(context.Background(), models.SessionConfig{
	Model:        "gpt-realtime",
	Modalities:   []models.SessionModality{models.SessionModalityText},
	Instructions: "Keep answers concise.",
})
if err != nil {
	panic(err)
}

_ = session
```

Use `pkg/models.SessionConfig` for gateway-owned session model, modality,
audio, tool, and turn-detection settings. Shared message, tool, and token-usage
contracts imported through `pkg/models` still follow the authoritative
definitions in `go-agent-loop/pkg/messages`.

## Provider Capabilities and Local Validation

Applications can inspect the configured provider's public capability contract
before deciding which features to expose. Discovery is local metadata lookup: it
does not perform network access, require live credentials, or mutate request
state.

```go
provider := openai.New(openai.WithAPIKey("sk-..."))

gw, err := gateway.NewGateway(gateway.WithProvider(provider))
if err != nil {
	panic(err)
}

caps := gw.Capabilities()
if caps.Stateless.Tools.IsSupported() {
	// Offer tool calling for this provider.
}
if caps.Stateless.Reasoning.State == gateway.CapabilityStateUnknown {
	// Do not present this as supported unless your application has another
	// provider-specific reason to allow it.
}
```

`Capabilities()` is available on `gateway.DefaultGateway` and
`gateway.DefaultSessionGateway`. Callers that receive an abstract gateway can
check `gateway.CapabilityReporter` when they need discovery. Providers that do
not implement explicit capability reporting return `unknown` for every field,
which means "no local support claim." Unknown is different from unsupported:
the gateway does not reject unknown capabilities locally, but consumers should
not display them as supported.

Capability states are:

| State | Meaning |
| --- | --- |
| `supported` | The provider wrapper explicitly claims local support for the feature. |
| `unsupported` | The provider wrapper explicitly rejects the feature as unavailable. |
| `unknown` | The provider wrapper has not published a support claim. This is the fallback for legacy providers. |

The public capability fields map to the feature areas requested by consumers:

| Customer feature | Capability field |
| --- | --- |
| Stateless tools | `caps.Stateless.Tools` |
| Stateless streaming | `caps.Stateless.Streaming` |
| Stateless image input | `caps.Stateless.ImageInput` |
| Stateless audio input | `caps.Stateless.AudioInput` |
| Stateless audio output | `caps.Stateless.AudioOutput` |
| Stateless video output | `caps.Stateless.VideoOutput` |
| Stateless reasoning | `caps.Stateless.Reasoning` |
| Stateless prompt caching | `caps.Stateless.PromptCaching` |
| Stateless provider-specific config | `caps.Stateless.ProviderSpecificConfig` |
| Realtime or bidirectional sessions | `caps.Session.Sessions` |
| Session tools | `caps.Session.Tools` |
| Session audio input | `caps.Session.AudioInput` |
| Session audio output | `caps.Session.AudioOutput` |
| Session provider-specific config | `caps.Session.ProviderSpecificConfig` |

Gateway validation rejects deterministic requests only when the matching
capability is explicitly `unsupported`. For stateless requests this covers
tools, streaming, image input, audio input/output already present in message
history, video output already present in message history, reasoning, prompt
caching, and raw provider-specific config. For sessions this covers unsupported
session setup, session tools, audio input/output config, and raw
provider-specific config before a provider connection is opened.

Validation failures use the structured `UnsupportedFeatureError` contract,
re-exported from `pkg/gateway` and `pkg/providers`:

```go
_, err = gw.Infer(ctx, gateway.InferenceRequest{
	Messages: []models.Message{
		models.NewTextMessage(models.RoleUser, "think step by step"),
	},
	Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingEnabled},
})
if err != nil {
	var unsupported *gateway.UnsupportedFeatureError
	if errors.As(err, &unsupported) {
		fmt.Printf(
			"%s rejected %s for %s: %s\n",
			unsupported.Provider,
			unsupported.Feature,
			unsupported.RequestedMode,
			unsupported.Capability.State,
		)
	}
}
```

Provider-specific gaps must stay explicit. A provider wrapper should mark a
feature `unsupported` when the local wrapper ignores or cannot translate that
request shape, and `unknown` when support cannot be proven without depending on
live provider behavior or credentials.

Current static provider-family capability states:

| Provider family | Stateless summary | Session summary | Unknown rationale |
| --- | --- | --- | --- |
| Anthropic | Tools, streaming, image input, reasoning, and prompt caching are supported. Native audio input/output, video output, and raw provider config are unsupported by this wrapper. | Sessions, session tools, session audio input/output, and raw session config are unsupported. | None currently reported. |
| OpenAI-compatible | Tools, streaming, image input, audio input/output are supported. Video output, reasoning options, prompt caching, and raw provider config are unsupported by this wrapper. | OpenAI Realtime sessions, tools, and audio input/output are supported. Raw session config is unsupported. | None currently reported. |
| Gemini | Tools, streaming, image input, and audio input are supported. Audio output, video output, reasoning options, prompt caching, and raw provider config are unsupported by this wrapper. | Sessions, session tools, session audio input/output, and raw session config are unsupported. | None currently reported. |
| Grok | Stateless features are unsupported because this provider is session-only in this module. | Realtime sessions, tools, and audio input/output are supported. Raw session config is unsupported. | None currently reported. |
| fal.ai | Image input, audio input/output, video output, and raw provider config are supported for sync stateless flows. Tools, streaming, reasoning options, and prompt caching are unsupported by this wrapper. | Sessions, session tools, session audio input/output, and raw session config are unsupported. | None currently reported. |

These states are local wrapper claims. They do not replace provider-side
authorization, model availability, quota, content, or endpoint validation, and
credential-free validation tests only prove deterministic local mismatches.

## Provider Surface Map

The module does not offer one identical capability set across all providers.
Treat the provider packages as adapters behind a shared shape, not as proof that
every feature is portable.

| Provider package | Stateless `Infer` | Stateless `InferStream` | Session `ConnectSession` | Notes |
| --- | --- | --- | --- | --- |
| `pkg/providers/anthropic` | Yes | Yes | No | Supports Anthropic-specific thinking and cache controls |
| `pkg/providers/openai` | Yes | Yes | Yes | Also supports OpenAI-compatible base URLs and Realtime sessions |
| `pkg/providers/gemini` | Yes | Yes | No | Stateless Gemini integration |
| `pkg/providers/grok` | No | No | Yes | Realtime session provider only |
| `pkg/providers/fal` | Yes | No | No | Media-oriented sync stateless flows; streaming rejects locally as unsupported |

Portable guarantees across stateless providers are limited to the gateway and
provider interfaces:

- `providers.Provider` exposes `Infer(...)` and `InferStream(...)`
- `gateway.Gateway` forwards `InferenceRequest` values to the configured
  provider
- `models.Message`, tool definitions, and token usage types are compatibility
  aliases over shared loop contracts re-exported through `pkg/models`

Provider-specific behavior lives behind those shared interfaces. Examples:

- Anthropic-only thinking and cache-control support
- OpenAI-compatible base URL overrides and Realtime sessions
- Grok realtime session transport
- fal.ai model-specific media flows and config payloads
- fal.ai streaming is unsupported in this wrapper; gateway and direct provider
  streaming calls return `providers.UnsupportedFeatureError` before HTTP work

## Typed Errors And Event Classification

Gateway, provider, replay, stream, session, and interaction failure paths expose
machine-readable classification through public typed errors or structured event
fields. Human-readable error text is still present for operators, but caller
policy should not parse `err.Error()`, stream error text, or interaction event
messages for the repaired representative classes.

### Returned Go Errors

Use `errors.Is` for policy decisions against the public taxonomy in
`pkg/providers`:

| Sentinel | Classification | Caller action |
| --- | --- | --- |
| `providers.ErrProviderRejected` | `provider_rejected` | Treat as an upstream provider rejection and inspect any narrower class before retrying. |
| `providers.ErrAuthentication` | `authentication` | Refresh or replace credentials, authorization, or provider account configuration. |
| `providers.ErrRateLimited` | `rate_limited` | Retry later using backoff or reduce request rate. |
| `providers.ErrInvalidRequest` | `invalid_request` | Correct the request shape, model input, or provider-rejected parameter. |
| `providers.ErrUnsupportedRequest` | `unsupported_request` | Choose a supported provider, model, feature, or mode. |
| `providers.ErrTransport` | `transport` | Retry according to network and provider availability policy. |
| `providers.ErrCancellation` | `cancellation` | Treat as caller-initiated shutdown or cancellation, not provider failure. |
| `providers.ErrReplayMismatch` | `replay_mismatch` | Treat deterministic replay as divergent and refresh the fixture or test expectation. |
| `providers.ErrReplayIncomplete` | `replay_incomplete` | Treat deterministic replay as ending before all required fixture or capture events were consumed. |
| `providers.ErrPartialOutput` | `partial_output` | Treat the operation as interrupted after caller-visible output was produced. |

Use `errors.As` when you need structured details:

- `*providers.ProviderError` exposes `Provider`, `StatusCode`, and `Detail` for
  representative provider HTTP rejections.
- `*providers.ValidationError` exposes `Provider`, `Feature`, `Requested`,
  `Supported`, and `Detail` for representative local invalid or unsupported
  request failures.

Provider HTTP errors may match both `ErrProviderRejected` and a narrower class
such as `ErrAuthentication`, `ErrRateLimited`, `ErrInvalidRequest`, or
`ErrTransport`. Prefer checking the most specific class your policy handles
first.

### Direct Stream Errors

Direct streaming failures can surface either as a returned setup error from
`InferStream(...)` or as an `ERROR` stream message. Classify returned setup
errors with `errors.Is` and `errors.As` as above. For stream messages, inspect
`messages.ErrorValue.Classification`, which carries one of the public
classification strings such as `authentication`, `rate_limited`, `transport`,
or `unsupported_request`.

`messages.ErrorValue.Message` remains the readable operator text. Existing
provider detail fields such as `ErrorType`, `Code`, `Param`, and `EventID`
remain provider context; they are not the public taxonomy.

### Interaction And Session Events

Normalized interaction errors expose the same taxonomy through structured event
fields:

- `gateway.InteractionError.Classification`
- `gateway.InteractionCancellation.Classification`
- `gateway.InteractionCancellation.OutputState`
- `messages.InteractionError.Classification`
- `messages.InteractionCancellation.Classification`
- `messages.InteractionCancellation.OutputState`

Use the `Classification` fields for event-level policy, including provider
rejection, gateway/runtime transport failure, local invalid request,
unsupported request, cancellation, replay mismatch, and partial output when that
meaning is available. Use `OutputState` to distinguish interrupted partial
output from clean completion or total failure; representative cancellation after
text output is reported with a partial-output terminal state.

Session connection errors are returned as Go errors and should be classified
with `errors.Is` or `errors.As`. Replay helpers in `pkg/testing`, including
session replayers and replay WebSocket dialers, preserve replay divergence as
`providers.ErrReplayMismatch` / `gateway.ErrReplayMismatch` and incomplete
replay as `providers.ErrReplayIncomplete` / `gateway.ErrReplayIncomplete`.

### Current Representative Scope

The repaired contract covers representative provider rejection, local
validation, direct stream error, session connect error, normalized interaction
error, cancellation, replay mismatch, replay incomplete, and partial-output
paths. Remaining follow-up scope is provider-wide parity for every
adapter/status/parser shape, every replay entrypoint, and a broader shared
final-status design if callers need one final accessor across all stream APIs.
Those limits are tracked in the Phase 4 repair scope record under
`docs/internal`.

## Using With go-agent-loop

`go-llm-gateway` includes adapters for the loop contracts defined in
`go-agent-loop`; it does not define a separate loop contract of its own.

```go
package main

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-llm-gateway/pkg/providers/openai"
)

func main() {
	provider := openai.New(openai.WithAPIKey("sk-..."))
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		panic(err)
	}

	inferencer := inference.NewGatewayInferencer(
		gw,
		inference.WithModel("gpt-4o"),
	)

	loop, err := agentloop.New(agentloop.WithInferencer(inferencer))
	if err != nil {
		panic(err)
	}

	_, _ = loop.Execute(context.Background(), agentloop.NewExecuteInput("hello"))
}
```

Use:

- `inference.NewGatewayInferencer(...)` for stateless loop turns
- `inference.NewSessionGatewayInferencer(...)` for loop-managed session flows

This relationship is part of the current architecture boundary: `agent-cli`
composes `go-agent-loop` and `go-llm-gateway`, and this module currently
develops against the checked-out loop contracts through `replace`.

## Deterministic Validation

Package-local validation:

```bash
cd go-llm-gateway
make deps
make build
make vet
make test
```

Workspace validation from the repository root:

```bash
make deps
make fmt
make typecheck
make vet
make lint
make staticcheck
make test
make test-regressions
make build
make coverage
make validate
make ci
```

Use the module-local targets when changing this package in isolation. Use the
root targets when you need to verify the current cross-module workspace state.

## Testing Utilities

`pkg/testing` is the supported test helper surface for deterministic provider
tests:

- HTTP recording and replay for stateless providers
- session recording and replay for realtime providers
- `cmd/session-fixture-validator` for validating committed `.session.json`
  fixtures

Before committing new or changed session fixtures, validate them with:

```bash
go run ./cmd/session-fixture-validator ./pkg/testing
```

For fixture format details, start with
[`pkg/testing/README.md`](./pkg/testing/README.md).

## Development Notes

Start with [docs/development.md](./docs/development.md) before changing provider
implementations or fixture workflows. When documenting or extending this module,
anchor claims to the exported packages above rather than internal directories or
aspirational provider parity.
