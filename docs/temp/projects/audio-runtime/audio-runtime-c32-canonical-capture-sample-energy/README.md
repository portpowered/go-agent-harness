# C32 canonical capture sample and packet energy

This evidence directory owns the platform-neutral public consumer and its
independent literal oracle. The production change is limited to the named
codec/audio helpers, the Windows adapter delegation, and the portable Windows
regression test.

The public APIs are `codec.DecodeSampleValue` and `codec.PacketEnergy`.
`SampleFormat.ValidBitsPerSample` is validated metadata; normalization always
uses the complete container width so reduced-valid-bit samples preserve the
historical scaling and padding is not silently shifted.

The repository `go.work` intentionally lists production modules only. Build the
consumer from the `go-audio` module directory with workspace mode disabled so
the command is reproducible and cannot accidentally resolve a sibling module.
Replace `/absolute/path/to/go-agent-harness` with this checkout's absolute
repository root:

```text
REPO=/absolute/path/to/go-agent-harness
(
  cd "$REPO/go-audio"
  rtk proxy env GOWORK=off go build -o /tmp/c32-capture-consumer \
    ../docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/consumer/main.go
)
(
  cd "$REPO"
  rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/verify.py \
    --consumer /tmp/c32-capture-consumer
  rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/verify.py \
    --consumer /tmp/c32-capture-consumer --negative-control
)
```

The verifier compares 40 literal decode/energy cases, checks unchanged input,
finite successful outputs, stable error identities, clean bounded shutdown and
required source ancestry. Its selectors cap each captured child stream at 1
MiB before storing output, and its deterministic internal controls exercise
both overflow termination and SIGKILL reaping. Run those controls alone with:

```text
cd /absolute/path/to/go-agent-harness
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c32-canonical-capture-sample-energy/verify.py \
  --bounds-only
```

`--negative-control` changes the independent 1.5 energy oracle to 1.25 in
memory and requires validation to reject it; the tracked `expected.json` is
never modified.

Native WASAPI execution is unavailable on the current Darwin host and remains
`BLOCKED`; the Windows build and portable callback test are separate software
proof. The existing Windows CI job must execute
`TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureEnergy` through
its current test-name regex. This work claims no hardware or acoustic proof.
