# Agent Session Record and Replay

---
author: Codex
owner: Agent CLI maintainers
last modified: 2026, April, 11
---

Use `agent session --record` to capture live Grok realtime or OpenAI Realtime session traffic. Use `agent session --replay` to run the same session traffic later without provider credentials or a live network connection.

## Prerequisites

- An Agent CLI build with the `agent session` command.
- Grok realtime credentials for Grok recording, or OpenAI credentials for OpenAI Realtime recording.
- A JSON capture file for replay.

Replay mode does not require live provider credentials. It reads provider traffic from the capture file and installs a replay dialer instead of a live WebSocket dialer.

## Configure Grok for Recording

Set Grok as the session provider in `~/.agent-cli/config.yaml`:

```yaml
model:
  provider: grok
  grok:
    model: grok-realtime
    api_key: xai-your-api-key
    base_url: https://api.x.ai/v1/realtime
```

You can also pass the provider settings as flags:

```bash
agent session --record captures/grok-demo.session.json --provider grok --model grok-realtime --api-key xai-your-api-key
```

The capture path must end with `.json`. Record mode validates the provider, model, API key, and capture path before starting session traffic.

## Configure OpenAI Realtime for Recording

OpenAI session mode is separate from `agent ask` and `agent chat`. It uses the sessional inferencer path only when the selected provider is `openai` and the selected model is `gpt-realtime`. Non-realtime OpenAI models fail before any network dial.

Set OpenAI as the session provider in `~/.agent-cli/config.yaml`:

```yaml
model:
  provider: openai
  openai:
    model: gpt-realtime
    api_key: sk-your-api-key
    base_url: wss://api.openai.com/v1/realtime
```

You can also pass the provider settings as flags:

```bash
agent session "hello from the CLI" --record captures/openai-demo.session.json --provider openai --model gpt-realtime --api-key sk-your-api-key
```

`model.openai.base_url` or `--base-url` overrides the OpenAI Realtime WebSocket endpoint. Omit it to use `wss://api.openai.com/v1/realtime`; the CLI adds the `model=gpt-realtime` query parameter when the URL does not already include `model`.

## OpenAI Realtime Event Flow

OpenAI Realtime sessions use raw WebSocket client and server events that the gateway normalizes into shared session messages for Agent CLI and Agent Loop consumers.

Typical client events include:

- `session.update` to send model, modalities, instructions, audio settings, turn detection, and tool definitions.
- `conversation.item.create` to append user input or tool results to the conversation.
- `response.create` to request model output when the session is not relying on server VAD to create responses automatically.

Typical server events include:

- `session.created` and `session.updated` lifecycle events.
- `response.output_text.delta` and `response.output_audio.delta` output events.
- Tool-call argument delta and completion events.
- `error` events that the CLI reports as session errors.

OpenAI Realtime server events are asynchronous. A fixture can deliver several server events before the next expected client event, and replay should preserve that order.

For current OpenAI model names and event details, verify provider behavior against the official [OpenAI Realtime guide](https://platform.openai.com/docs/guides/realtime) and [Realtime API reference](https://developers.openai.com/api/reference/realtime) before changing Agent CLI or gateway behavior.

## Record a Session

Record mode wraps the live provider WebSocket connection with a recording dialer. The capture includes client-to-server events and server-to-client events in one ordered JSON file.

Run a live Grok session and write the capture to a local file:

```bash
agent session "hello from the CLI" --record captures/grok-demo.session.json --provider grok --model grok-realtime --api-key xai-your-api-key
```

Run a live OpenAI Realtime session and write the capture to a local file:

```bash
agent session "hello from the CLI" --record captures/openai-demo.session.json --provider openai --model gpt-realtime --api-key sk-your-api-key
```

Do not commit a live capture until you have sanitized it. Session captures can contain sensitive text, audio payloads, prompts, model output, session identifiers, and provider metadata.

## Replay a Session

Replay a capture without opening a live provider connection:

```bash
agent session --replay captures/grok-demo.session.json
```

Replay mode uses the capture file as the provider transport. It accepts dummy or missing live credentials because all provider traffic comes from the capture. Raw WebSocket replay routes by `provider.name`: use `openai` for OpenAI Realtime fixtures and `grok` for Grok fixtures.

You can pass a prompt when the capture expects an outbound user event:

```bash
agent session "hello from the CLI" --replay captures/grok-demo.session.json
```

## Sanitize Before Committing

Before promoting a local recording into a test fixture:

1. Replace real user text with synthetic text.
2. Remove or replace audio payloads unless they are synthetic and safe to commit.
3. Remove secrets, tokens, authorization data, account identifiers, and internal URLs.
4. Replace real session IDs with values such as `sess_sanitized`.
5. Set `session.fixture_provenance` to the canonical value `synthetic` or `provider_recorded`.
6. Run the session fixture validator and replay tests against the sanitized file before committing it.

Committed Agent CLI session fixtures should live under `libraries/agent-cli/test/integration/testdata/` and should use clear names such as `session_text_reply.session.json`.

```bash
cd ../go-llm-gateway
go run ./cmd/session-fixture-validator ../agent-cli/test/integration/testdata
```

## Capture Fields

Session captures use a versioned JSON envelope:

```json
{
  "version": 1,
  "provider": {
    "name": "openai",
    "model": "gpt-realtime"
  },
  "session": {
    "id": "sess_sanitized",
    "started_at_utc": "2026-04-11T00:00:00Z",
    "fixture_provenance": "synthetic"
  },
  "records": []
}
```

Each item in `records` represents one traffic event:

| Field | Meaning |
|-------|---------|
| `sequence` | Logical order in the capture. |
| `direction` | `client_to_server` or `server_to_client`. |
| `timestamp_ms` | Milliseconds from session start. Replay can use this for timed playback. |
| `type` | Stream event type or provider WebSocket event type. |
| `payload_type` | `stream_message` for generic session events or `websocket_message` for raw provider wire events. |
| `payload` | Structured event payload. Treat it as sensitive until reviewed. |

Use `websocket_message` captures for OpenAI Realtime and Grok provider wire paths. OpenAI Realtime wire events should use provider event names such as `session.update`, `conversation.item.create`, `response.create`, `response.output_text.delta`, and `response.output_audio.delta`. Older generic session fixtures may use `stream_message`.

## Replay Divergence Errors

Replay validates outbound client events against the next expected `client_to_server` record. If the CLI sends a different event, sends it too early, or omits it, replay returns a divergence error.

Common causes:

| Error text | Likely cause | Fix |
|------------|--------------|-----|
| `session replay divergence at sequence ...` | The CLI sent an event that does not match the capture. | Re-run the command with the same prompt and settings used by the fixture, or update the sanitized fixture. |
| `session replay waiting for outbound event at sequence ...` | Replay reached a client event and needs the CLI to send it before provider events continue. | Pass the expected prompt or adjust the fixture ordering. |
| `session replay closed before expected outbound event ...` | The session ended before the expected client event was sent. | Check cancellation, prompt input, and fixture sequence order. |

For tests that only render a transcript and do not exercise client sends, disable outbound validation explicitly in the test helper. Do not disable validation for behavioral replay tests.

## Related Documentation

- `libraries/go-llm-gateway/pkg/testing/README.md` describes the lower-level recorder and replay dialer APIs.
- `libraries/agent-cli/README.md` lists the public Agent CLI commands and configuration file shape.
