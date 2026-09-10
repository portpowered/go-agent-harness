# C35 canonical PCM byte mixing

This admitted slice moves the legacy room adapter's PCM16 decode/mix/encode
boundary into `go-audio/pkg/mixer.MixPCM16Bytes`. The operation validates every
source and output dimension before decoding or allocating output-sized buffers,
uses the existing `MixPCM16Samples` accumulation and final clip, and returns a
fresh encoded frame. The legacy room keeps sorted attribution, cadence-sized
silence, queue locking, and consumes selected prefixes only after the complete
operation succeeds.

The standalone `consumer/` module imports only the exported `go-audio` mixer
API. Its literal little-endian controls cover extrema, opposing sources, one
final clip, short tails, empty output, order independence, input immutability,
invalid alignment, oversized dimensions, overlong sources, and the source
limit. `verify.py` runs those controls, the focused normal/race/vet/architecture
checks, and—when run with `--mode public` or `--mode all`—the accepted
credential-free yui audio/tool and interruption capture-to-bundle-to-replay
regressions. Public output is software/file replay only; it is not physical,
acoustic, device-consumption, live-provider, or Realtime evidence.

Run from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c35-canonical-pcm-byte-mixing/verify.py --mode focused
```

Every verifier child is launched in its own process group with a maximum
60-second deadline and bounded TERM/KILL/reap cleanup. The public aggregate is
bounded to 600 seconds. Generated binaries, temporary runs, and source
archives stay below the ignored evidence directories or a caller-provided
output directory; no CI polling is performed.
