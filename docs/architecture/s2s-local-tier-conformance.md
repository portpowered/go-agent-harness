# S2S LocalAI realtime tier conformance

## Measurement boundary

This document is the result ledger for the optional suite in
[`test/localai`](../../test/localai). It is deliberately empirical: a skipped
case is not a served or not-served result. A row may be assigned a gating tier
only after that provider/behavior case completes against a reachable endpoint.

The suite uses one parameterized behavior body for each row and varies only
the endpoint configuration, authentication, model, and audio-rate details.
The behavior assertions are:

| Behavior | Positive observation | Required negative control |
| --- | --- | --- |
| Audio round trip | Decoded PCM16 output RMS is above `0.01` | Well-formed PCM16 silence fails the same assertion |
| Three-turn context | Turn three contains `cobalt-17`, supplied only in turn one | A new session with turn-one and turn-two history withheld does not contain it |
| VAD/barge-in | A second speech segment produces VAD start, cancellation, and a playback flush with audio already in flight | A socket that only accepts audio cannot satisfy the event/order assertions |
| Model-chosen function call | Exactly one output function call has name `lookup_weather` | The identical prompt with no tools yields zero calls and fails the positive assertion |
| Image input | The reply contains `ORBIT`, printed only in the generated image fixture | The same question without the image fails the positive assertion |

## Measurements: 2026-08-16

The code and negative-control proofs were validated with Go 1.24.2. In the
latest same-day run, the pinned LocalAI fixture was reachable at
`ws://localhost:8080/v1/realtime?model=gpt-realtime` after Docker Engine
27.4.0 started `localai/localai:v4.8.2`. The five LocalAI behavior cases ran
against that image. The live command exits non-zero for the two reachable
behaviors whose positive assertions reject the subject; those failures are the
measured **NOT SERVED** evidence below, not skipped or inferred results.
The latest run began at 2026-08-16 21:02 PDT. The v4.8.2 handshake emits
`session.created` after the first client `session.update`, so the bounded probe
and shared connector send that update before waiting for `session.updated`.

OpenAI was credential-gated and did not run because
`AGENT_MODEL__OPENAI__API_KEY` was absent. Its rows remain **UNMEASURED**;
there is no OpenAI tier conclusion from this run.

| Measurement date | Provider / endpoint tier | Behavior | Result | Latency | Observed evidence | Divergence / gating conclusion |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-08-16 21:02 PDT | LocalAI / T2 | Audio round trip | **SERVED** — passing live assertion | 5.045s | 1 audio delta, 73,728 decoded PCM bytes, RMS `0.082722` | T2 may gate audio replay/round-trip work; OpenAI reference measurement remains pending |
| 2026-08-16 21:02 PDT | LocalAI / T2 | Three-turn context | **NOT SERVED** — positive assertion rejected | 2.328s | Replies were `READY`, `READY`, then `UNKNOWN`; withheld-history control also returned `UNKNOWN`, while `cobalt-17` was absent | T2 cannot gate retained context; T3 OpenAI measurement is required |
| 2026-08-16 21:02 PDT | LocalAI / T2 | VAD/barge-in | **SERVED** — passing live assertion | 11.731s | `speech_started=true`, cancellation=true, playback flush=true, 1 audio delta before cancellation | T2 may gate VAD/barge-in work; OpenAI reference measurement remains pending |
| 2026-08-16 21:02 PDT | LocalAI / T2 | Model-chosen function call | **SERVED** — passing live assertion | 6.402s | Exactly one `lookup_weather` call; no-tools control observed 0 calls | T2 may gate model-chosen tool dispatch; OpenAI reference measurement remains pending |
| 2026-08-16 21:02 PDT | LocalAI / T2 | Image input | **NOT SERVED** — positive request rejected | 1.209s | `image input is not supported` with `prediction_failed`; backend advised that an `mmproj` is required; no-image control returned `UNKNOWN` | T2 cannot gate vision; OpenAI is the only remaining live tier that can establish the vision gate |
| 2026-08-16 | OpenAI / T3 (`gpt-realtime-2.1-mini`) | Audio round trip | **UNMEASURED** — credential-gated skip | — | No live request; credential value was absent and not logged | Rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 (`gpt-realtime-2.1-mini`) | Three-turn context | **UNMEASURED** — credential-gated skip | — | No live conversation | Rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 (`gpt-realtime-2.1-mini`) | VAD/barge-in | **UNMEASURED** — credential-gated skip | — | No live VAD/cancellation sequence | Rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 (`gpt-realtime-2.1-mini`) | Model-chosen function call | **UNMEASURED** — credential-gated skip | — | No live tool event | Rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 (`gpt-realtime-2.1-mini`) | Image input | **UNMEASURED** — credential-gated skip | — | No live image response or no-image control | Rerun with `AGENT_MODEL__OPENAI__API_KEY` |

## Tier ownership

The current measured ownership boundary is:

* T1 replay may gate hermetic transport and wire behavior already covered by
  replay fixtures.
* T2 LocalAI may gate audio round trip, VAD/barge-in, and model-chosen function
  calling because those rows are **SERVED** by passing cases on the pinned image.
* T2 cannot gate retained context or vision because those rows are **NOT
  SERVED**. T3 OpenAI remains the required live gate for those behaviors, with
  its measurements still pending.
* OpenAI remains the independent live reference for the LocalAI-served rows.
* **UNMEASURED** and skipped cases gate nothing.

When another live run is available, replace only the affected rows with the
exact run date, endpoint tier, elapsed latency, observed event/output evidence,
and provider divergence. Do not infer a result from model metadata or a failed
connection.

## Reproducibility and licensing

Start the LocalAI fixture with:

```text
docker compose -f deploy/localai/docker-compose.yml up -d
```

Run the suite from `test/localai` with `GOWORK=off`; OpenAI cases use only the
`AGENT_MODEL__*` environment configuration and never read the repository
`credentials` file. The nested test module uses
`github.com/gorilla/websocket` v1.5.3 (BSD-3-Clause). The checked-in PCM16
speech and image fixtures are in-lease and do not copy from the unlicensed
`localai-org/localai-realtime-demo`.
