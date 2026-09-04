# go-agent-harness

`go-agent-harness` is a voice-agent runtime and CLI with realtime audio,
tools, provider integrations, and structured WebMCP browser control.

## Install

Download the `yui` binary for macOS, Linux, or Windows from the
[latest release page](https://github.com/portpowered/go-agent-harness/releases/latest)
and put it on your `PATH`.

## Set your OpenAI API key

```bash
export AGENT_MODEL__OPENAI__API_KEY="your-openai-api-key"
```

## Start talking

```bash
yui session
```

This starts a live OpenAI Realtime voice session using your microphone and
speakers. Grant microphone access when your operating system asks.

## Give the agent a browser

```bash
yui session --browser-tools webmcp
```

This opens a Chrome browser that the agent can control. It includes default
built-in functions for Google Maps, YouTube, Wikipedia, Spotify, and Reddit,
and it can use the structured tools provided by any website with WebMCP built
in. The agent can open pages, switch tabs, discover each page's available
tools, and perform supported actions through conversation.

To let the agent cast the selected browser tab to a Google Cast device:

```bash
yui session --browser-tools webmcp --web-cast
```

Casting may require local-network permission from your operating system.

## Packages

- [`agent-cli`](./agent-cli/README.md): the `yui` executable.
- [`go-agent-loop`](./go-agent-loop/README.md): the tick-driven agent runtime.
- [`go-llm-gateway`](./go-llm-gateway/README.md): provider-neutral inference
  and realtime session gateways.

## License

Licensed under the [MIT License](./LICENSE). Copyright © 2026 Port OS.
