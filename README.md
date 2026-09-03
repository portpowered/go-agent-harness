# go-agent-harness

`go-agent-harness` is a cross-platform voice-agent runtime and CLI with realtime audio, tools, provider integrations, and structured WebMCP browser control.

## Install

Install the `yui` binary from the latest release with Go 1.26 or newer:

```bash
go install github.com/portpowered/go-agent-harness/agent-cli/cmd/yui@latest
yui --help
```

Prebuilt macOS, Linux, and Windows archives are available on the
[latest release page](https://github.com/portpowered/go-agent-harness/releases/latest).

This README tracks `main`. If a documented feature is newer than the latest
published release, install the current development binary with:

```bash
go install github.com/portpowered/go-agent-harness/agent-cli/cmd/yui@main
```

## Set your API key

The default live session uses OpenAI Realtime:

```bash
export OPENAI_API_KEY="your-openai-api-key"
```

For a persistent CLI configuration, use the environment variable understood by
the layered config loader:

```bash
export AGENT_MODEL__OPENAI__API_KEY="your-openai-api-key"
```

Do not commit API keys. You can also pass a key for one run with `--api-key` or
store it as `model.openai.api_key` in `~/.agent-cli/config.yaml`.

Other binary providers use the same nested environment-variable convention:

| Provider | Environment variable |
| --- | --- |
| OpenAI | `AGENT_MODEL__OPENAI__API_KEY` |
| OpenRouter | `AGENT_MODEL__OPENROUTER__API_KEY` |
| Grok/xAI | `AGENT_MODEL__GROK__API_KEY` |
| fal.ai | `AGENT_MODEL__FAL__API_KEY` |

## Start a voice session

Run the command from the directory the agent should be allowed to work in:

```bash
cd /path/to/your/project
yui --workdir "$PWD" session
```

This starts an OpenAI Realtime session using the default microphone, speakers,
model, voice, WebSocket transport, semantic VAD, and workspace tools. Press
Ctrl-C to stop it.

The process needs microphone access. Grant access to the terminal or host
application that launches `yui`:

- macOS: System Settings → Privacy & Security → Microphone.
- Windows: Settings → Privacy & security → Microphone.
- Linux: ensure the user can access the active PipeWire, PulseAudio, or ALSA
  devices.

The agent's filesystem tools are confined to `--workdir`. Add another explicit
root only when needed:

```bash
yui --workdir "$PWD" --allow-path /path/to/shared session
```

`--allow-path` is filesystem-tool scope, not an operating-system sandbox. The
`exec` tool runs commands as the current user. The optional `show` and `mouse`
desktop tools also require Screen Recording permission on macOS; ordinary voice
and WebMCP sessions do not.

## Start a WebMCP browser session

Add WebMCP to the default voice session. This starts a visible managed Chrome
window; the agent can open and select tabs itself:

```bash
yui --workdir "$PWD" session --browser-tools webmcp
```

To begin on a specific page, supply its startup URL:

```bash
yui --workdir "$PWD" session \
  --browser-tools webmcp \
  --browser-open https://cubecade.openai.chatgpt.site/
```

The agent opens and manages a local Chrome instance, can create and switch tabs,
discovers each page's structured WebMCP tools, and closes only browsers it owns
when configured to do so. No CDP port, extension, browser profile, screenshot
permission, or secondary browser configuration is required. A page must expose
WebMCP tools for structured page operations.

Use `--browser-close-on-exit` if the managed browser should close with the
session. Write-like page operations require approval by default; set
`--browser-approval always`, `writes`, or `never` to choose the policy.

Add `--web-cast` alongside `--browser-tools webmcp` to let the agent discover
Google Cast receivers visible to Chrome, cast the exact selected tab, and stop
that Cast session. Cast discovery runs on the browser host's local network and
may require operating-system local-network permission. Agent-managed Chrome
starts receiver discovery eagerly, without requiring the customer to open
Chrome's Cast menu first:

```bash
yui --workdir "$PWD" session --browser-tools webmcp --web-cast
```

## Configuration

Configuration precedence is defaults, then `~/.agent-cli/config.yaml` when it
exists, then `AGENT_...` environment variables, then command-line flags. The
CLI does not create a workspace prompt when one is absent.

### AGENTS.md

Put `AGENTS.md` in the directory selected by `--workdir` when you want custom
session instructions. Its contents become the session's workspace prompt. If
the file is missing and `--system-prompt` is not supplied, Yui sends no system
prompt and does not create a file.

```markdown
# Session instructions

- Keep spoken answers short.
- Inspect files before changing them.
- Confirm destructive operations before running them.
- For browser tasks, use structured WebMCP tools instead of screenshots.
```

Override it for one run with a file or literal prompt:

```bash
yui --workdir "$PWD" session --system-prompt ./VOICE_AGENT.md
```

Copyable command-specific examples, including audio, tool, WebMCP, and room
guidance based on OpenAI's Realtime prompting recommendations, are indexed in
[`docs/references`](./docs/references/README.md). The upstream source is the
[official Realtime prompting guide](https://developers.openai.com/api/docs/guides/realtime-models-prompting).

### Voice

Select an OpenAI Realtime voice with `--voice`:

```bash
yui --workdir "$PWD" session --voice marin
```

Supported voices are `alloy`, `ash`, `ballad`, `cedar`, `coral`, `echo`,
`marin`, `sage`, `shimmer`, and `verse`.

### Model

The default session model is `gpt-realtime-2.1-mini`. Override it per run:

```bash
yui --workdir "$PWD" session --model gpt-realtime-2.1
```

Or persist the session model:

```yaml
session:
  provider: openai
  model: gpt-realtime-2.1-mini
```

### Useful settings

A practical `~/.agent-cli/config.yaml` can look like this:

```yaml
model:
  provider: openai
  openai:
    model: gpt-4o

session:
  provider: openai
  model: gpt-realtime-2.1-mini
  transport: ws
  vad:
    enabled: true
    type: semantic_vad
    eagerness: low
    create_response: true
    interrupt_response: true
  input_transcription:
    enabled: true

browser:
  policy:
    approval: writes
    cancel_on_interrupt: read-only
  managed:
    close_on_exit: true

tools:
  list:
    - id: exec
      enabled: true
    - id: write_file
      enabled: true
```

Other useful command-line settings include:

- `--audio-in-device` and `--audio-out-device` to select audio devices.
- `--audio-device-server host:port` to use a mock or remote audio device.
- `--max-duration 10m` to bound a session.
- `--no-input-transcription` to disable user-speech transcription.
- `--record capture.json` to record provider traffic.
- `--record-dir ./recording` to write a complete diagnostic bundle.
- `--replay capture.json` to replay a session without a live provider call.
- `-C /path/to/config-dir` to use a separate config directory.

Run `yui session --help` for the complete option list.

## Features

### Voice and audio

- **EAC:** native Apple Voice Processing I/O provides acoustic echo
  cancellation, noise suppression, and automatic gain control on supported
  macOS duplex devices. Cross-platform feedback guards prevent assistant audio
  from being treated as new customer speech.
- **Barge-in:** customer speech can interrupt an active response, cancel model
  output, discard queued playback, and truncate the provider conversation at
  the audio the customer actually heard.
- **VAD:** OpenAI live microphone sessions use provider-side `semantic_vad` by
  default, which is more tolerant of hesitation and fillers such as “umm.”
  `server_vad` remains configurable for silence/threshold-based turns.
- **Voice configuration:** ten selectable OpenAI Realtime voices, configurable
  per session.
- **Audio devices:** default or named devices on macOS, Linux, and Windows,
  plus a network audio-device server for deterministic testing.
- **Audio conversion and pacing:** PCM16 streaming, sample-rate conversion,
  bounded playback queues, and device-paced output.

### Agent runtime

- Live, multi-turn speech sessions with text, audio, image, and tool events.
- Workspace instructions through `AGENTS.md` and reusable skills from
  workspace/config `skills/` directories.
- Filesystem confinement with explicit workdir and additional allowed roots.
- Session recording, complete diagnostic bundles, offline replay, and stored
  session inspection.
- One-shot `yui ask`, interactive `yui chat`, direct `yui tool` debugging,
  and acceptance/customer-simulation probes.
- Tick-driven agent loop and provider gateway packages for custom Go agents.

### WebMCP browser control

- Agent-managed Chrome with one-command startup.
- Structured page-tool discovery and invocation without screenshot parsing.
- Multiple-page selection, origin allow/deny rules, write approvals, bounded
  invocation sizes/timeouts, interrupt cancellation, and semantic recording.
- External CDP/WebSocket attachment remains available for advanced setups.

### Providers

| Provider | Binary surface | Status |
| --- | --- | --- |
| OpenAI | Stateless inference and Realtime voice sessions | Production-supported |
| OpenRouter | Stateless inference through the OpenAI-compatible API | Production-supported |
| Grok/xAI | Realtime voice sessions | Experimental |
| Local OpenAI-compatible servers | Stateless inference with a custom base URL | Experimental; compatibility depends on the server |
| fal.ai | Media-oriented stateless inference | Experimental |

The reusable `go-llm-gateway` module also contains Anthropic and Gemini
adapters. They are library integrations and are not currently selectable by the
`yui` binary.

### Tools

The default session surface contains shell and filesystem tools. Desktop
computer-use tools and experimental network/skill utilities stay hidden until
their explicit flags are supplied, which keeps the model's tool choice focused.

| Tool | Purpose |
| --- | --- |
| `read_file`, `read_image`, `list_dir` | Inspect files, images, and directories inside the allowed filesystem scope. |
| `write_file`, `edit_file`, `append_file` | Create or modify files inside the allowed filesystem scope. |
| `exec` | Run a shell command as the current user. |
| `web_search`, `web_fetch` | Search the web or fetch readable URL content; requires `--experimental-tools`. |
| `show`, `mouse` | Inspect and control the desktop when supported; requires `--computer-use`. |
| `load_skill` | Load detailed instructions and resources from an installed skill; requires `--experimental-tools`. |
| `sleep` | Wait for an external operation for a bounded duration; requires `--experimental-tools`. |
| `webmcp_open_tab`, `webmcp_list_tabs`, `webmcp_select_tab` | Open, discover, and select browser tabs; requires `--browser-tools webmcp`. |
| `webmcp_get_context`, `webmcp_list_tools`, `webmcp_invoke`, `webmcp_cancel`, `show_page` | Inspect or operate the selected WebMCP page; requires `--browser-tools webmcp`. |
| `webmcp_list_cast_devices`, `webmcp_cast_tab`, `webmcp_stop_casting` | Discover Cast receivers, cast the exact selected tab, or stop casting; additionally requires `--web-cast`. |
| WebMCP page tools | Dynamically discovered structured tools supplied by the active browser page. |

The model receives the enabled tool definitions and chooses when to call them.
Run a built-in tool directly for debugging with:

```bash
yui --workdir "$PWD" tool read_file path=README.md
```

## Packages

- [`agent-cli`](./agent-cli/README.md): the `yui` executable.
- [`go-agent-loop`](./go-agent-loop/README.md): the tick-driven agent runtime.
- [`go-llm-gateway`](./go-llm-gateway/README.md): provider-neutral inference
  and realtime session gateways.

## License

Licensed under the [MIT License](./LICENSE). Copyright © 2026 Port OS.
