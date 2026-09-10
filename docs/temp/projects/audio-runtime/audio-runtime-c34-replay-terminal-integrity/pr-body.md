## Summary

- Enforce one leading `recording_started` and one final clean
  `recording_closed` event.
- Reject missing, duplicate, non-leading, unclean, and post-close lifecycle
  events before exposing a replay.
- Preserve the public `recording.OpenReplay`, `Replay.Next`, and `Replay.Clock`
  APIs and existing timestamp/PCM/hash behavior.

## Candidate and ancestry

This is the admitted `audio-runtime-c34-replay-terminal-integrity` task. The
implementation source tested by the evidence reports is
`1278b1a4eaaaf9784c072b47e8cdcf62b5e3f028`. The candidate merged fresh
`origin/main` at `431fc96c14f0e0045629d9c36f98ee61ff06e840`; ancestry probes also
pass for startup `8bdafc7f947a3a2c9856220abdc539437035bd21` and architecture
baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Repair of the rejected candidate

The previous head `ee15be2f0e4093e12c06c32df4e7a8ed4219861d` was rejected by the
static CI job for four OpenReplay architecture-baseline drifts and four pinned
lint `goconst` findings. The raw run and job log remain in this evidence
directory. The repair keeps the admitted manifest and required baseline
unchanged; the focused architecture-size and pinned lint gates pass locally.

The evidence runner also repairs the previously non-causal controls: it runs a
real valid-audio trace mutated after its first close, tests missing-timeline and
corrupt-audio bundle failures, and asserts bounded process-group cleanup and
bounded output capture.

## Validation

- `go test -C go-audio ./pkg/recording -count=1` — PASS
- `go test -C go-audio -race ./pkg/recording -count=1` — PASS
- `go vet -C go-audio ./pkg/recording` — PASS
- `make architecture-size-check` — PASS
- pinned `make lint` — PASS
- Public consumer verification — PASS: valid empty/audio-runtime sessions
  accepted to EOF; eight malformed lifecycle fixtures returned `ErrIncomplete`
  with `replay_exposed=false`.
- Dedicated mutated-valid-audio negative control — PASS: exit 1,
  `ErrIncomplete`, `replay_exposed=false`, no descendant leak.
- Existing software replay regression — PASS: both positive replays completed;
  rendered PCM is 3,360 bytes with SHA-256
  `302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.
- Missing timeline and corrupt audio controls — PASS: both fail with causal
  diagnostics and no descendant leak.

The latest stale-head review rejection was reconciled by merging current
`origin/main` in `1278b1a4eaaaf9784c072b47e8cdcf62b5e3f028`. The clean-source
reports record that exact source/base pair and the evidence-only docs/report
descendant does not change executable inputs. The evidence reports record exact
source/base revisions, consumer source hash
`ac95b4ad6f53fad552984a597f96d5280e5df28979c661eb7502fa36cf865ee3`, consumer
binary hash `9fc86f9e6e150a73ae78d80a0249f7d12c79d2048c420dd711deaa0a58a8b0e9`,
the mutated fixture hashes, the source-bundle file hashes, and rebuilt same-source
YUI hash `4aa43e242cea79f06c12c0bef2f9d1e061262835eb8e9ee567584f83fa99cba5`.

This is software replay evidence only; it does not claim physical-device or
acoustic proof.

## Handoff

The candidate is ready for the script-owned CI gate. No CI result is claimed in
this description, and the executor will not poll or duplicate the CI suite.
