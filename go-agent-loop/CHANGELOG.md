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

- Two stream types for full-duplex providers that delegate backend work
  (docs/architecture/gpt-live-provider.md, PR 3):
  - `messages.StreamTypeDelegationCreated` (`DELEGATION.CREATED`, inbound,
    observational) with `messages.DelegationCreatedValue{ID, Target,
    OffsetMS, Task, Transcript}` and `messages.TranscriptFragment`
    (`NewDelegationCreatedValue`, `DelegationTargetClient`). It carries no
    `ResponseID`, is not a response stream type, and message reconstruction
    ignores it. `messages.MustDeliver` now returns true for it, so a full
    session outbox waits for capacity instead of dropping it.
  - `messages.StreamTypeContextAppend` (`CONTEXT.APPEND`, outbound via
    `Send`) with `messages.ContextAppendValue{Kind, DelegationID, Content}`
    (`NewContextAppendValue`, `NewDelegationContextAppendValue`) and the
    kinds `ContextAppendInstructions`, `ContextAppendThinking` and
    `ContextAppendCommentary`. A provider with no channel for it returns a
    terminal-failure outcome.

  The addition is source compatible, but a switch over
  `messages.StreamMessageType` that the `exhaustive` linter checks (or that
  rejects unknown types, like a capture decoder) must now list both types.
  Outside implementers of `messages.Session` that forward every stream
  message should decline `CONTEXT.APPEND` unless they implement it.

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

### Fixed

- A bare `SendInterrupt` (no text, no held user turns) no longer leads to a
  request that Claude 4.6 and later reject with a 400. The interrupt still
  saves the partial response to history and dispatches a request that ends
  on it; go-llm-gateway's text providers now append a "Continue." user turn
  on the wire (`providers.ContinuationPrompt`) instead of sending the partial
  as a prefill. The `SendInterrupt` docs now say the resumed inference is a
  new turn with the partial in context, not a continuation of its text.
- A user turn that arrives while a model response is still streaming no
  longer drops that response's tool calls. Previously the coordinator reset
  the response's delta window when it dispatched the user turn, so the
  response's MESSAGE.END rebuilt a message without the tool calls the client
  had already seen complete, and they never executed. The response is not
  cancelled, so it now completes from its own deltas and its tool calls run.
- A user turn that arrives while a response is open, or while a tool batch
  has not reported its results, now joins the conversation history (and the
  kernel's full-message stream) only after that exchange completes:
  `[user, assistant answer, user]`, or `[user, assistant tool_call, tool,
  user]`. History no longer splits a tool call from its result, which Chat
  Completions and Anthropic reject, and no inference request ends on an
  assistant message, which current Claude models reject as a prefill.
  `History.HeldUserMessages` holds such turns in the meantime.
  - Duplex sessions: the user turn is still forwarded to the provider
    immediately.
  - Turn-based loops: the model answers the held turn once the exchange
    completes. A tool continuation carries it. After a final answer, the loop
    answers it instead of ending. Previously the new pass retired the open
    exchange, and its remaining deltas, including tool results, were dropped
    as stale.
  - Explicit interrupts (`SendInterrupt`) place held turns at once, ahead of
    the interrupt's own text; barge-in cancellation behaves as before.
  - Every way the loop ends places held turns, so a turn the user sent is
    never lost: a final answer, session close or stop, a failed tool or
    interaction end (before LOOP.END), and a terminal model ERROR or
    cancellation (as the engine exits). On that exit the recorders are
    flushed outside the history lock with a bounded context, which
    `agentloop.WithSettleRecordTimeout` (`Engine.SetSettleRecordTimeout`)
    configures; the default is `engine.DefaultSettleRecordTimeout` (3s).
    The new `subsystems.Recorder.Flush(ctx, []messages.Message)` records
    that settled history copy.
  - Only assistant response content opens a response. A user's input
    transcription (TRANSCRIPT deltas with RoleUser from realtime providers)
    and session events such as SESSION.OPEN do not hold typed turns.
- An interrupt that cuts off tool calls before they return now adds a
  cancelled result for each one, so history never leaves a tool call
  without its result. The results are also recorded on the kernel's
  full-message stream.
