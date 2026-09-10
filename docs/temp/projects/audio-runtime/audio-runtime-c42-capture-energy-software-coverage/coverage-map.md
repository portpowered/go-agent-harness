# C42 coverage map

## Authority and preserved checkpoints

This task uses the single admitted `audio-runtime` project and the immutable
manifest. Admission was verified with:

```text
project-control.py verify-work --type task --name audio-runtime-c42-capture-energy-software-coverage
=> {"status":"admitted","project":"audio-runtime","name":"audio-runtime-c42-capture-energy-software-coverage"}
```

The isolated worktree branch is
`codex/audio-runtime-c42-capture-energy-software-coverage`. The observed
fetched `origin/main` and current base are
`926ded7bfa8f3c3e42115192d03aa1240c4806db`; required ancestry retains the
baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` and startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`. The planning evidence and
admission/provenance record are in `implementation-provenance.json`.

The prior C32 review findings are carried forward as acceptance controls:

- The consumer build instructions include an explicit consumer-module cwd and
  `GOWORK=off`.
- This verifier uses capped nonblocking readers and bounded TERM/SIGKILL
  cleanup; it has deterministic output-overflow and stubborn-child controls.
- The stale C32 oracle/report is not rewritten. The exact original failed
  vertical report remains available for historical comparison.

## Existing reviewed coverage retained

The C32 consumer definitions are copied into the C42 consumer unchanged and
are checked against `c32-original-expected.json`, whose 40 case identities are:

```text
float-nan-energy
float-square-overflow
float-sum-overflow
float32-half
float32-nan
float32-negative-zero
float32-outside-range
float32-positive-zero
float32-short
float32-stereo-energy
float64-mono-energy
float64-negative-half
float64-outside-range
float64-positive-infinity
float64-short
invalid-channel-stride
invalid-frame-overflow
invalid-valid-bits-too-large
invalid-valid-bits-zero
pcm16-maximum
pcm16-minimum
pcm16-reduced-valid-padding
pcm16-short
pcm16-stereo-padded-energy
pcm24-maximum
pcm24-negative-one
pcm24-short
pcm32-maximum
pcm32-short
pcm64-maximum-rounds-to-one
pcm64-short
pcm8-maximum
pcm8-midpoint
pcm8-mono-energy
pcm8-short
pcm8-zero
short-final-frame
unsupported-encoding
unsupported-pcm-width
zero-frames
```

The existing in-repository tests that back those oracle families remain in
`go-audio/pkg/codec/sample_value_test.go`:

- `TestDecodeSampleValuePreservesSupportedLiteralScaling` and
  `TestDecodeSampleValueRejectsShortInputWithoutPanic` cover scalar PCM and
  float literals, widths, and short buffers.
- `TestDecodeSampleValueRejectsInvalidMetadataAndEncodings` and
  `TestDecodeSampleValueRejectsNonFiniteFloat` cover typed format and
  nonfinite errors.
- `TestDecodeSampleValueDoesNotMutateInput` covers caller ownership.
- `TestPacketEnergyMeasuresLiteralInterleavedSamplesAndSkipsPadding`,
  `TestPacketEnergyMeasuresIndependentMonoAndFloatLiterals`, and
  `TestPacketEnergyZeroFramesDoesNotReadData` cover energy, channels, padding,
  and zero-frame behavior.
- `TestPacketEnergyRejectsDimensionsBeforeTraversal` and
  `TestPacketEnergyRejectsNonFiniteAndOverflowingResults` cover malformed
  dimensions, truncation, nonfinite values, and overflow.

The C32 Windows coverage remains alongside the new direct adapter entry point
in `go-device-gateway/pkg/devices/device_windows_test.go`. Hardware tests keep
their existing skip behavior and are not converted into software evidence.

## New C42 codec controls

`TestPacketEnergyC42Coverage` and the selected regex-compatible alias
`TestWindowsPortablePlaybackBurstPreservesFIFOCaptureEnergyCodecCoverage`
share one helper and execute these literal controls:

- three channels, two frames, PCM16, valid bits 12, stride 8, padding and
  trailing sentinel bytes; samples are
  `[16385/32768, -0.5, 0.25, 0, -1, -0.25]` and energy is
  `1744863233/1073741824`;
- PCM24 valid bits 20, PCM32 valid bits 24, and float64 padded two-channel
  packets with explicit samples and energy;
- padding/trailing mutations preserve energy while measured-byte mutations
  change it, and successful calls preserve all bytes;
- zero-frame trailing NaN returns exact zero, while malformed format/layout
  errors are still typed and non-panicking;
- `0x1p+511` squares to finite `0x1p+1022`, then true accumulation overflow is
  tested for four channels in one frame and four mono frames. A separately
  reported scalar control proves the finite single-square result without
  increasing the exact 13-case C42 oracle set;
- later-channel NaN and later-frame negative infinity return
  `ErrNonFiniteSample` after an adjacent finite literal has been decoded;
- a one-byte-short final padded frame, malformed channel/stride, and an
  unrepresentable frame extent return typed errors; success paths are checked
  with `testing.AllocsPerRun` for zero allocations.

## New C42 Windows adapter controls

`TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureAdapter` is the
selected direct `wasapiCapturePacketEnergy` entry point. It executes the exact
three-channel padded PCM16 example against real storage and checks:

- exact delegated energy and unchanged caller bytes;
- zero-frame and silent nil-pointer bypasses even with malformed metadata;
- non-silent nil input, unsupported subformat, and malformed layout errors;
- later float64 negative infinity and true four-channel accumulation overflow;
- unchanged caller bytes for both rejected nonfinite and overflow packets.

The production Windows call remains the existing
`wasapiCapturePacketEnergy` delegation path. No production repair was needed;
the current implementation already supplied the canonical validation and typed
error behavior demonstrated by these tests.

## Independent consumer matrix

The C42 consumer appends these 13 new literal energy cases to the retained 40:

```text
c42-pcm16-three-channel-padded-valid12
c42-pcm24-two-channel-padded-valid20
c42-pcm32-two-channel-padded-valid24
c42-float64-two-channel-padded
c42-zero-frames-trailing-nonfinite
c42-zero-frames-malformed-format
c42-float64-four-channel-sum-overflow
c42-float64-four-frame-sum-overflow
c42-float64-later-channel-nan
c42-float64-later-frame-negative-infinity
c42-pcm16-truncated-padded-final-frame
c42-pcm16-malformed-channel-stride
c42-pcm16-unrepresentable-frame-extent
```

The expected values are literal data in `c42-expected.json`; they are not
derived from the consumer output. `run.py` requires exactly 53 cases, compares
samples, negative-zero bits, typed error identities, exact energy, metadata,
panic state, and input ownership, then proves that changing one energy oracle
is rejected.

## Public software replay and limits

The positive verifier replays both retained credential-free fixtures. It checks
the audio/tool marker bytes/hash, rendered PCM 3200-byte hash
`7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`, provider
PCM 4800-byte hash
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`, and
directory replay text. It also checks interruption provider PCM hash
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`, the
healthy 2400-byte tail hash
`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`, rendered
PCM hash
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`, and
directory replay text. The negative verifier removes only a generated copy's
timeline and requires strict replay to fail with `missing timeline.jsonl`.

The checks are software/file-replay evidence only. Native Windows WASAPI
endpoint execution and acoustic/physical endpoint consumption are `OUT OF
SCOPE` under the effective `factory/docs/operating-policy.md` user scope
amendment, never PASS and not a prerequisite for software delivery. The
preserved C32 failed report remains byte-for-byte unchanged and is not treated
as an acceptance waiver.
