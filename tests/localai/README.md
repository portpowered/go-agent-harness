# Realtime tier conformance suite

This optional suite runs the same five behavior bodies against LocalAI and
OpenAI: audio round trip, retained three-turn context, VAD/barge-in, a
model-chosen function call, and image input. It uses raw OpenAI-compatible
Realtime WebSocket events so the assertions observe customer-visible output
instead of provider-specific gateway internals.

Run it from this directory because the repository is a Go workspace with no
root module:

```powershell
$env:GOWORK = "off"
go test -tags live ./... -count=1 -timeout 120s
```

Without `-tags live` only the offline assertion controls build and run.

The LocalAI case uses `LOCALAI_REALTIME_URL`, then
`AGENT_MODEL__LOCALAI__BASE_URL`, then the pinned fixture default. The OpenAI
case requires `AGENT_MODEL__OPENAI__API_KEY` and always requests
`gpt-realtime-2.1-mini`; `AGENT_MODEL__OPENAI__BASE_URL` may override the
WebSocket endpoint. Secret values are never printed and the suite never reads
the repository `credentials` file.

With `-tags live`, a missing LocalAI or OpenAI prerequisite is a named test
failure, as is a reachable endpoint that fails a behavior. The four assertion controls run
without live services and intentionally log their expected rejection: silent
PCM, withheld history, no tools, and no image.

The audio cases use the checked-in mono PCM16 speech fixture so transcription
and VAD are exercised; the shared body resamples it to each provider's input
rate. Manual turns commit audio and use the provider's response-creation rule;
server-VAD turns let VAD create and interrupt the response and append a trailing
silence segment to close the initial utterance.

The suite keeps its own small PCM16 helpers (`resamplePCM16`, `pcm16RMS` in
`protocol_test.go`) on purpose. It is a standalone module (`GOWORK=off`) that
talks raw Realtime WebSocket events and depends on nothing in this
repository, so its oracle stays independent of the go-audio code under test.
It is therefore outside the architecture gate's `hand-rolled-pcm16` and
`hand-rolled-wav-container` source-pattern rules, which govern the workspace
modules. Note that `pcm16RMS` normalizes by `math.MaxInt16` (32767) where
`go-audio/pkg/codec` normalizes by 32768; the difference is below the
`silenceRMSThreshold` margin these checks use.

The dated measurement boundary is maintained in
`docs/architecture/s2s-local-tier-conformance.md`.
