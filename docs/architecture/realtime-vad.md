# Realtime turn detection

Live OpenAI microphone sessions use provider-side `semantic_vad` by default.
The provider decides when the user has completed a thought, including pauses and
fillers such as “umm,” and then creates or interrupts responses according to its
turn-detection policy. Audio-device playback timing is still tracked locally for
barge-in truncation; it is not used to decide whether speech has started.

Persist an explicit policy in the agent config when the provider default is not
appropriate:

```yaml
session:
  vad:
    enabled: true
    type: semantic_vad
    eagerness: low
    create_response: true
    interrupt_response: true
```

`semantic_vad` accepts `auto`, `low`, `medium`, or `high` eagerness. Lower
eagerness gives hesitant speakers more time but increases response latency. Do
not combine it with `threshold`, `prefix_padding_ms`, or `silence_duration_ms`;
those settings belong to `server_vad`.

OpenAI sessions may opt back into silence-based detection:

```yaml
session:
  vad:
    type: server_vad
    threshold: 0.5
    prefix_padding_ms: 300
    silence_duration_ms: 500
```

Grok continues to default to `server_vad`. Scheduled/replayed audio remains
client-owned and disables provider turn detection, because its commit boundary
is supplied by the harness rather than live speech.

The interactive `agent chat` command has a separate local RMS detector for its
record/stop UI. Changing this live-session configuration does not replace that
detector.
