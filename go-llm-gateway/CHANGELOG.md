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
  `Options` in its raw `Config` into a strict `session.start`. There is no
  session provider yet, so nothing in production reaches the package.
- `pkg/providers/openailive/fakelive`: a scripted fake GPT-Live server for
  tests, served in process (`Server.Dialer`) or over `httptest`
  (`Server.ServeHTTP`). It is test support; only `_test.go` files may import
  it.

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
