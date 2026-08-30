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

## Input transcription cost policy

Live OpenAI sessions that accept customer audio request the
`gpt-live-transcribe` input-transcription model once in their initial session
configuration. Input transcription is billed separately from the
speech-to-speech model and separately for each provider session. A
two-participant room therefore has two independent transcription streams when
both participants accept audio input; the room does not share or duplicate a
transcription request between participants.

For a standalone session where customer-speech text is not needed, opt out at
session startup:

```bash
agent session "Keep the response brief." \
  --provider openai \
  --model gpt-realtime-2.1-mini \
  --api-key "$OPENAI_API_KEY" \
  --no-input-transcription \
  --record /tmp/openai-no-input-transcription.session.json
```

The opt-out omits input transcription while leaving the audio conversation and
output recording path unchanged. Replay always follows the captured initial
`session.update`: changing the flag cannot partially disable transcription in
an already-recorded capture.
