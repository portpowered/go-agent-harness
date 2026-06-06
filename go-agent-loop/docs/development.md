# Go Agent Loop Development Guide

This guide is the package-local contributor guide for `libraries/go-agent-loop`. Read it before changing the agent runtime, messages, streaming protocol, buffers, or deterministic engine behavior, then apply the shared [Libraries Development Guide](../../../docs/processes/libraries-development.md) and [Library Standards](../../../docs/standards/systems/library-standards.md).

## Purpose

`go-agent-loop` is the portable agent runtime used by higher-level Port OS agent tooling. It coordinates an `Inferencer`, a `ToolExecutor`, shared messages, streaming deltas, interrupts, and deterministic tick-based execution.

## Local Architecture

- `pkg/agentloop/` is the public execution API.
- `pkg/messages/` owns the shared message and streaming delta model used across agent libraries.
- `pkg/engine/` owns the tick-based runtime and buffer-oriented execution model.
- `test/functional/` contains deterministic harness coverage.
- `docs/ORDERING.md` documents ordering behavior.
- `NOTES.md` captures deeper concurrency and engine design notes.

## Development Commands

Run commands from `libraries/go-agent-loop`.

```bash
make build
make test
make test-race
make fmt
make vet
make deps
make deps-tidy
```

## Package-Specific Verification

1. Run `make test` for normal runtime, message, and tool-loop changes.
2. Use deterministic mocks from the functional test harness instead of live provider calls.
3. Run `make test-race` when changing buffers, tick scheduling, interrupt handling, or concurrent tool execution.
4. If `pkg/messages` changes any exported message, content part, delta, or stream contract, also run the affected checks for `go-llm-gateway` and `agent-cli`.
5. Update `docs/ORDERING.md` or `NOTES.md` when behavior changes the documented tick order, buffer interaction, or concurrency model.

## Local Gotchas

- `messages.Message` is the canonical shared message model for `go-agent-loop`, `go-llm-gateway`, and `agent-cli`; contract changes have downstream impact.
- The engine is deterministic by design. Avoid timing-dependent tests when manual ticks or mock inferencers can prove the behavior.
- Streaming output uses ordered delta events for text, tool calls, reasoning, audio, image, video, file, message boundaries, loop lifecycle, and errors.
- The runtime communicates through actor buffers. Changes that appear local to one participant can change user, model, tool, and kernel ordering.
- Duplex session runners should emit a shared `SESSION.CLOSE` delta when the provider session `Done()` channel closes without a prior close event so consumers do not need provider-specific EOF handling.

## Related Docs

- [Go Agent Loop README](../README.md)
- [Ordering](ORDERING.md)
- [Engine Notes](../NOTES.md)
- [Agent Loop Intent](../../../docs/intents/agent-loop.md)
- [Library Standards](../../../docs/standards/systems/library-standards.md)
- [Golang Standards](../../../docs/standards/code/golang-standards.md)
