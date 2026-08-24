# Test-audio corpus generation via the pinned qwen3-tts backend

This note documents how to regenerate speech-to-speech test audio from the
pinned LocalAI `qwen3-tts-cpp` backend. The immutable model pin lives in
[`s2s-tts-pinning.md`](./s2s-tts-pinning.md) and
[`deploy/localai/models/qwen3-tts-pinned.json`](../../deploy/localai/models/qwen3-tts-pinned.json).

## One-command generation (linux/amd64 only)

```bash
LOCALAI_MODELS_DIR=/path/to/models \
LOCALAI_ENDPOINT=http://127.0.0.1:8080 \
TTS_CORPUS_OUTPUT=/tmp/corpus \
  scripts/generate-audio-corpus.sh
```

The script runs `go run ./go-agent-loop/cmd/ttscorpus`, which:

1. Refuses to run on any host other than linux/amd64 (`ErrUnsupportedPlatform`).
   Emulation (Rosetta/QEMU) is unsupported so audio can never be fabricated on
   an incapable host.
2. Verifies both pinned GGUF checksums before any synthesis:
   - talker `qwen3-tts-cpp/qwen-talker-0.6b-base-Q8_0.gguf` =
     `d54dbaf10591421fa764ed630d764efa717ae40cd959bd48c66d4eb1af226426`
   - tokenizer/codec `qwen3-tts-cpp/qwen-tokenizer-12hz-Q8_0.gguf` =
     `1883beeed99348fc35e23dd225e9082f93f6f8c109330a33d935baa8acdbfd94`
   Legacy endo5501 F16 weights
   (`huggingface://endo5501/qwen3-tts.cpp/qwen3-tts-0.6b-f16.gguf`) are
   explicitly refused.
3. Polls `/readyz` with a bounded timeout, surfacing the last observed error on
   failure; synthesis posts the exact pinned request object to
   `/v1/audio/speech` with a 150-second bound.
4. Writes one WAV per closed-set utterance at each session sample rate
   (16000 Hz, 24000 Hz), validating every clip for non-zero RMS energy
   (> 0.001 normalized) and duration inside `[0.25, 8.0]` seconds before the
   file is kept.
5. Emits `manifest.json` with per-file sha256, duration, sample-rate, and byte
   size metadata, asserting the corpus stays under the 25 MB program budget and
   that no stray WAV is left unmanifested.

## Manifest consumption

`go-agent-loop/pkg/audiofixture` loads the committed corpus at
`go-agent-loop/testdata/audio` bidirectionally: files missing from disk or
added without a manifest entry both fail loading, as does any byte-level
tampering (sha256 mismatch). Clip sanity assertions with silent and truncated
negative controls live in `go-agent-loop/pkg/ttscorpus`.

## Platform gap status

The pinned LocalAI image is linux/amd64 while primary development happens on
darwin/arm64. On incapable hosts generation exits non-zero with the observed
platform mismatch; no synthetic or substitute audio is ever produced. The
committed corpus under `go-agent-loop/testdata/audio` was produced by the
earlier corpus lane; regenerating it from the pinned qwen3-tts backend requires
the linux/amd64 command above and is deferred until such a host runs this lane,
per the program rule against fabricated audio.
