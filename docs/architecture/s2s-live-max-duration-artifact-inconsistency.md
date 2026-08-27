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

## Bundle terminal summary

The `--record-dir` bundle's `manifest.json` is the bundle-level discovery point
for the normalized terminal outcome. When a duration controller authors the
terminal, the manifest contains one optional `terminal` object with the same
five fields as the normalized sidecar:

```json
"terminal": {
  "reason": "max_duration",
  "classification": "max_duration",
  "terminal_reason": "max_duration",
  "terminal_provenance": "loop",
  "output_state": "partial"
}
```

The summary is typed recording state, not another transcript frame. In
particular, `terminal_provenance=loop` identifies the duration controller as
the author; it does not claim that the provider sent `response.done` or
`session.closed`. The raw `--record` capture remains a provider-wire ledger,
and no provider terminal is fabricated to make the manifest self-describing.
Existing bundles and recording inputs without a normalized terminal remain
valid because the manifest field is optional. This contract is proven here
for the planned max-duration cutoff; it does not redefine natural completion,
provider failure, or other terminal causes.

## Exact live reproduction

Following the validation standard, run the real OpenAI Realtime command with
temporary destinations and an API key supplied only by the environment:

```sh
work_dir="$(mktemp -d)"
agent session \
  --config-dir "$work_dir" \
  --provider openai \
  --model gpt-realtime \
  --api-key "$OPENAI_API_KEY" \
  --record "$work_dir/max-duration.session.json" \
  --record-dir "$work_dir/max-duration-recording" \
  --max-duration 5s \
  --system-prompt "Speak continuously for at least 60 seconds without stopping or ending the response." \
  "Start speaking now and continue continuously."
```

Validation succeeds only when the command exits with status 0 after partial
output, the sidecar has exactly one normalized terminal, and the finalized
bundle has a hash-valid manifest whose `terminal` object matches
`max_duration / max_duration / max_duration / loop / partial` field for field.
A clean command or valid bundle with missing or misleading terminal metadata is
the prior quiet failure and is not a passing result. Keep the live result in
PR evidence; do not commit the API key, raw capture, audio, or generated
recording directory.
