# s2s depth-5 milestone — tool-call conversation reflects the real tool result

Proof status: **implemented and locally verified** (2026-08-27). PR #192 is
OPEN; review owns terminal CI, conflict reconciliation, and merge.

The depth-5 milestone of the s2s program — a customer asks by voice, the agent
calls a specific CLI-registered tool, and the agent's spoken reply states what
the tool ACTUALLY returned — is proven by a hermetic, deterministic,
CI-gating test with no network and no credentials.

## What is proven

`TestSessionToolCallConversationSpokenReplyReflectsRealToolResult`
(`agent-cli/test/integration/session_tool_result_conversation_test.go`) drives
the real `agent session --replay <capture> --audio-in <wav> --audio-out
<path>` command surface over the record/replay WebSocket transport and closes
the full causal chain:

1. **One named tool call.** The injected `messages.ToolExecutor` (composed
   through the production wire graph, `wire.InitializeMockAgentCLI`) observes
   exactly one invocation of `get_weather` with arguments `{"city":"Lisbon"}`.
2. **The real result reaches the provider.** The replayed exchange contains
   exactly one client-to-server `conversation.item.create` whose item is
   `function_call_output`, and its `output` equals the executor's runtime
   return value verbatim. Replay outbound validation matches every frame
   payload-for-payload, so a clean completion proves the live session sent it.
3. **The spoken reply quotes the result.** The fixture's server-side transcript
   deltas are authored from the same runtime return value ("24 degrees",
   "clear skies") and are positioned strictly AFTER the function_call_output
   record: the replayer withholds them until the executed result is delivered.
   The rendered transcript on the session output must quote those values —
   they appear nowhere else in the exchange.
4. **Audible speech.** The recorded `--audio-out` WAV passes RMS > 500 energy
   and duration bounds derived from the scripted reply window.

## Non-vacuity control

`TestSessionToolCallConversationDifferentResultFailsReflection` runs the
identical fixture but the executor returns different content at runtime. The
outbound `function_call_output` diverges from the fixture at that exact frame;
replay terminates deterministically (never via timeout) before the authored
reply is served, and the shared transcript-reflection assertion fails naming
the missing expectation values.

## Failure controls

Every control starts from the successful capture and changes one behavioral
obligation. The test names and expected diagnostics make the failure category
observable:

- **Missing call:** `TestSessionToolSingleCallSuppressedFailsDeterministically`
  observes zero executor calls and rejects the shared exactly-one assertion by
  name; it still completes the CLI flow and does not use a timeout as proof.
- **Wrong call:**
  `TestSessionToolCallConversationWrongToolNameIsRejected` and
  `TestSessionToolCallConversationWrongArgumentsAreRejected` keep one real
  executor invocation and paired result, then report observed-versus-expected
  tool identity or decoded arguments.
- **Duplicate call:**
  `TestSessionToolCallConversationDuplicateCallIsRejected` observes two calls;
  the strict result gate rejects the second obligation and no follow-up speech
  is released.
- **Result pairing:**
  `TestSessionToolCallConversationMissingResultIsRejectedAtGate`,
  `TestSessionToolCallConversationDuplicateResultIsRejectedWithBoundedLiveness`,
  `TestSessionToolCallConversationMismatchedResultCallIDIsRejectedAtGate`, and
  `TestSessionToolCallConversationEmptyResultCallIDIsRejectedAtGate` reject,
  respectively, an absent, duplicated, mismatched, or empty-ID
  `function_call_output`. Invalid pairings fail at the strict outbound replay
  gate; an absent result instead reports the unresolved originating call ID at
  the close boundary.
- **Result mutation and grounding:**
  `TestSessionToolCallConversationDifferentResultFailsReflection` changes the
  executor output and fails at the exact result gate. Then
  `TestSessionToolCallConversationContradictoryGroundingIsRejected` keeps the
  correctly paired result and valid audio but changes only the fluent answer;
  the result-unique transcript assertion rejects the contradictory facts.
- **Unsafe termination and liveness:**
  `TestSessionToolResultConversationCloseBoundaryRequiresAcceptedResult` holds
  the real executor unresolved while a provider close arrives and requires
  `ErrSessionUnresolvedToolResults` naming the call ID. Its companion subtest
  releases and accepts that same result and completes exactly once, proving the
  close guard is causal rather than an unconditional rejection.
  `TestSessionToolResultConversationMissingContinuationIsBounded` accepts the
  result but withholds the next assistant response and requires the typed
  `ErrSessionAudioResponseIncomplete` within its explicit bound.
- **Audio:**
  `TestSessionToolResultConversationAudioAbsenceAndSignalControls` separately
  rejects missing response audio and silent response audio, while
  `TestSessionToolResultConversationCorruptAudioDeltaIsRejected` rejects an
  odd-length PCM16 delta. These validators distinguish audio absence, signal,
  and corruption from tool/result transport failures.

The controls use replay gates, channels, barriers, and bounded contexts; they
do not use wall-clock sleeps to establish ordering and their test cleanup
checks keep the session and executor goroutines finite.

## Evidence boundary and limitations

The committed input corpus and the generated control captures are tagged with
synthetic provenance and validated by `gwtesting.NewReplayWebSocketDialer`
before the CLI session runs. The replay dialer performs semantic payload
matching and withholds inbound records after each outbound expectation until
that exact frame is sent. The tests therefore require no network, credentials,
live provider, or live audio device.

Hermetic replay is necessary evidence for this scripted causal sequence, but it
is insufficient to claim that every live timing race is closed. Unavailable
credentials and optional live probes are not merge requirements; those belong
to separate live-environment validation.

## Production seams this proof rests on

- `agentloop.WithToolExecutor` wiring in the composed live session path
  (`agent-cli/internal/services/session_live.go`), so provider tool calls
  execute through the composed CLI executor instead of the loop default.
- Tool-result delivery onto the provider wire (`subsystems.ToolResultForwarder`
  plus the OpenAI Realtime outbound `function_call_output` translation), which
  gives the replay gate something real to validate against.
- Session-lifecycle continuation: an awaiting audio session no longer ends at
  an assistant MESSAGE.END whose turn left unresolved tool calls; the runner
  holds until the follow-up response carries the spoken reply
  (`runAgentLoopSessionStream`). The ToolRunner's role=tool MESSAGE.END is
  treated as an internal boundary, never a terminator.
- Transcript rendering: assistant speech transcripts are printed on the
  session output alongside text deltas, so voice-session logs show what was
  actually said (`writeSessionReplayMessage`).

## Historical context

The v4a lane (#163) proved call -> executor -> result ordering over this
transport but its scripted transcript was static text, so nothing bound the
spoken reply to the executor's actual return value. This lane closes that gap
end to end through the public CLI surface.

## How to run

```sh
cd agent-cli
go test ./test/integration/ -run 'TestSessionTool(CallConversation|ResultConversation|SingleCall)' -v -count=1
go test -race ./test/integration/ -run 'TestSessionToolResultConversation' -v -count=1
```

The broader module gates are `go test ./...`, `go vet ./...`, and
`go build ./...` from each Go module. The final implementation head is handed
to the existing PR #192; review drives required checks to terminal success and
merges it.
