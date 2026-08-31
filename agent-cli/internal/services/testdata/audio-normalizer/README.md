# Harvested realtime voice normalization fixtures

These are minimal, sanitized excerpts from the operator's live loudness probe,
not generated waveforms. The source run used the same short hiking-trip
conversation for two realtime participants on 2026-08-30:

- `alloy-turn-1.pcm` is the first response of Riley (`alloy`) from
  `live-room/out/agent-agent_riley.pcm`.
- `verse-turn-1.pcm` is the first response of Sam (`verse`) from
  `live-room/out/agent-agent_sam.pcm`.

Each fixture keeps the first three seconds (72,000 samples / 144,000 bytes) of
the first response, aligned to 150 20 ms frames at 24 kHz. The source probe
measured whole-turn RMS of approximately -18.1 dBFS for `alloy` and -25.6 dBFS
for `verse`; the retained three-second excerpts measure -18.27 dBFS and
-24.81 dBFS respectively, preserving a 6.55 dB pre-normalization gap.

Fixture SHA-256:

- `alloy-turn-1.pcm`: `ef4821ff807e2d1c750d7d21a9cf1dadac6bfef632cb91bfb91b9d7327159c83`
- `verse-turn-1.pcm`: `36d893965f0b827676b19e20ed391768f82d06b6ad52f1461c956f752bdf78bc`

Only PCM16 little-endian mono samples are committed. Provider session ledgers,
transcripts, prompts, credentials, private paths, and unrelated turns were
discarded. The fixture source and sanitization procedure follow
`scratchpad/failure-fixtures/README.md`: retain a small real waveform excerpt,
record provenance, and keep the regression hermetic and secret-free.

The regression feeds both files through the production session normalizer
boundary and the production two-participant room boundary. It measures active
20 ms frames above the -50 dBFS silence floor, while checking the complete
stream for sample count, peak ceiling, clipping, DC offset, and output-boundary
continuity.
