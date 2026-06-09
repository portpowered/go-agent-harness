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

## Gate Status

This story does not close `P4-GATE-01`. The gate remains governed by the
current validator and audit evidence, especially
`docs/internal/phase-4-api-contract-validator.md` and
`docs/architecture/contract-gap-audit.md`.
