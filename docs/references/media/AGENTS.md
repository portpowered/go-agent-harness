# Media inspection

- Identify the container, codec or PCM encoding, sample rate, channels, sample
  count, and duration before processing audio.
- Preserve source media. Write derived output to a separate explicit path.
- Compare ingress and egress using sample counts, boundaries, and digests where
  applicable; listening alone is not proof of losslessness.
- Report clipping, discontinuities, dropped frames, and resampling assumptions.
- Use device-consumed time for interruption claims.

# Prompting reference

OpenAI Realtime prompting guide:
https://developers.openai.com/api/docs/guides/realtime-models-prompting
