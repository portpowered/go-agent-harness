# Agent CLI

`agent-cli` is the workspace's user-facing executable. It composes
`go-agent-loop` for the runtime loop and `go-llm-gateway` for provider access;
it is not a separate downstream Go library surface.

Start here when you want a ready-to-run agent binary for one-shot prompts,
interactive chat, direct tool debugging, or session capture and replay.

## Install

Build from the checked-out workspace:

```bash
make build
./bin/agent --help
```

Or install the command into your Go bin directory:

```bash
go install ./cmd/agent
agent --help
```

Release binaries can also be downloaded from the repository releases page when
you want a prebuilt executable instead of a local build.

## First Run

The CLI reads configuration from `~/.agent-cli/config.yaml` by default. Use
`--config-dir` to point at a different workspace directory.

If you already have a remote provider configuration, a minimal first command is:

```bash
agent ask "what is 2 + 2?"
```

If you want to point the CLI at a local OpenAI-compatible server such as
Ollama, llama.cpp, LM Studio, or vLLM, bootstrap the config with:

```bash
agent config add-local --base-url http://127.0.0.1:11434/v1 --model llama3.1
agent ask "summarize the workspace"
```

## Command Map

These are the supported consumer-facing command groups:

| Command | Use it for |
| --- | --- |
| `agent ask [prompt] [files...]` | One-shot prompts, optional file inputs, stdin piping, JSON output, and iterative `--loop` runs. |
| `agent chat` | Interactive multi-turn chat, with optional audio input and iterative loop mode. |
| `agent tool <tool-id> key=value...` | Direct tool invocation for debugging tool behavior outside a full model run. |
| `agent session ...` | Live session capture, offline replay, and stored session inspection via `show`, `list`, and `delete`. |
| `agent config add-local ...` | Write a local provider entry into the CLI config for OpenAI-compatible local inference servers. |

Common starting flows:

```bash
# One-shot prompt
agent ask "describe the current directory"

# Prompt with files
agent ask "describe this image" ./example.png

# Continue the most recent saved session
agent ask "continue from the previous answer" --continue-last-session

# Interactive chat
agent chat

# Tool debugging
agent tool read_file path=./README.md

# Replay a previously captured realtime session without live provider calls
agent session --replay ./captures/demo.session.json
```

Run `agent --help` or `agent <command> --help` for the full flag surface.

## Configuration And Workspace

The default workspace lives under `~/.agent-cli/`:

```text
~/.agent-cli/
  config.yaml
  AGENTS.md
  sessions/
```

- `config.yaml` selects the provider, model, credentials, and tool settings.
- `AGENTS.md` holds the default workspace instructions that the runtime injects.
- `sessions/` stores conversation history and loop trace records.

CLI flags such as `--provider`, `--model`, `--api-key`, and `--config-dir`
override config-file values for the current command.

For bidirectional session work, `agent session --record` currently supports live
Grok and OpenAI Realtime capture flows, while `agent session --replay` replays a
saved `.json` capture without contacting the provider.

## Validation

Use the module `Makefile` for deterministic package-local validation:

```bash
make deps
make build
make vet
make test
```

From the repository root, the shared workspace validation entrypoints are:

```bash
make typecheck
make lint
make test
make validate
```

Use the root targets when you want to confirm `agent-cli` still composes cleanly
with `go-agent-loop` and `go-llm-gateway` in the active workspace.

## Composition Boundaries

`agent-cli` is the executable layer of this repository:

- It depends on `go-agent-loop` for the core agent execution model.
- It depends on `go-llm-gateway` for provider implementations and loop-facing
  inferencer adapters.
- Its supported surface is the `agent` command and the user documentation under
  `docs/`, not the internal package layout under `internal/`.

Consumers who need a library integration point should start with
[`go-agent-loop`](../go-agent-loop/README.md) or
[`go-llm-gateway`](../go-llm-gateway/README.md) instead of importing
`agent-cli/internal/...`.

## Further Reading

- [Agent CLI docs index](docs/README.md)
- [Agent session record and replay](docs/session-record-replay.md)
- [Agent CLI development guide](docs/development.md)
