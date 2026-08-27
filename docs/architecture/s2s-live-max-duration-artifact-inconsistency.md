# Live session max-duration artifact contract

`--max-duration` is a planned lifecycle boundary, not a provider failure. A
live session that reaches the bound returns status 0 when no independent
provider, transport, runtime, recording, or finalization error occurred.
The command still renders the planned `max_duration` reason so an operator can
distinguish an expected bounded run from a naturally completed session.

## Two artifact authorities

The raw `--record` capture is a wire ledger. The recording WebSocket dialer
adds an entry only after an outbound message was sent successfully or an
inbound message was received successfully. The duration controller never
appends synthetic `response.done`, `session.closed`, or other provider frames
to that file. Therefore a capture that ends on an output delta truthfully
shows that the provider had not supplied a wire terminal before shutdown.

The `.session.jsonl` sidecar is the normalized lifecycle ledger. It includes
the stream events admitted before the duration boundary and exactly one
terminal record. If bounded shutdown does not observe a provider terminal, the
controller appends:

```json
{
  "type": "SESSION.CLOSE",
  "value": {
    "reason": "max_duration",
    "classification": "max_duration",
    "terminal_reason": "max_duration",
    "terminal_provenance": "loop",
    "output_state": "partial"
  }
}
```

`terminal_provenance=loop` is an explicit statement that the lifecycle record
was synthesized by the controller; it is not evidence that the provider sent
`response.done` or `session.closed`. When a provider terminal is actually
observed during the bounded drain, that event and its provider provenance are
retained instead of being rewritten as `max_duration`.

`output_state=partial` applies when output deltas were admitted for the active
response without its message-end boundary. A bound reached before any output
uses `none`; a response that already ended uses `complete`.

## Prompt-only recording bundles

`--record-dir` also supports a text-only user turn. Input audio is optional:
the finalizer accepts zero input segments, keeps the required client and agent
transcripts plus any observed `session-log.jsonl`, and emits only the output
audio segments that were actually observed. The `audio/` directory remains in
the bundle as the stable container, but no `audio/in-NNN.pcm` file or manifest
hash is fabricated for a prompt-only session. Any segment supplied by a caller
is still required to be non-empty, so an empty element remains a recording
validation failure. The staged directory is verified against this conditional
artifact set and renamed into place atomically.

## Shutdown and errors

At expiry, the duration admission boundary stops ordinary late provider
events, sends the normal session-close control into the loop, and enters the
bounded drain. Terminal and error events remain observable during that drain;
late media or text deltas do not enter the normalized sidecar or audio
artifacts. The raw provider capture remains independent of this normalization
step.

The successful status-0 rule applies only when the planned cutoff is the
authoritative outcome. Provider transport errors, runtime errors, recording
write failures, artifact flush/close failures, and finalization failures are
still returned as nonzero command errors. If one of those errors accompanies a
duration cutoff, the error remains visible rather than being hidden by the
planned terminal.
