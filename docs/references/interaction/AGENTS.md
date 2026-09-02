# Interaction fixture review

- Treat the normalized fixture as immutable input.
- Check sequence ordering, actor identity, response boundaries, tool calls, and
  tool results before drawing conclusions.
- Preserve same-actor text chunks as one logical line; render each tool call
  and tool result on its own line.
- For audio-related events, distinguish metadata from raw PCM evidence and do
  not claim losslessness without sample-count or digest evidence.
- Report malformed or incomplete fixtures as errors instead of skipping them.

# Prompting reference

OpenAI Realtime prompting guide:
https://developers.openai.com/api/docs/guides/realtime-models-prompting
