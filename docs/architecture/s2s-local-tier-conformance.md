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

## Attempted measurement: 2026-08-16

The code and negative-control proofs were validated on 2026-08-16 with Go
1.24.2. No live measurement was completed on this workstation: LocalAI was
not listening on the configured endpoint, Docker Desktop was unavailable, and
no `AGENT_MODEL__OPENAI__API_KEY` was present. Therefore every live row below
is intentionally **UNMEASURED**, with no tier assignment. This is not evidence
that either provider serves or does not serve the behavior.

The LocalAI fixture configuration pins the image tag to
`localai/localai:v4.8.2`; that tag is an environment identity, not a substitute
for a completed run.

| Measurement date | Provider / endpoint tier | Behavior | Result | Latency | Observed evidence | Divergence / gating conclusion |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-08-16 | LocalAI / T2 | Audio round trip | **UNMEASURED** — named endpoint-unavailable skip | — | No `session.created` live proof | No conclusion; rerun with the pinned fixture |
| 2026-08-16 | LocalAI / T2 | Three-turn context | **UNMEASURED** — named endpoint-unavailable skip | — | No live conversation | No conclusion; rerun with the pinned fixture |
| 2026-08-16 | LocalAI / T2 | VAD/barge-in | **UNMEASURED** — named endpoint-unavailable skip | — | No live VAD/cancellation sequence | No conclusion; rerun with the pinned fixture |
| 2026-08-16 | LocalAI / T2 | Model-chosen function call | **UNMEASURED** — named endpoint-unavailable skip | — | No live tool event | No conclusion; rerun with the pinned fixture |
| 2026-08-16 | LocalAI / T2 | Image input | **UNMEASURED** — named endpoint-unavailable skip | — | No live image response or no-image control | No conclusion; rerun with the pinned fixture |
| 2026-08-16 | OpenAI / T3 | Audio round trip | **UNMEASURED** — credential-gated skip | — | No live request; credential value was absent and not logged | No conclusion; rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 | Three-turn context | **UNMEASURED** — credential-gated skip | — | No live conversation | No conclusion; rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 | VAD/barge-in | **UNMEASURED** — credential-gated skip | — | No live VAD/cancellation sequence | No conclusion; rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 | Model-chosen function call | **UNMEASURED** — credential-gated skip | — | No live tool event | No conclusion; rerun with `AGENT_MODEL__OPENAI__API_KEY` |
| 2026-08-16 | OpenAI / T3 | Image input | **UNMEASURED** — credential-gated skip | — | No live image response or no-image control | No conclusion; rerun with `AGENT_MODEL__OPENAI__API_KEY` |

## Tier ownership

Until a completed run fills the table, the only safe ownership statement is:

* T1 replay may gate hermetic transport and wire behavior already covered by
  replay fixtures.
* T2 LocalAI may gate only a behavior later recorded as **SERVED** by a passing
  LocalAI case on the pinned image.
* T3 OpenAI remains the gate for every behavior LocalAI is later measured as
  **NOT SERVED**, and remains the independent live reference for served
  behaviors.
* **UNMEASURED** and skipped cases gate nothing.

When a live run is available, replace the affected rows with the exact run
date, endpoint tier, elapsed latency, observed event/output evidence, and the
provider divergence. Do not infer a result from model metadata or a failed
connection.

## Reproducibility and licensing

Start the LocalAI fixture with:

```text
docker compose -f deploy/localai/docker-compose.yml up -d
```

Run the suite from `test/localai` with `GOWORK=off`; OpenAI cases use only the
`AGENT_MODEL__*` environment configuration and never read the repository
`credentials` file. The suite adds no new third-party module to the workspace;
its nested test module uses the already-present `github.com/gorilla/websocket`
v1.5.3 dependency (BSD-3-Clause). The checked-in test and image fixture code
is original and does not copy from `localai-org/localai-realtime-demo`.
