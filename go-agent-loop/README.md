# go-agent-loop

`go-agent-loop` is the runtime library in `go-agent-harness`. It provides a
tick-driven agent loop, the message contracts that flow through it, and the
participant/subsystem seams that provider adapters and tool executors plug into.

Start here when you want to build your own Go agent runtime instead of using the
top-level `agent-cli` binary.

## Supported Package Surface

- `pkg/agentloop`: primary consumer entrypoint for creating and running loops
- `pkg/messages`: shared message, tool, inference, and session contracts
- `pkg/subsystems`: subsystem interfaces plus reusable helpers such as recording,
  interrupt handling, token counting, and context-pressure notification

Most consumers should start with `pkg/agentloop` and `pkg/messages`. Lower-level
packages such as `pkg/engine` and `pkg/participants` exist in this module, but
they are not the first consumer-facing surface to build against.

## Install

```bash
go get github.com/portpowered/go-agent-loop@latest
```

Then import the package entrypoints you need:

```go
import (
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)
```

## Getting Started

The smallest integration is a stateless loop built with an implementation of
`messages.Inferencer`. The loop owns that contract; provider adapters such as
those in `go-llm-gateway` implement it.

```go
package main

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

type staticInferencer struct{}

func (staticInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, "2+2 = 4"),
	}, nil
}

func (staticInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 4)
	go func() {
		defer close(ch)
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue(), Role: messages.RoleAssistant}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("2+2 = 4"), Role: messages.RoleAssistant}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue(), Role: messages.RoleAssistant}
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant}
	}()
	return ch, nil
}

func main() {
	loop, err := agentloop.New(agentloop.WithInferencer(staticInferencer{}))
	if err != nil {
		panic(err)
	}

	result, err := loop.Execute(context.Background(), agentloop.NewExecuteInput("what is two plus two?"))
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Text())
}
```

That example uses the main request/response path:

- `agentloop.New(...)` creates a loop with explicit options
- `loop.Execute(...)` runs one user turn and returns the collected full messages
- `result.Text()` returns the final assistant text for the turn
- `loop.GetConversationHistory()` and `loop.GetConversationDeltas()` expose the
  recorded full-message and delta history after execution

For streaming text or reasoning deltas, use `ExecuteStreaming(...)` instead.

Tool execution is now an explicit constructor contract:

- if you configure tool definitions with `WithTools(...)`, also provide
  `WithToolExecutor(...)` to enable tool execution
- if your embedding wants an intentional no-tools path even when tool
  definitions are otherwise available, add `WithToolExecutionDisabled()`
- `agentloop.New(...)` fails fast when tools are configured without either of
  those explicit capability decisions

## Runtime Model

The loop is tick-driven:

- user, system, model, and tool events enter the loop as messages or deltas
- the engine advances on ticks
- subsystems run in a defined order on each tick
- the loop updates conversation state, dispatches inference/tool work, and
  forwards output back through typed message buffers

In consumer terms, this gives you one runtime that can handle both:

- stateless request/response turns through `Execute` or `ExecuteStreaming`
- long-running session flows through `Run`, `Send`, `Pause`, and session-mode
  inferencers

## Public Surfaces To Start From

### `pkg/agentloop`

Use this package first for:

- creating loops with `New(...)`
- configuring runtime behavior with options such as `WithInferencer`,
  `WithSessionInferencer`, `WithToolExecutor`,
  `WithToolExecutionDisabled`, `WithTools`,
  `WithSystemPrompt`, and `WithBufferCapacity`
- running a single turn with `Execute(...)` or `ExecuteStreaming(...)`
- running a continuous or duplex loop with `Run(...)` and `Send(...)`

### `pkg/messages`

Use this package when you need to:

- implement `messages.Inferencer` for stateless model calls
- implement `messages.SessionInferencer` for persistent bidirectional sessions
- implement `messages.ToolExecutor` for tool execution
- construct `messages.Message`, `messages.ToolDefinition`, and multimodal
  `ContentPart` values that flow through the loop

### `pkg/subsystems`

Use this package when you need custom helpers that run on each tick. The
subsystem interface is explicit:

- each subsystem declares a `TickGroup()`
- each subsystem runs `Execute(...)` against the current loop state

This is the extension point for recorder-style helpers, token counting, and
other loop-adjacent behaviors that should stay inside the tick lifecycle.

## Session And Adapter Boundaries

`go-agent-loop` owns the runtime contracts, not the provider transports.

- for stateless inference, the loop depends on `messages.Inferencer`
- for persistent realtime or duplex flows, the loop depends on
  `messages.SessionInferencer`
- provider adapters such as `go-llm-gateway` implement those contracts and plug
  into the loop through options

That means this module is reusable as a runtime library, but the current
workspace composition is still coupled in practice through the adapter layer:
`agent-cli` composes this module with `go-llm-gateway`, and
`go-llm-gateway` develops against the checked-out loop contracts.

## Validation Commands

Package-local validation:

```bash
cd go-agent-loop
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
make test-integration
make test-regressions
make build
make coverage
make validate
make ci
```

Use the module-local commands when changing this package in isolation. Use the
root commands when you want to verify the current cross-module workspace state.

## Development Notes

Start with [docs/development.md](./docs/development.md) before changing the
runtime, streaming behavior, ordering logic, or test harnesses. The functional
tests under `test/functional` exercise the consumer-visible runtime behavior and
are the best reference for current supported flows.
