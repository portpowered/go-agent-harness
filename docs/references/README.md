# AGENTS.md references

These directories contain copyable workspace-instruction examples for Yui's
main command families. Yui never installs or generates them: copy the closest
example to `<your-workdir>/AGENTS.md`, edit it for the task, and run the command
from that workdir.

- [`session`](session/AGENTS.md): realtime voice, audio, barge-in, and tools.
- [`webmcp`](webmcp/AGENTS.md): realtime voice plus structured browser tools.
- [`ask`](ask/AGENTS.md): concise one-shot work.
- [`chat`](chat/AGENTS.md): persistent text conversation.
- [`config`](config/AGENTS.md): configuration changes and credential hygiene.
- [`devices`](devices/AGENTS.md): audio-device discovery and selection.
- [`interaction`](interaction/AGENTS.md): provider-neutral fixture inspection.
- [`media`](media/AGENTS.md): external media inspection and replay probes.
- [`tool`](tool/AGENTS.md): direct tool-debugging workspaces.
- [`probe`](probe/AGENTS.md): artifact-backed acceptance and simulation runs.
- [`room`](room/AGENTS.md): multi-participant realtime audio.

The realtime examples follow OpenAI's official
[Realtime prompting guide](https://developers.openai.com/api/docs/guides/realtime-models-prompting):
make role and objective explicit, separate sections clearly, define when tools
are available, specify what to do with unclear audio, and prefer short spoken
responses for latency-sensitive interactions.

Typical runs from a directory containing one of these files are:

```bash
yui --workdir "$PWD" session
yui --workdir "$PWD" session --browser-tools webmcp --browser-open https://example.com
yui --workdir "$PWD" ask "inspect the audio fixture"
yui --workdir "$PWD" chat
yui --workdir "$PWD" tool read_file path=README.md
```
