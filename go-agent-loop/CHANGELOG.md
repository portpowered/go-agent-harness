# Changelog

## Unreleased

### Removed

Redundant alias forwarders were deleted. Each one has a direct replacement
that behaves identically:

| Removed | Use instead |
| --- | --- |
| `probe.Aggregate` | `probe.AggregateScenarioResults` |
| `probe.ValidateCatalog` | `probe.ValidateGoalCatalog` |
| `transcript.(*AgentCapture).ProviderIngress` | `(*AgentCapture).Inbound` |
| `transcript.(*AgentCapture).ProviderEgress` | `(*AgentCapture).Outbound` |
| `transcript.NewClient` | `transcript.NewClientCapture` |
| `transcript.NewTranscriptWriter` | `transcript.NewWriter` |
| `transcript.NewTransparentTee` | `transcript.NewTee` |
| `transcript.WithMaxSegmentBytes` | `transcript.WithSegmentSize` |
| `transcript.WithReporter` | `transcript.WithDegradationReporter` |

The participants session admission hook was removed. The model runner no
longer checks whether a `messages.Session` implements
`SessionAdmissionClosed() bool` or
`SessionAdmissionAllows(messages.StreamMessage) bool`. No session in this
repository implements them any more. A session that has to refuse input after
shutdown should reject it from `SendWithOutcome` and return
`messages.SessionSendClosed`.

`probe.scenario.v2` is now the only probe scenario document format. The
unversioned JSON scenario loader was removed:

| Removed | Use instead |
| --- | --- |
| `probe.Load`, `probe.LoadScenario`, `probe.Decode` | `probe.LoadScenarioV2` or `probe.LoadScenarioV2File`, then `ScenarioV2.ProviderScenario` for the runner's `probe.Scenario` plan |

To migrate a document, add `"schema_version": "probe.scenario.v2"`, rename
`wait` (`duration`) to `sleep_fake` (`duration_ms`), spell expectation types
in snake_case (`terminal-reason` becomes `terminal_reason`), write
`frame_count` as `{"type": "frame_count", "equals": N}`, write `tool_called`
with `name`, and list expectations under `expectations` (the
`expected_behavior` and `expected` spellings and the `payload` and alias
fields are gone). The `send_tool_result` and `advance_to` steps have no
document form; build a `probe.Scenario` in Go for them.

The logical-tick sequencer moved into the probe package's tests. It was used
only there, and it linked `go-agent-loop/test/functional/timeharness` into
every program that imports `pkg/probe`. These names have no replacement:
`probe.Sequencer`, `probe.NewSequencer`, `probe.SequencePlan`,
`probe.SequenceStep`, `probe.SequencedEvent`, `probe.SequenceResult`,
`probe.Participant` (and `ScenarioDriver`, `Client`, `Agent`),
`probe.ParticipantHandler`, `probe.ParticipantHandlers`, `probe.TickContext`,
`probe.CausalExpectation`, `probe.TickExpectation`,
`probe.ExpectationOutcome`, `probe.OrderingViolationError`,
`probe.WallDurationExpectationError`, `probe.ErrOrderingViolation` and
`probe.ErrWallDurationExpectation`.

### Added

- `messages.SessionFullDuplex` (`FullDuplex() bool`), forwarded by
  `messages.SessionCapabilities`. A session that reports it (OpenAI GPT-Live)
  owns interruption itself, so the session model runner never runs local
  barge-in against it: loud or overlapping user audio is forwarded as audio,
  with no `RESPONSE.CANCEL` and no local playback interrupt. Sessions without
  it behave as before.
- `probe.scenario.v2` provider-runner expectations: `frame_count`,
  `terminal_reason`, `terminal_provenance`, `output_state`,
  `buffer_disposition`, `audio_energy`, `tool_called`,
  `tool_result_delivered`, `tool_result_discarded` and
  `no_orphaned_tool_result` (new `ScenarioV2Expectation.ToolCallID` field).
  They are valid only in a provider-only document.
- `ScenarioV2.ProviderOnly`, `ScenarioV2.ProviderScenario`,
  `ScenarioV2Step.ProviderStep` and `ScenarioV2Expectation.ProviderExpectation`.
- `messages.(*TypedBuffer).Shed` records a value as dropped by a full buffer
  without writing it, for an owner that queues ahead of the buffer and sheds
  overload there.
- `participants.ErrSessionDeltaOverflow`: a session model runner whose delta
  consumer stops reading ends with this error once its outbox queue (4096
  deltas) is full, and publishes it as a terminal ERROR classified
  `session_delta_overflow`.

### Changed

- **Breaking:** `messages.BargeInCapableSession` now also requires
  `SessionFullDuplex` (`FullDuplex() bool`). Types that embed
  `messages.SessionCapabilities` get it automatically; an outside type that
  implements the interface by hand must add the method (return false to keep
  local barge-in).
- The session model runner no longer blocks its event loop on a full
  `DeltaOutbox`, so barge-in is decided even while the delta consumer is
  stalled. A must-deliver delta that meets a full outbox is queued in order
  and written by a separate goroutine. While that queue is non-empty, audio
  and other ordinary deltas beyond the outbox capacity are shed and counted
  in `DeltaOutbox` drops; previously the event loop waited and they were
  delivered late. Terminal session failures queue behind every delta the
  session already produced instead of evicting the oldest queued delta; they
  still evict once the session context has ended.
