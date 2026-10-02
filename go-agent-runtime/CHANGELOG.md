# Changelog

## Unreleased

### Added

- Recording evidence and replay captures decode the new `DELEGATION.CREATED`
  and `CONTEXT.APPEND` stream messages instead of rejecting them as unknown.
  The live session, liveness, session-duration, browser-conversation and
  device-probe observers treat both as non-response messages; routing
  `DELEGATION.CREATED` to a delegation executor is PR 4 of
  docs/architecture/gpt-live-provider.md.

- `providers.OpenAIChatGPTProvider` (`"openai-chatgpt"`) and
  `providers.Config.ChatGPTAuthPath`: the provider service builds the
  `openai-chatgpt` text provider over the ChatGPT auth store the host names.
  `Build` fails before any network call, with "run `yui auth chatgpt`", when
  the store is missing, and needs no API key. Token refresh uses its own HTTP
  client, so a recording never captures it. The session runtime carries the
  path from the host resolution to the provider build. With `ReplayPath` it
  reads no auth store and never refreshes, so replay works with no login;
  recording and replay use a fixed conversation id so captured bodies match.
- `providers.OpenAILiveProvider` (`"openai-live"`) and
  `providers.OpenAILive1Model` (`"gpt-live-1"`): the built-in catalog lists
  `gpt-live-1` under `openai-live`.
- `providers.RealtimeModel.Duplex` and `providers.RealtimeModel.Delegation`,
  with the values `providers.RealtimeDelegationClient` and
  `providers.RealtimeDelegationResponses`. `gpt-live-1` is `Duplex` with
  client delegation; the OpenAI Realtime models are neither.
- `tools.Request.HomeDir` and `session.Resolution.HomeDir`: the host's user
  home directory. The filesystem tools refuse reads and writes of its
  credential stores (`.ssh`, `.aws`, `.gnupg`, `.kube`, `Library/Keychains`
  and the rest), including through a scope root inside one of them.
- Filesystem refusal reason `sensitive_write` (`filesystem.refusal.v1`): a
  write, append, edit or create into a protected location.

### Changed

- HTTP recordings drop the `chatgpt-account-id` request header, as they
  already drop `Authorization` and cookies.
- Security: a broad scope root such as `--allow-path /` or a parent of the
  home directory no longer exposes `~/.ssh` and the other home credential
  stores to `read_file`, `list_dir`, `read_image` or a symlink, provided the
  host sets `HomeDir`. Before, credential stores were refused only directly
  below a scope root. The Unix protected set also covers the macOS system and
  root keychains (`/Library/Keychains`, `/var/root/Library/Keychains`).
- Security: `write_file`, `append_file` and `edit_file` now refuse protected
  system and credential locations, so a broad root can no longer be used to
  plant `~/.ssh/authorized_keys`. Before, only reads were refused.
- Security: protected locations are matched case-insensitively per path
  component on macOS and Windows, and by file identity on any filesystem, so
  `~/.SSH/id_rsa` or `~/.Aws/credentials` no longer bypass the refusal.
- `tools.FilesystemScopeStartupNotice` now says protected locations can be
  neither read nor written.

- Model admission restricts `openai-live` to its catalog, as it already does
  for `openai`, so only `gpt-live-1` is admitted there. The rejection's
  `UnsupportedRealtimeModelError.Provider` is `"OpenAI Live"`. `gpt-live-1` is
  not admitted under `openai`. The catalog lookups (`RealtimeModels`,
  `LookupRealtimeModel`, `SupportedRealtimeModelIDs` and
  `ModelAdmissionResolver.ResolveRealtimeModel`) now answer for
  `openai-live` too.
- `BuildSession` builds `openai-live` sessions with the GPT-Live provider
  (`go-llm-gateway/pkg/providers/openailive`). The OpenAI API key is the only
  credential: without one, a hosted endpoint is refused before dialing, and a
  ChatGPT sign-in is not accepted. The endpoint is the realtime URL, else the
  base URL, else `wss://api.openai.com/v1/live/sessions`; `/live/sessions` is
  appended when missing and HTTP(S) becomes WS(S). The provider's timers use
  the service clock.
- `audioio`: `openai-live` resolves the 24 kHz realtime rate when no rate is
  requested (new `audioio.ProviderOpenAILive`), and gets no transcription
  config, since GPT-Live transcripts are always on.

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
