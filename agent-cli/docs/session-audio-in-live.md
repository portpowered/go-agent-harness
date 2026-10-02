# Input transcription cost policy

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
