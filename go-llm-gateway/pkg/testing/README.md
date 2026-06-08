# go-llm-gateway/pkg/testing

Record/replay utilities for deterministic testing of LLM provider interactions.

For committed `.session.json` captures, follow the
[Session Fixture Authoring Guide](./session-fixture-authoring.md). It defines required
`session.fixture_provenance` metadata, fixture provenance categories, sanitization
rules, and the distinction between normalized `stream_message` payloads and provider
wire `websocket_message` payloads.

`go-llm-gateway/pkg/testing` is the authoritative owner for committed shared
`.session.json` replay fixtures. The canonical repository root for those shared
fixtures is `go-llm-gateway/pkg/testing/testdata/session-fixtures`.

Package-local fixtures may still exist elsewhere when they only prove
module-private behavior, but sibling modules should not treat another package's
private `testdata` directory as the shared fixture contract.

Validate committed session fixtures before review:

```sh
go run ./cmd/session-fixture-validator ./pkg/testing/testdata/session-fixtures
```

## Capture File Types

There are two independent capture file types, typically written as a pair:

| File suffix        | Contents                          | Produced by            | Consumed by           |
|--------------------|-----------------------------------|------------------------|-----------------------|
| `.http.json`       | HTTP request/response pairs       | `RecordRoundTripper`   | `ReplayRoundTripper`  |
| `.session.json`    | Bidirectional session events      | `SessionRecorder` or `RecordingWebSocketDialer` | `SessionReplayer` or `ReplayWebSocketDialer` |

When both HTTP and session traffic occur in a single agent invocation, both files are written alongside each other at the same base path.

## HTTP Capture Format (`.http.json`)

A JSON array of request/response pairs:

```json
[
  {
    "request": {
      "method": "POST",
      "url": "https://api.openai.com/v1/chat/completions",
      "headers": { "Content-Type": ["application/json"] },
      "body": "{\"model\":\"gpt-4\",\"messages\":[...]}"
    },
    "response": {
      "status_code": 200,
      "status": "200 OK",
      "headers": { "Content-Type": ["application/json"] },
      "body": "{\"id\":\"chatcmpl-...\",\"choices\":[...]}"
    }
  }
]
```

## Session Capture Format (`.session.json`)

A JSON envelope containing capture metadata and ordered bidirectional session records. Each record represents a single message that was either sent by the client or received from the server during a WebSocket session. The same format represents OpenAI Realtime and Grok Realtime traffic by storing provider metadata and raw provider event names in each record.

```json
{
  "version": 1,
  "provider": {
    "name": "grok",
    "model": "grok-realtime"
  },
  "session": {
    "id": "sess_sanitized",
    "started_at_utc": "2026-04-11T00:00:00Z",
    "fixture_provenance": "synthetic"
  },
  "records": []
}
```

### Event Schema

```json
{
  "sequence": 1,
  "direction": "client_to_server | server_to_client",
  "timestamp_ms": 0,
  "type": "session.created",
  "payload_type": "stream_message | websocket_message",
  "payload": { ... }
}
```

| Field          | Type    | Description |
|----------------|---------|-------------|
| `sequence`     | integer | Logical event order within the session capture |
| `direction`    | string  | `"client_to_server"` or `"server_to_client"` |
| `timestamp_ms` | integer | Milliseconds elapsed since session start |
| `type`         | string  | Event type matching either StreamMessageType constants (e.g. `TEXT.DELTA`, `SESSION.CREATED`) or provider wire event types (e.g. `session.update`, `response.output_text.delta`) |
| `payload_type` | string  | Encoding marker: `stream_message` for generic session events or `websocket_message` for raw provider WebSocket JSON |
| `payload`      | object  | Serialized `StreamMessage` for `stream_message`, or raw provider WebSocket JSON for `websocket_message` |

### Relationship to Event Types

For `stream_message` payloads, the `type` field uses the internal `StreamMessageType` constants from `go-agent-loop/pkg/messages` (e.g. `TEXT.DELTA`, `AUDIO.DELTA`, `SESSION.CREATED`). For `websocket_message` payloads, `type` uses the provider wire event name, such as OpenAI Realtime `session.update`, `conversation.item.create`, `response.create`, and `response.output_text.delta`.

Common normalized mappings:

- `SESSION.CREATED` -> OpenAI `session.created`
- `TEXT.DELTA` -> OpenAI `response.output_text.delta`
- `AUDIO.DELTA` -> OpenAI `response.output_audio.delta`
- `SESSION.UPDATE` -> OpenAI `session.update`

Replay preserves the ordered bidirectional flow. Server-to-client events are delivered until the next expected client-to-server event; once the client sends the matching event, later server events continue. If multiple server events arrive before the next expected client event, replay emits all of them before blocking on the outbound expectation.

### Example Capture

```json
{
  "version": 1,
  "provider": {
    "name": "grok",
    "model": "grok-realtime"
  },
  "session": {
    "id": "sess_sanitized",
    "started_at_utc": "2026-04-11T00:00:00Z",
    "fixture_provenance": "synthetic"
  },
  "records": [
    {
      "sequence": 1,
      "direction": "server_to_client",
      "timestamp_ms": 0,
      "type": "SESSION.CREATED",
      "payload_type": "stream_message",
      "payload": {
        "type": "SESSION.CREATED",
        "value": {
          "type": "session_created",
          "session_id": "sess_sanitized",
          "model": "grok-realtime"
        }
      }
    },
    {
      "sequence": 2,
      "direction": "client_to_server",
      "timestamp_ms": 15,
      "type": "SESSION.UPDATE",
      "payload_type": "stream_message",
      "payload": {
        "type": "SESSION.UPDATE",
        "value": {
          "type": "session_update",
          "instructions": "You are a helpful assistant.",
          "model": "grok-realtime"
        }
      }
    },
    {
      "sequence": 3,
      "direction": "server_to_client",
      "timestamp_ms": 120,
      "type": "TEXT.DELTA",
      "payload_type": "stream_message",
      "payload": {
        "type": "TEXT.DELTA",
        "value": {
          "type": "delta_text",
          "content": "Hello! How can I help you today?"
        }
      }
    },
    {
      "sequence": 4,
      "direction": "server_to_client",
      "timestamp_ms": 125,
      "type": "TEXT.END",
      "payload_type": "stream_message",
      "payload": {
        "type": "TEXT.END",
        "value": {
          "type": "text_end"
        }
      }
    }
  ]
}
```

## Usage

### Recording

```go
// HTTP recording
recorder := testing.NewRecordRoundTripper(http.DefaultTransport)
client := &http.Client{Transport: recorder}
// ... use client ...
recorder.FlushToFile("captures/my-test.http.json")

// Session recording
sessionRec := testing.NewSessionRecorder(
    realSession,
    testing.WithSessionRelayContext(ctx),
)
// ... use sessionRec as messages.Session ...
sessionRec.FlushToFile("captures/my-test.session.json")

// Grok WebSocket recording
dialer := testing.NewRecordingWebSocketDialer(
    grok.NewDefaultWebSocketDialer(),
    "grok",
    "grok-realtime",
)
provider := grok.New(grok.WithWebSocketDialer(dialer))
// ... use provider ...
dialer.FlushToFile("captures/my-test.session.json")
```

### Replaying

```go
// HTTP replay
replayRT, _ := testing.NewReplayRoundTripper("captures/my-test.http.json")
client := &http.Client{Transport: replayRT}

// Session replay
replayer, _ := testing.NewSessionReplayer(
    "captures/my-test.session.json",
    testing.WithReplayContext(ctx),
)
// replayer implements messages.Session — inject where a real session would go
// Send verifies outbound client events against the next recorded
// client_to_server record. Err reports replay divergence or an omitted
// expected outbound event.
// Replay and recorder relay writes stop when the owned context is cancelled.
// For read-only transcript rendering, disable outbound validation explicitly:
reader, _ := testing.NewSessionReplayer(
    "captures/my-test.session.json",
    testing.WithReplayContext(ctx),
    testing.WithReplayOutboundValidation(false),
)

// With timing delays (real-time playback):
replayer, _ := testing.NewSessionReplayer("captures/my-test.session.json", testing.WithReplayTiming())

// Grok WebSocket replay
replayDialer, _ := testing.NewReplayWebSocketDialer("captures/my-test.session.json")
provider := grok.New(grok.WithWebSocketDialer(replayDialer))
// provider uses the capture and does not open a live WebSocket
// replayDialer.Done closes when replay divergence or connection close is observed;
// replayDialer.Err reports the replay divergence or incomplete fixture reason.
```
