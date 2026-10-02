# Changelog

## Unreleased

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
