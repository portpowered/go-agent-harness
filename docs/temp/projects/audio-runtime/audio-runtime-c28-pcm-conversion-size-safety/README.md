# C28 PCM conversion size safety

This evidence directory belongs only to `audio-runtime-c28-pcm-conversion-size-safety`.
The production lease is limited to `go-audio/pkg/audio/pcm16_convert.go` and
`go-audio/pkg/audio/pcm16_convert_test.go`; the consumer, verifier, reports and
PCM fixtures here are the owned evidence surface. No live Realtime credentials,
physical device, or other project worktree is used.

## Characterization

The pre-fix public consumer is built from the pinned source revision and runs
only tiny inputs. It records the actual host width and arithmetic outcomes:

```text
rtk proxy python3 verify_runtime.py --mode characterize \
  --source-revision 98ce636dd67349ba64f22cd7916dd370cf4ba484
```

The 64-bit baseline showed three distinct defects: a positive `MaxInt` source
channel count returned success for empty and tiny identity-shaped inputs,
`MaxInt/2+1` returned `ErrPCM16ConversionAlignment` after its frame size wrapped
to `MinInt`, and `ResamplePCM16([1], 1, MaxInt)` panicked while converting the
rounded `float64` length to `int`. The possible positive zero-wrap candidate
`1<<(word_bits-1)` is not a positive `int`; the consumer records the proof that
doubling every positive `int` channel count cannot produce zero modulo the word
size. The target-channel byte-overflow case is characterized with an independent
integer oracle and is never called against the pre-fix implementation because it
could request an enormous allocation.

## Candidate verification

Build and verify the consumer and yui from the same committed source revision:

```text
rtk proxy python3 verify_runtime.py --mode verify --source-revision <candidate-sha>
```

The verifier records the exact source and executable hashes in
`artifact-manifest.json`, retains raw stdout/stderr under `runs/`, checks
machine-readable known-answer PCM, error identity, rounding, channel layout,
empty input and no-alias effects, and enforces a ten-second consumer watchdog.
`expected-effects.json` contains independent literal sample answers; the two
small immutable PCM files contain the full expected replay bytes, not only hashes.

The replay portion reuses the accepted credential-free C07 audio/tool fixture
read-only. It launches the same-source `yui session --replay` workflow, checks
the exact rendered and provider PCM bytes, `PROBE_TOOL_MARKER_9182`, strict
continuation, `fixture_complete`, 18 provider-wire events, one tool call/result,
five 480-sample 24 kHz trace frames at starts `0,480,960,1440,1920`, and clean
directory replay. Missing fixture or hash provenance is an unavailable
prerequisite, never a pass.

After the candidate is built, the immutable-artifact entry point is:

```text
rtk proxy python3 verify_runtime.py --mode probe \
  --consumer artifacts/pcm-consumer \
  --yui artifacts/yui \
  --effects expected-effects.json
```

Probe mode never rebuilds and requires the executable hashes and source revision
recorded by verify mode. `verify.json`, `probe.json`, and the characterization
report are evidence only; script CI, independent review, guarded merge, and the
post-merge independent vertical probe remain external gates.
