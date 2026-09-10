## Summary

- Enforce one leading `recording_started` and one final clean
  `recording_closed` event.
- Reject missing, duplicate, non-leading, unclean, and post-close lifecycle
  events before exposing a replay.
- Preserve the public `recording.OpenReplay`, `Replay.Next`, and `Replay.Clock`
  APIs and existing timestamp/PCM/hash behavior.

## Candidate and ancestry

This is the admitted `audio-runtime-c34-replay-terminal-integrity` task. The
strict replay implementation checkpoint is
`b72aa34ef612463abfcafb00d53bf60e41d9a32b`. The current-main integration merge
is `55b3a11a4b4da55efcd664bcb7fe6af2ee176898`; the causal Family-A repair is checkpointed at
`452ffddeb8ef5d7805021968c28f2966f0f1ea6e` and compacted at
`0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`. The candidate merged fresh
`origin/main` at `84f0a9d308729e38da8479f87c3518f1634bad82`; ancestry probes also
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
bounded output capture. Candidate verification and runtime reports were generated
at `0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`, source-pinned to the current-main
integration merge. The rebuilt consumer source hash is
`ac95b4ad6f53fad552984a597f96d5280e5df28979c661eb7502fa36cf865ee3` and its
binary hash is `d70195e5b292f4d49f8cbfa3fe6b0256dad966e618537075819452d984857081`.

## Validation

- `go test -C go-audio ./pkg/recording -count=1` — PASS
- `go test -C go-audio -race ./pkg/recording -count=1` — PASS
- `go vet -C go-audio ./pkg/recording` — PASS
- `make architecture-size-check` — PASS: 181 packages, 1,867 files,
  27,578 functions
- pinned `make lint` — PASS: 0 issues in every module
- pinned `make staticcheck` — PASS
- `make wire-check` — PASS; generated Wire files unchanged
- Family-A causal ordering — PASS: 5 normal and 3 race repetitions; eight input
  appends, four output frames, one connection, one session update, and terminal
  output `01415250024152500341525004415250`
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

The stale-head review rejection was reconciled by merging current `origin/main`
at `84f0a9d308729e38da8479f87c3518f1634bad82` in merge
`55b3a11a4b4da55efcd664bcb7fe6af2ee176898` after the
earlier `c61ee277`
checkpoint. Candidate reports record source
`0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`, origin-main/base revisions,
consumer source hash
`ac95b4ad6f53fad552984a597f96d5280e5df28979c661eb7502fa36cf865ee3`, consumer
binary hash `d70195e5b292f4d49f8cbfa3fe6b0256dad966e618537075819452d984857081`,
the mutated fixture hashes, the source-bundle file hashes, and rebuilt same-source
YUI hash `6899ff98572a8b4e357b9776b07bfd8a31e68e34101e4d35ebc0b707f408c98b`.

The earlier static CI rejection was run `34435976492`, job `102741003399`
(`https://github.com/portpowered/go-agent-harness/actions/runs/34435976492/job/102741003399`),
at old head `ee15be2f0e4093e12c06c32df4e7a8ed4219861d`. Its static gate found
four OpenReplay architecture-size baseline drifts and pinned `goconst` findings;
the focused architecture and lint gates pass on this candidate. The raw run JSON
and job log are saved in this evidence directory. Later green checks for
superseded heads are not reused.

The latest CI rejection was run `34463957535`, job `102828037756`, against
superseded head `b46011889adf71162f4f22a890e9688457f69b8`. Its established
failure was the Family-A final-input broken pipe. The repair is checkpointed at
`452ffddeb8ef5d7805021968c28f2966f0f1ea6e` and compacted at
`0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`; the focused normal/race corpus,
architecture-size, lint, staticcheck, and Wire checks pass on the repaired head.
The current reports are in `runs/replay-lifecycle-owodz04q`,
`runs/replay-lifecycle-1cmlvo_e`, and `runs/replay-lifecycle-v_f0hk5z`.
Current-head script CI must run after this changed candidate is submitted.

This is software replay evidence only; it does not claim physical-device or
acoustic proof.

## Handoff

The candidate is ready for the script-owned CI gate. No CI result is claimed in
this description, and the executor will not poll or duplicate the CI suite.
