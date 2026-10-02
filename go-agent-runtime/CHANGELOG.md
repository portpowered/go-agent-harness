# Changelog

## Unreleased

### Added

- `providers.OpenAILiveProvider` (`"openai-live"`) and
  `providers.OpenAILive1Model` (`"gpt-live-1"`): the built-in catalog lists
  `gpt-live-1` under `openai-live`.
- `providers.RealtimeModel.Duplex` and `providers.RealtimeModel.Delegation`,
  with the values `providers.RealtimeDelegationClient` and
  `providers.RealtimeDelegationResponses`. `gpt-live-1` is `Duplex` with
  client delegation; the OpenAI Realtime models are neither.

### Changed

- Model admission restricts `openai-live` to its catalog, as it already does
  for `openai`, so only `gpt-live-1` is admitted there. The rejection's
  `UnsupportedRealtimeModelError.Provider` is `"OpenAI Live"`. `gpt-live-1` is
  not admitted under `openai`. `BuildSession` still refuses `openai-live`
  ("realtime sessions do not support provider"), so no session can be built
  for it yet.

### Removed

Redundant re-exports and an unused error type were deleted. Each one has a
public replacement:

| Removed | Use instead |
| --- | --- |
| `roomevidence.RoomParticipantResult`, `RoomTerminationReason`, `ParticipantTerminationReason`, `ParticipantKindCustomer`, `RoomTerminationStopped`, `RoomTerminationMaxDurationReached`, `RoomTerminationFailed`, `ParticipantTerminationEnded`, `ParticipantTerminationDisconnected`, `ParticipantTerminationError`, `RoomReplayBundleSchemaVersion` | `rooms.*` (same names) |
| `roomevidence.ObservationKind` | `rooms.EvidenceObservationKind` |
| `rooms.RoomReplayBundleError`, `RoomReplayBundleErrorKind`, `RoomReplayBundleIncomplete`, `RoomReplayBundleMismatch` | `roomreplay.*` (same names) |
| `rooms/wire.NewParticipantMesh(ctx, factory)` | `rooms/wire.NewMesh(ctx, rooms.MeshConfig{PairFactory: factory})` |
| `sessiontrace.ScheduledAudioInput` | `audioio.ScheduledAudioInput` |
| `sessiontrace.LivenessError` (never constructed) | `session.LiveLivenessFailure` |
| `sessiontrace.ErrSilentProviderEmptyResponse`, `ErrSilentProviderTimeout` | `session.ErrLiveSilentProviderEmptyResponse`, `session.ErrLiveSilentProviderTimeout` |
