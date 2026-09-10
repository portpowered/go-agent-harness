# C34 replay terminal integrity evidence

This evidence lease uses only the public `recording.OpenReplay`, `Replay.Next`,
and `Replay.Clock` API. `consumer/main.go` is built in a temporary module with
an explicit `go-audio` replacement to the requested `--source-root`; the runner
sets `GOWORK=off` and records the source revision, build command, binary hash,
fixture hashes, stdout, stderr, and exit code.

The supplied pre-repair observation is the literal
`baseline-first-final-only` fixture:

```text
[recording_started, recording_closed, runtime, recording_closed]
post_close_runtime_accepted=true error=<nil>
```

Characterize it against the isolated `1f82284abee0bd31a6680310444cea2e4c16ef00`
source before running candidate verification:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <isolated-before-source> --build --mode characterize
```

Verify the candidate, including valid empty/audio-runtime recordings and every
malformed lifecycle fixture:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <candidate-source> --build --mode verify
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --source-root <candidate-source> --build --mode verify --negative-control
```

The negative control is a valid audio trace mutated after its first clean close;
it must return a nonzero process status, `replay_exposed=false`, and an error
matching `errors.Is(err, recording.ErrIncomplete)` in the public consumer.

The existing software capture/bundle replay regression is run against an exact
already-built `yui` executable without rebuilding it:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c34-replay-terminal-integrity/run.py \
  --runtime-regression --yui <exact-yui-executable>
```

This is software replay evidence only; it does not claim physical device or
acoustic proof.
