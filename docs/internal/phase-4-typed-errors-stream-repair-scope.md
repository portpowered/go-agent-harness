# Phase 4 Typed Errors And Stream Repair Scope

This file is the story-001 scope record for
`phase-4-typed-errors-stream-repair`. It consumes the reconciled Phase 4 audit
and validator findings and narrows the remaining `P4-ERR-*` and
`P4-STREAM-*` work into representative repair paths for this batch.

The goal of this batch is representative typed error and stream semantics
preservation. It is not a broad provider capability, provider behavior, or
coverage-percentage project.

## Evidence Inputs

- Reconciled audit row `P4-API-02`: typed errors and stream failure contracts.
- Reconciled audit row `P4-API-03`: result, lifecycle, and completion
  contracts.
- Reconciled audit row `P4-API-05`: public gateway, provider, and session
  surface alignment.
- Reconciled gate row `P4-GATE-01`: final Phase 4 closure remains open until
  implementation, docs, examples, and deterministic validation converge.
- Stale typed-error claims: `P4-ERR-01`, `P4-ERR-02`, and `P4-ERR-03`.
- Stale stream claims: `P4-STREAM-01`, `P4-STREAM-02`, and `P4-STREAM-03`.

## Selected Representative Repair Paths

| Path | Primary public surface | Audit mapping | Repair target for this batch |
| --- | --- | --- | --- |
| Provider rejection | `providers.Provider.Infer`, `providers.Provider.InferStream`, `gateway.Gateway.Infer`, `gateway.Gateway.InferStream`, `inference.GatewayInferencer` | `P4-ERR-01`, `P4-API-02`, `P4-API-03`, `P4-GATE-01` | Preserve caller-actionable provider rejection classes with `errors.Is` or `errors.As`, including provider/status/detail fields where available. |
| Local validation and unsupported requests | gateway/provider request validation and provider unsupported-feature paths | `P4-ERR-01`, `P4-API-02`, `P4-API-03`, `P4-GATE-01` | Preserve unsupported or invalid request classification and expose provider, feature, requested mode, or capability state where the public contract supports it. |
| Direct streaming provider-backed failure | `Provider.InferStream`, `Gateway.InferStream`, `messages.StreamMessage`, `messages.ErrorValue` | `P4-ERR-02`, `P4-STREAM-01`, `P4-STREAM-02`, `P4-API-02`, `P4-API-05`, `P4-GATE-01` | Emit or return classified stream failures so callers can map stream failures to the same taxonomy used by returned Go errors without parsing event text. |
| Direct streaming gateway/runtime failure | loop/gateway streaming bridges and `messages.StreamTypeError` | `P4-ERR-02`, `P4-STREAM-01`, `P4-API-02`, `P4-API-03`, `P4-API-05` | Preserve runtime or bridge error classification when converting failures into `ERROR` stream events. |
| Session failure | `messages.Session`, `Session.Done`, `Session.Close`, `SessionCloseValue`, session providers, recorder/replayer sessions | `P4-ERR-02`, `P4-ERR-03`, `P4-STREAM-03`, `P4-API-02`, `P4-API-03`, `P4-API-05` | Expose typed returned errors or structured terminal status for representative session provider errors, close reasons, replay failures, and cancellation. |
| Interaction event failure | `gateway.InteractionEvent`, `gateway.InteractionError`, `messages.InteractionEvent`, `messages.InteractionError` | `P4-ERR-02`, `P4-API-02`, `P4-API-03`, `P4-API-05` | Add or preserve machine-readable classification on representative interaction error events while keeping readable event text. |
| Caller cancellation | `context.Canceled` through gateway, stream, session, replay, and interaction surfaces | `P4-API-02`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` | Keep cancellation distinguishable from provider rejection, transport failure, validation failure, and replay mismatch. |
| Replay mismatch | `go-llm-gateway/pkg/testing.SessionReplayer`, `ReplayWebSocketDialer`, interaction/session fixture validation | `P4-ERR-03`, `P4-STREAM-03`, `P4-API-02`, `P4-API-03`, `P4-API-05` | Expose replay mismatch and fixture validation details through typed errors with `errors.As`, including sequence/path/expected/actual details where available. |
| Partial output | `agentloop.Stream`, `messages.StreamMessage`, PNIG interaction events, reconstructed messages | `P4-STREAM-01`, `P4-STREAM-03`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` | Document and test a representative terminal state or structured field that distinguishes partial output from clean completion and total failure. |

## Stale Claims Narrowed By This Batch

### `P4-ERR-01`

This batch repairs representative provider rejection and local validation paths
instead of attempting full provider-family parity. The remaining follow-up
scope after this batch is concrete provider coverage for any provider/status
code class not represented by the credential-free tests.

### `P4-ERR-02`

This batch repairs representative direct stream, gateway/runtime stream,
session, and interaction event paths. The remaining follow-up scope after this
batch is provider-wide stream event parity for every adapter and every parser
failure shape not covered by representative tests.

### `P4-ERR-03`

This batch repairs representative replay mismatch and fixture validation
classification. The remaining follow-up scope after this batch is parity across
all replay entrypoints where mismatch can surface through `Send`, `Close`,
`Err`, WebSocket reads/writes, or fixture decode helpers.

### `P4-STREAM-01`

This batch documents and tests representative terminal behavior for clean
completion, late stream error, cancellation, and partial output. The remaining
follow-up scope is a broader stream handle or final-status design if callers
need one final error accessor across direct provider, gateway, and loop stream
APIs.

### `P4-STREAM-02`

This batch may touch provider stream ordering only where a representative
typed error path requires it. The remaining follow-up scope is provider-wide
event-ordering parity for OpenAI, Anthropic, Gemini, and fal across clean text,
tool calls, parser errors, empty streams, usage-bearing completions, and
unsupported streaming.

### `P4-STREAM-03`

This batch repairs representative session terminal status and cancellation
classification. The remaining follow-up scope is a shared session terminal
status contract across all provider close, caller close, timeout, transport
failure, provider error, replay divergence, and replay completion paths.

## Explicit Non-Goals For This Batch

- Do not change provider capability reporting except where an unsupported
  stream or request error class is required by the typed error contract.
- Do not change fal streaming behavior except where the representative
  unsupported-stream error class must be exposed.
- Do not broadly rewrite all provider stream event ordering.
- Do not replace the current channel-based stream APIs with a new stream handle
  unless later stories prove a narrower additive final-status field is
  insufficient.
- Do not close `P4-GATE-01` from this scope record alone; the gate remains open
  until implementation, docs, and deterministic validation evidence converge.

## Reviewer Commands

Story 001 is scope reconciliation only. The reviewer-runnable validation for
this story is:

```sh
make typecheck
```

Later stories add the credential-free behavioral commands for provider,
validation, direct streaming, session, interaction, cancellation, replay
mismatch, and partial-output classification.
