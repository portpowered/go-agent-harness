# Changelog

## Unreleased

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
