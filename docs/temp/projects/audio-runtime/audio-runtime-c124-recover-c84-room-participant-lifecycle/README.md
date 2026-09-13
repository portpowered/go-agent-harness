# C124 recovery evidence

This directory contains candidate-independent recovery proof assets:

- `external-consumer/` is a `GOWORK=off` module that imports the public rooms
  contract and rooms Wire package and exercises connection, readiness, bounded
  admission, terminal cancellation and owned-session close.
- `verify.py` pins the recovery ancestry, runs the two causal mutation controls,
  checks retirement, Wire/architecture reconciliation, and excluded-path
  ownership.
- `run.py` builds a finalized two-participant offline replay from the committed
  C16 fixture, launches the shipped `yui`, checks participant terminal/tool/
  software-audio effects and process-group cleanup, rejects a malformed late
  terminal bundle, and reruns the non-room audio/tool replay.

Run the C124 vertical after building the binary:

```sh
rtk proxy go build -o docs/temp/projects/audio-runtime/audio-runtime-c124-recover-c84-room-participant-lifecycle/artifacts/yui ./agent-cli/cmd/yui
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c124-recover-c84-room-participant-lifecycle/run.py --case multi-participant-room-replay --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c124-recover-c84-room-participant-lifecycle/run.py --case malformed-or-late-terminal --child-timeout 60 --aggregate-timeout 180
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c124-recover-c84-room-participant-lifecycle/run.py --case non-room-audio-tool --child-timeout 60 --aggregate-timeout 240
```

All generated run bundles and reports remain under this directory. Replay is
credential-free and software-only: it does not open host devices, use a
Realtime session, or claim acoustic playback.
