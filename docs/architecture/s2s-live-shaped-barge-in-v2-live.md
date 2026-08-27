# Live OpenAI Realtime confirmation — live-shaped barge-in v2

The build-tagged `TestLiveSessionS2SLiveShapedBargeInV2` test is a bounded,
explicitly opt-in confirmation of the highest-risk collision boundaries in the
barge-in contract. It uses the shipped `agent session` command surface, the
real OpenAI Realtime WebSocket path, paced non-empty fixture speech, a raw
session capture, a `--record-dir` diagnostic bundle, and `--audio-out`.

The default test suite does not compile or run this probe. A live result may
incur provider charges. Missing credentials, authentication/setup failures,
rate limits, provider unavailability, timeouts, zero turns, and missing timing
boundaries are reported as **INCONCLUSIVE**, never as a passing contract
result. A repeatable observed contract violation fails the probe with the
first violated boundary.

## Exact redacted-key protocol

Run from the repository root. The wrapper reads the key without printing it,
passes it to the in-process shipped CLI as its `--api-key` value, and unsets
the shell variable on both normal and interrupted paths:

```bash
KEY=$(tr -d '\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
cleanup_key() { unset KEY; }
trap cleanup_key EXIT HUP INT TERM
probe_rc=0
OPENAI_API_KEY="$KEY" \
AGENT_HARNESS_LIVE_S2S_BARGE_IN=1 \
go test -tags live ./agent-cli/test/integration \
  -run '^TestLiveSessionS2SLiveShapedBargeInV2$' -count=1 -timeout 2m -v || probe_rc=$?
unset KEY
trap - EXIT HUP INT TERM
exit "$probe_rc"
```

The exercised command's effective provider arguments are:

```text
agent session --record <temporary-capture> --record-dir <temporary-recording> \
  --provider openai --model gpt-realtime --api-key "$KEY" \
  --audio-in - --audio-out <temporary-assistant.wav> --max-duration 90s
```

The test keeps all paths in a private temporary directory and discards CLI
stdout/stderr. Do not replace the placeholders with a committed path, paste a
key, print the key, or publish a raw capture, transcript, authorization value,
provider session identifier, or customer audio.

## Live boundaries and safe evidence

The reader waits for `session.updated`, then paces four non-empty utterances at
the harness's 30 ms PCM frame cadence. The next utterance is released only
after these normalized provider boundaries:

1. assistant audio for response `R1` is observed, so input `T2` tests active
   assistant audio;
2. response creation for `R2` is observed before its first audio, so input
   `T3` tests the response-created/pre-first-audio boundary; and
3. the terminal boundary for `R3` is observed, so input `T4` tests completion
   ordering and same-session continuation.

The raw capture validator correlates provider response IDs and user-item IDs,
but redacts them to ordered labels (`R1`–`R4` and `T1`–`T4`) in diagnostics.
It checks one non-empty input append group, commit, and `response.create` per
turn; one terminal response status per response; exactly one cancellation for
each response superseded by the first two gated interruptions; zero stale
post-cancel or post-terminal audio/text deltas; and a distinct completed
continuation response. The runtime observer independently checks four input
commits, four completed turns, non-empty output, one clean terminal observation,
and final accounting.

The expected diagnostic shape is a safe ledger like:

```text
T1...T4: one committed non-empty input each
R1: audio observed, cancelled, zero post-cancel deltas
R2: created before first audio, cancelled, zero post-cancel deltas
R3: completion boundary wins without cancel
R4: distinct completed continuation with output
terminal: clean, accounted, no unresolved work
```

The actual run logs only ordinals, event types, counts, byte counts, safe
terminal statuses, and file sizes. It never logs provider IDs, transcripts,
raw event payloads, keys, authorization data, or audio bytes.

## Coverage boundary

The deterministic shipped-CLI matrix remains the reproducible lower-bound
proof and owns the named outstanding-tool collision. A live model-selected
tool call is not claimed here: its selection and timing cannot be bounded
reliably by this probe without turning the test into an unrepeatable billable
workflow. The live confirmation therefore claims only the active-audio,
pre-first-audio, completion, and repeated same-session shapes, and does not
claim WebRTC/device parity, echo cancellation, latency SLOs, or unlimited
interruption endurance.
