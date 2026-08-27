# Async tool-result arrival during unrelated speech (s2s)

## Claim and scope

`agent-cli/test/integration/s2s_async_tool_result_interrupts_speech_test.go`
is the T1 proving integration for an older tool result becoming ready while a
later, unrelated assistant response is streaming audio. It drives the real
`agent session` command over a channel-gated synthetic OpenAI Realtime replay;
the test does not call the session loop, coordinator, tool runner, or provider
session helpers as the behavior boundary.

The replay is hermetic and uses no network, credentials, microphone, speaker,
or wall-clock sleep race. The command shape is:

```text
agent session --replay <temporary-session-capture> \
  --record-dir <temporary-recording-dir> \
  --audio-in-turn <temporary-pcm16-raw> \
  --wait-for-close --audio-out <temporary-pcm16-wav> --max-duration 3s \
  "finish the pending weather lookup"
```

The scheduled audio file creates the first provider response; the positional
prompt creates a distinct later response after that first response completes.
The test supplies the result to a temporary `--audio-out` path. The capture and
PCM deltas are generated from the committed audio corpus inside the test, then
validated by the shared replay validator.

## Selected runtime disposition

This lane names the current baseline as **queue/sequence**. The unrelated
response is allowed to preserve its current-response audio through its normal
audio and response boundaries; the replay does not accept a cancel/redirect
event. The local tool result returns and causes the second response request,
while the replay server queues that continuation's audio until the current
response finishes. A provider-facing result, when the leased production
forwarding contract exists, must be emitted exactly once with the original
call ID before that continuation request.

The ordered collision is:

1. The first audio response emits exactly one `get_weather` call with ID
   `call_async_weather_1`; the controllable executor remains blocked.
2. After the first response boundary, a distinct later response is requested
   and emits two distinguishable PCM16 audio deltas. The first delta is
   observed by the CLI stream observer before the executor is released.
3. The executor returns the sentinel
   `{"temperature_c":24,"condition":"clear","sentinel":"async-result-001"}`
   with the original call ID while the second unrelated audio delta remains
   gated. The local `RoleTool` result is observed exactly once before the
   result-driven continuation.
4. The current response's remaining audio then completes byte-for-byte. Under
   the selected queue/sequence contract, a provider-facing result (when the
   leased production forwarding contract exists) is delivered after that
   boundary and before the result-driven continuation request. The replay then
   releases the continuation audio.
5. The exact fixture terminal event
   `[session closed: async_collision_complete]` is observed within the
   bounded duration.

The positive verifier checks the executor call/result cardinality and
correlation, local tool-result deltas, causal gate order, three response creates
with a continuation, exact concatenated PCM16 output, and the exact terminal
boundary. It also contains a strict provider-facing `function_call_output`
verifier. On the current origin/main baseline that verifier reports the
missing `ToolResultForwarder` plus OpenAI
`StreamTypeToolCallEnd`-to-`function_call_output` translation as an explicit
canary; absent provider delivery is never treated as success.

## Controls

The controls use the same generated capture, channel gates, production command,
and shared verifiers. Each changes one observable outcome:

- `TestSessionAsyncToolResultProviderResultLossFailsVerifier` suppresses only a
  provider-facing `function_call_output` at the replay transport boundary.
  The healthy audio, local result, continuation, and terminal assertions stay
  green; the strict verifier fails deterministically and names
  `call_async_weather_1`.
- `TestSessionAsyncToolResultAudioDamageFailsVerifier` mutates one sample in
  collision delta 1 while transport completion remains healthy. The PCM16
  verifier fails with the affected collision delta span.
- `TestSessionAsyncToolResultMissingTerminalFailsBounded` withholds only the
  fixture terminal event and uses a 250ms `--max-duration`. The command returns
  within the bound and the verifier reports the missing exact terminal event;
  it cannot wedge the test process.

The result-loss control is intentionally a forward-compatible canary: on the
current baseline the provider result is already absent, so it fails for the
same named production gap even when the drop switch is enabled. Once the
out-of-lease forwarding contract lands, the switch removes the result from
the provider-facing exchange while leaving all other assertions unchanged.

## Adjacent coverage

This is different from:

- **v4f**, which interleaves a tool call inside one provider response's own
  audio stream. It does not race an outstanding result from an earlier turn
  against an independent later response.
- **v3b**, which proves the disposition of a tool result after a user audio
  barge-in. It does not prove a correlated older result arriving during
  unrelated assistant speech or the output PCM ordering across that collision.

This document scopes the claim to the hermetic T1 CLI replay. It does not claim
live-provider behavior, device I/O, source-topology coverage, or direct
internal-helper coverage.

## Reproduction

```sh
go test ./agent-cli/test/integration \
  -run '^TestSessionAsyncToolResult' -count=1 -v
```
