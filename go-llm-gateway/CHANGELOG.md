# Changelog

## Unreleased

### Added

- `openailive` delegation surface (PR 3 of
  docs/architecture/gpt-live-provider.md). A client
  `session.delegation.created` becomes `DELEGATION.CREATED` with no
  `ResponseID`, so a delegation mid-segment never retires the speech
  segment. It is reported once a user transcript fragment ending at or after
  its `offset_ms` arrives, or after the settle window (`WithDelegationSettle`,
  default `DefaultDelegationSettle` = 400 ms on the session clock), or at the
  session end, whichever is first, and it carries the recent transcript
  (a bounded ring of speaker spans). Responses-mode delegations are still
  logged and dropped. `CONTEXT.APPEND` maps to `session.instructions.append`,
  `session.thinking.append` or `session.commentary.append` by kind, with
  `delegation_id` the value's id or `null` and numbered event ids
  (`evt_ctx_<n>`). Content over `MaxAppendTokens` (500) UTF-8 bytes, a
  guaranteed ceiling for a byte-level BPE token count, is split at sentence
  or word boundaries (never inside a rune, invalid bytes included) into
  several appends under the same delegation id. Appends on one session are
  serialized, so their chunks never interleave; a multi-chunk append is not
  atomic. An empty append or an unknown kind fails with `ErrNoWireEvent`.
- `pkg/testing`: session captures decode `DELEGATION.CREATED` and
  `CONTEXT.APPEND` values.

- `pkg/providers/openaichatgpt`: the `openai-chatgpt` text provider
  (`Infer`, `InferStream`). It speaks the Responses API over the ChatGPT
  Codex backend (`POST https://chatgpt.com/backend-api/codex/responses`) on a
  ChatGPT login, taking the access token and account id from a
  `CredentialSource` (`*chatgptauth.Manager`, which refreshes before expiry).
  Requests send `store:false`, `stream:true`, `instructions`, input items,
  function tools (`strict:false`), optional `reasoning` (from the request
  Config `reasoning_effort`), `include:["reasoning.encrypted_content"]` and
  `prompt_cache_key`, with the headers `Authorization`,
  `chatgpt-account-id`, `originator`, `OpenAI-Beta: responses=experimental`,
  `accept: text/event-stream`, `session-id` and `x-client-request-id`, as Codex
  and OpenClaw do.
  - The SSE stream maps to text, reasoning, tool-call, refusal, usage and
    error stream messages. A line may be up to 16 MiB, and a stream that
    sends nothing for `DefaultStreamIdleTimeout` (300 s, Codex's
    `stream_idle_timeout`; `WithStreamIdleTimeout`) fails with
    `ErrStreamIdle`.
  - Reasoning items that carry `encrypted_content` are kept per function
    call and sent back immediately before that call in the next request,
    without their item id (as OpenClaw's ChatGPT path does), so reasoning
    carries across tool steps with `store:false` and interleaved responses
    keep their order.
  - With no model configured, the provider lists
    `GET {base}/models?client_version=...` once and uses the account's
    default model by Codex's rule (`DefaultModel`; `Provider.Models`).
  - A 401 force-refreshes the credential (`ForceRefresher`) and retries the
    request once; a second 401 is `ErrSignInAgain`. A 403 is not a sign-in
    failure: a policy code (`misalignment_policy_violation`, `cyber_policy`,
    `bio_policy`, `invalid_prompt`) is `ErrPolicyViolation`
    (invalid request), anything else `ErrBlocked`. `usage_limit_reached`,
    `usage_not_included`, `server_is_overloaded`/`slow_down`
    (`ErrServerOverloaded`), rate-limit and context-length codes map to typed
    errors. A tool result without a tool call id is rejected
    (`ErrToolResultWithoutCallID`).
  - Error text never contains a token or response text: at most the HTTP
    status and a sanitized error code, also for token refresh failures.
- `pkg/providers/openai/chatgptauth`: `Manager.ForceRefresh(ctx, rejected)`
  replaces an access token the backend rejected. Under the store lock it
  reloads the file and returns a token another process already rotated, and
  otherwise refreshes regardless of the expiry.
- `pkg/providers/openaichatgpt/fakechatgpt`: a scripted fake ChatGPT Codex
  backend (`POST /responses` as SSE, `GET /models`) for `httptest`. It is test
  support; only `_test.go` files may import it.
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
- `pkg/providers/openailive/codexlive`: the `gpt-live-1-codex` session of
  the `openai-live` provider, on a ChatGPT login (PR 9 of
  docs/architecture/gpt-live-provider.md). `ConnectSession` creates the
  WebRTC offer, creates the call through `codexrtc.CallClient` with the
  client version (`WithClientVersion`, sent as the `version` header),
  applies the answer, attaches the sideband (five attempts, 200 ms apart and
  doubling) and waits for the media connection. The session runs on the same
  state machine as the `gpt-live-1` session: the same speech segments,
  ordered outbox, `FullDuplex` capability and close handshake. Input PCM at
  the session rate (24 kHz, or 16 kHz) is resampled to 48 kHz, cut into 20 ms
  Opus frames and paced at 20 ms on the injected clock; the end of a user
  turn sends the held partial frame. Peer audio is resampled back; silence
  never opens or extends a segment. Transcripts, `turn.done`,
  `output_audio_buffer.cleared` (the segment ends `cancelled` and local
  playback is interrupted) and errors map onto the harness stream. A client
  `delegation.created` becomes `DELEGATION.CREATED` with its input_text as
  `Task`, and `CONTEXT.APPEND` becomes `delegation.context.append` or
  `session.context.append` (commentary on the speakable channel, thinking on
  the commentary channel), with the public route's settle window and
  500-byte split. A lost sideband is redialed with the Codex backoff (200 ms
  doubling to 5 s, reset after 30 s up); held control events are flushed in
  order before new writes, and a call that ended (HTTP 404/410) ends the
  session with reason `call_ended`. `expires_at` from `session.started` or
  `session.updated` ends the session as `expired`. Credential failures (an
  error event with `invalid_token`, `authentication_error`, `token_expired`
  or `invalid_api_key`, top-level or nested, or status 401; HTTP 401; a
  token manager with no sign-in or one it can no longer refresh) end the
  session with a terminal `authentication` error wrapping
  `codexlive.ErrSignInAgain` ("sign in again with `yui auth chatgpt`"); a
  refresh that only failed on the network is retried. `Transport` overrides the network edges (URLs,
  HTTP client, sideband dialer, pion settings, ICE servers; none by default),
  and `ChatGPTCredentials` adapts `chatgptauth.Manager`.
- `codexrtc.Peer` conceals packet loss: an inbound RTP sequence gap is filled
  with Opus PLC frames (at most `MaxConcealedFrames`, 100 ms) before the next
  decoded frame, and a late or repeated packet is dropped
  (`ConcealedFrames`, `LatePackets`). A sequence jump too large to be loss
  resynchronizes as RFC 3550 appendix A.1 does. `Peer.Failed` reports a
  connection that failed after it was up.
- `quicksilver.ErrorEvent` decodes a top-level `code` and a `status` (top
  level or nested); `AuthFailure` treats status 401 and a top-level code as
  credential failures, and `ErrorCode` returns the code.
- `quicksilver.BoundInitialItems` bounds the startup history as OpenClaw
  does: the newest `MaxInitialItems` (16) messages, each cut to 800
  characters, 8000 bytes in total. `BuildSession` applies it.
- `fakecodex.Backend`: `Send` pushes server events on the connected
  sideband, `DropSideband` cuts it without a close frame, `HangUp` closes it
  normally, `RejectSidebands` answers later handshakes with a status, and
  `Sidebands`/`WaitSidebands` count connections.
- `go.mod`: `github.com/google/uuid`, `github.com/pion/ice/v4`,
  `github.com/pion/logging` and `github.com/pion/transport/v4` are now
  direct requirements (all were
  already indirect at the same versions).

### Changed

- `openailive`: the session state machine (speech segments, the ordered
  outbox, the outbound mapping, the close handshake, the delegation tracker
  and the CONTEXT.APPEND split) moved to
  `pkg/providers/openailive/internal/livesession`, behind a `Dialect`, so the
  `gpt-live-1` and `gpt-live-1-codex` sessions share it. The public API and
  behaviour of `openailive` are unchanged.
- `codexrtc.Credential` redacts the account id as well as the token in
  `String`, `GoString` and `LogValue`.
- `quicksilver.ParseOptions` no longer rejects more than 128 startup
  messages; `BuildSession` keeps the newest that fit the bounds instead.

### Fixed

- A text request no longer ends on an assistant message. Claude 4.6 and
  later (including the default `claude-opus-4-6` and `claude-sonnet-4-6`)
  reject a trailing assistant message as a prefill with a 400; a bare
  `SendInterrupt` in go-agent-loop produced one by resuming on the saved
  partial response. The Anthropic, Gemini, OpenAI Chat Completions and
  openai-chatgpt (Responses) request builders now keep a trailing assistant
  message as context and append a user turn with the new
  `providers.ContinuationPrompt` ("Continue."). Requests that end on a user
  turn or a tool result are unchanged, and the caller's messages are not
  modified. No provider takes a trailing assistant message as a prefill.

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
