# go-llm-gateway

A unified gateway library for multi-provider LLM inference. It presents a single `Gateway` interface over Anthropic (Claude), OpenAI, Google Gemini, xAI Grok, and fal.ai — letting consumers write provider-agnostic code and swap backends via configuration.

The library supports two inference modes:

- **Stateless inference** — request/response via `Gateway`, implemented by `DefaultGateway`.
- **Persistent sessions** — bidirectional streaming (WebSocket) via `SessionGateway`, implemented by `DefaultSessionGateway` for realtime-capable providers.

It also provides adapter types (`GatewayInferencer`, `SessionGatewayInferencer`) that satisfy the `go-agent-loop` interfaces, making it straightforward to plug any provider into an agent loop.

## Key Features

- **Unified interface** — one `Gateway` type works with Anthropic, OpenAI, Gemini, Grok, and fal.ai; swap providers via configuration with no code changes
- **Two inference modes** — stateless request/response (`Gateway`) and bidirectional streaming sessions (`SessionGateway`) for real-time use cases
- **Streaming first** — all stateless providers support token-by-token streaming via Go channels
- **Provider-specific power** — extended thinking and prompt caching (Anthropic), compatible base URL override and Realtime sessions (OpenAI), WebSocket realtime sessions (Grok), media generation (fal.ai)
- **Agent-loop ready** — `GatewayInferencer` and `SessionGatewayInferencer` adapters satisfy `go-agent-loop` interfaces out of the box
- **Deterministic testing** — `pkg/testing` HTTP record/replay utilities let you capture live traffic once and replay it in CI with no network required
- **Functional options** — all constructors use the options pattern; no config structs to populate up front

---

## Installation

```bash
go get github.com/portpowered/go-llm-gateway
```

During development this module is consumed via local `replace` directives. See `go.mod` in the consuming module.

## Quick Start

### Stateless Inference

```go
import (
    "context"
    "fmt"

    "github.com/portpowered/go-llm-gateway/pkg/gateway"
    "github.com/portpowered/go-llm-gateway/pkg/models"
    "github.com/portpowered/go-llm-gateway/pkg/providers/anthropic"
)

func main() {
    provider := anthropic.New(
        anthropic.WithAPIKey("sk-ant-..."),
        anthropic.WithModel("claude-sonnet-4-20250514"),
    )

    gw, err := gateway.NewGateway(
        gateway.WithProvider(provider),
    )
    if err != nil {
        panic(err)
    }

    resp, err := gw.Infer(context.Background(), gateway.InferenceRequest{
        Messages: []models.Message{
            models.NewTextMessage(models.RoleUser, "What is 2 + 2?"),
        },
    })
    if err != nil {
        panic(err)
    }

    fmt.Println(resp.Message.ContentParts[0].Text)
}
```

### Streaming Inference

```go
stream, err := gw.InferStream(ctx, gateway.InferenceRequest{
    Messages: []models.Message{
        models.NewTextMessage(models.RoleUser, "Tell me a story."),
    },
})
if err != nil {
    panic(err)
}

for msg := range stream {
    if msg.Type == messages.StreamMessageTypeText {
        fmt.Print(msg.Text)
    }
}
```

### Using with go-agent-loop

`GatewayInferencer` wraps a `Gateway` to satisfy `go-agent-loop`'s `Inferencer` interface:

```go
import (
    "github.com/portpowered/go-llm-gateway/pkg/inference"
    "github.com/portpowered/go-llm-gateway/pkg/providers/openai"
    "github.com/portpowered/go-agent-loop/pkg/agentloop"
)

provider := openai.New(openai.WithAPIKey("sk-..."))
gw, _ := gateway.NewGateway(gateway.WithProvider(provider))

inferencer := inference.NewGatewayInferencer(gw,
    inference.WithModel("gpt-4o"),
)

loop := agentloop.New(inferencer, myToolExecutor)
result, _ := loop.Execute(ctx, input)
```

## Providers

| Provider | Package | Inference | Streaming | Sessions |
|----------|---------|-----------|-----------|----------|
| Anthropic (Claude) | `providers/anthropic` | Yes | Yes | No |
| OpenAI | `providers/openai` | Yes | Yes | Yes (Realtime WebSocket) |
| Google Gemini | `providers/gemini` | Yes | Yes | No |
| xAI Grok | `providers/grok` | No | No | Yes (WebSocket) |
| fal.ai | `providers/fal` | Yes | No | No |

### Provider Configuration

Each provider uses the functional options pattern:

```go
// Anthropic — extended thinking + prompt caching
anthropic.New(
    anthropic.WithAPIKey("sk-ant-..."),
    anthropic.WithModel("claude-sonnet-4-20250514"),
)

// OpenAI — stateless inference also works with OpenRouter and other compatible APIs
openai.New(
    openai.WithAPIKey("sk-..."),
    openai.WithModel("gpt-4o"),
    openai.WithBaseURL("https://openrouter.ai/api/v1"),
)

// OpenAI Realtime — bidirectional sessions through SessionGateway
openai.New(
    openai.WithAPIKey("sk-..."),
    openai.WithModel("gpt-realtime"),
    openai.WithRealtimeBaseURL("wss://api.openai.com/v1/realtime"),
)

// Gemini
gemini.New(
    gemini.WithAPIKey("AIza..."),
    gemini.WithModel("gemini-2.5-flash"),
)

// Grok — realtime WebSocket sessions
grok.New(
    grok.WithAPIKey("xai-..."),
)

// fal.ai — media generation (TTS, video, image-to-video)
fal.New(
    fal.WithAPIKey("..."),
)
```

## Key Interfaces

Implement these to extend or mock the library:

```go
// providers.Provider — implement to add a new inference provider
type Provider interface {
    Name() string
    Infer(ctx context.Context, req InferenceRequest) (*InferenceResponse, error)
    InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error)
}

// providers.SessionProvider — implement to add a bidirectional session provider
type SessionProvider interface {
    Name() string
    ConnectSession(ctx context.Context, cfg models.SessionConfig) (messages.Session, error)
}

// gateway.Gateway — the top-level abstraction consumers depend on
type Gateway interface {
    Infer(ctx context.Context, req InferenceRequest) (*providers.InferenceResponse, error)
    InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error)
}
```

## Advanced Features

### Provider-Specific Configuration

Pass provider-specific options through the generic `Config` field on `InferenceRequest`:

```go
// Anthropic extended thinking
req := gateway.InferenceRequest{
    Messages: messages,
    Config: providers.InferenceConfig{
        Thinking: &providers.ThinkingConfig{
            Mode:         providers.ThinkingModeEnabled,
            BudgetTokens: 10000,
        },
    },
}
```

### Prompt Caching (Anthropic)

```go
req := gateway.InferenceRequest{
    Messages: messages,
    Config: providers.InferenceConfig{
        CacheControl: &providers.CacheControlConfig{
            Retention: providers.CacheRetention1h,
        },
    },
}
```

### Persistent Sessions

```go
sessionGw, err := gateway.NewSessionGateway(
    gateway.WithSessionProvider(openai.New(
        openai.WithAPIKey("sk-..."),
        openai.WithModel("gpt-realtime"),
    )),
)

session, err := sessionGw.ConnectSession(ctx, gateway.SessionConfig{
    Model:             "gpt-realtime",
    Modalities:        []gateway.SessionModality{gateway.SessionModalityText, gateway.SessionModalityAudio},
    Voice:             "alloy",
    Instructions:      "Keep responses concise.",
    InputAudioFormat:  gateway.AudioFormatPCM16,
    OutputAudioFormat: gateway.AudioFormatPCM16,
    TurnDetection:     &gateway.TurnDetectionConfig{Type: "server_vad"},
})

// Send audio
session.Send(ctx, messages.StreamMessage{
    Type:  messages.StreamMessageTypeAudio,
    Audio: audioBytes,
})

// Receive events
for event := range session.Receive() {
    // handle StreamMessage
}
```

OpenAI Realtime sessions default to the GA WebSocket setup: bearer authentication,
`wss://api.openai.com/v1/realtime?model=<model>`, no `OpenAI-Beta` header, and a
`session.update` payload with `type: "realtime"`, `output_modalities`, nested
`audio.input` / `audio.output` settings, instructions, and tool definitions. Use
`openai.WithRealtimeBaseURL(...)` for replay or compatible WebSocket endpoints.
`openai.WithLegacyRealtimeSessionUpdate()` is available only for older replay
fixtures or compatible providers that still require the pre-GA flat session shape.

OpenAI keeps stateless inference and Realtime sessions on separate gateway paths.
Use `Gateway` / `GatewayInferencer` for normal OpenAI chat-style requests, including
OpenAI-compatible HTTP providers. Use `SessionGateway` /
`SessionGatewayInferencer` for Realtime-capable models such as `gpt-realtime` when
the caller needs bidirectional session inference. Agent CLI currently routes live
OpenAI `agent session --record` runs through this session path only for
`gpt-realtime`; non-session commands keep using stateless OpenAI inference.

The OpenAI Realtime session config is carried through `models.SessionConfig` /
`gateway.SessionConfig`. The provider reads these fields when building the initial
`session.update` client event:

- `Model` selects the Realtime model and is also added to the WebSocket URL query
  when the URL does not already include `model`.
- `Modalities` maps to OpenAI Realtime output modalities.
- `InputAudioFormat`, `OutputAudioFormat`, and `Voice` map to nested audio input
  and output settings.
- `TurnDetection` maps to OpenAI server-side turn detection configuration.
- `Instructions` and `Tools` map to Realtime session instructions and tool
  definitions.

The provider sends and receives OpenAI Realtime wire events such as
`session.update`, `conversation.item.create`, `response.create`,
`session.created`, `response.output_text.delta`, `response.output_audio.delta`, tool-call
events, and `error`. Inbound provider events are normalized into shared session
stream messages so Agent CLI and Agent Loop do not need provider-specific OpenAI
Realtime parsing. OpenAI server events are asynchronous; replay fixtures can emit
server events before the next expected client event.

Before changing OpenAI Realtime provider behavior, verify current model names,
session fields, and event semantics against the official
[OpenAI Realtime guide](https://platform.openai.com/docs/guides/realtime) and
[Realtime API reference](https://developers.openai.com/api/reference/realtime).

## Testing

The `pkg/testing` package provides HTTP record/replay utilities for deterministic tests without live API calls:

```go
import llmtesting "github.com/portpowered/go-llm-gateway/pkg/testing"

// Record: wrap the real HTTP client
recorder := &llmtesting.RecordRoundTripper{
    Wrapped: http.DefaultTransport,
}
httpClient := &http.Client{Transport: recorder}
provider := anthropic.New(anthropic.WithHTTPClient(httpClient))
// ... run your test, then:
recorder.FlushToFile("testdata/my_test.json")

// Replay: load captured traffic
replayer, _ := llmtesting.NewReplayRoundTripper("testdata/my_test.json")
httpClient := &http.Client{Transport: replayer}
provider := anthropic.New(anthropic.WithHTTPClient(httpClient))
// provider now returns recorded responses — no network required
```

## Package Layout

```
go-llm-gateway/
├── pkg/
│   ├── gateway/       # Gateway and SessionGateway interfaces + DefaultGateway
│   ├── models/        # Shared message and session types (re-exports from go-agent-loop)
│   ├── inference/     # go-agent-loop adapters: GatewayInferencer, SessionGatewayInferencer
│   ├── providers/     # Provider interface + implementations
│   │   ├── anthropic/ # Claude (extended thinking, prompt caching)
│   │   ├── openai/    # GPT, OpenAI-compatible APIs, and OpenAI Realtime sessions
│   │   ├── gemini/    # Google Gemini
│   │   ├── grok/      # xAI Grok realtime (WebSocket)
│   │   └── fal/       # fal.ai (TTS, video generation)
│   └── testing/       # HTTP record/replay utilities
├── go.mod
└── Makefile
```

## Build and Test

Contributors should start with the [LLM Gateway Development Guide](docs/development.md). It covers local package layout, provider verification, deterministic HTTP record/replay, and downstream agent-loop compatibility checks.

```bash
make build   # Build all packages
make test    # Run all tests
make fmt     # Format code
make vet     # Run go vet
```

## Dependencies

- [`github.com/portpowered/go-agent-loop`](../go-agent-loop/README.md) — shared message model and agent interfaces
- [`github.com/anthropics/anthropic-sdk-go`](https://github.com/anthropics/anthropic-sdk-go) — Anthropic provider
- [`google.golang.org/genai`](https://pkg.go.dev/google.golang.org/genai) — Gemini provider
- [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket) — OpenAI and Grok realtime providers

## See Also

- [`go-agent-loop`](../go-agent-loop/README.md) — agent runtime that consumes this gateway
- [`agent-cli`](../agent-cli/README.md) — CLI binary built on top of both libraries
- [LLM Gateway Development Guide](docs/development.md) — package-local contributor workflow
- [Library Standards (STD-023)](../../docs/standards/systems/library-standards.md) — conventions all libraries follow
- [Libraries Development Guide](../../docs/processes/libraries-development.md) — full development workflow
