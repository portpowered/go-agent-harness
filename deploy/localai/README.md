# LocalAI realtime fixture

This directory provides a local, credential-free realtime voice server for the
workspace. It exposes the OpenAI-compatible WebSocket URL used by the realtime
tests:

```text
ws://localhost:8080/v1/realtime?model=gpt-realtime
```

The image and model catalog are pinned to LocalAI `v4.8.2`; the model artifacts
are downloaded into a named Docker volume and are not stored in the repository.

## Start and stop

From the repository root, start the fixture with the command used by the PRD:

```sh
docker compose -f deploy/localai/docker-compose.yml up -d
```

The first start downloads the four component models. Follow the state transition
with:

```sh
docker compose -f deploy/localai/docker-compose.yml ps
docker compose -f deploy/localai/docker-compose.yml logs -f localai
```

Stop the server and retain the downloaded model cache with:

```sh
docker compose -f deploy/localai/docker-compose.yml down
```

To deliberately remove the cache as well, use `docker compose ... down -v`.

## Readiness

The Compose health check confirms that the HTTP service is alive. The
authoritative readiness check below performs the realtime HTTP upgrade and then
waits for a JSON `session.created` WebSocket event, so an open TCP port or an
incomplete model download does not report ready:

```sh
go run ./deploy/localai/readiness.go -url 'ws://localhost:8080/v1/realtime?model=gpt-realtime' -timeout 2m
```

Expected output:

```text
ready: ws://localhost:8080/v1/realtime?model=gpt-realtime (session.created)
```

The command is intentionally standard-library-only and closes the connection
after the readiness event. A failure prints the attempted endpoint and exits
non-zero; rerun it after the logs show that model preload has completed.

## Pipeline and component models

`models/gpt-realtime.yaml` keeps the original four-stage arrangement and turns
on the streaming paths:

| Stage | Selected model | Reason for selection |
| --- | --- | --- |
| VAD | `silero-vad-ggml` | Small CPU VAD model from the LocalAI gallery. |
| Transcription | `whisper-base` | Smaller Whisper checkpoint suitable for a local English fixture. |
| LLM | `qwen3-1.7b` | The smallest selected Qwen3 gallery variant tagged for chat/agent use; its tokenizer template supports tool calls. |
| TTS | `vits-piper-en_US-amy-sherpa` | English Amy Piper voice served by sherpa-onnx with native streaming and 22.05 kHz output. |

The pipeline sets `streaming.llm`, `streaming.tts`,
`streaming.transcription`, and `streaming.clause_chunking` to `true`. Qwen3
thinking is disabled for spoken replies, while function calling remains
available through the LLM model's tokenizer-template configuration. The Piper
fixture is a single-speaker model, so clients do not need to select a voice.

The component IDs in `models/preload.yaml` resolve through the pinned
v4.8.2 gallery URL in Compose. LocalAI verifies the checksums supplied by that
gallery while downloading. The named cache volume is
`go-agent-harness-localai-model-cache`; inspect its use with:

```sh
docker volume inspect go-agent-harness-localai-model-cache
docker system df -v
```

## Troubleshooting by state

| State | Observable signal | Action |
| --- | --- | --- |
| Downloading | `docker compose ... logs localai` shows gallery/model download jobs and the named volume grows. | Keep the first process running; downloads are retained for later starts. |
| Starting | `docker compose ... ps` reports `health: starting`, or `/readyz` is not yet successful. | Wait for the API and backend preload to finish. |
| Ready | The readiness command prints `session.created`. | Run the LocalAI-backed tests. |
| Failed | The container exits, health becomes `unhealthy`, or readiness reports a non-101 handshake/model error. | Inspect logs, run `docker compose -f deploy/localai/docker-compose.yml config`, and confirm host port 8080 is free. |

Common fixes:

* If port 8080 is occupied, stop the other service or change the host-side
  port and pass the matching whole URL to the tests.
* If a download is interrupted, rerun `docker compose ... up -d`; the named
  cache keeps completed artifacts.
* If a model-load error persists after an upgrade, remove the named cache with
  an explicit `docker volume rm go-agent-harness-localai-model-cache` and start
  again so the pinned gallery entries are installed from scratch.

## Reference measurements

Measurements must be recorded on the host that actually runs the fixture; no
credentials are needed. The repeatable method is:

1. Record the host OS/CPU/RAM, Docker and Compose versions, and the cache's
   initial size.
2. With an empty `go-agent-harness-localai-model-cache`, time `docker compose
   ... up -d` until the readiness command prints `session.created`. Report this
   as first-run download/startup wall-clock time and record the final volume
   size from `docker system df -v`.
3. On a ready connection, send one short deterministic PCM16 utterance and
   record the elapsed time to the first LLM transcript delta and first decoded
   audio delta. The live protocol test owns the exact audio and timing record.

The reference run below was collected from the authoring workstation with an
empty named volume and the pinned image/catalog. First-token and first-audio
latency were measured from `response.create` to the first corresponding
realtime event on a text turn; the first audio delta was also decoded and
verified as non-silent PCM.

| Metric | Reference host/result |
| --- | --- |
| Host | Windows 11 Home, Intel Core i7-13700K, 63.7 GiB RAM, Docker Engine 27.4.0, Compose 2.31.0-desktop.2 |
| First-run download + startup wall clock | 189.9 s from `up -d` on an empty model volume to realtime readiness (image already present) |
| Resulting model-cache disk use | 1.58 GB (`docker system df -v`) |
| First-token latency | 951.5 ms from `response.create` to the first transcript delta |
| First-audio latency | 2187.9 ms from `response.create` to the first audio delta; 32,878 non-silent PCM bytes received |

## Sources and licensing

The pipeline shape follows LocalAI's documented Realtime API configuration and
the checked-in files are original; nothing is copied from the unlicensed
`localai-org/localai-realtime-demo` repository. LocalAI is MIT-licensed. This
story adds no Go module dependency.

* [LocalAI Realtime API](https://localai.io/features/openai-realtime/)
* [LocalAI model gallery](https://localai.io/gallery.html)
* [LocalAI v4.8.2 release](https://github.com/mudler/LocalAI/releases/tag/v4.8.2)
