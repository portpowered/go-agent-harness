# Realtime session timing analysis

`session-timing-report` turns an integrity-protected OpenAI Realtime capture
into a provider/tool/playback timing ledger. It is intended for diagnosing a
recording before converting its topology into a deterministic integration
fixture.

## Analyze a recording

From the repository root:

```bash
go run ./agent-cli/cmd/session-timing-report \
  -capture /absolute/path/to/session.json > /tmp/session-timing.json

jq '.summary' /tmp/session-timing.json
```

For a local family such as `test7*.json`, run the command once per file. Do not
copy customer recordings into the repository. The report validates capture
integrity and emits no audio payloads, transcripts, tool arguments, or tool
results.

The important buckets are:

| Metric | Ownership |
| --- | --- |
| `input_to_first_output_ms` | End-to-end provider response latency after a committed input turn. |
| `tool_execution_ms` | Local executor time between complete call arguments and the outbound correlated result. |
| `tool_result_to_request_ms` | Harness scheduling time between the tool result and `response.create`. |
| `tool_request_to_response_created_ms` | Provider/network admission time for the continuation. |
| `tool_response_created_to_first_output_ms` | Model decision/generation time after continuation admission. |
| `tool_result_to_first_output_ms` | End-to-end continuation latency; the sum of the preceding three continuation buckets. |
| `tool_result_to_first_audio_ms` | End-to-end spoken-continuation latency when the immediate next response contains audio. |
| `audio_burst_ratio` | PCM duration divided by its wire delivery span. Values above one mean audio arrived faster than realtime and must be paced locally. |
| `estimated_queue_delay_ms` | Time newly arrived model audio waits behind earlier model PCM. |
| `estimated_audible_gap_ms` | Silence between model-audio responses in one committed user turn, excluding separate user turns. |

Playback estimates use the provider's PCM byte count and output sample rate.
They deliberately reset at every committed input turn. They do not include a
local hold tone and are not a substitute for device-edge PCM verification when
investigating loss, duplication, or callback stalls.

## Reproduce with the real model

The billed E2E test crosses the compiled binary, WAV ingress, the live
`gpt-realtime-2.1` WebSocket, a real tool execution and continuation, protected
capture, and WAV egress. It applies broad live ceilings to each timing bucket
and prints a sanitized summary:

```bash
export AGENT_MODEL__OPENAI__API_KEY='<private test key>'
export OPENAI_REALTIME_21_LIVE=1

go test -tags=e2e ./agent-cli/test/e2e \
  -run '^TestGPTRealtime21BinaryAudioAndToolRoundTrip$' \
  -count=1 -v
```

Live model timing is supplemental and must not run in ordinary CI. When it
finds a regression, reproduce the same response/tool/audio topology with the
local WebSocket provider, injected tool executor, and external
`audio-device-server` in `agent-cli/test/integration`. Hermetic assertions
should cover exact PCM and harness-owned timing; the live test should retain
generous bounds for provider variability.

## Interpret serial tool latency

One tool result can lead to another model-selected tool instead of speech.
Each link incurs another provider admission and model-decision interval. The
report preserves every link rather than attributing the entire chain to the
local executor. A long chain with fast `tool_execution_ms` and
`tool_result_to_request_ms`, but slow continuation-generation metrics, is
provider/model planning latency. Reduce unnecessary tool-discovery steps or
advertise a higher-level operation before changing playback pacing.
