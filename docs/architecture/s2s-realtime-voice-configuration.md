# OpenAI Realtime voice selection

This is a standalone base-agent-session contract. It is not part of the room
or self-play delivery; future room participants can reuse the same per-session
value when that feature is implemented.

As verified against the [OpenAI Realtime API reference](https://platform.openai.com/docs/api-reference/realtime)
on 2026-08-26, the built-in voice enum is:

`alloy`, `ash`, `ballad`, `cedar`, `coral`, `echo`, `marin`, `sage`,
`shimmer`, and `verse`.

`SessionRunOptions.Voice` and `agent session --voice <name>` are optional,
case-sensitive per-invocation state. An empty value leaves the provider choice
unchanged. A selected value is emitted in the current Realtime session update
at `session.audio.output.voice`; it is not translated to the legacy top-level
`session.voice` field. CLI and service validation share the one registry owned
by `agent-cli/internal/services`.
