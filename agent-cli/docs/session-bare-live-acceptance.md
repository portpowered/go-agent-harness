# Bare live-session startup probe

The bare-session probe is the bounded, billed confirmation for the exact
zero-flag voice command. It starts the shipped binary with `session` as its
only argument, supplies the OpenAI credential only through `OPENAI_API_KEY`,
waits for the post-`session.created` `Listening:` readiness banner, and sends
one SIGINT. It does not record raw provider traffic or microphone audio.

The normal and hermetic suites do not run this probe. Run it only when a real
OpenAI Realtime credential and usable default microphone and speakers are
available. The probe uses `gpt-realtime-2.1-mini` and a ten-second readiness
bound; it is intentionally limited to startup, device opening, session
creation/readiness, and clean interruption.

## Run

From the repository root, keep the key out of argv, logs, and artifacts:

```bash
make build BUILD_CGO_ENABLED=1

trap 'unset KEY OPENAI_API_KEY' EXIT HUP INT TERM
KEY=$(tr -d '\r\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
export OPENAI_API_KEY="$KEY"
unset KEY

AGENT_HARNESS_LIVE_BARE_SESSION=1 \
  CGO_ENABLED=1 \
  go test -tags live -v ./agent-cli/test/integration/ \
  -run '^TestLiveBareSessionDefaultDevicesStartsAndStops$'
```

The test itself launches the child as exactly:

```text
<built-agent> session
```

It gives that child an isolated temporary `HOME`, preserves ordinary network
and platform settings, removes inherited `AGENT_*` and alternate API-key
variables, and adds only `OPENAI_API_KEY`. This makes the reported provider,
model, and device identities evidence for the bare resolver and host defaults,
not for a local persisted session override.

## Sanitized evidence

A passing run logs one line shaped like this; copy only this sanitized line to
the PR body:

```text
bare live startup probe: argv=session provider=openai model=gpt-realtime-2.1-mini transport=ws input-device=<host-id> output-device=<host-id> session.created=observed readiness=listening elapsed-to-listening=<duration> sigint=one terminal=clean
```

`Listening:` is emitted only by the session runtime after the normalized
provider `session.created` event and both registry-backed device opens. The
probe also requires exactly one
`classification=user_cancelled terminal_reason=cancellation
terminal_provenance=cli output_state=none` terminal record and exit status 0.

Never publish the key, environment dumps, authorization headers, raw provider
payloads, raw audio, or unsanitized stderr. If the host lacks either default
direction, record the direction-specific prerequisite error instead of
claiming a live pass.
