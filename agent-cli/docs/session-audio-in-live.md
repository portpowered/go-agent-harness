# Live audio-in round-trip proof (OpenAI Realtime)

`TestLiveSessionAudioInElicitsSpokenResponse` proves that file-based
`--audio-in` elicits a real spoken response from the live OpenAI Realtime API.
It streams the committed WAV fixture, emits `input_audio_buffer.commit`
followed by `response.create` at end of turn, and asserts:

- `response.output_audio_transcript.done` is received, and
- non-zero `response.output_audio.delta` bytes arrive within 60 seconds, and
- `--audio-out` records a non-empty WAV.

## Running it

Requirements:

- An OpenAI API key with Realtime access (this test bills real usage).
- Go toolchain for this workspace.

```bash
export OPENAI_API_KEY=sk-...
export AGENT_HARNESS_LIVE_AUDIOIN=1   # explicit opt-in; billing happens
go test -tags live -v ./agent-cli/test/integration/ -run TestLiveSessionAudioInElicitsSpokenResponse
```

Both environment variables are required: without `OPENAI_API_KEY` or when
`AGENT_HARNESS_LIVE_AUDIOIN != 1` the test skips, so default CI and hermetic
targets never run it.

## Expected output

A passing run logs:

```
live audio-in proof: transcript done, <N> output audio bytes, <M> recorded bytes
```

Failures print the ordered list of server event types observed in the session
capture, which makes missing `speech_stopped` / response events visible. The
session is bounded by `--max-duration 60s`, so a silent provider cannot hang
the test.
