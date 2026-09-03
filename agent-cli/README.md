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
./agent-cli/bin/yui --help
```

Or install the command into your Go bin directory:

```bash
go install github.com/portpowered/go-agent-harness/agent-cli/cmd/yui@v0.0.2
yui --help
```

Release binaries can also be downloaded from the repository releases page when
you want a prebuilt executable instead of a local build.

## First Run

The CLI reads configuration from `~/.agent-cli/config.yaml` by default. Use
`--config-dir` to point at a different workspace directory.

If you already have a remote provider configuration, a minimal first command is:

```bash
yui ask "what is 2 + 2?"
```

If you want to point the CLI at a local OpenAI-compatible server such as
Ollama, llama.cpp, LM Studio, or vLLM, bootstrap the config with:

```bash
yui config add-local --base-url http://127.0.0.1:11434/v1 --model llama3.1
yui ask "summarize the workspace"
```

## Supported CLI Surface

These are the supported consumer-facing command groups:

| Command | Use it for |
| --- | --- |
| `yui ask [prompt] [files...]` | One-shot prompts, optional file inputs, stdin piping, JSON output, and iterative `--loop` runs. |
| `yui chat` | Interactive multi-turn chat, with optional audio input and iterative loop mode. |
| `yui tool <tool-id> key=value...` | Direct tool invocation for debugging tool behavior outside a full model run. |
| `yui probe acceptance <binary> <goal>` | Run one blind, artifact-backed acceptance probe in a fresh empty directory. |
| `yui probe customer-simulation --live ...` | Run the explicitly billed conversational customer-simulation suite and write sanitized reports plus finalized evidence bundles. |
| `yui session ...` | Live session capture, offline replay, and stored session inspection via `show`, `list`, and `delete`. |
| `yui config add-local ...` | Write a local provider entry into the CLI config for OpenAI-compatible local inference servers. |

Common starting flows:

```bash
# One-shot prompt
yui ask "describe the current directory"

# Prompt with files
yui ask "describe this image" ./example.png

# Continue the most recent saved session
yui ask "continue from the previous answer" --continue-last-session

# Interactive chat
yui chat

# Tool debugging
yui tool read_file path=./README.md

# Blind acceptance probe (the probe receives only the binary, goal, and empty cwd)
yui probe acceptance ./probe-agent "Create the requested result"

# Billed conversational customer simulation; see docs/customer-simulation-live.md
yui probe customer-simulation --live --required --audio-dir /absolute/path/to/audio

# Provider-neutral interaction fixture replay
yui interaction replay fixtures/demo.interaction.json

# Session management (sessions are stored in workspace/sessions/)
yui session --record capture.json --provider grok --model <session-model> --api-key <xai-api-key>
yui session --record openai.session.json --provider openai --model gpt-realtime-2.1 --reasoning-effort low --audio-in prompt.wav --audio-out response.wav
yui session --replay capture.json
yui session show <session-id>
yui session list                                      # newest 100 (default)
yui session list --limit 20 --since 2026-08-31T00:00:00Z --filter billing
yui session delete <session-id>
```

To preserve what is actually sent to the selected speaker path for debugging,
combine `--audio-out-device` with `--audio-out`. The WAV uses the negotiated
device rate and records PCM after gain, resampling, pacing, and cancellation
admission:

```bash
yui session --audio-out-device default --audio-out device-output.wav --record session.json
```

`yui session list` accepts composable `--limit` (1–1000), `--since` (RFC3339
file modification time), and case-insensitive literal `--filter` (session ID).

Run `yui --help` or `yui <command> --help` for the full flag surface.

## Configuration And Workspace

```text
~/.agent-cli/
  config.yaml
  sessions/
```

- `config.yaml` selects the provider, model, credentials, and tool settings.
- `sessions/` stores conversation history and loop trace records.

An existing `AGENTS.md` under `--workdir` supplies workspace instructions. If
it is absent, Yui sends no default system prompt and does not generate the file.

Interaction replay reads a normalized PNIG fixture and prints one JSON object
per event to stdout. It is credential-free and does not call live provider
endpoints.

Session replay reads a JSON capture file and does not make live provider
network calls. Session record mode supports live Grok realtime captures and
OpenAI Realtime captures with `--provider openai --model gpt-realtime-2.1`; it
validates the provider, model, API key, and `.json` capture path before
attempting the live provider path. OpenAI session mode uses the sessional
inferencer path and does not call the normal `yui ask` or `yui chat`
stateless OpenAI inference path.

Realtime reasoning can be selected with `--reasoning-effort` (`minimal`,
`low`, `medium`, `high`, or `xhigh`). Host screen and pointer tools are hidden
unless `--computer-use` is supplied. `load_skill`, `sleep`, `web_fetch`, and
`web_search` require `--experimental-tools`. The default catalog contains only
the built-in shell and filesystem tools; use `--no-terminal-tools` to hide
those as well.

See [PNIG Interaction Replay](docs/interaction-replay.md) for the normalized
interaction fixture workflow and NDJSON output contract.

See [Agent Session Record and Replay](docs/session-record-replay.md) for the
full workflow, capture format, replay divergence errors, and fixture
sanitization guidance.

See [Interactive Voice Tool Latency Contract](docs/session-interactive-tool-timeouts.md)
for the built-in voice tool timeout catalog, display admission rule, timing
boundaries, and live confirmation procedure.

CLI flags such as `--provider`, `--model`, `--api-key`, and `--config-dir`
override config-file values for the current command.

## Validation

Package-local validation:

```bash
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

Use the root targets when you want to confirm `agent-cli` still composes cleanly
with `go-agent-loop` and `go-llm-gateway` in the active workspace.

## Composition Boundaries

`agent-cli` is the executable layer of this repository:

- It depends on `go-agent-loop` for the core agent execution model.
- It depends on `go-llm-gateway` for provider implementations and loop-facing
  inferencer adapters.
- It owns constructor-time dependency choices that should not be hidden inside
  the reusable libraries, including tool executor wiring for loop construction
  and stateless provider HTTP runtime wiring for live, record, and replay
  modes.
- Its supported surface is the `yui` command and the user documentation under
  `docs/`, not the internal package layout under `internal/`.
- System prompt resolution is CLI-owned composition. `--system-prompt` reads an
  existing file path or treats a value that does not resolve to an existing
  entry as literal prompt text; the
  default path reads `AGENTS.md` from the workspace only when it exists and
  otherwise supplies no system prompt or side effect. Runtime system information
  and discovered skill metadata are appended only when a base prompt exists.

Consumers who need a library integration point should start with
[`go-agent-loop`](../go-agent-loop/README.md) or
[`go-llm-gateway`](../go-llm-gateway/README.md) instead of importing
`agent-cli/internal/...`.

For the constructor ownership boundary specifically:

- `go-agent-loop` callers explicitly choose tool capability with
  `WithToolExecutor(...)` or `WithToolExecutionDisabled()`.
- `agent-cli/internal/agent` computes one shared `*http.Client` runtime for
  stateless providers before provider construction, then injects it through
  `ProviderBuildContext` so provider builders do not assemble record/replay
  transport policy themselves.
- `agent-cli/internal/agent.Executor.LoadSystemPromptWithDetails(...)` is the
  additive inspection contract for prompt-resolution composition tests. It
  returns the resolved prompt plus the prompt sources and CLI-owned side effects
  consulted during resolution while `LoadSystemPrompt(...)` remains compatible
  for existing callers.

## Further Reading

- [Agent CLI docs index](docs/README.md)
- [Agent session record and replay](docs/session-record-replay.md)
- [Agent CLI development guide](docs/development.md)
