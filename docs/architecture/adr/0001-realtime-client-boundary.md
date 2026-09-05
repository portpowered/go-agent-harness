# ADR-0001: Realtime client boundary

Date: 2026-08-22
Status: accepted; audio ownership amended 2026-09-05

## Decision

Adopt `github.com/WqyJh/go-openai-realtime/v2` at version **`v2.0.0-rc`** as the
wire client for OpenAI-compatible realtime WebSocket sessions, plugged into the
gateway-owned `RealtimeTransport` / `RealtimeSession` seam: the gateway keeps
defining those interfaces and owns lifecycle, error policy, barge-in/cancel,
and audio plumbing; the library supplies dial/handshake, typed event
encode/decode, and frame transport beneath them.

## Audio ownership amendment (2026-09-05)

The [audio subsystem and device gateway decision](../audio-subsystem-device-gateway-design.md)
supersedes this ADR's placement of audio decoding, RMS/DSP and device plumbing
inside the provider gateway. `go-audio` owns audio payload parsing, DSP,
buffers and clocks; `go-device-gateway` owns hardware and device workers.
The provider gateway retains provider protocol envelopes and response/cancel
coordination. Session services compose those boundaries, and the agent loop
exchanges bounded memory buffers with audio workers. The wire client decision
and the historical probe evidence below are unchanged.

## Evidence

All claims below come from a bounded probe run on 2026-08-22 (disposable Go
module outside the tracked tree) that drove the library itself at the pinned
revision against the program's two tiers, under explicit deadlines (dial 15 s,
per-read 20 s, overall 120 s):

- **T3 OpenAI** (`wss://api.openai.com/v1/realtime?model=gpt-realtime-2.1-mini`,
  operator-supplied model/key configuration): **complete turn**, twice.
  Session update acknowledged (`session.updated`), one input turn appended and
  committed, `response.create` answered by a full response lifecycle ending in
  `response.done` with status `completed`. Decoded PCM16 output audio was
  non-silent both times: 110,400 samples at RMS 0.069399, and 44,400 samples at
  RMS 0.068957 (silence threshold 0.01).
- **T2 LocalAI** (`ws://localhost:8080/v1/realtime?model=gpt-realtime`, the
  `deploy/localai` fixture): dial, handshake, `session.created`, and
  `session.updated` all succeeded through the library. The turn itself failed
  inside the fixture's transcription stage; the library surfaced it verbatim as
  a typed `error` event (`transcription_failed`: "rpc error: code = Canceled
  desc = context canceled") instead of hanging or misreporting success,
  reproduced identically across two runs. This is a fixture-side service
  failure, not a transport defect; the historical hand-rolled client saw the
  same class of LocalAI server errors.

Licence position at the probed revision, verified against section 9.2 of
`docs/architecture/s2s-program-rules.md` ("import as a normal Go module"):
candidate `LICENSE` is MIT; transitive `WqyJh/jsontools` v0.3.1 is MIT;
`coder/websocket` v1.8.12 is ISC; `stretchr/testify` v1.9.0 is MIT and
test-only. Verdict: **section 9.2 pass**. Nothing was copied from the
unlicensed `localai-org/localai-realtime-demo`.

Known gap found by the probe: the typed `SessionUpdateEvent` cannot express
`turn_detection: null` (the field is `omitempty` and its union marshals an
error when empty), so turning off server VAD needs one raw frame via
`SendMessageRaw`. Everything else used the typed API.

Draft-era claims not re-verified by this probe — release counts, upstream-vs-
fork revision deltas, line-count estimates for hand-rolling — are dropped from
this record.

## Consequences

- One pinned dependency enters the build (`v2.0.0-rc`) plus its runtime deps
  `jsontools` and `coder/websocket`; licences are recorded above and in the PR
  description per section 9.2 record-keeping.
- The gateway seam stays authoritative: swapping or shimming the wire client
  later touches only the adapter behind `RealtimeTransport`/`RealtimeSession`,
  not call sites.
- Lifecycle policy (reconnect, cancel/truncate semantics, barge-in) and
  audio decode/RMS validation remain gateway code; the library deliberately
  leaves them to callers.
- The experimental evaluation client that produced earlier evidence on this
  lane is deleted; the probe module was never committed. Future live checks can
  rebuild a probe from this record in minutes.
