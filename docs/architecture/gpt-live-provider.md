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
- **Answered** (`chatgpt-oauth.md` 3.1): a ChatGPT OAuth token (the
  `yui auth chatgpt` credential) is **not** accepted for `gpt-live-1`. It
  reaches GPT-Live only as `gpt-live-1-codex` over WebRTC plus a sideband
  (2.3, PR 9).
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
  in bursts faster than real time. This matters for local buffering; see 2.6.2.

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
   sent and `session.started` never arrives. Azure states this directly
   ("A startup error prevents `session.started`."). OpenAI says it for WebRTC
   creation: an HTTP error on session creation means "the session did not
   reach `session.started`."
3. Wait for `session.started` before sending audio or commands.
4. Stream audio continuously, including silence: "Keep input audio running
   throughout this sequence, including silence before the caller speaks."
   "EOF on the audio source does not end the conversation."
5. Handle interleaved server events.
6. Close: install a `session.closed` handler, then send `session.close`, then
   keep reading until `session.closed` arrives. Azure: "On close, the service
   stops accepting new work, drains active delegation and output work".
   OpenAI: "Sending
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
   `response.event`. Azure: "Don't treat top-level `response.*` values as
   unwrapped Responses events."
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

   Azure: "Appending a function result doesn't automatically continue the
   response, and it has no standalone success acknowledgment." OpenAI says
   the same in its own words: "`response.item.create` has no separate success
   acknowledgment".
5. Read lifecycle events until a terminal nested event such as
   `response.completed`. Forwarded lifecycle snapshots have `output: []`,
   `tools: []`, `instructions: null` and no `input`, so function calls must be
   collected from the individual `output_item.done` events. Backend token
   usage arrives only in nested `response.completed`.
6. Azure: "Delegated output text is also injected into the live session, so
   it can surface as normal transcript and audio output."

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

- `error.type` is a string. The only documented value is
  `invalid_request_error`. `call_error` appears only in `transport.failed`.
  **UNCONFIRMED:** any other value, such as a server-side error type.
- Documented codes: `unknown_parameter`, `immutable_field_update` and
  `invalid_audio`. The SIP HTTP API also returns `decision_already_made`.
  "Provide a general error handler for errors whose code is `null` or whose
  client event ID is absent." **UNCONFIRMED:** the complete code list,
  including the codes for exceeding the 500-token append limit, the 16,384-token
  instruction limit, rate or concurrency limits, and an unknown `delegation_id`.
- Azure: "A command error doesn't necessarily close an already-started
  session."
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
| Session wrappers | `go-agent-runtime/services/session/internal/live/sessionwrap/*`, `services/replay/internal/plan/live.go`, `services/sessionduration/...`, `go-llm-gateway/pkg/testing/session_record.go` | Each embeds `messages.SessionCapabilities`. That type forwards only the capability methods it defines, so a new optional capability interface also needs a new forwarding method there. New stream message **types** need none, because they travel through `Send` and `Receive`. This design therefore adds stream types, not capabilities (2.7). |
| Loop consumer | `go-agent-loop/pkg/participants/model_runner_session*.go`, `model_runner_control.go`, `internal/sessionstate` | The session goroutine tracks the response lifecycle from `MESSAGE.START` to `MESSAGE.END`. It runs a local energy barge-in (onset, then `RESPONSE.CANCEL`). `ProviderTurnDetection()` does not stop it: under provider VAD the runner still sends the cancel (with `KeepPlayback`) and drops the response's later output as stale. Only `FullDuplex()` (2.7) turns it off. It forwards tool results as `TOOLCALL.END` and then asks for a continuation with `RESPONSE.CREATE`. It binds the continuation response as tagged, or as guessed for providers like Grok that do not echo the purpose. It sends the initial `SESSION.UPDATE` unless `InitialSessionConfigSent()`. |
| Loop tool path | `go-agent-loop/pkg/participants/tool_runner.go`, `go-agent-loop/pkg/subsystems/{interrupt_handler,tool_result_forwarder}.go`, `go-agent-loop/pkg/agentloop/agent_loop.go` | `ToolRunner.Tick` reads one `ToolBatchRequest` and executes it to completion before it reads the next, so batches are serialized. `InterruptHandler` cancels the current model execution and the current tool batch (`CancelCurrentExecution`) when a user control-plane interrupt arrives. When a `ToolAcknowledgementPolicy` is configured, a long-running tool makes the tool runner enqueue a `RESPONSE.CREATE` with the acknowledgement purpose. The live session service adds that policy in `go-agent-runtime/services/session/internal/live/start.go` (`toolAcknowledgementOption`), only when `recoversActiveResponseRejection(provider)` in `policies.go` is true. That check matches only `"openai"`. |
| Response-state hazards | `go-agent-loop/pkg/participants/internal/sessionstate/lifecycle.go` | `beginResponse` retires the in-flight response when a start arrives with a different response id. `StaleCustomerOutput` then drops later audio, text and assistant transcript for the retired id. `Continuation.OnResponseStart` binds the first response after an accepted continuation request as "guessed" when the provider does not echo the purpose. |
| Session-duration retry | `go-agent-runtime/services/sessionduration/internal/service/retry.go` | Sends `RESPONSE.CREATE` after a `MESSAGE.END` whose terminal status is eligible for a rate-limit retry. |
| Terminal vocabulary | `go-agent-loop/pkg/messages/terminal_values.go` | `TerminalReason` values: `provider_authored_completion`, `loop_synthesized_completion`, `cancellation`, `replay_divergence`, `replay_incomplete`, `replay_complete`, `session_close`, `partial_output`, `provider_close`, `terminal_failure`. `SessionCloseValue` carries a free-form `Reason` alongside them. |
| CLI | `agent-cli/internal/config/interface.go` (`ProviderOpenAI`, `ProviderGrok`, ...), `config/overrides.go`, `config/loading.go`, `services/session_host.go` (`resolvedProvider`), `services/wire/device_probe_session.go`, `transport/cli/*` (`--provider`, `--model` flags) | The provider name selects a config block (`model.openai`, `model.grok`) and its key, model and base URL. |
| CLI live host | `agent-cli/internal/services/livehost/events.go` | `selectProvider` turns any configured provider that is not `openai` or `grok` into `openai`. `providerConfig` rejects every other name. The default model is `gpt-realtime-2.1-mini`, and `/realtime` is appended to the OpenAI base URL. |
| Audio rates | `go-agent-runtime/services/audioio/internal/service/service.go` | When no rate is requested, `openai` and `grok` get 24 kHz and every other provider gets `DefaultSampleRate` (16 kHz). |
| Replay | `go-agent-runtime/services/replay/internal/strict/runtime.go` | The production offline replay factory accepts only provider `openai` and builds the OpenAI Realtime adapter. |
| Admission errors | `go-agent-runtime/services/providers/internal/admission/public.go` | `decisionError` hardcodes `Provider: "OpenAI"` in the unsupported-model error. |

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
   `openai-live`, only `gpt-live-1` is admitted (and `gpt-live-1-codex` once
   PR 9 lands, 2.3), and `gpt-live-1` is never
   admitted under `openai` (where the Realtime endpoint rejects it). Admission
   today treats every provider except `openai` as unrestricted, so the change
   must also add `openai-live` to the catalog-restricted set. Without it, any
   model string would be admitted. `decisionError` in `admission/public.go`
   must take the provider name from the decision instead of the hardcoded
   `"OpenAI"`.
3. **The name matches the vendor's own wording** ("GPT-Live", "Live API",
   `/v1/live`, the SDK's `client.live`), and reads naturally in
   `--provider openai-live --model gpt-live-1`.
4. **The credential is not part of the provider name.** The voice socket
   gets its auth headers from a credential provider (2.3), so the same
   `openai-live` provider works with a ChatGPT OAuth credential or an API key.

Rejected alternatives: `openai` plus model-based dispatch (reason 1);
`gpt-live` as the provider name (it names a model family, not a vendor
protocol, and the names would drift when `gpt-live-2` ships); `live`
(ambiguous next to "live session", which already means "real provider" in
this repo).

A collision to be aware of: `models.DefaultInputAudioTranscriptionModel` and
`audioio.DefaultTranscriptionModel` are already `"gpt-live-transcribe"`, a
Realtime transcription model. It is unrelated to GPT-Live, and the
`openai-live` provider ignores `InputAudioTranscription` entirely.

## 2.3 Auth (decided; see `chatgpt-oauth.md`)

The end goal is to sign in with `yui auth chatgpt` (ChatGPT OAuth, as in
OpenClaw and Codex) and run GPT-Live on that credential. The OAuth flow,
token store, refresh and endpoint evidence are in
[`chatgpt-oauth.md`](chatgpt-oauth.md). The user decided (2026-10-02):

- **Routing by model.** `openai-live` admits two models:
  - `gpt-live-1` runs on an **API key** over the primary WebSocket, with the
    public protocol this document specifies.
  - `gpt-live-1-codex` runs on the **ChatGPT login** over WebRTC plus a
    sideband. The call is created at
    `chatgpt.com/backend-api/codex/realtime/calls`, and the sideband
    attaches at `wss://api.openai.com/v1/live/{call_id}`. That route speaks
    the older Codex "quicksilver v2" dialect (`chatgpt-oauth.md` 3.2) and is
    built as a separate transport behind the same provider.

  A ChatGPT token cannot open `gpt-live-1`. Both references confirm this
  (`chatgpt-oauth.md` 3.1).
- **Credential order.** The ChatGPT auth store comes first, and an OpenAI
  API key (`model.openai.api_key` or `OPENAI_API_KEY`) is the fallback. With
  no `--model`, the credential found picks the model. An explicit `--model`
  narrows the order to the credential that model accepts: `gpt-live-1`
  needs an API key and fails fast, before any dial, when only a ChatGPT
  login exists.
- **Header injection.** The `openailive` provider never builds
  `Authorization: Bearer <api key>` itself. It takes a credential provider
  option, for example `WithCredentialProvider(func(ctx) (headers map[string]string, err error))`.
  The provider calls it once per dial (and once per sideband or fork dial),
  so a refreshed OAuth token is used on reconnect. For the ChatGPT login it
  is backed by `chatgptauth.Manager` and returns the bearer token,
  `chatgpt-account-id` and the route's headers. The API-key fallback simply
  returns the bearer header. The fake server checks whatever headers the
  credential provider supplies.
- **No Platform API-key exchange** from the ChatGPT login.
- **Credential check.** `providers/internal/service/credentials.go`, which
  today requires an API key for `api.openai.com`, accepts the auth store for
  `openai-live` only in PR 9, and only for `gpt-live-1-codex`.
- **Phasing.** PRs 1 to 8 ship `gpt-live-1` only, so until PR 9
  `openai-live` is **API-key only** and its default model stays `gpt-live-1`.
  PR 9 adds `gpt-live-1-codex`, the ChatGPT auth store and the ChatGPT-first
  credential order above, scoped to that model.

## 2.4 Delegation mode: client (decided)

**Client delegation was chosen** (user decision, 2026-10-02). The backend
runs Responses at `https://chatgpt.com/backend-api/codex/responses` with the
same ChatGPT token as the voice route, so one login covers voice and
reasoning. Responses delegation would not save a credential: on `gpt-live-1`
it runs on the Platform project that opened the session, which a ChatGPT
token cannot open, and the `gpt-live-1-codex` route uses only client
delegation (`chatgpt-oauth.md` 3.3). The comparison below is kept for the
record.

| | Client delegation | Responses delegation |
| --- | --- | --- |
| Who reasons and picks tools | The harness: a nested agent loop on any gateway provider (2.5) | An OpenAI Responses model that GPT-Live calls itself |
| Harness tools | All harness tools, under harness policy (approval, ordering, images) | Harness tools must be declared as `function` tools in `delegation.responses.tools`; only `function` and `web_search` are allowed |
| Context | The harness chooses exactly what the backend sees | GPT-Live supplies the conversation context |
| Result review | The harness can validate, redact or drop results before GPT-Live hears them | Backend output reaches GPT-Live directly; only function results pass through the harness |
| Credentials | One for the voice socket, plus whatever the backend provider needs | Not one credential with a ChatGPT login: hosted Responses delegation needs the Platform session that only an API key opens |
| Wire mapping | `delegation.created` to the executor; results as `commentary`/`thinking` appends | Nested `response.output_item.done` to a tool call; result as `response.item.create`, then `response.create` |
| Harness work | New executor (2.5) and two stream types | Close to the existing Realtime tool loop, but the loop hazards in 2.6.3 (serialized tool batches, the `response.create` continuation, interrupt cancellation) must be designed out for a provider whose speech has no response boundaries |
| Supported by OpenAI for | "you need to run your own workflow or review results" | "you want GPT-Live to manage requests" |

The PR 1 codec models both modes, so the choice needs no protocol work.
PRs 4 and 5 implement client delegation (2.5). Responses delegation remains
the optional PR 7.

## 2.5 Client delegation design: a dedicated asynchronous delegation executor

This section is the chosen mode (2.4). Its
loop-safety rules in 2.6.3 also bind a Responses design: the voice loop must
not open responses, tool batches or continuations that GPT-Live will never
close.

GPT-Live never names a tool. The harness needs a reasoning backend that sees
the conversation and can call the harness tools. That backend must keep
running while GPT-Live talks, because the protocol treats speech and backend
work as independent: "Live speech and delegated work continue independently",
and "Interrupting the spoken conversation leaves backend work running".

**Rule: delegations never enter the voice loop's tool path.** The
`openai-live` provider reports each client delegation as an observational
stream message, `DELEGATION.CREATED` (2.7). The runtime's live session
observer hands it to a new `livedelegation` executor. The executor runs the
backend work on its own goroutines and sends results back to the session as
`CONTEXT.APPEND` messages.

```text
GPT-Live --session.delegation.created--> openailive provider
         --DELEGATION.CREATED (no ResponseID)--> model runner --> delta stream
         --> live session observer (services/session/internal/live/observation.go)
         --> livedelegation executor (bounded worker pool)
               nested turn-based agentloop: backend provider + session toolset
         --> session.Send(CONTEXT.APPEND{commentary or thinking, delegation_id})
         --> openailive provider --session.commentary.append--> GPT-Live
```

**Why not the synthetic `live_delegate` tool call proposed in the first
revision.** Review found three ways it breaks the loop:

1. **Response-id conflict.** A synthetic `MESSAGE.START` with id
   `del:<id>` reaches `sessionstate.State.beginResponse` while a speech
   segment (`live_seg_n`) is open. `beginResponse` retires the segment, and
   `StaleCustomerOutput` then drops the segment's later audio and transcript.
   GPT-Live normally keeps talking while it delegates ("I'll check that"), so
   this would happen in most conversations.
2. **No concurrency.** Each delegation would be a separate `ToolBatchRequest`.
   `ToolRunner.Tick` runs one batch at a time, so a slow lookup would block
   every later delegation.
3. **Loop paths that assume Realtime.** The tool path brings in a
   `RESPONSE.CREATE` continuation (a no-op here, which leaves the next
   unrelated speech segment tagged as a guessed continuation), the
   tool-acknowledgement policy, and `InterruptHandler` cancelling the tool
   batch. Each of these conflicts with GPT-Live semantics (2.6.3).

The executor avoids all three by design: delegations carry no response id,
never become tool batches, and never ask for a response.

Rejected alternative: keep the tool path but **serialize** delegations
explicitly, closing the open segment before emitting `del:<id>`. That fixes
problem 1 only. Delegations would still queue behind each other, and
continuation and interrupt handling would still need special cases.

**Executor behaviour.**

- **Context window.** The delegation event carries no task text. The provider
  keeps a bounded ring of transcript fragments (speaker, text, `start_ms`,
  `end_ms`). It emits `DELEGATION.CREATED` once either of these happens:
  - an input-transcript fragment with `end_ms >= offset_ms` has arrived, or
  - a settle window `D` has passed on the injected clock (proposed 400 ms,
    open question Q5).

  The value carries the transcript snapshot:

  ```json
  {
    "delegation_id": "del_abc123",
    "offset_ms": 3600,
    "transcript": [
      {"speaker": "user", "text": "A table for two at seven, please.", "start_ms": 1600, "end_ms": 3400}
    ]
  }
  ```

  The executor also reads the session's message history, so short replies
  such as "yes" stay resolvable.
- **Concurrency.** A bounded worker pool. The default limit is 2 (open
  question Q11). Work that waits beyond the limit is queued, not dropped.
  Each worker runs a nested turn-based `agentloop` using:
  - the configured stateless backend provider and model (Q8). It stays configurable. With a ChatGPT login its default is the `openai-chatgpt` provider (PR 3a) with the account's default Codex Responses model;
  - the backend prompt prefix from 1.10.1;
  - the session's ordinary tool executor;
  - its own budget (iterations, wall time on the injected clock, tokens).

  PR 4 must verify that the tool executor is safe to call from concurrent
  workers. Tools that are not are serialized by a per-tool lock in the
  executor, never by the voice loop.
- **Lifetime.** Each worker's context comes from the session lifetime, not
  from the loop's tool execution context. `InterruptHandler`'s
  `toolCanceller.CancelCurrentExecution()` therefore cannot reach it. Workers
  are cancelled only:
  - by the executor's own `Cancel(delegationID)`, used for task revisions;
  - when the session terminates, after the graceful close has drained.
- **Results.** A worker returns its final result as
  `CONTEXT.APPEND{Kind: commentary, DelegationID: id}` and progress as
  `Kind: thinking`. Each append is at most 500 tokens: the backend is told to
  summarize, and the provider splits content as a safety net (Q6). If the
  result belongs to a superseded task revision, it is sent as `thinking`,
  never as `commentary`. A failed or over-budget delegation sends a short
  `commentary` stating the failure, so GPT-Live does not wait forever.

## 2.6 Event mapping onto the harness session contract

### 2.6.1 Inbound (server to `StreamMessage`)

| GPT-Live event | Harness output |
| --- | --- |
| `session.started` | Consumed by `ConnectSession` during the handshake (2.8). It then emits `SESSION.OPEN` and `SESSION.CREATED(session.id, model)`. |
| `session.updated` | `SESSION.UPDATED(session.id)` |
| `session.output_audio.delta` | Opens a speech segment if none is open: `MESSAGE.START`, then `AUDIO.START`, both with `ResponseID = live_seg_<n>`. Then `AUDIO.DELTA(bytes, media type from the configured format)` with the same id. |
| `session.output_transcript.delta` | `TRANSCRIPT.DELTA(Role=assistant, ResponseID=live_seg_<n>)`. It opens a segment if none is open. |
| `session.input_transcript.delta` | `TRANSCRIPT.DELTA(Role=user)` with a synthetic utterance id (`live_utt_<n>`), announced once with `INPUT_ITEM.ADDED`. The utterance closes with `TRANSCRIPT.END(Role=user)` after a server-timeline gap of `G` ms or more. User transcripts are never treated as stale output, so they are safe during a segment. |
| `session.delegation.created` (`target:"client"`) | After the settle window: `DELEGATION.CREATED` with **no `ResponseID`**. It is not a response stream type (2.7), so it cannot open, retire or tag a response. |
| `session.delegation.created` (`target:"responses"`) and `response.event` | Ignored and logged. They occur only in Responses mode, which was not chosen (2.4). A Responses design must map them without opening a response id while a speech segment is open. |
| `*.appended`, `session.input_audio.muted` / `unmuted` | Consumed inside the provider. Each settles the pending command it acknowledges, matched by `client_event_id` or in FIFO order. No stream message. |
| `session.usage.updated` | Kept as the latest cumulative seconds and context ratio. The `USAGE.INFO` mapping is open question Q7. |
| `session.closed` | Closes any open segment, then emits `SESSION.CLOSE` (see the table below). |
| `error` | `ERROR` with code, message, param and client event id. A startup error fails `ConnectSession` instead. |
| `info`, `transport.*` | Logged only. |

`session.closed` uses only existing `TerminalReason` values. The GPT-Live
detail goes into `SessionCloseValue.Reason`, verbatim (for example
`"expired"`). Provenance is `provider`.

| `session.closed.reason` | `TerminalReason` | Output state |
| --- | --- | --- |
| `close_requested` | `session_close` | `not_applicable` |
| `expired` | `provider_close` | `not_applicable` |
| `content` | `provider_close` | `partial` if a segment was open, otherwise `not_applicable` |
| `remote_hangup` | `provider_close` | `not_applicable` |
| `connection_lost` | `terminal_failure` | `partial` if a segment was open, otherwise `not_applicable` |
| (socket closed without `session.closed`) | `terminal_failure`, with `Reason = "finalization_unconfirmed"` | as above |

### 2.6.2 Speech segments

The loop's lifecycle needs a `MESSAGE.START`/`MESSAGE.END` boundary for each
response. GPT-Live has none, so the provider synthesizes **speech segments**.
Segments are the only response ids the provider ever emits, and at most one is
open at a time.

A segment (`ResponseID = "live_seg_<n>"`) opens at the first assistant audio or
transcript after an idle period. It closes, emitting `AUDIO.END`,
`TRANSCRIPT.END` and `MESSAGE.END` with status `completed`, at the first of:

- (a) a gap of `G` ms or more on the server timeline between output transcript
  fragments;
- (b) an idle timer of `G` ms on the injected clock after the last output event
  (primary audio has no timing);
- (c) `session.closed` or a transport close.

The proposed `G` is 600 ms (Q5). Audio duration is measured from the byte
count at the configured rate, not from arrival time.

Because delegations no longer carry response ids, a delegation that arrives
mid-segment cannot retire the segment. A behaviour test pins this (2.11).
Segments always end `completed`, so the session-duration rate-limit retry,
which acts only on a failed `MESSAGE.END`, never fires for this provider.

### 2.6.3 Loop paths that assume Realtime, and how each is handled for `openai-live`

| Loop path | Risk with GPT-Live | Handling |
| --- | --- | --- |
| Tool calls and `ToolRunner` | GPT-Live emits no tool calls; serialized batches would block delegations. | The provider never emits `TOOLCALL.*`. Delegations run in the `livedelegation` executor (2.5). The voice loop's tool runner stays idle. |
| Tool-result continuation (`RESPONSE.CREATE` with continuation purpose) | It is a no-op on the Live wire, so `Continuation` would stay `Requested`, and the next unrelated segment would be tagged `tool_continuation` as a guessed continuation. | It cannot be triggered: with no tool calls in the loop there are no tool results to continue. As a fail-closed backstop, `SupportsResponseRequests()` returns `false` (so `RequestSessionResponse` sends nothing), and `Send(RESPONSE.CREATE)` returns a terminal-failure outcome with a logged error, never a silent success. A test asserts that no segment ever carries a `ResponsePurpose`. |
| `ToolAcknowledgement` policy | A progress-ack `RESPONSE.CREATE` would be a no-op, the next segment would be attributed to the acknowledgement, and continuations would stay deferred behind it. | Already off for `openai-live`, and no code change is needed. `live/start.go` adds `toolAcknowledgementOption` only when `recoversActiveResponseRejection` (`live/policies.go`) is true, and that check matches only `"openai"`. A regression test pins this, so a later broadening of that check cannot silently enable it. GPT-Live already acknowledges delegated work in speech, and the executor sends progress as `thinking`. The backstop above also covers this. |
| `InterruptHandler` (`toolCanceller.CancelCurrentExecution()`, `modelCanceller.CancelCurrentExecution()`) | Cancelling would kill delegations, but GPT-Live keeps backend work running across interruptions. | Delegations are outside the tool runner, so the tool cancel has nothing to cancel. A user control-plane interrupt therefore does not cancel backend work. The executor cancels a delegation only on a task revision or at session end (Q12). The model-runner cancel keeps its current meaning. |
| Local energy barge-in (`RESPONSE.CANCEL` from onset) | It would fight GPT-Live's own turn-taking and its backchannels. `ProviderTurnDetection()` alone is not enough: under provider VAD the runner still cancels on onset (with `KeepPlayback`) and then drops the response's later output as stale. The session therefore also reports the new `FullDuplex()` capability (2.7), and the runner runs no local barge-in for a full-duplex session. An explicit `RESPONSE.CANCEL` (for example a host stop) has no wire event. It interrupts local playback unless `KeepPlayback` is set, and marks the open segment cancelled so its later output is dropped locally. |
| Initial `SESSION.UPDATE` | Live startup fields are immutable. | `InitialSessionConfigSent()` returns `true`, so the runner never sends it. A later `SESSION.UPDATE` is accepted with no wire event and logged. |
| Session-duration retry | It acts on a failed `MESSAGE.END`. | Segments always end `completed` (2.6.2), so the retry is never eligible. |

### 2.6.4 Outbound (`StreamMessage` to GPT-Live)

| Harness input | GPT-Live wire |
| --- | --- |
| `AUDIO.DELTA` | `session.input_audio.append`. A trailing odd byte is held back and joined to the next PCM chunk, as in the official sample. G.711 bytes pass through unchanged. |
| `MESSAGE.END` (the Realtime "commit and respond") | **No wire event.** Success is reported locally. GPT-Live decides when to speak. |
| `RESPONSE.CANCEL` | **No wire event.** See 2.6.3. |
| `RESPONSE.CREATE` | **No wire event.** Terminal-failure outcome (see 2.6.3). In Responses mode (not chosen, 2.4) the provider would send `response.create` itself after all function results, not on the loop's request. |
| `CONTEXT.APPEND` (new) | `session.instructions.append`, `session.thinking.append` or `session.commentary.append`. `delegation_id` is the value's id, or `null`. |
| `TOOLCALL.END` | Terminal-failure outcome, logged. GPT-Live has no generic tool-result channel. |
| `SESSION.UPDATE` | No wire event, success (see 2.6.3). |
| `TEXT.DELTA` (typed user text) | Undecided (Q4). The proposal: the live session routes it to the `livedelegation` executor as a user input, and mirrors it as `CONTEXT.APPEND{thinking, null, "The user typed: ..."}`. Until that is decided, the provider returns a terminal failure. |
| `Close()` | Graceful close: send `session.close`, wait for `session.closed` (bounded by an injected-clock timeout, 15 s by default), then close the socket. |

**Capabilities exposed by the session:**

| Capability | Value |
| --- | --- |
| `ProviderTurnDetection()` | `true` |
| `FullDuplex()` | `true` (new, see 2.7) |
| `SupportsResponseRequests()` | `false` |
| `SupportsCompleteMessages()` | `false` |
| `InitialSessionConfigSent()` | `true` |
| `InputAudioSampleRate()` | the configured rate |
| `LocalPlayback`, `InterruptLocalPlayback`, `RTCMedia` | inherited from the shared skeleton |

All of these except `FullDuplex()` are existing capabilities, so the wrappers already forward them; `FullDuplex()` is forwarded by `messages.SessionCapabilities`.

## 2.7 New interface surface

The plan keeps new surface small. It adds one capability interface,
`messages.SessionFullDuplex` (`FullDuplex() bool`, implemented in PR 2), with
its forwarding method in `messages.SessionCapabilities`; it is also part of
`messages.BargeInCapableSession`. The session model runner runs no local
barge-in against a session that reports it.

1. **New gateway package** `go-llm-gateway/pkg/providers/openailive`. No
   change to `SessionProvider`.
2. **Two new stream types in `go-agent-loop/pkg/messages`**, both carried by
   `Send` and `Receive`:
   - `DELEGATION.CREATED` (inbound, observational) with
     `DelegationCreatedValue{ID, OffsetMS, Target, Task, Transcript []TranscriptFragment}`.
     `Task` is the task text when the dialect sends one: the public dialect
     never does, the quicksilver `delegation.created` item carries content.
     It is not a response stream type: it must not be added to
     `sessionstate.IsResponseStreamType` or to the customer-output set.
     Message reconstruction and the coordinator's history ignore it, as they
     do `VAD.*`. It **must be added to `messages.MustDeliver`**
     (`go-agent-loop/pkg/messages/buffers.go`). `MustDeliver` is an allowlist,
     and the session outbox (`participants/model_runner_session_outbox.go`)
     drops any unlisted delta when it is full. A lost delegation would never be
     answered, because GPT-Live has no delegation timeout.
   - `CONTEXT.APPEND` (outbound) with
     `ContextAppendValue{Kind: instructions or thinking or commentary, DelegationID *string, Content string}`.
     It is used for delegation results and progress, greetings, disclosures,
     guardrail redirects and UI context. Other providers return a
     terminal-failure outcome, and senders must check it.
3. **Generalize the shared skeleton** (`internal/realtime`). The framing is the
   same: flat JSON objects with a `type` field. It needs two additive hooks:
   - a `Config.Close` hook (or a `GracefulClose(ctx)` helper) for the
     `session.close` handshake;
   - an injected `clock.Scheduler` for the segment, settle and close timers.
4. **`RealtimeModel` metadata.** Add `Duplex bool` and `Delegation string`
   (`"client"` or `"responses"`). Hosts use them to skip turn-detection,
   transcription, tool-acknowledgement and tool config, and to reject flags
   that have no effect.
5. **Runtime `livedelegation` service** (`go-agent-runtime/services/livedelegation`):
   the bounded executor, its wire, and the observer hookup in
   `services/session/internal/live/observation.go`.
6. **Session config input.** Live-only options (`store`, initial `input`
   history, delegation mode) travel in the existing raw
   `models.SessionConfig.Config` as a typed `openailive.Options` JSON object.

## 2.8 Connect handshake

Unlike `openai`, which writes `session.update` and returns at once,
`openailive.ConnectSession` will:

1. dial `wss://api.openai.com/v1/live/sessions` (overridable for the fake
   server and for Azure). The headers come from the credential provider (2.3), never from a hardcoded API key;
2. write `session.start`, built strictly from `models.SessionConfig` (2.9);
3. read frames until `session.started` or `error`, bounded by `ctx`. On
   `error`, close the socket and return a typed `StartupError{Code, Param, Message}`;
4. then start the shared loops and queue `SESSION.OPEN` and `SESSION.CREATED`.

Waiting here follows the documented rule ("Wait for session.started before
sending audio") and turns a startup failure into a connect error. It costs
one round trip before `ConnectSession` returns.

## 2.9 `session.start` construction

| `models.SessionConfig` field | `session` field |
| --- | --- |
| `Model` | `model`. Must be `gpt-live-1`; admission already enforces this. |
| `Instructions` | `instructions`, omitted when blank. The provider rejects text over 64 KiB as a cheap stand-in for the 16,384-token limit (**UNCONFIRMED** tokenizer). |
| `Voice` | `audio.output.voice`, omitted when empty (server default `marin`) |
| `InputAudioFormat`/`InputAudioSampleRate` and the output pair | `audio.format`. Input and output must match, otherwise it is a config error. `pcm16` at 24000 becomes `{"type":"audio/pcm","rate":24000}`; `pcm16` at 16000 becomes `{"type":"audio/pcm","rate":16000}`; `g711_ulaw` becomes `{"type":"audio/pcmu","rate":8000}`; `g711_alaw` becomes `{"type":"audio/pcma","rate":8000}`. Any other rate is rejected. |
| `Config` (`openailive.Options`) | `delegation` (default `{"type":"client"}`, sent explicitly), `store`, `input` |
| `Tools`, `TurnDetection`, `InputAudioTranscription`, `Modalities`, `ReasoningEffort` | **Never sent.** Tools go to the delegation backend; the others are ignored with a debug log. |

The voice registry (`agent-cli/internal/services`, see
`s2s-realtime-voice-configuration.md`) must hold a separate set of Live voices
for each provider. The 31 names are listed in 1.5.

## 2.10 Files to add or change

PR 1 (protocol, codec, fake server, catalog):

- `go-llm-gateway/pkg/providers/openailive/doc.go`: package doc that links to this design.
- `go-llm-gateway/pkg/providers/openailive/protocol.go`: typed structs for every client and server event in 1.6 and 1.7, plus `SessionConfig`, `SessionResource`, `AudioFormat`, `Delegation`, `ResponsesDelegationConfig`, `InitialItem` and `Error`.
- `go-llm-gateway/pkg/providers/openailive/codec.go`: `EncodeClientEvent` and `DecodeServerEvent`. An unknown `type` decodes to `UnknownEvent` and is not an error. The decoder accepts the forms that differ between OpenAI and Azure.
- `go-llm-gateway/pkg/providers/openailive/start.go`: `models.SessionConfig` plus `Options` to `session.start` (2.9).
- `go-llm-gateway/pkg/providers/openailive/{codec,start}_test.go`, with `testdata/protocol/*.json`: the literal Part 1 examples.
- `go-llm-gateway/pkg/providers/openailive/fakelive/server.go`: a scripted fake GPT-Live server (a test-support package, named to the test-support rule).
- `go-agent-runtime/services/providers/models.go`: add `OpenAILiveProvider = "openai-live"` and `OpenAILive1Model = "gpt-live-1"`.
- `go-agent-runtime/services/providers/internal/catalog/catalog.go`: the `openai-live` model list.
- `go-agent-runtime/services/providers/internal/admission/{admission,public}.go` and `internal/service/models.go`: restrict `openai-live` to the catalog, and stop hardcoding `"OpenAI"` in `decisionError`.

PR 2 (provider session without delegation):

- `go-llm-gateway/pkg/providers/openailive/{provider,options,session,session_inbound,session_outbound,segments,close}.go`
- `go-llm-gateway/pkg/providers/internal/realtime/`: the close hook and clock injection.
- `go-agent-runtime/services/providers/internal/service/session.go`: `openai-live` branches in `buildSessionProvider` and `sessionDialer`, passing a credential provider (2.3).
- `go-agent-runtime/services/providers/internal/service/credentials.go`: no change. Until PR 9, `openai-live` is API-key only, and the existing check already requires a key for `api.openai.com`.
- `go-agent-runtime/services/audioio/internal/service/service.go` (and the `audioio` contract): treat `openai-live` as a 24 kHz provider. Today it would fall back to `DefaultSampleRate`, 16 kHz.
- `agent-cli/internal/services/livehost/events.go`:
  - `selectProvider` must stop turning `openai-live` into `openai`;
  - `providerConfig` must accept it and resolve the `model.openai` API key through the credential provider (API-key only until PR 9, which adds the ChatGPT store and the ChatGPT-first order for `gpt-live-1-codex`);
  - the default model stays `gpt-live-1` on an API key until PR 9; PR 9 makes it follow the credential (2.3);
  - the `/realtime` suffix rule must not apply; the Live path is `/live/sessions`.
- `agent-cli/internal/config/{interface,overrides,loading}.go`, `services/session_host.go`, `services/wire/device_probe_session.go`, and the `--provider` help text: `ProviderOpenAILive = "openai-live"`.

PR 3 (stream vocabulary):

- `go-agent-loop/pkg/messages/{stream_types,session_values}.go`: `DELEGATION.CREATED` and `CONTEXT.APPEND`, plus their exclusion from response state and reconstruction.
- `go-agent-loop/pkg/messages/buffers.go`: add `StreamTypeDelegationCreated` to `MustDeliver`.
- Capture decoders, each of which rejects unknown stream types. Register both new types in:
  - `go-agent-runtime/services/recording/internal/evidence/provider_capture_writer.go` (the unknown-type error near line 275);
  - `go-agent-runtime/services/replay/internal/capture/stream_message.go` (`unmarshalStreamMessageValue`, near line 80);
  - `go-llm-gateway/pkg/testing/session_message.go` (near line 152).
- Exhaustive switches over `StreamMessageType` must list both new types. This includes `observeFiniteResponseMessage` in `go-agent-runtime/services/session/internal/live/observation.go` (near line 339). PR 3 must find every other exhaustive switch over the type, for example by grepping for `exhaustive`-checked switches on `StreamMessageType` and letting `make lint` flag the rest.
- The `openailive` mapping for both.

PR 3a (`openai-chatgpt` text provider, the delegation backend):

- `go-llm-gateway/pkg/providers/openaichatgpt/{provider,options,request,stream,models}.go`: an `llmproviders.Provider` (`Infer`, `InferStream`) over `POST https://chatgpt.com/backend-api/codex/responses` (Responses SSE). It sends `store:false`, `stream:true`, `instructions` and `include:["reasoning.encrypted_content"]`, plus the headers `Authorization`, `chatgpt-account-id`, `originator`, `OpenAI-Beta: responses=experimental` and `accept: text/event-stream`, all taken from a credential provider backed by `chatgptauth.Manager`. `models.go` lists the account's models from `GET /backend-api/codex/models?client_version=...`. Plan-allowance errors (`usage_limit_reached`, `usage_not_included`) map to typed errors.
- `go-llm-gateway/pkg/providers/openaichatgpt/*_test.go`: an `httptest` fake Responses SSE server; no real network.
- `go-agent-runtime/services/providers/internal/service/service.go` (`buildConfiguredProvider`) and `models.go`: the `openai-chatgpt` provider name. Its models come from the account's list, not the static catalog.
- `agent-cli/internal/config/{interface,overrides,loading}.go` and `services/session_host.go` (`resolvedProvider`): `ProviderOpenAIChatGPT = "openai-chatgpt"`, which reads the auth store at `<config-dir>/auth/chatgpt.json`; `yui ask`/`yui chat --provider openai-chatgpt`.

PR 4 (client-delegation executor):

- `go-agent-runtime/services/livedelegation/...` (service, worker pool, nested `agentloop`, wire).
- `go-agent-runtime/services/session/internal/live/observation.go`: route `DELEGATION.CREATED` to the executor.

PR 6 (replay):

- `go-agent-runtime/services/replay/internal/strict/runtime.go`: accept `openai-live` and build the `openailive` adapter (today it is OpenAI-only).
- `go-llm-gateway/pkg/testing/testdata/session-fixtures/openai-live/*.session.json`.

PR 9 (`gpt-live-1-codex` on the ChatGPT login; `chatgpt-oauth.md` 3.2 and 3.5):

- `go-llm-gateway/pkg/providers/openailive/quicksilver/{events,codec,session}.go` (landed in #638): the quicksilver-v2 dialect, with client `input_audio.append`, `session.update`, `session.context.append` and `delegation.context.append` (with `channel`), and server `output_audio.delta`, `input_transcript.added`, `output_transcript.added`, `turn.done`, `delegation.created`, `output_audio_buffer.cleared` and `error`, mapped onto the same session state machine and stream types as the public dialect.
- `go-llm-gateway/pkg/providers/openailive/codexrtc/{credential,call,peer,sideband}.go` (landed in #638): call creation with JSON `{sdp, session}` at `POST https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas`, with `OpenAI-Alpha: quicksilver=v2`. The response is a raw SDP answer; the call id comes from `Location`. A provider-side pion WebRTC peer carries the audio, with Opus through `go-audio/pkg/codec`. The sideband dials `wss://api.openai.com/v1/live/{call_id}` and redials through the credential provider.
- `go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex` (test support, landed in #638): a fake codex backend for call creation and the sideband, answered by an in-process pion peer on a pion `vnet` virtual network for media tests.
- Still to do in PR 9: the `openailive` session state machine over the `codexrtc` transport (handshake on `session.started`, the stream-type mapping, 48 kHz to 24 kHz resampling, sideband reconnect with backoff).
- `go-agent-runtime/services/providers/internal/catalog/catalog.go`: add `gpt-live-1-codex` under `openai-live`, with admission tests.
- `go-agent-runtime/services/providers/internal/service/{session,credentials}.go`: route `gpt-live-1-codex` to the codex transport, and accept the ChatGPT auth store as its credential.
- `agent-cli/internal/services/livehost/events.go`: `providerConfig` resolves the ChatGPT store first and the API key second (2.3); `--model` narrows the order; the default model follows the credential.
- The voice registry: the codex voices (`cove` as default, `arbor`, `breeze`, `ember`, `juniper`, `maple`, `sol`, `spruce`, `vale`).

## 2.11 Test strategy

All tests are deterministic, run each unit in 3 minutes or less, and use
virtual time (`testing/synctest` or `go-audio/pkg/clock.Deterministic`), with
no wall-clock sleeps.

1. **Codec goldens (PR 1).** Every literal JSON block in Part 1 lives under
   `testdata/protocol/`. Each decodes to the expected typed value and
   re-encodes to semantically equal JSON. Negative cases:
   - an odd-length PCM payload is refused by the encoder helper;
   - an unknown server `type` decodes to `UnknownEvent`;
   - `delegation_id` is always present (as `null` or a string) on every
     append.
2. **Strictness test for `session.start` (PR 1).** The built `session`
   object's key set is checked against an allowlist taken from 1.5.
3. **Admission (PR 1).** `openai-live` admits only `gpt-live-1` (PR 9 adds `gpt-live-1-codex`). `openai`
   does not admit `gpt-live-1`. The unsupported-model error names the
   requested provider.
4. **Fake GPT-Live server (`fakelive`, PR 1, used from PR 2).** It runs
   in-process, over a `transport.Conn` pair and over an `httptest` gorilla
   WebSocket server. The fake is a script of steps on virtual time. It:
   - validates the auth headers the credential provider supplies (an API-key bearer and a stub OAuth credential) and the empty query string;
   - validates the strict `session.start` and answers `session.started` or an
     `error`;
   - records audio appends;
   - plays scripted output audio, transcripts and `delegation.created`;
   - acknowledges appends, and can withhold an ack;
   - emits `session.closed` with a configurable reason, or drops the socket.

   It also implements `transporttest` conformance.
5. **Behaviour tests on virtual time.** They run against `fakelive` through
   the real model runner and live session service.
   - **PR 2:**
     - segments open and close at `G` and at server-timeline gaps, and
       `MESSAGE.END` is emitted exactly once per segment;
     - full-duplex overlap (a user fragment during a segment) causes no
       `RESPONSE.CANCEL` and drops no segment output;
     - every `session.closed` reason maps to the `TerminalReason`, `Reason`
       and output state in 2.6.1; a socket drop before `session.closed` yields
       `terminal_failure` with `finalization_unconfirmed`;
     - a startup `error` fails `ConnectSession` with a typed error;
     - regression pin: with an `openai-live` session, the live session service
       configures no tool-acknowledgement policy (today
       `recoversActiveResponseRejection` matches only `"openai"`);
     - `Send(RESPONSE.CREATE)` and `Send(TOOLCALL.END)` return a terminal
       failure, and no stream message ever carries a `ResponsePurpose`;
     - the `livehost` provider selection keeps `openai-live`, picks
       `gpt-live-1` by default, and does not append `/realtime`;
     - `audioio` resolves 24 kHz for `openai-live`.
   - **PR 3 and PR 4** (the PR 4 cases assume client delegation; a Responses design needs the same delegation-during-segment and interrupt cases):
     - **delegation during an open segment:** the fake streams segment
       audio, emits `delegation.created` mid-segment, then keeps streaming.
       All of the segment's audio and transcript reach the delta stream, the
       segment closes normally, and `DELEGATION.CREATED` arrives with no
       `ResponseID`;
     - **outbox pressure:** with the session outbox full, `DELEGATION.CREATED`
       still reaches the observer: the write waits for capacity and is not
       dropped. `MustDeliver(DELEGATION.CREATED)` returns true;
     - both new types round-trip through the three capture decoders (recording
       evidence, replay capture, gateway session message);
     - a delegation that arrives before its transcript waits for the settle
       window, then is reported exactly once;
     - **concurrency:** two delegations, where the first backend run is
       blocked on a fake tool until the second completes. The second
       `commentary.append` is sent first, both carry their own
       `delegation_id`, and speech segments keep flowing throughout;
     - with a pool limit of 1, the second delegation queues and is not
       dropped;
     - **interrupt independence:** a user control-plane interrupt during a
       running delegation cancels nothing in the executor, and the result is
       still delivered;
     - session end cancels running workers after the graceful close.
   - **PR 5:**
     - a stale-revision result is sent as `thinking`, not `commentary`;
     - an over-budget delegation sends its failure commentary;
     - an over-long result is split into appends of at most 500 tokens.
6. **Replay fixtures (PR 6).** Version-2 `.session.json` captures with
   `provider.name: "openai-live"`, sealed with `SealSessionCapture`. They
   cover a greeting, a delegation during speech with a commentary result, an
   error, and a graceful close. The strict offline replay runtime must replay
   them.
7. **Opt-in live smoke test (PR 6).** Guarded by an env var and never run in
   CI. It checks for `session.started`, sends 1 s of silence, then checks for
   `session.closed` with `close_requested`.

## 2.12 Phased PR plan

| PR | Scope | Mergeable because |
| --- | --- | --- |
| **1** | `openailive` protocol types, codec, `session.start` builder, goldens, `fakelive`, the catalog entry `openai-live`/`gpt-live-1`, the admission restriction and the error provider name. | No production path reaches it yet: `BuildSession` still rejects the provider. Pure additions plus tests. |
| **2** | The `openailive` provider session: handshake, inbound audio and transcripts, speech segments, outbound audio, the fail-closed mappings, graceful close, and the close-reason mapping. Also the skeleton hooks, provider-service branches, the `audioio` rate, the tool-acknowledgement regression pin (no code change), the `livehost` and CLI provider plumbing, and `--provider openai-live`. Delegations are logged and dropped. | An end-to-end voice conversation works with no tools. |
| **3** | `DELEGATION.CREATED` and `CONTEXT.APPEND` stream types: kept out of response state and reconstruction, `DELEGATION.CREATED` added to `MustDeliver`, both registered in the three capture decoders and every exhaustive switch, and their `openailive` mappings. Plus the transcript ring, the settle window, and an optional greeting. | New vocabulary that other providers decline. |
| **3a** | The `openai-chatgpt` text provider: Responses over `chatgpt.com/backend-api/codex/responses` on the ChatGPT login (`chatgpt-oauth.md` 2 and 4.4). It is the default delegation backend when a ChatGPT login exists (Q8). | `yui ask`/`yui chat` and the delegation backend run on the ChatGPT login with no API key. |
| **4** | Client delegation (2.4): the `livedelegation` asynchronous executor: worker pool, nested `agentloop`, observer hookup, result and progress appends, session-scoped lifetime. | Tools work through the harness backend, concurrently and independently of speech and interrupts. |
| **5** | Task revisions and `Cancel(delegationID)`, stale-result handling, budgets and failure commentary, result splitting, mute and unmute. | Hardening on top of PR 4. |
| **6** | Replay and recording support (`replay/internal/strict/runtime.go`), synthetic fixtures, and the opt-in smoke test. | Regression coverage. |
| **7** (optional) | The delegation mode not chosen in 2.4, if it is still wanted. A Responses design must avoid the hazards in 2.6.3. | Only if Q3 asks for both modes. |
| **8** (optional) | Sideband attach, WebRTC session creation, `store` and fork. | Only if a browser or telephony host needs them. |
| **9** | The `gpt-live-1-codex` route on the ChatGPT login (2.3): a provider-side pion WebRTC peer with Opus through `go-audio/pkg/codec`, call creation on the ChatGPT backend, the sideband, and a quicksilver-v2 codec behind the same session state machine. See `chatgpt-oauth.md` 3.2 and 3.5. | The ChatGPT login runs voice with no API key. |

## 2.13 Open questions for the user

1. **Q1 Credentials and access.** Does the project key in `credentials` have
   GPT-Live access (Tier 1 or higher)? May PR 6 record one real, sanitized
   capture?
2. **Q2 Naming and auth.** Is `openai-live` with `gpt-live-1` acceptable?
   **Decided (2.3):** the `yui auth chatgpt` store first with an API-key
   fallback; `gpt-live-1-codex` on the ChatGPT login, `gpt-live-1` on an
   API key.
   Is Azure Foundry a target?
3. **Q3 Delegation mode. Decided (2.4):** client delegation, with the
   backend on Responses over the ChatGPT backend. Should Responses delegation
   also be built later (PR 7)?
4. **Q4 Typed text input.** Options: (a) unsupported, failing fast;
   (b) route it to the delegation executor and mirror it as `thinking`;
   (c) send it as `commentary`. The proposal is (b).
5. **Q5 Timing constants.** Should the segment gap `G` (600 ms) and the
   settle window `D` (400 ms) be configurable per session?
6. **Q6 Long results.** Should over-500-token results be summarized by the
   backend prompt, split by the provider, or both? The proposal is both.
7. **Q7 Usage accounting.** Should `USAGE.INFO` gain a voice-seconds field, or
   should Live usage stay provider-internal?
8. **Q8 Backend model. Decided:** the backend stays configurable. With a
   ChatGPT login its default is the `openai-chatgpt` provider (PR 3a) with
   the account's default Codex Responses model.
9. **Q9 Barge-in observability.** Is it acceptable that barge-in and
   turn-latency metrics are approximate (segment-derived) for this provider?
10. **Q10 Greeting default.** Should a session send a default greeting
    instruction after `session.started` (needs PR 3)?
11. **Q11 Delegation concurrency.** What should the worker-pool limit be
    (proposed 2)? Are there harness tools that must never run concurrently
    with each other?
12. **Q12 Interrupt policy.** The design keeps backend work running when the
    user interrupts, which matches GPT-Live. Should an explicit user
    interrupt instead cancel all running delegations?

Also **UNCONFIRMED** in the protocol, and to be checked against the live
service in PR 6: maximum session duration, output pacing, the full error-code
list, the beta header requirement, an ephemeral-token flow, model behaviour
when a client delegation is never answered, and the moderation cut-off error
code.
