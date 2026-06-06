# GO AGENT CLI

The go agent cli allows you to run an LLM agent from the command line.

Examples:

`agent ask "what is 2 + 2?"`

`agent ask ./recording-stream.mp4 "is that a fish or a banana?" --model "google/gemini-3.1-pro-preview" --api-key "<api-key>" --provider "openrouter"`

agent cli is a binary with tools for filesystem/shell/web access, multimodality, streaming, and reasoning.

## Quickstart

```bash
curl -L https://github.com/portpowered/go-agent-cli/releases/latest/download/agent-cli-linux-amd64 -o agent
chmod +x agent
cp agent /usr/local/bin/agent
agent ask "what is 2 + 2?"
```

> **Note:** The quickstart uses the Linux amd64 binary. Check releases for other platforms (Windows, macOS, arm64). Replace placeholder values like `<api-key>` with your actual credentials.

For more commands see `./agent --help`

## Commands

```bash
# Ask command variants
agent ask "describe this picture for me" ./file.jpg --stream
agent ask "describe this picture for me" ./file.jpg --system-prompt ~/prompts/AWESOME_PICTURE_DESCRIBER_PROMPT.md
agent ask "are you sure its not a banana though?" --continue-last-session
agent ask "what about bananas though?" --session-id <session-id>
agent ask "can you read the files in x directory and tell me?" --model <model-id> --provider <provider-id> --show-tool-use

# Interactive chat
agent chat

# Tool testing - invoke a tool directly by name and key=value args (for debugging)
agent tool <tool-id> "key=param" "key2=param2" ...

# Session management (sessions are stored in workspace/sessions/)
agent session --record capture.json --provider grok --model <session-model> --api-key <xai-api-key>
agent session "hello" --record openai.session.json --provider openai --model gpt-realtime --api-key <openai-api-key>
agent session --replay capture.json
agent session show <session-id>
agent session list
agent session delete <session-id>
```

CLI flags like `--api-key` and `--model` override values from the config file.

Session replay reads a JSON capture file and does not make live provider network calls. Session record mode supports live Grok realtime captures and OpenAI Realtime captures with `--provider openai --model gpt-realtime`; it validates the provider, model, API key, and `.json` capture path before attempting the live provider path. OpenAI session mode uses the sessional inferencer path and does not call the normal `agent ask` or `agent chat` stateless OpenAI inference path.

See [Agent Session Record and Replay](docs/session-record-replay.md) for the full workflow, capture format, replay divergence errors, and fixture sanitization guidance.

## Documentation

See the [Agent CLI docs index](docs/README.md) for local guides grouped by CLI users, fixture and test authors, and Agent CLI contributors.

Contributors should start with the [Agent CLI Development Guide](docs/development.md). It covers the local package layout, build and test commands, Wire generation, session verification, and CLI-specific gotchas.

## Configuration

The agent cli is configured via a YAML file at `~/.agent-cli/config.yaml`.
The config directory can be overridden with `--config-dir` (e.g. `./agent --config-dir /path/to/config-dir ask "what is 2 + 2"`).

Format:

```yaml
model:
  provider: openrouter
  openai:
    model: gpt-realtime
    api_key: sk-...
    base_url: wss://api.openai.com/v1/realtime
  openrouter:
    model: z-ai/glm5
    api_key: sk-or-v1-...
  grok:
    model: <session-model>
    api_key: xai-...
    base_url: https://api.x.ai/v1/realtime
tools:
  web:
    brave:
      enabled: true
      api_key: "your-brave-key"
  exec:
    enable_deny_patterns: true
```

Set `model.provider: openai` with `model.openai.model: gpt-realtime` for OpenAI Realtime `agent session --record` runs. Set `model.provider: grok` for Grok realtime recording. Replay mode reads the capture and does not require live provider credentials.

API keys are stored on disk. Do not commit `config.yaml` or share it with your API keys. Prefer environment variables (e.g. `AGENT_MODEL__OPENROUTER__API_KEY`, `AGENT_MODEL__GROK__API_KEY`) for CI or shared machines.

## Workspace

The agent uses a workspace for agent context (AGENTS.md, skills, sessions). The default workspace is `~/.agent-cli/`.

Layout:

```
~/.agent-cli/
    config.yaml     # main config (model, tools, etc.)
    AGENTS.md   # instructions for the agent
    sessions/   # conversation history (session-1.json, etc.)
```

## Why?

Current tools don't support multimodality as necessary, are tied to a specific provider, or require TypeScript/Python bootstrap. We don't yet need the weight of most claw products (openclaw, nanoclaw, picoclaw) for exposing a server for message receiving.

The intent of this CLI is not for code, it's for running a general-purpose agent.

---

## Reference

### Model reference (as of Feb 2026)

| Provider  | Model             | Input modalities            | Output modalities |
|----------|-------------------|-----------------------------|-------------------|
| OpenAI   | gpt-realtime      | text, audio                 | text, audio       |
| OpenAI   | gpt-5.2           | text, image, file           | text              |
| OpenAI   | gpt-audio         | text, audio                 | text, audio       |
| anthropic| claude-4.6-sonnet | text, image                 | text              |
| glm      | glm-5             | text,                       | text              |
| google   | gemini-3.1-preview| audio, file, image, text, video | text          |
| google   | nano-banana       | image, text                 | image, text       |
| kimi     | kimi-2.5          | text, image                 | text              |
| qwen     | qwen-3.5          | text, image, video          | text              |
| minimax  | minimax-2.5       | text                        | text              |

### Comparative tools

**Coding agent CLIs**

- https://www.npmjs.com/package/@sourcegraph/amp
- https://code.claude.com/docs/en/overview
- https://github.com/openai/codex/tree/main

**General-purpose agents with server access**

- https://github.com/openclaw/openclaw
- https://github.com/sipeed/picoclaw
- https://github.com/zeroclaw-labs/zeroclaw

---

## Project structure (contributors)

- `doc/` - documentation
- `internal/` - internal Go packages
- `test/` - test suite
- `bin/` - command-line binary target
