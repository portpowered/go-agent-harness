# C30 checked PCM16 frame dimensions

This evidence directory belongs only to `audio-runtime-c30-frame-dimension-safety`.
The production lease is limited to the checked sizing API, its tests, the legacy
room format/constructor delegation, and this directory. The consumer imports
only the exported `go-audio/pkg/audio` package; it does not import CLI or room
internals, use credentials, allocate adversarial PCM, or use a physical device.

## Baseline characterization

The bounded pre-fix exported-method reproduction at source
`1f82284abee0bd31a6680310444cea2e4c16ef00` produced:

```text
format={SampleRate:2305843009213717952 Channels:1 FrameDuration:1s}
  FrameSamples=24000, FrameBytes=48000
format={SampleRate:4 Channels:4611686018427387905 FrameDuration:1s}
  FrameSamples=4, FrameBytes=8
```

The first result was a wrapped `int64(rate*duration)` intermediate. The second
result was an unchecked channel product. The repaired API returns the exact
rate-duration result when its final sample and byte counts fit, and rejects the
channel product with `ErrInvalidPCM16FrameSize`; the legacy room methods wrap
that error in `ErrMixerInvalidFormat`.

## Build and bounded runs

Run from the exact source root. Each child has a ten-second watchdog, a fresh
process group, and a 256 MiB address-space limit. Build uses the evidence-local
module with `GOWORK=off`; the runner rewrites a temporary `go.mod` replacement
to the explicit source root, so an alternate checkout is never silently
ignored and existing module manifests are not modified.

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --build --source-root .
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --positive --source-root .
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --negative-control --source-root .
```

The positive consumer uses authored literal answers for 8/16/24/44.1/48 kHz
mono and stereo 20 ms frames plus 1 Hz/1 s, fractional and sign controls. It
also checks the reduced rate-duration boundary, channel-product rejection and
checked byte-capacity boundaries. The negative-control run intentionally changes
the authored `480` answer to `481`; success requires the child to exit nonzero
with a causal `actual_samples=480 mutated_expected=481` mismatch.

`run.py` embeds the built source revision and refuses to run unless the
requested source root/module, current revision, fixture hashes, executable
SHA256 and embedded revision match the build record. It writes exact source
provenance, toolchain/platform, argv, stdout/stderr, exit code, duration and
clean-shutdown evidence to the owned `artifacts/` and `runs/` directories. Those reports are evidence only:
script CI, independent review, guarded merge, and the post-merge exact-artifact
vertical probe remain external gates. This software replay cannot establish
physical/acoustic device consumption or broad project completion.
