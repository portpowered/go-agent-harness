# C34 replay terminal integrity evidence

This checkpoint exercises the public `recording.OpenReplay`, `Replay.Next`, and
`Replay.Clock` APIs. The evidence runner builds a temporary public consumer with
`GOWORK=off`, an explicit `go-audio` replacement to the requested source root,
and records commands, source/build-input revisions, hashes, output, exit codes,
and process cleanup state.

## Revisions and admission

- admitted task: `audio-runtime-c34-replay-terminal-integrity`
- strict replay implementation checkpoint: `b72aa34ef612463abfcafb00d53bf60e41d9a32b`
- current-main integration merge: `55b3a11a4b4da55efcd664bcb7fe6af2ee176898`
- causal Family-A repair checkpoint: `452ffddeb8ef5d7805021968c28f2966f0f1ea6e`
- compacted Family-A baseline repair checkpoint: `0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`
- evidence lineage: current-main integration plus the admitted Family-A
  terminal-fixture repair; unrelated worktree changes remain untouched
- fresh `origin/main` merged into the candidate: `84f0a9d308729e38da8479f87c3518f1634bad82`
- required startup revision ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- architecture baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`

The reports record successful ancestry probes for the fresh main and both required
startup/baseline revisions. Candidate verification and the runtime regression were
generated at source `0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`, with
`origin_main_revision=84f0a9d308729e38da8479f87c3518f1634bad82`. The source
candidate verification began with only the preserved untracked operator note as
dirty; subsequent report runs observed the earlier report files as bookkeeping
changes. The final evidence head does not alter executable inputs. The rebuilt
public consumer has
source hash `ac95b4ad6f53fad552984a597f96d5280e5df28979c661eb7502fa36cf865ee3`
and binary hash `d70195e5b292f4d49f8cbfa3fe6b0256dad966e618537075819452d984857081`.
The operator's untracked `meta-operator-throughput-feedback.md` remains
untouched and is outside the admitted owned paths.

## Baseline characterization

The supplied pre-repair observation is the literal
`baseline-first-final-only` trace:

```text
[recording_started, recording_closed, runtime, recording_closed]
post_close_runtime_accepted=true error=<nil>
```

It was characterized against the isolated historical source
`1f82284abee0bd31a6680310444cea2e4c16ef00`:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <isolated-before-source> --build --mode characterize
```

## Candidate verification

The committed runner verifies two valid public sessions (empty and audio/runtime)
to EOF, and rejects eight malformed lifecycle fixtures before exposing a replay:
missing leading start, duplicate start, missing close, unclean close, duplicate
close, event after close, audio after close, and the baseline first-close trace.
Every rejection is a nonzero public-consumer exit with
`errors.Is(err, recording.ErrIncomplete)`, `replay_exposed=false`, and a reaped
process group.

The dedicated negative control starts from a valid audio trace and appends a
runtime event plus a second close after the first clean close. Its timeline hash
is `c94a802492721f645d3c45d7fc2ba23ba57f83c6e29c7a57e7d68aebeb98a0f3`; it exits
1 with `event "runtime" follows recording_closed`, `ErrIncomplete`, and no
exposed replay.

## Family-A terminal repair

CI run `34463957535`, job `102828037756`, rejected the prior candidate because
`TestFamilyAIterativeBuildUpThroughShippedProcess` sent its final
`input_audio_buffer.commit` after the fixture had already closed on final
silence. The provider therefore reported a broken pipe after four input
speech/silence pairs and four confirmation frames. The admitted repair keeps the
fixture in `--wait-for-close` mode, arms the final-silence boundary before the
terminal response, and emits exactly one `session.closed` only after the final
input commit. Early and unexpected-close paths remain negative controls; send
errors still become provider protocol failures rather than being swallowed.

The causal test passes 5 normal repetitions and 3 race repetitions. Its observed
terminal output remains `01415250024152500341525004415250`, with eight input
appends, four output frames, one connection, and one session update.

Reproduce the candidate reports with:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <candidate-source> --build --mode verify \
  --output docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/candidate-final-report.json
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <candidate-source> --build --mode verify --negative-control \
  --output docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/candidate-negative-control.json
```

The runner bounds stdout/stderr capture at 1 MiB, uses process groups for
termination, and records/asserts bounded cleanup, no output truncation, and
`descendants_reaped=true` for every consumer/build command.

## Existing software replay regression

The existing C12 interruption bundle is replayed through an exact prebuilt YUI
binary, not rebuilt by the regression command:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --runtime-regression --yui <exact-yui-executable> \
  --output docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/runtime-regression.json
```

The tested YUI is `/private/tmp/audio-runtime-c34-yui-0158156/yui` with SHA-256
`6899ff98572a8b4e357b9776b07bfd8a31e68e34101e4d35ebc0b707f408c98b`. It was
rebuilt from the exact candidate source used by the runtime report. Both
positive replay invocations passed; one reported `15 wire events, 0 tool calls`
and the terminal replay completed with `output_state=complete`. Rendered PCM is
3,360 bytes with SHA-256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.

The same source bundle is copied into two negative controls. Removing
`audio-trace/timeline.jsonl` returns a nonzero diagnostic naming the missing
timeline. Flipping one byte in `speaker-enqueued.wav` returns a nonzero
`PCM integrity mismatch ... at 1440`. Both controls have bounded cleanup and
`descendants_reaped=true`. The report preserves file-by-file hashes for the
source bundle and both mutated copies. The current runtime report is in
`runs/replay-lifecycle-v_f0hk5z`.

This is software replay evidence only; it does not claim physical-device,
microphone, speaker, or acoustic proof.

## Rejection reconciliation

Review attempts `work-review-148` and `work-review-154` rejected stale ancestry
and provenance; `work-review-173` recorded the same stale-main issue. The
candidate preserved those findings, merged the exact current `origin/main`
`84f0a9d308729e38da8479f87c3518f1634bad82` in
`55b3a11a4b4da55efcd664bcb7fe6af2ee176898`, and regenerated the
source-pinned reports with `contains_origin_main=true`. The runner includes the
requested real mutated-valid-audio control, missing-timeline and corrupt-audio
controls, bounded output capture, and descendant process-group cleanup
assertions; all pass.

The earlier static CI rejection was run `34435976492`, job `102741003399`
(`https://github.com/portpowered/go-agent-harness/actions/runs/34435976492/job/102741003399`),
against old head `ee15be2f0e4093e12c06c32df4e7a8ed4219861d`. Its static gate found
four OpenReplay architecture-size baseline drifts and pinned `goconst` findings
in `replay.go` and `replay_lifecycle_test.go`; the focused architecture and lint
gates now pass locally. The full raw run JSON and job log remain in this evidence
directory; later green checks for superseded heads are not reused.

The latest CI rejection was run `34463957535`, job `102828037756`, against the
superseded head `b46011889adf71162f4f22a890e9688457f69b8`. Its established
failure was the Family-A final-input `broken pipe` described above. The repair is
checkpointed at `452ffddeb8ef5d7805021968c28f2966f0f1ea6e` and compacted at
`0158156c0fb72e5fceb0ebe25b7becc5ba5324ce`; focused normal/race repetitions,
architecture-size, lint, staticcheck, and Wire checks pass on the repaired head.
The refreshed candidate, dedicated negative-control, and runtime reports are in
`runs/replay-lifecycle-owodz04q`, `runs/replay-lifecycle-1cmlvo_e`, and
`runs/replay-lifecycle-v_f0hk5z`. This candidate is ready for the script-owned
CI gate; no current CI result is claimed here.
