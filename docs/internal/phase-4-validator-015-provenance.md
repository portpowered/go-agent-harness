# Phase 4 Validator-015 Provenance

This note records how the missing `validator-015` evidence is handled for the
Phase 4 API contract audit reconciliation.

## Decision

`validator-015` is explicitly superseded for this reconciliation lane. Its
findings are not restored as a standalone artifact because the original
validator-015 evidence is not present in this worktree. The replacement
evidence for reviewer comparison is:

- `docs/internal/phase-4-api-contract-validator.md`
- `docs/architecture/contract-gap-audit.md`

The current Phase 4 validator artifact replaces validator-015 as the row-level
closure authority for `P4-API-01` through `P4-API-07` and `P4-GATE-01`. The
architecture audit then reconciles that validator evidence with current public
declarations, deterministic tests, README guidance, and the remaining repair
slices.

## Effect On Findings

Validator-015 findings should be treated as superseded, not silently dropped.
Where the current validator and audit name the same issue class, the current
row-level evidence replaces validator-015 provenance. Where the original
validator-015 finding cannot be compared directly, the reconciled audit keeps
the row `fail`, `uncertain`, or `open` rather than inferring closure.

This provenance note does not close any Phase 4 implementation checklist row by
itself. Closure still requires implementation evidence plus validator evidence.
After the provider capability/local validation repair, `P4-GATE-01` passes for
that repair lane only; whole-Phase-4 gate closure remains open until the other
repair lanes converge.

## Row Mapping

| Checklist row | Replacement evidence | Provenance result |
| --- | --- | --- |
| `P4-API-01` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-01-dependency-ownership-and-injection-contracts` | Superseded by current dependency, context, timeout, and cancellation evidence; not closed from provenance. |
| `P4-API-02` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-02-typed-errors-and-stream-failure-contracts` | Superseded by current typed-error and stream-failure evidence; remaining taxonomy and preservation gaps stay open. |
| `P4-API-03` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-03-result-lifecycle-and-completion-contracts` | Superseded by current result and lifecycle evidence; terminal authority remains unresolved. |
| `P4-API-04` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-04-provider-capability-discovery-and-unsupported-feature-validation` | Superseded by current capability evidence, including supported, unsupported, and unknown states. Provider capability discovery passes for the provider capability/local validation repair lane. |
| `P4-API-05` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-05-public-gateway-provider-and-session-surface-alignment` | Superseded by current gateway, provider, model, and session ownership evidence; compatibility ownership remains uncertain. |
| `P4-API-06` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-06-context-cancellation-timeout-and-retry-semantics` | Superseded by current context, cancellation, timeout, retry, and validation evidence. Local unsupported-feature validation passes for the provider capability/local validation repair lane; broader context, timeout, and retry ownership remain open outside that lane. |
| `P4-API-07` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-api-07-go-api-hygiene-documentation-and-compatibility-staging` | Superseded by current public API hygiene and compatibility evidence; staged ownership work remains open. |
| `P4-GATE-01` | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md#p4-gate-01-phase-4-closure-gate` | Superseded by the current final-gate decision. Passes for provider capability/local validation; explicitly remains open for whole-Phase-4 closure. |
