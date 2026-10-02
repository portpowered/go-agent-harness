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
