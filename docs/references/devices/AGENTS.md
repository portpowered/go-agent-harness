# Audio-device diagnostics

- Identify input and output devices separately, including their stable IDs.
- Report native sample rate, channel count, frame quantum, and default status.
- Do not infer successful playback from queued bytes; use device callbacks or
  loopback capture as evidence of consumed audio.
- When provider and device rates differ, validate duration and sample counts
  across conversion in both directions.
- Keep microphone recordings and diagnostic captures free of secrets.

# Prompting reference

OpenAI Realtime prompting guide, including unclear-audio handling:
https://developers.openai.com/api/docs/guides/realtime-models-prompting
