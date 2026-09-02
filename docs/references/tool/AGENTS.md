# Tool debugging workspace

This workspace is used to invoke one Yui tool directly.

- Use the smallest input that reproduces the behavior.
- Keep fixtures synthetic and free of credentials.
- Report stdout, stderr, exit status, and changed files separately.
- Do not broaden filesystem scope to make a failing test pass.
- For audio fixtures, record PCM encoding, sample rate, channels, frame size,
  expected sample count, and expected duration.
- Clean up only temporary artifacts created by the current test.

# Prompting reference

Tool descriptions and success criteria should follow OpenAI's guidance to make
availability and completion rules explicit:
https://developers.openai.com/api/docs/guides/realtime-models-prompting
