# C146 public trace publication recovery

This admitted `audio-runtime` recovery adopts the existing
`codex/audio-runtime-c117-repair-c113-public-trace-publication` worktree and
PR #503. The C117 checkpoint, C113 reproduction, C141 FAILED report, C112
ancestry, and current `origin/main` ancestry remain preserved.

The implementation binds the public sessiontrace `DeviceBinding` at the
livehost boundary. Capture pre-gate and provider upload observations come from
the capture source or the successful provider handoff. Speaker enqueue remains
the provider playback observer; speaker rendered is supplied by the selected
device's actual callback, or by the loopback device-server's cumulative
consumption snapshot when that public device exposes no callback setter. The
snapshot is transport evidence of the simulated callback only, never queue,
file, replay, or acoustic evidence.

Build the candidate artifacts and run the bounded evidence matrix with:

```text
rtk proxy go build -trimpath -o docs/temp/projects/audio-runtime/audio-runtime-c146-recover-c117-public-trace-publication/artifacts/yui ./agent-cli/cmd/yui
rtk proxy go build -trimpath -o docs/temp/projects/audio-runtime/audio-runtime-c146-recover-c117-public-trace-publication/artifacts/audio-device-server ./agent-cli/cmd/audio-device-server
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c146-recover-c117-public-trace-publication/run.py --case c141-simulated-duplex-four-tap --case render-tap-unavailable --case malformed-odd-pcm --case stale-provenance --case healthy-audio-tool --case interruption --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c146-recover-c117-public-trace-publication/verify.py --mode all
```
The four-tap runner first records the exact C141 device-input preflight failure
against the immutable text-only source fixture, then uses one captured PCM
frame in a derived, resealed fixture so the final replay is finite and strict.
The source fixture hash and the preflight failure remain in `runs/report.json`;
the derived fixture is not presented as a replacement source artifact.

All evidence is credential-free software/file or simulated callback evidence.
Native Windows hardware and physical/acoustic proof are out of scope and never
pass. Local evidence is ready for the script CI gate; it is not a claim that
CI, independent review, guarded merge, or the post-merge vertical probe is
green.
