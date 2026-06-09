# Phase 4 Typed Terminal Authoritative Reconciliation

This note records the story-001 disposition for
`phase-4-typed-terminal-authoritative-reconciliation`. It is reviewer evidence
for the branch relationship only; it does not close `P4-GATE-01` and it does
not by itself close the typed-terminal repair rows.

## Reconciliation Baseline

Evidence captured after fetching `origin` on 2026-06-09 00:00 UTC:

| Ref | Commit | Role |
| --- | --- | --- |
| `origin/main` | `3510ed599cd1c86e12fef6ae9b69a4a9c7d9feaf` | Current authoritative baseline used for this reconciliation worktree. |
| `origin/phase-4-authoritative-baseline-sync` | `a472b53f83efc3039aa5716eba203a1c8c50a917` | Baseline-sync evidence branch merged into `origin/main` by PR `#39`. |
| `origin/phase-4-typed-errors-stream-terminal-contract` | `b50b6219f22c16ef97649842cb665eb8aec16d8f` | Completed typed-terminal work branch. |
| Current worktree head before story-001 evidence | `3510ed599cd1c86e12fef6ae9b69a4a9c7d9feaf` | Fast-forwarded to `origin/main` before adding this note. |

The relationship is:

- `origin/phase-4-typed-errors-stream-terminal-contract` is an ancestor of
  `origin/main`.
- `origin/phase-4-authoritative-baseline-sync` is an ancestor of `origin/main`.
- The older local worktree head `34fc154e4e519cde4b35a91e5d10dd184a76ea71`
  was an ancestor of `origin/main` and was fast-forwarded before this evidence
  was written.

Reviewer commands:

```sh
git rev-parse origin/main origin/phase-4-authoritative-baseline-sync origin/phase-4-typed-errors-stream-terminal-contract
git merge-base --is-ancestor origin/phase-4-typed-errors-stream-terminal-contract origin/main
git merge-base --is-ancestor origin/phase-4-authoritative-baseline-sync origin/main
git log --oneline --grep='Merge pull request #3[7-9]' origin/main
```

The expected result is that both `merge-base --is-ancestor` commands exit `0`,
and the log includes PR `#37` for the typed-terminal branch, PR `#38` for the
convergence validator, and PR `#39` for the authoritative baseline sync.

## Disposition

Disposition: `landed`.

The standalone `phase-4-typed-errors-stream-terminal-contract` branch is no
longer an unconsumed or ambiguous completed branch. It was landed into
`origin/main` through PR `#37` and then preserved by the authoritative baseline
sync merged through PR `#39`. Future work should use `origin/main` at
`3510ed599cd1c86e12fef6ae9b69a4a9c7d9feaf` or a descendant, not the old
standalone branch head, because the standalone head does not contain later
validator and baseline-sync evidence.

This disposition intentionally differs from a request to cherry-pick or
re-land the old branch. Replaying branch head
`b50b6219f22c16ef97649842cb665eb8aec16d8f` would risk dropping later
authoritative evidence. The compatible resolution is to preserve the landed
merge ancestry and reconcile any remaining typed-terminal gaps on top of the
current authoritative baseline.

## Drift And Remaining Repair Scope

No merge conflict remains for story 001 because the typed-terminal branch is
already in the ancestry of `origin/main`. Semantic drift still matters for
later stories:

- `docs/internal/phase-4-authoritative-baseline-sync.md` names the typed
  terminal branch as landed and superseded as a standalone planning input.
- `docs/internal/phase-4-api-contract-validator.md` still keeps
  `P4-API-02`, `P4-API-03`, and `P4-API-05` open or uncertain where coverage
  is representative rather than exhaustive.
- `docs/architecture/stream-terminal-contract.md` is the landed taxonomy
  reference for terminal reason, provenance, output state, and classification
  fields.
- `docs/internal/phase-4-typed-errors-stream-repair-evidence.md` is the
  landed representative repair evidence, not a final closure of all
  provider/session/direct-stream parity gaps.

The remaining reconciliation stories should therefore preserve the landed
taxonomy and tests, then narrow or repair the still-open typed error, stream,
replay, cancellation, partial-output, CLI, session, docs, and reviewer-command
gaps on top of `origin/main`.

## Baseline Contract Preservation

Story 002 preserves the authoritative baseline contracts instead of replaying
or replacing them with typed-terminal-specific variants.

Provider capability and local-validation behavior remain the public baseline
for request feasibility:

- `go-llm-gateway/pkg/capabilities` remains the provider-neutral capability
  vocabulary with `supported`, `unsupported`, and `unknown` states.
- `go-llm-gateway/pkg/gateway.DefaultGateway.Capabilities()` and
  `DefaultSessionGateway.Capabilities()` still delegate to provider reporters
  without executing inference or connecting sessions.
- Unsupported stateless and session features still fail locally with
  `UnsupportedFeatureError` before provider execution. Unknown capabilities
  still allow execution without claiming support.
- `go-llm-gateway/pkg/gateway/gateway_test.go` now proves direct stream typed
  terminal error normalization is additive: terminal classification fields are
  populated on the error event, while the provider capability states remain
  discoverable and unchanged.

Dependency, result, context, and lifecycle contracts remain governed by the
authoritative baseline evidence rather than by this typed-terminal branch
disposition note:

- `docs/internal/phase-4-dependency-result-context-lifecycle-contract.md`
  remains the reviewer map for `FinalText()`, `Stream.Outcome()`,
  `TypedBuffer` context reads/writes, typed session send outcomes, replay
  outcomes, prompt-resolution observability, and CLI/session lifecycle states.
- `docs/architecture/dependency-result-contracts.md` remains the public
  migration guide for additive result/lifecycle surfaces and compatible legacy
  helpers.
- `docs/architecture/contract-gap-audit.md` remains the row-level Phase 4
  source of truth. This story preserves the existing provider capability and
  dependency/result/lifecycle evidence; it does not broaden into provider
  capability matrix completion or close `P4-GATE-01`.

Reviewer commands:

```sh
go test ./go-llm-gateway/pkg/gateway -run 'TestGatewayCapabilities|TestSessionGatewayCapabilities|TestGatewayRejectsUnsupported|TestSessionGatewayRejectsUnsupported|TestInferStream_TerminalErrorNormalizationPreservesProviderCapabilities'
go test ./go-agent-loop/pkg/agentloop ./go-agent-loop/pkg/messages
go test ./go-llm-gateway/pkg/testing ./go-llm-gateway/pkg/inference
make typecheck
make test
```

## Public Typed Error And Direct Stream Reconciliation

Story 003 reconciles the public typed error taxonomy and direct stream terminal
fields on top of the landed authoritative baseline. The disposition remains
`landed`; this section narrows the reviewer-visible proof for the direct
gateway/provider stream surface without claiming replay, session, or CLI parity
from later stories.

Returned representative gateway and provider failures expose stable
caller-actionable classes:

- `go-llm-gateway/pkg/gateway/errors.go` exports gateway sentinels including
  provider HTTP status, authentication, rate limit, invalid request,
  unsupported model, transport, cancellation, replay mismatch, and replay
  incomplete classes.
- `go-llm-gateway/pkg/gateway.ProviderHTTPStatusError` and
  `TransportError` preserve inspectable public details for `errors.As`.
- `go-llm-gateway/pkg/providers/errors.go` exports provider sentinels and
  classification strings including provider rejection, authentication, rate
  limit, invalid request, unsupported request, transport, cancellation, replay
  mismatch, replay incomplete, partial output, and unknown.
- Human-readable `Error()` text remains available for operators while callers
  branch on `errors.Is`, `errors.As`, or serialized classification fields.

Direct gateway stream `ERROR` payloads expose machine-readable terminal fields
after passing through `DefaultGateway.InferStream`:

- `ErrorValue.classification` carries the public gateway classification such as
  `authentication` or `transport`.
- `ErrorValue.terminal_reason` is populated with `terminal_failure` for
  representative non-cancellation direct stream failures.
- `ErrorValue.terminal_provenance` is populated with `gateway` when gateway
  normalization authors the terminal metadata.
- `ErrorValue.output_state` is populated with `none` for representative
  failures that emitted no usable output before the terminal event.
- `ErrorValue.message` remains readable, and the in-process `ErrorValue.Err`
  keeps the typed Go error for `errors.Is`/`errors.As` callers.

Credential-free evidence:

```sh
go test ./go-llm-gateway/pkg/gateway -run 'TestInfer_PreservesProviderHTTPStatusClassification|TestInfer_PreservesTransportClassification|TestInferStream_PreservesErrorEventClassification|TestInferStream_ErrorEventClassificationIsPublicAndSerializable|TestInferStream_PreservesRuntimeErrorEventClassification'
go test ./go-llm-gateway/pkg/providers -run 'TestErrorClassification_DistinguishesRuntimeOutcomes|TestNewStreamErrorValue_PreservesTypedErrorAndTerminalClassification|TestNewStreamTransportErrorValue_PreservesRuntimeClassification'
go test ./go-agent-loop/pkg/messages -run 'TestTerminalMetadataSerializesOn(ErrorValue|MessageEndValue|SessionCloseValue)|TestLegacyTerminalPayloadsOmitEmptyTerminalMetadata'
make typecheck
make test
```

The evidence above closes the story-003 direct stream and returned-error
contract. Replay/cancellation/partial-output distinction, CLI/session parity,
docs/audit final alignment, and final reviewer validation remain assigned to
stories 004 through 007. This story still does not close `P4-GATE-01`.

## Replay, Cancellation, And Partial Output Reconciliation

Story 004 reconciles replay, cancellation, and partial-output evidence on the
authoritative baseline without changing provider capability or provider
transport contracts.

Replay outcomes are distinguishable through public replay results and typed Go
errors:

- `SessionReplayOutcome.Status=diverged` identifies outbound replay divergence.
  The error matches `gateway.ErrReplayMismatch` and
  `providers.ErrReplayMismatch`, exposes mismatch details through
  `gateway.ReplayMismatchError`, and does not match replay incomplete,
  provider HTTP status, or transport classes.
- `SessionReplayOutcome.Status=incomplete` identifies replay closure before a
  required fixture event was consumed. The error matches
  `gateway.ErrReplayIncomplete` and `providers.ErrReplayIncomplete`, exposes
  missing-event details through `gateway.ReplayIncompleteError`, and does not
  match replay mismatch, provider HTTP status, or transport classes.
- `SessionReplayOutcome.Status=cancelled` identifies caller-owned replay
  context cancellation. The error preserves `context.Canceled`, classifies as
  `cancellation` through provider taxonomy helpers, and does not match replay
  mismatch, replay incomplete, provider rejection, provider HTTP status, or
  transport classes.

Partial output remains available separately from the terminal cause:

- `ExecuteResult.FinalText()` reports `FinalTextCanceled` or
  `FinalTextFailed` and sets `Partial=true` with the accumulated text deltas
  when output was emitted before cancellation or terminal failure.
- `Stream.Outcome()` reports `StreamCanceled` or `StreamFailed` and sets
  `Partial=true` when at least one event was delivered before the terminal
  event.
- Loop stream `ERROR` events can carry `classification`, `terminal_reason`,
  `terminal_provenance`, and `output_state=partial`; `Stream.Outcome()` now
  preserves the typed in-process `ErrorValue.Err` when present so cancellation
  after partial output remains a cancellation rather than a string-only
  terminal failure.

Credential-free evidence:

```sh
go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|FailsWhenExpectedOutboundIsOmitted|CancellationWakesExpectedOutboundWait)'
go test ./go-agent-loop/pkg/agentloop -run 'TestExecuteResultFinalText_CanceledWithPartialText|TestStreamOutcome_(FailedWithPartialOutput|Canceled|CanceledWithPartialOutputPreservesTerminalMetadata)'
go test ./go-llm-gateway/pkg/providers -run 'TestErrorClassification_DistinguishesRuntimeOutcomes'
make typecheck
make test
```

The evidence above closes the story-004 replay, cancellation, and
partial-output distinction. CLI/session parity, docs/audit final alignment, and
final reviewer validation remain assigned to stories 005 through 007. This
story still does not close `P4-GATE-01`.

## Gate Status

This story does not close `P4-GATE-01`. The gate remains governed by the
current validator and audit evidence, especially
`docs/internal/phase-4-api-contract-validator.md` and
`docs/architecture/contract-gap-audit.md`.
