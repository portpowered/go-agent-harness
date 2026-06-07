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
| `pkg/models` | Building messages and session config values that flow through the gateway |
| `pkg/providers/anthropic` | Anthropic stateless inference provider |
| `pkg/providers/openai` | OpenAI-compatible stateless inference plus OpenAI Realtime session provider |
| `pkg/providers/gemini` | Gemini stateless inference provider |
| `pkg/providers/grok` | Grok realtime session provider |
| `pkg/providers/fal` | fal.ai media-oriented stateless provider |
| `pkg/testing` | HTTP and session record/replay helpers for deterministic tests |

Most consumers start with `pkg/gateway`, one provider package, and `pkg/models`.
Use `pkg/inference` only when you are wiring this module into `go-agent-loop`.

## Constructor Ownership Boundary

`go-llm-gateway` provider builders consume runtime dependencies; they should
not decide generic application transport policy.

- Provider packages own provider-specific request shaping, option parsing, and
  protocol translation.
- The application composition layer, currently `agent-cli`, owns whether a
  stateless provider runs live, record, or replay and builds the shared
  `*http.Client` for that mode before provider construction.
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

Use `pkg/models.SessionConfig` for session model, modality, audio, tool, and
turn-detection settings.

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
| `pkg/providers/fal` | Yes | No | No | Media-oriented stateless flows; model-specific request expectations |

Portable guarantees across stateless providers are limited to the gateway and
provider interfaces:

- `providers.Provider` exposes `Infer(...)` and `InferStream(...)`
- `gateway.Gateway` forwards `InferenceRequest` values to the configured
  provider
- `models.Message`, tool definitions, and token usage types come from shared
  loop contracts re-exported through `pkg/models`

Provider-specific behavior lives behind those shared interfaces. Examples:

- Anthropic-only thinking and cache-control support
- OpenAI-compatible base URL overrides and Realtime sessions
- Grok realtime session transport
- fal.ai model-specific media flows and config payloads

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
