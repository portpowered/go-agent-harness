# GPT-Live provider: protocol spec and integration design

---
status: proposed (design only, no code)
component: go-llm-gateway, go-agent-runtime, agent-cli
sources verified: 2026-10-02
---

This document has two parts.

- **Part 1** is a literal spec of the OpenAI GPT-Live protocol (`gpt-live-1`,
  released 2026-09-10), written from the official docs.
- **Part 2** is the design for a harness provider that speaks it.

GPT-Live is **not** the Realtime API. It uses its own endpoint
(`/v1/live/sessions`), its own event names (`session.*`), and it has no turn
detector and no response lifecycle. The model page lists `v1/realtime` as
"Not supported" for `gpt-live-1`. The existing `openai` realtime provider
therefore cannot simply be pointed at the new model.

## Sources

The OpenAI pages publish a raw Markdown version when `.md` is appended to the
URL. The quotes below come from those raw files, not from rendered summaries.

| Source | URL |
| --- | --- |
| Primary WebSocket reference (authoritative schema) | https://developers.openai.com/api/reference/resources/live/primary-websocket |
| Sideband WebSocket reference | https://developers.openai.com/api/reference/resources/live/sideband-websocket |
| Fork WebSocket reference | https://developers.openai.com/api/reference/resources/live/fork-websocket |
| Create session (WebRTC) reference | https://developers.openai.com/api/reference/resources/live/methods/create |
| Getting started | https://developers.openai.com/api/docs/guides/live |
| Managing sessions | https://developers.openai.com/api/docs/guides/live-conversations |
| Delegation and tools | https://developers.openai.com/api/docs/guides/live-delegation |
| Prompting | https://developers.openai.com/api/docs/guides/live-prompting |
| Migration from Realtime | https://developers.openai.com/api/docs/guides/live-migration |
| WebSockets (`?api=live`) | https://developers.openai.com/api/docs/guides/voice-websockets?api=live |
| WebRTC (`?api=live`) | https://developers.openai.com/api/docs/guides/voice-webrtc?api=live |
| Server-side controls / sideband (`?api=live`) | https://developers.openai.com/api/docs/guides/voice-server-controls?api=live |
| Telephony and SIP (`?api=live`) | https://developers.openai.com/api/docs/guides/voice-sip?api=live |
| Cost (`?api=live`) | https://developers.openai.com/api/docs/guides/voice-latency-cost?api=live |
| Model page | https://developers.openai.com/api/docs/models/gpt-live-1 |
| Azure Foundry event reference | https://learn.microsoft.com/en-us/azure/foundry/openai/gpt-live-reference |
| Azure Foundry how-to and delegation how-to | https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/gpt-live , https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/gpt-live-delegation |

Where OpenAI and Azure disagree, this document follows the OpenAI primary
WebSocket reference and records the conflict. **UNCONFIRMED** marks every fact
that no official page states.

---

# Part 1: Protocol spec

## 1.1 Model facts

Quoted from the model page:

- Model ID: `gpt-live-1`. Default snapshot: `gpt-live-1`. It is the only snapshot.
- "GPT-Live 1 is a full-duplex voice model for real-time conversations. It
  can listen and speak at the same time, and delegate reasoning and tool use
  to a backend agent."
- Input modalities: audio, text. Output modalities: audio, text.
  Unsupported modalities: image, video.
- Knowledge cutoff: Jul 31, 2025.
- Endpoints: only `v1/live/sessions` is supported. Chat Completions,
  Responses, Realtime (`v1/realtime`), Realtime translation, Realtime
  transcription and every other endpoint are "Not supported".
- Supported features: `streaming`, `function_calling`. Unsupported features:
  `structured_outputs`, `fine_tuning`, `predicted_outputs`.
- Pricing: "Voice sessions cost $0.05 per minute, billed per second. Backend
  model and tool usage is billed separately." Duration is not rounded up to a
  whole minute.
- Rate limits are counted in **concurrent sessions**:

  | Tier | Concurrent sessions |
  | --- | ---: |
  | Tier 1 | 25 |
  | Tier 2 | 50 |
  | Tier 3 | 200 |
  | Tier 4 | 300 |
  | Tier 5 | 500 |

  The Free tier is not supported.

## 1.2 Transports and endpoints

| Transport | How it starts | Audio carried by | JSON events carried by |
| --- | --- | --- | --- |
| Primary WebSocket | `wss://api.openai.com/v1/live/sessions`, then send `session.start` | `session.input_audio.append` / `session.output_audio.delta` (base64) | the same socket |
| WebRTC | `POST /v1/live/sessions` with `{session, transport:{type:"webrtc", sdp}}` | negotiated media tracks | data channel labelled `oai-events` |
| SIP inbound | webhook `live.transport.incoming`, then `POST /v1/live/sessions/{session_id}/accept` | SIP media | a sideband WebSocket |
| Sideband WebSocket | `wss://api.openai.com/v1/live/sessions/{session_id}/attach` | reflected copies only (see 1.9) | the sideband socket |
| Fork WebSocket | `wss://api.openai.com/v1/live/sessions/{source_session_id}/fork`, then send `session.start` with overrides | as for the primary socket | as for the primary socket |

Primary WebSocket connection rules, quoted from the reference: "No query
parameters. After connecting, send session.start with your model and session
configuration. Wait for session.started before sending audio." The model goes
in the `session` object, never in the URL: "do not pass it as a URL query
parameter".

Other session endpoints, all under `/v1/live/sessions/{session_id}`:

| Method and path | Purpose |
| --- | --- |
| `POST .../accept` | Accept an inbound SIP call. The body has a top-level `session` object. |
| `POST .../reject` | Reject a call, for example `{ "status_code": 486 }`. The status must be an integer from 300 to 699. |
| `POST .../refer` | Transfer the call: `{ "target_uri": "sip:agent@example.com" }`. |
| `POST .../hangup` | Hang up. No body. Returns `200 OK` with an empty body. |
| `POST .../fork` | Fork over WebRTC. The body is a new SDP offer; the answer comes back in `transport.sdp`. |
| `GET .../content` | Download the stored recording: "binary stereo WAV, with input audio in the left channel and output audio in the right channel". |

The Azure base is `/openai/v1/live` on the Foundry resource endpoint, for
example `wss://<resource>.openai.azure.com/openai/v1/live/sessions`. Its
sideband path is `.../live/sessions/{session_id}/attach`.

## 1.3 Authentication

- Primary, sideband and fork WebSockets: "Authenticate from your backend with
  your OpenAI API key in the `Authorization: Bearer $OPENAI_API_KEY` header.
  Keep the key on your server."
- A sideband must use "the project authentication that created or accepted
  the session" and must include "the same connection headers required when
  creating the session".
- WebRTC: the browser never holds a key. The application server calls
  `POST /v1/live/sessions` with the project key and returns the SDP answer to
  the browser.
- **UNCONFIRMED:** whether GPT-Live has an ephemeral client-secret flow like
  Realtime's. No Live page documents one. The WebRTC guide uses server-side
  session creation instead.
- **UNCONFIRMED:** whether any beta header (such as `OpenAI-Beta`) is needed.
  The WebSocket guide says "include the connection headers shown in the
  example", but that example uses the SDK and shows only the bearer token.
- Azure accepts "An API key or Microsoft Entra ID credentials". Its example
  sends `Authorization: Bearer ${accessToken}`. **UNCONFIRMED:** the Azure
  `api-key` header form.

## 1.4 Audio formats

Primary WebSocket: one format is set at startup in `session.audio.format`. It
applies to **both** input and output and cannot change during the session.
Quoted from the WebSocket guide:

- `{"type":"audio/pcm","rate":24000}`: mono signed 16-bit little-endian PCM at 24 kHz; **the default**.
- `{"type":"audio/pcm","rate":16000}`: mono signed 16-bit little-endian PCM at 16 kHz.
- `{"type":"audio/pcmu","rate":8000}`: G.711 μ-law at 8 kHz, one byte per sample.
- `{"type":"audio/pcma","rate":8000}`: G.711 A-law at 8 kHz, one byte per sample.

Rules:

- "Base64-encode raw bytes without a WAV or other container header. PCM chunks
  must contain complete 16-bit samples, so their byte length must be even."
  An odd-length payload fails with `invalid_audio` (Azure example in 1.11).
- Chunk boundaries are otherwise arbitrary, but the stream must be continuous
  and ordered.
- The server never resamples: "Changing the format setting does not convert
  your input bytes."
- On WebRTC and SIP the format is negotiated, so `audio.format` must be left
  out.
- Sideband reflected audio is always mono PCM16LE at 24 kHz, whatever the
  primary format is.
- The stored recording is a stereo WAV: input on the left, output on the right.

Conflicts and unknowns:

- Azure describes WebSocket audio as fixed at 24 kHz PCM16 and does not list
  16 kHz or G.711. The OpenAI reference lists all four formats.
- **UNCONFIRMED:** whether the server sends output audio at playback pace or
  in bursts faster than real time. This matters for local buffering; see 2.6.

## 1.5 Session config schema (`session.start.session`, `SessionConfig`)

The object is strict: Azure says it "rejects unknown fields", and OpenAI
returns `unknown_parameter`. Fields, from the primary WebSocket reference:

| Field | Type | Notes |
| --- | --- | --- |
| `model` | `string` or `"gpt-live-1"` | Required. Immutable. |
| `instructions` | `string` or `null` | "Frontend instructions for voice, conversation, interruptions, and when to delegate." Limited to **16,384** client-supplied tokens. "Omitted or blank instructions use server defaults." Immutable; extend it with `session.instructions.append`. |
| `audio.format` | `AudioPCM` `{type:"audio/pcm", rate:16000 or 24000}`, `AudioPCMU` `{type:"audio/pcmu", rate:number}` or `AudioPCMA` `{type:"audio/pcma", rate:number}` | Primary WebSocket only. Immutable. |
| `audio.output.voice` | built-in name, any string, or `CustomVoice {id}` | Defaults to `marin`. Immutable. |
| `client.data_channel.allowed_client_events` | `"all"` or `string[]` | WebRTC frontend permissions only. Omitting it allows everything. |
| `client.data_channel.allowed_server_events` | `"all"` or `ServerEventSelector[]` | A selector is `{type, response_event?}`. `response_event` is required when `type` is `"response.event"`. |
| `delegation` | `{type:"client"}`, `{type:"responses", responses:{...}}` or `null` | "Omitted or null selects your application", which means client delegation. Immutable except for the `responses` settings. |
| `input` | `InitialItem[]` | Text-only history: "at most 128 messages and 8,192 rendered tokens in total". Each message has exactly one text part. Developer and user messages use `input_text`; assistant messages use `text` or `output_text`. Fields per item: `content`, `role`, `id?`, `status?` (`incomplete` or `completed`), `type?` (`message`). |
| `store` | `boolean` | Stores the session for forking and recording download. Defaults to `false`. Treated as `false` under Zero Data Retention. Recordings are kept for 30 days. |

Built-in voice enum (31 names, plus `CustomVoice {id}`):
`alloy`, `ash`, `ballad`, `beacon`, `bossa`, `brise`, `cedar`, `cinder`,
`coral`, `delta`, `echo`, `flitz`, `gleam`, `harema`, `juni`, `marin`,
`meridian`, `nira`, `noeul`, `nuri`, `quartz`, `ripple`, `sage`, `shimmer`,
`shitan`, `sillage`, `stone`, `tempo`, `verse`, `vesper`, `willow`.

`delegation.responses` (`ResponsesDelegationConfig`):

| Field | Type |
| --- | --- |
| `model` | `string`, required at creation |
| `instructions` | `string` or `null` (the backend prompt, separate from the Live instructions) |
| `max_output_tokens` | `number` or `null` (at least 16 when set) |
| `parallel_tool_calls` | `boolean` or `null` |
| `reasoning` | `{effort?: "none" or "minimal" or "low" or "medium" or "high" or "xhigh", summary?: "concise" or "detailed" or "auto"}` or `null` |
| `service_tier` | `"auto"`, `"default"`, `"fast_tier_temp_pilot"`, `"flex"`, `"priority"` or `"ultrafast"`, or `null`. The guides document only `auto`, `default`, `flex` and `priority`. Fast mode uses `priority`. |
| `text` | `{verbosity?: "low" or "medium" or "high"}` or `null` |
| `tool_choice` | `"auto"`, `"none"` or `"required"`, or `{type:"function", name}`, or `{type:"mcp", name, server_label}` |
| `tools` | an array of `FunctionTool {type:"function", name, description?, parameters?, strict?}` or `{type:"web_search"}` |

Literal `session.start` example from the reference:

```json
{
  "type": "session.start",
  "event_id": "evt_start_001",
  "session": {
    "model": "gpt-live-1",
    "instructions": "Help the caller plan a restaurant reservation. Confirm details before booking.",
    "audio": {
      "format": {
        "type": "audio/pcm",
        "rate": 24000
      },
      "output": {
        "voice": "marin"
      }
    },
    "delegation": {
      "type": "client"
    }
  }
}
```

Fork overrides: a fork inherits the model, the original instructions and the
input. A WebSocket fork may override only `store`, the Responses delegation
settings and `audio.format`. Sending `{}` keeps the inherited settings. "Do
not supply a new model."

Fields that **do not exist** in GPT-Live and must never be sent:
`turn_detection`, `input_audio_transcription`, `tools` at the top level,
`modalities` or `output_modalities`, `tool_choice` at the top level, and
`type: "realtime"`. The strict schema rejects them. (A rendered TypeScript SDK
summary seen while researching listed `turn_detection` and
`input_audio_transcription`. The raw reference does not, so treat that summary
as wrong.)

## 1.6 Client events

Every client event has an optional `event_id: string or null`, used for
"correlating this command with a server event's client_event_id or
error.client_event_id".

| Event | Fields | Ack | Notes |
| --- | --- | --- | --- |
| `session.start` | `session: SessionConfig` | `session.started` | Must be the first message on a primary or fork socket. Never sent on WebRTC or on a sideband. |
| `session.update` | `session: {delegation?}` | `session.updated` | Sparse. Only the `delegation.responses` settings can change. The delegation type cannot change, and sending `null` to a Responses session fails with `immutable_field_update`. |
| `session.input_audio.append` | `audio: string` (base64) | none | Primary WebSocket only. |
| `session.input_audio.mute` | (none) | `session.input_audio.muted` | Stops sending input to the model. Output and delegations continue. |
| `session.input_audio.unmute` | (none) | `session.input_audio.unmuted` | |
| `session.instructions.append` | `content: string`, `delegation_id: string or null` (required) | `session.instructions.appended` | Trusted instructions. "An appended instruction can interrupt the model's current speech or behavior." |
| `session.thinking.append` | `content`, `delegation_id` (required) | `session.thinking.appended` | Quiet context: "does not directly request speech, but can influence later speech and is not a secrecy boundary." |
| `session.commentary.append` | `content`, `delegation_id` (required) | `session.commentary.appended` | Speakable context. The model "is trained to paraphrase the text". |
| `response.item.create` | `item`: any Responses input item | none | Responses delegation only. |
| `response.create` | (none) | none | Responses delegation only. Starts or continues the backend response. "does not accept a standalone Responses request body". |
| `session.close` | (none) | `session.closed` | Graceful shutdown. |

All three append events take "plain-string `content`, limited to 500 tokens
per append". `delegation_id` is required but nullable. `null` means general
session context. A non-null value "must identify a known client delegation",
and "Non-null IDs are not accepted with Responses delegation."

Literal examples from the reference:

```json
{
  "type": "session.update",
  "event_id": "evt_update_001",
  "session": {
    "delegation": {
      "type": "responses",
      "responses": {
        "instructions": "Check restaurant availability. Ask before confirming a booking.",
        "max_output_tokens": 1024
      }
    }
  }
}
```

```json
{
  "type": "session.input_audio.append",
  "audio": "AACAAIAAAIAAAP9/AIAAgA=="
}
```

```json
{
  "type": "session.input_audio.mute",
  "event_id": "evt_mute_001"
}
```

```json
{
  "type": "session.instructions.append",
  "event_id": "evt_instructions_001",
  "delegation_id": null,
  "content": "The caller prefers outdoor seating."
}
```

```json
{
  "type": "session.thinking.append",
  "event_id": "evt_thinking_001",
  "delegation_id": "del_abc123",
  "content": "Checking availability for two guests at 7 PM."
}
```

```json
{
  "type": "session.commentary.append",
  "event_id": "evt_commentary_001",
  "delegation_id": "del_abc123",
  "content": "There is an outdoor table for two at 7 PM. Ask whether to reserve it."
}
```

```json
{
  "type": "response.item.create",
  "event_id": "evt_item_001",
  "item": {
    "type": "message",
    "role": "user",
    "content": [
      {
        "type": "input_text",
        "text": "Please check for a table for two at 7 PM."
      }
    ]
  }
}
```

```json
{
  "type": "response.create",
  "event_id": "evt_response_001"
}
```

```json
{
  "type": "session.close",
  "event_id": "evt_close_001"
}
```

## 1.7 Server events

Unless a row says otherwise, every server event has `event_id: string` and an
optional `client_event_id: string`.

| Event | Fields | Notes |
| --- | --- | --- |
| `session.started` | `session: SessionResource` | `SessionResource` is the `SessionConfig` plus `id`, `expires_at` (Unix seconds) and `status: "active"`. |
| `session.updated` | `session: SessionResource` | The complete resolved resource. |
| `session.input_audio.muted` / `session.input_audio.unmuted` | (none) | |
| `session.instructions.appended` / `session.thinking.appended` / `session.commentary.appended` | `start_ms: number`, `end_ms: number` | Sent "when the session timeline reaches the estimated end of the added context". This does **not** mean the update was spoken, consumed or played. |
| `session.output_audio.delta` | `delta: string`, `start_ms?`, `end_ms?` | **No `event_id`.** `start_ms` and `end_ms` are "Required on reflected sideband events; omitted on the primary WebSocket". There is no output-audio-done event. |
| `session.input_transcript.delta` | `delta`, `start_ms`, `end_ms` | User speech, as a fragment. |
| `session.output_transcript.delta` | `delta`, `start_ms`, `end_ms` | Assistant speech, as a fragment. |
| `session.delegation.created` | `delegation: {id, type:"delegation", target:"client" or "responses", response_id?}`, `offset_ms: number` | "This object contains metadata, not the task text." |
| `response.event` | `event: object`, `delegation_id?: string or null` | A nested Responses streaming event (Responses delegation). |
| `session.usage.updated` | `usage: {seconds: number}`, `context_window?: {usage_ratio: number}` | A cumulative snapshot, roughly once a minute. "Do not sum this value". |
| `session.closed` | `reason`, `session: SessionResource`, `usage: {seconds}` | Terminal. |
| `error` | `error: {code, message, type, client_event_id?, param?}` | See 1.11. |
| `info` | `code: string`, `message: string` | Notices, for example `data_channel_permissions`. |
| `session.input_audio.append` (server) | `audio` | Sideband only. Reflected input as PCM16LE at 24 kHz. No timestamps and no `event_id`. |
| `transport.ringing` / `transport.answered` | `session_id` | Sideband only (outbound SIP). |
| `transport.failed` | `session_id`, `error: {type:"call_error", code, message, param?}` | Sideband only. |
| `transport.dtmf.received` / `transport.dtmf.send` | `event: string` (the key) | Sideband only (SIP DTMF). |

Literal examples from the reference:

```json
{
  "type": "session.started",
  "event_id": "evt_started_001",
  "client_event_id": "evt_start_001",
  "session": {
    "id": "live_abc123",
    "model": "gpt-live-1",
    "status": "active",
    "expires_at": 1788555600,
    "instructions": "Help the caller plan a restaurant reservation. Confirm details before booking.",
    "input": [],
    "audio": {
      "format": {
        "type": "audio/pcm",
        "rate": 24000
      },
      "output": {
        "voice": "marin"
      }
    },
    "delegation": {
      "type": "client"
    }
  }
}
```

```json
{
  "type": "session.updated",
  "event_id": "evt_updated_001",
  "client_event_id": "evt_update_001",
  "session": {
    "id": "live_def456",
    "model": "gpt-live-1",
    "status": "active",
    "expires_at": 1788555600,
    "instructions": "Help the caller plan a restaurant reservation. Confirm details before booking.",
    "input": [],
    "audio": {
      "format": {
        "type": "audio/pcm",
        "rate": 24000
      },
      "output": {
        "voice": "marin"
      }
    },
    "delegation": {
      "type": "responses",
      "responses": {
        "model": "gpt-6-astra",
        "instructions": "Check restaurant availability. Ask before confirming a booking.",
        "max_output_tokens": 1024,
        "tools": []
      }
    }
  }
}
```

```json
{
  "type": "session.input_audio.muted",
  "event_id": "evt_muted_001",
  "client_event_id": "evt_mute_001"
}
```

```json
{
  "type": "session.commentary.appended",
  "event_id": "evt_commentary_002",
  "client_event_id": "evt_commentary_001",
  "start_ms": 5200,
  "end_ms": 5400
}
```

```json
{
  "type": "session.output_audio.delta",
  "delta": "AACAAIAAAIAAAP9/AIAAgA==",
  "start_ms": 1000,
  "end_ms": 1200
}
```

(This is the reflected sideband form. On the primary socket, `start_ms` and
`end_ms` are omitted.)

```json
{
  "type": "session.input_transcript.delta",
  "event_id": "evt_input_transcript_001",
  "delta": "A table for two at seven, please.",
  "start_ms": 1600,
  "end_ms": 3400
}
```

```json
{
  "type": "session.output_transcript.delta",
  "event_id": "evt_output_transcript_001",
  "delta": "Would you like me to reserve that table?",
  "start_ms": 5400,
  "end_ms": 7200
}
```

```json
{
  "type": "session.delegation.created",
  "event_id": "evt_delegation_001",
  "offset_ms": 3600,
  "delegation": {
    "id": "del_abc123",
    "type": "delegation",
    "target": "client"
  }
}
```

```json
{
  "type": "response.event",
  "event_id": "evt_response_002",
  "delegation_id": "del_responses123",
  "event": {
    "type": "response.output_text.delta",
    "item_id": "msg_abc123",
    "output_index": 0,
    "content_index": 0,
    "delta": "An outdoor table is available at 7 PM.",
    "sequence_number": 3,
    "logprobs": []
  }
}
```

```json
{
  "type": "session.usage.updated",
  "event_id": "evt_usage_001",
  "usage": {
    "seconds": 32.5
  },
  "context_window": {
    "usage_ratio": 0.12
  }
}
```

```json
{
  "type": "session.closed",
  "event_id": "evt_closed_001",
  "client_event_id": "evt_close_001",
  "reason": "close_requested",
  "session": {
    "id": "live_abc123",
    "model": "gpt-live-1",
    "status": "active",
    "expires_at": 1788555600,
    "instructions": "Help the caller plan a restaurant reservation. Confirm details before booking.",
    "input": [],
    "audio": {
      "format": {
        "type": "audio/pcm",
        "rate": 24000
      },
      "output": {
        "voice": "marin"
      }
    },
    "delegation": {
      "type": "client"
    }
  },
  "usage": {
    "seconds": 45.8
  }
}
```

```json
{
  "type": "info",
  "event_id": "evt_info_001",
  "code": "data_channel_permissions",
  "message": "The frontend data channel is configured with restricted event permissions."
}
```

```json
{
  "type": "transport.failed",
  "event_id": "event_call_4",
  "session_id": "live_u0_123",
  "error": {
    "type": "call_error",
    "code": "provider_invite_failed",
    "message": "provider rejected the call",
    "param": ""
  }
}
```

```json
{
  "type": "transport.dtmf.received",
  "event_id": "event_dtmf_1",
  "event": "5"
}
```

Conflicts between OpenAI and Azure:

- Azure shows `start_ms` and `end_ms` on primary `session.output_audio.delta`.
  The OpenAI reference and the WebSocket guide both say they are omitted on
  the primary socket. **Design rule:** read them if present, but never depend
  on them.
- Azure says "Successful acknowledgments don't echo `event_id`; only errors
  use `client_event_id`." The OpenAI reference shows `client_event_id` on
  `session.started`, `session.updated`, mute acks and append acks. **Design
  rule:** correlate by `client_event_id` when present, and otherwise match
  acks of the same type in FIFO order.
- Azure's `session.started` example has no `status` field.

## 1.8 Session lifecycle (primary WebSocket)

1. Connect with no query parameters and the bearer header.
2. Send `session.start` as the first message. If startup fails, an `error` is
   sent and `session.started` never arrives: "A startup error prevents
   `session.started`." An HTTP error on session creation means "the session
   did not reach `session.started`."
3. Wait for `session.started` before sending audio or commands.
4. Stream audio continuously, including silence: "Keep input audio running
   throughout this sequence, including silence before the caller speaks."
   "EOF on the audio source does not end the conversation."
5. Handle interleaved server events.
6. Close: install a `session.closed` handler, then send `session.close`, then
   keep reading until `session.closed` arrives. "On close, the service stops
   accepting new work, drains active delegation and output work". "Sending
   `session.close` cancels queued Responses and rejects further commands."
   "Closing the session returns errors for appends that are still pending."
   If the socket closes without `session.closed`, final usage is unconfirmed.
   The SDK examples wait up to 15 s.

`session.closed.reason` values:

| Reason | Meaning |
| --- | --- |
| `close_requested` | The application sent `session.close` or called the hangup endpoint. |
| `expired` | The session reached its duration limit. |
| `content` | A safety filter ended the session. |
| `remote_hangup` | The remote primary connection ended gracefully. |
| `connection_lost` | The primary or upstream connection was lost unexpectedly. |

The Azure lifecycle example, quoted literally:

```text
// 1. Client startup
{ "type": "session.start",
  "session": {
    "model": "gpt-live-1",
    "instructions": "Be concise.",
    "delegation": { "type": "client" }
  }
}

// 2. Server startup acknowledgment
{ "type": "session.started",
  "session": {
    "id": "sess_123",
    "model": "gpt-live-1",
    "instructions": "Be concise.",
    "audio": { "output": { "voice": "marin" } },
    "delegation": { "type": "client" }
  }
}

// 3. Client audio, repeated as data becomes available
{ "type": "session.input_audio.append",
  "audio": "<base64-encoded-24khz-pcm16le-mono-audio>"
}

// 4. Server events may interleave
{ "type": "session.input_transcript.delta",
  "delta": "Hello",
  "start_ms": 600,
  "end_ms": 800
}
{ "type": "session.output_audio.delta",
  "delta": "<base64-encoded-24khz-pcm16le-mono-audio>",
  "start_ms": 0,
  "end_ms": 100
}

// 5. Client shutdown; keep reading until session.closed
{ "type": "session.close" }
```

Greeting: GPT-Live does not speak first unless told to. "To have GPT-Live open
the conversation, send greeting instructions after `session.started`" with
`session.instructions.append` and `delegation_id: null`, while input audio
keeps running.

## 1.9 Sideband WebSocket

- URL: `wss://api.openai.com/v1/live/sessions/{session_id}/attach`, with the
  same bearer authentication.
- The reference lists one optional connection parameter, `graceful_close`
  (boolean): "Opt in to the graceful WebSocket closing handshake when the
  session ends."
- "Attaching does not create a session or replay earlier events." Do not send
  `session.start` or `session.input_audio.append` on a sideband.
- A sideband receives the same JSON server events as the primary connection,
  plus reflected audio:
  - reflected input arrives as server `session.input_audio.append`, with no
    timestamps, "before model-input muting";
  - reflected output arrives as `session.output_audio.delta` with `start_ms`
    and `end_ms`. Gaps in the ranges are dropped frames.
- A sideband can send `session.update`, the three appends, `response.item.create`
  with `response.create`, mute and unmute, and `session.close`.
- "If both connections receive a function-call event, execute the function
  once." Pick one owner for each action.

## 1.10 Delegation

GPT-Live never calls a tool itself. It decides when backend work is needed and
emits `session.delegation.created`. The mode is fixed at startup: "To switch
delegation modes, create a new session."

### 1.10.1 Client delegation (`{"type":"client"}`, also the default)

1. The server emits `session.delegation.created` with `target: "client"` and
   a delegation `id`. The event has **no task text**: "Use the transcript
   events and application state to work out what the user wants." "The
   notification may arrive before the full sentence is transcribed. Keep it
   until you have enough context, or ask the caller to clarify before taking
   action."
2. The client builds the backend request from its own transcript history and
   task state ("collect transcripts and keep the current task state
   yourself"), checks permissions, and runs its own agent or model.
3. The client returns results tagged with `delegation_id = delegation.id`:
   - `session.commentary.append` for results to speak. The model paraphrases
     them.
   - `session.thinking.append` for quiet progress or facts.
   - `session.instructions.append` for behaviour directives (instructions
     still apply to the whole session).
   "You can send multiple updates with the same client delegation ID." Each
   append is at most 500 tokens. Stream "coherent, verified chunks".
4. The ack (`*.appended`) arrives after estimated context injection. It does
   not prove anything was spoken.

There is **no** delegation-completed, delegation-cancelled or result-required
event, and no timeout. **UNCONFIRMED:** what the model does if a client
delegation never gets a result. The prompting guide only says "Do not guess
the result while waiting." There is also no cancel command:
"Interrupting the spoken conversation leaves backend work running."
Cancellation and stale-result discarding are the client's job; see task
revisions in the migration guide.

The migration guide's adapter, quoted literally (JavaScript):

```javascript
async function handleDelegation(event, app) {
  if (
    event.type !== "session.delegation.created" ||
    event.delegation?.target !== "client"
  )
    return;
  const context = app.readContext();
  if (!context) return; // Retain the notice; resolve the request before acting.
  const summary = await app.runAgent({
    revision: context.revision,
    recentConversation: context.recentConversation,
    task: context.task,
  });
  if (app.currentRevision() !== context.revision) return;
  app.send({
    type: "session.commentary.append",
    event_id: crypto.randomUUID(),
    delegation_id: event.delegation.id,
    content: summary,
  });
}
```

The suggested backend prompt prefix (quoted):

```text
## Voice conversation context
You are helping an assistant in a live voice conversation. Transcripts
can contain mistakes, unfinished phrases, and later corrections. Use
the latest context and verified records. If a needed detail is still
unclear, ask for that detail instead of guessing.
## Task instructions
[Your task instructions, business rules, available tools,
and confirmation requirements.]
## Return the result
Return the relevant facts, the task's current status, and the next step.
Report an action as complete after the tool or service confirms success.
If the outcome is unclear, state that and explain what needs to be checked.
```

### 1.10.2 Responses delegation (`{"type":"responses","responses":{...}}`)

1. `session.delegation.created` arrives with `target: "responses"` and a
   `response_id`:

   ```json
   {
     "type": "session.delegation.created",
     "event_id": "event_delegation",
     "offset_ms": 1000,
     "delegation": {
       "id": "item_delegation_456",
       "type": "delegation",
       "target": "responses",
       "response_id": "resp_123"
     }
   }
   ```

2. GPT-Live calls the configured Responses model and supplies the
   conversation context itself. Backend stream events arrive wrapped in
   `response.event`. "Don't treat top-level `response.*` values as unwrapped
   Responses events."
3. Hosted tools (`web_search`) run on the server. Function tools are returned
   to the client. Read a completed call from the nested
   `response.output_item.done`; "an arguments-done event alone is not
   sufficient to identify the call":

   ```json
   {
     "type": "response.event",
     "delegation_id": "item_delegation_456",
     "event": {
       "type": "response.output_item.done",
       "item": {
         "type": "function_call",
         "call_id": "call_123",
         "name": "get_weather",
         "arguments": "{\"location\":\"Seattle\"}"
       }
     }
   }
   ```

4. Submit one `response.item.create` with a `function_call_output` for **every**
   pending call, then send `response.create`:

   ```json
   {
     "type": "response.item.create",
     "event_id": "event_function_output_1",
     "item": {
       "type": "function_call_output",
       "call_id": "call_123",
       "output": "{\"temperature\":62,\"conditions\":\"rain\"}"
     }
   }
   ```

   "Appending a function result doesn't automatically continue the response,
   and it has no standalone success acknowledgment."
5. Read lifecycle events until a terminal nested event such as
   `response.completed`. Forwarded lifecycle snapshots have `output: []`,
   `tools: []`, `instructions: null` and no `input`, so function calls must be
   collected from the individual `output_item.done` events. Backend token
   usage arrives only in nested `response.completed`.
6. "Delegated output text is also injected into the live session".
   The Live model speaks it.

Typed user text in Responses mode is queued with `response.item.create`
(`type:"message", role:"user"`) followed by `response.create`. In client mode,
typed text goes to the client's own backend, and a summary may be mirrored
with `session.thinking.append`.

## 1.11 Errors

One envelope is used for every error:

```json
{
  "type": "error",
  "event_id": "evt_error_001",
  "error": {
    "type": "invalid_request_error",
    "code": "unknown_parameter",
    "message": "Unknown parameter: 'session.voice'.",
    "param": "session.voice",
    "client_event_id": "evt_invalid_001"
  }
}
```

```json
{
  "type": "error",
  "event_id": "event_error",
  "error": {
    "type": "invalid_request_error",
    "code": "immutable_field_update",
    "message": "The delegation type cannot change after session startup.",
    "param": "session.delegation.type",
    "client_event_id": "event_update"
  }
}
```

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "code": "invalid_audio",
    "message": "PCM16 audio must contain an even number of bytes",
    "param": "audio",
    "client_event_id": "event_audio_1"
  }
}
```

(The third example is from Azure.)

- `error.type` is a string. The documented values are `invalid_request_error`
  and `server_error`. `call_error` is used only in `transport.failed`.
- Documented codes: `unknown_parameter`, `immutable_field_update` and
  `invalid_audio`. The SIP HTTP API also returns `decision_already_made`.
  "Provide a general error handler for errors whose code is `null` or whose
  client event ID is absent." **UNCONFIRMED:** the complete code list,
  including the codes for exceeding the 500-token append limit, the 16,384-token
  instruction limit, rate or concurrency limits, and an unknown `delegation_id`.
- "A command error doesn't necessarily close an already-started session."
- Moderation: "Some moderation events end the session" (reason `content`).
  "Others cut off assistant audio for the remainder of its current speech and
  emit an `error` event without ending the session." **UNCONFIRMED:** the code
  for that error.

## 1.12 Turn-taking, interruption and barge-in

- GPT-Live has no turn detector to configure and no commit or
  `response.create` for speech. From the migration table: "Stream audio
  continuously. GPT-Live decides when to speak; remove manual audio commits
  and voice-turn triggers."
- "GPT-Live has no corresponding event marking the end of each spoken
  response." There is no `response.done`, no `output_audio.done` and no
  `speech_started`. Speaking indicators must come from the client's audio
  player.
- Barge-in is handled by the model and steered by the prompt ("Interruption
  policy: Stop speaking when the user interrupts."). The client sends nothing.
  **UNCONFIRMED:** whether any event signals that the model stopped
  mid-utterance. None is documented, so the client cannot tell an interruption
  from a natural pause.
- Backchannels ("mm-hmm") overlap the caller's speech by design. Overlap alone
  is therefore not an interruption.
- To block audio from reaching the user, the client must "Temporarily mute or
  drop the output, discard locally queued audio, and send the corrective
  instruction". `session.input_audio.mute` does not mute output.
- `session.instructions.append` "can interrupt speech in progress".

## 1.13 Transcripts

- `session.input_transcript.delta` carries user speech and
  `session.output_transcript.delta` carries assistant speech. Transcripts are
  always on; there is no transcription config.
- `start_ms` and `end_ms` are milliseconds from session start. The interval is
  half-open ("from 1,000 ms up to, but excluding, 1,200 ms") and approximate.
- "Fragment boundaries reflect audio cadence, not semantic turn boundaries."
  There is no item ID, no transcript-done event and no turn-completed event.
  User and assistant fragments interleave. Fragments must be appended
  "exactly as received, preserving spaces and repeated words".
- Transcripts can contain mistakes. Delivery can be uneven, and "a gap in
  delivery may be a network delay".

## 1.14 Limits

| Limit | Value | Source |
| --- | --- | --- |
| Live instructions | 16,384 tokens | reference |
| Each append's `content` | 500 tokens | reference |
| Startup `input` history | 128 messages and 8,192 tokens | reference |
| Live context window | 128,000 tokens, including audio tokens | managing sessions guide |
| Context compaction | Above 90% use, a replacement engine starts with the original instructions plus up to 8,192 tokens of history and summary | managing sessions guide |
| `delegation.responses.max_output_tokens` | at least 16 when set | delegation guide |
| Concurrent sessions | 25 to 500 by tier | model page |
| Recording retention | 30 days | managing sessions guide |
| WebRTC init billing | 15 s billed at creation, credited back once running | WebRTC guide |
| Outbound SIP | 1 MiB body, ringing up to 3 min, connected call up to 2 h | SIP reference |
| Session duration (WebSocket) | **UNCONFIRMED.** `expires_at` gives the deadline per session; reason `expired` | reference |
| Max `input_audio.append` size or frame cadence | **UNCONFIRMED** | none |
| Output pacing (real time vs burst) | **UNCONFIRMED** | none |

---

# Part 2: Integration design

## 2.1 How the existing OpenAI realtime provider plugs in

| Layer | File(s) | Role |
| --- | --- | --- |
| Loop contract | `go-agent-loop/pkg/messages/session.go` | `Session` (`Send`, `Receive`, `Done`, `Close`), `SessionInferencer`, and optional capabilities: `SessionResponseRequester`, `SessionTurnDetection`, `SessionLocalPlayback`, `SessionInputFormat`, `SessionInitialConfigMarker`, `SessionTerminalError`, `SessionMessageSender` and others. `SessionCapabilities` is the forwarding embed that every wrapper uses. |
| Stream vocabulary | `go-agent-loop/pkg/messages/stream_types.go`, `session_values.go` | `MESSAGE.START/END`, `AUDIO.*`, `TRANSCRIPT.*`, `TOOLCALL.*`, `VAD.*`, `INPUT_ITEM.ADDED`, `SESSION.*`, `RESPONSE.CREATE/CANCEL`, `ERROR`, `USAGE.INFO`. |
| Gateway contract | `go-llm-gateway/pkg/providers/session_provider.go` | `SessionProvider{Name(); ConnectSession(ctx, models.SessionConfig) (messages.Session, error)}`. |
| Gateway config | `go-llm-gateway/pkg/models/session.go` | `models.SessionConfig` (model, voice, instructions, formats and rates, tools, turn detection, transcription, raw `Config`) and the Realtime wire event names. |
| Shared skeleton | `go-llm-gateway/pkg/providers/internal/realtime/{session,loops,surface,media,dialer}.go` | Bounded send and receive queues, read and write loops, a `Handler` hook (`HandleEvent`, `ExpectedReadClose`, `ExpectedWriteClose`, `EventWritten`), flat-JSON `WriteEvent` and `ParseEvent`, terminal-error bookkeeping, RTC media endpoints, and the gorilla dialer. |
| OpenAI Realtime | `go-llm-gateway/pkg/providers/openai/session*.go` | `ConnectSession` dials `wss://.../v1/realtime?model=`, writes `session.update` and starts the loops. `realtimeInboundMessages` maps server events to stream messages. `realtimeOutboundEvents` maps stream messages to wire events (`AUDIO.DELTA` to `input_audio_buffer.append`; `MESSAGE.END` to commit plus `response.create`; `RESPONSE.CANCEL` to `response.cancel`; `TOOLCALL.END` to `function_call_output`; `RESPONSE.CREATE` to `response.create`). It also runs a response-admission gate, because Realtime allows one active response. |
| Catalog and admission | `go-agent-runtime/services/providers/models.go`, `internal/catalog/catalog.go`, `internal/admission/admission.go` | The catalog has `openai` models only. Admission restricts **only** `openai`; every other provider name is `Allowed` with no model check. |
| Provider service | `go-agent-runtime/services/providers/internal/service/session.go` | `resolveSessionProvider` runs admission and the credential check. `buildSessionProvider` and `sessionDialer` switch on the provider name (`openai`, `openrouter` and `local` all use the openai provider; `grok` uses its own). `sessionConfig` sets default PCM16 at 24 kHz. Recording and replay wrap the dialer and inferencer. |
| Session wrappers | `go-agent-runtime/services/session/internal/live/sessionwrap/*`, `services/replay/internal/plan/live.go`, `services/sessionduration/...`, `go-llm-gateway/pkg/testing/session_record.go` | Each embeds `messages.SessionCapabilities`, so new optional capabilities pass through automatically. |
| Loop consumer | `go-agent-loop/pkg/participants/model_runner_session*.go`, `model_runner_control.go`, `internal/sessionstate` | The session goroutine tracks the response lifecycle from `MESSAGE.START` to `MESSAGE.END`. It runs a local energy barge-in (onset, then `RESPONSE.CANCEL`) unless `ProviderTurnDetection()` is true. It forwards tool results as `TOOLCALL.END` and then asks for a continuation with `RESPONSE.CREATE`. It binds the continuation response as tagged, or as guessed for providers like Grok that do not echo the purpose. It sends the initial `SESSION.UPDATE` unless `InitialSessionConfigSent()`. |
| CLI | `agent-cli/internal/config/interface.go` (`ProviderOpenAI`, `ProviderGrok`, ...), `config/overrides.go`, `config/loading.go`, `services/session_host.go` (`resolvedProvider`), `services/wire/device_probe_session.go`, `transport/cli/*` (`--provider`, `--model` flags) | The provider name selects a config block (`model.openai`, `model.grok`) and its key, model and base URL. |

## 2.2 Naming: provider `openai-live`, model `gpt-live-1`

**Decision:** add a new session provider name `openai-live`, with catalog model
`gpt-live-1`. The Go package is `go-llm-gateway/pkg/providers/openailive`
(a Go package name cannot contain a hyphen).

Reasons:

1. **In this repo the provider name selects the wire protocol, not the
   vendor.** `buildSessionProvider` and `sessionDialer` switch on it, and
   `openrouter` and `local` are separate names that happen to share the openai
   protocol. GPT-Live has a different endpoint, handshake and event vocabulary,
   so it needs its own branch. Picking it by model inside the `openai` provider
   would put two unrelated state machines behind one `ConnectSession` and
   would weaken the Realtime response-admission code.
2. **Admission stays honest.** The catalog is keyed by provider. Under
   `openai-live`, only `gpt-live-1` is admitted, and `gpt-live-1` is never
   admitted under `openai` (where the Realtime endpoint rejects it). Admission
   today treats every provider except `openai` as unrestricted, so the change
   must also add `openai-live` to the catalog-restricted set. Without it, any
   model string would be admitted.
3. **The name matches the vendor's own wording** ("GPT-Live", "Live API",
   `/v1/live`, the SDK's `client.live`), and reads naturally in
   `--provider openai-live --model gpt-live-1`.
4. **Credentials are reused.** GPT-Live uses the same OpenAI project key.
   `openai-live` resolves its key and base URL from the existing `model.openai`
   block (`ActiveOpenAIConfig`), and accepts an optional override such as
   `model.openai_live.base_url` for Azure. This is open question Q2.

Rejected alternatives: `openai` plus model-based dispatch (reason 1);
`gpt-live` as the provider name (it names a model family, not a vendor
protocol, and the names would drift when `gpt-live-2` ships); `live`
(ambiguous next to "live session", which already means "real provider" in
this repo).

A collision to be aware of: `models.DefaultInputAudioTranscriptionModel` and
`audioio.DefaultTranscriptionModel` are already `"gpt-live-transcribe"`, a
Realtime transcription model. It is unrelated to GPT-Live, and the
`openai-live` provider ignores `InputAudioTranscription` entirely.

## 2.3 Decision: client delegation by default; Responses delegation later and optional

**Client delegation is the mode we build first and use by default.** In it,
the harness's own agent loop and tools act as the backend.

Reasons:

- **Provider independence is the point of this harness.** With client
  delegation the backend can be any gateway provider (openai, openrouter,
  anthropic, gemini or local). Responses delegation ties the backend to
  OpenAI Responses models and allows only `function` and `web_search` tools.
- **The harness tools are local, and the harness enforces their policy.**
  This covers filesystem tools, WebMCP and browser tools, device tools, tool
  ordering, approval and the tool-result image path. OpenAI says client
  delegation is the choice when "Your application must validate, redact,
  combine, or discard results before they reach GPT-Live" and when you need
  "custom routing ... fallbacks, checkpoints, or budgets". Both apply here.
- **Context ownership.** The harness already keeps transcripts, history and
  session storage. Client delegation lets the backend see exactly the history
  the harness chooses.
- **It is the documented path for "keep your existing agent"**
  ("From a text agent or chained pipeline: ... Configure `delegation` as
  `{"type":"client"}`").

The honest counterweight: **Responses delegation maps more directly onto the
existing session tool contract.** A nested `response.output_item.done`
becomes `TOOLCALL.END`, a tool result becomes `response.item.create`, and
`RESPONSE.CREATE` becomes `response.create`. That is almost the current
Realtime tool loop. It is therefore a cheap optional later phase (PR 7) for
users who want a hosted backend, and the PR 1 codec models `response.event`
and the Responses commands so that phase needs no protocol work. It is not
the default, because it hands reasoning, context and the model choice to
OpenAI.

## 2.4 Where the backend runs: delegation as a synthetic tool call

GPT-Live never names a tool. The harness needs a reasoning backend that sees
the conversation and can call the harness tools. Two shapes were considered:

- **(A) Delegation as a tool call. Chosen for the first implementation.**
  The provider turns each client `session.delegation.created` into one
  synthetic tool call named `live_delegate`. The tool call id is the
  delegation id, and the arguments carry a transcript window. The voice
  session's existing tool runner executes `live_delegate`. That tool is a
  runtime service that runs a nested turn-based `agentloop`, using a
  configured stateless backend provider and model and the session's ordinary
  toolset (without `live_delegate`). It returns a result of at most 500
  tokens. The tool result flows back through the existing `TOOLCALL.END`
  path, and the provider sends it as `session.commentary.append` with that
  `delegation_id`.
  - Pros: no change to the loop's participant model. It reuses tool
    scheduling, tool-result forwarding, recording and replay. Concurrent
    delegations become concurrent tool calls.
  - Cons: the voice model's view of the tool loop is synthetic, and the
    nested loop must be bounded (iterations, time and tokens).
- **(B) A second model participant in the loop.** A backend model participant
  receives delegations as input and routes its final text to the voice
  session. This is closer to the loop's tick and participant architecture,
  but it needs new multi-model routing in `go-agent-loop`. It stays the
  evolution path if (A) proves limiting.

Context for `live_delegate`. The delegation event carries no task text, so
the provider keeps a bounded ring of transcript fragments: speaker, text,
`start_ms` and `end_ms`. It emits the synthetic tool call once either of
these happens:

- an input-transcript fragment with `end_ms >= offset_ms` has arrived, or
- a settle window `D` has passed on the injected clock. The proposed `D` is
  400 ms (open question Q5).

The arguments are:

```json
{
  "delegation_id": "del_abc123",
  "offset_ms": 3600,
  "transcript": [
    {"speaker": "user", "text": "A table for two at seven, please.", "start_ms": 1600, "end_ms": 3400}
  ]
}
```

The nested loop also receives the session's message history, so short
replies such as "yes" stay resolvable. Task revisions and discarding stale
results (the migration guide's `currentRevision()` check) live in the
delegation service. A result for a superseded revision is sent as
`session.thinking.append`, never as commentary.

## 2.5 Event mapping onto the harness session contract

### Inbound (server to `StreamMessage`)

| GPT-Live event | Harness output |
| --- | --- |
| `session.started` | Consumed by `ConnectSession` during the handshake (2.7). It then emits `SESSION.OPEN` and `SESSION.CREATED(session.id, model)`. |
| `session.updated` | `SESSION.UPDATED(session.id)` |
| `session.output_audio.delta` | Opens an assistant segment if none is open: `MESSAGE.START`, then `AUDIO.START`, both with the synthetic segment id as `ResponseID`. Then `AUDIO.DELTA(bytes, media type from the configured format)`. |
| `session.output_transcript.delta` | `TRANSCRIPT.DELTA(Role=assistant, ResponseID=segment)`. It opens a segment if none is open. |
| `session.input_transcript.delta` | `TRANSCRIPT.DELTA(Role=user)` with a synthetic user utterance id (`live_utt_N`). The id is announced once with `INPUT_ITEM.ADDED`, so the existing attribution keeps working. The utterance closes with `TRANSCRIPT.END(Role=user)` after a server-timeline gap of `G` ms or more. |
| `session.delegation.created` (`target:"client"`) | After the settle window: `MESSAGE.START`, `TOOLCALL.START`, `TOOLCALL.END(id=delegation.id, name="live_delegate", args)`, `MESSAGE.END`. This is its own synthetic response with id `del:<delegation.id>`. |
| `session.delegation.created` (`target:"responses"`) and `response.event` | Ignored in PR 2. Mapped to the tool contract in PR 7 (2.3). |
| `*.appended`, `session.input_audio.muted` / `unmuted` | Consumed inside the provider. Each settles the pending command it acknowledges, matched by `client_event_id` or in FIFO order. No stream message. |
| `session.usage.updated` | Kept as the latest cumulative seconds and context ratio. The `USAGE.INFO` mapping is open question Q7. |
| `session.closed` | Closes any open segment, then `SESSION.CLOSE(reason)`. `reason` maps to `TerminalReason`: `close_requested` is a client close; `expired` is a timeout; `content` is a provider refusal; `remote_hangup` is a remote close; `connection_lost` is a provider transport failure. |
| `error` | `ERROR` with code, message, param and client event id. A startup error fails `ConnectSession` instead. |
| `info`, `transport.*` | Logged only. |

**Synthetic assistant segments.** The loop's lifecycle needs a
`MESSAGE.START`/`MESSAGE.END` boundary for each response. GPT-Live has none,
so the provider synthesizes one. A segment (`ResponseID = "live_seg_<n>"`)
opens at the first assistant audio or transcript after an idle period. It
closes, emitting `AUDIO.END`, `TRANSCRIPT.END` and `MESSAGE.END` with the
`completed` status, at the first of:

- (a) a gap of `G` ms or more on the server timeline between output transcript
  fragments;
- (b) an idle timer of `G` ms, on the injected clock, after the last output
  event (primary audio has no timing);
- (c) `session.closed` or a transport close.

The default `G` is proposed at 600 ms (open question Q5). Audio duration is
measured from the byte count at the configured rate, not from arrival time.

### Outbound (`StreamMessage` to GPT-Live)

| Harness input | GPT-Live wire |
| --- | --- |
| `AUDIO.DELTA` | `session.input_audio.append`. A trailing odd byte is held back and joined to the next PCM chunk, as in the official sample. G.711 bytes pass through unchanged. |
| `MESSAGE.END` (the Realtime "commit and respond") | **No wire event.** Success is reported locally. GPT-Live decides when to speak. |
| `RESPONSE.CANCEL` | **No wire event.** It interrupts local playback unless `KeepPlayback` is set. |
| `RESPONSE.CREATE` | **No wire event** in client delegation. Success is reported locally. In Responses mode (PR 7) it maps to `response.create`. |
| `TOOLCALL.END` (tool result) whose `ToolCallId` is a known client delegation | `session.commentary.append{delegation_id, content}`. The content is the result text, cut to the 500-token budget (open question Q6). |
| `TOOLCALL.END` for an unknown id | Terminal-failure outcome, logged. GPT-Live has no generic tool-result channel. |
| `SESSION.UPDATE` | No wire event, success. Live startup fields are immutable, and tools live in the backend. The initial config is sent by `ConnectSession`, so `InitialSessionConfigSent()` returns `true`. |
| `TEXT.DELTA` (typed user text) | Undecided (open question Q4). The proposal is to send it to the delegation backend and mirror it as `session.thinking.append{delegation_id:null, "The user typed: ..."}`. Until that is decided, the provider returns a terminal failure. |
| `CONTEXT.APPEND` (new, see 2.6) | `session.{instructions,thinking,commentary}.append` |
| `Close()` | Graceful close: send `session.close`, wait for `session.closed` (bounded by an injected-clock timeout, 15 s by default), then close the socket. |

**Capabilities exposed by the session:**

| Capability | Value |
| --- | --- |
| `ProviderTurnDetection()` | `true`. The local energy barge-in must never fire, because the model owns turn-taking and backchannels overlap by design. |
| `SupportsResponseRequests()` | `true`. The local no-op lets the tool-continuation path complete; the next segment binds as a guessed continuation, as for Grok. |
| `SupportsCompleteMessages()` | `false` |
| `InitialSessionConfigSent()` | `true` |
| `InputAudioSampleRate()` | the configured rate |
| `LocalPlayback`, `InterruptLocalPlayback`, `RTCMedia` | inherited from the shared skeleton |

## 2.6 New interface surface

The plan keeps new surface small. In order of need:

1. **New gateway package** `go-llm-gateway/pkg/providers/openailive`. No
   change to `SessionProvider`.
2. **Generalize the shared skeleton's doc and config**
   (`internal/realtime`). The framing is identical: flat JSON objects with a
   `type` field. Two hooks are needed:
   - a `Config.Close` hook (or a `GracefulClose(ctx)` helper) so `Close` can
     run the `session.close` handshake before it releases the connection;
   - an injected `clock.Scheduler` for the segment, settle and close timers.

   Both are additive.
3. **`messages.StreamTypeContextAppend` (`CONTEXT.APPEND`)** in
   `go-agent-loop/pkg/messages`, with
   `ContextAppendValue{Kind: instructions or thinking or commentary, DelegationID *string, Content string}`.
   This is the one new loop-level vocabulary item. It is needed for:
   - greetings and disclosures (GPT-Live does not speak first);
   - delegation progress (`thinking`) separate from the final result
     (`commentary`);
   - guardrail redirects and UI context.

   Other providers return `false`, so senders must check the outcome. It
   belongs in PR 5, not PR 1.
4. **`RealtimeModel` metadata.** Add `Duplex bool` and
   `Delegation string` (`"client"` or `"responses"`). Callers then know not to
   send turn-detection, transcription or tool config, and the CLI can reject
   flags that have no effect (for example `--turn-detection`). The fields are
   additive.
5. **Runtime `livedelegation` service and the `live_delegate` tool**
   (`go-agent-runtime/services/livedelegation`). Its config: backend
   provider, model and key, the max iterations, timeout and token budget for
   each delegation, and the backend instructions (the prefix from 1.10.1).
6. **Session config input.** `models.SessionConfig` already has `Voice`,
   `Instructions` and a raw `Config`. Live-only options (`store`, initial
   `input` history, delegation mode) travel in `Config` as a typed
   `openailive.Options` JSON object, so `models.SessionConfig` does not
   change.

## 2.7 Connect handshake

Unlike `openai`, which writes `session.update` and returns at once,
`openailive.ConnectSession` will:

1. dial `wss://api.openai.com/v1/live/sessions` (overridable for the fake
   server and for Azure) with `Authorization: Bearer <key>`;
2. write `session.start`, built strictly from `models.SessionConfig` (2.8);
3. read frames until `session.started` or `error`, bounded by `ctx`. On
   `error`, close the socket and return a typed `StartupError{Code, Param, Message}`;
4. then start the shared loops and queue `SESSION.OPEN` and `SESSION.CREATED`.

Waiting here follows the documented rule ("Wait for session.started before
sending audio") and turns a startup failure into a connect error, which the
loop already handles. It costs one round trip before `ConnectSession`
returns.

## 2.8 `session.start` construction

| `models.SessionConfig` field | `session` field |
| --- | --- |
| `Model` | `model`. Must be `gpt-live-1`; admission already enforces this. |
| `Instructions` | `instructions`, omitted when blank. The provider rejects text over 64 KiB as a cheap stand-in for the 16,384-token limit (**UNCONFIRMED** tokenizer). |
| `Voice` | `audio.output.voice`, omitted when empty (server default `marin`) |
| `InputAudioFormat`/`InputAudioSampleRate` and the output pair | `audio.format`. Input and output must match, otherwise it is a config error. `pcm16` at 24000 becomes `{"type":"audio/pcm","rate":24000}`; `pcm16` at 16000 becomes `{"type":"audio/pcm","rate":16000}`; `g711_ulaw` becomes `{"type":"audio/pcmu","rate":8000}`; `g711_alaw` becomes `{"type":"audio/pcma","rate":8000}`. Any other rate is rejected. |
| `Config` (`openailive.Options`) | `delegation` (default `{"type":"client"}`, sent explicitly), `store`, `input` |
| `Tools`, `TurnDetection`, `InputAudioTranscription`, `Modalities`, `ReasoningEffort` | **never sent**. Tools go to the delegation backend. The others are ignored with a debug log. |

The voice registry (`agent-cli/internal/services`, see
`s2s-realtime-voice-configuration.md`) must hold a separate set of Live voices
for each provider. The 31 names are listed in 1.5.

## 2.9 Files to add or change

PR 1 (protocol, codec, fake server, catalog):

- `go-llm-gateway/pkg/providers/openailive/doc.go`: package doc that links to this design.
- `go-llm-gateway/pkg/providers/openailive/protocol.go`: typed structs for every client and server event in 1.6 and 1.7, plus `SessionConfig`, `SessionResource`, `AudioFormat`, `Delegation`, `ResponsesDelegationConfig`, `InitialItem` and `Error`.
- `go-llm-gateway/pkg/providers/openailive/codec.go`: `EncodeClientEvent` and `DecodeServerEvent`. An unknown `type` decodes to `UnknownEvent` and is not an error. The decoder accepts the forms that differ between OpenAI and Azure (optional timing on output audio; optional `client_event_id`).
- `go-llm-gateway/pkg/providers/openailive/start.go`: `models.SessionConfig` plus `Options` to `session.start` (2.8), including audio-format validation.
- `go-llm-gateway/pkg/providers/openailive/codec_test.go` and `start_test.go`, with `testdata/protocol/*.json`: the literal examples from Part 1, as round-trip goldens.
- `go-llm-gateway/pkg/providers/openailive/fakelive/server.go`: a scripted fake GPT-Live server. This is a test-support package, named to the test-support rule.
- `go-agent-runtime/services/providers/models.go`: add `OpenAILiveProvider = "openai-live"` and `OpenAILive1Model = "gpt-live-1"`.
- `go-agent-runtime/services/providers/internal/catalog/catalog.go`: the `openai-live` model list.
- `go-agent-runtime/services/providers/internal/admission/admission.go` and the service `models.go`: restrict `openai-live` to the catalog.
- Admission tests. `buildSessionProvider` still rejects `openai-live` ("realtime sessions do not support provider"), so PR 1 has no runtime behaviour change.

Later PRs:

- `go-llm-gateway/pkg/providers/openailive/{provider,options,session,session_inbound,session_outbound,segments,close}.go`
- `go-llm-gateway/pkg/providers/internal/realtime/`: the close hook and clock injection.
- `go-agent-runtime/services/providers/internal/service/session.go`: `openai-live` branches in `buildSessionProvider` and `sessionDialer`; the credential host check is unchanged (`api.openai.com`).
- `agent-cli/internal/config/{interface,overrides,loading}.go`, `services/session_host.go`, `services/wire/device_probe_session.go`, and the `--provider` help text: `ProviderOpenAILive = "openai-live"`, which reuses `model.openai`.
- `go-agent-loop/pkg/messages/{stream_types,session_values}.go`: `CONTEXT.APPEND`.
- `go-agent-runtime/services/livedelegation/...` (service, wire, `live_delegate` tool).
- `go-llm-gateway/pkg/testing/testdata/session-fixtures/openai-live/*.session.json`: replay fixtures.

## 2.10 Test strategy

All tests must be deterministic, run each unit in 3 minutes or less, and use
virtual time (`testing/synctest` or `go-audio/pkg/clock.Deterministic`), with
no wall-clock sleeps.

1. **Codec goldens (PR 1).** Every literal JSON block in Part 1 lives under
   `testdata/protocol/`. Each decodes to the expected typed value and
   re-encodes to semantically equal JSON. Negative cases:
   - an odd-length PCM payload is refused by the encoder helper;
   - an unknown server `type` decodes to `UnknownEvent`;
   - `delegation_id` is always present (as `null` or a string) on every
     append, because the field is required.
2. **Strictness test for `session.start` (PR 1).** The built `session` object
   contains only documented keys. The test checks the key set against an
   allowlist taken from 1.5, so an accidental `turn_detection` or `tools`
   fails the build instead of failing in production with `unknown_parameter`.
3. **Fake GPT-Live server (`fakelive`, PR 1, used from PR 2).** It runs
   in-process, over a `transport.Conn` pair (fast unit tests) and over an
   `httptest` gorilla WebSocket server (dialer-level tests). The fake is a
   script of steps on virtual time. It:
   - validates the bearer header and the empty query string;
   - validates the strict `session.start` and answers `session.started` or an
     `error` (`unknown_parameter` or `invalid_audio`);
   - records `input_audio.append` bytes;
   - plays scripted output audio, transcript fragments and
     `delegation.created`;
   - acknowledges appends with `start_ms`/`end_ms` and `client_event_id`, and
     can withhold an ack to simulate a stalled timeline;
   - emits `session.closed` with a configurable reason, or drops the socket to
     test unconfirmed finalization.

   It also implements `transporttest` conformance.
4. **Replay fixtures (PR 6).** Version-2 `.session.json` captures with
   `provider.name: "openai-live"`, `fixture_provenance: "synthetic"`, sealed
   with `SealSessionCapture`. Fixtures cover a greeting, a client delegation
   with a commentary result, an error, and a graceful close. Add one real
   capture later, sanitized, under the fixture authoring guide, once Q1 is
   answered.
5. **Behaviour tests on virtual time (PRs 2 to 5)**, run against `fakelive`
   through the real model runner:
   - segments open and close at `G` and at the server-timeline gaps, and
     `MESSAGE.END` is never emitted twice;
   - full-duplex overlap (a user fragment during an assistant segment) does
     not trigger `RESPONSE.CANCEL` (`ProviderTurnDetection` is true);
   - a delegation that arrives before its transcript waits for the settle
     window, then emits exactly one `live_delegate` call;
   - two concurrent delegations each get their own commentary, keyed by id;
   - a stale-revision result is sent as `thinking`, not `commentary`;
   - graceful close waits for `session.closed`, and a socket drop before it is
     recorded as unconfirmed;
   - a startup `error` fails `ConnectSession` with a typed error;
   - `session.closed{reason:"expired"}` maps to the timeout terminal reason.
6. **Opt-in live smoke test (PR 6).** It is guarded by an env var and never
   runs in CI. It connects with a real key, sends 1 s of silence, checks for
   `session.started`, then closes and checks for `session.closed` with
   `close_requested`.

## 2.11 Phased PR plan

| PR | Scope | Mergeable because |
| --- | --- | --- |
| **1** | `openailive` protocol types, codec, `session.start` builder, literal-example goldens, the `fakelive` server, the catalog entry `openai-live`/`gpt-live-1`, and the admission restriction. | No production path reaches it yet: `BuildSession` still rejects the provider. It is pure additions plus tests. Dead-code gate: everything is reachable from tests. |
| **2** | `openailive` provider and session: dial, the `session.start` and `session.started` handshake, inbound audio and transcripts, synthetic segments, outbound audio, the no-op mappings, graceful close. Also the `realtime` skeleton hooks, the `openai-live` branches in the provider service, and CLI `--provider openai-live`. Delegations are logged and left unanswered. | An end-to-end voice conversation works with no tools. Behaviour tests run against `fakelive`. |
| **3** | The `livedelegation` service and `live_delegate` tool, the synthetic tool call from `delegation.created`, the transcript ring and settle window, and the result mapping (`TOOLCALL.END` to `commentary.append`). | Tools work through the harness backend. |
| **4** | Task revisions, stale-result handling, concurrent delegations, and limits on the nested loop's budget. | Hardening on top of PR 3. |
| **5** | `CONTEXT.APPEND` in `go-agent-loop/pkg/messages`, mapped by `openailive` (greeting, disclosure, progress `thinking`, guardrail `instructions`), plus mute and unmute. | A new optional vocabulary item. Other providers decline it. |
| **6** | Recording and replay support for `openai-live`, synthetic replay fixtures, and the opt-in live smoke test. | Regression coverage. |
| **7** (optional) | Responses delegation, mapping the nested `response.output_item.done`, `response.item.create` and `response.create` onto the existing tool contract. | Only if Q3 says yes. |
| **8** (optional) | Sideband attach, WebRTC session creation, `store` and fork. | Only if a browser or telephony host needs them. |

## 2.12 Open questions for the user

1. **Q1 Credentials and access.** Does the project key in `credentials` have
   GPT-Live access (Tier 1 or higher; the Free tier is unsupported)? May PR 6
   record one real, sanitized capture?
2. **Q2 Naming and config.** Is `openai-live` with `gpt-live-1` acceptable?
   Should it reuse the `model.openai` key block, or get its own
   `model.openai_live` block? Is Azure Foundry a target (it changes the base
   URL and auth)?
3. **Q3 Responses delegation.** Do you want it at all (PR 7)? Or is client
   delegation the only supported mode?
4. **Q4 Typed text input.** What should `agent session` text turns and
   `TEXT.DELTA` do with GPT-Live? Options: (a) unsupported, failing fast;
   (b) send them to the delegation backend and mirror them as `thinking`;
   (c) send them as `session.commentary.append` so the model says something.
   The proposal is (b).
5. **Q5 Timing constants.** The segment-close gap `G` (proposed 600 ms) and
   the delegation settle window `D` (proposed 400 ms) are heuristics, because
   the protocol has no turn or done events. Should they be configurable per
   session?
6. **Q6 Long tool results.** When a backend result exceeds 500 tokens,
   should the provider truncate it, split it into several commentary appends,
   or require the backend prompt to summarize? The proposal is that the
   backend summarizes and the provider splits as a safety net.
7. **Q7 Usage accounting.** GPT-Live reports voice **seconds**, not tokens.
   Should `USAGE.INFO` gain a duration field, or should Live usage stay
   provider-internal (logged, plus the `session.closed` final value)?
8. **Q8 Backend model for delegation.** Which provider and model should the
   default `livedelegation` backend use? For example, the configured
   `model.openai` chat model, or a dedicated `session.live.backend_model`.
9. **Q9 Barge-in observability.** GPT-Live gives no interrupted-speech
   signal. Is it acceptable that harness metrics for barge-in and turn
   latency are approximate (derived from segments) for this provider?
10. **Q10 Greeting default.** Should `agent session --provider openai-live`
    send a default greeting instruction after `session.started` (this needs
    PR 5), or wait for the user to speak?

Also **UNCONFIRMED** in the protocol, and to be checked against the live
service in PR 6: the maximum session duration, output pacing, the full error
code list, the beta header requirement, an ephemeral-token flow, model
behaviour when a client delegation is never answered, and the moderation
cut-off error code.
