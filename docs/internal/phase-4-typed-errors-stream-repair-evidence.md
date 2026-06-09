# Phase 4 Typed Errors And Stream Repair Evidence

This is the final reviewer-facing evidence record for
`phase-4-typed-errors-stream-repair`. It maps the repaired representative
behavior back to `P4-API-02`, `P4-API-03`, `P4-API-05`, and `P4-GATE-01` and
names deterministic commands reviewers can run without live provider
credentials.

## Evidence Summary

| Area | Evidence | Audit rows |
| --- | --- | --- |
| Provider rejection | OpenAI and fal provider tests assert `errors.Is` against public provider rejection classes and `errors.As` for provider details while preserving readable messages. | `P4-API-02`, `P4-API-05`, `P4-GATE-01` |
| Local validation and unsupported requests | fal validation tests assert invalid or unsupported request classes and structured `ValidationError` details such as provider, feature, requested value, supported value, and detail. | `P4-API-02`, `P4-API-05`, `P4-GATE-01` |
| Direct streaming provider-backed failures | OpenAI and Gemini stream tests assert returned setup errors or emitted `ERROR` events carry the public taxonomy through `errors.Is` or `messages.ErrorValue.Classification`. | `P4-API-02`, `P4-API-05`, `P4-GATE-01` |
| Direct streaming gateway/runtime failures | OpenAI stream reader/runtime tests assert mid-stream failures produce readable `ERROR` events with structured transport classification. | `P4-API-02`, `P4-API-03`, `P4-API-05` |
| Session failures | Session inferencer tests assert typed session connect errors pass through unchanged so callers classify with `errors.Is` and inspect details with `errors.As`. | `P4-API-02`, `P4-API-05` |
| Interaction event failures | Gateway, bridge, and loop subsystem tests assert interaction error events preserve `Classification` fields for provider, runtime, validation, and unsupported request behavior. | `P4-API-02`, `P4-API-03`, `P4-API-05` |
| Cancellation | Gateway and bridge tests assert caller cancellation carries cancellation classification and does not match provider rejection, transport, validation, or replay mismatch classes. | `P4-API-02`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` |
| Replay mismatch | Session replayer and replay WebSocket dialer tests assert divergence and incomplete replay wrap `providers.ErrReplayMismatch` without relying on error text. | `P4-API-02`, `P4-API-03`, `P4-API-05` |
| Partial output | Gateway and bridge tests assert interrupted interaction output exposes a partial-output terminal state through structured event fields rather than inferred missing completion. | `P4-API-03`, `P4-API-05`, `P4-GATE-01` |
| Public documentation | `go-llm-gateway/README.md` documents when callers should use `errors.Is`, `errors.As`, `messages.ErrorValue.Classification`, interaction `Classification`, and cancellation `OutputState`. | `P4-API-02`, `P4-API-05`, `P4-GATE-01` |

## Terminal Contract Addendum

The `phase-4-typed-errors-stream-terminal-contract` repair extends this evidence
with an explicit public terminal taxonomy for representative direct stream,
provider, loop, session, replay, cancellation, partial-output, CLI, and terminal
failure paths.

| Area | Evidence | Audit rows |
| --- | --- | --- |
| Public terminal taxonomy | `docs/architecture/stream-terminal-contract.md` names terminal reasons for provider-authored completion, loop-synthesized completion, cancellation, replay divergence, replay incomplete, session close, partial output, provider close, and terminal failure, plus caller actions for each category. | `P4-API-02`, `P4-API-03`, `P4-API-05` |
| Serialized direct stream fields | `go-llm-gateway/README.md` documents additive `classification`, `terminal_reason`, `terminal_provenance`, and `output_state` fields on `ERROR`, `MESSAGE.END`, and `SESSION.CLOSE` payloads, while preserving readable `message`, `reason`, and `err.Error()` text. | `P4-API-02`, `P4-API-05`, `P4-GATE-01` |
| Returned versus in-band failures | `go-llm-gateway/README.md` and `docs/architecture/stream-terminal-contract.md` state that setup, validation, replay, and provider-open failures return Go errors, while active stream/session terminal outcomes are emitted in-band when that surface is already active. | `P4-API-02`, `P4-API-03`, `P4-API-05` |
| Audit reconciliation | `docs/architecture/contract-gap-audit.md` maps the representative terminal-contract evidence to `P4-API-02`, `P4-API-03`, `P4-API-05`, and `P4-GATE-01` without closing provider-wide or helper-wide follow-up rows outside this selected repair scope. | `P4-GATE-01` |

## Reviewer Commands

Run these focused commands to prove the repaired representative behavior:

```sh
(cd go-llm-gateway && go test ./pkg/providers/openai ./pkg/providers/fal -run 'TestReplay_Error|TestFalProvider_Infer_InvalidRequests|TestFalProvider_Infer_HTTPError|TestFalProvider_Infer_QwenTTS_HTTPError|TestFalProvider_Infer_QwenTTS_MissingEmbedding|TestFalProvider_Infer_GrokImagineVideo_MissingImage|TestFalProvider_Infer_GrokImagineVideo_HTTPError|TestFalProvider_Infer_KlingVideoV3_MissingImage|TestFalProvider_Infer_KlingVideoV3_HTTPError|TestOpenAIProvider_InferStream_HTTPErrorClassification' -timeout 120s)
(cd go-llm-gateway && go test ./pkg/providers/openai ./pkg/providers/gemini ./pkg/providers/anthropic -run 'TestStreamSSEToGateway_ReaderErrorClassification|TestOpenAIProvider_InferStream_HTTPErrorClassification|TestStreamGeminiToGateway_ProviderErrorClassification|TestStreamGeminiToGateway_ErrorBeforeMessageEnd|TestStreamAnthropicToGateway_ErrorBeforeMessageEnd|TestStreamSSEToGateway_EmitsMessageStartAndTextEvents|TestStreamGeminiToGateway_TextStream|TestStreamAnthropicToGateway_EmitsMessageStartAndTextEvents' -timeout 120s)
(cd go-llm-gateway && go test ./pkg/gateway ./pkg/inference ./pkg/providers -run 'TestInteract_NormalizesProviderError|TestInteract_EmitsCancellationWhenContextCancelledBeforeProviderReturns|TestInteract_PreservesPartialOutputBeforeCancellation|TestInteract_NormalizesDeadlineExceededAsTimeoutError|TestInteract_RejectsInvalidToolResultsBeforeProviderContinuation|TestLoopInteractionEventFromGateway|TestSessionGatewayInferencer_ConnectSessionErrorClassification|TestErrorClassification_DistinguishesRuntimeOutcomes' -timeout 120s)
(cd go-llm-gateway && go test ./pkg/testing -run 'TestSessionReplayer_FailsOnUnexpectedOutboundEvent|TestSessionReplayer_FailsWhenExpectedOutboundIsOmitted|TestReplayWebSocketDialer_FailsOnUnexpectedOutbound|TestReplayWebSocketDialer_ReportsIncompleteExpectedOutboundOnClose' -timeout 120s)
(cd go-agent-loop && go test ./pkg/subsystems -run 'TestInteractionEvents_RecordsTerminalErrorAndCancellation|TestInteractionEvents_TracksStateAndOutputs' -timeout 120s)
```

Run the root quality gate before review:

```sh
make typecheck
make test
make lint
```

## Remaining Follow-Up Scope

The Phase 4 repair closes the representative behavior required for this PRD,
but it does not claim provider-wide parity across every adapter, status code,
parser failure, replay entrypoint, or final stream status surface. The narrower
remaining scopes are:

- `P4-ERR-01`: provider-family parity for status code classes not represented
  by the credential-free OpenAI and fal tests.
- `P4-ERR-02`: stream error event parity for every adapter and parser failure
  shape not covered by the representative OpenAI, Gemini, and Anthropic tests.
- `P4-ERR-03`: replay mismatch parity across every session `Send`, `Close`,
  `Err`, WebSocket read/write, and fixture decode entrypoint.
- `P4-STREAM-01`: a broader final-status stream handle only if callers need one
  final error accessor across direct provider, gateway, and loop stream APIs.
- `P4-STREAM-02`: provider-wide event-ordering parity across clean text, tool
  calls, parser errors, empty streams, usage-bearing completions, and
  unsupported streaming.
- `P4-STREAM-03`: a shared session terminal status contract across all provider
  close, caller close, timeout, transport failure, provider error, replay
  divergence, and replay completion paths.

## Verdict

`P4-API-02`, `P4-API-03`, `P4-API-05`, and `P4-GATE-01` pass for the
representative typed-error and stream repair scope selected in
`docs/internal/phase-4-typed-errors-stream-repair-scope.md` once the reviewer
commands and root quality gate pass on the reviewed head.
