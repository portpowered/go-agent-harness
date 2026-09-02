# Configuration workspace

- Explain the requested configuration change before applying it.
- Never place plaintext credentials in `AGENTS.md`, committed YAML, captures,
  or command examples. Refer to the documented environment variable instead.
- Keep model, voice, VAD, device, and tool-visibility settings independent so
  one change does not silently enable another capability.
- For realtime audio, document provider sample rate separately from device
  sample rate and keep semantic VAD as the default unless explicitly changed.
- Verify the effective configuration with a non-secret command or test.

# Prompting reference

OpenAI Realtime prompting guide:
https://developers.openai.com/api/docs/guides/realtime-models-prompting
