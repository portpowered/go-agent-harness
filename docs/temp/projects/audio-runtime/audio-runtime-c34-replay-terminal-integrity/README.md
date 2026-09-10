# C34 replay terminal integrity evidence

This checkpoint exercises the public `recording.OpenReplay`, `Replay.Next`, and
`Replay.Clock` APIs. The evidence runner builds a temporary public consumer with
`GOWORK=off`, an explicit `go-audio` replacement to the requested source root,
and records commands, source/build-input revisions, hashes, output, exit codes,
and process cleanup state.

## Revisions and admission

- admitted task: `audio-runtime-c34-replay-terminal-integrity`
- implementation source revision: `1278b1a4eaaaf9784c072b47e8cdcf62b5e3f028`
- fresh `origin/main` merged into the candidate: `431fc96c14f0e0045629d9c36f98ee61ff06e840`
- required startup revision ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- architecture baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`

The reports record successful ancestry probes for the fresh main and both required
startup/baseline revisions. They were generated from a clean detached checkout of
the implementation source above; the submitted evidence/docs descendant changes do
not alter executable inputs.
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

The tested YUI is `/private/tmp/audio-runtime-c34-yui-431fc96c` with SHA-256
`4aa43e242cea79f06c12c0bef2f9d1e061262835eb8e9ee567584f83fa99cba5`. Both
positive replay invocations passed; one reported `15 wire events, 0 tool calls`
and the terminal replay completed with `output_state=complete`. Rendered PCM is
3,360 bytes with SHA-256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.

The same source bundle is copied into two negative controls. Removing
`audio-trace/timeline.jsonl` returns a nonzero diagnostic naming the missing
timeline. Flipping one byte in `speaker-enqueued.wav` returns a nonzero
`PCM integrity mismatch ... at 1440`. Both controls have bounded cleanup and
`descendants_reaped=true`. The report preserves file-by-file hashes for the
source bundle and both mutated copies.

This is software replay evidence only; it does not claim physical-device,
microphone, speaker, or acoustic proof.
