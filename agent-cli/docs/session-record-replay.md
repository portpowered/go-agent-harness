# Agent Session Record and Replay

---
author: Codex
owner: Agent CLI maintainers
last modified: 2026, August, 28
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

## Stop a Session with Ctrl-C

Press Ctrl-C once to stop an active `agent session` run. The CLI treats this
SIGINT as intentional user cancellation, including when a tool is running or
the provider has already accepted a tool result and is preparing the next
response. The process exits with status `0` and emits one terminal summary:

```text
[session terminal: classification=user_cancelled terminal_reason=cancellation terminal_provenance=cli output_state=partial]
```

`output_state` is `none` when no assistant output was admitted and `partial`
when output was admitted before Ctrl-C. A cancellation summary is not a
provider failure: the CLI does not invent a tool result, continuation, or
scheduled-turn completion for work that was still pending. Diagnostics identify
pending work as canceled by the user, while already accepted tool calls remain
visible in the recording.

When `--record-dir` is enabled, cancellation still finalizes the recording
directory. The final bundle contains the JSONL artifacts and `manifest.json`;
the manifest records the terminal classification and hashes of the finalized
artifacts. Temporary staging content is removed, so a Ctrl-C does not leave a
partial bundle that looks complete.

The clean-cancellation classification applies only to the CLI-owned SIGINT
intent. Parent-context cancellation, an unexpected provider close, malformed
provider data, tool failures, and independent recording or transport errors
retain their normal failure classification and process status. Inspect the
terminal fields before treating a non-zero exit as a user stop.

For a reproducible local check, use a sanitized fixture or replay capture and
keep credentials out of the command and any evidence:

```bash
agent session --replay captures/openai-demo.session.json \
  --record-dir captures/openai-cancelled
```

If a live confirmation is required, use a short run with a test account,
record it, and redact prompts, outputs, authorization headers, session IDs,
and provider URLs before sharing the result. Do not commit the live capture or
the command's secret-bearing environment.

## Replay a Session

Replay a capture without opening a live provider connection:

```bash
agent session --replay captures/grok-demo.session.json
```

Replay mode uses the capture file as the provider transport. It accepts dummy or missing live credentials because all provider traffic comes from the capture. Raw WebSocket replay routes by `provider.name`: use `openai` for OpenAI Realtime fixtures and `grok` for Grok fixtures.

For raw provider captures, the first outbound `session.update` is the replay
handshake. Replay uses that captured payload as the authoritative provider
configuration, including any recorded instructions, tool schemas, model,
modalities, voice, audio settings, or turn-detection fields. The current
workspace and tool configuration still owns local tool execution, but it does
not rewrite the provider handshake or grant implementations that are not
currently allowlisted. Every outbound event after the handshake remains an
ordered strict comparison against the capture, so changed prompts, audio,
tool results, extra events, and omitted events still fail replay.

The handshake must be a usable captured `session.update`; missing or malformed
initial configuration fails before a provider session starts and includes the
capture path and reason in the error.

You can pass a prompt when the capture expects an outbound user event:

```bash
agent session "hello from the CLI" --replay captures/grok-demo.session.json
```

For an OpenAI Realtime capture whose first post-handshake client actions are
one user `conversation.item.create` containing a single `input_text` part and
one `response.create`, a bare replay reuses that recorded prompt automatically:

```bash
agent session --replay captures/openai-demo.session.json
```

The strict replay transport still validates the generated client frames in
capture order. A successful capture-derived prompt replay ends with exactly
one `[session replay complete]` line; a failed replay does not print that
success marker. Supplying `--prompt` (including `--prompt=`) remains an
explicit prompt and continues to use strict mismatch validation.

### Reproduce a prompt

Record through the normal CLI boundary, then replay the produced capture with
the same prompt. The replay command does not need `--api-key` or a provider
network endpoint:

```bash
agent session "hello from the CLI" \
  --record captures/prompt.session.json \
  --provider openai --model gpt-realtime --api-key "$OPENAI_API_KEY"

agent session "hello from the CLI" \
  --replay captures/prompt.session.json
```

### Reproduce scheduled spoken turns

`--audio-in-turn` accepts finite WAV/PCM inputs and is repeatable. It requires
`--record-dir`, which keeps the complete turn and audio sidecar in one
persistent session. The raw JSON capture can be recorded alongside it and
replayed without a key:

```bash
agent session \
  --record captures/spoken.session.json \
  --record-dir captures/spoken-recording \
  --provider openai --model gpt-realtime --api-key "$OPENAI_API_KEY" \
  --audio-in-turn fixtures/turn-1.wav \
  --audio-in-turn fixtures/turn-2.wav

agent session \
  --replay captures/spoken.session.json \
  --record-dir captures/spoken-replay \
  --audio-in-turn fixtures/turn-1.wav \
  --audio-in-turn fixtures/turn-2.wav
```

### Diagnose a corrupted capture

After copying a capture for a negative test, mutate a nested field in the
JSON payload (the capture itself remains untouched):

```bash
jq '(.records[] | select(.direction == "client_to_server" and .type == "conversation.item.create") | .payload.item.content[0].text) = "CORRUPTED_PROMPT"' \
  captures/prompt.session.json > captures/prompt-corrupt.session.json
agent session --replay captures/prompt-corrupt.session.json "hello from the CLI"
```

The non-zero replay error identifies the capture event and first differing
field, for example:

```text
replay mismatch: expected event type "conversation.item.create" at sequence 4, actual event type "conversation.item.create" at sequence 4: JSON pointer /item/content/0/text: expected "CORRUPTED_PROMPT", actual "hello from the CLI"
```

Long values are escaped and bounded with `...(truncated)`. Malformed JSON
payloads report the zero-based byte offset instead of a JSON pointer. This
keeps the same strict post-handshake replay contract useful for both prompt
and spoken-turn diagnosis.

## Sanitize Before Committing

Before promoting a local recording into a test fixture:

1. Replace real user text with synthetic text.
2. Remove or replace audio payloads unless they are synthetic and safe to commit.
3. Remove secrets, tokens, authorization data, account identifiers, and internal URLs.
4. Replace real session IDs with values such as `sess_sanitized`.
5. Set `session.fixture_provenance` to the canonical value `synthetic` or `provider_recorded`.
6. Run the session fixture validator and replay tests against the sanitized file before committing it.

`go-llm-gateway/pkg/testing` is the authoritative owner for committed shared `.session.json` replay fixtures because it owns the replay format, replay helpers, and fixture hygiene validator. Promote sanitized shared fixtures into `go-llm-gateway/pkg/testing/testdata/session-fixtures/`.

Keep `agent-cli/test/integration/testdata/` only for CLI-private integration fixtures that are not the shared repository contract. If a CLI fixture becomes the canonical replay proof for multiple modules, copy or re-home the sanitized fixture into the gateway-owned shared root instead of asking sibling modules to read Agent CLI private `testdata`.

```bash
cd ../go-llm-gateway
go run ./cmd/session-fixture-validator ./pkg/testing/testdata/session-fixtures
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

Replay and record relay writes now follow the owned session lifecycle context. In the CLI session path, canceling the command context stops replay delivery and recorder relay writes at the same seam that owns dialer selection and provider runtime wiring.

## Reviewer Notes

The delivered session runtime ownership model for this Phase 2 slice is:

- `agent-cli/internal/services/session_runtime.go` owns session-mode config
  loading, live or replay dialer selection, and provider-specific runtime
  injection before provider construction begins.
- `go-llm-gateway/pkg/providers/grok` and
  `go-llm-gateway/pkg/providers/openai` consume injected session dialers and
  fail explicitly when that owned runtime dependency is missing.
- `go-llm-gateway/pkg/testing.SessionRecorder` and
  `go-llm-gateway/pkg/testing.SessionReplayer` honor the owned caller or
  session context for relay writes, so replay and record cancellation semantics
  stay aligned with the same runtime seam.
- The broader constructor-ownership lane is not fully converged yet: the
  record planner still falls back to a factory-owned live dialer when the
  caller omits `SessionRunOptions.WebSocketDialer`, so `P2-COB-04` remains
  open until that fallback is removed.

This slice resolves the scoped `HC-03` provider-constructor ownership issue
and the remaining session-helper portion of `CTX-02`, while narrowing but not
fully resolving `DI-04` in `docs/architecture/contract-gap-audit.md`.
Reviewers validating checklist advancement should cite `P2-SRO-04`, `P2-GATE-01`, the broader
constructor-ownership row `P2-COB-04`, and
`docs/internal/phase-2-session-runtime-ownership-validator.md`.

## Related Documentation

- `go-llm-gateway/pkg/testing/README.md` describes the lower-level recorder and replay dialer APIs.
- `agent-cli/README.md` lists the public Agent CLI commands and configuration file shape.
