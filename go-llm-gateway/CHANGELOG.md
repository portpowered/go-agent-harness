# Changelog

## Unreleased

### Added

- `pkg/providers/openailive`: the wire layer of the OpenAI GPT-Live protocol
  (`gpt-live-1`, `/v1/live/sessions`), the first phase of
  docs/architecture/gpt-live-provider.md. It has typed structs for every
  documented client and server event, `EncodeEvent`, `DecodeServerEvent` and
  `DecodeClientEvent` (an unknown event type decodes to `UnknownEvent`), the
  `NewInputAudioAppend` helper (refuses odd-length PCM16), and
  `BuildSessionStart`, which turns a `models.SessionConfig` plus the GPT-Live
  `Options` in its raw `Config` into a strict `session.start`.
  `ParseOptions` decodes and validates those `Options` (unknown keys are
  rejected). Invalid configs wrap `ErrInvalidSessionConfig`, and frames that
  are not well-formed events wrap `ErrMalformedEvent`. Every server event
  that may carry `client_event_id` keeps it, and `OptionalID` keeps an absent,
  null or string `response.event` `delegation_id` as it arrived.
- `pkg/providers/openailive/fakelive`: a scripted fake GPT-Live server for
  tests, served in process (`Server.Dialer`) or over `httptest`
  (`Server.ServeHTTP`). It is test support; only `_test.go` files may import
  it.
- `openailive.Provider` (`openailive.New`, name `"openai-live"`): the GPT-Live
  session provider over the primary WebSocket, phase 2 of
  docs/architecture/gpt-live-provider.md. `ConnectSession` sends
  `session.start` and waits for `session.started`; a startup `error` fails it
  with `*openailive.StartupError`. Auth headers come from a
  `CredentialProvider` (`WithCredentialProvider`), called once per dial;
  `APIKeyCredentials` (the bearer API key) is the only implementation. Audio
  flows both ways in the one session format (PCM16 at 24 or 16 kHz, or
  G.711), with a trailing odd PCM byte held for the next chunk. Assistant
  audio and transcripts become synthesized speech segments (`live_seg_<n>`,
  one `MESSAGE.START`..`MESSAGE.END` each) that close at a quiet gap of
  `WithSegmentGap` (default 600 ms) on the injected clock (`WithClock`) or on
  the server timeline; user transcripts become utterances (`live_utt_<n>`).
  `session.closed` reasons map onto the existing `TerminalReason` values,
  with the GPT-Live reason kept as `SessionCloseValue.Reason`; a socket that
  ends before `session.closed` reports `terminal_failure` with reason
  `finalization_unconfirmed`. `Close` (and the end of the connect context)
  runs the `session.close` handshake, bounded by `WithCloseTimeout` (default
  15 s). Inbound messages go through one ordered outbox with backpressure;
  once the handshake starts, delivery stops waiting for the reader (messages
  that do not fit are dropped and counted, terminal ones always land), so a
  close during streaming output reaches `session.closed` at once. The session reports `ProviderTurnDetection`, the new `FullDuplex`
  capability and no response requests; `RESPONSE.CREATE`, `TOOLCALL.END` and
  `TEXT.DELTA` fail with `openailive.ErrNoWireEvent`, `MESSAGE.END` and
  `SESSION.UPDATE` succeed with no wire event, and `RESPONSE.CANCEL` ends the
  open segment locally. Command errors are non-terminal `ERROR`s.
  `session.delegation.created` is logged and ignored for now.

- `pkg/providers/openailive/quicksilver`: the wire layer of the older GPT-Live
  dialect that a ChatGPT login can open (`gpt-live-1-codex`, "OpenAI-Alpha:
  quicksilver=v2", route C of docs/architecture/chatgpt-oauth.md). Typed
  client events (`input_audio.append`, `session.update`,
  `session.context.append`, `delegation.context.append`, `session.close`) and
  server events (`session.started`/`updated`, `output_audio.delta`,
  `input_transcript.added`, `output_transcript.added`, `turn.done`,
  `delegation.created`, `output_audio_buffer.cleared`, `error`),
  `EncodeEvent`/`DecodeServerEvent`/`DecodeClientEvent` (unknown types decode
  to `UnknownEvent`), `BuildSession` (model, instructions, voice with the
  `cove` default and the nine codex voices, client delegation with optional
  `ack_filler:false`, role-bearing `initial_items` from `Options`),
  `NewSessionUpdate` and `ContextAppends` (500-byte UTF-8-safe chunks).
  Goldens are literal frames from the Codex and OpenClaw sources.
- `pkg/providers/openailive/codexrtc`: the route's transport. `CallClient`
  creates the WebRTC call on the ChatGPT backend (JSON `{sdp, session}` to
  `/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas`),
  reads the SDP answer and the call id from `Location` (or
  `openai-session-id`), and dials the sideband
  (`wss://api.openai.com/v1/live/{call_id}`) with the same identity headers.
  The credential comes from a `CredentialSource` on every request
  (`CredentialFunc` adapts `chatgptauth.Manager.Credential`); `Credential`
  redacts its token in `String`, `GoString` and `LogValue`. Every sideband
  write has a deadline (`CallConfig.SidebandWriteTimeout`, default 10 s),
  received frames are capped at 16 MiB, and `Sideband.Close` first expires
  the write deadline so a `Send` stuck on a peer that stopped reading
  returns, sends `session.close` only if the writer frees within
  `SidebandCloseGrace`, and always closes the connection. `Peer` is a pion
  WebRTC peer with one send-and-receive Opus track that converts 48 kHz
  mono PCM16 frames through go-audio's pure-Go Opus codec. Nothing in
  production reaches the package yet.
- `pkg/providers/openailive/codexrtc/fakecodex`: test support. A fake backend
  (`httptest` call creation answered by an in-process pion peer, and the
  sideband WebSocket) and `VirtualNetwork`, an in-memory pion network so the
  loopback tests open no UDP socket. Only `_test.go` files may import it.
- `go.mod`: `github.com/google/uuid`, `github.com/pion/ice/v4`,
  `github.com/pion/logging` and `github.com/pion/transport/v4` are now
  direct requirements (all were
  already indirect at the same versions).

### Removed

The re-export aliases and forwarders below were deleted. Each one is
identical to the declaration in the package listed under "Use instead",
under the same name:

| Removed | Use instead |
| --- | --- |
| `gateway.SessionConfig`, `SessionEvent`, `SessionEventType`, `SessionModality`, `TurnDetectionConfig`, `InputAudioTranscriptionConfig` | `models.*` |
| `gateway.AudioFormatPCM16`, `AudioFormatG711Ulaw`, `AudioFormatG711Alaw`, `SampleRate8000`, `SampleRate16000`, `SampleRate24000` | `models.*` |
| `gateway.SessionModalityText`, `SessionModalityAudio` and every `gateway.SessionEvent*` event-type constant | `models.*` |
| `gateway.NewAudioBufferAppendEvent`, `NewAudioBufferCommitEvent`, `NewAudioBufferClearEvent`, `NewResponseCreateEvent`, `NewResponseCancelEvent`, `NewSessionUpdateEvent` | `models.*` |
| `gateway.CapabilityStateUnknown`/`Supported`/`Unsupported`, `RequestedModeStateless`/`StatelessStream`/`Session` and every `gateway.Feature*` constant | `capabilities.*` |
| `providers.CapabilityState*`, `providers.RequestedMode*`, and every `providers.Feature*` constant | `capabilities.*` |

The type aliases `gateway.ProviderCapabilities`, `gateway.Feature` and the
other capability types that are still referenced remain available.
