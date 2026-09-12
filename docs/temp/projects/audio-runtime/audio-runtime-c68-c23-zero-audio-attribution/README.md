# C68 zero-audio attribution

This is the admitted, evidence-only work surface for
`audio-runtime-c68-c23-zero-audio-attribution`. It compares the exact
preserved C23 revision `b2fb41401cd0934378b9ff1bc131532fdc19f614` with the
exact pushed C56 revision `85710a53a2e9449884fb81f979df0599269ef3b3` using
the unchanged C23 public consumer and fixture. No production source, C23
predecessor source, C56 predecessor source, shared Wire code, or baseline
registry is changed here.

## Reproduction

From the repository root, run the bounded phases in order:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/attribute.py prepare --c23-revision b2fb41401cd0934378b9ff1bc131532fdc19f614 --c56-revision 85710a53a2e9449884fb81f979df0599269ef3b3 --child-timeout-seconds 60 --total-timeout-seconds 300
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/attribute.py build --c23-revision b2fb41401cd0934378b9ff1bc131532fdc19f614 --c56-revision 85710a53a2e9449884fb81f979df0599269ef3b3 --child-timeout-seconds 60 --total-timeout-seconds 300
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/attribute.py negative-control --mutation first-provider-audio --c23-revision b2fb41401cd0934378b9ff1bc131532fdc19f614 --c56-revision 85710a53a2e9449884fb81f979df0599269ef3b3 --child-timeout-seconds 60 --total-timeout-seconds 300
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/attribute.py compare --turns 1 --recording off on --c23-revision b2fb41401cd0934378b9ff1bc131532fdc19f614 --c56-revision 85710a53a2e9449884fb81f979df0599269ef3b3 --child-timeout-seconds 60 --total-timeout-seconds 300
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/attribute.py c21-regressions --c23-revision b2fb41401cd0934378b9ff1bc131532fdc19f614 --c56-revision 85710a53a2e9449884fb81f979df0599269ef3b3 --child-timeout-seconds 60 --total-timeout-seconds 300
python3 docs/temp/projects/audio-runtime/audio-runtime-c68-c23-zero-audio-attribution/verify.py --mode all
```

`provenance.json`, `build-manifest.json`, `negative-control.json`,
`comparison.json`, `c21-regressions.json`, and the first-failure pointer under
`artifacts/first-failures/` are the review surface. Full source archives,
child logs, semantic bundles, and binaries stay in ignored task-local
`scratch/` or `artifacts/` paths; the scripts recreate them as needed.

## Result

The canonical comparison contains exactly four positive executions: C23
recording off/on and C56 recording off/on, one logical turn each. Every case
has one provider `AUDIO.DELTA` and one public `AUDIO.DELTA` with 128 PCM16
bytes and SHA-256
`1e587b484cbfe009fae0409678b68a2d80e3576b7aa49e2d43dfca06730dc588`.
Recording-on has a clean terminal and drained queue, but
`accepted_audio=0`, `audio_bytes=0`, and no `semantic/audio/out-000.pcm`.
The first-provider-audio mutation is rejected, proving the audio oracle is
not silently accepting empty input. The focused C21 regression passed normal
`-count=5` and race `-count=1`.

The fail-closed attribution is `C56_RECORDING_DEFECT`. The first divergent
boundary is recording observer admission: `terminalDrainSession` exposes an
empty media capability, `capturingInferencer` marks it attached for the
no-device fixture, and `Observer.Message` skips its normalized-audio fallback
before `RecordAudio`. C23's fixture and oracle are not blamed. The smallest
repair is outside this owned path: preserve provider-media capability absence
through the existing terminal-drain/capturing handoff so the public observer
reaches `RecordAudio`.

This evidence does not claim script CI, review, merge, realtime credentials,
physical devices, or acoustic output. After commit/push/PR handoff, the next
action is the external script CI gate; do not poll it here.
