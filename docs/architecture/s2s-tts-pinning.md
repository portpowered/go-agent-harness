# S2S LocalAI Qwen3 TTS pin

This note is the handoff for `s2s-b2-audio-corpus-generator`. It pins the
LocalAI Qwen3 TTS model and the audio contract used by future corpus work. This
lane does not regenerate or edit corpus fixtures.

## Decision

The legacy file
`C:/Users/andre/.mangaka/models/qwen3-tts-0.6b-f16.gguf` is not the format
accepted by the migrated `qwen3-tts-cpp` backend. LocalAI PR [#10316](https://github.com/mudler/LocalAI/pull/10316)
states that the old `endo5501/qwen3-tts.cpp` F16 weights are incompatible with
the new `qwentts.cpp` backend. The requested legacy file was also absent on the
verification workstation, so no successful legacy load is claimed.

The replacement is the LocalAI gallery entry `qwen3-tts-cpp`, resolved at the
LocalAI `v4.8.2` gallery revision. The machine-readable source of truth is
[`deploy/localai/models/qwen3-tts-pinned.json`](../../deploy/localai/models/qwen3-tts-pinned.json);
the LocalAI runtime model file is
[`deploy/localai/models/qwen3-tts-cpp.yaml`](../../deploy/localai/models/qwen3-tts-cpp.yaml).

## Immutable pin

| Field | Pinned value |
| --- | --- |
| LocalAI release | `v4.8.2`, commit `5ff25d9d145e0a03a5b9a3559c620f1e1204ca6d` |
| LocalAI image | `localai/localai@sha256:5bc7df534c906ed6103cd08c2c37e81368d36657433e9f4305c9588932e01ab1` (`linux/amd64`) |
| Backend | `qwen3-tts-cpp` |
| Backend image | `quay.io/go-skynet/local-ai-backends@sha256:8d60c0c8a2221775f4235c38583af87de8edcad0be780b00fd4b627cde2a4e4b` |
| qwentts.cpp | commit `0bf4a18b22e8bb8718d95294e9f7f45c0d4270a4` |
| Gallery model | `qwen3-tts-cpp` |
| Talker | `qwen3-tts-cpp/qwen-talker-0.6b-base-Q8_0.gguf`, SHA-256 `d54dbaf10591421fa764ed630d764efa717ae40cd959bd48c66d4eb1af226426` |
| Tokenizer/codec | `qwen3-tts-cpp/qwen-tokenizer-12hz-Q8_0.gguf`, SHA-256 `1883beeed99348fc35e23dd225e9082f93f6f8c109330a33d935baa8acdbfd94` |

Both GGUF files are from
[`Serveurperso/Qwen3-TTS-GGUF`](https://huggingface.co/Serveurperso/Qwen3-TTS-GGUF).
The checksums above are the checksums in the pinned [LocalAI gallery
revision](https://raw.githubusercontent.com/mudler/LocalAI/v4.8.2/gallery/index.yaml),
not a filename or a mutable gallery label.

## Compatibility probe and migration

The bounded probe command was:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode Compatibility -LegacyArtifactPath C:/Users/andre/.mangaka/models/qwen3-tts-0.6b-f16.gguf -Endpoint http://127.0.0.1:8080
```

Captured output on 2026-08-16:

```text
CONTRACT=PASS fields=all-required-fields
COMPATIBILITY=UNAVAILABLE reason=legacy-artifact-absent path=C:/Users/andre/.mangaka/models/qwen3-tts-0.6b-f16.gguf
ENDPOINT=status=200 uri=http://127.0.0.1:8080/readyz
```

Therefore the local probe did not load the old file: the file was not present.
The incompatibility classification comes from the upstream migration result,
not from treating an absent file as a successful load. The old gallery talker
identity recorded by PR #10316 was
`huggingface://endo5501/qwen3-tts.cpp/qwen3-tts-0.6b-f16.gguf`, SHA-256
`0b89770118463af8f2467d824a8de57d96df6a09f927a9769a3f7b7fffa7087d`.

The exact gallery migration command is:

```powershell
local-ai models install qwen3-tts-cpp
```

It must resolve the two replacement files listed above. Do not keep or select
the old F16 talker under the new backend name.

## Audio contract

- Container: RIFF/WAVE.
- Samples: signed little-endian 16-bit PCM (`s16le`).
- Sample rate: 24,000 Hz.
- Channels: mono (1).
- RMS: square root of the mean of `(signed_int16 / 32768)^2`.
- A valid clip has RMS strictly greater than `0.001`.
- A valid clip has duration in the inclusive range `0.25` through `8.0` seconds.
- Probe text: `The local voice pin is stable.`
- Fixed model option: `seed:17`; request speed is `1.0`, response format is `wav`.

The validator checks the contract field by field and hashes only the exact
configured relative filenames. If a configured artifact is missing, S1 reports
a named hash-check skip; it never searches for or substitutes another model.

Run the offline S1 check with:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode Contract
```

The verification workstation output was:

```text
CONTRACT=PASS fields=all-required-fields
ARTIFACT_HASH=SKIP reason=ArtifactRoot-not-provided
```

When the model volume is available, pass its root explicitly, for example:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode Contract -ArtifactRoot $env:LOCALAI_MODELS_DIR
```

## Clip generation and negative controls

The exact bounded live command is:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode Live -Endpoint http://127.0.0.1:8080 -ArtifactRoot $env:LOCALAI_MODELS_DIR -OutputPath $env:TTS_OUTPUT
```

The script posts the `probe.request` object from the JSON pin to
`/v1/audio/speech`, writes a WAV, decodes it, measures RMS and duration, and
fails on silence, malformed audio, or an out-of-range duration. The separate
measurement command is:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode Measure -AudioPath $env:TTS_OUTPUT
```

The negative-control command is:

```powershell
pwsh -NoProfile -File deploy/localai/models/verify-qwen3-tts-pin.ps1 -Mode NegativeControls
```

It generated a silent WAV and a zero-length file. Both failed the same audio
validation, and the command completed with:

```text
NEGATIVE_CONTROL=PASS name=silent_clip reason=TTS_PIN_FAIL: audio RMS 0.000000 is not strictly above silence threshold 0.001000
NEGATIVE_CONTROL=PASS name=zero_length_file reason=TTS_PIN_FAIL: audio file is zero-length or shorter than a WAV header: <temporary path>
NEGATIVE_CONTROLS=PASS silent_clip=failed-validation zero_length_file=failed-validation
```

No real clip RMS or duration is recorded here because the pinned base Q8 model
was not present in the verification workstation's LocalAI model volume. The
live command therefore must remain a skip until that exact pair is installed;
an observed clip from the concurrently running custom-voice Q4 model would not
be evidence for this pin.

## Bounded live behavior

The readiness probe uses a 3-second timeout. Synthesis uses a separate
20-second timeout. If `readyz` is unreachable, the live command exits
successfully with `LIVE_SKIP reason=localai-endpoint-unreachable ...`. If the
endpoint is ready but `LOCALAI_MODELS_DIR` or either selected GGUF is absent, it
exits successfully with a named `LIVE_SKIP` reason. A live LocalAI process and
the 1.8 GB-class model pair are therefore not CI prerequisites.

## Corpus handoff and licensing

The corpus-generator lane should read the JSON pin, select the `qwen3-tts-cpp`
runtime model, verify the two artifact hashes, and use the bounded live command
as its generation reference. It should consume the 24 kHz mono PCM16 WAV
contract above. This lane changed no corpus script, fixture, or session code.

No third-party Go or npm modules were added. The pinned LocalAI project is MIT;
the qwentts.cpp backend is MIT; the Qwen3-TTS model and tokenizer are Apache-2.0
upstream artifacts. No code or assets were copied from
`localai-org/localai-realtime-demo`.
