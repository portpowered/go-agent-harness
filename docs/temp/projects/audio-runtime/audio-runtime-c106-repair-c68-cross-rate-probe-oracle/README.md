# C106 cross-rate probe oracle

This evidence is for the admitted `audio-runtime-c106-repair-c68-cross-rate-probe-oracle` task only.

The regression owns one credential-free public-session replay path. The provider boundary is 24 kHz PCM16 (2400 samples, 4800 bytes); the public file sink is 16 kHz PCM16 (1600 samples, 3200 bytes). The oracle decodes both streams, runs `wavio.NewPCM16Resampler(24000, 16000)` with final flush, checks exact rational duration, and compares every converted sample. It deliberately does not compare unequal-rate raw bytes.

Recording-off and recording-on public PCM are identical. The same-rate 128-byte control is identical across modes. The no-media recording observer control retains the C68 conclusion as `RECORDING_OBSERVER_OMISSION_UNRESOLVED` with `accepted_audio=0` and `audio_bytes=0`; C106 does not claim that defect repaired. The canonical C102 report remains immutable and `FAILED`.

The task changes only the owned regression test and this evidence directory. Native Windows hardware, acoustic playback, and broader project completion remain out of scope.

Run the fail-closed verifier from this directory or the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c106-repair-c68-cross-rate-probe-oracle/verify.py --mode all
```
