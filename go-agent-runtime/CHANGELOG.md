# Changelog

## Unreleased

### Added

- `session.LiveEvent.DelegationTool` (`session.LiveDelegationTool`): the
  `delegation_tool_call` and `delegation_tool_result` events carry the tool
  name, the arguments and (on the result) the result content.
  `LiveDelegationTool.Audited` redacts the whole values and then bounds each
  to `session.LiveDelegationToolPayloadLimit` (4 KiB) with the
  `session.LiveDelegationToolTruncated` marker, so a credential across the
  cut leaves no fragment. The sessiontrace live recorder
  (`sessiontrace.LiveRecorderOptions.Credentials`) writes `tool_name`,
  `tool_arguments` and `tool_result` that way, and so does the
  `--record-dir` evidence recorder.
- `livedelegation.ToolLock` and `Binding.ToolLock`: the session's
  resource-group lock. A live session with delegations locks its tools by
  resource group for both the voice loop and its delegations: every call
  routed to the browser surface shares one lock, `write_file` and
  `edit_file` another, and each other long-running tool its own.
- `tools.BrowserToolRouter`: the composed tool surface reports the calls it
  routes to the browser (the WebMCP broker's tools and the page tools a
  dynamic broker resolves, such as `google_maps_*`), and the interactive
  session-turn executor forwards it. The live session groups those tools by
  this routing, not by name.

- `services/livedelegation`: the GPT-Live client-delegation executor
  (docs/architecture/gpt-live-provider.md 2.5, PR 4). A live session whose
  `session.LiveRequest.Delegation` names a backend hands every
  `DELEGATION.CREATED` to a bounded worker pool outside the voice loop's
  ToolRunner (default limit 2; work beyond it queues and is never dropped).
  Each delegation runs a nested turn-based agent loop on the backend
  provider with the session's own tools, under the session's capability
  surface and tool policy; tools the interactive policy treats as
  long-running are serialized per tool. The task is the provider's task
  text, or else its transcript window, plus the recent session history.
  The result is sent back as `CONTEXT.APPEND` commentary with the delegation
  id, and each tool call as a quiet `thinking` progress note. A backend
  error or a spent budget (turns, tokens, time on the injected clock;
  defaults 8 turns, 200000 tokens, 2 minutes) is answered with a short
  spoken failure, because GPT-Live never times a delegation out. The backend
  is told to keep results short; the provider splits an over-long append.
  Delegations run on the session lifetime: a user interrupt cancels none of
  them, the session end cancels and joins all of them, and
  `Executor.Cancel` is there for task revisions. Delegations never become
  loop tool calls and never request a response.
- Delegation failures are spoken only as a category; the detail goes to
  `livedelegation/wire.Dependencies.Logger`. A backend whose build failed is
  rebuilt at the next delegation with the credential resolved the first time.
  `livedelegation.Backend.Unconfigured` lets a host report a backend it cannot
  run (for example, no text model); each delegation is then answered with a
  spoken failure and the reason is logged.
- `session.LiveEventDelegationToolCall` and
  `session.LiveEventDelegationToolResult`: each session tool call a
  delegation makes is published on the live event stream and to the
  invocation recorder, tagged with the delegation id (`ItemID`).
- `session.LiveRequest.Delegation` (`*livedelegation.Policy`) and
  `wire.LiveDependencies.Delegations`: the backend and limits of one session
  and the executor service of the live session owner. Without both, a
  delegation stays unanswered as before.

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
- `providers.OpenAILiveCodexModel` (`"gpt-live-1-codex"`), listed under
  `openai-live` next to `gpt-live-1`, also `Duplex` with client delegation.
  It runs on the ChatGPT login: `providers.SessionConfig.ChatGPTAuthPath`
  names the auth store (`yui auth chatgpt`), `ClientVersion` is sent as the
  route's `version` header, and `CodexTransport` optionally replaces the
  route's network edges (a hermetic fake, or STUN/TURN servers).
  `session.LiveRequest` carries `ChatGPTAuthPath` and `ClientVersion` to it,
  and `wire.ProviderInferenceDependencies.CodexTransport` injects the
  transport.
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

- **Breaking** `services/tools`: `ImagePartPreparer` changes from
  `func([]string) ([]messages.ImagePart, error)` to
  `func([]tools.ImageSource) ([]messages.ImagePart, error)`, and the new
  `tools.ImageSource{Path, Bytes}` carries each image read_image has read.
  read_image now reads the image itself, under the filesystem policy and its
  open-time protection check, and hands the preparer the bytes. Migration: a
  preparer validates and converts `Bytes` and must never read `Path` again
  (`Path` only names the image in messages); re-reading it would reopen the
  check-then-open race the change closes.
- `services/tools` filesystem tools (read_file, list_dir, read_image,
  write_file, append_file, edit_file) enforce protected system and credential
  locations at open time as well as in the path pre-check, so a symlink
  swapped in between the check and the open can no longer redirect a read or
  a write into `~/.ssh` and the other credential stores under a broad
  `--allow-path`.

- Security: the `--record-dir` evidence recorder redacts the session
  credentials (raw and JSON-escaped) from every voice-loop `TOOLCALL.*` and
  tool-result payload before spooling it. Transcript payloads are base64, so
  the bundle's byte redaction never reached them, and a credential in a
  voice-loop tool call or result reached the bundle.
- Breaking (unreleased API): `livedelegation.Binding.Serialized`, a per-tool
  lock private to the delegations, is replaced by `Binding.ToolLock`, shared
  with the voice loop. Two delegations can no longer drive the browser at
  once through different browser tools, nor alongside the voice loop's own
  browser call.
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
- `BuildSession` routes `openai-live` `gpt-live-1-codex` to the WebRTC route
  (`go-llm-gateway/pkg/providers/openailive/codexlive`), signed by the
  ChatGPT auth store through `chatgptauth.Manager`, which refreshes the
  token before every call and sideband dial. It needs the store and no API
  key: without a sign-in it fails before any network operation, and it
  refuses `RecordPath` and `ReplayPath` (the route has no provider capture
  yet). `gpt-live-1` still needs an API key; its refusal now names
  `gpt-live-1-codex` as the ChatGPT alternative.
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
