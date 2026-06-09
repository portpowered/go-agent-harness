# Phase 4 Authoritative Convergence Validator Rerun

This report reruns the Phase 4 public API contract convergence review from the
current authoritative baseline. It is intentionally incremental while the PRD
stories are being completed: this section establishes the branch, commit, and
evidence disposition that later row verdict sections must use.

## Authoritative Review Baseline

Branch under review:
`phase-4-authoritative-convergence-validator-rerun`.

Commit under review for story 001:
`8633cb6768623fd7342d7a19d9dbf2e260f4e95f`.

That commit is the current `origin/main` head after PR `#40`, and this work
branch was fast-forwarded to it before this report was written. The head
includes the authoritative Phase 4 baseline from
`phase-4-authoritative-baseline-sync` because
`origin/phase-4-authoritative-baseline-sync` is an ancestor of this head.

The head also includes the typed-terminal reconciliation decision from
`phase-4-typed-terminal-authoritative-reconciliation` because
`origin/phase-4-typed-terminal-authoritative-reconciliation` is an ancestor of
this head.

Reviewer commands:

```sh
git rev-parse HEAD origin/main origin/phase-4-authoritative-baseline-sync origin/phase-4-typed-terminal-authoritative-reconciliation
git merge-base --is-ancestor origin/phase-4-authoritative-baseline-sync HEAD
git merge-base --is-ancestor origin/phase-4-typed-terminal-authoritative-reconciliation HEAD
git log --oneline --grep='Merge pull request #3[7-9]\|Merge pull request #40' origin/main
```

Expected result:

- `HEAD` and `origin/main` resolve to
  `8633cb6768623fd7342d7a19d9dbf2e260f4e95f` for this story-001 evidence.
- Both `merge-base --is-ancestor` commands exit `0`.
- The log includes PR `#37` for
  `phase-4-typed-errors-stream-terminal-contract`, PR `#38` for
  `phase-4-api-contract-convergence-validator`, PR `#39` for
  `phase-4-authoritative-baseline-sync`, and PR `#40` for
  `phase-4-typed-terminal-authoritative-reconciliation`.

## Consumed Baseline Decision

The consumed authoritative baseline decision is:

- Use `origin/main` at
  `6e785952affad9cc5d07458c84f9a45b755c72c0` or a descendant that preserves
  that commit in ancestry as the Phase 4 baseline after batch 017.
- The current review head is such a descendant.
- Older local snapshots and pre-merge factory branches are not authoritative
  when they omit, predate, or are superseded by landed remote evidence.
- The baseline sync does not close `P4-API-01` through `P4-API-07` or
  `P4-GATE-01`; it only publishes branch-status and preservation evidence for
  future validator work.

Primary source:
`docs/internal/phase-4-authoritative-baseline-sync.md`.

## Consumed Typed-Terminal Reconciliation Decision

The consumed typed-terminal reconciliation decision is:

- Disposition: `landed`.
- The standalone
  `phase-4-typed-errors-stream-terminal-contract` branch is superseded as a
  planning base because it was landed through PR `#37` and later preserved by
  PRs `#39` and `#40`.
- Future Phase 4 validation and repair work should use `origin/main` or a
  descendant, not the old standalone typed-terminal branch head.
- The typed-terminal reconciliation contributes representative evidence for
  typed errors, stream terminal fields, replay/cancellation/partial-output
  outcomes, CLI/session terminal surfaces, docs alignment, and quality gates,
  but it explicitly does not close whole-Phase-4 `P4-GATE-01`.

Primary source:
`docs/internal/phase-4-typed-terminal-authoritative-reconciliation.md`.

## Prior Evidence Reconciliation

This rerun treats prior evidence as follows:

| Prior evidence source | Current disposition for this rerun |
| --- | --- |
| `phase-4-api-contract-convergence-validator` | Advisory unless matched against this authoritative head. Its previously risky stale-branch status was superseded when PR `#38` landed on `origin/main`, and its durable artifact remains `docs/internal/phase-4-api-contract-validator.md`. Later row verdicts must reconcile that artifact against the current head instead of copying its conclusions blindly. |
| Batch 017 evidence | Authoritative when it is present in current ancestry. PRs `#33` through `#36` landed the repair validator, audit reconciliation, provider capability/local validation contract, and dependency/result/context/lifecycle contract evidence. This head preserves those merges through `origin/main`. |
| `phase-4-typed-errors-stream-terminal-contract` | Advisory as a standalone branch head. Its implementation evidence landed through PR `#37`, then the typed-terminal authoritative reconciliation landed through PR `#40`; use current-head docs/tests instead of the old branch as a base. |
| `origin/main` | Authoritative for this rerun at `8633cb6768623fd7342d7a19d9dbf2e260f4e95f`, because it is the fetched remote head containing PRs `#33` through `#40`. |

Stale branch evidence is advisory only unless the same claim is reproducible on
the current authoritative baseline through public files, exported declarations,
deterministic tests, reviewer commands, or landed docs. CI success alone is not
row closure evidence.

## Story 001 Closure

Story `phase-4-authoritative-convergence-validator-rerun-001` may close for
this PRD iteration because the report names the branch and commit under review,
records the authoritative baseline and typed-terminal reconciliation decisions,
and explains how the prior validator, batch 017, typed-terminal, and
`origin/main` evidence are reconciled against the current baseline.

This story does not provide row verdicts for `P4-API-01` through `P4-API-07` or
`P4-GATE-01`. Those remain assigned to later PRD stories.
