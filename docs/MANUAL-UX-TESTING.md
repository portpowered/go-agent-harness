# Manual UX testing: local speaker feedback

This guide covers the repaired local-device requirement for the flagship
session command. It is a bounded, billed manual check and is never part of
ordinary CI. Keep all captures outside the checkout and never publish a
credential, raw PCM, transcript, prompt, or provider payload.

## Repaired requirement

When one session owns both a live local microphone and a live local speaker,
the runtime observes the exact PCM accepted by the speaker and the raw PCM
captured before provider delivery. It holds a finite capture window, correlates
the two streams over their monotonic media positions, and discards only
confirmed assistant-correlated microphone audio before server VAD can see it.
Independent speech is released once, in capture order, within the bounded
latency policy.

The default policy is:

| Setting | Default |
| --- | --- |
| PCM format | mono PCM16 at the negotiated device rate (16 kHz compatibility default; 24 kHz for a live OpenAI/Grok realtime session per PR #350), fixed 480-sample device frames |
| Active evidence | at least 80 ms |
| Correlation | normalized absolute correlation `>= 0.50` |
| Lag search | `-100 ms` through `+100 ms`, inclusive |
| Analysis window | 120 ms |
| Maximum non-feedback release latency | 120 ms |
| Post-playback acoustic tail | 200 ms |
| Silence floor | `-50 dBFS` |

The lag search is rate-aware. Streams with different explicit sample rates are
classified as insufficient evidence; their bytes are never compared as if they
shared a timeline. Silence, insufficient evidence, independent speech, and
headphone-isolated speech are non-feedback outcomes.

The gate declares the true rate each device negotiated, not a hardcoded
constant: a capture device that cannot honor the requested rate opens at
whatever rate it does support (see `openRTCDeviceSourceAtRate`), and every
duration above is computed from that real rate. Declaring the wrong rate would
not surface as an error — it would silently rescale every bound in the table
above without changing the classification outcome, which is why the automated
suite below exercises the gate at a live realtime session's actual 24 kHz rate
in addition to the 16 kHz compatibility default.

The first confirmed loop writes exactly one warning to command stderr:

```text
Acoustic feedback detected: speaker audio is entering the microphone. Use headphones or route assistant audio to a non-speaker/file output.
```

Warning output is separate from the media pumps and contains no audio or
conversation data. Repeated correlated windows do not flood the terminal.

## What remains unchanged

The policy is deliberately limited to the one-host paired-live-device
topology. File input, file output, replay, one-directional device sessions,
and no-audio sessions bypass the controller. Room peer ingress also bypasses
it: room participants remain full duplex and may receive one another's
contentful PCM while their own responses are open. This local speaker remedy
must not suppress or delay room audio.

Platform-specific acoustic echo cancellation (option (b)) is deferred. The
portable implementation uses selective local gating (option (a)) plus runtime
detection and the actionable warning floor (option (c)); a platform AEC would
need a separately supported backend and device capability contract.

## Bounded MacBook check

Run this only when the workstation has a usable default microphone and speaker,
an OpenAI Realtime credential, and permission to open both devices. The
commands below use `gpt-realtime-2.1-mini`, a terse prompt, a 30-second bound,
and a recorded capture. The explicit `default` selectors make the paired
topology visible while retaining the MacBook's default devices; they are
needed because `--record` selects the recording session mode.

From the repository root:

```bash
make build BUILD_CGO_ENABLED=1
AGENT_BIN="$(pwd)/agent-cli/bin/agent"
RUN_ROOT="$(mktemp -d)"

"$AGENT_BIN" devices list

trap 'unset KEY AGENT_MODEL__OPENAI__API_KEY' EXIT HUP INT TERM
KEY=$(tr -d '\r\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
export AGENT_MODEL__OPENAI__API_KEY="$KEY"
unset KEY
```

The device list must show a default `INPUT` and `OUTPUT`. Run the speaker
variant, say a short request such as “say hello and stop” into the selected
microphone, then remain silent while the assistant speaks:

```bash
"$AGENT_BIN" session \
  --provider openai \
  --model gpt-realtime-2.1-mini \
  --audio-in-device default \
  --audio-out-device default \
  --wait-for-close \
  --max-duration 30s \
  --record "$RUN_ROOT/speakers.session.json"
```

Expected speaker evidence is a normally completed assistant response, at most
one feedback warning, and no self-generated VAD cancellation. If the
speaker-to-microphone path is acoustically strong enough to confirm, the one
warning is expected and must recommend headphones or non-speaker/file output.
If the room is quiet or the correlation is below policy, no warning is also a
valid result; do not treat absence of a warning as evidence that the loop was
confirmed.

Repeat with headphones selected as the system output and the same microphone.
During the assistant response, say one short independent interruption. Record
the second run separately:

```bash
"$AGENT_BIN" session \
  --provider openai \
  --model gpt-realtime-2.1-mini \
  --audio-in-device default \
  --audio-out-device default \
  --wait-for-close \
  --max-duration 30s \
  --record "$RUN_ROOT/headphones.session.json"
```

Expected headphone evidence is no feedback warning, preserved independent
speech in capture order, and no false local-feedback suppression. If compatible
headphones are unavailable, record that prerequisite and do not claim this
manual variant passed. The hermetic virtual headphone-shaped regression remains
the evidence for the no-warning/in-order contract.

Do not commit `RUN_ROOT` or its captures. Sanitize any PR evidence to counts,
terminal outcome, warning count, and device direction; omit raw audio and
provider content.

## Automated evidence and room distinction

The required hermetic service/audio/CLI suite is:

```bash
(cd agent-cli && go test ./internal/services/... ./internal/audio/... ./internal/cli/...)
```

The virtual loop covers confirmed suppression, independent speech delivery,
warning rate limiting, terminal cleanup, topology bypasses, replay, and room
peer ingress. The room regression specifically proves Alice receives Bob's
contentful PCM and Bob receives Alice's contentful PCM during overlapping open
responses, with no local feedback disposition or warning. No assertion is
weakened or deleted for this policy.
