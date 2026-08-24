#!/usr/bin/env bash
# One-command pinned qwen3-tts test-audio corpus generation (linux/amd64 only).
#
# Prerequisites on the host:
#   1. The pinned LocalAI v4.8.2 backend running at $LOCALAI_ENDPOINT
#      (default http://127.0.0.1:8080), e.g. via docker with the image pinned in
#      deploy/localai/models/qwen3-tts-pinned.json.
#   2. Both pinned GGUFs installed under LOCALAI_MODELS_DIR, verified against
#      docs/architecture/s2s-tts-pinning.md before any synthesis:
#        local-ai models install qwen3-tts-cpp
#
# The script exits non-zero on darwin/arm64 or any non-linux/amd64 host, on any
# checksum mismatch, and if the backend cannot be reached. It never fabricates
# audio. See docs/architecture/s2s-tts-pinning.md for the pin contract.
set -euo pipefail

cd "$(dirname "$0")/.."
exec go run ./go-agent-loop/cmd/ttscorpus \
  -models "${LOCALAI_MODELS_DIR:?set LOCALAI_MODELS_DIR to the LocalAI models directory}" \
  -endpoint "${LOCALAI_ENDPOINT:-http://127.0.0.1:8080}" \
  -output "${TTS_CORPUS_OUTPUT:?set TTS_CORPUS_OUTPUT to the corpus output directory}"
